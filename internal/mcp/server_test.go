package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/konono/sokki/internal/board"
	"github.com/konono/sokki/internal/ipc"
	"github.com/konono/sokki/internal/session"
)

// --- 模擬 panecom (実際の pane は開かない) ---

type mockLauncher struct {
	available error
	launched  int
	opt       LaunchOptions
	rec       *session.Record
	launchErr error
}

func (m *mockLauncher) Available(ctx context.Context) error { return m.available }

func (m *mockLauncher) Launch(ctx context.Context, opt LaunchOptions) (*session.Record, error) {
	m.launched++
	m.opt = opt
	if m.launchErr != nil {
		return nil, m.launchErr
	}
	if m.rec != nil {
		_ = session.Save(m.rec)
	}
	return m.rec, nil
}

// --- 模擬の録音プロセス (IPC サーバー) ---

type fakeRecorder struct {
	lines     []ipc.Line
	interim   string
	cur       board.Board
	published []board.Board
	stopped   bool
}

func (f *fakeRecorder) Handle(req ipc.Request) ipc.Response {
	switch req.Op {
	case ipc.OpStatus:
		return ipc.Response{OK: true, State: "recording", NextFrom: len(f.lines),
			Interim: f.interim, SummaryVersion: f.cur.Version}
	case ipc.OpTranscript, ipc.OpContext:
		from := req.From
		if from > len(f.lines) {
			from = len(f.lines)
		}
		lines := f.lines[from:]
		if req.Limit > 0 && len(lines) > req.Limit {
			lines = lines[:req.Limit]
		}
		r := ipc.Response{OK: true, Lines: lines, NextFrom: from + len(lines)}
		if req.Op == ipc.OpContext {
			r.Interim = f.interim
			r.InterimAt = "00:01:00"
			b := f.cur
			r.Board = &b
			r.SummaryVersion = f.cur.Version
		}
		return r
	case ipc.OpPublish:
		f.published = append(f.published, *req.Board)
		f.cur = *req.Board
		f.cur.Version = len(f.published)
		f.cur.UpdatedAt = time.Now()
		return ipc.Response{OK: true, SummaryVersion: f.cur.Version,
			SummaryAt: f.cur.UpdatedAt.Format(time.RFC3339),
			BoardPath: "/tmp/m.md", HistoryPath: "/tmp/m.history.md"}
	case ipc.OpStop:
		f.stopped = true
		return ipc.Response{OK: true, State: "stopped",
			TranscriptPath: "/tmp/m.txt", BoardPath: "/tmp/m.md"}
	}
	return ipc.Response{OK: false, Error: "未対応"}
}

// --- テスト基盤 ---

func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

// liveSession は IPC サーバー付きの録音中セッションを用意する。
func liveSession(t *testing.T, rec *fakeRecorder) *session.Record {
	t.Helper()
	sock := shortSock(t)
	srv, err := ipc.Listen(sock, rec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })

	r := &session.Record{
		ID: "m1", PID: os.Getpid(), State: session.StateRecording,
		PaneRole: "sokki", StartedAt: time.Now(), MuxSession: testMux,
		TranscriptPath: "/tmp/m1.txt", BoardPath: "/tmp/m1.md",
		SocketPath: sock, BoardFormat: session.BoardFormatSummaryV2,
	}
	if err := session.Save(r); err != nil {
		t.Fatal(err)
	}
	return r
}

const testMux = "zellij:test-mux"

func useStateDir(t *testing.T) {
	t.Helper()
	session.SetDirForTest(t.TempDir())
	t.Cleanup(func() { session.SetDirForTest("") })
	// mux セッションを固定する (別セッションの録音を拾わないため)。
	t.Setenv("ZELLIJ_SESSION_NAME", "test-mux")
	t.Setenv("TMUX", "")
}

// call はツールを呼んで構造化結果を返す。
func call(t *testing.T, s *Server, name string, args map[string]any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := s.runTool(context.Background(), name, raw)
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(res)
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out, nil
}

// --- start_or_attach ---

// panecom が使えないときは音声デバイスを開かずに失敗すること。
func TestStartOrAttachFailsWithoutPanecom(t *testing.T) {
	useStateDir(t)
	l := &mockLauncher{available: fmt.Errorf("panecom を利用できません: Zellij / tmux のセッション内ではありません")}
	s := New(l)

	_, err := call(t, s, "start_or_attach", nil)
	if err == nil {
		t.Fatal("panecom が無いのに成功した")
	}
	if !strings.Contains(err.Error(), "panecom") {
		t.Errorf("理由が分からないエラー: %v", err)
	}
	if l.launched != 0 {
		t.Errorf("panecom 不可なのに起動を試みた (%d 回)", l.launched)
	}
}

