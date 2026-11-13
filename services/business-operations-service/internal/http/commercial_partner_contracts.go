package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// Owning-service partner contracts. The property-manager, artisan,
// professional-services and admin services own their actors and work; they
// report outcomes back to Business through these signed internal endpoints.
// A partner may only act on a Business record that names one of its
// accounts as counterparty ("<service>:account:<uuid>") — except Admin, the
// platform review authority for disputes — and only through the explicitly
// allow-listed actions below. Business rules (states, evidence, references,
// amounts, domain hooks, history, audit and outbox) are applied exactly as for
// staff transitions; MFA/approval/separation-of-duties rules of human staff
// do not transfer to services and are not bypassed: none of the allow-listed
// actions carries a Business staff approval.

// partnerActions is the per-caller allow-list (service -> domain -> actions).
var partnerActions = map[platform.Service]map[string][]string{
	platform.ArtisanService: {
		"provider_assignment": {"RECORD_ACCEPTANCE", "RECORD_ARRIVAL", "RECORD_COMPLETION", "RECORD_DECLINE"},
		"vendor_quote":        {"RECORD_QUOTE", "WITHDRAW"},
	},
	platform.ProfessionalServicesService: {
		"professional_appointment": {"ACCEPT", "START", "SUBMIT_REPORT"},
		"progress_certification":   {"START_REVIEW", "CERTIFY", "REJECT"},
	},
	platform.AdminService: {
		"dispute": {"RECORD_DECISION"},
	},
}

// Partner-visible attribute keys in event snapshots and readbacks.
var partnerAttributes = map[string][]string{
	"manager_assignment":       {"capabilities", "mandate_reference"},
	"provider_assignment":      {"scope_of_work", "rework_of", "warranty_days", "warranty_reference"},
	"vendor_quote":             {"scope_of_work"},
	"professional_appointment": {"purpose"},
	"progress_certification":   {"milestone_id", "percent_complete"},
	"dispute":                  {"claim_summary", "requested_remedy_minor"},
}

// PartnerReference is the counterparty reference naming a partner account.
func PartnerReference(svc platform.Service, account ids.ID) string {
	return string(svc) + ":account:" + account.String()
}

func publicAttributes(key string, raw json.RawMessage) map[string]any {
	all := map[string]any{}
	_ = json.Unmarshal(raw, &all)
	out := map[string]any{}
	for _, k := range partnerAttributes[key] {
		if v, ok := all[k]; ok {
			out[k] = v
		}
	}
	return out
}

// PartnerTransition is a partner-reported outcome.
type PartnerTransition struct {
	AccountID         ids.ID         `json:"account_id"`
	Action            string         `json:"action"`
	Version           int64          `json:"version,omitempty"`
	ExternalReference string         `json:"external_reference,omitempty"`
	EvidenceRefs      []ids.ID       `json:"evidence_refs,omitempty"`
	AmountMinor       *int64         `json:"amount_minor,omitempty"`
	Currency          string         `json:"currency,omitempty"`
	Attributes        map[string]any `json:"attributes,omitempty"`
	Reason            string         `json:"reason"`
}

// PartnerRecord is the partner readback projection.
type PartnerRecord struct {
	Domain     string         `json:"domain"`
	Record     OpsRecord      `json:"record"`
	Attributes map[string]any `json:"attributes"`
	Allowed    []string       `json:"allowed_actions"`
}

func partnerCaller(r *http.Request) (serviceauth.Principal, error) {
	p, ok := serviceauth.PrincipalFrom(r.Context())
	if !ok || p.Delegation.OrganizationID.IsZero() {
		return p, apperrors.PermissionDenied("A signed partner service with organisation delegation is required.")
	}
	return p, nil
}

func partnerDomain(r *http.Request, caller platform.Service) (*opsDomain, error) {
	key := chi.URLParam(r, "domain")
	d := opsDomains[key]
	if d == nil || partnerActions[caller][key] == nil && !(caller == platform.PropertyManagerService && key == "manager_assignment") {
		return nil, apperrors.PermissionDenied("This partner service has no contract for that record type.")
	}
	return d, nil
}

