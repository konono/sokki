package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/konono/sokki/internal/board"
	"github.com/konono/sokki/internal/config"
	"github.com/konono/sokki/internal/ipc"
	"github.com/konono/sokki/internal/transcript"
	"github.com/konono/sokki/internal/ui"
	"github.com/konono/sokki/internal/vad"
)

// REALTIME_PLAN の表示タイミング要件を、既定値そのままで検証する。
// 音声デバイスは使わない (合成した PCM フレームのみ)。

// collectEvents は既定値の Segmenter にフレームを流してイベントを集める。
func collectEvents(t *testing.T, frames [][]int16) []vad.Event {
	t.Helper()
	seg := newSegmenter(true)
	var evs []vad.Event
	for _, f := range frames {
		evs = append(evs, seg.Push(f)...)
	}
	if ev, ok := seg.Flush(); ok {
		evs = append(evs, ev)
	}
	return evs
}

func kinds(evs []vad.Event, k vad.Kind) []vad.Event {
	var out []vad.Event
	for _, e := range evs {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// 完了条件: 発話開始から約 1 秒で最初の暫定、その後約 1 秒ごとに更新。
func TestInterimStartsAboutOneSecondAndRepeatsEverySecond(t *testing.T) {
	cfg := config.Default()
	if cfg.InterimAfter != time.Second || cfg.InterimInterval != time.Second {
		t.Fatalf("既定値が 1 秒でない: after=%v interval=%v", cfg.InterimAfter, cfg.InterimInterval)
	}

	// 無音 1 秒 → 発話 6 秒 → 無音 1 秒
	frames := buildFrames(50, 300, 50)
	evs := collectEvents(t, frames)

	interims := kinds(evs, vad.Interim)
	if len(interims) < 4 {
		t.Fatalf("6 秒の発話で暫定が %d 回しか出ていない (毎秒更新になっていない)", len(interims))
	}

	// 最初の暫定は発話開始から約 1 秒 (preroll 0.3s + 立ち上がり 0.1s を含む)。
	first := interims[0].End - interims[0].Start
	if first < 900*time.Millisecond || first > 1800*time.Millisecond {
		t.Errorf("最初の暫定までが約 1 秒でない: %v", first)
	}

	// 以後の間隔は約 1 秒。
	for i := 1; i < len(interims); i++ {
		gap := interims[i].End - interims[i-1].End
		if gap < 800*time.Millisecond || gap > 1300*time.Millisecond {
			t.Errorf("暫定 %d→%d の間隔が約 1 秒でない: %v", i-1, i, gap)
		}
	}
}

// 完了条件: 各更新は発話開始からの音声を再認識する (暫定文全体を差し替える)。
func TestInterimCarriesAudioFromSegmentStart(t *testing.T) {
	frames := buildFrames(50, 300, 50)
	interims := kinds(collectEvents(t, frames), vad.Interim)
	if len(interims) < 2 {
		t.Fatalf("暫定が足りない: %d", len(interims))
	}
	for i, ev := range interims {
		// すべて同じ区間の先頭から始まっていること。
		if ev.Start != interims[0].Start {
			t.Errorf("暫定 %d の開始位置が異なる: %v (want %v)", i, ev.Start, interims[0].Start)
		}
		// 音声長が単調に伸びていること (続きを足して再認識する)。
		if i > 0 && len(ev.Audio) <= len(interims[i-1].Audio) {
			t.Errorf("暫定 %d の音声が伸びていない: %d → %d",
				i, len(interims[i-1].Audio), len(ev.Audio))
		}
	}
}

// 完了条件: 約 0.5 秒の無音で確定する。
func TestSilenceAboutHalfSecondFinalizes(t *testing.T) {
	if got := config.Default().SilenceHangover; got != 500*time.Millisecond {
		t.Fatalf("無音判定の既定値が 0.5 秒でない: %v", got)
	}
	// 発話 2 秒 → 無音 0.7 秒 (0.5 秒で確定するはず)
	frames := buildFrames(50, 100, 35)
	finals := kinds(collectEvents(t, frames), vad.Final)
	if len(finals) != 1 {
		t.Fatalf("0.7 秒の無音で確定しなかった: %d 件", len(finals))
	}
}

// 完了条件 2: 短い発話は最初の暫定表示を待たずに確定する。
func TestShortUtteranceFinalizesWithoutInterim(t *testing.T) {
	// 発話 0.6 秒 (暫定の 1 秒に届かない) → 無音 0.8 秒
	frames := buildFrames(50, 30, 40)
	evs := collectEvents(t, frames)

	if n := len(kinds(evs, vad.Interim)); n != 0 {
		t.Errorf("1 秒未満の発話で暫定が出た: %d 件", n)
	}
	finals := kinds(evs, vad.Final)
	if len(finals) != 1 {
		t.Fatalf("短い発話が確定しなかった: %d 件", len(finals))
	}
}

// 完了条件 1/4: 暫定は上書きされ、確定は 1 回だけ表示・保存される。
//
// 実際のタイミングを再現するため、ワーカーが 1 件ずつ処理し終えてから
// 次を送る (未処理の後続が無い状態 = 暫定が実際に表示される状態)。
func TestInterimOverwrittenAndFinalSavedOnce(t *testing.T) {
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(false) })

	dir := t.TempDir()
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)
	writer, err := transcript.New(dir, start)
	if err != nil {
		t.Fatal(err)
	}

	// 音声長ごとの認識結果 (発話が伸びるほど文が伸びる)。
	fake := &fakeTranscriber{byLen: map[int]string{
		16000: "本日の",
		32000: "本日の会議を",
		48000: "本日の会議を始めます",
	}}

	var buf bytes.Buffer
	con := ui.NewWith(&buf, true) // TTY 扱い

	events := make(chan vad.Event) // 非バッファ = 1 件ずつ確実に処理させる
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		transcribeLoop(context.Background(), config.Default(), fake, writer, &view{con: con}, events, nil)
	}()

	events <- vad.Event{Kind: vad.Interim, Audio: make([]int16, 16000), Start: time.Second, End: 2 * time.Second}
	events <- vad.Event{Kind: vad.Interim, Audio: make([]int16, 32000), Start: time.Second, End: 3 * time.Second}
	events <- vad.Event{Kind: vad.Final, Audio: make([]int16, 48000), Start: time.Second, End: 4 * time.Second}
	close(events)
	wg.Wait()

	if err := writer.Close(start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	// 暫定が順に表示され、後続の音声で書き直されていること。
	for _, want := range []string{"本日の", "本日の会議を"} {
		if !strings.Contains(out, want) {
			t.Errorf("暫定 %q が表示されていない:\n%q", want, out)
		}
	}
	// 上書き (復帰文字 + 行クリア) を使っていること。
	if !strings.Contains(out, "\r") {
		t.Errorf("暫定行が上書き表示されていない:\n%q", out)
	}
	// 確定行はちょうど 1 回。
	if n := strings.Count(out, "[00:00:01] 本日の会議を始めます\n"); n != 1 {
		t.Errorf("確定行の表示回数 = %d, want 1\n%q", n, out)
	}

	// 保存は確定分のみ 1 行。
	data, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if n := strings.Count(content, "[00:00:01] 本日の会議を始めます"); n != 1 {
		t.Errorf("保存された確定行 = %d 行, want 1\n%s", n, content)
	}
	// 途中の暫定文がファイルに残っていないこと。
	for _, interim := range []string{"[00:00:01] 本日の\n", "[00:00:01] 本日の会議を\n"} {
		if strings.Contains(content, interim) {
			t.Errorf("暫定文が保存された: %q\n%s", interim, content)
		}
	}
	if writer.Count() != 1 {
		t.Errorf("保存行数 = %d, want 1", writer.Count())
	}
}

