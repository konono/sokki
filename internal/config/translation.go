package config

import (
	"fmt"
	"strings"
)

// ValidateTranslation rejects unsupported source paths before capture starts.
func (c Config) ValidateTranslation() error {
	if c.TranslateMode == "" {
		return nil
	}
	if c.TranslateMode != "en-ja" {
		return fmt.Errorf("翻訳モード %q は未対応です (en-ja のみ)", c.TranslateMode)
	}
	if c.Backend != BackendWhisper || c.Language != "en" {
		return fmt.Errorf("en-ja 翻訳は --backend whisper --lang en の確定原文が必要です")
	}
	if c.SummaryProvider == "ollama" && strings.TrimSpace(c.SummaryModel) == "" {
		return fmt.Errorf("Ollama には --llm-model が必要です")
	}
	if c.SummaryProvider != "ollama" && c.SummaryProvider != "claude" {
		return fmt.Errorf("翻訳には claude / ollama の共通 LLM provider が必要です")
	}
	if len(c.SummaryCommand) != 0 {
		return fmt.Errorf("翻訳有効時は summary.command を併用できません")
	}
	return nil
}
