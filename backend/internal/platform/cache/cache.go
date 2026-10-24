package cache

import (
	"context"
	"errors"
	"time"
)

var ErrMiss = errors.New("cache miss")

// Store is intentionally small: everything the app needs is a key/value with
// TTL plus a counter. Both Redis and the in-process fallback implement it.
type Store interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Del(ctx context.Context, keys ...string) error
	Incr(ctx context.Context, key string) (int64, error)
	// IncrTTL increments and sets the expiry only when the key is created, in
	// one atomic operation. Fixed-window limiters must not split these steps.
	IncrTTL(ctx context.Context, key string, ttl time.Duration) (int64, error)
	Expire(ctx context.Context, key string, ttl time.Duration) error
	Close() error
}
