// Package http exposes media-document-service:
//
//	/internal/v1/media*        service-to-service (service JWT + capability policy)
//	/api/v1/media*             end users via the API gateway (gateway JWT + actor)
//	/health /ready /version
package http

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/usecases"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// Capabilities.
const (
	CapWrite serviceauth.Capability = "media.write"
	CapRead  serviceauth.Capability = "media.read"
)

var domainServices = []platform.Service{
	platform.MarketplaceService, platform.IndividualOwnerService, platform.BusinessOperationsService,
	platform.PropertyManagerService, platform.ArtisanService, platform.ProfessionalServicesService, platform.AdminService,
}

// Policy is the internal caller allow-list; ownership and classification
// rules are enforced by the use cases.
func Policy() *serviceauth.Policy {
	readers := append([]platform.Service{platform.VerificationService}, domainServices...)
	return serviceauth.NewPolicy().Allow(CapWrite, domainServices...).Allow(CapRead, readers...)
}

const maxBody = 64 << 10

// Config wires the router.
type Config struct {
	Service     *usecases.Service
	Idempotency *idempotency.Store
	Verifier    *serviceauth.Verifier
	Health      *health.Handler
	Logger      *slog.Logger
}

type handler struct {
	svc  *usecases.Service
	idem *idempotency.Store
}

// NewRouter builds the HTTP handler.
func NewRouter(cfg Config) http.Handler {
	h := &handler{svc: cfg.Service, idem: cfg.Idempotency}
	pol := Policy()
	r := chi.NewRouter()
	r.Use(observability.HTTPMiddleware("media-document-service"), requestcontext.Middleware, httpx.WithLogger(cfg.Logger),
		httpx.Recoverer, httpx.AccessLog, security.Headers(security.HeadersConfig{}))
	r.NotFound(httpx.NotFoundHandler().ServeHTTP)
	r.MethodNotAllowed(httpx.MethodNotAllowedHandler().ServeHTTP)
	cfg.Health.Register(r)

	r.Route("/internal/v1/media", func(r chi.Router) {
		r.Use(serviceauth.Authenticate(cfg.Verifier, serviceauth.HeaderAuthorization))
		r.With(serviceauth.Require(pol, CapWrite)).Post("/uploads", h.initiateInternal)
		r.With(serviceauth.Require(pol, CapRead)).Get("/", h.listByOwner)
		r.With(serviceauth.Require(pol, CapRead)).Get("/{media_id}", h.get)
		r.With(serviceauth.Require(pol, CapRead)).Get("/{media_id}/status", h.status)
		r.With(serviceauth.Require(pol, CapWrite)).Post("/{media_id}/complete", h.complete)
		r.With(serviceauth.Require(pol, CapRead)).Post("/{media_id}/download-url", h.download)
		r.With(serviceauth.Require(pol, CapWrite)).Put("/{media_id}/access-policy", h.setPolicy)
		r.With(serviceauth.Require(pol, CapWrite)).Post("/{media_id}/deletion", h.deletion)
	})
	r.Route("/api/v1/media", func(r chi.Router) {
		r.Use(serviceauth.Authenticate(cfg.Verifier, serviceauth.HeaderServiceAuthorization),
			serviceauth.RequireCaller(platform.APIGateway), requireActor)
		r.Post("/uploads", h.initiatePublic)
		r.Get("/{media_id}", h.get)
		r.Post("/{media_id}/complete", h.complete)
		r.Post("/{media_id}/download-url", h.download)
	})
	return r
}

func requireActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requestcontext.ActorFrom(r.Context()); !ok {
			httpx.WriteError(w, r, apperrors.AuthenticationRequired())
			return
		}
		next.ServeHTTP(w, r)
	})
}

func mediaID(r *http.Request) (ids.ID, error) {
	id, err := ids.ParseV7(chi.URLParam(r, "media_id"))
	if err != nil {
		return ids.Nil, domain.ErrNotFound()
	}
	return id, nil
}

func (h *handler) command(w http.ResponseWriter, r *http.Request, scope string, dst any, pre func(context.Context) error,
	fn func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error)) {
	var body []byte
	var err error
	if r.ContentLength == 0 && r.Header.Get("Content-Type") == "" {
		body = []byte("{}")
	} else {
		body, err = httpx.ReadBody(w, r, maxBody)
	}
	if err == nil && dst != nil {
		err = httpx.DecodeJSONBytes(body, dst)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.idem.ServeCommand(w, r, scope, body, pre, fn)
}

func data(status int, v any, id ids.ID) (idempotency.Result, error) {
	return idempotency.Result{Status: status, Body: httpx.DataEnvelope{Data: v}, ResourceType: "media", ResourceID: id.String()}, nil
}

type initiateBody struct {
	Owner                *ownerBody `json:"owner"`
	MediaClass           string     `json:"media_class"`
	Filename             string     `json:"filename"`
	MimeType             string     `json:"mime_type"`
	SizeBytes            int64      `json:"size_bytes"`
	ChecksumSHA256       string     `json:"checksum_sha256"`
	AccessClassification string     `json:"access_classification"`
	DocumentType         string     `json:"document_type"`
	Title                string     `json:"title"`
}

type ownerBody struct {
	OwnerDomain string `json:"owner_domain"`
	OwnerType   string `json:"owner_type"`
	OwnerID     ids.ID `json:"owner_id"`
}

func (b initiateBody) input() usecases.InitiateInput {
	in := usecases.InitiateInput{Class: domain.Class(b.MediaClass), Filename: b.Filename, MimeType: b.MimeType, SizeBytes: b.SizeBytes,
		ChecksumSHA256: b.ChecksumSHA256, Classification: domain.Classification(b.AccessClassification), DocumentType: b.DocumentType, Title: b.Title}
	if b.Owner != nil {
		in.Owner = domain.Owner{Domain: platform.Domain(b.Owner.OwnerDomain), Type: b.Owner.OwnerType, ID: b.Owner.OwnerID}
	}
	return in
}

func (h *handler) initiate(w http.ResponseWriter, r *http.Request, b *initiateBody, ownerFromActor bool) {
	var in usecases.InitiateInput
	h.command(w, r, "media.initiate", b, func(ctx context.Context) error {
		in = b.input()
		if ownerFromActor {
			if b.Owner != nil {
				return apperrors.InvalidRequest("owner is derived from the signed-in user and must not be supplied.")
			}
			a, _ := requestcontext.ActorFrom(ctx)
			in.Owner = domain.Owner{Domain: platform.Domain(a.Domain), Type: "USER", ID: a.ID}
		} else if b.Owner == nil {
			return apperrors.InvalidRequest("owner is required.")
		}
		return h.svc.ValidateInitiate(usecases.PrincipalFrom(ctx), &in)
	}, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		v, err := h.svc.InitiateTx(ctx, tx, usecases.PrincipalFrom(ctx), in)
		if err != nil {
			return idempotency.Result{}, err
		}
		return data(http.StatusCreated, v, v.Media.MediaID)
	})
}

