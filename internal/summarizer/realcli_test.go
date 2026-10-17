package summarizer

import (
	"context"
	"os"
	"strings"
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

// 合成した予告と本編の重複を、実プロバイダーが話題単位にまとめられるか観察する。
// 通常のテストでは AI を呼ばない。実会議の文字起こしは送らない。
func TestRealClaudeSynthesizesRepeatedTopic(t *testing.T) {
	if os.Getenv("SOKKI_REAL_CLI") != "1" {
		t.Skip("SOKKI_REAL_CLI=1 のときだけ実行する")
	}
	args, err := Preset("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	p := &ExecProvider{Command: args, Label: "claude", Timeout: 120 * time.Second}
	out, err := p.Summarize(context.Background(), Input{
		Topic:   "リリース計画",
		Summary: "・来週公開する。\n・来週リリースする予定。\n・テストは金曜まで。",
		NewLines: []Line{
			{At: "00:01:00", Text: "冒頭の予告です。新サービスは来週公開、試験は金曜までと話しました。"},
			{At: "00:01:20", Text: "本編です。新サービスの公開時期について改めて話します。"},
			{At: "00:01:40", Text: "公開は来週を目指しています。"},
			{At: "00:02:00", Text: "ただし試験が終わらなければ公開日を見直します。"},
			{At: "00:02:20", Text: "試験担当は山田さん、金曜までに結果を共有します。"},
			{At: "00:02:40", Text: "来週公開の話は先ほどの予告と同じ内容です。"},
			{At: "00:03:00", Text: "次の話題は利用者向けの案内です。"},
			{At: "00:03:20", Text: "案内文は木曜までに佐藤さんが下書きします。"},
		},
		MaxChars: 800,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("topic=%q summary=%q", out.Topic, out.Summary)
	if !strings.Contains(out.Summary, "山田") || !strings.Contains(out.Summary, "佐藤") {
		t.Errorf("担当者の重要情報が欠けた: %q", out.Summary)
	}
}

// 以前の要約に混入した根拠のない国名を、後の発言で訂正できるか確認する。
func TestRealClaudeCorrectsUnsupportedCountry(t *testing.T) {
	if os.Getenv("SOKKI_REAL_CLI") != "1" {
		t.Skip("SOKKI_REAL_CLI=1 のときだけ実行する")
	}
	args, err := Preset("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	p := &ExecProvider{Command: args, Label: "claude", Timeout: 120 * time.Second}
	out, err := p.Summarize(context.Background(), Input{
		Topic:   "米国の政府AI基盤",
		Summary: "政府向けAI基盤の比較\n・米国のアルバートAPIに対し、日本はアプリまで提供する。",
		NewLines: []Line{
			{At: "00:05:07", Text: "これは日本とフランスの違いで言うと、各省庁のIT部門がアプリケーションまで作れるのかという点です。"},
			{At: "00:05:17", Text: "フランスはAPIを提供し、日本はデジタル庁がアプリを各省庁に提供しています。"},
		},
		MaxChars: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("topic=%q summary=%q", out.Topic, out.Summary)
	if strings.Contains(out.Topic, "米国") || strings.Contains(out.Summary, "米国のアルバート") {
		t.Errorf("後の発言と矛盾する国名が残った: topic=%q summary=%q", out.Topic, out.Summary)
	}
	if !strings.Contains(out.Topic+out.Summary, "フランス") {
		t.Errorf("発言で確認できる国名が反映されていない: topic=%q summary=%q", out.Topic, out.Summary)
	}
}
