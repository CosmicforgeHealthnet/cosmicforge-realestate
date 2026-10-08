// Package http exposes verification-service over HTTP:
//
//	/internal/v1/*   service-to-service (service JWT in Authorization + capability policy)
//	/api/v1/verification/*   end-user reads, only via the API gateway (gateway JWT in X-Service-Authorization)
//	/webhooks/v1/{provider}/callbacks   provider callbacks via the gateway, authenticated by the provider signature
//	/health /ready /version
package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/usecases"
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
	CapCreate  serviceauth.Capability = "verification.create"
	CapRead    serviceauth.Capability = "verification.read"
	CapProcess serviceauth.Capability = "verification.process"
	CapReview  serviceauth.Capability = "verification.review"
	CapExpire  serviceauth.Capability = "verification.expire"
)

var domainServices = []platform.Service{
	platform.MarketplaceService, platform.IndividualOwnerService, platform.BusinessOperationsService,
	platform.PropertyManagerService, platform.ArtisanService, platform.ProfessionalServicesService, platform.AdminService,
}

// Policy is the internal caller allow-list. Fine-grained rules (subject
// domain, verification type, requester) are enforced by the use cases.
func Policy() *serviceauth.Policy {
	return serviceauth.NewPolicy().
		Allow(CapCreate, domainServices...).
		Allow(CapRead, domainServices...).
		Allow(CapProcess, domainServices...).
		Allow(CapReview, platform.AdminService).
		Allow(CapExpire, platform.AdminService)
}

const maxCommandBody = 64 << 10

// Handler holds HTTP dependencies.
type Handler struct {
	svc    *usecases.Service
	idem   *idempotency.Store
	policy *serviceauth.Policy
}

// Config configures the router.
type Config struct {
	Service     *usecases.Service
	Idempotency *idempotency.Store
	Verifier    *serviceauth.Verifier
	Health      *health.Handler
	Logger      *slog.Logger
}

