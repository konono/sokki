package translation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	groupWindow  = 15 * time.Second
	finishLimit  = 30 * time.Second
	waitingLimit = 256
	targetLimit  = 8 << 10
	contextLimit = 4 << 10
)

// Unit is a snapshot. Closed describes the source range; State describes the
// current translation response. Neither implies the other.
type Unit struct {
	ID            int       `json:"unit_id"`
	Version       int       `json:"version"`
	SourceIndices []int     `json:"source_indices"`
	Source        []string  `json:"source"`
	Context       []string  `json:"context_only,omitempty"`
	OpenedAt      time.Time `json:"opened_at"`
	ClosedAt      time.Time `json:"closed_at,omitempty"`
	CloseReason   string    `json:"close_reason,omitempty"`
	Closed        bool      `json:"closed"`
	State         string    `json:"state"`
	Japanese      string    `json:"japanese,omitempty"`
	Error         string    `json:"error,omitempty"`
	RequestAt     time.Time `json:"request_at,omitempty"`
	CompletedAt   time.Time `json:"completed_at,omitempty"`
	Seq           uint64    `json:"seq"`
}

type Status struct {
	Readiness      string
	ReadinessError string
	Pending        int
	Failed         []int
	Untranslated   []int
	Unsaved        []int
	StorageError   string
	Units          int
}
type job struct{ id, version int }
type change struct{ snapshot Unit }

// Worker owns source grouping, requests, and a separate, ordered output sink.
// Accept takes only its state lock; it never waits for HTTP, a terminal, or disk.
type Worker struct {
	mu                      sync.Mutex
	client                  Provider
	path                    string
	onUpdate                func(Unit)
	onStorageError          func(error)
	ctx                     context.Context
	cancel                  context.CancelFunc
	preloadCancel           context.CancelFunc
	readyDone               chan struct{}
	genDone                 chan struct{}
	sinkDone                chan struct{}
	wake                    chan struct{}
	sinkWake                chan struct{}
	readiness               string
	readinessError          string
	units                   []*Unit
	source                  []string
	open                    *Unit
	timer                   *time.Timer
	jobs                    []job
	changes                 []change
	nextSeq                 uint64
	persisted               map[int]uint64
	storageError            string
	sourceClosed            bool
	sinkClosed              bool
	expired                 bool
	deadline                time.Time
	finishTimer             *time.Timer
	activeID, activeVersion int
	activeCancel            context.CancelFunc
}

func New(model, url, path string, onUpdate func(Unit), onStorageError func(error)) (*Worker, error) {
	return NewWithProvider("ollama", model, url, path, onUpdate, onStorageError)
}

func NewWithProvider(provider, model, url, path string, onUpdate func(Unit), onStorageError func(error)) (*Worker, error) {
	c, err := makeProvider(provider, model, url)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	preloadCtx, preloadCancel := context.WithTimeout(ctx, startupLimit)
	w := &Worker{client: c, path: path, onUpdate: onUpdate, onStorageError: onStorageError, ctx: ctx, cancel: cancel, preloadCancel: preloadCancel, readyDone: make(chan struct{}), genDone: make(chan struct{}), sinkDone: make(chan struct{}), wake: make(chan struct{}, 1), sinkWake: make(chan struct{}, 1), readiness: "loading", persisted: make(map[int]uint64)}
	go w.preload(preloadCtx)
	go w.generate()
	go w.sink()
	return w, nil
}

