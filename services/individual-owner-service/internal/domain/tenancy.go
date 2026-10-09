package domain

import (
	"fmt"
	"time"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
)

// RentTerms are the schedule-relevant terms of a tenancy (§13.14).
type RentTerms struct {
	Start         time.Time
	TermMonths    int32
	Frequency     string // MONTHLY | QUARTERLY | BIANNUAL | ANNUAL | ONE_TIME
	DueDay        int32  // 1–28: rent for a period is due on this day of the month the period begins (the first period on its start date)
	RentMinor     int64  // cycle rent at the start of the term
	FeesMinor     int64  // mandatory recurring fees per cycle (not escalated)
	EscalateAfter int32  // months; 0 = none
	EscalateBps   int32
}

// RentPeriod is one materialized obligation of the schedule.
type RentPeriod struct {
	Seq         int32
	PeriodStart time.Time
	PeriodEnd   time.Time
	DueDate     time.Time
	AmountMinor int64
}

// CycleMonths reports the months covered by one cycle (0 for ONE_TIME).
func CycleMonths(freq string) int32 { return cycleMonths[freq] }

// ValidateRentTerms checks that the term is a whole number of cycles.
func ValidateRentTerms(t RentTerms) error {
	if t.Frequency != "ONE_TIME" && cycleMonths[t.Frequency] == 0 {
		return invalid("frequency", "unknown frequency")
	}
	if m := cycleMonths[t.Frequency]; m > 0 && t.TermMonths%m != 0 {
		return apperrors.Unprocessable("TERM_NOT_WHOLE_CYCLES", fmt.Sprintf("The term must be a whole number of %s cycles (%d months).", t.Frequency, m))
	}
	if t.DueDay < 1 || t.DueDay > 28 {
		return invalid("due_day", "due_day must be 1–28")
	}
	if t.RentMinor <= 0 {
		return invalid("rent_minor", "rent must be positive")
	}
	if (t.EscalateAfter == 0) != (t.EscalateBps == 0) {
		return invalid("escalation", "escalation needs both after_months and rate_bps")
	}
	return nil
}

// EndDate is the last day of the term.
func (t RentTerms) EndDate() time.Time {
	return AddMonths(t.Start, t.TermMonths).AddDate(0, 0, -1)
}

// AddMonths adds n calendar months, clamping to the last day of the target
// month (31 Jan + 1 month = 28/29 Feb), so schedules never drift.
func AddMonths(t time.Time, n int32) time.Time {
	t = day(t)
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, int(n), 0)
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(t.Day(), last), 0, 0, 0, 0, time.UTC)
}

// RentSchedule lists the periods of the term starting on or before until
// (inclusive), deterministically: escalation compounds every EscalateAfter
// months from the start of the term using the shared basis-point rounding.
func RentSchedule(t RentTerms, until time.Time) []RentPeriod {
	start, end, until := day(t.Start), t.EndDate(), day(until)
	var out []RentPeriod
	if t.Frequency == "ONE_TIME" {
		if !start.After(until) {
			out = append(out, RentPeriod{Seq: 1, PeriodStart: start, PeriodEnd: end, DueDate: start, AmountMinor: t.RentMinor + t.FeesMinor})
		}
		return out
	}
	m := cycleMonths[t.Frequency]
	for i := int32(0); ; i++ {
		ps := AddMonths(start, i*m)
		if ps.After(end) || ps.After(until) {
			break
		}
		pe := AddMonths(start, (i+1)*m).AddDate(0, 0, -1)
		if pe.After(end) {
			pe = end
		}
		due := ps
		if i > 0 {
			due = time.Date(ps.Year(), ps.Month(), int(t.DueDay), 0, 0, 0, 0, time.UTC)
		}
		rent := t.RentMinor
		if t.EscalateAfter > 0 {
			for s := int32(0); s < (i*m)/t.EscalateAfter; s++ {
				rent += bps(rent, t.EscalateBps)
			}
		}
		out = append(out, RentPeriod{Seq: i + 1, PeriodStart: ps, PeriodEnd: pe, DueDate: due, AmountMinor: rent + t.FeesMinor})
	}
	return out
}

// NoticeEffectiveDate applies the policy rule: the legal effective date is
// the later of the requested date and issue date + minimum notice days. An
// app acknowledgement never moves it.
func NoticeEffectiveDate(issued time.Time, minDays int32, requested *time.Time) time.Time {
	eff := day(issued).AddDate(0, 0, int(minDays))
	if requested != nil && day(*requested).After(eff) {
		return day(*requested)
	}
	return eff
}

// Allocation splits an amount by an allocation rule. Shares are basis points
// (PERCENTAGE), fixed minor amounts (FIXED) or meter units (SUB_METER); the
// rounding remainder goes to the first party so totals always reconcile.
type Allocation struct {
	Party       string `json:"party"`
	AmountMinor int64  `json:"amount_minor"`
}

// Share is one party's share in an allocation rule.
type Share struct {
	Party string `json:"party" v:"required,max=64"`
	Value int64  `json:"value" v:"min=0" doc:"PERCENTAGE: basis points; FIXED: minor units; SUB_METER: consumption units; EQUAL: ignored"`
}

// Allocate applies method to amount.
func Allocate(method string, shares []Share, amount int64) ([]Allocation, error) {
	if len(shares) == 0 {
		return nil, invalid("shares", "at least one share is required")
	}
	out := make([]Allocation, len(shares))
	var weights []int64
	switch method {
	case "EQUAL":
		for range shares {
			weights = append(weights, 1)
		}
	case "PERCENTAGE":
		var sum int64
		for _, s := range shares {
			sum += s.Value
			weights = append(weights, s.Value)
		}
		if sum != 10000 {
			return nil, invalid("shares", "percentage shares must total 10000 basis points")
		}
	case "SUB_METER":
		for _, s := range shares {
			weights = append(weights, s.Value)
		}
	case "FIXED":
		var sum int64
		for i, s := range shares {
			out[i] = Allocation{Party: s.Party, AmountMinor: s.Value}
			sum += s.Value
		}
		if sum != amount {
			return nil, apperrors.Unprocessable("ALLOCATION_MISMATCH", "Fixed shares must add up to the bill amount.")
		}
		return out, nil
	default:
		return nil, invalid("method", "unknown allocation method")
	}
	var total int64
	for _, w := range weights {
		total += w
	}
	if total <= 0 {
		return nil, invalid("shares", "shares must have a positive total")
	}
	var assigned int64
	for i, s := range shares {
		a := amount * weights[i] / total
		out[i] = Allocation{Party: s.Party, AmountMinor: a}
		assigned += a
	}
	out[0].AmountMinor += amount - assigned
	return out, nil
}
