package users

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

	"kodipms/internal/platform/cache"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/platform/pii"
	"kodipms/internal/rbac"
)

func TestBuildUserCreate(t *testing.T) {
	t.Run("applies defaults and normalizes", func(t *testing.T) {
		fields := map[string]string{}
		write := buildUserCreate(5, CreateInput{
			Email:     "  Jane.Example@ACME.COM ",
			Phone:     " +254712345678 ",
			FirstName: " Jane ",
			LastName:  " Wanjiku ",
			Password:  "s3cure-passphrase",
		}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected validation errors: %v", fields)
		}
		if write.OrganizationID != 5 {
			t.Fatalf("org = %d, want 5", write.OrganizationID)
		}
		if write.Email != "jane.example@acme.com" {
			t.Fatalf("email = %q", write.Email)
		}
		if write.Phone.String != "+254712345678" || !write.Phone.Valid {
			t.Fatalf("phone = %+v", write.Phone)
		}
		if write.FirstName != "Jane" || write.LastName != "Wanjiku" {
			t.Fatalf("names = %q %q", write.FirstName, write.LastName)
		}
		if write.Status != statusActive {
			t.Fatalf("status = %q, want active", write.Status)
		}
		if !write.MustChangePassword {
			t.Fatal("administrator-created accounts must change their password")
		}
	})

	t.Run("explicit must_change_password false is honoured", func(t *testing.T) {
		no := false
		fields := map[string]string{}
		write := buildUserCreate(1, CreateInput{
			Email: "a@b.com", FirstName: "A", LastName: "B",
			Password: "s3cure-passphrase", MustChangePassword: &no,
		}, fields)
		if len(fields) != 0 || write.MustChangePassword {
			t.Fatalf("write = %+v fields = %v", write, fields)
		}
	})

	t.Run("requires core fields", func(t *testing.T) {
		fields := map[string]string{}
		_ = buildUserCreate(1, CreateInput{}, fields)
		for field, want := range map[string]string{
			"first_name": "required",
			"last_name":  "required",
			"email":      "required",
			"password":   "required",
		} {
			if fields[field] != want {
				t.Errorf("%s = %q, want %q", field, fields[field], want)
			}
		}
	})

	base := func() CreateInput {
		return CreateInput{
			Email: "jane@example.com", FirstName: "Jane", LastName: "Wanjiku",
			Password: "s3cure-passphrase",
		}
	}
	cases := []struct {
		name    string
		mutate  func(*CreateInput)
		field   string
		message string
	}{
		{"long name", func(in *CreateInput) { in.FirstName = strings.Repeat("a", maxNameLength+1) }, "first_name", "100"},
		{"long email", func(in *CreateInput) { in.Email = strings.Repeat("a", maxEmail) + "@example.com" }, "email", "255"},
		{"not an email", func(in *CreateInput) { in.Email = "nope" }, "email", "valid email"},
		{"display name is not allowed", func(in *CreateInput) { in.Email = `Jane <jane@example.com>` }, "email", "valid email"},
		{"email with trailing junk", func(in *CreateInput) { in.Email = "jane@example.com jane" }, "email", "valid email"},
		{"phone without plus", func(in *CreateInput) { in.Phone = "0712345678" }, "phone", "E.164"},
		{"phone too short", func(in *CreateInput) { in.Phone = "+25412" }, "phone", "E.164"},
		{"phone too long", func(in *CreateInput) { in.Phone = "+254" + strings.Repeat("7", maxPhone) }, "phone", "E.164"},
		{"short password", func(in *CreateInput) { in.Password = "abc123" }, "password", "at least 8"},
		{"common password", func(in *CreateInput) { in.Password = "password123" }, "password", "too common"},
		{"whitespace password", func(in *CreateInput) { in.Password = "         " }, "password", "whitespace"},
		{"bad status", func(in *CreateInput) { in.Status = "archived" }, "status", "suspended"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.mutate(&in)
			fields := map[string]string{}
			_ = buildUserCreate(1, in, fields)
			if fields[tc.field] == "" {
				t.Fatalf("missing error for %s: %v", tc.field, fields)
			}
			if !strings.Contains(fields[tc.field], tc.message) {
				t.Fatalf("%s = %q, want substring %q", tc.field, fields[tc.field], tc.message)
			}
		})
	}

	t.Run("roles are required", func(t *testing.T) {
		fields := map[string]string{}
		_ = buildUserCreate(1, base(), fields)
		ids := normalizeIDs(nil, fields)
		if len(ids) != 0 || fields["role_ids"] != "" {
			t.Fatalf("normalizeIDs(nil) = %v, fields = %v", ids, fields)
		}
	})
}

