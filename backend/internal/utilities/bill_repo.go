package utilities

import (
	"context"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/rbac"
)

const billColumns = `
	ub.id, ub.unit_id, ub.meter_id, ub.reading_id, ub.invoice_id, ub.type, ub.amount,
	ub.date_due, ub.period_start, ub.period_end, ub.status, ub.created_at, ub.updated_at,
	u.property_id, p.name AS property_name, p.code AS property_code, u.number AS unit_number,
	m.meter_number, m.utility_type AS meter_utility_type,
	r.reading_date, r.consumption,
	iv.invoice_number, iv.status AS invoice_status,
	l.id AS lease_id, l.lease_number,` + tenantNameSubquery + ` AS tenant_name`

const billFrom = `
	FROM utility_bills ub
	INNER JOIN units u ON u.id = ub.unit_id AND u.deleted_at IS NULL
	INNER JOIN properties p ON p.id = u.property_id AND p.deleted_at IS NULL
	LEFT JOIN utility_meters m ON m.id = ub.meter_id
	LEFT JOIN utility_readings r ON r.id = ub.reading_id
	LEFT JOIN invoices iv ON iv.id = ub.invoice_id
	LEFT JOIN leases l ON l.unit_id = u.id AND l.status = '` + leaseActive + `'`

const billViewQuery = `SELECT ` + billColumns + billFrom

func (r *repository) ListBills(ctx context.Context, orgID uint64, scope rbac.Scope, q BillQuery, today time.Time) ([]BillViewRow, int, error) {
	where, args := billWhere(orgID, scope, q, today)
	var total int
	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*)`+billFrom+` WHERE `+where, args...); err != nil {
		return nil, 0, err
	}
	listArgs := append(append([]any{}, args...), q.Page.Limit, q.Page.Offset)
	var rows []BillViewRow
	err := r.db.SelectContext(ctx, &rows,
		billViewQuery+` WHERE `+where+` ORDER BY `+q.Sort+`, ub.id LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func billWhere(orgID uint64, scope rbac.Scope, q BillQuery, today time.Time) (string, []any) {
	where := []string{"p.organization_id = ?"}
	args := []any{orgID}
	clause, scopeArgs := scopeFilter(scope, "u.property_id", "ub.unit_id")
	where = append(where, clause)
	args = append(args, scopeArgs...)
	if q.Search != "" {
		like := likePattern(q.Search)
		where = append(where, `(u.number LIKE ? OR p.name LIKE ? OR m.meter_number LIKE ? OR ub.type LIKE ?)`)
		args = append(args, like, like, like, like)
	}
	if q.Status != "" {
		where = append(where, "ub.status = ?")
		args = append(args, q.Status)
	}
	if q.Type != "" {
		where = append(where, "ub.type = ?")
		args = append(args, q.Type)
	}
	if q.PropertyID != 0 {
		where = append(where, "u.property_id = ?")
		args = append(args, q.PropertyID)
	}
	if q.UnitID != 0 {
		where = append(where, "ub.unit_id = ?")
		args = append(args, q.UnitID)
	}
	if q.MeterID != 0 {
		where = append(where, "ub.meter_id = ?")
		args = append(args, q.MeterID)
	}
	if q.Overdue {
		where = append(where, `ub.date_due < ? AND ub.status IN ('`+billPending+`','`+billBilled+`')`)
		args = append(args, today)
	}
	if !q.DueFrom.IsZero() {
		where = append(where, "ub.date_due >= ?")
		args = append(args, q.DueFrom)
	}
	if !q.DueTo.IsZero() {
		where = append(where, "ub.date_due <= ?")
		args = append(args, q.DueTo)
	}
	return strings.Join(where, " AND "), args
}

