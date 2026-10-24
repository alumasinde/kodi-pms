package units

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"kodipms/internal/platform/cache"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

func TestBuildUnitCreate(t *testing.T) {
	t.Run("applies defaults and trims", func(t *testing.T) {
		fields := map[string]string{}
		row := buildUnitCreate(9, CreateInput{
			PropertyID: 4,
			TypeID:     2,
			Number:     " B-101 ",
			Floor:      ptrInt(3),
			Bedrooms:   ptrInt(2),
			Bathrooms:  ptrFloat(1.5),
			SquareFeet: ptrInt(850),
			Rent:       ptrFloat(45000),
		}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected validation errors: %v", fields)
		}
		if row.OrganizationID != 9 || row.PropertyID != 4 || row.TypeID != 2 {
			t.Fatalf("references = %+v", row)
		}
		if row.Number != "B-101" {
			t.Fatalf("number = %q", row.Number)
		}
		if row.Status != statusVacant {
			t.Fatalf("status = %q, want vacant", row.Status)
		}
		if row.Floor.Int64 != 3 || !row.Floor.Valid {
			t.Fatalf("floor = %+v", row.Floor)
		}
		if row.Bedrooms != 2 || row.Bathrooms != 1.5 || row.Rent != 45000 {
			t.Fatalf("template = %+v", row)
		}
		if row.SquareFeet.Int64 != 850 || !row.SquareFeet.Valid {
			t.Fatalf("square_feet = %+v", row.SquareFeet)
		}
	})

	t.Run("numeric defaults apply when omitted", func(t *testing.T) {
		fields := map[string]string{}
		row := buildUnitCreate(1, CreateInput{PropertyID: 1, TypeID: 1, Number: "1"}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected validation errors: %v", fields)
		}
		if row.Bedrooms != 0 || row.Bathrooms != 1 || row.Rent != 0 {
			t.Fatalf("defaults = %+v", row)
		}
		if row.Floor.Valid || row.SquareFeet.Valid {
			t.Fatalf("optional numbers should stay null: %+v", row)
		}
	})

	t.Run("requires core fields", func(t *testing.T) {
		fields := map[string]string{}
		_ = buildUnitCreate(1, CreateInput{}, fields)
		for _, field := range []string{"property_id", "type_id", "number"} {
			if fields[field] != "required" {
				t.Errorf("%s = %q, want required", field, fields[field])
			}
		}
	})

	t.Run("occupied comes from a lease", func(t *testing.T) {
		fields := map[string]string{}
		_ = buildUnitCreate(1, CreateInput{PropertyID: 1, TypeID: 1, Number: "1", Status: "Occupied"}, fields)
		if fields["status"] != occupiedMessage {
			t.Fatalf("status = %q", fields["status"])
		}
	})

	t.Run("reserved and maintenance are allowed", func(t *testing.T) {
		for _, status := range []string{statusVacant, statusReserved, statusMaintenance, statusInactive} {
			fields := map[string]string{}
			row := buildUnitCreate(1, CreateInput{PropertyID: 1, TypeID: 1, Number: "1", Status: strings.ToUpper(status)}, fields)
			if len(fields) != 0 || row.Status != status {
				t.Fatalf("status %q -> %q fields %v", status, row.Status, fields)
			}
		}
	})

	base := func() CreateInput {
		return CreateInput{PropertyID: 1, TypeID: 1, Number: "12"}
	}
	cases := []struct {
		name    string
		mutate  func(*CreateInput)
		field   string
		message string
	}{
		{"long number", func(in *CreateInput) { in.Number = strings.Repeat("9", maxNumber+1) }, "number", "20"},
		{"control character", func(in *CreateInput) { in.Number = "1\x002" }, "number", "control"},
		{"bad status", func(in *CreateInput) { in.Status = "sold" }, "status", "vacant"},
		{"floor too low", func(in *CreateInput) { in.Floor = ptrInt(minFloor - 1) }, "floor", "between"},
		{"floor too high", func(in *CreateInput) { in.Floor = ptrInt(maxFloor + 1) }, "floor", "between"},
		{"negative bedrooms", func(in *CreateInput) { in.Bedrooms = ptrInt(-1) }, "bedrooms", "between"},
		{"too many bedrooms", func(in *CreateInput) { in.Bedrooms = ptrInt(maxBedrooms + 1) }, "bedrooms", "50"},
		{"negative bathrooms", func(in *CreateInput) { in.Bathrooms = ptrFloat(-0.5) }, "bathrooms", "between"},
		{"too many bathrooms", func(in *CreateInput) { in.Bathrooms = ptrFloat(maxBathrooms + 1) }, "bathrooms", "99.9"},
		{"zero square feet", func(in *CreateInput) { in.SquareFeet = ptrInt(0) }, "square_feet", "between 1"},
		{"huge square feet", func(in *CreateInput) { in.SquareFeet = ptrInt(maxSquareFeet + 1) }, "square_feet", "1000000"},
		{"negative rent", func(in *CreateInput) { in.Rent = ptrFloat(-1) }, "rent", "between"},
		{"huge rent", func(in *CreateInput) { in.Rent = ptrFloat(maxRent + 1000) }, "rent", "between"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.mutate(&in)
			fields := map[string]string{}
			_ = buildUnitCreate(1, in, fields)
			if fields[tc.field] == "" {
				t.Fatalf("missing error for %s: %v", tc.field, fields)
			}
			if !strings.Contains(fields[tc.field], tc.message) {
				t.Fatalf("%s = %q, want substring %q", tc.field, fields[tc.field], tc.message)
			}
		})
	}
}