// NewRouter builds the HTTP handler.
func NewRouter(cfg Config) http.Handler {
	h := &Handler{svc: cfg.Service, idem: cfg.Idempotency, policy: Policy()}
	r := chi.NewRouter()
	r.Use(observability.HTTPMiddleware("verification-service"), requestcontext.Middleware, httpx.WithLogger(cfg.Logger),
		httpx.Recoverer, httpx.AccessLog, security.Headers(security.HeadersConfig{}))
	r.NotFound(httpx.NotFoundHandler().ServeHTTP)
	r.MethodNotAllowed(httpx.MethodNotAllowedHandler().ServeHTTP)
	cfg.Health.Register(r)

	r.Route("/internal/v1", func(r chi.Router) {
		r.Use(serviceauth.Authenticate(cfg.Verifier, serviceauth.HeaderAuthorization))
		r.With(serviceauth.Require(h.policy, CapCreate)).Post("/verifications", h.create)
		r.With(serviceauth.Require(h.policy, CapRead)).Get("/verifications", h.listBySubject)
		r.With(serviceauth.Require(h.policy, CapRead)).Get("/verifications/{verification_id}", h.get)
		r.With(serviceauth.Require(h.policy, CapRead)).Get("/verifications/{verification_id}/result", h.result)
		r.With(serviceauth.Require(h.policy, CapCreate)).Post("/verifications/{verification_id}/evidence", h.attachEvidence)
		r.With(serviceauth.Require(h.policy, CapCreate)).Post("/verifications/{verification_id}/submit", h.submit)
		r.With(serviceauth.Require(h.policy, CapProcess)).Post("/verifications/{verification_id}/process", h.process)
		r.With(serviceauth.Require(h.policy, CapCreate)).Post("/verifications/{verification_id}/reverify", h.reverify)
		r.With(serviceauth.Require(h.policy, CapExpire)).Post("/verifications/{verification_id}/expire", h.expire)
		r.With(serviceauth.Require(h.policy, CapReview)).Get("/manual-reviews", h.listReviews)
		r.With(serviceauth.Require(h.policy, CapReview)).Post("/verifications/{verification_id}/manual-review/assign", h.assignReview)
		r.With(serviceauth.Require(h.policy, CapReview)).Post("/verifications/{verification_id}/manual-review/decision", h.decideReview)
	})

	r.Route("/api/v1/verification", func(r chi.Router) {
		r.Use(serviceauth.Authenticate(cfg.Verifier, serviceauth.HeaderServiceAuthorization),
			serviceauth.RequireCaller(platform.APIGateway), requireActor)
		r.Get("/cases", h.listMine)
		r.Get("/cases/{verification_id}", h.getMine)
	})

	r.Route("/webhooks/v1", func(r chi.Router) {
		r.Use(serviceauth.Authenticate(cfg.Verifier, serviceauth.HeaderServiceAuthorization),
			serviceauth.RequireCaller(platform.APIGateway), httpx.MaxBody(256<<10))
		r.Post("/{provider}/callbacks", h.callback)
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

func caller(r *http.Request) platform.Service {
	p, _ := serviceauth.PrincipalFrom(r.Context())
	return p.Service
}

func pathID(r *http.Request) (ids.ID, error) {
	id, err := ids.ParseV7(chi.URLParam(r, "verification_id"))
	if err != nil {
		return ids.Nil, domain.ErrNotFound()
	}
	return id, nil
}

// command reads and strictly decodes the body, then runs pre (read-only
// checks, possibly calling other services) and fn (the transaction)
// idempotently.
func (h *Handler) command(w http.ResponseWriter, r *http.Request, scope string, dst any,
	pre func(ctx context.Context) error, fn func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error)) {
	var body []byte
	var err error
	if dst == nil {
		// Body-less command: any supplied body must still be a JSON object.
		body, err = readRaw(w, r)
		if err == nil && len(body) > 0 {
			err = httpx.DecodeJSONBytes(body, &struct{}{})
		}
	} else if r.ContentLength == 0 && r.Header.Get("Content-Type") == "" {
		body = []byte("{}") // no body: decode as an empty object (fields optional or validated)
	} else {
		body, err = httpx.ReadBody(w, r, maxCommandBody)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if dst != nil {
		if err := httpx.DecodeJSONBytes(body, dst); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	// pre runs after the Idempotency-Key is claimed, outside the transaction.
	h.idem.ServeCommand(w, r, scope, body, pre, fn)
}

func ok(status int, v any, resourceID ids.ID) (idempotency.Result, error) {
	return idempotency.Result{Status: status, Body: httpx.DataEnvelope{Data: v}, ResourceType: "verification", ResourceID: resourceID.String()}, nil
}

type subjectBody struct {
	SubjectType   string `json:"subject_type"`
	SubjectDomain string `json:"subject_domain"`
	SubjectID     ids.ID `json:"subject_id"`
}

type createBody struct {
	VerificationType string            `json:"verification_type"`
	Subject          subjectBody       `json:"subject"`
	Provider         string            `json:"provider"`
	CallerReference  string            `json:"caller_reference"`
	ProviderMetadata map[string]string `json:"provider_metadata"`
}

func (b createBody) input() (usecases.CreateInput, error) {
	if b.Subject.SubjectID.ValidateV7() != nil || !platform.KnownDomain(b.Subject.SubjectDomain) || b.Subject.SubjectType == "" {
		return usecases.CreateInput{}, apperrors.InvalidRequest("subject must have subject_type, a known subject_domain and a UUIDv7 subject_id.",
			apperrors.Detail{Field: "subject", Code: "INVALID", Message: "invalid subject reference"})
	}
	if len(b.CallerReference) > 128 {
		return usecases.CreateInput{}, apperrors.InvalidRequest("caller_reference must be at most 128 characters.")
	}
	return usecases.CreateInput{
		Type:     domain.Type(b.VerificationType),
		Subject:  domain.Subject{Type: domain.SubjectType(b.Subject.SubjectType), Domain: platform.Domain(b.Subject.SubjectDomain), ID: b.Subject.SubjectID},
		Provider: b.Provider, CallerReference: b.CallerReference, ProviderMetadata: b.ProviderMetadata,
	}, nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var b createBody
	var in usecases.CreateInput
	h.command(w, r, "verification.create", &b, func(ctx context.Context) error {
		var err error
		if in, err = b.input(); err != nil {
			return err
		}
		return h.svc.ValidateCreate(caller(r), in)
	}, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		v, err := h.svc.CreateTx(ctx, tx, caller(r), in, nil)
		if err != nil {
			return idempotency.Result{}, err
		}
		return ok(http.StatusCreated, v, v.VerificationID)
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		var v usecases.CaseView
		if v, err = h.svc.Get(r.Context(), caller(r), id); err == nil {
			httpx.WriteData(w, http.StatusOK, v)
			return
		}
	}
	httpx.WriteError(w, r, err)
}

func (h *Handler) result(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		var v usecases.ResultView
		if v, err = h.svc.Result(r.Context(), caller(r), id); err == nil {
			httpx.WriteData(w, http.StatusOK, v)
			return
		}
	}
	httpx.WriteError(w, r, err)
}

func (h *Handler) listBySubject(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sid, err := ids.ParseV7(q.Get("subject_id"))
	if err != nil || !platform.KnownDomain(q.Get("subject_domain")) || q.Get("subject_type") == "" {
		httpx.WriteError(w, r, apperrors.InvalidRequest("subject_type, subject_domain and subject_id query parameters are required."))
		return
	}
	limit, err := pagination.Limit(q.Get("limit"), 20, 100)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sub := domain.Subject{Type: domain.SubjectType(q.Get("subject_type")), Domain: platform.Domain(q.Get("subject_domain")), ID: sid}
	resp, err := h.svc.ListBySubject(r.Context(), caller(r), sub, q.Get("verification_type"), q.Get("cursor"), limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

type evidenceBody struct {
	MediaID      ids.ID `json:"media_id"`
	EvidenceType string `json:"evidence_type"`
}

func (h *Handler) attachEvidence(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var b evidenceBody
	var checksum string
	h.command(w, r, "verification.evidence", &b, func(ctx context.Context) error {
		if b.MediaID.ValidateV7() != nil {
			return apperrors.InvalidRequest("media_id must be a UUIDv7.")
		}
		var err error
		checksum, err = h.svc.PrecheckEvidence(ctx, caller(r), id, usecases.EvidenceInput{MediaID: b.MediaID, EvidenceType: b.EvidenceType})
		return err
	}, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		v, err := h.svc.AttachEvidenceTx(ctx, tx, caller(r), id, usecases.EvidenceInput{MediaID: b.MediaID, EvidenceType: b.EvidenceType}, checksum)
		if err != nil {
			return idempotency.Result{}, err
		}
		return ok(http.StatusCreated, v, id)
	})
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var checked int
	h.command(w, r, "verification.submit", nil, func(ctx context.Context) error {
		var err error
		checked, err = h.svc.PrecheckSubmit(ctx, caller(r), id)
		return err
	}, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		v, err := h.svc.SubmitTx(ctx, tx, caller(r), id, checked)
		if err != nil {
			return idempotency.Result{}, err
		}
		return ok(http.StatusOK, v, id)
	})
}

func (h *Handler) process(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.command(w, r, "verification.process", nil, h.readable(r, id), func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		v, err := h.svc.RequestProcessingTx(ctx, tx, caller(r), id)
		if err != nil {
			return idempotency.Result{}, err
		}
		return ok(http.StatusAccepted, v, id)
	})
}

// readable hides cases the caller may not see (404) before any command.
func (h *Handler) readable(r *http.Request, id ids.ID) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		_, err := h.svc.Get(ctx, caller(r), id)
		return err
	}
}

