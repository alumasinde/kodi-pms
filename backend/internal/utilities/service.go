package utilities

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/audit"
	"kodipms/internal/platform/httpx"
)

// Service is the entry point the utilities handlers call. Meters, readings and
// bills share it because they are one chain: a meter is the thing that is read,
// a reading is the quantity, and a bill is the money that quantity becomes. A
// write on any of them decides what the others may still be corrected to, so the
// rules live together rather than in three independent services.
type Service struct {
	db    *sqlx.DB
	repo  *repository
	audit *audit.Logger
	log   *slog.Logger
}

func NewService(db *sqlx.DB, auditLog *audit.Logger, log *slog.Logger) *Service {
	return &Service{
		db:    db,
		repo:  &repository{db: db},
		audit: auditLog,
		log:   log,
	}
}

// today answers "what date is it for this organization", because whether a bill
// is overdue is a business date in the organization's own timezone, not UTC. A
// timezone that cannot be read is not worth failing a request over, so the
// lookup degrades to the operating timezone.
func (s *Service) today(ctx context.Context, orgID uint64) time.Time {
	timezone, err := s.repo.OrganizationTimezone(ctx, orgID)
	if err != nil {
		s.log.Warn("read organization timezone", "org_id", orgID, "error", err)
	}
	return todayIn(orgLocation(timezone))
}

// readingLocked reports that a correction would falsify a bill, whichever side
// of the reading the bill sits on.
func (s *Service) readingLocked(billID uint64, status string, found bool) error {
	if !found {
		return nil
	}
	if status == billVoid {
		return httpx.Conflict("A bill was already voided from this reading; delete that bill's reading link in billing first")
	}
	return httpx.Conflict(readingLocked)
}

func (s *Service) reject(err error, action, fallback string) error {
	rejection := checkRejection(err, fallback)
	if rejection == nil {
		return nil
	}
	s.log.Warn("utilities rejected by database rule", "action", action, "error", err)
	return rejection
}

func (s *Service) writeAudit(ctx context.Context, e audit.Entry) {
	if err := s.audit.Log(ctx, e); err != nil {
		s.log.Warn("write utilities audit", "action", e.Action, "org_id", e.OrgID, "error", err)
	}
}

// notFound collapses the no-rows error every domain file turns a missing or
// out-of-scope row into, so a meter the caller cannot see and a meter that does
// not exist are indistinguishable from outside.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.ErrNotFound
	}
	return err
}
