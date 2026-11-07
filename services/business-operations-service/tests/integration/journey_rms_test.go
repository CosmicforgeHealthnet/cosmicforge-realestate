package integration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Journey 8: actual canonical booking data -> source-backed analytics and
// forecast -> recommendation -> authorized autopilot -> canonical rate version
// -> durable Channel Manager delta -> outcome measurement -> drift detection ->
// automatic safety pause -> human finding resolution.
func TestBusinessJourneyRMSBookingDataToAutopilotChannelAndMonitoring(t *testing.T) {
	x := newRMSAutomationFixture(t)
	org := x.owner.registered.OrganisationID
	// 1. Actual booking history (four confirmed canonical reservations) feeds analytics.
	snapshot := x.analytics("PROPERTY", x.property.ID)
	if snapshot.Result.LeadTime.Bookings != 4 || snapshot.Result.Status != "BASELINE_READY" {
		t.Fatalf("analytics from canonical bookings %+v", snapshot.Result)
	}
	// 2. Explicit authorized activation of bounded autopilot.
	p := x.policy("AUTOPILOT_WITH_LIMITS", nil)
	limits := rmsTestLimits()
	limits.DriftThresholdBPS = 2000
	limits.DriftMinMeasurements = 1
	limits.AutoPauseOnDrift = true
	a := x.activate(p, "AUTOPILOT_WITH_LIMITS", limits)
	historyBefore := x.count(`SELECT count(*) FROM commercial_history WHERE organisation_id=$1 AND entity_id=$2`, org, x.rate.ID)
	// 3. Worker: capture -> versioned forecast -> recommendation -> bounded application.
	x.process()
	run := x.runs(a.ID)[0]
	if run.Stage != "APPLIED" || run.SnapshotID == nil || run.ForecastID == nil || run.RecommendationID == nil {
		t.Fatalf("pipeline run %+v", run)
	}
	forecast := commercialData[businesshttp.RMSForecastRun](t, x.must(x.owner, "GET", "/revenue/forecasts/"+run.ForecastID.String(), nil, 200))
	if forecast.Result.Model != revenue.BaselineForecastModel || forecast.Result.AlgorithmVersion != 1 || forecast.SourceDigest == "" {
		t.Fatalf("versioned forecast %+v", forecast)
	}
	if x.rateAmount() != 9750 {
		t.Fatalf("canonical rate %d", x.rateAmount())
	}
	// 4. Channel Manager delta: the canonical history the channel maintenance
	// worker consumes, plus the rate-plan and pricing events in the outbox.
	if x.count(`SELECT count(*) FROM commercial_history WHERE organisation_id=$1 AND entity_id=$2`, org, x.rate.ID) <= historyBefore || x.count(`SELECT count(*) FROM outbox_events WHERE event_type IN ('pricing.applied.v1','rate_plan.changed.v1') AND payload->>'organisation_id'=$1`, org.String()) < 2 {
		t.Fatal("durable channel delta missing")
	}
	// 5. Monitoring: after the stay date the outcome is measured against the
	// retained forecast; a defined drift condition pauses automation.
	x.advance(48 * time.Hour)
	x.process()
	var status string
	var result []byte
	if e := x.db.Pool.QueryRow(t.Context(), `SELECT status,result FROM rms_outcome_measurements WHERE recommendation_id=$1 AND status='MEASURED'`, *run.RecommendationID).Scan(&status, &result); e != nil {
		t.Fatalf("measured outcome missing: %v", e)
	}
	var outcome struct {
		Points        []revenue.OutcomePoint `json:"points"`
		ForecastError revenue.DriftResult    `json:"forecast_error"`
		Realized      bool                   `json:"realized_financial_truth"`
	}
	if e := json.Unmarshal(result, &outcome); e != nil || len(outcome.Points) != 1 || outcome.Points[0].PredictedMilliNights != 500 || outcome.ForecastError.MeanAbsErrBPS == nil || *outcome.ForecastError.MeanAbsErrBPS != 5000 || outcome.Realized {
		t.Fatalf("outcome %+v %v", outcome, e)
	}
	if got := x.activation(a.ID); got.State != "PAUSED" {
		t.Fatalf("drift did not pause automation %+v", got)
	}
	findings := commercialData[struct {
		Items []map[string]any `json:"items"`
	}](t, x.must(x.owner, "GET", "/revenue/monitoring-findings?property_id="+x.property.ID.String(), nil, 200))
	var drift map[string]any
	for _, f := range findings.Items {
		if f["kind"] == "FORECAST_DRIFT" {
			drift = f
		}
	}
	if drift == nil || drift["action_taken"] != "AUTOMATION_PAUSED" || drift["state"] != "OPEN" {
		t.Fatalf("drift finding %+v", findings.Items)
	}
	outcomes := commercialData[struct {
		Items []map[string]any `json:"items"`
	}](t, x.must(x.owner, "GET", "/revenue/outcomes", nil, 200))
	if len(outcomes.Items) == 0 {
		t.Fatal("outcome list empty")
	}
	// Paused automation applies nothing further while the operator reviews.
	before := x.count(`SELECT count(*) FROM rate_plan_versions WHERE rate_plan_id=$1`, x.rate.ID)
	x.process()
	if x.count(`SELECT count(*) FROM rate_plan_versions WHERE rate_plan_id=$1`, x.rate.ID) != before {
		t.Fatal("paused automation changed rates")
	}
	id, _ := ids.Parse(drift["id"].(string))
	version := int64(drift["version"].(float64))
	resolved := commercialData[map[string]any](t, x.must(x.owner, "POST", "/revenue/monitoring-findings/"+id.String()+"/resolve", businesshttp.RMSFindingResolveCommand{Version: version, Resolution: "Forecast drift reviewed; automation stays paused pending model review"}, 200))
	if resolved["state"] != "RESOLVED" {
		t.Fatalf("resolution %+v", resolved)
	}
}

