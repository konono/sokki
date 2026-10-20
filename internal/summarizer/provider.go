// Package summarizer は確定した発言から会議ボードを自動生成する。
//
// 録音プロセス内で動き、AI クライアント (MCP) の接続とは独立している。
// AI の呼び出しは差し替え可能な Provider に閉じ込める。
package summarizer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"syscall"
	"time"

	"github.com/konono/sokki/internal/proc"
)

// Line は要約の材料となる確定発言。
type Line struct {
	Index int
	At    string
	Text  string
}

// Input は Provider に渡す材料。
type Input struct {
	// Topic / Summary は現在の会議ボード。
	Topic   string
	Summary string
	// NewLines は未反映の確定発言。
	NewLines []Line
	// MaxChars は要約本文の目安上限。
	MaxChars int
	// Final は会議終了時の最終要約かどうか。
	Final bool
}

// Output は Provider が返す新しい会議ボード。
type Output struct {
	Topic   string `json:"topic"`
	Summary string `json:"summary"`
}

// Provider は確定発言と現在の要約から、新しい要約を作る。
type Provider interface {
	Name() string
	Summarize(ctx context.Context, in Input) (Output, error)
}

// ExecProvider は外部コマンドを 1 回実行して要約を得る。
//
// プロンプトは stdin で渡し、結果は stdout の JSON で受け取る。
// 会議の内容を argv に載せない (プロセス一覧から読めてしまうため)。
type ExecProvider struct {
	// Command は実行するコマンドと引数。
	Command []string
	// Label は表示用の名前。
	Label string
	// Timeout は 1 回の呼び出しの上限。
	Timeout time.Duration
}

// Name は表示用の名前を返す。
func (p *ExecProvider) Name() string {
	if p.Label != "" {
		return p.Label
	}
	if len(p.Command) > 0 {
		return p.Command[0]
	}
	return "exec"
}

// Summarize は外部コマンドへプロンプトを渡し、JSON を受け取る。
func (p *ExecProvider) Summarize(ctx context.Context, in Input) (Output, error) {
	if len(p.Command) == 0 {
		return Output{}, fmt.Errorf("要約コマンドが設定されていません")
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := proc.Command(ctx, p.Command[0], p.Command[1:]...)
	// A shell/CLI can leave a child holding stdout after its own cancellation.
	// Cancel the dedicated process group and bound inherited-pipe draining.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 300 * time.Millisecond
	cmd.Stdin = strings.NewReader(BuildPrompt(in))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return Output{}, fmt.Errorf("要約がタイムアウトしました (%v): %s", timeout, msg)
		}
		return Output{}, fmt.Errorf("要約コマンドが失敗しました: %s", msg)
	}
	out, err := ParseOutput(stdout.Bytes())
	if err != nil {
		return Output{}, err
	}
	return out, nil
}

// envelope は CLI が結果を包んで返す形。
//
// 例: claude -p --output-format json は
// {"type":"result","is_error":false,"result":"{\"topic\":...}"} を返す。
type envelope struct {
	Type    string `json:"type"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Error   string `json:"error"`
}

// ParseOutput は応答から会議ボードの JSON を取り出す。
//
// 裸の {topic, summary} と、CLI が包んだ形 (result に文字列で入る) の
// 両方を受け付ける。前後に説明やコードブロックが付いてもよい。
// 読めなければ失敗にする (壊れた要約で上書きするより、前の版を保つ)。
func ParseOutput(data []byte) (Output, error) {
	return parseOutput(data, 0)
}

const maxEnvelopeDepth = 3

func parseOutput(data []byte, depth int) (Output, error) {
	raw := bytes.TrimSpace(data)
	if len(raw) == 0 {
		return Output{}, fmt.Errorf("要約の応答が空です")
	}
	candidate := extractJSONObject(raw)
	if candidate == nil {
		return Output{}, fmt.Errorf("要約の応答に JSON が含まれません: %s", preview(raw))
	}

	// 包まれた形かどうかを先に見る。
	var env envelope
	if err := json.Unmarshal(candidate, &env); err == nil {
		if env.IsError {
			msg := env.Error
			if msg == "" {
				msg = env.Result
			}
			if msg == "" {
				msg = "原因不明"
			}
			return Output{}, fmt.Errorf("要約コマンドがエラーを返しました: %s", preview([]byte(msg)))
		}
		if strings.TrimSpace(env.Result) != "" {
			if depth >= maxEnvelopeDepth {
				return Output{}, fmt.Errorf("要約の応答の入れ子が深すぎます")
			}
			// result の中身を要約 JSON として読む。
			return parseOutput([]byte(env.Result), depth+1)
		}
	}

	var out Output
	if err := json.Unmarshal(candidate, &out); err != nil {
		return Output{}, fmt.Errorf("要約の応答を解釈できません: %w (%s)", err, preview(candidate))
	}
	if strings.TrimSpace(out.Summary) == "" && strings.TrimSpace(out.Topic) == "" {
		return Output{}, fmt.Errorf("要約の応答が空です: %s", preview(candidate))
	}
	return out, nil
}

// extractJSONObject は最初の対応の取れた JSON オブジェクトを返す。
func extractJSONObject(b []byte) []byte {
	start := bytes.IndexByte(b, '{')
	if start < 0 {
		return nil
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(b); i++ {
		c := b[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
			// 文字列中の括弧は数えない
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return b[start : i+1]
			}
		}
	}
	return nil
}

func preview(b []byte) string {
	const max = 200
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// Preset は既知の CLI 向けの引数を返す。
//
// 会議の発言は外部入力なので、要約用の子プロセスには
// ツール実行・MCP・会話履歴を与えない。
func Preset(name, model string) ([]string, error) {
	switch name {
	case "claude":
		args := []string{"claude", "-p",
			"--output-format", "json",
			"--tools", "",
			"--strict-mcp-config",
			"--no-session-persistence",
		}
		if model != "" {
			args = append(args, "--model", model)
		}
		return args, nil
	case "codex":
		// ツールを使えない実行形を実機で確認できていないため、
		// 既定のプリセットとしては提供しない。
		// 会議の発言は外部入力であり、ツール権限を渡すと
		// 発言に紛れた指示で何かを実行されうる。
		return nil, fmt.Errorf(
			"codex プリセットは未対応です (要約子プロセスからツールを使えない実行形が未検証のため)\n" +
				"対処: provider を claude にするか、設定ファイルの summary.command に\n" +
				"      ツールを無効にした実行形を配列で明示してください\n" +
				`      例: summary:` + "\n" + `        command: ["codex", "exec", "--sandbox", "read-only", "-"]`)
	default:
		return nil, fmt.Errorf(
			"未対応の要約プロバイダーです: %q\n対処: claude、または command で直接指定してください", name)
	}
}
