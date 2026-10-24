package billing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/rbac"
)

const invoiceColumns = `
	i.id, i.organization_id, i.lease_id, i.invoice_number, i.invoice_type,
	i.billing_period_start, i.billing_period_end, i.issue_date, i.due_date,
	i.subtotal, i.tax_total, i.total, i.amount_paid, i.balance, i.status,
	i.notes, i.created_by, i.created_at, i.updated_at`

// invoiceFrom joins the lease, unit and property so scope can be applied and a
// list row can name the tenant. Parents are joined without a deleted_at filter:
// an invoice must stay readable after the unit or property is archived.
const invoiceFrom = `
	FROM invoices i
	INNER JOIN leases l ON l.organization_id = i.organization_id AND l.id = i.lease_id
	INNER JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
	INNER JOIN properties p ON p.organization_id = u.organization_id AND p.id = u.property_id`

const invoiceViewQuery = `
	SELECT ` + invoiceColumns + `,
		l.lease_number, u.number AS unit_number, u.property_id,
		p.name AS property_name, p.code AS property_code,` + tenantNameSubquery + ` AS primary_tenant` + invoiceFrom

func (r *repository) ListInvoices(ctx context.Context, orgID uint64, scope rbac.Scope, q InvoiceQuery, hideDrafts bool) ([]InvoiceViewRow, int, error) {
	where, args := invoiceWhere(orgID, scope, q, hideDrafts)
	var total int
	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*)`+invoiceFrom+` WHERE `+where, args...); err != nil {
		return nil, 0, err
	}
	order := q.Sort
	if order == "" {
		order = defaultInvoiceSort
	}
	listArgs := append(append([]any{}, args...), q.Page.Limit, q.Page.Offset)
	var rows []InvoiceViewRow
	err := r.db.SelectContext(ctx, &rows, invoiceViewQuery+` WHERE `+where+` ORDER BY `+order+`, i.id DESC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func invoiceWhere(orgID uint64, scope rbac.Scope, q InvoiceQuery, hideDrafts bool) (string, []any) {
	where := []string{"i.organization_id = ?"}
	args := []any{orgID}

	clause, scopeArgs := scopeFilter(scope, "u.property_id", "l.id")
	where = append(where, clause)
	args = append(args, scopeArgs...)
	if hideDrafts {
		where = append(where, `i.status <> 'draft'`)
	}
	if q.Search != "" {
		like := likePattern(q.Search)
		where = append(where, `(i.invoice_number LIKE ? OR l.lease_number LIKE ? OR u.number LIKE ? OR p.name LIKE ?)`)
		args = append(args, like, like, like, like)
	}
	if q.Status != "" {
		where = append(where, "i.status = ?")
		args = append(args, q.Status)
	}
	if q.Type != "" {
		where = append(where, "i.invoice_type = ?")
		args = append(args, q.Type)
	}
	if q.Overdue {
		where = append(where, `i.status NOT IN ('draft','void','paid') AND i.balance > 0`)
	}
	if !q.DueFrom.IsZero() {
		where = append(where, "i.due_date >= ?")
		args = append(args, q.DueFrom)
	}
	if !q.DueTo.IsZero() {
		where = append(where, "i.due_date <= ?")
		args = append(args, q.DueTo)
	}
	if q.LeaseID != 0 {
		where = append(where, "i.lease_id = ?")
		args = append(args, q.LeaseID)
	}
	if q.PropertyID != 0 {
		where = append(where, "u.property_id = ?")
		args = append(args, q.PropertyID)
	}
	if q.UnitID != 0 {
		where = append(where, "l.unit_id = ?")
		args = append(args, q.UnitID)
	}
	if q.TenantID != 0 {
		where = append(where, `EXISTS (SELECT 1 FROM lease_tenants lt_f WHERE lt_f.lease_id = l.id AND lt_f.tenant_id = ?)`)
		args = append(args, q.TenantID)
	}
	return strings.Join(where, " AND "), args
}

func (r *repository) GetInvoice(ctx context.Context, orgID, id uint64, scope rbac.Scope, hideDrafts bool) (*InvoiceViewRow, error) {
	clause, scopeArgs := scopeFilter(scope, "u.property_id", "l.id")
	args := append([]any{orgID, id}, scopeArgs...)
	query := invoiceViewQuery + ` WHERE i.organization_id = ? AND i.id = ? AND ` + clause
	if hideDrafts {
		query += ` AND i.status <> 'draft'`
	}
	var row InvoiceViewRow
	if err := r.db.GetContext(ctx, &row, query, args...); err != nil {
		return nil, err
	}
	return &row, nil
}

