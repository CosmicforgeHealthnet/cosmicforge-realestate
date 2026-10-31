package revenue

import (
	"math"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

func validRateDefinition() RateDefinition {
	return RateDefinition{Currency: "NGN", AmountMinor: 10000, IncludedAdults: 1, ExtraAdultMinor: 1500, ChildMinor: 500, MinGuests: 1, MaxGuests: 4, MinNights: 1, MaxNights: 30, CancellationReference: "REFUNDABLE:v1", PrepaymentReference: "NO_PREPAY:v1", PolicyCode: "STANDARD", PolicyVersion: 1, FloorMinor: 0, CeilingMinor: 1000000, MaxChangeBPS: 1000}
}
func TestExactDerivedArithmeticRoundingAndOverflow(t *testing.T) {
	for _, c := range []struct{ a, n, d, w int64 }{{1, 5000, 10000, 1}, {-1, 5000, 10000, -1}, {101, 1250, 10000, 13}, {100000, 3333, 10000, 33330}, {math.MaxInt64, 10000, 10000, math.MaxInt64}} {
		got, err := Ratio(c.a, c.n, c.d)
		if err != nil || got != c.w {
			t.Fatalf("ratio %+v =%d %v", c, got, err)
		}
	}
	if _, err := Ratio(math.MaxInt64, 2, 1); err == nil {
		t.Fatal("overflow accepted")
	}
	for _, currency := range []string{"JPY", "NGN", "KWD"} {
		base := money.MustNew(10000, currency)
		got, e := Adjust(base, []Adjustment{{Kind: "PERCENT", BasisPoints: 1250}, {Kind: "FIXED", AmountMinor: -250}})
		if e != nil || got.AmountMinor != 11000 || got.Currency != base.Currency {
			t.Fatalf("exact currency %s %+v %v", currency, got, e)
		}
	}
	if _, e := Adjust(money.MustNew(100, "NGN"), []Adjustment{{Kind: "FIXED", AmountMinor: -101}}); e == nil {
		t.Fatal("negative price accepted")
	}
	if _, e := Adjust(money.MustNew(math.MaxInt64, "NGN"), []Adjustment{{Kind: "FIXED", AmountMinor: 1}}); e == nil {
		t.Fatal("overflowing price accepted")
	}
}
func TestOccupancyLengthOfStaySeasonalAndChannelPricing(t *testing.T) {
	d := validRateDefinition()
	d.Rules = []Rule{{Priority: 1, DateFrom: "2026-11-01", DateTo: "2026-11-20", MinNights: 3, Channel: "COSMICFORGE_MARKETPLACE", Adjustment: Adjustment{Kind: "PERCENT", BasisPoints: -1000}}, {Priority: 2, DateFrom: "2026-11-01", DateTo: "2026-11-20", Channel: "COSMICFORGE_MARKETPLACE", Adjustment: Adjustment{Kind: "FIXED", AmountMinor: 100}}}
	got, err := Nightly(d, money.MustNew(10000, "NGN"), "2026-11-02", "COSMICFORGE_MARKETPLACE", 3, 2, 1)
	if err != nil || got.AmountMinor != 10900 {
		t.Fatalf("occupancy/LOS precedence %+v %v", got, err)
	}
	for _, values := range [][3]int{{0, 1, 3}, {5, 0, 3}, {1, 0, 31}} {
		if _, e := Nightly(d, money.MustNew(10000, "NGN"), "2026-11-02", "", values[2], values[0], values[1]); e == nil {
			t.Fatalf("impossible occupancy/stay %v", values)
		}
	}
	d.Rules = append(d.Rules, d.Rules[0])
	if d.Validate() == nil {
		t.Fatal("ambiguous rule priority accepted")
	}
}
func TestTaxFeeProvenanceAndInclusiveMinorUnits(t *testing.T) {
	p := TaxPolicy{DisplayMode: "ITEMIZED", FiscalReference: "TEST-POLICY-NOT-LEGAL-TAX-ADVICE", PolicyVersion: 1, Taxes: []TaxComponent{{Code: "TAX_A", BasisPoints: 500, Included: true}, {Code: "TAX_B", BasisPoints: 500, Included: true}, {Code: "TAX_C", BasisPoints: 1000}}, Fees: []FeeComponent{{Code: "FIXED_SERVICE", AmountMinor: 10, PerNight: true}}}
	total, components, err := p.Apply(money.MustNew(111, "JPY"), 2)
	if err != nil || total.AmountMinor != 141 {
		t.Fatalf("inclusive/exclusive breakdown %+v %v %v", total, components, err)
	}
	included := int64(0)
	for _, c := range components {
		if c.Included {
			included += c.AmountMinor
		}
	}
	if included != 10 {
		t.Fatalf("included residual lost %d", included)
	}
	p.FiscalReference = ""
	if _, _, err = p.Apply(money.MustNew(100, "NGN"), 1); err == nil {
		t.Fatal("unattributed tax policy accepted")
	}
}
func TestHardRestrictionsCivilBookingWindowsCTAAndCTD(t *testing.T) {
	r := inventory.DateRange{DateFrom: "2026-11-02", DateTo: "2026-11-05"}
	now := time.Date(2026, 11, 1, 23, 30, 0, 0, time.UTC)
	records := []RestrictionRecord{{DateFrom: "2026-11-01", DateTo: "2026-12-01", Definition: Restriction{MinNights: 2, MaxNights: 4, MaxAdvanceDays: 30, MaxGuests: 3}}}
	if err := CheckRestrictions(records, r, "Europe/Rome", 2, 1, now); err != nil {
		t.Fatal(err)
	}
	for _, rule := range []Restriction{{StopSell: true}, {ClosedToArrival: true}, {ClosedToDeparture: true}, {MinNights: 4}, {MinAdvanceDays: 1}, {MaxGuests: 2}, {CutoffLocal: "00:30"}} {
		copy := append([]RestrictionRecord{}, records...)
		copy = append(copy, RestrictionRecord{DateFrom: "2026-11-01", DateTo: "2026-12-01", Definition: rule})
		if e := CheckRestrictions(copy, r, "Europe/Rome", 2, 1, now); e == nil {
			t.Fatalf("restriction bypass %+v", rule)
		}
	}
	if _, err := Intersect([]Restriction{{MinNights: 5}, {MaxNights: 3}}); err == nil {
		t.Fatal("contradictory overlap accepted")
	}
}
