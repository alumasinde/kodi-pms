package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

type redisStore struct {
	client *redis.Client
}

func (s *redisStore) Get(ctx context.Context, key string) (string, error) {
	v, err := s.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrMiss
	}
	return v, err
}

func (s *redisStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return s.client.Set(ctx, key, value, ttl).Err()
}

func (s *redisStore) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return s.client.Del(ctx, keys...).Err()
}

func (s *redisStore) Incr(ctx context.Context, key string) (int64, error) {
	return s.client.Incr(ctx, key).Result()
}

func (s *redisStore) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return s.client.Expire(ctx, key, ttl).Err()
}

func (s *redisStore) Close() error { return s.client.Close() }
