package units

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
		{"list", http.MethodGet, "/units", "", h.list},
		{"lookups", http.MethodGet, "/units/lookups", "", h.lookups},
		{"get", http.MethodGet, "/units/1", "", h.get},
		{"create", http.MethodPost, "/units", `{}`, h.create},
		{"generate", http.MethodPost, "/units/generate", `{}`, h.generate},
		{"update", http.MethodPatch, "/units/1", `{}`, h.update},
		{"delete", http.MethodDelete, "/units/1", "", h.delete},
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
		{"list", http.MethodGet, "/units", "", "", h.list},
		{"lookups", http.MethodGet, "/units/lookups", "", "", h.lookups},
		{"get", http.MethodGet, "/units/1", "1", "", h.get},
		{"create", http.MethodPost, "/units", "", `{"number":"101"}`, h.create},
		{"generate", http.MethodPost, "/units/generate", "", `{"count":5}`, h.generate},
		{"update", http.MethodPatch, "/units/1", "1", `{"rent":50000}`, h.update},
		{"delete", http.MethodDelete, "/units/1", "1", "", h.delete},
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
		{"create empty body", http.MethodPost, "/units", false, "units.create", "", "validation_failed", "body", h.create},
		{"create malformed json", http.MethodPost, "/units", false, "units.create", "{", "bad_request", "", h.create},
		{"create unknown field", http.MethodPost, "/units", false, "units.create", `{"unknown":true}`, "validation_failed", "body", h.create},
		{"create missing fields", http.MethodPost, "/units", false, "units.create", `{}`, "validation_failed", "property_id", h.create},
		{"create occupied", http.MethodPost, "/units", false, "units.create", `{"property_id":1,"type_id":1,"number":"1","status":"occupied"}`, "validation_failed", "status", h.create},
		{"generate empty body", http.MethodPost, "/units/generate", false, "units.create", "", "validation_failed", "body", h.generate},
		{"generate bad count", http.MethodPost, "/units/generate", false, "units.create", `{"property_id":1,"type_id":1,"count":0}`, "validation_failed", "count", h.generate},
		{"generate bad pad", http.MethodPost, "/units/generate", false, "units.create", `{"property_id":1,"type_id":1,"count":2,"pad":9}`, "validation_failed", "pad", h.generate},
		{"patch malformed json", http.MethodPatch, "/units/1", true, "units.update", `{`, "bad_request", "", h.update},
		{"patch unknown field", http.MethodPatch, "/units/1", true, "units.update", `{"unknown":1}`, "validation_failed", "body", h.update},
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

func TestUpdateBadPathID(t *testing.T) {
	h := newTestHandler()
	p := principalWith("units.update")
	for _, id := range []string{"abc", "0", "-1"} {
		req := withPrincipal(requestWithID(http.MethodPatch, id, `{"rent":50000}`), p)
		rec := httptest.NewRecorder()
		h.update(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("id %q status = %d, want 404", id, rec.Code)
		}
	}
}

func TestGetBadPathID(t *testing.T) {
	h := newTestHandler()
	p := principalWith("units.view")
	for _, id := range []string{"abc", "0", "-1"} {
		req := withPrincipal(httptest.NewRequest(http.MethodGet, "/units/"+id, nil), p)
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
			"/units?q=101&status=Vacant&property_id=4&type_id=2&sort=-rent&page=2&per_page=10", nil)
		q, err := listQuery(req)
		if err != nil {
			t.Fatal(err)
		}
		if q.Search != "101" || q.Status != "Vacant" || q.PropertyID != 4 || q.TypeID != 2 {
			t.Fatalf("filters = %+v", q)
		}
		if q.Sort != "u.rent DESC" {
			t.Fatalf("sort = %q", q.Sort)
		}
		if q.Page.Page != 2 || q.Page.PerPage != 10 || q.Page.Limit != 10 || q.Page.Offset != 10 {
			t.Fatalf("page = %+v", q.Page)
		}
	})

	t.Run("applies defaults", func(t *testing.T) {
		q, err := listQuery(httptest.NewRequest(http.MethodGet, "/units", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Page.Page != 1 || q.Page.PerPage != 25 || q.Page.Limit != 25 || q.Page.Offset != 0 {
			t.Fatalf("page = %+v", q.Page)
		}
		if q.Sort != defaultUnitSort {
			t.Fatalf("sort = %q", q.Sort)
		}
	})

	t.Run("rejects invalid ids", func(t *testing.T) {
		for _, target := range []struct {
			path  string
			field string
		}{
			{"/units?property_id=abc", "property_id"},
			{"/units?property_id=0", "property_id"},
			{"/units?type_id=-2", "type_id"},
		} {
			_, err := listQuery(httptest.NewRequest(http.MethodGet, target.path, nil))
			var e *httpx.Err
			if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity || e.Fields[target.field] == "" {
				t.Fatalf("%s missing field %s: %v", target.path, target.field, err)
			}
		}
	})

	t.Run("unknown sort falls back to the default", func(t *testing.T) {
		q, err := listQuery(httptest.NewRequest(http.MethodGet, "/units?sort=tenant_name", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Sort != defaultUnitSort {
			t.Fatalf("sort = %q, want %q", q.Sort, defaultUnitSort)
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
		req := httptest.NewRequest(http.MethodGet, "/units/"+tc.raw, nil)
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
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/units"},
		{http.MethodGet, "/units/lookups"},
		{http.MethodGet, "/units/1"},
		{http.MethodPost, "/units"},
		{http.MethodPost, "/units/generate"},
		{http.MethodPatch, "/units/1"},
		{http.MethodDelete, "/units/1"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

func newTestHandler() *Handler {
	return NewHandler(NewService(nil, nil, nil, nil), &config.Config{})
}

func withPrincipal(r *http.Request, p *rbac.Principal) *http.Request {
	return r.WithContext(rbac.WithPrincipal(r.Context(), p))
}

func requestWithID(method, id, body string) *http.Request {
	req := httptest.NewRequest(method, "/units/"+id, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}
