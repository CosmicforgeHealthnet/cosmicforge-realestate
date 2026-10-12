package http

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"net/http"
)

func command[T any](fn func(http.ResponseWriter, *http.Request, T)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in T
		if decode(w, r, &in) {
			fn(w, r, in)
		}
	}
}
func (h handler) registerLifecycle(r chi.Router) {
	r.Post("/financial-policies/resolve", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.PolicySelector) {
		v, e := h.l.EffectivePolicy(r.Context(), in)
		respond(w, r, v, e)
	}))
	r.Post("/financial-policy-deactivations", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.PolicyDeactivation) {
		e := h.l.DeactivatePolicy(r.Context(), in)
		respond(w, r, map[string]bool{"recorded": e == nil}, e)
	}))
	r.Get("/financial-policy-families", func(w http.ResponseWriter, r *http.Request) { respond(w, r, lifecycle.Families(), nil) })
	r.Post("/financial-policies", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.Policy) {
		v, e := h.l.PutPolicy(r.Context(), in)
		respond(w, r, v, e)
	}))
	r.Get("/financial-policies/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(w, r)
		if ok {
			v, e := h.l.Policy(r.Context(), id)
			respond(w, r, v, e)
		}
	})
	r.Post("/beneficiary-authorities", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.Authority) {
		v, e := h.l.PutAuthority(r.Context(), in)
		respond(w, r, v, e)
	}))
	r.Get("/beneficiary-authorities/{id}", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := parseID(w, r); ok {
			v, e := h.l.AuthorityStatus(r.Context(), id)
			respond(w, r, v, e)
		}
	})
	r.Post("/beneficiary-authorities/{id}/revoke", command(func(w http.ResponseWriter, r *http.Request, in struct {
		EvidenceRef string `json:"evidence_ref"`
	}) {
		id, ok := parseID(w, r)
		if ok {
			e := h.l.RevokeAuthority(r.Context(), id, in.EvidenceRef)
			respond(w, r, map[string]bool{"recorded": e == nil}, e)
		}
	}))
	r.Post("/locked-quotes", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.Quote) {
		v, e := h.l.LockQuote(r.Context(), in, r.Header.Get("Idempotency-Key"))
		respond(w, r, v, e)
	}))
	r.Get("/locked-quotes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(w, r)
		if ok {
			v, e := h.l.Quote(r.Context(), id)
			respond(w, r, v, e)
		}
	})
	r.Get("/locked-quotes/{id}/receipt", h.receipt)
	r.Post("/allocations", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.AllocationInput) {
		v, e := h.l.Allocate(r.Context(), in)
		respond(w, r, v, e)
	}))
	r.Get("/allocations/{id}", h.getAllocation)
	r.Post("/allocation-lines/{id}/hold", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.Hold) {
		id, ok := parseID(w, r)
		if ok {
			e := h.l.Hold(r.Context(), id, in, r.Header.Get("Idempotency-Key"))
			respond(w, r, map[string]bool{"recorded": e == nil}, e)
		}
	}))
	r.Post("/release-evidence", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.Evidence) {
		e := h.l.SubmitEvidence(r.Context(), in)
		respond(w, r, map[string]bool{"recorded": e == nil}, e)
	}))
	r.Post("/allocation-releases", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.Release) {
		v, e := h.l.Release(r.Context(), in, r.Header.Get("Idempotency-Key"))
		respond(w, r, map[string]ids.ID{"journal_id": v}, e)
	}))
	r.Post("/transaction-funding", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.FundingInput) {
		e := h.l.ConfirmFunding(r.Context(), in)
		respond(w, r, map[string]bool{"recorded": e == nil}, e)
	}))
	r.Post("/payout-destination-bindings", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.Destination) {
		e := h.l.BindDestination(r.Context(), in)
		respond(w, r, map[string]bool{"recorded": e == nil}, e)
	}))
	r.Post("/withdrawal-approvals", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.WithdrawalApproval) {
		e := h.l.ApproveWithdrawal(r.Context(), in)
		respond(w, r, map[string]bool{"recorded": e == nil}, e)
	}))
	r.Post("/withdrawals", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.Withdrawal) {
		v, e := h.l.Withdraw(r.Context(), in, r.Header.Get("Idempotency-Key"))
		respond(w, r, v, e)
	}))
	r.Get("/withdrawals/{id}", h.getWithdrawal)
	r.Post("/withdrawals/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(w, r)
		if ok {
			v, e := h.l.ResolvePayout(r.Context(), id)
			respond(w, r, v, e)
		}
	})
	r.Post("/rent-agreements", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.Agreement) {
		v, e := h.l.PutAgreement(r.Context(), in)
		respond(w, r, v, e)
	}))
	r.Post("/rent-obligations", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.RentRequest) {
		v, e := h.l.RentObligation(r.Context(), in)
		respond(w, r, v, e)
	}))
	r.Get("/rent-obligations/{id}", h.getObligation)
	r.Post("/rent-obligations/{id}/allocate", command(func(w http.ResponseWriter, r *http.Request, in struct {
		IntentID ids.ID `json:"intent_id"`
	}) {
		id, ok := parseID(w, r)
		if ok {
			v, e := h.l.RentAllocation(r.Context(), id, in.IntentID)
			respond(w, r, v, e)
		}
	}))
}
func (h handler) getAllocation(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if ok {
		v, e := h.l.Allocation(r.Context(), id)
		respond(w, r, v, e)
	}
}
func (h handler) getWithdrawal(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if ok {
		v, e := h.l.Payout(r.Context(), id)
		respond(w, r, v, e)
	}
}
func (h handler) getObligation(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if ok {
		v, e := h.l.Obligation(r.Context(), id)
		respond(w, r, v, e)
	}
}
func (h handler) receipt(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if ok {
		v, e := h.l.Receipt(r.Context(), id)
		respond(w, r, v, e)
	}
}
