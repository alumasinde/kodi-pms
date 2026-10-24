package mpesa

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"time"

	"kodipms/internal/platform/httpx"
)

// GatewayPayment is a receipt this module has proved arrived and is handing to
// billing to spend. It mirrors billing's own gateway input, and the two stay in
// step through the adapter in internal/routes rather than an import: no domain
// package reaches into another one's Go types, which is the rule every module in
// this backend follows.
type GatewayPayment struct {
	OrganizationID  uint64
	LeaseID         uint64
	PaymentMethodID uint64
	Amount          float64
	Reference       string
	PaidAt          time.Time
	Notes           string
	Meta            ClientMeta
}

// GatewayReceipt is the part of billing's answer this module needs: which payment
// row the receipt became, and how much of it found a bill.
type GatewayReceipt struct {
	PaymentID uint64
	Amount    float64
	Allocated float64
	Credit    float64
}

// PaymentBooker is the seam to billing. Money never enters the ledger from here
// except through it, so an M-Pesa receipt gets the same oldest-due-first
// allocation, the same tenant credit and the same journal entries as a payment a
// cashier typed in.
type PaymentBooker interface {
	Book(ctx context.Context, in GatewayPayment) (GatewayReceipt, error)
}

// errNoBilling is what a deployment without the billing seam returns. It cannot be
// a silent no-op: a receipt that is stored but never booked is money nobody can
// find on a report.
var errNoBilling = httpx.Conflict("Billing is not connected, so this receipt cannot be booked")

// receiptBooking is one M-Pesa receipt in the shape this module records it in,
// whether it came from a C2B confirmation or from an STK prompt the tenant paid.
type receiptBooking struct {
	OrganizationID    uint64
	ShortcodeID       uint64
	TransID           string
	TransType         string
	BusinessShortCode string
	BillRefNumber     string
	LeaseID           uint64
	Msisdn            string
	FirstName         string
	Amount            float64
	TransTime         time.Time
	PaymentMethodID   uint64
	Notes             string
	RawPayload        string
}

// receiptOutcome says what happened to the receipt, in the order the two steps can
// go wrong: the row that now exists, the lease it landed on, and the payment
// billing booked for it, if any.
type receiptOutcome struct {
	TransactionID uint64
	LeaseID       uint64
	Payment       *GatewayReceipt
	// AlreadyBooked is the retry answer: this exact receipt already has a payment,
	// so nothing was spent a second time.
	AlreadyBooked bool
	// ForeignReceipt means the trans_id this callback carries belongs to another
	// organization, which is only possible on a shared sandbox paybill.
	ForeignReceipt bool
}

// recordReceipt writes the receipt first and spends it second.
//
// The order is the whole design. mpesa_transactions is unique on trans_id across
// the installation, so the insert is what a retried callback bumps into, and it
// happens before any money moves. Only then does billing book the payment, and
// only then does this row point at it. If the process dies between those two steps
// the payment exists and the receipt is still unmatched, which a person can fix;
// if the steps were reversed, a retry could pay the same rent twice.
func (s *Service) recordReceipt(ctx context.Context, in receiptBooking, meta ClientMeta) (receiptOutcome, error) {
	in.TransID = truncate(in.TransID, 30)
	if in.TransID == "" {
		return receiptOutcome{}, errors.New("a receipt needs an M-Pesa transaction id")
	}
	// NaN fails every comparison, so it has to be named: an amount that is not a
	// number cannot be rounded, stored, or spent.
	if math.IsNaN(in.Amount) || math.IsInf(in.Amount, 0) || in.Amount <= 0 || in.Amount > maxAmount {
		return receiptOutcome{}, httpx.Validation(map[string]string{"amount": "must be between 0.01 and 9999999999.99"})
	}

	row, created, err := s.ensureReceiptRow(ctx, in)
	if err != nil {
		return receiptOutcome{}, err
	}
	out := receiptOutcome{TransactionID: row.ID}
	if row.OrganizationID != in.OrganizationID {
		// A receipt already claimed by a different organization. Both channels write
		// the same globally unique trans_id, so this is either a shared sandbox
		// paybill or a forged callback, and neither may be booked here.
		s.log.Error("mpesa receipt belongs to another organization",
			"trans_id", in.TransID, "org_id", in.OrganizationID, "owner_org_id", row.OrganizationID)
		out.ForeignReceipt = true
		return out, nil
	}
	if !created && row.PaymentID.Valid {
		out.AlreadyBooked = true
		out.LeaseID = nullID(row.LeaseID)
		out.Payment = &GatewayReceipt{PaymentID: uint64(row.PaymentID.Int64)}
		return out, nil
	}

	leaseID := in.LeaseID
	if leaseID == 0 {
		leaseID = s.leaseForReference(ctx, in)
	}
	out.LeaseID = leaseID
	if leaseID == 0 || in.PaymentMethodID == 0 {
		// Real money with nowhere to go: it stays in the queue for whoever
		// reconciles, and the callback is still answered as accepted.
		return out, nil
	}

	receipt, err := s.bookForReceipt(ctx, row, leaseID, in, row.Amount, meta)
	if err != nil {
		return out, err
	}
	out.Payment = receipt
	return out, nil
}

