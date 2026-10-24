package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

type Err struct {
	Status  int               `json:"-"`
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func (e *Err) Error() string { return e.Message }

var (
	ErrUnauthorized  = &Err{Status: 401, Code: "unauthorized", Message: "Authentication required"}
	ErrInvalidToken  = &Err{Status: 401, Code: "invalid_token", Message: "Invalid or expired token"}
	ErrForbidden     = &Err{Status: 403, Code: "forbidden", Message: "You do not have permission"}
	ErrNotFound      = &Err{Status: 404, Code: "not_found", Message: "Resource not found"}
	ErrTooMany       = &Err{Status: 429, Code: "rate_limited", Message: "Too many requests, try again later"}
	ErrPasswordState = &Err{Status: 403, Code: "password_change_required", Message: "You must change your password before continuing"}
)

func Conflict(msg string) *Err {
	return &Err{Status: 409, Code: "conflict", Message: msg}
}

func Validation(fields map[string]string) *Err {
	return &Err{Status: 422, Code: "validation_failed", Message: "Invalid input", Fields: fields}
}

func BadRequest(msg string) *Err {
	return &Err{Status: 400, Code: "bad_request", Message: msg}
}

func Fail(w http.ResponseWriter, err error) {
	var e *Err
	if !errors.As(err, &e) {
		slog.Error("unhandled request error", "error", err)
		e = &Err{Status: 500, Code: "internal", Message: "Something went wrong"}
	}
	writeJSON(w, e.Status, map[string]any{"error": e})
}

func OK(w http.ResponseWriter, data any, meta any) {
	writeJSON(w, http.StatusOK, envelope{Data: data, Meta: meta})
}

func Created(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusCreated, envelope{Data: data})
}

func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

type envelope struct {
	Data any `json:"data"`
	Meta any `json:"meta,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// Decode reads a single JSON object, rejects unknown fields and oversized
// bodies, and maps parse failures to validation errors.
func Decode(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return Validation(map[string]string{"body": "required"})
		}
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) || errors.Is(err, io.ErrUnexpectedEOF) {
			return BadRequest("Malformed JSON body")
		}
		return Validation(map[string]string{"body": err.Error()})
	}
	if dec.More() {
		return BadRequest("Body must contain a single JSON object")
	}
	return nil
}

type Page struct {
	Page    int
	PerPage int
	Limit   int
	Offset  int
}

func Pagination(r *http.Request) Page {
	page := positiveInt(r.URL.Query().Get("page"), 1)
	perPage := positiveInt(r.URL.Query().Get("per_page"), 25)
	if perPage > 100 {
		perPage = 100
	}
	return Page{Page: page, PerPage: perPage, Limit: perPage, Offset: (page - 1) * perPage}
}

func positiveInt(raw string, def int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return def
	}
	return n
}

type Meta struct {
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
	Total   int `json:"total"`
}

func PageMeta(p Page, total int) Meta {
	return Meta{Page: p.Page, PerPage: p.PerPage, Total: total}
}

// SortParam turns ?sort=-created_at into an ORDER BY fragment, accepting only
// columns in the module whitelist. The default is used when the input is
// missing or invalid.
func SortParam(r *http.Request, allowed map[string]string, defaultSort string) string {
	raw := strings.TrimSpace(r.URL.Query().Get("sort"))
	if raw == "" {
		return defaultSort
	}
	dir := "ASC"
	key := raw
	if strings.HasPrefix(raw, "-") {
		dir = "DESC"
		key = raw[1:]
	} else if strings.HasPrefix(raw, "+") {
		key = raw[1:]
	}
	col, ok := allowed[key]
	if !ok {
		return defaultSort
	}
	return col + " " + dir
}