func (r *repository) GetBill(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*BillViewRow, error) {
	clause, scopeArgs := scopeFilter(scope, "u.property_id", "ub.unit_id")
	args := append([]any{orgID, id}, scopeArgs...)
	var row BillViewRow
	err := r.db.GetContext(ctx, &row,
		billViewQuery+` WHERE p.organization_id = ? AND ub.id = ? AND `+clause, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// BillForUpdate locks the bill about to be changed. Writes are staff-only, so
// the property filter is the whole scope test.
func (r *repository) BillForUpdate(ctx context.Context, tx *sqlx.Tx, orgID, id uint64, scope rbac.Scope) (*BillRow, error) {
	clause, scopeArgs := scope.PropertyFilter("u.property_id")
	args := append([]any{orgID, id}, scopeArgs...)
	var row BillRow
	err := tx.GetContext(ctx, &row, `
		SELECT ub.id, ub.unit_id, ub.meter_id, ub.reading_id, ub.invoice_id, ub.type, ub.amount,
			ub.date_due, ub.period_start, ub.period_end, ub.status, ub.created_at, ub.updated_at
		FROM utility_bills ub
		INNER JOIN units u ON u.id = ub.unit_id AND u.deleted_at IS NULL
		INNER JOIN properties p ON p.id = u.property_id AND p.deleted_at IS NULL
		WHERE p.organization_id = ? AND ub.id = ? AND `+clause+`
		FOR UPDATE`, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) InsertBill(ctx context.Context, tx *sqlx.Tx, row *BillRow) (uint64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO utility_bills
			(unit_id, meter_id, reading_id, invoice_id, type, amount, date_due, period_start, period_end, status)
		VALUES (?, ?, ?, NULL, ?, ?, ?, ?, ?, ?)`,
		row.UnitID, nullableID(row.MeterID), nullableID(row.ReadingID), row.Type,
		row.Amount, row.DateDue, row.PeriodStart, row.PeriodEnd, row.Status)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

func (r *repository) UpdateBill(ctx context.Context, tx *sqlx.Tx, row *BillRow) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE utility_bills
		SET unit_id = ?, meter_id = ?, type = ?, amount = ?, date_due = ?,
		    period_start = ?, period_end = ?, status = ?
		WHERE id = ?`,
		row.UnitID, nullableID(row.MeterID), row.Type, row.Amount, row.DateDue,
		row.PeriodStart, row.PeriodEnd, row.Status, row.ID)
	return err
}

// SetBillStatus moves a bill between states with the state it is leaving and the
// organization that owns it in the WHERE clause, so a replay is a no-op and a
// bill outside the caller's organization is never touched.
func (r *repository) SetBillStatus(ctx context.Context, q querier, orgID, billID uint64, from, to string) (int64, error) {
	res, err := q.ExecContext(ctx, `
		UPDATE utility_bills ub
		INNER JOIN units u ON u.id = ub.unit_id
		INNER JOIN properties p ON p.id = u.property_id
		SET ub.status = ?
		WHERE ub.id = ? AND ub.status = ? AND p.organization_id = ?`, to, billID, from, orgID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// OpenReadingsForBilling is the generate path: unbilled readings on a meter that
// is attached to a unit, newest period last, with the lease that would be
// charged. A row with no lease is returned rather than filtered out so the
// caller can report it as skipped instead of silently dropping it.
func (r *repository) OpenReadingsForBilling(ctx context.Context, orgID uint64, scope rbac.Scope, f GenerateFilter, limit int) ([]OpenReadingRow, error) {
	where := []string{
		"p.organization_id = ?",
		"m.unit_id IS NOT NULL",
		"m.status = '" + meterActive + "'",
		"b.id IS NULL",
	}
	args := []any{orgID}
	clause, scopeArgs := scopeFilter(scope, "u.property_id", "m.unit_id")
	where = append(where, clause)
	args = append(args, scopeArgs...)
	if f.MeterID != 0 {
		where = append(where, "ur.meter_id = ?")
		args = append(args, f.MeterID)
	}
	if f.PropertyID != 0 {
		where = append(where, "u.property_id = ?")
		args = append(args, f.PropertyID)
	}
	if f.UnitID != 0 {
		where = append(where, "m.unit_id = ?")
		args = append(args, f.UnitID)
	}
	if !f.From.IsZero() {
		where = append(where, "ur.reading_date >= ?")
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		where = append(where, "ur.reading_date <= ?")
		args = append(args, f.To)
	}
	args = append(args, limit)
	var rows []OpenReadingRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT ur.id AS reading_id, ur.meter_id, m.unit_id, p.name AS property_name,
			u.number AS unit_number, m.utility_type, m.rate_per_unit, ur.consumption,
			ur.reading_date,
			(SELECT MAX(prev.reading_date) FROM utility_readings prev
				WHERE prev.meter_id = ur.meter_id AND prev.reading_date < ur.reading_date) AS period_start,
			l.id AS lease_id, l.due_day AS lease_due_day
		FROM utility_readings ur
		INNER JOIN utility_meters m ON m.id = ur.meter_id
		INNER JOIN units u ON u.id = m.unit_id AND u.deleted_at IS NULL
		INNER JOIN properties p ON p.id = u.property_id AND p.deleted_at IS NULL
		LEFT JOIN leases l ON l.unit_id = u.id AND l.status = '`+leaseActive+`'
		LEFT JOIN utility_bills b ON b.reading_id = ur.id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY ur.reading_date ASC, ur.id ASC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// CountMeterBills backs the delete guard on a meter. A bill keeps its own copy
// of the type and amount, but losing the meter link would break the trail from
// the charge back to the dial that produced it.
func (r *repository) CountMeterBills(ctx context.Context, q querier, meterID uint64) (int, error) {
	var count int
	err := q.GetContext(ctx, &count, `SELECT COUNT(*) FROM utility_bills WHERE meter_id = ?`, meterID)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (r *repository) CountMeters(ctx context.Context, orgID uint64, scope rbac.Scope, propertyID uint64) (int, error) {
	q := MeterQuery{PropertyID: propertyID}
	where, args := meterWhere(orgID, scope, q)
	var count int
	if err := r.db.GetContext(ctx, &count, `SELECT COUNT(*)`+meterFrom+` WHERE `+where, args...); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *repository) CountOpenReadings(ctx context.Context, orgID uint64, scope rbac.Scope, propertyID uint64) (int, error) {
	where := []string{"p.organization_id = ?", "b.id IS NULL"}
	args := []any{orgID}
	clause, scopeArgs := scopeFilter(scope, "m.property_id", "m.unit_id")
	where = append(where, clause)
	args = append(args, scopeArgs...)
	if propertyID != 0 {
		where = append(where, "m.property_id = ?")
		args = append(args, propertyID)
	}
	var count int
	err := r.db.GetContext(ctx, &count, `
		SELECT COUNT(*)
		FROM utility_readings ur
		INNER JOIN utility_meters m ON m.id = ur.meter_id
		INNER JOIN properties p ON p.id = m.property_id AND p.deleted_at IS NULL
		LEFT JOIN units mu ON mu.id = m.unit_id
		LEFT JOIN utility_bills b ON b.reading_id = ur.id
		WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (r *repository) BillTotals(ctx context.Context, orgID uint64, scope rbac.Scope, propertyID uint64) (BillTotals, error) {
	where := []string{"p.organization_id = ?"}
	args := []any{orgID}
	clause, scopeArgs := scopeFilter(scope, "u.property_id", "ub.unit_id")
	where = append(where, clause)
	args = append(args, scopeArgs...)
	if propertyID != 0 {
		where = append(where, "u.property_id = ?")
		args = append(args, propertyID)
	}
	var row BillTotals
	err := r.db.GetContext(ctx, &row, `
		SELECT
			COALESCE(SUM(ub.status = '`+billPending+`'), 0) AS pending_count,
			COALESCE(SUM(CASE WHEN ub.status = '`+billPending+`' THEN ub.amount ELSE 0 END), 0) AS pending_total,
			COALESCE(SUM(CASE WHEN ub.status IN ('`+billBilled+`','`+billPaid+`') THEN ub.amount ELSE 0 END), 0) AS billed_total
		FROM utility_bills ub
		INNER JOIN units u ON u.id = ub.unit_id AND u.deleted_at IS NULL
		INNER JOIN properties p ON p.id = u.property_id AND p.deleted_at IS NULL
		WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return BillTotals{}, err
	}
	return row, nil
}
