package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/konono/sokki/internal/board"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 本体と sidecar をまとめて同じベース名へ移すこと。
func TestRenameWithTitleMovesAllFiles(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "2026-11-19_100000.txt")
	writeFile(t, txt, "本文")
	writeFile(t, filepath.Join(dir, "2026-11-19_100000.md"), "md")
	writeFile(t, filepath.Join(dir, "2026-11-19_100000.history.md"), "h")
	writeFile(t, filepath.Join(dir, ".2026-11-19_100000.board.json"), "{}")
	writeFile(t, filepath.Join(dir, "2026-11-19_100000.ja.txt"), "ja")

	got, err := renameWithTitle(txt, "予算 見直し")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "2026-11-19_100000_予算_見直し.txt")
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	for _, name := range []string{
		"2026-11-19_100000_予算_見直し.txt",
		"2026-11-19_100000_予算_見直し.md",
		"2026-11-19_100000_予算_見直し.history.md",
		".2026-11-19_100000_予算_見直し.board.json",
		"2026-11-19_100000_予算_見直し.ja.txt",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s が無い: %v", name, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 5 {
		t.Errorf("旧ファイルが残っている: %d 件", len(entries))
	}
}

// 既存ファイルを上書きせず連番を付けること。
func TestRenameWithTitleAvoidsCollision(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "a.txt")
	writeFile(t, txt, "新")
	existing := filepath.Join(dir, "a_定例.txt")
	writeFile(t, existing, "既存")

	got, err := renameWithTitle(txt, "定例")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "a_定例-2.txt") {
		t.Fatalf("got %s", got)
	}
	if b, _ := os.ReadFile(existing); string(b) != "既存" {
		t.Fatal("既存ファイルを上書きした")
	}
}

// 使える文字が無いタイトルでは何もしないこと。
func TestRenameWithTitleEmptySlug(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "a.txt")
	writeFile(t, txt, "x")
	got, err := renameWithTitle(txt, " / ")
	if err != nil || got != txt {
		t.Fatalf("got %s, %v", got, err)
	}
}

// setupMeeting は本体と sidecar を作り、本体パスを返す。
func setupMeeting(t *testing.T, dir string) string {
	t.Helper()
	txt := filepath.Join(dir, "m.txt")
	writeFile(t, txt, "本文")
	writeFile(t, filepath.Join(dir, "m.md"), "md")
	writeFile(t, filepath.Join(dir, ".m.board.json"), "{}")
	return txt
}

func swapFS(t *testing.T, link func(string, string) error, remove func(string) error) {
	t.Helper()
	oldLink, oldRemove := linkFile, removeFile
	if link != nil {
		linkFile = link
	}
	if remove != nil {
		removeFile = remove
	}
	t.Cleanup(func() { linkFile, removeFile = oldLink, oldRemove })
}

// 事前確認の後に宛先が作られても上書きせず、次の連番へ進むこと。
func TestRenameWithTitleRaceCreatedDestination(t *testing.T) {
	dir := t.TempDir()
	txt := setupMeeting(t, dir)
	intruder := filepath.Join(dir, "m_定例.md")
	swapFS(t, func(oldname, newname string) error {
		if newname == intruder {
			// 事前確認と link の間に別プロセスが作った状況。
			if _, err := os.Stat(intruder); os.IsNotExist(err) {
				writeFile(t, intruder, "他人")
			}
		}
		return os.Link(oldname, newname)
	}, nil)

	got, err := renameWithTitle(txt, "定例")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "m_定例-2.txt") {
		t.Fatalf("got %s", got)
	}
	if b, _ := os.ReadFile(intruder); string(b) != "他人" {
		t.Fatal("後から作られた宛先を上書きした")
	}
	if _, err := os.Stat(filepath.Join(dir, "m_定例.txt")); !os.IsNotExist(err) {
		t.Fatal("衝突した連番のリンクが残っている")
	}
	if _, err := os.Stat(filepath.Join(dir, "m_定例-2.md")); err != nil {
		t.Fatal("sidecar が移っていない")
	}
}

