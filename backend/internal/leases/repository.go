package leases

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/rbac"
)

type querier interface {
	GetContext(ctx context.Context, dest any, query string, args ...any) error
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type repository struct {
	db *sqlx.DB
}

const leaseColumns = `
	l.id, l.organization_id, l.unit_id, l.lease_number, l.start_date, l.end_date,
	l.rent_amount, l.deposit_amount, l.billing_day, l.due_day, l.notice_period_days,
	l.status, l.signed_at, l.terminated_at, l.termination_reason, l.created_by,
	l.created_at, l.updated_at`

// leaseViewQuery carries the unit, the property and the primary tenant name.
// Units and properties are joined without a deleted_at filter so a lease keeps
// pointing at the door it was written against after that record is archived.
const leaseViewQuery = `
	SELECT ` + leaseColumns + `,
		u.number AS unit_number,
		u.property_id,
		p.name AS property_name,
		p.code AS property_code,
		(SELECT CONCAT_WS(' ', tu.first_name, tu.last_name)
			FROM lease_tenants lt
			INNER JOIN tenants t ON t.id = lt.tenant_id
			INNER JOIN users tu ON tu.id = t.user_id
			WHERE lt.lease_id = l.id AND lt.role = 'primary'
			ORDER BY lt.tenant_id LIMIT 1) AS primary_tenant
	FROM leases l
	INNER JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
	INNER JOIN properties p ON p.organization_id = l.organization_id AND p.id = u.property_id`

func (r *repository) List(ctx context.Context, orgID uint64, scope rbac.Scope, q ListQuery) ([]LeaseViewRow, int, error) {
	where, args := leaseWhere(orgID, scope, q)
	var total int
	countQuery := `
		SELECT COUNT(*)
		FROM leases l
		INNER JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
		INNER JOIN properties p ON p.organization_id = l.organization_id AND p.id = u.property_id
		WHERE ` + where
	if err := r.db.GetContext(ctx, &total, countQuery, args...); err != nil {
		return nil, 0, err
	}
	order := q.Sort
	if order == "" {
		order = defaultLeaseSort
	}
	listArgs := append(append([]any{}, args...), q.Page.Limit, q.Page.Offset)
	var rows []LeaseViewRow
	err := r.db.SelectContext(ctx, &rows, leaseViewQuery+` WHERE `+where+` ORDER BY `+order+`, l.id DESC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func leaseWhere(orgID uint64, scope rbac.Scope, q ListQuery) (string, []any) {
	where := []string{"l.organization_id = ?"}
	args := []any{orgID}

	clause, scopeArgs := leaseScopeFilter(scope)
	where = append(where, clause)
	args = append(args, scopeArgs...)

	if q.Search != "" {
		like := likePattern(q.Search)
		where = append(where, `(l.lease_number LIKE ? OR u.number LIKE ? OR p.name LIKE ? OR p.code LIKE ?
			OR EXISTS (SELECT 1 FROM lease_tenants lt_s
				INNER JOIN tenants t_s ON t_s.id = lt_s.tenant_id
				INNER JOIN users us_s ON us_s.id = t_s.user_id
				WHERE lt_s.lease_id = l.id AND (CONCAT_WS(' ', us_s.first_name, us_s.last_name) LIKE ? OR us_s.email LIKE ?)))`)
		args = append(args, like, like, like, like, like, like)
	}
	if q.Status != "" {
		where = append(where, "l.status = ?")
		args = append(args, q.Status)
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

// leaseScopeFilter widens a row set to what the caller may see. A property
// scope covers staff; a tenant scope covers the leases they are on. A caller
// with both (a caretaker who also rents a unit) gets the union, and anything
// else sees nothing.
func leaseScopeFilter(scope rbac.Scope) (string, []any) {
	if scope.AllProperties {
		return "1=1", nil
	}
	var parts []string
	var args []any
	if len(scope.PropertyIDs) > 0 {
		clause, clauseArgs := scope.PropertyFilter("u.property_id")
		parts = append(parts, clause)
		args = append(args, clauseArgs...)
	}
	if scope.TenantID != nil {
		parts = append(parts, `EXISTS (SELECT 1 FROM lease_tenants lt_own
			WHERE lt_own.lease_id = l.id AND lt_own.tenant_id = ?)`)
		args = append(args, *scope.TenantID)
	}
	if len(parts) == 0 {
		return "1=0", nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func likePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return "%" + value + "%"
}

func (r *repository) Get(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*LeaseViewRow, error) {
	clause, scopeArgs := leaseScopeFilter(scope)
	args := append([]any{orgID, id}, scopeArgs...)
	var row LeaseViewRow
	err := r.db.GetContext(ctx, &row,
		leaseViewQuery+` WHERE l.organization_id = ? AND l.id = ? AND `+clause, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) GetForUpdate(ctx context.Context, tx *sqlx.Tx, orgID, id uint64, scope rbac.Scope) (*LeaseRow, error) {
	clause, scopeArgs := leaseScopeFilter(scope)
	args := append([]any{orgID, id}, scopeArgs...)
	var row LeaseRow
	err := tx.GetContext(ctx, &row, `
		SELECT `+leaseColumns+`
		FROM leases l
		INNER JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
		INNER JOIN properties p ON p.organization_id = l.organization_id AND p.id = u.property_id
		WHERE l.organization_id = ? AND l.id = ? AND `+clause+`
		FOR UPDATE`, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// UnitForScope reads the unit a lease is being written against. The caller
// checks deleted_at itself so an archived unit can produce a clear error.
func (r *repository) UnitForScope(ctx context.Context, q querier, orgID, unitID uint64, scope rbac.Scope) (*UnitRefRow, error) {
	clause, scopeArgs := scope.PropertyFilter("u.property_id")
	args := append([]any{orgID, unitID}, scopeArgs...)
	var row UnitRefRow
	err := q.GetContext(ctx, &row, `
		SELECT u.id, u.property_id, u.number, u.status, u.deleted_at AS unit_deleted_at,
			p.name AS property_name, p.code AS property_code, p.deleted_at AS property_deleted_at
		FROM units u
		INNER JOIN properties p ON p.organization_id = u.organization_id AND p.id = u.property_id
		WHERE u.organization_id = ? AND u.id = ? AND `+clause, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) Insert(ctx context.Context, tx *sqlx.Tx, row *LeaseRow) (uint64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO leases
			(organization_id, unit_id, lease_number, start_date, end_date, rent_amount, deposit_amount,
			 billing_day, due_day, notice_period_days, status, signed_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.OrganizationID, row.UnitID, row.LeaseNumber, row.StartDate, row.EndDate, row.RentAmount,
		row.DepositAmount, row.BillingDay, row.DueDay, row.NoticePeriodDays, row.Status, row.SignedAt,
		row.CreatedBy)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

func (r *repository) Update(ctx context.Context, tx *sqlx.Tx, row *LeaseRow) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE leases
		SET unit_id = ?, lease_number = ?, start_date = ?, end_date = ?, rent_amount = ?,
		    deposit_amount = ?, billing_day = ?, due_day = ?, notice_period_days = ?,
		    status = ?, signed_at = ?, terminated_at = ?, termination_reason = ?
		WHERE id = ? AND organization_id = ?`,
		row.UnitID, row.LeaseNumber, row.StartDate, row.EndDate, row.RentAmount, row.DepositAmount,
		row.BillingDay, row.DueDay, row.NoticePeriodDays, row.Status, row.SignedAt, row.TerminatedAt,
		row.TerminationReason, row.ID, row.OrganizationID)
	return err
}

// Delete removes a draft lease. lease_tenants rows cascade with it; the status
// guard is repeated in SQL so a concurrent activation cannot be deleted under.
func (r *repository) Delete(ctx context.Context, tx *sqlx.Tx, orgID, id uint64) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		DELETE FROM leases WHERE organization_id = ? AND id = ? AND status = 'draft'`, orgID, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *repository) LeaseNumberTaken(ctx context.Context, q querier, orgID uint64, number string, excludeID uint64) (bool, error) {
	query := `SELECT COUNT(*) FROM leases WHERE organization_id = ? AND lease_number = ?`
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

// NextLeaseNumber continues the per-organization, per-year counter used by the
// demo data: {property code}-L-{year}-{001}, or L-{year}-{001} when the
// property has no code. The locking read reserves the sequence for this
// transaction; the unique index is still the last word.
func (r *repository) NextLeaseNumber(ctx context.Context, tx *sqlx.Tx, orgID uint64, prefix string, year int) (string, error) {
	pattern := escapeLike(prefix) + "-" + strconv.Itoa(year) + "-%"
	var next sql.NullInt64
	err := tx.GetContext(ctx, &next, `
		SELECT COALESCE(MAX(CAST(SUBSTRING_INDEX(lease_number, '-', -1) AS UNSIGNED)), 0) + 1
		FROM leases
		WHERE organization_id = ? AND lease_number LIKE ? ESCAPE '!'
		FOR UPDATE`, orgID, pattern)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%d-%03d", prefix, year, next.Int64), nil
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, "!", "!!")
	value = strings.ReplaceAll(value, "%", "!%")
	value = strings.ReplaceAll(value, "_", "!_")
	return value
}

// ActiveLeaseOnUnit finds the lease that currently holds the unit, ignoring
// the lease being saved. The generated column makes this at most one row.
func (r *repository) ActiveLeaseOnUnit(ctx context.Context, q querier, unitID, excludeLeaseID uint64) (string, error) {
	query := `SELECT lease_number FROM leases WHERE unit_id = ? AND status = 'active'`
	args := []any{unitID}
	if excludeLeaseID != 0 {
		query += ` AND id <> ?`
		args = append(args, excludeLeaseID)
	}
	var number string
	err := q.GetContext(ctx, &number, query+` LIMIT 1`, args...)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return number, nil
}

func (r *repository) Roster(ctx context.Context, q querier, leaseID uint64) ([]TenantRosterRow, error) {
	var rows []TenantRosterRow
	err := q.SelectContext(ctx, &rows, `
		SELECT lt.tenant_id, lt.role,
			CONCAT_WS(' ', u.first_name, u.last_name) AS full_name, u.email, u.phone
		FROM lease_tenants lt
		INNER JOIN tenants t ON t.id = lt.tenant_id
		INNER JOIN users u ON u.id = t.user_id
		WHERE lt.lease_id = ?
		ORDER BY FIELD(lt.role, 'primary', 'co_tenant', 'guarantor', 'occupant'), lt.tenant_id`, leaseID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) ReplaceRoster(ctx context.Context, tx *sqlx.Tx, orgID, leaseID uint64, entries []TenantRef) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM lease_tenants WHERE lease_id = ?`, leaseID); err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`INSERT INTO lease_tenants (organization_id, lease_id, tenant_id, role) VALUES `)
	args := make([]any, 0, len(entries)*4)
	for i, entry := range entries {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("(?, ?, ?, ?)")
		args = append(args, orgID, leaseID, entry.TenantID, entry.Role)
	}
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}

