package utilities

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/audit"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

// Bills lists the charges raised from meters. No money is created here: a bill
// becomes receivable only when billing carries it onto an invoice, which is why
// the list can still show a pending bill for a unit whose lease has ended.
func (s *Service) Bills(ctx context.Context, p *rbac.Principal, q BillQuery) (*BillListDTO, error) {
	if err := authorize(p, "utilities.view"); err != nil {
		return nil, err
	}
	if err := normalizeBillQuery(&q); err != nil {
		return nil, err
	}
	today := s.today(ctx, p.OrgID)
	rows, total, err := s.repo.ListBills(ctx, p.OrgID, p.Scope, q, today)
	if err != nil {
		return nil, err
	}
	return &BillListDTO{Bills: billSummaryDTOs(rows, today), Total: total}, nil
}

func (s *Service) Bill(ctx context.Context, p *rbac.Principal, id uint64) (*BillDetailDTO, error) {
	if err := authorize(p, "utilities.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	return s.billDetail(ctx, p, id)
}

// CreateBill raises a charge by hand: the utility that is not metered in this
// property, or the collection billed from a paper invoice. When a reading is
// supplied, whatever the caller leaves out is taken from it, so billing a dial
// is one field rather than six.
func (s *Service) CreateBill(ctx context.Context, p *rbac.Principal, in CreateBillInput, meta ClientMeta) (*BillDetailDTO, error) {
	if err := authorize(p, "utilities.create"); err != nil {
		return nil, err
	}
	fields := map[string]string{}
	row := buildBillCreate(in, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	unit, err := s.unitForBilling(ctx, tx, p, row.UnitID, fields)
	if err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	if row.ReadingID.Valid {
		if err := s.billFromReadingSource(ctx, tx, p.OrgID, row, unit, fields); err != nil {
			return nil, err
		}
		if len(fields) > 0 {
			return nil, httpx.Validation(fields)
		}
	}
	if err := s.checkMeterOnBill(ctx, tx, row, unit.PropertyID, fields); err != nil {
		return nil, err
	}
	finalizeBillRow(row, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	id, err := s.repo.InsertBill(ctx, tx, row)
	if err != nil {
		if isDuplicateEntry(err) {
			return nil, httpx.Conflict("That reading has already been billed")
		}
		if rejection := s.reject(err, "create bill", "The bill breaks a database rule"); rejection != nil {
			return nil, rejection
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "utility_bills.created", EntityType: "utility_bills", EntityID: id,
		NewValues: map[string]any{
			"unit_id":      row.UnitID,
			"unit_number":  unit.Number,
			"property_id":  unit.PropertyID,
			"meter_id":     nullableID(row.MeterID),
			"reading_id":   nullableID(row.ReadingID),
			"type":         row.Type,
			"amount":       row.Amount,
			"date_due":     row.DateDue.Format(dateLayout),
			"period_start": row.PeriodStart.Format(dateLayout),
			"period_end":   row.PeriodEnd.Format(dateLayout),
			"status":       row.Status,
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.billDetail(ctx, p, id)
}

// UpdateBill edits a charge that billing has not taken yet. Once a bill is on an
// invoice the invoice is the record of what the tenant owes, so correcting the
// bill underneath it would leave the ledger and the bill disagreeing.
func (s *Service) UpdateBill(ctx context.Context, p *rbac.Principal, id uint64, in UpdateBillInput, meta ClientMeta) (*BillDetailDTO, error) {
	if err := authorize(p, "utilities.update"); err != nil {
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

	row, err := s.repo.BillForUpdate(ctx, tx, p.OrgID, id, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.Status != billPending {
		return nil, httpx.Conflict(notPendingBill)
	}

	fields := map[string]string{}
	oldValues, newValues := applyBillPatch(row, in, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}
	if len(newValues) == 0 {
		return s.billDetail(ctx, p, id)
	}

	unit, err := s.unitForBilling(ctx, tx, p, row.UnitID, fields)
	if err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}
	if row.ReadingID.Valid {
		reading, err := s.repo.ReadingSnap(ctx, tx, p.OrgID, uint64(row.ReadingID.Int64))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, httpx.Validation(map[string]string{"reading_id": "no longer references a reading that exists"})
		}
		if err != nil {
			return nil, err
		}
		if reading.PropertyID != unit.PropertyID {
			return nil, httpx.Conflict("The reading this bill came from belongs to another property")
		}
	}
	if err := s.checkMeterOnBill(ctx, tx, row, unit.PropertyID, fields); err != nil {
		return nil, err
	}
	finalizeBillRow(row, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	if err := s.repo.UpdateBill(ctx, tx, row); err != nil {
		if rejection := s.reject(err, "update bill", "The bill breaks a database rule"); rejection != nil {
			return nil, rejection
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "utility_bills.updated", EntityType: "utility_bills", EntityID: id,
		OldValues: oldValues, NewValues: newValues,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.billDetail(ctx, p, id)
}

// VoidBill withdraws a pending charge. It never touches money: a bill that has
// already reached an invoice is corrected on the invoice, where the ledger can
// mirror the change.
func (s *Service) VoidBill(ctx context.Context, p *rbac.Principal, id uint64, reason string, meta ClientMeta) (*BillDetailDTO, error) {
	if err := authorize(p, "utilities.update"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > maxNotes {
		return nil, httpx.Validation(map[string]string{
			"reason": fmt.Sprintf("must be at most %d characters", maxNotes),
		})
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := s.repo.BillForUpdate(ctx, tx, p.OrgID, id, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.Status == billVoid {
		return nil, httpx.Conflict("This bill is already void")
	}
	if row.InvoiceID.Valid {
		return nil, httpx.Conflict(fmt.Sprintf(
			"Billing has carried this bill onto invoice %d; correct it on the invoice instead", row.InvoiceID.Int64))
	}
	affected, err := s.repo.SetBillStatus(ctx, tx, p.OrgID, id, billPending, billVoid)
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, httpx.Conflict(notPendingBill)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "utility_bills.voided", EntityType: "utility_bills", EntityID: id,
		OldValues: map[string]any{"status": billPending, "amount": row.Amount, "type": row.Type},
		NewValues: map[string]any{"status": billVoid, "reason": reason},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.billDetail(ctx, p, id)
}

// GenerateBills turns every unbilled reading into the charge its rate and
// consumption imply, for the units that currently have a tenant to owe it.
// Re-running it is harmless: a reading can only ever carry one bill, so the
// second pass finds nothing open.
func (s *Service) GenerateBills(ctx context.Context, p *rbac.Principal, in GenerateBillsInput, meta ClientMeta) (*GenerateResultDTO, error) {
	if err := authorize(p, "utilities.create"); err != nil {
		return nil, err
	}
	fields := map[string]string{}
	in.From = strings.TrimSpace(in.From)
	in.To = strings.TrimSpace(in.To)
	filter := generateFilter(in, fields)
	if in.MeterID != 0 && in.UnitID != 0 {
		fields["unit_id"] = "send either a meter or a unit, not both"
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	open, err := s.repo.OpenReadingsForBilling(ctx, p.OrgID, p.Scope, filter, maxGenerated)
	if err != nil {
		return nil, err
	}
	result := &GenerateResultDTO{Created: []uint64{}, Skipped: 0}
	if len(open) == 0 {
		return result, nil
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, candidate := range open {
		row, skip := billRowFromReading(candidate)
		if skip != "" {
			result.Skipped++
			continue
		}
		id, err := s.repo.InsertBill(ctx, tx, row)
		if err != nil {
			if isDuplicateEntry(err) {
				result.Skipped++
				continue
			}
			if rejection := s.reject(err, "generate bills", "A generated bill breaks a database rule"); rejection != nil {
				return nil, rejection
			}
			return nil, err
		}
		result.Created = append(result.Created, id)
	}
	if len(result.Created) == 0 {
		return result, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "utility_bills.generated", EntityType: "utility_bills", EntityID: result.Created[0],
		NewValues: map[string]any{
			"meter_id":    in.MeterID,
			"property_id": in.PropertyID,
			"unit_id":     in.UnitID,
			"from":        in.From,
			"to":          in.To,
			"created":     result.Created,
			"skipped":     result.Skipped,
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return result, nil
}

// billFromReadingSource fills a hand-raised bill from the reading it names and
// checks the reading actually belongs to the unit being charged. Anything the
// caller sent is left alone, so an override of the amount or the period stands.
func (s *Service) billFromReadingSource(ctx context.Context, tx *sqlx.Tx, orgID uint64, row *BillRow, unit *UnitRefRow, fields map[string]string) error {
	reading, err := s.repo.ReadingSnap(ctx, tx, orgID, uint64(row.ReadingID.Int64))
	if errors.Is(err, sql.ErrNoRows) {
		fields["reading_id"] = "must reference a reading that exists"
		return nil
	}
	if err != nil {
		return err
	}
	if reading.PropertyID != unit.PropertyID {
		fields["reading_id"] = "belongs to another property"
	}
	if reading.UnitID.Valid && uint64(reading.UnitID.Int64) != row.UnitID {
		fields["reading_id"] = "was taken on another unit"
	}
	if reading.BillID.Valid {
		return httpx.Conflict(fmt.Sprintf("That reading has already been billed as bill %d", reading.BillID.Int64))
	}
	if row.MeterID.Valid && uint64(row.MeterID.Int64) != reading.MeterID {
		fields["meter_id"] = "is not the meter this reading was taken from"
	} else if !row.MeterID.Valid {
		row.MeterID = sql.NullInt64{Int64: int64(reading.MeterID), Valid: true}
	}
	if row.Type == "" {
		row.Type = reading.UtilityType
	}
	if row.Amount == 0 {
		row.Amount = roundToCents(reading.Consumption * reading.RatePerUnit)
	}
	if row.PeriodEnd.IsZero() {
		row.PeriodEnd = reading.ReadingDate.UTC()
	}
	if row.PeriodStart.IsZero() {
		row.PeriodStart = periodStartFrom(reading)
	}
	if row.DateDue.IsZero() {
		row.DateDue = dueDateAfter(row.PeriodEnd, int(dueDayOf(unit)))
	}
	return nil
}

// checkMeterOnBill confirms the meter named on a bill is a meter of the property
// being charged and, when it is attached to a unit, of the unit being charged.
func (s *Service) checkMeterOnBill(ctx context.Context, tx *sqlx.Tx, row *BillRow, propertyID uint64, fields map[string]string) error {
	if !row.MeterID.Valid {
		return nil
	}
	meter, err := s.repo.MeterSnapshot(ctx, tx, uint64(row.MeterID.Int64))
	if errors.Is(err, sql.ErrNoRows) {
		fields["meter_id"] = "must reference a meter that exists"
		return nil
	}
	if err != nil {
		return err
	}
	if meter.PropertyID != propertyID {
		fields["meter_id"] = "belongs to another property"
	}
	if meter.UnitID.Valid && uint64(meter.UnitID.Int64) != row.UnitID {
		fields["meter_id"] = "is attached to another unit"
	}
	return nil
}

// unitForBilling resolves the unit a charge is raised against. An inactive unit
// is refused: nothing should accrue on a door that has been taken out of
// circulation.
func (s *Service) unitForBilling(ctx context.Context, tx *sqlx.Tx, p *rbac.Principal, unitID uint64, fields map[string]string) (*UnitRefRow, error) {
	if unitID == 0 {
		fields["unit_id"] = "required"
		return &UnitRefRow{}, nil
	}
	unit, err := s.repo.UnitInScope(ctx, tx, p.OrgID, unitID, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		fields["unit_id"] = "must be a unit you can manage"
		return &UnitRefRow{}, nil
	}
	if err != nil {
		return nil, err
	}
	if unit.Status == unitInactive {
		fields["unit_id"] = "is inactive and cannot be charged"
	}
	return unit, nil
}

func (s *Service) billDetail(ctx context.Context, p *rbac.Principal, id uint64) (*BillDetailDTO, error) {
	row, err := s.repo.GetBill(ctx, p.OrgID, id, p.Scope)
	if err != nil {
		return nil, notFound(err)
	}
	out := billDetailDTO(row, s.today(ctx, p.OrgID))
	return &out, nil
}

func normalizeBillQuery(q *BillQuery) error {
	fields := map[string]string{}
	q.Search = strings.TrimSpace(q.Search)
	if utf8.RuneCountInString(q.Search) > maxSearch {
		fields["q"] = fmt.Sprintf("must be at most %d characters", maxSearch)
	}
	if strings.ContainsAny(q.Search, "%_") {
		fields["q"] = "cannot contain wildcards"
	}
	q.Status = strings.ToLower(strings.TrimSpace(q.Status))
	if q.Status != "" && !inList(q.Status, billStatuses) {
		fields["status"] = billStatusMessage
	}
	q.Type = strings.ToLower(strings.TrimSpace(q.Type))
	if q.Type != "" && !inList(q.Type, utilityTypes) {
		fields["type"] = utilityTypeMessage
	}
	if !q.DueFrom.IsZero() && !q.DueTo.IsZero() && q.DueTo.Before(q.DueFrom) {
		fields["due_to"] = "cannot be earlier than due_from"
	}
	if q.Sort == "" {
		q.Sort = defaultBillSort
	}
	if len(fields) > 0 {
		return httpx.Validation(fields)
	}
	return nil
}

// generateFilter parses the date bounds of a generate request. The wire input
// keeps them as text so the audit entry records what the caller asked for, while
// the query only ever compares a date column to a date.
func generateFilter(in GenerateBillsInput, fields map[string]string) GenerateFilter {
	filter := GenerateFilter{MeterID: in.MeterID, PropertyID: in.PropertyID, UnitID: in.UnitID}
	filter.From = parseOptionalDateString("from", in.From, fields)
	filter.To = parseOptionalDateString("to", in.To, fields)
	if !filter.From.IsZero() && !filter.To.IsZero() && filter.To.Before(filter.From) {
		fields["to"] = "cannot be earlier than from"
	}
	return filter
}

func buildBillCreate(in CreateBillInput, fields map[string]string) *BillRow {
	row := &BillRow{UnitID: in.UnitID, Status: billPending}
	if in.MeterID != 0 {
		row.MeterID = sql.NullInt64{Int64: int64(in.MeterID), Valid: true}
	}
	if in.ReadingID != 0 {
		row.ReadingID = sql.NullInt64{Int64: int64(in.ReadingID), Valid: true}
	}
	if raw := strings.TrimSpace(in.Type); raw != "" {
		value := strings.ToLower(raw)
		if inList(value, utilityTypes) {
			row.Type = value
		} else {
			fields["type"] = utilityTypeMessage
		}
	}
	row.DateDue = parseOptionalDateString("date_due", in.DateDue, fields)
	row.PeriodStart = parseOptionalDateString("period_start", in.PeriodStart, fields)
	row.PeriodEnd = parseOptionalDateString("period_end", in.PeriodEnd, fields)
	if in.Amount != nil {
		row.Amount = roundToCents(*in.Amount)
	}
	return row
}

// finalizeBillRow checks the row once every default has been applied, so a bill
// raised from a reading can be created without restating what the reading says.
func finalizeBillRow(row *BillRow, fields map[string]string) {
	if row.UnitID == 0 {
		fields["unit_id"] = "required"
	}
	if row.Type == "" {
		fields["type"] = "required"
	}
	if row.Amount <= 0 {
		fields["amount"] = "must be more than zero"
	} else if row.Amount > maxAmount {
		fields["amount"] = fmt.Sprintf("must be at most %.2f", maxAmount)
	}
	if row.DateDue.IsZero() {
		fields["date_due"] = "required"
	}
	if row.PeriodStart.IsZero() {
		fields["period_start"] = "required"
	}
	if row.PeriodEnd.IsZero() {
		fields["period_end"] = "required"
	}
	checkBillDates(row, fields)
}

// checkBillDates compares the three dates a bill carries. A period that runs
// backwards prices consumption the tenant never used, and a charge due before
// the period closed is late the day it is raised, so both are refused wherever
// the dates are assembled rather than left for the database to reject.
func checkBillDates(row *BillRow, fields map[string]string) {
	if !row.PeriodEnd.IsZero() && !row.PeriodStart.IsZero() && row.PeriodEnd.Before(row.PeriodStart) {
		fields["period_end"] = "cannot be earlier than period_start"
	}
	if !row.DateDue.IsZero() && !row.PeriodEnd.IsZero() && row.DateDue.Before(row.PeriodEnd) {
		fields["date_due"] = "cannot be earlier than period_end"
	}
}

func applyBillPatch(row *BillRow, in UpdateBillInput, fields map[string]string) (map[string]any, map[string]any) {
	oldValues := map[string]any{}
	newValues := map[string]any{}
	record := func(name string, before, after any) {
		oldValues[name] = before
		newValues[name] = after
	}

	if in.UnitID != nil {
		if *in.UnitID == 0 {
			fields["unit_id"] = "required"
		} else if *in.UnitID != row.UnitID {
			record("unit_id", row.UnitID, *in.UnitID)
			row.UnitID = *in.UnitID
		}
	}
	if in.MeterID != nil {
		next := sql.NullInt64{}
		if *in.MeterID != 0 {
			next = sql.NullInt64{Int64: int64(*in.MeterID), Valid: true}
		}
		if next != row.MeterID {
			record("meter_id", nullableID(row.MeterID), nullableID(next))
			row.MeterID = next
		}
	}
	if in.Type != nil {
		value := strings.ToLower(strings.TrimSpace(*in.Type))
		switch {
		case !inList(value, utilityTypes):
			fields["type"] = utilityTypeMessage
		case value != row.Type:
			record("type", row.Type, value)
			row.Type = value
		}
	}
	if in.Amount != nil {
		value := roundToCents(*in.Amount)
		switch {
		case value <= 0:
			fields["amount"] = "must be more than zero"
		case value > maxAmount:
			fields["amount"] = fmt.Sprintf("must be at most %.2f", maxAmount)
		case value != row.Amount:
			record("amount", row.Amount, value)
			row.Amount = value
		}
	}
	movedDate := false
	if in.DateDue != nil {
		next := parseOptionalDate("date_due", in.DateDue, fields)
		if !next.IsZero() && !next.Equal(row.DateDue) {
			record("date_due", row.DateDue.Format(dateLayout), next.Format(dateLayout))
			row.DateDue = next
			movedDate = true
		}
	}
	if in.PeriodStart != nil {
		next := parseOptionalDate("period_start", in.PeriodStart, fields)
		if !next.IsZero() && !next.Equal(row.PeriodStart) {
			record("period_start", row.PeriodStart.Format(dateLayout), next.Format(dateLayout))
			row.PeriodStart = next
			movedDate = true
		}
	}
	if in.PeriodEnd != nil {
		next := parseOptionalDate("period_end", in.PeriodEnd, fields)
		if !next.IsZero() && !next.Equal(row.PeriodEnd) {
			record("period_end", row.PeriodEnd.Format(dateLayout), next.Format(dateLayout))
			row.PeriodEnd = next
			movedDate = true
		}
	}
	// One field at a time is all a patch can be judged by, and a single end of a
	// period moved on its own is enough to turn the whole row backwards.
	if movedDate {
		checkBillDates(row, fields)
	}
	return oldValues, newValues
}

// billRowFromReading prices one open reading. The second return value is the
// reason the reading was skipped, empty when a bill can be raised from it.
func billRowFromReading(candidate OpenReadingRow) (*BillRow, string) {
	if !candidate.LeaseID.Valid {
		return nil, "no active lease"
	}
	if candidate.Consumption <= 0 {
		return nil, "nothing consumed"
	}
	amount := roundToCents(candidate.Consumption * candidate.RatePerUnit)
	if amount <= 0 {
		return nil, "no rate set on the meter"
	}
	periodEnd := candidate.ReadingDate.UTC()
	periodStart := periodEnd
	if candidate.PeriodStart.Valid {
		periodStart = candidate.PeriodStart.Time.UTC()
	}
	dueDay := defaultDueDay
	if candidate.LeaseDueDay.Valid {
		dueDay = int(candidate.LeaseDueDay.Int64)
	}
	return &BillRow{
		UnitID:      candidate.UnitID,
		MeterID:     sql.NullInt64{Int64: int64(candidate.MeterID), Valid: true},
		ReadingID:   sql.NullInt64{Int64: int64(candidate.ReadingID), Valid: true},
		Type:        candidate.UtilityType,
		Amount:      amount,
		DateDue:     dueDateAfter(periodEnd, dueDay),
		PeriodStart: periodStart,
		PeriodEnd:   periodEnd,
		Status:      billPending,
	}, ""
}

func billSummaryDTOs(rows []BillViewRow, today time.Time) []BillSummaryDTO {
	out := make([]BillSummaryDTO, 0, len(rows))
	for i := range rows {
		out = append(out, billSummaryDTO(&rows[i], today))
	}
	return out
}

func billSummaryDTO(row *BillViewRow, today time.Time) BillSummaryDTO {
	return BillSummaryDTO{
		ID:          row.ID,
		Type:        row.Type,
		Amount:      row.Amount,
		Status:      row.Status,
		DateDue:     row.DateDue.UTC(),
		PeriodStart: row.PeriodStart.UTC(),
		PeriodEnd:   row.PeriodEnd.UTC(),
		Property:    propertyRefDTO(row.PropertyID, row.PropertyName, row.PropertyCode),
		Unit:        &UnitRefDTO{ID: row.UnitID, Number: row.UnitNumber},
		MeterID:     nullIDPtr(row.MeterID),
		MeterNumber: nullText(row.MeterNumber),
		ReadingID:   nullIDPtr(row.ReadingID),
		ReadingDate: nullTimePtr(row.ReadingDate),
		Consumption: nullFloatPtr(row.Consumption),
		InvoiceID:   nullIDPtr(row.InvoiceID),
		Overdue:     isOverdue(row.Status, row.DateDue, today),
		CreatedAt:   row.CreatedAt.UTC(),
		UpdatedAt:   row.UpdatedAt.UTC(),
	}
}

func billDetailDTO(row *BillViewRow, today time.Time) BillDetailDTO {
	return BillDetailDTO{
		BillSummaryDTO: billSummaryDTO(row, today),
		Lease:          leaseRefDTO(row.LeaseID, row.LeaseNumber),
		TenantName:     nullText(row.TenantName),
		InvoiceNumber:  nullText(row.InvoiceNumber),
		InvoiceStatus:  nullText(row.InvoiceStatus),
	}
}

func leaseRefDTO(id sql.NullInt64, number sql.NullString) *LeaseRefDTO {
	if !id.Valid || id.Int64 <= 0 {
		return nil
	}
	return &LeaseRefDTO{ID: uint64(id.Int64), Number: nullText(number)}
}

func dueDayOf(unit *UnitRefRow) int {
	if unit != nil && unit.LeaseDueDay.Valid {
		return int(unit.LeaseDueDay.Int64)
	}
	return defaultDueDay
}

// periodStartFrom opens the charge at the reading before the one being billed,
// so the period on the bill is the stretch of the dial the consumption covers.
// The first reading on a new meter has no predecessor and is billed on its own
// day, which is the reading that establishes the starting figure.
func periodStartFrom(reading *ReadingSnapRow) time.Time {
	if reading.PrevReadingDate.Valid {
		return reading.PrevReadingDate.Time.UTC()
	}
	return reading.ReadingDate.UTC()
}

// isOverdue is the one date test a bill list needs: money that is past its due
// date and has not been settled. A void bill is never overdue.
func isOverdue(status string, due, today time.Time) bool {
	if status != billPending && status != billBilled {
		return false
	}
	return truncateDate(due).Before(truncateDate(today))
}
