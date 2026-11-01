package businesshttp

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
)

type ReservationWaitlistRejection struct {
	Version         int64  `json:"version"`
	OfferVersion    int64  `json:"offer_version"`
	ContinueWaiting bool   `json:"continue_waiting"`
	Reason          string `json:"reason"`
}

func (h *Handler) registerReservationWaitlistRejection(r chi.Router) {
	r.Post("/waitlist/{id}/offers/{offer_id}/reject", h.reservationWaitlistOfferReject)
}
func (h *Handler) reservationWaitlistOfferReject(w http.ResponseWriter, r *http.Request) {
	var in ReservationWaitlistRejection
	if !decode(w, r, &in) {
		return
	}
	offerID, e := ids.ParseV7(chi.URLParam(r, "offer_id"))
	if e != nil || in.Version < 1 || in.OfferVersion < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current waitlist/offer versions and a rejection reason required."))
		return
	}
	_, pre, e := h.waitlistTarget(r, "reservations:write")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "reservation:waitlist:reject:"+offerID.String(), in, "reservations:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reservations:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Load[ReservationWaitlist](ctx, tx, `SELECT to_jsonb(e) FROM waitlist_entries e WHERE organisation_id=$1 AND id=$2`, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		offer, e := reservations.Load[ReservationWaitlistOffer](ctx, tx, `SELECT to_jsonb(o) FROM waitlist_offers o WHERE organisation_id=$1 AND entry_id=$2 AND id=$3`, t.Organisation, v.ID, offerID)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		if v.Version != in.Version || v.State != "OFFERED" || offer.Version != in.OfferVersion || offer.State != "OFFERED" || !now.Before(offer.ExpiresAt) || !now.Before(v.ExpiresAt) {
			return 0, nil, apperrors.Conflict("WAITLIST_OFFER_STALE", "Only the current unexpired offered entry can be rejected.")
		}
		if e = h.releaseWaitlistOffers(ctx, tx, t.Organisation, v.ID, "REJECTED"); e != nil {
			return 0, nil, e
		}
		state := "CANCELLED"
		if in.ContinueWaiting {
			state = "WAITING"
		}
		if _, e = tx.Exec(ctx, `UPDATE waitlist_entries SET state=$3,version=version+1,updated_at=$4 WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, state, now); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, v.PropertyID, v.ID, "waitlist", "WAITLIST_OFFER_REJECTED", "reservation.waitlist.changed.v1", v.Version+1, map[string]any{"offer_id": offerID, "state": state, "offer_state": "REJECTED", "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 200, map[string]any{"id": v.ID, "offer_id": offerID, "state": state, "offer_state": "REJECTED", "version": v.Version + 1}, nil
	})
}