func TestGenerateNumbers(t *testing.T) {
	t.Run("prefix, counter and padding", func(t *testing.T) {
		fields := map[string]string{}
		got := generateNumbers(GenerateInput{
			Prefix: "B", FirstNumber: ptrInt(101), Count: 3, Pad: ptrInt(3),
		}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if !reflect.DeepEqual(got, []string{"B101", "B102", "B103"}) {
			t.Fatalf("numbers = %v", got)
		}
	})

	t.Run("no padding and no prefix", func(t *testing.T) {
		fields := map[string]string{}
		got := generateNumbers(GenerateInput{Count: 2}, fields)
		if len(fields) != 0 || !reflect.DeepEqual(got, []string{"1", "2"}) {
			t.Fatalf("numbers = %v fields = %v", got, fields)
		}
	})

	t.Run("bath-style numbers", func(t *testing.T) {
		fields := map[string]string{}
		got := generateNumbers(GenerateInput{Prefix: "BA", FirstNumber: ptrInt(1), Count: 4, Pad: ptrInt(2)}, fields)
		if len(fields) != 0 || !reflect.DeepEqual(got, []string{"BA01", "BA02", "BA03", "BA04"}) {
			t.Fatalf("numbers = %v fields = %v", got, fields)
		}
	})

	cases := []struct {
		name    string
		input   GenerateInput
		message string
	}{
		{"zero count", GenerateInput{Count: 0}, "between 1 and 200"},
		{"too many", GenerateInput{Count: maxGenerateCount + 1}, "between 1 and 200"},
		{"long prefix", GenerateInput{Count: 2, Prefix: strings.Repeat("b", maxPrefix+1)}, "at most 10 characters"},
		{"wildcard prefix", GenerateInput{Count: 2, Prefix: "A_1"}, "wildcards"},
		{"negative start", GenerateInput{Count: 2, FirstNumber: ptrInt(-3)}, "negative"},
		{"negative pad", GenerateInput{Count: 2, Pad: ptrInt(-1)}, "between 0 and 5"},
		{"huge pad", GenerateInput{Count: 2, Pad: ptrInt(maxPad + 1)}, "between 0 and 5"},
		{"numbers too long", GenerateInput{Count: 2, Prefix: strings.Repeat("b", maxPrefix), FirstNumber: ptrInt(999999999999999)}, "longer than 20"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]string{}
			if got := generateNumbers(tc.input, fields); got != nil {
				t.Fatalf("numbers = %v, want none", got)
			}
			if len(fields) == 0 {
				t.Fatal("expected a validation error")
			}
			found := false
			for _, message := range fields {
				if strings.Contains(message, tc.message) {
					found = true
				}
			}
			if !found {
				t.Fatalf("fields = %v, want substring %q", fields, tc.message)
			}
		})
	}
}

