package units

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
	Search     string
	Status     string
	PropertyID uint64
	TypeID     uint64
	Page       httpx.Page
	Sort       string
}

type LookupDTO struct {
	ID          uint64 `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type UnitLookupsDTO struct {
	UnitTypes []LookupDTO `json:"unit_types"`
	Amenities []LookupDTO `json:"amenities"`
}

type PropertyRefDTO struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
	Code string `json:"code,omitempty"`
}

type OccupantDTO struct {
	LeaseID     uint64     `json:"lease_id"`
	LeaseNumber string     `json:"lease_number"`
	StartDate   time.Time  `json:"start_date"`
	EndDate     *time.Time `json:"end_date"`
	MonthlyRent float64    `json:"monthly_rent"`
	TenantName  string     `json:"tenant_name,omitempty"`
}

type UnitSummaryDTO struct {
	ID         uint64         `json:"id"`
	Number     string         `json:"number"`
	Property   PropertyRefDTO `json:"property"`
	Type       LookupDTO      `json:"type"`
	Floor      *int           `json:"floor"`
	Bedrooms   int            `json:"bedrooms"`
	Bathrooms  float64        `json:"bathrooms"`
	SquareFeet *int           `json:"square_feet"`
	Rent       float64        `json:"rent"`
	Status     string         `json:"status"`
	Occupant   *OccupantDTO   `json:"occupant"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type UnitDetailDTO struct {
	UnitSummaryDTO
	Amenities []LookupDTO `json:"amenities"`
}

type UnitListDTO struct {
	Units []UnitSummaryDTO
	Total int
}

type CreateInput struct {
	PropertyID uint64   `json:"property_id"`
	TypeID     uint64   `json:"type_id"`
	Number     string   `json:"number"`
	Floor      *int     `json:"floor"`
	Bedrooms   *int     `json:"bedrooms"`
	Bathrooms  *float64 `json:"bathrooms"`
	SquareFeet *int     `json:"square_feet"`
	Rent       *float64 `json:"rent"`
	Status     string   `json:"status"`
	AmenityIDs []uint64 `json:"amenity_ids"`
}

// GenerateInput fills a property with numbered units in one call. Numbers are
// prefix + a padded counter starting at first_number.
type GenerateInput struct {
	PropertyID  uint64   `json:"property_id"`
	TypeID      uint64   `json:"type_id"`
	Prefix      string   `json:"prefix"`
	FirstNumber *int     `json:"first_number"`
	Count       int      `json:"count"`
	Pad         *int     `json:"pad"`
	Floor       *int     `json:"floor"`
	Bedrooms    *int     `json:"bedrooms"`
	Bathrooms   *float64 `json:"bathrooms"`
	SquareFeet  *int     `json:"square_feet"`
	Rent        *float64 `json:"rent"`
	AmenityIDs  []uint64 `json:"amenity_ids"`
}

type GenerateResultDTO struct {
	Created int              `json:"created"`
	Units   []UnitSummaryDTO `json:"units"`
}

// RawMessage fields distinguish an omitted field (nil) from an explicit JSON
// null, which clears a nullable column.
type UpdateInput struct {
	TypeID     *uint64         `json:"type_id"`
	Number     *string         `json:"number"`
	Floor      json.RawMessage `json:"floor"`
	SquareFeet json.RawMessage `json:"square_feet"`
	Bedrooms   *int            `json:"bedrooms"`
	Bathrooms  *float64        `json:"bathrooms"`
	Rent       *float64        `json:"rent"`
	Status     *string         `json:"status"`
	AmenityIDs *[]uint64       `json:"amenity_ids"`
}
