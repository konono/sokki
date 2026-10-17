package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

// Split は同じ pane を「上: 発言ログ」「下: 会議ボード」に分けて描画する。
//
// 高さは端末の行数に対しておよそ 6:4 (ログ : 会議ボード)。
// どちらの領域も自前のビューポートとして持ち、Tab で選んだ側を
// j/k などでスクロールできる。既定では双方が最新に追従し、
// 上へスクロールした領域だけ追従を外して未読件数を見出しに出す。
//
// 端末のスクロール領域 (DECSTBM) には依存せず、毎回フレームを組み立てて
// 前回と違う行だけを書き換える。これにより、領域をまたいだ書き込みや
// 古い描画の残骸が構造的に起きない。
//
// 幅が足りない行は切り詰めずに折り返す (表示幅で計算するため
// 日本語でも崩れない)。端末の大きさが変わったら Resize で追従する。
//
// TTY 以外では使わない (Console をそのまま使う)。
type Split struct {
	mu sync.Mutex

	out    io.Writer
	color  bool
	rows   int
	cols   int
	active bool

	// 発言ログ (会議の全確定行)。
	//
	// 折り返した行を全件持つとメモリが 2 倍になるため、
	// 行ごとの「折り返し後の行数」だけを持ち、実際の折り返しは
	// 画面に出す範囲だけ都度行う。長時間の会議でも、増えるのは
	// 本文 + 1 行あたり数バイトに収まる。
	logRaw []string
	// logEnds は各行までの折り返し後の累計行数 (二分探索用)。
	logEnds []int

	// interimRaw は確定前の暫定行 (折り返し前)。
	interimRaw string

	// 会議ボード。
	boardRaw     []string
	boardWrap    wrapped
	boardTopic   string
	boardVersion int
	autoSummary  bool

	logView   viewport
	boardView viewport
	focus     focusTarget

	// frame は前回描いた画面 (差分描画に使う)。
	frame []frameRow
}

const (
	// logShare / boardShare は上下の高さの比 (およそ 6:4)。
	logShare   = 6
	boardShare = 4
	// minRegionRows は 1 領域の最小行数 (見出し + 本文 1 行)。
	minRegionRows = 2
)

// focusTarget は操作対象の領域。
type focusTarget int

const (
	focusLog focusTarget = iota
	focusBoard
)

// viewport は 1 領域の表示位置。
type viewport struct {
	// top は本文の先頭に出す表示行 (0 始まり)。
	top int
	// follow は最新へ追従している状態か。
	follow bool
}

// frameRow は画面 1 行分。
type frameRow struct {
	text string
	dim  bool
}

// wrapped は折り返した表示行と、その元になった行の対応。
//
// owner があると、折り返しが変わっても同じ内容の行へ
// 表示位置を戻せる (リサイズで読んでいた場所が飛ばない)。
type wrapped struct {
	rows  []string
	owner []int
}

// NewSplit は分割表示を作る。rows / cols は端末の大きさ。
func NewSplit(out io.Writer, rows, cols int, color bool) *Split {
	s := &Split{out: out, color: color}
	s.setSize(rows, cols)
	s.logView = viewport{follow: true}
	s.boardView = viewport{follow: true}
	return s
}

func (s *Split) setSize(rows, cols int) {
	if rows < 1 {
		rows = 1
	}
	if cols < 1 {
		cols = 1
	}
	s.rows, s.cols = rows, cols
}

// layout は上下の領域の行数を返す (見出しを含む)。
func (s *Split) layout() (logH, boardH int) {
	if s.rows < minRegionRows*2 {
		// 極端に低い端末では双方が見える範囲まで縮める。
		logH = s.rows / 2
		if logH < 1 {
			logH = 1
		}
		boardH = s.rows - logH
		if boardH < 1 && s.rows > 1 {
			boardH = 1
			logH = s.rows - 1
		}
		return logH, boardH
	}
	logH = s.rows * logShare / (logShare + boardShare)
	if logH < minRegionRows {
		logH = minRegionRows
	}
	boardH = s.rows - logH
	if boardH < minRegionRows {
		boardH = minRegionRows
		logH = s.rows - boardH
	}
	return logH, boardH
}

