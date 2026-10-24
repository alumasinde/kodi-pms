package utilities

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

func day(year int, month time.Month, d int) time.Time {
	return time.Date(year, month, d, 0, 0, 0, 0, time.UTC)
}

func ptrFloat(value float64) *float64 { return &value }

func ptrUint(value uint64) *uint64 { return &value }

func ptrString(value string) *string { return &value }

func newTestService() *Service {
	return NewService(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
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

func conflictMessage(t *testing.T, err error) string {
	t.Helper()
	var e *httpx.Err
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an *httpx.Err", err)
	}
	if e.Code != "conflict" || e.Status != 409 {
		t.Fatalf("err = %+v, want a 409 conflict", e)
	}
	return e.Message
}

func TestAuthorize(t *testing.T) {
	viewer := &rbac.Principal{UserID: 3, OrgID: 1,
		Perms: map[string]struct{}{"utilities.view": {}}}
	if err := authorize(viewer, "utilities.view"); err != nil {
		t.Fatalf("err = %v, want the granted permission to pass", err)
	}
	if err := authorize(viewer, "utilities.create"); !errors.Is(err, httpx.ErrForbidden) {
		t.Fatalf("err = %v, want forbidden for a permission the caller does not hold", err)
	}
	if err := authorize(&rbac.Principal{}, "utilities.view"); !errors.Is(err, httpx.ErrUnauthorized) {
		t.Fatalf("err = %v, want unauthorized without an organization", err)
	}
	if err := authorize(nil, "utilities.view"); !errors.Is(err, httpx.ErrUnauthorized) {
		t.Fatalf("err = %v, want unauthorized without a principal", err)
	}
}

func TestReadingValueAndRounding(t *testing.T) {
	t.Run("a dial is stored at three decimals", func(t *testing.T) {
		fields := map[string]string{}
		if got := readingValue("current_reading", 12.34567, fields); got != 12.346 {
			t.Fatalf("got %v, want 12.346", got)
		}
		if len(fields) > 0 {
			t.Fatalf("fields = %v, a readable figure is not a validation failure", fields)
		}
	})

	t.Run("refuses the figures the schema cannot hold", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			value float64
			want  string
		}{
			{"negative", -0.001, "cannot be negative"},
			{"above the column", maxReadingValue + 1, "must be at most"},
		} {
			fields := map[string]string{}
			if got := readingValue("previous_reading", tc.value, fields); !strings.Contains(fields["previous_reading"], tc.want) {
				t.Fatalf("%s: fields = %v, want a message containing %q (value %v)", tc.name, fields, tc.want, got)
			}
		}
	})

	t.Run("money rounds to cents and a broken figure becomes zero", func(t *testing.T) {
		if got := roundToCents(1234.567); got != 1234.57 {
			t.Fatalf("roundToCents = %v, want 1234.57", got)
		}
		if got := roundToCents(0.005); got != 0.01 {
			t.Fatalf("roundToCents(0.005) = %v", got)
		}
		for _, bad := range []float64{math.NaN(), math.Inf(-1), math.Inf(1)} {
			if got := roundToCents(bad); got != 0 {
				t.Fatalf("roundToCents(%v) = %v, want 0 rather than a value the column cannot store", bad, got)
			}
			if got := roundToThree(bad); got != 0 {
				t.Fatalf("roundToThree(%v) = %v, want 0", bad, got)
			}
		}
		if got := roundToThree(12.3456); got != 12.346 {
			t.Fatalf("roundToThree = %v", got)
		}
	})
}

func TestDateParsing(t *testing.T) {
	t.Run("both accepted layouts land on the business date", func(t *testing.T) {
		plain, ok := parseDate("2026-03-05")
		if !ok || plain.Format(dateLayout) != "2026-03-05" {
			t.Fatalf("plain = %v ok = %v", plain, ok)
		}
		stamped, ok := parseDate("2026-03-05T21:15:00Z")
		if !ok || stamped.Format(dateLayout) != "2026-03-05" {
			t.Fatalf("stamped = %v ok = %v", stamped, ok)
		}
		if _, ok = parseDate("05/03/2026"); ok {
			t.Fatal("a slashed date must not parse")
		}
		if _, ok = parseDate("  "); ok {
			t.Fatal("blank is absent, not a date")
		}
	})

	t.Run("an optional date stays silent when left out", func(t *testing.T) {
		fields := map[string]string{}
		if got := parseOptionalDateString("from", "   ", fields); !got.IsZero() {
			t.Fatalf("got %v, want the zero time", got)
		}
		parseOptionalDateString("to", "2026-13-01", fields)
		if fields["to"] == "" {
			t.Fatalf("fields = %v, an impossible month must be refused", fields)
		}

		required := map[string]string{}
		if got := parseRequiredDate("reading_date", "", required); !got.IsZero() || required["reading_date"] == "" {
			t.Fatalf("got %v fields %v, a missing required date must be named", got, required)
		}

		nullable := map[string]string{}
		if got := parseOptionalDate("date_due", nil, nullable); !got.IsZero() || len(nullable) > 0 {
			t.Fatalf("got %v fields %v, a null date is an omitted date", got, nullable)
		}
	})
}

func TestDueDateAfter(t *testing.T) {
	t.Run("a reading is collected on the rent day of the following month", func(t *testing.T) {
		cases := []struct {
			reading time.Time
			want    time.Time
			dueDay  int
		}{
			{day(2026, 3, 20), day(2026, 4, 7), 7},
			{day(2026, 12, 31), day(2027, 1, 5), 5},
			{day(2026, 1, 1), day(2026, 2, 28), 28},
		}
		for _, tc := range cases {
			got := dueDateAfter(tc.reading, tc.dueDay)
			if got.Format(dateLayout) != tc.want.Format(dateLayout) {
				t.Fatalf("reading %v due_day %d = %v, want %v",
					tc.reading.Format(dateLayout), tc.dueDay, got.Format(dateLayout), tc.want.Format(dateLayout))
			}
		}
	})

	t.Run("a due day outside the lease constraint falls back", func(t *testing.T) {
		for _, dueDay := range []int{0, -3, maxDueDay + 1, 999} {
			if got := dueDateAfter(day(2026, 6, 10), dueDay); got.Format(dateLayout) != "2026-07-05" {
				t.Fatalf("due_day %d produced %v, the default must keep the row inside chk_leases_due_day",
					dueDay, got.Format(dateLayout))
			}
		}
	})
}

