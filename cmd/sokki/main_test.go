package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/konono/sokki/internal/mcp"
	"github.com/konono/sokki/internal/panecom"
	"github.com/konono/sokki/internal/session"
	"testing"
)

var (
	buildOnce sync.Once
	builtBin  string
	buildErr  error
	buildOut  []byte
)

// buildSokki は被テスト用のバイナリを作る。
//
// パッケージ全体で 1 回だけビルドする。テストごとにビルドすると
// `go test -race ./...` で他パッケージと資源を奪い合い、
// テスト全体がタイムアウトする。
func buildSokki(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sokki-bin")
		if err != nil {
			buildErr = err
			return
		}
		bin := filepath.Join(dir, "sokki")
		buildOut, buildErr = exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
		if buildErr == nil {
			builtBin = bin
		}
	})
	if buildErr != nil {
		t.Fatalf("ビルド失敗: %v\n%s", buildErr, buildOut)
	}
	return builtBin
}

// runSokki は環境変数 HOME を差し替えて sokki を実行する。
// 音声キャプチャは開始しない経路だけを対象にする。
func runSokki(t *testing.T, bin, home string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("実行失敗: %v\n%s", err, out)
	}
	return string(out), code
}

// 壊れた設定ファイルを置いたホームを用意する。
func homeWithBrokenConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "sokki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "capture:\n  bogus: 1\n  device: \"\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestListShowsDeviceNames(t *testing.T) {
	bin := buildSokki(t)
	out, code := runSokki(t, bin, t.TempDir(), "list")
	if code != 0 {
		t.Fatalf("exit=%d\n%s", code, out)
	}
	for _, want := range []string{"capture.device", "capture:", "device:", "sokki --device"} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い:\n%s", want, out)
		}
	}
}

// 設定ファイルが壊れていても list は動くこと (名前を調べて直せるように)。
func TestListWorksWithBrokenConfig(t *testing.T) {
	bin := buildSokki(t)
	home := homeWithBrokenConfig(t)

	out, code := runSokki(t, bin, home, "list")
	if code != 0 {
		t.Fatalf("壊れた設定で list が失敗した: exit=%d\n%s", code, out)
	}
	if !strings.Contains(out, "capture.device") {
		t.Errorf("デバイス一覧が出ていない:\n%s", out)
	}
	// 設定ファイルのエラーに巻き込まれていないこと。
	if strings.Contains(out, "設定ファイルの内容が不正です") {
		t.Errorf("list が設定ファイルを読んでしまっている:\n%s", out)
	}
}

// --config で壊れた設定を明示しても list は影響を受けないこと。
func TestListIgnoresExplicitConfigFlag(t *testing.T) {
	bin := buildSokki(t)
	home := homeWithBrokenConfig(t)
	broken := filepath.Join(home, ".config", "sokki", "config.yaml")

	out, code := runSokki(t, bin, home, "list", "--config", broken)
	// --config は list のフラグではないため、未知フラグとして終了してもよいが、
	// 設定ファイルの解析エラーで落ちてはいけない。
	if strings.Contains(out, "設定ファイルの内容が不正です") {
		t.Errorf("list が設定ファイルを解析した (exit=%d):\n%s", code, out)
	}
}

// 同じ壊れた設定でも、通常実行はきちんとエラーで止まること (対比)。
func TestNormalRunFailsOnBrokenConfig(t *testing.T) {
	bin := buildSokki(t)
	home := homeWithBrokenConfig(t)

	out, code := runSokki(t, bin, home, "--device", "ZZZ_NO_SUCH_DEVICE")
	if code == 0 {
		t.Fatalf("壊れた設定で正常終了した:\n%s", out)
	}
	if !strings.Contains(out, "設定ファイルの内容が不正です") {
		t.Errorf("設定エラーが報告されていない:\n%s", out)
	}
}

// --list-devices は互換のため残し、list と同じ出力になること。
func TestListDevicesFlagStillWorks(t *testing.T) {
	bin := buildSokki(t)
	home := t.TempDir()

	flagOut, flagCode := runSokki(t, bin, home, "--list-devices")
	subOut, subCode := runSokki(t, bin, home, "list")
	if flagCode != 0 || subCode != 0 {
		t.Fatalf("exit: --list-devices=%d list=%d", flagCode, subCode)
	}
	// 2 回の列挙の間にデバイスが抜き差しされると名前の集合は変わりうるので、
	// 同じ経路を通っていることを示す安定部分で比較する。
	for _, want := range []string{
		"音声入力デバイス一覧 (capture.device に指定できる名前):",
		"設定ファイル (~/.config/sokki/config.yaml) に書く場合:",
		"capture:",
		"sokki --device",
	} {
		if !strings.Contains(flagOut, want) {
			t.Errorf("--list-devices の出力に %q が無い:\n%s", want, flagOut)
		}
		if !strings.Contains(subOut, want) {
			t.Errorf("list の出力に %q が無い:\n%s", want, subOut)
		}
	}
}

