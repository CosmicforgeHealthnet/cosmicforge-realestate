package revenue

import "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"

type RMSMetrics struct {
	Basis                       string   `json:"basis"`
	Currency                    string   `json:"currency"`
	KnownCalendarDays           int      `json:"known_calendar_days"`
	ApplicableRoomNights        int64    `json:"applicable_room_nights"`
	ConfirmedRoomNights         int64    `json:"confirmed_room_nights"`
	OutstandingOverbookedNights int64    `json:"outstanding_overbooked_nights"`
	BookedOccupancyBPS          *int64   `json:"booked_occupancy_bps"`
	AcceptedAccommodationMinor  *int64   `json:"accepted_accommodation_minor"`
	ContractualADRMinor         *int64   `json:"contractual_adr_minor"`
	ContractualRevPARMinor      *int64   `json:"contractual_revpar_minor"`
	RealizedOccupancyBPS        *int64   `json:"realized_occupancy_bps"`
	RealizedADRMinor            *int64   `json:"realized_adr_minor"`
	NetRevenueMinor             *int64   `json:"net_revenue_minor"`
	ContributionMinor           *int64   `json:"contribution_minor"`
	ConversionBPS               *int64   `json:"conversion_bps"`
	MissingInputs               []string `json:"missing_inputs"`
	Definitions                 []string `json:"definitions"`
}

// MetricsFromDemand keeps contractual booking values separate from realized
// stays, cash, refunds and contribution. None of the latter are inferred from
// collection success or a reservation's advertised rate.
func MetricsFromDemand(data DemandDataset) (RMSMetrics, error) {
	out := RMSMetrics{Basis: data.Basis, Currency: data.Currency, MissingInputs: append([]string{}, data.MissingInputs...), Definitions: []string{"Applicable room nights sum known physical calendar capacity minus blocked nights; holds/allotments do not reduce that physical denominator.", "Booked occupancy is confirmed unreleased committed room nights / applicable physical room nights; it may exceed 100% under explicit overbooking.", "Contractual ADR is accepted net accommodation value / confirmed room nights. Contractual RevPAR uses applicable physical room nights. Neither is realized financial revenue.", "Unknown accepted accommodation attribution or mixed currency leaves contractual revenue metrics unknown. Zero denominators produce null, not zero.", "Realized stays, payment/refund reconciliation, costs and valid Marketplace conversion attribution are separate owning inputs."}}
	if data.Basis != "CONFIRMED_COMMITTED_DEMAND" || len(data.Days) > 732 {
		return out, ErrPricing
	}
	total, e := money.New(0, data.Currency)
	if e != nil {
		return out, e
	}
	knownRevenue := len(data.Days) > 0
	seen := map[string]bool{}
	for _, day := range data.Days {
		if seen[day.Day] || day.Capacity < 0 || day.Capacity > 100000 || day.Blocked < 0 || day.Blocked > day.Capacity || day.ConfirmedNights < 0 || day.ConfirmedNights > 1001000 || day.OverbookedNights < 0 || day.OverbookedNights > 1000 {
			return out, ErrPricing
		}
		seen[day.Day] = true
		out.KnownCalendarDays++
		out.ApplicableRoomNights += day.Capacity - day.Blocked
		out.ConfirmedRoomNights += day.ConfirmedNights
		out.OutstandingOverbookedNights += day.OverbookedNights
		if day.AccommodationMinor == nil {
			knownRevenue = false
			continue
		}
		if *day.AccommodationMinor < 0 {
			return out, ErrPricing
		}
		n, e := money.New(*day.AccommodationMinor, data.Currency)
		if e != nil {
			return out, e
		}
		total, e = total.Add(n)
		if e != nil {
			return out, e
		}
	}
	if out.ApplicableRoomNights > 0 {
		n, e := Ratio(out.ConfirmedRoomNights, 10000, out.ApplicableRoomNights)
		if e != nil {
			return out, e
		}
		out.BookedOccupancyBPS = &n
	}
	if knownRevenue {
		n := total.AmountMinor
		out.AcceptedAccommodationMinor = &n
		if out.ConfirmedRoomNights > 0 {
			v, e := Ratio(n, 1, out.ConfirmedRoomNights)
			if e != nil {
				return out, e
			}
			out.ContractualADRMinor = &v
		}
		if out.ApplicableRoomNights > 0 {
			v, e := Ratio(n, 1, out.ApplicableRoomNights)
			if e != nil {
				return out, e
			}
			out.ContractualRevPARMinor = &v
		}
	}
	return out, nil
}