// InvoiceForUpdate locks the invoice so two payments cannot race over the same
// balance. The locking read skips the tenant-name subquery and reads base
// columns only.
func (r *repository) InvoiceForUpdate(ctx context.Context, tx *sqlx.Tx, orgID, id uint64, scope rbac.Scope, hideDrafts bool) (*InvoiceRow, error) {
	clause, scopeArgs := scopeFilter(scope, "u.property_id", "l.id")
	args := append([]any{orgID, id}, scopeArgs...)
	query := `SELECT ` + invoiceColumns + invoiceFrom + ` WHERE i.organization_id = ? AND i.id = ? AND ` + clause
	if hideDrafts {
		query += ` AND i.status <> 'draft'`
	}
	var row InvoiceRow
	if err := tx.GetContext(ctx, &row, query+` FOR UPDATE`, args...); err != nil {
		return nil, err
	}
	return &row, nil
}

// InvoiceBalanceByID reads the money view of one invoice without the joins a
// full row needs. excludeInvoiceID is used when the caller already holds a
// lock on the invoice.
func (r *repository) InvoiceBalanceByID(ctx context.Context, q querier, orgID, id uint64) (*InvoiceBalance, error) {
	var row InvoiceBalance
	err := q.GetContext(ctx, &row, `
		SELECT i.id, i.lease_id, i.invoice_number, i.due_date, i.total, i.amount_paid, i.status,
			i.total - i.amount_paid AS balance
		FROM invoices i WHERE i.organization_id = ? AND i.id = ? FOR UPDATE`, orgID, id)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) OpenInvoiceBalances(ctx context.Context, q querier, orgID, leaseID uint64) ([]InvoiceBalance, error) {
	var rows []InvoiceBalance
	err := q.SelectContext(ctx, &rows, `
		SELECT i.id, i.lease_id, i.invoice_number, i.due_date, i.total, i.amount_paid, i.status,
			i.total - i.amount_paid AS balance, l.rent_amount
		FROM invoices i
		INNER JOIN leases l ON l.organization_id = i.organization_id AND l.id = i.lease_id
		WHERE i.organization_id = ? AND i.lease_id = ?
		  AND i.status NOT IN ('draft','void') AND i.total > i.amount_paid
		ORDER BY i.due_date ASC, i.id ASC
		FOR UPDATE`, orgID, leaseID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) InsertInvoice(ctx context.Context, tx *sqlx.Tx, row *InvoiceRow) (uint64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO invoices
			(organization_id, lease_id, invoice_number, invoice_type, billing_period_start,
			 billing_period_end, issue_date, due_date, subtotal, tax_total, total, status, notes, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.OrganizationID, row.LeaseID, row.InvoiceNumber, row.InvoiceType, row.BillingPeriodStart,
		row.BillingPeriodEnd, row.IssueDate, row.DueDate, row.Subtotal, row.TaxTotal, row.Total,
		row.Status, row.Notes, row.CreatedBy)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

// UpdateInvoice writes the draft-editable columns. amount_paid is left out on
// purpose: a trigger derives it from live payment allocations.
func (r *repository) UpdateInvoice(ctx context.Context, tx *sqlx.Tx, row *InvoiceRow) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE invoices
		SET invoice_number = ?, invoice_type = ?, billing_period_start = ?, billing_period_end = ?,
		    issue_date = ?, due_date = ?, subtotal = ?, tax_total = ?, total = ?, status = ?, notes = ?
		WHERE id = ? AND organization_id = ?`,
		row.InvoiceNumber, row.InvoiceType, row.BillingPeriodStart, row.BillingPeriodEnd, row.IssueDate,
		row.DueDate, row.Subtotal, row.TaxTotal, row.Total, row.Status, row.Notes, row.ID, row.OrganizationID)
	return err
}

func (r *repository) SetInvoiceStatus(ctx context.Context, tx *sqlx.Tx, orgID, id uint64, status string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE invoices SET status = ? WHERE id = ? AND organization_id = ?`, status, id, orgID)
	return err
}

