package mpesa

import (
	"database/sql"
	"time"
)

// ShortcodeRow is one organization's Daraja account. The *_enc columns are
// AES-256-GCM ciphertext produced by internal/platform/pii and callback_token_hash
// is the keyed HMAC of the webhook token, so a database dump hands an attacker
// neither the credentials nor a token they can replay.
type ShortcodeRow struct {
	ID                    uint64         `db:"id"`
	OrganizationID        uint64         `db:"organization_id"`
	Shortcode             string         `db:"shortcode"`
	AccountType           string         `db:"account_type"`
	Environment           string         `db:"environment"`
	InitiatorName         sql.NullString `db:"initiator_name"`
	ConsumerKeyEnc        []byte         `db:"consumer_key_enc"`
	ConsumerSecretEnc     []byte         `db:"consumer_secret_enc"`
	PasskeyEnc            []byte         `db:"passkey_enc"`
	SecurityCredentialEnc []byte         `db:"security_credential_enc"`
	CallbackTokenHash     sql.NullString `db:"callback_token_hash"`
	CallbackTokenEnc      []byte         `db:"callback_token_enc"`
	IsActive              int8           `db:"is_active"`
	CreatedAt             time.Time      `db:"created_at"`
	UpdatedAt             time.Time      `db:"updated_at"`
}

// HasCredentials reports whether this shortcode can talk to Daraja at all. A
// shortcode created for routing receipts can legitimately have no credentials.
func (r *ShortcodeRow) HasCredentials() bool {
	return len(r.ConsumerKeyEnc) > 0 && len(r.ConsumerSecretEnc) > 0 && len(r.PasskeyEnc) > 0
}

type AccountRefRow struct {
	ID               uint64         `db:"id"`
	OrganizationID   uint64         `db:"organization_id"`
	ShortcodeID      uint64         `db:"shortcode_id"`
	LeaseID          uint64         `db:"lease_id"`
	AccountReference string         `db:"account_reference"`
	IsActive         int8           `db:"is_active"`
	CreatedAt        time.Time      `db:"created_at"`
	UpdatedAt        time.Time      `db:"updated_at"`
	Shortcode        string         `db:"shortcode"`
	Environment      string         `db:"environment"`
	LeaseNumber      sql.NullString `db:"lease_number"`
	UnitNumber       sql.NullString `db:"unit_number"`
	PropertyName     sql.NullString `db:"property_name"`
	TenantName       sql.NullString `db:"tenant_name"`
}

// TransactionRow is a C2B receipt: either a confirmation Safaricom pushed, or a
// record an operator typed in from an SMS. PaymentID is the only column that
// makes it money, and only billing may fill it in.
type TransactionRow struct {
	ID                uint64         `db:"id"`
	OrganizationID    uint64         `db:"organization_id"`
	TransID           string         `db:"trans_id"`
	TransType         sql.NullString `db:"trans_type"`
	BusinessShortCode string         `db:"business_short_code"`
	ShortcodeID       sql.NullInt64  `db:"shortcode_id"`
	BillRefNumber     sql.NullString `db:"bill_ref_number"`
	LeaseID           sql.NullInt64  `db:"lease_id"`
	Msisdn            sql.NullString `db:"msisdn"`
	FirstName         sql.NullString `db:"first_name"`
	Amount            float64        `db:"amount"`
	TransTime         time.Time      `db:"trans_time"`
	Status            string         `db:"status"`
	PaymentID         sql.NullInt64  `db:"payment_id"`
	RawPayload        sql.NullString `db:"raw_payload"`
	CreatedAt         time.Time      `db:"created_at"`
	UpdatedAt         time.Time      `db:"updated_at"`
	LeaseNumber       sql.NullString `db:"lease_number"`
	UnitNumber        sql.NullString `db:"unit_number"`
	PropertyName      sql.NullString `db:"property_name"`
	TenantName        sql.NullString `db:"tenant_name"`
	Shortcode         sql.NullString `db:"shortcode"`
}

// STKRequestRow is one prompt and, when it completed, what Daraja said about it.
// MpesaReceipt is the C2B receipt of the same payment, which is why payment_id
// stays empty when a confirmation already booked the money.
type STKRequestRow struct {
	ID                uint64         `db:"id"`
	OrganizationID    uint64         `db:"organization_id"`
	ShortcodeID       sql.NullInt64  `db:"shortcode_id"`
	LeaseID           sql.NullInt64  `db:"lease_id"`
	InvoiceID         sql.NullInt64  `db:"invoice_id"`
	Phone             string         `db:"phone"`
	Amount            float64        `db:"amount"`
	MerchantRequestID sql.NullString `db:"merchant_request_id"`
	CheckoutRequestID string         `db:"checkout_request_id"`
	Status            string         `db:"status"`
	ResultCode        sql.NullInt64  `db:"result_code"`
	ResultDesc        sql.NullString `db:"result_desc"`
	MpesaReceipt      sql.NullString `db:"mpesa_receipt"`
	PaymentID         sql.NullInt64  `db:"payment_id"`
	CreatedAt         time.Time      `db:"created_at"`
	UpdatedAt         time.Time      `db:"updated_at"`
	Shortcode         sql.NullString `db:"shortcode"`
	Environment       sql.NullString `db:"environment"`
	LeaseNumber       sql.NullString `db:"lease_number"`
	UnitNumber        sql.NullString `db:"unit_number"`
	PropertyName      sql.NullString `db:"property_name"`
	TenantName        sql.NullString `db:"tenant_name"`
	InvoiceNumber     sql.NullString `db:"invoice_number"`
}

// GatewayLeaseRow is the lease a prompt or a matched receipt is attached to, with
// the tenant contact a push needs. It is read through the caller's scope, so an
// out-of-scope lease is invisible rather than forbidden.
type GatewayLeaseRow struct {
	ID           uint64         `db:"id"`
	Organization uint64         `db:"organization_id"`
	UnitID       uint64         `db:"unit_id"`
	PropertyID   uint64         `db:"property_id"`
	LeaseNumber  string         `db:"lease_number"`
	Status       string         `db:"status"`
	UnitNumber   string         `db:"unit_number"`
	PropertyName string         `db:"property_name"`
	PropertyCode sql.NullString `db:"property_code"`
	TenantName   sql.NullString `db:"tenant_name"`
	TenantPhone  sql.NullString `db:"tenant_phone"`
}

// GatewayInvoiceRow is the bill a prompt asks the tenant to pay. Balance is the
// generated column, so it already reflects every allocation booked until now.
type GatewayInvoiceRow struct {
	ID            uint64    `db:"id"`
	LeaseID       uint64    `db:"lease_id"`
	InvoiceNumber string    `db:"invoice_number"`
	Total         float64   `db:"total"`
	AmountPaid    float64   `db:"amount_paid"`
	Balance       float64   `db:"balance"`
	Status        string    `db:"status"`
	DueDate       time.Time `db:"due_date"`
}
