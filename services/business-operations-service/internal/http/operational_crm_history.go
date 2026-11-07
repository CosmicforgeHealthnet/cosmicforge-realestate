package businesshttp

import (
	"context"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/jackc/pgx/v5"
)

type CRMStayHistoryItem struct {
	ProjectionID      ids.ID          `json:"projection_id"`
	ProjectionVersion int64           `json:"projection_version"`
	ProjectionCurrent bool            `json:"projection_current"`
	History           crm.StayHistory `json:"history"`
}
type CRMStayHistoryList struct {
	PendingProjectionCount int64                `json:"pending_projection_count"`
	SourceReviewCount      int64                `json:"source_review_count"`
	Items                  []CRMStayHistoryItem `json:"items"`
	MergedGuestIDs         []ids.ID             `json:"merged_guest_ids,omitempty"`
	ObservedAt             time.Time            `json:"observed_at"`
	Limit                  int                  `json:"limit"`
	Offset                 int                  `json:"offset"`
}
type CRMStayHistoryRebuild struct {
	GuestVersion int64  `json:"guest_version"`
	Reason       string `json:"reason"`
}

func (h *Handler) crmStayHistory(w http.ResponseWriter, r *http.Request) {
	t, g, property, e := h.crmGuestTarget(r, "guests:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out := CRMStayHistoryList{Items: []CRMStayHistoryItem{}, ObservedAt: h.Auth.Service.Now().UTC(), Limit: limit, Offset: offset}
	e = database.WithTx(r.Context(), h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		// The same live tenant/row fence protects all source reads. History contains
		// actual financial references, so each distinct owning capability is needed.
		for _, cap := range []string{"guests:read", "reservations:read", "folios:read"} {
			if _, _, e := h.authorize(ctx, tx, cap, "PROPERTY", property, false); e != nil {
				return e
			}
		}
		if e := crm.RequireGuestScope(ctx, tx, t.Organisation, property, g.ID); e != nil {
			return e
		}
		out.ObservedAt = h.Auth.Service.Now().UTC()
		if e := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE NOT EXISTS(SELECT 1 FROM stay_history_projection p WHERE p.organisation_id=r.organisation_id AND p.reservation_id=r.id AND p.guest_id=$3)),count(*) FILTER(WHERE c.failure_count>0) FROM reservations r LEFT JOIN crm_history_cursors c ON c.organisation_id=r.organisation_id AND c.reservation_id=r.id WHERE r.organisation_id=$1 AND r.property_id=$2 AND (EXISTS(SELECT 1 FROM reservation_guests g WHERE g.organisation_id=r.organisation_id AND g.reservation_id=r.id AND g.guest_id=$3) OR EXISTS(SELECT 1 FROM stay_records s WHERE s.organisation_id=r.organisation_id AND s.reservation_id=r.id AND s.guest_id=$3))`, t.Organisation, property, g.ID).Scan(&out.PendingProjectionCount, &out.SourceReviewCount); e != nil {
			return e
		}
		family, e := guestFamily(ctx, tx, t.Organisation, g.ID)
		if e != nil {
			return e
		}
		if len(family) > 1 {
			// Merged records keep their original immutable keys; history for the
			// whole merge family is reloaded from canonical facts, one per booking.
			out.MergedGuestIDs = family
			items, e := h.crmFamilyHistory(ctx, tx, t.Organisation, property, family, limit, offset, out.ObservedAt)
			if e != nil {
				return e
			}
			out.Items = items
			_, e = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,organisation_id,action,target_type,target_id,change_context,correlation_id) VALUES($1,$2,$3,'GUEST_STAY_HISTORY_READ','guest',$4,$5,$6)`, ids.New(), t.Account, t.Organisation, g.ID, marshalCommercial(map[string]any{"property_id": property, "returned": len(out.Items), "purpose": "BUSINESS_STAY_OPERATIONS", "merge_family": family}), requestcontext.From(ctx).RequestID)
			return e
		}
		refs, e := crm.ListStayHistoryRecords(ctx, tx, t.Organisation, property, g.ID, int32(limit), int32(offset))
		if e != nil {
			return e
		}
		for _, v := range refs {
			// Projection lag or a later Payment expiry must never expose stale spend.
			fact, e := crm.CurrentStayHistory(ctx, tx, t.Organisation, v.Snapshot.ReservationID, g.ID, out.ObservedAt)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, CRMStayHistoryItem{ProjectionID: v.ID, ProjectionVersion: v.Version, ProjectionCurrent: fact.SourceDigest == v.SourceDigest, History: fact})
		}
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,organisation_id,action,target_type,target_id,change_context,correlation_id) VALUES($1,$2,$3,'GUEST_STAY_HISTORY_READ','guest',$4,$5,$6)`, ids.New(), t.Account, t.Organisation, g.ID, marshalCommercial(map[string]any{"property_id": property, "returned": len(out.Items), "purpose": "BUSINESS_STAY_OPERATIONS"}), requestcontext.From(ctx).RequestID)
		return e
	})
	w.Header().Set("Cache-Control", "no-store")
	reply(w, r, 200, out, e)
}

func (h *Handler) crmStayHistoryRebuild(w http.ResponseWriter, r *http.Request) {
	var in CRMStayHistoryRebuild
	if !decode(w, r, &in) {
		return
	}
	if in.GuestVersion < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current guest version and rebuild reason required."))
		return
	}
	_, g, property, e := h.crmGuestTarget(r, "guests:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:history:rebuild:"+g.ID.String()+":"+property.String(), in, "guests:write", "PROPERTY", property, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guests:write", "PROPERTY", property, true)
		if e != nil {
			return 0, nil, e
		}
		if _, _, e = h.authorize(ctx, tx, "reservations:read", "PROPERTY", property, false); e != nil {
			return 0, nil, e
		}
		current, e := crm.GetGuest(ctx, tx, t.Organisation, g.ID)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.GuestVersion {
			return 0, nil, apperrors.Conflict("GUEST_VERSION", "Guest identity changed before rebuilding history.")
		}
		if e = crm.RequireGuestScope(ctx, tx, t.Organisation, property, g.ID); e != nil {
			return 0, nil, e
		}
		rows, e := tx.Query(ctx, `SELECT r.id FROM reservations r WHERE r.organisation_id=$1 AND r.property_id=$2 AND (EXISTS(SELECT 1 FROM reservation_guests g WHERE g.organisation_id=r.organisation_id AND g.reservation_id=r.id AND g.guest_id=$3) OR EXISTS(SELECT 1 FROM stay_records s WHERE s.organisation_id=r.organisation_id AND s.reservation_id=r.id AND s.guest_id=$3)) ORDER BY r.id LIMIT 501`, t.Organisation, property, g.ID)
		if e != nil {
			return 0, nil, e
		}
		var refs []ids.ID
		for rows.Next() {
			var id ids.ID
			if e = rows.Scan(&id); e != nil {
				break
			}
			refs = append(refs, id)
		}
		re := rows.Err()
		rows.Close()
		if e != nil {
			return 0, nil, e
		}
		if re != nil {
			return 0, nil, re
		}
		if len(refs) > 500 {
			return 0, nil, apperrors.Conflict("HISTORY_REBUILD_BATCH_REQUIRED", "Large guest histories must use the bounded maintenance reconciler.")
		}
		projector := crm.HistoryProjector{Pool: h.Auth.Service.Pool, Outbox: h.Auth.Service.Outbox, Now: h.Auth.Service.Now}
		for _, id := range refs {
			if e = projector.RebuildTx(ctx, tx, t.Organisation, id, nil); e != nil {
				return 0, nil, e
			}
		}
		result := map[string]any{"guest_id": g.ID, "property_id": property, "reservations_reconciled": len(refs), "source": "OWNED_CANONICAL_RESERVATIONS_STAYS_AND_PAYMENT_READBACKS"}
		raw := marshalCommercial(map[string]any{"result": result, "reason": in.Reason})
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,organisation_id,action,target_type,target_id,change_context,correlation_id) VALUES($1,$2,$3,'GUEST_STAY_HISTORY_REBUILD','guest',$4,$5,$6)`, ids.New(), t.Account, t.Organisation, g.ID, raw, requestcontext.From(ctx).RequestID)
		return 200, result, e
	})
}

