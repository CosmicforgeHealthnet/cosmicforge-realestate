// Package revenue implements exact, versioned commercial pricing. Prediction,
// autopilot and financial execution belong to their later owning contexts.
package revenue

import (
	"errors"
	"math/big"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

var ErrPricing = errors.New("invalid pricing policy or arithmetic")

type Adjustment struct {
	Kind        string `json:"kind"`
	AmountMinor int64  `json:"amount_minor,omitempty"`
	BasisPoints int64  `json:"basis_points,omitempty"`
}

func (a Adjustment) Validate() error {
	if a.Kind == "FIXED" && a.BasisPoints == 0 {
		return nil
	}
	if a.Kind == "PERCENT" && a.AmountMinor == 0 && a.BasisPoints >= -10000 && a.BasisPoints <= 100000 {
		return nil
	}
	return ErrPricing
}

// Ratio applies one explicit HALF_AWAY_FROM_ZERO rounding in minor units.
// big.Int is used solely to avoid intermediate multiplication overflow.
func Ratio(amount, numerator, denominator int64) (int64, error) {
	if denominator <= 0 {
		return 0, ErrPricing
	}
	value := new(big.Int).Mul(big.NewInt(amount), big.NewInt(numerator))
	negative := value.Sign() < 0
	value.Abs(value)
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(value, big.NewInt(denominator), r)
	if new(big.Int).Mul(r, big.NewInt(2)).Cmp(big.NewInt(denominator)) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if negative {
		q.Neg(q)
	}
	if !q.IsInt64() {
		return 0, money.ErrOverflow
	}
	return q.Int64(), nil
}
func Adjust(amount money.Money, rules []Adjustment) (money.Money, error) {
	if len(rules) > 8 {
		return money.Money{}, ErrPricing
	}
	var err error
	for _, rule := range rules {
		if rule.Validate() != nil {
			return money.Money{}, ErrPricing
		}
		delta := rule.AmountMinor
		if rule.Kind == "PERCENT" {
			delta, err = Ratio(amount.AmountMinor, rule.BasisPoints, 10000)
			if err != nil {
				return money.Money{}, err
			}
		}
		amount, err = amount.Add(money.Money{AmountMinor: delta, Currency: amount.Currency})
		if err != nil || amount.IsNegative() {
			return money.Money{}, ErrPricing
		}
	}
	return amount, nil
}

type Rule struct {
	Priority   int        `json:"priority"`
	DateFrom   string     `json:"date_from"`
	DateTo     string     `json:"date_to"`
	Weekdays   []int      `json:"weekdays,omitempty"`
	MinNights  int        `json:"min_nights,omitempty"`
	MaxNights  int        `json:"max_nights,omitempty"`
	Channel    string     `json:"channel,omitempty"`
	Adjustment Adjustment `json:"adjustment"`
}
type RateDefinition struct {
	Currency              string       `json:"currency"`
	AmountMinor           int64        `json:"amount_minor"`
	ParentVersionID       *ids.ID      `json:"parent_version_id,omitempty"`
	Adjustments           []Adjustment `json:"adjustments,omitempty"`
	IncludedAdults        int          `json:"included_adults"`
	ExtraAdultMinor       int64        `json:"extra_adult_minor"`
	ChildMinor            int64        `json:"child_minor"`
	MinGuests             int          `json:"min_guests"`
	MaxGuests             int          `json:"max_guests"`
	MinNights             int          `json:"min_nights"`
	MaxNights             int          `json:"max_nights"`
	Channels              []string     `json:"channels,omitempty"`
	CancellationReference string       `json:"cancellation_reference"`
	PrepaymentReference   string       `json:"prepayment_reference"`
	PolicyCode            string       `json:"policy_code"`
	PolicyVersion         int64        `json:"policy_version"`
	FloorMinor            int64        `json:"floor_minor"`
	CeilingMinor          int64        `json:"ceiling_minor"`
	MaxChangeBPS          int64        `json:"max_change_bps"`
	IncludedAddOns        []ids.ID     `json:"included_add_ons,omitempty"`
	TaxProfileID          *ids.ID      `json:"tax_profile_id,omitempty"`
	Rules                 []Rule       `json:"rules,omitempty"`
}

func (d RateDefinition) Validate() error {
	if _, err := money.ParseCurrency(d.Currency); err != nil {
		return err
	}
	if d.AmountMinor < 0 || d.ExtraAdultMinor < 0 || d.ChildMinor < 0 || d.FloorMinor < 0 || d.CeilingMinor < d.FloorMinor || d.AmountMinor < d.FloorMinor || d.AmountMinor > d.CeilingMinor || d.MaxChangeBPS < 0 || d.MaxChangeBPS > 1000 || d.MinGuests < 1 || d.MaxGuests < d.MinGuests || d.MaxGuests > 100000 || d.IncludedAdults < 1 || d.IncludedAdults > d.MaxGuests || d.MinNights < 1 || d.MaxNights < d.MinNights || d.MaxNights > 366 || len(d.CancellationReference) < 1 || len(d.CancellationReference) > 200 || len(d.PrepaymentReference) < 1 || len(d.PrepaymentReference) > 200 || len(d.PolicyCode) < 1 || len(d.PolicyCode) > 80 || d.PolicyVersion < 1 || len(d.Rules) > 64 || len(d.Adjustments) > 8 || len(d.Channels) > 50 || len(d.IncludedAddOns) > 20 {
		return ErrPricing
	}
	for _, a := range d.Adjustments {
		if a.Validate() != nil {
			return ErrPricing
		}
	}
	for _, c := range d.Channels {
		if !inventory.ValidChannel(c) || c == "" {
			return ErrPricing
		}
	}
	if d.ParentVersionID != nil && d.ParentVersionID.ValidateV7() != nil || d.TaxProfileID != nil && d.TaxProfileID.ValidateV7() != nil {
		return ErrPricing
	}
	addOns := map[ids.ID]bool{}
	for _, id := range d.IncludedAddOns {
		if id.ValidateV7() != nil || addOns[id] {
			return ErrPricing
		}
		addOns[id] = true
	}
	priorities := map[int]bool{}
	for _, r := range d.Rules {
		if _, e := (inventory.DateRange{DateFrom: r.DateFrom, DateTo: r.DateTo}).Days("UTC"); e != nil || !inventory.ValidChannel(r.Channel) {
			return ErrPricing
		}
		if r.Priority < 0 || priorities[r.Priority] || r.Adjustment.Validate() != nil || r.DateTo <= r.DateFrom || r.MinNights < 0 || r.MaxNights < 0 || r.MaxNights > 0 && r.MaxNights < r.MinNights {
			return ErrPricing
		}
		priorities[r.Priority] = true
		for _, day := range r.Weekdays {
			if day < 0 || day > 6 {
				return ErrPricing
			}
		}
	}
	return nil
}

type Restriction struct {
	StopSell          bool   `json:"stop_sell,omitempty"`
	ClosedToArrival   bool   `json:"closed_to_arrival,omitempty"`
	ClosedToDeparture bool   `json:"closed_to_departure,omitempty"`
	MinNights         int    `json:"min_nights,omitempty"`
	MaxNights         int    `json:"max_nights,omitempty"`
	MinAdvanceDays    int    `json:"min_advance_days,omitempty"`
	MaxAdvanceDays    int    `json:"max_advance_days,omitempty"`
	CutoffLocal       string `json:"cutoff_local,omitempty"`
	MinGuests         int    `json:"min_guests,omitempty"`
	MaxGuests         int    `json:"max_guests,omitempty"`
	Weekdays          []int  `json:"weekdays,omitempty"`
}

func (r Restriction) Validate() error {
	if r.MinNights < 0 || r.MaxNights < 0 || r.MaxNights > 366 || r.MaxNights > 0 && r.MinNights > r.MaxNights || r.MinAdvanceDays < 0 || r.MaxAdvanceDays < 0 || r.MaxAdvanceDays > 730 || r.MaxAdvanceDays > 0 && r.MinAdvanceDays > r.MaxAdvanceDays || r.MinGuests < 0 || r.MaxGuests < 0 || r.MaxGuests > 100000 || r.MaxGuests > 0 && r.MinGuests > r.MaxGuests {
		return ErrPricing
	}
	if r.CutoffLocal != "" {
		t, e := time.Parse("15:04", r.CutoffLocal)
		if e != nil || t.Format("15:04") != r.CutoffLocal {
			return ErrPricing
		}
	}
	for _, d := range r.Weekdays {
		if d < 0 || d > 6 {
			return ErrPricing
		}
	}
	return nil
}

// Intersect is deterministic: hard restrictions narrow eligibility. No lower
// priority rule, promotion or channel override can widen the permitted window.
func Intersect(rules []Restriction) (Restriction, error) {
	out := Restriction{}
	for _, r := range rules {
		if r.Validate() != nil {
			return out, ErrPricing
		}
		out.StopSell = out.StopSell || r.StopSell
		out.ClosedToArrival = out.ClosedToArrival || r.ClosedToArrival
		out.ClosedToDeparture = out.ClosedToDeparture || r.ClosedToDeparture
		out.MinNights = max(out.MinNights, r.MinNights)
		if r.MaxNights > 0 && (out.MaxNights == 0 || r.MaxNights < out.MaxNights) {
			out.MaxNights = r.MaxNights
		}
		out.MinAdvanceDays = max(out.MinAdvanceDays, r.MinAdvanceDays)
		if r.MaxAdvanceDays > 0 && (out.MaxAdvanceDays == 0 || r.MaxAdvanceDays < out.MaxAdvanceDays) {
			out.MaxAdvanceDays = r.MaxAdvanceDays
		}
		out.MinGuests = max(out.MinGuests, r.MinGuests)
		if r.MaxGuests > 0 && (out.MaxGuests == 0 || r.MaxGuests < out.MaxGuests) {
			out.MaxGuests = r.MaxGuests
		}
		if r.CutoffLocal != "" && (out.CutoffLocal == "" || r.CutoffLocal < out.CutoffLocal) {
			out.CutoffLocal = r.CutoffLocal
		}
	}
	return out, out.Validate()
}
func Nightly(def RateDefinition, base money.Money, day, channel string, nights, adults, children int) (money.Money, error) {
	if def.Validate() != nil || adults < 1 || children < 0 || adults+children < def.MinGuests || adults+children > def.MaxGuests || nights < def.MinNights || nights > def.MaxNights || len(def.Channels) > 0 && !slices.Contains(def.Channels, channel) {
		return money.Money{}, ErrPricing
	}
	extra, e := money.New(def.ExtraAdultMinor, def.Currency)
	if e != nil {
		return base, e
	}
	extra, e = extra.MulInt(int64(max(0, adults-def.IncludedAdults)))
	if e != nil {
		return base, e
	}
	base, e = base.Add(extra)
	if e != nil {
		return base, e
	}
	child, _ := money.New(def.ChildMinor, def.Currency)
	child, e = child.MulInt(int64(children))
	if e != nil {
		return base, e
	}
	base, e = base.Add(child)
	if e != nil {
		return base, e
	}
	date, e := time.Parse(time.DateOnly, day)
	if e != nil {
		return base, ErrPricing
	}
	rules := append([]Rule(nil), def.Rules...)
	slices.SortFunc(rules, func(a, b Rule) int { return a.Priority - b.Priority })
	for _, r := range rules {
		if day < r.DateFrom || day >= r.DateTo || r.Channel != "" && r.Channel != channel || r.MinNights > 0 && nights < r.MinNights || r.MaxNights > 0 && nights > r.MaxNights || len(r.Weekdays) > 0 && !slices.Contains(r.Weekdays, int(date.Weekday())) {
			continue
		}
		base, e = Adjust(base, []Adjustment{r.Adjustment})
		if e != nil {
			return base, e
		}
	}
	if base.AmountMinor < def.FloorMinor || base.AmountMinor > def.CeilingMinor {
		return base, ErrPricing
	}
	return base, nil
}

type TaxComponent struct {
	Code        string `json:"code"`
	BasisPoints int64  `json:"basis_points"`
	Included    bool   `json:"included"`
}
type FeeComponent struct {
	Code        string `json:"code"`
	AmountMinor int64  `json:"amount_minor"`
	PerNight    bool   `json:"per_night"`
	Included    bool   `json:"included"`
}
type TaxPolicy struct {
	DisplayMode     string         `json:"display_mode"`
	Taxes           []TaxComponent `json:"taxes"`
	Fees            []FeeComponent `json:"fees"`
	FiscalReference string         `json:"fiscal_reference"`
	PolicyVersion   int64          `json:"policy_version"`
}
type ComponentAmount struct {
	Code        string `json:"code"`
	AmountMinor int64  `json:"amount_minor"`
	Included    bool   `json:"included"`
}

func (p TaxPolicy) Validate() error {
	if p.DisplayMode != "INCLUSIVE" && p.DisplayMode != "EXCLUSIVE" && p.DisplayMode != "ITEMIZED" || len(p.Taxes) > 20 || len(p.Fees) > 20 || len(p.FiscalReference) < 1 || len(p.FiscalReference) > 200 || p.PolicyVersion < 1 {
		return ErrPricing
	}
	codes := map[string]bool{}
	var bps int64
	for _, t := range p.Taxes {
		if t.Code == "" || len(t.Code) > 80 || codes[t.Code] || t.BasisPoints < 0 || t.BasisPoints > 10000 {
			return ErrPricing
		}
		codes[t.Code] = true
		bps += t.BasisPoints
	}
	if bps > 10000 {
		return ErrPricing
	}
	for _, f := range p.Fees {
		if f.Code == "" || len(f.Code) > 80 || codes[f.Code] || f.AmountMinor < 0 {
			return ErrPricing
		}
		codes[f.Code] = true
	}
	return nil
}
func (p TaxPolicy) Apply(base money.Money, nights int) (money.Money, []ComponentAmount, error) {
	if p.Validate() != nil || nights < 1 {
		return base, nil, ErrPricing
	}
	components := []ComponentAmount{}
	total := base
	net := base
	for _, fee := range p.Fees {
		amount, e := money.New(fee.AmountMinor, base.Currency.String())
		if e != nil {
			return total, nil, e
		}
		if fee.PerNight {
			amount, e = amount.MulInt(int64(nights))
			if e != nil {
				return total, nil, e
			}
		}
		if fee.Included {
			net, e = net.Sub(amount)
		} else {
			total, e = total.Add(amount)
		}
		if e != nil || net.IsNegative() {
			return total, nil, ErrPricing
		}
		components = append(components, ComponentAmount{Code: fee.Code, AmountMinor: amount.AmountMinor, Included: fee.Included})
	}
	var includedBPS int64
	for _, t := range p.Taxes {
		if t.Included {
			includedBPS += t.BasisPoints
		}
	}
	taxBaseMinor, e := Ratio(net.AmountMinor, 10000, 10000+includedBPS)
	if e != nil {
		return total, nil, e
	}
	// Included tax residual goes to the final included component, preserving the
	// exact original minor-unit amount even across independently rounded lines.
	remaining := net.AmountMinor - taxBaseMinor
	last := -1
	for i, t := range p.Taxes {
		if t.Included {
			last = i
		}
	}
	for i, t := range p.Taxes {
		amount, e := Ratio(taxBaseMinor, t.BasisPoints, 10000)
		if e != nil {
			return total, nil, e
		}
		if t.Included {
			if i == last {
				amount = remaining
			} else if amount > remaining {
				amount = remaining
			}
			remaining -= amount
		} else {
			total, e = total.Add(money.Money{AmountMinor: amount, Currency: base.Currency})
			if e != nil {
				return total, nil, e
			}
		}
		components = append(components, ComponentAmount{Code: t.Code, AmountMinor: amount, Included: t.Included})
	}
	return total, components, nil
}
