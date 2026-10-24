package leases

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
	UnitID     uint64
	TenantID   uint64
	Page       httpx.Page
	Sort       string
}

type PropertyRefDTO struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
	Code string `json:"code,omitempty"`
}

type UnitRefDTO struct {
	ID       uint64         `json:"id"`
	Number   string         `json:"number"`
	Property PropertyRefDTO `json:"property"`
}

type LeaseTenantDTO struct {
	TenantID uint64 `json:"tenant_id"`
	Role     string `json:"role"`
	Name     string `json:"name"`
	Email    string `json:"email,omitempty"`
	Phone    string `json:"phone,omitempty"`
}

type DepositDTO struct {
	ID       uint64     `json:"id"`
	Amount   float64    `json:"amount"`
	DatePaid *time.Time `json:"date_paid"`
	Status   string     `json:"status"`
}

type LeaseSummaryDTO struct {
	ID            uint64     `json:"id"`
	LeaseNumber   string     `json:"lease_number"`
	Unit          UnitRefDTO `json:"unit"`
	StartDate     time.Time  `json:"start_date"`
	EndDate       *time.Time `json:"end_date"`
	RentAmount    float64    `json:"rent_amount"`
	DepositAmount float64    `json:"deposit_amount"`
	Status        string     `json:"status"`
	PrimaryTenant string     `json:"primary_tenant,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type LeaseDetailDTO struct {
	LeaseSummaryDTO
	BillingDay        int              `json:"billing_day"`
	DueDay            int              `json:"due_day"`
	NoticePeriodDays  int              `json:"notice_period_days"`
	SignedAt          *time.Time       `json:"signed_at"`
	TerminatedAt      *time.Time       `json:"terminated_at"`
	TerminationReason string           `json:"termination_reason,omitempty"`
	Tenants           []LeaseTenantDTO `json:"tenants"`
	Deposits          []DepositDTO     `json:"deposits"`
}

type LeaseListDTO struct {
	Leases []LeaseSummaryDTO
	Total  int
}

type TenantRef struct {
	TenantID uint64 `json:"tenant_id"`
	Role     string `json:"role"`
}

type CreateInput struct {
	UnitID           uint64      `json:"unit_id"`
	LeaseNumber      string      `json:"lease_number"`
	StartDate        string      `json:"start_date"`
	EndDate          *string     `json:"end_date"`
	RentAmount       float64     `json:"rent_amount"`
	DepositAmount    *float64    `json:"deposit_amount"`
	BillingDay       *int        `json:"billing_day"`
	DueDay           *int        `json:"due_day"`
	NoticePeriodDays *int        `json:"notice_period_days"`
	Tenants          []TenantRef `json:"tenants"`
}

// RawMessage end_date distinguishes an omitted field from an explicit JSON
// null, which turns the lease into an open-ended (periodic) tenancy.
type UpdateInput struct {
	UnitID           *uint64         `json:"unit_id"`
	LeaseNumber      *string         `json:"lease_number"`
	StartDate        *string         `json:"start_date"`
	EndDate          json.RawMessage `json:"end_date"`
	RentAmount       *float64        `json:"rent_amount"`
	DepositAmount    *float64        `json:"deposit_amount"`
	BillingDay       *int            `json:"billing_day"`
	DueDay           *int            `json:"due_day"`
	NoticePeriodDays *int            `json:"notice_period_days"`
	Tenants          *[]TenantRef    `json:"tenants"`
}

type ActivateInput struct {
	SignedAt *string `json:"signed_at"`
}

type TerminateInput struct {
	Reason       string  `json:"reason"`
	TerminatedAt *string `json:"terminated_at"`
}

// RenewInput continues a lease on the same unit. months is only used when
// end_date is omitted, so a renewal always ends on a date the server computed.
type RenewInput struct {
	LeaseNumber   *string  `json:"lease_number"`
	StartDate     *string  `json:"start_date"`
	EndDate       *string  `json:"end_date"`
	Months        *int     `json:"months"`
	RentAmount    *float64 `json:"rent_amount"`
	DepositAmount *float64 `json:"deposit_amount"`
	SignedAt      *string  `json:"signed_at"`
}

type ReplaceTenantsInput struct {
	Tenants []TenantRef `json:"tenants"`
}
