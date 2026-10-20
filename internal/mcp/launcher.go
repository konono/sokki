package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/konono/sokki/internal/config"
	"github.com/konono/sokki/internal/panecom"
	"github.com/konono/sokki/internal/session"
)

// PaneLauncher は panecom で専用 pane を開き、そこで sokki を起動する。
type PaneLauncher struct {
	// SokkiBin は専用 pane で起動する sokki の絶対パス。
	SokkiBin string
	// RoleName は pane の role 名。
	RoleName string
	// Direction は pane を開く向き ("down" / "right")。
	Direction string
	// WaitFor は記録が現れるまで待つ時間。
	WaitFor time.Duration
	// ExtraArgs は起動コマンドへ追加する引数 (テスト・検証用)。
	ExtraArgs []string
	// BaseDir は panecom を実行する作業ディレクトリ。
	//
	// panecom は呼び出し元 pane が登録済みならその namespace を優先し、
	// 未登録のときだけ作業ディレクトリを使う。open 時の作業ディレクトリは
	// 新しい pane の作業ディレクトリにもなるため、ここを明示する。
	// プロセス全体の作業ディレクトリは変更しない (Cmd.Dir で渡す)。
	BaseDir string
}

// NewPaneLauncher は既定値を入れた PaneLauncher を作る。
func NewPaneLauncher(sokkiBin, baseDir string) *PaneLauncher {
	return &PaneLauncher{
		SokkiBin:  sokkiBin,
		RoleName:  "sokki",
		Direction: "down",
		WaitFor:   90 * time.Second,
		BaseDir:   baseDir,
	}
}

// Available は panecom / 多重化セッションの利用可否を調べる。
func (l *PaneLauncher) Available(ctx context.Context) error {
	return panecom.Check(ctx, l.BaseDir).Error()
}

// Launch は pane を開いて sokki を起動し、記録が現れるまで待つ。
func (l *PaneLauncher) Launch(ctx context.Context, opt LaunchOptions) (*session.Record, error) {
	if l.SokkiBin == "" {
		return nil, fmt.Errorf("sokki の実行ファイルを特定できません")
	}
	if opt.Profile != "" {
		path, err := config.DefaultConfigPath()
		if err != nil {
			return nil, err
		}
		file, err := config.Load(path, false)
		if err != nil {
			return nil, err
		}
		cfg := config.Default()
		if err := file.ApplySelected(&cfg, opt.Profile, path); err != nil {
			return nil, err
		}
		opt.ProfileOutputDir = cfg.OutputDir
	}
	role := l.RoleName

	// 新しい会議は毎回新しい pane に表示する。panecom の register は
	// 同じ role の古い紐付けを新しい pane に移す。
	if err := panecom.Open(ctx, l.BaseDir, role, l.Direction); err != nil {
		return nil, err
	}
	// pane の初期化 (role 登録) を待つ。
	if err := l.waitForRole(ctx, role); err != nil {
		return nil, err
	}

	// 会議 ID はここで決めて sokki に渡す。
	id := session.NewID(time.Now())
	cmdLine := l.commandLine(id, opt)
	if err := panecom.Send(ctx, l.BaseDir, role, cmdLine); err != nil {
		return nil, err
	}

	rec, err := l.waitForRecord(ctx, id)
	if err != nil {
		return nil, fmt.Errorf(
			"%w\n対処: pane '%s' の画面で sokki のエラーを確認してください", err, role)
	}
	return rec, nil
}

// waitForRole は open した pane の role が解決できるまで待つ。
//
// 解決できないまま送ると、録音が始まらないのに成功したように見える。
// panecom の namespace が呼び出し元 pane と一致しない場合はここで止まる
// (音声デバイスは開かない)。
func (l *PaneLauncher) waitForRole(ctx context.Context, role string) error {
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := panecom.Resolve(ctx, l.BaseDir, role); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf(
		"開いた pane の role '%s' を解決できません: %v\n"+
			"対処: AI の pane と録音 pane が同じ panecom namespace になるよう、\n"+
			"      --panecom-dir (または SOKKI_PANECOM_DIR) に AI pane と同じ\n"+
			"      作業ディレクトリを指定してください\n"+
			"      (録音は開始していません)",
		role, lastErr)
}

