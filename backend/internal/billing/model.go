package billing

import (
	"database/sql"
	"time"
)

// InvoiceRow mirrors the invoices table. balance is a generated column and
// amount_paid is maintained by triggers from live payment allocations, so this
// package reads both and never writes amount_paid.
type InvoiceRow struct {
	ID                 uint64         `db:"id"`
	OrganizationID     uint64         `db:"organization_id"`
	LeaseID            uint64         `db:"lease_id"`
	InvoiceNumber      string         `db:"invoice_number"`
	InvoiceType        string         `db:"invoice_type"`
	BillingPeriodStart time.Time      `db:"billing_period_start"`
	BillingPeriodEnd   time.Time      `db:"billing_period_end"`
	IssueDate          time.Time      `db:"issue_date"`
	DueDate            time.Time      `db:"due_date"`
	Subtotal           float64        `db:"subtotal"`
	TaxTotal           float64        `db:"tax_total"`
	Total              float64        `db:"total"`
	AmountPaid         float64        `db:"amount_paid"`
	Balance            float64        `db:"balance"`
	Status             string         `db:"status"`
	Notes              sql.NullString `db:"notes"`
	CreatedBy          sql.NullInt64  `db:"created_by"`
	CreatedAt          time.Time      `db:"created_at"`
	UpdatedAt          time.Time      `db:"updated_at"`
}

// InvoiceViewRow adds the lease, unit, property and primary tenant so a list
// row can be rendered without a second query. Parents are joined without a
// deleted_at filter: an invoice outlives the records it was written against.
type InvoiceViewRow struct {
	InvoiceRow
	LeaseNumber   string         `db:"lease_number"`
	UnitNumber    string         `db:"unit_number"`
	PropertyID    uint64         `db:"property_id"`
	PropertyName  string         `db:"property_name"`
	PropertyCode  sql.NullString `db:"property_code"`
	PrimaryTenant sql.NullString `db:"primary_tenant"`
}

type InvoiceItemRow struct {
	ID             uint64         `db:"id"`
	OrganizationID uint64         `db:"organization_id"`
	InvoiceID      uint64         `db:"invoice_id"`
	ItemType       string         `db:"item_type"`
	Description    string         `db:"description"`
	Quantity       float64        `db:"quantity"`
	UnitPrice      float64        `db:"unit_price"`
	TaxRate        float64        `db:"tax_rate"`
	Amount         float64        `db:"amount"`
	SourceType     sql.NullString `db:"source_type"`
	SourceID       sql.NullInt64  `db:"source_id"`
}

type PaymentRow struct {
	ID              uint64         `db:"id"`
	OrganizationID  uint64         `db:"organization_id"`
	LeaseID         uint64         `db:"lease_id"`
	PaymentMethodID uint64         `db:"payment_method_id"`
	Amount          float64        `db:"amount"`
	PaidAt          time.Time      `db:"paid_at"`
	Reference       sql.NullString `db:"reference"`
	Status          string         `db:"status"`
	ReceivedBy      sql.NullInt64  `db:"received_by"`
	Notes           sql.NullString `db:"notes"`
	CreatedAt       time.Time      `db:"created_at"`
	UpdatedAt       time.Time      `db:"updated_at"`
}

// PaymentViewRow carries the method, the lease and how much of the money is
// still unallocated, which is the tenant's spendable credit.
type PaymentViewRow struct {
	PaymentRow
	MethodCode     string         `db:"method_code"`
	MethodName     string         `db:"method_name"`
	LeaseNumber    string         `db:"lease_number"`
	UnitNumber     string         `db:"unit_number"`
	PropertyID     uint64         `db:"property_id"`
	PropertyName   string         `db:"property_name"`
	Allocated      float64        `db:"allocated"`
	ReversedAt     sql.NullTime   `db:"reversed_at"`
	ReversalReason sql.NullString `db:"reversal_reason"`
	ReversalBy     sql.NullInt64  `db:"reversal_by"`
}

