package mpesa

import (
	"strings"
	"time"
)

// The vocabulary this module writes, all of it constrained by migration 000007:
// the shortcode environment and account type, the C2B queue status, the STK
// result status, and the payment methods the seeded reference data offers.
const (
	envSandbox     = "sandbox"
	envProduction  = "production"
	typePaybill    = "paybill"
	typeTill       = "till"
	transUnmatched = "unmatched"
	transMatched   = "matched"
	transRefunded  = "refunded"
	transIgnored   = "ignored"
	stkPending     = "pending"
	stkSuccess     = "success"
	stkFailed      = "failed"
	stkCancelled   = "cancelled"
	stkTimeout     = "timeout"

	// A receipt is booked against the method that matches how it arrived, so the
	// payment rows an accountant reconciles look the same as the ones a cashier
	// types in. mpesa_stk is the method for money that came from a prompt.
	methodPaybill = "mpesa_paybill"
	methodTill    = "mpesa_till"
	methodSTK     = "mpesa_stk"

	// Daraja answers a callback with ResultCode 0 to mean "accepted". Anything
	// else tells Safaricom the receipt was not taken, so it retries.
	callbackAccepted = 0
	callbackRejected = 1

	defaultShortcodeSort = "m.shortcode ASC"
	defaultRefSort       = "m.shortcode ASC, r.account_reference ASC"
	defaultTransSort     = "mt.trans_time DESC"
	defaultSTKSort       = "ms.created_at DESC"

	// The result codes Daraja reports for an STK prompt the tenant did not
	// complete. They are not failures of the system, and the queue should say so.
	resultCancelled = 891
	resultTimeout   = 1010

	// leaseDraft is the one lease status a prompt may never be sent against: a
	// draft lease is not a tenancy yet, so nobody owes it anything. Every other
	// status is fair game, including a terminated lease with arrears on it.
	leaseDraft = "draft"

	maxShortcode     = 20
	maxInitiator     = 100
	maxAccountRef    = 100
	maxSearch        = 100
	maxNotes         = 255
	maxPhone         = 20
	maxDescription   = 80
	maxResultDesc    = 255
	maxAmount        = 9999999999.99
	maxCallbackBody  = 1 << 20
	tokenBytes       = 32
	oauthTTL         = 25 * time.Minute
	dateLayout       = "2006-01-02"
	darajaTimeLayout = "20060102150405"
)

var (
	environments  = []string{envSandbox, envProduction}
	accountTypes  = []string{typePaybill, typeTill}
	transStatuses = []string{transUnmatched, transMatched, transRefunded, transIgnored}
	stkStatuses   = []string{stkPending, stkSuccess, stkFailed, stkCancelled, stkTimeout}

	environmentMessage = "must be one of " + strings.Join(environments, ", ")
	accountTypeMessage = "must be one of " + strings.Join(accountTypes, ", ")
	transStatusMessage = "must be one of " + strings.Join(transStatuses, ", ")
	stkStatusMessage   = "must be one of " + strings.Join(stkStatuses, ", ")

	shortcodeSortColumns = map[string]string{
		"shortcode":   "m.shortcode",
		"environment": "m.environment",
		"type":        "m.account_type",
		"status":      "m.is_active",
		"created_at":  "m.created_at",
		"updated_at":  "m.updated_at",
	}
	refSortColumns = map[string]string{
		"reference":  "r.account_reference",
		"shortcode":  "m.shortcode",
		"lease":      "l.lease_number",
		"created_at": "r.created_at",
		"updated_at": "r.updated_at",
	}
	transactionSortColumns = map[string]string{
		"time":    "mt.trans_time",
		"amount":  "mt.amount",
		"status":  "mt.status",
		"ref":     "mt.bill_ref_number",
		"msisdn":  "mt.msisdn",
		"created": "mt.created_at",
		"updated": "mt.updated_at",
	}
	stkSortColumns = map[string]string{
		"created":  "ms.created_at",
		"updated":  "ms.updated_at",
		"amount":   "ms.amount",
		"status":   "ms.status",
		"phone":    "ms.phone",
		"checkout": "ms.checkout_request_id",
	}
)
