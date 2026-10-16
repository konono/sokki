//go:build darwin

package ui

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// 実端末 (PTY) を使った検証。
//
// fakeTerm は描画結果の確認に使うが、端末サイズの取得 (ioctl) や
// TTY 判定は実物でないと確かめられない。ここでは擬似端末を開いて
// 実際の fd に対して検証する。音声デバイスや実 pane は使わない。

const (
	tiocptygrant = 0x20007454
	tiocptyunlk  = 0x20007452
	tiocptygname = 0x40807453
	tiocswinsz   = 0x80087467
)

// openPTY は擬似端末の master / slave を開く。
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("/dev/ptmx を開けない: %v", err)
	}
	ok := func(req uintptr, arg unsafe.Pointer) bool {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), req, uintptr(arg))
		return errno == 0
	}
	var name [128]byte
	if !ok(tiocptygrant, nil) || !ok(tiocptyunlk, nil) ||
		!ok(tiocptygname, unsafe.Pointer(&name[0])) {
		m.Close()
		t.Skip("擬似端末を準備できない")
	}
	slaveName := strings.TrimRight(string(name[:]), "\x00")
	s, err := os.OpenFile(slaveName, os.O_RDWR, 0)
	if err != nil {
		m.Close()
		t.Skipf("擬似端末の slave を開けない: %v", err)
	}
	t.Cleanup(func() { s.Close(); m.Close() })
	return m, s
}

// setWinsize は端末サイズを設定する (実際のリサイズ)。
func setWinsize(t *testing.T, f *os.File, rows, cols int) {
	t.Helper()
	ws := winsize{Row: uint16(rows), Col: uint16(cols)}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(),
		uintptr(tiocswinsz), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		t.Fatalf("端末サイズを設定できない: %v", errno)
	}
}

// 実端末のサイズを ioctl で取得できること。
func TestTermSizeOfRealPTY(t *testing.T) {
	_, slave := openPTY(t)
	setWinsize(t, slave, 79, 140)

	rows, cols, ok := TermSizeOf(slave.Fd())
	if !ok {
		t.Fatal("端末サイズを取得できない")
	}
	if rows != 79 || cols != 140 {
		t.Fatalf("サイズ = %dx%d, want 79x140", rows, cols)
	}
}

// 実際にリサイズしたら、新しいサイズが読めること。
func TestTermSizeFollowsRealResize(t *testing.T) {
	_, slave := openPTY(t)
	setWinsize(t, slave, 24, 60)
	if r, c, _ := TermSizeOf(slave.Fd()); r != 24 || c != 60 {
		t.Fatalf("初期サイズ = %dx%d", r, c)
	}

	setWinsize(t, slave, 79, 140)
	r, c, ok := TermSizeOf(slave.Fd())
	if !ok || r != 79 || c != 140 {
		t.Fatalf("リサイズ後 = %dx%d (ok=%v)", r, c, ok)
	}
}

// 端末でない fd では ok=false になること。
func TestTermSizeOfNonTTY(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "notty")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, _, ok := TermSizeOf(f.Fd()); ok {
		t.Error("通常ファイルを端末と判定した")
	}
}

// 実端末に対して Console が TTY と判定すること。
func TestConsoleDetectsRealPTY(t *testing.T) {
	_, slave := openPTY(t)
	fi, err := slave.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		t.Fatal("擬似端末が文字デバイスでない")
	}
	con := NewWith(slave, true)
	if !con.IsTTY() {
		t.Error("実端末を TTY と判定しない")
	}
}

// 実端末へ分割表示を書き出し、リサイズまで通せること。
//
// 描画結果の検証は fakeTerm が担当する。ここでは
// 「実 fd への書き込みと、実サイズに基づく再配置が落ちない」ことを見る。
func TestSplitWritesToRealPTY(t *testing.T) {
	master, slave := openPTY(t)
	setWinsize(t, slave, 24, 60)

	// master 側を読み捨てる (書き込みが詰まらないように)。
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			if _, err := master.Read(buf); err != nil {
				return
			}
		}
	}()

	rows, cols, ok := TermSizeOf(slave.Fd())
	if !ok {
		t.Fatal("サイズを取得できない")
	}
	s := NewSplit(slave, rows, cols, true)
	s.Start()
	s.SetBoard([]string{"今の話題: 実端末での確認", "・要点"})
	for i := 0; i < 20; i++ {
		s.Interim("[00:00:01] " + strings.Repeat("暫定", i+1))
		s.Log("[00:00:02] " + strings.Repeat("確定の発言", i%4+1))
	}

	// 実際にリサイズして追従させる。
	setWinsize(t, slave, 79, 140)
	nr, nc, _ := TermSizeOf(slave.Fd())
	s.Resize(nr, nc)
	if s.rows != 79 || s.cols != 140 {
		t.Fatalf("実サイズを反映していない: %dx%d", s.rows, s.cols)
	}
	// 上下の領域が画面内に収まること。
	logH, boardH := s.layout()
	if logH < 2 || boardH < 2 || logH+boardH != s.rows {
		t.Errorf("領域の配分が不正: log=%d board=%d rows=%d", logH, boardH, s.rows)
	}

	s.Stop()
	slave.Close()
	master.Close()
	<-done
}

