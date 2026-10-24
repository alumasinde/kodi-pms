package mpesa

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"kodipms/internal/platform/config"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/platform/pii"
	"kodipms/internal/rbac"
)

func day(year int, month time.Month, d int) time.Time {
	return time.Date(year, month, d, 0, 0, 0, 0, time.UTC)
}

func ptrString(value string) *string  { return &value }
func ptrFloat(value float64) *float64 { return &value }
func ptrUint(value uint64) *uint64    { return &value }

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestService is the module without its outside world: no database, no Daraja,
// no billing. Everything tested here is a decision the code makes before it asks
// either of them a question.
func newTestService() *Service {
	return NewService(nil, nil, nil, &config.Config{}, nil, nil, discardLog())
}

func newCipherService(t *testing.T) *Service {
	t.Helper()
	cipher, err := pii.NewFromKey(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewFromKey: %v", err)
	}
	s := newTestService()
	s.cipher = cipher
	return s
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
	viewer := &rbac.Principal{UserID: 3, OrgID: 1, Perms: map[string]struct{}{"mpesa.view": {}}}
	if err := authorize(viewer, "mpesa.view"); err != nil {
		t.Fatalf("err = %v, want the granted permission to pass", err)
	}
	for _, perm := range []string{"mpesa.manage", "mpesa.push", "mpesa.reconcile"} {
		if err := authorize(viewer, perm); !errors.Is(err, httpx.ErrForbidden) {
			t.Fatalf("%s: err = %v, want forbidden for a permission the caller does not hold", perm, err)
		}
	}
	if err := authorize(&rbac.Principal{}, "mpesa.view"); !errors.Is(err, httpx.ErrUnauthorized) {
		t.Fatalf("err = %v, want unauthorized without an organization", err)
	}
	if err := authorize(nil, "mpesa.view"); !errors.Is(err, httpx.ErrUnauthorized) {
		t.Fatalf("err = %v, want unauthorized without a principal", err)
	}
}

// TestScalarAcceptsEveryShape covers the reason callbacks are not decoded by the
// API's strict decoder: the same field is quoted in one channel and bare in
// another, and a decode that fails is a payment that never gets recorded.
func TestScalarAcceptsEveryShape(t *testing.T) {
	for _, tc := range []struct {
		name, json, want string
	}{
		{"quoted", `{"v":"100.00"}`, "100.00"},
		{"bare number", `{"v":100}`, "100"},
		{"decimal", `{"v":100.5}`, "100.5"},
		{"null", `{"v":null}`, ""},
		{"boolean", `{"v":true}`, "true"},
		{"empty string", `{"v":""}`, ""},
	} {
		var in struct{ V Scalar }
		if err := json.Unmarshal([]byte(tc.json), &in); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if in.V.String() != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, in.V.String(), tc.want)
		}
	}
}

func TestParseAmountAndFirstAmount(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  float64
		ok    bool
	}{
		{"100.00", 100, true},
		{" 1,234.50 ", 1234.50, true},
		{"1 000", 1000, true},
		{"0", 0, true},
		{"-15", -15, true},
		{"", 0, false},
		{"abc", 0, false},
		{"1e5", 0, false},
		{"NaN", 0, false},
		{"Inf", 0, false},
		{"1.2.3", 0, false},
	} {
		got, ok := parseAmount(tc.value)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("parseAmount(%q) = %v, %v want %v, %v", tc.value, got, ok, tc.want, tc.ok)
		}
	}

	t.Run("exponent and infinity text never become money", func(t *testing.T) {
		for _, bad := range []string{"1e400", "9e9", "+Inf", "-Inf"} {
			if _, ok := firstAmount(Scalar(bad)); ok {
				t.Fatalf("firstAmount(%q) accepted a value the column cannot hold", bad)
			}
		}
	})

	t.Run("the first readable spelling wins", func(t *testing.T) {
		if got, ok := firstAmount(Scalar(""), Scalar("2500.00"), Scalar("99")); !ok || got != 2500 {
			t.Fatalf("got %v, %v want 2500, true", got, ok)
		}
		if _, ok := firstAmount(); ok {
			t.Fatal("an empty list of spellings is not an amount")
		}
	})
}

func TestFirstTextSkipsBlanks(t *testing.T) {
	if got := firstText(Scalar(" "), Scalar(""), Scalar("400200"), Scalar("600000")); got != "400200" {
		t.Fatalf("got %q, want the first filled value", got)
	}
	if got := firstText(Scalar("a"), Scalar("b")); got != "a" {
		t.Fatalf("got %q, want a", got)
	}
	if got := firstText(Scalar(""), Scalar("")); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestChannelTimeReadsEveryDarajaShape(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        time.Time
	}{
		{"stk style", "20260102150405", time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)},
		{"epoch millis", "1767000000000", time.UnixMilli(1767000000000).UTC()},
		{"epoch seconds", "1767000000", time.Unix(1767000000, 0).UTC()},
		{"rfc3339", "2026-01-02T15:04:05Z", time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)},
		{"plain date", "2026-01-02", day(2026, time.January, 2)},
	} {
		got, ok := channelTime(tc.value)
		if !ok || !got.Equal(tc.want) {
			t.Fatalf("%s: got %v, %v want %v", tc.name, got, ok, tc.want)
		}
	}
	for _, bad := range []string{"", "   ", "not a date", "0", "-1"} {
		if _, ok := channelTime(bad); ok {
			t.Fatalf("channelTime(%q) = ok, want the caller to fall back to this instant", bad)
		}
	}
}

func TestParseDateAcceptsDateAndTimestamp(t *testing.T) {
	if got, ok := parseDate("2026-03-04"); !ok || !got.Equal(day(2026, time.March, 4)) {
		t.Fatalf("got %v, %v want midnight UTC on 2026-03-04", got, ok)
	}
	if got, ok := parseDate("2026-03-04T10:00:00+03:00"); !ok || !got.Equal(day(2026, time.March, 4).Add(7*time.Hour)) {
		t.Fatalf("got %v, %v want the same instant in UTC", got, ok)
	}
	for _, bad := range []string{"", "04/03/2026", "2026-13-40", "yesterday"} {
		if _, ok := parseDate(bad); ok {
			t.Fatalf("parseDate(%q) = ok, want a refusal", bad)
		}
	}
}

