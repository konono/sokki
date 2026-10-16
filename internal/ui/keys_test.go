package ui

import "testing"

// キー列の解釈 (実端末を使わない単体確認)。
func TestDecodeKeys(t *testing.T) {
	cases := []struct {
		in   string
		want []Key
	}{
		{"\t", []Key{KeyFocusNext}},
		{"\x1b[Z", []Key{KeyFocusNext}}, // Shift-Tab
		{"jk", []Key{KeyDown, KeyUp}},
		{"gG", []Key{KeyHome, KeyEnd}},
		{" ", []Key{KeyPageDown}},
		{"\x04\x15", []Key{KeyHalfDown, KeyHalfUp}},
		{"\x1b[A", []Key{KeyUp}},
		{"\x1b[B", []Key{KeyDown}},
		{"\x1bOA", []Key{KeyUp}},
		{"\x1b[5~", []Key{KeyPageUp}},
		{"\x1b[6~", []Key{KeyPageDown}},
		{"\x1b[H", []Key{KeyHome}},
		{"\x1b[F", []Key{KeyEnd}},
		{"xyz", nil},                               // 未割り当ては捨てる
		{"\x1b[200~", nil},                         // 未知の列は読み飛ばす
		{"\t\x1b[B", []Key{KeyFocusNext, KeyDown}}, // 連結
		{"\x1b[A\x1b[A", []Key{KeyUp, KeyUp}},      // 連打
		{"\x1b[6~j", []Key{KeyPageDown, KeyDown}},  // 列の後に文字
	}
	for _, tc := range cases {
		got, rest := decodeKeys([]byte(tc.in))
		if len(rest) > 0 {
			t.Errorf("%q: 断片が残った: %q", tc.in, rest)
		}
		if len(got) != len(tc.want) {
			t.Errorf("%q: %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%q: %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}

// 途中で切れたエスケープ列は断片として持ち越すこと。
func TestDecodeKeysCarriesPartialSequence(t *testing.T) {
	cases := []struct {
		in       string
		wantRest string
	}{
		{"\x1b", "\x1b"},     // Esc だけ
		{"\x1b[", "\x1b["},   // CSI まで
		{"\x1b[5", "\x1b[5"}, // 数字の途中
		{"j\x1b[", "\x1b["},  // 文字の後に断片
	}
	for _, tc := range cases {
		_, rest := decodeKeys([]byte(tc.in))
		if string(rest) != tc.wantRest {
			t.Errorf("%q: rest = %q, want %q", tc.in, rest, tc.wantRest)
		}
	}
}

// 断片と続きを連結すれば元のキーになること (Read が分かれた場合)。
func TestDecodeKeysSplitAcrossReads(t *testing.T) {
	splits := []struct {
		first, second string
		want          Key
	}{
		{"\x1b", "[A", KeyUp},
		{"\x1b[", "B", KeyDown},
		{"\x1b[5", "~", KeyPageUp},
		{"\x1b[6", "~", KeyPageDown},
	}
	for _, sp := range splits {
		keys, rest := decodeKeys([]byte(sp.first))
		if len(keys) != 0 {
			t.Errorf("%q: 途中でキーを出した: %v", sp.first, keys)
		}
		keys, rest2 := decodeKeys(append(rest, []byte(sp.second)...))
		if len(rest2) != 0 {
			t.Errorf("%q+%q: 断片が残った: %q", sp.first, sp.second, rest2)
		}
		if len(keys) != 1 || keys[0] != sp.want {
			t.Errorf("%q+%q: %v, want [%v]", sp.first, sp.second, keys, sp.want)
		}
	}
}
