package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type PMSBusinessDay struct {
	ID             ids.ID    `json:"id"`
	OrganisationID ids.ID    `json:"organisation_id"`
	PropertyID     ids.ID    `json:"property_id"`
	Day            string    `json:"day"`
	Timezone       string    `json:"timezone"`
	CutoffLocal    string    `json:"cutoff_local"`
	CutoffAt       time.Time `json:"cutoff_at"`
	State          string    `json:"state"`
	Version        int64     `json:"version"`
	PolicyVersion  int64     `json:"policy_version"`
}
type PMSHospitalityPolicyCommand struct {
	PropertyID      ids.ID `json:"property_id"`
	ExpectedVersion int64  `json:"expected_version"`
	CutoffLocal     string `json:"cutoff_local"`
	ApprovalID      ids.ID `json:"approval_id"`
	Reason          string `json:"reason"`
}
type PMSAuditCommand struct {
	PropertyID ids.ID `json:"property_id"`
	Reason     string `json:"reason"`
}
type PMSDayCloseCommand struct {
	PropertyID     ids.ID `json:"property_id"`
	BusinessDayID  ids.ID `json:"business_day_id"`
	Version        int64  `json:"version"`
	AuditID        ids.ID `json:"audit_id"`
	SnapshotDigest string `json:"snapshot_digest"`
	ApprovalID     ids.ID `json:"approval_id,omitempty"`
	Reason         string `json:"reason"`
}
type PMSAuditException struct {
	Code      string `json:"code"`
	Quantity  int64  `json:"quantity"`
	HardBlock bool   `json:"hard_block"`
}
type PMSAuditSnapshot struct {
	SourceDigest string              `json:"source_digest"`
	Day          PMSBusinessDay      `json:"day"`
	Checks       map[string]int64    `json:"checks"`
	Exceptions   []PMSAuditException `json:"exceptions"`
}

func (h *Handler) registerPMSDays(r chi.Router) {
	r.Get("/hospitality-day-automation", h.pmsDayAutomationPolicies)
	r.Post("/hospitality-day-automation", h.pmsDayAutomationPolicy)
	r.Post("/hospitality-day-automation/{id}/revoke", h.pmsDayAutomationRevoke)
	r.Get("/hospitality-day-automation-runs", h.pmsDayAutomationRuns)
	r.Get("/hospitality-policies", h.pmsHospitalityPolicies)
	r.Post("/hospitality-policies", h.pmsHospitalityPolicy)
	r.Get("/night-audit", h.pmsNightAudits)
	r.Post("/night-audit/validate", h.pmsNightAuditValidate)
	r.Post("/night-audit/close", h.pmsBusinessDayClose)
	r.Get("/business-day-closes", h.pmsBusinessDayCloses)
}