func TestApplyUserPatch(t *testing.T) {
	t.Run("empty patch is a no-op", func(t *testing.T) {
		row := userRowFixture()
		fields := map[string]string{}
		write, patch := applyUserPatch(row, UpdateInput{}, fields)
		if len(fields) != 0 || patch.dirty() || patch.fieldsChanged {
			t.Fatalf("empty patch changed state: fields=%v dirty=%v changed=%v", fields, patch.dirty(), patch.fieldsChanged)
		}
		if write.Email != row.Email || write.Status != row.Status || write.ID != row.ID {
			t.Fatalf("write = %+v", write)
		}
		if uint64(row.OrganizationID.Int64) != write.OrganizationID {
			t.Fatalf("org = %d, want %d", write.OrganizationID, row.OrganizationID.Int64)
		}
	})

	t.Run("blank strings leave required text untouched", func(t *testing.T) {
		row := userRowFixture()
		fields := map[string]string{}
		write, patch := applyUserPatch(row, UpdateInput{Email: "   ", FirstName: "", LastName: "  "}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if patch.fieldsChanged || write.FirstName != "Jane" || write.Email != "jane@example.com" {
			t.Fatalf("write = %+v changed = %v", write, patch.fieldsChanged)
		}
	})

	t.Run("changes are recorded for audit", func(t *testing.T) {
		row := userRowFixture()
		next := "Jane W"
		fields := map[string]string{}
		write, patch := applyUserPatch(row, UpdateInput{
			Email:     "JOHN@EXAMPLE.COM",
			LastName:  next,
			FirstName: "John",
		}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if write.Email != "john@example.com" || write.LastName != "Jane W" {
			t.Fatalf("write = %+v", write)
		}
		if patch.old["email"] != "jane@example.com" || patch.new["email"] != "john@example.com" {
			t.Fatalf("email audit = %v -> %v", patch.old["email"], patch.new["email"])
		}
		if patch.old["last_name"] != "Wanjiku" || patch.new["last_name"] != "Jane W" {
			t.Fatalf("last_name audit = %v -> %v", patch.old["last_name"], patch.new["last_name"])
		}
		if !patch.fieldsChanged || !patch.dirty() {
			t.Fatal("patch should be marked dirty")
		}
	})

	t.Run("phone pointer clears, blank value clears the column", func(t *testing.T) {
		row := userRowFixture()
		empty := ""
		fields := map[string]string{}
		write, patch := applyUserPatch(row, UpdateInput{Phone: &empty}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if write.Phone.Valid {
			t.Fatalf("phone = %+v, want cleared", write.Phone)
		}
		if patch.old["phone"] != "+254712345678" || patch.new["phone"] != nil {
			t.Fatalf("phone audit = %v -> %v", patch.old["phone"], patch.new["phone"])
		}
	})

	t.Run("invalid phone keeps the old value", func(t *testing.T) {
		row := userRowFixture()
		bad := "0712345678"
		fields := map[string]string{}
		write, patch := applyUserPatch(row, UpdateInput{Phone: &bad}, fields)
		if fields["phone"] == "" {
			t.Fatalf("missing phone error: %v", fields)
		}
		if write.Phone.String != "+254712345678" || patch.new["phone"] != nil {
			t.Fatalf("write = %+v audit = %v", write, patch.new)
		}
	})

	t.Run("status", func(t *testing.T) {
		row := userRowFixture()
		suspended := " Suspended "
		fields := map[string]string{}
		write, patch := applyUserPatch(row, UpdateInput{Status: &suspended}, fields)
		if !patch.statusSet || write.Status != statusSuspended {
			t.Fatalf("status = %q set = %v", write.Status, patch.statusSet)
		}
		if patch.old["status"] != statusActive || patch.new["status"] != statusSuspended {
			t.Fatalf("status audit = %v -> %v", patch.old["status"], patch.new["status"])
		}

		row = userRowFixture()
		archived := "archived"
		fields = map[string]string{}
		_, patch = applyUserPatch(row, UpdateInput{Status: &archived}, fields)
		if fields["status"] != statusMessage {
			t.Fatalf("status = %q", fields["status"])
		}
		if !patch.statusSet {
			t.Fatal("an attempted status change still counts as set")
		}

		row = userRowFixture()
		blank := " "
		fields = map[string]string{}
		applyUserPatch(row, UpdateInput{Status: &blank}, fields)
		if fields["status"] != "required" {
			t.Fatalf("status = %q, want required", fields["status"])
		}
	})

	t.Run("password sets must_change_password", func(t *testing.T) {
		row := userRowFixture()
		row.MustChangePassword = false
		pass := "s3cure-passphrase"
		fields := map[string]string{}
		write, patch := applyUserPatch(row, UpdateInput{Password: &pass}, fields)
		if !patch.passwordSet {
			t.Fatal("passwordSet = false")
		}
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if !write.MustChangePassword || patch.new["must_change_password"] != true {
			t.Fatalf("write = %+v audit = %v", write, patch.new)
		}
	})

	t.Run("weak password is rejected", func(t *testing.T) {
		row := userRowFixture()
		pass := "abc123"
		fields := map[string]string{}
		_, patch := applyUserPatch(row, UpdateInput{Password: &pass}, fields)
		if !strings.Contains(fields["password"], "at least 8") {
			t.Fatalf("password = %q", fields["password"])
		}
		if !patch.passwordSet {
			t.Fatal("passwordSet should be true")
		}
	})

	t.Run("national id raw handling", func(t *testing.T) {
		row := userRowFixture()
		fields := map[string]string{}
		_, patch := applyUserPatch(row, UpdateInput{NationalID: json.RawMessage(`"12345678"`)}, fields)
		if !patch.nationalSet || patch.nationalRaw != "12345678" {
			t.Fatalf("patch = %+v", patch)
		}
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}

		fields = map[string]string{}
		_, patch = applyUserPatch(row, UpdateInput{NationalID: json.RawMessage(`null`)}, fields)
		if !patch.nationalSet || patch.nationalRaw != "" {
			t.Fatalf("null should clear: %+v", patch)
		}

		fields = map[string]string{}
		_, patch = applyUserPatch(row, UpdateInput{NationalID: json.RawMessage(`12345678`)}, fields)
		if fields["national_id"] != "must be a string or null" {
			t.Fatalf("national_id = %q", fields["national_id"])
		}

		fields = map[string]string{}
		_, patch = applyUserPatch(row, UpdateInput{}, fields)
		if patch.nationalSet {
			t.Fatal("omitted national id must not be treated as a change")
		}
	})

	t.Run("role ids", func(t *testing.T) {
		row := userRowFixture()
		ids := []uint64{3, 1, 3}
		fields := map[string]string{}
		_, patch := applyUserPatch(row, UpdateInput{RoleIDs: &ids}, fields)
		if !patch.rolesSet || !reflect.DeepEqual(patch.roleIDs, []uint64{3, 1}) {
			t.Fatalf("patch = %+v", patch)
		}

		empty := []uint64{}
		fields = map[string]string{}
		_, patch = applyUserPatch(row, UpdateInput{RoleIDs: &empty}, fields)
		if fields["role_ids"] != "must contain at least one role" {
			t.Fatalf("role_ids = %q", fields["role_ids"])
		}

		zero := []uint64{0}
		fields = map[string]string{}
		applyUserPatch(row, UpdateInput{RoleIDs: &zero}, fields)
		if fields["role_ids"] != "must contain positive identifiers" {
			t.Fatalf("role_ids = %q", fields["role_ids"])
		}

		fields = map[string]string{}
		_, patch = applyUserPatch(row, UpdateInput{}, fields)
		if patch.rolesSet {
			t.Fatal("omitted role_ids must not be treated as a change")
		}
	})

	t.Run("must_change_password toggle", func(t *testing.T) {
		row := userRowFixture()
		no := false
		fields := map[string]string{}
		write, patch := applyUserPatch(row, UpdateInput{MustChangePassword: &no}, fields)
		if len(fields) != 0 || write.MustChangePassword {
			t.Fatalf("write = %+v fields = %v", write, fields)
		}
		if patch.old["must_change_password"] != true || patch.new["must_change_password"] != false {
			t.Fatalf("audit = %v -> %v", patch.old["must_change_password"], patch.new["must_change_password"])
		}
	})
}

func TestPrepareNationalID(t *testing.T) {
	cipher := testCipher(t)
	svc := NewService(nil, nil, nil, cipher, discardLogger())

	t.Run("blank is absent, not invalid", func(t *testing.T) {
		fields := map[string]string{}
		values, err := svc.prepareNationalID("   ", fields)
		if err != nil {
			t.Fatal(err)
		}
		if len(fields) != 0 || values.hasValue() {
			t.Fatalf("fields = %v values = %+v", fields, values)
		}
	})

	t.Run("separators are stripped before validating", func(t *testing.T) {
		fields := map[string]string{}
		values, err := svc.prepareNationalID(" 12-34 5678 ", fields)
		if err != nil {
			t.Fatal(err)
		}
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		if !values.hasValue() || values.last4 != "5678" || len(values.ciphertext) == 0 || len(values.hash) != 32 {
			t.Fatalf("values = %+v", values)
		}
		plain, err := cipher.Decrypt(values.ciphertext)
		if err != nil {
			t.Fatal(err)
		}
		if plain != "12-34 5678" {
			t.Fatalf("round trip = %q", plain)
		}
	})

	t.Run("bad lengths and characters", func(t *testing.T) {
		for _, value := range []string{"12345", strings.Repeat("9", maxNationalIDLen+1), "1234567a"} {
			fields := map[string]string{}
			values, err := svc.prepareNationalID(value, fields)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(fields["national_id"], "digits") {
				t.Fatalf("%q -> fields = %v", value, fields)
			}
			if values.hasValue() {
				t.Fatalf("%q should not produce a value", value)
			}
		}
	})

	t.Run("missing cipher is an internal error", func(t *testing.T) {
		noCipher := NewService(nil, nil, nil, nil, discardLogger())
		fields := map[string]string{}
		if _, err := noCipher.prepareNationalID("12345678", fields); err == nil {
			t.Fatal("expected an error without a cipher")
		}
	})

	t.Run("duplicate detection uses the hash", func(t *testing.T) {
		first, err := svc.prepareNationalID("12345678", map[string]string{})
		if err != nil {
			t.Fatal(err)
		}
		second, err := svc.prepareNationalID("12-34-5678", map[string]string{})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first.hash, second.hash) {
			t.Fatal("normalised values must hash identically")
		}
		other, err := svc.prepareNationalID("12345679", map[string]string{})
		if err != nil {
			t.Fatal(err)
		}
		if reflect.DeepEqual(first.hash, other.hash) {
			t.Fatal("different values must hash differently")
		}
	})
}

func TestNationalIDValuesNilSafe(t *testing.T) {
	var values *NationalIDValues
	if values.hasValue() {
		t.Fatal("nil values should be empty")
	}
	if values.cipherArg() != nil || values.hashArg() != nil || values.last4Arg() != nil {
		t.Fatal("nil values must bind SQL NULL")
	}
	if writeAuditNational(values) != nil || nullableLast4(values) != nil {
		t.Fatal("nil values must audit as null")
	}
	empty := &NationalIDValues{}
	if empty.hasValue() {
		t.Fatal("empty values should be treated as absent")
	}
	full := &NationalIDValues{ciphertext: []byte{1}, hash: []byte{2}, last4: "5678"}
	if !full.hasValue() || full.cipherArg() == nil || nullableLast4(full) != "5678" {
		t.Fatalf("full = %+v", full)
	}
}

func TestNormalizeAssignments(t *testing.T) {
	t.Run("defaults the role and dedupes properties", func(t *testing.T) {
		fields := map[string]string{}
		out, ids := normalizeAssignments([]AssignmentInput{
			{PropertyID: 3, Role: " CARETAKER "},
			{PropertyID: 1},
			{PropertyID: 3, Role: "property_manager"},
		}, fields)
		if len(fields) != 0 {
			t.Fatalf("unexpected errors: %v", fields)
		}
		want := []AssignmentInput{{PropertyID: 3, Role: assignCaretaker}, {PropertyID: 1, Role: assignManager}}
		if !reflect.DeepEqual(out, want) {
			t.Fatalf("assignments = %+v, want %+v", out, want)
		}
		if !reflect.DeepEqual(ids, []uint64{3, 1}) {
			t.Fatalf("ids = %v", ids)
		}
	})

	t.Run("per-entry errors", func(t *testing.T) {
		fields := map[string]string{}
		out, ids := normalizeAssignments([]AssignmentInput{
			{PropertyID: 0, Role: assignManager},
			{PropertyID: 2, Role: "gardener"},
		}, fields)
		if len(out) != 0 || len(ids) != 0 {
			t.Fatalf("out = %+v ids = %v", out, ids)
		}
		if fields["assignments[0].property_id"] != "required" {
			t.Fatalf("property_id = %q", fields["assignments[0].property_id"])
		}
		if fields["assignments[1].role"] != assignmentMessage {
			t.Fatalf("role = %q", fields["assignments[1].role"])
		}
	})

	t.Run("bulk limit", func(t *testing.T) {
		many := make([]AssignmentInput, maxAssignments+1)
		for i := range many {
			many[i] = AssignmentInput{PropertyID: uint64(i + 1)}
		}
		fields := map[string]string{}
		out, _ := normalizeAssignments(many, fields)
		if out != nil {
			t.Fatalf("expected nil assignments, got %+v", out)
		}
		if !strings.Contains(fields["assignments"], "at most 100") {
			t.Fatalf("assignments = %q", fields["assignments"])
		}
	})
}

func TestSameIDsAndAssignments(t *testing.T) {
	if !sameIDs(nil, nil) || sameIDs([]uint64{1}, []uint64{1, 2}) || sameIDs([]uint64{1, 2}, []uint64{2, 3}) {
		t.Fatal("sameIDs disagrees with set equality")
	}
	if !sameIDs([]uint64{1, 2}, []uint64{2, 1}) {
		t.Fatal("sameIDs should ignore order")
	}

	rows := []AssignmentRow{{PropertyID: 1, AssignmentRole: assignManager}, {PropertyID: 2, AssignmentRole: assignCaretaker}}
	if !sameAssignments(rows, []AssignmentInput{{PropertyID: 2, Role: assignCaretaker}, {PropertyID: 1, Role: assignManager}}) {
		t.Fatal("equal assignment sets should match")
	}
	if sameAssignments(rows, []AssignmentInput{{PropertyID: 1, Role: assignCaretaker}}) {
		t.Fatal("different sizes should not match")
	}
	if sameAssignments(rows, []AssignmentInput{{PropertyID: 1, Role: assignManager}, {PropertyID: 2, Role: assignManager}}) {
		t.Fatal("role changes should not match")
	}
	if sameAssignments(nil, nil) == false {
		t.Fatal("two empty sets should match")
	}
}

func TestRoleHelpers(t *testing.T) {
	rows := []UserRoleRow{
		{UserID: 1, RoleID: 2, RoleName: "property_manager"},
		{UserID: 1, RoleID: 5, RoleName: "caretaker"},
	}
	if !reflect.DeepEqual(roleIDsOf(rows), []uint64{2, 5}) {
		t.Fatalf("roleIDsOf = %v", roleIDsOf(rows))
	}
	if !reflect.DeepEqual(roleNamesOf(rows), []string{"property_manager", "caretaker"}) {
		t.Fatalf("roleNamesOf = %v", roleNamesOf(rows))
	}
	names := roleNames([]RoleRow{{Name: roleOrgAdmin}, {Name: "landlord"}})
	if !roleNamesContain(names, roleOrgAdmin) || roleNamesContain(names, "tenant") {
		t.Fatalf("roleNamesContain = %v", names)
	}
	if !restrictedRole(roleOrgAdmin) || !restrictedRole(roleSystemAdmin) || restrictedRole("accountant") {
		t.Fatal("only administrator roles are restricted")
	}
	if !reflect.DeepEqual(assignmentKeys(rows2Assignments()), []string{"1:property_manager", "2:caretaker"}) {
		t.Fatalf("assignmentKeys = %v", assignmentKeys(rows2Assignments()))
	}
	if !reflect.DeepEqual(inputKeys([]AssignmentInput{{PropertyID: 4, Role: assignCaretaker}}), []string{"4:caretaker"}) {
		t.Fatalf("inputKeys = %v", inputKeys([]AssignmentInput{{PropertyID: 4, Role: assignCaretaker}}))
	}
}

func rows2Assignments() []AssignmentRow {
	return []AssignmentRow{
		{PropertyID: 1, AssignmentRole: assignManager},
		{PropertyID: 2, AssignmentRole: assignCaretaker},
	}
}

func TestTextHelpers(t *testing.T) {
	if got := fullName("Jane", "Wanjiku"); got != "Jane Wanjiku" {
		t.Fatalf("fullName = %q", got)
	}
	if fullName("", "Wanjiku") != "Wanjiku" {
		t.Fatalf("fullName = %q", fullName("", "Wanjiku"))
	}
	if textOf(sql.NullString{}) != "" || textOf(sql.NullString{String: "x", Valid: true}) != "x" {
		t.Fatal("textOf mismatch")
	}
	if nullableValue(sql.NullString{}) != nil || nullableValue(sql.NullString{String: "x", Valid: true}) != "x" {
		t.Fatal("nullableValue mismatch")
	}
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("EAT", 3*3600))
	got := timePointer(sql.NullTime{Time: at, Valid: true})
	if got == nil || !got.Equal(at) || got.Location() != time.UTC {
		t.Fatalf("timePointer = %v", got)
	}
	if timePointer(sql.NullTime{}) != nil {
		t.Fatal("null time should map to nil")
	}
	if isJSONNull(nil) || !isJSONNull(json.RawMessage(" null ")) {
		t.Fatal("isJSONNull mismatch")
	}
}