// --list-devices も壊れた設定に影響されないこと。
func TestListDevicesFlagWorksWithBrokenConfig(t *testing.T) {
	bin := buildSokki(t)
	out, code := runSokki(t, bin, homeWithBrokenConfig(t), "--list-devices")
	if code != 0 {
		t.Fatalf("壊れた設定で --list-devices が失敗した: exit=%d\n%s", code, out)
	}
	if strings.Contains(out, "設定ファイルの内容が不正です") {
		t.Errorf("--list-devices が設定ファイルを読んでしまっている:\n%s", out)
	}
}

// list は設定ファイルもモデルも読まないので、モデルが無くても動くこと。
func TestListDoesNotRequireModel(t *testing.T) {
	bin := buildSokki(t)
	// モデルの既定探索先が存在しないホームで実行する。
	out, code := runSokki(t, bin, t.TempDir(), "list")
	if code != 0 {
		t.Fatalf("モデル無しで list が失敗した: exit=%d\n%s", code, out)
	}
	if strings.Contains(out, "モデルが見つかりません") {
		t.Errorf("list がモデルを要求している:\n%s", out)
	}
}

// ヘルプに list の案内があること。
func TestUsageMentionsListSubcommand(t *testing.T) {
	bin := buildSokki(t)
	out, _ := runSokki(t, bin, t.TempDir(), "--help")
	if !strings.Contains(out, "sokki list") {
		t.Errorf("ヘルプに `sokki list` の案内が無い:\n%s", out)
	}
}

// TestMain は共有ビルド成果物を後片付けする。
func TestMain(m *testing.M) {
	code := m.Run()
	if builtBin != "" {
		_ = os.RemoveAll(filepath.Dir(builtBin))
	}
	os.Exit(code)
}

// --- sokki mcp (AI 連携) ---

// runSokkiStdin は標準入力を与えて sokki を実行する。
func runSokkiStdin(t *testing.T, bin, home, stdin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("実行失敗: %v\n%s", err, out)
	}
	return string(out), code
}

// `sokki mcp` が MCP の初期化とツール一覧に応答すること。
// 音声デバイスは開かない。
func TestMCPRespondsToInitializeAndToolsList(t *testing.T) {
	bin := buildSokki(t)
	in := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"

	out, code := runSokkiStdin(t, bin, t.TempDir(), in, "mcp")
	if code != 0 {
		t.Fatalf("exit=%d\n%s", code, out)
	}
	for _, want := range []string{
		`"serverInfo"`, `"sokki"`,
		"start_or_attach", "get_status", "read_transcript",
		"read_context", "publish_summary", "stop_session",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("応答に %q が無い:\n%s", want, out)
		}
	}
}

