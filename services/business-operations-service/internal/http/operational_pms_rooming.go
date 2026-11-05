package businesshttp

import (
	"context"
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

type PMSRoomingCommand struct {
	ReservationID         ids.ID `json:"reservation_id"`
	ReservationVersion    int64  `json:"reservation_version"`
	ReservationRoomID     ids.ID `json:"reservation_room_id"`
	Slot                  int    `json:"slot"`
	ExpectedVersion       int64  `json:"expected_version"`
	GuestID               ids.ID `json:"guest_id"`
	SourceKind            string `json:"source_kind"`
	SourceReference       string `json:"source_reference"`
	RegistrationReference string `json:"registration_reference"`
	State                 string `json:"state"`
	ApprovalID            ids.ID `json:"approval_id,omitempty"`
	Reason                string `json:"reason"`
}

func (h *Handler) pmsGroupRoomingSave(w http.ResponseWriter, r *http.Request) {
	var in PMSRoomingCommand
	if !decode(w, r, &in) {
		return
	}
	_, pre, e := h.pmsGroupTarget(r, "pms:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.ReservationID.ValidateV7() != nil || in.ReservationRoomID.ValidateV7() != nil || in.GuestID.ValidateV7() != nil || in.ReservationVersion < 1 || in.ExpectedVersion < 0 || in.Slot < 1 || in.Slot > 1000 || in.State != "ACTIVE" && in.State != "REVOKED" || !validStaffReason(in.Reason) || in.State == "ACTIVE" && (!crm.OpaqueReference(in.SourceReference) || !crm.Text(in.RegistrationReference, 5, 300)) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Exact group reservation/slot, trusted typed guest, current versions, state and actual registration evidence required."))
		return
	}
	h.execute(w, r, "pms:group:rooming:"+pre.ID.String(), in, "pms:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		if _, _, e = h.authorize(ctx, tx, "reservations:modify", "PROPERTY", pre.PropertyID, true); e != nil {
			return 0, nil, e
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, in.ReservationID)
		if e != nil {
			return 0, nil, e
		}
		if v.PropertyID != pre.PropertyID || v.GroupID == nil || *v.GroupID != pre.ID || v.Version != in.ReservationVersion || v.AllocationState != "ACTIVE" || v.State != "CONFIRMED" && v.State != "HOLD" && v.State != "PENDING_APPROVAL" && v.State != "CHECKED_IN" {
			return 0, nil, apperrors.Conflict("ROOMING_RESERVATION_STATE", "Current active canonical group allocation required.")
		}
		var eligible bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_rooms WHERE organisation_id=$1 AND property_id=$2 AND reservation_id=$3 AND id=$4 AND state='ACTIVE' AND quantity>=$5)`, t.Organisation, pre.PropertyID, v.ID, in.ReservationRoomID, in.Slot).Scan(&eligible); e != nil {
			return 0, nil, e
		}
		if !eligible {
			return 0, nil, apperrors.PermissionDenied("Rooming must fit the exact active canonical reservation room and quantity slot.")
		}
		var current int64
		var oldGuest *ids.ID
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0),(array_agg(guest_id ORDER BY version DESC))[1] FROM rooming_lists WHERE organisation_id=$1 AND group_id=$2 AND reservation_room_id=$3 AND slot=$4`, t.Organisation, pre.ID, in.ReservationRoomID, in.Slot).Scan(&current, &oldGuest); e != nil {
			return 0, nil, e
		}
		if current != in.ExpectedVersion {
			return 0, nil, apperrors.Conflict("ROOMING_VERSION", "Rooming slot changed.")
		}
		var occupied bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM room_assignments a JOIN stay_records s ON s.organisation_id=a.organisation_id AND s.assignment_id=a.id WHERE a.organisation_id=$1 AND a.reservation_room_id=$2 AND a.slot=$3)`, t.Organisation, in.ReservationRoomID, in.Slot).Scan(&occupied); e != nil {
			return 0, nil, e
		}
		if occupied {
			return 0, nil, apperrors.Conflict("ROOMING_STAY_STARTED", "Retain actual occupant history after check-in; use the owning stay workflow.")
		}
		var approval any
		if in.State == "ACTIVE" {
			if _, _, e = h.authorize(ctx, tx, "guests:read", "PROPERTY", pre.PropertyID, false); e != nil {
				return 0, nil, e
			}
			if e = crm.RequireIdentityLink(ctx, tx, t.Organisation, pre.PropertyID, in.GuestID, in.SourceKind, h.crmSourceHash(t.Organisation, in.SourceKind, in.SourceReference), h.Auth.Service.Now()); e != nil {
				return 0, nil, e
			}
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_ROOMING_CHANGE", target.Scope, in.ApprovalID, map[string]any{"group_id": pre.ID, "command": op}, "", nil, ""); e != nil {
				return 0, nil, e
			}
			approval = in.ApprovalID
			var existingGuest *ids.ID
			var existingRoom *ids.ID
			e = tx.QueryRow(ctx, `SELECT guest_id,room_id FROM reservation_guests WHERE organisation_id=$1 AND reservation_id=$2 AND source_kind=$3 AND source_reference=$4`, t.Organisation, v.ID, in.SourceKind, in.SourceReference).Scan(&existingGuest, &existingRoom)
			if e == pgx.ErrNoRows {
				_, e = tx.Exec(ctx, `INSERT INTO reservation_guests(id,organisation_id,property_id,reservation_id,room_id,guest_id,source_kind,source_reference,is_primary,registration_reference,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,false,$9,$10)`, ids.New(), t.Organisation, pre.PropertyID, v.ID, in.ReservationRoomID, in.GuestID, in.SourceKind, in.SourceReference, in.RegistrationReference, t.Account)
			} else if e == nil && (existingGuest == nil || *existingGuest != in.GuestID || existingRoom != nil && *existingRoom != in.ReservationRoomID) {
				return 0, nil, apperrors.Conflict("ROOMING_IDENTITY_CONFLICT", "Existing canonical guest binding needs explicit identity/booking review.")
			}
			if e != nil {
				return 0, nil, e
			}
			rooms, e := reservations.Rooms(ctx, tx, t.Organisation, v.ID, true)
			if e != nil {
				return 0, nil, e
			}
			if e = h.reservationCRMEligibility(ctx, tx, t, v, rooms); e != nil {
				return 0, nil, e
			}
		} else if oldGuest == nil || *oldGuest != in.GuestID || !in.ApprovalID.IsZero() {
			return 0, nil, apperrors.InvalidRequest("Revocation retains the exact previous occupant identity and consumes no expansion approval.")
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO rooming_lists(id,organisation_id,property_id,group_id,reservation_id,reservation_room_id,slot,guest_id,state,version,approval_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, id, t.Organisation, pre.PropertyID, pre.ID, v.ID, in.ReservationRoomID, in.Slot, in.GuestID, in.State, current+1, approval, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE reservations SET version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID); e != nil {
			return 0, nil, e
		}
		v, e = reservations.Get(ctx, tx, t.Organisation, v.ID)
		if e != nil {
			return 0, nil, e
		}
		if e = h.reservationCommit(ctx, tx, t, v, "GROUP_ROOMING_UPDATED", "reservation.modified.v1", in.Reason, map[string]any{"group_id": pre.ID, "rooming_id": id, "slot": in.Slot, "guest_id": in.GuestID, "state": in.State, "approval_id": approval}); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, pre.PropertyID, id, "rooming_list", "ROOMING_LIST_VERSION_CREATED", "group.booking.updated.v1", current+1, map[string]any{"group_id": pre.ID, "reservation_id": v.ID, "slot": in.Slot, "guest_id": in.GuestID, "state": in.State, "approval_id": approval, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"id": id, "version": current + 1, "state": in.State, "reservation_version": v.Version}, nil
	})
}
func (h *Handler) pmsGroupRooming(w http.ResponseWriter, r *http.Request) {
	t, g, e := h.pmsGroupTarget(r, "pms:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if _, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "guests:read", "PROPERTY", g.PropertyID, false); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(current) FROM (SELECT DISTINCT ON(reservation_room_id,slot) l.* FROM rooming_lists l WHERE organisation_id=$1 AND property_id=$2 AND group_id=$3 ORDER BY reservation_room_id,slot,version DESC) current ORDER BY reservation_room_id,slot LIMIT $4 OFFSET $5`, t.Organisation, g.PropertyID, g.ID, limit, offset)
	reply(w, r, 200, map[string]any{"items": rows, "limit": limit, "offset": offset}, e)
}
func (h *Handler) pmsGroupCheckIn(w http.ResponseWriter, r *http.Request) {
	var in PMSGroupCheckIns
	if !decode(w, r, &in) {
		return
	}
	if len(in.Items) < 1 || len(in.Items) > 50 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("One to fifty ordered current check-in commands and group reason required."))
		return
	}
	for _, item := range in.Items {
		if !validPMSCheckIn(item) {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Every member must provide a valid current check-in command."))
			return
		}
	}
	_, g, e := h.pmsGroupTarget(r, "pms:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:group:check-in:"+g.ID.String(), in, "pms:write", "PROPERTY", g.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", g.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		items := []any{}
		for _, item := range in.Items {
			a, e := hospitality.GetAssignment(ctx, tx, t.Organisation, item.AssignmentID)
			if e != nil {
				return 0, nil, e
			}
			v, e := reservations.Get(ctx, tx, t.Organisation, a.ReservationID)
			if e != nil {
				return 0, nil, e
			}
			if a.PropertyID != g.PropertyID || v.GroupID == nil || *v.GroupID != g.ID {
				return 0, nil, apperrors.PermissionDenied("Every check-in must belong to the exact authorized group.")
			}
			_, out, e := h.executePMSCheckIn(ctx, tx, a, item)
			if e != nil {
				return 0, nil, e
			}
			items = append(items, out)
		}
		return 201, map[string]any{"group_id": g.ID, "items": items, "atomic": true}, nil
	})
}
func (h *Handler) pmsGroupCheckOut(w http.ResponseWriter, r *http.Request) {
	var in PMSGroupCheckOuts
	if !decode(w, r, &in) {
		return
	}
	if len(in.Items) < 1 || len(in.Items) > 50 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("One to fifty ordered current checkout commands and group reason required."))
		return
	}
	for _, item := range in.Items {
		if item.StayID.ValidateV7() != nil || item.Version < 1 || item.ReservationVersion < 1 || item.FolioVersion < 1 || !validStaffReason(item.Reason) {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Every member must provide current stay/reservation/folio versions."))
			return
		}
	}
	_, g, e := h.pmsGroupTarget(r, "pms:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:group:check-out:"+g.ID.String(), in, "pms:write", "PROPERTY", g.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", g.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		items := []any{}
		for _, item := range in.Items {
			s, e := hospitality.GetStay(ctx, tx, t.Organisation, item.StayID)
			if e != nil {
				return 0, nil, e
			}
			v, e := reservations.Get(ctx, tx, t.Organisation, s.ReservationID)
			if e != nil {
				return 0, nil, e
			}
			if s.PropertyID != g.PropertyID || v.GroupID == nil || *v.GroupID != g.ID {
				return 0, nil, apperrors.PermissionDenied("Every checkout must belong to the exact authorized group.")
			}
			_, out, e := h.executePMSCheckOut(ctx, tx, s, item)
			if e != nil {
				return 0, nil, e
			}
			items = append(items, out)
		}
		return 200, map[string]any{"group_id": g.ID, "items": items, "atomic": true}, nil
	})
}
