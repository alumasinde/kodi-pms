package mpesa

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"kodipms/internal/platform/config"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

// newTestHandler is the HTTP surface over the service from service_test.go: no
// database, no Daraja, no billing. These tests only walk the paths that decide who
// may ask, and whether what they sent is usable, all of which happens before a
// query is written.
func newTestHandler() *Handler {
	return NewHandler(newTestService(), &config.Config{})
}

type handlerCase struct {
	name    string
	method  string
	path    string
	id      string
	body    string
	perm    string
	handler http.HandlerFunc
}

func mpesaRoutes(h *Handler) []handlerCase {
	return []handlerCase{
		{"list shortcodes", http.MethodGet, "/mpesa/shortcodes", "", "", "mpesa.view", h.listShortcodes},
		{"shortcode lookups", http.MethodGet, "/mpesa/shortcodes/lookups", "", "", "mpesa.view", h.lookups},
		{"get shortcode", http.MethodGet, "/mpesa/shortcodes/7", "7", "", "mpesa.view", h.getShortcode},
		{"create shortcode", http.MethodPost, "/mpesa/shortcodes", "", "{}", "mpesa.manage", h.createShortcode},
		{"update shortcode", http.MethodPatch, "/mpesa/shortcodes/7", "7", "{}", "mpesa.manage", h.updateShortcode},
		{"rotate token", http.MethodPost, "/mpesa/shortcodes/7/rotate-token", "7", "{}", "mpesa.manage", h.rotateToken},
		{"delete shortcode", http.MethodDelete, "/mpesa/shortcodes/7", "7", "", "mpesa.manage", h.deleteShortcode},
		{"list refs", http.MethodGet, "/mpesa/account-refs", "", "", "mpesa.view", h.listRefs},
		{"create ref", http.MethodPost, "/mpesa/account-refs", "", "{}", "mpesa.manage", h.createRef},
		{"delete ref", http.MethodDelete, "/mpesa/account-refs/4", "4", "", "mpesa.manage", h.deleteRef},
		{"list receipts", http.MethodGet, "/mpesa/receipts", "", "", "mpesa.view", h.listReceipts},
		{"get receipt", http.MethodGet, "/mpesa/receipts/9", "9", "", "mpesa.view", h.getReceipt},
		{"match receipt", http.MethodPost, "/mpesa/receipts/9/match", "9", "{}", "mpesa.reconcile", h.matchReceipt},
		{"ignore receipt", http.MethodPost, "/mpesa/receipts/9/ignore", "9", "{}", "mpesa.reconcile", h.ignoreReceipt},
		{"list prompts", http.MethodGet, "/mpesa/stk", "", "", "mpesa.view", h.listPrompts},
		{"push prompt", http.MethodPost, "/mpesa/stk/push", "", "{}", "mpesa.push", h.push},
		{"get prompt", http.MethodGet, "/mpesa/stk/5", "5", "", "mpesa.view", h.getPrompt},
	}
}

