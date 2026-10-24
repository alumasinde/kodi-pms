package auth

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"
)

type repository struct {
	db *sqlx.DB
}

type UserRow struct {
	ID                 uint64         `db:"id"`
	OrganizationID     sql.NullInt64  `db:"organization_id"`
	Email              string         `db:"email"`
	Phone              sql.NullString `db:"phone"`
	PasswordHash       string         `db:"password_hash"`
	MustChangePassword bool           `db:"must_change_password"`
	FirstName          string         `db:"first_name"`
	LastName           string         `db:"last_name"`
	Status             string         `db:"status"`
	LastLoginAt        sql.NullTime   `db:"last_login_at"`
}

type OrgRow struct {
	ID           uint64 `db:"id"`
	Name         string `db:"org_name"`
	Slug         string `db:"slug"`
	CurrencyCode string `db:"currency_code"`
	Timezone     string `db:"timezone"`
	Status       string `db:"org_status"`
	Domain       string `db:"domain"`
}

type RefreshRow struct {
	ID             uint64        `db:"id"`
	UserID         uint64        `db:"user_id"`
	OrganizationID sql.NullInt64 `db:"organization_id"`
	FamilyID       string        `db:"family_id"`
	ExpiresAt      time.Time     `db:"expires_at"`
	RevokedAt      sql.NullTime  `db:"revoked_at"`
}

type UserTokenRow struct {
	ID        uint64       `db:"id"`
	UserID    uint64       `db:"user_id"`
	Purpose   string       `db:"purpose"`
	ExpiresAt time.Time    `db:"expires_at"`
	UsedAt    sql.NullTime `db:"used_at"`
}

const userColumns = `
	id, organization_id, email, phone, password_hash, must_change_password,
	first_name, last_name, status, last_login_at`

func (r *repository) FindUserByIdentifier(ctx context.Context, identifier string) (*UserRow, error) {
	var u UserRow
	err := r.db.GetContext(ctx, &u, `
		SELECT `+userColumns+`
		FROM users
		WHERE (email = ? OR phone = ?) AND deleted_at IS NULL
		LIMIT 1`, identifier, identifier)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *repository) FindUserByID(ctx context.Context, id uint64) (*UserRow, error) {
	var u UserRow
	err := r.db.GetContext(ctx, &u, `
		SELECT `+userColumns+`
		FROM users WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *repository) HasGlobalRole(ctx context.Context, userID uint64, role string) (bool, error) {
	var count int
	err := r.db.GetContext(ctx, &count, `
		SELECT COUNT(*)
		FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = ? AND r.name = ? AND r.organization_id IS NULL`, userID, role)
	return count > 0, err
}

func (r *repository) OrgByID(ctx context.Context, id uint64) (*OrgRow, error) {
	var o OrgRow
	err := r.db.GetContext(ctx, &o, `
		SELECT o.id, o.org_name, o.slug, o.currency_code, o.timezone, o.org_status,
		       COALESCE((
		         SELECT d.domain FROM organization_domains d
		         WHERE d.organization_id = o.id
		         ORDER BY d.is_primary DESC, d.id ASC LIMIT 1
		       ), '') AS domain
		FROM organizations o
		WHERE o.id = ? AND o.deleted_at IS NULL`, id)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (r *repository) InsertRefreshTx(ctx context.Context, tx *sqlx.Tx, userID uint64, orgID uint64, familyID, tokenHash string, meta ClientMeta, expiresAt time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO refresh_tokens
			(user_id, organization_id, family_id, token_hash, user_agent, ip_address, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, nullableUint(orgID), familyID, tokenHash,
		nullableText(meta.UserAgent, 255), nullableText(meta.IP, 45), expiresAt.UTC())
	return err
}

func (r *repository) FindRefreshForUpdate(ctx context.Context, tx *sqlx.Tx, hash string) (*RefreshRow, error) {
	var rt RefreshRow
	err := tx.GetContext(ctx, &rt, `
		SELECT id, user_id, organization_id, family_id, expires_at, revoked_at
		FROM refresh_tokens
		WHERE token_hash = ?
		LIMIT 1
		FOR UPDATE`, hash)
	if err != nil {
		return nil, err
	}
	return &rt, nil
}

func (r *repository) FindRefresh(ctx context.Context, hash string) (*RefreshRow, error) {
	var rt RefreshRow
	err := r.db.GetContext(ctx, &rt, `
		SELECT id, user_id, organization_id, family_id, expires_at, revoked_at
		FROM refresh_tokens
		WHERE token_hash = ?
		LIMIT 1`, hash)
	if err != nil {
		return nil, err
	}
	return &rt, nil
}

func (r *repository) RevokeRefreshTx(ctx context.Context, tx *sqlx.Tx, id uint64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE refresh_tokens SET revoked_at = NOW()
		WHERE id = ? AND revoked_at IS NULL`, id)
	return err
}

func (r *repository) RevokeFamily(ctx context.Context, exec sqlx.ExtContext, familyID string) error {
	_, err := exec.ExecContext(ctx, `
		UPDATE refresh_tokens SET revoked_at = NOW()
		WHERE family_id = ? AND revoked_at IS NULL`, familyID)
	return err
}

func (r *repository) TouchLoginTx(ctx context.Context, tx *sqlx.Tx, userID uint64) error {
	_, err := tx.ExecContext(ctx, `UPDATE users SET last_login_at = NOW() WHERE id = ?`, userID)
	return err
}

func (r *repository) InvalidateUserTokensTx(ctx context.Context, tx *sqlx.Tx, userID uint64, purpose string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE user_tokens SET used_at = NOW()
		WHERE user_id = ? AND purpose = ? AND used_at IS NULL`, userID, purpose)
	return err
}

func (r *repository) InsertUserTokenTx(ctx context.Context, tx *sqlx.Tx, userID uint64, purpose, hash string, expiresAt time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_tokens (user_id, purpose, token_hash, expires_at)
		VALUES (?, ?, ?, ?)`, userID, purpose, hash, expiresAt.UTC())
	return err
}

func (r *repository) FindUserTokenForUpdate(ctx context.Context, tx *sqlx.Tx, hash, purpose string) (*UserTokenRow, error) {
	var t UserTokenRow
	err := tx.GetContext(ctx, &t, `
		SELECT id, user_id, purpose, expires_at, used_at
		FROM user_tokens
		WHERE token_hash = ? AND purpose = ?
		LIMIT 1
		FOR UPDATE`, hash, purpose)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *repository) MarkUserTokenUsedTx(ctx context.Context, tx *sqlx.Tx, id uint64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE user_tokens SET used_at = NOW()
		WHERE id = ? AND used_at IS NULL`, id)
	return err
}

func (r *repository) UpdatePasswordTx(ctx context.Context, tx *sqlx.Tx, userID uint64, hash string, activate bool) error {
	if activate {
		_, err := tx.ExecContext(ctx, `
			UPDATE users
			SET password_hash = ?, must_change_password = 0, status = 'active'
			WHERE id = ?`, hash, userID)
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE users SET password_hash = ?, must_change_password = 0
		WHERE id = ?`, hash, userID)
	return err
}

func (r *repository) DeleteUserRefreshTokensTx(ctx context.Context, tx *sqlx.Tx, userID uint64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE user_id = ?`, userID)
	return err
}

func nullableUint(id uint64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullableText(v string, max int) any {
	if v == "" {
		return nil
	}
	r := []rune(v)
	if len(r) > max {
		v = string(r[:max])
	}
	return v
}
