package users

import (
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
	httpx.OK(w, out.Users, httpx.PageMeta(q.Page, out.Total))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())
	if p == nil {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	out, err := h.svc.Get(r.Context(), p, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) lookups(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())
	if p == nil {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return
	}
	out, err := h.svc.Lookups(r.Context(), p)
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
	p := rbac.FromContext(r.Context())
	if p == nil {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
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
	p := rbac.FromContext(r.Context())
	if p == nil {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	if err := h.svc.Delete(r.Context(), p, id, h.meta(r)); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) listAssignments(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())
	if p == nil {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	out, err := h.svc.Assignments(r.Context(), p, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) replaceAssignments(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())
	if p == nil {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	var in ReplaceAssignmentsInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.ReplaceAssignments(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) meta(r *http.Request) ClientMeta {
	return ClientMeta{
		IP:        httpx.ClientIP(r, h.cfg.TrustProxy),
		UserAgent: r.UserAgent(),
	}
}

func listQuery(r *http.Request) (ListQuery, error) {
	page := httpx.Pagination(r)
	q := ListQuery{
		Search: r.URL.Query().Get("q"),
		Status: r.URL.Query().Get("status"),
		Page:   page,
		Sort:   httpx.SortParam(r, userSortColumns, defaultUserSort),
	}
	roleID, err := queryID(r, "role_id")
	if err != nil {
		return ListQuery{}, err
	}
	q.RoleID = roleID
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
