package businesshttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Capabilities an external manager may be delegated (never ownership,
// finance, payout, staff or approval authority).
var managerDelegableCapabilities = []string{"listings:read", "content:write", "reservations:read", "reservations:write", "pms:read", "pms:write", "housekeeping:write", "maintenance:write", "leads:write", "residential:write", "inbox:reply"}

// Protected characteristics never feed risk decisions.
var protectedRiskAttributes = []string{"race", "ethnicity", "religion", "gender", "sex", "sexual_orientation", "nationality", "national_origin", "disability", "age", "marital_status", "pregnancy", "political_opinion", "tribe"}

var integrationScopes = []string{"reservations:read", "inventory:read", "rates:read", "folios:read", "guests:read", "finance:read", "listings:read", "reports:read"}

func attr(rec *OpsRecord) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(rec.Attributes, &m)
	return m
}

func attrString(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

func init() {
	opsHooks["housekeeping_task"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "before:PASS" {
			return nil
		}
		// Room-ready gate: no unresolved room-blocking maintenance.
		var blocked bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM work_orders WHERE organisation_id=$1 AND subject_id=$2 AND coalesce((attributes->>'blocks_room')::boolean,false) AND state NOT IN ('VERIFIED','CANCELLED'))`, t.Organisation, rec.SubjectID).Scan(&blocked); e != nil {
			return e
		}
		if blocked {
			return apperrors.Conflict("ROOM_BLOCKED_BY_MAINTENANCE", "A room-blocking work order must be verified before the room is released.")
		}
		return nil
	}
	opsHooks["maintenance_request"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "before:LINK_WORK_ORDER" {
			return nil
		}
		id, e := ids.ParseV7(cmd.ExternalReference)
		if e != nil {
			return apperrors.InvalidRequest("The reference must be the work order identifier.")
		}
		var ok bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM work_orders WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND state<>'CANCELLED')`, t.Organisation, rec.ScopeID, id).Scan(&ok); e != nil {
			return e
		}
		if !ok {
			return apperrors.Conflict("WORK_ORDER_NOT_FOUND", "The work order must exist in the same property.")
		}
		return nil
	}
	opsHooks["work_order"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		m := attr(rec)
		switch stage {
		case "create":
			if v, ok := m["blocks_room"]; ok {
				if _, ok := v.(bool); !ok {
					return apperrors.InvalidRequest("blocks_room must be boolean.")
				}
			}
			if v, ok := m["warranty_days"]; ok {
				if f, ok := v.(float64); !ok || f < 0 || f > 3650 {
					return apperrors.InvalidRequest("warranty_days must be 0-3650.")
				}
			}
		case "before:REWORK":
			// Rework after verification only inside the warranty window.
			if rec.State == "VERIFIED" {
				days, _ := m["warranty_days"].(float64)
				var verified *time.Time
				if e := tx.QueryRow(ctx, `SELECT max(created_at) FROM operations_history WHERE organisation_id=$1 AND entity_type='work_order' AND entity_id=$2 AND action='VERIFY'`, t.Organisation, rec.ID).Scan(&verified); e != nil {
					return e
				}
				if verified == nil || h.Auth.Service.Now().After(verified.Add(time.Duration(days)*24*time.Hour)) {
					return apperrors.Conflict("WARRANTY_EXPIRED", "Rework after verification requires an active warranty window; open a new work order.")
				}
			}
		}
		return nil
	}
	opsHooks["manager_assignment"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" && stage != "before:ACTIVATE" {
			return nil
		}
		m := attr(rec)
		for k, v := range cmd.Attributes {
			m[k] = v
		}
		caps, _ := m["capabilities"].([]any)
		if len(caps) == 0 || len(caps) > len(managerDelegableCapabilities) {
			return apperrors.InvalidRequest("Scoped delegable capabilities are required.")
		}
		for _, c := range caps {
			s, _ := c.(string)
			if !slices.Contains(managerDelegableCapabilities, s) {
				return apperrors.PermissionDenied("Capability " + s + " cannot be delegated to an external manager.")
			}
		}
		if rec.EndsAt == nil || rec.EndsAt.After(h.Auth.Service.Now().AddDate(3, 0, 0)) {
			return apperrors.InvalidRequest("A delegation expiry within three years is required.")
		}
		if stage == "before:ACTIVATE" && !opsText(attrString(m, "mandate_reference"), 3, 300) && len(cmd.EvidenceRefs) == 0 {
			return apperrors.InvalidRequest("A verified mandate reference is required.")
		}
		return nil
	}
	opsHooks["data_room_grant"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" {
			return nil
		}
		var state string
		if e := tx.QueryRow(ctx, `SELECT state FROM professional_appointments WHERE organisation_id=$1 AND id=$2`, t.Organisation, rec.ParentID).Scan(&state); e != nil {
			return opsNotFound(e)
		}
		if !slices.Contains([]string{"ACCEPTED", "IN_PROGRESS", "REPORT_SUBMITTED"}, state) {
			return apperrors.Conflict("APPOINTMENT_NOT_ACTIVE", "Grants require an accepted, active professional appointment.")
		}
		if rec.EndsAt == nil || rec.EndsAt.After(h.Auth.Service.Now().Add(90*24*time.Hour)) {
			return apperrors.InvalidRequest("Data-room grants expire within 90 days.")
		}
		if !opsText(attrString(attr(rec), "purpose"), 5, 300) {
			return apperrors.InvalidRequest("A stated purpose is required.")
		}
		return nil
	}
	opsHooks["tenancy"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		raw := attrString(attr(rec), "application_id")
		if raw == "" {
			return nil
		}
		app, e := ids.ParseV7(raw)
		if e != nil {
			return apperrors.InvalidRequest("application_id must be a rental application identifier.")
		}
		switch stage {
		case "create":
			var ok bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rental_applications WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND state='APPROVED' AND subject_id=$4)`, t.Organisation, rec.ScopeID, app, rec.SubjectID).Scan(&ok); e != nil {
				return e
			}
			if !ok {
				return apperrors.Conflict("APPLICATION_NOT_APPROVED", "The application must be approved for the same unit.")
			}
		case "after:CREATE":
			return h.opsSystemTransition(ctx, tx, t, opsDomains["rental_application"], app, "CONVERTED", "CONVERTED_TO_TENANCY")
		}
		return nil
	}
	opsHooks["sale_offer"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		var state string
		if e := tx.QueryRow(ctx, `SELECT state FROM sale_transactions WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, rec.ParentID).Scan(&state); e != nil {
			return opsNotFound(e)
		}
		switch stage {
		case "create", "before:ACCEPT":
			if state != "LISTED" && state != "UNDER_OFFER" {
				return apperrors.Conflict("SALE_NOT_OPEN", "Offers are accepted only while the sale is listed or under offer.")
			}
		case "after:ACCEPT":
			if state == "LISTED" {
				return h.opsSystemTransition(ctx, tx, t, opsDomains["sale_transaction"], *rec.ParentID, "UNDER_OFFER", "OFFER_ACCEPTED")
			}
		}
		return nil
	}
	opsHooks["sale_transaction"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "before:APPROVE" {
			return nil
		}
		var ok bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sale_offers WHERE organisation_id=$1 AND parent_id=$2 AND state='ACCEPTED')`, t.Organisation, rec.ID).Scan(&ok); e != nil {
			return e
		}
		if !ok {
			return apperrors.Conflict("NO_ACCEPTED_OFFER", "Sale approval requires an accepted offer.")
		}
		return nil
	}
	opsHooks["development_allocation"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "create":
			if rec.EndsAt == nil || rec.EndsAt.After(h.Auth.Service.Now().Add(14*24*time.Hour)) {
				return apperrors.InvalidRequest("A unit hold expires within 14 days.")
			}
		case "before:ALLOCATE":
			// The milestone schedule is frozen into the allocation.
			rows, e := commercialJSONRows(ctx, tx, `SELECT jsonb_build_object('milestone_id',id,'kind',kind,'title',title,'due_at',ends_at,'state',state) FROM construction_milestones WHERE organisation_id=$1 AND scope_id=$2 ORDER BY ends_at,id`, t.Organisation, rec.ScopeID)
			if e != nil {
				return e
			}
			if cmd.Attributes == nil {
				cmd.Attributes = map[string]any{}
			}
			cmd.Attributes["frozen_milestone_schedule"] = rows
			cmd.Attributes["schedule_frozen_at"] = h.Auth.Service.Now().UTC()
		case "before:HAND_OVER":
			var open int
			if e := tx.QueryRow(ctx, `SELECT count(*) FROM snag_items WHERE organisation_id=$1 AND scope_id=$2 AND subject_id=$3 AND state IN ('OPEN','ASSIGNED','FIXED')`, t.Organisation, rec.ScopeID, rec.SubjectID).Scan(&open); e != nil {
				return e
			}
			if open > 0 {
				return apperrors.Conflict("OPEN_SNAGS", "All snags on the unit must be verified before handover.")
			}
		}
		return nil
	}
	opsHooks["risk_case"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		check := attr(rec)
		for k, v := range cmd.Attributes {
			check[k] = v
		}
		for k := range check {
			if slices.Contains(protectedRiskAttributes, strings.ToLower(k)) {
				return apperrors.InvalidRequest("Protected personal characteristics may not be used in risk decisions.")
			}
		}
		return nil
	}
	opsHooks["financing_offer"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" {
			return nil
		}
		m := attr(rec)
		total, ok1 := m["total_cost_minor"].(float64)
		apr, ok2 := m["apr_bps"].(float64)
		term, ok3 := m["term_months"].(float64)
		if !ok1 || !ok2 || !ok3 || total < float64(*rec.AmountMinor) || apr < 0 || apr > 100000 || term < 1 || term > 480 {
			return apperrors.InvalidRequest("Total cost (>= principal), APR and term are required for cost transparency.")
		}
		return nil
	}
	opsHooks["insurance_claim"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" {
			return nil
		}
		var state string
		if e := tx.QueryRow(ctx, `SELECT state FROM insurance_policy_refs WHERE organisation_id=$1 AND id=$2`, t.Organisation, rec.ParentID).Scan(&state); e != nil {
			return opsNotFound(e)
		}
		if state != "ACTIVE" && state != "EXPIRING" {
			return apperrors.Conflict("POLICY_NOT_ACTIVE", "Claims require an active policy.")
		}
		return nil
	}
	opsHooks["access_grant"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" && stage != "before:ACTIVATE" {
			return nil
		}
		var state string
		if e := tx.QueryRow(ctx, `SELECT state FROM access_devices WHERE organisation_id=$1 AND id=$2`, t.Organisation, rec.ParentID).Scan(&state); e != nil {
			return opsNotFound(e)
		}
		if state != "ACTIVE" {
			return apperrors.Conflict("DEVICE_NOT_ACTIVE", "Access grants require an active, provider-paired device.")
		}
		if rec.StartsAt != nil && rec.EndsAt != nil && rec.EndsAt.Sub(*rec.StartsAt) > 366*24*time.Hour {
			return apperrors.InvalidRequest("Access grants are time-bounded to at most one year.")
		}
		return nil
	}
	opsHooks["integration_client"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" {
			return nil
		}
		m := attr(rec)
		scopes, _ := m["scopes"].([]any)
		if len(scopes) == 0 {
			return apperrors.InvalidRequest("Scoped integration permissions are required.")
		}
		for _, s := range scopes {
			if v, _ := s.(string); !slices.Contains(integrationScopes, v) {
				return apperrors.InvalidRequest("Unsupported integration scope.")
			}
		}
		if ref := attrString(m, "credential_reference"); !credentialRef.MatchString(ref) || ref == "" {
			return apperrors.InvalidRequest("A secret-manager credential reference name is required; secrets are never stored.")
		}
		if rl, ok := m["rate_limit_per_minute"].(float64); !ok || rl < 1 || rl > 6000 || rl != float64(int(rl)) {
			return apperrors.InvalidRequest("A whole-number rate limit between 1 and 6000 per minute is required.")
		}
		if env, present := m["environment"]; present && env != PartnerSandbox && env != PartnerProduction {
			return apperrors.InvalidRequest("environment must be SANDBOX or PRODUCTION (default SANDBOX).")
		}
		return nil
	}
	registerAnnexDRules()
}

type SaleOfferDecision struct {
	Version     int64  `json:"version"`
	Decision    string `json:"decision"`
	AmountMinor *int64 `json:"amount_minor,omitempty"`
	Currency    string `json:"currency,omitempty"`
	Reason      string `json:"reason"`
}

type ProjectInventoryCreate = map[string]any

type AnalyticsReportCreate struct {
	PropertyID ids.ID `json:"property_id"`
	Kind       string `json:"kind"`
	DateFrom   string `json:"date_from"`
	DateTo     string `json:"date_to"`
	Reason     string `json:"reason"`
}

type FolioEnsureCommand struct {
	StayID ids.ID `json:"stay_id"`
}

type AuthorityCreate struct {
	NodeID         ids.ID `json:"node_id"`
	Version        int64  `json:"version"`
	VerificationID ids.ID `json:"verification_id"`
}

func (h *Handler) registerOpsDomainExtras(r chi.Router) {
	r.Post("/sales/{id}/offers/{offer_id}/decision", h.saleOfferDecision)
	r.Get("/projects", h.projectList)
	r.Post("/projects", h.projectCreate)
	r.Get("/projects/{id}/phases", h.projectPhases)
	r.Post("/projects/{id}/phases", h.projectPhaseCreate)
	r.Get("/integrations/{id}/health", h.integrationHealth)
	r.Get("/dashboards", h.analyticsDashboard)
	r.Get("/revenue/dashboard", h.revenueDashboard)
	r.Post("/reports", h.analyticsReportCreate)
	r.Get("/reports/{id}", h.analyticsReportGet)
	r.Get("/benchmarks", h.analyticsBenchmarks)
	// Annex E compatibility routes over the canonical owning handlers.
	r.Post("/revenue/forecast", h.rmsForecastCreate)
	r.Post("/revenue/simulations", h.rmsScenarioSimulate)
	r.Post("/revenue/recommendations/{id}/approve", h.rmsRecommendationApprove)
	r.Post("/revenue/recommendations/{id}/override", h.rmsRecommendationOverride)
	r.Post("/reservations/{id}/extend", h.reservationExtend)
	r.Post("/folios", h.folioEnsure)
	r.Get("/service-recovery", h.crmRecoveryCases)
	r.Post("/service-recovery", h.crmRecoveryCaseCreate)
	r.Post("/privacy-requests", h.privacyRequestCreate)
	r.Get("/tax-policy", h.taxPolicyGet)
	r.Put("/tax-policy", h.taxPolicyPut)
	r.Get("/corporate-rates", h.corporateRates)
	r.Post("/corporate-rates", h.corporateRateSave)
	r.Post("/offline-sync", h.pmsOfflineReplay)
	r.Get("/reconciliation-tasks", h.pmsOfflineTasks)
	r.Post("/reconciliation-tasks/{id}/resolve", h.pmsOfflineResolve)
	r.Get("/authority", h.authorityList)
	r.Post("/authority", h.authorityCreate)
	r.Post("/authority/{id}/verification", h.authorityVerification)
	r.Post("/listings/{id}/preview", h.listingPreview)
	r.Post("/listings/{id}/submit-approval", h.listingSubmitApproval)
	r.Get("/listings/{id}/quality", h.listingQualityAlias)
	r.Get("/residential/arrears", h.residentialArrears)
	r.Get("/assets/{id}/service-history", h.assetServiceHistory)
}

// withURLParam rebinds a chi route parameter for a delegated owning handler.
func withURLParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	if old := chi.RouteContext(r.Context()); old != nil {
		for i, k := range old.URLParams.Keys {
			if k != key {
				rctx.URLParams.Add(k, old.URLParams.Values[i])
			}
		}
	}
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// rewriteBody replaces the request body with a transformed JSON object.
func rewriteBody(w http.ResponseWriter, r *http.Request, fn func(map[string]any) error) (*http.Request, bool) {
	raw, e := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Unreadable body."))
		return nil, false
	}
	m := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if e = json.Unmarshal(raw, &m); e != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("A JSON object body is required."))
			return nil, false
		}
	}
	if e = fn(m); e != nil {
		reply(w, r, 0, nil, e)
		return nil, false
	}
	out, _ := json.Marshal(m)
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(out))
	r2.ContentLength = int64(len(out))
	return r2, true
}

func (h *Handler) saleOfferDecision(w http.ResponseWriter, r *http.Request) {
	var in SaleOfferDecision
	if !decode(w, r, &in) {
		return
	}
	action := map[string]string{"ACCEPT": "ACCEPT", "REJECT": "REJECT", "COUNTER": "COUNTER"}[in.Decision]
	if action == "" {
		reply(w, r, 0, nil, apperrors.InvalidRequest("decision must be ACCEPT, REJECT or COUNTER."))
		return
	}
	body, _ := json.Marshal(OpsRecordTransition{Version: in.Version, Action: action, AmountMinor: in.AmountMinor, Currency: in.Currency, Reason: in.Reason})
	r2 := withURLParam(r, "record_id", chi.URLParam(r, "offer_id"))
	r2.Body = io.NopCloser(bytes.NewReader(body))
	h.opsDomainTransition(w, r2, opsDomains["sale_offer"], "")
}

func (h *Handler) projectList(w http.ResponseWriter, r *http.Request) {
	h.inventoryList(w, r, portfolio.Development, false)
}
func (h *Handler) projectCreate(w http.ResponseWriter, r *http.Request) {
	h.inventoryCreate(w, r, portfolio.Development, false)
}
func (h *Handler) projectPhases(w http.ResponseWriter, r *http.Request) {
	h.inventoryList(w, r, portfolio.Phase, true)
}
func (h *Handler) projectPhaseCreate(w http.ResponseWriter, r *http.Request) {
	h.inventoryCreate(w, r, portfolio.Phase, true)
}

func (h *Handler) rmsRecommendationApprove(w http.ResponseWriter, r *http.Request) {
	if r2, ok := rewriteBody(w, r, func(m map[string]any) error { m["action"] = "APPROVE"; return nil }); ok {
		h.rmsRecommendationDecide(w, r2)
	}
}
func (h *Handler) rmsRecommendationOverride(w http.ResponseWriter, r *http.Request) {
	if r2, ok := rewriteBody(w, r, func(m map[string]any) error { m["action"] = "OVERRIDE"; return nil }); ok {
		h.rmsRecommendationDecide(w, r2)
	}
}

// reservationExtend resolves the reservation's active stay and delegates to
// the canonical in-house stay extension (Section 10).
func (h *Handler) reservationExtend(w http.ResponseWriter, r *http.Request) {
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var stay ids.ID
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT id FROM stay_records WHERE organisation_id=$1 AND reservation_id=$2 AND state='ACTIVE' ORDER BY checked_in_at DESC LIMIT 1`, t.Organisation, id).Scan(&stay); e != nil {
		reply(w, r, 0, nil, apperrors.Conflict("NO_ACTIVE_STAY", "Extension applies to an in-house reservation with an active stay; use reservation amendment before arrival."))
		return
	}
	h.pmsStayExtend(w, withURLParam(r, "id", stay.String()))
}

