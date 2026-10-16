package app

import (
	"context"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/konono/sokki/internal/config"
	"github.com/konono/sokki/internal/transcript"
	"github.com/konono/sokki/internal/ui"
	"github.com/konono/sokki/internal/vad"
)

// fakeTranscriber は決まった文字列を返す推論バックエンド。
type fakeTranscriber struct {
	mu    sync.Mutex
	calls int
	delay time.Duration
	texts []string
	// byLen が設定されていれば PCM 長で応答を決める
	// (呼び出し順に依存しないテスト用)。
	byLen map[int]string
	// err が設定されていれば常に失敗する。
	err error
}

func (f *fakeTranscriber) Transcribe(ctx context.Context, pcm []int16, sr int) (string, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if f.byLen != nil {
		return f.byLen[len(pcm)], nil
	}
	if i < len(f.texts) {
		return f.texts[i], nil
	}
	return "", nil
}

func (f *fakeTranscriber) Close() error { return nil }

// scriptedReader は「発話→無音」を繰り返す PCM 供給源。
type scriptedReader struct {
	mu     sync.Mutex
	frames [][]int16
	i      int
	// drained が閉じたら以降は EOF を返す。
	exhausted chan struct{}
	once      sync.Once
}

func (r *scriptedReader) Read(n int) ([]int16, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.i >= len(r.frames) {
		r.once.Do(func() { close(r.exhausted) })
		return nil, io.EOF
	}
	f := r.frames[r.i]
	r.i++
	return f, nil
}

func toneFrame(amp float64) []int16 {
	f := make([]int16, config.FrameSamples)
	for i := range f {
		f[i] = int16(amp * 32767 * math.Sin(float64(i)*0.3))
	}
	return f
}

func buildFrames(silentLead, speech, silentTail int) [][]int16 {
	var out [][]int16
	for i := 0; i < silentLead; i++ {
		out = append(out, toneFrame(0.0005))
	}
	for i := 0; i < speech; i++ {
		out = append(out, toneFrame(0.2))
	}
	for i := 0; i < silentTail; i++ {
		out = append(out, toneFrame(0.0005))
	}
	return out
}

func newSegmenter(interim bool) *vad.Segmenter {
	cfg := config.Default()
	return vad.New(vad.Options{
		SampleRate:      config.SampleRate,
		FrameSamples:    config.FrameSamples,
		SilenceHangover: cfg.SilenceHangover,
		MinSpeech:       cfg.MinSpeech,
		MaxSegment:      cfg.MaxSegment,
		Preroll:         cfg.Preroll,
		FloorRatio:      cfg.VADFloorRatio,
		AbsThreshold:    cfg.VADAbsThreshold,
		ShowInterim:     interim,
		InterimAfter:    cfg.InterimAfter,
		InterimInterval: cfg.InterimInterval,
	})
}

// REVIEW.md [P1] の回帰テスト。
// Run と同じ context 結線 (取り込み = 割り込み ctx / ワーカー = 分離 ctx) を
// 通し、Ctrl-C 後も検出済み Final と Flush の Final が保存されること。
func TestShutdownKeepsFinalsWithRunWiring(t *testing.T) {
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(false) })

	dir := t.TempDir()
	start := time.Date(2026, 10, 16, 10, 0, 0, 0, time.Local)
	writer, err := transcript.New(dir, start)
	if err != nil {
		t.Fatal(err)
	}

	// 1 件目: 無音で確定する発話。2 件目: 発話途中で入力が尽きる (Flush 対象)。
	frames := buildFrames(50, 100, 60)
	frames = append(frames, buildFrames(0, 100, 0)...)
	reader := &scriptedReader{frames: frames, exhausted: make(chan struct{})}

	// Run と同じ結線。
	interruptCtx, interrupt := context.WithCancel(context.Background())
	workCtx, cancelWork := context.WithCancel(context.Background())
	defer cancelWork()

	// 推論を遅くしてキューを滞留させ、Ctrl-C と競合させる。
	fake := &fakeTranscriber{
		delay: 150 * time.Millisecond,
		texts: []string{"最初の発話です", "終了直前の発話です"},
	}
	disp := newDispatcher(1, drainTimeout)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		transcribeLoop(workCtx, config.Default(), fake, writer, &view{con: ui.New()}, disp.events, nil)
	}()

	// 入力が尽きたタイミングで Ctrl-C を撃つ。
	go func() {
		<-reader.exhausted
		interrupt()
	}()

	seg := newSegmenter(false)
	_ = captureLoop(interruptCtx, reader, seg, disp)

	// Run の終了処理と同じ順序: Flush → 送信 → クローズ → ドレイン待ち。
	shuttingDown.Store(true)
	if ev, ok := seg.Flush(); ok {
		if !disp.SendFinal(ev) {
			t.Error("Flush の確定区間を送れなかった")
		}
	}
	disp.Close()
	wg.Wait()

	if err := writer.Close(start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if disp.Dropped() != 0 {
		t.Errorf("確定区間を %d 件落とした", disp.Dropped())
	}
	if writer.Count() < 1 {
		t.Fatalf("Ctrl-C 後に確定行が保存されなかった: %d 行", writer.Count())
	}
	t.Logf("保存行数: %d", writer.Count())
}

