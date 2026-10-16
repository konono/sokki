// Package board は会議ボード (AI が作る要約) の保持・描画・保存を担う。
//
// 本文は AI が作る。sokki は受け取った内容を画面へ反映し、
// 文字起こしと同じベース名の Markdown に保存する。
//
// 要約は固定の分類 (決定 / 宿題 / 未決 など) を持たない。
// AI が発言を読み、自由な文章で要点を積み重ね、必要に応じて
// 統合・訂正する。「今の話題」だけを独立した項目として持つ。
package board

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Board は会議ボード全体。AI が毎回まるごと送る。
type Board struct {
	// Topic は今話していること。話題が変われば更新する。
	Topic string `json:"topic,omitempty"`
	// Summary は累積した要約の全文。AI が追加・統合・訂正する。
	// 行頭の記号や時刻の付け方は AI に任せる。
	Summary string `json:"summary,omitempty"`
	// AppliedThrough は要約に反映済みの確定行位置 (次に読む位置)。
	// 再接続した AI が続きから読めるようにする。
	AppliedThrough int `json:"applied_through,omitempty"`

	// Version は発行ごとに 1 から増える版番号 (sokki が採番)。
	Version int `json:"version"`
	// UpdatedAt は反映時刻 (sokki が設定)。
	UpdatedAt time.Time `json:"updated_at"`
}

// legacyBoard は旧形式 (固定分類) の会議ボード。
// 読み込み時に Summary へ移行するためだけに使う。
type legacyBoard struct {
	Background string       `json:"background,omitempty"`
	Decisions  []legacyItem `json:"decisions,omitempty"`
	Actions    []legacyItem `json:"actions,omitempty"`
	Open       []legacyItem `json:"open,omitempty"`
}

type legacyItem struct {
	Text  string `json:"text"`
	Owner string `json:"owner,omitempty"`
	Due   string `json:"due,omitempty"`
	At    string `json:"at,omitempty"`
}

func (i legacyItem) line() string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(i.Text))
	var meta []string
	if s := strings.TrimSpace(i.Owner); s != "" {
		meta = append(meta, "担当: "+s)
	}
	if s := strings.TrimSpace(i.Due); s != "" {
		meta = append(meta, "期限: "+s)
	}
	if len(meta) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(meta, " / "))
	}
	if s := strings.TrimSpace(i.At); s != "" {
		fmt.Fprintf(&b, " [%s]", s)
	}
	return b.String()
}

// UnmarshalJSON は新形式を読みつつ、旧形式の控えも受け付ける。
//
// 旧形式の固定分類は要約本文へ移し、内容を失わないようにする。
func (b *Board) UnmarshalJSON(data []byte) error {
	type plain Board // 再帰を避ける
	var v struct {
		plain
		legacyBoard
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*b = Board(v.plain)
	if strings.TrimSpace(b.Summary) == "" {
		if migrated := migrateLegacy(v.legacyBoard); migrated != "" {
			b.Summary = migrated
		}
	}
	return nil
}

// migrateLegacy は旧形式の内容を要約本文へ移す。
func migrateLegacy(l legacyBoard) string {
	var lines []string
	add := func(prefix string, items []legacyItem) {
		for _, it := range items {
			if strings.TrimSpace(it.Text) == "" {
				continue
			}
			lines = append(lines, "・"+prefix+it.line())
		}
	}
	add("決まったこと: ", l.Decisions)
	add("やること: ", l.Actions)
	add("未決: ", l.Open)
	if s := strings.TrimSpace(l.Background); s != "" {
		lines = append(lines, "・経緯: "+s)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// IsEmpty は中身が何も無いかを返す。
func (b *Board) IsEmpty() bool {
	if b == nil {
		return true
	}
	return strings.TrimSpace(b.Topic) == "" && strings.TrimSpace(b.Summary) == ""
}

// SummaryLines は要約本文を行に分ける (空行は落とす)。
func (b *Board) SummaryLines() []string {
	var out []string
	for _, l := range strings.Split(b.Summary, "\n") {
		if s := strings.TrimRight(l, " \t"); strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// EmptyPlaceholder は要約が無いときに出す行を返す。
//
// 自動要約が無効なら「自動で作成される」と約束しない。
func EmptyPlaceholder(autoSummary bool) string {
	if autoSummary {
		return "（まだ要約はありません。発言がたまると自動で作成されます）"
	}
	return "（まだ要約はありません）"
}

// Lines は画面の会議ボード領域に出す行を返す。
// 幅の調整 (折り返し) は描画側が行う。
//
// 空の場合は自動要約を有効とみなした文言を返す。
// 無効の場合は LinesFor を使う。
func (b *Board) Lines() []string { return b.LinesFor(true) }

// LinesFor は自動要約の有無を踏まえた表示行を返す。
func (b *Board) LinesFor(autoSummary bool) []string {
	if b.IsEmpty() {
		return []string{EmptyPlaceholder(autoSummary)}
	}
	var out []string
	if s := strings.TrimSpace(b.Topic); s != "" {
		out = append(out, "今の話題: "+s)
	}
	if body := b.SummaryLines(); len(body) > 0 {
		out = append(out, "要約:")
		out = append(out, body...)
	}
	return out
}

// Markdown は保存用の Markdown を返す。
// 画面の会議ボードと同じ内容を保持する。
func (b *Board) Markdown(meetingName string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s 会議ボード\n\n", meetingName)
	fmt.Fprintf(&sb, "- 版: %d\n", b.Version)
	if !b.UpdatedAt.IsZero() {
		fmt.Fprintf(&sb, "- 更新: %s\n", b.UpdatedAt.Format("2006-01-02 15:04:05"))
	}
	if b.AppliedThrough > 0 {
		fmt.Fprintf(&sb, "- 反映済みの発言: %d 行目まで\n", b.AppliedThrough)
	}
	sb.WriteString("\n")

	if b.IsEmpty() {
		sb.WriteString("（まだ要約はありません）\n")
		return sb.String()
	}
	if s := strings.TrimSpace(b.Topic); s != "" {
		fmt.Fprintf(&sb, "## 今の話題\n\n%s\n\n", s)
	}
	if body := strings.TrimSpace(b.Summary); body != "" {
		fmt.Fprintf(&sb, "## 要約\n\n%s\n", body)
	}
	return sb.String()
}