// 完了条件 3: 暫定が溜まったら古いものを捨て、最新だけを認識する。
func TestStaleInterimsDroppedLatestWins(t *testing.T) {
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(false) })

	dir := t.TempDir()
	writer, err := transcript.New(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close(time.Now())

	fake := &fakeTranscriber{byLen: map[int]string{
		16000: "古い暫定1", 32000: "古い暫定2", 48000: "古い暫定3",
		64000: "古い暫定4", 80000: "最新の暫定",
	}}
	var buf bytes.Buffer
	con := ui.NewWith(&buf, true)

	// 推論が追いつかず 5 つの暫定が溜まった状況。
	events := make(chan vad.Event, 16)
	for i := 1; i <= 5; i++ {
		events <- vad.Event{
			Kind:  vad.Interim,
			Audio: make([]int16, 16000*i),
			Start: time.Second,
			End:   time.Duration(i+1) * time.Second,
		}
	}
	close(events)

	transcribeLoop(context.Background(), config.Default(), fake, writer, &view{con: con}, events, nil)

	// 推論は最新の 1 回だけ。
	if fake.calls != 1 {
		t.Errorf("推論回数 = %d, want 1 (古い暫定を捨てていない)", fake.calls)
	}
	if !strings.Contains(buf.String(), "最新の暫定") {
		t.Errorf("最新の暫定が表示されていない:\n%q", buf.String())
	}
	if writer.Count() != 0 {
		t.Errorf("暫定が保存された: %d 行", writer.Count())
	}
}

