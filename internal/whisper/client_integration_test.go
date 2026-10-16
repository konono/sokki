package whisper

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 実機統合テスト。実際の whisper-server とローカルモデルを使うため、
// 明示的に有効化したときだけ走らせる:
//
//	MEETSCRIBE_E2E=1 go test ./internal/whisper/ -run Integration -v
func requireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("MEETSCRIBE_E2E") != "1" {
		t.Skip("MEETSCRIBE_E2E=1 のときだけ実行する")
	}
}

func modelPath(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, ".local/share/talknote/models/ggml-large-v3-turbo.bin")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("モデルが無い: %s", p)
	}
	return p
}

// synthJapanese は macOS の say で日本語音声を作り 16 kHz モノラル PCM にする。
func synthJapanese(t *testing.T, text string) []int16 {
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

func TestIntegrationTranscribeJapanese(t *testing.T) {
	requireE2E(t)
	model := modelPath(t)
	pcm := synthJapanese(t, "本日の会議を始めます。議題は来週のリリース計画についてです。")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	started := time.Now()
	c, err := Start(ctx, Options{
		ServerBin: "whisper-server",
		ModelPath: model,
		Language:  "ja",
		Threads:   8,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()
	t.Logf("サーバー起動: %v", time.Since(started).Round(time.Millisecond))

	inferStart := time.Now()
	text, err := c.Transcribe(ctx, pcm, 16000)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	elapsed := time.Since(inferStart)
	audioLen := time.Duration(float64(len(pcm)) / 16000 * float64(time.Second))
	t.Logf("音声 %v / 推論 %v → %q", audioLen.Round(time.Millisecond), elapsed.Round(time.Millisecond), text)

	// 日本語が返ること (キーワードで判定)。
	for _, want := range []string{"会議", "議題", "リリース"} {
		if !strings.Contains(text, want) {
			t.Errorf("認識結果に %q が含まれない: %q", want, text)
		}
	}
	// 逐次表示が成立する速度か (実時間より十分速いこと)。
	if elapsed > audioLen {
		t.Errorf("推論が音声長より遅い (逐次表示できない): %v > %v", elapsed, audioLen)
	}
}

func TestIntegrationTranscribeSilenceIsHarmless(t *testing.T) {
	requireE2E(t)
	model := modelPath(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c, err := Start(ctx, Options{
		ServerBin: "whisper-server", ModelPath: model, Language: "ja", Threads: 8,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	// 2 秒の無音。エラーにならないことを確認する
	// (内容は whisper 次第なので、落ちないことだけを見る)。
	text, err := c.Transcribe(ctx, make([]int16, 32000), 16000)
	if err != nil {
		t.Fatalf("無音でエラー: %v", err)
	}
	t.Logf("無音の認識結果: %q", text)
}

// Ctrl-C (呼び出し側 context のキャンセル) の後もサーバーが生きており、
// 終了間際に残った音声を文字起こしできること。
//
// 実機デバイスは使わず、say で合成した音声のみを使う。
func TestIntegrationServerOutlivesInterrupt(t *testing.T) {
	requireE2E(t)
	model := modelPath(t)
	pcm := synthJapanese(t, "これは終了直前の発話です。")

	// 呼び出し側の context = Ctrl-C で切れる想定のもの。
	interruptCtx, interrupt := context.WithCancel(context.Background())
	c, err := Start(interruptCtx, Options{
		ServerBin: "whisper-server", ModelPath: model, Language: "ja", Threads: 8,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	// Ctrl-C 相当。以前はここでサーバーが kill され、
	// 残った音声が connection reset で失われていた。
	interrupt()
	time.Sleep(500 * time.Millisecond)

	text, err := c.Transcribe(context.Background(), pcm, 16000)
	if err != nil {
		t.Fatalf("Ctrl-C 後に文字起こしできない (終了時に取りこぼす): %v", err)
	}
	if !strings.Contains(text, "終了") {
		t.Errorf("認識結果が不正: %q", text)
	}
	t.Logf("Ctrl-C 後の認識結果: %q", text)

	// Close() でサーバーが確実に止まること。
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := c.Transcribe(context.Background(), pcm, 16000); err == nil {
		t.Error("Close 後もサーバーが応答している (プロセスが残る)")
	}
}

// REVIEW.md [P1] (実機確認) の回帰テスト。
//
// 実際の whisper-server が親と別の process group で起動していること。
// 端末の Ctrl-C は SIGINT を foreground process group 全体へ送るため、
// 同じ group に居ると終了間際の推論が
// "connection refused" で失敗する (実機で観測された症状)。
func TestIntegrationServerRunsInOwnProcessGroup(t *testing.T) {
	requireE2E(t)
	model := modelPath(t)

	c, err := Start(context.Background(), Options{
		ServerBin: "whisper-server", ModelPath: model, Language: "ja", Threads: 8,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	if c.cmd == nil || c.cmd.Process == nil {
		t.Fatal("サーバープロセスが取得できない")
	}
	parentPgid, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	serverPgid, err := syscall.Getpgid(c.cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if serverPgid == parentPgid {
		t.Fatalf("whisper-server が親と同じ process group (pgid=%d) にいる "+
			"= 端末の Ctrl-C で直接 kill される", serverPgid)
	}
	t.Logf("親 pgid=%d / whisper-server pgid=%d (分離 OK)", parentPgid, serverPgid)

	// 分離されていれば Close まで推論できる。
	if _, err := c.Transcribe(context.Background(), make([]int16, 16000), 16000); err != nil {
		t.Fatalf("推論できない: %v", err)
	}
}