func TestUserDTOs(t *testing.T) {
	row := userRowFixture()
	roles := []RoleDTO{{ID: 2, Name: "property_manager"}}
	summary := userSummaryDTO(row, roles)
	if summary.ID != 7 || summary.FullName != "Jane Wanjiku" || summary.Email != "jane@example.com" {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.Phone != "+254712345678" || summary.Status != statusActive {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.LastLoginAt == nil || !summary.LastLoginAt.Equal(row.LastLoginAt.Time) {
		t.Fatalf("last login = %v", summary.LastLoginAt)
	}
	if !summary.CreatedAt.Equal(row.CreatedAt) {
		t.Fatalf("created = %v", summary.CreatedAt)
	}

	withRoles := userDetailDTO(row, roles)
	if withRoles.NationalIDLast4 != "5678" {
		t.Fatalf("national id = %q, want masked last4", withRoles.NationalIDLast4)
	}
	if len(withRoles.Roles) != 1 || withRoles.Roles[0].Name != "property_manager" {
		t.Fatalf("roles = %+v", withRoles.Roles)
	}
	if withRoles.EmailVerifiedAt == nil || withRoles.PhoneVerifiedAt != nil {
		t.Fatalf("verified = %v / %v", withRoles.EmailVerifiedAt, withRoles.PhoneVerifiedAt)
	}

	bare := userSummaryDTO(row, nil)
	if bare.Roles == nil || len(bare.Roles) != 0 {
		t.Fatalf("roles = %#v, want empty slice", bare.Roles)
	}

	assignment := assignmentDTO(&AssignmentRow{
		PropertyID: 4, PropertyName: "Kilimani Heights",
		PropertyCode:   sql.NullString{String: "KD-001", Valid: true},
		AssignmentRole: assignCaretaker,
		AssignedAt:     time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
	})
	if assignment.PropertyCode != "KD-001" || assignment.Role != assignCaretaker || assignment.AssignedAt.IsZero() {
		t.Fatalf("assignment = %+v", assignment)
	}
	if dto := roleDTO(3, "landlord", sql.NullString{}); dto.Description != "" || dto.Name != "landlord" {
		t.Fatalf("role dto = %+v", dto)
	}
}

func TestUserDetailJSON(t *testing.T) {
	row := userRowFixture()
	row.NationalIDLast4 = sql.NullString{}
	row.LastLoginAt = sql.NullTime{}
	b, err := json.Marshal(userDetailDTO(row, []RoleDTO{{ID: 1, Name: roleOrgAdmin}}))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "full_name", "email", "status", "roles", "must_change_password", "created_at"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing key %q in %s", key, b)
		}
	}
	if _, ok := decoded["UserSummaryDTO"]; ok {
		t.Fatalf("embedded struct was not flattened: %s", b)
	}
	if _, ok := decoded["national_id_last4"]; ok {
		t.Fatalf("empty national id should be omitted: %s", b)
	}
	if decoded["last_login_at"] != nil {
		t.Fatalf("null last login = %v", decoded["last_login_at"])
	}
	for _, secret := range []string{"password", "password_hash", "pin_hash", "national_id_enc", "national_id_hash", "token_version"} {
		if _, ok := decoded[secret]; ok {
			t.Fatalf("secret field %q leaked into the response", secret)
		}
	}
}

