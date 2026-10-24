package utilities

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/audit"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

// Meters is the list view. Filtering happens in SQL, so a caretaker paging
// through a estate never receives rows from a property they do not manage.
func (s *Service) Meters(ctx context.Context, p *rbac.Principal, q MeterQuery) (*MeterListDTO, error) {
	if err := authorize(p, "utilities.view"); err != nil {
		return nil, err
	}
	if err := normalizeMeterQuery(&q); err != nil {
		return nil, err
	}
	rows, total, err := s.repo.ListMeters(ctx, p.OrgID, p.Scope, q)
	if err != nil {
		return nil, err
	}
	items := make([]MeterSummaryDTO, 0, len(rows))
	for i := range rows {
		items = append(items, meterSummaryDTO(&rows[i]))
	}
	return &MeterListDTO{Meters: items, Total: total}, nil
}

func (s *Service) Meter(ctx context.Context, p *rbac.Principal, id uint64) (*MeterDetailDTO, error) {
	if err := authorize(p, "utilities.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	return s.meterDetail(ctx, p, id)
}

func (s *Service) Lookups(ctx context.Context, p *rbac.Principal) (*UtilityLookupsDTO, error) {
	if err := authorize(p, "utilities.view"); err != nil {
		return nil, err
	}
	units, err := s.repo.ScopeUnits(ctx, p.OrgID, p.Scope)
	if err != nil {
		return nil, err
	}
	meters, _, err := s.repo.ListMeters(ctx, p.OrgID, p.Scope, MeterQuery{
		Page: httpx.Page{Page: 1, PerPage: maxLookupUnits, Limit: maxLookupUnits},
		Sort: defaultMeterSort,
	})
	if err != nil {
		return nil, err
	}
	unitOptions := make([]UnitOptionDTO, 0, len(units))
	for _, unit := range units {
		unitOptions = append(unitOptions, UnitOptionDTO{
			ID: unit.ID, Number: unit.Number,
			PropertyID: unit.PropertyID, PropertyName: unit.PropertyName,
		})
	}
	meterOptions := make([]MeterOptionDTO, 0, len(meters))
	for i := range meters {
		meterOptions = append(meterOptions, meterOptionDTO(&meters[i]))
	}
	return &UtilityLookupsDTO{
		UtilityTypes:  utilityTypes,
		MeterStatuses: meterStatuses,
		BillStatuses:  billStatuses,
		Units:         unitOptions,
		Meters:        meterOptions,
	}, nil
}

func (s *Service) Summary(ctx context.Context, p *rbac.Principal, propertyID uint64) (*UtilitySummaryDTO, error) {
	if err := authorize(p, "utilities.view"); err != nil {
		return nil, err
	}
	meters, err := s.repo.CountMeters(ctx, p.OrgID, p.Scope, propertyID)
	if err != nil {
		return nil, err
	}
	open, err := s.repo.CountOpenReadings(ctx, p.OrgID, p.Scope, propertyID)
	if err != nil {
		return nil, err
	}
	totals, err := s.repo.BillTotals(ctx, p.OrgID, p.Scope, propertyID)
	if err != nil {
		return nil, err
	}
	return &UtilitySummaryDTO{
		Meters:       meters,
		OpenReadings: open,
		PendingCount: totals.PendingCount,
		PendingTotal: roundToCents(totals.PendingTotal),
		BilledTotal:  roundToCents(totals.BilledTotal),
	}, nil
}

// CreateMeter registers a dial. The rate is stored on the meter because it is the
// meter's own tariff, but no money is created here: a meter is only ever read.
func (s *Service) CreateMeter(ctx context.Context, p *rbac.Principal, in CreateMeterInput, meta ClientMeta) (*MeterDetailDTO, error) {
	if err := authorize(p, "utilities.create"); err != nil {
		return nil, err
	}
	fields := map[string]string{}
	row := buildMeterCreate(in, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.checkMeterRefs(ctx, tx, p, row, fields); err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}
	taken, err := s.repo.MeterNumberTaken(ctx, tx, row.PropertyID, row.MeterNumber, 0)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, httpx.Conflict("This property already has a meter with that number")
	}

	id, err := s.repo.InsertMeter(ctx, tx, row)
	if err != nil {
		if isDuplicateEntry(err) {
			return nil, httpx.Conflict("This property already has a meter with that number")
		}
		if rejection := s.reject(err, "create meter", "The meter breaks a database rule"); rejection != nil {
			return nil, rejection
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "utility_meters.created", EntityType: "utility_meters", EntityID: id,
		NewValues: map[string]any{
			"property_id":     row.PropertyID,
			"unit_id":         nullableID(row.UnitID),
			"utility_type":    row.UtilityType,
			"meter_number":    row.MeterNumber,
			"unit_of_measure": row.UnitOfMeasure,
			"rate_per_unit":   row.RatePerUnit,
			"status":          row.Status,
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.meterDetail(ctx, p, id)
}

// UpdateMeter changes the dial or its tariff. Changing the utility type is only
// allowed while the meter has never been read, because the readings already
// taken are quantities of the old utility.
func (s *Service) UpdateMeter(ctx context.Context, p *rbac.Principal, id uint64, in UpdateMeterInput, meta ClientMeta) (*MeterDetailDTO, error) {
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

	row, err := s.repo.MeterForUpdate(ctx, tx, p.OrgID, id, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	fields := map[string]string{}
	oldValues, newValues := applyMeterPatch(row, in, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}
	if len(newValues) == 0 {
		return s.meterDetail(ctx, p, id)
	}

	if _, changed := newValues["utility_type"]; changed {
		readings, err := s.repo.CountReadings(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if readings > 0 {
			return nil, httpx.Conflict("A meter that has been read cannot change utility type")
		}
	}
	if err := s.checkMeterRefs(ctx, tx, p, row, fields); err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}
	if _, changed := newValues["meter_number"]; changed {
		taken, err := s.repo.MeterNumberTaken(ctx, tx, row.PropertyID, row.MeterNumber, id)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, httpx.Conflict("This property already has a meter with that number")
		}
	}

	if err := s.repo.UpdateMeter(ctx, tx, row); err != nil {
		if isDuplicateEntry(err) {
			return nil, httpx.Conflict("This property already has a meter with that number")
		}
		if rejection := s.reject(err, "update meter", "The meter breaks a database rule"); rejection != nil {
			return nil, rejection
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "utility_meters.updated", EntityType: "utility_meters", EntityID: id,
		OldValues: oldValues, NewValues: newValues,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.meterDetail(ctx, p, id)
}

// DeleteMeter removes a dial that has never produced a quantity. The readings
// cascade with it and the bills that were raised from them lose their link, so a
// meter with history has to be corrected rather than deleted.
func (s *Service) DeleteMeter(ctx context.Context, p *rbac.Principal, id uint64, meta ClientMeta) error {
	if err := authorize(p, "utilities.update"); err != nil {
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

	row, err := s.repo.MeterForUpdate(ctx, tx, p.OrgID, id, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	readings, err := s.repo.CountReadings(ctx, tx, id)
	if err != nil {
		return err
	}
	if readings > 0 {
		return httpx.Conflict("Delete this meter's readings before deleting the meter")
	}
	bills, err := s.repo.CountMeterBills(ctx, tx, id)
	if err != nil {
		return err
	}
	if bills > 0 {
		return httpx.Conflict("Charges have already been raised from this meter; set it to faulty instead")
	}
	if err := s.repo.DeleteMeter(ctx, tx, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "utility_meters.deleted", EntityType: "utility_meters", EntityID: id,
		OldValues: map[string]any{
			"property_id":  row.PropertyID,
			"meter_number": row.MeterNumber,
			"utility_type": row.UtilityType,
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return nil
}

// checkMeterRefs validates the two links a meter row carries: the property it
// belongs to and, when it is not a shared meter, the unit it is attached to.
func (s *Service) checkMeterRefs(ctx context.Context, tx *sqlx.Tx, p *rbac.Principal, row *MeterRow, fields map[string]string) error {
	inScope, err := s.repo.PropertyInScope(ctx, tx, p.OrgID, row.PropertyID, p.Scope)
	if err != nil {
		return err
	}
	if !inScope {
		fields["property_id"] = "must be a property you can manage"
	}
	if row.UnitID.Valid {
		onProperty, err := s.repo.UnitIsOnProperty(ctx, tx, row.PropertyID, uint64(row.UnitID.Int64))
		if err != nil {
			return err
		}
		if !onProperty {
			fields["unit_id"] = "must be a unit of this property"
		}
	}
	return nil
}

// meterDetail re-reads a meter through the caller's scope and attaches the last
// readings and bills, so every write endpoint answers with the same shape the
// list uses.
func (s *Service) meterDetail(ctx context.Context, p *rbac.Principal, id uint64) (*MeterDetailDTO, error) {
	row, err := s.repo.GetMeter(ctx, p.OrgID, id, p.Scope)
	if err != nil {
		return nil, notFound(err)
	}
	page := httpx.Page{Page: 1, PerPage: maxDetailRows, Limit: maxDetailRows}
	readings, _, err := s.repo.ListReadings(ctx, p.OrgID, id, p.Scope, ReadingQuery{Page: page, Sort: defaultReadingSort})
	if err != nil {
		return nil, err
	}
	today := s.today(ctx, p.OrgID)
	bills, _, err := s.repo.ListBills(ctx, p.OrgID, p.Scope, BillQuery{
		MeterID: id,
		Page:    page,
		Sort:    defaultBillSort,
	}, today)
	if err != nil {
		return nil, err
	}
	detail := MeterDetailDTO{
		MeterSummaryDTO: meterSummaryDTO(row),
		Readings:        readingDTOs(readings),
		Bills:           billSummaryDTOs(bills, today),
	}
	return &detail, nil
}

func normalizeMeterQuery(q *MeterQuery) error {
	fields := map[string]string{}
	q.Search = strings.TrimSpace(q.Search)
	if utf8.RuneCountInString(q.Search) > maxSearch {
		fields["q"] = fmt.Sprintf("must be at most %d characters", maxSearch)
	}
	if strings.ContainsAny(q.Search, "%_") {
		fields["q"] = "cannot contain wildcards"
	}
	q.UtilityType = strings.ToLower(strings.TrimSpace(q.UtilityType))
	if q.UtilityType != "" && !inList(q.UtilityType, utilityTypes) {
		fields["utility_type"] = utilityTypeMessage
	}
	q.Status = strings.ToLower(strings.TrimSpace(q.Status))
	if q.Status != "" && !inList(q.Status, meterStatuses) {
		fields["status"] = meterStatusMessage
	}
	if q.Sort == "" {
		q.Sort = defaultMeterSort
	}
	if len(fields) > 0 {
		return httpx.Validation(fields)
	}
	return nil
}

func buildMeterCreate(in CreateMeterInput, fields map[string]string) *MeterRow {
	row := &MeterRow{PropertyID: in.PropertyID, Status: meterActive}
	if in.PropertyID == 0 {
		fields["property_id"] = "required"
	}
	row.MeterNumber = cleanLabel(in.MeterNumber, maxMeterNumber, "meter_number", fields)
	if in.UtilityType == "" {
		fields["utility_type"] = "required"
	} else {
		value := strings.ToLower(strings.TrimSpace(in.UtilityType))
		if inList(value, utilityTypes) {
			row.UtilityType = value
		} else {
			fields["utility_type"] = utilityTypeMessage
		}
	}
	row.UnitOfMeasure = meterUnitOfMeasure(in.UnitOfMeasure, row.UtilityType, fields)
	row.RatePerUnit = meterRate(in.RatePerUnit, fields)
	if in.Status != "" {
		value := strings.ToLower(strings.TrimSpace(in.Status))
		if inList(value, meterStatuses) {
			row.Status = value
		} else {
			fields["status"] = meterStatusMessage
		}
	}
	if in.UnitID != 0 {
		row.UnitID = sql.NullInt64{Int64: int64(in.UnitID), Valid: true}
	}
	return row
}

func applyMeterPatch(row *MeterRow, in UpdateMeterInput, fields map[string]string) (map[string]any, map[string]any) {
	oldValues := map[string]any{}
	newValues := map[string]any{}
	record := func(name string, before, after any) {
		oldValues[name] = before
		newValues[name] = after
	}

	if in.UnitID != nil {
		next := sql.NullInt64{}
		if *in.UnitID != 0 {
			next = sql.NullInt64{Int64: int64(*in.UnitID), Valid: true}
		}
		if next != row.UnitID {
			record("unit_id", nullableID(row.UnitID), nullableID(next))
			row.UnitID = next
		}
	}
	if in.UtilityType != nil {
		value := strings.ToLower(strings.TrimSpace(*in.UtilityType))
		switch {
		case !inList(value, utilityTypes):
			fields["utility_type"] = utilityTypeMessage
		case value != row.UtilityType:
			record("utility_type", row.UtilityType, value)
			row.UtilityType = value
		}
	}
	if in.MeterNumber != nil {
		next := cleanLabel(*in.MeterNumber, maxMeterNumber, "meter_number", fields)
		if fields["meter_number"] == "" && next != row.MeterNumber {
			record("meter_number", row.MeterNumber, next)
			row.MeterNumber = next
		}
	}
	if in.UnitOfMeasure != nil {
		next := meterUnitOfMeasure(*in.UnitOfMeasure, row.UtilityType, fields)
		if fields["unit_of_measure"] == "" && next != row.UnitOfMeasure {
			record("unit_of_measure", row.UnitOfMeasure, next)
			row.UnitOfMeasure = next
		}
	}
	if in.RatePerUnit != nil {
		value := meterRate(*in.RatePerUnit, fields)
		if fields["rate_per_unit"] == "" && value != row.RatePerUnit {
			record("rate_per_unit", row.RatePerUnit, value)
			row.RatePerUnit = value
		}
	}
	if in.Status != nil {
		value := strings.ToLower(strings.TrimSpace(*in.Status))
		switch {
		case value == "":
			fields["status"] = "required"
		case !inList(value, meterStatuses):
			fields["status"] = meterStatusMessage
		case value != row.Status:
			record("status", row.Status, value)
			row.Status = value
		}
	}
	return oldValues, newValues
}

// meterUnitOfMeasure accepts the caller's unit or falls back to the one this
// utility is measured in, so a water meter is never billed by the kilowatt hour.
func meterUnitOfMeasure(raw, utilityType string, fields map[string]string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return defaultUnitOfMeasure(utilityType)
	}
	switch {
	case utf8.RuneCountInString(value) > maxUnitOfMeasure:
		fields["unit_of_measure"] = fmt.Sprintf("must be at most %d characters", maxUnitOfMeasure)
		return defaultUnitOfMeasure(utilityType)
	case strings.IndexFunc(value, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) >= 0:
		fields["unit_of_measure"] = "must be a short unit such as m3, kWh or unit"
		return defaultUnitOfMeasure(utilityType)
	}
	return value
}

func defaultUnitOfMeasure(utilityType string) string {
	switch utilityType {
	case utilityWater, utilityGas:
		return "m3"
	case utilityElectricity:
		return "kWh"
	default:
		return "unit"
	}
}

func meterRate(value float64, fields map[string]string) float64 {
	rounded := roundToThree(value)
	if rounded < 0 || rounded > maxRate {
		fields["rate_per_unit"] = fmt.Sprintf("must be between 0 and %.4f", maxRate)
		return 0
	}
	return rounded
}

func meterSummaryDTO(row *MeterViewRow) MeterSummaryDTO {
	return MeterSummaryDTO{
		ID:            row.ID,
		MeterNumber:   row.MeterNumber,
		UtilityType:   row.UtilityType,
		UnitOfMeasure: row.UnitOfMeasure,
		RatePerUnit:   row.RatePerUnit,
		Status:        row.Status,
		Property:      propertyRefDTO(row.PropertyID, row.PropertyName, row.PropertyCode),
		Unit:          unitRefDTO(row.UnitID, row.UnitNumber),
		LastReadingAt: nullTimePtr(row.LastReadingDate),
		LastReading:   nullFloatPtr(row.LastReadingVal),
		OpenReadings:  row.OpenReadings,
		CreatedAt:     row.CreatedAt.UTC(),
		UpdatedAt:     row.UpdatedAt.UTC(),
	}
}

func meterOptionDTO(row *MeterViewRow) MeterOptionDTO {
	return MeterOptionDTO{
		ID:            row.ID,
		MeterNumber:   row.MeterNumber,
		UtilityType:   row.UtilityType,
		UnitOfMeasure: row.UnitOfMeasure,
		RatePerUnit:   row.RatePerUnit,
		UnitID:        nullIDPtr(row.UnitID),
		UnitNumber:    unitNumberText(row.UnitID, row.UnitNumber),
		PropertyName:  row.PropertyName,
		OpenReadings:  row.OpenReadings,
	}
}

// unitNumberText names the door a meter serves only while the meter is served
// to a unit: a property-level meter has no unit id, and a picklist that shows it
// against a door would offer a link that does not exist.
func unitNumberText(id sql.NullInt64, number sql.NullString) string {
	if !id.Valid || id.Int64 <= 0 {
		return ""
	}
	return nullText(number)
}

func propertyRefDTO(id uint64, name string, code sql.NullString) PropertyRefDTO {
	return PropertyRefDTO{ID: id, Name: name, Code: nullText(code)}
}

func unitRefDTO(id sql.NullInt64, number sql.NullString) *UnitRefDTO {
	if !id.Valid || id.Int64 <= 0 {
		return nil
	}
	return &UnitRefDTO{ID: uint64(id.Int64), Number: nullText(number)}
}