// Start は分割表示を開始する。
func (s *Split) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return
	}
	s.active = true
	// 以前のスクロール領域が残っていても影響を受けないよう戻す。
	fmt.Fprint(s.out, "\x1b[r")
	s.frame = nil // 全行描き直す
	s.render()
}

// Stop は分割表示を解除し、端末を元に戻す。
func (s *Split) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return
	}
	s.active = false
	fmt.Fprint(s.out, "\x1b[r")
	fmt.Fprint(s.out, "\x1b[?25h") // カーソルを戻す
	fmt.Fprintf(s.out, "\x1b[%d;1H", s.rows)
	fmt.Fprint(s.out, "\n")
}

// Resize は端末の大きさの変化を反映する。
func (s *Split) Resize(rows, cols int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rows <= 0 || cols <= 0 {
		return
	}
	if rows == s.rows && cols == s.cols {
		return
	}
	// 折り返しが変わるので、読んでいた位置を元の行で覚えておく。
	logAnchor := s.logLineAt(s.logView.top)
	boardAnchor := s.boardWrap.ownerAt(s.boardView.top)

	widthChanged := cols != s.cols
	_, oldBoardH := s.layout()
	s.setSize(rows, cols)
	_, newBoardH := s.layout()
	if widthChanged {
		s.rebuildLogEnds()
		s.logView.top = s.logRowOf(logAnchor)
	}
	if widthChanged || (oldBoardH >= 3) != (newBoardH >= 3) {
		s.rebuildBoardWrap()
		s.boardView.top = s.boardWrap.firstRowOf(boardAnchor)
	}
	s.frame = nil // 行が動くので全行描き直す
	s.render()
}

// Log は確定行を発言ログへ足す。
func (s *Split) Log(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logRaw = append(s.logRaw, line)
	s.logEnds = append(s.logEnds, s.logTotalRows()+len(WrapToWidth(line, s.cols)))
	// 確定行が出た時点で、その発話の暫定行は役目を終えている。
	// 消さないと古い暫定と確定行が並んで見える。
	s.interimRaw = ""
	s.render()
}

// Interim は確定前の暫定行を発言ログの末尾に出す。
func (s *Split) Interim(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interimRaw = line
	s.render()
}

// ClearInterim は暫定行を消す。
func (s *Split) ClearInterim() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interimRaw = ""
	s.render()
}

// SetAutoSummary は自動要約の有効状態を伝える (見出しの文言に反映)。
func (s *Split) SetAutoSummary(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoSummary = on
	s.render()
}

// SetBoardVersion は会議ボードの版を伝える (見出しに出す)。
//
// ここでは描き直さない。直後に SetBoard を呼ぶ前提で、
// 見出しと本文を 1 回の描画でまとめて更新する。
func (s *Split) SetBoardVersion(v int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.boardVersion = v
}

// SetBoard は会議ボードの内容を差し替える。
//
// 読んでいる位置は可能な限り保ち、範囲外になれば近い位置へ寄せる。
func (s *Split) SetBoard(lines []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	anchor := s.boardWrap.ownerAt(s.boardView.top)
	s.boardRaw = lines
	s.boardTopic = ""
	if len(lines) > 0 {
		if topic, ok := strings.CutPrefix(strings.TrimSpace(lines[0]), "今の話題:"); ok {
			s.boardTopic = strings.TrimSpace(topic)
		}
	}
	s.rebuildBoardWrap()
	if !s.boardView.follow {
		s.boardView.top = s.boardWrap.firstRowOf(anchor)
	}
	s.render()
}

// rebuildBoardWrap は固定表示できる話題を本文から除き、重複表示を防ぐ。
// 小さな端末では固定行を置けないため、話題を本文に残す。
func (s *Split) rebuildBoardWrap() {
	lines := s.boardRaw
	_, boardH := s.layout()
	if s.boardTopic != "" && boardH >= 3 && len(lines) > 0 {
		lines = lines[1:]
	}
	s.boardWrap = wrapLines(lines, s.cols)
}

// --- 操作 ---

// FocusNext は操作対象の領域を切り替える。
func (s *Split) FocusNext() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.focus == focusLog {
		s.focus = focusBoard
	} else {
		s.focus = focusLog
	}
	s.render()
}

