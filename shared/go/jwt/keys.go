package jwt

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// StaticKeys is an immutable issuer -> kid -> public key resolver. Multiple
// kids per issuer support zero-downtime key rotation: publish the new key,
// switch the signer, then retire the old key after the maximum token lifetime.
type StaticKeys struct {
	keys map[string]map[string]ed25519.PublicKey
}

// NewStaticKeys builds a resolver.
func NewStaticKeys() *StaticKeys { return &StaticKeys{keys: map[string]map[string]ed25519.PublicKey{}} }

// Add registers a key for issuer. It panics on malformed input because key
// sets are built at startup from validated configuration.
func (s *StaticKeys) Add(issuer, kid string, pub ed25519.PublicKey) *StaticKeys {
	if issuer == "" || kid == "" || len(pub) != ed25519.PublicKeySize {
		panic("jwt: invalid static key")
	}
	if s.keys[issuer] == nil {
		s.keys[issuer] = map[string]ed25519.PublicKey{}
	}
	s.keys[issuer][kid] = pub
	return s
}

// Issuers returns the configured issuers.
func (s *StaticKeys) Issuers() []string {
	out := make([]string, 0, len(s.keys))
	for k := range s.keys {
		out = append(out, k)
	}
	return out
}

// ResolveKey implements KeyResolver.
func (s *StaticKeys) ResolveKey(_ context.Context, issuer, kid string) (ed25519.PublicKey, error) {
	byKid, ok := s.keys[issuer]
	if !ok {
		return nil, ErrInvalidIssuer
	}
	k, ok := byKid[kid]
	if !ok {
		return nil, ErrUnknownKey
	}
	return k, nil
}

// JWK is an OKP/Ed25519 JSON Web Key (RFC 8037).
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Kid string `json:"kid"`
	Use string `json:"use,omitempty"`
	Alg string `json:"alg,omitempty"`
}

// JWKS is a JSON Web Key Set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// PublicJWK converts a public key.
func PublicJWK(kid string, pub ed25519.PublicKey) JWK {
	return JWK{Kty: "OKP", Crv: "Ed25519", X: b64.EncodeToString(pub), Kid: kid, Use: "sig", Alg: Algorithm}
}

// Key decodes the public key, rejecting anything except OKP/Ed25519.
func (k JWK) Key() (ed25519.PublicKey, error) {
	if k.Kty != "OKP" || k.Crv != "Ed25519" || k.Kid == "" {
		return nil, fmt.Errorf("jwt: unsupported JWK (kty=%q crv=%q)", k.Kty, k.Crv)
	}
	if k.Alg != "" && k.Alg != Algorithm {
		return nil, ErrUnsupportedAlg
	}
	if k.Use != "" && k.Use != "sig" {
		return nil, errors.New("jwt: JWK not for signatures")
	}
	raw, err := b64.DecodeString(k.X)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("jwt: invalid JWK x")
	}
	return ed25519.PublicKey(raw), nil
}

// ParseJWKS decodes a key set into kid -> key; unsupported keys are rejected.
func ParseJWKS(data []byte) (map[string]ed25519.PublicKey, error) {
	var set JWKS
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("jwt: invalid JWKS: %w", err)
	}
	out := make(map[string]ed25519.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		pub, err := k.Key()
		if err != nil {
			return nil, err
		}
		out[k.Kid] = pub
	}
	return out, nil
}

// RemoteJWKS resolves keys for one issuer from a JWKS URL with caching.
// Unknown kids trigger a refresh at most once per MinRefreshInterval, so an
// attacker cannot force unbounded fetches with random kids.
type RemoteJWKS struct {
	Issuer             string
	URL                string
	Client             *http.Client
	RefreshInterval    time.Duration
	MinRefreshInterval time.Duration
	Now                func() time.Time

	mu          sync.Mutex
	keys        map[string]ed25519.PublicKey
	fetchedAt   time.Time
	lastAttempt time.Time
}

func (r *RemoteJWKS) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// ResolveKey implements KeyResolver.
func (r *RemoteJWKS) ResolveKey(ctx context.Context, issuer, kid string) (ed25519.PublicKey, error) {
	if issuer != r.Issuer {
		return nil, ErrInvalidIssuer
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	refresh := r.RefreshInterval
	if refresh <= 0 {
		refresh = 10 * time.Minute
	}
	minRefresh := r.MinRefreshInterval
	if minRefresh <= 0 {
		minRefresh = 30 * time.Second
	}
	now := r.now()
	stale := r.keys == nil || now.Sub(r.fetchedAt) > refresh
	_, known := r.keys[kid]
	if (stale || !known) && now.Sub(r.lastAttempt) >= minRefresh {
		r.lastAttempt = now
		if keys, err := r.fetch(ctx); err == nil {
			r.keys, r.fetchedAt = keys, now
		} else if r.keys == nil {
			return nil, fmt.Errorf("%w: jwks unavailable", ErrUnknownKey)
		}
	}
	k, ok := r.keys[kid]
	if !ok {
		return nil, ErrUnknownKey
	}
	return k, nil
}

func (r *RemoteJWKS) fetch(ctx context.Context) (map[string]ed25519.PublicKey, error) {
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	return ParseJWKS(body)
}

// MultiResolver dispatches by issuer to per-issuer resolvers.
type MultiResolver map[string]KeyResolver

// ResolveKey implements KeyResolver.
func (m MultiResolver) ResolveKey(ctx context.Context, issuer, kid string) (ed25519.PublicKey, error) {
	r, ok := m[issuer]
	if !ok {
		return nil, ErrInvalidIssuer
	}
	return r.ResolveKey(ctx, issuer, kid)
}
