package businesshttp

import (
	"context"
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type PMSGroup struct {
	ID               ids.ID `json:"id"`
	OrganisationID   ids.ID `json:"organisation_id"`
	PropertyID       ids.ID `json:"property_id"`
	Name             string `json:"name"`
	State            string `json:"state"`
	SourceReference  string `json:"source_reference"`
	BillingReference string `json:"billing_reference"`
	Version          int64  `json:"version"`
}
type PMSGroupCreate struct {
	PropertyID       ids.ID `json:"property_id"`
	Name             string `json:"name"`
	SourceReference  string `json:"source_reference"`
	BillingReference string `json:"billing_reference"`
	Reason           string `json:"reason"`
}
type PMSGroupPatch struct {
	Version int64  `json:"version"`
	State   string `json:"state"`
	Reason  string `json:"reason"`
}
type PMSGroupBlockCommand struct {
	Booking reservations.Create `json:"booking"`
}
type PMSGroupCheckIns struct {
	Items  []PMSCheckInCommand `json:"items"`
	Reason string              `json:"reason"`
}
type PMSGroupCheckOuts struct {
	Items  []PMSCheckoutCommand `json:"items"`
	Reason string               `json:"reason"`
}

func (h *Handler) registerPMSGroups(r chi.Router) {
	r.Get("/groups", h.pmsGroups)
	r.Post("/groups", h.pmsGroupCreate)
	r.Get("/groups/{id}", h.pmsGroupGet)
	r.Patch("/groups/{id}", h.pmsGroupPatch)
	r.Get("/groups/{id}/blocks", h.pmsGroupBlocks)
	r.Post("/groups/{id}/blocks", h.pmsGroupBlockCreate)
	r.Get("/groups/{id}/rooming-list", h.pmsGroupRooming)
	r.Post("/groups/{id}/rooming-list", h.pmsGroupRoomingSave)
	r.Post("/groups/{id}/check-in", h.pmsGroupCheckIn)
	r.Post("/groups/{id}/check-out", h.pmsGroupCheckOut)
	r.Get("/master-folios", h.pmsMasterFolios)
	r.Post("/master-folios", h.pmsMasterFolioCreate)
	r.Get("/master-folios/{id}", h.pmsMasterFolioGet)
	r.Get("/events", h.pmsEventBookings)
	r.Post("/events", h.pmsEventBookingCreate)
}
func (h *Handler) pmsGroupTarget(r *http.Request, cap string, mfa bool) (tenant, PMSGroup, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, PMSGroup{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, PMSGroup{}, e
	}
	g, e := loadPMSGroup(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		return t, g, e
	}
	t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, cap, "PROPERTY", g.PropertyID, mfa)
	return t, g, e
}
func (h *Handler) pmsGroupCreate(w http.ResponseWriter, r *http.Request) {
	var in PMSGroupCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || !crm.Text(in.Name, 1, 200) || !crm.OpaqueReference(in.SourceReference) || !crm.Text(in.BillingReference, 5, 300) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, actual group name/source/billing reference and reason required."))
		return
	}
	h.execute(w, r, "pms:group:create", in, "pms:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		if e = h.commercialGate(ctx, tx, t, in.PropertyID); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_groups(id,organisation_id,property_id,name,source_reference,billing_reference,created_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, t.Organisation, in.PropertyID, in.Name, in.SourceReference, in.BillingReference, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, in.PropertyID, id, "reservation_group", "GROUP_CREATED", "group.booking.updated.v1", 1, in); e != nil {
			return 0, nil, e
		}
		out, e := loadPMSGroup(ctx, tx, t.Organisation, id)
		return 201, out, e
	})
}
func (h *Handler) pmsGroupGet(w http.ResponseWriter, r *http.Request) {
	t, g, e := h.pmsGroupTarget(r, "pms:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT jsonb_build_object('reservation_id',r.id,'state',r.state,'version',r.version,'date_from',r.date_from,'date_to',r.date_to,'in_house',(SELECT count(*) FROM stay_records s WHERE s.organisation_id=r.organisation_id AND s.reservation_id=r.id AND s.state='ACTIVE'),'departed',(SELECT count(*) FROM stay_records s WHERE s.organisation_id=r.organisation_id AND s.reservation_id=r.id AND s.state='DEPARTED')) FROM reservations r WHERE organisation_id=$1 AND property_id=$2 AND group_id=$3 AND allocation_state='ACTIVE' ORDER BY date_from,id LIMIT $4 OFFSET $5`, t.Organisation, g.PropertyID, g.ID, limit, offset)
	reply(w, r, 200, map[string]any{"group": g, "reservations": rows, "limit": limit, "offset": offset}, e)
}
func (h *Handler) pmsGroupPatch(w http.ResponseWriter, r *http.Request) {
	var in PMSGroupPatch
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || in.State != "CANCELLED" && in.State != "COMPLETED" || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current group version, terminal lifecycle and reason required."))
		return
	}
	_, pre, e := h.pmsGroupTarget(r, "pms:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:group:lifecycle:"+pre.ID.String(), in, "pms:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		g, e := loadPMSGroup(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if g.Version != in.Version || g.State != "ACTIVE" {
			return 0, nil, apperrors.Conflict("GROUP_VERSION_STATE", "Only the current active group can close.")
		}
		var blocked bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservations WHERE organisation_id=$1 AND group_id=$2 AND (allocation_state='ACTIVE' AND (CASE WHEN $3='COMPLETED' THEN state<>'COMPLETED' ELSE state NOT IN ('CANCELLED','REJECTED','EXPIRED','NO_SHOW') END))) OR EXISTS(SELECT 1 FROM reservations r JOIN folios f ON f.organisation_id=r.organisation_id AND f.reservation_id=r.id WHERE r.organisation_id=$1 AND r.group_id=$2 AND f.state<>'CLOSED')`, t.Organisation, g.ID, in.State).Scan(&blocked); e != nil {
			return 0, nil, e
		}
		if blocked {
			return 0, nil, apperrors.Conflict("GROUP_OBLIGATIONS_OUTSTANDING", "Resolve canonical bookings, physical stays and folios before closing the group.")
		}
		if _, e = tx.Exec(ctx, `UPDATE reservation_groups SET state=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, g.ID, in.State); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, g.PropertyID, g.ID, "reservation_group", "GROUP_CLOSED", "group.booking.updated.v1", g.Version+1, in); e != nil {
			return 0, nil, e
		}
		out, e := loadPMSGroup(ctx, tx, t.Organisation, g.ID)
		return 200, out, e
	})
}
func (h *Handler) pmsGroupBlockCreate(w http.ResponseWriter, r *http.Request) {
	var in PMSGroupBlockCommand
	if !decode(w, r, &in) {
		return
	}
	_, pre, e := h.pmsGroupTarget(r, "pms:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Booking.Validate() != nil || in.Booking.GroupID == nil || *in.Booking.GroupID != pre.ID || in.Booking.PropertyID != pre.PropertyID || in.Booking.GroupName != "" || in.Booking.Source != "GROUP" && in.Booking.Source != "CORPORATE" || in.Booking.Source == "CORPORATE" && in.Booking.CorporateID == nil || in.Booking.State != "HOLD" && in.Booking.State != "CONFIRMED" {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Exact existing group/property and complete canonical GROUP/CORPORATE held or confirmed booking required."))
		return
	}
	h.execute(w, r, "pms:group:block:"+pre.ID.String(), in, "pms:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		if _, _, e = h.authorize(ctx, tx, "reservations:write", "PROPERTY", pre.PropertyID, false); e != nil {
			return 0, nil, e
		}
		booking, e := h.createReservationTx(ctx, tx, t, in.Booking, false)
		if e != nil {
			return 0, nil, e
		}
		for _, room := range booking.Rooms {
			id := ids.New()
			if _, e = tx.Exec(ctx, `INSERT INTO group_blocks(id,organisation_id,property_id,group_id,reservation_id,reservation_room_id,quantity,date_from,date_to,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, t.Organisation, pre.PropertyID, pre.ID, booking.Reservation.ID, room.ID, room.Quantity, room.DateFrom, room.DateTo, t.Account); e != nil {
				return 0, nil, e
			}
			if e = h.pmsCommit(ctx, tx, t, pre.PropertyID, id, "group_block", "GROUP_BLOCK_CREATED", "group.booking.updated.v1", 1, map[string]any{"group_id": pre.ID, "reservation_id": booking.Reservation.ID, "reservation_room_id": room.ID, "quantity": room.Quantity, "state": booking.Reservation.State, "reason": in.Booking.Reason}); e != nil {
				return 0, nil, e
			}
		}
		return 201, booking, nil
	})
}
func (h *Handler) pmsGroupBlocks(w http.ResponseWriter, r *http.Request) {
	t, g, e := h.pmsGroupTarget(r, "pms:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(b)||jsonb_build_object('reservation_state',r.state,'room_state',rr.state,'hold_expires_at',r.hold_expires_at,'allocation_state',r.allocation_state) FROM group_blocks b JOIN reservations r ON r.organisation_id=b.organisation_id AND r.id=b.reservation_id JOIN reservation_rooms rr ON rr.organisation_id=b.organisation_id AND rr.id=b.reservation_room_id WHERE b.organisation_id=$1 AND b.property_id=$2 AND b.group_id=$3 ORDER BY b.created_at DESC,b.id DESC LIMIT $4 OFFSET $5`, t.Organisation, g.PropertyID, g.ID, limit, offset)
	reply(w, r, 200, map[string]any{"items": rows, "limit": limit, "offset": offset}, e)
}
func (h *Handler) pmsGroups(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "reservation_groups", "pms:read")
}

func loadPMSGroup(ctx context.Context, q database.Querier, org, id ids.ID) (PMSGroup, error) {
	g, e := hospitality.GetGroup(ctx, q, org, id)
	return PMSGroup(g), e
}
