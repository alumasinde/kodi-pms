package units

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

const unitColumns = `
	u.id, u.organization_id, u.property_id, u.type_id, u.number, u.floor,
	u.bedrooms, u.bathrooms, u.square_feet, u.rent, u.status, u.created_at, u.updated_at`

const unitViewQuery = `
	SELECT ` + unitColumns + `,
		p.name AS property_name,
		p.code AS property_code,
		ut.name AS type_name,
		ut.description AS type_description,
		l.id AS lease_id,
		l.lease_number,
		l.start_date AS lease_start,
		l.end_date AS lease_end,
		l.rent_amount AS lease_rent,
		(SELECT CONCAT_WS(' ', tu.first_name, tu.last_name)
			FROM lease_tenants lt
			INNER JOIN tenants t ON t.id = lt.tenant_id
			INNER JOIN users tu ON tu.id = t.user_id
			WHERE lt.lease_id = l.id AND lt.role = 'primary'
			ORDER BY lt.tenant_id LIMIT 1) AS tenant_name
	FROM units u
	INNER JOIN properties p ON p.organization_id = u.organization_id AND p.id = u.property_id
	INNER JOIN unit_types ut ON ut.id = u.type_id
	LEFT JOIN leases l ON l.unit_id = u.id AND l.status = 'active'`

func (r *repository) List(ctx context.Context, orgID uint64, scope rbac.Scope, q ListQuery) ([]UnitViewRow, int, error) {
	where, args := unitWhere(orgID, scope, q)
	var total int
	if err := r.db.GetContext(ctx, &total,
		`SELECT COUNT(*) FROM units u INNER JOIN properties p ON p.id = u.property_id WHERE `+where, args...); err != nil {
		return nil, 0, err
	}
	order := q.Sort
	if order == "" {
		order = defaultUnitSort
	}
	listArgs := append(append([]any{}, args...), q.Page.Limit, q.Page.Offset)
	var rows []UnitViewRow
	err := r.db.SelectContext(ctx, &rows,
		unitViewQuery+` WHERE `+where+` ORDER BY `+order+`, u.id LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func unitWhere(orgID uint64, scope rbac.Scope, q ListQuery) (string, []any) {
	where := []string{"u.organization_id = ?", "u.deleted_at IS NULL", "p.deleted_at IS NULL"}
	args := []any{orgID}
	clause, scopeArgs := scope.PropertyFilter("u.property_id")
	where = append(where, clause)
	args = append(args, scopeArgs...)
	if q.Search != "" {
		like := likePattern(q.Search)
		where = append(where, `(u.number LIKE ? OR p.name LIKE ? OR p.code LIKE ?)`)
		args = append(args, like, like, like)
	}
	if q.Status != "" {
		where = append(where, "u.status = ?")
		args = append(args, q.Status)
	}
	if q.PropertyID != 0 {
		where = append(where, "u.property_id = ?")
		args = append(args, q.PropertyID)
	}
	if q.TypeID != 0 {
		where = append(where, "u.type_id = ?")
		args = append(args, q.TypeID)
	}
	return strings.Join(where, " AND "), args
}

func likePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return "%" + value + "%"
}

func (r *repository) Get(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*UnitViewRow, error) {
	clause, scopeArgs := scope.PropertyFilter("u.property_id")
	args := append([]any{orgID, id}, scopeArgs...)
	var row UnitViewRow
	err := r.db.GetContext(ctx, &row,
		unitViewQuery+` WHERE u.organization_id = ? AND u.id = ? AND u.deleted_at IS NULL AND p.deleted_at IS NULL AND `+clause, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) ByIDs(ctx context.Context, orgID uint64, ids []uint64, scope rbac.Scope) ([]UnitViewRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	clause, scopeArgs := scope.PropertyFilter("u.property_id")
	args := make([]any, 0, len(ids)+1+len(scopeArgs))
	args = append(args, orgID)
	for _, id := range ids {
		args = append(args, id)
	}
	for _, a := range scopeArgs {
		args = append(args, a)
	}
	query, args, err := sqlx.In(
		unitViewQuery+` WHERE u.organization_id = ? AND u.id IN (?) AND u.deleted_at IS NULL AND p.deleted_at IS NULL AND `+clause,
		args...)
	if err != nil {
		return nil, err
	}
	var rows []UnitViewRow
	if err := r.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) GetForUpdate(ctx context.Context, tx *sqlx.Tx, orgID, id uint64, scope rbac.Scope) (*UnitRow, error) {
	clause, scopeArgs := scope.PropertyFilter("u.property_id")
	args := append([]any{orgID, id}, scopeArgs...)
	var row UnitRow
	err := tx.GetContext(ctx, &row, `
		SELECT `+unitColumns+`
		FROM units u
		INNER JOIN properties p ON p.organization_id = u.organization_id AND p.id = u.property_id
		WHERE u.organization_id = ? AND u.id = ? AND u.deleted_at IS NULL AND p.deleted_at IS NULL AND `+clause+`
		FOR UPDATE`, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) Insert(ctx context.Context, tx *sqlx.Tx, row *UnitRow) (uint64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO units
			(organization_id, property_id, type_id, number, floor, bedrooms, bathrooms,
			 square_feet, rent, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.OrganizationID, row.PropertyID, row.TypeID, row.Number, row.Floor, row.Bedrooms,
		row.Bathrooms, row.SquareFeet, row.Rent, row.Status)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

func (r *repository) Update(ctx context.Context, tx *sqlx.Tx, row *UnitRow) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE units
		SET type_id = ?, number = ?, floor = ?, bedrooms = ?, bathrooms = ?,
		    square_feet = ?, rent = ?, status = ?
		WHERE id = ? AND organization_id = ? AND deleted_at IS NULL`,
		row.TypeID, row.Number, row.Floor, row.Bedrooms, row.Bathrooms, row.SquareFeet,
		row.Rent, row.Status, row.ID, row.OrganizationID)
	return err
}