func TestNormalizeMsisdn(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"0712345678", "254712345678"},
		{"0112345678", "254112345678"},
		{"254712345678", "254712345678"},
		{"+254 712 345 678", "254712345678"},
		{"+254712345678", "254712345678"},
		{"712345678", "254712345678"},
		{"112345678", "254112345678"},
		{"912345678", "254912345678"},
		{" 0712-345-678 ", "254712345678"},
		{"(0712) 345 678", "254712345678"},
	} {
		got, err := normalizeMsisdn(tc.in)
		if err != nil {
			t.Fatalf("normalizeMsisdn(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeMsisdn(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"", "0712345", "07123456789", "25471234567", "256712345678", "0712 345 6O8", "plus254"} {
		if got, err := normalizeMsisdn(bad); err == nil {
			t.Fatalf("normalizeMsisdn(%q) = %q, want a refusal rather than a wrong number to charge", bad, got)
		}
	}
}

func TestDisplayPhone(t *testing.T) {
	if got := DisplayPhone("254712345678"); got != "+254712345678" {
		t.Fatalf("got %q, want the +254 form the API shows", got)
	}
	for _, other := range []string{"", "0712345678", "2547123456"} {
		if got := DisplayPhone(other); got != other {
			t.Fatalf("DisplayPhone(%q) = %q, want it left alone", other, got)
		}
	}
}

func TestRoundToCentsKeepsTheColumnPromise(t *testing.T) {
	if got := roundToCents(1234.567); got != 1234.57 {
		t.Fatalf("got %v, want 1234.57", got)
	}
	if got := roundToCents(0.01); got != 0.01 {
		t.Fatalf("got %v, want 0.01", got)
	}
	for _, bad := range []float64{math.NaN(), math.Inf(-1), math.Inf(1)} {
		if got := roundToCents(bad); got != 0 {
			t.Fatalf("roundToCents(%v) = %v, want 0 rather than a value the column cannot store", bad, got)
		}
	}
	if got := roundToCents(maxAmount); got != maxAmount {
		t.Fatalf("roundToCents(maxAmount) = %v, want the column maximum to survive rounding", got)
	}
}

func TestStkPasswordIsDarajaSpelling(t *testing.T) {
	encoded := stkPassword("600000", "the-passkey", time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC))
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("business password is not base64: %v", err)
	}
	if string(raw) != "600000the-passkey20260102150405" {
		t.Fatalf("got %q, want shortcode+passkey+yyyyMMddHHmmss in that order", raw)
	}
}

func TestOauthCacheKey(t *testing.T) {
	key := oauthCacheKey("sandbox", "ck-1234567890")
	if !strings.HasPrefix(key, "mpesa:oauth:sandbox:") {
		t.Fatalf("key = %q, want the environment in the key", key)
	}
	if strings.Contains(key, "ck-1234567890") {
		t.Fatalf("key = %q, want a hash rather than the consumer key in a Redis listing", key)
	}
	if key == oauthCacheKey("production", "ck-1234567890") {
		t.Fatal("one token cannot serve two environments: a sandbox token is not a production token")
	}
	if key == oauthCacheKey("sandbox", "ck-0987654321") {
		t.Fatal("two organizations must not share one Daraja token")
	}
	if key != oauthCacheKey("sandbox", "ck-1234567890") {
		t.Fatal("the key must be stable, or no token is ever reused")
	}
}

func TestStkStatusForCode(t *testing.T) {
	for _, tc := range []struct {
		code int
		want string
	}{{resultCancelled, stkCancelled}, {resultTimeout, stkTimeout}, {1, stkFailed}, {0, stkFailed}, {-1, stkFailed}} {
		if got := stkStatusForCode(tc.code); got != tc.want {
			t.Fatalf("stkStatusForCode(%d) = %q, want %q", tc.code, got, tc.want)
		}
	}
}

func TestAtoiString(t *testing.T) {
	if n, ok := atoiString(" 891 "); !ok || n != 891 {
		t.Fatalf("got %d, %v want 891, true", n, ok)
	}
	for _, bad := range []string{"", "   ", "abc", "0.5", "9999999999999999999999"} {
		if _, ok := atoiString(bad); ok {
			t.Fatalf("atoiString(%q) = ok, want the caller to refuse the callback", bad)
		}
	}
}

// TestScopeFilter is the module's isolation contract in four cases: a receipt with
// no lease is the reconcilers' business, a matched one follows the lease, and a
// caller with no scope at all sees nothing.
func TestScopeFilter(t *testing.T) {
	if clause, args := scopeFilter(rbac.Scope{AllProperties: true}, "mt.lease_id"); clause != "1=1" || len(args) != 0 {
		t.Fatalf("all-properties scope = %q / %v, want 1=1 with no arguments", clause, args)
	}

	clause, args := scopeFilter(rbac.Scope{PropertyIDs: []uint64{3, 7}}, "mt.lease_id")
	if clause != "(p.id IN (?,?))" || len(args) != 2 {
		t.Fatalf("clause = %q args = %v, want the caller's properties", clause, args)
	}

	clause, args = scopeFilter(rbac.Scope{TenantID: ptrUint(5)}, "mt.lease_id")
	if !strings.Contains(clause, "lt_own.lease_id = mt.lease_id") || !strings.Contains(clause, "tenant_id = ?") || len(args) != 1 {
		t.Fatalf("clause = %q args = %v, want the tenant's own leases", clause, args)
	}

	clause, args = scopeFilter(rbac.Scope{PropertyIDs: []uint64{3}, TenantID: ptrUint(5)}, "mt.lease_id")
	if !strings.Contains(clause, " OR ") || len(args) != 2 {
		t.Fatalf("clause = %q args = %v, want either the property or the tenant's own lease", clause, args)
	}

	if clause, args := scopeFilter(rbac.Scope{}, "mt.lease_id"); clause != "1=0" || len(args) != 0 {
		t.Fatalf("empty scope = %q / %v, want nothing visible", clause, args)
	}
}

