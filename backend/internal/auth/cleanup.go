package auth

import (
	"context"
	"time"
)

const (
	tokenCleanupInterval = time.Hour
	tokenCleanupBatch    = 5000
	tokenCleanupBatches  = 10
)

// StartTokenCleanup purges expired and long-dead refresh/user tokens until ctx
// is cancelled. It is safe to run on every instance: DELETE ... LIMIT makes
// concurrent runs harmless.
func (s *Service) StartTokenCleanup(ctx context.Context) {
	go func() {
		s.purgeTokens(ctx)
		ticker := time.NewTicker(tokenCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.purgeTokens(ctx)
			}
		}
	}()
}

func (s *Service) purgeTokens(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	refresh, err := s.deleteTokenBatches(ctx, `
		DELETE FROM refresh_tokens
		WHERE expires_at < UTC_TIMESTAMP()
		   OR (revoked_at IS NOT NULL AND revoked_at < UTC_TIMESTAMP() - INTERVAL 30 DAY)
		ORDER BY id
		LIMIT ?`, tokenCleanupBatch)
	if err != nil {
		s.log.Error("purge refresh tokens", "error", err)
	}

	userTokens, err := s.deleteTokenBatches(ctx, `
		DELETE FROM user_tokens
		WHERE expires_at < UTC_TIMESTAMP()
		   OR (used_at IS NOT NULL AND used_at < UTC_TIMESTAMP() - INTERVAL 30 DAY)
		ORDER BY id
		LIMIT ?`, tokenCleanupBatch)
	if err != nil {
		s.log.Error("purge user tokens", "error", err)
	}

	if refresh > 0 || userTokens > 0 {
		s.log.Info("purged expired tokens", "refresh_tokens", refresh, "user_tokens", userTokens)
	}
}

func (s *Service) deleteTokenBatches(ctx context.Context, query string, batch int) (int64, error) {
	var total int64
	for i := 0; i < tokenCleanupBatches; i++ {
		res, err := s.db.ExecContext(ctx, query, batch)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(batch) {
			break
		}
	}
	return total, nil
}
