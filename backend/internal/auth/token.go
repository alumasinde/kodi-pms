package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"kodipms/internal/platform/config"
)

type Claims struct {
	OrgID uint64 `json:"org"`
	jwt.RegisteredClaims
}

type TokenService struct {
	secret    []byte
	issuer    string
	accessTTL time.Duration
}

func NewTokenService(cfg *config.Config) *TokenService {
	return &TokenService{
		secret:    []byte(cfg.JWTSecret),
		issuer:    cfg.TokenIssuer,
		accessTTL: cfg.AccessTTL,
	}
}

func (s *TokenService) NewAccess(userID, orgID uint64) (raw, jti string, expiresAt time.Time, err error) {
	now := time.Now().UTC()
	expiresAt = now.Add(s.accessTTL)
	jti = uuid.NewString()
	claims := Claims{
		OrgID: orgID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   strconv.FormatUint(userID, 10),
			ID:        jti,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	raw, err = tok.SignedString(s.secret)
	if err != nil {
		return "", "", time.Time{}, err
	}
	return raw, jti, expiresAt, nil
}

func (s *TokenService) ParseAccess(raw string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return s.secret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(s.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30*time.Second),
	)
	if err != nil {
		return nil, err
	}
	return claims, nil
}

func (s *TokenService) AccessTTL() time.Duration { return s.accessTTL }

// NewRefreshToken returns the raw value handed to the client and the SHA-256
// hash stored in the database.
func NewRefreshToken() (raw, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, HashToken(raw), nil
}

func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
