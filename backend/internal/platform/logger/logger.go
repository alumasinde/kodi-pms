package logger

import (
	"log/slog"
	"os"

	"kodipms/internal/platform/config"
)

func New(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	if cfg.IsDevelopment() {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.IsDevelopment() {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h).With("service", "kodipms-api", "env", cfg.Env)
}
