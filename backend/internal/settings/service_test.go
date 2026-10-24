package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

func stringPtr(value string) *string { return &value }

func newDef(key, typ, defaultValue string) *settingDefinition {
	return &settingDefinition{key: key, typ: typ, defaultValue: defaultValue}
}

func globalSetting(key, typ, value string) SettingRow {
	return SettingRow{Key: key, Type: typ, Value: sql.NullString{String: value, Valid: true}}
}

func orgSetting(orgID uint64, key, typ, value string) SettingRow {
	return SettingRow{
		OrganizationID: sql.NullInt64{Int64: int64(orgID), Valid: true},
		Key:            key,
		Type:           typ,
		Value:          sql.NullString{String: value, Valid: true},
	}
}

func baseOrganization() *OrganizationRow {
	return &OrganizationRow{
		ID:           1,
		Name:         "Acme",
		Slug:         "acme",
		LegalName:    sql.NullString{String: "Acme Ltd", Valid: true},
		Email:        sql.NullString{String: "old@acme.test", Valid: true},
		Phone:        sql.NullString{String: "+254700000000", Valid: true},
		Address:      sql.NullString{String: "1 Old Road", Valid: true},
		County:       sql.NullString{String: "Mombasa", Valid: true},
		CountryCode:  "KE",
		CurrencyCode: "KES",
		Timezone:     "Africa/Nairobi",
		Status:       "active",
	}
}

func TestMergeSettingRows(t *testing.T) {
	global := globalSetting("rent_due_day", "int", "5")
	global.Description = sql.NullString{String: "Day of month rent is due", Valid: true}
	rows := []SettingRow{
		global,
		globalSetting("currency_code", "string", "KES"),
		orgSetting(9, "rent_due_day", "int", "10"),
		orgSetting(9, "orphan_key", "string", "ignored"),
	}

	defs := mergeSettingRows(rows)
	if len(defs) != 2 {
		t.Fatalf("definitions = %d, want 2", len(defs))
	}

	rent, ok := defs["rent_due_day"]
	if !ok {
		t.Fatal("rent_due_day missing")
	}
	if rent.typ != "int" || rent.defaultValue != "5" || rent.description != "Day of month rent is due" {
		t.Fatalf("rent def = %+v", rent)
	}
	if rent.orgValue == nil || *rent.orgValue != "10" {
		t.Fatalf("rent override = %v, want 10", rent.orgValue)
	}
	if defs["currency_code"].orgValue != nil {
		t.Fatal("currency_code should not have an org override")
	}
	if _, ok := defs["orphan_key"]; ok {
		t.Fatal("org override without a global definition should be ignored")
	}
}

func TestEffectiveFromDefs(t *testing.T) {
	rent := newDef("rent_due_day", "int", "5")
	rent.orgValue = stringPtr("10")
	defs := map[string]*settingDefinition{
		"rent_due_day":    rent,
		"currency_code":   newDef("currency_code", "string", "KES"),
		"currency_symbol": newDef("currency_symbol", "string", "KSh"),
	}

	out := effectiveFromDefs(defs)
	if len(out) != 2 {
		t.Fatalf("effective settings = %d, want 2", len(out))
	}
	if out[0].Key != "currency_symbol" || out[1].Key != "rent_due_day" {
		t.Fatalf("keys not sorted or currency_code leaked: %+v", out)
	}
	if out[1].Value != "10" || out[1].Default != "5" || !out[1].Overridden {
		t.Fatalf("rent result = %+v", out[1])
	}
	if out[0].Value != "KSh" || out[0].Overridden {
		t.Fatalf("symbol result = %+v", out[0])
	}
}

