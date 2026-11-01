package businesshttp

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"slices"
	"time"
)

type ReservationConflictView struct {
	ID              ids.ID    `json:"id"`
	PropertyID      ids.ID    `json:"property_id"`
	ConnectionID    *ids.ID   `json:"connection_id,omitempty"`
	ReceiptID       *ids.ID   `json:"receipt_id,omitempty"`
	ReservationID   *ids.ID   `json:"reservation_id,omitempty"`
	SourceReference string    `json:"source_reference"`
	Classification  string    `json:"classification"`
	State           string    `json:"state"`
	Reason          string    `json:"reason"`
	Version         int64     `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
}
type ReservationConflictDecision struct {
	Version            int64    `json:"version"`
	Action             string   `json:"action"`
	ReservationID      *ids.ID  `json:"reservation_id,omitempty"`
	ReservationVersion int64    `json:"reservation_version,omitempty"`
	ApprovalID         ids.ID   `json:"approval_id"`
	Reason             string   `json:"reason"`
	EvidenceReferences []string `json:"evidence_references"`
}

func (h *Handler) registerReservationConflicts(r chi.Router) {
	r.Get("/reservation-conflicts", h.reservationConflicts)
	r.Get("/reservation-conflicts/{id}", h.reservationConflictGet)
	r.Post("/reservation-conflicts/{id}/decision", h.reservationConflictDecision)
}
func (h *Handler) reservationConflictTarget(r *http.Request, cap string, mfa bool) (tenant, ReservationConflictView, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, ReservationConflictView{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, ReservationConflictView{}, e
	}
	v, e := reservations.Load[ReservationConflictView](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(c) FROM reservation_conflicts c WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if e == nil {
		t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, cap, "PROPERTY", v.PropertyID, mfa)
	}
	return t, v, e
}
func (h *Handler) reservationConflicts(w http.ResponseWriter, r *http.Request) {
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
	state := r.URL.Query().Get("state")
	if state != "" && !slices.Contains([]string{"OPEN", "RETRY", "RESOLVED", "REJECTED"}, state) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid conflict state."))
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(c)-'evidence' FROM reservation_conflicts c WHERE organisation_id=$1 AND property_id=ANY($2::uuid[]) AND ($3='' OR state=$3) ORDER BY created_at DESC,id DESC LIMIT $4 OFFSET $5`, t.Organisation, properties, state, limit, offset)
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) reservationConflictGet(w http.ResponseWriter, r *http.Request) {
	t, v, e := h.reservationConflictTarget(r, "reservations:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	history, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(d) FROM reservation_conflict_decisions d WHERE organisation_id=$1 AND conflict_id=$2 ORDER BY created_at,id`, t.Organisation, v.ID)
	reply(w, r, 200, map[string]any{"conflict": v, "decisions": history}, e)
}
func (h *Handler) reservationConflictDecision(w http.ResponseWriter, r *http.Request) {
	var in ReservationConflictDecision
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !slices.Contains([]string{"RETRY", "REJECT", "BIND_REVIEWED"}, in.Action) || in.ApprovalID.ValidateV7() != nil || !validStaffReason(in.Reason) || len(in.EvidenceReferences) < 1 || len(in.EvidenceReferences) > 10 || in.Action == "BIND_REVIEWED" && (in.ReservationID == nil || in.ReservationID.ValidateV7() != nil || in.ReservationVersion < 1) || in.Action != "BIND_REVIEWED" && (in.ReservationID != nil || in.ReservationVersion != 0) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current conflict version, reviewed action, independent approval and evidence required."))
		return
	}
	for _, ref := range in.EvidenceReferences {
		if len(ref) < 1 || len(ref) > 300 {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Bounded evidence references required."))
			return
		}
	}
	_, pre, e := h.reservationConflictTarget(r, "reservations:resolve", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "reservation:conflict:"+pre.ID.String(), in, "reservations:resolve", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "reservations:resolve", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Load[ReservationConflictView](ctx, tx, `SELECT to_jsonb(c) FROM reservation_conflicts c WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version || v.State != "OPEN" && v.State != "RETRY" || v.ConnectionID == nil || v.ReceiptID == nil {
			return 0, nil, apperrors.Conflict("RESERVATION_CONFLICT_STATE", "Only the current unresolved authenticated source receipt can be decided.")
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESERVATION_CONFLICT", target.Scope, in.ApprovalID, map[string]any{"conflict_id": v.ID, "command": op}, "", nil, ""); e != nil {
			return 0, nil, e
		}
		connection, e := h.loadChannelConnection(ctx, tx, t.Organisation, *v.ConnectionID)
		if e != nil {
			return 0, nil, e
		}
		var raw []byte
		var hash, receiptState string
		if e = tx.QueryRow(ctx, `SELECT normalized,payload_hash,state FROM channel_event_receipts WHERE organisation_id=$1 AND connection_id=$2 AND id=$3 FOR UPDATE`, t.Organisation, connection.ID, *v.ReceiptID).Scan(&raw, &hash, &receiptState); e != nil {
			return 0, nil, e
		}
		if receiptState == "ACCEPTED" {
			return 0, nil, apperrors.Conflict("SOURCE_ALREADY_ACCEPTED", "This source receipt already has canonical acceptance.")
		}
		var event distribution.Receipt
		if e = json.Unmarshal(raw, &event); e != nil {
			return 0, nil, e
		}
		state := "REJECTED"
		var canonical any
		if in.Action != "REJECT" {
			var accepted reservations.Acceptance
			if in.Action == "RETRY" {
				accepted, e = h.acceptReservationReceipt(ctx, tx, connection, event, *v.ReceiptID, hash)
			} else {
				accepted, e = h.bindReviewedReservationReceipt(ctx, tx, t, connection, event, *v.ReceiptID, hash, in)
			}
			if e != nil {
				return 0, nil, e
			}
			canonical = accepted.ReservationID
			if _, e = tx.Exec(ctx, `UPDATE channel_event_receipts SET state='ACCEPTED',reason='CANONICAL_RESERVATION_ACCEPTED',processed_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, *v.ReceiptID); e != nil {
				return 0, nil, e
			}
			if _, e = tx.Exec(ctx, `UPDATE channel_reservation_refs SET state='CANONICALLY_ACCEPTED',canonical_reservation_id=$3,acceptance_reference=$4 WHERE organisation_id=$1 AND receipt_id=$2`, t.Organisation, *v.ReceiptID, accepted.ReservationID, accepted.DurableReference); e != nil {
				return 0, nil, e
			}
			state = "RESOLVED"
		}
		if _, e = tx.Exec(ctx, `UPDATE reservation_conflicts SET state=$3,reservation_id=$4,reason=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, state, canonical, in.Reason); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_conflict_decisions(id,organisation_id,property_id,conflict_id,action,reason,approval_id,evidence_references,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, ids.New(), t.Organisation, v.PropertyID, v.ID, in.Action, in.Reason, in.ApprovalID, in.EvidenceReferences, t.Account); e != nil {
			return 0, nil, e
		}
		if e = businessEvents.CommercialCommit(ctx, tx, h.Auth.Service.Outbox, t.Organisation, v.PropertyID, v.ID, t.Account, "reservation_conflict", "RESERVATION_CONFLICT_DECIDED", "", v.Version+1, map[string]any{"action": in.Action, "state": state, "receipt_id": v.ReceiptID, "reservation_id": canonical, "approval_id": in.ApprovalID}); e != nil {
			return 0, nil, e
		}
		return 200, map[string]any{"id": v.ID, "state": state, "version": v.Version + 1, "canonical_reservation_id": canonical, "canonical_reservation_accepted": state == "RESOLVED"}, nil
	})
}
