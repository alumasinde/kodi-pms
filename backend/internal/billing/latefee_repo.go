package billing

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
)

func (r *repository) InsertLateFee(ctx context.Context, tx *sqlx.Tx, row *LateFeeRow) (uint64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO late_fees (organization_id, invoice_id, amount, date_assessed, status)
		VALUES (?, ?, ?, ?, ?)`, row.OrganizationID, row.InvoiceID, row.Amount, row.DateAssessed, row.Status)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

func (r *repository) LateFees(ctx context.Context, orgID, invoiceID uint64) ([]LateFeeRow, error) {
	var rows []LateFeeRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT id, organization_id, invoice_id, amount, date_assessed, status, waived_by, waived_reason,
			created_at, updated_at
		FROM late_fees WHERE organization_id = ? AND invoice_id = ? ORDER BY id`, orgID, invoiceID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) LateFeeForUpdate(ctx context.Context, tx *sqlx.Tx, orgID, id uint64) (*LateFeeRow, error) {
	var row LateFeeRow
	err := tx.GetContext(ctx, &row, `
		SELECT id, organization_id, invoice_id, amount, date_assessed, status, waived_by, waived_reason,
			created_at, updated_at
		FROM late_fees WHERE organization_id = ? AND id = ? FOR UPDATE`, orgID, id)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// HasLateFee keeps the assessment sweep idempotent: one fee per invoice, ever.
func (r *repository) HasLateFee(ctx context.Context, q querier, orgID, invoiceID uint64) (bool, error) {
	var count int
	err := q.GetContext(ctx, &count, `
		SELECT COUNT(*) FROM late_fees WHERE organization_id = ? AND invoice_id = ?`, orgID, invoiceID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *repository) SetLateFeeStatus(ctx context.Context, tx *sqlx.Tx, orgID, id uint64, status string, waivedBy uint64, reason string) error {
	var err error
	if status == lateFeeWaived {
		_, err = tx.ExecContext(ctx, `
			UPDATE late_fees SET status = ?, waived_by = ?, waived_reason = ?
			WHERE organization_id = ? AND id = ?`, status, nullableID(waivedBy), nullableText(reason), orgID, id)
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE late_fees SET status = ? WHERE organization_id = ? AND id = ?`, status, orgID, id)
	return err
}

// RemoveLateFeeLine deletes only the line the fee engine added, never a line a
// utility or a person wrote.
func (r *repository) RemoveLateFeeLine(ctx context.Context, tx *sqlx.Tx, orgID, feeID uint64) error {
	_, err := tx.ExecContext(ctx, `
		DELETE FROM invoice_items
		WHERE organization_id = ? AND source_type = 'late_fee' AND source_id = ?`, orgID, feeID)
	return err
}

// MarkFeesWaivedOnVoid closes the fees an invoice carried when the invoice
// itself is cancelled. A void already wrote off the receivable, so a fee left
// 'applied' would point at money nobody owes.
func (r *repository) MarkFeesWaivedOnVoid(ctx context.Context, tx *sqlx.Tx, orgID, invoiceID, userID uint64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE late_fees SET status = 'waived', waived_by = ?, waived_reason = 'Invoice voided'
		WHERE organization_id = ? AND invoice_id = ? AND status IN ('pending','applied')`,
		nullableID(userID), orgID, invoiceID)
	return err
}

func (r *repository) MarkLateFeesPaid(ctx context.Context, tx *sqlx.Tx, orgID, invoiceID uint64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE late_fees SET status = 'paid'
		WHERE organization_id = ? AND invoice_id = ? AND status = 'applied'`, orgID, invoiceID)
	return err
}

// FeeCandidates returns invoices still unpaid after the grace period with no
// fee of their own. The caller assesses them one transaction at a time.
func (r *repository) FeeCandidates(ctx context.Context, q querier, orgID uint64, cutoff time.Time, limit int) ([]InvoiceBalance, error) {
	var rows []InvoiceBalance
	err := q.SelectContext(ctx, &rows, `
		SELECT i.id, i.lease_id, i.invoice_number, i.due_date, i.total, i.amount_paid, i.status,
			i.balance, l.rent_amount
		FROM invoices i
		INNER JOIN leases l ON l.organization_id = i.organization_id AND l.id = i.lease_id
		WHERE i.organization_id = ? AND i.status IN ('issued','overdue','partially_paid')
		  AND i.balance > 0 AND i.due_date < ?
		  AND NOT EXISTS (SELECT 1 FROM late_fees lf WHERE lf.invoice_id = i.id)
		ORDER BY i.id LIMIT ?`, orgID, cutoff, limit)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
