package mpesa

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"kodipms/internal/audit"
)

// C2B is the channel where a tenant types a paybill number and an account number on
// their phone. Safaricom asks two questions about it: validation, before the money
// moves, and confirmation, after it has. They are different jobs and this module
// treats them that way -- validation is an opinion about an account reference,
// confirmation is a record of cash.

// ValidateReceipt answers a C2B validation callback. It stores nothing.
//
// A validation is not money: the tenant can still walk away, and a rejected one is
// reversed by the network, so writing a row here would fill the reconcile queue with
// payments that never happened. What it does decide is whether this account number is
// one this organization recognizes, which is the only useful answer available before
// the cash moves and the reason the callback exists at all.
func (s *Service) ValidateReceipt(ctx context.Context, token string, in C2BValidationRequest) CallbackReply {
	shortcode, err := s.shortcodeByToken(ctx, token)
	if err != nil {
		return rejectCallback("This paybill is not recognized")
	}
	provided := firstText(in.BusinessShortCode)
	if provided != shortcode.Shortcode {
		s.log.Warn("mpesa validation names another shortcode",
			"org_id", shortcode.OrganizationID, "token_shortcode", shortcode.Shortcode, "provided", provided)
		return rejectCallback("The shortcode in this request does not match its token")
	}

	reference := strings.TrimSpace(in.BillRefNumber.String())
	if reference == "" {
		// A buy-goods till has no account number to check, so there is nothing here
		// worth refusing a payment over.
		if shortcode.AccountType == typeTill {
			return acceptCallback()
		}
		return rejectCallback("An account number is required")
	}
	amount, ok := firstAmount(in.Amount)
	if !ok || amount <= 0 {
		return rejectCallback("The amount could not be read")
	}

	ref, err := s.repo.AccountRefByNumber(ctx, s.db, shortcode.OrganizationID, shortcode.ID, reference)
	if errors.Is(err, sql.ErrNoRows) {
		// Refusing here is the kindness: the tenant is told to type the right number
		// and their balance is never touched, instead of paying into a queue.
		s.log.Info("mpesa validation refused an unknown account reference",
			"org_id", shortcode.OrganizationID, "shortcode", shortcode.Shortcode, "reference", reference)
		return rejectCallback("That account number is not known for this paybill")
	}
	if err != nil {
		s.log.Error("look up mpesa account reference for validation", "org_id", shortcode.OrganizationID, "error", err)
		return rejectCallback("This request cannot be checked right now")
	}
	if ref.IsActive == 0 {
		return rejectCallback("That account number is retired")
	}
	return acceptCallback()
}

// ConfirmReceipt records a C2B confirmation and spends it if it can be placed.
//
// The reply is always the shape Safaricom reads. Anything other than ResultCode 0
// tells the network the receipt was not taken, which is correct when this
// installation genuinely could not record it -- and why an unreadable amount is
// refused outright rather than stored as zero: a refused confirmation is reversed to
// the tenant, while an unrecorded one is money nobody can find.
func (s *Service) ConfirmReceipt(ctx context.Context, token string, in C2BConfirmationRequest, raw []byte, meta ClientMeta) CallbackReply {
	shortcode, err := s.shortcodeByToken(ctx, token)
	if err != nil {
		return rejectCallback("This paybill is not recognized")
	}
	provided := firstText(in.PaybillShopNumber, in.TillNumber, in.BusinessShortCode)
	if provided == "" {
		provided = shortcode.Shortcode
	}
	if provided != shortcode.Shortcode {
		s.log.Error("mpesa confirmation names another shortcode",
			"org_id", shortcode.OrganizationID, "token_shortcode", shortcode.Shortcode, "provided", provided)
		return rejectCallback("The shortcode in this request does not match its token")
	}

	transID := strings.TrimSpace(in.TransactionID.String())
	if transID == "" {
		return rejectCallback("This confirmation carries no transaction id")
	}
	amount, ok := firstAmount(in.Amount, in.TransAmount)
	if !ok {
		s.log.Error("mpesa confirmation has an unreadable amount",
			"org_id", shortcode.OrganizationID, "trans_id", transID,
			"amount", firstText(in.Amount, in.TransAmount))
		return rejectCallback("The amount could not be read, so this payment is not recorded")
	}
	when, ok := channelTime(firstText(in.TransactionDate))
	if !ok {
		when = time.Now().UTC()
	}

	booking := receiptBooking{
		OrganizationID:    shortcode.OrganizationID,
		ShortcodeID:       shortcode.ID,
		TransID:           transID,
		TransType:         firstText(in.TransactionType),
		BusinessShortCode: provided,
		BillRefNumber:     firstText(in.BillRefNumber, in.MerchantReceiptNumber),
		Msisdn:            s.receiptPhone(firstText(in.Msisdn)),
		FirstName:         confirmationName(in),
		Amount:            amount,
		TransTime:         when,
		PaymentMethodID:   s.methodForAccountType(ctx, shortcode.AccountType),
		Notes:             "M-Pesa confirmation on " + shortcode.Shortcode,
		RawPayload:        string(raw),
	}
	outcome, err := s.recordReceipt(ctx, booking, meta)
	if err != nil {
		s.log.Error("record mpesa confirmation", "org_id", shortcode.OrganizationID, "trans_id", transID, "error", err)
		return rejectCallback("This receipt could not be recorded, please retry")
	}
	if outcome.ForeignReceipt {
		return rejectCallback("This receipt already belongs to another account")
	}

	switch {
	case outcome.AlreadyBooked:
		s.log.Info("duplicate mpesa confirmation ignored", "org_id", shortcode.OrganizationID, "trans_id", transID)
	case outcome.Payment != nil:
		s.writeAudit(ctx, audit.Entry{
			OrgID:  shortcode.OrganizationID,
			Action: "mpesa_transactions.matched", EntityType: "mpesa_transactions", EntityID: outcome.TransactionID,
			NewValues: map[string]any{
				"trans_id": transID, "amount": booking.Amount, "lease_id": outcome.LeaseID,
				"payment_id": outcome.Payment.PaymentID, "reference": booking.BillRefNumber,
			},
			IPAddress: meta.IP, UserAgent: meta.UserAgent,
		})
	default:
		// The money arrived and nothing knows whose rent it is. This is the queue's
		// whole purpose, so the receipt is kept and whoever reconciles is told.
		s.log.Warn("unmatched mpesa receipt queued", "org_id", shortcode.OrganizationID,
			"trans_id", transID, "amount", booking.Amount, "reference", booking.BillRefNumber)
		s.writeAudit(ctx, audit.Entry{
			OrgID:  shortcode.OrganizationID,
			Action: "mpesa_transactions.unmatched", EntityType: "mpesa_transactions", EntityID: outcome.TransactionID,
			NewValues: map[string]any{"trans_id": transID, "amount": booking.Amount, "reference": booking.BillRefNumber},
			IPAddress: meta.IP, UserAgent: meta.UserAgent,
		})
	}
	return acceptCallback()
}

