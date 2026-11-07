package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"github.com/jackc/pgx/v5"
)

const (
	rmsJobLease           = 2 * time.Minute
	rmsMaxJobAttempts     = 8
	rmsAutomatedRateRule  = "COMMERCIAL_RMS_AUTOMATED_RATE_CHANGE"
	rmsActivationApproval = "COMMERCIAL_RMS_AUTOMATION_ACTIVATION"
)

type rmsJob struct {
	ID               ids.ID  `json:"id"`
	OrganisationID   ids.ID  `json:"organisation_id"`
	PropertyID       ids.ID  `json:"property_id"`
	ActivationID     *ids.ID `json:"activation_id"`
	RunID            *ids.ID `json:"run_id"`
	RecommendationID *ids.ID `json:"recommendation_id"`
	Kind             string  `json:"kind"`
	State            string  `json:"state"`
	Attempts         int     `json:"attempts"`
	LeaseOwner       *string `json:"lease_owner"`
	LeaseGeneration  int64   `json:"lease_generation"`
	Version          int64   `json:"version"`
}
type rmsJobOutcome struct {
	state  string
	at     time.Time
	result map[string]any
}

func rmsDone(result map[string]any) rmsJobOutcome {
	return rmsJobOutcome{state: "SUCCEEDED", result: result}
}
func rmsLater(at time.Time, result map[string]any) rmsJobOutcome {
	return rmsJobOutcome{state: "QUEUED", at: at, result: result}
}

// ProcessRMSAutomation runs on the established worker scheduler. It schedules
// due activations, then claims leased jobs one at a time. Every job reloads its
// state under the organisation lock and is fenced by its lease generation, so
// duplicate replicas, restarts and delayed workers cannot apply twice.
func (h *Handler) ProcessRMSAutomation(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	if e := h.rmsScheduleActivations(ctx, limit); e != nil {
		return e
	}
	owner := "rms-automation:" + ids.New().String()
	var failures []error
	for range limit {
		job, ok, e := h.rmsClaimJob(ctx, owner)
		if e != nil {
			return e
		}
		if !ok {
			break
		}
		if e = h.rmsProcessJob(ctx, job, owner); e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}

func (h *Handler) rmsAutomationTenant(ctx context.Context, q database.Querier, org ids.ID) (tenant, error) {
	t := tenant{Organisation: org}
	if e := q.QueryRow(ctx, `SELECT state FROM organisations WHERE id=$1`, org).Scan(&t.State); e != nil {
		return t, e
	}
	if t.State == "ACTIVE" {
		var current bool
		if e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM verification_links WHERE organisation_id=$1 AND purpose='KYB' AND provider_status='VERIFIED' AND expires_at>$2)`, org, h.Auth.Service.Now().UTC()).Scan(&current); e != nil {
			return t, e
		}
		if !current {
			t.State = "RESTRICTED"
		}
	}
	return t, nil
}

func (h *Handler) rmsScheduleActivations(ctx context.Context, limit int) error {
	now := h.Auth.Service.Now().UTC()
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT organisation_id,id FROM rms_automation_activations WHERE state='ACTIVE' AND (next_run_at<=$1 OR expires_at<=$1) ORDER BY next_run_at,id LIMIT $2`, now, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, id ids.ID }
	refs := []ref{}
	for rows.Next() {
		var v ref
		if e = rows.Scan(&v.org, &v.id); e != nil {
			break
		}
		refs = append(refs, v)
	}
	re := rows.Err()
	rows.Close()
	if e = errors.Join(e, re); e != nil {
		return e
	}
	for _, v := range refs {
		e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, v.org); e != nil {
				return e
			}
			if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
				return e
			}
			a, e := loadRMSActivation(ctx, tx, v.org, v.id, true)
			if e != nil || a.State != "ACTIVE" {
				return e
			}
			t, e := h.rmsAutomationTenant(ctx, tx, v.org)
			if e != nil {
				return e
			}
			now := h.Auth.Service.Now().UTC()
			if !now.Before(a.ExpiresAt) {
				_, e = h.rmsActivationTransition(ctx, tx, t, a, "EXPIRED", "Activation authorization window elapsed", nil)
				return e
			}
			if now.Before(a.EffectiveAt) || now.Before(a.NextRunAt) {
				return nil
			}
			bucket := a.NextRunAt.UTC().Format(time.RFC3339)
			if _, e = tx.Exec(ctx, `UPDATE rms_automation_activations SET next_run_at=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, v.org, a.ID, now.Add(time.Duration(a.Limits.RunIntervalSeconds)*time.Second)); e != nil {
				return e
			}
			if _, e = h.rmsStartRun(ctx, tx, t, a, "SCHEDULED"); e != nil && commercialErrorCode(e) != "RMS_AUTOMATION_RUN_OPEN" {
				return e
			}
			for _, kind := range []string{"CHECK_FRESHNESS", "MONITOR_LIMITS", "DETECT_DRIFT"} {
				if e = h.rmsEnqueue(ctx, tx, v.org, a.PropertyID, &a.ID, nil, nil, kind, "activation:"+a.ID.String()+":"+kind+":"+bucket, now); e != nil {
					return e
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	return nil
}

func (h *Handler) rmsClaimJob(ctx context.Context, owner string) (rmsJob, bool, error) {
	var job rmsJob
	found := false
	e := database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		now := h.Auth.Service.Now().UTC()
		var id, org ids.ID
		e := tx.QueryRow(ctx, `SELECT id,organisation_id FROM rms_automation_jobs WHERE (state IN ('QUEUED','RETRY') AND available_at<=$1) OR (state='RUNNING' AND lease_expires_at<=$1) ORDER BY available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, now).Scan(&id, &org)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		var raw []byte
		if e = tx.QueryRow(ctx, `UPDATE rms_automation_jobs SET state='RUNNING',lease_owner=$3,lease_generation=lease_generation+1,lease_expires_at=$4,attempts=attempts+1,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 RETURNING to_jsonb(rms_automation_jobs)`, org, id, owner, now.Add(rmsJobLease)).Scan(&raw); e != nil {
			return e
		}
		found = true
		return json.Unmarshal(raw, &job)
	})
	return job, found, e
}

func rmsDomainError(e error) bool {
	var app *apperrors.Error
	return errors.As(e, &app) && app.Status >= 400 && app.Status < 500
}

