// Package pii encrypts sensitive personal fields at rest with AES-256-GCM
// and records reads of those fields.
//
// Ciphertext layout: version byte (0x01) || 12-byte nonce || GCM ciphertext.
// Store the returned bytes in the * VARBINARY columns created by migrations
// 000002 and 000009. Hash is a keyed HMAC-SHA256 of the normalised value,
// suitable for duplicate detection in national_id_hash without decrypting.
package pii

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	keySize       = 32
	cipherVersion = byte(1)
	hashLabel     = "kodipms/pii/hash/v1"
)

var ErrNoKey = errors.New("pii: encryption key is not configured")

type Cipher struct {
	aead   cipher.AEAD
	macKey [32]byte
}

// New accepts a base64-encoded 32-byte key. Both padded and raw standard
// encodings are accepted so keys pasted from `openssl rand -base64 32` and
// similar tools work as-is.
func New(base64Key string) (*Cipher, error) {
	raw := strings.TrimSpace(base64Key)
	if raw == "" {
		return nil, ErrNoKey
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil {
		return nil, fmt.Errorf("pii: key must be base64: %w", err)
	}
	return NewFromKey(key)
}

func NewFromKey(key []byte) (*Cipher, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("pii: key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("pii: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("pii: %w", err)
	}
	mac := sha256.New()
	mac.Write(key)
	mac.Write([]byte(hashLabel))
	var macKey [32]byte
	copy(macKey[:], mac.Sum(nil))
	return &Cipher{aead: aead, macKey: macKey}, nil
}

// Encrypt seals plain. The caller stores the raw bytes; do not base64 them
// into the VARBINARY column.
func (c *Cipher) Encrypt(plain string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("pii: nonce: %w", err)
	}
	out := make([]byte, 1, 1+len(nonce)+len(plain)+c.aead.Overhead())
	out[0] = cipherVersion
	out = append(out, nonce...)
	out = c.aead.Seal(out, nonce, []byte(plain), nil)
	return out, nil
}

// EncryptOptional treats a blank value as absent and returns nil so it maps to
// SQL NULL.
func (c *Cipher) EncryptOptional(plain string) ([]byte, error) {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return nil, nil
	}
	return c.Encrypt(plain)
}

func (c *Cipher) Decrypt(ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	if ciphertext[0] != cipherVersion {
		return "", fmt.Errorf("pii: unsupported ciphertext version %d", ciphertext[0])
	}
	body := ciphertext[1:]
	nonceSize := c.aead.NonceSize()
	if len(body) < nonceSize+c.aead.Overhead() {
		return "", errors.New("pii: ciphertext is truncated")
	}
	plain, err := c.aead.Open(nil, body[:nonceSize], body[nonceSize:], nil)
	if err != nil {
		return "", errors.New("pii: ciphertext failed authentication")
	}
	return string(plain), nil
}

// Hash normalises then HMAC-SHA256s a value for equality checks. It returns
// nil for a blank value so it maps to SQL NULL. Rotating the PII key changes
// the hashes, so re-compute them when re-encrypting during a rotation.
func (c *Cipher) Hash(value string) []byte {
	normal := normalize(value)
	if normal == "" {
		return nil
	}
	mac := hmac.New(sha256.New, c.macKey[:])
	mac.Write([]byte(normal))
	return mac.Sum(nil)
}

// Last4 returns the final four characters of the normalised value, for display
// without decrypting the full value.
func Last4(value string) string {
	normal := normalize(value)
	if normal == "" {
		return ""
	}
	runes := []rune(normal)
	if len(runes) <= 4 {
		return string(runes)
	}
	return string(runes[len(runes)-4:])
}

// normalize upper-cases and keeps only letters and digits. Kenyan national IDs
// and KRA PINs are frequently typed with spaces, slashes or hyphens.
func normalize(value string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(value)) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}