// ensureReceiptRow inserts the receipt, or returns the row an earlier attempt
// already wrote. The re-read is not a race this module can close in Go: two
// organizations on one sandbox paybill can be confirmed by Safaricom in the same
// instant, and the unique key is what settles it.
func (s *Service) ensureReceiptRow(ctx context.Context, in receiptBooking) (*TransactionRow, bool, error) {
	row := &TransactionRow{
		OrganizationID:    in.OrganizationID,
		TransID:           in.TransID,
		TransType:         optText(in.TransType),
		BusinessShortCode: truncate(in.BusinessShortCode, maxShortcode),
		ShortcodeID:       optID(in.ShortcodeID),
		BillRefNumber:     optText(truncate(in.BillRefNumber, maxAccountRef)),
		Msisdn:            optText(truncate(in.Msisdn, 64)),
		FirstName:         optText(truncate(in.FirstName, 100)),
		Amount:            roundToCents(in.Amount),
		TransTime:         in.TransTime,
		Status:            transUnmatched,
		RawPayload:        optText(in.RawPayload),
	}
	id, err := s.repo.InsertTransaction(ctx, s.db, row)
	if err == nil {
		row.ID = id
		return row, true, nil
	}
	if !isDuplicateEntry(err) {
		if rejection := checkRejection(err, "the receipt breaks a database rule"); rejection != nil {
			return nil, false, rejection
		}
		return nil, false, err
	}
	existing, err := s.repo.TransactionByReceipt(ctx, in.TransID)
	if err != nil {
		return nil, false, err
	}
	return existing, false, nil
}

// leaseForReference turns the account number a tenant typed into a lease. It is
// the only translation this module does on its own, and it is allowed to fail: an
// unrecognised reference means a receipt in the queue, not a rejected payment.
func (s *Service) leaseForReference(ctx context.Context, in receiptBooking) uint64 {
	if in.ShortcodeID == 0 || in.BillRefNumber == "" {
		return 0
	}
	ref, err := s.repo.AccountRefByNumber(ctx, s.db, in.OrganizationID, in.ShortcodeID, in.BillRefNumber)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		s.log.Warn("look up mpesa account reference", "org_id", in.OrganizationID, "error", err)
		return 0
	}
	if ref.IsActive == 0 {
		s.log.Info("mpesa receipt paid to a retired account reference",
			"org_id", in.OrganizationID, "reference", in.BillRefNumber)
		return 0
	}
	return ref.LeaseID
}

// paymentForBooking is the shape billing is handed: the receipt's own number as
// the payment reference, the channel's timestamp as the paid date, and the caller's
// client meta so the payment row says who was logged in when the money moved.
func paymentForBooking(row *TransactionRow, leaseID uint64, in receiptBooking, amount float64, meta ClientMeta) GatewayPayment {
	return GatewayPayment{
		OrganizationID:  row.OrganizationID,
		LeaseID:         leaseID,
		PaymentMethodID: in.PaymentMethodID,
		Amount:          roundToCents(amount),
		Reference:       row.TransID,
		PaidAt:          row.TransTime,
		Notes:           in.Notes,
		Meta:            meta,
	}
}

// bookForReceipt spends a receipt that has a lease and links the two. The link is
// guarded in SQL by payment_id IS NULL, so a callback Safaricom sends twice cannot
// repoint a receipt that is already booked.
func (s *Service) bookForReceipt(ctx context.Context, row *TransactionRow, leaseID uint64, in receiptBooking, amount float64, meta ClientMeta) (*GatewayReceipt, error) {
	if s.payments == nil {
		return nil, errNoBilling
	}
	reference := row.TransID
	receipt, err := s.payments.Book(ctx, paymentForBooking(row, leaseID, in, amount, meta))
	if err != nil {
		return s.recoverBooking(ctx, row, leaseID, in.PaymentMethodID, reference, err)
	}
	if err := s.repo.LinkTransaction(ctx, s.db, row.OrganizationID, row.ID, leaseID, receipt.PaymentID); err != nil {
		// The money is booked; the row that points at it is the part that failed.
		// Say so loudly, because this receipt now needs a human.
		s.log.Error("link mpesa receipt to payment", "trans_id", reference, "payment_id", receipt.PaymentID, "error", err)
		return nil, err
	}
	out := receipt
	return &out, nil
}

// recoverBooking handles the one rejection that means good news: billing already
// has a payment with this receipt number, because an earlier attempt booked it and
// died before the link. Find that payment, link it, and hand it back as the answer
// to this callback rather than treating the retry as a failure.
func (s *Service) recoverBooking(ctx context.Context, row *TransactionRow, leaseID, methodID uint64, reference string, err error) (*GatewayReceipt, error) {
	var e *httpx.Err
	if !errors.As(err, &e) || e.Status != http.StatusConflict {
		return nil, err
	}
	paymentID, lookupErr := s.repo.PaymentByReference(ctx, row.OrganizationID, methodID, reference)
	if lookupErr != nil || paymentID == 0 {
		s.log.Warn("mpesa receipt rejected by billing", "trans_id", reference, "error", err)
		return nil, err
	}
	if linkErr := s.repo.LinkTransaction(ctx, s.db, row.OrganizationID, row.ID, leaseID, paymentID); linkErr != nil {
		return nil, linkErr
	}
	s.log.Info("mpesa receipt already booked, linked", "trans_id", reference, "payment_id", paymentID)
	return &GatewayReceipt{PaymentID: paymentID}, nil
}
