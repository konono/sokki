package app

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/konono/sokki/internal/board"
	"github.com/konono/sokki/internal/config"
	"github.com/konono/sokki/internal/ipc"
	"github.com/konono/sokki/internal/session"
	"github.com/konono/sokki/internal/summarizer"
	"github.com/konono/sokki/internal/ui"
)

// newView は画面出力を決める。
//
// pane role が指定され (AI から起動され) かつ TTY の場合だけ上下分割にする。
// 手動実行や非 TTY は従来どおりの行出力を保つ。
func newView(con *ui.Console, cfg config.Config) *view {
	v := &view{con: con, autoSummary: cfg.AutoSummary}
	if cfg.PaneRole == "" || !con.IsTTY() || !cfg.ShowBoard {
		return v
	}
	rows, cols := ui.TermSize()
	v.split = ui.NewSplit(os.Stdout, rows, cols, con.Color())
	return v
}

// Start は分割表示を開始し、端末サイズの変化を追従し始める。
func (v *view) Start() {
	if v.split == nil {
		return
	}
	v.split.Start()
	// 分割表示中は Console の直接出力を画面へ流さない
	// (画面全体がスクロールし、差分描画では位置を戻せないため)。
	v.con.Redirect(v.split)
	v.split.SetAutoSummary(v.autoSummary)
	v.split.SetBoard((&board.Board{}).LinesFor(v.autoSummary))
	v.watchResize()
	v.watchKeys()
}

// watchResize は SIGWINCH を受けて端末サイズを取り直す。
//
// 起動時の大きさのまま固定すると、pane を広げたときに
// 下の領域が画面外に残り、発言ログも元の幅で折り返されてしまう。
func (v *view) watchResize() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	v.stopResize = func() {
		signal.Stop(ch)
		close(ch)
	}
	go func() {
		for range ch {
			rows, cols := ui.TermSize()
			v.split.Resize(rows, cols)
		}
	}()
}

// watchKeys は発言ログ・会議ボードのスクロールをキーで操作できるようにする。
//
// 端末の入力モードを変更するので、Stop で必ず元へ戻す。
// 読み取れない端末ではキー操作だけ諦め、文字起こしは続ける。
func (v *view) watchKeys() {
	keys, err := ui.StartKeys(os.Stdin, v.onKey)
	if err != nil {
		return
	}
	v.keys = keys
}

// onKey はキー入力を画面操作へ割り当てる。
//
// 操作対象は Tab で切り替え、移動はその領域に効く。
func (v *view) onKey(k ui.Key) {
	if v.split == nil {
		return
	}
	switch k {
	case ui.KeyFocusNext:
		v.split.FocusNext()
	case ui.KeyUp:
		v.split.Scroll(-1)
	case ui.KeyDown:
		v.split.Scroll(1)
	case ui.KeyPageUp:
		v.split.ScrollPage(-1)
	case ui.KeyPageDown:
		v.split.ScrollPage(1)
	case ui.KeyHalfUp:
		v.split.ScrollHalf(-1)
	case ui.KeyHalfDown:
		v.split.ScrollHalf(1)
	case ui.KeyHome:
		v.split.ScrollHome()
	case ui.KeyEnd:
		v.split.ScrollEnd()
	}
}

// Stop は分割表示を解除する。
func (v *view) Stop() {
	// 入力モードの復元を最優先にする (以降の処理が失敗しても端末を戻す)。
	if v.keys != nil {
		v.keys.Stop()
		v.keys = nil
	}
	if v.stopResize != nil {
		v.stopResize()
		v.stopResize = nil
	}
	if v.split != nil {
		v.con.Redirect(nil)
		v.split.Stop()
	}
}

// startEarlyRecord は初期化前に会議記録を作る。
//
// モデル不在・デバイス不在・whisper 起動失敗・保存先作成失敗のように
// 文字起こしが始まる前に終わる場合でも、AI が原因を取得できるようにする。
func startEarlyRecord(cfg config.Config, now time.Time) (*session.Record, error) {
	if !cfg.EnableAI {
		return nil, nil
	}
	id := cfg.SessionID
	if id == "" {
		id = session.NewID(now)
	}
	rec := &session.Record{
		ID:          id,
		PID:         os.Getpid(),
		State:       session.StateStarting,
		PaneRole:    cfg.PaneRole,
		MuxSession:  cfg.MuxSession,
		BoardFormat: session.BoardFormatSummaryV2,
		StartedAt:   now,
	}
	if err := session.Save(rec); err != nil {
		return nil, fmt.Errorf("セッション記録を作成できません: %w", err)
	}
	return rec, nil
}

