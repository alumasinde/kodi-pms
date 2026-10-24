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

	"kodipms/internal/audit"
	"kodipms/internal/auth"
	"kodipms/internal/billing"
	"kodipms/internal/leases"
	"kodipms/internal/platform/cache"
	"kodipms/internal/platform/config"
	"kodipms/internal/platform/database"
	"kodipms/internal/platform/logger"
	"kodipms/internal/platform/mailer"
	"kodipms/internal/platform/pii"
	"kodipms/internal/properties"
	"kodipms/internal/rbac"
	"kodipms/internal/routes"
	"kodipms/internal/settings"
	"kodipms/internal/units"
	"kodipms/internal/users"
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

	mail, err := mailer.New(cfg.SMTPURL, cfg.MailFrom, log)
	if err != nil {
		log.Error("mailer unavailable", "error", err)
		os.Exit(1)
	}

	loader := rbac.NewLoader(db, store, log)
	auditLog := audit.NewLogger(db, log)
	tokens := auth.NewTokenService(cfg)
	authSvc := auth.NewService(cfg, db, tokens, loader, auditLog, store, mail, log)
	settingsSvc := settings.NewService(db, store, auditLog, log)
	propertiesSvc := properties.NewService(db, store, auditLog, log)

	piiCipher, err := pii.New(cfg.PIIEncryptionKey)
	if err != nil {
		log.Error("pii key invalid", "error", err)
		os.Exit(1)
	}
	piiAccess := pii.NewAccessLog(db, log)

	unitsSvc := units.NewService(db, store, auditLog, log)
	usersSvc := users.NewService(db, store, auditLog, piiCipher, log)
	leasesSvc := leases.NewService(db, auditLog, log)
	billingSvc := billing.NewService(db, auditLog, log)

	handler := routes.New(routes.Deps{
		Config:            cfg,
		Logger:            log,
		DB:                db,
		Store:             store,
		AuthHandler:       auth.NewHandler(authSvc, cfg),
		Authenticator:     auth.NewAuthenticator(tokens, loader, store, log),
		SettingsHandler:   settings.NewHandler(settingsSvc, cfg),
		PropertiesHandler: properties.NewHandler(propertiesSvc, cfg),
		UnitsHandler:      units.NewHandler(unitsSvc, cfg),
		UsersHandler:      users.NewHandler(usersSvc, cfg),
		LeasesHandler:     leases.NewHandler(leasesSvc, cfg),
		BillingHandler:    billing.NewHandler(billingSvc, cfg),
		PII:               piiCipher,
		PIIAccess:         piiAccess,
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	authSvc.StartTokenCleanup(sigCtx)
	leasesSvc.StartExpirySweep(sigCtx)
	billingSvc.StartBillingSweep(sigCtx)

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
