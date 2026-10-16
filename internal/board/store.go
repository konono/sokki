package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Store は会議ボードの保持と Markdown 保存を行う。
//
// 「画面反映」と「Markdown 保存」を 1 つの操作として扱う。
// 保存に失敗した場合は版を進めず、公開は成功扱いにしない
// (公開できたのにファイルに残らない状態を作らない)。
//
// 履歴の追記も公開の一部とみなす。履歴が書けない場合は
// 「古い版を失わない」約束を守れないため、公開を失敗にする。
type Store struct {
	mu sync.Mutex

	// mdPath は最新の会議ボード (.txt と同じベース名の .md)。
	mdPath string
	// historyPath は過去の版を残す追記ファイル。
	historyPath string
	// statePath は録音終了後も続きから更新できるようにする控え (JSON)。
	statePath string
	// name は見出しに使う会議名 (ベース名)。
	name string

	current Board
	version int
}

// NewStore は文字起こしのパス (.txt) に対応する Store を作る。
func NewStore(transcriptPath string) *Store {
	dir := filepath.Dir(transcriptPath)
	base := strings.TrimSuffix(filepath.Base(transcriptPath), filepath.Ext(transcriptPath))
	return &Store{
		mdPath:      filepath.Join(dir, base+".md"),
		historyPath: filepath.Join(dir, base+".history.md"),
		statePath:   filepath.Join(dir, "."+base+".board.json"),
		name:        base,
	}
}

// MarkdownPath は最新の会議ボードの保存先を返す。
func (s *Store) MarkdownPath() string { return s.mdPath }

// HistoryPath は更新履歴の保存先を返す。
func (s *Store) HistoryPath() string { return s.historyPath }

// StatePath は会議ボードの控え (JSON) のパスを返す。
func (s *Store) StatePath() string { return s.statePath }

// Current は現在の会議ボードを返す。
func (s *Store) Current() Board {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// Version は現在の版番号を返す (未公開なら 0)。
func (s *Store) Version() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// LoadState は保存済みの会議ボードを読み込む。
//
// 録音終了後に別プロセスから続きを更新する場合に使う。
// 控えが無ければ何もしない (版 0 のまま)。
func (s *Store) LoadState() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 読み取り前にも対象を検査する。名前付きパイプ等を読もうとすると
	// 応答が返らなくなるため (Publish だけでなく読み取り経路も守る)。
	for _, p := range []string{s.statePath, s.historyPath} {
		if err := ensureReadable(p); err != nil {
			return fmt.Errorf("会議ボードを読めません: %w", err)
		}
	}

	data, err := os.ReadFile(s.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			// 控えが無くても履歴があれば版を引き継ぐ
			// (同じ版番号を重複させないため)。
			s.version = versionFromHistory(s.historyPath)
			return nil
		}
		return fmt.Errorf("会議ボードの控えを読めません (%s): %w", s.statePath, err)
	}
	var b Board
	if err := json.Unmarshal(data, &b); err != nil {
		return fmt.Errorf("会議ボードの控えを解釈できません (%s): %w", s.statePath, err)
	}
	s.current = b
	// 控えと履歴のうち大きい方を採用し、版が重複しないようにする。
	s.version = b.Version
	if h := versionFromHistory(s.historyPath); h > s.version {
		s.version = h
	}
	return nil
}

// historyVersionRE は履歴の版見出しから番号を読む。
var historyVersionRE = regexp.MustCompile(`<!-- 版 (\d+) /`)