func TestSettingDTOs(t *testing.T) {
	out := settingDTOs([]effectiveSetting{
		{Key: "flag", Value: "true", Type: "bool", Default: "false"},
		{Key: "ratio", Value: "12.50", Type: "decimal", Default: "1"},
		{Key: "count", Value: "7", Type: "int", Default: "5", Overridden: true},
		{Key: "meta", Value: `{ "x": 1 }`, Type: "json", Default: `[]`},
		{Key: "symbol", Value: "KSh", Type: "string", Default: "KSh"},
		{Key: "stale", Value: "not-a-number", Type: "int", Default: "1"},
	})
	byKey := make(map[string]SettingDTO, len(out))
	for _, item := range out {
		byKey[item.Key] = item
	}

	if got := byKey["count"].Value; got != int64(7) {
		t.Fatalf("count value = %#v, want int64(7)", got)
	}
	if !byKey["count"].Overridden {
		t.Fatal("count should be marked overridden")
	}
	if got := byKey["ratio"].Value; got != 12.5 {
		t.Fatalf("ratio value = %#v, want 12.5", got)
	}
	if got := byKey["flag"].Value; got != true {
		t.Fatalf("flag value = %#v, want true", got)
	}
	if got := byKey["symbol"].Value; got != "KSh" {
		t.Fatalf("symbol value = %#v, want KSh", got)
	}
	if got := byKey["stale"].Value; got != "not-a-number" {
		t.Fatalf("stale value = %#v, want raw string fallback", got)
	}
	meta, ok := byKey["meta"].Value.(json.RawMessage)
	if !ok || string(meta) != `{ "x": 1 }` {
		t.Fatalf("meta value = %#v", byKey["meta"].Value)
	}
	if def, ok := byKey["meta"].DefaultValue.(json.RawMessage); !ok || string(def) != `[]` {
		t.Fatalf("meta default = %#v", byKey["meta"].DefaultValue)
	}
}

func TestDecodeStored(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		typ  string
		want any
	}{
		{"int trims", " 42 ", "int", int64(42)},
		{"invalid int falls back", "4.2", "int", "4.2"},
		{"decimal", "0.5", "decimal", 0.5},
		{"nan falls back", "NaN", "decimal", "NaN"},
		{"inf falls back", "+Inf", "decimal", "+Inf"},
		{"bool", "true", "bool", true},
		{"invalid bool falls back", "yes", "bool", "yes"},
		{"json", `{"a":1}`, "json", json.RawMessage(`{"a":1}`)},
		{"invalid json falls back", "{nope}", "json", "{nope}"},
		{"string passthrough", "  spaced  ", "string", "  spaced  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeStored(tt.raw, tt.typ)
			want, isRaw := tt.want.(json.RawMessage)
			if isRaw {
				raw, ok := got.(json.RawMessage)
				if !ok || string(raw) != string(want) {
					t.Fatalf("decodeStored(%q, %q) = %#v", tt.raw, tt.typ, got)
				}
				return
			}
			if got != tt.want {
				t.Fatalf("decodeStored(%q, %q) = %#v, want %#v", tt.raw, tt.typ, got, tt.want)
			}
		})
	}
}

func TestCanonicalSettingValue(t *testing.T) {
	tests := []struct {
		name      string
		typ       string
		raw       string
		canonical string
		value     any
		message   string
	}{
		{"int", "int", "10", "10", int64(10), ""},
		{"int rejects fractional", "int", "1.5", "", nil, "must be a whole number"},
		{"int rejects string", "int", `"10"`, "", nil, "must be a whole number"},
		{"decimal", "decimal", "12.50", "12.5", 12.5, ""},
		{"decimal rejects string", "decimal", `"1.5"`, "", nil, "must be a number"},
		{"bool", "bool", "true", "true", true, ""},
		{"bool rejects string", "bool", `"true"`, "", nil, "must be true or false"},
		{"string trims", "string", `"  KES  "`, "KES", "KES", ""},
		{"string rejects number", "string", "5", "", nil, "must be a string"},
		{"json compacts", "json", `{ "b": 2, "a": 1 }`, `{"a":1,"b":2}`, json.RawMessage(`{"a":1,"b":2}`), ""},
		{"json rejects invalid", "json", `{nope}`, "", nil, "must be valid JSON"},
		{"json rejects oversized", "json", `"` + strings.Repeat("a", maxJSONValueBytes) + `"`, "", nil, "must be at most 10240 bytes"},
		{"unsupported type", "date", `"2026-01-01"`, "", nil, "has an unsupported value type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canonical, value, message := canonicalSettingValue(tt.typ, json.RawMessage(tt.raw))
			if message != tt.message {
				t.Fatalf("message = %q, want %q", message, tt.message)
			}
			if canonical != tt.canonical {
				t.Fatalf("canonical = %q, want %q", canonical, tt.canonical)
			}
			if tt.message != "" {
				return
			}
			want, isRaw := tt.value.(json.RawMessage)
			if isRaw {
				raw, ok := value.(json.RawMessage)
				if !ok || string(raw) != string(want) {
					t.Fatalf("value = %#v, want %s", value, want)
				}
				return
			}
			if value != tt.value {
				t.Fatalf("value = %#v, want %#v", value, tt.value)
			}
		})
	}
}

