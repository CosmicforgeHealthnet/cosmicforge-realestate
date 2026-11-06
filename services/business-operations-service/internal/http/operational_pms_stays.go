package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

type PMSCheckInCommand struct {
	AssignmentID          ids.ID `json:"assignment_id"`
	AssignmentVersion     int64  `json:"assignment_version"`
	ReservationVersion    int64  `json:"reservation_version"`
	RoomStatusVersion     int64  `json:"room_status_version"`
	GuestID               ids.ID `json:"guest_id"`
	RegistrationReference string `json:"registration_reference"`
	ApprovalID            ids.ID `json:"approval_id,omitempty"`
	Reason                string `json:"reason"`
}
type PMSCheckoutCommand struct {
	StayID             ids.ID `json:"stay_id"`
	Version            int64  `json:"version"`
	ReservationVersion int64  `json:"reservation_version"`
	FolioVersion       int64  `json:"folio_version"`
	ApprovalID         ids.ID `json:"approval_id,omitempty"`
	Reason             string `json:"reason"`
}
type PMSStayDetail struct {
	Stay         hospitality.Stay       `json:"stay"`
	Assignment   hospitality.Assignment `json:"assignment"`
	Folio        hospitality.Folio      `json:"folio"`
	Items        []hospitality.Item     `json:"items"`
	BalanceMinor int64                  `json:"balance_minor"`
	Access       []json.RawMessage      `json:"access"`
}

