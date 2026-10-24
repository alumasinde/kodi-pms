package billing

import (
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

func TestHandlersRequireAuthentication(t *testing.T) {
	h := newTestHandler()
	for _, tc := range billingRoutes(h) {
		t.Run(tc.name, func(t *testing.T) {
			req := billingRequest(tc, "")
			rec := httptest.NewRecorder()
			tc.handler(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestHandlersRequirePermission checks that every billing endpoint asks for its
// own permission before it touches the database, so a tenant with no billing
// write cannot reach an invoice or a payment at all.
func TestHandlersRequirePermission(t *testing.T) {
	h := newTestHandler()
	noPerms := principalWith()
	for _, tc := range billingRoutes(h) {
		t.Run(tc.name, func(t *testing.T) {
			req := withPrincipal(billingRequest(tc, tc.body), noPerms)
			rec := httptest.NewRecorder()
			tc.handler(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestBadPathIDsReturnNotFound(t *testing.T) {
	h := newTestHandler()
	cases := []struct {
		name    string
		handler http.HandlerFunc
		perm    string
	}{
		{"invoice", h.getInvoice, "invoices.view"},
		{"invoice ledger", h.invoiceLedger, "invoices.view"},
		{"update invoice", h.updateInvoice, "invoices.update"},
		{"delete invoice", h.deleteInvoice, "invoices.delete"},
		{"issue invoice", h.issueInvoice, "invoices.update"},
		{"void invoice", h.voidInvoice, "invoices.update"},
		{"late fee", h.assessLateFee, "invoices.update"},
		{"waive fee", h.waiveLateFee, "invoices.update"},
		{"apply credit", h.applyCredit, "payments.reconcile"},
		{"payment", h.getPayment, "payments.view"},
		{"reverse payment", h.reversePayment, "payments.reverse"},
		{"lease credit", h.leaseCredit, "payments.view"},
		{"credit note", h.getCreditNote, "invoices.view"},
		{"update credit note", h.updateCreditNote, "invoices.update"},
		{"delete credit note", h.deleteCreditNote, "invoices.delete"},
		{"issue credit note", h.issueCreditNote, "invoices.update"},
		{"apply credit note", h.applyCreditNote, "invoices.update"},
		{"void credit note", h.voidCreditNote, "invoices.update"},
	}
	for _, tc := range cases {
		for _, id := range []string{"abc", "0", "-1", ""} {
			req := withPrincipal(requestWithID(http.MethodGet, id, ""), principalWith(tc.perm))
			rec := httptest.NewRecorder()
			tc.handler(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s with id %q: status = %d, want 404 (%s)", tc.name, id, rec.Code, rec.Body.String())
			}
		}
	}
}

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
		field   string
		handler http.HandlerFunc
	}{
		{"invoice needs a body", http.MethodPost, "/invoices", "", "", "invoices.create", "validation_failed", "body", h.createInvoice},
		{"invoice json must parse", http.MethodPost, "/invoices", "", "{", "invoices.create", "bad_request", "", h.createInvoice},
		{"invoice rejects unknown fields", http.MethodPost, "/invoices", "", `{"amount_paid":500}`, "invoices.create", "validation_failed", "body", h.createInvoice},
		{"patch needs a body", http.MethodPatch, "/invoices/7", "7", "", "invoices.update", "validation_failed", "body", h.updateInvoice},
		{"payment needs a body", http.MethodPost, "/payments", "", "", "payments.create", "validation_failed", "body", h.recordPayment},
		{"payment rejects an invented allocation total", http.MethodPost, "/payments", "", `{"lease_id":1,"payment_method_id":2,"amount":100,"total":100}`, "payments.create", "validation_failed", "body", h.recordPayment},
		{"reversal needs a body", http.MethodPost, "/payments/9/reverse", "9", "", "payments.reverse", "validation_failed", "body", h.reversePayment},
		{"credit note needs a body", http.MethodPost, "/credit-notes", "", "", "invoices.create", "validation_failed", "body", h.createCreditNote},
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
			if rec.Code < 400 || rec.Code >= 500 {
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
			if tc.field != "" && envelope.Error.Fields[tc.field] == "" {
				t.Fatalf("missing field %q in %v", tc.field, envelope.Error.Fields)
			}
		})
	}
}

func TestInvoiceQuery(t *testing.T) {
	t.Run("parses the documented filters", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/invoices?q=KD-1&status=Overdue&invoice_type=Utility&lease_id=4&property_id=2"+
				"&unit_id=9&tenant_id=3&overdue=true&due_from=2026-01-01&due_to=2026-03-31"+
				"&sort=-balance&page=3&per_page=10", nil)
		q, err := invoiceQuery(req)
		if err != nil {
			t.Fatal(err)
		}
		if q.Search != "KD-1" || q.Status != "Overdue" || q.Type != "Utility" {
			t.Fatalf("filters = %+v", q)
		}
		if q.LeaseID != 4 || q.PropertyID != 2 || q.UnitID != 9 || q.TenantID != 3 {
			t.Fatalf("ids = %+v", q)
		}
		if !q.Overdue {
			t.Fatal("overdue filter lost")
		}
		if q.DueFrom.Format(dateLayout) != "2026-01-01" || q.DueTo.Format(dateLayout) != "2026-03-31" {
			t.Fatalf("range = %v / %v", q.DueFrom, q.DueTo)
		}
		if q.Sort != "i.balance DESC" {
			t.Fatalf("sort = %q", q.Sort)
		}
		if q.Page.Page != 3 || q.Page.Limit != 10 || q.Page.Offset != 20 {
			t.Fatalf("page = %+v", q.Page)
		}
	})

	t.Run("applies its own defaults", func(t *testing.T) {
		q, err := invoiceQuery(httptest.NewRequest(http.MethodGet, "/invoices", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Sort != defaultInvoiceSort {
			t.Fatalf("sort = %q", q.Sort)
		}
		if q.Page.Page != 1 || q.Page.Limit != 25 {
			t.Fatalf("page = %+v", q.Page)
		}
	})

	t.Run("an unknown sort falls back", func(t *testing.T) {
		q, err := invoiceQuery(httptest.NewRequest(http.MethodGet, "/invoices?sort=-total;DROP", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Sort != defaultInvoiceSort {
			t.Fatalf("sort = %q, only whitelisted columns may reach SQL", q.Sort)
		}
	})

	t.Run("rejects malformed filters", func(t *testing.T) {
		for _, tc := range []struct{ path, field string }{
			{"/invoices?lease_id=abc", "lease_id"},
			{"/invoices?tenant_id=0", "tenant_id"},
			{"/invoices?overdue=maybe", "overdue"},
			{"/invoices?due_from=01-01-2026", "due_from"},
			{"/invoices?due_to=2026-13-01", "due_to"},
		} {
			_, err := invoiceQuery(httptest.NewRequest(http.MethodGet, tc.path, nil))
			fields := validationFields(t, err)
			if fields[tc.field] == "" {
				t.Fatalf("%s: fields = %v", tc.path, fields)
			}
		}
	})
}

func TestPaymentLedgerAndCreditNoteQueries(t *testing.T) {
	t.Run("payments", func(t *testing.T) {
		q, err := paymentQuery(httptest.NewRequest(http.MethodGet,
			"/payments?q=MPESA&status=Completed&lease_id=5&method_id=2&from=2026-01-01&to=2026-02-01&sort=-amount", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Status != "Completed" || q.LeaseID != 5 || q.MethodID != 2 {
			t.Fatalf("query = %+v", q)
		}
		if q.From.Format(dateLayout) != "2026-01-01" || q.To.Format(dateLayout) != "2026-02-01" {
			t.Fatalf("range = %v / %v", q.From, q.To)
		}
		if q.Sort != "p.amount DESC" {
			t.Fatalf("sort = %q", q.Sort)
		}
	})

	t.Run("ledger", func(t *testing.T) {
		q, err := ledgerQuery(httptest.NewRequest(http.MethodGet,
			"/ledger/entries?account=rent_income&source_type=invoice&source_id=8&lease_id=5&sort=-debit", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.AccountCode != "rent_income" || q.SourceType != "invoice" || q.SourceID != 8 || q.LeaseID != 5 {
			t.Fatalf("query = %+v", q)
		}
		if q.Sort != "le.debit_amount DESC" {
			t.Fatalf("sort = %q", q.Sort)
		}
		if q, err = ledgerQuery(httptest.NewRequest(http.MethodGet, "/ledger/entries", nil)); err != nil {
			t.Fatal(err)
		}
		if q.Sort != defaultLedgerSort {
			t.Fatalf("default sort = %q", q.Sort)
		}
	})

	t.Run("credit notes", func(t *testing.T) {
		q, err := creditNoteQuery(httptest.NewRequest(http.MethodGet,
			"/credit-notes?status=applied&invoice_id=6&property_id=2&sort=amount", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Status != "applied" || q.InvoiceID != 6 || q.PropertyID != 2 {
			t.Fatalf("query = %+v", q)
		}
		if q.Sort != "c.amount ASC" {
			t.Fatalf("sort = %q", q.Sort)
		}
	})

	t.Run("a range that cannot exist is refused by the service, not the handler", func(t *testing.T) {
		q, err := paymentQuery(httptest.NewRequest(http.MethodGet,
			"/payments?from=2026-05-01&to=2026-01-01", nil))
		if err != nil {
			t.Fatal(err)
		}
		if err = normalizePaymentQuery(&q); err == nil {
			t.Fatal("an inverted range must fail validation")
		}
	})
}

func TestDecodeOptionalTakesAnEmptyActionBody(t *testing.T) {
	t.Run("no body is a valid action", func(t *testing.T) {
		rec := httptest.NewRecorder()
		var in ApplyCreditInput
		if err := decodeOptional(rec, httptest.NewRequest(http.MethodPost, "/invoices/1/apply-credit", nil), &in); err != nil {
			t.Fatalf("err = %v, an omitted action body should be accepted", err)
		}
		if in.Amount != nil {
			t.Fatalf("amount = %v", *in.Amount)
		}
	})

	t.Run("a broken body still fails", func(t *testing.T) {
		rec := httptest.NewRecorder()
		var in VoidInput
		err := decodeOptional(rec, httptest.NewRequest(http.MethodPost, "/invoices/1/void",
			strings.NewReader("{")), &in)
		if err == nil {
			t.Fatal("malformed json must be rejected")
		}
		var e *httpx.Err
		if !asErr(err, &e) || e.Code != "bad_request" {
			t.Fatalf("err = %v, want a bad_request", err)
		}
	})

	t.Run("a value the client did not send is not invented", func(t *testing.T) {
		rec := httptest.NewRecorder()
		var in LateFeeInput
		if err := decodeOptional(rec, httptest.NewRequest(http.MethodPost, "/invoices/1/late-fee",
			strings.NewReader(`{"reason":"courtesy"}`)), &in); err != nil {
			t.Fatal(err)
		}
		if in.Amount != nil {
			t.Fatal("an omitted amount must stay nil so the settings formula is used")
		}
		if in.Reason != "courtesy" {
			t.Fatalf("reason = %q", in.Reason)
		}
	})
}

func TestRoutesCoverEveryHandler(t *testing.T) {
	h := newTestHandler()
	router := chi.NewRouter()
	h.Routes(router)

	for _, want := range []string{
		"GET /invoices", "POST /invoices", "GET /invoices/{id}", "PATCH /invoices/{id}",
		"DELETE /invoices/{id}", "POST /invoices/{id}/issue", "POST /invoices/{id}/void",
		"POST /invoices/{id}/late-fee", "POST /invoices/{id}/apply-credit",
		"GET /invoices/{id}/ledger", "POST /late-fees/{id}/waive",
		"GET /payments", "POST /payments", "GET /payments/{id}", "POST /payments/{id}/reverse",
		"GET /leases/{id}/credit", "GET /payment-methods",
		"GET /credit-notes", "POST /credit-notes", "GET /credit-notes/{id}",
		"PATCH /credit-notes/{id}", "DELETE /credit-notes/{id}",
		"POST /credit-notes/{id}/issue", "POST /credit-notes/{id}/apply",
		"POST /credit-notes/{id}/void",
		"GET /ledger/entries", "GET /ledger/accounts", "GET /billing/summary",
	} {
		method, path, ok := splitRoute(want)
		if !ok {
			t.Fatalf("bad route in the test table: %q", want)
		}
		if !routeServed(router, method, path) {
			t.Fatalf("%s is not registered", want)
		}
	}
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

func billingRoutes(h *Handler) []handlerCase {
	return []handlerCase{
		{"list invoices", http.MethodGet, "/invoices", "", "", "invoices.view", h.listInvoices},
		{"get invoice", http.MethodGet, "/invoices/7", "7", "", "invoices.view", h.getInvoice},
		{"invoice ledger", http.MethodGet, "/invoices/7/ledger", "7", "", "invoices.view", h.invoiceLedger},
		{"create invoice", http.MethodPost, "/invoices", "", "{}", "invoices.create", h.createInvoice},
		{"update invoice", http.MethodPatch, "/invoices/7", "7", "{}", "invoices.update", h.updateInvoice},
		{"delete invoice", http.MethodDelete, "/invoices/7", "7", "", "invoices.delete", h.deleteInvoice},
		{"issue invoice", http.MethodPost, "/invoices/7/issue", "7", "{}", "invoices.update", h.issueInvoice},
		{"void invoice", http.MethodPost, "/invoices/7/void", "7", "{}", "invoices.update", h.voidInvoice},
		{"assess fee", http.MethodPost, "/invoices/7/late-fee", "7", "{}", "invoices.update", h.assessLateFee},
		{"waive fee", http.MethodPost, "/late-fees/7/waive", "7", "{}", "invoices.update", h.waiveLateFee},
		{"apply credit", http.MethodPost, "/invoices/7/apply-credit", "7", "{}", "payments.reconcile", h.applyCredit},
		{"list payments", http.MethodGet, "/payments", "", "", "payments.view", h.listPayments},
		{"get payment", http.MethodGet, "/payments/9", "9", "", "payments.view", h.getPayment},
		{"record payment", http.MethodPost, "/payments", "", "{}", "payments.create", h.recordPayment},
		{"reverse payment", http.MethodPost, "/payments/9/reverse", "9", "{}", "payments.reverse", h.reversePayment},
		{"lease credit", http.MethodGet, "/leases/4/credit", "4", "", "payments.view", h.leaseCredit},
		{"payment methods", http.MethodGet, "/payment-methods", "", "", "payments.view", h.paymentMethods},
		{"list credit notes", http.MethodGet, "/credit-notes", "", "", "invoices.view", h.listCreditNotes},
		{"get credit note", http.MethodGet, "/credit-notes/3", "3", "", "invoices.view", h.getCreditNote},
		{"create credit note", http.MethodPost, "/credit-notes", "", "{}", "invoices.create", h.createCreditNote},
		{"update credit note", http.MethodPatch, "/credit-notes/3", "3", "{}", "invoices.update", h.updateCreditNote},
		{"delete credit note", http.MethodDelete, "/credit-notes/3", "3", "", "invoices.delete", h.deleteCreditNote},
		{"issue credit note", http.MethodPost, "/credit-notes/3/issue", "3", "{}", "invoices.update", h.issueCreditNote},
		{"apply credit note", http.MethodPost, "/credit-notes/3/apply", "3", "", "invoices.update", h.applyCreditNote},
		{"void credit note", http.MethodPost, "/credit-notes/3/void", "3", "{}", "invoices.update", h.voidCreditNote},
		{"list ledger", http.MethodGet, "/ledger/entries", "", "", "invoices.view", h.listLedger},
		{"list accounts", http.MethodGet, "/ledger/accounts", "", "", "invoices.view", h.listAccounts},
		{"summary", http.MethodGet, "/billing/summary", "", "", "invoices.view", h.summary},
	}
}

func billingRequest(tc handlerCase, body string) *http.Request {
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

func newTestHandler() *Handler {
	return NewHandler(NewService(nil, nil, nil), &config.Config{})
}

func splitRoute(route string) (string, string, bool) {
	parts := strings.SplitN(route, " ", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// routeServed asks the router itself whether the pattern resolves, which is how
// a handler can end up written but unreachable.
func routeServed(router *chi.Mux, method, path string) bool {
	r := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)
	return rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed
}

func asErr(err error, target **httpx.Err) bool {
	e, ok := err.(*httpx.Err)
	if ok {
		*target = e
	}
	return ok
}
