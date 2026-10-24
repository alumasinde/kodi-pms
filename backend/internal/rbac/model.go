package rbac

import "database/sql"

type userState struct {
	ID                 uint64        `db:"id"`
	OrganizationID     sql.NullInt64 `db:"organization_id"`
	Status             string        `db:"status"`
	MustChangePassword bool          `db:"must_change_password"`
}

type roleRow struct {
	ID   uint64 `db:"id"`
	Name string `db:"name"`
}
