package billing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/audit"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

// Payments lists money received inside the caller's scope.
func (s *Service) Payments(ctx context.Context, p *rbac.Principal, q PaymentQuery) (*PaymentListDTO, error) {
	if err := authorize(p, "payments.view"); err != nil {
		return nil, err
	}
	if err := normalizePaymentQuery(&q); err != nil {
		return nil, err
	}
	rows, total, err := s.repo.ListPayments(ctx, p.OrgID, p.Scope, q)
	if err != nil {
		return nil, err
	}
	items := make([]PaymentSummaryDTO, 0, len(rows))
	for i := range rows {
		items = append(items, paymentSummaryDTO(&rows[i]))
	}
	return &PaymentListDTO{Payments: items, Total: total}, nil
}

func (s *Service) Payment(ctx context.Context, p *rbac.Principal, id uint64) (*PaymentDetailDTO, error) {
	if err := authorize(p, "payments.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	return s.paymentDetail(ctx, p.OrgID, id, p.Scope)
}

// LeaseCredit shows how a lease's credit was built and spent. The balance is
// derived from the payments; these rows are the movements a tenant can be
// walked through.
func (s *Service) LeaseCredit(ctx context.Context, p *rbac.Principal, leaseID uint64) ([]TenantCreditDTO, error) {
	if err := authorize(p, "payments.view"); err != nil {
		return nil, err
	}
	if leaseID == 0 {
		return nil, httpx.ErrNotFound
	}
	if _, err := s.repo.BillingLease(ctx, s.db, p.OrgID, leaseID, p.Scope, false); errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	rows, err := s.repo.TenantCreditEntries(ctx, p.OrgID, leaseID)
	if err != nil {
		return nil, err
	}
	return tenantCreditDTOs(rows), nil
}

// RecordPayment books money in and then spends it. A caller may name the
// invoices the receipt settles; otherwise it is applied oldest-due-first. Money
// no invoice can take stays with the lease as tenant credit, so a payment is
// never refused because the arrears are smaller than the receipt.
func (s *Service) RecordPayment(ctx context.Context, p *rbac.Principal, in PaymentInput, meta ClientMeta) (*PaymentDetailDTO, error) {
	if err := authorize(p, "payments.create"); err != nil {
		return nil, err
	}
	row, fields := buildPayment(p.OrgID, p.UserID, in)
	fields = validateAllocations(in.Allocations, row.Amount, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	// Only a date the client chose can be wrong: the default is this instant. The
	// organization's own calendar decides, so its clock is read just for that.
	if in.PaidAt != nil && strings.TrimSpace(*in.PaidAt) != "" {
		timezone, err := s.repo.OrganizationTimezone(ctx, p.OrgID)
		if err != nil {
			return nil, err
		}
		if problem := paymentDateProblem(row.PaidAt, todayIn(orgLocation(timezone))); problem != "" {
			return nil, httpx.Validation(map[string]string{"paid_at": problem})
		}
	}

	return s.bookPayment(ctx, p, row, in.Allocations, meta)
}

// bookPayment writes the receipt, spends it and posts the journal. Everything
// that judges a caller's input happens before it, so a payment channel that has
// already proved money arrived can book through this same path and inherit the
// same allocation plans, credit handling and database triggers as a cashier.
func (s *Service) bookPayment(ctx context.Context, p *rbac.Principal, row *PaymentRow, allocations *[]AllocationInput, meta ClientMeta) (*PaymentDetailDTO, error) {
	method, err := s.repo.PaymentMethod(ctx, row.PaymentMethodID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.Validation(map[string]string{"payment_method_id": "is not a known payment method"})
	}
	if err != nil {
		return nil, err
	}
	if method.IsActive == 0 {
		return nil, httpx.Conflict("The " + method.Name + " payment method is disabled")
	}

	settings, err := s.repo.Settings(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}

	var paymentID uint64
	var allocated, credit float64

	err = s.inTx(ctx, func(tx *sqlx.Tx) error {
		lease, err := s.billingLease(ctx, tx, p, row.LeaseID, true)
		if err != nil {
			return err
		}
		if lease.Status == leaseDraft {
			return httpx.Conflict("A payment needs a lease that has been activated")
		}

		plans, spent, err := s.planPayment(ctx, tx, p, row, allocations)
		if err != nil {
			return err
		}
		allocated = spent
		credit = roundToCents(row.Amount - spent)

		id, err := s.repo.InsertPayment(ctx, tx, row)
		if err != nil {
			if isDuplicateEntry(err) {
				return httpx.Conflict("A payment with that reference is already recorded for this method")
			}
			if isForeignKeyRestrict(err) {
				return httpx.Validation(map[string]string{"lease_id": "does not belong to this organization"})
			}
			return err
		}
		paymentID = id

		if err := s.writeAllocations(ctx, tx, p.OrgID, id, plans); err != nil {
			return err
		}
		if credit > ledgerTolerance {
			if _, err := s.repo.InsertTenantCredit(ctx, tx, &TenantCreditRow{
				OrganizationID: p.OrgID, LeaseID: optID(row.LeaseID),
				EntryType: entryOverpayment, Amount: credit,
				SourceType: optText(sourcePayment), SourceID: optID(id),
				Notes:     optText("Unallocated receipt"),
				CreatedBy: optID(p.UserID),
			}); err != nil {
				return err
			}
		}
		lines := paymentLines(row.Amount, allocated, credit, "Payment "+nullText(row.Reference))
		if _, err := s.repo.PostJournal(ctx, tx, p.OrgID, row.LeaseID, sourcePayment, id,
			truncateDate(row.PaidAt), settings.CurrencyCode, p.UserID, lines); err != nil {
			return s.postingFailure(err, "payment")
		}
		today := truncateDate(row.PaidAt)
		for _, plan := range plans {
			if err := s.settleInvoice(ctx, tx, p.OrgID, plan.InvoiceID, today); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "payments.recorded", EntityType: "payments", EntityID: paymentID,
		NewValues: map[string]any{"lease_id": row.LeaseID, "amount": row.Amount,
			"allocated": allocated, "credit": credit, "reference": nullText(row.Reference),
			"method": method.Code, "paid_at": row.PaidAt.Format(time.RFC3339)},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.paymentDetail(ctx, p.OrgID, paymentID, p.Scope)
}

// ReversePayment takes money back out. The schema only allows a full reversal,
// so a partial refund is a credit note, never a reversal.
func (s *Service) ReversePayment(ctx context.Context, p *rbac.Principal, id uint64, in ReversePaymentInput, meta ClientMeta) (*PaymentDetailDTO, error) {
	if err := authorize(p, "payments.reverse"); err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(in.Reason)
	fields := map[string]string{}
	if reason == "" {
		fields["reason"] = "required"
	} else if utf8.RuneCountInString(reason) > maxReasonLen {
		fields["reason"] = fmt.Sprintf("must be at most %d characters", maxReasonLen)
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}

	var amount float64
	var at time.Time

	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		payment, err := s.repo.PaymentForUpdate(ctx, tx, p.OrgID, id, p.Scope)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if payment.Status != paymentCompleted {
			return httpx.Conflict("Only a completed payment can be reversed")
		}
		allocated, err := s.repo.PaymentAllocated(ctx, tx, p.OrgID, id)
		if err != nil {
			return err
		}
		targets, err := s.repo.AllocationTargets(ctx, tx, p.OrgID, id)
		if err != nil {
			return err
		}
		settings, err := s.repo.Settings(ctx, p.OrgID)
		if err != nil {
			return err
		}

		at = time.Now().UTC()
		reversalID, err := s.repo.InsertReversal(ctx, tx, p.OrgID, id, payment.Amount, reason, p.UserID, at)
		if err != nil {
			if isDuplicateEntry(err) {
				return httpx.Conflict("This payment has already been reversed")
			}
			if isTriggerRejection(err) {
				return httpx.Conflict("This payment cannot be reversed")
			}
			return err
		}

		// The receipt is gone, so the credit its unspent part created has to be
		// taken back or the lease would keep spending money that no longer exists.
		credit := roundToCents(payment.Amount - allocated)
		lines := reversalLines(payment.Amount, allocated, credit, "Reversal of payment "+nullText(payment.Reference))
		if _, err := s.repo.PostJournal(ctx, tx, p.OrgID, payment.LeaseID, sourceReversal, reversalID,
			truncateDate(at), settings.CurrencyCode, p.UserID, lines); err != nil {
			return s.postingFailure(err, "reversal")
		}
		if credit > ledgerTolerance {
			if _, err := s.repo.InsertTenantCredit(ctx, tx, &TenantCreditRow{
				OrganizationID: p.OrgID, LeaseID: optID(payment.LeaseID),
				EntryType: entryReversal, Amount: -credit,
				SourceType: optText(sourceReversal), SourceID: optID(reversalID),
				Notes:     optText(reason),
				CreatedBy: optID(p.UserID),
			}); err != nil {
				return err
			}
		}
		amount = payment.Amount
		today := truncateDate(at)
		for _, invoiceID := range targets {
			if err := s.settleInvoice(ctx, tx, p.OrgID, invoiceID, today); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "payments.reversed", EntityType: "payments", EntityID: id,
		OldValues: map[string]any{"status": paymentCompleted},
		NewValues: map[string]any{"status": paymentReversed, "amount": amount,
			"reason": reason, "reversed_at": at.Format(time.RFC3339)},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.paymentDetail(ctx, p.OrgID, id, p.Scope)
}

// ApplyCredit settles an invoice with money the lease already paid and that no
// invoice has taken yet. It spends real payments, so amount_paid stays what the
// triggers derive and unallocated_funds falls by exactly the amount applied.
func (s *Service) ApplyCredit(ctx context.Context, p *rbac.Principal, id uint64, in ApplyCreditInput, meta ClientMeta) (*InvoiceDetailDTO, error) {
	if err := authorize(p, "payments.reconcile"); err != nil {
		return nil, err
	}
	if in.Amount != nil {
		amount := roundToCents(*in.Amount)
		if amount <= 0 || amount > maxAmount {
			return nil, httpx.Validation(map[string]string{
				"amount": fmt.Sprintf("must be greater than zero and at most %.2f", maxAmount),
			})
		}
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}

	var applied float64
	var leaseID uint64

	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		invoice, err := s.repo.InvoiceForUpdate(ctx, tx, p.OrgID, id, p.Scope, tenantOnly(p.Scope))
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if invoice.Status == statusDraft || invoice.Status == statusVoid {
			return httpx.Conflict("Credit can only be applied to an invoice that has been issued")
		}
		if invoice.Balance <= ledgerTolerance {
			return httpx.Conflict("This invoice is already settled")
		}
		amount := roundToCents(invoice.Balance)
		if in.Amount != nil && roundToCents(*in.Amount) < amount {
			amount = roundToCents(*in.Amount)
		}

		sources, err := s.repo.CreditSources(ctx, tx, p.OrgID, invoice.LeaseID, id)
		if err != nil {
			return err
		}
		available := 0.0
		for _, source := range sources {
			available = roundToCents(available + source.Unallocated)
		}
		if amount > available+ledgerTolerance {
			return httpx.Conflict(notEnoughCredit)
		}
		settings, err := s.repo.Settings(ctx, p.OrgID)
		if err != nil {
			return err
		}

		remaining := amount
		for _, source := range sources {
			if remaining <= ledgerTolerance {
				break
			}
			take := roundToCents(source.Unallocated)
			if take > remaining {
				take = remaining
			}
			if take <= 0 {
				continue
			}
			if err := s.repo.InsertAllocation(ctx, tx, p.OrgID, source.PaymentID, id, take); err != nil {
				if isDuplicateEntry(err) {
					return httpx.Conflict("That payment has already been applied to this invoice")
				}
				if isTriggerRejection(err) {
					return httpx.Conflict("Credit could not be applied to this invoice")
				}
				return err
			}
			if _, err := s.repo.InsertTenantCredit(ctx, tx, &TenantCreditRow{
				OrganizationID: p.OrgID, LeaseID: optID(invoice.LeaseID), InvoiceID: optID(id),
				EntryType: entryApplication, Amount: -take,
				SourceType: optText(sourcePayment), SourceID: optID(source.PaymentID),
				Notes:     optText("Credit applied to " + invoice.InvoiceNumber),
				CreatedBy: optID(p.UserID),
			}); err != nil {
				return err
			}
			remaining = roundToCents(remaining - take)
			applied = roundToCents(applied + take)
		}
		if applied <= ledgerTolerance {
			return httpx.Conflict(notEnoughCredit)
		}
		if _, err := s.repo.PostJournal(ctx, tx, p.OrgID, invoice.LeaseID, sourceAllocation, id,
			truncateDate(time.Now().UTC()), settings.CurrencyCode, p.UserID,
			applicationLines(applied, "Credit applied to "+invoice.InvoiceNumber)); err != nil {
			return s.postingFailure(err, "credit application")
		}
		leaseID = invoice.LeaseID
		return s.settleInvoice(ctx, tx, p.OrgID, id, truncateDate(time.Now().UTC()))
	})
	if err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "payments.credit_applied", EntityType: "invoices", EntityID: id,
		NewValues: map[string]any{"amount": applied, "lease_id": leaseID},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.detail(ctx, p.OrgID, id, p.Scope)
}

// planPayment turns the money in hand into allocation plans.
func (s *Service) planPayment(ctx context.Context, tx *sqlx.Tx, p *rbac.Principal,
	row *PaymentRow, explicit *[]AllocationInput) ([]allocationPlan, float64, error) {
	if explicit != nil {
		plans, err := s.planExplicit(ctx, tx, p, row.LeaseID, row.Amount, *explicit)
		if err != nil {
			return nil, 0, err
		}
		total := 0.0
		for _, plan := range plans {
			total = roundToCents(total + plan.Amount)
		}
		return plans, total, nil
	}
	open, err := s.repo.OpenInvoiceBalances(ctx, tx, p.OrgID, row.LeaseID)
	if err != nil {
		return nil, 0, err
	}
	plans, leftover := planAllocations(row.Amount, open)
	return plans, roundToCents(row.Amount - leftover), nil
}

// planExplicit checks a client's split against the locked invoice balances, so
// nobody can overpay an invoice or settle one belonging to another lease.
func (s *Service) planExplicit(ctx context.Context, tx *sqlx.Tx, p *rbac.Principal,
	leaseID uint64, amount float64, in []AllocationInput) ([]allocationPlan, error) {
	seen := map[uint64]bool{}
	plans := make([]allocationPlan, 0, len(in))
	total := 0.0
	for i := range in {
		field := fmt.Sprintf("allocations[%d]", i)
		invoiceID := in[i].InvoiceID
		if invoiceID == 0 {
			return nil, httpx.Validation(map[string]string{field + ".invoice_id": "required"})
		}
		if seen[invoiceID] {
			return nil, httpx.Validation(map[string]string{field + ".invoice_id": "is listed more than once"})
		}
		share := roundToCents(in[i].Amount)
		if share <= 0 || share > maxAmount {
			return nil, httpx.Validation(map[string]string{field + ".amount": "must be greater than zero"})
		}
		balance, err := s.repo.InvoiceBalanceByID(ctx, tx, p.OrgID, invoiceID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, httpx.Validation(map[string]string{field + ".invoice_id": "does not exist"})
		}
		if err != nil {
			return nil, err
		}
		if balance.LeaseID != leaseID {
			return nil, httpx.Validation(map[string]string{field + ".invoice_id": "belongs to another lease"})
		}
		if balance.Status == statusDraft || balance.Status == statusVoid {
			return nil, httpx.Conflict("Invoice " + balance.InvoiceNumber + " cannot take a payment")
		}
		if share > balance.Balance+ledgerTolerance {
			return nil, httpx.Conflict("Invoice " + balance.InvoiceNumber + " owes less than that")
		}
		seen[invoiceID] = true
		total = roundToCents(total + share)
		plans = append(plans, allocationPlan{InvoiceID: invoiceID, Amount: share})
	}
	if total > amount+ledgerTolerance {
		return nil, httpx.Validation(map[string]string{
			"allocations": "add up to more than the payment amount",
		})
	}
	return plans, nil
}

func (s *Service) writeAllocations(ctx context.Context, tx *sqlx.Tx, orgID, paymentID uint64, plans []allocationPlan) error {
	for _, plan := range plans {
		if plan.Amount <= 0 {
			continue
		}
		if err := s.repo.InsertAllocation(ctx, tx, orgID, paymentID, plan.InvoiceID, plan.Amount); err != nil {
			if isTriggerRejection(err) {
				return httpx.Conflict("This payment cannot be split that way")
			}
			if isForeignKeyRestrict(err) {
				return httpx.Validation(map[string]string{"allocations": "names an invoice that does not exist"})
			}
			return err
		}
	}
	return nil
}

func (s *Service) paymentDetail(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*PaymentDetailDTO, error) {
	row, err := s.repo.GetPayment(ctx, orgID, id, scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	allocations, err := s.repo.AllocationsForPayment(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	return &PaymentDetailDTO{
		PaymentSummaryDTO: paymentSummaryDTO(row),
		Allocations:       allocationDTOs(allocations),
	}, nil
}

// inTx runs one write transaction and rolls it back on any error, so a money
// path can never leave a half-posted journal behind.
func (s *Service) inTx(ctx context.Context, fn func(tx *sqlx.Tx) error) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) postingFailure(err error, what string) error {
	if errors.Is(err, errUnbalancedJournal) {
		s.log.Error("refusing to post an unbalanced journal", "kind", what)
		return httpx.Conflict("This " + what + " does not balance and was not recorded")
	}
	return err
}

func buildPayment(orgID, userID uint64, in PaymentInput) (*PaymentRow, map[string]string) {
	fields := map[string]string{}
	row := &PaymentRow{
		OrganizationID: orgID, LeaseID: in.LeaseID, PaymentMethodID: in.PaymentMethodID,
		Amount: roundToCents(in.Amount), Status: paymentCompleted,
		PaidAt:     time.Now().UTC(),
		ReceivedBy: optID(userID),
	}
	if in.LeaseID == 0 {
		fields["lease_id"] = "required"
	}
	if in.PaymentMethodID == 0 {
		fields["payment_method_id"] = "required"
	}
	if in.Amount <= 0 {
		fields["amount"] = "must be greater than zero"
	} else if in.Amount > maxAmount {
		fields["amount"] = fmt.Sprintf("must be at most %.2f", maxAmount)
	}
	if in.PaidAt != nil {
		if paidAt, ok := parseTimestampField("paid_at", in.PaidAt, fields); ok {
			row.PaidAt = paidAt
		}
	}
	if in.Reference != nil {
		reference := strings.TrimSpace(*in.Reference)
		if utf8.RuneCountInString(reference) > maxReferenceLen {
			fields["reference"] = fmt.Sprintf("must be at most %d characters", maxReferenceLen)
		} else {
			row.Reference = optText(reference)
		}
	}
	if in.Notes != nil {
		notes := strings.TrimSpace(*in.Notes)
		if utf8.RuneCountInString(notes) > maxNoteLen {
			fields["notes"] = fmt.Sprintf("must be at most %d characters", maxNoteLen)
		} else {
			row.Notes = optText(notes)
		}
	}
	return row, fields
}

func validateAllocations(in *[]AllocationInput, amount float64, fields map[string]string) map[string]string {
	if in == nil {
		return fields
	}
	list := *in
	if len(list) == 0 {
		fields["allocations"] = "must list at least one invoice, or be omitted"
		return fields
	}
	if len(list) > maxAllocations {
		fields["allocations"] = fmt.Sprintf("cannot list more than %d invoices", maxAllocations)
		return fields
	}
	total := 0.0
	for i := range list {
		total = roundToCents(total + roundToCents(list[i].Amount))
	}
	if total > amount+ledgerTolerance {
		fields["allocations"] = "add up to more than the payment amount"
	}
	return fields
}

func normalizePaymentQuery(q *PaymentQuery) error {
	fields := map[string]string{}
	q.Search = strings.TrimSpace(q.Search)
	if utf8.RuneCountInString(q.Search) > maxSearchLen {
		fields["q"] = fmt.Sprintf("must be at most %d characters", maxSearchLen)
	}
	q.Status = strings.ToLower(strings.TrimSpace(q.Status))
	if q.Status != "" && !inList(paymentStatuses, q.Status) {
		fields["status"] = "must be one of " + strings.Join(paymentStatuses, ", ")
	}
	if !q.From.IsZero() && !q.To.IsZero() && q.From.After(q.To) {
		fields["from"] = "must be on or before to"
	}
	if len(fields) > 0 {
		return httpx.Validation(fields)
	}
	return nil
}

func paymentSummaryDTO(row *PaymentViewRow) PaymentSummaryDTO {
	out := PaymentSummaryDTO{
		ID:        row.ID,
		Amount:    roundToCents(row.Amount),
		Allocated: roundToCents(row.Allocated),
		Credit:    roundToCents(row.Amount - row.Allocated),
		Reference: nullText(row.Reference), PaidAt: row.PaidAt, Status: row.Status,
		Method: MethodRefDTO{ID: row.PaymentMethodID, Code: row.MethodCode, Name: row.MethodName},
		Lease: leaseRef(row.LeaseID, row.LeaseNumber, row.UnitNumber, row.PropertyID,
			row.PropertyName, ""),
		ReceivedBy:     nullIDValue(row.ReceivedBy),
		Notes:          nullText(row.Notes),
		ReversedAt:     nullTimePointer(row.ReversedAt),
		ReversalReason: nullText(row.ReversalReason),
	}
	if row.ReversedAt.Valid {
		out.Reversal = &ReversalDTO{
			Amount: roundToCents(row.Amount), Reason: nullText(row.ReversalReason),
			ReversedBy: nullIDValue(row.ReversalBy), ReversedAt: row.ReversedAt.Time,
		}
	}
	return out
}

func tenantCreditDTOs(rows []TenantCreditRow) []TenantCreditDTO {
	out := make([]TenantCreditDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, TenantCreditDTO{
			ID: row.ID, LeaseID: nullIDValue(row.LeaseID), EntryType: row.EntryType,
			Amount: roundToCents(row.Amount), InvoiceID: nullIDValue(row.InvoiceID),
			Notes: nullText(row.Notes), CreatedAt: row.CreatedAt,
		})
	}
	return out
}

func optID(id uint64) sql.NullInt64 {
	if id == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(id), Valid: true}
}

func optText(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}
func (s *Service) PaymentMethods(ctx context.Context, p *rbac.Principal) ([]PaymentMethodDTO, error) {
	if err := authorize(p, "payments.view"); err != nil {
		return nil, err
	}
	rows, err := s.repo.PaymentMethods(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PaymentMethodDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, PaymentMethodDTO{
			ID: row.ID, Code: row.Code, Name: row.Name,
			Description: nullText(row.Description), IsActive: row.IsActive != 0,
		})
	}
	return out, nil
}

// allocationPlan is one payment-to-invoice link the service intends to write.
type allocationPlan struct {
	InvoiceID uint64
	Amount    float64
}

// planAllocations spends money oldest-due-first and stops when either the
// payment or the open invoices run out. The remainder is tenant credit, never a
// forced allocation, because the schema refuses an allocation above a balance.
func planAllocations(amount float64, open []InvoiceBalance) ([]allocationPlan, float64) {
	remaining := roundToCents(amount)
	var plans []allocationPlan
	for _, invoice := range open {
		if remaining <= ledgerTolerance {
			break
		}
		if invoice.Status == statusDraft || invoice.Status == statusVoid || invoice.Balance <= 0 {
			continue
		}
		take := remaining
		if invoice.Balance < take {
			take = roundToCents(invoice.Balance)
		}
		if take <= 0 {
			continue
		}
		plans = append(plans, allocationPlan{InvoiceID: invoice.ID, Amount: take})
		remaining = roundToCents(remaining - take)
	}
	if remaining < 0 {
		remaining = 0
	}
	return plans, remaining
}

func allocationDTOs(rows []AllocationRow) []AllocationDTO {
	out := make([]AllocationDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, AllocationDTO{
			ID: row.ID, PaymentID: row.PaymentID, InvoiceID: row.InvoiceID,
			InvoiceNumber: row.InvoiceNumber, Amount: row.Amount,
			ReversedAt: nullTimePointer(row.ReversedAt), CreatedAt: row.CreatedAt,
		})
	}
	return out
}