func (h *Handler) rmsProcessJob(ctx context.Context, claimed rmsJob, owner string) error {
	return database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, claimed.OrganisationID); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
			return e
		}
		var raw []byte
		if e := tx.QueryRow(ctx, `SELECT to_jsonb(j) FROM rms_automation_jobs j WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, claimed.OrganisationID, claimed.ID).Scan(&raw); e != nil {
			return e
		}
		var job rmsJob
		if e := json.Unmarshal(raw, &job); e != nil {
			return e
		}
		// Lease fencing: a delayed replica whose lease was taken over stops here.
		if job.State != "RUNNING" || job.LeaseOwner == nil || *job.LeaseOwner != owner || job.LeaseGeneration != claimed.LeaseGeneration {
			return nil
		}
		t, e := h.rmsAutomationTenant(ctx, tx, job.OrganisationID)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `SAVEPOINT rms_job`); e != nil {
			return e
		}
		out, jobErr := h.rmsDispatch(ctx, tx, t, job)
		if jobErr != nil {
			if _, e = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT rms_job`); e != nil {
				return e
			}
			if rmsDomainError(jobErr) {
				if e = h.rmsBlockRun(ctx, tx, t, job, commercialErrorCode(jobErr), jobErr.Error()); e != nil {
					return e
				}
				out = rmsJobOutcome{state: "BLOCKED", result: map[string]any{"code": commercialErrorCode(jobErr), "message": jobErr.Error()}}
			} else {
				out = rmsJobOutcome{state: "RETRY", at: h.Auth.Service.Now().UTC().Add(time.Duration(job.Attempts*job.Attempts) * 15 * time.Second), result: map[string]any{"error": "TRANSIENT_FAILURE"}}
				if job.Attempts >= rmsMaxJobAttempts {
					out.state = "QUARANTINED"
					if e = h.rmsFailRun(ctx, tx, t, job); e != nil {
						return e
					}
				}
			}
		}
		if out.result == nil {
			out.result = map[string]any{}
		}
		available := out.at
		if available.IsZero() {
			available = h.Auth.Service.Now().UTC()
		}
		var lastError any
		if jobErr != nil {
			lastError = jobErr.Error()
		}
		_, e = tx.Exec(ctx, `UPDATE rms_automation_jobs SET state=$3,available_at=$4,result=$5,last_error=$6,lease_owner=NULL,lease_expires_at=NULL,attempts=CASE WHEN $3='QUEUED' THEN 0 ELSE attempts END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, job.OrganisationID, job.ID, out.state, available, marshalCommercial(out.result), lastError)
		return e
	})
}

func (h *Handler) rmsBlockRun(ctx context.Context, tx pgx.Tx, t tenant, job rmsJob, code, message string) error {
	if job.RunID == nil {
		return nil
	}
	run, e := loadRMSRun(ctx, tx, t.Organisation, *job.RunID, true)
	if e != nil {
		return e
	}
	if slices.Contains([]string{"APPLIED", "NO_CHANGE", "BLOCKED", "REJECTED", "EXPIRED", "FAILED"}, run.Stage) {
		return nil
	}
	if run.RecommendationID != nil {
		if e = h.rmsServiceReject(ctx, tx, t, *run.RecommendationID, code); e != nil {
			return e
		}
	}
	return h.rmsRunStage(ctx, tx, t, run, "BLOCKED", code, map[string]any{"job_kind": job.Kind, "message": message, "rates_changed": false}, nil)
}
func (h *Handler) rmsFailRun(ctx context.Context, tx pgx.Tx, t tenant, job rmsJob) error {
	if job.RunID == nil {
		return nil
	}
	run, e := loadRMSRun(ctx, tx, t.Organisation, *job.RunID, true)
	if e != nil || slices.Contains([]string{"APPLIED", "NO_CHANGE", "BLOCKED", "REJECTED", "EXPIRED", "FAILED"}, run.Stage) {
		return e
	}
	return h.rmsRunStage(ctx, tx, t, run, "FAILED", "JOB_QUARANTINED", map[string]any{"job_kind": job.Kind, "attempts": job.Attempts}, nil)
}

// rmsServiceReject withdraws an unapplied automation proposal.
func (h *Handler) rmsServiceReject(ctx context.Context, tx pgx.Tx, t tenant, recommendation ids.ID, code string) error {
	v, e := loadRMSRecommendation(ctx, tx, t.Organisation, recommendation)
	if e != nil {
		return e
	}
	if v.State != "DRAFT" && v.State != "PENDING_APPROVAL" && v.State != "APPROVED" {
		return nil
	}
	return h.rmsRecordDecision(ctx, tx, t, v, "REJECTED", "REJECT", v.ApprovalID, v.AppliedVersions, "Automation withdrew the proposal: "+code, map[string]any{"code": code, "actor_service": "business-operations-service"})
}

func (h *Handler) rmsDispatch(ctx context.Context, tx pgx.Tx, t tenant, job rmsJob) (rmsJobOutcome, error) {
	switch job.Kind {
	case "CAPTURE_DEMAND", "GENERATE_FORECAST", "GENERATE_RECOMMENDATION", "SUBMIT_APPROVAL", "MONITOR_APPROVAL", "APPLY_RATE":
		if job.RunID == nil || job.ActivationID == nil {
			return rmsJobOutcome{}, apperrors.Conflict("RMS_JOB_SCOPE_INVALID", "Run job lacks its run or activation.")
		}
		run, e := loadRMSRun(ctx, tx, t.Organisation, *job.RunID, true)
		if e != nil {
			return rmsJobOutcome{}, e
		}
		a, e := loadRMSActivation(ctx, tx, t.Organisation, *job.ActivationID, true)
		if e != nil {
			return rmsJobOutcome{}, e
		}
		return h.rmsRunJob(ctx, tx, t, job, a, run)
	case "MEASURE_OUTCOME":
		return h.rmsMeasureOutcome(ctx, tx, t, job)
	case "RECONCILE_PUBLICATION":
		return h.rmsReconcilePublication(ctx, tx, t, job)
	case "CHECK_FRESHNESS", "MONITOR_LIMITS", "DETECT_DRIFT":
		if job.ActivationID == nil {
			return rmsJobOutcome{}, apperrors.Conflict("RMS_JOB_SCOPE_INVALID", "Monitoring job lacks its activation.")
		}
		a, e := loadRMSActivation(ctx, tx, t.Organisation, *job.ActivationID, true)
		if e != nil {
			return rmsJobOutcome{}, e
		}
		switch job.Kind {
		case "CHECK_FRESHNESS":
			return h.rmsCheckFreshness(ctx, tx, t, a)
		case "MONITOR_LIMITS":
			return h.rmsMonitorLimits(ctx, tx, t, a)
		default:
			return h.rmsDetectDrift(ctx, tx, t, a)
		}
	}
	return rmsJobOutcome{}, apperrors.Conflict("RMS_JOB_KIND_UNSUPPORTED", "Unsupported RMS job.")
}

// rmsAuthority rechecks every live authority input for unattended execution.
func (h *Handler) rmsAuthority(ctx context.Context, tx pgx.Tx, t tenant, a RMSAutomationActivation) (RMSPolicy, error) {
	now := h.Auth.Service.Now().UTC()
	if a.State != "ACTIVE" {
		return RMSPolicy{}, apperrors.Conflict("AUTOMATION_"+a.State, "The activation is not active.")
	}
	if now.Before(a.EffectiveAt) || !now.Before(a.ExpiresAt) {
		return RMSPolicy{}, apperrors.Conflict("AUTOMATION_EXPIRED", "The activation authorization window is not current.")
	}
	caps := []string{"rms:write", "rates:write"}
	if a.Mode == "AUTOPILOT_WITH_LIMITS" {
		caps = append(caps, "rms:autopilot")
	}
	for _, capability := range caps {
		ok, e := h.currentRecordedPMSAuthority(ctx, tx, t.Organisation, a.PropertyID, a.SponsorMembershipID, a.SponsorRoleID, permissions.Permission(capability))
		if e != nil {
			return RMSPolicy{}, e
		}
		if !ok {
			return RMSPolicy{}, apperrors.Conflict("SPONSOR_AUTHORITY_REVOKED", "The sponsoring member no longer holds "+capability+" for this property.")
		}
	}
	p, e := loadRMSPolicy(ctx, tx, t.Organisation, a.PolicyID)
	if e != nil {
		return p, e
	}
	if e = h.rmsCurrentPolicy(ctx, tx, t, p); e != nil {
		return p, apperrors.Conflict("POLICY_NOT_CURRENT", "The bound RMS policy version is no longer the current active policy.")
	}
	if p.Definition.Mode != a.Mode || a.Limits.ValidateAgainst(p.Definition, a.Mode) != nil {
		return p, apperrors.Conflict("LIMITS_EXCEED_POLICY", "The activation limits no longer fit the bound policy.")
	}
	if e = h.commercialGate(ctx, tx, t, a.PropertyID); e != nil {
		return p, apperrors.Conflict("COMMERCIAL_AUTHORITY_UNAVAILABLE", "Verified business, property or operating-capacity authority is unavailable.")
	}
	property, e := portfolio.Get(ctx, tx, t.Organisation, a.PropertyID)
	if e != nil {
		return p, e
	}
	if property.CapacityCode != a.Limits.RequiredCapacityCode {
		return p, apperrors.Conflict("CAPACITY_MISMATCH", "The required verified operating capacity changed.")
	}
	return p, nil
}

func rmsAutomationBounds(p revenue.PricingPolicy, l revenue.AutomationLimits) revenue.PricingPolicy {
	b := p
	b.FloorMinor = max(p.FloorMinor, l.FloorMinor)
	b.CeilingMinor = min(p.CeilingMinor, l.CeilingMinor)
	b.MaxDailyChangeBPS = min(p.MaxDailyChangeBPS, l.MaxDailyChangeBPS)
	b.MaxCumulativeChangeBPS = min(p.MaxCumulativeChangeBPS, l.MaxCumulativeChangeBPS)
	b.CumulativeDays = min(p.CumulativeDays, l.CumulativeDays)
	b.ApprovalThresholdBPS = min(p.ApprovalThresholdBPS, b.MaxDailyChangeBPS)
	b.MaxSourceAgeSeconds = min(p.MaxSourceAgeSeconds, l.MaxSourceAgeSeconds)
	b.BlackoutDates = append([]string{}, p.BlackoutDates...)
	for _, d := range l.BlackoutDates {
		if !slices.Contains(b.BlackoutDates, d) {
			b.BlackoutDates = append(b.BlackoutDates, d)
		}
	}
	return b
}

func (h *Handler) rmsEvaluate(ctx context.Context, q database.Querier, t tenant, a RMSAutomationActivation, p RMSPolicy, v RMSRecommendation, f RMSForecastRun) (revenue.AutomationDecision, error) {
	property, e := portfolio.Get(ctx, q, t.Organisation, v.PropertyID)
	if e != nil {
		return revenue.AutomationDecision{}, e
	}
	loc, e := time.LoadLocation(property.Timezone)
	if e != nil {
		return revenue.AutomationDecision{}, e
	}
	forecast := map[string]revenue.ForecastDay{}
	for _, d := range f.Result.Days {
		forecast[d.Day] = d
	}
	days := []revenue.AutomationDay{}
	for _, s := range v.Snapshot.Segments {
		daily, cumulative, e := h.rmsMovementRemaining(ctx, q, t, v.RatePlanID, p.Definition.Currency, s.DateFrom, a.Limits.MaxDailyChangeBPS, a.Limits.MaxCumulativeChangeBPS, a.Limits.CumulativeDays)
		if e != nil {
			return revenue.AutomationDecision{}, e
		}
		arrival, e := time.ParseInLocation(time.DateOnly, s.DateFrom, loc)
		if e != nil {
			return revenue.AutomationDecision{}, e
		}
		fd := forecast[s.DateFrom]
		days = append(days, revenue.AutomationDay{Day: s.DateFrom, PreviousMinor: s.Previous.Definition.AmountMinor, ProposedMinor: s.Proposed.AmountMinor, PhysicalCapacity: fd.PhysicalCapacity, ComparableDays: fd.ComparableDays, ArrivalAt: arrival.UTC(), DailyBudgetMinor: daily, CumulativeBudgetMinor: cumulative})
	}
	return revenue.EvaluateAutomation(a.Limits, revenue.AutomationInput{Mode: a.Mode, Currency: p.Definition.Currency, Model: revenue.ModelRef{Key: f.Result.Model, Version: f.Result.AlgorithmVersion}, CoverageBPS: f.Result.HistoryCoverageBPS, SourceAsOf: f.SourceAsOf, Now: h.Auth.Service.Now().UTC(), Days: days}), nil
}

func rmsAutomationApprovalOperation(a RMSAutomationActivation, run RMSAutomationRun, v RMSRecommendation) map[string]any {
	op := rmsApprovalOperation(v, false)
	op["automation_run_id"] = run.ID
	op["activation_id"] = a.ID
	op["limits_digest"] = a.LimitsDigest
	op["submitted_by_service"] = "business-operations-service"
	return op
}

func (h *Handler) rmsRunJob(ctx context.Context, tx pgx.Tx, t tenant, job rmsJob, a RMSAutomationActivation, run RMSAutomationRun) (rmsJobOutcome, error) {
	now := h.Auth.Service.Now().UTC()
	expected := map[string]string{"CAPTURE_DEMAND": "QUEUED", "GENERATE_FORECAST": "SOURCE_CAPTURED", "GENERATE_RECOMMENDATION": "FORECAST_READY", "SUBMIT_APPROVAL": "RECOMMENDED", "MONITOR_APPROVAL": "APPROVAL_PENDING", "APPLY_RATE": "APPROVED"}[job.Kind]
	if run.Stage != expected {
		// Replays after a committed step are no-ops.
		return rmsDone(map[string]any{"skipped": true, "stage": run.Stage}), nil
	}
	p, e := h.rmsAuthority(ctx, tx, t, a)
	if e != nil {
		return rmsJobOutcome{}, e
	}
	switch job.Kind {
	case "CAPTURE_DEMAND":
		pool, e := inventory.GetPool(ctx, tx, t.Organisation, a.PoolID)
		if e != nil {
			return rmsJobOutcome{}, e
		}
		if e = inventory.CheckStockAuthority(ctx, tx, pool); e != nil {
			return rmsJobOutcome{}, e
		}
		property, e := portfolio.Get(ctx, tx, t.Organisation, a.PropertyID)
		if e != nil {
			return rmsJobOutcome{}, e
		}
		loc, e := time.LoadLocation(property.Timezone)
		if e != nil {
			return rmsJobOutcome{}, e
		}
		today, _ := time.Parse(time.DateOnly, now.In(loc).Format(time.DateOnly))
		from := today.AddDate(0, 0, a.Limits.HorizonOffsetDays)
		to := from.AddDate(0, 0, a.Limits.HorizonDays)
		snapshot, e := h.createRMSDemandSnapshot(ctx, tx, t, pool, RMSDemandCommand{PropertyID: a.PropertyID, PoolID: a.PoolID, DateFrom: from.Format(time.DateOnly), DateTo: to.Format(time.DateOnly), HistoryDays: a.Limits.HistoryDays})
		if e != nil {
			return rmsJobOutcome{}, e
		}
		if e = h.rmsRunStage(ctx, tx, t, run, "SOURCE_CAPTURED", "", map[string]any{"snapshot_id": snapshot.ID, "source_digest": snapshot.SourceDigest, "as_of": snapshot.AsOf, "date_from": snapshot.DateFrom, "date_to": snapshot.DateTo}, map[string]any{"snapshot_id": snapshot.ID}); e != nil {
			return rmsJobOutcome{}, e
		}
		return rmsDone(map[string]any{"snapshot_id": snapshot.ID}), h.rmsEnqueue(ctx, tx, t.Organisation, a.PropertyID, &a.ID, &run.ID, nil, "GENERATE_FORECAST", "run:"+run.ID.String()+":forecast", now)
	case "GENERATE_FORECAST":
		source, e := loadRMSDemandSnapshot(ctx, tx, t.Organisation, *run.SnapshotID)
		if e != nil {
			return rmsJobOutcome{}, e
		}
		f, e := h.createRMSForecastRun(ctx, tx, t, source, revenue.ForecastConfiguration{HistoryDays: a.Limits.HistoryDays, MinimumComparableDays: a.Limits.MinimumComparableDays, MinimumHistoryCoverageBPS: a.Limits.MinimumCoverageBPS}, nil)
		if e != nil {
			return rmsJobOutcome{}, e
		}
		if f.Status != "BASELINE_READY" {
			return rmsDone(map[string]any{"forecast_id": f.ID, "status": f.Status}), h.rmsRunStage(ctx, tx, t, run, "BLOCKED", "INSUFFICIENT_DATA", map[string]any{"forecast_id": f.ID, "history_coverage_bps": f.Result.HistoryCoverageBPS, "rates_changed": false}, map[string]any{"forecast_id": f.ID})
		}
		if e = h.rmsRunStage(ctx, tx, t, run, "FORECAST_READY", "", map[string]any{"forecast_id": f.ID, "model": f.Result.Model, "model_version": f.Result.AlgorithmVersion, "history_coverage_bps": f.Result.HistoryCoverageBPS}, map[string]any{"forecast_id": f.ID}); e != nil {
			return rmsJobOutcome{}, e
		}
		return rmsDone(map[string]any{"forecast_id": f.ID}), h.rmsEnqueue(ctx, tx, t.Organisation, a.PropertyID, &a.ID, &run.ID, nil, "GENERATE_RECOMMENDATION", "run:"+run.ID.String()+":recommend", now)
	case "GENERATE_RECOMMENDATION":
		f, e := loadRMSForecast(ctx, tx, t.Organisation, *run.ForecastID)
		if e != nil {
			return rmsJobOutcome{}, e
		}
		bounds := rmsAutomationBounds(p.Definition, a.Limits)
		for _, d := range f.Result.Days {
			if slices.Contains(bounds.BlackoutDates, d.Day) {
				return rmsDone(map[string]any{"code": "BLACKOUT_DATE", "day": d.Day}), h.rmsRunStage(ctx, tx, t, run, "BLOCKED", "BLACKOUT_DATE", map[string]any{"day": d.Day, "rates_changed": false}, nil)
			}
		}
		v, e := h.createRMSRecommendation(ctx, tx, t, p, bounds, f.ID, nil, "Automation run "+run.ID.String()+" under activation "+a.ID.String(), &run.ID)
		if e != nil {
			return rmsJobOutcome{}, e
		}
		decision, e := h.rmsEvaluate(ctx, tx, t, a, p, v, f)
		if e != nil {
			return rmsJobOutcome{}, e
		}
		if e = h.rmsRunStage(ctx, tx, t, run, "RECOMMENDED", decision.Code, map[string]any{"recommendation_id": v.ID, "change_digest": v.ChangeDigest, "decision": decision}, map[string]any{"recommendation_id": v.ID, "change_digest": v.ChangeDigest}); e != nil {
			return rmsJobOutcome{}, e
		}
		run, e = loadRMSRun(ctx, tx, t.Organisation, run.ID, true)
		if e != nil {
			return rmsJobOutcome{}, e
		}
		result := map[string]any{"recommendation_id": v.ID, "decision": decision}
		evidence := map[string]any{"decision": decision, "activation_id": a.ID, "automation_run_id": run.ID, "actor_service": "business-operations-service"}
		switch decision.Outcome {
		case "NO_CHANGE":
			if e = h.rmsRecordDecision(ctx, tx, t, v, "REJECTED", "REJECT", nil, v.AppliedVersions, "Automation: no canonical change is warranted", evidence); e != nil {
				return rmsJobOutcome{}, e
			}
			return rmsDone(result), h.rmsRunStage(ctx, tx, t, run, "NO_CHANGE", "NO_CHANGE", evidence, nil)
		case "BLOCKED":
			if e = h.rmsRecordDecision(ctx, tx, t, v, "REJECTED", "REJECT", nil, v.AppliedVersions, "Automation limits blocked execution: "+decision.Code, evidence); e != nil {
				return rmsJobOutcome{}, e
			}
			if _, e = h.rmsFinding(ctx, tx, t, a.PropertyID, &a.ID, &v.ID, "POLICY_EXCEPTION", "WARNING", "NONE", "run:"+run.ID.String()+":policy-exception", map[string]any{"violations": decision.Violations, "run_id": run.ID}); e != nil {
				return rmsJobOutcome{}, e
			}
			return rmsDone(result), h.rmsRunStage(ctx, tx, t, run, "BLOCKED", decision.Code, evidence, nil)
		}
		if e = h.rmsRecordDecision(ctx, tx, t, v, "PENDING_APPROVAL", "SUBMIT", nil, v.AppliedVersions, "Automation submitted the exact proposal", evidence); e != nil {
			return rmsJobOutcome{}, e
		}
		if decision.Outcome == "AUTO_APPLY" {
			v, e = loadRMSRecommendation(ctx, tx, t.Organisation, v.ID)
			if e != nil {
				return rmsJobOutcome{}, e
			}
			// The standing activation approval is the authority; it was granted by
			// an independent human with MFA and binds these exact limits.
			approval := a.ApprovalID
			evidence["authority"] = "AUTOPILOT_ACTIVATION_WITHIN_LIMITS"
			if e = h.rmsRecordDecision(ctx, tx, t, v, "APPROVED", "APPROVE", &approval, v.AppliedVersions, "Autopilot within independently approved limits", evidence); e != nil {
				return rmsJobOutcome{}, e
			}
			if e = h.rmsRunStage(ctx, tx, t, run, "APPROVED", "WITHIN_AUTOPILOT_LIMITS", evidence, map[string]any{"approval_id": approval}); e != nil {
				return rmsJobOutcome{}, e
			}
			return rmsDone(result), h.rmsEnqueue(ctx, tx, t.Organisation, a.PropertyID, &a.ID, &run.ID, &v.ID, "APPLY_RATE", "run:"+run.ID.String()+":apply", now.Add(time.Duration(a.Limits.ApplyDelaySeconds)*time.Second))
		}
		return rmsDone(result), h.rmsEnqueue(ctx, tx, t.Organisation, a.PropertyID, &a.ID, &run.ID, &v.ID, "SUBMIT_APPROVAL", "run:"+run.ID.String()+":submit", now)
	case "SUBMIT_APPROVAL":
		v, e := loadRMSRecommendation(ctx, tx, t.Organisation, *run.RecommendationID)
		if e != nil {
			return rmsJobOutcome{}, e
		}
		if v.State != "PENDING_APPROVAL" {
			return rmsJobOutcome{}, apperrors.Conflict("RMS_RECOMMENDATION_STATE", "The proposal is no longer awaiting approval.")
		}
		approvalID, e := h.rmsSubmitApproval(ctx, tx, t, a, run, v, p)
		if e != nil {
			return rmsJobOutcome{}, e
		}
		if e = h.rmsRunStage(ctx, tx, t, run, "APPROVAL_PENDING", "INDEPENDENT_APPROVAL_REQUIRED", map[string]any{"approval_id": approvalID, "requester_account_id": a.SponsorAccountID, "submitted_by_service": "business-operations-service"}, map[string]any{"approval_id": approvalID}); e != nil {
			return rmsJobOutcome{}, e
		}
		return rmsDone(map[string]any{"approval_id": approvalID}), h.rmsEnqueue(ctx, tx, t.Organisation, a.PropertyID, &a.ID, &run.ID, &v.ID, "MONITOR_APPROVAL", "run:"+run.ID.String()+":monitor", now)
	case "MONITOR_APPROVAL":
		v, e := loadRMSRecommendation(ctx, tx, t.Organisation, *run.RecommendationID)
		if e != nil {
			return rmsJobOutcome{}, e
		}
		if v.State == "EXPIRED" || v.State == "PENDING_APPROVAL" && !now.Before(v.ExpiresAt) {
			if v.State != "EXPIRED" {
				if e = h.rmsRecordDecision(ctx, tx, t, v, "EXPIRED", "EXPIRE", v.ApprovalID, v.AppliedVersions, "Approval was not granted before the proposal expired", map[string]any{"actor_service": "business-operations-service"}); e != nil {
					return rmsJobOutcome{}, e
				}
			}
			return rmsDone(map[string]any{"state": "EXPIRED"}), h.rmsRunStage(ctx, tx, t, run, "EXPIRED", "RECOMMENDATION_EXPIRED", map[string]any{"recommendation_id": v.ID}, nil)
		}
		var state string
		var requester ids.ID
		if e = tx.QueryRow(ctx, `SELECT state,requester_id FROM approval_requests WHERE organisation_id=$1 AND id=$2`, t.Organisation, *run.ApprovalID).Scan(&state, &requester); e != nil {
			return rmsJobOutcome{}, e
		}
		switch state {
		case "PENDING", "DRAFT":
			return rmsLater(now.Add(time.Minute), map[string]any{"approval_state": state}), nil
		case "APPROVED":
			var self bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM approval_decisions WHERE organisation_id=$1 AND request_id=$2 AND (actor_id=$3 OR $3=ANY(authority_account_ids)))`, t.Organisation, *run.ApprovalID, requester).Scan(&self); e != nil {
				return rmsJobOutcome{}, e
			}
			if self || requester != a.SponsorAccountID {
				return rmsJobOutcome{}, apperrors.Conflict("APPROVAL_NOT_INDEPENDENT", "The approval was not independent of the automation sponsor.")
			}
			approval := *run.ApprovalID
			evidence := map[string]any{"approval_id": approval, "actor_service": "business-operations-service", "authority": "INDEPENDENT_HUMAN_APPROVAL"}
			if e = h.rmsRecordDecision(ctx, tx, t, v, "APPROVED", "APPROVE", &approval, v.AppliedVersions, "Independent approval observed by automation", evidence); e != nil {
				return rmsJobOutcome{}, e
			}
			if e = h.rmsRunStage(ctx, tx, t, run, "APPROVED", "INDEPENDENT_APPROVAL_GRANTED", evidence, nil); e != nil {
				return rmsJobOutcome{}, e
			}
			return rmsDone(evidence), h.rmsEnqueue(ctx, tx, t.Organisation, a.PropertyID, &a.ID, &run.ID, &v.ID, "APPLY_RATE", "run:"+run.ID.String()+":apply", now.Add(time.Duration(a.Limits.ApplyDelaySeconds)*time.Second))
		default:
			if e = h.rmsServiceReject(ctx, tx, t, v.ID, "APPROVAL_"+state); e != nil {
				return rmsJobOutcome{}, e
			}
			return rmsDone(map[string]any{"approval_state": state}), h.rmsRunStage(ctx, tx, t, run, "REJECTED", "APPROVAL_"+state, map[string]any{"approval_id": *run.ApprovalID}, nil)
		}
	case "APPLY_RATE":
		return h.rmsApplyRun(ctx, tx, t, a, run, p)
	}
	return rmsJobOutcome{}, apperrors.Conflict("RMS_JOB_KIND_UNSUPPORTED", "Unsupported RMS run job.")
}

