package utilities

import (
	"database/sql"
	"time"
)

type MeterRow struct {
	ID            uint64        `db:"id"`
	PropertyID    uint64        `db:"property_id"`
	UnitID        sql.NullInt64 `db:"unit_id"`
	UtilityType   string        `db:"utility_type"`
	MeterNumber   string        `db:"meter_number"`
	UnitOfMeasure string        `db:"unit_of_measure"`
	RatePerUnit   float64       `db:"rate_per_unit"`
	Status        string        `db:"status"`
	CreatedAt     time.Time     `db:"created_at"`
	UpdatedAt     time.Time     `db:"updated_at"`
}

// MeterViewRow adds the property and unit the meter hangs off, the last reading
// taken and how many readings still have no bill, so a list can be triaged
// without a second round trip per meter.
type MeterViewRow struct {
	MeterRow
	PropertyName    string          `db:"property_name"`
	PropertyCode    sql.NullString  `db:"property_code"`
	UnitNumber      sql.NullString  `db:"unit_number"`
	LastReadingDate sql.NullTime    `db:"last_reading_date"`
	LastReadingVal  sql.NullFloat64 `db:"last_reading_value"`
	OpenReadings    int             `db:"open_readings"`
}

type ReadingRow struct {
	ID              uint64         `db:"id"`
	MeterID         uint64         `db:"meter_id"`
	ReadingDate     time.Time      `db:"reading_date"`
	PreviousReading float64        `db:"previous_reading"`
	CurrentReading  float64        `db:"current_reading"`
	Consumption     float64        `db:"consumption"`
	RecordedBy      sql.NullInt64  `db:"recorded_by"`
	Notes           sql.NullString `db:"notes"`
	CreatedAt       time.Time      `db:"created_at"`
	UpdatedAt       time.Time      `db:"updated_at"`
}

// ReadingViewRow carries the meter and the bill that was raised from the
// reading. Whether a bill exists decides if the reading may still be corrected.
type ReadingViewRow struct {
	ReadingRow
	PropertyID     uint64         `db:"property_id"`
	PropertyName   string         `db:"property_name"`
	UnitID         sql.NullInt64  `db:"unit_id"`
	UnitNumber     sql.NullString `db:"unit_number"`
	MeterNumber    string         `db:"meter_number"`
	UtilityType    string         `db:"utility_type"`
	UnitOfMeasure  string         `db:"unit_of_measure"`
	RatePerUnit    float64        `db:"rate_per_unit"`
	RecordedName   sql.NullString `db:"recorded_name"`
	BillID         sql.NullInt64  `db:"bill_id"`
	BillStatus     sql.NullString `db:"bill_status"`
	NextBillStatus sql.NullString `db:"next_bill_status"`
}

type BillRow struct {
	ID          uint64        `db:"id"`
	UnitID      uint64        `db:"unit_id"`
	MeterID     sql.NullInt64 `db:"meter_id"`
	ReadingID   sql.NullInt64 `db:"reading_id"`
	InvoiceID   sql.NullInt64 `db:"invoice_id"`
	Type        string        `db:"type"`
	Amount      float64       `db:"amount"`
	DateDue     time.Time     `db:"date_due"`
	PeriodStart time.Time     `db:"period_start"`
	PeriodEnd   time.Time     `db:"period_end"`
	Status      string        `db:"status"`
	CreatedAt   time.Time     `db:"created_at"`
	UpdatedAt   time.Time     `db:"updated_at"`
}

// BillViewRow adds who the bill is for and, once billing has picked it up, the
// invoice carrying it. The lease shown is the active one on the unit at read
// time, which is what a pending bill is waiting to be attached to.
type BillViewRow struct {
	BillRow
	PropertyID    uint64          `db:"property_id"`
	PropertyName  string          `db:"property_name"`
	PropertyCode  sql.NullString  `db:"property_code"`
	UnitNumber    string          `db:"unit_number"`
	MeterNumber   sql.NullString  `db:"meter_number"`
	UtilityType   sql.NullString  `db:"meter_utility_type"`
	ReadingDate   sql.NullTime    `db:"reading_date"`
	Consumption   sql.NullFloat64 `db:"consumption"`
	InvoiceNumber sql.NullString  `db:"invoice_number"`
	InvoiceStatus sql.NullString  `db:"invoice_status"`
	LeaseID       sql.NullInt64   `db:"lease_id"`
	LeaseNumber   sql.NullString  `db:"lease_number"`
	TenantName    sql.NullString  `db:"tenant_name"`
}