// 録音中があれば再利用し、二重起動しないこと。
func TestStartOrAttachReusesRunningSession(t *testing.T) {
	useStateDir(t)
	liveSession(t, &fakeRecorder{})

	l := &mockLauncher{}
	s := New(l)
	out, err := call(t, s, "start_or_attach", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["attached"] != true {
		t.Errorf("既存会議に接続していない: %+v", out)
	}
	if l.launched != 0 {
		t.Errorf("二重起動した (%d 回)", l.launched)
	}
	if out["id"] != "m1" {
		t.Errorf("会議 ID = %v", out["id"])
	}
}

// 録音中が無ければ起動すること。
func TestStartOrAttachLaunchesWhenIdle(t *testing.T) {
	useStateDir(t)
	rec := &session.Record{ID: "new1", PID: os.Getpid(), State: session.StateRecording,
		PaneRole: "sokki", StartedAt: time.Now(), MuxSession: testMux}
	l := &mockLauncher{rec: rec}
	s := New(l)

	out, err := call(t, s, "start_or_attach", nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.launched != 1 {
		t.Errorf("起動回数 = %d, want 1", l.launched)
	}
	if !l.opt.AutoSummary {
		t.Error("MCP の既定値で自動要約が無効")
	}
	if out["attached"] != false || out["id"] != "new1" {
		t.Errorf("応答が不正: %+v", out)
	}
}

// 起動に失敗したらエラーが AI に返ること。
func TestStartOrAttachReportsLaunchFailure(t *testing.T) {
	useStateDir(t)
	l := &mockLauncher{launchErr: fmt.Errorf("sokki の起動に失敗しました: デバイスがありません")}
	s := New(l)
	if _, err := call(t, s, "start_or_attach", nil); err == nil {
		t.Fatal("起動失敗がエラーにならなかった")
	} else if !strings.Contains(err.Error(), "デバイス") {
		t.Errorf("原因が分からない: %v", err)
	}
}

// --- get_status ---

func TestGetStatusWithoutSession(t *testing.T) {
	useStateDir(t)
	out, err := call(t, New(&mockLauncher{}), "get_status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["running"] != false {
		t.Errorf("running = %v, want false", out["running"])
	}
}

func TestGetStatusReportsRunning(t *testing.T) {
	useStateDir(t)
	liveSession(t, &fakeRecorder{interim: "途中の発話"})
	out, err := call(t, New(&mockLauncher{}), "get_status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["running"] != true || out["state"] != "recording" {
		t.Errorf("状態が不正: %+v", out)
	}
	if out["interim"] != "途中の発話" {
		t.Errorf("暫定行が返らない: %+v", out)
	}
}

// 起動失敗の記録は状態として区別できること。
func TestGetStatusDistinguishesFailure(t *testing.T) {
	useStateDir(t)
	r := &session.Record{ID: "bad", PID: 999999, State: session.StateFailed,
		StartedAt: time.Now(), Error: "入力デバイスが見つかりません", PaneRole: "sokki",
		MuxSession: testMux}
	if err := session.Save(r); err != nil {
		t.Fatal(err)
	}
	out, err := call(t, New(&mockLauncher{}), "get_status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["state"] != "failed" || out["running"] != false {
		t.Errorf("失敗を区別できていない: %+v", out)
	}
	if !strings.Contains(fmt.Sprint(out["error"]), "デバイス") {
		t.Errorf("失敗理由が返らない: %+v", out)
	}
}

// --- read_transcript / read_context ---

func TestReadTranscriptPaginates(t *testing.T) {
	useStateDir(t)
	rec := &fakeRecorder{}
	for i := 0; i < 5; i++ {
		rec.lines = append(rec.lines, ipc.Line{Index: i, At: "00:00:01", Text: fmt.Sprintf("行%d", i)})
	}
	liveSession(t, rec)
	s := New(&mockLauncher{})

	out, err := call(t, s, "read_transcript", map[string]any{"from": 0, "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	if n := int(out["next_from"].(float64)); n != 2 {
		t.Fatalf("next_from = %d, want 2", n)
	}
	out2, err := call(t, s, "read_transcript", map[string]any{"from": 2})
	if err != nil {
		t.Fatal(err)
	}
	lines := out2["lines"].([]any)
	if len(lines) != 3 {
		t.Fatalf("続きの行数 = %d, want 3", len(lines))
	}
}

// read_context は暫定行と会議ボードを返し、暫定は未確定と示すこと。
func TestReadContextIncludesInterimAndBoard(t *testing.T) {
	useStateDir(t)
	rec := &fakeRecorder{interim: "話している途中", cur: board.Board{Topic: "来週のリリース", Version: 1}}
	liveSession(t, rec)

	out, err := call(t, New(&mockLauncher{}), "read_context", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["interim"] != "話している途中" {
		t.Errorf("暫定行が無い: %+v", out)
	}
	if out["interim_is_unconfirmed"] != true {
		t.Errorf("暫定が未確定と示されていない: %+v", out)
	}
	b, ok := out["board"].(map[string]any)
	if !ok || b["topic"] != "来週のリリース" {
		t.Errorf("会議ボードが返らない: %+v", out["board"])
	}
}

// --- publish_summary ---

func TestPublishSummaryReachesRecorder(t *testing.T) {
	useStateDir(t)
	rec := &fakeRecorder{}
	liveSession(t, rec)

	out, err := call(t, New(&mockLauncher{}), "publish_summary", map[string]any{
		"topic":           "来週のリリース",
		"summary":         "・[00:11:42] テストを金曜までに終える方針で合意した。",
		"applied_through": 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if int(out["summary_version"].(float64)) != 1 {
		t.Errorf("版 = %v", out["summary_version"])
	}
	if len(rec.published) != 1 {
		t.Fatalf("録音プロセスに届いていない: %+v", rec.published)
	}
	got := rec.published[0]
	if got.Topic != "来週のリリース" || !strings.Contains(got.Summary, "金曜") {
		t.Errorf("会議ボードの内容が違う: %+v", got)
	}
	if got.AppliedThrough != 12 {
		t.Errorf("反映済み行位置が渡っていない: %d", got.AppliedThrough)
	}
}

// 録音終了後も Markdown の更新を続けられること (計画 110/126 行)。
// 画面には反映できないので、その旨を返す。
func TestPublishSummaryWorksAfterStop(t *testing.T) {
	useStateDir(t)
	dir := t.TempDir()
	tx := filepath.Join(dir, "m.txt")
	if err := os.WriteFile(tx, []byte("[00:00:01] 発言\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &session.Record{ID: "dead", PID: 999999, State: session.StateStopped,
		StartedAt: time.Now(), TranscriptPath: tx, MuxSession: testMux}
	if err := session.Save(r); err != nil {
		t.Fatal(err)
	}

	out, err := call(t, New(&mockLauncher{}), "publish_summary",
		map[string]any{"id": "dead", "topic": "後から要約"})
	if err != nil {
		t.Fatalf("終了後に公開できない: %v", err)
	}
	if out["screen_updated"] != false {
		t.Errorf("画面反映の有無が示されていない: %+v", out)
	}
	if int(out["summary_version"].(float64)) != 1 {
		t.Errorf("版 = %v", out["summary_version"])
	}
	md := filepath.Join(dir, "m.md")
	data, err := os.ReadFile(md)
	if err != nil {
		t.Fatalf("Markdown が保存されていない: %v", err)
	}
	if !strings.Contains(string(data), "後から要約") {
		t.Errorf("内容が保存されていない:\n%s", data)
	}
}

// 終了後に複数回公開しても版が続き、既存ボードを読めること。
func TestPublishAfterStopContinuesVersions(t *testing.T) {
	useStateDir(t)
	dir := t.TempDir()
	tx := filepath.Join(dir, "m.txt")
	if err := os.WriteFile(tx, []byte("[00:00:01] 発言\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &session.Record{ID: "fin2", PID: 999999, State: session.StateStopped,
		StartedAt: time.Now(), TranscriptPath: tx, MuxSession: testMux}
	if err := session.Save(r); err != nil {
		t.Fatal(err)
	}
	s := New(&mockLauncher{})

	for i, topic := range []string{"一回目", "二回目", "三回目"} {
		out, err := call(t, s, "publish_summary", map[string]any{"id": "fin2", "topic": topic})
		if err != nil {
			t.Fatalf("%d 回目で失敗: %v", i+1, err)
		}
		if got := int(out["summary_version"].(float64)); got != i+1 {
			t.Fatalf("%d 回目の版 = %d", i+1, got)
		}
	}
	// 既存ボードを read_context で読めること。
	ctxOut, err := call(t, s, "read_context", map[string]any{"id": "fin2"})
	if err != nil {
		t.Fatal(err)
	}
	b, ok := ctxOut["board"].(map[string]any)
	if !ok || b["topic"] != "三回目" {
		t.Fatalf("終了後に既存ボードを読めない: %+v", ctxOut["board"])
	}
	// 履歴に全版が残ること。
	hist, err := os.ReadFile(filepath.Join(dir, "m.history.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"一回目", "二回目", "三回目"} {
		if !strings.Contains(string(hist), want) {
			t.Errorf("履歴に %q が無い", want)
		}
	}
}

// 別の mux セッションの録音へは接続も停止もしないこと。
func TestOperationsAreScopedToMuxSession(t *testing.T) {
	useStateDir(t)
	other := &session.Record{ID: "other", PID: os.Getpid(), State: session.StateRecording,
		PaneRole: "sokki", StartedAt: time.Now(), MuxSession: "zellij:other-session"}
	if err := session.Save(other); err != nil {
		t.Fatal(err)
	}
	l := &mockLauncher{rec: &session.Record{ID: "mine", PID: os.Getpid(),
		State: session.StateRecording, StartedAt: time.Now(), MuxSession: testMux}}
	s := New(l)

	// ID 省略の get_status は別セッションの録音を返さない。
	st, err := call(t, s, "get_status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if st["running"] == true {
		t.Errorf("別セッションの録音を拾った: %+v", st)
	}
	// start_or_attach は別セッションを再利用せず、新規に起動する。
	out, err := call(t, s, "start_or_attach", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["attached"] == true {
		t.Errorf("別セッションの録音へ接続した: %+v", out)
	}
	if l.launched != 1 {
		t.Errorf("起動回数 = %d, want 1", l.launched)
	}
	// ID 省略の stop_session も別セッションを止めない。
	if _, err := call(t, s, "stop_session", nil); err != nil {
		// 自分のセッションの録音 (mine) は IPC が無いので失敗してよい。
		if strings.Contains(err.Error(), "other") {
			t.Errorf("別セッションを止めようとした: %v", err)
		}
	}
}

// --- stop_session ---

func TestStopSessionRequestsGracefulStop(t *testing.T) {
	useStateDir(t)
	rec := &fakeRecorder{}
	liveSession(t, rec)

	out, err := call(t, New(&mockLauncher{}), "stop_session", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.stopped {
		t.Error("停止依頼が届いていない")
	}
	// 文字起こしの保存と最終要約を別の状態として返すこと。
	tr, ok := out["transcript"].(map[string]any)
	if !ok || tr["state"] != "saved" {
		t.Errorf("文字起こしの保存状態が返らない: %+v", out["transcript"])
	}
	fs, ok := out["final_summary"].(map[string]any)
	if !ok || fs["state"] != "pending" {
		t.Errorf("最終要約が pending で返らない: %+v", out["final_summary"])
	}
	if !strings.Contains(fmt.Sprint(out["note"]), "get_status") {
		t.Errorf("結果の確認方法が案内されていない: %+v", out)
	}
}

func TestStopSessionOnAlreadyStopped(t *testing.T) {
	useStateDir(t)
	r := &session.Record{ID: "done", PID: 999999, State: session.StateStopped,
		StartedAt: time.Now(), TranscriptPath: "/tmp/d.txt", MuxSession: testMux}
	if err := session.Save(r); err != nil {
		t.Fatal(err)
	}
	out, err := call(t, New(&mockLauncher{}), "stop_session", map[string]any{"id": "done"})
	if err != nil {
		t.Fatal(err)
	}
	if out["already_stopped"] != true {
		t.Errorf("停止済みと示されない: %+v", out)
	}
}

// --- 録音終了後の読み取り ---

// 録音終了後も保存済みファイルから確定行を読めること。
func TestReadAfterStopUsesFile(t *testing.T) {
	useStateDir(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "m.txt")
	body := "# 会議文字起こし  開始: 2026-10-16 15:00:00\n" +
		"[00:00:01] 最初の発言\n[00:00:05] 次の発言\n# 終了: ...\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &session.Record{ID: "fin", PID: 999999, State: session.StateStopped,
		StartedAt: time.Now(), TranscriptPath: path, MuxSession: testMux}
	if err := session.Save(r); err != nil {
		t.Fatal(err)
	}

	out, err := call(t, New(&mockLauncher{}), "read_transcript", map[string]any{"id": "fin"})
	if err != nil {
		t.Fatal(err)
	}
	lines := out["lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("行数 = %d, want 2 (見出しを含めない): %+v", len(lines), lines)
	}
	first := lines[0].(map[string]any)
	if first["at"] != "00:00:01" || first["text"] != "最初の発言" {
		t.Errorf("行の解析が不正: %+v", first)
	}
}

// --- ツール定義 ---

func TestToolDefsCoverPlan(t *testing.T) {
	want := []string{"start_or_attach", "get_status", "read_transcript",
		"read_context", "publish_summary", "stop_session"}
	defs := toolDefs()
	got := map[string]bool{}
	for _, d := range defs {
		got[d.Name] = true
		if d.Description == "" {
			t.Errorf("%s に説明が無い", d.Name)
		}
		if d.InputSchema == nil {
			t.Errorf("%s にスキーマが無い", d.Name)
		}
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("ツール %s が無い", name)
		}
	}
}

// 要約の観点がツール説明に含まれること。
func TestPublishToolDescribesSummaryGuidance(t *testing.T) {
	for _, d := range toolDefs() {
		if d.Name != "publish_summary" {
			continue
		}
		// 固定分類を使わない方針と、積み重ね方が説明されていること。
		for _, want := range []string{"固定の分類", "追加・統合・訂正", "時刻", "作らない"} {
			if !strings.Contains(d.Description, want) {
				t.Errorf("説明に %q が無い", want)
			}
		}
		return
	}
	t.Fatal("publish_summary が無い")
}

func TestAbsOutputDir(t *testing.T) {
	if got := AbsOutputDir("transcripts", "/work"); got != "/work/transcripts" {
		t.Errorf("相対パスの解決 = %q", got)
	}
	if got := AbsOutputDir("/abs/dir", "/work"); got != "/abs/dir" {
		t.Errorf("絶対パスが変わった = %q", got)
	}
	if got := AbsOutputDir("", "/work"); got != "" {
		t.Errorf("未指定で値が入った = %q", got)
	}
}

// --- REVIEW.md [P1] mux セッションを特定できない場合 ---

// mux 環境変数が無い状態では、別セッションの録音へ接続しないこと。
func TestStartOrAttachRefusesWithoutMuxSession(t *testing.T) {
	session.SetDirForTest(t.TempDir())
	t.Cleanup(func() { session.SetDirForTest("") })
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	t.Setenv("ZELLIJ", "")
	t.Setenv("TMUX", "")

	other := &session.Record{ID: "other", PID: os.Getpid(), State: session.StateRecording,
		PaneRole: "sokki", StartedAt: time.Now(), MuxSession: "zellij:other"}
	if err := session.Save(other); err != nil {
		t.Fatal(err)
	}

	l := &mockLauncher{}
	_, err := call(t, New(l), "start_or_attach", nil)
	if err == nil {
		t.Fatal("mux 不明なのに成功した")
	}
	if l.launched != 0 {
		t.Errorf("mux 不明なのに起動した (%d 回)", l.launched)
	}
}

// セッション不明を「録音なし」と誤案内しないこと。
func TestStatusReportsUnknownMuxSession(t *testing.T) {
	useStateDir(t)
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	t.Setenv("TMUX", "")
	_, err := call(t, New(&mockLauncher{}), "get_status", nil)
	if err == nil || !strings.Contains(err.Error(), "MCP プロセスから") {
		t.Fatalf("セッション不明の原因が返らない: %v", err)
	}
}

// mux 不明時は ID 省略の stop_session も対象を選ばないこと。
func TestStopRefusesWithoutMuxSession(t *testing.T) {
	session.SetDirForTest(t.TempDir())
	t.Cleanup(func() { session.SetDirForTest("") })
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	t.Setenv("ZELLIJ", "")
	t.Setenv("TMUX", "")

	rec := &fakeRecorder{}
	sock := shortSock(t)
	srv, err := ipc.Listen(sock, rec)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	other := &session.Record{ID: "other", PID: os.Getpid(), State: session.StateRecording,
		PaneRole: "sokki", StartedAt: time.Now(), MuxSession: "zellij:other", SocketPath: sock}
	if err := session.Save(other); err != nil {
		t.Fatal(err)
	}

	if _, err := call(t, New(&mockLauncher{}), "stop_session", nil); err == nil {
		t.Fatal("mux 不明なのに停止対象を選んだ")
	}
	if rec.stopped {
		t.Fatal("別セッションの録音を止めた")
	}
}

// 明示 ID でも、録音中なら所属セッションを検証すること。
func TestExplicitIDRespectsSessionBoundaryWhileRecording(t *testing.T) {
	useStateDir(t) // 現在は zellij:test-mux

	rec := &fakeRecorder{}
	sock := shortSock(t)
	srv, err := ipc.Listen(sock, rec)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	other := &session.Record{ID: "other", PID: os.Getpid(), State: session.StateRecording,
		PaneRole: "sokki", StartedAt: time.Now(), MuxSession: "zellij:other", SocketPath: sock}
	if err := session.Save(other); err != nil {
		t.Fatal(err)
	}

	_, err = call(t, New(&mockLauncher{}), "stop_session", map[string]any{"id": "other"})
	if err == nil {
		t.Fatal("別セッションの録音を明示 ID で止められた")
	}
	if !strings.Contains(err.Error(), "別の Zellij / tmux セッション") {
		t.Errorf("理由が分からない: %v", err)
	}
	if rec.stopped {
		t.Fatal("別セッションの録音を止めた")
	}
}

// 終了済みの会議は、別セッションでも読み書きできること
// (録音終了後の議事録更新を妨げない)。
func TestFinishedMeetingIsReadableAcrossSessions(t *testing.T) {
	useStateDir(t)
	dir := t.TempDir()
	tx := filepath.Join(dir, "m.txt")
	if err := os.WriteFile(tx, []byte("[00:00:01] 発言\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := &session.Record{ID: "done-other", PID: 999999, State: session.StateStopped,
		StartedAt: time.Now(), TranscriptPath: tx, MuxSession: "zellij:other"}
	if err := session.Save(done); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, New(&mockLauncher{}), "read_transcript",
		map[string]any{"id": "done-other"}); err != nil {
		t.Errorf("終了済みの会議を読めない: %v", err)
	}
}

// --- REVIEW.md [P1] 録音終了後の読み書きが固まらないこと ---

// fifoHistory は会議の履歴を「名前付きパイプへの symlink」にする。
func fifoHistory(t *testing.T, transcriptPath string) {
	t.Helper()
	dir := filepath.Dir(transcriptPath)
	base := strings.TrimSuffix(filepath.Base(transcriptPath), filepath.Ext(transcriptPath))
	fifo := filepath.Join(dir, "deadpipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("fifo を作れない: %v", err)
	}
	if err := os.Symlink(fifo, filepath.Join(dir, base+".history.md")); err != nil {
		t.Fatal(err)
	}
}

// 終了済み会議の publish_summary が、固まらずエラーを返すこと。
func TestPublishAfterStopDoesNotHangOnFIFOHistory(t *testing.T) {
	useStateDir(t)
	dir := t.TempDir()
	tx := filepath.Join(dir, "m.txt")
	if err := os.WriteFile(tx, []byte("[00:00:01] 発言\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fifoHistory(t, tx)

	r := &session.Record{ID: "fifo1", PID: 999999, State: session.StateStopped,
		StartedAt: time.Now(), TranscriptPath: tx, MuxSession: testMux}
	if err := session.Save(r); err != nil {
		t.Fatal(err)
	}

	type result struct {
		out any
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := call(t, New(&mockLauncher{}), "publish_summary",
			map[string]any{"id": "fifo1", "topic": "後から要約"})
		done <- result{out, err}
	}()

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatalf("名前付きパイプの履歴なのに成功した: %+v", got.out)
		}
		if !strings.Contains(got.err.Error(), "通常のファイルではありません") {
			t.Errorf("理由が分からない: %v", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publish_summary が応答しない (名前付きパイプで停止)")
	}
}

// 終了済み会議の read_context が、固まらず確定行を返すこと。
func TestReadContextAfterStopDoesNotHangOnFIFOHistory(t *testing.T) {
	useStateDir(t)
	dir := t.TempDir()
	tx := filepath.Join(dir, "m.txt")
	if err := os.WriteFile(tx, []byte("[00:00:01] 発言\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fifoHistory(t, tx)

	r := &session.Record{ID: "fifo2", PID: 999999, State: session.StateStopped,
		StartedAt: time.Now(), TranscriptPath: tx, MuxSession: testMux}
	if err := session.Save(r); err != nil {
		t.Fatal(err)
	}

	done := make(chan map[string]any, 1)
	go func() {
		out, err := call(t, New(&mockLauncher{}), "read_context", map[string]any{"id": "fifo2"})
		if err != nil {
			done <- map[string]any{"err": err.Error()}
			return
		}
		done <- out
	}()

	select {
	case out := <-done:
		if msg, failed := out["err"]; failed {
			t.Fatalf("read_context がエラーを返した: %v", msg)
		}
		// 確定行は読めること (会議ボードだけが読めない)。
		// call は JSON を往復するため、行は []any になる。
		lines, ok := out["lines"].([]any)
		if !ok {
			t.Fatalf("lines が配列でない: %T (%+v)", out["lines"], out["lines"])
		}
		if len(lines) != 1 {
			t.Fatalf("確定行が読めていない: %d 行 (%+v)", len(lines), lines)
		}
		first, ok := lines[0].(map[string]any)
		if !ok {
			t.Fatalf("行の形式が不正: %T", lines[0])
		}
		if first["at"] != "00:00:01" || first["text"] != "発言" {
			t.Errorf("行の内容が不正: %+v", first)
		}
		// 会議ボードだけが読めないことが示されること。
		if out["board_error"] == nil {
			t.Errorf("会議ボードの読み取り失敗が示されない: %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read_context が応答しない (名前付きパイプで停止)")
	}
}

// --- REVIEW.md [P1] 旧形式の録音プロセスへ新形式を公開しない ---

// 録音中の会議が board_format を持たない (旧形式) 場合、
// IPC へ送らずに失敗すること。
func TestPublishRefusedForLegacyRecordingSession(t *testing.T) {
	useStateDir(t)
	rec := &fakeRecorder{}
	sock := shortSock(t)
	srv, err := ipc.Listen(sock, rec)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	// board_format 無し = 旧形式の録音プロセス (稼働中の pid 相当)。
	legacy := &session.Record{
		ID: "legacy1", PID: os.Getpid(), State: session.StateRecording,
		PaneRole: "terminal", StartedAt: time.Now(), MuxSession: testMux,
		SocketPath: sock, TranscriptPath: "/tmp/legacy1.txt",
	}
	if err := session.Save(legacy); err != nil {
		t.Fatal(err)
	}

	_, err = call(t, New(&mockLauncher{}), "publish_summary",
		map[string]any{"id": "legacy1", "topic": "新形式", "summary": "・要点"})
	if err == nil {
		t.Fatal("旧形式の録音へ公開できてしまった")
	}
	for _, want := range []string{"古い形式", "対処:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("案内に %q が無い: %v", want, err)
		}
	}
	// IPC へ送っていないこと (旧プロセスを汚さない)。
	if len(rec.published) != 0 {
		t.Errorf("旧形式の録音へ送信した: %+v", rec.published)
	}
}

// 新形式の録音中セッションへは従来どおり公開できること。
func TestPublishAllowedForSummaryV2Session(t *testing.T) {
	useStateDir(t)
	rec := &fakeRecorder{}
	sock := shortSock(t)
	srv, err := ipc.Listen(sock, rec)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	modern := &session.Record{
		ID: "modern1", PID: os.Getpid(), State: session.StateRecording,
		PaneRole: "sokki", StartedAt: time.Now(), MuxSession: testMux,
		SocketPath: sock, BoardFormat: session.BoardFormatSummaryV2,
	}
	if err := session.Save(modern); err != nil {
		t.Fatal(err)
	}

	out, err := call(t, New(&mockLauncher{}), "publish_summary",
		map[string]any{"id": "modern1", "topic": "新形式", "summary": "・要点"})
	if err != nil {
		t.Fatalf("新形式へ公開できない: %v", err)
	}
	if out["screen_updated"] != true {
		t.Errorf("画面反映が示されない: %+v", out)
	}
	if len(rec.published) != 1 {
		t.Fatalf("録音プロセスに届いていない: %+v", rec.published)
	}
}

// 終了済みの旧形式会議は、移行して更新できること (互換を壊さない)。
func TestPublishAllowedForStoppedLegacySession(t *testing.T) {
	useStateDir(t)
	dir := t.TempDir()
	tx := filepath.Join(dir, "m.txt")
	if err := os.WriteFile(tx, []byte("[00:00:01] 発言\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 旧形式の控えを置いておく (固定分類)。
	legacyState := `{"version":2,"topic":"旧題","decisions":[{"text":"旧決定","at":"00:01:00"}]}`
	if err := os.WriteFile(filepath.Join(dir, ".m.board.json"), []byte(legacyState), 0o644); err != nil {
		t.Fatal(err)
	}

	stopped := &session.Record{
		ID: "legacy-done", PID: 999999, State: session.StateStopped,
		StartedAt: time.Now(), TranscriptPath: tx, MuxSession: testMux,
		// board_format 無し (旧形式)
	}
	if err := session.Save(stopped); err != nil {
		t.Fatal(err)
	}

	out, err := call(t, New(&mockLauncher{}), "publish_summary",
		map[string]any{"id": "legacy-done", "topic": "新題", "summary": "・新しい要点"})
	if err != nil {
		t.Fatalf("終了済みの旧形式会議を更新できない: %v", err)
	}
	// 旧版を引き継いで版が進むこと。
	if got := int(out["summary_version"].(float64)); got != 3 {
		t.Errorf("版 = %d, want 3 (旧版 2 の続き)", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "m.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "新しい要点") {
		t.Errorf("新形式で保存されていない:\n%s", data)
	}
}

// get_status で board_format を確認できること。
func TestStatusReportsBoardFormat(t *testing.T) {
	useStateDir(t)
	liveSession(t, &fakeRecorder{})
	out, err := call(t, New(&mockLauncher{}), "get_status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["board_format"]; !ok {
		t.Errorf("board_format が返らない: %+v", out)
	}
}

// REVIEW.md [P2] 停止後も案内どおり summary.final_state で確認できること。
func TestStatusExposesFinalStateAfterStop(t *testing.T) {
	useStateDir(t)
	for _, want := range []string{"done", "failed", "timeout"} {
		r := &session.Record{
			ID: "fin-" + want, PID: 999999, State: session.StateStopped,
			StartedAt: time.Now(), MuxSession: testMux,
			TranscriptPath: "/tmp/x.txt", FinalSummary: want,
			SummaryVersion: 4,
		}
		if err := session.Save(r); err != nil {
			t.Fatal(err)
		}
		out, err := call(t, New(&mockLauncher{}), "get_status",
			map[string]any{"id": r.ID})
		if err != nil {
			t.Fatal(err)
		}
		sum, ok := out["summary"].(map[string]any)
		if !ok {
			t.Fatalf("%s: summary が返らない: %+v", want, out)
		}
		if sum["final_state"] != want {
			t.Errorf("%s: final_state = %v", want, sum["final_state"])
		}
	}
}

// 最終要約の結果が無ければ unknown とすること。
func TestStatusFinalStateUnknown(t *testing.T) {
	useStateDir(t)
	r := &session.Record{
		ID: "nofinal", PID: 999999, State: session.StateStopped,
		StartedAt: time.Now(), MuxSession: testMux, TranscriptPath: "/tmp/x.txt",
	}
	if err := session.Save(r); err != nil {
		t.Fatal(err)
	}
	out, err := call(t, New(&mockLauncher{}), "get_status", map[string]any{"id": "nofinal"})
	if err != nil {
		t.Fatal(err)
	}
	sum := out["summary"].(map[string]any)
	if sum["final_state"] != "unknown" {
		t.Errorf("final_state = %v, want unknown", sum["final_state"])
	}
}

// stop_session が案内する場所と、実際の返却場所が一致すること。
func TestStopSessionGuidanceMatchesStatusShape(t *testing.T) {
	useStateDir(t)
	rec := &fakeRecorder{}
	liveSession(t, rec)

	out, err := call(t, New(&mockLauncher{}), "stop_session", nil)
	if err != nil {
		t.Fatal(err)
	}
	note := fmt.Sprint(out["note"])
	if !strings.Contains(note, "summary.final_state") {
		t.Fatalf("案内が変わった: %q", note)
	}
	// 案内どおりの場所に実際に入ること (停止後の記録で確認)。
	r, err := session.Load("m1")
	if err != nil {
		t.Fatal(err)
	}
	r.State = session.StateStopped
	r.FinalSummary = "done"
	if err := session.Save(r); err != nil {
		t.Fatal(err)
	}
	st, err := call(t, New(&mockLauncher{}), "get_status", map[string]any{"id": "m1"})
	if err != nil {
		t.Fatal(err)
	}
	sum, ok := st["summary"].(map[string]any)
	if !ok || sum["final_state"] != "done" {
		t.Errorf("案内した場所に結果が無い: %+v", st["summary"])
	}
}

// REVIEW.md [P2] 自動要約を開始できなかった場合、成功を約束しないこと。
func TestStartOrAttachReportsFailedAutoSummary(t *testing.T) {
	useStateDir(t)
	// 録音は始まったが、自動要約は起動できなかった記録。
	rec := &session.Record{
		ID: "nosum", PID: os.Getpid(), State: session.StateRecording,
		PaneRole: "sokki", StartedAt: time.Now(), MuxSession: testMux,
		BoardFormat: session.BoardFormatSummaryV2,
		AutoSummary: false,
		SummaryNote: "要約に使う claude が見つかりません",
	}
	l := &mockLauncher{rec: rec}

	out, err := call(t, New(l), "start_or_attach", map[string]any{"auto_summary": true})
	if err != nil {
		t.Fatal(err)
	}
	if out["auto_summary"] != false {
		t.Errorf("実状態を返していない: %+v", out["auto_summary"])
	}
	if out["auto_summary_requested"] != true {
		t.Errorf("要求値が残っていない: %+v", out)
	}
	note := fmt.Sprint(out["note"])
	if !strings.Contains(note, "開始できませんでした") {
		t.Errorf("失敗が案内されない: %q", note)
	}
	if !strings.Contains(note, "claude が見つかりません") {
		t.Errorf("理由が案内されない: %q", note)
	}
	if strings.Contains(note, "自動で作成・更新されます") {
		t.Errorf("起動していないのに自動更新を約束した: %q", note)
	}
	// 録音自体は続いていること。
	if out["state"] != "recording" {
		t.Errorf("録音が止まった扱いになっている: %+v", out["state"])
	}
}

// 起動できていれば従来どおり案内すること。
func TestStartOrAttachReportsWorkingAutoSummary(t *testing.T) {
	useStateDir(t)
	rec := &session.Record{
		ID: "withsum", PID: os.Getpid(), State: session.StateRecording,
		PaneRole: "sokki", StartedAt: time.Now(), MuxSession: testMux,
		BoardFormat: session.BoardFormatSummaryV2, AutoSummary: true,
	}
	out, err := call(t, New(&mockLauncher{rec: rec}), "start_or_attach", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["auto_summary"] != true {
		t.Errorf("auto_summary = %v", out["auto_summary"])
	}
	note := fmt.Sprint(out["note"])
	if !strings.Contains(note, "自動で作成・更新されます") {
		t.Errorf("自動更新が案内されない: %q", note)
	}
	if !strings.Contains(note, "AI プロバイダーへ送られます") {
		t.Errorf("送信の注意が無い: %q", note)
	}
}

// get_status も実状態と理由を返すこと。
func TestStatusReportsAutoSummaryState(t *testing.T) {
	useStateDir(t)
	rec := &session.Record{
		ID: "st1", PID: 999999, State: session.StateStopped,
		StartedAt: time.Now(), MuxSession: testMux, TranscriptPath: "/tmp/x.txt",
		AutoSummary: false, SummaryNote: "未対応の要約プロバイダーです",
	}
	if err := session.Save(rec); err != nil {
		t.Fatal(err)
	}
	out, err := call(t, New(&mockLauncher{}), "get_status", map[string]any{"id": "st1"})
	if err != nil {
		t.Fatal(err)
	}
	if out["auto_summary"] != false {
		t.Errorf("auto_summary = %v", out["auto_summary"])
	}
	if !strings.Contains(fmt.Sprint(out["auto_summary_note"]), "未対応") {
		t.Errorf("理由が返らない: %+v", out["auto_summary_note"])
	}
}

// --- REVIEW.md [P2] 再接続応答も録音プロセスの最新版を返すこと ---

// 録音中に再接続したら、会議ボードの版は記録ではなく IPC の実値になること。
func TestAttachReportsLiveSummaryVersion(t *testing.T) {
	useStateDir(t)
	rec := &fakeRecorder{cur: board.Board{Topic: "来週の予定", Summary: "・決めた", Version: 4}}
	r := liveSession(t, rec)
	// 記録側は保存時点の古い版のまま (自動要約は IPC 側でだけ進む)。
	r.SummaryVersion = 1
	if err := session.Save(r); err != nil {
		t.Fatal(err)
	}

	s := &Server{launcher: &mockLauncher{}}
	out := mustCall(t, s, "start_or_attach", nil)

	if out["attached"] != true {
		t.Fatalf("接続していない: %v", out)
	}
	if got := jsonNum(t, out["summary_version"]); got != 4 {
		t.Errorf("summary_version = %v, want 4 (IPC の実値)", got)
	}
	if out["state"] != "recording" {
		t.Errorf("state = %v", out["state"])
	}
}

// get_status と再接続で版が食い違わないこと。
func TestAttachAndStatusAgreeOnSummaryVersion(t *testing.T) {
	useStateDir(t)
	rec := &fakeRecorder{cur: board.Board{Summary: "・x", Version: 3}}
	r := liveSession(t, rec)
	r.SummaryVersion = 0
	if err := session.Save(r); err != nil {
		t.Fatal(err)
	}

	s := &Server{launcher: &mockLauncher{}}
	attached := mustCall(t, s, "start_or_attach", nil)
	status := mustCall(t, s, "get_status", nil)

	av := jsonNum(t, attached["summary_version"])
	sv := jsonNum(t, status["summary_version"])
	if av != sv {
		t.Errorf("版が食い違う: attach = %v, get_status = %v", av, sv)
	}
	if av != 3 {
		t.Errorf("summary_version = %v, want 3", av)
	}
}

// 録音プロセスへ届かない場合は記録の値のまま返すこと (接続自体は壊さない)。
func TestAttachFallsBackWhenIPCUnreachable(t *testing.T) {
	useStateDir(t)
	r := &session.Record{
		ID: "m1", PID: os.Getpid(), State: session.StateRecording,
		PaneRole: "sokki", StartedAt: time.Now(), MuxSession: testMux,
		TranscriptPath: "/tmp/m1.txt", BoardPath: "/tmp/m1.md",
		SocketPath:  filepath.Join(t.TempDir(), "none.sock"),
		BoardFormat: session.BoardFormatSummaryV2, SummaryVersion: 2,
	}
	if err := session.Save(r); err != nil {
		t.Fatal(err)
	}

	s := &Server{launcher: &mockLauncher{}}
	out := mustCall(t, s, "start_or_attach", nil)

	if out["attached"] != true {
		t.Fatalf("接続していない: %v", out)
	}
	if got := jsonNum(t, out["summary_version"]); got != 2 {
		t.Errorf("summary_version = %v, want 2 (記録の値)", got)
	}
}

// jsonNum は数値を float64 として取り出す。
func jsonNum(t *testing.T, v any) float64 {
	t.Helper()
	switch n := v.(type) {
	case int:
		return float64(n)
	case float64:
		return n
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	t.Fatalf("数値ではない: %T (%v)", v, v)
	return 0
}

// mustCall はツールを呼び、エラーなら失敗させる。
func mustCall(t *testing.T, s *Server, name string, args map[string]any) map[string]any {
	t.Helper()
	out, err := call(t, s, name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}
