package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// CRMPrivacyRequestCommand records a verified data-subject request for the
// guest's Business-owned CRM data in one property scope.
type CRMPrivacyRequestCommand struct {
	Kind                  string       `json:"kind"`
	RequesterKind         string       `json:"requester_kind"`
	VerificationReference string       `json:"verification_reference"`
	Correction            *crm.Profile `json:"correction,omitempty"`
	Reason                string       `json:"reason"`
}

// CRMPrivacyDecision approves or rejects a privacy request. Erasure consumes
// an exact independent approval; legal holds block it.
type CRMPrivacyDecision struct {
	Version    int64  `json:"version"`
	Decision   string `json:"decision"`
	ApprovalID ids.ID `json:"approval_id,omitempty"`
	Reason     string `json:"reason"`
}

type CRMLegalHoldCommand struct {
	Scope              string `json:"scope"`
	AuthorityReference string `json:"authority_reference"`
	Reason             string `json:"reason"`
}

type CRMLegalHoldRelease struct {
	Version    int64  `json:"version"`
	ApprovalID ids.ID `json:"approval_id"`
	Reason     string `json:"reason"`
}

type CRMVIPCommand struct {
	ExpectedVersion   int64      `json:"expected_version"`
	Tier              string     `json:"tier"`
	Basis             string     `json:"basis"`
	EvidenceReference string     `json:"evidence_reference"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	Reason            string     `json:"reason"`
}

// CRMSegmentDefinition holds only the parameter its closed kind uses.
type CRMSegmentDefinition struct {
	MinStays    int     `json:"min_stays,omitempty"`
	WindowDays  int     `json:"window_days,omitempty"`
	CorporateID *ids.ID `json:"corporate_id,omitempty"`
	Tier        string  `json:"tier,omitempty"`
	Source      string  `json:"source,omitempty"`
}

type CRMSegmentCommand struct {
	PropertyID      ids.ID               `json:"property_id"`
	SegmentKey      *ids.ID              `json:"segment_key,omitempty"`
	ExpectedVersion int64                `json:"expected_version"`
	Name            string               `json:"name"`
	Kind            string               `json:"kind"`
	Definition      CRMSegmentDefinition `json:"definition"`
	State           string               `json:"state"`
	Reason          string               `json:"reason"`
}

func (h *Handler) registerPrivacyCRM(r chi.Router) {
	r.Post("/guests/{id}/privacy-requests", h.crmPrivacyRequestCreate)
	r.Get("/privacy-requests", h.crmPrivacyRequests)
	r.Get("/privacy-requests/{id}", h.crmPrivacyRequestGet)
	r.Post("/privacy-requests/{id}/decision", h.crmPrivacyDecision)
	r.Get("/privacy-requests/{id}/export", h.crmPrivacyExport)
	r.Get("/guests/{id}/legal-holds", h.crmLegalHolds)
	r.Post("/guests/{id}/legal-holds", h.crmLegalHoldCreate)
	r.Post("/legal-holds/{id}/release", h.crmLegalHoldRelease)
	r.Get("/guests/{id}/vip", h.crmVIPGet)
	r.Post("/guests/{id}/vip", h.crmVIPSave)
	r.Get("/guest-segments", h.crmSegments)
	r.Post("/guest-segments", h.crmSegmentSave)
	r.Post("/guest-segments/{key}/evaluate", h.crmSegmentEvaluate)
	r.Get("/guest-segments/{key}/members", h.crmSegmentMembers)
}

type privacyRequest struct {
	ID           ids.ID          `json:"id"`
	PropertyID   ids.ID          `json:"property_id"`
	GuestID      ids.ID          `json:"guest_id"`
	Kind         string          `json:"kind"`
	State        string          `json:"state"`
	Correction   *crm.Profile    `json:"correction"`
	DueAt        time.Time       `json:"due_at"`
	ApprovalID   *ids.ID         `json:"approval_id"`
	Result       json.RawMessage `json:"result"`
	ResultDigest *string         `json:"result_digest"`
	Coordination json.RawMessage `json:"coordination"`
	Version      int64           `json:"version"`
	CreatedBy    ids.ID          `json:"created_by"`
	CreatedAt    time.Time       `json:"created_at"`
	CompletedAt  *time.Time      `json:"completed_at"`
}

func loadPrivacyRequest(ctx context.Context, q database.Querier, org, id ids.ID, lock bool) (privacyRequest, error) {
	var out privacyRequest
	var raw []byte
	query := `SELECT to_jsonb(p)-'verification_reference' FROM privacy_requests p WHERE organisation_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	e := q.QueryRow(ctx, query, org, id).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, apperrors.NotFound("PRIVACY_REQUEST_NOT_FOUND", "Privacy request not found.")
	}
	if e != nil {
		return out, e
	}
	return out, json.Unmarshal(raw, &out)
}

func (h *Handler) privacyTarget(r *http.Request, capability string, mfa bool) (tenant, privacyRequest, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, privacyRequest{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, privacyRequest{}, e
	}
	p, e := loadPrivacyRequest(r.Context(), h.Auth.Service.Pool, t.Organisation, id, false)
	if e != nil {
		return t, p, e
	}
	t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", p.PropertyID, mfa)
	return t, p, e
}

