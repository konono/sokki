package ipc

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/konono/sokki/internal/board"
)

// fakeHandler は録音プロセス側の代わり。
type fakeHandler struct {
	mu        sync.Mutex
	calls     int
	stopped   atomic.Bool
	lines     []Line
	published []board.Board
}

func (h *fakeHandler) Handle(req Request) Response {
	h.mu.Lock()
	h.calls++
	h.mu.Unlock()

	switch req.Op {
	case OpStatus:
		return Response{OK: true, ID: "m1", State: "recording", NextFrom: len(h.lines)}
	case OpTranscript, OpContext:
		from := req.From
		if from > len(h.lines) {
			from = len(h.lines)
		}
		lines := h.lines[from:]
		if req.Limit > 0 && len(lines) > req.Limit {
			lines = lines[:req.Limit]
		}
		r := Response{OK: true, Lines: lines, NextFrom: from + len(lines)}
		if req.Op == OpContext {
			r.Interim = "途中の発話"
		}
		return r
	case OpPublish:
		h.mu.Lock()
		h.published = append(h.published, *req.Board)
		v := len(h.published)
		h.mu.Unlock()
		return Response{OK: true, SummaryVersion: v}
	case OpStop:
		h.stopped.Store(true)
		return Response{OK: true, State: "stopped"}
	}
	return Response{OK: false, Error: "未対応"}
}

// shortSock は Unix socket のパス長制限 (約 104 byte) に収まる場所を返す。
// t.TempDir() はテスト名を含むため長くなりがち。
func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func startServer(t *testing.T, h Handler) string {
	t.Helper()
	sock := shortSock(t)
	srv, err := Listen(sock, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestStatusRoundTrip(t *testing.T) {
	sock := startServer(t, &fakeHandler{})
	resp, err := Call(sock, Request{Op: OpStatus})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.ID != "m1" || resp.State != "recording" {
		t.Fatalf("応答が不正: %+v", resp)
	}
}

// 確定行を重複も欠落もなく続きから読めること。
func TestTranscriptPaginationHasNoGapOrDuplicate(t *testing.T) {
	h := &fakeHandler{}
	for i := 0; i < 10; i++ {
		h.lines = append(h.lines, Line{Index: i, At: "00:00:0" + string(rune('0'+i)), Text: "行" + string(rune('0'+i))})
	}
	sock := startServer(t, h)

	var got []Line
	from := 0
	for {
		resp, err := Call(sock, Request{Op: OpTranscript, From: from, Limit: 3})
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Lines) == 0 {
			break
		}
		got = append(got, resp.Lines...)
		if resp.NextFrom <= from {
			t.Fatalf("next_from が進まない: %d", resp.NextFrom)
		}
		from = resp.NextFrom
	}
	if len(got) != 10 {
		t.Fatalf("読めた行数 = %d, want 10", len(got))
	}
	for i, l := range got {
		if l.Index != i {
			t.Fatalf("%d 番目の Index = %d (重複または欠落)", i, l.Index)
		}
	}
}

func TestContextIncludesInterim(t *testing.T) {
	sock := startServer(t, &fakeHandler{})
	resp, err := Call(sock, Request{Op: OpContext})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Interim != "途中の発話" {
		t.Errorf("暫定行が返らない: %+v", resp)
	}
}

func TestPublishSendsBoard(t *testing.T) {
	h := &fakeHandler{}
	sock := startServer(t, h)
	b := &board.Board{Topic: "来週のリリース"}
	resp, err := Call(sock, Request{Op: OpPublish, Board: b})
	if err != nil {
		t.Fatal(err)
	}
	if resp.SummaryVersion != 1 {
		t.Errorf("版 = %d, want 1", resp.SummaryVersion)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.published) != 1 || h.published[0].Topic != "来週のリリース" {
		t.Errorf("会議ボードが届いていない: %+v", h.published)
	}
}

func TestStopRequest(t *testing.T) {
	h := &fakeHandler{}
	sock := startServer(t, h)
	if _, err := Call(sock, Request{Op: OpStop}); err != nil {
		t.Fatal(err)
	}
	if !h.stopped.Load() {
		t.Error("停止が伝わっていない")
	}
}

// AI (MCP) 側が接続を切っても、サーバー (録音プロセス) は動き続けること。
func TestServerSurvivesClientDisconnect(t *testing.T) {
	h := &fakeHandler{}
	sock := startServer(t, h)

	// 何度も接続・切断を繰り返す。
	for i := 0; i < 5; i++ {
		if _, err := Call(sock, Request{Op: OpStatus}); err != nil {
			t.Fatalf("%d 回目の呼び出しで失敗: %v", i, err)
		}
	}
	// 切断後も応答できること。
	resp, err := Call(sock, Request{Op: OpStatus})
	if err != nil {
		t.Fatalf("切断後に応答しない: %v", err)
	}
	if !resp.OK {
		t.Error("切断後の応答が OK でない")
	}
	if !Reachable(sock) {
		t.Error("切断後に到達できない")
	}
}

// 録音プロセスが居ない場合は、原因と対処が分かるエラーになること。
func TestCallWithoutServerExplains(t *testing.T) {
	sock := shortSock(t)
	_, err := Call(sock, Request{Op: OpStatus})
	if err == nil {
		t.Fatal("接続できないのにエラーにならなかった")
	}
	for _, want := range []string{"接続できません", "対処:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("エラー文面に %q が無い: %v", want, err)
		}
	}
}

// 同じソケットで二重に録音プロセスを立ち上げない。
func TestListenRejectsDuplicate(t *testing.T) {
	sock := startServer(t, &fakeHandler{})
	if _, err := Listen(sock, &fakeHandler{}); err == nil {
		t.Fatal("同じソケットで二重に待ち受けできてしまった")
	}
}

// 死んだプロセスのソケットが残っていても起動できる。
func TestListenReclaimsStaleSocket(t *testing.T) {
	sock := shortSock(t)

	srv1, err := Listen(sock, &fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	// ソケットファイルを残したまま待ち受けだけ止める。
	srv1.ln.Close()

	srv2, err := Listen(sock, &fakeHandler{})
	if err != nil {
		t.Fatalf("残骸のソケットで起動できない: %v", err)
	}
	defer srv2.Close()
	if _, err := Call(sock, Request{Op: OpStatus}); err != nil {
		t.Fatalf("再起動後に応答しない: %v", err)
	}
}

// Close 後は接続できないこと。
func TestCloseRemovesSocket(t *testing.T) {
	sock := shortSock(t)
	srv, err := Listen(sock, &fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	if Reachable(sock) {
		t.Error("Close 後も到達できる")
	}
}

// 1 接続で複数要求を続けて扱えること。
func TestMultipleRequestsPerConnection(t *testing.T) {
	h := &fakeHandler{lines: []Line{{Index: 0, At: "00:00:01", Text: "一行目"}}}
	sock := startServer(t, h)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 3; i++ {
			if _, err := Call(sock, Request{Op: OpStatus}); err != nil {
				t.Errorf("呼び出し %d: %v", i, err)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("タイムアウト")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.calls < 3 {
		t.Errorf("処理回数 = %d, want >= 3", h.calls)
	}
}
