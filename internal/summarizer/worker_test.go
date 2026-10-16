package summarizer

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSource は録音プロセスの代わり。
type fakeSource struct {
	mu      sync.Mutex
	lines   []Line
	topic   string
	summary string
	applied int
	version int

	published int
	lastBase  int
	// conflictOnce を立てると次の公開だけ版ずれにする。
	conflictOnce bool
}

func (s *fakeSource) SummarySnapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	from := s.applied
	if from > len(s.lines) {
		from = len(s.lines)
	}
	return Snapshot{
		Lines:          append([]Line(nil), s.lines[from:]...),
		Total:          len(s.lines),
		Topic:          s.topic,
		Summary:        s.summary,
		AppliedThrough: from,
		Version:        s.version,
	}
}

func (s *fakeSource) PublishSummary(topic, summary string, applied, base int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastBase = base
	if s.conflictOnce {
		s.conflictOnce = false
		s.version++ // 手動公開が入った状況
		return ErrVersionConflict
	}
	if base != s.version {
		return ErrVersionConflict
	}
	s.topic, s.summary, s.applied = topic, summary, applied
	s.version++
	s.published++
	return nil
}

func (s *fakeSource) addLines(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < n; i++ {
		idx := len(s.lines)
		s.lines = append(s.lines, Line{
			Index: idx,
			At:    fmt.Sprintf("00:00:%02d", idx),
			Text:  fmt.Sprintf("発言 %d", idx),
		})
	}
}

func (s *fakeSource) snapshotState() (topic, summary string, applied, version, published int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.topic, s.summary, s.applied, s.version, s.published
}

// fakeProvider は AI の代わり。
type fakeProvider struct {
	mu    sync.Mutex
	calls int
	out   Output
	err   error
	delay time.Duration
	// grow が true なら呼ぶたびに要約を伸ばす。
	grow bool
}

func (p *fakeProvider) Name() string { return "fake" }

func (p *fakeProvider) Summarize(ctx context.Context, in Input) (Output, error) {
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return Output{}, ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return Output{}, p.err
	}
	if p.grow {
		body := strings.TrimSpace(in.Summary)
		for _, l := range in.NewLines {
			if body != "" {
				body += "\n"
			}
			body += fmt.Sprintf("・[%s] %s", l.At, l.Text)
		}
		return Output{Topic: fmt.Sprintf("話題 %d", p.calls), Summary: body}, nil
	}
	return p.out, nil
}

