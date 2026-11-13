package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// opsDomain is one Section 16 governed operational bounded context. The
// definitions are generated together with their database state guards.
type opsDomain struct {
	Key, Table, Path, Scope, Read, Write, Initial, Event string
	Kinds, States, Edges                                 []string
	Parent                                               string
	ParentInBody                                         bool
	Subject                                              string
	SubjectRequired, CounterpartyRequired                bool
	AmountRequired, StartsRequired, EndsRequired         bool
	CreateMFA                                            bool
	CreateApproval                                       *opsApproval
	Transitions                                          map[string]opsTransition
}

type opsTransition struct {
	From                                            []string
	To                                              string
	Capability                                      string
	MFA                                             bool
	Approval                                        *opsApproval
	Evidence, Distinct, Assignee, Amount, Reference bool
}

// opsApproval is either an exact commercial approval (bound to the server
// derived amount) or a privileged Annex F approval.
type opsApproval struct {
	Commercial, Capability, Privilege string
	BindAmount                        bool
}

// OpsRecord is the public projection of a governed operational record.
type OpsRecord struct {
	ID                    ids.ID          `json:"id"`
	ScopeKind             string          `json:"scope_kind"`
	ScopeID               ids.ID          `json:"scope_id"`
	PropertyID            *ids.ID         `json:"property_id"`
	ParentID              *ids.ID         `json:"parent_id"`
	SubjectID             *ids.ID         `json:"subject_id"`
	Kind                  string          `json:"kind"`
	State                 string          `json:"state"`
	Title                 string          `json:"title"`
	CounterpartyReference string          `json:"counterparty_reference"`
	ExternalReference     string          `json:"external_reference"`
	AmountMinor           *int64          `json:"amount_minor"`
	Currency              *string         `json:"currency"`
	StartsAt              *time.Time      `json:"starts_at"`
	EndsAt                *time.Time      `json:"ends_at"`
	Attributes            json.RawMessage `json:"attributes"`
	EvidenceRefs          json.RawMessage `json:"evidence_refs"`
	ApprovalID            *ids.ID         `json:"approval_id"`
	AssignedTo            *ids.ID         `json:"assigned_to"`
	LastActorID           *ids.ID         `json:"last_actor_id"`
	LastAction            string          `json:"last_action"`
	ClosedAt              *time.Time      `json:"closed_at"`
	CreatedBy             *ids.ID         `json:"created_by"`
	CreatedAt             time.Time       `json:"created_at"`
	Version               int64           `json:"version"`
}

// OpsRecordCreate creates a governed record. scope_id is the property (or
// parcel) for scoped domains; nested routes take the parent from the path.
type OpsRecordCreate struct {
	ScopeID               *ids.ID        `json:"scope_id,omitempty"`
	ParentID              *ids.ID        `json:"parent_id,omitempty"`
	SubjectID             *ids.ID        `json:"subject_id,omitempty"`
	Kind                  string         `json:"kind"`
	Title                 string         `json:"title"`
	CounterpartyReference string         `json:"counterparty_reference,omitempty"`
	ExternalReference     string         `json:"external_reference,omitempty"`
	AmountMinor           *int64         `json:"amount_minor,omitempty"`
	Currency              string         `json:"currency,omitempty"`
	StartsAt              *time.Time     `json:"starts_at,omitempty"`
	EndsAt                *time.Time     `json:"ends_at,omitempty"`
	Attributes            map[string]any `json:"attributes,omitempty"`
	EvidenceRefs          []ids.ID       `json:"evidence_refs,omitempty"`
	ApprovalID            ids.ID         `json:"approval_id,omitempty"`
	Reason                string         `json:"reason"`
}

// OpsRecordTransition applies one governed action at the current version.
type OpsRecordTransition struct {
	Version           int64          `json:"version"`
	Action            string         `json:"action"`
	AssigneeID        *ids.ID        `json:"assignee_id,omitempty"`
	AmountMinor       *int64         `json:"amount_minor,omitempty"`
	Currency          string         `json:"currency,omitempty"`
	ExternalReference string         `json:"external_reference,omitempty"`
	EvidenceRefs      []ids.ID       `json:"evidence_refs,omitempty"`
	Attributes        map[string]any `json:"attributes,omitempty"`
	ApprovalID        ids.ID         `json:"approval_id,omitempty"`
	Reason            string         `json:"reason"`
}

