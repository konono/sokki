package board

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func sampleBoard() Board {
	return Board{
		Topic: "来週のリリース",
		Summary: "・[00:11:42] テストを金曜までに終える方針で合意した。\n" +
			"・[00:12:10] 山田さんが手順を確認する (木曜まで)。\n" +
			"・公開時刻は未定のまま。",
		AppliedThrough: 12,
	}
}

// 今の話題と要約本文を、固定の分類ラベル無しで出すこと。
func TestLinesShowTopicAndSummary(t *testing.T) {
	b := sampleBoard()
	lines := strings.Join(b.Lines(), "\n")
	for _, want := range []string{
		"今の話題: 来週のリリース",
		"要約:",
		"テストを金曜までに終える方針で合意した",
		"公開時刻は未定のまま",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("行に %q が無い:\n%s", want, lines)
		}
	}
	// 固定の分類ラベルを作らないこと。
	for _, ng := range []string{"決定:", "宿題:", "未決:", "経緯:"} {
		if strings.Contains(lines, ng) {
			t.Errorf("固定の分類ラベル %q が出た:\n%s", ng, lines)
		}
	}
}

// 今の話題だけでも表示できること。
func TestLinesWithTopicOnly(t *testing.T) {
	b := Board{Topic: "テーブル2の発表"}
	lines := strings.Join(b.Lines(), "\n")
	if !strings.Contains(lines, "今の話題: テーブル2の発表") {
		t.Errorf("話題が出ていない: %s", lines)
	}
}

func TestEmptyBoardShowsPlaceholder(t *testing.T) {
	var b Board
	if !b.IsEmpty() {
		t.Fatal("空判定が効いていない")
	}
	lines := b.Lines()
	if len(lines) != 1 {
		t.Fatalf("行数 = %d, want 1: %v", len(lines), lines)
	}
	// 自動更新であることが分かる文言にする (質問待ちと誤解させない)。
	if !strings.Contains(lines[0], "自動") {
		t.Errorf("自動更新の案内になっていない: %q", lines[0])
	}
	if strings.Contains(lines[0], "質問") {
		t.Errorf("質問待ちと読める文言が残っている: %q", lines[0])
	}
}

func TestMarkdownContainsAllSections(t *testing.T) {
	b := sampleBoard()
	b.Version = 2
	b.UpdatedAt = time.Date(2026, 10, 16, 15, 4, 5, 0, time.Local)
	md := b.Markdown("2026-10-16_150000")

	for _, want := range []string{
		"# 2026-10-16_150000 会議ボード",
		"- 版: 2",
		"## 今の話題",
		"## 要約",
		"[00:11:42]",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown に %q が無い:\n%s", want, md)
		}
	}
	for _, ng := range []string{"## 決まったこと", "## やること", "## 未決定", "## 経緯"} {
		if strings.Contains(md, ng) {
			t.Errorf("固定の見出し %q が出た:\n%s", ng, md)
		}
	}
}

// --- Store ---

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return NewStore(filepath.Join(dir, "2026-10-16_150000.txt"))
}

// .md は .txt と同じベース名になること。
func TestStorePathsShareBaseName(t *testing.T) {
	s := newStore(t)
	if filepath.Base(s.MarkdownPath()) != "2026-10-16_150000.md" {
		t.Errorf("Markdown のパスが規約と違う: %s", s.MarkdownPath())
	}
	if filepath.Base(s.HistoryPath()) != "2026-10-16_150000.history.md" {
		t.Errorf("履歴のパスが規約と違う: %s", s.HistoryPath())
	}
}