func (p *fakeProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func testConfig() Config {
	c := DefaultConfig()
	c.MinInterval = 10 * time.Millisecond
	c.MaxInterval = 50 * time.Millisecond
	c.FinalTimeout = time.Second
	c.Tick = 5 * time.Millisecond
	return c
}

// --- トリガー ---

// 新しい発言が無ければ AI を呼ばないこと。
func TestNoCallWithoutNewLines(t *testing.T) {
	src := &fakeSource{}
	p := &fakeProvider{out: Output{Topic: "x", Summary: "・y"}}
	w := New(src, p, testConfig())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	w.Run(ctx)

	if p.callCount() != 0 {
		t.Errorf("発言が無いのに %d 回呼んだ", p.callCount())
	}
}

// 既定の行数がたまったら呼ぶこと。
func TestRunsWhenEnoughNewLines(t *testing.T) {
	src := &fakeSource{}
	p := &fakeProvider{grow: true}
	cfg := testConfig()
	cfg.MinNewLines = 3
	w := New(src, p, cfg)

	src.addLines(3)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go w.Run(ctx)

	waitFor(t, 2*time.Second, func() bool { return p.callCount() >= 1 })
	w.Stop()

	_, summary, applied, _, published := src.snapshotState()
	if published < 1 {
		t.Fatalf("公開されていない")
	}
	if applied != 3 {
		t.Errorf("反映位置 = %d, want 3", applied)
	}
	if !strings.Contains(summary, "発言 0") {
		t.Errorf("要約に発言が入っていない: %q", summary)
	}
}

// 行数が足りなくても一定時間で呼ぶこと。
func TestRunsAfterMaxInterval(t *testing.T) {
	src := &fakeSource{}
	p := &fakeProvider{grow: true}
	cfg := testConfig()
	cfg.MinNewLines = 10 // 行数では届かない
	cfg.MinInterval = 10 * time.Millisecond
	cfg.MaxInterval = 30 * time.Millisecond
	w := New(src, p, cfg)

	src.addLines(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go w.Run(ctx)

	waitFor(t, 3*time.Second, func() bool { return p.callCount() >= 1 })
	w.Stop()
}

// 1 行目で即座に呼ばないこと (最初の時間基準は開始時)。
func TestDoesNotCallImmediatelyOnFirstLine(t *testing.T) {
	src := &fakeSource{}
	p := &fakeProvider{grow: true}
	cfg := testConfig()
	cfg.MinNewLines = 1
	cfg.MinInterval = 300 * time.Millisecond
	cfg.MaxInterval = time.Second
	w := New(src, p, cfg)

	src.addLines(1)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	w.Run(ctx)

	if p.callCount() != 0 {
		t.Errorf("開始直後に呼んだ: %d 回", p.callCount())
	}
}

// --- 失敗とバックオフ ---

// 失敗したら反映位置を進めないこと。
func TestFailureDoesNotAdvanceApplied(t *testing.T) {
	src := &fakeSource{}
	p := &fakeProvider{err: fmt.Errorf("AI が落ちた")}
	w := New(src, p, testConfig())

	src.addLines(5)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go w.Run(ctx)
	waitFor(t, time.Second, func() bool { return p.callCount() >= 1 })
	w.Stop()

	_, _, applied, _, published := src.snapshotState()
	if applied != 0 {
		t.Errorf("失敗したのに反映位置が進んだ: %d", applied)
	}
	if published != 0 {
		t.Errorf("失敗したのに公開した: %d 回", published)
	}
	st := w.Status()
	if st.State != StateFailed || st.LastError == "" {
		t.Errorf("失敗が記録されていない: %+v", st)
	}
}

// 失敗が続いたら呼び出し間隔を広げること。
func TestBackoffAfterFailures(t *testing.T) {
	src := &fakeSource{}
	p := &fakeProvider{err: fmt.Errorf("失敗")}
	cfg := testConfig()
	cfg.MinInterval = 20 * time.Millisecond
	cfg.MaxInterval = time.Hour // 時間での発火を避ける
	cfg.MinNewLines = 1
	w := New(src, p, cfg)

	src.addLines(1)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	w.Run(ctx)

	// バックオフが無ければ 400ms / 20ms = 約 20 回呼ばれる。
	if n := p.callCount(); n > 8 {
		t.Errorf("バックオフが効いていない: %d 回", n)
	}
	if n := p.callCount(); n == 0 {
		t.Error("1 度も呼ばれていない")
	}
}

// 要約が大幅に短くなったら反映しないこと。
func TestRejectsDrasticShrink(t *testing.T) {
	src := &fakeSource{
		summary: strings.Repeat("あ", 100),
		version: 1,
	}
	p := &fakeProvider{out: Output{Topic: "x", Summary: "短い"}}
	w := New(src, p, testConfig())

	src.addLines(5)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go w.Run(ctx)
	waitFor(t, time.Second, func() bool { return w.Status().State == StateFailed })
	w.Stop()

	_, summary, _, _, published := src.snapshotState()
	if published != 0 {
		t.Errorf("大幅に短い要約を反映した")
	}
	if summary == "短い" {
		t.Errorf("要約が置き換わった")
	}
}

// --- 競合 ---

// 手動公開で版が進んでいたら反映せず、次回に回すこと。
func TestVersionConflictSkipsPublish(t *testing.T) {
	src := &fakeSource{conflictOnce: true}
	p := &fakeProvider{grow: true}
	w := New(src, p, testConfig())

	src.addLines(5)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go w.Run(ctx)

	// 1 回目は競合で見送り、2 回目で公開されること。
	waitFor(t, 2*time.Second, func() bool {
		_, _, _, _, published := src.snapshotState()
		return published >= 1
	})
	w.Stop()

	if st := w.Status(); st.State == StateFailed {
		t.Errorf("競合を失敗として扱った: %+v", st)
	}
}

// --- 最終要約 ---

// 未反映の発言を最後に 1 回まとめて要約すること。
func TestFinalSummarizesRemaining(t *testing.T) {
	src := &fakeSource{}
	p := &fakeProvider{grow: true}
	w := New(src, p, testConfig())

	src.addLines(4)
	state := w.Final(context.Background())
	if state != StateDone {
		t.Fatalf("最終要約の結果 = %s", state)
	}
	_, summary, applied, _, published := src.snapshotState()
	if published != 1 {
		t.Errorf("公開回数 = %d, want 1", published)
	}
	if applied != 4 {
		t.Errorf("反映位置 = %d, want 4", applied)
	}
	if !strings.Contains(summary, "発言 3") {
		t.Errorf("最後の発言が入っていない: %q", summary)
	}
}

// 未反映が無ければ何もしないこと。
func TestFinalWithoutPendingLines(t *testing.T) {
	src := &fakeSource{}
	p := &fakeProvider{grow: true}
	w := New(src, p, testConfig())

	if state := w.Final(context.Background()); state != StateDone {
		t.Errorf("結果 = %s, want done", state)
	}
	if p.callCount() != 0 {
		t.Errorf("発言が無いのに呼んだ: %d 回", p.callCount())
	}
}

// 最終要約が失敗しても状態として返すこと。
func TestFinalReportsFailure(t *testing.T) {
	src := &fakeSource{}
	p := &fakeProvider{err: fmt.Errorf("AI が落ちた")}
	w := New(src, p, testConfig())

	src.addLines(2)
	if state := w.Final(context.Background()); state != StateFailed {
		t.Errorf("結果 = %s, want failed", state)
	}
	if _, _, applied, _, _ := src.snapshotState(); applied != 0 {
		t.Errorf("失敗したのに反映位置が進んだ: %d", applied)
	}
}

// 最終要約がタイムアウトしたら timeout を返すこと。
func TestFinalTimeout(t *testing.T) {
	src := &fakeSource{}
	p := &fakeProvider{delay: time.Second, grow: true}
	cfg := testConfig()
	cfg.FinalTimeout = 50 * time.Millisecond
	w := New(src, p, cfg)

	src.addLines(2)
	state := w.Final(context.Background())
	if state != StateTimeout && state != StateFailed {
		t.Errorf("結果 = %s, want timeout/failed", state)
	}
	if _, _, applied, _, _ := src.snapshotState(); applied != 0 {
		t.Errorf("タイムアウトしたのに反映位置が進んだ: %d", applied)
	}
}

// --- 状態 ---

func TestStatusReportsPendingLines(t *testing.T) {
	src := &fakeSource{}
	w := New(src, &fakeProvider{grow: true}, testConfig())
	src.addLines(7)

	st := w.Status()
	if st.PendingLines != 7 {
		t.Errorf("未処理行数 = %d, want 7", st.PendingLines)
	}
	if !st.Enabled || st.Provider != "fake" {
		t.Errorf("状態が不正: %+v", st)
	}
}

func TestDisabledStatus(t *testing.T) {
	st := DisabledStatus("無効です")
	if st.Enabled || st.State != StateDisabled || st.LastError == "" {
		t.Errorf("状態が不正: %+v", st)
	}
}

// waitFor は条件が満たされるまで待つ。
func waitFor(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("条件が %v 以内に満たされなかった", limit)
}
