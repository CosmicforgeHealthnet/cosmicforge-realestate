package revenue

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

const AnalyticsModel = "canonical-pace-cancellation-v1"

// ErrNoRecommendation means the retained facts do not support this kind now.
var ErrNoRecommendation = errors.New("no source-backed recommendation for the requested kind")

// BookingFact is one canonical reservation line. Nothing is inferred beyond
// recorded states, booking/confirmation times and accepted amounts.
type BookingFact struct {
	RoomID             ids.ID     `json:"room_id"`
	PropertyID         ids.ID     `json:"property_id"`
	ProductID          ids.ID     `json:"product_id"`
	Channel            string     `json:"channel"`
	Source             string     `json:"source"`
	State              string     `json:"state"`
	BookedAt           time.Time  `json:"booked_at"`
	ConfirmedAt        *time.Time `json:"confirmed_at"`
	CancelledAt        *time.Time `json:"cancelled_at"`
	DateFrom           string     `json:"date_from"`
	DateTo             string     `json:"date_to"`
	Quantity           int64      `json:"quantity"`
	AccommodationMinor *int64     `json:"accommodation_minor"`
	Currency           string     `json:"currency"`
}

// MarketInput is a governed, licensed external observation.
type MarketInput struct {
	Kind            string  `json:"kind"`
	Channel         string  `json:"channel"`
	DateFrom        string  `json:"date_from"`
	DateTo          string  `json:"date_to"`
	ValueMinor      *int64  `json:"value_minor"`
	ValueBPS        *int64  `json:"value_bps"`
	Currency        *string `json:"currency"`
	Label           string  `json:"label"`
	SourceReference string  `json:"source_reference"`
}

type PropertyDemand struct {
	PropertyID ids.ID      `json:"property_id"`
	BranchID   ids.ID      `json:"branch_id"`
	Currency   string      `json:"currency"`
	Timezone   string      `json:"timezone"`
	Days       []DemandDay `json:"days"`
}

type AnalyticsInput struct {
	ScopeKind    string           `json:"scope_kind"`
	ScopeID      ids.ID           `json:"scope_id"`
	AsOf         time.Time        `json:"as_of"`
	Today        string           `json:"today"`
	HistoryFrom  string           `json:"history_from"`
	DateFrom     string           `json:"date_from"`
	DateTo       string           `json:"date_to"`
	Properties   []PropertyDemand `json:"properties"`
	Bookings     []BookingFact    `json:"bookings"`
	MarketInputs []MarketInput    `json:"market_inputs"`
}

type AnalyticsKPIs struct {
	KnownDays            int    `json:"known_days"`
	ApplicableRoomNights int64  `json:"applicable_room_nights"`
	ConfirmedRoomNights  int64  `json:"confirmed_room_nights"`
	OccupancyBPS         *int64 `json:"occupancy_bps"`
	AcceptedMinor        *int64 `json:"accepted_accommodation_minor"`
	ADRMinor             *int64 `json:"adr_minor"`
	RevPARMinor          *int64 `json:"revpar_minor"`
}
type AnalyticsAggregate struct {
	ScopeKind string        `json:"scope_kind"`
	ScopeID   ids.ID        `json:"scope_id"`
	History   AnalyticsKPIs `json:"history"`
	Horizon   AnalyticsKPIs `json:"horizon"`
}
type SegmentAnalysis struct {
	Key           string `json:"key"`
	RoomNights    int64  `json:"room_nights"`
	ShareBPS      *int64 `json:"share_bps"`
	AcceptedMinor *int64 `json:"accepted_accommodation_minor"`
	ADRMinor      *int64 `json:"adr_minor"`
	CommissionBPS *int64 `json:"commission_bps"`
	CommissionMin *int64 `json:"commission_minor"`
	NetMinor      *int64 `json:"net_minor"`
}
type LeadTimeStats struct {
	Bookings      int    `json:"bookings"`
	MedianDays    *int64 `json:"median_days"`
	P90Days       *int64 `json:"p90_days"`
	MeanMilliDays *int64 `json:"mean_milli_days"`
}
type PickupStats struct {
	HorizonOnBooksNights int64 `json:"horizon_on_books_nights"`
	Last7DaysNights      int64 `json:"last_7_days_nights"`
	Last28DaysNights     int64 `json:"last_28_days_nights"`
}
type PeriodEffect struct {
	Key          string `json:"key"`
	OccupancyBPS *int64 `json:"occupancy_bps"`
	IndexBPS     *int64 `json:"index_bps"`
}
type ComparablePeriod struct {
	DateFrom     string `json:"date_from"`
	DateTo       string `json:"date_to"`
	Status       string `json:"status"`
	OccupancyBPS *int64 `json:"occupancy_bps"`
	AcceptedMin  *int64 `json:"accepted_accommodation_minor"`
}
type AnalyticsForecastDay struct {
	Day                       string   `json:"day"`
	PhysicalCapacity          int64    `json:"physical_capacity"`
	OnBooksNights             int64    `json:"on_books_nights"`
	BaselineMilliNights       *int64   `json:"baseline_milli_nights"`
	PickupShareBPS            *int64   `json:"pickup_share_bps"`
	ExpectedPickupMilliNights *int64   `json:"expected_pickup_milli_nights"`
	CancellationAdjustedMilli *int64   `json:"cancellation_adjusted_milli_nights"`
	MarketIndexBPS            *int64   `json:"market_index_bps"`
	Events                    []string `json:"events"`
	Status                    string   `json:"status"`
	Explanation               string   `json:"explanation"`
}
type AnalyticsResult struct {
	Model                   string                 `json:"model"`
	AlgorithmVersion        int                    `json:"algorithm_version"`
	Status                  string                 `json:"status"`
	ConfidenceMethod        string                 `json:"confidence_method"`
	IsCalibratedProbability bool                   `json:"is_calibrated_probability"`
	Currency                string                 `json:"currency"`
	Features                []string               `json:"features"`
	MissingInputs           []string               `json:"missing_inputs"`
	Assumptions             []string               `json:"assumptions"`
	Portfolio               AnalyticsAggregate     `json:"portfolio"`
	Branches                []AnalyticsAggregate   `json:"branches"`
	Properties              []AnalyticsAggregate   `json:"properties"`
	Products                []SegmentAnalysis      `json:"products"`
	Channels                []SegmentAnalysis      `json:"channels"`
	LeadTime                LeadTimeStats          `json:"lead_time"`
	Pickup                  PickupStats            `json:"pickup"`
	CancellationRateBPS     *int64                 `json:"cancellation_rate_bps"`
	NoShowRateBPS           *int64                 `json:"no_show_rate_bps"`
	AverageLOSMilliNights   *int64                 `json:"average_los_milli_nights"`
	WeekdayEffects          []PeriodEffect         `json:"weekday_effects"`
	SeasonalEffects         []PeriodEffect         `json:"seasonal_effects"`
	ComparablePeriod        ComparablePeriod       `json:"comparable_period"`
	Forecast                []AnalyticsForecastDay `json:"forecast"`
}

