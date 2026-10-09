package domain

import (
	"testing"
	"time"
)

func date(s string) time.Time { t, _ := time.Parse(time.DateOnly, s); return t }

func TestRentScheduleMonthlyWithEscalation(t *testing.T) {
	terms := RentTerms{Start: date("2026-11-15"), TermMonths: 24, Frequency: "MONTHLY", DueDay: 1, RentMinor: 100000, FeesMinor: 5000,
		EscalateAfter: 12, EscalateBps: 1000}
	if err := ValidateRentTerms(terms); err != nil {
		t.Fatal(err)
	}
	all := RentSchedule(terms, date("2030-01-01"))
	if len(all) != 24 {
		t.Fatalf("periods = %d", len(all))
	}
	if all[0].DueDate != date("2026-11-15") || all[0].AmountMinor != 105000 {
		t.Fatalf("first period %+v", all[0])
	}
	if all[1].PeriodStart != date("2026-12-15") || all[1].DueDate != date("2026-12-01") || all[1].PeriodEnd != date("2027-01-14") {
		t.Fatalf("second period %+v", all[1])
	}
	if all[12].AmountMinor != 115000 || all[11].AmountMinor != 105000 {
		t.Fatalf("escalation: %d %d", all[11].AmountMinor, all[12].AmountMinor)
	}
	if all[23].PeriodEnd != terms.EndDate() || terms.EndDate() != date("2028-11-14") {
		t.Fatalf("last period %+v end %s", all[23], terms.EndDate())
	}
	if got := RentSchedule(terms, date("2027-01-20")); len(got) != 3 {
		t.Fatalf("horizon periods = %d", len(got))
	}
}

func TestRentTermsValidation(t *testing.T) {
	if err := ValidateRentTerms(RentTerms{Start: date("2026-01-01"), TermMonths: 10, Frequency: "QUARTERLY", DueDay: 1, RentMinor: 1}); err == nil {
		t.Fatal("10 months is not whole quarters")
	}
	one := RentTerms{Start: date("2026-01-01"), TermMonths: 120, Frequency: "ONE_TIME", DueDay: 1, RentMinor: 5_000_000}
	if err := ValidateRentTerms(one); err != nil {
		t.Fatal(err)
	}
	if p := RentSchedule(one, date("2026-01-01")); len(p) != 1 || p[0].PeriodEnd != date("2035-12-31") {
		t.Fatalf("one-time %+v", p)
	}
}

func TestNoticeEffectiveDate(t *testing.T) {
	issued := time.Date(2026, 10, 9, 15, 0, 0, 0, time.UTC)
	if got := NoticeEffectiveDate(issued, 30, nil); got != date("2026-11-08") {
		t.Fatalf("got %s", got)
	}
	early, late := date("2026-10-20"), date("2027-01-31")
	if got := NoticeEffectiveDate(issued, 30, &early); got != date("2026-11-08") {
		t.Fatal("a requested date before the legal minimum must not shorten the notice")
	}
	if got := NoticeEffectiveDate(issued, 30, &late); got != late {
		t.Fatal("a later requested date applies")
	}
}

func TestAllocate(t *testing.T) {
	a, err := Allocate("PERCENTAGE", []Share{{"A", 3333}, {"B", 3333}, {"C", 3334}}, 10001)
	if err != nil {
		t.Fatal(err)
	}
	if a[0].AmountMinor+a[1].AmountMinor+a[2].AmountMinor != 10001 {
		t.Fatalf("allocation does not reconcile: %+v", a)
	}
	if _, err := Allocate("PERCENTAGE", []Share{{"A", 5000}}, 100); err == nil {
		t.Fatal("percentages must total 100%")
	}
	if _, err := Allocate("FIXED", []Share{{"A", 50}, {"B", 40}}, 100); err == nil {
		t.Fatal("fixed shares must equal the bill")
	}
	e, _ := Allocate("EQUAL", []Share{{"A", 0}, {"B", 0}, {"C", 0}}, 100)
	if e[0].AmountMinor != 34 || e[1].AmountMinor != 33 {
		t.Fatalf("equal: %+v", e)
	}
	m, _ := Allocate("SUB_METER", []Share{{"A", 30}, {"B", 70}}, 1000)
	if m[0].AmountMinor != 300 || m[1].AmountMinor != 700 {
		t.Fatalf("sub-meter: %+v", m)
	}
}

func TestRentScheduleMonthEnd(t *testing.T) {
	terms := RentTerms{Start: date("2027-01-31"), TermMonths: 3, Frequency: "MONTHLY", DueDay: 28, RentMinor: 1}
	p := RentSchedule(terms, date("2028-01-01"))
	if p[1].PeriodStart != date("2027-02-28") || p[0].PeriodEnd != date("2027-02-27") || p[2].PeriodStart != date("2027-03-31") || terms.EndDate() != date("2027-04-29") {
		t.Fatalf("month-end clamping: %+v end %s", p, terms.EndDate())
	}
}

func TestRentFreeMonths(t *testing.T) {
	terms := RentTerms{Start: date("2027-01-01"), TermMonths: 12, Frequency: "QUARTERLY", DueDay: 1, RentMinor: 100, RentFreeMonths: 6}
	if err := ValidateRentTerms(terms); err != nil {
		t.Fatal(err)
	}
	p := RentSchedule(terms, date("2030-01-01"))
	if len(p) != 2 || p[0].PeriodStart != date("2027-07-01") || p[0].Seq != 3 {
		t.Fatalf("rent-free schedule %+v", p)
	}
	terms.RentFreeMonths = 4
	if ValidateRentTerms(terms) == nil {
		t.Fatal("rent-free must be whole cycles")
	}
}