func clone(u *Unit) Unit {
	v := *u
	v.SourceIndices = append([]int(nil), u.SourceIndices...)
	v.Source = append([]string(nil), u.Source...)
	v.Context = append([]string(nil), u.Context...)
	return v
}
func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
func (w *Worker) emitLocked(u *Unit) {
	w.nextSeq++
	u.Seq = w.nextSeq
	w.changes = append(w.changes, change{clone(u)})
	signal(w.sinkWake)
}
func (w *Worker) queueLocked(u *Unit) {
	// Only the latest unstarted version of a unit can be useful.
	kept := w.jobs[:0]
	for _, j := range w.jobs {
		if j.id != u.ID {
			kept = append(kept, j)
		}
	}
	w.jobs = kept
	if len([]byte(strings.Join(u.Source, "\n"))) > targetLimit {
		u.State = "failed"
		u.Error = "input_too_long"
		w.emitLocked(u)
		return
	}
	if len(w.jobs) >= waitingLimit {
		u.State = "untranslated"
		u.Error = "backlog_limit"
		w.emitLocked(u)
		return
	}
	u.State = "queued"
	u.Error = ""
	w.jobs = append(w.jobs, job{u.ID, u.Version})
	w.emitLocked(u)
	signal(w.wake)
}

// Accept receives exactly one saved transcript line with its source index.
func (w *Worker) Accept(index int, line string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sourceClosed {
		return
	}
	now := time.Now()
	if w.open != nil && !now.Before(w.open.OpenedAt.Add(groupWindow)) {
		w.sealLocked(w.open, "15s", now)
	}
	if w.open == nil {
		from := len(w.source) - 2
		if from < 0 {
			from = 0
		}
		ctxLines := append([]string(nil), w.source[from:]...)
		for len(strings.Join(ctxLines, "\n")) > contextLimit && len(ctxLines) > 0 {
			ctxLines = ctxLines[1:]
		}
		u := &Unit{ID: len(w.units) + 1, Version: 1, SourceIndices: []int{index}, Source: []string{line}, Context: ctxLines, OpenedAt: now, State: "queued"}
		w.units = append(w.units, u)
		w.open = u
		// Enqueue the initial source slot before any request can publish a response.
		w.emitLocked(u)
		id := u.ID
		w.timer = time.AfterFunc(groupWindow, func() { w.seal(id, "15s") })
		if w.readiness == "failed" {
			u.State = "untranslated"
			u.Error = w.readinessError
			w.emitLocked(u)
		} else {
			w.queueLocked(u)
		}
	} else {
		u := w.open
		w.open = nil
		if w.timer != nil {
			w.timer.Stop()
			w.timer = nil
		}
		u.Version++
		u.SourceIndices = append(u.SourceIndices, index)
		u.Source = append(u.Source, line)
		u.Japanese = ""
		u.Error = ""
		u.CompletedAt = time.Time{}
		u.RequestAt = time.Time{}
		u.Closed = true
		u.ClosedAt = now
		u.CloseReason = "second_source"
		if w.activeID == u.ID && w.activeVersion < u.Version && w.activeCancel != nil {
			w.activeCancel()
		}
		if w.readiness == "failed" {
			u.State = "untranslated"
			u.Error = w.readinessError
			w.emitLocked(u)
		} else {
			w.queueLocked(u)
		}
	}
	w.source = append(w.source, line)
}

func (w *Worker) seal(id int, reason string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.open != nil && w.open.ID == id {
		w.sealLocked(w.open, reason, time.Now())
	}
}
func (w *Worker) sealLocked(u *Unit, reason string, now time.Time) {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.open = nil
	u.Closed = true
	u.ClosedAt = now
	u.CloseReason = reason
	if u.State == "complete" {
		u.State = "final"
	}
	w.emitLocked(u)
}

func (w *Worker) preload(ctx context.Context) {
	defer w.preloadCancel()
	err := w.client.preload(ctx)
	w.mu.Lock()
	if w.readiness != "loading" {
		close(w.readyDone)
		w.mu.Unlock()
		signal(w.wake)
		return
	}
	if err != nil {
		w.readiness = "failed"
		if w.readinessError == "" {
			w.readinessError = err.Error()
		}
		for _, u := range w.units {
			if u.State == "queued" {
				u.State = "untranslated"
				u.Error = w.readinessError
				w.emitLocked(u)
			}
		}
	} else {
		w.readiness = "ready"
	}
	close(w.readyDone)
	w.mu.Unlock()
	signal(w.wake)
}

