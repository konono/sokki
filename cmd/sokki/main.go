// Command sokki は Loopback の会議音声をローカルで日本語文字起こしする。
//
// 音声は avfoundation (ffmpeg) から取り込み、推論はローカルの
// whisper.cpp (whisper-server) を外部プロセスとして使う。
// 自動要約が有効な場合、確定テキストは設定した AI プロバイダーへ送る。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"flag"

	"github.com/konono/sokki/internal/app"
	"github.com/konono/sokki/internal/audio"
	"github.com/konono/sokki/internal/config"
	"github.com/konono/sokki/internal/session"
	"github.com/konono/sokki/internal/ui"
)

func main() {
	// `sokki list` は設定ファイル・モデルの読込より前に処理する。
	// 設定が壊れていても、デバイス名を確認して直せるようにするため。
	if len(os.Args) > 1 && os.Args[1] == "list" {
		os.Exit(runList(os.Args[2:]))
	}
	// `sokki mcp` は AI クライアントが起動する stdio サーバー。
	// 音声デバイスは開かない (録音は専用 pane の別プロセスで動く)。
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		os.Exit(runMCP(os.Args[2:]))
	}

	defaults := config.Default()
	cfg := defaults
	var (
		listDevices bool
		configPath  string
	)

	fs := flag.NewFlagSet("sokki", flag.ExitOnError)
	fs.StringVar(&configPath, "config", "",
		"設定ファイルのパス (既定: ~/.config/sokki/config.yaml があれば自動で読む)")
	fs.StringVar(&cfg.Device, "device", cfg.Device, "入力デバイス名 (avfoundation)")
	fs.StringVar(&cfg.Model, "model", "", "ggml モデルのパス (既定: talknote の large-v3-turbo)")
	fs.StringVar(&cfg.Backend, "backend", cfg.Backend, "認識 backend: whisper / qwen")
	fs.StringVar(&cfg.FinalStrategy, "final-strategy", cfg.FinalStrategy, "確定文の処理: hybrid / whole")
	fs.StringVar(&cfg.QwenPython, "qwen-python", "", "Qwen 用の隔離 Python 実行ファイル (絶対パス)")
	fs.StringVar(&cfg.QwenModel, "qwen-model", "", "ローカル Qwen3-ASR モデルディレクトリ (絶対パス)")
	fs.StringVar(&cfg.Language, "lang", cfg.Language, "認識する言語")
	fs.StringVar(&cfg.Language, "l", cfg.Language, "認識する言語 (--lang の短縮形)")
	fs.StringVar(&cfg.OutputDir, "out", cfg.OutputDir, "文字起こしの保存先ディレクトリ")
	fs.BoolVar(&listDevices, "list-devices", false,
		"音声入力デバイス一覧を表示して終了 (`sokki list` と同じ)")
	fs.BoolVar(&cfg.ShowInterim, "interim", cfg.ShowInterim, "暫定結果を表示する")
	fs.IntVar(&cfg.Threads, "threads", cfg.Threads, "推論スレッド数")
	fs.StringVar(&cfg.ServerURL, "server-url", "", "起動済み whisper-server の URL (例 http://127.0.0.1:8080)")
	fs.StringVar(&cfg.ServerBin, "server-bin", cfg.ServerBin, "whisper-server の実行ファイル")
	fs.StringVar(&cfg.FFmpegBin, "ffmpeg", cfg.FFmpegBin, "ffmpeg の実行ファイル")
	fs.StringVar(&cfg.InitialPrompt, "prompt", "", "認識のヒントになる初期プロンプト (固有名詞など)")
	fs.StringVar(&cfg.PaneRole, "pane-role", "",
		"AI 連携時の panecom role 名 (指定すると MCP から操作できる)")
	fs.StringVar(&cfg.SessionID, "session-id", "", "会議 ID (省略時は開始時刻から生成)")
	fs.StringVar(&cfg.MuxSession, "mux-session", "",
		"Zellij / tmux のセッション識別子 (省略時は環境から判定)")
	fs.BoolVar(&cfg.ShowBoard, "board", cfg.ShowBoard, "pane 内に会議ボードを表示する")
	fs.BoolVar(&cfg.AutoSummary, "auto-summary", cfg.AutoSummary,
		"確定した発言から会議ボードを自動更新する (AI を呼ぶ)")
	fs.StringVar(&cfg.SummaryProvider, "summary-provider", cfg.SummaryProvider,
		"自動要約に使う AI (claude / ollama。その他は summary.command で指定)")
	fs.StringVar(&cfg.SummaryModel, "summary-model", "", "自動要約のモデル (省略時は CLI の既定)")
	fs.StringVar(&cfg.OllamaURL, "summary-ollama-url", cfg.OllamaURL, "要約用の既存 Ollama server URL (loopback のみ)")
	fs.StringVar(&cfg.TranslateMode, "translate", "", "ライブ翻訳: en-ja (既定: 無効)")
	fs.StringVar(&cfg.SummaryProvider, "llm-provider", cfg.SummaryProvider, "要約と翻訳に共通の AI (claude / ollama)")
	fs.StringVar(&cfg.SummaryModel, "llm-model", "", "要約と翻訳に共通のモデル")
	fs.StringVar(&cfg.OllamaURL, "llm-ollama-url", cfg.OllamaURL, "共通の Ollama server URL (loopback のみ)")
	fs.DurationVar(&cfg.SilenceHangover, "silence", cfg.SilenceHangover, "この長さの無音で発話を確定する")
	fs.DurationVar(&cfg.InterimAfter, "interim-after", cfg.InterimAfter,
		"発話開始から最初の暫定表示までの時間")
	fs.DurationVar(&cfg.InterimInterval, "interim-interval", cfg.InterimInterval,
		"暫定表示を更新する間隔")
	fs.DurationVar(&cfg.MaxSegment, "max-segment", cfg.MaxSegment, "1 発話区間の最大長")
	fs.Float64Var(&cfg.VADAbsThreshold, "vad-threshold", cfg.VADAbsThreshold, "発話判定の下限しきい値 (0-1)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `sokki - 会議音声のローカル文字起こし

使い方:
  sokki [options]
  sokki list                             # 入力デバイス名の一覧
  sokki mcp                              # AI (MCP) 用の stdio サーバー

例:
  sokki                                  # 設定したデバイスから文字起こし
  sokki list                             # capture.device に書く名前を調べる
  sokki --device "BlackHole 2ch"         # 入力デバイスを変更
  sokki --config ./my.yaml               # 設定ファイルを指定
  sokki --model /path/to/ggml-model.bin  # モデルを変更

オプション:
`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(os.Args[1:])

	// --list-devices も設定ファイルの読込より前に処理する
	// (`sokki list` と同じ理由。互換のため残しているフラグ)。
	if listDevices {
		os.Exit(runList([]string{"--ffmpeg", cfg.FFmpegBin}))
	}

	// 設定ファイルを読み、「明示した CLI フラグ > YAML > 既定値」で確定する。
	file, err := loadConfigFile(configPath)
	if err != nil {
		// AI から起動された場合は、設定エラーも会議記録に残す
		// (記録が無いと AI が原因を取得できず待ち続ける)。
		recordStartupFailure(cfg, err)
		fmt.Fprintf(os.Stderr, "エラー: %v\n", err)
		os.Exit(1)
	}
	if err := config.ValidateLLMFlags(fs); err != nil {
		recordStartupFailure(cfg, err)
		fmt.Fprintf(os.Stderr, "エラー: %v\n", err)
		os.Exit(1)
	}
	cfg = config.Resolve(defaults, cfg, fs, file)
	// pane role か会議 ID が指定されていれば AI 連携を有効にする。
	cfg.EnableAI = cfg.PaneRole != "" || cfg.SessionID != ""
	if cfg.EnableAI && cfg.MuxSession == "" {
		cfg.MuxSession = session.CurrentMuxSession()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	con := ui.New()
	if file != nil && configPath != "" {
		con.Info("設定ファイル : %s", configPath)
	}
	if err := app.Run(ctx, cfg, con); err != nil {
		fmt.Fprintf(os.Stderr, "\nエラー: %v\n", err)
		os.Exit(1)
	}
}

// recordStartupFailure は Run に入る前の失敗を会議記録に残す。
//
// 設定ファイルの読込エラーなど、app.Run より前で終了する場合でも
// AI が原因を取得できるようにする。
func recordStartupFailure(cfg config.Config, cause error) {
	if cfg.PaneRole == "" && cfg.SessionID == "" {
		return // 手動実行では記録を作らない
	}
	id := cfg.SessionID
	if id == "" {
		id = session.NewID(time.Now())
	}
	mux := cfg.MuxSession
	if mux == "" {
		mux = session.CurrentMuxSession()
	}
	_ = session.Save(&session.Record{
		ID:         id,
		PID:        os.Getpid(),
		State:      session.StateFailed,
		PaneRole:   cfg.PaneRole,
		MuxSession: mux,
		StartedAt:  time.Now(),
		Error:      cause.Error(),
	})
}

// loadConfigFile は設定ファイルを読む。
// --config が指定されていればそれを必須として読み、
// 未指定なら ~/.config/sokki/config.yaml があれば読む (無ければ既定値)。
func loadConfigFile(explicitPath string) (*config.File, error) {
	if explicitPath != "" {
		return config.Load(explicitPath, true)
	}
	path, err := config.DefaultConfigPath()
	if err != nil {
		// ホームディレクトリが解決できない場合は既定値で動く。
		return nil, nil
	}
	return config.Load(path, false)
}

// runList は `sokki list` の処理。
//
// AVFoundation のデバイス一覧を取得して表示するだけで、
// 音声のキャプチャは開始しない。設定ファイルもモデルも読まないため、
// 設定が壊れていてもデバイス名を確認できる。
func runList(args []string) int {
	ffmpegBin := config.Default().FFmpegBin

	fs := flag.NewFlagSet("sokki list", flag.ExitOnError)
	fs.StringVar(&ffmpegBin, "ffmpeg", ffmpegBin, "ffmpeg の実行ファイル")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `sokki list - 音声入力デバイス名の一覧

使い方:
  sokki list [--ffmpeg PATH]

表示された名前を設定ファイルの capture.device にコピーして使います。
設定ファイルは読み込まないため、設定が壊れていても実行できます。

オプション:
`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	devices, err := audio.ListDevices(ctx, ffmpegBin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "エラー: %v\n", err)
		return 1
	}
	fmt.Println(audio.FormatDeviceList(devices))
	return 0
}
