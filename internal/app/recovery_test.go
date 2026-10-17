package app

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/konono/sokki/internal/audio"
	"github.com/konono/sokki/internal/vad"
)

type recoveryReader struct {
	frames [][]int16
	n      int
	endErr error
	closed bool
}

func (r *recoveryReader) Read(int) ([]int16, error) {
	if r.n >= len(r.frames) {
		return nil, r.endErr
	}
	f := r.frames[r.n]
	r.n++
	return f, nil
}

func (r *recoveryReader) Close() error { r.closed = true; return nil }

func TestCaptureRecoveryFlushesSpeechAndContinues(t *testing.T) {
	first := &recoveryReader{frames: buildFrames(20, 60, 0), endErr: audio.ErrStalled}
	second := &recoveryReader{frames: buildFrames(20, 60, 40), endErr: io.EOF}
	seg := newSegmenter(false)
	disp := newDispatcher(8, time.Second)
	opened, warned := 0, 0
	last, err := captureWithRecovery(context.Background(), first,
		func() (captureReader, error) { opened++; return second, nil },
		seg, disp, func() { warned++ })
	if !errors.Is(err, io.EOF) {
		t.Fatalf("2 回目の入力の終了エラー = %v, want EOF", err)
	}
	if last != second || !first.closed || opened != 1 || warned != 1 {
		t.Fatalf("再接続の状態: last=%T first.closed=%v opened=%d warned=%d",
			last, first.closed, opened, warned)
	}
	if err := last.Close(); err != nil {
		t.Fatal(err)
	}
	disp.Close()
	var finals []vad.Event
	for ev := range disp.events {
		if ev.Kind == vad.Final {
			finals = append(finals, ev)
		}
	}
	if len(finals) != 2 {
		t.Fatalf("停止前の発言と再接続後の発言 = %d 件, want 2", len(finals))
	}
	if finals[1].Start <= finals[0].Start {
		t.Fatalf("再接続後の時刻が戻った: %v → %v", finals[0].Start, finals[1].Start)
	}
}