func activeLegalHold(ctx context.Context, q database.Querier, org, guest ids.ID) (bool, error) {
	var held bool
	// Holds attached to any record merged into the same canonical identity
	// also protect it; a merge never escapes a legal hold.
	e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM legal_holds h WHERE h.organisation_id=$1 AND h.state='ACTIVE' AND (h.guest_id=$2 OR business_guest_canonical($1,h.guest_id)=business_guest_canonical($1,$2)))`, org, guest).Scan(&held)
	return held, e
}

func (h *Handler) crmPrivacyRequestCreate(w http.ResponseWriter, r *http.Request) {
	var in CRMPrivacyRequestCommand
	if !decode(w, r, &in) {
		return
	}
	if !slices.Contains([]string{"EXPORT", "ACCESS", "CORRECTION", "ERASURE", "RESTRICTION"}, in.Kind) || !slices.Contains([]string{"GUEST", "AUTHORISED_REPRESENTATIVE"}, in.RequesterKind) ||
		!crm.Text(in.VerificationReference, 5, 300) || (in.Kind == "CORRECTION") != (in.Correction != nil) || in.Correction != nil && in.Correction.Validate() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Supported request kind, requester, verified identity evidence and reason required; only corrections carry the corrected profile."))
		return
	}
	_, g, property, e := h.crmGuestTarget(r, "guest_privacy:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:privacy:create:"+g.ID.String(), in, "guest_privacy:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guest_privacy:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		var correction any
		if in.Correction != nil {
			correction = marshalCommercial(in.Correction)
		}
		due := h.Auth.Service.Now().UTC().AddDate(0, 0, 30)
		if _, e = tx.Exec(ctx, `INSERT INTO privacy_requests(id,organisation_id,property_id,guest_id,kind,requester_kind,verification_reference,correction,due_at,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, t.Organisation, property, g.ID, in.Kind, in.RequesterKind, in.VerificationReference, correction, due, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, property, id, "privacy_request", "PRIVACY_REQUEST_RECEIVED", "guest.privacy_request.updated.v1", 1, map[string]any{"guest_id": g.ID, "kind": in.Kind, "state": "RECEIVED", "due_at": due}); e != nil {
			return 0, nil, e
		}
		out, e := loadPrivacyRequest(ctx, tx, t.Organisation, id, false)
		return 201, out, e
	})
}

