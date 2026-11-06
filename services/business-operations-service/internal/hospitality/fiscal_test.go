package hospitality

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

func TestFiscalComponentPartitionConservesEverySourceAndOriginalSlotGross(t *testing.T) {
	random := rand.New(rand.NewPCG(35, 19))
	for quantity := 1; quantity <= 53; quantity++ {
		for trial := 0; trial < 20; trial++ {
			original := make([]int64, 1+random.IntN(30))
			var gross int64
			for i := range original {
				original[i] = random.Int64N(100003)
				gross += original[i]
			}
			reconstructed := make([]int64, len(original))
			for slot := 1; slot <= quantity; slot++ {
				shares, e := ComponentSlotShares(original, quantity, slot)
				if e != nil {
					t.Fatal(e)
				}
				var sum int64
				for i, v := range shares {
					if v < 0 {
						t.Fatal("negative share")
					}
					reconstructed[i] += v
					sum += v
				}
				want := gross / int64(quantity)
				if int64(slot) <= gross%int64(quantity) {
					want++
				}
				if sum != want {
					t.Fatalf("slot conservation: qty%d slot%d got%d want%d", quantity, slot, sum, want)
				}
			}
			for i := range original {
				if reconstructed[i] != original[i] {
					t.Fatalf("source component %d changed: %d/%d", i, reconstructed[i], original[i])
				}
			}
		}
	}
}
func TestFiscalComponentPartitionRejectsOverflowAndInvalidBounds(t *testing.T) {
	for _, in := range []struct {
		amounts        []int64
		quantity, slot int
	}{{[]int64{math.MaxInt64, 1}, 2, 1}, {[]int64{-1}, 1, 1}, {[]int64{1}, 0, 1}, {[]int64{1}, 1001, 1}, {[]int64{1}, 2, 0}, {[]int64{1}, 2, 3}} {
		if _, e := ComponentSlotShares(in.amounts, in.quantity, in.slot); e == nil {
			t.Fatalf("unsafe partition accepted %+v", in)
		}
	}
}

func fiscalFixture(t *testing.T) (revenue.QuoteSnapshot, map[ids.ID]FiscalPolicySource) {
	t.Helper()
	profile, addon := ids.New(), ids.New()
	policy := revenue.TaxPolicy{DisplayMode: "ITEMIZED", FiscalReference: "approved-fiscal-source-v2", PolicyVersion: 1, Taxes: []revenue.TaxComponent{{Code: "VAT", BasisPoints: 1000, Included: true}, {Code: "LOCAL", BasisPoints: 500}}, Fees: []revenue.FeeComponent{{Code: "RESORT", AmountMinor: 1000, Included: true}, {Code: "SERVICE", AmountMinor: 500}}}
	q := revenue.QuoteSnapshot{QuoteID: ids.New(), Quantity: 3, BaseAmountMinor: 12000, DiscountMinor: 1000, AddOnsMinor: 2000, Currency: "NGN", FiscalAllocationVersion: "PROPORTIONAL_PRETAX_V1", AddOnAmounts: []revenue.QuoteAddOnAmount{{AddOnID: addon, Version: 4, Quantity: 2, AmountMinor: 2000, ExecutionReference: "accepted-menu-service-v4"}}, Nights: []revenue.QuoteNight{{Day: "2026-11-06", TaxProfileID: profile, AmountMinor: 6000}, {Day: "2026-11-07", TaxProfileID: profile, AmountMinor: 6000}}}
	for i, n := range q.Nights {
		applied := policy
		if i > 0 {
			applied.Fees = nil
		}
		input, e := money.New(6500, "NGN")
		if e != nil {
			t.Fatal(e)
		}
		final, parts, e := applied.Apply(input, 1)
		if e != nil {
			t.Fatal(e)
		}
		q.AmountMinor += final.AmountMinor
		for _, part := range parts {
			part.Code = n.Day + ":" + part.Code
			q.Components = append(q.Components, part)
		}
	}
	return q, map[ids.ID]FiscalPolicySource{profile: {Version: 2, Policy: policy}}
}

func TestAcceptedFiscalDetailsConserveFrozenTaxesFeesAddOnsAndGross(t *testing.T) {
	q, profiles := fiscalFixture(t)
	totals := map[string]int64{}
	var combinedNet int64
	for slot := 1; slot <= q.Quantity; slot++ {
		breakdown, e := AcceptedFiscalBreakdown(q, profiles, slot)
		if e != nil {
			t.Fatal(e)
		}
		if breakdown.State != "ITEMIZED" || breakdown.FinancialEffect != "DETAIL_ONLY" {
			t.Fatalf("invalid financial claim %+v", breakdown)
		}
		var sum int64
		for _, part := range breakdown.Components {
			sum += part.AmountMinor
			totals[part.Code] += part.AmountMinor
			if part.Kind == "TAX" || part.Kind == "FEE" {
				if part.TaxProfileID == nil || part.SourceVersion != 2 {
					t.Fatal("original immutable fiscal provenance lost")
				}
			} else {
				combinedNet += part.AmountMinor
			}
			if part.Kind == "ADD_ON_NET" && (part.AddOnID == nil || part.SourceVersion != 4 || part.AcceptedQuantity != 2) {
				t.Fatal("charged add-on source lost")
			}
		}
		if sum != breakdown.GrossMinor {
			t.Fatal("detail was charged twice or failed to sum to original gross")
		}
	}
	var included int64
	for _, part := range q.Components {
		if totals[part.Code] != part.AmountMinor {
			t.Fatalf("frozen component changed %+v", part)
		}
		if part.Included {
			included += part.AmountMinor
		}
	}
	if combinedNet != q.BaseAmountMinor-q.DiscountMinor+q.AddOnsMinor-included {
		t.Fatal("included fiscal amounts were not removed from net exactly once")
	}
}

func TestLegacyFiscalDetailsKeepMissingAddOnAttributionExplicit(t *testing.T) {
	q, profiles := fiscalFixture(t)
	q.FiscalAllocationVersion = ""
	q.AddOnAmounts = nil
	detail, e := AcceptedFiscalBreakdown(q, profiles, 1)
	if e != nil {
		t.Fatal(e)
	}
	if detail.State != "LEGACY_AGGREGATE_REVIEW_REQUIRED" || detail.Components[0].Kind != "ACCEPTED_SERVICES_NET" {
		t.Fatalf("legacy source data invented %+v", detail)
	}
	for _, part := range detail.Components {
		if part.AddOnID != nil || part.Kind == "ACCOMMODATION_NET" {
			t.Fatal("unavailable legacy service allocation was invented")
		}
	}
}
func TestFiscalDetailsRejectMissingOrAlteredAcceptedSource(t *testing.T) {
	for _, scenario := range []string{"missing_profile", "missing_tax", "duplicate_code", "altered_fee", "altered_gross", "missing_addon_quantity"} {
		t.Run(scenario, func(t *testing.T) {
			q, profiles := fiscalFixture(t)
			switch scenario {
			case "missing_profile":
				profiles = nil
			case "missing_tax":
				q.Components = q.Components[:len(q.Components)-1]
			case "duplicate_code":
				q.Components = append(q.Components, q.Components[0])
			case "altered_fee":
				q.Components[0].AmountMinor++
			case "altered_gross":
				q.AmountMinor++
			case "missing_addon_quantity":
				q.AddOnAmounts[0].Quantity = 0
			}
			if _, e := AcceptedFiscalBreakdown(q, profiles, 1); e == nil {
				t.Fatal("unreconciled source accepted")
			}
		})
	}
}
