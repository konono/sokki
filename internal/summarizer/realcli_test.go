package summarizer

import (
	"context"
	"os"
	"testing"
	"time"
)

// 合成入力で実 Claude CLI を 1 回だけ呼び、応答の形を確認する。
// SOKKI_REAL_CLI=1 のときだけ実行する (音声デバイス・実会議は使わない)。
func TestRealClaudeCLIShape(t *testing.T) {
	if os.Getenv("SOKKI_REAL_CLI") != "1" {
		t.Skip("SOKKI_REAL_CLI=1 のときだけ実行する")
	}
	args, err := Preset("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	p := &ExecProvider{Command: args, Label: "claude", Timeout: 120 * time.Second}
	out, err := p.Summarize(context.Background(), Input{
		NewLines: []Line{
			{At: "00:00:01", Text: "これは合成入力です。来週のリリース日を決めます。"},
			{At: "00:00:09", Text: "テストは金曜までに終える方針で合意しました。"},
		},
		MaxChars: 500,
	})
	if err != nil {
		t.Fatalf("実 CLI の応答を解釈できない: %v", err)
	}
	t.Logf("topic=%q", out.Topic)
	t.Logf("summary=%q", out.Summary)
	if out.Summary == "" {
		t.Error("summary が空")
	}
}
