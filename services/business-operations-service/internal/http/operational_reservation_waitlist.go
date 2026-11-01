package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

type ReservationWaitlist struct {
	ID             ids.ID    `json:"id"`
	OrganisationID ids.ID    `json:"organisation_id"`
	PropertyID     ids.ID    `json:"property_id"`
	ProductID      ids.ID    `json:"product_id"`
	DateFrom       string    `json:"date_from"`
	DateTo         string    `json:"date_to"`
	Adults         int       `json:"adults"`
	Children       int       `json:"children"`
	Quantity       int       `json:"quantity"`
	Priority       int       `json:"priority"`
	GuestReference string    `json:"guest_reference"`
	State          string    `json:"state"`
	ExpiresAt      time.Time `json:"expires_at"`
	Version        int64     `json:"version"`
	Reason         string    `json:"reason,omitempty"`
}
type ReservationWaitlistOffer struct {
	ID             ids.ID    `json:"id"`
	OrganisationID ids.ID    `json:"organisation_id"`
	PropertyID     ids.ID    `json:"property_id"`
	EntryID        ids.ID    `json:"entry_id"`
	HoldID         ids.ID    `json:"hold_id"`
	QuoteID        ids.ID    `json:"quote_id"`
	State          string    `json:"state"`
	ExpiresAt      time.Time `json:"expires_at"`
	ReservationID  *ids.ID   `json:"reservation_id,omitempty"`
	Version        int64     `json:"version"`
}
type ReservationWaitlistOfferCommand struct {
	Version        int64  `json:"version"`
	QuoteID        ids.ID `json:"quote_id"`
	ProductVersion int64  `json:"product_version"`
	PoolVersion    int64  `json:"pool_version"`
	Channel        string `json:"channel"`
	Reason         string `json:"reason"`
}
type ReservationWaitlistAccept struct {
	Version      int64               `json:"version"`
	OfferID      ids.ID              `json:"offer_id"`
	OfferVersion int64               `json:"offer_version"`
	Booking      reservations.Create `json:"booking"`
}

