// Package auth validates end-user access tokens at the edge and normalizes
// the actor context. The gateway is not an account authority: it only
// verifies tokens from configured trusted domain issuers.
package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway/internal/routing"
	sharedauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

type principalKey struct{}

// PrincipalFrom returns the verified user, if any.
func PrincipalFrom(ctx context.Context) (sharedauth.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(sharedauth.Principal)
	return p, ok
}

// Authenticator applies a route's AuthMode.
type Authenticator struct{ v *sharedauth.Verifier }

// New wraps a verifier.
func New(v *sharedauth.Verifier) *Authenticator { return &Authenticator{v: v} }

// ErrCrossDomain is returned when a token's domain may not use a route.
var ErrCrossDomain = errors.New("gateway: token domain not accepted on this route")

// Authenticate verifies the bearer token (if required/present) and returns
// the context with the actor populated. It never trusts client-supplied
// actor headers.
func (a *Authenticator) Authenticate(r *http.Request, route routing.Route) (context.Context, error) {
	ctx := r.Context()
	if route.Auth == routing.AuthNone {
		return ctx, nil
	}
	h := r.Header.Get("Authorization")
	if h == "" {
		if route.Auth == routing.AuthRequired {
			return nil, apperrors.AuthenticationRequired()
		}
		return ctx, nil
	}
	const prefix = "bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return nil, apperrors.InvalidToken()
	}
	p, err := a.v.Verify(ctx, strings.TrimSpace(h[len(prefix):]))
	if err != nil {
		logging.FromContext(ctx).InfoContext(ctx, "user token rejected", "reason", reason(err), "route", route.Name)
		return nil, apperrors.InvalidToken().WithCause(err)
	}
	if !route.AllowsDomain(p.Domain) {
		logging.FromContext(ctx).WarnContext(ctx, "cross-domain token rejected", "token_domain", string(p.Domain), "route", route.Name)
		return nil, apperrors.PermissionDenied("This access token is not valid for this product.").WithCause(ErrCrossDomain)
	}
	ctx = context.WithValue(ctx, principalKey{}, p)
	ctx = requestcontext.WithActor(ctx, requestcontext.Actor{Domain: string(p.Domain), ID: p.ActorID})
	if !p.OrganizationID.IsZero() {
		ctx = requestcontext.WithOrganization(ctx, p.OrganizationID)
	}
	return ctx, nil
}

func reason(err error) string {
	switch {
	case errors.Is(err, sharedauth.ErrUntrustedIssuer):
		return "untrusted_issuer"
	case errors.Is(err, jwt.ErrExpired):
		return "expired"
	case errors.Is(err, jwt.ErrInvalidSignature):
		return "invalid_signature"
	case errors.Is(err, jwt.ErrInvalidAudience):
		return "wrong_audience"
	case errors.Is(err, jwt.ErrUnknownKey):
		return "unknown_kid"
	case errors.Is(err, jwt.ErrUnexpectedType):
		return "wrong_token_type"
	}
	return "malformed"
}
