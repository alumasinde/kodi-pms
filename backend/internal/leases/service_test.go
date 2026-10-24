package leases

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

func TestBuildLeaseCreate(t *testing.T) {
	t.Run("applies supplied values", func(t *testing.T) {
		end := "2026-12-31"
		deposit := 90000.0
		fields := map[string]string{}
		row := buildLeaseCreate(9, 3, CreateInput{
			UnitID: 21, LeaseNumber: " KD-L-2026-001 ", StartDate: "2026-01-01",
			EndDate: &end, RentAmount: 45000.006, DepositAmount: &deposit,
			BillingDay: ptrInt(10), DueDay: ptrInt(5), NoticePeriodDays: ptrInt(60),
		}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected validation errors: %v", fields)
		}
		if row.OrganizationID != 9 || row.UnitID != 21 || row.Status != statusDraft {
			t.Fatalf("row = %+v", row)
		}
		if row.LeaseNumber != "KD-L-2026-001" {
			t.Fatalf("lease_number = %q", row.LeaseNumber)
		}
		if row.StartDate.Format(dateLayout) != "2026-01-01" || !row.EndDate.Valid {
			t.Fatalf("dates = %v / %+v", row.StartDate, row.EndDate)
		}
		if row.EndDate.Time.Format(dateLayout) != "2026-12-31" {
			t.Fatalf("end_date = %v", row.EndDate.Time)
		}
		if row.RentAmount != 45000.01 || row.DepositAmount != 90000 {
			t.Fatalf("money = %v / %v", row.RentAmount, row.DepositAmount)
		}
		if row.BillingDay != 10 || row.DueDay != 5 || row.NoticePeriodDays != 60 {
			t.Fatalf("terms = %v / %v / %v", row.BillingDay, row.DueDay, row.NoticePeriodDays)
		}
		if !row.CreatedBy.Valid || row.CreatedBy.Int64 != 3 {
			t.Fatalf("created_by = %+v", row.CreatedBy)
		}
		if row.SignedAt.Valid || row.TerminatedAt.Valid {
			t.Fatalf("a draft should carry no signature or termination: %+v %+v", row.SignedAt, row.TerminatedAt)
		}
	})

	t.Run("defaults fill the gaps", func(t *testing.T) {
		fields := map[string]string{}
		row := buildLeaseCreate(1, 2, CreateInput{
			UnitID: 5, StartDate: "2026-02-01", RentAmount: 12000,
		}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected validation errors: %v", fields)
		}
		if row.BillingDay != defaultBillingDay || row.DueDay != defaultDueDay {
			t.Fatalf("days = %v / %v", row.BillingDay, row.DueDay)
		}
		if row.NoticePeriodDays != defaultNoticeDays || row.DepositAmount != 0 {
			t.Fatalf("terms = %v / %v", row.NoticePeriodDays, row.DepositAmount)
		}
		if row.LeaseNumber != "" {
			t.Fatalf("an omitted number should stay empty for the generator: %q", row.LeaseNumber)
		}
		if row.EndDate.Valid {
			t.Fatalf("end_date should stay open-ended: %+v", row.EndDate)
		}
	})

	t.Run("requires core fields", func(t *testing.T) {
		fields := map[string]string{}
		_ = buildLeaseCreate(1, 1, CreateInput{}, fields)
		if fields["unit_id"] != "required" || fields["start_date"] != "required" {
			t.Fatalf("fields = %v", fields)
		}
		if !strings.Contains(fields["rent_amount"], "greater than zero") {
			t.Fatalf("rent_amount = %q", fields["rent_amount"])
		}
	})

	t.Run("end date needs a usable start", func(t *testing.T) {
		end := "2025-12-31"
		fields := map[string]string{}
		_ = buildLeaseCreate(1, 1, CreateInput{
			UnitID: 1, StartDate: "2026-01-01", EndDate: &end, RentAmount: 1000,
		}, fields)
		if !strings.Contains(fields["end_date"], "on or after") {
			t.Fatalf("end_date = %q", fields["end_date"])
		}

		bad := "31/12/2026"
		fields = map[string]string{}
		row := buildLeaseCreate(1, 1, CreateInput{
			UnitID: 1, StartDate: "2026-01-01", EndDate: &bad, RentAmount: 1000,
		}, fields)
		if !strings.Contains(fields["end_date"], dateLayout) {
			t.Fatalf("end_date = %q", fields["end_date"])
		}
		if row.EndDate.Valid {
			t.Fatalf("an invalid end date should not be stored: %+v", row.EndDate)
		}
	})

	cases := []struct {
		name    string
		mutate  func(*CreateInput)
		field   string
		message string
	}{
		{"long number", func(in *CreateInput) { in.LeaseNumber = strings.Repeat("9", maxLeaseNumber+1) }, "lease_number", "at most 50"},
		{"control character", func(in *CreateInput) { in.LeaseNumber = "L-\x001" }, "lease_number", "control"},
		{"bad start", func(in *CreateInput) { in.StartDate = "soon" }, "start_date", dateLayout},
		{"zero rent", func(in *CreateInput) { in.RentAmount = 0 }, "rent_amount", "greater than zero"},
		{"huge rent", func(in *CreateInput) { in.RentAmount = maxAmount + 1 }, "rent_amount", "9999999999.99"},
		{"negative deposit", func(in *CreateInput) { in.DepositAmount = ptrFloat(-1) }, "deposit_amount", "between 0"},
		{"huge deposit", func(in *CreateInput) { in.DepositAmount = ptrFloat(maxAmount + 1) }, "deposit_amount", "between 0"},
		{"billing day too low", func(in *CreateInput) { in.BillingDay = ptrInt(minDayOfMonth - 1) }, "billing_day", "between 1 and 28"},
		{"billing day 31", func(in *CreateInput) { in.BillingDay = ptrInt(31) }, "billing_day", "between 1 and 28"},
		{"due day 29", func(in *CreateInput) { in.DueDay = ptrInt(maxDayOfMonth + 1) }, "due_day", "between 1 and 28"},
		{"negative notice", func(in *CreateInput) { in.NoticePeriodDays = ptrInt(-1) }, "notice_period_days", "between 0 and 3650"},
		{"huge notice", func(in *CreateInput) { in.NoticePeriodDays = ptrInt(maxNoticeDays + 1) }, "notice_period_days", "between 0 and 3650"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := CreateInput{UnitID: 1, StartDate: "2026-01-01", RentAmount: 1000}
			tc.mutate(&in)
			fields := map[string]string{}
			_ = buildLeaseCreate(1, 1, in, fields)
			if fields[tc.field] == "" {
				t.Fatalf("missing error for %s: %v", tc.field, fields)
			}
			if !strings.Contains(fields[tc.field], tc.message) {
				t.Fatalf("%s = %q, want substring %q", tc.field, fields[tc.field], tc.message)
			}
		})
	}
}