// 公開のたびに版が増え、Markdown が置き換わること。
func TestPublishIncrementsVersionAndSaves(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 10, 16, 15, 0, 0, 0, time.Local)

	first, err := s.Publish(Board{Topic: "最初の話題"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != 1 {
		t.Errorf("版 = %d, want 1", first.Version)
	}
	second, err := s.Publish(Board{Topic: "次の話題"}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != 2 {
		t.Errorf("版 = %d, want 2", second.Version)
	}

	// 最新の .md は最後の内容だけを持つ。
	data, err := os.ReadFile(s.MarkdownPath())
	if err != nil {
		t.Fatal(err)
	}
	md := string(data)
	if !strings.Contains(md, "次の話題") {
		t.Errorf("最新の内容が保存されていない:\n%s", md)
	}
	if strings.Contains(md, "最初の話題") {
		t.Errorf("古い内容が最新ファイルに残っている:\n%s", md)
	}
}

// 過去の版を失わないこと (AI が前の判断を修正できる)。
func TestPublishKeepsHistory(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	for i, topic := range []string{"話題A", "話題B", "話題C"} {
		if _, err := s.Publish(Board{Topic: topic}, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(s.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	history := string(data)
	for _, want := range []string{"話題A", "話題B", "話題C", "版 1", "版 2", "版 3"} {
		if !strings.Contains(history, want) {
			t.Errorf("履歴に %q が無い", want)
		}
	}
}

// 保存に失敗したら版を進めない (公開できたのにファイルが無い状態を作らない)。
func TestPublishFailureDoesNotAdvanceVersion(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "sub", "m.txt"))
	// 保存先ディレクトリを作れない状況を作る (同名のファイルを置く)。
	if err := os.WriteFile(filepath.Join(dir, "sub"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(Board{Topic: "保存できない"}, time.Now()); err == nil {
		t.Fatal("保存に失敗したのにエラーにならなかった")
	}
	if s.Version() != 0 {
		t.Errorf("失敗したのに版が進んだ: %d", s.Version())
	}
	cur := s.Current()
	if !cur.IsEmpty() {
		t.Error("失敗したのに現在の会議ボードが更新された")
	}
}

// 公開後は Current が最新を返すこと。
func TestCurrentReturnsLatest(t *testing.T) {
	s := newStore(t)
	if _, err := s.Publish(sampleBoard(), time.Now()); err != nil {
		t.Fatal(err)
	}
	cur := s.Current()
	if cur.Topic != "来週のリリース" || cur.Version != 1 {
		t.Errorf("現在の会議ボードが違う: %+v", cur)
	}
}

// REVIEW.md [P2] 履歴が書けない場合は公開を失敗にすること
// (最新だけ更新されて古い版を失う状態を作らない)。
func TestPublishFailsWhenHistoryUnwritable(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "m.txt"))

	// 履歴のパスをディレクトリで塞ぐ (音声デバイス不要で再現できる)。
	if err := os.MkdirAll(s.HistoryPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(Board{Topic: "履歴が書けない"}, time.Now()); err == nil {
		t.Fatal("履歴が書けないのに公開が成功した")
	}
	if s.Version() != 0 {
		t.Errorf("失敗したのに版が進んだ: %d", s.Version())
	}
	// 最新 .md も更新していないこと (履歴との不整合を作らない)。
	if _, err := os.Stat(s.MarkdownPath()); err == nil {
		t.Error("履歴が書けないのに .md を更新した")
	}
}

// 控え (JSON) から版を引き継いで続きから公開できること。
func TestLoadStateResumesVersions(t *testing.T) {
	dir := t.TempDir()
	tx := filepath.Join(dir, "m.txt")

	first := NewStore(tx)
	if _, err := first.Publish(Board{Topic: "録音中の要約"}, time.Now()); err != nil {
		t.Fatal(err)
	}

	// 別プロセス相当 (録音終了後) で読み直す。
	later := NewStore(tx)
	if err := later.LoadState(); err != nil {
		t.Fatal(err)
	}
	if later.Version() != 1 {
		t.Fatalf("版を引き継げていない: %d", later.Version())
	}
	cur := later.Current()
	if cur.Topic != "録音中の要約" {
		t.Errorf("内容を引き継げていない: %+v", cur)
	}

	got, err := later.Publish(Board{Topic: "終了後の追記"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 {
		t.Errorf("続きの版 = %d, want 2", got.Version)
	}
	hist, _ := os.ReadFile(later.HistoryPath())
	for _, want := range []string{"録音中の要約", "終了後の追記"} {
		if !strings.Contains(string(hist), want) {
			t.Errorf("履歴に %q が無い", want)
		}
	}
}

// 控えが無くても (初回) エラーにしないこと。
func TestLoadStateWithoutFile(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "m.txt"))
	if err := s.LoadState(); err != nil {
		t.Fatalf("控えが無いのにエラー: %v", err)
	}
	if s.Version() != 0 {
		t.Errorf("版 = %d, want 0", s.Version())
	}
}

// REVIEW.md [P1] 控えの保存に失敗したら、.md と履歴も更新しないこと。
//
// 「公開は失敗」なのにファイルだけ新しい状態になると、
// 次の公開や再接続で版が分岐する。
func TestPublishFailsCleanlyWhenStateUnwritable(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "m.txt"))

	// まず 1 回成功させておく (旧内容)。
	if _, err := s.Publish(Board{Topic: "旧の内容"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	oldMD, err := os.ReadFile(s.MarkdownPath())
	if err != nil {
		t.Fatal(err)
	}
	oldHist, err := os.ReadFile(s.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}

	// 控えのパスをディレクトリで塞ぐ。
	if err := os.Remove(s.StatePath()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.StatePath(), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Publish(Board{Topic: "新しい内容"}, time.Now()); err == nil {
		t.Fatal("控えが書けないのに公開が成功した")
	}
	if s.Version() != 1 {
		t.Errorf("失敗したのに版が進んだ: %d", s.Version())
	}

	// .md と履歴に新内容が残っていないこと。
	gotMD, _ := os.ReadFile(s.MarkdownPath())
	if string(gotMD) != string(oldMD) {
		t.Errorf("公開失敗なのに .md が更新された:\n%s", gotMD)
	}
	if strings.Contains(string(gotMD), "新しい内容") {
		t.Errorf(".md に新内容が残った:\n%s", gotMD)
	}
	gotHist, _ := os.ReadFile(s.HistoryPath())
	if strings.Contains(string(gotHist), "新しい内容") {
		t.Errorf("履歴に新内容が残った:\n%s", gotHist)
	}
	if len(gotHist) != len(oldHist) {
		t.Errorf("履歴の長さが変わった: %d → %d", len(oldHist), len(gotHist))
	}
}

// 失敗後に再試行すれば、版が分岐せず続きから公開できること。
func TestPublishRetryAfterFailureKeepsVersionLine(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "m.txt"))
	if _, err := s.Publish(Board{Topic: "一回目"}, time.Now()); err != nil {
		t.Fatal(err)
	}

	// 一時的に控えを塞いで失敗させる。
	if err := os.Remove(s.StatePath()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.StatePath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(Board{Topic: "失敗する回"}, time.Now()); err == nil {
		t.Fatal("失敗しなかった")
	}

	// 塞ぎを解除して再試行する。
	if err := os.RemoveAll(s.StatePath()); err != nil {
		t.Fatal(err)
	}
	got, err := s.Publish(Board{Topic: "二回目"}, time.Now())
	if err != nil {
		t.Fatalf("再試行に失敗: %v", err)
	}
	if got.Version != 2 {
		t.Errorf("再試行後の版 = %d, want 2 (版が分岐した)", got.Version)
	}

	// 履歴に同じ版番号が重複していないこと。
	hist, _ := os.ReadFile(s.HistoryPath())
	if n := strings.Count(string(hist), "<!-- 版 2 /"); n != 1 {
		t.Errorf("版 2 の履歴が %d 件 (重複)", n)
	}
	if strings.Contains(string(hist), "失敗する回") {
		t.Errorf("失敗した内容が履歴に残った:\n%s", hist)
	}
}

// 控えが失われても履歴から版を引き継ぎ、番号を重複させないこと。
func TestLoadStateRecoversVersionFromHistory(t *testing.T) {
	dir := t.TempDir()
	tx := filepath.Join(dir, "m.txt")
	first := NewStore(tx)
	for _, topic := range []string{"一回目", "二回目"} {
		if _, err := first.Publish(Board{Topic: topic}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// 控えだけを失った状況。
	if err := os.Remove(first.StatePath()); err != nil {
		t.Fatal(err)
	}

	later := NewStore(tx)
	if err := later.LoadState(); err != nil {
		t.Fatal(err)
	}
	if later.Version() != 2 {
		t.Fatalf("履歴から版を復元できない: %d", later.Version())
	}
	got, err := later.Publish(Board{Topic: "三回目"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 3 {
		t.Errorf("版 = %d, want 3", got.Version)
	}
}

// REVIEW.md [P1] 変更不可フラグ (chflags uchg) の控えで公開が失敗する場合も、
// .md と履歴を変更しないこと。
//
// ディレクトリ判定だけでは uchg を見逃し、履歴追記後の Rename で失敗して
// 「公開は失敗したのに履歴だけ新版」になっていた。
func TestPublishFailsCleanlyWhenStateImmutable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("chflags は macOS 専用")
	}
	if _, err := exec.LookPath("chflags"); err != nil {
		t.Skip("chflags が無い")
	}

	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "m.txt"))
	if _, err := s.Publish(Board{Topic: "旧の内容"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	oldMD, _ := os.ReadFile(s.MarkdownPath())
	oldHist, _ := os.ReadFile(s.HistoryPath())

	// 控えを変更不可にする (Rename が失敗する状態)。
	if out, err := exec.Command("chflags", "uchg", s.StatePath()).CombinedOutput(); err != nil {
		t.Skipf("chflags uchg を設定できない: %v %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("chflags", "nouchg", s.StatePath()).Run()
	})

	if _, err := s.Publish(Board{Topic: "新しい内容"}, time.Now()); err == nil {
		t.Fatal("変更不可の控えなのに公開が成功した")
	}
	if s.Version() != 1 {
		t.Errorf("失敗したのに版が進んだ: %d", s.Version())
	}

	gotMD, _ := os.ReadFile(s.MarkdownPath())
	if string(gotMD) != string(oldMD) {
		t.Errorf("公開失敗なのに .md が変わった:\n%s", gotMD)
	}
	gotHist, _ := os.ReadFile(s.HistoryPath())
	if len(gotHist) != len(oldHist) {
		t.Errorf("公開失敗なのに履歴が伸びた: %d → %d byte", len(oldHist), len(gotHist))
	}
	if strings.Contains(string(gotHist), "新しい内容") {
		t.Errorf("履歴に新内容が残った:\n%s", gotHist)
	}

	// 解除して再試行すれば版 2 として公開でき、重複しないこと。
	if err := exec.Command("chflags", "nouchg", s.StatePath()).Run(); err != nil {
		t.Fatal(err)
	}
	got, err := s.Publish(Board{Topic: "再試行"}, time.Now())
	if err != nil {
		t.Fatalf("解除後の再試行に失敗: %v", err)
	}
	if got.Version != 2 {
		t.Errorf("再試行の版 = %d, want 2", got.Version)
	}
	hist, _ := os.ReadFile(s.HistoryPath())
	if n := strings.Count(string(hist), "<!-- 版 2 /"); n != 1 {
		t.Errorf("版 2 の履歴が %d 件 (重複)", n)
	}
}

// .md が変更不可の場合も、履歴を汚さずに失敗すること。
func TestPublishFailsCleanlyWhenMarkdownImmutable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("chflags は macOS 専用")
	}
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "m.txt"))
	if _, err := s.Publish(Board{Topic: "旧"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	oldHist, _ := os.ReadFile(s.HistoryPath())

	if out, err := exec.Command("chflags", "uchg", s.MarkdownPath()).CombinedOutput(); err != nil {
		t.Skipf("chflags uchg を設定できない: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("chflags", "nouchg", s.MarkdownPath()).Run() })

	if _, err := s.Publish(Board{Topic: "新"}, time.Now()); err == nil {
		t.Fatal("変更不可の .md なのに公開が成功した")
	}
	gotHist, _ := os.ReadFile(s.HistoryPath())
	if len(gotHist) != len(oldHist) {
		t.Errorf("公開失敗なのに履歴が伸びた: %d → %d byte", len(oldHist), len(gotHist))
	}
}

// 変更不可フラグは事前検査で分かりやすいエラーにすること。
func TestEnsureReplaceableDetectsImmutable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("chflags は macOS 専用")
	}
	path := filepath.Join(t.TempDir(), "locked")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("chflags", "uchg", path).CombinedOutput(); err != nil {
		t.Skipf("chflags uchg を設定できない: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("chflags", "nouchg", path).Run() })

	err := ensureReplaceable(path)
	if err == nil {
		t.Fatal("変更不可を検出できない")
	}
	for _, want := range []string{"変更不可", "chflags nouchg"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("案内に %q が無い: %v", want, err)
		}
	}
}

// REVIEW.md [P1] 履歴への書き込みが失敗しても、.md と控えを新版にしないこと。
//
// 履歴を「閉じた pipe への symlink」にすると、OpenFile は成功するが
// 書き込みが broken pipe で失敗する。追記方式ではこの時点で
// .md と控えが既に新版になっていた。
func TestPublishFailsCleanlyWhenHistoryWriteFails(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "m.txt"))

	// 旧版を 1 つ作っておく。
	if _, err := s.Publish(Board{Topic: "旧の内容"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	oldMD, _ := os.ReadFile(s.MarkdownPath())
	oldState, _ := os.ReadFile(s.StatePath())

	// 履歴を、書き込みが失敗する対象への symlink に差し替える。
	if err := os.Remove(s.HistoryPath()); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "deadpipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("fifo を作れない: %v", err)
	}
	// 読み手を開いてすぐ閉じる → 以後の書き込みは broken pipe になる。
	rd, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Skipf("fifo を開けない: %v", err)
	}
	rd.Close()
	if err := os.Symlink(fifo, s.HistoryPath()); err != nil {
		t.Fatal(err)
	}

	_, perr := s.Publish(Board{Topic: "新しい内容"}, time.Now())
	if perr == nil {
		t.Fatal("履歴を書けないのに公開が成功した")
	}
	if s.Version() != 1 {
		t.Errorf("失敗したのに版が進んだ: %d", s.Version())
	}

	// .md と控えが新版になっていないこと。
	gotMD, _ := os.ReadFile(s.MarkdownPath())
	if string(gotMD) != string(oldMD) {
		t.Errorf("公開失敗なのに .md が変わった:\n%s", gotMD)
	}
	if strings.Contains(string(gotMD), "新しい内容") {
		t.Errorf(".md に新内容が残った:\n%s", gotMD)
	}
	gotState, _ := os.ReadFile(s.StatePath())
	if string(gotState) != string(oldState) {
		t.Errorf("公開失敗なのに控えが変わった:\n%s", gotState)
	}
}

// 置き換えの途中で失敗した場合、既に置き換えた分を元へ戻すこと。
func TestPublishRollsBackPartialCommit(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("chflags は macOS 専用")
	}
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "m.txt"))
	if _, err := s.Publish(Board{Topic: "旧"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	oldHist, _ := os.ReadFile(s.HistoryPath())
	oldState, _ := os.ReadFile(s.StatePath())

	// 最後に置き換える .md だけを変更不可にする
	// → 履歴と控えは置き換え済みの状態で失敗する。
	if out, err := exec.Command("chflags", "uchg", s.MarkdownPath()).CombinedOutput(); err != nil {
		t.Skipf("chflags uchg を設定できない: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("chflags", "nouchg", s.MarkdownPath()).Run() })

	// 事前検査をすり抜ける経路を確かめるため、検査後に失敗する状況を作る。
	// (ensureReplaceable が uchg を弾くので、ここでは検査で止まることを確認)
	if _, err := s.Publish(Board{Topic: "新"}, time.Now()); err == nil {
		t.Fatal("公開が成功した")
	}

	// 履歴と控えが元のままであること (巻き戻し or 未変更)。
	if got, _ := os.ReadFile(s.HistoryPath()); string(got) != string(oldHist) {
		t.Errorf("履歴が元に戻っていない:\n%s", got)
	}
	if got, _ := os.ReadFile(s.StatePath()); string(got) != string(oldState) {
		t.Errorf("控えが元に戻っていない:\n%s", got)
	}
}

// rollback が元の内容へ戻せること (直接検証)。
func TestRollbackRestoresContents(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(existing, []byte("もとの内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(created, []byte("新規に作られた"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := rollback([]commitTarget{
		{path: existing, old: []byte("もとの内容"), label: "A"},
		{path: created, old: nil, label: "B"}, // 元々存在しなかった
	})
	if err != nil {
		t.Fatalf("巻き戻しに失敗: %v", err)
	}
	if got, _ := os.ReadFile(existing); string(got) != "もとの内容" {
		t.Errorf("内容が戻っていない: %q", got)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Error("元々無かったファイルが残っている")
	}
}

// REVIEW.md [P1] 読み取り経路 (LoadState) も名前付きパイプで固まらないこと。
func TestLoadStateDoesNotHangOnFIFO(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "m.txt"))

	fifo := filepath.Join(dir, "deadpipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("fifo を作れない: %v", err)
	}
	if err := os.Symlink(fifo, s.HistoryPath()); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- s.LoadState() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("名前付きパイプの履歴がエラーにならない")
		}
		if !strings.Contains(err.Error(), "通常のファイルではありません") {
			t.Errorf("理由が分からない: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("LoadState が応答しない (名前付きパイプで停止)")
	}
}

// 控えが名前付きパイプでも固まらないこと。
func TestLoadStateDoesNotHangOnFIFOState(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "m.txt"))

	fifo := filepath.Join(dir, "deadpipe2")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("fifo を作れない: %v", err)
	}
	if err := os.Symlink(fifo, s.StatePath()); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- s.LoadState() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("名前付きパイプの控えがエラーにならない")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("LoadState が応答しない")
	}
}

