package utilities

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/rbac"
)

const readingColumns = `
	ur.id, ur.meter_id, ur.reading_date, ur.previous_reading, ur.current_reading,
	ur.consumption, ur.recorded_by, ur.notes, ur.created_at, ur.updated_at,
	m.property_id, p.name AS property_name, m.unit_id, mu.number AS unit_number,
	m.meter_number, m.utility_type, m.unit_of_measure, m.rate_per_unit,
	CONCAT_WS(' ', recorder.first_name, recorder.last_name) AS recorded_name,
	b.id AS bill_id, b.status AS bill_status,
	(SELECT nb.status FROM utility_readings nx
		INNER JOIN utility_bills nb ON nb.reading_id = nx.id
		WHERE nx.meter_id = ur.meter_id AND nx.reading_date > ur.reading_date
		ORDER BY nx.reading_date ASC, nx.id ASC LIMIT 1) AS next_bill_status`

const readingFrom = `
	FROM utility_readings ur
	INNER JOIN utility_meters m ON m.id = ur.meter_id
	INNER JOIN properties p ON p.id = m.property_id AND p.deleted_at IS NULL
	LEFT JOIN units mu ON mu.id = m.unit_id
	LEFT JOIN users recorder ON recorder.id = ur.recorded_by
	LEFT JOIN utility_bills b ON b.reading_id = ur.id`

const readingViewQuery = `SELECT ` + readingColumns + readingFrom

