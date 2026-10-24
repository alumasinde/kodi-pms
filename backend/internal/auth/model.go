package auth

import (
	"database/sql"
	"time"
)

type UserRow struct {
	ID                 uint64         `db:"id"`
	OrganizationID     sql.NullInt64  `db:"organization_id"`
	Email              string         `db:"email"`
	Phone              sql.NullString `db:"phone"`
	PasswordHash       string         `db:"password_hash"`
	PinHash            sql.NullString `db:"pin_hash"`
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
