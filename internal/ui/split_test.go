package ui

import (
	"fmt"
	"strings"
	"testing"
)

// --- 補助 ---

// logHeaderRow / boardHeaderRow は各領域の見出し行を返す。
func logHeaderRow(term *fakeTerm) int {
	for i, l := range term.screen {
		if strings.Contains(l, "発言ログ") {
			return i
		}
	}
	return -1
}

func boardHeaderRow(term *fakeTerm) int {
	for i, l := range term.screen {
		if strings.Contains(l, "会議ボード") {
			return i
		}
	}
	return -1
}

// logArea / boardArea は各領域の本文をまとめて返す。
func logArea(t *testing.T, term *fakeTerm) string {
	t.Helper()
	lh, bh := logHeaderRow(term), boardHeaderRow(term)
	if lh < 0 || bh < 0 {
		t.Fatalf("見出しが無い:\n%s", term.All())
	}
	return strings.Join(term.screen[lh+1:bh], "\n")
}

func boardArea(t *testing.T, term *fakeTerm) string {
	t.Helper()
	bh := boardHeaderRow(term)
	if bh < 0 {
		t.Fatalf("会議ボードの見出しが無い:\n%s", term.All())
	}
	return strings.Join(term.screen[bh+1:], "\n")
}

// newSplitTerm は模擬端末付きの分割表示を用意する。
func newSplitTerm(t *testing.T, rows, cols int) (*fakeTerm, *Split) {
	t.Helper()
	term := newFakeTerm(rows, cols)
	s := NewSplit(term, rows, cols, false)
	s.Start()
	return term, s
}

func boardLines(n int) []string {
	out := []string{"今の話題: 長い会議", "要約:"}
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("・要点 %03d の内容", i))
	}
	return out
}

// --- 完了条件 2: 上下およそ 6:4、Tab と j/k が見た目と一致 ---

func TestLayoutIsSixToFour(t *testing.T) {
	for _, rows := range []int{20, 24, 30, 50} {
		term, s := newSplitTerm(t, rows, 60)
		lh, bh := logHeaderRow(term), boardHeaderRow(term)
		if lh != 0 {
			t.Fatalf("rows=%d: 発言ログが先頭にない (%d)", rows, lh)
		}
		logH := bh
		boardH := rows - bh
		want := rows * 6 / 10
		if logH != want {
			t.Errorf("rows=%d: ログ領域 = %d 行, want %d", rows, logH, want)
		}
		if logH+boardH != rows {
			t.Errorf("rows=%d: 合計が画面と違う (%d + %d)", rows, logH, boardH)
		}
		// 比が 6:4 に近いこと。
		ratio := float64(logH) / float64(rows)
		if ratio < 0.55 || ratio > 0.65 {
			t.Errorf("rows=%d: ログの比率 = %.2f", rows, ratio)
		}
		_ = s
	}
}

// 極端に低い端末でも双方が見えること。
func TestLayoutKeepsBothOnTinyTerminal(t *testing.T) {
	for _, rows := range []int{4, 5, 6, 8} {
		term, s := newSplitTerm(t, rows, 40)
		s.Log("[00:00:01] 発言")
		s.SetBoard([]string{"今の話題: 短い"})
		if logHeaderRow(term) < 0 || boardHeaderRow(term) < 0 {
			t.Errorf("rows=%d: どちらかの領域が消えた:\n%s", rows, term.All())
		}
	}
}