// 途中の link が失敗したら元のファイルを無傷のまま残し、新しい名前を残さないこと。
func TestRenameWithTitleLinkFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	txt := setupMeeting(t, dir)
	swapFS(t, func(oldname, newname string) error {
		if filepath.Ext(newname) == ".md" {
			return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: os.ErrPermission}
		}
		return os.Link(oldname, newname)
	}, nil)

	got, err := renameWithTitle(txt, "定例")
	if err == nil {
		t.Fatal("失敗が報告されない")
	}
	if got != txt {
		t.Fatalf("元のパスを返さない: %s", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Fatalf("元の 3 ファイルだけが残るべき: %d 件", len(entries))
	}
}

// 戻しの削除に失敗したら、残った名前を報告すること。
func TestRenameWithTitleReportsCleanupFailure(t *testing.T) {
	dir := t.TempDir()
	txt := setupMeeting(t, dir)
	stuck := filepath.Join(dir, "m_定例.txt")
	swapFS(t, func(oldname, newname string) error {
		if filepath.Ext(newname) == ".md" {
			return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: os.ErrPermission}
		}
		return os.Link(oldname, newname)
	}, func(name string) error {
		if name == stuck {
			return &os.PathError{Op: "remove", Path: name, Err: os.ErrPermission}
		}
		return os.Remove(name)
	})

	got, err := renameWithTitle(txt, "定例")
	if err == nil || !strings.Contains(err.Error(), stuck) {
		t.Fatalf("残った名前が報告されない: %v", err)
	}
	if got != txt {
		t.Fatalf("元のパスを返さない: %s", got)
	}
	if b, _ := os.ReadFile(txt); string(b) != "本文" {
		t.Fatal("元のファイルが失われた")
	}
}

// 元の名前を消せなかった場合は新パスを採用し、残った名前を報告すること。
func TestRenameWithTitleReportsLeftoverOriginal(t *testing.T) {
	dir := t.TempDir()
	txt := setupMeeting(t, dir)
	swapFS(t, nil, func(name string) error {
		if name == txt {
			return &os.PathError{Op: "remove", Path: name, Err: os.ErrPermission}
		}
		return os.Remove(name)
	})

	got, err := renameWithTitle(txt, "定例")
	if err == nil || !strings.Contains(err.Error(), txt) {
		t.Fatalf("残った元の名前が報告されない: %v", err)
	}
	want := filepath.Join(dir, "m_定例.txt")
	if got != want {
		t.Fatalf("新パスを返さない: %s", got)
	}
	for _, name := range []string{"m_定例.txt", "m_定例.md", ".m_定例.board.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s が無い", name)
		}
	}
}

// タイトル読み取り直前に公開が割り込んでも、最終タイトルとファイル名が一致すること。
func TestRenameByTitleUsesLatestTitle(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "m.txt")
	writeFile(t, txt, "本文")
	boards := board.NewStore(txt)
	if _, err := boards.Publish(board.Board{Title: "古い題", Summary: "・a"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	lv := newLive("id", "", txt, time.Now(), boards, nil)

	beforeRenameHook = func() {
		if _, err := boards.Publish(board.Board{Title: "新しい題", Summary: "・b"}, time.Now()); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeRenameHook = nil })

	got, err := lv.renameByTitle()
	if err != nil {
		t.Fatal(err)
	}
	if title := boards.Current().Title; got != filepath.Join(dir, "m_"+title+".txt") {
		t.Fatalf("タイトル %q とファイル名 %s がずれた", title, got)
	}
	if boards.MarkdownPath() != filepath.Join(dir, "m_新しい題.md") {
		t.Fatalf("会議ボードの保存先が追従しない: %s", boards.MarkdownPath())
	}
}