// FocusIsBoard は操作対象が会議ボードかを返す。
func (s *Split) FocusIsBoard() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.focus == focusBoard
}

// Scroll は対象の領域を行単位で動かす (正で下、負で上)。
func (s *Split) Scroll(lines int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lines == 0 {
		return
	}
	v := s.focused()
	v.top += lines
	// 上へ動かしたら最新への追従を外す (新着で位置が飛ばない)。
	if lines < 0 {
		v.follow = false
	}
	s.render()
}

// ScrollPage は対象の領域をページ単位で動かす。
func (s *Split) ScrollPage(pages int) {
	s.scrollBy(pages, 1)
}

// ScrollHalf は対象の領域を半ページ動かす (Ctrl+d / Ctrl+u)。
func (s *Split) ScrollHalf(pages int) {
	s.scrollBy(pages, 2)
}

func (s *Split) scrollBy(pages, div int) {
	if pages == 0 {
		return
	}
	s.mu.Lock()
	step := s.focusedBodyHeight() / div
	s.mu.Unlock()
	if step < 1 {
		step = 1
	}
	s.Scroll(pages * step)
}

// ScrollHome は対象の領域を先頭へ移動する。
func (s *Split) ScrollHome() {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.focused()
	v.top = 0
	v.follow = false
	s.render()
}

// ScrollEnd は対象の領域を末尾へ移動し、最新への追従を再開する。
func (s *Split) ScrollEnd() {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.focused()
	v.follow = true
	s.render()
}

// focused は操作対象のビューポートを返す (ロック済みで呼ぶ)。
func (s *Split) focused() *viewport {
	if s.focus == focusBoard {
		return &s.boardView
	}
	return &s.logView
}

// focusedBodyHeight は操作対象の本文の行数 (ロック済みで呼ぶ)。
func (s *Split) focusedBodyHeight() int {
	logH, boardH := s.layout()
	h := logH - 1
	if s.focus == focusBoard {
		h = s.boardBodyHeight(boardH)
	}
	if h < 1 {
		h = 1
	}
	return h
}

// boardBodyHeight は見出しと、表示可能なら固定の「今の話題」を除いた高さ。
func (s *Split) boardBodyHeight(boardH int) int {
	h := boardH - 1
	if s.boardTopic != "" && h >= 2 {
		h--
	}
	return h
}

// --- 描画 ---

// render はフレームを組み立てて差分だけ書き出す (ロック済みで呼ぶ)。
func (s *Split) render() {
	frame := s.buildFrame()
	if !s.active {
		return
	}
	s.flush(frame)
}

// logTotalRows は発言ログの折り返し後の総行数。
func (s *Split) logTotalRows() int {
	if len(s.logEnds) == 0 {
		return 0
	}
	return s.logEnds[len(s.logEnds)-1]
}

// rebuildLogEnds は幅が変わったときに累計行数を作り直す。
func (s *Split) rebuildLogEnds() {
	s.logEnds = s.logEnds[:0]
	total := 0
	for _, line := range s.logRaw {
		total += len(WrapToWidth(line, s.cols))
		s.logEnds = append(s.logEnds, total)
	}
}