// Tab でフォーカスが移り、見出しで分かること。
func TestFocusToggleShowsInHeader(t *testing.T) {
	term, s := newSplitTerm(t, 24, 70)
	s.Log("[00:00:01] 発言")
	s.SetBoard(boardLines(5))

	lh, bh := logHeaderRow(term), boardHeaderRow(term)
	if !strings.Contains(term.screen[lh], "▶") {
		t.Errorf("初期フォーカスがログでない: %q", term.screen[lh])
	}
	if strings.Contains(term.screen[bh], "▶") {
		t.Errorf("会議ボードにフォーカス印がある: %q", term.screen[bh])
	}
	// 操作案内はフォーカス側に出ること。
	if !strings.Contains(term.screen[lh], "Tab") {
		t.Errorf("操作案内が無い: %q", term.screen[lh])
	}

	s.FocusNext()
	if !s.FocusIsBoard() {
		t.Fatal("Tab でフォーカスが移らない")
	}
	lh, bh = logHeaderRow(term), boardHeaderRow(term)
	if strings.Contains(term.screen[lh], "▶") {
		t.Errorf("ログにフォーカス印が残った: %q", term.screen[lh])
	}
	if !strings.Contains(term.screen[bh], "▶") {
		t.Errorf("会議ボードにフォーカス印が無い: %q", term.screen[bh])
	}

	s.FocusNext()
	if s.FocusIsBoard() {
		t.Error("Tab で戻らない")
	}
}

// j/k が対象領域だけを動かすこと。
func TestScrollMovesOnlyFocusedRegion(t *testing.T) {
	term, s := newSplitTerm(t, 24, 60)
	for i := 0; i < 60; i++ {
		s.Log(fmt.Sprintf("[00:00:%02d] 発言 %03d", i, i))
	}
	s.SetBoard(boardLines(60))

	beforeBoard := boardArea(t, term)
	s.Scroll(-5) // ログを上へ
	if logArea(t, term) == "" {
		t.Fatal("ログが空になった")
	}
	if boardArea(t, term) != beforeBoard {
		t.Errorf("ログ操作で会議ボードが動いた")
	}

	s.FocusNext()
	beforeLog := logArea(t, term)
	s.Scroll(-5) // 会議ボードを上へ
	if logArea(t, term) != beforeLog {
		t.Errorf("会議ボード操作でログが動いた")
	}
}

// --- 完了条件 1: 長いログと長い要約を端から端まで読める ---

func TestScrollReadsAllLogLines(t *testing.T) {
	term, s := newSplitTerm(t, 24, 60)
	const total = 600 // 画面用履歴の上限より多い想定の長さ
	for i := 0; i < total; i++ {
		s.Log(fmt.Sprintf("[00:00:%02d] 発言 %03d", i%60, i))
	}

	// 既定は最新追従。
	if !strings.Contains(logArea(t, term), fmt.Sprintf("発言 %03d", total-1)) {
		t.Fatalf("最新の発言が見えない:\n%s", logArea(t, term))
	}

	// 先頭へ飛んで、ページ送りで最後まで読めること。
	s.ScrollHome()
	if !strings.Contains(logArea(t, term), "発言 000") {
		t.Fatalf("先頭へ行けない:\n%s", logArea(t, term))
	}
	seen := map[int]bool{}
	collect := func() {
		for i := 0; i < total; i++ {
			if strings.Contains(logArea(t, term), fmt.Sprintf("発言 %03d", i)) {
				seen[i] = true
			}
		}
	}
	collect()
	for n := 0; n < total && !strings.Contains(term.screen[logHeaderRow(term)], "追従中"); n++ {
		s.ScrollPage(1)
		collect()
	}
	if len(seen) != total {
		t.Errorf("読めた発言 = %d / %d", len(seen), total)
	}
}

func TestScrollReadsWholeBoard(t *testing.T) {
	term, s := newSplitTerm(t, 24, 60)
	s.SetBoardVersion(9)
	s.SetBoard(boardLines(80))
	s.FocusNext() // 会議ボードへ

	bh := boardHeaderRow(term)
	if !strings.Contains(term.screen[bh], "版 9") {
		t.Errorf("見出しに版が無い: %q", term.screen[bh])
	}

	s.ScrollHome()
	seen := map[int]bool{}
	collect := func() {
		for i := 0; i < 80; i++ {
			if strings.Contains(boardArea(t, term), fmt.Sprintf("要点 %03d", i)) {
				seen[i] = true
			}
		}
	}
	collect()
	for n := 0; n < 200 && !strings.Contains(term.screen[boardHeaderRow(term)], "追従中"); n++ {
		s.ScrollPage(1)
		collect()
	}
	if len(seen) != 80 {
		t.Errorf("読めた要点 = %d / 80", len(seen))
	}
	// 「… 他 N 行」を唯一の手段にしない。
	if strings.Contains(term.All(), "他 ") && strings.Contains(term.All(), "行）") {
		t.Errorf("省略案内が残っている:\n%s", term.All())
	}
}