func TestApplyLeasePatch(t *testing.T) {
	t.Run("identical values are not recorded", func(t *testing.T) {
		row := leaseRowFixture()
		fields := map[string]string{}
		oldValues, newValues := applyLeasePatch(row, UpdateInput{
			LeaseNumber: ptrString(" KD-L-2026-001 "),
			StartDate:   ptrString("2026-01-01"),
			RentAmount:  ptrFloat(45000),
			BillingDay:  ptrInt(1),
		}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if len(oldValues) != 0 || len(newValues) != 0 {
			t.Fatalf("no-op recorded: old=%v new=%v", oldValues, newValues)
		}
	})

	t.Run("changes are recorded for the audit trail", func(t *testing.T) {
		row := leaseRowFixture()
		unit := uint64(22)
		end := "2027-06-30"
		fields := map[string]string{}
		oldValues, newValues := applyLeasePatch(row, UpdateInput{
			UnitID: &unit, EndDate: json.RawMessage(`"` + end + `"`),
			DepositAmount: ptrFloat(50000.0), BillingDay: ptrInt(20), DueDay: ptrInt(7),
			NoticePeriodDays: ptrInt(45),
		}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if row.UnitID != 22 || row.BillingDay != 20 || row.DueDay != 7 || row.NoticePeriodDays != 45 {
			t.Fatalf("row = %+v", row)
		}
		if oldValues["unit_id"] != uint64(21) || newValues["unit_id"] != uint64(22) {
			t.Fatalf("unit audit = %v -> %v", oldValues["unit_id"], newValues["unit_id"])
		}
		if oldValues["end_date"] != "2026-12-31" || newValues["end_date"] != end {
			t.Fatalf("end audit = %v -> %v", oldValues["end_date"], newValues["end_date"])
		}
		if newValues["deposit_amount"] != 50000.0 || oldValues["deposit_amount"] != 90000.0 {
			t.Fatalf("deposit audit = %v -> %v", oldValues["deposit_amount"], newValues["deposit_amount"])
		}
	})

	t.Run("zero unit and blank number are rejected", func(t *testing.T) {
		row := leaseRowFixture()
		zero := uint64(0)
		fields := map[string]string{}
		applyLeasePatch(row, UpdateInput{UnitID: &zero}, fields)
		if fields["unit_id"] != "required" || row.UnitID != 21 {
			t.Fatalf("unit_id = %q row = %v", fields["unit_id"], row.UnitID)
		}

		row = leaseRowFixture()
		blank := "   "
		fields = map[string]string{}
		applyLeasePatch(row, UpdateInput{LeaseNumber: &blank}, fields)
		if fields["lease_number"] != "required" || row.LeaseNumber != "KD-L-2026-001" {
			t.Fatalf("lease_number = %q row = %q", fields["lease_number"], row.LeaseNumber)
		}
	})

	t.Run("end_date null makes the tenancy open-ended", func(t *testing.T) {
		row := leaseRowFixture()
		fields := map[string]string{}
		oldValues, newValues := applyLeasePatch(row, UpdateInput{EndDate: json.RawMessage(`  null `)}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if row.EndDate.Valid {
			t.Fatalf("end_date not cleared: %+v", row.EndDate)
		}
		if oldValues["end_date"] != "2026-12-31" || newValues["end_date"] != nil {
			t.Fatalf("end audit = %v -> %v", oldValues["end_date"], newValues["end_date"])
		}
	})

	t.Run("end_date rejects unusable payloads", func(t *testing.T) {
		for _, raw := range []string{`12`, `"31-12-2026"`, `"2025-01-01"`} {
			row := leaseRowFixture()
			fields := map[string]string{}
			applyLeasePatch(row, UpdateInput{EndDate: json.RawMessage(raw)}, fields)
			if fields["end_date"] == "" {
				t.Fatalf("%s accepted: %v", raw, fields)
			}
		}
	})

	t.Run("moving the start past the end is refused", func(t *testing.T) {
		row := leaseRowFixture()
		fields := map[string]string{}
		applyLeasePatch(row, UpdateInput{StartDate: ptrString("2027-01-01")}, fields)
		if !strings.Contains(fields["end_date"], "on or after") {
			t.Fatalf("fields = %v", fields)
		}
	})

	t.Run("money is rounded and bounded", func(t *testing.T) {
		row := leaseRowFixture()
		fields := map[string]string{}
		oldValues, newValues := applyLeasePatch(row, UpdateInput{RentAmount: ptrFloat(60500.016)}, fields)
		if len(fields) != 0 || row.RentAmount != 60500.02 {
			t.Fatalf("row = %+v fields = %v", row, fields)
		}
		if oldValues["rent_amount"] != 45000.0 || newValues["rent_amount"] != 60500.02 {
			t.Fatalf("rent audit = %v -> %v", oldValues["rent_amount"], newValues["rent_amount"])
		}

		row = leaseRowFixture()
		fields = map[string]string{}
		applyLeasePatch(row, UpdateInput{RentAmount: ptrFloat(0)}, fields)
		if fields["rent_amount"] == "" {
			t.Fatalf("missing rent error: %v", fields)
		}
		row = leaseRowFixture()
		fields = map[string]string{}
		applyLeasePatch(row, UpdateInput{DepositAmount: ptrFloat(maxAmount + 1)}, fields)
		if fields["deposit_amount"] == "" {
			t.Fatalf("missing deposit error: %v", fields)
		}
	})

	t.Run("days and notice stay inside the schema checks", func(t *testing.T) {
		row := leaseRowFixture()
		fields := map[string]string{}
		applyLeasePatch(row, UpdateInput{BillingDay: ptrInt(29), DueDay: ptrInt(0), NoticePeriodDays: ptrInt(maxNoticeDays + 1)}, fields)
		for _, field := range []string{"billing_day", "due_day", "notice_period_days"} {
			if fields[field] == "" {
				t.Fatalf("missing error for %s: %v", field, fields)
			}
		}
		if row.BillingDay != 1 || row.DueDay != 5 || row.NoticePeriodDays != 30 {
			t.Fatalf("rejected values leaked into the row: %+v", row)
		}
	})

	t.Run("omitted fields are untouched", func(t *testing.T) {
		row := leaseRowFixture()
		fields := map[string]string{}
		oldValues, newValues := applyLeasePatch(row, UpdateInput{}, fields)
		if len(fields) != 0 || len(oldValues) != 0 || len(newValues) != 0 {
			t.Fatalf("empty patch: %v %v %v", fields, oldValues, newValues)
		}
		if row.LeaseNumber != "KD-L-2026-001" || row.Status != statusActive {
			t.Fatalf("row changed: %+v", row)
		}
	})
}

func TestNormalizeTenants(t *testing.T) {
	t.Run("dedupes and defaults the first role", func(t *testing.T) {
		fields := map[string]string{}
		got := normalizeTenants([]TenantRef{{TenantID: 7}, {TenantID: 8, Role: " CO_TENANT "}, {TenantID: 7, Role: roleOccupant}}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		want := []TenantRef{{TenantID: 7, Role: rolePrimary}, {TenantID: 8, Role: roleCoTenant}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("refs = %+v, want %+v", got, want)
		}
	})

	t.Run("rejects a second primary", func(t *testing.T) {
		fields := map[string]string{}
		got := normalizeTenants([]TenantRef{{TenantID: 1, Role: rolePrimary}, {TenantID: 2, Role: rolePrimary}}, fields)
		if fields["tenants"] != "only one tenant can be the primary tenant" {
			t.Fatalf("tenants = %q", fields["tenants"])
		}
		if len(got) != 1 || got[0].TenantID != 1 {
			t.Fatalf("refs = %+v", got)
		}
	})

	t.Run("rejects unknown roles and zero ids", func(t *testing.T) {
		fields := map[string]string{}
		got := normalizeTenants([]TenantRef{{TenantID: 3, Role: "roommate"}}, fields)
		if fields["tenants"] != tenantRoleMessage || len(got) != 0 {
			t.Fatalf("refs = %+v tenants = %q", got, fields["tenants"])
		}

		fields = map[string]string{}
		got = normalizeTenants([]TenantRef{{TenantID: 0, Role: rolePrimary}}, fields)
		if fields["tenants"] != "must use tenant identifiers greater than zero" || len(got) != 0 {
			t.Fatalf("refs = %+v tenants = %q", got, fields["tenants"])
		}
	})

	t.Run("caps the roster", func(t *testing.T) {
		refs := make([]TenantRef, maxLeaseTenants+1)
		for i := range refs {
			refs[i] = TenantRef{TenantID: uint64(i + 1), Role: roleOccupant}
		}
		fields := map[string]string{}
		if got := normalizeTenants(refs, fields); got != nil {
			t.Fatalf("oversized roster should be dropped: %+v", got)
		}
		if !strings.Contains(fields["tenants"], fmt.Sprintf("more than %d", maxLeaseTenants)) {
			t.Fatalf("tenants = %q", fields["tenants"])
		}
	})

	t.Run("an empty roster is allowed for a draft", func(t *testing.T) {
		fields := map[string]string{}
		if got := normalizeTenants(nil, fields); len(got) != 0 || len(fields) != 0 {
			t.Fatalf("refs = %+v fields = %v", got, fields)
		}
	})
}

func TestDateHelpers(t *testing.T) {
	t.Run("parseDate", func(t *testing.T) {
		got, ok := parseDate("2026-03-01")
		if !ok || got.Format(dateLayout) != "2026-03-01" || got.Location() != time.UTC {
			t.Fatalf("date = %v ok = %v", got, ok)
		}
		got, ok = parseDate("2026-03-01T10:00:00+03:00")
		if !ok || got.Format(time.RFC3339) != "2026-03-01T07:00:00Z" {
			t.Fatalf("timestamp = %v ok = %v", got, ok)
		}
		if _, ok := parseDate("2026-3-1"); ok {
			t.Fatal("unpadded date should be rejected")
		}
		if _, ok := parseDate("  "); ok {
			t.Fatal("blank should be rejected")
		}
	})

	t.Run("parseOptionalDate distinguishes omission", func(t *testing.T) {
		fields := map[string]string{}
		if _, ok := parseOptionalDate("start_date", nil, fields); ok || len(fields) != 0 {
			t.Fatalf("omitted value should parse as absent: %v %v", fields, ok)
		}
		bad := "01-02-2026"
		if _, ok := parseOptionalDate("start_date", &bad, fields); ok || fields["start_date"] == "" {
			t.Fatalf("bad date = %v fields = %v", ok, fields)
		}
	})

	t.Run("parseTimestampField accepts dates and timestamps", func(t *testing.T) {
		fields := map[string]string{}
		empty := "   "
		if _, ok := parseTimestampField("signed_at", &empty, fields); ok || len(fields) != 0 {
			t.Fatalf("blank should read as omitted: %v %v", ok, fields)
		}
		stamp := "2026-01-05T09:30:00Z"
		got, ok := parseTimestampField("signed_at", &stamp, fields)
		if !ok || got.Format(time.RFC3339) != stamp {
			t.Fatalf("signed_at = %v ok = %v", got, ok)
		}
		if _, ok := parseTimestampField("signed_at", ptrString("next week"), fields); ok ||
			fields["signed_at"] != "must be a date or an RFC3339 timestamp" {
			t.Fatalf("fields = %v", fields)
		}
	})
}

func TestNumberPrefix(t *testing.T) {
	if got := numberPrefix("  KD-001 "); got != "KD-001-L" {
		t.Fatalf("prefix = %q", got)
	}
	if got := numberPrefix(""); got != "L" {
		t.Fatalf("blank code = %q, want L", got)
	}
}

func TestLeaseScopeFilter(t *testing.T) {
	tenantID := uint64(12)
	cases := []struct {
		name     string
		scope    rbac.Scope
		contains string
		args     int
	}{
		{"all properties", rbac.Scope{AllProperties: true}, "1=1", 0},
		{"property list", rbac.Scope{PropertyIDs: []uint64{4, 5}}, "u.property_id IN (?,?)", 2},
		{"tenant only", rbac.Scope{TenantID: &tenantID}, "lt_own.tenant_id = ?", 1},
		{"both widen", rbac.Scope{PropertyIDs: []uint64{4}, TenantID: &tenantID}, " OR ", 2},
		{"nothing narrows to empty", rbac.Scope{}, "1=0", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clause, args := leaseScopeFilter(tc.scope)
			if !strings.Contains(clause, tc.contains) {
				t.Fatalf("clause = %q, want %q", clause, tc.contains)
			}
			if len(args) != tc.args {
				t.Fatalf("args = %v, want %d", args, tc.args)
			}
		})
	}
}

func TestLikeEscaping(t *testing.T) {
	if got := likePattern("100_"); got != `%100\_%` {
		t.Fatalf("likePattern = %q", got)
	}
	if got := likePattern(`a\b`); got != `%a\\b%` {
		t.Fatalf("likePattern = %q", got)
	}
	if got := escapeLike("KD!%_"); got != "KD!!!%!_" {
		t.Fatalf("escapeLike = %q", got)
	}
}

func TestRosterHelpers(t *testing.T) {
	rows := []TenantRosterRow{
		{TenantID: 1, Role: rolePrimary},
		{TenantID: 2, Role: roleCoTenant},
	}
	if !reflect.DeepEqual(rosterRefs(rows), []TenantRef{{TenantID: 1, Role: rolePrimary}, {TenantID: 2, Role: roleCoTenant}}) {
		t.Fatal("rosterRefs mismatch")
	}
	if !reflect.DeepEqual(tenantIDs(rosterRefs(rows)), []uint64{1, 2}) {
		t.Fatal("tenantIDs mismatch")
	}
	if countRole(rows, rolePrimary) != 1 || countRole(rows, roleGuarantor) != 0 {
		t.Fatal("countRole mismatch")
	}
	if countRefs([]TenantRef{{TenantID: 1, Role: rolePrimary}, {TenantID: 2, Role: rolePrimary}}, rolePrimary) != 2 {
		t.Fatal("countRefs mismatch")
	}
	if !sameRoster(nil, nil) || !sameRoster(rows, []TenantRef{{TenantID: 2, Role: roleCoTenant}, {TenantID: 1, Role: rolePrimary}}) {
		t.Fatal("equal rosters should match regardless of order")
	}
	if sameRoster(rows, []TenantRef{{TenantID: 1, Role: roleOccupant}, {TenantID: 2, Role: roleCoTenant}}) {
		t.Fatal("a changed role should not match")
	}
	if sameRoster(rows, []TenantRef{{TenantID: 1, Role: rolePrimary}}) {
		t.Fatal("a shorter roster should not match")
	}
}

func TestStatusAndRoleValidation(t *testing.T) {
	for _, status := range leaseStatuses {
		if !validStatus(status) {
			t.Errorf("status %q should be valid", status)
		}
	}
	if validStatus("Active") || validStatus("") || validStatus("cancelled") {
		t.Fatal("unknown statuses should be rejected")
	}
	for _, role := range tenantRoles {
		if !validRole(role) {
			t.Errorf("role %q should be valid", role)
		}
	}
	if validRole("Primary") || validRole("roommate") {
		t.Fatal("unknown roles should be rejected")
	}
	if !editableStatuses[statusDraft] || !editableStatuses[statusPendingSignature] || editableStatuses[statusActive] {
		t.Fatal("only drafts and pending signatures are editable")
	}
}

func TestLeaseSummaryDTO(t *testing.T) {
	row := leaseViewFixture()
	out := leaseSummaryDTO(row)
	if out.ID != 55 || out.LeaseNumber != "KD-L-2026-001" || out.Status != statusActive {
		t.Fatalf("summary = %+v", out)
	}
	if out.Unit.ID != 21 || out.Unit.Number != "101" {
		t.Fatalf("unit = %+v", out.Unit)
	}
	if out.Unit.Property.ID != 4 || out.Unit.Property.Name != "Kilimani Heights" || out.Unit.Property.Code != "KD-001" {
		t.Fatalf("property = %+v", out.Unit.Property)
	}
	if out.RentAmount != 45000 || out.DepositAmount != 90000 {
		t.Fatalf("money = %v / %v", out.RentAmount, out.DepositAmount)
	}
	if out.PrimaryTenant != "Jane Wanjiku" {
		t.Fatalf("primary tenant = %q", out.PrimaryTenant)
	}
	if out.EndDate == nil || !out.EndDate.Equal(row.EndDate.Time) {
		t.Fatalf("end_date = %v", out.EndDate)
	}
	if !out.CreatedAt.Equal(row.CreatedAt) || !out.UpdatedAt.Equal(row.UpdatedAt) {
		t.Fatalf("timestamps = %v / %v", out.CreatedAt, out.UpdatedAt)
	}

	openEnded := leaseViewFixture()
	openEnded.LeaseRow.EndDate = sql.NullTime{}
	openEnded.PrimaryTenant = sql.NullString{}
	if got := leaseSummaryDTO(openEnded); got.EndDate != nil || got.PrimaryTenant != "" {
		t.Fatalf("open-ended lease = %+v", got)
	}
}

func TestLeaseDetailJSON(t *testing.T) {
	row := leaseViewFixture()
	detail := leaseDetailDTO(row,
		tenantDTOs([]TenantRosterRow{
			{TenantID: 1, Role: rolePrimary, FullName: "Jane Wanjiku", Email: "jane@example.co.ke"},
			{TenantID: 2, Role: roleCoTenant, FullName: "Peter Otieno", Email: "peter@example.co.ke",
				Phone: sql.NullString{String: "+254700000002", Valid: true}},
		}),
		depositDTOs([]DepositRow{{ID: 7, Amount: 90000, Status: "held"}}),
	)
	b, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "lease_number", "unit", "start_date", "end_date", "status",
		"billing_day", "due_day", "notice_period_days", "signed_at", "terminated_at", "tenants", "deposits"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing key %q in %s", key, b)
		}
	}
	if _, ok := decoded["LeaseSummaryDTO"]; ok {
		t.Fatalf("embedded struct was not flattened: %s", b)
	}
	if decoded["terminated_at"] != nil || decoded["end_date"] == nil {
		t.Fatalf("nullable fields = %v / %v", decoded["terminated_at"], decoded["end_date"])
	}
	if detail.Tenants[1].Phone != "+254700000002" || detail.Tenants[0].Phone != "" {
		t.Fatalf("tenants = %+v", detail.Tenants)
	}
	if detail.Deposits[0].DatePaid != nil || detail.Deposits[0].Amount != 90000 {
		t.Fatalf("deposits = %+v", detail.Deposits)
	}
	if detail.TerminationReason != "" {
		t.Fatalf("termination_reason = %q", detail.TerminationReason)
	}
}

func TestSmallHelpers(t *testing.T) {
	if got := roundToCents(45000.006); got != 45000.01 {
		t.Fatalf("roundToCents = %v", got)
	}
	if got := roundToCents(45000.004); got != 45000 {
		t.Fatalf("roundToCents = %v", got)
	}
	if hasRaw(nil) || hasRaw(json.RawMessage("   ")) {
		t.Fatal("empty raw should read as omitted")
	}
	if !hasRaw(json.RawMessage(`null`)) || !isJSONNull(json.RawMessage(` null `)) {
		t.Fatal("explicit null should be recognised")
	}
	if isJSONNull(json.RawMessage(`"null"`)) {
		t.Fatal("a string is not a SQL null")
	}
	if got := nullText(sql.NullString{}); got != "" {
		t.Fatalf("nullText = %q", got)
	}
	if got := nullText(sql.NullString{String: "KD-001", Valid: true}); got != "KD-001" {
		t.Fatalf("nullText = %q", got)
	}
	if formatDate(sql.NullTime{}) != nil {
		t.Fatal("formatDate of a null should be nil")
	}
	if got := formatDate(sql.NullTime{Time: time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC), Valid: true}); got != "2026-05-06" {
		t.Fatalf("formatDate = %v", got)
	}
	if timePointer(sql.NullTime{}) != nil {
		t.Fatal("timePointer of a null should be nil")
	}
	stamped := sql.NullTime{Time: time.Date(2026, 5, 6, 9, 0, 0, 0, time.FixedZone("EAT", 3*3600)), Valid: true}
	if got := timePointer(stamped); got == nil || got.Format(time.RFC3339) != "2026-05-06T06:00:00Z" {
		t.Fatalf("timePointer = %v", got)
	}
	if got := dayField("billing_day", nil, defaultBillingDay, map[string]string{}); got != defaultBillingDay {
		t.Fatalf("dayField default = %d", got)
	}
	fields := map[string]string{}
	if got := dayField("due_day", ptrInt(29), defaultDueDay, fields); got != defaultDueDay || fields["due_day"] == "" {
		t.Fatalf("dayField = %d fields = %v", got, fields)
	}
	if got := leaseNumber(" L-1 ", fields); got != "L-1" {
		t.Fatalf("leaseNumber = %q", got)
	}
	if got := leaseNumber("", fields); got != "" || fields["lease_number"] != "" {
		t.Fatalf("an omitted number is not an error: %q %v", got, fields)
	}
}