// UnitRefRow is the unit as this module needs it: the property it belongs to and
// the active lease on it, which together decide whether a bill can be raised,
// who it is billed to and which day of the month that tenant pays on.
type UnitRefRow struct {
	ID          uint64        `db:"id"`
	PropertyID  uint64        `db:"property_id"`
	Number      string        `db:"number"`
	Status      string        `db:"status"`
	LeaseID     sql.NullInt64 `db:"lease_id"`
	LeaseDueDay sql.NullInt64 `db:"lease_due_day"`
}

// MeterSnapRow is the meter as the bill path needs it: the property and unit it
// hangs off, the type that labels the charge and the rate that prices it.
type MeterSnapRow struct {
	ID            uint64        `db:"id"`
	PropertyID    uint64        `db:"property_id"`
	UnitID        sql.NullInt64 `db:"unit_id"`
	MeterNumber   string        `db:"meter_number"`
	UtilityType   string        `db:"utility_type"`
	UnitOfMeasure string        `db:"unit_of_measure"`
	RatePerUnit   float64       `db:"rate_per_unit"`
	Status        string        `db:"status"`
}

// ReadingSnapRow is the reading as the bill path needs it: the meter, property
// and unit it belongs to, the quantity it produced and the bill, if any, that
// already carries it.
type ReadingSnapRow struct {
	ID              uint64        `db:"id"`
	MeterID         uint64        `db:"meter_id"`
	PropertyID      uint64        `db:"property_id"`
	UnitID          sql.NullInt64 `db:"unit_id"`
	MeterStatus     string        `db:"meter_status"`
	UtilityType     string        `db:"utility_type"`
	RatePerUnit     float64       `db:"rate_per_unit"`
	ReadingDate     time.Time     `db:"reading_date"`
	PrevReadingDate sql.NullTime  `db:"previous_reading_date"`
	PreviousReading float64       `db:"previous_reading"`
	CurrentReading  float64       `db:"current_reading"`
	Consumption     float64       `db:"consumption"`
	BillID          sql.NullInt64 `db:"bill_id"`
}

// UnitLookRow is a unit as the lookups endpoint needs it: enough to label a
// dropdown and group it under its property.
type UnitLookRow struct {
	ID           uint64         `db:"id"`
	PropertyID   uint64         `db:"property_id"`
	Number       string         `db:"number"`
	PropertyName string         `db:"property_name"`
	PropertyCode sql.NullString `db:"property_code"`
}

// OpenReadingRow is one unbilled reading together with everything a bill needs
// to be raised from it: the unit and its active lease, the rate that prices the
// consumption, and the previous reading date that opens the period.
type OpenReadingRow struct {
	ReadingID    uint64        `db:"reading_id"`
	MeterID      uint64        `db:"meter_id"`
	UnitID       uint64        `db:"unit_id"`
	PropertyName string        `db:"property_name"`
	UnitNumber   string        `db:"unit_number"`
	UtilityType  string        `db:"utility_type"`
	RatePerUnit  float64       `db:"rate_per_unit"`
	Consumption  float64       `db:"consumption"`
	ReadingDate  time.Time     `db:"reading_date"`
	PeriodStart  sql.NullTime  `db:"period_start"`
	LeaseID      sql.NullInt64 `db:"lease_id"`
	LeaseDueDay  sql.NullInt64 `db:"lease_due_day"`
}

// BillTotals is the money position over the bills a caller can see.
type BillTotals struct {
	PendingCount int     `db:"pending_count"`
	PendingTotal float64 `db:"pending_total"`
	BilledTotal  float64 `db:"billed_total"`
}

// GenerateFilter is the parsed selection the bill-generating query runs on. The
// wire input carries its dates as text; they are parsed once, here, so the SQL
// never compares a date column to a string.
type GenerateFilter struct {
	MeterID    uint64
	PropertyID uint64
	UnitID     uint64
	From       time.Time
	To         time.Time
}