// SetInvoiceIssued re-prices, dates and issues a draft in one statement. The
// status guard makes a replay by a second instance affect no rows, so the
// journal is only ever posted once.
func (r *repository) SetInvoiceIssued(ctx context.Context, tx *sqlx.Tx, orgID, id uint64, subtotal, taxTotal, total float64, issueDate time.Time) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE invoices SET subtotal = ?, tax_total = ?, total = ?, issue_date = ?, status = 'issued'
		WHERE id = ? AND organization_id = ? AND status = 'draft' AND amount_paid = 0`,
		subtotal, taxTotal, total, issueDate, id, orgID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// AddInvoiceCharge appends a charge to an already issued invoice, used by the
// late-fee engine. total moves with subtotal so subtotal + tax_total = total.
func (r *repository) AddInvoiceCharge(ctx context.Context, tx *sqlx.Tx, orgID, id uint64, amount float64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE invoices SET subtotal = subtotal + ?, total = total + ?
		WHERE id = ? AND organization_id = ?`, amount, amount, id, orgID)
	return err
}

func (r *repository) ReduceInvoiceTotal(ctx context.Context, tx *sqlx.Tx, orgID, id uint64, preTax, tax, total float64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE invoices SET subtotal = subtotal - ?, tax_total = tax_total - ?, total = total - ?
		WHERE id = ? AND organization_id = ?`, preTax, tax, total, id, orgID)
	return err
}

// DeleteInvoice removes a draft that has no money against it. The status and
// amount_paid guards are repeated in SQL so a concurrent payment or issue
// cannot be deleted out from under.
func (r *repository) DeleteInvoice(ctx context.Context, tx *sqlx.Tx, orgID, id uint64) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		DELETE FROM invoices WHERE organization_id = ? AND id = ? AND status = 'draft' AND amount_paid = 0`,
		orgID, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *repository) InvoiceNumberTaken(ctx context.Context, q querier, orgID uint64, number string, excludeID uint64) (bool, error) {
	query := `SELECT COUNT(*) FROM invoices WHERE organization_id = ? AND invoice_number = ?`
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

// NextInvoiceNumber continues the per-organization counter used by the demo
// data: {prefix}-{property code}-{YYYYMM}-{001}. The locking read reserves the
// sequence for this transaction; the unique index is still the last word.
func (r *repository) NextInvoiceNumber(ctx context.Context, tx *sqlx.Tx, orgID uint64, prefix string, period time.Time) (string, error) {
	pattern := escapeLike(prefix) + "-" + period.Format("200601") + "-%"
	var next sql.NullInt64
	err := tx.GetContext(ctx, &next, `
		SELECT COALESCE(MAX(CAST(SUBSTRING_INDEX(invoice_number, '-', -1) AS UNSIGNED)), 0) + 1
		FROM invoices
		WHERE organization_id = ? AND invoice_number LIKE ? ESCAPE '!'
		FOR UPDATE`, orgID, pattern)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%03d", prefix, period.Format("200601"), next.Int64), nil
}

func (r *repository) Items(ctx context.Context, orgID, invoiceID uint64) ([]InvoiceItemRow, error) {
	return r.ItemsTx(ctx, r.db, orgID, invoiceID)
}

// ItemsTx reads the lines through the caller's querier, so the rent sweep can
// price an invoice from lines it inserted seconds earlier in the same
// transaction. Those rows are invisible to a read on the pool.
func (r *repository) ItemsTx(ctx context.Context, q querier, orgID, invoiceID uint64) ([]InvoiceItemRow, error) {
	var rows []InvoiceItemRow
	err := q.SelectContext(ctx, &rows, `
		SELECT id, organization_id, invoice_id, item_type, description, quantity, unit_price,
			tax_rate, amount, source_type, source_id
		FROM invoice_items WHERE organization_id = ? AND invoice_id = ? ORDER BY id`, orgID, invoiceID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) InsertItem(ctx context.Context, tx *sqlx.Tx, item *InvoiceItemRow) (uint64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO invoice_items
			(organization_id, invoice_id, item_type, description, quantity, unit_price, tax_rate, amount, source_type, source_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.OrganizationID, item.InvoiceID, item.ItemType, item.Description, item.Quantity,
		item.UnitPrice, item.TaxRate, item.Amount, item.SourceType, item.SourceID)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

// ReplaceItems rewrites the lines of a draft invoice. Lines that carry a
// source keep it only when the caller sends it, so a replaced utility line is
// never left pointing at a stale bill.
func (r *repository) ReplaceItems(ctx context.Context, tx *sqlx.Tx, orgID, invoiceID uint64, items []InvoiceItemRow) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM invoice_items WHERE organization_id = ? AND invoice_id = ?`, orgID, invoiceID); err != nil {
		return err
	}
	for i := range items {
		items[i].OrganizationID = orgID
		items[i].InvoiceID = invoiceID
		if _, err := r.InsertItem(ctx, tx, &items[i]); err != nil {
			return err
		}
	}
	return nil
}

func (r *repository) Summary(ctx context.Context, orgID uint64, scope rbac.Scope, q InvoiceQuery, hideDrafts bool, today time.Time) (*SummaryRow, error) {
	where, args := invoiceWhere(orgID, scope, q, hideDrafts)
	args = append(args, today, today)
	var row SummaryRow
	err := r.db.GetContext(ctx, &row, `
		SELECT
			COALESCE(SUM(CASE WHEN i.status <> 'draft' THEN i.total ELSE 0 END), 0) AS billed,
			COALESCE(SUM(i.amount_paid), 0) AS collected,
			COALESCE(SUM(CASE WHEN i.status NOT IN ('draft','void') THEN i.balance ELSE 0 END), 0) AS outstanding,
			COALESCE(SUM(CASE WHEN i.status NOT IN ('draft','void','paid') AND i.balance > 0 AND i.due_date < ?
				THEN i.balance ELSE 0 END), 0) AS overdue_amount,
			COALESCE(SUM(CASE WHEN i.status NOT IN ('draft','void','paid') AND i.balance > 0 THEN 1 ELSE 0 END), 0) AS open_count,
			COALESCE(SUM(CASE WHEN i.status NOT IN ('draft','void','paid') AND i.balance > 0 AND i.due_date < ? THEN 1 ELSE 0 END), 0) AS overdue_count,
			COALESCE(SUM(CASE WHEN i.status = 'draft' THEN 1 ELSE 0 END), 0) AS draft_count
		`+invoiceFrom+` WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	credit, err := r.creditTotal(ctx, orgID, scope, q.LeaseID)
	if err != nil {
		return nil, err
	}
	row.Credit = credit
	return &row, nil
}

// FindInvoiceForPeriod is how the sweep picks a draft up again: the unique key
// says an invoice for this lease and period already exists, and the sweep needs
// its id and status to decide whether to issue it.
func (r *repository) FindInvoiceForPeriod(ctx context.Context, q querier, orgID, leaseID uint64, invoiceType string, periodStart time.Time) (uint64, string, bool, error) {
	var row struct {
		ID     uint64 `db:"id"`
		Status string `db:"status"`
	}
	err := q.GetContext(ctx, &row, `
		SELECT id, status FROM invoices
		WHERE organization_id = ? AND lease_id = ? AND invoice_type = ? AND billing_period_start = ?
		LIMIT 1`, orgID, leaseID, invoiceType, periodStart)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return row.ID, row.Status, true, nil
}

func (r *repository) MarkOverdue(ctx context.Context, q querier, orgID uint64, today time.Time, limit int) (int64, error) {
	res, err := q.ExecContext(ctx, `
		UPDATE invoices
		SET status = 'overdue'
		WHERE organization_id = ? AND status = 'issued' AND balance > 0 AND due_date < ?
		ORDER BY id LIMIT ?`, orgID, today, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PeriodTaken reports the one-invoice-per-lease-per-type-per-period clash that
// the unique key enforces, so a manual invoice can be refused with a message
// instead of a database error.
func (r *repository) PeriodTaken(ctx context.Context, q querier, orgID, leaseID uint64, invoiceType string, periodStart time.Time, excludeID uint64) (string, bool, error) {
	query := `
		SELECT invoice_number FROM invoices
		WHERE organization_id = ? AND lease_id = ? AND invoice_type = ? AND billing_period_start = ?`
	args := []any{orgID, leaseID, invoiceType, periodStart}
	if excludeID != 0 {
		query += ` AND id <> ?`
		args = append(args, excludeID)
	}
	query += ` LIMIT 1`
	var number string
	err := q.GetContext(ctx, &number, query, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return number, true, nil
}
