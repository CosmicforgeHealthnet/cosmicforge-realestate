package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/jackc/pgx/v5"
)

// Prices are offers with bounded lifetimes, never reservation acknowledgements.
// Fees apply per quote/stay, or per civil night as their approved policy states.
type CommercialQuoteCommand struct {
	ProductID    ids.ID                       `json:"product_id"`
	DateFrom     string                       `json:"date_from"`
	DateTo       string                       `json:"date_to"`
	Adults       int                          `json:"adults"`
	Children     int                          `json:"children"`
	Quantity     int                          `json:"quantity"`
	Channel      string                       `json:"channel"`
	PromotionIDs []ids.ID                     `json:"promotion_ids,omitempty"`
	AddOns       []CommercialPackageComponent `json:"add_ons,omitempty"`
	PackageID    *ids.ID                      `json:"package_id,omitempty"`
}

func (h *Handler) quoteCreate(w http.ResponseWriter, r *http.Request) {
	var in CommercialQuoteCommand
	if !decode(w, r, &in) {
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	p, e := inventory.GetProduct(r.Context(), h.Auth.Service.Pool, t.Organisation, in.ProductID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "commercial:quote:create", in, "pricing:read", "PROPERTY", p.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pricing:read", "PROPERTY", p.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		quote, e := h.calculateQuote(ctx, tx, t, in, true)
		if e != nil {
			return 0, nil, e
		}
		raw, e := json.Marshal(quote)
		if e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO price_quotes(id,organisation_id,property_id,product_id,rate_version_id,date_from,date_to,amount_minor,currency,expires_at,snapshot,created_by) VALUES($1,$2,$3,$4,$5,$6::date,$7::date,$8,$9,$10,$11,$12)`, quote.QuoteID, t.Organisation, quote.PropertyID, quote.ProductID, quote.Nights[0].RateVersionID, quote.DateFrom, quote.DateTo, quote.AmountMinor, quote.Currency, quote.ExpiresAt, raw, t.Account); e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, quote.PropertyID, quote.QuoteID, "price_quote", "PRICE_QUOTED", "", 1, map[string]any{"quote_id": quote.QuoteID, "product_id": quote.ProductID, "amount_minor": quote.AmountMinor, "currency": quote.Currency, "expires_at": quote.ExpiresAt})
		return 201, quote, e
	})
}
func (h *Handler) quoteGet(w http.ResponseWriter, r *http.Request) {
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
	q, e := loadCommercialQuote(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e == nil {
		_, e = h.commercialScope(r, "pricing:read", q.PropertyID, false)
	}
	reply(w, r, 200, map[string]any{"quote": q, "expired": !h.Auth.Service.Now().Before(q.ExpiresAt)}, e)
}

func loadCommercialQuote(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, org, id ids.ID) (revenue.QuoteSnapshot, error) {
	var raw []byte
	var snapshot revenue.QuoteSnapshot
	e := q.QueryRow(ctx, `SELECT snapshot FROM price_quotes WHERE organisation_id=$1 AND id=$2`, org, id).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return snapshot, apperrors.NotFound("QUOTE_NOT_FOUND", "Price quote not found.")
	}
	if e != nil {
		return snapshot, e
	}
	e = json.Unmarshal(raw, &snapshot)
	return snapshot, e
}

func (h *Handler) commercialProductEligibility(ctx context.Context, tx pgx.Tx, t tenant, p inventory.Product, adults, children int, channel string) error {
	if p.State != "ACTIVE" || adults < 1 || children < 0 || adults > 100000 || children > 100000 || adults+children < p.MinGuests || adults+children > p.MaxGuests || p.RatePlanID == nil {
		return apperrors.Conflict("PRODUCT_INELIGIBLE", "Active priced product and permitted occupancy required.")
	}
	node, e := portfolio.Get(ctx, tx, t.Organisation, p.NodeID)
	if e != nil {
		return e
	}
	if node.NonSellable || node.State != "ACTIVE" || node.Attributes.Capacity == nil || adults+children > *node.Attributes.Capacity || p.MaxGuests > *node.Attributes.Capacity {
		return apperrors.Conflict("PHYSICAL_OCCUPANCY_CHANGED", "Product occupancy must be reviewed against current canonical physical capacity.")
	}
	if e = h.commercialGate(ctx, tx, t, p.PropertyID); e != nil {
		return e
	}
	return h.commercialChannel(ctx, tx, t, p, channel)
}

func (h *Handler) calculateQuote(ctx context.Context, tx pgx.Tx, t tenant, in CommercialQuoteCommand, checkStock bool) (revenue.QuoteSnapshot, error) {
	var out revenue.QuoteSnapshot
	if in.ProductID.ValidateV7() != nil || in.Quantity < 1 || in.Quantity > 100000 || len(in.PromotionIDs) > 20 || len(in.AddOns) > 20 || !inventory.ValidChannel(in.Channel) {
		return out, apperrors.InvalidRequest("Valid product, quantity, channel and bounded offer selections required.")
	}
	p, e := inventory.GetProduct(ctx, tx, t.Organisation, in.ProductID)
	if e != nil {
		return out, e
	}
	if e = h.commercialProductEligibility(ctx, tx, t, p, in.Adults, in.Children, in.Channel); e != nil {
		return out, e
	}
	property, e := portfolio.Get(ctx, tx, t.Organisation, p.PropertyID)
	if e != nil {
		return out, e
	}
	dates := inventory.DateRange{DateFrom: in.DateFrom, DateTo: in.DateTo}
	days, e := dates.Days(property.Timezone)
	if e != nil {
		return out, apperrors.InvalidRequest("Valid property-local checkout-exclusive dates required.")
	}
	if e = h.channelSaleControls(ctx, tx, p, in.Channel, len(days)); e != nil {
		return out, e
	}
	pool, e := inventory.GetPool(ctx, tx, t.Organisation, p.PoolID)
	if e != nil {
		return out, e
	}
	if e = inventory.CheckStockAuthority(ctx, tx, pool); e != nil {
		return out, e
	}
	stock, e := inventory.Calendar(ctx, tx, t.Organisation, pool.ID, dates, property.Timezone, in.Channel, h.Auth.Service.Now().UTC(), false)
	if e != nil {
		return out, e
	}
	plan, e := revenue.GetPlan(ctx, tx, t.Organisation, *p.RatePlanID)
	if e != nil {
		return out, e
	}
	if plan.State != "ACTIVE" {
		return out, apperrors.Conflict("RATE_INACTIVE", "Rate plan must be active.")
	}
	records, e := revenue.Restrictions(ctx, tx, p, plan.ID, dates, in.Channel)
	if e != nil {
		return out, e
	}
	now := h.Auth.Service.Now().UTC()
	if e = revenue.CheckRestrictions(records, dates, property.Timezone, in.Adults, in.Children, now); e != nil {
		return out, e
	}
	out = revenue.QuoteSnapshot{QuoteID: ids.New(), ProductID: p.ID, PropertyID: p.PropertyID, PoolID: pool.ID, ProductVersion: p.Version, PoolVersion: pool.Version, RatePlanID: plan.ID, RatePlanVersion: plan.Version, DateFrom: in.DateFrom, DateTo: in.DateTo, Adults: in.Adults, Children: in.Children, Quantity: in.Quantity, Channel: in.Channel, Restrictions: records, PolicyCode: p.PolicyCode, PolicyVersion: p.PolicyVersion, PromotionVersions: map[string]int64{}, AddOnVersions: map[string]int64{}, ExpiresAt: now.Add(5 * time.Minute), CalculatedAt: now, Rounding: "HALF_AWAY_FROM_ZERO", Nights: []revenue.QuoteNight{}, TaxProfileIDs: []ids.ID{}, Components: []revenue.ComponentAmount{}}
	amounts := []money.Money{}
	taxes := []CommercialTaxProfile{}
	included := map[ids.ID]bool{}
	for i, day := range days {
		if checkStock && stock[i].Available < in.Quantity {
			return out, apperrors.Conflict("INSUFFICIENT_INVENTORY", "No complete available stock range satisfies this quote.")
		}
		v, e := revenue.VersionForDay(ctx, tx, t.Organisation, plan.ID, day)
		if e != nil {
			return out, e
		}
		base, parents, e := revenue.ResolveBase(ctx, tx, t.Organisation, v, day)
		if e != nil {
			return out, e
		}
		night, e := revenue.Nightly(v.Definition, base, day, in.Channel, len(days), in.Adults, in.Children)
		if e != nil {
			return out, apperrors.Conflict("RATE_INELIGIBLE", "Rate rules deny this occupancy, stay or price range.")
		}
		night, e = night.MulInt(int64(in.Quantity))
		if e != nil {
			return out, e
		}
		if out.Currency != "" && out.Currency != v.Currency {
			return out, apperrors.Conflict("QUOTE_CURRENCY_CONFLICT", "A stay cannot silently convert currencies.")
		}
		out.Currency = v.Currency
		out.CurrencyPrecision = night.Currency.MinorUnits()
		if v.Definition.TaxProfileID == nil {
			return out, apperrors.Conflict("FISCAL_POLICY_REQUIRED", "An approved fiscal profile is required, including an explicit zero-tax policy where applicable.")
		}
		fiscal, e := typedJSON[CommercialTaxProfile](ctx, tx, `SELECT to_jsonb(t) FROM tax_profiles t WHERE organisation_id=$1 AND id=$2`, t.Organisation, *v.Definition.TaxProfileID)
		if e != nil {
			return out, e
		}
		if fiscal.PropertyID != p.PropertyID || fiscal.Currency != out.Currency || fiscal.ApprovalID == nil || fiscal.Definition.Validate() != nil || now.Before(fiscal.EffectiveAt) || fiscal.ExpiresAt != nil && !now.Before(*fiscal.ExpiresAt) {
			return out, apperrors.Conflict("FISCAL_POLICY_INELIGIBLE", "Current approved same-property fiscal policy required.")
		}
		if fiscal.ExpiresAt != nil && fiscal.ExpiresAt.Before(out.ExpiresAt) {
			out.ExpiresAt = *fiscal.ExpiresAt
		}
		if !slices.Contains(out.TaxProfileIDs, fiscal.ID) {
			out.TaxProfileIDs = append(out.TaxProfileIDs, fiscal.ID)
		}
		taxes = append(taxes, fiscal)
		amounts = append(amounts, night)
		for _, id := range v.Definition.IncludedAddOns {
			included[id] = true
		}
		out.Nights = append(out.Nights, revenue.QuoteNight{Day: day, CalendarVersion: stock[i].Version, RateVersionID: v.ID, ParentVersionIDs: parents, AmountMinor: night.AmountMinor, TaxProfileID: fiscal.ID, CancellationReference: v.Definition.CancellationReference, PrepaymentReference: v.Definition.PrepaymentReference})
		if i == 0 {
			out.CancellationReference = v.Definition.CancellationReference
			out.PrepaymentReference = v.Definition.PrepaymentReference
			out.TaxProfileID = v.Definition.TaxProfileID
		}
	}
	zero, e := money.New(0, out.Currency)
	if e != nil {
		return out, e
	}
	subtotal := zero
	for _, v := range amounts {
		subtotal, e = subtotal.Add(v)
		if e != nil {
			return out, e
		}
	}
	out.BaseAmountMinor = subtotal.AmountMinor
	addons := append([]CommercialPackageComponent{}, in.AddOns...)
	var pack *revenue.Package
	if in.PackageID != nil {
		value, e := typedJSON[revenue.Package](ctx, tx, `SELECT to_jsonb(p) FROM packages p WHERE organisation_id=$1 AND id=$2`, t.Organisation, *in.PackageID)
		if e != nil {
			return out, e
		}
		d := value.Definition
		if value.State != "ACTIVE" || value.ApprovalID == nil || value.ProductID != p.ID || value.PropertyID != p.PropertyID || d.Validate() != nil || d.Currency != out.Currency || len(days) < d.MinNights || len(days) > d.MaxNights || len(d.Channels) > 0 && !slices.Contains(d.Channels, in.Channel) || len(in.PromotionIDs) > 0 && !d.AllowPromotions {
			return out, apperrors.Conflict("PACKAGE_INELIGIBLE", "Package scope, currency, stay or stacking policy denies this quote.")
		}
		pack = &value
		out.PackageID = &value.ID
		out.PackageVersion = &value.Version
		rows, e := tx.Query(ctx, `SELECT add_on_id,quantity FROM package_components WHERE organisation_id=$1 AND package_id=$2 ORDER BY add_on_id`, t.Organisation, value.ID)
		if e != nil {
			return out, e
		}
		for rows.Next() {
			var c CommercialPackageComponent
			if e = rows.Scan(&c.AddOnID, &c.Quantity); e != nil {
				break
			}
			addons = append(addons, c)
		}
		rowErr := rows.Err()
		rows.Close()
		if e == nil {
			e = rowErr
		}
		if e != nil {
			return out, e
		}
	}
	promos := []revenue.Promotion{}
	seen := map[ids.ID]bool{}
	priorities := map[int]bool{}
	for _, id := range in.PromotionIDs {
		if id.ValidateV7() != nil || seen[id] {
			return out, apperrors.InvalidRequest("Promotion IDs must be unique UUIDv7 values.")
		}
		seen[id] = true
		v, e := typedJSON[revenue.Promotion](ctx, tx, `SELECT to_jsonb(p) FROM promotions p WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return out, e
		}
		d := v.Definition
		if v.State != "ACTIVE" || v.ApprovalID == nil || v.PropertyID != p.PropertyID || d.Validate() != nil || d.Currency != out.Currency || now.Before(d.EffectiveAt) || !now.Before(d.ExpiresAt) || in.DateFrom < d.DateFrom || in.DateTo > d.DateTo || len(days) < d.MinNights || len(days) > d.MaxNights || len(d.Channels) > 0 && !slices.Contains(d.Channels, in.Channel) || len(d.RatePlanIDs) > 0 && !slices.Contains(d.RatePlanIDs, plan.ID) || len(in.PromotionIDs) > 1 && !d.Stackable || priorities[d.Priority] {
			return out, apperrors.Conflict("PROMOTION_INELIGIBLE", "Promotion windows, scope, stacking or unique priority policy denies this quote.")
		}
		priorities[d.Priority] = true
		promos = append(promos, v)
		if d.ExpiresAt.Before(out.ExpiresAt) {
			out.ExpiresAt = d.ExpiresAt
		}
	}
	sort.Slice(promos, func(i, j int) bool { return promos[i].Definition.Priority < promos[j].Definition.Priority })
	for _, v := range promos {
		d := v.Definition
		subtotal, e = boundedDiscount(subtotal, d.Discount, d.MaxDiscountMinor, d.MaxDiscountBPS)
		if e != nil {
			return out, e
		}
		out.PromotionVersions[v.ID.String()] = v.Version
	}
	if pack != nil {
		d := pack.Definition
		subtotal, e = boundedDiscount(subtotal, d.Discount, d.MaxDiscountMinor, d.MaxDiscountBPS)
		if e != nil {
			return out, e
		}
	}
	out.DiscountMinor = out.BaseAmountMinor - subtotal.AmountMinor
	// Allocate the final accommodation price proportionally; shared money assigns
	// residual minor units deterministically and exactly conserves the total.
	ratios := make([]int64, len(amounts))
	for i, v := range amounts {
		ratios[i] = v.AmountMinor
	}
	if out.BaseAmountMinor == 0 {
		for i := range ratios {
			ratios[i] = 1
		}
	}
	amounts, e = subtotal.Allocate(ratios...)
	if e != nil {
		return out, e
	}
	quantities := map[ids.ID]int{}
	for _, c := range addons {
		if c.AddOnID.ValidateV7() != nil || c.Quantity < 1 || c.Quantity > 100 {
			return out, apperrors.InvalidRequest("Valid bounded add-on quantities required.")
		}
		quantities[c.AddOnID] += c.Quantity
		if quantities[c.AddOnID] > 100 {
			return out, apperrors.InvalidRequest("Combined add-on quantity exceeds its bound.")
		}
	}
	for id := range included {
		if quantities[id] > 0 {
			return out, apperrors.Conflict("ADD_ON_ALREADY_INCLUDED", "A rate-included add-on cannot be charged a second time.")
		}
		quantities[id] = 0
	}
	addonIDs := make([]ids.ID, 0, len(quantities))
	for id := range quantities {
		addonIDs = append(addonIDs, id)
	}
	slices.SortFunc(addonIDs, func(a, b ids.ID) int {
		if a.String() < b.String() {
			return -1
		}
		if a == b {
			return 0
		}
		return 1
	})
	for _, id := range addonIDs {
		addon, e := typedJSON[revenue.AddOn](ctx, tx, `SELECT to_jsonb(a) FROM add_ons a WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return out, e
		}
		d := addon.Definition
		qty := quantities[id]
		var approved bool
		if e = tx.QueryRow(ctx, `SELECT approval_id IS NOT NULL FROM add_ons WHERE organisation_id=$1 AND id=$2`, t.Organisation, id).Scan(&approved); e != nil {
			return out, e
		}
		if addon.State != "ACTIVE" || !approved || addon.PropertyID != p.PropertyID || d.Validate() != nil || d.Currency != out.Currency || qty > d.MaxQuantity {
			return out, apperrors.Conflict("ADD_ON_INELIGIBLE", "Current approved same-property add-on and permitted currency/quantity required.")
		}
		out.AddOnVersions[id.String()] = addon.Version
		if qty == 0 {
			continue
		}
		cost, e := money.New(d.AmountMinor, out.Currency)
		if e != nil {
			return out, e
		}
		cost, e = cost.MulInt(int64(qty))
		if e != nil {
			return out, e
		}
		if d.PerNight {
			cost, e = cost.MulInt(int64(len(days)))
			if e != nil {
				return out, e
			}
		}
		out.AddOnsMinor, e = addMinor(out.AddOnsMinor, cost.AmountMinor, out.Currency)
		if e != nil {
			return out, e
		}
		equal := make([]int64, len(days))
		for i := range equal {
			equal[i] = 1
		}
		allocated, e := cost.Allocate(equal...)
		if e != nil {
			return out, e
		}
		for i := range amounts {
			amounts[i], e = amounts[i].Add(allocated[i])
			if e != nil {
				return out, e
			}
		}
	}
	// A per-stay fee is charged once. Changing its definition mid-stay has no
	// authoritative allocation policy, so refuse that ambiguity explicitly.
	var stayFees []revenue.FeeComponent
	for i, tax := range taxes {
		fees := []revenue.FeeComponent{}
		for _, fee := range tax.Definition.Fees {
			if !fee.PerNight {
				fees = append(fees, fee)
			}
		}
		if i == 0 {
			stayFees = fees
		} else if !reflect.DeepEqual(stayFees, fees) {
			return out, apperrors.Conflict("STAY_FEE_POLICY_CONFLICT", "Per-stay fees must have a consistent approved definition throughout the stay.")
		}
	}
	total := zero
	for i, amount := range amounts {
		policy := taxes[i].Definition
		fees := []revenue.FeeComponent{}
		for _, fee := range policy.Fees {
			if i == 0 || fee.PerNight {
				fees = append(fees, fee)
			}
		}
		policy.Fees = fees
		final, components, e := policy.Apply(amount, 1)
		if e != nil {
			return out, apperrors.Conflict("FISCAL_CALCULATION_INVALID", "Approved fiscal components cannot be applied to this exact price.")
		}
		total, e = total.Add(final)
		if e != nil {
			return out, e
		}
		for _, c := range components {
			c.Code = days[i] + ":" + c.Code
			out.Components = append(out.Components, c)
		}
	}
	out.AmountMinor = total.AmountMinor
	return out, nil
}
func addMinor(a, b int64, currency string) (int64, error) {
	m, e := money.New(a, currency)
	if e != nil {
		return 0, e
	}
	n, e := money.New(b, currency)
	if e != nil {
		return 0, e
	}
	m, e = m.Add(n)
	return m.AmountMinor, e
}
func boundedDiscount(base money.Money, a revenue.Adjustment, maxMinor, maxBPS int64) (money.Money, error) {
	next, e := revenue.Adjust(base, []revenue.Adjustment{a})
	if e != nil {
		return next, e
	}
	limit, e := revenue.Ratio(base.AmountMinor, maxBPS, 10000)
	if e != nil {
		return next, e
	}
	discount := base.AmountMinor - next.AmountMinor
	if discount < 0 || discount > limit || discount > maxMinor {
		return next, apperrors.Conflict("DISCOUNT_POLICY_LIMIT", "Discount exceeds its approved fixed or percentage limit.")
	}
	return next, nil
}

func (h *Handler) validateHoldQuote(ctx context.Context, tx pgx.Tx, t tenant, in inventory.HoldCommand) error {
	q, e := loadCommercialQuote(ctx, tx, t.Organisation, in.QuoteID)
	if e != nil {
		return e
	}
	if !h.Auth.Service.Now().Before(q.ExpiresAt) || q.ProductID != in.ProductID || q.DateFrom != in.DateFrom || q.DateTo != in.DateTo || q.Quantity != in.Quantity || q.Channel != in.Channel {
		return apperrors.Conflict("QUOTE_MISMATCH_OR_EXPIRED", "An unexpired matching immutable price quote is required.")
	}
	p, e := inventory.GetProduct(ctx, tx, t.Organisation, in.ProductID)
	if e != nil {
		return e
	}
	if p.Version != q.ProductVersion || p.PoolID != q.PoolID || p.RatePlanID == nil || *p.RatePlanID != q.RatePlanID {
		return apperrors.Conflict("QUOTE_VERSION_CHANGED", "Product price context changed; obtain a new quote.")
	}
	if e = h.commercialProductEligibility(ctx, tx, t, p, q.Adults, q.Children, q.Channel); e != nil {
		return e
	}
	pool, e := inventory.GetPool(ctx, tx, t.Organisation, p.PoolID)
	if e != nil {
		return e
	}
	if pool.Version != q.PoolVersion {
		return apperrors.Conflict("QUOTE_VERSION_CHANGED", "Physical pool configuration changed.")
	}
	if e = inventory.CheckStockAuthority(ctx, tx, pool); e != nil {
		return e
	}
	plan, e := revenue.GetPlan(ctx, tx, t.Organisation, q.RatePlanID)
	if e != nil {
		return e
	}
	if plan.State != "ACTIVE" || plan.Version != q.RatePlanVersion {
		return apperrors.Conflict("QUOTE_VERSION_CHANGED", "Canonical rate policy changed.")
	}
	property, e := portfolio.Get(ctx, tx, t.Organisation, p.PropertyID)
	if e != nil {
		return e
	}
	dates := inventory.DateRange{DateFrom: q.DateFrom, DateTo: q.DateTo}
	records, e := revenue.Restrictions(ctx, tx, p, plan.ID, dates, q.Channel)
	if e != nil {
		return e
	}
	if e = revenue.CheckRestrictions(records, dates, property.Timezone, q.Adults, q.Children, h.Auth.Service.Now().UTC()); e != nil {
		return e
	}
	if e = h.channelSaleControls(ctx, tx, p, q.Channel, len(q.Nights)); e != nil {
		return e
	}
	for _, night := range q.Nights {
		v, e := revenue.VersionForDay(ctx, tx, t.Organisation, plan.ID, night.Day)
		if e != nil {
			return e
		}
		if v.ID != night.RateVersionID {
			return apperrors.Conflict("QUOTE_VERSION_CHANGED", "An effective rate version changed.")
		}
		if _, _, e = revenue.ResolveBase(ctx, tx, t.Organisation, v, night.Day); e != nil {
			return e
		}
	}
	for _, id := range q.TaxProfileIDs {
		var eligible bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tax_profiles WHERE organisation_id=$1 AND id=$2 AND approval_id IS NOT NULL AND effective_at<=$3 AND (expires_at IS NULL OR expires_at>$3))`, t.Organisation, id, h.Auth.Service.Now().UTC()).Scan(&eligible)
		if e != nil {
			return e
		}
		if !eligible {
			return apperrors.Conflict("FISCAL_POLICY_EXPIRED", "Quoted fiscal policy is no longer effective.")
		}
	}
	for _, table := range []string{"promotions", "add_ons"} {
		versions := q.PromotionVersions
		if table == "add_ons" {
			versions = q.AddOnVersions
		}
		for raw, version := range versions {
			id, e := ids.ParseV7(raw)
			if e != nil {
				return e
			}
			var actual int64
			var state string
			var definition []byte
			if e = tx.QueryRow(ctx, `SELECT version,state,definition FROM `+table+` WHERE organisation_id=$1 AND id=$2`, t.Organisation, id).Scan(&actual, &state, &definition); e != nil {
				return e
			}
			if actual != version || state != "ACTIVE" {
				return apperrors.Conflict("QUOTE_OFFER_CHANGED", "Quoted offer version is no longer active.")
			}
			if table == "promotions" {
				var d revenue.PromotionDefinition
				if e = json.Unmarshal(definition, &d); e != nil {
					return e
				}
				now := h.Auth.Service.Now().UTC()
				if now.Before(d.EffectiveAt) || !now.Before(d.ExpiresAt) {
					return apperrors.Conflict("PROMOTION_EXPIRED", "Quoted promotion expired.")
				}
			}
		}
	}
	if q.PackageID != nil {
		var version int64
		var state string
		e = tx.QueryRow(ctx, `SELECT version,state FROM packages WHERE organisation_id=$1 AND id=$2`, t.Organisation, *q.PackageID).Scan(&version, &state)
		if e != nil {
			return e
		}
		if q.PackageVersion == nil || version != *q.PackageVersion || state != "ACTIVE" {
			return apperrors.Conflict("QUOTE_PACKAGE_CHANGED", "Quoted package is no longer active.")
		}
	}
	return nil
}