type OpsRecordDetail struct {
	Record  OpsRecord         `json:"record"`
	History []json.RawMessage `json:"history"`
}

type OpsRecordList struct {
	Items  []json.RawMessage `json:"items"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

func init() {
	for _, d := range opsDomains {
		registerOpsTable(d.Table)
	}
}

// opsHook validates or applies domain-specific behaviour inside the owning
// transaction. stage is "create", "before:<ACTION>" or "after:<ACTION>".
type opsHook func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error

var opsHooks = map[string]opsHook{}

// opsScope resolves the authorization scope kind/id for a domain request.
func (h *Handler) opsScope(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, t tenant, d *opsDomain, r *http.Request, scope *ids.ID, parent *ids.ID) (string, ids.ID, *ids.ID, *ids.ID, error) {
	switch {
	case d.Parent == "DEVELOPMENT":
		dev, e := ids.ParseV7(chi.URLParam(r, "id"))
		if e != nil {
			return "", ids.Nil, nil, nil, apperrors.InvalidRequest("A project (development) identifier is required.")
		}
		return "DEVELOPMENT", dev, nil, nil, nil
	case d.Parent != "":
		pid := parent
		if !d.ParentInBody {
			v, e := ids.ParseV7(chi.URLParam(r, "id"))
			if e != nil {
				return "", ids.Nil, nil, nil, apperrors.InvalidRequest("A parent identifier is required.")
			}
			pid = &v
		}
		if pid == nil {
			return "", ids.Nil, nil, nil, apperrors.InvalidRequest("parent_id is required.")
		}
		pd := opsDomains[d.Parent]
		var property ids.ID
		if e := q.QueryRow(ctx, `SELECT property_id FROM `+pd.Table+` WHERE organisation_id=$1 AND id=$2`, t.Organisation, *pid).Scan(&property); e != nil {
			return "", ids.Nil, nil, nil, opsNotFound(e)
		}
		return "PROPERTY", property, &property, pid, nil
	case d.Scope == "COMPANY":
		return "COMPANY", t.Organisation, nil, nil, nil
	}
	if scope == nil {
		return "", ids.Nil, nil, nil, apperrors.InvalidRequest("scope_id is required.")
	}
	if d.Scope == "PROPERTY" {
		return "PROPERTY", *scope, scope, nil, nil
	}
	return d.Scope, *scope, nil, nil, nil
}

func (h *Handler) opsDomainList(w http.ResponseWriter, r *http.Request, d *opsDomain) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	q := r.URL.Query()
	var scope, parent *ids.ID
	for _, key := range []string{"scope_id", "property_id"} {
		if raw := q.Get(key); raw != "" {
			v, e := ids.ParseV7(raw)
			if e != nil {
				reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid "+key+"."))
				return
			}
			scope = &v
		}
	}
	if raw := q.Get("parent_id"); raw != "" && d.ParentInBody {
		v, e := ids.ParseV7(raw)
		if e != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid parent_id."))
			return
		}
		parent = &v
	}
	if d.ParentInBody && parent == nil {
		// Top-level listing of a parented domain is scoped by property.
		if scope == nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("property_id or parent_id is required."))
			return
		}
		kind, id := "PROPERTY", *scope
		t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, d.Read, kind, id, false)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		h.opsDomainRows(w, r, d, t, id, nil)
		return
	}
	kind, id, _, pid, e := h.opsScope(r.Context(), h.Auth.Service.Pool, t, d, r, scope, parent)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, d.Read, kind, id, false); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.opsDomainRows(w, r, d, t, id, pid)
}

func (h *Handler) opsDomainRows(w http.ResponseWriter, r *http.Request, d *opsDomain, t tenant, scope ids.ID, parent *ids.ID) {
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	q := r.URL.Query()
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x) FROM `+d.Table+` x WHERE organisation_id=$1 AND scope_id=$2 AND ($3::uuid IS NULL OR parent_id=$3) AND ($4='' OR state=$4) AND ($5='' OR kind=$5) AND ($6='' OR subject_id::text=$6) ORDER BY created_at DESC,id LIMIT $7 OFFSET $8`, t.Organisation, scope, parent, q.Get("state"), q.Get("kind"), q.Get("subject_id"), limit, offset)
	reply(w, r, 200, OpsRecordList{Items: items, Limit: limit, Offset: offset}, e)
}

