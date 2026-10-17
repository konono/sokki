package mcp

// toolDef は MCP の tools/list で返すツール定義。
type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// 会議向け要約の観点。ツール説明に含めて AI の判断を助ける。
const summaryGuidance = `要約は固定の分類 (決定 / 宿題 / 未決 など) に ` +
	`振り分けず、話題ごとに論点・結論・理由を統合する。` +
	`発言 1 行につき 1 項目を作らず、同じ話題は原則 1～3 項目にまとめる。` +
	`予告と本編などで同じ内容が繰り返されたら一度だけ残す。` +
	`過去の要約を読み直し、新しい発言に応じて追加・統合・訂正する ` +
	`(単純な末尾への追記で重複を増やさない)。時刻は参照が必要な箇所だけに添える。` +
	`発言から確認できない決定・担当・期限は作らない。` +
	`画面の下側に収まる量を優先する。`

func obj(props map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func toolDefs() []toolDef {
	return []toolDef{
		{
			Name: "start_or_attach",
			Description: "会議の文字起こしを開始する。すでに録音中ならその会議を返し、" +
				"新しいキャプチャは開始しない。録音がなければ panecom で専用 pane を開いて " +
				"sokki を起動する。フォーカスは呼び出し元の pane に残す。" +
				"panecom や Zellij/tmux が使えない場合は音声デバイスを開かずエラーを返す。",
			InputSchema: obj(map[string]any{
				"output_dir": str("保存先ディレクトリ。相対パスはこの呼び出しの作業ディレクトリ基準。" +
					"省略時は設定ファイルの output.directory、なければ transcripts"),
				"language": str("認識する言語 (ja / en / auto)。省略時は設定に従う"),
				"device":   str("入力デバイス名。省略時は設定に従う"),
				"auto_summary": map[string]any{"type": "boolean", "description": "会議ボードを発言から自動更新するか (既定 true)。" +
					"有効にすると会議テキストが AI プロバイダーへ送られる"},
				"summary_provider": str("自動要約に使う AI (claude / codex)。省略時は設定に従う"),
			}),
		},
		{
			Name: "get_status",
			Description: "起動中の会議を確認する。会議 ID、状態 (starting/loading/recording/stopped/failed)、" +
				"開始時刻、pane role、文字起こしと会議ボードの保存先を返す。" +
				"モデル読み込み中・デバイス不在・起動失敗を区別できる。" +
				"summary に自動要約の状態 (有効/状態/版/未処理行数/最終要約の結果) が入る。",
			InputSchema: obj(map[string]any{
				"id": str("会議 ID。省略時は録音中の会議"),
			}),
		},
		{
			Name: "read_transcript",
			Description: "指定した位置以降の確定行を読む。next_from を次回に渡すと、" +
				"重複も欠落もなく続きから読める。",
			InputSchema: obj(map[string]any{
				"from":  intProp("読み始める行位置 (0 始まり)。前回の next_from を渡す"),
				"limit": intProp("返す行数の上限 (0 なら全部)"),
				"id":    str("会議 ID。省略時は録音中の会議"),
			}),
		},
		{
			Name: "read_context",
			Description: "確定行に加え、現在の暫定行と既存の会議ボードを読む。" +
				"質問に答える前にこれを呼ぶとよい。暫定行は未確定として扱い、" +
				"後で確定行に訂正されても同じ発言とみなす。",
			InputSchema: obj(map[string]any{
				"from":  intProp("読み始める行位置 (0 始まり)。前回の next_from を渡す"),
				"limit": intProp("返す行数の上限 (0 なら全部)"),
				"id":    str("会議 ID。省略時は録音中の会議"),
			}),
		},
		{
			Name: "publish_summary",
			Description: "会議ボード全体を渡して画面に反映し、同じ会議名の Markdown に保存する。" +
				"通常は自動要約ワーカーが更新するため、手動で上書きしたいときだけ使う。" +
				"画面反映と保存は同じ操作で、保存できなければ失敗を返す。" +
				"古い項目を残す・統合する・訂正するかは呼び出し側が判断し、毎回全体を送る。" +
				"過去の版は履歴ファイルに残る。" + summaryGuidance,
			InputSchema: obj(map[string]any{
				"topic": str("今話していること (話題が変われば更新する)"),
				"summary": str("累積した要約の全文。固定の分類は作らず、" +
					"発言に沿った自然な文章で要点を積み重ねる"),
				"applied_through": intProp("要約に反映済みの確定行位置 " +
					"(read_context / read_transcript の next_from を渡す)"),
				"id": str("会議 ID。省略時は録音中の会議"),
			}),
		},
		{
			Name: "stop_session",
			Description: "録音の通常終了を依頼し、未処理の確定行が保存されるのを待つ。" +
				"応答時点で文字起こしは保存済み。最終要約は続行中 (final_summary.state=pending) で、" +
				"結果は get_status の summary.final_state で確認する。",
			InputSchema: obj(map[string]any{
				"id": str("会議 ID。省略時は録音中の会議"),
			}),
		},
	}
}
