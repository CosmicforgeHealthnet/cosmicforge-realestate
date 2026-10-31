package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

var commercialCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,79}$`)

type CommercialPromotionCreate struct {
	PropertyID ids.ID                      `json:"property_id"`
	Code       string                      `json:"code"`
	Definition revenue.PromotionDefinition `json:"definition"`
	Reason     string                      `json:"reason"`
}
type CommercialAddOnCreate struct {
	PropertyID ids.ID                  `json:"property_id"`
	Name       string                  `json:"name"`
	Definition revenue.AddOnDefinition `json:"definition"`
	Reason     string                  `json:"reason"`
}
type CommercialPackageComponent struct {
	AddOnID  ids.ID `json:"add_on_id"`
	Quantity int    `json:"quantity"`
}
type CommercialPackageCreate struct {
	PropertyID ids.ID                       `json:"property_id"`
	ProductID  ids.ID                       `json:"product_id"`
	Name       string                       `json:"name"`
	Definition revenue.PackageDefinition    `json:"definition"`
	Components []CommercialPackageComponent `json:"components"`
	Reason     string                       `json:"reason"`
}

func (h *Handler) promotionList(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "promotions", "promotions:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		return typedJSON[revenue.Promotion](ctx, h.Auth.Service.Pool, `SELECT to_jsonb(p) FROM promotions p WHERE organisation_id=$1 AND id=$2`, org, id)
	})
}
func (h *Handler) addOnList(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "add_ons", "promotions:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		return typedJSON[revenue.AddOn](ctx, h.Auth.Service.Pool, `SELECT to_jsonb(p) FROM add_ons p WHERE organisation_id=$1 AND id=$2`, org, id)
	})
}
func (h *Handler) packageList(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "packages", "promotions:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		p, e := typedJSON[revenue.Package](ctx, h.Auth.Service.Pool, `SELECT to_jsonb(p) FROM packages p WHERE organisation_id=$1 AND id=$2`, org, id)
		if e != nil {
			return nil, e
		}
		components, e := commercialJSONRows(ctx, h.Auth.Service.Pool, `SELECT jsonb_build_object('add_on_id',add_on_id,'quantity',quantity) FROM package_components WHERE organisation_id=$1 AND package_id=$2 ORDER BY add_on_id`, org, id)
		return map[string]any{"package": p, "components": components}, e
	})
}
func (h *Handler) promotionCreate(w http.ResponseWriter, r *http.Request) {
	var in CommercialPromotionCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || !commercialCode.MatchString(in.Code) || in.Definition.Validate() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid code, scoped promotion policy and reason required."))
		return
	}
	h.execute(w, r, "commercial:promotion:create", in, "promotions:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "promotions:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		for _, id := range in.Definition.RatePlanIDs {
			if e = h.validateProductRate(ctx, tx, t.Organisation, in.PropertyID, id); e != nil {
				return 0, nil, e
			}
		}
		id := ids.New()
		raw, _ := json.Marshal(in.Definition)
		if _, e = tx.Exec(ctx, `INSERT INTO promotions(id,organisation_id,property_id,code,definition,created_by) VALUES($1,$2,$3,$4,$5,$6)`, id, t.Organisation, in.PropertyID, in.Code, raw, t.Account); e != nil {
			return 0, nil, e
		}
		value, e := typedJSON[revenue.Promotion](ctx, tx, `SELECT to_jsonb(p) FROM promotions p WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "promotion", "PROMOTION_CREATED", businessEvents.RatePlanChanged, 1, value)
		return 201, value, e
	})
}
func (h *Handler) addOnCreate(w http.ResponseWriter, r *http.Request) {
	var in CommercialAddOnCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || !commercialName(in.Name) || in.Definition.Validate() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid scoped add-on definition, execution reference and reason required."))
		return
	}
	h.execute(w, r, "commercial:addon:create", in, "promotions:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "promotions:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		raw, _ := json.Marshal(in.Definition)
		if _, e = tx.Exec(ctx, `INSERT INTO add_ons(id,organisation_id,property_id,name,definition,created_by) VALUES($1,$2,$3,$4,$5,$6)`, id, t.Organisation, in.PropertyID, in.Name, raw, t.Account); e != nil {
			return 0, nil, e
		}
		value, e := typedJSON[revenue.AddOn](ctx, tx, `SELECT to_jsonb(p) FROM add_ons p WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "add_on", "ADD_ON_CREATED", businessEvents.RatePlanChanged, 1, value)
		return 201, value, e
	})
}
func (h *Handler) packageCreate(w http.ResponseWriter, r *http.Request) {
	var in CommercialPackageCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.ProductID.ValidateV7() != nil || !commercialName(in.Name) || in.Definition.Validate() != nil || len(in.Components) > 20 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid product-backed package, bounded components and reason required."))
		return
	}
	h.execute(w, r, "commercial:package:create", in, "promotions:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "promotions:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		var valid bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sellable_products WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND state<>'RETIRED')`, t.Organisation, in.PropertyID, in.ProductID).Scan(&valid); e != nil {
			return 0, nil, e
		}
		if !valid {
			return 0, nil, apperrors.PermissionDenied("Package inventory must use this property's existing commercial product.")
		}
		seen := map[ids.ID]bool{}
		for _, component := range in.Components {
			if component.AddOnID.ValidateV7() != nil || seen[component.AddOnID] || component.Quantity < 1 || component.Quantity > 100 {
				return 0, nil, apperrors.InvalidRequest("Package component IDs and quantities must be unique and bounded.")
			}
			seen[component.AddOnID] = true
			addon, e := typedJSON[revenue.AddOn](ctx, tx, `SELECT to_jsonb(a) FROM add_ons a WHERE organisation_id=$1 AND id=$2`, t.Organisation, component.AddOnID)
			if e != nil {
				return 0, nil, e
			}
			if addon.PropertyID != in.PropertyID || addon.Definition.Currency != in.Definition.Currency {
				return 0, nil, apperrors.PermissionDenied("Package components must be same-property, same-currency references.")
			}
		}
		id := ids.New()
		raw, _ := json.Marshal(in.Definition)
		if _, e = tx.Exec(ctx, `INSERT INTO packages(id,organisation_id,property_id,product_id,name,definition,created_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, t.Organisation, in.PropertyID, in.ProductID, in.Name, raw, t.Account); e != nil {
			return 0, nil, e
		}
		for _, component := range in.Components {
			if _, e = tx.Exec(ctx, `INSERT INTO package_components(organisation_id,package_id,add_on_id,quantity) VALUES($1,$2,$3,$4)`, t.Organisation, id, component.AddOnID, component.Quantity); e != nil {
				return 0, nil, e
			}
		}
		value, e := typedJSON[revenue.Package](ctx, tx, `SELECT to_jsonb(p) FROM packages p WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "package", "PACKAGE_CREATED", businessEvents.RatePlanChanged, 1, map[string]any{"package": value, "components": in.Components})
		return 201, map[string]any{"package": value, "components": in.Components}, e
	})
}
func (h *Handler) promotionPatch(w http.ResponseWriter, r *http.Request) {
	h.offerPatch(w, r, "promotions")
}
func (h *Handler) addOnPatch(w http.ResponseWriter, r *http.Request) { h.offerPatch(w, r, "add_ons") }
func (h *Handler) packagePatch(w http.ResponseWriter, r *http.Request) {
	h.offerPatch(w, r, "packages")
}
func (h *Handler) offerPatch(w http.ResponseWriter, r *http.Request, table string) {
	if table != "promotions" && table != "add_ons" && table != "packages" {
		reply(w, r, 0, nil, apperrors.Unavailable("Unknown commercial definition."))
		return
	}
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
	var property ids.ID
	e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT property_id FROM `+table+` WHERE organisation_id=$1 AND id=$2`, t.Organisation, id).Scan(&property)
	if errors.Is(e, pgx.ErrNoRows) {
		e = apperrors.NotFound("OFFER_NOT_FOUND", "Commercial offer not found.")
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) || in.Revalidate || in.Name != nil && (!commercialName(*in.Name) || table == "promotions") {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, permitted metadata, lifecycle and reason required."))
		return
	}
	h.execute(w, r, "commercial:offer:update:"+id.String(), in, "promotions:write", "PROPERTY", property, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "promotions:write", "PROPERTY", property, true)
		if e != nil {
			return 0, nil, e
		}
		var state string
		var version int64
		var definition []byte
		if e = tx.QueryRow(ctx, `SELECT state,version,definition FROM `+table+` WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, id).Scan(&state, &version, &definition); e != nil {
			return 0, nil, e
		}
		if version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Offer version changed.")
		}
		if state == "ARCHIVED" || in.State == "RETIRED" || in.State != "" && !commercialTransition(state, in.State) {
			return 0, nil, apperrors.InvalidRequest("Invalid commercial-offer transition.")
		}
		var consumed any
		if in.State == "ACTIVE" {
			if e = h.commercialGate(ctx, tx, t, property); e != nil {
				return 0, nil, e
			}
			var exposure int64
			var currency string
			switch table {
			case "promotions":
				var d revenue.PromotionDefinition
				if e = json.Unmarshal(definition, &d); e != nil {
					return 0, nil, e
				}
				if d.Validate() != nil {
					return 0, nil, apperrors.InvalidRequest("Invalid promotion policy.")
				}
				exposure = d.MaxDiscountMinor
				currency = d.Currency
			case "packages":
				var d revenue.PackageDefinition
				if e = json.Unmarshal(definition, &d); e != nil {
					return 0, nil, e
				}
				if d.Validate() != nil {
					return 0, nil, apperrors.InvalidRequest("Invalid package policy.")
				}
				exposure = d.MaxDiscountMinor
				currency = d.Currency
			case "add_ons":
				var d revenue.AddOnDefinition
				if e = json.Unmarshal(definition, &d); e != nil {
					return 0, nil, e
				}
				if d.Validate() != nil {
					return 0, nil, apperrors.InvalidRequest("Invalid add-on policy.")
				}
				cost, e := commercialMoney(d.AmountMinor, d.Currency)
				if e != nil {
					return 0, nil, e
				}
				cost, e = cost.MulInt(int64(d.MaxQuantity))
				if e != nil {
					return 0, nil, e
				}
				exposure = cost.AmountMinor
				currency = d.Currency
			}
			command := in
			command.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_OFFER_ACTIVATION", target.Scope, in.ApprovalID, map[string]any{"offer_id": id, "offer_type": table, "command": command}, "", &exposure, currency); e != nil {
				return 0, nil, e
			}
			consumed = in.ApprovalID
		} else if !in.ApprovalID.IsZero() {
			return 0, nil, apperrors.InvalidRequest("An approval reference is accepted only for an executed activation.")
		}
		if table == "promotions" {
			_, e = tx.Exec(ctx, `UPDATE promotions SET state=coalesce(nullif($3,''),state),approval_id=coalesce($4,approval_id),version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.State, consumed)
		} else {
			_, e = tx.Exec(ctx, `UPDATE `+table+` SET state=coalesce(nullif($3,''),state),name=coalesce($4,name),approval_id=coalesce($5,approval_id),version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.State, in.Name, consumed)
		}
		if e != nil {
			return 0, nil, e
		}
		value, e := typedJSON[json.RawMessage](ctx, tx, `SELECT to_jsonb(p)-'created_by' FROM `+table+` p WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, property, id, table, "OFFER_UPDATED", businessEvents.RatePlanChanged, version+1, map[string]any{"offer": value, "reason": in.Reason})
		return 200, value, e
	})
}
func nullableCommercialID(id ids.ID) any {
	if id.IsZero() {
		return nil
	}
	return id
}
