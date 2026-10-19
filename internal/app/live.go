package app

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/konono/sokki/internal/board"
	"github.com/konono/sokki/internal/ipc"
	"github.com/konono/sokki/internal/session"
	"github.com/konono/sokki/internal/summarizer"
	"github.com/konono/sokki/internal/transcript"
	"github.com/konono/sokki/internal/translation"
	"github.com/konono/sokki/internal/ui"
)

// live は録音中の会議の状態を保持し、IPC 要求に答える。
//
// 確定行・現在の暫定行・会議ボードをまとめて持ち、
// 画面への反映もここから行う (AI や MCP は pane に直接書かない)。
type live struct {
	mu sync.RWMutex

	id             string
	paneRole       string
	startedAt      time.Time
	state          session.State
	transcriptPath string

	lines     []ipc.Line
	interim   string
	interimAt string

	boards *board.Store
	view   *view

	stopOnce func()
	// summaryStatus は自動要約の状態を返す (未使用なら nil)。
	summaryStatus     func() summarizer.Status
	translationStatus func() translation.Status
	// done は録音プロセスがドレインを終え、保存ファイルを閉じたら閉じる。
	done chan struct{}
	// finished は終了処理の結果 (保存に失敗した場合の理由)。
	finishErr string
}

// newLive は録音セッションの状態を作る。
func newLive(id, paneRole, transcriptPath string, startedAt time.Time,
	boards *board.Store, v *view) *live {
	return &live{
		id:             id,
		paneRole:       paneRole,
		transcriptPath: transcriptPath,
		startedAt:      startedAt,
		state:          session.StateLoading,
		boards:         boards,
		view:           v,
		done:           make(chan struct{}),
	}
}

// markFinished は保存完了を知らせる (Run の終了処理から呼ぶ)。
func (l *live) markFinished(err error) {
	l.mu.Lock()
	if err != nil {
		l.finishErr = err.Error()
	}
	l.state = session.StateStopped
	l.mu.Unlock()
	select {
	case <-l.done:
	default:
		close(l.done)
	}
}

// setState は状態を更新する。
func (l *live) setState(s session.State) {
	l.mu.Lock()
	l.state = s
	l.mu.Unlock()
}

// addLine は確定行を記録する。
func (l *live) addLine(at, text string) {
	l.mu.Lock()
	l.lines = append(l.lines, ipc.Line{Index: len(l.lines), At: at, Text: text})
	l.interim = ""
	l.interimAt = ""
	l.mu.Unlock()
}

// setInterim は現在の暫定行を記録する。
func (l *live) setInterim(at, text string) {
	l.mu.Lock()
	l.interim = text
	l.interimAt = at
	l.mu.Unlock()
}

// clearInterim は暫定行を消す。
func (l *live) clearInterim() {
	l.mu.Lock()
	l.interim = ""
	l.interimAt = ""
	l.mu.Unlock()
}

// Handle は IPC 要求を処理する。
func (l *live) Handle(req ipc.Request) ipc.Response {
	switch req.Op {
	case ipc.OpStatus:
		return l.status()
	case ipc.OpTranscript:
		return l.read(req.From, req.Limit, false)
	case ipc.OpContext:
		return l.read(req.From, req.Limit, true)
	case ipc.OpPublish:
		return l.publish(req.Board)
	case ipc.OpStop:
		return l.stop()
	default:
		return ipc.Response{OK: false, Error: "未対応の操作: " + string(req.Op)}
	}
}

func (l *live) base() ipc.Response {
	return ipc.Response{
		OK:             true,
		ID:             l.id,
		State:          string(l.state),
		StartedAt:      l.startedAt.Format(time.RFC3339),
		PaneRole:       l.paneRole,
		TranscriptPath: l.transcriptPath,
		BoardPath:      l.boards.MarkdownPath(),
		HistoryPath:    l.boards.HistoryPath(),
	}
}

