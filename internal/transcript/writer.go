// Package transcript は確定テキストの整形・重複抑制・ファイル保存を行う。
package transcript

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// hallucinationPhrases は無音・雑音区間で whisper が出しやすい定型句。
// 区間全体がこれらと一致する場合のみ落とす (発話中の部分一致では消さない)。
var hallucinationPhrases = []string{
	"ご視聴ありがとうございました",
	"ご視聴ありがとうございます",
	"ご清聴ありがとうございました",
	"チャンネル登録お願いします",
	"チャンネル登録をお願いします",
	"最後までご視聴いただきありがとうございます",
	"おやすみなさい",
	"thank you for watching",
	"thanks for watching",
	"please subscribe",
	"you",
	"音楽",
	"拍手",
}

// boundaryWindow は「区間境界の重なり」とみなす最大の時間差。
//
// 直前の区間の終了からこれ以内に始まった区間だけを重複抑制の対象にする。
// これより離れていれば、同じ文言でも別の発話として残す
// (例: 10 秒後に別の人が「はい」と答えるケース)。
const boundaryWindow = 2 * time.Second

// Writer は会議 1 回分のテキストファイルを管理する。
type Writer struct {
	path    string
	file    *os.File
	buf     *bufio.Writer
	lastKey string
	lastEnd time.Duration
	hasLast bool
	count   int
}

// New は transcripts/YYYY-MM-DD_HHMMSS.txt を作成して Writer を返す。
func New(dir string, start time.Time) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("保存先ディレクトリを作成できません (%s): %w", dir, err)
	}
	// 会議ごとに必ず別ファイルにする。O_EXCL で作成し、同じ秒に
	// 開始した実行があれば連番を付ける (既存ファイルへ追記しない)。
	base := start.Format("2006-01-02_150405")
	path, f, err := createUnique(dir, base)
	if err != nil {
		return nil, err
	}
	w := &Writer{path: path, file: f, buf: bufio.NewWriter(f)}
	fmt.Fprintf(w.buf, "# 会議文字起こし  開始: %s\n", start.Format("2006-01-02 15:04:05"))
	_ = w.buf.Flush()
	return w, nil
}

