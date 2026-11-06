package businesshttp

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type PMSRoomReconcileCommand struct {
	ExpectedRoomVersion int64  `json:"expected_room_version"`
	SourceDigest        string `json:"source_digest"`
	Observation         string `json:"observation,omitempty"`
	EvidenceReference   string `json:"evidence_reference,omitempty"`
	ApprovalID          ids.ID `json:"approval_id,omitempty"`
	Reason              string `json:"reason"`
}
type PMSRoomReconcileResult struct {
	Report      hospitality.RoomReconciliationReport `json:"report"`
	RoomVersion int64                                `json:"room_version"`
	RoomState   string                               `json:"room_state"`
	SafetyHold  bool                                 `json:"safety_hold"`
	Changed     bool                                 `json:"changed"`
}

func (h *Handler) loadRoomSafetyProof(ctx context.Context, q database.Querier, org, room ids.ID) (hospitality.RoomSafetyProof, error) {
	proof, e := hospitality.RoomProof(ctx, q, org, room)
	if e != nil {
		return proof, e
	}
	for i := range proof.ActiveStays {
		source := &proof.ActiveStays[i]
		e = reservations.RequirePhysicalBacking(ctx, q, org, source.ReservationRoomID, inventory.DateRange{DateFrom: source.DateFrom, DateTo: source.DateTo})
		if e != nil {
			var app *apperrors.Error
			if !errors.As(e, &app) || app.Status != 409 {
				return proof, e
			}
		} else {
			if e = q.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM reservation_inventory_days d LEFT JOIN inventory_calendar c ON c.organisation_id=d.organisation_id AND c.pool_id=d.pool_id AND c.day=d.day WHERE d.organisation_id=$1 AND d.room_id=$2 AND d.day>=$3::date AND d.day<$4::date AND d.released_at IS NULL AND (c.pool_id IS NULL OR c.committed<d.quantity))`, org, source.ReservationRoomID, source.DateFrom, source.DateTo).Scan(&source.StockBacked); e != nil {
				return proof, e
			}
		}
		if source.AcceptanceTimezone != "" && source.CheckoutLocal != "" {
			due, e := hospitality.LocalInstant(source.DateTo, source.CheckoutLocal, source.AcceptanceTimezone)
			if e == nil {
				source.DepartureDueAt = &due
				source.Overdue = !h.Auth.Service.Now().Before(due)
			}
		}
	}
	return proof, nil
}
func (h *Handler) pmsRoomReconciliationReports(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "room_reconciliation_reports", "pms:read")
}
func (h *Handler) pmsRoomSafetyProof(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	room, e := ids.ParseV7(chi.URLParam(r, "room_id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid canonical room required."))
		return
	}
	// Read under the same source fence; an audit does not expose a mixture of
	// pre-departure and post-departure records from concurrent transactions.
	var result any
	e = database.WithTx(r.Context(), h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, t.Organisation); e != nil {
			return e
		}
		var property ids.ID
		if e := tx.QueryRow(ctx, `SELECT property_id FROM room_status_current WHERE organisation_id=$1 AND room_id=$2`, t.Organisation, room).Scan(&property); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return apperrors.NotFound("ROOM_STATUS_NOT_FOUND", "Room status not found.")
			}
			return e
		}
		t, _, e = h.authorize(ctx, tx, "pms:read", "PROPERTY", property, false)
		if e != nil {
			return e
		}
		proof, e := h.loadRoomSafetyProof(ctx, tx, t.Organisation, room)
		if e != nil {
			return e
		}
		if e = audit(ctx, tx, t, "ROOM_SAFETY_SOURCE_READ", "room", room, map[string]any{"property_id": proof.PropertyID}); e != nil {
			return e
		}
		result = map[string]any{"proof": proof, "source_digest": security.SHA256Hex(marshalCommercial(proof)), "plan": hospitality.PlanRoomReconciliation(proof)}
		return nil
	})
	w.Header().Set("Cache-Control", "no-store")
	reply(w, r, 200, result, e)
}
func (h *Handler) pmsRoomReconcile(w http.ResponseWriter, r *http.Request) {
	var in PMSRoomReconcileCommand
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRoomVersion < 1 || len(in.SourceDigest) != 64 || !validStaffReason(in.Reason) || (in.Observation != "" && in.Observation != "OBSERVED_VACANT_DIRTY") || in.Observation != "" && (!crm.OpaqueReference(in.EvidenceReference) || len(in.EvidenceReference) < 5) || in.Observation == "" && (!in.ApprovalID.IsZero() || in.EvidenceReference != "") {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Exact current room/source versions, supported qualified observation and reason required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	room, e := ids.ParseV7(chi.URLParam(r, "room_id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid canonical room required."))
		return
	}
	pre, e := hospitality.RoomProof(r.Context(), h.Auth.Service.Pool, t.Organisation, room)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if _, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "room_status:inspect", "PROPERTY", pre.PropertyID, true); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:room-reconcile:"+room.String(), in, "pms:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		if _, _, e = h.authorize(ctx, tx, "room_status:inspect", "PROPERTY", pre.PropertyID, true); e != nil {
			return 0, nil, e
		}
		proof, e := h.loadRoomSafetyProof(ctx, tx, t.Organisation, room)
		if e != nil {
			return 0, nil, e
		}
		digest := security.SHA256Hex(marshalCommercial(proof))
		if proof.RoomVersion != in.ExpectedRoomVersion || digest != in.SourceDigest {
			return 0, nil, apperrors.Conflict("ROOM_RECONCILIATION_SOURCE_CHANGED", "Re-read current immutable source references before reconciling.")
		}
		observed := false
		if in.Observation != "" {
			plan := hospitality.PlanRoomReconciliation(proof)
			if proof.RoomState != "OCCUPIED" || proof.ActiveStayCount != 0 || proof.OccupiedAssignmentCount != 0 || len(plan.Issues) != 1 || plan.Issues[0] != "UNPROVEN_PHYSICAL_VACANCY" {
				return 0, nil, apperrors.Conflict("ROOM_VACANCY_OBSERVATION_DENIED", "An observation cannot evict an active stay, clear maintenance or rewrite contradictory source ownership.")
			}
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_OCCUPANCY_CORRECTION", target.Scope, in.ApprovalID, map[string]any{"room_id": room, "command": op}, "", nil, ""); e != nil {
				return 0, nil, e
			}
			if e = h.setReconciledRoomState(ctx, tx, t, proof, "VACANT_DIRTY", "QUALIFIED_VACANCY_OBSERVATION", map[string]any{"source_digest": digest, "approval_id": in.ApprovalID, "evidence_reference": in.EvidenceReference, "reason": in.Reason}); e != nil {
				return 0, nil, e
			}
			observed = true
		}
		out, e := h.reconcilePMSRoom(ctx, tx, t, room, roomReconcileOptions{Reason: in.Reason, Observed: observed, ApprovalID: in.ApprovalID, OriginalProof: &proof})
		if e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO hospitality_room_cursors(organisation_id,room_id,next_check_at) VALUES($1,$2,$3) ON CONFLICT(organisation_id,room_id) DO UPDATE SET next_check_at=excluded.next_check_at,state='READY',consecutive_failures=0,last_error_code=''`, t.Organisation, room, h.Auth.Service.Now().Add(time.Minute)); e != nil {
			return 0, nil, e
		}
		return 200, out, nil
	})
}
func (h *Handler) setReconciledRoomState(ctx context.Context, tx pgx.Tx, t tenant, proof hospitality.RoomSafetyProof, state, action string, evidence any) error {
	tag, e := tx.Exec(ctx, `UPDATE room_status_current SET state=$3,evidence_reference=$4,reason=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND room_id=$2 AND version=$6`, t.Organisation, proof.RoomID, state, "source-reconciliation:"+security.SHA256Hex(marshalCommercial(proof)), action, proof.RoomVersion)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return apperrors.Conflict("ROOM_RECONCILIATION_VERSION", "Room state changed.")
	}
	return h.pmsCommit(ctx, tx, t, proof.PropertyID, proof.StatusID, "room_status", action, "room.status.changed.v1", proof.RoomVersion+1, map[string]any{"room_id": proof.RoomID, "previous_state": proof.RoomState, "previous_version": proof.RoomVersion, "state": state, "source": evidence, "actor_service": serviceActor(t)})
}

