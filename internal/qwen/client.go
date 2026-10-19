// Package qwen runs a local, explicitly configured Qwen3-ASR Python worker.
package qwen

import (
	"bufio"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/konono/sokki/internal/proc"
	"github.com/konono/sokki/internal/whisper"
)

//go:embed worker.py
var workerFS embed.FS

type Options struct {
	Python    string
	ModelPath string
}

type Client struct {
	mu        sync.Mutex
	terminate func()
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	stderr    *errorTail
	next      int
	closed    bool
}

type errorTail struct {
	mu   sync.Mutex
	text string
}

func (b *errorTail) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.text += string(p)
	if len(b.text) > 8192 {
		b.text = b.text[len(b.text)-8192:]
	}
	return len(p), nil
}
func (b *errorTail) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.text)
}

type response struct {
	Kind  string `json:"kind"`
	ID    int    `json:"id"`
	Text  string `json:"text"`
	Error string `json:"error"`
}

func Start(ctx context.Context, opt Options) (*Client, error) {
	source, err := workerFS.ReadFile("worker.py")
	if err != nil {
		return nil, err
	}
	cmd := proc.Command(context.Background(), opt.Python, "-u", "-c", string(source), opt.ModelPath)
	cmd.Env = append(os.Environ(), "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "HF_DATASETS_OFFLINE=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	tail := &errorTail{}
	cmd.Stderr = tail
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("Qwen Python を起動できません: %w", err)
	}
	c := &Client{terminate: func() { proc.Terminate(cmd) }, stdin: in, stdout: bufio.NewReader(out), stderr: tail, next: 1}
	readyCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	msg, err := c.read(readyCtx)
	if err != nil || msg.Kind != "ready" || msg.Error != "" {
		_ = c.Close()
		return nil, fmt.Errorf("Qwen worker の準備に失敗しました: %v %s %s", err, msg.Error, tail.String())
	}
	return c, nil
}

func (c *Client) read(ctx context.Context) (response, error) {
	type outcome struct {
		value response
		err   error
	}
	ch := make(chan outcome, 1)
	go func() {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			ch <- outcome{err: err}
			return
		}
		if len(line) > 1<<20 {
			ch <- outcome{err: errors.New("Qwen worker response too large")}
			return
		}
		var value response
		err = json.Unmarshal(line, &value)
		ch <- outcome{value, err}
	}()
	select {
	case result := <-ch:
		return result.value, result.err
	case <-ctx.Done():
		return response{}, ctx.Err()
	}
}

// A worker can announce ready and then stop reading stdin. Large WAV requests
// exceed pipe capacity, so the write must observe the same deadline as read.
func (c *Client) write(ctx context.Context, data []byte) error {
	result := make(chan error, 1)
	go func() {
		_, err := c.stdin.Write(data)
		result <- err
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		c.terminate()
		_ = c.stdin.Close()
		return ctx.Err()
	}
}

func (c *Client) Transcribe(ctx context.Context, pcm []int16, sampleRate int) (string, error) {
	if sampleRate != 16000 {
		return "", fmt.Errorf("Qwen は 16 kHz PCM のみ対応します: %d", sampleRate)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", errors.New("Qwen worker は停止済みです")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := requestCtx.Err(); err != nil {
		return "", err
	}
	request := struct {
		ID  int    `json:"id"`
		WAV string `json:"wav_base64"`
	}{
		ID: c.next, WAV: base64.StdEncoding.EncodeToString(whisper.EncodeWAV(pcm, sampleRate))}
	data, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	if err = c.write(requestCtx, append(data, '\n')); err != nil {
		c.closed = true
		if requestCtx.Err() == nil {
			c.terminate()
		}
		return "", fmt.Errorf("Qwen worker への送信に失敗: %w", err)
	}
	msg, err := c.read(requestCtx)
	if err != nil {
		c.closed = true
		c.terminate()
		return "", fmt.Errorf("Qwen worker からの応答に失敗: %w (%s)", err, c.stderr.String())
	}
	if msg.ID != c.next {
		c.closed = true
		c.terminate()
		return "", fmt.Errorf("Qwen worker の応答番号が一致しません")
	}
	c.next++
	if msg.Error != "" {
		return "", fmt.Errorf("Qwen 推論に失敗: %s", msg.Error)
	}
	return msg.Text, nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	_ = c.stdin.Close()
	c.terminate()
	return nil
}
