package settings

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
)

type repository struct {
	db *sqlx.DB
}

const organizationColumns = `
	id, org_name, slug, legal_name, kra_pin, email, phone, org_address, county,
	country_code, currency_code, timezone, org_status`

func (r *repository) Organization(ctx context.Context, id uint64) (*OrganizationRow, error) {
	var org OrganizationRow
	err := r.db.GetContext(ctx, &org, `
		SELECT `+organizationColumns+`
		FROM organizations
		WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return nil, err
	}
	return &org, nil
}

func (r *repository) OrganizationForUpdate(ctx context.Context, tx *sqlx.Tx, id uint64) (*OrganizationRow, error) {
	var org OrganizationRow
	err := tx.GetContext(ctx, &org, `
		SELECT `+organizationColumns+`
		FROM organizations
		WHERE id = ? AND deleted_at IS NULL
		FOR UPDATE`, id)
	if err != nil {
		return nil, err
	}
	return &org, nil
}

func (r *repository) UpdateOrganization(ctx context.Context, tx *sqlx.Tx, org *OrganizationRow) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE organizations
		SET org_name = ?, legal_name = ?, kra_pin = ?, email = ?, phone = ?,
		    org_address = ?, county = ?, country_code = ?, currency_code = ?, timezone = ?
		WHERE id = ? AND deleted_at IS NULL`,
		org.Name, org.LegalName, org.KRAPIN, org.Email, org.Phone,
		org.Address, org.County, org.CountryCode, org.CurrencyCode, org.Timezone, org.ID)
	return err
}

func (r *repository) SettingRows(ctx context.Context, orgID uint64) ([]SettingRow, error) {
	var rows []SettingRow
	err := r.db.SelectContext(ctx, &rows, settingRowsQuery, orgID)
	return rows, err
}

func (r *repository) SettingRowsTx(ctx context.Context, tx *sqlx.Tx, orgID uint64) ([]SettingRow, error) {
	var rows []SettingRow
	err := tx.SelectContext(ctx, &rows, settingRowsQuery, orgID)
	return rows, err
}

const settingRowsQuery = `
	SELECT organization_id, setting_key, setting_value, value_type, description
	FROM system_settings
	WHERE organization_id IS NULL OR organization_id = ?
	ORDER BY setting_key`

func (r *repository) UpsertSetting(ctx context.Context, tx *sqlx.Tx, orgID uint64, key, value, valueType, description string) error {
	desc := sql.NullString{String: description, Valid: description != ""}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO system_settings (organization_id, setting_key, setting_value, value_type, description)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			setting_value = VALUES(setting_value),
			value_type = VALUES(value_type),
			description = VALUES(description)`,
		orgID, key, value, valueType, desc)
	return err
}

func (r *repository) DeleteSetting(ctx context.Context, tx *sqlx.Tx, orgID uint64, key string) error {
	_, err := tx.ExecContext(ctx, `
		DELETE FROM system_settings
		WHERE organization_id = ? AND setting_key = ?`, orgID, key)
	return err
}

func (r *repository) Domains(ctx context.Context, orgID uint64) ([]string, error) {
	var domains []string
	err := r.db.SelectContext(ctx, &domains, `
		SELECT domain FROM organization_domains WHERE organization_id = ?`, orgID)
	return domains, err
}
