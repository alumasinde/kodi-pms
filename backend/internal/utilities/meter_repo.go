package utilities

import (
	"context"
	"strings"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/rbac"
)

const meterColumns = `
	m.id, m.property_id, m.unit_id, m.utility_type, m.meter_number, m.unit_of_measure,
	m.rate_per_unit, m.status, m.created_at, m.updated_at,
	p.name AS property_name, p.code AS property_code, mu.number AS unit_number,
	(SELECT MAX(r.reading_date) FROM utility_readings r WHERE r.meter_id = m.id) AS last_reading_date,
	(SELECT r.current_reading FROM utility_readings r WHERE r.meter_id = m.id
		ORDER BY r.reading_date DESC, r.id DESC LIMIT 1) AS last_reading_value,
	(SELECT COUNT(*) FROM utility_readings r WHERE r.meter_id = m.id
		AND NOT EXISTS (SELECT 1 FROM utility_bills b WHERE b.reading_id = r.id)) AS open_readings`

const meterFrom = `
	FROM utility_meters m
	INNER JOIN properties p ON p.id = m.property_id AND p.deleted_at IS NULL
	LEFT JOIN units mu ON mu.id = m.unit_id`

const meterViewQuery = `SELECT ` + meterColumns + meterFrom

func (r *repository) ListMeters(ctx context.Context, orgID uint64, scope rbac.Scope, q MeterQuery) ([]MeterViewRow, int, error) {
	where, args := meterWhere(orgID, scope, q)
	var total int
	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*)`+meterFrom+` WHERE `+where, args...); err != nil {
		return nil, 0, err
	}
	listArgs := append(append([]any{}, args...), q.Page.Limit, q.Page.Offset)
	var rows []MeterViewRow
	err := r.db.SelectContext(ctx, &rows,
		meterViewQuery+` WHERE `+where+` ORDER BY `+q.Sort+`, m.id LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func meterWhere(orgID uint64, scope rbac.Scope, q MeterQuery) (string, []any) {
	where := []string{"p.organization_id = ?"}
	args := []any{orgID}
	clause, scopeArgs := scopeFilter(scope, "m.property_id", "m.unit_id")
	where = append(where, clause)
	args = append(args, scopeArgs...)
	if q.Search != "" {
		like := likePattern(q.Search)
		where = append(where, `(m.meter_number LIKE ? OR p.name LIKE ? OR mu.number LIKE ?)`)
		args = append(args, like, like, like)
	}
	if q.UtilityType != "" {
		where = append(where, "m.utility_type = ?")
		args = append(args, q.UtilityType)
	}
	if q.Status != "" {
		where = append(where, "m.status = ?")
		args = append(args, q.Status)
	}
	if q.PropertyID != 0 {
		where = append(where, "m.property_id = ?")
		args = append(args, q.PropertyID)
	}
	if q.UnitID != 0 {
		where = append(where, "m.unit_id = ?")
		args = append(args, q.UnitID)
	}
	if q.OpenOnly {
		where = append(where, `EXISTS (SELECT 1 FROM utility_readings o
			WHERE o.meter_id = m.id
			AND NOT EXISTS (SELECT 1 FROM utility_bills ob WHERE ob.reading_id = o.id))`)
	}
	return strings.Join(where, " AND "), args
}

func (r *repository) GetMeter(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*MeterViewRow, error) {
	clause, scopeArgs := scopeFilter(scope, "m.property_id", "m.unit_id")
	args := append([]any{orgID, id}, scopeArgs...)
	var row MeterViewRow
	err := r.db.GetContext(ctx, &row,
		meterViewQuery+` WHERE p.organization_id = ? AND m.id = ? AND `+clause, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// MeterForUpdate locks the meter a write is about to change. Writes are
// staff-only, so the property filter is the whole scope test.
func (r *repository) MeterForUpdate(ctx context.Context, tx *sqlx.Tx, orgID, id uint64, scope rbac.Scope) (*MeterRow, error) {
	clause, scopeArgs := scope.PropertyFilter("m.property_id")
	args := append([]any{id, orgID}, scopeArgs...)
	var row MeterRow
	err := tx.GetContext(ctx, &row, `
		SELECT m.id, m.property_id, m.unit_id, m.utility_type, m.meter_number,
			m.unit_of_measure, m.rate_per_unit, m.status, m.created_at, m.updated_at
		FROM utility_meters m
		INNER JOIN properties p ON p.id = m.property_id AND p.deleted_at IS NULL
		WHERE m.id = ? AND p.organization_id = ? AND `+clause+`
		FOR UPDATE`, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) MeterNumberTaken(ctx context.Context, q querier, propertyID uint64, number string, excludeID uint64) (bool, error) {
	query := `SELECT COUNT(*) FROM utility_meters WHERE property_id = ? AND meter_number = ?`
	args := []any{propertyID, number}
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

// CountReadings is the delete guard on a meter: dropping one cascades to its
// readings and orphans the bills raised from them, so the readings have to be
// removed deliberately, one correction at a time.
func (r *repository) CountReadings(ctx context.Context, q querier, meterID uint64) (int, error) {
	var count int
	err := q.GetContext(ctx, &count, `SELECT COUNT(*) FROM utility_readings WHERE meter_id = ?`, meterID)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (r *repository) InsertMeter(ctx context.Context, tx *sqlx.Tx, row *MeterRow) (uint64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO utility_meters
			(property_id, unit_id, utility_type, meter_number, unit_of_measure, rate_per_unit, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		row.PropertyID, nullableID(row.UnitID), row.UtilityType, row.MeterNumber,
		row.UnitOfMeasure, row.RatePerUnit, row.Status)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

func (r *repository) UpdateMeter(ctx context.Context, tx *sqlx.Tx, row *MeterRow) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE utility_meters
		SET unit_id = ?, utility_type = ?, meter_number = ?, unit_of_measure = ?,
		    rate_per_unit = ?, status = ?
		WHERE id = ?`,
		nullableID(row.UnitID), row.UtilityType, row.MeterNumber, row.UnitOfMeasure,
		row.RatePerUnit, row.Status, row.ID)
	return err
}

func (r *repository) DeleteMeter(ctx context.Context, tx *sqlx.Tx, id uint64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM utility_meters WHERE id = ?`, id)
	return err
}

// MeterSnapshot is the meter as the bill path needs it: enough to price a
// charge and to tell whether the meter is attached to the unit being billed.
func (r *repository) MeterSnapshot(ctx context.Context, q querier, meterID uint64) (*MeterSnapRow, error) {
	var row MeterSnapRow
	err := q.GetContext(ctx, &row, `
		SELECT m.id, m.property_id, m.meter_number, m.utility_type, m.unit_of_measure,
			m.rate_per_unit, m.unit_id, m.status
		FROM utility_meters m
		INNER JOIN properties p ON p.id = m.property_id
		WHERE m.id = ? AND p.deleted_at IS NULL`, meterID)
	if err != nil {
		return nil, err
	}
	return &row, nil
}
