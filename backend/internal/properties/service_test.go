package properties

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"kodipms/internal/platform/cache"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

func TestBuildPropertyCreate(t *testing.T) {
	t.Run("applies defaults and trims", func(t *testing.T) {
		fields := map[string]string{}
		row := buildPropertyCreate(9, CreateInput{
			OwnerID: 2,
			TypeID:  3,
			Name:    "  Kilimani Heights ",
			Code:    " KD-001 ",
			Address: " Argwings Kodhek Road ",
			County:  " Nairobi ",
		}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected validation errors: %v", fields)
		}
		if row.OrganizationID != 9 || row.OwnerID != 2 || row.TypeID != 3 {
			t.Fatalf("unexpected references: %+v", row)
		}
		if row.Name != "Kilimani Heights" || row.Address != "Argwings Kodhek Road" || row.County != "Nairobi" {
			t.Fatalf("values not trimmed: %+v", row)
		}
		if !row.Code.Valid || row.Code.String != "KD-001" {
			t.Fatalf("code = %+v, want KD-001", row.Code)
		}
		if row.Status != statusActive || row.CountryCode != "KE" {
			t.Fatalf("defaults = %q/%q, want active/KE", row.Status, row.CountryCode)
		}
		if row.Estate.Valid || row.Town.Valid || row.PostalCode.Valid || row.Notes.Valid {
			t.Fatalf("optional fields should be empty: %+v", row)
		}
		if row.Latitude.Valid || row.Longitude.Valid || row.YearBuilt.Valid {
			t.Fatalf("optional numbers should be empty: %+v", row)
		}
	})

	t.Run("requires core fields", func(t *testing.T) {
		fields := map[string]string{}
		_ = buildPropertyCreate(1, CreateInput{}, fields)
		for _, field := range []string{"owner_id", "type_id", "name", "address", "county"} {
			if fields[field] != "required" {
				t.Errorf("%s = %q, want required", field, fields[field])
			}
		}
	})

	base := func() CreateInput {
		return CreateInput{OwnerID: 1, TypeID: 1, Name: "Property", Address: "Road", County: "Nairobi"}
	}
	cases := []struct {
		name    string
		mutate  func(*CreateInput)
		field   string
		message string
	}{
		{"long name", func(in *CreateInput) { in.Name = strings.Repeat("a", maxName+1) }, "name", "150"},
		{"long code", func(in *CreateInput) { in.Code = strings.Repeat("c", maxCode+1) }, "code", "30"},
		{"control character", func(in *CreateInput) { in.Address = "a\x00b" }, "address", "control"},
		{"bad status", func(in *CreateInput) { in.Status = "occupied" }, "status", "active"},
		{"bad country", func(in *CreateInput) { in.CountryCode = "Kenya" }, "country_code", "2-letter"},
		{"latitude out of range", func(in *CreateInput) { in.Latitude = ptrFloat(90.5) }, "latitude", "-90"},
		{"longitude out of range", func(in *CreateInput) { in.Longitude = ptrFloat(-181) }, "longitude", "-180"},
		{"year too old", func(in *CreateInput) { in.YearBuilt = ptrInt(minYearBuiltOffset - 1) }, "year_built", "between"},
		{"notes too long", func(in *CreateInput) { in.Notes = strings.Repeat("n", maxNotes+1) }, "notes", "5000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.mutate(&in)
			fields := map[string]string{}
			_ = buildPropertyCreate(1, in, fields)
			if fields[tc.field] == "" {
				t.Fatalf("missing error for %s: %v", tc.field, fields)
			}
			if !strings.Contains(fields[tc.field], tc.message) {
				t.Fatalf("%s = %q, want substring %q", tc.field, fields[tc.field], tc.message)
			}
		})
	}

	t.Run("coordinates must be paired", func(t *testing.T) {
		in := base()
		in.Latitude = ptrFloat(-1.2921)
		fields := map[string]string{}
		_ = buildPropertyCreate(1, in, fields)
		if fields["longitude"] != "must be provided with latitude" {
			t.Fatalf("longitude = %q", fields["longitude"])
		}

		in = base()
		in.Longitude = ptrFloat(36.8219)
		fields = map[string]string{}
		_ = buildPropertyCreate(1, in, fields)
		if fields["latitude"] != "must be provided with longitude" {
			t.Fatalf("latitude = %q", fields["latitude"])
		}
	})
}