func (h *Handler) pmsCheckIn(w http.ResponseWriter, r *http.Request) {
	var in PMSCheckInCommand
	if !decode(w, r, &in) {
		return
	}
	if !validPMSCheckIn(in) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current assignment/reservation/room versions and registered guest evidence required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	pre, e := hospitality.GetAssignment(r.Context(), h.Auth.Service.Pool, t.Organisation, in.AssignmentID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	reservationID, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if reservationID != pre.ReservationID {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Assignment does not belong to the route reservation."))
		return
	}
	h.execute(w, r, "pms:check-in:"+in.AssignmentID.String(), in, "pms:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		return h.executePMSCheckIn(ctx, tx, pre, in)
	})
}
func (h *Handler) executePMSCheckIn(ctx context.Context, tx pgx.Tx, pre hospitality.Assignment, in PMSCheckInCommand) (int, any, error) {
	t, target, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", pre.PropertyID, false)
	if e != nil {
		return 0, nil, e
	}
	if _, _, e = h.authorize(ctx, tx, "guests:read", "PROPERTY", pre.PropertyID, false); e != nil {
		return 0, nil, e
	}
	a, e := hospitality.GetAssignment(ctx, tx, t.Organisation, in.AssignmentID)
	if e != nil {
		return 0, nil, e
	}
	v, e := reservations.Get(ctx, tx, t.Organisation, a.ReservationID)
	if e != nil {
		return 0, nil, e
	}
	if a.Version != in.AssignmentVersion || a.State != "ASSIGNED" || v.Version != in.ReservationVersion || v.AllocationState != "ACTIVE" || v.State != "CONFIRMED" && v.State != "CHECKED_IN" {
		return 0, nil, apperrors.Conflict("CHECK_IN_VERSION_STATE", "Current confirmed reservation and unoccupied assignment required.")
	}
	if e = h.commercialGate(ctx, tx, t, a.PropertyID); e != nil {
		return 0, nil, e
	}
	if e = pmsPhysicalRoom(ctx, tx, t.Organisation, a.PropertyID, a.RoomID); e != nil {
		return 0, nil, e
	}
	rooms, e := reservations.Rooms(ctx, tx, t.Organisation, v.ID, true)
	if e != nil {
		return 0, nil, e
	}
	if e = h.reservationCRMEligibility(ctx, tx, t, v, rooms); e != nil {
		return 0, nil, e
	}
	var room reservations.Room
	for _, rv := range rooms {
		if rv.ID == a.ReservationRoomID {
			room = rv
		}
	}
	if room.ID.IsZero() {
		return 0, nil, apperrors.Conflict("CHECK_IN_ALLOCATION_CHANGED", "The physical assignment must still own the active canonical allocation.")
	}
	if v.GroupID != nil {
		var rostered bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rooming_lists l JOIN reservation_groups g ON g.organisation_id=l.organisation_id AND g.id=l.group_id WHERE l.organisation_id=$1 AND l.group_id=$2 AND l.reservation_room_id=$3 AND l.slot=$4 AND l.version=(SELECT max(latest.version) FROM rooming_lists latest WHERE latest.organisation_id=l.organisation_id AND latest.group_id=l.group_id AND latest.reservation_room_id=l.reservation_room_id AND latest.slot=l.slot) AND l.state='ACTIVE' AND l.guest_id=$5 AND g.state='ACTIVE')`, t.Organisation, *v.GroupID, room.ID, a.Slot, in.GuestID).Scan(&rostered); e != nil {
			return 0, nil, e
		}
		if !rostered {
			return 0, nil, apperrors.Conflict("GROUP_ROOMING_REQUIRED", "The current independently reviewed group rooming slot must authorize this exact guest.")
		}
	}
	if e = reservations.RequirePhysicalBacking(ctx, tx, t.Organisation, room.ID, inventory.DateRange{DateFrom: a.DateFrom, DateTo: a.DateTo}); e != nil {
		return 0, nil, e
	}
	if e = h.reservationFinancialEligibility(ctx, tx, t.Organisation, room.FinancialGroupID); e != nil {
		return 0, nil, e
	}
	if e = h.reservationRiskGate(ctx, tx, t.Organisation, v); e != nil {
		return 0, nil, e
	}
	if e = crm.RequireGuest(ctx, tx, t.Organisation, a.PropertyID, in.GuestID); e != nil {
		return 0, nil, e
	}
	var bound, ready bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_guests g WHERE g.organisation_id=$1 AND g.reservation_id=$2 AND g.guest_id=$3 AND (g.room_id IS NULL OR g.room_id=$4)),EXISTS(SELECT 1 FROM room_status_current s WHERE s.organisation_id=$1 AND s.room_id=$5 AND s.property_id=$6 AND s.version=$7 AND s.state IN ('VACANT_CLEAN','INSPECTED') AND NOT EXISTS(SELECT 1 FROM room_assignments other WHERE other.organisation_id=$1 AND other.room_id=$5 AND other.state='OCCUPIED'))`, t.Organisation, v.ID, in.GuestID, room.ID, a.RoomID, a.PropertyID, in.RoomStatusVersion).Scan(&bound, &ready); e != nil {
		return 0, nil, e
	}
	if !bound || !ready {
		return 0, nil, apperrors.Conflict("CHECK_IN_READINESS", "Registered reservation guest and current unoccupied inspected physical room required.")
	}
	now := h.Auth.Service.Now().UTC()
	arrival, e := hospitality.LocalInstant(a.DateFrom, v.PolicySnapshot.CheckInLocal, v.PolicySnapshot.AcceptanceTimezone)
	if e != nil {
		return 0, nil, e
	}
	loc, e := time.LoadLocation(v.PolicySnapshot.AcceptanceTimezone)
	if e != nil {
		return 0, nil, e
	}
	if now.In(loc).Format(time.DateOnly) < a.DateFrom {
		return 0, nil, apperrors.Conflict("CHECK_IN_DATES_REQUIRE_AMENDMENT", "Accept the earlier canonical inventory dates before early arrival.")
	}
	if now.In(loc).Format(time.DateOnly) >= a.DateTo {
		return 0, nil, apperrors.Conflict("CHECK_IN_STAY_EXPIRED", "Arrival cannot start after the accepted departure date.")
	}
	if now.Before(arrival) {
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_EARLY_CHECK_IN", target.Scope, in.ApprovalID, map[string]any{"command": op}, "", nil, ""); e != nil {
			return 0, nil, e
		}
	}
	day, e := h.pmsDay(ctx, tx, t, a.PropertyID, true)
	if e != nil {
		return 0, nil, e
	}
	stay, folio := ids.New(), ids.New()
	if _, e = tx.Exec(ctx, `UPDATE room_assignments SET state='OCCUPIED',version=version+1,reason=$3,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, a.ID, in.Reason); e != nil {
		return 0, nil, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO stay_records(id,organisation_id,property_id,reservation_id,assignment_id,guest_id,state,registration_reference,checked_in_at,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,'ACTIVE',$7,$8,$9,$10)`, stay, t.Organisation, a.PropertyID, v.ID, a.ID, in.GuestID, in.RegistrationReference, now, in.Reason, t.Account); e != nil {
		return 0, nil, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO folios(id,organisation_id,property_id,reservation_id,stay_id,state,currency,created_by) VALUES($1,$2,$3,$4,$5,'OPEN',$6,$7)`, folio, t.Organisation, a.PropertyID, v.ID, stay, room.Currency, t.Account); e != nil {
		return 0, nil, e
	}
	// Partition the frozen gross obligation by accepted quantity. Remainders go
	// to the first slots; the sum equals the exact quote, with no repricing.
	amount := room.AmountMinor / int64(room.Quantity)
	if int64(a.Slot) <= room.AmountMinor%int64(room.Quantity) {
		amount++
	}
	acceptedItemID := ids.New()
	if _, e = tx.Exec(ctx, `INSERT INTO folio_items(id,organisation_id,property_id,folio_id,kind,source_reference,quantity,amount_minor,currency,policy_snapshot,reason,created_by,business_day_id) VALUES($1,$2,$3,$4,'ACCOMMODATION',$5,1,$6,$7,$8,$9,$10,$11)`, acceptedItemID, t.Organisation, a.PropertyID, folio, "accepted-reservation-slot:"+a.ID.String(), amount, room.Currency, marshalCommercial(map[string]any{"accepted_quote": room.Snapshot, "reservation_room_id": room.ID, "slot": a.Slot, "quantity": room.Quantity, "gross_includes_accepted_components": true}), in.Reason, t.Account, day.ID); e != nil {
		return 0, nil, e
	}
	grossItem, e := hospitality.Load[hospitality.Item](ctx, tx, `SELECT to_jsonb(i) FROM folio_items i WHERE organisation_id=$1 AND id=$2`, t.Organisation, acceptedItemID)
	if e != nil {
		return 0, nil, e
	}
	initialFolio := hospitality.Folio{ID: folio, OrganisationID: t.Organisation, PropertyID: a.PropertyID, ReservationID: v.ID, StayID: stay, State: "OPEN", Currency: room.Currency, Version: 1}
	detail, _, e := h.acceptedFolioFiscal(ctx, tx, initialFolio, grossItem)
	if e != nil {
		return 0, nil, e
	}
	if _, e = h.captureFolioFiscal(ctx, tx, t, initialFolio, grossItem, detail, "ACCEPTED_QUOTE", ids.Nil, "", in.Reason, false); e != nil {
		return 0, nil, e
	}

	if _, e = tx.Exec(ctx, `UPDATE room_status_current SET state='OCCUPIED',reason=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND room_id=$2`, t.Organisation, a.RoomID, in.Reason); e != nil {
		return 0, nil, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO access_grant_references(id,organisation_id,property_id,stay_id,assignment_id,operation,state) VALUES($1,$2,$3,$4,$5,'ISSUE','UNKNOWN')`, ids.New(), t.Organisation, a.PropertyID, stay, a.ID); e != nil {
		return 0, nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE reservations SET state='CHECKED_IN',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID); e != nil {
		return 0, nil, e
	}
	v, e = reservations.Get(ctx, tx, t.Organisation, v.ID)
	if e != nil {
		return 0, nil, e
	}
	if e = h.reservationCommit(ctx, tx, t, v, "CHECKED_IN", "reservation.checked_in.v1", in.Reason, map[string]any{"stay_id": stay, "assignment_id": a.ID}); e != nil {
		return 0, nil, e
	}
	status, e := hospitality.Load[hospitality.RoomStatus](ctx, tx, `SELECT to_jsonb(s) FROM room_status_current s WHERE organisation_id=$1 AND room_id=$2`, t.Organisation, a.RoomID)
	if e != nil {
		return 0, nil, e
	}
	if e = h.pmsCommit(ctx, tx, t, a.PropertyID, status.ID, "room_status", "ROOM_OCCUPIED", "room.status.changed.v1", status.Version, map[string]any{"stay_id": stay, "room_id": a.RoomID, "state": "OCCUPIED", "reason": in.Reason}); e != nil {
		return 0, nil, e
	}
	if e = h.pmsCommit(ctx, tx, t, a.PropertyID, folio, "folio", "FOLIO_OPENED", "hospitality.folio.updated.v1", 1, map[string]any{"stay_id": stay, "amount_minor": amount, "currency": room.Currency, "reason": in.Reason}); e != nil {
		return 0, nil, e
	}
	if e = h.pmsCommit(ctx, tx, t, a.PropertyID, a.ID, "room_assignment", "ROOM_OCCUPIED", "hospitality.room.assigned.v1", a.Version+1, map[string]any{"stay_id": stay, "reason": in.Reason}); e != nil {
		return 0, nil, e
	}
	if e = h.pmsCommit(ctx, tx, t, a.PropertyID, stay, "stay", "CHECKED_IN", "hospitality.stay.checked_in.v1", 1, map[string]any{"reservation_id": v.ID, "assignment_id": a.ID, "guest_id": in.GuestID, "folio_id": folio, "reason": in.Reason}); e != nil {
		return 0, nil, e
	}
	return 201, map[string]any{"stay_id": stay, "folio_id": folio, "reservation_version": v.Version, "access_state": "UNKNOWN"}, nil

}
func (h *Handler) pmsStayGet(w http.ResponseWriter, r *http.Request) {
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
	s, e := hospitality.GetStay(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if _, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "pms:read", "PROPERTY", s.PropertyID, false); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	// Folio and identity data require their own capabilities, not a role label.
	if _, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "folios:read", "PROPERTY", s.PropertyID, false); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	a, e := hospitality.GetAssignment(r.Context(), h.Auth.Service.Pool, t.Organisation, s.AssignmentID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	f, e := hospitality.Load[hospitality.Folio](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(f) FROM folios f WHERE organisation_id=$1 AND stay_id=$2`, t.Organisation, s.ID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := hospitality.FolioItems(r.Context(), h.Auth.Service.Pool, t.Organisation, f.ID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var balance int64
	if e = func() error {
		var err error
		balance, err = hospitality.Balance(r.Context(), h.Auth.Service.Pool, t.Organisation, f.ID, h.Auth.Service.Now())
		return err
	}(); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	access, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(a) FROM access_grant_references a WHERE organisation_id=$1 AND stay_id=$2 ORDER BY created_at,id`, t.Organisation, s.ID)
	reply(w, r, 200, PMSStayDetail{Stay: s, Assignment: a, Folio: f, Items: items, BalanceMinor: balance, Access: access}, e)
}
func (h *Handler) pmsCheckOut(w http.ResponseWriter, r *http.Request) {
	var in PMSCheckoutCommand
	if !decode(w, r, &in) {
		return
	}
	if in.StayID.ValidateV7() != nil || in.Version < 1 || in.ReservationVersion < 1 || in.FolioVersion < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current stay, reservation and folio versions and reason required."))
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
	id := in.StayID
	pre, e := hospitality.GetStay(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if pre.ReservationID != reservationID {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Stay does not belong to the route reservation."))
		return
	}
	h.execute(w, r, "pms:checkout:"+id.String(), in, "pms:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		return h.executePMSCheckOut(ctx, tx, pre, in)

	})
}
func (h *Handler) executePMSCheckOut(ctx context.Context, tx pgx.Tx, pre hospitality.Stay, in PMSCheckoutCommand) (int, any, error) {
	id := pre.ID

	t, target, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", pre.PropertyID, false)
	if e != nil {
		return 0, nil, e
	}
	if _, _, e = h.authorize(ctx, tx, "folios:write", "PROPERTY", pre.PropertyID, false); e != nil {
		return 0, nil, e
	}
	s, e := hospitality.GetStay(ctx, tx, t.Organisation, id)
	if e != nil {
		return 0, nil, e
	}
	a, e := hospitality.GetAssignment(ctx, tx, t.Organisation, s.AssignmentID)
	if e != nil {
		return 0, nil, e
	}
	v, e := reservations.Get(ctx, tx, t.Organisation, s.ReservationID)
	if e != nil {
		return 0, nil, e
	}
	f, e := hospitality.Load[hospitality.Folio](ctx, tx, `SELECT to_jsonb(f) FROM folios f WHERE organisation_id=$1 AND stay_id=$2`, t.Organisation, id)
	if e != nil {
		return 0, nil, e
	}
	if s.State != "ACTIVE" || s.Version != in.Version || a.State != "OCCUPIED" || v.State != "CHECKED_IN" || v.Version != in.ReservationVersion || f.Version != in.FolioVersion || f.State != "OPEN" && f.State != "SETTLEMENT_PENDING" && f.State != "SETTLED" {
		return 0, nil, apperrors.Conflict("CHECKOUT_STATE_VERSION", "Current active stay, occupied assignment and reviewable folio required.")
	}
	var balance int64
	if e = func() error {
		var err error
		balance, err = hospitality.Balance(ctx, tx, t.Organisation, f.ID, h.Auth.Service.Now())
		return err
	}(); e != nil {
		return 0, nil, e
	}
	fiscalReady, e := hospitality.FiscalReady(ctx, tx, t.Organisation, f.ID)
	if e != nil {
		return 0, nil, e
	}
	state := "CLOSED"
	if !fiscalReady {
		state = "SETTLEMENT_PENDING"
	}
	if balance != 0 {
		op := in
		op.ApprovalID = ids.Nil
		exposure := balance
		if exposure < 0 {
			exposure = -exposure
		}
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_DEPARTURE_DEBT", target.Scope, in.ApprovalID, map[string]any{"stay_id": s.ID, "command": op}, "", &exposure, f.Currency); e != nil {
			return 0, nil, e
		}
		state = "SETTLEMENT_PENDING"
	}
	now := h.Auth.Service.Now().UTC()
	if _, e = tx.Exec(ctx, `UPDATE stay_records SET state='DEPARTED',checked_out_at=$3,reason=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, now, in.Reason); e != nil {
		return 0, nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE room_assignment_days SET released_at=$3 WHERE organisation_id=$1 AND assignment_id=$2 AND released_at IS NULL`, t.Organisation, a.ID, now); e != nil {
		return 0, nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE room_assignments SET state='RELEASED',reason=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, a.ID, in.Reason); e != nil {
		return 0, nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE room_status_current SET state='VACANT_DIRTY',reason=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND room_id=$2`, t.Organisation, a.RoomID, in.Reason); e != nil {
		return 0, nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE folios SET state=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, f.ID, state); e != nil {
		return 0, nil, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO access_grant_references(id,organisation_id,property_id,stay_id,assignment_id,operation,state) VALUES($1,$2,$3,$4,$5,'REVOKE','UNKNOWN')`, ids.New(), t.Organisation, s.PropertyID, id, a.ID); e != nil {
		return 0, nil, e
	}
	var pending bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_rooms rr WHERE rr.organisation_id=$1 AND rr.reservation_id=$2 AND rr.state='ACTIVE' AND rr.quantity<>(SELECT count(*) FROM stay_records s JOIN room_assignments a ON a.organisation_id=s.organisation_id AND a.id=s.assignment_id WHERE s.organisation_id=$1 AND s.reservation_id=$2 AND a.reservation_room_id=rr.id AND s.state='DEPARTED'))`, t.Organisation, v.ID).Scan(&pending); e != nil {
		return 0, nil, e
	}
	next := "CHECKED_OUT"
	if pending {
		next = "CHECKED_IN"
	}
	if _, e = tx.Exec(ctx, `UPDATE reservations SET state=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, next); e != nil {
		return 0, nil, e
	}
	v, e = reservations.Get(ctx, tx, t.Organisation, v.ID)
	if e != nil {
		return 0, nil, e
	}
	if e = h.reservationCommit(ctx, tx, t, v, "CHECKED_OUT", "reservation.checked_out.v1", in.Reason, map[string]any{"stay_id": id, "folio_state": state, "balance_minor": balance}); e != nil {
		return 0, nil, e
	}
	status, e := hospitality.Load[hospitality.RoomStatus](ctx, tx, `SELECT to_jsonb(s) FROM room_status_current s WHERE organisation_id=$1 AND room_id=$2`, t.Organisation, a.RoomID)
	if e != nil {
		return 0, nil, e
	}
	if e = h.pmsCommit(ctx, tx, t, s.PropertyID, status.ID, "room_status", "ROOM_VACATED", "room.status.changed.v1", status.Version, map[string]any{"stay_id": id, "room_id": a.RoomID, "state": "VACANT_DIRTY", "reason": in.Reason}); e != nil {
		return 0, nil, e
	}
	if e = h.pmsCommit(ctx, tx, t, s.PropertyID, a.ID, "room_assignment", "ASSIGNMENT_RELEASED", "hospitality.room.assigned.v1", a.Version+1, map[string]any{"stay_id": id, "reason": in.Reason}); e != nil {
		return 0, nil, e
	}
	if e = h.pmsCommit(ctx, tx, t, s.PropertyID, f.ID, "folio", "FOLIO_DEPARTURE_REVIEWED", "hospitality.folio.updated.v1", f.Version+1, map[string]any{"state": state, "outstanding_minor": balance, "approval_id": in.ApprovalID, "reason": in.Reason}); e != nil {
		return 0, nil, e
	}
	if e = h.pmsCommit(ctx, tx, t, s.PropertyID, id, "stay", "CHECKED_OUT", "hospitality.stay.checked_out.v1", s.Version+1, map[string]any{"reservation_id": v.ID, "folio_id": f.ID, "folio_state": state, "outstanding_minor": balance, "approval_id": in.ApprovalID, "reason": in.Reason}); e != nil {
		return 0, nil, e
	}
	return 200, map[string]any{"stay_id": id, "state": "DEPARTED", "folio_state": state, "balance_minor": balance, "reservation_version": v.Version, "room_state": "VACANT_DIRTY", "access_state": "UNKNOWN"}, nil
}

func validPMSCheckIn(in PMSCheckInCommand) bool {
	return !(in.AssignmentID.ValidateV7() != nil || in.GuestID.ValidateV7() != nil || in.AssignmentVersion < 1 || in.ReservationVersion < 1 || in.RoomStatusVersion < 1 || !crm.Text(in.RegistrationReference, 5, 300) || !validStaffReason(in.Reason))
}
