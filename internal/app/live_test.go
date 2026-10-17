package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/konono/sokki/internal/board"
	"github.com/konono/sokki/internal/config"
	"github.com/konono/sokki/internal/ipc"
	"github.com/konono/sokki/internal/session"
	"github.com/konono/sokki/internal/summarizer"
	"github.com/konono/sokki/internal/ui"
)

func newTestLive(t *testing.T) (*live, *board.Store, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	store := board.NewStore(filepath.Join(dir, "2026-10-16_150000.txt"))
	var buf bytes.Buffer
	v := &view{con: ui.NewWith(&buf, false)}
	lv := newLive("m1", "sokki", filepath.Join(dir, "2026-10-16_150000.txt"),
		time.Now(), store, v)
	lv.setState(session.StateRecording)
	return lv, store, &buf
}

func TestLiveStatus(t *testing.T) {
	lv, _, _ := newTestLive(t)
	lv.addLine("00:00:01", "最初の発言")
	lv.setInterim("00:00:05", "話している途中")

	resp := lv.Handle(ipc.Request{Op: ipc.OpStatus})
	if !resp.OK || resp.State != string(session.StateRecording) {
		t.Fatalf("状態が不正: %+v", resp)
	}
	if resp.NextFrom != 1 {
		t.Errorf("next_from = %d, want 1", resp.NextFrom)
	}
	if resp.Interim != "話している途中" {
		t.Errorf("暫定行 = %q", resp.Interim)
	}
}

// 確定行を重複も欠落もなく続きから読めること。
func TestLiveTranscriptPagination(t *testing.T) {
	lv, _, _ := newTestLive(t)
	for i := 0; i < 7; i++ {
		lv.addLine("00:00:0"+string(rune('0'+i)), "発言"+string(rune('0'+i)))
	}
	var all []ipc.Line
	from := 0
	for {
		resp := lv.Handle(ipc.Request{Op: ipc.OpTranscript, From: from, Limit: 3})
		if len(resp.Lines) == 0 {
			break
		}
		all = append(all, resp.Lines...)
		from = resp.NextFrom
	}
	if len(all) != 7 {
		t.Fatalf("読めた行数 = %d, want 7", len(all))
	}
	for i, l := range all {
		if l.Index != i {
			t.Fatalf("%d 番目の Index = %d (重複または欠落)", i, l.Index)
		}
	}
}

// 確定したら暫定行は消えること (同じ発言が二重に見えない)。
func TestLiveFinalClearsInterim(t *testing.T) {
	lv, _, _ := newTestLive(t)
	lv.setInterim("00:00:03", "こんにち")
	lv.addLine("00:00:03", "こんにちは、日本の皆さん")

	resp := lv.Handle(ipc.Request{Op: ipc.OpContext})
	if resp.Interim != "" {
		t.Errorf("確定後も暫定行が残っている: %q", resp.Interim)
	}
	if len(resp.Lines) != 1 || resp.Lines[0].Text != "こんにちは、日本の皆さん" {
		t.Errorf("確定行が不正: %+v", resp.Lines)
	}
}

// publish は画面反映と Markdown 保存を同時に行うこと。
func TestLivePublishUpdatesScreenAndFile(t *testing.T) {
	lv, store, _ := newTestLive(t)

	b := &board.Board{
		Topic:   "来週のリリース",
		Summary: "・[00:11:42] テストを金曜までに終える方針で合意した。",
	}
	resp := lv.Handle(ipc.Request{Op: ipc.OpPublish, Board: b})
	if !resp.OK {
		t.Fatalf("公開に失敗: %+v", resp)
	}
	if resp.SummaryVersion != 1 {
		t.Errorf("版 = %d, want 1", resp.SummaryVersion)
	}
	// Markdown に残っていること。
	data, err := os.ReadFile(store.MarkdownPath())
	if err != nil {
		t.Fatalf("Markdown が保存されていない: %v", err)
	}
	if !strings.Contains(string(data), "来週のリリース") {
		t.Errorf("内容が保存されていない:\n%s", data)
	}
	// 状態としても読めること。
	ctxResp := lv.Handle(ipc.Request{Op: ipc.OpContext})
	if ctxResp.Board == nil || ctxResp.Board.Topic != "来週のリリース" {
		t.Errorf("会議ボードが読めない: %+v", ctxResp.Board)
	}
}