// 半ページ移動 (Ctrl+d / Ctrl+u) が効くこと。
func TestHalfPageScroll(t *testing.T) {
	term, s := newSplitTerm(t, 24, 60)
	for i := 0; i < 100; i++ {
		s.Log(fmt.Sprintf("[00:00:01] 発言 %03d", i))
	}
	s.ScrollHome()
	first := logArea(t, term)
	s.ScrollHalf(1)
	half := logArea(t, term)
	if half == first {
		t.Fatal("半ページ下がっていない")
	}
	s.ScrollPage(1)
	full := logArea(t, term)
	if full == half {
		t.Fatal("1 ページ下がっていない")
	}
	s.ScrollHalf(-1)
	if logArea(t, term) == full {
		t.Error("半ページ戻っていない")
	}
}

// --- REVIEW.md [P1-2] 古い発言を捨てないこと ---

// 大量の確定行でも、同じ pane から先頭まで読めること。
func TestKeepsAllLogLines(t *testing.T) {
	term := newFakeTerm(20, 60)
	s := NewSplit(term, 20, 60, false)
	s.Start()
	const total = 25000 // 以前の上限 (20000) を超える長さ
	for i := 0; i < total; i++ {
		s.Log(fmt.Sprintf("[00:00:01] 発言 %05d", i))
	}
	if len(s.logRaw) != total {
		t.Fatalf("保持行数 = %d, want %d (古い行を捨てている)", len(s.logRaw), total)
	}
	// 最新が見えること。
	if !strings.Contains(logArea(t, term), fmt.Sprintf("発言 %05d", total-1)) {
		t.Errorf("最新の発言が見えない:\n%s", logArea(t, term))
	}
	// 先頭も同じ pane から読めること。
	s.ScrollHome()
	if !strings.Contains(logArea(t, term), "発言 00000") {
		t.Errorf("最初の発言が読めない:\n%s", logArea(t, term))
	}
	// 見出しの総行数も実際の件数に一致すること。
	if !strings.Contains(term.screen[logHeaderRow(term)], fmt.Sprintf("/ %d 行", total)) {
		t.Errorf("総行数の表示が合わない: %q", term.screen[logHeaderRow(term)])
	}
	// 途中も読めること。
	s.ScrollEnd()
	s.ScrollHome()
	s.ScrollPage(100)
	if strings.TrimSpace(logArea(t, term)) == "" {
		t.Errorf("途中が空になった")
	}
}

// 折り返す長い行でも全件の位置計算が合うこと。
func TestKeepsAllLogLinesWithWrapping(t *testing.T) {
	term := newFakeTerm(20, 30)
	s := NewSplit(term, 20, 30, false)
	s.Start()
	for i := 0; i < 500; i++ {
		s.Log(fmt.Sprintf("[00:00:01] 発言 %03d %s", i, strings.Repeat("長い内容", 3)))
	}
	// 各行が 30 桁で複数行に折り返る前提。
	if s.logTotalRows() <= 500 {
		t.Fatalf("折り返しが効いていない: %d 行", s.logTotalRows())
	}
	s.ScrollHome()
	if !strings.Contains(logArea(t, term), "発言 000") {
		t.Errorf("先頭が読めない:\n%s", logArea(t, term))
	}
	s.ScrollEnd()
	if !strings.Contains(logArea(t, term), "発言 499") {
		t.Errorf("末尾が読めない:\n%s", logArea(t, term))
	}
	for i, line := range term.screen {
		if w := DisplayWidth(line); w > 30 {
			t.Errorf("行 %d が幅を超えた (%d): %q", i, w, line)
		}
	}
}