// --- キー入力 (実 PTY) ---

// 実端末でキーを読み、入力モードを復元できること。
func TestKeysOnRealPTY(t *testing.T) {
	master, slave := openPTY(t)
	setWinsize(t, slave, 24, 60)

	before, err := getTermios(slave.Fd())
	if err != nil {
		t.Fatalf("入力モードを取得できない: %v", err)
	}

	got := make(chan Key, 32)
	keys, err := StartKeys(slave, func(k Key) { got <- k })
	if err != nil {
		t.Fatalf("キー読み取りを開始できない: %v", err)
	}

	// 入力モードが変わっていること (エコーと行バッファが切れている)。
	during, err := getTermios(slave.Fd())
	if err != nil {
		t.Fatal(err)
	}
	if during.Lflag&syscall.ECHO != 0 {
		t.Error("エコーが切れていない")
	}
	if during.Lflag&syscall.ICANON != 0 {
		t.Error("行バッファが切れていない")
	}
	// Ctrl-C を壊さないこと。
	if during.Lflag&syscall.ISIG == 0 {
		t.Error("ISIG が落ちている (Ctrl-C が効かなくなる)")
	}

	// master 側から実際にキーを送る。
	send := func(s string) {
		if _, err := master.WriteString(s); err != nil {
			t.Fatalf("キーを送れない: %v", err)
		}
	}
	want := []struct {
		in  string
		key Key
	}{
		{"\t", KeyFocusNext},
		{"j", KeyDown},
		{"k", KeyUp},
		{"\x1b[B", KeyDown},
		{"\x1b[A", KeyUp},
		{"\x1b[6~", KeyPageDown},
		{"\x1b[5~", KeyPageUp},
		{"\x04", KeyHalfDown},
		{"\x15", KeyHalfUp},
		{"g", KeyHome},
		{"G", KeyEnd},
	}
	for _, w := range want {
		send(w.in)
		select {
		case k := <-got:
			if k != w.key {
				t.Errorf("%q: %v, want %v", w.in, k, w.key)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%q のキーが届かない", w.in)
		}
	}

	keys.Stop()

	after, err := getTermios(slave.Fd())
	if err != nil {
		t.Fatal(err)
	}
	if after.Lflag != before.Lflag {
		t.Errorf("入力モードが復元されていない: %x → %x", before.Lflag, after.Lflag)
	}
	if after.Cc[syscall.VMIN] != before.Cc[syscall.VMIN] ||
		after.Cc[syscall.VTIME] != before.Cc[syscall.VTIME] {
		t.Errorf("読み取り設定が復元されていない")
	}
	// 二重に止めても安全なこと (起動失敗経路と通常終了の両方から呼ぶ)。
	keys.Stop()
}

// 端末でない fd では開始に失敗し、入力モードを変えないこと。
func TestKeysRejectsNonTTY(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "notty")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if keys, err := StartKeys(f, func(Key) {}); err == nil {
		keys.Stop()
		t.Error("端末でない fd で開始できてしまった")
	}
}

// キー操作が実 PTY の描画と結びつくこと (Tab → フォーカス移動)。
func TestKeysDriveSplitOnRealPTY(t *testing.T) {
	master, slave := openPTY(t)
	setWinsize(t, slave, 24, 60)
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := master.Read(buf); err != nil {
				return
			}
		}
	}()

	rows, cols, _ := TermSizeOf(slave.Fd())
	s := NewSplit(slave, rows, cols, false)
	s.Start()
	defer s.Stop()
	for i := 0; i < 50; i++ {
		s.Log("[00:00:01] 発言")
	}
	s.SetBoard([]string{"今の話題: 実端末", "要約:", "・要点"})

	keys, err := StartKeys(slave, func(k Key) {
		switch k {
		case KeyFocusNext:
			s.FocusNext()
		case KeyUp:
			s.Scroll(-1)
		}
	})
	if err != nil {
		t.Fatalf("キー読み取りを開始できない: %v", err)
	}
	defer keys.Stop()

	master.WriteString("\t")
	deadline := time.Now().Add(3 * time.Second)
	for !s.FocusIsBoard() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !s.FocusIsBoard() {
		t.Fatal("Tab でフォーカスが移らない")
	}

	master.WriteString("k")
	time.Sleep(200 * time.Millisecond) // 反映を待つ (落ちないことの確認)
}

