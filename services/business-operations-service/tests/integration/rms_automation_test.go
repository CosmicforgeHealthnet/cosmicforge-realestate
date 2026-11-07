package integration

import (
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type rmsAutomationFixture struct {
	*commercialHarness
	forecast businesshttp.RMSForecastRun
	day      string
}

// newRMSAutomationFixture reuses the actual canonical booking-history fixture
// and extends disposable verification horizons so clock advances stay inside
// a current authority window. No forecast inputs are seeded directly.
func newRMSAutomationFixture(t *testing.T) *rmsAutomationFixture {
	c, f := rmsOwnedHistoryFixture(t)
	x := &rmsAutomationFixture{commercialHarness: c, forecast: f, day: f.DateFrom}
	clock := c.service.Now()
	for _, statement := range []string{`UPDATE verification_links SET expires_at=$2 WHERE organisation_id=$1`, `UPDATE organisation_capacities SET expires_at=$2 WHERE organisation_id=$1`, `UPDATE inventory_verification_links SET expires_at=$2,version=version+1 WHERE organisation_id=$1`} {
		if _, e := c.db.Pool.Exec(t.Context(), statement, c.owner.registered.OrganisationID, clock.Add(10*24*time.Hour)); e != nil {
			t.Fatal(e)
		}
	}
	x.refreshMFA()
	return x
}
func (x *rmsAutomationFixture) refreshMFA() {
	now := x.service.Now().UTC()
	if _, e := x.db.Pool.Exec(x.t.Context(), `UPDATE business_sessions SET mfa_at=$2,expires_at=$3 WHERE organisation_id=$1`, x.owner.registered.OrganisationID, now, now.Add(time.Hour)); e != nil {
		x.t.Fatal(e)
	}
}
func (x *rmsAutomationFixture) advance(d time.Duration) {
	next := x.service.Now().Add(d)
	x.service.Now = func() time.Time { return next }
	x.refreshMFA()
}
func (x *rmsAutomationFixture) policy(mode string, mutate func(*revenue.PricingPolicy)) businesshttp.RMSPolicy {
	x.t.Helper()
	var current int64
	if e := x.db.Pool.QueryRow(x.t.Context(), `SELECT coalesce(max(version),0) FROM rms_policies WHERE rate_plan_id=$1`, x.rate.ID).Scan(&current); e != nil {
		x.t.Fatal(e)
	}
	now := x.service.Now().UTC()
	d := revenue.PricingPolicy{Mode: mode, Currency: "NGN", FloorMinor: 9000, CeilingMinor: 11000, MaxDailyChangeBPS: 500, MaxCumulativeChangeBPS: 1000, CumulativeDays: 7, ApprovalThresholdBPS: 300, TargetOccupancyBPS: 7500, FeedbackGainBPS: 1000, MinimumHistoryCoverageBPS: 8000, MaxSourceAgeSeconds: 3600, RecommendationTTLSeconds: 7200, EffectiveAt: now.Add(-time.Minute), ExpiresAt: now.Add(7 * 24 * time.Hour)}
	if mutate != nil {
		mutate(&d)
	}
	in := businesshttp.RMSPolicyCommand{PropertyID: x.property.ID, PoolID: x.pool.ID, RatePlanID: x.rate.ID, ExpectedVersion: current, State: "ACTIVE", Definition: d, Reason: "Independent bounded RMS mandate for automation"}
	op := in
	in.ApprovalID = x.approval("COMMERCIAL_RMS_POLICY", "rms:approve", "", nil, "", map[string]any{"command": op})
	return commercialData[businesshttp.RMSPolicy](x.t, x.must(x.owner, "POST", "/revenue/policies", in, 201))
}
func rmsTestLimits() revenue.AutomationLimits {
	return revenue.AutomationLimits{FloorMinor: 9000, CeilingMinor: 11000, MaxDailyChangeBPS: 300, MaxCumulativeChangeBPS: 600, CumulativeDays: 7, ApprovalThresholdBPS: 300, BlackoutDates: []string{}, ComplianceRestrictions: []string{}, RequiredCapacityCode: "MANAGEMENT", MaxSourceAgeSeconds: 1800, EligibleModels: []revenue.ModelRef{{Key: revenue.BaselineForecastModel, Version: 1}}, MinimumCoverageBPS: 8000, MinimumComparableDays: 3, MaxExposureMinor: 100000, HorizonOffsetDays: 0, HorizonDays: 1, HistoryDays: 56, RunIntervalSeconds: 3600, ApplyDelaySeconds: 0, OutcomeDelaySeconds: 0, DriftThresholdBPS: 2000, DriftMinMeasurements: 1, AutoPauseOnDrift: true}
}
func (x *rmsAutomationFixture) activationCommand(p businesshttp.RMSPolicy, mode string, limits revenue.AutomationLimits) businesshttp.RMSAutomationActivateCommand {
	now := x.service.Now().UTC()
	in := businesshttp.RMSAutomationActivateCommand{PolicyID: p.ID, Mode: mode, Limits: limits, EffectiveAt: now, ExpiresAt: now.Add(5 * 24 * time.Hour), Reason: "Activate bounded RMS automation under independent review"}
	op := in
	exposure := limits.MaxExposureMinor
	in.ApprovalID = x.approval("COMMERCIAL_RMS_AUTOMATION_ACTIVATION", "rms:approve", "", &exposure, "NGN", map[string]any{"command": op})
	return in
}
func (x *rmsAutomationFixture) activate(p businesshttp.RMSPolicy, mode string, limits revenue.AutomationLimits) businesshttp.RMSAutomationActivation {
	x.t.Helper()
	return commercialData[businesshttp.RMSAutomationActivation](x.t, x.must(x.owner, "POST", "/revenue/automation-activations", x.activationCommand(p, mode, limits), 201))
}
func (x *rmsAutomationFixture) process() {
	x.t.Helper()
	if e := x.domain.ProcessRMSAutomation(x.t.Context(), 50); e != nil {
		x.t.Fatal(e)
	}
}
func (x *rmsAutomationFixture) runs(activation ids.ID) []businesshttp.RMSAutomationRun {
	x.t.Helper()
	rows, e := x.db.Pool.Query(x.t.Context(), `SELECT to_jsonb(r) FROM rms_automation_runs r WHERE activation_id=$1 ORDER BY created_at,id`, activation)
	if e != nil {
		x.t.Fatal(e)
	}
	defer rows.Close()
	out := []businesshttp.RMSAutomationRun{}
	for rows.Next() {
		var raw []byte
		var v businesshttp.RMSAutomationRun
		if e = rows.Scan(&raw); e != nil {
			x.t.Fatal(e)
		}
		if e = json.Unmarshal(raw, &v); e != nil {
			x.t.Fatal(e)
		}
		out = append(out, v)
	}
	return out
}
func (x *rmsAutomationFixture) rateAmount() int64 {
	x.t.Helper()
	v, e := revenue.VersionForDay(x.t.Context(), x.db.Pool, x.owner.registered.OrganisationID, x.rate.ID, x.day)
	if e != nil {
		x.t.Fatal(e)
	}
	return v.AmountMinor
}
func (x *rmsAutomationFixture) count(query string, args ...any) int {
	x.t.Helper()
	var n int
	if e := x.db.Pool.QueryRow(x.t.Context(), query, args...).Scan(&n); e != nil {
		x.t.Fatal(e)
	}
	return n
}
func (x *rmsAutomationFixture) activation(id ids.ID) businesshttp.RMSAutomationActivation {
	x.t.Helper()
	return commercialData[businesshttp.RMSAutomationActivation](x.t, x.must(x.owner, "GET", "/revenue/automation-activations/"+id.String(), nil, 200))
}
func (x *rmsAutomationFixture) deny(account ids.ID, capability string) {
	x.t.Helper()
	org := x.owner.registered.OrganisationID
	if _, e := x.db.Pool.Exec(x.t.Context(), `INSERT INTO staff_scope_grants(id,organisation_id,membership_id,capability,scope_kind,scope_id,inherit,deny,effective_at) SELECT $1,$2,m.id,$4,'COMPANY',$2,true,true,now()-interval '1 day' FROM staff_memberships m WHERE m.organisation_id=$2 AND m.account_id=$3`, ids.New(), org, account, capability); e != nil {
		x.t.Fatal(e)
	}
}

func TestBusinessRMSAutopilotRequiresExplicitAuthorizedActivation(t *testing.T) {
	x := newRMSAutomationFixture(t)
	manual := x.policy("RECOMMEND_ONLY", nil)
	x.must(x.owner, "POST", "/revenue/automation-activations", x.activationCommand(manual, "AUTOPILOT_WITH_LIMITS", rmsTestLimits()), 409)
	p := x.policy("AUTOPILOT_WITH_LIMITS", nil)
	// A configured automatic mode label alone never executes anything.
	before := x.rateAmount()
	x.process()
	if x.rateAmount() != before || x.count(`SELECT count(*) FROM rms_automation_runs WHERE organisation_id=$1`, x.owner.registered.OrganisationID) != 0 {
		t.Fatal("an automatic mode label executed without an activation")
	}
	unapproved := x.activationCommand(p, "AUTOPILOT_WITH_LIMITS", rmsTestLimits())
	unapproved.ApprovalID = ids.Nil
	x.must(x.owner, "POST", "/revenue/automation-activations", unapproved, 403)
	wider := rmsTestLimits()
	wider.CeilingMinor = 20000
	bad := x.activationCommand(p, "AUTOPILOT_WITH_LIMITS", rmsTestLimits())
	bad.Limits = wider
	x.must(x.owner, "POST", "/revenue/automation-activations", bad, 400)
	// A staff member without the explicit autopilot capability cannot sponsor it.
	clock := x.service.Now
	x.service.Now = time.Now
	staff := x.user()
	x.secure(staff)
	x.join(staff, x.owner, []string{"rms:write", "rates:write", "approvals:create"})
	x.service.Now = clock
	x.refreshMFA()
	x.deny(staff.registered.AccountID, "rms:autopilot")
	staffCommand := x.activationCommand(p, "AUTOPILOT_WITH_LIMITS", rmsTestLimits())
	x.must(staff, "POST", "/revenue/automation-activations", staffCommand, 403)
	a := x.activate(p, "AUTOPILOT_WITH_LIMITS", rmsTestLimits())
	if a.State != "ACTIVE" || a.SponsorAccountID != x.owner.registered.AccountID || a.ApprovalID.IsZero() || a.LimitsDigest == "" {
		t.Fatalf("activation %+v", a)
	}
	x.must(x.owner, "POST", "/revenue/automation-activations", x.activationCommand(p, "AUTOPILOT_WITH_LIMITS", rmsTestLimits()), 409)
	// Limits are immutable once approved.
	if _, e := x.db.Pool.Exec(t.Context(), `UPDATE rms_automation_activations SET limits='{}',version=version+1 WHERE id=$1`, a.ID); e == nil {
		t.Fatal("approved limits were mutated")
	}
	// Kill switch, then resume only with a new independent approval.
	paused := commercialData[businesshttp.RMSAutomationActivation](t, x.must(x.owner, "POST", "/revenue/automation-activations/"+a.ID.String()+"/pause", businesshttp.RMSAutomationStateCommand{Version: a.Version, Reason: "Operator kill switch engaged"}, 200))
	x.process()
	if paused.State != "PAUSED" || x.rateAmount() != before || len(x.runs(a.ID)) != 0 {
		t.Fatal("paused automation executed")
	}
	x.must(x.owner, "POST", "/revenue/automation-activations/"+a.ID.String()+"/resume", businesshttp.RMSAutomationStateCommand{Version: paused.Version, Reason: "Resume without approval must fail"}, 403)
	exposure := a.Limits.MaxExposureMinor
	resume := businesshttp.RMSAutomationStateCommand{Version: paused.Version, Reason: "Resume after independent review"}
	resume.ApprovalID = x.approval("COMMERCIAL_RMS_AUTOMATION_ACTIVATION", "rms:approve", "", &exposure, "NGN", map[string]any{"activation_id": a.ID, "action": "RESUME", "version": paused.Version, "limits_digest": a.LimitsDigest})
	resumed := commercialData[businesshttp.RMSAutomationActivation](t, x.must(x.owner, "POST", "/revenue/automation-activations/"+a.ID.String()+"/resume", resume, 200))
	if resumed.State != "ACTIVE" || x.count(`SELECT count(*) FROM rms_automation_activation_history WHERE activation_id=$1`, a.ID) != 3 {
		t.Fatalf("resume %+v", resumed)
	}
	// Losing sponsor authority pauses the mandate and records the reason.
	x.deny(x.owner.registered.AccountID, "rates:write")
	x.process()
	if got := x.activation(a.ID); got.State != "PAUSED" {
		t.Fatalf("revoked sponsor authority did not pause automation: %+v", got)
	}
	if x.rateAmount() != before || x.count(`SELECT count(*) FROM rms_monitoring_findings WHERE activation_id=$1 AND kind='AUTHORITY_REVOKED' AND action_taken='AUTOMATION_PAUSED'`, a.ID) != 1 {
		t.Fatal("authority revocation finding missing or rate changed")
	}
}

func TestBusinessRMSAutopilotAppliesOnceWithinLimitsAndRollsBack(t *testing.T) {
	x := newRMSAutomationFixture(t)
	p := x.policy("AUTOPILOT_WITH_LIMITS", nil)
	a := x.activate(p, "AUTOPILOT_WITH_LIMITS", rmsTestLimits())
	versionsBefore := x.count(`SELECT count(*) FROM rate_plan_versions WHERE rate_plan_id=$1`, x.rate.ID)
	// Duplicate worker replicas race on the same due activation and jobs.
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- x.domain.ProcessRMSAutomation(t.Context(), 50)
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Logf("replica error (retried next tick): %v", e)
		}
	}
	x.process()
	runs := x.runs(a.ID)
	if len(runs) != 1 || runs[0].Stage != "APPLIED" || runs[0].RecommendationID == nil || runs[0].CreatedByService != "business-operations-service" {
		t.Fatalf("runs %+v", runs)
	}
	if got := x.rateAmount(); got != 9750 {
		t.Fatalf("canonical rate %d", got)
	}
	if n := x.count(`SELECT count(*) FROM rate_plan_versions WHERE rate_plan_id=$1`, x.rate.ID); n != versionsBefore+1 {
		t.Fatalf("duplicate workers applied %d versions", n-versionsBefore)
	}
	rec := commercialData[businesshttp.RMSRecommendation](t, x.must(x.owner, "GET", "/revenue/recommendations/"+runs[0].RecommendationID.String(), nil, 200))
	if rec.State != "APPLIED" || !rec.CreatedBy.IsZero() || rec.CreatedByService != "business-operations-service" || rec.AutomationRunID == nil || rec.Snapshot.Model == nil || rec.Snapshot.Confidence == "" || rec.ApprovalID == nil || *rec.ApprovalID != a.ApprovalID {
		t.Fatalf("automation recommendation lineage %+v", rec)
	}
	// The service never impersonates a human: decisions and rate versions carry the service identity.
	if x.count(`SELECT count(*) FROM pricing_decisions WHERE recommendation_id=$1 AND actor_id IS NOT NULL`, rec.ID) != 0 || x.count(`SELECT count(*) FROM rate_plan_versions WHERE rate_plan_id=$1 AND created_by IS NULL AND created_by_service='business-operations-service'`, x.rate.ID) != 1 {
		t.Fatal("service-applied change borrowed a human identity")
	}
	// Channel deltas are durable: canonical history (the channel worker's source) plus outbox.
	if x.count(`SELECT count(*) FROM commercial_history WHERE entity_id=$1 AND operation='RMS_CANONICAL_RATE_APPLIED'`, x.rate.ID) != 1 || x.count(`SELECT count(*) FROM outbox_events WHERE event_type='rate_plan.changed.v1' AND aggregate_id=$1`, x.rate.ID) < 1 || x.count(`SELECT count(*) FROM outbox_events WHERE event_type='pricing.applied.v1' AND aggregate_id=$1`, rec.ID) != 1 {
		t.Fatal("canonical channel delta was not durably recorded")
	}
	var publication string
	if e := x.db.Pool.QueryRow(t.Context(), `SELECT result->>'state' FROM rms_automation_jobs WHERE recommendation_id=$1 AND kind='RECONCILE_PUBLICATION'`, rec.ID).Scan(&publication); e != nil || publication != "NO_ACTIVE_CHANNEL_CONNECTIONS" {
		t.Fatalf("publication reconciliation %q %v", publication, e)
	}
	var stages int
	if stages = x.count(`SELECT count(*) FROM rms_automation_run_stages WHERE run_id=$1`, runs[0].ID); stages < 5 {
		t.Fatalf("run audit stages %d", stages)
	}
	detail := commercialData[businesshttp.RMSAutomationRunDetail](t, x.must(x.owner, "GET", "/revenue/automation-runs/"+runs[0].ID.String(), nil, 200))
	if len(detail.Stages) != stages || len(detail.Jobs) < 4 {
		t.Fatalf("run detail %+v", detail)
	}
	if _, e := x.db.Pool.Exec(t.Context(), `UPDATE rms_automation_runs SET stage='QUEUED',version=version+1 WHERE id=$1`, runs[0].ID); e == nil {
		t.Fatal("terminal run rewritten")
	}
	// Authorized human rollback creates a new compensating canonical version.
	rolled := rmsDecision(x.commercialHarness, rec, "ROLLBACK", rmsApproval(x.commercialHarness, rec, true))
	current, e := revenue.VersionForDay(t.Context(), x.db.Pool, x.owner.registered.OrganisationID, x.rate.ID, x.day)
	if e != nil || rolled.State != "ROLLED_BACK" || current.AmountMinor != 10000 || current.ID == rec.Snapshot.Segments[0].Previous.ID {
		t.Fatalf("rollback %+v %+v %v", rolled, current, e)
	}
	if n := x.count(`SELECT count(*) FROM rate_plan_versions WHERE rate_plan_id=$1`, x.rate.ID); n != versionsBefore+2 {
		t.Fatalf("rollback history versions %d", n)
	}
}

