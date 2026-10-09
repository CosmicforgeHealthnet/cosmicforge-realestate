package domain

import (
	"fmt"
	"slices"
	"time"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
)

// Pricing (§13.8): deterministic all-in cost calculation shared by the owner
// preview, the marketplace cost preview and offer acceptance. Integer minor
// units only; percentages are basis points rounded half-up.

// CalculatorVersion identifies the calculation rules (recorded with results).
const CalculatorVersion = "2026-10-1"

// MaxAmountMinor bounds money inputs so integer arithmetic cannot overflow.
const MaxAmountMinor int64 = 100_000_000_000_000

// Fee is one disclosed fee of a pricing version.
type Fee struct {
	Code        string `json:"code"`
	Category    string `json:"category"`
	Label       string `json:"label"`
	Calculation string `json:"calculation"` // FIXED | PERCENT_OF_BASE
	AmountMinor *int64 `json:"amount_minor"`
	RateBps     *int32 `json:"rate_bps"`
	Frequency   string `json:"frequency"` // ONE_TIME | PER_CYCLE | PER_NIGHT | PER_STAY
	Mandatory   bool   `json:"mandatory"`
	Refundable  bool   `json:"refundable"`
	PayableTo   string `json:"payable_to"`
}

// RuleParams parameterize pricing rules.
type RuleParams struct {
	AfterMonths   int32    `json:"after_months,omitempty" v:"min=1,max=600" doc:"ESCALATION: every N months"`
	RateBps       int32    `json:"rate_bps,omitempty" v:"min=-10000,max=10000" doc:"Change in basis points (+surcharge / -discount; DISCOUNT and LONG_STAY take a positive discount)"`
	MinTermMonths int32    `json:"min_term_months,omitempty" v:"min=1,max=600" doc:"DISCOUNT: minimum term"`
	MinNights     int32    `json:"min_nights,omitempty" v:"min=1,max=365" doc:"LONG_STAY: minimum nights"`
	From          string   `json:"from,omitempty" v:"pattern=(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])" doc:"SEASONAL: MM-DD (inclusive)"`
	To            string   `json:"to,omitempty" v:"pattern=(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])" doc:"SEASONAL: MM-DD (inclusive, may wrap the year)"`
	Days          []string `json:"days,omitempty" v:"max=7,enum=MON|TUE|WED|THU|FRI|SAT|SUN" doc:"WEEKEND: nights starting on these days"`
}

// Rule is one pricing rule.
type Rule struct {
	Type   string     `json:"rule_type"`
	Params RuleParams `json:"params"`
}

// PriceModel is a complete pricing version.
type PriceModel struct {
	TransactionType string
	Currency        string
	Base            int64
	BasePeriod      string // MONTH | YEAR | NIGHT | TOTAL
	Cycle           string // MONTHLY | QUARTERLY | BIANNUAL | ANNUAL | PER_STAY | ONE_TIME
	Deposit         int64
	MinTermMonths   int32 // from listing details (leases)
	MinNights       int32 // from listing details (short-let)
	Fees            []Fee
	Rules           []Rule
}

// Scenario is what the customer asks about.
type Scenario struct {
	TermMonths int32     `json:"term_months,omitempty" v:"min=1,max=600"`
	CheckIn    time.Time `json:"check_in,omitzero"`
	CheckOut   time.Time `json:"check_out,omitzero"`
}

// LineItem is one cost line.
type LineItem struct {
	Code        string `json:"code"`
	Label       string `json:"label"`
	Category    string `json:"category"`
	AmountMinor int64  `json:"amount_minor"`
	Mandatory   bool   `json:"mandatory"`
	Refundable  bool   `json:"refundable"`
	Timing      string `json:"timing" doc:"UPFRONT, RECURRING (per cycle), STAY or PURCHASE"`
	PayableTo   string `json:"payable_to"`
}