func TestSealCredentialsIsAllOrNothing(t *testing.T) {
	cipher, err := pii.NewFromKey(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewFromKey: %v", err)
	}

	t.Run("a partial set is refused on all three fields", func(t *testing.T) {
		row := &ShortcodeRow{}
		fields := map[string]string{}
		err := sealCredentials(row, credentialSet{
			consumerKey:    ptrString("ck"),
			consumerSecret: nil,
			passkey:        ptrString("pk"),
		}, cipher, fields)
		validationFields(t, err)
		for _, name := range []string{"consumer_key", "consumer_secret", "passkey"} {
			if !strings.Contains(fields[name], "set together") {
				t.Fatalf("fields[%s] = %q, want the joint-entry rule", name, fields[name])
			}
		}
		if len(row.ConsumerKeyEnc) > 0 || len(row.PasskeyEnc) > 0 {
			t.Fatal("a refused set must not leave half a credential stored")
		}
	})

	t.Run("a full set is stored sealed", func(t *testing.T) {
		row := &ShortcodeRow{}
		set := credentialSet{
			consumerKey:        ptrString(" ck "),
			consumerSecret:     ptrString("secret"),
			passkey:            ptrString("passkey"),
			securityCredential: ptrString("svc"),
		}
		if err := sealCredentials(row, set, cipher, map[string]string{}); err != nil {
			t.Fatalf("sealCredentials: %v", err)
		}
		for _, tc := range []struct {
			name  string
			crypt []byte
			want  string
		}{
			{"key", row.ConsumerKeyEnc, "ck"},
			{"secret", row.ConsumerSecretEnc, "secret"},
			{"passkey", row.PasskeyEnc, "passkey"},
			{"credential", row.SecurityCredentialEnc, "svc"},
		} {
			if len(tc.crypt) == 0 || strings.Contains(string(tc.crypt), tc.want) {
				t.Fatalf("%s stored as %q, want ciphertext that does not contain the value", tc.name, tc.crypt)
			}
			plain, err := cipher.Decrypt(tc.crypt)
			if err != nil || plain != tc.want {
				t.Fatalf("%s round-trip = %q, %v want %q", tc.name, plain, err, tc.want)
			}
		}
	})

	t.Run("an empty string erases and a nil leaves alone", func(t *testing.T) {
		kept := []byte("existing-ciphertext")
		row := &ShortcodeRow{ConsumerKeyEnc: kept, PasskeyEnc: kept}
		set := credentialSet{consumerKey: ptrString("  "), passkey: nil}
		if err := sealCredentials(row, set, cipher, map[string]string{}); err != nil {
			t.Fatalf("sealCredentials: %v", err)
		}
		if len(row.ConsumerKeyEnc) != 0 {
			t.Fatal("an empty credential must clear the stored one")
		}
		if string(row.PasskeyEnc) != string(kept) {
			t.Fatal("a credential the caller did not send is not the caller's to delete")
		}
	})

	t.Run("without a PII key nothing can be stored", func(t *testing.T) {
		err := sealCredentials(&ShortcodeRow{}, credentialSet{
			consumerKey: ptrString("ck"), consumerSecret: ptrString("cs"), passkey: ptrString("pk"),
		}, nil, map[string]string{})
		if !strings.Contains(conflictMessage(t, err), "PII key") {
			t.Fatalf("message = %q, want the missing key named", err)
		}
	})

	t.Run("an over-long secret is refused before encryption", func(t *testing.T) {
		fields := map[string]string{}
		err := sealCredentials(&ShortcodeRow{}, credentialSet{
			consumerKey: ptrString("ck"), consumerSecret: ptrString("cs"), passkey: ptrString("pk"),
			securityCredential: ptrString(strings.Repeat("x", maxSecurityCredLen+1)),
		}, cipher, fields)
		if fields["security_credential"] != "is too long to store" {
			t.Fatalf("fields = %v, want the column limit (%v)", fields, err)
		}
	})
}

func TestCredentialsFromInputsCarryEveryField(t *testing.T) {
	created := credentialsFromCreate(CreateShortcodeInput{
		ConsumerKey: ptrString("k"), ConsumerSecret: ptrString("s"), Passkey: ptrString("p"), SecurityCredential: ptrString("c"),
	})
	updated := credentialsFromUpdate(UpdateShortcodeInput{
		ConsumerKey: ptrString("k"), ConsumerSecret: ptrString("s"), Passkey: ptrString("p"), SecurityCredential: ptrString("c"),
	})
	// A credentialSet is four pointers, and ptrString hands out a fresh one per
	// call, so the sets are only the same when each field carries the same text --
	// compared as text, because %+v prints two lists of addresses.
	if got, want := snapshotOf(created), snapshotOf(updated); got != want {
		t.Fatalf("create set %+v, update set %+v, want the same four fields", got, want)
	}
	if want := (credentialSnapshot{key: "k", secret: "s", pass: "p", cred: "c"}); snapshotOf(created) != want {
		t.Fatalf("create set = %+v, want each input field copied to its own slot", snapshotOf(created))
	}
	if anyCredential(credentialsFromCreate(CreateShortcodeInput{})) {
		t.Fatal("an input that sent no credentials must not look like a credential change")
	}
	if !anyCredential(credentialSet{securityCredential: ptrString("")}) {
		t.Fatal("a field sent empty is a field the caller wrote")
	}
	if anyCredential(credentialSet{}) {
		t.Fatal("nothing sent is not a credential change")
	}
}

type credentialSnapshot struct{ key, secret, pass, cred string }

func snapshotOf(set credentialSet) credentialSnapshot {
	out := credentialSnapshot{}
	if set.consumerKey != nil {
		out.key = *set.consumerKey
	}
	if set.consumerSecret != nil {
		out.secret = *set.consumerSecret
	}
	if set.passkey != nil {
		out.pass = *set.passkey
	}
	if set.securityCredential != nil {
		out.cred = *set.securityCredential
	}
	return out
}

