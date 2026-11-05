package businesshttp

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type CRMCorporateCreate struct {
	PropertyID            ids.ID                 `json:"property_id"`
	LegalName             string                 `json:"legal_name"`
	RegistrationReference string                 `json:"registration_reference"`
	Contacts              []crm.CorporateContact `json:"contacts"`
	Reason                string                 `json:"reason"`
}
type CRMCorporatePatch struct {
	Version   int64                   `json:"version"`
	LegalName string                  `json:"legal_name,omitempty"`
	State     string                  `json:"state,omitempty"`
	Contacts  *[]crm.CorporateContact `json:"contacts,omitempty"`
	Reason    string                  `json:"reason"`
}
type CRMCorporateContractCommand struct {
	ContractKey     *ids.ID            `json:"contract_key,omitempty"`
	ExpectedVersion int64              `json:"expected_version"`
	State           string             `json:"state"`
	Definition      crm.CorporateTerms `json:"definition"`
	EffectiveAt     time.Time          `json:"effective_at"`
	ExpiresAt       time.Time          `json:"expires_at"`
	ApprovalID      ids.ID             `json:"approval_id,omitempty"`
	Reason          string             `json:"reason"`
}
type CRMTravelerCommand struct {
	GuestID         ids.ID             `json:"guest_id"`
	ContractID      ids.ID             `json:"contract_id"`
	ExpectedVersion int64              `json:"expected_version"`
	State           string             `json:"state"`
	Policy          crm.CorporateTerms `json:"policy"`
	EffectiveAt     time.Time          `json:"effective_at"`
	ExpiresAt       time.Time          `json:"expires_at"`
	ApprovalID      ids.ID             `json:"approval_id,omitempty"`
	Reason          string             `json:"reason"`
}

