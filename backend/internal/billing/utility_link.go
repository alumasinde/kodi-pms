package billing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// Utilities measures a charge; this is the only place it becomes money. A line
// that carries source_type='utility_bill' has to leave the bill pointing at the
// invoice that holds it, and a bill whose line goes away — or whose invoice is
// voided or deleted — goes back to pending so it can be billed again. Nothing
// here reads the meter or the reading: the tenant is charged what the line says.

const (
	utilityBillPending = "pending"
	utilityBillPaid    = "paid"
	utilityBillVoid    = "void"
)

type utilityBillRef struct {
	ID        uint64        `db:"id"`
	UnitID    uint64        `db:"unit_id"`
	Type      string        `db:"type"`
	Amount    float64       `db:"amount"`
	Status    string        `db:"status"`
	InvoiceID sql.NullInt64 `db:"invoice_id"`
}

func (r *repository) UtilityBill(ctx context.Context, q querier, orgID, billID uint64) (*utilityBillRef, error) {
	var row utilityBillRef
	err := q.GetContext(ctx, &row, `
		SELECT b.id, b.unit_id, b.type, b.amount, b.status, b.invoice_id
		FROM utility_bills b
		INNER JOIN units u ON u.id = b.unit_id AND u.deleted_at IS NULL
		INNER JOIN properties p ON p.id = u.property_id AND p.deleted_at IS NULL
		WHERE b.id = ? AND p.organization_id = ?`, billID, orgID)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// SyncUtilityBills makes the bills behind an invoice agree with its lines. The
// release runs first, which is what lets a replaced line set move a bill to a
// different invoice inside one call.
func (r *repository) SyncUtilityBills(ctx context.Context, tx *sqlx.Tx, orgID, invoiceID uint64) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE utility_bills b
		SET b.invoice_id = NULL, b.status = ?
		WHERE b.invoice_id = ? AND b.status = 'billed'
			AND NOT EXISTS (SELECT 1 FROM invoice_items ii
				WHERE ii.invoice_id = ? AND ii.source_type = ? AND ii.source_id = b.id)`,
		utilityBillPending, invoiceID, invoiceID, itemSourceUtilityBill); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE utility_bills b
		INNER JOIN invoice_items ii ON ii.source_type = ? AND ii.source_id = b.id
		SET b.invoice_id = ii.invoice_id, b.status = 'billed'
		WHERE ii.organization_id = ? AND ii.invoice_id = ? AND b.status = ?`,
		itemSourceUtilityBill, orgID, invoiceID, utilityBillPending)
	return err
}

// UnlinkUtilityBills hands every bill on an invoice back to utilities. A paid
// bill keeps its link: the money it settled has not gone anywhere, and reopening
// it would invent a charge that was already collected.
func (r *repository) UnlinkUtilityBills(ctx context.Context, tx *sqlx.Tx, invoiceID uint64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE utility_bills
		SET invoice_id = NULL, status = ?
		WHERE invoice_id = ? AND status = 'billed'`, utilityBillPending, invoiceID)
	return err
}

// checkUtilityLines rejects a line that claims a bill it has no right to: one
// from another organization, another unit or another invoice, and one already
// collected or voided. invoiceID is 0 while the invoice is still being created.
func (s *Service) checkUtilityLines(ctx context.Context, tx *sqlx.Tx, orgID, unitID, invoiceID uint64,
	items []InvoiceItemRow, fields map[string]string) error {
	for i := range items {
		item := &items[i]
		if !item.SourceType.Valid || item.SourceType.String != itemSourceUtilityBill || !item.SourceID.Valid {
			continue
		}
		bill, err := s.repo.UtilityBill(ctx, tx, orgID, uint64(item.SourceID.Int64))
		if errors.Is(err, sql.ErrNoRows) {
			fields["items"] = fmt.Sprintf("line %d points at a utility bill that does not exist here", i+1)
			continue
		}
		if err != nil {
			return err
		}
		line := fmt.Sprintf("line %d", i+1)
		switch {
		case bill.UnitID != unitID:
			fields["items"] = line + " is a utility bill taken on another unit"
		case bill.Status == utilityBillPaid:
			fields["items"] = line + " is a utility bill that has already been paid"
		case bill.Status == utilityBillVoid:
			fields["items"] = line + " is a utility bill that has been voided"
		case bill.InvoiceID.Valid && uint64(bill.InvoiceID.Int64) != invoiceID:
			fields["items"] = fmt.Sprintf("%s is already carried on invoice %d", line, bill.InvoiceID.Int64)
		}
	}
	return nil
}

// attachUtilityBills links the bills behind an invoice's lines, and only when a
// line actually carries one, so a rent-only invoice never touches the utility
// tables.
func (s *Service) attachUtilityBills(ctx context.Context, tx *sqlx.Tx, orgID, invoiceID uint64, items []InvoiceItemRow) error {
	for i := range items {
		if items[i].SourceType.Valid && items[i].SourceType.String == itemSourceUtilityBill {
			return s.repo.SyncUtilityBills(ctx, tx, orgID, invoiceID)
		}
	}
	return nil
}
