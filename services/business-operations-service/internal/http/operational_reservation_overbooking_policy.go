package businesshttp

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

type ReservationOverbookingPolicy struct {
	ID         ids.ID                         `json:"id"`
	PropertyID ids.ID                         `json:"property_id"`
	PoolID     ids.ID                         `json:"pool_id"`
	Definition reservations.OverbookingPolicy `json:"definition"`
	State      string                         `json:"state"`
	Version    int64                          `json:"version"`
}
type ReservationOverbookingPolicyCommand struct {
	PropertyID      ids.ID                         `json:"property_id"`
	PoolID          ids.ID                         `json:"pool_id"`
	ExpectedVersion int64                          `json:"expected_version"`
	Definition      reservations.OverbookingPolicy `json:"definition"`
	State           string                         `json:"state"`
	ApprovalID      ids.ID                         `json:"approval_id"`
	Reason          string                         `json:"reason"`
}
type ReservationOverbookingQuote struct {
	PolicyID      ids.ID                 `json:"policy_id"`
	PolicyVersion int64                  `json:"policy_version"`
	Quote         CommercialQuoteCommand `json:"quote"`
}

func (h *Handler) registerReservationOverbooking(r chi.Router) {
	r.Get("/reservation-overbooking-policies", h.reservationOverbookingPolicies)
	r.Post("/reservation-overbooking-policies", h.reservationOverbookingPolicySave)
	r.Post("/reservation-overbooking-quotes", h.reservationOverbookingQuote)
	r.Post("/reservations/{id}/overbook", h.reservationOverbook)
	r.Post("/reservations/{id}/overbooking/reconcile", h.reservationOverbookingReconcile)
}
func (h *Handler) reservationOverbookingPolicies(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	properties, e := h.operationalProperties(r.Context(), h.Auth.Service.Pool, t, "reservations:read", "", "")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(p) FROM reservation_overbooking_policies p WHERE organisation_id=$1 AND property_id=ANY($2::uuid[]) AND pool_id IS NOT NULL ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, properties, limit, offset)
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) reservationOverbookingPolicySave(w http.ResponseWriter, r *http.Request) {
	var in ReservationOverbookingPolicyCommand
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.PoolID.ValidateV7() != nil || in.ExpectedVersion < 0 || in.State != "ACTIVE" && in.State != "SUSPENDED" || in.Definition.Validate(h.Auth.Service.Now().UTC()) != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Explicit valid pool-scoped policy, version, state and reason required."))
		return
	}
	h.execute(w, r, "reservation:overbooking-policy:"+in.PoolID.String(), in, "reservations:overbook", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "reservations:overbook", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		pool, e := inventory.GetPool(ctx, tx, t.Organisation, in.PoolID)
		if e != nil {
			return 0, nil, e
		}
		if pool.PropertyID != in.PropertyID {
			return 0, nil, apperrors.PermissionDenied("Pool belongs to another property.")
		}
		if e = h.commercialGate(ctx, tx, t, in.PropertyID); e != nil {
			return 0, nil, e
		}
		var version int64
		var oldID *ids.ID
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0),(array_agg(id ORDER BY version DESC))[1] FROM reservation_overbooking_policies WHERE organisation_id=$1 AND pool_id=$2`, t.Organisation, in.PoolID).Scan(&version, &oldID); e != nil {
			return 0, nil, e
		}
		if version != in.ExpectedVersion {
			return 0, nil, apperrors.Conflict("OVERBOOKING_POLICY_VERSION", "Overbooking policy changed.")
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESERVATION_OVERBOOKING", target.Scope, in.ApprovalID, map[string]any{"action": "POLICY", "command": op}, "", nil, ""); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		status := 201
		if oldID != nil {
			id = *oldID
			status = 200
			if _, e = tx.Exec(ctx, `UPDATE reservation_overbooking_policies SET definition=$3,state=$4,approval_id=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, marshalCommercial(in.Definition), in.State, in.ApprovalID); e != nil {
				return 0, nil, e
			}
		} else {
			if _, e = tx.Exec(ctx, `INSERT INTO reservation_overbooking_policies(id,organisation_id,property_id,pool_id,definition,state,approval_id,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, in.PropertyID, in.PoolID, marshalCommercial(in.Definition), in.State, in.ApprovalID, t.Account); e != nil {
				return 0, nil, e
			}
		}
		if e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "overbooking_policy", "OVERBOOKING_POLICY_CHANGED", "reservation.overbooking.changed.v1", version+1, map[string]any{"state": in.State, "pool_id": in.PoolID, "definition": in.Definition, "approval_id": in.ApprovalID, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := reservations.Load[ReservationOverbookingPolicy](ctx, tx, `SELECT to_jsonb(p) FROM reservation_overbooking_policies p WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		return status, out, e
	})
}
func (h *Handler) currentOverbookingPolicy(ctx context.Context, tx pgx.Tx, t tenant, id ids.ID, version int64) (ReservationOverbookingPolicy, error) {
	v, e := reservations.Load[ReservationOverbookingPolicy](ctx, tx, `SELECT to_jsonb(p) FROM reservation_overbooking_policies p WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if e != nil {
		return v, e
	}
	now := h.Auth.Service.Now().UTC()
	if v.Version != version || v.State != "ACTIVE" || now.Before(v.Definition.EffectiveAt) || !now.Before(v.Definition.ExpiresAt) || v.Definition.Validate(now) != nil {
		return v, apperrors.Conflict("OVERBOOKING_POLICY_INACTIVE", "The exact active and effective overbooking policy is required.")
	}
	return v, nil
}
func (h *Handler) reservationOverbookingQuote(w http.ResponseWriter, r *http.Request) {
	var in ReservationOverbookingQuote
	if !decode(w, r, &in) {
		return
	}
	if in.PolicyID.ValidateV7() != nil || in.PolicyVersion < 1 || in.Quote.ProductID.ValidateV7() != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current overbooking policy and product quote required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	p, e := inventory.GetProduct(r.Context(), h.Auth.Service.Pool, t.Organisation, in.Quote.ProductID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "reservation:overbooking:quote", in, "reservations:overbook", "PROPERTY", p.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reservations:overbook", "PROPERTY", p.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		policy, e := h.currentOverbookingPolicy(ctx, tx, t, in.PolicyID, in.PolicyVersion)
		if e != nil {
			return 0, nil, e
		}
		current, e := inventory.GetProduct(ctx, tx, t.Organisation, in.Quote.ProductID)
		if e != nil {
			return 0, nil, e
		}
		if policy.PoolID != current.PoolID || policy.PropertyID != current.PropertyID || in.Quote.Channel != "" {
			return 0, nil, apperrors.PermissionDenied("Overbooking quotes are limited to their approved pool and direct staff channel.")
		}
		q, e := h.calculateQuote(ctx, tx, t, in.Quote, false)
		if e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO price_quotes(id,organisation_id,property_id,product_id,rate_version_id,date_from,date_to,amount_minor,currency,expires_at,snapshot,created_by) VALUES($1,$2,$3,$4,$5,$6::date,$7::date,$8,$9,$10,$11,$12)`, q.QuoteID, t.Organisation, q.PropertyID, q.ProductID, q.Nights[0].RateVersionID, q.DateFrom, q.DateTo, q.AmountMinor, q.Currency, q.ExpiresAt, marshalCommercial(q), t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, q.PropertyID, q.QuoteID, "price_quote", "OVERBOOKING_PRICE_QUOTED", "", 1, map[string]any{"policy_id": policy.ID, "policy_version": policy.Version, "quote_id": q.QuoteID, "amount_minor": q.AmountMinor, "currency": q.Currency, "capacity_reserved": false, "expires_at": q.ExpiresAt}); e != nil {
			return 0, nil, e
		}
		return 201, q, nil
	})
}

type ReservationOverbookingCommand struct {
	Version           int64  `json:"version"`
	PolicyID          ids.ID `json:"policy_id"`
	PolicyVersion     int64  `json:"policy_version"`
	ApprovalID        ids.ID `json:"approval_id"`
	RecoveryReference string `json:"recovery_reference"`
	Reason            string `json:"reason"`
}

func validOverbookingCommand(in ReservationOverbookingCommand) bool {
	return in.Version > 0 && in.PolicyID.ValidateV7() == nil && in.PolicyVersion > 0 && in.ApprovalID.ValidateV7() == nil && len(in.RecoveryReference) > 0 && len(in.RecoveryReference) <= 300 && !strings.ContainsRune(in.RecoveryReference, 0) && validStaffReason(in.Reason)
}

// Keep policy expiry separate from the already accepted customer's obligation.
func overbookingPermitDeadline(policy ReservationOverbookingPolicy, now time.Time) time.Time {
	deadline := now.Add(5 * time.Minute)
	if policy.Definition.ExpiresAt.Before(deadline) {
		return policy.Definition.ExpiresAt
	}
	return deadline
}
