package app

import (
	"context"

	"github.com/konono/sokki/internal/config"
	"github.com/konono/sokki/internal/qwen"
	"github.com/konono/sokki/internal/whisper"
)

// startBackend はローカルの whisper.cpp バックエンドを用意する。
func startBackend(ctx context.Context, cfg config.Config, modelPath string) (transcriber, error) {
	if cfg.Backend == config.BackendQwen {
		return qwen.Start(ctx, qwen.Options{Python: cfg.QwenPython, ModelPath: modelPath})
	}
	return whisper.Start(ctx, whisper.Options{
		ServerBin: cfg.ServerBin,
		ModelPath: modelPath,
		Language:  cfg.Language,
		Threads:   cfg.Threads,
		Prompt:    cfg.InitialPrompt,
		ServerURL: cfg.ServerURL,
	})
}
