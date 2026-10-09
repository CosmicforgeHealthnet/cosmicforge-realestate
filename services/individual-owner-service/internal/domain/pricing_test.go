package domain

import (
	"testing"
	"time"
)

func i64(v int64) *int64 { return &v }
func i32(v int32) *int32 { return &v }

var cats = []string{"AGENCY", "LEGAL", "CAUTION", "SERVICE_CHARGE", "CLEANING"}

func TestLeaseCostIsDeterministicWithFeesEscalationAndDiscount(t *testing.T) {
	m := PriceModel{TransactionType: "RENT_LONG_TERM", Currency: "NGN", Base: 3_600_000_00, BasePeriod: "YEAR", Cycle: "ANNUAL",
		Deposit: 500_000_00, MinTermMonths: 12,
		Fees: []Fee{
			{Code: "agency", Category: "AGENCY", Label: "Agency fee", Calculation: "PERCENT_OF_BASE", RateBps: i32(1000), Frequency: "ONE_TIME", Mandatory: true, PayableTo: "THIRD_PARTY"},
			{Code: "legal", Category: "LEGAL", Label: "Legal fee", Calculation: "PERCENT_OF_BASE", RateBps: i32(500), Frequency: "ONE_TIME", Mandatory: true, PayableTo: "THIRD_PARTY"},
			{Code: "service", Category: "SERVICE_CHARGE", Label: "Service charge", Calculation: "FIXED", AmountMinor: i64(200_000_00), Frequency: "PER_CYCLE", Mandatory: true, PayableTo: "OWNER"},
			{Code: "cleaning", Category: "CLEANING", Label: "Move-in cleaning", Calculation: "FIXED", AmountMinor: i64(50_000_00), Frequency: "ONE_TIME", Mandatory: false, PayableTo: "THIRD_PARTY"},
		},
		Rules: []Rule{{Type: "ESCALATION", Params: RuleParams{AfterMonths: 12, RateBps: 1000}}},
	}
	if err := ValidatePriceModel(m, cats); err != nil {
		t.Fatal(err)
	}
	c, err := Calculate(m, Scenario{TermMonths: 24})
	if err != nil {
		t.Fatal(err)
	}
	// Upfront: rent 3.6m + deposit 0.5m + agency 360k + legal 180k + service 200k.
	if c.UpfrontTotal != 4_840_000_00 || c.RefundableTotal != 500_000_00 || c.CycleCount != 2 {
		t.Fatalf("upfront %+v", c)
	}
	// Term: year1 3.6m + year2 3.96m (10% escalation) + fees 540k + service 2×200k.
	if c.TermTotal != 3_600_000_00+3_960_000_00+540_000_00+400_000_00 {
		t.Fatalf("term total %d", c.TermTotal)
	}
	if len(c.OptionalLines) != 1 {
		t.Fatalf("optional %+v", c.OptionalLines)
	}
	again, _ := Calculate(m, Scenario{TermMonths: 24})
	if again.TermTotal != c.TermTotal || again.UpfrontTotal != c.UpfrontTotal {
		t.Fatal("calculator is not deterministic")
	}
	if _, err := Calculate(m, Scenario{TermMonths: 6}); err == nil {
		t.Fatal("term below minimum accepted")
	}
}

func TestMonthlyCycleFromYearlyPriceRoundsHalfUp(t *testing.T) {
	m := PriceModel{TransactionType: "RENT_LONG_TERM", Currency: "NGN", Base: 1_000_001, BasePeriod: "YEAR", Cycle: "MONTHLY"}
	c, err := Calculate(m, Scenario{TermMonths: 12})
	if err != nil {
		t.Fatal(err)
	}
	if c.RecurringPerCycle != 83_333 || c.CycleCount != 12 {
		t.Fatalf("monthly %+v", c)
	}
}

