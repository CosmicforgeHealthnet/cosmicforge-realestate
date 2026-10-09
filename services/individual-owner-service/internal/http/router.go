// Package http exposes individual-owner-service over HTTP:
//
//	/api/v1/owner/*        owner-facing API, reachable only through the API
//	                       gateway (gateway service JWT in X-Service-Authorization);
//	                       protected routes additionally require this service's
//	                       own access token, re-verified here against the live
//	                       session and bound to the gateway-asserted actor
//	/internal/v1/owner/*   service-to-service (service JWT + capability policy)
//	/.well-known/jwks.json public keys of owner access tokens (gateway trust)
//	/health /ready /version
package http

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

const maxCommandBody = 32 << 10

// Config configures the router.
type Config struct {
	Service     *usecases.Service
	Idempotency *idempotency.Store
	Verifier    *serviceauth.Verifier
	Health      *health.Handler
	Logger      *slog.Logger
	JWKS        jwt.JWKS
}

// Handler holds HTTP dependencies.
type Handler struct {
	svc  *usecases.Service
	idem *idempotency.Store
	jwks jwt.JWKS
}

// NewRouter builds the HTTP handler.
func NewRouter(cfg Config) http.Handler {
	h := &Handler{svc: cfg.Service, idem: cfg.Idempotency, jwks: cfg.JWKS}
	r := chi.NewRouter()
	r.Use(observability.HTTPMiddleware("individual-owner-service"), requestcontext.Middleware, httpx.WithLogger(cfg.Logger),
		httpx.Recoverer, httpx.AccessLog, security.Headers(security.HeadersConfig{}))
	r.NotFound(httpx.NotFoundHandler().ServeHTTP)
	r.MethodNotAllowed(httpx.MethodNotAllowedHandler().ServeHTTP)
	cfg.Health.Register(r)
	r.Get("/.well-known/jwks.json", h.getJWKS)

	r.Route("/api/v1/owner", func(r chi.Router) {
		r.Use(serviceauth.Authenticate(cfg.Verifier, serviceauth.HeaderServiceAuthorization),
			serviceauth.RequireCaller(platform.APIGateway), httpx.MaxBody(maxCommandBody))
		// Credential-presenting routes (no access token).
		r.Post("/auth/register", h.register)
		r.Post("/auth/login", h.login)
		r.Post("/auth/otp/request", h.requestOTP)
		r.Post("/auth/otp/verify", h.verifyOTP)
		r.Post("/auth/mfa/send", h.sendMFA)
		r.Post("/auth/mfa/verify", h.verifyMFA)
		r.Post("/auth/refresh", h.refresh)
		r.Post("/auth/password/forgot", h.forgotPassword)
		r.Post("/auth/password/reset", h.resetPassword)
		r.Get("/auth/oauth/providers", h.oauthProviders)
		r.Post("/auth/oauth/{provider}/start", h.oauthUnavailable)
		r.Post("/auth/oauth/{provider}/callback", h.oauthUnavailable)

		r.Group(func(r chi.Router) {
			r.Use(h.ownerAuth)
			r.Post("/auth/logout", h.logout)
			r.Post("/auth/logout-all", h.logoutAll)
			r.Post("/auth/step-up", h.stepUp)
			r.Post("/auth/step-up/challenge", h.stepUpChallenge)
			r.Get("/me", h.getMe)
			r.Patch("/me", h.patchMe)
			r.Post("/me/email/verification", h.startEmailVerification)
			r.Post("/me/email/verification/confirm", h.confirmEmail)
			r.Post("/me/email/change", h.startEmailChange)
			r.Post("/me/phone/verification", h.startPhoneVerification)
			r.Post("/me/phone/verification/confirm", h.confirmPhone)
			r.Post("/me/password", h.changePassword)
			r.Get("/me/sessions", h.listSessions)
			r.Delete("/me/sessions/{session_id}", h.revokeSession)
			r.Get("/me/devices", h.listDevices)
			r.Post("/me/devices/{device_id}/trust", h.trustDevice)
			r.Delete("/me/devices/{device_id}", h.revokeDevice)
			r.Get("/me/security-events", h.listSecurityEvents)
			r.Get("/me/mfa", h.getMFA)
			r.Post("/me/mfa/totp/setup", h.setupTOTP)
			r.Post("/me/mfa/totp/confirm", h.confirmTOTP)
			r.Delete("/me/mfa/totp", h.disableTOTP)
			r.Post("/me/mfa/recovery-codes", h.regenerateRecoveryCodes)
			r.Post("/me/closure", h.requestClosure)
			r.Delete("/me/closure", h.cancelClosure)
			// Sections 6–13 domain API (route registry).
			h.mountRoutes(r, Owner)
		})
	})

	r.Route("/internal/v1/owner", func(r chi.Router) {
		r.Use(serviceauth.Authenticate(cfg.Verifier, serviceauth.HeaderAuthorization), httpx.MaxBody(maxCommandBody))
		r.Get("/accounts/{account_id}", h.adminGetAccount)
		r.Post("/accounts/{account_id}/restrict", h.adminTransition(authz.ActAdminRestrict, "owner.admin.restrict"))
		r.Post("/accounts/{account_id}/suspend", h.adminTransition(authz.ActAdminSuspend, "owner.admin.suspend"))
		r.Post("/accounts/{account_id}/reinstate", h.adminTransition(authz.ActAdminReinstate, "owner.admin.reinstate"))
		r.Post("/accounts/{account_id}/sessions/revoke", h.adminRevokeSessions)
		h.mountRoutes(r, Service)
	})
	return r
}