func ptr(v int64) *int64 { return &v }
func parseDay(s string) (time.Time, error) {
	d, e := time.Parse(time.DateOnly, s)
	if e != nil || d.Format(time.DateOnly) != s {
		return d, ErrPricing
	}
	return d, nil
}
func nights(from, to string) int64 {
	a, e1 := parseDay(from)
	b, e2 := parseDay(to)
	if e1 != nil || e2 != nil || !b.After(a) {
		return 0
	}
	return int64(b.Sub(a).Hours() / 24)
}
func overlap(aFrom, aTo, bFrom, bTo string) int64 {
	from, to := max(aFrom, bFrom), min(aTo, bTo)
	return nights(from, to)
}
func activeState(s string) bool {
	return slices.Contains([]string{"CONFIRMED", "CHECKED_IN", "CHECKED_OUT", "COMPLETED"}, s)
}

func kpis(days []DemandDay, from, to string, unknownRevenue bool) AnalyticsKPIs {
	out := AnalyticsKPIs{}
	var revenue int64
	known := !unknownRevenue
	for _, d := range days {
		if d.Day < from || d.Day >= to {
			continue
		}
		out.KnownDays++
		out.ApplicableRoomNights += d.Capacity - d.Blocked
		out.ConfirmedRoomNights += d.ConfirmedNights
		if d.AccommodationMinor == nil {
			known = false
		} else if next, e := checkedAdd(revenue, *d.AccommodationMinor); e == nil {
			revenue = next
		} else {
			known = false
		}
	}
	if out.ApplicableRoomNights > 0 {
		v, _ := Ratio(out.ConfirmedRoomNights, 10000, out.ApplicableRoomNights)
		out.OccupancyBPS = &v
	}
	if known && out.KnownDays > 0 {
		out.AcceptedMinor = ptr(revenue)
		if out.ConfirmedRoomNights > 0 {
			v, _ := Ratio(revenue, 1, out.ConfirmedRoomNights)
			out.ADRMinor = &v
		}
		if out.ApplicableRoomNights > 0 {
			v, _ := Ratio(revenue, 1, out.ApplicableRoomNights)
			out.RevPARMinor = &v
		}
	}
	return out
}

func percentile(values []int64, bps int64) *int64 {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]int64{}, values...)
	slices.Sort(sorted)
	index, _ := Ratio(int64(len(sorted)-1), bps, 10000)
	return ptr(sorted[index])
}