// rmsSubmitApproval files an approval request on the sponsor's mandate. The
// request is explicitly marked as service-submitted; the sponsor (requester)
// can never decide it, and the automation never decides any approval.
func (h *Handler) rmsSubmitApproval(ctx context.Context, tx pgx.Tx, t tenant, a RMSAutomationActivation, run RMSAutomationRun, v RMSRecommendation, p RMSPolicy) (ids.ID, error) {
	sponsor := tenant{Organisation: t.Organisation, Account: a.SponsorAccountID, Member: a.SponsorMembershipID, Role: a.SponsorRoleID, State: t.State}
	target, e := resolve(ctx, tx, t.Organisation, "PROPERTY", a.PropertyID)
	if e != nil {
		return ids.Nil, e
	}
	amount := rmsExposure(v, false)
	pol, e := h.findPolicy(ctx, tx, sponsor, target, rmsAutomatedRateRule, "HIGH", &amount, p.Definition.Currency, "")
	if e != nil {
		return ids.Nil, apperrors.Conflict("APPROVAL_POLICY_UNAVAILABLE", "No single effective approval policy governs automated RMS rate changes for this scope.")
	}
	now := h.Auth.Service.Now().UTC()
	deadline := v.ExpiresAt
	if limit := now.Add(7 * 24 * time.Hour); limit.Before(deadline) {
		deadline = limit
	}
	if !deadline.After(now) {
		return ids.Nil, apperrors.Conflict("RECOMMENDATION_EXPIRED", "The proposal expires before an approval could be decided.")
	}
	request, e := approvals.NewRequest(pol, a.SponsorAccountID, target.Scope, &amount, p.Definition.Currency, now, deadline)
	if e != nil {
		return ids.Nil, apperrors.Conflict("APPROVAL_POLICY_UNAVAILABLE", "The governing approval policy cannot create this request.")
	}
	view := request.Snapshot()
	snapshot, e := json.Marshal(pol)
	if e != nil {
		return ids.Nil, e
	}
	op := rmsAutomationApprovalOperation(a, run, v)
	digest, e := operationDigest(rmsAutomatedRateRule, op)
	if e != nil {
		return ids.Nil, e
	}
	operationContext, e := normalizedOperation(op)
	if e != nil {
		return ids.Nil, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO approval_requests(id,organisation_id,policy_id,requester_id,action,scope_kind,scope_id,amount_minor,currency,policy_snapshot,state,deadline,reason,idempotency_key,request_hash,operation_digest,operation_context,channel) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15,$16,'')`, view.ID, t.Organisation, pol.ID, a.SponsorAccountID, rmsAutomatedRateRule, target.Scope.Kind, target.Scope.ID, amount, p.Definition.Currency, snapshot, view.State, deadline, "Submitted by RMS automation (business-operations-service) for run "+run.ID.String()+" on the mandate of activation "+a.ID.String(), "rms-automation:"+run.ID.String(), digest, []byte(operationContext)); e != nil {
		return ids.Nil, e
	}
	for i, level := range pol.Levels {
		if _, e := tx.Exec(ctx, `INSERT INTO approval_steps(id,organisation_id,request_id,level,capability,required_count,require_mfa) VALUES($1,$2,$3,$4,$5,$6,$7)`, ids.New(), t.Organisation, view.ID, i, string(level.Capability), level.Count, level.RequireMFA); e != nil {
			return ids.Nil, e
		}
	}
	if e = audit(ctx, tx, t, "APPROVAL_REQUEST_CREATED_BY_SERVICE", "approval_request", view.ID, map[string]any{"action": rmsAutomatedRateRule, "policy_id": pol.ID, "operation_digest": digest, "submitted_by_service": "business-operations-service", "sponsor_account_id": a.SponsorAccountID, "automation_run_id": run.ID}); e != nil {
		return ids.Nil, e
	}
	return view.ID, h.approvalEvent(ctx, tx, view, "business.approval.requested.v1")
}

// rmsApplyRun re-checks every authority and freshness input at application
// time, then writes through the existing canonical rate path.
func (h *Handler) rmsApplyRun(ctx context.Context, tx pgx.Tx, t tenant, a RMSAutomationActivation, run RMSAutomationRun, p RMSPolicy) (rmsJobOutcome, error) {
	now := h.Auth.Service.Now().UTC()
	v, e := loadRMSRecommendation(ctx, tx, t.Organisation, *run.RecommendationID)
	if e != nil {
		return rmsJobOutcome{}, e
	}
	if v.State == "EXPIRED" || !now.Before(v.ExpiresAt) {
		if v.State != "EXPIRED" {
			if e = h.rmsRecordDecision(ctx, tx, t, v, "EXPIRED", "EXPIRE", v.ApprovalID, v.AppliedVersions, "Proposal expired before scheduled application", map[string]any{"actor_service": "business-operations-service"}); e != nil {
				return rmsJobOutcome{}, e
			}
		}
		return rmsDone(map[string]any{"state": "EXPIRED"}), h.rmsRunStage(ctx, tx, t, run, "EXPIRED", "RECOMMENDATION_EXPIRED", map[string]any{"recommendation_id": v.ID}, nil)
	}
	if v.State != "APPROVED" || v.ApprovalID == nil || v.ChangeDigest != hashCommercialPayload(marshalCommercial(v.Snapshot)) {
		return rmsJobOutcome{}, apperrors.Conflict("RMS_RECOMMENDATION_STATE", "Exact approved proposal required.")
	}
	// Fence concurrent human and worker writers on the canonical rate row.
	if _, e = tx.Exec(ctx, `SELECT id FROM rate_plans WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, v.RatePlanID); e != nil {
		return rmsJobOutcome{}, e
	}
	f, e := loadRMSForecast(ctx, tx, t.Organisation, v.ForecastID)
	if e != nil {
		return rmsJobOutcome{}, e
	}
	if e = h.rmsForecastUsable(ctx, tx, t, p, f); e != nil {
		return rmsJobOutcome{}, apperrors.Conflict("SOURCE_STALE", "The forecast source is no longer fresh enough to apply.")
	}
	if e = h.rmsValidateCurrent(ctx, tx, t, p, v); e != nil {
		return rmsJobOutcome{}, e
	}
	decision, e := h.rmsEvaluate(ctx, tx, t, a, p, v, f)
	if e != nil {
		return rmsJobOutcome{}, e
	}
	if decision.Outcome == "BLOCKED" || a.Mode == "AUTOPILOT_WITH_LIMITS" && decision.Outcome != "AUTO_APPLY" {
		return rmsJobOutcome{}, apperrors.Conflict(decision.Code, "Re-evaluation at application time no longer permits this change.")
	}
	var state, action string
	var consumed *time.Time
	var deadline time.Time
	if e = tx.QueryRow(ctx, `SELECT state,action,consumed_at,deadline FROM approval_requests WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, *v.ApprovalID).Scan(&state, &action, &consumed, &deadline); e != nil {
		return rmsJobOutcome{}, e
	}
	if a.Mode == "AUTOPILOT_WITH_LIMITS" {
		if *v.ApprovalID != a.ApprovalID || state != "APPROVED" || action != rmsActivationApproval {
			return rmsJobOutcome{}, apperrors.Conflict("ACTIVATION_APPROVAL_INVALID", "The standing activation approval is no longer valid.")
		}
	} else {
		digest, e := operationDigest(rmsAutomatedRateRule, rmsAutomationApprovalOperation(a, run, v))
		if e != nil {
			return rmsJobOutcome{}, e
		}
		var stored string
		if e = tx.QueryRow(ctx, `SELECT operation_digest FROM approval_requests WHERE organisation_id=$1 AND id=$2`, t.Organisation, *v.ApprovalID).Scan(&stored); e != nil {
			return rmsJobOutcome{}, e
		}
		if state != "APPROVED" || action != rmsAutomatedRateRule || consumed != nil || !now.Before(deadline) || stored != digest {
			return rmsJobOutcome{}, apperrors.Conflict("APPROVAL_NOT_USABLE", "The independent approval is not current, exact and unconsumed.")
		}
		if _, e = tx.Exec(ctx, `UPDATE approval_requests SET consumed_at=$3 WHERE organisation_id=$1 AND id=$2 AND consumed_at IS NULL`, t.Organisation, *v.ApprovalID, now); e != nil {
			return rmsJobOutcome{}, e
		}
		if e = audit(ctx, tx, t, "APPROVAL_AUTHORIZATION_CONSUMED_BY_SERVICE", "approval_request", *v.ApprovalID, map[string]any{"action": rmsAutomatedRateRule, "operation_digest": digest, "automation_run_id": run.ID}); e != nil {
			return rmsJobOutcome{}, e
		}
	}
	applied, e := h.rmsWriteSegments(ctx, tx, t, v, *v.ApprovalID, false)
	if e != nil {
		return rmsJobOutcome{}, e
	}
	evidence := map[string]any{"decision": decision, "automation_run_id": run.ID, "activation_id": a.ID, "actor_service": "business-operations-service", "external_delivery_confirmed": false}
	if e = h.rmsRecordDecision(ctx, tx, t, v, "APPLIED", "APPLY", v.ApprovalID, applied, "Automation applied the exact approved proposal", evidence); e != nil {
		return rmsJobOutcome{}, e
	}
	if e = h.rmsRunStage(ctx, tx, t, run, "APPLIED", "CANONICAL_RATE_VERSIONED", map[string]any{"rate_version_ids": applied, "external_delivery_confirmed": false}, nil); e != nil {
		return rmsJobOutcome{}, e
	}
	if e = h.rmsEnqueue(ctx, tx, t.Organisation, a.PropertyID, &a.ID, nil, &v.ID, "MEASURE_OUTCOME", "recommendation:"+v.ID.String()+":outcome", now.Add(time.Duration(a.Limits.OutcomeDelaySeconds)*time.Second)); e != nil {
		return rmsJobOutcome{}, e
	}
	return rmsDone(map[string]any{"rate_version_ids": applied}), h.rmsEnqueue(ctx, tx, t.Organisation, a.PropertyID, &a.ID, nil, &v.ID, "RECONCILE_PUBLICATION", "recommendation:"+v.ID.String()+":publication", now)
}

// rmsMeasureOutcome compares the retained forecast with later canonical facts.
// It records realized booking evidence only; cash and refunds stay external.
func (h *Handler) rmsMeasureOutcome(ctx context.Context, tx pgx.Tx, t tenant, job rmsJob) (rmsJobOutcome, error) {
	if job.RecommendationID == nil {
		return rmsJobOutcome{}, apperrors.Conflict("RMS_JOB_SCOPE_INVALID", "Outcome job lacks its recommendation.")
	}
	v, e := loadRMSRecommendation(ctx, tx, t.Organisation, *job.RecommendationID)
	if e != nil {
		return rmsJobOutcome{}, e
	}
	if v.State != "APPLIED" && v.State != "ROLLED_BACK" {
		return rmsDone(map[string]any{"skipped": true, "state": v.State}), nil
	}
	result, status, e := h.rmsOutcome(ctx, tx, t, v)
	if e != nil {
		return rmsJobOutcome{}, e
	}
	digest := hashCommercialPayload(marshalCommercial(result))
	id := ids.New()
	tag, e := tx.Exec(ctx, `INSERT INTO rms_outcome_measurements(id,organisation_id,property_id,recommendation_id,run_id,status,measured_at,source_digest,result) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(recommendation_id,source_digest) DO NOTHING`, id, t.Organisation, v.PropertyID, v.ID, v.AutomationRunID, status, h.Auth.Service.Now().UTC(), digest, marshalCommercial(result))
	if e != nil {
		return rmsJobOutcome{}, e
	}
	if tag.RowsAffected() == 1 {
		if e = h.commercialCommit(ctx, tx, t, v.PropertyID, id, "rms_outcome_measurement", "RMS_OUTCOME_MEASURED", "revenue.outcome.measured.v1", 1, map[string]any{"recommendation_id": v.ID, "status": status, "source_digest": digest, "realized_financial_truth": false}); e != nil {
			return rmsJobOutcome{}, e
		}
	}
	if status != "MEASURED" {
		return rmsLater(h.Auth.Service.Now().UTC().Add(6*time.Hour), map[string]any{"status": status}), nil
	}
	if job.ActivationID != nil {
		if e = h.rmsEnqueue(ctx, tx, t.Organisation, v.PropertyID, job.ActivationID, nil, nil, "DETECT_DRIFT", "activation:"+job.ActivationID.String()+":drift-after:"+v.ID.String(), h.Auth.Service.Now().UTC()); e != nil {
			return rmsJobOutcome{}, e
		}
	}
	return rmsDone(map[string]any{"status": status, "source_digest": digest}), nil
}

// rmsOutcome measures forecast error, applied-rate effectiveness, revenue,
// cancellations, no-shows and channel contribution for the proposal dates.
func (h *Handler) rmsOutcome(ctx context.Context, q database.Querier, t tenant, v RMSRecommendation) (map[string]any, string, error) {
	property, e := portfolio.Get(ctx, q, t.Organisation, v.PropertyID)
	if e != nil {
		return nil, "", e
	}
	loc, e := time.LoadLocation(property.Timezone)
	if e != nil {
		return nil, "", e
	}
	today := h.Auth.Service.Now().In(loc).Format(time.DateOnly)
	f, e := loadRMSForecast(ctx, q, t.Organisation, v.ForecastID)
	if e != nil {
		return nil, "", e
	}
	var appliedAt time.Time
	if e = q.QueryRow(ctx, `SELECT created_at FROM pricing_decisions WHERE organisation_id=$1 AND recommendation_id=$2 AND state='APPLIED'`, t.Organisation, v.ID).Scan(&appliedAt); e != nil {
		return nil, "", e
	}
	points := []revenue.OutcomePoint{}
	for _, d := range f.Result.Days {
		if d.ProjectedSoldMilliNights == nil {
			continue
		}
		var capacity, confirmed int64
		if e = q.QueryRow(ctx, `SELECT coalesce((SELECT capacity-blocked FROM inventory_calendar WHERE organisation_id=$1 AND pool_id=$2 AND day=$3::date),-1),(SELECT coalesce(sum(d.quantity),0) FROM reservation_inventory_days d JOIN reservation_rooms r ON r.organisation_id=d.organisation_id AND r.id=d.room_id JOIN reservations p ON p.organisation_id=r.organisation_id AND p.id=r.reservation_id WHERE d.organisation_id=$1 AND d.pool_id=$2 AND d.day=$3::date AND d.released_at IS NULL AND p.state IN ('CONFIRMED','CHECKED_IN','CHECKED_OUT','COMPLETED'))`, t.Organisation, v.PoolID, d.Day).Scan(&capacity, &confirmed); e != nil {
			return nil, "", e
		}
		if capacity < 0 {
			continue
		}
		points = append(points, revenue.OutcomePoint{Day: d.Day, PhysicalCapacity: capacity, PredictedMilliNights: *d.ProjectedSoldMilliNights, ActualConfirmedNights: confirmed})
	}
	type line struct {
		Channel            string `json:"channel"`
		State              string `json:"state"`
		Nights             int64  `json:"nights"`
		AccommodationMinor *int64 `json:"accommodation_minor"`
		Total              int64  `json:"total"`
		After              bool   `json:"after"`
		CancelledAfter     bool   `json:"cancelled_after"`
	}
	rows, e := commercialJSONRows(ctx, q, `SELECT jsonb_build_object('channel',p.channel,'state',p.state,'nights',(least(r.date_to,$4::date)-greatest(r.date_from,$3::date))*r.quantity,'accommodation_minor',r.accommodation_minor,'total',(r.date_to-r.date_from)*r.quantity,'after',r.accepted_at>=$5,'cancelled_after',p.cancelled_at IS NOT NULL AND p.cancelled_at>=$5) FROM reservation_rooms r JOIN reservations p ON p.organisation_id=r.organisation_id AND p.id=r.reservation_id WHERE r.organisation_id=$1 AND r.pool_id=$2 AND r.date_to>$3::date AND r.date_from<$4::date ORDER BY r.id`, t.Organisation, v.PoolID, v.DateFrom, v.DateTo, appliedAt)
	if e != nil {
		return nil, "", e
	}
	channels := map[string]map[string]int64{}
	var afterNights, afterMinor, cancelled, noShow int64
	revenueKnown := true
	for _, raw := range rows {
		var l line
		if e = json.Unmarshal(raw, &l); e != nil {
			return nil, "", e
		}
		if l.CancelledAfter {
			cancelled += l.Nights
		}
		if l.State == "NO_SHOW" {
			noShow += l.Nights
		}
		if !l.After || !slices.Contains([]string{"CONFIRMED", "CHECKED_IN", "CHECKED_OUT", "COMPLETED"}, l.State) || l.Nights <= 0 {
			continue
		}
		afterNights += l.Nights
		c := channels[l.Channel]
		if c == nil {
			c = map[string]int64{}
			channels[l.Channel] = c
		}
		c["room_nights"] += l.Nights
		if l.AccommodationMinor == nil || l.Total <= 0 {
			revenueKnown = false
			continue
		}
		share, e := revenue.Ratio(*l.AccommodationMinor, l.Nights, l.Total)
		if e != nil {
			return nil, "", e
		}
		afterMinor += share
		c["accepted_accommodation_minor"] += share
	}
	drift := revenue.DetectDrift(points, 1, 1)
	var adr, accepted any
	if revenueKnown {
		accepted = afterMinor
		if afterNights > 0 {
			value, _ := revenue.Ratio(afterMinor, 1, afterNights)
			adr = value
		}
	}
	segments := []map[string]any{}
	for _, s := range v.Snapshot.Segments {
		segments = append(segments, map[string]any{"day": s.DateFrom, "previous_minor": s.Previous.Definition.AmountMinor, "applied_minor": s.Proposed.AmountMinor})
	}
	status := "MEASURED"
	if v.DateTo > today {
		status = "PENDING_STAY_DATES"
	}
	if len(points) == 0 {
		status = "INSUFFICIENT_DATA"
	}
	return map[string]any{"points": points, "forecast_error": drift, "applied_rate_effectiveness": map[string]any{"segments": segments, "post_application_room_nights": afterNights, "post_application_accepted_minor": accepted, "post_application_adr_minor": adr}, "cancelled_after_application_nights": cancelled, "no_show_nights": noShow, "channels": channels, "basis": "CANONICAL_CONFIRMED_BOOKINGS_NOT_REALIZED_CASH", "realized_financial_truth": false, "measured_through": today}, status, nil
}

func (h *Handler) rmsActivationPoints(ctx context.Context, q database.Querier, t tenant, a RMSAutomationActivation) ([]revenue.OutcomePoint, error) {
	rows, e := commercialJSONRows(ctx, q, `SELECT m.result->'points' FROM rms_outcome_measurements m JOIN pricing_recommendations r ON r.organisation_id=m.organisation_id AND r.id=m.recommendation_id JOIN rms_automation_runs run ON run.organisation_id=r.organisation_id AND run.id=r.automation_run_id WHERE m.organisation_id=$1 AND run.activation_id=$2 AND m.status='MEASURED' AND m.created_at=(SELECT max(x.created_at) FROM rms_outcome_measurements x WHERE x.recommendation_id=m.recommendation_id AND x.status='MEASURED') ORDER BY m.created_at DESC,m.id DESC LIMIT 100`, t.Organisation, a.ID)
	if e != nil {
		return nil, e
	}
	points := []revenue.OutcomePoint{}
	for _, raw := range rows {
		var batch []revenue.OutcomePoint
		if e = json.Unmarshal(raw, &batch); e != nil {
			return nil, e
		}
		points = append(points, batch...)
	}
	return points, nil
}

func (h *Handler) rmsSafetyPause(ctx context.Context, tx pgx.Tx, t tenant, a RMSAutomationActivation, reason string) (bool, error) {
	if a.State != "ACTIVE" {
		return false, nil
	}
	_, e := h.rmsActivationTransition(ctx, tx, t, a, "PAUSED", "Automatic safety pause: "+reason, nil)
	return e == nil, e
}

func (h *Handler) rmsDetectDrift(ctx context.Context, tx pgx.Tx, t tenant, a RMSAutomationActivation) (rmsJobOutcome, error) {
	points, e := h.rmsActivationPoints(ctx, tx, t, a)
	if e != nil {
		return rmsJobOutcome{}, e
	}
	drift := revenue.DetectDrift(points, a.Limits.DriftThresholdBPS, a.Limits.DriftMinMeasurements)
	result := map[string]any{"drift": drift}
	if !drift.Drift {
		return rmsDone(result), nil
	}
	action := "ROLLBACK_REVIEW_QUEUED"
	if a.Limits.AutoPauseOnDrift && a.State == "ACTIVE" {
		action = "AUTOMATION_PAUSED"
	}
	var latest *ids.ID
	var id ids.ID
	if e = tx.QueryRow(ctx, `SELECT r.id FROM pricing_recommendations r JOIN rms_automation_runs run ON run.organisation_id=r.organisation_id AND run.id=r.automation_run_id WHERE r.organisation_id=$1 AND run.activation_id=$2 AND r.state='APPLIED' ORDER BY r.updated_at DESC,r.id DESC LIMIT 1`, t.Organisation, a.ID).Scan(&id); e == nil {
		latest = &id
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return rmsJobOutcome{}, e
	}
	created, e := h.rmsFinding(ctx, tx, t, a.PropertyID, &a.ID, latest, "FORECAST_DRIFT", "CRITICAL", action, "activation:"+a.ID.String()+":drift:"+hashCommercialPayload(marshalCommercial(points))[:32], map[string]any{"drift": drift, "rollback_review_recommendation_id": latest})
	if e != nil {
		return rmsJobOutcome{}, e
	}
	if created && action == "AUTOMATION_PAUSED" {
		if _, e = h.rmsSafetyPause(ctx, tx, t, a, fmt.Sprintf("forecast drift %d bps exceeds %d bps", *drift.MeanAbsErrBPS, a.Limits.DriftThresholdBPS)); e != nil {
			return rmsJobOutcome{}, e
		}
	}
	result["action"] = action
	return rmsDone(result), nil
}

func (h *Handler) rmsCheckFreshness(ctx context.Context, tx pgx.Tx, t tenant, a RMSAutomationActivation) (rmsJobOutcome, error) {
	now := h.Auth.Service.Now().UTC()
	var last ids.ID
	var asOf time.Time
	e := tx.QueryRow(ctx, `SELECT id,as_of FROM demand_snapshots WHERE organisation_id=$1 AND property_id=$2 AND pool_id=$3 ORDER BY as_of DESC,id DESC LIMIT 1`, t.Organisation, a.PropertyID, a.PoolID).Scan(&last, &asOf)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return rmsJobOutcome{}, e
	}
	result := map[string]any{}
	allowed := time.Duration(a.Limits.MaxSourceAgeSeconds+a.Limits.RunIntervalSeconds) * time.Second
	if errors.Is(e, pgx.ErrNoRows) || now.Sub(asOf) > allowed {
		key := "activation:" + a.ID.String() + ":stale:none"
		if !last.IsZero() {
			key = "activation:" + a.ID.String() + ":stale:" + last.String()
		}
		if _, e = h.rmsFinding(ctx, tx, t, a.PropertyID, &a.ID, nil, "SOURCE_STALE", "WARNING", "NONE", key, map[string]any{"latest_snapshot_id": last, "allowed_age_seconds": int64(allowed / time.Second)}); e != nil {
			return rmsJobOutcome{}, e
		}
		result["source_stale"] = true
	}
	baseline := revenue.ModelRef{Key: revenue.BaselineForecastModel, Version: 1}
	ineligible := !slices.Contains(a.Limits.EligibleModels, baseline)
	for _, m := range a.Limits.EligibleModels {
		var registered bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM forecast_models WHERE model_key=$1 AND version=$2)`, m.Key, m.Version).Scan(&registered); e != nil {
			return rmsJobOutcome{}, e
		}
		ineligible = ineligible || !registered
	}
	if ineligible {
		created, e := h.rmsFinding(ctx, tx, t, a.PropertyID, &a.ID, nil, "MODEL_INELIGIBLE", "CRITICAL", "AUTOMATION_PAUSED", "activation:"+a.ID.String()+":model", map[string]any{"pipeline_model": baseline, "eligible_models": a.Limits.EligibleModels})
		if e != nil {
			return rmsJobOutcome{}, e
		}
		if created {
			if _, e = h.rmsSafetyPause(ctx, tx, t, a, "the pipeline forecast model is not an eligible registered version"); e != nil {
				return rmsJobOutcome{}, e
			}
		}
		result["model_ineligible"] = true
	}
	return rmsDone(result), nil
}

