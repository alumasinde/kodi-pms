package auth

import (
	"errors"
	"testing"

	"kodipms/internal/platform/httpx"
)

func TestValidatePIN(t *testing.T) {
	valid := []string{"1234", "2026", "0000", "12345", "123456"}
	for _, pin := range valid {
		if err := ValidatePIN(pin); err != nil {
			t.Errorf("ValidatePIN(%q) = %v, want nil", pin, err)
		}
	}

	invalid := []string{"", "123", "1234567", "12a4", "12 34", "١٢٣٤", "-123", "123\n"}
	for _, pin := range invalid {
		err := ValidatePIN(pin)
		var he *httpx.Err
		if !errors.As(err, &he) || he.Fields["pin"] == "" {
			t.Errorf("ValidatePIN(%q) = %v, want a pin field validation error", pin, err)
		}
	}
}