func TestShortLetSeasonWeekendLongStay(t *testing.T) {
	m := PriceModel{TransactionType: "SHORT_LET", Currency: "NGN", Base: 50_000_00, BasePeriod: "NIGHT", Cycle: "PER_STAY", Deposit: 100_000_00, MinNights: 2,
		Fees: []Fee{{Code: "cleaning", Category: "CLEANING", Label: "Cleaning", Calculation: "FIXED", AmountMinor: i64(15_000_00), Frequency: "PER_STAY", Mandatory: true, PayableTo: "OWNER"}},
		Rules: []Rule{{Type: "WEEKEND", Params: RuleParams{Days: []string{"FRI", "SAT"}, RateBps: 2000}},
			{Type: "SEASONAL", Params: RuleParams{From: "12-20", To: "01-05", RateBps: 5000}},
			{Type: "LONG_STAY", Params: RuleParams{MinNights: 7, RateBps: 1000}}}}
	if err := ValidatePriceModel(m, cats); err != nil {
		t.Fatal(err)
	}
	// Thu 2026-12-31 → Sun 2027-01-03: Thu (season), Fri (season+weekend), Sat (season+weekend).
	c, err := Calculate(m, Scenario{CheckIn: time.Date(2026, 12, 31, 14, 0, 0, 0, time.UTC), CheckOut: time.Date(2027, 1, 3, 11, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	want := int64(75_000_00 + 85_000_00 + 85_000_00)
	if c.Nights != 3 || c.Lines[0].AmountMinor != want || c.TermTotal != want+15_000_00 || c.UpfrontTotal != want+15_000_00+100_000_00 {
		t.Fatalf("short-let %+v", c)
	}
	if _, err := Calculate(m, Scenario{CheckIn: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), CheckOut: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)}); err == nil {
		t.Fatal("below min nights accepted")
	}
}

func TestShortLetExtensionPricesOnlyAddedNights(t *testing.T) {
	m := PriceModel{TransactionType: "SHORT_LET", Currency: "NGN", Base: 50_000_00, BasePeriod: "NIGHT", Cycle: "PER_STAY", Deposit: 100_000_00, MinNights: 2,
		Fees: []Fee{{Code: "cleaning", Category: "CLEANING", Label: "Cleaning", Calculation: "FIXED", AmountMinor: i64(15_000_00), Frequency: "PER_STAY", Mandatory: true, PayableTo: "OWNER"},
			{Code: "levy", Category: "SERVICE", Label: "Levy", Calculation: "FIXED", AmountMinor: i64(1_000_00), Frequency: "PER_NIGHT", Mandatory: true, PayableTo: "OWNER"}}}
	// One added night (Mon): below the stay minimum, without per-stay fees or deposit.
	c, err := Calculate(m, Scenario{CheckIn: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), CheckOut: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC), Extension: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.Nights != 1 || c.UpfrontTotal != 50_000_00+1_000_00 || c.RefundableTotal != 0 || len(c.Lines) != 2 {
		t.Fatalf("extension %+v", c)
	}
	// The same dates as a new stay are refused (minimum) and would carry fees.
	if _, err := Calculate(m, Scenario{CheckIn: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), CheckOut: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)}); err == nil {
		t.Fatal("new one-night stay accepted below the minimum")
	}
}

func TestPriceModelValidation(t *testing.T) {
	bad := []PriceModel{
		{TransactionType: "SALE", Currency: "NGN", Base: 1, BasePeriod: "YEAR", Cycle: "ONE_TIME"},
		{TransactionType: "SHORT_LET", Currency: "NGN", Base: 1, BasePeriod: "NIGHT", Cycle: "MONTHLY"},
		{TransactionType: "RENT_LONG_TERM", Currency: "NGN", Base: 1, BasePeriod: "YEAR", Cycle: "ANNUAL",
			Fees: []Fee{{Code: "x", Category: "MYSTERY", Label: "x", Calculation: "FIXED", AmountMinor: i64(1), Frequency: "ONE_TIME"}}},
		{TransactionType: "RENT_LONG_TERM", Currency: "NGN", Base: 1, BasePeriod: "YEAR", Cycle: "ANNUAL",
			Fees: []Fee{{Code: "x", Category: "LEGAL", Label: "x", Calculation: "PERCENT_OF_BASE", AmountMinor: i64(1), Frequency: "ONE_TIME"}}},
		{TransactionType: "RENT_LONG_TERM", Currency: "NGN", Base: 1, BasePeriod: "YEAR", Cycle: "ANNUAL", Rules: []Rule{{Type: "WEEKEND"}}},
		{TransactionType: "RENT_LONG_TERM", Currency: "NGN", Base: 0, BasePeriod: "YEAR", Cycle: "ANNUAL"},
	}
	for i, m := range bad {
		if ValidatePriceModel(m, cats) == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}
