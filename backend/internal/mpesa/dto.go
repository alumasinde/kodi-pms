package mpesa

import (
	"encoding/json"
	"strings"
	"time"

	"kodipms/internal/platform/httpx"
)

type ClientMeta struct {
	IP        string
	UserAgent string
}

// Scalar is a callback value Safaricom sends as a quoted string in one channel
// ("Amount":"100.00") and as a bare number in another, sometimes for the same
// field across versions. Reading it as a string keeps one decode path.
type Scalar string

func (s *Scalar) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	switch {
	case trimmed == "null":
		*s = ""
	case strings.HasPrefix(trimmed, `"`):
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		*s = Scalar(text)
	default:
		// A bare number or boolean: the literal text is the value to keep.
		*s = Scalar(trimmed)
	}
	return nil
}

func (s Scalar) String() string { return string(s) }

// ShortcodeDTO is the safe half of an mpesa_shortcodes row. Credentials are never
// returned, not even masked: a consumer secret is not a value anybody should be
// shown on a screen, and the booleans below answer the only question a screen has.
type ShortcodeDTO struct {
	ID                uint64    `json:"id"`
	Shortcode         string    `json:"shortcode"`
	AccountType       string    `json:"account_type"`
	Environment       string    `json:"environment"`
	InitiatorName     string    `json:"initiator_name,omitempty"`
	HasConsumerKey    bool      `json:"has_consumer_key"`
	HasConsumerSecret bool      `json:"has_consumer_secret"`
	HasPasskey        bool      `json:"has_passkey"`
	HasSecurityCred   bool      `json:"has_security_credential"`
	HasCallbackToken  bool      `json:"has_callback_token"`
	IsActive          bool      `json:"is_active"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// SecretDTO is what a create or a token rotation returns once, and the only
// response in this module that carries a callback token. The three URLs are what an
// operator pastes into the Daraja portal; after this response the token exists
// nowhere the app can read it back.
type SecretDTO struct {
	ShortcodeDTO
	CallbackToken   string `json:"callback_token"`
	ValidationURL   string `json:"validation_url"`
	ConfirmationURL string `json:"confirmation_url"`
	STKCallbackURL  string `json:"stk_callback_url"`
}

type ShortcodeListDTO struct {
	Shortcodes []ShortcodeDTO
	Total      int
}

type RefListDTO struct {
	References []AccountRefDTO
	Total      int
}

type TransactionListDTO struct {
	Transactions []TransactionDTO
	Total        int
}

type STKListDTO struct {
	Requests []STKRequestDTO
	Total    int
}

// MpesaLookupsDTO is what the M-Pesa screens offer as choices. The three paths are
// shown with a {TOKEN} placeholder rather than a live token: the route shape is
// documentation, the token is a secret handed out once.
type MpesaLookupsDTO struct {
	Environments       []string `json:"environments"`
	AccountTypes       []string `json:"account_types"`
	ReceiptStatuses    []string `json:"receipt_statuses"`
	PromptStatuses     []string `json:"prompt_statuses"`
	PaymentMethodCodes []string `json:"payment_method_codes"`
	ValidationPath     string   `json:"validation_path"`
	ConfirmationPath   string   `json:"confirmation_path"`
	STKCallbackPath    string   `json:"stk_callback_path"`
	GatewayConfigured  bool     `json:"gateway_configured"`
	BillingConnected   bool     `json:"billing_connected"`
}

type CreateShortcodeInput struct {
	Shortcode          string  `json:"shortcode"`
	AccountType        string  `json:"account_type"`
	Environment        string  `json:"environment"`
	InitiatorName      *string `json:"initiator_name"`
	ConsumerKey        *string `json:"consumer_key"`
	ConsumerSecret     *string `json:"consumer_secret"`
	Passkey            *string `json:"passkey"`
	SecurityCredential *string `json:"security_credential"`
	IsActive           *bool   `json:"is_active"`
}

type UpdateShortcodeInput struct {
	Shortcode          *string `json:"shortcode"`
	AccountType        *string `json:"account_type"`
	Environment        *string `json:"environment"`
	InitiatorName      *string `json:"initiator_name"`
	ConsumerKey        *string `json:"consumer_key"`
	ConsumerSecret     *string `json:"consumer_secret"`
	Passkey            *string `json:"passkey"`
	SecurityCredential *string `json:"security_credential"`
	IsActive           *bool   `json:"is_active"`
}

type AccountRefDTO struct {
	ID               uint64    `json:"id"`
	ShortcodeID      uint64    `json:"shortcode_id"`
	Shortcode        string    `json:"shortcode"`
	Environment      string    `json:"environment"`
	LeaseID          uint64    `json:"lease_id"`
	AccountReference string    `json:"account_reference"`
	IsActive         bool      `json:"is_active"`
	LeaseNumber      string    `json:"lease_number,omitempty"`
	UnitNumber       string    `json:"unit_number,omitempty"`
	PropertyName     string    `json:"property_name,omitempty"`
	TenantName       string    `json:"tenant_name,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type CreateRefInput struct {
	ShortcodeID      uint64 `json:"shortcode_id"`
	LeaseID          uint64 `json:"lease_id"`
	AccountReference string `json:"account_reference"`
}

