// Package passwords implements Argon2id password hashing (AUTHENTICATION_MODEL.md).
//
// Hashes use the PHC string format so parameters travel with each hash:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<salt>$<key>
//
// When the platform raises Params, NeedsRehash reports true for older hashes
// so a domain can transparently re-hash on the next successful login.
//
// Raw passwords are never logged or returned; the Hasher bounds concurrent
// hashing so a burst of logins cannot exhaust memory.
package passwords

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// Params are Argon2id cost parameters.
type Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultParams is the current platform baseline (exceeds the OWASP 2024
// minimum of m=19MiB,t=2,p=1). Increase over time; old hashes are rehashed.
var DefaultParams = Params{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32}

// Policy bounds password input. Length is measured in Unicode code points.
type Policy struct {
	MinLength int
	MaxLength int
}

// DefaultPolicy is a conservative baseline; domains may tighten it.
var DefaultPolicy = Policy{MinLength: 10, MaxLength: 128}

var (
	ErrMismatch        = errors.New("passwords: password does not match")
	ErrInvalidHash     = errors.New("passwords: invalid hash encoding")
	ErrIncompatible    = errors.New("passwords: incompatible argon2 version")
	ErrTooShort        = errors.New("passwords: password too short")
	ErrTooLong         = errors.New("passwords: password too long")
	ErrInvalidEncoding = errors.New("passwords: password is not valid UTF-8")
)

// Validate checks a candidate password against the policy.
func (p Policy) Validate(password string) error {
	if !utf8.ValidString(password) {
		return ErrInvalidEncoding
	}
	n := utf8.RuneCountInString(password)
	if n < p.MinLength {
		return ErrTooShort
	}
	if p.MaxLength > 0 && n > p.MaxLength {
		return ErrTooLong
	}
	return nil
}

// maxInputBytes caps hashing input independent of policy (DoS protection).
const maxInputBytes = 4096

// Hasher hashes and verifies passwords with bounded concurrency.
type Hasher struct {
	params Params
	sem    chan struct{}
}

// NewHasher returns a Hasher using params, allowing maxConcurrent
// simultaneous hash computations (memory = maxConcurrent * MemoryKiB).
func NewHasher(params Params, maxConcurrent int) (*Hasher, error) {
	if params.MemoryKiB < 8*1024 || params.Iterations < 1 || params.Parallelism < 1 || params.SaltLength < 16 || params.KeyLength < 16 {
		return nil, errors.New("passwords: parameters below platform minimum")
	}
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Hasher{params: params, sem: make(chan struct{}, maxConcurrent)}, nil
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.sem }

// Hash returns the PHC-encoded Argon2id hash of password.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	if len(password) > maxInputBytes {
		return "", ErrTooLong
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	p := h.params
	salt := security.RandomBytes(int(p.SaltLength))
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, p.KeyLength)
	return encode(p, salt, key), nil
}

// Verify compares password to encoded in constant time. It returns
// (needsRehash, nil) on match and ErrMismatch otherwise.
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
	p, salt, want, err := decode(encoded)
	if err != nil {
		return false, err
	}
	if len(password) > maxInputBytes {
		return false, ErrMismatch
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, ErrMismatch
	}
	return h.needsRehash(p, salt, want), nil
}

// NeedsRehash reports whether encoded was produced with weaker/different
// parameters than the hasher's current ones.
func (h *Hasher) NeedsRehash(encoded string) bool {
	p, salt, key, err := decode(encoded)
	if err != nil {
		return true
	}
	return h.needsRehash(p, salt, key)
}

func (h *Hasher) needsRehash(p Params, salt, key []byte) bool {
	c := h.params
	return p.MemoryKiB != c.MemoryKiB || p.Iterations != c.Iterations || p.Parallelism != c.Parallelism ||
		uint32(len(salt)) != c.SaltLength || uint32(len(key)) != c.KeyLength
}

var b64 = base64.RawStdEncoding

func encode(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Iterations, p.Parallelism, b64.EncodeToString(salt), b64.EncodeToString(key))
}

func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return Params{}, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return Params{}, nil, nil, ErrIncompatible
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Iterations, &p.Parallelism); err != nil {
		return Params{}, nil, nil, ErrInvalidHash
	}
	// Refuse absurd parameters from a tampered store (memory DoS).
	if p.MemoryKiB == 0 || p.MemoryKiB > 1<<20 || p.Iterations == 0 || p.Iterations > 64 || p.Parallelism == 0 {
		return Params{}, nil, nil, ErrInvalidHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return Params{}, nil, nil, ErrInvalidHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 128 {
		return Params{}, nil, nil, ErrInvalidHash
	}
	p.SaltLength, p.KeyLength = uint32(len(salt)), uint32(len(key))
	return p, salt, key, nil
}
