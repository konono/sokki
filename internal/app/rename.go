package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/konono/sokki/internal/board"
	"github.com/konono/sokki/internal/transcript"
)

// maxRenameTries は名前の衝突時に連番を試す上限。
const maxRenameTries = 100

// linkFile / removeFile はテストで競合や失敗を注入するための差し替え口。
var (
	linkFile   = os.Link
	removeFile = os.Remove
)

// relatedFiles は文字起こし (.txt) と同じベース名を持つ保存ファイルを返す。
// 先頭が文字起こし本体。存在しないものも含む。
func relatedFiles(txtPath string) []string {
	files := []string{txtPath}
	files = append(files, board.NewStore(txtPath).Files()...)
	// 翻訳 sidecar (translation.store と同じ命名)。
	files = append(files, strings.TrimSuffix(txtPath, ".txt")+".ja.txt")
	return files
}

// errNameTaken は宛先の名前が既に使われていることを示す (次の連番を試す)。
var errNameTaken = errors.New("宛先が既に存在します")

// renameWithTitle は会議タイトルをベース名に付けて保存ファイル一式を移す。
//
// 例: 2026-11-19_100000.txt → 2026-11-19_100000_予算見直し.txt
// .md / .history.md / 控え JSON / .ja.txt も同じベース名へ揃える。
//
// 上書きを確実に防ぐため、移動は 2 段階で行う。
//  1. 全ファイルを新しい名前へハードリンクする。link(2) は宛先があれば
//     失敗するので、確認後に作られたファイルも上書きしない。
//     1 つでも失敗したら作ったリンクだけを消す (元のファイルは無傷)。
//     宛先が使われていれば -2, -3 … の連番で再試行する。
//  2. 全リンクが揃ってから元の名前を消す。消せなかった名前が残っても
//     同じ内容への別名なので失われる内容は無い。残った名前はエラーで報告する。
//
// 戻り値は実際に使うべき文字起こしパス (失敗時は元のパス、2 の失敗時は新パス)。
func renameWithTitle(txtPath, title string) (string, error) {
	slug := transcript.TitleSlug(title)
	if slug == "" {
		return txtPath, nil
	}
	dir := filepath.Dir(txtPath)
	base := strings.TrimSuffix(filepath.Base(txtPath), ".txt")

	// 移す対象は存在するものだけ (本体は必須)。
	oldAll := relatedFiles(txtPath)
	var idx []int
	for i, p := range oldAll {
		if _, err := os.Lstat(p); err == nil {
			idx = append(idx, i)
		} else if i == 0 {
			return txtPath, fmt.Errorf("文字起こしファイルを確認できません (%s): %w", txtPath, err)
		}
	}

	for n := 1; n <= maxRenameTries; n++ {
		name := base + "_" + slug
		if n > 1 {
			name = fmt.Sprintf("%s-%d", name, n)
		}
		newTxt := filepath.Join(dir, name+".txt")
		newAll := relatedFiles(newTxt)
		if anyExists(newAll) {
			continue // 既知の衝突は試す前に避ける
		}
		err := linkAll(oldAll, newAll, idx)
		if errors.Is(err, errNameTaken) {
			continue
		}
		if err != nil {
			return txtPath, err
		}
		// 2. 元の名前を消す。
		var left []string
		for _, i := range idx {
			if err := removeFile(oldAll[i]); err != nil && !os.IsNotExist(err) {
				left = append(left, oldAll[i])
			}
		}
		if len(left) > 0 {
			return newTxt, fmt.Errorf(
				"新しい名前 (%s) へ移しましたが、元の名前を削除できませんでした: %s\n"+
					"対処: 同じ内容の別名なので、不要なら手動で削除してください",
				newTxt, strings.Join(left, ", "))
		}
		return newTxt, nil
	}
	return txtPath, fmt.Errorf("保存ファイル名が衝突し続けています (%s/%s_%s.txt)", dir, base, slug)
}

// linkAll は idx の各ファイルを新しい名前へハードリンクする。
// 失敗したら作ったリンクを消して元の状態へ戻す。宛先が既に存在した場合は
// errNameTaken を返す。戻しきれなかった場合はその名前をエラーに含める。
func linkAll(oldAll, newAll []string, idx []int) error {
	var made []string
	for _, i := range idx {
		err := linkFile(oldAll[i], newAll[i])
		if err == nil {
			made = append(made, newAll[i])
			continue
		}
		cause := err
		if errors.Is(err, os.ErrExist) {
			cause = errNameTaken
		}
		var left []string
		for _, m := range made {
			if rerr := removeFile(m); rerr != nil && !os.IsNotExist(rerr) {
				left = append(left, m)
			}
		}
		if len(left) > 0 {
			// 片付けに失敗したら連番の再試行はせず、残った名前を報告する。
			return fmt.Errorf(
				"保存ファイル名を変更できません (%s): %v\n"+
					"さらに途中で作った名前を削除できませんでした: %s\n"+
					"対処: 元のファイル (%s) は無傷です。上記の名前は同じ内容の別名なので手動で削除してください",
				newAll[i], err, strings.Join(left, ", "), oldAll[0])
		}
		if cause == errNameTaken {
			return errNameTaken
		}
		return fmt.Errorf("保存ファイル名を変更できません (%s): %w", newAll[i], err)
	}
	return nil
}

func anyExists(paths []string) bool {
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil || !os.IsNotExist(err) {
			return true
		}
	}
	return false
}