// TestHandlersRequireAuthentication and TestHandlersRequirePermission walk the
// operator surface twice. Reading a paybill, changing one, prompting a tenant for
// money and spending an unmatched receipt are four different trust levels, and a
// caller who holds none of them must find that out without a database error.
func TestHandlersRequireAuthentication(t *testing.T) {
	h := newTestHandler()
	for _, tc := range mpesaRoutes(h) {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.handler(rec, mpesaRequest(tc, tc.body))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandlersRequirePermission(t *testing.T) {
	h := newTestHandler()
	noPerms := principalWith()
	for _, tc := range mpesaRoutes(h) {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.handler(rec, withPrincipal(mpesaRequest(tc, tc.body), noPerms))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestBadPathIDsReturnNotFound: a shortcode, receipt, ref or prompt is addressed by
// primary key, so "abc" and "0" name nothing and are not the caller's to be told
// anything about -- not even whether the row exists.
func TestBadPathIDsReturnNotFound(t *testing.T) {
	h := newTestHandler()
	for _, tc := range mpesaRoutes(h) {
		if tc.id == "" {
			continue
		}
		for _, id := range []string{"abc", "0", "-1", ""} {
			rec := httptest.NewRecorder()
			tc.handler(rec, withPrincipal(requestWithID(tc.method, id, tc.body), principalWith(tc.perm)))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s with id %q: status = %d, want 404 (%s)", tc.name, id, rec.Code, rec.Body.String())
			}
		}
	}
}

// TestInputIsValidatedBeforeTheDatabaseIsReached covers the write paths that can be
// answered from the request alone. Each case is refused by the module's own rules,
// so a nil pool is never asked a question.
func TestInputIsValidatedBeforeTheDatabaseIsReached(t *testing.T) {
	h := newTestHandler()
	cases := []struct {
		name    string
		method  string
		path    string
		id      string
		body    string
		perm    string
		field   string
		handler http.HandlerFunc
	}{
		{"a shortcode needs a number", http.MethodPost, "/mpesa/shortcodes", "", "{}", "mpesa.manage", "shortcode", h.createShortcode},
		{"a paybill number is digits", http.MethodPost, "/mpesa/shortcodes", "", `{"shortcode":"600000a"}`, "mpesa.manage", "shortcode", h.createShortcode},
		{"a shortcode environment is the schema's vocabulary", http.MethodPost, "/mpesa/shortcodes", "",
			`{"shortcode":"600000","environment":"prod"}`, "mpesa.manage", "environment", h.createShortcode},
		{"credentials cannot be sent one at a time", http.MethodPost, "/mpesa/shortcodes", "",
			`{"shortcode":"600000","consumer_key":"ck"}`, "mpesa.manage", "consumer_secret", h.createShortcode},
		{"an account reference needs its number", http.MethodPost, "/mpesa/account-refs", "", `{"shortcode_id":3,"lease_id":4}`,
			"mpesa.manage", "account_reference", h.createRef},
		{"a reference needs a paybill", http.MethodPost, "/mpesa/account-refs", "", `{"lease_id":4,"account_reference":"KD-001-A1"}`,
			"mpesa.manage", "shortcode_id", h.createRef},
		{"a match needs a lease", http.MethodPost, "/mpesa/receipts/9/match", "9", "{}", "mpesa.reconcile", "lease_id", h.matchReceipt},
		{"ignoring a receipt needs a reason", http.MethodPost, "/mpesa/receipts/9/ignore", "9", "{}", "mpesa.reconcile", "reason", h.ignoreReceipt},
		{"a prompt needs a paybill", http.MethodPost, "/mpesa/stk/push", "", `{"lease_id":4}`, "mpesa.push", "shortcode_id", h.push},
		{"a prompt needs a lease", http.MethodPost, "/mpesa/stk/push", "", `{"shortcode_id":3}`, "mpesa.push", "lease_id", h.push},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req *http.Request
			if tc.id != "" {
				req = requestWithID(tc.method, tc.id, tc.body)
			} else {
				req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			}
			rec := httptest.NewRecorder()
			tc.handler(rec, withPrincipal(req, principalWith(tc.perm)))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 without touching a database (%s)", rec.Code, rec.Body.String())
			}
			fields := decodeFields(t, rec)
			if fields[tc.field] == "" {
				t.Fatalf("fields = %v, want %q named", fields, tc.field)
			}
		})
	}
}