func TestCleanShortcodeAllowsOnlyWhatDarajaAccepts(t *testing.T) {
	for _, tc := range []struct {
		value, want string
		bad         bool
	}{
		{"600000", "600000", false},
		{" 400200 ", "400200", false},
		{"1234", "1234", false},
		{"", "", true},
		{"600", "", true},
		{strings.Repeat("1", maxShortcode+1), "", true},
		{"600000a", "", true},
		{"600000;DROP", "", true},
		{"+254600000", "", true},
	} {
		fields := map[string]string{}
		got := cleanShortcode(tc.value, fields)
		if tc.bad {
			if len(fields) == 0 {
				t.Fatalf("cleanShortcode(%q) = %q, want a refusal", tc.value, got)
			}
			continue
		}
		if got != tc.want || len(fields) > 0 {
			t.Fatalf("cleanShortcode(%q) = %q (%v), want %q", tc.value, got, fields, tc.want)
		}
	}
}

func TestCleanChoiceAndLabel(t *testing.T) {
	fields := map[string]string{}
	if got := cleanChoice(" Paybill ", "paybill", accountTypes, "account_type", accountTypeMessage, fields); got != "paybill" || len(fields) > 0 {
		t.Fatalf("got %q (%v), want the case-insensitive vocabulary match", got, fields)
	}
	if got := cleanChoice("", "till", accountTypes, "account_type", accountTypeMessage, fields); got != "till" {
		t.Fatalf("got %q, want an absent choice to keep the row's value", got)
	}
	if got := cleanChoice("vip", "paybill", accountTypes, "account_type", accountTypeMessage, fields); got != "paybill" || fields["account_type"] != accountTypeMessage {
		t.Fatalf("got %q (%v), want the schema's words for an unknown choice", got, fields)
	}

	fields = map[string]string{}
	if got := cleanLabel("  matched  ", maxNotes, "status", fields); got != "matched" || len(fields) > 0 {
		t.Fatalf("got %q (%v), want a trimmed label", got, fields)
	}
	if got := cleanLabel(strings.Repeat("é", maxNotes+1), maxNotes, "reason", fields); len(fields) == 0 || got == "" {
		t.Fatalf("got %q (%v), want the column limit reported in characters not bytes", got, fields)
	}

	if valueOrEmpty(nil) != "" || valueOrEmpty(ptrString("x")) != "x" {
		t.Fatal("valueOrEmpty must tell an absent field from an empty one")
	}
}

func TestCallbackURLsAreTheMountedPaths(t *testing.T) {
	validation, confirmation, stk := callbackURLs("https://api.kodi.co.ke/", "abc123")
	if validation != "https://api.kodi.co.ke/integrations/mpesa/c2b/abc123/validate" {
		t.Fatalf("validation = %q", validation)
	}
	if confirmation != "https://api.kodi.co.ke/integrations/mpesa/c2b/abc123/confirm" {
		t.Fatalf("confirmation = %q", confirmation)
	}
	if stk != "https://api.kodi.co.ke/integrations/mpesa/stk/abc123/callback" {
		t.Fatalf("stk = %q", stk)
	}
	if _, _, other := callbackURLs("", "abc123"); !strings.HasPrefix(other, "/integrations/mpesa/stk/") {
		t.Fatalf("with no base URL the callback is still a path, got %q", other)
	}
}

func TestTokenHashIsTheHexOfTheKeyedHash(t *testing.T) {
	s := newCipherService(t)
	hash, err := s.tokenHash("a-callback-token")
	if err != nil {
		t.Fatalf("tokenHash: %v", err)
	}
	if len(hash) != 64 {
		t.Fatalf("hash = %q, want the 64 characters callback_token_hash holds", hash)
	}
	again, _ := s.tokenHash("a-callback-token")
	if again != hash {
		t.Fatal("a webhook is matched by equality, so the hash must be deterministic")
	}
	other, _ := s.tokenHash("another-token")
	if other == hash {
		t.Fatal("two tokens must not collide")
	}

	// Without a PII key the module cannot answer "which shortcode is this", and
	// guessing would book somebody else's money.
	bare := newTestService()
	if _, err := bare.tokenHash("a-callback-token"); !strings.Contains(conflictMessage(t, err), "PII key") {
		t.Fatalf("err = %v, want the missing key named", err)
	}
}

func TestIssueTokenWritesBothForms(t *testing.T) {
	s := newCipherService(t)
	row := &ShortcodeRow{OrganizationID: 1, Shortcode: "600000"}
	token, err := s.issueToken(row)
	if err != nil {
		t.Fatalf("issueToken: %v", err)
	}
	if len(token) != tokenBytes*2 {
		t.Fatalf("token = %q, want %d hex characters", token, tokenBytes*2)
	}
	want, err := s.tokenHash(token)
	if err != nil || row.CallbackTokenHash.String != want {
		t.Fatalf("stored hash = %q, want %q (%v)", row.CallbackTokenHash.String, want, err)
	}
	plain, err := s.cipher.Decrypt(row.CallbackTokenEnc)
	if err != nil || plain != token {
		t.Fatalf("sealed token = %q, want it readable again for an outbound callback URL (%v)", plain, err)
	}

	bare := newTestService()
	if _, err := bare.issueToken(&ShortcodeRow{}); !strings.Contains(conflictMessage(t, err), "PII key") {
		t.Fatalf("err = %v, want a refusal rather than a shortcode nobody can call back", err)
	}
}

func TestCallbackTokenReadsBackOnlyWhatWasIssued(t *testing.T) {
	s := newCipherService(t)
	row := &ShortcodeRow{OrganizationID: 1}
	if _, err := s.callbackToken(row); !strings.Contains(conflictMessage(t, err), "rotate") {
		t.Fatalf("err = %v, want the operator told to rotate", err)
	}
	token, err := s.issueToken(row)
	if err != nil {
		t.Fatalf("issueToken: %v", err)
	}
	got, err := s.callbackToken(row)
	if err != nil || got != token {
		t.Fatalf("got %q, want the issued token back (%v)", got, err)
	}
}

