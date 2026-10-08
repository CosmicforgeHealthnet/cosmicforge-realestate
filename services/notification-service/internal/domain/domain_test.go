package domain

import (
	"testing"
	"time"
)

func TestDeliveryStateMachine(t *testing.T) {
	all := []Status{StatusQueued, StatusDispatching, StatusSent, StatusRetryScheduled, StatusFailedFinal}
	allowed := map[[2]Status]bool{
		{StatusQueued, StatusDispatching}: true, {StatusDispatching, StatusSent}: true, {StatusDispatching, StatusRetryScheduled}: true,
		{StatusDispatching, StatusFailedFinal}: true, {StatusRetryScheduled, StatusDispatching}: true,
	}
	for _, f := range all {
		for _, to := range all {
			if Machine.Can(f, to) != allowed[[2]Status{f, to}] {
				t.Errorf("%s -> %s", f, to)
			}
		}
	}
	if !Machine.Terminal(StatusSent) || !Machine.Terminal(StatusFailedFinal) {
		t.Fatal("terminal states")
	}
}

func TestQuietHours(t *testing.T) {
	lagos, _ := time.LoadLocation("Africa/Lagos") // UTC+1
	q := QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour, Location: lagos}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 8, h, m, 0, 0, lagos) }
	cases := []struct {
		t    time.Time
		want time.Time
	}{
		{at(23, 0), time.Date(2026, 10, 9, 7, 0, 0, 0, lagos)},
		{at(3, 0), at(7, 0)},
		{at(12, 0), at(12, 0)},
		{at(21, 59), at(21, 59)},
	}
	for _, c := range cases {
		if got := q.DeferUntil(c.t); !got.Equal(c.want) {
			t.Errorf("%v -> %v want %v", c.t, got, c.want)
		}
	}
	day := QuietHours{Start: 13 * time.Hour, End: 14 * time.Hour, Location: lagos}
	if !day.DeferUntil(at(13, 30)).Equal(at(14, 0)) || !day.DeferUntil(at(15, 0)).Equal(at(15, 0)) {
		t.Fatal("same-day window")
	}
	if !CategorySecurity.Critical() || CategorySecurity.Configurable() || !CategoryActivity.Configurable() {
		t.Fatal("categories")
	}
}
