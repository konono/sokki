// Package panecom は表示用 pane の作成と参照に使う panecom CLI を包む。
//
// 起動前に利用可否を必ず確認する。利用できない場合は
// 音声デバイスを開かずにエラーを返す
// (画面の無い録音を意図せず始めないため)。
package panecom

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Bin は実行する panecom コマンド (テストで差し替える)。
var Bin = "panecom"

// Availability は panecom を使える状態かどうか。
type Availability struct {
	OK bool
	// Reason は使えない理由 (OK が false のとき)。
	Reason string
	// Mux は検出した多重化ソフト (zellij / tmux)。
	Mux string
}

// Error は利用不可を表すエラー。
func (a Availability) Error() error {
	if a.OK {
		return nil
	}
	return fmt.Errorf(
		"panecom を利用できません: %s\n"+
			"対処: Zellij または tmux のセッション内で、panecom を入れた状態で AI を起動してください\n"+
			"      (画面の無い録音を始めないため、音声デバイスは開いていません)",
		a.Reason)
}

// Check は panecom と多重化セッションの利用可否を調べる。
// 音声デバイスには一切触れない。dir は panecom を実行する作業ディレクトリ。
func Check(ctx context.Context, dir string) Availability {
	if _, err := exec.LookPath(Bin); err != nil {
		return Availability{Reason: fmt.Sprintf("%s が見つかりません", Bin)}
	}
	mux := detectMux()
	if mux == "" {
		return Availability{Reason: "Zellij / tmux のセッション内ではありません"}
	}
	// 実際に panecom が応答するかを確認する (whoami は非破壊)。
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, Bin, "whoami")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return Availability{Reason: fmt.Sprintf("panecom whoami が失敗しました: %s", msg), Mux: mux}
	}
	return Availability{OK: true, Mux: mux}
}

// detectMux は多重化ソフトを環境変数から判定する。
func detectMux() string {
	if os.Getenv("ZELLIJ_SESSION_NAME") != "" || os.Getenv("ZELLIJ") != "" {
		return "zellij"
	}
	if os.Getenv("TMUX") != "" {
		return "tmux"
	}
	return ""
}

// Resolve は role に対応する pane が存在するかを調べる。
func Resolve(ctx context.Context, dir, role string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, Bin, "resolve", role)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("pane '%s' を解決できません: %s",
			role, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Open は role を登録した新しい pane を開く。
//
// direction は "down" / "right"。
func Open(ctx context.Context, dir, role, direction string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"open"}
	if direction != "" {
		args = append(args, "-d", direction)
	}
	args = append(args, role)
	cmd := exec.CommandContext(ctx, Bin, args...)
	// panecom は open 時の作業ディレクトリを新しい pane の
	// 作業ディレクトリ兼 namespace に使うため、明示する。
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pane '%s' を開けません: %s",
			role, strings.TrimSpace(string(out)))
	}
	return nil
}

// Send は pane にコマンド行を送る。
//
// 用途は録音プロセスの起動のみ。要約など会議の内容は
// ローカル IPC で渡し、pane のシェルへは一切注入しない。
func Send(ctx context.Context, dir, role, line string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, Bin, "send", role, line)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pane '%s' にコマンドを送れません: %s",
			role, strings.TrimSpace(string(out)))
	}
	return nil
}
