// Package serviceauth implements service-to-service authentication and
// authorization (SERVICE_AUTH_STANDARD.md).
//
// Each deployable component has an Ed25519 identity. Calls carry a
// short-lived JWT:
//
//	header: alg=EdDSA, typ=service+jwt, kid
//	claims: iss=<calling service>, sub=<calling service>, aud=<destination service>,
//	        iat, exp (<= iat+5m), jti (UUIDv7)
//	        optional delegation: actor_domain, actor_id, organization_id, original_request_id
//
// Receivers verify issuer (against the trusted service registry), kid,
// signature, exact audience, expiry and lifetime — and then authorize the
// caller against an explicit capability policy. A valid token alone never
// grants every internal operation.
package serviceauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"crypto/ed25519"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// TokenType is the JOSE typ of service tokens.
const TokenType = "service+jwt"

// MaxTTL is the maximum service-token lifetime.
const MaxTTL = 5 * time.Minute

// DefaultTTL is the default service-token lifetime.
const DefaultTTL = 2 * time.Minute

var (
	ErrUnknownService = errors.New("serviceauth: unknown calling service")
	ErrWrongAudience  = errors.New("serviceauth: token not issued for this service")
	ErrBadDelegation  = errors.New("serviceauth: invalid delegation claims")
	ErrNotAuthorized  = errors.New("serviceauth: caller not authorized for capability")
)

// Claims is the service-token claim set.
type Claims struct {
	jwt.RegisteredClaims
	ActorDomain       string `json:"actor_domain,omitempty"`
	ActorID           string `json:"actor_id,omitempty"`
	OrganizationID    string `json:"organization_id,omitempty"`
	OriginalRequestID string `json:"original_request_id,omitempty"`
}

// Delegation is the user context a service acts on behalf of.
type Delegation struct {
	ActorDomain       platform.Domain
	ActorID           ids.ID
	OrganizationID    ids.ID
	OriginalRequestID string
}

// HasActor reports whether an actor is delegated.
func (d Delegation) HasActor() bool { return d.ActorDomain != "" && !d.ActorID.IsZero() }

// SignerConfig configures a Signer.
type SignerConfig struct {
	Service platform.Service
	Key     jwt.SigningKey
	TTL     time.Duration
	Now     func() time.Time
}

// Signer mints service tokens for the local component.
type Signer struct{ cfg SignerConfig }

// NewSigner validates configuration.
func NewSigner(cfg SignerConfig) (*Signer, error) {
	if !platform.KnownService(string(cfg.Service)) {
		return nil, fmt.Errorf("serviceauth: unknown service %q", cfg.Service)
	}
	if !cfg.Key.Valid() {
		return nil, errors.New("serviceauth: valid signing key required")
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.TTL > MaxTTL {
		return nil, fmt.Errorf("serviceauth: TTL exceeds %s", MaxTTL)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Signer{cfg: cfg}, nil
}

// Service returns the signer's identity.
func (s *Signer) Service() platform.Service { return s.cfg.Service }

// Token implements httpx.TokenSource. Delegation is derived from the
// verified request context (actor, organization, request ID); it is never
// taken from client-supplied headers.
func (s *Signer) Token(ctx context.Context, audience string) (string, error) {
	v := requestcontext.From(ctx)
	d := Delegation{OriginalRequestID: v.RequestID, OrganizationID: v.OrganizationID}
	if !v.Actor.IsZero() {
		d.ActorDomain, d.ActorID = platform.Domain(v.Actor.Domain), v.Actor.ID
	}
	return s.TokenFor(audience, d)
}

// TokenFor mints a token for audience with explicit delegation.
func (s *Signer) TokenFor(audience string, d Delegation) (string, error) {
	if !platform.KnownService(audience) {
		return "", fmt.Errorf("serviceauth: unknown audience %q", audience)
	}
	now := s.cfg.Now().UTC()
	c := Claims{RegisteredClaims: jwt.RegisteredClaims{
		Issuer:    string(s.cfg.Service),
		Subject:   string(s.cfg.Service),
		Audience:  jwt.Audience{audience},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(s.cfg.TTL)),
		ID:        ids.New().String(),
	}, OriginalRequestID: d.OriginalRequestID}
	if d.HasActor() {
		c.ActorDomain, c.ActorID = string(d.ActorDomain), d.ActorID.String()
	}
	if !d.OrganizationID.IsZero() {
		c.OrganizationID = d.OrganizationID.String()
	}
	return jwt.Sign(s.cfg.Key, TokenType, c)
}

// Registry is the trusted service registry: service -> kid -> public key.
type Registry struct{ keys *jwt.StaticKeys }

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{keys: jwt.NewStaticKeys()} }

// Add trusts pub (kid) for service.
func (r *Registry) Add(service platform.Service, kid string, pub ed25519.PublicKey) error {
	if !platform.KnownService(string(service)) {
		return fmt.Errorf("serviceauth: unknown service %q", service)
	}
	if kid == "" || len(pub) != ed25519.PublicKeySize {
		return errors.New("serviceauth: invalid key")
	}
	r.keys.Add(string(service), kid, pub)
	return nil
}

