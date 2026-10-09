// Package oauth defines the provider-neutral OAuth 2.0 / OpenID Connect
// sign-in adapter contract for owner accounts (Section 5 A).
//
// No provider adapter is implemented or enabled: provider selection, client
// registration and credentials are an unresolved product/vendor decision
// (Appendix D "OAuth providers"). Until a certified adapter exists the
// public OAuth routes respond 503 OAUTH_PROVIDER_UNAVAILABLE and nothing is
// presented as working. An adapter must:
//
//   - use the authorization-code flow with PKCE (S256), state and nonce;
//   - verify the ID token signature against the provider JWKS, issuer,
//     audience, expiry and nonce before trusting any claim;
//   - link identities by (provider, subject) only — never by matching email
//     to an existing owner account without that account proving control.
package oauth

import (
	"context"
	"errors"
)

// Identity is a verified external identity.
type Identity struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
}

// AuthorizationRequest carries per-attempt secrets the caller persists
// (hashed) to validate the callback.
type AuthorizationRequest struct {
	URL          string
	State        string
	Nonce        string
	CodeVerifier string
}

// Provider is the adapter contract.
type Provider interface {
	Name() string
	Begin(ctx context.Context, redirectURI string) (AuthorizationRequest, error)
	Complete(ctx context.Context, redirectURI, code, codeVerifier, nonce string) (Identity, error)
}

// ErrProviderUnavailable is returned for unconfigured providers.
var ErrProviderUnavailable = errors.New("oauth: provider not configured")

// Registry holds configured providers (empty by default).
type Registry struct{ providers map[string]Provider }

// NewRegistry builds a registry.
func NewRegistry(ps ...Provider) *Registry {
	r := &Registry{providers: map[string]Provider{}}
	for _, p := range ps {
		r.providers[p.Name()] = p
	}
	return r
}

// Names lists configured providers.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	return out
}

// Get returns a configured provider.
func (r *Registry) Get(name string) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, ErrProviderUnavailable
	}
	return p, nil
}