type AllocationRow struct {
	ID            uint64       `db:"id"`
	PaymentID     uint64       `db:"payment_id"`
	InvoiceID     uint64       `db:"invoice_id"`
	Amount        float64      `db:"amount"`
	ReversedAt    sql.NullTime `db:"reversed_at"`
	CreatedAt     time.Time    `db:"created_at"`
	InvoiceNumber string       `db:"invoice_number"`
	IssueDate     time.Time    `db:"issue_date"`
}

type LateFeeRow struct {
	ID             uint64         `db:"id"`
	OrganizationID uint64         `db:"organization_id"`
	InvoiceID      uint64         `db:"invoice_id"`
	Amount         float64        `db:"amount"`
	DateAssessed   time.Time      `db:"date_assessed"`
	Status         string         `db:"status"`
	WaivedBy       sql.NullInt64  `db:"waived_by"`
	WaivedReason   sql.NullString `db:"waived_reason"`
	CreatedAt      time.Time      `db:"created_at"`
	UpdatedAt      time.Time      `db:"updated_at"`
}

type LedgerAccountRow struct {
	Code        string `db:"code"`
	Name        string `db:"name"`
	AccountType string `db:"account_type"`
	IsSystem    int    `db:"is_system"`
}

type LedgerEntryRow struct {
	ID             uint64         `db:"id"`
	OrganizationID uint64         `db:"organization_id"`
	LeaseID        sql.NullInt64  `db:"lease_id"`
	JournalID      string         `db:"journal_id"`
	EntryDate      time.Time      `db:"entry_date"`
	AccountCode    string         `db:"account_code"`
	DebitAmount    float64        `db:"debit_amount"`
	CreditAmount   float64        `db:"credit_amount"`
	CurrencyCode   string         `db:"currency_code"`
	SourceType     string         `db:"source_type"`
	SourceID       uint64         `db:"source_id"`
	Memo           sql.NullString `db:"memo"`
	CreatedBy      sql.NullInt64  `db:"created_by"`
	CreatedAt      time.Time      `db:"created_at"`
}

// LedgerViewRow is a posting joined to its account and lease. Property is
// carried so a property-scoped user only sees postings for their buildings.
type LedgerViewRow struct {
	LedgerEntryRow
	AccountName   string         `db:"account_name"`
	AccountType   string         `db:"account_type"`
	LeaseNumber   sql.NullString `db:"lease_number"`
	UnitNumber    sql.NullString `db:"unit_number"`
	PropertyID    uint64         `db:"property_id"`
	PropertyName  sql.NullString `db:"property_name"`
	PrimaryTenant sql.NullString `db:"primary_tenant"`
}

type CreditNoteRow struct {
	ID               uint64         `db:"id"`
	OrganizationID   uint64         `db:"organization_id"`
	LeaseID          uint64         `db:"lease_id"`
	InvoiceID        sql.NullInt64  `db:"invoice_id"`
	CreditNoteNumber string         `db:"credit_note_number"`
	Amount           float64        `db:"amount"`
	Reason           sql.NullString `db:"reason"`
	Status           string         `db:"status"`
	IssueDate        time.Time      `db:"issue_date"`
	AppliedAt        sql.NullTime   `db:"applied_at"`
	CreatedBy        sql.NullInt64  `db:"created_by"`
	CreatedAt        time.Time      `db:"created_at"`
	UpdatedAt        time.Time      `db:"updated_at"`
}

type CreditNoteViewRow struct {
	CreditNoteRow
	LeaseNumber   string         `db:"lease_number"`
	UnitNumber    string         `db:"unit_number"`
	PropertyID    uint64         `db:"property_id"`
	PropertyName  string         `db:"property_name"`
	InvoiceNumber sql.NullString `db:"invoice_number"`
	PrimaryTenant sql.NullString `db:"primary_tenant"`
}

