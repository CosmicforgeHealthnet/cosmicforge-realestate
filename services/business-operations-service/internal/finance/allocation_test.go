package finance

import (
	"math/rand"
	"testing"
)

func TestAllocateConservesEveryMinorUnit(t *testing.T) {
	shares := []SplitShare{{"a", "PERCENT_BPS", 3333}, {"b", "PERCENT_BPS", 3333}, {"c", "PERCENT_BPS", 3334}, {"d", "FIXED_MINOR", 500}}
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 2000; i++ {
		total := 500 + r.Int63n(10_000_000)
		out, err := Allocate(total, shares)
		if err != nil {
			t.Fatal(err)
		}
		var sum int64
		for _, a := range out {
			sum += a.AmountMinor
		}
		if sum != total {
			t.Fatalf("total %d allocated %d", total, sum)
		}
	}
	if _, err := Allocate(100, shares); err == nil {
		t.Fatal("fixed share exceeding amount accepted")
	}
}

func TestValidateSharesRejectsNonConservingRules(t *testing.T) {
	for _, bad := range [][]SplitShare{
		{{"a", "PERCENT_BPS", 6000}, {"b", "PERCENT_BPS", 3000}},
		{{"a", "PERCENT_BPS", 10000}, {"a", "PERCENT_BPS", 0}},
		{{"a", "FIXED_MINOR", 100}},
		{{"a", "OTHER", 10000}},
	} {
		if ValidateShares(bad) == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if n, err := NetExpected(10000, 1500, 500, 200, 0); err != nil || n != 7800 {
		t.Fatal(n, err)
	}
	if _, err := NetExpected(100, 200, 0, 0, 0); err == nil {
		t.Fatal("negative net accepted")
	}
}
