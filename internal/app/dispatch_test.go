package app

import (
	"context"
	"testing"
	"time"

	"github.com/konono/sokki/internal/vad"
)

// REVIEW.md [P1] の回帰テスト。
// キューが満杯 + Ctrl-C 済みでも、確定区間は捨てられず保持されること。
//
// 修正前は captureLoop が `select { case events <- ev: case <-ctx.Done(): }`
// で送っていたため、満杯時は必ずキャンセル側が選ばれて確定区間を失っていた。
func TestSendFinalSurvivesCancelledContextWithFullQueue(t *testing.T) {
	// Ctrl-C 済みを再現する (dispatcher はこれを見てはいけない)。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// 容量 1 のキューを満杯にしておく。
	disp := newDispatcher(1, 2*time.Second)
	disp.events <- vad.Event{Kind: vad.Final, Start: 0}

	// 少し遅れて消費する = 送信側は一時的にブロックされる。
	consumed := make(chan vad.Event, 2)
	go func() {
		time.Sleep(100 * time.Millisecond)
		consumed <- <-disp.events // 1 件目を取り出し空きを作る
	}()

	ev := vad.Event{Kind: vad.Final, Start: 5 * time.Second}
	if ok := disp.SendFinal(ev); !ok {
		t.Fatal("満杯 + キャンセル済みで確定区間を捨てた")
	}
	if disp.Dropped() != 0 {
		t.Fatalf("落とした件数 = %d, want 0", disp.Dropped())
	}
	// ctx がキャンセル済みでも送信が成立したことを明示する。
	if ctx.Err() == nil {
		t.Fatal("テスト前提が崩れている (ctx がキャンセルされていない)")
	}

	<-consumed
	got := <-disp.events
	if got.Start != 5*time.Second {
		t.Fatalf("届いた区間 = %v, want 5s", got.Start)
	}
}

// 期限内に渡せなければ false を返し、件数を記録すること。
func TestSendFinalReportsDropOnTimeout(t *testing.T) {
	disp := newDispatcher(1, 50*time.Millisecond)
	disp.events <- vad.Event{Kind: vad.Final} // 消費者なしで満杯

	if ok := disp.SendFinal(vad.Event{Kind: vad.Final, Start: time.Second}); ok {
		t.Fatal("消費者が居ないのに送信成功を返した")
	}
	if disp.Dropped() != 1 {
		t.Fatalf("落とした件数 = %d, want 1", disp.Dropped())
	}
}

// 暫定区間は滞留時に捨てられ、取り込みを止めないこと。
func TestSendInterimDropsWhenFull(t *testing.T) {
	disp := newDispatcher(1, time.Second)
	disp.events <- vad.Event{Kind: vad.Final}

	start := time.Now()
	if ok := disp.SendInterim(vad.Event{Kind: vad.Interim}); ok {
		t.Fatal("満杯なのに暫定を受け付けた")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("暫定送信がブロックした: %v", elapsed)
	}
	if disp.Dropped() != 0 {
		t.Fatalf("暫定の破棄が確定の欠落として数えられている: %d", disp.Dropped())
	}
}
