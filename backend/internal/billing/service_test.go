package billing

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

func day(year int, month time.Month, d int) time.Time {
	return time.Date(year, month, d, 0, 0, 0, 0, time.UTC)
}

func validationFields(t *testing.T, err error) map[string]string {
	t.Helper()
	if err == nil {
		t.Fatal("expected a validation error")
	}
	var e *httpx.Err
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an *httpx.Err", err)
	}
	if e.Code != "validation_failed" {
		t.Fatalf("code = %q, want validation_failed (%v)", e.Code, err)
	}
	return e.Fields
}

func TestMissingPeriods(t *testing.T) {
	t.Run("raises every unpaid month", func(t *testing.T) {
		periods := missingPeriods(day(2026, 1, 1), time.Time{}, time.Time{},
			day(2026, 3, 15), 1, 5, 5, 3)
		if len(periods) != 3 {
			t.Fatalf("got %d periods, want 3: %+v", len(periods), periods)
		}
		want := []string{"2026-01-01", "2026-02-01", "2026-03-01"}
		for i, p := range periods {
			if got := p.Start.Format(dateLayout); got != want[i] {
				t.Fatalf("period %d start = %s, want %s", i, got, want[i])
			}
		}
		if periods[0].End.Format(dateLayout) != "2026-01-31" {
			t.Fatalf("January period ends %s", periods[0].End.Format(dateLayout))
		}
		if periods[0].Due.Format(dateLayout) != "2026-01-05" {
			t.Fatalf("January due = %s", periods[0].Due.Format(dateLayout))
		}
		if !periods[0].First || periods[1].First {
			t.Fatal("only the opening period should be flagged for the deposit")
		}
	})

	t.Run("caps the backlog", func(t *testing.T) {
		periods := missingPeriods(day(2026, 1, 1), time.Time{}, time.Time{},
			day(2026, 6, 1), 1, 5, 5, 2)
		if len(periods) != 2 {
			t.Fatalf("got %d periods, want the 2 oldest", len(periods))
		}
		if periods[1].Start.Format(dateLayout) != "2026-02-01" {
			t.Fatalf("second period = %s", periods[1].Start.Format(dateLayout))
		}
	})

	t.Run("waits for the lead time", func(t *testing.T) {
		periods := missingPeriods(day(2026, 6, 1), time.Time{}, day(2026, 6, 1),
			day(2026, 6, 28), 1, 5, 5, 3)
		if len(periods) != 1 || periods[0].Start.Format(dateLayout) != "2026-07-01" {
			t.Fatalf("periods = %+v, want only July inside the 5 day lead", periods)
		}
		if periods[0].First {
			t.Fatal("July is not the opening period of a lease billed since June")
		}
	})

	t.Run("stops at the lease end", func(t *testing.T) {
		periods := missingPeriods(day(2026, 1, 1), day(2026, 2, 15), time.Time{},
			day(2026, 6, 1), 1, 5, 5, 12)
		if len(periods) != 2 {
			t.Fatalf("got %d periods, want January and February: %+v", len(periods), periods)
		}
	})

	t.Run("resumes after the last billed period", func(t *testing.T) {
		periods := missingPeriods(day(2026, 1, 1), time.Time{}, day(2026, 3, 1),
			day(2026, 4, 1), 1, 5, 5, 3)
		if len(periods) != 1 || periods[0].Start.Format(dateLayout) != "2026-04-01" {
			t.Fatalf("periods = %+v, want April only", periods)
		}
	})

	t.Run("opens on the first billing day at or after the start", func(t *testing.T) {
		periods := missingPeriods(day(2026, 1, 20), time.Time{}, time.Time{},
			day(2026, 3, 1), 5, 10, 5, 3)
		if len(periods) == 0 {
			t.Fatal("expected periods")
		}
		if got := periods[0].Start.Format(dateLayout); got != "2026-02-05" {
			t.Fatalf("first period = %s, want 2026-02-05", got)
		}
		if !periods[0].First {
			t.Fatal("2026-02-05 is the opening period")
		}
		if got := periods[0].Due.Format(dateLayout); got != "2026-02-10" {
			t.Fatalf("due = %s, want the due day inside the period", got)
		}
		if got := periods[0].End.Format(dateLayout); got != "2026-03-04" {
			t.Fatalf("period end = %s, want the day before the next billing day", got)
		}
	})

	t.Run("nothing to bill", func(t *testing.T) {
		if periods := missingPeriods(time.Time{}, time.Time{}, time.Time{},
			day(2026, 3, 1), 1, 5, 5, 3); periods != nil {
			t.Fatalf("a lease with no start date should raise nothing, got %+v", periods)
		}
		if periods := missingPeriods(day(2026, 1, 1), time.Time{}, time.Time{},
			day(2026, 3, 1), 1, 5, 5, 0); len(periods) != 0 {
			t.Fatalf("limit 0 must raise nothing, got %+v", periods)
		}
	})
}