func (h *Handler) rmsMonitorLimits(ctx context.Context, tx pgx.Tx, t tenant, a RMSAutomationActivation) (rmsJobOutcome, error) {
	if a.State != "ACTIVE" {
		return rmsDone(map[string]any{"skipped": true, "state": a.State}), nil
	}
	if _, e := h.rmsAuthority(ctx, tx, t, a); e != nil {
		if !rmsDomainError(e) {
			return rmsJobOutcome{}, e
		}
		code := commercialErrorCode(e)
		kind := "POLICY_EXCEPTION"
		if code == "SPONSOR_AUTHORITY_REVOKED" || code == "COMMERCIAL_AUTHORITY_UNAVAILABLE" || code == "CAPACITY_MISMATCH" {
			kind = "AUTHORITY_REVOKED"
		}
		if _, e = h.rmsFinding(ctx, tx, t, a.PropertyID, &a.ID, nil, kind, "CRITICAL", "AUTOMATION_PAUSED", "activation:"+a.ID.String()+":authority:"+code+":"+fmt.Sprint(a.Version), map[string]any{"code": code}); e != nil {
			return rmsJobOutcome{}, e
		}
		if _, e = h.rmsSafetyPause(ctx, tx, t, a, code); e != nil {
			return rmsJobOutcome{}, e
		}
		return rmsDone(map[string]any{"paused": true, "code": code}), nil
	}
	// Re-measure movement budgets on every date this activation changed.
	rows, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(r) FROM pricing_recommendations r JOIN rms_automation_runs run ON run.organisation_id=r.organisation_id AND run.id=r.automation_run_id WHERE r.organisation_id=$1 AND run.activation_id=$2 AND r.state='APPLIED' AND r.updated_at>=$3 ORDER BY r.id`, t.Organisation, a.ID, h.Auth.Service.Now().UTC().AddDate(0, 0, -a.Limits.CumulativeDays))
	if e != nil {
		return rmsJobOutcome{}, e
	}
	p, e := loadRMSPolicy(ctx, tx, t.Organisation, a.PolicyID)
	if e != nil {
		return rmsJobOutcome{}, e
	}
	breaches := []map[string]any{}
	for _, raw := range rows {
		var v RMSRecommendation
		if e = json.Unmarshal(raw, &v); e != nil {
			return rmsJobOutcome{}, e
		}
		for _, s := range v.Snapshot.Segments {
			daily, cumulative, e := h.rmsMovementRemaining(ctx, tx, t, v.RatePlanID, p.Definition.Currency, s.DateFrom, a.Limits.MaxDailyChangeBPS, a.Limits.MaxCumulativeChangeBPS, a.Limits.CumulativeDays)
			if e != nil {
				if rmsDomainError(e) {
					breaches = append(breaches, map[string]any{"day": s.DateFrom, "code": commercialErrorCode(e)})
					continue
				}
				return rmsJobOutcome{}, e
			}
			if daily < 0 || cumulative < 0 {
				breaches = append(breaches, map[string]any{"day": s.DateFrom, "recommendation_id": v.ID, "daily_remaining_minor": daily, "cumulative_remaining_minor": cumulative})
			}
		}
	}
	if len(breaches) == 0 {
		return rmsDone(map[string]any{"breaches": 0}), nil
	}
	created, e := h.rmsFinding(ctx, tx, t, a.PropertyID, &a.ID, nil, "POLICY_LIMIT_BREACH", "CRITICAL", "AUTOMATION_PAUSED", "activation:"+a.ID.String()+":limits:"+hashCommercialPayload(marshalCommercial(breaches))[:32], map[string]any{"breaches": breaches})
	if e != nil {
		return rmsJobOutcome{}, e
	}
	if created {
		if _, e = h.rmsSafetyPause(ctx, tx, t, a, "canonical movement exceeds approved limits"); e != nil {
			return rmsJobOutcome{}, e
		}
	}
	return rmsDone(map[string]any{"breaches": breaches, "paused": created}), nil
}

// rmsReconcilePublication never treats a queued or unknown channel delivery as
// success. Delivery is observed from the existing distribution records.
func (h *Handler) rmsReconcilePublication(ctx context.Context, tx pgx.Tx, t tenant, job rmsJob) (rmsJobOutcome, error) {
	if job.RecommendationID == nil {
		return rmsJobOutcome{}, apperrors.Conflict("RMS_JOB_SCOPE_INVALID", "Publication job lacks its recommendation.")
	}
	v, e := loadRMSRecommendation(ctx, tx, t.Organisation, *job.RecommendationID)
	if e != nil {
		return rmsJobOutcome{}, e
	}
	var appliedAt time.Time
	if e = tx.QueryRow(ctx, `SELECT created_at FROM pricing_decisions WHERE organisation_id=$1 AND recommendation_id=$2 AND state='APPLIED'`, t.Organisation, v.ID).Scan(&appliedAt); e != nil {
		return rmsJobOutcome{}, e
	}
	var connections, unconfirmed, failed int
	if e = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM channel_connections WHERE organisation_id=$1 AND property_id=$2 AND state IN ('CONNECTED','DEGRADED')),(SELECT count(*) FROM listing_publication p JOIN channel_connections c ON c.organisation_id=p.organisation_id AND c.id=p.connection_id WHERE p.organisation_id=$1 AND c.property_id=$2 AND p.desired_state='PUBLISHED' AND p.delivery_state<>'CONFIRMED'),(SELECT count(*) FROM channel_sync_jobs WHERE organisation_id=$1 AND property_id=$2 AND created_at>=$3 AND state IN ('QUARANTINED','FAILED_FINAL'))`, t.Organisation, v.PropertyID, appliedAt).Scan(&connections, &unconfirmed, &failed); e != nil {
		return rmsJobOutcome{}, e
	}
	result := map[string]any{"connections": connections, "unconfirmed_publications": unconfirmed, "failed_sync_jobs": failed, "external_delivery_confirmed": false}
	now := h.Auth.Service.Now().UTC()
	key := "recommendation:" + v.ID.String() + ":publication-unknown"
	if connections == 0 {
		result["state"] = "NO_ACTIVE_CHANNEL_CONNECTIONS"
		return rmsDone(result), nil
	}
	if unconfirmed > 0 || failed > 0 {
		result["state"] = "UNKNOWN"
		if now.Sub(appliedAt) >= 15*time.Minute || failed > 0 {
			action := "NONE"
			if now.Sub(appliedAt) >= time.Hour {
				action = "ROLLBACK_REVIEW_QUEUED"
			}
			if _, e = h.rmsFinding(ctx, tx, t, v.PropertyID, job.ActivationID, &v.ID, "PUBLICATION_UNKNOWN", "WARNING", action, key, result); e != nil {
				return rmsJobOutcome{}, e
			}
		}
		if now.Sub(appliedAt) > 7*24*time.Hour {
			return rmsDone(result), nil
		}
		return rmsLater(now.Add(5*time.Minute), result), nil
	}
	result["state"] = "CONFIRMED_BY_DISTRIBUTION_RECORDS"
	if _, e = tx.Exec(ctx, `UPDATE rms_monitoring_findings SET state='RESOLVED',resolution='Distribution records confirm every publication',resolved_by_service='business-operations-service',resolved_at=now(),version=version+1,updated_at=now() WHERE organisation_id=$1 AND dedupe_key=$2 AND state='OPEN'`, t.Organisation, key); e != nil {
		return rmsJobOutcome{}, e
	}
	return rmsDone(result), nil
}
