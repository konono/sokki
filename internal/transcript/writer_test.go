package transcript

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestFormatOffset(t *testing.T) {
	cases := map[time.Duration]string{
		0:                               "00:00:00",
		5 * time.Second:                 "00:00:05",
		75 * time.Second:                "00:01:15",
		3725 * time.Second:              "01:02:05",
		-1 * time.Second:                "00:00:00",
		90*time.Minute + 30*time.Second: "01:30:30",
	}
	for in, want := range cases {
		if got := FormatOffset(in); got != want {
			t.Errorf("FormatOffset(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestClean(t *testing.T) {
	cases := map[string]string{
		" こんにちは \n":     "こんにちは",
		"[BLANK_AUDIO]": "",
		"会議を\n始めます":     "会議を 始めます",
		"（音楽） テスト":      "テスト",
		"  複数   空白  ":   "複数 空白",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsNoise(t *testing.T) {
	noisy := []string{
		"",
		"ご視聴ありがとうございました",
		"ご視聴ありがとうございました。",
		"Thanks for watching!",
		"ーーーーー",
		"音楽",
	}
	for _, s := range noisy {
		if !IsNoise(s) {
			t.Errorf("IsNoise(%q) = false, want true", s)
		}
	}
	real := []string{
		"本日の会議を始めます",
		"ご視聴ありがとうございました、と言われました",
		"はい",
	}
	for _, s := range real {
		if IsNoise(s) {
			t.Errorf("IsNoise(%q) = true, want false", s)
		}
	}
}

func newTestWriter(t *testing.T) *Writer {
	t.Helper()
	w, err := New(t.TempDir(), time.Date(2026, 10, 16, 9, 30, 0, 0, time.Local))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close(time.Now()) })
	return w
}

func TestAppendWritesTimestampedLine(t *testing.T) {
	w := newTestWriter(t)
	line, err := w.Append(75*time.Second, 78*time.Second, "本日の会議を始めます")
	if err != nil {
		t.Fatal(err)
	}
	if want := "[00:01:15] 本日の会議を始めます"; line != want {
		t.Fatalf("line = %q, want %q", line, want)
	}
	if !strings.HasSuffix(w.Path(), "2026-10-16_093000.txt") {
		t.Fatalf("ファイル名が規約と違う: %s", w.Path())
	}
}

func TestAppendSkipsExactDuplicate(t *testing.T) {
	w := newTestWriter(t)
	if _, err := w.Append(time.Second, 3*time.Second, "来週のリリース計画について"); err != nil {
		t.Fatal(err)
	}
	line, err := w.Append(4*time.Second, 6*time.Second, "来週のリリース計画について。")
	if err != nil {
		t.Fatal(err)
	}
	if line != "" {
		t.Fatalf("重複行が書き込まれた: %q", line)
	}
	if w.Count() != 1 {
		t.Fatalf("行数 = %d, want 1", w.Count())
	}
}

func TestAppendTrimsBoundaryOverlap(t *testing.T) {
	w := newTestWriter(t)
	if _, err := w.Append(0, 3*time.Second, "今日の議題は来週のリリース計画です"); err != nil {
		t.Fatal(err)
	}
	line, err := w.Append(3*time.Second, 6*time.Second, "来週のリリース計画ですが、延期を検討しています")
	if err != nil {
		t.Fatal(err)
	}
	if line == "" {
		t.Fatal("重なり部分だけで行が消えた")
	}
	if strings.Contains(line, "今日の議題") {
		t.Fatalf("想定外の内容: %q", line)
	}
	if !strings.Contains(line, "延期を検討しています") {
		t.Fatalf("新規部分が失われた: %q", line)
	}
}

func TestAppendSkipsNoise(t *testing.T) {
	w := newTestWriter(t)
	line, err := w.Append(time.Second, 2*time.Second, "ご視聴ありがとうございました")
	if err != nil {
		t.Fatal(err)
	}
	if line != "" || w.Count() != 0 {
		t.Fatalf("雑音が書き込まれた: %q", line)
	}
}

func TestCloseWritesFooterAndFlushes(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 10, 16, 9, 30, 0, 0, time.Local)
	w, err := New(dir, start)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(time.Second, 2*time.Second, "テスト発話"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// 二重 Close で落ちないこと。
	if err := w.Close(start.Add(time.Minute)); err != nil {
		t.Fatalf("2 回目の Close: %v", err)
	}

	data, err := os.ReadFile(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{"# 会議文字起こし", "[00:00:01] テスト発話", "# 終了:", "(1 行)"} {
		if !strings.Contains(content, want) {
			t.Errorf("保存内容に %q がない:\n%s", want, content)
		}
	}
}

// REVIEW.md [P1] の回帰テスト。
// 時間的に離れた同一発話は別の発話として残す
// (例: 10 秒後に別の人が「はい」と答える)。
func TestRepeatedUtteranceFarApartIsKept(t *testing.T) {
	w := newTestWriter(t)
	if _, err := w.Append(time.Second, 2*time.Second, "はい"); err != nil {
		t.Fatal(err)
	}
	line, err := w.Append(12*time.Second, 13*time.Second, "はい")
	if err != nil {
		t.Fatal(err)
	}
	if line == "" {
		t.Fatal("10 秒離れた同一発話が重複として消えた")
	}
	if w.Count() != 2 {
		t.Fatalf("行数 = %d, want 2", w.Count())
	}
	if !strings.Contains(line, "00:00:12") {
		t.Fatalf("2 行目の時刻が不正: %q", line)
	}
}

// 隣接した区間の重複は 1 行に抑制する。
func TestRepeatedUtteranceAdjacentIsSuppressed(t *testing.T) {
	w := newTestWriter(t)
	if _, err := w.Append(time.Second, 2*time.Second, "はい"); err != nil {
		t.Fatal(err)
	}
	// 直前の終了 (2s) から boundaryWindow 以内に開始。
	line, err := w.Append(2500*time.Millisecond, 3*time.Second, "はい")
	if err != nil {
		t.Fatal(err)
	}
	if line != "" {
		t.Fatalf("隣接した重複が抑制されなかった: %q", line)
	}
	if w.Count() != 1 {
		t.Fatalf("行数 = %d, want 1", w.Count())
	}
}

// 離れていれば包含関係でも残す。
func TestContainedTextFarApartIsKept(t *testing.T) {
	w := newTestWriter(t)
	if _, err := w.Append(0, time.Second, "本日の会議を始めます"); err != nil {
		t.Fatal(err)
	}
	line, err := w.Append(30*time.Second, 31*time.Second, "会議")
	if err != nil {
		t.Fatal(err)
	}
	if line == "" {
		t.Fatal("30 秒離れた発話が包含判定で消えた")
	}
}

// REVIEW.md [P1] の回帰テスト。
// 同じ秒に開始した 2 回の実行が同じファイルを共有しないこと。
func TestSameSecondStartsGetSeparateFiles(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 10, 16, 10, 0, 0, 0, time.Local)

	w1, err := New(dir, start)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w1.Append(time.Second, 2*time.Second, "1 回目の会議"); err != nil {
		t.Fatal(err)
	}
	if err := w1.Close(start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	w2, err := New(dir, start) // 同じ秒に再起動
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w2.Append(time.Second, 2*time.Second, "2 回目の会議"); err != nil {
		t.Fatal(err)
	}
	if err := w2.Close(start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if w1.Path() == w2.Path() {
		t.Fatalf("同じファイルに追記された: %s", w1.Path())
	}
	for _, tc := range []struct {
		w    *Writer
		want string
		deny string
	}{
		{w1, "1 回目の会議", "2 回目の会議"},
		{w2, "2 回目の会議", "1 回目の会議"},
	} {
		data, err := os.ReadFile(tc.w.Path())
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		if !strings.Contains(content, tc.want) {
			t.Errorf("%s に %q が無い", tc.w.Path(), tc.want)
		}
		if strings.Contains(content, tc.deny) {
			t.Errorf("%s に別の会議の内容 %q が混ざった", tc.w.Path(), tc.deny)
		}
		// ヘッダが 1 つだけであること。
		if n := strings.Count(content, "# 会議文字起こし"); n != 1 {
			t.Errorf("%s のヘッダ数 = %d, want 1", tc.w.Path(), n)
		}
	}
}