// ListReadings walks one meter's history. The meter is already scoped by the
// caller, so a reading for a meter they cannot see is a 404 rather than a leak.
func (r *repository) ListReadings(ctx context.Context, orgID, meterID uint64, scope rbac.Scope, q ReadingQuery) ([]ReadingViewRow, int, error) {
	where := []string{"p.organization_id = ?", "ur.meter_id = ?"}
	args := []any{orgID, meterID}
	clause, scopeArgs := scopeFilter(scope, "m.property_id", "m.unit_id")
	where = append(where, clause)
	args = append(args, scopeArgs...)
	if !q.From.IsZero() {
		where = append(where, "ur.reading_date >= ?")
		args = append(args, q.From)
	}
	if !q.To.IsZero() {
		where = append(where, "ur.reading_date <= ?")
		args = append(args, q.To)
	}
	join := strings.Join(where, " AND ")

	var total int
	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*)`+readingFrom+` WHERE `+join, args...); err != nil {
		return nil, 0, err
	}
	listArgs := append(append([]any{}, args...), q.Page.Limit, q.Page.Offset)
	var rows []ReadingViewRow
	err := r.db.SelectContext(ctx, &rows,
		readingViewQuery+` WHERE `+join+` ORDER BY `+q.Sort+`, ur.id LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *repository) GetReading(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*ReadingViewRow, error) {
	clause, scopeArgs := scopeFilter(scope, "m.property_id", "m.unit_id")
	args := append([]any{orgID, id}, scopeArgs...)
	var row ReadingViewRow
	err := r.db.GetContext(ctx, &row,
		readingViewQuery+` WHERE p.organization_id = ? AND ur.id = ? AND `+clause, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ReadingForUpdate locks the reading about to be corrected. The meter's property
// carries the organization, and writes are staff-only.
func (r *repository) ReadingForUpdate(ctx context.Context, tx *sqlx.Tx, orgID, id uint64, scope rbac.Scope) (*ReadingRow, error) {
	clause, scopeArgs := scope.PropertyFilter("m.property_id")
	args := append([]any{orgID, id}, scopeArgs...)
	var row ReadingRow
	err := tx.GetContext(ctx, &row, `
		SELECT ur.id, ur.meter_id, ur.reading_date, ur.previous_reading, ur.current_reading,
			ur.consumption, ur.recorded_by, ur.notes, ur.created_at, ur.updated_at
		FROM utility_readings ur
		INNER JOIN utility_meters m ON m.id = ur.meter_id
		INNER JOIN properties p ON p.id = m.property_id AND p.deleted_at IS NULL
		WHERE p.organization_id = ? AND ur.id = ? AND `+clause+`
		FOR UPDATE`, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// LatestReadingBefore returns the reading whose current value opens the next
// period, which is what a new reading defaults its previous_reading from.
func (r *repository) LatestReadingBefore(ctx context.Context, q querier, meterID uint64, onOrBefore time.Time) (*ReadingRow, error) {
	var row ReadingRow
	err := q.GetContext(ctx, &row, `
		SELECT id, meter_id, reading_date, previous_reading, current_reading, consumption,
			recorded_by, notes, created_at, updated_at
		FROM utility_readings
		WHERE meter_id = ? AND reading_date <= ?
		ORDER BY reading_date DESC, id DESC LIMIT 1`, meterID, onOrBefore)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// NextReading returns the reading that follows this one on the same meter. It is
// the row whose previous_reading has to move with any correction, and the second
// row a correction can invalidate.
func (r *repository) NextReading(ctx context.Context, q querier, meterID uint64, after time.Time) (*ReadingRow, error) {
	var row ReadingRow
	err := q.GetContext(ctx, &row, `
		SELECT id, meter_id, reading_date, previous_reading, current_reading, consumption,
			recorded_by, notes, created_at, updated_at
		FROM utility_readings
		WHERE meter_id = ? AND reading_date > ?
		ORDER BY reading_date ASC, id ASC LIMIT 1`, meterID, after)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ReadingSnap loads one reading with its meter for the bill path, which has
// already established that the caller manages the property the unit sits on. It
// locks nothing: a bill records the quantity it was priced from, so a later
// correction to the reading is reported rather than silently re-pricing a charge.
func (r *repository) ReadingSnap(ctx context.Context, q querier, orgID, readingID uint64) (*ReadingSnapRow, error) {
	var row ReadingSnapRow
	err := q.GetContext(ctx, &row, `
		SELECT ur.id, ur.meter_id, m.property_id, m.unit_id, m.status AS meter_status,
			m.utility_type, m.rate_per_unit, ur.reading_date, ur.previous_reading,
			ur.current_reading, ur.consumption, b.id AS bill_id,
			(SELECT MAX(prev.reading_date) FROM utility_readings prev
				WHERE prev.meter_id = ur.meter_id AND prev.reading_date < ur.reading_date) AS previous_reading_date
		FROM utility_readings ur
		INNER JOIN utility_meters m ON m.id = ur.meter_id
		INNER JOIN properties p ON p.id = m.property_id
		LEFT JOIN utility_bills b ON b.reading_id = ur.id
		WHERE p.organization_id = ? AND ur.id = ? AND p.deleted_at IS NULL`, orgID, readingID)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) ReadingDateTaken(ctx context.Context, q querier, meterID uint64, on time.Time, excludeID uint64) (bool, error) {
	query := `SELECT COUNT(*) FROM utility_readings WHERE meter_id = ? AND reading_date = ?`
	args := []any{meterID, on}
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

// BillOnReading finds the bill raised from a reading, if any. One reading can
// only ever carry one bill, which is what makes this a single row rather than a
// list, and why any bill at all locks the reading against correction.
func (r *repository) BillOnReading(ctx context.Context, q querier, readingID uint64) (uint64, string, bool, error) {
	var row readingBillRow
	err := q.GetContext(ctx, &row, `
		SELECT b.id AS bill_id, b.status
		FROM utility_bills b
		WHERE b.reading_id = ? LIMIT 1`, readingID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	if !row.BillID.Valid {
		return 0, "", false, nil
	}
	return uint64(row.BillID.Int64), row.Status.String, true, nil
}

type readingBillRow struct {
	BillID sql.NullInt64  `db:"bill_id"`
	Status sql.NullString `db:"status"`
}

func (r *repository) InsertReading(ctx context.Context, tx *sqlx.Tx, row *ReadingRow) (uint64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO utility_readings
			(meter_id, reading_date, previous_reading, current_reading, recorded_by, notes)
		VALUES (?, ?, ?, ?, ?, ?)`,
		row.MeterID, row.ReadingDate, row.PreviousReading, row.CurrentReading,
		nullableID(row.RecordedBy), nullText(row.Notes))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

// UpdateReading writes the corrected figure. The reading date never moves: the
// dial was read on the day it was read, and moving it would re-order the chain.
func (r *repository) UpdateReading(ctx context.Context, tx *sqlx.Tx, row *ReadingRow) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE utility_readings
		SET current_reading = ?, notes = ?
		WHERE id = ?`,
		row.CurrentReading, nullText(row.Notes), row.ID)
	return err
}

func (r *repository) SetReadingPrevious(ctx context.Context, tx *sqlx.Tx, readingID uint64, previous float64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE utility_readings SET previous_reading = ? WHERE id = ?`, previous, readingID)
	return err
}

func (r *repository) DeleteReading(ctx context.Context, tx *sqlx.Tx, id uint64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM utility_readings WHERE id = ?`, id)
	return err
}
