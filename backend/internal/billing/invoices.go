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

func (s *Service) Invoices(ctx context.Context, p *rbac.Principal, q InvoiceQuery) (*InvoiceListDTO, error) {
	if err := authorize(p, "invoices.view"); err != nil {
		return nil, err
	}
	if err := normalizeInvoiceQuery(&q, p.Scope); err != nil {
		return nil, err
	}
	rows, total, err := s.repo.ListInvoices(ctx, p.OrgID, p.Scope, q, tenantOnly(p.Scope))
	if err != nil {
		return nil, err
	}
	items := make([]InvoiceSummaryDTO, 0, len(rows))
	for i := range rows {
		items = append(items, invoiceSummaryDTO(&rows[i]))
	}
	return &InvoiceListDTO{Invoices: items, Total: total}, nil
}

func (s *Service) Invoice(ctx context.Context, p *rbac.Principal, id uint64) (*InvoiceDetailDTO, error) {
	if err := authorize(p, "invoices.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	return s.detail(ctx, p.OrgID, id, p.Scope)
}

// InvoiceLedger returns the postings behind one invoice so a dispute can be
// traced to the journal that created it.
func (s *Service) InvoiceLedger(ctx context.Context, p *rbac.Principal, id uint64) ([]LedgerEntryDTO, error) {
	if err := authorize(p, "invoices.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	if _, err := s.repo.GetInvoice(ctx, p.OrgID, id, p.Scope, tenantOnly(p.Scope)); errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	rows, err := s.repo.LedgerForSource(ctx, p.OrgID, sourceInvoice, id)
	if err != nil {
		return nil, err
	}
	return ledgerEntryDTOs(rows), nil
}

func (s *Service) Summary(ctx context.Context, p *rbac.Principal, q InvoiceQuery) (*BillingSummaryDTO, error) {
	if err := authorize(p, "invoices.view"); err != nil {
		return nil, err
	}
	if err := normalizeInvoiceQuery(&q, p.Scope); err != nil {
		return nil, err
	}
	settings, err := s.repo.Settings(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	timezone, err := s.repo.OrganizationTimezone(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	row, err := s.repo.Summary(ctx, p.OrgID, p.Scope, q, tenantOnly(p.Scope), todayIn(orgLocation(timezone)))
	if err != nil {
		return nil, err
	}
	return &BillingSummaryDTO{
		Currency: settings.CurrencyCode,
		Billed:   roundToCents(row.Billed), Collected: roundToCents(row.Collected),
		Outstanding: roundToCents(row.Outstanding), Overdue: roundToCents(row.Overdue),
		Credit: roundToCents(row.Credit), OpenCount: row.OpenCount,
		OverdueCount: row.OverdueRows, DraftCount: row.DraftCount,
	}, nil
}

// CreateInvoice writes a manual invoice. Every figure comes from the lines the
// caller sends: quantity times unit price, plus tax. Nothing in the body can
// state a total, a balance or a status.
func (s *Service) CreateInvoice(ctx context.Context, p *rbac.Principal, in CreateInvoiceInput, meta ClientMeta) (*InvoiceDetailDTO, error) {
	if err := authorize(p, "invoices.create"); err != nil {
		return nil, err
	}
	fields := map[string]string{}
	row, items := buildInvoiceCreate(p.OrgID, p.UserID, in, fields)
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	lease, err := s.billingLease(ctx, tx, p, row.LeaseID, true)
	if err != nil {
		return nil, err
	}
	settings, err := s.repo.Settings(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	if clash, taken, err := s.repo.PeriodTaken(ctx, tx, p.OrgID, row.LeaseID, row.InvoiceType, row.BillingPeriodStart, 0); err != nil {
		return nil, err
	} else if taken {
		return nil, httpx.Conflict(periodAlreadyBilled + " (" + clash + ")")
	}
	if row.InvoiceNumber == "" {
		number, err := s.repo.NextInvoiceNumber(ctx, tx, p.OrgID,
			numberPrefix(settings.InvoicePrefix, nullText(lease.PropertyCode)), row.BillingPeriodStart)
		if err != nil {
			return nil, err
		}
		if utf8.RuneCountInString(number) > maxNumberLen {
			return nil, httpx.Validation(map[string]string{
				"invoice_number": fmt.Sprintf("generated number is longer than %d characters, send your own", maxNumberLen)})
		}
		row.InvoiceNumber = number
	} else if taken, err := s.repo.InvoiceNumberTaken(ctx, tx, p.OrgID, row.InvoiceNumber, 0); err != nil {
		return nil, err
	} else if taken {
		return nil, httpx.Conflict("An invoice with that number already exists")
	}

	if in.Issue {
		row.Status = statusIssued
	}
	id, err := s.repo.InsertInvoice(ctx, tx, row)
	if err != nil {
		if isDuplicateEntry(err) {
			return nil, httpx.Conflict("An invoice with that number already exists")
		}
		if isTriggerRejection(err) {
			return nil, s.rejectTrigger(err, "create invoice")
		}
		return nil, err
	}
	for i := range items {
		items[i].InvoiceID = id
		if _, err := s.repo.InsertItem(ctx, tx, &items[i]); err != nil {
			return nil, err
		}
	}
	if in.Issue {
		if err := s.postIssue(ctx, tx, p.OrgID, id, row.LeaseID, items,
			row.TaxTotal, row.Total, row.IssueDate, settings.CurrencyCode, p.UserID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "invoices.created", EntityType: "invoices", EntityID: id,
		NewValues: map[string]any{
			"invoice_number": row.InvoiceNumber,
			"lease_id":       row.LeaseID,
			"invoice_type":   row.InvoiceType,
			"total":          row.Total,
			"status":         row.Status,
			"items":          len(items),
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.detail(ctx, p.OrgID, id, p.Scope)
}

func (s *Service) UpdateInvoice(ctx context.Context, p *rbac.Principal, id uint64, in UpdateInvoiceInput, meta ClientMeta) (*InvoiceDetailDTO, error) {
	if err := authorize(p, "invoices.update"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := s.repo.InvoiceForUpdate(ctx, tx, p.OrgID, id, p.Scope, false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.Status != statusDraft {
		return nil, httpx.Conflict(notDraftInvoice)
	}

	fields := map[string]string{}
	oldValues, newValues := applyInvoicePatch(row, in, fields)
	var items []InvoiceItemRow
	if in.Items != nil {
		items = buildInvoiceItems(*in.Items, fields)
		subtotal, taxTotal, total := totalsOf(items)
		if len(fields) == 0 {
			if total <= 0 {
				fields["items"] = "must add up to more than zero"
			} else {
				row.Subtotal, row.TaxTotal, row.Total = subtotal, taxTotal, total
			}
		}
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	if !row.BillingPeriodEnd.After(row.BillingPeriodStart) && !row.BillingPeriodEnd.Equal(row.BillingPeriodStart) {
		return nil, httpx.Validation(map[string]string{"billing_period_end": "must be on or after billing_period_start"})
	}
	if row.DueDate.Before(row.IssueDate) {
		return nil, httpx.Validation(map[string]string{"due_date": "cannot be earlier than issue_date"})
	}
	if row.InvoiceNumber != newValues["invoice_number"] && row.InvoiceNumber != "" {
		taken, err := s.repo.InvoiceNumberTaken(ctx, tx, p.OrgID, row.InvoiceNumber, id)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, httpx.Conflict("An invoice with that number already exists")
		}
	}
	if clash, taken, err := s.repo.PeriodTaken(ctx, tx, p.OrgID, row.LeaseID, row.InvoiceType, row.BillingPeriodStart, id); err != nil {
		return nil, err
	} else if taken {
		return nil, httpx.Conflict(periodAlreadyBilled + " (" + clash + ")")
	}

	if err := s.repo.UpdateInvoice(ctx, tx, row); err != nil {
		if isTriggerRejection(err) {
			return nil, s.rejectTrigger(err, "update invoice")
		}
		return nil, err
	}
	if in.Items != nil {
		if err := s.repo.ReplaceItems(ctx, tx, p.OrgID, id, items); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "invoices.updated", EntityType: "invoices", EntityID: id,
		OldValues: oldValues, NewValues: newValues,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.detail(ctx, p.OrgID, id, p.Scope)
}

// IssueInvoice makes a draft billable: the receivable and the income postings
// land in the ledger here, never at insert time, so a draft is invisible to the
// accounts until somebody with invoices.update signs it off.
func (s *Service) IssueInvoice(ctx context.Context, p *rbac.Principal, id uint64, in IssueInput, meta ClientMeta) (*InvoiceDetailDTO, error) {
	if err := authorize(p, "invoices.update"); err != nil {
		return nil, err
	}
	return s.mutateInvoice(ctx, p, id, meta, "invoices.issued", func(ctx context.Context, tx *sqlx.Tx, p *rbac.Principal, row *InvoiceRow, settings BillingSettings) (map[string]any, error) {
		if row.Status != statusDraft {
			return nil, httpx.Conflict("Only a draft invoice can be issued")
		}
		fields := map[string]string{}
		if issue := parseOptionalDate("issue_date", in.IssueDate, fields); !issue.IsZero() {
			row.IssueDate = issue
		}
		if due := parseOptionalDate("due_date", in.DueDate, fields); !due.IsZero() {
			row.DueDate = due
		}
		if len(fields) > 0 {
			return nil, httpx.Validation(fields)
		}
		if row.DueDate.Before(row.IssueDate) {
			return nil, httpx.Validation(map[string]string{"due_date": "cannot be earlier than issue_date"})
		}
		items, err := s.repo.Items(ctx, p.OrgID, id)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return nil, httpx.Conflict("An invoice needs at least one line before it can be issued")
		}
		subtotal, taxTotal, total := totalsOf(items)
		if total <= 0 {
			return nil, httpx.Conflict("An invoice needs a positive total before it can be issued")
		}
		row.Subtotal, row.TaxTotal, row.Total, row.Status = subtotal, taxTotal, total, statusIssued
		if err := s.postIssue(ctx, tx, p.OrgID, id, row.LeaseID, items,
			taxTotal, total, row.IssueDate, settings.CurrencyCode, p.UserID); err != nil {
			return nil, err
		}
		return map[string]any{"status": statusIssued, "total": total, "issue_date": row.IssueDate.Format(dateLayout)}, nil
	})
}

// VoidInvoice cancels an unpaid invoice and unwinds its postings. Money that
// has already been allocated cannot be voided away: that is what a payment
// reversal and a credit note are for.
func (s *Service) VoidInvoice(ctx context.Context, p *rbac.Principal, id uint64, in VoidInput, meta ClientMeta) (*InvoiceDetailDTO, error) {
	if err := authorize(p, "invoices.update"); err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(in.Reason)
	if reason != "" && utf8.RuneCountInString(reason) > maxReasonLen {
		return nil, httpx.Validation(map[string]string{"reason": fmt.Sprintf("must be at most %d characters", maxReasonLen)})
	}
	return s.mutateInvoice(ctx, p, id, meta, "invoices.voided", func(ctx context.Context, tx *sqlx.Tx, p *rbac.Principal, row *InvoiceRow, settings BillingSettings) (map[string]any, error) {
		if row.Status == statusVoid {
			return nil, httpx.Conflict("This invoice is already void")
		}
		if row.AmountPaid > 0 {
			return nil, httpx.Conflict(moneyOnInvoice)
		}
		if row.Status != statusDraft {
			items, err := s.repo.Items(ctx, p.OrgID, id)
			if err != nil {
				return nil, err
			}
			lines := mirror(issueLines(items, row.TaxTotal, row.Total, "Void "+row.InvoiceNumber))
			if _, err := s.repo.PostJournal(ctx, tx, p.OrgID, row.LeaseID, sourceInvoice, id,
				truncateDate(row.IssueDate), settings.CurrencyCode, p.UserID, lines); err != nil {
				return nil, err
			}
			if err := s.repo.MarkFeesWaivedOnVoid(ctx, tx, p.OrgID, id, p.UserID); err != nil {
				return nil, err
			}
		}
		row.Status = statusVoid
		return map[string]any{"status": statusVoid, "reason": reason}, nil
	})
}

func (s *Service) DeleteInvoice(ctx context.Context, p *rbac.Principal, id uint64, meta ClientMeta) error {
	if err := authorize(p, "invoices.delete"); err != nil {
		return err
	}
	if id == 0 {
		return httpx.ErrNotFound
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := s.repo.InvoiceForUpdate(ctx, tx, p.OrgID, id, p.Scope, false)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	if row.Status != statusDraft {
		return httpx.Conflict("Only a draft invoice can be deleted")
	}
	affected, err := s.repo.DeleteInvoice(ctx, tx, p.OrgID, id)
	if err != nil {
		if isForeignKeyRestrict(err) {
			return httpx.Conflict("This invoice is referenced by a credit note or another record")
		}
		return err
	}
	if affected == 0 {
		return httpx.Conflict("Only a draft invoice can be deleted")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "invoices.deleted", EntityType: "invoices", EntityID: id,
		OldValues: map[string]any{"invoice_number": row.InvoiceNumber, "total": row.Total},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return nil
}

// mutateInvoice locks an invoice, runs one rule set over it and writes the
// result plus an audit row, so every status change shares the same guards.
func (s *Service) mutateInvoice(ctx context.Context, p *rbac.Principal, id uint64, meta ClientMeta, action string, fn mutateFunc) (*InvoiceDetailDTO, error) {
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := s.repo.InvoiceForUpdate(ctx, tx, p.OrgID, id, p.Scope, false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	settings, err := s.repo.Settings(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	statusBefore := row.Status
	newValues, err := fn(ctx, tx, p, row, settings)
	if err != nil {
		return nil, err
	}
	if err := s.repo.UpdateInvoice(ctx, tx, row); err != nil {
		if isTriggerRejection(err) {
			return nil, s.rejectTrigger(err, action)
		}
		return nil, err
	}
	today := truncateDate(time.Now().UTC())
	if err := s.settleInvoice(ctx, tx, p.OrgID, id, today); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	values := map[string]any{"invoice_number": row.InvoiceNumber, "status": row.Status}
	for key, value := range newValues {
		values[key] = value
	}
	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: action, EntityType: "invoices", EntityID: id,
		OldValues: map[string]any{"status": statusBefore}, NewValues: values,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return s.detail(ctx, p.OrgID, id, p.Scope)
}

type mutateFunc func(ctx context.Context, tx *sqlx.Tx, p *rbac.Principal, row *InvoiceRow, settings BillingSettings) (map[string]any, error)

func normalizeInvoiceQuery(q *InvoiceQuery, scope rbac.Scope) error {
	fields := map[string]string{}
	q.Search = strings.TrimSpace(q.Search)
	if utf8.RuneCountInString(q.Search) > maxSearchLen {
		fields["q"] = fmt.Sprintf("must be at most %d characters", maxSearchLen)
	}
	q.Status = strings.ToLower(strings.TrimSpace(q.Status))
	if q.Status != "" && !inList(invoiceStatuses, q.Status) {
		fields["status"] = "must be one of " + strings.Join(invoiceStatuses, ", ")
	}
	q.Type = strings.ToLower(strings.TrimSpace(q.Type))
	if q.Type != "" && !inList(invoiceTypes, q.Type) {
		fields["invoice_type"] = "must be one of " + strings.Join(invoiceTypes, ", ")
	}
	if !q.DueFrom.IsZero() && !q.DueTo.IsZero() && q.DueFrom.After(q.DueTo) {
		fields["due_from"] = "must be on or before due_to"
	}
	// A tenant may not narrow a query into drafts they are not allowed to see.
	if q.Status == statusDraft && tenantOnly(scope) {
		fields["status"] = "draft invoices are not available to tenants"
	}
	if len(fields) > 0 {
		return httpx.Validation(fields)
	}
	return nil
}

// invoiceStatusFor derives the label from the money, never the other way round.
func invoiceStatusFor(status string, total, paid float64, dueDate, today time.Time) string {
	if status == statusDraft || status == statusVoid {
		return status
	}
	switch {
	case total > 0 && paid+ledgerTolerance >= total:
		return statusPaid
	case paid > 0:
		return statusPartiallyPaid
	case dueDate.Before(today):
		return statusOverdue
	default:
		return status
	}
}

func buildInvoiceCreate(orgID, userID uint64, in CreateInvoiceInput, fields map[string]string) (*InvoiceRow, []InvoiceItemRow) {
	row := &InvoiceRow{
		OrganizationID: orgID,
		LeaseID:        in.LeaseID,
		InvoiceType:    typeRent,
		Status:         statusDraft,
		CreatedBy:      sql.NullInt64{Int64: int64(userID), Valid: true},
	}
	if in.LeaseID == 0 {
		fields["lease_id"] = "required"
	}
	if in.InvoiceType != "" {
		typ := strings.ToLower(strings.TrimSpace(in.InvoiceType))
		if !inList(invoiceTypes, typ) {
			fields["invoice_type"] = "must be one of " + strings.Join(invoiceTypes, ", ")
		} else {
			row.InvoiceType = typ
		}
	}
	start, startOK := parseDate(in.BillingPeriodStart)
	if in.BillingPeriodStart == "" {
		fields["billing_period_start"] = "required"
	} else if !startOK {
		fields["billing_period_start"] = "must be a date formatted as " + dateLayout
	}
	end, endOK := parseDate(in.BillingPeriodEnd)
	switch {
	case in.BillingPeriodEnd == "":
		fields["billing_period_end"] = "required"
	case !endOK:
		fields["billing_period_end"] = "must be a date formatted as " + dateLayout
	case startOK && !end.IsZero() && end.Before(start):
		fields["billing_period_end"] = "must be on or after billing_period_start"
	}
	row.BillingPeriodStart, row.BillingPeriodEnd = start, end

	issue := start
	if in.IssueDate != nil {
		parsed, ok := parseDate(*in.IssueDate)
		switch {
		case !ok:
			fields["issue_date"] = "must be a date formatted as " + dateLayout
		default:
			issue = parsed
		}
	}
	row.IssueDate = issue
	due := end
	if in.DueDate != nil {
		parsed, ok := parseDate(*in.DueDate)
		switch {
		case !ok:
			fields["due_date"] = "must be a date formatted as " + dateLayout
		case !issue.IsZero() && parsed.Before(issue):
			fields["due_date"] = "cannot be earlier than issue_date"
		default:
			due = parsed
		}
	}
	row.DueDate = due
	if !due.IsZero() && !issue.IsZero() && due.Before(issue) {
		fields["due_date"] = "cannot be earlier than issue_date"
	}

	note := strings.TrimSpace(in.Notes)
	if utf8.RuneCountInString(note) > maxNoteLen {
		fields["notes"] = fmt.Sprintf("must be at most %d characters", maxNoteLen)
	}
	row.Notes = optText(note)
	row.InvoiceNumber = cleanNumber(in.InvoiceNumber, "invoice_number", fields)

	items := buildInvoiceItems(in.Items, fields)
	subtotal, taxTotal, total := totalsOf(items)
	if len(items) == 0 {
		fields["items"] = "at least one line is required"
	} else if total <= 0 {
		fields["items"] = "must add up to more than zero"
	}
	row.Subtotal, row.TaxTotal, row.Total = subtotal, taxTotal, total
	return row, items
}

// buildInvoiceItems prices every line on the server. A client cannot send an
// amount, only the quantity, price and tax rate the amount is computed from.
func buildInvoiceItems(in []ItemInput, fields map[string]string) []InvoiceItemRow {
	items := make([]InvoiceItemRow, 0, len(in))
	if len(in) > maxItems {
		fields["items"] = fmt.Sprintf("must contain at most %d lines", maxItems)
		return items
	}
	for _, raw := range in {
		key := "items"
		item := InvoiceItemRow{ItemType: strings.ToLower(strings.TrimSpace(raw.ItemType))}
		if item.ItemType == "" {
			fields[key] = "every line needs an item_type"
			continue
		}
		if !inList(itemTypes, item.ItemType) {
			fields[key] = "item_type must be one of " + strings.Join(itemTypes, ", ")
			continue
		}
		description := strings.TrimSpace(raw.Description)
		if description == "" {
			fields[key] = "every line needs a description"
			continue
		}
		if utf8.RuneCountInString(description) > maxDescription {
			fields[key] = fmt.Sprintf("a line description must be at most %d characters", maxDescription)
			continue
		}
		item.Description = truncateString(description, maxDescription)

		quantity := 1.0
		if raw.Quantity != nil {
			quantity = *raw.Quantity
		}
		if quantity <= 0 || quantity > maxQuantity {
			fields[key] = fmt.Sprintf("a line quantity must be greater than zero and at most %.3f", maxQuantity)
			continue
		}
		price := roundToCents(raw.UnitPrice)
		if price < 0 || price > maxAmount {
			fields[key] = fmt.Sprintf("a line unit_price must be between 0 and %.2f", maxAmount)
			continue
		}
		rate := 0.0
		if raw.TaxRate != nil {
			rate = roundToCents(*raw.TaxRate)
			if rate < 0 || rate > maxTaxRate {
				fields[key] = "a line tax_rate must be between 0 and 100"
				continue
			}
		}
		amount := roundToCents(quantity * price)
		if amount > maxAmount {
			fields[key] = fmt.Sprintf("a line amount must be at most %.2f", maxAmount)
			continue
		}
		item.Quantity = roundQuantity(quantity)
		item.UnitPrice = price
		item.TaxRate = rate
		item.Amount = amount
		sourceType := strings.ToLower(strings.TrimSpace(raw.SourceType))
		if sourceType != "" {
			item.SourceType = sql.NullString{String: sourceType, Valid: true}
		}
		if raw.SourceID != 0 {
			item.SourceID = sql.NullInt64{Int64: int64(raw.SourceID), Valid: true}
		} else if item.SourceType.Valid {
			fields[key] = "a line with a source_type needs a source_id"
			continue
		}
		items = append(items, item)
	}
	return items
}

func totalsOf(items []InvoiceItemRow) (subtotal, taxTotal, total float64) {
	for _, item := range items {
		subtotal += item.Amount
		taxTotal += roundToCents(item.Amount * item.TaxRate / 100)
	}
	subtotal, taxTotal = roundToCents(subtotal), roundToCents(taxTotal)
	return subtotal, taxTotal, roundToCents(subtotal + taxTotal)
}

func applyInvoicePatch(row *InvoiceRow, in UpdateInvoiceInput, fields map[string]string) (map[string]any, map[string]any) {
	oldValues, newValues := map[string]any{}, map[string]any{}
	record := func(key string, before, after any) {
		oldValues[key] = before
		newValues[key] = after
	}
	if in.InvoiceNumber != nil {
		number := cleanNumber(*in.InvoiceNumber, "invoice_number", fields)
		switch {
		case fields["invoice_number"] != "":
		case number == "":
			fields["invoice_number"] = "required"
		case number != row.InvoiceNumber:
			record("invoice_number", row.InvoiceNumber, number)
			row.InvoiceNumber = number
		}
	}
	if in.InvoiceType != nil {
		typ := strings.ToLower(strings.TrimSpace(*in.InvoiceType))
		if !inList(invoiceTypes, typ) {
			fields["invoice_type"] = "must be one of " + strings.Join(invoiceTypes, ", ")
		} else if typ != row.InvoiceType {
			record("invoice_type", row.InvoiceType, typ)
			row.InvoiceType = typ
		}
	}
	if start := parseOptionalDate("billing_period_start", in.BillingPeriodStart, fields); !start.IsZero() && start != row.BillingPeriodStart {
		record("billing_period_start", row.BillingPeriodStart.Format(dateLayout), start.Format(dateLayout))
		row.BillingPeriodStart = start
	}
	if end := parseOptionalDate("billing_period_end", in.BillingPeriodEnd, fields); !end.IsZero() && end != row.BillingPeriodEnd {
		record("billing_period_end", row.BillingPeriodEnd.Format(dateLayout), end.Format(dateLayout))
		row.BillingPeriodEnd = end
	}
	if issue := parseOptionalDate("issue_date", in.IssueDate, fields); !issue.IsZero() && issue != row.IssueDate {
		record("issue_date", row.IssueDate.Format(dateLayout), issue.Format(dateLayout))
		row.IssueDate = issue
	}
	if due := parseOptionalDate("due_date", in.DueDate, fields); !due.IsZero() && due != row.DueDate {
		record("due_date", row.DueDate.Format(dateLayout), due.Format(dateLayout))
		row.DueDate = due
	}
	if in.Notes != nil && !isJSONNull(in.Notes) {
		var note string
		if err := json.Unmarshal(in.Notes, &note); err != nil {
			fields["notes"] = "must be a string or null"
		} else {
			note = strings.TrimSpace(note)
			if utf8.RuneCountInString(note) > maxNoteLen {
				fields["notes"] = fmt.Sprintf("must be at most %d characters", maxNoteLen)
			} else {
				record("notes", nullText(row.Notes), note)
				row.Notes = optText(note)
			}
		}
	}
	return oldValues, newValues
}

func invoiceSummaryDTO(row *InvoiceViewRow) InvoiceSummaryDTO {
	return InvoiceSummaryDTO{
		ID: row.ID, InvoiceNumber: row.InvoiceNumber, InvoiceType: row.InvoiceType,
		Lease:         leaseRef(row.LeaseID, row.LeaseNumber, row.UnitNumber, row.PropertyID, row.PropertyName, nullText(row.PropertyCode)),
		PrimaryTenant: nullText(row.PrimaryTenant),
		PeriodStart:   row.BillingPeriodStart, PeriodEnd: row.BillingPeriodEnd,
		IssueDate: row.IssueDate, DueDate: row.DueDate,
		Subtotal: row.Subtotal, TaxTotal: row.TaxTotal, Total: row.Total,
		AmountPaid: row.AmountPaid, Balance: row.Balance, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func invoiceDetailDTO(row *InvoiceViewRow, items []InvoiceItemDTO, allocations []AllocationDTO, fees []LateFeeDTO) InvoiceDetailDTO {
	return InvoiceDetailDTO{
		InvoiceSummaryDTO: invoiceSummaryDTO(row),
		Notes:             nullText(row.Notes),
		Items:             items,
		Allocations:       allocations,
		LateFees:          fees,
	}
}

func leaseRef(id uint64, number, unit string, propertyID uint64, propertyName, propertyCode string) LeaseRefDTO {
	return LeaseRefDTO{
		ID: id, Number: number, Unit: unit,
		Property: PropertyRefDTO{ID: propertyID, Name: propertyName, Code: propertyCode},
	}
}

func itemDTOs(rows []InvoiceItemRow) []InvoiceItemDTO {
	out := make([]InvoiceItemDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, InvoiceItemDTO{
			ID: row.ID, ItemType: row.ItemType, Description: row.Description,
			Quantity: row.Quantity, UnitPrice: row.UnitPrice, TaxRate: row.TaxRate,
			Amount: row.Amount, SourceType: nullText(row.SourceType), SourceID: nullIDValue(row.SourceID),
		})
	}
	return out
}
