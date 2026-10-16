package ui

import (
	"bytes"
	"strings"
	"testing"
)

func newTestConsole(tty bool) (*Console, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return &Console{out: buf, tty: tty, color: false}, buf
}

func TestInterimSuppressedWhenNotTTY(t *testing.T) {
	// リダイレクト先では \r の上書きが効かず読めない出力になるため、
	// 暫定結果を出さない (実機確認で判明した不具合の回帰テスト)。
	c, buf := newTestConsole(false)
	c.Interim("[00:00:01] 暫定テキスト")
	if buf.Len() != 0 {
		t.Fatalf("非 TTY で暫定結果が出力された: %q", buf.String())
	}
}

func TestInterimPrintedWhenTTY(t *testing.T) {
	c, buf := newTestConsole(true)
	c.Interim("[00:00:01] 暫定テキスト")
	if !strings.Contains(buf.String(), "暫定テキスト") {
		t.Fatalf("TTY で暫定結果が出ない: %q", buf.String())
	}
	if strings.Contains(buf.String(), "\n") {
		t.Fatalf("暫定結果が改行を含む (行が流れる): %q", buf.String())
	}
}

func TestFinalAlwaysPrintedWithNewline(t *testing.T) {
	for _, tty := range []bool{true, false} {
		c, buf := newTestConsole(tty)
		c.Final("[00:00:01] 確定テキスト")
		out := buf.String()
		if !strings.Contains(out, "確定テキスト") || !strings.HasSuffix(out, "\n") {
			t.Fatalf("tty=%v: 確定行の出力が不正: %q", tty, out)
		}
	}
}

func TestFinalClearsPendingInterim(t *testing.T) {
	c, buf := newTestConsole(true)
	c.Interim("[00:00:01] 暫定")
	buf.Reset()
	c.Final("[00:00:02] 確定")
	out := buf.String()
	// 残った暫定行を消してから確定行を出すこと。
	if !strings.HasPrefix(out, "\r") {
		t.Fatalf("暫定行をクリアしていない: %q", out)
	}
	if !strings.Contains(out, "確定") {
		t.Fatalf("確定行が出ていない: %q", out)
	}
}
