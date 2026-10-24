package settings

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/mail"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // keep IANA zones available in minimal deployment images
	"unicode"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/audit"
	"kodipms/internal/platform/cache"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
	"kodipms/internal/tenancy"
)

const (
	settingsCacheTTL  = 10 * time.Minute
	currencyCodeKey   = "currency_code"
	maxStringValue    = 1000
	maxJSONValueBytes = 10 * 1024
)

var (
	countryCodeRE   = regexp.MustCompile(`^[A-Z]{2}$`)
	currencyCodeRE  = regexp.MustCompile(`^[A-Z]{3}$`)
	invoicePrefixRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,19}$`)
)

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

func (s *Service) Get(ctx context.Context, p *rbac.Principal) (*SettingsDTO, error) {
	if p == nil || p.OrgID == 0 {
		return nil, httpx.ErrUnauthorized
	}
	if !p.Can("settings.view") {
		return nil, httpx.ErrForbidden
	}
	org, err := s.repo.Organization(ctx, p.OrgID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	effective, err := s.effectiveSettings(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	return &SettingsDTO{
		Organization: organizationDTO(org),
		Settings:     settingDTOs(effective),
	}, nil
}

func (s *Service) Update(ctx context.Context, p *rbac.Principal, in UpdateInput, meta ClientMeta) (*SettingsDTO, error) {
	if p == nil || p.OrgID == 0 {
		return nil, httpx.ErrUnauthorized
	}
	if !p.Can("settings.update") {
		return nil, httpx.ErrForbidden
	}
	hasOrg := in.Organization != nil
	hasSettings := len(in.Settings) > 0
	if !hasOrg && !hasSettings {
		return nil, httpx.Validation(map[string]string{
			"body": "at least one of organization or settings is required",
		})
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// The organization row doubles as the write lock for all of its settings,
	// so one PATCH cannot validate against values another PATCH is replacing.
	org, err := s.repo.OrganizationForUpdate(ctx, tx, p.OrgID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.SettingRowsTx(ctx, tx, p.OrgID)
	if err != nil {
		return nil, err
	}
	defs := mergeSettingRows(rows)

	fields := map[string]string{}
	var orgOld, orgNew map[string]any
	if hasOrg {
		orgOld, orgNew = applyOrganizationPatch(org, in.Organization, fields)
	}
	updates := map[string]*string{}
	if hasSettings {
		applySettingsPatch(defs, in.Settings, updates, fields)
		validateLateFee(defs, updates, fields)
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	if len(orgNew) > 0 {
		if err := s.repo.UpdateOrganization(ctx, tx, org); err != nil {
			return nil, err
		}
	}
	var settingOld, settingNew map[string]any
	if hasSettings {
		settingOld, settingNew, err = persistSettings(ctx, tx, s.repo, p.OrgID, defs, updates)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	changed := len(orgNew) > 0 || len(settingNew) > 0
	effective := effectiveFromDefs(defs)
	if changed {
		s.invalidate(ctx, p.OrgID, len(orgNew) > 0)
		s.cacheSettings(ctx, p.OrgID, effective)
	}

	if len(orgNew) > 0 {
		s.writeAudit(ctx, audit.Entry{
			OrgID: p.OrgID, UserID: p.UserID,
			Action: "settings.organization_updated", EntityType: "organizations", EntityID: p.OrgID,
			OldValues: orgOld, NewValues: orgNew,
			IPAddress: meta.IP, UserAgent: meta.UserAgent,
		})
	}
	if len(settingNew) > 0 {
		s.writeAudit(ctx, audit.Entry{
			OrgID: p.OrgID, UserID: p.UserID,
			Action: "settings.values_updated", EntityType: "organizations", EntityID: p.OrgID,
			OldValues: settingOld, NewValues: settingNew,
			IPAddress: meta.IP, UserAgent: meta.UserAgent,
		})
	}

	return &SettingsDTO{
		Organization: organizationDTO(org),
		Settings:     settingDTOs(effective),
	}, nil
}

func (s *Service) effectiveSettings(ctx context.Context, orgID uint64) ([]effectiveSetting, error) {
	key := settingsCacheKey(orgID)
	if raw, err := s.store.Get(ctx, key); err == nil {
		var cached []effectiveSetting
		if json.Unmarshal([]byte(raw), &cached) == nil {
			return cached, nil
		}
	}
	rows, err := s.repo.SettingRows(ctx, orgID)
	if err != nil {
		return nil, err
	}
	effective := effectiveFromDefs(mergeSettingRows(rows))
	s.cacheSettings(ctx, orgID, effective)
	return effective, nil
}

func (s *Service) cacheSettings(ctx context.Context, orgID uint64, effective []effectiveSetting) {
	b, err := json.Marshal(effective)
	if err != nil {
		return
	}
	if err := s.store.Set(ctx, settingsCacheKey(orgID), string(b), settingsCacheTTL); err != nil {
		s.log.Debug("cache organization settings", "org_id", orgID, "error", err)
	}
}

func (s *Service) invalidate(ctx context.Context, orgID uint64, domains bool) {
	if err := s.store.Del(ctx, settingsCacheKey(orgID)); err != nil {
		s.log.Warn("invalidate settings cache", "org_id", orgID, "error", err)
	}
	if !domains {
		return
	}
	rows, err := s.repo.Domains(ctx, orgID)
	if err != nil {
		s.log.Warn("load organization domains for cache invalidation", "org_id", orgID, "error", err)
		return
	}
	keys := make([]string, 0, len(rows))
	for _, domain := range rows {
		if host := tenancy.NormalizeHost(domain); host != "" {
			keys = append(keys, "domain:"+host)
		}
	}
	if len(keys) == 0 {
		return
	}
	if err := s.store.Del(ctx, keys...); err != nil {
		s.log.Warn("invalidate organization domain cache", "org_id", orgID, "error", err)
	}
}

func (s *Service) writeAudit(ctx context.Context, e audit.Entry) {
	if err := s.audit.Log(ctx, e); err != nil {
		s.log.Warn("write settings audit", "action", e.Action, "org_id", e.OrgID, "error", err)
	}
}

func mergeSettingRows(rows []SettingRow) map[string]*settingDefinition {
	defs := make(map[string]*settingDefinition)
	for _, row := range rows {
		if row.OrganizationID.Valid {
			continue
		}
		defs[row.Key] = &settingDefinition{
			key:          row.Key,
			typ:          row.Type,
			description:  row.Description.String,
			defaultValue: row.Value.String,
		}
	}
	for _, row := range rows {
		if !row.OrganizationID.Valid {
			continue
		}
		def, ok := defs[row.Key]
		if !ok {
			continue
		}
		value := row.Value.String
		def.orgValue = &value
	}
	return defs
}

func effectiveFromDefs(defs map[string]*settingDefinition) []effectiveSetting {
	effective := make([]effectiveSetting, 0, len(defs))
	for key, def := range defs {
		if key == currencyCodeKey {
			continue
		}
		value := def.defaultValue
		overridden := false
		if def.orgValue != nil {
			value = *def.orgValue
			overridden = true
		}
		effective = append(effective, effectiveSetting{
			Key:         key,
			Value:       value,
			Type:        def.typ,
			Description: def.description,
			Default:     def.defaultValue,
			Overridden:  overridden,
		})
	}
	sort.Slice(effective, func(i, j int) bool { return effective[i].Key < effective[j].Key })
	return effective
}

func settingDTOs(effective []effectiveSetting) []SettingDTO {
	out := make([]SettingDTO, 0, len(effective))
	for _, item := range effective {
		out = append(out, SettingDTO{
			Key:          item.Key,
			Value:        decodeStored(item.Value, item.Type),
			ValueType:    item.Type,
			Description:  item.Description,
			Overridden:   item.Overridden,
			DefaultValue: decodeStored(item.Default, item.Type),
		})
	}
	return out
}

func organizationDTO(org *OrganizationRow) OrganizationDTO {
	return OrganizationDTO{
		ID:           org.ID,
		Name:         org.Name,
		Slug:         org.Slug,
		LegalName:    org.LegalName.String,
		KRAPIN:       org.KRAPIN.String,
		Email:        org.Email.String,
		Phone:        org.Phone.String,
		Address:      org.Address.String,
		County:       org.County.String,
		CountryCode:  org.CountryCode,
		CurrencyCode: org.CurrencyCode,
		Timezone:     org.Timezone,
		Status:       org.Status,
	}
}

func applyOrganizationPatch(org *OrganizationRow, in *OrganizationPatch, fields map[string]string) (map[string]any, map[string]any) {
	oldValues := map[string]any{}
	newValues := map[string]any{}
	record := func(name string, old, next any) {
		oldValues[name] = old
		newValues[name] = next
	}

	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		switch {
		case name == "":
			fields["organization.name"] = "required"
		case utf8.RuneCountInString(name) > 150:
			fields["organization.name"] = "must be at most 150 characters"
		case name != org.Name:
			record("name", org.Name, name)
			org.Name = name
		}
	}
	if in.LegalName != nil {
		value := strings.TrimSpace(*in.LegalName)
		if utf8.RuneCountInString(value) > 200 {
			fields["organization.legal_name"] = "must be at most 200 characters"
		} else {
			old := org.LegalName
			if setNullable(&org.LegalName, value) {
				record("legal_name", nullableValue(old), nullableValue(org.LegalName))
			}
		}
	}
	if in.KRAPIN != nil {
		value := strings.ToUpper(strings.TrimSpace(*in.KRAPIN))
		switch {
		case utf8.RuneCountInString(value) > 20:
			fields["organization.kra_pin"] = "must be at most 20 characters"
		case value != "" && !isAlphaNumeric(value):
			fields["organization.kra_pin"] = "must contain only letters and digits"
		default:
			old := org.KRAPIN
			if setNullable(&org.KRAPIN, value) {
				record("kra_pin", nullableValue(old), nullableValue(org.KRAPIN))
			}
		}
	}
	if in.Email != nil {
		value := strings.ToLower(strings.TrimSpace(*in.Email))
		if msg := validateEmail(value); msg != "" {
			fields["organization.email"] = msg
		} else {
			old := org.Email
			if setNullable(&org.Email, value) {
				record("email", nullableValue(old), nullableValue(org.Email))
			}
		}
	}
	if in.Phone != nil {
		value := strings.TrimSpace(*in.Phone)
		if msg := validatePhone(value); msg != "" {
			fields["organization.phone"] = msg
		} else {
			old := org.Phone
			if setNullable(&org.Phone, value) {
				record("phone", nullableValue(old), nullableValue(org.Phone))
			}
		}
	}
	if in.Address != nil {
		value := strings.TrimSpace(*in.Address)
		if utf8.RuneCountInString(value) > 255 {
			fields["organization.address"] = "must be at most 255 characters"
		} else {
			old := org.Address
			if setNullable(&org.Address, value) {
				record("address", nullableValue(old), nullableValue(org.Address))
			}
		}
	}
	if in.County != nil {
		value := strings.TrimSpace(*in.County)
		if utf8.RuneCountInString(value) > 100 {
			fields["organization.county"] = "must be at most 100 characters"
		} else {
			old := org.County
			if setNullable(&org.County, value) {
				record("county", nullableValue(old), nullableValue(org.County))
			}
		}
	}
	if in.CountryCode != nil {
		value := strings.ToUpper(strings.TrimSpace(*in.CountryCode))
		if !countryCodeRE.MatchString(value) {
			fields["organization.country_code"] = "must be a 2-letter country code"
		} else if value != org.CountryCode {
			record("country_code", org.CountryCode, value)
			org.CountryCode = value
		}
	}
	if in.CurrencyCode != nil {
		value := strings.ToUpper(strings.TrimSpace(*in.CurrencyCode))
		if !currencyCodeRE.MatchString(value) {
			fields["organization.currency_code"] = "must be a 3-letter currency code"
		} else if value != org.CurrencyCode {
			record("currency_code", org.CurrencyCode, value)
			org.CurrencyCode = value
		}
	}
	if in.Timezone != nil {
		value := strings.TrimSpace(*in.Timezone)
		switch {
		case value == "":
			fields["organization.timezone"] = "required"
		case utf8.RuneCountInString(value) > 50:
			fields["organization.timezone"] = "must be at most 50 characters"
		case !validTimezone(value):
			fields["organization.timezone"] = "must be a valid IANA timezone"
		case value != org.Timezone:
			record("timezone", org.Timezone, value)
			org.Timezone = value
		}
	}
	return oldValues, newValues
}

func applySettingsPatch(defs map[string]*settingDefinition, in map[string]json.RawMessage, updates map[string]*string, fields map[string]string) {
	for key, raw := range in {
		field := "settings." + key
		def, ok := defs[key]
		if !ok {
			fields[field] = "unknown setting"
			continue
		}
		if key == currencyCodeKey {
			fields[field] = "update this through the organization profile"
			continue
		}
		if isJSONNull(raw) {
			updates[key] = nil
			continue
		}
		canonical, value, msg := canonicalSettingValue(def.typ, raw)
		if msg != "" {
			fields[field] = msg
			continue
		}
		if key == "late_fee_type" {
			canonical = strings.ToLower(canonical)
			value = canonical
		}
		if msg := validateSettingValue(key, value); msg != "" {
			fields[field] = msg
			continue
		}
		updates[key] = &canonical
	}
}

func validateLateFee(defs map[string]*settingDefinition, updates map[string]*string, fields map[string]string) {
	typeDef, okType := defs["late_fee_type"]
	valueDef, okValue := defs["late_fee_value"]
	if !okType || !okValue {
		return
	}
	if strings.ToLower(strings.TrimSpace(effectiveValue(typeDef, updates))) != "percent" {
		return
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(effectiveValue(valueDef, updates)), 64)
	if err == nil && (value < 0 || value > 100) {
		fields["settings.late_fee_value"] = "must be between 0 and 100 when the late fee type is percent"
	}
}

func effectiveValue(def *settingDefinition, updates map[string]*string) string {
	if next, ok := updates[def.key]; ok {
		if next == nil {
			return def.defaultValue
		}
		return *next
	}
	if def.orgValue != nil {
		return *def.orgValue
	}
	return def.defaultValue
}

func persistSettings(ctx context.Context, tx *sqlx.Tx, repo *repository, orgID uint64, defs map[string]*settingDefinition, updates map[string]*string) (map[string]any, map[string]any, error) {
	oldValues := map[string]any{}
	newValues := map[string]any{}
	for key, next := range updates {
		def, ok := defs[key]
		if !ok {
			continue
		}
		previous := def.orgValue
		same := previous == nil && next == nil
		if previous != nil && next != nil && *previous == *next {
			same = true
		}
		def.orgValue = next
		if same {
			continue
		}
		oldValues[key] = nullablePointer(previous)
		newValues[key] = nullablePointer(next)
		if next == nil {
			if err := repo.DeleteSetting(ctx, tx, orgID, key); err != nil {
				return nil, nil, err
			}
			continue
		}
		if err := repo.UpsertSetting(ctx, tx, orgID, key, *next, def.typ, def.description); err != nil {
			return nil, nil, err
		}
	}
	return oldValues, newValues, nil
}

func canonicalSettingValue(typ string, raw json.RawMessage) (string, any, string) {
	raw = bytes.TrimSpace(raw)
	switch typ {
	case "int":
		var value int64
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", nil, "must be a whole number"
		}
		return strconv.FormatInt(value, 10), value, ""
	case "decimal":
		var value float64
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", nil, "must be a number"
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "", nil, "must be a finite number"
		}
		return strconv.FormatFloat(value, 'f', -1, 64), value, ""
	case "bool":
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", nil, "must be true or false"
		}
		return strconv.FormatBool(value), value, ""
	case "json":
		if len(raw) > maxJSONValueBytes {
			return "", nil, fmt.Sprintf("must be at most %d bytes", maxJSONValueBytes)
		}
		if !json.Valid(raw) {
			return "", nil, "must be valid JSON"
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", nil, "must be valid JSON"
		}
		compact, err := json.Marshal(value)
		if err != nil {
			return "", nil, "must be valid JSON"
		}
		return string(compact), json.RawMessage(compact), ""
	case "string":
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", nil, "must be a string"
		}
		value = strings.TrimSpace(value)
		return value, value, ""
	default:
		return "", nil, "has an unsupported value type"
	}
}

func validateSettingValue(key string, value any) string {
	switch key {
	case "currency_symbol":
		text, ok := value.(string)
		if !ok {
			return "must be a string"
		}
		if text == "" {
			return "required"
		}
		if utf8.RuneCountInString(text) > 8 {
			return "must be at most 8 characters"
		}
		if strings.IndexFunc(text, unicode.IsControl) >= 0 {
			return "must not contain control characters"
		}
	case "rent_due_day":
		number, ok := value.(int64)
		if !ok {
			return "must be a whole number"
		}
		if number < 1 || number > 28 {
			return "must be between 1 and 28"
		}
	case "late_fee_grace_days":
		number, ok := value.(int64)
		if !ok {
			return "must be a whole number"
		}
		if number < 0 || number > 90 {
			return "must be between 0 and 90"
		}
	case "late_fee_type":
		text, ok := value.(string)
		if !ok {
			return "must be a string"
		}
		if text != "percent" && text != "fixed" {
			return "must be percent or fixed"
		}
	case "late_fee_value":
		number, ok := value.(float64)
		if !ok {
			return "must be a number"
		}
		if number < 0 {
			return "must be zero or more"
		}
		if number > 1_000_000_000 {
			return "must be at most 1000000000"
		}
	case "default_deposit_months":
		number, ok := value.(int64)
		if !ok {
			return "must be a whole number"
		}
		if number < 0 || number > 24 {
			return "must be between 0 and 24"
		}
	case "invoice_prefix":
		text, ok := value.(string)
		if !ok {
			return "must be a string"
		}
		if !invoicePrefixRE.MatchString(text) {
			return "must be 1-20 letters, digits, hyphens or underscores"
		}
	case "vat_rate":
		number, ok := value.(float64)
		if !ok {
			return "must be a number"
		}
		if number < 0 || number > 100 {
			return "must be between 0 and 100"
		}
	}
	if text, ok := value.(string); ok && utf8.RuneCountInString(text) > maxStringValue {
		return fmt.Sprintf("must be at most %d characters", maxStringValue)
	}
	return ""
}

func decodeStored(raw, typ string) any {
	trimmed := strings.TrimSpace(raw)
	switch typ {
	case "int":
		if value, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return value
		}
	case "decimal":
		if value, err := strconv.ParseFloat(trimmed, 64); err == nil && !math.IsNaN(value) && !math.IsInf(value, 0) {
			return value
		}
	case "bool":
		if value, err := strconv.ParseBool(trimmed); err == nil {
			return value
		}
	case "json":
		if trimmed != "" && json.Valid([]byte(trimmed)) {
			return json.RawMessage(trimmed)
		}
	}
	return raw
}

func settingsCacheKey(orgID uint64) string {
	return "settings:org:" + strconv.FormatUint(orgID, 10)
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func setNullable(dst *sql.NullString, value string) bool {
	next := sql.NullString{String: value, Valid: value != ""}
	if dst.Valid == next.Valid && dst.String == next.String {
		return false
	}
	*dst = next
	return true
}

func nullableValue(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func nullablePointer(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func validateEmail(value string) string {
	if value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) > 255 {
		return "must be at most 255 characters"
	}
	addr, err := mail.ParseAddress(value)
	if err != nil || !strings.EqualFold(addr.Address, value) {
		return "must be a valid email address"
	}
	return ""
}

func validatePhone(value string) string {
	if value == "" {
		return ""
	}
	if len(value) < 8 || len(value) > 20 || value[0] != '+' {
		return "must be in international format, e.g. +254712345678"
	}
	for i := 1; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return "must be in international format, e.g. +254712345678"
		}
	}
	return ""
}

func validTimezone(value string) bool {
	if value == "" || value == "Local" {
		return false
	}
	_, err := time.LoadLocation(value)
	return err == nil
}

func isAlphaNumeric(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
