package summarizer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	ollamaStartupLimit    = 120 * time.Second
	ollamaRequestLimit    = 60 * time.Second
	ollamaKeepAlive       = "5m"
	ollamaRefreshInterval = 4 * time.Minute
	ollamaTokenCap        = 4096
	ollamaMaxResponse     = 4 << 20
)

// OllamaProvider uses an already-running loopback Ollama server. Construction
// does not perform I/O; preload and keep-alive refresh run in the background.
type OllamaProvider struct {
	endpoint string
	model    string
	client   *http.Client

	mu             sync.Mutex
	requestMu      sync.Mutex
	state          string // loading, ready, failed
	err            error
	closed         bool
	readyDone      chan struct{}
	readyOnce      sync.Once
	backgroundDone chan struct{}
	stopBackground context.CancelFunc
	stopPreload    context.CancelFunc
}

func NewOllamaProvider(model, rawURL string) (*OllamaProvider, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, fmt.Errorf("Ollama 要約には --summary-model または summary.model が必要です")
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("Ollama URL は http://127.0.0.1:PORT 形式で指定してください")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("Ollama URL は loopback IP のみ指定できます")
	}
	if u.Port() == "" {
		return nil, fmt.Errorf("Ollama URL にポートを指定してください")
	}
	ctx, cancel := context.WithCancel(context.Background())
	preloadCtx, cancelPreload := context.WithTimeout(ctx, ollamaStartupLimit)
	p := &OllamaProvider{
		endpoint: u.String() + "/api/chat", model: model,
		client: &http.Client{
			Timeout:       ollamaStartupLimit,
			Transport:     &http.Transport{Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}, state: "loading",
		readyDone: make(chan struct{}), backgroundDone: make(chan struct{}),
		stopBackground: cancel, stopPreload: cancelPreload,
	}
	go p.background(ctx, preloadCtx)
	return p, nil
}

func (p *OllamaProvider) Name() string { return "ollama" }

func (p *OllamaProvider) Readiness() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state, p.err
}

func (p *OllamaProvider) ReadyDone() <-chan struct{} { return p.readyDone }

func (p *OllamaProvider) finishPreload(err error) {
	p.mu.Lock()
	if p.state == "loading" {
		if err != nil {
			p.state, p.err = "failed", fmt.Errorf("Ollama 準備失敗: %w", err)
		} else {
			p.state = "ready"
		}
		p.readyOnce.Do(func() { close(p.readyDone) })
	}
	p.mu.Unlock()
}

func (p *OllamaProvider) background(ctx, preloadCtx context.Context) {
	defer close(p.backgroundDone)
	defer p.stopPreload()
	_, err := p.chat(preloadCtx, []map[string]string{}, false)
	p.finishPreload(err)
	if err != nil {
		return
	}
	ticker := time.NewTicker(ollamaRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshCtx, cancel := context.WithTimeout(ctx, ollamaRequestLimit)
			_, err := p.chat(refreshCtx, []map[string]string{}, false)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				p.mu.Lock()
				p.state, p.err = "failed", fmt.Errorf("Ollama 保持更新失敗: %w", err)
				p.mu.Unlock()
				return
			}
		}
	}
}

// StopBackground prevents a refresh from racing the final summary.
func (p *OllamaProvider) StopBackground() { p.stopBackground() }

// CancelPreloadIfLoading lets Stop finish promptly when startup is in flight.
// A canceled preload cannot be restarted by Final.
func (p *OllamaProvider) CancelPreloadIfLoading() {
	p.mu.Lock()
	if p.state == "loading" {
		p.state, p.err = "failed", fmt.Errorf("Ollama 準備中に録音が終了したため要約を実施しません")
		p.readyOnce.Do(func() { close(p.readyDone) })
		p.stopPreload()
	}
	p.mu.Unlock()
}

// Close stops background refresh and closes idle connections. The application
// calls it after Stop and the synchronous Final, so no summary request remains.
// It does not send keep_alive:0, which could evict a model used by another client.
func (p *OllamaProvider) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.stopBackground()
	<-p.backgroundDone
	p.client.CloseIdleConnections()
}