func (h *handler) initiateInternal(w http.ResponseWriter, r *http.Request) {
	h.initiate(w, r, &initiateBody{}, false)
}
func (h *handler) initiatePublic(w http.ResponseWriter, r *http.Request) {
	h.initiate(w, r, &initiateBody{}, true)
}

func (h *handler) complete(w http.ResponseWriter, r *http.Request) {
	id, err := mediaID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.command(w, r, "media.complete", &struct{}{}, func(ctx context.Context) error {
		return h.svc.PrecheckComplete(ctx, usecases.PrincipalFrom(ctx), id)
	}, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		v, err := h.svc.CompleteTx(ctx, tx, usecases.PrincipalFrom(ctx), id)
		if err != nil {
			return idempotency.Result{}, err
		}
		return data(http.StatusOK, v, id)
	})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := mediaID(r)
	if err == nil {
		var v usecases.MediaView
		if v, err = h.svc.GetMetadata(r.Context(), usecases.PrincipalFrom(r.Context()), id); err == nil {
			httpx.WriteData(w, http.StatusOK, v)
			return
		}
	}
	httpx.WriteError(w, r, err)
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	id, err := mediaID(r)
	if err == nil {
		var v usecases.StatusView
		if v, err = h.svc.GetStatus(r.Context(), usecases.PrincipalFrom(r.Context()), id); err == nil {
			httpx.WriteData(w, http.StatusOK, v)
			return
		}
	}
	httpx.WriteError(w, r, err)
}

func (h *handler) listByOwner(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	oid, err := ids.ParseV7(q.Get("owner_id"))
	if err != nil || !platform.KnownDomain(q.Get("owner_domain")) || q.Get("owner_type") == "" {
		httpx.WriteError(w, r, apperrors.InvalidRequest("owner_domain, owner_type and owner_id query parameters are required."))
		return
	}
	limit, err := pagination.Limit(q.Get("limit"), 20, 100)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := h.svc.ListByOwner(r.Context(), usecases.PrincipalFrom(r.Context()),
		domain.Owner{Domain: platform.Domain(q.Get("owner_domain")), Type: q.Get("owner_type"), ID: oid}, q.Get("cursor"), limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

type downloadBody struct {
	Purpose string `json:"purpose"`
}

func (h *handler) download(w http.ResponseWriter, r *http.Request) {
	id, err := mediaID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var b downloadBody
	if r.ContentLength != 0 || r.Header.Get("Content-Type") != "" {
		if err := httpx.DecodeJSON(w, r, &b, maxBody); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if len(b.Purpose) > 100 {
		httpx.WriteError(w, r, apperrors.InvalidRequest("purpose must be at most 100 characters."))
		return
	}
	v, err := h.svc.RequestDownload(r.Context(), usecases.PrincipalFrom(r.Context()), id, b.Purpose)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, v)
}

type policyBody struct {
	AccessClassification string                `json:"access_classification"`
	Grants               []usecases.GrantInput `json:"grants"`
}

func (h *handler) setPolicy(w http.ResponseWriter, r *http.Request) {
	id, err := mediaID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var b policyBody
	h.command(w, r, "media.access_policy", &b, nil, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		v, err := h.svc.SetPolicyTx(ctx, tx, usecases.PrincipalFrom(ctx), id, domain.Classification(b.AccessClassification), b.Grants)
		if err != nil {
			return idempotency.Result{}, err
		}
		return data(http.StatusOK, v, id)
	})
}

type deletionBody struct {
	Reason string `json:"reason"`
}

func (h *handler) deletion(w http.ResponseWriter, r *http.Request) {
	id, err := mediaID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var b deletionBody
	h.command(w, r, "media.deletion", &b, func(context.Context) error {
		if b.Reason == "" || len(b.Reason) > 64 {
			return apperrors.InvalidRequest("reason is required (at most 64 characters).")
		}
		return nil
	}, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		v, err := h.svc.RequestDeletionTx(ctx, tx, usecases.PrincipalFrom(ctx), id, b.Reason)
		if err != nil {
			return idempotency.Result{}, err
		}
		return data(http.StatusAccepted, v, id)
	})
}
