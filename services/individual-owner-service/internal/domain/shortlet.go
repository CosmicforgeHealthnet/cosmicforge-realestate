package domain

import "time"

// RefundBps is the share of a short-let stay refunded on cancellation
// (§13.20), by policy and notice before check-in. Owner cancellations are
// always refunded in full.
func RefundBps(policy string, byOwner bool, cancelledAt, checkIn time.Time) int64 {
	if byOwner {
		return 10000
	}
	notice := day(checkIn).Sub(day(cancelledAt)) / (24 * time.Hour)
	switch policy {
	case "FLEXIBLE":
		if notice >= 1 {
			return 10000
		}
		return 5000
	case "MODERATE":
		switch {
		case notice >= 5:
			return 10000
		case notice >= 1:
			return 5000
		}
	case "STRICT":
		switch {
		case notice >= 14:
			return 10000
		case notice >= 7:
			return 5000
		}
	}
	return 0
}

// Refund applies RefundBps to an amount (rounded down: never over-refund).
func Refund(amount, refundBps int64) int64 { return amount * refundBps / 10000 }

// Nights lists the nights of a stay [checkIn, checkOut).
func Nights(checkIn, checkOut time.Time) []time.Time {
	var out []time.Time
	for d := day(checkIn); d.Before(day(checkOut)); d = d.AddDate(0, 0, 1) {
		out = append(out, d)
	}
	return out
}
