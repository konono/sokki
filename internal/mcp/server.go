package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/konono/sokki/internal/board"
	"github.com/konono/sokki/internal/config"
	"github.com/konono/sokki/internal/ipc"
	"github.com/konono/sokki/internal/session"
)

// Launcher は録音プロセスを専用 pane で起動する処理。
// テストでは模擬 panecom に差し替える。
type Launcher interface {
	// Available は panecom / 多重化セッションの利用可否を調べる。
	// 利用できなければエラーを返し、音声デバイスは開かない。
	Available(ctx context.Context) error
	// Launch は pane を開いて sokki を起動し、記録された会議 ID を返す。
	Launch(ctx context.Context, opt LaunchOptions) (*session.Record, error)
}

// LaunchOptions は start_or_attach の引数。
type LaunchOptions struct {
	OutputDir        string
	Profile          string
	ProfileOutputDir string
	Language         string
	Device           string
	// WorkDir は相対パスの基準 (呼び出し時の作業ディレクトリ)。
	WorkDir string
	// MuxSession は Zellij / tmux のセッション識別子。
	MuxSession string
	// AutoSummary は自動要約を有効にするか。
	AutoSummary          bool
	AutoSummarySpecified bool
	// SummaryProvider は自動要約に使う AI。
	SummaryProvider string
}

// Server は MCP の stdio サーバー。
type Server struct {
	launcher Launcher
	// now はテスト用の時刻注入。
	now func() time.Time
}

// New は Server を作る。
func New(l Launcher) *Server {
	return &Server{launcher: l, now: time.Now}
}

// Serve は stdio 上で MCP の要求を処理する。
//
// 戻ってもそれは接続の終了であって、録音の終了ではない。
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	c := newConn(in, out)
	for {
		if ctx.Err() != nil {
			return nil
		}
		req, err := c.read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil // AI が接続を閉じた。録音は継続する。
			}
			return err
		}
		if err := s.dispatch(ctx, c, req); err != nil {
			return err
		}
	}
}

func (s *Server) dispatch(ctx context.Context, c *conn, req *request) error {
	switch req.Method {
	case "initialize":
		return c.replyResult(req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "sokki", "version": "1"},
		})
	case "notifications/initialized", "initialized":
		return nil
	case "ping":
		return c.replyResult(req.ID, map[string]any{})
	case "tools/list":
		return c.replyResult(req.ID, map[string]any{"tools": toolDefs()})
	case "tools/call":
		return s.callTool(ctx, c, req)
	default:
		return c.replyError(req.ID, codeMethodNotFound, "未対応のメソッド: "+req.Method)
	}
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) callTool(ctx context.Context, c *conn, req *request) error {
	var p callParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return c.replyError(req.ID, codeInvalidParams, "引数を解釈できません: "+err.Error())
		}
	}
	result, err := s.runTool(ctx, p.Name, p.Arguments)
	if err != nil {
		// ツールの失敗は JSON-RPC エラーではなく isError で返す (MCP の作法)。
		return c.replyResult(req.ID, toolResult(map[string]any{
			"ok":    false,
			"error": errText(err),
		}, true))
	}
	return c.replyResult(req.ID, toolResult(result, false))
}

// toolResult は MCP の tools/call 応答を組み立てる。
func toolResult(payload any, isError bool) map[string]any {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		data = []byte(fmt.Sprintf("%v", payload))
	}
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(data)}},
		"isError":           isError,
		"structuredContent": payload,
	}
}

type commonArgs struct {
	ID    string `json:"id"`
	From  int    `json:"from"`
	Limit int    `json:"limit"`
}

// RunToolForTest はツールを直接呼ぶ (テスト・検証用)。
func (s *Server) RunToolForTest(name string, raw json.RawMessage) (any, error) {
	return s.runTool(context.Background(), name, raw)
}

func (s *Server) runTool(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	switch name {
	case "start_or_attach":
		return s.startOrAttach(ctx, raw)
	case "get_status":
		return s.getStatus(raw)
	case "read_transcript":
		return s.readLines(raw, false)
	case "read_context":
		return s.readLines(raw, true)
	case "publish_summary":
		return s.publishSummary(raw)
	case "stop_session":
		return s.stopSession(raw)
	default:
		return nil, fmt.Errorf("未対応のツール: %s", name)
	}
}

