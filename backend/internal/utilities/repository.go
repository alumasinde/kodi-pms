package utilities

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/rbac"
)

// querier lets a repository method run on either the pool or a transaction.
type querier interface {
	GetContext(ctx context.Context, dest any, query string, args ...any) error
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// repository is the package's only SQL door. The domain files (meter_repo.go,
// reading_repo.go, bill_repo.go) hang their queries off it so the scope filter,
// the LIKE escaping and the property/unit lookups below exist once.
type repository struct {
	db *sqlx.DB
}

// tenantNameSubquery names the primary tenant of the lease aliased l. Every
// view that exposes a lease uses it, so it sits with the shared plumbing.
const tenantNameSubquery = `
		(SELECT CONCAT_WS(' ', tu.first_name, tu.last_name)
			FROM lease_tenants lt
			INNER JOIN tenants t ON t.id = lt.tenant_id
			INNER JOIN users tu ON tu.id = t.user_id
			WHERE lt.lease_id = l.id AND lt.role = 'primary'
			ORDER BY lt.tenant_id LIMIT 1)`

// scopeFilter answers "which rows may this caller see". The utility tables carry
// no organization or tenant column of their own, so a caller is limited to the
// properties they manage, or, for a tenant, to the meters and bills attached to
// a unit they have leased. A caller with both gets the union and anyone else
// sees nothing.
func scopeFilter(scope rbac.Scope, propertyColumn, unitColumn string) (string, []any) {
	if scope.AllProperties {
		return "1=1", nil
	}
	var parts []string
	var args []any
	if len(scope.PropertyIDs) > 0 {
		clause, clauseArgs := scope.PropertyFilter(propertyColumn)
		parts = append(parts, clause)
		args = append(args, clauseArgs...)
	}
	if scope.TenantID != nil {
		parts = append(parts, `EXISTS (SELECT 1 FROM leases l_own
			INNER JOIN lease_tenants lt_own ON lt_own.lease_id = l_own.id
			WHERE l_own.unit_id = `+unitColumn+` AND lt_own.tenant_id = ?)`)
		args = append(args, *scope.TenantID)
	}
	if len(parts) == 0 {
		return "1=0", nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func likePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return "%" + value + "%"
}

// OrganizationTimezone is the clock the "is this bill overdue" test runs on. A
// missing row is not an error here: the caller falls back to the operating
// timezone.
func (r *repository) OrganizationTimezone(ctx context.Context, orgID uint64) (string, error) {
	var tz sql.NullString
	err := r.db.GetContext(ctx, &tz, `SELECT timezone FROM organizations WHERE id = ?`, orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(tz.String), nil
}

// PropertyInScope confirms the property exists in this organization, is not
// archived and is inside the caller's data scope. Writes are staff-only, so the
// property filter alone is the right test.
func (r *repository) PropertyInScope(ctx context.Context, q querier, orgID, propertyID uint64, scope rbac.Scope) (bool, error) {
	clause, scopeArgs := scope.PropertyFilter("p.id")
	args := append([]any{orgID, propertyID}, scopeArgs...)
	var count int
	err := q.GetContext(ctx, &count, `
		SELECT COUNT(*) FROM properties p
		WHERE p.organization_id = ? AND p.id = ? AND p.deleted_at IS NULL AND `+clause, args...)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// UnitInScope returns the unit a bill or a meter is being attached to, with the
// active lease on it. No lease is not an error here: a vacant unit can still be
// metered, it just cannot be billed to anybody.
func (r *repository) UnitInScope(ctx context.Context, q querier, orgID, unitID uint64, scope rbac.Scope) (*UnitRefRow, error) {
	clause, scopeArgs := scope.PropertyFilter("u.property_id")
	args := append([]any{leaseActive, leaseActive, orgID, unitID}, scopeArgs...)
	var row UnitRefRow
	err := q.GetContext(ctx, &row, `
		SELECT u.id, u.property_id, u.number, u.status,
			(SELECT l.id FROM leases l
				WHERE l.organization_id = u.organization_id AND l.unit_id = u.id AND l.status = ?
				LIMIT 1) AS lease_id,
			(SELECT l.due_day FROM leases l
				WHERE l.organization_id = u.organization_id AND l.unit_id = u.id AND l.status = ?
				LIMIT 1) AS lease_due_day
		FROM units u
		INNER JOIN properties p ON p.organization_id = u.organization_id AND p.id = u.property_id
		WHERE u.organization_id = ? AND u.id = ? AND u.deleted_at IS NULL AND p.deleted_at IS NULL AND `+clause,
		args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ScopeUnits lists the units a caller can attach a meter or a bill to, which is
// what fills the lookups dropdown without a second round trip per property.
func (r *repository) ScopeUnits(ctx context.Context, orgID uint64, scope rbac.Scope) ([]UnitLookRow, error) {
	clause, scopeArgs := scope.PropertyFilter("u.property_id")
	args := append([]any{orgID}, scopeArgs...)
	var rows []UnitLookRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT u.id, u.property_id, u.number, p.name AS property_name, p.code AS property_code
		FROM units u
		INNER JOIN properties p ON p.organization_id = u.organization_id AND p.id = u.property_id
		WHERE u.organization_id = ? AND u.deleted_at IS NULL AND p.deleted_at IS NULL AND `+clause+`
		ORDER BY p.name ASC, u.number ASC
		LIMIT ?`, append(args, maxLookupUnits)...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// UnitIsOnProperty checks a meter's unit link: the meter belongs to the property
// in the row, so the unit has to too.
func (r *repository) UnitIsOnProperty(ctx context.Context, q querier, propertyID, unitID uint64) (bool, error) {
	var count int
	err := q.GetContext(ctx, &count, `
		SELECT COUNT(*) FROM units u
		INNER JOIN properties p ON p.id = u.property_id
		WHERE u.id = ? AND u.property_id = ? AND u.deleted_at IS NULL AND p.deleted_at IS NULL`,
		unitID, propertyID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
