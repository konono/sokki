// Package config は実行時設定とその既定値を提供する。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// SampleRate 以下は whisper の入力仕様に合わせた固定値。
	SampleRate  = 16000
	FrameMillis = 20
	// FrameSamples は VAD が扱う 1 フレームのサンプル数 (20ms = 320)。
	FrameSamples = SampleRate * FrameMillis / 1000

	// DefaultDevice は PLAN.md で指定された Loopback の仮想デバイス。
	DefaultDevice = "Conference Output"
	// DefaultAutoSummary は CLI と MCP に共通の自動要約の既定値。
	DefaultAutoSummary = true
)

// Config は 1 回の実行を決める設定一式。
type Config struct {
	Device    string
	Model     string
	Language  string
	OutputDir string

	// 発話区間検出
	SilenceHangover time.Duration
	MinSpeech       time.Duration
	MaxSegment      time.Duration
	Preroll         time.Duration
	VADFloorRatio   float64
	VADAbsThreshold float64

	// 暫定表示
	ShowInterim     bool
	InterimAfter    time.Duration
	InterimInterval time.Duration

	// 推論バックエンド
	Backend       string // whisper (既定) / qwen
	FinalStrategy string // hybrid (既定) / whole
	QwenPython    string // Qwen 専用の隔離 Python 実行ファイル
	QwenModel     string // ローカルの Qwen3-ASR モデルディレクトリ
	ServerURL     string // 指定時は起動済み whisper-server を利用する
	ServerBin     string
	FFmpegBin     string
	Threads       int
	InitialPrompt string

	// AI 連携 (MCP)
	// EnableAI が true のとき、セッション記録と IPC を用意する。
	EnableAI  bool
	SessionID string
	PaneRole  string
	// MuxSession は Zellij / tmux のセッション識別子 (起動側から渡す)。
	MuxSession string
	// ShowBoard は pane 内を「上: 発言ログ / 下: 会議ボード」に分けるか。
	ShowBoard bool

	// 自動要約 (AI が確定発言から会議ボードを作る)
	AutoSummary     bool
	SummaryProvider string
	SummaryCommand  []string
	SummaryModel    string
	OllamaURL       string
	SummaryMinLines int
	SummaryMinWait  time.Duration
	SummaryMaxWait  time.Duration
	SummaryMaxChars int

	// Optional English-to-Japanese live translation. Empty means disabled.
	TranslateMode string

	// 診断
	Verbose bool
}

// Default は既定値を返す。
func Default() Config {
	threads := runtime.NumCPU() / 2
	if threads < 4 {
		threads = 4
	}
	return Config{
		Device:    DefaultDevice,
		Language:  "ja",
		OutputDir: "transcripts",
		// 約 0.5 秒の無音で発話の終わりとみなし、確定する。
		SilenceHangover: 500 * time.Millisecond,
		MinSpeech:       400 * time.Millisecond,
		// 長い発話は 12 秒で区切って確定する。
		MaxSegment:    12 * time.Second,
		Preroll:       300 * time.Millisecond,
		VADFloorRatio: 3.0,
		// 静かな会議音声も拾えるよう、実機で確認した 0.006 より低い値にする。
		VADAbsThreshold: 0.004,
		ShowInterim:     true,
		// 発話開始から約 1 秒で最初の暫定表示、以後約 1 秒ごとに
		// 発話開始からの音声を再認識して暫定行全体を差し替える。
		InterimAfter:    1000 * time.Millisecond,
		InterimInterval: 1000 * time.Millisecond,
		ShowBoard:       true,
		AutoSummary:     DefaultAutoSummary,
		SummaryProvider: "claude",
		OllamaURL:       "http://127.0.0.1:11434",
		SummaryMinLines: 8,
		SummaryMinWait:  90 * time.Second,
		SummaryMaxWait:  180 * time.Second,
		SummaryMaxChars: 2000,
		ServerBin:       "whisper-server",
		Backend:         "whisper",
		FinalStrategy:   "hybrid",
		FFmpegBin:       "ffmpeg",
		Threads:         threads,
	}
}

// ModelCandidates は --model 未指定時に探索する ggml モデルの既定パス。
//
// PLAN.md の方針どおり Superwhisper がダウンロード済みのモデルを第一候補とし、
// 無ければ talknote 配下にフォールバックする。
// Superwhisper のモデルは `models/` ではなくアプリサポート直下に置かれる
// (`models/` 配下は argmaxinc の CoreML 形式で別物)。
func ModelCandidates() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, "Library/Application Support/superwhisper/ggml-large-v3-turbo.bin"),
		filepath.Join(home, ".local/share/talknote/models/ggml-large-v3-turbo.bin"),
	}
}

// ResolveModel は利用するモデルファイルを決定する。
// 明示指定が無ければ既定候補を順に探し、どれも無ければ対処を含むエラーを返す。
func (c Config) ResolveModel() (string, error) {
	if c.Model != "" {
		if _, err := os.Stat(c.Model); err != nil {
			return "", fmt.Errorf(
				"モデルが見つかりません: %s\n対処: --model に ggml 形式 (.bin) のモデルパスを指定してください",
				c.Model)
		}
		return c.Model, nil
	}
	candidates := ModelCandidates()
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	var b strings.Builder
	b.WriteString("ggml 形式の Whisper モデルが見つかりません。探索した場所:\n")
	for _, p := range candidates {
		fmt.Fprintf(&b, "  - %s\n", p)
	}
	b.WriteString("対処: 次のいずれかでモデルを指定してください\n")
	b.WriteString("      - 設定ファイル: transcription.model: \"/path/to/ggml-model.bin\"\n")
	b.WriteString("      - コマンド:     --model /path/to/ggml-model.bin\n")
	b.WriteString("      - 上記の既定の場所のいずれかにファイルを置く\n")
	b.WriteString("      モデルを持っていない場合は無料で入手できます\n")
	b.WriteString("      (README「モデルを持っていない場合」を参照)")
	return "", fmt.Errorf("%s", b.String())
}