func TestListValidation(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, discardLogger())
	p := principalWith("users.view")
	cases := []struct {
		name  string
		query ListQuery
		field string
	}{
		{"long search", ListQuery{Search: strings.Repeat("a", maxSearch+1)}, "q"},
		{"bad status", ListQuery{Status: "archived"}, "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.List(context.Background(), p, tc.query)
			var e *httpx.Err
			if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity {
				t.Fatalf("err = %v, want validation", err)
			}
			if e.Fields[tc.field] == "" {
				t.Fatalf("missing %s in %v", tc.field, e.Fields)
			}
		})
	}
}

func TestServiceAuthorization(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, discardLogger())
	ctx := context.Background()
	noPerms := principalWith()
	noOrg := &rbac.Principal{Perms: map[string]struct{}{"users.view": {}}}
	cases := []struct {
		name   string
		status int
		call   func() error
	}{
		{"list without principal", 401, func() error { _, err := svc.List(ctx, nil, ListQuery{}); return err }},
		{"list without org", 401, func() error { _, err := svc.List(ctx, noOrg, ListQuery{}); return err }},
		{"list without permission", 403, func() error { _, err := svc.List(ctx, noPerms, ListQuery{}); return err }},
		{"get without permission", 403, func() error { _, err := svc.Get(ctx, noPerms, 1); return err }},
		{"lookups without permission", 403, func() error { _, err := svc.Lookups(ctx, noPerms); return err }},
		{"create without permission", 403, func() error {
			_, err := svc.Create(ctx, noPerms, CreateInput{}, ClientMeta{})
			return err
		}},
		{"update without permission", 403, func() error { _, err := svc.Update(ctx, noPerms, 1, UpdateInput{}, ClientMeta{}); return err }},
		{"delete without permission", 403, func() error { return svc.Delete(ctx, noPerms, 1, ClientMeta{}) }},
		{"assignments without permission", 403, func() error { _, err := svc.Assignments(ctx, noPerms, 1); return err }},
		{"replace assignments without permission", 403, func() error {
			_, err := svc.ReplaceAssignments(ctx, noPerms, 1, ReplaceAssignmentsInput{}, ClientMeta{})
			return err
		}},
		{"get missing id", 404, func() error { _, err := svc.Get(ctx, principalWith("users.view"), 0); return err }},
		{"delete missing id", 404, func() error { return svc.Delete(ctx, principalWith("users.delete"), 0, ClientMeta{}) }},
		{"delete own account", 409, func() error {
			p := &rbac.Principal{UserID: 7, OrgID: 1, Perms: map[string]struct{}{"users.delete": {}}}
			return svc.Delete(ctx, p, 7, ClientMeta{})
		}},
		{"replace assignments missing id", 404, func() error {
			_, err := svc.ReplaceAssignments(ctx, principalWith("users.update"), 0, ReplaceAssignmentsInput{}, ClientMeta{})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var e *httpx.Err
			err := tc.call()
			if !errors.As(err, &e) || e.Status != tc.status {
				t.Fatalf("err = %v, want status %d", err, tc.status)
			}
		})
	}
}