// folioEnsure returns the canonical folio of a checked-in stay. Folios are
// opened by check-in; no detached folio is ever fabricated.
func (h *Handler) folioEnsure(w http.ResponseWriter, r *http.Request) {
	var in FolioEnsureCommand
	if !decode(w, r, &in) {
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var property ids.ID
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT property_id FROM stay_records WHERE organisation_id=$1 AND id=$2`, t.Organisation, in.StayID).Scan(&property); e != nil {
		reply(w, r, 0, nil, opsNotFound(e))
		return
	}
	if t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "folios:read", "PROPERTY", property, false); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out, e := oneJSON(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(f) FROM folios f WHERE organisation_id=$1 AND stay_id=$2`, t.Organisation, in.StayID)
	if e != nil {
		reply(w, r, 0, nil, apperrors.Conflict("FOLIO_OPENED_AT_CHECK_IN", "Folios are opened by check-in for an actual stay."))
		return
	}
	reply(w, r, 200, out, nil)
}

func (h *Handler) privacyRequestCreate(w http.ResponseWriter, r *http.Request) {
	var guest string
	r2, ok := rewriteBody(w, r, func(m map[string]any) error {
		guest, _ = m["guest_id"].(string)
		delete(m, "guest_id")
		if _, e := ids.ParseV7(guest); e != nil {
			return apperrors.InvalidRequest("guest_id is required.")
		}
		return nil
	})
	if ok {
		h.crmPrivacyRequestCreate(w, withURLParam(r2, "id", guest))
	}
}

