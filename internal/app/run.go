// Package app は各コンポーネントを結線して実行する。
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/konono/sokki/internal/audio"
	"github.com/konono/sokki/internal/board"
	"github.com/konono/sokki/internal/config"
	"github.com/konono/sokki/internal/session"
	"github.com/konono/sokki/internal/transcript"
	"github.com/konono/sokki/internal/ui"
	"github.com/konono/sokki/internal/vad"
	"github.com/konono/sokki/internal/whisper"
)

// drainTimeout は終了時に未処理の確定区間を書き切るまで待つ上限。
const drainTimeout = 30 * time.Second

// shuttingDown は終了処理に入ったかどうか。
// 終了後は暫定結果を捨て、確定区間の保存だけを行う。
var shuttingDown atomic.Bool

// Run は文字起こしを開始し、ctx がキャンセルされるまで動作する。
func Run(ctx context.Context, cfg config.Config, con *ui.Console) error {
	shuttingDown.Store(false)
	traceDir := asrTraceDir()

	// 起動直後に会議記録を作る。これより前で失敗すると AI が
	// 原因を取得できないため、初期化のどの段階で失敗しても
	// failed として理由を残せるようにする。
	early, earlyErr := startEarlyRecord(cfg, time.Now())
	if earlyErr != nil {
		return earlyErr
	}
	fail := func(err error) error {
		markFailed(early, err)
		return err
	}

	if err := cfg.ValidateRecognition(); err != nil {
		return fail(err)
	}
	if cfg.Backend == config.BackendWhisper {
		if err := whisper.ValidateLocalURL(cfg.ServerURL); err != nil {
			return fail(err)
		}
	}
	modelPath, err := cfg.ResolveRecognitionModel()
	if err != nil {
		return fail(err)
	}
	dev, err := audio.ResolveDevice(ctx, cfg.FFmpegBin, cfg.Device)
	if err != nil {
		return fail(err)
	}

	con.Info("入力デバイス : [%d] %s", dev.Index, dev.Name)
	con.Info("認識 backend : %s", cfg.Backend)
	con.Info("確定処理     : %s", cfg.FinalStrategy)
	con.Info("モデル       : %s", modelPath)
	con.Info("言語         : %s", cfg.Language)
	con.Info("%s 起動中 (モデル読み込みに時間がかかります)...", cfg.Backend)

	client, err := startBackend(ctx, cfg, modelPath)
	if err != nil {
		return fail(err)
	}
	defer client.Close()

	start := time.Now()
	writer, err := transcript.New(cfg.OutputDir, start)
	if err != nil {
		return fail(err)
	}

	// 会議ボードと AI 連携 (MCP) のための状態・IPC を用意する。
	boards := board.NewStore(writer.Path())
	v := newView(con, cfg)
	lv := newLive(cfg.SessionID, cfg.PaneRole, writer.Path(), start, boards, v)
	stopAI, cancelAI := context.WithCancel(context.Background())
	lv.stopOnce = cancelAI

	ipcSrv, rec, err := startIPC(cfg, lv, early, start, writer.Path(), boards)
	if err != nil {
		_ = writer.Close(time.Now())
		return fail(err)
	}
	if ipcSrv != nil {
		defer ipcSrv.Close()
	}
	if rec != nil {
		defer finishRecord(rec, lv)
	}

	initialCap, err := audio.StartCapture(ctx, cfg.FFmpegBin, dev, config.SampleRate)
	if err != nil {
		markFailed(rec, err)
		_ = writer.Close(time.Now())
		return err
	}
	var cap captureReader = initialCap
	defer func() { _ = cap.Close() }()
	rawTrace, traceErr := startRawASRTrace(traceDir)
	if traceErr != nil {
		con.Warn("ASR 診断の開始に失敗しました: %v", traceErr)
	} else if rawTrace != nil {
		rawTrace.onIssue = func(err error) { con.Warn("ASR 診断の音声保存: %v", err) }
		cap = &tracedCaptureReader{captureReader: cap, trace: rawTrace}
		defer func() {
			if err := rawTrace.Close(); err != nil {
				con.Warn("ASR 診断の音声保存に失敗しました: %v", err)
			}
		}()
	}

	con.Info("保存先       : %s", writer.Path())
	if rec != nil {
		con.Info("会議 ID      : %s", rec.ID)
		con.Info("会議ボード   : %s", boards.MarkdownPath())
	}
	con.Info("録音を開始しました。Ctrl-C で終了します。")
	con.Info("%s", "----------------------------------------")

	v.Start()
	defer v.Stop()

	// 自動要約ワーカー。AI クライアントの接続とは独立して動き、
	// 確定発言がたまったら会議ボードを更新する。
	worker, werr := startSummarizer(cfg, lv)
	// 表示は「実際に動いているか」に揃える。
	// 起動できていないのに「自動更新」と約束しない。
	v.setAutoSummary(worker != nil)

	// recording への遷移は要約の起動結果が決まってから、1 回の保存で行う。
	// 先に recording を保存すると、起動を待っている AI が
	// 「要約は失敗」と誤って受け取る (waitForRecord は recording で返る)。
	lv.setState(session.StateRecording)
	markRecording(rec, lv, worker != nil, werr)

	if werr != nil {
		con.Warn("自動要約を開始できません: %v", werr)
		con.Info("自動要約     : 無効 (文字起こしは続行します)")
	}
	if worker != nil {
		con.Info("自動要約     : %s (%d 行 / %v ごと)",
			cfg.SummaryProvider, cfg.SummaryMinLines, cfg.SummaryMinWait)
		workerCtx, cancelWorker := context.WithCancel(context.Background())
		defer cancelWorker()
		go worker.Run(workerCtx)
	}

	seg := vad.New(vad.Options{
		SampleRate:      config.SampleRate,
		FrameSamples:    config.FrameSamples,
		SilenceHangover: cfg.SilenceHangover,
		MinSpeech:       cfg.MinSpeech,
		MaxSegment:      cfg.MaxSegment,
		Preroll:         cfg.Preroll,
		FloorRatio:      cfg.VADFloorRatio,
		AbsThreshold:    cfg.VADAbsThreshold,
		ShowInterim:     cfg.ShowInterim,
		InterimAfter:    cfg.InterimAfter,
		InterimInterval: cfg.InterimInterval,
	})

	// 推論は 1 本のワーカーで直列化する。確定を優先し、
	// 滞留中は暫定結果を捨てて取り込みを止めないようにする。
	//
	// ワーカーの context は Ctrl-C (ctx) とは分離する。終了時に
	// 残っている確定区間を取りこぼさず保存するため。
	workCtx, cancelWork := context.WithCancel(context.Background())
	defer cancelWork()

	disp := newDispatcher(32, drainTimeout)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		transcribeLoop(workCtx, cfg, client, writer, v, disp.events, lv, traceDir)
	}()

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	go func() {
		select {
		case <-stopAI.Done(): // AI からの停止依頼
			cancelRun()
		case <-runCtx.Done():
		}
	}()

	cap, readErr := captureWithRecovery(runCtx, cap, func() (captureReader, error) {
		next, err := audio.StartCapture(runCtx, cfg.FFmpegBin, dev, config.SampleRate)
		if err != nil || rawTrace == nil {
			return next, err
		}
		return &tracedCaptureReader{captureReader: next, trace: rawTrace}, nil
	}, seg, disp, func() {
		v.ClearInterim()
		lv.clearInterim()
		con.Warn("音声入力が止まったため、取り込みを再接続します")
	})

	// 取り込みを直ちに止めて音声デバイスを解放する。
	// (Loopback の "Mute when capturing" 下では、解放が遅れるほど
	//  会議音声が聞こえない時間が延びる)
	_ = cap.Close()

	// 終了処理: 残りの区間を確定させてからワーカーを閉じる。
	// 以降は暫定結果を出さず、確定分だけを処理する。
	shuttingDown.Store(true)
	if ev, ok := seg.Flush(); ok {
		disp.SendFinal(ev)
	}
	disp.Close()

	// 未処理の確定区間を書き切るのを待つ。
	// 推論が詰まっても終了できるよう上限を設ける。
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(drainTimeout):
		con.Warn("未処理の音声が残ったまま終了します (%v 待機)", drainTimeout)
		cancelWork()
		<-done
	}

	v.ClearInterim()
	end := time.Now()
	closeErr := writer.Close(end)
	// 保存完了を stop_session の待ち側へ知らせる。
	// 最終要約はこの後に行い、結果は記録と get_status で確認する。
	lv.markFinished(closeErr)

	if worker != nil {
		worker.Stop()
		con.Info("最後の要約を作成しています...")
		state := worker.Final(context.Background())
		con.Info("最終要約     : %s", state)
		if rec != nil {
			rec.FinalSummary = string(state)
		}
	}

	con.Info("%s", "----------------------------------------")
	con.Info("録音を終了しました (%s)", transcript.FormatOffset(end.Sub(start)))
	con.Info("確定行数     : %d", writer.Count())
	if n := disp.Dropped(); n > 0 {
		con.Warn("期限内に処理できず保存できなかった区間: %d 件", n)
	}
	if traceDir != "" {
		for _, discarded := range seg.Discarded() {
			if err := appendASRTrace(traceDir, asrTraceRecord{
				Kind: "vad_discard", StartMS: discarded.Start.Milliseconds(), EndMS: discarded.End.Milliseconds(),
				AudioSamps: discarded.Samples, SpeechSamples: discarded.SpeechSamples,
			}, nil); err != nil && asrTraceWarned.CompareAndSwap(false, true) {
				con.Warn("ASR 診断の保存に失敗しました: %v", err)
			}
		}
		if err := traceASRStats(traceDir, disp.Dropped()); err != nil && asrTraceWarned.CompareAndSwap(false, true) {
			con.Warn("ASR 診断の保存に失敗しました: %v", err)
		}
	}
	con.Info("保存先       : %s", writer.Path())

	if closeErr != nil {
		return fmt.Errorf("保存先ファイルを閉じられませんでした: %w", closeErr)
	}
	return readErr
}