func TestFutureAndOverdue(t *testing.T) {
	today := day(2026, 5, 10)
	if !onOrAfterFuture(day(2026, 5, 11), today) {
		t.Fatal("a reading dated tomorrow must be refused")
	}
	if onOrAfterFuture(today, today) {
		t.Fatal("today is billable; only the future is")
	}
	if onOrAfterFuture(today.Add(20*time.Hour), today) {
		t.Fatal("the clock on today must not read as the future")
	}

	cases := []struct {
		status string
		due    time.Time
		want   bool
	}{
		{billPending, day(2026, 5, 9), true},
		{billBilled, day(2026, 5, 9), true},
		{billPending, today, false},
		{billPending, day(2026, 6, 1), false},
		{billPaid, day(2026, 1, 1), false},
		{billVoid, day(2026, 1, 1), false},
	}
	for _, tc := range cases {
		if got := isOverdue(tc.status, tc.due, today); got != tc.want {
			t.Fatalf("isOverdue(%s, %v) = %v, want %v", tc.status, tc.due.Format(dateLayout), got, tc.want)
		}
	}
}

func TestOrganizationLocation(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("timezone database not installed on this machine")
	}
	// Berlin is not the fallback, so this fails if the argument is ignored. The
	// runtime may hand back a freshly built *Location for a name it has already
	// loaded, so the zones are compared by what the clock would read, not by
	// pointer.
	if got := orgLocation("Europe/Berlin"); got == nil || got.String() != berlin.String() {
		t.Fatalf("got %v, want the timezone asked for", got)
	}
	// A timezone that cannot be loaded must still give a usable clock: the
	// overdue test is only as correct as the location it falls back to.
	for _, bad := range []string{"", "   ", "Mars/Olympus"} {
		if got := orgLocation(bad); got == nil {
			t.Fatalf("orgLocation(%q) returned no location", bad)
		}
	}
	if got := orgLocation("Mars/Olympus").String(); got != "Africa/Nairobi" {
		t.Fatalf("fallback = %q, want the operating timezone", got)
	}
	if got := todayIn(time.UTC); got != truncateDate(time.Now().UTC()) {
		t.Fatalf("todayIn = %v, want today with no clock", got)
	}
}

func TestNormalizeMeterQuery(t *testing.T) {
	t.Run("clean filters pass and the sort defaults", func(t *testing.T) {
		q := MeterQuery{Search: "  KD-2 ", UtilityType: " WATER ", Status: "Active"}
		if err := normalizeMeterQuery(&q); err != nil {
			t.Fatal(err)
		}
		if q.Search != "KD-2" || q.UtilityType != "water" || q.Status != "active" {
			t.Fatalf("query = %+v", q)
		}
		if q.Sort != defaultMeterSort {
			t.Fatalf("sort = %q, want the default when the caller sent none", q.Sort)
		}
	})

	t.Run("refuses what would reach SQL as a bad predicate", func(t *testing.T) {
		q := MeterQuery{Search: strings.Repeat("x", maxSearch+1), UtilityType: "power", Status: "broken"}
		fields := validationFields(t, normalizeMeterQuery(&q))
		if fields["q"] == "" || fields["utility_type"] == "" || fields["status"] == "" {
			t.Fatalf("fields = %v", fields)
		}
		for _, search := range []string{"50%", "unit_1"} {
			q := MeterQuery{Search: search}
			if fields := validationFields(t, normalizeMeterQuery(&q)); !strings.Contains(fields["q"], "wildcard") {
				t.Fatalf("search %q: fields = %v", search, fields)
			}
		}
	})
}

func TestNormalizeBillQuery(t *testing.T) {
	q := BillQuery{Search: "water", Status: "pending", Type: "water",
		DueFrom: day(2026, 4, 1), DueTo: day(2026, 4, 30), Sort: "ub.amount ASC"}
	if err := normalizeBillQuery(&q); err != nil {
		t.Fatal(err)
	}

	q = BillQuery{Status: "settled", Type: "power", DueFrom: day(2026, 4, 30), DueTo: day(2026, 4, 1)}
	fields := validationFields(t, normalizeBillQuery(&q))
	if fields["status"] == "" || fields["type"] == "" || fields["due_to"] == "" {
		t.Fatalf("fields = %v", fields)
	}
	if q.Sort != defaultBillSort {
		t.Fatalf("sort = %q", q.Sort)
	}
}

func TestGenerateFilter(t *testing.T) {
	t.Run("carries the ids and parses both bounds", func(t *testing.T) {
		fields := map[string]string{}
		f := generateFilter(GenerateBillsInput{MeterID: 4, PropertyID: 2, UnitID: 9,
			From: "2026-01-01", To: "2026-03-31"}, fields)
		if f.MeterID != 4 || f.PropertyID != 2 || f.UnitID != 9 {
			t.Fatalf("filter = %+v", f)
		}
		if f.From.Format(dateLayout) != "2026-01-01" || f.To.Format(dateLayout) != "2026-03-31" {
			t.Fatalf("range = %v / %v", f.From, f.To)
		}
		if len(fields) > 0 {
			t.Fatalf("fields = %v", fields)
		}
	})

	t.Run("a date the query would compare to a string is refused first", func(t *testing.T) {
		fields := map[string]string{}
		generateFilter(GenerateBillsInput{From: "01-01-2026"}, fields)
		if fields["from"] == "" {
			t.Fatalf("fields = %v", fields)
		}

		fields = map[string]string{}
		generateFilter(GenerateBillsInput{From: "2026-05-01", To: "2026-02-01"}, fields)
		if fields["to"] == "" {
			t.Fatalf("fields = %v, an inverted range must not reach the sweep", fields)
		}
	})
}

