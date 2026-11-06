package hospitality

import (
	"errors"
	"math"
	"strings"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

var ErrFiscalSource = errors.New("accepted fiscal source does not reconcile")

type FiscalPolicySource struct {
	Version int64
	Policy  revenue.TaxPolicy
}

type FiscalComponent struct {
	Kind             string  `json:"kind"`
	Code             string  `json:"code"`
	AmountMinor      int64   `json:"amount_minor"`
	Included         bool    `json:"included"`
	TaxProfileID     *ids.ID `json:"tax_profile_id,omitempty"`
	AddOnID          *ids.ID `json:"add_on_id,omitempty"`
	SourceVersion    int64   `json:"source_version,omitempty"`
	AcceptedQuantity int     `json:"accepted_quantity,omitempty"`
	SourceReference  string  `json:"source_reference"`
}
type FiscalBreakdown struct {
	QuoteID          ids.ID            `json:"quote_id"`
	State            string            `json:"state"`
	AllocationMethod string            `json:"allocation_method"`
	OriginalQuantity int               `json:"original_quantity"`
	Slot             int               `json:"slot"`
	GrossMinor       int64             `json:"gross_minor"`
	Currency         string            `json:"currency"`
	FinancialEffect  string            `json:"financial_effect"`
	Components       []FiscalComponent `json:"components"`
}

// ComponentSlotShares partitions both dimensions exactly: each component's
// total is conserved across slots, and each slot retains its original gross
// quotient/remainder amount. Consecutive cyclic residuals avoid independent
// rounding adding extra cents to some folios. Runtime is O(components).
func ComponentSlotShares(amounts []int64, quantity, slot int) ([]int64, error) {
	if quantity < 1 || quantity > 1000 || slot < 1 || slot > quantity || len(amounts) > 20000 {
		return nil, ErrFiscalSource
	}
	out := make([]int64, len(amounts))
	cursor := int64(0)
	var sum int64
	q := int64(quantity)
	for i, amount := range amounts {
		if amount < 0 || sum > math.MaxInt64-amount {
			return nil, ErrFiscalSource
		}
		sum += amount
		out[i] = amount / q
		remaining := amount % q
		if (int64(slot-1)-cursor+q)%q < remaining {
			out[i]++
		}
		cursor = (cursor + remaining) % q
	}
	return out, nil
}

// AcceptedFiscalBreakdown uses frozen accepted amounts and original immutable
// fiscal definitions. It never reprices, charges tax twice or infers missing
// legacy add-on quantities. DETAIL_ONLY components partition one gross posting.
func AcceptedFiscalBreakdown(q revenue.QuoteSnapshot, profiles map[ids.ID]FiscalPolicySource, slot int) (FiscalBreakdown, error) {
	out := FiscalBreakdown{QuoteID: q.QuoteID, State: "ITEMIZED", AllocationMethod: "CYCLIC_RESIDUAL_V1", OriginalQuantity: q.Quantity, Slot: slot, Currency: q.Currency, FinancialEffect: "DETAIL_ONLY", Components: []FiscalComponent{}}
	if q.QuoteID.ValidateV7() != nil || q.BaseAmountMinor < 0 || q.DiscountMinor < 0 || q.DiscountMinor > q.BaseAmountMinor || q.AddOnsMinor < 0 || q.AmountMinor < 0 || len(q.Nights) == 0 {
		return out, ErrFiscalSource
	}
	netBase := q.BaseAmountMinor - q.DiscountMinor
	if netBase > math.MaxInt64-q.AddOnsMinor {
		return out, ErrFiscalSource
	}
	pretax := netBase + q.AddOnsMinor
	net := []FiscalComponent{{Kind: "ACCOMMODATION_NET", Code: "ACCOMMODATION_NET", AmountMinor: netBase, SourceReference: q.QuoteID.String()}}
	if q.FiscalAllocationVersion != "" && q.FiscalAllocationVersion != "PROPORTIONAL_PRETAX_V1" {
		return out, ErrFiscalSource
	}
	if q.FiscalAllocationVersion == "PROPORTIONAL_PRETAX_V1" && q.AddOnsMinor > 0 && len(q.AddOnAmounts) == 0 {
		return out, ErrFiscalSource
	}
	if q.AddOnsMinor > 0 {
		if q.FiscalAllocationVersion == "PROPORTIONAL_PRETAX_V1" && len(q.AddOnAmounts) > 0 {
			var total int64
			seen := map[ids.ID]bool{}
			for _, a := range q.AddOnAmounts {
				if a.AddOnID.ValidateV7() != nil || a.Version < 1 || seen[a.AddOnID] || a.AmountMinor < 0 || a.Quantity < 0 || a.Quantity > 100 || a.Included && (a.AmountMinor != 0 || a.Quantity != 0) || !a.Included && a.Quantity < 1 || (len(a.ExecutionReference) < 1 || len(a.ExecutionReference) > 200 || strings.ContainsAny(a.ExecutionReference, "\x00\r\n")) {
					return out, ErrFiscalSource
				}
				seen[a.AddOnID] = true
				if total > math.MaxInt64-a.AmountMinor {
					return out, ErrFiscalSource
				}
				total += a.AmountMinor
				if a.AmountMinor == 0 {
					continue
				}
				id := a.AddOnID
				net = append(net, FiscalComponent{Kind: "ADD_ON_NET", Code: "ADD_ON:" + id.String(), AmountMinor: a.AmountMinor, AddOnID: &id, SourceVersion: a.Version, AcceptedQuantity: a.Quantity, SourceReference: a.ExecutionReference})
			}
			if total != q.AddOnsMinor {
				return out, ErrFiscalSource
			}
		} else {
			// Original records lack an authoritative per-add-on allocation. Preserve
			// known combined net, taxes and fees; require explicit reviewed detail.
			net = []FiscalComponent{{Kind: "ACCEPTED_SERVICES_NET", Code: "LEGACY_COMBINED_NET", AmountMinor: pretax, SourceReference: q.QuoteID.String()}}
			out.State = "LEGACY_AGGREGATE_REVIEW_REQUIRED"
		}
	}
	byDay := map[string]ids.ID{}
	for _, night := range q.Nights {
		if _, exists := byDay[night.Day]; exists {
			return out, ErrFiscalSource
		}
		source, exists := profiles[night.TaxProfileID]
		if !exists || source.Version < 1 || source.Policy.Validate() != nil {
			return out, ErrFiscalSource
		}
		byDay[night.Day] = night.TaxProfileID
	}
	var included, extra int64
	seenCodes := map[string]bool{}
	taxes := []FiscalComponent{}
	for _, c := range q.Components {
		day, code, found := strings.Cut(c.Code, ":")
		profileID, exists := byDay[day]
		if !found || !exists || seenCodes[c.Code] || c.AmountMinor < 0 {
			return out, ErrFiscalSource
		}
		seenCodes[c.Code] = true
		source := profiles[profileID]
		policy := source.Policy
		kind := ""
		sourceIncluded := false
		for _, t := range policy.Taxes {
			if t.Code == code {
				kind = "TAX"
				sourceIncluded = t.Included
			}
		}
		for _, f := range policy.Fees {
			if f.Code == code {
				if !f.PerNight && day != q.Nights[0].Day || c.AmountMinor != f.AmountMinor {
					return out, ErrFiscalSource
				}
				kind = "FEE"
				sourceIncluded = f.Included
			}
		}
		if kind == "" || sourceIncluded != c.Included {
			return out, ErrFiscalSource
		}
		if c.Included {
			if included > math.MaxInt64-c.AmountMinor {
				return out, ErrFiscalSource
			}
			included += c.AmountMinor
		} else {
			if extra > math.MaxInt64-c.AmountMinor {
				return out, ErrFiscalSource
			}
			extra += c.AmountMinor
		}
		id := profileID
		taxes = append(taxes, FiscalComponent{Kind: kind, Code: c.Code, AmountMinor: c.AmountMinor, Included: c.Included, TaxProfileID: &id, SourceVersion: source.Version, SourceReference: policy.FiscalReference})
	}
	for i, night := range q.Nights {
		policy := profiles[night.TaxProfileID].Policy
		for _, t := range policy.Taxes {
			if !seenCodes[night.Day+":"+t.Code] {
				return out, ErrFiscalSource
			}
		}
		for _, f := range policy.Fees {
			if (f.PerNight || i == 0) && !seenCodes[night.Day+":"+f.Code] {
				return out, ErrFiscalSource
			}
		}
	}
	if included > pretax || pretax > math.MaxInt64-extra || pretax+extra != q.AmountMinor {
		return out, ErrFiscalSource
	}
	remaining := pretax - included
	if pretax > 0 {
		weights := make([]int64, len(net))
		for i := range net {
			weights[i] = net[i].AmountMinor
		}
		m, e := money.New(remaining, q.Currency)
		if e != nil {
			return out, e
		}
		allocated, e := m.Allocate(weights...)
		if e != nil {
			return out, e
		}
		for i := range net {
			net[i].AmountMinor = allocated[i].AmountMinor
		}
	} else if _, e := money.ParseCurrency(q.Currency); e != nil {
		return out, e
	}
	out.Components = append(net, taxes...)
	amounts := make([]int64, len(out.Components))
	for i := range out.Components {
		amounts[i] = out.Components[i].AmountMinor
	}
	shares, e := ComponentSlotShares(amounts, q.Quantity, slot)
	if e != nil {
		return out, e
	}
	for i := range out.Components {
		out.Components[i].AmountMinor = shares[i]
		out.GrossMinor += shares[i]
	}
	expected := q.AmountMinor / int64(q.Quantity)
	if int64(slot) <= q.AmountMinor%int64(q.Quantity) {
		expected++
	}
	if out.GrossMinor != expected {
		return out, ErrFiscalSource
	}
	// The method is part of the retained source contract, including the version
	// of net attribution; original source quantities describe the whole booking.
	if q.FiscalAllocationVersion != "" {
		out.AllocationMethod = q.FiscalAllocationVersion + "/" + out.AllocationMethod
	}
	return out, nil
}
