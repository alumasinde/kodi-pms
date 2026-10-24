package mpesa

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"

	"kodipms/internal/audit"
	"kodipms/internal/platform/config"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/platform/pii"
	"kodipms/internal/rbac"
)

// Service is the entry point for everything M-Pesa: the shortcode configuration
// that says which paybill an organization owns, the two callback surfaces that
// Safaricom posts to, the queue of receipts that could not be placed, and the
// prompts that ask a tenant for money.
//
// The two halves of the struct are deliberately different in kind. repo, pii and
// cfg are this module's own business; gateway and payments are seams to the
// outside, so a test can drive a whole confirmation without a network or a
// second database and the code cannot quietly reach into either provider.
type Service struct {
	db       *sqlx.DB
	repo     *repository
	audit    *audit.Logger
	log      *slog.Logger
	cfg      *config.Config
	cipher   *pii.Cipher
	gateway  Daraja
	payments PaymentBooker
}

func NewService(db *sqlx.DB, auditLog *audit.Logger, cipher *pii.Cipher, cfg *config.Config, gateway Daraja, payments PaymentBooker, log *slog.Logger) *Service {
	if gateway == nil {
		gateway = disabledGateway{}
	}
	return &Service{
		db:       db,
		repo:     &repository{db: db},
		audit:    auditLog,
		log:      log,
		cfg:      cfg,
		cipher:   cipher,
		gateway:  gateway,
		payments: payments,
	}
}

func (s *Service) writeAudit(ctx context.Context, e audit.Entry) {
	if err := s.audit.Log(ctx, e); err != nil {
		s.log.Warn("write mpesa audit", "action", e.Action, "org_id", e.OrgID, "error", err)
	}
}

func authorize(p *rbac.Principal, permission string) error {
	if p == nil || p.OrgID == 0 {
		return httpx.ErrUnauthorized
	}
	if !p.Can(permission) {
		return httpx.ErrForbidden
	}
	return nil
}

// notFound collapses a missing row and a row the caller may not see into the same
// answer, which matters most for the shortcode: its id is in every webhook URL
// this module hands out.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.ErrNotFound
	}
	return err
}

func isDuplicateEntry(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

// isForeignKeyRestrict is the race the pre-flight link count cannot rule out:
// another writer attaches a receipt to a shortcode after this request has checked
// and before it deletes.
func isForeignKeyRestrict(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1451
}

// checkRejection turns MySQL 3819, the error a CHECK constraint raises, into a 409
// carrying the schema's own words. The shortcode environment, account type and
// receipt status all live in CHECK constraints, so reaching here means a value
// got past this module's own vocabulary.
func checkRejection(err error, fallback string) error {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 3819 {
		return nil
	}
	message := mysqlErr.Message
	if i := strings.LastIndexByte(message, ']'); i >= 0 {
		message = message[i+1:]
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = fallback
	}
	return httpx.Conflict(message)
}