func (h *Handler) crmPrivacyRequests(w http.ResponseWriter, r *http.Request) {
	property, e := ids.ParseV7(r.URL.Query().Get("property_id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A property scope is required."))
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "guest_privacy:write", "PROPERTY", property, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(p)-'verification_reference'-'result'||jsonb_build_object('overdue',p.state IN ('RECEIVED','APPROVED') AND p.due_at<$6) FROM privacy_requests p WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR state=$3) ORDER BY due_at,id LIMIT $4 OFFSET $5`, t.Organisation, property, r.URL.Query().Get("state"), limit, offset, h.Auth.Service.Now())
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}

func (h *Handler) crmPrivacyRequestGet(w http.ResponseWriter, r *http.Request) {
	_, p, e := h.privacyTarget(r, "guest_privacy:write", false)
	reply(w, r, 200, p, e)
}

// privacyCoordination lists the external owners that hold their own guest
// data. They receive references through their own contracts; Business never
// deletes another service's canonical records.
var privacyCoordination = []map[string]string{
	{"service": "marketplace-service", "data": "customer account and booking source records", "state": "OWNER_ACTION_REQUIRED"},
	{"service": "payment-service", "data": "financial records retained under financial law", "state": "OWNER_ACTION_REQUIRED"},
	{"service": "notification-service", "data": "delivery logs", "state": "OWNER_ACTION_REQUIRED"},
}

func (h *Handler) crmPrivacyDecision(w http.ResponseWriter, r *http.Request) {
	var in CRMPrivacyDecision
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || in.Decision != "APPROVE" && in.Decision != "REJECT" || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, APPROVE or REJECT and reason required."))
		return
	}
	_, pre, e := h.privacyTarget(r, "guest_privacy:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:privacy:decision:"+pre.ID.String(), in, "guest_privacy:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "guest_privacy:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		p, e := loadPrivacyRequest(ctx, tx, t.Organisation, pre.ID, true)
		if e != nil {
			return 0, nil, e
		}
		if p.Version != in.Version || p.State != "RECEIVED" && p.State != "BLOCKED_BY_LEGAL_HOLD" {
			return 0, nil, apperrors.Conflict("PRIVACY_REQUEST_STATE", "Only a current received or hold-blocked request can be decided.")
		}
		state, approval := "REJECTED", any(nil)
		var result any
		if in.Decision == "APPROVE" {
			held, e := activeLegalHold(ctx, tx, t.Organisation, p.GuestID)
			if e != nil {
				return 0, nil, e
			}
			switch p.Kind {
			case "EXPORT", "ACCESS":
				categories, e := privacyCategories(ctx, tx, t.Organisation, p.PropertyID, p.GuestID)
				if e != nil {
					return 0, nil, e
				}
				state, result = "COMPLETED", map[string]any{"categories": categories, "delivery": "AUTHORISED_EXPORT_ENDPOINT_ONLY"}
			case "CORRECTION":
				if h.Auth.Service.Sealer == nil {
					return 0, nil, apperrors.Unavailable("Protected guest storage is unavailable.")
				}
				cipher := h.Auth.Service.Sealer.Seal(marshalCommercial(p.Correction), crm.ProfileAAD(t.Organisation, p.GuestID))
				if _, e = tx.Exec(ctx, `UPDATE guest_profiles SET encrypted_profile=$3,profile_key_id=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND state IN ('ACTIVE','RESTRICTED')`, t.Organisation, p.GuestID, cipher, h.Auth.Service.Sealer.KeyID()); e != nil {
					return 0, nil, e
				}
				state, result = "COMPLETED", map[string]any{"corrected_fields": []string{"display_name", "language"}}
			case "RESTRICTION":
				if _, e = tx.Exec(ctx, `UPDATE guest_profiles SET state='RESTRICTED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND state='ACTIVE'`, t.Organisation, p.GuestID); e != nil {
					return 0, nil, e
				}
				state, result = "COMPLETED", map[string]any{"guest_state": "RESTRICTED"}
			case "ERASURE":
				op := in
				op.ApprovalID = ids.Nil
				if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_GUEST_ERASURE", target.Scope, in.ApprovalID, map[string]any{"privacy_request_id": p.ID, "guest_id": p.GuestID, "command": op}, "", nil, ""); e != nil {
					return 0, nil, e
				}
				approval = in.ApprovalID
				if held {
					state, approval = "BLOCKED_BY_LEGAL_HOLD", nil
					result = map[string]any{"blocked_by": "ACTIVE_LEGAL_HOLD"}
					break
				}
				state = "APPROVED"
				if _, e = tx.Exec(ctx, `INSERT INTO data_retention_jobs(id,organisation_id,property_id,guest_id,privacy_request_id,action,created_by) VALUES($1,$2,$3,$4,$5,'ANONYMISE_GUEST',$6)`, ids.New(), t.Organisation, p.PropertyID, p.GuestID, p.ID, t.Account); e != nil {
					return 0, nil, e
				}
			}
		}
		var digest any
		if result != nil {
			digest = hashCommercialPayload(marshalCommercial(result))
		}
		coordination := "[]"
		if p.Kind == "ERASURE" && state == "APPROVED" {
			coordination = string(marshalCommercial(privacyCoordination))
		}
		if _, e = tx.Exec(ctx, `UPDATE privacy_requests SET state=$3,approval_id=coalesce($4,approval_id),result=$5,result_digest=$6,coordination=$7::jsonb,decided_by=$8,completed_at=CASE WHEN $3='COMPLETED' THEN now() END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, p.ID, state, approval, marshalNullable(result), digest, coordination, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, p.PropertyID, p.ID, "privacy_request", "PRIVACY_REQUEST_"+state, "guest.privacy_request.updated.v1", p.Version+1, map[string]any{"guest_id": p.GuestID, "kind": p.Kind, "state": state, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := loadPrivacyRequest(ctx, tx, t.Organisation, p.ID, false)
		return 200, out, e
	})
}

func marshalNullable(v any) any {
	if v == nil {
		return nil
	}
	return marshalCommercial(v)
}

// privacyCategories counts the Business-owned records an export covers.
func privacyCategories(ctx context.Context, q database.Querier, org, property, guest ids.ID) (map[string]int64, error) {
	out := map[string]int64{}
	var profile, prefs, consents, links, reservations, stays, loyalty, recovery int64
	e := q.QueryRow(ctx, `SELECT 1,(SELECT count(*) FROM guest_preferences WHERE organisation_id=$1 AND guest_id=$3 AND property_id=$2),(SELECT count(*) FROM consents WHERE organisation_id=$1 AND guest_id=$3 AND property_id=$2),(SELECT count(*) FROM guest_identity_links WHERE organisation_id=$1 AND guest_id=$3 AND property_id=$2),(SELECT count(DISTINCT reservation_id) FROM reservation_guests WHERE organisation_id=$1 AND guest_id=$3 AND property_id=$2),(SELECT count(*) FROM stay_records WHERE organisation_id=$1 AND guest_id=$3 AND property_id=$2),(SELECT count(*) FROM loyalty_accounts WHERE organisation_id=$1 AND guest_id=$3 AND property_id=$2),(SELECT count(*) FROM service_recovery_cases WHERE organisation_id=$1 AND guest_id=$3 AND property_id=$2)`, org, property, guest).Scan(&profile, &prefs, &consents, &links, &reservations, &stays, &loyalty, &recovery)
	out["profile"], out["preferences"], out["consents"], out["identity_links"], out["reservations"], out["stays"], out["loyalty_accounts"], out["recovery_cases"] = profile, prefs, consents, links, reservations, stays, loyalty, recovery
	return out, e
}

// crmPrivacyExport returns only this property's Business-owned guest data for
// a completed export/access request, to a caller holding private-read authority.
func (h *Handler) crmPrivacyExport(w http.ResponseWriter, r *http.Request) {
	t, p, e := h.privacyTarget(r, "guest_private:read", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if p.State != "COMPLETED" || p.Kind != "EXPORT" && p.Kind != "ACCESS" {
		reply(w, r, 0, nil, apperrors.Conflict("PRIVACY_EXPORT_NOT_AVAILABLE", "Only a completed export or access request exposes its data."))
		return
	}
	if h.Auth.Service.Sealer == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Protected guest storage is unavailable."))
		return
	}
	ctx := r.Context()
	profile, e := crm.ReadProfile(ctx, h.Auth.Service.Pool, h.Auth.Service.Sealer, t.Organisation, p.GuestID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var private any
	var cipher []byte
	var key string
	if e = h.Auth.Service.Pool.QueryRow(ctx, `SELECT encrypted_preferences,preference_key_id FROM guest_preferences WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3`, t.Organisation, p.GuestID, p.PropertyID).Scan(&cipher, &key); e == nil {
		plain, e := h.Auth.Service.Sealer.Open(key, cipher, crm.PreferencesAAD(t.Organisation, p.GuestID, p.PropertyID))
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		private = json.RawMessage(plain)
	} else if !errors.Is(e, pgx.ErrNoRows) {
		reply(w, r, 0, nil, e)
		return
	}
	data := map[string]any{"profile": profile, "private_preferences": private}
	for name, query := range map[string]string{
		"consents":       `SELECT to_jsonb(c)-'created_by' FROM consents c WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 ORDER BY created_at,id LIMIT 500`,
		"identity_links": `SELECT jsonb_build_object('source_kind',source_kind,'state',state,'created_at',created_at) FROM guest_identity_links WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 ORDER BY created_at,id LIMIT 100`,
		"stays":          `SELECT jsonb_build_object('stay_id',s.id,'reservation_id',s.reservation_id,'state',s.state,'checked_in_at',s.checked_in_at,'checked_out_at',s.checked_out_at) FROM stay_records s WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 ORDER BY checked_in_at,id LIMIT 500`,
		"loyalty":        `SELECT jsonb_build_object('account_id',a.id,'tier',a.tier,'state',a.state,'balance_points',(SELECT coalesce(sum(points),0) FROM loyalty_transactions x WHERE x.organisation_id=a.organisation_id AND x.account_id=a.id)) FROM loyalty_accounts a WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 LIMIT 20`,
		"recovery_cases": `SELECT jsonb_build_object('case_id',id,'issue_type',issue_type,'state',state,'created_at',created_at,'outcome',outcome) FROM service_recovery_cases WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 ORDER BY created_at,id LIMIT 200`,
	} {
		rows, e := commercialJSONRows(ctx, h.Auth.Service.Pool, query, t.Organisation, p.GuestID, p.PropertyID)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		data[name] = rows
	}
	if e = audit(ctx, h.Auth.Service.Pool, t, "PRIVACY_EXPORT_READ", "privacy_request", p.ID, map[string]any{"guest_id": p.GuestID, "property_id": p.PropertyID}); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, r, 200, map[string]any{"privacy_request_id": p.ID, "guest_id": p.GuestID, "property_id": p.PropertyID, "scope": "BUSINESS_OWNED_CRM_DATA_FOR_PROPERTY", "data": data, "external_owners": privacyCoordination}, nil)
}

func (h *Handler) crmLegalHolds(w http.ResponseWriter, r *http.Request) {
	t, g, property, e := h.crmGuestTarget(r, "guest_privacy:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(h) FROM legal_holds h WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 ORDER BY created_at DESC,id DESC LIMIT 100`, t.Organisation, g.ID, property)
	reply(w, r, 200, map[string]any{"items": items}, e)
}

