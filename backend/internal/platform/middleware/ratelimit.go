package middleware

import (
	"net/http"
	"strconv"
	"time"

	"kodipms/internal/platform/cache"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

// RateLimit is a fixed-window counter. If the cache is unavailable it fails
// open: rate limiting must not take the API down.
func RateLimit(store cache.Store, limit int, window time.Duration, keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limit <= 0 {
				next.ServeHTTP(w, r)
				return
			}
			key := keyFn(r)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			n, err := store.Incr(r.Context(), key)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			if n == 1 {
				_ = store.Expire(r.Context(), key, window)
			}
			if n > int64(limit) {
				w.Header().Set("Retry-After", strconv.Itoa(int(window.Seconds())))
				httpx.Fail(w, httpx.ErrTooMany)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func RateLimitIP(store cache.Store, limit int, window time.Duration, trustProxy bool) func(http.Handler) http.Handler {
	return RateLimit(store, limit, window, func(r *http.Request) string {
		return "rl:ip:" + httpx.ClientIP(r, trustProxy)
	})
}

func RateLimitUser(store cache.Store, limit int, window time.Duration) func(http.Handler) http.Handler {
	return RateLimit(store, limit, window, func(r *http.Request) string {
		p := rbac.FromContext(r.Context())
		if p == nil {
			return ""
		}
		return "rl:api:" + strconv.FormatUint(p.UserID, 10)
	})
}
