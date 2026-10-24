package billing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/audit"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

// AssessLateFee charges a late fee now instead of waiting for the sweep. The
// fee becomes an invoice line, so the balance the tenant owes stays equal to
// what the ledger says is receivable.
func (s *Service) AssessLateFee(ctx context.Context, p *rbac.Principal, id uint64, in LateFeeInput, meta ClientMeta) (*InvoiceDetailDTO, error) {
	if err := authorize(p, "invoices.update"); err != nil {
		return nil, err
	}
	return s.mutateInvoice(ctx, p, id, meta, "invoices.late_fee_assessed", func(ctx context.Context, tx *sqlx.Tx, p *rbac.Principal, row *InvoiceRow, settings BillingSettings) (map[string]any, error) {
		if row.Status == statusDraft || row.Status == statusVoid {
			return nil, httpx.Conflict("A late fee needs an issued, unpaid invoice")
		}
		if row.Balance <= 0 {
			return nil, httpx.Conflict("This invoice has no balance left to charge")
		}
		if has, err := s.repo.HasLateFee(ctx, tx, p.OrgID, id); err != nil {
			return nil, err
		} else if has {
			return nil, httpx.Conflict("A late fee has already been raised on this invoice")
		}
		lease, err := s.repo.BillingLease(ctx, tx, p.OrgID, row.LeaseID, p.Scope, true)
		if err != nil {
			return nil, err
		}
		amount := lateFeeAmount(settings.LateFeeType, settings.LateFeeValue, lease.RentAmount)
		if in.Amount != nil {
			amount = roundToCents(*in.Amount)
			if amount <= 0 || amount > maxAmount {
				return nil, httpx.Validation(map[string]string{
					"amount": fmt.Sprintf("must be greater than zero and at most %.2f", maxAmount)})
			}
		}
		if amount <= 0 {
			return nil, httpx.Conflict("The late-fee settings produce nothing to charge")
		}
		today := truncateDate(time.Now().UTC())
		feeID, err := s.applyLateFee(ctx, tx, p.OrgID, row.LeaseID, id, row.InvoiceNumber,
			amount, today, settings.CurrencyCode, p.UserID)
		if errors.Is(err, errNothingToBill) {
			return nil, httpx.Conflict("A late fee has already been raised on this invoice today")
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"late_fee_id": feeID, "amount": amount}, nil
	})
}

// WaiveLateFee removes a fee again. A fee that has been added to the invoice is
// taken back off it, line and ledger, so the invoice still balances.
func (s *Service) WaiveLateFee(ctx context.Context, p *rbac.Principal, feeID uint64, in WaiveInput, meta ClientMeta) (*InvoiceDetailDTO, error) {
	if err := authorize(p, "invoices.update"); err != nil {
		return nil, err
	}
	if feeID == 0 {
		return nil, httpx.ErrNotFound
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, httpx.Validation(map[string]string{"reason": "required"})
	}
	if utf8.RuneCountInString(reason) > maxReasonLen {
		return nil, httpx.Validation(map[string]string{"reason": fmt.Sprintf("must be at most %d characters", maxReasonLen)})
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	fee, err := s.repo.LateFeeForUpdate(ctx, tx, p.OrgID, feeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	row, err := s.repo.InvoiceForUpdate(ctx, tx, p.OrgID, fee.InvoiceID, p.Scope, false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	switch fee.Status {
	case lateFeeWaived:
		return nil, httpx.Conflict("This late fee is already waived")
	case lateFeePaid:
		return nil, httpx.Conflict("This late fee has been paid")
	}

	settings, err := s.repo.Settings(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	if fee.Status == lateFeeApplied {
		if row.Balance < fee.Amount {
			return nil, httpx.Conflict("Part of this fee is already paid; issue a credit note instead")
		}
		if err := s.repo.RemoveLateFeeLine(ctx, tx, p.OrgID, fee.ID); err != nil {
			return nil, err
		}
		if err := s.repo.ReduceInvoiceTotal(ctx, tx, p.OrgID, row.ID, fee.Amount, 0, fee.Amount); err != nil {
			return nil, err
		}
		lines := mirror(lateFeeLines(fee.Amount, "Waived late fee on "+row.InvoiceNumber))
		if _, err := s.repo.PostJournal(ctx, tx, p.OrgID, row.LeaseID, sourceLateFee, fee.ID,
			truncateDate(time.Now().UTC()), settings.CurrencyCode, p.UserID, lines); err != nil {
			return nil, err
		}
	}
	if err := s.repo.SetLateFeeStatus(ctx, tx, p.OrgID, fee.ID, lateFeeWaived, p.UserID, reason); err != nil {
		return nil, err
	}
	if err := s.settleInvoice(ctx, tx, p.OrgID, row.ID, truncateDate(time.Now().UTC())); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "late_fees.waived", EntityType: "late_fees", EntityID: fee.ID,
		OldValues: map[string]any{"status": fee.Status},
		NewValues: map[string]any{"status": lateFeeWaived, "reason": reason, "amount": fee.Amount,
			"invoice_id": row.ID},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.detail(ctx, p.OrgID, row.ID, p.Scope)
}

func lateFeeAmount(kind string, value, rent float64) float64 {
	if strings.EqualFold(strings.TrimSpace(kind), "fixed") {
		return roundToCents(value)
	}
	return roundToCents(rent * value / 100)
}

func lateFeeDTOs(rows []LateFeeRow) []LateFeeDTO {
	out := make([]LateFeeDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, LateFeeDTO{
			ID: row.ID, Amount: row.Amount, DateAssessed: row.DateAssessed, Status: row.Status,
			WaivedBy: nullIDValue(row.WaivedBy), WaivedReason: nullText(row.WaivedReason),
		})
	}
	return out
}

// applyLateFee is one fee: the row, the line it adds to the invoice, the extra
// the invoice now owes and the journal that recognises the income. Both the
// sweep and a person pressing "assess" run through here, so a manual fee and an
// automatic one cannot drift apart.
func (s *Service) applyLateFee(ctx context.Context, tx *sqlx.Tx, orgID, leaseID, invoiceID uint64,
	invoiceNumber string, amount float64, on time.Time, currency string, userID uint64) (uint64, error) {
	feeID, err := s.repo.InsertLateFee(ctx, tx, &LateFeeRow{
		OrganizationID: orgID, InvoiceID: invoiceID, Amount: amount,
		DateAssessed: on, Status: lateFeeApplied,
	})
	if err != nil {
		if isDuplicateEntry(err) {
			return 0, errNothingToBill
		}
		return 0, err
	}
	if _, err := s.repo.InsertItem(ctx, tx, &InvoiceItemRow{
		OrganizationID: orgID, InvoiceID: invoiceID, ItemType: itemLateFee,
		Description: truncateString("Late fee on "+invoiceNumber, maxDescription),
		Quantity:    1, UnitPrice: amount, TaxRate: 0, Amount: amount,
		SourceType: sql.NullString{String: itemSourceLateFee, Valid: true},
		SourceID:   sql.NullInt64{Int64: int64(feeID), Valid: true},
	}); err != nil {
		return 0, err
	}
	if err := s.repo.AddInvoiceCharge(ctx, tx, orgID, invoiceID, amount); err != nil {
		return 0, err
	}
	if _, err := s.repo.PostJournal(ctx, tx, orgID, leaseID, sourceLateFee, feeID, on,
		currency, userID, lateFeeLines(amount, "Late fee on "+invoiceNumber)); err != nil {
		return 0, err
	}
	return feeID, nil
}
