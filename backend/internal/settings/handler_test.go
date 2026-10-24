package settings

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kodipms/internal/platform/config"
	"kodipms/internal/rbac"
)

func newTestHandler() *Handler {
	return NewHandler(NewService(nil, nil, nil, nil), &config.Config{})
}

func settingsRequest(method, body string, p *rbac.Principal) *http.Request {
	req := httptest.NewRequest(method, "/api/v1/settings", strings.NewReader(body))
	if p != nil {
		req = req.WithContext(rbac.WithPrincipal(req.Context(), p))
	}
	return req
}

type apiErrorBody struct {
	Error struct {
		Code   string            `json:"code"`
		Fields map[string]string `json:"fields"`
	} `json:"error"`
}

func decodeAPIError(t *testing.T, rec *httptest.ResponseRecorder) apiErrorBody {
	t.Helper()
	var body apiErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body
}

func settingsEditor() *rbac.Principal {
	return &rbac.Principal{
		UserID: 5,
		OrgID:  1,
		Perms:  map[string]struct{}{"settings.update": {}},
	}
}

func TestHandlerGetRequiresPrincipal(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler().get(rec, settingsRequest(http.MethodGet, "", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if body := decodeAPIError(t, rec); body.Error.Code != "unauthorized" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestHandlerGetRequiresPermission(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler().get(rec, settingsRequest(http.MethodGet, "", &rbac.Principal{OrgID: 1}))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if body := decodeAPIError(t, rec); body.Error.Code != "forbidden" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestHandlerUpdateRequiresPrincipal(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler().update(rec, settingsRequest(http.MethodPatch, `{}`, nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if body := decodeAPIError(t, rec); body.Error.Code != "unauthorized" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestHandlerUpdateRequiresPermission(t *testing.T) {
	viewer := &rbac.Principal{OrgID: 1, Perms: map[string]struct{}{"settings.view": {}}}
	rec := httptest.NewRecorder()
	newTestHandler().update(rec, settingsRequest(http.MethodPatch, `{}`, viewer))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if body := decodeAPIError(t, rec); body.Error.Code != "forbidden" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestHandlerUpdateRejectsBadBody(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantField  string
		wantValue  string
	}{
		{"empty body", "", http.StatusUnprocessableEntity, "validation_failed", "body", "required"},
		{"malformed json", "{", http.StatusBadRequest, "bad_request", "", ""},
		{"unknown field", `{"bogus":true}`, http.StatusUnprocessableEntity, "validation_failed", "body", ""},
		{"no sections", `{}`, http.StatusUnprocessableEntity, "validation_failed", "body", "at least one of organization or settings is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newTestHandler().update(rec, settingsRequest(http.MethodPatch, tt.body, settingsEditor()))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			body := decodeAPIError(t, rec)
			if body.Error.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q", body.Error.Code, tt.wantCode)
			}
			if tt.wantField == "" {
				return
			}
			got, ok := body.Error.Fields[tt.wantField]
			if !ok {
				t.Fatalf("missing field %q in %v", tt.wantField, body.Error.Fields)
			}
			if tt.wantValue != "" && got != tt.wantValue {
				t.Fatalf("field %q = %q, want %q", tt.wantField, got, tt.wantValue)
			}
		})
	}
}
