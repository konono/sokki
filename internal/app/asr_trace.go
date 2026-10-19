package app

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/konono/sokki/internal/vad"
)

// SOKKI_ASR_TRACE_DIR enables an opt-in diagnostic log. It stores each final
// audio segment as 16 kHz mono s16le PCM and records the raw recognition passes
// and displayed interim hypotheses in JSONL. Meeting audio is never traced by
// default.
var asrTraceMu sync.Mutex
var asrTraceSeq atomic.Uint64
var asrTraceWarned atomic.Bool
var asrTraceRunID = time.Now().UnixNano()
var asrTraceRawBytes atomic.Int64
var asrTraceFinalBytes atomic.Int64

// Each PCM stream has its own approximate budget, so continuous capture
// cannot consume the final-segment budget. Failed writes may overcount it.
const maxASRTracePCMBytes = 512 << 20

func reserveASRTraceBytes(counter *atomic.Int64, n int) bool {
	for {
		used := counter.Load()
		if used+int64(n) > maxASRTracePCMBytes {
			return false
		}
		if counter.CompareAndSwap(used, used+int64(n)) {
			return true
		}
	}
}

type rawASRTrace struct {
	dir           string
	f             *os.File
	buf           *bufio.Writer
	queue         chan []int16
	done          chan struct{}
	samples       int64 // capture goroutine only; includes frames lost by tracing
	droppedFrames int
	recoveries    int
	err           error // writer goroutine; read after done closes
	limited       bool
	closed        bool
	onIssue       func(error)
}

type tracedCaptureReader struct {
	captureReader
	trace    *rawASRTrace
	lastRead time.Time
}

func (r *tracedCaptureReader) Read(n int) ([]int16, error) {
	frame, err := r.captureReader.Read(n)
	if err == nil {
		r.lastRead = time.Now()
		r.trace.WriteFrame(frame)
	}
	return frame, err
}

func startRawASRTrace(dir string) (*rawASRTrace, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("capture-%d.pcm", asrTraceRunID)
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := appendASRTrace(dir, asrTraceRecord{Kind: "capture", PCMFile: name}, nil); err != nil {
		_ = f.Close()
		return nil, err
	}
	t := &rawASRTrace{
		dir: dir, f: f, buf: bufio.NewWriterSize(f, 64*1024),
		queue: make(chan []int16, 256), done: make(chan struct{}),
	}
	go t.writeLoop()
	return t, nil
}

func (t *rawASRTrace) WriteFrame(frame []int16) {
	if t == nil || t.closed {
		return
	}
	t.samples += int64(len(frame))
	if t.limited {
		return
	}
	if !reserveASRTraceBytes(&asrTraceRawBytes, 2*len(frame)) {
		t.limited = true
		t.report(fmt.Errorf("ASR diagnostic raw PCM storage limit reached"))
		return
	}
	copyFrame := append([]int16(nil), frame...)
	select {
	case t.queue <- copyFrame:
	default:
		t.droppedFrames++
		t.limited = true // never present a partial capture as replayable
		t.report(fmt.Errorf("ASR diagnostic capture queue filled"))
	}

}

func (t *rawASRTrace) report(err error) {
	if t.onIssue != nil {
		t.onIssue(err)
	}
}

func (t *rawASRTrace) writeLoop() {
	defer close(t.done)
	frames := 0
	for frame := range t.queue {
		if t.err != nil {
			continue
		}
		raw := make([]byte, 2*len(frame))
		for i, sample := range frame {
			binary.LittleEndian.PutUint16(raw[2*i:], uint16(sample))
		}
		_, t.err = t.buf.Write(raw)
		frames++
		if t.err == nil && frames%50 == 0 {
			t.err = t.buf.Flush()
		}
	}
	if t.err == nil {
		t.err = t.buf.Flush()
	}
}

func (t *rawASRTrace) Close() error {
	if t == nil || t.closed {
		return nil
	}
	t.closed = true
	close(t.queue)
	<-t.done
	closeErr := t.f.Close()
	rec := asrTraceRecord{Kind: "capture_end", AudioSamps: int(t.samples), TraceDroppedFrames: t.droppedFrames, Recoveries: t.recoveries}
	if t.limited {
		rec.TraceError = "raw PCM capture incomplete"
	}
	if t.err != nil {
		rec.TraceError = t.err.Error()
	}
	traceErr := appendASRTrace(t.dir, rec, nil)
	if t.err != nil {
		return t.err
	}
	if closeErr != nil {
		return closeErr
	}
	return traceErr
}