// opsSubject verifies the subject is a canonical record of the expected
// kind inside the authorised scope.
func opsSubject(ctx context.Context, tx pgx.Tx, org ids.ID, d *opsDomain, scopeKind string, scope ids.ID, subject ids.ID) error {
	switch d.Subject {
	case "DATA_ROOM":
		var ok bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM data_rooms WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND state='OPEN')`, org, scope, subject).Scan(&ok); e != nil {
			return e
		}
		if !ok {
			return apperrors.Conflict("DATA_ROOM_NOT_OPEN", "The data room must be open in the same property.")
		}
		return nil
	case "":
		return apperrors.InvalidRequest("This domain does not accept a subject.")
	}
	n, e := portfolio.Get(ctx, tx, org, subject)
	if e != nil {
		return apperrors.NotFound("SUBJECT_NOT_FOUND", "The subject inventory node was not found.")
	}
	allowed := map[string][]string{"ROOM": {"ROOM", "UNIT"}, "UNIT": {"UNIT"}, "ASSET_OR_SPACE": {"ROOM", "UNIT", "ASSET", "FACILITY", "COMMON_AREA", "BUILDING", "FLOOR"}, "DEVELOPMENT_UNIT": {"UNIT"}}[d.Subject]
	if !slices.Contains(allowed, string(n.Kind)) {
		return apperrors.InvalidRequest("The subject is not a supported inventory kind for this domain.")
	}
	target, e := portfolio.Resolve(ctx, tx, org, string(n.Kind), subject)
	if e != nil {
		return e
	}
	for _, a := range target.Ancestors {
		if a.ID == scope {
			return nil
		}
	}
	return apperrors.PermissionDenied("The subject is not inside the authorised " + strings.ToLower(scopeKind) + " scope.")
}

func validEvidence(list []ids.ID) bool {
	if len(list) > 20 {
		return false
	}
	for _, id := range list {
		if id.ValidateV7() != nil {
			return false
		}
	}
	return true
}

func (h *Handler) opsDomainCreate(w http.ResponseWriter, r *http.Request, d *opsDomain) {
	var in OpsRecordCreate
	if !decode(w, r, &in) {
		return
	}
	now := h.Auth.Service.Now().UTC()
	ok := slices.Contains(d.Kinds, in.Kind) && opsText(in.Title, 3, 200) && len(in.CounterpartyReference) <= 300 && len(in.ExternalReference) <= 300 && validEvidence(in.EvidenceRefs) && validStaffReason(in.Reason) &&
		(!d.CounterpartyRequired || opsText(in.CounterpartyReference, 3, 300)) && (!d.SubjectRequired || in.SubjectID != nil) && (in.SubjectID == nil || d.Subject != "") &&
		(!d.AmountRequired || in.AmountMinor != nil) && (in.AmountMinor == nil || *in.AmountMinor >= 0 && validCurrency(in.Currency)) &&
		(!d.StartsRequired || in.StartsAt != nil) && (!d.EndsRequired || in.EndsAt != nil) && (in.StartsAt == nil || in.EndsAt == nil || in.EndsAt.After(*in.StartsAt)) &&
		(in.EndsAt == nil || in.EndsAt.After(now)) && (in.AmountMinor != nil || in.Currency == "")
	if in.Attributes == nil {
		in.Attributes = map[string]any{}
	}
	if raw, _ := json.Marshal(in.Attributes); len(raw) > 16384 {
		ok = false
	}
	if !ok {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Supported kind, title, reason and the domain's required scope/subject/counterparty/amount/date fields are required."))
		return
	}
	t0, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	kind, scope, property, parent, e := h.opsScope(r.Context(), h.Auth.Service.Pool, t0, d, r, in.ScopeID, in.ParentID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	mfa := d.CreateMFA || d.CreateApproval != nil
	h.execute(w, r, "ops:"+d.Key+":create:"+scope.String(), in, d.Write, kind, scope, mfa, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, d.Write, kind, scope, mfa)
		if e != nil {
			return 0, nil, e
		}
		if kind == "DEVELOPMENT" || kind == "PARCEL" {
			if n, e := portfolio.Get(ctx, tx, t.Organisation, scope); e != nil || string(n.Kind) != kind {
				return 0, nil, apperrors.InvalidRequest("The scope is not a " + strings.ToLower(kind) + ".")
			}
		}
		if in.SubjectID != nil {
			if e = opsSubject(ctx, tx, t.Organisation, d, kind, scope, *in.SubjectID); e != nil {
				return 0, nil, e
			}
		}
		id := ids.New()
		var currency any
		if in.AmountMinor != nil {
			currency = in.Currency
		}
		var parentRef any
		if parent != nil && d.Parent != "DEVELOPMENT" {
			parentRef = *parent
		}
		evidence := in.EvidenceRefs
		if evidence == nil {
			evidence = []ids.ID{}
		}
		rec := OpsRecord{ID: id, ScopeKind: kind, ScopeID: scope, PropertyID: property, SubjectID: in.SubjectID, Kind: in.Kind, State: d.Initial, Title: in.Title, CounterpartyReference: in.CounterpartyReference, ExternalReference: in.ExternalReference, AmountMinor: in.AmountMinor, StartsAt: in.StartsAt, EndsAt: in.EndsAt, Attributes: marshalCommercial(in.Attributes)}
		if parentRef != nil {
			rec.ParentID = parent
		}
		if hook := opsHooks[d.Key]; hook != nil {
			if e = hook(ctx, tx, h, t, d, &rec, "create", &OpsRecordTransition{Attributes: in.Attributes, EvidenceRefs: in.EvidenceRefs}); e != nil {
				return 0, nil, e
			}
		}
		var approval any
		if d.CreateApproval != nil {
			if e = h.opsRequireApproval(ctx, tx, t, target, d, d.CreateApproval, in.ApprovalID, map[string]any{"domain": d.Key, "action": "CREATE", "scope_id": scope, "parent_id": parentRef, "subject_id": in.SubjectID, "ends_at": in.EndsAt, "kind": in.Kind}, in.AmountMinor, in.Currency); e != nil {
				return 0, nil, e
			}
			approval = in.ApprovalID
		}
		if _, e = tx.Exec(ctx, `INSERT INTO `+d.Table+`(id,organisation_id,scope_kind,scope_id,property_id,parent_id,subject_id,kind,state,title,counterparty_reference,external_reference,amount_minor,currency,starts_at,ends_at,attributes,evidence_refs,approval_id,last_actor_id,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$20)`,
			id, t.Organisation, kind, scope, property, parentRef, in.SubjectID, in.Kind, d.Initial, in.Title, in.CounterpartyReference, in.ExternalReference, in.AmountMinor, currency, in.StartsAt, in.EndsAt, rec.Attributes, marshalCommercial(evidence), approval, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[OpsRecord](ctx, tx, d.Table, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if hook := opsHooks[d.Key]; hook != nil {
			if e = hook(ctx, tx, h, t, d, &out, "after:CREATE", &OpsRecordTransition{Attributes: in.Attributes}); e != nil {
				return 0, nil, e
			}
		}
		return 201, out, h.opsDomainCommit(ctx, tx, t, d, out, "CREATED", "", in.Reason)
	})
}

func (h *Handler) opsDomainCommit(ctx context.Context, tx pgx.Tx, t tenant, d *opsDomain, rec OpsRecord, action, from, reason string) error {
	property := ids.Nil
	if rec.PropertyID != nil {
		property = *rec.PropertyID
	}
	return h.opsCommit(ctx, tx, t, property, rec.ID, d.Key, action, from, rec.State, d.Event, rec.Version, map[string]any{"domain": d.Key, "kind": rec.Kind, "scope_kind": rec.ScopeKind, "scope_id": rec.ScopeID, "parent_id": rec.ParentID, "subject_id": rec.SubjectID, "amount_minor": rec.AmountMinor, "currency": rec.Currency, "approval_id": rec.ApprovalID, "reason": reason})
}

func (h *Handler) opsRequireApproval(ctx context.Context, tx pgx.Tx, t tenant, target workforce.Target, d *opsDomain, a *opsApproval, approvalID ids.ID, operation map[string]any, amount *int64, currency string) error {
	if a.Privilege != "" {
		return h.requirePrivilegeChange(ctx, tx, t, a.Privilege, target.Scope, approvalID, operation)
	}
	var bound *int64
	cur := ""
	if a.BindAmount && amount != nil {
		bound, cur = amount, currency
	}
	return h.requireCommercialApproval(ctx, tx, t, a.Commercial, target.Scope, approvalID, operation, "", bound, cur)
}

// opsRecordTarget loads a record by path and authorises its scope.
func (h *Handler) opsRecordTarget(r *http.Request, d *opsDomain, capability string, mfa bool) (tenant, OpsRecord, error) {
	param := "id"
	if strings.Contains(d.Path, "{id}") {
		param = "record_id"
	}
	id, e := ids.ParseV7(chi.URLParam(r, param))
	if e != nil {
		return tenant{}, OpsRecord{}, apperrors.InvalidRequest("A canonical UUIDv7 identifier is required.")
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, OpsRecord{}, e
	}
	rec, e := opsGet[OpsRecord](r.Context(), h.Auth.Service.Pool, d.Table, t.Organisation, id)
	if e != nil {
		return t, rec, e
	}
	if strings.Contains(d.Path, "{id}") {
		parent, pe := ids.ParseV7(chi.URLParam(r, "id"))
		if pe != nil || d.Parent == "DEVELOPMENT" && rec.ScopeID != parent || d.Parent != "DEVELOPMENT" && (rec.ParentID == nil || *rec.ParentID != parent) {
			return t, rec, apperrors.NotFound("RESOURCE_NOT_FOUND", "Resource not found.")
		}
	}
	t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, capability, rec.ScopeKind, rec.ScopeID, mfa)
	return t, rec, e
}

func (h *Handler) opsDomainGet(w http.ResponseWriter, r *http.Request, d *opsDomain) {
	t, rec, e := h.opsRecordTarget(r, d, d.Read, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	history, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x) FROM operations_history x WHERE organisation_id=$1 AND entity_type=$2 AND entity_id=$3 ORDER BY sequence LIMIT 500`, t.Organisation, d.Key, rec.ID)
	reply(w, r, 200, OpsRecordDetail{Record: rec, History: history}, e)
}

