package mpesa

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"kodipms/internal/audit"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

// The reconcile queue is the human half of this module. A receipt whose account
// number no reference claims is real money sitting in no ledger, and the only safe
// thing to do with it is to show it, keep it, and let somebody who can see the rent
// roll say whose it was.

func (s *Service) Transactions(ctx context.Context, p *rbac.Principal, q TransactionQuery) (*TransactionListDTO, error) {
	if err := authorize(p, "mpesa.view"); err != nil {
		return nil, err
	}
	if err := normalizeTransactionQuery(&q); err != nil {
		return nil, err
	}
	rows, total, err := s.repo.ListTransactions(ctx, p.OrgID, p.Scope, q)
	if err != nil {
		return nil, err
	}
	items := make([]TransactionDTO, 0, len(rows))
	for i := range rows {
		item := transactionDTO(&rows[i])
		// The raw callback belongs to the receipt, not the row in a list: one payload
		// is a paragraph and twenty-five of them are a page nobody scrolls.
		item.RawPayload = nil
		items = append(items, item)
	}
	return &TransactionListDTO{Transactions: items, Total: total}, nil
}

func (s *Service) Transaction(ctx context.Context, p *rbac.Principal, id uint64) (*TransactionDTO, error) {
	if err := authorize(p, "mpesa.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	row, err := s.repo.Transaction(ctx, p.OrgID, id, p.Scope)
	if err != nil {
		return nil, notFound(err)
	}
	out := transactionDTO(row)
	return &out, nil
}

// MatchTransaction places an unmatched receipt on a lease the caller picks, and books
// it through the same path a confirmation uses.
//
// The lease is checked in scope before any money moves, because this is the one place
// a person decides who paid what, and a receipt matched to the wrong tenancy is a
// correction somebody has to reverse rather than an edit.
func (s *Service) MatchTransaction(ctx context.Context, p *rbac.Principal, id uint64, in MatchInput, meta ClientMeta) (*TransactionDTO, error) {
	if err := authorize(p, "mpesa.reconcile"); err != nil {
		return nil, err
	}
	if id == 0 || in.LeaseID == 0 {
		fields := map[string]string{}
		if in.LeaseID == 0 {
			fields["lease_id"] = "required"
		}
		return nil, httpx.Validation(fields)
	}

	row, err := s.repo.Transaction(ctx, p.OrgID, id, p.Scope)
	if err != nil {
		return nil, notFound(err)
	}
	if row.PaymentID.Valid {
		return nil, httpx.Conflict("This receipt is already matched to a payment")
	}
	if row.Status == transIgnored {
		return nil, httpx.Conflict("This receipt was ignored; undo that before matching it")
	}
	// Reading the lease through the caller's scope is what makes this safe: an
	// accountant cannot push a receipt onto a tenancy they cannot bill.
	if _, err := s.repo.GatewayLease(ctx, s.db, p.OrgID, in.LeaseID, p.Scope); err != nil {
		return nil, notFound(err)
	}

	methodID, err := s.methodForReceipt(ctx, row)
	if err != nil {
		return nil, err
	}
	if methodID == 0 {
		return nil, httpx.Conflict("The M-Pesa payment method is missing, so this receipt cannot be booked")
	}

	booking := receiptBooking{
		OrganizationID:  p.OrgID,
		PaymentMethodID: methodID,
		Notes:           "Matched to lease " + strconv.FormatUint(in.LeaseID, 10) + " by user " + strconv.FormatUint(p.UserID, 10),
	}
	if row.FirstName.Valid {
		booking.FirstName = row.FirstName.String
	}
	receipt, err := s.bookForReceipt(ctx, row, in.LeaseID, booking, row.Amount, meta)
	if err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "mpesa_transactions.matched", EntityType: "mpesa_transactions", EntityID: id,
		OldValues: map[string]any{"status": row.Status, "lease_id": nil},
		NewValues: map[string]any{"status": transMatched, "lease_id": in.LeaseID,
			"payment_id": receipt.PaymentID, "amount": row.Amount},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.transactionDetail(ctx, p, id)
}

// IgnoreTransaction takes a receipt out of the queue without spending it.
//
// The reason is required and the row is kept, because "we never worked out whose this
// was" is information the next accountant needs, and a deleted receipt cannot be
// argued about. Money that turns out to belong to another organization is refunded by
// whoever received it, not here.
func (s *Service) IgnoreTransaction(ctx context.Context, p *rbac.Principal, id uint64, in IgnoreInput, meta ClientMeta) (*TransactionDTO, error) {
	if err := authorize(p, "mpesa.reconcile"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	fields := map[string]string{}
	reason := cleanLabel(in.Reason, maxNotes, "reason", fields)
	if reason == "" {
		return nil, httpx.Validation(map[string]string{"reason": "Say why this receipt is being ignored"})
	}

	row, err := s.repo.Transaction(ctx, p.OrgID, id, p.Scope)
	if err != nil {
		return nil, notFound(err)
	}
	if row.PaymentID.Valid {
		return nil, httpx.Conflict("This receipt already has a payment; reverse that payment in billing instead")
	}
	if row.Status == transIgnored {
		return s.transactionDetail(ctx, p, id)
	}
	if err := s.repo.SetTransactionStatus(ctx, s.db, p.OrgID, id, transIgnored); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "mpesa_transactions.ignored", EntityType: "mpesa_transactions", EntityID: id,
		OldValues: map[string]any{"status": row.Status},
		NewValues: map[string]any{"status": transIgnored, "reason": reason, "amount": row.Amount},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.transactionDetail(ctx, p, id)
}

// methodForReceipt picks the payment method for a receipt an accountant matched by
// hand. A receipt that arrived with no shortcode row at all -- typed in from an SMS,
// say -- is booked as a paybill, which is what an unmatched queue nearly always is.
func (s *Service) methodForReceipt(ctx context.Context, row *TransactionRow) (uint64, error) {
	code := methodPaybill
	if shortcodeID := nullID(row.ShortcodeID); shortcodeID > 0 {
		shortcode, err := s.repo.Shortcode(ctx, s.db, row.OrganizationID, shortcodeID)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		if shortcode.AccountType == typeTill {
			code = methodTill
		}
	}
	id, err := s.repo.PaymentMethodIDByCode(ctx, code)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func (s *Service) transactionDetail(ctx context.Context, p *rbac.Principal, id uint64) (*TransactionDTO, error) {
	row, err := s.repo.Transaction(ctx, p.OrgID, id, p.Scope)
	if err != nil {
		return nil, notFound(err)
	}
	out := transactionDTO(row)
	return &out, nil
}

func transactionDTO(row *TransactionRow) TransactionDTO {
	return TransactionDTO{
		ID:                row.ID,
		TransID:           row.TransID,
		TransType:         nullText(row.TransType),
		BusinessShortCode: row.BusinessShortCode,
		ShortcodeID:       nullID(row.ShortcodeID),
		BillRefNumber:     nullText(row.BillRefNumber),
		Msisdn:            nullText(row.Msisdn),
		FirstName:         nullText(row.FirstName),
		Amount:            roundToCents(row.Amount),
		TransTime:         row.TransTime,
		Status:            row.Status,
		PaymentID:         nullID(row.PaymentID),
		LeaseID:           nullID(row.LeaseID),
		LeaseNumber:       nullText(row.LeaseNumber),
		UnitNumber:        nullText(row.UnitNumber),
		PropertyName:      nullText(row.PropertyName),
		TenantName:        nullText(row.TenantName),
		RawPayload:        payloadJSON(row.RawPayload),
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}

func normalizeTransactionQuery(q *TransactionQuery) error {
	fields := map[string]string{}
	q.Search = truncate(q.Search, maxSearch)
	if q.Status != "" && !inList(q.Status, transStatuses) {
		fields["status"] = transStatusMessage
	}
	if len(fields) > 0 {
		return httpx.Validation(fields)
	}
	return nil
}
