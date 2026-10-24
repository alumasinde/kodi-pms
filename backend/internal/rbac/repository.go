package rbac

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
)

type repository struct {
	db *sqlx.DB
}

func (r *repository) UserState(ctx context.Context, userID uint64) (*userState, error) {
	var u userState
	err := r.db.GetContext(ctx, &u, `
		SELECT id, organization_id, status, must_change_password
		FROM users WHERE id = ? AND deleted_at IS NULL`, userID)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *repository) OrgIsActive(ctx context.Context, orgID uint64) (bool, error) {
	var status string
	err := r.db.GetContext(ctx, &status, `
		SELECT org_status FROM organizations WHERE id = ? AND deleted_at IS NULL`, orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == "active", nil
}

func (r *repository) RolesForUser(ctx context.Context, userID, orgID uint64) ([]roleRow, error) {
	var roles []roleRow
	err := r.db.SelectContext(ctx, &roles, `
		SELECT r.id, r.name
		FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = ? AND (r.organization_id IS NULL OR r.organization_id = ?)
		ORDER BY r.name`, userID, orgID)
	return roles, err
}

func (r *repository) PermissionCodes(ctx context.Context, roleIDs []uint64) ([]string, error) {
	if len(roleIDs) == 0 {
		return nil, nil
	}
	q, args, err := sqlx.In(`
		SELECT DISTINCT p.code
		FROM role_permissions rp
		JOIN permissions p ON p.id = rp.permission_id
		WHERE rp.role_id IN (?)`, roleIDs)
	if err != nil {
		return nil, err
	}
	var codes []string
	err = r.db.SelectContext(ctx, &codes, r.db.Rebind(q), args...)
	return codes, err
}

func (r *repository) OwnedProperties(ctx context.Context, userID, orgID uint64) ([]uint64, error) {
	var ids []uint64
	err := r.db.SelectContext(ctx, &ids, `
		SELECT p.id
		FROM properties p
		JOIN owners o ON o.id = p.owner_id
		WHERE o.user_id = ? AND o.organization_id = ?
		  AND p.organization_id = ? AND p.deleted_at IS NULL`, userID, orgID, orgID)
	return ids, err
}

func (r *repository) AssignedProperties(ctx context.Context, userID, orgID uint64) ([]uint64, error) {
	var ids []uint64
	err := r.db.SelectContext(ctx, &ids, `
		SELECT pa.property_id
		FROM property_assignments pa
		JOIN properties p ON p.id = pa.property_id
		WHERE pa.user_id = ? AND pa.unassigned_at IS NULL
		  AND p.organization_id = ? AND p.deleted_at IS NULL`, userID, orgID)
	return ids, err
}

func (r *repository) AgentProperties(ctx context.Context, userID, orgID uint64) ([]uint64, error) {
	var ids []uint64
	err := r.db.SelectContext(ctx, &ids, `
		SELECT pa.property_id
		FROM property_agents pa
		JOIN agents a ON a.id = pa.agent_id
		JOIN properties p ON p.id = pa.property_id
		WHERE a.user_id = ? AND a.organization_id = ?
		  AND p.organization_id = ? AND p.deleted_at IS NULL`, userID, orgID, orgID)
	return ids, err
}

func (r *repository) TenantID(ctx context.Context, userID, orgID uint64) (*uint64, error) {
	var id uint64
	err := r.db.GetContext(ctx, &id, `
		SELECT id FROM tenants WHERE user_id = ? AND organization_id = ? LIMIT 1`, userID, orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (r *repository) ContractorID(ctx context.Context, userID, orgID uint64) (*uint64, error) {
	var id uint64
	err := r.db.GetContext(ctx, &id, `
		SELECT id FROM contractors WHERE user_id = ? AND organization_id = ? LIMIT 1`, userID, orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}
