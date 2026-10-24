package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"kodipms/internal/platform/config"
)

func newTokenTestService(secret, issuer string, ttl time.Duration) *TokenService {
	return NewTokenService(&config.Config{
		JWTSecret:   secret,
		TokenIssuer: issuer,
		AccessTTL:   ttl,
	})
}

func TestAccessTokenRoundTrip(t *testing.T) {
	svc := newTokenTestService("test-secret", "kodipms-test", 15*time.Minute)

	raw, jti, expiresAt, err := svc.NewAccess(42, 7, 3)
	if err != nil {
		t.Fatalf("NewAccess: %v", err)
	}
	if raw == "" || jti == "" {
		t.Fatalf("raw = %q, jti = %q", raw, jti)
	}
	if svc.AccessTTL() != 15*time.Minute {
		t.Fatalf("AccessTTL = %v", svc.AccessTTL())
	}

	claims, err := svc.ParseAccess(raw)
	if err != nil {
		t.Fatalf("ParseAccess: %v", err)
	}
	if claims.Subject != "42" {
		t.Fatalf("subject = %q", claims.Subject)
	}
	if claims.OrgID != 7 || claims.TokenVersion != 3 {
		t.Fatalf("claims = %+v", claims)
	}
	if claims.ID != jti {
		t.Fatalf("jti = %q, want %q", claims.ID, jti)
	}
	if claims.Issuer != "kodipms-test" {
		t.Fatalf("issuer = %q", claims.Issuer)
	}
	if claims.ExpiresAt == nil || claims.ExpiresAt.Unix() != expiresAt.Unix() {
		t.Fatalf("expires at = %v, want %v", claims.ExpiresAt, expiresAt)
	}
	if claims.IssuedAt == nil {
		t.Fatal("issued at missing")
	}
}

func TestParseAccessRejectsInvalidTokens(t *testing.T) {
	const issuer = "kodipms-test"
	svc := newTokenTestService("secret-a", issuer, 15*time.Minute)
	raw, _, _, err := svc.NewAccess(1, 2, 0)
	if err != nil {
		t.Fatalf("NewAccess: %v", err)
	}

	t.Run("wrong secret", func(t *testing.T) {
		other := newTokenTestService("secret-b", issuer, 15*time.Minute)
		if _, err := other.ParseAccess(raw); err == nil {
			t.Fatal("expected a signature error")
		}
	})

	t.Run("wrong issuer", func(t *testing.T) {
		other := newTokenTestService("secret-a", "other-issuer", 15*time.Minute)
		if _, err := other.ParseAccess(raw); err == nil {
			t.Fatal("expected an issuer error")
		}
	})

	t.Run("expired", func(t *testing.T) {
		expired := newTokenTestService("secret-a", issuer, -time.Minute)
		expiredRaw, _, _, err := expired.NewAccess(1, 2, 0)
		if err != nil {
			t.Fatalf("NewAccess: %v", err)
		}
		if _, err := svc.ParseAccess(expiredRaw); err == nil {
			t.Fatal("expected an expiration error")
		}
	})

	t.Run("tampered", func(t *testing.T) {
		if _, err := svc.ParseAccess(raw + "x"); err == nil {
			t.Fatal("expected a signature error")
		}
	})

	t.Run("none algorithm", func(t *testing.T) {
		tok := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{
			OrgID:        2,
			TokenVersion: 0,
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    issuer,
				Subject:   "1",
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
		})
		unsigned, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatalf("sign none: %v", err)
		}
		if _, err := svc.ParseAccess(unsigned); err == nil {
			t.Fatal("expected the none algorithm to be rejected")
		}
	})
}

func TestNewRefreshToken(t *testing.T) {
	raw, hash, err := NewRefreshToken()
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	if raw == "" || hash == "" {
		t.Fatalf("raw = %q, hash = %q", raw, hash)
	}
	if strings.ContainsAny(raw, "=+/") {
		t.Fatalf("raw token is not URL-safe: %q", raw)
	}
	if len(hash) != 64 {
		t.Fatalf("hash length = %d, want 64 hex characters", len(hash))
	}
	if HashToken(raw) != hash {
		t.Fatal("returned hash does not match HashToken(raw)")
	}

	again, _, err := NewRefreshToken()
	if err != nil {
		t.Fatalf("NewRefreshToken again: %v", err)
	}
	if again == raw {
		t.Fatal("refresh tokens should be unique")
	}
}

func TestHashToken(t *testing.T) {
	const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := HashToken(""); got != emptySHA256 {
		t.Fatalf("HashToken(empty) = %q", got)
	}
	if HashToken("abc") != HashToken("abc") {
		t.Fatal("HashToken is not deterministic")
	}
	if HashToken("abc") == HashToken("abd") {
		t.Fatal("different inputs should produce different hashes")
	}
}
