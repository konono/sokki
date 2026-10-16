package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
)

// Handler は録音プロセス側の処理。
type Handler interface {
	Handle(Request) Response
}

// HandlerFunc は関数を Handler として使う。
type HandlerFunc func(Request) Response

// Handle は Handler を満たす。
func (f HandlerFunc) Handle(r Request) Response { return f(r) }

// Server は録音プロセス内で動く IPC サーバー。
type Server struct {
	path string
	ln   net.Listener
	h    Handler

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

// Listen は Unix domain socket を開いて待ち受けを始める。
func Listen(path string, h Handler) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// 前回の残骸があれば片付ける (死んだプロセスのソケット)。
	if _, err := os.Stat(path); err == nil {
		if c, derr := net.Dial("unix", path); derr == nil {
			c.Close()
			return nil, fmt.Errorf("すでに別の sokki がこのソケットを使っています: %s", path)
		}
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("IPC ソケットを開けません (%s): %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	s := &Server{path: path, ln: ln, h: h}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// Path はソケットのパスを返す。
func (s *Server) Path() string { return s.path }

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(conn)
		}()
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	// 1 接続で複数要求を扱う (AI が続けて読む場合に備える)。
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(conn)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(Response{OK: false, Error: "要求を解釈できません: " + err.Error()})
			continue
		}
		resp := s.h.Handle(req)
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

// Close は待ち受けを終了し、ソケットを削除する。
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	err := s.ln.Close()
	_ = os.Remove(s.path)
	s.wg.Wait()
	return err
}