func (l *live) status() ipc.Response {
	l.mu.RLock()
	r := l.base()
	r.NextFrom = len(l.lines)
	r.Interim = l.interim
	r.InterimAt = l.interimAt
	statusFn := l.summaryStatus
	translationFn := l.translationStatus
	l.mu.RUnlock()

	cur := l.boards.Current()
	r.SummaryVersion = cur.Version
	if !cur.UpdatedAt.IsZero() {
		r.SummaryAt = cur.UpdatedAt.Format(time.RFC3339)
	}
	// 自動要約の状態は live.mu を解放してから取得する。
	// 保持したまま呼ぶと、ワーカーが live を読み返すときに
	// ロック順が循環して詰まる (addLine の書き込み待ちで顕在化する)。
	if statusFn != nil {
		r.Summary = summaryToMap(statusFn())
	}
	if translationFn != nil {
		s := translationFn()
		r.Translation = map[string]any{"readiness": s.Readiness, "readiness_error": s.ReadinessError, "units": s.Units, "pending": s.Pending, "failed": s.Failed, "untranslated": s.Untranslated, "unsaved": s.Unsaved, "storage_error": s.StorageError}
	}
	return r
}

func (l *live) read(from, limit int, withContext bool) ipc.Response {
	l.mu.RLock()
	defer l.mu.RUnlock()

	r := l.base()
	if from < 0 {
		from = 0
	}
	if from > len(l.lines) {
		from = len(l.lines)
	}
	lines := l.lines[from:]
	if limit > 0 && len(lines) > limit {
		lines = lines[:limit]
	}
	// コピーを返す (呼び出し中に追記されても壊れない)。
	r.Lines = append([]ipc.Line(nil), lines...)
	r.NextFrom = from + len(r.Lines)

	if withContext {
		r.Interim = l.interim
		r.InterimAt = l.interimAt
		cur := l.boards.Current()
		if !cur.IsEmpty() || cur.Version > 0 {
			b := cur
			r.Board = &b
		}
		r.SummaryVersion = cur.Version
		if !cur.UpdatedAt.IsZero() {
			r.SummaryAt = cur.UpdatedAt.Format(time.RFC3339)
		}
	}
	return r
}

// publish は会議ボードを保存し、画面へ反映する。
//
// 保存に失敗した場合は画面も更新せず、失敗を返す
// (公開できたのにファイルに残らない状態を作らない)。
func (l *live) publish(b *board.Board) ipc.Response {
	if b == nil {
		return ipc.Response{OK: false, Error: "会議ボードが空です"}
	}
	saved, err := l.boards.Publish(*b, time.Now())
	if err != nil {
		return ipc.Response{OK: false, Error: err.Error()}
	}
	if l.view != nil {
		l.view.setBoardOf(saved)
	}

	l.mu.RLock()
	defer l.mu.RUnlock()
	r := l.base()
	r.NextFrom = len(l.lines)
	r.SummaryVersion = saved.Version
	r.SummaryAt = saved.UpdatedAt.Format(time.RFC3339)
	bb := saved
	r.Board = &bb
	return r
}

// stopDrainTimeout は保存完了を待つ上限。
// 録音側の drainTimeout (30 秒) + 終了処理の余裕を見る。
const stopDrainTimeout = 50 * time.Second

// stop は通常終了を依頼し、**未処理の確定行が保存されるまで待つ**。
//
// キャンセルを発火しただけで応答すると、直後に読み取った AI が
// 最後の発言を取り逃す。保存ファイルを閉じたことを確認してから返す。
func (l *live) stop() ipc.Response {
	l.mu.Lock()
	stop := l.stopOnce
	l.mu.Unlock()
	if stop != nil {
		stop()
	}

	select {
	case <-l.done:
		// ドレイン完了。
	case <-time.After(stopDrainTimeout):
		l.mu.RLock()
		r := l.base()
		r.NextFrom = len(l.lines)
		l.mu.RUnlock()
		r.OK = false
		r.Error = fmt.Sprintf(
			"保存の完了を確認できませんでした (%v 待機)。"+
				"録音プロセスの状態を get_status で確認してください", stopDrainTimeout)
		return r
	}

	l.mu.RLock()
	defer l.mu.RUnlock()
	r := l.base()
	r.NextFrom = len(l.lines)
	if l.finishErr != "" {
		r.OK = false
		r.Error = "終了時の保存に失敗しました: " + l.finishErr
	}
	return r
}