// partnerOwns verifies the record names the partner account as counterparty
// (Admin reviews disputes as platform authority instead).
func partnerOwns(caller platform.Service, d *opsDomain, rec OpsRecord, account ids.ID) error {
	if caller == platform.AdminService && d.Key == "dispute" {
		return nil
	}
	if account.ValidateV7() != nil || rec.CounterpartyReference != PartnerReference(caller, account) {
		return apperrors.NotFound("RESOURCE_NOT_FOUND", "Resource not found.")
	}
	return nil
}

func (h *Handler) internalPartnerRecord(w http.ResponseWriter, r *http.Request) {
	p, e := partnerCaller(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	d, e := partnerDomain(r, p.Service)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := ids.ParseV7(chi.URLParam(r, "id"))
	account, ae := ids.ParseV7(r.URL.Query().Get("account_id"))
	if e != nil || ae != nil && p.Service != platform.AdminService {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Canonical record and partner account identifiers are required."))
		return
	}
	rec, e := opsGet[OpsRecord](r.Context(), h.Auth.Service.Pool, d.Table, p.Delegation.OrganizationID, id)
	if e == nil {
		e = partnerOwns(p.Service, d, rec, account)
	}
	reply(w, r, 200, PartnerRecord{Domain: d.Key, Record: rec, Attributes: publicAttributes(d.Key, rec.Attributes), Allowed: partnerActions[p.Service][d.Key]}, e)
}

func (h *Handler) internalPartnerTransition(w http.ResponseWriter, r *http.Request) {
	p, e := partnerCaller(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	d, e := partnerDomain(r, p.Service)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := ids.ParseV7(chi.URLParam(r, "id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A canonical record identifier is required."))
		return
	}
	var in PartnerTransition
	if !decode(w, r, &in) {
		return
	}
	var out OpsRecord
	e = withServiceTx(r.Context(), h, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = h.partnerTransition(ctx, tx, p.Delegation.OrganizationID, p.Service, d, id, in)
		return err
	})
	reply(w, r, 200, PartnerRecord{Domain: d.Key, Record: out, Attributes: publicAttributes(d.Key, out.Attributes), Allowed: partnerActions[p.Service][d.Key]}, e)
}