// AnalyzeDemand is deterministic over its canonical input. Missing sources are
// reported and leave the dependent outputs null; nothing is zero-filled.
func AnalyzeDemand(in AnalyticsInput) (AnalyticsResult, error) {
	out := AnalyticsResult{Model: AnalyticsModel, AlgorithmVersion: 1, Status: "INSUFFICIENT_DATA", ConfidenceMethod: "OBSERVED_COVERAGE_AND_COMPARABLE_COUNT_NOT_CALIBRATED", Features: []string{}, MissingInputs: []string{}, Branches: []AnalyticsAggregate{}, Properties: []AnalyticsAggregate{}, Products: []SegmentAnalysis{}, Channels: []SegmentAnalysis{}, WeekdayEffects: []PeriodEffect{}, SeasonalEffects: []PeriodEffect{}, Forecast: []AnalyticsForecastDay{},
		Assumptions: []string{"Confirmed committed nights are recorded demand, not realized stays or verified cash.", "Pickup expectation uses the observed share of historical room nights booked within the same remaining lead time; no elasticity is inferred.", "Cancellation adjustment applies the observed post-confirmation cancellation rate of historical arrivals to on-books and expected pickup.", "Holiday/event inputs are labels only; a governed MARKET_DEMAND_INDEX is the only external demand multiplier.", "Revenue by channel/product is pro-rated by night count from each accepted line total.", "Mixed currencies leave monetary outputs null."}}
	if len(in.Properties) == 0 || len(in.Properties) > 500 || in.AsOf.IsZero() || len(in.Bookings) > 200000 || len(in.MarketInputs) > 5000 {
		return out, ErrPricing
	}
	start, e := parseDay(in.DateFrom)
	if e != nil {
		return out, e
	}
	end, e := parseDay(in.DateTo)
	if e != nil || !end.After(start) || end.After(start.AddDate(0, 0, 90)) {
		return out, ErrPricing
	}
	today, e := parseDay(in.Today)
	if e != nil || start.Before(today) {
		return out, ErrPricing
	}
	historyStart, e := parseDay(in.HistoryFrom)
	if e != nil || !historyStart.Before(today) {
		return out, ErrPricing
	}
	currency := in.Properties[0].Currency
	for _, p := range in.Properties {
		if _, e := money.ParseCurrency(p.Currency); e != nil {
			return out, e
		}
		if p.Currency != currency {
			currency = ""
		}
	}
	out.Currency = currency
	if currency == "" {
		out.MissingInputs = append(out.MissingInputs, "single_currency")
	}
	// Aggregate calendars by day across the requested properties.
	all := map[string]DemandDay{}
	branchDays := map[ids.ID][]DemandDay{}
	for _, p := range in.Properties {
		seen := map[string]bool{}
		for _, d := range p.Days {
			if _, e := parseDay(d.Day); e != nil || seen[d.Day] || d.Capacity < 0 || d.Blocked < 0 || d.Blocked > d.Capacity || d.ConfirmedNights < 0 {
				return out, ErrPricing
			}
			seen[d.Day] = true
			a := all[d.Day]
			a.Day = d.Day
			a.Capacity += d.Capacity
			a.Blocked += d.Blocked
			a.ConfirmedNights += d.ConfirmedNights
			if _, exists := all[d.Day]; !exists {
				a.AccommodationMinor = d.AccommodationMinor
			} else if a.AccommodationMinor != nil && d.AccommodationMinor != nil {
				a.AccommodationMinor = ptr(*a.AccommodationMinor + *d.AccommodationMinor)
			} else {
				a.AccommodationMinor = nil
			}
			all[d.Day] = a
		}
		out.Properties = append(out.Properties, AnalyticsAggregate{ScopeKind: "PROPERTY", ScopeID: p.PropertyID, History: kpis(p.Days, in.HistoryFrom, in.Today, currency == ""), Horizon: kpis(p.Days, in.DateFrom, in.DateTo, currency == "")})
		branchDays[p.BranchID] = append(branchDays[p.BranchID], p.Days...)
	}
	merged := make([]DemandDay, 0, len(all))
	for _, d := range all {
		merged = append(merged, d)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Day < merged[j].Day })
	scopeID := in.ScopeID
	out.Portfolio = AnalyticsAggregate{ScopeKind: in.ScopeKind, ScopeID: scopeID, History: kpis(merged, in.HistoryFrom, in.Today, currency == ""), Horizon: kpis(merged, in.DateFrom, in.DateTo, currency == "")}
	branchIDs := []ids.ID{}
	for id := range branchDays {
		branchIDs = append(branchIDs, id)
	}
	slices.SortFunc(branchIDs, func(a, b ids.ID) int {
		if a.String() < b.String() {
			return -1
		}
		if a == b {
			return 0
		}
		return 1
	})
	for _, id := range branchIDs {
		days := branchDays[id]
		// Branch totals merge per day; duplicates across properties add.
		byDay := map[string]DemandDay{}
		for _, d := range days {
			a := byDay[d.Day]
			_, existed := byDay[d.Day]
			a.Day = d.Day
			a.Capacity += d.Capacity
			a.Blocked += d.Blocked
			a.ConfirmedNights += d.ConfirmedNights
			if !existed {
				a.AccommodationMinor = d.AccommodationMinor
			} else if a.AccommodationMinor != nil && d.AccommodationMinor != nil {
				a.AccommodationMinor = ptr(*a.AccommodationMinor + *d.AccommodationMinor)
			} else {
				a.AccommodationMinor = nil
			}
			byDay[d.Day] = a
		}
		list := []DemandDay{}
		for _, d := range byDay {
			list = append(list, d)
		}
		out.Branches = append(out.Branches, AnalyticsAggregate{ScopeKind: "BRANCH", ScopeID: id, History: kpis(list, in.HistoryFrom, in.Today, currency == ""), Horizon: kpis(list, in.DateFrom, in.DateTo, currency == "")})
	}
	out.Features = append(out.Features, "confirmed_committed_nights", "physical_calendar")
	if out.Portfolio.History.AcceptedMinor != nil {
		out.Features = append(out.Features, "accepted_accommodation")
	} else {
		out.MissingInputs = append(out.MissingInputs, "accepted_accommodation_complete")
	}
	// Booking-derived features.
	asOfDay := in.Today
	var leads []int64
	var leadSum int64
	var everNights, cancelledNights, noShowNights, losSum, losCount int64
	pickupNumer := map[int64]int64{} // lead days -> historical nights booked with that lead
	var pickupTotal int64
	channelNights, productNights := map[string]int64{}, map[string]int64{}
	channelMinor, productMinor := map[string]int64{}, map[string]int64{}
	channelUnknown, productUnknown := map[string]bool{}, map[string]bool{}
	for _, b := range in.Bookings {
		n := nights(b.DateFrom, b.DateTo)
		if n <= 0 || b.Quantity <= 0 {
			continue
		}
		arrival, _ := parseDay(b.DateFrom)
		booked, _ := parseDay(b.BookedAt.UTC().Format(time.DateOnly))
		lead := int64(arrival.Sub(booked).Hours() / 24)
		if lead < 0 {
			lead = 0
		}
		if b.BookedAt.After(in.AsOf) {
			continue
		}
		confirmedEver := b.ConfirmedAt != nil && !b.ConfirmedAt.After(in.AsOf)
		if confirmedEver {
			leads = append(leads, lead)
			leadSum += lead
			losSum += n
			losCount++
		}
		// Historical arrivals inform cancellation/no-show/pickup curves.
		if confirmedEver && b.DateFrom >= in.HistoryFrom && b.DateFrom < asOfDay {
			everNights += n * b.Quantity
			if b.State == "CANCELLED" && b.CancelledAt != nil && !b.CancelledAt.After(in.AsOf) {
				cancelledNights += n * b.Quantity
			}
			if b.State == "NO_SHOW" {
				noShowNights += n * b.Quantity
			}
		}
		if confirmedEver && activeState(b.State) {
			histNights := overlap(b.DateFrom, b.DateTo, in.HistoryFrom, asOfDay) * b.Quantity
			if histNights > 0 {
				pickupNumer[lead] += histNights
				pickupTotal += histNights
			}
			if horizon := overlap(b.DateFrom, b.DateTo, in.DateFrom, in.DateTo) * b.Quantity; horizon > 0 {
				if in.AsOf.Sub(b.BookedAt) <= 7*24*time.Hour {
					out.Pickup.Last7DaysNights += horizon
				}
				if in.AsOf.Sub(b.BookedAt) <= 28*24*time.Hour {
					out.Pickup.Last28DaysNights += horizon
				}
			}
			window := overlap(b.DateFrom, b.DateTo, in.HistoryFrom, in.DateTo) * b.Quantity
			if window > 0 {
				ch := b.Channel
				if ch == "" {
					ch = "DIRECT"
				}
				channelNights[ch] += window
				productNights[b.ProductID.String()] += window
				if b.AccommodationMinor == nil || b.Currency != currency || currency == "" {
					channelUnknown[ch], productUnknown[b.ProductID.String()] = true, true
				} else {
					share, e := Ratio(*b.AccommodationMinor, window, n*b.Quantity)
					if e != nil {
						return out, e
					}
					channelMinor[ch] += share
					productMinor[b.ProductID.String()] += share
				}
			}
		}
	}
	out.LeadTime.Bookings = len(leads)
	if len(leads) > 0 {
		out.LeadTime.MedianDays = percentile(leads, 5000)
		out.LeadTime.P90Days = percentile(leads, 9000)
		v, _ := Ratio(leadSum, 1000, int64(len(leads)))
		out.LeadTime.MeanMilliDays = &v
		out.Features = append(out.Features, "booking_lead_time")
		v2, _ := Ratio(losSum, 1000, losCount)
		out.AverageLOSMilliNights = &v2
	} else {
		out.MissingInputs = append(out.MissingInputs, "booking_lead_time")
	}
	if everNights > 0 {
		c, _ := Ratio(cancelledNights, 10000, everNights)
		ns, _ := Ratio(noShowNights, 10000, everNights)
		out.CancellationRateBPS, out.NoShowRateBPS = &c, &ns
		out.Features = append(out.Features, "cancellations", "no_shows")
	} else {
		out.MissingInputs = append(out.MissingInputs, "historical_arrival_outcomes")
	}
	if pickupTotal > 0 {
		out.Features = append(out.Features, "pickup_pace")
	} else {
		out.MissingInputs = append(out.MissingInputs, "pickup_pace")
	}
	segments := func(nightsBy map[string]int64, minorBy map[string]int64, unknown map[string]bool, commission map[string]*int64) []SegmentAnalysis {
		keys := []string{}
		var total int64
		for k, v := range nightsBy {
			keys = append(keys, k)
			total += v
		}
		slices.Sort(keys)
		list := []SegmentAnalysis{}
		for _, k := range keys {
			s := SegmentAnalysis{Key: k, RoomNights: nightsBy[k]}
			if total > 0 {
				v, _ := Ratio(nightsBy[k], 10000, total)
				s.ShareBPS = &v
			}
			if !unknown[k] {
				s.AcceptedMinor = ptr(minorBy[k])
				if s.RoomNights > 0 {
					v, _ := Ratio(minorBy[k], 1, s.RoomNights)
					s.ADRMinor = &v
				}
				if bps := commission[k]; bps != nil {
					s.CommissionBPS = bps
					c, _ := Ratio(minorBy[k], *bps, 10000)
					s.CommissionMin = &c
					s.NetMinor = ptr(minorBy[k] - c)
				}
			}
			list = append(list, s)
		}
		return list
	}
	commissions := map[string]*int64{}
	demandIndex := map[string]int64{}
	events := map[string][]string{}
	for _, m := range in.MarketInputs {
		from, to := m.DateFrom, m.DateTo
		switch m.Kind {
		case "CHANNEL_COMMISSION":
			if m.ValueBPS != nil && overlap(from, to, in.HistoryFrom, in.DateTo) > 0 {
				commissions[m.Channel] = m.ValueBPS
			}
		case "MARKET_DEMAND_INDEX", "HOLIDAY", "LOCAL_EVENT":
			for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
				key := d.Format(time.DateOnly)
				if key >= from && key < to {
					if m.Kind == "MARKET_DEMAND_INDEX" && m.ValueBPS != nil {
						demandIndex[key] = *m.ValueBPS
					} else {
						events[key] = append(events[key], m.Kind+":"+m.Label)
					}
				}
			}
		}
	}
	if len(commissions) > 0 {
		out.Features = append(out.Features, "governed_channel_commission")
	} else {
		out.MissingInputs = append(out.MissingInputs, "channel_commission")
	}
	if len(demandIndex) > 0 || len(events) > 0 {
		out.Features = append(out.Features, "governed_market_inputs")
	} else {
		out.MissingInputs = append(out.MissingInputs, "verified_market_holiday_event_inputs")
	}
	out.Channels = segments(channelNights, channelMinor, channelUnknown, commissions)
	out.Products = segments(productNights, productMinor, productUnknown, map[string]*int64{})
	if len(out.Channels) > 0 {
		out.Features = append(out.Features, "channel_source")
	}
	// Weekday and month effects over the observed history.
	var weekdayCap, weekdayDemand [7]int64
	monthCap, monthDemand := map[string]int64{}, map[string]int64{}
	var totalCap, totalDemand int64
	for _, d := range merged {
		if d.Day < in.HistoryFrom || d.Day >= in.Today {
			continue
		}
		date, _ := parseDay(d.Day)
		eligible := d.Capacity - d.Blocked
		if eligible <= 0 {
			continue
		}
		weekdayCap[date.Weekday()] += eligible
		weekdayDemand[date.Weekday()] += d.ConfirmedNights
		monthCap[d.Day[:7]] += eligible
		monthDemand[d.Day[:7]] += d.ConfirmedNights
		totalCap += eligible
		totalDemand += d.ConfirmedNights
	}
	var overall *int64
	if totalCap > 0 {
		v, _ := Ratio(totalDemand, 10000, totalCap)
		overall = &v
		out.Features = append(out.Features, "occupancy", "weekday", "month_season")
	}
	effect := func(key string, demand, capacity int64) PeriodEffect {
		e := PeriodEffect{Key: key}
		if capacity > 0 {
			v, _ := Ratio(demand, 10000, capacity)
			e.OccupancyBPS = &v
			if overall != nil && *overall > 0 {
				idx, _ := Ratio(v, 10000, *overall)
				e.IndexBPS = &idx
			}
		}
		return e
	}
	for wd := 0; wd < 7; wd++ {
		out.WeekdayEffects = append(out.WeekdayEffects, effect(time.Weekday(wd).String(), weekdayDemand[wd], weekdayCap[wd]))
	}
	months := []string{}
	for k := range monthCap {
		months = append(months, k)
	}
	slices.Sort(months)
	for _, m := range months {
		out.SeasonalEffects = append(out.SeasonalEffects, effect(m, monthDemand[m], monthCap[m]))
	}
	// Comparable period: the same weekday-aligned horizon 364 days earlier.
	cFrom, cTo := start.AddDate(0, 0, -364).Format(time.DateOnly), end.AddDate(0, 0, -364).Format(time.DateOnly)
	out.ComparablePeriod = ComparablePeriod{DateFrom: cFrom, DateTo: cTo, Status: "UNAVAILABLE"}
	comparable := kpis(merged, cFrom, cTo, currency == "")
	if int64(comparable.KnownDays) == nights(cFrom, cTo) && comparable.KnownDays > 0 {
		out.ComparablePeriod.Status = "AVAILABLE"
		out.ComparablePeriod.OccupancyBPS = comparable.OccupancyBPS
		out.ComparablePeriod.AcceptedMin = comparable.AcceptedMinor
		out.Features = append(out.Features, "comparable_period")
	} else {
		out.MissingInputs = append(out.MissingInputs, "comparable_period_history")
	}
	ready := true
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		key := d.Format(time.DateOnly)
		day, known := all[key]
		point := AnalyticsForecastDay{Day: key, Status: "INSUFFICIENT_DATA", Events: events[key]}
		if point.Events == nil {
			point.Events = []string{}
		}
		if !known {
			ready = false
			point.Explanation = "Target calendar is unknown."
			out.Forecast = append(out.Forecast, point)
			continue
		}
		point.PhysicalCapacity = day.Capacity - day.Blocked
		point.OnBooksNights = day.ConfirmedNights
		wd := d.Weekday()
		if weekdayCap[wd] > 0 && point.PhysicalCapacity > 0 {
			v, _ := Ratio(point.PhysicalCapacity*1000, weekdayDemand[wd], weekdayCap[wd])
			point.BaselineMilliNights = &v
		}
		lead := int64(d.Sub(today).Hours() / 24)
		if pickupTotal > 0 {
			var within int64
			for l, n := range pickupNumer {
				if l <= lead {
					within += n
				}
			}
			share, _ := Ratio(within, 10000, pickupTotal)
			point.PickupShareBPS = &share
			if share < 10000 {
				final, e := Ratio(point.OnBooksNights*1000, 10000, 10000-share)
				if e == nil {
					p := max(final-point.OnBooksNights*1000, 0)
					point.ExpectedPickupMilliNights = &p
				}
			}
		}
		cancel := int64(0)
		if out.CancellationRateBPS != nil {
			cancel = *out.CancellationRateBPS
		}
		var projected *int64
		switch {
		case point.ExpectedPickupMilliNights != nil && out.CancellationRateBPS != nil:
			v, _ := Ratio(point.OnBooksNights*1000+*point.ExpectedPickupMilliNights, 10000-cancel, 10000)
			projected = &v
			point.Explanation = fmt.Sprintf("On-books %d nights plus observed pickup share %d bps for %d remaining lead days, reduced by observed cancellation %d bps.", point.OnBooksNights, *point.PickupShareBPS, lead, cancel)
		case point.BaselineMilliNights != nil && out.CancellationRateBPS != nil:
			adjusted, _ := Ratio(point.OnBooksNights*1000, 10000-cancel, 10000)
			v := max(*point.BaselineMilliNights, adjusted)
			projected = &v
			point.Explanation = "Pickup curve unavailable; weekday baseline bounded below by cancellation-adjusted on-books."
		}
		if projected != nil {
			if idx, ok := demandIndex[key]; ok {
				v, _ := Ratio(*projected, 10000+idx, 10000)
				projected = &v
				point.MarketIndexBPS = ptr(idx)
				point.Explanation += " Governed market demand index applied."
			}
			v := min(max(*projected, 0), point.PhysicalCapacity*1000)
			point.CancellationAdjustedMilli = &v
			point.Status = "BASELINE_READY"
		} else {
			ready = false
			point.Explanation = "Pickup or weekday history and historical cancellation outcomes are required."
		}
		out.Pickup.HorizonOnBooksNights += point.OnBooksNights
		out.Forecast = append(out.Forecast, point)
	}
	if ready {
		out.Status = "BASELINE_READY"
	}
	slices.Sort(out.MissingInputs)
	out.MissingInputs = slices.Compact(out.MissingInputs)
	return out, nil
}

