package businesshttp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

type PMSFolioPosting struct {
	Version         int64   `json:"version"`
	Kind            string  `json:"kind"`
	SourceReference string  `json:"source_reference"`
	Quantity        int     `json:"quantity"`
	AmountMinor     int64   `json:"amount_minor"`
	Currency        string  `json:"currency"`
	PolicyReference string  `json:"policy_reference"`
	AdjustsItemID   *ids.ID `json:"adjusts_item_id,omitempty"`
	ApprovalID      ids.ID  `json:"approval_id,omitempty"`
	Reason          string  `json:"reason"`
}

func (h *Handler) pmsFolioGet(w http.ResponseWriter, r *http.Request) {
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
	f, e := hospitality.GetFolio(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if _, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "folios:read", "PROPERTY", f.PropertyID, false); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := hospitality.FolioItems(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var balance int64
	balance, e = hospitality.Balance(r.Context(), h.Auth.Service.Pool, t.Organisation, id, h.Auth.Service.Now())
	reply(w, r, 200, map[string]any{"folio": f, "items": items, "balance_minor": balance, "payment_truth": "PAYMENT_SERVICE"}, e)
}
func (h *Handler) pmsFolioPost(w http.ResponseWriter, r *http.Request) {
	var in PMSFolioPosting
	if !decode(w, r, &in) {
		return
	}
	if !validPMSFolioPosting(in) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Bounded integer-minor posting, source, accepted tax/fee policy reference and current folio version required."))
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
	pre, e := hospitality.GetFolio(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:folio:post:"+id.String(), in, "folios:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		return h.executePMSFolioPosting(ctx, tx, pre, in)
	})
}

