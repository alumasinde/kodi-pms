package routes

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"kodipms/internal/auth"
	"kodipms/internal/billing"
	"kodipms/internal/leases"
	"kodipms/internal/mpesa"
	"kodipms/internal/platform/cache"
	"kodipms/internal/platform/config"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/platform/middleware"
	"kodipms/internal/platform/pii"
	"kodipms/internal/properties"
	"kodipms/internal/rbac"
	"kodipms/internal/settings"
	"kodipms/internal/tenancy"
	"kodipms/internal/units"
	"kodipms/internal/users"
	"kodipms/internal/utilities"
)

type Deps struct {
	Config            *config.Config
	Logger            *slog.Logger
	DB                *sqlx.DB
	Store             cache.Store
	AuthHandler       *auth.Handler
	Authenticator     *auth.Authenticator
	SettingsHandler   *settings.Handler
	PropertiesHandler *properties.Handler
	UnitsHandler      *units.Handler
	UsersHandler      *users.Handler
	LeasesHandler     *leases.Handler
	BillingHandler    *billing.Handler
	UtilitiesHandler  *utilities.Handler
	MpesaHandler      *mpesa.Handler
	PII               *pii.Cipher
	PIIAccess         *pii.AccessLog
}

func New(deps Deps) http.Handler {
	tenancyMW := tenancy.NewMiddleware(tenancy.NewResolver(deps.DB, deps.Store, deps.Logger), deps.Config.TrustProxy)
	userRateLimit := middleware.RateLimitUser(deps.Store, deps.Config.RateLimitUser, time.Minute)
	ipRateLimit := middleware.RateLimitIP(deps.Store, deps.Config.RateLimitIP, time.Minute, deps.Config.TrustProxy)

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
		api.Use(ipRateLimit)
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
			deps.PropertiesHandler.Routes(pr)
			deps.UnitsHandler.Routes(pr)
			deps.UsersHandler.Routes(pr)
			deps.LeasesHandler.Routes(pr)
			deps.BillingHandler.Routes(pr)
			deps.UtilitiesHandler.Routes(pr)
			deps.MpesaHandler.Routes(pr)
		})
	})

	// Safaricom's callbacks, on the root router. A webhook carries no host that
	// resolves an organization and no bearer token, so it cannot pass the tenancy
	// resolver or the auth middleware; it is authenticated by the token in its own
	// path. It is still IP-limited, because a URL that needs no credential is a URL
	// somebody will probe.
	r.Group(func(wh chi.Router) {
		wh.Use(ipRateLimit)
		deps.MpesaHandler.WebhookRoutes(wh)
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, httpx.ErrNotFound)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, &httpx.Err{Status: 405, Code: "method_not_allowed", Message: "Method not allowed"})
	})

	return r
}