func (h *Handler) crmLegalHoldCreate(w http.ResponseWriter, r *http.Request) {
	var in CRMLegalHoldCommand
	if !decode(w, r, &in) {
		return
	}
	if !slices.Contains([]string{"GUEST_PROFILE", "ALL_GUEST_DATA"}, in.Scope) || !crm.Text(in.AuthorityReference, 5, 300) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Hold scope, legal authority reference and reason required."))
		return
	}
	_, g, property, e := h.crmGuestTarget(r, "guest_privacy:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:legal-hold:"+g.ID.String(), in, "guest_privacy:write", "PROPERTY", property, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guest_privacy:write", "PROPERTY", property, true)
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO legal_holds(id,organisation_id,property_id,guest_id,scope,authority_reference,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, property, g.ID, in.Scope, in.AuthorityReference, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		// Pending anonymisation stops immediately while the hold is active.
		if _, e = tx.Exec(ctx, `UPDATE data_retention_jobs SET state='BLOCKED',last_error='LEGAL_HOLD_ACTIVE',updated_at=now() WHERE organisation_id=$1 AND guest_id=$2 AND state IN ('QUEUED','FAILED')`, t.Organisation, g.ID); e != nil {
			return 0, nil, e
		}
		out := map[string]any{"id": id, "guest_id": g.ID, "property_id": property, "scope": in.Scope, "state": "ACTIVE", "version": 1}
		if e = h.commercialCommit(ctx, tx, t, property, id, "legal_hold", "LEGAL_HOLD_PLACED", "guest.legal_hold.updated.v1", 1, out); e != nil {
			return 0, nil, e
		}
		return 201, out, nil
	})
}

func (h *Handler) crmLegalHoldRelease(w http.ResponseWriter, r *http.Request) {
	var in CRMLegalHoldRelease
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || in.ApprovalID.ValidateV7() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, independent approval and reason required."))
		return
	}
	t0, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var property, guest ids.ID
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT property_id,guest_id FROM legal_holds WHERE organisation_id=$1 AND id=$2`, t0.Organisation, id).Scan(&property, &guest); errors.Is(e, pgx.ErrNoRows) {
		reply(w, r, 0, nil, apperrors.NotFound("LEGAL_HOLD_NOT_FOUND", "Legal hold not found."))
		return
	} else if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:legal-hold-release:"+id.String(), in, "guest_privacy:write", "PROPERTY", property, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "guest_privacy:write", "PROPERTY", property, true)
		if e != nil {
			return 0, nil, e
		}
		var version int64
		var state string
		if e = tx.QueryRow(ctx, `SELECT version,state FROM legal_holds WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, id).Scan(&version, &state); e != nil {
			return 0, nil, e
		}
		if version != in.Version || state != "ACTIVE" {
			return 0, nil, apperrors.Conflict("LEGAL_HOLD_STATE", "Only the current active hold can be released.")
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_LEGAL_HOLD_RELEASE", target.Scope, in.ApprovalID, map[string]any{"legal_hold_id": id, "command": op}, "", nil, ""); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE legal_holds SET state='RELEASED',release_approval_id=$3,released_by=$4,released_at=now(),version=version+1 WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.ApprovalID, t.Account); e != nil {
			return 0, nil, e
		}
		out := map[string]any{"id": id, "guest_id": guest, "state": "RELEASED", "version": version + 1}
		if e = h.commercialCommit(ctx, tx, t, property, id, "legal_hold", "LEGAL_HOLD_RELEASED", "guest.legal_hold.updated.v1", version+1, out); e != nil {
			return 0, nil, e
		}
		return 200, out, nil
	})
}

