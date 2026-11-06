package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type CRMGuestCreate struct {
	PropertyID            ids.ID      `json:"property_id"`
	Profile               crm.Profile `json:"profile"`
	SourceKind            string      `json:"source_kind"`
	RegistrationReference string      `json:"registration_reference"`
	Reason                string      `json:"reason"`
}
type CRMGuestPatch struct {
	Version               int64        `json:"version"`
	Profile               *crm.Profile `json:"profile,omitempty"`
	State                 string       `json:"state,omitempty"`
	RegistrationReference string       `json:"registration_reference,omitempty"`
	ApprovalID            ids.ID       `json:"approval_id,omitempty"`
	Reason                string       `json:"reason"`
}

func (h *Handler) registerCRM(r chi.Router) {
	r.Get("/guests", h.crmGuestList)
	r.Post("/guests", h.crmGuestCreate)
	r.Get("/guests/{id}", h.crmGuestGet)
	r.Patch("/guests/{id}", h.crmGuestPatch)
	r.Get("/guests/{id}/identity-links", h.crmGuestLinks)
	r.Post("/guests/{id}/identity-links", h.crmGuestLink)
	r.Post("/guests/{id}/identity-links/{link_id}/revoke", h.crmGuestLinkRevoke)
	r.Post("/guests/{id}/property-scopes", h.crmGuestScope)
	r.Get("/guests/{id}/private", h.crmGuestPrivateGet)
	r.Put("/guests/{id}/private", h.crmGuestPrivateSave)
	r.Get("/guests/{id}/stay-history", h.crmStayHistory)
	r.Post("/guests/{id}/stay-history/rebuild", h.crmStayHistoryRebuild)
	r.Get("/guests/{id}/consents", h.crmGuestConsents)
	r.Post("/guests/{id}/consents", h.crmGuestConsent)
	h.registerCorporateCRM(r)
}
func (h *Handler) crmSourceHash(org ids.ID, kind, reference string) string {
	// These are opaque identifiers, never credentials or inferred contact
	// identities. A stable tenant namespace survives authentication-key rotation.
	return security.SHA256Hex([]byte("business:guest-source:" + org.String() + ":" + kind + ":" + reference))
}
func (h *Handler) crmGuestTarget(r *http.Request, capability string, mfa bool) (tenant, crm.Guest, ids.ID, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, crm.Guest{}, ids.Nil, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, crm.Guest{}, ids.Nil, e
	}
	g, e := crm.GetGuest(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		return t, g, ids.Nil, e
	}
	property := g.PropertyID
	if s := r.URL.Query().Get("property_id"); s != "" {
		property, e = ids.ParseV7(s)
		if e != nil {
			return t, g, ids.Nil, apperrors.InvalidRequest("Invalid guest property scope.")
		}
	}
	var scoped bool
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM guest_property_scopes WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 AND (state='ACTIVE' OR $4::boolean))`, t.Organisation, g.ID, property, crmWithdrawalAllowed(r.Context(), t.Organisation, capability, "PROPERTY", property)).Scan(&scoped); e != nil {
		return t, g, property, e
	}
	if !scoped {
		return t, g, property, apperrors.PermissionDenied("Guest is not available in this property scope.")
	}
	t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", property, mfa)
	return t, g, property, e
}
func (h *Handler) crmGuestCreate(w http.ResponseWriter, r *http.Request) {
	var in CRMGuestCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.Profile.Validate() != nil || !crm.LocalSource(in.SourceKind) || !crm.Text(in.RegistrationReference, 5, 300) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, local guest registration evidence, display profile and reason required."))
		return
	}
	if h.Auth.Service.Sealer == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Protected guest storage is unavailable."))
		return
	}
	h.execute(w, r, "crm:guest:create", in, "guests:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guests:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		cipher := h.Auth.Service.Sealer.Seal(marshalCommercial(in.Profile), crm.ProfileAAD(t.Organisation, id))
		if _, e = tx.Exec(ctx, `INSERT INTO guest_profiles(id,organisation_id,property_id,registration_reference,encrypted_profile,profile_key_id,created_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, t.Organisation, in.PropertyID, in.RegistrationReference, cipher, h.Auth.Service.Sealer.KeyID(), t.Account); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO guest_property_scopes(id,organisation_id,guest_id,property_id,state,created_by) VALUES($1,$2,$3,$4,'ACTIVE',$5)`, ids.New(), t.Organisation, id, in.PropertyID, t.Account); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO guest_identity_links(id,organisation_id,guest_id,property_id,source_kind,source_authority,source_reference_hash,proof_kind,evidence_reference,state,created_by) VALUES($1,$2,$3,$4,$5,'business-operations-service',$6,'LOCAL_REGISTRATION',$7,'ACTIVE',$8)`, ids.New(), t.Organisation, id, in.PropertyID, in.SourceKind, h.crmSourceHash(t.Organisation, in.SourceKind, id.String()), in.RegistrationReference, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.crmGuestCommit(ctx, tx, t, in.PropertyID, id, id, "guest_profile", 1, "GUEST_REGISTERED", map[string]any{"state": "ACTIVE", "source_kind": in.SourceKind, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		g, e := crm.GetGuest(ctx, tx, t.Organisation, id)
		return 201, map[string]any{"guest": g, "source_kind": in.SourceKind, "source_reference": id.String()}, e
	})
}
func (h *Handler) crmGuestGet(w http.ResponseWriter, r *http.Request) {
	t, g, _, e := h.crmGuestTarget(r, "guests:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	p, e := crm.ReadProfile(r.Context(), h.Auth.Service.Pool, h.Auth.Service.Sealer, t.Organisation, g.ID)
	reply(w, r, 200, crm.GuestView{Guest: g, Profile: p}, e)
}
func (h *Handler) crmGuestList(w http.ResponseWriter, r *http.Request) {
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
	properties, e := h.operationalProperties(r.Context(), h.Auth.Service.Pool, t, "guests:read", r.URL.Query().Get("property_id"), r.URL.Query().Get("branch_id"))
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rows, e := h.Auth.Service.Pool.Query(r.Context(), `SELECT to_jsonb(g)-'encrypted_profile'-'profile_key_id' FROM guest_profiles g WHERE organisation_id=$1 AND EXISTS(SELECT 1 FROM guest_property_scopes s WHERE s.organisation_id=g.organisation_id AND s.guest_id=g.id AND s.property_id=ANY($2::uuid[]) AND s.state='ACTIVE') ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, properties, limit, offset)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	guests := []crm.Guest{}
	for rows.Next() {
		var raw []byte
		var g crm.Guest
		if e = rows.Scan(&raw); e != nil {
			break
		}
		if e = json.Unmarshal(raw, &g); e != nil {
			break
		}
		guests = append(guests, g)
	}
	rowErr := rows.Err()
	rows.Close()
	if e == nil {
		e = rowErr
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items := []crm.GuestView{}
	for _, g := range guests {
		p, err := crm.ReadProfile(r.Context(), h.Auth.Service.Pool, h.Auth.Service.Sealer, t.Organisation, g.ID)
		if err != nil {
			reply(w, r, 0, nil, err)
			return
		}
		items = append(items, crm.GuestView{Guest: g, Profile: p})
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, nil)
}
func (h *Handler) crmGuestPatch(w http.ResponseWriter, r *http.Request) {
	var in CRMGuestPatch
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) || in.Profile != nil && in.Profile.Validate() != nil || in.State != "" && !slices.Contains([]string{"ACTIVE", "RESTRICTED", "ARCHIVED"}, in.State) || in.RegistrationReference != "" && !crm.Text(in.RegistrationReference, 5, 300) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current guest version and a valid profile/lifecycle command required."))
		return
	}
	_, pre, _, e := h.crmGuestTarget(r, "guests:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:guest:update:"+pre.ID.String(), in, "guests:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "guests:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		g, e := crm.GetGuest(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if g.Version != in.Version || g.State == "ANONYMISED" || g.State == "MERGED" || g.State == "ARCHIVED" {
			return 0, nil, apperrors.Conflict("GUEST_VERSION_OR_STATE", "Guest version or lifecycle changed.")
		}
		if in.State == "ACTIVE" && g.State == "RESTRICTED" {
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_GUEST_RESTRICTION_RELEASE", target.Scope, in.ApprovalID, map[string]any{"guest_id": g.ID, "command": op}, "", nil, ""); e != nil {
				return 0, nil, e
			}
		} else if !in.ApprovalID.IsZero() {
			return 0, nil, apperrors.InvalidRequest("An approval must bind the exact restriction release.")
		}
		var cipher any
		var key any
		if in.Profile != nil {
			if h.Auth.Service.Sealer == nil {
				return 0, nil, apperrors.Unavailable("Protected guest storage is unavailable.")
			}
			cipher = h.Auth.Service.Sealer.Seal(marshalCommercial(in.Profile), crm.ProfileAAD(t.Organisation, g.ID))
			key = h.Auth.Service.Sealer.KeyID()
		}
		if _, e = tx.Exec(ctx, `UPDATE guest_profiles SET encrypted_profile=coalesce($3,encrypted_profile),profile_key_id=coalesce($4,profile_key_id),state=coalesce(nullif($5,''),state),registration_reference=coalesce(nullif($6,''),registration_reference),version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, g.ID, cipher, key, in.State, in.RegistrationReference); e != nil {
			return 0, nil, e
		}
		if e = h.crmGuestCommit(ctx, tx, t, g.PropertyID, g.ID, g.ID, "guest_profile", g.Version+1, "GUEST_PROFILE_UPDATED", map[string]any{"state": in.State, "reason": in.Reason, "protected_profile_changed": in.Profile != nil, "registration_evidence_changed": in.RegistrationReference != ""}); e != nil {
			return 0, nil, e
		}
		out, e := crm.GetGuest(ctx, tx, t.Organisation, g.ID)
		return 200, out, e
	})
}
func (h *Handler) crmGuestCommit(ctx context.Context, tx pgx.Tx, t tenant, property, guest, target ids.ID, kind string, version int64, operation string, data any) error {
	payload := map[string]any{}
	if values, ok := data.(map[string]any); ok {
		for key, value := range values {
			payload[key] = value
		}
	} else {
		payload["change"] = data
	}
	payload["guest_id"] = guest
	var actor any
	if !t.Account.IsZero() {
		actor = t.Account
	}
	if _, e := tx.Exec(ctx, `INSERT INTO guest_identity_history(id,organisation_id,property_id,guest_id,entity_type,entity_id,version,operation,actor_id,context) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, ids.New(), t.Organisation, property, guest, kind, target, version, operation, actor, marshalCommercial(payload)); e != nil {
		return e
	}
	return h.commercialCommit(ctx, tx, t, property, target, kind, operation, "guest.profile.updated.v1", version, payload)
}
