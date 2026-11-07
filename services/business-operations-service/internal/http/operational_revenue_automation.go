package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// RMSAutomationActivation is the independently approved mandate that alone can
// make APPROVAL_AUTOMATION or AUTOPILOT_WITH_LIMITS execute.
type RMSAutomationActivation struct {
	ID                  ids.ID                   `json:"id"`
	PropertyID          ids.ID                   `json:"property_id"`
	BranchID            ids.ID                   `json:"branch_id"`
	PoolID              ids.ID                   `json:"pool_id"`
	RatePlanID          ids.ID                   `json:"rate_plan_id"`
	PolicyID            ids.ID                   `json:"policy_id"`
	Mode                string                   `json:"mode"`
	Limits              revenue.AutomationLimits `json:"limits"`
	LimitsDigest        string                   `json:"limits_digest"`
	SponsorAccountID    ids.ID                   `json:"sponsor_account_id"`
	SponsorMembershipID ids.ID                   `json:"sponsor_membership_id"`
	SponsorRoleID       ids.ID                   `json:"sponsor_role_id"`
	ApprovalID          ids.ID                   `json:"approval_id"`
	State               string                   `json:"state"`
	StateReason         string                   `json:"state_reason"`
	EffectiveAt         time.Time                `json:"effective_at"`
	ExpiresAt           time.Time                `json:"expires_at"`
	NextRunAt           time.Time                `json:"next_run_at"`
	Reason              string                   `json:"reason"`
	Version             int64                    `json:"version"`
	CreatedAt           time.Time                `json:"created_at"`
	UpdatedAt           time.Time                `json:"updated_at"`
}
type RMSAutomationActivateCommand struct {
	PolicyID    ids.ID                   `json:"policy_id"`
	Mode        string                   `json:"mode"`
	Limits      revenue.AutomationLimits `json:"limits"`
	EffectiveAt time.Time                `json:"effective_at"`
	ExpiresAt   time.Time                `json:"expires_at"`
	ApprovalID  ids.ID                   `json:"approval_id,omitempty"`
	Reason      string                   `json:"reason"`
}
type RMSAutomationStateCommand struct {
	Version    int64  `json:"version"`
	ApprovalID ids.ID `json:"approval_id,omitempty"`
	Reason     string `json:"reason"`
}
type RMSAutomationRunCommand struct {
	Reason string `json:"reason"`
}
type RMSAutomationRun struct {
	ID               ids.ID          `json:"id"`
	PropertyID       ids.ID          `json:"property_id"`
	ActivationID     ids.ID          `json:"activation_id"`
	Mode             string          `json:"mode"`
	Trigger          string          `json:"trigger"`
	Stage            string          `json:"stage"`
	SnapshotID       *ids.ID         `json:"snapshot_id,omitempty"`
	ForecastID       *ids.ID         `json:"forecast_id,omitempty"`
	RecommendationID *ids.ID         `json:"recommendation_id,omitempty"`
	ApprovalID       *ids.ID         `json:"approval_id,omitempty"`
	ChangeDigest     *string         `json:"change_digest,omitempty"`
	BlockedCode      string          `json:"blocked_code"`
	Evidence         json.RawMessage `json:"evidence"`
	RequestedBy      *ids.ID         `json:"requested_by,omitempty"`
	CreatedByService string          `json:"created_by_service"`
	Version          int64           `json:"version"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}
type RMSAutomationRunDetail struct {
	Run    RMSAutomationRun  `json:"run"`
	Stages []json.RawMessage `json:"stages"`
	Jobs   []json.RawMessage `json:"jobs"`
}
type RMSFindingResolveCommand struct {
	Version    int64  `json:"version"`
	Resolution string `json:"resolution"`
}

func (h *Handler) registerRMSAutomation(r chi.Router) {
	r.Get("/revenue/automation-activations", h.rmsAutomationActivations)
	r.Post("/revenue/automation-activations", h.rmsAutomationActivate)
	r.Get("/revenue/automation-activations/{id}", h.rmsAutomationActivationGet)
	r.Post("/revenue/automation-activations/{id}/pause", h.rmsAutomationPause)
	r.Post("/revenue/automation-activations/{id}/resume", h.rmsAutomationResume)
	r.Post("/revenue/automation-activations/{id}/revoke", h.rmsAutomationRevoke)
	r.Post("/revenue/automation-activations/{id}/runs", h.rmsAutomationRunTrigger)
	r.Get("/revenue/automation-runs", h.rmsAutomationRuns)
	r.Get("/revenue/automation-runs/{id}", h.rmsAutomationRunGet)
	r.Get("/revenue/automation-jobs", h.rmsAutomationJobs)
	r.Get("/revenue/monitoring-findings", h.rmsMonitoringFindings)
	r.Post("/revenue/monitoring-findings/{id}/resolve", h.rmsMonitoringFindingResolve)
	r.Get("/revenue/outcomes", h.rmsOutcomes)
	h.registerRMSAnalytics(r)
	h.registerRMSAdvanced(r)
}

func loadRMSActivation(ctx context.Context, q database.Querier, org, id ids.ID, lock bool) (RMSAutomationActivation, error) {
	query := `SELECT to_jsonb(a) FROM rms_automation_activations a WHERE organisation_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var raw []byte
	e := q.QueryRow(ctx, query, org, id).Scan(&raw)
	return decodeRMSRecord[RMSAutomationActivation](raw, e)
}
func loadRMSRun(ctx context.Context, q database.Querier, org, id ids.ID, lock bool) (RMSAutomationRun, error) {
	query := `SELECT to_jsonb(r) FROM rms_automation_runs r WHERE organisation_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var raw []byte
	e := q.QueryRow(ctx, query, org, id).Scan(&raw)
	return decodeRMSRecord[RMSAutomationRun](raw, e)
}

func validRMSActivationCommand(in RMSAutomationActivateCommand) bool {
	return in.PolicyID.ValidateV7() == nil && (in.Mode == "APPROVAL_AUTOMATION" || in.Mode == "AUTOPILOT_WITH_LIMITS") && !in.EffectiveAt.IsZero() && in.ExpiresAt.After(in.EffectiveAt) && validStaffReason(in.Reason)
}

func (h *Handler) rmsAutomationActivate(w http.ResponseWriter, r *http.Request) {
	var in RMSAutomationActivateCommand
	if !decode(w, r, &in) {
		return
	}
	if !validRMSActivationCommand(in) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Exact policy, automatic mode, bounded limits, authorization window and reason required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	pre, e := loadRMSPolicy(r.Context(), h.Auth.Service.Pool, t.Organisation, in.PolicyID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "revenue:automation:activate:"+pre.RatePlanID.String(), in, "rms:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "rms:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		// The sponsor must personally hold live rate authority; autopilot also
		// needs the explicit autopilot capability. Labels never authorize.
		if _, _, e = h.authorize(ctx, tx, "rates:write", "PROPERTY", pre.PropertyID, true); e != nil {
			return 0, nil, e
		}
		if in.Mode == "AUTOPILOT_WITH_LIMITS" {
			if _, _, e = h.authorize(ctx, tx, "rms:autopilot", "PROPERTY", pre.PropertyID, true); e != nil {
				return 0, nil, e
			}
		}
		if e = h.commercialGate(ctx, tx, t, pre.PropertyID); e != nil {
			return 0, nil, e
		}
		p, e := loadRMSPolicy(ctx, tx, t.Organisation, in.PolicyID)
		if e != nil {
			return 0, nil, e
		}
		if e = h.rmsCurrentPolicy(ctx, tx, t, p); e != nil {
			return 0, nil, e
		}
		if p.Definition.Mode != in.Mode {
			return 0, nil, apperrors.Conflict("RMS_POLICY_MODE_MISMATCH", "The exact current policy version must authorize this automatic mode.")
		}
		if in.Limits.ValidateAgainst(p.Definition, in.Mode) != nil {
			return 0, nil, apperrors.InvalidRequest("Automation limits must be valid and no wider than the current RMS policy.")
		}
		now := h.Auth.Service.Now().UTC()
		if in.EffectiveAt.Before(now.Add(-time.Minute)) || in.EffectiveAt.After(now.Add(24*time.Hour)) || in.ExpiresAt.After(p.Definition.ExpiresAt) || in.ExpiresAt.After(in.EffectiveAt.AddDate(0, 0, 366)) {
			return 0, nil, apperrors.InvalidRequest("Activation must begin now or within 24 hours and end within the policy authorization.")
		}
		property, e := portfolio.Get(ctx, tx, t.Organisation, p.PropertyID)
		if e != nil {
			return 0, nil, e
		}
		if property.CapacityCode != in.Limits.RequiredCapacityCode {
			return 0, nil, apperrors.Conflict("RMS_CAPACITY_MISMATCH", "The required verified operating capacity does not match the property authority.")
		}
		for _, m := range in.Limits.EligibleModels {
			var registered bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM forecast_models WHERE model_key=$1 AND version=$2)`, m.Key, m.Version).Scan(&registered); e != nil {
				return 0, nil, e
			}
			if !registered {
				return 0, nil, apperrors.InvalidRequest("Every eligible model must be a registered forecast model version.")
			}
		}
		op := in
		op.ApprovalID = ids.Nil
		exposure := in.Limits.MaxExposureMinor
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RMS_AUTOMATION_ACTIVATION", target.Scope, in.ApprovalID, map[string]any{"command": op}, "", &exposure, p.Definition.Currency); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		digest := hashCommercialPayload(marshalCommercial(in.Limits))
		if _, e = tx.Exec(ctx, `INSERT INTO rms_automation_activations(id,organisation_id,property_id,branch_id,pool_id,rate_plan_id,policy_id,mode,limits,limits_digest,sponsor_account_id,sponsor_membership_id,sponsor_role_id,approval_id,effective_at,expires_at,next_run_at,reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$15,$17)`, id, t.Organisation, p.PropertyID, p.BranchID, p.PoolID, p.RatePlanID, p.ID, in.Mode, marshalCommercial(in.Limits), digest, t.Account, t.Member, t.Role, in.ApprovalID, in.EffectiveAt.UTC(), in.ExpiresAt.UTC(), in.Reason); e != nil {
			if database.SQLState(e) == database.CodeUniqueViolation {
				return 0, nil, apperrors.Conflict("RMS_AUTOMATION_ALREADY_ACTIVE", "Revoke the current activation for this rate before creating another.")
			}
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO rms_automation_activation_history(id,organisation_id,property_id,activation_id,activation_version,prior_state,state,reason,approval_id,actor_id) VALUES($1,$2,$3,$4,1,NULL,'ACTIVE',$5,$6,$7)`, ids.New(), t.Organisation, p.PropertyID, id, in.Reason, in.ApprovalID, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, p.PropertyID, id, "rms_automation_activation", "RMS_AUTOMATION_ACTIVATED", "revenue.automation.activation.changed.v1", 1, map[string]any{"activation_id": id, "mode": in.Mode, "policy_id": p.ID, "rate_plan_id": p.RatePlanID, "limits_digest": digest, "state": "ACTIVE", "approval_id": in.ApprovalID, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := loadRMSActivation(ctx, tx, t.Organisation, id, false)
		return 201, out, e
	})
}

// rmsActivationTransition appends history and changes only state/schedule.
func (h *Handler) rmsActivationTransition(ctx context.Context, tx pgx.Tx, t tenant, a RMSAutomationActivation, state, reason string, approval *ids.ID) (RMSAutomationActivation, error) {
	if _, e := tx.Exec(ctx, `INSERT INTO rms_automation_activation_history(id,organisation_id,property_id,activation_id,activation_version,prior_state,state,reason,approval_id,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, ids.New(), t.Organisation, a.PropertyID, a.ID, a.Version+1, a.State, state, reason, approval, t.Account); e != nil {
		return a, e
	}
	if _, e := tx.Exec(ctx, `UPDATE rms_automation_activations SET state=$3,state_reason=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, a.ID, state, reason); e != nil {
		return a, e
	}
	if e := h.commercialCommit(ctx, tx, t, a.PropertyID, a.ID, "rms_automation_activation", "RMS_AUTOMATION_"+state, "revenue.automation.activation.changed.v1", a.Version+1, map[string]any{"activation_id": a.ID, "prior_state": a.State, "state": state, "reason": reason, "approval_id": approval}); e != nil {
		return a, e
	}
	if state != "ACTIVE" {
		// Queued work for this mandate stops; open runs end blocked.
		if _, e := tx.Exec(ctx, `UPDATE rms_automation_jobs SET state='CANCELLED',result=result||jsonb_build_object('cancelled_by_activation_state',$3::text),version=version+1,updated_at=now() WHERE organisation_id=$1 AND activation_id=$2 AND state IN ('QUEUED','RETRY')`, t.Organisation, a.ID, state); e != nil {
			return a, e
		}
		rows, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(r) FROM rms_automation_runs r WHERE organisation_id=$1 AND activation_id=$2 AND stage IN ('QUEUED','SOURCE_CAPTURED','FORECAST_READY','RECOMMENDED','APPROVAL_PENDING','APPROVED') FOR UPDATE`, t.Organisation, a.ID)
		if e != nil {
			return a, e
		}
		for _, raw := range rows {
			var run RMSAutomationRun
			if e = json.Unmarshal(raw, &run); e != nil {
				return a, e
			}
			if e = h.rmsRunStage(ctx, tx, t, run, "BLOCKED", "AUTOMATION_"+state, map[string]any{"reason": reason}, nil); e != nil {
				return a, e
			}
		}
	}
	return loadRMSActivation(ctx, tx, t.Organisation, a.ID, false)
}