// 多重化セッションの外では、start_or_attach が音声デバイスを開かずに失敗すること。
func TestMCPStartRefusesWithoutPanecom(t *testing.T) {
	bin := buildSokki(t)
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"start_or_attach","arguments":{}}}` + "\n"

	cmd := exec.Command(bin, "mcp")
	// Zellij / tmux の外を再現する。
	env := []string{}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "ZELLIJ") || strings.HasPrefix(e, "TMUX") {
			continue
		}
		env = append(env, e)
	}
	cmd.Env = append(env, "HOME="+t.TempDir(), "SOKKI_DISABLE_PARENT_MUX=1")
	cmd.Stdin = strings.NewReader(in)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("実行失敗: %v\n%s", err, out)
		}
	}
	text := string(out)
	if !strings.Contains(text, "isError") || !strings.Contains(text, "true") {
		t.Errorf("エラーとして返っていない:\n%s", text)
	}
	if !strings.Contains(text, "panecom") {
		t.Errorf("panecom が理由として示されていない:\n%s", text)
	}
	// 音声デバイスを開いていないことを明示すること。
	if !strings.Contains(text, "音声デバイス") {
		t.Errorf("デバイスを開いていない旨が無い:\n%s", text)
	}
}

// 接続 (標準入力) が閉じても、エラー終了しないこと。
// 録音プロセスは別 pane で動くため、この終了では止まらない。
func TestMCPExitsCleanlyOnEOF(t *testing.T) {
	bin := buildSokki(t)
	out, code := runSokkiStdin(t, bin, t.TempDir(), "", "mcp")
	if code != 0 {
		t.Fatalf("EOF で異常終了した: exit=%d\n%s", code, out)
	}
}

// ヘルプに mcp サブコマンドの案内があること。
func TestUsageMentionsMCPSubcommand(t *testing.T) {
	bin := buildSokki(t)
	out, _ := runSokki(t, bin, t.TempDir(), "--help")
	if !strings.Contains(out, "sokki mcp") {
		t.Errorf("ヘルプに `sokki mcp` の案内が無い:\n%s", out)
	}
}

// `sokki mcp --help` が使い方を示すこと。
func TestMCPHelp(t *testing.T) {
	bin := buildSokki(t)
	out, _ := runSokki(t, bin, t.TempDir(), "mcp", "--help")
	for _, want := range []string{"stdio", "音声デバイスは開きません", "録音は"} {
		if !strings.Contains(out, want) {
			t.Errorf("ヘルプに %q が無い:\n%s", want, out)
		}
	}
}

// --- 起動前の失敗がセッション記録に残ること (REVIEW.md [P1]) ---

// stateDirFor は HOME 配下のセッション記録ディレクトリを返す。
func stateDirFor(home string) string {
	return filepath.Join(home, ".local", "state", "sokki", "sessions")
}

// readSessionRecord は会議 ID の記録を読む (JSON をそのまま map で)。
func readSessionRecord(t *testing.T, home, id string) map[string]any {
	t.Helper()
	path := filepath.Join(stateDirFor(home), id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		entries, _ := os.ReadDir(stateDirFor(home))
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("セッション記録が無い (%s)。存在するのは %v", path, names)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("記録を解釈できない: %v\n%s", err, data)
	}
	return m
}

// モデル不在で終了した場合も、原因付きの failed 記録が残ること。
// 音声デバイスは開かない (モデル解決の段階で失敗する)。
func TestFailedBeforeCaptureIsRecorded(t *testing.T) {
	bin := buildSokki(t)
	home := t.TempDir()
	const id = "review-missing-model"

	out, code := runSokki(t, bin, home,
		"--pane-role", "sokki",
		"--session-id", id,
		"--model", filepath.Join(t.TempDir(), "does-not-exist.bin"))
	if code == 0 {
		t.Fatalf("モデル不在で成功した:\n%s", out)
	}

	rec := readSessionRecord(t, home, id)
	if rec["state"] != "failed" {
		t.Errorf("状態 = %v, want failed\n%+v", rec["state"], rec)
	}
	msg, _ := rec["error"].(string)
	if !strings.Contains(msg, "モデルが見つかりません") {
		t.Errorf("原因が残っていない: %q", msg)
	}
	if rec["pane_role"] != "sokki" {
		t.Errorf("pane role が残っていない: %+v", rec)
	}
}

// デバイス不在で終了した場合も failed として残ること。
func TestFailedDeviceIsRecorded(t *testing.T) {
	bin := buildSokki(t)
	home := t.TempDir()
	const id = "review-missing-device"

	// モデルは存在するダミーを渡し、デバイス解決で失敗させる。
	model := filepath.Join(t.TempDir(), "dummy.bin")
	if err := os.WriteFile(model, []byte("ggml"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runSokki(t, bin, home,
		"--pane-role", "sokki",
		"--session-id", id,
		"--model", model,
		"--device", "ZZZ_NO_SUCH_DEVICE_FOR_TEST")
	if code == 0 {
		t.Fatalf("デバイス不在で成功した:\n%s", out)
	}

	rec := readSessionRecord(t, home, id)
	if rec["state"] != "failed" {
		t.Errorf("状態 = %v, want failed\n%+v", rec["state"], rec)
	}
	msg, _ := rec["error"].(string)
	if !strings.Contains(msg, "見つかりません") {
		t.Errorf("原因が残っていない: %q", msg)
	}
}

// 外部 URL 指定のような起動前エラーも failed として残ること。
func TestFailedServerURLIsRecorded(t *testing.T) {
	bin := buildSokki(t)
	home := t.TempDir()
	const id = "review-bad-url"

	_, code := runSokki(t, bin, home,
		"--pane-role", "sokki",
		"--session-id", id,
		"--server-url", "https://api.openai.com")
	if code == 0 {
		t.Fatal("外部 URL が許可された")
	}
	rec := readSessionRecord(t, home, id)
	if rec["state"] != "failed" {
		t.Errorf("状態 = %v, want failed", rec["state"])
	}
}

// AI 連携を使わない手動実行では、セッション記録を作らないこと。
func TestManualRunDoesNotCreateSessionRecord(t *testing.T) {
	bin := buildSokki(t)
	home := t.TempDir()

	runSokki(t, bin, home, "--device", "ZZZ_NO_SUCH_DEVICE_FOR_TEST")

	if entries, err := os.ReadDir(stateDirFor(home)); err == nil && len(entries) > 0 {
		t.Errorf("手動実行で記録が作られた: %d 件", len(entries))
	}
}

// 設定ファイルのエラーも failed 記録に残ること (Run より前の失敗)。
func TestFailedConfigIsRecorded(t *testing.T) {
	bin := buildSokki(t)
	home := t.TempDir()
	const id = "review-bad-config"

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("capture:\n  devicce: \"X\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runSokki(t, bin, home,
		"--pane-role", "sokki", "--session-id", id, "--config", bad)
	if code == 0 {
		t.Fatalf("不正な設定で成功した:\n%s", out)
	}

	rec := readSessionRecord(t, home, id)
	if rec["state"] != "failed" {
		t.Errorf("状態 = %v, want failed\n%+v", rec["state"], rec)
	}
	msg, _ := rec["error"].(string)
	if !strings.Contains(msg, "設定ファイル") {
		t.Errorf("原因が残っていない: %q", msg)
	}
}

// 設定エラーでも、手動実行なら記録を作らないこと。
func TestManualConfigErrorDoesNotRecord(t *testing.T) {
	bin := buildSokki(t)
	home := t.TempDir()
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("nope: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSokki(t, bin, home, "--config", bad)
	if entries, err := os.ReadDir(stateDirFor(home)); err == nil && len(entries) > 0 {
		t.Errorf("手動実行で記録が作られた: %d 件", len(entries))
	}
}

// 起動前に失敗した場合、記録に mux セッションも残ること
// (AI が所属を確認できるように)。
func TestFailedRecordKeepsMuxSession(t *testing.T) {
	bin := buildSokki(t)
	home := t.TempDir()
	const id = "review-mux-on-failure"

	cmd := exec.Command(bin, "--pane-role", "sokki", "--session-id", id,
		"--model", filepath.Join(t.TempDir(), "none.bin"))
	cmd.Env = append(os.Environ(), "HOME="+home, "ZELLIJ_SESSION_NAME=itest-mux", "TMUX=")
	_ = cmd.Run()

	rec := readSessionRecord(t, home, id)
	if rec["mux_session"] != "zellij:itest-mux" {
		t.Errorf("mux セッションが残っていない: %+v", rec["mux_session"])
	}
}

// --- 実 CLI の起動失敗が launcher の応答まで届くこと (REVIEW.md [P1]) ---

// mockPanecomRunning は send されたコマンドを実際に実行する模擬 panecom。
// 実 pane は開かず、sh でバックグラウンド実行する。
func mockPanecomRunning(t *testing.T, home string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "panecom")
	script := `#!/bin/sh
