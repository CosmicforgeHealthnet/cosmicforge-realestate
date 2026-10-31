package inventory

import "testing"

func TestCivilDateRangesAcrossDaylightSaving(t *testing.T) {
	for _, test := range []struct {
		from, to, zone string
		count          int
	}{{"2026-03-28", "2026-03-31", "Europe/Rome", 3}, {"2026-10-24", "2026-10-27", "Europe/Rome", 3}, {"2026-03-07", "2026-03-10", "America/New_York", 3}, {"2026-11-01", "2026-11-02", "UTC", 1}} {
		days, err := (DateRange{DateFrom: test.from, DateTo: test.to}).Days(test.zone)
		if err != nil || len(days) != test.count || days[0] != test.from || days[len(days)-1] == test.to {
			t.Fatalf("civil range %+v %v %v", test, days, err)
		}
	}
	for _, r := range []DateRange{{"2026-02-30", "2026-03-02"}, {"2026-01-01", "2026-01-01"}, {"2026-12-01", "2026-11-01"}, {"2026-01-01", "2028-01-01"}, {"0000-01-01", "0000-01-02"}} {
		if _, err := r.Days("UTC"); err == nil {
			t.Fatalf("invalid civil range accepted %+v", r)
		}
	}
	if _, err := (DateRange{"2026-11-01", "2026-11-02"}).Days("invalid/timezone"); err == nil {
		t.Fatal("invalid zone accepted")
	}
}