// startIPC はセッション記録を仕上げ、IPC サーバーを用意する。
//
// AI 連携が不要な場合 (--session-id も --pane-role も無い手動実行) は
// 何もせず nil を返し、従来どおりの動作にする。
func startIPC(cfg config.Config, lv *live, rec *session.Record, start time.Time,
	transcriptPath string, boards *board.Store) (*ipc.Server, *session.Record, error) {

	if !cfg.EnableAI || rec == nil {
		return nil, nil, nil
	}
	lv.id = rec.ID

	sock, err := session.SocketPathFor(rec.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("IPC ソケットのパスを決められません: %w", err)
	}
	srv, err := ipc.Listen(sock, lv)
	if err != nil {
		return nil, nil, err
	}

	// 保存先は全て絶対パスで記録する (pane の作業ディレクトリに左右されない)。
	rec.State = session.StateLoading
	rec.TranscriptPath = absPath(transcriptPath)
	rec.BoardPath = absPath(boards.MarkdownPath())
	rec.HistoryPath = absPath(boards.HistoryPath())
	rec.SocketPath = sock
	if err := session.Save(rec); err != nil {
		srv.Close()
		return nil, nil, err
	}
	return srv, rec, nil
}

// absPath は絶対パスに直す (失敗時は元の値)。
func absPath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// setAutoSummary は自動要約の実状態を表示へ反映する。
//
// 起動に失敗した場合に「自動更新」と約束しないため、
// ワーカーの起動結果が出てから呼ぶ。
func (v *view) setAutoSummary(on bool) {
	v.autoSummary = on
	if v.split != nil {
		v.split.SetAutoSummary(on)
		v.split.SetBoard((&board.Board{}).LinesFor(on))
	}
}

// startSummarizer は自動要約ワーカーを用意する。
//
// 無効なら nil, nil。プロバイダーを用意できない場合はエラーを返すが、
// 録音は続ける (黙って無効化しない)。
func startSummarizer(cfg config.Config, lv *live) (*summarizer.Worker, error) {
	if !cfg.AutoSummary {
		lv.SetSummaryStatus(func() summarizer.Status {
			return summarizer.DisabledStatus("自動要約は無効です (--auto-summary で有効化)")
		})
		return nil, nil
	}
	command := cfg.SummaryCommand
	if len(command) == 0 {
		var err error
		command, err = summarizer.Preset(cfg.SummaryProvider, cfg.SummaryModel)
		if err != nil {
			lv.SetSummaryStatus(func() summarizer.Status {
				return summarizer.DisabledStatus(err.Error())
			})
			return nil, err
		}
	}
	if _, err := exec.LookPath(command[0]); err != nil {
		msg := fmt.Sprintf("要約に使う %s が見つかりません", command[0])
		lv.SetSummaryStatus(func() summarizer.Status {
			return summarizer.DisabledStatus(msg)
		})
		return nil, fmt.Errorf("%s\n対処: インストールするか --summary-provider で変更してください", msg)
	}

	scfg := summarizer.DefaultConfig()
	if cfg.SummaryMinLines > 0 {
		scfg.MinNewLines = cfg.SummaryMinLines
	}
	if cfg.SummaryMinWait > 0 {
		scfg.MinInterval = cfg.SummaryMinWait
	}
	if cfg.SummaryMaxWait > 0 {
		scfg.MaxInterval = cfg.SummaryMaxWait
	}
	if cfg.SummaryMaxChars > 0 {
		scfg.MaxChars = cfg.SummaryMaxChars
	}
	p := &summarizer.ExecProvider{
		Command: command,
		Label:   cfg.SummaryProvider,
		Timeout: scfg.FinalTimeout,
	}
	w := summarizer.New(lv, p, scfg)
	lv.SetSummaryStatus(w.Status)
	return w, nil
}

// updateRecord は記録の状態を更新する。
func updateRecord(rec *session.Record, st session.State, lv *live) {
	if rec == nil {
		return
	}
	rec.State = st
	if lv != nil {
		cur := lv.boards.Current()
		rec.SummaryVersion = cur.Version
		rec.SummaryAt = cur.UpdatedAt
	}
	_ = session.Save(rec)
}

// markFailed は起動失敗を記録する (AI がエラーを受け取れるように)。
func markFailed(rec *session.Record, err error) {
	if rec == nil || err == nil {
		return
	}
	rec.State = session.StateFailed
	rec.Error = err.Error()
	_ = session.Save(rec)
}

// finishRecord は終了状態を記録する。
//
// 失敗として記録済みの場合は上書きしない (原因を残す)。
func finishRecord(rec *session.Record, lv *live) {
	if rec == nil {
		return
	}
	if rec.State == session.StateFailed {
		return
	}
	rec.State = session.StateStopped
	if lv != nil {
		cur := lv.boards.Current()
		rec.SummaryVersion = cur.Version
		rec.SummaryAt = cur.UpdatedAt
	}
	_ = session.Save(rec)
}
