package billing

import (
	"encoding/json"
	"time"

	"kodipms/internal/platform/httpx"
)

type ClientMeta struct {
	IP        string
	UserAgent string
}

type InvoiceQuery struct {
	Search     string
	Status     string
	Type       string
	LeaseID    uint64
	PropertyID uint64
	UnitID     uint64
	TenantID   uint64
	Overdue    bool
	DueFrom    time.Time
	DueTo      time.Time
	Page       httpx.Page
	Sort       string
}

type PaymentQuery struct {
	Search     string
	Status     string
	LeaseID    uint64
	PropertyID uint64
	MethodID   uint64
	From       time.Time
	To         time.Time
	Page       httpx.Page
	Sort       string
}

type LedgerQuery struct {
	AccountCode string
	SourceType  string
	SourceID    uint64
	LeaseID     uint64
	PropertyID  uint64
	From        time.Time
	To          time.Time
	Page        httpx.Page
	Sort        string
}

type CreditNoteQuery struct {
	Search     string
	Status     string
	LeaseID    uint64
	InvoiceID  uint64
	PropertyID uint64
	Page       httpx.Page
	Sort       string
}

// ItemInput is one invoice line. The line amount and the invoice totals are
// computed by the server from quantity, unit price and tax rate, so a client
// can never set money directly.
type ItemInput struct {
	ItemType    string   `json:"item_type"`
	Description string   `json:"description"`
	Quantity    *float64 `json:"quantity"`
	UnitPrice   float64  `json:"unit_price"`
	TaxRate     *float64 `json:"tax_rate"`
	SourceType  string   `json:"source_type"`
	SourceID    uint64   `json:"source_id"`
}

type CreateInvoiceInput struct {
	LeaseID            uint64      `json:"lease_id"`
	InvoiceNumber      string      `json:"invoice_number"`
	InvoiceType        string      `json:"invoice_type"`
	BillingPeriodStart string      `json:"billing_period_start"`
	BillingPeriodEnd   string      `json:"billing_period_end"`
	IssueDate          *string     `json:"issue_date"`
	DueDate            *string     `json:"due_date"`
	Notes              string      `json:"notes"`
	Items              []ItemInput `json:"items"`
	Issue              bool        `json:"issue"`
}

// UpdateInvoiceInput only edits a draft invoice. status and amount_paid are not
// writable at all, so a payment can never be invented through the invoice API.
type UpdateInvoiceInput struct {
	InvoiceNumber      *string         `json:"invoice_number"`
	InvoiceType        *string         `json:"invoice_type"`
	BillingPeriodStart *string         `json:"billing_period_start"`
	BillingPeriodEnd   *string         `json:"billing_period_end"`
	IssueDate          *string         `json:"issue_date"`
	DueDate            *string         `json:"due_date"`
	Notes              json.RawMessage `json:"notes"`
	Items              *[]ItemInput    `json:"items"`
}

type IssueInput struct {
	IssueDate *string `json:"issue_date"`
	DueDate   *string `json:"due_date"`
}

type VoidInput struct {
	Reason string `json:"reason"`
}

// ApplyCreditInput settles an invoice from the lease's unallocated payments.
// An omitted amount applies as much credit as the invoice still owes.
type ApplyCreditInput struct {
	Amount *float64 `json:"amount"`
}

type LateFeeInput struct {
	Amount *float64 `json:"amount"`
	Reason string   `json:"reason"`
}

type WaiveInput struct {
	Reason string `json:"reason"`
}

type AllocationInput struct {
	InvoiceID uint64  `json:"invoice_id"`
	Amount    float64 `json:"amount"`
}

// PaymentInput records money received. Allocations may be given explicitly or
// omitted to let the server apply the payment to the oldest open invoices;
// anything left over stays with the lease as tenant credit.
type PaymentInput struct {
	LeaseID         uint64             `json:"lease_id"`
	PaymentMethodID uint64             `json:"payment_method_id"`
	Amount          float64            `json:"amount"`
	PaidAt          *string            `json:"paid_at"`
	Reference       *string            `json:"reference"`
	Notes           *string            `json:"notes"`
	Allocations     *[]AllocationInput `json:"allocations"`
}