func TestBusinessRMSRollbackNeverOverwritesIntervening(t *testing.T) {
	x := newRMSAutomationFixture(t)
	p := x.policy("AUTOPILOT_WITH_LIMITS", nil)
	a := x.activate(p, "AUTOPILOT_WITH_LIMITS", rmsTestLimits())
	x.process()
	runs := x.runs(a.ID)
	if len(runs) != 1 || runs[0].Stage != "APPLIED" {
		t.Fatalf("runs %+v", runs)
	}
	rec := commercialData[businesshttp.RMSRecommendation](t, x.must(x.owner, "GET", "/revenue/recommendations/"+runs[0].RecommendationID.String(), nil, 200))
	// An intervening approved canonical change happens after the automated version.
	if _, e := x.db.Pool.Exec(t.Context(), `UPDATE rate_plans SET version=version+1 WHERE id=$1`, x.rate.ID); e != nil {
		t.Fatal(e)
	}
	approval := rmsApproval(x.commercialHarness, rec, true)
	x.must(x.owner, "POST", "/revenue/recommendations/"+rec.ID.String()+"/decision", businesshttp.RMSDecisionCommand{Version: rec.Version, Action: "ROLLBACK", ApprovalID: approval, Reason: "Rollback must not overwrite a newer rate"}, 409)
	if x.rateAmount() != 9750 {
		t.Fatal("rollback overwrote an intervening change")
	}
}

