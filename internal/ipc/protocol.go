// Package ipc は録音プロセスと MCP サーバーの間のローカル通信を担う。
//
// Unix domain socket 上の 1 行 1 メッセージ (JSON) で、
// pane のシェルへ文字列を注入せずに要約の受け渡しと停止依頼を行う。
// MCP 接続が切れても録音プロセスは動き続ける (接続と録音の寿命は独立)。
package ipc

import (
	"github.com/konono/sokki/internal/board"
)

// Op は要求の種類。
type Op string

const (
	// OpStatus は会議の状態を返す。
	OpStatus Op = "status"
	// OpTranscript は確定行を指定位置以降で返す。
	OpTranscript Op = "transcript"
	// OpContext は確定行 + 現在の暫定行 + 会議ボードを返す。
	OpContext Op = "context"
	// OpPublish は会議ボードを反映し Markdown に保存する。
	OpPublish Op = "publish"
	// OpStop は通常終了を依頼する。
	OpStop Op = "stop"
)

// Request は録音プロセスへの要求。
type Request struct {
	Op Op `json:"op"`
	// From は transcript / context で読み始める行位置 (0 始まり)。
	From int `json:"from,omitempty"`
	// Limit は返す行数の上限 (0 なら制限なし)。
	Limit int `json:"limit,omitempty"`
	// Board は publish で渡す会議ボード。
	Board *board.Board `json:"board,omitempty"`
}

// Line は確定した発言 1 行。
type Line struct {
	// Index は 0 始まりの行位置。次回はここ + 1 から読む。
	Index int `json:"index"`
	// At は会議開始からの時刻 (HH:MM:SS)。
	At string `json:"at"`
	// Text は確定した本文。
	Text string `json:"text"`
}

// Response は録音プロセスからの応答。
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`

	// 状態
	ID             string `json:"id,omitempty"`
	State          string `json:"state,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
	PaneRole       string `json:"pane_role,omitempty"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	BoardPath      string `json:"board_path,omitempty"`
	HistoryPath    string `json:"history_path,omitempty"`

	// 読み取り結果
	Lines []Line `json:"lines,omitempty"`
	// NextFrom は次回読み始める位置 (重複・欠落を避けるため常に返す)。
	NextFrom int `json:"next_from"`
	// Interim は現在の暫定行 (未確定)。空なら発話中でない。
	Interim string `json:"interim,omitempty"`
	// InterimAt は暫定行の発話開始時刻。
	InterimAt string `json:"interim_at,omitempty"`

	// Summary は自動要約ワーカーの状態 (任意)。
	Summary map[string]any `json:"summary,omitempty"`
	// Translation reports optional live translation readiness and pending work.
	Translation map[string]any `json:"translation,omitempty"`

	// 会議ボード
	Board          *board.Board `json:"board,omitempty"`
	SummaryVersion int          `json:"summary_version,omitempty"`
	SummaryAt      string       `json:"summary_at,omitempty"`
}
