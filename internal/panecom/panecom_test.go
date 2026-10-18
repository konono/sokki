package panecom

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mockBin は模擬 panecom を作る。実際の pane は開かない。
//
// script は shell の本体。引数は "$@" で受け取れる。
func mockBin(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "panecom")
	body := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	old := Bin
	Bin = path
	t.Cleanup(func() { Bin = old })
	return dir
}

// inMux は多重化セッション内に居る状態を作る。
func inMux(t *testing.T) {
	t.Helper()
	t.Setenv("ZELLIJ_SESSION_NAME", "test-session")
	t.Setenv("ZELLIJ_PANE_ID", "42")
	t.Setenv("TMUX", "")
}

func TestCheckOKInsideMux(t *testing.T) {
	inMux(t)
	mockBin(t, `echo developer`)

	av := Check(context.Background(), "")
	if !av.OK {
		t.Fatalf("利用可能と判定されない: %+v", av)
	}
	if av.Mux != "zellij" {
		t.Errorf("mux = %q", av.Mux)
	}
	if av.Error() != nil {
		t.Errorf("OK なのにエラー: %v", av.Error())
	}
}

// 多重化セッションの外では利用不可とすること。
func TestCheckFailsOutsideMux(t *testing.T) {
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	t.Setenv("ZELLIJ_PANE_ID", "")
	t.Setenv("ZELLIJ", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	mockBin(t, `echo developer`)

	av := Check(context.Background(), "")
	if av.OK {
		t.Fatal("セッション外なのに利用可能と判定された")
	}
	err := av.Error()
	if err == nil {
		t.Fatal("エラーが返らない")
	}
	// 音声デバイスを開いていないことを明示していること。
	for _, want := range []string{"Zellij", "tmux", "対処:", "音声デバイスは開いていません"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("案内に %q が無い:\n%v", want, err)
		}
	}
}

// panecom が無ければ利用不可とすること。
func TestCheckFailsWithoutBinary(t *testing.T) {
	inMux(t)
	old := Bin
	Bin = filepath.Join(t.TempDir(), "no-such-panecom")
	t.Cleanup(func() { Bin = old })

	av := Check(context.Background(), "")
	if av.OK {
		t.Fatal("panecom が無いのに利用可能と判定された")
	}
	if !strings.Contains(av.Reason, "見つかりません") {
		t.Errorf("理由が不明: %q", av.Reason)
	}
}

// panecom が異常終了する場合も利用不可とすること。
func TestCheckFailsWhenWhoamiFails(t *testing.T) {
	inMux(t)
	mockBin(t, `echo "no session" >&2; exit 1`)

	av := Check(context.Background(), "")
	if av.OK {
		t.Fatal("whoami 失敗なのに利用可能と判定された")
	}
	if !strings.Contains(av.Reason, "whoami") {
		t.Errorf("理由が不明: %q", av.Reason)
	}
}

func TestResolveReturnsPaneID(t *testing.T) {
	inMux(t)
	mockBin(t, `case "$1" in resolve) echo terminal_42 ;; *) exit 1 ;; esac`)

	id, err := Resolve(context.Background(), "", "sokki")
	if err != nil {
		t.Fatal(err)
	}
	if id != "terminal_42" {
		t.Errorf("pane ID = %q", id)
	}
}

func TestResolveFailsForUnknownRole(t *testing.T) {
	inMux(t)
	mockBin(t, `echo "unknown role" >&2; exit 1`)

	if _, err := Resolve(context.Background(), "", "nope"); err == nil {
		t.Fatal("未知の role がエラーにならない")
	}
}

// Open が向きと role を正しく渡すこと。
func TestOpenPassesDirectionAndRole(t *testing.T) {
	inMux(t)
	dir := mockBin(t, `echo "$@" > "$(dirname "$0")/args.txt"`)

	if err := Open(context.Background(), "", "sokki", "down"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "open -d down sokki" {
		t.Errorf("引数 = %q", got)
	}
}

func TestOpenReportsFailure(t *testing.T) {
	inMux(t)
	mockBin(t, `echo "cannot open" >&2; exit 1`)
	if err := Open(context.Background(), "", "sokki", "down"); err == nil {
		t.Fatal("失敗がエラーにならない")
	}
}

// Send はコマンド行をそのまま渡すこと。
func TestSendPassesCommandLine(t *testing.T) {
	inMux(t)
	dir := mockBin(t, `printf '%s\n' "$1" "$2" "$3" > "$(dirname "$0")/args.txt"`)

	if err := Send(context.Background(), "", "sokki", "/bin/sokki --pane-role 'sokki'"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 || lines[0] != "send" || lines[1] != "sokki" {
		t.Fatalf("引数が不正: %+v", lines)
	}
	if lines[2] != "/bin/sokki --pane-role 'sokki'" {
		t.Errorf("コマンド行が変わった: %q", lines[2])
	}
}
