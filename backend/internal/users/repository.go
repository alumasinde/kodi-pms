package users

import (
	"context"
	"database/sql"
	"strings"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/rbac"
)

type querier interface {
	GetContext(ctx context.Context, dest any, query string, args ...any) error
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type repository struct {
	db *sqlx.DB
}

const userColumns = `
	u.id, u.organization_id, u.email, u.phone, u.first_name, u.last_name,
	u.national_id_last4, u.status, u.must_change_password, u.email_verified_at,
	u.phone_verified_at, u.last_login_at, u.created_at, u.updated_at`

func (r *repository) List(ctx context.Context, orgID uint64, q ListQuery) ([]UserRow, int, error) {
	where, args := userWhere(orgID, q)
	var total int
	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM users u WHERE `+where, args...); err != nil {
		return nil, 0, err
	}
	order := q.Sort
	if order == "" {
		order = defaultUserSort
	}
	listArgs := append(append([]any{}, args...), q.Page.Limit, q.Page.Offset)
	var rows []UserRow
	err := r.db.SelectContext(ctx, &rows,
		`SELECT `+userColumns+` FROM users u WHERE `+where+` ORDER BY `+order+`, u.id LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func userWhere(orgID uint64, q ListQuery) (string, []any) {
	where := []string{"u.organization_id = ?", "u.deleted_at IS NULL"}
	args := []any{orgID}
	if q.Search != "" {
		like := likePattern(q.Search)
		where = append(where, `(u.email LIKE ? OR u.first_name LIKE ? OR u.last_name LIKE ? OR u.phone LIKE ?)`)
		args = append(args, like, like, like, like)
	}
	if q.Status != "" {
		where = append(where, "u.status = ?")
		args = append(args, q.Status)
	}
	if q.RoleID != 0 {
		where = append(where, `EXISTS (
			SELECT 1 FROM user_roles ur WHERE ur.user_id = u.id AND ur.role_id = ?)`)
		args = append(args, q.RoleID)
	}
	return strings.Join(where, " AND "), args
}

func likePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return "%" + value + "%"
}

func (r *repository) Get(ctx context.Context, orgID, id uint64) (*UserRow, error) {
	var row UserRow
	err := r.db.GetContext(ctx, &row,
		`SELECT `+userColumns+` FROM users u
		 WHERE u.organization_id = ? AND u.id = ? AND u.deleted_at IS NULL`, orgID, id)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) GetForUpdate(ctx context.Context, tx *sqlx.Tx, orgID, id uint64) (*UserRow, error) {
	var row UserRow
	err := tx.GetContext(ctx, &row,
		`SELECT `+userColumns+` FROM users u
		 WHERE u.organization_id = ? AND u.id = ? AND u.deleted_at IS NULL
		 FOR UPDATE`, orgID, id)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) EmailTaken(ctx context.Context, q querier, orgID uint64, email string, excludeID uint64) (bool, error) {
	query := `SELECT COUNT(*) FROM users WHERE organization_id = ? AND email = ? AND deleted_at IS NULL`
	args := []any{orgID, email}
	if excludeID != 0 {
		query += ` AND id <> ?`
		args = append(args, excludeID)
	}
	var count int
	if err := q.GetContext(ctx, &count, query, args...); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *repository) PhoneTaken(ctx context.Context, q querier, orgID uint64, phone string, excludeID uint64) (bool, error) {
	if phone == "" {
		return false, nil
	}
	query := `SELECT COUNT(*) FROM users WHERE organization_id = ? AND phone = ? AND deleted_at IS NULL`
	args := []any{orgID, phone}
	if excludeID != 0 {
		query += ` AND id <> ?`
		args = append(args, excludeID)
	}
	var count int
	if err := q.GetContext(ctx, &count, query, args...); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *repository) NationalIDTaken(ctx context.Context, q querier, orgID uint64, hash []byte, excludeID uint64) (bool, error) {
	if len(hash) == 0 {
		return false, nil
	}
	query := `SELECT COUNT(*) FROM users
		WHERE organization_id = ? AND national_id_hash = ? AND deleted_at IS NULL`
	args := []any{orgID, hash}
	if excludeID != 0 {
		query += ` AND id <> ?`
		args = append(args, excludeID)
	}
	var count int
	if err := q.GetContext(ctx, &count, query, args...); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *repository) Roles(ctx context.Context, orgID uint64) ([]RoleRow, error) {
	var rows []RoleRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT id, name, description FROM roles
		WHERE organization_id IS NULL OR organization_id = ?
		ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) RolesByIDs(ctx context.Context, q querier, ids []uint64, orgID uint64) ([]RoleRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In(`
		SELECT id, name, description FROM roles
		WHERE id IN (?) AND (organization_id IS NULL OR organization_id = ?)`, ids, orgID)
	if err != nil {
		return nil, err
	}
	var rows []RoleRow
	if err := q.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) UserRoles(ctx context.Context, q querier, ids []uint64) ([]UserRoleRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In(`
		SELECT ur.user_id, r.id AS role_id, r.name AS role_name, r.description AS role_description
		FROM user_roles ur
		INNER JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id IN (?)
		ORDER BY ur.user_id, r.name`, ids)
	if err != nil {
		return nil, err
	}
	var rows []UserRoleRow
	if err := q.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) ReplaceRoles(ctx context.Context, tx *sqlx.Tx, userID uint64, roleIDs []uint64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_roles WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return insertRoleRows(ctx, tx, userID, roleIDs)
}

func insertRoleRows(ctx context.Context, tx *sqlx.Tx, userID uint64, roleIDs []uint64) error {
	if len(roleIDs) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`INSERT INTO user_roles (user_id, role_id) VALUES `)
	args := make([]any, 0, len(roleIDs)*2)
	for i, roleID := range roleIDs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("(?, ?)")
		args = append(args, userID, roleID)
	}
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}