// 確定が待っている間は暫定を一切処理せず、確定を優先して保存すること。
func TestPendingFinalTakesPriorityOverQueuedInterims(t *testing.T) {
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(false) })

	dir := t.TempDir()
	writer, err := transcript.New(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close(time.Now())

	fake := &fakeTranscriber{byLen: map[int]string{
		16000: "暫定A", 32000: "暫定B", 96000: "確定した文",
	}}
	var buf bytes.Buffer
	con := ui.NewWith(&buf, true)

	events := make(chan vad.Event, 8)
	events <- vad.Event{Kind: vad.Interim, Audio: make([]int16, 16000), Start: time.Second, End: 2 * time.Second}
	events <- vad.Event{Kind: vad.Interim, Audio: make([]int16, 32000), Start: time.Second, End: 3 * time.Second}
	events <- vad.Event{Kind: vad.Final, Audio: make([]int16, 96000), Start: time.Second, End: 4 * time.Second}
	close(events)

	transcribeLoop(context.Background(), config.Default(), fake, writer, &view{con: con}, events, nil)

	// 確定が既にあるので、溜まった暫定の推論は行わない。
	if fake.calls != 1 {
		t.Errorf("推論回数 = %d, want 1 (確定待ちなのに暫定を推論した)", fake.calls)
	}
	if writer.Count() != 1 {
		t.Fatalf("確定行数 = %d, want 1", writer.Count())
	}
	data, _ := os.ReadFile(writer.Path())
	if !strings.Contains(string(data), "確定した文") {
		t.Errorf("確定文が保存されていない:\n%s", data)
	}
}

// 完了条件 4: 非 TTY では制御文字を出さず、確定行だけを出す。
func TestNonTTYEmitsNoControlCharacters(t *testing.T) {
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(false) })

	dir := t.TempDir()
	writer, err := transcript.New(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close(time.Now())

	fake := &fakeTranscriber{byLen: map[int]string{
		16000: "暫定テキスト", 32000: "確定テキスト",
	}}
	var buf bytes.Buffer
	con := ui.NewWith(&buf, false) // 非 TTY

	events := make(chan vad.Event) // 非バッファ = 暫定も必ず処理させる
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		transcribeLoop(context.Background(), config.Default(), fake, writer, &view{con: con}, events, nil)
	}()
	events <- vad.Event{Kind: vad.Interim, Audio: make([]int16, 16000), Start: time.Second, End: 2 * time.Second}
	events <- vad.Event{Kind: vad.Final, Audio: make([]int16, 32000), Start: time.Second, End: 3 * time.Second}
	close(events)
	wg.Wait()

	out := buf.String()
	for _, ctrl := range []string{"\r", "\x1b["} {
		if strings.Contains(out, ctrl) {
			t.Errorf("非 TTY に制御文字が出た (%q):\n%q", ctrl, out)
		}
	}
	if strings.Contains(out, "暫定テキスト") {
		t.Errorf("非 TTY に暫定結果が出た:\n%q", out)
	}
	if !strings.Contains(out, "確定テキスト") {
		t.Errorf("確定結果が出ていない:\n%q", out)
	}
}

