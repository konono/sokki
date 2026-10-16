//go:build darwin

package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// escapeTimeout は途中まで届いたエスケープ列の続きを待つ時間。
//
// 矢印キーの "\x1b[A" は Read が分かれて届くことがあるため、
// 断片をすぐ捨てるとキーが消える。一方で待ち続けると単独の Esc が
// 残り続けるので、この時間を過ぎたら捨てる。
const escapeTimeout = 200 * time.Millisecond

// Key は画面操作のキー。
type Key int

const (
	KeyNone Key = iota
	KeyUp
	KeyDown
	KeyPageUp
	KeyPageDown
	KeyHome
	KeyEnd
	// KeyHalfUp / KeyHalfDown は半ページ移動 (Ctrl+u / Ctrl+d)。
	KeyHalfUp
	KeyHalfDown
	// KeyFocusNext は操作対象の領域の切り替え (Tab)。
	KeyFocusNext
)

// Keys は TTY のキー入力を読み、コールバックを呼ぶ。
//
// エコーと行バッファだけを切り、SIGINT (ISIG) は残す。
// Ctrl-C は従来どおりシグナルとして届く。
// Stop で必ず元の入力モードへ戻す。
type Keys struct {
	f     *os.File
	saved syscall.Termios

	once sync.Once
	stop chan struct{}
	done chan struct{}
}

