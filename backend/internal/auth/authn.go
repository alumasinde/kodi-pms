package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"kodipms/internal/platform/cache"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/platform/middleware"
	"kodipms/internal/rbac"
	"kodipms/internal/tenancy"
)

type AccessSession struct {
	JTI       string
	ExpiresAt time.Time
}

type accessSessionKey struct{}

func withAccessSession(ctx context.Context, s AccessSession) context.Context {
	return context.WithValue(ctx, accessSessionKey{}, s)
}

func AccessSessionFrom(ctx context.Context) (AccessSession, bool) {
	s, ok := ctx.Value(accessSessionKey{}).(AccessSession)
	return s, ok
}

type Authenticator struct {
	tokens *TokenService
	loader *rbac.Loader
	store  cache.Store
	log    *slog.Logger
}

func NewAuthenticator(tokens *TokenService, loader *rbac.Loader, store cache.Store, log *slog.Logger) *Authenticator {
	return &Authenticator{tokens: tokens, loader: loader, store: store, log: log}
}

func (a *Authenticator) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearerToken(r)
		if !ok {
			httpx.Fail(w, httpx.ErrUnauthorized)
			return
		}
		claims, err := a.tokens.ParseAccess(raw)
		if err != nil {
			httpx.Fail(w, httpx.ErrInvalidToken)
			return
		}
		userID, err := strconv.ParseUint(claims.Subject, 10, 64)
		if err != nil || userID == 0 || claims.ExpiresAt == nil {
			httpx.Fail(w, httpx.ErrInvalidToken)
			return
		}
		if claims.ID != "" {
			if v, err := a.store.Get(r.Context(), "jwt:deny:"+claims.ID); err == nil && v == "1" {
				httpx.Fail(w, httpx.ErrInvalidToken)
				return
			}
		}

		orgID := claims.OrgID
		hostOrg, hasHost := tenancy.FromContext(r.Context())
		if orgID == 0 && hasHost {
			orgID = hostOrg.ID
		}
		if orgID == 0 {
			httpx.Fail(w, httpx.ErrInvalidToken)
			return
		}

		p, err := a.loader.Load(r.Context(), userID, orgID)
		if err != nil {
			a.failLoad(w, err)
			return
		}
		if claims.TokenVersion != p.TokenVersion {
			a.log.Warn("access token version mismatch",
				"request_id", middleware.RequestIDFrom(r.Context()),
				"user_id", userID, "token_version", claims.TokenVersion,
				"current_version", p.TokenVersion)
			httpx.Fail(w, httpx.ErrInvalidToken)
			return
		}

		var switchOrgID uint64
		if rawHeader := strings.TrimSpace(r.Header.Get("X-Org-ID")); rawHeader != "" {
			id, err := strconv.ParseUint(rawHeader, 10, 64)
			if err != nil || id == 0 {
				httpx.Fail(w, httpx.Validation(map[string]string{"X-Org-ID": "must be a positive integer"}))
				return
			}
			if !p.IsSystemAdmin() {
				httpx.Fail(w, httpx.ErrForbidden)
				return
			}
			if id != orgID {
				switched, err := a.loader.Load(r.Context(), userID, id)
				if err != nil {
					a.failLoad(w, err)
					return
				}
				a.log.Info("system admin switched organization",
					"request_id", middleware.RequestIDFrom(r.Context()),
					"user_id", userID, "from_org", orgID, "to_org", id)
				p = switched
				orgID = id
				switchOrgID = id
			}
		}

		if hasHost && orgID != hostOrg.ID {
			if !p.IsSystemAdmin() || switchOrgID == 0 {
				httpx.Fail(w, httpx.ErrUnauthorized)
				return
			}
		}

		ctx := rbac.WithPrincipal(r.Context(), p)
		ctx = withAccessSession(ctx, AccessSession{JTI: claims.ID, ExpiresAt: claims.ExpiresAt.Time})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *Authenticator) failLoad(w http.ResponseWriter, err error) {
	var he *httpx.Err
	if errors.As(err, &he) {
		httpx.Fail(w, he)
		return
	}
	a.log.Error("load principal", "error", err)
	httpx.Fail(w, &httpx.Err{Status: 500, Code: "internal", Message: "Something went wrong"})
}

func bearerToken(r *http.Request) (string, bool) {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if raw == "" {
		return "", false
	}
	parts := strings.SplitN(raw, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return strings.TrimSpace(parts[1]), true
}
