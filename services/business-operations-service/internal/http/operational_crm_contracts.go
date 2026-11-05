package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

func (h *Handler) crmCorporateContractSave(w http.ResponseWriter, r *http.Request) {
	var in CRMCorporateContractCommand
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedVersion < 0 || in.ContractKey != nil && in.ContractKey.ValidateV7() != nil || in.State != "ACTIVE" && in.State != "SUSPENDED" || in.State == "ACTIVE" && !in.ExpiresAt.After(in.EffectiveAt) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Versioned corporate terms, effective dates, lifecycle and reason required."))
		return
	}
	_, pre, property, e := h.crmCorporateTarget(r, "corporate:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:corporate-contract:"+pre.ID.String()+":"+property.String(), in, "corporate:write", "PROPERTY", property, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "corporate:write", "PROPERTY", property, true)
		if e != nil {
			return 0, nil, e
		}
		c, e := crm.GetCorporate(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if c.State != "ACTIVE" {
			return 0, nil, apperrors.Conflict("CORPORATE_INACTIVE", "Corporate customer must be active.")
		}
		if in.State == "ACTIVE" {
			if e = crm.RequireCorporateScope(ctx, tx, t.Organisation, property, c.ID); e != nil {
				return 0, nil, e
			}
			if e = h.validateCorporateTerms(ctx, tx, t, property, in.Definition); e != nil {
				return 0, nil, e
			}
		}
		key := ids.New()
		if in.ContractKey != nil {
			key = *in.ContractKey
		}
		var current int64
		var previousApproval, previousID *ids.ID
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0),(array_agg(approval_id ORDER BY version DESC))[1],(array_agg(id ORDER BY version DESC))[1] FROM corporate_customer_contracts WHERE organisation_id=$1 AND property_id=$2 AND corporate_id=$3 AND contract_key=$4`, t.Organisation, property, c.ID, key).Scan(&current, &previousApproval, &previousID); e != nil {
			return 0, nil, e
		}
		if current != in.ExpectedVersion || current == 0 && in.ContractKey != nil {
			return 0, nil, apperrors.Conflict("CORPORATE_CONTRACT_VERSION", "Corporate contract version/key changed.")
		}
		approval := in.ApprovalID
		if in.State == "ACTIVE" {
			if !h.Auth.Service.Now().Before(in.ExpiresAt) {
				return 0, nil, apperrors.InvalidRequest("Contract authority has expired.")
			}
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_CORPORATE_CONTRACT", target.Scope, approval, map[string]any{"corporate_id": c.ID, "property_id": property, "command": op}, "", &in.Definition.MaxBookingMinor, in.Definition.Currency); e != nil {
				return 0, nil, e
			}
		} else {
			if previousApproval == nil || !in.ApprovalID.IsZero() {
				return 0, nil, apperrors.InvalidRequest("Suspension requires an existing contract and does not consume new authority.")
			}
			approval = *previousApproval
			old, e := crm.GetContract(ctx, tx, t.Organisation, *previousID)
			if e != nil {
				return 0, nil, e
			}
			in.Definition = old.Definition
			in.EffectiveAt = old.EffectiveAt
			in.ExpiresAt = old.ExpiresAt
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO corporate_customer_contracts(id,organisation_id,property_id,corporate_id,contract_key,version,state,definition,effective_at,expires_at,approval_id,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, t.Organisation, property, c.ID, key, current+1, in.State, marshalCommercial(in.Definition), in.EffectiveAt, in.ExpiresAt, approval, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, property, key, "corporate_contract", "CORPORATE_CONTRACT_VERSION_CREATED", "corporate.customer.updated.v1", current+1, map[string]any{"corporate_id": c.ID, "contract_id": id, "contract_key": key, "state": in.State, "approval_id": approval, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := crm.GetContract(ctx, tx, t.Organisation, id)
		return 201, out, e
	})
}
func (h *Handler) crmCorporateTravelerSave(w http.ResponseWriter, r *http.Request) {
	var in CRMTravelerCommand
	if !decode(w, r, &in) {
		return
	}
	if in.GuestID.ValidateV7() != nil || in.ContractID.ValidateV7() != nil || in.ExpectedVersion < 0 || in.State != "ACTIVE" && in.State != "REVOKED" || in.State == "ACTIVE" && !in.ExpiresAt.After(in.EffectiveAt) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Exact guest/contract, bounded traveler policy and current version required."))
		return
	}
	_, pre, property, e := h.crmCorporateTarget(r, "corporate:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:traveler:"+in.ContractID.String()+":"+in.GuestID.String(), in, "corporate:write", "PROPERTY", property, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "corporate:write", "PROPERTY", property, true)
		if e != nil {
			return 0, nil, e
		}
		if in.State == "ACTIVE" {
			if _, _, e = h.authorize(ctx, tx, "guests:write", "PROPERTY", property, true); e != nil {
				return 0, nil, e
			}
		}
		contract, e := crm.GetContract(ctx, tx, t.Organisation, in.ContractID)
		if e != nil {
			return 0, nil, e
		}
		if contract.CorporateID != pre.ID || contract.PropertyID != property {
			return 0, nil, apperrors.PermissionDenied("Corporate traveler must belong to the exact property/customer contract.")
		}
		if in.State == "ACTIVE" {
			if e = crm.RequireGuest(ctx, tx, t.Organisation, property, in.GuestID); e != nil {
				return 0, nil, e
			}
			if e = h.validateCorporateTerms(ctx, tx, t, property, in.Policy); e != nil {
				return 0, nil, e
			}
			if in.EffectiveAt.Before(contract.EffectiveAt) || in.ExpiresAt.After(contract.ExpiresAt) || in.Policy.Currency != contract.Definition.Currency || in.Policy.MaxBookingMinor > contract.Definition.MaxBookingMinor || in.Policy.MaxNights > contract.Definition.MaxNights || in.Policy.BillingResponsibility != contract.Definition.BillingResponsibility || in.Policy.BillingReference != contract.Definition.BillingReference || contract.Definition.RequireNamedTravelers && !in.Policy.RequireNamedTravelers {
				return 0, nil, apperrors.PermissionDenied("Traveler policy cannot expand the current corporate contract.")
			}
			for _, id := range in.Policy.RatePlanIDs {
				if !slices.Contains(contract.Definition.RatePlanIDs, id) {
					return 0, nil, apperrors.PermissionDenied("Traveler cannot add a negotiated plan.")
				}
			}
			for _, source := range in.Policy.AllowedSources {
				if !slices.Contains(contract.Definition.AllowedSources, source) {
					return 0, nil, apperrors.PermissionDenied("Traveler cannot add a booking source.")
				}
			}
		}
		var current int64
		var old, oldApproval *ids.ID
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0),(array_agg(id))[1],(array_agg(approval_id))[1] FROM guest_company_associations WHERE organisation_id=$1 AND contract_id=$2 AND guest_id=$3`, t.Organisation, in.ContractID, in.GuestID).Scan(&current, &old, &oldApproval); e != nil {
			return 0, nil, e
		}
		if current != in.ExpectedVersion {
			return 0, nil, apperrors.Conflict("CORPORATE_TRAVELER_VERSION", "Traveler association changed.")
		}
		approval := in.ApprovalID
		if in.State == "ACTIVE" {
			if e = crm.RequireCurrentContract(ctx, tx, t.Organisation, property, pre.ID, contract.ID, h.Auth.Service.Now()); e != nil {
				return 0, nil, e
			}
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_CORPORATE_TRAVELER", target.Scope, approval, map[string]any{"corporate_id": pre.ID, "property_id": property, "command": op}, "", &in.Policy.MaxBookingMinor, in.Policy.Currency); e != nil {
				return 0, nil, e
			}
		} else {
			if oldApproval == nil || !in.ApprovalID.IsZero() {
				return 0, nil, apperrors.InvalidRequest("Revocation requires an existing traveler association.")
			}
			approval = *oldApproval
		}
		id := ids.New()
		if old == nil {
			_, e = tx.Exec(ctx, `INSERT INTO guest_company_associations(id,organisation_id,property_id,guest_id,corporate_id,contract_id,state,effective_at,expires_at,policy,approval_id,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, t.Organisation, property, in.GuestID, pre.ID, contract.ID, in.State, in.EffectiveAt, in.ExpiresAt, marshalCommercial(in.Policy), approval, t.Account)
		} else {
			id = *old
			if in.State == "REVOKED" {
				prior, readErr := crm.Load[struct {
					Policy      crm.CorporateTerms `json:"policy"`
					EffectiveAt time.Time          `json:"effective_at"`
					ExpiresAt   time.Time          `json:"expires_at"`
				}](ctx, tx, `SELECT to_jsonb(a) FROM guest_company_associations a WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
				if readErr != nil {
					return 0, nil, readErr
				}
				in.Policy = prior.Policy
				in.EffectiveAt = prior.EffectiveAt
				in.ExpiresAt = prior.ExpiresAt
				_, e = tx.Exec(ctx, `UPDATE guest_company_associations SET state='REVOKED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
			} else {
				_, e = tx.Exec(ctx, `UPDATE guest_company_associations SET state=$3,effective_at=$4,expires_at=$5,policy=$6,approval_id=$7,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.State, in.EffectiveAt, in.ExpiresAt, marshalCommercial(in.Policy), approval)
			}
		}
		if e != nil {
			return 0, nil, e
		}
		if e = h.crmGuestCommit(ctx, tx, t, property, in.GuestID, id, "corporate_traveler", current+1, "CORPORATE_TRAVELER_CHANGED", map[string]any{"corporate_id": pre.ID, "contract_id": contract.ID, "state": in.State, "approval_id": approval, "reason": in.Reason, "policy": in.Policy, "effective_at": in.EffectiveAt, "expires_at": in.ExpiresAt}); e != nil {
			return 0, nil, e
		}
		return 200, map[string]any{"id": id, "guest_id": in.GuestID, "corporate_id": pre.ID, "contract_id": contract.ID, "state": in.State, "version": current + 1}, nil
	})
}
func (h *Handler) crmCorporateContacts(w http.ResponseWriter, r *http.Request) {
	_, pre, property, e := h.crmCorporateTarget(r, "corporate:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	reason := r.URL.Query().Get("reason")
	if !validStaffReason(reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Protected corporate contact access requires a recorded purpose/reason."))
		return
	}
	out := []crm.CorporateContact{}
	e = database.WithTx(r.Context(), h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		t, _, e := h.authorize(ctx, tx, "corporate:write", "PROPERTY", property, true)
		if e != nil {
			return e
		}
		var cipher []byte
		var key string
		if e = tx.QueryRow(ctx, `SELECT c.encrypted_contacts,c.contact_key_id FROM corporate_customer_profiles c WHERE c.organisation_id=$1 AND c.id=$2 AND EXISTS(SELECT 1 FROM corporate_property_scopes s WHERE s.organisation_id=c.organisation_id AND s.corporate_id=c.id AND s.property_id=$3 AND s.state='ACTIVE')`, t.Organisation, pre.ID, property).Scan(&cipher, &key); e != nil {
			return e
		}
		if h.Auth.Service.Sealer == nil {
			return apperrors.Unavailable("Protected corporate storage is unavailable.")
		}
		plain, e := h.Auth.Service.Sealer.Open(key, cipher, corporateAAD(t.Organisation, pre.ID))
		if e != nil {
			return apperrors.Unavailable("Protected corporate contacts cannot be read.")
		}
		if e = json.Unmarshal(plain, &out); e != nil {
			return e
		}
		return audit(ctx, tx, t, "CORPORATE_PROTECTED_CONTACTS_READ", "corporate_customer", pre.ID, map[string]any{"property_id": property, "reason": reason})
	})
	w.Header().Set("Cache-Control", "no-store")
	reply(w, r, 200, map[string]any{"corporate_id": pre.ID, "contacts": out}, e)
}
func (h *Handler) crmCorporateScope(w http.ResponseWriter, r *http.Request) {
	var in CRMGuestScopeCommand
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.ExpectedVersion < 0 || in.State != "ACTIVE" && in.State != "REVOKED" || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Destination property, lifecycle and version required."))
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
	pre, e := crm.GetCorporate(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:corporate-scope:"+pre.ID.String()+":"+in.PropertyID.String(), in, "corporate:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "corporate:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		_, target, e := h.authorize(ctx, tx, "corporate:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		var version int64
		var old *ids.ID
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0),(array_agg(id))[1] FROM corporate_property_scopes WHERE organisation_id=$1 AND corporate_id=$2 AND property_id=$3`, t.Organisation, pre.ID, in.PropertyID).Scan(&version, &old); e != nil {
			return 0, nil, e
		}
		if version != in.ExpectedVersion {
			return 0, nil, apperrors.Conflict("CORPORATE_SCOPE_VERSION", "Corporate property association changed.")
		}
		var approval any
		if in.State == "ACTIVE" {
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_CORPORATE_SCOPE_LINK", target.Scope, in.ApprovalID, map[string]any{"corporate_id": pre.ID, "command": op}, "", nil, ""); e != nil {
				return 0, nil, e
			}
			approval = in.ApprovalID
		} else if !in.ApprovalID.IsZero() {
			return 0, nil, apperrors.InvalidRequest("Revocation does not consume scope expansion authority.")
		}
		id := ids.New()
		if old == nil {
			_, e = tx.Exec(ctx, `INSERT INTO corporate_property_scopes(id,organisation_id,corporate_id,property_id,state,approval_id,created_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, t.Organisation, pre.ID, in.PropertyID, in.State, approval, t.Account)
		} else {
			id = *old
			_, e = tx.Exec(ctx, `UPDATE corporate_property_scopes SET state=$3,approval_id=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.State, approval)
		}
		if e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "corporate_property_scope", "CORPORATE_PROPERTY_SCOPE_CHANGED", "corporate.customer.updated.v1", version+1, map[string]any{"corporate_id": pre.ID, "state": in.State, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 200, map[string]any{"id": id, "corporate_id": pre.ID, "property_id": in.PropertyID, "state": in.State, "version": version + 1}, nil
	})
}
