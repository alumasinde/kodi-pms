package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"kodipms/internal/audit"
	"kodipms/internal/platform/cache"
	"kodipms/internal/platform/config"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/platform/mailer"
	"kodipms/internal/rbac"
	"kodipms/internal/tenancy"
)

const (
	maxLoginFailures = 5
	loginLockWindow  = 15 * time.Minute
	refreshGrace     = 30 * time.Second
	resetTokenTTL    = time.Hour
	inviteTokenTTL   = 7 * 24 * time.Hour
	resetQueueSize   = 128
)

var (
	errInvalidCredentials = &httpx.Err{Status: 401, Code: "invalid_credentials", Message: "Invalid email/phone or password"}
	errInvalidPIN         = &httpx.Err{Status: 401, Code: "invalid_credentials", Message: "Invalid email/phone or PIN"}
	errWrongPassword      = &httpx.Err{Status: 400, Code: "invalid_password", Message: "Current password is incorrect"}
	errInvalidUserToken   = &httpx.Err{Status: 400, Code: "invalid_token", Message: "Invalid or expired token"}
	errPINDeviceRequired  = &httpx.Err{Status: 401, Code: "pin_device_required", Message: "Sign in with your password on this device before using a PIN"}
	errDeliveryFailed     = &httpx.Err{Status: 502, Code: "delivery_failed", Message: "The message could not be delivered"}
)

type Service struct {
	cfg    *config.Config
	db     *sqlx.DB
	repo   *repository
	tokens *TokenService
	loader *rbac.Loader
	audit  *audit.Logger
	store  cache.Store
	mail   *mailer.Mailer
	log    *slog.Logger

	resetQueue chan resetJob
}

type resetJob struct {
	org  *tenancy.Organization
	who  string
	meta ClientMeta
}

func NewService(cfg *config.Config, db *sqlx.DB, tokens *TokenService, loader *rbac.Loader, auditLog *audit.Logger, store cache.Store, mail *mailer.Mailer, log *slog.Logger) *Service {
	s := &Service{
		cfg:        cfg,
		db:         db,
		repo:       &repository{db: db},
		tokens:     tokens,
		loader:     loader,
		audit:      auditLog,
		store:      store,
		mail:       mail,
		log:        log,
		resetQueue: make(chan resetJob, resetQueueSize),
	}
	go s.resetWorker()
	return s
}

func (s *Service) Login(ctx context.Context, org *tenancy.Organization, in LoginInput, meta ClientMeta) (*SessionDTO, error) {
	who := strings.TrimSpace(in.Who())
	if org == nil || who == "" || in.Password == "" {
		fields := map[string]string{}
		if who == "" {
			fields["identifier"] = "required"
		}
		if in.Password == "" {
			fields["password"] = "required"
		}
		return nil, httpx.Validation(fields)
	}
	who = normalizeIdentifier(who)
	lockKey := loginKey(meta.IP, who)
	if s.loginLocked(ctx, lockKey) {
		return nil, httpx.ErrTooMany
	}

	u, err := s.repo.FindUserByIdentifier(ctx, org.ID, who)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if u != nil && u.LockedUntil.Valid && time.Now().Before(u.LockedUntil.Time) {
		s.auditLockout(ctx, org, u, who, meta, "login")
		return nil, httpx.ErrTooMany
	}

	hash := dummyHash
	if u != nil {
		hash = u.PasswordHash
	}
	passwordOK := ComparePassword(hash, in.Password)
	if u == nil || !passwordOK || u.Status != "active" {
		return nil, s.failLogin(ctx, org, u, who, meta)
	}

	allowed, err := s.userInOrg(ctx, u, org)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, s.failLogin(ctx, org, u, who, meta)
	}

	p, err := s.loader.Load(ctx, u.ID, org.ID)
	if err != nil {
		var he *httpx.Err
		if errors.As(err, &he) {
			return nil, s.failLogin(ctx, org, u, who, meta)
		}
		return nil, err
	}

	return s.issueSession(ctx, org, u, p, meta, "login", lockKey, "")
}

