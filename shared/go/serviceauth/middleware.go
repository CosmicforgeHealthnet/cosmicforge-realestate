package serviceauth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// Header names carrying service tokens.
const (
	// HeaderAuthorization carries the service token on /internal/v1 calls.
	HeaderAuthorization = "Authorization"
	// HeaderServiceAuthorization carries the gateway's service token on
	// gateway-forwarded /api/v1 and /webhooks/v1 requests, leaving
	// Authorization for the end user's token.
	HeaderServiceAuthorization = "X-Service-Authorization"
)

type principalKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the authenticated calling service.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

func bearer(h string) (string, bool) {
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}

// Authenticate verifies the service token in header and populates the
// principal plus the canonical request context (calling_service and, when
// delegated, actor and organization).
func Authenticate(v *Verifier, header string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get(header)
			if raw == "" {
				httpx.WriteError(w, r, apperrors.AuthenticationRequired())
				return
			}
			tok, ok := bearer(raw)
			if !ok {
				httpx.WriteError(w, r, apperrors.InvalidToken())
				return
			}
			p, err := v.Verify(r.Context(), tok)
			if err != nil {
				claimed := ""
				if h, herr := jwt.UnverifiedHeader(tok); herr == nil {
					claimed = h.Kid
				}
				logging.FromContext(r.Context()).WarnContext(r.Context(), "service token rejected",
					"reason", reason(err), "kid", claimed, "path", r.URL.Path)
				httpx.WriteError(w, r, apperrors.InvalidToken())
				return
			}
			ctx := WithPrincipal(r.Context(), p)
			ctx = requestcontext.WithCallingService(ctx, string(p.Service))
			if p.Delegation.HasActor() {
				ctx = requestcontext.WithActor(ctx, requestcontext.Actor{Domain: string(p.Delegation.ActorDomain), ID: p.Delegation.ActorID})
			}
			if !p.Delegation.OrganizationID.IsZero() {
				ctx = requestcontext.WithOrganization(ctx, p.Delegation.OrganizationID)
			}
			logging.FromContext(ctx).DebugContext(ctx, "service authenticated", "token_id", p.TokenID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func reason(err error) string {
	switch {
	case errors.Is(err, jwt.ErrExpired):
		return "expired"
	case errors.Is(err, jwt.ErrInvalidSignature):
		return "invalid_signature"
	case errors.Is(err, ErrWrongAudience):
		return "wrong_audience"
	case errors.Is(err, ErrUnknownService):
		return "unknown_service"
	case errors.Is(err, jwt.ErrUnknownKey):
		return "unknown_key"
	case errors.Is(err, jwt.ErrUnexpectedType):
		return "wrong_token_type"
	case errors.Is(err, jwt.ErrLifetimeTooLong):
		return "lifetime_too_long"
	}
	return "malformed"
}

// Require authorizes the authenticated caller for capability c.
func Require(p *Policy, c Capability) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := Check(r.Context(), p, c); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Check authorizes the caller in ctx for capability c (for use inside handlers).
func Check(ctx context.Context, p *Policy, c Capability) error {
	pr, ok := PrincipalFrom(ctx)
	if !ok {
		return apperrors.AuthenticationRequired()
	}
	if !p.Allowed(c, pr.Service) {
		logging.FromContext(ctx).WarnContext(ctx, "service caller denied", "capability", string(c), "caller", string(pr.Service))
		return apperrors.PermissionDenied("The calling service is not authorized for this operation.").WithCause(ErrNotAuthorized)
	}
	return nil
}

// RequireCaller allows only the listed services (e.g. only the gateway may
// call public routes).
func RequireCaller(services ...platform.Service) func(http.Handler) http.Handler {
	pol := NewPolicy().Allow("caller", services...)
	return Require(pol, "caller")
}