// 完了条件 3: 連続発話でも Ctrl-C 時に残る確定区間を保存できる
// (既定の短い無音・暫定間隔でも成立すること)。
func TestContinuousSpeechDrainsFinalsOnShutdownWithNewDefaults(t *testing.T) {
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(false) })

	dir := t.TempDir()
	writer, err := transcript.New(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// 発話 → 無音 → 発話 の途中で入力が尽きる。
	frames := buildFrames(50, 100, 40)
	frames = append(frames, buildFrames(0, 100, 0)...)
	reader := &scriptedReader{frames: frames, exhausted: make(chan struct{})}

	interruptCtx, interrupt := context.WithCancel(context.Background())
	go func() { <-reader.exhausted; interrupt() }()

	fake := &fakeTranscriber{delay: 50 * time.Millisecond, texts: []string{
		"最初の発話", "途中の暫定", "終了直前の発話",
	}}
	disp := newDispatcher(4, drainTimeout)
	var buf bytes.Buffer
	con := ui.NewWith(&buf, false)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		transcribeLoop(context.Background(), config.Default(), fake, writer, &view{con: con}, disp.events, nil)
	}()

	seg := newSegmenter(true)
	_ = captureLoop(interruptCtx, reader, seg, disp)

	shuttingDown.Store(true)
	if ev, ok := seg.Flush(); ok {
		if !disp.SendFinal(ev) {
			t.Error("Flush の確定区間を送れなかった")
		}
	}
	disp.Close()
	wg.Wait()

	if err := writer.Close(time.Now()); err != nil {
		t.Fatal(err)
	}
	if disp.Dropped() != 0 {
		t.Errorf("確定区間を %d 件落とした", disp.Dropped())
	}
	if writer.Count() < 1 {
		t.Fatalf("Ctrl-C 後に確定行が保存されなかった: %d 行", writer.Count())
	}
}

// REALTIME_PLAN の UI 例をそのまま検証する。
//
//	… [00:00:03] こんにち
//	… [00:00:03] こんにちは、日本の皆さん     ← 同じ 1 行を更新
//	[00:00:03] こんにちは、日本の皆さん       ← 無音後に確定行へ
//
// 時刻は暫定と確定で同じであること、暫定だけが "…" を持つこと、
// 更新は同じ 1 行の上書きで行われることを確認する。
func TestUIExampleSameLineUpdatesThenFinalizes(t *testing.T) {
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(false) })

	dir := t.TempDir()
	start := time.Date(2026, 10, 16, 14, 0, 0, 0, time.Local)
	writer, err := transcript.New(dir, start)
	if err != nil {
		t.Fatal(err)
	}

	const at = 3 * time.Second // 会議開始から 3 秒の発話
	fake := &fakeTranscriber{byLen: map[int]string{
		16000: "こんにち",
		32000: "こんにちは、日本の皆さん",
		48000: "こんにちは、日本の皆さん",
	}}

	var buf bytes.Buffer
	con := ui.NewWith(&buf, true)

	events := make(chan vad.Event) // 1 件ずつ処理させる
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		transcribeLoop(context.Background(), config.Default(), fake, writer, &view{con: con}, events, nil)
	}()
	events <- vad.Event{Kind: vad.Interim, Audio: make([]int16, 16000), Start: at, End: at + time.Second}
	events <- vad.Event{Kind: vad.Interim, Audio: make([]int16, 32000), Start: at, End: at + 2*time.Second}
	events <- vad.Event{Kind: vad.Final, Audio: make([]int16, 48000), Start: at, End: at + 3*time.Second}
	close(events)
	wg.Wait()

	if err := writer.Close(start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	t.Logf("端末出力:\n%q", out)

	// 1. 暫定は "… [00:00:03] ..." の形で出ること。
	for _, want := range []string{"… [00:00:03] こんにち", "… [00:00:03] こんにちは、日本の皆さん"} {
		if !strings.Contains(out, want) {
			t.Errorf("暫定表示 %q が無い:\n%q", want, out)
		}
	}
	// 2. 確定は "…" なしの行として 1 回だけ出ること。
	finalLine := "[00:00:03] こんにちは、日本の皆さん\n"
	if n := strings.Count(out, finalLine); n != 1 {
		t.Errorf("確定行の表示回数 = %d, want 1:\n%q", n, out)
	}
	if strings.Contains(out, "… [00:00:03] こんにちは、日本の皆さん\n") {
		t.Errorf("確定行に暫定の \"…\" が付いている:\n%q", out)
	}
	// 3. 暫定の更新は改行せず、同じ行の上書きで行うこと。
	//    確定までに出る改行は 1 回 (確定行の末尾) だけ。
	if n := strings.Count(out, "\n"); n != 1 {
		t.Errorf("改行が %d 回。暫定が行を増やしている:\n%q", n, out)
	}
	// 4. 時刻は暫定と確定で同じであること。
	if strings.Contains(out, "[00:00:04]") || strings.Contains(out, "[00:00:05]") {
		t.Errorf("暫定と確定で時刻がずれている:\n%q", out)
	}
	// 5. 保存されるのは確定行 1 行だけ。
	data, _ := os.ReadFile(writer.Path())
	if !strings.Contains(string(data), "[00:00:03] こんにちは、日本の皆さん") {
		t.Errorf("確定行が保存されていない:\n%s", data)
	}
	if strings.Contains(string(data), "こんにち\n") {
		t.Errorf("途中の暫定文が保存された:\n%s", data)
	}
	if writer.Count() != 1 {
		t.Errorf("保存行数 = %d, want 1", writer.Count())
	}
}

