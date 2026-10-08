// Package auth provides reusable user access-token primitives
// (AUTHENTICATION_MODEL.md). It never stores users, accounts or sessions:
// each user-domain service is its own account authority and uses Issuer to
// mint tokens; the API Gateway (and any service that needs to) uses Verifier
// with an explicit list of trusted domain issuers.
//
// Token profile:
//   - JWT, EdDSA/Ed25519, header typ "at+jwt" (RFC 9068) and kid
//   - iss: the domain's issuer name (e.g. "individual-owner-service")
//   - aud: the shared Real Estate API audience
//   - sub: the domain-local account ID (UUIDv7)
//   - iat, exp (15 minutes by default), jti (UUIDv7)
//   - optional sid (refresh session family) and org_id
//
// Business roles are intentionally not modelled here; each domain authorizes
// its own operations.
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// TokenType is the JOSE typ for user access tokens.
const TokenType = "at+jwt"

// DefaultAudience is the shared Real Estate API audience.
const DefaultAudience = "cosmicforge-realestate-api"

// DefaultAccessTTL is the default access-token lifetime.
const DefaultAccessTTL = 15 * time.Minute

// MaxAccessTTL is the hard upper bound accepted by verifiers.
const MaxAccessTTL = 60 * time.Minute

// AccessClaims is the user access-token claim set.
type AccessClaims struct {
	jwt.RegisteredClaims
	SessionID      string `json:"sid,omitempty"`
	OrganizationID string `json:"org_id,omitempty"`
}

var (
	ErrUntrustedIssuer = errors.New("auth: untrusted issuer")
	ErrInvalidSubject  = errors.New("auth: subject must be a UUIDv7")
)

// IssuerConfig configures a domain token issuer.
type IssuerConfig struct {
	Issuer   string // e.g. "artisan-service"
	Audience string // default DefaultAudience
	TTL      time.Duration
	Key      jwt.SigningKey
	Now      func() time.Time
}

// Issuer mints access tokens for one domain.
type Issuer struct{ cfg IssuerConfig }

// NewIssuer validates configuration.
func NewIssuer(cfg IssuerConfig) (*Issuer, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("auth: issuer required")
	}
	if !cfg.Key.Valid() {
		return nil, errors.New("auth: valid signing key required")
	}
	if cfg.Audience == "" {
		cfg.Audience = DefaultAudience
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultAccessTTL
	}
	if cfg.TTL > MaxAccessTTL {
		return nil, fmt.Errorf("auth: TTL exceeds %s", MaxAccessTTL)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Issuer{cfg: cfg}, nil
}

// IssueRequest describes a token to mint.
type IssueRequest struct {
	Subject        ids.ID
	SessionID      ids.ID
	OrganizationID ids.ID
}

// Issue signs a new access token.
func (i *Issuer) Issue(req IssueRequest) (string, AccessClaims, error) {
	if req.Subject.ValidateV7() != nil {
		return "", AccessClaims{}, ErrInvalidSubject
	}
	now := i.cfg.Now().UTC()
	c := AccessClaims{RegisteredClaims: jwt.RegisteredClaims{
		Issuer:    i.cfg.Issuer,
		Subject:   req.Subject.String(),
		Audience:  jwt.Audience{i.cfg.Audience},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(i.cfg.TTL)),
		ID:        ids.New().String(),
	}}
	if !req.SessionID.IsZero() {
		c.SessionID = req.SessionID.String()
	}
	if !req.OrganizationID.IsZero() {
		c.OrganizationID = req.OrganizationID.String()
	}
	tok, err := jwt.Sign(i.cfg.Key, TokenType, c)
	return tok, c, err
}

// TrustedIssuer binds an issuer name to the user domain it is authoritative
// for and the keys it signs with.
type TrustedIssuer struct {
	Issuer string
	Domain platform.Domain
	Keys   jwt.KeyResolver
}

