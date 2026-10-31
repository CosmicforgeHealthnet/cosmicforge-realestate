package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func (h *Handler) registerRates(r chi.Router) {
	r.Get("/rate-plans", h.rateList)
	r.Post("/rate-plans", h.rateCreate)
	r.Get("/rate-plans/{id}", h.rateGet)
	r.Patch("/rate-plans/{id}", h.ratePatch)
	r.Get("/rate-plans/{id}/versions", h.rateVersions)
	r.Get("/rate-plan-versions/{id}", h.rateVersionGet)
	r.Get("/restrictions", h.restrictionList)
	r.Post("/restrictions", h.restrictionCreate)
	r.Patch("/restrictions/{id}", h.restrictionPatch)
	r.Get("/promotions", h.promotionList)
	r.Post("/promotions", h.promotionCreate)
	r.Patch("/promotions/{id}", h.promotionPatch)
	r.Get("/add-ons", h.addOnList)
	r.Post("/add-ons", h.addOnCreate)
	r.Patch("/add-ons/{id}", h.addOnPatch)
	r.Get("/packages", h.packageList)
	r.Post("/packages", h.packageCreate)
	r.Patch("/packages/{id}", h.packagePatch)
	r.Post("/rate-quotes", h.quoteCreate)
	r.Get("/rate-quotes/{id}", h.quoteGet)
	r.Post("/rates/bulk-preview", h.bulkRatePreview)
	r.Post("/rates/bulk-apply", h.bulkRateApply)
	r.Post("/rates/bulk-rollback", h.bulkRateRollback)
	r.Get("/rates/bulk-commands/{id}", h.bulkRateGet)
	r.Get("/tax-profiles", h.taxList)
	r.Post("/tax-profiles", h.taxCreate)
	r.Get("/currency-policy", h.currencyPolicyGet)
	r.Put("/currency-policy", h.currencyPolicySet)
	r.Get("/compliance-holds", h.complianceHoldList)
	r.Post("/compliance-holds", h.complianceHoldCreate)
	r.Post("/compliance-holds/{id}/revoke", h.complianceHoldRevoke)
}
func (h *Handler) validateProductRate(ctx context.Context, q database.Querier, org, property, plan ids.ID) error {
	p, e := revenue.GetPlan(ctx, q, org, plan)
	if e != nil {
		return e
	}
	if p.PropertyID != property || p.State == "ARCHIVED" {
		return apperrors.PermissionDenied("Rate plan must belong to this canonical property.")
	}
	return nil
}
func (h *Handler) validateActiveRate(ctx context.Context, q database.Querier, org, plan ids.ID) error {
	p, e := revenue.GetPlan(ctx, q, org, plan)
	if e != nil {
		return e
	}
	if p.State != "ACTIVE" {
		return apperrors.Conflict("RATE_INACTIVE", "An active rate plan is required.")
	}
	return nil
}
func (h *Handler) rateList(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "rate_plans", "rates:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		return revenue.GetPlan(ctx, h.Auth.Service.Pool, org, id)
	})
}
func (h *Handler) rateTarget(r *http.Request, cap string, mfa bool) (tenant, revenue.Plan, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, revenue.Plan{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, revenue.Plan{}, e
	}
	p, e := revenue.GetPlan(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e == nil {
		t, e = h.commercialScope(r, cap, p.PropertyID, mfa)
	}
	return t, p, e
}
func (h *Handler) rateGet(w http.ResponseWriter, r *http.Request) {
	_, p, e := h.rateTarget(r, "rates:read", false)
	reply(w, r, 200, p, e)
}
func (h *Handler) rateVersions(w http.ResponseWriter, r *http.Request) {
	t, p, e := h.rateTarget(r, "rates:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	values, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(v) FROM rate_plan_versions v WHERE organisation_id=$1 AND rate_plan_id=$2 ORDER BY version DESC LIMIT $3 OFFSET $4`, t.Organisation, p.ID, limit, offset)
	reply(w, r, 200, map[string]any{"items": values, "limit": limit, "offset": offset}, e)
}
func (h *Handler) rateVersionGet(w http.ResponseWriter, r *http.Request) {
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
	v, e := revenue.GetVersion(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e == nil {
		_, e = h.commercialScope(r, "rates:read", v.PropertyID, false)
	}
	reply(w, r, 200, v, e)
}
func commercialJSONRows(ctx context.Context, q database.Querier, query string, args ...any) ([]json.RawMessage, error) {
	rows, e := q.Query(ctx, query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		result = append(result, raw)
	}
	return result, rows.Err()
}

type CommercialRateCreate struct {
	PropertyID ids.ID                 `json:"property_id"`
	Name       string                 `json:"name"`
	Kind       string                 `json:"kind"`
	DateFrom   string                 `json:"date_from"`
	DateTo     string                 `json:"date_to"`
	Definition revenue.RateDefinition `json:"definition"`
	Reason     string                 `json:"reason"`
}

func validRateKind(kind string) bool {
	switch kind {
	case "STANDARD", "REFUNDABLE", "NON_REFUNDABLE", "CORPORATE", "PROMOTIONAL", "PACKAGE", "ADVANCE_PURCHASE", "LONG_STAY", "SEASONAL", "CONFIGURABLE":
		return true
	}
	return false
}
func (h *Handler) rateCreate(w http.ResponseWriter, r *http.Request) {
	var in CommercialRateCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || !commercialName(in.Name) || !validRateKind(in.Kind) || in.Definition.Validate() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid property, plan type, exact price policy, effective dates and reason required."))
		return
	}
	if _, e := (inventory.DateRange{DateFrom: in.DateFrom, DateTo: in.DateTo}).Days("UTC"); e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Rate effective range must contain 1–366 civil dates."))
		return
	}
	h.execute(w, r, "commercial:rate:create", in, "rates:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "rates:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if e = h.validateRateReferences(ctx, tx, t.Organisation, in.PropertyID, id, in.Definition); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO rate_plans(id,organisation_id,property_id,name,kind,created_by) VALUES($1,$2,$3,$4,$5,$6)`, id, t.Organisation, in.PropertyID, in.Name, in.Kind, t.Account); e != nil {
			return 0, nil, e
		}
		version, e := h.writeRateVersion(ctx, tx, t, in.PropertyID, id, in.Definition, in.DateFrom, in.DateTo, in.Reason, ids.Nil)
		if e != nil {
			return 0, nil, e
		}
		plan, e := revenue.GetPlan(ctx, tx, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "rate_plan", "RATE_PLAN_CREATED", businessEvents.RatePlanChanged, plan.Version, map[string]any{"plan": plan, "rate_version": version})
		return 201, map[string]any{"rate_plan": plan, "rate_version": version}, e
	})
}
func (h *Handler) validateRateReferences(ctx context.Context, tx pgx.Tx, org, property, plan ids.ID, d revenue.RateDefinition) error {
	if d.Validate() != nil {
		return apperrors.InvalidRequest("Invalid exact price, occupancy, policy, bounds or calculation rules.")
	}
	if e := revenue.ValidateParent(ctx, tx, org, property, plan, d); e != nil {
		return e
	}
	var propertyCurrency string
	if e := tx.QueryRow(ctx, `SELECT currency FROM inventory_nodes WHERE organisation_id=$1 AND id=$2 AND kind='PROPERTY'`, org, property).Scan(&propertyCurrency); e != nil {
		return e
	}
	if propertyCurrency != d.Currency {
		var configured bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM currency_policies WHERE organisation_id=$1 AND property_id=$2 AND currency=$3)`, org, property, d.Currency).Scan(&configured); e != nil {
			return e
		}
		if !configured {
			return apperrors.PermissionDenied("An approved property currency policy is required; implicit FX is prohibited.")
		}
	}
	if d.TaxProfileID != nil {
		var same bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tax_profiles WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND currency=$4)`, org, property, *d.TaxProfileID, d.Currency).Scan(&same); e != nil {
			return e
		}
		if !same {
			return apperrors.PermissionDenied("Tax/fiscal reference must match the owning property and rate currency.")
		}
	}
	for _, id := range d.IncludedAddOns {
		var same bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM add_ons WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND definition->>'currency'=$4)`, org, property, id, d.Currency).Scan(&same); e != nil {
			return e
		}
		if !same {
			return apperrors.PermissionDenied("Included add-ons must have matching tenant/property/currency.")
		}
	}
	return nil
}
func (h *Handler) writeRateVersion(ctx context.Context, tx pgx.Tx, t tenant, property, plan ids.ID, d revenue.RateDefinition, from, to, reason string, approval ids.ID) (revenue.RateVersion, error) {
	if _, e := (inventory.DateRange{DateFrom: from, DateTo: to}).Days("UTC"); e != nil {
		return revenue.RateVersion{}, apperrors.InvalidRequest("Invalid effective civil-date range.")
	}
	if e := h.validateRateReferences(ctx, tx, t.Organisation, property, plan, d); e != nil {
		return revenue.RateVersion{}, e
	}
	var number int64
	if e := tx.QueryRow(ctx, `SELECT coalesce(max(version),0)+1 FROM rate_plan_versions WHERE organisation_id=$1 AND rate_plan_id=$2`, t.Organisation, plan).Scan(&number); e != nil {
		return revenue.RateVersion{}, e
	}
	id := ids.New()
	raw, e := json.Marshal(d)
	if e != nil {
		return revenue.RateVersion{}, e
	}
	var approvalRef any
	if !approval.IsZero() {
		approvalRef = approval
	}
	if _, e = tx.Exec(ctx, `INSERT INTO rate_plan_versions(id,organisation_id,property_id,rate_plan_id,version,currency,amount_minor,definition,effective_from,effective_to,approval_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::date,$10::date,$11,$12,$13)`, id, t.Organisation, property, plan, number, d.Currency, d.AmountMinor, raw, from, to, approvalRef, reason, t.Account); e != nil {
		return revenue.RateVersion{}, e
	}
	if d.ParentVersionID != nil {
		adjustments, _ := json.Marshal(d.Adjustments)
		if _, e = tx.Exec(ctx, `INSERT INTO rate_relations(organisation_id,child_version_id,parent_version_id,adjustments) VALUES($1,$2,$3,$4)`, t.Organisation, id, *d.ParentVersionID, adjustments); e != nil {
			return revenue.RateVersion{}, e
		}
	}
	for _, rule := range d.Rules {
		definition, _ := json.Marshal(rule)
		if _, e = tx.Exec(ctx, `INSERT INTO rate_rules(id,organisation_id,rate_version_id,priority,definition) VALUES($1,$2,$3,$4,$5)`, ids.New(), t.Organisation, id, rule.Priority, definition); e != nil {
			return revenue.RateVersion{}, e
		}
	}
	return revenue.GetVersion(ctx, tx, t.Organisation, id)
}

