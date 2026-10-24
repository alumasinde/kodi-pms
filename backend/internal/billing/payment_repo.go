package billing

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/rbac"
)

func (r *repository) InsertAllocation(ctx context.Context, tx *sqlx.Tx, orgID, paymentID, invoiceID uint64, amount float64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO payment_allocations (organization_id, payment_id, invoice_id, amount)
		VALUES (?, ?, ?, ?)`, orgID, paymentID, invoiceID, amount)
	return err
}

func (r *repository) AllocationsForInvoice(ctx context.Context, orgID, invoiceID uint64) ([]AllocationRow, error) {
	var rows []AllocationRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT a.id, a.payment_id, a.invoice_id, a.amount, a.reversed_at, a.created_at,
			i.invoice_number, i.issue_date
		FROM payment_allocations a
		INNER JOIN invoices i ON i.organization_id = a.organization_id AND i.id = a.invoice_id
		WHERE a.organization_id = ? AND a.invoice_id = ? ORDER BY a.id`, orgID, invoiceID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) AllocationsForPayment(ctx context.Context, orgID, paymentID uint64) ([]AllocationRow, error) {
	var rows []AllocationRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT a.id, a.payment_id, a.invoice_id, a.amount, a.reversed_at, a.created_at,
			i.invoice_number, i.issue_date
		FROM payment_allocations a
		INNER JOIN invoices i ON i.organization_id = a.organization_id AND i.id = a.invoice_id
		WHERE a.organization_id = ? AND a.payment_id = ? ORDER BY a.id`, orgID, paymentID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// PaymentAllocated totals the live allocations of a payment. Reversed ones stay
// out of the figure, which is what makes a reversal idempotent to read.
func (r *repository) PaymentAllocated(ctx context.Context, q querier, orgID, paymentID uint64) (float64, error) {
	var total sql.NullFloat64
	err := q.GetContext(ctx, &total, `
		SELECT COALESCE(SUM(amount), 0) FROM payment_allocations
		WHERE organization_id = ? AND payment_id = ? AND reversed_at IS NULL`, orgID, paymentID)
	if err != nil {
		return 0, err
	}
	return roundToCents(total.Float64), nil
}

// CreditSources lists money received on a lease that has not been allocated to
// an invoice yet, oldest first. A payment that already touched the invoice
// being credited is skipped because one payment can hold only one allocation
// per invoice.
func (r *repository) CreditSources(ctx context.Context, tx *sqlx.Tx, orgID, leaseID, excludeInvoiceID uint64) ([]CreditSourceRow, error) {
	var rows []CreditSourceRow
	err := tx.SelectContext(ctx, &rows, `
		SELECT p.id AS payment_id, p.paid_at,
			p.amount - COALESCE((SELECT SUM(a.amount) FROM payment_allocations a
				WHERE a.payment_id = p.id AND a.reversed_at IS NULL), 0) AS unallocated
		FROM payments p
		WHERE p.organization_id = ? AND p.lease_id = ? AND p.status = 'completed'
		  AND p.amount - COALESCE((SELECT SUM(a.amount) FROM payment_allocations a
				WHERE a.payment_id = p.id AND a.reversed_at IS NULL), 0) > 0.004
		  AND NOT EXISTS (SELECT 1 FROM payment_allocations a2
				WHERE a2.payment_id = p.id AND a2.invoice_id = ?)
		ORDER BY p.paid_at ASC, p.id ASC`, orgID, leaseID, excludeInvoiceID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

const paymentColumns = `
	p.id, p.organization_id, p.lease_id, p.payment_method_id, p.amount, p.paid_at,
	p.reference, p.status, p.received_by, p.notes, p.created_at, p.updated_at`

const paymentViewQuery = `
	SELECT ` + paymentColumns + `,
		pm.code AS method_code, pm.name AS method_name,
		l.lease_number, u.number AS unit_number, u.property_id, p2.name AS property_name,
		COALESCE((SELECT SUM(a.amount) FROM payment_allocations a
			WHERE a.payment_id = p.id AND a.reversed_at IS NULL), 0) AS allocated,
		pr.reversed_at, pr.reason AS reversal_reason, pr.reversed_by AS reversal_by
	FROM payments p
	INNER JOIN payment_methods pm ON pm.id = p.payment_method_id
	INNER JOIN leases l ON l.organization_id = p.organization_id AND l.id = p.lease_id
	INNER JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
	INNER JOIN properties p2 ON p2.organization_id = u.organization_id AND p2.id = u.property_id
	LEFT JOIN payment_reversals pr ON pr.payment_id = p.id`

func (r *repository) ListPayments(ctx context.Context, orgID uint64, scope rbac.Scope, q PaymentQuery) ([]PaymentViewRow, int, error) {
	where, args := paymentWhere(orgID, scope, q)
	var total int
	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*)`+paymentFrom+` WHERE `+where, args...); err != nil {
		return nil, 0, err
	}
	order := q.Sort
	if order == "" {
		order = defaultPaymentSort
	}
	listArgs := append(append([]any{}, args...), q.Page.Limit, q.Page.Offset)
	var rows []PaymentViewRow
	err := r.db.SelectContext(ctx, &rows, paymentViewQuery+` WHERE `+where+` ORDER BY `+order+`, p.id DESC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

const paymentFrom = `
	FROM payments p
	INNER JOIN payment_methods pm ON pm.id = p.payment_method_id
	INNER JOIN leases l ON l.organization_id = p.organization_id AND l.id = p.lease_id
	INNER JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
	INNER JOIN properties p2 ON p2.organization_id = u.organization_id AND p2.id = u.property_id`

func paymentWhere(orgID uint64, scope rbac.Scope, q PaymentQuery) (string, []any) {
	where := []string{"p.organization_id = ?"}
	args := []any{orgID}

	clause, scopeArgs := scopeFilter(scope, "u.property_id", "p.lease_id")
	where = append(where, clause)
	args = append(args, scopeArgs...)
	if q.Search != "" {
		like := likePattern(q.Search)
		where = append(where, `(p.reference LIKE ? OR l.lease_number LIKE ? OR u.number LIKE ? OR p.notes LIKE ?)`)
		args = append(args, like, like, like, like)
	}
	if q.Status != "" {
		where = append(where, "p.status = ?")
		args = append(args, q.Status)
	}
	if q.LeaseID != 0 {
		where = append(where, "p.lease_id = ?")
		args = append(args, q.LeaseID)
	}
	if q.MethodID != 0 {
		where = append(where, "p.payment_method_id = ?")
		args = append(args, q.MethodID)
	}
	if q.PropertyID != 0 {
		where = append(where, "u.property_id = ?")
		args = append(args, q.PropertyID)
	}
	if !q.From.IsZero() {
		where = append(where, "p.paid_at >= ?")
		args = append(args, q.From)
	}
	if !q.To.IsZero() {
		where = append(where, "p.paid_at <= ?")
		args = append(args, q.To)
	}
	return strings.Join(where, " AND "), args
}

func (r *repository) GetPayment(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*PaymentViewRow, error) {
	clause, scopeArgs := scopeFilter(scope, "u.property_id", "p.lease_id")
	args := append([]any{orgID, id}, scopeArgs...)
	var row PaymentViewRow
	err := r.db.GetContext(ctx, &row, paymentViewQuery+` WHERE p.organization_id = ? AND p.id = ? AND `+clause, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// PaymentForUpdate locks the payment and proves it is in the caller's scope
// before any reversal is written.
func (r *repository) PaymentForUpdate(ctx context.Context, tx *sqlx.Tx, orgID, id uint64, scope rbac.Scope) (*PaymentRow, error) {
	clause, scopeArgs := scopeFilter(scope, "u.property_id", "p.lease_id")
	args := append([]any{orgID, id}, scopeArgs...)
	var row PaymentRow
	err := tx.GetContext(ctx, &row, `SELECT `+paymentColumns+paymentFrom+` WHERE p.organization_id = ? AND p.id = ? AND `+clause+` FOR UPDATE`, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) InsertPayment(ctx context.Context, tx *sqlx.Tx, row *PaymentRow) (uint64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO payments
			(organization_id, lease_id, payment_method_id, amount, paid_at, reference, status, received_by, notes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.OrganizationID, row.LeaseID, row.PaymentMethodID, row.Amount, row.PaidAt, row.Reference,
		row.Status, row.ReceivedBy, row.Notes)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

// InsertReversal writes the reversal row; triggers mark every live allocation
// reversed and move the payment itself to 'reversed'.
func (r *repository) InsertReversal(ctx context.Context, tx *sqlx.Tx, orgID, paymentID uint64, amount float64, reason string, userID uint64, at time.Time) (uint64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO payment_reversals (organization_id, payment_id, amount, reason, reversed_by, reversed_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		orgID, paymentID, amount, nullableText(reason), nullableID(userID), at)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

// AllocationTargets lists the invoices a payment still has live money on. The
// reversal has to re-settle each one because amount_paid falls with the
// allocations the trigger just reversed.
func (r *repository) AllocationTargets(ctx context.Context, q querier, orgID, paymentID uint64) ([]uint64, error) {
	var rows []uint64
	err := q.SelectContext(ctx, &rows, `
		SELECT DISTINCT invoice_id FROM payment_allocations
		WHERE organization_id = ? AND payment_id = ? AND reversed_at IS NULL`, orgID, paymentID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) PaymentMethods(ctx context.Context) ([]PaymentMethodRow, error) {
	var rows []PaymentMethodRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT id, code, name, description, is_active FROM payment_methods ORDER BY code`)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) PaymentMethod(ctx context.Context, id uint64) (*PaymentMethodRow, error) {
	var row PaymentMethodRow
	err := r.db.GetContext(ctx, &row, `
		SELECT id, code, name, description, is_active FROM payment_methods WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// creditTotal derives spendable credit from money received minus money
// allocated, which is exactly the unallocated_funds balance of the scope.
func (r *repository) creditTotal(ctx context.Context, orgID uint64, scope rbac.Scope, leaseID uint64) (float64, error) {
	q := PaymentQuery{LeaseID: leaseID}
	where, args := paymentWhere(orgID, scope, q)
	var credit sql.NullFloat64
	err := r.db.GetContext(ctx, &credit, `
		SELECT COALESCE(SUM(p.amount - COALESCE((SELECT SUM(a.amount) FROM payment_allocations a
			WHERE a.payment_id = p.id AND a.reversed_at IS NULL), 0)), 0) AS credit
		`+paymentFrom+` WHERE `+where+` AND p.status = 'completed'`, args...)
	if err != nil {
		return 0, err
	}
	return roundToCents(credit.Float64), nil
}
