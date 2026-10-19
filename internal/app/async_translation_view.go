package app

import (
	"sync"

	"github.com/konono/sokki/internal/ui"
)

// asyncTranslationView is the sole terminal writer while translation is enabled.
// Producers only append ordered display operations; a stalled terminal cannot
// hold the source, translation, or shutdown locks. The queue is owned by one
// goroutine, and is drained after the terminal resumes.
type asyncTranslationView struct {
	mu      sync.Mutex
	pending []func()
	wake    chan struct{}
	closed  bool
	done    chan struct{}
}

func newAsyncTranslationView() *asyncTranslationView {
	a := &asyncTranslationView{wake: make(chan struct{}, 1), done: make(chan struct{})}
	go a.run()
	return a
}

func (a *asyncTranslationView) enqueue(fn func()) {
	a.mu.Lock()
	if !a.closed {
		a.pending = append(a.pending, fn)
		select {
		case a.wake <- struct{}{}:
		default:
		}
	}
	a.mu.Unlock()
}

func (a *asyncTranslationView) closeAfter(fn func()) {
	a.mu.Lock()
	if !a.closed {
		a.pending = append(a.pending, fn)
		a.closed = true
		select {
		case a.wake <- struct{}{}:
		default:
		}
	}
	a.mu.Unlock()
}

func (a *asyncTranslationView) run() {
	defer close(a.done)
	for range a.wake {
		for {
			a.mu.Lock()
			if len(a.pending) == 0 {
				end := a.closed
				a.mu.Unlock()
				if end {
					return
				}
				break
			}
			fn := a.pending[0]
			a.pending[0] = nil
			a.pending = a.pending[1:]
			a.mu.Unlock()
			fn()
		}
	}
}

// Console.Redirect accepts a Sink. Its calls must use the same display queue.
type asyncSplitSink struct {
	a     *asyncTranslationView
	split *ui.Split
}

func (s asyncSplitSink) Log(line string)     { s.a.enqueue(func() { s.split.Log(line) }) }
func (s asyncSplitSink) Interim(line string) { s.a.enqueue(func() { s.split.Interim(line) }) }
func (s asyncSplitSink) ClearInterim()       { s.a.enqueue(s.split.ClearInterim) }

// Non-TTY status messages from app.Run also pass through this boundary.
// plain is an unredirected Console and is used only by the display goroutine.
type asyncConsoleSink struct {
	a     *asyncTranslationView
	plain *ui.Console
}

func (s asyncConsoleSink) Log(line string)     { s.a.enqueue(func() { s.plain.Final(line) }) }
func (s asyncConsoleSink) Interim(line string) { s.a.enqueue(func() { s.plain.Interim(line) }) }
func (s asyncConsoleSink) ClearInterim()       { s.a.enqueue(s.plain.ClearInterim) }