// ProcessDataRetention anonymises Business-owned guest CRM data for approved
// erasure jobs. Legal holds block execution; reservations, stays, folios and
// financial evidence are retained as legally required records.
func (h *Handler) ProcessDataRetention(ctx context.Context, limit int) error {
	if limit < 1 || limit > 50 {
		limit = 50
	}
	owner := ids.New().String()
	for range limit {
		var job struct {
			ID         ids.ID  `json:"id"`
			Org        ids.ID  `json:"organisation_id"`
			PropertyID ids.ID  `json:"property_id"`
			GuestID    ids.ID  `json:"guest_id"`
			RequestID  *ids.ID `json:"privacy_request_id"`
			Generation int64   `json:"lease_generation"`
			Attempts   int     `json:"attempts"`
		}
		var raw []byte
		e := h.Auth.Service.Pool.QueryRow(ctx, `UPDATE data_retention_jobs j SET state='RUNNING',lease_owner=$1,lease_generation=lease_generation+1,lease_expires_at=now()+interval '60 seconds',attempts=attempts+1,updated_at=now() WHERE id=(SELECT id FROM data_retention_jobs WHERE available_at<=now() AND (state IN ('QUEUED','BLOCKED','FAILED') OR state='RUNNING' AND lease_expires_at<now()) ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING to_jsonb(j)`, owner).Scan(&raw)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if e = json.Unmarshal(raw, &job); e != nil {
			return e
		}
		e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
				return e
			}
			var live bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM data_retention_jobs WHERE id=$1 AND state='RUNNING' AND lease_owner=$2 AND lease_generation=$3 AND lease_expires_at>now())`, job.ID, owner, job.Generation).Scan(&live); e != nil || !live {
				if e == nil {
					e = apperrors.Conflict("RETENTION_LEASE_LOST", "Another worker owns this retention job.")
				}
				return e
			}
			if _, e := tx.Exec(ctx, `SELECT id FROM guest_profiles WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, job.Org, job.GuestID); e != nil {
				return e
			}
			held, e := activeLegalHold(ctx, tx, job.Org, job.GuestID)
			if e != nil {
				return e
			}
			if held {
				_, e = tx.Exec(ctx, `UPDATE data_retention_jobs SET state='BLOCKED',last_error='LEGAL_HOLD_ACTIVE',lease_owner=NULL,lease_expires_at=NULL,available_at=now()+interval '1 hour',updated_at=now() WHERE id=$1`, job.ID)
				return e
			}
			if h.Auth.Service.Sealer == nil {
				return apperrors.Unavailable("Protected guest storage is unavailable.")
			}
			erased := crm.Profile{DisplayName: "Erased guest", Language: "und"}
			cipher := h.Auth.Service.Sealer.Seal(marshalCommercial(erased), crm.ProfileAAD(job.Org, job.GuestID))
			if _, e = tx.Exec(ctx, `UPDATE guest_profiles SET state='ANONYMISED',encrypted_profile=$3,profile_key_id=$4,registration_reference='anonymised:'||id::text,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND state<>'ANONYMISED'`, job.Org, job.GuestID, cipher, h.Auth.Service.Sealer.KeyID()); e != nil {
				return e
			}
			var prefs int64
			if e = tx.QueryRow(ctx, `WITH changed AS (UPDATE guest_preferences SET encrypted_preferences=$3,preference_key_id=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND guest_id=$2 RETURNING 1) SELECT count(*) FROM changed`, job.Org, job.GuestID, h.Auth.Service.Sealer.Seal([]byte(`{}`), crm.PreferencesAAD(job.Org, job.GuestID, job.PropertyID)), h.Auth.Service.Sealer.KeyID()).Scan(&prefs); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE guest_identity_links SET state='REVOKED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND guest_id=$2 AND state='ACTIVE'`, job.Org, job.GuestID); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE loyalty_accounts SET state='CLOSED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND guest_id=$2 AND state<>'CLOSED'`, job.Org, job.GuestID); e != nil {
				return e
			}
			mergedErased, e := h.eraseMergedMembers(ctx, tx, job.Org, job.GuestID, erased)
			if e != nil {
				return e
			}
			result := map[string]any{"profile": "ANONYMISED", "preferences_cleared": prefs, "identity_links": "REVOKED", "loyalty_accounts": "CLOSED", "merged_records_anonymised": mergedErased, "retained": []string{"reservations", "stays", "folios", "payment_evidence", "consent_history", "audit", "merge_lineage"}}
			if _, e = tx.Exec(ctx, `UPDATE data_retention_jobs SET state='COMPLETED',result=$2,lease_owner=NULL,lease_expires_at=NULL,last_error=NULL,updated_at=now() WHERE id=$1`, job.ID, marshalCommercial(result)); e != nil {
				return e
			}
			if job.RequestID != nil {
				if _, e = tx.Exec(ctx, `UPDATE privacy_requests SET state='COMPLETED',result=$3,result_digest=$4,completed_at=now(),version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND state='APPROVED'`, job.Org, *job.RequestID, marshalCommercial(result), hashCommercialPayload(marshalCommercial(result))); e != nil {
					return e
				}
			}
			t := tenant{Organisation: job.Org}
			return h.commercialCommit(ctx, tx, t, job.PropertyID, job.ID, "data_retention_job", "GUEST_ANONYMISED", "guest.privacy_request.updated.v1", 1, map[string]any{"guest_id": job.GuestID, "privacy_request_id": job.RequestID, "state": "COMPLETED"})
		})
		if e != nil && ctx.Err() == nil {
			state := "FAILED"
			if job.Attempts >= 12 {
				state = "BLOCKED"
			}
			if _, ue := h.Auth.Service.Pool.Exec(ctx, `UPDATE data_retention_jobs SET state=$2,last_error='RETENTION_EXECUTION_FAILED',lease_owner=NULL,lease_expires_at=NULL,available_at=now()+($3::int*interval '1 second'),updated_at=now() WHERE id=$1 AND lease_owner=$4`, job.ID, state, min(3600, 60*job.Attempts), owner); ue != nil {
				return ue
			}
		}
	}
	return nil
}

func (h *Handler) crmVIPGet(w http.ResponseWriter, r *http.Request) {
	t, g, property, e := h.crmGuestTarget(r, "guests:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(v)||jsonb_build_object('current',v.version=(SELECT max(version) FROM vip_classifications x WHERE x.organisation_id=v.organisation_id AND x.guest_id=v.guest_id AND x.property_id=v.property_id) AND (v.expires_at IS NULL OR v.expires_at>$4)) FROM vip_classifications v WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 ORDER BY version DESC LIMIT 50`, t.Organisation, g.ID, property, h.Auth.Service.Now())
	reply(w, r, 200, map[string]any{"items": items}, e)
}