func TestApplyPropertyPatch(t *testing.T) {
	t.Run("no-op is not recorded", func(t *testing.T) {
		row := propertyRowFixture()
		next := " Kilimani Heights "
		fields := map[string]string{}
		oldValues, newValues := applyPropertyPatch(row, UpdateInput{Name: &next}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if len(oldValues) != 0 || len(newValues) != 0 {
			t.Fatalf("no-op recorded: old=%v new=%v", oldValues, newValues)
		}
	})

	t.Run("rename records old and new", func(t *testing.T) {
		row := propertyRowFixture()
		next := "  Kilimani Court "
		fields := map[string]string{}
		oldValues, newValues := applyPropertyPatch(row, UpdateInput{Name: &next}, fields)
		if row.Name != "Kilimani Court" {
			t.Fatalf("name = %q", row.Name)
		}
		if oldValues["name"] != "Kilimani Heights" || newValues["name"] != "Kilimani Court" {
			t.Fatalf("audit = %v -> %v", oldValues, newValues)
		}
	})

	t.Run("empty optional strings clear values", func(t *testing.T) {
		row := propertyRowFixture()
		empty := ""
		fields := map[string]string{}
		oldValues, newValues := applyPropertyPatch(row, UpdateInput{
			Code:       &empty,
			Estate:     &empty,
			PostalCode: &empty,
			Notes:      &empty,
		}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if row.Code.Valid || row.Estate.Valid || row.PostalCode.Valid || row.Notes.Valid {
			t.Fatalf("values not cleared: %+v", row)
		}
		if oldValues["code"] != "KD-001" || newValues["code"] != nil {
			t.Fatalf("code audit = %v -> %v", oldValues["code"], newValues["code"])
		}
		if oldValues["notes"] != "Gate code 1234" || newValues["notes"] != nil {
			t.Fatalf("notes audit = %v -> %v", oldValues["notes"], newValues["notes"])
		}
	})

	t.Run("required text cannot be cleared", func(t *testing.T) {
		row := propertyRowFixture()
		empty := "  "
		fields := map[string]string{}
		applyPropertyPatch(row, UpdateInput{Address: &empty}, fields)
		if fields["address"] != "required" {
			t.Fatalf("address = %q", fields["address"])
		}
	})

	t.Run("status and country are validated", func(t *testing.T) {
		row := propertyRowFixture()
		status := "occupied"
		country := "Kenya"
		fields := map[string]string{}
		applyPropertyPatch(row, UpdateInput{Status: &status, CountryCode: &country}, fields)
		if fields["status"] != statusMessage {
			t.Fatalf("status = %q", fields["status"])
		}
		if fields["country_code"] != "must be a 2-letter country code" {
			t.Fatalf("country_code = %q", fields["country_code"])
		}
	})

	t.Run("type change is recorded", func(t *testing.T) {
		row := propertyRowFixture()
		next := uint64(7)
		fields := map[string]string{}
		oldValues, newValues := applyPropertyPatch(row, UpdateInput{TypeID: &next}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if row.TypeID != 7 || oldValues["type_id"] != uint64(3) || newValues["type_id"] != uint64(7) {
			t.Fatalf("type audit = %v -> %v, row %d", oldValues["type_id"], newValues["type_id"], row.TypeID)
		}
	})

	t.Run("zero type is rejected", func(t *testing.T) {
		row := propertyRowFixture()
		zero := uint64(0)
		fields := map[string]string{}
		applyPropertyPatch(row, UpdateInput{TypeID: &zero}, fields)
		if fields["type_id"] != "required" {
			t.Fatalf("type_id = %q", fields["type_id"])
		}
	})

	t.Run("bad coordinate payloads", func(t *testing.T) {
		row := propertyRowFixture()
		fields := map[string]string{}
		applyPropertyPatch(row, UpdateInput{Latitude: json.RawMessage(`"north"`)}, fields)
		if fields["latitude"] != "must be a number or null" {
			t.Fatalf("latitude = %q", fields["latitude"])
		}

		row = propertyRowFixture()
		fields = map[string]string{}
		applyPropertyPatch(row, UpdateInput{Longitude: json.RawMessage(`181`)}, fields)
		if !strings.Contains(fields["longitude"], "between -180 and 180") {
			t.Fatalf("longitude = %q", fields["longitude"])
		}
	})

	t.Run("clearing only latitude breaks the pair", func(t *testing.T) {
		row := propertyRowFixture()
		fields := map[string]string{}
		applyPropertyPatch(row, UpdateInput{Latitude: json.RawMessage(`null`)}, fields)
		if fields["latitude"] != "must be provided with longitude" {
			t.Fatalf("latitude = %q", fields["latitude"])
		}
	})

	t.Run("clearing both coordinates is allowed", func(t *testing.T) {
		row := propertyRowFixture()
		fields := map[string]string{}
		oldValues, newValues := applyPropertyPatch(row, UpdateInput{
			Latitude:  json.RawMessage(`null`),
			Longitude: json.RawMessage(`null`),
		}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if row.Latitude.Valid || row.Longitude.Valid {
			t.Fatalf("coordinates still set: %+v", row)
		}
		if oldValues["latitude"] != -1.2921 || newValues["latitude"] != nil {
			t.Fatalf("latitude audit = %v -> %v", oldValues["latitude"], newValues["latitude"])
		}
	})

	t.Run("bad year payloads", func(t *testing.T) {
		row := propertyRowFixture()
		fields := map[string]string{}
		applyPropertyPatch(row, UpdateInput{YearBuilt: json.RawMessage(`1990.5`)}, fields)
		if fields["year_built"] != "must be a whole number or null" {
			t.Fatalf("year_built = %q", fields["year_built"])
		}

		row = propertyRowFixture()
		fields = map[string]string{}
		applyPropertyPatch(row, UpdateInput{YearBuilt: json.RawMessage(`1700`)}, fields)
		if !strings.Contains(fields["year_built"], "between") {
			t.Fatalf("year_built = %q", fields["year_built"])
		}
	})

	t.Run("year can be cleared", func(t *testing.T) {
		row := propertyRowFixture()
		fields := map[string]string{}
		oldValues, newValues := applyPropertyPatch(row, UpdateInput{YearBuilt: json.RawMessage(`null`)}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if row.YearBuilt.Valid || oldValues["year_built"] != int64(2018) || newValues["year_built"] != nil {
			t.Fatalf("year audit = %v -> %v", oldValues["year_built"], newValues["year_built"])
		}
	})

	t.Run("notes length is bounded", func(t *testing.T) {
		row := propertyRowFixture()
		notes := strings.Repeat("n", maxNotes+1)
		fields := map[string]string{}
		applyPropertyPatch(row, UpdateInput{Notes: &notes}, fields)
		if !strings.Contains(fields["notes"], "5000") {
			t.Fatalf("notes = %q", fields["notes"])
		}
	})
}

func TestRawNumberParsing(t *testing.T) {
	if hasRaw(nil) {
		t.Fatal("nil raw should be treated as omitted")
	}
	if !hasRaw(json.RawMessage(" null ")) {
		t.Fatal("null raw should be treated as provided")
	}
	value, ok := parseNullableFloat(json.RawMessage(`null`))
	if !ok || value.Valid {
		t.Fatalf("null float = %+v, %v", value, ok)
	}
	value, ok = parseNullableFloat(json.RawMessage(`-1.2921`))
	if !ok || !value.Valid || value.Float64 != -1.2921 {
		t.Fatalf("float = %+v, %v", value, ok)
	}
	if _, ok := parseNullableFloat(json.RawMessage(`"1.2"`)); ok {
		t.Fatal("string should not parse as float")
	}
	integer, ok := parseNullableInt(json.RawMessage(`2018`))
	if !ok || !integer.Valid || integer.Int64 != 2018 {
		t.Fatalf("int = %+v, %v", integer, ok)
	}
	if _, ok := parseNullableInt(json.RawMessage(`2018.5`)); ok {
		t.Fatal("fraction should not parse as int")
	}
}

func TestNormalizedAmenityIDs(t *testing.T) {
	fields := map[string]string{}
	got := normalizedAmenityIDs([]uint64{3, 1, 3, 2}, fields)
	if !reflect.DeepEqual(got, []uint64{3, 1, 2}) {
		t.Fatalf("ids = %v", got)
	}
	if len(fields) != 0 {
		t.Fatalf("unexpected errors: %v", fields)
	}

	got = normalizedAmenityIDs([]uint64{1, 0, 2}, fields)
	if !reflect.DeepEqual(got, []uint64{1, 2}) {
		t.Fatalf("ids = %v", got)
	}
	if fields["amenity_ids"] != "must contain positive integers" {
		t.Fatalf("amenity_ids = %q", fields["amenity_ids"])
	}
}

func TestSameIDSet(t *testing.T) {
	if !sameIDSet(nil, nil) {
		t.Fatal("empty sets should match")
	}
	if !sameIDSet([]uint64{1, 2, 3}, []uint64{3, 1, 2}) {
		t.Fatal("same members should match")
	}
	if sameIDSet([]uint64{1, 2}, []uint64{1}) {
		t.Fatal("different sizes should not match")
	}
	if sameIDSet([]uint64{1, 2}, []uint64{1, 3}) {
		t.Fatal("different members should not match")
	}
}

func TestPropertySummaryDTO(t *testing.T) {
	row := propertyViewFixture()
	out := propertySummaryDTO(row)
	if out.ID != 10 || out.Name != "Kilimani Heights" || out.Code != "KD-001" {
		t.Fatalf("summary = %+v", out)
	}
	if out.Type.ID != 3 || out.Type.Name != "Apartment Block" || out.Type.Description != "Multi-unit residential" {
		t.Fatalf("type = %+v", out.Type)
	}
	if out.Owner.ID != 2 || out.Owner.Name != "Jane Wanjiku" || out.Owner.Company != "Wanjiku Holdings" {
		t.Fatalf("owner = %+v", out.Owner)
	}
	if out.Owner.Email != "jane@example.com" || out.Owner.Phone != "+254712345678" {
		t.Fatalf("owner contact = %+v", out.Owner)
	}
	if out.Latitude == nil || *out.Latitude != -1.2921 || out.Longitude == nil || *out.Longitude != 36.8219 {
		t.Fatalf("coordinates = %v/%v", out.Latitude, out.Longitude)
	}
	if out.YearBuilt == nil || *out.YearBuilt != 2018 {
		t.Fatalf("year_built = %v", out.YearBuilt)
	}
	if out.Units != (UnitCountsDTO{Total: 4, Occupied: 1, Vacant: 3}) {
		t.Fatalf("units = %+v", out.Units)
	}
	if !out.CreatedAt.Equal(row.CreatedAt) || !out.UpdatedAt.Equal(row.UpdatedAt) {
		t.Fatalf("timestamps = %v/%v", out.CreatedAt, out.UpdatedAt)
	}
}

func TestPropertyDetailDTO(t *testing.T) {
	row := propertyViewFixture()
	out := propertyDetailDTO(row, []LookupRow{
		{ID: 1, Name: "Parking", Description: sql.NullString{String: "Secure parking", Valid: true}},
		{ID: 2, Name: "Borehole"},
	})
	if out.Notes != "Gate code 1234" {
		t.Fatalf("notes = %q", out.Notes)
	}
	if len(out.Amenities) != 2 || out.Amenities[0].Name != "Parking" || out.Amenities[0].Description != "Secure parking" {
		t.Fatalf("amenities = %+v", out.Amenities)
	}
	if out.Amenities[1].Description != "" {
		t.Fatalf("amenities = %+v", out.Amenities)
	}
	if names := lookupDTOs(nil); names == nil || len(names) != 0 {
		t.Fatalf("empty lookups = %#v", names)
	}
}

func TestPropertyDetailJSON(t *testing.T) {
	row := propertyViewFixture()
	row.Latitude = sql.NullFloat64{}
	row.Longitude = sql.NullFloat64{}
	row.YearBuilt = sql.NullInt64{}
	detail := propertyDetailDTO(row, nil)
	b, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "name", "type", "owner", "units", "amenities", "created_at"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing key %q in %s", key, b)
		}
	}
	if _, ok := decoded["PropertySummaryDTO"]; ok {
		t.Fatalf("embedded struct was not flattened: %s", b)
	}
	if decoded["latitude"] != nil || decoded["year_built"] != nil {
		t.Fatalf("nullable fields = %v/%v", decoded["latitude"], decoded["year_built"])
	}
	if amenities, ok := decoded["amenities"].([]any); !ok || len(amenities) != 0 {
		t.Fatalf("amenities = %#v", decoded["amenities"])
	}
}

func TestListValidation(t *testing.T) {
	svc := NewService(nil, nil, nil, nil)
	p := principalWith("properties.view")
	cases := []struct {
		name  string
		query ListQuery
		field string
	}{
		{"long search", ListQuery{Search: strings.Repeat("a", maxSearch+1)}, "q"},
		{"bad status", ListQuery{Status: "occupied"}, "status"},
		{"long county", ListQuery{County: strings.Repeat("c", maxLocality+1)}, "county"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.List(context.Background(), p, tc.query)
			var e *httpx.Err
			if !errors.As(err, &e) || e.Status != 422 {
				t.Fatalf("err = %v, want validation", err)
			}
			if e.Fields[tc.field] == "" {
				t.Fatalf("missing %s in %v", tc.field, e.Fields)
			}
		})
	}
}

func TestServiceAuthorization(t *testing.T) {
	svc := NewService(nil, nil, nil, nil)
	ctx := context.Background()
	noPerms := principalWith()
	noOrg := &rbac.Principal{Perms: map[string]struct{}{"properties.view": {}}}
	cases := []struct {
		name   string
		status int
		call   func() error
	}{
		{"list without principal", 401, func() error {
			_, err := svc.List(ctx, nil, ListQuery{})
			return err
		}},
		{"list without org", 401, func() error {
			_, err := svc.List(ctx, noOrg, ListQuery{})
			return err
		}},
		{"list without permission", 403, func() error {
			_, err := svc.List(ctx, noPerms, ListQuery{})
			return err
		}},
		{"get without permission", 403, func() error {
			_, err := svc.Get(ctx, noPerms, 1)
			return err
		}},
		{"lookups without permission", 403, func() error {
			_, err := svc.Lookups(ctx, noPerms)
			return err
		}},
		{"create without permission", 403, func() error {
			_, err := svc.Create(ctx, noPerms, CreateInput{}, ClientMeta{})
			return err
		}},
		{"update without permission", 403, func() error {
			_, err := svc.Update(ctx, noPerms, 1, UpdateInput{}, ClientMeta{})
			return err
		}},
		{"delete without permission", 403, func() error {
			return svc.Delete(ctx, noPerms, 1, ClientMeta{})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var e *httpx.Err
			err := tc.call()
			if !errors.As(err, &e) || e.Status != tc.status {
				t.Fatalf("err = %v, want status %d", err, tc.status)
			}
		})
	}
}

func TestCachedLookups(t *testing.T) {
	store := newMemStore()
	svc := NewService(nil, store, nil, discardLogger())
	calls := 0
	load := func(context.Context) ([]LookupRow, error) {
		calls++
		return []LookupRow{{
			ID:          1,
			Name:        "Apartment Block",
			Description: sql.NullString{String: "Multi-unit residential", Valid: true},
		}}, nil
	}

	first, err := svc.cachedLookups(context.Background(), "lookup:test", load)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.cachedLookups(context.Background(), "lookup:test", load)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("loader called %d times, want 1", calls)
	}
	if len(first) != 1 || first[0].Name != "Apartment Block" || second[0].ID != 1 {
		t.Fatalf("lookups = %+v / %+v", first, second)
	}

	store.values["lookup:corrupt"] = "{not json"
	calls = 0
	third, err := svc.cachedLookups(context.Background(), "lookup:corrupt", load)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(third) != 1 {
		t.Fatalf("corrupt cache fallback = %+v (calls %d)", third, calls)
	}
}

func TestIsDuplicateEntry(t *testing.T) {
	if !isDuplicateEntry(&mysql.MySQLError{Number: 1062}) {
		t.Fatal("1062 should be recognised")
	}
	if !isDuplicateEntry(fmt.Errorf("insert: %w", &mysql.MySQLError{Number: 1062})) {
		t.Fatal("wrapped 1062 should be recognised")
	}
	if isDuplicateEntry(errors.New("boom")) {
		t.Fatal("plain error should not be recognised")
	}
	if isDuplicateEntry(&mysql.MySQLError{Number: 1452}) {
		t.Fatal("other mysql errors should not be recognised")
	}
}

func TestValidYearBuilt(t *testing.T) {
	year := time.Now().Year()
	if !validYearBuilt(minYearBuiltOffset) || !validYearBuilt(year) {
		t.Fatal("known years should be accepted")
	}
	if validYearBuilt(minYearBuiltOffset-1) || validYearBuilt(year+maxYearBuiltAhead+1) {
		t.Fatal("out of range years should be rejected")
	}
}

func principalWith(perms ...string) *rbac.Principal {
	set := make(map[string]struct{}, len(perms))
	for _, perm := range perms {
		set[perm] = struct{}{}
	}
	return &rbac.Principal{OrgID: 1, Perms: set}
}

func propertyRowFixture() *PropertyRow {
	return &PropertyRow{
		ID:             10,
		OrganizationID: 1,
		OwnerID:        2,
		TypeID:         3,
		Name:           "Kilimani Heights",
		Code:           sql.NullString{String: "KD-001", Valid: true},
		Address:        "Argwings Kodhek Road",
		Estate:         sql.NullString{String: "Kilimani", Valid: true},
		Town:           sql.NullString{String: "Nairobi", Valid: true},
		County:         "Nairobi",
		PostalCode:     sql.NullString{String: "00100", Valid: true},
		CountryCode:    "KE",
		Latitude:       sql.NullFloat64{Float64: -1.2921, Valid: true},
		Longitude:      sql.NullFloat64{Float64: 36.8219, Valid: true},
		YearBuilt:      sql.NullInt64{Int64: 2018, Valid: true},
		Status:         statusActive,
		Notes:          sql.NullString{String: "Gate code 1234", Valid: true},
		CreatedAt:      time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
	}
}

func propertyViewFixture() *PropertyViewRow {
	return &PropertyViewRow{
		PropertyRow:     *propertyRowFixture(),
		TypeName:        "Apartment Block",
		TypeDescription: sql.NullString{String: "Multi-unit residential", Valid: true},
		OwnerName:       "Jane Wanjiku",
		OwnerCompany:    sql.NullString{String: "Wanjiku Holdings", Valid: true},
		OwnerEmail:      "jane@example.com",
		OwnerPhone:      sql.NullString{String: "+254712345678", Valid: true},
		UnitsTotal:      4,
		UnitsOccupied:   1,
		UnitsVacant:     3,
	}
}

func ptrFloat(value float64) *float64 { return &value }

func ptrInt(value int) *int { return &value }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type memStore struct {
	values map[string]string
}

func newMemStore() *memStore {
	return &memStore{values: make(map[string]string)}
}

func (s *memStore) Get(_ context.Context, key string) (string, error) {
	value, ok := s.values[key]
	if !ok {
		return "", cache.ErrMiss
	}
	return value, nil
}

func (s *memStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	s.values[key] = value
	return nil
}

func (s *memStore) Del(_ context.Context, keys ...string) error {
	for _, key := range keys {
		delete(s.values, key)
	}
	return nil
}

func (s *memStore) Incr(context.Context, string) (int64, error) { return 0, nil }

func (s *memStore) IncrTTL(context.Context, string, time.Duration) (int64, error) {
	return 0, nil
}

func (s *memStore) Expire(context.Context, string, time.Duration) error { return nil }

func (s *memStore) Close() error { return nil }