func TestRentPeriodDateMaths(t *testing.T) {
	t.Run("billing days clamp to the 28th", func(t *testing.T) {
		if got := dayOfMonth(2026, time.February, 31).Format(dateLayout); got != "2026-02-28" {
			t.Fatalf("clamped day = %s", got)
		}
		if got := dayOfMonth(2026, time.March, 0).Format(dateLayout); got != "2026-03-01" {
			t.Fatalf("day 0 = %s, want the 1st", got)
		}
	})

	t.Run("periods stay whole months", func(t *testing.T) {
		periods := missingPeriods(day(2026, 1, 1), time.Time{}, time.Time{},
			day(2026, 2, 20), 1, 5, 5, 3)
		if len(periods) != 2 {
			t.Fatalf("periods = %+v, want January and February", periods)
		}
		if periods[1].End.Format(dateLayout) != "2026-02-28" {
			t.Fatalf("February period ends %s", periods[1].End.Format(dateLayout))
		}
	})

	t.Run("a due day before the period slips a month", func(t *testing.T) {
		if got := dueDateFor(day(2026, 2, 20), 5).Format(dateLayout); got != "2026-03-05" {
			t.Fatalf("due = %s, want the next month's due day", got)
		}
		if got := dueDateFor(day(2026, 2, 5), 5).Format(dateLayout); got != "2026-02-05" {
			t.Fatalf("due = %s, want the period's own due day", got)
		}
		if got := firstRentPeriod(day(2026, 1, 20), 5).Format(dateLayout); got != "2026-02-05" {
			t.Fatalf("first period = %s, want the following billing day", got)
		}
		if got := firstRentPeriod(day(2026, 1, 3), 5).Format(dateLayout); got != "2026-01-05" {
			t.Fatalf("first period = %s, want the start month's billing day", got)
		}
	})

	t.Run("addMonth crosses a year", func(t *testing.T) {
		year, month := addMonth(2026, time.December, 1)
		if year != 2027 || month != time.January {
			t.Fatalf("addMonth = %d/%s", year, month)
		}
	})
}

func TestPlanAllocations(t *testing.T) {
	open := []InvoiceBalance{
		{ID: 1, Status: statusDraft, Balance: 500},
		{ID: 2, Status: statusVoid, Balance: 500},
		{ID: 3, Status: statusIssued, Balance: 0},
		{ID: 4, Status: statusOverdue, Balance: 100},
		{ID: 5, Status: statusIssued, Balance: 50.5},
	}
	plans, credit := planAllocations(120, open)
	if len(plans) != 2 || plans[0].InvoiceID != 4 || plans[1].InvoiceID != 5 {
		t.Fatalf("plans = %+v, want invoice 4 then 5", plans)
	}
	if plans[0].Amount != 100 || plans[1].Amount != 20 {
		t.Fatalf("amounts = %+v, want 100 then 20", plans)
	}
	if credit != 0 {
		t.Fatalf("credit = %v, want the money fully spent", credit)
	}

	plans, credit = planAllocations(200, open)
	if len(plans) != 2 || plans[1].Amount != 50.5 {
		t.Fatalf("plans = %+v", plans)
	}
	if credit != 49.5 {
		t.Fatalf("credit = %v, want the remainder held as tenant credit", credit)
	}

	plans, credit = planAllocations(0, open)
	if len(plans) != 0 || credit != 0 {
		t.Fatalf("a zero payment planned %+v with credit %v", plans, credit)
	}

	plans, credit = planAllocations(99.999, open)
	if credit != 0 || len(plans) != 1 || plans[0].Amount != 100 {
		t.Fatalf("a 99.999 payment is rounded to cents before planning: %+v / %v", plans, credit)
	}
}