// captureWithRecovery は ffmpeg が生きたまま PCM を返さなくなったとき、
// 未確定の発話を保存経路へ渡し、音声入力だけを開き直す。
// 入力サンプルが途切れない通常時は captureLoop と同じ動作になる。
func captureWithRecovery(ctx context.Context, cap captureReader,
	open func() (captureReader, error), seg *vad.Segmenter,
	disp *dispatcher, onStall func()) (captureReader, error) {
	for {
		err := captureLoop(ctx, cap, seg, disp)
		if ctx.Err() != nil {
			return cap, nil
		}
		if !errors.Is(err, audio.ErrStalled) {
			return cap, err
		}
		var traced *tracedCaptureReader
		if r, ok := cap.(*tracedCaptureReader); ok {
			traced = r
		}
		_ = cap.Close()
		if ev, ok := seg.Flush(); ok {
			disp.SendFinal(ev)
		}
		if onStall != nil {
			onStall()
		}
		next, openErr := open()
		if openErr != nil {
			return cap, fmt.Errorf("音声入力を再接続できません: %w", openErr)
		}
		if traced != nil {
			gap := time.Duration(0)
			if !traced.lastRead.IsZero() {
				gap = time.Since(traced.lastRead)
			}
			traced.trace.recoveries++
			if err := traceASRRecovery(traced.trace.dir, traced.trace.samples, gap); err != nil {
				traced.trace.report(err)
			}
		}
		cap = next
	}
}