func TestValidateSettingValue(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value any
		want  string
	}{
		{"currency symbol", "currency_symbol", "KSh", ""},
		{"currency symbol empty", "currency_symbol", "", "required"},
		{"currency symbol type", "currency_symbol", 1, "must be a string"},
		{"currency symbol long", "currency_symbol", "123456789", "must be at most 8 characters"},
		{"currency symbol control", "currency_symbol", "K\x00h", "must not contain control characters"},
		{"rent day low", "rent_due_day", int64(0), "must be between 1 and 28"},
		{"rent day high", "rent_due_day", int64(29), "must be between 1 and 28"},
		{"rent day valid", "rent_due_day", int64(28), ""},
		{"rent day type", "rent_due_day", 5, "must be a whole number"},
		{"grace low", "late_fee_grace_days", int64(-1), "must be between 0 and 90"},
		{"grace high", "late_fee_grace_days", int64(91), "must be between 0 and 90"},
		{"grace valid", "late_fee_grace_days", int64(0), ""},
		{"fee type percent", "late_fee_type", "percent", ""},
		{"fee type fixed", "late_fee_type", "fixed", ""},
		{"fee type case sensitive", "late_fee_type", "Percent", "must be percent or fixed"},
		{"fee type unknown", "late_fee_type", "flat", "must be percent or fixed"},
		{"fee value negative", "late_fee_value", -1.0, "must be zero or more"},
		{"fee value too large", "late_fee_value", 1_000_000_001.0, "must be at most 1000000000"},
		{"fee value valid", "late_fee_value", 100.0, ""},
		{"fee value type", "late_fee_value", int64(100), "must be a number"},
		{"deposit low", "default_deposit_months", int64(-1), "must be between 0 and 24"},
		{"deposit high", "default_deposit_months", int64(25), "must be between 0 and 24"},
		{"deposit valid", "default_deposit_months", int64(24), ""},
		{"invoice prefix", "invoice_prefix", "INV_2026-01", ""},
		{"invoice prefix leading space", "invoice_prefix", " INV", "must be 1-20 letters, digits, hyphens or underscores"},
		{"invoice prefix leading dash", "invoice_prefix", "-INV", "must be 1-20 letters, digits, hyphens or underscores"},
		{"invoice prefix long", "invoice_prefix", strings.Repeat("A", 21), "must be 1-20 letters, digits, hyphens or underscores"},
		{"vat low", "vat_rate", -0.1, "must be between 0 and 100"},
		{"vat high", "vat_rate", 100.1, "must be between 0 and 100"},
		{"vat valid", "vat_rate", 16.0, ""},
		{"unknown key", "custom", "anything", ""},
		{"generic string limit", "custom", strings.Repeat("a", maxStringValue+1), "must be at most 1000 characters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateSettingValue(tt.key, tt.value); got != tt.want {
				t.Fatalf("validateSettingValue(%q, %#v) = %q, want %q", tt.key, tt.value, got, tt.want)
			}
		})
	}
}