type CommercialRatePatch struct {
	Version    int64                   `json:"version"`
	State      string                  `json:"state,omitempty"`
	Name       *string                 `json:"name,omitempty"`
	DateFrom   string                  `json:"date_from,omitempty"`
	DateTo     string                  `json:"date_to,omitempty"`
	Definition *revenue.RateDefinition `json:"definition,omitempty"`
	Reason     string                  `json:"reason"`
	ApprovalID ids.ID                  `json:"approval_id,omitempty"`
}

func rateWithinBand(previous, next revenue.RateDefinition) bool {
	old, proposed := previous, next
	old.AmountMinor = 0
	proposed.AmountMinor = 0
	if !reflect.DeepEqual(old, proposed) || previous.AmountMinor == 0 {
		return previous.AmountMinor == next.AmountMinor && reflect.DeepEqual(old, proposed)
	}
	difference := next.AmountMinor - previous.AmountMinor
	if difference < 0 {
		difference = -difference
	}
	allowed, e := revenue.Ratio(previous.AmountMinor, previous.MaxChangeBPS, 10000)
	return e == nil && difference <= allowed
}
func (h *Handler) ratePatch(w http.ResponseWriter, r *http.Request) {
	var in CommercialRatePatch
	if !decode(w, r, &in) {
		return
	}
	_, p, e := h.rateTarget(r, "rates:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) || in.Name != nil && !commercialName(*in.Name) || in.Definition != nil && in.Definition.Validate() != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, valid price policy and reason required."))
		return
	}
	h.execute(w, r, "commercial:rate:update:"+p.ID.String(), in, "rates:write", "PROPERTY", p.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "rates:write", "PROPERTY", p.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := revenue.GetPlan(ctx, tx, t.Organisation, p.ID)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Rate plan changed.")
		}
		if current.State == "ARCHIVED" || in.State == "RETIRED" || in.State != "" && !commercialTransition(current.State, in.State) {
			return 0, nil, apperrors.InvalidRequest("Invalid rate lifecycle transition.")
		}
		command := in
		command.ApprovalID = ids.Nil
		operation := map[string]any{"rate_plan_id": p.ID, "command": command}
		exposureDefinition := in.Definition
		if exposureDefinition == nil {
			var latest ids.ID
			if e = tx.QueryRow(ctx, `SELECT id FROM rate_plan_versions WHERE organisation_id=$1 AND rate_plan_id=$2 ORDER BY version DESC LIMIT 1`, t.Organisation, p.ID).Scan(&latest); e != nil {
				return 0, nil, e
			}
			v, e := revenue.GetVersion(ctx, tx, t.Organisation, latest)
			if e != nil {
				return 0, nil, e
			}
			exposureDefinition = &v.Definition
		}
		exposure := exposureDefinition.CeilingMinor
		exposureCurrency := exposureDefinition.Currency
		consumedApproval := false
		if in.State == "ACTIVE" {
			if e = h.commercialGate(ctx, tx, t, p.PropertyID); e != nil {
				return 0, nil, e
			}
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RATE_ACTIVATION", target.Scope, in.ApprovalID, operation, "", &exposure, exposureCurrency); e != nil {
				return 0, nil, e
			}
			consumedApproval = true
		}
		var version *revenue.RateVersion
		if in.Definition != nil {
			var latest ids.ID
			if e = tx.QueryRow(ctx, `SELECT id FROM rate_plan_versions WHERE organisation_id=$1 AND rate_plan_id=$2 ORDER BY version DESC LIMIT 1`, t.Organisation, p.ID).Scan(&latest); e != nil {
				return 0, nil, e
			}
			previous, e := revenue.GetVersion(ctx, tx, t.Organisation, latest)
			if e != nil {
				return 0, nil, e
			}
			if in.State == "ACTIVE" && !rateWithinBand(previous.Definition, *in.Definition) {
				return 0, nil, apperrors.InvalidRequest("Separate rate activation from an out-of-band price/policy change.")
			}
			if in.State != "ACTIVE" && !rateWithinBand(previous.Definition, *in.Definition) {
				if e = h.requireCommercialApproval(ctx, tx, t, approvals.ActionExceptionalRateChange, target.Scope, in.ApprovalID, operation, "", &exposure, exposureCurrency); e != nil {
					return 0, nil, e
				}
				consumedApproval = true
			}
			if !in.ApprovalID.IsZero() && !consumedApproval {
				return 0, nil, apperrors.InvalidRequest("An approval reference must be consumed by this exact high-risk price operation.")
			}
			v, e := h.writeRateVersion(ctx, tx, t, p.PropertyID, p.ID, *in.Definition, in.DateFrom, in.DateTo, in.Reason, in.ApprovalID)
			if e != nil {
				return 0, nil, e
			}
			version = &v
		}
		if _, e = tx.Exec(ctx, `UPDATE rate_plans SET state=coalesce(nullif($3,''),state),name=coalesce($4,name),version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, p.ID, in.State, in.Name); e != nil {
			return 0, nil, e
		}
		updated, e := revenue.GetPlan(ctx, tx, t.Organisation, p.ID)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, p.PropertyID, p.ID, "rate_plan", "RATE_PLAN_UPDATED", businessEvents.RatePlanChanged, updated.Version, map[string]any{"plan": updated, "rate_version": version, "reason": in.Reason})
		return 200, map[string]any{"rate_plan": updated, "rate_version": version}, e
	})
}

type CommercialRestrictionCreate struct {
	PropertyID ids.ID              `json:"property_id"`
	PoolID     *ids.ID             `json:"pool_id,omitempty"`
	ProductID  *ids.ID             `json:"product_id,omitempty"`
	RatePlanID *ids.ID             `json:"rate_plan_id,omitempty"`
	Channel    string              `json:"channel,omitempty"`
	DateFrom   string              `json:"date_from"`
	DateTo     string              `json:"date_to"`
	Definition revenue.Restriction `json:"definition"`
	Reason     string              `json:"reason"`
}

func (h *Handler) restrictionList(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "rate_restrictions", "restrictions:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		return typedJSON[revenue.RestrictionRecord](ctx, h.Auth.Service.Pool, `SELECT to_jsonb(r) FROM rate_restrictions r WHERE organisation_id=$1 AND id=$2`, org, id)
	})
}
func (h *Handler) restrictionCreate(w http.ResponseWriter, r *http.Request) {
	var in CommercialRestrictionCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.Definition.Validate() != nil || !inventory.ValidChannel(in.Channel) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid scope, restriction policy and reason required."))
		return
	}
	if _, e := (inventory.DateRange{DateFrom: in.DateFrom, DateTo: in.DateTo}).Days("UTC"); e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid restriction civil-date range."))
		return
	}
	h.execute(w, r, "commercial:restriction:create", in, "restrictions:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "restrictions:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		if in.ProductID != nil {
			p, e := inventory.GetProduct(ctx, tx, t.Organisation, *in.ProductID)
			if e != nil {
				return 0, nil, e
			}
			if p.PropertyID != in.PropertyID || in.PoolID != nil && *in.PoolID != p.PoolID || in.RatePlanID != nil && (p.RatePlanID == nil || *in.RatePlanID != *p.RatePlanID) {
				return 0, nil, apperrors.InvalidRequest("Restriction product, pool and rate scope must describe the same canonical product.")
			}
		}
		existing, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(r) FROM rate_restrictions r WHERE organisation_id=$1 AND property_id=$2 AND state='ACTIVE' AND date_from<$4::date AND date_to>$3::date AND (channel='' OR $5='' OR channel=$5) AND (pool_id IS NULL OR $6::uuid IS NULL OR pool_id=$6) AND (product_id IS NULL OR $7::uuid IS NULL OR product_id=$7) AND (rate_plan_id IS NULL OR $8::uuid IS NULL OR rate_plan_id=$8) ORDER BY id LIMIT 1001`, t.Organisation, in.PropertyID, in.DateFrom, in.DateTo, in.Channel, in.PoolID, in.ProductID, in.RatePlanID)
		if e != nil {
			return 0, nil, e
		}
		if len(existing) > 1000 {
			return 0, nil, apperrors.Conflict("RESTRICTION_REVIEW_REQUIRED", "Too many overlapping restrictions; consolidate policy before publishing.")
		}
		for _, raw := range existing {
			var record revenue.RestrictionRecord
			if e = json.Unmarshal(raw, &record); e != nil {
				return 0, nil, e
			}
			overlap := len(record.Definition.Weekdays) == 0 || len(in.Definition.Weekdays) == 0
			for _, day := range in.Definition.Weekdays {
				overlap = overlap || slicesCommercialDay(record.Definition.Weekdays, day)
			}
			if overlap {
				if _, e = revenue.Intersect([]revenue.Restriction{in.Definition, record.Definition}); e != nil {
					return 0, nil, apperrors.Conflict("RESTRICTION_CONFLICT", "The overlapping hard windows cannot be satisfied together.")
				}
			}
		}
		id := ids.New()
		raw, _ := json.Marshal(in.Definition)
		if _, e = tx.Exec(ctx, `INSERT INTO rate_restrictions(id,organisation_id,property_id,pool_id,product_id,rate_plan_id,channel,date_from,date_to,definition,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8::date,$9::date,$10,$11,$12)`, id, t.Organisation, in.PropertyID, in.PoolID, in.ProductID, in.RatePlanID, in.Channel, in.DateFrom, in.DateTo, raw, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		record, e := typedJSON[revenue.RestrictionRecord](ctx, tx, `SELECT to_jsonb(r) FROM rate_restrictions r WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "rate_restriction", "RESTRICTION_CREATED", businessEvents.InventoryChanged, 1, record)
		return 201, record, e
	})
}
func slicesCommercialDay(days []int, day int) bool {
	for _, d := range days {
		if d == day {
			return true
		}
	}
	return false
}
func (h *Handler) restrictionPatch(w http.ResponseWriter, r *http.Request) {
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
	old, e := typedJSON[revenue.RestrictionRecord](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(r) FROM rate_restrictions r WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if errors.Is(e, pgx.ErrNoRows) {
		e = apperrors.NotFound("RESTRICTION_NOT_FOUND", "Restriction not found.")
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || in.State != "REVOKED" || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Only reasoned, versioned revocation is allowed; policies are replaced with new records."))
		return
	}
	h.execute(w, r, "commercial:restriction:revoke:"+id.String(), in, "restrictions:write", "PROPERTY", old.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "restrictions:write", "PROPERTY", old.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := typedJSON[revenue.RestrictionRecord](ctx, tx, `SELECT to_jsonb(r) FROM rate_restrictions r WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.Version || current.State != "ACTIVE" {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Restriction changed.")
		}
		command := in
		command.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESTRICTION_RELAXATION", target.Scope, in.ApprovalID, map[string]any{"restriction_id": id, "command": command}, "", nil, ""); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE rate_restrictions SET state='REVOKED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id); e != nil {
			return 0, nil, e
		}
		current.State = "REVOKED"
		current.Version++
		e = h.commercialCommit(ctx, tx, t, old.PropertyID, id, "rate_restriction", "RESTRICTION_REVOKED", businessEvents.InventoryChanged, current.Version, current)
		return 200, current, e
	})
}

// Reference-only view: pricing uses the existing money primitive and never
// executes an obligation, provider charge, ledger entry, payout or refund.
func commercialMoney(amount int64, currency string) (money.Money, error) {
	return money.New(amount, currency)
}