func TestCreateValidationStopsBeforeDatabase(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, discardLogger())
	p := principalWith("users.create")
	_, err := svc.Create(context.Background(), p, CreateInput{Email: "not-an-email"}, ClientMeta{})
	var e *httpx.Err
	if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity {
		t.Fatalf("err = %v, want validation", err)
	}
	for _, field := range []string{"email", "first_name", "last_name", "password", "role_ids"} {
		if e.Fields[field] == "" {
			t.Errorf("missing %s in %v", field, e.Fields)
		}
	}

	t.Run("valid profile without roles", func(t *testing.T) {
		_, err := svc.Create(context.Background(), p, CreateInput{
			Email: "jane@example.com", FirstName: "Jane", LastName: "Wanjiku",
			Password: "s3cure-passphrase",
		}, ClientMeta{})
		var e *httpx.Err
		if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity {
			t.Fatalf("err = %v, want validation", err)
		}
		if e.Fields["role_ids"] != "at least one role is required" {
			t.Fatalf("role_ids = %q", e.Fields["role_ids"])
		}
	})
}

func TestReplaceAssignmentsValidation(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, discardLogger())
	p := principalWith("users.update")
	_, err := svc.ReplaceAssignments(context.Background(), p, 1, ReplaceAssignmentsInput{
		Assignments: []AssignmentInput{{PropertyID: 3, Role: "gardener"}},
	}, ClientMeta{})
	var e *httpx.Err
	if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity {
		t.Fatalf("err = %v, want validation", err)
	}
	if e.Fields["assignments[0].role"] != assignmentMessage {
		t.Fatalf("fields = %v", e.Fields)
	}
}