// ScenarioDay is one projected night for simulation.
type ScenarioDay struct {
	Day                  string `json:"day"`
	PhysicalCapacity     int64  `json:"physical_capacity"`
	ProjectedMilliNights int64  `json:"projected_milli_nights"`
	CurrentAmountMinor   int64  `json:"current_amount_minor"`
}
type ChannelShare struct {
	Channel  string `json:"channel"`
	ShareBPS int64  `json:"share_bps"`
}
type ScenarioAlternative struct {
	Name                  string         `json:"name"`
	AmountMinor           *int64         `json:"amount_minor,omitempty"`
	ChangeBPS             *int64         `json:"change_bps,omitempty"`
	PromotionDiscountBPS  int64          `json:"promotion_discount_bps"`
	PromotionShareBPS     int64          `json:"promotion_share_bps"`
	AverageLOSMilliNights *int64         `json:"average_los_milli_nights,omitempty"`
	ChannelMix            []ChannelShare `json:"channel_mix,omitempty"`
}
type ScenarioInput struct {
	Currency                   string                `json:"currency"`
	Days                       []ScenarioDay         `json:"days"`
	BaselineChannelMix         []ChannelShare        `json:"baseline_channel_mix"`
	CommissionBPS              map[string]int64      `json:"commission_bps"`
	OperatingCostPerNightMinor *int64                `json:"operating_cost_per_night_minor"`
	CostPerStayMinor           *int64                `json:"cost_per_stay_minor"`
	CurrentAverageLOSMilli     *int64                `json:"current_average_los_milli_nights"`
	Alternatives               []ScenarioAlternative `json:"alternatives"`
}
type ChannelOutcome struct {
	Channel         string `json:"channel"`
	MilliNights     int64  `json:"milli_nights"`
	GrossMinor      int64  `json:"gross_minor"`
	CommissionMinor *int64 `json:"commission_minor"`
}
type ScenarioResult struct {
	Name                     string           `json:"name"`
	ProjectedSoldMilliNights int64            `json:"projected_sold_milli_nights"`
	ApplicableRoomNights     int64            `json:"applicable_room_nights"`
	OccupancyBPS             *int64           `json:"occupancy_bps"`
	ADRMinor                 *int64           `json:"adr_minor"`
	RevPARMinor              *int64           `json:"revpar_minor"`
	GrossMinor               int64            `json:"gross_minor"`
	PromotionCostMinor       int64            `json:"promotion_cost_minor"`
	ChannelCommissionMinor   *int64           `json:"channel_commission_minor"`
	NetRevenueMinor          *int64           `json:"net_revenue_minor"`
	OperationalCostMinor     *int64           `json:"operational_cost_minor"`
	ContributionMinor        *int64           `json:"contribution_minor"`
	ContributionMarginBPS    *int64           `json:"contribution_margin_bps"`
	ArrivalsMilli            *int64           `json:"arrivals_milli"`
	Channels                 []ChannelOutcome `json:"channels"`
}
type ScenarioSimulation struct {
	Basis       string           `json:"basis"`
	Currency    string           `json:"currency"`
	Results     []ScenarioResult `json:"results"`
	Assumptions []string         `json:"assumptions"`
}

