package hospitality

import (
	"errors"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
)

func TestSlotNightAmountsConservesAcceptedSlotShare(t *testing.T) {
	nights := []revenue.QuoteNight{{Day: "2026-12-01", AmountMinor: 20001}, {Day: "2026-12-02", AmountMinor: 20000}, {Day: "2026-12-03", AmountMinor: 20000}}
	total := int64(0)
	for slot := 1; slot <= 3; slot++ {
		parts, e := SlotNightAmounts(60001, "NGN", 3, slot, nights)
		if e != nil {
			t.Fatal(e)
		}
		for _, p := range parts {
			total += p
		}
	}
	if total != 60001 {
		t.Fatalf("slot partition total %d", total)
	}
	if _, e := SlotNightAmounts(100, "NGN", 2, 3, nights); e == nil {
		t.Fatal("slot above quantity accepted")
	}
	zero, e := SlotNightAmounts(3, "NGN", 1, 1, []revenue.QuoteNight{{Day: "a"}, {Day: "b"}, {Day: "c"}})
	if e != nil || zero[0]+zero[1]+zero[2] != 3 {
		t.Fatalf("zero-weight nights %v %v", zero, e)
	}
}

func TestEvaluateEarlyDepartureUsesFrozenNoticeWindow(t *testing.T) {
	nights := []DepartureNight{{Day: "2026-12-01", AmountMinor: 10000}, {Day: "2026-12-02", AmountMinor: 10000}, {Day: "2026-12-03", AmountMinor: 12000}}
	policy := DeparturePolicy{CheckInLocal: "14:00", Timezone: "Africa/Lagos", CancellationHours: 24, CancellationPenaltyBPS: 5000}
	// Departing on the morning of the 2nd: the night of the 2nd starts within
	// 24 hours, so half of the two unused nights is retained.
	now := time.Date(2026, 12, 2, 8, 0, 0, 0, time.UTC)
	terms, e := EvaluateEarlyDeparture(nights, "2026-12-02", now, policy)
	if e != nil {
		t.Fatal(e)
	}
	if terms.UnusedNights != 2 || terms.UnusedMinor != 22000 || terms.PenaltyMinor != 11000 || terms.CreditMinor != 11000 || !terms.PenaltyApplied || terms.FirstUnusedDay != "2026-12-02" {
		t.Fatalf("late notice terms %+v", terms)
	}
	// Notice two days ahead of the first unused night: everything is credited.
	early := time.Date(2026, 11, 30, 8, 0, 0, 0, time.UTC)
	terms, e = EvaluateEarlyDeparture(nights, "2026-12-03", early, policy)
	if e != nil || terms.UnusedNights != 1 || terms.PenaltyMinor != 0 || terms.CreditMinor != 12000 || terms.PenaltyApplied {
		t.Fatalf("timely notice terms %+v %v", terms, e)
	}
	if _, e = EvaluateEarlyDeparture(nights, "2026-12-04", now, policy); !errors.Is(e, ErrNotEarlyDeparture) {
		t.Fatalf("departure on the accepted checkout date is not early: %v", e)
	}
	if _, e = EvaluateEarlyDeparture(nights, "2026-12-02", now, DeparturePolicy{CheckInLocal: "14:00", Timezone: "Africa/Lagos", CancellationPenaltyBPS: 10001}); e == nil {
		t.Fatal("invalid frozen penalty accepted")
	}
}

func TestSplitByWeightsConservesAmount(t *testing.T) {
	parts, e := SplitByWeights(10001, "NGN", []int64{3, 0, 7})
	if e != nil || parts[0]+parts[1]+parts[2] != 10001 || parts[1] != 0 {
		t.Fatalf("split %v %v", parts, e)
	}
	if parts, e = SplitByWeights(0, "NGN", []int64{0, 0}); e != nil || parts[0] != 0 {
		t.Fatalf("zero split %v %v", parts, e)
	}
	if _, e = SplitByWeights(5, "NGN", []int64{0}); e == nil {
		t.Fatal("non-zero amount without weights accepted")
	}
}
