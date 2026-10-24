package leases

import (
	"database/sql"
	"time"
)

type LeaseRow struct {
	ID                uint64         `db:"id"`
	OrganizationID    uint64         `db:"organization_id"`
	UnitID            uint64         `db:"unit_id"`
	LeaseNumber       string         `db:"lease_number"`
	StartDate         time.Time      `db:"start_date"`
	EndDate           sql.NullTime   `db:"end_date"`
	RentAmount        float64        `db:"rent_amount"`
	DepositAmount     float64        `db:"deposit_amount"`
	BillingDay        int            `db:"billing_day"`
	DueDay            int            `db:"due_day"`
	NoticePeriodDays  int            `db:"notice_period_days"`
	Status            string         `db:"status"`
	SignedAt          sql.NullTime   `db:"signed_at"`
	TerminatedAt      sql.NullTime   `db:"terminated_at"`
	TerminationReason sql.NullString `db:"termination_reason"`
	CreatedBy         sql.NullInt64  `db:"created_by"`
	CreatedAt         time.Time      `db:"created_at"`
	UpdatedAt         time.Time      `db:"updated_at"`
}

// LeaseViewRow adds the unit and property the lease hangs off plus the primary
// tenant's name. Units and properties are not filtered on deleted_at: a lease
// outlives the record that hosted it, so history must stay readable.
type LeaseViewRow struct {
	LeaseRow
	UnitNumber    string         `db:"unit_number"`
	PropertyID    uint64         `db:"property_id"`
	PropertyName  string         `db:"property_name"`
	PropertyCode  sql.NullString `db:"property_code"`
	PrimaryTenant sql.NullString `db:"primary_tenant"`
}

type UnitRefRow struct {
	ID                uint64         `db:"id"`
	PropertyID        uint64         `db:"property_id"`
	Number            string         `db:"number"`
	Status            string         `db:"status"`
	UnitDeletedAt     sql.NullTime   `db:"unit_deleted_at"`
	PropertyName      string         `db:"property_name"`
	PropertyCode      sql.NullString `db:"property_code"`
	PropertyDeletedAt sql.NullTime   `db:"property_deleted_at"`
}

type TenantRosterRow struct {
	TenantID uint64         `db:"tenant_id"`
	Role     string         `db:"role"`
	FullName string         `db:"full_name"`
	Email    string         `db:"email"`
	Phone    sql.NullString `db:"phone"`
}

type DepositRow struct {
	ID       uint64       `db:"id"`
	Amount   float64      `db:"amount"`
	DatePaid sql.NullTime `db:"date_paid"`
	Status   string       `db:"status"`
}