d="$(dirname "$0")"
case "$1" in
  whoami)  echo developer ;;
  resolve) echo terminal_9 ;;
  open)    exit 0 ;;
  send)    echo "$3" > "$d/cmd.txt"; ( HOME="` + home + `" sh -c "$3" > "$d/out.txt" 2>&1 & ) ; exit 0 ;;
esac`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := panecom.Bin
	panecom.Bin = path
	t.Cleanup(func() { panecom.Bin = old })
	return dir
}

// 実 sokki がモデル不在で終了した場合、start_or_attach が原因を返すこと。
// 音声デバイスは開かない (モデル解決で失敗する)。
func TestLauncherSurfacesRealCLIFailure(t *testing.T) {
	bin := buildSokki(t)
	home := t.TempDir()

	// launcher 側 (テストプロセス) も同じ HOME を見るようにする。
	t.Setenv("HOME", home)
	t.Setenv("ZELLIJ_SESSION_NAME", "itest-mux")
	t.Setenv("TMUX", "")
	mockPanecomRunning(t, home)

	l := mcp.NewPaneLauncher(bin, t.TempDir())
	l.WaitFor = 20 * time.Second
	// モデル不在で必ず失敗させる。
	l.SokkiBin = bin
	srv := mcp.New(&modelLessLauncher{PaneLauncher: l, model: filepath.Join(t.TempDir(), "none.bin")})

	raw, _ := json.Marshal(map[string]any{})
	_, err := srv.RunToolForTest("start_or_attach", raw)
	if err == nil {
		t.Fatal("実 CLI が失敗したのに成功を返した")
	}
	if !strings.Contains(err.Error(), "モデルが見つかりません") {
		t.Errorf("実 CLI の原因が届いていない: %v", err)
	}
}

// modelLessLauncher は起動コマンドに --model を足して必ず失敗させる。
type modelLessLauncher struct {
	*mcp.PaneLauncher
	model string
}

func (m *modelLessLauncher) Launch(ctx context.Context, opt mcp.LaunchOptions) (*session.Record, error) {
	m.PaneLauncher.ExtraArgs = []string{"--model", m.model}
	return m.PaneLauncher.Launch(ctx, opt)
}