// logLineAt は表示行に対応する確定行の番号を返す (無ければ -1)。
func (s *Split) logLineAt(row int) int {
	if row < 0 || row >= s.logTotalRows() {
		return -1
	}
	// logEnds は単調増加なので二分探索で引ける。
	lo, hi := 0, len(s.logEnds)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if s.logEnds[mid] <= row {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// logRowOf は確定行の先頭が何行目に来るかを返す。
func (s *Split) logRowOf(line int) int {
	if line <= 0 {
		return 0
	}
	if line > len(s.logEnds) {
		return s.logTotalRows()
	}
	return s.logEnds[line-1]
}

// logRowsFrom は from 行目から n 行分の表示行を折り返して返す。
//
// 画面に出す範囲だけを都度折り返すので、会議が長くなっても
// 折り返し済みの行を全件抱えずに済む。
func (s *Split) logRowsFrom(from, n int) []string {
	out := make([]string, 0, n)
	if n <= 0 {
		return out
	}
	total := s.logTotalRows()
	if from < 0 {
		from = 0
	}
	for row := from; row < total && len(out) < n; {
		line := s.logLineAt(row)
		if line < 0 {
			break
		}
		wrapped := WrapToWidth(s.logRaw[line], s.cols)
		off := row - s.logRowOf(line)
		for i := off; i < len(wrapped) && len(out) < n; i++ {
			out = append(out, wrapped[i])
		}
		row = s.logRowOf(line) + len(wrapped)
	}
	return out
}

// interimRows は暫定行の表示行を返す。
func (s *Split) interimRows() []string {
	if s.interimRaw == "" {
		return nil
	}
	return WrapToWidth("… "+s.interimRaw, s.cols)
}

// buildFrame は画面 1 枚分を組み立てる (ロック済みで呼ぶ)。
func (s *Split) buildFrame() []frameRow {
	logH, boardH := s.layout()
	logBody := logH - 1
	if logBody < 0 {
		logBody = 0
	}
	boardBody := s.boardBodyHeight(boardH)
	if boardBody < 0 {
		boardBody = 0
	}

	interim := s.interimRows()
	logTotal := s.logTotalRows() + len(interim)
	clampView(&s.logView, logTotal, logBody)
	clampView(&s.boardView, len(s.boardWrap.rows), boardBody)

	frame := make([]frameRow, 0, s.rows)
	if logH > 0 {
		frame = append(frame, frameRow{
			text: s.header(focusLog, logTotal, logBody), dim: true})
		frame = append(frame, s.logBodyRows(logBody, interim)...)
	}
	if boardH > 0 {
		frame = append(frame, frameRow{
			text: s.header(focusBoard, len(s.boardWrap.rows), boardBody), dim: true})
		if s.boardTopic != "" && boardH >= 3 {
			frame = append(frame, frameRow{
				text: truncateToWidth(" 今の話題: "+s.boardTopic, s.cols), dim: true})
		}
		frame = append(frame, s.bodyRows(s.boardWrap.rows, s.boardView, boardBody)...)
	}
	for len(frame) < s.rows {
		frame = append(frame, frameRow{})
	}
	return frame[:s.rows]
}

// logBodyRows は発言ログの本文を組み立てる。
//
// 確定行は可視範囲だけ折り返し、末尾に暫定行を足す。
func (s *Split) logBodyRows(height int, interim []string) []frameRow {
	confirmed := s.logTotalRows()
	rows := s.logRowsFrom(s.logView.top, height)
	// 画面の残りに暫定行を足す。
	if rest := height - len(rows); rest > 0 && len(interim) > 0 {
		from := s.logView.top + len(rows) - confirmed
		if from < 0 {
			from = 0
		}
		for i := from; i < len(interim) && len(rows) < height; i++ {
			rows = append(rows, interim[i])
		}
	}
	out := make([]frameRow, 0, height)
	for i := 0; i < height; i++ {
		if i >= len(rows) {
			out = append(out, frameRow{})
			continue
		}
		// 暫定行は淡く出す。
		isInterim := s.logView.top+i >= confirmed
		out = append(out, frameRow{text: rows[i], dim: isInterim})
	}
	return out
}

// bodyRows は本文の表示行を返す (足りない分は空行)。
//
// dimFrom 以降の行は暫定行として淡く出す (-1 で無効)。
func (s *Split) bodyRows(rows []string, v viewport, height int) []frameRow {
	out := make([]frameRow, 0, height)
	for i := 0; i < height; i++ {
		idx := v.top + i
		if idx >= len(rows) {
			out = append(out, frameRow{})
			continue
		}
		out = append(out, frameRow{text: rows[idx]})
	}
	return out
}

// clampView は表示位置を範囲内に収め、追従状態を更新する。
func clampView(v *viewport, total, height int) {
	max := total - height
	if max < 0 {
		max = 0
	}
	if v.follow {
		v.top = max
		return
	}
	if v.top > max {
		v.top = max
	}
	if v.top < 0 {
		v.top = 0
	}
	// 末尾まで下がったら追従を再開する。
	if v.top >= max {
		v.follow = true
	}
}

// header は領域の見出しを作る。
func (s *Split) header(target focusTarget, total, height int) string {
	var title string
	v := s.logView
	if target == focusBoard {
		v = s.boardView
		title = "会議ボード"
		if s.autoSummary {
			title = "会議ボード（自動更新）"
		}
		if s.boardVersion > 0 {
			title = fmt.Sprintf("%s 版 %d", title, s.boardVersion)
		}
	} else {
		title = "発言ログ"
	}

	focused := s.focus == target
	mark := "  "
	if focused {
		mark = "▶ "
	}
	left := " " + mark + title + " " + positionText(v, total, height)

	right := ""
	if focused {
		right = "Tab 切替 j/k 行 PgUp/PgDn 頁 g/G 先頭末尾 "
	}
	gap := s.cols - DisplayWidth(left) - DisplayWidth(right)
	if gap < 1 {
		if right == "" {
			return truncateToWidth(left, s.cols)
		}
		return truncateToWidth(left+" "+right, s.cols)
	}
	return left + strings.Repeat("─", gap) + right
}

// positionText は表示位置と追従状態を返す。
func positionText(v viewport, total, height int) string {
	if total == 0 {
		return "0 行"
	}
	first := v.top + 1
	last := v.top + height
	if last > total {
		last = total
	}
	pos := fmt.Sprintf("%d–%d / %d 行", first, last, total)
	if v.follow {
		return pos + " 追従中"
	}
	if unread := total - (v.top + height); unread > 0 {
		return pos + fmt.Sprintf(" 未読 %d 行", unread)
	}
	return pos
}

// flush は前回と違う行だけ書き換える。
func (s *Split) flush(frame []frameRow) {
	fmt.Fprint(s.out, "\x1b[?25l") // 描画中はカーソルを隠す
	for i, row := range frame {
		if i < len(s.frame) && s.frame[i] == row {
			continue
		}
		fmt.Fprintf(s.out, "\x1b[%d;1H\x1b[2K", i+1)
		if row.text == "" {
			continue
		}
		if row.dim && s.color {
			fmt.Fprint(s.out, dim+row.text+reset)
		} else {
			fmt.Fprint(s.out, row.text)
		}
	}
	// カーソルは下端へ寄せる (入力は読み取るので見た目だけ揃える)。
	fmt.Fprintf(s.out, "\x1b[%d;1H", s.rows)
	fmt.Fprint(s.out, "\x1b[?25h")
	s.frame = frame
}

// Snapshot は現在画面に出ている行を返す。
//
// 画面の内容をそのまま確認したい場合 (テストや不具合調査) に使う。
func (s *Split) Snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	frame := s.frame
	if frame == nil {
		frame = s.buildFrame()
	}
	out := make([]string, 0, len(frame))
	for _, row := range frame {
		out = append(out, row.text)
	}
	return out
}

// --- 折り返し ---

// wrapLines は各行を幅で折り返し、元の行との対応を残す。
func wrapLines(src []string, cols int) wrapped {
	var w wrapped
	for i, line := range src {
		for _, row := range WrapToWidth(line, cols) {
			w.rows = append(w.rows, row)
			w.owner = append(w.owner, i)
		}
	}
	return w
}

// ownerAt は表示行に対応する元の行番号を返す。
func (w wrapped) ownerAt(row int) int {
	if row < 0 || row >= len(w.owner) {
		return -1
	}
	return w.owner[row]
}

// firstRowOf は元の行番号に対応する最初の表示行を返す。
//
// 対応する行が無ければ、その手前で最も近い表示行を返す。
func (w wrapped) firstRowOf(owner int) int {
	if owner < 0 {
		return 0
	}
	for i, o := range w.owner {
		if o >= owner {
			return i
		}
	}
	return len(w.rows)
}

// WrapToWidth は表示幅で折り返した行を返す。
//
// 日本語などの全角文字を 2 桁として数えるため、幅のはみ出しが起きない。
// 空文字列は 1 行 (空行) として返す。
func WrapToWidth(s string, cells int) []string {
	if cells < 1 {
		cells = 1
	}
	if s == "" {
		return []string{""}
	}
	var out []string
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runeCells(r)
		if w+rw > cells && b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
			w = 0
		}
		b.WriteRune(r)
		w += rw
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// truncateToWidth は表示幅で切り詰める。
func truncateToWidth(s string, cells int) string {
	if DisplayWidth(s) <= cells {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runeCells(r)
		if w+rw > cells {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String()
}