func (h *Handler) registerReservationWaitlist(r chi.Router) {
	r.Get("/waitlist", h.reservationWaitlistList)
	r.Post("/waitlist", h.reservationWaitlistCreate)
	r.Get("/waitlist/{id}/offers", h.reservationWaitlistOffers)
	r.Post("/waitlist/{id}/offers", h.reservationWaitlistOfferCreate)
	r.Post("/waitlist/{id}/accept", h.reservationWaitlistAccept)
	r.Post("/waitlist/{id}/cancel", h.reservationWaitlistCancel)
}
func (h *Handler) reservationWaitlistList(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "waitlist_entries", "reservations:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		return reservations.Load[ReservationWaitlist](ctx, h.Auth.Service.Pool, `SELECT to_jsonb(e) FROM waitlist_entries e WHERE organisation_id=$1 AND id=$2`, org, id)
	})
}
func (h *Handler) reservationWaitlistCreate(w http.ResponseWriter, r *http.Request) {
	var in ReservationWaitlist
	if !decode(w, r, &in) {
		return
	}
	now := h.Auth.Service.Now().UTC()
	if !in.ID.IsZero() || !in.OrganisationID.IsZero() || in.Version != 0 || in.State != "" || in.PropertyID.ValidateV7() != nil || in.ProductID.ValidateV7() != nil || in.Adults < 1 || in.Adults > 100 || in.Children < 0 || in.Children > 100 || in.Quantity < 1 || in.Quantity > 100 || in.Priority < 0 || in.Priority > 100 || len(in.GuestReference) < 1 || len(in.GuestReference) > 300 || !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(90*24*time.Hour)) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Bounded canonical product, stay, guest reference and future waitlist expiration required."))
		return
	}
	if _, e := (inventory.DateRange{DateFrom: in.DateFrom, DateTo: in.DateTo}).Days("UTC"); e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid civil dates required."))
		return
	}
	h.execute(w, r, "reservation:waitlist:create", in, "reservations:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reservations:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		if e = h.commercialGate(ctx, tx, t, in.PropertyID); e != nil {
			return 0, nil, e
		}
		p, e := inventory.GetProduct(ctx, tx, t.Organisation, in.ProductID)
		if e != nil {
			return 0, nil, e
		}
		if p.PropertyID != in.PropertyID || p.State != "ACTIVE" || in.Adults+in.Children < p.MinGuests || in.Adults+in.Children > p.MaxGuests {
			return 0, nil, apperrors.Conflict("WAITLIST_PRODUCT_INELIGIBLE", "Current product property and occupancy must match this request.")
		}
		var duplicate bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM waitlist_entries WHERE organisation_id=$1 AND product_id=$2 AND date_from=$3::date AND date_to=$4::date AND guest_reference=$5 AND state IN ('WAITING','OFFERED') AND expires_at>$6)`, t.Organisation, p.ID, in.DateFrom, in.DateTo, in.GuestReference, now).Scan(&duplicate); e != nil {
			return 0, nil, e
		}
		if duplicate {
			return 0, nil, apperrors.Conflict("WAITLIST_ALREADY_ACTIVE", "An active waitlist entry already represents this guest and stay.")
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO waitlist_entries(id,organisation_id,property_id,product_id,date_from,date_to,adults,children,quantity,priority,guest_reference,expires_at,created_by) VALUES($1,$2,$3,$4,$5::date,$6::date,$7,$8,$9,$10,$11,$12,$13)`, id, t.Organisation, p.PropertyID, p.ID, in.DateFrom, in.DateTo, in.Adults, in.Children, in.Quantity, in.Priority, in.GuestReference, in.ExpiresAt, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := reservations.Load[ReservationWaitlist](ctx, tx, `SELECT to_jsonb(e) FROM waitlist_entries e WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, p.PropertyID, id, "waitlist", "WAITLIST_CREATED", "", 1, map[string]any{"product_id": p.ID, "date_from": in.DateFrom, "date_to": in.DateTo, "reason": in.Reason})
		return 201, out, e
	})
}
func (h *Handler) waitlistTarget(r *http.Request, cap string) (tenant, ReservationWaitlist, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, ReservationWaitlist{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, ReservationWaitlist{}, e
	}
	v, e := reservations.Load[ReservationWaitlist](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(e) FROM waitlist_entries e WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if e == nil {
		t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, cap, "PROPERTY", v.PropertyID, false)
	}
	return t, v, e
}
func (h *Handler) reservationWaitlistOffers(w http.ResponseWriter, r *http.Request) {
	t, v, e := h.waitlistTarget(r, "reservations:read")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(o) FROM waitlist_offers o WHERE organisation_id=$1 AND entry_id=$2 ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, v.ID, limit, offset)
	reply(w, r, 200, map[string]any{"items": rows, "limit": limit, "offset": offset}, e)
}
func (h *Handler) reservationWaitlistOfferCreate(w http.ResponseWriter, r *http.Request) {
	var in ReservationWaitlistOfferCommand
	if !decode(w, r, &in) {
		return
	}
	_, pre, e := h.waitlistTarget(r, "reservations:write")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || in.QuoteID.ValidateV7() != nil || in.ProductVersion < 1 || in.PoolVersion < 1 || !inventory.ValidChannel(in.Channel) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current waitlist, product, pool and accepted quote required."))
		return
	}
	h.execute(w, r, "reservation:waitlist:offer:"+pre.ID.String(), in, "reservations:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reservations:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Load[ReservationWaitlist](ctx, tx, `SELECT to_jsonb(e) FROM waitlist_entries e WHERE organisation_id=$1 AND id=$2`, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		if v.Version != in.Version || v.State != "WAITING" || !now.Before(v.ExpiresAt) {
			return 0, nil, apperrors.Conflict("WAITLIST_STALE", "An unexpired waiting entry at its current version is required.")
		}
		if e = h.commercialGate(ctx, tx, t, v.PropertyID); e != nil {
			return 0, nil, e
		}
		q, e := loadCommercialQuote(ctx, tx, t.Organisation, in.QuoteID)
		if e != nil {
			return 0, nil, e
		}
		if q.ProductID != v.ProductID || q.Adults != v.Adults || q.Children != v.Children || q.Quantity != v.Quantity || q.DateFrom != v.DateFrom || q.DateTo != v.DateTo {
			return 0, nil, apperrors.Conflict("WAITLIST_QUOTE_MISMATCH", "Offer must retain the requested guest occupancy and stay.")
		}
		seconds := min(300, int(v.ExpiresAt.Sub(now).Seconds()), int(q.ExpiresAt.Sub(now).Seconds()))
		if seconds < 30 {
			return 0, nil, apperrors.Conflict("WAITLIST_OFFER_TOO_SHORT", "The entry and quote must remain valid for at least 30 seconds.")
		}
		command := inventory.HoldCommand{ProductID: v.ProductID, ProductVersion: in.ProductVersion, PoolVersion: in.PoolVersion, QuoteID: q.QuoteID, DateFrom: v.DateFrom, DateTo: v.DateTo, Quantity: v.Quantity, Channel: in.Channel, DurationSeconds: seconds}
		if e = h.validateHoldQuote(ctx, tx, t, command); e != nil {
			return 0, nil, e
		}
		hold, e := (inventory.Repository{Pool: h.Auth.Service.Pool, Outbox: h.Auth.Service.Outbox, Now: h.Auth.Service.Now}).PlaceHold(ctx, tx, t.Organisation, t.Account, command)
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO waitlist_offers(id,organisation_id,property_id,entry_id,hold_id,quote_id,expires_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, v.PropertyID, v.ID, hold.ID, q.QuoteID, hold.ExpiresAt, t.Account); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE waitlist_entries SET state='OFFERED',version=version+1,updated_at=$3 WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, now); e != nil {
			return 0, nil, e
		}
		out, e := reservations.Load[ReservationWaitlistOffer](ctx, tx, `SELECT to_jsonb(o) FROM waitlist_offers o WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		return 201, out, e
	})
}
func (h *Handler) reservationWaitlistAccept(w http.ResponseWriter, r *http.Request) {
	var in ReservationWaitlistAccept
	if !decode(w, r, &in) {
		return
	}
	_, pre, e := h.waitlistTarget(r, "reservations:write")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || in.OfferVersion < 1 || in.OfferID.ValidateV7() != nil || in.Booking.Validate() != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current waitlist offer and complete accepted reservation required."))
		return
	}
	h.execute(w, r, "reservation:waitlist:accept:"+pre.ID.String(), in, "reservations:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reservations:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Load[ReservationWaitlist](ctx, tx, `SELECT to_jsonb(e) FROM waitlist_entries e WHERE organisation_id=$1 AND id=$2`, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		offer, e := reservations.Load[ReservationWaitlistOffer](ctx, tx, `SELECT to_jsonb(o) FROM waitlist_offers o WHERE organisation_id=$1 AND id=$2`, t.Organisation, in.OfferID)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		if v.Version != in.Version || v.State != "OFFERED" || offer.EntryID != v.ID || offer.Version != in.OfferVersion || offer.State != "OFFERED" || !now.Before(offer.ExpiresAt) || !now.Before(v.ExpiresAt) {
			return 0, nil, apperrors.Conflict("WAITLIST_OFFER_STALE", "This offer has expired, changed or was already accepted.")
		}
		booking := in.Booking
		if booking.PropertyID != v.PropertyID || len(booking.Rooms) != 1 || len(booking.Guests) != 1 || booking.Guests[0].SourceReference != v.GuestReference || booking.Rooms[0].QuoteID != offer.QuoteID || booking.Rooms[0].HoldID == nil || *booking.Rooms[0].HoldID != offer.HoldID || booking.Rooms[0].ProductID != v.ProductID || booking.Rooms[0].DateFrom != v.DateFrom || booking.Rooms[0].DateTo != v.DateTo || booking.Rooms[0].Quantity != v.Quantity || booking.State == "INQUIRY" {
			return 0, nil, apperrors.InvalidRequest("Accept only the exact held offer, original guest reference and stay.")
		}
		out, e := h.createReservationTx(ctx, tx, t, booking, false)
		if e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE waitlist_offers SET state='ACCEPTED',reservation_id=$3,version=version+1,updated_at=$4 WHERE organisation_id=$1 AND id=$2`, t.Organisation, offer.ID, out.Reservation.ID, now); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE waitlist_entries SET state='ACCEPTED',version=version+1,updated_at=$3 WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, now); e != nil {
			return 0, nil, e
		}
		return 201, out, nil
	})
}
func (h *Handler) reservationWaitlistCancel(w http.ResponseWriter, r *http.Request) {
	var in reservations.Command
	if !decode(w, r, &in) {
		return
	}
	_, pre, e := h.waitlistTarget(r, "reservations:cancel")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if !validReservationCommand(in) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version and reason required."))
		return
	}
	h.execute(w, r, "reservation:waitlist:cancel:"+pre.ID.String(), in, "reservations:cancel", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reservations:cancel", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Load[ReservationWaitlist](ctx, tx, `SELECT to_jsonb(e) FROM waitlist_entries e WHERE organisation_id=$1 AND id=$2`, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version || v.State != "WAITING" && v.State != "OFFERED" {
			return 0, nil, apperrors.Conflict("WAITLIST_STALE", "Only a current waiting/offered entry can be cancelled.")
		}
		if e = h.releaseWaitlistOffers(ctx, tx, t.Organisation, v.ID, "REJECTED"); e != nil {
			return 0, nil, e
		}
		_, e = tx.Exec(ctx, `UPDATE waitlist_entries SET state='CANCELLED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID)
		return 200, map[string]any{"id": v.ID, "state": "CANCELLED", "version": v.Version + 1}, e
	})
}
func (h *Handler) releaseWaitlistOffers(ctx context.Context, tx pgx.Tx, org, entry ids.ID, state string) error {
	rows, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(o) FROM waitlist_offers o WHERE organisation_id=$1 AND entry_id=$2 AND state='OFFERED' ORDER BY id`, org, entry)
	if e != nil {
		return e
	}
	for _, raw := range rows {
		var v ReservationWaitlistOffer
		if e = json.Unmarshal(raw, &v); e != nil {
			return e
		}
		hold, e := inventory.GetHold(ctx, tx, org, v.HoldID)
		if e != nil {
			return e
		}
		if hold.State == "ACTIVE" {
			if _, e = tx.Exec(ctx, `UPDATE inventory_holds SET state='CANCELLED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, org, hold.ID); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE inventory_calendar SET version=version+1,updated_at=now() WHERE organisation_id=$1 AND pool_id=$2 AND day>=$3::date AND day<$4::date`, org, hold.PoolID, hold.DateFrom, hold.DateTo); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE waitlist_offers SET state=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, org, v.ID, state); e != nil {
			return e
		}
	}
	return nil
}