func (r *repository) Insert(ctx context.Context, tx *sqlx.Tx, row *UserWrite, passwordHash string, pii *NationalIDValues) (uint64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO users
			(organization_id, email, phone, password_hash, must_change_password, first_name,
			 last_name, national_id_enc, national_id_hash, national_id_last4, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.OrganizationID, row.Email, row.Phone, passwordHash, row.MustChangePassword,
		row.FirstName, row.LastName, pii.cipherArg(), pii.hashArg(), pii.last4Arg(), row.Status)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

func (r *repository) Update(ctx context.Context, tx *sqlx.Tx, row *UserWrite) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE users
		SET email = ?, phone = ?, first_name = ?, last_name = ?, status = ?, must_change_password = ?
		WHERE id = ? AND organization_id = ? AND deleted_at IS NULL`,
		row.Email, row.Phone, row.FirstName, row.LastName, row.Status, row.MustChangePassword,
		row.ID, row.OrganizationID)
	return err
}

func (r *repository) UpdateNationalID(ctx context.Context, tx *sqlx.Tx, userID uint64, pii *NationalIDValues) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE users SET national_id_enc = ?, national_id_hash = ?, national_id_last4 = ?
		WHERE id = ?`, pii.cipherArg(), pii.hashArg(), pii.last4Arg(), userID)
	return err
}

// SetPasswordTx replaces the hash and bumps token_version, which invalidates
// every access token already minted for the user. must_change_password is
// written by Update so one PATCH has a single owner for the column.
func (r *repository) SetPasswordTx(ctx context.Context, tx *sqlx.Tx, userID uint64, hash string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE users
		SET password_hash = ?, token_version = token_version + 1,
		    failed_login_attempts = 0, locked_until = NULL
		WHERE id = ?`, hash, userID)
	return err
}

func (r *repository) BumpTokenVersionTx(ctx context.Context, tx *sqlx.Tx, userID uint64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE users SET token_version = token_version + 1 WHERE id = ?`, userID)
	return err
}

func (r *repository) RevokeSessionsTx(ctx context.Context, tx *sqlx.Tx, userID uint64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM refresh_tokens WHERE user_id = ?`, userID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE user_tokens SET used_at = UTC_TIMESTAMP()
		WHERE user_id = ? AND used_at IS NULL`, userID)
	return err
}

