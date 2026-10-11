package http

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strconv"
)

func (h handler) registerWalletTransfers(r chi.Router) {
	r.Get("/locked-quotes/{id}/invoice", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := parseID(w, r); ok {
			v, e := h.l.Invoice(r.Context(), id)
			respond(w, r, v, e)
		}
	})

	r.Get("/wallets/{id}/profile", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := parseID(w, r); ok {
			v, e := h.s.WalletProfile(r.Context(), id)
			respond(w, r, v, e)
		}
	})
	r.Post("/wallet-transfers", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.WalletTransferInput) {
		v, e := h.l.RequestWalletTransfer(r.Context(), in, r.Header.Get("Idempotency-Key"))
		respond(w, r, v, e)
	}))
	r.Post("/wallet-transfers/{id}/confirm", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := parseID(w, r); ok {
			v, e := h.l.ConfirmWalletTransfer(r.Context(), id)
			respond(w, r, v, e)
		}
	})
	r.Get("/wallet-transfers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := parseID(w, r); ok {
			v, e := h.l.WalletTransfer(r.Context(), id)
			respond(w, r, v, e)
		}
	})
	r.Get("/wallets/{id}/transfers", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(w, r)
		if !ok {
			return
		}
		after := ids.Nil
		limit := 50
		if raw := r.URL.Query().Get("after"); raw != "" {
			v, e := ids.ParseV7(raw)
			if e != nil {
				respond(w, r, nil, core.Fail(422, "INVALID_CURSOR"))
				return
			}
			after = v
		}
		if raw := r.URL.Query().Get("limit"); raw != "" {
			v, e := strconv.Atoi(raw)
			if e != nil {
				respond(w, r, nil, core.Fail(422, "INVALID_PAGE_LIMIT"))
				return
			}
			limit = v
		}
		v, e := h.l.WalletTransfers(r.Context(), id, after, limit)
		respond(w, r, v, e)
	})
}
func (h handler) registerWalletAdministration(r chi.Router) {
	r.Put("/wallets/{id}/state", command(func(w http.ResponseWriter, r *http.Request, in core.IdentityControl) {
		if id, ok := parseID(w, r); ok {
			e := h.s.ControlWallet(r.Context(), id, in)
			respond(w, r, map[string]bool{"recorded": e == nil}, e)
		}
	}))
	r.Post("/wallet-transfer-policies", command(func(w http.ResponseWriter, r *http.Request, in lifecycle.WalletTransferPolicy) {
		e := h.l.PutWalletTransferPolicy(r.Context(), in)
		respond(w, r, map[string]bool{"recorded": e == nil}, e)
	}))
	r.Post("/financial-capability-evidence", command(func(w http.ResponseWriter, r *http.Request, in finance.CapabilityEvidence) {
		e := h.s.RecordCapabilityEvidence(r.Context(), in)
		respond(w, r, map[string]bool{"recorded": e == nil}, e)
	}))
	r.Post("/financial-capabilities/evaluate", command(func(w http.ResponseWriter, r *http.Request, in finance.CapabilityRequest) {
		v, e := h.s.EvaluateCapability(r.Context(), in)
		respond(w, r, v, e)
	}))
}
