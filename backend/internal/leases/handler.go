package leases

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"kodipms/internal/platform/config"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

type Handler struct {
	svc *Service
	cfg *config.Config
}

func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{svc: svc, cfg: cfg}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())
	if p == nil {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return
	}
	q, err := listQuery(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.List(r.Context(), p, q)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out.Leases, httpx.PageMeta(q.Page, out.Total))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	out, err := h.svc.Get(r.Context(), p, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) tenants(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	out, err := h.svc.Tenants(r.Context(), p, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) replaceTenants(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	var in ReplaceTenantsInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.ReplaceTenants(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())
	if p == nil {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return
	}
	var in CreateInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Create(r.Context(), p, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Created(w, out)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	var in UpdateInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Update(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), p, id, h.meta(r)); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) activate(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	var in ActivateInput
	if err := decodeOptional(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Activate(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) terminate(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	var in TerminateInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Terminate(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) renew(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	var in RenewInput
	if err := decodeOptional(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Renew(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Created(w, out)
}

func (h *Handler) principalID(w http.ResponseWriter, r *http.Request) (*rbac.Principal, uint64, bool) {
	p := rbac.FromContext(r.Context())
	if p == nil {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return nil, 0, false
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return nil, 0, false
	}
	return p, id, true
}

func (h *Handler) meta(r *http.Request) ClientMeta {
	return ClientMeta{
		IP:        httpx.ClientIP(r, h.cfg.TrustProxy),
		UserAgent: r.UserAgent(),
	}
}

func listQuery(r *http.Request) (ListQuery, error) {
	q := ListQuery{
		Search: r.URL.Query().Get("q"),
		Status: r.URL.Query().Get("status"),
		Page:   httpx.Pagination(r),
		Sort:   httpx.SortParam(r, leaseSortColumns, defaultLeaseSort),
	}
	for _, item := range []struct {
		name string
		dest *uint64
	}{{"property_id", &q.PropertyID}, {"unit_id", &q.UnitID}, {"tenant_id", &q.TenantID}} {
		id, err := queryID(r, item.name)
		if err != nil {
			return ListQuery{}, err
		}
		*item.dest = id
	}
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

func pathID(r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

// decodeOptional reads an action body when the client sends one. Activating or
// renewing a lease has no required fields, so an omitted body is valid.
func decodeOptional(w http.ResponseWriter, r *http.Request, dst any) error {
	err := httpx.Decode(w, r, dst)
	var e *httpx.Err
	if errors.As(err, &e) && e.Fields["body"] == "required" {
		return nil
	}
	return err
}