// resolveRecord は対象の会議を決める。
//
// ID 省略時は「今の Zellij / tmux セッション内」の録音だけを対象にする。
// 別セッションの録音へ誤って接続・停止しないため。
func resolveRecord(id string) (*session.Record, error) {
	mux := session.CurrentMuxSession()
	if id != "" {
		r, err := session.Load(id)
		if err != nil {
			return nil, fmt.Errorf("会議 '%s' が見つかりません", id)
		}
		// 録音中の会議を操作する場合は、所属セッションを検証する
		// (別セッションの録音を止めない)。
		if r.Active() && !r.InSession(mux) {
			return nil, outOfSessionError(r, mux)
		}
		return r, nil
	}
	if mux == "" {
		return nil, errNoMuxSession
	}
	r, err := session.FindActive(mux)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf(
			"このセッションで録音中の会議がありません\n対処: start_or_attach で開始してください")
	}
	return r, nil
}

// errNoMuxSession は Zellij / tmux のセッションを特定できない場合のエラー。
var errNoMuxSession = fmt.Errorf(
	"MCP プロセスから Zellij / tmux のセッションを特定できません\n" +
		"対処: 呼び出し元のシェルで panecom whoami と sokki mcp --check-panecom を確認してください\n" +
		"      (別セッションの録音を誤って操作しないため、会議を自動選択していません)")

// outOfSessionError は別セッションの録音を操作しようとした場合のエラー。
func outOfSessionError(r *session.Record, mux string) error {
	where := r.MuxSession
	if where == "" {
		where = "(不明)"
	}
	cur := mux
	if cur == "" {
		cur = "(不明)"
	}
	return fmt.Errorf(
		"会議 '%s' は別の Zellij / tmux セッションで録音中です (録音: %s / 現在: %s)\n"+
			"対処: その会議を操作するには、同じセッションから実行してください",
		r.ID, where, cur)
}

func recordPayload(r *session.Record) map[string]any {
	out := map[string]any{
		"id":              r.ID,
		"state":           string(r.State),
		"pane_role":       r.PaneRole,
		"transcript_path": r.TranscriptPath,
		"board_path":      r.BoardPath,
		"history_path":    r.HistoryPath,
		"pid":             r.PID,
		"active":          r.Active(),
		"summary_version": r.SummaryVersion,
		"mux_session":     r.MuxSession,
		"board_format":    r.BoardFormat,
		"final_summary":   finalStateOf(r),
		"auto_summary":    r.AutoSummary,
	}
	if r.SummaryNote != "" {
		out["auto_summary_note"] = r.SummaryNote
	}
	if !r.StartedAt.IsZero() {
		out["started_at"] = r.StartedAt.Format(time.RFC3339)
	}
	if !r.SummaryAt.IsZero() {
		out["summary_at"] = r.SummaryAt.Format(time.RFC3339)
	}
	if r.Error != "" {
		out["error"] = r.Error
	}
	return out
}