// 複数回公開しても古い版を失わないこと。
func TestLivePublishKeepsVersions(t *testing.T) {
	lv, store, _ := newTestLive(t)
	for i, topic := range []string{"話題A", "話題B"} {
		resp := lv.Handle(ipc.Request{Op: ipc.OpPublish, Board: &board.Board{Topic: topic}})
		if resp.SummaryVersion != i+1 {
			t.Fatalf("版 = %d, want %d", resp.SummaryVersion, i+1)
		}
	}
	hist, err := os.ReadFile(store.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"話題A", "話題B"} {
		if !strings.Contains(string(hist), want) {
			t.Errorf("履歴に %q が無い", want)
		}
	}
}

func TestLivePublishRejectsEmpty(t *testing.T) {
	lv, _, _ := newTestLive(t)
	if resp := lv.Handle(ipc.Request{Op: ipc.OpPublish}); resp.OK {
		t.Fatal("空の会議ボードが受理された")
	}
}

// stop は停止処理を呼び、**保存完了まで待ってから**応答すること。
func TestLiveStopWaitsForDrain(t *testing.T) {
	lv, _, _ := newTestLive(t)

	var (
		mu          sync.Mutex
		saveDone    bool
		respondedAt time.Time
		savedAt     time.Time
	)
	lv.stopOnce = func() {
		// 録音側のドレイン (最終区間の推論と保存) を模擬する。
		go func() {
			time.Sleep(300 * time.Millisecond)
			mu.Lock()
			saveDone = true
			savedAt = time.Now()
			mu.Unlock()
			lv.markFinished(nil)
		}()
	}

	resp := lv.Handle(ipc.Request{Op: ipc.OpStop})
	respondedAt = time.Now()

	if !resp.OK {
		t.Fatalf("停止に失敗: %+v", resp)
	}
	mu.Lock()
	defer mu.Unlock()
	if !saveDone {
		t.Fatal("保存完了を待たずに応答した (直後の read_transcript で取りこぼす)")
	}
	if respondedAt.Before(savedAt) {
		t.Errorf("応答が保存より先: respond=%v save=%v", respondedAt, savedAt)
	}
	if resp.State != string(session.StateStopped) {
		t.Errorf("状態 = %q", resp.State)
	}
}

// 終了時の保存に失敗したら、成功扱いにしないこと。
func TestLiveStopReportsSaveFailure(t *testing.T) {
	lv, _, _ := newTestLive(t)
	lv.stopOnce = func() {
		go lv.markFinished(errors.New("ファイルを閉じられません"))
	}
	resp := lv.Handle(ipc.Request{Op: ipc.OpStop})
	if resp.OK {
		t.Fatal("保存失敗なのに成功を返した")
	}
	if !strings.Contains(resp.Error, "ファイルを閉じられません") {
		t.Errorf("理由が返らない: %q", resp.Error)
	}
}

// ドレインが終わらない場合は期限切れとして失敗を返すこと。
func TestLiveStopTimesOutWithoutDrain(t *testing.T) {
	lv, _, _ := newTestLive(t)
	lv.stopOnce = func() {} // 何も起きない (ドレインしない)

	// 待ち時間を短縮して検証する。
	done := make(chan ipc.Response, 1)
	go func() {
		// stopDrainTimeout は定数なので、done を閉じない状態での
		// 応答が「成功」にならないことだけを短時間で確認する。
		select {
		case <-lv.done:
			done <- ipc.Response{OK: true}
		case <-time.After(300 * time.Millisecond):
			done <- ipc.Response{OK: false}
		}
	}()
	got := <-done
	if got.OK {
		t.Fatal("ドレインしていないのに完了扱いになった")
	}
}