func (h *Handler) taxPolicyGet(w http.ResponseWriter, r *http.Request) { h.taxList(w, r) }
func (h *Handler) taxPolicyPut(w http.ResponseWriter, r *http.Request) { h.taxCreate(w, r) }

func (h *Handler) corporateRates(w http.ResponseWriter, r *http.Request) {
	corporate := r.URL.Query().Get("corporate_id")
	if _, e := ids.ParseV7(corporate); e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("corporate_id is required."))
		return
	}
	h.crmCorporateContracts(w, withURLParam(r, "id", corporate))
}
func (h *Handler) corporateRateSave(w http.ResponseWriter, r *http.Request) {
	var corporate string
	r2, ok := rewriteBody(w, r, func(m map[string]any) error {
		corporate, _ = m["corporate_id"].(string)
		delete(m, "corporate_id")
		if _, e := ids.ParseV7(corporate); e != nil {
			return apperrors.InvalidRequest("corporate_id is required.")
		}
		return nil
	})
	if ok {
		h.crmCorporateContractSave(w, withURLParam(r2, "id", corporate))
	}
}

func (h *Handler) authorityList(w http.ResponseWriter, r *http.Request) {
	property, e := ids.ParseV7(r.URL.Query().Get("property_id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A property_id scope is required."))
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "property:read", "PROPERTY", property, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(v) FROM inventory_verification_links v WHERE v.organisation_id=$1 AND v.node_id=$2 ORDER BY v.provider_updated_at DESC LIMIT 100`, t.Organisation, property)
	reply(w, r, 200, map[string]any{"items": items, "authority": "verification-service"}, e)
}

func (h *Handler) authorityCreate(w http.ResponseWriter, r *http.Request) {
	var node string
	r2, ok := rewriteBody(w, r, func(m map[string]any) error {
		node, _ = m["node_id"].(string)
		delete(m, "node_id")
		if _, e := ids.ParseV7(node); e != nil {
			return apperrors.InvalidRequest("node_id is required.")
		}
		return nil
	})
	if ok {
		h.inventoryVerificationLink(w, withURLParam(r2, "id", node))
	}
}
func (h *Handler) authorityVerification(w http.ResponseWriter, r *http.Request) {
	h.inventoryVerificationLink(w, r)
}
func (h *Handler) listingPreview(w http.ResponseWriter, r *http.Request) { h.listingReadiness(w, r) }
func (h *Handler) listingSubmitApproval(w http.ResponseWriter, r *http.Request) {
	h.listingSubmit(w, r)
}
func (h *Handler) listingQualityAlias(w http.ResponseWriter, r *http.Request) {
	h.listingReadiness(w, r)
}

// integrationHealth derives health from the integration lifecycle record and
// its recorded history; credentials are never returned.
func (h *Handler) integrationHealth(w http.ResponseWriter, r *http.Request) {
	t, rec, e := h.opsRecordTarget(r, opsDomains["integration_client"], "integrations:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var last *time.Time
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT max(created_at) FROM operations_history WHERE organisation_id=$1 AND entity_type='integration_client' AND entity_id=$2`, t.Organisation, rec.ID).Scan(&last); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	status := map[string]string{"ACTIVE": "HEALTHY", "SUSPENDED": "SUSPENDED", "REVOKED": "REVOKED", "DRAFT": "NOT_ACTIVATED"}[rec.State]
	reply(w, r, 200, map[string]any{"integration_id": rec.ID, "state": rec.State, "status": status, "last_change_at": last, "database_access": false, "scopes": attr(&rec)["scopes"]}, nil)
}