func TestCredentialsRefuseToGuess(t *testing.T) {
	s := newCipherService(t)
	if _, err := s.credentials(&ShortcodeRow{}); !strings.Contains(conflictMessage(t, err), "no Daraja credentials") {
		t.Fatalf("err = %v, want a refusal for a shortcode with nothing stored", err)
	}
	sealed, err := s.cipher.Encrypt("ck")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	garbled := &ShortcodeRow{ConsumerKeyEnc: sealed, ConsumerSecretEnc: []byte("not ciphertext"), PasskeyEnc: sealed}
	if _, err := s.credentials(garbled); !strings.Contains(conflictMessage(t, err), "decrypted") {
		t.Fatalf("err = %v, want re-entry asked for rather than a call with half a key", err)
	}
}

func TestShortcodeQueryAndStatusFilters(t *testing.T) {
	q := ShortcodeQuery{Environment: "sandbox", AccountType: "till", Status: "inactive", Search: strings.Repeat("x", maxSearch+10)}
	if err := normalizeShortcodeQuery(&q); err != nil {
		t.Fatalf("normalizeShortcodeQuery: %v", err)
	}
	if len(q.Search) != maxSearch {
		t.Fatalf("search = %d characters, want it clipped to the index it is searched by", len(q.Search))
	}
	if activeFlag(q.Status) != 0 || activeFlag("active") != 1 {
		t.Fatal("the status filter must read as the tinyint the column stores")
	}

	for _, tc := range []struct{ name, value string }{
		{"environment", "prod"},
		{"account_type", "vip"},
		{"status", "on"},
	} {
		q := ShortcodeQuery{}
		switch tc.name {
		case "environment":
			q.Environment = tc.value
		case "account_type":
			q.AccountType = tc.value
		default:
			q.Status = tc.value
		}
		fields := validationFields(t, normalizeShortcodeQuery(&q))
		if fields[tc.name] == "" {
			t.Fatalf("%s = %q, want a named field in the 422", tc.name, tc.value)
		}
	}
}

func TestReceiptAndPromptQueryFilters(t *testing.T) {
	for _, status := range transStatuses {
		q := TransactionQuery{Status: status}
		if err := normalizeTransactionQuery(&q); err != nil {
			t.Fatalf("status %q: %v", status, err)
		}
	}
	fields := validationFields(t, normalizeTransactionQuery(&TransactionQuery{Status: "paid"}))
	if fields["status"] != transStatusMessage {
		t.Fatalf("fields = %v, want the schema's vocabulary in the message", fields)
	}

	for _, status := range stkStatuses {
		if err := normalizeSTKQuery(&STKQuery{Status: status}); err != nil {
			t.Fatalf("status %q: %v", status, err)
		}
	}
	fields = validationFields(t, normalizeSTKQuery(&STKQuery{Status: "sent"}))
	if fields["status"] != stkStatusMessage {
		t.Fatalf("fields = %v, want the schema's vocabulary in the message", fields)
	}
}

func TestCleanAccountRefKeepsWhatTheTenantSees(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"KD-001-A1", "KD-001-A1"},
		{" kd-001-a1 ", "kd-001-a1"},
	} {
		fields := map[string]string{}
		got := cleanAccountRef(tc.value, fields)
		if got != tc.want || len(fields) > 0 {
			t.Fatalf("cleanAccountRef(%q) = %q (%v), want %q kept as written", tc.value, got, fields, tc.want)
		}
	}
	for _, bad := range []string{"", "   ", strings.Repeat("R", maxAccountRef+1), "KD-001\x00A1"} {
		fields := map[string]string{}
		cleanAccountRef(bad, fields)
		if len(fields) == 0 {
			t.Fatalf("cleanAccountRef(%q) accepted a value the column or the tenant cannot use", bad)
		}
	}
}

func TestMessagesSayWhatIsActuallyTrue(t *testing.T) {
	if got := shortcodeTakenMessage("600000", envSandbox); !strings.Contains(got, "same number") {
		t.Fatalf("message = %q, want the shared sandbox paybill explained", got)
	}
	if got := shortcodeTakenMessage("400200", envProduction); !strings.Contains(got, "400200") || strings.Contains(got, "Safaricom gives every test account") {
		t.Fatalf("message = %q, want a production clash told without the sandbox story", got)
	}
	if got := linkPhrase(1, 0, 2); got != "1 account reference, 2 prompts" {
		t.Fatalf("linkPhrase = %q", got)
	}
	if got := linkPhrase(0, 0, 0); got != "" {
		t.Fatalf("linkPhrase = %q, want nothing to report", got)
	}
	if plural(1) != "" || plural(2) != "s" {
		t.Fatal("plural is inverted: one reference, two references")
	}
}

func TestTruncateClipsRunesNotBytes(t *testing.T) {
	if got := truncate("  600000  ", maxShortcode); got != "600000" {
		t.Fatalf("got %q, want the value trimmed", got)
	}
	if got := truncate("KÉ-001-À1-XX", 8); got != "KÉ-001-À" {
		t.Fatalf("got %q, want eight characters rather than eight bytes", got)
	}
}

func TestSQLHelpersDoNotInventMeaning(t *testing.T) {
	// Backslash is MySQL's own LIKE escape character, so a search for "50%" finds the
	// receipt whose note says 50% rather than every note that starts with "50".
	if got := likePattern(`50%\abc`); got != `%50\%\\abc%` {
		t.Fatalf("likePattern = %q, want the wildcards and the escape character quoted", got)
	}
	if got := likePattern("a_b"); got != `%a\_b%` {
		t.Fatalf("likePattern = %q, want the single-character wildcard quoted", got)
	}
	if !strings.HasPrefix(likePattern("a"), "%") || !strings.HasSuffix(likePattern("a"), "%") {
		t.Fatal("a search is a contains match")
	}
	if orderOr("", defaultTransSort) != defaultTransSort {
		t.Fatal("an absent sort takes the module default")
	}
	if orderOr("mt.amount DESC", defaultTransSort) != "mt.amount DESC" {
		t.Fatal("a validated sort is the caller's")
	}
	if !inList("paybill", accountTypes) || inList("paybill", []string{typeTill}) {
		t.Fatal("inList is the vocabulary test every normalizer uses")
	}
	if flag(0) || !flag(1) {
		t.Fatal("is_active is stored as a tinyint")
	}
}

