package users

import (
	"database/sql"
	"time"
)

// UserRow is the organization-scoped projection of a user. Password material
// and PII ciphertext are never selected.
type UserRow struct {
	ID                 uint64         `db:"id"`
	OrganizationID     sql.NullInt64  `db:"organization_id"`
	Email              string         `db:"email"`
	Phone              sql.NullString `db:"phone"`
	FirstName          string         `db:"first_name"`
	LastName           string         `db:"last_name"`
	NationalIDLast4    sql.NullString `db:"national_id_last4"`
	Status             string         `db:"status"`
	MustChangePassword bool           `db:"must_change_password"`
	EmailVerifiedAt    sql.NullTime   `db:"email_verified_at"`
	PhoneVerifiedAt    sql.NullTime   `db:"phone_verified_at"`
	LastLoginAt        sql.NullTime   `db:"last_login_at"`
	CreatedAt          time.Time      `db:"created_at"`
	UpdatedAt          time.Time      `db:"updated_at"`
}

// UserWrite carries the columns this module is allowed to set.
type UserWrite struct {
	ID                 uint64
	OrganizationID     uint64
	Email              string
	Phone              sql.NullString
	FirstName          string
	LastName           string
	Status             string
	MustChangePassword bool
}

type RoleRow struct {
	ID          uint64         `db:"id"`
	Name        string         `db:"name"`
	Description sql.NullString `db:"description"`
}

type UserRoleRow struct {
	UserID      uint64         `db:"user_id"`
	RoleID      uint64         `db:"role_id"`
	RoleName    string         `db:"role_name"`
	Description sql.NullString `db:"role_description"`
}

type AssignmentRow struct {
	ID             uint64         `db:"id"`
	UserID         uint64         `db:"user_id"`
	PropertyID     uint64         `db:"property_id"`
	AssignmentRole string         `db:"assignment_role"`
	AssignedAt     time.Time      `db:"assigned_at"`
	PropertyName   string         `db:"property_name"`
	PropertyCode   sql.NullString `db:"property_code"`
}
