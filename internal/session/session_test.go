package session

import (
	"os"
	"strings"
	"testing"
	"time"
)

func useTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	SetDirForTest(dir)
	t.Cleanup(func() { SetDirForTest("") })
	return dir
}

func newRecord(id string, state State, pid int) *Record {
	return &Record{
		ID:             id,
		PID:            pid,
		State:          state,
		PaneRole:       "sokki",
		StartedAt:      time.Now(),
		TranscriptPath: "/tmp/m/" + id + ".txt",
		BoardPath:      "/tmp/m/" + id + ".md",
		SocketPath:     "/tmp/m/" + id + ".sock",
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	useTempDir(t)
	want := newRecord("m1", StateRecording, os.Getpid())
	want.SummaryVersion = 3
	want.SummaryAt = time.Now().Truncate(time.Second)

	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := Load("m1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.PaneRole != want.PaneRole ||
		got.TranscriptPath != want.TranscriptPath || got.SummaryVersion != 3 {
		t.Fatalf("読み戻した記録が違う: %+v", got)
	}
}

// 生きているプロセスの録音中セッションだけを有効とみなす。
func TestFindActiveIgnoresDeadAndStopped(t *testing.T) {
	useTempDir(t)

	const mux = "zellij:test"
	// 死んだ PID の録音中
	dead := newRecord("dead", StateRecording, 999999)
	dead.MuxSession = mux
	dead.StartedAt = time.Now().Add(-time.Hour)
	if err := Save(dead); err != nil {
		t.Fatal(err)
	}
	// 停止済み (自分の PID でも停止状態なら対象外)
	stopped := newRecord("stopped", StateStopped, os.Getpid())
	stopped.MuxSession = mux
	stopped.StartedAt = time.Now().Add(-30 * time.Minute)
	if err := Save(stopped); err != nil {
		t.Fatal(err)
	}

	if got, err := FindActive(mux); err != nil || got != nil {
		t.Fatalf("死んだ/停止済みが有効扱いされた: %+v (err=%v)", got, err)
	}

	// 生きている録音中
	alive := newRecord("alive", StateRecording, os.Getpid())
	alive.MuxSession = mux
	if err := Save(alive); err != nil {
		t.Fatal(err)
	}
	got, err := FindActive(mux)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "alive" {
		t.Fatalf("録音中を見つけられない: %+v", got)
	}
}

// モデル読み込み中・起動直後も「録音中」として扱う (二重起動を防ぐ)。
func TestActiveCoversStartingAndLoading(t *testing.T) {
	useTempDir(t)
	for _, st := range []State{StateStarting, StateLoading, StateRecording} {
		r := newRecord("x", st, os.Getpid())
		if !r.Active() {
			t.Errorf("状態 %s が有効扱いされない", st)
		}
	}
	for _, st := range []State{StateStopped, StateFailed} {
		r := newRecord("x", st, os.Getpid())
		if r.Active() {
			t.Errorf("状態 %s が有効扱いされた", st)
		}
	}
}

func TestListSortsNewestFirst(t *testing.T) {
	useTempDir(t)
	old := newRecord("old", StateStopped, os.Getpid())
	old.StartedAt = time.Now().Add(-time.Hour)
	recent := newRecord("recent", StateStopped, os.Getpid())
	for _, r := range []*Record{old, recent} {
		if err := Save(r); err != nil {
			t.Fatal(err)
		}
	}
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "recent" {
		t.Fatalf("新しい順になっていない: %+v", list)
	}
}

func TestListIgnoresBrokenRecords(t *testing.T) {
	dir := useTempDir(t)
	if err := os.WriteFile(dir+"/broken.json", []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := newRecord("good", StateStopped, os.Getpid())
	if err := Save(good); err != nil {
		t.Fatal(err)
	}
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "good" {
		t.Fatalf("壊れた記録を無視できていない: %+v", list)
	}
}

func TestListEmptyWhenNoDir(t *testing.T) {
	SetDirForTest(t.TempDir() + "/missing")
	t.Cleanup(func() { SetDirForTest("") })
	list, err := List()
	if err != nil {
		t.Fatalf("存在しないディレクトリでエラー: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("空でない: %+v", list)
	}
}

func TestRemove(t *testing.T) {
	useTempDir(t)
	if err := Save(newRecord("gone", StateStopped, os.Getpid())); err != nil {
		t.Fatal(err)
	}
	if err := Remove("gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("gone"); err == nil {
		t.Fatal("削除されていない")
	}
	// 二重削除でもエラーにしない。
	if err := Remove("gone"); err != nil {
		t.Fatalf("二重削除でエラー: %v", err)
	}
}

// ソケットのパスが長くなりすぎないこと (bind の制限)。
func TestSocketPathStaysWithinLimit(t *testing.T) {
	SetDirForTest(t.TempDir())
	t.Cleanup(func() { SetDirForTest("") })
	p, err := SocketPathFor("20261016-150405")
	if err != nil {
		t.Fatal(err)
	}
	if len(p) > maxSocketPath {
		t.Fatalf("ソケットのパスが長すぎる (%d): %s", len(p), p)
	}
	if !strings.HasSuffix(p, ".sock") {
		t.Errorf("拡張子が .sock でない: %s", p)
	}
}

// 記録ディレクトリが極端に長い場合は短いパスへ退避すること。
func TestSocketPathFallsBackWhenDirTooLong(t *testing.T) {
	long := t.TempDir() + "/" + strings.Repeat("a", 90)
	SetDirForTest(long)
	t.Cleanup(func() { SetDirForTest("") })

	p, err := SocketPathFor("20261016-150405")
	if err != nil {
		t.Fatal(err)
	}
	if len(p) > maxSocketPath {
		t.Fatalf("退避後もパスが長すぎる (%d): %s", len(p), p)
	}
	if strings.HasPrefix(p, long) {
		t.Errorf("長いディレクトリのままになっている: %s", p)
	}
}

func TestNewIDIsReadable(t *testing.T) {
	id := NewID(time.Date(2026, 10, 16, 15, 4, 5, 0, time.Local))
	if id != "20261016-150405" {
		t.Fatalf("会議 ID = %q", id)
	}
}

func TestProcessAliveRejectsInvalid(t *testing.T) {
	if ProcessAlive(0) || ProcessAlive(-1) {
		t.Error("不正な PID が生存扱いされた")
	}
	if !ProcessAlive(os.Getpid()) {
		t.Error("自分自身が生存扱いされない")
	}
}

// --- mux セッションの分離 ---

// 別の Zellij / tmux セッションの録音には接続しないこと。
func TestFindActiveIsScopedToMuxSession(t *testing.T) {
	useTempDir(t)

	a := newRecord("in-a", StateRecording, os.Getpid())
	a.MuxSession = "zellij:A"
	b := newRecord("in-b", StateRecording, os.Getpid())
	b.MuxSession = "zellij:B"
	b.StartedAt = a.StartedAt.Add(time.Minute) // B の方が新しい
	for _, r := range []*Record{a, b} {
		if err := Save(r); err != nil {
			t.Fatal(err)
		}
	}

	gotA, err := FindActive("zellij:A")
	if err != nil {
		t.Fatal(err)
	}
	if gotA == nil || gotA.ID != "in-a" {
		t.Fatalf("セッション A で A の録音を返さない: %+v", gotA)
	}
	gotB, err := FindActive("zellij:B")
	if err != nil {
		t.Fatal(err)
	}
	if gotB == nil || gotB.ID != "in-b" {
		t.Fatalf("セッション B で B の録音を返さない: %+v", gotB)
	}
	// 無関係なセッションでは何も返さない。
	gotC, err := FindActive("zellij:C")
	if err != nil {
		t.Fatal(err)
	}
	if gotC != nil {
		t.Fatalf("別セッションの録音に接続した: %+v", gotC)
	}
}

func TestInSessionMatching(t *testing.T) {
	r := &Record{MuxSession: "zellij:A"}
	if !r.InSession("zellij:A") {
		t.Error("同一セッションが一致しない")
	}
	if r.InSession("zellij:B") {
		t.Error("別セッションが一致した")
	}
	// セッションを特定できない場合は一致とみなさない
	// (別セッションの録音を誤って操作しないため)。
	if r.InSession("") {
		t.Error("セッション未特定なのに一致した")
	}
	// 記録側が空なら、セッション指定時は対象外。
	empty := &Record{}
	if empty.InSession("zellij:A") {
		t.Error("セッション不明の記録が一致した")
	}
}

func TestCurrentMuxSession(t *testing.T) {
	t.Setenv("ZELLIJ_SESSION_NAME", "my-session")
	t.Setenv("TMUX", "")
	if got := CurrentMuxSession(); got != "zellij:my-session" {
		t.Errorf("zellij 判定 = %q", got)
	}
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	t.Setenv("TMUX", "/tmp/tmux-501/default,123,0")
	if got := CurrentMuxSession(); got != "tmux:/tmp/tmux-501/default,123,0" {
		t.Errorf("tmux 判定 = %q", got)
	}
	t.Setenv("TMUX", "")
	if got := CurrentMuxSession(); got != "" {
		t.Errorf("セッション外で値が返った = %q", got)
	}
}

// mux セッションを特定できない場合は会議を自動選択しないこと。
func TestFindActiveRefusesWithoutMuxSession(t *testing.T) {
	useTempDir(t)
	r := newRecord("somewhere", StateRecording, os.Getpid())
	r.MuxSession = "zellij:A"
	if err := Save(r); err != nil {
		t.Fatal(err)
	}
	got, err := FindActive("")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("セッション不明なのに会議を選んだ: %+v", got)
	}
}

// 記録側に mux が無い場合も自動選択しないこと。
func TestFindActiveSkipsRecordsWithoutMux(t *testing.T) {
	useTempDir(t)
	r := newRecord("legacy", StateRecording, os.Getpid()) // MuxSession 未設定
	if err := Save(r); err != nil {
		t.Fatal(err)
	}
	got, err := FindActive("zellij:A")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("セッション不明の記録を選んだ: %+v", got)
	}
}
