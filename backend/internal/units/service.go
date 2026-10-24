package units

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"

	"kodipms/internal/audit"
	"kodipms/internal/platform/cache"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

const (
	lookupsCacheTTL = time.Hour
	unitTypesKey    = "lookup:unit_types"
	amenitiesKey    = "lookup:amenities"

	defaultUnitSort = "p.name ASC, u.number ASC"

	maxNumber        = 20
	maxPrefix        = 10
	maxSearch        = 100
	minFloor         = -10
	maxFloor         = 200
	maxBedrooms      = 50
	maxBathrooms     = 99.9
	maxSquareFeet    = 1000000
	maxRent          = 9999999999.99
	minGenerateCount = 1
	maxGenerateCount = 200
	maxPad           = 5

	statusVacant      = "vacant"
	statusOccupied    = "occupied"
	statusReserved    = "reserved"
	statusMaintenance = "maintenance"
	statusInactive    = "inactive"

	occupiedMessage = "occupied is set by an active lease"
	leaseHeld       = "the active lease keeps this unit occupied"
)

var (
	unitStatuses  = []string{statusVacant, statusOccupied, statusReserved, statusMaintenance, statusInactive}
	statusMessage = "must be one of vacant, occupied, reserved, maintenance, inactive"
)

var unitSortColumns = map[string]string{
	"number":     "u.number",
	"rent":       "u.rent",
	"floor":      "u.floor",
	"bedrooms":   "u.bedrooms",
	"status":     "u.status",
	"created_at": "u.created_at",
	"updated_at": "u.updated_at",
	"property":   "p.name",
}

type Service struct {
	db    *sqlx.DB
	repo  *repository
	store cache.Store
	audit *audit.Logger
	log   *slog.Logger
}

func NewService(db *sqlx.DB, store cache.Store, auditLog *audit.Logger, log *slog.Logger) *Service {
	return &Service{
		db:    db,
		repo:  &repository{db: db},
		store: store,
		audit: auditLog,
		log:   log,
	}
}