// internalManagerAuthorization lets the property-manager service check a
// live delegation; revocation or expiry denies immediately.
func (h *Handler) internalManagerAuthorization(w http.ResponseWriter, r *http.Request) {
	p, ok := serviceauth.PrincipalFrom(r.Context())
	id, e := pathCommercialID(r)
	property, pe := ids.ParseV7(r.URL.Query().Get("property_id"))
	capability := r.URL.Query().Get("capability")
	if !ok || p.Service != platform.PropertyManagerService || p.Delegation.OrganizationID.IsZero() || e != nil || pe != nil || capability == "" {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Signed property-manager service, organisation, property and capability required."))
		return
	}
	rec, e := opsGet[OpsRecord](r.Context(), h.Auth.Service.Pool, "manager_assignment_refs", p.Delegation.OrganizationID, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	caps, _ := attr(&rec)["capabilities"].([]any)
	allowed := false
	for _, c := range caps {
		if c == capability {
			allowed = true
		}
	}
	now := h.Auth.Service.Now().UTC()
	allowed = allowed && (rec.State == "ACTIVE" || rec.State == "EXPIRING") && rec.ScopeID == property && rec.EndsAt != nil && now.Before(*rec.EndsAt)
	reply(w, r, 200, map[string]any{"assignment_id": rec.ID, "allowed": allowed, "state": rec.State, "expires_at": rec.EndsAt}, nil)
}

// internalDataRoomAuthorization lets Media & Document Service enforce a
// purpose- and time-limited grant before issuing a signed download.
func (h *Handler) internalDataRoomAuthorization(w http.ResponseWriter, r *http.Request) {
	p, ok := serviceauth.PrincipalFrom(r.Context())
	id, e := pathCommercialID(r)
	if !ok || p.Service != platform.MediaDocumentService && p.Service != platform.ProfessionalServicesService || p.Delegation.OrganizationID.IsZero() || e != nil {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Signed Media & Document or Professional Services service and organisation required."))
		return
	}
	rec, e := opsGet[OpsRecord](r.Context(), h.Auth.Service.Pool, "data_room_grants", p.Delegation.OrganizationID, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if p.Service == platform.ProfessionalServicesService {
		// A professional sees only grants on their own appointment.
		account, ae := ids.ParseV7(r.URL.Query().Get("account_id"))
		var counterparty string
		if ae != nil || h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT counterparty_reference FROM professional_appointments WHERE organisation_id=$1 AND id=$2`, p.Delegation.OrganizationID, rec.ParentID).Scan(&counterparty) != nil || counterparty != PartnerReference(platform.ProfessionalServicesService, account) {
			reply(w, r, 0, nil, apperrors.NotFound("RESOURCE_NOT_FOUND", "Resource not found."))
			return
		}
	}
	now := h.Auth.Service.Now().UTC()
	allowed := rec.State == "ACTIVE" && rec.EndsAt != nil && now.Before(*rec.EndsAt)
	if allowed && rec.SubjectID != nil {
		var open bool
		if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT state='OPEN' FROM data_rooms WHERE organisation_id=$1 AND id=$2`, p.Delegation.OrganizationID, *rec.SubjectID).Scan(&open); e != nil || !open {
			allowed = false
		}
	}
	reply(w, r, 200, map[string]any{"grant_id": rec.ID, "allowed": allowed, "download_policy": rec.Kind, "purpose": attr(&rec)["purpose"], "expires_at": rec.EndsAt, "watermark": rec.Kind == "WATERMARKED_DOWNLOAD"}, nil)
}

// opsSweep is one date-driven system transition applied by the worker.
type opsSweep struct {
	domain, query, to, action string
}

// ProcessOperations runs Section 16/19 domain workers: expiries, hold
// release, housekeeping turnover scheduling and preventive maintenance.
func (h *Handler) ProcessOperations(ctx context.Context, limit int) error {
	now := h.Auth.Service.Now().UTC()
	type sweep = opsSweep
	sweeps := []sweep{
		{"manager_assignment", `SELECT organisation_id,id FROM manager_assignment_refs WHERE state IN ('ACTIVE','EXPIRING','SUSPENDED') AND ends_at<=$1 LIMIT $2`, "EXPIRED", "DELEGATION_EXPIRED"},
		{"manager_assignment", `SELECT organisation_id,id FROM manager_assignment_refs WHERE state='ACTIVE' AND ends_at<=$1::timestamptz+interval '7 days' AND ends_at>$1 LIMIT $2`, "EXPIRING", "DELEGATION_EXPIRING"},
		{"data_room_grant", `SELECT organisation_id,id FROM data_room_grants WHERE state='ACTIVE' AND ends_at<=$1 LIMIT $2`, "EXPIRED", "GRANT_EXPIRED"},
		{"access_grant", `SELECT organisation_id,id FROM access_grants WHERE state IN ('REQUESTED','ACTIVE') AND ends_at<=$1 LIMIT $2`, "EXPIRED", "ACCESS_EXPIRED"},
		{"insurance_policy", `SELECT organisation_id,id FROM insurance_policy_refs WHERE state IN ('ACTIVE','EXPIRING') AND ends_at<=$1 LIMIT $2`, "EXPIRED", "POLICY_EXPIRED"},
		{"insurance_policy", `SELECT organisation_id,id FROM insurance_policy_refs WHERE state='ACTIVE' AND ends_at<=$1::timestamptz+interval '30 days' AND ends_at>$1 LIMIT $2`, "EXPIRING", "POLICY_EXPIRING"},
		{"development_allocation", `SELECT organisation_id,id FROM development_allocations WHERE state='HELD' AND ends_at<=$1 LIMIT $2`, "RELEASED", "HOLD_EXPIRED"},
	}
	sweeps = append(sweeps, opsGeneratedSweeps...)
	var failures []error
	for _, s := range sweeps {
		rows, e := h.Auth.Service.Pool.Query(ctx, s.query, now, limit)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		type ref struct{ org, id ids.ID }
		refs := []ref{}
		for rows.Next() {
			var x ref
			if e = rows.Scan(&x.org, &x.id); e == nil {
				refs = append(refs, x)
			}
		}
		rows.Close()
		for _, x := range refs {
			e := withServiceTx(ctx, h, func(ctx context.Context, tx pgx.Tx) error {
				return h.opsSystemTransition(ctx, tx, workerTenant(x.org), opsDomains[s.domain], x.id, s.to, s.action)
			})
			var ae *apperrors.Error
			if e != nil && !(errors.As(e, &ae) && ae.Code == "INVALID_TRANSITION") {
				failures = append(failures, e)
			}
		}
	}
	failures = append(failures, h.scheduleTurnovers(ctx, limit), h.schedulePreventive(ctx, limit), h.scheduleRentObligations(ctx, limit))
	return errors.Join(failures...)
}

// scheduleTurnovers creates one turnover task for each dirty room without a
// live housekeeping task (canonical PMS room status is the source).
func (h *Handler) scheduleTurnovers(ctx context.Context, limit int) error {
	return withServiceTx(ctx, h, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT s.organisation_id,s.property_id,s.room_id FROM room_status_current s WHERE s.state='VACANT_DIRTY' AND NOT EXISTS(SELECT 1 FROM housekeeping_tasks t WHERE t.organisation_id=s.organisation_id AND t.subject_id=s.room_id AND t.state IN ('PLANNED','ASSIGNED','IN_PROGRESS','INSPECTION','REWORK')) LIMIT $1`, limit)
		if e != nil {
			return e
		}
		type room struct{ org, property, room ids.ID }
		list := []room{}
		for rows.Next() {
			var x room
			if e = rows.Scan(&x.org, &x.property, &x.room); e != nil {
				rows.Close()
				return e
			}
			list = append(list, x)
		}
		rows.Close()
		d := opsDomains["housekeeping_task"]
		for _, x := range list {
			id := ids.New()
			if _, e = tx.Exec(ctx, `INSERT INTO housekeeping_tasks(id,organisation_id,scope_kind,scope_id,property_id,subject_id,kind,state,title,attributes) VALUES($1,$2,'PROPERTY',$3,$3,$4,'TURNOVER','PLANNED','Turnover after departure','{"source":"ROOM_STATUS_VACANT_DIRTY"}') ON CONFLICT DO NOTHING`, id, x.org, x.property, x.room); e != nil {
				return e
			}
			rec, e := opsGet[OpsRecord](ctx, tx, d.Table, x.org, id)
			if e != nil {
				continue
			}
			if e = h.opsDomainCommit(ctx, tx, workerTenant(x.org), d, rec, "SCHEDULED", "", "system:turnover"); e != nil {
				return e
			}
		}
		return nil
	})
}

// schedulePreventive creates due preventive work orders and advances the
// schedule's next due date exactly once per due period.
func (h *Handler) schedulePreventive(ctx context.Context, limit int) error {
	return withServiceTx(ctx, h, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT organisation_id,property_id,id,subject_id,title,coalesce((attributes->>'interval_days')::int,30),coalesce((attributes->>'next_due_on')::date,starts_at::date) FROM preventive_schedules WHERE state='ACTIVE' AND coalesce((attributes->>'next_due_on')::date,starts_at::date)<=current_date ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
		if e != nil {
			return e
		}
		type sched struct {
			org, property, id, subject ids.ID
			title                      string
			interval                   int
			due                        time.Time
		}
		list := []sched{}
		for rows.Next() {
			var x sched
			if e = rows.Scan(&x.org, &x.property, &x.id, &x.subject, &x.title, &x.interval, &x.due); e != nil {
				rows.Close()
				return e
			}
			list = append(list, x)
		}
		rows.Close()
		wo := opsDomains["work_order"]
		for _, x := range list {
			if x.interval < 1 || x.interval > 3650 {
				x.interval = 30
			}
			id := ids.New()
			if _, e = tx.Exec(ctx, `INSERT INTO work_orders(id,organisation_id,scope_kind,scope_id,property_id,subject_id,kind,state,title,attributes) VALUES($1,$2,'PROPERTY',$3,$3,$4,'PREVENTIVE','DRAFT',$5,jsonb_build_object('schedule_id',$6::text,'due_on',$7::date))`, id, x.org, x.property, x.subject, truncate("Preventive: "+x.title, 200), x.id, x.due); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE preventive_schedules SET attributes=attributes||jsonb_build_object('next_due_on',($2::date+$3)::text,'last_work_order_id',$4::text),version=version+1,updated_at=now() WHERE id=$1`, x.id, x.due, x.interval, id); e != nil {
				return e
			}
			rec, e := opsGet[OpsRecord](ctx, tx, wo.Table, x.org, id)
			if e != nil {
				return e
			}
			if e = h.opsDomainCommit(ctx, tx, workerTenant(x.org), wo, rec, "PREVENTIVE_SCHEDULED", "", "system:preventive"); e != nil {
				return e
			}
		}
		return nil
	})
}
