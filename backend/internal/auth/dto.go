package auth

import (
	"strings"
	"time"
)

type LoginInput struct {
	Identifier string `json:"identifier"`
	Email      string `json:"email"`
	Phone      string `json:"phone"`
	Password   string `json:"password"`
}

func (i LoginInput) Who() string {
	return whoFrom(i.Identifier, i.Email, i.Phone)
}

type PinLoginInput struct {
	Identifier string `json:"identifier"`
	Email      string `json:"email"`
	Phone      string `json:"phone"`
	Pin        string `json:"pin"`
}

func (i PinLoginInput) Who() string {
	return whoFrom(i.Identifier, i.Email, i.Phone)
}

func whoFrom(identifier, email, phone string) string {
	if v := strings.TrimSpace(identifier); v != "" {
		return v
	}
	if v := strings.TrimSpace(email); v != "" {
		return v
	}
	return strings.TrimSpace(phone)
}

type RefreshInput struct {
	RefreshToken string `json:"refresh_token"`
}

type ForgotPasswordInput struct {
	Identifier string `json:"identifier"`
	Email      string `json:"email"`
	Phone      string `json:"phone"`
}

func (i ForgotPasswordInput) Who() string {
	return whoFrom(i.Identifier, i.Email, i.Phone)
}

type ResetPasswordInput struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type AcceptInviteInput struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type ChangePasswordInput struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type SetPINInput struct {
	CurrentPassword string `json:"current_password"`
	Pin             string `json:"pin"`
}

type LogoutInput struct {
	RefreshToken string `json:"refresh_token"`
}

type ClientMeta struct {
	IP        string
	UserAgent string
}

type UserDTO struct {
	ID        uint64 `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Email     string `json:"email"`
	Phone     string `json:"phone,omitempty"`
	Status    string `json:"status"`
	HasPIN    bool   `json:"has_pin"`
}

type OrganizationDTO struct {
	ID           uint64 `json:"id"`
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	Domain       string `json:"domain,omitempty"`
	CurrencyCode string `json:"currency_code"`
	Timezone     string `json:"timezone"`
}

type ScopeDTO struct {
	AllProperties bool     `json:"all_properties"`
	PropertyIDs   []uint64 `json:"property_ids"`
	TenantID      *uint64  `json:"tenant_id,omitempty"`
	ContractorID  *uint64  `json:"contractor_id,omitempty"`
}

type MeDTO struct {
	User               UserDTO         `json:"user"`
	Organization       OrganizationDTO `json:"organization"`
	Roles              []string        `json:"roles"`
	Permissions        []string        `json:"permissions"`
	Scope              ScopeDTO        `json:"scope"`
	MustChangePassword bool            `json:"must_change_password"`
}

type SessionDTO struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int64     `json:"expires_in"`
	ExpiresAt    time.Time `json:"expires_at"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Me           *MeDTO    `json:"me,omitempty"`
}