// 矢印キーが Read 2 回に分かれて届いても失われないこと。
//
// 実機では ESC と "[A" が別の Read になり、矢印が消えていた。
func TestKeysSplitAcrossReadsOnRealPTY(t *testing.T) {
	master, slave := openPTY(t)
	setWinsize(t, slave, 24, 60)

	got := make(chan Key, 16)
	keys, err := StartKeys(slave, func(k Key) { got <- k })
	if err != nil {
		t.Fatalf("キー読み取りを開始できない: %v", err)
	}
	defer keys.Stop()

	cases := []struct {
		first, second string
		want          Key
	}{
		{"\x1b", "[A", KeyUp},
		{"\x1b", "[B", KeyDown},
		{"\x1b[", "5~", KeyPageUp},
		{"\x1b[6", "~", KeyPageDown},
	}
	for _, tc := range cases {
		if _, err := master.WriteString(tc.first); err != nil {
			t.Fatal(err)
		}
		// 別の Read になる程度の間隔を空ける (実機で起きた分かれ方)。
		time.Sleep(20 * time.Millisecond)
		if _, err := master.WriteString(tc.second); err != nil {
			t.Fatal(err)
		}
		select {
		case k := <-got:
			if k != tc.want {
				t.Errorf("%q + %q: %v, want %v", tc.first, tc.second, k, tc.want)
			}
		case <-time.After(3 * time.Second):
			t.Errorf("%q + %q: キーが届かない", tc.first, tc.second)
		}
	}
}

// 単独の Esc は何も起こさず、後続のキーを妨げないこと。
func TestLoneEscapeIsIgnoredOnRealPTY(t *testing.T) {
	master, slave := openPTY(t)
	setWinsize(t, slave, 24, 60)

	got := make(chan Key, 16)
	keys, err := StartKeys(slave, func(k Key) { got <- k })
	if err != nil {
		t.Fatalf("キー読み取りを開始できない: %v", err)
	}
	defer keys.Stop()

	master.WriteString("\x1b")
	// 続きを待つ時間を過ぎれば捨てられること。
	time.Sleep(escapeTimeout + 300*time.Millisecond)
	select {
	case k := <-got:
		t.Fatalf("単独 Esc でキーが出た: %v", k)
	default:
	}

	master.WriteString("j")
	select {
	case k := <-got:
		if k != KeyDown {
			t.Errorf("後続のキー = %v, want KeyDown", k)
		}
	case <-time.After(3 * time.Second):
		t.Error("Esc の後にキーが届かない")
	}
}

// 入力が無い時間が続いても、キー読み取りが止まらないこと。
//
// 待ち時間切れの 0 バイト読み取りを終了と誤解すると、
// 少し放置しただけでキー操作が効かなくなる。
func TestKeysSurviveIdlePeriodOnRealPTY(t *testing.T) {
	master, slave := openPTY(t)
	setWinsize(t, slave, 24, 60)

	got := make(chan Key, 8)
	keys, err := StartKeys(slave, func(k Key) { got <- k })
	if err != nil {
		t.Fatalf("キー読み取りを開始できない: %v", err)
	}
	defer keys.Stop()

	// 読み取りの待ち時間 (VTIME=0.1 秒) を何度も跨ぐ。
	time.Sleep(1200 * time.Millisecond)

	master.WriteString("j")
	select {
	case k := <-got:
		if k != KeyDown {
			t.Errorf("キー = %v, want KeyDown", k)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("放置後にキーが届かない (読み取りが止まっている)")
	}

	// さらに放置しても続くこと。
	time.Sleep(600 * time.Millisecond)
	master.WriteString("\t")
	select {
	case k := <-got:
		if k != KeyFocusNext {
			t.Errorf("キー = %v, want KeyFocusNext", k)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("2 度目の放置後にキーが届かない")
	}
}
