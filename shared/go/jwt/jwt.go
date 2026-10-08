// Package jwt is a deliberately small JWS/JWT implementation that supports
// exactly one algorithm: EdDSA over Ed25519 (RFC 8037).
//
// It is the cryptographic core for both user access tokens (package auth) and
// service tokens (package serviceauth). Restricting to a single algorithm
// removes algorithm-confusion and "alg: none" classes of vulnerability.
//
// Verification order: structural checks -> header (alg/typ/kid/crit) ->
// unverified issuer used only to select a key -> Ed25519 signature check ->
// claims decoded and validated. Claims are never trusted before the signature
// is verified.
package jwt

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Algorithm is the only supported JWS algorithm.
const Algorithm = "EdDSA"

// MaxTokenSize bounds parsing work for hostile input.
const MaxTokenSize = 8 << 10

var (
	ErrMalformed        = errors.New("jwt: malformed token")
	ErrUnsupportedAlg   = errors.New("jwt: unsupported algorithm")
	ErrUnexpectedType   = errors.New("jwt: unexpected token type")
	ErrUnknownKey       = errors.New("jwt: unknown signing key")
	ErrInvalidSignature = errors.New("jwt: invalid signature")
	ErrExpired          = errors.New("jwt: token expired")
	ErrNotYetValid      = errors.New("jwt: token not yet valid")
	ErrIssuedInFuture   = errors.New("jwt: token issued in the future")
	ErrInvalidIssuer    = errors.New("jwt: invalid issuer")
	ErrInvalidAudience  = errors.New("jwt: invalid audience")
	ErrMissingClaim     = errors.New("jwt: missing required claim")
	ErrLifetimeTooLong  = errors.New("jwt: token lifetime exceeds policy")
)

// Header is the JOSE header.
type Header struct {
	Alg  string   `json:"alg"`
	Typ  string   `json:"typ,omitempty"`
	Kid  string   `json:"kid"`
	Crit []string `json:"crit,omitempty"`
}

// NumericDate is seconds since the epoch.
type NumericDate int64

// NewNumericDate converts t.
func NewNumericDate(t time.Time) NumericDate { return NumericDate(t.Unix()) }

// Time converts back to time.Time.
func (d NumericDate) Time() time.Time { return time.Unix(int64(d), 0).UTC() }

// Audience accepts a string or an array of strings, and marshals a single
// audience as a string.
type Audience []string

func (a Audience) MarshalJSON() ([]byte, error) {
	if len(a) == 1 {
		return json.Marshal(a[0])
	}
	return json.Marshal([]string(a))
}

func (a *Audience) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*a = Audience{s}
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return ErrMalformed
	}
	*a = list
	return nil
}

// Contains reports whether aud is present.
func (a Audience) Contains(aud string) bool {
	for _, x := range a {
		if x == aud {
			return true
		}
	}
	return false
}

// RegisteredClaims are the RFC 7519 registered claims used by the platform.
type RegisteredClaims struct {
	Issuer    string      `json:"iss"`
	Subject   string      `json:"sub,omitempty"`
	Audience  Audience    `json:"aud"`
	ExpiresAt NumericDate `json:"exp"`
	NotBefore NumericDate `json:"nbf,omitempty"`
	IssuedAt  NumericDate `json:"iat"`
	ID        string      `json:"jti"`
}

// Registered gives access to the registered claims of composite claim structs.
func (c *RegisteredClaims) Registered() *RegisteredClaims { return c }

// Claims is implemented by any struct embedding RegisteredClaims.
type Claims interface{ Registered() *RegisteredClaims }

// ValidationOptions describe time and audience policy.
type ValidationOptions struct {
	Now func() time.Time
	// Leeway tolerates clock skew for exp/nbf/iat. Default 30s; capped at 2m.
	Leeway time.Duration
	// Audience, if non-empty, must be contained in aud.
	Audience string
	// MaxLifetime, if > 0, bounds exp - iat.
	MaxLifetime time.Duration
}

// DefaultLeeway is the platform clock-skew allowance.
const DefaultLeeway = 30 * time.Second

// MaxLeeway caps configurable skew to keep expiry meaningful.
const MaxLeeway = 2 * time.Minute

// Validate checks required claims and time/audience policy.
func (c *RegisteredClaims) Validate(o ValidationOptions) error {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	leeway := o.Leeway
	if leeway <= 0 {
		leeway = DefaultLeeway
	}
	if leeway > MaxLeeway {
		leeway = MaxLeeway
	}
	t := now()
	if c.Issuer == "" || c.ID == "" || c.ExpiresAt == 0 || c.IssuedAt == 0 || len(c.Audience) == 0 {
		return ErrMissingClaim
	}
	if t.After(c.ExpiresAt.Time().Add(leeway)) || t.Equal(c.ExpiresAt.Time().Add(leeway)) {
		return ErrExpired
	}
	if c.NotBefore != 0 && t.Add(leeway).Before(c.NotBefore.Time()) {
		return ErrNotYetValid
	}
	if t.Add(leeway).Before(c.IssuedAt.Time()) {
		return ErrIssuedInFuture
	}
	if c.ExpiresAt <= c.IssuedAt {
		return ErrMalformed
	}
	if o.MaxLifetime > 0 && c.ExpiresAt.Time().Sub(c.IssuedAt.Time()) > o.MaxLifetime {
		return ErrLifetimeTooLong
	}
	if o.Audience != "" && !c.Audience.Contains(o.Audience) {
		return ErrInvalidAudience
	}
	return nil
}