func (h *Handler) rmsActivationCommand(w http.ResponseWriter, r *http.Request, action string) {
	var in RMSAutomationStateCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) || action != "RESUME" && !in.ApprovalID.IsZero() {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current activation version and reason required; only resume consumes an approval."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	pre, e := loadRMSActivation(r.Context(), h.Auth.Service.Pool, t.Organisation, id, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "revenue:automation:"+action+":"+id.String(), in, "rms:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "rms:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		a, e := loadRMSActivation(ctx, tx, t.Organisation, id, true)
		if e != nil {
			return 0, nil, e
		}
		if a.Version != in.Version {
			return 0, nil, apperrors.Conflict("RMS_AUTOMATION_VERSION", "Activation changed.")
		}
		var next string
		var approval *ids.ID
		switch {
		case action == "PAUSE" && a.State == "ACTIVE":
			next = "PAUSED"
		case action == "REVOKE" && (a.State == "ACTIVE" || a.State == "PAUSED"):
			next = "REVOKED"
		case action == "RESUME" && a.State == "PAUSED":
			now := h.Auth.Service.Now().UTC()
			if !now.Before(a.ExpiresAt) {
				return 0, nil, apperrors.Conflict("RMS_AUTOMATION_EXPIRED", "An expired activation cannot resume.")
			}
			p, e := loadRMSPolicy(ctx, tx, t.Organisation, a.PolicyID)
			if e != nil {
				return 0, nil, e
			}
			if e = h.rmsCurrentPolicy(ctx, tx, t, p); e != nil {
				return 0, nil, e
			}
			if e = h.commercialGate(ctx, tx, t, a.PropertyID); e != nil {
				return 0, nil, e
			}
			exposure := a.Limits.MaxExposureMinor
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RMS_AUTOMATION_ACTIVATION", target.Scope, in.ApprovalID, map[string]any{"activation_id": a.ID, "action": "RESUME", "version": a.Version, "limits_digest": a.LimitsDigest}, "", &exposure, p.Definition.Currency); e != nil {
				return 0, nil, e
			}
			approval = &in.ApprovalID
			next = "ACTIVE"
		default:
			return 0, nil, apperrors.Conflict("RMS_AUTOMATION_STATE", "The activation cannot make this transition.")
		}
		out, e := h.rmsActivationTransition(ctx, tx, t, a, next, in.Reason, approval)
		return 200, out, e
	})
}
func (h *Handler) rmsAutomationPause(w http.ResponseWriter, r *http.Request) {
	h.rmsActivationCommand(w, r, "PAUSE")
}
func (h *Handler) rmsAutomationResume(w http.ResponseWriter, r *http.Request) {
	h.rmsActivationCommand(w, r, "RESUME")
}
func (h *Handler) rmsAutomationRevoke(w http.ResponseWriter, r *http.Request) {
	h.rmsActivationCommand(w, r, "REVOKE")
}