func (r *repository) MissingTenants(ctx context.Context, q querier, orgID uint64, ids []uint64) ([]uint64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, orgID)
	for _, id := range ids {
		args = append(args, id)
	}
	query, args, err := sqlx.In(`
		SELECT t.id FROM tenants t
		INNER JOIN users u ON u.id = t.user_id AND u.deleted_at IS NULL
		WHERE t.organization_id = ? AND t.id IN (?)`, args...)
	if err != nil {
		return nil, err
	}
	var found []uint64
	if err := q.SelectContext(ctx, &found, query, args...); err != nil {
		return nil, err
	}
	known := make(map[uint64]struct{}, len(found))
	for _, id := range found {
		known[id] = struct{}{}
	}
	var missing []uint64
	for _, id := range ids {
		if _, ok := known[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

func (r *repository) Deposits(ctx context.Context, orgID, leaseID uint64) ([]DepositRow, error) {
	var rows []DepositRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT id, amount, date_paid, status
		FROM deposits WHERE organization_id = ? AND lease_id = ? ORDER BY id`, orgID, leaseID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// HasDeposit reports whether a lease already carries a deposit record, so
// activating a second lease for the same tenancy cannot double the obligation.
func (r *repository) HasDeposit(ctx context.Context, q querier, orgID, leaseID uint64) (bool, error) {
	var count int
	err := q.GetContext(ctx, &count, `
		SELECT COUNT(*) FROM deposits WHERE organization_id = ? AND lease_id = ?`, orgID, leaseID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// OpenDeposit records what the tenant owes when the lease comes into force. It
// stays pending until the money arrives, which is the billing module's work.
func (r *repository) OpenDeposit(ctx context.Context, tx *sqlx.Tx, orgID, leaseID uint64, amount float64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO deposits (organization_id, lease_id, amount, status)
		VALUES (?, ?, ?, 'pending')`, orgID, leaseID, amount)
	return err
}

// ExpireOverdue flips a batch of active leases whose end date has passed to
// expired and reports how many rows moved.
func (r *repository) ExpireOverdue(ctx context.Context, q querier, limit int) (int64, error) {
	res, err := q.ExecContext(ctx, `
		UPDATE leases
		SET status = 'expired'
		WHERE status = 'active' AND end_date IS NOT NULL AND end_date < UTC_DATE()
		ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetUnitStatus moves a unit between vacant and occupied. Occupancy is the
// lease module's to own: the units module refuses to set it directly.
func (r *repository) SetUnitStatus(ctx context.Context, tx *sqlx.Tx, orgID, unitID uint64, status string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE units SET status = ?
		WHERE organization_id = ? AND id = ? AND deleted_at IS NULL`, status, orgID, unitID)
	return err
}
