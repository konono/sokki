package ui

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// TIOCGWINSZ (darwin) で端末の桁数を取得する。
const tiocgwinsz = 0x40087468

type winsize struct {
	Row, Col, Xpixel, Ypixel uint16
}

// TermSize は端末の行数・桁数を返す。取得できない場合は 30, 100 を返す。
func TermSize() (rows, cols int) {
	if r, c, ok := TermSizeOf(os.Stdout.Fd()); ok {
		return r, c
	}
	return 30, TermWidth()
}

// TermSizeOf は指定した fd の端末サイズを返す。
// 端末でない場合は ok=false。
func TermSizeOf(fd uintptr) (rows, cols int, ok bool) {
	var ws winsize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		fd, uintptr(tiocgwinsz), uintptr(unsafe.Pointer(&ws)))
	if errno == 0 && ws.Row > 0 && ws.Col > 0 {
		return int(ws.Row), int(ws.Col), true
	}
	return 0, 0, false
}

// TermWidth は端末の桁数を返す。取得できない場合は 100 を返す。
func TermWidth() int {
	var ws winsize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		os.Stdout.Fd(), uintptr(tiocgwinsz), uintptr(unsafe.Pointer(&ws)))
	if errno == 0 && ws.Col > 0 {
		return int(ws.Col)
	}
	if v, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && v > 0 {
		return v
	}
	return 100
}

// runeCells は 1 文字が占める端末上の桁数を返す。
// 日本語などの全角文字は 2 桁として扱う。
func runeCells(r rune) int {
	switch {
	case r < 0x1100:
		return 1
	case r >= 0x1100 && r <= 0x115F, // ハングル字母
		r == 0x2329, r == 0x232A,
		r >= 0x2E80 && r <= 0xA4CF && r != 0x303F, // CJK 部首〜漢字
		r >= 0xAC00 && r <= 0xD7A3,                // ハングル音節
		r >= 0xF900 && r <= 0xFAFF,                // CJK 互換漢字
		r >= 0xFE30 && r <= 0xFE6F,                // CJK 互換形
		r >= 0xFF00 && r <= 0xFF60,                // 全角英数・記号
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F64F, // 絵文字
		r >= 0x20000 && r <= 0x3FFFD: // CJK 拡張
		return 2
	default:
		return 1
	}
}

// DisplayWidth は文字列が占める桁数を返す。
func DisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeCells(r)
	}
	return w
}

// TruncateTail は末尾側を残して先頭を切り詰め、maxCells 桁に収める。
// 暫定結果は「直近の発話」が見たいので先頭を落とす。
func TruncateTail(s string, maxCells int) string {
	if maxCells <= 0 {
		return ""
	}
	if DisplayWidth(s) <= maxCells {
		return s
	}
	const ellipsis = "…"
	budget := maxCells - runeCells('…')
	if budget <= 0 {
		return ellipsis
	}
	rs := []rune(s)
	w := 0
	start := len(rs)
	for i := len(rs) - 1; i >= 0; i-- {
		c := runeCells(rs[i])
		if w+c > budget {
			break
		}
		w += c
		start = i
	}
	return ellipsis + string(rs[start:])
}
