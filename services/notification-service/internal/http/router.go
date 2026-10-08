// Package http exposes notification-service:
//
//	/internal/v1/notifications*   service-to-service (service JWT + capability policy)
//	/api/v1/notifications*        end users via the API gateway (gateway JWT + actor)
//	/health /ready /version
package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/usecases"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// Capabilities.
const (
	CapSend           serviceauth.Capability = "notifications.send"
	CapRecipients     serviceauth.Capability = "notifications.recipients.manage"
	CapTemplatesRead  serviceauth.Capability = "notifications.templates.read"
	CapTemplatesWrite serviceauth.Capability = "notifications.templates.manage"
)

var domainServices = []platform.Service{
	platform.MarketplaceService, platform.IndividualOwnerService, platform.BusinessOperationsService,
	platform.PropertyManagerService, platform.ArtisanService, platform.ProfessionalServicesService, platform.AdminService,
}

// Policy is the internal caller allow-list; recipient-domain isolation is
// enforced by the use cases.
func Policy() *serviceauth.Policy {
	senders := append([]platform.Service{platform.VerificationService, platform.PaymentService}, domainServices...)
	return serviceauth.NewPolicy().
		Allow(CapSend, senders...).
		Allow(CapRecipients, domainServices...).
		Allow(CapTemplatesRead, senders...).
		Allow(CapTemplatesWrite, platform.AdminService)
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
	r.Use(observability.HTTPMiddleware("notification-service"), requestcontext.Middleware, httpx.WithLogger(cfg.Logger),
		httpx.Recoverer, httpx.AccessLog, security.Headers(security.HeadersConfig{}))
	r.NotFound(httpx.NotFoundHandler().ServeHTTP)
	r.MethodNotAllowed(httpx.MethodNotAllowedHandler().ServeHTTP)
	cfg.Health.Register(r)

	r.Route("/internal/v1/notifications", func(r chi.Router) {
		r.Use(serviceauth.Authenticate(cfg.Verifier, serviceauth.HeaderAuthorization))
		r.With(serviceauth.Require(pol, CapSend)).Post("/", h.send)
		r.With(serviceauth.Require(pol, CapSend)).Get("/{notification_id}", h.get)
		r.With(serviceauth.Require(pol, CapRecipients)).Post("/push-endpoints", h.registerInternal)
		r.With(serviceauth.Require(pol, CapRecipients)).Delete("/push-endpoints/{endpoint_id}", h.revoke)
		r.With(serviceauth.Require(pol, CapRecipients)).Get("/recipients/{recipient_domain}/{recipient_id}/push-endpoints", h.listEndpointsInternal)
		r.With(serviceauth.Require(pol, CapRecipients)).Get("/recipients/{recipient_domain}/{recipient_id}/preferences", h.getPreferencesInternal)
		r.With(serviceauth.Require(pol, CapRecipients)).Put("/recipients/{recipient_domain}/{recipient_id}/preferences", h.putPreferencesInternal)
		r.With(serviceauth.Require(pol, CapTemplatesWrite)).Post("/templates", h.createTemplate)
		r.With(serviceauth.Require(pol, CapTemplatesRead)).Get("/templates/{template_key}", h.listTemplates)
	})
	r.Route("/api/v1/notifications", func(r chi.Router) {
		r.Use(serviceauth.Authenticate(cfg.Verifier, serviceauth.HeaderServiceAuthorization),
			serviceauth.RequireCaller(platform.APIGateway), requireActor)
		r.Post("/push-endpoints", h.registerPublic)
		r.Get("/push-endpoints", h.listEndpointsPublic)
		r.Delete("/push-endpoints/{endpoint_id}", h.revoke)
		r.Get("/preferences", h.getPreferencesPublic)
		r.Put("/preferences", h.putPreferencesPublic)
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

func self(ctx context.Context) usecases.Recipient {
	a, _ := requestcontext.ActorFrom(ctx)
	return usecases.Recipient{Domain: platform.Domain(a.Domain), ID: a.ID}
}

func pathRecipient(r *http.Request) (usecases.Recipient, error) {
	id, err := ids.ParseV7(chi.URLParam(r, "recipient_id"))
	d := chi.URLParam(r, "recipient_domain")
	if err != nil || !platform.KnownDomain(d) {
		return usecases.Recipient{}, apperrors.InvalidRequest("recipient_domain must be a known domain and recipient_id a UUIDv7.")
	}
	return usecases.Recipient{Domain: platform.Domain(d), ID: id}, nil
}

func (h *handler) command(w http.ResponseWriter, r *http.Request, scope string, dst any, pre func(context.Context) error,
	fn func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error)) {
	body, err := httpx.ReadBody(w, r, maxBody)
	if err == nil {
		err = httpx.DecodeJSONBytes(body, dst)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.idem.ServeCommand(w, r, scope, body, pre, fn)
}

func result(status int, v any, typ string, id ids.ID) (idempotency.Result, error) {
	return idempotency.Result{Status: status, Body: httpx.DataEnvelope{Data: v}, ResourceType: typ, ResourceID: id.String()}, nil
}

type sendBody struct {
	Recipient       *usecases.Recipient `json:"recipient"`
	TemplateKey     string              `json:"template_key"`
	TemplateVersion int32               `json:"template_version"`
	Locale          string              `json:"locale"`
	Channels        []domain.Channel    `json:"channels"`
	Variables       map[string]string   `json:"variables"`
	Destinations    struct {
		Email string `json:"email"`
		Phone string `json:"phone"`
	} `json:"destinations"`
	Data            map[string]string `json:"data"`
	CallerReference string            `json:"caller_reference"`
}

func (h *handler) send(w http.ResponseWriter, r *http.Request) {
	var b sendBody
	var in usecases.SendInput
	h.command(w, r, "notification.send", &b, func(ctx context.Context) error {
		if b.Recipient == nil {
			return apperrors.InvalidRequest("recipient is required.")
		}
		in = usecases.SendInput{Recipient: *b.Recipient, TemplateKey: b.TemplateKey, TemplateVersion: b.TemplateVersion, Locale: b.Locale,
			Channels: b.Channels, Variables: b.Variables, Email: b.Destinations.Email, Phone: b.Destinations.Phone, Data: b.Data,
			CallerReference: b.CallerReference}
		return h.svc.ValidateSend(usecases.PrincipalFrom(ctx), &in)
	}, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		v, err := h.svc.SendTx(ctx, tx, usecases.PrincipalFrom(ctx), in)
		if err != nil {
			return idempotency.Result{}, err
		}
		return result(http.StatusAccepted, v, "notification", v.NotificationID)
	})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "notification_id"))
	if err != nil {
		httpx.WriteError(w, r, domain.ErrNotFound())
		return
	}
	v, err := h.svc.Get(r.Context(), usecases.PrincipalFrom(r.Context()), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, v)
}

