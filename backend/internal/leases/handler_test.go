package leases

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

func TestHandlersRequireAuthentication(t *testing.T) {
	h := newTestHandler()
	cases := []struct {
		name    string
		method  string
		path    string
		body    string
		handler http.HandlerFunc
	}{
		{"list", http.MethodGet, "/leases", "", h.list},
		{"get", http.MethodGet, "/leases/1", "", h.get},
		{"tenants", http.MethodGet, "/leases/1/tenants", "", h.tenants},
		{"replace tenants", http.MethodPut, "/leases/1/tenants", `{}`, h.replaceTenants},
		{"create", http.MethodPost, "/leases", `{}`, h.create},
		{"update", http.MethodPatch, "/leases/1", `{}`, h.update},
		{"delete", http.MethodDelete, "/leases/1", "", h.delete},
		{"activate", http.MethodPost, "/leases/1/activate", "", h.activate},
		{"terminate", http.MethodPost, "/leases/1/terminate", `{}`, h.terminate},
		{"renew", http.MethodPost, "/leases/1/renew", "", h.renew},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			tc.handler(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandlersRequirePermission(t *testing.T) {
	h := newTestHandler()
	noPerms := principalWith()
	cases := []struct {
		name    string
		method  string
		path    string
		id      string
		body    string
		handler http.HandlerFunc
	}{
		{"list", http.MethodGet, "/leases", "", "", h.list},
		{"get", http.MethodGet, "/leases/1", "1", "", h.get},
		{"tenants", http.MethodGet, "/leases/1/tenants", "1", "", h.tenants},
		{"replace tenants", http.MethodPut, "/leases/1/tenants", "1", `{"tenants":[]}`, h.replaceTenants},
		{"create", http.MethodPost, "/leases", "", `{"unit_id":1}`, h.create},
		{"update", http.MethodPatch, "/leases/1", "1", `{"rent_amount":50000}`, h.update},
		{"delete", http.MethodDelete, "/leases/1", "1", "", h.delete},
		{"activate", http.MethodPost, "/leases/1/activate", "1", "", h.activate},
		{"terminate", http.MethodPost, "/leases/1/terminate", "1", `{"reason":"moved out"}`, h.terminate},
		{"renew", http.MethodPost, "/leases/1/renew", "1", `{"months":12}`, h.renew},
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
			tc.handler(rec, withPrincipal(req, noPerms))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestBodyValidation(t *testing.T) {
	h := newTestHandler()
	cases := []struct {
		name    string
		method  string
		path    string
		withID  bool
		perm    string
		body    string
		code    string
		field   string
		handler http.HandlerFunc
	}{
		{"create empty body", http.MethodPost, "/leases", false, "leases.create", "", "validation_failed", "body", h.create},
		{"create malformed json", http.MethodPost, "/leases", false, "leases.create", "{", "bad_request", "", h.create},
		{"create unknown field", http.MethodPost, "/leases", false, "leases.create", `{"unknown":true}`, "validation_failed", "body", h.create},
		{"create missing fields", http.MethodPost, "/leases", false, "leases.create", `{}`, "validation_failed", "unit_id", h.create},
		{"create zero rent", http.MethodPost, "/leases", false, "leases.create",
			`{"unit_id":1,"start_date":"2026-01-01","rent_amount":0}`, "validation_failed", "rent_amount", h.create},
		{"create bad date", http.MethodPost, "/leases", false, "leases.create",
			`{"unit_id":1,"start_date":"01-02-2026","rent_amount":45000}`, "validation_failed", "start_date", h.create},
		{"create bad tenant role", http.MethodPost, "/leases", false, "leases.create",
			`{"unit_id":1,"start_date":"2026-01-01","rent_amount":45000,"tenants":[{"tenant_id":1,"role":"friend"}]}`,
			"validation_failed", "tenants", h.create},
		{"update malformed json", http.MethodPatch, "/leases/1", true, "leases.update", `{`, "bad_request", "", h.update},
		{"update status is not writable", http.MethodPatch, "/leases/1", true, "leases.update",
			`{"status":"active"}`, "validation_failed", "body", h.update},
		{"terminate missing reason", http.MethodPost, "/leases/1/terminate", true, "leases.terminate",
			`{}`, "validation_failed", "reason", h.terminate},
		{"terminate bad timestamp", http.MethodPost, "/leases/1/terminate", true, "leases.terminate",
			`{"reason":"moved out","terminated_at":"soon"}`, "validation_failed", "terminated_at", h.terminate},
		{"activate bad timestamp", http.MethodPost, "/leases/1/activate", true, "leases.activate",
			`{"signed_at":"last tuesday"}`, "validation_failed", "signed_at", h.activate},
		{"renew unknown field", http.MethodPost, "/leases/1/renew", true, "leases.renew",
			`{"unit_id":2}`, "validation_failed", "body", h.renew},
		{"renew bad months", http.MethodPost, "/leases/1/renew", true, "leases.renew",
			`{"months":0}`, "validation_failed", "months", h.renew},
		{"replace tenants zero id", http.MethodPut, "/leases/1/tenants", true, "leases.update",
			`{"tenants":[{"tenant_id":0}]}`, "validation_failed", "tenants", h.replaceTenants},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req *http.Request
			if tc.withID {
				req = requestWithID(tc.method, "1", tc.body)
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

func TestOptionalActionBodies(t *testing.T) {
	t.Run("activating without a body is allowed", func(t *testing.T) {
		bodies := []string{"", "{}"}
		for _, body := range bodies {
			var in ActivateInput
			req := httptest.NewRequest(http.MethodPost, "/leases/1/activate", strings.NewReader(body))
			if err := decodeOptional(httptest.NewRecorder(), req, &in); err != nil {
				t.Fatalf("body %q rejected: %v", body, err)
			}
			if in.SignedAt != nil {
				t.Fatalf("body %q set signed_at = %v", body, *in.SignedAt)
			}
		}
		var in RenewInput
		req := httptest.NewRequest(http.MethodPost, "/leases/1/renew", nil)
		if err := decodeOptional(httptest.NewRecorder(), req, &in); err != nil {
			t.Fatalf("nil body rejected: %v", err)
		}
		if in.Months != nil {
			t.Fatalf("nil body set months = %d", *in.Months)
		}
	})

	t.Run("an action body still parses", func(t *testing.T) {
		var in ActivateInput
		req := httptest.NewRequest(http.MethodPost, "/leases/1/activate", strings.NewReader(`{"signed_at":"2026-01-05T09:30:00Z"}`))
		if err := decodeOptional(httptest.NewRecorder(), req, &in); err != nil {
			t.Fatal(err)
		}
		if in.SignedAt == nil || *in.SignedAt != "2026-01-05T09:30:00Z" {
			t.Fatalf("signed_at = %v", in.SignedAt)
		}
	})

	t.Run("bad bodies are reported", func(t *testing.T) {
		var in RenewInput
		req := httptest.NewRequest(http.MethodPost, "/leases/1/renew", strings.NewReader(`{"months":`))
		err := decodeOptional(httptest.NewRecorder(), req, &in)
		var e *httpx.Err
		if !errors.As(err, &e) || e.Status != http.StatusBadRequest {
			t.Fatalf("err = %v, want 400", err)
		}

		req = httptest.NewRequest(http.MethodPost, "/leases/1/renew", strings.NewReader(`{"unit_id":5}`))
		err = decodeOptional(httptest.NewRecorder(), req, &in)
		if !errors.As(err, &e) || e.Fields["body"] == "" {
			t.Fatalf("err = %v, want an unknown-field rejection", err)
		}
	})

	t.Run("termination requires a body", func(t *testing.T) {
		var in TerminateInput
		err := httpx.Decode(httptest.NewRecorder(),
			httptest.NewRequest(http.MethodPost, "/leases/1/terminate", nil), &in)
		var e *httpx.Err
		if !errors.As(err, &e) || e.Fields["body"] != "required" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestUpdateBadPathID(t *testing.T) {
	h := newTestHandler()
	p := principalWith("leases.update")
	for _, id := range []string{"abc", "0", "-1"} {
		req := withPrincipal(requestWithID(http.MethodPatch, id, `{"rent_amount":50000}`), p)
		rec := httptest.NewRecorder()
		h.update(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("id %q status = %d, want 404", id, rec.Code)
		}
	}
}

func TestGetBadPathID(t *testing.T) {
	h := newTestHandler()
	p := principalWith("leases.view")
	for _, id := range []string{"abc", "0", "-1"} {
		req := withPrincipal(httptest.NewRequest(http.MethodGet, "/leases/"+id, nil), p)
		rec := httptest.NewRecorder()
		h.get(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("id %q status = %d, want 404", id, rec.Code)
		}
	}
}

func TestListQuery(t *testing.T) {
	t.Run("parses filters", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/leases?q=KD&status=Active&property_id=4&unit_id=21&tenant_id=12&sort=-rent&page=2&per_page=10", nil)
		q, err := listQuery(req)
		if err != nil {
			t.Fatal(err)
		}
		if q.Search != "KD" || q.Status != "Active" || q.PropertyID != 4 || q.UnitID != 21 || q.TenantID != 12 {
			t.Fatalf("filters = %+v", q)
		}
		if q.Sort != "l.rent_amount DESC" {
			t.Fatalf("sort = %q", q.Sort)
		}
		if q.Page.Page != 2 || q.Page.PerPage != 10 || q.Page.Limit != 10 || q.Page.Offset != 10 {
			t.Fatalf("page = %+v", q.Page)
		}
	})

	t.Run("applies defaults", func(t *testing.T) {
		q, err := listQuery(httptest.NewRequest(http.MethodGet, "/leases", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Page.Page != 1 || q.Page.PerPage != 25 || q.Page.Limit != 25 || q.Page.Offset != 0 {
			t.Fatalf("page = %+v", q.Page)
		}
		if q.Sort != defaultLeaseSort {
			t.Fatalf("sort = %q", q.Sort)
		}
	})

	t.Run("every whitelisted column sorts", func(t *testing.T) {
		for key, column := range leaseSortColumns {
			q, err := listQuery(httptest.NewRequest(http.MethodGet, "/leases?sort="+key, nil))
			if err != nil {
				t.Fatal(err)
			}
			if q.Sort != column+" ASC" {
				t.Fatalf("sort %s = %q, want %q", key, q.Sort, column+" ASC")
			}
		}
	})

	t.Run("rejects invalid ids", func(t *testing.T) {
		for _, target := range []struct {
			path  string
			field string
		}{
			{"/leases?property_id=abc", "property_id"},
			{"/leases?property_id=0", "property_id"},
			{"/leases?unit_id=-2", "unit_id"},
			{"/leases?tenant_id=0", "tenant_id"},
		} {
			_, err := listQuery(httptest.NewRequest(http.MethodGet, target.path, nil))
			var e *httpx.Err
			if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity || e.Fields[target.field] == "" {
				t.Fatalf("%s missing field %s: %v", target.path, target.field, err)
			}
		}
	})

	t.Run("unknown sort falls back to the default", func(t *testing.T) {
		q, err := listQuery(httptest.NewRequest(http.MethodGet, "/leases?sort=tenant_name", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Sort != defaultLeaseSort {
			t.Fatalf("sort = %q, want %q", q.Sort, defaultLeaseSort)
		}
	})
}

func TestPathID(t *testing.T) {
	cases := []struct {
		raw  string
		want uint64
		ok   bool
	}{
		{"42", 42, true},
		{"0", 0, false},
		{"", 0, false},
		{"abc", 0, false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/leases/"+tc.raw, nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", tc.raw)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		got, ok := pathID(req)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("pathID(%q) = %d, %v; want %d, %v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRoutesRequireAuthentication(t *testing.T) {
	h := newTestHandler()
	r := chi.NewRouter()
	h.Routes(r)
	for _, tc := range leaseRoutes() {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestRoutesEnforcePermissions(t *testing.T) {
	h := newTestHandler()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, withPrincipal(req, principalWith()))
		})
	})
	h.Routes(r)
	for _, tc := range leaseRoutes() {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s status = %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}

type routeCase struct {
	method string
	path   string
}

func leaseRoutes() []routeCase {
	return []routeCase{
		{http.MethodGet, "/leases"},
		{http.MethodGet, "/leases/1"},
		{http.MethodGet, "/leases/1/tenants"},
		{http.MethodPut, "/leases/1/tenants"},
		{http.MethodPost, "/leases"},
		{http.MethodPatch, "/leases/1"},
		{http.MethodDelete, "/leases/1"},
		{http.MethodPost, "/leases/1/activate"},
		{http.MethodPost, "/leases/1/terminate"},
		{http.MethodPost, "/leases/1/renew"},
	}
}

func newTestHandler() *Handler {
	return NewHandler(NewService(nil, nil, discardLogger()), &config.Config{})
}

func withPrincipal(r *http.Request, p *rbac.Principal) *http.Request {
	return r.WithContext(rbac.WithPrincipal(r.Context(), p))
}

func requestWithID(method, id, body string) *http.Request {
	req := httptest.NewRequest(method, "/leases/"+id, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}
