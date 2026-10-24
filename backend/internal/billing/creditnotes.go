package billing

import (
	"context"
	"database/sql"
	"encoding/json"
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

// A credit note corrects what was billed; it never touches the lines of the
// invoice it settles. The original lines stay as they were issued and the note
// stands beside them as the document that says part of that bill is cancelled.
// Credit notes are also not a second source of tenant credit: the money a lease
// can spend is derived from payments, so applying a note reduces what is owed
// instead of adding spendable balance.

func (s *Service) CreditNotes(ctx context.Context, p *rbac.Principal, q CreditNoteQuery) (*CreditNoteListDTO, error) {
	if err := authorize(p, "invoices.view"); err != nil {
		return nil, err
	}
	if err := normalizeCreditNoteQuery(&q); err != nil {
		return nil, err
	}
	rows, total, err := s.repo.ListCreditNotes(ctx, p.OrgID, p.Scope, q)
	if err != nil {
		return nil, err
	}
	items := make([]CreditNoteSummaryDTO, 0, len(rows))
	for i := range rows {
		items = append(items, creditNoteSummaryDTO(&rows[i]))
	}
	return &CreditNoteListDTO{CreditNotes: items, Total: total}, nil
}

// CreditNote returns one note with the journal it posted, so a disputed
// adjustment can be traced to the entries that moved the invoice.
func (s *Service) CreditNote(ctx context.Context, p *rbac.Principal, id uint64) (*CreditNoteDetailDTO, error) {
	if err := authorize(p, "invoices.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	row, err := s.repo.GetCreditNote(ctx, p.OrgID, id, p.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	entries, err := s.repo.LedgerForSource(ctx, p.OrgID, sourceCreditNote, id)
	if err != nil {
		return nil, err
	}
	return &CreditNoteDetailDTO{
		CreditNoteSummaryDTO: creditNoteSummaryDTO(row),
		Ledger:               ledgerEntryDTOs(entries),
	}, nil
}

func (s *Service) CreateCreditNote(ctx context.Context, p *rbac.Principal, in CreateCreditNoteInput, meta ClientMeta) (*CreditNoteDetailDTO, error) {
	if err := authorize(p, "invoices.create"); err != nil {
		return nil, err
	}
	row, fields := buildCreditNote(p.OrgID, p.UserID, in)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	var noteID uint64
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		lease, err := s.billingLease(ctx, tx, p, row.LeaseID, true)
		if err != nil {
			return err
		}
		if row.InvoiceID.Valid {
			if err := s.checkNoteInvoice(ctx, tx, p.OrgID, row.LeaseID, uint64(row.InvoiceID.Int64), row.Amount, false); err != nil {
				return err
			}
		}
		number := row.CreditNoteNumber
		if number == "" {
			settings, err := s.repo.Settings(ctx, p.OrgID)
			if err != nil {
				return err
			}
			prefix := numberPrefix(settings.InvoicePrefix, nullText(lease.PropertyCode))
			number, err = s.repo.NextCreditNoteNumber(ctx, tx, p.OrgID, prefix, truncateDate(row.IssueDate))
			if err != nil {
				return err
			}
		} else if taken, err := s.repo.CreditNoteNumberTaken(ctx, tx, p.OrgID, number, 0); err != nil {
			return err
		} else if taken {
			return httpx.Conflict("That credit note number is already used")
		}
		row.CreditNoteNumber = number

		id, err := s.repo.InsertCreditNote(ctx, tx, row)
		if err != nil {
			if isDuplicateEntry(err) {
				return httpx.Conflict("That credit note number is already used")
			}
			if isForeignKeyRestrict(err) || isTriggerRejection(err) {
				return httpx.Validation(map[string]string{"invoice_id": "does not belong to this lease"})
			}
			return err
		}
		noteID = id
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "credit_notes.created", EntityType: "credit_notes", EntityID: noteID,
		NewValues: map[string]any{"credit_note_number": row.CreditNoteNumber, "lease_id": row.LeaseID,
			"invoice_id": nullIDValue(row.InvoiceID), "amount": row.Amount, "status": creditDraft},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.creditNoteDetail(ctx, p.OrgID, noteID, p.Scope)
}

// UpdateCreditNote edits a draft. Once a note is issued the schema freezes its
// money and its target, and this layer refuses the same thing with a message.
func (s *Service) UpdateCreditNote(ctx context.Context, p *rbac.Principal, id uint64, in UpdateCreditNoteInput, meta ClientMeta) (*CreditNoteDetailDTO, error) {
	if err := authorize(p, "invoices.update"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	fields := map[string]string{}
	if in.Amount != nil && (*in.Amount <= 0 || *in.Amount > maxAmount) {
		fields["amount"] = fmt.Sprintf("must be greater than zero and at most %.2f", maxAmount)
	}
	if in.CreditNoteNumber != nil && *in.CreditNoteNumber != "" {
		if cleanNumber(*in.CreditNoteNumber, "credit_note_number", fields) == "" {
			fields["credit_note_number"] = "required"
		}
	}
	issueDate := parseOptionalDate("issue_date", in.IssueDate, fields)
	var reason sql.NullString
	if in.Reason != nil && !isJSONNull(in.Reason) {
		var text string
		if err := json.Unmarshal(in.Reason, &text); err != nil {
			fields["reason"] = "must be a string or null"
		} else {
			text = strings.TrimSpace(text)
			if utf8.RuneCountInString(text) > maxNoteLen {
				fields["reason"] = fmt.Sprintf("must be at most %d characters", maxNoteLen)
			} else {
				reason = optText(text)
			}
		}
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	err := s.mutateCreditNote(ctx, p, id, func(tx *sqlx.Tx, row *CreditNoteRow) (map[string]any, error) {
		if in.Amount != nil {
			row.Amount = roundToCents(*in.Amount)
		}
		if in.CreditNoteNumber != nil && *in.CreditNoteNumber != "" {
			number := cleanNumber(*in.CreditNoteNumber, "credit_note_number", fields)
			if number == "" {
				return nil, httpx.Validation(fields)
			}
			if number != row.CreditNoteNumber {
				taken, err := s.repo.CreditNoteNumberTaken(ctx, tx, p.OrgID, number, row.ID)
				if err != nil {
					return nil, err
				}
				if taken {
					return nil, httpx.Conflict("That credit note number is already used")
				}
				row.CreditNoteNumber = number
			}
		}
		if in.InvoiceID != nil {
			if *in.InvoiceID == 0 {
				row.InvoiceID = sql.NullInt64{}
			} else {
				if err := s.checkNoteInvoice(ctx, tx, p.OrgID, row.LeaseID, *in.InvoiceID, row.Amount, false); err != nil {
					return nil, err
				}
				row.InvoiceID = optID(*in.InvoiceID)
			}
		}
		if in.Reason != nil {
			row.Reason = reason
		}
		if !issueDate.IsZero() {
			row.IssueDate = issueDate
		}
		return map[string]any{"amount": row.Amount, "invoice_id": nullIDValue(row.InvoiceID)}, nil
	}, "credit_notes.updated", meta)
	if err != nil {
		return nil, err
	}
	return s.creditNoteDetail(ctx, p.OrgID, id, p.Scope)
}

// IssueCreditNote signs the note. The invoice it settles is named here if the
// draft did not already say so, because the schema freezes invoice_id the
// moment the note leaves draft.
func (s *Service) IssueCreditNote(ctx context.Context, p *rbac.Principal, id uint64, in IssueCreditNoteInput, meta ClientMeta) (*CreditNoteDetailDTO, error) {
	if err := authorize(p, "invoices.update"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}

	err := s.mutateCreditNote(ctx, p, id, func(tx *sqlx.Tx, row *CreditNoteRow) (map[string]any, error) {
		if in.InvoiceID != nil && *in.InvoiceID != 0 {
			if err := s.checkNoteInvoice(ctx, tx, p.OrgID, row.LeaseID, *in.InvoiceID, row.Amount, false); err != nil {
				return nil, err
			}
			row.InvoiceID = optID(*in.InvoiceID)
		}
		if !row.InvoiceID.Valid {
			return nil, httpx.Conflict("A credit note needs an invoice to settle before it can be issued")
		}
		row.Status = creditIssued
		return map[string]any{"status": creditIssued, "invoice_id": nullIDValue(row.InvoiceID)}, nil
	}, "credit_notes.issued", meta)
	if err != nil {
		return nil, err
	}
	return s.creditNoteDetail(ctx, p.OrgID, id, p.Scope)
}

// ApplyCreditNote is where the money moves: the invoice stops owing the amount
// of the note and the journal writes the receivable back.
func (s *Service) ApplyCreditNote(ctx context.Context, p *rbac.Principal, id uint64, meta ClientMeta) (*CreditNoteDetailDTO, error) {
	if err := authorize(p, "invoices.update"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}

	err := s.mutateCreditNote(ctx, p, id, func(tx *sqlx.Tx, row *CreditNoteRow) (map[string]any, error) {
		settings, err := s.repo.Settings(ctx, p.OrgID)
		if err != nil {
			return nil, err
		}
		if err := s.checkNoteInvoice(ctx, tx, p.OrgID, row.LeaseID, uint64(row.InvoiceID.Int64), row.Amount, true); err != nil {
			return nil, err
		}
		invoice, err := s.repo.InvoiceForUpdate(ctx, tx, p.OrgID, uint64(row.InvoiceID.Int64), p.Scope, false)
		if err != nil {
			return nil, err
		}
		preTax, tax := splitPreTax(invoice.Subtotal, invoice.TaxTotal, row.Amount)
		if err := s.repo.ReduceInvoiceTotal(ctx, tx, p.OrgID, invoice.ID, preTax, tax, row.Amount); err != nil {
			if isTriggerRejection(err) {
				return nil, httpx.Conflict("This credit note cannot be applied to that invoice")
			}
			return nil, err
		}
		lines := creditNoteLines(row.Amount, preTax, tax, "Credit note "+row.CreditNoteNumber)
		if _, err := s.repo.PostJournal(ctx, tx, p.OrgID, row.LeaseID, sourceCreditNote, row.ID,
			truncateDate(time.Now().UTC()), settings.CurrencyCode, p.UserID, lines); err != nil {
			return nil, s.postingFailure(err, "credit note")
		}
		now := time.Now().UTC()
		row.Status = creditApplied
		row.AppliedAt = sql.NullTime{Time: now, Valid: true}
		if err := s.settleInvoice(ctx, tx, p.OrgID, invoice.ID, truncateDate(now)); err != nil {
			return nil, err
		}
		return map[string]any{"status": creditApplied, "invoice_id": invoice.ID,
			"invoice_number": invoice.InvoiceNumber, "amount": row.Amount, "applied_at": now.Format(time.RFC3339)}, nil
	}, "credit_notes.applied", meta)
	if err != nil {
		return nil, err
	}
	return s.creditNoteDetail(ctx, p.OrgID, id, p.Scope)
}

// VoidCreditNote cancels a note that was never applied. An applied note has
// already moved money, so it is final: the fix is another invoice, not a void.
func (s *Service) VoidCreditNote(ctx context.Context, p *rbac.Principal, id uint64, in VoidCreditNoteInput, meta ClientMeta) (*CreditNoteDetailDTO, error) {
	if err := authorize(p, "invoices.update"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	reason := strings.TrimSpace(in.Reason)
	if utf8.RuneCountInString(reason) > maxReasonLen {
		return nil, httpx.Validation(map[string]string{
			"reason": fmt.Sprintf("must be at most %d characters", maxReasonLen),
		})
	}

	err := s.mutateCreditNote(ctx, p, id, func(tx *sqlx.Tx, row *CreditNoteRow) (map[string]any, error) {
		row.Status = creditVoid
		return map[string]any{"status": creditVoid, "reason": reason}, nil
	}, "credit_notes.voided", meta)
	if err != nil {
		return nil, err
	}
	return s.creditNoteDetail(ctx, p.OrgID, id, p.Scope)
}

func (s *Service) DeleteCreditNote(ctx context.Context, p *rbac.Principal, id uint64, meta ClientMeta) error {
	if err := authorize(p, "invoices.delete"); err != nil {
		return err
	}
	if id == 0 {
		return httpx.ErrNotFound
	}

	var number string
	var amount float64
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		row, err := s.repo.CreditNoteForUpdate(ctx, tx, p.OrgID, id, p.Scope)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if row.Status != creditDraft {
			return httpx.Conflict("Only a draft credit note can be deleted")
		}
		affected, err := s.repo.DeleteCreditNote(ctx, tx, p.OrgID, id)
		if err != nil {
			return err
		}
		if affected == 0 {
			return httpx.Conflict("Only a draft credit note can be deleted")
		}
		number, amount = row.CreditNoteNumber, row.Amount
		return nil
	})
	if err != nil {
		return err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "credit_notes.deleted", EntityType: "credit_notes", EntityID: id,
		OldValues: map[string]any{"credit_note_number": number, "amount": amount},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return nil
}

// mutateCreditNote locks a note, refuses a transition the schema would reject
// anyway, and keeps the audit trail in one place.
func (s *Service) mutateCreditNote(ctx context.Context, p *rbac.Principal, id uint64,
	fn func(tx *sqlx.Tx, row *CreditNoteRow) (map[string]any, error), action string, meta ClientMeta) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		row, err := s.repo.CreditNoteForUpdate(ctx, tx, p.OrgID, id, p.Scope)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		required, refusal := noteTransition(action)
		if row.Status != required {
			return httpx.Conflict(refusal)
		}
		statusBefore := row.Status
		values, err := fn(tx, row)
		if err != nil {
			return err
		}
		if err := s.repo.UpdateCreditNote(ctx, tx, row); err != nil {
			if isTriggerRejection(err) {
				return httpx.Conflict("This credit note cannot be changed any more")
			}
			if isForeignKeyRestrict(err) {
				return httpx.Validation(map[string]string{"invoice_id": "does not belong to this lease"})
			}
			return err
		}
		s.writeAudit(ctx, audit.Entry{
			OrgID: p.OrgID, UserID: p.UserID,
			Action: action, EntityType: "credit_notes", EntityID: row.ID,
			OldValues: map[string]any{"status": statusBefore}, NewValues: values,
			IPAddress: meta.IP, UserAgent: meta.UserAgent,
		})
		return nil
	})
}

// noteTransition states which status a note must already be in for one action
// and what to say when it is not, so no caller can re-write a finalised note.
func noteTransition(action string) (string, string) {
	switch action {
	case "credit_notes.applied":
		return creditIssued, "Only an issued credit note can be applied"
	case "credit_notes.voided":
		return creditIssued, "Only an issued credit note can be voided"
	case "credit_notes.issued":
		return creditDraft, "Only a draft credit note can be issued"
	default:
		return creditDraft, "Only a draft credit note can be changed"
	}
}

// checkNoteInvoice keeps a note on the lease it belongs to and, when enforce is
// set, that the note is no larger than the balance the invoice still owes.
func (s *Service) checkNoteInvoice(ctx context.Context, tx *sqlx.Tx, orgID, leaseID, invoiceID uint64, amount float64, enforce bool) error {
	if invoiceID == 0 {
		return httpx.Validation(map[string]string{"invoice_id": "required"})
	}
	balance, err := s.repo.InvoiceBalanceByID(ctx, tx, orgID, invoiceID)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.Validation(map[string]string{"invoice_id": "does not exist"})
	}
	if err != nil {
		return err
	}
	if balance.LeaseID != leaseID {
		return httpx.Validation(map[string]string{"invoice_id": "belongs to another lease"})
	}
	if balance.Status == statusDraft {
		return httpx.Conflict("A credit note cannot settle a draft invoice")
	}
	if balance.Status == statusVoid {
		return httpx.Conflict("A credit note cannot settle a void invoice")
	}
	if balance.Status == statusPaid && enforce {
		return httpx.Conflict("This invoice is already paid; reverse the payment instead")
	}
	if enforce && amount > balance.Balance+ledgerTolerance {
		return httpx.Conflict("This credit note is larger than the balance this invoice still owes")
	}
	return nil
}

