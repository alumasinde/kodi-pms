package utilities

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/audit"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

// Readings is one meter's history, oldest correction last. A meter the caller
// cannot see is a 404 here rather than an empty list, so the endpoint does not
// confirm that a neighbouring property has a meter with that id.
func (s *Service) Readings(ctx context.Context, p *rbac.Principal, meterID uint64, q ReadingQuery) (*ReadingListDTO, error) {
	if err := authorize(p, "utilities.view"); err != nil {
		return nil, err
	}
	if meterID == 0 {
		return nil, httpx.ErrNotFound
	}
	if q.Sort == "" {
		q.Sort = defaultReadingSort
	}
	if _, err := s.repo.GetMeter(ctx, p.OrgID, meterID, p.Scope); err != nil {
		return nil, notFound(err)
	}
	rows, total, err := s.repo.ListReadings(ctx, p.OrgID, meterID, p.Scope, q)
	if err != nil {
		return nil, err
	}
	return &ReadingListDTO{Readings: readingDTOs(rows), Total: total}, nil
}

func (s *Service) Reading(ctx context.Context, p *rbac.Principal, id uint64) (*ReadingDTO, error) {
	if err := authorize(p, "utilities.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	row, err := s.repo.GetReading(ctx, p.OrgID, id, p.Scope)
	if err != nil {
		return nil, notFound(err)
	}
	out := readingDTO(row)
	return &out, nil
}

// CreateReading records what the dial said. The consumption the bill is later
// priced from is the difference between this reading and the one before it, so
// the previous figure is taken from the meter rather than typed, and a reading
// that goes backwards is refused instead of being stored as a negative quantity.
func (s *Service) CreateReading(ctx context.Context, p *rbac.Principal, meterID uint64, in CreateReadingInput, meta ClientMeta) (*ReadingDTO, error) {
	if err := authorize(p, "utilities.record_reading"); err != nil {
		return nil, err
	}
	if meterID == 0 {
		return nil, httpx.ErrNotFound
	}
	fields := map[string]string{}
	date := parseRequiredDate("reading_date", in.ReadingDate, fields)
	current := readingValue("current_reading", in.CurrentReading, fields)
	notes := readingNotes(in.Notes, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	meter, err := s.repo.MeterForUpdate(ctx, tx, p.OrgID, meterID, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if onOrAfterFuture(date, s.today(ctx, p.OrgID)) {
		return nil, httpx.Validation(map[string]string{"reading_date": "cannot be in the future"})
	}
	taken, err := s.repo.ReadingDateTaken(ctx, tx, meterID, date, 0)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, httpx.Conflict("This meter already has a reading on that date")
	}

	previous := 0.0
	if in.PreviousReading != nil {
		previous = readingValue("previous_reading", *in.PreviousReading, fields)
		if len(fields) > 0 {
			return nil, httpx.Validation(fields)
		}
	} else if last, err := s.repo.LatestReadingBefore(ctx, tx, meterID, date); err != nil {
		return nil, err
	} else if last != nil {
		previous = last.CurrentReading
	}
	if previous > current {
		return nil, httpx.Validation(map[string]string{
			"current_reading": "cannot be lower than previous_reading",
		})
	}

	row := &ReadingRow{
		MeterID:         meterID,
		ReadingDate:     date,
		PreviousReading: roundToThree(previous),
		CurrentReading:  current,
		Consumption:     roundToThree(current - previous),
		RecordedBy:      sql.NullInt64{Int64: int64(p.UserID), Valid: p.UserID != 0},
		Notes:           notes,
	}
	id, err := s.repo.InsertReading(ctx, tx, row)
	if err != nil {
		if isDuplicateEntry(err) {
			return nil, httpx.Conflict("This meter already has a reading on that date")
		}
		if rejection := s.reject(err, "create reading", "The reading breaks a database rule"); rejection != nil {
			return nil, rejection
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "utility_readings.created", EntityType: "utility_readings", EntityID: id,
		NewValues: map[string]any{
			"meter_id":         meterID,
			"meter_number":     meter.MeterNumber,
			"reading_date":     date.Format(dateLayout),
			"previous_reading": row.PreviousReading,
			"current_reading":  row.CurrentReading,
			"consumption":      row.Consumption,
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.readingDetail(ctx, p, id)
}

// UpdateReading corrects a dial reading. Because the next reading carries the
// figure being corrected as its own starting point, the chain moves with it, and
// a correction is refused as soon as either side of it has been billed.
func (s *Service) UpdateReading(ctx context.Context, p *rbac.Principal, id uint64, in UpdateReadingInput, meta ClientMeta) (*ReadingDTO, error) {
	if err := authorize(p, "utilities.record_reading"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := s.repo.ReadingForUpdate(ctx, tx, p.OrgID, id, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.repo.MeterForUpdate(ctx, tx, p.OrgID, row.MeterID, p.Scope); err != nil {
		return nil, notFound(err)
	}

	fields := map[string]string{}
	oldValues := map[string]any{}
	newValues := map[string]any{}

	current := row.CurrentReading
	if in.CurrentReading != nil {
		current = readingValue("current_reading", *in.CurrentReading, fields)
	}
	notes := row.Notes
	if in.Notes != nil {
		notes = readingNotes(*in.Notes, fields)
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}
	if current < row.PreviousReading {
		return nil, httpx.Validation(map[string]string{
			"current_reading": "cannot be lower than previous_reading",
		})
	}
	if err := s.readingCorrectionAllowed(ctx, tx, row); err != nil {
		return nil, err
	}

	next, err := s.repo.NextReading(ctx, tx, row.MeterID, row.ReadingDate)
	if err != nil {
		return nil, err
	}
	if next != nil {
		if billID, status, found, err := s.repo.BillOnReading(ctx, tx, next.ID); err != nil {
			return nil, err
		} else if err := s.readingLocked(billID, status, found); err != nil {
			return nil, err
		}
		if current > next.CurrentReading {
			return nil, httpx.Validation(map[string]string{
				"current_reading": fmt.Sprintf("cannot be higher than the next reading on %.3f", next.CurrentReading),
			})
		}
	}

	changedFigure := current != row.CurrentReading
	changedNotes := notes != row.Notes
	if changedFigure {
		oldValues["current_reading"] = row.CurrentReading
		newValues["current_reading"] = current
		oldValues["consumption"] = row.Consumption
		newValues["consumption"] = roundToThree(current - row.PreviousReading)
	}
	if changedNotes {
		oldValues["notes"] = nullText(row.Notes)
		newValues["notes"] = nullText(notes)
	}
	if !changedFigure && !changedNotes {
		return s.readingDetail(ctx, p, id)
	}

	row.CurrentReading = current
	row.Notes = notes
	if err := s.repo.UpdateReading(ctx, tx, row); err != nil {
		if rejection := s.reject(err, "update reading", "The reading breaks a database rule"); rejection != nil {
			return nil, rejection
		}
		return nil, err
	}
	if changedFigure && next != nil {
		if err := s.repo.SetReadingPrevious(ctx, tx, next.ID, current); err != nil {
			return nil, err
		}
		newValues["next_reading_id"] = next.ID
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "utility_readings.updated", EntityType: "utility_readings", EntityID: id,
		OldValues: oldValues, NewValues: newValues,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.readingDetail(ctx, p, id)
}

// DeleteReading removes a reading that has not been billed and closes the gap it
// left: the reading after it takes over the deleted row's starting figure, so
// the meter's history stays continuous and no consumption is lost or doubled.
func (s *Service) DeleteReading(ctx context.Context, p *rbac.Principal, id uint64, meta ClientMeta) error {
	if err := authorize(p, "utilities.record_reading"); err != nil {
		return err
	}
	if id == 0 {
		return httpx.ErrNotFound
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := s.repo.ReadingForUpdate(ctx, tx, p.OrgID, id, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	meter, err := s.repo.MeterForUpdate(ctx, tx, p.OrgID, row.MeterID, p.Scope)
	if err != nil {
		return notFound(err)
	}
	if err := s.readingCorrectionAllowed(ctx, tx, row); err != nil {
		return err
	}
	next, err := s.repo.NextReading(ctx, tx, row.MeterID, row.ReadingDate)
	if err != nil {
		return err
	}
	if next != nil {
		if billID, status, found, err := s.repo.BillOnReading(ctx, tx, next.ID); err != nil {
			return err
		} else if err := s.readingLocked(billID, status, found); err != nil {
			return err
		}
	}

	oldValues := map[string]any{
		"meter_id":         row.MeterID,
		"meter_number":     meter.MeterNumber,
		"reading_date":     row.ReadingDate.Format(dateLayout),
		"previous_reading": row.PreviousReading,
		"current_reading":  row.CurrentReading,
		"consumption":      row.Consumption,
	}
	if err := s.repo.DeleteReading(ctx, tx, id); err != nil {
		return err
	}
	if next != nil {
		if err := s.repo.SetReadingPrevious(ctx, tx, next.ID, row.PreviousReading); err != nil {
			return err
		}
		oldValues["next_reading_id"] = next.ID
		oldValues["next_previous_reading"] = next.PreviousReading
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "utility_readings.deleted", EntityType: "utility_readings", EntityID: id,
		OldValues: oldValues,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return nil
}

// readingCorrectionAllowed is the lock that keeps a bill truthful: once a
// reading has produced a charge, the quantity that charge was priced from is
// fixed, and so is the quantity the following reading started from.
func (s *Service) readingCorrectionAllowed(ctx context.Context, tx *sqlx.Tx, row *ReadingRow) error {
	billID, status, found, err := s.repo.BillOnReading(ctx, tx, row.ID)
	if err != nil {
		return err
	}
	if err := s.readingLocked(billID, status, found); err != nil {
		return err
	}
	return nil
}

func (s *Service) readingDetail(ctx context.Context, p *rbac.Principal, id uint64) (*ReadingDTO, error) {
	row, err := s.repo.GetReading(ctx, p.OrgID, id, p.Scope)
	if err != nil {
		return nil, notFound(err)
	}
	out := readingDTO(row)
	return &out, nil
}

// onOrAfterFuture answers "is this date beyond today" on whole calendar days:
// a reading taken at 20:00 this evening is still today's reading, so the
// boundary is the first instant of tomorrow, not this morning.
func onOrAfterFuture(date, today time.Time) bool {
	return !date.Before(truncateDate(today).AddDate(0, 0, 1))
}

func readingValue(name string, value float64, fields map[string]string) float64 {
	rounded := roundToThree(value)
	switch {
	case rounded < 0:
		fields[name] = "cannot be negative"
	case rounded > maxReadingValue:
		fields[name] = fmt.Sprintf("must be at most %.3f", maxReadingValue)
		return 0
	}
	return rounded
}

func readingNotes(value string, fields map[string]string) sql.NullString {
	value = strings.TrimSpace(value)
	if value == "" {
		return sql.NullString{}
	}
	if utf8.RuneCountInString(value) > maxNotes {
		fields["notes"] = fmt.Sprintf("must be at most %d characters", maxNotes)
		return sql.NullString{}
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		fields["notes"] = "cannot contain control characters"
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func readingDTOs(rows []ReadingViewRow) []ReadingDTO {
	out := make([]ReadingDTO, 0, len(rows))
	for i := range rows {
		out = append(out, readingDTO(&rows[i]))
	}
	return out
}

func readingDTO(row *ReadingViewRow) ReadingDTO {
	return ReadingDTO{
		ID:              row.ID,
		ReadingDate:     row.ReadingDate.UTC(),
		PreviousReading: row.PreviousReading,
		CurrentReading:  row.CurrentReading,
		Consumption:     row.Consumption,
		UnitOfMeasure:   row.UnitOfMeasure,
		RatePerUnit:     row.RatePerUnit,
		RecordedBy:      nullIDPtr(row.RecordedBy),
		RecordedName:    nullText(row.RecordedName),
		Notes:           nullText(row.Notes),
		BillID:          nullIDPtr(row.BillID),
		BillStatus:      nullText(row.BillStatus),
		Adjustable:      !row.BillID.Valid && !row.NextBillStatus.Valid,
		CreatedAt:       row.CreatedAt.UTC(),
	}
}
