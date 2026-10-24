package auth

import (
	"strings"

	"golang.org/x/crypto/bcrypt"

	"kodipms/internal/platform/httpx"
)

// Cost 12. Used when the identifier does not exist so the response time is
// comparable to a real password check.
const dummyHash = "$2y$12$lvDZYxg40zWSK8ebMCO6yeLbsOAiqyRZWzBXzGZ/noXmfKElRESDO"

var commonPasswords = map[string]struct{}{
	"password": {}, "password1": {}, "password123": {}, "12345678": {},
	"123456789": {}, "1234567890": {}, "qwerty123": {}, "qwertyuiop": {},
	"letmein": {}, "welcome": {}, "welcome1": {}, "admin123": {},
	"iloveyou": {}, "abc12345": {}, "football": {}, "monkey123": {},
	"dragon123": {}, "sunshine": {}, "princess": {}, "passw0rd": {},
	"p@ssw0rd": {}, "changeme": {}, "kodipms123": {},
}

func ComparePassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func ValidatePassword(password string) error {
	fields := map[string]string{}
	if password == "" {
		fields["password"] = "required"
	} else {
		if len(password) < 8 {
			fields["password"] = "must be at least 8 characters"
		} else if len(password) > 72 {
			fields["password"] = "must be at most 72 characters"
		} else if strings.TrimSpace(password) == "" {
			fields["password"] = "cannot be only whitespace"
		} else if _, common := commonPasswords[strings.ToLower(password)]; common {
			fields["password"] = "is too common"
		}
	}
	if len(fields) > 0 {
		return httpx.Validation(fields)
	}
	return nil
}

const pinRule = "must be 4 to 6 digits"

func ValidatePIN(pin string) error {
	if pin == "" {
		return httpx.Validation(map[string]string{"pin": "required"})
	}
	if len(pin) < 4 || len(pin) > 6 {
		return httpx.Validation(map[string]string{"pin": pinRule})
	}
	for i := 0; i < len(pin); i++ {
		if pin[i] < '0' || pin[i] > '9' {
			return httpx.Validation(map[string]string{"pin": pinRule})
		}
	}
	return nil
}