func TestBuildBillCreate(t *testing.T) {
	t.Run("a hand-raised charge keeps what the caller sent", func(t *testing.T) {
		fields := map[string]string{}
		row := buildBillCreate(CreateBillInput{
			UnitID: 5, MeterID: 2, ReadingID: 3, Type: "WATER",
			Amount: ptrFloat(1234.567), DateDue: "2026-04-05",
			PeriodStart: "2026-03-01", PeriodEnd: "2026-03-31",
		}, fields)
		if len(fields) > 0 {
			t.Fatalf("fields = %v", fields)
		}
		if row.UnitID != 5 || row.MeterID.Int64 != 2 || row.ReadingID.Int64 != 3 {
			t.Fatalf("row = %+v", row)
		}
		if row.Type != "water" || row.Status != billPending {
			t.Fatalf("row = %+v, the type is lowercased and status is never a client field", row)
		}
		if row.Amount != 1234.57 {
			t.Fatalf("amount = %v, want the charge rounded to the column", row.Amount)
		}
		if row.DateDue.Format(dateLayout) != "2026-04-05" || row.PeriodStart.Format(dateLayout) != "2026-03-01" {
			t.Fatalf("dates = %+v", row)
		}
	})

	t.Run("an omitted amount stays zero so the reading can price it", func(t *testing.T) {
		fields := map[string]string{}
		row := buildBillCreate(CreateBillInput{UnitID: 5, ReadingID: 3}, fields)
		if len(fields) > 0 || row.Amount != 0 || row.Type != "" || !row.DateDue.IsZero() {
			t.Fatalf("row = %+v fields = %v", row, fields)
		}
		if row.MeterID.Valid || row.ReadingID.Int64 != 3 {
			t.Fatalf("row = %+v", row)
		}
	})

	t.Run("names every field it cannot use", func(t *testing.T) {
		fields := map[string]string{}
		buildBillCreate(CreateBillInput{
			Type: "power", DateDue: "05-04-2026", PeriodStart: "yesterday", Amount: ptrFloat(-1),
		}, fields)
		for _, name := range []string{"type", "date_due", "period_start"} {
			if fields[name] == "" {
				t.Fatalf("fields = %v, %s must be named", fields, name)
			}
		}
	})
}

func TestFinalizeBillRow(t *testing.T) {
	t.Run("a bill with nothing on it fails on every field", func(t *testing.T) {
		fields := map[string]string{}
		finalizeBillRow(&BillRow{}, fields)
		for _, name := range []string{"unit_id", "type", "amount", "date_due", "period_start", "period_end"} {
			if fields[name] == "" {
				t.Fatalf("fields = %v, %s is required on a bill", fields, name)
			}
		}
	})

	t.Run("the dates have to run in order", func(t *testing.T) {
		fields := map[string]string{}
		finalizeBillRow(&BillRow{UnitID: 5, Type: "water", Amount: 100,
			DateDue: day(2026, 4, 5), PeriodStart: day(2026, 4, 30), PeriodEnd: day(2026, 4, 1)}, fields)
		if fields["period_end"] == "" {
			t.Fatalf("fields = %v, a period that ends before it starts cannot be charged", fields)
		}

		fields = map[string]string{}
		finalizeBillRow(&BillRow{UnitID: 5, Type: "water", Amount: 100,
			DateDue: day(2026, 3, 1), PeriodStart: day(2026, 4, 1), PeriodEnd: day(2026, 4, 30)}, fields)
		if fields["date_due"] == "" {
			t.Fatalf("fields = %v, a charge due before the period it covers closed must be refused", fields)
		}

		fields = map[string]string{}
		finalizeBillRow(&BillRow{UnitID: 5, Type: "water", Amount: maxAmount + 1,
			DateDue: day(2026, 4, 5), PeriodStart: day(2026, 3, 1), PeriodEnd: day(2026, 3, 31)}, fields)
		if fields["amount"] == "" {
			t.Fatalf("fields = %v", fields)
		}
	})

	t.Run("a complete charge passes untouched", func(t *testing.T) {
		fields := map[string]string{}
		finalizeBillRow(&BillRow{UnitID: 5, Type: "water", Amount: 1500,
			DateDue: day(2026, 4, 5), PeriodStart: day(2026, 3, 1), PeriodEnd: day(2026, 3, 31)}, fields)
		if len(fields) > 0 {
			t.Fatalf("fields = %v", fields)
		}
	})
}

