// Package session は録音セッションの所在をローカルに記録する。
//
// AI (MCP クライアント) が再起動しても同じ会議を見つけられるよう、
// 会議 ID・プロセス状態・pane role・絶対パス・会議ボードの反映時刻を残す。
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// State は録音セッションの状態。
type State string

const (
	// StateStarting は pane を開いて sokki を起動した直後。
	StateStarting State = "starting"
	// StateLoading はモデル読み込み中 (まだ文字起こしは始まっていない)。
	StateLoading State = "loading"
	// StateRecording は録音・文字起こし中。
	StateRecording State = "recording"
	// StateStopped は正常終了済み。
	StateStopped State = "stopped"
	// StateFailed は起動または実行に失敗した。
	StateFailed State = "failed"
)

// Record は 1 つの録音セッションの記録。
type Record struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"`
	State     State     `json:"state"`
	PaneRole  string    `json:"pane_role"`
	StartedAt time.Time `json:"started_at"`

	// MuxSession は Zellij / tmux のセッション識別子。
	// 別のセッションの録音へ誤って接続・停止しないために使う。
	MuxSession string `json:"mux_session,omitempty"`

	// BoardFormat は会議ボードの形式。
	// 旧形式 (固定分類) の録音プロセスへ新形式を送らないために使う。
	// 空の場合は旧形式とみなす。
	BoardFormat string `json:"board_format,omitempty"`

	// 絶対パスで保持する (専用 pane の作業ディレクトリに左右されない)。
	TranscriptPath string `json:"transcript_path"`
	BoardPath      string `json:"board_path"`
	HistoryPath    string `json:"history_path"`
	SocketPath     string `json:"socket_path"`

	// 会議ボードの反映状況。
	SummaryVersion int       `json:"summary_version"`
	SummaryAt      time.Time `json:"summary_at,omitempty"`

	// AutoSummary は自動要約ワーカーが実際に動いているか。
	AutoSummary bool `json:"auto_summary"`
	// SummaryNote は自動要約が動いていない理由 (動いていれば空)。
	SummaryNote string `json:"summary_note,omitempty"`

	// FinalSummary は終了時の最終要約の結果 (done / failed / timeout)。
	FinalSummary string `json:"final_summary,omitempty"`

	// Error は起動・実行に失敗した理由 (AI へ返す)。
	Error string `json:"error,omitempty"`
}

// Active は録音を継続中とみなせるかを返す。
func (r *Record) Active() bool {
	if r == nil {
		return false
	}
	switch r.State {
	case StateStarting, StateLoading, StateRecording:
		return ProcessAlive(r.PID)
	default:
		return false
	}
}

// ProcessAlive は PID のプロセスが生きているかを返す。
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// Dir は記録の保存先ディレクトリを返す。
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "sokki", "sessions"), nil
}

// dirOverride はテスト用の差し替え先。
var dirOverride string

// SetDirForTest は記録先を差し替える (テスト専用)。
func SetDirForTest(dir string) { dirOverride = dir }

func resolveDir() (string, error) {
	if dirOverride != "" {
		return dirOverride, nil
	}
	return Dir()
}

func recordPath(dir, id string) string {
	return filepath.Join(dir, id+".json")
}

// maxSocketPath は Unix domain socket のパス長の上限 (macOS は 104 byte)。
// 余裕を見て少し小さい値で判定する。
const maxSocketPath = 100

// SocketPathFor は会議 ID に対応する IPC ソケットのパスを返す。
//
// 記録ディレクトリ配下が長すぎる場合 (ホームのパスが長い環境など) は
// bind に失敗するため、一時ディレクトリ配下の短いパスへ退避する。
// 実際のパスはセッション記録に残るので、AI は記録から参照できる。
func SocketPathFor(id string) (string, error) {
	dir, err := resolveDir()
	if err != nil {
		return "", err
	}
	if p := filepath.Join(dir, id+".sock"); len(p) <= maxSocketPath {
		return p, nil
	}
	short := filepath.Join(os.TempDir(), "sokki-"+shortHash(dir+id)+".sock")
	if len(short) > maxSocketPath {
		return "", fmt.Errorf(
			"IPC ソケットのパスが長すぎます (%d byte > %d)\n"+
				"対処: TMPDIR を短いパスに設定してください", len(short), maxSocketPath)
	}
	return short, nil
}

// shortHash は衝突しにくい短い識別子を作る。
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// Save は記録を書き込む (一時ファイル経由で置き換える)。
func Save(r *Record) error {
	dir, err := resolveDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("セッション記録のディレクトリを作成できません (%s): %w", dir, err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	path := recordPath(dir, r.ID)
	tmp, err := os.CreateTemp(dir, ".sokki-session-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Load は会議 ID の記録を読む。
func Load(id string) (*Record, error) {
	dir, err := resolveDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(recordPath(dir, id))
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("セッション記録を解釈できません (%s): %w", id, err)
	}
	return &r, nil
}

// List は記録を新しい順に返す。
func List() ([]*Record, error) {
	dir, err := resolveDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		r, err := Load(id)
		if err != nil {
			continue // 壊れた記録は無視する
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out, nil
}

// FindActive は指定した mux セッション内で録音継続中のものを 1 つ返す。
//
// 1 つの panecom セッションにつき録音中の sokki は 1 つとし、
// これが見つかれば start_or_attach は新規起動しない。
// 別の Zellij / tmux セッションの録音は対象にしない。
func FindActive(muxSession string) (*Record, error) {
	if muxSession == "" {
		// セッションを特定できない場合は自動選択しない。
		return nil, nil
	}
	records, err := List()
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		if !r.Active() {
			continue
		}
		if !r.InSession(muxSession) {
			continue
		}
		return r, nil
	}
	return nil, nil
}

// InSession はこの記録が指定の mux セッションに属するかを返す。
//
// どちらかが空 (セッションを特定できない / 古い記録) の場合は
// 一致とみなさない。別セッションの録音へ誤って接続・停止するより、
// 「特定できない」として止める方が安全なため。
func (r *Record) InSession(muxSession string) bool {
	if r == nil || muxSession == "" || r.MuxSession == "" {
		return false
	}
	return r.MuxSession == muxSession
}

// CurrentMuxSession は現在の Zellij / tmux セッション識別子を返す。
// 特定できない場合は空文字列。
func CurrentMuxSession() string {
	if s := os.Getenv("ZELLIJ_SESSION_NAME"); s != "" {
		return "zellij:" + s
	}
	if s := os.Getenv("TMUX"); s != "" {
		// "socket,pid,session" 形式。セッション単位で識別する。
		return "tmux:" + s
	}
	return ""
}

// Remove は記録とソケットを削除する。
func Remove(id string) error {
	dir, err := resolveDir()
	if err != nil {
		return err
	}
	if sock, serr := SocketPathFor(id); serr == nil {
		_ = os.Remove(sock)
	}
	_ = os.Remove(filepath.Join(dir, id+".sock"))
	err = os.Remove(recordPath(dir, id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// BoardFormatSummaryV2 は topic + 累積要約の形式。
const BoardFormatSummaryV2 = "summary-v2"

// SupportsSummaryBoard は新形式の会議ボードを扱えるかを返す。
func (r *Record) SupportsSummaryBoard() bool {
	return r != nil && r.BoardFormat == BoardFormatSummaryV2
}

// NewID は会議 ID を作る (開始時刻ベースで人が読める形)。
func NewID(start time.Time) string {
	return start.Format("20060102-150405")
}