func (s *Server) startOrAttach(ctx context.Context, raw json.RawMessage) (any, error) {
	args := struct {
		OutputDir       string  `json:"output_dir"`
		Profile         *string `json:"profile"`
		Language        string  `json:"language"`
		Device          string  `json:"device"`
		AutoSummary     *bool   `json:"auto_summary"`
		SummaryProvider string  `json:"summary_provider"`
	}{}
	var fields map[string]json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("引数を解釈できません: %w", err)
		}
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("引数を解釈できません: %w", err)
		}
	}
	var profileName, profileOutputDir string
	profileAutoSummary := config.DefaultAutoSummary
	if _, supplied := fields["profile"]; supplied {
		if args.Profile == nil {
			return nil, fmt.Errorf("profile名が空です")
		}
		profileName = *args.Profile
		if profileName == "" {
			return nil, fmt.Errorf("profile名が空です")
		}
		path, err := config.DefaultConfigPath()
		if err != nil {
			return nil, err
		}
		file, err := config.Load(path, false)
		if err != nil {
			return nil, err
		}
		profileCfg := config.Default()
		if err := file.ApplySelected(&profileCfg, profileName, path); err != nil {
			return nil, err
		}
		profileOutputDir = profileCfg.OutputDir
		profileAutoSummary = profileCfg.AutoSummary
	}
	// CLI と同じ既定値を使う。明示指定があればその値を優先する。
	autoSummary := config.DefaultAutoSummary
	if profileName != "" && args.AutoSummary == nil {
		autoSummary = profileAutoSummary
	}
	if args.AutoSummary != nil {
		autoSummary = *args.AutoSummary
	}

	// panecom / 多重化セッションが使えないなら、音声デバイスを開かずに失敗する。
	// 会議の自動選択より先に確認する (セッション外から別の録音へ接続しない)。
	if err := s.launcher.Available(ctx); err != nil {
		return nil, err
	}
	mux := session.CurrentMuxSession()
	if mux == "" {
		return nil, errNoMuxSession
	}

	// 同じ Zellij / tmux セッション内に録音中があれば再利用する (二重起動しない)。
	if existing, err := session.FindActive(mux); err == nil && existing != nil {
		out := recordPayload(existing)
		// 接続先は録音中なので、会議ボードの版は録音プロセスの実値に揃える
		// (記録の値は保存時点のもので、自動要約の更新に追いついていない)。
		overlayLive(existing, out)
		out["attached"] = true
		out["note"] = "すでに録音中の会議に接続しました。新しいキャプチャは開始していません。"
		if profileName != "" {
			out["profile_requested"] = profileName
			out["note"] = "すでに録音中の会議に接続しました。指定profileは既存会議に適用していません。"
		}
		return out, nil
	}

	wd, _ := os.Getwd()
	rec, err := s.launcher.Launch(ctx, LaunchOptions{
		OutputDir:            args.OutputDir,
		Profile:              profileName,
		ProfileOutputDir:     profileOutputDir,
		Language:             args.Language,
		Device:               args.Device,
		WorkDir:              wd,
		MuxSession:           mux,
		AutoSummary:          autoSummary,
		AutoSummarySpecified: args.AutoSummary != nil,
		SummaryProvider:      args.SummaryProvider,
	})
	if err != nil {
		return nil, err
	}
	out := recordPayload(rec)
	out["attached"] = false
	// 要求値ではなく、録音プロセスが報告した実際の状態で案内する。
	out["auto_summary"] = rec.AutoSummary
	out["auto_summary_requested"] = autoSummary
	note := "専用 pane で録音を開始しました。フォーカスは呼び出し元に残しています。"
	switch {
	case rec.AutoSummary:
		note += " 会議ボードは発言から自動で作成・更新されます" +
			" (要約のため会議テキストが AI プロバイダーへ送られます)。"
	case autoSummary:
		note += " 自動要約は開始できませんでした"
		if rec.SummaryNote != "" {
			note += ": " + rec.SummaryNote
		}
		note += "。文字起こしは続きます。会議ボードは publish_summary で更新できます。"
	default:
		note += " 自動要約は無効です。"
	}
	out["note"] = note
	return out, nil
}

func (s *Server) getStatus(raw json.RawMessage) (any, error) {
	var args commonArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &args)
	}
	if args.ID == "" {
		if session.CurrentMuxSession() == "" {
			return nil, errNoMuxSession
		}
		// 録音中が無ければ、直近の記録を状態付きで返す。
		if active, _ := session.FindActive(session.CurrentMuxSession()); active == nil {
			records, _ := session.List()
			mux := session.CurrentMuxSession()
			var recent *session.Record
			for _, r := range records {
				if r.InSession(mux) {
					recent = r
					break
				}
			}
			if recent == nil {
				return map[string]any{
					"ok":      true,
					"running": false,
					"note":    "このセッションで録音中の会議はありません。start_or_attach で開始できます。",
				}, nil
			}
			out := recordPayload(recent)
			out["running"] = false
			out["summary"] = map[string]any{
				"final_state": finalStateOf(recent),
				"version":     recent.SummaryVersion,
			}
			out["note"] = "録音中の会議はありません。直近の会議を返しています。"
			return out, nil
		}
	}
	r, err := resolveRecord(args.ID)
	if err != nil {
		return nil, err
	}
	out := recordPayload(r)
	out["running"] = r.Active()
	// 停止後も案内どおり summary.final_state で結果を確認できるようにする。
	out["summary"] = map[string]any{
		"final_state": finalStateOf(r),
		"version":     r.SummaryVersion,
	}

	overlayLive(r, out)
	return out, nil
}

