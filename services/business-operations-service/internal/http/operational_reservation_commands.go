package businesshttp

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func (h *Handler) registerReservationCommands(r chi.Router) {
	r.Post("/reservations/{id}/confirm", h.reservationConfirm)
	r.Post("/reservations/{id}/cancel", h.reservationCancel)
	r.Post("/reservations/{id}/partial-cancel", h.reservationPartialCancel)
	r.Post("/reservations/{id}/no-show", h.reservationNoShow)
	r.Post("/reservations/{id}/reject", h.reservationReject)
	r.Post("/reservations/{id}/dispute", h.reservationDispute)
	r.Post("/reservations/{id}/resolve-dispute", h.reservationResolveDispute)
	r.Get("/reservations/{id}/risk", h.reservationRiskGet)
	r.Post("/reservations/{id}/risk-review", h.reservationRiskReview)
}
func validReservationCommand(in reservations.Command) bool {
	if in.Version < 1 || !validStaffReason(in.Reason) || len(in.RoomIDs) > 50 || len(in.Rooms) > 50 || len(in.EvidenceReferences) > 10 || !in.ApprovalID.IsZero() && in.ApprovalID.ValidateV7() != nil {
		return false
	}
	seen := map[ids.ID]bool{}
	for _, id := range in.RoomIDs {
		if id.ValidateV7() != nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	for _, v := range in.EvidenceReferences {
		if len(v) < 1 || len(v) > 300 {
			return false
		}
	}
	return true
}

// Every command re-reads the version and permission after acquiring the same
// organisation fence used by inventory and source intake. HTTP preflight is not
// an authority decision inside the owning transaction.
func (h *Handler) reservationCommand(w http.ResponseWriter, r *http.Request, action, cap string, mfa bool, fn func(context.Context, pgx.Tx, tenant, reservations.Reservation, reservations.Command) (string, error)) {
	var in reservations.Command
	if !decode(w, r, &in) {
		return
	}
	if !validReservationCommand(in) || (action != "AMENDED" && len(in.Rooms) > 0) || in.TargetReservationID != nil || in.TargetVersion != 0 || in.DateTo != "" || action != "PARTIAL_CANCELLED" && len(in.RoomIDs) > 0 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current reservation version, bounded room references and reason required."))
		return
	}
	_, pre, e := h.reservationTarget(r, cap, mfa)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "reservation:"+action+":"+pre.ID.String(), in, cap, "PROPERTY", pre.PropertyID, mfa, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, cap, "PROPERTY", pre.PropertyID, mfa)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version || v.AllocationState != "ACTIVE" {
			return 0, nil, apperrors.Conflict("RESERVATION_VERSION_CONFLICT", "Reservation version or allocation ownership changed.")
		}
		event, e := fn(ctx, tx, t, v, in)
		if e != nil {
			return 0, nil, e
		}
		next, e := reservations.Get(ctx, tx, t.Organisation, v.ID)
		if e != nil {
			return 0, nil, e
		}
		if next.Version != v.Version+1 {
			return 0, nil, apperrors.Conflict("RESERVATION_VERSION_CONFLICT", "A command must produce one immutable reservation version.")
		}
		if e = h.reservationCommit(ctx, tx, t, next, action, event, in.Reason, in); e != nil {
			return 0, nil, e
		}
		detail, e := h.reservationDetail(ctx, tx, t.Organisation, v.ID)
		return 200, detail, e
	})
}
func (h *Handler) reservationConfirm(w http.ResponseWriter, r *http.Request) {
	h.reservationCommand(w, r, "CONFIRMED", "reservations:confirm", false, func(ctx context.Context, tx pgx.Tx, t tenant, v reservations.Reservation, in reservations.Command) (string, error) {
		if !reservations.Transition(v.State, "CONFIRMED") {
			return "", apperrors.Conflict("RESERVATION_TRANSITION_DENIED", "Only a pending held reservation can be confirmed.")
		}
		if e := h.commercialGate(ctx, tx, t, v.PropertyID); e != nil {
			return "", e
		}
		if e := h.reservationRiskGate(ctx, tx, t.Organisation, v); e != nil {
			return "", e
		}
		target, e := resolve(ctx, tx, t.Organisation, "PROPERTY", v.PropertyID)
		if e != nil {
			return "", e
		}
		if v.PolicySnapshot.RequireApproval {
			operation := in
			operation.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESERVATION_CREATE", target.Scope, in.ApprovalID, map[string]any{"reservation_id": v.ID, "command": operation}, v.Channel, &v.AmountMinor, v.Currency); e != nil {
				return "", e
			}
		}
		rooms, e := reservations.Rooms(ctx, tx, t.Organisation, v.ID, true)
		if e != nil {
			return "", e
		}
		if len(rooms) == 0 {
			return "", apperrors.Conflict("RESERVATION_ROOMS_REQUIRED", "An active room allocation is required.")
		}
		for _, room := range rooms {
			product, err := inventory.GetProduct(ctx, tx, t.Organisation, room.ProductID)
			if err != nil {
				return "", err
			}
			if product.State != "ACTIVE" || product.PoolID != room.PoolID || len(product.Channels) > 0 && !slices.Contains(product.Channels, v.Channel) {
				return "", apperrors.Conflict("RESERVATION_PRODUCT_RESTRICTED", "Accepted inventory must still have an active supported product and channel.")
			}
			if product.RatePlanID != nil {
				restrictions, err := revenue.Restrictions(ctx, tx, product, *product.RatePlanID, inventory.DateRange{DateFrom: room.DateFrom, DateTo: room.DateTo}, v.Channel)
				if err != nil {
					return "", err
				}
				if err = revenue.CheckRestrictions(restrictions, inventory.DateRange{DateFrom: room.DateFrom, DateTo: room.DateTo}, v.PolicySnapshot.AcceptanceTimezone, room.Adults, room.Children, h.Auth.Service.Now()); err != nil {
					return "", err
				}
			}
			if e = h.reservationFinancialEligibility(ctx, tx, t.Organisation, room.FinancialGroupID); e != nil {
				return "", e
			}

			pool, e := inventory.GetPool(ctx, tx, t.Organisation, room.PoolID)
			if e != nil {
				return "", e
			}
			if e = inventory.CheckStockAuthority(ctx, tx, pool); e != nil {
				return "", e
			}
			if e = reservations.CommitHold(ctx, tx, t.Organisation, t.Account, room, h.Auth.Service.Now().UTC()); e != nil {
				return "", e
			}
		}
		var guarantee bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_rooms r JOIN reservation_financial_groups f ON f.id=r.financial_group_id WHERE r.organisation_id=$1 AND r.reservation_id=$2 AND r.state='ACTIVE' AND f.state='APPROVED_GUARANTEE')`, t.Organisation, v.ID).Scan(&guarantee); e != nil {
			return "", e
		}
		financial := "VERIFIED"
		if guarantee {
			financial = "APPROVED_GUARANTEE"
		}
		_, e = tx.Exec(ctx, `UPDATE reservations SET state='CONFIRMED',financial_state=$5,confirmed_at=$3,hold_expires_at=NULL,version=version+1,updated_at=$3,reason=$4 WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, h.Auth.Service.Now().UTC(), in.Reason, financial)
		return "reservation.modified.v1", e
	})
}
func (h *Handler) reservationRiskGate(ctx context.Context, q database.Querier, org ids.ID, v reservations.Reservation) error {
	var denied, clear bool
	e := q.QueryRow(ctx, `WITH RECURSIVE origins(id) AS (SELECT $2::uuid UNION SELECT l.source_reservation_id FROM origins o JOIN reservation_lineage l ON l.organisation_id=$1 AND l.target_reservation_id=o.id) SELECT EXISTS(SELECT 1 FROM reservation_risk_projection p JOIN origins o ON o.id=p.reservation_id WHERE p.organisation_id=$1 AND p.status IN ('REVIEW','RESTRICTED') AND p.expires_at>$3),NOT EXISTS(SELECT 1 FROM origins o JOIN reservations r ON r.id=o.id AND r.organisation_id=$1 WHERE (r.policy_snapshot->>'require_risk_clear')::boolean AND NOT EXISTS(SELECT 1 FROM reservation_risk_projection p WHERE p.organisation_id=$1 AND p.reservation_id=r.id AND p.status='CLEAR' AND p.expires_at>$3))`, org, v.ID, h.Auth.Service.Now().UTC()).Scan(&denied, &clear)
	if e != nil {
		return e
	}
	if denied || !clear {
		return apperrors.Conflict("RESERVATION_RISK_REVIEW_REQUIRED", "Current reservation risk restrictions require an authorised review.")
	}
	return nil
}
func (h *Handler) reservationCancel(w http.ResponseWriter, r *http.Request) {
	h.reservationReleaseCommand(w, r, "CANCELLED", false)
}
func (h *Handler) reservationPartialCancel(w http.ResponseWriter, r *http.Request) {
	h.reservationReleaseCommand(w, r, "CANCELLED", true)
}
func (h *Handler) reservationReject(w http.ResponseWriter, r *http.Request) {
	h.reservationReleaseCommand(w, r, "REJECTED", false)
}
func (h *Handler) reservationNoShow(w http.ResponseWriter, r *http.Request) {
	h.reservationReleaseCommand(w, r, "NO_SHOW", false)
}
func (h *Handler) reservationReleaseCommand(w http.ResponseWriter, r *http.Request, state string, partial bool) {
	var exitErr error
	r, exitErr = h.reservationExitRequest(r)
	if exitErr != nil {
		reply(w, r, 0, nil, exitErr)
		return
	}
	action := state
	if partial {
		action = "PARTIAL_CANCELLED"
	}
	h.reservationCommand(w, r, action, "reservations:cancel", false, func(ctx context.Context, tx pgx.Tx, t tenant, v reservations.Reservation, in reservations.Command) (string, error) {
		if !reservations.Transition(v.State, state) {
			return "", apperrors.Conflict("RESERVATION_TRANSITION_DENIED", "This lifecycle state cannot release reservation stock through this command.")
		}
		rooms, e := reservations.Rooms(ctx, tx, t.Organisation, v.ID, true)
		if e != nil {
			return "", e
		}
		if partial && (len(in.RoomIDs) == 0 || len(in.RoomIDs) >= len(rooms)) {
			return "", apperrors.InvalidRequest("Partial cancellation requires a proper nonempty subset of room lines; use full cancellation for all rooms.")
		}
		if !partial && len(in.RoomIDs) > 0 {
			return "", apperrors.InvalidRequest("Only partial cancellation accepts room IDs.")
		}
		selected := []reservations.Room{}
		for _, room := range rooms {
			if !partial || slices.Contains(in.RoomIDs, room.ID) {
				selected = append(selected, room)
			}
		}
		if partial && len(selected) != len(in.RoomIDs) {
			return "", apperrors.PermissionDenied("Every selected room must currently belong to this reservation.")
		}
		now := h.Auth.Service.Now().UTC()
		zone := v.PolicySnapshot.AcceptanceTimezone
		if zone == "" {
			if e = tx.QueryRow(ctx, `SELECT timezone FROM inventory_nodes WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.PropertyID).Scan(&zone); e != nil {
				return "", e
			}
		}
		loc, e := time.LoadLocation(zone)
		if e != nil {
			return "", e
		}
		if state == "NO_SHOW" {
			for _, room := range selected {
				arrival, e := time.ParseInLocation("2006-01-02 15:04", room.DateFrom+" "+v.PolicySnapshot.CheckInLocal, loc)
				if e != nil {
					return "", e
				}
				if now.Before(arrival.Add(time.Duration(v.PolicySnapshot.NoShowGraceHours) * time.Hour)) {
					return "", apperrors.Conflict("NO_SHOW_DEADLINE_NOT_REACHED", "Every selected room line must have passed its frozen property-local arrival and no-show grace period.")
				}
			}
		}

		for _, room := range selected {
			from := room.DateFrom
			if state == "NO_SHOW" {
				start, _ := time.Parse(time.DateOnly, from)
				from = start.AddDate(0, 0, v.PolicySnapshot.NoShowRetainNights).Format(time.DateOnly)
				if from > room.DateTo {
					from = room.DateTo
				}
			}
			if e = reservations.Release(ctx, tx, t.Organisation, t.Account, room, from, now); e != nil {
				return "", e
			}
			if state != "NO_SHOW" {
				if _, e = tx.Exec(ctx, `UPDATE reservation_rooms SET state='CANCELLED',version=version+1,updated_at=$3 WHERE organisation_id=$1 AND id=$2`, t.Organisation, room.ID, now); e != nil {
					return "", e
				}
			}
		}
		nextState := state
		amount := v.AmountMinor
		if partial {
			nextState = v.State
			for _, room := range selected {
				amount -= room.AmountMinor
			}
		}
		// The cancelled obligation remains auditable. No refund is inferred from a
		// cancellation: only the Payment-owned refund reference can settle it.
		if _, e = tx.Exec(ctx, `UPDATE reservations SET state=$3,amount_minor=$4,cancelled_at=CASE WHEN $3 IN ('CANCELLED','REJECTED') THEN $5 ELSE cancelled_at END,reason=$6,version=version+1,updated_at=$5,hold_expires_at=CASE WHEN $3 IN ('CANCELLED','REJECTED','NO_SHOW') THEN NULL ELSE hold_expires_at END WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, nextState, amount, now, in.Reason); e != nil {
			return "", e
		}
		if e = h.cancellationObligation(ctx, tx, t, v, selected, action, in.Reason, v.Version+1); e != nil {
			return "", e
		}
		event := "reservation.modified.v1"
		if nextState == "CANCELLED" {
			event = "reservation.cancelled.v1"
		}
		return event, nil
	})
}
func (h *Handler) reservationDispute(w http.ResponseWriter, r *http.Request) {
	h.reservationCommand(w, r, "DISPUTED", "reservations:resolve", true, func(ctx context.Context, tx pgx.Tx, t tenant, v reservations.Reservation, in reservations.Command) (string, error) {
		if !reservations.Transition(v.State, "DISPUTED") {
			return "", apperrors.Conflict("RESERVATION_TRANSITION_DENIED", "Reservation is not disputable in its current state.")
		}
		_, e := tx.Exec(ctx, `UPDATE reservations SET prior_state=state,state='DISPUTED',financial_state='DISPUTED',reason=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, in.Reason)
		return "reservation.modified.v1", e
	})
}
func (h *Handler) reservationResolveDispute(w http.ResponseWriter, r *http.Request) {
	h.reservationCommand(w, r, "DISPUTE_RESOLVED", "reservations:resolve", true, func(ctx context.Context, tx pgx.Tx, t tenant, v reservations.Reservation, in reservations.Command) (string, error) {
		if v.State != "DISPUTED" || v.PriorState == nil {
			return "", apperrors.Conflict("RESERVATION_TRANSITION_DENIED", "A disputed reservation with its retained previous state is required.")
		}
		target, e := resolve(ctx, tx, t.Organisation, "PROPERTY", v.PropertyID)
		if e != nil {
			return "", e
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESERVATION_CONFLICT", target.Scope, in.ApprovalID, map[string]any{"reservation_id": v.ID, "command": op}, v.Channel, &v.AmountMinor, v.Currency); e != nil {
			return "", e
		}
		// Restore only the recorded pre-dispute operational state; payment remains
		// unknown until an owning-service reconciliation supplies fresh evidence.
		_, e = tx.Exec(ctx, `UPDATE reservations SET state=prior_state,prior_state=NULL,financial_state='UNKNOWN',reason=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, in.Reason)
		return "reservation.modified.v1", e
	})
}

type ReservationRiskReview struct {
	Version         int64     `json:"version"`
	Status          string    `json:"status"`
	ReasonCode      string    `json:"reason_code"`
	ReviewReference string    `json:"review_reference"`
	AppealReference string    `json:"appeal_reference,omitempty"`
	ExpiresAt       time.Time `json:"expires_at"`
	ApprovalID      ids.ID    `json:"approval_id"`
	Reason          string    `json:"reason"`
}

func (h *Handler) reservationRiskGet(w http.ResponseWriter, r *http.Request) {
	t, v, e := h.reservationTarget(r, "reservation_risk:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(p) FROM reservation_risk_projection p WHERE organisation_id=$1 AND reservation_id=$2 ORDER BY created_at,id`, t.Organisation, v.ID)
	reply(w, r, 200, map[string]any{"reservation_id": v.ID, "status": v.RiskStatus, "assessments": rows}, e)
}
func (h *Handler) reservationRiskReview(w http.ResponseWriter, r *http.Request) {
	var in ReservationRiskReview
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !slices.Contains([]string{"CLEAR", "REVIEW", "RESTRICTED"}, in.Status) || len(in.ReasonCode) < 1 || len(in.ReasonCode) > 100 || len(in.ReviewReference) < 1 || len(in.ReviewReference) > 300 || len(in.AppealReference) > 300 || !validStaffReason(in.Reason) || !in.ExpiresAt.After(h.Auth.Service.Now()) || in.ExpiresAt.After(h.Auth.Service.Now().Add(24*time.Hour)) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, bounded risk review reference and expiration within 24 hours required."))
		return
	}
	_, pre, e := h.reservationTarget(r, "reservation_risk:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "reservation:risk:"+pre.ID.String(), in, "reservation_risk:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "reservation_risk:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Reservation changed before review.")
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESERVATION_CONFLICT", target.Scope, in.ApprovalID, map[string]any{"reservation_id": v.ID, "risk_review": op}, v.Channel, &v.AmountMinor, v.Currency); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_risk_projection(id,organisation_id,property_id,reservation_id,source_service,source_reference,status,reason_code,review_reference,appeal_reference,expires_at,approval_id,created_by) VALUES($1,$2,$3,$4,'business-operations-service',$5,$6,$7,$5,$8,$9,$10,$11) ON CONFLICT(reservation_id,source_service) DO UPDATE SET source_reference=EXCLUDED.source_reference,status=EXCLUDED.status,reason_code=EXCLUDED.reason_code,review_reference=EXCLUDED.review_reference,appeal_reference=EXCLUDED.appeal_reference,expires_at=EXCLUDED.expires_at,approval_id=EXCLUDED.approval_id,version=reservation_risk_projection.version+1,updated_at=now()`, ids.New(), t.Organisation, v.PropertyID, v.ID, in.ReviewReference, in.Status, in.ReasonCode, in.AppealReference, in.ExpiresAt, in.ApprovalID, t.Account); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE reservations SET risk_status=$3,reason=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, in.Status, in.Reason); e != nil {
			return 0, nil, e
		}
		v, e = reservations.Get(ctx, tx, t.Organisation, v.ID)
		if e != nil {
			return 0, nil, e
		}
		e = h.reservationCommit(ctx, tx, t, v, "RISK_REVIEWED", "reservation.modified.v1", in.Reason, map[string]any{"status": in.Status, "review_reference": in.ReviewReference, "source": "human_review", "approval_id": in.ApprovalID})
		return 200, v, e
	})
}
