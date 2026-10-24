package properties

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

const propertyColumns = `
	p.id, p.organization_id, p.owner_id, p.type_id, p.name, p.code, p.address,
	p.estate, p.town, p.county, p.postal_code, p.country_code, p.latitude,
	p.longitude, p.year_built, p.status, p.notes, p.created_at, p.updated_at`

const propertyViewQuery = `
	SELECT ` + propertyColumns + `,
		pt.name AS type_name,
		pt.description AS type_description,
		CONCAT_WS(' ', u.first_name, u.last_name) AS owner_name,
		o.company AS owner_company,
		u.email AS owner_email,
		u.phone AS owner_phone,
		(SELECT COUNT(*) FROM units un
			WHERE un.property_id = p.id AND un.deleted_at IS NULL) AS units_total,
		(SELECT COUNT(*) FROM units un
			WHERE un.property_id = p.id AND un.deleted_at IS NULL AND un.status = 'occupied') AS units_occupied,
		(SELECT COUNT(*) FROM units un
			WHERE un.property_id = p.id AND un.deleted_at IS NULL AND un.status = 'vacant') AS units_vacant
	FROM properties p
	INNER JOIN property_types pt ON pt.id = p.type_id
	INNER JOIN owners o ON o.organization_id = p.organization_id AND o.id = p.owner_id
	INNER JOIN users u ON u.id = o.user_id`

func (r *repository) List(ctx context.Context, orgID uint64, scope rbac.Scope, q ListQuery) ([]PropertyViewRow, int, error) {
	where, args := propertyWhere(orgID, scope, q)
	var total int
	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM properties p WHERE `+where, args...); err != nil {
		return nil, 0, err
	}
	order := q.Sort
	if order == "" {
		order = defaultPropertySort
	}
	listArgs := append(append([]any{}, args...), q.Page.Limit, q.Page.Offset)
	var rows []PropertyViewRow
	err := r.db.SelectContext(ctx, &rows,
		propertyViewQuery+` WHERE `+where+` ORDER BY `+order+`, p.id LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func propertyWhere(orgID uint64, scope rbac.Scope, q ListQuery) (string, []any) {
	where := []string{"p.organization_id = ?", "p.deleted_at IS NULL"}
	args := []any{orgID}
	clause, scopeArgs := scope.PropertyFilter("p.id")
	where = append(where, clause)
	args = append(args, scopeArgs...)
	if q.Search != "" {
		like := likePattern(q.Search)
		where = append(where,
			`(p.name LIKE ? OR p.code LIKE ? OR p.address LIKE ? OR p.estate LIKE ? OR p.town LIKE ?)`)
		args = append(args, like, like, like, like, like)
	}
	if q.Status != "" {
		where = append(where, "p.status = ?")
		args = append(args, q.Status)
	}
	if q.TypeID != 0 {
		where = append(where, "p.type_id = ?")
		args = append(args, q.TypeID)
	}
	if q.OwnerID != 0 {
		where = append(where, "p.owner_id = ?")
		args = append(args, q.OwnerID)
	}
	if q.County != "" {
		where = append(where, "p.county = ?")
		args = append(args, q.County)
	}
	return strings.Join(where, " AND "), args
}

func likePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return "%" + value + "%"
}