// methodForAccountType books a receipt against the method that matches how it
// arrived, so an accountant's payment list reads the same however the money came in.
// A missing method row is not fatal: the receipt simply stays unmatched.
func (s *Service) methodForAccountType(ctx context.Context, accountType string) uint64 {
	code := methodPaybill
	if accountType == typeTill {
		code = methodTill
	}
	id, err := s.repo.PaymentMethodIDByCode(ctx, code)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.log.Error("look up payment method", "code", code, "error", err)
		}
		return 0
	}
	return id
}

// receiptPhone normalizes the caller's line into the 254 form the queue is searched
// by. A number in a shape this code does not know is stored as it arrived rather than
// dropped, because a receipt is worth keeping even when the phone on it is not.
func (s *Service) receiptPhone(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	normalized, err := normalizeMsisdn(value)
	if err != nil {
		return truncate(value, 64)
	}
	return normalized
}

// confirmationName is the payer's name as the reconcile queue shows it. Safaricom
// sends ClientName for some channels and three separate names for others.
func confirmationName(in C2BConfirmationRequest) string {
	if client := strings.TrimSpace(in.ClientName.String()); client != "" {
		return truncate(client, 100)
	}
	parts := make([]string, 0, 3)
	for _, part := range []Scalar{in.FirstName, in.MiddleName, in.LastName} {
		if text := strings.TrimSpace(part.String()); text != "" {
			parts = append(parts, text)
		}
	}
	return truncate(strings.Join(parts, " "), 100)
}

// firstText returns the first value a callback actually filled, in the order the
// channel is known to send it.
func firstText(values ...Scalar) string {
	for _, value := range values {
		if text := strings.TrimSpace(value.String()); text != "" {
			return text
		}
	}
	return ""
}

// firstAmount reads an amount from whichever spelling this channel used. Daraja
// quotes it, unquotes it, and occasionally pads it with a thousands separator, and a
// confirmation that cannot be read is worth treating as one.
func firstAmount(values ...Scalar) (float64, bool) {
	for _, value := range values {
		if amount, ok := parseAmount(value.String()); ok {
			return amount, true
		}
	}
	return 0, false
}

func parseAmount(value string) (float64, bool) {
	trimmed := strings.TrimSpace(strings.NewReplacer(",", "", " ", "").Replace(value))
	if trimmed == "" || strings.ContainsAny(trimmed, "eE") {
		return 0, false
	}
	amount, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0, false
	}
	return roundToCents(amount), true
}

func acceptCallback() CallbackReply {
	return CallbackReply{ResultCode: callbackAccepted, ResultDesc: "Accepted"}
}

func rejectCallback(reason string) CallbackReply {
	return CallbackReply{ResultCode: callbackRejected, ResultDesc: truncate(reason, maxResultDesc)}
}
