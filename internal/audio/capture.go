package audio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/konono/sokki/internal/proc"
)

// Capture は ffmpeg(avfoundation) を外部プロセスとして起動し、
// 16 kHz モノラル s16le の PCM を読み出す。
//
// Conference Output は 48 kHz ステレオ (Channels 1&2) で、
// ffmpeg の -ac 1 がその 2ch を 1ch に統合する。
type Capture struct {
	cmd         *exec.Cmd
	stdout      io.ReadCloser
	reader      *bufio.Reader
	stderr      *lockedBuffer
	device      Device
	readTimeout time.Duration
	closeMu     sync.Mutex
	closed      bool
}

// ErrStalled は ffmpeg が生きていても音声サンプルを返さなくなった状態。
var ErrStalled = errors.New("音声入力が止まりました")

const defaultReadTimeout = 10 * time.Second

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// StartCapture は指定デバイスからの取り込みを開始する。
func StartCapture(ctx context.Context, ffmpegBin string, dev Device, sampleRate int) (*Capture, error) {
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-f", "avfoundation",
		"-i", fmt.Sprintf(":%d", dev.Index),
		"-ac", "1", // Channels 1&2 をモノラルに統合
		"-ar", fmt.Sprintf("%d", sampleRate),
		"-f", "s16le",
		"-",
	}
	cmd := proc.Command(ctx, ffmpegBin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg の stdout を準備できません: %w", err)
	}
	errBuf := &lockedBuffer{}
	cmd.Stderr = errBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg を起動できません: %w", err)
	}
	return &Capture{
		cmd:    cmd,
		stdout: stdout,
		// 0.5 秒分のバッファを持たせ、推論中の取りこぼしを防ぐ。
		reader:      bufio.NewReaderSize(stdout, sampleRate),
		stderr:      errBuf,
		device:      dev,
		readTimeout: defaultReadTimeout,
	}, nil
}

// Read は samples 個のサンプルを読み出して返す。
// 返り値の長さは常に n か、エラー時は 0。
func (c *Capture) Read(n int) ([]int16, error) {
	// avfoundation はプロセスが生きたまま PCM を返さなくなることがある。
	// パイプの read deadline で検出し、呼び出し側でキャプチャを再起動する。
	if c.readTimeout > 0 {
		pipe, ok := c.stdout.(*os.File)
		if !ok {
			return nil, fmt.Errorf("ffmpeg の stdout に読み取り期限を設定できません")
		}
		if err := pipe.SetReadDeadline(time.Now().Add(c.readTimeout)); err != nil {
			return nil, fmt.Errorf("ffmpeg の stdout に読み取り期限を設定できません: %w", err)
		}
		defer pipe.SetReadDeadline(time.Time{})
	}
	raw := make([]byte, n*2)
	if _, err := io.ReadFull(c.reader, raw); err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, fmt.Errorf("%w: %v", ErrStalled, err)
		}
		return nil, c.wrapReadError(err)
	}
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	return out, nil
}

// wrapReadError は ffmpeg の stderr を添えて原因の分かるエラーにする。
func (c *Capture) wrapReadError(err error) error {
	stderr := strings.TrimSpace(c.stderr.String())
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		if stderr == "" {
			return fmt.Errorf("音声入力が終了しました (デバイス '%s' が切断された可能性があります)", c.device.Name)
		}
		return fmt.Errorf("%w\nffmpeg: %s\n%s", err, stderr, hintFor(stderr))
	}
	if stderr != "" {
		return fmt.Errorf("%w\nffmpeg: %s", err, stderr)
	}
	return err
}

// hintFor は ffmpeg のエラー文面から対処のヒントを組み立てる。
func hintFor(stderr string) string {
	low := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "permission"), strings.Contains(low, "not permitted"),
		strings.Contains(low, "not authorized"), strings.Contains(low, "input/output error"):
		return "対処: 実行元のターミナルアプリに「マイク」アクセス権限を与えてください\n" +
			"      システム設定 → プライバシーとセキュリティ → マイク"
	case strings.Contains(low, "device"), strings.Contains(low, "no such"):
		return "対処: 仮想デバイス (Loopback / BlackHole 2ch など) が有効か確認し、\n" +
			"      `sokki list` で正確な名前を確認してください"
	default:
		return "対処: `sokki list` でデバイス一覧と正確な名前を確認してください"
	}
}

// Close は ffmpeg プロセスを終了させる。
func (c *Capture) Close() error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	_ = c.stdout.Close()
	proc.Terminate(c.cmd)
	return nil
}
