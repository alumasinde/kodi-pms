package billing

import (
	"errors"
)

// The vocabulary billing is written against: the statuses the schema stores,
// the limits a request may carry, and the defaults an organization inherits
// until it configures its own.
const (
	defaultInvoiceSort    = "i.due_date DESC"
	defaultPaymentSort    = "p.paid_at DESC"
	defaultLedgerSort     = "le.entry_date DESC"
	defaultCreditNoteSort = "c.created_at DESC"

	statusDraft         = "draft"
	statusIssued        = "issued"
	statusPartiallyPaid = "partially_paid"
	statusPaid          = "paid"
	statusOverdue       = "overdue"
	statusVoid          = "void"

	paymentCompleted = "completed"
	paymentReversed  = "reversed"

	// The schema's other two payment statuses. Nothing in billing writes them
	// today — a receipt is booked when the money is in hand — but a payment
	// gateway will need them, and a caller may already filter for them.
	paymentPending = "pending"
	paymentFailed  = "failed"

	// leaseDraft mirrors the leases table's first status: billing needs to know
	// it because a draft lease is not yet a tenancy money can be booked against.
	leaseDraft = "draft"

	lateFeePending = "pending"
	lateFeeApplied = "applied"
	lateFeeWaived  = "waived"
	lateFeePaid    = "paid"
	creditDraft    = "draft"
	creditIssued   = "issued"
	creditApplied  = "applied"
	creditVoid     = "void"

	entryOverpayment = "overpayment"
	entryCreditNote  = "credit_note"
	entryApplication = "application"
	entryReversal    = "reversal"

	sourceInvoice     = "invoice"
	sourcePayment     = "payment"
	sourceAllocation  = "allocation"
	sourceReversal    = "reversal"
	sourceCreditNote  = "credit_note"
	sourceLateFee     = "late_fee"
	itemSourceLateFee = "late_fee"

	// A utility charge is measured in internal/utilities and only becomes money
	// when a line here claims it, so this is the one source type billing has to
	// keep in sync with another module's rows.
	itemSourceUtilityBill = "utility_bill"

	itemRent        = "rent"
	itemWater       = "water"
	itemElectricity = "electricity"
	itemGarbage     = "garbage"
	itemService     = "service_charge"
	itemParking     = "parking"
	itemLateFee     = "late_fee"
	itemDeposit     = "deposit"
	itemMaintenance = "maintenance"
	itemAdjustment  = "adjustment"
	itemOther       = "other"

	typeRent        = "rent"
	typeUtility     = "utility"
	typeService     = "service"
	typeMaintenance = "maintenance"
	typeAdjustment  = "adjustment"
	typeOther       = "other"

	maxAmount       = 9999999999.99
	maxNumberLen    = 50
	maxDescription  = 255
	maxReasonLen    = 255
	maxNoteLen      = 2000
	maxReferenceLen = 100
	maxSearchLen    = 100
	maxItems        = 60
	maxAllocations  = 60
	maxQuantity     = 999999.999
	maxTaxRate      = 100
	minDayOfMonth   = 1
	maxDayOfMonth   = 28

	defaultInvoicePrefix = "INV"
	defaultCurrency      = "KES"
	defaultGraceDays     = 5
	defaultLateFeeType   = "percent"
	defaultLateFeeValue  = 5.0
	defaultRentDueDay    = 5
	defaultVATRate       = 16.0

	// The books can be backfilled, but a receipt dated before this year is a
	// mistyped year rather than history, and the organization's own today is the
	// latest a payment may be dated: money cannot have arrived tomorrow.
	minPaymentYear = 2000

	dateLayout = "2006-01-02"

	notDraftInvoice     = "Only a draft invoice can be changed"
	moneyOnInvoice      = "Money is already allocated to this invoice; reverse the payment or issue a credit note instead"
	notEnoughCredit     = "This lease does not have enough unallocated credit for that amount"
	periodAlreadyBilled = "That lease already has an invoice for this period"
)

// The value lists a filter is checked against and the sort whitelists that keep
// an unknown column out of ORDER BY.
var (
	invoiceStatuses = []string{statusDraft, statusIssued, statusPartiallyPaid, statusPaid, statusOverdue, statusVoid}
	paymentStatuses = []string{paymentPending, paymentCompleted, paymentFailed, paymentReversed}
	creditStatuses  = []string{creditDraft, creditIssued, creditApplied, creditVoid}
	invoiceTypes    = []string{typeRent, typeUtility, typeService, typeMaintenance, typeAdjustment, typeOther}
	itemTypes       = []string{
		itemRent, itemWater, itemElectricity, itemGarbage, itemService, itemParking,
		itemLateFee, itemDeposit, itemMaintenance, itemAdjustment, itemOther,
	}

	invoiceSortColumns = map[string]string{
		"number":     "i.invoice_number",
		"period":     "i.billing_period_start",
		"issue_date": "i.issue_date",
		"due_date":   "i.due_date",
		"total":      "i.total",
		"paid":       "i.amount_paid",
		"balance":    "i.balance",
		"status":     "i.status",
		"type":       "i.invoice_type",
		"lease":      "l.lease_number",
		"unit":       "u.number",
		"property":   "p.name",
		"created_at": "i.created_at",
		"updated_at": "i.updated_at",
	}
	paymentSortColumns = map[string]string{
		"paid_at":    "p.paid_at",
		"amount":     "p.amount",
		"reference":  "p.reference",
		"status":     "p.status",
		"method":     "pm.code",
		"lease":      "l.lease_number",
		"created_at": "p.created_at",
	}
	ledgerSortColumns = map[string]string{
		"entry_date": "le.entry_date",
		"account":    "le.account_code",
		"debit":      "le.debit_amount",
		"credit":     "le.credit_amount",
		"source":     "le.source_type",
		"created_at": "le.created_at",
	}
	creditNoteSortColumns = map[string]string{
		"number":     "c.credit_note_number",
		"amount":     "c.amount",
		"issue_date": "c.issue_date",
		"status":     "c.status",
		"lease":      "l.lease_number",
		"invoice":    "i.invoice_number",
		"created_at": "c.created_at",
	}

	errUnbalancedJournal = errors.New("billing: refusing to post an unbalanced journal")
)
