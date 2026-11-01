package revenue

import (
	"math"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

func checkedAdd(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b || b < 0 && a < math.MinInt64-b {
		return 0, money.ErrOverflow
	}
	return a + b, nil
}

// PricingPolicy is a bounded operator mandate, never a claim that a model has
// measured elasticity. Canonical rate restrictions still apply independently.
type PricingPolicy struct {
	Mode                      string    `json:"mode"`
	Currency                  string    `json:"currency"`
	FloorMinor                int64     `json:"floor_minor"`
	CeilingMinor              int64     `json:"ceiling_minor"`
	MaxDailyChangeBPS         int64     `json:"max_daily_change_bps"`
	MaxCumulativeChangeBPS    int64     `json:"max_cumulative_change_bps"`
	CumulativeDays            int       `json:"cumulative_days"`
	ApprovalThresholdBPS      int64     `json:"approval_threshold_bps"`
	TargetOccupancyBPS        int64     `json:"target_occupancy_bps"`
	FeedbackGainBPS           int64     `json:"feedback_gain_bps"`
	MinimumHistoryCoverageBPS int64     `json:"minimum_history_coverage_bps"`
	MaxSourceAgeSeconds       int       `json:"max_source_age_seconds"`
	RecommendationTTLSeconds  int       `json:"recommendation_ttl_seconds"`
	BlackoutDates             []string  `json:"blackout_dates"`
	Channels                  []string  `json:"channels"`
	MandatoryInputs           []string  `json:"mandatory_inputs"`
	EffectiveAt               time.Time `json:"effective_at"`
	ExpiresAt                 time.Time `json:"expires_at"`
}

func (p PricingPolicy) Validate() error {
	if !slices.Contains([]string{"MANUAL", "RECOMMEND_ONLY", "APPROVAL_AUTOMATION", "AUTOPILOT_WITH_LIMITS"}, p.Mode) || p.FloorMinor < 0 || p.CeilingMinor < p.FloorMinor || p.CeilingMinor > 1000000000000000 || p.MaxDailyChangeBPS < 0 || p.MaxDailyChangeBPS > 1000 || p.MaxCumulativeChangeBPS < p.MaxDailyChangeBPS || p.MaxCumulativeChangeBPS > 5000 || p.CumulativeDays < 1 || p.CumulativeDays > 90 || p.ApprovalThresholdBPS < 0 || p.ApprovalThresholdBPS > p.MaxDailyChangeBPS || p.TargetOccupancyBPS < 1 || p.TargetOccupancyBPS > 10000 || p.FeedbackGainBPS < 0 || p.FeedbackGainBPS > 10000 || p.MinimumHistoryCoverageBPS < 5000 || p.MinimumHistoryCoverageBPS > 10000 || p.MaxSourceAgeSeconds < 60 || p.MaxSourceAgeSeconds > 86400 || p.RecommendationTTLSeconds < 60 || p.RecommendationTTLSeconds > 86400 || p.EffectiveAt.IsZero() || !p.ExpiresAt.After(p.EffectiveAt) || p.ExpiresAt.After(p.EffectiveAt.AddDate(1, 0, 0)) || len(p.BlackoutDates) > 366 || len(p.Channels) > 50 || len(p.MandatoryInputs) > 32 {
		return ErrPricing
	}
	if _, e := Ratio(p.CeilingMinor, 10000, 10000); e != nil {
		return e
	}
	if _, e := money.ParseCurrency(p.Currency); e != nil {
		return e
	}
	for _, day := range p.BlackoutDates {
		if _, e := time.Parse(time.DateOnly, day); e != nil {
			return ErrPricing
		}
	}
	seen := map[string]bool{}
	for _, input := range p.MandatoryInputs {
		if input == "" || len(input) > 100 || seen[input] {
			return ErrPricing
		}
		seen[input] = true
	}
	for _, channel := range p.Channels {
		if channel == "" || !inventory.ValidChannel(channel) {
			return ErrPricing
		}
	}
	return nil
}

type PricingSegment struct {
	DateFrom         string         `json:"date_from"`
	DateTo           string         `json:"date_to"`
	Previous         RateVersion    `json:"previous"`
	Proposed         RateDefinition `json:"proposed"`
	CalendarVersion  int64          `json:"calendar_version"`
	AppliedVersionID *ids.ID        `json:"applied_version_id,omitempty"`
}

// RecommendAmount is an explicitly configured feedback rule. It does not assert
// causal price/demand response and cannot widen canonical floors or ceilings.
func RecommendAmount(p PricingPolicy, old RateDefinition, day ForecastDay) (int64, error) {
	if p.Validate() != nil || old.Validate() != nil || old.Currency != p.Currency || day.Status != "BASELINE_READY" || day.ProjectedSoldMilliNights == nil || day.PhysicalCapacity <= 0 || slices.Contains(p.BlackoutDates, day.Day) {
		return 0, ErrPricing
	}
	if len(p.Channels) > 0 && !slices.Equal(p.Channels, old.Channels) {
		return 0, ErrPricing
	}
	occ, e := Ratio(*day.ProjectedSoldMilliNights, 10000, day.PhysicalCapacity*1000)
	if e != nil {
		return 0, e
	}
	change, e := Ratio(occ-p.TargetOccupancyBPS, p.FeedbackGainBPS, 10000)
	if e != nil {
		return 0, e
	}
	change = max(-p.MaxDailyChangeBPS, min(p.MaxDailyChangeBPS, change))
	delta, e := Ratio(old.AmountMinor, change, 10000)
	if e != nil {
		return 0, e
	}
	low, high := max(p.FloorMinor, old.FloorMinor), min(p.CeilingMinor, old.CeilingMinor)
	if low > high {
		return 0, ErrPricing
	}
	next, e := checkedAdd(old.AmountMinor, delta)
	if e != nil {
		return 0, e
	}
	return max(low, min(high, next)), nil
}

type PricingSimulation struct {
	Currency                         string   `json:"currency"`
	Basis                            string   `json:"basis"`
	ProjectedSoldMilliNights         int64    `json:"projected_sold_milli_nights"`
	ApplicableRoomNights             int64    `json:"applicable_room_nights"`
	ProjectedOccupancyBPS            *int64   `json:"projected_occupancy_bps"`
	ProjectedADRMinor                *int64   `json:"projected_adr_minor"`
	ProjectedRevPARMinor             *int64   `json:"projected_revpar_minor"`
	ProjectedGrossAccommodationMinor int64    `json:"projected_gross_accommodation_minor"`
	ProjectedChannelCommissionMinor  *int64   `json:"projected_channel_commission_minor"`
	ProjectedPromotionCostMinor      *int64   `json:"projected_promotion_cost_minor"`
	ProjectedPaymentFeeMinor         *int64   `json:"projected_payment_fee_minor"`
	ProjectedOperationalCostMinor    *int64   `json:"projected_operational_cost_minor"`
	ProjectedNetContributionMinor    *int64   `json:"projected_net_contribution_minor"`
	Assumptions                      []string `json:"assumptions"`
}

// SimulatePricing holds forecast demand constant: there is no calibrated
// elasticity input. Unknown fees/costs remain null rather than manufactured zero.
func SimulatePricing(f DemandForecast, segments []PricingSegment) (PricingSimulation, error) {
	out := PricingSimulation{Basis: "PROJECTED_FIXED_DEMAND_BASE_RATE_SCENARIO", Assumptions: []string{"Projected demand is held constant; no causal elasticity or conversion response is inferred.", "Amounts are base accommodation only; occupancy extras, taxes, promotions and derived adjustments are not assumed absent.", "Channel commissions, provider fees and operational costs are unknown until authoritative scoped inputs exist.", "No canonical rates, availability or accepted reservation snapshots are changed by simulation."}}
	if f.Status != "BASELINE_READY" || len(segments) != len(f.Days) || len(segments) == 0 || len(segments) > 90 {
		return out, ErrPricing
	}
	for i, s := range segments {
		d := f.Days[i]
		if s.DateFrom != d.Day || d.ProjectedSoldMilliNights == nil || d.Status != "BASELINE_READY" || s.Proposed.Validate() != nil || s.Proposed.ParentVersionID != nil || len(s.Proposed.Adjustments) > 0 || len(s.Proposed.Rules) > 0 || out.Currency != "" && out.Currency != s.Proposed.Currency {
			return out, ErrPricing
		}
		if d.PhysicalCapacity < 0 || d.PhysicalCapacity > 100000 || *d.ProjectedSoldMilliNights < 0 || *d.ProjectedSoldMilliNights > d.PhysicalCapacity*1000 {
			return out, ErrPricing
		}
		out.Currency = s.Proposed.Currency
		gross, e := Ratio(s.Proposed.AmountMinor, *d.ProjectedSoldMilliNights, 1000)
		if e != nil {
			return out, e
		}
		out.ProjectedGrossAccommodationMinor, e = checkedAdd(out.ProjectedGrossAccommodationMinor, gross)
		if e != nil {
			return out, e
		}
		out.ProjectedSoldMilliNights, e = checkedAdd(out.ProjectedSoldMilliNights, *d.ProjectedSoldMilliNights)
		if e != nil {
			return out, e
		}
		out.ApplicableRoomNights, e = checkedAdd(out.ApplicableRoomNights, d.PhysicalCapacity)
		if e != nil {
			return out, e
		}
	}
	if out.ApplicableRoomNights > 0 {
		v, e := Ratio(out.ProjectedSoldMilliNights, 10000, out.ApplicableRoomNights*1000)
		if e != nil {
			return out, e
		}
		out.ProjectedOccupancyBPS = &v
		v, e = Ratio(out.ProjectedGrossAccommodationMinor, 1, out.ApplicableRoomNights)
		if e != nil {
			return out, e
		}
		out.ProjectedRevPARMinor = &v
	}
	if out.ProjectedSoldMilliNights > 0 {
		v, e := Ratio(out.ProjectedGrossAccommodationMinor, 1000, out.ProjectedSoldMilliNights)
		if e != nil {
			return out, e
		}
		out.ProjectedADRMinor = &v
	}
	return out, nil
}
