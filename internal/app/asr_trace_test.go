package app

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/konono/sokki/internal/audio"
	"github.com/konono/sokki/internal/config"
	"github.com/konono/sokki/internal/vad"
)

// The trace must retain enough information to replay text reconciliation
// and the exact PCM passed to the final recognizer.
func TestASRTraceReplayInputs(t *testing.T) {
	dir := t.TempDir()
	audio := []int16{1, -2, 32767, -32768}
	whole, chunks := "帰ってきて発信", "帰ってきて ノートで発信"
	obs := finalObservation{Whole: whole, ReconcileWhole: whole, ReconcileChunks: chunks,
		RangeCount: 2, Result: reconcileTranscripts(whole, chunks)}
	if err := traceASRFinal(dir, vad.Event{Kind: vad.Final, Audio: audio}, obs, nil, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "asr.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var rec asrTraceRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &rec); err != nil {
		t.Fatal(err)
	}
	if got := reconcileTranscripts(rec.ReconcileWhole, rec.ReconcileChunks); got != rec.Result {
		t.Fatalf("trace cannot replay reconciliation: got %q, want %q", got, rec.Result)
	}
	raw, err := os.ReadFile(filepath.Join(dir, rec.PCMFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != len(audio)*2 {
		t.Fatalf("PCM bytes=%d", len(raw))
	}
	for i, want := range audio {
		if got := int16(binary.LittleEndian.Uint16(raw[i*2:])); got != want {
			t.Fatalf("sample %d=%d, want %d", i, got, want)
		}
	}
}

func TestASRTraceMarksRecoveryBoundary(t *testing.T) {
	dir := t.TempDir()
	trace, err := startRawASRTrace(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := &tracedCaptureReader{captureReader: &recoveryReader{frames: [][]int16{make([]int16, config.FrameSamples)}, endErr: audio.ErrStalled}, trace: trace}
	second := &tracedCaptureReader{captureReader: &recoveryReader{endErr: os.ErrClosed}, trace: trace}
	disp := newDispatcher(2, time.Second)
	_, _ = captureWithRecovery(context.Background(), first, func() (captureReader, error) { return second, nil }, newSegmenter(false), disp, nil)
	disp.Close()
	if err := trace.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "asr.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var captureFile string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec asrTraceRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Kind == "capture" {
			captureFile = rec.PCMFile
		}
		if rec.Kind == "recovery" {
			if rec.SampleOffset != config.FrameSamples {
				t.Fatalf("recovery at sample %d, want %d", rec.SampleOffset, config.FrameSamples)
			}
			raw, err := os.ReadFile(filepath.Join(dir, captureFile))
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) != 2*config.FrameSamples {
				t.Fatalf("capture bytes=%d", len(raw))
			}
			return
		}
	}
	t.Fatal("recovery marker missing")
}