func (h *Handler) rmsAutomationRunTrigger(w http.ResponseWriter, r *http.Request) {
	var in RMSAutomationRunCommand
	if !decode(w, r, &in) {
		return
	}
	if !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Reason required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	pre, e := loadRMSActivation(r.Context(), h.Auth.Service.Pool, t.Organisation, id, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "revenue:automation:run:"+id.String(), in, "rms:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "rms:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		a, e := loadRMSActivation(ctx, tx, t.Organisation, id, true)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		if a.State != "ACTIVE" || now.Before(a.EffectiveAt) || !now.Before(a.ExpiresAt) {
			return 0, nil, apperrors.Conflict("RMS_AUTOMATION_NOT_ACTIVE", "Only a current active activation can run.")
		}
		run, e := h.rmsStartRun(ctx, tx, t, a, "MANUAL")
		if e != nil {
			return 0, nil, e
		}
		return 202, run, nil
	})
}

// rmsStartRun creates one run and its first durable job. The partial unique
// index guarantees at most one open run per activation across replicas.
func (h *Handler) rmsStartRun(ctx context.Context, tx pgx.Tx, t tenant, a RMSAutomationActivation, trigger string) (RMSAutomationRun, error) {
	var open bool
	if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rms_automation_runs WHERE organisation_id=$1 AND activation_id=$2 AND stage IN ('QUEUED','SOURCE_CAPTURED','FORECAST_READY','RECOMMENDED','APPROVAL_PENDING','APPROVED'))`, t.Organisation, a.ID).Scan(&open); e != nil {
		return RMSAutomationRun{}, e
	}
	if open {
		return RMSAutomationRun{}, apperrors.Conflict("RMS_AUTOMATION_RUN_OPEN", "An automation run for this activation is still in progress.")
	}
	id := ids.New()
	var requester any
	if !t.Account.IsZero() {
		requester = t.Account
	}
	if _, e := tx.Exec(ctx, `INSERT INTO rms_automation_runs(id,organisation_id,property_id,activation_id,mode,trigger,requested_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, t.Organisation, a.PropertyID, a.ID, a.Mode, trigger, requester); e != nil {
		return RMSAutomationRun{}, e
	}
	if _, e := tx.Exec(ctx, `INSERT INTO rms_automation_run_stages(id,organisation_id,property_id,run_id,stage,code,evidence,actor_id) VALUES($1,$2,$3,$4,'QUEUED',$5,$6,$7)`, ids.New(), t.Organisation, a.PropertyID, id, trigger, marshalCommercial(map[string]any{"activation_id": a.ID, "limits_digest": a.LimitsDigest, "mode": a.Mode}), requester); e != nil {
		return RMSAutomationRun{}, e
	}
	if e := h.rmsEnqueue(ctx, tx, t.Organisation, a.PropertyID, &a.ID, &id, nil, "CAPTURE_DEMAND", "run:"+id.String()+":capture", h.Auth.Service.Now().UTC()); e != nil {
		return RMSAutomationRun{}, e
	}
	if e := h.commercialCommit(ctx, tx, t, a.PropertyID, id, "rms_automation_run", "RMS_AUTOMATION_RUN_QUEUED", "revenue.automation.run.changed.v1", 1, map[string]any{"run_id": id, "activation_id": a.ID, "mode": a.Mode, "trigger": trigger, "stage": "QUEUED"}); e != nil {
		return RMSAutomationRun{}, e
	}
	return loadRMSRun(ctx, tx, t.Organisation, id, false)
}

