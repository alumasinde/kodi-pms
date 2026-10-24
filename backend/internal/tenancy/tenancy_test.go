package tenancy

import "testing"

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"LocalHost:8080": "localhost",
		"127.0.0.1:3000": "127.0.0.1",
		"example.co.ke.": "example.co.ke",
		" ":              "",
	}
	for in, want := range cases {
		if got := NormalizeHost(in); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}