func TestSplitPreTax(t *testing.T) {
	preTax, tax := splitPreTax(1000, 160, 580)
	if preTax != 500 || tax != 80 {
		t.Fatalf("split = %v / %v, want 500 / 80", preTax, tax)
	}
	if preTax+tax != 580 {
		t.Fatal("a split must add back to the amount")
	}

	preTax, tax = splitPreTax(0, 0, 100)
	if preTax != 100 || tax != 0 {
		t.Fatalf("an untaxed invoice split = %v / %v", preTax, tax)
	}

	preTax, tax = splitPreTax(0, 100, 50)
	if preTax != 0 || tax != 50 {
		t.Fatalf("all-net split = %v / %v, want the tax side absorbing the amount", preTax, tax)
	}

	preTax, tax = splitPreTax(1000, 0, 250)
	if preTax != 250 || tax != 0 {
		t.Fatalf("gross split = %v / %v", preTax, tax)
	}
}

func TestJournalsBalance(t *testing.T) {
	rent := []InvoiceItemRow{{ItemType: itemRent, Amount: 45000}}
	if !journalBalanced(issueLines(rent, 0, 45000, "Issue")) {
		t.Fatalf("rent issue journal does not balance: %+v", issueLines(rent, 0, 45000, "Issue"))
	}

	mixed := []InvoiceItemRow{
		{ItemType: itemRent, Amount: 45000},
		{ItemType: itemElectricity, Amount: 1000},
		{ItemType: itemDeposit, Amount: 90000},
	}
	lines := issueLines(mixed, 160, 136160, "Issue")
	if !journalBalanced(lines) {
		t.Fatalf("mixed issue journal does not balance: %+v", lines)
	}
	if journalDebits(lines) != 136160 {
		t.Fatalf("debits = %v", journalDebits(lines))
	}

	for _, set := range [][]journalLine{
		paymentLines(1000, 800, 200, "Payment"),
		reversalLines(1000, 800, 200, "Reversal"),
		applicationLines(200, "Credit applied"),
		lateFeeLines(500, "Late fee"),
		creditNoteLines(580, 500, 80, "Credit note"),
		mirror(paymentLines(1000, 800, 200, "Payment")),
	} {
		if !journalBalanced(set) {
			t.Fatalf("journal does not balance: %+v", set)
		}
	}

	if journalBalanced([]journalLine{debitLine(accountCash, 100, "one side only")}) {
		t.Fatal("a single-sided journal must be rejected")
	}
}

func TestIncomeAccounts(t *testing.T) {
	cases := map[string]string{
		itemRent:        accountRentIncome,
		itemWater:       accountUtilityIncome,
		itemElectricity: accountUtilityIncome,
		itemGarbage:     accountUtilityIncome,
		itemLateFee:     accountLateFeeIncome,
		itemDeposit:     accountDepositLiability,
		itemMaintenance: accountMaintenanceExpense,
		itemService:     accountOtherIncome,
		itemAdjustment:  accountOtherIncome,
	}
	for itemType, want := range cases {
		if got := incomeAccountFor(itemType); got != want {
			t.Fatalf("%s posts to %s, want %s", itemType, got, want)
		}
	}
}