// partnerTransition applies one allow-listed action reported by a partner
// service. Repeating an already-applied action returns the current record
// (idempotent replay); any other state is rejected.
func (h *Handler) partnerTransition(ctx context.Context, tx pgx.Tx, org ids.ID, caller platform.Service, d *opsDomain, id ids.ID, in PartnerTransition) (OpsRecord, error) {
	rule, known := d.Transitions[in.Action]
	if !known || !slices.Contains(partnerActions[caller][d.Key], in.Action) || !validStaffReason(in.Reason) || !validEvidence(in.EvidenceRefs) || len(in.ExternalReference) > 300 || in.AmountMinor != nil && (*in.AmountMinor < 0 || !validCurrency(in.Currency)) {
		return OpsRecord{}, apperrors.InvalidRequest("An allow-listed action, reason and valid evidence/reference/amount are required.")
	}
	t := workerTenant(org)
	rec, e := opsLock[OpsRecord](ctx, tx, d.Table, org, id)
	if e != nil {
		return rec, opsNotFound(e)
	}
	if e = partnerOwns(caller, d, rec, in.AccountID); e != nil {
		return rec, e
	}
	if rec.State == rule.To && rec.LastAction == in.Action {
		return rec, nil
	}
	if in.Version > 0 && rec.Version != in.Version {
		return rec, apperrors.Conflict("VERSION_CONFLICT", "The record version has changed.")
	}
	if !slices.Contains(rule.From, rec.State) {
		return rec, apperrors.Conflict("INVALID_TRANSITION", in.Action+" is not allowed from "+rec.State+".")
	}
	if rule.Evidence && len(in.EvidenceRefs) == 0 || rule.Reference && !opsText(in.ExternalReference, 3, 300) || rule.Amount && in.AmountMinor == nil {
		return rec, apperrors.InvalidRequest(in.Action + " requires the partner's evidence, reference and amount as declared by the Business lifecycle.")
	}
	if caller == platform.ProfessionalServicesService && (in.Action == "CERTIFY" || in.Action == "SUBMIT_REPORT") && !opsText(attrString(in.Attributes, "professional_verification_reference"), 3, 300) {
		return rec, apperrors.PermissionDenied("Only a verified professional can certify or submit a report.")
	}
	cmd := OpsRecordTransition{Version: rec.Version, Action: in.Action, AmountMinor: in.AmountMinor, Currency: in.Currency, ExternalReference: in.ExternalReference, EvidenceRefs: in.EvidenceRefs, Attributes: in.Attributes, Reason: in.Reason}
	if cmd.Attributes == nil {
		cmd.Attributes = map[string]any{}
	}
	cmd.Attributes["reported_by_service"] = string(caller)
	if !in.AccountID.IsZero() {
		cmd.Attributes["reported_by_account"] = in.AccountID.String()
	}
	if hook := opsHooks[d.Key]; hook != nil {
		if e = hook(ctx, tx, h, t, d, &rec, "before:"+in.Action, &cmd); e != nil {
			return rec, e
		}
	}
	amount, currency := rec.AmountMinor, ""
	if rec.Currency != nil {
		currency = *rec.Currency
	}
	if cmd.AmountMinor != nil {
		amount, currency = cmd.AmountMinor, cmd.Currency
	}
	attributes := attr(&rec)
	for k, v := range cmd.Attributes {
		attributes[k] = v
	}
	var evidence []ids.ID
	_ = json.Unmarshal(rec.EvidenceRefs, &evidence)
	evidence = append(evidence, cmd.EvidenceRefs...)
	if len(evidence) > 50 {
		return rec, apperrors.InvalidRequest("Evidence reference limit reached.")
	}
	var currencyRef any
	if amount != nil {
		currencyRef = currency
	}
	reference := rec.ExternalReference
	if cmd.ExternalReference != "" {
		reference = cmd.ExternalReference
	}
	terminal := !slices.ContainsFunc(d.Edges, func(e string) bool { return strings.HasPrefix(e, rule.To+">") })
	if _, e = tx.Exec(ctx, `UPDATE `+d.Table+` SET state=$3,amount_minor=$4,currency=$5,external_reference=$6,attributes=$7,evidence_refs=$8,last_actor_id=NULL,last_action=$9,closed_at=CASE WHEN $10 THEN now() ELSE closed_at END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`,
		org, rec.ID, rule.To, amount, currencyRef, reference, marshalCommercial(attributes), marshalCommercial(evidence), in.Action, terminal); e != nil {
		return rec, e
	}
	out, e := opsGet[OpsRecord](ctx, tx, d.Table, org, rec.ID)
	if e != nil {
		return out, e
	}
	if hook := opsHooks[d.Key]; hook != nil {
		if e = hook(ctx, tx, h, t, d, &out, "after:"+in.Action, &cmd); e != nil {
			return out, e
		}
	}
	return out, h.opsDomainCommit(ctx, tx, t, d, out, in.Action, rec.State, "partner:"+string(caller)+":"+in.Reason)
}

// ManagerActionRequest is a property-manager delegated maintenance request.
type ManagerActionRequest struct {
	AccountID  ids.ID  `json:"account_id"`
	RequestKey string  `json:"request_key"`
	Kind       string  `json:"kind"`
	Title      string  `json:"title"`
	SubjectID  *ids.ID `json:"subject_id,omitempty"`
	Reason     string  `json:"reason"`
}