func (h *Handler) opsDomainTransition(w http.ResponseWriter, r *http.Request, d *opsDomain, forced string) {
	var in OpsRecordTransition
	if !decode(w, r, &in) {
		return
	}
	if forced != "" {
		in.Action = forced
	}
	rule, known := d.Transitions[in.Action]
	if !known || in.Version < 1 || !validStaffReason(in.Reason) || !validEvidence(in.EvidenceRefs) || len(in.ExternalReference) > 300 || in.AmountMinor != nil && (*in.AmountMinor < 0 || !validCurrency(in.Currency)) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Supported action, current version, reason and valid evidence/reference/amount are required."))
		return
	}
	capability := rule.Capability
	if capability == "" {
		capability = d.Write
	}
	mfa := rule.MFA || rule.Approval != nil
	_, pre, e := h.opsRecordTarget(r, d, capability, mfa)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "ops:"+d.Key+":"+in.Action+":"+pre.ID.String(), in, capability, pre.ScopeKind, pre.ScopeID, mfa, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, capability, pre.ScopeKind, pre.ScopeID, mfa)
		if e != nil {
			return 0, nil, e
		}
		rec, e := opsLock[OpsRecord](ctx, tx, d.Table, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if rec.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "The record version has changed.")
		}
		if !slices.Contains(rule.From, rec.State) {
			return 0, nil, apperrors.Conflict("INVALID_TRANSITION", in.Action+" is not allowed from "+rec.State+".")
		}
		if rule.Evidence && len(in.EvidenceRefs) == 0 {
			return 0, nil, apperrors.InvalidRequest(in.Action + " requires Media evidence references.")
		}
		if rule.Reference && !opsText(in.ExternalReference, 3, 300) {
			return 0, nil, apperrors.InvalidRequest(in.Action + " requires the owning party's reference.")
		}
		if rule.Amount && in.AmountMinor == nil {
			return 0, nil, apperrors.InvalidRequest(in.Action + " requires an amount and currency.")
		}
		if rule.Distinct && (rec.LastActorID != nil && *rec.LastActorID == t.Account || rec.AssignedTo != nil && *rec.AssignedTo == t.Account) {
			return 0, nil, apperrors.PermissionDenied("Separation of duties: a different authorised person must perform " + in.Action + ".")
		}
		assignee := rec.AssignedTo
		if rule.Assignee {
			if in.AssigneeID == nil {
				return 0, nil, apperrors.InvalidRequest(in.Action + " requires an assignee.")
			}
			ok, e := h.opsMemberAuthority(ctx, tx, t.Organisation, rec, *in.AssigneeID, d.Write)
			if e != nil {
				return 0, nil, e
			}
			if !ok {
				return 0, nil, apperrors.PermissionDenied("The assignee must currently hold " + d.Write + " for this scope.")
			}
			assignee = in.AssigneeID
		}
		amount, currency := rec.AmountMinor, ""
		if rec.Currency != nil {
			currency = *rec.Currency
		}
		if in.AmountMinor != nil {
			amount, currency = in.AmountMinor, in.Currency
		}
		if hook := opsHooks[d.Key]; hook != nil {
			if e = hook(ctx, tx, h, t, d, &rec, "before:"+in.Action, &in); e != nil {
				return 0, nil, e
			}
		}
		var approval any
		if rule.Approval != nil {
			op := map[string]any{"domain": d.Key, "record_id": rec.ID, "action": in.Action, "version": rec.Version}
			if e = h.opsRequireApproval(ctx, tx, t, target, d, rule.Approval, in.ApprovalID, op, amount, currency); e != nil {
				return 0, nil, e
			}
			approval = in.ApprovalID
		}
		attributes := map[string]any{}
		_ = json.Unmarshal(rec.Attributes, &attributes)
		for k, v := range in.Attributes {
			attributes[k] = v
		}
		var evidence []ids.ID
		_ = json.Unmarshal(rec.EvidenceRefs, &evidence)
		evidence = append(evidence, in.EvidenceRefs...)
		if len(evidence) > 50 {
			return 0, nil, apperrors.InvalidRequest("Evidence reference limit reached.")
		}
		var currencyRef any
		if amount != nil {
			currencyRef = currency
		}
		reference := rec.ExternalReference
		if in.ExternalReference != "" {
			reference = in.ExternalReference
		}
		terminal := !slices.ContainsFunc(d.Edges, func(e string) bool { return strings.HasPrefix(e, rule.To+">") })
		if _, e = tx.Exec(ctx, `UPDATE `+d.Table+` SET state=$3,assigned_to=$4,amount_minor=$5,currency=$6,external_reference=$7,attributes=$8,evidence_refs=$9,approval_id=coalesce($10,approval_id),last_actor_id=$11,last_action=$12,closed_at=CASE WHEN $13 THEN now() ELSE closed_at END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`,
			t.Organisation, rec.ID, rule.To, assignee, amount, currencyRef, reference, marshalCommercial(attributes), marshalCommercial(evidence), approval, t.Account, in.Action, terminal); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[OpsRecord](ctx, tx, d.Table, t.Organisation, rec.ID)
		if e != nil {
			return 0, nil, e
		}
		if hook := opsHooks[d.Key]; hook != nil {
			if e = hook(ctx, tx, h, t, d, &out, "after:"+in.Action, &in); e != nil {
				return 0, nil, e
			}
		}
		return 200, out, h.opsDomainCommit(ctx, tx, t, d, out, in.Action, rec.State, in.Reason)
	})
}

