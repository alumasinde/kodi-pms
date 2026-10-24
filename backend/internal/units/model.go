package units

import (
	"database/sql"
	"time"
)

type UnitRow struct {
	ID             uint64        `db:"id"`
	OrganizationID uint64        `db:"organization_id"`
	PropertyID     uint64        `db:"property_id"`
	TypeID         uint64        `db:"type_id"`
	Number         string        `db:"number"`
	Floor          sql.NullInt64 `db:"floor"`
	Bedrooms       int           `db:"bedrooms"`
	Bathrooms      float64       `db:"bathrooms"`
	SquareFeet     sql.NullInt64 `db:"square_feet"`
	Rent           float64       `db:"rent"`
	Status         string        `db:"status"`
	CreatedAt      time.Time     `db:"created_at"`
	UpdatedAt      time.Time     `db:"updated_at"`
}

// UnitViewRow adds the property, unit type and the occupant taken from the one
// active lease the schema allows per unit.
type UnitViewRow struct {
	UnitRow
	PropertyName    string          `db:"property_name"`
	PropertyCode    sql.NullString  `db:"property_code"`
	TypeName        string          `db:"type_name"`
	TypeDescription sql.NullString  `db:"type_description"`
	LeaseID         sql.NullInt64   `db:"lease_id"`
	LeaseNumber     sql.NullString  `db:"lease_number"`
	LeaseStart      sql.NullTime    `db:"lease_start"`
	LeaseEnd        sql.NullTime    `db:"lease_end"`
	LeaseRent       sql.NullFloat64 `db:"lease_rent"`
	TenantName      sql.NullString  `db:"tenant_name"`
}

type LookupRow struct {
	ID          uint64         `db:"id"`
	Name        string         `db:"name"`
	Description sql.NullString `db:"description"`
}
