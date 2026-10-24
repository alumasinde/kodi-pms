package billing

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

const (
	sweepInterval = time.Hour
	sweepBatch    = 500
	sweepBatches  = 20
	feeBatch      = 200

	// billingLeadDays is how early a rent period is raised as a draft, so a
	// meter reading or a service charge can still be attached before the tenant
	// is actually billed. The invoice is issued on the day the period starts.
	billingLeadDays = 5

	// maxPeriodsPerRun keeps a lease that was billed while the API was down
	// from flooding the ledger with its whole backlog in one sweep.
	maxPeriodsPerRun = 3

	// rentTaxRate is zero: residential rent is not a VAT supply, and the demo
	// ledger bills rent gross. Utilities get their own tax rate per line.
	rentTaxRate = 0.0
)

// errNothingToBill is the sweep's "another instance already did this" signal.
// It is not a failure, so the caller logs it at debug and moves on.
var errNothingToBill = errors.New("billing: nothing to bill")

// StartBillingSweep runs the three jobs an office should not have to do by hand:
// raise the rent invoices a period is due for, mark unpaid invoices overdue and
// assess the late fees the grace period allows. Every instance can run it; the
// unique keys and the status guards in SQL make a replay a no-op.
func (s *Service) StartBillingSweep(ctx context.Context) {
	go func() {
		s.runBillingSweep(ctx)
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runBillingSweep(ctx)
			}
		}
	}()
}

func (s *Service) runBillingSweep(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	orgs, err := s.repo.Organizations(ctx)
	if err != nil {
		s.log.Error("billing sweep: load organizations", "error", err)
		return
	}
	for _, org := range orgs {
		if ctx.Err() != nil {
			return
		}
		s.sweepOrganization(ctx, org.ID, todayIn(orgLocation(org.Timezone)))
	}
}

// sweepOrganization works in the organization's own date, because a due date is
// a business date for the property manager who reads it.
func (s *Service) sweepOrganization(ctx context.Context, orgID uint64, today time.Time) {
	settings, err := s.repo.Settings(ctx, orgID)
	if err != nil {
		s.log.Error("billing sweep: settings", "org_id", orgID, "error", err)
		return
	}
	s.generateRentInvoices(ctx, orgID, settings, today)
	s.markInvoicesOverdue(ctx, orgID, today)
	s.assessLateFees(ctx, orgID, settings, today)
}

func (s *Service) generateRentInvoices(ctx context.Context, orgID uint64, settings BillingSettings, today time.Time) {
	var after uint64
	raised, issued := 0, 0
	for batch := 0; batch < sweepBatches; batch++ {
		leases, err := s.repo.InvoicingLeases(ctx, orgID, after, sweepBatch)
		if err != nil {
			s.log.Error("billing sweep: load leases", "org_id", orgID, "error", err)
			return
		}
		if len(leases) == 0 {
			break
		}
		for i := range leases {
			lease := leases[i]
			after = lease.ID
			periods := missingPeriods(lease.StartDate, nullTimeValue(lease.EndDate),
				nullTimeValue(lease.LastRentPeriod), today, lease.BillingDay, lease.DueDay,
				billingLeadDays, maxPeriodsPerRun)
			for _, period := range periods {
				done, err := s.raiseRentPeriod(ctx, orgID, &lease, period, settings, today)
				if err != nil {
					if errors.Is(err, errNothingToBill) {
						continue
					}
					s.log.Error("billing sweep: rent invoice", "org_id", orgID,
						"lease_id", lease.ID, "period", period.Start.Format(dateLayout), "error", err)
					continue
				}
				raised++
				if done {
					issued++
				}
			}
		}
		if len(leases) < sweepBatch {
			break
		}
	}
	if raised > 0 {
		s.log.Info("billing sweep: rent invoices", "org_id", orgID, "raised", raised, "issued", issued)
	}
}