func TestApplyBillPatch(t *testing.T) {
	row := func() *BillRow {
		return &BillRow{ID: 9, UnitID: 5, MeterID: sql.NullInt64{Int64: 2, Valid: true},
			Type: "water", Amount: 1500,
			DateDue: day(2026, 4, 5), PeriodStart: day(2026, 3, 1), PeriodEnd: day(2026, 3, 31),
			Status: billPending}
	}

	t.Run("an empty patch changes nothing", func(t *testing.T) {
		fields := map[string]string{}
		oldValues, newValues := applyBillPatch(row(), UpdateBillInput{}, fields)
		if len(fields) > 0 || len(newValues) > 0 || len(oldValues) > 0 {
			t.Fatalf("fields = %v old = %v new = %v", fields, oldValues, newValues)
		}
	})

	t.Run("only the changed columns are recorded", func(t *testing.T) {
		before := row()
		fields := map[string]string{}
		oldValues, newValues := applyBillPatch(before, UpdateBillInput{
			Amount: ptrFloat(1600), Type: ptrString("electricity"), DateDue: ptrString("2026-04-10"),
		}, fields)
		if len(fields) > 0 {
			t.Fatalf("fields = %v", fields)
		}
		if newValues["amount"] != 1600.0 || oldValues["amount"] != 1500.0 {
			t.Fatalf("values = %v / %v", oldValues, newValues)
		}
		if newValues["type"] != "electricity" || newValues["date_due"] != "2026-04-10" {
			t.Fatalf("values = %v", newValues)
		}
		if _, touched := newValues["unit_id"]; touched {
			t.Fatalf("values = %v, an untouched field must not be audited", newValues)
		}
		if before.Amount != 1600 || before.Type != "electricity" || before.DateDue.Format(dateLayout) != "2026-04-10" {
			t.Fatalf("row = %+v, the patched row is what gets written", before)
		}
	})

	t.Run("a meter can be detached but a unit cannot", func(t *testing.T) {
		fields := map[string]string{}
		detached := row()
		_, newValues := applyBillPatch(detached, UpdateBillInput{MeterID: ptrUint(0)}, fields)
		if detached.MeterID.Valid {
			t.Fatal("meter_id 0 must drop the bill off the dial")
		}
		if v, ok := newValues["meter_id"]; !ok || v != nil {
			t.Fatalf("new = %v, the audit has to show the link going away", newValues)
		}

		fields = map[string]string{}
		if _, newValues := applyBillPatch(row(), UpdateBillInput{UnitID: ptrUint(0)}, fields); fields["unit_id"] == "" {
			t.Fatalf("fields = %v, a bill cannot be moved off every unit", newValues)
		}
	})

	t.Run("refuses a figure the column cannot hold", func(t *testing.T) {
		fields := map[string]string{}
		applyBillPatch(row(), UpdateBillInput{Amount: ptrFloat(-5)}, fields)
		if fields["amount"] == "" {
			t.Fatalf("fields = %v", fields)
		}
		fields = map[string]string{}
		applyBillPatch(row(), UpdateBillInput{Type: ptrString("power"), PeriodEnd: ptrString("2026-02-01")}, fields)
		if fields["type"] == "" || fields["period_end"] == "" {
			t.Fatalf("fields = %v", fields)
		}
	})
}

func TestBuildMeterCreate(t *testing.T) {
	t.Run("the tariff and the measuring unit follow the utility", func(t *testing.T) {
		fields := map[string]string{}
		row := buildMeterCreate(CreateMeterInput{PropertyID: 2, UnitID: 5, UtilityType: "Electricity",
			MeterNumber: " EM-12 ", RatePerUnit: 21.5}, fields)
		if len(fields) > 0 {
			t.Fatalf("fields = %v", fields)
		}
		if row.UtilityType != "electricity" || row.MeterNumber != "EM-12" || row.Status != meterActive {
			t.Fatalf("row = %+v", row)
		}
		if row.UnitOfMeasure != "kWh" {
			t.Fatalf("unit_of_measure = %q, an electricity dial is not measured in cubic metres", row.UnitOfMeasure)
		}
		if row.UnitID.Int64 != 5 || !row.UnitID.Valid {
			t.Fatalf("row = %+v", row)
		}
	})

	t.Run("a shared meter carries no unit", func(t *testing.T) {
		fields := map[string]string{}
		row := buildMeterCreate(CreateMeterInput{PropertyID: 2, UtilityType: "water",
			MeterNumber: "W-1", Status: "faulty"}, fields)
		if len(fields) > 0 {
			t.Fatalf("fields = %v", fields)
		}
		if row.UnitID.Valid {
			t.Fatalf("row = %+v, unit_id 0 means the whole property", row)
		}
		if row.UnitOfMeasure != "m3" || row.Status != meterFaulty {
			t.Fatalf("row = %+v", row)
		}
	})

	t.Run("names every field it cannot use", func(t *testing.T) {
		fields := map[string]string{}
		row := buildMeterCreate(CreateMeterInput{UtilityType: "power", UnitOfMeasure: "kilolitres per week",
			MeterNumber: strings.Repeat("9", maxMeterNumber+1), RatePerUnit: -1, Status: "broken"}, fields)
		for _, name := range []string{"property_id", "meter_number", "utility_type", "unit_of_measure",
			"rate_per_unit", "status"} {
			if fields[name] == "" {
				t.Fatalf("fields = %v, %s must be named", fields, name)
			}
		}
		// A rejected unit of measure still has to leave a value the column can
		// hold, since the insert runs only when no field failed.
		if row.UnitOfMeasure != "unit" {
			t.Fatalf("unit_of_measure = %q", row.UnitOfMeasure)
		}
	})
}

func TestMeterUnitOfMeasure(t *testing.T) {
	for _, tc := range []struct{ raw, utilityType, want string }{
		{"", "water", "m3"},
		{"", "gas", "m3"},
		{"", "electricity", "kWh"},
		{"", "garbage", "unit"},
		{"", "internet", "unit"},
		{" kWh ", "electricity", "kWh"},
		{"m2", "water", "m2"},
	} {
		fields := map[string]string{}
		if got := meterUnitOfMeasure(tc.raw, tc.utilityType, fields); got != tc.want || len(fields) > 0 {
			t.Fatalf("meterUnitOfMeasure(%q, %q) = %q fields = %v, want %q", tc.raw, tc.utilityType, got, fields, tc.want)
		}
	}
	for _, bad := range []string{"kWh/h", strings.Repeat("u", maxUnitOfMeasure+1)} {
		fields := map[string]string{}
		if got := meterUnitOfMeasure(bad, "electricity", fields); fields["unit_of_measure"] == "" || got != "kWh" {
			t.Fatalf("value %q = %q fields = %v", bad, got, fields)
		}
	}
}

