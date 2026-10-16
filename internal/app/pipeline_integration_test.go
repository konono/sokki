package app

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/konono/sokki/internal/config"
	"github.com/konono/sokki/internal/transcript"
	"github.com/konono/sokki/internal/ui"
	"github.com/konono/sokki/internal/vad"
	"github.com/konono/sokki/internal/whisper"
)

// 音声デバイス以外の全段 (区間検出 → whisper.cpp → 重複抑制 → 保存) を
// 実機の whisper-server で通す統合テスト。
//
//	MEETSCRIBE_E2E=1 go test ./internal/app/ -run Integration -v
func TestIntegrationPipelineEndToEnd(t *testing.T) {
	if os.Getenv("MEETSCRIBE_E2E") != "1" {
		t.Skip("MEETSCRIBE_E2E=1 のときだけ実行する")
	}
	home, _ := os.UserHomeDir()
	model := filepath.Join(home, ".local/share/talknote/models/ggml-large-v3-turbo.bin")
	if _, err := os.Stat(model); err != nil {
		t.Skipf("モデルが無い: %s", model)
	}

	utterances := []string{
		"本日の会議を始めます。",
		"議題は来週のリリース計画についてです。",
		"それでは、担当者から説明をお願いします。",
	}
	pcm := buildMeetingAudio(t, utterances)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client, err := whisper.Start(ctx, whisper.Options{
		ServerBin: "whisper-server", ModelPath: model, Language: "ja", Threads: 8,
	})
	if err != nil {
		t.Fatalf("whisper 起動: %v", err)
	}
	defer client.Close()

	dir := t.TempDir()
	start := time.Date(2026, 10, 16, 10, 0, 0, 0, time.Local)
	writer, err := transcript.New(dir, start)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	seg := vad.New(vad.Options{
		SampleRate:      config.SampleRate,
		FrameSamples:    config.FrameSamples,
		SilenceHangover: cfg.SilenceHangover,
		MinSpeech:       cfg.MinSpeech,
		MaxSegment:      cfg.MaxSegment,
		Preroll:         cfg.Preroll,
		FloorRatio:      cfg.VADFloorRatio,
		AbsThreshold:    cfg.VADAbsThreshold,
		ShowInterim:     false, // 確定結果の検証に集中する
	})

	events := make(chan vad.Event, 256)
	for off := 0; off+config.FrameSamples <= len(pcm); off += config.FrameSamples {
		for _, ev := range seg.Push(pcm[off : off+config.FrameSamples]) {
			events <- ev
		}
	}
	if ev, ok := seg.Flush(); ok {
		events <- ev
	}
	close(events)

	nFinal := len(events)
	t.Logf("検出した確定区間: %d 件 (発話 %d 件)", nFinal, len(utterances))
	if nFinal < len(utterances) {
		t.Errorf("区間検出が不足: %d 件 < %d 発話", nFinal, len(utterances))
	}

	transcribeLoop(ctx, cfg, client, writer, &view{con: ui.New()}, events, nil)
	if err := writer.Close(start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	t.Logf("保存内容:\n%s", content)

	// 1. 各発話の特徴語が保存されている。
	for _, want := range []string{"会議", "リリース", "担当"} {
		if !strings.Contains(content, want) {
			t.Errorf("保存内容に %q が無い", want)
		}
	}
	// 2. 行頭が [HH:MM:SS] 形式で、時刻が単調増加する。
	var prev time.Duration = -1
	lines := 0
	for _, line := range strings.Split(content, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines++
		if len(line) < 11 || line[0] != '[' || line[9] != ']' {
			t.Fatalf("行の形式が規約と違う: %q", line)
		}
		var h, m, s int
		if _, err := fmt.Sscanf(line[1:9], "%d:%d:%d", &h, &m, &s); err != nil {
			t.Fatalf("時刻を解釈できない: %q", line)
		}
		cur := time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second
		if cur < prev {
			t.Errorf("時刻が逆行した: %v → %v", prev, cur)
		}
		prev = cur
	}
	// 3. 重複抑制が効き、発話数から極端に増えていない。
	if lines == 0 {
		t.Fatal("確定行が 1 行も保存されなかった")
	}
	if lines > len(utterances)+2 {
		t.Errorf("行数が多すぎる (重複の疑い): %d 行 / %d 発話", lines, len(utterances))
	}
	// 4. 無音由来の定型句が混入していない。
	if strings.Contains(content, "ご視聴ありがとう") {
		t.Errorf("無音由来の定型句が保存された:\n%s", content)
	}
}

// buildMeetingAudio は発話の間に無音を挟んだ会議風の PCM を作る。
func buildMeetingAudio(t *testing.T, utterances []string) []int16 {
	t.Helper()
	gap := make([]int16, config.SampleRate*3/2) // 1.5 秒の無音
	out := append([]int16(nil), gap...)         // 先頭に無音 (雑音フロア学習用)
	for _, u := range utterances {
		out = append(out, synth(t, u)...)
		out = append(out, gap...)
	}
	return out
}

func synth(t *testing.T, text string) []int16 {
	t.Helper()
	dir := t.TempDir()
	aiff := filepath.Join(dir, "s.aiff")
	if err := exec.Command("say", "-v", "Kyoko", "-o", aiff, text).Run(); err != nil {
		t.Skipf("say を実行できない: %v", err)
	}
	raw, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", aiff, "-ac", "1", "-ar", "16000", "-f", "s16le", "-").Output()
	if err != nil {
		t.Fatalf("ffmpeg: %v", err)
	}
	pcm := make([]int16, len(raw)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	return pcm
}

// 日英混在会議の回帰テスト (音声デバイス不使用)。
//
// 無音で区切られた日本語区間と英語区間を language=auto で処理し、
// それぞれの区間が正しい言語で出力されることを確認する。
//
// 制限: 言語判定は VAD の確定区間ごとに行われる。無音を挟まず
// 同一区間内で言語が切り替わる場合は保証されない (README に明記)。
func TestIntegrationMixedLanguageAutoPerSegment(t *testing.T) {
	if os.Getenv("MEETSCRIBE_E2E") != "1" {
		t.Skip("MEETSCRIBE_E2E=1 のときだけ実行する")
	}
	home, _ := os.UserHomeDir()
	model := filepath.Join(home, "Library/Application Support/superwhisper/ggml-large-v3-turbo.bin")
	if _, err := os.Stat(model); err != nil {
		model = filepath.Join(home, ".local/share/talknote/models/ggml-large-v3-turbo.bin")
	}
	if _, err := os.Stat(model); err != nil {
		t.Skipf("モデルが無い: %s", model)
	}

	gap := make([]int16, config.SampleRate*3/2) // 1.5 秒の無音で区切る
	pcm := append([]int16(nil), gap...)
	pcm = append(pcm, synthVoice(t, "Kyoko", "本日の会議を始めます。")...)
	pcm = append(pcm, gap...)
	pcm = append(pcm, synthVoice(t, "Samantha", "Let's review the release plan.")...)
	pcm = append(pcm, gap...)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client, err := whisper.Start(ctx, whisper.Options{
		ServerBin: "whisper-server", ModelPath: model, Language: "auto", Threads: 8,
	})
	if err != nil {
		t.Fatalf("whisper 起動 (language=auto): %v", err)
	}
	defer client.Close()

	dir := t.TempDir()
	start := time.Date(2026, 10, 16, 11, 0, 0, 0, time.Local)
	writer, err := transcript.New(dir, start)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Language = "auto"
	seg := vad.New(vad.Options{
		SampleRate: config.SampleRate, FrameSamples: config.FrameSamples,
		SilenceHangover: cfg.SilenceHangover, MinSpeech: cfg.MinSpeech,
		MaxSegment: cfg.MaxSegment, Preroll: cfg.Preroll,
		FloorRatio: cfg.VADFloorRatio, AbsThreshold: cfg.VADAbsThreshold,
		ShowInterim: false,
	})

	events := make(chan vad.Event, 64)
	for off := 0; off+config.FrameSamples <= len(pcm); off += config.FrameSamples {
		for _, ev := range seg.Push(pcm[off : off+config.FrameSamples]) {
			events <- ev
		}
	}
	if ev, ok := seg.Flush(); ok {
		events <- ev
	}
	close(events)

	transcribeLoop(ctx, cfg, client, writer, &view{con: ui.New()}, events, nil)
	if err := writer.Close(start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	t.Logf("保存内容:\n%s", content)

	// 日本語区間が日本語で出ていること。
	if !strings.Contains(content, "会議") {
		t.Errorf("日本語区間が認識されていない:\n%s", content)
	}
	// 英語区間が英語で出ていること (日本語へ翻訳/脱落していない)。
	if !strings.Contains(strings.ToLower(content), "release") {
		t.Errorf("英語区間が英語で認識されていない (auto の言語判定が区間ごとに効いていない):\n%s", content)
	}
}

// synthVoice は指定した声で合成音声を作る (日英の作り分け用)。
func synthVoice(t *testing.T, voice, text string) []int16 {
	t.Helper()
	dir := t.TempDir()
	aiff := filepath.Join(dir, "s.aiff")
	if err := exec.Command("say", "-v", voice, "-o", aiff, text).Run(); err != nil {
		t.Skipf("say -v %s を実行できない: %v", voice, err)
	}
	raw, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", aiff, "-ac", "1", "-ar", "16000", "-f", "s16le", "-").Output()
	if err != nil {
		t.Fatalf("ffmpeg: %v", err)
	}
	pcm := make([]int16, len(raw)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	return pcm
}

// REALTIME_PLAN 完了条件 1 の実機 (whisper-server) 検証。
// 音声デバイスは使わず、say で合成した 1 文を使う。
//
// 発話途中の暫定文が後続の音声を受けて書き直され、
// 確定文は 1 回だけ表示・保存されることを確認する。
func TestIntegrationInterimRefinesThenFinalizesOnce(t *testing.T) {
	if os.Getenv("MEETSCRIBE_E2E") != "1" {
		t.Skip("MEETSCRIBE_E2E=1 のときだけ実行する")
	}
	home, _ := os.UserHomeDir()
	model := filepath.Join(home, "Library/Application Support/superwhisper/ggml-large-v3-turbo.bin")
	if _, err := os.Stat(model); err != nil {
		model = filepath.Join(home, ".local/share/talknote/models/ggml-large-v3-turbo.bin")
	}
	if _, err := os.Stat(model); err != nil {
		t.Skipf("モデルが無い: %s", model)
	}

	gap := make([]int16, config.SampleRate) // 1 秒の無音
	pcm := append([]int16(nil), gap...)
	pcm = append(pcm, synthVoice(t, "Kyoko", "本日の会議を始めます。議題は来週のリリース計画についてです。")...)
	pcm = append(pcm, gap...)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 進行中の録音と資源を取り合わないよう、スレッド数は控えめにする。
	client, err := whisper.Start(ctx, whisper.Options{
		ServerBin: "whisper-server", ModelPath: model, Language: "ja", Threads: 4,
	})
	if err != nil {
		t.Fatalf("whisper 起動: %v", err)
	}
	defer client.Close()

	dir := t.TempDir()
	start := time.Date(2026, 10, 16, 13, 0, 0, 0, time.Local)
	writer, err := transcript.New(dir, start)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	seg := newSegmenter(true) // 既定値 (暫定 1 秒 / 無音 0.5 秒)

	var interimTexts []string
	var finalLines []string
	for off := 0; off+config.FrameSamples <= len(pcm); off += config.FrameSamples {
		for _, ev := range seg.Push(pcm[off : off+config.FrameSamples]) {
			text, err := client.Transcribe(ctx, ev.Audio, config.SampleRate)
			if err != nil {
				t.Fatalf("推論失敗: %v", err)
			}
			text = transcript.Clean(text)
			if text == "" || transcript.IsNoise(text) {
				continue
			}
			if ev.Kind == vad.Interim {
				interimTexts = append(interimTexts, text)
				continue
			}
			line, err := writer.Append(ev.Start, ev.End, text)
			if err != nil {
				t.Fatal(err)
			}
			if line != "" {
				finalLines = append(finalLines, line)
			}
		}
	}
	if ev, ok := seg.Flush(); ok {
		text, err := client.Transcribe(ctx, ev.Audio, config.SampleRate)
		if err != nil {
			t.Fatal(err)
		}
		if line, _ := writer.Append(ev.Start, ev.End, transcript.Clean(text)); line != "" {
			finalLines = append(finalLines, line)
		}
	}
	if err := writer.Close(start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	t.Logf("暫定 %d 回:", len(interimTexts))
	for i, s := range interimTexts {
		t.Logf("  %d: %s", i+1, s)
	}
	t.Logf("確定 %d 行: %v", len(finalLines), finalLines)

	// 発話途中に複数回の暫定が出ること (約 1 秒ごと)。
	if len(interimTexts) < 2 {
		t.Fatalf("暫定が %d 回しか出ていない", len(interimTexts))
	}
	// 後続の音声を受けて暫定文が書き直されること。
	if interimTexts[0] == interimTexts[len(interimTexts)-1] {
		t.Errorf("暫定文が更新されていない: %q のまま", interimTexts[0])
	}
	// 最終的に文が伸びていること。
	if len([]rune(interimTexts[len(interimTexts)-1])) <= len([]rune(interimTexts[0])) {
		t.Errorf("暫定文が伸びていない: %q → %q",
			interimTexts[0], interimTexts[len(interimTexts)-1])
	}
	// 確定は 1 回だけ。
	if len(finalLines) != 1 {
		t.Errorf("確定行 = %d, want 1: %v", len(finalLines), finalLines)
	}

	data, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "会議") || !strings.Contains(content, "リリース") {
		t.Errorf("確定内容が不足:\n%s", content)
	}
	// 暫定文がファイルに残っていないこと (本文行は 1 行だけ)。
	body := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "[") {
			body++
		}
	}
	if body != 1 {
		t.Errorf("保存された本文行 = %d, want 1\n%s", body, content)
	}
	_ = cfg
}