func (r *repository) SoftDeleteTx(ctx context.Context, tx *sqlx.Tx, orgID, userID uint64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE users SET deleted_at = UTC_TIMESTAMP()
		WHERE id = ? AND organization_id = ? AND deleted_at IS NULL`, userID, orgID)
	return err
}

func (r *repository) IsOrgAdmin(ctx context.Context, q querier, userID uint64) (bool, error) {
	var count int
	err := q.GetContext(ctx, &count, `
		SELECT COUNT(*)
		FROM user_roles ur
		INNER JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = ? AND r.name = 'org_admin'`, userID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *repository) CountOtherActiveOrgAdmins(ctx context.Context, q querier, orgID, excludeUserID uint64) (int, error) {
	var count int
	err := q.GetContext(ctx, &count, `
		SELECT COUNT(DISTINCT u.id)
		FROM users u
		INNER JOIN user_roles ur ON ur.user_id = u.id
		INNER JOIN roles r ON r.id = ur.role_id
		WHERE u.organization_id = ? AND u.id <> ? AND u.status = 'active'
		  AND u.deleted_at IS NULL AND r.name = 'org_admin'`, orgID, excludeUserID)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (r *repository) LiveAssignments(ctx context.Context, orgID, userID uint64, scope rbac.Scope) ([]AssignmentRow, error) {
	clause, scopeArgs := scope.PropertyFilter("pa.property_id")
	args := append([]any{orgID, userID}, scopeArgs...)
	var rows []AssignmentRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT pa.id, pa.user_id, pa.property_id, pa.assignment_role, pa.assigned_at,
			p.name AS property_name, p.code AS property_code
		FROM property_assignments pa
		INNER JOIN properties p ON p.id = pa.property_id
		WHERE p.organization_id = ? AND p.deleted_at IS NULL
		  AND pa.user_id = ? AND pa.unassigned_at IS NULL AND `+clause+`
		ORDER BY p.name`, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) MissingProperties(ctx context.Context, q querier, orgID uint64, ids []uint64, scope rbac.Scope) ([]uint64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	clause, scopeArgs := scope.PropertyFilter("p.id")
	query, args, err := sqlx.In(`
		SELECT p.id FROM properties p
		WHERE p.organization_id = ? AND p.deleted_at IS NULL AND `+clause+` AND p.id IN (?)`,
		append(append([]any{orgID}, scopeArgs...), ids)...)
	if err != nil {
		return nil, err
	}
	var found []uint64
	if err := q.SelectContext(ctx, &found, query, args...); err != nil {
		return nil, err
	}
	known := make(map[uint64]struct{}, len(found))
	for _, id := range found {
		known[id] = struct{}{}
	}
	var missing []uint64
	for _, id := range ids {
		if _, ok := known[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

func (r *repository) ReplaceAssignments(ctx context.Context, tx *sqlx.Tx, userID uint64, rows []AssignmentInput) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM property_assignments WHERE user_id = ? AND unassigned_at IS NULL`, userID); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`INSERT INTO property_assignments (property_id, user_id, assignment_role) VALUES `)
	args := make([]any, 0, len(rows)*3)
	for i, row := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("(?, ?, ?)")
		args = append(args, row.PropertyID, userID, row.Role)
	}
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}

// NationalIDValues holds one prepared PII write. A nil pointer or an empty
// value clears the stored ciphertext.
type NationalIDValues struct {
	ciphertext []byte
	hash       []byte
	last4      string
}

func (v *NationalIDValues) hasValue() bool {
	return v != nil && len(v.ciphertext) > 0
}

func (v *NationalIDValues) cipherArg() any {
	if v == nil || len(v.ciphertext) == 0 {
		return nil
	}
	return v.ciphertext
}

func (v *NationalIDValues) hashArg() any {
	if v == nil || len(v.hash) == 0 {
		return nil
	}
	return v.hash
}

func (v *NationalIDValues) last4Arg() any {
	if v == nil || v.last4 == "" {
		return nil
	}
	return v.last4
}