// raiseRentPeriod writes one period for one lease. The lease row is re-read
// under the lock so a rent change or a termination in the same moment is seen.
func (s *Service) raiseRentPeriod(ctx context.Context, orgID uint64, snapshot *BillingLeaseRow,
	period rentPeriod, settings BillingSettings, today time.Time) (bool, error) {

	issued := !period.Start.After(today)
	var created bool

	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		lease, err := s.repo.BillingLease(ctx, tx, orgID, snapshot.ID, rbac.Scope{AllProperties: true}, true)
		if errors.Is(err, sql.ErrNoRows) {
			return errNothingToBill
		}
		if err != nil {
			return err
		}
		if lease.Status != "active" || lease.RentAmount <= 0 {
			return errNothingToBill
		}

		knownID, knownStatus, found, err := s.repo.FindInvoiceForPeriod(ctx, tx, orgID, lease.ID, typeRent, period.Start)
		if err != nil {
			return err
		}
		if found {
			if knownStatus != statusDraft || !issued {
				return errNothingToBill
			}
			return s.issueDraft(ctx, tx, orgID, knownID, lease, settings, today)
		}

		items, err := s.rentItems(ctx, tx, orgID, lease, period)
		if err != nil {
			return err
		}
		subtotal, taxTotal, total := totalsOf(items)
		if total <= 0 {
			return errNothingToBill
		}
		row := rentInvoice(orgID, lease, period, today)
		row.Subtotal, row.TaxTotal, row.Total = subtotal, taxTotal, total
		number, err := s.repo.NextInvoiceNumber(ctx, tx, orgID,
			numberPrefix(settings.InvoicePrefix, nullText(lease.PropertyCode)), period.Start)
		if err != nil {
			return err
		}
		row.InvoiceNumber = number

		newID, err := s.repo.InsertInvoice(ctx, tx, row)
		if err != nil {
			if isDuplicateEntry(err) {
				return errNothingToBill
			}
			return err
		}
		for i := range items {
			items[i].InvoiceID = newID
			if _, err := s.repo.InsertItem(ctx, tx, &items[i]); err != nil {
				return err
			}
		}
		created = true
		if !issued {
			return nil
		}
		return s.issueDraft(ctx, tx, orgID, newID, lease, settings, today)
	})
	if err != nil {
		return false, err
	}
	return issued && created, nil
}

// issueDraft moves a draft to issued and posts its journal in the same
// transaction, so an invoice is never visible without the ledger behind it.
// The issuing UPDATE is guarded on status='draft'; when another instance wins
// the race it affects no rows and no second journal is written.
func (s *Service) issueDraft(ctx context.Context, tx *sqlx.Tx, orgID, invoiceID uint64,
	lease *BillingLeaseRow, settings BillingSettings, today time.Time) error {
	items, err := s.repo.ItemsTx(ctx, tx, orgID, invoiceID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return errNothingToBill
	}
	subtotal, taxTotal, total := totalsOf(items)
	if total <= 0 {
		return errNothingToBill
	}
	rows, err := s.repo.SetInvoiceIssued(ctx, tx, orgID, invoiceID, subtotal, taxTotal, total,
		truncateDate(today))
	if err != nil {
		return err
	}
	if rows == 0 {
		return errNothingToBill
	}
	return s.postIssue(ctx, tx, orgID, invoiceID, lease.ID, items, taxTotal, total,
		today, settings.CurrencyCode, 0)
}

// rentItems prices the period. The deposit rides on the very first rent
// invoice, and only while the deposit record is still pending.
func (s *Service) rentItems(ctx context.Context, tx *sqlx.Tx, orgID uint64, lease *BillingLeaseRow, period rentPeriod) ([]InvoiceItemRow, error) {
	items := []InvoiceItemRow{rentItem(lease, period)}
	if !period.First || lease.DepositAmount <= 0 || !lease.PendingDeposit.Valid {
		return items, nil
	}
	has, err := s.repo.HasDepositItem(ctx, tx, orgID, lease.ID)
	if err != nil || has {
		return items, err
	}
	return append(items, depositItem(lease)), nil
}

