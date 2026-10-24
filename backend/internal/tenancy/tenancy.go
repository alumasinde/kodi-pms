package tenancy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/platform/cache"
	"kodipms/internal/platform/httpx"
)

var ErrUnknownDomain = &httpx.Err{
	Status:  404,
	Code:    "organization_not_found",
	Message: "No active organization is configured for this domain",
}

type Organization struct {
	ID           uint64 `json:"id" db:"id"`
	Name         string `json:"name" db:"org_name"`
	Slug         string `json:"slug" db:"slug"`
	CurrencyCode string `json:"currency_code" db:"currency_code"`
	Timezone     string `json:"timezone" db:"timezone"`
	Domain       string `json:"domain" db:"domain"`
}

type Resolver struct {
	db    *sqlx.DB
	store cache.Store
	log   *slog.Logger
	ttl   time.Duration
}

func NewResolver(db *sqlx.DB, store cache.Store, log *slog.Logger) *Resolver {
	return &Resolver{db: db, store: store, log: log, ttl: time.Hour}
}

func (r *Resolver) Resolve(ctx context.Context, host string) (*Organization, error) {
	host = NormalizeHost(host)
	if host == "" {
		return nil, ErrUnknownDomain
	}
	key := "domain:" + host
	if raw, err := r.store.Get(ctx, key); err == nil {
		var org Organization
		if json.Unmarshal([]byte(raw), &org) == nil {
			return &org, nil
		}
	}

	var org Organization
	err := r.db.GetContext(ctx, &org, `
		SELECT o.id, o.org_name, o.slug, o.currency_code, o.timezone, d.domain
		FROM organization_domains d
		JOIN organizations o ON o.id = d.organization_id
		WHERE d.domain = ? AND o.deleted_at IS NULL AND o.org_status = 'active'
		LIMIT 1`, host)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnknownDomain
	}
	if err != nil {
		return nil, err
	}
	if b, err := json.Marshal(org); err == nil {
		if err := r.store.Set(ctx, key, string(b), r.ttl); err != nil {
			r.log.Debug("cache organization domain", "error", err)
		}
	}
	return &org, nil
}

func NormalizeHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(host, ".")
}

type ctxKey struct{}

func WithOrganization(ctx context.Context, org *Organization) context.Context {
	return context.WithValue(ctx, ctxKey{}, org)
}

func FromContext(ctx context.Context) (*Organization, bool) {
	org, ok := ctx.Value(ctxKey{}).(*Organization)
	return org, ok
}

type Middleware struct {
	resolver   *Resolver
	trustProxy bool
}

func NewMiddleware(resolver *Resolver, trustProxy bool) *Middleware {
	return &Middleware{resolver: resolver, trustProxy: trustProxy}
}

func (m *Middleware) Resolve(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		org, err := m.resolver.Resolve(r.Context(), m.requestHost(r))
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithOrganization(r.Context(), org)))
	})
}

func (m *Middleware) requestHost(r *http.Request) string {
	if m.trustProxy {
		if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
			parts := strings.Split(fwd, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				if host := strings.TrimSpace(parts[i]); host != "" {
					return host
				}
			}
		}
	}
	return r.Host
}