func (h *Handler) registerCorporateCRM(r chi.Router) {
	r.Get("/corporate-customers", h.crmCorporateList)
	r.Post("/corporate-customers", h.crmCorporateCreate)
	r.Get("/corporate-customers/{id}", h.crmCorporateGet)
	r.Patch("/corporate-customers/{id}", h.crmCorporatePatch)
	r.Get("/corporate-customers/{id}/contacts", h.crmCorporateContacts)
	r.Get("/corporate-customers/{id}/contracts", h.crmCorporateContracts)
	r.Post("/corporate-customers/{id}/contracts", h.crmCorporateContractSave)
	r.Get("/corporate-customers/{id}/travelers", h.crmCorporateTravelers)
	r.Post("/corporate-customers/{id}/travelers", h.crmCorporateTravelerSave)
	r.Post("/corporate-customers/{id}/property-scopes", h.crmCorporateScope)
}
func (h *Handler) crmCorporateTarget(r *http.Request, capability string, mfa bool) (tenant, crm.Corporate, ids.ID, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, crm.Corporate{}, ids.Nil, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, crm.Corporate{}, ids.Nil, e
	}
	c, e := crm.GetCorporate(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		return t, c, ids.Nil, e
	}
	property := c.PropertyID
	if s := r.URL.Query().Get("property_id"); s != "" {
		property, e = ids.ParseV7(s)
		if e != nil {
			return t, c, ids.Nil, apperrors.InvalidRequest("Invalid corporate property scope.")
		}
	}
	var scoped bool
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM corporate_property_scopes WHERE organisation_id=$1 AND corporate_id=$2 AND property_id=$3 AND state='ACTIVE')`, t.Organisation, id, property).Scan(&scoped); e != nil {
		return t, c, property, e
	}
	if !scoped {
		return t, c, property, apperrors.PermissionDenied("Corporate customer is unavailable in this property scope.")
	}
	t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", property, mfa)
	return t, c, property, e
}
func validCorporateContacts(items []crm.CorporateContact) bool {
	if len(items) > 20 {
		return false
	}
	for _, c := range items {
		if !crm.Text(c.Name, 1, 200) || !crm.Text(c.RepresentativeReference, 5, 300) || (crm.PrivateProfile{Email: c.Email, Phone: c.Phone}).Validate() != nil {
			return false
		}
	}
	return true
}
func corporateAAD(org, id ids.ID) []byte {
	return []byte("business:crm:corporate-contacts:" + org.String() + ":" + id.String())
}
func (h *Handler) crmCorporateCreate(w http.ResponseWriter, r *http.Request) {
	var in CRMCorporateCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || !crm.Text(in.LegalName, 1, 200) || !crm.Text(in.RegistrationReference, 5, 300) || !validCorporateContacts(in.Contacts) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Corporate registration, scoped property, protected contacts and reason required."))
		return
	}
	if h.Auth.Service.Sealer == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Protected corporate storage is unavailable."))
		return
	}
	h.execute(w, r, "crm:corporate:create", in, "corporate:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "corporate:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		var duplicate bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM corporate_customer_profiles WHERE organisation_id=$1 AND registration_reference=$2)`, t.Organisation, in.RegistrationReference).Scan(&duplicate); e != nil {
			return 0, nil, e
		}
		if duplicate {
			return 0, nil, apperrors.Conflict("CORPORATE_ALREADY_REGISTERED", "This business customer already has a canonical record; use an approved property association.")
		}
		id := ids.New()
		cipher := h.Auth.Service.Sealer.Seal(marshalCommercial(in.Contacts), corporateAAD(t.Organisation, id))
		if _, e = tx.Exec(ctx, `INSERT INTO corporate_customer_profiles(id,organisation_id,property_id,legal_name,registration_reference,state,encrypted_contacts,contact_key_id,created_by) VALUES($1,$2,$3,$4,$5,'ACTIVE',$6,$7,$8)`, id, t.Organisation, in.PropertyID, in.LegalName, in.RegistrationReference, cipher, h.Auth.Service.Sealer.KeyID(), t.Account); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO corporate_property_scopes(id,organisation_id,corporate_id,property_id,state,created_by) VALUES($1,$2,$3,$4,'ACTIVE',$5)`, ids.New(), t.Organisation, id, in.PropertyID, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "corporate_customer", "CORPORATE_CUSTOMER_REGISTERED", "corporate.customer.updated.v1", 1, map[string]any{"state": "ACTIVE", "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := crm.GetCorporate(ctx, tx, t.Organisation, id)
		return 201, out, e
	})
}
func (h *Handler) crmCorporateGet(w http.ResponseWriter, r *http.Request) {
	_, v, _, e := h.crmCorporateTarget(r, "corporate:read", false)
	reply(w, r, 200, v, e)
}
func (h *Handler) crmCorporateList(w http.ResponseWriter, r *http.Request) {
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
	properties, e := h.operationalProperties(r.Context(), h.Auth.Service.Pool, t, "corporate:read", r.URL.Query().Get("property_id"), r.URL.Query().Get("branch_id"))
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(c)-'encrypted_contacts'-'contact_key_id' FROM corporate_customer_profiles c WHERE organisation_id=$1 AND EXISTS(SELECT 1 FROM corporate_property_scopes s WHERE s.organisation_id=c.organisation_id AND s.corporate_id=c.id AND s.state='ACTIVE' AND s.property_id=ANY($2::uuid[])) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, properties, limit, offset)
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) crmCorporatePatch(w http.ResponseWriter, r *http.Request) {
	var in CRMCorporatePatch
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) || in.LegalName != "" && !crm.Text(in.LegalName, 1, 200) || in.State != "" && !slices.Contains([]string{"ACTIVE", "SUSPENDED", "ARCHIVED"}, in.State) || in.Contacts != nil && !validCorporateContacts(*in.Contacts) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current corporate version and valid lifecycle/profile command required."))
		return
	}
	_, pre, _, e := h.crmCorporateTarget(r, "corporate:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:corporate:update:"+pre.ID.String(), in, "corporate:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "corporate:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		c, e := crm.GetCorporate(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if c.Version != in.Version || c.State == "ARCHIVED" {
			return 0, nil, apperrors.Conflict("CORPORATE_VERSION_OR_STATE", "Corporate record version/lifecycle changed.")
		}
		var cipher any
		var key any
		if in.Contacts != nil {
			if h.Auth.Service.Sealer == nil {
				return 0, nil, apperrors.Unavailable("Protected corporate storage is unavailable.")
			}
			cipher = h.Auth.Service.Sealer.Seal(marshalCommercial(*in.Contacts), corporateAAD(t.Organisation, c.ID))
			key = h.Auth.Service.Sealer.KeyID()
		}
		if _, e = tx.Exec(ctx, `UPDATE corporate_customer_profiles SET legal_name=coalesce(nullif($3,''),legal_name),state=coalesce(nullif($4,''),state),encrypted_contacts=coalesce($5,encrypted_contacts),contact_key_id=coalesce($6,contact_key_id),version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, c.ID, in.LegalName, in.State, cipher, key); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, c.PropertyID, c.ID, "corporate_customer", "CORPORATE_CUSTOMER_UPDATED", "corporate.customer.updated.v1", c.Version+1, map[string]any{"state": in.State, "contacts_changed": in.Contacts != nil, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := crm.GetCorporate(ctx, tx, t.Organisation, c.ID)
		return 200, out, e
	})
}
func (h *Handler) crmCorporateContracts(w http.ResponseWriter, r *http.Request) {
	h.crmCorporateChildren(w, r, "corporate_customer_contracts")
}
func (h *Handler) crmCorporateTravelers(w http.ResponseWriter, r *http.Request) {
	h.crmCorporateChildren(w, r, "guest_company_associations")
}
func (h *Handler) crmCorporateChildren(w http.ResponseWriter, r *http.Request, table string) {
	if table != "corporate_customer_contracts" && table != "guest_company_associations" {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Unknown corporate collection."))
		return
	}
	t, c, property, e := h.crmCorporateTarget(r, "corporate:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(p) FROM `+table+` p WHERE organisation_id=$1 AND corporate_id=$2 AND property_id=$3 ORDER BY created_at DESC,id DESC LIMIT $4 OFFSET $5`, t.Organisation, c.ID, property, limit, offset)
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) validateCorporateTerms(ctx context.Context, tx pgx.Tx, t tenant, property ids.ID, d crm.CorporateTerms) error {
	if e := d.Validate(); e != nil {
		return e
	}
	if len(d.RatePlanIDs) < 1 || len(d.RatePlanIDs) > 20 || len(d.AllowedSources) < 1 || len(d.AllowedSources) > 8 || !slices.Contains([]string{"CORPORATE_CREDIT", "PLATFORM_COLLECT", "PAY_AT_PROPERTY"}, d.BillingResponsibility) || !crm.Text(d.BillingReference, 5, 300) || d.MaxBookingMinor < 1 || d.MaxBookingMinor > 1000000000000000 || d.MaxNights < 1 || d.MaxNights > 366 {
		return apperrors.InvalidRequest("Explicit negotiated canonical plans, bounded traveler/financial policy and billing reference required.")
	}
	var currency string
	if e := tx.QueryRow(ctx, `SELECT currency FROM inventory_nodes WHERE organisation_id=$1 AND id=$2`, t.Organisation, property).Scan(&currency); e != nil {
		return e
	}
	if currency != d.Currency {
		return apperrors.InvalidRequest("Corporate contract currency must match the owning property.")
	}
	seen := map[ids.ID]bool{}
	for _, id := range d.RatePlanIDs {
		if id.ValidateV7() != nil || seen[id] {
			return apperrors.InvalidRequest("Unique canonical rate plans required.")
		}
		seen[id] = true
		p, e := revenue.GetPlan(ctx, tx, t.Organisation, id)
		if e != nil {
			return e
		}
		if p.PropertyID != property || p.State != "ACTIVE" {
			return apperrors.PermissionDenied("Negotiated plans must be active in this property.")
		}
	}
	for _, source := range d.AllowedSources {
		if !slices.Contains([]string{"DIRECT", "CORPORATE", "GROUP", "WALK_IN", "MARKETPLACE", "OTA", "LEGACY"}, source) {
			return apperrors.InvalidRequest("Invalid corporate booking source.")
		}
	}
	return nil
}