// opsMemberAuthority checks a member's current recorded capability on the
// record's scope (any supported scope kind).
func (h *Handler) opsMemberAuthority(ctx context.Context, tx pgx.Tx, org ids.ID, rec OpsRecord, account ids.ID, capability string) (bool, error) {
	var member, role ids.ID
	e := tx.QueryRow(ctx, `SELECT id,role_id FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2 AND status='ACTIVE' ORDER BY effective_at DESC LIMIT 1`, org, account).Scan(&member, &role)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	return h.recordedAuthority(ctx, tx, org, rec.ScopeKind, rec.ScopeID, member, role, capability)
}

// recordedAuthority evaluates a stored membership's grants on any scope
// without borrowing a session (used for assignees, sponsors and workers).
func (h *Handler) recordedAuthority(ctx context.Context, tx pgx.Tx, org ids.ID, kind string, scope, member, role ids.ID, capability string) (bool, error) {
	if kind == "PROPERTY" {
		return h.currentRecordedPMSAuthority(ctx, tx, org, scope, member, role, permissions.Permission(capability))
	}
	var m workforce.Membership
	var state string
	e := tx.QueryRow(ctx, `SELECT m.account_id,m.organisation_id,m.status,m.effective_at,m.expires_at,o.state FROM staff_memberships m JOIN role_templates r ON r.organisation_id=m.organisation_id AND r.id=m.role_id JOIN business_accounts a ON a.id=m.account_id JOIN organisations o ON o.id=m.organisation_id WHERE m.organisation_id=$1 AND m.id=$2 AND m.role_id=$3 AND r.active AND a.status='ACTIVE'`, org, member, role).Scan(&m.AccountID, &m.OrganisationID, &m.Status, &m.EffectiveAt, &m.ExpiresAt, &state)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	m.RoleCapabilities = permissions.NewSet()
	if e = workforce.LoadDirectGrants(ctx, tx, org, member, role, &m); e != nil {
		return false, e
	}
	if kind == "COMPANY" {
		scope = org
	}
	target, e := resolve(ctx, tx, org, kind, scope)
	if e != nil {
		return false, e
	}
	return workforce.Authorize(m, workforce.DecisionInput{AccountID: m.AccountID, Target: target, Capability: permissions.Permission(capability), Now: h.Auth.Service.Now().UTC(), Restricted: state != "ACTIVE"}) == nil, nil
}

// opsSystemTransition applies a worker-driven edge (expiry, conversion)
// under the explicit service identity.
func (h *Handler) opsSystemTransition(ctx context.Context, tx pgx.Tx, t tenant, d *opsDomain, id ids.ID, to, action string) error {
	org := t.Organisation
	rec, e := opsLock[OpsRecord](ctx, tx, d.Table, org, id)
	if e != nil {
		return e
	}
	if !slices.Contains(d.Edges, rec.State+">"+to) {
		return apperrors.Conflict("INVALID_TRANSITION", "System transition not permitted.")
	}
	if _, e = tx.Exec(ctx, `UPDATE `+d.Table+` SET state=$3,last_action=$4,closed_at=CASE WHEN $5 THEN now() ELSE closed_at END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, org, id, to, action, !slices.ContainsFunc(d.Edges, func(e string) bool { return strings.HasPrefix(e, to+">") })); e != nil {
		return e
	}
	out, e := opsGet[OpsRecord](ctx, tx, d.Table, org, id)
	if e != nil {
		return e
	}
	return h.opsDomainCommit(ctx, tx, t, d, out, action, rec.State, "system:"+action)
}