// internalManagerMaintenanceRequest performs a delegated action on behalf of
// an external manager: the live Business delegation (state, expiry, property
// scope and capability) is re-validated in the same transaction.
func (h *Handler) internalManagerMaintenanceRequest(w http.ResponseWriter, r *http.Request) {
	p, e := partnerCaller(r)
	if e == nil && p.Service != platform.PropertyManagerService {
		e = apperrors.PermissionDenied("Signed property-manager service required.")
	}
	id, ie := ids.ParseV7(chi.URLParam(r, "id"))
	var in ManagerActionRequest
	if e == nil && ie != nil {
		e = apperrors.InvalidRequest("A canonical assignment identifier is required.")
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if !decode(w, r, &in) {
		return
	}
	d := opsDomains["maintenance_request"]
	if !slices.Contains(d.Kinds, in.Kind) || !opsText(in.Title, 3, 200) || !validStaffReason(in.Reason) || !opsText(in.RequestKey, 8, 120) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Supported kind, title, reason and request key are required."))
		return
	}
	org := p.Delegation.OrganizationID
	var out OpsRecord
	status := 201
	e = withServiceTx(r.Context(), h, func(ctx context.Context, tx pgx.Tx) error {
		a, err := opsLock[OpsRecord](ctx, tx, "manager_assignment_refs", org, id)
		if err != nil {
			return opsNotFound(err)
		}
		if err = partnerOwns(platform.PropertyManagerService, opsDomains["manager_assignment"], a, in.AccountID); err != nil {
			return err
		}
		caps, _ := attr(&a)["capabilities"].([]any)
		now := h.Auth.Service.Now().UTC()
		if a.State != "ACTIVE" && a.State != "EXPIRING" || a.EndsAt == nil || !now.Before(*a.EndsAt) || !slices.Contains(caps, any("maintenance:write")) {
			return apperrors.PermissionDenied("The management delegation is not active for maintenance:write on this property.")
		}
		reference := "pm-request:" + in.RequestKey
		var existing ids.ID
		if err = tx.QueryRow(ctx, `SELECT id FROM maintenance_requests WHERE organisation_id=$1 AND property_id=$2 AND external_reference=$3`, org, a.ScopeID, reference).Scan(&existing); err == nil {
			out, err = opsGet[OpsRecord](ctx, tx, d.Table, org, existing)
			status = 200
			return err
		}
		if in.SubjectID != nil {
			if err = opsSubject(ctx, tx, org, d, "PROPERTY", a.ScopeID, *in.SubjectID); err != nil {
				return err
			}
		}
		rid := ids.New()
		attrs := marshalCommercial(map[string]any{"delegated_by_assignment": a.ID.String(), "manager_account": in.AccountID.String(), "delegated_capability": "maintenance:write"})
		if _, err = tx.Exec(ctx, `INSERT INTO maintenance_requests(id,organisation_id,scope_kind,scope_id,property_id,subject_id,kind,state,title,counterparty_reference,external_reference,attributes) VALUES($1,$2,'PROPERTY',$3,$3,$4,$5,'OPEN',$6,$7,$8,$9)`,
			rid, org, a.ScopeID, in.SubjectID, in.Kind, in.Title, a.CounterpartyReference, reference, attrs); err != nil {
			return err
		}
		if out, err = opsGet[OpsRecord](ctx, tx, d.Table, org, rid); err != nil {
			return err
		}
		return h.opsDomainCommit(ctx, tx, workerTenant(org), d, out, "CREATED_BY_DELEGATED_MANAGER", "", in.Reason)
	})
	reply(w, r, status, out, e)
}

// ProfessionalFindingCreate is a finding reported by the appointed professional.
type ProfessionalFindingCreate struct {
	AccountID             ids.ID   `json:"account_id"`
	FindingKey            string   `json:"finding_key"`
	Kind                  string   `json:"kind"`
	Title                 string   `json:"title"`
	EvidenceRefs          []ids.ID `json:"evidence_refs"`
	VerificationReference string   `json:"professional_verification_reference"`
	Reason                string   `json:"reason"`
}