// versionFromHistory は履歴ファイルに残る最大の版番号を返す。
func versionFromHistory(path string) int {
	if err := ensureReadable(path); err != nil {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	max := 0
	for _, m := range historyVersionRE.FindAllStringSubmatch(string(data), -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > max {
			max = n
		}
	}
	return max
}

// afterVersionCheck は版の比較直後に呼ばれるテスト用フック。
var afterVersionCheck func()

// ErrVersionConflict は基準の版が古い場合に返す。
var ErrVersionConflict = errors.New("会議ボードが更新されています")

// PublishIfVersion は現在の版が base と一致する場合だけ公開する。
//
// 自動要約が材料を読んでから公開するまでの間に手動公開が入った場合、
// 古い内容で上書きしないようにする。
func (s *Store) PublishIfVersion(b Board, now time.Time, base int) (Board, error) {
	// 版の比較と保存を同じロック保持中に行う。
	// 比較後にロックを外すと、その隙に手動公開が入って
	// 古い自動要約で上書きしてしまう。
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.version != base {
		return Board{}, fmt.Errorf("%w (基準: 版 %d / 現在: 版 %d)",
			ErrVersionConflict, base, s.version)
	}
	if afterVersionCheck != nil {
		// テスト用の同期点。比較と保存の間に割り込みが入らないことを
		// 決定的に確かめるために使う (本番では nil)。
		afterVersionCheck()
	}
	return s.publishLocked(b, now)
}

// Publish は AI が作った会議ボードを採用し、Markdown に保存する。
//
// 最新 .md・履歴・控えの 3 つが揃って書けたときだけ版を進める。
//
// 途中で失敗しても「公開は失敗したのにファイルだけ新しい」状態を作らない。
// そのため 3 つとも「一時ファイルを作って置き換える」形に統一し
// (履歴も追記ではなく全文の置き換え)、置き換えの途中で失敗した場合は
// それまでに置き換えた分を元の内容へ戻す。
func (s *Store) Publish(b Board, now time.Time) (Board, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publishLocked(b, now)
}

// publishLocked は s.mu を保持した状態で公開する。
func (s *Store) publishLocked(b Board, now time.Time) (Board, error) {
	next := b
	next.Version = s.version + 1
	next.UpdatedAt = now
	md := next.Markdown(s.name)
	state, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return Board{}, err
	}

	// 1. 置き換え先が書ける状態かを先に確かめる。
	//    読み込みより前に行う (名前付きパイプ等を読もうとして固まらないため)。
	for _, p := range []string{s.historyPath, s.statePath, s.mdPath} {
		if err := ensureReplaceable(p); err != nil {
			return Board{}, fmt.Errorf("会議ボードを保存できません: %w", err)
		}
	}

	// 2. 既存の内容を読む (履歴は追記元、他は失敗時の復元用)。
	oldHistory, err := readIfExists(s.historyPath)
	if err != nil {
		return Board{}, fmt.Errorf(
			"会議ボードの履歴を読めません (%s): %w\n"+
				"対処: 古い版を失わないため公開していません。保存先を確認してください",
			s.historyPath, err)
	}
	oldMD, err := readIfExists(s.mdPath)
	if err != nil {
		return Board{}, fmt.Errorf("会議ボードを読めません (%s): %w", s.mdPath, err)
	}
	oldState, err := readIfExists(s.statePath)
	if err != nil {
		return Board{}, fmt.Errorf("会議ボードの控えを読めません (%s): %w", s.statePath, err)
	}

	entry := fmt.Sprintf("\n---\n\n<!-- 版 %d / %s -->\n\n%s",
		next.Version, next.UpdatedAt.Format("2006-01-02 15:04:05"), md)
	newHistory := append(append([]byte(nil), oldHistory...), entry...)

	// 3. 一時ファイルを用意する (まだ公開しない)。
	targets := []commitTarget{
		{path: s.historyPath, data: newHistory, old: oldHistory, label: "履歴"},
		{path: s.statePath, data: state, old: oldState, label: "控え"},
		{path: s.mdPath, data: md2bytes(md), old: oldMD, label: "会議ボード"},
	}
	for i := range targets {
		tmp, err := writeTemp(targets[i].path, targets[i].data)
		if err != nil {
			cleanupTemps(targets[:i])
			return Board{}, fmt.Errorf("%sを保存できません (%s): %w",
				targets[i].label, targets[i].path, err)
		}
		targets[i].tmp = tmp
	}
	defer cleanupTemps(targets)

	// 4. 置き換える。失敗したらそれまでの分を元に戻す。
	for i := range targets {
		// 失敗した場合、targets[i].tmp は未公開のまま残るので
		// defer の cleanupTemps で消す (tmp を空にしない)。
		if err := renameFile(targets[i].tmp, targets[i].path); err != nil {
			restoreErr := rollback(targets[:i])
			msg := fmt.Errorf("%sを保存できません (%s): %w",
				targets[i].label, targets[i].path, err)
			if restoreErr != nil {
				return Board{}, fmt.Errorf(
					"%w\nさらに元の内容へ戻せませんでした: %v\n"+
						"対処: %s と %s の内容を確認してください",
					msg, restoreErr, s.mdPath, s.historyPath)
			}
			return Board{}, msg
		}
		targets[i].tmp = "" // 置き換え済み (後片付け不要)
	}

	s.current = next
	s.version = next.Version
	return next, nil
}

