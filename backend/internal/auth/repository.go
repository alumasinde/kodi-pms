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

const userColumns = `
	id, organization_id, email, phone, password_hash, pin_hash, must_change_password,
	first_name, last_name, status, last_login_at, token_version, failed_login_attempts, locked_until`

const refreshColumns = `
	id, user_id, organization_id, family_id, expires_at, revoked_at, rotated_at, grace_used_at`

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
		SELECT `+refreshColumns+`
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
		SELECT `+refreshColumns+`
		FROM refresh_tokens
		WHERE token_hash = ?
		LIMIT 1`, hash)
	if err != nil {
		return nil, err
	}
	return &rt, nil
}

// RotateRefreshTx revokes the presented token and records why, so a retry can
// be distinguished from theft while the token is still inside the grace window.
func (r *repository) RotateRefreshTx(ctx context.Context, tx *sqlx.Tx, id uint64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE refresh_tokens SET revoked_at = NOW(), rotated_at = NOW()
		WHERE id = ? AND revoked_at IS NULL`, id)
	return err
}

func (r *repository) MarkGraceUsedTx(ctx context.Context, tx *sqlx.Tx, id uint64) (bool, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE refresh_tokens SET grace_used_at = NOW()
		WHERE id = ? AND grace_used_at IS NULL AND rotated_at IS NOT NULL`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *repository) CountLiveFamilyTx(ctx context.Context, tx *sqlx.Tx, familyID string) (int, error) {
	var n int
	err := tx.GetContext(ctx, &n, `
		SELECT COUNT(*) FROM refresh_tokens
		WHERE family_id = ? AND revoked_at IS NULL AND expires_at > UTC_TIMESTAMP()`, familyID)
	return n, err
}

func (r *repository) RevokeFamily(ctx context.Context, exec sqlx.ExtContext, familyID string) error {
	_, err := exec.ExecContext(ctx, `
		UPDATE refresh_tokens SET revoked_at = NOW()
		WHERE family_id = ? AND revoked_at IS NULL`, familyID)
	return err
}

// RecordFailedLogin increments the account counter under a row lock and sets
// locked_until at the threshold. This is the authoritative lock; the cache
// counter is only a cheaper first line of defence.
func (r *repository) RecordFailedLogin(ctx context.Context, userID uint64, threshold int, lockFor time.Duration) (int, sql.NullTime, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, sql.NullTime{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var attempts int
	var locked sql.NullTime
	row := tx.QueryRowxContext(ctx, `
		SELECT failed_login_attempts, locked_until
		FROM users WHERE id = ? AND deleted_at IS NULL
		FOR UPDATE`, userID)
	if err := row.Scan(&attempts, &locked); err != nil {
		return 0, sql.NullTime{}, err
	}
	now := time.Now().UTC()
	if locked.Valid && !now.Before(locked.Time) {
		attempts = 0
		locked = sql.NullTime{}
	}
	attempts++
	if threshold > 0 && attempts >= threshold {
		locked = sql.NullTime{Time: now.Add(lockFor), Valid: true}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET failed_login_attempts = ?, locked_until = ?
		WHERE id = ?`, attempts, nullableTime(locked), userID); err != nil {
		return 0, sql.NullTime{}, err
	}
	if err := tx.Commit(); err != nil {
		return 0, sql.NullTime{}, err
	}
	return attempts, locked, nil
}

func (r *repository) TouchLoginTx(ctx context.Context, tx *sqlx.Tx, userID uint64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE users
		SET last_login_at = NOW(), failed_login_attempts = 0, locked_until = NULL
		WHERE id = ?`, userID)
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

// UpdatePasswordTx also increments token_version, which invalidates every
// access token already minted for the user.
func (r *repository) UpdatePasswordTx(ctx context.Context, tx *sqlx.Tx, userID uint64, hash string, activate bool) error {
	if activate {
		_, err := tx.ExecContext(ctx, `
			UPDATE users
			SET password_hash = ?, must_change_password = 0, status = 'active',
			    token_version = token_version + 1,
			    failed_login_attempts = 0, locked_until = NULL
			WHERE id = ?`, hash, userID)
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE users
		SET password_hash = ?, must_change_password = 0,
		    token_version = token_version + 1,
		    failed_login_attempts = 0, locked_until = NULL
		WHERE id = ?`, hash, userID)
	return err
}

func (r *repository) UpdatePINHash(ctx context.Context, userID uint64, hash string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE users SET pin_hash = ? WHERE id = ?`, hash, userID)
	return err
}

func (r *repository) ClearPINHashTx(ctx context.Context, tx *sqlx.Tx, userID uint64) error {
	_, err := tx.ExecContext(ctx, `UPDATE users SET pin_hash = NULL WHERE id = ?`, userID)
	return err
}

func (r *repository) DeleteUserRefreshTokensTx(ctx context.Context, tx *sqlx.Tx, userID uint64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE user_id = ?`, userID)
	return err
}

func (r *repository) RoleForInvite(ctx context.Context, roleID, orgID uint64) (*InviteRoleRow, error) {
	var role InviteRoleRow
	err := r.db.GetContext(ctx, &role, `
		SELECT id, name FROM roles
		WHERE id = ? AND (organization_id IS NULL OR organization_id = ?)`, roleID, orgID)
	if err != nil {
		return nil, err
	}
	return &role, nil
}

func (r *repository) FindInviteUserByEmail(ctx context.Context, email string) (*InviteUserRow, error) {
	var u InviteUserRow
	err := r.db.GetContext(ctx, &u, `
		SELECT id, organization_id, status FROM users WHERE email = ? LIMIT 1`, email)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *repository) PhoneTakenByOther(ctx context.Context, phone string, userID uint64) (bool, error) {
	if phone == "" {
		return false, nil
	}
	var count int
	err := r.db.GetContext(ctx, &count, `
		SELECT COUNT(*) FROM users WHERE phone = ? AND id <> ?`, phone, userID)
	return count > 0, err
}

func (r *repository) InsertInvitedUserTx(ctx context.Context, tx *sqlx.Tx, orgID uint64, email, phone, passwordHash, firstName, lastName string) (uint64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO users
			(organization_id, email, phone, password_hash, must_change_password, first_name, last_name, status)
		VALUES (?, ?, ?, ?, 1, ?, ?, 'invited')`,
		orgID, email, nullableText(phone, 20), passwordHash, firstName, lastName)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

func (r *repository) UpdateInvitedUserTx(ctx context.Context, tx *sqlx.Tx, userID uint64, firstName, lastName, phone string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE users SET first_name = ?, last_name = ?, phone = ?
		WHERE id = ? AND status = 'invited'`,
		firstName, lastName, nullableText(phone, 20), userID)
	return err
}

func (r *repository) AssignRoleTx(ctx context.Context, tx *sqlx.Tx, userID, roleID uint64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO user_roles (user_id, role_id) VALUES (?, ?)`, userID, roleID)
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

func nullableTime(t sql.NullTime) any {
	if !t.Valid {
		return nil
	}
	return t.Time.UTC()
}
