package properties

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
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
	lookupsCacheTTL  = time.Hour
	propertyTypesKey = "lookup:property_types"
	amenitiesKey     = "lookup:amenities"

	defaultPropertySort = "p.name ASC"

	maxName       = 150
	maxCode       = 30
	maxAddress    = 255
	maxLocality   = 100
	maxPostalCode = 20
	maxNotes      = 5000
	maxSearch     = 100

	minYearBuiltOffset = 1800
	maxYearBuiltAhead  = 5

	statusActive            = "active"
	statusInactive          = "inactive"
	statusUnderConstruction = "under_construction"
	statusSold              = "sold"
)

var (
	countryCodeRE = regexp.MustCompile(`^[A-Z]{2}$`)
	statuses      = []string{statusActive, statusInactive, statusUnderConstruction, statusSold}
	statusMessage = "must be one of active, inactive, under_construction, sold"
)

var propertySortColumns = map[string]string{
	"name":        "p.name",
	"code":        "p.code",
	"status":      "p.status",
	"county":      "p.county",
	"created_at":  "p.created_at",
	"updated_at":  "p.updated_at",
	"units_total": "units_total",
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

func (s *Service) List(ctx context.Context, p *rbac.Principal, q ListQuery) (*PropertyListDTO, error) {
	if err := authorize(p, "properties.view"); err != nil {
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
	q.County = strings.TrimSpace(q.County)
	if utf8.RuneCountInString(q.County) > maxLocality {
		fields["county"] = fmt.Sprintf("must be at most %d characters", maxLocality)
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	rows, total, err := s.repo.List(ctx, p.OrgID, p.Scope, q)
	if err != nil {
		return nil, err
	}
	items := make([]PropertySummaryDTO, 0, len(rows))
	for i := range rows {
		items = append(items, propertySummaryDTO(&rows[i]))
	}
	return &PropertyListDTO{Properties: items, Total: total}, nil
}

func (s *Service) Get(ctx context.Context, p *rbac.Principal, id uint64) (*PropertyDetailDTO, error) {
	if err := authorize(p, "properties.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	return s.detail(ctx, p.OrgID, id, p.Scope)
}

func (s *Service) Lookups(ctx context.Context, p *rbac.Principal) (*PropertyLookupsDTO, error) {
	if err := authorize(p, "properties.view"); err != nil {
		return nil, err
	}
	types, err := s.cachedLookups(ctx, propertyTypesKey, s.repo.PropertyTypes)
	if err != nil {
		return nil, err
	}
	amenities, err := s.cachedLookups(ctx, amenitiesKey, s.repo.AmenityLookups)
	if err != nil {
		return nil, err
	}
	return &PropertyLookupsDTO{PropertyTypes: types, Amenities: amenities}, nil
}

func (s *Service) Create(ctx context.Context, p *rbac.Principal, in CreateInput, meta ClientMeta) (*PropertyDetailDTO, error) {
	if err := authorize(p, "properties.create"); err != nil {
		return nil, err
	}
	fields := map[string]string{}
	row := buildPropertyCreate(p.OrgID, in, fields)
	amenityIDs := normalizedAmenityIDs(in.AmenityIDs, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	ownerOK, err := s.repo.OwnerExists(ctx, tx, p.OrgID, row.OwnerID)
	if err != nil {
		return nil, err
	}
	if !ownerOK {
		fields["owner_id"] = "must reference an existing owner"
	}
	typeOK, err := s.repo.PropertyTypeExists(ctx, tx, row.TypeID)
	if err != nil {
		return nil, err
	}
	if !typeOK {
		fields["type_id"] = "must reference an existing property type"
	}
	if len(amenityIDs) > 0 {
		missing, err := s.repo.MissingAmenities(ctx, tx, amenityIDs)
		if err != nil {
			return nil, err
		}
		if len(missing) > 0 {
			fields["amenity_ids"] = "contains unknown amenities"
		}
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}
	if row.Code.Valid {
		taken, err := s.repo.CodeTaken(ctx, tx, p.OrgID, row.Code.String, 0)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, httpx.Conflict("A property with this code already exists")
		}
	}

	id, err := s.repo.Insert(ctx, tx, row)
	if err != nil {
		if isDuplicateEntry(err) {
			return nil, httpx.Conflict("A property with this code already exists")
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
		Action: "properties.created", EntityType: "properties", EntityID: id,
		NewValues: map[string]any{
			"name":      row.Name,
			"code":      nullableValue(row.Code),
			"owner_id":  row.OwnerID,
			"type_id":   row.TypeID,
			"status":    row.Status,
			"amenities": amenityIDs,
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})

	return s.detail(ctx, p.OrgID, id, rbac.Scope{OrgID: p.OrgID, AllProperties: true})
}

func (s *Service) Update(ctx context.Context, p *rbac.Principal, id uint64, in UpdateInput, meta ClientMeta) (*PropertyDetailDTO, error) {
	if err := authorize(p, "properties.update"); err != nil {
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

	originalType := row.TypeID
	fields := map[string]string{}
	oldValues, newValues := applyPropertyPatch(row, in, fields)

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
		ok, err := s.repo.PropertyTypeExists(ctx, tx, row.TypeID)
		if err != nil {
			return nil, err
		}
		if !ok {
			fields["type_id"] = "must reference an existing property type"
		}
	}
	if len(fields) == 0 && in.Code != nil && row.Code.Valid {
		taken, err := s.repo.CodeTaken(ctx, tx, p.OrgID, row.Code.String, id)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, httpx.Conflict("A property with this code already exists")
		}
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	if len(newValues) > 0 {
		if err := s.repo.Update(ctx, tx, row); err != nil {
			if isDuplicateEntry(err) {
				return nil, httpx.Conflict("A property with this code already exists")
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
			Action: "properties.updated", EntityType: "properties", EntityID: id,
			OldValues: oldValues, NewValues: newValues,
			IPAddress: meta.IP, UserAgent: meta.UserAgent,
		})
	}

	return s.detail(ctx, p.OrgID, id, p.Scope)
}

func (s *Service) Delete(ctx context.Context, p *rbac.Principal, id uint64, meta ClientMeta) error {
	if err := authorize(p, "properties.delete"); err != nil {
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
	units, err := s.repo.CountUnits(ctx, tx, p.OrgID, id)
	if err != nil {
		return err
	}
	if units > 0 {
		return httpx.Conflict("Property has units and cannot be deleted")
	}
	if err := s.repo.SoftDelete(ctx, tx, p.OrgID, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "properties.deleted", EntityType: "properties", EntityID: id,
		OldValues: map[string]any{
			"name":     row.Name,
			"code":     nullableValue(row.Code),
			"owner_id": row.OwnerID,
			"type_id":  row.TypeID,
			"status":   row.Status,
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return nil
}

func (s *Service) detail(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*PropertyDetailDTO, error) {
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
	out := propertyDetailDTO(row, amenities)
	return &out, nil
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
			s.log.Debug("cache property lookups", "key", key, "error", err)
		}
	}
	return out, nil
}

func (s *Service) writeAudit(ctx context.Context, e audit.Entry) {
	if err := s.audit.Log(ctx, e); err != nil {
		s.log.Warn("write property audit", "action", e.Action, "org_id", e.OrgID, "error", err)
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

func buildPropertyCreate(orgID uint64, in CreateInput, fields map[string]string) *PropertyRow {
	row := &PropertyRow{
		OrganizationID: orgID,
		OwnerID:        in.OwnerID,
		TypeID:         in.TypeID,
	}
	if in.OwnerID == 0 {
		fields["owner_id"] = "required"
	}
	if in.TypeID == 0 {
		fields["type_id"] = "required"
	}
	row.Name = requiredText("name", in.Name, maxName, fields)
	row.Code = optionalText("code", in.Code, maxCode, fields)
	row.Address = requiredText("address", in.Address, maxAddress, fields)
	row.Estate = optionalText("estate", in.Estate, maxLocality, fields)
	row.Town = optionalText("town", in.Town, maxLocality, fields)
	row.County = requiredText("county", in.County, maxLocality, fields)
	row.PostalCode = optionalText("postal_code", in.PostalCode, maxPostalCode, fields)
	row.Notes = optionalNotes(in.Notes, fields)

	row.CountryCode = "KE"
	if value := strings.ToUpper(strings.TrimSpace(in.CountryCode)); value != "" {
		if countryCodeRE.MatchString(value) {
			row.CountryCode = value
		} else {
			fields["country_code"] = "must be a 2-letter country code"
		}
	}
	row.Status = statusActive
	if value := strings.ToLower(strings.TrimSpace(in.Status)); value != "" {
		if validStatus(value) {
			row.Status = value
		} else {
			fields["status"] = statusMessage
		}
	}
	row.Latitude = coordinateValue("latitude", in.Latitude, -90, 90, fields)
	row.Longitude = coordinateValue("longitude", in.Longitude, -180, 180, fields)
	if fields["latitude"] == "" && fields["longitude"] == "" && row.Latitude.Valid != row.Longitude.Valid {
		if row.Latitude.Valid {
			fields["longitude"] = "must be provided with latitude"
		} else {
			fields["latitude"] = "must be provided with longitude"
		}
	}
	if in.YearBuilt != nil {
		if validYearBuilt(*in.YearBuilt) {
			row.YearBuilt = sql.NullInt64{Int64: int64(*in.YearBuilt), Valid: true}
		} else {
			fields["year_built"] = yearBuiltMessage()
		}
	}
	return row
}

func applyPropertyPatch(row *PropertyRow, in UpdateInput, fields map[string]string) (map[string]any, map[string]any) {
	oldValues := map[string]any{}
	newValues := map[string]any{}
	record := func(name string, before, after any) {
		oldValues[name] = before
		newValues[name] = after
	}

	if in.Name != nil {
		next := requiredText("name", *in.Name, maxName, fields)
		if fields["name"] == "" && next != row.Name {
			record("name", row.Name, next)
			row.Name = next
		}
	}
	applyRequiredTextPatch("address", in.Address, &row.Address, maxAddress, fields, record)
	applyRequiredTextPatch("county", in.County, &row.County, maxLocality, fields, record)
	applyOptionalTextPatch("code", in.Code, &row.Code, maxCode, fields, record)
	applyOptionalTextPatch("estate", in.Estate, &row.Estate, maxLocality, fields, record)
	applyOptionalTextPatch("town", in.Town, &row.Town, maxLocality, fields, record)
	applyOptionalTextPatch("postal_code", in.PostalCode, &row.PostalCode, maxPostalCode, fields, record)

	if in.Notes != nil {
		next := optionalNotes(*in.Notes, fields)
		if fields["notes"] == "" && next != row.Notes {
			record("notes", nullableValue(row.Notes), nullableValue(next))
			row.Notes = next
		}
	}
	if in.CountryCode != nil {
		value := strings.ToUpper(strings.TrimSpace(*in.CountryCode))
		switch {
		case value == "":
			fields["country_code"] = "required"
		case !countryCodeRE.MatchString(value):
			fields["country_code"] = "must be a 2-letter country code"
		case value != row.CountryCode:
			record("country_code", row.CountryCode, value)
			row.CountryCode = value
		}
	}
	if in.Status != nil {
		value := strings.ToLower(strings.TrimSpace(*in.Status))
		switch {
		case value == "":
			fields["status"] = "required"
		case !validStatus(value):
			fields["status"] = statusMessage
		case value != row.Status:
			record("status", row.Status, value)
			row.Status = value
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

	if hasRaw(in.Latitude) {
		next, ok := parseNullableFloat(in.Latitude)
		switch {
		case !ok:
			fields["latitude"] = "must be a number or null"
		case next.Valid && (next.Float64 < -90 || next.Float64 > 90):
			fields["latitude"] = "must be between -90 and 90"
		case next != row.Latitude:
			record("latitude", nullableFloat(row.Latitude), nullableFloat(next))
			row.Latitude = next
		}
	}
	if hasRaw(in.Longitude) {
		next, ok := parseNullableFloat(in.Longitude)
		switch {
		case !ok:
			fields["longitude"] = "must be a number or null"
		case next.Valid && (next.Float64 < -180 || next.Float64 > 180):
			fields["longitude"] = "must be between -180 and 180"
		case next != row.Longitude:
			record("longitude", nullableFloat(row.Longitude), nullableFloat(next))
			row.Longitude = next
		}
	}
	if fields["latitude"] == "" && fields["longitude"] == "" && row.Latitude.Valid != row.Longitude.Valid {
		if row.Latitude.Valid {
			fields["longitude"] = "must be provided with latitude"
		} else {
			fields["latitude"] = "must be provided with longitude"
		}
	}
	if hasRaw(in.YearBuilt) {
		next, ok := parseNullableInt(in.YearBuilt)
		switch {
		case !ok:
			fields["year_built"] = "must be a whole number or null"
		case next.Valid && !validYearBuilt(int(next.Int64)):
			fields["year_built"] = yearBuiltMessage()
		case next != row.YearBuilt:
			record("year_built", nullableInt(row.YearBuilt), nullableInt(next))
			row.YearBuilt = next
		}
	}
	return oldValues, newValues
}

func applyRequiredTextPatch(field string, value *string, dst *string, max int, fields map[string]string, record func(string, any, any)) {
	if value == nil {
		return
	}
	next := requiredText(field, *value, max, fields)
	if fields[field] == "" && next != *dst {
		record(field, *dst, next)
		*dst = next
	}
}

func applyOptionalTextPatch(field string, value *string, dst *sql.NullString, max int, fields map[string]string, record func(string, any, any)) {
	if value == nil {
		return
	}
	next := optionalText(field, *value, max, fields)
	if fields[field] == "" && next != *dst {
		record(field, nullableValue(*dst), nullableValue(next))
		*dst = next
	}
}

func requiredText(field, value string, max int, fields map[string]string) string {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		fields[field] = "required"
	case utf8.RuneCountInString(value) > max:
		fields[field] = fmt.Sprintf("must be at most %d characters", max)
	case hasControl(value):
		fields[field] = "must not contain control characters"
	}
	return value
}

func optionalText(field, value string, max int, fields map[string]string) sql.NullString {
	value = strings.TrimSpace(value)
	if value == "" {
		return sql.NullString{}
	}
	if utf8.RuneCountInString(value) > max {
		fields[field] = fmt.Sprintf("must be at most %d characters", max)
		return sql.NullString{}
	}
	if hasControl(value) {
		fields[field] = "must not contain control characters"
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func optionalNotes(value string, fields map[string]string) sql.NullString {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > maxNotes {
		fields["notes"] = fmt.Sprintf("must be at most %d characters", maxNotes)
		return sql.NullString{}
	}
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func coordinateValue(field string, value *float64, min, max float64, fields map[string]string) sql.NullFloat64 {
	if value == nil {
		return sql.NullFloat64{}
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < min || *value > max {
		fields[field] = fmt.Sprintf("must be between %g and %g", min, max)
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *value, Valid: true}
}

func validYearBuilt(year int) bool {
	return year >= minYearBuiltOffset && year <= time.Now().Year()+maxYearBuiltAhead
}

func yearBuiltMessage() string {
	return fmt.Sprintf("must be between %d and %d", minYearBuiltOffset, time.Now().Year()+maxYearBuiltAhead)
}

func validStatus(value string) bool {
	for _, status := range statuses {
		if value == status {
			return true
		}
	}
	return false
}

func normalizedAmenityIDs(ids []uint64, fields map[string]string) []uint64 {
	seen := make(map[uint64]struct{}, len(ids))
	out := make([]uint64, 0, len(ids))
	positive := true
	for _, id := range ids {
		if id == 0 {
			positive = false
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if !positive {
		fields["amenity_ids"] = "must contain positive integers"
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

func hasRaw(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) > 0
}

func parseNullableFloat(raw json.RawMessage) (sql.NullFloat64, bool) {
	if isJSONNull(raw) {
		return sql.NullFloat64{}, true
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return sql.NullFloat64{}, false
	}
	return sql.NullFloat64{Float64: value, Valid: true}, true
}

func parseNullableInt(raw json.RawMessage) (sql.NullInt64, bool) {
	if isJSONNull(raw) {
		return sql.NullInt64{}, true
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return sql.NullInt64{}, false
	}
	return sql.NullInt64{Int64: value, Valid: true}, true
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func propertySummaryDTO(row *PropertyViewRow) PropertySummaryDTO {
	return PropertySummaryDTO{
		ID:   row.ID,
		Name: row.Name,
		Code: row.Code.String,
		Type: LookupDTO{
			ID:          row.TypeID,
			Name:        row.TypeName,
			Description: row.TypeDescription.String,
		},
		Owner: OwnerDTO{
			ID:      row.OwnerID,
			Name:    row.OwnerName,
			Company: row.OwnerCompany.String,
			Email:   row.OwnerEmail,
			Phone:   row.OwnerPhone.String,
		},
		Address:     row.Address,
		Estate:      row.Estate.String,
		Town:        row.Town.String,
		County:      row.County,
		PostalCode:  row.PostalCode.String,
		CountryCode: row.CountryCode,
		Latitude:    floatPointer(row.Latitude),
		Longitude:   floatPointer(row.Longitude),
		YearBuilt:   intPointer(row.YearBuilt),
		Status:      row.Status,
		Units: UnitCountsDTO{
			Total:    row.UnitsTotal,
			Occupied: row.UnitsOccupied,
			Vacant:   row.UnitsVacant,
		},
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func propertyDetailDTO(row *PropertyViewRow, amenities []LookupRow) PropertyDetailDTO {
	return PropertyDetailDTO{
		PropertySummaryDTO: propertySummaryDTO(row),
		Notes:              row.Notes.String,
		Amenities:          lookupDTOs(amenities),
	}
}

func lookupDTOs(rows []LookupRow) []LookupDTO {
	out := make([]LookupDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, LookupDTO{ID: row.ID, Name: row.Name, Description: row.Description.String})
	}
	return out
}

func floatPointer(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	out := value.Float64
	return &out
}

func intPointer(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	out := int(value.Int64)
	return &out
}

func nullableValue(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func nullableFloat(value sql.NullFloat64) any {
	if value.Valid {
		return value.Float64
	}
	return nil
}

func nullableInt(value sql.NullInt64) any {
	if value.Valid {
		return value.Int64
	}
	return nil
}

func isDuplicateEntry(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