func TestOptionalColumnHelpers(t *testing.T) {
	// optText only answers "is there anything here"; trimming is the label
	// normalizers' job, so a value that reaches it is already the value to store.
	if optText("").Valid || optText("x").String != "x" {
		t.Fatal("a blank text column is NULL, not an empty string")
	}
	if optID(0).Valid || optID(7).Int64 != 7 {
		t.Fatal("a zero foreign key is NULL so the constraint can do its job")
	}
	if nullText(sql.NullString{}) != "" || nullText(optText("x")) != "x" {
		t.Fatal("nullText unwraps for the DTO")
	}
	if nullID(sql.NullInt64{}) != 0 || nullID(optID(9)) != 9 {
		t.Fatal("nullID unwraps for the DTO")
	}
	if intPtr(sql.NullInt64{}) != nil {
		t.Fatal("a missing result code must stay missing in JSON")
	}
	n := 1010
	if got := intPtr(sql.NullInt64{Int64: 1010, Valid: true}); got == nil || *got != n {
		t.Fatalf("intPtr = %v, want 1010", got)
	}
}

func TestDatabaseErrorsBecomeTheRightAnswer(t *testing.T) {
	if err := notFound(sql.ErrNoRows); !errors.Is(err, httpx.ErrNotFound) {
		t.Fatalf("err = %v, want a 404 rather than a 500 for a row that is not there", err)
	}
	if err := notFound(errors.New("boom")); err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want a real database failure passed up for logging", err)
	}

	duplicate := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}
	if !isDuplicateEntry(duplicate) || isForeignKeyRestrict(duplicate) {
		t.Fatal("1062 is a clash, not a restriction")
	}
	if !isForeignKeyRestrict(&mysql.MySQLError{Number: 1451}) {
		t.Fatal("1451 is a restriction")
	}
	if isDuplicateEntry(errors.New("boom")) {
		t.Fatal("an unrelated error is neither")
	}

	rejection := &mysql.MySQLError{Number: 3819, Message: "Check constraint 'chk_mpesa_status' is violated."}
	err := checkRejection(rejection, "the receipt breaks a database rule")
	if !strings.Contains(conflictMessage(t, err), "chk_mpesa_status") {
		t.Fatalf("err = %v, want the schema's own words about the constraint", err)
	}
	if checkRejection(errors.New("boom"), "fallback") != nil {
		t.Fatal("an unrelated error must reach the logger unchanged")
	}
	// A constraint error that carries no text is still a refusal, told in the
	// caller's words rather than an empty message.
	if got := conflictMessage(t, checkRejection(&mysql.MySQLError{Number: 3819}, "the receipt breaks a database rule")); got != "the receipt breaks a database rule" {
		t.Fatalf("message = %q, want the fallback", got)
	}
}

// TestBookForReceiptFailsClosed is the money rule with no billing behind it: a
// receipt that cannot be spent says so, rather than storing it and moving on.
func TestBookForReceiptFailsClosed(t *testing.T) {
	s := newTestService()
	_, err := s.bookForReceipt(context.Background(), &TransactionRow{ID: 1, OrganizationID: 1}, 2,
		receiptBooking{PaymentMethodID: 3}, 100, ClientMeta{})
	if !strings.Contains(conflictMessage(t, err), "Billing is not connected") {
		t.Fatalf("err = %v, want the missing seam named", err)
	}
}

// TestRecordReceiptRefusesBeforeTouchingTheDatabase covers the two inputs that can
// never become a row: no receipt number, and an amount the column or the currency
// cannot hold.
func TestRecordReceiptRefusesBeforeTouchingTheDatabase(t *testing.T) {
	s := newTestService()
	if _, err := s.recordReceipt(context.Background(), receiptBooking{Amount: 100}, ClientMeta{}); err == nil ||
		!strings.Contains(err.Error(), "transaction id") {
		t.Fatalf("err = %v, want a receipt without a trans_id refused by name", err)
	}
	for _, amount := range []float64{0, -1, maxAmount + 1, math.NaN(), math.Inf(1)} {
		_, err := s.recordReceipt(context.Background(), receiptBooking{TransID: "SKQ4ABC", Amount: amount}, ClientMeta{})
		validationFields(t, err)
	}
}

// TestRecoverBookingOnlyReadsAConflict: any other rejection is the provider's
// answer and stays the answer. Turning a real refusal into a silent success would
// leave a receipt unmatched and nobody told.
func TestRecoverBookingOnlyReadsAConflict(t *testing.T) {
	s := newTestService()
	sentinel := errors.New("daraja refused")
	if _, err := s.recoverBooking(context.Background(), &TransactionRow{ID: 1}, 2, 3, "SKQ4ABC", sentinel); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the original refusal", err)
	}
	if _, err := s.recoverBooking(context.Background(), &TransactionRow{ID: 1}, 2, 3, "SKQ4ABC",
		httpx.Validation(map[string]string{"amount": "required"})); err == nil {
		t.Fatal("a 422 is not an already-booked receipt")
	}
}

