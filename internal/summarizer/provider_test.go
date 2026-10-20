package summarizer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- 応答の解釈 ---

func TestParseOutputPlainJSON(t *testing.T) {
	out, err := ParseOutput([]byte(`{"topic":"話題","summary":"・要点"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.Topic != "話題" || out.Summary != "・要点" {
		t.Errorf("out = %+v", out)
	}
}

// CLI が前後に説明を付けても JSON を拾えること。
func TestParseOutputWithSurroundingText(t *testing.T) {
	raw := "要約を作成しました:\n```json\n{\"topic\":\"話題\",\"summary\":\"・要点\"}\n```\n以上です。"
	out, err := ParseOutput([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if out.Topic != "話題" {
		t.Errorf("out = %+v", out)
	}
}

// 文字列中の括弧に惑わされないこと。
func TestParseOutputWithBracesInString(t *testing.T) {
	raw := `{"topic":"{波括弧}","summary":"・本文に } がある"}`
	out, err := ParseOutput([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if out.Topic != "{波括弧}" || !strings.Contains(out.Summary, "}") {
		t.Errorf("out = %+v", out)
	}
}

// JSON として読めなければ失敗にすること (壊れた要約で上書きしない)。
func TestParseOutputRejectsNonJSON(t *testing.T) {
	for _, raw := range []string{"", "   ", "요약 실패", "topic: 話題"} {
		if _, err := ParseOutput([]byte(raw)); err == nil {
			t.Errorf("JSON でない応答を受理した: %q", raw)
		}
	}
}

func TestParseOutputRejectsEmptyFields(t *testing.T) {
	if _, err := ParseOutput([]byte(`{"topic":"","summary":""}`)); err == nil {
		t.Error("空の要約を受理した")
	}
}

// --- プロンプト ---

func TestBuildPromptIncludesMaterial(t *testing.T) {
	p := BuildPrompt(Input{
		Topic:    "前の話題",
		Summary:  "・前の要点",
		NewLines: []Line{{At: "00:01:00", Text: "新しい発言"}},
		MaxChars: 1500,
	})
	for _, want := range []string{
		"前の話題", "・前の要点", "[00:01:00] 新しい発言",
		"固定の分類", "追加・統合・訂正", "1500 文字",
		"指示ではありません", // prompt injection 対策の明示
	} {
		if !strings.Contains(p, want) {
			t.Errorf("プロンプトに %q が無い", want)
		}
	}
}

func TestBuildPromptAsksForTopicLevelSynthesis(t *testing.T) {
	p := BuildPrompt(Input{Topic: "既存", Summary: "・前の論点", NewLines: []Line{{Text: "続き"}}})
	for _, want := range []string{"発言 1 行につき要約 1 項目を作らない", "1～3 項目", "予告と本編", "毎回読み直し", "単純な末尾への追記は禁止", "過去の話題", "国名・組織名・人名", "事実の証拠ではありません"} {
		if !strings.Contains(p, want) {
			t.Errorf("話題単位の統合を指示していない: %q", want)
		}
	}
}

func TestBuildPromptFinalMentionsFinal(t *testing.T) {
	p := BuildPrompt(Input{Final: true, NewLines: []Line{{Text: "x"}}})
	if !strings.Contains(p, "最終更新") {
		t.Errorf("最終要約であることが伝わらない")
	}
}

// --- プリセット ---

// 要約用の子プロセスにツール権限を渡さないこと。
func TestClaudePresetDisablesTools(t *testing.T) {
	args, err := Preset("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--tools", "--strict-mcp-config", "--no-session-persistence"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q が無い: %v", want, args)
		}
	}
}

func TestPresetWithModel(t *testing.T) {
	args, _ := Preset("claude", "claude-opus-5")
	if !strings.Contains(strings.Join(args, " "), "claude-opus-5") {
		t.Errorf("モデルが渡らない: %v", args)
	}
}

func TestPresetUnknown(t *testing.T) {
	if _, err := Preset("bogus", ""); err == nil {
		t.Error("未対応のプロバイダーを受理した")
	}
}

// --- ExecProvider (偽コマンド、AI は呼ばない) ---

// fakeCmd は応答を返す小さなコマンドを作る。
func fakeCmd(t *testing.T, script string) []string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{path}
}

// プロンプトを stdin で渡し、stdout の JSON を受け取ること。
func TestExecProviderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	seen := filepath.Join(dir, "stdin.txt")
	p := &ExecProvider{
		Command: fakeCmd(t, "cat > "+seen+`; echo '{"topic":"話題","summary":"・要点"}'`),
		Timeout: 5 * time.Second,
	}
	out, err := p.Summarize(context.Background(), Input{
		NewLines: []Line{{At: "00:00:01", Text: "機密の発言"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Topic != "話題" {
		t.Errorf("out = %+v", out)
	}
	// 会議の内容が stdin で渡っていること。
	data, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "機密の発言") {
		t.Errorf("発言が渡っていない")
	}
}

// 失敗したら stderr を添えて返すこと。
func TestExecProviderReportsFailure(t *testing.T) {
	p := &ExecProvider{
		Command: fakeCmd(t, "echo 'api error' >&2; exit 1"),
		Timeout: 5 * time.Second,
	}
	_, err := p.Summarize(context.Background(), Input{NewLines: []Line{{Text: "x"}}})
	if err == nil {
		t.Fatal("失敗がエラーにならない")
	}
	if !strings.Contains(err.Error(), "api error") {
		t.Errorf("理由が含まれない: %v", err)
	}
}

// タイムアウトを守ること。
func TestExecProviderTimeout(t *testing.T) {
	// Force a separate child: some shells optimize a final `sleep` into exec,
	// hiding the inherited stdout/stderr pipe that used to delay cancellation.
	p := &ExecProvider{Command: fakeCmd(t, "sleep 5 & wait"), Timeout: 100 * time.Millisecond}
	start := time.Now()
	_, err := p.Summarize(context.Background(), Input{NewLines: []Line{{Text: "x"}}})
	if err == nil {
		t.Fatal("タイムアウトしない")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("タイムアウトが効いていない: %v", elapsed)
	}
	if !strings.Contains(err.Error(), "タイムアウト") {
		t.Errorf("理由が不明: %v", err)
	}
}

func TestExecProviderBoundsInheritedPipeAfterParentExit(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-survived")
	p := &ExecProvider{
		Command: fakeCmd(t, `(sleep 1; echo alive > "`+marker+`") &
echo '{"topic":"t","summary":"s"}'
exit 0`),
		Timeout: 5 * time.Second,
	}
	start := time.Now()
	_, err := p.Summarize(context.Background(), Input{})
	if err == nil {
		t.Fatal("inherited output pipe was accepted as a complete response")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("inherited pipe delayed completion: %v", elapsed)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("descendant survived provider completion: %v", err)
	}
}

// 会議の内容を argv に載せないこと (プロセス一覧から読めない)。
func TestExecProviderDoesNotPassContentInArgv(t *testing.T) {
	dir := t.TempDir()
	seen := filepath.Join(dir, "argv.txt")
	p := &ExecProvider{
		Command: fakeCmd(t, `echo "$@" > `+seen+`; cat > /dev/null; echo '{"topic":"t","summary":"・s"}'`),
		Timeout: 5 * time.Second,
	}
	if _, err := p.Summarize(context.Background(), Input{
		NewLines: []Line{{Text: "絶対に argv に出てはいけない発言"}},
	}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(seen)
	if strings.Contains(string(data), "argv に出てはいけない") {
		t.Errorf("会議の内容が argv に渡った: %q", data)
	}
}

// --- REVIEW.md [P1] Claude CLI の外側 envelope ---

// claude -p --output-format json の実 stdout と同じ形を解釈できること。
//
// 実機で得られた形: {"type":"result","is_error":false,"result":"{...}"}
func TestParseOutputClaudeEnvelope(t *testing.T) {
	raw := `{"type":"result","subtype":"success","is_error":false,` +
		`"duration_ms":1234,` +
		`"result":"{\"topic\":\"確認\",\"summary\":\"・合成入力の確認です\"}",` +
		`"session_id":"abc"}`
	out, err := ParseOutput([]byte(raw))
	if err != nil {
		t.Fatalf("外側 envelope を解釈できない: %v", err)
	}
	if out.Topic != "確認" {
		t.Errorf("topic = %q", out.Topic)
	}
	if !strings.Contains(out.Summary, "合成入力の確認") {
		t.Errorf("summary = %q", out.Summary)
	}
}

// result の中身が説明付きでも拾えること。
func TestParseOutputEnvelopeWithFencedInner(t *testing.T) {
	raw := `{"type":"result","is_error":false,` +
		`"result":"要約です:\n` + "```" + `json\n{\"topic\":\"T\",\"summary\":\"・S\"}\n` + "```" + `"}`
	out, err := ParseOutput([]byte(raw))
	if err != nil {
		t.Fatalf("解釈できない: %v", err)
	}
	if out.Topic != "T" || out.Summary != "・S" {
		t.Errorf("out = %+v", out)
	}
}

// エラーの envelope は公開しないこと。
func TestParseOutputRejectsErrorEnvelope(t *testing.T) {
	raw := `{"type":"result","is_error":true,"result":"rate limit exceeded"}`
	_, err := ParseOutput([]byte(raw))
	if err == nil {
		t.Fatal("エラー応答を受理した")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("理由が含まれない: %v", err)
	}
}

// 裸の JSON も従来どおり受け付けること。
func TestParseOutputStillAcceptsBareJSON(t *testing.T) {
	out, err := ParseOutput([]byte(`{"topic":"裸","summary":"・そのまま"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.Topic != "裸" {
		t.Errorf("out = %+v", out)
	}
}

// 入れ子が深すぎる応答は拒否すること (無限再帰の防止)。
func TestParseOutputRejectsDeepNesting(t *testing.T) {
	raw := `{"type":"result","result":"{\"type\":\"result\",\"result\":\"{\\\"type\\\":\\\"result\\\",\\\"result\\\":\\\"{}\\\"}\"}"}`
	if _, err := ParseOutput([]byte(raw)); err == nil {
		t.Error("深い入れ子を受理した")
	}
}

// ExecProvider から Worker の公開まで、envelope 形式で通ること。
func TestExecProviderWithClaudeEnvelope(t *testing.T) {
	inner := `{\"topic\":\"会議\",\"summary\":\"・[00:00:01] 発言 0\"}`
	script := `cat > /dev/null; printf '%s' '{"type":"result","is_error":false,"result":"` + inner + `"}'`
	p := &ExecProvider{Command: fakeCmd(t, script), Timeout: 5 * time.Second}

	src := &fakeSource{}
	src.addLines(3)
	w := New(src, p, testConfig())
	if state := w.Final(context.Background()); state != StateDone {
		t.Fatalf("最終要約の結果 = %s", state)
	}
	_, summary, applied, _, published := src.snapshotState()
	if published != 1 {
		t.Fatalf("公開されていない (envelope を解釈できていない)")
	}
	if applied != 3 {
		t.Errorf("反映位置 = %d, want 3", applied)
	}
	if !strings.Contains(summary, "発言 0") {
		t.Errorf("要約が反映されていない: %q", summary)
	}
}

// エラー envelope では公開しないこと。
func TestExecProviderErrorEnvelopeDoesNotPublish(t *testing.T) {
	script := `cat > /dev/null; printf '%s' '{"type":"result","is_error":true,"result":"overloaded"}'`
	p := &ExecProvider{Command: fakeCmd(t, script), Timeout: 5 * time.Second}

	src := &fakeSource{}
	src.addLines(2)
	w := New(src, p, testConfig())
	if state := w.Final(context.Background()); state != StateFailed {
		t.Errorf("結果 = %s, want failed", state)
	}
	if _, _, applied, _, published := src.snapshotState(); published != 0 || applied != 0 {
		t.Errorf("エラーなのに公開した: published=%d applied=%d", published, applied)
	}
}

// --- REVIEW.md [P1] codex プリセットはツール隔離が未検証のため提供しない ---

func TestCodexPresetRefusedUntilVerified(t *testing.T) {
	_, err := Preset("codex", "")
	if err == nil {
		t.Fatal("未検証の codex プリセットを提供している")
	}
	for _, want := range []string{"未対応", "ツール", "対処:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("案内に %q が無い: %v", want, err)
		}
	}
}