func TestBuildUnitBatch(t *testing.T) {
	fields := map[string]string{}
	numbers := []string{"B01", "B02"}
	rows := buildUnitBatch(7, GenerateInput{
		PropertyID: 3, TypeID: 5, Bedrooms: ptrInt(1), Rent: ptrFloat(20000.5),
	}, numbers, fields)
	if len(fields) != 0 {
		t.Fatalf("unexpected errors: %v", fields)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for i, row := range rows {
		if row.Number != numbers[i] || row.OrganizationID != 7 || row.PropertyID != 3 || row.TypeID != 5 {
			t.Fatalf("row %d = %+v", i, row)
		}
		if row.Status != statusVacant || row.Bedrooms != 1 || row.Rent != 20000.5 {
			t.Fatalf("row %d template = %+v", i, row)
		}
	}
}

func TestGenerateStopsBeforeDatabase(t *testing.T) {
	svc := NewService(nil, nil, nil, discardLogger())
	p := principalWith("units.create")
	_, err := svc.Generate(context.Background(), p, GenerateInput{Count: 0}, ClientMeta{})
	var e *httpx.Err
	if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity {
		t.Fatalf("err = %v, want validation", err)
	}
	if e.Fields["count"] == "" || e.Fields["property_id"] != "required" || e.Fields["type_id"] != "required" {
		t.Fatalf("fields = %v", e.Fields)
	}
}

func TestApplyUnitPatch(t *testing.T) {
	t.Run("no-op is not recorded", func(t *testing.T) {
		row := unitRowFixture()
		next := " 101 "
		status := "vacant"
		fields := map[string]string{}
		oldValues, newValues := applyUnitPatch(row, UpdateInput{Number: &next, Status: &status}, fields, false)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if len(oldValues) != 0 || len(newValues) != 0 {
			t.Fatalf("no-op recorded: old=%v new=%v", oldValues, newValues)
		}
	})

	t.Run("rename and retype are recorded", func(t *testing.T) {
		row := unitRowFixture()
		next := " 10B "
		typeID := uint64(7)
		fields := map[string]string{}
		oldValues, newValues := applyUnitPatch(row, UpdateInput{Number: &next, TypeID: &typeID}, fields, false)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if row.Number != "10B" || row.TypeID != 7 {
			t.Fatalf("row = %+v", row)
		}
		if oldValues["number"] != "101" || newValues["number"] != "10B" {
			t.Fatalf("number audit = %v -> %v", oldValues["number"], newValues["number"])
		}
		if oldValues["type_id"] != uint64(2) || newValues["type_id"] != uint64(7) {
			t.Fatalf("type audit = %v -> %v", oldValues["type_id"], newValues["type_id"])
		}
	})

	t.Run("zero type is rejected", func(t *testing.T) {
		row := unitRowFixture()
		zero := uint64(0)
		fields := map[string]string{}
		applyUnitPatch(row, UpdateInput{TypeID: &zero}, fields, false)
		if fields["type_id"] != "required" {
			t.Fatalf("type_id = %q", fields["type_id"])
		}
	})

	t.Run("blank or invalid number is rejected", func(t *testing.T) {
		row := unitRowFixture()
		blank := "   "
		fields := map[string]string{}
		applyUnitPatch(row, UpdateInput{Number: &blank}, fields, false)
		if fields["number"] != "required" || row.Number != "101" {
			t.Fatalf("number = %q row = %q", fields["number"], row.Number)
		}

		row = unitRowFixture()
		long := strings.Repeat("9", maxNumber+1)
		fields = map[string]string{}
		applyUnitPatch(row, UpdateInput{Number: &long}, fields, false)
		if fields["number"] == "" {
			t.Fatalf("missing number error: %v", fields)
		}
	})

	t.Run("money and sizes are rounded and bounded", func(t *testing.T) {
		row := unitRowFixture()
		fields := map[string]string{}
		oldValues, newValues := applyUnitPatch(row, UpdateInput{
			Bedrooms:  ptrInt(3),
			Bathrooms: ptrFloat(2.25),
			Rent:      ptrFloat(60500.016),
		}, fields, false)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if row.Bedrooms != 3 || row.Bathrooms != 2.3 || row.Rent != 60500.02 {
			t.Fatalf("row = %+v", row)
		}
		if newValues["bathrooms"] != 2.3 || oldValues["bathrooms"] != 1.0 {
			t.Fatalf("bathrooms audit = %v -> %v", oldValues["bathrooms"], newValues["bathrooms"])
		}
		if newValues["rent"] != 60500.02 {
			t.Fatalf("rent audit = %v", newValues["rent"])
		}

		row = unitRowFixture()
		fields = map[string]string{}
		applyUnitPatch(row, UpdateInput{Rent: ptrFloat(maxRent + 1000)}, fields, false)
		if fields["rent"] == "" {
			t.Fatalf("missing rent error: %v", fields)
		}
	})

	t.Run("status rules", func(t *testing.T) {
		vacant := statusVacant
		occupied := statusOccupied
		maintenance := " Maintenance "
		blank := " "
		sold := "sold"

		row := unitRowFixture()
		fields := map[string]string{}
		oldValues, newValues := applyUnitPatch(row, UpdateInput{Status: &maintenance}, fields, false)
		if len(fields) != 0 || row.Status != statusMaintenance {
			t.Fatalf("row = %+v fields = %v", row, fields)
		}
		if oldValues["status"] != vacant || newValues["status"] != statusMaintenance {
			t.Fatalf("status audit = %v -> %v", oldValues["status"], newValues["status"])
		}

		row = unitRowFixture()
		fields = map[string]string{}
		applyUnitPatch(row, UpdateInput{Status: &occupied}, fields, false)
		if fields["status"] != occupiedMessage {
			t.Fatalf("status = %q, want the lease-owned message", fields["status"])
		}

		row = unitRowFixture()
		row.Status = statusOccupied
		fields = map[string]string{}
		applyUnitPatch(row, UpdateInput{Status: &vacant}, fields, true)
		if fields["status"] != leaseHeld {
			t.Fatalf("status = %q, want the lease-held message", fields["status"])
		}

		row = unitRowFixture()
		row.Status = statusOccupied
		fields = map[string]string{}
		oldValues, newValues = applyUnitPatch(row, UpdateInput{Status: &occupied}, fields, true)
		if len(fields) != 0 || len(newValues) != 0 || len(oldValues) != 0 {
			t.Fatalf("occupied unit keeping occupied should be a no-op: %v %v %v", fields, oldValues, newValues)
		}

		row = unitRowFixture()
		fields = map[string]string{}
		applyUnitPatch(row, UpdateInput{Status: &blank}, fields, false)
		if fields["status"] != "required" {
			t.Fatalf("status = %q", fields["status"])
		}

		row = unitRowFixture()
		fields = map[string]string{}
		applyUnitPatch(row, UpdateInput{Status: &sold}, fields, false)
		if fields["status"] != statusMessage {
			t.Fatalf("status = %q", fields["status"])
		}
	})

	t.Run("nullable numbers clear and reject bad payloads", func(t *testing.T) {
		row := unitRowFixture()
		fields := map[string]string{}
		oldValues, newValues := applyUnitPatch(row, UpdateInput{
			Floor:      json.RawMessage(`null`),
			SquareFeet: json.RawMessage(`null`),
		}, fields, false)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if row.Floor.Valid || row.SquareFeet.Valid {
			t.Fatalf("values not cleared: %+v", row)
		}
		if oldValues["floor"] != int64(3) || newValues["floor"] != nil {
			t.Fatalf("floor audit = %v -> %v", oldValues["floor"], newValues["floor"])
		}
		if oldValues["square_feet"] != int64(850) || newValues["square_feet"] != nil {
			t.Fatalf("square_feet audit = %v -> %v", oldValues["square_feet"], newValues["square_feet"])
		}

		row = unitRowFixture()
		fields = map[string]string{}
		applyUnitPatch(row, UpdateInput{Floor: json.RawMessage(`"third"`)}, fields, false)
		if fields["floor"] != "must be a whole number or null" {
			t.Fatalf("floor = %q", fields["floor"])
		}

		row = unitRowFixture()
		fields = map[string]string{}
		applyUnitPatch(row, UpdateInput{SquareFeet: json.RawMessage(`0`)}, fields, false)
		if fields["square_feet"] == "" {
			t.Fatalf("missing square_feet error: %v", fields)
		}

		row = unitRowFixture()
		fields = map[string]string{}
		oldValues, newValues = applyUnitPatch(row, UpdateInput{Floor: json.RawMessage(`-1`)}, fields, false)
		if len(fields) != 0 || row.Floor.Int64 != -1 {
			t.Fatalf("basement floor should apply: row %+v fields %v", row, fields)
		}
		if oldValues["floor"] != int64(3) || newValues["floor"] != int64(-1) {
			t.Fatalf("floor audit = %v -> %v", oldValues["floor"], newValues["floor"])
		}
	})

	t.Run("omitted fields are untouched", func(t *testing.T) {
		row := unitRowFixture()
		fields := map[string]string{}
		oldValues, newValues := applyUnitPatch(row, UpdateInput{}, fields, false)
		if len(fields) != 0 || len(oldValues) != 0 || len(newValues) != 0 {
			t.Fatalf("empty patch: %v %v %v", fields, oldValues, newValues)
		}
		if row.Number != "101" || row.Status != statusVacant || row.Rent != 45000 {
			t.Fatalf("row changed: %+v", row)
		}
	})
}

func TestNullableIntParsing(t *testing.T) {
	if hasRaw(nil) {
		t.Fatal("nil raw should be treated as omitted")
	}
	if !hasRaw(json.RawMessage(" null ")) {
		t.Fatal("null raw should be treated as provided")
	}
	value, ok := parseNullableInt(json.RawMessage(`null`))
	if !ok || value.Valid {
		t.Fatalf("null = %+v, %v", value, ok)
	}
	value, ok = parseNullableInt(json.RawMessage(`12`))
	if !ok || !value.Valid || value.Int64 != 12 {
		t.Fatalf("int = %+v, %v", value, ok)
	}
	if _, ok := parseNullableInt(json.RawMessage(`12.5`)); ok {
		t.Fatal("fraction should not parse as int")
	}
	if _, ok := parseNullableInt(json.RawMessage(`"12"`)); ok {
		t.Fatal("string should not parse as int")
	}
	if nullableInt(sql.NullInt64{}) != nil || nullableInt(sql.NullInt64{Int64: 4, Valid: true}) != int64(4) {
		t.Fatal("nullableInt mismatch")
	}
	if intPointer(sql.NullInt64{}) != nil {
		t.Fatal("intPointer of a null should be nil")
	}
	if got := intPointer(sql.NullInt64{Int64: 7, Valid: true}); got == nil || *got != 7 {
		t.Fatalf("intPointer = %v", got)
	}
}

func TestRounding(t *testing.T) {
	if got := roundToCents(45000.006); got != 45000.01 {
		t.Fatalf("roundToCents = %v", got)
	}
	if got := roundToCents(45000.004); got != 45000 {
		t.Fatalf("roundToCents = %v", got)
	}
	if got := roundToOne(1.24); got != 1.2 {
		t.Fatalf("roundToOne = %v", got)
	}
	if got := roundToOne(1.25); got != 1.3 {
		t.Fatalf("roundToOne = %v", got)
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

	got = normalizedAmenityIDs([]uint64{1, 0}, fields)
	if !reflect.DeepEqual(got, []uint64{1}) {
		t.Fatalf("ids = %v", got)
	}
	if fields["amenity_ids"] != "must contain positive identifiers" {
		t.Fatalf("amenity_ids = %q", fields["amenity_ids"])
	}
}

func TestSameIDSet(t *testing.T) {
	if !sameIDSet(nil, nil) || !sameIDSet([]uint64{1, 2}, []uint64{2, 1}) {
		t.Fatal("equal sets should match")
	}
	if sameIDSet([]uint64{1}, []uint64{1, 2}) || sameIDSet([]uint64{1, 2}, []uint64{1, 3}) {
		t.Fatal("different sets should not match")
	}
}

func TestValidStatus(t *testing.T) {
	for _, status := range unitStatuses {
		if !validStatus(status) {
			t.Errorf("status %q should be valid", status)
		}
	}
	if validStatus("Vacant") || validStatus("") || validStatus("sold") {
		t.Fatal("unknown statuses should be rejected")
	}
}

func TestUnitSummaryDTO(t *testing.T) {
	row := unitViewFixture()
	out := unitSummaryDTO(row)
	if out.ID != 21 || out.Number != "101" || out.Status != statusOccupied {
		t.Fatalf("summary = %+v", out)
	}
	if out.Property.ID != 4 || out.Property.Name != "Kilimani Heights" || out.Property.Code != "KD-001" {
		t.Fatalf("property = %+v", out.Property)
	}
	if out.Type.ID != 2 || out.Type.Name != "Two Bedroom" || out.Type.Description != "Master ensuite" {
		t.Fatalf("type = %+v", out.Type)
	}
	if out.Floor == nil || *out.Floor != 3 {
		t.Fatalf("floor = %v", out.Floor)
	}
	if out.SquareFeet == nil || *out.SquareFeet != 850 {
		t.Fatalf("square_feet = %v", out.SquareFeet)
	}
	if out.Bedrooms != 2 || out.Bathrooms != 1.5 || out.Rent != 45000 {
		t.Fatalf("template = %+v", out)
	}
	if !out.CreatedAt.Equal(row.CreatedAt) || !out.UpdatedAt.Equal(row.UpdatedAt) {
		t.Fatalf("timestamps = %v / %v", out.CreatedAt, out.UpdatedAt)
	}

	occupant := out.Occupant
	if occupant == nil || occupant.LeaseID != 55 || occupant.LeaseNumber != "L-2026-001" {
		t.Fatalf("occupant = %+v", occupant)
	}
	if occupant.TenantName != "Jane Wanjiku" || occupant.MonthlyRent != 45000 {
		t.Fatalf("occupant = %+v", occupant)
	}
	if !occupant.StartDate.Equal(row.LeaseStart.Time) {
		t.Fatalf("start = %v", occupant.StartDate)
	}
	if occupant.EndDate == nil || !occupant.EndDate.Equal(row.LeaseEnd.Time) {
		t.Fatalf("end = %v", occupant.EndDate)
	}

	vacant := unitViewFixture()
	vacant.Status = statusVacant
	vacant.LeaseID = sql.NullInt64{}
	vacant.LeaseEnd = sql.NullTime{}
	if unitSummaryDTO(vacant).Occupant != nil {
		t.Fatal("a unit without an active lease has no occupant")
	}
	if names := lookupDTOs(nil); names == nil || len(names) != 0 {
		t.Fatalf("empty lookups = %#v", names)
	}
	if got := nullText(sql.NullString{}); got != "" {
		t.Fatalf("nullText = %q", got)
	}
}

func TestUnitDetailJSON(t *testing.T) {
	row := unitViewFixture()
	row.Floor = sql.NullInt64{}
	row.SquareFeet = sql.NullInt64{}
	row.LeaseID = sql.NullInt64{}
	detail := unitDetailDTO(row, []LookupRow{
		{ID: 1, Name: "Parking", Description: sql.NullString{String: "Secure parking", Valid: true}},
		{ID: 2, Name: "Borehole"},
	})
	b, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "number", "property", "type", "rent", "status", "occupant", "amenities", "created_at"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing key %q in %s", key, b)
		}
	}
	if _, ok := decoded["UnitSummaryDTO"]; ok {
		t.Fatalf("embedded struct was not flattened: %s", b)
	}
	if decoded["floor"] != nil || decoded["square_feet"] != nil || decoded["occupant"] != nil {
		t.Fatalf("nullable fields = %v / %v / %v", decoded["floor"], decoded["square_feet"], decoded["occupant"])
	}
	if len(detail.Amenities) != 2 || detail.Amenities[0].Description != "Secure parking" || detail.Amenities[1].Description != "" {
		t.Fatalf("amenities = %+v", detail.Amenities)
	}
}

