package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/konono/sokki/internal/ipc"
	"github.com/konono/sokki/internal/session"
)

// serve は stdio のやりとりを模擬して応答を集める。
func serve(t *testing.T, s *Server, requests ...string) []map[string]any {
	t.Helper()
	in := strings.NewReader(strings.Join(requests, "\n") + "\n")
	var out bytes.Buffer
	if err := s.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var got []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("応答を解釈できない: %q (%v)", line, err)
		}
		got = append(got, m)
	}
	return got
}

func TestInitializeHandshake(t *testing.T) {
	useStateDir(t)
	got := serve(t, New(&mockLauncher{}),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if len(got) != 1 {
		t.Fatalf("応答数 = %d", len(got))
	}
	res := got[0]["result"].(map[string]any)
	if res["protocolVersion"] == nil {
		t.Errorf("protocolVersion が無い: %+v", res)
	}
	info := res["serverInfo"].(map[string]any)
	if info["name"] != "sokki" {
		t.Errorf("serverInfo = %+v", info)
	}
}

func TestToolsListReturnsAllTools(t *testing.T) {
	useStateDir(t)
	got := serve(t, New(&mockLauncher{}),
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	res := got[0]["result"].(map[string]any)
	tools := res["tools"].([]any)
	if len(tools) != 6 {
		t.Fatalf("ツール数 = %d, want 6", len(tools))
	}
}

// 通知 (id 無し) には応答しないこと。
func TestNotificationGetsNoResponse(t *testing.T) {
	useStateDir(t)
	got := serve(t, New(&mockLauncher{}),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if len(got) != 0 {
		t.Fatalf("通知に応答した: %+v", got)
	}
}

func TestUnknownMethodReturnsError(t *testing.T) {
	useStateDir(t)
	got := serve(t, New(&mockLauncher{}),
		`{"jsonrpc":"2.0","id":1,"method":"nope"}`)
	if got[0]["error"] == nil {
		t.Fatalf("未知メソッドがエラーにならない: %+v", got[0])
	}
}

// 壊れた行があっても接続を落とさず、後続を処理すること。
func TestMalformedLineDoesNotKillConnection(t *testing.T) {
	useStateDir(t)
	got := serve(t, New(&mockLauncher{}),
		`{ broken`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if len(got) != 1 || got[0]["result"] == nil {
		t.Fatalf("壊れた行の後に処理できていない: %+v", got)
	}
}

// ツールの失敗は JSON-RPC エラーではなく isError で返すこと。
func TestToolFailureUsesIsError(t *testing.T) {
	useStateDir(t)
	s := New(&mockLauncher{available: fmt.Errorf("panecom を利用できません")})
	got := serve(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"start_or_attach","arguments":{}}}`)
	res := got[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("isError が立っていない: %+v", res)
	}
	content := res["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "panecom") {
		t.Errorf("理由が含まれない: %s", text)
	}
}

// tools/call の結果に構造化データが含まれること。
func TestToolCallReturnsStructuredContent(t *testing.T) {
	useStateDir(t)
	liveSession(t, &fakeRecorder{})
	got := serve(t, New(&mockLauncher{}),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_status","arguments":{}}}`)
	res := got[0]["result"].(map[string]any)
	sc, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("structuredContent が無い: %+v", res)
	}
	if sc["id"] != "m1" {
		t.Errorf("会議 ID = %v", sc["id"])
	}
}

// 重要: AI (MCP) の接続が終了しても録音プロセスは動き続けること。
//
// Serve は EOF で戻るだけで、録音プロセス (IPC サーバー) には触れない。
func TestServeExitDoesNotStopRecording(t *testing.T) {
	useStateDir(t)
	rec := &fakeRecorder{lines: []ipc.Line{{Index: 0, At: "00:00:01", Text: "発言"}}}
	r := liveSession(t, rec)

	s := New(&mockLauncher{})
	// 接続して 1 回問い合わせ、そのまま EOF で切断する。
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_status","arguments":{}}}` + "\n")
	var out bytes.Buffer
	if err := s.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	// 接続終了後も録音プロセスは応答し、停止もしていない。
	if rec.stopped {
		t.Fatal("接続終了で録音が停止した")
	}
	if !ipc.Reachable(r.SocketPath) {
		t.Fatal("接続終了後に録音プロセスへ到達できない")
	}
	resp, err := ipc.Call(r.SocketPath, ipc.Request{Op: ipc.OpStatus})
	if err != nil || resp.State != "recording" {
		t.Fatalf("録音が続いていない: %+v (err=%v)", resp, err)
	}
	// 記録上も有効なまま。
	again, err := session.Load("m1")
	if err != nil {
		t.Fatal(err)
	}
	if !again.Active() {
		t.Fatal("セッション記録が無効になった")
	}
}

// 接続を複数回開き直しても同じ会議を見つけられること
// (AI が再起動しても追跡できる)。
func TestReconnectFindsSameSession(t *testing.T) {
	useStateDir(t)
	liveSession(t, &fakeRecorder{})

	for i := 0; i < 3; i++ {
		s := New(&mockLauncher{})
		got := serve(t, s,
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_status","arguments":{}}}`)
		res := got[0]["result"].(map[string]any)
		sc := res["structuredContent"].(map[string]any)
		if sc["id"] != "m1" {
			t.Fatalf("%d 回目で会議を見失った: %+v", i, sc)
		}
	}
}

// ctx のキャンセルで Serve が戻ること (プロセス終了時)。
func TestServeStopsOnContextCancel(t *testing.T) {
	useStateDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pr, pw := io.Pipe()
	defer pw.Close()
	done := make(chan error, 1)
	go func() { done <- New(&mockLauncher{}).Serve(ctx, pr, io.Discard) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ctx キャンセルで戻らない")
	}
}

func TestReadTranscriptFileSkipsHeaders(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/m.txt"
	body := "# 会議文字起こし  開始: x\n[00:00:01] 一行目\n\n[00:01:02] 二行目\n# 終了: y  (2 行)\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := ReadTranscriptFile(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("行数 = %d, want 2: %+v", len(lines), lines)
	}
	if lines[1].At != "00:01:02" || lines[1].Text != "二行目" {
		t.Errorf("解析が不正: %+v", lines[1])
	}
}

func TestSplitLine(t *testing.T) {
	at, text := SplitLine("[00:12:34] 本文です")
	if at != "00:12:34" || text != "本文です" {
		t.Errorf("at=%q text=%q", at, text)
	}
	if at, text := SplitLine("見出し"); at != "" || text != "見出し" {
		t.Errorf("時刻なしの扱いが不正: at=%q text=%q", at, text)
	}
}