// Cost is the all-in breakdown.
type Cost struct {
	Currency          string     `json:"currency"`
	TransactionType   string     `json:"transaction_type"`
	Lines             []LineItem `json:"lines"`
	OptionalLines     []LineItem `json:"optional_lines" doc:"Disclosed optional extras, excluded from totals"`
	UpfrontTotal      int64      `json:"upfront_total_minor" doc:"Everything due before move-in / check-in / completion"`
	RecurringPerCycle int64      `json:"recurring_per_cycle_minor"`
	Cycle             string     `json:"cycle"`
	CycleCount        int32      `json:"cycle_count"`
	TermMonths        int32      `json:"term_months"`
	Nights            int32      `json:"nights"`
	TermTotal         int64      `json:"term_total_minor" doc:"Total payable over the term/stay, excluding refundable deposits"`
	RefundableTotal   int64      `json:"refundable_total_minor"`
	Assumptions       []string   `json:"assumptions"`
	CalculatorVersion string     `json:"calculator_version"`
}

// Lease transaction types.
func isLease(t string) bool {
	return t == "RENT_LONG_TERM" || t == "COMMERCIAL_LEASE" || t == "LAND_LEASE"
}

func invalid(field, msg string) error {
	return apperrors.InvalidRequest(msg, apperrors.Detail{Field: field, Code: "INVALID", Message: msg})
}

// ValidatePriceModel checks period/cycle/fee/rule compatibility with the
// transaction type. Fee categories are checked against configured policy.
func ValidatePriceModel(m PriceModel, categories []string) error {
	type combo struct{ periods, cycles, freqs, rules []string }
	allowed := map[string]combo{
		"RENT_LONG_TERM":   {[]string{"MONTH", "YEAR"}, []string{"MONTHLY", "QUARTERLY", "BIANNUAL", "ANNUAL"}, []string{"ONE_TIME", "PER_CYCLE"}, []string{"ESCALATION", "DISCOUNT"}},
		"COMMERCIAL_LEASE": {[]string{"MONTH", "YEAR"}, []string{"MONTHLY", "QUARTERLY", "BIANNUAL", "ANNUAL"}, []string{"ONE_TIME", "PER_CYCLE"}, []string{"ESCALATION", "DISCOUNT"}},
		"LAND_LEASE":       {[]string{"YEAR", "TOTAL"}, []string{"ANNUAL", "ONE_TIME"}, []string{"ONE_TIME", "PER_CYCLE"}, []string{"ESCALATION", "DISCOUNT"}},
		"SHORT_LET":        {[]string{"NIGHT"}, []string{"PER_STAY"}, []string{"PER_NIGHT", "PER_STAY", "ONE_TIME"}, []string{"SEASONAL", "WEEKEND", "LONG_STAY"}},
		"SALE":             {[]string{"TOTAL"}, []string{"ONE_TIME"}, []string{"ONE_TIME"}, nil},
	}
	c, ok := allowed[m.TransactionType]
	switch {
	case !ok:
		return invalid("transaction_type", "unknown transaction type")
	case !slices.Contains(c.periods, m.BasePeriod):
		return invalid("base_period", fmt.Sprintf("base_period must be one of %v for %s", c.periods, m.TransactionType))
	case !slices.Contains(c.cycles, m.Cycle):
		return invalid("payment_cycle", fmt.Sprintf("payment_cycle must be one of %v for %s", c.cycles, m.TransactionType))
	case m.BasePeriod == "TOTAL" && m.Cycle != "ONE_TIME" || m.Cycle == "ONE_TIME" && m.BasePeriod != "TOTAL":
		return invalid("payment_cycle", "a TOTAL base price is paid ONE_TIME")
	case m.Base <= 0 || m.Base > MaxAmountMinor || m.Deposit < 0 || m.Deposit > MaxAmountMinor:
		return invalid("base_amount_minor", "amounts must be positive and within range")
	}
	codes := map[string]bool{}
	for i, f := range m.Fees {
		field := fmt.Sprintf("fees[%d]", i)
		if codes[f.Code] {
			return invalid(field+".code", "duplicate fee code "+f.Code)
		}
		codes[f.Code] = true
		if !slices.Contains(categories, f.Category) {
			return apperrors.Unprocessable("FEE_CATEGORY_NOT_CONFIGURED", "Fee category "+f.Category+" is not configured by policy.").
				WithDetails(apperrors.Detail{Field: field + ".category", Code: "NOT_CONFIGURED", Message: f.Category})
		}
		if !slices.Contains(c.freqs, f.Frequency) {
			return invalid(field+".frequency", fmt.Sprintf("frequency must be one of %v for %s", c.freqs, m.TransactionType))
		}
		switch f.Calculation {
		case "FIXED":
			if f.AmountMinor == nil || f.RateBps != nil || *f.AmountMinor < 0 || *f.AmountMinor > MaxAmountMinor {
				return invalid(field+".amount_minor", "FIXED fees need amount_minor only")
			}
		case "PERCENT_OF_BASE":
			if f.RateBps == nil || f.AmountMinor != nil || *f.RateBps <= 0 || *f.RateBps > 10000 {
				return invalid(field+".rate_bps", "PERCENT_OF_BASE fees need rate_bps (1–10000) only")
			}
		default:
			return invalid(field+".calculation", "calculation must be FIXED or PERCENT_OF_BASE")
		}
	}
	for i, r := range m.Rules {
		field := fmt.Sprintf("rules[%d]", i)
		if !slices.Contains(c.rules, r.Type) {
			return invalid(field+".rule_type", fmt.Sprintf("rule_type must be one of %v for %s", c.rules, m.TransactionType))
		}
		p := r.Params
		var ok bool
		switch r.Type {
		case "ESCALATION":
			ok = p.AfterMonths > 0 && p.RateBps > 0
		case "DISCOUNT":
			ok = p.MinTermMonths > 0 && p.RateBps > 0
		case "LONG_STAY":
			ok = p.MinNights > 0 && p.RateBps > 0
		case "SEASONAL":
			ok = p.From != "" && p.To != "" && p.RateBps != 0
		case "WEEKEND":
			ok = len(p.Days) > 0 && p.RateBps != 0
		}
		if !ok {
			return invalid(field+".params", "missing or invalid parameters for "+r.Type)
		}
	}
	return nil
}

