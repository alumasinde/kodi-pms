package pii

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func newTestCipher(t *testing.T, key byte) *Cipher {
	t.Helper()
	c, err := NewFromKey(bytes.Repeat([]byte{key}, keySize))
	if err != nil {
		t.Fatalf("NewFromKey: %v", err)
	}
	return c
}

func TestNew(t *testing.T) {
	key := bytes.Repeat([]byte{7}, keySize)

	t.Run("missing key", func(t *testing.T) {
		if _, err := New("   "); !errors.Is(err, ErrNoKey) {
			t.Fatalf("New(empty) error = %v", err)
		}
	})

	t.Run("invalid base64", func(t *testing.T) {
		if _, err := New("not base64!!"); err == nil {
			t.Fatal("expected a base64 error")
		}
	})

	t.Run("padded and raw encodings", func(t *testing.T) {
		padded := base64.StdEncoding.EncodeToString(key)
		raw := strings.TrimRight(padded, "=")
		encodings := map[string]string{
			"padded": padded,
			"raw":    raw,
			"spaced": "  " + padded + "  ",
		}
		for name, encoded := range encodings {
			c, err := New(encoded)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			ciphertext, err := c.Encrypt("12345678")
			if err != nil {
				t.Fatalf("%s encrypt: %v", name, err)
			}
			plain, err := c.Decrypt(ciphertext)
			if err != nil || plain != "12345678" {
				t.Fatalf("%s round trip = %q, %v", name, plain, err)
			}
		}
	})

	t.Run("wrong key size", func(t *testing.T) {
		encoded := base64.StdEncoding.EncodeToString([]byte("short"))
		_, err := New(encoded)
		if err == nil || !strings.Contains(err.Error(), "must be 32 bytes") {
			t.Fatalf("New(short) error = %v", err)
		}
	})
}

func TestNewFromKeyRejectsWrongSize(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33} {
		if _, err := NewFromKey(make([]byte, size)); err == nil {
			t.Fatalf("NewFromKey(%d bytes) succeeded", size)
		}
	}
}

func TestEncryptDecrypt(t *testing.T) {
	c := newTestCipher(t, 3)
	plain := "P051234567X"

	ciphertext, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if ciphertext[0] != cipherVersion {
		t.Fatalf("version = %d, want %d", ciphertext[0], cipherVersion)
	}
	wantLen := 1 + c.aead.NonceSize() + len(plain) + c.aead.Overhead()
	if len(ciphertext) != wantLen {
		t.Fatalf("length = %d, want %d", len(ciphertext), wantLen)
	}

	got, err := c.Decrypt(ciphertext)
	if err != nil || got != plain {
		t.Fatalf("Decrypt = %q, %v", got, err)
	}

	again, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt again: %v", err)
	}
	if bytes.Equal(ciphertext, again) {
		t.Fatal("two encryptions reused the same nonce")
	}
}

func TestDecryptRejectsBadCiphertext(t *testing.T) {
	c := newTestCipher(t, 4)
	ciphertext, err := c.Encrypt("secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if got, err := c.Decrypt(nil); err != nil || got != "" {
		t.Fatalf("Decrypt(nil) = %q, %v", got, err)
	}

	wrongVersion := append([]byte(nil), ciphertext...)
	wrongVersion[0] = 2
	if _, err := c.Decrypt(wrongVersion); err == nil || !strings.Contains(err.Error(), "unsupported ciphertext version") {
		t.Fatalf("wrong version error = %v", err)
	}

	if _, err := c.Decrypt([]byte{cipherVersion, 1, 2, 3}); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated error = %v", err)
	}

	tampered := append([]byte(nil), ciphertext...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := c.Decrypt(tampered); err == nil || !strings.Contains(err.Error(), "failed authentication") {
		t.Fatalf("tampered error = %v", err)
	}
}

func TestEncryptOptional(t *testing.T) {
	c := newTestCipher(t, 5)

	out, err := c.EncryptOptional("   ")
	if err != nil || out != nil {
		t.Fatalf("EncryptOptional(blank) = %v, %v", out, err)
	}

	out, err = c.EncryptOptional("  12345678  ")
	if err != nil {
		t.Fatalf("EncryptOptional: %v", err)
	}
	plain, err := c.Decrypt(out)
	if err != nil || plain != "12345678" {
		t.Fatalf("round trip = %q, %v", plain, err)
	}
}

func TestHash(t *testing.T) {
	c := newTestCipher(t, 6)
	other := newTestCipher(t, 7)

	if got := c.Hash("   "); got != nil {
		t.Fatalf("Hash(blank) = %v, want nil", got)
	}
	if got := c.Hash(" -/ "); got != nil {
		t.Fatalf("Hash(punctuation) = %v, want nil", got)
	}

	spaced := c.Hash(" 12-34 567 ")
	plain := c.Hash("1234567")
	if !bytes.Equal(spaced, plain) {
		t.Fatal("normalisation should ignore case, spaces and punctuation")
	}
	if !bytes.Equal(plain, c.Hash("1234567")) {
		t.Fatal("Hash is not deterministic")
	}
	if len(plain) != sha256.Size {
		t.Fatalf("hash length = %d, want %d", len(plain), sha256.Size)
	}
	if bytes.Equal(plain, other.Hash("1234567")) {
		t.Fatal("hashes from different keys should differ")
	}
}

func TestLast4(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"12345678", "5678"},
		{"12-34-5678", "5678"},
		{"123", "123"},
		{"AB12", "AB12"},
		{"", ""},
		{"  -  ", ""},
		{"ID-alpha-1234", "1234"},
	}
	for _, tt := range tests {
		if got := Last4(tt.in); got != tt.want {
			t.Fatalf("Last4(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