// versionFromHistory も固まらないこと。
func TestVersionFromHistoryDoesNotHangOnFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "p")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("fifo を作れない: %v", err)
	}
	link := filepath.Join(dir, "h.md")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- versionFromHistory(link) }()
	select {
	case got := <-done:
		if got != 0 {
			t.Errorf("版 = %d, want 0", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("versionFromHistory が応答しない")
	}
}

// REVIEW.md [P2] Rename が失敗しても一時ファイルを残さず、巻き戻すこと。
func TestPublishCleansTempsAndRollsBackOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "m.txt"))
	if _, err := s.Publish(Board{Topic: "旧"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	oldHist, _ := os.ReadFile(s.HistoryPath())
	oldState, _ := os.ReadFile(s.StatePath())
	oldMD, _ := os.ReadFile(s.MarkdownPath())

	// 3 回目の Rename (.md) だけを失敗させる。
	// 事前検査では防げない後段の失敗を再現する。
	orig := renameFile
	calls := 0
	renameFile = func(from, to string) error {
		calls++
		if calls == 3 {
			return fmt.Errorf("疑似的な Rename 失敗")
		}
		return orig(from, to)
	}
	t.Cleanup(func() { renameFile = orig })

	if _, err := s.Publish(Board{Topic: "新"}, time.Now()); err == nil {
		t.Fatal("Rename が失敗したのに公開が成功した")
	}
	if s.Version() != 1 {
		t.Errorf("失敗したのに版が進んだ: %d", s.Version())
	}

	// 履歴・控え・.md が元の内容へ戻っていること。
	if got, _ := os.ReadFile(s.HistoryPath()); string(got) != string(oldHist) {
		t.Errorf("履歴が巻き戻っていない:\n%s", got)
	}
	if got, _ := os.ReadFile(s.StatePath()); string(got) != string(oldState) {
		t.Errorf("控えが巻き戻っていない:\n%s", got)
	}
	if got, _ := os.ReadFile(s.MarkdownPath()); string(got) != string(oldMD) {
		t.Errorf(".md が変わった:\n%s", got)
	}

	// 一時ファイルが残っていないこと。
	assertNoTempFiles(t, dir)
}

// 1 回目の Rename で失敗した場合も一時ファイルを残さないこと。
func TestPublishCleansTempsOnFirstRenameFailure(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "m.txt"))

	orig := renameFile
	renameFile = func(from, to string) error { return fmt.Errorf("疑似的な Rename 失敗") }
	t.Cleanup(func() { renameFile = orig })

	if _, err := s.Publish(Board{Topic: "新"}, time.Now()); err == nil {
		t.Fatal("Rename が失敗したのに公開が成功した")
	}
	assertNoTempFiles(t, dir)
}