// The organisation fence serializes posts, validation and closing. A crash
// cannot commit a close without its report, history, rollover and outbox.
func (h *Handler) pmsDay(ctx context.Context, tx pgx.Tx, t tenant, property ids.ID, posting bool) (d PMSBusinessDay, e error) {
	var raw []byte
	e = tx.QueryRow(ctx, `SELECT to_jsonb(d) FROM hospitality_business_days d WHERE organisation_id=$1 AND property_id=$2 AND state='OPEN' FOR UPDATE`, t.Organisation, property).Scan(&raw)
	if e == nil {
		e = json.Unmarshal(raw, &d)
	} else if e == pgx.ErrNoRows {
		var zone, cutoff string
		var version int64
		if e = tx.QueryRow(ctx, `SELECT n.timezone,coalesce(p.cutoff_local,'00:00'),coalesce(p.version,0) FROM inventory_nodes n LEFT JOIN LATERAL(SELECT cutoff_local,version FROM hospitality_policies p WHERE p.organisation_id=n.organisation_id AND p.property_id=n.id ORDER BY version DESC LIMIT 1)p ON true WHERE n.organisation_id=$1 AND n.id=$2 AND n.kind='PROPERTY'`, t.Organisation, property).Scan(&zone, &cutoff, &version); e != nil {
			return d, e
		}
		day, e := hospitality.BusinessDate(h.Auth.Service.Now(), cutoff, zone)
		if e != nil {
			return d, e
		}
		var prior *string
		if e = tx.QueryRow(ctx, `SELECT max(day)::text FROM hospitality_business_days WHERE organisation_id=$1 AND property_id=$2 AND state='CLOSED'`, t.Organisation, property).Scan(&prior); e != nil {
			return d, e
		}
		if prior != nil {
			p, _ := time.Parse(time.DateOnly, *prior)
			day = p.AddDate(0, 0, 1).Format(time.DateOnly)
		}
		civil, _ := time.Parse(time.DateOnly, day)
		end, e := hospitality.LocalInstant(civil.AddDate(0, 0, 1).Format(time.DateOnly), cutoff, zone)
		if e != nil {
			return d, e
		}
		d = PMSBusinessDay{ID: ids.New(), OrganisationID: t.Organisation, PropertyID: property, Day: day, Timezone: zone, CutoffLocal: cutoff, CutoffAt: end, State: "OPEN", Version: 1, PolicyVersion: version}
		if _, e = tx.Exec(ctx, `INSERT INTO hospitality_business_days(id,organisation_id,property_id,day,timezone,cutoff_local,cutoff_at,state,policy_version,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,'OPEN',$8,$9)`, d.ID, t.Organisation, property, day, zone, cutoff, end, version, t.Account); e != nil {
			return d, e
		}
		if e = h.pmsCommit(ctx, tx, t, property, d.ID, "business_day", "BUSINESS_DAY_OPENED", "hospitality.business_day.opened.v1", 1, d); e != nil {
			return d, e
		}
	} else {
		return d, e
	}
	if e != nil {
		return d, e
	}
	if posting && !h.Auth.Service.Now().Before(d.CutoffAt) {
		return d, apperrors.Conflict("BUSINESS_DAY_CUTOFF", "Close the expired business day before accepting further postings.")
	}
	return d, nil
}
func (h *Handler) pmsHospitalityPolicy(w http.ResponseWriter, r *http.Request) {
	var in PMSHospitalityPolicyCommand
	if !decode(w, r, &in) {
		return
	}
	_, valid := hospitality.LocalInstant("2026-11-02", in.CutoffLocal, "UTC")
	if in.PropertyID.ValidateV7() != nil || in.ExpectedVersion < 0 || valid != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, valid local cutoff, expected policy version and reason required."))
		return
	}
	h.execute(w, r, "pms:policy:"+in.PropertyID.String(), in, "night_audit:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "night_audit:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		var current int64
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM hospitality_policies WHERE organisation_id=$1 AND property_id=$2`, t.Organisation, in.PropertyID).Scan(&current); e != nil {
			return 0, nil, e
		}
		if current != in.ExpectedVersion {
			return 0, nil, apperrors.Conflict("HOSPITALITY_POLICY_VERSION", "Hospitality policy changed.")
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_BUSINESS_DAY_POLICY", target.Scope, in.ApprovalID, map[string]any{"command": op}, "", nil, ""); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO hospitality_policies(id,organisation_id,property_id,cutoff_local,version,approval_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, in.PropertyID, in.CutoffLocal, current+1, in.ApprovalID, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, in.PropertyID, id, "hospitality_policy", "HOSPITALITY_POLICY_CREATED", "hospitality.policy.updated.v1", current+1, in); e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"id": id, "version": current + 1, "cutoff_local": in.CutoffLocal, "applies_to": "NEXT_OPENED_BUSINESS_DAY"}, nil
	})
}
func (h *Handler) pmsAuditSnapshot(ctx context.Context, tx pgx.Tx, t tenant, d PMSBusinessDay) (PMSAuditSnapshot, error) {
	checks, e := hospitality.Load[map[string]int64](ctx, tx, `SELECT jsonb_build_object(
 'UNKNOWN_CANCELLATION_FINANCIAL_OUTCOMES',(SELECT count(*) FROM reservation_cancellation_obligations WHERE organisation_id=$1 AND property_id=$2 AND financial_state<>'KNOWN_PLATFORM_REFERENCE'),
 'PENDING_RESERVATION_FINANCIAL_REMEDIES',(SELECT count(*) FROM reservation_financial_requests WHERE organisation_id=$1 AND property_id=$2 AND kind IN ('REFUND','SETTLEMENT') AND state<>'SUCCEEDED'),
 'FISCAL_DETAIL_REVIEW_REQUIRED',(SELECT count(*) FROM folio_items i JOIN folios f ON f.organisation_id=i.organisation_id AND f.id=i.folio_id LEFT JOIN LATERAL(SELECT state FROM folio_fiscal_reviews v WHERE v.organisation_id=i.organisation_id AND v.folio_item_id=i.id ORDER BY review_version DESC LIMIT 1)v ON true WHERE i.organisation_id=$1 AND i.property_id=$2 AND i.kind='ACCOMMODATION' AND i.policy_snapshot->'accepted_quote' IS NOT NULL AND (i.amount_minor=0 OR i.amount_minor+coalesce((SELECT sum(a.amount_minor) FROM folio_items a WHERE a.organisation_id=i.organisation_id AND a.adjusts_item_id=i.id),0)>0) AND (f.state<>'CLOSED' OR i.business_day_id=$5) AND (v.state IS NULL OR v.state NOT IN ('ITEMIZED','OPERATOR_REVIEWED'))),
 'UNRESOLVED_OPERATIONAL_EXCEPTIONS',(SELECT count(*) FROM hospitality_operation_exceptions WHERE organisation_id=$1 AND property_id=$2 AND state='OPEN'),
 'UNRESOLVED_ACCESS_REVOCATION',(SELECT count(*) FROM access_grant_references a JOIN hospitality_access_jobs j ON j.organisation_id=a.organisation_id AND j.id=a.id WHERE a.organisation_id=$1 AND a.property_id=$2 AND a.operation='REVOKE' AND a.state<>'CONFIRMED'),
 'UNKNOWN_CONFIGURED_ACCESS_OUTCOME',(SELECT count(*) FROM access_grant_references a JOIN hospitality_access_jobs j ON j.organisation_id=a.organisation_id AND j.id=a.id WHERE a.organisation_id=$1 AND a.property_id=$2 AND a.operation='ISSUE' AND a.state<>'CONFIRMED' AND j.delivery_state<>'CANCELLED' AND NOT EXISTS(SELECT 1 FROM access_grant_references rev WHERE rev.organisation_id=a.organisation_id AND rev.assignment_id=a.assignment_id AND rev.operation='REVOKE' AND rev.state='CONFIRMED')),
 'PENDING_ARRIVALS',(SELECT count(*) FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND date_from<=$3::date AND state='CONFIRMED'),
 'GROUP_ROOMING_MISSING',(SELECT coalesce(sum(rr.quantity-(SELECT count(*) FROM rooming_lists l WHERE l.organisation_id=rr.organisation_id AND l.reservation_room_id=rr.id AND l.group_id=r.group_id AND l.state='ACTIVE' AND NOT EXISTS(SELECT 1 FROM rooming_lists later WHERE later.organisation_id=l.organisation_id AND later.group_id=l.group_id AND later.reservation_room_id=l.reservation_room_id AND later.slot=l.slot AND later.version>l.version))),0) FROM reservation_rooms rr JOIN reservations r ON r.organisation_id=rr.organisation_id AND r.id=rr.reservation_id JOIN inventory_pools p ON p.organisation_id=rr.organisation_id AND p.id=rr.pool_id WHERE rr.organisation_id=$1 AND rr.property_id=$2 AND rr.state='ACTIVE' AND `+stayExtensionLineExcluded+` AND r.group_id IS NOT NULL AND r.allocation_state='ACTIVE' AND r.state IN ('CONFIRMED','CHECKED_IN') AND rr.date_from<=$3::date AND p.kind IN ('ROOM','UNIT','ROOM_TYPE')),

 'OVERDUE_DEPARTURES',(SELECT count(*) FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND date_to<=$3::date AND state='CHECKED_IN'),
 'OCCUPANCY_CONFLICT',(SELECT count(*) FROM room_status_current s WHERE s.organisation_id=$1 AND s.property_id=$2 AND (s.state='OCCUPIED')<>EXISTS(SELECT 1 FROM room_assignments a JOIN stay_records stay ON stay.organisation_id=a.organisation_id AND stay.assignment_id=a.id WHERE a.organisation_id=s.organisation_id AND a.room_id=s.room_id AND a.state='OCCUPIED' AND stay.state='ACTIVE')),
 'PENDING_FINANCIAL_SETTLEMENT',(SELECT count(*) FROM folios WHERE organisation_id=$1 AND property_id=$2 AND state IN ('SETTLEMENT_PENDING','DISPUTED')),
 'OPEN_CASHIER_SHIFTS',(SELECT count(*) FROM cashier_shifts WHERE organisation_id=$1 AND property_id=$2 AND state='OPEN'),
 'UNKNOWN_CASH_RECONCILIATION',(SELECT count(*) FROM cashier_shifts WHERE organisation_id=$1 AND property_id=$2 AND state='RECONCILIATION_REQUIRED'),
 'STALE_CASH_RECONCILIATION',(SELECT count(*) FROM cashier_shifts s WHERE s.organisation_id=$1 AND s.property_id=$2 AND s.state='CLOSED' AND s.business_day_id=$5 AND NOT EXISTS(SELECT 1 FROM cashier_financial_attestations a WHERE a.organisation_id=s.organisation_id AND a.shift_id=s.id AND a.id=(SELECT latest.id FROM cashier_financial_attestations latest WHERE latest.organisation_id=s.organisation_id AND latest.shift_id=s.id ORDER BY source_version DESC LIMIT 1) AND a.expires_at>$4 AND s.expected_minor=s.opening_minor+a.expected_movement_minor)),
 'UNUSABLE_FOLIO_PAYMENT_REFERENCE',(SELECT count(*) FROM folio_payment_references a JOIN folios f ON f.organisation_id=a.organisation_id AND f.id=a.folio_id WHERE a.organisation_id=$1 AND a.property_id=$2 AND f.state IN ('SETTLED','CLOSED') AND NOT EXISTS(SELECT 1 FROM reservation_payment_projections p WHERE p.organisation_id=a.organisation_id AND p.financial_group_id=a.financial_group_id AND p.intent_id=a.intent_id AND p.id=(SELECT latest.id FROM reservation_payment_projections latest WHERE latest.organisation_id=a.organisation_id AND latest.financial_group_id=a.financial_group_id ORDER BY observed_at DESC,id DESC LIMIT 1) AND p.expires_at>$4 AND p.provider_state='VERIFIED_SUCCESS' AND p.settlement_state='SETTLED' AND p.funding_confirmed AND p.reversed_minor=0 AND p.remedy_version=1 AND NOT p.remedy_review_required)),

 'MISSING_FOLIO_POSTINGS',(SELECT count(*) FROM stay_records s WHERE s.organisation_id=$1 AND s.property_id=$2 AND NOT EXISTS(SELECT 1 FROM folios f JOIN folio_items i ON i.organisation_id=f.organisation_id AND i.folio_id=f.id AND i.kind='ACCOMMODATION' WHERE f.organisation_id=s.organisation_id AND f.stay_id=s.id)),
 'RATE_CURRENCY_MISMATCH',(SELECT count(*) FROM folio_items i JOIN folios f ON f.organisation_id=i.organisation_id AND f.id=i.folio_id WHERE i.organisation_id=$1 AND i.property_id=$2 AND i.currency<>f.currency),
 'SOURCE_CONFLICT',(SELECT count(*) FROM reservation_conflicts WHERE organisation_id=$1 AND property_id=$2 AND state NOT IN ('RESOLVED','REJECTED')),
 'HARD_ROOM_BLOCKS',(SELECT count(*) FROM room_status_current WHERE organisation_id=$1 AND property_id=$2 AND state IN ('BLOCKED','OUT_OF_ORDER')),
 'OPEN_GUEST_REQUESTS',(SELECT count(*) FROM guest_requests WHERE organisation_id=$1 AND property_id=$2 AND state IN ('OPEN','ACKNOWLEDGED')),
 'UNRESOLVED_STAY_AMENDMENT_OBLIGATIONS',(SELECT count(*) FROM stay_early_departures WHERE organisation_id=$1 AND property_id=$2 AND financial_state IN ('READBACK_REQUIRED','EXTERNAL_COLLECTION_UNKNOWN'))+(SELECT count(*) FROM stay_extension_payers p JOIN reservation_financial_groups g ON g.organisation_id=p.organisation_id AND g.id=p.financial_group_id WHERE p.organisation_id=$1 AND p.property_id=$2 AND g.state IN ('UNKNOWN','DISPUTED','FAILED'))
 )`, t.Organisation, d.PropertyID, d.Day, h.Auth.Service.Now(), d.ID)
	snapshot := PMSAuditSnapshot{Day: d, Checks: checks, Exceptions: []PMSAuditException{}}
	if e != nil {
		return snapshot, e
	}
	for _, code := range []string{"UNRESOLVED_ACCESS_REVOCATION", "UNKNOWN_CONFIGURED_ACCESS_OUTCOME", "UNKNOWN_CANCELLATION_FINANCIAL_OUTCOMES", "PENDING_RESERVATION_FINANCIAL_REMEDIES", "FISCAL_DETAIL_REVIEW_REQUIRED", "UNRESOLVED_OPERATIONAL_EXCEPTIONS", "GROUP_ROOMING_MISSING", "PENDING_ARRIVALS", "OVERDUE_DEPARTURES", "OCCUPANCY_CONFLICT", "PENDING_FINANCIAL_SETTLEMENT", "OPEN_CASHIER_SHIFTS", "UNKNOWN_CASH_RECONCILIATION", "STALE_CASH_RECONCILIATION", "UNUSABLE_FOLIO_PAYMENT_REFERENCE", "MISSING_FOLIO_POSTINGS", "RATE_CURRENCY_MISMATCH", "SOURCE_CONFLICT", "HARD_ROOM_BLOCKS", "OPEN_GUEST_REQUESTS", "UNRESOLVED_STAY_AMENDMENT_OBLIGATIONS"} {
		if checks[code] > 0 {
			hard := code != "PENDING_ARRIVALS" && code != "OVERDUE_DEPARTURES" && code != "OPEN_GUEST_REQUESTS"
			snapshot.Exceptions = append(snapshot.Exceptions, PMSAuditException{Code: code, Quantity: checks[code], HardBlock: hard})
		}
	}
	var source []byte
	if e = tx.QueryRow(ctx, `SELECT jsonb_build_object(
 'reservations',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,version,state) ORDER BY id),'[]') FROM reservations WHERE organisation_id=$1 AND property_id=$2),
 'fiscal_reviews',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,review_version,digest,state) ORDER BY id),'[]') FROM folio_fiscal_reviews WHERE organisation_id=$1 AND property_id=$2),
 'operational_exceptions',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,version,state) ORDER BY id),'[]') FROM hospitality_operation_exceptions WHERE organisation_id=$1 AND property_id=$2),
 'groups',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,version,state) ORDER BY id),'[]') FROM reservation_groups WHERE organisation_id=$1 AND property_id=$2),
 'rooming',(SELECT coalesce(jsonb_agg(id ORDER BY id),'[]') FROM rooming_lists WHERE organisation_id=$1 AND property_id=$2),
 'masters',(SELECT coalesce(jsonb_agg(id ORDER BY id),'[]') FROM master_folios WHERE organisation_id=$1 AND property_id=$2),
 'events',(SELECT coalesce(jsonb_agg(id ORDER BY id),'[]') FROM event_bookings WHERE organisation_id=$1 AND property_id=$2),

 'access_references',(SELECT coalesce(jsonb_agg(jsonb_build_array(a.id,a.version,a.state,j.request_digest,j.delivery_state) ORDER BY a.id),'[]') FROM access_grant_references a LEFT JOIN hospitality_access_jobs j ON j.organisation_id=a.organisation_id AND j.id=a.id WHERE a.organisation_id=$1 AND a.property_id=$2),
 'access_configurations',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,version,expires_at) ORDER BY id),'[]') FROM hospitality_access_configurations WHERE organisation_id=$1 AND property_id=$2),
 'rooms',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,version,state) ORDER BY id),'[]') FROM room_status_current WHERE organisation_id=$1 AND property_id=$2),
 'stays',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,version,state) ORDER BY id),'[]') FROM stay_records WHERE organisation_id=$1 AND property_id=$2),
 'assignments',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,version,state) ORDER BY id),'[]') FROM room_assignments WHERE organisation_id=$1 AND property_id=$2),
 'folios',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,version,state) ORDER BY id),'[]') FROM folios WHERE organisation_id=$1 AND property_id=$2),
 'items',(SELECT coalesce(jsonb_agg(id ORDER BY id),'[]') FROM folio_items WHERE organisation_id=$1 AND property_id=$2),
 'cash',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,version,state) ORDER BY id),'[]') FROM cashier_shifts WHERE organisation_id=$1 AND property_id=$2),
 'cash_proofs',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,source_version,expires_at) ORDER BY id),'[]') FROM cashier_financial_attestations WHERE organisation_id=$1 AND property_id=$2),
 'requests',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,version,state) ORDER BY id),'[]') FROM guest_requests WHERE organisation_id=$1 AND property_id=$2),
 'payments',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,expires_at,remedy_version,reversed_minor) ORDER BY id),'[]') FROM reservation_payment_projections WHERE organisation_id=$1 AND property_id=$2),
 'cancellation_obligations',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,financial_state,penalty_minor,refundable_minor) ORDER BY id),'[]') FROM reservation_cancellation_obligations WHERE organisation_id=$1 AND property_id=$2),
 'financial_remedies',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,version,state,provider_reference) ORDER BY id),'[]') FROM reservation_financial_requests WHERE organisation_id=$1 AND property_id=$2 AND kind IN ('REFUND','SETTLEMENT')),
 'source_conflicts',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,version,state) ORDER BY id),'[]') FROM reservation_conflicts WHERE organisation_id=$1 AND property_id=$2),
 'stay_extensions',(SELECT coalesce(jsonb_agg(id ORDER BY id),'[]') FROM stay_extensions WHERE organisation_id=$1 AND property_id=$2),
 'early_departures',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,financial_state) ORDER BY id),'[]') FROM stay_early_departures WHERE organisation_id=$1 AND property_id=$2),
 'unit_releases',(SELECT coalesce(jsonb_agg(id ORDER BY id),'[]') FROM reservation_unit_releases WHERE organisation_id=$1 AND property_id=$2)
 )`, t.Organisation, d.PropertyID).Scan(&source); e != nil {
		return snapshot, e
	}
	snapshot.SourceDigest = security.SHA256Hex(source)
	return snapshot, nil
}
func (h *Handler) pmsNightAuditValidate(w http.ResponseWriter, r *http.Request) {
	var in PMSAuditCommand
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property and audit evidence reason required."))
		return
	}
	h.execute(w, r, "pms:night-audit:validate:"+in.PropertyID.String(), in, "night_audit:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "night_audit:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		out, e := h.validatePMSNightAudit(ctx, tx, t, in.PropertyID, in.Reason)
		return 201, out, e
	})
}
func (h *Handler) pmsBusinessDayClose(w http.ResponseWriter, r *http.Request) {
	var in PMSDayCloseCommand
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.BusinessDayID.ValidateV7() != nil || in.AuditID.ValidateV7() != nil || in.Version < 1 || len(in.SnapshotDigest) != 64 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Exact business day, audit snapshot, current version and closure reason required."))
		return
	}
	h.execute(w, r, "pms:day:close:"+in.BusinessDayID.String(), in, "night_audit:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "night_audit:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		out, e := h.closePMSBusinessDay(ctx, tx, t, in)
		return 200, out, e
	})
}
func (h *Handler) pmsHospitalityPolicies(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "hospitality_policies", "night_audit:read")
}
func (h *Handler) pmsNightAudits(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "night_audits", "night_audit:read")
}
func (h *Handler) pmsBusinessDayCloses(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "business_day_closes", "night_audit:read")
}

type PMSAuditResult struct {
	ID             ids.ID           `json:"id"`
	State          string           `json:"state"`
	SnapshotDigest string           `json:"snapshot_digest"`
	Snapshot       PMSAuditSnapshot `json:"snapshot"`
}

func (h *Handler) validatePMSNightAudit(ctx context.Context, tx pgx.Tx, t tenant, property ids.ID, reason string) (out PMSAuditResult, e error) {
	d, e := h.pmsDay(ctx, tx, t, property, false)
	if e != nil {
		return out, e
	}
	snapshot, e := h.pmsAuditSnapshot(ctx, tx, t, d)
	if e != nil {
		return out, e
	}
	id := ids.New()
	state := "READY"
	if len(snapshot.Exceptions) > 0 {
		state = "EXCEPTIONS"
	}
	digest := security.SHA256Hex(marshalCommercial(snapshot))
	if _, e = tx.Exec(ctx, `INSERT INTO night_audits(id,organisation_id,property_id,business_day_id,state,snapshot,snapshot_digest,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, property, d.ID, state, marshalCommercial(snapshot), digest, t.Account); e != nil {
		return out, e
	}
	for _, ex := range snapshot.Exceptions {
		if _, e = tx.Exec(ctx, `INSERT INTO night_audit_exceptions(id,organisation_id,property_id,audit_id,code,hard_block,quantity,evidence) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ids.New(), t.Organisation, property, id, ex.Code, ex.HardBlock, ex.Quantity, marshalCommercial(ex)); e != nil {
			return out, e
		}
	}
	if e = h.pmsCommit(ctx, tx, t, property, id, "night_audit", "NIGHT_AUDIT_VALIDATED", "hospitality.night_audit.validated.v1", 1, map[string]any{"business_day_id": d.ID, "snapshot_digest": digest, "state": state, "reason": reason}); e != nil {
		return out, e
	}
	return PMSAuditResult{ID: id, State: state, SnapshotDigest: digest, Snapshot: snapshot}, nil
}

type PMSDayCloseResult struct {
	ID              ids.ID         `json:"id"`
	State           string         `json:"state"`
	BusinessDayID   ids.ID         `json:"business_day_id"`
	NextBusinessDay PMSBusinessDay `json:"next_business_day"`
}

func (h *Handler) closePMSBusinessDay(ctx context.Context, tx pgx.Tx, t tenant, in PMSDayCloseCommand) (out PMSDayCloseResult, e error) {
	d, e := h.pmsDay(ctx, tx, t, in.PropertyID, false)
	if e != nil {
		return out, e
	}
	if d.ID != in.BusinessDayID || d.Version != in.Version || h.Auth.Service.Now().Before(d.CutoffAt) {
		return out, apperrors.Conflict("BUSINESS_DAY_CLOSE_WINDOW", "Only the exact current business day after its local cutoff can close.")
	}
	var digest string
	if e = tx.QueryRow(ctx, `SELECT snapshot_digest FROM night_audits WHERE organisation_id=$1 AND property_id=$2 AND business_day_id=$3 AND id=$4`, t.Organisation, in.PropertyID, d.ID, in.AuditID).Scan(&digest); e != nil {
		return out, e
	}
	snapshot, e := h.pmsAuditSnapshot(ctx, tx, t, d)
	if e != nil {
		return out, e
	}
	if digest != in.SnapshotDigest || security.SHA256Hex(marshalCommercial(snapshot)) != digest {
		return out, apperrors.Conflict("NIGHT_AUDIT_STALE", "Revalidate the actual current property evidence before closing.")
	}
	for _, ex := range snapshot.Exceptions {
		if ex.HardBlock {
			return out, apperrors.Conflict("NIGHT_AUDIT_HARD_EXCEPTION", "Resolve hard occupancy, stock, fiscal, cash and settlement exceptions before closing.")
		}
	}
	if len(snapshot.Exceptions) > 0 {
		if t.Account.IsZero() {
			return out, apperrors.Conflict("AUTOMATION_EXCEPTION_DENIED", "Automatic closure requires no exceptions; a human must resolve or independently approve eligible exceptions.")
		}
		target, e := resolve(ctx, tx, t.Organisation, "PROPERTY", in.PropertyID)
		if e != nil {
			return out, e
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_NIGHT_AUDIT_EXCEPTION", target.Scope, in.ApprovalID, map[string]any{"command": op}, "", nil, ""); e != nil {
			return out, e
		}
	}
	id := ids.New()
	var approval any
	if !in.ApprovalID.IsZero() {
		approval = in.ApprovalID
	}
	if _, e = tx.Exec(ctx, `INSERT INTO business_day_closes(id,organisation_id,property_id,business_day_id,audit_id,approval_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, in.PropertyID, d.ID, in.AuditID, approval, in.Reason, t.Account); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO business_day_close_reports(id,organisation_id,close_id,report) VALUES($1,$2,$3,$4)`, ids.New(), t.Organisation, id, marshalCommercial(map[string]any{"snapshot": snapshot, "audit_id": in.AuditID, "approval_id": approval, "snapshot_digest": digest, "closed_at": h.Auth.Service.Now().UTC()})); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `UPDATE hospitality_business_days SET state='CLOSED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, d.ID); e != nil {
		return out, e
	}
	if e = h.pmsCommit(ctx, tx, t, in.PropertyID, d.ID, "business_day", "BUSINESS_DAY_CLOSED", "night_audit.closed.v1", d.Version+1, map[string]any{"close_id": id, "audit_id": in.AuditID, "day": d.Day, "snapshot_digest": digest, "reason": in.Reason}); e != nil {
		return out, e
	}
	next, e := h.pmsDay(ctx, tx, t, in.PropertyID, false)
	if e != nil {
		return out, e
	}
	return PMSDayCloseResult{ID: id, State: "CLOSED", BusinessDayID: d.ID, NextBusinessDay: next}, nil
}