func (w *Worker) generate() {
	defer close(w.genDone)
	select {
	case <-w.readyDone:
	case <-w.ctx.Done():
		return
	}
	for {
		w.mu.Lock()
		if w.readiness != "ready" && !w.sourceClosed && w.ctx.Err() == nil {
			w.mu.Unlock()
			select {
			case <-w.wake:
			case <-w.ctx.Done():
			}
			continue
		}
		if w.readiness != "ready" || w.ctx.Err() != nil {
			w.sinkClosed = true
			w.mu.Unlock()
			signal(w.sinkWake)
			return
		}
		var j job
		found := false
		for len(w.jobs) > 0 {
			j = w.jobs[0]
			w.jobs = w.jobs[1:]
			u := w.units[j.id-1]
			if u.Version == j.version && u.State == "queued" {
				found = true
				break
			}
		}
		if !found {
			if w.sourceClosed {
				w.sinkClosed = true
				w.mu.Unlock()
				signal(w.sinkWake)
				return
			}
			w.mu.Unlock()
			select {
			case <-w.wake:
			case <-w.ctx.Done():
			}
			continue
		}
		u := w.units[j.id-1]
		reqCtx, cancel := context.WithTimeout(w.ctx, requestLimit)
		if !w.deadline.IsZero() {
			var deadlineCancel context.CancelFunc
			reqCtx, deadlineCancel = context.WithDeadline(reqCtx, w.deadline)
			old := cancel
			cancel = func() { deadlineCancel(); old() }
		}
		w.activeID, w.activeVersion, w.activeCancel = j.id, j.version, cancel
		u.State = "generating"
		u.RequestAt = time.Now()
		w.emitLocked(u)
		target := make([]string, len(u.Source))
		for i, line := range u.Source {
			target[i] = speechText(line)
		}
		ctxLines := make([]string, len(u.Context))
		for i, line := range u.Context {
			ctxLines[i] = speechText(line)
		}
		w.mu.Unlock()
		result, err := w.client.translate(reqCtx, ctxLines, target, func(part string) {
			w.mu.Lock()
			if j.id <= len(w.units) {
				cur := w.units[j.id-1]
				if cur.Version == j.version && cur.ID == len(w.units) && w.ctx.Err() == nil {
					cur.State = "provisional"
					cur.Japanese = part
					w.emitLocked(cur)
				}
			}
			w.mu.Unlock()
		})
		cancel()
		w.mu.Lock()
		w.activeID, w.activeVersion, w.activeCancel = 0, 0, nil
		cur := w.units[j.id-1]
		if cur.Version == j.version && w.ctx.Err() == nil {
			cur.CompletedAt = time.Now()
			if err != nil {
				cur.State = "failed"
				cur.Error = err.Error()
				cur.Japanese = ""
			} else {
				cur.State = "complete"
				if cur.Closed {
					cur.State = "final"
				}
				cur.Japanese = result
				cur.Error = ""
			}
			w.emitLocked(cur)
		}
		w.mu.Unlock()
	}
}

func (w *Worker) sink() {
	defer close(w.sinkDone)
	store := newStore(w.path)
	for {
		w.mu.Lock()
		if len(w.changes) == 0 {
			if w.sinkClosed {
				w.mu.Unlock()
				return
			}
			w.mu.Unlock()
			<-w.sinkWake
			continue
		}
		ev := w.changes[0]
		w.changes = w.changes[1:]
		current := clone(w.units[ev.snapshot.ID-1])
		w.mu.Unlock()
		if w.onUpdate != nil {
			w.onUpdate(current)
		}
		if err := store.save(ev.snapshot, current); err != nil {
			w.mu.Lock()
			first := w.storageError == ""
			w.storageError = err.Error()
			w.mu.Unlock()
			if first && w.onStorageError != nil {
				w.onStorageError(err)
			}
		} else {
			w.mu.Lock()
			if current.Seq > w.persisted[current.ID] {
				w.persisted[current.ID] = current.Seq
			}
			w.mu.Unlock()
		}
	}
}