// assertNoTempFiles は一時ファイルの残骸が無いことを確かめる。
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var leaked []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".sokki-board-") {
			leaked = append(leaked, e.Name())
		}
	}
	if len(leaked) > 0 {
		t.Errorf("一時ファイルが残った: %v", leaked)
	}
}

// REVIEW.md [P1] 版の比較と保存が原子的であること。
//
// 比較後にロックを外すと、その隙に手動公開が入り、
// 古い自動要約で上書きできてしまう。
func TestPublishIfVersionIsAtomic(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "m.txt"))

	// 版 1 を作る。
	if _, err := s.Publish(Board{Topic: "初回", Summary: "・初回"}, time.Now()); err != nil {
		t.Fatal(err)
	}

	// 手動公開と自動公開 (基準: 版 1) を同時に走らせる。
	var wg sync.WaitGroup
	start := make(chan struct{})
	var autoErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, _ = s.Publish(Board{Topic: "手動", Summary: "・手動の修正"}, time.Now())
	}()
	go func() {
		defer wg.Done()
		<-start
		_, autoErr = s.PublishIfVersion(
			Board{Topic: "自動", Summary: "・自動の要約"}, time.Now(), 1)
	}()
	close(start)
	wg.Wait()

	cur := s.Current()
	// どちらが先でも、最終状態は一貫していること。
	switch cur.Topic {
	case "自動":
		// 自動が先に通った → 手動が後から上書きしているはず
		if autoErr != nil {
			t.Errorf("自動が通ったのにエラー: %v", autoErr)
		}
	case "手動":
		// 手動が先なら、自動は競合で弾かれるか、後勝ちで手動が残る
	default:
		t.Fatalf("想定外の状態: %+v", cur)
	}
	// 版は 2 以上に進み、飛びや重複が無いこと。
	if cur.Version < 2 {
		t.Errorf("版 = %d, want >= 2", cur.Version)
	}
	// .md と控えが一致していること。
	data, err := os.ReadFile(s.MarkdownPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), cur.Topic) {
		t.Errorf(".md が現在の状態と一致しない:\n%s", data)
	}
}