type reverifyBody struct {
	Provider         string            `json:"provider"`
	CallerReference  string            `json:"caller_reference"`
	ProviderMetadata map[string]string `json:"provider_metadata"`
}

func (h *Handler) reverify(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var b reverifyBody
	h.command(w, r, "verification.reverify", &b, h.readable(r, id), func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		v, err := h.svc.ReverifyTx(ctx, tx, caller(r), id, usecases.CreateInput{Provider: b.Provider, CallerReference: b.CallerReference, ProviderMetadata: b.ProviderMetadata})
		if err != nil {
			return idempotency.Result{}, err
		}
		return ok(http.StatusCreated, v, v.VerificationID)
	})
}

type reasonBody struct {
	ReasonCode string `json:"reason_code"`
}

func validReason(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func (h *Handler) expire(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var b reasonBody
	h.command(w, r, "verification.expire", &b, func(ctx context.Context) error {
		if !validReason(b.ReasonCode) {
			return apperrors.InvalidRequest("reason_code must be an uppercase identifier.")
		}
		return nil
	}, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		v, err := h.svc.ExpireTx(ctx, tx, id, b.ReasonCode)
		if err != nil {
			return idempotency.Result{}, err
		}
		return ok(http.StatusOK, v, id)
	})
}

func (h *Handler) listReviews(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	statuses := []string{"OPEN", "ASSIGNED"}
	if s := q.Get("status"); s != "" {
		if s != "OPEN" && s != "ASSIGNED" && s != "DECIDED" {
			httpx.WriteError(w, r, apperrors.InvalidRequest("status must be OPEN, ASSIGNED or DECIDED."))
			return
		}
		statuses = []string{s}
	}
	limit, err := pagination.Limit(q.Get("limit"), 20, 100)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := h.svc.ListReviews(r.Context(), statuses, q.Get("cursor"), limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) assignReview(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.command(w, r, "verification.review.assign", nil, nil, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		v, err := h.svc.AssignReviewTx(ctx, tx, id)
		if err != nil {
			return idempotency.Result{}, err
		}
		return ok(http.StatusOK, v, id)
	})
}