type asrTraceRecord struct {
	Kind               string     `json:"kind"`
	RunID              int64      `json:"run_id"`
	Seq                uint64     `json:"seq"`
	StartMS            int64      `json:"start_ms"`
	EndMS              int64      `json:"end_ms"`
	PCMFile            string     `json:"pcm_file,omitempty"`
	Interim            string     `json:"interim,omitempty"`
	Whole              string     `json:"whole,omitempty"`
	Parts              []string   `json:"parts,omitempty"`
	RetryParts         []string   `json:"retry_parts,omitempty"`
	ReconcileWhole     string     `json:"reconcile_whole,omitempty"`
	ReconcileChunks    string     `json:"reconcile_chunks,omitempty"`
	RangeCount         int        `json:"range_count,omitempty"`
	PartSpans          []partSpan `json:"part_spans,omitempty"`
	ReconcileMode      string     `json:"reconcile_mode,omitempty"`
	Dropped            *int       `json:"dropped,omitempty"`
	Result             string     `json:"result,omitempty"`
	Error              string     `json:"error,omitempty"`
	TraceError         string     `json:"trace_error,omitempty"`
	LatencyMS          int64      `json:"latency_ms,omitempty"`
	AudioSamps         int        `json:"audio_samples,omitempty"`
	SpeechSamples      int64      `json:"speech_samples,omitempty"`
	SampleOffset       int64      `json:"sample_offset,omitempty"`
	GapMS              int64      `json:"gap_ms,omitempty"`
	TraceDroppedFrames int        `json:"trace_dropped_frames,omitempty"`
	Recoveries         int        `json:"recoveries,omitempty"`
}

func asrTraceDir() string { return os.Getenv("SOKKI_ASR_TRACE_DIR") }

func traceASRInterim(dir string, ev vad.Event, text string, latency time.Duration) error {
	return appendASRTrace(dir, asrTraceRecord{
		Kind: "interim", StartMS: ev.Start.Milliseconds(), EndMS: ev.End.Milliseconds(),
		Interim: text, LatencyMS: latency.Milliseconds(), AudioSamps: len(ev.Audio),
	}, nil)
}

func traceASRFinal(dir string, ev vad.Event, obs finalObservation, inferenceErr error,
	latency time.Duration) error {
	rec := asrTraceRecord{
		Kind: "final", StartMS: ev.Start.Milliseconds(), EndMS: ev.End.Milliseconds(),
		Whole: obs.Whole, Parts: obs.Parts, RetryParts: obs.RetryParts, Result: obs.Result,
		ReconcileWhole: obs.ReconcileWhole, ReconcileChunks: obs.ReconcileChunks,
		RangeCount:    obs.RangeCount,
		PartSpans:     obs.PartSpans,
		ReconcileMode: obs.ReconcileMode,
		LatencyMS:     latency.Milliseconds(), AudioSamps: len(ev.Audio),
	}
	if inferenceErr != nil {
		rec.Error = inferenceErr.Error()
	}
	return appendASRTrace(dir, rec, ev.Audio)
}

func traceASRStats(dir string, dropped int) error {
	return appendASRTrace(dir, asrTraceRecord{Kind: "stats", Dropped: &dropped}, nil)
}

// A replay must flush the segmenter at this sample offset before feeding
// samples from the reconnected capture. The wall gap is for diagnosis only.
func traceASRRecovery(dir string, sampleOffset int64, gap time.Duration) error {
	return appendASRTrace(dir, asrTraceRecord{
		Kind: "recovery", SampleOffset: sampleOffset, GapMS: gap.Milliseconds(),
	}, nil)
}

func appendASRTrace(dir string, rec asrTraceRecord, audio []int16) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	rec.RunID = asrTraceRunID
	rec.Seq = asrTraceSeq.Add(1)
	var limitErr error
	if audio != nil {
		if !reserveASRTraceBytes(&asrTraceFinalBytes, 2*len(audio)) {
			rec.TraceError = "diagnostic PCM storage limit reached"
			limitErr = fmt.Errorf("ASR diagnostic final PCM storage limit reached")
		} else {
			rec.PCMFile = fmt.Sprintf("final-%d-%06d.pcm", asrTraceRunID, rec.Seq)
			raw := make([]byte, 2*len(audio))
			for i, sample := range audio {
				binary.LittleEndian.PutUint16(raw[2*i:], uint16(sample))
			}
			pcmFile, err := os.OpenFile(filepath.Join(dir, rec.PCMFile),
				os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			if _, err := pcmFile.Write(raw); err != nil {
				_ = pcmFile.Close()
				return err
			}
			if err := pcmFile.Close(); err != nil {
				return err
			}
		}
	}
	asrTraceMu.Lock()
	defer asrTraceMu.Unlock()
	f, err := os.OpenFile(filepath.Join(dir, "asr.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(rec); err != nil {
		return err
	}
	return limitErr
}