// CloseSource seals the final group and starts the absolute finish deadline.
// It must be called immediately after the source writer closes, before summary Final.
func (w *Worker) CloseSource() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sourceClosed {
		return w.deadline
	}
	w.sourceClosed = true
	w.deadline = time.Now().Add(finishLimit)
	if w.open != nil {
		w.sealLocked(w.open, "eof", time.Now())
	}
	if w.readiness == "loading" {
		w.preloadCancel()
		w.readiness = "failed"
		w.readinessError = "stopped_before_ready"
		for _, u := range w.units {
			if u.State == "queued" {
				u.State = "untranslated"
				u.Error = w.readinessError
				w.emitLocked(u)
			}
		}
	}
	// Reserve the last part of the same EOF budget for healthy sidecar writes.
	reserve := finishLimit / 10
	if finishLimit < time.Second {
		reserve = finishLimit / 4
	}
	w.finishTimer = time.AfterFunc(time.Until(w.deadline.Add(-reserve)), w.expire)
	signal(w.wake)
	return w.deadline
}

func (w *Worker) expire() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.expired {
		return
	}
	w.expired = true
	for _, u := range w.units {
		if u.State != "final" && u.State != "failed" && u.State != "untranslated" {
			u.State = "untranslated"
			u.Error = "shutdown_deadline"
			u.Japanese = ""
			w.emitLocked(u)
		}
	}
	// Cancel requests only after terminal changes have been queued. The sink
	// has an independent lifetime and can persist these changes until the
	// absolute EOF deadline, even though model generation is canceled.
	w.cancel()
	signal(w.sinkWake)
}

// Wait never waits past the source-EOF deadline for a blocked file or terminal.
func (w *Worker) Wait(deadline time.Time) Status {
	wait := time.Until(deadline)
	if wait < 0 {
		wait = 0
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-w.genDone:
	case <-timer.C:
		w.expire()
		return w.Status()
	}
	select {
	case <-w.sinkDone:
		w.mu.Lock()
		if w.finishTimer != nil {
			w.finishTimer.Stop()
		}
		w.mu.Unlock()
	case <-timer.C:
		w.expire()
	}
	return w.Status()
}

func (w *Worker) Status() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := Status{Readiness: w.readiness, ReadinessError: w.readinessError, StorageError: w.storageError, Units: len(w.units)}
	for _, u := range w.units {
		if u.State == "failed" {
			s.Failed = append(s.Failed, u.ID)
		}
		if u.State == "untranslated" {
			s.Untranslated = append(s.Untranslated, u.ID)
		}
		if u.State != "final" && u.State != "failed" && u.State != "untranslated" {
			s.Pending++
		}
		if w.persisted[u.ID] < u.Seq {
			s.Unsaved = append(s.Unsaved, u.ID)
		}
	}
	return s
}

func (w *Worker) ReadyDone() <-chan struct{} { return w.readyDone }

func speechText(line string) string {
	if strings.HasPrefix(line, "[") {
		if end := strings.Index(line, "] "); end >= 0 {
			return line[end+2:]
		}
	}
	return line
}

func (w *Worker) Close() {
	w.preloadCancel()
	w.cancel()
	w.mu.Lock()
	w.sinkClosed = true
	w.mu.Unlock()
	signal(w.sinkWake)
	if w.client != nil {
		w.client.close()
	}
}
func (s Status) String() string {
	return fmt.Sprintf("ready=%s units=%d pending=%d failed=%v untranslated=%v unsaved=%v storage=%s", s.Readiness, s.Units, s.Pending, s.Failed, s.Untranslated, s.Unsaved, s.StorageError)
}