func TestApplySettingsPatch(t *testing.T) {
	t.Run("canonicalises values", func(t *testing.T) {
		defs := map[string]*settingDefinition{
			"rent_due_day":  newDef("rent_due_day", "int", "5"),
			"late_fee_type": newDef("late_fee_type", "string", "fixed"),
		}
		updates := map[string]*string{}
		fields := map[string]string{}
		applySettingsPatch(defs, map[string]json.RawMessage{
			"rent_due_day":  json.RawMessage("10"),
			"late_fee_type": json.RawMessage(`"PERCENT"`),
		}, updates, fields)

		if len(fields) != 0 {
			t.Fatalf("fields = %v", fields)
		}
		if next := updates["rent_due_day"]; next == nil || *next != "10" {
			t.Fatalf("rent_due_day update = %v", next)
		}
		if next := updates["late_fee_type"]; next == nil || *next != "percent" {
			t.Fatalf("late_fee_type update = %v", next)
		}
	})

	t.Run("rejects unknown and currency code", func(t *testing.T) {
		defs := map[string]*settingDefinition{
			"currency_code": newDef("currency_code", "string", "KES"),
			"rent_due_day":  newDef("rent_due_day", "int", "5"),
		}
		updates := map[string]*string{}
		fields := map[string]string{}
		applySettingsPatch(defs, map[string]json.RawMessage{
			"currency_code": json.RawMessage(`"USD"`),
			"orphan":        json.RawMessage(`"x"`),
		}, updates, fields)

		if len(updates) != 0 {
			t.Fatalf("updates = %v", updates)
		}
		if fields["settings.currency_code"] != "update this through the organization profile" {
			t.Fatalf("currency error = %q", fields["settings.currency_code"])
		}
		if fields["settings.orphan"] != "unknown setting" {
			t.Fatalf("orphan error = %q", fields["settings.orphan"])
		}
	})

	t.Run("null reverts an override", func(t *testing.T) {
		defs := map[string]*settingDefinition{
			"currency_symbol": newDef("currency_symbol", "string", "KSh"),
		}
		updates := map[string]*string{}
		fields := map[string]string{}
		applySettingsPatch(defs, map[string]json.RawMessage{
			"currency_symbol": json.RawMessage("null"),
		}, updates, fields)

		if len(fields) != 0 {
			t.Fatalf("fields = %v", fields)
		}
		next, ok := updates["currency_symbol"]
		if !ok || next != nil {
			t.Fatalf("currency_symbol update = %v, present = %v", next, ok)
		}
	})

	t.Run("reports type and format errors", func(t *testing.T) {
		defs := map[string]*settingDefinition{
			"rent_due_day":   newDef("rent_due_day", "int", "5"),
			"invoice_prefix": newDef("invoice_prefix", "string", "INV"),
		}
		updates := map[string]*string{}
		fields := map[string]string{}
		applySettingsPatch(defs, map[string]json.RawMessage{
			"rent_due_day":   json.RawMessage(`"10"`),
			"invoice_prefix": json.RawMessage(`"bad prefix!"`),
		}, updates, fields)

		if len(updates) != 0 {
			t.Fatalf("updates = %v", updates)
		}
		if fields["settings.rent_due_day"] != "must be a whole number" {
			t.Fatalf("rent error = %q", fields["settings.rent_due_day"])
		}
		if fields["settings.invoice_prefix"] != "must be 1-20 letters, digits, hyphens or underscores" {
			t.Fatalf("prefix error = %q", fields["settings.invoice_prefix"])
		}
	})
}