// overlayLive は録音中の実状態を応答へ重ねる。
//
// session 記録の summary_version は保存のたびにしか進まないため、
// 録音中は IPC で録音プロセスへ問い合わせた値を使う。
// get_status と start_or_attach の両方から呼ぶ (片方だけ古い版を返さない)。
func overlayLive(r *session.Record, out map[string]any) {
	if r == nil || !r.Active() {
		return
	}
	resp, err := ipc.Call(r.SocketPath, ipc.Request{Op: ipc.OpStatus})
	if err != nil {
		return // 届かなければ記録の値のまま返す
	}
	if resp.State != "" {
		out["state"] = resp.State
	}
	out["summary_version"] = resp.SummaryVersion
	if resp.Interim != "" {
		out["interim"] = resp.Interim
	}
	if resp.Summary != nil {
		// 録音中は実状態を使いつつ、最終要約の欄は残す。
		sum := resp.Summary
		if _, ok := sum["final_state"]; !ok {
			sum["final_state"] = finalStateOf(r)
		}
		out["summary"] = sum
	}
}

func (s *Server) readLines(raw json.RawMessage, withContext bool) (any, error) {
	var args commonArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &args)
	}
	r, err := resolveRecord(args.ID)
	if err != nil {
		return nil, err
	}

	op := ipc.OpTranscript
	if withContext {
		op = ipc.OpContext
	}
	// 録音が終わっていてもファイルから読めるようにする。
	if !r.Active() {
		return readFromFile(r, args.From, args.Limit, withContext)
	}
	resp, err := ipc.Call(r.SocketPath, ipc.Request{Op: op, From: args.From, Limit: args.Limit})
	if err != nil {
		// プロセスが落ちた直後などはファイルへフォールバックする。
		return readFromFile(r, args.From, args.Limit, withContext)
	}
	out := map[string]any{
		"ok":        true,
		"id":        r.ID,
		"lines":     resp.Lines,
		"next_from": resp.NextFrom,
	}
	if withContext {
		out["interim"] = resp.Interim
		out["interim_at"] = resp.InterimAt
		out["interim_is_unconfirmed"] = true
		out["board"] = resp.Board
		out["summary_version"] = resp.SummaryVersion
	}
	return out, nil
}

// readFromFile は録音終了後に確定行と会議ボードをファイルから読む。
func readFromFile(r *session.Record, from, limit int, withContext bool) (any, error) {
	lines, err := ReadTranscriptFile(r.TranscriptPath, from, limit)
	if err != nil {
		return nil, err
	}
	next := from + len(lines)
	out := map[string]any{
		"ok":        true,
		"id":        r.ID,
		"lines":     lines,
		"next_from": next,
		"recording": false,
		"note": "録音は終了しています。保存済みファイルから読みました。" +
			"publish_summary で Markdown の更新は続けられます (画面には反映されません)。",
	}
	if withContext {
		out["interim"] = ""
		out["interim_is_unconfirmed"] = true
		// 最後に公開した会議ボードを控えから読む (続きから更新できるように)。
		store := board.NewStore(r.TranscriptPath)
		if err := store.LoadState(); err == nil {
			cur := store.Current()
			if cur.Version > 0 {
				out["board"] = cur
				out["summary_version"] = cur.Version
			} else {
				out["board"] = nil
				out["summary_version"] = 0
			}
		} else {
			out["board"] = nil
			out["summary_version"] = 0
			out["board_error"] = err.Error()
		}
	}
	return out, nil
}

