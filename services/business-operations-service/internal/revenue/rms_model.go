package revenue

import (
	"fmt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"slices"
	"time"
)

const BaselineForecastModel = "canonical-weekday-demand-v1"

// DemandDay records owned confirmed commitments, not independently verified
// physical occupancy or realized cash. Missing calendar rows remain missing.
type DemandDay struct {
	Day                string `json:"day"`
	Capacity           int64  `json:"capacity"`
	Blocked            int64  `json:"blocked"`
	ConfirmedNights    int64  `json:"confirmed_nights"`
	HeldNights         int64  `json:"held_nights"`
	OverbookedNights   int64  `json:"overbooked_nights"`
	SellableNights     int64  `json:"sellable_nights"`
	CalendarVersion    int64  `json:"calendar_version"`
	AccommodationMinor *int64 `json:"accommodation_minor"`
}
type DemandDataset struct {
	Currency      string      `json:"currency"`
	Timezone      string      `json:"timezone"`
	Basis         string      `json:"basis"`
	AsOf          time.Time   `json:"as_of"`
	Days          []DemandDay `json:"days"`
	MissingInputs []string    `json:"missing_inputs"`
}
type ForecastConfiguration struct {
	HistoryDays               int   `json:"history_days"`
	MinimumComparableDays     int   `json:"minimum_comparable_days"`
	MinimumHistoryCoverageBPS int64 `json:"minimum_history_coverage_bps"`
}

func (c ForecastConfiguration) Validate() error {
	if c.HistoryDays < 21 || c.HistoryDays > 366 || c.MinimumComparableDays < 3 || c.MinimumComparableDays > 52 || c.MinimumHistoryCoverageBPS < 5000 || c.MinimumHistoryCoverageBPS > 10000 {
		return ErrPricing
	}
	return nil
}

type ForecastDay struct {
	Day                        string `json:"day"`
	PhysicalCapacity           int64  `json:"physical_capacity"`
	CurrentConfirmedNights     int64  `json:"current_confirmed_nights"`
	ComparableDays             int    `json:"comparable_days"`
	PotentialDemandMilliNights *int64 `json:"potential_demand_milli_nights"`
	ProjectedSoldMilliNights   *int64 `json:"projected_sold_milli_nights"`
	Status                     string `json:"status"`
	Explanation                string `json:"explanation"`
}
type DemandForecast struct {
	Model                   string        `json:"model"`
	AlgorithmVersion        int           `json:"algorithm_version"`
	Status                  string        `json:"status"`
	HistoryCoverageBPS      int64         `json:"history_coverage_bps"`
	ConfidenceMethod        string        `json:"confidence_method"`
	IsCalibratedProbability bool          `json:"is_calibrated_probability"`
	Days                    []ForecastDay `json:"days"`
	MissingInputs           []string      `json:"missing_inputs"`
	Assumptions             []string      `json:"assumptions"`
}