func TestValidateLateFee(t *testing.T) {
	newDefs := func() map[string]*settingDefinition {
		return map[string]*settingDefinition{
			"late_fee_type":  newDef("late_fee_type", "string", "percent"),
			"late_fee_value": newDef("late_fee_value", "decimal", "5"),
		}
	}

	t.Run("percent above 100", func(t *testing.T) {
		fields := map[string]string{}
		validateLateFee(newDefs(), map[string]*string{"late_fee_value": stringPtr("150")}, fields)
		if fields["settings.late_fee_value"] != "must be between 0 and 100 when the late fee type is percent" {
			t.Fatalf("fields = %v", fields)
		}
	})

	t.Run("fixed allows over 100", func(t *testing.T) {
		fields := map[string]string{}
		validateLateFee(newDefs(), map[string]*string{
			"late_fee_type":  stringPtr("fixed"),
			"late_fee_value": stringPtr("150"),
		}, fields)
		if len(fields) != 0 {
			t.Fatalf("fields = %v", fields)
		}
	})

	t.Run("existing override is validated", func(t *testing.T) {
		defs := newDefs()
		defs["late_fee_value"].orgValue = stringPtr("150")
		fields := map[string]string{}
		validateLateFee(defs, map[string]*string{"late_fee_type": nil}, fields)
		if fields["settings.late_fee_value"] == "" {
			t.Fatalf("expected error for existing percent override, fields = %v", fields)
		}
	})

	t.Run("percent within range", func(t *testing.T) {
		fields := map[string]string{}
		validateLateFee(newDefs(), map[string]*string{"late_fee_value": stringPtr("50")}, fields)
		if len(fields) != 0 {
			t.Fatalf("fields = %v", fields)
		}
	})

	t.Run("missing definitions are ignored", func(t *testing.T) {
		fields := map[string]string{}
		validateLateFee(map[string]*settingDefinition{}, map[string]*string{}, fields)
		if len(fields) != 0 {
			t.Fatalf("fields = %v", fields)
		}
	})
}

