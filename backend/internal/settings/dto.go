package settings

import "encoding/json"

type ClientMeta struct {
	IP        string
	UserAgent string
}

type OrganizationDTO struct {
	ID           uint64 `json:"id"`
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	LegalName    string `json:"legal_name,omitempty"`
	KRAPIN       string `json:"kra_pin,omitempty"`
	Email        string `json:"email,omitempty"`
	Phone        string `json:"phone,omitempty"`
	Address      string `json:"address,omitempty"`
	County       string `json:"county,omitempty"`
	CountryCode  string `json:"country_code"`
	CurrencyCode string `json:"currency_code"`
	Timezone     string `json:"timezone"`
	Status       string `json:"status"`
}

type SettingDTO struct {
	Key          string `json:"key"`
	Value        any    `json:"value"`
	ValueType    string `json:"value_type"`
	Description  string `json:"description,omitempty"`
	Overridden   bool   `json:"overridden"`
	DefaultValue any    `json:"default_value"`
}

type SettingsDTO struct {
	Organization OrganizationDTO `json:"organization"`
	Settings     []SettingDTO    `json:"settings"`
}

type UpdateInput struct {
	Organization *OrganizationPatch         `json:"organization,omitempty"`
	Settings     map[string]json.RawMessage `json:"settings,omitempty"`
}

// Nullable fields are cleared by sending an empty string.
type OrganizationPatch struct {
	Name         *string `json:"name"`
	LegalName    *string `json:"legal_name"`
	KRAPIN       *string `json:"kra_pin"`
	Email        *string `json:"email"`
	Phone        *string `json:"phone"`
	Address      *string `json:"address"`
	County       *string `json:"county"`
	CountryCode  *string `json:"country_code"`
	CurrencyCode *string `json:"currency_code"`
	Timezone     *string `json:"timezone"`
}