// Journey 8b: an unknown external channel outcome is never treated as success.
func TestBusinessJourneyRMSUnknownChannelOutcomeNeverSucceeds(t *testing.T) {
	x := newRMSAutomationFixture(t)
	_, client := newCommercialSupplyConsumer(t)
	clock := x.service.Now
	x.service.Now = time.Now
	x.refreshMFA()
	conn := x.connectMarketplace(client)
	// Publish the horizon night that still has stock; real bookings hold the others.
	original := x.dates
	end, _ := time.Parse(time.DateOnly, x.day)
	next := end.AddDate(0, 0, 1).Format(time.DateOnly)
	capacity := 1
	x.must(x.owner, "PUT", "/inventory", businesshttp.CommercialCalendarCommand{PoolID: x.pool.ID, DateFrom: next, DateTo: end.AddDate(0, 0, 2).Format(time.DateOnly), Capacity: &capacity, ExpectedVersions: map[string]int64{next: 0}, Reason: "Open the following physical night for publication"}, 200)
	x.dates = inventory.DateRange{DateFrom: x.day, DateTo: end.AddDate(0, 0, 2).Format(time.DateOnly)}
	x.publishListing(conn) // durably queued, not yet delivered
	x.dates = original
	x.service.Now = clock
	x.refreshMFA()
	org := x.owner.registered.OrganisationID
	p := x.policy("AUTOPILOT_WITH_LIMITS", nil)
	a := x.activate(p, "AUTOPILOT_WITH_LIMITS", rmsTestLimits())
	x.process()
	run := x.runs(a.ID)[0]
	if run.Stage != "APPLIED" {
		t.Fatalf("run %+v", run)
	}
	var state string
	if e := x.db.Pool.QueryRow(t.Context(), `SELECT result->>'state' FROM rms_automation_jobs WHERE recommendation_id=$1 AND kind='RECONCILE_PUBLICATION'`, *run.RecommendationID).Scan(&state); e != nil || state != "UNKNOWN" {
		t.Fatalf("pending delivery must be UNKNOWN, got %q %v", state, e)
	}
	// The provider then times out after a possible commit: the outcome is unknown.
	if _, e := x.db.Pool.Exec(t.Context(), `INSERT INTO channel_sync_jobs(id,organisation_id,property_id,connection_id,kind,canonical_version,payload,payload_hash,idempotency_key,actor_id,state,last_error) VALUES($1,$2,$3,$4,'ARI',1,'{}',$5,$6,$7,'QUARANTINED','provider timeout after commit; outcome unknown')`, ids.New(), org, x.property.ID, conn.ID, strings.Repeat("b", 64), "rms-test-unknown-"+ids.New().String(), x.owner.registered.AccountID); e != nil {
		t.Fatal(e)
	}
	if _, e := x.db.Pool.Exec(t.Context(), `UPDATE rms_automation_jobs SET available_at=$2,version=version+1 WHERE recommendation_id=$1 AND kind='RECONCILE_PUBLICATION'`, *run.RecommendationID, x.service.Now()); e != nil {
		t.Fatal(e)
	}
	x.process()
	var jobState string
	if e := x.db.Pool.QueryRow(t.Context(), `SELECT state,result->>'state' FROM rms_automation_jobs WHERE recommendation_id=$1 AND kind='RECONCILE_PUBLICATION'`, *run.RecommendationID).Scan(&jobState, &state); e != nil || state != "UNKNOWN" || jobState != "QUEUED" {
		t.Fatalf("unknown outcome must keep reconciling: %s/%s %v", jobState, state, e)
	}
	if x.count(`SELECT count(*) FROM rms_monitoring_findings WHERE kind='PUBLICATION_UNKNOWN' AND recommendation_id=$1 AND state='OPEN'`, *run.RecommendationID) != 1 {
		t.Fatal("unknown external outcome was not surfaced")
	}
	if x.count(`SELECT count(*) FROM pricing_decisions WHERE recommendation_id=$1 AND (evidence->>'external_delivery_confirmed')::boolean`, *run.RecommendationID) != 0 {
		t.Fatal("external delivery was claimed")
	}
}
