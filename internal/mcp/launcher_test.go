package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/konono/sokki/internal/panecom"
	"github.com/konono/sokki/internal/session"
)

// mockPanecom は模擬 panecom を用意する。実際の pane は開かない。
//
// send を受けたとき、script の内容に応じてセッション記録を書き、
// 専用 pane で sokki が起動した状況を再現する。
func mockPanecom(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "panecom")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := panecom.Bin
	panecom.Bin = path
	t.Cleanup(func() { panecom.Bin = old })
	// useStateDir と同じ mux セッションを使う (記録の絞り込みと一致させる)。
	t.Setenv("ZELLIJ_SESSION_NAME", "test-mux")
	t.Setenv("TMUX", "")
	return dir
}

// writeRecord はテストから直接セッション記録を書く (sokki の代わり)。
func writeRecord(t *testing.T, id string, st session.State, errMsg string) {
	t.Helper()
	r := &session.Record{
		ID: id, PID: os.Getpid(), State: st, PaneRole: "sokki",
		StartedAt: time.Now(), Error: errMsg, MuxSession: testMux,
		TranscriptPath: "/tmp/" + id + ".txt",
	}
	if err := session.Save(r); err != nil {
		t.Errorf("セッション記録を書けない: %v", err)
	}
}

// sessionIDFromCmd は send されたコマンド行から --session-id を取り出す。
func sessionIDFromCmd(t *testing.T, cmdFile string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(cmdFile)
		if err == nil {
			fields := strings.Fields(string(data))
			for i, f := range fields {
				if f == "--session-id" && i+1 < len(fields) {
					return strings.Trim(fields[i+1], "'")
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("send されたコマンドから会議 ID を取得できない (%s)", cmdFile)
	return ""
}

// recordOnSend は send を検知して、その会議 ID で記録を書く
// (専用 pane で sokki が起動した状況の再現)。
func recordOnSend(t *testing.T, dir string, st session.State, errMsg string) {
	t.Helper()
	go func() {
		id := sessionIDFromCmd(t, filepath.Join(dir, "cmd.txt"))
		writeRecord(t, id, st, errMsg)
	}()
}

func newLauncher(t *testing.T) *PaneLauncher {
	t.Helper()
	l := NewPaneLauncher("/usr/local/bin/sokki", t.TempDir())
	l.WaitFor = 5 * time.Second
	return l
}

// panecom が使えなければ Available がエラーを返すこと。
func TestLauncherAvailableFailsOutsideMux(t *testing.T) {
	mockPanecom(t, `echo developer`)
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	t.Setenv("ZELLIJ", "")
	t.Setenv("TMUX", "")

	if err := newLauncher(t).Available(context.Background()); err == nil {
		t.Fatal("セッション外なのに利用可能と判定された")
	}
}

func TestLauncherAvailableOK(t *testing.T) {
	mockPanecom(t, `echo developer`)
	if err := newLauncher(t).Available(context.Background()); err != nil {
		t.Fatalf("利用可能なのにエラー: %v", err)
	}
}

// pane が無ければ open してから send すること。
func TestLaunchOpensPaneWhenMissing(t *testing.T) {
	useStateDir(t)
	dir := mockPanecom(t, `
d="$(dirname "$0")"
echo "$1" >> "$d/log.txt"
case "$1" in
  # pane は最初は無く、open すると解決できるようになる
  resolve) [ -f "$d/opened" ] && echo terminal_9 || exit 1 ;;
  open)    touch "$d/opened"; exit 0 ;;
  send)    echo "$3" > "$d/cmd.txt"; exit 0 ;;
  whoami)  echo developer ;;
esac`)

	l := newLauncher(t)
	recordOnSend(t, dir, session.StateRecording, "")

	rec, err := l.Launch(context.Background(), LaunchOptions{WorkDir: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID == "" {
		t.Error("会議 ID が空")
	}
	log, _ := os.ReadFile(filepath.Join(dir, "log.txt"))
	ops := strings.Fields(string(log))
	if !contains(ops, "open") || !contains(ops, "send") {
		t.Errorf("open / send が呼ばれていない: %v", ops)
	}
}

// 既存 pane があれば open せず再利用すること (停止済み pane の使い回し)。
func TestLaunchReusesExistingPane(t *testing.T) {
	useStateDir(t)
	dir := mockPanecom(t, `
log="$(dirname "$0")/log.txt"
echo "$1" >> "$log"
case "$1" in
  resolve) echo terminal_9 ;;   # pane は既にある
  open)    exit 0 ;;
  send)    echo "$3" > "$(dirname "$0")/cmd.txt"; exit 0 ;;
  whoami)  echo developer ;;
esac`)

	l := newLauncher(t)
	recordOnSend(t, dir, session.StateRecording, "")
	if _, err := l.Launch(context.Background(), LaunchOptions{}); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "log.txt"))
	if contains(strings.Fields(string(log)), "open") {
		t.Errorf("既存 pane があるのに open した: %s", log)
	}
}

// 起動コマンドに会議の内容が混ざらず、引数が安全に引用されること。
func TestLaunchCommandLineIsSafe(t *testing.T) {
	l := NewPaneLauncher("/path/to/sokki", "/base")
	got := l.commandLine("20261016-150405", LaunchOptions{
		OutputDir: "out dir",
		Language:  "ja",
		Device:    "BlackHole $HOME",
		WorkDir:   "/work",
	})
	// シェルの変数展開が起きない形であること。
	if strings.Contains(got, `"BlackHole $HOME"`) {
		t.Errorf("二重引用符で変数展開の恐れ: %s", got)
	}
	if !strings.Contains(got, `'BlackHole $HOME'`) {
		t.Errorf("単一引用符で囲まれていない: %s", got)
	}
	// 相対パスが「起動要求時の作業ディレクトリ」基準で絶対化されること。
	if !strings.Contains(got, `'/work/out dir'`) {
		t.Errorf("保存先が絶対パスになっていない: %s", got)
	}
	if !strings.Contains(got, "--pane-role 'sokki'") {
		t.Errorf("pane role が渡っていない: %s", got)
	}
	if !strings.Contains(got, "--session-id '20261016-150405'") {
		t.Errorf("会議 ID が渡っていない: %s", got)
	}
}

// 起動失敗 (sokki が failed を記録) はエラーとして返ること。
func TestLaunchReportsSokkiFailure(t *testing.T) {
	useStateDir(t)
	dir := mockPanecom(t, `
case "$1" in
  resolve) echo terminal_9 ;;
  send)    echo "$3" > "$(dirname "$0")/cmd.txt"; exit 0 ;;
  whoami)  echo developer ;;
esac`)

	l := newLauncher(t)
	recordOnSend(t, dir, session.StateFailed, "入力デバイスが見つかりません")

	_, err := l.Launch(context.Background(), LaunchOptions{})
	if err == nil {
		t.Fatal("起動失敗がエラーにならない")
	}
	if !strings.Contains(err.Error(), "入力デバイス") {
		t.Errorf("原因が含まれない: %v", err)
	}
	// 残った pane を識別できること。
	if !strings.Contains(err.Error(), "sokki") {
		t.Errorf("pane role が示されない: %v", err)
	}
}

// 記録が現れなければタイムアウトすること。
func TestLaunchTimesOutWithoutRecord(t *testing.T) {
	useStateDir(t)
	mockPanecom(t, `
case "$1" in
  resolve) echo terminal_9 ;;
  send)    exit 0 ;;
  whoami)  echo developer ;;
esac`)
	l := newLauncher(t)
	l.WaitFor = 600 * time.Millisecond

	if _, err := l.Launch(context.Background(), LaunchOptions{}); err == nil {
		t.Fatal("タイムアウトしなかった")
	}
}

// send が失敗したらエラーになること。
func TestLaunchReportsSendFailure(t *testing.T) {
	useStateDir(t)
	mockPanecom(t, `
case "$1" in
  resolve) echo terminal_9 ;;
  send)    echo "send failed" >&2; exit 1 ;;
  whoami)  echo developer ;;
esac`)
	if _, err := newLauncher(t).Launch(context.Background(), LaunchOptions{}); err == nil {
		t.Fatal("send 失敗がエラーにならない")
	}
}

// start_or_attach を通しても二重に起動しないこと (模擬 panecom 経由)。
func TestStartOrAttachDoesNotDoubleLaunchWithRealLauncher(t *testing.T) {
	useStateDir(t)
	dir := mockPanecom(t, `
log="$(dirname "$0")/log.txt"
echo "$1" >> "$log"
case "$1" in
  resolve) echo terminal_9 ;;
  send)    echo "$3" > "$(dirname "$0")/cmd.txt"; exit 0 ;;
  whoami)  echo developer ;;
esac`)

	l := newLauncher(t)
	s := New(l)

	recordOnSend(t, dir, session.StateRecording, "")
	if _, err := call(t, s, "start_or_attach", nil); err != nil {
		t.Fatal(err)
	}
	// 2 回目は既存会議に接続するだけ。
	out, err := call(t, s, "start_or_attach", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["attached"] != true {
		t.Errorf("2 回目が再利用になっていない: %+v", out)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "log.txt"))
	if n := strings.Count(string(log), "send"); n != 1 {
		t.Errorf("send 回数 = %d, want 1 (二重起動)", n)
	}
}

// 既定では自分の作業ディレクトリ (= AI pane のもの) を使うこと。
//
// panecom の namespace は「登録済み pane の namespace」が優先され、
// 未登録なら作業ディレクトリが使われる。MCP サーバーは AI pane から
// 起動されるため、CWD をそのまま使うのが最も一致しやすい。
func TestDefaultBaseDirUsesWorkingDirectory(t *testing.T) {
	sub := filepath.Join(t.TempDir(), "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	prev, _ := os.Getwd()
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(prev) })

	wantResolved, _ := filepath.EvalSymlinks(sub)
	gotResolved, _ := filepath.EvalSymlinks(DefaultBaseDir())
	if gotResolved != wantResolved {
		t.Errorf("基準ディレクトリ = %q, want %q", gotResolved, wantResolved)
	}
}

// open した pane の role を解決できない場合は、録音を開始せず
// 原因を返すこと (namespace 不一致の検知)。
func TestLaunchStopsWhenRoleUnresolvableAfterOpen(t *testing.T) {
	useStateDir(t)
	dir := mockPanecom(t, `
d="$(dirname "$0")"
echo "$1" >> "$d/log.txt"
case "$1" in
  resolve) exit 1 ;;   # open しても解決できない (namespace 不一致)
  open)    exit 0 ;;
  send)    echo "$3" > "$d/cmd.txt"; exit 0 ;;
  whoami)  echo developer ;;
esac`)

	l := newLauncher(t)
	_, err := l.Launch(context.Background(), LaunchOptions{})
	if err == nil {
		t.Fatal("解決できないのに成功した")
	}
	for _, want := range []string{"解決できません", "namespace", "録音は開始していません"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("案内に %q が無い:\n%v", want, err)
		}
	}
	// send していないこと (= sokki を起動していない)。
	if _, serr := os.Stat(filepath.Join(dir, "cmd.txt")); serr == nil {
		t.Error("role を解決できないのに起動コマンドを送った")
	}
}

func TestDefaultBaseDirRespectsEnv(t *testing.T) {
	t.Setenv("SOKKI_PANECOM_DIR", "/explicit/dir")
	if got := DefaultBaseDir(); got != "/explicit/dir" {
		t.Errorf("環境変数が優先されない: %q", got)
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

var _ = json.Marshal

// --- REVIEW.md [P1] 保存先の相対パス解決 ---

// output_dir 未指定でも --out を必ず渡し、起動要求時の作業ディレクトリ
// 基準の絶対パスにすること (録音 pane の CWD に左右されない)。
func TestCommandLineAlwaysPassesAbsoluteOutDir(t *testing.T) {
	// 設定ファイルを読ませない (HOME を空のディレクトリに)。
	t.Setenv("HOME", t.TempDir())

	l := NewPaneLauncher("/path/to/sokki", "/base")
	got := l.commandLine("id1", LaunchOptions{WorkDir: "/req/dir"})

	if !strings.Contains(got, "--out ") {
		t.Fatalf("--out が渡されていない: %s", got)
	}
	// 既定の transcripts が要求側 CWD 基準で絶対化されること。
	if !strings.Contains(got, `'/req/dir/transcripts'`) {
		t.Errorf("既定の保存先が要求側基準で絶対化されていない: %s", got)
	}
}

// 設定ファイルの相対 output.directory も要求側の CWD 基準で絶対化すること。
func TestOutputDirFromConfigIsResolvedAgainstRequestDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".config", "sokki")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "output:\n  directory: mtg-logs\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got := ResolveOutputDir(LaunchOptions{WorkDir: "/req/dir"})
	if got != "/req/dir/mtg-logs" {
		t.Errorf("設定の相対パスが要求側基準になっていない: %q", got)
	}
}

// 引数で渡した output_dir が設定より優先されること。
func TestOutputDirArgumentWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".config", "sokki")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"),
		[]byte("output:\n  directory: from-config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ResolveOutputDir(LaunchOptions{OutputDir: "from-arg", WorkDir: "/req/dir"})
	if got != "/req/dir/from-arg" {
		t.Errorf("引数が優先されていない: %q", got)
	}
}

