package usecases

import (
	"testing"
	"time"
)

func TestPropertyClockDSTAndInvalidZone(t *testing.T) {
	if validateTimezone("Invented/Zone") == nil {
		t.Fatal("unsupported timezone accepted")
	}
	for _, tc := range []struct {
		date, zone string
		hour       int
		elapsed    time.Duration
	}{
		{"2026-03-28", "Europe/Rome", 13, 23 * time.Hour},
		{"2026-10-24", "Europe/Rome", 12, 25 * time.Hour},
		{"2026-07-01", "America/Los_Angeles", 21, 24 * time.Hour},
	} {
		d, _ := time.Parse(time.DateOnly, tc.date)
		arrival := checkInAt(d, "14:00", tc.zone)
		if arrival.UTC().Hour() != tc.hour {
			t.Fatalf("local instant %s: %s", tc.zone, arrival)
		}
		next := checkInAt(d.AddDate(0, 0, 1), "14:00", tc.zone)
		if next.Sub(arrival) != tc.elapsed {
			t.Fatalf("calendar day at DST boundary: %v", next.Sub(arrival))
		}
	}
}