// 暫定文は訂正で短くなることもある。前の行の文字が残らないこと。
func TestInterimCanShrinkWithoutLeftovers(t *testing.T) {
	// 色無効 (空白で消す経路) を明示的に試す。
	t.Setenv("NO_COLOR", "1")

	var buf bytes.Buffer
	con := ui.NewWith(&buf, true)

	con.Interim("[00:00:03] こんにちは、日本の皆さん")
	con.Interim("[00:00:03] こんにちは") // 訂正で短くなった
	con.Final("[00:00:03] こんにちは。")

	out := buf.String()
	t.Logf("端末出力:\n%q", out)

	// 最後に表示されるのは確定行。前の長い暫定の残りが見えないこと。
	lastNL := strings.LastIndex(out, "\n")
	if lastNL < 0 {
		t.Fatalf("確定行が出ていない:\n%q", out)
	}
	// 確定行の直前には、前の行を消すための復帰と空白があること。
	tail := out[:lastNL]
	if !strings.Contains(tail, "\r") {
		t.Errorf("行をクリアしていない:\n%q", out)
	}
	if !strings.HasSuffix(out, "[00:00:03] こんにちは。\n") {
		t.Errorf("確定行で終わっていない:\n%q", out)
	}
	// 「日本の皆さん」が確定行の後ろに残っていないこと。
	after := out[strings.LastIndex(out, "\r"):]
	if strings.Contains(after, "日本の皆さん") {
		t.Errorf("短くなった際に前の文字が残った:\n%q", out)
	}
}

// --- REVIEW.md [P1] 確定後に暫定行を残さないこと ---

// splitView は分割表示付きの view を用意する (実デバイスは使わない)。
func splitView(t *testing.T, rows, cols int) (*ui.Split, *view) {
	t.Helper()
	var out bytes.Buffer
	con := ui.NewWith(&out, true)
	s := ui.NewSplit(&out, rows, cols, false)
	s.Start()
	con.Redirect(s)
	t.Cleanup(func() { con.Redirect(nil); s.Stop() })
	return s, &view{con: con, split: s}
}

// screenOf は画面に出ている内容をまとめて返す。
func screenOf(s *ui.Split) string { return strings.Join(s.Snapshot(), "\n") }

