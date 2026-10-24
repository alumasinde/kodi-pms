package auth

import "testing"

func TestDemoPasswordHash(t *testing.T) {
	const hash = "$2y$12$lvDZYxg40zWSK8ebMCO6yeLbsOAiqyRZWzBXzGZ/noXmfKElRESDO"
	if !ComparePassword(hash, "Demo@2026!") {
		t.Fatal("demo bcrypt hash does not verify with Demo@2026!")
	}
	if ComparePassword(hash, "wrong-password") {
		t.Fatal("demo hash accepted the wrong password")
	}
}

func TestValidatePassword(t *testing.T) {
	cases := []struct {
		password string
		ok       bool
	}{
		{"Demo@2026!", true},
		{"short", false},
		{"password123", false},
	}
	for _, tc := range cases {
		err := ValidatePassword(tc.password)
		if tc.ok && err != nil {
			t.Errorf("%q should be valid: %v", tc.password, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%q should be rejected", tc.password)
		}
	}
}
