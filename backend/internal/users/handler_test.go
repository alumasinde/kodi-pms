package users

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
		{"list", http.MethodGet, "/users", "", h.list},
		{"lookups", http.MethodGet, "/users/lookups", "", h.lookups},
		{"get", http.MethodGet, "/users/1", "", h.get},
		{"create", http.MethodPost, "/users", `{}`, h.create},
		{"update", http.MethodPatch, "/users/1", `{}`, h.update},
		{"delete", http.MethodDelete, "/users/1", "", h.delete},
		{"list assignments", http.MethodGet, "/users/1/assignments", "", h.listAssignments},
		{"replace assignments", http.MethodPut, "/users/1/assignments", `{}`, h.replaceAssignments},
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
		{"list", http.MethodGet, "/users", "", "", h.list},
		{"lookups", http.MethodGet, "/users/lookups", "", "", h.lookups},
		{"get", http.MethodGet, "/users/1", "1", "", h.get},
		{"create", http.MethodPost, "/users", "", `{"email":"a@b.com"}`, h.create},
		{"update", http.MethodPatch, "/users/1", "1", `{"first_name":"John"}`, h.update},
		{"delete", http.MethodDelete, "/users/1", "1", "", h.delete},
		{"list assignments", http.MethodGet, "/users/1/assignments", "1", "", h.listAssignments},
		{"replace assignments", http.MethodPut, "/users/1/assignments", "1", `{}`, h.replaceAssignments},
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
	p := principalWith("users.create")
	cases := []struct {
		name  string
		body  string
		code  string
		field string
	}{
		{"empty body", "", "validation_failed", "body"},
		{"malformed json", "{", "bad_request", ""},
		{"unknown field", `{"unknown":true}`, "validation_failed", "body"},
		{"missing required fields", `{}`, "validation_failed", "email"},
		{"bad email", `{"email":"nope","first_name":"A","last_name":"B","password":"longenough1","role_ids":[1]}`, "validation_failed", "email"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := withPrincipal(httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(tc.body)), p)
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
	p := principalWith("users.update")

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

	for _, id := range []string{"abc", "0", "-1"} {
		req = withPrincipal(requestWithID(http.MethodPatch, id, `{"first_name":"John"}`), p)
		rec = httptest.NewRecorder()
		h.update(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("id %q status = %d, want 404", id, rec.Code)
		}
	}
}

func TestReplaceAssignmentsBodyValidation(t *testing.T) {
	h := newTestHandler()
	p := principalWith("users.update")

	req := withPrincipal(requestWithID(http.MethodPut, "9", `{"assignments":[{"property_id":1,"role":"gardener"}]}`), p)
	rec := httptest.NewRecorder()
	h.replaceAssignments(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (%s)", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Error httpx.Err `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Fields["assignments[0].role"] == "" {
		t.Fatalf("fields = %v", envelope.Error.Fields)
	}

	req = withPrincipal(requestWithID(http.MethodPut, "9", `{"assignments":[{"property_id":0}]}`), p)
	rec = httptest.NewRecorder()
	h.replaceAssignments(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing property id status = %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteOwnAccount(t *testing.T) {
	h := newTestHandler()
	p := &rbac.Principal{UserID: 9, OrgID: 1, Perms: map[string]struct{}{"users.delete": {}}}
	req := withPrincipal(requestWithID(http.MethodDelete, "9", ""), p)
	rec := httptest.NewRecorder()
	h.delete(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

func TestGetBadPathID(t *testing.T) {
	h := newTestHandler()
	p := principalWith("users.view")
	for _, id := range []string{"abc", "0", "-1"} {
		req := withPrincipal(httptest.NewRequest(http.MethodGet, "/users/"+id, nil), p)
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
			"/users?q=jane&status=Suspended&role_id=4&sort=-last_login_at&page=3&per_page=5", nil)
		q, err := listQuery(req)
		if err != nil {
			t.Fatal(err)
		}
		if q.Search != "jane" || q.Status != "Suspended" || q.RoleID != 4 {
			t.Fatalf("filters = %+v", q)
		}
		if q.Sort != "u.last_login_at DESC" {
			t.Fatalf("sort = %q", q.Sort)
		}
		if q.Page.Page != 3 || q.Page.PerPage != 5 || q.Page.Limit != 5 || q.Page.Offset != 10 {
			t.Fatalf("page = %+v", q.Page)
		}
	})

	t.Run("applies defaults", func(t *testing.T) {
		q, err := listQuery(httptest.NewRequest(http.MethodGet, "/users", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Page.Page != 1 || q.Page.PerPage != 25 || q.Page.Offset != 0 {
			t.Fatalf("page = %+v", q.Page)
		}
		if q.Sort != defaultUserSort {
			t.Fatalf("sort = %q", q.Sort)
		}
	})

	t.Run("rejects invalid ids", func(t *testing.T) {
		for _, path := range []string{"/users?role_id=abc", "/users?role_id=0"} {
			_, err := listQuery(httptest.NewRequest(http.MethodGet, path, nil))
			var e *httpx.Err
			if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity || e.Fields["role_id"] == "" {
				t.Fatalf("%s err = %v", path, err)
			}
		}
	})

	t.Run("unknown sort falls back to the default", func(t *testing.T) {
		q, err := listQuery(httptest.NewRequest(http.MethodGet, "/users?sort=password_hash", nil))
		if err != nil {
			t.Fatal(err)
		}
		if q.Sort != defaultUserSort {
			t.Fatalf("sort = %q, want %q", q.Sort, defaultUserSort)
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
		req := httptest.NewRequest(http.MethodGet, "/users/"+tc.raw, nil)
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
		{http.MethodGet, "/users"},
		{http.MethodGet, "/users/lookups"},
		{http.MethodGet, "/users/1"},
		{http.MethodGet, "/users/1/assignments"},
		{http.MethodPost, "/users"},
		{http.MethodPatch, "/users/1"},
		{http.MethodPut, "/users/1/assignments"},
		{http.MethodDelete, "/users/1"},
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
	return NewHandler(NewService(nil, nil, nil, nil, discardLogger()), &config.Config{})
}

func withPrincipal(r *http.Request, p *rbac.Principal) *http.Request {
	return r.WithContext(rbac.WithPrincipal(r.Context(), p))
}

func requestWithID(method, id, body string) *http.Request {
	req := httptest.NewRequest(method, "/users/"+id, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}
