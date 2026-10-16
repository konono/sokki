package ui

import (
	"regexp"
	"strconv"
	"strings"
)

// --- 模擬端末 ---
//
// 出力した制御シーケンスを解釈し、画面の各行の内容を保持する。
// 実端末を使わずに「見え方」を検証する。

type fakeTerm struct {
	rows, cols int
	screen     []string
	cy, cx     int // 0 始まり
	top, bot   int // スクロール領域 (0 始まり、bot は含む)
	savedY     int
	savedX     int
}

func newFakeTerm(rows, cols int) *fakeTerm {
	t := &fakeTerm{rows: rows, cols: cols}
	t.screen = make([]string, rows)
	t.top, t.bot = 0, rows-1
	return t
}

// Resize は端末のリサイズを模す。
//
// 実端末は行数が減ると内容を上へ詰める (行が移動する)。
// 移動を模さないと「古い描画が別の行に残る」症状を再現できない。
func (t *fakeTerm) Resize(rows, cols int) {
	screen := make([]string, rows)
	if rows < t.rows {
		// 上から詰める (下端を残す)。
		copy(screen, t.screen[t.rows-rows:])
		t.cy -= t.rows - rows
	} else {
		copy(screen, t.screen)
	}
	t.screen = screen
	t.rows, t.cols = rows, cols
	t.top, t.bot = 0, rows-1
	if t.cy >= rows {
		t.cy = rows - 1
	}
	if t.cy < 0 {
		t.cy = 0
	}
}

var csiRE = regexp.MustCompile(`^\x1b\[([?0-9;]*)([A-Za-z])`)

// Write は端末のふるまいを最低限再現する。
func (t *fakeTerm) Write(p []byte) (int, error) {
	s := string(p)
	n := len(p)
	for len(s) > 0 {
		switch {
		case strings.HasPrefix(s, "\x1b7"):
			t.savedY, t.savedX = t.cy, t.cx
			s = s[2:]
		case strings.HasPrefix(s, "\x1b8"):
			t.cy, t.cx = t.savedY, t.savedX
			s = s[2:]
		case strings.HasPrefix(s, "\x1b["):
			m := csiRE.FindStringSubmatch(s)
			if m == nil {
				s = s[2:]
				continue
			}
			t.csi(m[1], m[2])
			s = s[len(m[0]):]
		case strings.HasPrefix(s, "\r"):
			t.cx = 0
			s = s[1:]
		case strings.HasPrefix(s, "\n"):
			t.lineFeed()
			s = s[1:]
		default:
			r, size := decodeRune(s)
			t.put(r)
			s = s[size:]
		}
	}
	return n, nil
}

func decodeRune(s string) (rune, int) {
	for i, r := range s {
		_ = i
		return r, len(string(r))
	}
	return 0, 1
}

func (t *fakeTerm) csi(args, verb string) {
	nums := []int{}
	for _, a := range strings.Split(args, ";") {
		if a == "" {
			continue
		}
		v, _ := strconv.Atoi(a)
		nums = append(nums, v)
	}
	at := func(i, def int) int {
		if i < len(nums) {
			return nums[i]
		}
		return def
	}
	if strings.HasPrefix(args, "?") {
		// 私的モード (カーソル表示など) は画面内容に影響しない。
		return
	}
	switch verb {
	case "r": // スクロール領域 (DEC: 設定するとカーソルは原点へ戻る)
		if len(nums) == 0 {
			t.top, t.bot = 0, t.rows-1
		} else {
			t.top, t.bot = at(0, 1)-1, at(1, t.rows)-1
		}
		t.cy, t.cx = 0, 0
	case "H": // カーソル移動
		t.cy, t.cx = at(0, 1)-1, at(1, 1)-1
	case "A": // 上へ
		t.cy -= at(0, 1)
		if t.cy < 0 {
			t.cy = 0
		}
	case "K": // 行消去
		if t.cy >= 0 && t.cy < t.rows {
			t.screen[t.cy] = ""
		}
	case "J": // 画面消去 (2 = 全体)
		if at(0, 0) == 2 {
			for i := range t.screen {
				t.screen[i] = ""
			}
		}
	case "m": // 装飾 (無視)
	}
}

func (t *fakeTerm) lineFeed() {
	t.cx = 0
	if t.cy == t.bot {
		// スクロール領域内でスクロールする。
		copy(t.screen[t.top:t.bot], t.screen[t.top+1:t.bot+1])
		t.screen[t.bot] = ""
		return
	}
	if t.cy < t.rows-1 {
		t.cy++
	}
}

func (t *fakeTerm) put(r rune) {
	if t.cy < 0 || t.cy >= t.rows {
		return
	}
	line := []rune(t.screen[t.cy])
	for len(line) < t.cx {
		line = append(line, ' ')
	}
	if t.cx < len(line) {
		line[t.cx] = r
	} else {
		line = append(line, r)
	}
	t.screen[t.cy] = string(line)
	t.cx++
}

func (t *fakeTerm) Line(i int) string { return t.screen[i] }

func (t *fakeTerm) All() string { return strings.Join(t.screen, "\n") }

// releaseRegionKeepCursor は「端末側でスクロール領域だけが解除され、
// カーソル位置は保たれた」状態を作る。
//
// 実端末では CSI r でカーソルが原点へ戻るが、別経路 (別プログラムの
// 出力やリセット) で領域だけ外れ、カーソルがそのまま残ることがある。
// その状態を明示的に再現する。
func (t *fakeTerm) releaseRegionKeepCursor() {
	t.top, t.bot = 0, t.rows-1
}

// boardRow は会議ボードの見出しがある行を返す (無ければ -1)。
func (t *fakeTerm) boardRow() int {
	for i, l := range t.screen {
		if strings.Contains(l, "会議ボ") {
			return i
		}
	}
	return -1
}
