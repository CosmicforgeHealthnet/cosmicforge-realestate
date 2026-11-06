package crm

import (
	"testing"
	"time"
)

func TestActualGuestNightsUsesCivilDatesAndDeduplicatesRooms(t *testing.T) {
	loc, e := time.LoadLocation("Europe/Rome")
	if e != nil {
		t.Fatal(e)
	}
	start := time.Date(2026, 10, 24, 15, 0, 0, 0, loc)
	end := time.Date(2026, 10, 26, 11, 0, 0, 0, loc)
	stays := []StayFact{{CheckedInAt: start, CheckedOutAt: &end}, {CheckedInAt: start, CheckedOutAt: &end}}
	n, e := ActualGuestNights(stays, "Europe/Rome")
	if e != nil || n != 2 {
		t.Fatalf("DST/duplicate rooms: nights=%d %v", n, e)
	}
	later := time.Date(2026, 10, 26, 13, 0, 0, 0, loc)
	n, e = ActualGuestNights([]StayFact{{CheckedInAt: end, CheckedOutAt: &later}}, "Europe/Rome")
	if e != nil || n != 0 {
		t.Fatalf("same-day day use: nights=%d %v", n, e)
	}
	n, e = ActualGuestNights([]StayFact{{CheckedInAt: start}}, "Europe/Rome")
	if e != nil || n != 0 {
		t.Fatalf("invented departure: nights=%d %v", n, e)
	}
}
func TestActualGuestNightsRejectsInvalidChronologyAndZone(t *testing.T) {
	start := time.Now()
	end := start.Add(-time.Hour)
	if _, e := ActualGuestNights([]StayFact{{CheckedInAt: start, CheckedOutAt: &end}}, "UTC"); e == nil {
		t.Fatal("invalid actual chronology accepted")
	}
	if _, e := ActualGuestNights(nil, "unconfigured/property-zone"); e == nil {
		t.Fatal("unknown property zone accepted")
	}
}
