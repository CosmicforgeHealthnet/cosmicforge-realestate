package http

import (
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/go-chi/chi/v5"
)

type Config struct {
	Service  *finance.Service
	Verifier *serviceauth.Verifier
	Health   *health.Handler
	Logger   *slog.Logger
}
type handler struct {
	s *finance.Service
	l *lifecycle.Service
}

func NewRouter(c Config) http.Handler {
	r := chi.NewRouter()
	h := handler{c.Service, lifecycle.New(c.Service)}
	r.Use(observability.HTTPMiddleware("payment-service"), requestcontext.Middleware, httpx.WithLogger(c.Logger), httpx.Recoverer, httpx.AccessLog, security.Headers(security.HeadersConfig{}), httpx.MaxBody(64<<10))
	r.NotFound(httpx.NotFoundHandler().ServeHTTP)
	r.MethodNotAllowed(httpx.MethodNotAllowedHandler().ServeHTTP)
	c.Health.Register(r)
	r.Route("/internal/v1", func(r chi.Router) {
		r.Use(serviceauth.Authenticate(c.Verifier, serviceauth.HeaderAuthorization))
		h.registerLifecycle(r)
		r.Post("/wallets/ensure", h.ensure)
		r.Get("/wallets/{id}", h.wallet)
		r.Get("/wallets/{id}/balances", h.balances)
		r.Get("/wallets/{id}/transactions", h.history)
		r.Post("/financial-identities/link", h.link)
		r.Put("/financial-identities/{id}/verification", h.verification)
		r.Put("/financial-identities/{id}/state", h.identityState)
		r.Post("/payout-account-references", h.recordPayout)
		r.Get("/wallets/{id}/payout-account-references", h.payoutReferences)
		r.Post("/payment-intents", h.createIntent)
		r.Get("/payment-intents/{id}", h.getIntent)
		r.Post("/payment-intents/{id}/verify", h.verifyIntent)
		r.Post("/virtual-accounts", h.requestDVA)
		r.Get("/virtual-accounts/{id}", h.getDVA)
		r.Post("/virtual-accounts/{id}/sync", h.syncDVA)
		r.Post("/virtual-accounts/{id}/deactivate", h.deactivateDVA)
		r.Post("/provider-inbox/{id}/replay", h.replay)
		r.Get("/custody-pools/{id}/coverage", h.coverage)
		r.Post("/provider-accounts/{id}/reconcile", h.reconcile)
		r.Post("/provider-accounts/{id}/settlements", h.settlement)
	})
	r.Route("/api/v1/payments", func(r chi.Router) {
		r.Use(serviceauth.Authenticate(c.Verifier, serviceauth.HeaderServiceAuthorization), serviceauth.RequireCaller(platform.APIGateway))
		r.Get("/allocations/{id}", h.getAllocation)
		r.Get("/withdrawals/{id}", h.getWithdrawal)
		r.Get("/rent-obligations/{id}", h.getObligation)
		r.Get("/locked-quotes/{id}/receipt", h.receipt)
		r.Get("/wallets/me", h.mine)
		r.Get("/wallets/{id}", h.wallet)
		r.Get("/wallets/{id}/balances", h.balances)
		r.Get("/wallets/{id}/transactions", h.history)
		r.Get("/payment-intents/{id}", h.getIntent)
		r.Get("/virtual-accounts/{id}", h.getDVA)
	})
	r.Route("/webhooks/v1/paystack", func(r chi.Router) {
		r.Use(serviceauth.Authenticate(c.Verifier, serviceauth.HeaderServiceAuthorization), serviceauth.RequireCaller(platform.APIGateway))
		r.Post("/{id}/callbacks", h.webhook)
	})
	return r
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if e := httpx.DecodeJSON(w, r, v, 64<<10); e != nil {
		httpx.WriteError(w, r, e)
		return false
	}
	return true
}
func parseID(w http.ResponseWriter, r *http.Request) (ids.ID, bool) {
	id, e := ids.ParseV7(chi.URLParam(r, "id"))
	if e != nil {
		httpx.WriteError(w, r, core.Fail(422, "INVALID_ID"))
		return id, false
	}
	return id, true
}
func respond(w http.ResponseWriter, r *http.Request, v any, e error) {
	if e != nil {
		httpx.WriteError(w, r, e)
		return
	}
	httpx.WriteData(w, 200, v)
}
func (h handler) ensure(w http.ResponseWriter, r *http.Request) {
	var in core.Provision
	if !decode(w, r, &in) {
		return
	}
	v, e := h.s.EnsureWallet(r.Context(), in)
	respond(w, r, v, e)
}
func (h handler) mine(w http.ResponseWriter, r *http.Request) {
	v, e := h.s.MyWallets(r.Context())
	respond(w, r, v, e)
}
func (h handler) wallet(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	v, e := h.s.OwnWallet(r.Context(), id)
	respond(w, r, v, e)
}
func (h handler) balances(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	v, e := h.s.Balances(r.Context(), id)
	respond(w, r, v, e)
}
func (h handler) history(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	after := ids.Nil
	if str := r.URL.Query().Get("after"); str != "" {
		var e error
		after, e = ids.ParseV7(str)
		if e != nil {
			respond(w, r, nil, core.Fail(422, "INVALID_CURSOR"))
			return
		}
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		var e error
		limit, e = strconv.Atoi(v)
		if e != nil {
			respond(w, r, nil, core.Fail(422, "INVALID_PAGE_LIMIT"))
			return
		}
	}
	v, e := h.s.History(r.Context(), id, after, limit)
	respond(w, r, v, e)
}
func (h handler) link(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IdentityID  ids.ID `json:"identity_id"`
		Domain      string `json:"domain"`
		AccountID   ids.ID `json:"account_id"`
		EvidenceRef string `json:"evidence_ref"`
	}
	if !decode(w, r, &in) {
		return
	}
	e := h.s.LinkAccount(r.Context(), in.IdentityID, in.Domain, in.AccountID, in.EvidenceRef)
	respond(w, r, map[string]bool{"linked": e == nil}, e)
}
func (h handler) verification(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var in struct {
		Ref       string    `json:"verification_ref"`
		State     string    `json:"state"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if !decode(w, r, &in) {
		return
	}
	e := h.s.Verification(r.Context(), id, in.Ref, in.State, in.ExpiresAt)
	respond(w, r, map[string]bool{"recorded": e == nil}, e)
}
func (h handler) createIntent(w http.ResponseWriter, r *http.Request) {
	var in finance.IntentInput
	if !decode(w, r, &in) {
		return
	}
	v, e := h.s.CreateIntent(r.Context(), in, r.Header.Get("Idempotency-Key"))
	respond(w, r, v, e)
}
func (h handler) identityState(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var in core.IdentityControl
	if !decode(w, r, &in) {
		return
	}
	e := h.s.ControlIdentity(r.Context(), id, in)
	respond(w, r, map[string]bool{"recorded": e == nil}, e)
}
func (h handler) recordPayout(w http.ResponseWriter, r *http.Request) {
	var in core.PayoutReferenceInput
	if !decode(w, r, &in) {
		return
	}
	v, e := h.s.RecordPayoutReference(r.Context(), in)
	respond(w, r, v, e)
}
func (h handler) payoutReferences(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	v, e := h.s.PayoutReferences(r.Context(), id)
	respond(w, r, v, e)
}
func (h handler) getIntent(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	v, e := h.s.GetIntent(r.Context(), id)
	respond(w, r, v, e)
}
func (h handler) verifyIntent(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	v, e := h.s.VerifyIntent(r.Context(), id)
	respond(w, r, v, e)
}
func (h handler) requestDVA(w http.ResponseWriter, r *http.Request) {
	var in finance.DVAInput
	if !decode(w, r, &in) {
		return
	}
	v, e := h.s.RequestDVA(r.Context(), in)
	respond(w, r, v, e)
}
func (h handler) getDVA(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	v, e := h.s.DVA(r.Context(), id)
	respond(w, r, v, e)
}
func (h handler) syncDVA(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	v, e := h.s.SyncDVA(r.Context(), id, false)
	respond(w, r, v, e)
}
func (h handler) deactivateDVA(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	v, e := h.s.SyncDVA(r.Context(), id, true)
	respond(w, r, v, e)
}
func (h handler) webhook(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	body, e := io.ReadAll(r.Body)
	if e != nil {
		respond(w, r, nil, core.Fail(413, "PAYLOAD_TOO_LARGE"))
		return
	}
	e = h.s.IngestWebhook(r.Context(), id, body, r.Header.Get("X-Paystack-Signature"))
	respond(w, r, map[string]bool{"received": e == nil}, e)
}
func (h handler) replay(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var in struct {
		EvidenceRef string `json:"evidence_ref"`
	}
	if !decode(w, r, &in) {
		return
	}
	e := h.s.ReplayInbox(r.Context(), id, in.EvidenceRef)
	respond(w, r, map[string]bool{"requeued": e == nil}, e)
}
func (h handler) coverage(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	v, e := h.s.Coverage(r.Context(), id)
	respond(w, r, v, e)
}
func (h handler) reconcile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	e := h.s.ReconcileBalance(r.Context(), id)
	respond(w, r, map[string]bool{"reconciled": e == nil}, e)
}
func (h handler) settlement(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var in struct {
		Reference string `json:"reference"`
	}
	if !decode(w, r, &in) {
		return
	}
	e := h.s.ObserveSettlement(r.Context(), id, in.Reference)
	respond(w, r, map[string]bool{"observed": e == nil}, e)
}