// markRecording は録音開始を 1 回の保存で記録する。
//
// 状態と自動要約の結果を同時に書き、中途半端な記録を外へ見せない。
func markRecording(rec *session.Record, lv *live, summarizing bool, err error) {
	if rec == nil {
		return
	}
	rec.AutoSummary = summarizing
	switch {
	case err != nil:
		rec.SummaryNote = err.Error()
	case !summarizing:
		rec.SummaryNote = "自動要約は無効です (--auto-summary で有効化)"
	default:
		rec.SummaryNote = ""
	}
	updateRecord(rec, session.StateRecording, lv)
}

// captureLoop は PCM を読み続け、検出したイベントを dispatcher へ送る。
//
// ctx (Ctrl-C) は「取り込みを止めるか」だけに使う。既に検出した確定区間の
// 送信は ctx で中断しない (中断すると終了間際の発話を取りこぼす)。
func captureLoop(ctx context.Context, cap frameReader, seg *vad.Segmenter,
	disp *dispatcher) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		frame, err := cap.Read(config.FrameSamples)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, io.EOF) && ctx.Err() != nil {
				return nil
			}
			return err
		}
		for _, ev := range seg.Push(frame) {
			if ev.Kind == vad.Final {
				disp.SendFinal(ev)
				continue
			}
			disp.SendInterim(ev)
		}
	}
}