// createUnique は衝突しないファイルを作って返す。
// base.txt が既にあれば base-2.txt, base-3.txt … を試す。
func createUnique(dir, base string) (string, *os.File, error) {
	for i := 1; i < 1000; i++ {
		name := base + ".txt"
		if i > 1 {
			name = fmt.Sprintf("%s-%d.txt", base, i)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
		if err == nil {
			return path, f, nil
		}
		if !os.IsExist(err) {
			return "", nil, fmt.Errorf("保存先ファイルを作成できません (%s): %w", path, err)
		}
	}
	return "", nil, fmt.Errorf("保存先ファイル名が衝突し続けています (%s/%s.txt)", dir, base)
}

// Path は保存先パスを返す。
func (w *Writer) Path() string { return w.path }

// Count は書き込んだ行数を返す。
func (w *Writer) Count() int { return w.count }

// normalizeKey は重複判定用に記号・空白を落とした比較キーを作る。
func normalizeKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// Clean は whisper 出力を 1 行のテキストに整える。
func Clean(text string) string {
	// whisper が返すタグや改行を除去する。
	text = strings.ReplaceAll(text, "\n", " ")
	for _, tag := range []string{"[BLANK_AUDIO]", "[_BEG_]", "(音楽)", "（音楽）", "[音楽]"} {
		text = strings.ReplaceAll(text, tag, " ")
	}
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

// IsNoise はテキスト全体が無音由来の定型句かどうかを判定する。
func IsNoise(text string) bool {
	key := normalizeKey(text)
	if key == "" {
		return true
	}
	for _, p := range hallucinationPhrases {
		if key == normalizeKey(p) {
			return true
		}
	}
	// 同一文字の繰り返しのみ (例: "ーーーー") は捨てる。
	first := []rune(key)[0]
	same := true
	for _, r := range key {
		if r != first {
			same = false
			break
		}
	}
	return same && len([]rune(key)) > 1
}

// Dedup は直前の確定行との重複を取り除いた表示用テキストを返す。
// 戻り値が空なら書き込むべき内容が無い。
//
// start は今回の区間の開始時刻。直前の区間の終了から boundaryWindow を
// 超えて離れている場合は、同じ文言でも正当な別発話として抑制しない。
func (w *Writer) Dedup(start time.Duration, text string) string {
	key := normalizeKey(text)
	if key == "" {
		return ""
	}
	if !w.hasLast || w.lastKey == "" {
		return text
	}
	// 時間的に離れていれば別の発話として扱う。
	if start-w.lastEnd > boundaryWindow {
		return text
	}
	// 完全一致、または直前行に完全に含まれる場合は捨てる。
	if key == w.lastKey || strings.Contains(w.lastKey, key) {
		return ""
	}
	// 直前行の末尾と今回の先頭が重なる分を落とす (区間境界の重複対策)。
	if n := overlapLen(w.lastKey, key); n > 0 {
		trimmed := trimRunePrefixByKey(text, n)
		if normalizeKey(trimmed) == "" {
			return ""
		}
		return trimmed
	}
	return text
}

// overlapLen は a の末尾と b の先頭が一致する最大長 (rune 数) を返す。
func overlapLen(a, b string) int {
	ar, br := []rune(a), []rune(b)
	max := len(ar)
	if len(br) < max {
		max = len(br)
	}
	// 短すぎる重なりは偶然の一致なので無視する。
	for n := max; n >= 4; n-- {
		if string(ar[len(ar)-n:]) == string(br[:n]) {
			return n
		}
	}
	return 0
}

// trimRunePrefixByKey は正規化キーで n 文字分に相当する接頭辞を text から落とす。
func trimRunePrefixByKey(text string, n int) string {
	kept := 0
	rs := []rune(text)
	for i, r := range rs {
		if !(unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)) {
			kept++
		}
		if kept == n {
			return strings.TrimSpace(string(rs[i+1:]))
		}
	}
	return ""
}

// Append は確定テキストを 1 行書き込む。
// start / end は会議開始からの区間の開始・終了時刻。
// 書き込んだ行 (表示用) を返す。重複・雑音で書かなかった場合は空文字列。
func (w *Writer) Append(start, end time.Duration, text string) (string, error) {
	text = Clean(text)
	if text == "" || IsNoise(text) {
		return "", nil
	}
	text = w.Dedup(start, text)
	if text == "" {
		return "", nil
	}

	line := fmt.Sprintf("[%s] %s", FormatOffset(start), text)
	if _, err := w.buf.WriteString(line + "\n"); err != nil {
		return "", err
	}
	// 途中で異常終了しても内容が残るよう、毎行フラッシュする。
	if err := w.buf.Flush(); err != nil {
		return "", err
	}
	w.lastKey = normalizeKey(text)
	w.lastEnd = end
	w.hasLast = true
	w.count++
	return line, nil
}

// FormatOffset は会議開始からの経過時間を HH:MM:SS にする。
func FormatOffset(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d:%02d", total/3600, (total%3600)/60, total%60)
}

// Close はファイルを確実に閉じる。
func (w *Writer) Close(end time.Time) error {
	if w.file == nil {
		return nil
	}
	fmt.Fprintf(w.buf, "# 終了: %s  (%d 行)\n", end.Format("2006-01-02 15:04:05"), w.count)
	if err := w.buf.Flush(); err != nil {
		_ = w.file.Close()
		w.file = nil
		return err
	}
	if err := w.file.Sync(); err != nil {
		_ = w.file.Close()
		w.file = nil
		return err
	}
	err := w.file.Close()
	w.file = nil
	return err
}