// TestRequestBodyIsDecodedStrictly: the operator API is the product's own, so a
// field it does not name is a client that has misunderstood it, and a half-read
// shortcode write is not worth committing.
func TestRequestBodyIsDecodedStrictly(t *testing.T) {
	h := newTestHandler()
	cases := []struct {
		name    string
		method  string
		path    string
		id      string
		body    string
		perm    string
		code    string
		handler http.HandlerFunc
	}{
		{"a shortcode needs a body", http.MethodPost, "/mpesa/shortcodes", "", "", "mpesa.manage", "validation_failed", h.createShortcode},
		{"shortcode json must parse", http.MethodPost, "/mpesa/shortcodes", "", "{", "mpesa.manage", "bad_request", h.createShortcode},
		{"a shortcode rejects a field it does not own", http.MethodPost, "/mpesa/shortcodes", "",
			`{"shortcode":"600000","callback_token":"abc"}`, "mpesa.manage", "validation_failed", h.createShortcode},
		{"a reference rejects a status written by hand", http.MethodPost, "/mpesa/account-refs", "",
			`{"shortcode_id":3,"lease_id":4,"account_reference":"KD-001-A1","is_active":false}`, "mpesa.manage", "validation_failed", h.createRef},
		{"a match needs a body", http.MethodPost, "/mpesa/receipts/9/match", "9", "", "mpesa.reconcile", "validation_failed", h.matchReceipt},
		{"a match cannot book the money somewhere else", http.MethodPost, "/mpesa/receipts/9/match", "9",
			`{"lease_id":4,"payment_method_id":9}`, "mpesa.reconcile", "validation_failed", h.matchReceipt},
		{"an ignore cannot carry a refund", http.MethodPost, "/mpesa/receipts/9/ignore", "9",
			`{"reason":"duplicate","status":"refunded"}`, "mpesa.reconcile", "validation_failed", h.ignoreReceipt},
		{"a prompt amount has to be a number", http.MethodPost, "/mpesa/stk/push", "", `{"shortcode_id":3,"lease_id":4,"amount":"100"}`,
			"mpesa.push", "validation_failed", h.push},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req *http.Request
			if tc.id != "" {
				req = requestWithID(tc.method, tc.id, tc.body)
			} else {
				req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			}
			rec := httptest.NewRecorder()
			tc.handler(rec, withPrincipal(req, principalWith(tc.perm)))
			if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var envelope struct {
				Error httpx.Err `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != tc.code {
				t.Fatalf("code = %q, want %q (%s)", envelope.Error.Code, tc.code, rec.Body.String())
			}
			if tc.code == "validation_failed" && envelope.Error.Fields["body"] == "" {
				t.Fatalf("fields = %v, want the body named", envelope.Error.Fields)
			}
		})
	}
}

// TestWebhookBodiesAreReadTolerantly is the reason the callbacks do not use the
// API's decoder: Safaricom adds fields to these payloads without asking anybody,
// and a confirmation that fails to parse is money that never reaches a ledger.
func TestWebhookBodiesAreReadTolerantly(t *testing.T) {
	var in C2BConfirmationRequest
	raw := []byte(`{"TransactionID":"ABC123","Amount":"100","NewFromSafaricom":{"nested":[1,2]},"TillNumber":600000}`)
	got, err := readCallback(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/integrations/mpesa/c2b/t/confirm", bytes.NewReader(raw)), &in)
	if err != nil {
		t.Fatalf("err = %v, want an unknown field to be kept rather than refused", err)
	}
	if in.TransactionID != "ABC123" || in.Amount != "100" || in.TillNumber != "600000" {
		t.Fatalf("request = %+v, want a quoted number read as text", in)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("raw = %q, want the bytes the network sent kept whole", got)
	}

	for _, tc := range []struct {
		name string
		body string
	}{
		{"no body at all", ""},
		{"blank body", "   "},
		{"broken json", `{"TransactionID":`},
		{"not an object", `"a receipt"`},
		{"larger than a callback may be", `{"TransactionID":"` + strings.Repeat("x", maxCallbackBody) + `"}`},
	} {
		rec := httptest.NewRecorder()
		var dst C2BConfirmationRequest
		if _, err := readCallback(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(tc.body)), &dst); err == nil {
			t.Fatalf("%s: read without an error, want the caller to refuse the callback", tc.name)
		}
	}
}

func TestWriteCallbackAnswersInTheNetworksEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	writeCallback(rec, rejectCallback("This paybill is not recognized"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a non-2xx makes Safaricom retry a receipt it already sent", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
	var reply CallbackReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("body = %q: %v", rec.Body.String(), err)
	}
	if reply.ResultCode != callbackRejected || reply.ResultDesc == "" {
		t.Fatalf("reply = %+v, want a refusal the provider can read", reply)
	}
}

// TestWebhooksRefuseBeforeTheDatabaseIsReached is the fail-closed half of the
// webhook surface: an installation with no PII key cannot answer "which paybill is
// this", and guessing would book somebody else's money. Either way the answer is a
// refusal at HTTP 200, not a 500 Safaricom retries for hours.
func TestWebhooksRefuseBeforeTheDatabaseIsReached(t *testing.T) {
	h := newTestHandler()
	for _, tc := range []struct {
		name    string
		token   string
		body    string
		handler http.HandlerFunc
	}{
		{"validation with an empty token", "", `{"BusinessShortCode":"600000"}`, h.validateReceipt},
		{"validation with no key configured", "a-token-that-cannot-be-hashed", `{"BusinessShortCode":"600000"}`, h.validateReceipt},
		{"confirmation with no key configured", "a-token-that-cannot-be-hashed", `{"TransactionID":"ABC"}`, h.confirmReceipt},
		{"stk callback with an empty token", "", `{"RequestBody":{"ResultCode":"0","CheckoutRequestID":"x"}}`, h.stkCallback},
		{"stk callback with no key configured", "a-token-that-cannot-be-hashed", `{"RequestBody":{"ResultCode":"0"}}`, h.stkCallback},
	} {
		rec := httptest.NewRecorder()
		tc.handler(rec, withToken(httptest.NewRequest(http.MethodPost, "/integrations/mpesa/x", strings.NewReader(tc.body)), tc.token))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (%s)", tc.name, rec.Code, rec.Body.String())
		}
		var reply CallbackReply
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
			t.Fatalf("%s: body = %q: %v", tc.name, rec.Body.String(), err)
		}
		if reply.ResultCode != callbackRejected {
			t.Fatalf("%s: reply = %+v, want a refusal rather than an accepted payment", tc.name, reply)
		}
	}
}

// TestUnreadableCallbackIsRefusedNotGuessed: a body that cannot be read cannot be
// stored, and a receipt stored without its number is worse than a retry.
func TestUnreadableCallbackIsRefusedNotGuessed(t *testing.T) {
	h := newTestHandler()
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"validation", h.validateReceipt},
		{"confirmation", h.confirmReceipt},
		{"stk callback", h.stkCallback},
	} {
		rec := httptest.NewRecorder()
		tc.handler(rec, withToken(httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{")), "a-token"))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 so the provider reads the verdict in ResultCode", tc.name, rec.Code)
		}
		var reply CallbackReply
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
			t.Fatalf("%s: body = %q: %v", tc.name, rec.Body.String(), err)
		}
		if reply.ResultCode != callbackRejected {
			t.Fatalf("%s: reply = %+v, want the unreadable body refused", tc.name, reply)
		}
	}
}

func TestQueriesBuildBeforeTheDatabase(t *testing.T) {
	t.Run("shortcodes", func(t *testing.T) {
		q, err := shortcodeQuery(httptest.NewRequest(http.MethodGet,
			"/mpesa/shortcodes?q=600&environment=sandbox&type=till&status=inactive&sort=-created_at&page=2&per_page=10", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Search != "600" || q.Environment != "sandbox" || q.AccountType != "till" || q.Status != "inactive" {
			t.Fatalf("query = %+v, the handler keeps the text and the service normalises it", q)
		}
		if q.Sort != "m.created_at DESC" {
			t.Fatalf("sort = %q, only a whitelisted column may reach SQL", q.Sort)
		}
		if q.Page.Page != 2 || q.Page.Limit != 10 || q.Page.Offset != 10 {
			t.Fatalf("page = %+v", q.Page)
		}
		if q, err = shortcodeQuery(httptest.NewRequest(http.MethodGet, "/mpesa/shortcodes?sort=-amount;DROP", nil)); err != nil {
			t.Fatal(err)
		}
		if q.Sort != defaultShortcodeSort {
			t.Fatalf("sort = %q, want the module default for an unknown key", q.Sort)
		}
	})

	t.Run("refs", func(t *testing.T) {
		q, err := refQuery(httptest.NewRequest(http.MethodGet, "/mpesa/account-refs?shortcode_id=3&sort=-reference", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.ShortcodeID != 3 || q.Sort != "r.account_reference DESC" {
			t.Fatalf("query = %+v", q)
		}
		fields := validationFields(t, mustErr(t, func() error {
			_, err := refQuery(httptest.NewRequest(http.MethodGet, "/mpesa/account-refs?shortcode_id=three", nil))
			return err
		}))
		if fields["shortcode_id"] == "" {
			t.Fatalf("fields = %v", fields)
		}
	})

	t.Run("receipts", func(t *testing.T) {
		q, err := transactionQuery(httptest.NewRequest(http.MethodGet,
			"/mpesa/receipts?q=ABC&status=unmatched&shortcode_id=3&lease_id=4&from=2026-01-01&to=2026-03-04&sort=-amount", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Search != "ABC" || q.Status != "unmatched" || q.ShortcodeID != 3 || q.LeaseID != 4 {
			t.Fatalf("query = %+v", q)
		}
		if q.From.Format(dateLayout) != "2026-01-01" || q.To.Format(dateLayout) != "2026-03-04" {
			t.Fatalf("range = %v / %v", q.From, q.To)
		}
		if q.Sort != "mt.amount DESC" {
			t.Fatalf("sort = %q", q.Sort)
		}
	})

	t.Run("prompts", func(t *testing.T) {
		q, err := stkQuery(httptest.NewRequest(http.MethodGet,
			"/mpesa/stk?lease_id=4&status=pending&from=2026-01-01T10:00:00Z&sort=-checkout", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.LeaseID != 4 || q.Status != "pending" || q.From.IsZero() {
			t.Fatalf("query = %+v", q)
		}
		if q.Sort != "ms.checkout_request_id DESC" {
			t.Fatalf("sort = %q", q.Sort)
		}
		if q, err = stkQuery(httptest.NewRequest(http.MethodGet, "/mpesa/stk", nil)); err != nil {
			t.Fatal(err)
		}
		if q.Sort != defaultSTKSort || !q.To.IsZero() {
			t.Fatalf("query = %+v", q)
		}
	})

	t.Run("a filter that cannot name a row is refused", func(t *testing.T) {
		for _, tc := range []struct{ path, field string }{
			{"/mpesa/receipts?shortcode_id=0", "shortcode_id"},
			{"/mpesa/receipts?lease_id=-2", "lease_id"},
			{"/mpesa/receipts?from=04-03-2026", "from"},
			{"/mpesa/receipts?to=yesterday", "to"},
			{"/mpesa/account-refs?shortcode_id=three", "shortcode_id"},
			{"/mpesa/stk?lease_id=abc", "lease_id"},
			{"/mpesa/stk?from=2026-13-40", "from"},
			{"/mpesa/stk?to=04-03-2026", "to"},
		} {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			var err error
			switch {
			case strings.HasPrefix(tc.path, "/mpesa/stk"):
				_, err = stkQuery(req)
			case strings.Contains(tc.path, "account-refs"):
				_, err = refQuery(req)
			default:
				_, err = transactionQuery(req)
			}
			fields := validationFields(t, err)
			if fields[tc.field] == "" {
				t.Fatalf("%s: fields = %v, want %q named", tc.path, fields, tc.field)
			}
		}
	})

	t.Run("every sort key the table promises reaches its column", func(t *testing.T) {
		cases := []struct {
			path     string
			sortable map[string]string
			build    func(*http.Request) (string, error)
		}{
			{"/mpesa/shortcodes?sort=%s", shortcodeSortColumns, func(r *http.Request) (string, error) {
				q, err := shortcodeQuery(r)
				return q.Sort, err
			}},
			{"/mpesa/account-refs?sort=%s", refSortColumns, func(r *http.Request) (string, error) {
				q, err := refQuery(r)
				return q.Sort, err
			}},
			{"/mpesa/receipts?sort=%s", transactionSortColumns, func(r *http.Request) (string, error) {
				q, err := transactionQuery(r)
				return q.Sort, err
			}},
			{"/mpesa/stk?sort=%s", stkSortColumns, func(r *http.Request) (string, error) {
				q, err := stkQuery(r)
				return q.Sort, err
			}},
		}
		for _, tc := range cases {
			for key, column := range tc.sortable {
				req := httptest.NewRequest(http.MethodGet, strings.ReplaceAll(tc.path, "%s", key), nil)
				sort, err := tc.build(req)
				if err != nil {
					t.Fatalf("%s: err = %v", key, err)
				}
				if sort != column+" ASC" {
					t.Fatalf("sort %q = %q, want %q ASC", key, sort, column)
				}
				req = httptest.NewRequest(http.MethodGet, strings.ReplaceAll(tc.path, "%s", "-"+key), nil)
				if sort, err = tc.build(req); err != nil {
					t.Fatalf("-%s: err = %v", key, err)
				}
				if sort != column+" DESC" {
					t.Fatalf("sort -%q = %q, want %q DESC", key, sort, column)
				}
			}
		}
	})
}

func TestPathIDAndToken(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want uint64
		ok   bool
	}{
		{"12", 12, true},
		{"9007199254740993", 9007199254740993, true},
		{"0", 0, false},
		{"-3", 0, false},
		{"", 0, false},
		{"7;drop", 0, false},
	} {
		id, ok := pathID(requestWithID(http.MethodGet, tc.raw, ""))
		if id != tc.want || ok != tc.ok {
			t.Fatalf("pathID(%q) = %d, %v want %d, %v", tc.raw, id, ok, tc.want, tc.ok)
		}
	}
	for _, tc := range []struct{ raw, want string }{
		{"abc123", "abc123"},
		{"  abc123  ", "abc123"},
		{"", ""},
		{"   ", ""},
	} {
		if got := pathToken(withToken(httptest.NewRequest(http.MethodPost, "/x", nil), tc.raw)); got != tc.want {
			t.Fatalf("pathToken(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// TestMetaCarriesTheClientThrough: the audit entry for a receipt says who asked for
// it, and that is only true if the handler reads the request rather than the token.
func TestMetaReadsTheRequest(t *testing.T) {
	h := NewHandler(newTestService(), &config.Config{TrustProxy: true})
	req := httptest.NewRequest(http.MethodPost, "/integrations/mpesa/c2b/t/confirm", nil)
	req.Header.Set("User-Agent", "Safaricom-Daraja")
	req.Header.Set("X-Forwarded-For", "203.0.113.1, 196.1.1.1")
	meta := h.meta(req)
	// The rightmost hop is the one this installation's own proxy wrote; the left
	// entry is whoever the caller claimed to be.
	if meta.IP != "196.1.1.1" || meta.UserAgent != "Safaricom-Daraja" {
		t.Fatalf("meta = %+v, want the proxy's own hop and the caller's agent", meta)
	}

	// Without TRUST_PROXY a forwarded header is a claim, not a fact.
	behind := NewHandler(newTestService(), &config.Config{})
	if got := behind.meta(req).IP; got != "192.0.2.1" {
		t.Fatalf("ip = %q, want the peer address when the proxy is not trusted", got)
	}
}

func TestRoutesCoverEveryHandler(t *testing.T) {
	h := newTestHandler()
	router := chi.NewRouter()
	h.Routes(router)

	for _, want := range []string{
		"GET /mpesa/shortcodes", "GET /mpesa/shortcodes/lookups", "GET /mpesa/shortcodes/7",
		"POST /mpesa/shortcodes", "PATCH /mpesa/shortcodes/7", "DELETE /mpesa/shortcodes/7",
		"POST /mpesa/shortcodes/7/rotate-token",
		"GET /mpesa/account-refs", "POST /mpesa/account-refs", "DELETE /mpesa/account-refs/4",
		"GET /mpesa/receipts", "GET /mpesa/receipts/9", "POST /mpesa/receipts/9/match",
		"POST /mpesa/receipts/9/ignore",
		"GET /mpesa/stk", "GET /mpesa/stk/5", "POST /mpesa/stk/push",
	} {
		method, path, ok := splitRoute(want)
		if !ok {
			t.Fatalf("bad route in the test table: %q", want)
		}
		if !routeServed(router, method, path) {
			t.Fatalf("%s %s is not registered", method, path)
		}
	}

	// The table the handler tests walk is the same one the routes are built from, so
	// a handler written but never mounted fails here.
	for _, tc := range mpesaRoutes(h) {
		if !routeServed(router, tc.method, tc.path) {
			t.Fatalf("%s (%s %s) has no route", tc.name, tc.method, tc.path)
		}
	}
}

// TestRoutesAskForTheirPermission reads the mounted router rather than the
// handlers, so a route registered without rbac.Require -- which would let any
// signed-in caretaker spend an unmatched receipt -- fails here instead of in
// production.
func TestRoutesAskForTheirPermission(t *testing.T) {
	h := newTestHandler()
	router := chi.NewRouter()
	h.Routes(router)

	for _, tc := range []struct {
		method, path, perm string
	}{
		{http.MethodGet, "/mpesa/shortcodes", "mpesa.view"},
		{http.MethodGet, "/mpesa/shortcodes/lookups", "mpesa.view"},
		{http.MethodGet, "/mpesa/shortcodes/7", "mpesa.view"},
		{http.MethodPost, "/mpesa/shortcodes", "mpesa.manage"},
		{http.MethodPatch, "/mpesa/shortcodes/7", "mpesa.manage"},
		{http.MethodPost, "/mpesa/shortcodes/7/rotate-token", "mpesa.manage"},
		{http.MethodDelete, "/mpesa/shortcodes/7", "mpesa.manage"},
		{http.MethodGet, "/mpesa/account-refs", "mpesa.view"},
		{http.MethodPost, "/mpesa/account-refs", "mpesa.manage"},
		{http.MethodDelete, "/mpesa/account-refs/4", "mpesa.manage"},
		{http.MethodGet, "/mpesa/receipts", "mpesa.view"},
		{http.MethodPost, "/mpesa/receipts/9/match", "mpesa.reconcile"},
		{http.MethodPost, "/mpesa/receipts/9/ignore", "mpesa.reconcile"},
		{http.MethodGet, "/mpesa/stk", "mpesa.view"},
		{http.MethodPost, "/mpesa/stk/push", "mpesa.push"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: status = %d, want the router to stop an anonymous caller (%s)",
				tc.method, tc.path, rec.Code, rec.Body.String())
		}

		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, withPrincipal(httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`)),
			principalWith(wrongPerm(tc.perm))))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s with an unrelated permission: status = %d, want 403 (%s)",
				tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