func (h *Handler) crmVIPSave(w http.ResponseWriter, r *http.Request) {
	var in CRMVIPCommand
	if !decode(w, r, &in) {
		return
	}
	now := h.Auth.Service.Now().UTC()
	if in.ExpectedVersion < 0 || !slices.Contains([]string{"NONE", "VIP", "VVIP"}, in.Tier) || !slices.Contains([]string{"OPERATOR_REVIEWED", "LOYALTY_TIER", "CORPORATE_CONTRACT", "REPEAT_STAYS"}, in.Basis) || !crm.Text(in.EvidenceReference, 5, 300) || in.ExpiresAt != nil && !in.ExpiresAt.After(now) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, supported tier and source-backed basis, evidence and reason required."))
		return
	}
	_, g, property, e := h.crmGuestTarget(r, "guests:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:vip:"+g.ID.String()+":"+property.String(), in, "guests:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guests:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `SELECT id FROM guest_property_scopes WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 AND state='ACTIVE' FOR UPDATE`, t.Organisation, g.ID, property); e != nil {
			return 0, nil, e
		}
		var current int64
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM vip_classifications WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3`, t.Organisation, g.ID, property).Scan(&current); e != nil {
			return 0, nil, e
		}
		if current != in.ExpectedVersion {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "VIP classification changed.")
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO vip_classifications(id,organisation_id,property_id,guest_id,version,tier,basis,evidence_reference,expires_at,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, t.Organisation, property, g.ID, current+1, in.Tier, in.Basis, in.EvidenceReference, in.ExpiresAt, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		out := map[string]any{"id": id, "guest_id": g.ID, "property_id": property, "version": current + 1, "tier": in.Tier, "basis": in.Basis}
		if e = h.commercialCommit(ctx, tx, t, property, g.ID, "guest_vip", "GUEST_VIP_CLASSIFIED", "guest.profile.updated.v1", current+1, out); e != nil {
			return 0, nil, e
		}
		return 201, out, nil
	})
}

func validSegment(in CRMSegmentCommand) bool {
	d := in.Definition
	if in.PropertyID.ValidateV7() != nil || in.SegmentKey != nil && in.SegmentKey.ValidateV7() != nil || in.ExpectedVersion < 0 || (in.SegmentKey == nil) != (in.ExpectedVersion == 0) || !crm.Text(in.Name, 1, 200) || !slices.Contains([]string{"ACTIVE", "RETIRED"}, in.State) || !validStaffReason(in.Reason) {
		return false
	}
	empty := d.MinStays == 0 && d.WindowDays == 0 && d.CorporateID == nil && d.Tier == "" && d.Source == ""
	switch in.Kind {
	case "STAY_FREQUENCY":
		return d.MinStays >= 1 && d.MinStays <= 1000 && d.WindowDays >= 1 && d.WindowDays <= 3650 && d.CorporateID == nil && d.Tier == "" && d.Source == ""
	case "CORPORATE":
		return d.CorporateID != nil && d.CorporateID.ValidateV7() == nil && d.MinStays == 0 && d.WindowDays == 0 && d.Tier == "" && d.Source == ""
	case "LOYALTY_TIER":
		return crm.Text(d.Tier, 1, 40) && d.MinStays == 0 && d.WindowDays == 0 && d.CorporateID == nil && d.Source == ""
	case "VIP":
		return slices.Contains([]string{"VIP", "VVIP"}, d.Tier) && d.MinStays == 0 && d.WindowDays == 0 && d.CorporateID == nil && d.Source == ""
	case "MARKETING_ELIGIBLE":
		return empty
	case "SOURCE_CHANNEL":
		return slices.Contains([]string{"DIRECT", "MARKETPLACE", "OTA", "CORPORATE", "WALK_IN", "GROUP"}, d.Source) && d.MinStays == 0 && d.WindowDays == 0 && d.CorporateID == nil && d.Tier == ""
	}
	return false
}

func (h *Handler) crmSegmentSave(w http.ResponseWriter, r *http.Request) {
	var in CRMSegmentCommand
	if !decode(w, r, &in) {
		return
	}
	if !validSegment(in) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, a closed source-backed segment kind with exactly its parameters, state and reason required."))
		return
	}
	h.execute(w, r, "crm:segment:"+in.PropertyID.String(), in, "guests:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guests:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		key := ids.New()
		if in.SegmentKey != nil {
			key = *in.SegmentKey
		}
		var current int64
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM guest_segments WHERE organisation_id=$1 AND property_id=$2 AND segment_key=$3`, t.Organisation, in.PropertyID, key).Scan(&current); e != nil {
			return 0, nil, e
		}
		if current != in.ExpectedVersion {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Segment changed.")
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO guest_segments(id,organisation_id,property_id,segment_key,version,name,kind,definition,state,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, t.Organisation, in.PropertyID, key, current+1, in.Name, in.Kind, marshalCommercial(in.Definition), in.State, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		out := map[string]any{"id": id, "segment_key": key, "version": current + 1, "kind": in.Kind, "state": in.State}
		if e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "guest_segment", "GUEST_SEGMENT_SAVED", "guest.segment.updated.v1", current+1, out); e != nil {
			return 0, nil, e
		}
		return 201, out, nil
	})
}

