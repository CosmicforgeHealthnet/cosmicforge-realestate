// Package credentials holds business-domain credential primitives that the
// shared foundation does not provide: RFC 6238 TOTP, peppered hashing of
// one-time codes and tokens, recovery codes and authenticated encryption of
// TOTP seeds at rest.
//
// Everything secret is either hashed (codes, tokens, recovery codes, device
// keys) or sealed with AES-256-GCM (TOTP seeds); nothing here logs values.
package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP interoperability requires HMAC-SHA1.
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// Hasher computes keyed (peppered) hashes so a database leak alone does not
// allow offline guessing of low-entropy codes.
type Hasher struct{ pepper []byte }

// MinPepperBytes is the minimum pepper length.
const MinPepperBytes = 32

// NewHasher validates the pepper.
func NewHasher(pepper []byte) (*Hasher, error) {
	if len(pepper) < MinPepperBytes {
		return nil, fmt.Errorf("credentials: pepper must be at least %d bytes", MinPepperBytes)
	}
	return &Hasher{pepper: append([]byte(nil), pepper...)}, nil
}

// Hash returns hex HMAC-SHA256 over the length-prefixed parts (domain
// separation: purpose, binding identifier and secret are hashed together).
func (h *Hasher) Hash(parts ...string) string {
	m := hmac.New(sha256.New, h.pepper)
	var n [4]byte
	for _, p := range parts {
		binary.BigEndian.PutUint32(n[:], uint32(len(p)))
		m.Write(n[:])
		m.Write([]byte(p))
	}
	return hex.EncodeToString(m.Sum(nil))
}

// Equal compares two hex hashes in constant time.
func Equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// NumericCode returns a uniformly random decimal code of n digits.
func NumericCode(n int) string {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	v, err := rand.Int(rand.Reader, max)
	if err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return fmt.Sprintf("%0*d", n, v)
}

// OpaqueToken returns a prefixed 256-bit random token (e.g. MFA login token).
func OpaqueToken(prefix string) string { return prefix + security.RandomToken(32) }

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// RecoveryCode returns a human-friendly 10-character code formatted xxxxx-xxxxx.
func RecoveryCode() string {
	s := strings.ToLower(b32.EncodeToString(security.RandomBytes(7)))[:10]
	return s[:5] + "-" + s[5:]
}

// NormalizeRecoveryCode removes separators/whitespace and lowercases.
func NormalizeRecoveryCode(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer("-", "", " ", "").Replace(s)
}

// TOTP implements RFC 6238 with HMAC-SHA1, 6 digits and a 30-second step,
// the profile supported by mainstream authenticator apps.
type TOTP struct {
	Issuer string
	// Skew is the number of steps accepted either side of now (default 1).
	Skew int
}

const (
	totpStep   = 30
	totpDigits = 6
)

// NewSecret returns a fresh 160-bit TOTP secret.
func (TOTP) NewSecret() []byte { return security.RandomBytes(20) }

// EncodeSecret returns the base32 secret shown to the user.
func (TOTP) EncodeSecret(secret []byte) string { return b32.EncodeToString(secret) }

// URI returns the otpauth:// provisioning URI.
func (t TOTP) URI(secret []byte, account string) string {
	label := url.PathEscape(t.Issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", b32.EncodeToString(secret))
	q.Set("issuer", t.Issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// Step returns the RFC 6238 time step for at.
func Step(at time.Time) int64 { return at.Unix() / totpStep }

func hotp(secret []byte, counter int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1_000_000)
}

// Code returns the code for a step (tests, enrolment display).
func (TOTP) Code(secret []byte, step int64) string { return hotp(secret, step) }

// ErrTOTPMismatch means no step within the skew window matched.
var ErrTOTPMismatch = errors.New("credentials: totp mismatch")

// Verify checks code against now ± Skew steps and returns the matched step.
// Callers must persist the step and reject any code whose step is not
// strictly greater than the last accepted one (replay protection).
func (t TOTP) Verify(secret []byte, code string, now time.Time) (int64, error) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, ErrTOTPMismatch
	}
	skew := t.Skew
	if skew <= 0 {
		skew = 1
	}
	cur := Step(now)
	matched, found := int64(0), 0
	for d := -skew; d <= skew; d++ {
		if subtle.ConstantTimeCompare([]byte(hotp(secret, cur+int64(d))), []byte(code)) == 1 {
			matched, found = cur+int64(d), 1
		}
	}
	if found == 0 {
		return 0, ErrTOTPMismatch
	}
	return matched, nil
}

// Sealer encrypts small secrets (TOTP seeds) with AES-256-GCM. The key ID is
// stored next to each ciphertext so keys can be rotated without re-enrolment.
type Sealer struct {
	keyID string
	aead  cipher.AEAD
	old   map[string]cipher.AEAD
}

// NewSealer builds a sealer from a 32-byte key; retired maps old key IDs to
// keys that may still decrypt existing ciphertexts.
func NewSealer(keyID string, key []byte, retired map[string][]byte) (*Sealer, error) {
	mk := func(k []byte) (cipher.AEAD, error) {
		if len(k) != 32 {
			return nil, errors.New("credentials: encryption key must be 32 bytes")
		}
		b, err := aes.NewCipher(k)
		if err != nil {
			return nil, err
		}
		return cipher.NewGCM(b)
	}
	if keyID == "" {
		return nil, errors.New("credentials: encryption key id required")
	}
	a, err := mk(key)
	if err != nil {
		return nil, err
	}
	s := &Sealer{keyID: keyID, aead: a, old: map[string]cipher.AEAD{}}
	for id, k := range retired {
		ra, err := mk(k)
		if err != nil {
			return nil, err
		}
		s.old[id] = ra
	}
	return s, nil
}

// KeyID is the current key ID.
func (s *Sealer) KeyID() string { return s.keyID }

// Seal encrypts plaintext bound to aad (e.g. account and credential IDs).
func (s *Sealer) Seal(plaintext, aad []byte) []byte {
	nonce := security.RandomBytes(s.aead.NonceSize())
	return s.aead.Seal(nonce, nonce, plaintext, aad)
}

// Open decrypts a ciphertext produced under keyID.
func (s *Sealer) Open(keyID string, ciphertext, aad []byte) ([]byte, error) {
	a := s.aead
	if keyID != s.keyID {
		var ok bool
		if a, ok = s.old[keyID]; !ok {
			return nil, errors.New("credentials: unknown encryption key id")
		}
	}
	ns := a.NonceSize()
	if len(ciphertext) < ns {
		return nil, errors.New("credentials: ciphertext too short")
	}
	return a.Open(nil, ciphertext[:ns], ciphertext[ns:], aad)
}