// --- 完了条件 3: 読んでいる間も更新が続き、位置が飛ばない ---

func TestScrolledLogDoesNotJumpOnNewLines(t *testing.T) {
	term, s := newSplitTerm(t, 24, 60)
	for i := 0; i < 50; i++ {
		s.Log(fmt.Sprintf("[00:00:01] 発言 %03d", i))
	}
	s.Scroll(-10)
	before := logArea(t, term)

	for i := 50; i < 60; i++ {
		s.Log(fmt.Sprintf("[00:00:02] 新しい発言 %03d", i))
	}
	if logArea(t, term) != before {
		t.Errorf("新着で表示位置が動いた:\n前:\n%s\n後:\n%s", before, logArea(t, term))
	}
	head := term.screen[logHeaderRow(term)]
	if !strings.Contains(head, "未読") {
		t.Errorf("未読の案内が無い: %q", head)
	}
	if strings.Contains(head, "追従中") {
		t.Errorf("追従中のままになっている: %q", head)
	}

	// G で最新追従へ戻れること。
	s.ScrollEnd()
	if !strings.Contains(logArea(t, term), "新しい発言 059") {
		t.Errorf("G で最新へ戻れない:\n%s", logArea(t, term))
	}
	if !strings.Contains(term.screen[logHeaderRow(term)], "追従中") {
		t.Errorf("追従が再開しない: %q", term.screen[logHeaderRow(term)])
	}
}

// 片方を読んでいる間も、もう片方は更新を続けること。
func TestOtherRegionKeepsUpdatingWhileScrolled(t *testing.T) {
	term, s := newSplitTerm(t, 24, 60)
	for i := 0; i < 40; i++ {
		s.Log(fmt.Sprintf("[00:00:01] 発言 %03d", i))
	}
	s.Scroll(-10) // ログを読んでいる
	logBefore := logArea(t, term)

	s.SetBoard(boardLines(3))
	if !strings.Contains(boardArea(t, term), "要点 002") {
		t.Errorf("会議ボードが更新されない:\n%s", boardArea(t, term))
	}
	if logArea(t, term) != logBefore {
		t.Errorf("要約更新でログの位置が動いた")
	}

	// 逆方向: 会議ボードを読んでいる間もログは流れる。
	s.ScrollEnd() // ログを最新追従へ戻す
	s.FocusNext()
	s.SetBoard(boardLines(40))
	s.Scroll(-5)
	boardBefore := boardArea(t, term)
	s.Log("[00:00:30] 読んでいる間の発言")
	if !strings.Contains(logArea(t, term), "読んでいる間の発言") {
		t.Errorf("ログが更新されない:\n%s", logArea(t, term))
	}
	if boardArea(t, term) != boardBefore {
		t.Errorf("ログ更新で会議ボードの位置が動いた")
	}
}

// 要約が書き換わっても閲覧位置を保ち、範囲外なら寄せること。
func TestBoardKeepsPositionAcrossUpdates(t *testing.T) {
	term, s := newSplitTerm(t, 24, 60)
	s.SetBoard(boardLines(60))
	s.FocusNext()
	s.ScrollHome()
	s.ScrollPage(1)
	before := boardArea(t, term)

	s.SetBoard(boardLines(80)) // 伸びる
	if boardArea(t, term) != before {
		t.Errorf("要約が伸びて表示位置が動いた:\n前:\n%s\n後:\n%s", before, boardArea(t, term))
	}

	s.SetBoard(boardLines(3)) // 大幅に縮む
	area := boardArea(t, term)
	if strings.TrimSpace(area) == "" {
		t.Errorf("縮小後に本文が空になった:\n%s", term.All())
	}
	if !strings.Contains(area, "要点 000") {
		t.Errorf("縮小後に近い位置へ寄っていない:\n%s", area)
	}
}