func TestApplyMeterPatch(t *testing.T) {
	row := func() *MeterRow {
		return &MeterRow{ID: 4, PropertyID: 2, UnitID: sql.NullInt64{Int64: 5, Valid: true},
			UtilityType: "water", MeterNumber: "W-1", UnitOfMeasure: "m3", RatePerUnit: 55.5, Status: meterActive}
	}

	t.Run("an empty patch changes nothing", func(t *testing.T) {
		fields := map[string]string{}
		oldValues, newValues := applyMeterPatch(row(), UpdateMeterInput{}, fields)
		if len(fields) > 0 || len(newValues) > 0 || len(oldValues) > 0 {
			t.Fatalf("fields = %v old = %v new = %v", fields, oldValues, newValues)
		}
	})

	t.Run("unit_id 0 detaches the meter to the property", func(t *testing.T) {
		before := row()
		fields := map[string]string{}
		oldValues, newValues := applyMeterPatch(before, UpdateMeterInput{UnitID: ptrUint(0)}, fields)
		if before.UnitID.Valid {
			t.Fatal("the meter is still attached to a unit")
		}
		if oldValues["unit_id"] != uint64(5) || newValues["unit_id"] != nil {
			t.Fatalf("old = %v new = %v", oldValues, newValues)
		}
	})

	t.Run("a value that only differs in case is not a change", func(t *testing.T) {
		before := row()
		fields := map[string]string{}
		_, newValues := applyMeterPatch(before, UpdateMeterInput{UtilityType: ptrString("WATER"),
			Status: ptrString("active"), MeterNumber: ptrString("W-1")}, fields)
		if len(fields) > 0 || len(newValues) > 0 {
			t.Fatalf("fields = %v new = %v, a patch that says the same thing must not write or audit", fields, newValues)
		}
	})

	t.Run("refuses what the meter table cannot store", func(t *testing.T) {
		fields := map[string]string{}
		applyMeterPatch(row(), UpdateMeterInput{UtilityType: ptrString("power"),
			MeterNumber: ptrString("  "), RatePerUnit: ptrFloat(maxRate * 2), Status: ptrString("broken"),
			UnitOfMeasure: ptrString("m3 per week")}, fields)
		for _, name := range []string{"utility_type", "meter_number", "rate_per_unit", "status", "unit_of_measure"} {
			if fields[name] == "" {
				t.Fatalf("fields = %v, %s must be named", fields, name)
			}
		}
	})
}

func TestBillRowFromReading(t *testing.T) {
	t.Run("prices the consumption on the tenant's rent day", func(t *testing.T) {
		candidate := OpenReadingRow{
			ReadingID: 30, MeterID: 2, UnitID: 5, UtilityType: "water", RatePerUnit: 1.5,
			Consumption: 10, ReadingDate: day(2026, 3, 20),
			PeriodStart: sql.NullTime{Time: day(2026, 2, 20), Valid: true},
			LeaseID:     sql.NullInt64{Int64: 9, Valid: true},
			LeaseDueDay: sql.NullInt64{Int64: 7, Valid: true},
		}
		row, skip := billRowFromReading(candidate)
		if skip != "" {
			t.Fatalf("skip = %q, a metered reading with a tenant has to bill", skip)
		}
		if row.Amount != 15 || row.Type != "water" || row.Status != billPending {
			t.Fatalf("row = %+v", row)
		}
		if row.UnitID != 5 || row.MeterID.Int64 != 2 || row.ReadingID.Int64 != 30 {
			t.Fatalf("row = %+v", row)
		}
		if row.DateDue.Format(dateLayout) != "2026-04-07" {
			t.Fatalf("date_due = %v, the charge follows the rent day it will be collected on", row.DateDue)
		}
		if row.PeriodStart.Format(dateLayout) != "2026-02-20" || row.PeriodEnd.Format(dateLayout) != "2026-03-20" {
			t.Fatalf("period = %v / %v, the charge covers the stretch of dial it came from",
				row.PeriodStart, row.PeriodEnd)
		}
	})

	t.Run("skips the readings that cannot produce a charge", func(t *testing.T) {
		open := OpenReadingRow{ReadingID: 30, MeterID: 2, UnitID: 5, UtilityType: "water",
			RatePerUnit: 1.5, Consumption: 10, ReadingDate: day(2026, 3, 20),
			LeaseID: sql.NullInt64{Int64: 9, Valid: true}, LeaseDueDay: sql.NullInt64{Int64: 7, Valid: true}}

		noLease := open
		noLease.LeaseID = sql.NullInt64{}
		if row, skip := billRowFromReading(noLease); row != nil || skip == "" {
			t.Fatalf("row = %+v skip = %q, a vacant unit is not somebody's bill", row, skip)
		}

		nothingUsed := open
		nothingUsed.Consumption = 0
		if row, skip := billRowFromReading(nothingUsed); row != nil || skip == "" {
			t.Fatalf("row = %+v skip = %q", row, skip)
		}

		unpriced := open
		unpriced.RatePerUnit = 0
		if row, skip := billRowFromReading(unpriced); row != nil || skip == "" {
			t.Fatalf("row = %+v skip = %q, a meter with no tariff cannot be charged", row, skip)
		}
	})

	t.Run("a first reading opens the period on its own day", func(t *testing.T) {
		row, skip := billRowFromReading(OpenReadingRow{ReadingID: 1, UnitID: 5, UtilityType: "water",
			RatePerUnit: 2, Consumption: 5, ReadingDate: day(2026, 1, 31),
			LeaseID: sql.NullInt64{Int64: 9, Valid: true}})
		if skip != "" {
			t.Fatal(skip)
		}
		if row.PeriodStart.Format(dateLayout) != "2026-01-31" {
			t.Fatalf("period_start = %v", row.PeriodStart)
		}
		if row.DateDue.Format(dateLayout) != "2026-02-05" {
			t.Fatalf("date_due = %v, a lease with no due day still bills on a real date", row.DateDue)
		}
	})
}