func TestInvoiceStatusFor(t *testing.T) {
	today := day(2026, 3, 15)
	past := day(2026, 3, 1)
	future := day(2026, 4, 1)

	cases := []struct {
		name        string
		status      string
		total, paid float64
		dueDate     time.Time
		want        string
	}{
		{"draft stays", statusDraft, 100, 0, past, statusDraft},
		{"void stays", statusVoid, 100, 0, past, statusVoid},
		{"paid", statusIssued, 100, 100, future, statusPaid},
		{"paid within tolerance", statusIssued, 100, 99.996, future, statusPaid},
		{"partially paid", statusIssued, 100, 40, past, statusPartiallyPaid},
		{"overdue", statusIssued, 100, 0, past, statusOverdue},
		{"still issued", statusIssued, 100, 0, future, statusIssued},
		{"repaired", statusOverdue, 100, 100, past, statusPaid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := invoiceStatusFor(tc.status, tc.total, tc.paid, tc.dueDate, today); got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeInvoiceQuery(t *testing.T) {
	tenant := rbac.Scope{OrgID: 1, TenantID: func() *uint64 { id := uint64(7); return &id }()}

	t.Run("normalizes and accepts", func(t *testing.T) {
		q := InvoiceQuery{Search: " KD-01 ", Status: "ISSUED", Type: "Rent",
			DueFrom: day(2026, 1, 1), DueTo: day(2026, 3, 1)}
		if err := normalizeInvoiceQuery(&q, rbac.Scope{OrgID: 1, AllProperties: true}); err != nil {
			t.Fatal(err)
		}
		if q.Search != "KD-01" || q.Status != statusIssued || q.Type != typeRent {
			t.Fatalf("query = %+v", q)
		}
	})

	t.Run("rejects unknown labels", func(t *testing.T) {
		q := InvoiceQuery{Status: "unpaid", Type: "rates"}
		err := normalizeInvoiceQuery(&q, rbac.Scope{OrgID: 1, AllProperties: true})
		fields := validationFields(t, err)
		if fields["status"] == "" || fields["invoice_type"] == "" {
			t.Fatalf("fields = %v", fields)
		}
	})

	t.Run("rejects an inverted due range", func(t *testing.T) {
		q := InvoiceQuery{DueFrom: day(2026, 3, 1), DueTo: day(2026, 1, 1)}
		fields := validationFields(t, normalizeInvoiceQuery(&q, rbac.Scope{AllProperties: true}))
		if fields["due_from"] == "" {
			t.Fatalf("fields = %v", fields)
		}
	})

	t.Run("a tenant cannot ask for drafts", func(t *testing.T) {
		q := InvoiceQuery{Status: statusDraft}
		fields := validationFields(t, normalizeInvoiceQuery(&q, tenant))
		if fields["status"] == "" {
			t.Fatalf("fields = %v", fields)
		}
	})

	t.Run("rejects an oversized search", func(t *testing.T) {
		q := InvoiceQuery{Search: strings.Repeat("a", maxSearchLen+1)}
		fields := validationFields(t, normalizeInvoiceQuery(&q, rbac.Scope{AllProperties: true}))
		if fields["q"] == "" {
			t.Fatalf("fields = %v", fields)
		}
	})
}

func TestBuildInvoiceCreate(t *testing.T) {
	t.Run("prices the lines and defaults the dates", func(t *testing.T) {
		fields := map[string]string{}
		row, items := buildInvoiceCreate(9, 3, CreateInvoiceInput{
			LeaseID: 7, BillingPeriodStart: "2026-01-01", BillingPeriodEnd: "2026-01-31",
			Notes: " January ",
			Items: []ItemInput{
				{ItemType: "Rent", Description: " Rent ", UnitPrice: 45000},
				{ItemType: itemElectricity, Description: "Power", Quantity: ptrFloat(250),
					UnitPrice: 4, TaxRate: ptrFloat(16)},
			},
		}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected fields %v", fields)
		}
		if row.OrganizationID != 9 || row.LeaseID != 7 || row.Status != statusDraft {
			t.Fatalf("row = %+v", row)
		}
		if row.InvoiceType != typeRent {
			t.Fatalf("type = %q, an omitted invoice_type is rent", row.InvoiceType)
		}
		if !row.CreatedBy.Valid || row.CreatedBy.Int64 != 3 {
			t.Fatalf("created_by = %+v", row.CreatedBy)
		}
		if row.IssueDate.Format(dateLayout) != "2026-01-01" || row.DueDate.Format(dateLayout) != "2026-01-31" {
			t.Fatalf("dates default to the period: %v / %v", row.IssueDate, row.DueDate)
		}
		if items[0].Quantity != 1 || items[0].Amount != 45000 || items[0].Description != "Rent" {
			t.Fatalf("rent line = %+v", items[0])
		}
		if items[1].Amount != 1000 {
			t.Fatalf("utility line = %+v, want 250 x 4", items[1])
		}
		if row.Subtotal != 46000 || row.TaxTotal != 160 || row.Total != 46160 {
			t.Fatalf("totals = %v / %v / %v", row.Subtotal, row.TaxTotal, row.Total)
		}
		if row.AmountPaid != 0 || row.Balance != 0 {
			t.Fatalf("a created invoice carries no money: %+v", row)
		}
	})

	t.Run("rejects missing and impossible values", func(t *testing.T) {
		issue := "2026-02-01"
		due := "2026-01-01"
		fields := map[string]string{}
		buildInvoiceCreate(9, 3, CreateInvoiceInput{
			IssueDate: &issue, DueDate: &due,
			Items: []ItemInput{{ItemType: "rent", Description: "x", UnitPrice: 10, TaxRate: ptrFloat(120)}},
		}, fields)
		for _, want := range []string{"lease_id", "billing_period_start", "billing_period_end", "items"} {
			if fields[want] == "" {
				t.Fatalf("missing validation for %q in %v", want, fields)
			}
		}
	})

	t.Run("a client cannot send a total", func(t *testing.T) {
		body := `{"lease_id":1,"invoice_type":"rent","billing_period_start":"2026-01-01",` +
			`"billing_period_end":"2026-01-31","items":[{"item_type":"rent","description":"Rent",` +
			`"unit_price":10,"amount":999999}]}`
		var in CreateInvoiceInput
		decoder := json.NewDecoder(strings.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&in); err == nil {
			t.Fatal("an item amount must not be accepted from a client")
		}
	})
}

func TestCleanNumberAndPrefix(t *testing.T) {
	t.Run("invoice numbers stay inert", func(t *testing.T) {
		fields := map[string]string{}
		if got := cleanNumber(" KD-L/2026_01.2 ", "invoice_number", fields); got != "KD-L/2026_01.2" {
			t.Fatalf("number = %q", got)
		}
		if len(fields) != 0 {
			t.Fatalf("fields = %v", fields)
		}
		cleanNumber(`INV'; DROP TABLE invoices; --`, "invoice_number", fields)
		if fields["invoice_number"] == "" {
			t.Fatal("a number with operators must be rejected")
		}
		cleanNumber(strings.Repeat("A", maxNumberLen+1), "invoice_number", fields)
		if fields["invoice_number"] == "" {
			t.Fatal("an oversized number must be rejected")
		}
	})

	t.Run("a configured prefix cannot hold wildcards", func(t *testing.T) {
		for _, bad := range []string{"IN V", "INV%", "INV_", "!", strings.Repeat("A", 21), ""} {
			if got := sanitizePrefix(bad); got != "" {
				t.Fatalf("sanitizePrefix(%q) = %q, want it dropped", bad, got)
			}
		}
		if got := sanitizePrefix(" inv"); got != "INV" {
			t.Fatalf("sanitizePrefix = %q", got)
		}
	})

	t.Run("like patterns escape", func(t *testing.T) {
		if got := likePattern(`100%_`); got != `%100\%\_%` {
			t.Fatalf("likePattern = %q, want the wildcards escaped", got)
		}
		if got := likePattern(`a\b`); got != `%a\\b%` {
			t.Fatalf("likePattern = %q, want the backslash escaped first", got)
		}
		if escapeLike("a_b%c") != `a!_b!%c` {
			t.Fatalf("escapeLike = %q", escapeLike("a_b%c"))
		}
		if escapeLike("a!b") != `a!!b` {
			t.Fatal("escapeLike must double the escape character")
		}
	})

	t.Run("number prefix namespaces per property", func(t *testing.T) {
		if got := numberPrefix("INV", "KD"); got != "INV-KD" {
			t.Fatalf("numberPrefix = %q", got)
		}
		if got := numberPrefix("INV", "  "); got != "INV" {
			t.Fatalf("numberPrefix without a code = %q", got)
		}
	})
}

func TestLateFeeAmount(t *testing.T) {
	if got := lateFeeAmount("fixed", 500.004, 45000); got != 500 {
		t.Fatalf("fixed fee = %v", got)
	}
	if got := lateFeeAmount("PERCENT", 5, 45000); got != 2250 {
		t.Fatalf("percent fee = %v", got)
	}
	if got := lateFeeAmount("", 5, 0); got != 0 {
		t.Fatalf("a zero rent cannot carry a percentage fee, got %v", got)
	}
}

func TestBuildPaymentAndAllocations(t *testing.T) {
	t.Run("prices and stamps the receipt", func(t *testing.T) {
		reference := "  MPESA-ABC "
		row, fields := buildPayment(9, 3, PaymentInput{
			LeaseID: 4, PaymentMethodID: 2, Amount: 1000, Reference: &reference,
		})
		if len(fields) != 0 {
			t.Fatalf("fields = %v", fields)
		}
		if row.Status != paymentCompleted || row.Amount != 1000 {
			t.Fatalf("row = %+v", row)
		}
		if !row.Reference.Valid || row.Reference.String != "MPESA-ABC" {
			t.Fatalf("reference = %+v", row.Reference)
		}
		if !row.ReceivedBy.Valid || row.ReceivedBy.Int64 != 3 {
			t.Fatalf("received_by = %+v", row.ReceivedBy)
		}
		if row.PaidAt.IsZero() {
			t.Fatal("paid_at must default to now")
		}
		if roundToCents(1.004) != 1 || roundToCents(1.006) != 1.01 {
			t.Fatalf("money must round to cents: %v / %v", roundToCents(1.004), roundToCents(1.006))
		}
	})

	t.Run("rejects money that cannot exist", func(t *testing.T) {
		long := strings.Repeat("r", maxReferenceLen+1)
		_, fields := buildPayment(9, 3, PaymentInput{Reference: &long, Amount: -1})
		if fields["lease_id"] == "" || fields["payment_method_id"] == "" ||
			fields["amount"] == "" || fields["reference"] == "" {
			t.Fatalf("fields = %v", fields)
		}
	})

	t.Run("allocations cannot exceed the money received", func(t *testing.T) {
		over := []AllocationInput{{InvoiceID: 1, Amount: 600}, {InvoiceID: 2, Amount: 500}}
		fields := validateAllocations(&over, 1000, map[string]string{})
		if fields["allocations"] == "" {
			t.Fatalf("fields = %v", fields)
		}

		exact := []AllocationInput{{InvoiceID: 1, Amount: 600}, {InvoiceID: 2, Amount: 400}}
		if fields = validateAllocations(&exact, 1000, map[string]string{}); len(fields) != 0 {
			t.Fatalf("an exact split should be fine, got %v", fields)
		}

		if fields = validateAllocations(&[]AllocationInput{}, 1000, map[string]string{}); fields["allocations"] == "" {
			t.Fatal("an empty allocation list must be rejected rather than treated as auto")
		}
		if fields = validateAllocations(nil, 1000, map[string]string{}); len(fields) != 0 {
			t.Fatalf("omitted allocations should be fine, got %v", fields)
		}
	})
}

func TestMergeSettings(t *testing.T) {
	global := func(key, value string) settingRow {
		return settingRow{Key: key, Value: sql.NullString{String: value, Valid: true}}
	}
	org := func(key, value string) settingRow {
		return settingRow{Key: key, Value: sql.NullString{String: value, Valid: true},
			OrganizationID: sql.NullInt64{Int64: 7, Valid: true}}
	}

	t.Run("falls back to the defaults", func(t *testing.T) {
		settings := mergeSettings(nil, 7)
		if settings.InvoicePrefix != defaultInvoicePrefix || settings.CurrencyCode != defaultCurrency {
			t.Fatalf("settings = %+v", settings)
		}
		if settings.LateFeeGraceDays != defaultGraceDays || settings.RentDueDay != defaultRentDueDay {
			t.Fatalf("settings = %+v", settings)
		}
	})

	t.Run("an organization row wins over the platform row", func(t *testing.T) {
		settings := mergeSettings([]settingRow{
			global("invoice_prefix", "INV"), global("currency_code", "usd"),
			global("late_fee_type", "percent"), global("late_fee_value", "5"),
			global("rent_due_day", "5"), global("vat_rate", "16"),
			org("invoice_prefix", "glee"), org("currency_code", "KES"),
			org("late_fee_type", "fixed"), org("late_fee_value", "500"),
			org("rent_due_day", "10"), org("vat_rate", "0"),
			org("invoice_prefix", "nope%"),
		}, 7)
		if settings.InvoicePrefix != "GLEE" {
			t.Fatalf("prefix = %q, an invalid org value must not override", settings.InvoicePrefix)
		}
		if settings.CurrencyCode != "KES" || settings.LateFeeType != "fixed" || settings.LateFeeValue != 500 {
			t.Fatalf("settings = %+v", settings)
		}
		if settings.RentDueDay != 10 || settings.VATRate != 0 {
			t.Fatalf("settings = %+v", settings)
		}
	})

	t.Run("another organization is ignored", func(t *testing.T) {
		other := settingRow{Key: "currency_code",
			Value:          sql.NullString{String: "UGX", Valid: true},
			OrganizationID: sql.NullInt64{Int64: 8, Valid: true}}
		if settings := mergeSettings([]settingRow{other}, 7); settings.CurrencyCode != defaultCurrency {
			t.Fatalf("settings = %+v", settings)
		}
	})
}

func TestNormalizePaymentQuery(t *testing.T) {
	q := PaymentQuery{Status: "COMPLETED", Search: "abc"}
	if err := normalizePaymentQuery(&q); err != nil {
		t.Fatal(err)
	}
	if q.Status != paymentCompleted {
		t.Fatalf("status = %q", q.Status)
	}

	// The filter speaks the column's whole vocabulary, including the two gateway
	// statuses billing does not write yet.
	for _, status := range []string{paymentPending, paymentCompleted, paymentFailed, paymentReversed} {
		q := PaymentQuery{Status: status}
		if err := normalizePaymentQuery(&q); err != nil {
			t.Fatalf("status %q: %v", status, err)
		}
	}

	fields := validationFields(t, normalizePaymentQuery(&PaymentQuery{
		Status: "settled", From: day(2026, 3, 1), To: day(2026, 1, 1)}))
	if fields["status"] == "" || fields["from"] == "" {
		t.Fatalf("fields = %v", fields)
	}
	if !strings.Contains(fields["status"], paymentPending) {
		t.Fatalf("fields = %v, the refusal has to name the statuses a caller may send", fields)
	}
}

// TestPaymentDateProblem is the boundary a receipt date may not cross: money
// cannot have arrived tomorrow, and a paid_at in 1905 is a mistyped year, not
// history. Both would otherwise move the journal period, the aging report and
// the oldest-first credit order along with them.
func TestPaymentDateProblem(t *testing.T) {
	today := day(2026, 10, 24)

	if problem := paymentDateProblem(day(2026, 10, 24), today); problem != "" {
		t.Fatalf("today = %q, the rent paid this morning is a real receipt", problem)
	}
	if problem := paymentDateProblem(day(2026, 10, 23), today); problem != "" {
		t.Fatalf("yesterday = %q", problem)
	}
	// A timestamp from later this afternoon is still today's money.
	afternoon := time.Date(2026, 10, 24, 15, 45, 0, 0, time.UTC)
	if problem := paymentDateProblem(afternoon, today); problem != "" {
		t.Fatalf("this afternoon = %q", problem)
	}

	if problem := paymentDateProblem(day(2026, 10, 25), today); !strings.Contains(problem, "future") {
		t.Fatalf("tomorrow = %q", problem)
	}
	if problem := paymentDateProblem(day(2027, 4, 1), today); !strings.Contains(problem, "future") {
		t.Fatalf("next year = %q", problem)
	}
	if problem := paymentDateProblem(day(1999, 12, 31), today); !strings.Contains(problem, "2000") {
		t.Fatalf("before the books begin = %q", problem)
	}
	if problem := paymentDateProblem(day(1905, 6, 1), today); problem == "" {
		t.Fatal("a mistyped year must be refused, not silently booked 120 years back")
	}
}

func TestScopeFilterAndTenantOnly(t *testing.T) {
	tenantID := uint64(7)
	tenantScope := rbac.Scope{OrgID: 1, TenantID: &tenantID}
	if !tenantOnly(tenantScope) {
		t.Fatal("a scope with only a tenant id is tenant-only")
	}
	if tenantOnly(rbac.Scope{OrgID: 1, AllProperties: true, TenantID: &tenantID}) {
		t.Fatal("a caretaker who also rents is not limited to their own lease")
	}

	clause, args := scopeFilter(rbac.Scope{OrgID: 1, PropertyIDs: []uint64{3, 4}}, "u.property_id", "l.id")
	if !strings.Contains(clause, "IN (") || len(args) != 2 {
		t.Fatalf("clause = %q args = %v", clause, args)
	}

	if clause, _ = scopeFilter(rbac.Scope{OrgID: 1}, "u.property_id", "l.id"); !strings.Contains(clause, "1=0") {
		t.Fatalf("an empty scope must match nothing, got %q", clause)
	}

	if clause, _ := scopeFilter(tenantScope, "u.property_id", "l.id"); !strings.Contains(clause, "lease_tenants") {
		t.Fatalf("a tenant scope must be limited to its own leases, got %q", clause)
	}
}

func ptrFloat(value float64) *float64 { return &value }
