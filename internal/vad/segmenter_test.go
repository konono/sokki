package vad

import (
	"math"
	"testing"
	"time"
)

const (
	sr = 16000
	fs = 320
)

func testOptions() Options {
	return Options{
		SampleRate:      sr,
		FrameSamples:    fs,
		SilenceHangover: 700 * time.Millisecond,
		MinSpeech:       400 * time.Millisecond,
		MaxSegment:      25 * time.Second,
		Preroll:         300 * time.Millisecond,
		FloorRatio:      3.0,
		AbsThreshold:    0.006,
		ShowInterim:     true,
		InterimAfter:    2500 * time.Millisecond,
		InterimInterval: 2000 * time.Millisecond,
	}
}

// tone は発話に相当する振幅のフレームを作る。
func tone(amp float64) []int16 {
	f := make([]int16, fs)
	for i := range f {
		f[i] = int16(amp * 32767 * math.Sin(float64(i)*0.3))
	}
	return f
}

// silence はほぼ無音のフレームを作る。
func silence() []int16 {
	return tone(0.0005)
}

// feed は同じフレームを n 回流し、発生したイベントを集める。
func feed(s *Segmenter, frame []int16, n int) []Event {
	var evs []Event
	for i := 0; i < n; i++ {
		evs = append(evs, s.Push(append([]int16(nil), frame...))...)
	}
	return evs
}

func finals(evs []Event) []Event {
	var out []Event
	for _, e := range evs {
		if e.Kind == Final {
			out = append(out, e)
		}
	}
	return out
}

func TestSilenceProducesNoSegment(t *testing.T) {
	s := New(testOptions())
	evs := feed(s, silence(), 250) // 5 秒
	if got := len(finals(evs)); got != 0 {
		t.Fatalf("無音から確定区間が出た: %d 件", got)
	}
}

func TestSpeechThenSilenceEmitsOneFinal(t *testing.T) {
	s := New(testOptions())
	var evs []Event
	evs = append(evs, feed(s, silence(), 50)...)  // 1 秒の無音でフロア学習
	evs = append(evs, feed(s, tone(0.2), 100)...) // 2 秒の発話
	evs = append(evs, feed(s, silence(), 60)...)  // 1.2 秒の無音 → 確定

	f := finals(evs)
	if len(f) != 1 {
		t.Fatalf("確定区間は 1 件のはずが %d 件", len(f))
	}
	// 2 秒の発話 + preroll 0.3 秒程度。
	dur := f[0].End - f[0].Start
	if dur < 1900*time.Millisecond || dur > 3600*time.Millisecond {
		t.Fatalf("区間長が想定外: %v", dur)
	}
	if f[0].Start < 500*time.Millisecond {
		t.Fatalf("開始時刻が早すぎる: %v", f[0].Start)
	}
	if len(f[0].Audio) == 0 {
		t.Fatal("音声が空")
	}
}

func TestShortBlipIsDiscarded(t *testing.T) {
	s := New(testOptions())
	feed(s, silence(), 50)
	evs := feed(s, tone(0.2), 8) // 160ms < MinSpeech
	evs = append(evs, feed(s, silence(), 60)...)

	// 発話実時間 160ms は MinSpeech(400ms) 未満なので破棄される
	// (preroll や後続無音は発話長に数えない)。
	if got := len(finals(evs)); got != 0 {
		t.Fatalf("短い雑音が確定区間になった: %d 件", got)
	}
}

func TestInterimEmittedDuringLongSpeech(t *testing.T) {
	s := New(testOptions())
	feed(s, silence(), 50)
	evs := feed(s, tone(0.2), 300) // 6 秒の連続発話

	var interim int
	for _, e := range evs {
		if e.Kind == Interim {
			interim++
		}
	}
	if interim < 2 {
		t.Fatalf("長い発話中の暫定結果が足りない: %d 件", interim)
	}
}

func TestMaxSegmentForcesCut(t *testing.T) {
	opt := testOptions()
	opt.MaxSegment = 2 * time.Second
	opt.ShowInterim = false
	s := New(opt)
	feed(s, silence(), 50)
	evs := feed(s, tone(0.2), 500) // 10 秒の連続発話

	f := finals(evs)
	if len(f) < 4 {
		t.Fatalf("最大長での強制分割が効いていない: %d 件", len(f))
	}
	for _, e := range f {
		if e.End-e.Start > 2500*time.Millisecond {
			t.Fatalf("区間が最大長を超えた: %v", e.End-e.Start)
		}
	}
}

func TestFlushEmitsPendingSpeech(t *testing.T) {
	s := New(testOptions())
	feed(s, silence(), 50)
	feed(s, tone(0.2), 100) // 発話中のまま終了

	ev, ok := s.Flush()
	if !ok {
		t.Fatal("終了時に残った発話が確定しなかった")
	}
	if ev.Kind != Final {
		t.Fatalf("Flush は Final を返すべき: %v", ev.Kind)
	}
	if _, ok := s.Flush(); ok {
		t.Fatal("2 回目の Flush で区間が出た")
	}
}

func TestNoiseFloorAdaptsToSteadyBackground(t *testing.T) {
	s := New(testOptions())
	// そこそこ大きい定常雑音を流してもフロアが追従し、確定区間を出さない。
	evs := feed(s, tone(0.01), 400)
	if got := len(finals(evs)); got > 1 {
		t.Fatalf("定常雑音で確定区間が連発した: %d 件", got)
	}
}
