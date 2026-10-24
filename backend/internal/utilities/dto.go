package utilities

import (
	"time"

	"kodipms/internal/platform/httpx"
)

type ClientMeta struct {
	IP        string
	UserAgent string
}

type MeterQuery struct {
	Search      string
	UtilityType string
	Status      string
	PropertyID  uint64
	UnitID      uint64
	OpenOnly    bool
	Page        httpx.Page
	Sort        string
}

type ReadingQuery struct {
	From time.Time
	To   time.Time
	Page httpx.Page
	Sort string
}

type BillQuery struct {
	Search     string
	Status     string
	Type       string
	PropertyID uint64
	UnitID     uint64
	MeterID    uint64
	Overdue    bool
	DueFrom    time.Time
	DueTo      time.Time
	Page       httpx.Page
	Sort       string
}

type CreateMeterInput struct {
	PropertyID    uint64  `json:"property_id"`
	UnitID        uint64  `json:"unit_id"`
	UtilityType   string  `json:"utility_type"`
	MeterNumber   string  `json:"meter_number"`
	UnitOfMeasure string  `json:"unit_of_measure"`
	RatePerUnit   float64 `json:"rate_per_unit"`
	Status        string  `json:"status"`
}

// UpdateMeterInput follows the module's convention for a nullable link: an
// omitted unit_id leaves the meter where it is, 0 makes it a shared
// property-level meter.
type UpdateMeterInput struct {
	UnitID        *uint64  `json:"unit_id"`
	UtilityType   *string  `json:"utility_type"`
	MeterNumber   *string  `json:"meter_number"`
	UnitOfMeasure *string  `json:"unit_of_measure"`
	RatePerUnit   *float64 `json:"rate_per_unit"`
	Status        *string  `json:"status"`
}

// CreateReadingInput takes the dial reading. previous_reading is optional: when
// it is left out the module uses the last reading on the meter, which is what
// makes the consumption the bill is priced from.
type CreateReadingInput struct {
	ReadingDate     string   `json:"reading_date"`
	PreviousReading *float64 `json:"previous_reading"`
	CurrentReading  float64  `json:"current_reading"`
	Notes           string   `json:"notes"`
}

type UpdateReadingInput struct {
	CurrentReading *float64 `json:"current_reading"`
	Notes          *string  `json:"notes"`
}

// CreateBillInput raises a charge either by hand or from a reading. Amount and
// period default from the reading when they are not sent; status is never a
// client field, because pending becomes billed only when an invoice takes it.
type CreateBillInput struct {
	UnitID      uint64   `json:"unit_id"`
	MeterID     uint64   `json:"meter_id"`
	ReadingID   uint64   `json:"reading_id"`
	Type        string   `json:"type"`
	Amount      *float64 `json:"amount"`
	DateDue     string   `json:"date_due"`
	PeriodStart string   `json:"period_start"`
	PeriodEnd   string   `json:"period_end"`
}

type UpdateBillInput struct {
	UnitID      *uint64  `json:"unit_id"`
	MeterID     *uint64  `json:"meter_id"`
	Type        *string  `json:"type"`
	Amount      *float64 `json:"amount"`
	DateDue     *string  `json:"date_due"`
	PeriodStart *string  `json:"period_start"`
	PeriodEnd   *string  `json:"period_end"`
}

// VoidBillInput carries only the reason. A bill's state is not a client field:
// pending becomes billed when billing takes it and void only through this call.
type VoidBillInput struct {
	Reason string `json:"reason"`
}

type GenerateBillsInput struct {
	MeterID    uint64 `json:"meter_id"`
	PropertyID uint64 `json:"property_id"`
	UnitID     uint64 `json:"unit_id"`
	From       string `json:"from"`
	To         string `json:"to"`
}

type PropertyRefDTO struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
	Code string `json:"code,omitempty"`
}

type UnitRefDTO struct {
	ID     uint64 `json:"id"`
	Number string `json:"number"`
}

// UnitOptionDTO is one entry in the lookups list. The property travels with it so
// a picker can group units under their building without a second request.
type UnitOptionDTO struct {
	ID           uint64 `json:"id"`
	Number       string `json:"number"`
	PropertyID   uint64 `json:"property_id"`
	PropertyName string `json:"property_name"`
}

type LeaseRefDTO struct {
	ID     uint64 `json:"id"`
	Number string `json:"number"`
}