func rentItem(lease *BillingLeaseRow, period rentPeriod) InvoiceItemRow {
	return InvoiceItemRow{
		OrganizationID: lease.OrganizationID,
		ItemType:       itemRent,
		Description:    truncateString("Rent "+lease.UnitNumber+" for "+period.Start.Format("2006-01"), maxDescription),
		Quantity:       1, UnitPrice: roundToCents(lease.RentAmount),
		TaxRate: rentTaxRate, Amount: roundToCents(lease.RentAmount),
	}
}

func depositItem(lease *BillingLeaseRow) InvoiceItemRow {
	return InvoiceItemRow{
		OrganizationID: lease.OrganizationID,
		ItemType:       itemDeposit,
		Description:    truncateString("Security deposit "+lease.LeaseNumber, maxDescription),
		Quantity:       1, UnitPrice: roundToCents(lease.DepositAmount),
		TaxRate: 0, Amount: roundToCents(lease.DepositAmount),
	}
}

func rentInvoice(orgID uint64, lease *BillingLeaseRow, period rentPeriod, today time.Time) *InvoiceRow {
	issueDate := period.Start
	if today.Before(issueDate) {
		issueDate = today
	}
	return &InvoiceRow{
		OrganizationID: orgID, LeaseID: lease.ID,
		InvoiceType: typeRent, BillingPeriodStart: period.Start, BillingPeriodEnd: period.End,
		IssueDate: issueDate, DueDate: period.Due, Status: statusDraft,
	}
}

func (s *Service) markInvoicesOverdue(ctx context.Context, orgID uint64, today time.Time) {
	var total int64
	for batch := 0; batch < sweepBatches; batch++ {
		moved, err := s.repo.MarkOverdue(ctx, s.db, orgID, today, sweepBatch)
		if err != nil {
			s.log.Error("billing sweep: mark overdue", "org_id", orgID, "error", err)
			return
		}
		total += moved
		if moved < sweepBatch {
			break
		}
	}
	if total > 0 {
		s.log.Info("billing sweep: invoices marked overdue", "org_id", orgID, "count", total)
	}
}

// assessLateFees charges the invoices that are past the grace period. Machines
// assess a fee once per invoice and never repeat it; only a person can waive one,
// and a waived fee still counts as raised so the sweep does not re-charge it.
func (s *Service) assessLateFees(ctx context.Context, orgID uint64, settings BillingSettings, today time.Time) {
	if settings.LateFeeValue <= 0 {
		return
	}
	cutoff := today.AddDate(0, 0, -settings.LateFeeGraceDays)
	invoices, err := s.repo.FeeCandidates(ctx, s.db, orgID, cutoff, feeBatch)
	if err != nil {
		s.log.Error("billing sweep: late fee candidates", "org_id", orgID, "error", err)
		return
	}
	charged := 0
	for _, candidate := range invoices {
		amount := lateFeeAmount(settings.LateFeeType, settings.LateFeeValue, candidate.RentAmount)
		if amount <= 0 {
			continue
		}
		err := s.inTx(ctx, func(tx *sqlx.Tx) error {
			invoice, err := s.repo.InvoiceBalanceByID(ctx, tx, orgID, candidate.ID)
			if errors.Is(err, sql.ErrNoRows) {
				return errNothingToBill
			}
			if err != nil {
				return err
			}
			if invoice.Status == statusDraft || invoice.Status == statusVoid || invoice.Balance <= 0 {
				return errNothingToBill
			}
			has, err := s.repo.HasLateFee(ctx, tx, orgID, invoice.ID)
			if err != nil {
				return err
			}
			if has {
				return errNothingToBill
			}
			lease, err := s.repo.BillingLease(ctx, tx, orgID, invoice.LeaseID, rbac.Scope{AllProperties: true}, true)
			if errors.Is(err, sql.ErrNoRows) {
				return errNothingToBill
			}
			if err != nil {
				return err
			}
			_, err = s.applyLateFee(ctx, tx, orgID, lease.ID, invoice.ID, invoice.InvoiceNumber,
				amount, truncateDate(today), settings.CurrencyCode, 0)
			return err
		})
		if err != nil {
			if !errors.Is(err, errNothingToBill) && !errors.Is(err, httpx.ErrNotFound) {
				s.log.Error("billing sweep: late fee", "org_id", orgID, "invoice_id", candidate.ID, "error", err)
			}
			continue
		}
		charged++
	}
	if charged > 0 {
		s.log.Info("billing sweep: late fees assessed", "org_id", orgID, "count", charged)
	}
}

