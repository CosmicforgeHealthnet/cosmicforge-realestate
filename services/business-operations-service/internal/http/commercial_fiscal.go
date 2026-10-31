package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/jackc/pgx/v5"
)

type CommercialTaxProfile struct {
	ID              ids.ID            `json:"id"`
	OrganisationID  ids.ID            `json:"organisation_id"`
	PropertyID      ids.ID            `json:"property_id"`
	PolicyKey       ids.ID            `json:"policy_key"`
	Version         int64             `json:"version"`
	Currency        string            `json:"currency"`
	Definition      revenue.TaxPolicy `json:"definition"`
	SourceReference string            `json:"source_reference"`
	ApprovalID      *ids.ID           `json:"approval_id,omitempty"`
	EffectiveAt     time.Time         `json:"effective_at"`
	ExpiresAt       *time.Time        `json:"expires_at,omitempty"`
}
type CommercialTaxCreate struct {
	PropertyID      ids.ID            `json:"property_id"`
	PolicyKey       *ids.ID           `json:"policy_key,omitempty"`
	ExpectedVersion int64             `json:"expected_version,omitempty"`
	Currency        string            `json:"currency"`
	Definition      revenue.TaxPolicy `json:"definition"`
	SourceReference string            `json:"source_reference"`
	EffectiveAt     time.Time         `json:"effective_at"`
	ExpiresAt       *time.Time        `json:"expires_at,omitempty"`
	Reason          string            `json:"reason"`
	ApprovalID      ids.ID            `json:"approval_id"`
}

func (h *Handler) taxList(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "tax_profiles", "tax:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		return typedJSON[CommercialTaxProfile](ctx, h.Auth.Service.Pool, `SELECT to_jsonb(t) FROM tax_profiles t WHERE organisation_id=$1 AND id=$2`, org, id)
	})
}
func (h *Handler) taxCreate(w http.ResponseWriter, r *http.Request) {
	var in CommercialTaxCreate
	if !decode(w, r, &in) {
		return
	}
	_, currencyErr := money.ParseCurrency(in.Currency)
	if in.PropertyID.ValidateV7() != nil || in.Definition.Validate() != nil || currencyErr != nil || len(in.SourceReference) < 1 || len(in.SourceReference) > 500 || in.EffectiveAt.IsZero() || in.ExpiresAt != nil && !in.ExpiresAt.After(in.EffectiveAt) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A source-backed, versioned fiscal policy, currency, dates and reason are required."))
		return
	}
	h.execute(w, r, "commercial:tax:create", in, "tax:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "tax:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		command := in
		command.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_FISCAL_POLICY_CHANGE", target.Scope, in.ApprovalID, map[string]any{"property_id": in.PropertyID, "command": command}, "", nil, ""); e != nil {
			return 0, nil, e
		}
		key := ids.New()
		version := int64(1)
		if in.PolicyKey != nil {
			key = *in.PolicyKey
			var previousProperty ids.ID
			if e = tx.QueryRow(ctx, `SELECT property_id,version FROM tax_profiles WHERE organisation_id=$1 AND policy_key=$2 ORDER BY version DESC LIMIT 1`, t.Organisation, key).Scan(&previousProperty, &version); e != nil {
				return 0, nil, e
			}
			if previousProperty != in.PropertyID || version != in.ExpectedVersion {
				return 0, nil, apperrors.Conflict("FISCAL_VERSION_CONFLICT", "Previous fiscal policy scope/version changed.")
			}
			version++
		} else if in.ExpectedVersion != 0 {
			return 0, nil, apperrors.InvalidRequest("New policies have no previous version.")
		}
		id := ids.New()
		definition, _ := json.Marshal(in.Definition)
		if _, e = tx.Exec(ctx, `INSERT INTO tax_profiles(id,organisation_id,property_id,policy_key,version,currency,definition,source_reference,approval_id,effective_at,expires_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, t.Organisation, in.PropertyID, key, version, in.Currency, definition, in.SourceReference, in.ApprovalID, in.EffectiveAt, in.ExpiresAt, t.Account); e != nil {
			return 0, nil, e
		}
		value, e := typedJSON[CommercialTaxProfile](ctx, tx, `SELECT to_jsonb(t) FROM tax_profiles t WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "tax_profile", "FISCAL_POLICY_VERSION_CREATED", businessEvents.RatePlanChanged, version, value)
		return 201, value, e
	})
}