// SimulateScenarios is a pure projection: no canonical record changes. Demand
// is held at the retained forecast; unknown commissions/costs stay null.
func SimulateScenarios(in ScenarioInput) (ScenarioSimulation, error) {
	out := ScenarioSimulation{Basis: "FIXED_FORECAST_DEMAND_ALTERNATIVES", Currency: in.Currency, Results: []ScenarioResult{}, Assumptions: []string{"Projected demand is held at the retained forecast; no price elasticity is inferred.", "Promotion cost applies the discount to the stated share of projected nights.", "Commission, net revenue, operating cost and contribution are null unless every required governed input is present.", "Arrivals derive from projected nights divided by average length of stay when provided.", "No canonical rate, restriction, promotion or availability is changed."}}
	if _, e := money.ParseCurrency(in.Currency); e != nil || len(in.Days) == 0 || len(in.Days) > 90 || len(in.Alternatives) == 0 || len(in.Alternatives) > 20 {
		return out, ErrPricing
	}
	validMix := func(mix []ChannelShare) bool {
		var total int64
		seen := map[string]bool{}
		for _, c := range mix {
			if c.ShareBPS < 0 || c.ShareBPS > 10000 || seen[c.Channel] {
				return false
			}
			seen[c.Channel] = true
			total += c.ShareBPS
		}
		return len(mix) == 0 || total == 10000
	}
	if !validMix(in.BaselineChannelMix) {
		return out, ErrPricing
	}
	for _, alt := range in.Alternatives {
		if len(alt.Name) < 1 || len(alt.Name) > 80 || (alt.AmountMinor == nil) == (alt.ChangeBPS == nil) || alt.AmountMinor != nil && *alt.AmountMinor < 0 || alt.ChangeBPS != nil && (*alt.ChangeBPS < -9000 || *alt.ChangeBPS > 10000) || alt.PromotionDiscountBPS < 0 || alt.PromotionDiscountBPS > 10000 || alt.PromotionShareBPS < 0 || alt.PromotionShareBPS > 10000 || !validMix(alt.ChannelMix) || alt.AverageLOSMilliNights != nil && *alt.AverageLOSMilliNights < 1000 {
			return out, ErrPricing
		}
		r := ScenarioResult{Name: alt.Name, Channels: []ChannelOutcome{}}
		for _, d := range in.Days {
			if d.PhysicalCapacity < 0 || d.ProjectedMilliNights < 0 || d.ProjectedMilliNights > d.PhysicalCapacity*1000 || d.CurrentAmountMinor < 0 {
				return out, ErrPricing
			}
			amount := d.CurrentAmountMinor
			if alt.AmountMinor != nil {
				amount = *alt.AmountMinor
			} else {
				delta, e := Ratio(d.CurrentAmountMinor, *alt.ChangeBPS, 10000)
				if e != nil {
					return out, e
				}
				amount += delta
			}
			gross, e := Ratio(amount, d.ProjectedMilliNights, 1000)
			if e != nil {
				return out, e
			}
			promoNights, _ := Ratio(gross, alt.PromotionShareBPS, 10000)
			promo, _ := Ratio(promoNights, alt.PromotionDiscountBPS, 10000)
			if r.GrossMinor, e = checkedAdd(r.GrossMinor, gross-promo); e != nil {
				return out, e
			}
			r.PromotionCostMinor += promo
			r.ProjectedSoldMilliNights += d.ProjectedMilliNights
			r.ApplicableRoomNights += d.PhysicalCapacity
		}
		if r.ApplicableRoomNights > 0 {
			v, _ := Ratio(r.ProjectedSoldMilliNights, 10000, r.ApplicableRoomNights*1000)
			r.OccupancyBPS = &v
			rev, _ := Ratio(r.GrossMinor, 1, r.ApplicableRoomNights)
			r.RevPARMinor = &rev
		}
		if r.ProjectedSoldMilliNights > 0 {
			v, _ := Ratio(r.GrossMinor, 1000, r.ProjectedSoldMilliNights)
			r.ADRMinor = &v
		}
		mix := in.BaselineChannelMix
		if len(alt.ChannelMix) > 0 {
			mix = alt.ChannelMix
		}
		if len(mix) > 0 {
			var commission int64
			known := true
			for _, c := range mix {
				g, _ := Ratio(r.GrossMinor, c.ShareBPS, 10000)
				n, _ := Ratio(r.ProjectedSoldMilliNights, c.ShareBPS, 10000)
				o := ChannelOutcome{Channel: c.Channel, MilliNights: n, GrossMinor: g}
				if bps, ok := in.CommissionBPS[c.Channel]; ok {
					v, _ := Ratio(g, bps, 10000)
					o.CommissionMinor = &v
					commission += v
				} else {
					known = false
				}
				r.Channels = append(r.Channels, o)
			}
			if known {
				r.ChannelCommissionMinor = ptr(commission)
				r.NetRevenueMinor = ptr(r.GrossMinor - commission)
			}
		}
		los := in.CurrentAverageLOSMilli
		if alt.AverageLOSMilliNights != nil {
			los = alt.AverageLOSMilliNights
		}
		if los != nil && *los >= 1000 {
			v, _ := Ratio(r.ProjectedSoldMilliNights, 1000, *los)
			r.ArrivalsMilli = &v
		}
		if in.OperatingCostPerNightMinor != nil {
			cost, _ := Ratio(*in.OperatingCostPerNightMinor, r.ProjectedSoldMilliNights, 1000)
			if in.CostPerStayMinor != nil {
				if r.ArrivalsMilli == nil {
					cost = -1
				} else {
					stay, _ := Ratio(*in.CostPerStayMinor, *r.ArrivalsMilli, 1000)
					cost += stay
				}
			}
			if cost >= 0 {
				r.OperationalCostMinor = &cost
			}
		}
		if r.NetRevenueMinor != nil && r.OperationalCostMinor != nil {
			c := *r.NetRevenueMinor - *r.OperationalCostMinor
			r.ContributionMinor = &c
			if *r.NetRevenueMinor > 0 {
				m, _ := Ratio(c, 10000, *r.NetRevenueMinor)
				r.ContributionMarginBPS = &m
			}
		}
		out.Results = append(out.Results, r)
	}
	return out, nil
}

