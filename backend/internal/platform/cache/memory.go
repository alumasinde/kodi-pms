package cache

import (
	"context"
	"strconv"
	"sync"
	"time"
)

type memoryStore struct {
	mu        sync.RWMutex
	m         map[string]memoryItem
	stop      chan struct{}
	closeOnce sync.Once
}

type memoryItem struct {
	value  string
	expiry time.Time
}

func newMemoryStore() *memoryStore {
	s := &memoryStore{m: make(map[string]memoryItem), stop: make(chan struct{})}
	go s.evictLoop()
	return s
}

// Unread keys would otherwise live forever, so the janitor sweeps expired
// entries instead of relying only on the lazy delete in Get.
func (s *memoryStore) evictLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.evictExpired()
		case <-s.stop:
			return
		}
	}
}

func (s *memoryStore) evictExpired() {
	now := time.Now()
	s.mu.Lock()
	for k, it := range s.m {
		if !it.expiry.IsZero() && now.After(it.expiry) {
			delete(s.m, k)
		}
	}
	s.mu.Unlock()
}

func (s *memoryStore) Get(_ context.Context, key string) (string, error) {
	s.mu.RLock()
	it, ok := s.m[key]
	s.mu.RUnlock()
	if !ok {
		return "", ErrMiss
	}
	if !it.expiry.IsZero() && time.Now().After(it.expiry) {
		s.mu.Lock()
		delete(s.m, key)
		s.mu.Unlock()
		return "", ErrMiss
	}
	return it.value, nil
}

func (s *memoryStore) Set(_ context.Context, key, value string, ttl time.Duration) error {
	var expiry time.Time
	if ttl > 0 {
		expiry = time.Now().Add(ttl)
	}
	s.mu.Lock()
	s.m[key] = memoryItem{value: value, expiry: expiry}
	s.mu.Unlock()
	return nil
}

func (s *memoryStore) Del(_ context.Context, keys ...string) error {
	s.mu.Lock()
	for _, k := range keys {
		delete(s.m, k)
	}
	s.mu.Unlock()
	return nil
}

func (s *memoryStore) Incr(_ context.Context, key string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	it, ok := s.m[key]
	if !ok || (!it.expiry.IsZero() && time.Now().After(it.expiry)) {
		s.m[key] = memoryItem{value: "1"}
		return 1, nil
	}
	n, err := strconv.ParseInt(it.value, 10, 64)
	if err != nil {
		n = 0
	}
	n++
	it.value = strconv.FormatInt(n, 10)
	s.m[key] = it
	return n, nil
}

func (s *memoryStore) IncrTTL(_ context.Context, key string, ttl time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	it, ok := s.m[key]
	if !ok || (!it.expiry.IsZero() && now.After(it.expiry)) {
		it = memoryItem{value: "1"}
		if ttl > 0 {
			it.expiry = now.Add(ttl)
		}
		s.m[key] = it
		return 1, nil
	}
	n, err := strconv.ParseInt(it.value, 10, 64)
	if err != nil {
		n = 0
	}
	n++
	it.value = strconv.FormatInt(n, 10)
	if it.expiry.IsZero() && ttl > 0 {
		it.expiry = now.Add(ttl)
	}
	s.m[key] = it
	return n, nil
}

func (s *memoryStore) Expire(_ context.Context, key string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.m[key]
	if !ok {
		return nil
	}
	if ttl > 0 {
		it.expiry = time.Now().Add(ttl)
	} else {
		it.expiry = time.Time{}
	}
	s.m[key] = it
	return nil
}

func (s *memoryStore) Close() error {
	s.closeOnce.Do(func() { close(s.stop) })
	return nil
}