type CommercialCurrencyPolicy struct {
	ID              ids.ID `json:"id"`
	OrganisationID  ids.ID `json:"organisation_id"`
	PropertyID      ids.ID `json:"property_id"`
	Currency        string `json:"currency"`
	PrecisionDigits int    `json:"precision_digits"`
	Rounding        string `json:"rounding"`
	SourceReference string `json:"source_reference"`
	Version         int64  `json:"version"`
}
type CommercialCurrencyCommand struct {
	PropertyID      ids.ID `json:"property_id"`
	Currency        string `json:"currency"`
	PrecisionDigits int    `json:"precision_digits"`
	Rounding        string `json:"rounding"`
	SourceReference string `json:"source_reference"`
	Version         int64  `json:"version"`
	Reason          string `json:"reason"`
	ApprovalID      ids.ID `json:"approval_id"`
}

func (h *Handler) currencyPolicyGet(w http.ResponseWriter, r *http.Request) {
	property, e := queryCommercialID(r, "property_id")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	t, e := h.commercialScope(r, "tax:read", property, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(c)-'created_by' FROM currency_policies c WHERE organisation_id=$1 AND property_id=$2 ORDER BY currency`, t.Organisation, property)
	reply(w, r, 200, map[string]any{"items": rows, "default_precision": "SHARED_ISO4217_MINOR_UNITS", "default_rounding": "HALF_AWAY_FROM_ZERO", "implicit_fx": false}, e)
}
func (h *Handler) currencyPolicySet(w http.ResponseWriter, r *http.Request) {
	var in CommercialCurrencyCommand
	if !decode(w, r, &in) {
		return
	}
	currency, e := money.ParseCurrency(in.Currency)
	if e != nil || in.PropertyID.ValidateV7() != nil || in.PrecisionDigits != currency.MinorUnits() || in.Rounding != "HALF_AWAY_FROM_ZERO" || len(in.SourceReference) < 1 || len(in.SourceReference) > 500 || in.Version < 0 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Explicit ISO minor-unit precision, approved rounding/source, current version and reason required; FX is unsupported."))
		return
	}
	h.execute(w, r, "commercial:currency:set", in, "tax:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "tax:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		command := in
		command.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_CURRENCY_POLICY_CHANGE", target.Scope, in.ApprovalID, map[string]any{"property_id": in.PropertyID, "command": command}, "", nil, ""); e != nil {
			return 0, nil, e
		}
		var id ids.ID
		var version int64
		e = tx.QueryRow(ctx, `SELECT id,version FROM currency_policies WHERE organisation_id=$1 AND property_id=$2 AND currency=$3 FOR UPDATE`, t.Organisation, in.PropertyID, in.Currency).Scan(&id, &version)
		if errors.Is(e, pgx.ErrNoRows) {
			if in.Version != 0 {
				return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Currency policy does not exist.")
			}
			id = ids.New()
			version = 1
			_, e = tx.Exec(ctx, `INSERT INTO currency_policies(id,organisation_id,property_id,currency,precision_digits,rounding,source_reference,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, in.PropertyID, in.Currency, in.PrecisionDigits, in.Rounding, in.SourceReference, t.Account)
		} else if e == nil {
			if version != in.Version {
				return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Currency policy changed.")
			}
			version++
			_, e = tx.Exec(ctx, `UPDATE currency_policies SET precision_digits=$4,rounding=$5,source_reference=$6,version=version+1 WHERE organisation_id=$1 AND property_id=$2 AND currency=$3`, t.Organisation, in.PropertyID, in.Currency, in.PrecisionDigits, in.Rounding, in.SourceReference)
		}
		if e != nil {
			return 0, nil, e
		}
		value, e := typedJSON[CommercialCurrencyPolicy](ctx, tx, `SELECT to_jsonb(c) FROM currency_policies c WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "currency_policy", "CURRENCY_POLICY_SET", businessEvents.RatePlanChanged, version, value)
		return 200, value, e
	})
}

type CommercialComplianceHold struct {
	SourceService   string     `json:"source_service"`
	ID              ids.ID     `json:"id"`
	OrganisationID  ids.ID     `json:"organisation_id"`
	PropertyID      ids.ID     `json:"property_id"`
	Reason          string     `json:"reason"`
	SourceReference string     `json:"source_reference"`
	EffectiveAt     time.Time  `json:"effective_at"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
	Version         int64      `json:"version"`
}
type CommercialComplianceCreate struct {
	PropertyID      ids.ID     `json:"property_id"`
	Reason          string     `json:"reason"`
	SourceReference string     `json:"source_reference"`
	EffectiveAt     time.Time  `json:"effective_at"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
}

func (h *Handler) complianceHoldList(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "compliance_holds", "restrictions:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		return typedJSON[CommercialComplianceHold](ctx, h.Auth.Service.Pool, `SELECT to_jsonb(h) FROM compliance_holds h WHERE organisation_id=$1 AND id=$2`, org, id)
	})
}
func (h *Handler) complianceHoldCreate(w http.ResponseWriter, r *http.Request) {
	var in CommercialComplianceCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || !validStaffReason(in.Reason) || len(in.SourceReference) < 1 || len(in.SourceReference) > 500 || in.EffectiveAt.IsZero() || in.ExpiresAt != nil && !in.ExpiresAt.After(in.EffectiveAt) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A scoped compliance reason/source and valid effective window are required."))
		return
	}
	h.execute(w, r, "commercial:compliance:create", in, "restrictions:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "restrictions:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO compliance_holds(id,organisation_id,property_id,reason,source_reference,effective_at,expires_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, in.PropertyID, in.Reason, in.SourceReference, in.EffectiveAt, in.ExpiresAt, t.Account); e != nil {
			return 0, nil, e
		}
		value, e := typedJSON[CommercialComplianceHold](ctx, tx, `SELECT to_jsonb(h) FROM compliance_holds h WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "compliance_hold", "COMPLIANCE_HOLD_CREATED", businessEvents.InventoryChanged, 1, value)
		return 201, value, e
	})
}
func (h *Handler) complianceHoldRevoke(w http.ResponseWriter, r *http.Request) {
	var in CommercialStateChange
	if !decode(w, r, &in) {
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
	hold, e := typedJSON[CommercialComplianceHold](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(h) FROM compliance_holds h WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if errors.Is(e, pgx.ErrNoRows) {
		e = apperrors.NotFound("COMPLIANCE_HOLD_NOT_FOUND", "Compliance hold not found.")
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current compliance hold version and review reason required."))
		return
	}
	h.execute(w, r, "commercial:compliance:revoke:"+id.String(), in, "restrictions:write", "PROPERTY", hold.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "restrictions:write", "PROPERTY", hold.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := typedJSON[CommercialComplianceHold](ctx, tx, `SELECT to_jsonb(h) FROM compliance_holds h WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if current.SourceService != "business-operations-service" {
			return 0, nil, apperrors.PermissionDenied("Externally imposed compliance holds require their owning authenticated provider to release them.")
		}
		if current.Version != in.Version || current.RevokedAt != nil {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Compliance hold changed.")
		}
		command := in
		command.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_COMPLIANCE_RELEASE", target.Scope, in.ApprovalID, map[string]any{"compliance_hold_id": id, "command": command}, "", nil, ""); e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		if _, e = tx.Exec(ctx, `UPDATE compliance_holds SET revoked_at=$3,version=version+1 WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, now); e != nil {
			return 0, nil, e
		}
		current.RevokedAt = &now
		current.Version++
		e = h.commercialCommit(ctx, tx, t, hold.PropertyID, id, "compliance_hold", "COMPLIANCE_HOLD_REVOKED", businessEvents.InventoryChanged, current.Version, current)
		return 200, current, e
	})
}