// ForecastDemand is deterministic over an immutable retained dataset/config.
// It models recorded committed demand with same-weekday capacity-weighted
// comparisons. Coverage is explicitly not a statistical probability. It never
// predicts from an absent day as though the day had zero bookings/capacity.
func ForecastDemand(data DemandDataset, config ForecastConfiguration, from, to string) (DemandForecast, error) {
	out := DemandForecast{Model: BaselineForecastModel, AlgorithmVersion: 1, Status: "INSUFFICIENT_DATA", ConfidenceMethod: "OBSERVED_CAPACITY_DAY_COVERAGE_NOT_CALIBRATED", Days: []ForecastDay{}, MissingInputs: append([]string{}, data.MissingInputs...), Assumptions: []string{"Confirmed committed nights represent recorded demand, not realized stays or verified financial revenue.", "Comparable weekday demand is capacity-weighted; unknown historical days are excluded and reduce coverage.", "No calibrated price elasticity, search conversion, holiday/event or competitor effect is inferred.", "Projected nights use fixed-point thousandths; no floating-point monetary arithmetic is used. Projected sold nights cannot exceed applicable physical capacity; overbooking remains a separate approval-controlled action."}}
	if config.Validate() != nil || len(data.Days) > 732 || data.Basis != "CONFIRMED_COMMITTED_DEMAND" || data.AsOf.IsZero() {
		return out, ErrPricing
	}
	if _, e := money.ParseCurrency(data.Currency); e != nil {
		return out, e
	}
	loc, e := time.LoadLocation(data.Timezone)
	if e != nil {
		return out, ErrPricing
	}
	start, e := time.ParseInLocation(time.DateOnly, from, time.UTC)
	if e != nil {
		return out, ErrPricing
	}
	end, e := time.ParseInLocation(time.DateOnly, to, time.UTC)
	if e != nil || !start.Before(end) || end.After(start.AddDate(0, 0, 90)) {
		return out, ErrPricing
	}
	// Training ends at the observation's local civil day, not at a future
	// forecast start (which would leak forward bookings into historical truth).
	cutoff, e := time.ParseInLocation(time.DateOnly, data.AsOf.In(loc).Format(time.DateOnly), time.UTC)
	if e != nil || start.Before(cutoff) || end.After(cutoff.AddDate(0, 0, 90)) {
		return out, ErrPricing
	}
	historyStart := cutoff.AddDate(0, 0, -config.HistoryDays)
	byDate := map[string]DemandDay{}
	counts := [7]int{}
	capacity := [7]int64{}
	demand := [7]int64{}
	observed := int64(0)
	for _, day := range data.Days {
		date, e := time.ParseInLocation(time.DateOnly, day.Day, time.UTC)
		if e != nil || date.Format(time.DateOnly) != day.Day || day.Capacity < 0 || day.Capacity > 100000 || day.Blocked < 0 || day.Blocked > day.Capacity || day.ConfirmedNights < 0 || day.ConfirmedNights > 1001000 || day.OverbookedNights < 0 || day.OverbookedNights > 1000 || day.CalendarVersion < 1 || day.HeldNights < 0 || day.SellableNights < 0 {
			return out, ErrPricing
		}
		if _, exists := byDate[day.Day]; exists {
			return out, ErrPricing
		}
		byDate[day.Day] = day
		if !date.Before(historyStart) && date.Before(cutoff) {
			observed++
			eligible := day.Capacity - day.Blocked
			if eligible > 0 {
				weekday := int(date.Weekday())
				counts[weekday]++
				capacity[weekday] += eligible
				demand[weekday] += day.ConfirmedNights
			}
		}
	}
	out.HistoryCoverageBPS, e = Ratio(observed, 10000, int64(config.HistoryDays))
	if e != nil {
		return out, e
	}
	ready := true
	for date := start; date.Before(end); date = date.AddDate(0, 0, 1) {
		key := date.Format(time.DateOnly)
		weekday := int(date.Weekday())
		day, known := byDate[key]
		point := ForecastDay{Day: key, Status: "INSUFFICIENT_DATA", ComparableDays: counts[weekday], CurrentConfirmedNights: day.ConfirmedNights}
		if known {
			point.PhysicalCapacity = day.Capacity - day.Blocked
		}
		if !known || counts[weekday] < config.MinimumComparableDays || capacity[weekday] == 0 || out.HistoryCoverageBPS < config.MinimumHistoryCoverageBPS {
			ready = false
			point.Explanation = "A known target calendar, sufficient observed comparable weekdays and the configured historical coverage are required."
		} else {
			prediction, e := Ratio(point.PhysicalCapacity*1000, demand[weekday], capacity[weekday])
			if e != nil {
				return out, e
			}
			prediction = max(prediction, day.ConfirmedNights*1000)
			sold := min(prediction, point.PhysicalCapacity*1000)
			point.PotentialDemandMilliNights = &prediction
			point.ProjectedSoldMilliNights = &sold
			point.Status = "BASELINE_READY"
			point.Explanation = fmt.Sprintf("%d comparable weekdays; %d recorded demand nights / %d applicable capacity nights. Current accepted demand is a lower bound; physical sales are capped.", counts[weekday], demand[weekday], capacity[weekday])
		}
		out.Days = append(out.Days, point)
	}
	if ready {
		out.Status = "BASELINE_READY"
	}
	slices.Sort(out.MissingInputs)
	return out, nil
}