// rentPeriod is one billing month the sweep has to account for.
type rentPeriod struct {
	Start time.Time
	End   time.Time
	Due   time.Time
	// First marks the lease's opening period, the one that bills the deposit.
	First bool
}

// missingPeriods is pure date maths: which rent periods still have to be
// invoiced, oldest first. A period is raised once today is within leadDays of
// its start, so an invoice appears as a draft before the tenant owes anything.
func missingPeriods(start, end, lastBilled, today time.Time, billingDay, dueDay, leadDays, limit int) []rentPeriod {
	if start.IsZero() || limit <= 0 {
		return nil
	}
	if leadDays < 0 {
		leadDays = 0
	}
	start = truncateDate(start)
	end = truncateDate(end)
	today = truncateDate(today)
	lastBilled = truncateDate(lastBilled)

	first := firstRentPeriod(start, billingDay)
	year, month := start.Year(), start.Month()
	if !lastBilled.IsZero() {
		year, month = addMonth(lastBilled.Year(), lastBilled.Month(), 1)
	}

	periods := make([]rentPeriod, 0, limit)
	// Twelve iterations per requested period bounds the loop for a lease whose
	// billing day has not arrived for months, without ever running long.
	for guard := 0; guard < 12*(limit+1) && len(periods) < limit; guard++ {
		periodStart := dayOfMonth(year, month, billingDay)
		if periodStart.Before(start) {
			year, month = addMonth(year, month, 1)
			continue
		}
		if !end.IsZero() && periodStart.After(end) {
			break
		}
		if periodStart.After(today.AddDate(0, 0, leadDays)) {
			break
		}
		nextYear, nextMonth := addMonth(year, month, 1)
		periodEnd := dayOfMonth(nextYear, nextMonth, billingDay).AddDate(0, 0, -1)
		periods = append(periods, rentPeriod{
			Start: periodStart,
			End:   periodEnd,
			Due:   dueDateFor(periodStart, dueDay),
			First: periodStart.Equal(first),
		})
		year, month = nextYear, nextMonth
	}
	return periods
}

// firstRentPeriod is the period a lease opens on: the billing day of the month
// it starts, or the following month when the day has already passed.
func firstRentPeriod(start time.Time, billingDay int) time.Time {
	candidate := dayOfMonth(start.Year(), start.Month(), billingDay)
	if candidate.Before(start) {
		year, month := addMonth(start.Year(), start.Month(), 1)
		return dayOfMonth(year, month, billingDay)
	}
	return candidate
}

// dueDateFor keeps rent due in the month it is billed, and slips to the next
// month when the due day falls before the period starts.
func dueDateFor(periodStart time.Time, dueDay int) time.Time {
	due := dayOfMonth(periodStart.Year(), periodStart.Month(), dueDay)
	if due.Before(periodStart) {
		year, month := addMonth(periodStart.Year(), periodStart.Month(), 1)
		due = dayOfMonth(year, month, dueDay)
	}
	return due
}

// dayOfMonth clamps to the 28th so every month has the same billing day and a
// period boundary cannot land on a day that does not exist.
func dayOfMonth(year int, month time.Month, day int) time.Time {
	if day < 1 {
		day = 1
	}
	if day > maxDayOfMonth {
		day = maxDayOfMonth
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func addMonth(year int, month time.Month, n int) (int, time.Month) {
	t := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).AddDate(0, n, 0)
	return t.Year(), t.Month()
}

func nullTimeValue(value sql.NullTime) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}
