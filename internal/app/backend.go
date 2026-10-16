package app

import (
	"context"

	"github.com/konono/sokki/internal/config"
	"github.com/konono/sokki/internal/whisper"
)

// startBackend はローカルの whisper.cpp バックエンドを用意する。
func startBackend(ctx context.Context, cfg config.Config, modelPath string) (transcriber, error) {
	return whisper.Start(ctx, whisper.Options{
		ServerBin: cfg.ServerBin,
		ModelPath: modelPath,
		Language:  cfg.Language,
		Threads:   cfg.Threads,
		Prompt:    cfg.InitialPrompt,
		ServerURL: cfg.ServerURL,
	})
}