func TestBusinessRMSApprovalAutomationNeedsIndependentHumanApproval(t *testing.T) {
	x := newRMSAutomationFixture(t)
	p := x.policy("APPROVAL_AUTOMATION", nil)
	a := x.activate(p, "APPROVAL_AUTOMATION", rmsTestLimits())
	// Without a governing approval policy the run blocks rather than self-authorizing.
	x.process()
	runs := x.runs(a.ID)
	if len(runs) != 1 || runs[0].Stage != "BLOCKED" || runs[0].BlockedCode != "APPROVAL_POLICY_UNAVAILABLE" || x.rateAmount() != 10000 {
		t.Fatalf("blocked run %+v", runs)
	}
	x.must(x.owner, "POST", "/approval-policies", map[string]any{"action": "COMMERCIAL_RMS_AUTOMATED_RATE_CHANGE", "risk": "HIGH", "scope_kind": "COMPANY", "channel": "", "levels": []map[string]any{{"capability": "rates:approve", "count": 1, "require_mfa": true}}}, 201)
	run := commercialData[businesshttp.RMSAutomationRun](t, x.must(x.owner, "POST", "/revenue/automation-activations/"+a.ID.String()+"/runs", businesshttp.RMSAutomationRunCommand{Reason: "Operator requested an immediate automation run"}, 202))
	x.must(x.owner, "POST", "/revenue/automation-activations/"+a.ID.String()+"/runs", businesshttp.RMSAutomationRunCommand{Reason: "Second concurrent run must be refused"}, 409)
	x.process()
	run = x.runs(a.ID)[1]
	if run.Stage != "APPROVAL_PENDING" || run.ApprovalID == nil || x.rateAmount() != 10000 {
		t.Fatalf("pending run %+v", run)
	}
	var requester ids.ID
	var context []byte
	if e := x.db.Pool.QueryRow(t.Context(), `SELECT requester_id,operation_context FROM approval_requests WHERE id=$1`, *run.ApprovalID).Scan(&requester, &context); e != nil {
		t.Fatal(e)
	}
	var op map[string]any
	_ = json.Unmarshal(context, &op)
	if requester != x.owner.registered.AccountID || op["submitted_by_service"] != "business-operations-service" || op["automation_run_id"] != run.ID.String() {
		t.Fatalf("approval provenance requester=%s op=%v", requester, op)
	}
	// The sponsor cannot approve the automation's request (no self-approval).
	x.must(x.owner, "POST", "/approvals/"+run.ApprovalID.String()+"/decision", map[string]any{"version": 1, "level": 0, "outcome": "APPROVED", "reason": "Sponsor tries to self approve"}, 403)
	x.process()
	if got := x.runs(a.ID)[1]; got.Stage != "APPROVAL_PENDING" || x.rateAmount() != 10000 {
		t.Fatalf("applied before independent approval %+v", got)
	}
	x.advance(2 * time.Minute)
	x.must(x.approver, "POST", "/approvals/"+run.ApprovalID.String()+"/decision", map[string]any{"version": 1, "level": 0, "outcome": "APPROVED", "reason": "Independent rate approver reviewed the digest"}, 200)
	x.process()
	run = x.runs(a.ID)[1]
	if run.Stage != "APPLIED" || x.rateAmount() != 9750 {
		t.Fatalf("approved run %+v rate=%d", run, x.rateAmount())
	}
	var consumed *time.Time
	var consumer *ids.ID
	if e := x.db.Pool.QueryRow(t.Context(), `SELECT consumed_at,consumed_by_account_id FROM approval_requests WHERE id=$1`, *run.ApprovalID).Scan(&consumed, &consumer); e != nil || consumed == nil || consumer != nil {
		t.Fatalf("approval consumption %v %v %v", consumed, consumer, e)
	}
}