// transcribeLoop は区間を順に文字起こしして表示・保存する。
func transcribeLoop(ctx context.Context, cfg config.Config, client transcriber,
	writer *transcript.Writer, con *view, events <-chan vad.Event, lv *live, traceDirs ...string) {
	traceDir := asrTraceDir()
	if len(traceDirs) > 0 {
		traceDir = traceDirs[0]
	}
	for ev := range events {
		if ctx.Err() != nil {
			continue
		}
		// 終了処理中は暫定結果を捨て、確定区間だけを保存する。
		if ev.Kind == vad.Interim && shuttingDown.Load() {
			continue
		}
		// 暫定が溜まっている場合は最新だけを使う。
		if ev.Kind == vad.Interim && len(events) > 0 {
			continue
		}
		handleEventWithStrategy(ctx, client, writer, con, lv, ev, traceDir, cfg.FinalStrategy)
	}
}

// handleEvent は 1 区間を文字起こしして表示・保存する。
//
// 確定区間は、この時点で発話が終わっている。雑音・推論失敗・保存失敗の
// どの経路で抜けても暫定行を残さない (残すと、古い暫定と確定行が
// 画面に並び、AI 側も消えた発話を読み続ける)。
func handleEvent(ctx context.Context, client transcriber, writer *transcript.Writer,
	con *view, lv *live, ev vad.Event) {
	handleEventWithTrace(ctx, client, writer, con, lv, ev, asrTraceDir())
}

func handleEventWithTrace(ctx context.Context, client transcriber, writer *transcript.Writer,
	con *view, lv *live, ev vad.Event, traceDir string) {
	handleEventWithStrategy(ctx, client, writer, con, lv, ev, traceDir, config.FinalHybrid)
}

func handleEventWithStrategy(ctx context.Context, client transcriber, writer *transcript.Writer,
	con *view, lv *live, ev vad.Event, traceDir, finalStrategy string) {
	if ev.Kind != vad.Interim {
		defer clearInterim(con, lv)
	}

	var text string
	var err error
	startedInference := time.Now()
	var observation finalObservation
	if ev.Kind == vad.Final {
		if finalStrategy == config.FinalWhole {
			text, err = client.Transcribe(ctx, ev.Audio, config.SampleRate)
			observation = finalObservation{Whole: text, ReconcileWhole: transcript.Clean(text),
				RangeCount: 1, ReconcileMode: "whole", Result: text}
		} else if traceDir == "" {
			text, err = transcribeFinal(ctx, client, ev.Audio)
		} else {
			text, err = transcribeFinalObserved(ctx, client, ev.Audio, func(obs finalObservation) {
				observation = obs
			})
		}
	} else {
		text, err = client.Transcribe(ctx, ev.Audio, config.SampleRate)
	}
	if traceDir != "" {
		latency := time.Since(startedInference)
		var traceErr error
		if ev.Kind == vad.Final {
			traceErr = traceASRFinal(traceDir, ev, observation, err, latency)
		} else {
			traceErr = traceASRInterim(traceDir, ev, text, latency)
		}
		if traceErr != nil && asrTraceWarned.CompareAndSwap(false, true) {
			con.Warn("ASR 診断の保存に失敗しました: %v", traceErr)
		}
	}
	if err != nil {
		if ctx.Err() == nil {
			con.Warn("文字起こしに失敗しました: %v", err)
		}
		if ev.Kind == vad.Interim {
			return
		}
	}
	text = transcript.Clean(text)
	if text == "" || transcript.IsNoise(text) {
		clearInterim(con, lv)
		return
	}

	if ev.Kind == vad.Interim {
		at := transcript.FormatOffset(ev.Start)
		if lv != nil {
			lv.setInterim(at, text)
		}
		con.Interim(fmt.Sprintf("[%s] %s", at, text))
		return
	}

	line, err := writer.Append(ev.Start, ev.End, text)
	if err != nil {
		con.Warn("保存に失敗しました: %v", err)
		return
	}
	if line == "" {
		return
	}
	if lv != nil {
		at, body := splitLine(line)
		lv.addLine(at, body)
	}
	con.Log(line)
}

// clearInterim は画面と AI 向けの暫定行を消す。
func clearInterim(con *view, lv *live) {
	if lv != nil {
		lv.clearInterim()
	}
	con.ClearInterim()
}

// frameReader は PCM を供給する入力源 (テスト差し替え用)。
type frameReader interface {
	Read(n int) ([]int16, error)
}

type captureReader interface {
	frameReader
	Close() error
}

// transcriber は推論バックエンドの最小インタフェース (テスト差し替え用)。
type transcriber interface {
	Transcribe(ctx context.Context, pcm []int16, sampleRate int) (string, error)
	Close() error
}