// SummarySnapshot は自動要約の材料を返す (summarizer.Source)。
func (l *live) SummarySnapshot() summarizer.Snapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	cur := l.boards.Current()
	from := cur.AppliedThrough
	if from < 0 {
		from = 0
	}
	if from > len(l.lines) {
		from = len(l.lines)
	}
	var lines []summarizer.Line
	for _, ln := range l.lines[from:] {
		lines = append(lines, summarizer.Line{Index: ln.Index, At: ln.At, Text: ln.Text})
	}
	return summarizer.Snapshot{
		Lines:          lines,
		Total:          len(l.lines),
		Topic:          cur.Topic,
		Summary:        cur.Summary,
		AppliedThrough: from,
		Version:        cur.Version,
	}
}

// PublishSummary は自動要約の結果を反映する (summarizer.Source)。
//
// 基準の版が進んでいれば反映せず、呼び出し側に読み直させる。
func (l *live) PublishSummary(topic, summary string, appliedThrough, base int) error {
	b := board.Board{Topic: topic, Summary: summary, AppliedThrough: appliedThrough}
	saved, err := l.boards.PublishIfVersion(b, time.Now(), base)
	if err != nil {
		if errors.Is(err, board.ErrVersionConflict) {
			return summarizer.ErrVersionConflict
		}
		return err
	}
	if l.view != nil {
		l.view.setBoardOf(saved)
	}
	return nil
}

// SetSummaryStatus は自動要約の状態を保持する (get_status 用)。
func (l *live) SetSummaryStatus(fn func() summarizer.Status) {
	l.mu.Lock()
	l.summaryStatus = fn
	l.mu.Unlock()
}

// summaryToMap は自動要約の状態を応答用に変換する。
func summaryToMap(s summarizer.Status) map[string]any {
	out := map[string]any{
		"enabled":              s.Enabled,
		"state":                string(s.State),
		"version":              s.Version,
		"pending_lines":        s.PendingLines,
		"consecutive_failures": s.Failures,
	}
	if s.Provider != "" {
		out["provider"] = s.Provider
	}
	if s.Readiness != "" {
		out["readiness"] = s.Readiness
	}
	if s.ReadinessError != "" {
		out["readiness_error"] = s.ReadinessError
	}
	if !s.UpdatedAt.IsZero() {
		out["updated_at"] = s.UpdatedAt.Format(time.RFC3339)
	}
	if s.LastError != "" {
		out["last_error"] = s.LastError
	}
	if s.FinalState != "" {
		out["final_state"] = string(s.FinalState)
	}
	return out
}

// view は画面出力の切り替え (通常の 1 行出力 / 上下分割)。
type view struct {
	con   *ui.Console
	split *ui.Split
	// stopResize は端末サイズの監視を止める。
	stopResize func()
	// keys はキー入力の読み取り (端末の入力モードを変更する)。
	keys *ui.Keys
	// autoSummary は自動要約が有効か (表示の文言に使う)。
	autoSummary      bool
	translationShown map[int]uint64
	display          *asyncTranslationView
	plain            *ui.Console
}

// Translation renders one stable TTY slot; non-TTY output is explicitly revisioned.
func (v *view) Translation(u translation.Unit) {
	if v.display != nil {
		v.display.enqueue(func() { v.translationNow(u) })
		return
	}
	v.translationNow(u)
}

func (v *view) translationNow(u translation.Unit) {
	if v.translationShown == nil {
		v.translationShown = make(map[int]uint64)
	}
	if v.translationShown[u.ID] >= u.Seq {
		return
	}
	v.translationShown[u.ID] = u.Seq
	var b strings.Builder
	if v.split != nil {
		fmt.Fprintf(&b, "[翻訳 unit=%d v%d source=%v %s]", u.ID, u.Version, u.SourceIndices, u.State)
	} else {
		fmt.Fprintf(&b, "[translation unit=%d version=%d sources=%v state=%s]", u.ID, u.Version, u.SourceIndices, u.State)
	}
	if u.Closed {
		fmt.Fprintf(&b, " closed=%s", u.CloseReason)
	}
	if u.Japanese != "" {
		fmt.Fprintf(&b, "\nJA: %s", safeTranslationDisplay(u.Japanese))
	} else if u.Error != "" {
		fmt.Fprintf(&b, "\nJA: （未訳: %s）", safeTranslationDisplay(u.Error))
	} else {
		b.WriteString("\nJA: （待機中）")
	}
	for _, source := range u.Source {
		fmt.Fprintf(&b, "\nEN: %s", safeTranslationDisplay(source))
	}
	if v.split != nil {
		v.split.UpsertUnit(u.ID, b.String())
	} else {
		v.outputConsole().Final(b.String())
	}
}