// LoginWithPIN is the quick-login path. The PIN only works on a device that
// already holds a live refresh token from a password login, so rotating IPs
// cannot brute-force a short PIN.
func (s *Service) LoginWithPIN(ctx context.Context, org *tenancy.Organization, in PinLoginInput, deviceToken string, meta ClientMeta) (*SessionDTO, error) {
	who := normalizeIdentifier(strings.TrimSpace(in.Who()))
	if org == nil || who == "" {
		return nil, httpx.Validation(map[string]string{"identifier": "required"})
	}
	if err := ValidatePIN(in.Pin); err != nil {
		return nil, err
	}
	deviceToken = strings.TrimSpace(deviceToken)
	if deviceToken == "" {
		return nil, errPINDeviceRequired
	}

	lockKey := pinKey(meta.IP, who)
	if s.loginLocked(ctx, lockKey) {
		return nil, httpx.ErrTooMany
	}

	u, err := s.repo.FindUserByIdentifier(ctx, org.ID, who)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if u != nil && u.LockedUntil.Valid && time.Now().Before(u.LockedUntil.Time) {
		s.auditLockout(ctx, org, u, who, meta, "pin_login")
		return nil, httpx.ErrTooMany
	}
	if u == nil || u.Status != "active" || u.MustChangePassword {
		return nil, s.failPIN(ctx, org, u, who, meta)
	}

	device, err := s.repo.FindRefresh(ctx, HashToken(deviceToken))
	if errors.Is(err, sql.ErrNoRows) {
		s.auditPINDeviceRejected(ctx, org, u, meta)
		return nil, errPINDeviceRequired
	}
	if err != nil {
		return nil, err
	}
	if !deviceMatches(device, u.ID, org.ID) {
		s.auditPINDeviceRejected(ctx, org, u, meta)
		return nil, errPINDeviceRequired
	}

	hash := dummyHash
	if u.PinHash.Valid && u.PinHash.String != "" {
		hash = u.PinHash.String
	}
	if !ComparePassword(hash, in.Pin) {
		return nil, s.failPIN(ctx, org, u, who, meta)
	}

	allowed, err := s.userInOrg(ctx, u, org)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, s.failPIN(ctx, org, u, who, meta)
	}

	p, err := s.loader.Load(ctx, u.ID, org.ID)
	if err != nil {
		var he *httpx.Err
		if errors.As(err, &he) {
			return nil, s.failPIN(ctx, org, u, who, meta)
		}
		return nil, err
	}

	return s.issueSession(ctx, org, u, p, meta, "pin_login", lockKey, device.FamilyID)
}

