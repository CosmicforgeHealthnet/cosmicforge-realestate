package revenue

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/jackc/pgx/v5"
)

type Plan struct {
	ID             ids.ID    `json:"id"`
	OrganisationID ids.ID    `json:"organisation_id"`
	PropertyID     ids.ID    `json:"property_id"`
	Name           string    `json:"name"`
	Kind           string    `json:"kind"`
	State          string    `json:"state"`
	Version        int64     `json:"version"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type RateVersion struct {
	ID             ids.ID         `json:"id"`
	OrganisationID ids.ID         `json:"organisation_id"`
	PropertyID     ids.ID         `json:"property_id"`
	RatePlanID     ids.ID         `json:"rate_plan_id"`
	Version        int64          `json:"version"`
	Currency       string         `json:"currency"`
	AmountMinor    int64          `json:"amount_minor"`
	Definition     RateDefinition `json:"definition"`
	EffectiveFrom  string         `json:"effective_from"`
	EffectiveTo    string         `json:"effective_to"`
	ApprovalID     *ids.ID        `json:"approval_id,omitempty"`
	Reason         string         `json:"reason"`
	CreatedBy      ids.ID         `json:"created_by"`
	CreatedAt      time.Time      `json:"created_at"`
}

func GetPlan(ctx context.Context, q database.Querier, org, id ids.ID) (Plan, error) {
	var p Plan
	var raw []byte
	err := q.QueryRow(ctx, `SELECT to_jsonb(p) FROM rate_plans p WHERE organisation_id=$1 AND id=$2`, org, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, apperrors.NotFound("RATE_PLAN_NOT_FOUND", "Rate plan not found.")
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(raw, &p)
	return p, err
}
func GetVersion(ctx context.Context, q database.Querier, org, id ids.ID) (RateVersion, error) {
	var v RateVersion
	var raw []byte
	err := q.QueryRow(ctx, `SELECT to_jsonb(v) FROM rate_plan_versions v WHERE organisation_id=$1 AND id=$2`, org, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, apperrors.NotFound("RATE_VERSION_NOT_FOUND", "Rate version not found.")
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(raw, &v)
	if err == nil && (v.Currency != v.Definition.Currency || v.AmountMinor != v.Definition.AmountMinor || v.Definition.Validate() != nil) {
		err = apperrors.Conflict("RATE_POLICY_INVALID", "Stored rate policy is inconsistent.")
	}
	return v, err
}
func VersionForDay(ctx context.Context, q database.Querier, org, plan ids.ID, day string) (RateVersion, error) {
	var id ids.ID
	err := q.QueryRow(ctx, `SELECT id FROM rate_plan_versions WHERE organisation_id=$1 AND rate_plan_id=$2 AND effective_from<=$3::date AND effective_to>$3::date ORDER BY version DESC LIMIT 1`, org, plan, day).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return RateVersion{}, apperrors.Conflict("RATE_DATE_UNCOVERED", "No effective canonical rate covers a requested civil date.")
	}
	if err != nil {
		return RateVersion{}, err
	}
	return GetVersion(ctx, q, org, id)
}

// ResolveBase follows immutable parent-version references. Plan-level repetition
// is forbidden even when version IDs differ; chain depth is bounded to eight.
func ResolveBase(ctx context.Context, q database.Querier, org ids.ID, v RateVersion, day string) (money.Money, []ids.ID, error) {
	return resolveBase(ctx, q, org, v, day, map[ids.ID]bool{}, 0)
}
func resolveBase(ctx context.Context, q database.Querier, org ids.ID, v RateVersion, day string, seen map[ids.ID]bool, depth int) (money.Money, []ids.ID, error) {
	if depth >= 8 || seen[v.RatePlanID] || day < v.EffectiveFrom || day >= v.EffectiveTo {
		return money.Money{}, nil, apperrors.Conflict("DERIVED_RATE_CONFLICT", "Circular, excessive or ineffective derived-rate relationship.")
	}
	seen[v.RatePlanID] = true
	plan, err := GetPlan(ctx, q, org, v.RatePlanID)
	if err != nil {
		return money.Money{}, nil, err
	}
	if plan.State != "ACTIVE" {
		return money.Money{}, nil, apperrors.Conflict("RATE_INACTIVE", "Every applied rate plan must be active.")
	}
	amount, err := money.New(v.AmountMinor, v.Currency)
	if err != nil {
		return amount, nil, err
	}
	versions := []ids.ID{v.ID}
	if v.Definition.ParentVersionID != nil {
		parent, e := GetVersion(ctx, q, org, *v.Definition.ParentVersionID)
		if e != nil {
			return amount, nil, e
		}
		if parent.PropertyID != v.PropertyID || parent.Currency != v.Currency {
			return amount, nil, apperrors.Conflict("DERIVED_RATE_CURRENCY_OR_SCOPE", "Derived rates cannot cross properties or convert currency.")
		}
		amount, versions, e = resolveBase(ctx, q, org, parent, day, seen, depth+1)
		if e != nil {
			return amount, nil, e
		}
		versions = append(versions, v.ID)
	}
	amount, err = Adjust(amount, v.Definition.Adjustments)
	return amount, versions, err
}
func ValidateParent(ctx context.Context, q database.Querier, org, property, childPlan ids.ID, definition RateDefinition) error {
	if definition.ParentVersionID == nil {
		return nil
	}
	seen := map[ids.ID]bool{childPlan: true}
	id := *definition.ParentVersionID
	for depth := 0; depth < 8; depth++ {
		v, e := GetVersion(ctx, q, org, id)
		if e != nil {
			return e
		}
		if seen[v.RatePlanID] || v.PropertyID != property || v.Currency != definition.Currency {
			return apperrors.Conflict("DERIVED_RATE_CONFLICT", "Parent chain must be acyclic, same-property and same-currency.")
		}
		seen[v.RatePlanID] = true
		if v.Definition.ParentVersionID == nil {
			return nil
		}
		id = *v.Definition.ParentVersionID
	}
	return apperrors.InvalidRequest("Derived-rate chains are limited to eight plans.")
}

type RestrictionRecord struct {
	ID             ids.ID      `json:"id"`
	OrganisationID ids.ID      `json:"organisation_id"`
	PropertyID     ids.ID      `json:"property_id"`
	PoolID         *ids.ID     `json:"pool_id,omitempty"`
	ProductID      *ids.ID     `json:"product_id,omitempty"`
	RatePlanID     *ids.ID     `json:"rate_plan_id,omitempty"`
	Channel        string      `json:"channel"`
	DateFrom       string      `json:"date_from"`
	DateTo         string      `json:"date_to"`
	Definition     Restriction `json:"definition"`
	State          string      `json:"state"`
	Version        int64       `json:"version"`
}

func Restrictions(ctx context.Context, q database.Querier, p inventory.Product, plan ids.ID, r inventory.DateRange, channel string) ([]RestrictionRecord, error) {
	// A derived plan may specialize its commercial terms but cannot escape a
	// live hard restriction on any immutable parent from which its price derives.
	plans := []ids.ID{plan}
	seen := map[ids.ID]bool{plan: true}
	versions := map[ids.ID]bool{}
	days, err := r.Days("UTC")
	if err != nil {
		return nil, err
	}
	for _, day := range days {
		v, e := VersionForDay(ctx, q, p.OrganisationID, plan, day)
		if e != nil {
			var app *apperrors.Error
			if errors.As(e, &app) && app.Code == "RATE_DATE_UNCOVERED" {
				continue
			}
			return nil, e
		}
		for depth := 0; v.Definition.ParentVersionID != nil; depth++ {
			if depth >= 8 {
				return nil, apperrors.Conflict("DERIVED_RATE_CONFLICT", "Parent restriction chain is too deep.")
			}
			id := *v.Definition.ParentVersionID
			if versions[id] {
				break
			}
			versions[id] = true
			parent, e := GetVersion(ctx, q, p.OrganisationID, id)
			if e != nil {
				return nil, e
			}
			if parent.PropertyID != p.PropertyID {
				return nil, apperrors.PermissionDenied("Parent restriction scope cannot cross properties.")
			}
			if !seen[parent.RatePlanID] {
				plans = append(plans, parent.RatePlanID)
				seen[parent.RatePlanID] = true
			}
			v = parent
		}
	}
	rows, err := q.Query(ctx, `SELECT to_jsonb(r) FROM rate_restrictions r WHERE organisation_id=$1 AND property_id=$2 AND state='ACTIVE' AND date_from<=$4::date AND date_to>$3::date AND (pool_id IS NULL OR pool_id=$5) AND (product_id IS NULL OR product_id=$6) AND (rate_plan_id IS NULL OR rate_plan_id=ANY($7::uuid[])) AND (channel='' OR channel=$8) ORDER BY id`, p.OrganisationID, p.PropertyID, r.DateFrom, r.DateTo, p.PoolID, p.ID, plans, channel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RestrictionRecord{}
	for rows.Next() {
		var raw []byte
		var record RestrictionRecord
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

// CheckRestrictions intersects all hard constraints. CTA is evaluated on arrival,
// CTD on the exclusive departure day, and stop-sell on each occupied civil date.
func CheckRestrictions(records []RestrictionRecord, r inventory.DateRange, timezone string, adults, children int, now time.Time) error {
	days, err := r.Days(timezone)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return err
	}
	arrival, _ := time.ParseInLocation(time.DateOnly, r.DateFrom, loc)
	localNow := now.In(loc)
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.UTC)
	civilArrival := time.Date(arrival.Year(), arrival.Month(), arrival.Day(), 0, 0, 0, 0, time.UTC)
	advance := int(civilArrival.Sub(today) / (24 * time.Hour))
	if advance < 0 {
		return apperrors.Conflict("ARRIVAL_IN_PAST", "Arrival must not precede the property's current civil date.")
	}
	for _, day := range append(append([]string{}, days...), r.DateTo) {
		date, _ := time.Parse(time.DateOnly, day)
		rules := []Restriction{}
		for _, record := range records {
			d := record.Definition
			if day < record.DateFrom || day >= record.DateTo || len(d.Weekdays) > 0 && !containsWeekday(d.Weekdays, int(date.Weekday())) {
				continue
			}
			if d.StopSell && day != r.DateTo || d.ClosedToArrival && day == r.DateFrom || d.ClosedToDeparture && day == r.DateTo {
				return apperrors.Conflict("SALE_CLOSED", "A hard stop-sell or arrival/departure closure applies.")
			}
			rules = append(rules, d)
		}
		combined, e := Intersect(rules)
		if e != nil {
			return apperrors.Conflict("RESTRICTION_CONFLICT", "Applicable restrictions are contradictory.")
		}
		if len(days) < combined.MinNights || combined.MaxNights > 0 && len(days) > combined.MaxNights || advance < combined.MinAdvanceDays || combined.MaxAdvanceDays > 0 && advance > combined.MaxAdvanceDays || adults+children < combined.MinGuests || combined.MaxGuests > 0 && adults+children > combined.MaxGuests {
			return apperrors.Conflict("STAY_RESTRICTION", "Length, occupancy or advance-booking restrictions deny this stay.")
		}
		if advance == 0 && combined.CutoffLocal != "" && localNow.Format("15:04") >= combined.CutoffLocal {
			return apperrors.Conflict("BOOKING_CUTOFF", "The property-local booking cutoff has passed.")
		}
	}
	return nil
}
func containsWeekday(days []int, day int) bool {
	for _, d := range days {
		if d == day {
			return true
		}
	}
	return false
}

type QuoteNight struct {
	CalendarVersion       int64    `json:"calendar_version"`
	TaxProfileID          ids.ID   `json:"tax_profile_id"`
	CancellationReference string   `json:"cancellation_reference"`
	PrepaymentReference   string   `json:"prepayment_reference"`
	Day                   string   `json:"day"`
	RateVersionID         ids.ID   `json:"rate_version_id"`
	ParentVersionIDs      []ids.ID `json:"parent_version_ids"`
	AmountMinor           int64    `json:"amount_minor"`
}
type QuoteSnapshot struct {
	QuoteID               ids.ID              `json:"quote_id"`
	ProductID             ids.ID              `json:"product_id"`
	PropertyID            ids.ID              `json:"property_id"`
	PoolID                ids.ID              `json:"pool_id"`
	ProductVersion        int64               `json:"product_version"`
	PoolVersion           int64               `json:"pool_version"`
	RatePlanID            ids.ID              `json:"rate_plan_id"`
	RatePlanVersion       int64               `json:"rate_plan_version"`
	DateFrom              string              `json:"date_from"`
	DateTo                string              `json:"date_to"`
	Adults                int                 `json:"adults"`
	Children              int                 `json:"children"`
	Quantity              int                 `json:"quantity"`
	Channel               string              `json:"channel"`
	Nights                []QuoteNight        `json:"nights"`
	BaseAmountMinor       int64               `json:"base_amount_minor"`
	DiscountMinor         int64               `json:"discount_minor"`
	AddOnsMinor           int64               `json:"add_ons_minor"`
	Components            []ComponentAmount   `json:"components"`
	AmountMinor           int64               `json:"amount_minor"`
	Currency              string              `json:"currency"`
	CurrencyPrecision     int                 `json:"currency_precision"`
	Rounding              string              `json:"rounding"`
	Restrictions          []RestrictionRecord `json:"restrictions"`
	CancellationReference string              `json:"cancellation_reference"`
	PrepaymentReference   string              `json:"prepayment_reference"`
	PolicyCode            string              `json:"policy_code"`
	PolicyVersion         int64               `json:"policy_version"`
	TaxProfileID          *ids.ID             `json:"tax_profile_id,omitempty"`
	TaxProfileIDs         []ids.ID            `json:"tax_profile_ids"`
	PromotionVersions     map[string]int64    `json:"promotion_versions"`
	AddOnVersions         map[string]int64    `json:"add_on_versions"`
	PackageID             *ids.ID             `json:"package_id,omitempty"`
	PackageVersion        *int64              `json:"package_version,omitempty"`
	ExpiresAt             time.Time           `json:"expires_at"`
	CalculatedAt          time.Time           `json:"calculated_at"`
	IsReservation         bool                `json:"is_reservation"`
}
