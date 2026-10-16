// Package whisper はローカルの whisper.cpp (whisper-server) を
// 外部プロセスとして起動し、HTTP 経由で推論を依頼する。
//
// 通信先は常に 127.0.0.1 で、クラウド API は一切呼ばない。
package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/konono/sokki/internal/proc"
)

// Client は whisper-server への推論クライアント。
type Client struct {
	baseURL  string
	language string
	prompt   string
	http     *http.Client

	cmd     *exec.Cmd
	stderr  *strings.Builder
	errMu   sync.Mutex
	owned   bool // 自前で起動したサーバーか
	stopCmd context.CancelFunc
}

// Options は Start の設定。
type Options struct {
	ServerBin string
	ModelPath string
	Language  string
	Threads   int
	Prompt    string
	// ServerURL が非空なら起動済みサーバーに接続する。
	ServerURL string
}

// Start は whisper-server を起動 (または既存サーバーに接続) して Client を返す。
func Start(ctx context.Context, opt Options) (*Client, error) {
	// 起動側でも URL を検証する (呼び出し側の検証漏れに備える)。
	if err := ValidateLocalURL(opt.ServerURL); err != nil {
		return nil, err
	}
	httpClient := newLocalHTTPClient()

	if opt.ServerURL != "" {
		c := &Client{
			baseURL:  strings.TrimRight(opt.ServerURL, "/"),
			language: opt.Language,
			prompt:   opt.Prompt,
			http:     httpClient,
		}
		if err := c.waitReady(ctx, 10*time.Second); err != nil {
			return nil, fmt.Errorf("指定された whisper-server (%s) に接続できません: %w", c.baseURL, err)
		}
		return c, nil
	}

	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("空きポートを確保できません: %w", err)
	}

	args := []string{
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"-m", opt.ModelPath,
		"-l", opt.Language,
		"-t", strconv.Itoa(opt.Threads),
		"-nt", // タイムスタンプ不要 (時刻は本ツール側で付ける)
	}
	// サーバープロセスの寿命は呼び出し側の ctx から切り離す。
	// Ctrl-C で即座に kill されると、終了時に残った音声を
	// 文字起こしできなくなるため (明示的な Close() で止める)。
	procCtx, stopCmd := context.WithCancel(context.Background())
	cmd := proc.Command(procCtx, opt.ServerBin, args...)
	errBuf := &strings.Builder{}
	c := &Client{
		baseURL:  fmt.Sprintf("http://127.0.0.1:%d", port),
		language: opt.Language,
		prompt:   opt.Prompt,
		http:     httpClient,
		cmd:      cmd,
		stderr:   errBuf,
		owned:    true,
		stopCmd:  stopCmd,
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = &syncWriter{mu: &c.errMu, b: errBuf}

	if err := cmd.Start(); err != nil {
		stopCmd()
		return nil, fmt.Errorf(
			"%s を起動できません: %w\n対処: brew install whisper-cpp を実行してください",
			opt.ServerBin, err)
	}
	// モデル読み込みに時間がかかるため余裕を持って待つ。
	if err := c.waitReady(ctx, 120*time.Second); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("whisper-server が起動しませんでした: %w\n%s", err, c.stderrSnapshot())
	}
	return c, nil
}

type syncWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (c *Client) stderrSnapshot() string {
	if c.stderr == nil {
		return ""
	}
	c.errMu.Lock()
	defer c.errMu.Unlock()
	s := strings.TrimSpace(c.stderr.String())
	if len(s) > 2000 {
		s = s[len(s)-2000:]
	}
	return s
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitReady はサーバーが推論を受け付けられるまで待つ。
func (c *Client) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	host := strings.TrimPrefix(c.baseURL, "http://")
	var lastErr error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 自前起動の場合、プロセスが落ちていたら即座に諦める。
		if c.owned && c.cmd.ProcessState != nil && c.cmd.ProcessState.Exited() {
			return fmt.Errorf("プロセスが終了しました")
		}
		conn, err := net.DialTimeout("tcp", host, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			// 実際に推論できるか 0.1 秒の無音で確認する。
			if _, err := c.Transcribe(ctx, make([]int16, 1600), 16000); err == nil {
				return nil
			} else {
				lastErr = err
			}
		} else {
			lastErr = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("タイムアウト")
	}
	return lastErr
}

type inferenceResponse struct {
	Text  string `json:"text"`
	Error string `json:"error"`
}

// Transcribe は PCM を文字起こしする。
func (c *Client) Transcribe(ctx context.Context, pcm []int16, sampleRate int) (string, error) {
	wav := EncodeWAV(pcm, sampleRate)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "chunk.wav")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wav); err != nil {
		return "", err
	}
	fields := map[string]string{
		"temperature":     "0.0",
		"temperature_inc": "0.2",
		"response_format": "json",
		"language":        c.language,
	}
	if c.prompt != "" {
		fields["prompt"] = c.prompt
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return "", err
		}
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/inference", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whisper-server が %d を返しました: %s",
			resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out inferenceResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		// response_format=json が効かずプレーンテキストで返る場合に備える。
		return strings.TrimSpace(string(raw)), nil
	}
	if out.Error != "" {
		return "", fmt.Errorf("whisper-server: %s", out.Error)
	}
	return strings.TrimSpace(out.Text), nil
}

// Close はサーバープロセスを終了させる (自前起動の場合のみ)。
func (c *Client) Close() error {
	if !c.owned {
		return nil
	}
	if c.stopCmd != nil {
		c.stopCmd()
	}
	// process group ごと確実に停止する。
	proc.Terminate(c.cmd)
	return nil
}