func TestBusinessRMSAutomationBlocksBlackoutStaleAndCumulativeLimits(t *testing.T) {
	x := newRMSAutomationFixture(t)
	p := x.policy("AUTOPILOT_WITH_LIMITS", nil)
	blackout := rmsTestLimits()
	blackout.BlackoutDates = []string{x.day}
	a := x.activate(p, "AUTOPILOT_WITH_LIMITS", blackout)
	x.process()
	if runs := x.runs(a.ID); len(runs) != 1 || runs[0].Stage != "BLOCKED" || runs[0].BlockedCode != "BLACKOUT_DATE" || x.rateAmount() != 10000 {
		t.Fatalf("blackout %+v", runs)
	}
	revoke := businesshttp.RMSAutomationStateCommand{Version: x.activation(a.ID).Version, Reason: "Replace the blackout-bound activation"}
	x.must(x.owner, "POST", "/revenue/automation-activations/"+a.ID.String()+"/revoke", revoke, 200)
	// Stale source: scheduled application happens after the approved freshness window.
	stale := rmsTestLimits()
	stale.MaxSourceAgeSeconds = 60
	stale.ApplyDelaySeconds = 120
	a = x.activate(p, "AUTOPILOT_WITH_LIMITS", stale)
	x.process()
	if runs := x.runs(a.ID); runs[0].Stage != "APPROVED" {
		t.Fatalf("scheduled application %+v", runs)
	}
	x.advance(3 * time.Minute)
	x.process()
	runs := x.runs(a.ID)
	if runs[0].Stage != "BLOCKED" || runs[0].BlockedCode != "SOURCE_STALE" || x.rateAmount() != 10000 {
		t.Fatalf("stale %+v", runs)
	}
	if rec := commercialData[businesshttp.RMSRecommendation](t, x.must(x.owner, "GET", "/revenue/recommendations/"+runs[0].RecommendationID.String(), nil, 200)); rec.State != "REJECTED" {
		t.Fatalf("stale proposal not withdrawn %+v", rec)
	}
	revoke = businesshttp.RMSAutomationStateCommand{Version: x.activation(a.ID).Version, Reason: "Replace the stale-window activation"}
	x.must(x.owner, "POST", "/revenue/automation-activations/"+a.ID.String()+"/revoke", revoke, 200)
	// Cumulative movement: a second change inside the window exceeds the budget.
	tight := rmsTestLimits()
	tight.MaxCumulativeChangeBPS = 300
	a = x.activate(p, "AUTOPILOT_WITH_LIMITS", tight)
	x.process()
	if x.rateAmount() != 9750 {
		t.Fatalf("first bounded change %d", x.rateAmount())
	}
	x.must(x.owner, "POST", "/revenue/automation-activations/"+a.ID.String()+"/runs", businesshttp.RMSAutomationRunCommand{Reason: "Second run inside the cumulative window"}, 202)
	x.process()
	runs = x.runs(a.ID)
	last := runs[len(runs)-1]
	var decision struct {
		Decision revenue.AutomationDecision `json:"decision"`
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(last.Evidence, &raw)
	_ = json.Unmarshal(raw["RECOMMENDED"], &decision)
	if last.Stage != "BLOCKED" || !slices.Contains(decision.Decision.Violations, "CUMULATIVE_LIMIT") || x.rateAmount() != 9750 {
		t.Fatalf("cumulative limit %+v %+v", last, decision)
	}
	if x.count(`SELECT count(*) FROM rms_monitoring_findings WHERE activation_id=$1 AND kind='POLICY_EXCEPTION'`, a.ID) != 1 {
		t.Fatal("policy exception not recorded")
	}
}

func TestBusinessRMSHumanAutomationRaceIsFenced(t *testing.T) {
	x := newRMSAutomationFixture(t)
	p := x.policy("AUTOPILOT_WITH_LIMITS", nil)
	limits := rmsTestLimits()
	limits.ApplyDelaySeconds = 60
	a := x.activate(p, "AUTOPILOT_WITH_LIMITS", limits)
	x.process()
	if runs := x.runs(a.ID); runs[0].Stage != "APPROVED" {
		t.Fatalf("automation not awaiting scheduled application %+v", runs)
	}
	// A human prepares an independently approved manual proposal from the same facts.
	human := commercialData[businesshttp.RMSRecommendation](t, x.must(x.owner, "POST", "/revenue/recommendations", businesshttp.RMSRecommendationCommand{PolicyID: p.ID, ForecastID: x.forecast.ID, Reason: "Human operator proposal racing automation"}, 201))
	human = rmsDecision(x.commercialHarness, human, "SUBMIT", ids.Nil)
	human = rmsDecision(x.commercialHarness, human, "APPROVE", rmsApproval(x.commercialHarness, human, false))
	x.advance(2 * time.Minute)
	before := x.count(`SELECT count(*) FROM rate_plan_versions WHERE rate_plan_id=$1`, x.rate.ID)
	var wg sync.WaitGroup
	var humanStatus int
	wg.Add(2)
	go func() {
		defer wg.Done()
		humanStatus, _ = x.call(x.owner, "POST", "/revenue/recommendations/"+human.ID.String()+"/decision", businesshttp.RMSDecisionCommand{Version: human.Version, Action: "APPLY", Reason: "Concurrent human application"}, nil)
	}()
	go func() {
		defer wg.Done()
		_ = x.domain.ProcessRMSAutomation(t.Context(), 50)
	}()
	wg.Wait()
	x.process()
	after := x.count(`SELECT count(*) FROM rate_plan_versions WHERE rate_plan_id=$1`, x.rate.ID)
	run := x.runs(a.ID)[0]
	humanApplied := humanStatus == 200
	if after != before+1 || humanApplied == (run.Stage == "APPLIED") {
		t.Fatalf("race not fenced: versions %d->%d human=%d run=%s/%s", before, after, humanStatus, run.Stage, run.BlockedCode)
	}
	if !humanApplied && humanStatus != 409 || humanApplied && run.Stage != "BLOCKED" {
		t.Fatalf("loser outcome human=%d run=%+v", humanStatus, run)
	}
}

func TestBusinessRMSWorkerRestartRecoversExpiredLease(t *testing.T) {
	x := newRMSAutomationFixture(t)
	p := x.policy("AUTOPILOT_WITH_LIMITS", nil)
	limits := rmsTestLimits()
	a := x.activate(p, "AUTOPILOT_WITH_LIMITS", limits)
	// Consume the schedule so only the manual run's job remains to recover.
	if _, e := x.db.Pool.Exec(t.Context(), `UPDATE rms_automation_activations SET next_run_at=$2,version=version+1 WHERE id=$1`, a.ID, x.service.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	run := commercialData[businesshttp.RMSAutomationRun](t, x.must(x.owner, "POST", "/revenue/automation-activations/"+a.ID.String()+"/runs", businesshttp.RMSAutomationRunCommand{Reason: "Run interrupted by a crashed replica"}, 202))
	// A replica claimed the job and crashed before finishing; its lease expired.
	if _, e := x.db.Pool.Exec(t.Context(), `UPDATE rms_automation_jobs SET state='RUNNING',lease_owner='crashed-replica',lease_generation=1,lease_expires_at=$2,attempts=1,version=version+1 WHERE run_id=$1`, run.ID, x.service.Now().Add(-time.Minute)); e != nil {
		t.Fatal(e)
	}
	x.process()
	var generation int64
	if e := x.db.Pool.QueryRow(t.Context(), `SELECT lease_generation FROM rms_automation_jobs WHERE run_id=$1 AND kind='CAPTURE_DEMAND'`, run.ID).Scan(&generation); e != nil || generation < 2 {
		t.Fatalf("lease not taken over: %d %v", generation, e)
	}
	if got := x.runs(a.ID)[0]; got.Stage != "APPLIED" || x.rateAmount() != 9750 {
		t.Fatalf("recovered run %+v", got)
	}
}