func TestPushAndReconcileRefuseWithoutPermission(t *testing.T) {
	s := newTestService()
	ctx := context.Background()
	none := &rbac.Principal{UserID: 3, OrgID: 1, Perms: map[string]struct{}{"mpesa.view": {}},
		Scope: rbac.Scope{OrgID: 1, AllProperties: true}}

	if _, err := s.Push(ctx, none, PushInput{ShortcodeID: 1, LeaseID: 2}, ClientMeta{}); !errors.Is(err, httpx.ErrForbidden) {
		t.Fatalf("push: err = %v, want mpesa.push", err)
	}
	if _, err := s.MatchTransaction(ctx, none, 1, MatchInput{LeaseID: 2}, ClientMeta{}); !errors.Is(err, httpx.ErrForbidden) {
		t.Fatalf("match: err = %v, want mpesa.reconcile", err)
	}
	if _, err := s.IgnoreTransaction(ctx, none, 1, IgnoreInput{Reason: "not ours"}, ClientMeta{}); !errors.Is(err, httpx.ErrForbidden) {
		t.Fatalf("ignore: err = %v, want mpesa.reconcile", err)
	}
	if _, err := s.CreateShortcode(ctx, none, CreateShortcodeInput{}, ClientMeta{}); !errors.Is(err, httpx.ErrForbidden) {
		t.Fatalf("create shortcode: err = %v, want mpesa.manage", err)
	}
	if _, err := s.CreateRef(ctx, none, CreateRefInput{}, ClientMeta{}); !errors.Is(err, httpx.ErrForbidden) {
		t.Fatalf("create ref: err = %v, want mpesa.manage", err)
	}
	if err := s.DeleteShortcode(ctx, none, 1, ClientMeta{}); !errors.Is(err, httpx.ErrForbidden) {
		t.Fatalf("delete shortcode: err = %v, want mpesa.manage", err)
	}
}

// TestReadsWithoutAnOrganizationRefuseBeforeAnyQuery is the same rule seen from the
// other side: a caller the module cannot scope has no list to read, and finds out
// before a query is built rather than after an empty one runs.
func TestReadsWithoutAnOrganizationRefuseBeforeAnyQuery(t *testing.T) {
	s := newTestService()
	ctx := context.Background()
	outsider := &rbac.Principal{UserID: 4, Perms: map[string]struct{}{"mpesa.view": {}}}

	// With no organization on the principal there is nothing to scope a query to,
	// so every read refuses as 401 rather than running SQL against a nil pool.
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"shortcodes", func() error { _, err := s.Shortcodes(ctx, outsider, ShortcodeQuery{}); return err }},
		{"receipts", func() error { _, err := s.Transactions(ctx, outsider, TransactionQuery{}); return err }},
		{"prompts", func() error { _, err := s.STKRequests(ctx, outsider, STKQuery{}); return err }},
		{"refs", func() error { _, err := s.Refs(ctx, outsider, RefQuery{}); return err }},
		{"lookups", func() error { _, err := s.Lookups(ctx, outsider); return err }},
	} {
		if err := tc.run(); !errors.Is(err, httpx.ErrUnauthorized) {
			t.Fatalf("%s: err = %v, want 401 before any query", tc.name, err)
		}
	}
}

func TestPushValidatesItsIdsBeforeAskingAnything(t *testing.T) {
	s := newTestService()
	allowed := &rbac.Principal{UserID: 3, OrgID: 1, Perms: map[string]struct{}{"mpesa.push": {}},
		Scope: rbac.Scope{OrgID: 1, AllProperties: true}}
	for _, tc := range []struct {
		name  string
		in    PushInput
		field string
	}{
		{"no shortcode", PushInput{LeaseID: 4}, "shortcode_id"},
		{"no lease", PushInput{ShortcodeID: 1}, "lease_id"},
		{"nothing at all", PushInput{}, "shortcode_id"},
	} {
		fields := validationFields(t, mustErr(t, func() error {
			_, err := s.Push(context.Background(), allowed, tc.in, ClientMeta{})
			return err
		}))
		if fields[tc.field] != "required" {
			t.Fatalf("%s: fields = %v, want %s required", tc.name, fields, tc.field)
		}
	}
}

// TestCallbackReplyIsAlwaysAnAnswer covers the shape Safaricom reads. A rejection
// still has to be a ResultCode, because an error page makes the provider retry a
// callback this installation has already decided against.
func TestCallbackReplyIsAlwaysAnAnswer(t *testing.T) {
	accepted := acceptCallback()
	if accepted.ResultCode != callbackAccepted || accepted.ResultDesc == "" {
		t.Fatalf("reply = %+v, want an accepted callback answered in code 0", accepted)
	}
	rejected := rejectCallback(strings.Repeat("r", maxResultDesc+100))
	if rejected.ResultCode != callbackRejected {
		t.Fatalf("reply = %+v, want code 1", rejected)
	}
	if len([]rune(rejected.ResultDesc)) != maxResultDesc {
		t.Fatalf("description is %d characters, want it clipped to what the caller can read back", len([]rune(rejected.ResultDesc)))
	}
}

func TestConfirmationNameAndReceiptPhone(t *testing.T) {
	if got := confirmationName(C2BConfirmationRequest{ClientName: Scalar("  Jane Wanjiru  ")}); got != "Jane Wanjiru" {
		t.Fatalf("got %q, want the single-name channel's answer trimmed", got)
	}
	split := C2BConfirmationRequest{FirstName: Scalar("Jane"), MiddleName: Scalar(""), LastName: Scalar("Wanjiru")}
	if got := confirmationName(split); got != "Jane Wanjiru" {
		t.Fatalf("got %q, want the three names joined with the blanks left out", got)
	}
	if got := confirmationName(C2BConfirmationRequest{}); got != "" {
		t.Fatalf("got %q, want an anonymous payer named nothing", got)
	}

	s := newTestService()
	if got := s.receiptPhone("0712 345 678"); got != "254712345678" {
		t.Fatalf("got %q, want the number the queue is searched by", got)
	}
	// A phone in a shape this code does not know is still worth keeping with the
	// receipt: dropping it would lose the only clue to what the money was.
	if got := s.receiptPhone("+254-712-345-678"); got != "254712345678" {
		t.Fatalf("got %q, want the normalized form", got)
	}
	if got := s.receiptPhone("not-a-phone"); got != "not-a-phone" {
		t.Fatalf("got %q, want an unreadable number stored as it arrived", got)
	}
	if got := s.receiptPhone("   "); got != "" {
		t.Fatalf("got %q, want a blank number left blank", got)
	}
}