// AdvancedParameters narrow the operator's recommendation thresholds.
type AdvancedParameters struct {
	HighOccupancyBPS int64  `json:"high_occupancy_bps"`
	LowOccupancyBPS  int64  `json:"low_occupancy_bps"`
	LastMinuteDays   int    `json:"last_minute_days"`
	Channel          string `json:"channel,omitempty"`
}

func (p *AdvancedParameters) normalize() error {
	if p.HighOccupancyBPS == 0 {
		p.HighOccupancyBPS = 8500
	}
	if p.LowOccupancyBPS == 0 {
		p.LowOccupancyBPS = 5000
	}
	if p.LastMinuteDays == 0 {
		p.LastMinuteDays = 3
	}
	if p.LowOccupancyBPS < 1 || p.HighOccupancyBPS > 10000 || p.LowOccupancyBPS >= p.HighOccupancyBPS || p.LastMinuteDays < 1 || p.LastMinuteDays > 30 || len(p.Channel) > 64 {
		return ErrPricing
	}
	return nil
}

type AdvancedContext struct {
	Kind       string             `json:"kind"`
	Policy     PricingPolicy      `json:"policy"`
	Forecast   DemandForecast     `json:"forecast"`
	Analytics  *AnalyticsResult   `json:"analytics"`
	Current    RateDefinition     `json:"current"`
	Today      string             `json:"today"`
	Now        time.Time          `json:"now"`
	Parameters AdvancedParameters `json:"parameters"`
}
type AdvancedProposal struct {
	Kind            string               `json:"kind"`
	Application     string               `json:"application"`
	AdvisoryReason  string               `json:"advisory_reason"`
	DateFrom        string               `json:"date_from"`
	DateTo          string               `json:"date_to"`
	OccupancyBPS    int64                `json:"projected_occupancy_bps"`
	Explanation     string               `json:"explanation"`
	Rate            *RateDefinition      `json:"rate,omitempty"`
	Restriction     *Restriction         `json:"restriction,omitempty"`
	Promotion       *PromotionDefinition `json:"promotion,omitempty"`
	PromotionCode   string               `json:"promotion_code,omitempty"`
	Advisory        map[string]any       `json:"advisory,omitempty"`
	Evidence        map[string]any       `json:"evidence"`
	Parameters      AdvancedParameters   `json:"parameters"`
	ModelProvenance ModelRef             `json:"model"`
	Confidence      string               `json:"confidence"`
}

