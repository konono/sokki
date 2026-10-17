package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/konono/sokki/internal/vad"
)

const (
	swRel        = "Library/Application Support/superwhisper/ggml-large-v3-turbo.bin"
	talknoteRel  = ".local/share/talknote/models/ggml-large-v3-turbo.bin"
	swCoreMLRel  = "Library/Application Support/superwhisper/models/ggml-large-v3-turbo.bin"
	dummyContent = "ggml"
)

// fakeHome は $HOME を差し替え、指定した相対パスにダミーモデルを置く。
func fakeHome(t *testing.T, rels ...string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, rel := range rels {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(dummyContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// REVIEW.md [P1] の回帰テスト。
// Superwhisper の実際の保存先 (models/ ではなくアプリサポート直下) を
// 探索すること。talknote 側が無くても起動できる。
func TestResolveModelFindsSuperwhisperActualPath(t *testing.T) {
	home := fakeHome(t, swRel)
	got, err := Default().ResolveModel()
	if err != nil {
		t.Fatalf("Superwhisper のモデルを見つけられない: %v", err)
	}
	if want := filepath.Join(home, swRel); got != want {
		t.Fatalf("選ばれたモデル = %s, want %s", got, want)
	}
}

// talknote 側だけでも動くこと (フォールバック)。
func TestResolveModelFallsBackToTalknote(t *testing.T) {
	home := fakeHome(t, talknoteRel)
	got, err := Default().ResolveModel()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, talknoteRel); got != want {
		t.Fatalf("選ばれたモデル = %s, want %s", got, want)
	}
}

// 両方あれば Superwhisper を優先する (PLAN.md の方針)。
func TestResolveModelPrefersSuperwhisper(t *testing.T) {
	home := fakeHome(t, swRel, talknoteRel)
	got, err := Default().ResolveModel()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, swRel); got != want {
		t.Fatalf("選ばれたモデル = %s, want %s", got, want)
	}
}

// models/ 配下 (CoreML 形式が置かれる場所) は候補にしない。
func TestResolveModelIgnoresSuperwhisperModelsSubdir(t *testing.T) {
	fakeHome(t, swCoreMLRel)
	if got, err := Default().ResolveModel(); err == nil {
		t.Fatalf("models/ 配下を誤って採用した: %s", got)
	}
}

// 見つからない場合は探索先と対処を示す。
func TestResolveModelMissingExplainsWhere(t *testing.T) {
	fakeHome(t)
	_, err := Default().ResolveModel()
	if err == nil {
		t.Fatal("モデルが無いのにエラーにならなかった")
	}
	for _, want := range []string{"superwhisper", "talknote", "対処:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("エラー文面に %q が無い:\n%s", want, err)
		}
	}
}

// --model の明示指定が最優先される。
func TestResolveModelExplicitWins(t *testing.T) {
	home := fakeHome(t, swRel)
	explicit := filepath.Join(home, "custom.bin")
	if err := os.WriteFile(explicit, []byte(dummyContent), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Model = explicit
	got, err := cfg.ResolveModel()
	if err != nil {
		t.Fatal(err)
	}
	if got != explicit {
		t.Fatalf("選ばれたモデル = %s, want %s", got, explicit)
	}
}

// 実機で観測した静かな入力 (背景 RMS 約 0.004、発話の強弱が交互) を
// 既定の VAD 設定で発話として切り出せることを確認する。
func TestDefaultVADDetectsQuietSpeech(t *testing.T) {
	cfg := Default()
	s := vad.New(vad.Options{
		SampleRate: 16000, FrameSamples: 320,
		SilenceHangover: cfg.SilenceHangover,
		MinSpeech:       cfg.MinSpeech, MaxSegment: cfg.MaxSegment,
		Preroll: cfg.Preroll, FloorRatio: cfg.VADFloorRatio,
		AbsThreshold: cfg.VADAbsThreshold,
	})
	tone := func(amp float64) []int16 {
		frame := make([]int16, 320)
		for i := range frame {
			frame[i] = int16(amp * 32767 * math.Sin(float64(i)*0.3))
		}
		return frame
	}
	var finals int
	feed := func(amp float64, n int) {
		frame := tone(amp)
		for range n {
			for _, ev := range s.Push(frame) {
				if ev.Kind == vad.Final {
					finals++
				}
			}
		}
	}
	feed(0.0057, 150) // 3 秒の背景音 (RMS 約 0.004)
	for range 12 {    // 1.2 秒の静かな発話
		feed(0.030, 3) // 強い音節 60ms
		feed(0.019, 2) // 弱い音節 40ms
	}
	feed(0.0005, int((cfg.SilenceHangover+time.Second)/(20*time.Millisecond)))
	if finals != 1 {
		t.Fatalf("静かな発話の確定区間 = %d, want 1 (しきい値 %.3f)", finals, cfg.VADAbsThreshold)
	}
}
