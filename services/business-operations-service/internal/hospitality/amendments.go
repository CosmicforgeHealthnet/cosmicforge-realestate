package hospitality

import (
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

// SlotNightAmounts partitions an accepted line amount to one quantity slot
// exactly as check-in does (remainders go to the first slots), then across the
// accepted nights using the frozen nightly weights. The sum equals the slot
// share of the original quote; nothing is repriced.
func SlotNightAmounts(lineAmount int64, currency string, quantity, slot int, nights []revenue.QuoteNight) ([]int64, error) {
	if quantity < 1 || slot < 1 || slot > quantity || len(nights) == 0 || lineAmount < 0 {
		return nil, errors.New("invalid accepted slot partition")
	}
	share := lineAmount / int64(quantity)
	if int64(slot) <= lineAmount%int64(quantity) {
		share++
	}
	weights := make([]int64, len(nights))
	zero := true
	for i, night := range nights {
		weights[i] = night.AmountMinor
		if night.AmountMinor < 0 {
			return nil, errors.New("negative accepted night")
		}
		if night.AmountMinor > 0 {
			zero = false
		}
	}
	if zero {
		for i := range weights {
			weights[i] = 1
		}
	}
	base, e := money.New(share, currency)
	if e != nil {
		return nil, e
	}
	parts, e := base.Allocate(weights...)
	if e != nil {
		return nil, e
	}
	out := make([]int64, len(parts))
	for i, p := range parts {
		out[i] = p.AmountMinor
	}
	return out, nil
}

// DepartureNight is one accepted unit night with its frozen slot amount.
type DepartureNight struct {
	Day         string `json:"day"`
	AmountMinor int64  `json:"amount_minor"`
}

// DeparturePolicy is the frozen reservation policy used for early departure.
type DeparturePolicy struct {
	CheckInLocal           string `json:"check_in_local"`
	Timezone               string `json:"timezone"`
	CancellationHours      int    `json:"cancellation_hours"`
	CancellationPenaltyBPS int64  `json:"cancellation_penalty_bps"`
	PolicyReference        string `json:"policy_reference"`
}

// DepartureTerms is the revised obligation for unused nights. Credit is a
// contractual reduction, never evidence that money was refunded.
type DepartureTerms struct {
	UnusedNights   int       `json:"unused_nights"`
	UnusedMinor    int64     `json:"unused_minor"`
	PenaltyMinor   int64     `json:"penalty_minor"`
	CreditMinor    int64     `json:"credit_minor"`
	PenaltyApplied bool      `json:"penalty_applied"`
	NoticeDeadline time.Time `json:"notice_deadline"`
	FirstUnusedDay string    `json:"first_unused_day"`
}

var ErrNotEarlyDeparture = errors.New("no unused accepted night remains")

// EvaluateEarlyDeparture applies the frozen cancellation terms to the nights
// from the actual departure civil date onwards: inside the notice window the
// penalty share of the unused nights is retained, otherwise all is credited.
func EvaluateEarlyDeparture(nights []DepartureNight, departureDay string, now time.Time, p DeparturePolicy) (DepartureTerms, error) {
	var out DepartureTerms
	if p.CancellationHours < 0 || p.CancellationPenaltyBPS < 0 || p.CancellationPenaltyBPS > 10000 {
		return out, errors.New("invalid frozen departure policy")
	}
	for _, night := range nights {
		if night.Day < departureDay {
			continue
		}
		if out.FirstUnusedDay == "" || night.Day < out.FirstUnusedDay {
			out.FirstUnusedDay = night.Day
		}
		out.UnusedNights++
		out.UnusedMinor += night.AmountMinor
	}
	if out.UnusedNights == 0 {
		return out, ErrNotEarlyDeparture
	}
	start, e := LocalInstant(out.FirstUnusedDay, p.CheckInLocal, p.Timezone)
	if e != nil {
		return out, e
	}
	out.NoticeDeadline = start.Add(-time.Duration(p.CancellationHours) * time.Hour)
	if !now.Before(out.NoticeDeadline) {
		out.PenaltyApplied = true
		out.PenaltyMinor, e = revenue.Ratio(out.UnusedMinor, p.CancellationPenaltyBPS, 10000)
		if e != nil {
			return out, e
		}
	}
	out.CreditMinor = out.UnusedMinor - out.PenaltyMinor
	return out, nil
}

// SplitByWeights allocates an amount across non-negative weights without
// creating or losing minor units. An all-zero weight set returns zeros.
func SplitByWeights(amount int64, currency string, weights []int64) ([]int64, error) {
	out := make([]int64, len(weights))
	total := int64(0)
	for _, w := range weights {
		if w < 0 {
			return nil, errors.New("negative weight")
		}
		total += w
	}
	if amount == 0 || total == 0 {
		if amount != 0 {
			return nil, errors.New("cannot allocate a non-zero amount without weights")
		}
		return out, nil
	}
	base, e := money.New(amount, currency)
	if e != nil {
		return nil, e
	}
	parts, e := base.Allocate(weights...)
	if e != nil {
		return nil, e
	}
	for i, p := range parts {
		out[i] = p.AmountMinor
	}
	return out, nil
}
