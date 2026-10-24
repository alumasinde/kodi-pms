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

// querier lets a repository method run on either the pool or a transaction.
type querier interface {
	GetContext(ctx context.Context, dest any, query string, args ...any) error
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// repository is the package's only SQL door. The domain files
// (invoice_repo.go, payment_repo.go, latefee_repo.go, ledger_repo.go,
// creditnote_repo.go) hang their queries off it so the scope filter, the LIKE
// escaping and the org/lease lookups below exist once.
type repository struct {
	db *sqlx.DB
}

// tenantNameSubquery names the primary tenant of a lease. The invoice view uses
// it as it stands and ledgerViewQuery rewrites its lease alias, so it lives with
// the shared plumbing rather than in either domain file.
const tenantNameSubquery = `
		(SELECT CONCAT_WS(' ', tu.first_name, tu.last_name)
			FROM lease_tenants lt
			INNER JOIN tenants t ON t.id = lt.tenant_id
			INNER JOIN users tu ON tu.id = t.user_id
			WHERE lt.lease_id = l.id AND lt.role = 'primary'
			ORDER BY lt.tenant_id LIMIT 1)`

// scopeFilter answers "which rows may this caller see". Staff are limited to
// their properties; a tenant is limited to the leases they are on. A caller
// with both, for example a caretaker who also rents a unit, gets the union and
// anyone else sees nothing.
func scopeFilter(scope rbac.Scope, propertyColumn, leaseColumn string) (string, []any) {
	if scope.AllProperties {
		return "1=1", nil
	}
	var parts []string
	var args []any
	if len(scope.PropertyIDs) > 0 {
		clause, clauseArgs := scope.PropertyFilter(propertyColumn)
		parts = append(parts, clause)
		args = append(args, clauseArgs...)
	}
	if scope.TenantID != nil {
		parts = append(parts, `EXISTS (SELECT 1 FROM lease_tenants lt_own
			WHERE lt_own.lease_id = `+leaseColumn+` AND lt_own.tenant_id = ?)`)
		args = append(args, *scope.TenantID)
	}
	if len(parts) == 0 {
		return "1=0", nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func tenantOnly(scope rbac.Scope) bool {
	return scope.TenantID != nil && !scope.AllProperties && len(scope.PropertyIDs) == 0
}

func likePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return "%" + value + "%"
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, "!", "!!")
	value = strings.ReplaceAll(value, "%", "!%")
	value = strings.ReplaceAll(value, "_", "!_")
	return value
}

// Settings resolves the billing keys for one organization. Keys are seeded as
// global defaults by 000003 and may be overridden per organization, so both
// sets are read and the organization's own values win.
func (r *repository) Settings(ctx context.Context, orgID uint64) (BillingSettings, error) {
	var rows []settingRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT setting_key, setting_value, organization_id
		FROM system_settings
		WHERE organization_id IS NULL OR organization_id = ?`, orgID)
	if err != nil {
		return BillingSettings{}, err
	}
	return mergeSettings(rows, orgID), nil
}

type settingRow struct {
	Key            string         `db:"setting_key"`
	Value          sql.NullString `db:"setting_value"`
	OrganizationID sql.NullInt64  `db:"organization_id"`
}

func (r *repository) BillingLease(ctx context.Context, q querier, orgID, leaseID uint64, scope rbac.Scope, forUpdate bool) (*BillingLeaseRow, error) {
	if _, isTx := q.(*sqlx.Tx); isTx && !forUpdate {
		return nil, fmt.Errorf("billing: a transaction read must lock")
	}
	clause, scopeArgs := scopeFilter(scope, "u.property_id", "l.id")
	args := append([]any{orgID, leaseID}, scopeArgs...)
	query := `
		SELECT ` + billingLeaseColumns + `
		FROM leases l
		INNER JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
		INNER JOIN properties p ON p.organization_id = u.organization_id AND p.id = u.property_id
		WHERE l.organization_id = ? AND l.id = ? AND ` + clause
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var row BillingLeaseRow
	if err := q.GetContext(ctx, &row, query, args...); err != nil {
		return nil, err
	}
	return &row, nil
}

const billingLeaseColumns = `
	l.id, l.organization_id, l.lease_number, l.unit_id, u.property_id, p.code AS property_code,
	u.number AS unit_number, l.start_date, l.end_date, l.rent_amount, l.deposit_amount,
	l.billing_day, l.due_day, l.status,
	(SELECT d.id FROM deposits d WHERE d.organization_id = l.organization_id AND d.lease_id = l.id
		AND d.status = 'pending' ORDER BY d.id LIMIT 1) AS pending_deposit,
	(SELECT MAX(v.billing_period_start) FROM invoices v WHERE v.lease_id = l.id AND v.invoice_type = 'rent') AS last_rent_period`

// InvoicingLeases walks the active leases of one organization for the
// recurring-invoice sweep. Keyset pagination on the lease id keeps every batch
// cheap and lets several instances run the same sweep harmlessly.
func (r *repository) InvoicingLeases(ctx context.Context, orgID, afterLeaseID uint64, limit int) ([]BillingLeaseRow, error) {
	var rows []BillingLeaseRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT `+billingLeaseColumns+`
		FROM leases l
		INNER JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
		INNER JOIN properties p ON p.organization_id = u.organization_id AND p.id = u.property_id
		WHERE l.organization_id = ? AND l.status = 'active' AND l.id > ?
		ORDER BY l.id LIMIT ?`, orgID, afterLeaseID, limit)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// HoldDeposit moves a pending deposit to held once the invoice carrying the
// deposit line is fully paid. The status guard makes a replay a no-op.
func (r *repository) HoldDeposit(ctx context.Context, tx *sqlx.Tx, orgID, depositID uint64, paidOn time.Time) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE deposits SET status = 'held', date_paid = ?
		WHERE organization_id = ? AND id = ? AND status = 'pending'`, paidOn, orgID, depositID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// HasDepositItem reports whether an invoice already bills a deposit, which is
// what stops the sweep from charging the deposit a second time.
func (r *repository) HasDepositItem(ctx context.Context, q querier, orgID, leaseID uint64) (bool, error) {
	var count int
	err := q.GetContext(ctx, &count, `
		SELECT COUNT(*) FROM invoice_items it
		INNER JOIN invoices iv ON iv.organization_id = it.organization_id AND iv.id = it.invoice_id
		WHERE it.organization_id = ? AND iv.lease_id = ? AND it.item_type = 'deposit'`, orgID, leaseID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// InvoiceHasDepositItem scopes the same question to one invoice, which is the
// invoice whose clearance holds the deposit.
func (r *repository) InvoiceHasDepositItem(ctx context.Context, q querier, orgID, invoiceID uint64) (bool, error) {
	var count int
	err := q.GetContext(ctx, &count, `
		SELECT COUNT(*) FROM invoice_items
		WHERE organization_id = ? AND invoice_id = ? AND item_type = 'deposit'`, orgID, invoiceID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// PendingDeposit finds the deposit row a lease still owes. Leases open one
// deposit per lease, so the lowest pending id is the live one.
func (r *repository) PendingDeposit(ctx context.Context, q querier, orgID, leaseID uint64) (uint64, bool, error) {
	var id sql.NullInt64
	err := q.GetContext(ctx, &id, `
		SELECT id FROM deposits
		WHERE organization_id = ? AND lease_id = ? AND status = 'pending'
		ORDER BY id LIMIT 1`, orgID, leaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !id.Valid || id.Int64 <= 0 {
		return 0, false, nil
	}
	return uint64(id.Int64), true, nil
}

func (r *repository) Organizations(ctx context.Context) ([]organizationClock, error) {
	var rows []organizationClock
	err := r.db.SelectContext(ctx, &rows, `
		SELECT id, timezone FROM organizations
		WHERE deleted_at IS NULL AND org_status = 'active' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// OrganizationTimezone is the clock the billing date maths runs on. A missing
// row is not an error here: the caller falls back to the operating timezone.
func (r *repository) OrganizationTimezone(ctx context.Context, orgID uint64) (string, error) {
	var tz sql.NullString
	err := r.db.GetContext(ctx, &tz, `SELECT timezone FROM organizations WHERE id = ?`, orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(tz.String), nil
}