// --- 完了条件 4: 日本語・リサイズ・伸縮・暫定行で崩れない ---

func TestNoRowExceedsWidth(t *testing.T) {
	term, s := newSplitTerm(t, 20, 32)
	s.Log("[00:00:01] " + strings.Repeat("長い日本語の発言", 8))
	s.SetBoard([]string{"今の話題: 折り返し", strings.Repeat("長い要約の文章", 10)})
	s.Interim(strings.Repeat("暫定の長い発話", 6))

	for i, line := range term.screen {
		if w := DisplayWidth(line); w > 32 {
			t.Errorf("行 %d が幅を超えた (%d): %q", i, w, line)
		}
	}
}

func TestResizeKeepsBothRegionsAndPosition(t *testing.T) {
	term, s := newSplitTerm(t, 30, 80)
	// 幅 80 では 1 行、幅 40 では折り返す長さにする
	// (折り返しが変わっても位置が飛ばないことを見るため)。
	for i := 0; i < 80; i++ {
		s.Log(fmt.Sprintf("[00:00:01] 発言 %03d の内容 %s", i,
			strings.Repeat("長い日本語", 2)))
	}
	s.SetBoard(boardLines(40))
	// 特定の行が本文の先頭に来るようにスクロールする。
	s.ScrollHome()
	s.Scroll(30)
	top := strings.SplitN(strings.TrimSpace(logArea(t, term)), "\n", 2)[0]
	if top == "" {
		t.Fatalf("先頭行が空:\n%s", term.All())
	}

	term.Resize(20, 40)
	s.Resize(20, 40)

	if logHeaderRow(term) < 0 || boardHeaderRow(term) < 0 {
		t.Fatalf("リサイズで領域が消えた:\n%s", term.All())
	}
	for i, line := range term.screen {
		if w := DisplayWidth(line); w > 40 {
			t.Errorf("行 %d が新しい幅を超えた (%d): %q", i, w, line)
		}
	}
	// 折り返しが変わっても、読んでいた行が本文の先頭に残ること。
	// 時刻は全行同じなので、行を特定できる「発言 NNN」で確かめる。
	num := logNumberOf(t, top)
	newTop := strings.SplitN(strings.TrimSpace(logArea(t, term)), "\n", 2)[0]
	if got := logNumberOf(t, newTop); got != num {
		t.Errorf("リサイズで読んでいた位置が飛んだ: 発言 %s → 発言 %s (%q → %q)",
			num, got, top, newTop)
	}

	// 広げても破綻しないこと。
	term.Resize(40, 100)
	s.Resize(40, 100)
	if logHeaderRow(term) < 0 || boardHeaderRow(term) < 0 {
		t.Fatalf("拡大で領域が消えた:\n%s", term.All())
	}
	if strings.TrimSpace(logArea(t, term)) == "" {
		t.Errorf("拡大後にログが空:\n%s", term.All())
	}
}

// 要約の伸長・縮小でログが消えないこと (実機で出た症状)。
func TestBoardGrowShrinkKeepsLog(t *testing.T) {
	term, s := newSplitTerm(t, 24, 60)
	for i := 0; i < 10; i++ {
		s.Log(fmt.Sprintf("[00:00:01] 発言 %03d", i))
	}
	for round := 0; round < 3; round++ {
		s.SetBoard(boardLines(40))
		if !strings.Contains(logArea(t, term), "発言 009") {
			t.Fatalf("要約が伸びてログが消えた:\n%s", term.All())
		}
		s.SetBoard(boardLines(1))
		if !strings.Contains(logArea(t, term), "発言 009") {
			t.Fatalf("要約が縮んでログが消えた:\n%s", term.All())
		}
	}
	// 区切りが重複していないこと。
	if n := strings.Count(term.All(), "会議ボード"); n != 1 {
		t.Errorf("会議ボードの見出しが %d 個:\n%s", n, term.All())
	}
}