type endpointBody struct {
	Recipient  *usecases.Recipient `json:"recipient"`
	Token      string              `json:"token"`
	Platform   string              `json:"platform"`
	DeviceID   string              `json:"device_id"`
	AppVersion string              `json:"app_version"`
}

func (h *handler) register(w http.ResponseWriter, r *http.Request, public bool) {
	var b endpointBody
	var in usecases.PushEndpointInput
	h.command(w, r, "notification.push_endpoint", &b, func(ctx context.Context) error {
		in = usecases.PushEndpointInput{Token: b.Token, Platform: b.Platform, DeviceID: b.DeviceID, AppVersion: b.AppVersion}
		switch {
		case public && b.Recipient != nil:
			return apperrors.InvalidRequest("recipient is derived from the signed-in user and must not be supplied.")
		case public:
			in.Recipient = self(ctx)
		case b.Recipient == nil:
			return apperrors.InvalidRequest("recipient is required.")
		default:
			in.Recipient = *b.Recipient
		}
		return usecases.ValidatePushEndpoint(usecases.PrincipalFrom(ctx), in)
	}, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		v, err := h.svc.RegisterPushEndpointTx(ctx, tx, in)
		if err != nil {
			return idempotency.Result{}, err
		}
		return result(http.StatusCreated, v, "push_endpoint", v.EndpointID)
	})
}

