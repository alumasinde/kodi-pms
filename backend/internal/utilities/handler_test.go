package utilities

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"kodipms/internal/platform/config"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

// TestHandlersRequireAuthentication and TestHandlersRequirePermission walk every
// endpoint twice: once with nobody signed in and once with a caller who has no
// utility permission at all. A caretaker must not be able to reach a meter, a
// tenant must not be able to move a bill, and neither has to be shown a database
// error to find that out.
func TestHandlersRequireAuthentication(t *testing.T) {
	h := newTestHandler()
	for _, tc := range utilityRoutes(h) {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.handler(rec, utilityRequest(tc, ""))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandlersRequirePermission(t *testing.T) {
	h := newTestHandler()
	noPerms := principalWith()
	for _, tc := range utilityRoutes(h) {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.handler(rec, withPrincipal(utilityRequest(tc, tc.body), noPerms))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestBadPathIDsReturnNotFound covers the ids that cannot name a row: a meter,
// reading or bill address the database by primary key, so "abc" and "0" are not
// the caller's to be told anything about.
func TestBadPathIDsReturnNotFound(t *testing.T) {
	h := newTestHandler()
	cases := []struct {
		name    string
		handler http.HandlerFunc
		perm    string
	}{
		{"get meter", h.getMeter, "utilities.view"},
		{"update meter", h.updateMeter, "utilities.update"},
		{"delete meter", h.deleteMeter, "utilities.update"},
		{"list readings", h.listReadings, "utilities.view"},
		{"create reading", h.createReading, "utilities.record_reading"},
		{"get reading", h.getReading, "utilities.view"},
		{"update reading", h.updateReading, "utilities.record_reading"},
		{"delete reading", h.deleteReading, "utilities.record_reading"},
		{"get bill", h.getBill, "utilities.view"},
		{"update bill", h.updateBill, "utilities.update"},
		{"void bill", h.voidBill, "utilities.update"},
	}
	for _, tc := range cases {
		for _, id := range []string{"abc", "0", "-1", ""} {
			rec := httptest.NewRecorder()
			tc.handler(rec, withPrincipal(requestWithID(http.MethodGet, id, `{}`), principalWith(tc.perm)))
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
		{"meter needs a body", http.MethodPost, "/utility/meters", "", "", "utilities.create", "validation_failed", "body", h.createMeter},
		{"meter json must parse", http.MethodPost, "/utility/meters", "", "{", "utilities.create", "bad_request", "", h.createMeter},
		{"meter rejects a field it does not own", http.MethodPost, "/utility/meters", "", `{"property_id":2,"status":"active","invoice_id":9}`,
			"utilities.create", "validation_failed", "body", h.createMeter},
		{"reading needs a body", http.MethodPost, "/utility/meters/7/readings", "7", "", "utilities.record_reading", "validation_failed", "body", h.createReading},
		{"reading rejects a consumption sent by hand", http.MethodPost, "/utility/meters/7/readings", "7",
			`{"reading_date":"2026-03-01","current_reading":10,"consumption":4}`, "utilities.record_reading", "validation_failed", "body", h.createReading},
		{"bill update needs a body", http.MethodPatch, "/utility/bills/4", "4", "", "utilities.update", "validation_failed", "body", h.updateBill},
		{"generate needs a body", http.MethodPost, "/utility/bills/generate", "", "", "utilities.create", "validation_failed", "body", h.generateBills},
		{"a bill cannot be marked paid from here", http.MethodPost, "/utility/bills", "", `{"unit_id":5,"status":"paid"}`,
			"utilities.create", "validation_failed", "body", h.createBill},
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
			if tc.field != "" && envelope.Error.Fields[tc.field] == "" {
				t.Fatalf("missing field %q in %v", tc.field, envelope.Error.Fields)
			}
		})
	}
}

// TestDecodeOptionalTakesAnEmptyActionBody keeps the void call's body optional:
// the reason is the only field it has, and refusing a bare POST would make the
// endpoint contradict the contract the README documents.
func TestDecodeOptionalTakesAnEmptyActionBody(t *testing.T) {
	t.Run("no body is a valid action", func(t *testing.T) {
		rec := httptest.NewRecorder()
		var in VoidBillInput
		if err := decodeOptional(rec, httptest.NewRequest(http.MethodPost, "/utility/bills/4/void", nil), &in); err != nil {
			t.Fatalf("err = %v, an omitted reason should be accepted", err)
		}
		if in.Reason != "" {
			t.Fatalf("reason = %q", in.Reason)
		}
	})

	t.Run("a broken body still fails", func(t *testing.T) {
		rec := httptest.NewRecorder()
		var in VoidBillInput
		err := decodeOptional(rec, httptest.NewRequest(http.MethodPost, "/utility/bills/4/void",
			strings.NewReader("{")), &in)
		if err == nil {
			t.Fatal("malformed json must be rejected")
		}
		var e *httpx.Err
		if !errors.As(err, &e) || e.Code != "bad_request" {
			t.Fatalf("err = %v, want a bad_request", err)
		}
	})

	t.Run("an unknown field is still refused", func(t *testing.T) {
		rec := httptest.NewRecorder()
		var in VoidBillInput
		err := decodeOptional(rec, httptest.NewRequest(http.MethodPost, "/utility/bills/4/void",
			strings.NewReader(`{"status":"paid"}`)), &in)
		fields := validationFields(t, err)
		if fields["body"] == "" {
			t.Fatalf("fields = %v", fields)
		}
	})
}

// TestInputIsValidatedBeforeTheDatabaseIsReached is the no-DB half of the write
// paths: a service built on a nil pool still answers these requests properly,
// because every builder runs before the transaction opens.
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
		{"a meter needs a property", http.MethodPost, "/utility/meters", "", `{"utility_type":"water","meter_number":"W-1"}`,
			"utilities.create", "property_id", h.createMeter},
		{"a meter type is the schema's vocabulary", http.MethodPost, "/utility/meters", "",
			`{"property_id":2,"utility_type":"power","meter_number":"W-1"}`, "utilities.create", "utility_type", h.createMeter},
		{"a reading needs a date", http.MethodPost, "/utility/meters/7/readings", "7", `{"current_reading":120}`,
			"utilities.record_reading", "reading_date", h.createReading},
		{"a reading date has to parse", http.MethodPost, "/utility/meters/7/readings", "7",
			`{"reading_date":"2026-13-40","current_reading":120}`, "utilities.record_reading", "reading_date", h.createReading},
		{"a bill type is the schema's vocabulary", http.MethodPost, "/utility/bills", "",
			`{"unit_id":5,"type":"power","amount":100}`, "utilities.create", "type", h.createBill},
		{"a hand-raised charge needs its dates as dates", http.MethodPost, "/utility/bills", "",
			`{"unit_id":5,"type":"water","amount":100,"date_due":"05-04-2026"}`, "utilities.create", "date_due", h.createBill},
		{"a generated run takes one target", http.MethodPost, "/utility/bills/generate", "", `{"meter_id":3,"unit_id":5}`,
			"utilities.create", "unit_id", h.generateBills},
		{"a generated run reads its bounds as dates", http.MethodPost, "/utility/bills/generate", "",
			`{"from":"01-02-2026","to":"2026-03-04"}`, "utilities.create", "from", h.generateBills},
		{"a void reason has a length", http.MethodPost, "/utility/bills/4/void", "4",
			`{"reason":"` + strings.Repeat("r", maxNotes+1) + `"}`, "utilities.update", "reason", h.voidBill},
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
			var envelope struct {
				Error httpx.Err `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Fields[tc.field] == "" {
				t.Fatalf("fields = %v, want %q named", envelope.Error.Fields, tc.field)
			}
		})
	}
}

func TestMeterQuery(t *testing.T) {
	t.Run("parses the documented filters", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/utility/meters?q=W-1&utility_type=Water&status=Faulty&property_id=2&unit_id=9"+
				"&open_only=yes&sort=-last_reading&page=3&per_page=10", nil)
		q, err := meterQuery(req)
		if err != nil {
			t.Fatal(err)
		}
		if q.Search != "W-1" || q.UtilityType != "Water" || q.Status != "Faulty" {
			t.Fatalf("query = %+v, the handler keeps the text and the service normalises it", q)
		}
		if q.PropertyID != 2 || q.UnitID != 9 || !q.OpenOnly {
			t.Fatalf("query = %+v", q)
		}
		if q.Sort != "last_reading_date DESC" {
			t.Fatalf("sort = %q, only a whitelisted column may reach SQL", q.Sort)
		}
		if q.Page.Page != 3 || q.Page.Limit != 10 || q.Page.Offset != 20 {
			t.Fatalf("page = %+v", q.Page)
		}
	})

	t.Run("applies its own defaults", func(t *testing.T) {
		q, err := meterQuery(httptest.NewRequest(http.MethodGet, "/utility/meters", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Sort != defaultMeterSort {
			t.Fatalf("sort = %q", q.Sort)
		}
		if q.Page.Page != 1 || q.Page.Limit != 25 {
			t.Fatalf("page = %+v", q.Page)
		}
	})

	t.Run("an unknown sort falls back", func(t *testing.T) {
		q, err := meterQuery(httptest.NewRequest(http.MethodGet, "/utility/meters?sort=-rate;DROP", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Sort != defaultMeterSort {
			t.Fatalf("sort = %q", q.Sort)
		}
	})

	t.Run("rejects malformed filters", func(t *testing.T) {
		for _, tc := range []struct{ path, field string }{
			{"/utility/meters?property_id=abc", "property_id"},
			{"/utility/meters?unit_id=0", "unit_id"},
			{"/utility/meters?open_only=maybe", ""},
		} {
			q, err := meterQuery(httptest.NewRequest(http.MethodGet, tc.path, nil))
			if tc.field == "" {
				if err != nil || q.OpenOnly {
					t.Fatalf("%s: err = %v open_only = %v, an unreadable flag is an absent one", tc.path, err, q.OpenOnly)
				}
				continue
			}
			fields := validationFields(t, err)
			if fields[tc.field] == "" {
				t.Fatalf("%s: fields = %v", tc.path, fields)
			}
		}
	})
}

func TestReadingAndBillQueries(t *testing.T) {
	t.Run("readings", func(t *testing.T) {
		q, err := readingQuery(httptest.NewRequest(http.MethodGet,
			"/utility/meters/7/readings?from=2026-01-01&to=2026-06-30&sort=-consumption", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.From.Format(dateLayout) != "2026-01-01" || q.To.Format(dateLayout) != "2026-06-30" {
			t.Fatalf("range = %v / %v", q.From, q.To)
		}
		if q.Sort != "ur.consumption DESC" {
			t.Fatalf("sort = %q", q.Sort)
		}
		if q, err = readingQuery(httptest.NewRequest(http.MethodGet, "/utility/meters/7/readings", nil)); err != nil {
			t.Fatal(err)
		}
		if q.Sort != defaultReadingSort || !q.From.IsZero() {
			t.Fatalf("query = %+v", q)
		}
		if _, err = readingQuery(httptest.NewRequest(http.MethodGet, "/utility/meters/7/readings?from=31-12-2026", nil)); err == nil {
			t.Fatal("a slashed date must fail before the query is built")
		}
	})

	t.Run("bills", func(t *testing.T) {
		q, err := billQuery(httptest.NewRequest(http.MethodGet,
			"/utility/bills?q=B12&status=pending&type=water&property_id=2&unit_id=5&meter_id=3"+
				"&overdue=1&due_from=2026-04-01&due_to=2026-04-30&sort=-amount", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Search != "B12" || q.Status != "pending" || q.Type != "water" {
			t.Fatalf("query = %+v", q)
		}
		if q.PropertyID != 2 || q.UnitID != 5 || q.MeterID != 3 || !q.Overdue {
			t.Fatalf("query = %+v", q)
		}
		if q.DueFrom.Format(dateLayout) != "2026-04-01" || q.DueTo.Format(dateLayout) != "2026-04-30" {
			t.Fatalf("range = %v / %v", q.DueFrom, q.DueTo)
		}
		if q.Sort != "ub.amount DESC" {
			t.Fatalf("sort = %q", q.Sort)
		}
	})

	t.Run("every sort key the table promises reaches its column", func(t *testing.T) {
		cases := []struct {
			path     string
			sortable map[string]string
			build    func(*http.Request) (string, error)
		}{
			{"/utility/meters?sort=%s", meterSortColumns, func(r *http.Request) (string, error) {
				q, err := meterQuery(r)
				return q.Sort, err
			}},
			{"/utility/meters/7/readings?sort=%s", readingSortColumns, func(r *http.Request) (string, error) {
				q, err := readingQuery(r)
				return q.Sort, err
			}},
			{"/utility/bills?sort=%s", billSortColumns, func(r *http.Request) (string, error) {
				q, err := billQuery(r)
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

func TestPathID(t *testing.T) {
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
}

func TestRoutesCoverEveryHandler(t *testing.T) {
	h := newTestHandler()
	router := chi.NewRouter()
	h.Routes(router)

	for _, want := range []string{
		"GET /utility/meters", "POST /utility/meters", "GET /utility/meters/lookups",
		"GET /utility/meters/7", "PATCH /utility/meters/7", "DELETE /utility/meters/7",
		"GET /utility/meters/7/readings", "POST /utility/meters/7/readings",
		"GET /utility/readings/7", "PATCH /utility/readings/7", "DELETE /utility/readings/7",
		"GET /utility/bills", "POST /utility/bills", "POST /utility/bills/generate",
		"GET /utility/bills/7", "PATCH /utility/bills/7", "POST /utility/bills/7/void",
		"GET /utility/summary",
	} {
		method, path, ok := splitRoute(want)
		if !ok {
			t.Fatalf("bad route in the test table: %q", want)
		}
		if !routeServed(router, method, path) {
			t.Fatalf("%s %s is not registered", method, path)
		}
	}

	// The table the handler tests walk is the same one the routes are built
	// from, so a handler written but never mounted fails here.
	for _, tc := range utilityRoutes(h) {
		if !routeServed(router, tc.method, tc.path) {
			t.Fatalf("%s (%s %s) has no route", tc.name, tc.method, tc.path)
		}
	}
}

// TestRoutesAskForTheirPermission reads the mounted router rather than the
// handlers, so a route registered without rbac.Require — which would let any
// signed-in user record a dial reading — fails here instead of in production.
func TestRoutesAskForTheirPermission(t *testing.T) {
	h := newTestHandler()
	router := chi.NewRouter()
	h.Routes(router)

	for _, tc := range []struct {
		method, path, perm string
	}{
		{http.MethodGet, "/utility/meters", "utilities.view"},
		{http.MethodGet, "/utility/meters/lookups", "utilities.view"},
		{http.MethodPost, "/utility/meters", "utilities.create"},
		{http.MethodPatch, "/utility/meters/7", "utilities.update"},
		{http.MethodDelete, "/utility/meters/7", "utilities.update"},
		{http.MethodPost, "/utility/meters/7/readings", "utilities.record_reading"},
		{http.MethodPatch, "/utility/readings/7", "utilities.record_reading"},
		{http.MethodDelete, "/utility/readings/7", "utilities.record_reading"},
		{http.MethodPost, "/utility/bills", "utilities.create"},
		{http.MethodPost, "/utility/bills/generate", "utilities.create"},
		{http.MethodPatch, "/utility/bills/7", "utilities.update"},
		{http.MethodPost, "/utility/bills/7/void", "utilities.update"},
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

func wrongPerm(perm string) string {
	if perm == "utilities.view" {
		return "utilities.record_reading"
	}
	return "utilities.view"
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

func utilityRoutes(h *Handler) []handlerCase {
	return []handlerCase{
		{"list meters", http.MethodGet, "/utility/meters", "", "", "utilities.view", h.listMeters},
		{"meter lookups", http.MethodGet, "/utility/meters/lookups", "", "", "utilities.view", h.lookups},
		{"get meter", http.MethodGet, "/utility/meters/7", "7", "", "utilities.view", h.getMeter},
		{"create meter", http.MethodPost, "/utility/meters", "", "{}", "utilities.create", h.createMeter},
		{"update meter", http.MethodPatch, "/utility/meters/7", "7", "{}", "utilities.update", h.updateMeter},
		{"delete meter", http.MethodDelete, "/utility/meters/7", "7", "", "utilities.update", h.deleteMeter},
		{"list readings", http.MethodGet, "/utility/meters/7/readings", "7", "", "utilities.view", h.listReadings},
		{"create reading", http.MethodPost, "/utility/meters/7/readings", "7", "{}", "utilities.record_reading", h.createReading},
		{"get reading", http.MethodGet, "/utility/readings/9", "9", "", "utilities.view", h.getReading},
		{"update reading", http.MethodPatch, "/utility/readings/9", "9", "{}", "utilities.record_reading", h.updateReading},
		{"delete reading", http.MethodDelete, "/utility/readings/9", "9", "", "utilities.record_reading", h.deleteReading},
		{"list bills", http.MethodGet, "/utility/bills", "", "", "utilities.view", h.listBills},
		{"get bill", http.MethodGet, "/utility/bills/4", "4", "", "utilities.view", h.getBill},
		{"create bill", http.MethodPost, "/utility/bills", "", "{}", "utilities.create", h.createBill},
		{"generate bills", http.MethodPost, "/utility/bills/generate", "", "{}", "utilities.create", h.generateBills},
		{"update bill", http.MethodPatch, "/utility/bills/4", "4", "{}", "utilities.update", h.updateBill},
		{"void bill", http.MethodPost, "/utility/bills/4/void", "4", "{}", "utilities.update", h.voidBill},
		{"summary", http.MethodGet, "/utility/summary", "", "", "utilities.view", h.summary},
	}
}

func utilityRequest(tc handlerCase, body string) *http.Request {
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

// newTestHandler builds the handler over a service with no database. These tests
// only ever exercise the paths that decide whether a request is allowed and
// whether its body is usable, all of which happen before a query is written.
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

// routeServed asks the router itself whether the pattern resolves, which is how a
// handler can end up written but unreachable.
func routeServed(router *chi.Mux, method, path string) bool {
	r := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)
	return rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed
}