// ResolveKey implements jwt.KeyResolver.
func (r *Registry) ResolveKey(ctx context.Context, issuer, kid string) (ed25519.PublicKey, error) {
	k, err := r.keys.ResolveKey(ctx, issuer, kid)
	if errors.Is(err, jwt.ErrInvalidIssuer) {
		return nil, ErrUnknownService
	}
	return k, err
}

// RegistrySpec is the JSON configuration of trusted peers:
//
//	[{"service":"api-gateway","keys":[{"kid":"gw-2026-10","public_key":"<base64url>"}]}]
type RegistrySpec struct {
	Service platform.Service `json:"service"`
	Keys    []struct {
		KID       string `json:"kid"`
		PublicKey string `json:"public_key"`
	} `json:"keys"`
}

// ParseRegistry decodes the JSON registry.
func ParseRegistry(data []byte) (*Registry, error) {
	var specs []RegistrySpec
	if err := json.Unmarshal(data, &specs); err != nil {
		return nil, fmt.Errorf("serviceauth: registry JSON invalid: %w", err)
	}
	r := NewRegistry()
	for _, s := range specs {
		if len(s.Keys) == 0 {
			return nil, fmt.Errorf("serviceauth: service %q has no keys", s.Service)
		}
		for _, k := range s.Keys {
			pub, err := security.ParseEd25519PublicKey(k.PublicKey)
			if err != nil {
				return nil, fmt.Errorf("serviceauth: invalid key for %q", s.Service)
			}
			if err := r.Add(s.Service, k.KID, pub); err != nil {
				return nil, err
			}
		}
	}
	return r, nil
}

// VerifierConfig configures a Verifier.
type VerifierConfig struct {
	Self     platform.Service
	Registry *Registry
	Leeway   time.Duration
	Now      func() time.Time
}

// Verifier authenticates inbound service tokens.
type Verifier struct{ cfg VerifierConfig }

// NewVerifier validates configuration.
func NewVerifier(cfg VerifierConfig) (*Verifier, error) {
	if !platform.KnownService(string(cfg.Self)) {
		return nil, fmt.Errorf("serviceauth: unknown self %q", cfg.Self)
	}
	if cfg.Registry == nil {
		return nil, errors.New("serviceauth: registry required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Verifier{cfg: cfg}, nil
}

// Principal is an authenticated calling service.
type Principal struct {
	Service    platform.Service
	TokenID    string
	ExpiresAt  time.Time
	Delegation Delegation
}

// Verify authenticates token.
func (v *Verifier) Verify(ctx context.Context, token string) (Principal, error) {
	var c Claims
	if _, err := jwt.Verify(ctx, token, TokenType, v.cfg.Registry, &c); err != nil {
		return Principal{}, err
	}
	if err := c.Validate(jwt.ValidationOptions{Now: v.cfg.Now, Leeway: v.cfg.Leeway, MaxLifetime: MaxTTL}); err != nil {
		return Principal{}, err
	}
	if len(c.Audience) != 1 || c.Audience[0] != string(v.cfg.Self) {
		return Principal{}, ErrWrongAudience
	}
	if c.Subject != "" && c.Subject != c.Issuer {
		return Principal{}, jwt.ErrMalformed
	}
	p := Principal{Service: platform.Service(c.Issuer), TokenID: c.ID, ExpiresAt: c.ExpiresAt.Time()}
	p.Delegation.OriginalRequestID = c.OriginalRequestID
	if (c.ActorDomain == "") != (c.ActorID == "") {
		return Principal{}, ErrBadDelegation
	}
	if c.ActorDomain != "" {
		if !platform.KnownDomain(c.ActorDomain) {
			return Principal{}, ErrBadDelegation
		}
		id, err := ids.ParseV7(c.ActorID)
		if err != nil {
			return Principal{}, ErrBadDelegation
		}
		p.Delegation.ActorDomain, p.Delegation.ActorID = platform.Domain(c.ActorDomain), id
	}
	if c.OrganizationID != "" {
		org, err := ids.ParseV7(c.OrganizationID)
		if err != nil {
			return Principal{}, ErrBadDelegation
		}
		p.Delegation.OrganizationID = org
	}
	return p, nil
}

// Capability names an internal operation, e.g. "verification.create".
type Capability string

// Policy is an explicit allow-list of callers per capability.
type Policy struct {
	allow map[Capability]map[platform.Service]bool
}

// NewPolicy returns an empty (deny-all) policy.
func NewPolicy() *Policy { return &Policy{allow: map[Capability]map[platform.Service]bool{}} }

// Allow grants capability to services.
func (p *Policy) Allow(c Capability, services ...platform.Service) *Policy {
	if p.allow[c] == nil {
		p.allow[c] = map[platform.Service]bool{}
	}
	for _, s := range services {
		if !platform.KnownService(string(s)) {
			panic(fmt.Sprintf("serviceauth: unknown service %q in policy", s))
		}
		p.allow[c][s] = true
	}
	return p
}

// Allowed reports whether service holds capability.
func (p *Policy) Allowed(c Capability, s platform.Service) bool { return p.allow[c][s] }

// Callers lists the services granted c.
func (p *Policy) Callers(c Capability) []platform.Service {
	var out []platform.Service
	for s := range p.allow[c] {
		out = append(out, s)
	}
	return out
}