func TestApplyOrganizationPatch(t *testing.T) {
	t.Run("normalises and records changes", func(t *testing.T) {
		org := baseOrganization()
		fields := map[string]string{}
		oldValues, newValues := applyOrganizationPatch(org, &OrganizationPatch{
			Name:         stringPtr("  New Name  "),
			LegalName:    stringPtr(""),
			KRAPIN:       stringPtr(" p051234567x "),
			Email:        stringPtr(" Ops@Acme.TEST "),
			CountryCode:  stringPtr("ke"),
			CurrencyCode: stringPtr("usd"),
			Timezone:     stringPtr("Europe/London"),
		}, fields)

		if len(fields) != 0 {
			t.Fatalf("fields = %v", fields)
		}
		if org.Name != "New Name" || org.KRAPIN.String != "P051234567X" || !org.KRAPIN.Valid {
			t.Fatalf("org = %+v", org)
		}
		if org.Email.String != "ops@acme.test" {
			t.Fatalf("email = %q", org.Email.String)
		}
		if org.LegalName.Valid {
			t.Fatalf("legal name should be cleared: %+v", org.LegalName)
		}
		if org.CurrencyCode != "USD" || org.Timezone != "Europe/London" {
			t.Fatalf("org = %+v", org)
		}
		if len(oldValues) != 6 || len(newValues) != 6 {
			t.Fatalf("old = %v, new = %v", oldValues, newValues)
		}
		if oldValues["name"] != "Acme" || newValues["name"] != "New Name" {
			t.Fatalf("name audit = %v -> %v", oldValues["name"], newValues["name"])
		}
		if oldValues["legal_name"] != "Acme Ltd" || newValues["legal_name"] != nil {
			t.Fatalf("legal name audit = %v -> %v", oldValues["legal_name"], newValues["legal_name"])
		}
		if oldValues["kra_pin"] != nil || newValues["kra_pin"] != "P051234567X" {
			t.Fatalf("pin audit = %v -> %v", oldValues["kra_pin"], newValues["kra_pin"])
		}
		if _, ok := newValues["country_code"]; ok {
			t.Fatal("unchanged country code should not be recorded")
		}
	})

	t.Run("unchanged values are not recorded", func(t *testing.T) {
		org := baseOrganization()
		fields := map[string]string{}
		oldValues, newValues := applyOrganizationPatch(org, &OrganizationPatch{
			Name:         stringPtr(" Acme "),
			LegalName:    stringPtr("Acme Ltd"),
			KRAPIN:       stringPtr(""),
			Email:        stringPtr(" OLD@ACME.TEST "),
			CountryCode:  stringPtr("ke"),
			CurrencyCode: stringPtr("kes"),
			Timezone:     stringPtr("Africa/Nairobi"),
		}, fields)

		if len(fields) != 0 {
			t.Fatalf("fields = %v", fields)
		}
		if len(oldValues) != 0 || len(newValues) != 0 {
			t.Fatalf("old = %v, new = %v", oldValues, newValues)
		}
	})

	t.Run("validation errors", func(t *testing.T) {
		tests := []struct {
			name  string
			patch OrganizationPatch
			field string
			want  string
		}{
			{"name required", OrganizationPatch{Name: stringPtr("   ")}, "organization.name", "required"},
			{"name too long", OrganizationPatch{Name: stringPtr(strings.Repeat("a", 151))}, "organization.name", "must be at most 150 characters"},
			{"legal name too long", OrganizationPatch{LegalName: stringPtr(strings.Repeat("a", 201))}, "organization.legal_name", "must be at most 200 characters"},
			{"kra pin punctuation", OrganizationPatch{KRAPIN: stringPtr("P-051")}, "organization.kra_pin", "must contain only letters and digits"},
			{"kra pin too long", OrganizationPatch{KRAPIN: stringPtr(strings.Repeat("A", 21))}, "organization.kra_pin", "must be at most 20 characters"},
			{"invalid email", OrganizationPatch{Email: stringPtr("not an email")}, "organization.email", "must be a valid email address"},
			{"invalid phone", OrganizationPatch{Phone: stringPtr("0712345678")}, "organization.phone", "must be in international format, e.g. +254712345678"},
			{"address too long", OrganizationPatch{Address: stringPtr(strings.Repeat("a", 256))}, "organization.address", "must be at most 255 characters"},
			{"county too long", OrganizationPatch{County: stringPtr(strings.Repeat("a", 101))}, "organization.county", "must be at most 100 characters"},
			{"country code invalid", OrganizationPatch{CountryCode: stringPtr("KEN")}, "organization.country_code", "must be a 2-letter country code"},
			{"currency code invalid", OrganizationPatch{CurrencyCode: stringPtr("US")}, "organization.currency_code", "must be a 3-letter currency code"},
			{"timezone required", OrganizationPatch{Timezone: stringPtr("")}, "organization.timezone", "required"},
			{"timezone local", OrganizationPatch{Timezone: stringPtr("Local")}, "organization.timezone", "must be a valid IANA timezone"},
			{"timezone too long", OrganizationPatch{Timezone: stringPtr(strings.Repeat("a", 51))}, "organization.timezone", "must be at most 50 characters"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				org := baseOrganization()
				fields := map[string]string{}
				applyOrganizationPatch(org, &tt.patch, fields)
				if len(fields) != 1 || fields[tt.field] != tt.want {
					t.Fatalf("fields = %v, want %s = %q", fields, tt.field, tt.want)
				}
			})
		}
	})
}