// 絶対パスはそのまま使うこと。
func TestOutputDirAbsoluteIsKept(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := ResolveOutputDir(LaunchOptions{OutputDir: "/abs/out", WorkDir: "/req/dir"})
	if got != "/abs/out" {
		t.Errorf("絶対パスが変わった: %q", got)
	}
}

// mux セッション識別子を録音プロセスへ渡すこと。
func TestCommandLinePassesMuxSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	l := NewPaneLauncher("/path/to/sokki", "/base")
	got := l.commandLine("id1", LaunchOptions{WorkDir: "/w", MuxSession: "zellij:abc"})
	if !strings.Contains(got, "--mux-session 'zellij:abc'") {
		t.Errorf("mux セッションが渡っていない: %s", got)
	}
}

// --- REVIEW.md [P1] starting を成功扱いしない ---

// 記録が starting のまま進まない場合、起動成功として返さないこと。
func TestLaunchDoesNotTreatStartingAsSuccess(t *testing.T) {
	useStateDir(t)
	dir := mockPanecom(t, `
d="$(dirname "$0")"
case "$1" in
  resolve) echo terminal_9 ;;
  send)    echo "$3" > "$d/cmd.txt"; exit 0 ;;
  whoami)  echo developer ;;
esac`)

	l := newLauncher(t)
	l.WaitFor = 1500 * time.Millisecond
	// 準備中 (starting) のまま止まる状況。
	recordOnSend(t, dir, session.StateStarting, "")

	if _, err := l.Launch(context.Background(), LaunchOptions{}); err == nil {
		t.Fatal("starting を起動成功として返した")
	} else if !strings.Contains(err.Error(), "starting") {
		t.Errorf("最後の状態が示されない: %v", err)
	}
}

