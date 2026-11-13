package finance

import (
	"errors"
	"sort"
)

// SplitShare is one configured allocation. Basis is PERCENT_BPS (value in
// basis points of the amount remaining after fixed shares) or FIXED_MINOR.
type SplitShare struct {
	DestinationID string `json:"destination_id"`
	Basis         string `json:"basis"`
	Value         int64  `json:"value"`
}

// Allocation is one computed exact minor-unit amount.
type Allocation struct {
	DestinationID string `json:"destination_id"`
	AmountMinor   int64  `json:"amount_minor"`
}

var ErrSplitInvalid = errors.New("revenue split rule is invalid")

// ValidateShares enforces allocation conservation at configuration time:
// percentage shares must total exactly 10000 bps; fixed shares are positive;
// destinations are unique.
func ValidateShares(shares []SplitShare) error {
	if len(shares) < 1 || len(shares) > 20 {
		return ErrSplitInvalid
	}
	seen := map[string]bool{}
	var bps int64
	percent := 0
	for _, s := range shares {
		if s.DestinationID == "" || seen[s.DestinationID] {
			return ErrSplitInvalid
		}
		seen[s.DestinationID] = true
		switch s.Basis {
		case "PERCENT_BPS":
			if s.Value <= 0 || s.Value > 10000 {
				return ErrSplitInvalid
			}
			bps += s.Value
			percent++
		case "FIXED_MINOR":
			if s.Value <= 0 || s.Value > 1_000_000_000_000 {
				return ErrSplitInvalid
			}
		default:
			return ErrSplitInvalid
		}
	}
	if percent == 0 || bps != 10000 {
		return ErrSplitInvalid
	}
	return nil
}

// Allocate splits total exactly: fixed shares first, then percentages of the
// remainder using largest-remainder rounding (ties by destination id), so the
// sum of allocations always equals total.
func Allocate(total int64, shares []SplitShare) ([]Allocation, error) {
	if total < 0 || ValidateShares(shares) != nil {
		return nil, ErrSplitInvalid
	}
	out := make([]Allocation, 0, len(shares))
	remaining := total
	for _, s := range shares {
		if s.Basis == "FIXED_MINOR" {
			remaining -= s.Value
			out = append(out, Allocation{DestinationID: s.DestinationID, AmountMinor: s.Value})
		}
	}
	if remaining < 0 {
		return nil, errors.New("fixed shares exceed the amount")
	}
	type part struct {
		id        string
		amount    int64
		remainder int64
	}
	parts := []part{}
	var assigned int64
	for _, s := range shares {
		if s.Basis != "PERCENT_BPS" {
			continue
		}
		amount := remaining * s.Value / 10000
		parts = append(parts, part{s.DestinationID, amount, remaining * s.Value % 10000})
		assigned += amount
	}
	sort.Slice(parts, func(i, j int) bool {
		if parts[i].remainder != parts[j].remainder {
			return parts[i].remainder > parts[j].remainder
		}
		return parts[i].id < parts[j].id
	})
	for i := int64(0); i < remaining-assigned; i++ {
		parts[i%int64(len(parts))].amount++
	}
	for _, p := range parts {
		out = append(out, Allocation{DestinationID: p.id, AmountMinor: p.amount})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DestinationID < out[j].DestinationID })
	return out, nil
}

// NetExpected is the conserved expected channel remittance.
func NetExpected(gross, commission, promotion, fees, tax int64) (int64, error) {
	if gross < 0 || commission < 0 || promotion < 0 || fees < 0 || tax < 0 {
		return 0, errors.New("settlement components must be non-negative")
	}
	net := gross - commission - promotion - fees - tax
	if net < 0 {
		return 0, errors.New("deductions exceed gross")
	}
	return net, nil
}