func safeTranslationDisplay(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
}

func (v *view) Log(line string) {
	if v.display != nil {
		v.display.enqueue(func() { v.logNow(line) })
		return
	}
	v.logNow(line)
}
func (v *view) logNow(line string) {
	if v.split != nil {
		v.split.Log(line)
		return
	}
	v.outputConsole().Final(line)
}

func (v *view) Interim(line string) {
	if v.display != nil {
		v.display.enqueue(func() { v.interimNow(line) })
		return
	}
	v.interimNow(line)
}
func (v *view) interimNow(line string) {
	if v.split != nil {
		v.split.Interim(line)
		return
	}
	v.outputConsole().Interim(line)
}

func (v *view) ClearInterim() {
	if v.display != nil {
		v.display.enqueue(func() { v.clearinterimNow() })
		return
	}
	v.clearinterimNow()
}
func (v *view) clearinterimNow() {
	if v.split != nil {
		v.split.ClearInterim()
		return
	}
	v.outputConsole().ClearInterim()
}

// Info は補助メッセージを出す。
//
// 分割表示中は発言ログ領域へ流す (別経路で書くと、
// 会議ボードの描画と混ざって表示が崩れる)。
func (v *view) Info(format string, args ...any) {
	if v.display != nil {
		v.display.enqueue(func() { v.infoNow(format, args...) })
		return
	}
	v.infoNow(format, args...)
}
func (v *view) infoNow(format string, args ...any) {
	if v.split != nil {
		v.split.Log(fmt.Sprintf(format, args...))
		return
	}
	v.outputConsole().Info(format, args...)
}

// Warn は警告を出す。分割表示中は発言ログ領域へ流す。
func (v *view) Warn(format string, args ...any) {
	if v.display != nil {
		v.display.enqueue(func() { v.warnNow(format, args...) })
		return
	}
	v.warnNow(format, args...)
}
func (v *view) warnNow(format string, args ...any) {
	if v.split != nil {
		v.split.Log("警告: " + fmt.Sprintf(format, args...))
		return
	}
	v.outputConsole().Warn(format, args...)
}

func (v *view) SetBoard(lines []string) {
	if v.display != nil {
		copyLines := append([]string(nil), lines...)
		v.display.enqueue(func() { v.setboardNow(copyLines) })
		return
	}
	v.setboardNow(lines)
}
func (v *view) setboardNow(lines []string) {
	if v.split != nil {
		v.split.SetBoard(lines)
	}
}

// setBoardOf は会議ボードの版と内容をまとめて反映する。
func (v *view) setBoardOf(b board.Board) {
	if v.display != nil {
		v.display.enqueue(func() { v.setBoardOfNow(b) })
		return
	}
	v.setBoardOfNow(b)
}
func (v *view) setBoardOfNow(b board.Board) {
	if v.split == nil {
		return
	}
	v.split.SetBoardVersion(b.Version)
	v.split.SetBoard(b.LinesFor(v.autoSummary))
}

// splitLine は保存済みの行 "[HH:MM:SS] 本文" を分解する。
func splitLine(line string) (at, text string) {
	if !strings.HasPrefix(line, "[") {
		return "", line
	}
	end := strings.Index(line, "]")
	if end < 0 {
		return "", line
	}
	return line[1:end], strings.TrimSpace(line[end+1:])
}

// offsetString は時刻を HH:MM:SS にする。
func offsetString(d time.Duration) string { return transcript.FormatOffset(d) }

func (v *view) outputConsole() *ui.Console {
	if v.plain != nil {
		return v.plain
	}
	return v.con
}