func (s *Service) List(ctx context.Context, p *rbac.Principal, q ListQuery) (*UnitListDTO, error) {
	if err := authorize(p, "units.view"); err != nil {
		return nil, err
	}
	fields := map[string]string{}
	q.Search = strings.TrimSpace(q.Search)
	if utf8.RuneCountInString(q.Search) > maxSearch {
		fields["q"] = fmt.Sprintf("must be at most %d characters", maxSearch)
	}
	q.Status = strings.ToLower(strings.TrimSpace(q.Status))
	if q.Status != "" && !validStatus(q.Status) {
		fields["status"] = statusMessage
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	rows, total, err := s.repo.List(ctx, p.OrgID, p.Scope, q)
	if err != nil {
		return nil, err
	}
	items := make([]UnitSummaryDTO, 0, len(rows))
	for i := range rows {
		items = append(items, unitSummaryDTO(&rows[i]))
	}
	return &UnitListDTO{Units: items, Total: total}, nil
}

func (s *Service) Get(ctx context.Context, p *rbac.Principal, id uint64) (*UnitDetailDTO, error) {
	if err := authorize(p, "units.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	return s.detail(ctx, p.OrgID, id, p.Scope)
}

func (s *Service) Lookups(ctx context.Context, p *rbac.Principal) (*UnitLookupsDTO, error) {
	if err := authorize(p, "units.view"); err != nil {
		return nil, err
	}
	types, err := s.cachedLookups(ctx, unitTypesKey, s.repo.UnitTypes)
	if err != nil {
		return nil, err
	}
	amenities, err := s.cachedLookups(ctx, amenitiesKey, s.repo.AmenityLookups)
	if err != nil {
		return nil, err
	}
	return &UnitLookupsDTO{UnitTypes: types, Amenities: amenities}, nil
}

func (s *Service) Create(ctx context.Context, p *rbac.Principal, in CreateInput, meta ClientMeta) (*UnitDetailDTO, error) {
	if err := authorize(p, "units.create"); err != nil {
		return nil, err
	}
	fields := map[string]string{}
	row := buildUnitCreate(p.OrgID, in, fields)
	amenityIDs := normalizedAmenityIDs(in.AmenityIDs, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.checkReferences(ctx, tx, p, row.PropertyID, row.TypeID, amenityIDs, fields); err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}
	taken, err := s.repo.NumberTaken(ctx, tx, row.PropertyID, row.Number, 0)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, httpx.Conflict("This property already has a unit with that number")
	}

	id, err := s.repo.Insert(ctx, tx, row)
	if err != nil {
		if isDuplicateEntry(err) {
			return nil, httpx.Conflict("This property already has a unit with that number")
		}
		return nil, err
	}
	if err := s.repo.ReplaceAmenities(ctx, tx, id, amenityIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "units.created", EntityType: "units", EntityID: id,
		NewValues: map[string]any{
			"number":      row.Number,
			"property_id": row.PropertyID,
			"type_id":     row.TypeID,
			"rent":        row.Rent,
			"status":      row.Status,
			"amenities":   amenityIDs,
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})

	return s.detail(ctx, p.OrgID, id, p.Scope)
}

// Generate fills a property with sequentially numbered units that share one
// template, so a new block does not need one request per door.
func (s *Service) Generate(ctx context.Context, p *rbac.Principal, in GenerateInput, meta ClientMeta) (*GenerateResultDTO, error) {
	if err := authorize(p, "units.create"); err != nil {
		return nil, err
	}
	fields := map[string]string{}
	numbers := generateNumbers(in, fields)
	rows := buildUnitBatch(p.OrgID, in, numbers, fields)
	amenityIDs := normalizedAmenityIDs(in.AmenityIDs, fields)
	if in.PropertyID == 0 {
		fields["property_id"] = "required"
	}
	if in.TypeID == 0 {
		fields["type_id"] = "required"
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	propertyID := in.PropertyID
	typeID := in.TypeID
	if err := s.checkReferences(ctx, tx, p, propertyID, typeID, amenityIDs, fields); err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	used, err := s.repo.UsedNumbers(ctx, tx, propertyID, numbers)
	if err != nil {
		return nil, err
	}
	if len(used) > 0 {
		return nil, httpx.Conflict(fmt.Sprintf("This property already has units numbered: %s", strings.Join(used, ", ")))
	}

	ids := make([]uint64, 0, len(rows))
	for _, row := range rows {
		id, err := s.repo.Insert(ctx, tx, row)
		if err != nil {
			if isDuplicateEntry(err) {
				return nil, httpx.Conflict("This property already has a unit with that number")
			}
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := s.repo.AddAmenities(ctx, tx, ids, amenityIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "units.generated", EntityType: "properties", EntityID: propertyID,
		NewValues: map[string]any{
			"count":   len(ids),
			"first":   numbers[0],
			"last":    numbers[len(numbers)-1],
			"type_id": typeID,
			"rent":    rows[0].Rent,
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})

	views, err := s.repo.ByIDs(ctx, p.OrgID, ids, p.Scope)
	if err != nil {
		return nil, err
	}
	units := make([]UnitSummaryDTO, 0, len(views))
	for i := range views {
		units = append(units, unitSummaryDTO(&views[i]))
	}
	return &GenerateResultDTO{Created: len(units), Units: units}, nil
}

func (s *Service) Update(ctx context.Context, p *rbac.Principal, id uint64, in UpdateInput, meta ClientMeta) (*UnitDetailDTO, error) {
	if err := authorize(p, "units.update"); err != nil {
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

	row, err := s.repo.GetForUpdate(ctx, tx, p.OrgID, id, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	leaseID, err := s.repo.ActiveLeaseID(ctx, tx, id)
	if err != nil {
		return nil, err
	}

	fields := map[string]string{}
	originalType := row.TypeID
	oldValues, newValues := applyUnitPatch(row, in, fields, leaseID != 0)

	var amenityIDs, oldAmenityIDs []uint64
	amenitiesChanged := false
	if in.AmenityIDs != nil {
		amenityIDs = normalizedAmenityIDs(*in.AmenityIDs, fields)
	}
	if len(fields) == 0 && in.AmenityIDs != nil {
		missing, err := s.repo.MissingAmenities(ctx, tx, amenityIDs)
		if err != nil {
			return nil, err
		}
		if len(missing) > 0 {
			fields["amenity_ids"] = "contains unknown amenities"
		} else {
			oldAmenityIDs, err = s.repo.AmenityIDs(ctx, tx, id)
			if err != nil {
				return nil, err
			}
			amenitiesChanged = !sameIDSet(oldAmenityIDs, amenityIDs)
		}
	}
	if len(fields) == 0 && row.TypeID != originalType {
		ok, err := s.repo.UnitTypeExists(ctx, tx, row.TypeID)
		if err != nil {
			return nil, err
		}
		if !ok {
			fields["type_id"] = "must reference an existing unit type"
		}
	}
	if len(fields) == 0 && in.Number != nil {
		taken, err := s.repo.NumberTaken(ctx, tx, row.PropertyID, row.Number, id)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, httpx.Conflict("This property already has a unit with that number")
		}
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	if len(newValues) > 0 {
		if err := s.repo.Update(ctx, tx, row); err != nil {
			if isDuplicateEntry(err) {
				return nil, httpx.Conflict("This property already has a unit with that number")
			}
			return nil, err
		}
	}
	if amenitiesChanged {
		if err := s.repo.ReplaceAmenities(ctx, tx, id, amenityIDs); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	if amenitiesChanged {
		oldValues["amenities"] = oldAmenityIDs
		newValues["amenities"] = amenityIDs
	}
	if len(newValues) > 0 {
		s.writeAudit(ctx, audit.Entry{
			OrgID: p.OrgID, UserID: p.UserID,
			Action: "units.updated", EntityType: "units", EntityID: id,
			OldValues: oldValues, NewValues: newValues,
			IPAddress: meta.IP, UserAgent: meta.UserAgent,
		})
	}

	return s.detail(ctx, p.OrgID, id, p.Scope)
}

func (s *Service) Delete(ctx context.Context, p *rbac.Principal, id uint64, meta ClientMeta) error {
	if err := authorize(p, "units.delete"); err != nil {
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

	row, err := s.repo.GetForUpdate(ctx, tx, p.OrgID, id, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	leaseID, err := s.repo.ActiveLeaseID(ctx, tx, id)
	if err != nil {
		return err
	}
	if leaseID != 0 {
		return httpx.Conflict("Unit has an active lease and cannot be deleted")
	}
	if err := s.repo.SoftDelete(ctx, tx, p.OrgID, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "units.deleted", EntityType: "units", EntityID: id,
		OldValues: map[string]any{"number": row.Number, "property_id": row.PropertyID},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return nil
}

func (s *Service) detail(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*UnitDetailDTO, error) {
	row, err := s.repo.Get(ctx, orgID, id, scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	amenities, err := s.repo.Amenities(ctx, id)
	if err != nil {
		return nil, err
	}
	out := unitDetailDTO(row, amenities)
	return &out, nil
}

func (s *Service) checkReferences(ctx context.Context, q querier, p *rbac.Principal, propertyID, typeID uint64, amenityIDs []uint64, fields map[string]string) error {
	inScope, err := s.repo.PropertyInScope(ctx, q, p.OrgID, propertyID, p.Scope)
	if err != nil {
		return err
	}
	if !inScope {
		fields["property_id"] = "must be a property you can manage"
	}
	exists, err := s.repo.UnitTypeExists(ctx, q, typeID)
	if err != nil {
		return err
	}
	if !exists {
		fields["type_id"] = "must reference an existing unit type"
	}
	if len(amenityIDs) > 0 {
		missing, err := s.repo.MissingAmenities(ctx, q, amenityIDs)
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			fields["amenity_ids"] = "contains unknown amenities"
		}
	}
	return nil
}

func (s *Service) cachedLookups(ctx context.Context, key string, load func(context.Context) ([]LookupRow, error)) ([]LookupDTO, error) {
	if raw, err := s.store.Get(ctx, key); err == nil {
		var cached []LookupDTO
		if json.Unmarshal([]byte(raw), &cached) == nil {
			return cached, nil
		}
	}
	rows, err := load(ctx)
	if err != nil {
		return nil, err
	}
	out := lookupDTOs(rows)
	if b, err := json.Marshal(out); err == nil {
		if err := s.store.Set(ctx, key, string(b), lookupsCacheTTL); err != nil {
			s.log.Debug("cache unit lookups", "key", key, "error", err)
		}
	}
	return out, nil
}

func (s *Service) writeAudit(ctx context.Context, e audit.Entry) {
	if err := s.audit.Log(ctx, e); err != nil {
		s.log.Warn("write unit audit", "action", e.Action, "org_id", e.OrgID, "error", err)
	}
}

func authorize(p *rbac.Principal, permission string) error {
	if p == nil || p.OrgID == 0 {
		return httpx.ErrUnauthorized
	}
	if !p.Can(permission) {
		return httpx.ErrForbidden
	}
	return nil
}

func buildUnitCreate(orgID uint64, in CreateInput, fields map[string]string) *UnitRow {
	row := &UnitRow{OrganizationID: orgID, PropertyID: in.PropertyID, TypeID: in.TypeID}
	if in.PropertyID == 0 {
		fields["property_id"] = "required"
	}
	if in.TypeID == 0 {
		fields["type_id"] = "required"
	}
	row.Number = unitNumber(in.Number, fields)
	if in.Status == "" {
		row.Status = statusVacant
	} else {
		value := strings.ToLower(strings.TrimSpace(in.Status))
		if validStatus(value) {
			row.Status = value
		} else {
			fields["status"] = statusMessage
		}
	}
	if row.Status == statusOccupied {
		fields["status"] = occupiedMessage
	}
	applyUnitTemplate(row, unitTemplate{
		Floor: in.Floor, Bedrooms: in.Bedrooms, Bathrooms: in.Bathrooms,
		SquareFeet: in.SquareFeet, Rent: in.Rent,
	}, fields)
	return row
}

type unitTemplate struct {
	Floor      *int
	Bedrooms   *int
	Bathrooms  *float64
	SquareFeet *int
	Rent       *float64
}

func applyUnitTemplate(row *UnitRow, t unitTemplate, fields map[string]string) {
	row.Bedrooms = 0
	if t.Bedrooms != nil {
		if *t.Bedrooms < 0 || *t.Bedrooms > maxBedrooms {
			fields["bedrooms"] = fmt.Sprintf("must be between 0 and %d", maxBedrooms)
		} else {
			row.Bedrooms = *t.Bedrooms
		}
	}
	row.Bathrooms = 1
	if t.Bathrooms != nil {
		value := roundToOne(*t.Bathrooms)
		if value < 0 || value > maxBathrooms {
			fields["bathrooms"] = fmt.Sprintf("must be between 0 and %v", maxBathrooms)
		} else {
			row.Bathrooms = value
		}
	}
	row.Rent = 0
	if t.Rent != nil {
		value := roundToCents(*t.Rent)
		if value < 0 || value > maxRent {
			fields["rent"] = fmt.Sprintf("must be between 0 and %.2f", maxRent)
		} else {
			row.Rent = value
		}
	}
	if t.Floor != nil {
		if *t.Floor < minFloor || *t.Floor > maxFloor {
			fields["floor"] = fmt.Sprintf("must be between %d and %d", minFloor, maxFloor)
		} else {
			row.Floor = sql.NullInt64{Int64: int64(*t.Floor), Valid: true}
		}
	}
	if t.SquareFeet != nil {
		if *t.SquareFeet < 1 || *t.SquareFeet > maxSquareFeet {
			fields["square_feet"] = fmt.Sprintf("must be between 1 and %d", maxSquareFeet)
		} else {
			row.SquareFeet = sql.NullInt64{Int64: int64(*t.SquareFeet), Valid: true}
		}
	}
}

func buildUnitBatch(orgID uint64, in GenerateInput, numbers []string, fields map[string]string) []*UnitRow {
	template := unitTemplate{
		Floor: in.Floor, Bedrooms: in.Bedrooms, Bathrooms: in.Bathrooms,
		SquareFeet: in.SquareFeet, Rent: in.Rent,
	}
	rows := make([]*UnitRow, 0, len(numbers))
	for _, number := range numbers {
		row := &UnitRow{
			OrganizationID: orgID,
			PropertyID:     in.PropertyID,
			TypeID:         in.TypeID,
			Status:         statusVacant,
		}
		row.Number = unitNumber(number, fields)
		applyUnitTemplate(row, template, fields)
		rows = append(rows, row)
	}
	return rows
}

// generateNumbers builds the requested run of unit numbers and validates the
// prefix, counter and padding before any SQL runs.
func generateNumbers(in GenerateInput, fields map[string]string) []string {
	if in.Count < minGenerateCount || in.Count > maxGenerateCount {
		fields["count"] = fmt.Sprintf("must be between %d and %d", minGenerateCount, maxGenerateCount)
		return nil
	}
	prefix := strings.TrimSpace(in.Prefix)
	if utf8.RuneCountInString(prefix) > maxPrefix {
		fields["prefix"] = fmt.Sprintf("must be at most %d characters", maxPrefix)
		return nil
	}
	if strings.ContainsAny(prefix, "%_") {
		fields["prefix"] = "cannot contain wildcards"
		return nil
	}
	first := 1
	if in.FirstNumber != nil {
		if *in.FirstNumber < 0 {
			fields["first_number"] = "cannot be negative"
			return nil
		}
		first = *in.FirstNumber
	}
	pad := 0
	if in.Pad != nil {
		if *in.Pad < 0 || *in.Pad > maxPad {
			fields["pad"] = fmt.Sprintf("must be between 0 and %d", maxPad)
			return nil
		}
		pad = *in.Pad
	}
	numbers := make([]string, 0, in.Count)
	for i := 0; i < in.Count; i++ {
		number := fmt.Sprintf("%s%0*d", prefix, pad, first+i)
		if utf8.RuneCountInString(number) > maxNumber {
			fields["prefix"] = fmt.Sprintf("produces numbers longer than %d characters", maxNumber)
			return nil
		}
		numbers = append(numbers, number)
	}
	return numbers
}

func applyUnitPatch(row *UnitRow, in UpdateInput, fields map[string]string, occupiedByLease bool) (map[string]any, map[string]any) {
	oldValues := map[string]any{}
	newValues := map[string]any{}
	record := func(name string, before, after any) {
		oldValues[name] = before
		newValues[name] = after
	}

	if in.Number != nil {
		next := unitNumber(*in.Number, fields)
		if fields["number"] == "" && next != row.Number {
			record("number", row.Number, next)
			row.Number = next
		}
	}
	if in.TypeID != nil {
		switch {
		case *in.TypeID == 0:
			fields["type_id"] = "required"
		case *in.TypeID != row.TypeID:
			record("type_id", row.TypeID, *in.TypeID)
			row.TypeID = *in.TypeID
		}
	}
	if in.Bedrooms != nil {
		if *in.Bedrooms < 0 || *in.Bedrooms > maxBedrooms {
			fields["bedrooms"] = fmt.Sprintf("must be between 0 and %d", maxBedrooms)
		} else if *in.Bedrooms != row.Bedrooms {
			record("bedrooms", row.Bedrooms, *in.Bedrooms)
			row.Bedrooms = *in.Bedrooms
		}
	}
	if in.Bathrooms != nil {
		value := roundToOne(*in.Bathrooms)
		if value < 0 || value > maxBathrooms {
			fields["bathrooms"] = fmt.Sprintf("must be between 0 and %v", maxBathrooms)
		} else if value != row.Bathrooms {
			record("bathrooms", row.Bathrooms, value)
			row.Bathrooms = value
		}
	}
	if in.Rent != nil {
		value := roundToCents(*in.Rent)
		if value < 0 || value > maxRent {
			fields["rent"] = fmt.Sprintf("must be between 0 and %.2f", maxRent)
		} else if value != row.Rent {
			record("rent", row.Rent, value)
			row.Rent = value
		}
	}
	if in.Status != nil {
		value := strings.ToLower(strings.TrimSpace(*in.Status))
		switch {
		case value == "":
			fields["status"] = "required"
		case !validStatus(value):
			fields["status"] = statusMessage
		case value == statusOccupied && !occupiedByLease:
			fields["status"] = occupiedMessage
		case occupiedByLease && value != statusOccupied:
			fields["status"] = leaseHeld
		case value != row.Status:
			record("status", row.Status, value)
			row.Status = value
		}
	}
	if hasRaw(in.Floor) {
		next, ok := parseNullableInt(in.Floor)
		switch {
		case !ok:
			fields["floor"] = "must be a whole number or null"
		case next.Valid && (next.Int64 < minFloor || next.Int64 > maxFloor):
			fields["floor"] = fmt.Sprintf("must be between %d and %d", minFloor, maxFloor)
		case next != row.Floor:
			record("floor", nullableInt(row.Floor), nullableInt(next))
			row.Floor = next
		}
	}
	if hasRaw(in.SquareFeet) {
		next, ok := parseNullableInt(in.SquareFeet)
		switch {
		case !ok:
			fields["square_feet"] = "must be a whole number or null"
		case next.Valid && (next.Int64 < 1 || next.Int64 > maxSquareFeet):
			fields["square_feet"] = fmt.Sprintf("must be between 1 and %d", maxSquareFeet)
		case next != row.SquareFeet:
			record("square_feet", nullableInt(row.SquareFeet), nullableInt(next))
			row.SquareFeet = next
		}
	}
	return oldValues, newValues
}

func unitNumber(value string, fields map[string]string) string {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		fields["number"] = "required"
	case utf8.RuneCountInString(value) > maxNumber:
		fields["number"] = fmt.Sprintf("must be at most %d characters", maxNumber)
	case strings.IndexFunc(value, unicode.IsControl) >= 0:
		fields["number"] = "cannot contain control characters"
	}
	return value
}

func validStatus(value string) bool {
	for _, s := range unitStatuses {
		if s == value {
			return true
		}
	}
	return false
}

func normalizedAmenityIDs(ids []uint64, fields map[string]string) []uint64 {
	seen := make(map[uint64]struct{}, len(ids))
	out := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			fields["amenity_ids"] = "must contain positive identifiers"
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func sameIDSet(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[uint64]struct{}, len(a))
	for _, id := range a {
		seen[id] = struct{}{}
	}
	for _, id := range b {
		if _, ok := seen[id]; !ok {
			return false
		}
	}
	return true
}

func roundToCents(value float64) float64 {
	return math.Round(value*100) / 100
}

func roundToOne(value float64) float64 {
	return math.Round(value*10) / 10
}

func hasRaw(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte{})
}

func parseNullableInt(raw json.RawMessage) (sql.NullInt64, bool) {
	if isJSONNull(raw) {
		return sql.NullInt64{}, true
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return sql.NullInt64{}, false
	}
	return sql.NullInt64{Int64: int64(value), Valid: true}, true
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func nullableInt(value sql.NullInt64) any {
	if value.Valid {
		return value.Int64
	}
	return nil
}

func intPointer(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	out := int(value.Int64)
	return &out
}

func lookupDTOs(rows []LookupRow) []LookupDTO {
	out := make([]LookupDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, LookupDTO{ID: r.ID, Name: r.Name, Description: nullText(r.Description)})
	}
	return out
}

func nullText(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func unitSummaryDTO(row *UnitViewRow) UnitSummaryDTO {
	return UnitSummaryDTO{
		ID:         row.ID,
		Number:     row.Number,
		Property:   PropertyRefDTO{ID: row.PropertyID, Name: row.PropertyName, Code: nullText(row.PropertyCode)},
		Type:       LookupDTO{ID: row.TypeID, Name: row.TypeName, Description: nullText(row.TypeDescription)},
		Floor:      intPointer(row.Floor),
		Bedrooms:   row.Bedrooms,
		Bathrooms:  row.Bathrooms,
		SquareFeet: intPointer(row.SquareFeet),
		Rent:       row.Rent,
		Status:     row.Status,
		Occupant:   occupantDTO(row),
		CreatedAt:  row.CreatedAt.UTC(),
		UpdatedAt:  row.UpdatedAt.UTC(),
	}
}

func occupantDTO(row *UnitViewRow) *OccupantDTO {
	if !row.LeaseID.Valid {
		return nil
	}
	return &OccupantDTO{
		LeaseID:     uint64(row.LeaseID.Int64),
		LeaseNumber: nullText(row.LeaseNumber),
		StartDate:   row.LeaseStart.Time.UTC(),
		EndDate:     timePointer(row.LeaseEnd),
		MonthlyRent: row.LeaseRent.Float64,
		TenantName:  nullText(row.TenantName),
	}
}

func unitDetailDTO(row *UnitViewRow, amenities []LookupRow) UnitDetailDTO {
	return UnitDetailDTO{
		UnitSummaryDTO: unitSummaryDTO(row),
		Amenities:      lookupDTOs(amenities),
	}
}

func timePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time.UTC()
	return &out
}

func isDuplicateEntry(err error) bool {
	var my *mysql.MySQLError
	if errors.As(err, &my) {
		return my.Number == 1062
	}
	return false
}