func (r *repository) Get(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*PropertyViewRow, error) {
	clause, scopeArgs := scope.PropertyFilter("p.id")
	args := append([]any{orgID, id}, scopeArgs...)
	var row PropertyViewRow
	err := r.db.GetContext(ctx, &row,
		propertyViewQuery+` WHERE p.organization_id = ? AND p.id = ? AND p.deleted_at IS NULL AND `+clause, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) GetForUpdate(ctx context.Context, tx *sqlx.Tx, orgID, id uint64, scope rbac.Scope) (*PropertyRow, error) {
	clause, scopeArgs := scope.PropertyFilter("p.id")
	args := append([]any{orgID, id}, scopeArgs...)
	var row PropertyRow
	err := tx.GetContext(ctx, &row, `
		SELECT `+propertyColumns+`
		FROM properties p
		WHERE p.organization_id = ? AND p.id = ? AND p.deleted_at IS NULL AND `+clause+`
		FOR UPDATE`, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) Insert(ctx context.Context, tx *sqlx.Tx, row *PropertyRow) (uint64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO properties
			(organization_id, owner_id, type_id, name, code, address, estate, town, county,
			 postal_code, country_code, latitude, longitude, year_built, status, notes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.OrganizationID, row.OwnerID, row.TypeID, row.Name, row.Code, row.Address, row.Estate,
		row.Town, row.County, row.PostalCode, row.CountryCode, row.Latitude, row.Longitude,
		row.YearBuilt, row.Status, row.Notes)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

func (r *repository) Update(ctx context.Context, tx *sqlx.Tx, row *PropertyRow) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE properties
		SET type_id = ?, name = ?, code = ?, address = ?, estate = ?, town = ?, county = ?,
		    postal_code = ?, country_code = ?, latitude = ?, longitude = ?, year_built = ?,
		    status = ?, notes = ?
		WHERE id = ? AND organization_id = ? AND deleted_at IS NULL`,
		row.TypeID, row.Name, row.Code, row.Address, row.Estate, row.Town, row.County,
		row.PostalCode, row.CountryCode, row.Latitude, row.Longitude, row.YearBuilt,
		row.Status, row.Notes, row.ID, row.OrganizationID)
	return err
}

func (r *repository) SoftDelete(ctx context.Context, tx *sqlx.Tx, orgID, id uint64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE properties SET deleted_at = UTC_TIMESTAMP()
		WHERE id = ? AND organization_id = ? AND deleted_at IS NULL`, id, orgID)
	return err
}

func (r *repository) CodeTaken(ctx context.Context, q querier, orgID uint64, code string, excludeID uint64) (bool, error) {
	query := `SELECT COUNT(*) FROM properties WHERE organization_id = ? AND code = ?`
	args := []any{orgID, code}
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

func (r *repository) OwnerExists(ctx context.Context, q querier, orgID, ownerID uint64) (bool, error) {
	var count int
	err := q.GetContext(ctx, &count,
		`SELECT COUNT(*) FROM owners WHERE organization_id = ? AND id = ?`, orgID, ownerID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *repository) PropertyTypeExists(ctx context.Context, q querier, typeID uint64) (bool, error) {
	var count int
	err := q.GetContext(ctx, &count, `SELECT COUNT(*) FROM property_types WHERE id = ?`, typeID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
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

func (r *repository) AmenityIDs(ctx context.Context, q querier, propertyID uint64) ([]uint64, error) {
	var ids []uint64
	err := q.SelectContext(ctx, &ids,
		`SELECT amenity_id FROM property_amenities WHERE property_id = ? ORDER BY amenity_id`, propertyID)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *repository) ReplaceAmenities(ctx context.Context, tx *sqlx.Tx, propertyID uint64, ids []uint64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM property_amenities WHERE property_id = ?`, propertyID); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`INSERT INTO property_amenities (property_id, amenity_id) VALUES `)
	args := make([]any, 0, len(ids)*2)
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("(?, ?)")
		args = append(args, propertyID, id)
	}
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}

func (r *repository) Amenities(ctx context.Context, propertyID uint64) ([]LookupRow, error) {
	var rows []LookupRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT a.id, a.name, a.description
		FROM property_amenities pa
		INNER JOIN amenities a ON a.id = pa.amenity_id
		WHERE pa.property_id = ?
		ORDER BY a.name`, propertyID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) CountUnits(ctx context.Context, q querier, orgID, propertyID uint64) (int, error) {
	var count int
	err := q.GetContext(ctx, &count, `
		SELECT COUNT(*) FROM units
		WHERE organization_id = ? AND property_id = ? AND deleted_at IS NULL`, orgID, propertyID)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (r *repository) PropertyTypes(ctx context.Context) ([]LookupRow, error) {
	var rows []LookupRow
	err := r.db.SelectContext(ctx, &rows,
		`SELECT id, name, description FROM property_types ORDER BY name`)
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