// 暫定 → 確定が成功した経路で、古い暫定が画面と IPC に残らないこと。
func TestFinalClearsInterimOnSuccess(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	writer, err := transcript.New(dir, start)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close(start.Add(time.Minute))

	fake := &fakeTranscriber{byLen: map[int]string{
		16000: "本日の",
		48000: "本日の会議を始めます",
	}}
	sp, v := splitView(t, 24, 60)
	tpath := filepath.Join(dir, "m1.txt")
	lv := newLive("m1", "sokki", tpath, start, board.NewStore(tpath), nil)

	events := make(chan vad.Event)
	done := make(chan struct{})
	go func() {
		defer close(done)
		transcribeLoop(context.Background(), config.Default(), fake, writer, v, events, lv)
	}()
	events <- vad.Event{Kind: vad.Interim, Audio: make([]int16, 16000), Start: time.Second, End: 2 * time.Second}
	events <- vad.Event{Kind: vad.Final, Audio: make([]int16, 48000), Start: time.Second, End: 4 * time.Second}
	close(events)
	<-done

	screen := screenOf(sp)
	if !strings.Contains(screen, "本日の会議を始めます") {
		t.Fatalf("確定行が出ていない:\n%s", screen)
	}
	// 古い暫定 ("本日の" の暫定表示) が残っていないこと。
	if strings.Contains(screen, "… ") {
		t.Errorf("暫定行が画面に残った:\n%s", screen)
	}
	if got := interimOf(lv); got != "" {
		t.Errorf("IPC の暫定が残った: %q", got)
	}
}

// 雑音・推論失敗・保存失敗の確定経路でも暫定が残らないこと。
func TestFinalClearsInterimOnFailurePaths(t *testing.T) {
	cases := []struct {
		name  string
		final string // 確定区間の認識結果
		err   error  // 推論エラー
		close bool   // 保存先を先に閉じて保存失敗にする
	}{
		// IsNoise が実際に落とす定型句を使う (whisper が無音区間で出しやすい)。
		{name: "雑音で捨てられる", final: "ご視聴ありがとうございました"},
		{name: "同一文字の繰り返し", final: "ーーーー"},
		{name: "空文字", final: "   "},
		{name: "推論失敗", err: errors.New("whisper が落ちた")},
		{name: "保存失敗", final: "確定の本文", close: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			start := time.Now()
			writer, err := transcript.New(dir, start)
			if err != nil {
				t.Fatal(err)
			}
			if tc.close {
				// 保存先を閉じてから確定を流し、Append を失敗させる。
				if err := writer.Close(start.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			} else {
				defer writer.Close(start.Add(time.Minute))
			}

			fake := &fakeTranscriber{
				byLen: map[int]string{16000: "途中まで", 48000: tc.final},
				err:   tc.err,
			}
			sp, v := splitView(t, 24, 60)
			tpath := filepath.Join(dir, "m1.txt")
			lv := newLive("m1", "sokki", tpath, start, board.NewStore(tpath), nil)

			events := make(chan vad.Event)
			done := make(chan struct{})
			go func() {
				defer close(done)
				transcribeLoop(context.Background(), config.Default(), fake, writer, v, events, lv)
			}()
			if tc.err == nil {
				// 推論失敗の場合は暫定も失敗するので、成功時だけ暫定を置く。
				events <- vad.Event{Kind: vad.Interim, Audio: make([]int16, 16000),
					Start: time.Second, End: 2 * time.Second}
			} else {
				lv.setInterim("00:00:01", "途中まで")
				v.Interim("[00:00:01] 途中まで")
			}
			events <- vad.Event{Kind: vad.Final, Audio: make([]int16, 48000),
				Start: time.Second, End: 4 * time.Second}
			close(events)
			<-done

			if screen := screenOf(sp); strings.Contains(screen, "途中まで") {
				t.Errorf("暫定行が画面に残った:\n%s", screen)
			}
			if got := interimOf(lv); got != "" {
				t.Errorf("IPC の暫定が残った: %q", got)
			}
		})
	}
}

// interimOf は IPC が返す暫定行を取り出す。
func interimOf(lv *live) string {
	resp := lv.Handle(ipc.Request{Op: ipc.OpContext})
	return resp.Interim
}
