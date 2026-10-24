package settings

import "database/sql"

type OrganizationRow struct {
	ID           uint64         `db:"id"`
	Name         string         `db:"org_name"`
	Slug         string         `db:"slug"`
	LegalName    sql.NullString `db:"legal_name"`
	KRAPIN       sql.NullString `db:"kra_pin"`
	Email        sql.NullString `db:"email"`
	Phone        sql.NullString `db:"phone"`
	Address      sql.NullString `db:"org_address"`
	County       sql.NullString `db:"county"`
	CountryCode  string         `db:"country_code"`
	CurrencyCode string         `db:"currency_code"`
	Timezone     string         `db:"timezone"`
	Status       string         `db:"org_status"`
}

type SettingRow struct {
	OrganizationID sql.NullInt64  `db:"organization_id"`
	Key            string         `db:"setting_key"`
	Value          sql.NullString `db:"setting_value"`
	Type           string         `db:"value_type"`
	Description    sql.NullString `db:"description"`
}

type settingDefinition struct {
	key          string
	typ          string
	description  string
	defaultValue string
	orgValue     *string
}

type effectiveSetting struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default"`
	Overridden  bool   `json:"overridden"`
}