func TestPendingDeposit(t *testing.T) {
	cases := []struct {
		name     string
		amount   float64
		existing bool
		want     bool
	}{
		{"opens what the tenant owes", 90000, false, true},
		{"a lease with no deposit owes nothing", 0, false, false},
		{"an existing record is kept", 90000, true, false},
		{"nothing to open when already recorded", 0, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pendingDeposit(tc.amount, tc.existing); got != tc.want {
				t.Fatalf("pendingDeposit(%v, %v) = %v, want %v", tc.amount, tc.existing, got, tc.want)
			}
		})
	}
}

func TestIsDuplicateEntry(t *testing.T) {
	if !isDuplicateEntry(&mysql.MySQLError{Number: 1062}) {
		t.Fatal("1062 should be recognised")
	}
	if !isDuplicateEntry(fmt.Errorf("insert: %w", &mysql.MySQLError{Number: 1062})) {
		t.Fatal("wrapped 1062 should be recognised")
	}
	if isDuplicateEntry(errors.New("boom")) || isDuplicateEntry(&mysql.MySQLError{Number: 1452}) {
		t.Fatal("other errors should not be recognised")
	}
}

func TestServiceAuthorization(t *testing.T) {
	svc := NewService(nil, nil, discardLogger())
	ctx := context.Background()
	noPerms := principalWith()
	noOrg := &rbac.Principal{Perms: map[string]struct{}{"leases.view": {}}}
	cases := []struct {
		name   string
		status int
		call   func() error
	}{
		{"list without principal", 401, func() error { _, err := svc.List(ctx, nil, ListQuery{}); return err }},
		{"list without org", 401, func() error { _, err := svc.List(ctx, noOrg, ListQuery{}); return err }},
		{"list without permission", 403, func() error { _, err := svc.List(ctx, noPerms, ListQuery{}); return err }},
		{"get without permission", 403, func() error { _, err := svc.Get(ctx, noPerms, 1); return err }},
		{"tenants without permission", 403, func() error { _, err := svc.Tenants(ctx, noPerms, 1); return err }},
		{"create without permission", 403, func() error {
			_, err := svc.Create(ctx, noPerms, CreateInput{}, ClientMeta{})
			return err
		}},
		{"update without permission", 403, func() error {
			_, err := svc.Update(ctx, noPerms, 1, UpdateInput{}, ClientMeta{})
			return err
		}},
		{"delete without permission", 403, func() error { return svc.Delete(ctx, noPerms, 1, ClientMeta{}) }},
		{"activate without permission", 403, func() error {
			_, err := svc.Activate(ctx, noPerms, 1, ActivateInput{}, ClientMeta{})
			return err
		}},
		{"terminate without permission", 403, func() error {
			_, err := svc.Terminate(ctx, noPerms, 1, TerminateInput{}, ClientMeta{})
			return err
		}},
		{"renew without permission", 403, func() error {
			_, err := svc.Renew(ctx, noPerms, 1, RenewInput{}, ClientMeta{})
			return err
		}},
		{"replace tenants without permission", 403, func() error {
			_, err := svc.ReplaceTenants(ctx, noPerms, 1, ReplaceTenantsInput{}, ClientMeta{})
			return err
		}},
		{"get missing id", 404, func() error { _, err := svc.Get(ctx, principalWith("leases.view"), 0); return err }},
		{"tenants missing id", 404, func() error {
			_, err := svc.Tenants(ctx, principalWith("leases.view"), 0)
			return err
		}},
		{"update missing id", 404, func() error {
			_, err := svc.Update(ctx, principalWith("leases.update"), 0, UpdateInput{}, ClientMeta{})
			return err
		}},
		{"delete missing id", 404, func() error {
			return svc.Delete(ctx, principalWith("leases.delete"), 0, ClientMeta{})
		}},
		{"activate missing id", 404, func() error {
			_, err := svc.Activate(ctx, principalWith("leases.activate"), 0, ActivateInput{}, ClientMeta{})
			return err
		}},
		{"terminate missing id", 404, func() error {
			_, err := svc.Terminate(ctx, principalWith("leases.terminate"), 0, TerminateInput{}, ClientMeta{})
			return err
		}},
		{"renew missing id", 404, func() error {
			_, err := svc.Renew(ctx, principalWith("leases.renew"), 0, RenewInput{}, ClientMeta{})
			return err
		}},
		{"replace tenants missing id", 404, func() error {
			_, err := svc.ReplaceTenants(ctx, principalWith("leases.update"), 0, ReplaceTenantsInput{}, ClientMeta{})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			var e *httpx.Err
			if !errors.As(err, &e) || e.Status != tc.status {
				t.Fatalf("err = %v, want status %d", err, tc.status)
			}
		})
	}
}

