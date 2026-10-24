package routes

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"kodipms/internal/auth"
	"kodipms/internal/platform/cache"
	"kodipms/internal/platform/config"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/platform/middleware"
	"kodipms/internal/platform/pii"
	"kodipms/internal/rbac"
	"kodipms/internal/settings"
	"kodipms/internal/tenancy"
)

type Deps struct {
	Config          *config.Config
	Logger          *slog.Logger
	DB              *sqlx.DB
	Store           cache.Store
	AuthHandler     *auth.Handler
	Authenticator   *auth.Authenticator
	SettingsHandler *settings.Handler
	PII             *pii.Cipher
	PIIAccess       *pii.AccessLog
}

func New(deps Deps) http.Handler {
	tenancyMW := tenancy.NewMiddleware(tenancy.NewResolver(deps.DB, deps.Store, deps.Logger), deps.Config.TrustProxy)
	userRateLimit := middleware.RateLimitUser(deps.Store, deps.Config.RateLimitUser, time.Minute)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recover(deps.Logger))
	r.Use(middleware.Logger(deps.Logger, deps.Config.TrustProxy))
	r.Use(middleware.CORS(deps.Config.CORSOrigins))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, map[string]string{"status": "ok"}, nil)
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := deps.DB.PingContext(ctx); err != nil {
			httpx.Fail(w, &httpx.Err{Status: 503, Code: "not_ready", Message: "Database is not reachable"})
			return
		}
		httpx.OK(w, map[string]string{"status": "ready"}, nil)
	})

	r.Route("/api/v1", func(api chi.Router) {
		api.Use(middleware.RateLimitIP(deps.Store, deps.Config.RateLimitIP, time.Minute, deps.Config.TrustProxy))
		api.Use(tenancyMW.Resolve)

		api.Route("/auth", func(ar chi.Router) {
			ar.Group(deps.AuthHandler.PublicRoutes)
			ar.Group(func(pr chi.Router) {
				pr.Use(deps.Authenticator.Authenticate)
				pr.Use(userRateLimit)
				deps.AuthHandler.ProtectedRoutes(pr)
			})
		})

		api.Group(func(pr chi.Router) {
			pr.Use(deps.Authenticator.Authenticate)
			pr.Use(userRateLimit)
			pr.Use(rbac.RequirePasswordChanged)
			deps.SettingsHandler.Routes(pr)
		})
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, httpx.ErrNotFound)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, &httpx.Err{Status: 405, Code: "method_not_allowed", Message: "Method not allowed"})
	})

	return r
}
