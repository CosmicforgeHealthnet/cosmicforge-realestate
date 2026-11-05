package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type PMSRoomStatusCommand struct {
	PropertyID        ids.ID `json:"property_id"`
	RoomID            ids.ID `json:"room_id"`
	ExpectedVersion   int64  `json:"expected_version"`
	State             string `json:"state"`
	EvidenceReference string `json:"evidence_reference"`
	Reason            string `json:"reason"`
}
type PMSAssignmentCommand struct {
	ReservationID      ids.ID `json:"reservation_id"`
	ReservationVersion int64  `json:"reservation_version"`
	ReservationRoomID  ids.ID `json:"reservation_room_id"`
	RoomID             ids.ID `json:"room_id"`
	RoomStatusVersion  int64  `json:"room_status_version"`
	Slot               int    `json:"slot"`
	Reason             string `json:"reason"`
}
type PMSVersionCommand struct {
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
}

func (h *Handler) registerPMS(r chi.Router) {
	h.registerPMSDays(r)
	h.registerPMSCashier(r)
	h.registerPMSOffline(r)
	r.Get("/guest-requests", h.pmsGuestRequests)
	r.Post("/guest-requests", h.pmsGuestRequestCreate)
	r.Patch("/guest-requests/{id}", h.pmsGuestRequestPatch)
	r.Get("/front-desk", h.pmsFrontDesk)
	r.Get("/front-desk/arrivals", h.pmsFrontDesk)
	r.Get("/front-desk/departures", h.pmsFrontDesk)
	r.Get("/room-status", h.pmsRoomStatuses)
	r.Put("/room-status/{room_id}", h.pmsRoomStatus)
	r.Get("/room-status/{room_id}", h.pmsRoomStatusGet)
	r.Get("/room-assignments", h.pmsAssignments)
	r.Post("/room-assignments", h.pmsAssign)
	r.Post("/room-assignments/{id}/release", h.pmsAssignmentRelease)
	r.Get("/stays", h.pmsStays)
	r.Get("/stays/{id}", h.pmsStayGet)
	r.Post("/reservations/{id}/check-in", h.pmsCheckIn)
	r.Post("/reservations/{id}/room-move", h.pmsRoomMove)
	r.Post("/reservations/{id}/check-out", h.pmsCheckOut)
	r.Get("/folios", h.pmsFolios)
	r.Get("/folios/{id}", h.pmsFolioGet)
	r.Post("/folios/{id}/items", h.pmsFolioPost)
	r.Post("/folios/{id}/reconcile", h.pmsFolioReconcile)
}
func (h *Handler) pmsCommit(ctx context.Context, tx pgx.Tx, t tenant, property, id ids.ID, kind, action, event string, version int64, change any) error {
	if _, e := tx.Exec(ctx, `INSERT INTO hospitality_history(id,organisation_id,property_id,entity_id,entity_type,entity_version,action,change_context,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, ids.New(), t.Organisation, property, id, kind, version, action, marshalCommercial(change), t.Account); e != nil {
		return e
	}
	return h.commercialCommit(ctx, tx, t, property, id, kind, action, event, version, change)
}
func pmsPhysicalRoom(ctx context.Context, q database.Querier, org, property, room ids.ID) error {
	var ok bool
	e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inventory_nodes n WHERE n.organisation_id=$1 AND n.id=$3 AND n.kind IN ('ROOM','UNIT') AND n.state='ACTIVE' AND business_graph_property(n.organisation_id,n.id)=$2 AND coalesce((n.attributes->>'capacity')::integer,0)>0)`, org, property, room).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return apperrors.PermissionDenied("An active canonical physical room in this property is required.")
	}
	return nil
}
func (h *Handler) pmsRoomStatus(w http.ResponseWriter, r *http.Request) {
	var in PMSRoomStatusCommand
	if !decode(w, r, &in) {
		return
	}
	if chi.URLParam(r, "room_id") != in.RoomID.String() || in.PropertyID.ValidateV7() != nil || in.RoomID.ValidateV7() != nil || in.ExpectedVersion < 0 || !validStaffReason(in.Reason) || !validStaffReason(in.EvidenceReference) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, physical room, current version and operational evidence required."))
		return
	}
	cap := "room_status:write"
	mfa := false
	if hospitality.Ready(in.State) {
		cap = "room_status:inspect"
		mfa = true
	}
	h.execute(w, r, "pms:room-status:"+in.RoomID.String(), in, cap, "PROPERTY", in.PropertyID, mfa, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, cap, "PROPERTY", in.PropertyID, mfa)
		if e != nil {
			return 0, nil, e
		}
		if e = pmsPhysicalRoom(ctx, tx, t.Organisation, in.PropertyID, in.RoomID); e != nil {
			return 0, nil, e
		}
		var oldState string
		var id ids.ID
		var version int64
		e = tx.QueryRow(ctx, `SELECT id,state,version FROM room_status_current WHERE organisation_id=$1 AND room_id=$2`, t.Organisation, in.RoomID).Scan(&id, &oldState, &version)
		if e != nil && e != pgx.ErrNoRows {
			return 0, nil, e
		}
		if version != in.ExpectedVersion || !hospitality.StatusTransition(oldState, in.State) {
			return 0, nil, apperrors.Conflict("ROOM_STATUS_TRANSITION", "Room state or version changed; occupancy is changed only by stay commands.")
		}
		if oldState == "BLOCKED" || oldState == "OUT_OF_ORDER" {
			if _, _, e = h.authorize(ctx, tx, "pms:approve", "PROPERTY", in.PropertyID, true); e != nil {
				return 0, nil, e
			}
		}
		if version == 0 {
			id = ids.New()
			_, e = tx.Exec(ctx, `INSERT INTO room_status_current(id,organisation_id,property_id,room_id,state,evidence_reference,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, in.PropertyID, in.RoomID, in.State, in.EvidenceReference, in.Reason, t.Account)
		} else {
			_, e = tx.Exec(ctx, `UPDATE room_status_current SET state=$3,evidence_reference=$4,reason=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.State, in.EvidenceReference, in.Reason)
		}
		if e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, in.PropertyID, id, "room_status", "ROOM_STATUS_CHANGED", "room.status.changed.v1", version+1, in); e != nil {
			return 0, nil, e
		}
		out, e := hospitality.Load[hospitality.RoomStatus](ctx, tx, `SELECT to_jsonb(s) FROM room_status_current s WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		return 200, out, e
	})
}
func (h *Handler) pmsAssign(w http.ResponseWriter, r *http.Request) {
	var in PMSAssignmentCommand
	if !decode(w, r, &in) {
		return
	}
	if !validPMSAssignment(in) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Exact canonical allocation, physical room and expected versions required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	pre, e := reservations.Get(r.Context(), h.Auth.Service.Pool, t.Organisation, in.ReservationID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:assign:"+pre.ID.String(), in, "pms:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		return h.executePMSAssignment(ctx, tx, pre, in)
	})
}
func (h *Handler) executePMSAssignment(ctx context.Context, tx pgx.Tx, pre reservations.Reservation, in PMSAssignmentCommand) (int, any, error) {
	return h.assignPhysicalRoom(ctx, tx, pre, in, "")
}
func (h *Handler) assignPhysicalRoom(ctx context.Context, tx pgx.Tx, pre reservations.Reservation, in PMSAssignmentCommand, movingFrom string) (int, any, error) {
	t, _, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", pre.PropertyID, false)
	if e != nil {
		return 0, nil, e
	}
	v, e := reservations.Get(ctx, tx, t.Organisation, pre.ID)
	if e != nil {
		return 0, nil, e
	}
	if v.Version != in.ReservationVersion || v.AllocationState != "ACTIVE" || v.State != "CONFIRMED" && v.State != "CHECKED_IN" {
		return 0, nil, apperrors.Conflict("ASSIGNMENT_RESERVATION_STATE", "Current confirmed canonical reservation required.")
	}
	if e = h.commercialGate(ctx, tx, t, v.PropertyID); e != nil {
		return 0, nil, e
	}
	room, e := hospitality.Load[reservations.Room](ctx, tx, `SELECT to_jsonb(r) FROM reservation_rooms r WHERE organisation_id=$1 AND id=$2 AND reservation_id=$3 AND state='ACTIVE'`, t.Organisation, in.ReservationRoomID, v.ID)
	if e != nil {
		return 0, nil, e
	}
	if movingFrom != "" {
		if movingFrom < room.DateFrom || movingFrom >= room.DateTo {
			return 0, nil, apperrors.Conflict("ROOM_MOVE_ACCEPTED_DATES", "Room moves must remain within accepted canonical nights.")
		}
		room.DateFrom = movingFrom
	}
	if in.Slot > room.Quantity {
		return 0, nil, apperrors.InvalidRequest("Assignment slot exceeds accepted quantity.")
	}
	if e = reservations.RequirePhysicalBacking(ctx, tx, t.Organisation, room.ID, inventory.DateRange{DateFrom: room.DateFrom, DateTo: room.DateTo}); e != nil {
		return 0, nil, e
	}
	if e = h.reservationFinancialEligibility(ctx, tx, t.Organisation, room.FinancialGroupID); e != nil {
		return 0, nil, e
	}
	if e = h.reservationRiskGate(ctx, tx, t.Organisation, v); e != nil {
		return 0, nil, e
	}
	if e = pmsPhysicalRoom(ctx, tx, t.Organisation, v.PropertyID, in.RoomID); e != nil {
		return 0, nil, e
	}
	var eligible bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inventory_pool_members m JOIN inventory_nodes n ON n.organisation_id=m.organisation_id AND n.id=m.node_id JOIN room_status_current s ON s.organisation_id=n.organisation_id AND s.room_id=n.id WHERE m.organisation_id=$1 AND m.pool_id=$2 AND m.node_id=$3 AND m.node_version=n.version AND s.property_id=$4 AND s.version=$5 AND s.state IN ('VACANT_CLEAN','INSPECTED') AND (n.attributes->>'capacity')::integer >= $6 AND NOT EXISTS(SELECT 1 FROM room_assignment_days d WHERE d.organisation_id=$1 AND d.room_id=$3 AND d.day>=$7::date AND d.day<$8::date AND d.released_at IS NULL) AND NOT EXISTS(SELECT 1 FROM room_assignments a WHERE a.organisation_id=$1 AND a.reservation_room_id=$9 AND a.slot=$10 AND a.state IN ('ASSIGNED','OCCUPIED')))`, t.Organisation, room.PoolID, in.RoomID, v.PropertyID, in.RoomStatusVersion, room.Adults+room.Children, room.DateFrom, room.DateTo, room.ID, in.Slot).Scan(&eligible)
	if e != nil {
		return 0, nil, e
	}
	if !eligible {
		return 0, nil, apperrors.Conflict("PHYSICAL_ROOM_UNAVAILABLE", "Current inspected compatible room, vacant nights, capacity and unused slot required.")
	}
	id := ids.New()
	_, e = tx.Exec(ctx, `INSERT INTO room_assignments(id,organisation_id,property_id,reservation_id,reservation_room_id,room_id,slot,date_from,date_to,state,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'ASSIGNED',$10,$11)`, id, t.Organisation, v.PropertyID, v.ID, room.ID, in.RoomID, in.Slot, room.DateFrom, room.DateTo, in.Reason, t.Account)
	if e != nil {
		return 0, nil, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO room_assignment_days(organisation_id,assignment_id,room_id,day) SELECT $1,$2,$3,d::date FROM generate_series($4::date,$5::date-1,interval '1 day') d`, t.Organisation, id, in.RoomID, room.DateFrom, room.DateTo)
	if e != nil {
		return 0, nil, e
	}
	if e = h.pmsCommit(ctx, tx, t, v.PropertyID, id, "room_assignment", "ROOM_ASSIGNED", "hospitality.room.assigned.v1", 1, in); e != nil {
		return 0, nil, e
	}
	out, e := hospitality.GetAssignment(ctx, tx, t.Organisation, id)
	return 201, out, e

}
func (h *Handler) pmsAssignmentRelease(w http.ResponseWriter, r *http.Request) {
	var in PMSVersionCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Version and release reason required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	pre, e := hospitality.GetAssignment(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:assignment:release:"+id.String(), in, "pms:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		a, e := hospitality.GetAssignment(ctx, tx, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if a.Version != in.Version || a.State != "ASSIGNED" {
			return 0, nil, apperrors.Conflict("ASSIGNMENT_RELEASE_DENIED", "Only the current unoccupied assignment can be released.")
		}
		if _, e = tx.Exec(ctx, `UPDATE room_assignment_days SET released_at=now() WHERE organisation_id=$1 AND assignment_id=$2 AND released_at IS NULL`, t.Organisation, id); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE room_assignments SET state='RELEASED',reason=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.Reason); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, a.PropertyID, id, "room_assignment", "ASSIGNMENT_RELEASED", "hospitality.room.assigned.v1", a.Version+1, in); e != nil {
			return 0, nil, e
		}
		out, e := hospitality.GetAssignment(ctx, tx, t.Organisation, id)
		return 200, out, e
	})
}

// The table name is a source-code constant selected by the individual handler.
// Property scopes are resolved before pagination; clients cannot choose tables.
func (h *Handler) pmsList(w http.ResponseWriter, r *http.Request, table, cap string) {
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
	properties, e := h.operationalProperties(r.Context(), h.Auth.Service.Pool, t, cap, r.URL.Query().Get("property_id"), r.URL.Query().Get("branch_id"))
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	records, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(v) FROM `+table+` v WHERE organisation_id=$1 AND property_id=ANY($2::uuid[]) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, properties, limit, offset)
	if records == nil {
		records = []json.RawMessage{}
	}
	reply(w, r, 200, map[string]any{"items": records, "limit": limit, "offset": offset}, e)
}
func (h *Handler) pmsRoomStatuses(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "room_status_current", "room_status:read")
}
func (h *Handler) pmsAssignments(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "room_assignments", "pms:read")
}
func (h *Handler) pmsStays(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "stay_records", "pms:read")
}
func (h *Handler) pmsFolios(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "folios", "folios:read")
}

func (h *Handler) pmsRoomStatusGet(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := ids.ParseV7(chi.URLParam(r, "room_id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid room required."))
		return
	}
	out, e := hospitality.Load[hospitality.RoomStatus](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(s) FROM room_status_current s WHERE organisation_id=$1 AND room_id=$2`, t.Organisation, id)
	if e == nil {
		_, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "room_status:read", "PROPERTY", out.PropertyID, false)
	}
	reply(w, r, 200, out, e)
}

func validPMSAssignment(in PMSAssignmentCommand) bool {
	return !(in.ReservationID.ValidateV7() != nil || in.ReservationRoomID.ValidateV7() != nil || in.RoomID.ValidateV7() != nil || in.ReservationVersion < 1 || in.RoomStatusVersion < 1 || in.Slot < 1 || !validStaffReason(in.Reason))
}
