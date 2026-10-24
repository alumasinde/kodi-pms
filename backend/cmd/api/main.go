package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"kodipms/internal/audit"
	"kodipms/internal/auth"
	"kodipms/internal/platform/cache"
	"kodipms/internal/platform/config"
	"kodipms/internal/platform/database"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/platform/logger"
	"kodipms/internal/platform/middleware"
	"kodipms/internal/rbac"
	"kodipms/internal/tenancy"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}
	log := logger.New(cfg)
	slog.SetDefault(log)

	dsn, err := cfg.MySQLDSN()
	if err != nil {
		log.Error("build database DSN", "error", err)
		os.Exit(1)
	}
	ctx := context.Background()
	db, err := database.Connect(ctx, dsn)
	if err != nil {
		log.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	store, err := cache.New(ctx, cfg, log)
	if err != nil {
		log.Error("cache unavailable", "error", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	tenancyResolver := tenancy.NewResolver(db, store, log)
	tenancyMW := tenancy.NewMiddleware(tenancyResolver, cfg.TrustProxy)
	loader := rbac.NewLoader(db, store, log)
	auditLog := audit.NewLogger(db, log)
	tokens := auth.NewTokenService(cfg)
	authSvc := auth.NewService(cfg, db, tokens, loader, auditLog, store, log)
	authHandler := auth.NewHandler(authSvc, cfg)
	authn := auth.NewAuthenticator(tokens, loader, store, log)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recover(log))
	r.Use(middleware.Logger(log, cfg.TrustProxy))
	r.Use(middleware.CORS(cfg.CORSOrigins))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, map[string]string{"status": "ok"}, nil)
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(pingCtx); err != nil {
			httpx.Fail(w, &httpx.Err{Status: 503, Code: "not_ready", Message: "Database is not reachable"})
			return
		}
		httpx.OK(w, map[string]string{"status": "ready"}, nil)
	})

	r.Route("/api/v1", func(api chi.Router) {
		api.Use(middleware.RateLimitIP(store, cfg.RateLimitIP, time.Minute, cfg.TrustProxy))
		api.Use(tenancyMW.Resolve)

		api.Route("/auth", func(ar chi.Router) {
			ar.Group(authHandler.PublicRoutes)
			ar.Group(func(pr chi.Router) {
				pr.Use(authn.Authenticate)
				pr.Use(middleware.RateLimitUser(store, cfg.RateLimitUser, time.Minute))
				authHandler.ProtectedRoutes(pr)
			})
		})

	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, httpx.ErrNotFound)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, &httpx.Err{Status: 405, Code: "method_not_allowed", Message: "Method not allowed"})
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("api listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "error", err)
			stop()
		}
	}()

	<-sigCtx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown", "error", err)
	}
	log.Info("api stopped")
}