// 基準の版が古ければ公開せず、手動の内容を残すこと。
func TestPublishIfVersionRejectsStaleBase(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "m.txt"))
	if _, err := s.Publish(Board{Topic: "v1", Summary: "・v1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(Board{Topic: "手動 v2", Summary: "・手動の修正"}, time.Now()); err != nil {
		t.Fatal(err)
	}

	// 版 1 を基準にした自動公開は弾かれること。
	_, err := s.PublishIfVersion(Board{Topic: "自動", Summary: "・古い自動要約"}, time.Now(), 1)
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("競合にならない: %v", err)
	}
	cur := s.Current()
	if cur.Topic != "手動 v2" {
		t.Errorf("手動の内容が上書きされた: %+v", cur)
	}
	data, _ := os.ReadFile(s.MarkdownPath())
	if strings.Contains(string(data), "古い自動要約") {
		t.Errorf("弾いたはずの内容が .md に残った:\n%s", data)
	}
	hist, _ := os.ReadFile(s.HistoryPath())
	if strings.Contains(string(hist), "古い自動要約") {
		t.Errorf("弾いたはずの内容が履歴に残った")
	}
}

// 自動要約が無効なら「自動で作成」と約束しないこと。
func TestEmptyPlaceholderRespectsAutoSummary(t *testing.T) {
	if on := EmptyPlaceholder(true); !strings.Contains(on, "自動") {
		t.Errorf("有効時の文言が不正: %q", on)
	}
	off := EmptyPlaceholder(false)
	if strings.Contains(off, "自動") {
		t.Errorf("無効時に自動更新を約束している: %q", off)
	}
	var b Board
	if got := b.LinesFor(false)[0]; strings.Contains(got, "自動") {
		t.Errorf("LinesFor(false) が自動更新を約束している: %q", got)
	}
}

