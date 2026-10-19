package summarizer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// State はワーカーの状態。
type State string

const (
	StateDisabled State = "disabled"
	StateIdle     State = "idle"
	StateRunning  State = "running"
	StateFailed   State = "failed"
	StateDone     State = "done"
	StatePending  State = "pending"
	StateTimeout  State = "timeout"
)

// ErrVersionConflict は公開しようとした版が古い場合に返す。
var ErrVersionConflict = errors.New("会議ボードが更新されています")

// Snapshot は要約の材料。
type Snapshot struct {
	// Lines は AppliedThrough 以降の確定行。
	Lines []Line
	// Total は確定行の総数。
	Total int
	// Topic / Summary は現在の会議ボード。
	Topic   string
	Summary string
	// AppliedThrough は反映済みの位置。
	AppliedThrough int
	// Version は現在の会議ボードの版。
	Version int
}

// Source は録音プロセス側の窓口。
type Source interface {
	// SummarySnapshot は現在の材料を返す。
	SummarySnapshot() Snapshot
	// PublishSummary は版が base のままなら会議ボードを差し替える。
	// 版が進んでいれば ErrVersionConflict を返す。
	PublishSummary(topic, summary string, appliedThrough, base int) error
}

// Config はワーカーの設定値。
type Config struct {
	MinNewLines  int
	MinInterval  time.Duration
	MaxInterval  time.Duration
	MaxChars     int
	FinalTimeout time.Duration
	// Tick は更新条件を評価する間隔 (AI は呼ばない)。
	Tick time.Duration
	// MinShrinkRatio は、新しい要約が既存比でこの割合を下回ったら
	// 反映しない (過去の要点を落とす事故を防ぐ)。0 で無効。
	MinShrinkRatio float64
}

// DefaultConfig は合意済みの初期値を返す。
func DefaultConfig() Config {
	return Config{
		MinNewLines:    8,
		MinInterval:    90 * time.Second,
		MaxInterval:    180 * time.Second,
		MaxChars:       2000,
		FinalTimeout:   60 * time.Second,
		Tick:           time.Second,
		MinShrinkRatio: 0.5,
	}
}

