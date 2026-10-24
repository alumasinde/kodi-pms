package properties

import (
	"database/sql"
	"time"
)

type PropertyRow struct {
	ID             uint64          `db:"id"`
	OrganizationID uint64          `db:"organization_id"`
	OwnerID        uint64          `db:"owner_id"`
	TypeID         uint64          `db:"type_id"`
	Name           string          `db:"name"`
	Code           sql.NullString  `db:"code"`
	Address        string          `db:"address"`
	Estate         sql.NullString  `db:"estate"`
	Town           sql.NullString  `db:"town"`
	County         string          `db:"county"`
	PostalCode     sql.NullString  `db:"postal_code"`
	CountryCode    string          `db:"country_code"`
	Latitude       sql.NullFloat64 `db:"latitude"`
	Longitude      sql.NullFloat64 `db:"longitude"`
	YearBuilt      sql.NullInt64   `db:"year_built"`
	Status         string          `db:"status"`
	Notes          sql.NullString  `db:"notes"`
	CreatedAt      time.Time       `db:"created_at"`
	UpdatedAt      time.Time       `db:"updated_at"`
}

type PropertyViewRow struct {
	PropertyRow
	TypeName        string         `db:"type_name"`
	TypeDescription sql.NullString `db:"type_description"`
	OwnerName       string         `db:"owner_name"`
	OwnerCompany    sql.NullString `db:"owner_company"`
	OwnerEmail      string         `db:"owner_email"`
	OwnerPhone      sql.NullString `db:"owner_phone"`
	UnitsTotal      int            `db:"units_total"`
	UnitsOccupied   int            `db:"units_occupied"`
	UnitsVacant     int            `db:"units_vacant"`
}

type LookupRow struct {
	ID          uint64         `db:"id"`
	Name        string         `db:"name"`
	Description sql.NullString `db:"description"`
}
