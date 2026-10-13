// Package security holds small, dependency-free security primitives shared by
// every component: secret wrappers that never print, random tokens,
// constant-time comparison, Ed25519 key loading, HMAC webhook verification and
// HTTP security headers.
package security

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

const redacted = "[REDACTED]"

// Secret wraps sensitive configuration (keys, provider secrets, DSNs with
// passwords). Formatting, logging and JSON encoding never reveal the value.
type Secret struct{ v string }

// NewSecret wraps s.
func NewSecret(s string) Secret { return Secret{v: s} }

// Reveal returns the raw value. Call only at the point of use.
func (s Secret) Reveal() string { return s.v }

// IsEmpty reports whether no value is set.
func (s Secret) IsEmpty() bool { return s.v == "" }

func (s Secret) String() string                { return redacted }
func (s Secret) GoString() string              { return redacted }
func (s Secret) LogValue() slog.Value          { return slog.StringValue(redacted) }
func (s Secret) MarshalJSON() ([]byte, error)  { return []byte(`"` + redacted + `"`), nil }
func (s Secret) MarshalText() ([]byte, error)  { return []byte(redacted), nil }
func (s Secret) Format(f fmt.State, verb rune) { _, _ = f.Write([]byte(redacted)) }

// RandomBytes returns n cryptographically random bytes.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b) // never fails on supported platforms
	return b
}

// RandomToken returns n random bytes encoded as unpadded base64url.
func RandomToken(n int) string { return base64.RawURLEncoding.EncodeToString(RandomBytes(n)) }

// ConstantTimeEqual compares two strings without leaking timing information
// about their content (length may still leak).
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// SHA256Hex returns the lowercase hex SHA-256 of b.
func SHA256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// HMACSHA256Hex computes a hex HMAC-SHA256.
func HMACSHA256Hex(secret, payload []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(payload)
	return hex.EncodeToString(m.Sum(nil))
}

// VerifyHMACSHA256Hex verifies a hex HMAC-SHA256 signature in constant time.
func VerifyHMACSHA256Hex(secret, payload []byte, signatureHex string) bool {
	want, err := hex.DecodeString(strings.TrimSpace(signatureHex))
	if err != nil || len(want) != sha256.Size {
		return false
	}
	m := hmac.New(sha256.New, secret)
	m.Write(payload)
	return hmac.Equal(m.Sum(nil), want)
}

var ErrInvalidKey = errors.New("security: invalid Ed25519 key")

// ParseEd25519PrivateKey accepts a PEM "PRIVATE KEY" (PKCS#8), or base64
// (std or url, padded or not) of a 32-byte seed or 64-byte private key.
func ParseEd25519PrivateKey(s string) (ed25519.PrivateKey, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, ErrInvalidKey
	}
	if strings.HasPrefix(s, "-----BEGIN") {
		block, _ := pem.Decode([]byte(s))
		if block == nil {
			return nil, ErrInvalidKey
		}
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, ErrInvalidKey
		}
		pk, ok := k.(ed25519.PrivateKey)
		if !ok {
			return nil, ErrInvalidKey
		}
		return pk, nil
	}
	raw, err := decodeBase64(s)
	if err != nil {
		return nil, ErrInvalidKey
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		pk := ed25519.PrivateKey(raw)
		// Verify the embedded public half matches the seed.
		if !ed25519.NewKeyFromSeed(pk.Seed()).Equal(pk) {
			return nil, ErrInvalidKey
		}
		return pk, nil
	}
	return nil, ErrInvalidKey
}

// ParseEd25519PublicKey accepts a PEM "PUBLIC KEY" (PKIX) or base64 of the
// 32-byte raw key.
func ParseEd25519PublicKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "-----BEGIN") {
		block, _ := pem.Decode([]byte(s))
		if block == nil {
			return nil, ErrInvalidKey
		}
		k, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, ErrInvalidKey
		}
		pk, ok := k.(ed25519.PublicKey)
		if !ok {
			return nil, ErrInvalidKey
		}
		return pk, nil
	}
	raw, err := decodeBase64(s)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, ErrInvalidKey
	}
	return ed25519.PublicKey(raw), nil
}

// EncodeEd25519PublicKey returns the unpadded base64url of the raw public key.
func EncodeEd25519PublicKey(k ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(k)
}

// EncodeEd25519Seed returns the unpadded base64url of the private seed.
func EncodeEd25519Seed(k ed25519.PrivateKey) string {
	return base64.RawURLEncoding.EncodeToString(k.Seed())
}

func decodeBase64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, ErrInvalidKey
}

// sensitiveKeyFragments identifies attribute/field names whose values must
// never be logged.
var sensitiveKeyFragments = []string{
	"password", "passwd", "secret", "token", "authorization", "cookie",
	"private_key", "privatekey", "signing_key", "api_key", "apikey",
	"credential", "session_key", "refresh", "otp_code", "card_number",
	"cvv", "signature", "dsn", "database_url",
	"signed_url", "download_url", "document_url", "identity_document", "account_number", "otp", "mfa_code",
}

// IsSensitiveKey reports whether a field/attribute name denotes a secret.
func IsSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, f := range sensitiveKeyFragments {
		if strings.Contains(k, f) {
			return true
		}
	}
	return false
}

// Redacted is the placeholder used for secrets in logs.
const Redacted = redacted