// Status はワーカーの現在の状態 (get_status 用)。
type Status struct {
	Enabled        bool      `json:"enabled"`
	State          State     `json:"state"`
	Provider       string    `json:"provider,omitempty"`
	Readiness      string    `json:"readiness,omitempty"`
	ReadinessError string    `json:"readiness_error,omitempty"`
	Version        int       `json:"version"`
	PendingLines   int       `json:"pending_lines"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	Failures       int       `json:"consecutive_failures"`
	FinalState     State     `json:"final_state,omitempty"`
}

type readinessProvider interface {
	Readiness() (string, error)
	ReadyDone() <-chan struct{}
	CancelPreloadIfLoading()
	StopBackground()
	Close()
}

// Worker は確定行から会議ボードを自動更新する。
//
// 録音プロセス内で動き、AI クライアントの接続とは独立している。
type Worker struct {
	src      Source
	provider Provider
	cfg      Config
	now      func() time.Time

	mu       sync.Mutex
	status   Status
	lastRun  time.Time
	backoff  time.Duration
	stopOnce sync.Once
	stopped  chan struct{}
	done     chan struct{}
}

// New はワーカーを作る。
func New(src Source, p Provider, cfg Config) *Worker {
	w := &Worker{
		src:      src,
		provider: p,
		cfg:      cfg,
		now:      time.Now,
		stopped:  make(chan struct{}),
		done:     make(chan struct{}),
	}
	w.status = Status{Enabled: true, State: StateIdle}
	if p != nil {
		w.status.Provider = p.Name()
	}
	return w
}

// Status は現在の状態を返す。
//
// Source の参照は mutex の外で行う。worker.mu を保持したまま
// Source を呼ぶと、呼び出し元が live のロックを持っている場合に
// ロック順が逆転して詰まる。
func (w *Worker) Status() Status {
	w.mu.Lock()
	s := w.status
	w.mu.Unlock()
	if p, ok := w.provider.(readinessProvider); ok {
		var err error
		s.Readiness, err = p.Readiness()
		if err != nil {
			s.ReadinessError = err.Error()
		}
	}

	snap := w.src.SummarySnapshot()
	if n := snap.Total - snap.AppliedThrough; n > 0 {
		s.PendingLines = n
	}
	return s
}

// Run は更新のループを回す。ctx の完了で戻る。
func (w *Worker) Run(ctx context.Context) {
	defer close(w.done)

	// 最初の時間基準は会議開始時に置く (1 行目で即座に呼ばない)。
	w.mu.Lock()
	w.lastRun = w.now()
	w.mu.Unlock()

	tick := w.cfg.Tick
	if tick <= 0 {
		tick = time.Second
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopped:
			return
		case <-ticker.C:
			if w.shouldRun() {
				w.runOnce(ctx, false)
			}
		}
	}
}

// Stop はループを止める (最終要約は別に呼ぶ)。
func (w *Worker) Stop() {
	if p, ok := w.provider.(readinessProvider); ok {
		p.CancelPreloadIfLoading()
		p.StopBackground()
	}
	w.stopOnce.Do(func() { close(w.stopped) })
	<-w.done
}

// ReadyDone closes after background preload succeeds or fails; nil for CLI providers.
func (w *Worker) ReadyDone() <-chan struct{} {
	if p, ok := w.provider.(readinessProvider); ok {
		return p.ReadyDone()
	}
	return nil
}

// CloseProvider stops background refresh after the final summary.
func (w *Worker) CloseProvider() {
	if p, ok := w.provider.(readinessProvider); ok {
		p.Close()
	}
}

// shouldRun は更新すべきかを判定する。
func (w *Worker) shouldRun() bool {
	if p, ok := w.provider.(readinessProvider); ok {
		ready, _ := p.Readiness()
		if ready != "ready" {
			return false
		}
	}
	// Source は mutex の外で読む (ロック順を worker.mu → live.mu にしない)。
	snap := w.src.SummarySnapshot()

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status.State == StateRunning {
		return false
	}
	newLines := snap.Total - snap.AppliedThrough
	if newLines <= 0 {
		return false // 発言が無ければ呼ばない
	}
	// 最初の 1～2 行は予告・挨拶・文の断片だけのことがある。
	// 最大待ち時間に達しても、初回は最低 3 行の文脈を待つ。
	if snap.Version == 0 && newLines < 3 {
		return false
	}
	since := w.now().Sub(w.lastRun)
	wait := w.cfg.MinInterval
	if w.backoff > wait {
		wait = w.backoff
	}
	if since < wait {
		return false
	}
	return newLines >= w.cfg.MinNewLines || since >= w.cfg.MaxInterval
}

// runOnce は 1 回の更新を行う。
func (w *Worker) runOnce(ctx context.Context, final bool) {
	snap := w.src.SummarySnapshot()
	if len(snap.Lines) == 0 && !final {
		return
	}

	w.mu.Lock()
	w.status.State = StateRunning
	w.lastRun = w.now()
	w.mu.Unlock()

	out, err := w.provider.Summarize(ctx, Input{
		Topic:    snap.Topic,
		Summary:  snap.Summary,
		NewLines: snap.Lines,
		MaxChars: w.cfg.MaxChars,
		Final:    final,
	})
	if err != nil {
		w.fail(err)
		return
	}
	if err := w.validate(snap, out); err != nil {
		w.fail(err)
		return
	}

	applied := snap.AppliedThrough + len(snap.Lines)
	topic := strings.TrimSpace(out.Topic)
	if topic == "" {
		topic = snap.Topic // 話題が返らなければ前の値を保つ
	}
	if err := w.src.PublishSummary(topic, out.Summary, applied, snap.Version); err != nil {
		if errors.Is(err, ErrVersionConflict) {
			// 手動公開が入った。次の回で読み直す。
			w.mu.Lock()
			w.status.State = StateIdle
			w.mu.Unlock()
			return
		}
		w.fail(err)
		return
	}

	w.mu.Lock()
	w.status.State = StateIdle
	w.status.Version = snap.Version + 1
	w.status.UpdatedAt = w.now()
	w.status.LastError = ""
	w.status.Failures = 0
	w.backoff = 0
	w.mu.Unlock()
}

// validate は明らかにおかしい応答を弾く。
func (w *Worker) validate(snap Snapshot, out Output) error {
	got := strings.TrimSpace(out.Summary)
	if got == "" {
		return fmt.Errorf("要約が空です")
	}
	if w.cfg.MinShrinkRatio > 0 {
		old := len([]rune(strings.TrimSpace(snap.Summary)))
		if old > 0 {
			limit := int(float64(old) * w.cfg.MinShrinkRatio)
			if len([]rune(got)) < limit {
				return fmt.Errorf(
					"要約が大幅に短くなりました (%d → %d 文字)。過去の要点を失う恐れがあるため反映しません",
					old, len([]rune(got)))
			}
		}
	}
	return nil
}

// fail は失敗を記録し、次回までの間隔を広げる。
func (w *Worker) fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status.State = StateFailed
	w.status.LastError = err.Error()
	w.status.Failures++
	// 指数バックオフ (上限 10 分)。反映位置は進めない。
	if w.backoff == 0 {
		w.backoff = w.cfg.MinInterval
	} else {
		w.backoff *= 2
	}
	if max := 10 * time.Minute; w.backoff > max {
		w.backoff = max
	}
}

// Final は会議終了時の最終要約を 1 回だけ行う。
//
// 未反映の確定行が無ければ何もしない。
func (w *Worker) Final(ctx context.Context) State {
	snap := w.src.SummarySnapshot()
	if len(snap.Lines) == 0 {
		w.setFinal(StateDone)
		return StateDone
	}
	if p, ok := w.provider.(readinessProvider); ok {
		ready, err := p.Readiness()
		if ready != "ready" {
			if err == nil {
				err = fmt.Errorf("Ollama モデルを準備中のため最終要約を実施しません")
			}
			w.fail(err)
			w.setFinal(StateFailed)
			return StateFailed
		}
	}
	timeout := w.cfg.FinalTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	w.mu.Lock()
	w.backoff = 0 // 最終要約はバックオフを無視する
	w.mu.Unlock()

	w.runOnce(ctx, true)

	w.mu.Lock()
	defer w.mu.Unlock()
	state := StateDone
	if w.status.State == StateFailed {
		state = StateFailed
		if ctx.Err() == context.DeadlineExceeded {
			state = StateTimeout
		}
	}
	w.status.FinalState = state
	return state
}

func (w *Worker) setFinal(s State) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status.FinalState = s
}

// DisabledStatus は自動要約を使わない場合の状態を返す。
func DisabledStatus(reason string) Status {
	return Status{Enabled: false, State: StateDisabled, LastError: reason}
}