// renameFile は置き換えに使う (テストで失敗を注入できるようにする)。
var renameFile = os.Rename

// commitTarget は置き換え対象 1 件。
type commitTarget struct {
	path  string
	data  []byte
	old   []byte // 元の内容 (nil なら元々存在しない)
	tmp   string
	label string
}

func md2bytes(s string) []byte { return []byte(s) }

// readIfExists はファイルを読む。存在しなければ nil, nil。
func readIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// cleanupTemps は未使用の一時ファイルを消す。
func cleanupTemps(targets []commitTarget) {
	for _, t := range targets {
		if t.tmp != "" {
			_ = os.Remove(t.tmp)
		}
	}
}

// rollback は置き換え済みのファイルを元の内容へ戻す。
func rollback(done []commitTarget) error {
	var failed []string
	for _, t := range done {
		if t.old == nil {
			// 元々無かったので消す。
			if err := os.Remove(t.path); err != nil && !os.IsNotExist(err) {
				failed = append(failed, t.path)
			}
			continue
		}
		tmp, err := writeTemp(t.path, t.old)
		if err != nil {
			failed = append(failed, t.path)
			continue
		}
		if err := renameFile(tmp, t.path); err != nil {
			_ = os.Remove(tmp)
			failed = append(failed, t.path)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("復元できなかったファイル: %s", strings.Join(failed, ", "))
	}
	return nil
}

// ensureReadable は安全に読めるパスかを確かめる。
//
// 通常ファイル以外 (シンボリックリンク・名前付きパイプ・ソケット・
// デバイス) は読もうとすると固まることがあるため拒否する。
func ensureReadable(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("%s を確認できません: %w", path, err)
	}
	if st.IsDir() {
		return fmt.Errorf("%s はディレクトリです", path)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf(
			"%s は通常のファイルではありません (%s)\n"+
				"対処: 通常のファイルに置き換えるか、保存先を変えてください",
			path, st.Mode().Type())
	}
	return nil
}

// ensureReplaceable は置き換え可能なパスかを確かめる。
//
// ディレクトリだけでなく、変更不可フラグ (macOS の chflags uchg/schg) も
// 先に弾く。これを見落とすと、履歴を書いた後の Rename で失敗し、
// 「公開は失敗したのに履歴だけ新しい」状態になる。
func ensureReplaceable(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("%s を確認できません: %w", path, err)
	}
	if st.IsDir() {
		return fmt.Errorf("%s はディレクトリです (ファイルとして保存できません)", path)
	}
	// 通常ファイル以外 (シンボリックリンク・名前付きパイプ・ソケット・
	// デバイス) は置き換え対象にしない。読み書きで固まったり、
	// 書き込み途中で失敗したりして整合を保てないため。
	if !st.Mode().IsRegular() {
		return fmt.Errorf(
			"%s は通常のファイルではありません (%s)\n"+
				"対処: 通常のファイルに置き換えるか、保存先を変えてください",
			path, st.Mode().Type())
	}
	if immutable(st) {
		return fmt.Errorf(
			"%s は変更不可に設定されています (chflags uchg 等)\n"+
				"対処: chflags nouchg %s で解除してください", path, path)
	}
	return nil
}

// writeTemp は同じディレクトリに一時ファイルを作って内容を書く。
// 置き換えは呼び出し側が Rename で行う。
func writeTemp(target string, data []byte) (string, error) {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".sokki-board-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}