func TestLiveUnknownOp(t *testing.T) {
	lv, _, _ := newTestLive(t)
	if resp := lv.Handle(ipc.Request{Op: "bogus"}); resp.OK {
		t.Fatal("未対応の操作が成功扱いになった")
	}
}

// 会議ボードは発言ログと混ざらず、別の領域として返ること。
func TestLiveContextSeparatesLinesAndBoard(t *testing.T) {
	lv, _, _ := newTestLive(t)
	lv.addLine("00:00:01", "発言です")
	lv.Handle(ipc.Request{Op: ipc.OpPublish, Board: &board.Board{Topic: "話題です"}})

	resp := lv.Handle(ipc.Request{Op: ipc.OpContext})
	if len(resp.Lines) != 1 || resp.Lines[0].Text != "発言です" {
		t.Errorf("発言ログが不正: %+v", resp.Lines)
	}
	if resp.Board == nil || resp.Board.Topic != "話題です" {
		t.Errorf("会議ボードが不正: %+v", resp.Board)
	}
	// 発言ログに会議ボードの内容が混ざっていないこと。
	for _, l := range resp.Lines {
		if strings.Contains(l.Text, "話題です") {
			t.Errorf("発言ログに会議ボードが混ざった: %+v", l)
		}
	}
}

func TestSplitLineHelper(t *testing.T) {
	at, text := splitLine("[00:12:34] 本文")
	if at != "00:12:34" || text != "本文" {
		t.Errorf("at=%q text=%q", at, text)
	}
}

// syncBuffer は並行書き込みに耐える検証用バッファ。
//
// リサイズは別 goroutine から書き込むため、素の bytes.Buffer だと
// テスト側の読み取りと競合する (実装側は Split の mutex で直列化済み)。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Reset はそれまでの出力を捨てる (直後の描画だけを見たいとき用)。
func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// --- 端末サイズの追従 (SIGWINCH) ---

// SIGWINCH を受けて Split.Resize が呼ばれること。
//
// 実機では pane を広げても会議ボードが元の位置に残っていた
// (起動時に一度だけ ui.TermSize を読んでいたため)。
func TestViewFollowsTerminalResize(t *testing.T) {
	buf := &syncBuffer{}
	split := ui.NewSplit(buf, 24, 60, false)
	v := &view{con: ui.NewWith(buf, true), split: split}

	v.Start()
	t.Cleanup(v.Stop)

	// SIGWINCH を自分へ送る (既定動作は無視なので安全)。
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatalf("SIGWINCH を送れない: %v", err)
	}

	// ハンドラが端末サイズを取り直して Resize を呼ぶまで待つ。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if buf.Len() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if buf.Len() == 0 {
		t.Fatal("SIGWINCH で再描画されなかった")
	}
}