type TransactionDTO struct {
	ID                uint64    `json:"id"`
	TransID           string    `json:"trans_id"`
	TransType         string    `json:"trans_type,omitempty"`
	BusinessShortCode string    `json:"business_short_code"`
	ShortcodeID       uint64    `json:"shortcode_id,omitempty"`
	BillRefNumber     string    `json:"bill_ref_number,omitempty"`
	Msisdn            string    `json:"msisdn,omitempty"`
	FirstName         string    `json:"first_name,omitempty"`
	Amount            float64   `json:"amount"`
	TransTime         time.Time `json:"trans_time"`
	Status            string    `json:"status"`
	PaymentID         uint64    `json:"payment_id,omitempty"`
	LeaseID           uint64    `json:"lease_id,omitempty"`
	LeaseNumber       string    `json:"lease_number,omitempty"`
	UnitNumber        string    `json:"unit_number,omitempty"`
	PropertyName      string    `json:"property_name,omitempty"`
	TenantName        string    `json:"tenant_name,omitempty"`
	// RawPayload is the callback body as it arrived, and is returned on a single
	// receipt only: it is what an accountant argues with when the account number
	// matches nothing.
	RawPayload json.RawMessage `json:"raw_payload,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

type STKRequestDTO struct {
	ID                uint64    `json:"id"`
	ShortcodeID       uint64    `json:"shortcode_id,omitempty"`
	Shortcode         string    `json:"shortcode,omitempty"`
	Environment       string    `json:"environment,omitempty"`
	LeaseID           uint64    `json:"lease_id,omitempty"`
	LeaseNumber       string    `json:"lease_number,omitempty"`
	UnitNumber        string    `json:"unit_number,omitempty"`
	PropertyName      string    `json:"property_name,omitempty"`
	TenantName        string    `json:"tenant_name,omitempty"`
	InvoiceID         uint64    `json:"invoice_id,omitempty"`
	InvoiceNumber     string    `json:"invoice_number,omitempty"`
	Phone             string    `json:"phone"`
	Amount            float64   `json:"amount"`
	MerchantRequestID string    `json:"merchant_request_id,omitempty"`
	CheckoutRequestID string    `json:"checkout_request_id"`
	Status            string    `json:"status"`
	ResultCode        *int      `json:"result_code"`
	ResultDesc        string    `json:"result_desc,omitempty"`
	MpesaReceipt      string    `json:"mpesa_receipt,omitempty"`
	PaymentID         uint64    `json:"payment_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// PushInput asks Daraja to prompt a phone for payment. Amount and phone may both
// be left out: the amount then becomes the invoice balance, or the rent the lease
// carries, and the phone becomes the primary tenant's.
type PushInput struct {
	ShortcodeID uint64   `json:"shortcode_id"`
	LeaseID     uint64   `json:"lease_id"`
	InvoiceID   uint64   `json:"invoice_id"`
	Phone       *string  `json:"phone"`
	Amount      *float64 `json:"amount"`
	Description *string  `json:"description"`
}

type MatchInput struct {
	LeaseID uint64 `json:"lease_id"`
}

type IgnoreInput struct {
	Reason string `json:"reason"`
}

type ShortcodeQuery struct {
	Search      string
	Environment string
	AccountType string
	Status      string
	Page        httpx.Page
	Sort        string
}

type RefQuery struct {
	Search      string
	ShortcodeID uint64
	Page        httpx.Page
	Sort        string
}

type TransactionQuery struct {
	Search      string
	Status      string
	ShortcodeID uint64
	LeaseID     uint64
	From        time.Time
	To          time.Time
	Page        httpx.Page
	Sort        string
}

type STKQuery struct {
	Search  string
	Status  string
	LeaseID uint64
	From    time.Time
	To      time.Time
	Page    httpx.Page
	Sort    string
}

// CallbackReply is the body Safaricom reads. It is deliberately not the API's
// envelope: a validation callback that answers {"data":...} is read as a failure
// and the tenant's payment is reversed by the network.
type CallbackReply struct {
	ResultCode int    `json:"ResultCode"`
	ResultDesc string `json:"ResultDesc"`
}

// C2BValidationRequest is the body of a Daraja C2B validation callback. Safaricom's
// own spellings are kept, misspelling included, and the fields this module does not
// read are still declared: an unknown field must never fail a decode whose whole job
// is to say yes or no about money.
type C2BValidationRequest struct {
	BusinessShortCode Scalar `json:"BusinessShortCode"`
	MerchantUnitID    Scalar `json:"MerchantUnitID"`
	BillRefNumber     Scalar `json:"BillRefNumber"`
	InvoiceNumber     Scalar `json:"InvoiceNumber"`
	Particulars       Scalar `json:"Particulars"`
	TransactionType   Scalar `json:"TransactionType"`
	TransactionID     Scalar `json:"TransactionID"`
	Amount            Scalar `json:"Amount"`
	TransTime         Scalar `json:"TransTime"`
	Msisdn            Scalar `json:"MSISDN"`
}

// C2BConfirmationRequest is the body of a confirmation callback. A paybill puts its
// number in PaybillShopNumber and a till in TillNumber, and the amount is Amount here
// but TransAmount everywhere else in Daraja, so both spellings are accepted and the
// first one that reads is used.
type C2BConfirmationRequest struct {
	ConversationID           Scalar `json:"ConversationID"`
	OriginatorConversationID Scalar `json:"OriginatorCoversationID"`
	TransactionID            Scalar `json:"TransactionID"`
	TransactionType          Scalar `json:"TransactionType"`
	BusinessShortCode        Scalar `json:"BusinessShortCode"`
	PaybillShopNumber        Scalar `json:"PaybillShopNumber"`
	TillNumber               Scalar `json:"TillNumber"`
	BillRefNumber            Scalar `json:"BillRefNumber"`
	MerchantReceiptNumber    Scalar `json:"MerchantReceiptNumber"`
	CustomerMemo             Scalar `json:"CutomerMemo"`
	CustomerMemoFixed        Scalar `json:"CustomerMemo"`
	Amount                   Scalar `json:"Amount"`
	TransAmount              Scalar `json:"TransAmount"`
	Msisdn                   Scalar `json:"MSISDN"`
	ClientName               Scalar `json:"ClientName"`
	FirstName                Scalar `json:"FirstName"`
	MiddleName               Scalar `json:"MiddleName"`
	LastName                 Scalar `json:"LastName"`
	TransactionDate          Scalar `json:"TransactionDate"`
	Status                   Scalar `json:"Status"`
	ReceiptID                Scalar `json:"ReceiptID"`
}

// STKCallbackRequest is the body of an STK push callback, which nests its fields
// under RequestBody.
type STKCallbackRequest struct {
	RequestBody struct {
		BusinessShortCode  Scalar `json:"BusinessShortCode"`
		MerchantRequestID  Scalar `json:"MerchantRequestID"`
		CheckoutRequestID  Scalar `json:"CheckoutRequestID"`
		ResultCode         Scalar `json:"ResultCode"`
		ResultDesc         Scalar `json:"ResultDesc"`
		CustomerMessage    Scalar `json:"CustomerMessage"`
		Amount             Scalar `json:"Amount"`
		MpesaReceiptNumber Scalar `json:"MpesaReceiptNumber"`
		TransactionDate    Scalar `json:"TransactionDate"`
		PhoneNumber        Scalar `json:"PhoneNumber"`
	} `json:"RequestBody"`
}
