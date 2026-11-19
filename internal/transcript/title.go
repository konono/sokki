package transcript

import (
	"strings"
	"unicode"
)

// maxTitleRunes はファイル名に付けるタイトルの最大文字数。
const maxTitleRunes = 40

// TitleSlug は会議タイトルをファイル名に使える形に整える。
//
// パス区切りや制御文字・ファイル名で問題になる記号を落とし、
// 空白は "_" にまとめる。使える文字が残らなければ空文字列を返す。
func TitleSlug(title string) string {
	var b strings.Builder
	pendingSep := false
	for _, r := range title {
		switch {
		case unicode.IsSpace(r):
			pendingSep = true
			continue
		case unicode.IsControl(r), r == unicode.ReplacementChar:
			continue
		case strings.ContainsRune(`/\:*?"<>|`, r):
			pendingSep = true
			continue
		}
		if pendingSep && b.Len() > 0 {
			b.WriteRune('_')
		}
		pendingSep = false
		b.WriteRune(r)
	}
	rs := []rune(b.String())
	if len(rs) > maxTitleRunes {
		rs = rs[:maxTitleRunes]
	}
	// 隠しファイル化や見づらい区切りを避ける。
	return strings.Trim(string(rs), "._- ")
}