func (h *Handler) crmFamilyHistory(ctx context.Context, tx pgx.Tx, org, property ids.ID, family []ids.ID, limit, offset int, now time.Time) ([]CRMStayHistoryItem, error) {
	rows, e := tx.Query(ctx, `SELECT p.id,p.version,p.source_digest,p.guest_id,p.reservation_id FROM stay_history_projection p WHERE p.organisation_id=$1 AND p.property_id=$2 AND p.guest_id=ANY($3::uuid[]) AND (EXISTS(SELECT 1 FROM stay_records s WHERE s.organisation_id=p.organisation_id AND s.reservation_id=p.reservation_id AND s.guest_id=p.guest_id) OR EXISTS(SELECT 1 FROM reservation_guests g WHERE g.organisation_id=p.organisation_id AND g.reservation_id=p.reservation_id AND g.guest_id=p.guest_id)) ORDER BY p.created_at DESC,p.id DESC LIMIT 2000`, org, property, family)
	if e != nil {
		return nil, e
	}
	type ref struct {
		id, guest, reservation ids.ID
		version                int64
		digest                 string
	}
	seen := map[ids.ID]bool{}
	refs := []ref{}
	for rows.Next() {
		var v ref
		if e = rows.Scan(&v.id, &v.version, &v.digest, &v.guest, &v.reservation); e != nil {
			break
		}
		if !seen[v.reservation] {
			seen[v.reservation] = true
			refs = append(refs, v)
		}
	}
	re := rows.Err()
	rows.Close()
	if e == nil {
		e = re
	}
	if e != nil {
		return nil, e
	}
	out := []CRMStayHistoryItem{}
	for i := offset; i < len(refs) && len(out) < limit; i++ {
		fact, e := crm.CurrentStayHistory(ctx, tx, org, refs[i].reservation, refs[i].guest, now)
		if e != nil {
			return nil, e
		}
		out = append(out, CRMStayHistoryItem{ProjectionID: refs[i].id, ProjectionVersion: refs[i].version, ProjectionCurrent: fact.SourceDigest == refs[i].digest, History: fact})
	}
	return out, nil
}
