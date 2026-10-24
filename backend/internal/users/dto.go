package users

import (
	"encoding/json"
	"time"

	"kodipms/internal/platform/httpx"
)

type ClientMeta struct {
	IP        string
	UserAgent string
}

type ListQuery struct {
	Search string
	Status string
	RoleID uint64
	Page   httpx.Page
	Sort   string
}

type RoleDTO struct {
	ID          uint64 `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type UserLookupsDTO struct {
	Roles []RoleDTO `json:"roles"`
}

type UserSummaryDTO struct {
	ID                 uint64     `json:"id"`
	FullName           string     `json:"full_name"`
	FirstName          string     `json:"first_name"`
	LastName           string     `json:"last_name"`
	Email              string     `json:"email"`
	Phone              string     `json:"phone,omitempty"`
	Status             string     `json:"status"`
	Roles              []RoleDTO  `json:"roles"`
	MustChangePassword bool       `json:"must_change_password"`
	LastLoginAt        *time.Time `json:"last_login_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type UserDetailDTO struct {
	UserSummaryDTO
	NationalIDLast4 string     `json:"national_id_last4,omitempty"`
	EmailVerifiedAt *time.Time `json:"email_verified_at"`
	PhoneVerifiedAt *time.Time `json:"phone_verified_at"`
}

type UserListDTO struct {
	Users []UserSummaryDTO
	Total int
}

type AssignmentDTO struct {
	PropertyID   uint64    `json:"property_id"`
	PropertyName string    `json:"property_name"`
	PropertyCode string    `json:"property_code,omitempty"`
	Role         string    `json:"role"`
	AssignedAt   time.Time `json:"assigned_at"`
}

type AssignmentInput struct {
	PropertyID uint64 `json:"property_id"`
	Role       string `json:"role"`
}

type ReplaceAssignmentsInput struct {
	Assignments []AssignmentInput `json:"assignments"`
}

type CreateInput struct {
	Email              string   `json:"email"`
	Phone              string   `json:"phone"`
	FirstName          string   `json:"first_name"`
	LastName           string   `json:"last_name"`
	Password           string   `json:"password"`
	NationalID         string   `json:"national_id"`
	Status             string   `json:"status"`
	RoleIDs            []uint64 `json:"role_ids"`
	MustChangePassword *bool    `json:"must_change_password"`
}

// Plain string fields treat a blank value as "no change" because the columns
// are NOT NULL. Phone is nullable, so a pointer distinguishes an omitted field
// from an explicit empty string, which clears it. RawMessage national_id
// distinguishes an omitted field from an explicit JSON null, which clears the
// stored ciphertext.
type UpdateInput struct {
	Email              string          `json:"email"`
	Phone              *string         `json:"phone"`
	FirstName          string          `json:"first_name"`
	LastName           string          `json:"last_name"`
	Status             *string         `json:"status"`
	Password           *string         `json:"password"`
	NationalID         json.RawMessage `json:"national_id"`
	RoleIDs            *[]uint64       `json:"role_ids"`
	MustChangePassword *bool           `json:"must_change_password"`
}