// Reconciliation consumes only current, signed owning-service projections
// retained by the existing Payment readback worker. It never accepts a paid flag.
func (h *Handler) executePMSFolioPosting(ctx context.Context, tx pgx.Tx, pre hospitality.Folio, in PMSFolioPosting) (int, any, error) {
	id := pre.ID

	t, target, e := h.authorize(ctx, tx, "folios:write", "PROPERTY", pre.PropertyID, false)
	if e != nil {
		return 0, nil, e
	}
	f, e := hospitality.GetFolio(ctx, tx, t.Organisation, id)
	if e != nil {
		return 0, nil, e
	}
	if f.Version != in.Version || f.State != "OPEN" && f.State != "SETTLEMENT_PENDING" && f.State != "SETTLED" || f.Currency != in.Currency {
		return 0, nil, apperrors.Conflict("FOLIO_POSTING_STATE", "Current open or settlement-pending same-currency folio required.")
	}
	var duplicate bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM folio_items WHERE organisation_id=$1 AND folio_id=$2 AND source_reference=$3)`, t.Organisation, id, in.SourceReference).Scan(&duplicate); e != nil {
		return 0, nil, e
	}
	if duplicate {
		return 0, nil, apperrors.Conflict("FOLIO_SOURCE_ALREADY_POSTED", "A source posting is immutable and may be applied only once.")
	}
	approvalRequired := slices.Contains([]string{"ADJUSTMENT", "DAMAGE", "OTHER", "TAX", "FEE"}, in.Kind)
	if approvalRequired {
		amount := in.AmountMinor
		if amount < 0 {
			amount = -amount
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_FOLIO_ADJUSTMENT", target.Scope, in.ApprovalID, map[string]any{"folio_id": id, "command": op}, "", &amount, f.Currency); e != nil {
			return 0, nil, e
		}
	} else if !in.ApprovalID.IsZero() {
		return 0, nil, apperrors.InvalidRequest("This charge does not consume an adjustment approval.")
	}
	if in.AdjustsItemID != nil {
		var remaining int64
		e = tx.QueryRow(ctx, `SELECT (i.amount_minor+coalesce((SELECT sum(a.amount_minor) FROM folio_items a WHERE a.organisation_id=i.organisation_id AND a.adjusts_item_id=i.id),0))::bigint FROM folio_items i WHERE i.organisation_id=$1 AND i.folio_id=$2 AND i.id=$3 AND i.kind<>'ADJUSTMENT'`, t.Organisation, id, *in.AdjustsItemID).Scan(&remaining)
		if e == pgx.ErrNoRows {
			return 0, nil, apperrors.PermissionDenied("Adjustment must target an original item in this folio.")
		}
		if e != nil {
			return 0, nil, e
		}
		if in.AmountMinor >= 0 || in.AmountMinor < -remaining {
			return 0, nil, apperrors.Conflict("FOLIO_ADJUSTMENT_EXCEEDS_SOURCE", "A compensating correction cannot exceed the remaining original charge.")
		}
	}
	day, e := h.pmsDay(ctx, tx, t, f.PropertyID, true)
	if e != nil {
		return 0, nil, e
	}
	item := ids.New()
	var approval any
	if !in.ApprovalID.IsZero() {
		approval = in.ApprovalID
	}
	_, e = tx.Exec(ctx, `INSERT INTO folio_items(id,organisation_id,property_id,folio_id,kind,source_reference,quantity,amount_minor,currency,policy_snapshot,adjusts_item_id,approval_id,reason,created_by,business_day_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, item, t.Organisation, f.PropertyID, id, in.Kind, in.SourceReference, in.Quantity, in.AmountMinor, in.Currency, marshalCommercial(map[string]any{"policy_reference": in.PolicyReference}), in.AdjustsItemID, approval, in.Reason, t.Account, day.ID)
	if e != nil {
		return 0, nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE folios SET state=CASE WHEN state='SETTLED' THEN 'OPEN' ELSE state END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id); e != nil {
		return 0, nil, e
	}
	if e = h.pmsCommit(ctx, tx, t, f.PropertyID, id, "folio", "FOLIO_ITEM_POSTED", "hospitality.folio.updated.v1", f.Version+1, map[string]any{"item_id": item, "command": in}); e != nil {
		return 0, nil, e
	}
	if in.AdjustsItemID != nil {
		ready, e := hospitality.FiscalReady(ctx, tx, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if ready {
			resolvedFolio := f
			resolvedFolio.Version++
			if e = h.resolvePMSFolioExceptionsByCode(ctx, tx, t, resolvedFolio, "FISCAL_SOURCE_REVIEW_REQUIRED"); e != nil {
				return 0, nil, e
			}
		}
	}
	out, e := hospitality.GetFolio(ctx, tx, t.Organisation, id)
	return 201, map[string]any{"folio": out, "item_id": item}, e

}
func (h *Handler) pmsFolioReconcile(w http.ResponseWriter, r *http.Request) {
	var in PMSVersionCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current folio version and reconciliation reason required."))
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
	pre, e := hospitality.GetFolio(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:folio:reconcile:"+id.String(), in, "folios:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "folios:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		f, e := hospitality.GetFolio(ctx, tx, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if f.Version != in.Version || f.State == "CLOSED" || f.State == "DISPUTED" {
			return 0, nil, apperrors.Conflict("FOLIO_RECONCILIATION_STATE", "Current reviewable folio required.")
		}
		var group ids.ID
		if e = tx.QueryRow(ctx, `SELECT rr.financial_group_id FROM stay_records s JOIN room_assignments a ON a.organisation_id=s.organisation_id AND a.id=s.assignment_id JOIN reservation_rooms rr ON rr.organisation_id=a.organisation_id AND rr.id=a.reservation_room_id WHERE s.organisation_id=$1 AND s.id=$2`, t.Organisation, f.StayID).Scan(&group); e != nil {
			return 0, nil, e
		}
		type proof struct {
			ID          ids.ID `json:"id"`
			IntentID    ids.ID `json:"intent_id"`
			AmountMinor int64  `json:"amount_minor"`
			Currency    string `json:"currency"`
			Available   int64  `json:"available"`
		}
		p, e := hospitality.Load[proof](ctx, tx, `SELECT to_jsonb(p)||jsonb_build_object('available',p.amount_minor-coalesce((SELECT sum(a.amount_minor) FROM folio_payment_references a WHERE a.organisation_id=p.organisation_id AND a.financial_group_id=p.financial_group_id),0)) FROM reservation_payment_projections p JOIN reservation_payment_bindings b ON b.organisation_id=p.organisation_id AND b.financial_group_id=p.financial_group_id AND b.intent_id=p.intent_id WHERE p.organisation_id=$1 AND p.financial_group_id=$2 AND p.id=(SELECT latest.id FROM reservation_payment_projections latest WHERE latest.organisation_id=$1 AND latest.financial_group_id=$2 ORDER BY observed_at DESC,id DESC LIMIT 1) AND p.provider_state='VERIFIED_SUCCESS' AND p.settlement_state='SETTLED' AND p.funding_confirmed AND p.reversed_minor=0 AND p.remedy_version=1 AND NOT p.remedy_review_required AND p.expires_at>$3`, t.Organisation, group, h.Auth.Service.Now())
		if e != nil {
			var app *apperrors.Error
			if !errors.As(e, &app) || app.Status != 404 {
				return 0, nil, e
			}
			return 0, nil, apperrors.Conflict("FOLIO_PAYMENT_READBACK_REQUIRED", "Current bound settled, funded and unreversed Payment receipt required; collection/guarantee alone cannot settle a folio.")
		}
		balance, e := hospitality.Balance(ctx, tx, t.Organisation, id, h.Auth.Service.Now())
		if e != nil {
			return 0, nil, e
		}
		amount := min(balance, p.Available)
		var prior bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM folio_payment_references WHERE organisation_id=$1 AND folio_id=$2 AND financial_group_id=$3)`, t.Organisation, id, group).Scan(&prior); e != nil {
			return 0, nil, e
		}
		if amount > 0 && !prior {
			if p.Currency != f.Currency {
				return 0, nil, apperrors.Conflict("FOLIO_CURRENCY_MISMATCH", "Financial evidence cannot change accepted currency.")
			}
			if _, e = tx.Exec(ctx, `INSERT INTO folio_payment_references(id,organisation_id,property_id,folio_id,financial_group_id,intent_id,payment_projection_id,amount_minor,currency,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, ids.New(), t.Organisation, f.PropertyID, id, group, p.IntentID, p.ID, amount, f.Currency, t.Account); e != nil {
				return 0, nil, e
			}
		}
		balance, e = hospitality.Balance(ctx, tx, t.Organisation, id, h.Auth.Service.Now())
		if e != nil {
			return 0, nil, e
		}
		state := "SETTLEMENT_PENDING"
		if balance == 0 {
			state = "SETTLED"
		}
		if _, e = tx.Exec(ctx, `UPDATE folios SET state=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, state); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, f.PropertyID, id, "folio", "FOLIO_PAYMENT_RECONCILED", "hospitality.folio.updated.v1", f.Version+1, map[string]any{"financial_group_id": group, "payment_projection_id": p.ID, "state": state, "balance_minor": balance, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := hospitality.GetFolio(ctx, tx, t.Organisation, id)
		return 200, map[string]any{"folio": out, "balance_minor": balance, "payment_truth": "PAYMENT_SERVICE"}, e
	})
}
func (h *Handler) pmsFrontDesk(w http.ResponseWriter, r *http.Request) {
	property, e := ids.ParseV7(r.URL.Query().Get("property_id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A single property scope is required for its local front desk."))
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "pms:read", "PROPERTY", property, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var zone string
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT timezone FROM inventory_nodes WHERE organisation_id=$1 AND id=$2 AND kind='PROPERTY'`, t.Organisation, property).Scan(&zone); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var date, cutoff string
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT coalesce(d.day::text,''),coalesce(d.cutoff_local,p.cutoff_local,'00:00') FROM inventory_nodes n LEFT JOIN hospitality_business_days d ON d.organisation_id=n.organisation_id AND d.property_id=n.id AND d.state='OPEN' LEFT JOIN LATERAL(SELECT cutoff_local FROM hospitality_policies WHERE organisation_id=n.organisation_id AND property_id=n.id ORDER BY version DESC LIMIT 1)p ON true WHERE n.organisation_id=$1 AND n.id=$2`, t.Organisation, property).Scan(&date, &cutoff); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if date == "" {
		date, e = hospitality.BusinessDate(h.Auth.Service.Now(), cutoff, zone)
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	counts, e := hospitality.Load[map[string]int64](r.Context(), h.Auth.Service.Pool, `SELECT jsonb_build_object('unresolved_operational_exceptions',(SELECT count(*) FROM hospitality_operation_exceptions WHERE organisation_id=$1 AND property_id=$2 AND state='OPEN'),'arrivals_today',(SELECT count(*) FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND date_from=$3::date AND state='CONFIRMED'),'upcoming_arrivals',(SELECT count(*) FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND date_from>$3::date AND state='CONFIRMED'),'departures_today',(SELECT count(*) FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND date_to=$3::date AND state='CHECKED_IN'),'overdue_departures',(SELECT count(*) FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND date_to<$3::date AND state='CHECKED_IN'),'in_house',(SELECT count(*) FROM stay_records WHERE organisation_id=$1 AND property_id=$2 AND state='ACTIVE'),'dirty_rooms',(SELECT count(*) FROM room_status_current WHERE organisation_id=$1 AND property_id=$2 AND state='VACANT_DIRTY'),'clean_rooms',(SELECT count(*) FROM room_status_current WHERE organisation_id=$1 AND property_id=$2 AND state='VACANT_CLEAN'),'inspected_rooms',(SELECT count(*) FROM room_status_current WHERE organisation_id=$1 AND property_id=$2 AND state='INSPECTED'),'occupied_rooms',(SELECT count(*) FROM room_status_current WHERE organisation_id=$1 AND property_id=$2 AND state='OCCUPIED'),'blocked_rooms',(SELECT count(*) FROM room_status_current WHERE organisation_id=$1 AND property_id=$2 AND state='BLOCKED'),'out_of_order_rooms',(SELECT count(*) FROM room_status_current WHERE organisation_id=$1 AND property_id=$2 AND state='OUT_OF_ORDER'),'pending_guarantees',(SELECT count(*) FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND state IN ('HOLD','PENDING_CONFIRMATION','CONFIRMED') AND financial_state NOT IN ('VERIFIED','APPROVED_GUARANTEE')),'walk_in_registrations',(SELECT count(*) FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND source='WALK_IN' AND date_from=$3::date),'group_arrivals',(SELECT count(*) FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND group_id IS NOT NULL AND date_from=$3::date AND state='CONFIRMED'),'settlement_pending',(SELECT count(*) FROM folios WHERE organisation_id=$1 AND property_id=$2 AND state='SETTLEMENT_PENDING'))`, t.Organisation, property, date)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	view := ""
	if strings.HasSuffix(r.URL.Path, "/arrivals") {
		view = "ARRIVALS"
	}
	if strings.HasSuffix(r.URL.Path, "/departures") {
		view = "DEPARTURES"
	}
	records, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT jsonb_build_object('id',id,'state',state,'source',source,'date_from',date_from,'date_to',date_to,'version',version,'financial_state',financial_state) FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND state IN ('CONFIRMED','CHECKED_IN') AND ($3='' OR source_reference=$3 OR id::text=$3) AND ($6='' OR $6='ARRIVALS' AND state='CONFIRMED' AND date_from=$7::date OR $6='DEPARTURES' AND state='CHECKED_IN' AND date_to<=$7::date) ORDER BY date_from,id LIMIT $4 OFFSET $5`, t.Organisation, property, r.URL.Query().Get("reference"), limit, offset, view, date)
	reply(w, r, 200, map[string]any{"property_id": property, "business_date": date, "timezone": zone, "posting_cutoff_local": cutoff, "observed_at": h.Auth.Service.Now().UTC().Format(time.RFC3339Nano), "counts": counts, "reservations": records, "limit": limit, "offset": offset}, e)
}

func validPMSFolioPosting(in PMSFolioPosting) bool {
	return !(in.Version < 1 || !slices.Contains([]string{"TAX", "FEE", "MEALS", "MINIBAR", "LAUNDRY", "SPA", "TRANSFER", "EVENT", "DAMAGE", "OTHER", "ADJUSTMENT"}, in.Kind) || in.Quantity < 1 || in.Quantity > 10000 || in.AmountMinor == 0 || in.AmountMinor > 1000000000000 || in.AmountMinor < -1000000000000 || !crm.OpaqueReference(in.SourceReference) || !crm.Text(in.PolicyReference, 5, 300) || !validStaffReason(in.Reason) || (in.Kind == "ADJUSTMENT") != (in.AdjustsItemID != nil) || in.Kind != "ADJUSTMENT" && in.AmountMinor < 0)
}