// SigningKey is a private key with its key ID.
type SigningKey struct {
	KID        string
	PrivateKey ed25519.PrivateKey
}

// Valid reports whether the key is usable.
func (k SigningKey) Valid() bool { return k.KID != "" && len(k.PrivateKey) == ed25519.PrivateKeySize }

// PublicKey returns the public half.
func (k SigningKey) PublicKey() ed25519.PublicKey { return k.PrivateKey.Public().(ed25519.PublicKey) }

// KeyResolver finds the public key for (issuer, kid). Implementations must
// only return keys configured for that issuer.
type KeyResolver interface {
	ResolveKey(ctx context.Context, issuer, kid string) (ed25519.PublicKey, error)
}

var b64 = base64.RawURLEncoding

// Sign serialises header+claims and signs them. typ is placed in the header.
func Sign(key SigningKey, typ string, claims any) (string, error) {
	if !key.Valid() {
		return "", errors.New("jwt: invalid signing key")
	}
	h, err := json.Marshal(Header{Alg: Algorithm, Typ: typ, Kid: key.KID})
	if err != nil {
		return "", err
	}
	p, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := b64.EncodeToString(h) + "." + b64.EncodeToString(p)
	sig := ed25519.Sign(key.PrivateKey, []byte(signingInput))
	return signingInput + "." + b64.EncodeToString(sig), nil
}

// Parsed is a structurally valid token prior to signature verification.
type Parsed struct {
	Header       Header
	rawPayload   []byte
	signingInput []byte
	signature    []byte
}

// parse performs structural checks only.
func parse(token string) (*Parsed, error) {
	if len(token) == 0 || len(token) > MaxTokenSize {
		return nil, ErrMalformed
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, ErrMalformed
	}
	hb, err := b64.DecodeString(parts[0])
	if err != nil {
		return nil, ErrMalformed
	}
	var h Header
	dec := json.NewDecoder(bytes.NewReader(hb))
	if err := dec.Decode(&h); err != nil {
		return nil, ErrMalformed
	}
	pb, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, ErrMalformed
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, ErrMalformed
	}
	if h.Alg != Algorithm {
		return nil, ErrUnsupportedAlg
	}
	if len(h.Crit) > 0 {
		// No critical extensions are understood.
		return nil, ErrMalformed
	}
	if h.Kid == "" {
		return nil, ErrMalformed
	}
	return &Parsed{Header: h, rawPayload: pb, signingInput: []byte(parts[0] + "." + parts[1]), signature: sig}, nil
}

// Verify parses token, requires typ to equal expectedTyp (when non-empty),
// resolves the key for (iss, kid), verifies the signature and decodes the
// claims into out. Time/audience validation is the caller's next step.
func Verify(ctx context.Context, token, expectedTyp string, resolver KeyResolver, out Claims) (Header, error) {
	p, err := parse(token)
	if err != nil {
		return Header{}, err
	}
	if expectedTyp != "" && !strings.EqualFold(p.Header.Typ, expectedTyp) {
		return Header{}, ErrUnexpectedType
	}
	var peek struct {
		Issuer string `json:"iss"`
	}
	if err := json.Unmarshal(p.rawPayload, &peek); err != nil || peek.Issuer == "" {
		return Header{}, ErrMalformed
	}
	pub, err := resolver.ResolveKey(ctx, peek.Issuer, p.Header.Kid)
	if err != nil {
		if errors.Is(err, ErrUnknownKey) || errors.Is(err, ErrInvalidIssuer) {
			return Header{}, err
		}
		return Header{}, fmt.Errorf("%w: %w", ErrUnknownKey, err)
	}
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, p.signingInput, p.signature) {
		return Header{}, ErrInvalidSignature
	}
	dec := json.NewDecoder(bytes.NewReader(p.rawPayload))
	if err := dec.Decode(out); err != nil {
		return Header{}, ErrMalformed
	}
	if out.Registered().Issuer != peek.Issuer {
		return Header{}, ErrMalformed
	}
	return p.Header, nil
}

// UnverifiedHeader returns the header without verifying anything. Use only
// for routing/diagnostics; never for authorization.
func UnverifiedHeader(token string) (Header, error) {
	p, err := parse(token)
	if err != nil {
		return Header{}, err
	}
	return p.Header, nil
}
