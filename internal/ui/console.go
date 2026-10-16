// Package ui はターミナル表示を扱う。
//
// 暫定結果は 1 行を上書きし続け、確定結果はその上に積む。
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

const (
	dim   = "\x1b[2m"
	reset = "\x1b[0m"
	clear = "\r\x1b[2K"
)

// Sink は Console の出力を引き取る先 (分割表示など)。
type Sink interface {
	Log(line string)
	Interim(line string)
	ClearInterim()
}

// Console は暫定行と確定行の出力を調停する。
type Console struct {
	mu         sync.Mutex
	out        io.Writer
	tty        bool
	color      bool
	interimLen int
	// sink が設定されている間、出力は全てそちらへ渡す。
	sink Sink
}

// Redirect は出力先を差し替える (nil で元に戻す)。
//
// 分割表示中に Console が直接書くと画面全体がスクロールし、
// 差分描画では崩れた位置を戻せない。表示を持っている側へ
// 全ての出力を渡し、画面の管理を 1 箇所に集約する。
func (c *Console) Redirect(s Sink) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s != nil {
		c.clearLine()
	}
	c.sink = s
}

// New は標準出力向けの Console を返す。
func New() *Console {
	fi, err := os.Stdout.Stat()
	tty := err == nil && (fi.Mode()&os.ModeCharDevice) != 0
	return &Console{
		out:   os.Stdout,
		tty:   tty,
		color: tty && os.Getenv("NO_COLOR") == "",
	}
}

// NewWith は出力先と TTY 判定を指定して Console を作る (テスト・埋め込み用)。
func NewWith(out io.Writer, tty bool) *Console {
	return &Console{out: out, tty: tty, color: tty && os.Getenv("NO_COLOR") == ""}
}

// IsTTY は出力先が端末かを返す。
func (c *Console) IsTTY() bool { return c.tty }

// Color は色表示を使うかを返す。
func (c *Console) Color() bool { return c.color }

// Interim は暫定結果を上書き表示する。ファイルには書かれない。
//
// 端末でない場合 (ログへのリダイレクト等) は上書きが成立せず
// 出力が読めなくなるため、暫定結果を出さない。
func (c *Console) Interim(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sink != nil {
		c.sink.Interim(text)
		return
	}
	if text == "" || !c.tty {
		return
	}
	c.clearLine()
	// 1 行に収める。折り返すと画面が流れて確定行が読めなくなるため、
	// 直近の発話が見えるよう先頭側を切り詰める。
	line := "… " + TruncateTail(text, c.width()-3)
	if c.color {
		fmt.Fprint(c.out, dim+line+reset)
	} else {
		fmt.Fprint(c.out, line)
	}
	c.interimLen = DisplayWidth(line)
}

// width は折り返しを避けるための表示可能桁数を返す。
func (c *Console) width() int {
	w := TermWidth()
	if w < 20 {
		w = 20
	}
	return w
}

// Final は確定行を出力する。
func (c *Console) Final(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sink != nil {
		c.sink.Log(line)
		return
	}
	c.clearLine()
	fmt.Fprintln(c.out, line)
}

// Info は補助メッセージを出力する。
func (c *Console) Info(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sink != nil {
		c.sink.Log(fmt.Sprintf(format, args...))
		return
	}
	c.clearLine()
	fmt.Fprintf(c.out, format+"\n", args...)
}

// Warn は警告を標準エラーへ出力する。
func (c *Console) Warn(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sink != nil {
		c.sink.Log("警告: " + fmt.Sprintf(format, args...))
		return
	}
	c.clearLine()
	fmt.Fprintf(os.Stderr, "警告: "+format+"\n", args...)
}

// ClearInterim は暫定行を消す。
func (c *Console) ClearInterim() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sink != nil {
		c.sink.ClearInterim()
		return
	}
	c.clearLine()
}

func (c *Console) clearLine() {
	if c.interimLen == 0 {
		return
	}
	if c.color {
		fmt.Fprint(c.out, clear)
	} else {
		fmt.Fprint(c.out, "\r"+strings.Repeat(" ", c.interimLen)+"\r")
	}
	c.interimLen = 0
}