func (s *Service) creditNoteDetail(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*CreditNoteDetailDTO, error) {
	row, err := s.repo.GetCreditNote(ctx, orgID, id, scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	entries, err := s.repo.LedgerForSource(ctx, orgID, sourceCreditNote, id)
	if err != nil {
		return nil, err
	}
	return &CreditNoteDetailDTO{
		CreditNoteSummaryDTO: creditNoteSummaryDTO(row),
		Ledger:               ledgerEntryDTOs(entries),
	}, nil
}

func buildCreditNote(orgID, userID uint64, in CreateCreditNoteInput) (*CreditNoteRow, map[string]string) {
	fields := map[string]string{}
	row := &CreditNoteRow{
		OrganizationID: orgID, LeaseID: in.LeaseID,
		InvoiceID: optID(in.InvoiceID),
		Amount:    roundToCents(in.Amount),
		Reason:    optText(strings.TrimSpace(in.Reason)),
		Status:    creditDraft,
		IssueDate: truncateDate(time.Now().UTC()),
		CreatedBy: optID(userID),
	}
	if in.LeaseID == 0 {
		fields["lease_id"] = "required"
	}
	if in.Amount <= 0 {
		fields["amount"] = "must be greater than zero"
	} else if in.Amount > maxAmount {
		fields["amount"] = fmt.Sprintf("must be at most %.2f", maxAmount)
	}
	if in.CreditNoteNumber != "" {
		number := cleanNumber(in.CreditNoteNumber, "credit_note_number", fields)
		if number != "" && utf8.RuneCountInString(number) > maxNumberLen {
			fields["credit_note_number"] = fmt.Sprintf("must be at most %d characters", maxNumberLen)
		}
		row.CreditNoteNumber = number
	}
	if utf8.RuneCountInString(row.Reason.String) > maxNoteLen {
		fields["reason"] = fmt.Sprintf("must be at most %d characters", maxNoteLen)
	}
	if in.IssueDate != nil {
		issueDate := parseOptionalDate("issue_date", in.IssueDate, fields)
		if !issueDate.IsZero() {
			row.IssueDate = issueDate
		}
	}
	return row, fields
}

func normalizeCreditNoteQuery(q *CreditNoteQuery) error {
	fields := map[string]string{}
	q.Search = strings.TrimSpace(q.Search)
	if utf8.RuneCountInString(q.Search) > maxSearchLen {
		fields["q"] = fmt.Sprintf("must be at most %d characters", maxSearchLen)
	}
	q.Status = strings.ToLower(strings.TrimSpace(q.Status))
	if q.Status != "" && !inList(creditStatuses, q.Status) {
		fields["status"] = "must be one of " + strings.Join(creditStatuses, ", ")
	}
	if len(fields) > 0 {
		return httpx.Validation(fields)
	}
	return nil
}

func creditNoteSummaryDTO(row *CreditNoteViewRow) CreditNoteSummaryDTO {
	return CreditNoteSummaryDTO{
		ID: row.ID, CreditNoteNumber: row.CreditNoteNumber,
		Lease: leaseRef(row.LeaseID, row.LeaseNumber, row.UnitNumber, row.PropertyID,
			row.PropertyName, ""),
		InvoiceID: nullIDValue(row.InvoiceID), InvoiceNumber: nullText(row.InvoiceNumber),
		PrimaryTenant: nullText(row.PrimaryTenant),
		Amount:        roundToCents(row.Amount), Reason: nullText(row.Reason),
		Status: row.Status, IssueDate: row.IssueDate, AppliedAt: nullTimePointer(row.AppliedAt),
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
