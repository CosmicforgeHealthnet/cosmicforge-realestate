package businesshttp

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"slices"
	"time"
)

func (h *Handler) registerReservationLineage(r chi.Router) {
	r.Post("/reservations/{id}/split", h.reservationSplit)
	r.Post("/reservations/{id}/merge", h.reservationMerge)
	r.Get("/reservations/{id}/lineage", h.reservationLineageGet)
}
func (h *Handler) reservationLineageGet(w http.ResponseWriter, r *http.Request) {
	t, v, e := h.reservationTarget(r, "reservations:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(l) FROM reservation_lineage l WHERE organisation_id=$1 AND (source_reservation_id=$2 OR target_reservation_id=$2) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, v.ID, limit, offset)
	reply(w, r, 200, map[string]any{"items": rows, "limit": limit, "offset": offset}, e)
}
func (h *Handler) reservationLineage(w http.ResponseWriter, r *http.Request, merge bool) {
	var in reservations.Command
	if !decode(w, r, &in) {
		return
	}
	if !validReservationCommand(in) || len(in.Rooms) > 0 || len(in.RoomIDs) < 1 || merge && (in.TargetReservationID == nil || in.TargetReservationID.ValidateV7() != nil || in.TargetVersion < 1) || !merge && in.TargetReservationID != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current versions, a unique set of room lines, target for merge, and reason required."))
		return
	}
	_, pre, e := h.reservationTarget(r, "reservations:modify", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	action := "SPLIT"
	if merge {
		action = "MERGED"
	}
	h.execute(w, r, "reservation:lineage:"+action+":"+pre.ID.String(), in, "reservations:modify", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "reservations:modify", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version || v.AllocationState != "ACTIVE" || !slices.Contains([]string{"HOLD", "PENDING_PAYMENT", "PENDING_APPROVAL", "CONFIRMED"}, v.State) {
			return 0, nil, apperrors.Conflict("RESERVATION_LINEAGE_STATE", "Only a current active pre-arrival reservation can transfer allocations.")
		}
		if e = h.commercialGate(ctx, tx, t, v.PropertyID); e != nil {
			return 0, nil, e
		}
		if e = h.reservationRiskGate(ctx, tx, t.Organisation, v); e != nil {
			return 0, nil, e
		}
		rooms, e := reservations.Rooms(ctx, tx, t.Organisation, v.ID, true)
		if e != nil {
			return 0, nil, e
		}
		moved := []reservations.Room{}
		amount := int64(0)
		for _, room := range rooms {
			if slices.Contains(in.RoomIDs, room.ID) {
				moved = append(moved, room)
				amount, e = addMinor(amount, room.AmountMinor, v.Currency)
				if e != nil {
					return 0, nil, e
				}
			}
		}
		if len(moved) != len(in.RoomIDs) {
			return 0, nil, apperrors.PermissionDenied("Each selected room must belong to this reservation at its current version.")
		}
		var destination reservations.Reservation
		if merge {
			destination, e = reservations.Get(ctx, tx, t.Organisation, *in.TargetReservationID)
			if e != nil {
				return 0, nil, e
			}
			if destination.ID == v.ID || destination.Version != in.TargetVersion || destination.PropertyID != v.PropertyID || destination.State != v.State || destination.AllocationState != "ACTIVE" || destination.Currency != v.Currency || destination.Channel != v.Channel || destination.PaymentResponsibility != v.PaymentResponsibility || destination.PolicyID != v.PolicyID || hashCommercialPayload(marshalCommercial(destination.PolicySnapshot)) != hashCommercialPayload(marshalCommercial(v.PolicySnapshot)) {
				return 0, nil, apperrors.Conflict("RESERVATION_MERGE_INCOMPATIBLE", "Target version, lifecycle, property, channel, currency and accepted governing terms must match.")
			}
			var sameSource bool
			if e = tx.QueryRow(ctx, `WITH RECURSIVE origins(side,id) AS (VALUES(0,$2::uuid),(1,$3::uuid) UNION SELECT o.side,l.source_reservation_id FROM origins o JOIN reservation_lineage l ON l.organisation_id=$1 AND l.target_reservation_id=o.id), keys AS (SELECT DISTINCT o.side,s.connection_id::text||':'||s.external_reservation_id AS key FROM origins o JOIN reservation_source_refs s ON s.organisation_id=$1 AND s.reservation_id=o.id) SELECT coalesce((SELECT array_agg(key ORDER BY key) FROM keys WHERE side=0),ARRAY[]::text[])=coalesce((SELECT array_agg(key ORDER BY key) FROM keys WHERE side=1),ARRAY[]::text[])`, t.Organisation, v.ID, destination.ID).Scan(&sameSource); e != nil {
				return 0, nil, e
			}
			if !sameSource {
				return 0, nil, apperrors.Conflict("RESERVATION_SOURCE_MERGE_DENIED", "Independent external bookings/connections cannot be merged into one source acceptance. Reconcile their original source contracts separately.")
			}
			if _, _, e = h.authorize(ctx, tx, "reservations:modify", "PROPERTY", destination.PropertyID, true); e != nil {
				return 0, nil, e
			}
			if e = h.reservationRiskGate(ctx, tx, t.Organisation, destination); e != nil {
				return 0, nil, e
			}
		}
		operation := in
		operation.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESERVATION_LINEAGE", target.Scope, in.ApprovalID, map[string]any{"reservation_id": v.ID, "action": action, "command": operation}, v.Channel, &amount, v.Currency); e != nil {
			return 0, nil, e
		}
		if !merge {
			destination = v
			destination.ID = ids.New()
			destination.Version = 1
			destination.DateFrom, destination.DateTo = moved[0].DateFrom, moved[0].DateTo
			for _, room := range moved {
				if room.DateFrom < destination.DateFrom {
					destination.DateFrom = room.DateFrom
				}
				if room.DateTo > destination.DateTo {
					destination.DateTo = room.DateTo
				}
			}
			if _, e = tx.Exec(ctx, `INSERT INTO reservations(id,organisation_id,property_id,source,channel,source_reference,state,group_id,parent_reservation_id,policy_id,policy_snapshot,payment_responsibility,financial_state,amount_minor,currency,date_from,date_to,hold_expires_at,confirmed_at,risk_status,corporate_id,corporate_contract_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::date,$17::date,$18,$19,$20,$21,$22,$23,$24)`, destination.ID, t.Organisation, v.PropertyID, v.Source, v.Channel, v.SourceReference, v.State, v.GroupID, v.ID, v.PolicyID, marshalCommercial(v.PolicySnapshot), v.PaymentResponsibility, v.FinancialState, amount, v.Currency, destination.DateFrom, destination.DateTo, v.HoldExpiresAt, v.ConfirmedAt, v.RiskStatus, v.CorporateID, v.CorporateContractID, in.Reason, t.Account); e != nil {
				return 0, nil, e
			}
			assessments, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(p) FROM reservation_risk_projection p WHERE organisation_id=$1 AND reservation_id=$2 ORDER BY id`, t.Organisation, v.ID)
			if e != nil {
				return 0, nil, e
			}
			for _, raw := range assessments {
				var p struct {
					SourceService   string    `json:"source_service"`
					SourceReference string    `json:"source_reference"`
					Status          string    `json:"status"`
					ReasonCode      string    `json:"reason_code"`
					ReviewReference string    `json:"review_reference"`
					AppealReference string    `json:"appeal_reference"`
					ExpiresAt       time.Time `json:"expires_at"`
					ApprovalID      *ids.ID   `json:"approval_id"`
				}
				if e = json.Unmarshal(raw, &p); e != nil {
					return 0, nil, e
				}
				if _, e = tx.Exec(ctx, `INSERT INTO reservation_risk_projection(id,organisation_id,property_id,reservation_id,source_service,source_reference,status,reason_code,review_reference,appeal_reference,expires_at,approval_id,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, ids.New(), t.Organisation, v.PropertyID, destination.ID, p.SourceService, p.SourceReference, p.Status, p.ReasonCode, p.ReviewReference, p.AppealReference, p.ExpiresAt, p.ApprovalID, t.Account); e != nil {
					return 0, nil, e
				}
			}

		}
		for _, room := range moved {
			if _, e = tx.Exec(ctx, `UPDATE reservation_rooms SET reservation_id=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, room.ID, destination.ID); e != nil {
				return 0, nil, e
			}
		}
		// Explicit room assignments move with the selected line; unassigned group
		// guest references are copied without merging global guest identities.
		if _, e = tx.Exec(ctx, `UPDATE reservation_guests SET reservation_id=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND reservation_id=$2 AND room_id=ANY($4::uuid[])`, t.Organisation, v.ID, destination.ID, in.RoomIDs); e != nil {
			return 0, nil, e
		}
		guests, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(g) FROM reservation_guests g WHERE organisation_id=$1 AND reservation_id=$2 AND room_id IS NULL ORDER BY id`, t.Organisation, v.ID)
		if e != nil {
			return 0, nil, e
		}
		for _, raw := range guests {
			var g reservations.GuestCommand
			if e = json.Unmarshal(raw, &g); e != nil {
				return 0, nil, e
			}
			if g.DocumentReferences == nil {
				g.DocumentReferences = []ids.ID{}
			}
			if _, e = tx.Exec(ctx, `INSERT INTO reservation_guests(id,organisation_id,property_id,reservation_id,guest_id,source_kind,source_reference,is_primary,registration_reference,document_references,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(reservation_id,source_kind,source_reference) DO NOTHING`, ids.New(), t.Organisation, v.PropertyID, destination.ID, g.GuestID, g.SourceKind, g.SourceReference, g.IsPrimary, g.RegistrationReference, g.DocumentReferences, t.Account); e != nil {
				return 0, nil, e
			}
		}
		sourceAllocation := "ACTIVE"
		if len(moved) == len(rooms) {
			sourceAllocation = "SPLIT_SOURCE"
			if merge {
				sourceAllocation = "MERGED_SOURCE"
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE reservations SET allocation_state=$3,amount_minor=$4,date_from=coalesce((SELECT min(date_from) FROM reservation_rooms WHERE organisation_id=$1 AND reservation_id=$2 AND state='ACTIVE'),date_from),date_to=coalesce((SELECT max(date_to) FROM reservation_rooms WHERE organisation_id=$1 AND reservation_id=$2 AND state='ACTIVE'),date_to),reason=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, sourceAllocation, v.AmountMinor-amount, in.Reason); e != nil {
			return 0, nil, e
		}
		if merge {
			if _, e = tx.Exec(ctx, `UPDATE reservations SET amount_minor=(SELECT sum(amount_minor) FROM reservation_rooms WHERE organisation_id=$1 AND reservation_id=$2 AND state='ACTIVE'),date_from=(SELECT min(date_from) FROM reservation_rooms WHERE organisation_id=$1 AND reservation_id=$2 AND state='ACTIVE'),date_to=(SELECT max(date_to) FROM reservation_rooms WHERE organisation_id=$1 AND reservation_id=$2 AND state='ACTIVE'),hold_expires_at=CASE WHEN state='CONFIRMED' THEN NULL ELSE least(hold_expires_at,$3) END,reason=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, destination.ID, v.HoldExpiresAt, in.Reason); e != nil {
				return 0, nil, e
			}
		}
		source, e := reservations.Get(ctx, tx, t.Organisation, v.ID)
		if e != nil {
			return 0, nil, e
		}
		destination, e = reservations.Get(ctx, tx, t.Organisation, destination.ID)
		if e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_lineage(id,organisation_id,property_id,source_reservation_id,target_reservation_id,room_ids,action,source_version,target_version,approval_id,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, ids.New(), t.Organisation, v.PropertyID, v.ID, destination.ID, in.RoomIDs, action, source.Version, destination.Version, in.ApprovalID, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.reservationCommit(ctx, tx, t, source, action, "reservation.modified.v1", in.Reason, map[string]any{"target_reservation_id": destination.ID, "room_ids": in.RoomIDs, "approval_id": in.ApprovalID}); e != nil {
			return 0, nil, e
		}
		event := "reservation.modified.v1"
		if !merge {
			event = "reservation.created.v1"
		}
		if e = h.reservationCommit(ctx, tx, t, destination, action+"_TARGET", event, in.Reason, map[string]any{"source_reservation_id": v.ID, "room_ids": in.RoomIDs, "approval_id": in.ApprovalID}); e != nil {
			return 0, nil, e
		}
		out, e := h.reservationDetail(ctx, tx, t.Organisation, destination.ID)
		return 200, map[string]any{"source": source, "target": out, "action": action}, e
	})
}

func (h *Handler) reservationSplit(w http.ResponseWriter, r *http.Request) {
	h.reservationLineage(w, r, false)
}
func (h *Handler) reservationMerge(w http.ResponseWriter, r *http.Request) {
	h.reservationLineage(w, r, true)
}
