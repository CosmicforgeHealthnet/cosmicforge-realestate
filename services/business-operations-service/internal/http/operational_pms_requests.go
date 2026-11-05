package businesshttp

import (
	"context"
	"net/http"
	"slices"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

type PMSGuestRequestCommand struct {
	StayID            ids.ID `json:"stay_id"`
	Kind              string `json:"kind"`
	EvidenceReference string `json:"evidence_reference"`
	Reason            string `json:"reason"`
}
type PMSGuestRequestUpdate struct {
	Version           int64  `json:"version"`
	State             string `json:"state"`
	EvidenceReference string `json:"evidence_reference"`
	Reason            string `json:"reason"`
}
type PMSGuestRequest struct {
	ID                ids.ID `json:"id"`
	OrganisationID    ids.ID `json:"organisation_id"`
	PropertyID        ids.ID `json:"property_id"`
	StayID            ids.ID `json:"stay_id"`
	Kind              string `json:"kind"`
	State             string `json:"state"`
	EvidenceReference string `json:"evidence_reference"`
	Reason            string `json:"reason"`
	Version           int64  `json:"version"`
}

func (h *Handler) pmsGuestRequestCreate(w http.ResponseWriter, r *http.Request) {
	var in PMSGuestRequestCommand
	if !decode(w, r, &in) {
		return
	}
	if in.StayID.ValidateV7() != nil || !slices.Contains([]string{"HOUSEKEEPING", "MAINTENANCE", "SERVICE", "ACCESSIBILITY", "OTHER"}, in.Kind) || !crm.Text(in.EvidenceReference, 5, 300) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current stay, typed request and source evidence required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	s, e := hospitality.GetStay(r.Context(), h.Auth.Service.Pool, t.Organisation, in.StayID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:guest-request:"+in.StayID.String(), in, "pms:write", "PROPERTY", s.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", s.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		s, e := hospitality.GetStay(ctx, tx, t.Organisation, in.StayID)
		if e != nil {
			return 0, nil, e
		}
		if s.State != "ACTIVE" {
			return 0, nil, apperrors.Conflict("GUEST_REQUEST_STAY_INACTIVE", "New operational requests require an actual in-house stay.")
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO guest_requests(id,organisation_id,property_id,stay_id,kind,state,evidence_reference,reason,created_by) VALUES($1,$2,$3,$4,$5,'OPEN',$6,$7,$8)`, id, t.Organisation, s.PropertyID, s.ID, in.Kind, in.EvidenceReference, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, s.PropertyID, id, "guest_request", "GUEST_REQUEST_OPENED", "hospitality.guest_request.updated.v1", 1, in); e != nil {
			return 0, nil, e
		}
		out, e := hospitality.Load[PMSGuestRequest](ctx, tx, `SELECT to_jsonb(g) FROM guest_requests g WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		return 201, out, e
	})
}
func (h *Handler) pmsGuestRequestPatch(w http.ResponseWriter, r *http.Request) {
	var in PMSGuestRequestUpdate
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !slices.Contains([]string{"ACKNOWLEDGED", "RESOLVED", "CANCELLED"}, in.State) || !crm.Text(in.EvidenceReference, 5, 300) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, explicit outcome and actual execution evidence required."))
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
	pre, e := hospitality.Load[PMSGuestRequest](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(g) FROM guest_requests g WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:guest-request:update:"+id.String(), in, "pms:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		v, e := hospitality.Load[PMSGuestRequest](ctx, tx, `SELECT to_jsonb(g) FROM guest_requests g WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version || !slices.Contains([]string{"OPEN", "ACKNOWLEDGED"}, v.State) || v.State == in.State {
			return 0, nil, apperrors.Conflict("GUEST_REQUEST_TRANSITION", "Only a current unresolved request can transition.")
		}
		if _, e = tx.Exec(ctx, `UPDATE guest_requests SET state=$3,evidence_reference=$4,reason=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.State, in.EvidenceReference, in.Reason); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, v.PropertyID, id, "guest_request", "GUEST_REQUEST_UPDATED", "hospitality.guest_request.updated.v1", v.Version+1, in); e != nil {
			return 0, nil, e
		}
		out, e := hospitality.Load[PMSGuestRequest](ctx, tx, `SELECT to_jsonb(g) FROM guest_requests g WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		return 200, out, e
	})
}
func (h *Handler) pmsGuestRequests(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "guest_requests", "pms:read")
}
