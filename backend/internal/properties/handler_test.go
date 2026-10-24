package properties

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
		{"list", http.MethodGet, "/properties", "", h.list},
		{"lookups", http.MethodGet, "/properties/lookups", "", h.lookups},
		{"get", http.MethodGet, "/properties/1", "", h.get},
		{"create", http.MethodPost, "/properties", `{}`, h.create},
		{"update", http.MethodPatch, "/properties/1", `{}`, h.update},
		{"delete", http.MethodDelete, "/properties/1", "", h.delete},
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
		{"list", http.MethodGet, "/properties", "", "", h.list},
		{"lookups", http.MethodGet, "/properties/lookups", "", "", h.lookups},
		{"get", http.MethodGet, "/properties/1", "1", "", h.get},
		{"create", http.MethodPost, "/properties", "", `{}`, h.create},
		{"update", http.MethodPatch, "/properties/1", "1", `{}`, h.update},
		{"delete", http.MethodDelete, "/properties/1", "1", "", h.delete},
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

func TestCreateBodyValidation(t *testing.T) {
	h := newTestHandler()
	p := principalWith("properties.create")
	cases := []struct {
		name  string
		body  string
		code  string
		field string
	}{
		{"empty body", "", "validation_failed", "body"},
		{"malformed json", "{", "bad_request", ""},
		{"unknown field", `{"unknown":true}`, "validation_failed", "body"},
		{"missing required fields", `{"owner_id":1,"type_id":2}`, "validation_failed", "name"},
		{"zero owner", `{"owner_id":0,"type_id":2,"name":"N","address":"A","county":"C"}`, "validation_failed", "owner_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := withPrincipal(httptest.NewRequest(http.MethodPost, "/properties", strings.NewReader(tc.body)), p)
			rec := httptest.NewRecorder()
			h.create(rec, req)
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

func TestUpdateBodyAndPathValidation(t *testing.T) {
	h := newTestHandler()
	p := principalWith("properties.update")

	req := withPrincipal(requestWithID(http.MethodPatch, "1", "{"), p)
	rec := httptest.NewRecorder()
	h.update(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", rec.Code)
	}

	req = withPrincipal(requestWithID(http.MethodPatch, "1", `{"unknown":1}`), p)
	rec = httptest.NewRecorder()
	h.update(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown field status = %d, want 422", rec.Code)
	}

	req = withPrincipal(requestWithID(http.MethodPatch, "abc", `{}`), p)
	rec = httptest.NewRecorder()
	h.update(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bad id status = %d, want 404", rec.Code)
	}
}

func TestGetBadPathID(t *testing.T) {
	h := newTestHandler()
	p := principalWith("properties.view")
	for _, id := range []string{"abc", "0", "-1"} {
		req := withPrincipal(httptest.NewRequest(http.MethodGet, "/properties/"+id, nil), p)
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
			"/properties?q=kilimani&status=Active&type_id=2&owner_id=3&county=Nairobi&sort=-name&page=2&per_page=10", nil)
		q, err := listQuery(req)
		if err != nil {
			t.Fatal(err)
		}
		if q.Search != "kilimani" || q.Status != "Active" || q.TypeID != 2 || q.OwnerID != 3 || q.County != "Nairobi" {
			t.Fatalf("filters = %+v", q)
		}
		if q.Sort != "p.name DESC" {
			t.Fatalf("sort = %q", q.Sort)
		}
		if q.Page.Page != 2 || q.Page.PerPage != 10 || q.Page.Limit != 10 || q.Page.Offset != 10 {
			t.Fatalf("page = %+v", q.Page)
		}
	})

	t.Run("applies defaults", func(t *testing.T) {
		q, err := listQuery(httptest.NewRequest(http.MethodGet, "/properties", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Page.Page != 1 || q.Page.PerPage != 25 || q.Page.Limit != 25 || q.Page.Offset != 0 {
			t.Fatalf("page = %+v", q.Page)
		}
		if q.Sort != defaultPropertySort {
			t.Fatalf("sort = %q", q.Sort)
		}
	})

	t.Run("rejects invalid ids", func(t *testing.T) {
		for _, target := range []struct {
			path  string
			field string
		}{
			{"/properties?type_id=abc", "type_id"},
			{"/properties?owner_id=0", "owner_id"},
			{"/properties?type_id=-1", "type_id"},
		} {
			_, err := listQuery(httptest.NewRequest(http.MethodGet, target.path, nil))
			var e *httpx.Err
			if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity || e.Fields[target.field] == "" {
				t.Fatalf("%s missing field %s: %v", target.path, target.field, err)
			}
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
		req := httptest.NewRequest(http.MethodGet, "/properties/"+tc.raw, nil)
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
		{http.MethodGet, "/properties"},
		{http.MethodGet, "/properties/lookups"},
		{http.MethodGet, "/properties/1"},
		{http.MethodPost, "/properties"},
		{http.MethodPatch, "/properties/1"},
		{http.MethodDelete, "/properties/1"},
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
	req := httptest.NewRequest(method, "/properties/"+id, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}