func (h *Handler) internalProfessionalFinding(w http.ResponseWriter, r *http.Request) {
	p, e := partnerCaller(r)
	if e == nil && p.Service != platform.ProfessionalServicesService {
		e = apperrors.PermissionDenied("Signed professional-services service required.")
	}
	id, ie := ids.ParseV7(chi.URLParam(r, "id"))
	if e == nil && ie != nil {
		e = apperrors.InvalidRequest("A canonical appointment identifier is required.")
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var in ProfessionalFindingCreate
	if !decode(w, r, &in) {
		return
	}
	d := opsDomains["professional_finding"]
	if !slices.Contains(d.Kinds, in.Kind) || !opsText(in.Title, 3, 200) || !validStaffReason(in.Reason) || len(in.EvidenceRefs) == 0 || !validEvidence(in.EvidenceRefs) || !opsText(in.FindingKey, 8, 120) || !opsText(in.VerificationReference, 3, 300) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Kind, title, reason, evidence, finding key and the professional's verification reference are required."))
		return
	}
	org := p.Delegation.OrganizationID
	var out OpsRecord
	status := 201
	e = withServiceTx(r.Context(), h, func(ctx context.Context, tx pgx.Tx) error {
		appt, err := opsLock[OpsRecord](ctx, tx, "professional_appointments", org, id)
		if err != nil {
			return opsNotFound(err)
		}
		if err = partnerOwns(platform.ProfessionalServicesService, opsDomains["professional_appointment"], appt, in.AccountID); err != nil {
			return err
		}
		reference := "prof-finding:" + in.FindingKey
		var existing ids.ID
		if err = tx.QueryRow(ctx, `SELECT id FROM professional_findings WHERE organisation_id=$1 AND parent_id=$2 AND external_reference=$3`, org, appt.ID, reference).Scan(&existing); err == nil {
			out, err = opsGet[OpsRecord](ctx, tx, d.Table, org, existing)
			status = 200
			return err
		}
		rec := OpsRecord{ID: ids.New(), ScopeKind: "PROPERTY", ScopeID: appt.ScopeID, PropertyID: appt.PropertyID, ParentID: &appt.ID, Kind: in.Kind, Title: in.Title, Attributes: marshalCommercial(map[string]any{"professional_verification_reference": in.VerificationReference, "reported_by_service": string(platform.ProfessionalServicesService)})}
		if hook := opsHooks[d.Key]; hook != nil {
			if err = hook(ctx, tx, h, workerTenant(org), d, &rec, "create", &OpsRecordTransition{EvidenceRefs: in.EvidenceRefs}); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO professional_findings(id,organisation_id,scope_kind,scope_id,property_id,parent_id,kind,state,title,counterparty_reference,external_reference,attributes,evidence_refs) VALUES($1,$2,'PROPERTY',$3,$3,$4,$5,'RECORDED',$6,$7,$8,$9,$10)`,
			rec.ID, org, appt.ScopeID, appt.ID, in.Kind, in.Title, appt.CounterpartyReference, reference, rec.Attributes, marshalCommercial(in.EvidenceRefs)); err != nil {
			return err
		}
		if out, err = opsGet[OpsRecord](ctx, tx, d.Table, org, rec.ID); err != nil {
			return err
		}
		return h.opsDomainCommit(ctx, tx, workerTenant(org), d, out, "RECORDED_BY_PROFESSIONAL", "", in.Reason)
	})
	reply(w, r, status, out, e)
}

func (h *Handler) registerPartnerRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(serviceauth.RequireCaller(platform.ArtisanService, platform.ProfessionalServicesService, platform.AdminService, platform.PropertyManagerService))
		r.Get("/partner-records/{domain}/{id}", h.internalPartnerRecord)
		r.Post("/partner-records/{domain}/{id}/transitions", h.internalPartnerTransition)
	})
	r.Group(func(r chi.Router) {
		r.Use(serviceauth.RequireCaller(platform.PropertyManagerService))
		r.Post("/manager-assignments/{id}/maintenance-requests", h.internalManagerMaintenanceRequest)
	})
	r.Group(func(r chi.Router) {
		r.Use(serviceauth.RequireCaller(platform.ProfessionalServicesService))
		r.Post("/professional-appointments/{id}/findings", h.internalProfessionalFinding)
	})
}

var _ = database.SQLState
