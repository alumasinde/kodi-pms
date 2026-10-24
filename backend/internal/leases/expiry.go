package leases

import (
	"context"
	"time"
)

const (
	expiryInterval = time.Hour
	expiryBatch    = 500
	expiryBatches  = 20
)

// StartExpirySweep moves active leases past their end date to expired until ctx
// is cancelled. The unit stays occupied: a tenant who holds over is still in the
// door, and it is terminate or renew that frees it. Running on every instance is
// harmless because the UPDATE only touches rows still marked active.
func (s *Service) StartExpirySweep(ctx context.Context) {
	go func() {
		s.expireOverdue(ctx)
		ticker := time.NewTicker(expiryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.expireOverdue(ctx)
			}
		}
	}()
}

func (s *Service) expireOverdue(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var total int64
	for i := 0; i < expiryBatches; i++ {
		moved, err := s.repo.ExpireOverdue(ctx, s.db, expiryBatch)
		if err != nil {
			s.log.Error("expire overdue leases", "error", err)
			return
		}
		total += moved
		if moved < expiryBatch {
			break
		}
	}
	if total > 0 {
		s.log.Info("expired overdue leases", "count", total)
	}
}

// pendingDeposit reports whether activating a lease should open the deposit
// record. A lease that already carries one keeps the record it has.
func pendingDeposit(amount float64, hasDeposit bool) bool {
	return amount > 0 && !hasDeposit
}