func bps(amount int64, rate int32) int64 {
	n := amount * int64(rate)
	if n >= 0 {
		return (n + 5000) / 10000
	}
	return -((-n + 5000) / 10000)
}

var cycleMonths = map[string]int32{"MONTHLY": 1, "QUARTERLY": 3, "BIANNUAL": 6, "ANNUAL": 12}
var periodMonths = map[string]int64{"MONTH": 1, "YEAR": 12}

// Calculate computes the all-in cost for a scenario.
func Calculate(m PriceModel, sc Scenario) (Cost, error) {
	c := Cost{Currency: m.Currency, TransactionType: m.TransactionType, Lines: []LineItem{}, OptionalLines: []LineItem{},
		Assumptions: []string{}, CalculatorVersion: CalculatorVersion}
	fee := func(f Fee, base int64, mult int64, timing string) LineItem {
		amt := int64(0)
		if f.Calculation == "FIXED" {
			amt = *f.AmountMinor
		} else {
			amt = bps(base, *f.RateBps)
		}
		return LineItem{Code: f.Code, Label: f.Label, Category: f.Category, AmountMinor: amt * mult, Mandatory: f.Mandatory, Refundable: f.Refundable,
			Timing: timing, PayableTo: f.PayableTo}
	}
	add := func(l LineItem) {
		if !l.Mandatory {
			c.OptionalLines = append(c.OptionalLines, l)
			return
		}
		c.Lines = append(c.Lines, l)
	}
	switch {
	case isLease(m.TransactionType):
		term := sc.TermMonths
		if term == 0 {
			term = max(m.MinTermMonths, 12)
			c.Assumptions = append(c.Assumptions, fmt.Sprintf("term of %d months assumed", term))
		}
		if m.MinTermMonths > 0 && term < m.MinTermMonths {
			return Cost{}, apperrors.Unprocessable("TERM_BELOW_MINIMUM", fmt.Sprintf("The minimum term is %d months.", m.MinTermMonths))
		}
		c.TermMonths = term
		var cycleRent int64
		var cm int32
		if m.BasePeriod == "TOTAL" {
			cm, cycleRent = term, m.Base
			c.Cycle, c.CycleCount = "ONE_TIME", 1
		} else {
			cm = cycleMonths[m.Cycle]
			cycleRent = (m.Base*int64(cm)*2 + periodMonths[m.BasePeriod]) / (2 * periodMonths[m.BasePeriod]) // rounded half-up
			c.Cycle, c.CycleCount = m.Cycle, (term+cm-1)/cm
		}
		for _, r := range m.Rules {
			if r.Type == "DISCOUNT" && term >= r.Params.MinTermMonths {
				cycleRent -= bps(cycleRent, r.Params.RateBps)
				c.Assumptions = append(c.Assumptions, fmt.Sprintf("term discount of %d bps applied", r.Params.RateBps))
			}
		}
		// Escalation compounds at each completed interval.
		var total int64
		rentAt := func(i int32) int64 {
			rent := cycleRent
			for _, r := range m.Rules {
				if r.Type == "ESCALATION" {
					for step := (i * cm) / r.Params.AfterMonths; step > 0; step-- {
						rent += bps(rent, r.Params.RateBps)
					}
				}
			}
			return rent
		}
		for i := int32(0); i < c.CycleCount; i++ {
			total += rentAt(i)
		}
		first := rentAt(0)
		c.Lines = append(c.Lines, LineItem{Code: "rent", Label: "Rent (first " + lowerCycle(c.Cycle) + ")", Category: "RENT", AmountMinor: first,
			Mandatory: true, Timing: "UPFRONT", PayableTo: "OWNER"})
		yearRent := first * int64(min(term, 12)) / int64(cm)
		if c.Cycle == "ONE_TIME" {
			yearRent = first
		}
		c.UpfrontTotal, c.RecurringPerCycle, c.TermTotal = first, first, total
		if m.Deposit > 0 {
			c.Lines = append(c.Lines, LineItem{Code: "deposit", Label: "Refundable deposit", Category: "DEPOSIT", AmountMinor: m.Deposit, Mandatory: true,
				Refundable: true, Timing: "UPFRONT", PayableTo: "OWNER"})
			c.UpfrontTotal += m.Deposit
			c.RefundableTotal += m.Deposit
		}
		for _, f := range m.Fees {
			var l LineItem
			if f.Frequency == "PER_CYCLE" {
				l = fee(f, first, 1, "RECURRING")
			} else {
				l = fee(f, yearRent, 1, "UPFRONT")
			}
			add(l)
			if !l.Mandatory {
				continue
			}
			c.UpfrontTotal += l.AmountMinor
			if l.Refundable {
				c.RefundableTotal += l.AmountMinor
				continue
			}
			if f.Frequency == "PER_CYCLE" {
				c.RecurringPerCycle += l.AmountMinor
				c.TermTotal += l.AmountMinor * int64(c.CycleCount)
			} else {
				c.TermTotal += l.AmountMinor
			}
		}
		c.Assumptions = append(c.Assumptions, "percentage one-time fees are computed on up to 12 months of rent; per-cycle percentage fees on the cycle rent")
	case m.TransactionType == "SHORT_LET":
		if sc.CheckIn.IsZero() || sc.CheckOut.IsZero() {
			return Cost{}, invalid("check_in", "check_in and check_out are required for short-let previews")
		}
		in, out := day(sc.CheckIn), day(sc.CheckOut)
		nights := int32(out.Sub(in).Hours() / 24)
		if nights < 1 || nights > 365 {
			return Cost{}, invalid("check_out", "a stay is 1–365 nights")
		}
		if m.MinNights > 0 && nights < m.MinNights {
			return Cost{}, apperrors.Unprocessable("STAY_BELOW_MINIMUM", fmt.Sprintf("The minimum stay is %d nights.", m.MinNights))
		}
		c.Nights, c.Cycle, c.CycleCount = nights, "PER_STAY", 1
		var subtotal int64
		for d := in; d.Before(out); d = d.AddDate(0, 0, 1) {
			rate := m.Base
			for _, r := range m.Rules {
				switch r.Type {
				case "SEASONAL":
					if inSeason(d, r.Params.From, r.Params.To) {
						rate += bps(m.Base, r.Params.RateBps)
					}
				case "WEEKEND":
					if slices.Contains(r.Params.Days, weekday(d)) {
						rate += bps(m.Base, r.Params.RateBps)
					}
				}
			}
			subtotal += rate
		}
		for _, r := range m.Rules {
			if r.Type == "LONG_STAY" && nights >= r.Params.MinNights {
				subtotal -= bps(subtotal, r.Params.RateBps)
				c.Assumptions = append(c.Assumptions, fmt.Sprintf("long-stay discount of %d bps applied", r.Params.RateBps))
			}
		}
		c.Lines = append(c.Lines, LineItem{Code: "accommodation", Label: fmt.Sprintf("Accommodation (%d nights)", nights), Category: "ACCOMMODATION",
			AmountMinor: subtotal, Mandatory: true, Timing: "STAY", PayableTo: "OWNER"})
		c.TermTotal = subtotal
		for _, f := range m.Fees {
			mult := int64(1)
			if f.Frequency == "PER_NIGHT" {
				mult = int64(nights)
			}
			base := subtotal
			if f.Frequency == "PER_NIGHT" {
				base = m.Base
			}
			l := fee(f, base, mult, "STAY")
			add(l)
			if l.Mandatory {
				if l.Refundable {
					c.RefundableTotal += l.AmountMinor
				} else {
					c.TermTotal += l.AmountMinor
				}
			}
		}
		if m.Deposit > 0 {
			c.Lines = append(c.Lines, LineItem{Code: "deposit", Label: "Refundable security deposit", Category: "DEPOSIT", AmountMinor: m.Deposit,
				Mandatory: true, Refundable: true, Timing: "STAY", PayableTo: "OWNER"})
			c.RefundableTotal += m.Deposit
		}
		c.UpfrontTotal = c.TermTotal + c.RefundableTotal
	case m.TransactionType == "SALE":
		c.Cycle, c.CycleCount = "ONE_TIME", 1
		c.Lines = append(c.Lines, LineItem{Code: "price", Label: "Purchase price", Category: "PRICE", AmountMinor: m.Base, Mandatory: true,
			Timing: "PURCHASE", PayableTo: "OWNER"})
		c.TermTotal = m.Base
		for _, f := range m.Fees {
			l := fee(f, m.Base, 1, "PURCHASE")
			add(l)
			if l.Mandatory {
				c.TermTotal += l.AmountMinor
			}
		}
		if m.Deposit > 0 {
			c.Assumptions = append(c.Assumptions, fmt.Sprintf("a deposit of %d minor units is part of the purchase price", m.Deposit))
		}
		c.UpfrontTotal = c.TermTotal
	}
	return c, nil
}

func lowerCycle(c string) string {
	switch c {
	case "MONTHLY":
		return "month"
	case "QUARTERLY":
		return "quarter"
	case "BIANNUAL":
		return "half-year"
	case "ANNUAL":
		return "year"
	}
	return "payment"
}

func day(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func weekday(t time.Time) string {
	return [...]string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}[t.Weekday()]
}

func inSeason(d time.Time, from, to string) bool {
	md := d.Format("01-02")
	if from <= to {
		return md >= from && md <= to
	}
	return md >= from || md <= to // wraps the year
}