type decisionBody struct {
	Decision   string `json:"decision"`
	ReasonCode string `json:"reason_code"`
	Notes      string `json:"notes"`
}

func (h *Handler) decideReview(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var b decisionBody
	h.command(w, r, "verification.review.decision", &b, func(ctx context.Context) error {
		if b.ReasonCode != "" && !validReason(b.ReasonCode) {
			return apperrors.InvalidRequest("reason_code must be an uppercase identifier.")
		}
		return nil
	}, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		v, err := h.svc.DecideReviewTx(ctx, tx, id, usecases.DecisionInput{Decision: domain.Status(b.Decision), ReasonCode: b.ReasonCode, Notes: b.Notes})
		if err != nil {
			return idempotency.Result{}, err
		}
		return ok(http.StatusOK, v, id)
	})
}

func (h *Handler) listMine(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := pagination.Limit(q.Get("limit"), 20, 100)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := h.svc.ListForActor(r.Context(), q.Get("verification_type"), q.Get("cursor"), limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) getMine(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		var v usecases.PublicCaseView
		if v, err = h.svc.GetForActor(r.Context(), id); err == nil {
			httpx.WriteData(w, http.StatusOK, v)
			return
		}
	}
	httpx.WriteError(w, r, err)
}

func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	body, err := readRaw(w, r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.HandleCallback(r.Context(), chi.URLParam(r, "provider"), r.Header, body)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, res)
}

// readRaw reads a bounded raw body (provider callbacks, body-less commands).
func readRaw(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCommandBody))
	if err != nil {
		return nil, apperrors.PayloadTooLarge()
	}
	return b, nil
}
