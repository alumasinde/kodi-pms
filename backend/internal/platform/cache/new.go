package cache

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"kodipms/internal/platform/config"
)

// New returns Redis when REDIS_ADDR is configured and reachable. In
// development an unreachable Redis falls back to process memory so the API
// still runs; outside development a configured-but-unreachable Redis is fatal
// because deny-lists and cross-instance limits would silently stop working.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (Store, error) {
	if cfg.RedisAddr == "" {
		if !cfg.IsDevelopment() {
			log.Warn("REDIS_ADDR is empty; using in-process cache (single instance only)")
		}
		return newMemoryStore(), nil
	}

	client := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		if !cfg.IsDevelopment() {
			return nil, fmt.Errorf("redis ping: %w", err)
		}
		log.Warn("redis unavailable, falling back to in-process cache", "addr", cfg.RedisAddr, "error", err)
		return newMemoryStore(), nil
	}
	log.Info("redis connected", "addr", cfg.RedisAddr, "db", cfg.RedisDB)
	return &redisStore{client: client}, nil
}
