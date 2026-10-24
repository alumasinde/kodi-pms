package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPagination(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  Page
	}{
		{"defaults", "", Page{Page: 1, PerPage: 25, Limit: 25, Offset: 0}},
		{"explicit", "?page=2&per_page=10", Page{Page: 2, PerPage: 10, Limit: 10, Offset: 10}},
		{"invalid falls back", "?page=0&per_page=nope", Page{Page: 1, PerPage: 25, Limit: 25, Offset: 0}},
		{"negative falls back", "?page=-2&per_page=-5", Page{Page: 1, PerPage: 25, Limit: 25, Offset: 0}},
		{"caps per page", "?page=3&per_page=1000", Page{Page: 3, PerPage: 100, Limit: 100, Offset: 200}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/items"+tt.query, nil)
			if got := Pagination(req); got != tt.want {
				t.Fatalf("Pagination(%q) = %+v, want %+v", tt.query, got, tt.want)
			}
		})
	}
}

func TestSortParam(t *testing.T) {
	allowed := map[string]string{
		"created_at": "t.created_at",
		"name":       "t.name",
	}
	const def = "t.created_at DESC"
	tests := []struct {
		query string
		want  string
	}{
		{"", def},
		{"?sort=name", "t.name ASC"},
		{"?sort=%2Bname", "t.name ASC"},
		{"?sort=-name", "t.name DESC"},
		{"?sort=created_at", "t.created_at ASC"},
		{"?sort=-created_at", "t.created_at DESC"},
		{"?sort=%20name%20", "t.name ASC"},
		{"?sort=unknown", def},
		{"?sort=-", def},
		{"?sort=%2B", def},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/items"+tt.query, nil)
		if got := SortParam(req, allowed, def); got != tt.want {
			t.Fatalf("SortParam(%q) = %q, want %q", tt.query, got, tt.want)
		}
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		xff    string
		real   string
		trust  bool
		want   string
	}{
		{"direct", "203.0.113.9:1234", "", "", false, "203.0.113.9"},
		{"direct ignores forwarded", "203.0.113.9:1234", "1.2.3.4", "", false, "203.0.113.9"},
		{"trusts rightmost hop", "10.0.0.1:80", "1.2.3.4, 203.0.113.9", "", true, "203.0.113.9"},
		{"skips invalid hops", "10.0.0.1:80", "1.2.3.4, not-an-ip", "", true, "1.2.3.4"},
		{"hop with port", "10.0.0.1:80", "[2001:db8::2]:443", "", true, "2001:db8::2"},
		{"real ip", "10.0.0.1:80", "", "198.51.100.5", true, "198.51.100.5"},
		{"invalid headers fall back", "10.0.0.1:80", "bogus", "bogus", true, "10.0.0.1"},
		{"ipv6 remote", "[2001:db8::1]:443", "", "", false, "2001:db8::1"},
		{"unknown remote", "not-an-ip", "", "", false, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remote
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.real != "" {
				req.Header.Set("X-Real-IP", tt.real)
			}
			if got := ClientIP(req, tt.trust); got != tt.want {
				t.Fatalf("ClientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecode(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	decode := func(body string, dst any) error {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		return Decode(rec, req, dst)
	}

	t.Run("valid", func(t *testing.T) {
		var p payload
		if err := decode(`{"name":"Acme"} `, &p); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if p.Name != "Acme" {
			t.Fatalf("payload = %+v", p)
		}
	})

	t.Run("empty", func(t *testing.T) {
		var p payload
		err := decode("", &p)
		var apiErr *Err
		if !errors.As(err, &apiErr) || apiErr.Status != 422 || apiErr.Fields["body"] != "required" {
			t.Fatalf("error = %#v", err)
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		var p payload
		err := decode(`{"name":"Acme","bogus":1}`, &p)
		var apiErr *Err
		if !errors.As(err, &apiErr) || apiErr.Status != 422 {
			t.Fatalf("error = %#v", err)
		}
		if !strings.Contains(apiErr.Fields["body"], `unknown field "bogus"`) {
			t.Fatalf("fields = %v", apiErr.Fields)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		var p payload
		err := decode(`{"name":`, &p)
		var apiErr *Err
		if !errors.As(err, &apiErr) || apiErr.Status != 400 || apiErr.Code != "bad_request" {
			t.Fatalf("error = %#v", err)
		}
	})

	t.Run("trailing object", func(t *testing.T) {
		var p payload
		err := decode(`{"name":"a"}{"name":"b"}`, &p)
		var apiErr *Err
		if !errors.As(err, &apiErr) || apiErr.Status != 400 {
			t.Fatalf("error = %#v", err)
		}
		if apiErr.Message != "Body must contain a single JSON object" {
			t.Fatalf("message = %q", apiErr.Message)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		var p payload
		body := strings.Repeat(" ", (1<<20)+1) + `{"name":"Acme"}`
		err := decode(body, &p)
		var apiErr *Err
		if !errors.As(err, &apiErr) || apiErr.Status != 422 {
			t.Fatalf("error = %#v", err)
		}
		if !strings.Contains(apiErr.Fields["body"], "request body too large") {
			t.Fatalf("fields = %v", apiErr.Fields)
		}
	})
}