func TestPeriodStartFrom(t *testing.T) {
	withPrev := &ReadingSnapRow{ReadingDate: day(2026, 3, 20),
		PrevReadingDate: sql.NullTime{Time: day(2026, 2, 18), Valid: true}}
	if got := periodStartFrom(withPrev); got.Format(dateLayout) != "2026-02-18" {
		t.Fatalf("got %v, the period opens where the previous dial was read", got)
	}
	firstEver := &ReadingSnapRow{ReadingDate: day(2026, 3, 20)}
	if got := periodStartFrom(firstEver); got.Format(dateLayout) != "2026-03-20" {
		t.Fatalf("got %v", got)
	}
}

func TestReadingNotesAndCleanLabel(t *testing.T) {
	t.Run("notes are trimmed or absent", func(t *testing.T) {
		fields := map[string]string{}
		if got := readingNotes("  meter changed  ", fields); got.String != "meter changed" || !got.Valid {
			t.Fatalf("got %+v", got)
		}
		if got := readingNotes("   ", fields); got.Valid {
			t.Fatalf("got %+v, blank notes must stay NULL", got)
		}
		if got := readingNotes(strings.Repeat("n", maxNotes+1), fields); got.Valid || fields["notes"] == "" {
			t.Fatalf("got %+v fields = %v", got, fields)
		}
		if got := readingNotes("tab\there", fields); got.Valid || fields["notes"] == "" {
			t.Fatalf("got %+v fields = %v, a control character does not survive the column", got, fields)
		}
	})

	t.Run("labels report why they were refused", func(t *testing.T) {
		fields := map[string]string{}
		if got := cleanLabel("  KD-1  ", maxMeterNumber, "meter_number", fields); got != "KD-1" || len(fields) > 0 {
			t.Fatalf("got %q fields = %v", got, fields)
		}
		cleanLabel("", maxMeterNumber, "meter_number", fields)
		cleanLabel(strings.Repeat("x", maxMeterNumber+1), maxMeterNumber, "number_two", fields)
		cleanLabel("line\nbreak", maxMeterNumber, "three", fields)
		if fields["meter_number"] != "required" || fields["number_two"] == "" || fields["three"] == "" {
			t.Fatalf("fields = %v", fields)
		}
	})
}

func TestScopeFilter(t *testing.T) {
	tenant := uint64(77)

	t.Run("a property manager is limited to the properties they hold", func(t *testing.T) {
		clause, args := scopeFilter(rbac.Scope{OrgID: 1, PropertyIDs: []uint64{4, 5}}, "m.property_id", "bu.unit_id")
		if !strings.Contains(clause, "m.property_id IN (?,?)") {
			t.Fatalf("clause = %q", clause)
		}
		if len(args) != 2 || args[0] != uint64(4) || args[1] != uint64(5) {
			t.Fatalf("args = %v", args)
		}
	})

	t.Run("a tenant sees the unit they lease", func(t *testing.T) {
		clause, args := scopeFilter(rbac.Scope{OrgID: 1, TenantID: &tenant}, "m.property_id", "bu.unit_id")
		if !strings.Contains(clause, "l_own.unit_id = bu.unit_id") || !strings.Contains(clause, "lt_own.tenant_id = ?") {
			t.Fatalf("clause = %q, the unit column must be the one the caller passed", clause)
		}
		if len(args) != 1 || args[0] != tenant {
			t.Fatalf("args = %v", args)
		}
	})

	t.Run("both at once is the union", func(t *testing.T) {
		clause, args := scopeFilter(rbac.Scope{OrgID: 1, PropertyIDs: []uint64{4}, TenantID: &tenant},
			"m.property_id", "ub.unit_id")
		if !strings.Contains(clause, " OR ") {
			t.Fatalf("clause = %q", clause)
		}
		if len(args) != 2 {
			t.Fatalf("args = %v, one placeholder per bound value", args)
		}
	})

	t.Run("nobody is excluded in SQL, not in the response", func(t *testing.T) {
		clause, args := scopeFilter(rbac.Scope{OrgID: 1}, "m.property_id", "ub.unit_id")
		if clause != "1=0" || args != nil {
			t.Fatalf("clause = %q args = %v", clause, args)
		}
		if clause, args := scopeFilter(rbac.Scope{OrgID: 1, AllProperties: true}, "m.property_id", "ub.unit_id"); clause != "1=1" || args != nil {
			t.Fatalf("clause = %q args = %v", clause, args)
		}
	})
}

func TestLikePatternEscapes(t *testing.T) {
	got := likePattern("50% off_unit\\deal")
	if strings.HasPrefix(got, "%50%") {
		t.Fatal("an unescaped wildcard would let the caller widen the search")
	}
	if got != `%50\% off\_unit\\deal%` {
		t.Fatalf("got %q", got)
	}
	if likePattern("") != "%%" {
		t.Fatal("an empty search still needs the surrounding wildcards")
	}
}