// 暫定行はログ末尾に出て、確定で置き換わること。
func TestInterimAtLogTail(t *testing.T) {
	term, s := newSplitTerm(t, 24, 60)
	s.Log("[00:00:01] 確定した発言")
	s.Interim("話している途中")

	area := logArea(t, term)
	if !strings.Contains(area, "話している途中") {
		t.Fatalf("暫定行が出ていない:\n%s", area)
	}
	if !strings.Contains(area, "確定した発言") {
		t.Fatalf("確定行が消えた:\n%s", area)
	}
	// 暫定行は会議ボード側へはみ出さない。
	if strings.Contains(boardArea(t, term), "話している途中") {
		t.Errorf("暫定行が会議ボード領域に出た:\n%s", term.All())
	}

	s.ClearInterim()
	s.Log("[00:00:02] 確定した発言 2")
	area = logArea(t, term)
	if strings.Contains(area, "話している途中") {
		t.Errorf("暫定行が残った:\n%s", area)
	}
	if strings.Count(area, "確定した発言") != 2 {
		t.Errorf("確定行が揃っていない:\n%s", area)
	}
}

// スクロール中は暫定行で画面が末尾へ飛ばないこと。
func TestInterimDoesNotJumpWhileScrolled(t *testing.T) {
	term, s := newSplitTerm(t, 24, 60)
	for i := 0; i < 50; i++ {
		s.Log(fmt.Sprintf("[00:00:01] 発言 %03d", i))
	}
	s.Scroll(-10)
	before := logArea(t, term)

	for i := 0; i < 5; i++ {
		s.Interim(fmt.Sprintf("話している途中 %d", i))
	}
	if logArea(t, term) != before {
		t.Errorf("暫定行で表示位置が動いた:\n前:\n%s\n後:\n%s", before, logArea(t, term))
	}
}

// 空の状態でも落ちず、両方の見出しが出ること。
func TestEmptyState(t *testing.T) {
	term, s := newSplitTerm(t, 20, 50)
	if logHeaderRow(term) < 0 || boardHeaderRow(term) < 0 {
		t.Fatalf("見出しが出ていない:\n%s", term.All())
	}
	if !strings.Contains(term.screen[logHeaderRow(term)], "0 行") {
		t.Errorf("行数表示が無い: %q", term.screen[logHeaderRow(term)])
	}
	s.Scroll(-5)
	s.ScrollPage(1)
	s.ScrollEnd()
	s.ScrollHome()
	if logHeaderRow(term) < 0 {
		t.Errorf("操作で表示が壊れた:\n%s", term.All())
	}
}

// --- 端末制御 ---

func TestStartStopAreIdempotent(t *testing.T) {
	term := newFakeTerm(20, 50)
	s := NewSplit(term, 20, 50, false)
	s.Start()
	s.Start()
	s.Stop()
	s.Stop()
	if term.top != 0 || term.bot != term.rows-1 {
		t.Errorf("スクロール領域が戻っていない: %d-%d", term.top, term.bot)
	}
}

// 描画は差分だけを書くこと (毎回全画面を書き直さない)。
func TestRendersOnlyChangedRows(t *testing.T) {
	term := newFakeTerm(24, 60)
	rec := &countingWriter{inner: term}
	s := NewSplit(rec, 24, 60, false)
	s.Start()
	s.SetBoard(boardLines(5))
	for i := 0; i < 5; i++ {
		s.Log(fmt.Sprintf("[00:00:01] 発言 %03d", i))
	}
	rec.reset()
	s.Log("[00:00:02] 次の発言")

	// 1 行追加で書き換わるのは、ログ本文の一部と見出しだけ。
	if rec.moves > 12 {
		t.Errorf("書き換えた行が多すぎる: %d", rec.moves)
	}
	if rec.moves == 0 {
		t.Error("何も描かれていない")
	}
}