type ReversePaymentInput struct {
	Reason string `json:"reason"`
}

type CreateCreditNoteInput struct {
	LeaseID          uint64  `json:"lease_id"`
	InvoiceID        uint64  `json:"invoice_id"`
	CreditNoteNumber string  `json:"credit_note_number"`
	Amount           float64 `json:"amount"`
	Reason           string  `json:"reason"`
	IssueDate        *string `json:"issue_date"`
}

type UpdateCreditNoteInput struct {
	InvoiceID        *uint64         `json:"invoice_id"`
	CreditNoteNumber *string         `json:"credit_note_number"`
	Amount           *float64        `json:"amount"`
	Reason           json.RawMessage `json:"reason"`
	IssueDate        *string         `json:"issue_date"`
}

// IssueCreditNoteInput names the invoice the note settles. The schema freezes
// invoice_id the moment a note leaves draft, so issuing is the last chance to
// point a note at an invoice.
type IssueCreditNoteInput struct {
	InvoiceID *uint64 `json:"invoice_id"`
}

type VoidCreditNoteInput struct {
	Reason string `json:"reason"`
}

type LeaseRefDTO struct {
	ID       uint64         `json:"id"`
	Number   string         `json:"lease_number"`
	Unit     string         `json:"unit_number,omitempty"`
	Property PropertyRefDTO `json:"property"`
}

type PropertyRefDTO struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
	Code string `json:"code,omitempty"`
}

type InvoiceItemDTO struct {
	ID          uint64  `json:"id"`
	ItemType    string  `json:"item_type"`
	Description string  `json:"description"`
	Quantity    float64 `json:"quantity"`
	UnitPrice   float64 `json:"unit_price"`
	TaxRate     float64 `json:"tax_rate"`
	Amount      float64 `json:"amount"`
	SourceType  string  `json:"source_type,omitempty"`
	SourceID    uint64  `json:"source_id,omitempty"`
}