func (s *Service) issueSession(ctx context.Context, org *tenancy.Organization, u *UserRow, p *rbac.Principal, meta ClientMeta, action, lockKey, revokeFamily string) (*SessionDTO, error) {
	access, _, exp, err := s.tokens.NewAccess(u.ID, org.ID, u.TokenVersion)
	if err != nil {
		return nil, err
	}
	refreshRaw, refreshHash, err := NewRefreshToken()
	if err != nil {
		return nil, err
	}
	family := uuid.NewString()

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.repo.InsertRefreshTx(ctx, tx, u.ID, org.ID, family, refreshHash, meta, time.Now().Add(s.cfg.RefreshTTL)); err != nil {
		return nil, err
	}
	if err := s.repo.TouchLoginTx(ctx, tx, u.ID); err != nil {
		return nil, err
	}
	if revokeFamily != "" {
		if err := s.repo.RevokeFamily(ctx, tx, revokeFamily); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if lockKey != "" {
		_ = s.store.Del(ctx, lockKey)
	}

	if err := s.audit.Log(ctx, audit.Entry{
		OrgID: org.ID, UserID: u.ID, Action: action, EntityType: "users", EntityID: u.ID,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	}); err != nil {
		s.log.Warn("write login audit", "action", action, "user_id", u.ID, "error", err)
	}

	return &SessionDTO{
		AccessToken:  access,
		TokenType:    "Bearer",
		ExpiresIn:    int64(time.Until(exp).Seconds()),
		ExpiresAt:    exp,
		RefreshToken: refreshRaw,
		Me:           buildMe(u, p, orgDTOFromTenancy(org)),
	}, nil
}

func (s *Service) Refresh(ctx context.Context, raw string, meta ClientMeta) (*SessionDTO, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, httpx.ErrUnauthorized
	}
	hash := HashToken(raw)

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rt, err := s.repo.FindRefreshForUpdate(ctx, tx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if rt.RevokedAt.Valid {
		return s.refreshAfterRevoked(ctx, tx, rt, meta)
	}
	if time.Now().After(rt.ExpiresAt) {
		return nil, httpx.ErrUnauthorized
	}
	if !rt.OrganizationID.Valid {
		_ = s.repo.RevokeFamily(ctx, tx, rt.FamilyID)
		_ = tx.Commit()
		return nil, httpx.ErrUnauthorized
	}
	orgID := uint64(rt.OrganizationID.Int64)

	u, err := s.repo.FindUserByID(ctx, rt.UserID)
	if err != nil || u.Status != "active" {
		_ = s.repo.RevokeFamily(ctx, tx, rt.FamilyID)
		_ = tx.Commit()
		return nil, httpx.ErrUnauthorized
	}
	org, err := s.repo.OrgByID(ctx, orgID)
	if err != nil || org.Status != "active" {
		_ = s.repo.RevokeFamily(ctx, tx, rt.FamilyID)
		_ = tx.Commit()
		return nil, httpx.ErrUnauthorized
	}

	if err := s.repo.RotateRefreshTx(ctx, tx, rt.ID); err != nil {
		return nil, err
	}
	newRaw, newHash, err := NewRefreshToken()
	if err != nil {
		return nil, err
	}
	if err := s.repo.InsertRefreshTx(ctx, tx, rt.UserID, orgID, rt.FamilyID, newHash, meta, time.Now().Add(s.cfg.RefreshTTL)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	access, _, exp, err := s.tokens.NewAccess(u.ID, orgID, u.TokenVersion)
	if err != nil {
		return nil, err
	}
	return &SessionDTO{
		AccessToken:  access,
		TokenType:    "Bearer",
		ExpiresIn:    int64(time.Until(exp).Seconds()),
		ExpiresAt:    exp,
		RefreshToken: newRaw,
	}, nil
}

func (s *Service) refreshAfterRevoked(ctx context.Context, tx *sqlx.Tx, rt *RefreshRow, meta ClientMeta) (*SessionDTO, error) {
	now := time.Now()
	if rt.RotatedAt.Valid && !rt.GraceUsedAt.Valid && now.Sub(rt.RotatedAt.Time) <= refreshGrace {
		live, err := s.repo.CountLiveFamilyTx(ctx, tx, rt.FamilyID)
		if err != nil {
			return nil, err
		}
		if live > 0 {
			used, err := s.repo.MarkGraceUsedTx(ctx, tx, rt.ID)
			if err != nil {
				return nil, err
			}
			if used {
				return s.issueGraceSession(ctx, tx, rt, meta)
			}
		}
	}

	if err := s.repo.RevokeFamily(ctx, tx, rt.FamilyID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	var orgID uint64
	if rt.OrganizationID.Valid {
		orgID = uint64(rt.OrganizationID.Int64)
	}
	if err := s.audit.Log(ctx, audit.Entry{
		OrgID: orgID, UserID: rt.UserID, Action: "refresh_reuse_detected",
		EntityType: "refresh_tokens", EntityID: rt.ID,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	}); err != nil {
		s.log.Warn("write refresh reuse audit", "user_id", rt.UserID, "error", err)
	}
	return nil, httpx.ErrUnauthorized
}

func (s *Service) issueGraceSession(ctx context.Context, tx *sqlx.Tx, rt *RefreshRow, meta ClientMeta) (*SessionDTO, error) {
	if !rt.OrganizationID.Valid {
		_ = s.repo.RevokeFamily(ctx, tx, rt.FamilyID)
		_ = tx.Commit()
		return nil, httpx.ErrUnauthorized
	}
	orgID := uint64(rt.OrganizationID.Int64)
	u, err := s.repo.FindUserByID(ctx, rt.UserID)
	if err != nil || u.Status != "active" {
		_ = s.repo.RevokeFamily(ctx, tx, rt.FamilyID)
		_ = tx.Commit()
		return nil, httpx.ErrUnauthorized
	}
	org, err := s.repo.OrgByID(ctx, orgID)
	if err != nil || org.Status != "active" {
		_ = s.repo.RevokeFamily(ctx, tx, rt.FamilyID)
		_ = tx.Commit()
		return nil, httpx.ErrUnauthorized
	}
	newRaw, newHash, err := NewRefreshToken()
	if err != nil {
		return nil, err
	}
	if err := s.repo.InsertRefreshTx(ctx, tx, rt.UserID, orgID, rt.FamilyID, newHash, meta, time.Now().Add(s.cfg.RefreshTTL)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	access, _, exp, err := s.tokens.NewAccess(u.ID, orgID, u.TokenVersion)
	if err != nil {
		return nil, err
	}
	if err := s.audit.Log(ctx, audit.Entry{
		OrgID: orgID, UserID: rt.UserID, Action: "refresh_grace_used",
		EntityType: "refresh_tokens", EntityID: rt.ID,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	}); err != nil {
		s.log.Warn("write refresh grace audit", "user_id", rt.UserID, "error", err)
	}
	return &SessionDTO{
		AccessToken:  access,
		TokenType:    "Bearer",
		ExpiresIn:    int64(time.Until(exp).Seconds()),
		ExpiresAt:    exp,
		RefreshToken: newRaw,
	}, nil
}

func (s *Service) Logout(ctx context.Context, p *rbac.Principal, raw string, meta ClientMeta) {
	if sess, ok := AccessSessionFrom(ctx); ok && sess.JTI != "" {
		ttl := time.Until(sess.ExpiresAt)
		if ttl > 0 {
			_ = s.store.Set(ctx, "jwt:deny:"+sess.JTI, "1", ttl)
		}
	}
	if strings.TrimSpace(raw) != "" {
		rt, err := s.repo.FindRefresh(ctx, HashToken(raw))
		if err == nil {
			_ = s.repo.RevokeFamily(ctx, s.db, rt.FamilyID)
		}
	}
	if p != nil {
		if err := s.audit.Log(ctx, audit.Entry{
			OrgID: p.OrgID, UserID: p.UserID, Action: "logout", EntityType: "users", EntityID: p.UserID,
			IPAddress: meta.IP, UserAgent: meta.UserAgent,
		}); err != nil {
			s.log.Warn("write logout audit", "user_id", p.UserID, "error", err)
		}
	}
}

// ForgotPassword always succeeds from the caller's point of view. The actual
// lookup, token write and delivery run on a bounded worker queue so the
// response neither leaks account existence through timing nor blocks on SMTP.
func (s *Service) ForgotPassword(ctx context.Context, org *tenancy.Organization, in ForgotPasswordInput, meta ClientMeta) {
	who := normalizeIdentifier(strings.TrimSpace(in.Who()))
	if who == "" || org == nil {
		return
	}
	select {
	case s.resetQueue <- resetJob{org: org, who: who, meta: meta}:
	default:
		s.log.Warn("password reset queue full; dropping request", "org_id", org.ID)
	}
}

func (s *Service) resetWorker() {
	for job := range s.resetQueue {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					s.log.Error("password reset worker panic", "panic", rec)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.deliverPasswordReset(ctx, job)
		}()
	}
}

func (s *Service) deliverPasswordReset(ctx context.Context, job resetJob) {
	u, err := s.repo.FindUserByIdentifier(ctx, job.org.ID, job.who)
	if err != nil || u.Status != "active" {
		return
	}
	allowed, err := s.userInOrg(ctx, u, job.org)
	if err != nil || !allowed {
		return
	}

	raw, hash, err := NewRefreshToken()
	if err != nil {
		s.log.Error("generate password reset token", "user_id", u.ID, "error", err)
		return
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		s.log.Error("begin password reset", "user_id", u.ID, "error", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.repo.InvalidateUserTokensTx(ctx, tx, u.ID, "password_reset"); err != nil {
		s.log.Error("invalidate password reset tokens", "user_id", u.ID, "error", err)
		return
	}
	if err := s.repo.InsertUserTokenTx(ctx, tx, u.ID, "password_reset", hash, time.Now().Add(resetTokenTTL)); err != nil {
		s.log.Error("insert password reset token", "user_id", u.ID, "error", err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.log.Error("commit password reset token", "user_id", u.ID, "error", err)
		return
	}

	link := s.link("/reset-password", raw)
	if s.mail != nil && s.mail.Enabled() {
		body := fmt.Sprintf("We received a request to reset your Kodi PMS password.\n\n%s\n\nThis link expires in one hour. If you did not request it, ignore this email.", link)
		if err := s.mail.Send(ctx, u.Email, "Reset your Kodi PMS password", body); err != nil {
			if err := s.audit.Log(ctx, audit.Entry{
				OrgID: job.org.ID, UserID: u.ID, Action: "password_reset_delivery_failed",
				EntityType: "users", EntityID: u.ID,
				IPAddress: job.meta.IP, UserAgent: job.meta.UserAgent,
			}); err != nil {
				s.log.Warn("write reset delivery audit", "user_id", u.ID, "error", err)
			}
			return
		}
	} else if s.cfg.IsDevelopment() {
		s.log.Info("password reset link (development only)", "user_id", u.ID, "url", link)
	} else {
		s.log.Error("SMTP is not configured; password reset was not delivered", "user_id", u.ID)
		return
	}

	if err := s.audit.Log(ctx, audit.Entry{
		OrgID: job.org.ID, UserID: u.ID, Action: "password_reset_requested",
		EntityType: "users", EntityID: u.ID,
		IPAddress: job.meta.IP, UserAgent: job.meta.UserAgent,
	}); err != nil {
		s.log.Warn("write password reset audit", "user_id", u.ID, "error", err)
	}
}

func (s *Service) ResetPassword(ctx context.Context, in ResetPasswordInput, meta ClientMeta) error {
	if strings.TrimSpace(in.Token) == "" {
		return errInvalidUserToken
	}
	if err := validatePasswordAs(in.Password, "password"); err != nil {
		return err
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return err
	}
	return s.consumeUserToken(ctx, in.Token, "password_reset", hash, false, meta)
}

func (s *Service) AcceptInvite(ctx context.Context, in AcceptInviteInput, meta ClientMeta) error {
	if strings.TrimSpace(in.Token) == "" {
		return errInvalidUserToken
	}
	if err := validatePasswordAs(in.Password, "password"); err != nil {
		return err
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return err
	}
	return s.consumeUserToken(ctx, in.Token, "invite", hash, true, meta)
}

func (s *Service) consumeUserToken(ctx context.Context, raw, purpose, passwordHash string, activate bool, meta ClientMeta) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	t, err := s.repo.FindUserTokenForUpdate(ctx, tx, HashToken(raw), purpose)
	if errors.Is(err, sql.ErrNoRows) {
		return errInvalidUserToken
	}
	if err != nil {
		return err
	}
	if t.UsedAt.Valid || time.Now().After(t.ExpiresAt) {
		return errInvalidUserToken
	}

	if err := s.repo.MarkUserTokenUsedTx(ctx, tx, t.ID); err != nil {
		return err
	}
	if err := s.repo.UpdatePasswordTx(ctx, tx, t.UserID, passwordHash, activate); err != nil {
		return err
	}
	// A reset or invite replaces the password, so the old convenience PIN must
	// not survive it.
	if err := s.repo.ClearPINHashTx(ctx, tx, t.UserID); err != nil {
		return err
	}
	if err := s.repo.DeleteUserRefreshTokensTx(ctx, tx, t.UserID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	if err := rbac.BumpUserAuthzVersion(ctx, s.store, t.UserID); err != nil {
		s.log.Warn("bump user authz version", "user_id", t.UserID, "error", err)
	}
	u, err := s.repo.FindUserByID(ctx, t.UserID)
	if err == nil && u.OrganizationID.Valid {
		orgID := uint64(u.OrganizationID.Int64)
		if err := s.audit.Log(ctx, audit.Entry{
			OrgID: orgID, UserID: t.UserID, Action: purpose + "_consumed", EntityType: "users", EntityID: t.UserID,
			IPAddress: meta.IP, UserAgent: meta.UserAgent,
		}); err != nil {
			s.log.Warn("write token consumption audit", "purpose", purpose, "user_id", t.UserID, "error", err)
		}
	}
	return nil
}

func (s *Service) ChangePassword(ctx context.Context, p *rbac.Principal, in ChangePasswordInput, meta ClientMeta) error {
	if in.CurrentPassword == "" {
		return httpx.Validation(map[string]string{"current_password": "required"})
	}
	if err := validatePasswordAs(in.NewPassword, "new_password"); err != nil {
		return err
	}
	u, err := s.repo.FindUserByID(ctx, p.UserID)
	if err != nil {
		return httpx.ErrUnauthorized
	}
	if !ComparePassword(u.PasswordHash, in.CurrentPassword) {
		return errWrongPassword
	}
	if ComparePassword(u.PasswordHash, in.NewPassword) {
		return httpx.Validation(map[string]string{"new_password": "must be different from the current password"})
	}
	hash, err := HashPassword(in.NewPassword)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.repo.UpdatePasswordTx(ctx, tx, p.UserID, hash, false); err != nil {
		return err
	}
	if err := s.repo.DeleteUserRefreshTokensTx(ctx, tx, p.UserID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	if sess, ok := AccessSessionFrom(ctx); ok && sess.JTI != "" {
		ttl := time.Until(sess.ExpiresAt)
		if ttl > 0 {
			_ = s.store.Set(ctx, "jwt:deny:"+sess.JTI, "1", ttl)
		}
	}
	if err := rbac.BumpUserAuthzVersion(ctx, s.store, p.UserID); err != nil {
		s.log.Warn("bump user authz version", "user_id", p.UserID, "error", err)
	}
	if err := s.audit.Log(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID, Action: "password_changed", EntityType: "users", EntityID: p.UserID,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	}); err != nil {
		s.log.Warn("write password change audit", "user_id", p.UserID, "error", err)
	}
	return nil
}

func (s *Service) SetPIN(ctx context.Context, p *rbac.Principal, in SetPINInput, meta ClientMeta) error {
	if p == nil {
		return httpx.ErrUnauthorized
	}
	if p.MustChangePassword {
		return httpx.ErrPasswordState
	}
	if in.CurrentPassword == "" {
		return httpx.Validation(map[string]string{"current_password": "required"})
	}
	if err := ValidatePIN(in.Pin); err != nil {
		return err
	}

	u, err := s.repo.FindUserByID(ctx, p.UserID)
	if err != nil {
		return httpx.ErrUnauthorized
	}
	if !ComparePassword(u.PasswordHash, in.CurrentPassword) {
		return errWrongPassword
	}
	hash, err := HashPassword(in.Pin)
	if err != nil {
		return err
	}
	if err := s.repo.UpdatePINHash(ctx, p.UserID, hash); err != nil {
		return err
	}

	if err := s.audit.Log(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID, Action: "pin_set", EntityType: "users", EntityID: p.UserID,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	}); err != nil {
		s.log.Warn("write pin audit", "user_id", p.UserID, "error", err)
	}
	return nil
}

func (s *Service) Invite(ctx context.Context, p *rbac.Principal, in InviteInput, meta ClientMeta) (*InviteDTO, error) {
	if p == nil || !p.Can("users.create") {
		return nil, httpx.ErrForbidden
	}
	firstName := strings.TrimSpace(in.FirstName)
	lastName := strings.TrimSpace(in.LastName)
	email := strings.ToLower(strings.TrimSpace(in.Email))
	phone := strings.TrimSpace(in.Phone)
	fields := map[string]string{}
	switch {
	case firstName == "":
		fields["first_name"] = "required"
	case len([]rune(firstName)) > 100:
		fields["first_name"] = "must be at most 100 characters"
	}
	switch {
	case lastName == "":
		fields["last_name"] = "required"
	case len([]rune(lastName)) > 100:
		fields["last_name"] = "must be at most 100 characters"
	}
	if email == "" {
		fields["email"] = "required"
	} else if addr, err := mail.ParseAddress(email); err != nil || !strings.EqualFold(addr.Address, email) {
		fields["email"] = "must be a valid email address"
	}
	if phone != "" && !validPhone(phone) {
		fields["phone"] = "must be an E.164 number, e.g. +254712345678"
	}
	if in.RoleID == 0 {
		fields["role_id"] = "required"
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	role, err := s.repo.RoleForInvite(ctx, in.RoleID, p.OrgID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.Validation(map[string]string{"role_id": "is not valid for this organization"})
	}
	if err != nil {
		return nil, err
	}
	if role.Name == "system_admin" {
		return nil, httpx.ErrForbidden
	}

	existing, err := s.repo.FindInviteUserByEmail(ctx, p.OrgID, email)
	if errors.Is(err, sql.ErrNoRows) {
		existing = nil
	} else if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.Status != "invited" || !existing.OrganizationID.Valid || uint64(existing.OrganizationID.Int64) != p.OrgID {
			return nil, httpx.Conflict("A user with this email already exists")
		}
	}
	taken, err := s.repo.PhoneTakenByOther(ctx, p.OrgID, phone, inviteUserID(existing))
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, httpx.Conflict("A user with this phone already exists")
	}

	var passwordHash string
	if existing == nil {
		temp, _, err := NewRefreshToken()
		if err != nil {
			return nil, err
		}
		passwordHash, err = HashPassword(temp)
		if err != nil {
			return nil, err
		}
	}

	tokenRaw, tokenHash, err := NewRefreshToken()
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().Add(inviteTokenTTL)

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var userID uint64
	if existing != nil {
		userID = existing.ID
		if err := s.repo.UpdateInvitedUserTx(ctx, tx, userID, firstName, lastName, phone); err != nil {
			return nil, err
		}
	} else {
		userID, err = s.repo.InsertInvitedUserTx(ctx, tx, p.OrgID, email, phone, passwordHash, firstName, lastName)
		if err != nil {
			return nil, err
		}
	}
	if err := s.repo.InvalidateUserTokensTx(ctx, tx, userID, "invite"); err != nil {
		return nil, err
	}
	if err := s.repo.InsertUserTokenTx(ctx, tx, userID, "invite", tokenHash, expiresAt); err != nil {
		return nil, err
	}
	if err := s.repo.AssignRoleTx(ctx, tx, userID, role.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	action := "user_invited"
	if existing != nil {
		action = "invite_resent"
	}
	if err := s.audit.Log(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID, Action: action, EntityType: "users", EntityID: userID,
		NewValues: map[string]any{"email": email, "role": role.Name},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	}); err != nil {
		s.log.Warn("write invite audit", "action", action, "user_id", userID, "error", err)
	}

	link := s.link("/accept-invite", tokenRaw)
	if s.mail != nil && s.mail.Enabled() {
		body := fmt.Sprintf("You have been invited to Kodi PMS.\n\nSet your password here:\n%s\n\nThis link expires in seven days.", link)
		// The invite is already committed; deliver it even if the client disconnects.
		mailCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		if err := s.mail.Send(mailCtx, email, "You are invited to Kodi PMS", body); err != nil {
			if auditErr := s.audit.Log(ctx, audit.Entry{
				OrgID: p.OrgID, UserID: p.UserID, Action: "invite_delivery_failed",
				EntityType: "users", EntityID: userID,
				IPAddress: meta.IP, UserAgent: meta.UserAgent,
			}); auditErr != nil {
				s.log.Warn("write invite delivery audit", "user_id", userID, "error", auditErr)
			}
			return nil, errDeliveryFailed
		}
	} else if s.cfg.IsDevelopment() {
		s.log.Info("invite link (development only)", "email", email, "url", link)
	} else {
		return nil, errDeliveryFailed
	}

	return &InviteDTO{UserID: userID, Email: email, ExpiresAt: expiresAt}, nil
}

func (s *Service) Me(ctx context.Context, p *rbac.Principal) (*MeDTO, error) {
	u, err := s.repo.FindUserByID(ctx, p.UserID)
	if err != nil {
		return nil, httpx.ErrUnauthorized
	}
	org, err := s.repo.OrgByID(ctx, p.OrgID)
	if err != nil {
		return nil, httpx.ErrUnauthorized
	}
	return buildMe(u, p, OrganizationDTO{
		ID:           org.ID,
		Name:         org.Name,
		Slug:         org.Slug,
		Domain:       org.Domain,
		CurrencyCode: org.CurrencyCode,
		Timezone:     org.Timezone,
	}), nil
}

func (s *Service) loginLocked(ctx context.Context, key string) bool {
	raw, err := s.store.Get(ctx, key)
	if err != nil {
		return false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	return err == nil && n >= maxLoginFailures
}

func (s *Service) failLogin(ctx context.Context, org *tenancy.Organization, u *UserRow, who string, meta ClientMeta) error {
	return s.failAttempt(ctx, org, u, who, meta, loginKey(meta.IP, who), "login", errInvalidCredentials)
}

func (s *Service) failPIN(ctx context.Context, org *tenancy.Organization, u *UserRow, who string, meta ClientMeta) error {
	return s.failAttempt(ctx, org, u, who, meta, pinKey(meta.IP, who), "pin_login", errInvalidPIN)
}

func (s *Service) failAttempt(ctx context.Context, org *tenancy.Organization, u *UserRow, who string, meta ClientMeta, lockKey, kind string, authErr error) error {
	s.throttle(ctx, lockKey)

	attempts := 0
	var locked sql.NullTime
	if u != nil {
		n, until, err := s.repo.RecordFailedLogin(ctx, u.ID, maxLoginFailures, loginLockWindow)
		if err != nil {
			s.log.Error("record failed login", "user_id", u.ID, "error", err)
		} else {
			attempts, locked = n, until
		}
	}

	action := kind + "_failed"
	if locked.Valid {
		action = kind + "_locked"
	}
	values := map[string]any{"identifier": who, "attempts": attempts}
	if locked.Valid {
		values["locked_until"] = locked.Time.UTC()
	}
	if err := s.audit.Log(ctx, audit.Entry{
		OrgID: orgIDOf(org), UserID: idOf(u), Action: action, EntityType: "users", EntityID: idOf(u),
		NewValues: values, IPAddress: meta.IP, UserAgent: meta.UserAgent,
	}); err != nil {
		s.log.Warn("write failed login audit", "action", action, "user_id", idOf(u), "error", err)
	}

	if locked.Valid {
		return httpx.ErrTooMany
	}
	return authErr
}

func (s *Service) auditLockout(ctx context.Context, org *tenancy.Organization, u *UserRow, who string, meta ClientMeta, kind string) {
	values := map[string]any{"identifier": who}
	if u.LockedUntil.Valid {
		values["locked_until"] = u.LockedUntil.Time.UTC()
	}
	if err := s.audit.Log(ctx, audit.Entry{
		OrgID: orgIDOf(org), UserID: u.ID, Action: kind + "_locked", EntityType: "users", EntityID: u.ID,
		NewValues: values, IPAddress: meta.IP, UserAgent: meta.UserAgent,
	}); err != nil {
		s.log.Warn("write lockout audit", "kind", kind, "user_id", u.ID, "error", err)
	}
}

func (s *Service) auditPINDeviceRejected(ctx context.Context, org *tenancy.Organization, u *UserRow, meta ClientMeta) {
	if err := s.audit.Log(ctx, audit.Entry{
		OrgID: orgIDOf(org), UserID: u.ID, Action: "pin_device_rejected", EntityType: "users", EntityID: u.ID,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	}); err != nil {
		s.log.Warn("write pin device audit", "user_id", u.ID, "error", err)
	}
}

func (s *Service) throttle(ctx context.Context, key string) {
	if _, err := s.store.IncrTTL(ctx, key, loginLockWindow); err != nil {
		s.log.Warn("increment throttle counter", "key", key, "error", err)
	}
}

func (s *Service) userInOrg(ctx context.Context, u *UserRow, org *tenancy.Organization) (bool, error) {
	if u.OrganizationID.Valid {
		return uint64(u.OrganizationID.Int64) == org.ID, nil
	}
	return s.repo.HasGlobalRole(ctx, u.ID, "system_admin")
}

func deviceMatches(rt *RefreshRow, userID, orgID uint64) bool {
	return rt.UserID == userID &&
		rt.OrganizationID.Valid &&
		uint64(rt.OrganizationID.Int64) == orgID &&
		!rt.RevokedAt.Valid &&
		time.Now().Before(rt.ExpiresAt)
}

func (s *Service) link(path, raw string) string {
	return s.cfg.PublicBaseURL + path + "?token=" + url.QueryEscape(raw)
}

func validPhone(phone string) bool {
	if len(phone) < 8 || len(phone) > 20 || phone[0] != '+' {
		return false
	}
	for i := 1; i < len(phone); i++ {
		if phone[i] < '0' || phone[i] > '9' {
			return false
		}
	}
	return true
}

func inviteUserID(u *InviteUserRow) uint64 {
	if u == nil {
		return 0
	}
	return u.ID
}

func idOf(u *UserRow) uint64 {
	if u == nil {
		return 0
	}
	return u.ID
}

func orgIDOf(org *tenancy.Organization) uint64 {
	if org == nil {
		return 0
	}
	return org.ID
}

func loginKey(ip, identifier string) string {
	return throttleKey("login", ip, identifier)
}

func pinKey(ip, identifier string) string {
	return throttleKey("pin", ip, identifier)
}

func throttleKey(kind, ip, identifier string) string {
	if len(identifier) > 120 {
		identifier = identifier[:120]
	}
	return "rl:" + kind + ":" + ip + ":" + identifier
}

func normalizeIdentifier(v string) string {
	v = strings.TrimSpace(v)
	if strings.Contains(v, "@") {
		return strings.ToLower(v)
	}
	return v
}

func validatePasswordAs(password, field string) error {
	err := ValidatePassword(password)
	if err == nil {
		return nil
	}
	var e *httpx.Err
	if errors.As(err, &e) && e.Fields["password"] != "" {
		return httpx.Validation(map[string]string{field: e.Fields["password"]})
	}
	return err
}

func buildMe(u *UserRow, p *rbac.Principal, org OrganizationDTO) *MeDTO {
	perms := make([]string, 0, len(p.Perms))
	for code := range p.Perms {
		perms = append(perms, code)
	}
	sort.Strings(perms)
	roles := append([]string(nil), p.Roles...)
	if roles == nil {
		roles = []string{}
	}
	props := append([]uint64(nil), p.Scope.PropertyIDs...)
	if props == nil {
		props = []uint64{}
	}
	phone := ""
	if u.Phone.Valid {
		phone = u.Phone.String
	}
	return &MeDTO{
		User: UserDTO{
			ID:        u.ID,
			FirstName: u.FirstName,
			LastName:  u.LastName,
			Email:     u.Email,
			Phone:     phone,
			Status:    u.Status,
			HasPIN:    u.PinHash.Valid && u.PinHash.String != "",
		},
		Organization: org,
		Roles:        roles,
		Permissions:  perms,
		Scope: ScopeDTO{
			AllProperties: p.Scope.AllProperties,
			PropertyIDs:   props,
			TenantID:      p.Scope.TenantID,
			ContractorID:  p.Scope.ContractorID,
		},
		MustChangePassword: p.MustChangePassword,
	}
}

func orgDTOFromTenancy(org *tenancy.Organization) OrganizationDTO {
	return OrganizationDTO{
		ID:           org.ID,
		Name:         org.Name,
		Slug:         org.Slug,
		Domain:       org.Domain,
		CurrencyCode: org.CurrencyCode,
		Timezone:     org.Timezone,
	}
}
