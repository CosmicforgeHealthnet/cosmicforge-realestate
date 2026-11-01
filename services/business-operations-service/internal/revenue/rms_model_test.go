package revenue

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func forecastDatasetFixture() (DemandDataset, ForecastConfiguration, string, string) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	d := DemandDataset{Currency: "NGN", Timezone: "UTC", Basis: "CONFIRMED_COMMITTED_DEMAND", AsOf: now, Days: []DemandDay{}, MissingInputs: []string{"realized_stays", "payment_reconciled_revenue", "marketplace_conversion_attribution", "calibrated_price_elasticity"}}
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for date := start.AddDate(0, 0, -56); date.Before(start.AddDate(0, 0, 7)); date = date.AddDate(0, 0, 1) {
		amount := int64(50000)
		d.Days = append(d.Days, DemandDay{Day: date.Format(time.DateOnly), Capacity: 10, ConfirmedNights: 5, CalendarVersion: 1, AccommodationMinor: &amount})
	}
	return d, ForecastConfiguration{HistoryDays: 56, MinimumComparableDays: 3, MinimumHistoryCoverageBPS: 8000}, start.Format(time.DateOnly), start.AddDate(0, 0, 7).Format(time.DateOnly)
}
func TestRMSBaselineForecastIsReproducibleAndExplainsCoverage(t *testing.T) {
	d, c, from, to := forecastDatasetFixture()
	a, e := ForecastDemand(d, c, from, to)
	if e != nil {
		t.Fatal(e)
	}
	b, e := ForecastDemand(d, c, from, to)
	if e != nil {
		t.Fatal(e)
	}
	ar, _ := json.Marshal(a)
	br, _ := json.Marshal(b)
	if string(ar) != string(br) || a.Status != "BASELINE_READY" || a.IsCalibratedProbability || a.HistoryCoverageBPS != 10000 || len(a.Days) != 7 {
		t.Fatalf("non-reproducible or overstated forecast %+v", a)
	}
	for _, day := range a.Days {
		if day.ComparableDays != 8 || day.PotentialDemandMilliNights == nil || *day.PotentialDemandMilliNights != 5000 || day.ProjectedSoldMilliNights == nil || *day.ProjectedSoldMilliNights != 5000 || day.Explanation == "" {
			t.Fatalf("incorrect weekday projection %+v", day)
		}
	}
}
func TestRMSMissingHistoryRemainsUnknownAndCannotBecomeZeroDemand(t *testing.T) {
	d, c, from, to := forecastDatasetFixture()
	d.Days = d.Days[len(d.Days)-7:]
	f, e := ForecastDemand(d, c, from, to)
	if e != nil {
		t.Fatal(e)
	}
	if f.Status != "INSUFFICIENT_DATA" || f.HistoryCoverageBPS != 0 {
		t.Fatalf("invented history %+v", f)
	}
	for _, day := range f.Days {
		if day.PotentialDemandMilliNights != nil || day.ProjectedSoldMilliNights != nil || day.CurrentConfirmedNights != 5 {
			t.Fatalf("unknown forecast lost its known commitments %+v", day)
		}
	}
}
func TestRMSForecastCannotManufactureCapacityFromExcessDemand(t *testing.T) {
	d, c, from, to := forecastDatasetFixture()
	for i := len(d.Days) - 7; i < len(d.Days); i++ {
		d.Days[i].ConfirmedNights = 12
		d.Days[i].OverbookedNights = 2
	}
	f, e := ForecastDemand(d, c, from, to)
	if e != nil {
		t.Fatal(e)
	}
	for _, day := range f.Days {
		if *day.PotentialDemandMilliNights != 12000 || *day.ProjectedSoldMilliNights != 10000 || day.PhysicalCapacity != 10 {
			t.Fatalf("forecast invented physical capacity %+v", day)
		}
	}
}
func TestRMSForecastRejectsDuplicateAndFutureTrainingLeakage(t *testing.T) {
	d, c, from, to := forecastDatasetFixture()
	d.Days = append(d.Days, d.Days[0])
	if _, e := ForecastDemand(d, c, from, to); e == nil {
		t.Fatal("duplicate source day accepted")
	}
	d, c, from, to = forecastDatasetFixture()
	for i := len(d.Days) - 7; i < len(d.Days); i++ {
		d.Days[i].ConfirmedNights = 100
	}
	f, e := ForecastDemand(d, c, from, to)
	if e != nil {
		t.Fatal(e)
	}
	// Forward bookings remain an explicit current lower bound, never historical
	// comparable observations or extra confidence.
	for _, day := range f.Days {
		if day.ComparableDays != 8 || *day.ProjectedSoldMilliNights != 10000 {
			t.Fatal("future bookings contaminated comparable history")
		}
	}
}
func TestRMSMetricsKeepUnknownAndZeroDenominatorsDistinct(t *testing.T) {
	d, _, _, _ := forecastDatasetFixture()
	m, e := MetricsFromDemand(d)
	if e != nil {
		t.Fatal(e)
	}
	if m.BookedOccupancyBPS == nil || *m.BookedOccupancyBPS != 5000 || m.ContractualADRMinor == nil || *m.ContractualADRMinor != 10000 || m.ContractualRevPARMinor == nil || *m.ContractualRevPARMinor != 5000 || m.RealizedADRMinor != nil || m.NetRevenueMinor != nil || m.ConversionBPS != nil {
		t.Fatalf("mixed contractual and financial truth %+v", m)
	}
	d.Days[0].AccommodationMinor = nil
	m, e = MetricsFromDemand(d)
	if e != nil {
		t.Fatal(e)
	}
	if m.AcceptedAccommodationMinor != nil || m.ContractualADRMinor != nil || m.ContractualRevPARMinor != nil {
		t.Fatal("unknown attribution became zero")
	}
	d.Days = []DemandDay{{Day: "2026-11-01", CalendarVersion: 1}}
	m, e = MetricsFromDemand(d)
	if e != nil {
		t.Fatal(e)
	}
	if m.BookedOccupancyBPS != nil || m.ContractualADRMinor != nil || m.ContractualRevPARMinor != nil {
		t.Fatal("zero denominators became real metrics")
	}
}
func TestRMSMonetaryOverflowFailsRatherThanWrapping(t *testing.T) {
	d, _, _, _ := forecastDatasetFixture()
	v := int64(math.MaxInt64)
	d.Days[0].AccommodationMinor = &v
	d.Days[1].AccommodationMinor = &v
	if _, e := MetricsFromDemand(d); e == nil {
		t.Fatal("overflow fabricated a revenue value")
	}
}