type AllocationDTO struct {
	ID            uint64     `json:"id"`
	PaymentID     uint64     `json:"payment_id"`
	InvoiceID     uint64     `json:"invoice_id"`
	InvoiceNumber string     `json:"invoice_number,omitempty"`
	Amount        float64    `json:"amount"`
	ReversedAt    *time.Time `json:"reversed_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

type LateFeeDTO struct {
	ID           uint64    `json:"id"`
	Amount       float64   `json:"amount"`
	DateAssessed time.Time `json:"date_assessed"`
	Status       string    `json:"status"`
	WaivedBy     uint64    `json:"waived_by,omitempty"`
	WaivedReason string    `json:"waived_reason,omitempty"`
}

type InvoiceSummaryDTO struct {
	ID            uint64      `json:"id"`
	InvoiceNumber string      `json:"invoice_number"`
	InvoiceType   string      `json:"invoice_type"`
	Lease         LeaseRefDTO `json:"lease"`
	PrimaryTenant string      `json:"primary_tenant,omitempty"`
	PeriodStart   time.Time   `json:"billing_period_start"`
	PeriodEnd     time.Time   `json:"billing_period_end"`
	IssueDate     time.Time   `json:"issue_date"`
	DueDate       time.Time   `json:"due_date"`
	Subtotal      float64     `json:"subtotal"`
	TaxTotal      float64     `json:"tax_total"`
	Total         float64     `json:"total"`
	AmountPaid    float64     `json:"amount_paid"`
	Balance       float64     `json:"balance"`
	Status        string      `json:"status"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

type InvoiceDetailDTO struct {
	InvoiceSummaryDTO
	Notes       string           `json:"notes,omitempty"`
	Items       []InvoiceItemDTO `json:"items"`
	Allocations []AllocationDTO  `json:"allocations"`
	LateFees    []LateFeeDTO     `json:"late_fees"`
}

type InvoiceListDTO struct {
	Invoices []InvoiceSummaryDTO
	Total    int
}

type PaymentMethodDTO struct {
	ID          uint64 `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	IsActive    bool   `json:"is_active"`
}

type PaymentSummaryDTO struct {
	ID             uint64       `json:"id"`
	Amount         float64      `json:"amount"`
	Allocated      float64      `json:"allocated"`
	Credit         float64      `json:"credit"`
	Reference      string       `json:"reference,omitempty"`
	PaidAt         time.Time    `json:"paid_at"`
	Status         string       `json:"status"`
	Method         MethodRefDTO `json:"method"`
	Lease          LeaseRefDTO  `json:"lease"`
	ReceivedBy     uint64       `json:"received_by,omitempty"`
	Notes          string       `json:"notes,omitempty"`
	ReversedAt     *time.Time   `json:"reversed_at"`
	ReversalReason string       `json:"reversal_reason,omitempty"`
	Reversal       *ReversalDTO `json:"reversal,omitempty"`
}

type MethodRefDTO struct {
	ID   uint64 `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type ReversalDTO struct {
	Amount     float64   `json:"amount"`
	Reason     string    `json:"reason,omitempty"`
	ReversedBy uint64    `json:"reversed_by,omitempty"`
	ReversedAt time.Time `json:"reversed_at"`
}

type PaymentDetailDTO struct {
	PaymentSummaryDTO
	Allocations []AllocationDTO `json:"allocations"`
}

type PaymentListDTO struct {
	Payments []PaymentSummaryDTO
	Total    int
}

type LedgerAccountDTO struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	AccountType string `json:"account_type"`
	IsSystem    bool   `json:"is_system"`
}

type LedgerEntryDTO struct {
	ID           uint64    `json:"id"`
	JournalID    string    `json:"journal_id"`
	EntryDate    time.Time `json:"entry_date"`
	AccountCode  string    `json:"account_code"`
	AccountName  string    `json:"account_name,omitempty"`
	AccountType  string    `json:"account_type,omitempty"`
	DebitAmount  float64   `json:"debit_amount"`
	CreditAmount float64   `json:"credit_amount"`
	CurrencyCode string    `json:"currency_code"`
	SourceType   string    `json:"source_type"`
	SourceID     uint64    `json:"source_id"`
	Memo         string    `json:"memo,omitempty"`
	LeaseID      uint64    `json:"lease_id,omitempty"`
	LeaseNumber  string    `json:"lease_number,omitempty"`
	CreatedBy    uint64    `json:"created_by,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type LedgerListDTO struct {
	Entries []LedgerEntryDTO
	Total   int
}

type CreditNoteSummaryDTO struct {
	ID               uint64      `json:"id"`
	CreditNoteNumber string      `json:"credit_note_number"`
	Lease            LeaseRefDTO `json:"lease"`
	InvoiceID        uint64      `json:"invoice_id,omitempty"`
	InvoiceNumber    string      `json:"invoice_number,omitempty"`
	PrimaryTenant    string      `json:"primary_tenant,omitempty"`
	Amount           float64     `json:"amount"`
	Reason           string      `json:"reason,omitempty"`
	Status           string      `json:"status"`
	IssueDate        time.Time   `json:"issue_date"`
	AppliedAt        *time.Time  `json:"applied_at"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
}

type CreditNoteDetailDTO struct {
	CreditNoteSummaryDTO
	Ledger []LedgerEntryDTO `json:"ledger"`
}

type CreditNoteListDTO struct {
	CreditNotes []CreditNoteSummaryDTO
	Total       int
}

type TenantCreditDTO struct {
	ID        uint64    `json:"id"`
	LeaseID   uint64    `json:"lease_id,omitempty"`
	EntryType string    `json:"entry_type"`
	Amount    float64   `json:"amount"`
	InvoiceID uint64    `json:"invoice_id,omitempty"`
	Notes     string    `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type BillingSummaryDTO struct {
	Currency     string  `json:"currency"`
	Billed       float64 `json:"billed"`
	Collected    float64 `json:"collected"`
	Outstanding  float64 `json:"outstanding"`
	Overdue      float64 `json:"overdue"`
	Credit       float64 `json:"credit"`
	OpenCount    int     `json:"open_invoices"`
	OverdueCount int     `json:"overdue_invoices"`
	DraftCount   int     `json:"draft_invoices"`
}