// countingWriter は行移動の回数を数える。
type countingWriter struct {
	inner *fakeTerm
	moves int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.moves += strings.Count(string(p), "\x1b[2K")
	return w.inner.Write(p)
}

func (w *countingWriter) reset() { w.moves = 0 }

func TestTruncateToWidth(t *testing.T) {
	if got := truncateToWidth("あいうえお", 4); got != "あい" {
		t.Errorf("got = %q", got)
	}
	if got := truncateToWidth("abc", 10); got != "abc" {
		t.Errorf("got = %q", got)
	}
}

// logNumberOf は "発言 NNN" の番号を取り出す (無ければ空)。
func logNumberOf(t *testing.T, line string) string {
	t.Helper()
	i := strings.Index(line, "発言 ")
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(line[i+len("発言 "):])
	if f := strings.Fields(rest); len(f) > 0 {
		return f[0]
	}
	return ""
}

// --- REVIEW.md [P1-1] Console の直接出力で画面が崩れないこと ---

// 分割表示中の con.Info / con.Warn が画面をスクロールさせないこと。
//
// 実機では v.Start 後の案内が画面全体を流し、差分描画では
// 会議ボードの位置が戻らなかった。
func TestConsoleOutputGoesToLogArea(t *testing.T) {
	term := newFakeTerm(24, 60)
	s := NewSplit(term, 24, 60, false)
	con := NewWith(term, true)
	s.Start()
	con.Redirect(s)

	s.SetBoard([]string{"今の話題: 会議", "要約:", "・要点"})
	boardRowBefore := boardHeaderRow(term)
	if boardRowBefore < 0 {
		t.Fatalf("会議ボードが無い:\n%s", term.All())
	}

	con.Info("保存先       : %s", "/tmp/m.txt")
	con.Info("録音を開始しました。Ctrl-C で終了します。")
	con.Warn("自動要約を開始できません: %v", "claude が無い")
	con.Final("[00:00:01] 確定した発言")

	// 会議ボードの位置が動いていないこと。
	if got := boardHeaderRow(term); got != boardRowBefore {
		t.Errorf("会議ボードの位置が動いた: %d → %d\n%s", boardRowBefore, got, term.All())
	}
	// 本文が残っていること (画面が流れていない)。
	if !strings.Contains(boardArea(t, term), "要点") {
		t.Errorf("会議ボードの本文が消えた:\n%s", term.All())
	}
	// 案内はログ領域に入ること。
	area := logArea(t, term)
	for _, want := range []string{"保存先", "録音を開始しました", "警告: 自動要約", "確定した発言"} {
		if !strings.Contains(area, want) {
			t.Errorf("ログ領域に %q が無い:\n%s", want, area)
		}
	}
	// ログ領域の行数として数えられていること (スクロール位置が合う)。
	if !strings.Contains(term.screen[logHeaderRow(term)], "/ 4 行") {
		t.Errorf("行数が合わない: %q", term.screen[logHeaderRow(term)])
	}

	// Redirect を外すと従来どおり直接出力へ戻ること。
	con.Redirect(nil)
	before := len(s.logRaw)
	con.Info("直接出力")
	if len(s.logRaw) != before {
		t.Error("Redirect 解除後もログへ流れている")
	}
}

// 暫定行も Console 経由で分割表示へ渡ること。
func TestConsoleInterimGoesToSplit(t *testing.T) {
	term := newFakeTerm(24, 60)
	s := NewSplit(term, 24, 60, false)
	con := NewWith(term, true)
	s.Start()
	con.Redirect(s)

	con.Interim("話している途中")
	if !strings.Contains(logArea(t, term), "話している途中") {
		t.Errorf("暫定行がログ領域に出ていない:\n%s", term.All())
	}
	con.ClearInterim()
	if strings.Contains(logArea(t, term), "話している途中") {
		t.Errorf("暫定行が消えていない:\n%s", term.All())
	}
}