func TestContactValidation(t *testing.T) {
	t.Run("email", func(t *testing.T) {
		tests := []struct {
			in   string
			want string
		}{
			{"", ""},
			{"user@example.com", ""},
			{"user.name+tag@example.co.ke", ""},
			{"User <user@example.com>", "must be a valid email address"},
			{"user@@example.com", "must be a valid email address"},
			{strings.Repeat("a", 244) + "@example.com", "must be at most 255 characters"},
		}
		for _, tt := range tests {
			if got := validateEmail(tt.in); got != tt.want {
				t.Fatalf("validateEmail(%q) = %q, want %q", tt.in, got, tt.want)
			}
		}
	})

	t.Run("phone", func(t *testing.T) {
		tests := []struct {
			in   string
			want string
		}{
			{"", ""},
			{"+254712345678", ""},
			{"+" + strings.Repeat("1", 19), ""},
			{"0712345678", "must be in international format, e.g. +254712345678"},
			{"+25471234567a", "must be in international format, e.g. +254712345678"},
			{"+2547", "must be in international format, e.g. +254712345678"},
			{"+" + strings.Repeat("1", 20), "must be in international format, e.g. +254712345678"},
		}
		for _, tt := range tests {
			if got := validatePhone(tt.in); got != tt.want {
				t.Fatalf("validatePhone(%q) = %q, want %q", tt.in, got, tt.want)
			}
		}
	})

	t.Run("timezone", func(t *testing.T) {
		tests := []struct {
			in   string
			want bool
		}{
			{"Africa/Nairobi", true},
			{"UTC", true},
			{"Europe/London", true},
			{"Local", false},
			{"", false},
			{"Not/AZone", false},
		}
		for _, tt := range tests {
			if got := validTimezone(tt.in); got != tt.want {
				t.Fatalf("validTimezone(%q) = %v, want %v", tt.in, got, tt.want)
			}
		}
	})
}

func TestServiceAuthorization(t *testing.T) {
	svc := NewService(nil, nil, nil, nil)
	ctx := context.Background()

	if _, err := svc.Get(ctx, nil); !errors.Is(err, httpx.ErrUnauthorized) {
		t.Fatalf("Get(nil) error = %v", err)
	}
	if _, err := svc.Get(ctx, &rbac.Principal{Perms: map[string]struct{}{"settings.view": {}}}); !errors.Is(err, httpx.ErrUnauthorized) {
		t.Fatalf("Get(no org) error = %v", err)
	}
	if _, err := svc.Get(ctx, &rbac.Principal{OrgID: 7}); !errors.Is(err, httpx.ErrForbidden) {
		t.Fatalf("Get(no permission) error = %v", err)
	}

	if _, err := svc.Update(ctx, nil, UpdateInput{}, ClientMeta{}); !errors.Is(err, httpx.ErrUnauthorized) {
		t.Fatalf("Update(nil) error = %v", err)
	}
	viewer := &rbac.Principal{OrgID: 7, Perms: map[string]struct{}{"settings.view": {}}}
	if _, err := svc.Update(ctx, viewer, UpdateInput{}, ClientMeta{}); !errors.Is(err, httpx.ErrForbidden) {
		t.Fatalf("Update(viewer) error = %v", err)
	}

	editor := &rbac.Principal{OrgID: 7, Perms: map[string]struct{}{"settings.update": {}}}
	_, err := svc.Update(ctx, editor, UpdateInput{}, ClientMeta{})
	var apiErr *httpx.Err
	if !errors.As(err, &apiErr) || apiErr.Status != 422 {
		t.Fatalf("Update(empty) error = %v", err)
	}
	if apiErr.Fields["body"] != "at least one of organization or settings is required" {
		t.Fatalf("fields = %v", apiErr.Fields)
	}
}

func TestSettingsCacheKey(t *testing.T) {
	if got := settingsCacheKey(42); got != "settings:org:42" {
		t.Fatalf("cache key = %q", got)
	}
}

func TestOrganizationDTO(t *testing.T) {
	org := baseOrganization()
	org.County = sql.NullString{}
	dto := organizationDTO(org)

	if dto.ID != 1 || dto.Name != "Acme" || dto.Slug != "acme" {
		t.Fatalf("dto = %+v", dto)
	}
	if dto.LegalName != "Acme Ltd" || dto.County != "" || dto.Status != "active" {
		t.Fatalf("dto = %+v", dto)
	}
	if dto.CountryCode != "KE" || dto.CurrencyCode != "KES" || dto.Timezone != "Africa/Nairobi" {
		t.Fatalf("dto = %+v", dto)
	}
}
