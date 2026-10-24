package rbac

import (
	"context"
	"net/http"
	"strings"

	"kodipms/internal/platform/httpx"
)

type Scope struct {
	OrgID         uint64   `json:"org_id"`
	AllProperties bool     `json:"all_properties"`
	PropertyIDs   []uint64 `json:"property_ids,omitempty"`
	TenantID      *uint64  `json:"tenant_id,omitempty"`
	ContractorID  *uint64  `json:"contractor_id,omitempty"`
}

type Principal struct {
	UserID             uint64              `json:"user_id"`
	OrgID              uint64              `json:"org_id"`
	Roles              []string            `json:"roles"`
	Perms              map[string]struct{} `json:"permissions"`
	Scope              Scope               `json:"scope"`
	MustChangePassword bool                `json:"must_change_password"`
	TokenVersion       uint64              `json:"token_version"`
}

func (p *Principal) Can(perm string) bool {
	if p == nil {
		return false
	}
	_, ok := p.Perms[perm]
	return ok
}

func (p *Principal) HasRole(name string) bool {
	if p == nil {
		return false
	}
	for _, r := range p.Roles {
		if r == name {
			return true
		}
	}
	return false
}

func (p *Principal) IsSystemAdmin() bool { return p.HasRole("system_admin") }

func (s Scope) PropertyFilter(column string) (string, []any) {
	if s.AllProperties {
		return "1=1", nil
	}
	if len(s.PropertyIDs) == 0 {
		return "1=0", nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(s.PropertyIDs)), ",")
	args := make([]any, len(s.PropertyIDs))
	for i, id := range s.PropertyIDs {
		args[i] = id
	}
	return column + " IN (" + placeholders + ")", args
}

func (s Scope) AllowsProperty(id uint64) bool {
	if s.AllProperties {
		return true
	}
	for _, p := range s.PropertyIDs {
		if p == id {
			return true
		}
	}
	return false
}

type ctxKey struct{}

func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}

func Require(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := FromContext(r.Context())
			if p == nil {
				httpx.Fail(w, httpx.ErrUnauthorized)
				return
			}
			if !p.Can(perm) {
				httpx.Fail(w, httpx.ErrForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequirePasswordChanged blocks normal application routes until an invited or
// reset user replaces the temporary password. Auth self-service routes are
// mounted without this guard.
func RequirePasswordChanged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := FromContext(r.Context())
		if p != nil && p.MustChangePassword {
			httpx.Fail(w, httpx.ErrPasswordState)
			return
		}
		next.ServeHTTP(w, r)
	})
}
