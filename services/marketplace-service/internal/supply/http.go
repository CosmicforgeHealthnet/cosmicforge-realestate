package supply

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// CapSupply is the internal capability for the Business supply contract.
const CapSupply serviceauth.Capability = "marketplace.business_supply"

// Policy allows only Business Operations to drive the supply contract.
func Policy() *serviceauth.Policy {
	return serviceauth.NewPolicy().Allow(CapSupply, platform.BusinessOperationsService)
}

const maxBody = 512 << 10

// RouterConfig configures the HTTP surface.
type RouterConfig struct {
	Service  *Service
	Verifier *serviceauth.Verifier
	Health   *health.Handler
	Logger   *slog.Logger
}

// NewRouter builds the marketplace-service HTTP handler.
func NewRouter(cfg RouterConfig) http.Handler {
	r := chi.NewRouter()
	r.Use(observability.HTTPMiddleware("marketplace-service"), requestcontext.Middleware, httpx.WithLogger(cfg.Logger),
		httpx.Recoverer, httpx.AccessLog, security.Headers(security.HeadersConfig{}))
	r.NotFound(httpx.NotFoundHandler().ServeHTTP)
	r.MethodNotAllowed(httpx.MethodNotAllowedHandler().ServeHTTP)
	cfg.Health.Register(r)
	Mount(r, cfg.Service, cfg.Verifier)
	return r
}

// Mount adds the business-supply.v1 contract routes (their own signed
// Business-only authentication) to r.
func Mount(r chi.Router, svc *Service, verifier *serviceauth.Verifier) {
	h := &handler{svc: svc}
	r.Route("/internal/v1/business-supply", func(r chi.Router) {
		r.Use(serviceauth.Authenticate(verifier, serviceauth.HeaderAuthorization), serviceauth.Require(Policy(), CapSupply), httpx.MaxBody(maxBody))
		r.Post("/connections/validate", h.validate)
		r.Post("/commands", h.apply)
		r.Get("/commands/{key}", h.readback)
		r.Get("/connections/{id}/events", h.poll)
		r.Post("/connections/{id}/mapping-inspection", h.inspect)
		r.Get("/connections/{id}/listing", h.listing)
	})
}

type handler struct{ svc *Service }

// organisation is the signed delegated organisation; a body value is never
// trusted on its own.
func organisation(r *http.Request) (ids.ID, error) {
	p, ok := serviceauth.PrincipalFrom(r.Context())
	if !ok || p.Delegation.OrganizationID.IsZero() {
		return ids.Nil, apperrors.PermissionDenied("A signed organisation delegation is required.")
	}
	return p.Delegation.OrganizationID, nil
}

func connectionParam(r *http.Request) (ids.ID, error) {
	id, e := ids.ParseV7(chi.URLParam(r, "id"))
	if e != nil {
		return ids.Nil, apperrors.InvalidRequest("A canonical connection identifier is required.")
	}
	return id, nil
}

func (h *handler) validate(w http.ResponseWriter, r *http.Request) {
	org, e := organisation(r)
	var in Connection
	if e == nil {
		e = httpx.DecodeJSON(w, r, &in, maxBody)
	}
	if e != nil {
		httpx.WriteError(w, r, e)
		return
	}
	out, e := h.svc.Validate(r.Context(), org, in)
	if e != nil {
		httpx.WriteError(w, r, e)
		return
	}
	httpx.WriteData(w, http.StatusOK, out)
}

func (h *handler) apply(w http.ResponseWriter, r *http.Request) {
	org, e := organisation(r)
	var in Command
	if e == nil {
		e = httpx.DecodeJSON(w, r, &in, maxBody)
	}
	if e == nil && r.Header.Get("Idempotency-Key") != in.IdempotencyKey {
		e = apperrors.InvalidRequest("The Idempotency-Key header must equal the command idempotency key.")
	}
	if e != nil {
		httpx.WriteError(w, r, e)
		return
	}
	out, e := h.svc.Apply(r.Context(), org, in)
	if e != nil {
		httpx.WriteError(w, r, e)
		return
	}
	httpx.WriteData(w, http.StatusOK, out)
}

func (h *handler) readback(w http.ResponseWriter, r *http.Request) {
	org, e := organisation(r)
	var conn ids.ID
	if e == nil {
		conn, e = ids.ParseV7(r.URL.Query().Get("connection_id"))
		if e != nil {
			e = apperrors.InvalidRequest("connection_id is required.")
		}
	}
	if e != nil {
		httpx.WriteError(w, r, e)
		return
	}
	out, e := h.svc.Readback(r.Context(), org, conn, chi.URLParam(r, "key"))
	if e != nil {
		httpx.WriteError(w, r, e)
		return
	}
	httpx.WriteData(w, http.StatusOK, out)
}

func (h *handler) poll(w http.ResponseWriter, r *http.Request) {
	org, e := organisation(r)
	var conn ids.ID
	if e == nil {
		conn, e = connectionParam(r)
	}
	if e != nil {
		httpx.WriteError(w, r, e)
		return
	}
	out, e := h.svc.Poll(r.Context(), org, conn, r.URL.Query().Get("cursor"))
	if e != nil {
		httpx.WriteError(w, r, e)
		return
	}
	httpx.WriteData(w, http.StatusOK, out)
}

func (h *handler) inspect(w http.ResponseWriter, r *http.Request) {
	org, e := organisation(r)
	var conn ids.ID
	var in MappingReference
	if e == nil {
		conn, e = connectionParam(r)
	}
	if e == nil {
		e = httpx.DecodeJSON(w, r, &in, maxBody)
	}
	if e != nil {
		httpx.WriteError(w, r, e)
		return
	}
	out, e := h.svc.InspectObject(r.Context(), org, conn, in)
	if e != nil {
		httpx.WriteError(w, r, e)
		return
	}
	httpx.WriteData(w, http.StatusOK, out)
}

func (h *handler) listing(w http.ResponseWriter, r *http.Request) {
	org, e := organisation(r)
	var conn ids.ID
	if e == nil {
		conn, e = connectionParam(r)
	}
	if e != nil {
		httpx.WriteError(w, r, e)
		return
	}
	out, e := h.svc.Listing(r.Context(), org, conn)
	if e != nil {
		httpx.WriteError(w, r, e)
		return
	}
	httpx.WriteData(w, http.StatusOK, out)
}