func TestValidationStopsBeforeTheDatabase(t *testing.T) {
	svc := NewService(nil, nil, discardLogger())
	ctx := context.Background()

	t.Run("list filters", func(t *testing.T) {
		p := principalWith("leases.view")
		cases := []struct {
			query ListQuery
			field string
		}{
			{ListQuery{Search: strings.Repeat("a", maxSearch+1)}, "q"},
			{ListQuery{Status: "cancelled"}, "status"},
		}
		for _, tc := range cases {
			_, err := svc.List(ctx, p, tc.query)
			var e *httpx.Err
			if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity || e.Fields[tc.field] == "" {
				t.Fatalf("query %+v err = %v", tc.query, err)
			}
		}
	})

	t.Run("create", func(t *testing.T) {
		_, err := svc.Create(ctx, principalWith("leases.create"), CreateInput{}, ClientMeta{})
		var e *httpx.Err
		if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity {
			t.Fatalf("err = %v", err)
		}
		for _, field := range []string{"unit_id", "start_date", "rent_amount"} {
			if e.Fields[field] == "" {
				t.Errorf("missing %s in %v", field, e.Fields)
			}
		}
	})

	t.Run("activate", func(t *testing.T) {
		_, err := svc.Activate(ctx, principalWith("leases.activate"), 1, ActivateInput{SignedAt: ptrString("yesterday")}, ClientMeta{})
		var e *httpx.Err
		if !errors.As(err, &e) || e.Fields["signed_at"] == "" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("terminate", func(t *testing.T) {
		_, err := svc.Terminate(ctx, principalWith("leases.terminate"), 1, TerminateInput{}, ClientMeta{})
		var e *httpx.Err
		if !errors.As(err, &e) || e.Fields["reason"] != "required" {
			t.Fatalf("err = %v", err)
		}

		_, err = svc.Terminate(ctx, principalWith("leases.terminate"), 1,
			TerminateInput{Reason: strings.Repeat("x", maxReason+1)}, ClientMeta{})
		if !errors.As(err, &e) || !strings.Contains(e.Fields["reason"], "at most 255") {
			t.Fatalf("err = %v", err)
		}

		_, err = svc.Terminate(ctx, principalWith("leases.terminate"), 1,
			TerminateInput{Reason: "rent\x00arrears"}, ClientMeta{})
		if !errors.As(err, &e) || !strings.Contains(e.Fields["reason"], "control") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("renew", func(t *testing.T) {
		cases := []struct {
			input RenewInput
			field string
		}{
			{RenewInput{Months: ptrInt(minMonths - 1)}, "months"},
			{RenewInput{Months: ptrInt(maxMonths + 1)}, "months"},
			{RenewInput{StartDate: ptrString("spring 2027")}, "start_date"},
			{RenewInput{EndDate: ptrString("31-12-2026")}, "end_date"},
			{RenewInput{SignedAt: ptrString("soon")}, "signed_at"},
		}
		for _, tc := range cases {
			_, err := svc.Renew(ctx, principalWith("leases.renew"), 1, tc.input, ClientMeta{})
			var e *httpx.Err
			if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity || e.Fields[tc.field] == "" {
				t.Fatalf("input %+v err = %v", tc.input, err)
			}
		}
	})

	t.Run("replace tenants", func(t *testing.T) {
		_, err := svc.ReplaceTenants(ctx, principalWith("leases.update"), 1,
			ReplaceTenantsInput{Tenants: []TenantRef{{TenantID: 4, Role: "friend"}}}, ClientMeta{})
		var e *httpx.Err
		if !errors.As(err, &e) || e.Fields["tenants"] != tenantRoleMessage {
			t.Fatalf("err = %v", err)
		}
	})
}

func principalWith(perms ...string) *rbac.Principal {
	set := make(map[string]struct{}, len(perms))
	for _, perm := range perms {
		set[perm] = struct{}{}
	}
	return &rbac.Principal{UserID: 3, OrgID: 1, Perms: set}
}

func leaseRowFixture() *LeaseRow {
	return &LeaseRow{
		ID:               55,
		OrganizationID:   1,
		UnitID:           21,
		LeaseNumber:      "KD-L-2026-001",
		StartDate:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		EndDate:          sql.NullTime{Time: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Valid: true},
		RentAmount:       45000,
		DepositAmount:    90000,
		BillingDay:       1,
		DueDay:           5,
		NoticePeriodDays: 30,
		Status:           statusActive,
		SignedAt:         sql.NullTime{Time: time.Date(2025, 12, 20, 9, 0, 0, 0, time.UTC), Valid: true},
		CreatedBy:        sql.NullInt64{Int64: 3, Valid: true},
		CreatedAt:        time.Date(2025, 12, 20, 9, 30, 0, 0, time.UTC),
		UpdatedAt:        time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC),
	}
}

func leaseViewFixture() *LeaseViewRow {
	return &LeaseViewRow{
		LeaseRow:      *leaseRowFixture(),
		UnitNumber:    "101",
		PropertyID:    4,
		PropertyName:  "Kilimani Heights",
		PropertyCode:  sql.NullString{String: "KD-001", Valid: true},
		PrimaryTenant: sql.NullString{String: "Jane Wanjiku", Valid: true},
	}
}

func ptrInt(value int) *int           { return &value }
func ptrFloat(value float64) *float64 { return &value }
func ptrString(value string) *string  { return &value }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