func TestCheckRejection(t *testing.T) {
	t.Run("a CHECK violation is a conflict carrying the schema's words", func(t *testing.T) {
		err := &mysql.MySQLError{Number: 3819,
			Message: "Check constraint 'chk_readings_order' is violated."}
		rejection := checkRejection(err, "The reading breaks a database rule")
		if got := conflictMessage(t, rejection); !strings.Contains(got, "chk_readings_order") {
			t.Fatalf("message = %q, the caller has to be told which rule fired", got)
		}
	})

	t.Run("a raised message is cleaned of its driver prefix", func(t *testing.T) {
		err := &mysql.MySQLError{Number: 3819, Message: "[SQLSTATE 45000] A bill cannot be void twice"}
		if got := conflictMessage(t, checkRejection(err, "fallback")); got != "A bill cannot be void twice" {
			t.Fatalf("message = %q", got)
		}
		if got := conflictMessage(t, checkRejection(&mysql.MySQLError{Number: 3819, Message: "[SQLSTATE 45000]  "},
			"The bill breaks a database rule")); got != "The bill breaks a database rule" {
			t.Fatalf("message = %q, an empty signal must still say something", got)
		}
	})

	t.Run("anything else is not the module's to translate", func(t *testing.T) {
		// A driver error that is not a CHECK violation has to fall through so the
		// service can decide what the caller sees; silently mapping it to a 409
		// would turn a dead connection into a validation message.
		for _, err := range []error{
			nil,
			errors.New("connection reset"),
			&mysql.MySQLError{Number: 1062, Message: "Duplicate entry for key 'uq_meters_property_number'"},
			&mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"},
		} {
			if got := checkRejection(err, "fallback"); got != nil {
				t.Fatalf("err = %v got = %v", err, got)
			}
		}
	})

	t.Run("a CHECK violation wrapped in context still fires", func(t *testing.T) {
		err := fmt.Errorf("insert reading: %w",
			&mysql.MySQLError{Number: 3819, Message: "Check constraint 'chk_readings_order' is violated."})
		if got := checkRejection(err, "fallback"); got == nil {
			t.Fatal("errors.As must reach the driver error behind the context")
		}
	})
}

func TestDuplicateAndNoRowsMapping(t *testing.T) {
	if !isDuplicateEntry(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}) {
		t.Fatal("1062 must read as a duplicate")
	}
	wrapped := fmt.Errorf("insert meter: %w",
		&mysql.MySQLError{Number: 1062, Message: "Duplicate entry for key 'uq_readings_meter_date'"})
	if !isDuplicateEntry(wrapped) {
		t.Fatal("a unique index violation wrapped in context is still a duplicate")
	}
	if isDuplicateEntry(&mysql.MySQLError{Number: 3819, Message: "Check violated"}) || isDuplicateEntry(errors.New("boom")) {
		t.Fatal("only 1062 is a duplicate")
	}

	if !errors.Is(notFound(sql.ErrNoRows), httpx.ErrNotFound) {
		t.Fatal("a missing row and a row outside the scope must be indistinguishable")
	}
	boom := errors.New("connection reset")
	if notFound(boom) != boom {
		t.Fatal("a real database error must not be turned into a 404")
	}
}

func TestReadingLockedRefusesBothSides(t *testing.T) {
	s := newTestService()
	if err := s.readingLocked(0, "", false); err != nil {
		t.Fatalf("err = %v, an unbilled reading is still correctable", err)
	}
	if got := conflictMessage(t, s.readingLocked(5, billBilled, true)); got != readingLocked {
		t.Fatalf("message = %q", got)
	}
	if got := conflictMessage(t, s.readingLocked(5, billPending, true)); !strings.Contains(got, readingLocked) {
		t.Fatalf("message = %q", got)
	}
	if got := conflictMessage(t, s.readingLocked(5, billVoid, true)); !strings.Contains(got, "voided") {
		t.Fatalf("message = %q, a void bill points the caller at the row to fix", got)
	}
}

func TestRejectLogsAndConflicts(t *testing.T) {
	s := newTestService()
	if err := s.reject(errors.New("driver: bad connection"), "create meter", "fallback"); err != nil {
		t.Fatalf("err = %v, a database failure is not a rule violation", err)
	}
	rejection := s.reject(&mysql.MySQLError{Number: 3819,
		Message: "Check constraint 'chk_meters_type' is violated."}, "create meter", "The meter breaks a database rule")
	if got := conflictMessage(t, rejection); !strings.Contains(got, "chk_meters_type") {
		t.Fatalf("message = %q", got)
	}
}

func TestNullHelpers(t *testing.T) {
	if got := nullableID(sql.NullInt64{}); got != nil {
		t.Fatalf("got %v", got)
	}
	if got := nullableID(sql.NullInt64{Int64: 0, Valid: true}); got != nil {
		t.Fatalf("got %v, a zero id is no link", got)
	}
	if got := nullableID(sql.NullInt64{Int64: 8, Valid: true}); got != uint64(8) {
		t.Fatalf("got %v", got)
	}

	if nullText(sql.NullString{}) != "" || nullText(sql.NullString{String: "water", Valid: true}) != "water" {
		t.Fatal("nullText must read an absent string as empty")
	}
	if nullIDPtr(sql.NullInt64{}) != nil || *nullIDPtr(sql.NullInt64{Int64: 3, Valid: true}) != 3 {
		t.Fatal("nullIDPtr")
	}
	stamp := day(2026, 3, 20).Add(5 * time.Hour)
	if nullTimePtr(sql.NullTime{}) != nil {
		t.Fatal("nullTimePtr must leave an absent timestamp absent")
	}
	if got := nullTimePtr(sql.NullTime{Time: stamp, Valid: true}); got == nil || got.Format(dateLayout) != "2026-03-20" {
		t.Fatalf("got %v", got)
	}
	if nullFloatPtr(sql.NullFloat64{}) != nil || *nullFloatPtr(sql.NullFloat64{Float64: 12.5, Valid: true}) != 12.5 {
		t.Fatal("nullFloatPtr")
	}
	if unitRefDTO(sql.NullInt64{}, sql.NullString{}) != nil {
		t.Fatal("a shared meter has no unit to show")
	}
	ref := unitRefDTO(sql.NullInt64{Int64: 5, Valid: true}, sql.NullString{String: "B12", Valid: true})
	if ref == nil || ref.ID != 5 || ref.Number != "B12" {
		t.Fatalf("ref = %+v", ref)
	}
	if leaseRefDTO(sql.NullInt64{}, sql.NullString{}) != nil {
		t.Fatal("a bill on a vacant unit has no lease")
	}
}

