package app

import (
	"sync/atomic"
	"time"

	"github.com/konono/sokki/internal/vad"
)

// dispatcher は検出イベントを推論ワーカーへ渡す経路。
//
// 確定区間 (Final) は Ctrl-C の context では中断しない。
// 取り込み停止と確定区間のドレインを分離するため、送信の打ち切りは
// 期限 (timeout) だけで決める。暫定区間 (Interim) は捨ててよい。
type dispatcher struct {
	events  chan vad.Event
	timeout time.Duration
	dropped atomic.Int64
}

func newDispatcher(buffer int, timeout time.Duration) *dispatcher {
	return &dispatcher{
		events:  make(chan vad.Event, buffer),
		timeout: timeout,
	}
}

// SendFinal は確定区間を必ず届けようとする。
// 期限内に渡せなかった場合のみ false を返し、落とした件数を記録する。
//
// 割り込み (Ctrl-C) の context を見ないのが要点。見てしまうと、
// キューが満杯のときに必ずキャンセル側が選ばれて確定区間を捨てる。
func (d *dispatcher) SendFinal(ev vad.Event) bool {
	timer := time.NewTimer(d.timeout)
	defer timer.Stop()
	select {
	case d.events <- ev:
		return true
	case <-timer.C:
		d.dropped.Add(1)
		return false
	}
}

// SendInterim は暫定区間を渡す。滞留していれば捨てる。
func (d *dispatcher) SendInterim(ev vad.Event) bool {
	select {
	case d.events <- ev:
		return true
	default:
		return false
	}
}

// Dropped は期限切れで捨てた確定区間の件数を返す。
func (d *dispatcher) Dropped() int { return int(d.dropped.Load()) }

// Close はイベント送信を終了する。
func (d *dispatcher) Close() { close(d.events) }