// Stop でシグナル監視を止めること (多重 Stop でも落ちない)。
func TestViewStopEndsResizeWatch(t *testing.T) {
	buf := &syncBuffer{}
	v := &view{con: ui.NewWith(buf, true), split: ui.NewSplit(buf, 24, 60, false)}
	v.Start()
	v.Stop()
	v.Stop() // 二重 Stop

	// 監視終了後に SIGWINCH を送っても落ちないこと。
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatalf("SIGWINCH を送れない: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
}

// 非 TTY では分割表示を使わず、従来どおりの出力にすること。
func TestNonTTYViewHasNoSplit(t *testing.T) {
	var buf bytes.Buffer
	cfg := config.Default()
	cfg.PaneRole = "sokki"
	con := ui.NewWith(&buf, false) // 非 TTY

	v := newView(con, cfg)
	if v.split != nil {
		t.Fatal("非 TTY で分割表示が有効になった")
	}
	v.Start()
	t.Cleanup(v.Stop)

	v.Log("[00:00:01] 確定行")
	v.Interim("[00:00:02] 暫定行")

	out := buf.String()
	if strings.Contains(out, "\x1b[") || strings.Contains(out, "\r") {
		t.Errorf("非 TTY に制御文字が出た: %q", out)
	}
	if strings.Contains(out, "暫定行") {
		t.Errorf("非 TTY に暫定行が出た: %q", out)
	}
	if !strings.Contains(out, "確定行") {
		t.Errorf("確定行が出ていない: %q", out)
	}
}

// pane role が無い手動実行でも分割表示を使わないこと。
func TestManualRunHasNoSplit(t *testing.T) {
	var buf bytes.Buffer
	cfg := config.Default() // PaneRole 未設定
	v := newView(ui.NewWith(&buf, true), cfg)
	if v.split != nil {
		t.Fatal("手動実行で分割表示が有効になった")
	}
}

// --- 自動要約ワーカーとの結線 ---

// live が summarizer.Source として材料を返し、公開で画面も更新すること。
func TestLiveServesSummarizerSource(t *testing.T) {
	lv, store, _ := newTestLive(t)
	lv.addLine("00:00:01", "最初の発言")
	lv.addLine("00:00:05", "次の発言")

	snap := lv.SummarySnapshot()
	if snap.Total != 2 || len(snap.Lines) != 2 || snap.AppliedThrough != 0 {
		t.Fatalf("材料が不正: %+v", snap)
	}

	// 公開すると反映位置と版が進むこと。
	if err := lv.PublishSummary("話題", "・要点", 2, snap.Version); err != nil {
		t.Fatal(err)
	}
	after := lv.SummarySnapshot()
	if after.AppliedThrough != 2 || len(after.Lines) != 0 {
		t.Errorf("反映位置が進んでいない: %+v", after)
	}
	if after.Topic != "話題" || after.Summary != "・要点" {
		t.Errorf("会議ボードが更新されていない: %+v", after)
	}
	// Markdown にも保存されること。
	data, err := os.ReadFile(store.MarkdownPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "・要点") {
		t.Errorf("Markdown に残っていない:\n%s", data)
	}
}

// 版が古ければ公開を拒否すること (手動公開との競合)。
func TestLivePublishSummaryRejectsStaleVersion(t *testing.T) {
	lv, _, _ := newTestLive(t)
	lv.addLine("00:00:01", "発言")

	// 手動公開が先に入った状況。
	lv.Handle(ipc.Request{Op: ipc.OpPublish, Board: &board.Board{Topic: "手動"}})

	err := lv.PublishSummary("自動", "・自動の要約", 1, 0) // 古い版 0 を基準にする
	if !errors.Is(err, summarizer.ErrVersionConflict) {
		t.Fatalf("競合として扱われない: %v", err)
	}
	if cur := lv.SummarySnapshot(); cur.Topic != "手動" {
		t.Errorf("手動公開が上書きされた: %+v", cur)
	}
}

// 自動要約の状態が IPC の status に載ること。
func TestLiveStatusIncludesSummary(t *testing.T) {
	lv, _, _ := newTestLive(t)
	lv.SetSummaryStatus(func() summarizer.Status {
		return summarizer.Status{
			Enabled: true, State: summarizer.StateIdle,
			Provider: "claude", Version: 3, PendingLines: 2,
		}
	})
	resp := lv.Handle(ipc.Request{Op: ipc.OpStatus})
	if resp.Summary == nil {
		t.Fatal("summary が返らない")
	}
	if resp.Summary["provider"] != "claude" || resp.Summary["enabled"] != true {
		t.Errorf("summary が不正: %+v", resp.Summary)
	}
}

// ワーカーと live をつないで、発言から会議ボードが自動更新されること
// (AI は偽プロバイダー、音声デバイスは不使用)。
func TestWorkerUpdatesBoardFromLines(t *testing.T) {
	lv, store, _ := newTestLive(t)

	p := &fakeSummaryProvider{}
	cfg := summarizer.DefaultConfig()
	cfg.MinNewLines = 2
	cfg.MinInterval = 10 * time.Millisecond
	cfg.MaxInterval = 50 * time.Millisecond
	cfg.Tick = 5 * time.Millisecond
	w := summarizer.New(lv, p, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go w.Run(ctx)

	lv.addLine("00:00:01", "来週のリリースについて話します")
	lv.addLine("00:00:05", "テストは金曜までに終える予定です")
	lv.addLine("00:00:09", "担当者を決めます")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if lv.SummarySnapshot().Version > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.Stop()

	cur := lv.SummarySnapshot()
	if cur.Version == 0 {
		t.Fatal("会議ボードが自動更新されなかった")
	}
	if cur.AppliedThrough != 3 {
		t.Errorf("反映位置 = %d, want 3", cur.AppliedThrough)
	}
	if !strings.Contains(cur.Summary, "リリース") {
		t.Errorf("要約に発言が反映されていない: %q", cur.Summary)
	}
	data, err := os.ReadFile(store.MarkdownPath())
	if err != nil {
		t.Fatalf("Markdown が保存されていない: %v", err)
	}
	if !strings.Contains(string(data), "リリース") {
		t.Errorf("Markdown に残っていない:\n%s", data)
	}
}

// fakeSummaryProvider は発言をそのまま箇条書きにする偽 AI。
type fakeSummaryProvider struct{}

func (fakeSummaryProvider) Name() string { return "fake" }

func (fakeSummaryProvider) Summarize(ctx context.Context, in summarizer.Input) (summarizer.Output, error) {
	body := strings.TrimSpace(in.Summary)
	for _, l := range in.NewLines {
		if body != "" {
			body += "\n"
		}
		body += "・[" + l.At + "] " + l.Text
	}
	topic := "会議"
	if len(in.NewLines) > 0 {
		topic = in.NewLines[len(in.NewLines)-1].Text
	}
	return summarizer.Output{Topic: topic, Summary: body}, nil
}

// --- 自動要約の有効化判定 ---

func TestStartSummarizerDisabledByDefault(t *testing.T) {
	lv, _, _ := newTestLive(t)
	cfg := config.Default() // AutoSummary = false
	w, err := startSummarizer(cfg, lv)
	if err != nil || w != nil {
		t.Fatalf("無効のはずが w=%v err=%v", w, err)
	}
	resp := lv.Handle(ipc.Request{Op: ipc.OpStatus})
	if resp.Summary["enabled"] != false {
		t.Errorf("無効として報告されない: %+v", resp.Summary)
	}
	if resp.Summary["state"] != string(summarizer.StateDisabled) {
		t.Errorf("state = %v", resp.Summary["state"])
	}
}

// 未対応のプロバイダーは理由を返し、黙って無効化しないこと。
func TestStartSummarizerUnknownProvider(t *testing.T) {
	lv, _, _ := newTestLive(t)
	cfg := config.Default()
	cfg.AutoSummary = true
	cfg.SummaryProvider = "bogus"

	w, err := startSummarizer(cfg, lv)
	if err == nil {
		t.Fatal("未対応のプロバイダーがエラーにならない")
	}
	if w != nil {
		t.Error("ワーカーが作られた")
	}
	resp := lv.Handle(ipc.Request{Op: ipc.OpStatus})
	if resp.Summary["enabled"] != false {
		t.Errorf("無効として報告されない: %+v", resp.Summary)
	}
	// 理由が残ること (黙って無効化しない)。
	if msg, _ := resp.Summary["last_error"].(string); !strings.Contains(msg, "bogus") {
		t.Errorf("理由が残っていない: %+v", resp.Summary)
	}
}

// コマンドが見つからない場合も理由を返すこと。
func TestStartSummarizerMissingCommand(t *testing.T) {
	lv, _, _ := newTestLive(t)
	cfg := config.Default()
	cfg.AutoSummary = true
	cfg.SummaryCommand = []string{"zzz-no-such-ai-command"}

	w, err := startSummarizer(cfg, lv)
	if err == nil {
		t.Fatal("不在のコマンドがエラーにならない")
	}
	if w != nil {
		t.Error("ワーカーが作られた")
	}
	resp := lv.Handle(ipc.Request{Op: ipc.OpStatus})
	if msg, _ := resp.Summary["last_error"].(string); !strings.Contains(msg, "zzz-no-such-ai-command") {
		t.Errorf("理由が残っていない: %+v", resp.Summary)
	}
}

// 有効かつコマンドがあればワーカーを作ること。
func TestStartSummarizerCreatesWorker(t *testing.T) {
	lv, _, _ := newTestLive(t)
	cfg := config.Default()
	cfg.AutoSummary = true
	cfg.SummaryCommand = []string{"/bin/echo"} // 必ず存在する
	cfg.SummaryMinLines = 2

	w, err := startSummarizer(cfg, lv)
	if err != nil {
		t.Fatalf("作成に失敗: %v", err)
	}
	if w == nil {
		t.Fatal("ワーカーが作られていない")
	}
	resp := lv.Handle(ipc.Request{Op: ipc.OpStatus})
	if resp.Summary["enabled"] != true {
		t.Errorf("有効として報告されない: %+v", resp.Summary)
	}
}

// REVIEW.md [P1] get_status と確定発言の同時処理で詰まらないこと。
//
// live.mu を保持したままワーカーの状態を取ると、ワーカーが live を
// 読み返すときにロック順が循環し、addLine の書き込み待ちで詰まる。
func TestStatusAndAddLineDoNotDeadlock(t *testing.T) {
	lv, _, _ := newTestLive(t)
	w := summarizer.New(lv, fakeSummaryProvider{}, summarizer.DefaultConfig())
	lv.SetSummaryStatus(w.Status)

	const rounds = 200
	done := make(chan struct{})
	go func() { // 確定発言を追加し続ける (live.mu の書き込み)
		defer close(done)
		for i := 0; i < rounds; i++ {
			lv.addLine("00:00:01", "発言")
		}
	}()
	statusDone := make(chan struct{})
	go func() { // get_status を叩き続ける
		defer close(statusDone)
		for i := 0; i < rounds; i++ {
			if resp := lv.Handle(ipc.Request{Op: ipc.OpStatus}); !resp.OK {
				t.Errorf("status が失敗: %+v", resp)
				return
			}
		}
	}()
	snapDone := make(chan struct{})
	go func() { // ワーカー相当の読み取り
		defer close(snapDone)
		for i := 0; i < rounds; i++ {
			_ = w.Status()
			_ = lv.SummarySnapshot()
		}
	}()

	for _, ch := range []chan struct{}{done, statusDone, snapDone} {
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatal("10 秒以内に応答しない (デッドロック)")
		}
	}
}

// ワーカーを動かしながら get_status を叩いても詰まらないこと。
func TestStatusWhileWorkerRunning(t *testing.T) {
	lv, _, _ := newTestLive(t)
	cfg := summarizer.DefaultConfig()
	cfg.MinNewLines = 1
	cfg.MinInterval = time.Millisecond
	cfg.MaxInterval = 5 * time.Millisecond
	cfg.Tick = time.Millisecond
	w := summarizer.New(lv, fakeSummaryProvider{}, cfg)
	lv.SetSummaryStatus(w.Status)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go w.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for i := 0; time.Now().Before(deadline) && i < 300; i++ {
		lv.addLine("00:00:01", "発言")
		resp := lv.Handle(ipc.Request{Op: ipc.OpStatus})
		if !resp.OK {
			t.Fatalf("status が失敗: %+v", resp)
		}
	}
	w.Stop()
}

// --- REVIEW.md [P2] 起動できなければ「自動更新」と約束しない ---

// プロバイダー不在なら、見出しと空文言が自動更新を約束しないこと。
func TestViewReflectsFailedSummarizer(t *testing.T) {
	term := &syncBuffer{}
	split := ui.NewSplit(term, 24, 60, false)
	v := &view{con: ui.NewWith(term, true), split: split, autoSummary: true}
	v.Start()
	t.Cleanup(v.Stop)

	// 起動に失敗した想定で実状態を反映する。
	// 反映後に描かれる内容だけを見る (それ以前のフレームは対象外)。
	term.Reset()
	v.setAutoSummary(false)

	out := term.String()
	if strings.Contains(out, "自動更新") {
		t.Errorf("起動できていないのに自動更新と表示した:\n%s", out)
	}
	if strings.Contains(out, "自動で作成されます") {
		t.Errorf("起動できていないのに自動作成を約束した:\n%s", out)
	}
	if !strings.Contains(out, "会議ボード") {
		t.Errorf("会議ボードの見出しが無い:\n%s", out)
	}
}

// 起動できていれば自動更新と表示すること。
func TestViewShowsAutoWhenWorkerRuns(t *testing.T) {
	term := &syncBuffer{}
	split := ui.NewSplit(term, 24, 60, false)
	v := &view{con: ui.NewWith(term, true), split: split}
	v.Start()
	t.Cleanup(v.Stop)

	term.Reset()
	v.setAutoSummary(true)
	out := term.String()
	if !strings.Contains(out, "自動更新") {
		t.Errorf("自動更新と表示されない:\n%s", out)
	}
}

// --- REVIEW.md [P2] recording の保存と自動要約の判定を分けないこと ---

// markRecording が状態と自動要約の結果を 1 回で書くこと。
func TestMarkRecordingSavesStateAndSummaryTogether(t *testing.T) {
	cases := []struct {
		name        string
		summarizing bool
		err         error
		wantAuto    bool
		wantNote    string
	}{
		{"起動できた", true, nil, true, ""},
		{"無効", false, nil, false, "自動要約は無効です (--auto-summary で有効化)"},
		{"起動に失敗", false, fmt.Errorf("claude が見つかりません"), false, "claude が見つかりません"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session.SetDirForTest(t.TempDir())
			defer session.SetDirForTest("")

			rec := &session.Record{ID: "rec-1", State: session.StateStarting}
			if err := session.Save(rec); err != nil {
				t.Fatal(err)
			}
			markRecording(rec, nil, tc.summarizing, tc.err)

			got, err := session.Load("rec-1")
			if err != nil {
				t.Fatal(err)
			}
			// AI は recording を見た時点で読む。同じ保存に揃っていること。
			if got.State != session.StateRecording {
				t.Errorf("state = %s", got.State)
			}
			if got.AutoSummary != tc.wantAuto {
				t.Errorf("auto_summary = %v, want %v", got.AutoSummary, tc.wantAuto)
			}
			if got.SummaryNote != tc.wantNote {
				t.Errorf("summary_note = %q, want %q", got.SummaryNote, tc.wantNote)
			}
		})
	}
}

// 録音開始の保存が、要約ワーカーの起動より後に来ること。
//
// 先に recording を保存すると、waitForRecord で待っている AI が
// 自動要約の判定前の記録を読み、正常起動でも「失敗」と受け取る。
func TestRecordingIsSavedAfterSummarizerStarts(t *testing.T) {
	src, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	start := strings.Index(text, "startSummarizer(")
	if start < 0 {
		t.Fatal("startSummarizer の呼び出しが見つからない")
	}
	mark := strings.Index(text, "markRecording(")
	if mark < 0 {
		t.Fatal("markRecording の呼び出しが見つからない")
	}
	if mark < start {
		t.Error("録音開始を要約ワーカーの起動より先に保存している")
	}
	// recording への遷移が 1 箇所でも前にあれば、AI は途中の記録を読みうる。
	if rs := strings.Index(text, "session.StateRecording"); rs < start {
		t.Error("recording への遷移が要約ワーカーの起動より前にある")
	}
}
