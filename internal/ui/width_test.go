package ui

import "testing"

func TestDisplayWidth(t *testing.T) {
	cases := map[string]int{
		"":            0,
		"abc":         3,
		"会議":          4, // 全角 2 文字
		"[00:01:15] ": 11,
		"会議a":         5,
	}
	for in, want := range cases {
		if got := DisplayWidth(in); got != want {
			t.Errorf("DisplayWidth(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTruncateTailKeepsRecentText(t *testing.T) {
	s := "本日の会議を始めます議題は来週のリリース計画についてです"
	got := TruncateTail(s, 20)
	if DisplayWidth(got) > 20 {
		t.Fatalf("幅が超過: %q = %d 桁", got, DisplayWidth(got))
	}
	if []rune(got)[0] != '…' {
		t.Fatalf("切り詰め記号が無い: %q", got)
	}
	// 末尾 (直近の発話) が残ること。
	if !hasSuffix(got, "についてです") {
		t.Fatalf("末尾が残っていない: %q", got)
	}
}

func TestTruncateTailNoopWhenFits(t *testing.T) {
	s := "短い発話"
	if got := TruncateTail(s, 40); got != s {
		t.Fatalf("収まる文字列が変更された: %q", got)
	}
}

func TestTruncateTailDegenerate(t *testing.T) {
	if got := TruncateTail("会議", 0); got != "" {
		t.Fatalf("幅 0 で %q", got)
	}
	if got := TruncateTail("会議です", 1); got != "…" {
		t.Fatalf("幅 1 で %q", got)
	}
}

func hasSuffix(s, suf string) bool {
	return len(s) >= len(suf) && s[len(s)-len(suf):] == suf
}
