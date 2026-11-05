package businesshttp

import (
	"context"
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

type PMSMasterFolioCommand struct {
	PropertyID          ids.ID           `json:"property_id"`
	GroupID             ids.ID           `json:"group_id"`
	FolioIDs            []ids.ID         `json:"folio_ids"`
	FolioVersions       map[string]int64 `json:"folio_versions"`
	Currency            string           `json:"currency"`
	BillingReference    string           `json:"billing_reference"`
	CorporateID         *ids.ID          `json:"corporate_id,omitempty"`
	CorporateContractID *ids.ID          `json:"corporate_contract_id,omitempty"`
	ApprovalID          ids.ID           `json:"approval_id"`
	Reason              string           `json:"reason"`
}
type PMSEventBookingCommand struct {
	PropertyID            ids.ID `json:"property_id"`
	GroupID               ids.ID `json:"group_id"`
	ReservationID         ids.ID `json:"reservation_id"`
	ReservationVersion    int64  `json:"reservation_version"`
	FacilityID            ids.ID `json:"facility_id"`
	SourceReference       string `json:"source_reference"`
	ConversationReference string `json:"conversation_reference,omitempty"`
	Reason                string `json:"reason"`
}

func (h *Handler) pmsMasterFolioCreate(w http.ResponseWriter, r *http.Request) {
	var in PMSMasterFolioCommand
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.GroupID.ValidateV7() != nil || len(in.FolioIDs) < 1 || len(in.FolioIDs) > 100 || len(in.FolioVersions) != len(in.FolioIDs) || !crm.Text(in.BillingReference, 5, 300) || !validStaffReason(in.Reason) || (in.CorporateID == nil) != (in.CorporateContractID == nil) || in.CorporateID != nil && (in.CorporateID.ValidateV7() != nil || in.CorporateContractID.ValidateV7() != nil) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Actual group, current unique folios, one currency and complete billing/contract reference required."))
		return
	}
	seen := map[ids.ID]bool{}
	for _, id := range in.FolioIDs {
		if id.ValidateV7() != nil || seen[id] || in.FolioVersions[id.String()] < 1 {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Unique UUIDv7 folios with exact expected versions required."))
			return
		}
		seen[id] = true
	}
	h.execute(w, r, "pms:master-folio:create", in, "folios:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "folios:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		if _, _, e = h.authorize(ctx, tx, "pms:write", "PROPERTY", in.PropertyID, false); e != nil {
			return 0, nil, e
		}
		g, e := hospitality.Load[PMSGroup](ctx, tx, `SELECT to_jsonb(g) FROM reservation_groups g WHERE organisation_id=$1 AND property_id=$2 AND id=$3`, t.Organisation, in.PropertyID, in.GroupID)
		if e != nil {
			return 0, nil, e
		}
		if in.CorporateID != nil {
			if _, _, e = h.authorize(ctx, tx, "corporate:read", "PROPERTY", in.PropertyID, false); e != nil {
				return 0, nil, e
			}
			if e = crm.RequireCurrentContract(ctx, tx, t.Organisation, in.PropertyID, *in.CorporateID, *in.CorporateContractID, h.Auth.Service.Now()); e != nil {
				return 0, nil, e
			}
		}
		var exposure int64
		for _, id := range in.FolioIDs {
			f, e := hospitality.GetFolio(ctx, tx, t.Organisation, id)
			if e != nil {
				return 0, nil, e
			}
			v, e := reservations.Get(ctx, tx, t.Organisation, f.ReservationID)
			if e != nil {
				return 0, nil, e
			}
			if f.PropertyID != g.PropertyID || v.GroupID == nil || *v.GroupID != g.ID || f.Version != in.FolioVersions[id.String()] || f.Currency != in.Currency || in.CorporateID != nil && (v.CorporateID == nil || *v.CorporateID != *in.CorporateID || v.CorporateContractID == nil || *v.CorporateContractID != *in.CorporateContractID) {
				return 0, nil, apperrors.Conflict("MASTER_FOLIO_BINDING", "Same-property/group/currency and exact accepted payer/folio versions required; payer obligations cannot be reassigned by aggregation.")
			}
			var gross int64
			if e = tx.QueryRow(ctx, `SELECT coalesce(sum(amount_minor),0)::bigint FROM folio_items WHERE organisation_id=$1 AND folio_id=$2`, t.Organisation, id).Scan(&gross); e != nil {
				return 0, nil, e
			}
			if gross < 0 {
				gross = -gross
			}
			exposure, e = addMinor(exposure, gross, in.Currency)
			if e != nil {
				return 0, nil, e
			}
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_GROUP_BILLING", target.Scope, in.ApprovalID, map[string]any{"command": op}, "", &exposure, in.Currency); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO master_folios(id,organisation_id,property_id,group_id,currency,billing_reference,corporate_id,corporate_contract_id,approval_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, t.Organisation, in.PropertyID, g.ID, in.Currency, in.BillingReference, in.CorporateID, in.CorporateContractID, in.ApprovalID, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		for _, folio := range in.FolioIDs {
			if _, e = tx.Exec(ctx, `INSERT INTO master_folio_members(organisation_id,property_id,master_folio_id,folio_id,folio_version) VALUES($1,$2,$3,$4,$5)`, t.Organisation, in.PropertyID, id, folio, in.FolioVersions[folio.String()]); e != nil {
				return 0, nil, e
			}
		}
		if e = h.pmsCommit(ctx, tx, t, in.PropertyID, id, "master_folio", "GROUP_BILLING_ASSOCIATED", "group.booking.updated.v1", 1, map[string]any{"group_id": g.ID, "folio_ids": in.FolioIDs, "currency": in.Currency, "exposure_minor": exposure, "approval_id": in.ApprovalID, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"id": id, "group_id": g.ID, "currency": in.Currency, "source_folio_count": len(in.FolioIDs), "payment_execution": "PAYMENT_SERVICE"}, nil
	})
}
func (h *Handler) pmsMasterFolioGet(w http.ResponseWriter, r *http.Request) {
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
	var property ids.ID
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT property_id FROM master_folios WHERE organisation_id=$1 AND id=$2`, t.Organisation, id).Scan(&property); e != nil {
		if e == pgx.ErrNoRows {
			e = apperrors.NotFound("MASTER_FOLIO_NOT_FOUND", "Master folio not found.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	var out any
	e = database.WithTx(r.Context(), h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, t.Organisation); e != nil {
			return e
		}
		current, _, e := h.authorize(ctx, tx, "folios:read", "PROPERTY", property, false)
		if e != nil {
			return e
		}
		out, e = h.readMasterFolio(ctx, tx, current, id)
		return e
	})
	reply(w, r, 200, out, e)
}
func (h *Handler) readMasterFolio(ctx context.Context, tx pgx.Tx, t tenant, id ids.ID) (any, error) {
	type master struct {
		ID               ids.ID `json:"id"`
		PropertyID       ids.ID `json:"property_id"`
		GroupID          ids.ID `json:"group_id"`
		Currency         string `json:"currency"`
		BillingReference string `json:"billing_reference"`
	}
	m, e := hospitality.Load[master](ctx, tx, `SELECT to_jsonb(m) FROM master_folios m WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if e != nil {
		return nil, e
	}
	rows, e := tx.Query(ctx, `SELECT f.id FROM master_folio_members m JOIN folios f ON f.organisation_id=m.organisation_id AND f.id=m.folio_id WHERE m.organisation_id=$1 AND m.master_folio_id=$2 ORDER BY f.id`, t.Organisation, id)
	if e != nil {
		return nil, e
	}
	sources := []ids.ID{}
	for rows.Next() {
		var source ids.ID
		if e = rows.Scan(&source); e != nil {
			break
		}
		sources = append(sources, source)
	}
	rowErr := rows.Err()
	rows.Close()
	if e == nil {
		e = rowErr
	}
	if e != nil {
		return nil, e
	}
	items := []map[string]any{}
	var total int64
	closed := len(sources) > 0
	now := h.Auth.Service.Now()
	for _, source := range sources {
		f, e := hospitality.GetFolio(ctx, tx, t.Organisation, source)
		if e != nil {
			return nil, e
		}
		balance, e := hospitality.Balance(ctx, tx, t.Organisation, source, now)
		if e != nil {
			return nil, e
		}
		total, e = addMinor(total, balance, m.Currency)
		if e != nil {
			return nil, e
		}
		closed = closed && f.State == "CLOSED" && balance == 0
		items = append(items, map[string]any{"folio_id": source, "state": f.State, "version": f.Version, "balance_minor": balance})
	}
	return map[string]any{"id": m.ID, "group_id": m.GroupID, "currency": m.Currency, "billing_reference": m.BillingReference, "items": items, "balance_minor": total, "all_source_folios_closed": closed, "basis": "CURRENT_SOURCE_FOLIOS_AND_PAYMENT_READBACKS"}, nil
}
func (h *Handler) pmsEventBookingCreate(w http.ResponseWriter, r *http.Request) {
	var in PMSEventBookingCommand
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.GroupID.ValidateV7() != nil || in.ReservationID.ValidateV7() != nil || in.FacilityID.ValidateV7() != nil || in.ReservationVersion < 1 || !crm.OpaqueReference(in.SourceReference) || in.ConversationReference != "" && !crm.OpaqueReference(in.ConversationReference) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Exact group/facility/canonical reservation, version and opaque source/conversation references required."))
		return
	}
	h.execute(w, r, "pms:event:associate:"+in.ReservationID.String(), in, "pms:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		if _, _, e = h.authorize(ctx, tx, "reservations:read", "PROPERTY", in.PropertyID, false); e != nil {
			return 0, nil, e
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, in.ReservationID)
		if e != nil {
			return 0, nil, e
		}
		if v.PropertyID != in.PropertyID || v.GroupID == nil || *v.GroupID != in.GroupID || v.Version != in.ReservationVersion || v.AllocationState != "ACTIVE" || v.State != "HOLD" && v.State != "PENDING_APPROVAL" && v.State != "CONFIRMED" {
			return 0, nil, apperrors.Conflict("EVENT_BOOKING_RESERVATION", "Actual current held/confirmed group facility allocation required.")
		}
		var exact bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM facilities f WHERE f.organisation_id=$1 AND f.id=$3 AND business_graph_property(f.organisation_id,f.id)=$2) AND EXISTS(SELECT 1 FROM reservation_rooms rr JOIN sellable_products p ON p.organisation_id=rr.organisation_id AND p.id=rr.product_id WHERE rr.organisation_id=$1 AND rr.reservation_id=$4 AND rr.state='ACTIVE' AND p.node_id=$3) AND NOT EXISTS(SELECT 1 FROM reservation_rooms rr JOIN sellable_products p ON p.organisation_id=rr.organisation_id AND p.id=rr.product_id WHERE rr.organisation_id=$1 AND rr.reservation_id=$4 AND rr.state='ACTIVE' AND p.node_id<>$3)`, t.Organisation, in.PropertyID, in.FacilityID, v.ID).Scan(&exact); e != nil {
			return 0, nil, e
		}
		if !exact {
			return 0, nil, apperrors.PermissionDenied("Every event allocation must reference the exact same-property canonical facility product.")
		}
		rooms, e := reservations.Rooms(ctx, tx, t.Organisation, v.ID, true)
		if e != nil {
			return 0, nil, e
		}
		for _, room := range rooms {
			pool, e := inventory.GetPool(ctx, tx, t.Organisation, room.PoolID)
			if e != nil {
				return 0, nil, e
			}
			if e = inventory.CheckStockAuthority(ctx, tx, pool); e != nil {
				return 0, nil, e
			}
			if v.State == "CONFIRMED" {
				if e = h.reservationFinancialEligibility(ctx, tx, t.Organisation, room.FinancialGroupID); e != nil {
					return 0, nil, e
				}
			}
		}
		if e = h.reservationRiskGate(ctx, tx, t.Organisation, v); e != nil {
			return 0, nil, e
		}
		if e = h.commercialGate(ctx, tx, t, in.PropertyID); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO event_bookings(id,organisation_id,property_id,group_id,reservation_id,facility_id,source_reference,conversation_reference,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, t.Organisation, in.PropertyID, in.GroupID, v.ID, in.FacilityID, in.SourceReference, in.ConversationReference, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, in.PropertyID, id, "event_booking", "EVENT_BOOKING_ASSOCIATED", "group.booking.updated.v1", 1, map[string]any{"group_id": in.GroupID, "reservation_id": v.ID, "facility_id": in.FacilityID, "reservation_state": v.State, "date_from": v.DateFrom, "date_to": v.DateTo, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"id": id, "reservation_id": v.ID, "reservation_state": v.State, "date_from": v.DateFrom, "date_to": v.DateTo, "capacity_authority": "CANONICAL_RESERVATION", "conversation_verified": false}, nil
	})
}
func (h *Handler) pmsMasterFolios(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "master_folios", "folios:read")
}
func (h *Handler) pmsEventBookings(w http.ResponseWriter, r *http.Request) {
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
	properties, e := h.operationalProperties(r.Context(), h.Auth.Service.Pool, t, "pms:read", r.URL.Query().Get("property_id"), r.URL.Query().Get("branch_id"))
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(e)||jsonb_build_object('reservation_state',r.state,'allocation_state',r.allocation_state,'date_from',r.date_from,'date_to',r.date_to) FROM event_bookings e JOIN reservations r ON r.organisation_id=e.organisation_id AND r.id=e.reservation_id WHERE e.organisation_id=$1 AND e.property_id=ANY($2::uuid[]) ORDER BY e.created_at DESC,e.id DESC LIMIT $3 OFFSET $4`, t.Organisation, properties, limit, offset)
	reply(w, r, 200, map[string]any{"items": rows, "limit": limit, "offset": offset}, e)
}
