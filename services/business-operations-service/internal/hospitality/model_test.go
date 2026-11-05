package hospitality

import (
	"testing"
	"time"
)

func TestCivilCutoffDSTFoldGapAndBusinessDateMonotonicity(t *testing.T) {
	for _, v := range []struct{ day, clock, zone, want string }{
		{"2026-10-25", "02:30", "Europe/Rome", "2026-10-25T01:30:00Z"},
		{"2026-03-29", "02:30", "Europe/Rome", "2026-03-29T01:00:00Z"},
		{"2026-11-01", "01:30", "America/New_York", "2026-11-01T06:30:00Z"},
		{"2026-11-02", "06:00", "Africa/Lagos", "2026-11-02T05:00:00Z"},
	} {
		got, e := LocalInstant(v.day, v.clock, v.zone)
		if e != nil || got.Format(time.RFC3339) != v.want {
			t.Fatalf("%+v got=%v err=%v", v, got, e)
		}
	}
	start, _ := time.Parse(time.RFC3339, "2026-10-24T23:00:00Z")
	prior := ""
	for i := 0; i < 240; i++ {
		d, e := BusinessDate(start.Add(time.Duration(i)*time.Minute), "02:30", "Europe/Rome")
		if e != nil || prior != "" && d < prior {
			t.Fatalf("business date regressed at %d: %s -> %s %v", i, prior, d, e)
		}
		prior = d
	}
	if _, e := LocalInstant("2026-11-02", "25:01", "Europe/Rome"); e == nil {
		t.Fatal("invalid clock accepted")
	}
}