type roomReconcileOptions struct {
	Reason        string
	Observed      bool
	ApprovalID    ids.ID
	OriginalProof *hospitality.RoomSafetyProof
}

func (h *Handler) reconcilePMSRoom(ctx context.Context, tx pgx.Tx, t tenant, room ids.ID, options roomReconcileOptions) (out PMSRoomReconcileResult, e error) {
	reason, observed, approval := options.Reason, options.Observed, options.ApprovalID
	proof, e := h.loadRoomSafetyProof(ctx, tx, t.Organisation, room)
	if e != nil {
		return out, e
	}
	original := proof
	if options.OriginalProof != nil {
		original = *options.OriginalProof
	}
	digest := security.SHA256Hex(marshalCommercial(original))
	plan := hospitality.PlanRoomReconciliation(proof)
	releasedIDs := append([]ids.ID{}, plan.ReleaseAssignmentIDs...)
	state := "CONSISTENT"
	changed := observed
	if observed {
		state = "OPERATOR_OBSERVATION"
	}
	if plan.DesiredState != proof.RoomState {
		state = "DERIVED_OCCUPANCY"
		if plan.DesiredState == "VACANT_DIRTY" {
			state = "DERIVED_DEPARTURE"
		}
		if e = h.setReconciledRoomState(ctx, tx, t, proof, plan.DesiredState, "OWNING_STAY_SOURCE_RECONCILED", map[string]any{"source_digest": digest, "active_stay_count": proof.ActiveStayCount, "latest_departure": proof.LatestDeparture}); e != nil {
			return out, e
		}
		changed = true
	}
	for _, id := range plan.ReleaseAssignmentIDs {
		a, e := hospitality.GetAssignment(ctx, tx, t.Organisation, id)
		if e != nil {
			return out, e
		}
		if _, e = tx.Exec(ctx, `UPDATE room_assignment_days SET released_at=$3 WHERE organisation_id=$1 AND assignment_id=$2 AND released_at IS NULL`, t.Organisation, id, h.Auth.Service.Now()); e != nil {
			return out, e
		}
		if _, e = tx.Exec(ctx, `UPDATE room_assignments SET state='RELEASED',version=version+1,updated_at=now(),reason=$3 WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, reason); e != nil {
			return out, e
		}
		if e = h.pmsCommit(ctx, tx, t, a.PropertyID, id, "room_assignment", "TERMINAL_UNUSED_ASSIGNMENT_RELEASED", "hospitality.room.assigned.v1", a.Version+1, map[string]any{"reservation_id": a.ReservationID, "source_digest": digest, "actor_service": serviceActor(t), "reason": reason}); e != nil {
			return out, e
		}
		changed = true
		if state == "CONSISTENT" {
			state = "ASSIGNMENTS_RELEASED"
		}
	}
	current, e := h.loadRoomSafetyProof(ctx, tx, t.Organisation, room)
	if e != nil {
		return out, e
	}
	plan = hospitality.PlanRoomReconciliation(current)
	if len(plan.Issues) > 0 {
		state = "REVIEW_REQUIRED"
		opened, e := h.openRoomSafetyException(ctx, tx, t, current.PropertyID, room, digest, plan.Issues)
		if e != nil {
			return out, e
		}
		changed = changed || opened
	} else {
		resolved, e := h.resolveRoomSafetyExceptions(ctx, tx, t, current.PropertyID, room, security.SHA256Hex(marshalCommercial(current)))
		if e != nil {
			return out, e
		}
		changed = changed || resolved
	}
	report, e := h.recordRoomReconciliation(ctx, tx, t, current.PropertyID, room, digest, state, original, map[string]any{"previous_state": original.RoomState, "room_state": current.RoomState, "room_version": current.RoomVersion, "issues": plan.Issues, "released_assignment_ids": releasedIDs, "observed_vacant_dirty": observed}, approval, reason)
	if e != nil {
		return out, e
	}
	return PMSRoomReconcileResult{Report: report, RoomVersion: current.RoomVersion, RoomState: current.RoomState, SafetyHold: len(plan.Issues) > 0 || current.RoomState == "BLOCKED" || current.RoomState == "OUT_OF_ORDER", Changed: changed}, nil
}
func (h *Handler) recordRoomReconciliation(ctx context.Context, tx pgx.Tx, t tenant, property, room ids.ID, digest, state string, proof, changes any, approval ids.ID, reason string) (hospitality.RoomReconciliationReport, error) {
	id := ids.New()
	tag, e := tx.Exec(ctx, `INSERT INTO room_reconciliation_reports(id,organisation_id,property_id,room_id,source_digest,state,proof,changes,approval_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(organisation_id,room_id,source_digest,state) DO NOTHING`, id, t.Organisation, property, room, digest, state, marshalCommercial(proof), marshalCommercial(changes), approval, reason, t.Account)
	if e != nil {
		return hospitality.RoomReconciliationReport{}, e
	}
	if tag.RowsAffected() == 0 {
		return hospitality.Load[hospitality.RoomReconciliationReport](ctx, tx, `SELECT to_jsonb(r) FROM room_reconciliation_reports r WHERE organisation_id=$1 AND room_id=$2 AND source_digest=$3 AND state=$4`, t.Organisation, room, digest, state)
	}
	if e = h.pmsCommit(ctx, tx, t, property, id, "room_reconciliation_report", "ROOM_SOURCE_RECONCILED", "hospitality.room.reconciled.v1", 1, map[string]any{"room_id": room, "source_digest": digest, "state": state, "changes": changes, "actor_service": serviceActor(t)}); e != nil {
		return hospitality.RoomReconciliationReport{}, e
	}
	return hospitality.GetRoomReconciliationReport(ctx, tx, t.Organisation, id)
}
func (h *Handler) resolveRoomSafetyExceptions(ctx context.Context, tx pgx.Tx, t tenant, property, room ids.ID, digest string) (bool, error) {
	rows, e := tx.Query(ctx, `UPDATE hospitality_operation_exceptions SET state='RESOLVED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND property_id=$2 AND source_type='room' AND source_id=$3 AND code='ROOM_SAFETY_REVIEW_REQUIRED' AND state='OPEN' RETURNING id,version`, t.Organisation, property, room)
	if e != nil {
		return false, e
	}
	type ref struct {
		id      ids.ID
		version int64
	}
	refs := []ref{}
	for rows.Next() {
		var r ref
		if e = rows.Scan(&r.id, &r.version); e != nil {
			break
		}
		refs = append(refs, r)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	if re != nil {
		return false, re
	}
	for _, r := range refs {
		if e = h.pmsCommit(ctx, tx, t, property, r.id, "hospitality_exception", "ROOM_SAFETY_EXCEPTION_RESOLVED", "hospitality.operation.exception.v1", r.version, map[string]any{"room_id": room, "source_digest": digest, "actor_service": serviceActor(t), "reason": "Current owning stay, room and assignment facts are consistent; no cleanliness or money was inferred"}); e != nil {
			return false, e
		}
	}
	return len(refs) > 0, nil
}

func (h *Handler) openRoomSafetyException(ctx context.Context, tx pgx.Tx, t tenant, property, room ids.ID, digest string, issues []string) (bool, error) {
	id := ids.New()
	tag, e := tx.Exec(ctx, `INSERT INTO hospitality_operation_exceptions(id,organisation_id,property_id,source_type,source_id,source_digest,code,state,evidence,created_by) VALUES($1,$2,$3,'room',$4,$5,'ROOM_SAFETY_REVIEW_REQUIRED','OPEN',$6,$7) ON CONFLICT(organisation_id,source_type,source_id,source_digest,code) DO NOTHING`, id, t.Organisation, property, room, digest, marshalCommercial(map[string]any{"issues": issues, "source_digest": digest}), t.Account)
	if e != nil || tag.RowsAffected() == 0 {
		return false, e
	}
	e = h.pmsCommit(ctx, tx, t, property, id, "hospitality_exception", "ROOM_SAFETY_EXCEPTION_OPENED", "hospitality.operation.exception.v1", 1, map[string]any{"room_id": room, "issues": issues, "source_digest": digest, "actor_service": serviceActor(t)})
	return true, e
}