type MeterSummaryDTO struct {
	ID            uint64         `json:"id"`
	MeterNumber   string         `json:"meter_number"`
	UtilityType   string         `json:"utility_type"`
	UnitOfMeasure string         `json:"unit_of_measure"`
	RatePerUnit   float64        `json:"rate_per_unit"`
	Status        string         `json:"status"`
	Property      PropertyRefDTO `json:"property"`
	Unit          *UnitRefDTO    `json:"unit,omitempty"`
	LastReadingAt *time.Time     `json:"last_reading_date,omitempty"`
	LastReading   *float64       `json:"last_reading,omitempty"`
	OpenReadings  int            `json:"open_readings"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type MeterDetailDTO struct {
	MeterSummaryDTO
	Readings []ReadingDTO     `json:"readings"`
	Bills    []BillSummaryDTO `json:"bills"`
}

type ReadingDTO struct {
	ID              uint64    `json:"id"`
	ReadingDate     time.Time `json:"reading_date"`
	PreviousReading float64   `json:"previous_reading"`
	CurrentReading  float64   `json:"current_reading"`
	Consumption     float64   `json:"consumption"`
	UnitOfMeasure   string    `json:"unit_of_measure,omitempty"`
	RatePerUnit     float64   `json:"rate_per_unit"`
	RecordedBy      *uint64   `json:"recorded_by,omitempty"`
	RecordedName    string    `json:"recorded_name,omitempty"`
	Notes           string    `json:"notes,omitempty"`
	BillID          *uint64   `json:"bill_id,omitempty"`
	BillStatus      string    `json:"bill_status,omitempty"`
	Adjustable      bool      `json:"adjustable"`
	CreatedAt       time.Time `json:"created_at"`
}

type ReadingListDTO struct {
	Readings []ReadingDTO `json:"readings"`
	Total    int          `json:"total"`
}

type BillSummaryDTO struct {
	ID          uint64         `json:"id"`
	Type        string         `json:"type"`
	Amount      float64        `json:"amount"`
	Status      string         `json:"status"`
	DateDue     time.Time      `json:"date_due"`
	PeriodStart time.Time      `json:"period_start"`
	PeriodEnd   time.Time      `json:"period_end"`
	Property    PropertyRefDTO `json:"property"`
	Unit        *UnitRefDTO    `json:"unit,omitempty"`
	MeterID     *uint64        `json:"meter_id,omitempty"`
	MeterNumber string         `json:"meter_number,omitempty"`
	ReadingID   *uint64        `json:"reading_id,omitempty"`
	ReadingDate *time.Time     `json:"reading_date,omitempty"`
	Consumption *float64       `json:"consumption,omitempty"`
	InvoiceID   *uint64        `json:"invoice_id,omitempty"`
	Overdue     bool           `json:"overdue"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type BillDetailDTO struct {
	BillSummaryDTO
	Lease         *LeaseRefDTO `json:"lease,omitempty"`
	TenantName    string       `json:"tenant_name,omitempty"`
	InvoiceNumber string       `json:"invoice_number,omitempty"`
	InvoiceStatus string       `json:"invoice_status,omitempty"`
}

type BillListDTO struct {
	Bills []BillSummaryDTO `json:"bills"`
	Total int              `json:"total"`
}

type MeterListDTO struct {
	Meters []MeterSummaryDTO `json:"meters"`
	Total  int               `json:"total"`
}

type GenerateResultDTO struct {
	Created []uint64 `json:"created"`
	Skipped int      `json:"skipped"`
}

type UtilitySummaryDTO struct {
	Meters       int     `json:"meters"`
	OpenReadings int     `json:"open_readings"`
	PendingCount int     `json:"pending_bills"`
	PendingTotal float64 `json:"pending_total"`
	BilledTotal  float64 `json:"billed_total"`
}

// MeterOptionDTO is one entry in the meter picker: enough to label it and to
// show what a reading on it would cost.
type MeterOptionDTO struct {
	ID            uint64  `json:"id"`
	MeterNumber   string  `json:"meter_number"`
	UtilityType   string  `json:"utility_type"`
	UnitOfMeasure string  `json:"unit_of_measure"`
	RatePerUnit   float64 `json:"rate_per_unit"`
	UnitID        *uint64 `json:"unit_id,omitempty"`
	UnitNumber    string  `json:"unit_number,omitempty"`
	PropertyName  string  `json:"property_name"`
	OpenReadings  int     `json:"open_readings"`
}

type UtilityLookupsDTO struct {
	UtilityTypes  []string         `json:"utility_types"`
	MeterStatuses []string         `json:"meter_statuses"`
	BillStatuses  []string         `json:"bill_statuses"`
	Units         []UnitOptionDTO  `json:"units"`
	Meters        []MeterOptionDTO `json:"meters"`
}