// commandLine は専用 pane へ送る起動コマンドを組み立てる。
//
// 会議の内容 (要約など) は一切含めない。
// 保存先は起動要求時の作業ディレクトリを基準に絶対パスへ解決し、
// 録音 pane の作業ディレクトリに左右されないようにする。
func (l *PaneLauncher) commandLine(id string, opt LaunchOptions) string {
	parts := []string{
		shellQuote(l.SokkiBin),
		"--pane-role", shellQuote(l.RoleName),
		"--session-id", shellQuote(id),
		"--out", shellQuote(ResolveOutputDir(opt)),
	}
	if opt.Profile != "" {
		parts = append(parts, "--profile", shellQuote(opt.Profile))
	}
	if opt.MuxSession != "" {
		parts = append(parts, "--mux-session", shellQuote(opt.MuxSession))
	}
	if opt.Language != "" {
		parts = append(parts, "--lang", shellQuote(opt.Language))
	}
	if opt.Device != "" {
		parts = append(parts, "--device", shellQuote(opt.Device))
	}
	// 明示的に要約を無効にした場合も CLI の既定値で再有効化されないようにする。
	if opt.AutoSummary {
		if opt.Profile == "" || opt.AutoSummarySpecified {
			parts = append(parts, "--auto-summary")
		}
	} else {
		if opt.Profile == "" || opt.AutoSummarySpecified {
			parts = append(parts, "--auto-summary=false")
		}
	}
	// The provider is shared with translation, including translation-only profiles.
	if opt.SummaryProvider != "" {
		parts = append(parts, "--summary-provider", shellQuote(opt.SummaryProvider))
	}
	for _, a := range l.ExtraArgs {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// ResolveOutputDir は保存先を「CLI 引数 > 設定ファイル > 既定」の順に決め、
// 起動要求時の作業ディレクトリを基準に絶対パスへ直す。
func ResolveOutputDir(opt LaunchOptions) string {
	dir := opt.OutputDir
	if dir == "" {
		if opt.Profile != "" {
			dir = opt.ProfileOutputDir
		} else {
			dir = outputDirFromConfig()
		}
	}
	if dir == "" {
		dir = config.Default().OutputDir
	}
	return AbsOutputDir(dir, opt.WorkDir)
}

// outputDirFromConfig は設定ファイルの output.directory を読む
// (読めない・未設定なら空)。
func outputDirFromConfig() string {
	path, err := config.DefaultConfigPath()
	if err != nil {
		return ""
	}
	file, err := config.Load(path, false)
	if err != nil || file == nil {
		return ""
	}
	cfg := config.Default()
	cfg.OutputDir = ""
	file.Apply(&cfg)
	return cfg.OutputDir
}

// shellQuote はシェルの引数として安全な形にする
// (変数展開やコマンド置換を起こさない)。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// waitForRecord は指定した会議 ID の録音開始を待つ。
//
// pane の画面は解析せず、sokki が書いた記録で起動成否を判定する。
//
// starting / loading は「準備中」であって成功ではない。
// この後にモデル・デバイス・whisper の初期化が失敗しうるため、
// recording になるまで待ち、failed なら原因を返す。
func (l *PaneLauncher) waitForRecord(ctx context.Context, id string) (*session.Record, error) {
	deadline := time.Now().Add(l.WaitFor)
	var last *session.Record
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if r, err := session.Load(id); err == nil {
			last = r
			switch r.State {
			case session.StateFailed:
				msg := r.Error
				if msg == "" {
					msg = "原因不明"
				}
				return nil, fmt.Errorf("sokki の起動に失敗しました: %s", msg)
			case session.StateStopped:
				return nil, fmt.Errorf("sokki が録音を始める前に終了しました")
			case session.StateRecording:
				if r.Active() {
					return r, nil
				}
				return nil, fmt.Errorf("sokki のプロセスが見つかりません (記録は recording)")
			case session.StateStarting, session.StateLoading:
				// 準備中。まだ成功とみなさない。
				if !r.Active() {
					return nil, fmt.Errorf(
						"sokki が準備中に終了しました (最後の状態: %s)", r.State)
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if last != nil {
		return nil, fmt.Errorf(
			"sokki が録音を開始しませんでした (最後の状態: %s, %v 待機)\n"+
				"モデルの読み込みに時間がかかっている可能性があります。"+
				"get_status で状態を確認してください",
			last.State, l.WaitFor)
	}
	return nil, fmt.Errorf("sokki が起動しませんでした (記録が作成されませんでした)")
}

// DefaultBaseDir は panecom を実行する作業ディレクトリを決める。
//
// panecom の namespace は「登録済み pane の namespace」が優先され、
// 未登録なら作業ディレクトリが使われる。MCP サーバーは AI の pane から
// 起動されるため、既定では自分の作業ディレクトリ (= AI pane のもの) を使う。
func DefaultBaseDir() string {
	if d := os.Getenv("SOKKI_PANECOM_DIR"); d != "" {
		return d
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Clean(wd)
}
