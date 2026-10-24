package billing

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/rbac"
)

const creditNoteColumns = `
	c.id, c.organization_id, c.lease_id, c.invoice_id, c.credit_note_number, c.amount, c.reason,
	c.status, c.issue_date, c.applied_at, c.created_by, c.created_at, c.updated_at`

// creditNoteViewQuery reuses the tenant subquery with the credit note's own
// lease column, so it has to be built at run time rather than as a constant.
var creditNoteViewQuery = `
	SELECT ` + creditNoteColumns + `,
		l.lease_number, u.number AS unit_number, u.property_id, p.name AS property_name,
		i.invoice_number,` + strings.ReplaceAll(tenantNameSubquery, "lt.lease_id = l.id", "lt.lease_id = c.lease_id") + ` AS primary_tenant
	FROM credit_notes c
	INNER JOIN leases l ON l.organization_id = c.organization_id AND l.id = c.lease_id
	INNER JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
	INNER JOIN properties p ON p.organization_id = u.organization_id AND p.id = u.property_id
	LEFT JOIN invoices i ON i.organization_id = c.organization_id AND i.id = c.invoice_id`

const creditNoteFrom = `
	FROM credit_notes c
	INNER JOIN leases l ON l.organization_id = c.organization_id AND l.id = c.lease_id
	INNER JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
	INNER JOIN properties p ON p.organization_id = u.organization_id AND p.id = u.property_id
	LEFT JOIN invoices i ON i.organization_id = c.organization_id AND i.id = c.invoice_id`

func (r *repository) ListCreditNotes(ctx context.Context, orgID uint64, scope rbac.Scope, q CreditNoteQuery) ([]CreditNoteViewRow, int, error) {
	where := []string{"c.organization_id = ?"}
	args := []any{orgID}
	clause, scopeArgs := scopeFilter(scope, "u.property_id", "c.lease_id")
	where = append(where, clause)
	args = append(args, scopeArgs...)
	if q.Search != "" {
		like := likePattern(q.Search)
		where = append(where, `(c.credit_note_number LIKE ? OR l.lease_number LIKE ? OR u.number LIKE ?)`)
		args = append(args, like, like, like)
	}
	if q.Status != "" {
		where = append(where, "c.status = ?")
		args = append(args, q.Status)
	}
	if q.LeaseID != 0 {
		where = append(where, "c.lease_id = ?")
		args = append(args, q.LeaseID)
	}
	if q.InvoiceID != 0 {
		where = append(where, "c.invoice_id = ?")
		args = append(args, q.InvoiceID)
	}
	if q.PropertyID != 0 {
		where = append(where, "u.property_id = ?")
		args = append(args, q.PropertyID)
	}
	joined := strings.Join(where, " AND ")

	var total int
	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*)`+creditNoteFrom+` WHERE `+joined, args...); err != nil {
		return nil, 0, err
	}
	order := q.Sort
	if order == "" {
		order = defaultCreditNoteSort
	}
	listArgs := append(append([]any{}, args...), q.Page.Limit, q.Page.Offset)
	var rows []CreditNoteViewRow
	err := r.db.SelectContext(ctx, &rows, creditNoteViewQuery+` WHERE `+joined+` ORDER BY `+order+`, c.id DESC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *repository) GetCreditNote(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*CreditNoteViewRow, error) {
	clause, scopeArgs := scopeFilter(scope, "u.property_id", "c.lease_id")
	args := append([]any{orgID, id}, scopeArgs...)
	var row CreditNoteViewRow
	err := r.db.GetContext(ctx, &row, creditNoteViewQuery+` WHERE c.organization_id = ? AND c.id = ? AND `+clause, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) CreditNoteForUpdate(ctx context.Context, tx *sqlx.Tx, orgID, id uint64, scope rbac.Scope) (*CreditNoteRow, error) {
	clause, scopeArgs := scopeFilter(scope, "u.property_id", "c.lease_id")
	args := append([]any{orgID, id}, scopeArgs...)
	var row CreditNoteRow
	err := tx.GetContext(ctx, &row, `SELECT `+creditNoteColumns+creditNoteFrom+` WHERE c.organization_id = ? AND c.id = ? AND `+clause+` FOR UPDATE`, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) InsertCreditNote(ctx context.Context, tx *sqlx.Tx, row *CreditNoteRow) (uint64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO credit_notes
			(organization_id, lease_id, invoice_id, credit_note_number, amount, reason, status, issue_date, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.OrganizationID, row.LeaseID, row.InvoiceID, row.CreditNoteNumber, row.Amount,
		row.Reason, row.Status, row.IssueDate, row.CreatedBy)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

func (r *repository) UpdateCreditNote(ctx context.Context, tx *sqlx.Tx, row *CreditNoteRow) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE credit_notes
		SET invoice_id = ?, credit_note_number = ?, amount = ?, reason = ?, status = ?, issue_date = ?, applied_at = ?
		WHERE id = ? AND organization_id = ?`,
		row.InvoiceID, row.CreditNoteNumber, row.Amount, row.Reason, row.Status,
		row.IssueDate, row.AppliedAt, row.ID, row.OrganizationID)
	return err
}

func (r *repository) DeleteCreditNote(ctx context.Context, tx *sqlx.Tx, orgID, id uint64) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		DELETE FROM credit_notes WHERE organization_id = ? AND id = ? AND status = 'draft'`, orgID, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *repository) CreditNoteNumberTaken(ctx context.Context, q querier, orgID uint64, number string, excludeID uint64) (bool, error) {
	query := `SELECT COUNT(*) FROM credit_notes WHERE organization_id = ? AND credit_note_number = ?`
	args := []any{orgID, number}
	if excludeID != 0 {
		query += ` AND id <> ?`
		args = append(args, excludeID)
	}
	var count int
	if err := q.GetContext(ctx, &count, query, args...); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *repository) NextCreditNoteNumber(ctx context.Context, tx *sqlx.Tx, orgID uint64, prefix string, period time.Time) (string, error) {
	pattern := escapeLike(prefix) + "-CN-" + period.Format("200601") + "-%"
	var next sql.NullInt64
	err := tx.GetContext(ctx, &next, `
		SELECT COALESCE(MAX(CAST(SUBSTRING_INDEX(credit_note_number, '-', -1) AS UNSIGNED)), 0) + 1
		FROM credit_notes
		WHERE organization_id = ? AND credit_note_number LIKE ? ESCAPE '!'
		FOR UPDATE`, orgID, pattern)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-CN-%s-%03d", prefix, period.Format("200601"), next.Int64), nil
}
