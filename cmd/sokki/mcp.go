package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/konono/sokki/internal/mcp"
	"github.com/konono/sokki/internal/panecom"
	"github.com/konono/sokki/internal/session"
)

// runMCP は `sokki mcp` を処理する。
//
// AI クライアントが起動する stdio サーバーで、音声デバイスは開かない。
// 接続が終了しても、専用 pane の録音プロセスは動き続ける。
func runMCP(args []string) int {
	// Some MCP clients launch servers with a reduced environment even when the
	// client itself is running in Zellij/tmux. Recover only the mux identity
	// from the direct parent before checking panecom or selecting a meeting.
	recoverMuxEnvFromParent()
	var (
		sokkiBin string
		baseDir  string
		role     string
		check    bool
	)
	fs := flag.NewFlagSet("sokki mcp", flag.ExitOnError)
	fs.StringVar(&sokkiBin, "sokki-bin", "", "録音に使う sokki の実行ファイル (既定: 自分自身)")
	fs.StringVar(&baseDir, "panecom-dir", "",
		"panecom の role 解決を行う作業ディレクトリ (既定: .panecom を持つ親、または $SOKKI_PANECOM_DIR)")
	fs.StringVar(&role, "pane-role", "sokki", "録音用 pane の role 名")
	fs.BoolVar(&check, "check-panecom", false, "panecom とセッションを確認して終了する (録音しない)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `sokki mcp - AI (MCP) 用の stdio サーバー

使い方:
  sokki mcp [options]

AI クライアントの MCP 設定から起動します。音声デバイスは開きません。
録音は panecom の専用 pane で独立して動き、この接続を閉じても止まりません。

オプション:
`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if sokkiBin == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "エラー: sokki の実行ファイルを特定できません: %v\n", err)
			return 1
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		sokkiBin = exe
	}
	if baseDir == "" {
		baseDir = mcp.DefaultBaseDir()
	}
	if check {
		if err := panecom.Check(context.Background(), baseDir).Error(); err != nil {
			fmt.Fprintf(os.Stderr, "エラー: %v\n", err)
			return 1
		}
		fmt.Printf("panecom: 利用可能 (%s)\n", session.CurrentMuxSession())
		return 0
	}

	launcher := mcp.NewPaneLauncher(sokkiBin, baseDir)
	launcher.RoleName = role

	// 接続の終了で録音を止めないため、ここで扱うのは自プロセスの終了だけ。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := mcp.New(launcher).Serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "エラー: %v\n", err)
		return 1
	}
	return 0
}