// Expiry is also checked on commands. This worker only reconciles persisted
// lifecycle/history and releases stock; stopped scheduling never extends trust.
func (h *Handler) ProcessReservationMaintenance(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	now := h.Auth.Service.Now().UTC()
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT organisation_id,id FROM reservations WHERE state IN ('INQUIRY','HOLD','PENDING_PAYMENT','PENDING_APPROVAL') AND hold_expires_at<=$1 ORDER BY hold_expires_at,id LIMIT $2`, now, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, id ids.ID }
	refs := []ref{}
	for rows.Next() {
		var v ref
		if e = rows.Scan(&v.org, &v.id); e != nil {
			break
		}
		refs = append(refs, v)
	}
	rowErr := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if rowErr != nil {
		return rowErr
	}
	for _, v := range refs {
		e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, v.org); e != nil {
				return e
			}
			if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
				return e
			}
			r, e := reservations.Get(ctx, tx, v.org, v.id)
			if e != nil {
				return e
			}
			if r.HoldExpiresAt == nil || now.Before(*r.HoldExpiresAt) || !reservations.Transition(r.State, "EXPIRED") {
				return nil
			}
			rooms, e := reservations.Rooms(ctx, tx, v.org, v.id, true)
			if e != nil {
				return e
			}
			for _, room := range rooms {
				if e = reservations.Release(ctx, tx, v.org, r.Actor(), room, room.DateFrom, now); e != nil {
					return e
				}
			}
			if e = h.cancellationObligation(ctx, tx, tenant{Organisation: v.org, Account: r.Actor()}, r, rooms, "EXPIRED", "Accepted reservation hold deadline elapsed", r.Version+1); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE reservations SET state='EXPIRED',hold_expires_at=NULL,reason='Accepted reservation hold deadline elapsed',version=version+1,updated_at=$3 WHERE organisation_id=$1 AND id=$2`, v.org, v.id, now); e != nil {
				return e
			}
			r, e = reservations.Get(ctx, tx, v.org, v.id)
			if e != nil {
				return e
			}
			return h.reservationCommit(ctx, tx, tenant{Organisation: v.org, Account: r.Actor()}, r, "EXPIRED", "reservation.modified.v1", "Accepted reservation hold deadline elapsed", map[string]any{"deadline": now})
		})
		if e != nil {
			return e
		}
	}
	return h.expireReservationWaitlist(ctx, limit, now)
}
func (h *Handler) expireReservationWaitlist(ctx context.Context, limit int, now time.Time) error {
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT organisation_id,id FROM waitlist_entries e WHERE state IN ('WAITING','OFFERED') AND (expires_at<=$1 OR EXISTS(SELECT 1 FROM waitlist_offers o WHERE o.entry_id=e.id AND o.state='OFFERED' AND o.expires_at<=$1)) ORDER BY created_at,id LIMIT $2`, now, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, id ids.ID }
	refs := []ref{}
	for rows.Next() {
		var v ref
		if e = rows.Scan(&v.org, &v.id); e != nil {
			break
		}
		refs = append(refs, v)
	}
	rowErr := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if rowErr != nil {
		return rowErr
	}
	for _, v := range refs {
		e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, v.org); e != nil {
				return e
			}
			entry, e := reservations.Load[ReservationWaitlist](ctx, tx, `SELECT to_jsonb(e) FROM waitlist_entries e WHERE organisation_id=$1 AND id=$2`, v.org, v.id)
			if errors.Is(e, pgx.ErrNoRows) {
				return nil
			}
			if e != nil {
				return e
			}
			if entry.State != "WAITING" && entry.State != "OFFERED" {
				return nil
			}
			if e = h.releaseWaitlistOffers(ctx, tx, v.org, v.id, "EXPIRED"); e != nil {
				return e
			}
			state := "WAITING"
			if !now.Before(entry.ExpiresAt) {
				state = "EXPIRED"
			}
			_, e = tx.Exec(ctx, `UPDATE waitlist_entries SET state=$3,version=version+1,updated_at=$4 WHERE organisation_id=$1 AND id=$2`, v.org, v.id, state, now)
			return e
		})
		if e != nil {
			return e
		}
	}
	return nil
}