func (h *Handler) crmSegments(w http.ResponseWriter, r *http.Request) {
	property, e := ids.ParseV7(r.URL.Query().Get("property_id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A property scope is required."))
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "guests:read", "PROPERTY", property, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(s) FROM guest_segments s WHERE organisation_id=$1 AND property_id=$2 AND version=(SELECT max(version) FROM guest_segments x WHERE x.segment_key=s.segment_key) ORDER BY created_at,id LIMIT 200`, t.Organisation, property)
	reply(w, r, 200, map[string]any{"items": items}, e)
}

type guestSegment struct {
	ID         ids.ID               `json:"id"`
	PropertyID ids.ID               `json:"property_id"`
	SegmentKey ids.ID               `json:"segment_key"`
	Kind       string               `json:"kind"`
	State      string               `json:"state"`
	Definition CRMSegmentDefinition `json:"definition"`
}

func (h *Handler) segmentTarget(r *http.Request, capability string) (tenant, guestSegment, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, guestSegment{}, e
	}
	key, e := ids.ParseV7(chi.URLParam(r, "key"))
	if e != nil {
		return t, guestSegment{}, apperrors.InvalidRequest("A UUIDv7 segment key is required.")
	}
	var raw []byte
	var s guestSegment
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT to_jsonb(s) FROM guest_segments s WHERE organisation_id=$1 AND segment_key=$2 ORDER BY version DESC LIMIT 1`, t.Organisation, key).Scan(&raw); errors.Is(e, pgx.ErrNoRows) {
		return t, s, apperrors.NotFound("SEGMENT_NOT_FOUND", "Segment not found.")
	} else if e != nil {
		return t, s, e
	}
	if e = json.Unmarshal(raw, &s); e != nil {
		return t, s, e
	}
	t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", s.PropertyID, false)
	return t, s, e
}

