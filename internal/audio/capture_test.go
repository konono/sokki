package audio

import (
	"bufio"
	"errors"
	"os"
	"testing"
	"time"
)

func TestCaptureReadDetectsStalledPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	c := &Capture{
		stdout: r, reader: bufio.NewReader(r),
		stderr: &lockedBuffer{}, device: Device{Name: "test"},
		readTimeout: 30 * time.Millisecond,
	}
	start := time.Now()
	_, err = c.Read(320)
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("停止した入力のエラー = %v, want ErrStalled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("停止検知が遅い: %v", elapsed)
	}
}