func TestPayloadJSONOnlyHandsBackADocument(t *testing.T) {
	if got := payloadJSON(sql.NullString{String: `{"Amount":"100"}`, Valid: true}); string(got) != `{"Amount":"100"}` {
		t.Fatalf("got %q, want the callback returned as the document it is", got)
	}
	for _, bad := range []sql.NullString{
		{},
		{String: "not json", Valid: true},
		{String: "   ", Valid: true},
	} {
		if got := payloadJSON(bad); len(got) != 0 {
			t.Fatalf("got %q, want nothing rather than a value JSON cannot express", got)
		}
	}
}

// fakeBooker is the seam billing is replaced with, so the shape of the money this
// module asks for can be tested without a second module or a database.
type fakeBooker struct {
	got []GatewayPayment
	err error
}

func (f *fakeBooker) Book(ctx context.Context, in GatewayPayment) (GatewayReceipt, error) {
	f.got = append(f.got, in)
	return GatewayReceipt{}, f.err
}

func TestBookForReceiptHandsBillingTheChannelAnswer(t *testing.T) {
	// The booker refuses with something that is not a clash, so the call stops
	// before LinkTransaction needs a database. What is being proved is the handover
	// itself: the receipt number becomes the payment reference, the channel's own
	// timestamp becomes the paid date, and the caller's client meta travels with it.
	booker := &fakeBooker{err: errors.New("billing has nothing to bill")}
	s := NewService(nil, nil, nil, &config.Config{}, nil, booker, discardLog())
	row := &TransactionRow{ID: 5, OrganizationID: 1, TransID: "SKQ4ABC", TransTime: day(2026, time.March, 4)}
	in := receiptBooking{PaymentMethodID: 9, Notes: "confirmation"}

	_, err := s.bookForReceipt(context.Background(), row, 12, in, 100.007, ClientMeta{IP: "196.1.1.1", UserAgent: "Safaricom"})
	if !errors.Is(err, booker.err) {
		t.Fatalf("err = %v, want billing's refusal told to the caller", err)
	}
	if len(booker.got) != 1 {
		t.Fatalf("booked %d times, want exactly one attempt", len(booker.got))
	}
	payment := booker.got[0]
	if payment.Reference != "SKQ4ABC" || payment.LeaseID != 12 || payment.PaymentMethodID != 9 ||
		payment.OrganizationID != 1 || payment.Amount != 100.01 || payment.Notes != "confirmation" ||
		!payment.PaidAt.Equal(row.TransTime) || payment.Meta.IP != "196.1.1.1" || payment.Meta.UserAgent != "Safaricom" {
		t.Fatalf("Book received %+v", payment)
	}
}

func TestPromptAmountUsesWhatTheCallerGave(t *testing.T) {
	s := newTestService()
	ctx := context.Background()
	who := &rbac.Principal{OrgID: 1}
	lease := &GatewayLeaseRow{ID: 4, LeaseNumber: "L-004", UnitNumber: "B1", PropertyName: "Kodi Apartments"}

	amount, desc, err := s.promptAmount(ctx, who, PushInput{Amount: ptrFloat(1500.004)}, lease)
	if err != nil || amount != 1500 || desc != "Rent for B1 at Kodi Apartments" {
		t.Fatalf("got %v, %q, %v, want the rounded amount and the lease's own description", amount, desc, err)
	}
	amount, desc, err = s.promptAmount(ctx, who, PushInput{Amount: ptrFloat(200), Description: ptrString("  rent  ")}, lease)
	if err != nil || amount != 200 || desc != "rent" {
		t.Fatalf("got %v, %q, %v, want the caller's words kept", amount, desc, err)
	}

	for _, bad := range []float64{0, -5, maxAmount + 1, math.NaN()} {
		fields := validationFields(t, mustErr(t, func() error {
			_, _, err := s.promptAmount(ctx, who, PushInput{Amount: ptrFloat(bad)}, lease)
			return err
		}))
		if fields["amount"] == "" {
			t.Fatalf("amount %v was accepted, want a refusal the column would give anyway", bad)
		}
	}

	// With no amount and no invoice there is nothing to ask for, and guessing at a
	// balance would prompt a tenant for money nobody has worked out yet.
	fields := validationFields(t, mustErr(t, func() error {
		_, _, err := s.promptAmount(ctx, who, PushInput{}, lease)
		return err
	}))
	if !strings.Contains(fields["amount"], "required") {
		t.Fatalf("fields = %v, want the caller told an amount is needed", fields)
	}
}

func TestPromptPhoneFallsBackToTheLease(t *testing.T) {
	s := newTestService()
	lease := &GatewayLeaseRow{ID: 4, UnitNumber: "B1"}

	phone, err := s.promptPhone(PushInput{Phone: ptrString("0712 345 678")}, lease)
	if err != nil || phone != "254712345678" {
		t.Fatalf("got %q, %v, want the number Daraja can charge", phone, err)
	}
	if phone, err := s.promptPhone(PushInput{}, lease); !strings.Contains(validationFields(t, err)["phone"], "required") {
		t.Fatalf("got %q, %v, want a lease with no tenant phone refused by naming the field", phone, err)
	}

	withPhone := &GatewayLeaseRow{ID: 4, TenantPhone: sql.NullString{String: "0112345678", Valid: true}}
	phone, err = s.promptPhone(PushInput{}, withPhone)
	if err != nil || phone != "254112345678" {
		t.Fatalf("got %q, %v, want the tenant's stored line used", phone, err)
	}
	if _, err := s.promptPhone(PushInput{Phone: ptrString("071234")}, lease); validationFields(t, err)["phone"] == "" {
		t.Fatal("a number Daraja would reject has to be rejected here, not by a failed prompt")
	}
}

func mustErr(t *testing.T, run func() error) error {
	t.Helper()
	err := run()
	if err == nil {
		t.Fatal("expected an error")
	}
	return err
}