func (s *Server) publishSummary(raw json.RawMessage) (any, error) {
	var args struct {
		commonArgs
		Topic          string `json:"topic"`
		Summary        string `json:"summary"`
		AppliedThrough int    `json:"applied_through"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("会議ボードを解釈できません: %w", err)
		}
	}
	r, err := resolveRecord(args.ID)
	if err != nil {
		return nil, err
	}

	b := &board.Board{
		Topic:          args.Topic,
		Summary:        args.Summary,
		AppliedThrough: args.AppliedThrough,
	}

	// 録音が終わっていても議事録の更新は続けられる。
	// ただし画面には反映できないので、その旨を返す。
	if !r.Active() {
		return publishOffline(r, b)
	}

	// 録音中の相手が新形式を扱えない場合は送らない。
	// 旧形式の録音プロセスは topic / summary を解釈できず、
	// 要約が欠落した会議ボードを書いてしまう。
	if !r.SupportsSummaryBoard() {
		return nil, fmt.Errorf(
			"会議 '%s' は古い形式で録音中のため、新しい要約形式を反映できません "+
				"(board_format: %q)\n"+
				"対処: 録音を新しい sokki で開始し直してください。"+
				"録音終了後であれば、この会議の Markdown も新形式で更新できます",
			r.ID, r.BoardFormat)
	}

	resp, err := ipc.Call(r.SocketPath, ipc.Request{Op: ipc.OpPublish, Board: b})
	if err != nil {
		return nil, fmt.Errorf("会議ボードを反映できません: %w", err)
	}
	return map[string]any{
		"ok":              true,
		"id":              r.ID,
		"summary_version": resp.SummaryVersion,
		"summary_at":      resp.SummaryAt,
		"board_path":      resp.BoardPath,
		"history_path":    resp.HistoryPath,
		"screen_updated":  true,
		"note":            "画面に反映し、Markdown に保存しました。",
	}, nil
}

// publishOffline は録音終了後に Markdown だけを更新する。
//
// 最後の版を引き継いで版番号を進め、履歴も残す。
func publishOffline(r *session.Record, b *board.Board) (any, error) {
	if r.TranscriptPath == "" {
		return nil, fmt.Errorf("会議 '%s' の保存先が分かりません", r.ID)
	}
	store := board.NewStore(r.TranscriptPath)
	if err := store.LoadState(); err != nil {
		return nil, err
	}
	saved, err := store.Publish(*b, time.Now())
	if err != nil {
		return nil, err
	}
	// 記録にも反映しておく (再接続した AI が版を追えるように)。
	r.SummaryVersion = saved.Version
	r.SummaryAt = saved.UpdatedAt
	_ = session.Save(r)

	return map[string]any{
		"ok":              true,
		"id":              r.ID,
		"summary_version": saved.Version,
		"summary_at":      saved.UpdatedAt.Format(time.RFC3339),
		"board_path":      store.MarkdownPath(),
		"history_path":    store.HistoryPath(),
		"screen_updated":  false,
		"note": "録音は終了しているため画面には反映していません。" +
			"Markdown と履歴は更新しました。",
	}, nil
}

func (s *Server) stopSession(raw json.RawMessage) (any, error) {
	var args commonArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &args)
	}
	r, err := resolveRecord(args.ID)
	if err != nil {
		return nil, err
	}
	if !r.Active() {
		return map[string]any{
			"ok":              true,
			"id":              r.ID,
			"state":           string(r.State),
			"already_stopped": true,
			"transcript_path": r.TranscriptPath,
			"board_path":      r.BoardPath,
			"final_summary":   map[string]any{"state": finalStateOf(r)},
		}, nil
	}
	// 未処理の確定行の保存を待つため、余裕を持った待ち時間にする。
	resp, err := ipc.CallTimeout(r.SocketPath, ipc.Request{Op: ipc.OpStop}, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("停止を依頼できません: %w", err)
	}
	return map[string]any{
		"ok": true,
		"id": r.ID,
		"transcript": map[string]any{
			"state": "saved",
			"path":  resp.TranscriptPath,
		},
		"final_summary": map[string]any{
			"state": "pending",
		},
		"state":      resp.State,
		"board_path": resp.BoardPath,
		"note": "文字起こしの保存は完了しました。最終要約は実行中です。" +
			"結果は get_status (summary.final_state) で確認できます。",
	}, nil
}

// AbsOutputDir は相対パスを基準ディレクトリで絶対パスにする。
// finalStateOf は記録に残った最終要約の結果を返す。
func finalStateOf(r *session.Record) string {
	if r.FinalSummary == "" {
		return "unknown"
	}
	return r.FinalSummary
}

func AbsOutputDir(dir, workDir string) string {
	if dir == "" {
		return ""
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Clean(filepath.Join(workDir, dir))
}
