package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
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
	"kodipms/internal/rbac"
	"kodipms/internal/tenancy"
)

const (
	maxLoginFailures = 5
	loginLockWindow  = 15 * time.Minute
	resetTokenTTL    = time.Hour
)

var (
	errInvalidCredentials = &httpx.Err{Status: 401, Code: "invalid_credentials", Message: "Invalid email/phone or password"}
	errWrongPassword      = &httpx.Err{Status: 400, Code: "invalid_password", Message: "Current password is incorrect"}
	errInvalidUserToken   = &httpx.Err{Status: 400, Code: "invalid_token", Message: "Invalid or expired token"}
)

type Service struct {
	cfg    *config.Config
	db     *sqlx.DB
	repo   *repository
	tokens *TokenService
	loader *rbac.Loader
	audit  *audit.Logger
	store  cache.Store
	log    *slog.Logger
}

func NewService(cfg *config.Config, db *sqlx.DB, tokens *TokenService, loader *rbac.Loader, auditLog *audit.Logger, store cache.Store, log *slog.Logger) *Service {
	return &Service{
		cfg:    cfg,
		db:     db,
		repo:   &repository{db: db},
		tokens: tokens,
		loader: loader,
		audit:  auditLog,
		store:  store,
		log:    log,
	}
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

	u, err := s.repo.FindUserByIdentifier(ctx, who)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	hash := dummyHash
	if u != nil {
		hash = u.PasswordHash
	}
	passwordOK := ComparePassword(hash, in.Password)
	if u == nil || !passwordOK || u.Status != "active" {
		return nil, s.failLogin(ctx, lockKey)
	}

	if u.OrganizationID.Valid {
		if uint64(u.OrganizationID.Int64) != org.ID {
			return nil, s.failLogin(ctx, lockKey)
		}
	} else {
		admin, err := s.repo.HasGlobalRole(ctx, u.ID, "system_admin")
		if err != nil {
			return nil, err
		}
		if !admin {
			return nil, s.failLogin(ctx, lockKey)
		}
	}

	p, err := s.loader.Load(ctx, u.ID, org.ID)
	if err != nil {
		var he *httpx.Err
		if errors.As(err, &he) {
			return nil, s.failLogin(ctx, lockKey)
		}
		return nil, err
	}

	access, _, exp, err := s.tokens.NewAccess(u.ID, org.ID)
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
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_ = s.store.Del(ctx, lockKey)

	s.audit.Log(ctx, audit.Entry{
		OrgID: org.ID, UserID: u.ID, Action: "login", EntityType: "users", EntityID: u.ID,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})

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
		if err := s.repo.RevokeFamily(ctx, tx, rt.FamilyID); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, httpx.ErrUnauthorized
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

	if err := s.repo.RevokeRefreshTx(ctx, tx, rt.ID); err != nil {
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

	access, _, exp, err := s.tokens.NewAccess(rt.UserID, orgID)
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
		s.audit.Log(ctx, audit.Entry{
			OrgID: p.OrgID, UserID: p.UserID, Action: "logout", EntityType: "users", EntityID: p.UserID,
			IPAddress: meta.IP, UserAgent: meta.UserAgent,
		})
	}
}

// ForgotPassword always succeeds from the caller's point of view so account
// existence cannot be probed. Delivery is not configured yet: in development
// the reset link is logged, in other environments it is intentionally dropped.
func (s *Service) ForgotPassword(ctx context.Context, org *tenancy.Organization, in ForgotPasswordInput, meta ClientMeta) {
	who := normalizeIdentifier(strings.TrimSpace(in.Who()))
	if who == "" || org == nil {
		return
	}
	u, err := s.repo.FindUserByIdentifier(ctx, who)
	if err != nil || u.Status != "active" {
		return
	}
	if u.OrganizationID.Valid && uint64(u.OrganizationID.Int64) != org.ID {
		return
	}
	if !u.OrganizationID.Valid {
		admin, err := s.repo.HasGlobalRole(ctx, u.ID, "system_admin")
		if err != nil || !admin {
			return
		}
	}

	raw, hash, err := NewRefreshToken()
	if err != nil {
		return
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.repo.InvalidateUserTokensTx(ctx, tx, u.ID, "password_reset"); err != nil {
		return
	}
	if err := s.repo.InsertUserTokenTx(ctx, tx, u.ID, "password_reset", hash, time.Now().Add(resetTokenTTL)); err != nil {
		return
	}
	if err := tx.Commit(); err != nil {
		return
	}

	if s.cfg.IsDevelopment() {
		s.log.Info("password reset link (development only)",
			"user_id", u.ID,
			"url", fmt.Sprintf("%s/reset-password?token=%s", s.cfg.PublicBaseURL, raw))
	} else {
		s.log.Warn("password reset requested but no email/SMS delivery is configured", "user_id", u.ID)
	}
	s.audit.Log(ctx, audit.Entry{
		OrgID: org.ID, UserID: u.ID, Action: "password_reset_requested", EntityType: "users", EntityID: u.ID,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
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
	if err := s.repo.DeleteUserRefreshTokensTx(ctx, tx, t.UserID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	u, err := s.repo.FindUserByID(ctx, t.UserID)
	if err == nil && u.OrganizationID.Valid {
		orgID := uint64(u.OrganizationID.Int64)
		if err := rbac.BumpAuthzVersion(ctx, s.store, orgID); err != nil {
			s.log.Warn("bump authz version", "org_id", orgID, "error", err)
		}
		s.audit.Log(ctx, audit.Entry{
			OrgID: orgID, UserID: t.UserID, Action: purpose + "_consumed", EntityType: "users", EntityID: t.UserID,
			IPAddress: meta.IP, UserAgent: meta.UserAgent,
		})
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
	if err := rbac.BumpAuthzVersion(ctx, s.store, p.OrgID); err != nil {
		s.log.Warn("bump authz version", "org_id", p.OrgID, "error", err)
	}
	s.audit.Log(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID, Action: "password_changed", EntityType: "users", EntityID: p.UserID,
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return nil
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

func (s *Service) failLogin(ctx context.Context, key string) error {
	if _, err := s.store.Incr(ctx, key); err == nil {
		_ = s.store.Expire(ctx, key, loginLockWindow)
	}
	return errInvalidCredentials
}

func loginKey(ip, identifier string) string {
	if len(identifier) > 120 {
		identifier = identifier[:120]
	}
	return "rl:login:" + ip + ":" + identifier
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