func TestListValidation(t *testing.T) {
	svc := NewService(nil, nil, nil, discardLogger())
	p := principalWith("units.view")
	cases := []struct {
		name  string
		query ListQuery
		field string
	}{
		{"long search", ListQuery{Search: strings.Repeat("a", maxSearch+1)}, "q"},
		{"bad status", ListQuery{Status: "sold"}, "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.List(context.Background(), p, tc.query)
			var e *httpx.Err
			if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity {
				t.Fatalf("err = %v, want validation", err)
			}
			if e.Fields[tc.field] == "" {
				t.Fatalf("missing %s in %v", tc.field, e.Fields)
			}
		})
	}
}

func TestServiceAuthorization(t *testing.T) {
	svc := NewService(nil, nil, nil, discardLogger())
	ctx := context.Background()
	noPerms := principalWith()
	noOrg := &rbac.Principal{Perms: map[string]struct{}{"units.view": {}}}
	cases := []struct {
		name   string
		status int
		call   func() error
	}{
		{"list without principal", 401, func() error { _, err := svc.List(ctx, nil, ListQuery{}); return err }},
		{"list without org", 401, func() error { _, err := svc.List(ctx, noOrg, ListQuery{}); return err }},
		{"list without permission", 403, func() error { _, err := svc.List(ctx, noPerms, ListQuery{}); return err }},
		{"get without permission", 403, func() error { _, err := svc.Get(ctx, noPerms, 1); return err }},
		{"lookups without permission", 403, func() error { _, err := svc.Lookups(ctx, noPerms); return err }},
		{"create without permission", 403, func() error {
			_, err := svc.Create(ctx, noPerms, CreateInput{}, ClientMeta{})
			return err
		}},
		{"generate without permission", 403, func() error {
			_, err := svc.Generate(ctx, noPerms, GenerateInput{}, ClientMeta{})
			return err
		}},
		{"update without permission", 403, func() error {
			_, err := svc.Update(ctx, noPerms, 1, UpdateInput{}, ClientMeta{})
			return err
		}},
		{"delete without permission", 403, func() error { return svc.Delete(ctx, noPerms, 1, ClientMeta{}) }},
		{"get missing id", 404, func() error { _, err := svc.Get(ctx, principalWith("units.view"), 0); return err }},
		{"update missing id", 404, func() error {
			_, err := svc.Update(ctx, principalWith("units.update"), 0, UpdateInput{}, ClientMeta{})
			return err
		}},
		{"delete missing id", 404, func() error {
			return svc.Delete(ctx, principalWith("units.delete"), 0, ClientMeta{})
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

func TestCreateValidationStopsBeforeDatabase(t *testing.T) {
	svc := NewService(nil, nil, nil, discardLogger())
	p := principalWith("units.create")

	_, err := svc.Create(context.Background(), p, CreateInput{Number: " "}, ClientMeta{})
	var e *httpx.Err
	if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity {
		t.Fatalf("err = %v, want validation", err)
	}
	for _, field := range []string{"property_id", "type_id", "number"} {
		if e.Fields[field] != "required" {
			t.Errorf("%s = %q, want required", field, e.Fields[field])
		}
	}

	_, err = svc.Create(context.Background(), p, CreateInput{PropertyID: 1, TypeID: 1, Number: "1", Status: statusOccupied}, ClientMeta{})
	if !errors.As(err, &e) || e.Fields["status"] != occupiedMessage {
		t.Fatalf("err = %v", err)
	}
}

func TestCachedLookups(t *testing.T) {
	store := newMemStore()
	svc := NewService(nil, store, nil, discardLogger())
	calls := 0
	load := func(context.Context) ([]LookupRow, error) {
		calls++
		return []LookupRow{{
			ID:          2,
			Name:        "Two Bedroom",
			Description: sql.NullString{String: "Master ensuite", Valid: true},
		}}, nil
	}

	first, err := svc.cachedLookups(context.Background(), unitTypesKey, load)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.cachedLookups(context.Background(), unitTypesKey, load)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("loader called %d times, want 1", calls)
	}
	if len(first) != 1 || first[0].Name != "Two Bedroom" || first[0].Description != "Master ensuite" || second[0].ID != 2 {
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
	if _, err := store.Get(context.Background(), "missing"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("memStore miss = %v", err)
	}
}

func TestIsDuplicateEntry(t *testing.T) {
	if !isDuplicateEntry(&mysql.MySQLError{Number: 1062}) {
		t.Fatal("1062 should be recognised")
	}
	if !isDuplicateEntry(fmt.Errorf("insert: %w", &mysql.MySQLError{Number: 1062})) {
		t.Fatal("wrapped 1062 should be recognised")
	}
	if isDuplicateEntry(errors.New("boom")) || isDuplicateEntry(&mysql.MySQLError{Number: 1452}) {
		t.Fatal("other errors should not be recognised")
	}
}

func principalWith(perms ...string) *rbac.Principal {
	set := make(map[string]struct{}, len(perms))
	for _, perm := range perms {
		set[perm] = struct{}{}
	}
	return &rbac.Principal{UserID: 1, OrgID: 1, Perms: set}
}

func unitRowFixture() *UnitRow {
	return &UnitRow{
		ID:             21,
		OrganizationID: 1,
		PropertyID:     4,
		TypeID:         2,
		Number:         "101",
		Floor:          sql.NullInt64{Int64: 3, Valid: true},
		Bedrooms:       2,
		Bathrooms:      1.0,
		SquareFeet:     sql.NullInt64{Int64: 850, Valid: true},
		Rent:           45000,
		Status:         statusVacant,
		CreatedAt:      time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
	}
}

func unitViewFixture() *UnitViewRow {
	unit := unitRowFixture()
	unit.Status = statusOccupied
	unit.Bathrooms = 1.5
	return &UnitViewRow{
		UnitRow:         *unit,
		PropertyName:    "Kilimani Heights",
		PropertyCode:    sql.NullString{String: "KD-001", Valid: true},
		TypeName:        "Two Bedroom",
		TypeDescription: sql.NullString{String: "Master ensuite", Valid: true},
		LeaseID:         sql.NullInt64{Int64: 55, Valid: true},
		LeaseNumber:     sql.NullString{String: "L-2026-001", Valid: true},
		LeaseStart:      sql.NullTime{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		LeaseEnd:        sql.NullTime{Time: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Valid: true},
		LeaseRent:       sql.NullFloat64{Float64: 45000, Valid: true},
		TenantName:      sql.NullString{String: "Jane Wanjiku", Valid: true},
	}
}

func ptrInt(value int) *int           { return &value }
func ptrFloat(value float64) *float64 { return &value }

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