// rmsEnqueue is idempotent by dedupe key.
func (h *Handler) rmsEnqueue(ctx context.Context, tx pgx.Tx, org, property ids.ID, activation, run, recommendation *ids.ID, kind, key string, at time.Time) error {
	_, e := tx.Exec(ctx, `INSERT INTO rms_automation_jobs(id,organisation_id,property_id,activation_id,run_id,recommendation_id,kind,dedupe_key,available_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(organisation_id,dedupe_key) DO NOTHING`, ids.New(), org, property, activation, run, recommendation, kind, key, at)
	return e
}

// rmsRunStage appends an immutable stage record and advances the run.
func (h *Handler) rmsRunStage(ctx context.Context, tx pgx.Tx, t tenant, run RMSAutomationRun, stage, code string, evidence map[string]any, set map[string]any) error {
	var actor any
	if !t.Account.IsZero() {
		actor = t.Account
	}
	if _, e := tx.Exec(ctx, `INSERT INTO rms_automation_run_stages(id,organisation_id,property_id,run_id,stage,code,evidence,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ids.New(), t.Organisation, run.PropertyID, run.ID, stage, code, marshalCommercial(evidence), actor); e != nil {
		return e
	}
	merged := map[string]any{}
	_ = json.Unmarshal(run.Evidence, &merged)
	merged[stage] = evidence
	args := []any{t.Organisation, run.ID, stage, code, marshalCommercial(merged)}
	query := `UPDATE rms_automation_runs SET stage=$3,blocked_code=CASE WHEN $3 IN ('BLOCKED','FAILED','REJECTED','EXPIRED') THEN $4 ELSE blocked_code END,evidence=$5,version=version+1,updated_at=now()`
	for _, column := range []string{"snapshot_id", "forecast_id", "recommendation_id", "approval_id", "change_digest"} {
		if v, ok := set[column]; ok {
			args = append(args, v)
			query += `,` + column + `=$` + strconv.Itoa(len(args))
		}
	}
	if _, e := tx.Exec(ctx, query+` WHERE organisation_id=$1 AND id=$2`, args...); e != nil {
		return e
	}
	return h.commercialCommit(ctx, tx, t, run.PropertyID, run.ID, "rms_automation_run", "RMS_AUTOMATION_RUN_"+stage, "revenue.automation.run.changed.v1", run.Version+1, map[string]any{"run_id": run.ID, "activation_id": run.ActivationID, "prior_stage": run.Stage, "stage": stage, "code": code, "rates_changed": stage == "APPLIED"})
}

func (h *Handler) rmsAutomationScopedList(w http.ResponseWriter, r *http.Request, table, order string) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	properties, e := h.operationalProperties(r.Context(), h.Auth.Service.Pool, t, "rms:read", r.URL.Query().Get("property_id"), r.URL.Query().Get("branch_id"))
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if !slices.Contains([]string{"rms_automation_activations", "rms_automation_runs", "rms_automation_jobs", "rms_monitoring_findings", "rms_outcome_measurements", "rms_market_inputs", "rms_advanced_recommendations"}, table) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Unsupported RMS collection."))
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x) FROM `+table+` x WHERE organisation_id=$1 AND property_id=ANY($2::uuid[]) ORDER BY `+order+` LIMIT $3 OFFSET $4`, t.Organisation, properties, limit, offset)
	if items == nil {
		items = []json.RawMessage{}
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) rmsAutomationActivations(w http.ResponseWriter, r *http.Request) {
	h.rmsAutomationScopedList(w, r, "rms_automation_activations", "created_at DESC,id DESC")
}
func (h *Handler) rmsAutomationRuns(w http.ResponseWriter, r *http.Request) {
	h.rmsAutomationScopedList(w, r, "rms_automation_runs", "created_at DESC,id DESC")
}
func (h *Handler) rmsAutomationJobs(w http.ResponseWriter, r *http.Request) {
	h.rmsAutomationScopedList(w, r, "rms_automation_jobs", "created_at DESC,id DESC")
}
func (h *Handler) rmsMonitoringFindings(w http.ResponseWriter, r *http.Request) {
	h.rmsAutomationScopedList(w, r, "rms_monitoring_findings", "created_at DESC,id DESC")
}
func (h *Handler) rmsOutcomes(w http.ResponseWriter, r *http.Request) {
	h.rmsAutomationScopedList(w, r, "rms_outcome_measurements", "created_at DESC,id DESC")
}
func (h *Handler) rmsAutomationActivationGet(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	v, e := loadRMSActivation(r.Context(), h.Auth.Service.Pool, t.Organisation, id, false)
	if e == nil {
		_, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "rms:read", "PROPERTY", v.PropertyID, false)
	}
	reply(w, r, 200, v, e)
}
func (h *Handler) rmsAutomationRunGet(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	v, e := loadRMSRun(r.Context(), h.Auth.Service.Pool, t.Organisation, id, false)
	if e == nil {
		_, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "rms:read", "PROPERTY", v.PropertyID, false)
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	stages, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(s) FROM rms_automation_run_stages s WHERE organisation_id=$1 AND run_id=$2 ORDER BY created_at,id`, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	jobs, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(j)-'lease_owner' FROM rms_automation_jobs j WHERE organisation_id=$1 AND run_id=$2 ORDER BY created_at,id`, t.Organisation, id)
	if stages == nil {
		stages = []json.RawMessage{}
	}
	if jobs == nil {
		jobs = []json.RawMessage{}
	}
	reply(w, r, 200, RMSAutomationRunDetail{Run: v, Stages: stages, Jobs: jobs}, e)
}
func (h *Handler) rmsMonitoringFindingResolve(w http.ResponseWriter, r *http.Request) {
	var in RMSFindingResolveCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Resolution) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current finding version and a resolution required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var property ids.ID
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT property_id FROM rms_monitoring_findings WHERE organisation_id=$1 AND id=$2`, t.Organisation, id).Scan(&property); errors.Is(e, pgx.ErrNoRows) {
		reply(w, r, 0, nil, apperrors.NotFound("RMS_RECORD_NOT_FOUND", "RMS record not found."))
		return
	} else if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "revenue:finding:resolve:"+id.String(), in, "rms:write", "PROPERTY", property, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "rms:write", "PROPERTY", property, true)
		if e != nil {
			return 0, nil, e
		}
		tag, e := tx.Exec(ctx, `UPDATE rms_monitoring_findings SET state='RESOLVED',resolution=$4,resolved_by=$5,resolved_at=now(),version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND version=$3 AND state='OPEN'`, t.Organisation, id, in.Version, in.Resolution, t.Account)
		if e != nil {
			return 0, nil, e
		}
		if tag.RowsAffected() != 1 {
			return 0, nil, apperrors.Conflict("RMS_FINDING_STATE", "Finding changed or is already resolved.")
		}
		if e = h.commercialCommit(ctx, tx, t, property, id, "rms_monitoring_finding", "RMS_FINDING_RESOLVED", "revenue.monitoring.finding.changed.v1", in.Version+1, map[string]any{"state": "RESOLVED", "resolution": in.Resolution}); e != nil {
			return 0, nil, e
		}
		out, e := oneJSON(ctx, tx, `SELECT to_jsonb(f) FROM rms_monitoring_findings f WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		return 200, out, e
	})
}

// rmsFinding records (or re-observes) one monitoring finding idempotently.
func (h *Handler) rmsFinding(ctx context.Context, tx pgx.Tx, t tenant, property ids.ID, activation, recommendation *ids.ID, kind, severity, action, key string, evidence map[string]any) (bool, error) {
	id := ids.New()
	tag, e := tx.Exec(ctx, `INSERT INTO rms_monitoring_findings(id,organisation_id,property_id,activation_id,recommendation_id,kind,severity,action_taken,dedupe_key,evidence) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(organisation_id,dedupe_key) DO NOTHING`, id, t.Organisation, property, activation, recommendation, kind, severity, action, key, marshalCommercial(evidence))
	if e != nil || tag.RowsAffected() == 0 {
		return false, e
	}
	return true, h.commercialCommit(ctx, tx, t, property, id, "rms_monitoring_finding", "RMS_FINDING_"+kind, "revenue.monitoring.finding.changed.v1", 1, map[string]any{"kind": kind, "severity": severity, "action_taken": action, "activation_id": activation, "recommendation_id": recommendation, "state": "OPEN"})
}
