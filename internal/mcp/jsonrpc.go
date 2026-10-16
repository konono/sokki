// Package mcp は AI クライアントが起動するローカル stdio MCP サーバー。
//
// 録音プロセスは専用 pane で独立して動き、この接続が終了しても停止しない。
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// JSON-RPC 2.0 の最小実装 (MCP の stdio トランスポート)。

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// conn は 1 本の stdio 接続。
type conn struct {
	in  *bufio.Reader
	out io.Writer
	mu  sync.Mutex
}

func newConn(in io.Reader, out io.Writer) *conn {
	return &conn{in: bufio.NewReaderSize(in, 1<<20), out: out}
}

// read は 1 行 1 メッセージ (改行区切り JSON) を読む。
func (c *conn) read() (*request, error) {
	for {
		line, err := c.in.ReadBytes('\n')
		if err != nil {
			if len(line) == 0 {
				return nil, err
			}
		}
		trimmed := trimSpace(line)
		if len(trimmed) == 0 {
			if err != nil {
				return nil, err
			}
			continue
		}
		var req request
		if jerr := json.Unmarshal(trimmed, &req); jerr != nil {
			// 壊れた行は読み飛ばす (接続は維持する)。
			if err != nil {
				return nil, err
			}
			continue
		}
		return &req, nil
	}
}

func trimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\t' || b[start] == '\r' || b[start] == '\n') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\r' || b[end-1] == '\n') {
		end--
	}
	return b[start:end]
}

func (c *conn) write(resp response) error {
	resp.JSONRPC = "2.0"
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.out.Write(append(data, '\n')); err != nil {
		return err
	}
	if f, ok := c.out.(interface{ Sync() error }); ok {
		_ = f.Sync()
	}
	return nil
}

func (c *conn) replyResult(id json.RawMessage, result any) error {
	if len(id) == 0 {
		return nil // 通知には応答しない
	}
	return c.write(response{ID: id, Result: result})
}

func (c *conn) replyError(id json.RawMessage, code int, msg string) error {
	if len(id) == 0 {
		return nil
	}
	return c.write(response{ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%v", err)
}