func (p *OllamaProvider) Summarize(ctx context.Context, in Input) (Output, error) {
	p.mu.Lock()
	closed, state, err := p.closed, p.state, p.err
	p.mu.Unlock()
	if closed {
		return Output{}, fmt.Errorf("Ollama provider は終了しています")
	}
	if state != "ready" {
		if err != nil {
			return Output{}, err
		}
		return Output{}, fmt.Errorf("Ollama モデルを準備中です")
	}
	requestCtx, cancel := context.WithTimeout(ctx, ollamaRequestLimit)
	defer cancel()
	msg := []map[string]string{{"role": "user", "content": BuildPrompt(in)}}
	r, err := p.chat(requestCtx, msg, true)
	if err != nil {
		return Output{}, err
	}
	out, err := ParseOutput([]byte(r.Message.Content))
	if err != nil {
		return Output{}, err
	}
	if strings.TrimSpace(out.Topic) == "" || strings.TrimSpace(out.Summary) == "" {
		return Output{}, fmt.Errorf("Ollama 要約の topic または summary が空です")
	}
	return out, nil
}

type ollamaReply struct {
	Model      string `json:"model"`
	Error      string `json:"error"`
	Done       *bool  `json:"done"`
	DoneReason string `json:"done_reason"`
	Message    struct {
		Content string `json:"content"`
	} `json:"message"`
	EvalCount int `json:"eval_count"`
}

func (p *OllamaProvider) chat(ctx context.Context, messages []map[string]string, summary bool) (ollamaReply, error) {
	// A refresh never competes with a summary on the same client.
	p.requestMu.Lock()
	defer p.requestMu.Unlock()
	if err := ctx.Err(); err != nil {
		return ollamaReply{}, err
	}
	body := map[string]any{
		"model": p.model, "messages": messages, "stream": false, "think": false,
		"keep_alive": ollamaKeepAlive,
		"options":    map[string]any{"temperature": 0, "seed": 42, "num_ctx": 16384, "num_predict": ollamaTokenCap},
	}
	if summary {
		body["format"] = map[string]any{
			"type": "object", "properties": map[string]any{
				"topic": map[string]string{"type": "string"}, "summary": map[string]string{"type": "string"},
			}, "required": []string{"topic", "summary"}, "additionalProperties": false,
		}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return ollamaReply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(data))
	if err != nil {
		return ollamaReply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return ollamaReply{}, fmt.Errorf("Ollama HTTP: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, ollamaMaxResponse+1))
	if err != nil {
		return ollamaReply{}, fmt.Errorf("Ollama 応答読取: %w", err)
	}
	if len(raw) > ollamaMaxResponse {
		return ollamaReply{}, fmt.Errorf("Ollama 応答が大きすぎます")
	}
	if resp.StatusCode != http.StatusOK {
		return ollamaReply{}, fmt.Errorf("Ollama HTTP %d: %s", resp.StatusCode, preview(raw))
	}
	var out ollamaReply
	if err := json.Unmarshal(raw, &out); err != nil {
		return ollamaReply{}, fmt.Errorf("Ollama 応答JSON: %w", err)
	}
	if out.Error != "" {
		return ollamaReply{}, fmt.Errorf("Ollama 応答エラー: %s", preview([]byte(out.Error)))
	}
	if out.Model != p.model {
		return ollamaReply{}, fmt.Errorf("Ollama 応答モデル %q (期待 %q)", out.Model, p.model)
	}
	if out.Done == nil || !*out.Done {
		return ollamaReply{}, fmt.Errorf("Ollama 応答が完了していません")
	}
	want := "load"
	if summary {
		want = "stop"
	}
	if out.DoneReason != want {
		return ollamaReply{}, fmt.Errorf("Ollama 完了理由 %q (期待 %q)", out.DoneReason, want)
	}
	if summary && out.EvalCount >= ollamaTokenCap {
		return ollamaReply{}, fmt.Errorf("Ollama 出力が %d token 上限に達しました", ollamaTokenCap)
	}
	return out, nil
}
