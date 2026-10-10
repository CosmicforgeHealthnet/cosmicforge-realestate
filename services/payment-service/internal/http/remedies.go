package http

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"net/http"
)

func parsePool(v string) (ids.ID, error) {
	id, e := ids.ParseV7(v)
	if e != nil {
		return id, core.Fail(422, "INVALID_POOL_ID")
	}
	return id, nil
}

type remedyReference struct {
	AuthorityRef string   `json:"authority_ref"`
	Approvers    []string `json:"approvers"`
}

type readbackReplay struct {
	Kind         string `json:"kind"`
	AuthorityRef string `json:"authority_ref"`
}

func (h handler) registerRemedies(r chi.Router) {
	r.Post("/readback-jobs/{id}/replay", command(func(w http.ResponseWriter, r *http.Request, in readbackReplay) {
		if id, ok := parseID(w, r); ok {
			e := h.l.ReplayReadback(r.Context(), in.Kind, id, in.AuthorityRef)
			respond(w, r, map[string]bool{"recorded": e == nil}, e)
		}
	}))
	r.Post("/recoverable-actions", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.RecoverableAction) {
		e := h.l.RecoverableAction(r.Context(), in, r.Header.Get("Idempotency-Key"))
		respond(w, r, map[string]bool{"recorded": e == nil}, e)
	}))
	r.Post("/financial-case-overrides", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.CaseOverride) {
		e := h.l.OverrideCase(r.Context(), in)
		respond(w, r, map[string]bool{"recorded": e == nil}, e)
	}))
	r.Post("/reconciliation-resolutions", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.ReconciliationResolution) {
		e := h.l.ResolveReconciliation(r.Context(), in)
		respond(w, r, map[string]bool{"recorded": e == nil}, e)
	}))
	r.Post("/intent-cancellations", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.Cancellation) {
		v, e := h.l.CancelIntent(r.Context(), in, r.Header.Get("Idempotency-Key"))
		respond(w, r, v, e)
	}))
	r.Post("/remedy-policies", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.RemedyPolicy) {
		e := h.l.PutRemedyPolicy(r.Context(), in)
		respond(w, r, map[string]bool{"recorded": e == nil}, e)
	}))
	r.Post("/remedy-evidence", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.RemedyEvidence) {
		e := h.l.PutRemedyEvidence(r.Context(), in)
		respond(w, r, map[string]bool{"recorded": e == nil}, e)
	}))
	r.Post("/remedy-evidence/{id}/revoke", command(func(w http.ResponseWriter, r *http.Request, in remedyReference) {
		if id, ok := parseID(w, r); ok {
			e := h.l.RevokeRemedyEvidence(r.Context(), id, in.AuthorityRef)
			respond(w, r, map[string]bool{"recorded": e == nil}, e)
		}
	}))
	r.Post("/refundable-deposits", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.Deposit) {
		v, e := h.l.RegisterDeposit(r.Context(), in)
		respond(w, r, v, e)
	}))
	r.Get("/refundable-deposits/{id}", h.getDeposit)
	r.Post("/financial-cases", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.CaseRequest) {
		v, e := h.l.OpenCase(r.Context(), in, r.Header.Get("Idempotency-Key"))
		respond(w, r, v, e)
	}))
	r.Get("/financial-cases/{id}", h.getCase)
	r.Get("/financial-cases/{id}/facts", h.getCaseFacts)
	r.Post("/financial-case-facts", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.CaseFact) {
		e := h.l.AppendCaseFact(r.Context(), in, r.Header.Get("Idempotency-Key"))
		respond(w, r, map[string]bool{"recorded": e == nil}, e)
	}))
	r.Post("/financial-case-decisions", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.CaseDecision) {
		e := h.l.DecideCase(r.Context(), in, r.Header.Get("Idempotency-Key"))
		respond(w, r, map[string]bool{"recorded": e == nil}, e)
	}))
	r.Post("/refunds", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.RefundRequest) {
		v, e := h.l.RequestRefund(r.Context(), in, r.Header.Get("Idempotency-Key"))
		respond(w, r, v, e)
	}))
	r.Get("/refunds/{id}", h.getRefund)
	r.Post("/refunds/{id}/submit", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := parseID(w, r); ok {
			v, e := h.l.SubmitRefund(r.Context(), id)
			respond(w, r, v, e)
		}
	})
	r.Post("/refunds/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := parseID(w, r); ok {
			v, e := h.l.ResolveRefund(r.Context(), id)
			respond(w, r, v, e)
		}
	})
	r.Post("/refunds/{id}/cancel", command(func(w http.ResponseWriter, r *http.Request, in remedyReference) {
		if id, ok := parseID(w, r); ok {
			v, e := h.l.CancelRefund(r.Context(), id, in.AuthorityRef)
			respond(w, r, v, e)
		}
	}))
	r.Post("/collection-reversals", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.ReversalRequest) {
		v, e := h.l.ReverseCollection(r.Context(), in)
		respond(w, r, v, e)
	}))
	r.Post("/payout-destination-versions", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.DestinationChange) {
		v, e := h.l.ChangeDestination(r.Context(), in)
		respond(w, r, v, e)
	}))
	r.Post("/payout-destination-versions/{id}/revoke", command(func(w http.ResponseWriter, r *http.Request, in remedyReference) {
		if id, ok := parseID(w, r); ok {
			e := h.l.RevokeDestination(r.Context(), id, in.AuthorityRef)
			respond(w, r, map[string]bool{"recorded": e == nil}, e)
		}
	}))
	h.registerCustomer(r)
	r.Post("/customer-topup-funding", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.TopupFunding) {
		e := h.l.FundTopup(r.Context(), in)
		respond(w, r, map[string]bool{"recorded": e == nil}, e)
	}))
	r.Post("/unmatched-dva-credits/{id}/resolve", command(func(w http.ResponseWriter, r *http.Request, in remedyReference) {
		if id, ok := parseID(w, r); ok {
			e := h.l.ResolveUnmatchedDVA(r.Context(), id, in.AuthorityRef, in.Approvers)
			respond(w, r, map[string]bool{"recorded": e == nil}, e)
		}
	}))
	r.Post("/reconciliation-runs", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.ReconciliationRequest) {
		v, e := h.l.Reconcile(r.Context(), in)
		respond(w, r, v, e)
	}))
	r.Get("/financial-metrics", func(w http.ResponseWriter, r *http.Request) {
		v, e := h.l.OperationalMetrics(r.Context())
		respond(w, r, v, e)
	})
}
func (h handler) registerCustomer(r chi.Router) {
	r.Post("/customer-topups", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.TopupRequest) {
		v, e := h.l.RequestTopup(r.Context(), in, r.Header.Get("Idempotency-Key"))
		respond(w, r, v, e)
	}))
	r.Get("/customer-topups/{id}", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := parseID(w, r); ok {
			v, e := h.l.Topup(r.Context(), id)
			respond(w, r, v, e)
		}
	})
	r.Post("/wallet-spends", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.WalletSpend) {
		v, e := h.l.SpendWallet(r.Context(), in, r.Header.Get("Idempotency-Key"))
		respond(w, r, v, e)
	}))
	r.Get("/wallets/{id}/availability/{pool}", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := parseID(w, r); ok {
			pool, e := parsePool(chi.URLParam(r, "pool"))
			if e != nil {
				respond(w, r, nil, e)
				return
			}
			v, e := h.l.Availability(r.Context(), id, pool)
			respond(w, r, v, e)
		}
	})
	r.Get("/recurring-design", func(w http.ResponseWriter, r *http.Request) { respond(w, r, lifecycle.ReservedRecurringDesign(), nil) })
}
func (h handler) getCase(w http.ResponseWriter, r *http.Request) {
	if id, ok := parseID(w, r); ok {
		v, e := h.l.Case(r.Context(), id)
		respond(w, r, v, e)
	}
}
func (h handler) getCaseFacts(w http.ResponseWriter, r *http.Request) {
	if id, ok := parseID(w, r); ok {
		v, e := h.l.CaseFacts(r.Context(), id)
		respond(w, r, v, e)
	}
}
func (h handler) getRefund(w http.ResponseWriter, r *http.Request) {
	if id, ok := parseID(w, r); ok {
		v, e := h.l.Refund(r.Context(), id)
		respond(w, r, v, e)
	}
}
func (h handler) getDeposit(w http.ResponseWriter, r *http.Request) {
	if id, ok := parseID(w, r); ok {
		v, e := h.l.Deposit(r.Context(), id)
		respond(w, r, v, e)
	}
}