func roundUpMilli(v int64) int64 { return (v + 999) / 1000 }

// RecommendAdvanced derives one advanced proposal from retained forecast and
// analytics facts. Kinds that the canonical engine cannot express are ADVISORY.
func RecommendAdvanced(c AdvancedContext) (AdvancedProposal, error) {
	p := c.Parameters
	if e := p.normalize(); e != nil || c.Policy.Validate() != nil || c.Current.Validate() != nil || c.Forecast.Status != "BASELINE_READY" || len(c.Forecast.Days) == 0 {
		return AdvancedProposal{}, ErrPricing
	}
	out := AdvancedProposal{Kind: c.Kind, DateFrom: c.Forecast.Days[0].Day, Parameters: p, Evidence: map[string]any{}, ModelProvenance: ModelRef{Key: c.Forecast.Model, Version: c.Forecast.AlgorithmVersion}, Confidence: c.Forecast.ConfidenceMethod}
	last, _ := parseDay(c.Forecast.Days[len(c.Forecast.Days)-1].Day)
	out.DateTo = last.AddDate(0, 0, 1).Format(time.DateOnly)
	var sold, capacity int64
	for _, d := range c.Forecast.Days {
		if d.ProjectedSoldMilliNights == nil {
			return out, ErrPricing
		}
		sold += *d.ProjectedSoldMilliNights
		capacity += d.PhysicalCapacity
	}
	if capacity <= 0 {
		return out, ErrNoRecommendation
	}
	occ, _ := Ratio(sold, 10000, capacity*1000)
	out.OccupancyBPS = occ
	out.Evidence["projected_sold_milli_nights"] = sold
	out.Evidence["physical_capacity_nights"] = capacity
	high, low := occ >= p.HighOccupancyBPS, occ <= p.LowOccupancyBPS
	discount := func() (int64, error) {
		gap := p.LowOccupancyBPS - occ
		bps, e := Ratio(gap, max(c.Policy.FeedbackGainBPS, 1), 10000)
		if e != nil {
			return 0, e
		}
		bps = min(max(bps, 100), c.Policy.MaxDailyChangeBPS)
		if bps <= 0 {
			return 0, ErrNoRecommendation
		}
		return bps, nil
	}
	start, _ := parseDay(out.DateFrom)
	today, e := parseDay(c.Today)
	if e != nil {
		return out, e
	}
	promotion := func(discountBPS int64, minNights, maxNights int, expires time.Time, channels []string) *PromotionDefinition {
		return &PromotionDefinition{Currency: c.Current.Currency, Discount: Adjustment{Kind: "PERCENT", BasisPoints: -discountBPS}, MaxDiscountBPS: discountBPS, MaxDiscountMinor: 0, DateFrom: out.DateFrom, DateTo: out.DateTo, EffectiveAt: c.Now.UTC().Truncate(time.Second), ExpiresAt: expires.UTC().Truncate(time.Second), MinNights: minNights, MaxNights: maxNights, Channels: channels, RatePlanIDs: []ids.ID{}, Priority: 10, PolicyReference: "RMS-ADVANCED:" + c.Kind}
	}
	analytics := c.Analytics
	switch c.Kind {
	case "RATE_RANGE":
		d := c.Policy.MaxDailyChangeBPS
		lowBand, highBand := -d/2, d/2
		if high {
			lowBand, highBand = 0, d
		} else if low {
			lowBand, highBand = -d, 0
		}
		lo, _ := Ratio(c.Current.AmountMinor, 10000+lowBand, 10000)
		hi, _ := Ratio(c.Current.AmountMinor, 10000+highBand, 10000)
		next := c.Current
		next.FloorMinor = max(lo, c.Policy.FloorMinor)
		next.CeilingMinor = min(hi, c.Policy.CeilingMinor)
		next.AmountMinor = min(max(next.AmountMinor, next.FloorMinor), next.CeilingMinor)
		if next.CeilingMinor < next.FloorMinor || next.Validate() != nil {
			return out, ErrNoRecommendation
		}
		if next.FloorMinor == c.Current.FloorMinor && next.CeilingMinor == c.Current.CeilingMinor {
			return out, ErrNoRecommendation
		}
		out.Application, out.Rate = "CANONICAL_RATE_VERSION", &next
		out.Explanation = fmt.Sprintf("Projected occupancy %d bps; band set within RMS floor/ceiling and maximum daily movement %d bps.", occ, d)
	case "MIN_LOS":
		if !high {
			return out, ErrNoRecommendation
		}
		out.Application, out.Restriction = "CANONICAL_RESTRICTION", &Restriction{MinNights: max(2, c.Current.MinNights)}
		out.Explanation = fmt.Sprintf("Projected occupancy %d bps meets the high-demand threshold %d bps; a two-night minimum protects peak nights.", occ, p.HighOccupancyBPS)
	case "MAX_LOS":
		if !high || analytics == nil || analytics.AverageLOSMilliNights == nil {
			return out, ErrNoRecommendation
		}
		limit := int(min(max(2, roundUpMilli(*analytics.AverageLOSMilliNights)*2), int64(c.Current.MaxNights)))
		out.Application, out.Restriction = "CANONICAL_RESTRICTION", &Restriction{MaxNights: limit}
		out.Evidence["average_los_milli_nights"] = *analytics.AverageLOSMilliNights
		out.Explanation = fmt.Sprintf("High projected occupancy; maximum stay set to twice the observed average length of stay (%d nights).", limit)
	case "BOOKING_WINDOW":
		if !high || analytics == nil || analytics.LeadTime.P90Days == nil {
			return out, ErrNoRecommendation
		}
		window := int(min(max(*analytics.LeadTime.P90Days, 7), 730))
		out.Application, out.Restriction = "CANONICAL_RESTRICTION", &Restriction{MaxAdvanceDays: window}
		out.Evidence["lead_time_p90_days"] = *analytics.LeadTime.P90Days
		out.Explanation = fmt.Sprintf("High projected occupancy; sales open no earlier than the observed 90th-percentile lead time (%d days).", window)
	case "EARLY_BOOKER":
		if !low || analytics == nil || analytics.LeadTime.MedianDays == nil {
			return out, ErrNoRecommendation
		}
		lead := *analytics.LeadTime.MedianDays
		expires := start.AddDate(0, 0, -int(lead))
		if !expires.After(today) {
			return out, ErrNoRecommendation
		}
		bps, e := discount()
		if e != nil {
			return out, e
		}
		out.Application, out.Promotion, out.PromotionCode = "CANONICAL_PROMOTION", promotion(bps, c.Current.MinNights, c.Current.MaxNights, expires, []string{}), "RMS-EB-"+out.DateFrom
		out.Evidence["lead_time_median_days"] = lead
		out.Explanation = fmt.Sprintf("Low projected occupancy %d bps; a %d bps discount is bookable only until the observed median lead time (%d days) before arrival.", occ, bps, lead)
	case "LAST_MINUTE":
		if !low || start.After(today.AddDate(0, 0, p.LastMinuteDays)) {
			return out, ErrNoRecommendation
		}
		bps, e := discount()
		if e != nil {
			return out, e
		}
		expires, _ := parseDay(out.DateTo)
		out.Application, out.Promotion, out.PromotionCode = "CANONICAL_PROMOTION", promotion(bps, c.Current.MinNights, c.Current.MaxNights, expires, []string{}), "RMS-LM-"+out.DateFrom
		out.Explanation = fmt.Sprintf("Low projected occupancy within %d days of arrival; %d bps last-minute discount.", p.LastMinuteDays, bps)
	case "LONG_STAY":
		if !low || analytics == nil || analytics.AverageLOSMilliNights == nil {
			return out, ErrNoRecommendation
		}
		minNights := int(max(3, roundUpMilli(*analytics.AverageLOSMilliNights)+1))
		if minNights > c.Current.MaxNights {
			return out, ErrNoRecommendation
		}
		bps, e := discount()
		if e != nil {
			return out, e
		}
		expires, _ := parseDay(out.DateTo)
		out.Application, out.Promotion, out.PromotionCode = "CANONICAL_PROMOTION", promotion(bps, minNights, c.Current.MaxNights, expires, []string{}), "RMS-LS-"+out.DateFrom
		out.Evidence["average_los_milli_nights"] = *analytics.AverageLOSMilliNights
		out.Explanation = fmt.Sprintf("Low projected occupancy; stays of at least %d nights (above observed average) receive %d bps.", minNights, bps)
	case "CHANNEL_PRICING":
		if p.Channel == "" || analytics == nil {
			return out, ErrNoRecommendation
		}
		var share *SegmentAnalysis
		for i := range analytics.Channels {
			if analytics.Channels[i].Key == p.Channel {
				share = &analytics.Channels[i]
			}
		}
		if share == nil {
			return out, ErrNoRecommendation
		}
		out.Evidence["channel"] = share
		if high {
			out.Application, out.AdvisoryReason = "ADVISORY", "A channel-specific markup requires a canonical rate rule that would make the base plan conditional; the operator must apply it through the rate-plan rule command."
			out.Advisory = map[string]any{"channel": p.Channel, "markup_bps": c.Policy.MaxDailyChangeBPS}
			out.Explanation = "High projected occupancy; channel markup recommended as advisory."
			break
		}
		if !low {
			return out, ErrNoRecommendation
		}
		bps, e := discount()
		if e != nil {
			return out, e
		}
		expires, _ := parseDay(out.DateTo)
		out.Application, out.Promotion, out.PromotionCode = "CANONICAL_PROMOTION", promotion(bps, c.Current.MinNights, c.Current.MaxNights, expires, []string{p.Channel}), "RMS-CH-"+out.DateFrom
		out.Explanation = fmt.Sprintf("Low projected occupancy; %d bps channel-scoped discount for %s.", bps, p.Channel)
	case "DERIVED_RATE":
		if c.Current.ParentVersionID == nil || len(c.Current.Adjustments) != 1 || c.Current.Adjustments[0].Kind != "PERCENT" {
			out.Application, out.AdvisoryReason = "ADVISORY", "The target rate is not a single-percentage derived rate; derived-rate adjustment cannot be applied canonically."
			out.Explanation = "Derived-rate change is advisory for a non-derived target."
			out.Advisory = map[string]any{"projected_occupancy_bps": occ}
			break
		}
		change, e := Ratio(occ-c.Policy.TargetOccupancyBPS, c.Policy.FeedbackGainBPS, 10000)
		if e != nil {
			return out, e
		}
		change = max(-c.Policy.MaxDailyChangeBPS, min(c.Policy.MaxDailyChangeBPS, change))
		if change == 0 {
			return out, ErrNoRecommendation
		}
		next := c.Current
		next.Adjustments = []Adjustment{{Kind: "PERCENT", BasisPoints: c.Current.Adjustments[0].BasisPoints + change}}
		if next.Validate() != nil {
			return out, ErrNoRecommendation
		}
		out.Application, out.Rate = "CANONICAL_RATE_VERSION", &next
		out.Explanation = fmt.Sprintf("Derived adjustment moves %d bps toward target occupancy %d bps.", change, c.Policy.TargetOccupancyBPS)
	case "CHANNEL_ALLOCATION":
		if analytics == nil || len(analytics.Channels) == 0 {
			return out, ErrNoRecommendation
		}
		unsold := max(capacity*1000-sold, 0)
		allocation := []map[string]any{}
		for _, ch := range analytics.Channels {
			if ch.ShareBPS == nil {
				continue
			}
			q, _ := Ratio(unsold, *ch.ShareBPS, 10000*1000)
			allocation = append(allocation, map[string]any{"channel": ch.Key, "recommended_room_nights": q, "observed_share_bps": *ch.ShareBPS})
		}
		out.Application, out.AdvisoryReason = "ADVISORY", "Channel allotments commit physical stock to a provider and are created only through the channel-owned allotment command with confirmed mappings."
		out.Advisory = map[string]any{"unsold_milli_nights": unsold, "allocation": allocation}
		out.Explanation = "Projected unsold nights allocated in proportion to observed channel share."
	case "OVERBOOKING":
		if analytics == nil || analytics.CancellationRateBPS == nil || analytics.NoShowRateBPS == nil {
			return out, ErrNoRecommendation
		}
		rate := *analytics.CancellationRateBPS + *analytics.NoShowRateBPS
		days := []map[string]any{}
		for _, d := range c.Forecast.Days {
			q, _ := Ratio(d.CurrentConfirmedNights, rate, 10000)
			days = append(days, map[string]any{"day": d.Day, "recommended_excess_rooms": q})
		}
		out.Application, out.AdvisoryReason = "ADVISORY", "Overbooking is a controlled advisory action; excess sales require the separate reservation overbooking policy and independent approval."
		out.Advisory = map[string]any{"expected_attrition_bps": rate, "days": days}
		out.Explanation = fmt.Sprintf("Observed cancellation plus no-show attrition %d bps applied to on-books.", rate)
	default:
		return out, ErrPricing
	}
	return out, nil
}