func (h *Handler) getJWKS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	httpx.WriteJSON(w, http.StatusOK, h.jwks)
}

type subjectKey struct{}

// ownerAuth requires an owner access token. The gateway already verified it
// at the edge; this service re-verifies its own token, binds it to the
// gateway-asserted actor (a forged or mismatched actor is rejected) and
// checks the live session and account state so revocation is immediate.
func (h *Handler) ownerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("Authorization")
		if raw == "" {
			httpx.WriteError(w, r, apperrors.AuthenticationRequired())
			return
		}
		const prefix = "bearer "
		if len(raw) <= len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
			httpx.WriteError(w, r, apperrors.InvalidToken())
			return
		}
		asserted, _ := requestcontext.ActorFrom(r.Context())
		if asserted.Domain != string(platform.DomainIndividualOwner) {
			httpx.WriteError(w, r, apperrors.InvalidToken())
			return
		}
		subj, err := h.svc.AuthenticateAccess(r.Context(), strings.TrimSpace(raw[len(prefix):]), asserted.ID)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ctx := context.WithValue(r.Context(), subjectKey{}, subj)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func ownerSubject(r *http.Request) authz.Subject {
	s, _ := r.Context().Value(subjectKey{}).(authz.Subject)
	return s
}

// serviceSubject builds the subject of an internal call from the verified
// service principal (never from headers).
func serviceSubject(r *http.Request) authz.Subject {
	p, _ := serviceauth.PrincipalFrom(r.Context())
	return authz.Subject{Kind: authz.KindService, Service: p.Service, ActorDomain: p.Delegation.ActorDomain, ActorID: p.Delegation.ActorID}
}

// clientInfo extracts informational client metadata. X-Forwarded-For is set
// by the gateway (client-supplied values are stripped at the edge).
func clientInfo(r *http.Request) usecases.ClientInfo {
	ci := usecases.ClientInfo{UserAgent: r.UserAgent(), DeviceName: headerValue(r, "X-Device-Name", 120),
		Platform: strings.ToUpper(headerValue(r, "X-Device-Platform", 16))}
	if d := r.Header.Get("X-Device-ID"); len(d) >= 8 && len(d) <= 128 && requestcontext.ValidIdempotencyKey(d) {
		ci.DeviceKey = d
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip, err := netip.ParseAddr(strings.TrimSpace(strings.Split(xff, ",")[0])); err == nil {
			ci.IP = &ip
		}
	}
	return ci
}

func headerValue(r *http.Request, name string, max int) string {
	v := strings.TrimSpace(r.Header.Get(name))
	if len(v) > max {
		v = v[:max]
	}
	return v
}

func pathID(r *http.Request, param, code, msg string) (ids.ID, error) {
	id, err := ids.ParseV7(chi.URLParam(r, param))
	if err != nil {
		return ids.Nil, apperrors.NotFound(code, msg)
	}
	return id, nil
}

// decode strictly decodes an optional-or-required JSON body.
func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.ContentLength == 0 && r.Header.Get("Content-Type") == "" {
		return httpx.DecodeJSONBytes([]byte("{}"), dst)
	}
	return httpx.DecodeJSON(w, r, dst, maxCommandBody)
}

func write(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	httpx.WriteData(w, status, v)
}