func TestCachedRolesUsesStoreFirst(t *testing.T) {
	store := newMemStore()
	svc := NewService(nil, store, nil, nil, discardLogger())
	store.values[rolesCacheKeyBase+"12"] = `[{"id":4,"name":"accountant","description":"Books"}]`

	roles, err := svc.cachedRoles(context.Background(), 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 1 || roles[0].ID != 4 || roles[0].Description != "Books" {
		t.Fatalf("roles = %+v", roles)
	}
	if _, err := store.Get(context.Background(), "missing"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("memStore miss = %v", err)
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

func TestValidStatusAndPhone(t *testing.T) {
	for _, status := range userStatuses {
		if !validStatus(status) {
			t.Errorf("status %q should be valid", status)
		}
	}
	if validStatus("Active") || validStatus("") || validStatus("archived") {
		t.Fatal("unknown statuses should be rejected")
	}

	fields := map[string]string{}
	if got := phoneValue("", fields); got.Valid || len(fields) != 0 {
		t.Fatalf("blank phone = %+v fields = %v", got, fields)
	}
	if got := phoneValue("+254712345678", fields); !got.Valid || got.String != "+254712345678" {
		t.Fatalf("phone = %+v", got)
	}
}

func TestNormalizeIDsDedupes(t *testing.T) {
	fields := map[string]string{}
	if got := normalizeIDs([]uint64{2, 2, 1}, fields); !reflect.DeepEqual(got, []uint64{2, 1}) {
		t.Fatalf("ids = %v", got)
	}
	if len(fields) != 0 {
		t.Fatalf("unexpected errors: %v", fields)
	}
}

func testCipher(t *testing.T) *pii.Cipher {
	t.Helper()
	cipher, err := pii.NewFromKey(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func principalWith(perms ...string) *rbac.Principal {
	set := make(map[string]struct{}, len(perms))
	for _, perm := range perms {
		set[perm] = struct{}{}
	}
	return &rbac.Principal{UserID: 1, OrgID: 1, Perms: set}
}

type memStore struct {
	values map[string]string
}

func newMemStore() *memStore {
	return &memStore{values: make(map[string]string)}
}

func (s *memStore) Get(_ context.Context, key string) (string, error) {
	value, ok := s.values[key]
	if !ok {
		return "", cache.ErrMiss
	}
	return value, nil
}

func (s *memStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	s.values[key] = value
	return nil
}

func (s *memStore) Del(_ context.Context, keys ...string) error {
	for _, key := range keys {
		delete(s.values, key)
	}
	return nil
}

func (s *memStore) Incr(context.Context, string) (int64, error) { return 0, nil }

func (s *memStore) IncrTTL(context.Context, string, time.Duration) (int64, error) {
	return 0, nil
}

func (s *memStore) Expire(context.Context, string, time.Duration) error { return nil }

func (s *memStore) Close() error { return nil }

func userRowFixture() *UserRow {
	return &UserRow{
		ID:                 7,
		OrganizationID:     sql.NullInt64{Int64: 1, Valid: true},
		Email:              "jane@example.com",
		Phone:              sql.NullString{String: "+254712345678", Valid: true},
		FirstName:          "Jane",
		LastName:           "Wanjiku",
		NationalIDLast4:    sql.NullString{String: "5678", Valid: true},
		Status:             statusActive,
		MustChangePassword: true,
		EmailVerifiedAt:    sql.NullTime{Time: time.Date(2026, 1, 5, 6, 0, 0, 0, time.UTC), Valid: true},
		LastLoginAt:        sql.NullTime{Time: time.Date(2026, 2, 1, 9, 30, 0, 0, time.UTC), Valid: true},
		CreatedAt:          time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		UpdatedAt:          time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
	}
}
