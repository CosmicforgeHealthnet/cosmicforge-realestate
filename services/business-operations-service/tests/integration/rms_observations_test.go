package integration

import (
	"encoding/json"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"testing"
	"time"
)

func rmsSnapshotCommand(c *commercialHarness) businesshttp.RMSDemandCommand {
	return businesshttp.RMSDemandCommand{PropertyID: c.property.ID, PoolID: c.pool.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, HistoryDays: 56}
}

func rmsOwnedHistoryFixture(t *testing.T) (*commercialHarness, businesshttp.RMSForecastRun) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	loc, e := time.LoadLocation(c.property.Timezone)
	if e != nil {
		t.Fatal(e)
	}
	today, e := time.Parse(time.DateOnly, time.Now().In(loc).Format(time.DateOnly))
	if e != nil {
		t.Fatal(e)
	}
	asOfDate := today.AddDate(0, 0, 30)
	history := inventory.DateRange{DateFrom: asOfDate.AddDate(0, 0, -56).Format(time.DateOnly), DateTo: asOfDate.AddDate(0, 0, 1).Format(time.DateOnly)}
	days, e := history.Days(c.property.Timezone)
	if e != nil {
		t.Fatal(e)
	}
	versions := map[string]int64{}
	for _, day := range days {
		versions[day] = 0
		if day >= c.dates.DateFrom && day < c.dates.DateTo {
			versions[day] = 1
		}
	}
	capacity := 1
	c.must(c.owner, "PUT", "/inventory", businesshttp.CommercialCalendarCommand{PoolID: c.pool.ID, DateFrom: history.DateFrom, DateTo: history.DateTo, Capacity: &capacity, ExpectedVersions: versions, Reason: "Configure explicit physical calendar facts for a disposable model scenario"}, 200)
	for _, offset := range []int{2, 9, 16, 23} {
		arrival := today.AddDate(0, 0, offset)
		q := commercialData[revenue.QuoteSnapshot](t, c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: arrival.Format(time.DateOnly), DateTo: arrival.AddDate(0, 0, 1).Format(time.DateOnly), Adults: 2, Quantity: 1}, 201))
		in := reservations.Create{PropertyID: c.property.ID, Source: "DIRECT", State: "HOLD", PaymentResponsibility: "PAY_AT_PROPERTY", Rooms: []reservations.RoomCommand{{ProductID: q.ProductID, PoolVersion: q.PoolVersion, ProductVersion: q.ProductVersion, QuoteID: q.QuoteID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1}}, Reason: "Record a real canonical test booking rather than seed invented forecast counts"}
		op := in
		in.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &q.AmountMinor, q.Currency, map[string]any{"command": op})
		v := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", in, 201))
		c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/confirm", reservations.Command{Version: v.Reservation.Version, Reason: "Commit actual test booking and inventory provenance"}, 200)
	}
	// Explicit disposable clock/provider projection fixture. It is not a real
	// historical dataset or live verification certification.
	clock, e := time.ParseInLocation("2006-01-02 15:04", asOfDate.Format(time.DateOnly)+" 12:00", loc)
	if e != nil {
		t.Fatal(e)
	}
	c.service.Now = func() time.Time { return clock }
	for _, statement := range []string{`UPDATE verification_links SET expires_at=$2 WHERE organisation_id=$1`, `UPDATE organisation_capacities SET expires_at=$2 WHERE organisation_id=$1`, `UPDATE inventory_verification_links SET expires_at=$2,version=version+1 WHERE organisation_id=$1`} {
		if _, e = c.db.Pool.Exec(t.Context(), statement, c.owner.registered.OrganisationID, clock.Add(time.Hour)); e != nil {
			t.Fatal(e)
		}
	}
	snapshot := commercialData[businesshttp.RMSDemandSnapshot](t, c.must(c.owner, "POST", "/revenue/demand-snapshots", businesshttp.RMSDemandCommand{PropertyID: c.property.ID, PoolID: c.pool.ID, DateFrom: asOfDate.Format(time.DateOnly), DateTo: history.DateTo, HistoryDays: 56}, 201))
	forecast := commercialData[businesshttp.RMSForecastRun](t, c.must(c.owner, "POST", "/revenue/forecasts", rmsForecastCommand(snapshot), 201))
	return c, forecast
}
func TestBusinessRMSBaselineForecastUsesActualOwnedBookingHistory(t *testing.T) {
	_, forecast := rmsOwnedHistoryFixture(t)
	if forecast.Status != "BASELINE_READY" || forecast.Result.HistoryCoverageBPS != 10000 || forecast.Result.IsCalibratedProbability || len(forecast.Result.Days) != 1 {
		t.Fatalf("owned historical forecast %+v", forecast)
	}
	point := forecast.Result.Days[0]
	if point.ComparableDays != 8 || point.PotentialDemandMilliNights == nil || *point.PotentialDemandMilliNights != 500 || point.ProjectedSoldMilliNights == nil || *point.ProjectedSoldMilliNights != 500 {
		t.Fatalf("fractional observed demand was rounded into fabricated full rooms %+v", point)
	}
}
func rmsForecastCommand(id businesshttp.RMSDemandSnapshot) businesshttp.RMSForecastCommand {
	return businesshttp.RMSForecastCommand{SnapshotID: id.ID, Configuration: revenue.ForecastConfiguration{HistoryDays: 56, MinimumComparableDays: 3, MinimumHistoryCoverageBPS: 8000}}
}
func TestBusinessRMSRetainsRealObservationsAndUnknownForecastsWithoutRateChanges(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	accepted := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, true), 201))
	accepted = commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+accepted.Reservation.ID.String()+"/confirm", reservations.Command{Version: accepted.Reservation.Version, Reason: "Commit actual canonical demand for the analytical observation"}, 200))
	in := rmsSnapshotCommand(c)
	key := "retained-observation-replay"
	status, raw := c.call(c.owner, "POST", "/revenue/demand-snapshots", in, map[string]string{"Idempotency-Key": key})
	if status != 201 {
		t.Fatalf("capture %d %s", status, raw)
	}
	snapshot := commercialData[businesshttp.RMSDemandSnapshot](t, raw)
	if len(snapshot.Dataset.Days) != 2 || len(snapshot.SourceDigest) != 64 {
		t.Fatalf("invented or unbound observed history %+v", snapshot)
	}
	for _, day := range snapshot.Dataset.Days {
		if day.ConfirmedNights != 1 || day.AccommodationMinor == nil || *day.AccommodationMinor != 10000 {
			t.Fatalf("incorrect actual accepted attribution %+v", day)
		}
	}
	metrics := commercialData[businesshttp.RMSMetricsResult](t, c.must(c.owner, "GET", "/revenue/metrics?snapshot_id="+snapshot.ID.String(), nil, 200))
	if metrics.Metrics.BookedOccupancyBPS == nil || *metrics.Metrics.BookedOccupancyBPS != 10000 || metrics.Metrics.ContractualADRMinor == nil || *metrics.Metrics.ContractualADRMinor != 10000 || metrics.Metrics.NetRevenueMinor != nil || metrics.Metrics.RealizedADRMinor != nil || metrics.Metrics.ConversionBPS != nil {
		t.Fatalf("contractual/realized truth mixed %+v", metrics)
	}
	forecast := commercialData[businesshttp.RMSForecastRun](t, c.must(c.owner, "POST", "/revenue/forecasts", rmsForecastCommand(snapshot), 201))
	if forecast.Status != "INSUFFICIENT_DATA" || forecast.Result.HistoryCoverageBPS != 0 || forecast.Result.IsCalibratedProbability {
		t.Fatalf("fabricated sufficient history %+v", forecast)
	}
	for _, day := range forecast.Result.Days {
		if day.PotentialDemandMilliNights != nil || day.ProjectedSoldMilliNights != nil || day.CurrentConfirmedNights != 1 {
			t.Fatalf("unknown prediction became zero or lost actual demand %+v", day)
		}
	}
	calculated, e := revenue.ForecastDemand(snapshot.Dataset, forecast.Configuration, snapshot.DateFrom, snapshot.DateTo)
	if e != nil {
		t.Fatal(e)
	}
	want, _ := json.Marshal(calculated)
	got, _ := json.Marshal(forecast.Result)
	if string(want) != string(got) {
		t.Fatal("stored forecast cannot be deterministically replayed")
	}
	c.must(c.owner, "GET", "/revenue/forecasts/"+forecast.ID.String(), nil, 200)
	c.must(c.owner, "GET", "/revenue/demand-snapshots", nil, 200)
	c.must(c.owner, "GET", "/revenue/forecasts", nil, 200)
	firstPace := commercialData[businesshttp.RMSPaceResult](t, c.must(c.owner, "GET", "/revenue/demand-snapshots/"+snapshot.ID.String()+"/pace", nil, 200))
	if firstPace.State != "NO_EARLIER_OBSERVATION" || firstPace.PreviousSnapshotID != nil {
		t.Fatal("invented an earlier booking pace observation")
	}
	for _, point := range firstPace.Days {
		if point.PickupNights != nil || point.ContractualRevenueChangeMinor != nil {
			t.Fatal("unknown pickup became zero")
		}
	}
	if _, e = c.db.Pool.Exec(t.Context(), `UPDATE demand_snapshots SET dataset='{}' WHERE id=$1`, snapshot.ID); e == nil {
		t.Fatal("retained observation mutable")
	}
	if _, e = c.db.Pool.Exec(t.Context(), `DELETE FROM forecast_runs WHERE id=$1`, forecast.ID); e == nil {
		t.Fatal("forecast history mutable")
	}
	if _, e = c.db.Pool.Exec(t.Context(), `UPDATE forecast_models SET definition='{}'`); e == nil {
		t.Fatal("registered model silently editable")
	}
	var rateVersion int64
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT version FROM rate_plans WHERE id=$1`, c.rate.ID).Scan(&rateVersion); e != nil || rateVersion != c.rate.Version {
		t.Fatal("analytics mutated canonical rates", e)
	}
	c.must(c.owner, "POST", "/reservations/"+accepted.Reservation.ID.String()+"/cancel", reservations.Command{Version: accepted.Reservation.Version, Reason: "Release canonical demand between actual analytical observations"}, 200)
	observationTime := snapshot.Dataset.AsOf.Add(time.Minute)
	c.service.Now = func() time.Time { return observationTime }
	second := commercialData[businesshttp.RMSDemandSnapshot](t, c.must(c.owner, "POST", "/revenue/demand-snapshots", in, 201))
	pace := commercialData[businesshttp.RMSPaceResult](t, c.must(c.owner, "GET", "/revenue/demand-snapshots/"+second.ID.String()+"/pace", nil, 200))
	if pace.State != "COMPARABLE" || pace.PreviousSnapshotID == nil || *pace.PreviousSnapshotID != snapshot.ID {
		t.Fatalf("lost actual observation lineage %+v", pace)
	}
	for _, day := range pace.Days {
		if day.PickupNights == nil || *day.PickupNights != -1 || day.ContractualRevenueChangeMinor == nil || *day.ContractualRevenueChangeMinor != -10000 {
			t.Fatalf("incorrect released-demand pickup %+v", day)
		}
	}
	status, replayed := c.call(c.owner, "POST", "/revenue/demand-snapshots", in, map[string]string{"Idempotency-Key": key})
	if status != 201 || commercialData[businesshttp.RMSDemandSnapshot](t, replayed).ID != snapshot.ID {
		t.Fatal("observation retry fabricated a new snapshot")
	}
	revision := rmsForecastCommand(second)
	revision.SupersedesID = &forecast.ID
	newForecast := commercialData[businesshttp.RMSForecastRun](t, c.must(c.owner, "POST", "/revenue/forecasts", revision, 201))
	if newForecast.SupersedesID == nil || *newForecast.SupersedesID != forecast.ID {
		t.Fatal("forecast revision erased its source lineage")
	}
}
func TestBusinessRMSObservationScopeCannotBeConfusedBySourceIdentifiers(t *testing.T) {
	c := newCommercialHTTP(t)
	snapshot := commercialData[businesshttp.RMSDemandSnapshot](t, c.must(c.owner, "POST", "/revenue/demand-snapshots", rmsSnapshotCommand(c), 201))
	outsider := c.user()
	c.must(outsider, "GET", "/revenue/demand-snapshots/"+snapshot.ID.String(), nil, 404)
	limited := c.user()
	c.join(limited, c.owner, []string{"rms:read", "rms:write"})
	branch := c.inventoryBranch(c.owner)
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET scope_kind='BRANCH',scope_id=$3 WHERE organisation_id=$1 AND membership_id=(SELECT id FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2) AND capability IN ('rms:read','rms:write')`, c.owner.registered.OrganisationID, limited.registered.AccountID, branch); e != nil {
		t.Fatal(e)
	}
	c.must(limited, "GET", "/revenue/demand-snapshots/"+snapshot.ID.String(), nil, 403)
	c.must(limited, "GET", "/revenue/metrics?snapshot_id="+snapshot.ID.String(), nil, 403)
	c.must(limited, "POST", "/revenue/forecasts", rmsForecastCommand(snapshot), 403)
	c.must(limited, "POST", "/revenue/demand-snapshots", rmsSnapshotCommand(c), 403)
	type list struct {
		Items []json.RawMessage `json:"items"`
	}
	out := commercialData[list](t, c.must(limited, "GET", "/revenue/demand-snapshots?limit=1", nil, 200))
	if len(out.Items) != 0 {
		t.Fatal("scope filtered after pagination")
	}
}