func (h *handler) registerInternal(w http.ResponseWriter, r *http.Request) { h.register(w, r, false) }
func (h *handler) registerPublic(w http.ResponseWriter, r *http.Request)   { h.register(w, r, true) }

func (h *handler) revoke(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "endpoint_id"))
	if err != nil {
		httpx.WriteError(w, r, apperrors.NotFound(domain.CodeEndpointNotFound, "Push endpoint not found."))
		return
	}
	if err := h.svc.RevokePushEndpoint(r.Context(), usecases.PrincipalFrom(r.Context()), id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) listEndpoints(w http.ResponseWriter, r *http.Request, rc usecases.Recipient) {
	v, err := h.svc.ListPushEndpoints(r.Context(), usecases.PrincipalFrom(r.Context()), rc)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, v)
}

func (h *handler) listEndpointsPublic(w http.ResponseWriter, r *http.Request) {
	h.listEndpoints(w, r, self(r.Context()))
}

func (h *handler) listEndpointsInternal(w http.ResponseWriter, r *http.Request) {
	rc, err := pathRecipient(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.listEndpoints(w, r, rc)
}

func (h *handler) getPreferences(w http.ResponseWriter, r *http.Request, rc usecases.Recipient) {
	v, err := h.svc.GetPreferences(r.Context(), usecases.PrincipalFrom(r.Context()), rc)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, v)
}

func (h *handler) getPreferencesPublic(w http.ResponseWriter, r *http.Request) {
	h.getPreferences(w, r, self(r.Context()))
}

func (h *handler) getPreferencesInternal(w http.ResponseWriter, r *http.Request) {
	rc, err := pathRecipient(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.getPreferences(w, r, rc)
}

type preferencesBody struct {
	Preferences []usecases.Preference `json:"preferences"`
	QuietHours  json.RawMessage       `json:"quiet_hours"`
}

// putPreferences is a naturally idempotent PUT (no Idempotency-Key needed).
func (h *handler) putPreferences(w http.ResponseWriter, r *http.Request, rc usecases.Recipient) {
	var b preferencesBody
	if err := httpx.DecodeJSON(w, r, &b, maxBody); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	u := usecases.PreferencesUpdate{Preferences: b.Preferences}
	if len(b.QuietHours) > 0 {
		u.SetQuietHours = true
		if string(b.QuietHours) != "null" {
			u.QuietHours = &usecases.QuietHoursView{}
			if err := json.Unmarshal(b.QuietHours, u.QuietHours); err != nil {
				httpx.WriteError(w, r, apperrors.InvalidRequest("quiet_hours must be an object or null."))
				return
			}
		}
	}
	v, err := h.svc.UpdatePreferences(r.Context(), usecases.PrincipalFrom(r.Context()), rc, u)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, v)
}

func (h *handler) putPreferencesPublic(w http.ResponseWriter, r *http.Request) {
	h.putPreferences(w, r, self(r.Context()))
}

func (h *handler) putPreferencesInternal(w http.ResponseWriter, r *http.Request) {
	rc, err := pathRecipient(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.putPreferences(w, r, rc)
}

func (h *handler) createTemplate(w http.ResponseWriter, r *http.Request) {
	var b usecases.TemplateInput
	h.command(w, r, "notification.template", &b, func(context.Context) error {
		return usecases.ValidateTemplate(&b)
	}, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		v, err := h.svc.CreateTemplateVersionTx(ctx, tx, usecases.PrincipalFrom(ctx), b)
		if err != nil {
			return idempotency.Result{}, err
		}
		return result(http.StatusCreated, v, "notification_template", v.TemplateID)
	})
}

func (h *handler) listTemplates(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.ListTemplateVersions(r.Context(), chi.URLParam(r, "template_key"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, v)
}