// segmentCandidates evaluates the closed criteria against authoritative
// owned records. Marketing eligibility always follows current consent.
func segmentCandidates(ctx context.Context, q database.Querier, org ids.ID, s guestSegment, now time.Time) ([]ids.ID, error) {
	d := s.Definition
	var query string
	args := []any{org, s.PropertyID, now}
	switch s.Kind {
	case "STAY_FREQUENCY":
		query = `SELECT s.guest_id FROM stay_records s WHERE s.organisation_id=$1 AND s.property_id=$2 AND s.state='DEPARTED' AND s.checked_out_at>=$3-($4::int*interval '1 day') GROUP BY s.guest_id HAVING count(*)>=$5`
		args = append(args, d.WindowDays, d.MinStays)
	case "CORPORATE":
		query = `SELECT DISTINCT guest_id FROM guest_company_associations WHERE organisation_id=$1 AND property_id=$2 AND corporate_id=$4 AND state='ACTIVE' AND effective_at<=$3 AND expires_at>$3`
		args = append(args, *d.CorporateID)
	case "LOYALTY_TIER":
		query = `SELECT DISTINCT guest_id FROM loyalty_accounts WHERE organisation_id=$1 AND property_id=$2 AND state='ACTIVE' AND tier=$4 AND $3::timestamptz IS NOT NULL`
		args = append(args, d.Tier)
	case "VIP":
		query = `SELECT v.guest_id FROM vip_classifications v WHERE v.organisation_id=$1 AND v.property_id=$2 AND v.version=(SELECT max(version) FROM vip_classifications x WHERE x.organisation_id=v.organisation_id AND x.guest_id=v.guest_id AND x.property_id=v.property_id) AND (v.expires_at IS NULL OR v.expires_at>$3) AND (v.tier=$4 OR $4='VIP' AND v.tier='VVIP')`
		args = append(args, d.Tier)
	case "MARKETING_ELIGIBLE":
		query = `SELECT c.guest_id FROM consents c WHERE c.organisation_id=$1 AND c.property_id=$2 AND c.purpose='MARKETING' AND c.version=(SELECT max(version) FROM consents x WHERE x.guest_id=c.guest_id AND x.property_id=c.property_id AND x.purpose='MARKETING') AND c.state='GRANTED' AND (c.expires_at IS NULL OR c.expires_at>$3)`
	case "SOURCE_CHANNEL":
		query = `SELECT DISTINCT g.guest_id FROM reservation_guests g JOIN reservations r ON r.organisation_id=g.organisation_id AND r.id=g.reservation_id WHERE g.organisation_id=$1 AND g.property_id=$2 AND g.guest_id IS NOT NULL AND r.source=$4 AND $3::timestamptz IS NOT NULL`
		args = append(args, d.Source)
	default:
		return nil, apperrors.Conflict("SEGMENT_KIND_UNSUPPORTED", "Unsupported segment kind.")
	}
	rows, e := q.Query(ctx, `SELECT x.guest_id FROM (`+query+`) x(guest_id) JOIN guest_property_scopes p ON p.organisation_id=$1 AND p.guest_id=x.guest_id AND p.property_id=$2 AND p.state='ACTIVE' JOIN guest_profiles g ON g.organisation_id=$1 AND g.id=x.guest_id AND g.state IN ('ACTIVE','RESTRICTED') ORDER BY x.guest_id LIMIT 10001`, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []ids.ID
	for rows.Next() {
		var id ids.ID
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out = append(out, id)
	}
	if len(out) > 10000 {
		return nil, apperrors.Conflict("SEGMENT_TOO_LARGE", "Segment exceeds the bounded evaluation size.")
	}
	return out, rows.Err()
}

func (h *Handler) crmSegmentEvaluate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Evaluation reason required."))
		return
	}
	_, pre, e := h.segmentTarget(r, "guests:write")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:segment-evaluate:"+pre.SegmentKey.String(), in, "guests:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guests:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		if pre.State != "ACTIVE" {
			return 0, nil, apperrors.Conflict("SEGMENT_RETIRED", "Only an active segment is evaluated.")
		}
		now := h.Auth.Service.Now().UTC()
		members, e := segmentCandidates(ctx, tx, t.Organisation, pre, now)
		if e != nil {
			return 0, nil, e
		}
		previous := map[ids.ID]bool{}
		rows, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(m.guest_id) FROM guest_segment_memberships m JOIN guest_segments s ON s.organisation_id=m.organisation_id AND s.id=m.segment_id WHERE m.organisation_id=$1 AND s.segment_key=$2 AND m.state='MEMBER' AND m.evaluation_id=(SELECT x.evaluation_id FROM guest_segment_memberships x JOIN guest_segments y ON y.organisation_id=x.organisation_id AND y.id=x.segment_id WHERE x.organisation_id=$1 AND y.segment_key=$2 ORDER BY x.created_at DESC,x.id DESC LIMIT 1)`, t.Organisation, pre.SegmentKey)
		if e != nil {
			return 0, nil, e
		}
		for _, raw := range rows {
			var id ids.ID
			if e = json.Unmarshal(raw, &id); e != nil {
				return 0, nil, e
			}
			previous[id] = true
		}
		evaluation := ids.New()
		digest := hashCommercialPayload(marshalCommercial(map[string]any{"segment_id": pre.ID, "members": members, "evaluated_at": now}))
		current := map[ids.ID]bool{}
		joined, exited := 0, 0
		for _, g := range members {
			current[g] = true
			if !previous[g] {
				joined++
			}
			if _, e = tx.Exec(ctx, `INSERT INTO guest_segment_memberships(id,organisation_id,property_id,segment_id,evaluation_id,guest_id,state,source_digest,created_by) VALUES($1,$2,$3,$4,$5,$6,'MEMBER',$7,$8)`, ids.New(), t.Organisation, pre.PropertyID, pre.ID, evaluation, g, digest, t.Account); e != nil {
				return 0, nil, e
			}
		}
		for g := range previous {
			if current[g] {
				continue
			}
			exited++
			if _, e = tx.Exec(ctx, `INSERT INTO guest_segment_memberships(id,organisation_id,property_id,segment_id,evaluation_id,guest_id,state,source_digest,created_by) VALUES($1,$2,$3,$4,$5,$6,'EXITED',$7,$8)`, ids.New(), t.Organisation, pre.PropertyID, pre.ID, evaluation, g, digest, t.Account); e != nil {
				return 0, nil, e
			}
		}
		out := map[string]any{"segment_key": pre.SegmentKey, "evaluation_id": evaluation, "member_count": len(members), "joined": joined, "exited": exited, "source_digest": digest}
		if e = audit(ctx, tx, t, "GUEST_SEGMENT_EVALUATED", "guest_segment", pre.ID, out); e != nil {
			return 0, nil, e
		}
		return 200, out, nil
	})
}

func (h *Handler) crmSegmentMembers(w http.ResponseWriter, r *http.Request) {
	t, s, e := h.segmentTarget(r, "guests:read")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT jsonb_build_object('guest_id',m.guest_id,'evaluation_id',m.evaluation_id,'evaluated_at',m.created_at) FROM guest_segment_memberships m JOIN guest_segments s ON s.organisation_id=m.organisation_id AND s.id=m.segment_id JOIN guest_profiles g ON g.organisation_id=m.organisation_id AND g.id=m.guest_id AND g.state IN ('ACTIVE','RESTRICTED') WHERE m.organisation_id=$1 AND s.segment_key=$2 AND m.state='MEMBER' AND m.evaluation_id=(SELECT x.evaluation_id FROM guest_segment_memberships x JOIN guest_segments y ON y.organisation_id=x.organisation_id AND y.id=x.segment_id WHERE x.organisation_id=$1 AND y.segment_key=$2 ORDER BY x.created_at DESC,x.id DESC LIMIT 1) ORDER BY m.guest_id LIMIT 1000`, t.Organisation, s.SegmentKey)
	reply(w, r, 200, map[string]any{"segment_key": s.SegmentKey, "items": items}, e)
}