// captureLoop が Flush 分も含めて取りこぼさないことを、
// segmenter を共有した形で確認する。
func TestCaptureLoopFlushDeliversPendingSpeech(t *testing.T) {
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(false) })

	// 発話の途中で入力が尽きる = Flush でしか拾えない区間。
	frames := buildFrames(50, 100, 0)
	reader := &scriptedReader{frames: frames, exhausted: make(chan struct{})}
	seg := newSegmenter(false)

	interruptCtx, interrupt := context.WithCancel(context.Background())
	go func() { <-reader.exhausted; interrupt() }()

	disp := newDispatcher(4, time.Second)
	_ = captureLoop(interruptCtx, reader, seg, disp)

	ev, ok := seg.Flush()
	if !ok {
		t.Fatal("Flush すべき区間が無い (テスト前提が崩れている)")
	}
	if !disp.SendFinal(ev) {
		t.Fatal("Ctrl-C 後に Flush の確定区間を送れなかった")
	}
	disp.Close()

	var finals int
	for e := range disp.events {
		if e.Kind == vad.Final {
			finals++
		}
	}
	if finals < 1 {
		t.Fatalf("確定区間が届いていない: %d 件", finals)
	}
}

// 終了処理中は暫定結果を処理しない (確定の保存を優先する)。
func TestInterimDroppedWhileShuttingDown(t *testing.T) {
	shuttingDown.Store(true)
	t.Cleanup(func() { shuttingDown.Store(false) })

	dir := t.TempDir()
	writer, err := transcript.New(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close(time.Now())

	events := make(chan vad.Event, 4)
	events <- vad.Event{Kind: vad.Interim, Audio: make([]int16, 16000)}
	events <- vad.Event{Kind: vad.Final, Audio: make([]int16, 16000), Start: time.Second, End: 2 * time.Second}
	close(events)

	fake := &fakeTranscriber{texts: []string{"暫定は捨てられるべき", "確定は保存されるべき"}}
	transcribeLoop(context.Background(), config.Default(), fake, writer, &view{con: ui.New()}, events, nil)

	if fake.calls != 1 {
		t.Fatalf("推論回数 = %d, want 1 (暫定を捨てていない)", fake.calls)
	}
	if writer.Count() != 1 {
		t.Fatalf("確定行数 = %d, want 1", writer.Count())
	}
}

// 幻聴の定型句はファイルに書かれない。
func TestHallucinationNotSavedThroughLoop(t *testing.T) {
	shuttingDown.Store(false)
	dir := t.TempDir()
	writer, err := transcript.New(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close(time.Now())

	events := make(chan vad.Event, 2)
	events <- vad.Event{Kind: vad.Final, Audio: make([]int16, 16000), Start: time.Second, End: 2 * time.Second}
	close(events)

	fake := &fakeTranscriber{texts: []string{"ご視聴ありがとうございました"}}
	transcribeLoop(context.Background(), config.Default(), fake, writer, &view{con: ui.New()}, events, nil)

	if writer.Count() != 0 {
		t.Fatalf("無音由来の定型句が保存された: %d 行", writer.Count())
	}
	if !strings.HasSuffix(writer.Path(), ".txt") {
		t.Fatalf("保存先が .txt でない: %s", writer.Path())
	}
}

// cancelingReader は指定フレームを返す直前に ctx をキャンセルする。
// 「Read 後・Final 送信前に Ctrl-C が入る」という欠落の窓を再現する。
type cancelingReader struct {
	frames   [][]int16
	i        int
	cancelAt int
	cancel   context.CancelFunc
}

func (r *cancelingReader) Read(n int) ([]int16, error) {
	if r.i >= len(r.frames) {
		return nil, io.EOF
	}
	if r.i == r.cancelAt {
		r.cancel() // ここで Ctrl-C
	}
	f := r.frames[r.i]
	r.i++
	return f, nil
}

// REVIEW.md [P1] の回帰テスト (本命)。
//
// Read 直後・Final 送信前に Ctrl-C が入り、かつキューに空きが無い状況で、
// 確定区間が捨てられないこと。
// 旧実装 (`select { case events <- ev: case <-ctx.Done(): return }`) では
// キャンセル側が必ず選ばれて確定区間を失っていた。
func TestFinalNotDroppedWhenCancelledMidSend(t *testing.T) {
	shuttingDown.Store(false)
	t.Cleanup(func() { shuttingDown.Store(false) })

	const (
		lead     = 50  // 無音 (雑音フロア学習)
		speech   = 100 // 発話
		hangover = 35  // 700ms / 20ms = 無音ハングオーバー
	)
	frames := buildFrames(lead, speech, hangover+20)

	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelingReader{
		frames: frames,
		// ハングオーバーが満了して Final が出るフレームでキャンセルする。
		cancelAt: lead + speech + hangover - 1,
		cancel:   cancel,
	}

	// 空きの無いキュー。消費は遅れて始まる。
	disp := newDispatcher(0, 3*time.Second)
	received := make(chan vad.Event, 8)
	go func() {
		time.Sleep(250 * time.Millisecond)
		for ev := range disp.events {
			received <- ev
		}
		close(received)
	}()

	_ = captureLoop(ctx, reader, newSegmenter(false), disp)
	disp.Close()

	var finals int
	for ev := range received {
		if ev.Kind == vad.Final {
			finals++
		}
	}
	if finals != 1 {
		t.Fatalf("Ctrl-C と同時に確定区間を失った: 受信 %d 件, want 1 (dropped=%d)",
			finals, disp.Dropped())
	}
}
