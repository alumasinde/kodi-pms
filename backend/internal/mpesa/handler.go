package mpesa

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kodipms/internal/platform/config"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

// Handler serves the two surfaces this module has, and they are not the same kind
// of HTTP. The API half is a signed-in operator looking at paybills, routing
// references, the reconcile queue and prompts. The webhook half is Safaricom
// posting money at a URL that carries its only credential, the token in the path,
// and answers in a body shape the network dictated.
//
// The two are kept apart by routes.go: the API is mounted under /api/v1 behind the
// tenancy resolver, the webhooks are mounted on the root router in front of nothing.

type Handler struct {
	svc *Service
	cfg *config.Config
}

func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{svc: svc, cfg: cfg}
}

func (h *Handler) listShortcodes(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q, err := shortcodeQuery(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Shortcodes(r.Context(), p, q)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out.Shortcodes, httpx.PageMeta(q.Page, out.Total))
}

func (h *Handler) lookups(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	out, err := h.svc.Lookups(r.Context(), p)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) getShortcode(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	out, err := h.svc.Shortcode(r.Context(), p, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) createShortcode(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in CreateShortcodeInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.CreateShortcode(r.Context(), p, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Created(w, out)
}

func (h *Handler) updateShortcode(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	var in UpdateShortcodeInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.UpdateShortcode(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

// rotateToken is the only request in the product that prints a callback token.
func (h *Handler) rotateToken(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	out, err := h.svc.RotateToken(r.Context(), p, id, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) deleteShortcode(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	if err := h.svc.DeleteShortcode(r.Context(), p, id, h.meta(r)); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) listRefs(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q, err := refQuery(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Refs(r.Context(), p, q)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out.References, httpx.PageMeta(q.Page, out.Total))
}

func (h *Handler) createRef(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in CreateRefInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.CreateRef(r.Context(), p, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Created(w, out)
}

func (h *Handler) deleteRef(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	if err := h.svc.DeleteRef(r.Context(), p, id, h.meta(r)); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) listReceipts(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q, err := transactionQuery(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Transactions(r.Context(), p, q)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out.Transactions, httpx.PageMeta(q.Page, out.Total))
}

func (h *Handler) getReceipt(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	out, err := h.svc.Transaction(r.Context(), p, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) matchReceipt(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	var in MatchInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.MatchTransaction(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) ignoreReceipt(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	var in IgnoreInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.IgnoreTransaction(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) listPrompts(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q, err := stkQuery(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.STKRequests(r.Context(), p, q)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out.Requests, httpx.PageMeta(q.Page, out.Total))
}

func (h *Handler) getPrompt(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	out, err := h.svc.STKRequest(r.Context(), p, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) push(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in PushInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Push(r.Context(), p, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Created(w, out)
}

// validateReceipt answers the question a C2B validation asks about money that has
// not moved yet. The reply is the whole job, so a body this installation cannot
// read is refused rather than accepted: an accepted validation commits the
// network to a payment, and a refusal only costs the tenant a retry.
func (h *Handler) validateReceipt(w http.ResponseWriter, r *http.Request) {
	var in C2BValidationRequest
	raw, err := readCallback(w, r, &in)
	if err != nil {
		h.logBadCallback(r, "mpesa c2b validation body", err, raw)
		writeCallback(w, rejectCallback("This callback could not be read"))
		return
	}
	writeCallback(w, h.svc.ValidateReceipt(r.Context(), pathToken(r), in))
}

func (h *Handler) confirmReceipt(w http.ResponseWriter, r *http.Request) {
	var in C2BConfirmationRequest
	raw, err := readCallback(w, r, &in)
	if err != nil {
		h.logBadCallback(r, "mpesa c2b confirmation body", err, raw)
		writeCallback(w, rejectCallback("This callback could not be read, please retry"))
		return
	}
	writeCallback(w, h.svc.ConfirmReceipt(r.Context(), pathToken(r), in, raw, h.meta(r)))
}

func (h *Handler) stkCallback(w http.ResponseWriter, r *http.Request) {
	var in STKCallbackRequest
	raw, err := readCallback(w, r, &in)
	if err != nil {
		h.logBadCallback(r, "mpesa stk callback body", err, raw)
		writeCallback(w, rejectCallback("This callback could not be read, please retry"))
		return
	}
	writeCallback(w, h.svc.ReceiveSTKCallback(r.Context(), pathToken(r), in, raw, h.meta(r)))
}

func (h *Handler) meta(r *http.Request) ClientMeta {
	return ClientMeta{
		IP:        httpx.ClientIP(r, h.cfg.TrustProxy),
		UserAgent: r.UserAgent(),
	}
}

func (h *Handler) logBadCallback(r *http.Request, what string, err error, raw []byte) {
	h.svc.log.Error(what, "path", r.URL.Path, "error", err, "bytes", len(raw))
}

// readCallback reads a Daraja body and hands back both the decoded struct and the
// bytes it came from.
//
// It cannot use httpx.Decode: that decoder refuses unknown fields, which is right
// for a form our own frontend sends and fatal here, because Safaricom adds fields
// to these payloads without asking anybody, and a confirmation that fails to parse
// is money that never reaches a ledger. The raw bytes are returned so the receipt
// keeps exactly what the network said, including the fields no struct names.
func readCallback(w http.ResponseWriter, r *http.Request, dst any) ([]byte, error) {
	body := http.MaxBytesReader(w, r.Body, maxCallbackBody)
	defer func() { _ = body.Close() }()
	raw, err := io.ReadAll(body)
	if err != nil {
		return raw, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return raw, errors.New("empty callback body")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return raw, err
	}
	return raw, nil
}

// writeCallback answers in Safaricom's own envelope, at HTTP 200, always. A
// non-2xx tells the network the delivery failed and it retries the same receipt
// for hours; the module's verdict is in ResultCode, which is where Daraja reads it.
func writeCallback(w http.ResponseWriter, reply CallbackReply) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(reply)
}

func principal(w http.ResponseWriter, r *http.Request) (*rbac.Principal, bool) {
	p := rbac.FromContext(r.Context())
	if p == nil {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return nil, false
	}
	return p, true
}

func shortcodeQuery(r *http.Request) (ShortcodeQuery, error) {
	return ShortcodeQuery{
		Search:      r.URL.Query().Get("q"),
		Environment: r.URL.Query().Get("environment"),
		AccountType: r.URL.Query().Get("type"),
		Status:      r.URL.Query().Get("status"),
		Page:        httpx.Pagination(r),
		Sort:        httpx.SortParam(r, shortcodeSortColumns, defaultShortcodeSort),
	}, nil
}

func refQuery(r *http.Request) (RefQuery, error) {
	q := RefQuery{
		Search: r.URL.Query().Get("q"),
		Page:   httpx.Pagination(r),
		Sort:   httpx.SortParam(r, refSortColumns, defaultRefSort),
	}
	shortcodeID, err := queryID(r, "shortcode_id")
	if err != nil {
		return q, err
	}
	q.ShortcodeID = shortcodeID
	return q, nil
}

func transactionQuery(r *http.Request) (TransactionQuery, error) {
	q := TransactionQuery{
		Search: r.URL.Query().Get("q"),
		Status: r.URL.Query().Get("status"),
		Page:   httpx.Pagination(r),
		Sort:   httpx.SortParam(r, transactionSortColumns, defaultTransSort),
	}
	shortcodeID, err := queryID(r, "shortcode_id")
	if err != nil {
		return q, err
	}
	leaseID, err := queryID(r, "lease_id")
	if err != nil {
		return q, err
	}
	from, err := queryDate(r, "from")
	if err != nil {
		return q, err
	}
	to, err := queryDate(r, "to")
	if err != nil {
		return q, err
	}
	q.ShortcodeID = shortcodeID
	q.LeaseID = leaseID
	q.From = from
	q.To = to
	return q, nil
}

func stkQuery(r *http.Request) (STKQuery, error) {
	q := STKQuery{
		Search: r.URL.Query().Get("q"),
		Status: r.URL.Query().Get("status"),
		Page:   httpx.Pagination(r),
		Sort:   httpx.SortParam(r, stkSortColumns, defaultSTKSort),
	}
	leaseID, err := queryID(r, "lease_id")
	if err != nil {
		return q, err
	}
	from, err := queryDate(r, "from")
	if err != nil {
		return q, err
	}
	to, err := queryDate(r, "to")
	if err != nil {
		return q, err
	}
	q.LeaseID = leaseID
	q.From = from
	q.To = to
	return q, nil
}

func queryID(r *http.Request, name string) (uint64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, httpx.Validation(map[string]string{name: "must be a positive integer"})
	}
	return id, nil
}

func queryDate(r *http.Request, name string) (time.Time, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return time.Time{}, nil
	}
	value, ok := parseDate(raw)
	if !ok {
		return time.Time{}, httpx.Validation(map[string]string{name: "must be a date"})
	}
	return value, nil
}

func pathID(r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

// pathToken is the credential a webhook arrives with. It is taken from the URL and
// nothing else, never logged, and never compared in plaintext beyond the HMAC the
// service computes from it.
func pathToken(r *http.Request) string {
	return strings.TrimSpace(chi.URLParam(r, "token"))
}