// StartKeys はキー入力の読み取りを始める。
//
// 端末でない場合はエラーを返す (呼び出し側は読み取りなしで続行する)。
func StartKeys(f *os.File, fn func(Key)) (*Keys, error) {
	if f == nil || fn == nil {
		return nil, fmt.Errorf("キー入力の設定が不正です")
	}
	saved, err := getTermios(f.Fd())
	if err != nil {
		return nil, fmt.Errorf("端末の入力モードを取得できません: %w", err)
	}

	raw := saved
	// エコーと行バッファのみ切る。ISIG は残して Ctrl-C を壊さない。
	raw.Lflag &^= syscall.ECHO | syscall.ICANON
	// 読み取りが止まり続けないよう 0.1 秒で戻す (停止要求を拾うため)。
	raw.Cc[syscall.VMIN] = 0
	raw.Cc[syscall.VTIME] = 1
	if err := setTermios(f.Fd(), raw); err != nil {
		return nil, fmt.Errorf("端末の入力モードを変更できません: %w", err)
	}

	k := &Keys{
		f:     f,
		saved: saved,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go k.loop(fn)
	return k, nil
}

// Stop は読み取りを止め、入力モードを元に戻す。
//
// 複数回呼んでも安全 (起動失敗経路と通常終了の両方から呼ぶ)。
func (k *Keys) Stop() {
	if k == nil {
		return
	}
	k.once.Do(func() {
		close(k.stop)
		<-k.done
		// 読み取りを止めてから戻す (戻した後に読むと行バッファに戻る)。
		_ = setTermios(k.f.Fd(), k.saved)
	})
}

func (k *Keys) loop(fn func(Key)) {
	defer close(k.done)
	buf := make([]byte, 64)
	// pending は読み切れなかったエスケープ列の断片。
	//
	// 矢印キーは "\x1b[A" の 3 バイトだが、Read が "\x1b" と "[A" に
	// 分かれて返ることがある。断片を次の読み取りへ繰り越さないと
	// キーが消える。
	var pending []byte
	var pendingAt time.Time
	// 断片が古くなっていれば捨てる。
	expire := func() {
		if len(pending) > 0 && time.Since(pendingAt) > escapeTimeout {
			pending = nil
		}
	}
	for {
		select {
		case <-k.stop:
			return
		default:
		}
		n, err := k.f.Read(buf)
		if err != nil {
			// VMIN=0 / VTIME=1 では、入力が無いまま待ち時間が切れると
			// 0 バイト = EOF として返る。これは終了ではないので続ける
			// (ここで抜けると、少し放置しただけでキー操作が効かなくなる)。
			if errors.Is(err, io.EOF) {
				expire()
				continue
			}
			// それ以外のエラーは諦める (録音は続く)。
			return
		}
		if n == 0 {
			expire()
			continue
		}
		data := buf[:n]
		if len(pending) > 0 {
			data = append(pending, data...)
			pending = nil
		}
		keys, rest := decodeKeys(data)
		if len(rest) > 0 {
			pending = append([]byte(nil), rest...)
			pendingAt = time.Now()
		}
		for _, key := range keys {
			select {
			case <-k.stop:
				return
			default:
			}
			fn(key)
		}
	}
}

// decodeKeys は入力バイト列をキーへ変換する。
//
// 矢印・PageUp/PageDown はエスケープ列で届く。
// 解釈できないバイトは捨てる (誤作動させない)。
// 末尾が途中までのエスケープ列なら、その断片を rest として返す
// (次の読み取りと連結して解釈するため)。
func decodeKeys(b []byte) (keys []Key, rest []byte) {
	for i := 0; i < len(b); {
		c := b[i]
		if c != 0x1b {
			if k := plainKey(c); k != KeyNone {
				keys = append(keys, k)
			}
			i++
			continue
		}
		seq, used, complete := parseEscape(b[i:])
		if !complete {
			// 続きが来るかもしれないので持ち越す。
			return keys, b[i:]
		}
		if seq != KeyNone {
			keys = append(keys, seq)
		}
		i += used
	}
	return keys, nil
}

// plainKey は通常文字のキーを返す。
func plainKey(c byte) Key {
	switch c {
	case '\t':
		return KeyFocusNext
	case 0x04: // Ctrl+d
		return KeyHalfDown
	case 0x15: // Ctrl+u
		return KeyHalfUp
	case 'j':
		return KeyDown
	case 'k':
		return KeyUp
	case 'g':
		return KeyHome
	case 'G':
		return KeyEnd
	case ' ':
		return KeyPageDown
	}
	return KeyNone
}

// parseEscape はエスケープ列を 1 つ読み、キーと消費したバイト数を返す。
//
// complete=false は「列が途中で終わっている」= 続きを待つべき状態。
// 解釈できない完結した列は読み飛ばす (誤作動させない)。
func parseEscape(b []byte) (key Key, used int, complete bool) {
	if len(b) == 1 {
		return KeyNone, 1, false // Esc だけ: 続きを待つ
	}
	if b[1] != '[' && b[1] != 'O' {
		return KeyNone, 2, true // Alt+文字などは捨てる
	}
	if len(b) == 2 {
		return KeyNone, 2, false // "\x1b[" まで: 続きを待つ
	}
	if b[2] == 'Z' { // Shift-Tab
		return KeyFocusNext, 3, true
	}
	switch b[2] {
	case 'A':
		return KeyUp, 3, true
	case 'B':
		return KeyDown, 3, true
	case 'C':
		return KeyDown, 3, true // → は下へ (1 行送り)
	case 'D':
		return KeyUp, 3, true
	case 'H':
		return KeyHome, 3, true
	case 'F':
		return KeyEnd, 3, true
	}
	// CSI n ~ 形式 (PageUp = 5~, PageDown = 6~, Home = 1~/7~, End = 4~/8~)
	j := 2
	num := 0
	for j < len(b) && b[j] >= '0' && b[j] <= '9' {
		num = num*10 + int(b[j]-'0')
		j++
	}
	if j < len(b) && b[j] == '~' {
		n := j + 1
		switch num {
		case 1, 7:
			return KeyHome, n, true
		case 4, 8:
			return KeyEnd, n, true
		case 5:
			return KeyPageUp, n, true
		case 6:
			return KeyPageDown, n, true
		}
		return KeyNone, n, true
	}
	if j < len(b) {
		// 数字でも ~ でもない文字で終わる列は、ここで完結とみなす。
		return KeyNone, j + 1, true
	}
	// 数字の途中で切れている: 続きを待つ。
	return KeyNone, len(b), false
}

func getTermios(fd uintptr) (syscall.Termios, error) {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		fd, uintptr(syscall.TIOCGETA), uintptr(unsafe.Pointer(&t)))
	if errno != 0 {
		return t, errno
	}
	return t, nil
}

func setTermios(fd uintptr, t syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		fd, uintptr(syscall.TIOCSETA), uintptr(unsafe.Pointer(&t)))
	if errno != 0 {
		return errno
	}
	return nil
}
