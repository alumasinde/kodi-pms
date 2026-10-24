package billing

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/audit"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

// Service is the one entry point the billing handlers call. Invoices, payments,
// late fees and credit notes share it because a write on any of them settles
// money on the others, and the helpers below are where that settling happens.
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

// settleInvoice re-derives an invoice's status from its money. amount_paid is
// written by triggers, so this only moves the label and settles the records that
// depend on the invoice being cleared: late fees and a deposit held on it.
func (s *Service) settleInvoice(ctx context.Context, tx *sqlx.Tx, orgID, invoiceID uint64, today time.Time) error {
	balance, err := s.repo.InvoiceBalanceByID(ctx, tx, orgID, invoiceID)
	if err != nil {
		return err
	}
	if next := invoiceStatusFor(balance.Status, balance.Total, balance.AmountPaid, balance.DueDate, today); next != balance.Status {
		if err := s.repo.SetInvoiceStatus(ctx, tx, orgID, invoiceID, next); err != nil {
			return err
		}
	}
	if balance.AmountPaid+ledgerTolerance < balance.Total {
		return nil
	}
	if err := s.repo.MarkLateFeesPaid(ctx, tx, orgID, invoiceID); err != nil {
		return err
	}
	return s.holdDeposit(ctx, tx, orgID, invoiceID, balance.LeaseID, today)
}

// holdDeposit is the money-derived half of the deposit obligation the lease
// module opens: the deposit moves from pending to held when the invoice that
// bills it is paid in full.
func (s *Service) holdDeposit(ctx context.Context, tx *sqlx.Tx, orgID, invoiceID, leaseID uint64, paidOn time.Time) error {
	has, err := s.repo.InvoiceHasDepositItem(ctx, tx, orgID, invoiceID)
	if err != nil || !has {
		return err
	}
	depositID, ok, err := s.repo.PendingDeposit(ctx, tx, orgID, leaseID)
	if err != nil || !ok {
		return err
	}
	_, err = s.repo.HoldDeposit(ctx, tx, orgID, depositID, paidOn)
	return err
}

func (s *Service) postIssue(ctx context.Context, tx *sqlx.Tx, orgID, invoiceID, leaseID uint64, items []InvoiceItemRow, taxTotal, total float64, issueDate time.Time, currency string, userID uint64) error {
	lines := issueLines(items, taxTotal, total, "Issue invoice")
	_, err := s.repo.PostJournal(ctx, tx, orgID, leaseID, sourceInvoice, invoiceID,
		truncateDate(issueDate), currency, userID, lines)
	if errors.Is(err, errUnbalancedJournal) {
		s.log.Error("invoice posting does not balance", "invoice_id", invoiceID, "total", total)
		return httpx.Conflict("This invoice does not balance and cannot be issued")
	}
	return err
}

// billingLease resolves the lease a billing write hangs off. An out-of-scope
// lease is a 404 so ids cannot be probed, and an archived unit is refused with
// a message rather than a foreign-key error.
func (s *Service) billingLease(ctx context.Context, tx *sqlx.Tx, p *rbac.Principal, leaseID uint64, forUpdate bool) (*BillingLeaseRow, error) {
	if leaseID == 0 {
		return nil, httpx.Validation(map[string]string{"lease_id": "required"})
	}
	lease, err := s.repo.BillingLease(ctx, tx, p.OrgID, leaseID, p.Scope, forUpdate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return lease, nil
}

func (s *Service) detail(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*InvoiceDetailDTO, error) {
	row, err := s.repo.GetInvoice(ctx, orgID, id, scope, tenantOnly(scope))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	items, err := s.repo.Items(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	allocations, err := s.repo.AllocationsForInvoice(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	fees, err := s.repo.LateFees(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	out := invoiceDetailDTO(row, itemDTOs(items), allocationDTOs(allocations), lateFeeDTOs(fees))
	return &out, nil
}

// rejectTrigger keeps database-guarded money rules in the service's voice: the
// raw SQLSTATE text is logged for the operator, and the caller gets a conflict.
func (s *Service) rejectTrigger(err error, action string) error {
	s.log.Warn("billing rejected by database rule", "action", action, "error", err)
	return httpx.Conflict("This change breaks a billing rule the database enforces")
}

func (s *Service) writeAudit(ctx context.Context, e audit.Entry) {
	if err := s.audit.Log(ctx, e); err != nil {
		s.log.Warn("write billing audit", "action", e.Action, "org_id", e.OrgID, "error", err)
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
