package businesshttp

import (
	"context"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

type PMSRoomMoveCommand struct {
	StayID             ids.ID `json:"stay_id"`
	Version            int64  `json:"version"`
	ReservationVersion int64  `json:"reservation_version"`
	AssignmentVersion  int64  `json:"assignment_version"`
	RoomID             ids.ID `json:"room_id"`
	RoomStatusVersion  int64  `json:"room_status_version"`
	Reason             string `json:"reason"`
}

func (h *Handler) pmsRoomMove(w http.ResponseWriter, r *http.Request) {
	var in PMSRoomMoveCommand
	if !decode(w, r, &in) {
		return
	}
	if in.StayID.ValidateV7() != nil || in.RoomID.ValidateV7() != nil || in.Version < 1 || in.ReservationVersion < 1 || in.AssignmentVersion < 1 || in.RoomStatusVersion < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Exact active stay, canonical reservation, physical assignment/destination versions and reason required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	reservationID, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	pre, e := hospitality.GetStay(r.Context(), h.Auth.Service.Pool, t.Organisation, in.StayID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if pre.ReservationID != reservationID {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Stay must belong to the route reservation."))
		return
	}
	h.execute(w, r, "pms:room-move:"+in.StayID.String(), in, "pms:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		s, e := hospitality.GetStay(ctx, tx, t.Organisation, in.StayID)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, s.ReservationID)
		if e != nil {
			return 0, nil, e
		}
		a, e := hospitality.GetAssignment(ctx, tx, t.Organisation, s.AssignmentID)
		if e != nil {
			return 0, nil, e
		}
		if s.State != "ACTIVE" || s.Version != in.Version || a.State != "OCCUPIED" || a.Version != in.AssignmentVersion || v.State != "CHECKED_IN" || v.Version != in.ReservationVersion || a.RoomID == in.RoomID {
			return 0, nil, apperrors.Conflict("ROOM_MOVE_STATE_VERSION", "Current occupied stay and a different inspected destination are required.")
		}
		zone, e := time.LoadLocation(v.PolicySnapshot.AcceptanceTimezone)
		if e != nil {
			return 0, nil, e
		}
		date := h.Auth.Service.Now().In(zone).Format(time.DateOnly)
		if _, e = tx.Exec(ctx, `UPDATE room_assignment_days SET released_at=now() WHERE organisation_id=$1 AND assignment_id=$2 AND day>=$3::date AND released_at IS NULL`, t.Organisation, a.ID, date); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE room_assignments SET state='RELEASED',version=version+1,reason=$3,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, a.ID, in.Reason); e != nil {
			return 0, nil, e
		}
		command := PMSAssignmentCommand{ReservationID: v.ID, ReservationVersion: v.Version, ReservationRoomID: a.ReservationRoomID, RoomID: in.RoomID, RoomStatusVersion: in.RoomStatusVersion, Slot: a.Slot, Reason: in.Reason}
		_, value, e := h.assignPhysicalRoom(ctx, tx, v, command, date)
		if e != nil {
			return 0, nil, e
		}
		next, ok := value.(hospitality.Assignment)
		if !ok {
			return 0, nil, apperrors.Unavailable("Physical assignment result is unavailable.")
		}
		if _, e = tx.Exec(ctx, `UPDATE room_assignments SET state='OCCUPIED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, next.ID); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE stay_records SET assignment_id=$3,version=version+1,reason=$4,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, s.ID, next.ID, in.Reason); e != nil {
			return 0, nil, e
		}
		for _, status := range []struct {
			Room  ids.ID
			State string
		}{{a.RoomID, "VACANT_DIRTY"}, {in.RoomID, "OCCUPIED"}} {
			if _, e = tx.Exec(ctx, `UPDATE room_status_current SET state=CASE WHEN room_id=$5 AND state IN ('BLOCKED','OUT_OF_ORDER') THEN state ELSE $3 END,version=version+1,reason=$4,updated_at=now() WHERE organisation_id=$1 AND room_id=$2`, t.Organisation, status.Room, status.State, in.Reason, a.RoomID); e != nil {
				return 0, nil, e
			}
			row, e := hospitality.Load[hospitality.RoomStatus](ctx, tx, `SELECT to_jsonb(s) FROM room_status_current s WHERE organisation_id=$1 AND room_id=$2`, t.Organisation, status.Room)
			if e != nil {
				return 0, nil, e
			}
			if e = h.pmsCommit(ctx, tx, t, s.PropertyID, row.ID, "room_status", "ROOM_MOVE_STATUS_CHANGED", "room.status.changed.v1", row.Version, map[string]any{"stay_id": s.ID, "room_id": status.Room, "state": row.State, "reason": in.Reason}); e != nil {
				return 0, nil, e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE reservations SET version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID); e != nil {
			return 0, nil, e
		}
		v, e = reservations.Get(ctx, tx, t.Organisation, v.ID)
		if e != nil {
			return 0, nil, e
		}
		if e = h.reservationCommit(ctx, tx, t, v, "ROOM_MOVED", "reservation.modified.v1", in.Reason, in); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, s.PropertyID, a.ID, "room_assignment", "ROOM_MOVE_SOURCE_RELEASED", "hospitality.room.assigned.v1", a.Version+1, in); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, s.PropertyID, next.ID, "room_assignment", "ROOM_MOVE_DESTINATION_OCCUPIED", "hospitality.room.assigned.v1", next.Version+1, in); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, s.PropertyID, s.ID, "stay", "ROOM_MOVED", "hospitality.stay.moved.v1", s.Version+1, map[string]any{"previous_assignment_id": a.ID, "assignment_id": next.ID, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		// An old device grant cannot be reused after a physical move. Adapter work
		// remains separate; UNKNOWN never authorizes unlocking a new room.
		if _, e = h.queuePMSAccess(ctx, tx, t, s.ID, a.ID, "REVOKE", in.Reason); e != nil {
			return 0, nil, e
		}
		if _, e = h.queuePMSAccess(ctx, tx, t, s.ID, next.ID, "ISSUE", in.Reason); e != nil {
			return 0, nil, e
		}
		return 200, map[string]any{"stay_id": s.ID, "assignment_id": next.ID, "version": s.Version + 1, "reservation_version": v.Version, "access_state": "UNKNOWN"}, nil
	})
}
