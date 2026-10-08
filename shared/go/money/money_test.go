package money

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestParseCurrency(t *testing.T) {
	for _, ok := range []string{"NGN", "USD", "EUR", "JPY", "KWD"} {
		if _, err := ParseCurrency(ok); err != nil {
			t.Errorf("%s should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ngn", "NG", "NGNN", "XXX", "ABC", "XAU"} {
		if _, err := ParseCurrency(bad); !errors.Is(err, ErrInvalidCurrency) {
			t.Errorf("%q should be invalid", bad)
		}
	}
	if MustNew(1, "JPY").Currency.MinorUnits() != 0 || MustNew(1, "KWD").Currency.MinorUnits() != 3 {
		t.Fatal("minor units wrong")
	}
}

func TestArithmetic(t *testing.T) {
	a := MustNew(2500050, "NGN")
	b := MustNew(50, "NGN")
	sum, err := a.Add(b)
	if err != nil || sum.AmountMinor != 2500100 {
		t.Fatalf("add: %v %v", sum, err)
	}
	diff, err := a.Sub(b)
	if err != nil || diff.AmountMinor != 2500000 {
		t.Fatalf("sub: %v %v", diff, err)
	}
	prod, err := b.MulInt(3)
	if err != nil || prod.AmountMinor != 150 {
		t.Fatalf("mul: %v %v", prod, err)
	}
	if c, _ := a.Cmp(b); c != 1 {
		t.Fatal("cmp")
	}
}

func TestMixedCurrencyRejected(t *testing.T) {
	_, err := MustNew(1, "NGN").Add(MustNew(1, "USD"))
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("expected mismatch, got %v", err)
	}
	if _, err := MustNew(1, "NGN").Sub(MustNew(1, "USD")); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatal("sub mismatch")
	}
	if _, err := MustNew(1, "NGN").Cmp(MustNew(1, "USD")); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatal("cmp mismatch")
	}
	if _, err := (Money{AmountMinor: 1}).Add(Money{AmountMinor: 1}); !errors.Is(err, ErrInvalidCurrency) {
		t.Fatal("zero-currency money must not be usable in arithmetic")
	}
}

func TestOverflow(t *testing.T) {
	max := MustNew(math.MaxInt64, "NGN")
	if _, err := max.Add(MustNew(1, "NGN")); !errors.Is(err, ErrOverflow) {
		t.Fatal("expected add overflow")
	}
	min := MustNew(math.MinInt64, "NGN")
	if _, err := min.Sub(MustNew(1, "NGN")); !errors.Is(err, ErrOverflow) {
		t.Fatal("expected sub overflow")
	}
	if _, err := min.Neg(); !errors.Is(err, ErrOverflow) {
		t.Fatal("expected neg overflow")
	}
	if _, err := max.MulInt(2); !errors.Is(err, ErrOverflow) {
		t.Fatal("expected mul overflow")
	}
	if _, err := MustNew(-1, "NGN").MulInt(math.MinInt64); !errors.Is(err, ErrOverflow) {
		t.Fatal("expected mul overflow on -1*min")
	}
	if _, err := MustNew(1, "NGN").Sub(MustNew(math.MinInt64, "NGN")); !errors.Is(err, ErrOverflow) {
		t.Fatal("expected overflow subtracting MinInt64")
	}
}

func TestAllocateConservesValue(t *testing.T) {
	m := MustNew(100, "NGN")
	parts, err := m.Allocate(1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := []int64{parts[0].AmountMinor, parts[1].AmountMinor, parts[2].AmountMinor}
	if !reflect.DeepEqual(got, []int64{34, 33, 33}) {
		t.Fatalf("allocation %v", got)
	}
	neg, _ := MustNew(-100, "NGN").Allocate(70, 30)
	if neg[0].AmountMinor != -70 || neg[1].AmountMinor != -30 {
		t.Fatalf("negative allocation %v", neg)
	}
	big, err := MustNew(math.MaxInt64, "NGN").Allocate(3, 7)
	if err != nil || big[0].AmountMinor+big[1].AmountMinor != math.MaxInt64 {
		t.Fatalf("large allocation lost value: %v %v", big, err)
	}
	skip, _ := MustNew(5, "NGN").Allocate(0, 1, 1)
	if skip[0].AmountMinor != 0 || skip[1].AmountMinor+skip[2].AmountMinor != 5 {
		t.Fatalf("zero ratio must receive nothing: %v", skip)
	}
	if _, err := m.Allocate(); !errors.Is(err, ErrInvalidRatios) {
		t.Fatal("empty ratios")
	}
	if _, err := m.Allocate(0, 0); !errors.Is(err, ErrInvalidRatios) {
		t.Fatal("zero total")
	}
	if _, err := m.Allocate(-1, 2); !errors.Is(err, ErrInvalidRatios) {
		t.Fatal("negative ratio")
	}
}

func TestJSON(t *testing.T) {
	m := MustNew(2500050, "NGN")
	b, err := json.Marshal(m)
	if err != nil || string(b) != `{"amount_minor":2500050,"currency":"NGN"}` {
		t.Fatalf("marshal %s %v", b, err)
	}
	var back Money
	if err := json.Unmarshal(b, &back); err != nil || back != m {
		t.Fatalf("roundtrip %v %v", back, err)
	}
	bad := []string{
		`{"amount_minor":25000.50,"currency":"NGN"}`,
		`{"amount_minor":2.5e6,"currency":"NGN"}`,
		`{"amount_minor":"2500050","currency":"NGN"}`,
		`{"amount_minor":2500050,"currency":"ngn"}`,
		`{"amount_minor":2500050}`,
		`{"currency":"NGN"}`,
		`{"amount_minor":2500050,"currency":"NGN","amount":1}`,
		`{"amount_minor":99999999999999999999,"currency":"NGN"}`,
	}
	for _, s := range bad {
		var x Money
		if err := json.Unmarshal([]byte(s), &x); err == nil {
			t.Errorf("expected error for %s", s)
		}
	}
}

func TestString(t *testing.T) {
	cases := map[Money]string{
		MustNew(2500050, "NGN"): "NGN 25000.50",
		MustNew(5, "NGN"):       "NGN 0.05",
		MustNew(-5, "USD"):      "USD -0.05",
		MustNew(1500, "JPY"):    "JPY 1500",
		MustNew(1234, "KWD"):    "KWD 1.234",
	}
	for m, want := range cases {
		if m.String() != want {
			t.Errorf("%v.String() = %q want %q", m.AmountMinor, m.String(), want)
		}
	}
}

func TestNoFloatFields(t *testing.T) {
	typ := reflect.TypeOf(Money{})
	for i := 0; i < typ.NumField(); i++ {
		k := typ.Field(i).Type.Kind()
		if k == reflect.Float32 || k == reflect.Float64 {
			t.Fatalf("field %s is floating point", typ.Field(i).Name)
		}
	}
}