// TestWebhookRoutesAreOnTheirOwnSurface is the mounting rule stated as a test: the
// callbacks answer without a principal and without an organization header, because
// the token in the path is the whole credential. Serving them through a router that
// has no auth middleware is how that stays true.
func TestWebhookRoutesAreOnTheirOwnSurface(t *testing.T) {
	h := newTestHandler()
	router := chi.NewRouter()
	h.WebhookRoutes(router)

	for _, path := range []string{
		"/integrations/mpesa/c2b/abc123/validate",
		"/integrations/mpesa/c2b/abc123/confirm",
		"/integrations/mpesa/stk/abc123/callback",
	} {
		if !routeServed(router, http.MethodPost, path) {
			t.Fatalf("%s is not registered", path)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 with the verdict in the body (%s)", path, rec.Code, rec.Body.String())
		}
		var reply CallbackReply
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
			t.Fatalf("%s: body = %q: %v", path, rec.Body.String(), err)
		}
		if reply.ResultCode == callbackAccepted {
			t.Fatalf("%s: reply = %+v, want an unknown token refused", path, reply)
		}
	}

	// A GET is not a callback. Answering one would mean a browser or a scanner
	// could be told "this URL exists" by a method that carries no receipt.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/integrations/mpesa/c2b/abc123/validate", nil))
	if rec.Code == http.StatusOK {
		t.Fatalf("status = %d, want a method that posts nothing to be refused", rec.Code)
	}
}

