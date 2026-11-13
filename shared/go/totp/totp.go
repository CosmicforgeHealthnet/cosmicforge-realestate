// Package totp provides RFC 6238 time-based one-time passwords and an
// AES-256-GCM sealer for TOTP seeds at rest. It is the shared form of the
// primitives Business Operations uses for staff MFA, for domain services
// whose privileged actions require step-up authentication.
package totp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP is defined over HMAC-SHA1 (authenticator compatibility).
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

const (
	stepSeconds = 30
	digits      = 6
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// ErrMismatch means no step within the skew window matched.
var ErrMismatch = errors.New("totp: code mismatch")

// NewSecret returns a fresh 160-bit seed.
func NewSecret() []byte { return security.RandomBytes(20) }

// Encode returns the base32 seed shown to the user once at enrolment.
func Encode(secret []byte) string { return b32.EncodeToString(secret) }

// URI returns the otpauth:// provisioning URI.
func URI(issuer, account string, secret []byte) string {
	q := url.Values{}
	q.Set("secret", Encode(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + q.Encode()
}

// Step returns the time step for at.
func Step(at time.Time) int64 { return at.Unix() / stepSeconds }

// Code returns the code for a step.
func Code(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", digits, v%1_000_000)
}

// Verify checks code against now ±1 step and returns the matched step.
// Callers persist the step and reject any code whose step is not strictly
// greater than the last accepted one (replay protection).
func Verify(secret []byte, code string, now time.Time) (int64, error) {
	code = strings.TrimSpace(code)
	if len(code) != digits {
		return 0, ErrMismatch
	}
	cur := Step(now)
	matched, found := int64(0), 0
	for d := int64(-1); d <= 1; d++ {
		if subtle.ConstantTimeCompare([]byte(Code(secret, cur+d)), []byte(code)) == 1 {
			matched, found = cur+d, 1
		}
	}
	if found == 0 {
		return 0, ErrMismatch
	}
	return matched, nil
}

// Sealer encrypts seeds with AES-256-GCM bound to additional data.
type Sealer struct {
	keyID string
	aead  cipher.AEAD
}

// NewSealer builds a sealer from a 32-byte key.
func NewSealer(keyID string, key []byte) (*Sealer, error) {
	if keyID == "" || len(key) != 32 {
		return nil, errors.New("totp: a key id and a 32-byte key are required")
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return &Sealer{keyID: keyID, aead: a}, nil
}

// KeyID is the sealing key id stored next to each ciphertext.
func (s *Sealer) KeyID() string { return s.keyID }

// Seal encrypts plaintext bound to aad.
func (s *Sealer) Seal(plaintext, aad []byte) []byte {
	nonce := security.RandomBytes(s.aead.NonceSize())
	return s.aead.Seal(nonce, nonce, plaintext, aad)
}

// Open decrypts a ciphertext sealed under keyID.
func (s *Sealer) Open(keyID string, ciphertext, aad []byte) ([]byte, error) {
	if keyID != s.keyID {
		return nil, errors.New("totp: unknown sealing key id")
	}
	ns := s.aead.NonceSize()
	if len(ciphertext) < ns {
		return nil, errors.New("totp: ciphertext too short")
	}
	return s.aead.Open(nil, ciphertext[:ns], ciphertext[ns:], aad)
}