// loading (モデル読み込み中) も、それだけでは成功にしないこと。
func TestLaunchDoesNotTreatLoadingAsSuccess(t *testing.T) {
	useStateDir(t)
	dir := mockPanecom(t, `
d="$(dirname "$0")"
case "$1" in
  resolve) echo terminal_9 ;;
  send)    echo "$3" > "$d/cmd.txt"; exit 0 ;;
  whoami)  echo developer ;;
esac`)

	l := newLauncher(t)
	l.WaitFor = 1500 * time.Millisecond
	recordOnSend(t, dir, session.StateLoading, "")

	if _, err := l.Launch(context.Background(), LaunchOptions{}); err == nil {
		t.Fatal("loading を起動成功として返した")
	}
}

// starting → failed と進む場合は、原因を返すこと
// (初期化の失敗が成功報告の後に起きない)。
func TestLaunchReportsFailureAfterStarting(t *testing.T) {
	useStateDir(t)
	dir := mockPanecom(t, `
d="$(dirname "$0")"
case "$1" in
  resolve) echo terminal_9 ;;
  send)    echo "$3" > "$d/cmd.txt"; exit 0 ;;
  whoami)  echo developer ;;
esac`)

	l := newLauncher(t)
	l.WaitFor = 5 * time.Second
	go func() {
		id := sessionIDFromCmd(t, filepath.Join(dir, "cmd.txt"))
		writeRecord(t, id, session.StateStarting, "")
		time.Sleep(300 * time.Millisecond)
		writeRecord(t, id, session.StateFailed, "モデルが見つかりません")
	}()

	_, err := l.Launch(context.Background(), LaunchOptions{})
	if err == nil {
		t.Fatal("初期化失敗がエラーにならない")
	}
	if !strings.Contains(err.Error(), "モデルが見つかりません") {
		t.Errorf("原因が返らない: %v", err)
	}
}

// recording になれば成功すること (正常系)。
func TestLaunchSucceedsOnRecording(t *testing.T) {
	useStateDir(t)
	dir := mockPanecom(t, `
d="$(dirname "$0")"
case "$1" in
  resolve) echo terminal_9 ;;
  send)    echo "$3" > "$d/cmd.txt"; exit 0 ;;
  whoami)  echo developer ;;
esac`)

	l := newLauncher(t)
	go func() {
		id := sessionIDFromCmd(t, filepath.Join(dir, "cmd.txt"))
		writeRecord(t, id, session.StateStarting, "")
		time.Sleep(200 * time.Millisecond)
		writeRecord(t, id, session.StateRecording, "")
	}()

	rec, err := l.Launch(context.Background(), LaunchOptions{})
	if err != nil {
		t.Fatalf("recording なのに失敗: %v", err)
	}
	if rec.State != session.StateRecording {
		t.Errorf("状態 = %s", rec.State)
	}
}