func wrongPerm(perm string) string {
	switch perm {
	case "mpesa.view":
		return "mpesa.manage"
	default:
		return "mpesa.view"
	}
}

func decodeFields(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var envelope struct {
		Error httpx.Err `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("body = %q: %v", rec.Body.String(), err)
	}
	return envelope.Error.Fields
}

func mpesaRequest(tc handlerCase, body string) *http.Request {
	if tc.id != "" {
		return requestWithID(tc.method, tc.id, body)
	}
	return httptest.NewRequest(tc.method, tc.path, strings.NewReader(body))
}

func requestWithID(method, id, body string) *http.Request {
	req := httptest.NewRequest(method, "/anything/"+id, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func withToken(r *http.Request, token string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("token", token)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func withPrincipal(r *http.Request, p *rbac.Principal) *http.Request {
	return r.WithContext(rbac.WithPrincipal(r.Context(), p))
}

func principalWith(perms ...string) *rbac.Principal {
	set := make(map[string]struct{}, len(perms))
	for _, perm := range perms {
		set[perm] = struct{}{}
	}
	return &rbac.Principal{UserID: 3, OrgID: 1, Perms: set,
		Scope: rbac.Scope{OrgID: 1, AllProperties: true}}
}

func splitRoute(route string) (string, string, bool) {
	parts := strings.SplitN(route, " ", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// routeServed asks the router itself whether the pattern resolves, which is how a
// handler can end up written but unreachable.
func routeServed(router *chi.Mux, method, path string) bool {
	r := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)
	return rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed
}