type TenantCreditRow struct {
	ID             uint64         `db:"id"`
	OrganizationID uint64         `db:"organization_id"`
	LeaseID        sql.NullInt64  `db:"lease_id"`
	TenantID       sql.NullInt64  `db:"tenant_id"`
	InvoiceID      sql.NullInt64  `db:"invoice_id"`
	EntryType      string         `db:"entry_type"`
	Amount         float64        `db:"amount"`
	SourceType     sql.NullString `db:"source_type"`
	SourceID       sql.NullInt64  `db:"source_id"`
	Notes          sql.NullString `db:"notes"`
	CreatedBy      sql.NullInt64  `db:"created_by"`
	CreatedAt      time.Time      `db:"created_at"`
}

// BillingLeaseRow is the lease snapshot the billing engine needs: what to
// charge, when to charge it and whether a deposit is still owed.
type BillingLeaseRow struct {
	ID             uint64         `db:"id"`
	OrganizationID uint64         `db:"organization_id"`
	LeaseNumber    string         `db:"lease_number"`
	UnitID         uint64         `db:"unit_id"`
	PropertyID     uint64         `db:"property_id"`
	PropertyCode   sql.NullString `db:"property_code"`
	UnitNumber     string         `db:"unit_number"`
	StartDate      time.Time      `db:"start_date"`
	EndDate        sql.NullTime   `db:"end_date"`
	RentAmount     float64        `db:"rent_amount"`
	DepositAmount  float64        `db:"deposit_amount"`
	BillingDay     int            `db:"billing_day"`
	DueDay         int            `db:"due_day"`
	Status         string         `db:"status"`
	PendingDeposit sql.NullInt64  `db:"pending_deposit"`
	LastRentPeriod sql.NullTime   `db:"last_rent_period"`
}

// InvoiceBalance is the money view of one invoice: the slice an allocator may
// consume. lease_id and rent_amount ride along so a payment or the late-fee
// sweep can validate and price from a single read.
type InvoiceBalance struct {
	ID            uint64    `db:"id"`
	LeaseID       uint64    `db:"lease_id"`
	InvoiceNumber string    `db:"invoice_number"`
	DueDate       time.Time `db:"due_date"`
	Total         float64   `db:"total"`
	AmountPaid    float64   `db:"amount_paid"`
	Status        string    `db:"status"`
	Balance       float64   `db:"balance"`
	RentAmount    float64   `db:"rent_amount"`
}

// CreditSourceRow is one payment whose money has not been allocated yet.
type CreditSourceRow struct {
	PaymentID   uint64    `db:"payment_id"`
	PaidAt      time.Time `db:"paid_at"`
	Unallocated float64   `db:"unallocated"`
}

type PaymentMethodRow struct {
	ID          uint64         `db:"id"`
	Code        string         `db:"code"`
	Name        string         `db:"name"`
	Description sql.NullString `db:"description"`
	IsActive    int            `db:"is_active"`
}

// SummaryRow is the arrears and credit roll-up behind GET /billing/summary.
type SummaryRow struct {
	Billed      float64 `db:"billed"`
	Collected   float64 `db:"collected"`
	Outstanding float64 `db:"outstanding"`
	Overdue     float64 `db:"overdue_amount"`
	Credit      float64 `db:"credit"`
	OpenCount   int     `db:"open_count"`
	OverdueRows int     `db:"overdue_count"`
	DraftCount  int     `db:"draft_count"`
}

// BillingSettings are the effective system settings that drive numbering,
// billing dates and late fees. Organization rows win over the global defaults.
type BillingSettings struct {
	InvoicePrefix    string
	CurrencyCode     string
	RentDueDay       int
	LateFeeGraceDays int
	LateFeeType      string
	LateFeeValue     float64
	VATRate          float64
}

type organizationClock struct {
	ID       uint64 `db:"id"`
	Timezone string `db:"timezone"`
}