func TestReadingDTOShowsWhatCanBeCorrected(t *testing.T) {
	row := &ReadingViewRow{
		ReadingRow: ReadingRow{ID: 12, MeterID: 2, ReadingDate: day(2026, 3, 20),
			PreviousReading: 100, CurrentReading: 112.5, Consumption: 12.5,
			Notes: sql.NullString{String: "estimated", Valid: true}},
		UnitOfMeasure: "m3", RatePerUnit: 55.5, MeterNumber: "W-1",
		RecordedName: sql.NullString{String: "Jane Mwangi", Valid: true},
	}
	out := readingDTO(row)
	if !out.Adjustable {
		t.Fatal("an unbilled reading is correctable")
	}
	if out.Consumption != 12.5 || out.Notes != "estimated" || out.RatePerUnit != 55.5 {
		t.Fatalf("dto = %+v", out)
	}
	if out.BillID != nil || out.BillStatus != "" {
		t.Fatalf("dto = %+v", out)
	}

	billed := *row
	billed.BillID = sql.NullInt64{Int64: 40, Valid: true}
	billed.BillStatus = sql.NullString{String: billPending, Valid: true}
	if dto := readingDTO(&billed); dto.Adjustable || *dto.BillID != 40 || dto.BillStatus != billPending {
		t.Fatalf("dto = %+v, a reading with a bill on it is not adjustable", dto)
	}

	// The reading after this one is priced from this figure, so its bill locks
	// this row just as firmly as its own does.
	nextBilled := *row
	nextBilled.NextBillStatus = sql.NullString{String: billBilled, Valid: true}
	if dto := readingDTO(&nextBilled); dto.Adjustable {
		t.Fatalf("dto = %+v", dto)
	}
}

func TestBillSummaryAndDetailDTO(t *testing.T) {
	today := day(2026, 5, 10)
	row := &BillViewRow{
		BillRow: BillRow{ID: 40, UnitID: 5, Type: "water", Amount: 690, Status: billPending,
			DateDue: day(2026, 5, 5), PeriodStart: day(2026, 4, 1), PeriodEnd: day(2026, 4, 30),
			MeterID:   sql.NullInt64{Int64: 2, Valid: true},
			InvoiceID: sql.NullInt64{}},
		PropertyID: 2, PropertyName: "Kodi Apartments", UnitNumber: "B12",
		MeterNumber: sql.NullString{String: "W-1", Valid: true},
		ReadingDate: sql.NullTime{Time: day(2026, 4, 30), Valid: true},
		Consumption: sql.NullFloat64{Float64: 12.5, Valid: true},
	}
	sum := billSummaryDTO(row, today)
	if !sum.Overdue {
		t.Fatal("a pending bill past its due date is overdue")
	}
	if sum.Property.ID != 2 || sum.Property.Name != "Kodi Apartments" || sum.Unit.ID != 5 {
		t.Fatalf("summary = %+v", sum)
	}
	if sum.MeterNumber != "W-1" || *sum.Consumption != 12.5 || sum.InvoiceID != nil {
		t.Fatalf("summary = %+v", sum)
	}

	// Once billing takes the charge, the invoice is the record of what the
	// tenant owes and the bill stops being something utilities can chase.
	onInvoice := *row
	onInvoice.Status = billBilled
	onInvoice.InvoiceID = sql.NullInt64{Int64: 99, Valid: true}
	onInvoice.InvoiceNumber = sql.NullString{String: "INV-2026-0044", Valid: true}
	onInvoice.InvoiceStatus = sql.NullString{String: "issued", Valid: true}
	detail := billDetailDTO(&onInvoice, today)
	if detail.InvoiceID == nil || *detail.InvoiceID != 99 {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.InvoiceNumber != "INV-2026-0044" || detail.InvoiceStatus != "issued" {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.Lease != nil || detail.TenantName != "" {
		t.Fatalf("detail = %+v, a bill with no active lease must not invent one", detail)
	}

	voided := *row
	voided.Status = billVoid
	if billSummaryDTO(&voided, today).Overdue {
		t.Fatal("a void charge is nobody's overdue money")
	}
}

func TestMeterSummaryDTOCarriesTheDialHistory(t *testing.T) {
	row := &MeterViewRow{
		MeterRow: MeterRow{ID: 2, PropertyID: 2, UnitID: sql.NullInt64{Int64: 5, Valid: true},
			UtilityType: "water", MeterNumber: "W-1", UnitOfMeasure: "m3", RatePerUnit: 55.5,
			Status: meterActive, CreatedAt: day(2026, 1, 1), UpdatedAt: day(2026, 4, 1)},
		PropertyName: "Kodi Apartments", PropertyCode: sql.NullString{String: "KD", Valid: true},
		UnitNumber:      sql.NullString{String: "B12", Valid: true},
		LastReadingDate: sql.NullTime{Time: day(2026, 4, 30), Valid: true},
		LastReadingVal:  sql.NullFloat64{Float64: 112.5, Valid: true},
		OpenReadings:    1,
	}
	out := meterSummaryDTO(row)
	if out.Property.Code != "KD" || out.Unit == nil || out.Unit.Number != "B12" {
		t.Fatalf("dto = %+v", out)
	}
	if out.LastReading == nil || *out.LastReading != 112.5 || out.OpenReadings != 1 {
		t.Fatalf("dto = %+v", out)
	}

	shared := *row
	shared.UnitID = sql.NullInt64{}
	shared.LastReadingDate = sql.NullTime{}
	shared.LastReadingVal = sql.NullFloat64{}
	option := meterOptionDTO(&shared)
	if option.UnitID != nil || option.UnitNumber != "" {
		t.Fatalf("option = %+v, a property-level meter belongs to no door", option)
	}
	if meterSummaryDTO(&shared).LastReadingAt != nil {
		t.Fatal("a never-read meter has no last reading to show")
	}
}