func (r *repository) SoftDelete(ctx context.Context, tx *sqlx.Tx, orgID, id uint64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE units SET deleted_at = UTC_TIMESTAMP()
		WHERE id = ? AND organization_id = ? AND deleted_at IS NULL`, id, orgID)
	return err
}

func (r *repository) NumberTaken(ctx context.Context, q querier, propertyID uint64, number string, excludeID uint64) (bool, error) {
	query := `SELECT COUNT(*) FROM units WHERE property_id = ? AND number = ?`
	args := []any{propertyID, number}
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

func (r *repository) UsedNumbers(ctx context.Context, q querier, propertyID uint64, numbers []string) ([]string, error) {
	if len(numbers) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In(`
		SELECT number FROM units WHERE property_id = ? AND number IN (?)`,
		append([]any{propertyID}, numbers)...)
	if err != nil {
		return nil, err
	}
	var used []string
	if err := q.SelectContext(ctx, &used, query, args...); err != nil {
		return nil, err
	}
	return used, nil
}

// PropertyInScope confirms the property exists in this organization, is not
// archived and is inside the caller's data scope.
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

func (r *repository) UnitTypeExists(ctx context.Context, q querier, typeID uint64) (bool, error) {
	var count int
	err := q.GetContext(ctx, &count, `SELECT COUNT(*) FROM unit_types WHERE id = ?`, typeID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *repository) ActiveLeaseID(ctx context.Context, q querier, unitID uint64) (uint64, error) {
	var id sql.NullInt64
	err := q.GetContext(ctx, &id, `
		SELECT id FROM leases WHERE unit_id = ? AND status = 'active' LIMIT 1`, unitID)
	if err != nil {
		return 0, err
	}
	if !id.Valid {
		return 0, nil
	}
	return uint64(id.Int64), nil
}

func (r *repository) MissingAmenities(ctx context.Context, q querier, ids []uint64) ([]uint64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In(`SELECT id FROM amenities WHERE id IN (?)`, ids)
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

func (r *repository) AmenityIDs(ctx context.Context, q querier, unitID uint64) ([]uint64, error) {
	var ids []uint64
	err := q.SelectContext(ctx, &ids,
		`SELECT amenity_id FROM unit_amenities WHERE unit_id = ? ORDER BY amenity_id`, unitID)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *repository) ReplaceAmenities(ctx context.Context, tx *sqlx.Tx, unitID uint64, ids []uint64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM unit_amenities WHERE unit_id = ?`, unitID); err != nil {
		return err
	}
	return addAmenities(ctx, tx, []uint64{unitID}, ids)
}

func (r *repository) AddAmenities(ctx context.Context, tx *sqlx.Tx, unitIDs []uint64, ids []uint64) error {
	return addAmenities(ctx, tx, unitIDs, ids)
}

func addAmenities(ctx context.Context, tx *sqlx.Tx, unitIDs []uint64, ids []uint64) error {
	if len(unitIDs) == 0 || len(ids) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`INSERT INTO unit_amenities (unit_id, amenity_id) VALUES `)
	args := make([]any, 0, len(unitIDs)*len(ids)*2)
	first := true
	for _, unitID := range unitIDs {
		for _, id := range ids {
			if !first {
				b.WriteByte(',')
			}
			first = false
			b.WriteString("(?, ?)")
			args = append(args, unitID, id)
		}
	}
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}

func (r *repository) Amenities(ctx context.Context, unitID uint64) ([]LookupRow, error) {
	var rows []LookupRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT a.id, a.name, a.description
		FROM unit_amenities ua
		INNER JOIN amenities a ON a.id = ua.amenity_id
		WHERE ua.unit_id = ?
		ORDER BY a.name`, unitID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) UnitTypes(ctx context.Context) ([]LookupRow, error) {
	var rows []LookupRow
	err := r.db.SelectContext(ctx, &rows,
		`SELECT id, name, description FROM unit_types ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) AmenityLookups(ctx context.Context) ([]LookupRow, error) {
	var rows []LookupRow
	err := r.db.SelectContext(ctx, &rows,
		`SELECT id, name, description FROM amenities ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