// VerifierConfig configures verification.
type VerifierConfig struct {
	Issuers  []TrustedIssuer
	Audience string        // default DefaultAudience
	Leeway   time.Duration // default jwt.DefaultLeeway
	MaxTTL   time.Duration // default MaxAccessTTL
	Now      func() time.Time
}

// Verifier validates user access tokens from trusted domain issuers only.
type Verifier struct {
	cfg      VerifierConfig
	byIssuer map[string]TrustedIssuer
	resolver jwt.MultiResolver
}

// NewVerifier validates the trusted issuer list.
func NewVerifier(cfg VerifierConfig) (*Verifier, error) {
	if len(cfg.Issuers) == 0 {
		return nil, errors.New("auth: at least one trusted issuer required")
	}
	if cfg.Audience == "" {
		cfg.Audience = DefaultAudience
	}
	if cfg.MaxTTL <= 0 {
		cfg.MaxTTL = MaxAccessTTL
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	v := &Verifier{cfg: cfg, byIssuer: map[string]TrustedIssuer{}, resolver: jwt.MultiResolver{}}
	seenDomain := map[platform.Domain]bool{}
	for _, ti := range cfg.Issuers {
		if ti.Issuer == "" || ti.Keys == nil {
			return nil, errors.New("auth: trusted issuer requires name and keys")
		}
		if !platform.KnownDomain(string(ti.Domain)) {
			return nil, fmt.Errorf("auth: issuer %q maps to unknown domain %q", ti.Issuer, ti.Domain)
		}
		if _, dup := v.byIssuer[ti.Issuer]; dup {
			return nil, fmt.Errorf("auth: duplicate issuer %q", ti.Issuer)
		}
		if seenDomain[ti.Domain] {
			return nil, fmt.Errorf("auth: domain %q has more than one issuer", ti.Domain)
		}
		seenDomain[ti.Domain] = true
		v.byIssuer[ti.Issuer] = ti
		v.resolver[ti.Issuer] = ti.Keys
	}
	return v, nil
}

// Principal is the normalized, verified user identity.
type Principal struct {
	Domain         platform.Domain
	ActorID        ids.ID
	Issuer         string
	SessionID      ids.ID
	OrganizationID ids.ID
	TokenID        string
	ExpiresAt      time.Time
}

// Verify validates signature, issuer trust, audience, expiry (with bounded
// skew) and claim shape, returning the normalized actor. The domain comes from
// the trusted-issuer configuration, never from a token claim.
func (v *Verifier) Verify(ctx context.Context, token string) (Principal, error) {
	var c AccessClaims
	if _, err := jwt.Verify(ctx, token, TokenType, v.resolver, &c); err != nil {
		if errors.Is(err, jwt.ErrInvalidIssuer) {
			return Principal{}, ErrUntrustedIssuer
		}
		return Principal{}, err
	}
	ti, ok := v.byIssuer[c.Issuer]
	if !ok {
		return Principal{}, ErrUntrustedIssuer
	}
	if err := c.Validate(jwt.ValidationOptions{
		Now: v.cfg.Now, Leeway: v.cfg.Leeway, Audience: v.cfg.Audience, MaxLifetime: v.cfg.MaxTTL,
	}); err != nil {
		return Principal{}, err
	}
	sub, err := ids.ParseV7(c.Subject)
	if err != nil {
		return Principal{}, ErrInvalidSubject
	}
	p := Principal{Domain: ti.Domain, ActorID: sub, Issuer: c.Issuer, TokenID: c.ID, ExpiresAt: c.ExpiresAt.Time()}
	if c.SessionID != "" {
		if p.SessionID, err = ids.ParseV7(c.SessionID); err != nil {
			return Principal{}, jwt.ErrMalformed
		}
	}
	if c.OrganizationID != "" {
		if p.OrganizationID, err = ids.ParseV7(c.OrganizationID); err != nil {
			return Principal{}, jwt.ErrMalformed
		}
	}
	return p, nil
}