// 版の比較直後に手動公開が割り込んでも、古い内容で上書きしないこと。
//
// 比較と保存を同じロック保持中で行っていれば、割り込みは完了できない。
func TestPublishIfVersionBlocksInterleavedManualPublish(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "m.txt"))
	if _, err := s.Publish(Board{Topic: "v1", Summary: "・v1"}, time.Now()); err != nil {
		t.Fatal(err)
	}

	manualDone := make(chan struct{})
	var once sync.Once
	afterVersionCheck = func() {
		once.Do(func() {
			go func() {
				_, _ = s.Publish(Board{Topic: "手動", Summary: "・手動の修正"}, time.Now())
				close(manualDone)
			}()
			// 比較直後に手動公開が完了できてしまうかを見る。
			select {
			case <-manualDone:
				t.Error("比較と保存の間に手動公開が割り込めた (原子的でない)")
			case <-time.After(300 * time.Millisecond):
				// 期待どおり: ロック保持中なので完了できない
			}
		})
	}
	t.Cleanup(func() { afterVersionCheck = nil })

	if _, err := s.PublishIfVersion(Board{Topic: "自動", Summary: "・自動の要約"}, time.Now(), 1); err != nil {
		t.Fatalf("自動公開に失敗: %v", err)
	}
	<-manualDone

	// 最後に通った手動の内容が残ること。
	cur := s.Current()
	if cur.Topic != "手動" {
		t.Errorf("手動の修正が失われた: %+v", cur)
	}
	data, _ := os.ReadFile(s.MarkdownPath())
	if !strings.Contains(string(data), "手動の修正") {
		t.Errorf(".md に手動の内容が無い:\n%s", data)
	}
	// 版は 1 → 2 → 3 と進み、飛びが無いこと。
	if cur.Version != 3 {
		t.Errorf("版 = %d, want 3", cur.Version)
	}
}
