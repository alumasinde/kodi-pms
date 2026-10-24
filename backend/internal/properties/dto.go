package properties

import (
	"encoding/json"
	"time"

	"kodipms/internal/platform/httpx"
)

type ClientMeta struct {
	IP        string
	UserAgent string
}

type ListQuery struct {
	Search  string
	Status  string
	TypeID  uint64
	OwnerID uint64
	County  string
	Page    httpx.Page
	Sort    string
}

type LookupDTO struct {
	ID          uint64 `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type PropertyLookupsDTO struct {
	PropertyTypes []LookupDTO `json:"property_types"`
	Amenities     []LookupDTO `json:"amenities"`
}

type OwnerDTO struct {
	ID      uint64 `json:"id"`
	Name    string `json:"name"`
	Company string `json:"company,omitempty"`
	Email   string `json:"email"`
	Phone   string `json:"phone,omitempty"`
}

type UnitCountsDTO struct {
	Total    int `json:"total"`
	Occupied int `json:"occupied"`
	Vacant   int `json:"vacant"`
}

type PropertySummaryDTO struct {
	ID          uint64        `json:"id"`
	Name        string        `json:"name"`
	Code        string        `json:"code,omitempty"`
	Type        LookupDTO     `json:"type"`
	Owner       OwnerDTO      `json:"owner"`
	Address     string        `json:"address"`
	Estate      string        `json:"estate,omitempty"`
	Town        string        `json:"town,omitempty"`
	County      string        `json:"county"`
	PostalCode  string        `json:"postal_code,omitempty"`
	CountryCode string        `json:"country_code"`
	Latitude    *float64      `json:"latitude"`
	Longitude   *float64      `json:"longitude"`
	YearBuilt   *int          `json:"year_built"`
	Status      string        `json:"status"`
	Units       UnitCountsDTO `json:"units"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

type PropertyDetailDTO struct {
	PropertySummaryDTO
	Notes     string      `json:"notes,omitempty"`
	Amenities []LookupDTO `json:"amenities"`
}

type PropertyListDTO struct {
	Properties []PropertySummaryDTO
	Total      int
}

type CreateInput struct {
	OwnerID     uint64   `json:"owner_id"`
	TypeID      uint64   `json:"type_id"`
	Name        string   `json:"name"`
	Code        string   `json:"code"`
	Address     string   `json:"address"`
	Estate      string   `json:"estate"`
	Town        string   `json:"town"`
	County      string   `json:"county"`
	PostalCode  string   `json:"postal_code"`
	CountryCode string   `json:"country_code"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
	YearBuilt   *int     `json:"year_built"`
	Status      string   `json:"status"`
	Notes       string   `json:"notes"`
	AmenityIDs  []uint64 `json:"amenity_ids"`
}

// RawMessage number fields distinguish an omitted field (nil) from an explicit
// JSON null, which clears a stored value.
type UpdateInput struct {
	TypeID      *uint64         `json:"type_id"`
	Name        *string         `json:"name"`
	Code        *string         `json:"code"`
	Address     *string         `json:"address"`
	Estate      *string         `json:"estate"`
	Town        *string         `json:"town"`
	County      *string         `json:"county"`
	PostalCode  *string         `json:"postal_code"`
	CountryCode *string         `json:"country_code"`
	Latitude    json.RawMessage `json:"latitude"`
	Longitude   json.RawMessage `json:"longitude"`
	YearBuilt   json.RawMessage `json:"year_built"`
	Status      *string         `json:"status"`
	Notes       *string         `json:"notes"`
	AmenityIDs  *[]uint64       `json:"amenity_ids"`
}
