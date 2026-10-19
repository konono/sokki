package config

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	BackendWhisper = "whisper"
	BackendQwen    = "qwen"
	FinalHybrid    = "hybrid"
	FinalWhole     = "whole"
)

// ValidateRecognition はバックエンドと確定処理を、音声入力を開く前に検証する。
func (c Config) ValidateRecognition() error {
	if c.Backend != BackendWhisper && c.Backend != BackendQwen {
		return fmt.Errorf("認識 backend が不正です: %q (whisper / qwen)", c.Backend)
	}
	if c.FinalStrategy != FinalHybrid && c.FinalStrategy != FinalWhole {
		return fmt.Errorf("final strategy が不正です: %q (hybrid / whole)", c.FinalStrategy)
	}
	if c.Backend != BackendQwen {
		return nil
	}
	if c.Language != "ja" {
		return fmt.Errorf("Qwen backend は language=ja のみ対応します: %q", c.Language)
	}
	if c.InitialPrompt != "" {
		return fmt.Errorf("Qwen backend では --prompt を使用できません")
	}
	if c.ServerURL != "" {
		return fmt.Errorf("Qwen backend では --server-url を使用できません")
	}
	if c.Model != "" {
		return fmt.Errorf("Qwen backend では --model ではなく --qwen-model を指定してください")
	}
	if c.QwenPython == "" || c.QwenModel == "" {
		return fmt.Errorf("Qwen backend には --qwen-python と --qwen-model の明示指定が必要です")
	}
	if !filepath.IsAbs(c.QwenPython) || !filepath.IsAbs(c.QwenModel) {
		return fmt.Errorf("Qwen の Python とモデルは絶対パスで指定してください")
	}
	python, err := os.Stat(c.QwenPython)
	if err != nil || python.IsDir() || python.Mode()&0111 == 0 {
		return fmt.Errorf("Qwen Python を実行できません: %s", c.QwenPython)
	}
	model, err := os.Stat(c.QwenModel)
	if err != nil || !model.IsDir() {
		return fmt.Errorf("Qwen モデルディレクトリが見つかりません: %s", c.QwenModel)
	}
	if _, err := os.Stat(filepath.Join(c.QwenModel, "config.json")); err != nil {
		return fmt.Errorf("Qwen モデルの config.json が見つかりません: %s", c.QwenModel)
	}
	weights, err := os.Stat(filepath.Join(c.QwenModel, "model.safetensors"))
	if err != nil || weights.IsDir() || weights.Size() == 0 {
		return fmt.Errorf("Qwen BF16 の model.safetensors が見つかりません: %s", c.QwenModel)
	}
	return nil
}

func (c Config) ResolveRecognitionModel() (string, error) {
	if err := c.ValidateRecognition(); err != nil {
		return "", err
	}
	if c.Backend == BackendQwen {
		return c.QwenModel, nil
	}
	return c.ResolveModel()
}
