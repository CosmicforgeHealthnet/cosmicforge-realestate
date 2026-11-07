package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type LoyaltyTier struct {
	Code      string `json:"code"`
	MinPoints int64  `json:"min_points"`
}

// LoyaltyProgramDefinition is a bounded policy, not free-form scoring.
type LoyaltyProgramDefinition struct {
	Tiers             []LoyaltyTier `json:"tiers"`
	PointsExpiryDays  int           `json:"points_expiry_days"`
	CorporateExcluded bool          `json:"corporate_excluded"`
	AllowedSources    []string      `json:"allowed_sources"`
}

type LoyaltyProgramCommand struct {
	PropertyID      ids.ID                   `json:"property_id"`
	ProgramKey      *ids.ID                  `json:"program_key,omitempty"`
	ExpectedVersion int64                    `json:"expected_version"`
	Name            string                   `json:"name"`
	State           string                   `json:"state"`
	Definition      LoyaltyProgramDefinition `json:"definition"`
	EffectiveAt     time.Time                `json:"effective_at"`
	ApprovalID      ids.ID                   `json:"approval_id,omitempty"`
	Reason          string                   `json:"reason"`
}

type LoyaltyRuleDefinition struct {
	PointsPerNight     int64 `json:"points_per_night"`
	PointsPerMajorUnit int64 `json:"points_per_major_unit"`
	BonusPoints        int64 `json:"bonus_points"`
	MinNights          int   `json:"min_nights"`
}

type LoyaltyRuleCommand struct {
	RuleKey         *ids.ID               `json:"rule_key,omitempty"`
	ExpectedVersion int64                 `json:"expected_version"`
	Kind            string                `json:"kind"`
	State           string                `json:"state"`
	Definition      LoyaltyRuleDefinition `json:"definition"`
	EffectiveAt     time.Time             `json:"effective_at"`
	ApprovalID      ids.ID                `json:"approval_id,omitempty"`
	Reason          string                `json:"reason"`
}

type LoyaltyRewardCommand struct {
	RewardKey       *ids.ID `json:"reward_key,omitempty"`
	ExpectedVersion int64   `json:"expected_version"`
	Name            string  `json:"name"`
	Kind            string  `json:"kind"`
	PointsCost      int64   `json:"points_cost"`
	ValidityDays    int     `json:"validity_days"`
	State           string  `json:"state"`
	Reason          string  `json:"reason"`
}

type LoyaltyEnrolCommand struct {
	ProgramKey ids.ID `json:"program_key"`
	GuestID    ids.ID `json:"guest_id"`
	Reason     string `json:"reason"`
}

type LoyaltyAccrualCommand struct {
	StayID ids.ID `json:"stay_id"`
	Reason string `json:"reason"`
}

type LoyaltyRedemptionCommand struct {
	RewardID         ids.ID `json:"reward_id"`
	RequestReference ids.ID `json:"request_reference"`
	Reason           string `json:"reason"`
}

type LoyaltyAccountView struct {
	ID             ids.ID          `json:"id"`
	PropertyID     ids.ID          `json:"property_id"`
	ProgramKey     ids.ID          `json:"program_key"`
	GuestID        ids.ID          `json:"guest_id"`
	State          string          `json:"state"`
	Tier           string          `json:"tier"`
	BalancePoints  int64           `json:"balance_points"`
	LifetimePoints int64           `json:"lifetime_points"`
	Version        int64           `json:"version"`
	Transactions   json.RawMessage `json:"transactions"`
	Entitlements   json.RawMessage `json:"entitlements"`
	PaymentWallet  bool            `json:"payment_wallet"`
}

func (h *Handler) registerLoyaltyCRM(r chi.Router) {
	r.Get("/loyalty/programs", h.loyaltyPrograms)
	r.Post("/loyalty/programs", h.loyaltyProgramSave)
	r.Post("/loyalty/programs/{key}/rules", h.loyaltyRuleSave)
	r.Post("/loyalty/programs/{key}/rewards", h.loyaltyRewardSave)
	r.Post("/loyalty/accounts", h.loyaltyEnrol)
	r.Get("/loyalty/accounts/{id}", h.loyaltyAccountGet)
	r.Post("/loyalty/accounts/{id}/accruals", h.loyaltyAccrue)
	r.Post("/loyalty/accounts/{id}/reconcile", h.loyaltyReconcile)
	r.Post("/loyalty/accounts/{id}/redemptions", h.loyaltyRedeem)
}

func validLoyaltyProgram(in LoyaltyProgramCommand) bool {
	d := in.Definition
	if in.PropertyID.ValidateV7() != nil || in.ProgramKey != nil && in.ProgramKey.ValidateV7() != nil || in.ExpectedVersion < 0 || (in.ProgramKey == nil) != (in.ExpectedVersion == 0) ||
		!crm.Text(in.Name, 1, 200) || !slices.Contains([]string{"ACTIVE", "SUSPENDED", "RETIRED"}, in.State) || in.EffectiveAt.IsZero() || !validStaffReason(in.Reason) ||
		len(d.Tiers) < 1 || len(d.Tiers) > 10 || d.PointsExpiryDays < 30 || d.PointsExpiryDays > 1825 || len(d.AllowedSources) < 1 || len(d.AllowedSources) > 6 {
		return false
	}
	seen := map[string]bool{}
	for i, t := range d.Tiers {
		if !crm.Text(t.Code, 1, 40) || seen[t.Code] || t.MinPoints < 0 || i == 0 && t.MinPoints != 0 || i > 0 && t.MinPoints <= d.Tiers[i-1].MinPoints {
			return false
		}
		seen[t.Code] = true
	}
	for _, s := range d.AllowedSources {
		if !slices.Contains([]string{"DIRECT", "MARKETPLACE", "OTA", "CORPORATE", "WALK_IN", "GROUP"}, s) {
			return false
		}
	}
	return true
}

func (h *Handler) loyaltyProgramSave(w http.ResponseWriter, r *http.Request) {
	var in LoyaltyProgramCommand
	if !decode(w, r, &in) {
		return
	}
	if !validLoyaltyProgram(in) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, bounded ascending tiers starting at zero, 30–1825 day expiry, supported qualifying sources and reason required; new programmes start at version 0 without a key."))
		return
	}
	h.execute(w, r, "crm:loyalty:program:"+in.PropertyID.String(), in, "loyalty:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "loyalty:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		key := ids.New()
		if in.ProgramKey != nil {
			key = *in.ProgramKey
		}
		var current int64
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM loyalty_programs WHERE organisation_id=$1 AND property_id=$2 AND program_key=$3`, t.Organisation, in.PropertyID, key).Scan(&current); e != nil {
			return 0, nil, e
		}
		if current != in.ExpectedVersion {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Loyalty programme changed.")
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_LOYALTY_PROGRAM", target.Scope, in.ApprovalID, map[string]any{"property_id": in.PropertyID, "command": op}, "", nil, ""); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO loyalty_programs(id,organisation_id,property_id,program_key,version,name,state,definition,effective_at,approval_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, t.Organisation, in.PropertyID, key, current+1, in.Name, in.State, marshalCommercial(in.Definition), in.EffectiveAt, in.ApprovalID, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		out := map[string]any{"id": id, "program_key": key, "version": current + 1, "state": in.State, "definition": in.Definition, "liability_kind": "LOYALTY_BENEFIT_NOT_MONEY"}
		if e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "loyalty_program", "LOYALTY_PROGRAM_SAVED", "loyalty.program.updated.v1", current+1, out); e != nil {
			return 0, nil, e
		}
		return 201, out, nil
	})
}

func (h *Handler) loyaltyPrograms(w http.ResponseWriter, r *http.Request) {
	property, e := ids.ParseV7(r.URL.Query().Get("property_id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A property scope is required."))
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "loyalty:read", "PROPERTY", property, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(p)||jsonb_build_object('rules',coalesce((SELECT jsonb_agg(to_jsonb(x) ORDER BY x.rule_key) FROM loyalty_rules x WHERE x.organisation_id=p.organisation_id AND x.program_key=p.program_key AND x.version=(SELECT max(version) FROM loyalty_rules y WHERE y.rule_key=x.rule_key)),'[]'::jsonb),'rewards',coalesce((SELECT jsonb_agg(to_jsonb(x) ORDER BY x.reward_key) FROM loyalty_rewards x WHERE x.organisation_id=p.organisation_id AND x.program_key=p.program_key AND x.version=(SELECT max(version) FROM loyalty_rewards y WHERE y.reward_key=x.reward_key)),'[]'::jsonb)) FROM loyalty_programs p WHERE p.organisation_id=$1 AND p.property_id=$2 AND p.version=(SELECT max(version) FROM loyalty_programs q WHERE q.program_key=p.program_key) ORDER BY p.created_at,p.id LIMIT 100`, t.Organisation, property)
	reply(w, r, 200, map[string]any{"items": items}, e)
}

type loyaltyProgram struct {
	ID          ids.ID                   `json:"id"`
	PropertyID  ids.ID                   `json:"property_id"`
	ProgramKey  ids.ID                   `json:"program_key"`
	Version     int64                    `json:"version"`
	State       string                   `json:"state"`
	Definition  LoyaltyProgramDefinition `json:"definition"`
	EffectiveAt time.Time                `json:"effective_at"`
}

func currentLoyaltyProgram(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, org, key ids.ID) (loyaltyProgram, error) {
	var out loyaltyProgram
	var raw []byte
	e := q.QueryRow(ctx, `SELECT to_jsonb(p) FROM loyalty_programs p WHERE organisation_id=$1 AND program_key=$2 ORDER BY version DESC LIMIT 1`, org, key).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, apperrors.NotFound("LOYALTY_PROGRAM_NOT_FOUND", "Loyalty programme not found.")
	}
	if e != nil {
		return out, e
	}
	return out, json.Unmarshal(raw, &out)
}

func (h *Handler) loyaltyProgramTarget(r *http.Request, capability string) (tenant, loyaltyProgram, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, loyaltyProgram{}, e
	}
	key, e := ids.ParseV7(chi.URLParam(r, "key"))
	if e != nil {
		return t, loyaltyProgram{}, apperrors.InvalidRequest("A UUIDv7 programme key is required.")
	}
	p, e := currentLoyaltyProgram(r.Context(), h.Auth.Service.Pool, t.Organisation, key)
	if e != nil {
		return t, p, e
	}
	t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", p.PropertyID, true)
	return t, p, e
}

func (h *Handler) loyaltyRuleSave(w http.ResponseWriter, r *http.Request) {
	var in LoyaltyRuleCommand
	if !decode(w, r, &in) {
		return
	}
	d := in.Definition
	valid := in.RuleKey == nil || in.RuleKey.ValidateV7() == nil
	valid = valid && in.ExpectedVersion >= 0 && (in.RuleKey == nil) == (in.ExpectedVersion == 0) && slices.Contains([]string{"ACTIVE", "RETIRED"}, in.State) && !in.EffectiveAt.IsZero() && validStaffReason(in.Reason) && d.MinNights >= 0 && d.MinNights <= 365
	switch in.Kind {
	case "EARN_PER_NIGHT":
		valid = valid && d.PointsPerNight >= 1 && d.PointsPerNight <= 10000 && d.PointsPerMajorUnit == 0 && d.BonusPoints == 0
	case "EARN_PER_SPEND":
		valid = valid && d.PointsPerMajorUnit >= 1 && d.PointsPerMajorUnit <= 1000 && d.PointsPerNight == 0 && d.BonusPoints == 0
	case "QUALIFYING_STAY_BONUS":
		valid = valid && d.BonusPoints >= 1 && d.BonusPoints <= 1000000 && d.PointsPerNight == 0 && d.PointsPerMajorUnit == 0
	default:
		valid = false
	}
	if !valid {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Supported earning kind with exactly its bounded parameter, state, effective time and reason required."))
		return
	}
	_, p, e := h.loyaltyProgramTarget(r, "loyalty:write")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:loyalty:rule:"+p.ProgramKey.String(), in, "loyalty:write", "PROPERTY", p.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "loyalty:write", "PROPERTY", p.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		key := ids.New()
		if in.RuleKey != nil {
			key = *in.RuleKey
		}
		var current int64
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM loyalty_rules WHERE organisation_id=$1 AND program_key=$2 AND rule_key=$3`, t.Organisation, p.ProgramKey, key).Scan(&current); e != nil {
			return 0, nil, e
		}
		if current != in.ExpectedVersion {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Loyalty rule changed.")
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_LOYALTY_PROGRAM", target.Scope, in.ApprovalID, map[string]any{"program_key": p.ProgramKey, "command": op}, "", nil, ""); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO loyalty_rules(id,organisation_id,property_id,program_key,rule_key,version,kind,state,definition,effective_at,approval_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, id, t.Organisation, p.PropertyID, p.ProgramKey, key, current+1, in.Kind, in.State, marshalCommercial(d), in.EffectiveAt, in.ApprovalID, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		out := map[string]any{"id": id, "program_key": p.ProgramKey, "rule_key": key, "version": current + 1, "kind": in.Kind, "state": in.State}
		if e = h.commercialCommit(ctx, tx, t, p.PropertyID, id, "loyalty_rule", "LOYALTY_RULE_SAVED", "loyalty.program.updated.v1", current+1, out); e != nil {
			return 0, nil, e
		}
		return 201, out, nil
	})
}

func (h *Handler) loyaltyRewardSave(w http.ResponseWriter, r *http.Request) {
	var in LoyaltyRewardCommand
	if !decode(w, r, &in) {
		return
	}
	if in.RewardKey != nil && in.RewardKey.ValidateV7() != nil || in.ExpectedVersion < 0 || (in.RewardKey == nil) != (in.ExpectedVersion == 0) || !crm.Text(in.Name, 1, 200) ||
		!slices.Contains([]string{"COMPLIMENTARY_SERVICE", "LATE_CHECKOUT", "UPGRADE_ELIGIBILITY", "FUTURE_STAY_BENEFIT"}, in.Kind) || in.PointsCost < 1 || in.PointsCost > 100000000 || in.ValidityDays < 1 || in.ValidityDays > 730 ||
		!slices.Contains([]string{"ACTIVE", "RETIRED"}, in.State) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Supported non-cash reward, bounded points cost and validity, state and reason required."))
		return
	}
	_, p, e := h.loyaltyProgramTarget(r, "loyalty:write")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:loyalty:reward:"+p.ProgramKey.String(), in, "loyalty:write", "PROPERTY", p.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "loyalty:write", "PROPERTY", p.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		key := ids.New()
		if in.RewardKey != nil {
			key = *in.RewardKey
		}
		var current int64
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM loyalty_rewards WHERE organisation_id=$1 AND program_key=$2 AND reward_key=$3`, t.Organisation, p.ProgramKey, key).Scan(&current); e != nil {
			return 0, nil, e
		}
		if current != in.ExpectedVersion {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Loyalty reward changed.")
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO loyalty_rewards(id,organisation_id,property_id,program_key,reward_key,version,name,kind,points_cost,state,validity_days,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, id, t.Organisation, p.PropertyID, p.ProgramKey, key, current+1, in.Name, in.Kind, in.PointsCost, in.State, in.ValidityDays, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		out := map[string]any{"id": id, "program_key": p.ProgramKey, "reward_key": key, "version": current + 1, "kind": in.Kind, "points_cost": in.PointsCost, "state": in.State}
		if e = h.commercialCommit(ctx, tx, t, p.PropertyID, id, "loyalty_reward", "LOYALTY_REWARD_SAVED", "loyalty.program.updated.v1", current+1, out); e != nil {
			return 0, nil, e
		}
		return 201, out, nil
	})
}

func (h *Handler) loyaltyEnrol(w http.ResponseWriter, r *http.Request) {
	var in LoyaltyEnrolCommand
	if !decode(w, r, &in) {
		return
	}
	if in.ProgramKey.ValidateV7() != nil || in.GuestID.ValidateV7() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Programme key, guest and reason required."))
		return
	}
	t0, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	p, e := currentLoyaltyProgram(r.Context(), h.Auth.Service.Pool, t0.Organisation, in.ProgramKey)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:loyalty:enrol:"+in.ProgramKey.String()+":"+in.GuestID.String(), in, "loyalty:write", "PROPERTY", p.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "loyalty:write", "PROPERTY", p.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		p, e := currentLoyaltyProgram(ctx, tx, t.Organisation, in.ProgramKey)
		if e != nil {
			return 0, nil, e
		}
		if p.State != "ACTIVE" || p.EffectiveAt.After(now) {
			return 0, nil, apperrors.Conflict("LOYALTY_PROGRAM_INACTIVE", "Enrolment requires a current active programme.")
		}
		// Enrolment needs the guest's current explicit LOYALTY consent in scope.
		var consent ids.ID
		e = tx.QueryRow(ctx, `SELECT c.id FROM consents c JOIN guest_property_scopes s ON s.organisation_id=c.organisation_id AND s.guest_id=c.guest_id AND s.property_id=c.property_id AND s.state='ACTIVE' JOIN guest_profiles g ON g.organisation_id=c.organisation_id AND g.id=c.guest_id AND g.state='ACTIVE' WHERE c.organisation_id=$1 AND c.guest_id=$2 AND c.property_id=$3 AND c.purpose='LOYALTY' AND c.version=(SELECT max(version) FROM consents x WHERE x.guest_id=c.guest_id AND x.property_id=c.property_id AND x.purpose='LOYALTY') AND c.state='GRANTED' AND (c.expires_at IS NULL OR c.expires_at>$4)`, t.Organisation, in.GuestID, p.PropertyID, now).Scan(&consent)
		if errors.Is(e, pgx.ErrNoRows) {
			return 0, nil, apperrors.PermissionDenied("Loyalty enrolment requires the guest's current explicit LOYALTY consent in this property.")
		}
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		tier := p.Definition.Tiers[0].Code
		if _, e = tx.Exec(ctx, `INSERT INTO loyalty_accounts(id,organisation_id,property_id,program_key,guest_id,state,tier,consent_id,created_by) VALUES($1,$2,$3,$4,$5,'ACTIVE',$6,$7,$8)`, id, t.Organisation, p.PropertyID, p.ProgramKey, in.GuestID, tier, consent, t.Account); e != nil {
			if database.SQLState(e) == "23505" {
				return 0, nil, apperrors.Conflict("LOYALTY_ALREADY_ENROLLED", "The guest already has an account in this programme.")
			}
			return 0, nil, e
		}
		out := map[string]any{"id": id, "program_key": p.ProgramKey, "guest_id": in.GuestID, "state": "ACTIVE", "tier": tier}
		if e = h.commercialCommit(ctx, tx, t, p.PropertyID, id, "loyalty_account", "LOYALTY_ENROLLED", "loyalty.account.updated.v1", 1, out); e != nil {
			return 0, nil, e
		}
		return 201, out, nil
	})
}

type loyaltyAccount struct {
	ID         ids.ID `json:"id"`
	PropertyID ids.ID `json:"property_id"`
	ProgramKey ids.ID `json:"program_key"`
	GuestID    ids.ID `json:"guest_id"`
	State      string `json:"state"`
	Tier       string `json:"tier"`
	Version    int64  `json:"version"`
}

func loadLoyaltyAccount(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, org, id ids.ID, lock bool) (loyaltyAccount, error) {
	var out loyaltyAccount
	var raw []byte
	query := `SELECT to_jsonb(a) FROM loyalty_accounts a WHERE organisation_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	e := q.QueryRow(ctx, query, org, id).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, apperrors.NotFound("LOYALTY_ACCOUNT_NOT_FOUND", "Loyalty account not found.")
	}
	if e != nil {
		return out, e
	}
	return out, json.Unmarshal(raw, &out)
}

func (h *Handler) loyaltyAccountTarget(r *http.Request, capability string) (tenant, loyaltyAccount, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, loyaltyAccount{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, loyaltyAccount{}, e
	}
	a, e := loadLoyaltyAccount(r.Context(), h.Auth.Service.Pool, t.Organisation, id, false)
	if e != nil {
		return t, a, e
	}
	t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", a.PropertyID, false)
	return t, a, e
}

func loyaltyBalances(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, org, account ids.ID) (balance, lifetime int64, e error) {
	e = q.QueryRow(ctx, `SELECT coalesce(sum(points),0)::bigint,coalesce(sum(points) FILTER (WHERE kind IN ('ACCRUAL','RECOVERY_BENEFIT')),0)::bigint-coalesce((SELECT sum(o.points) FROM loyalty_reversals v JOIN loyalty_transactions o ON o.organisation_id=v.organisation_id AND o.id=v.original_transaction_id WHERE v.organisation_id=$1 AND v.account_id=$2 AND o.kind IN ('ACCRUAL','RECOVERY_BENEFIT')),0)::bigint FROM loyalty_transactions WHERE organisation_id=$1 AND account_id=$2`, org, account).Scan(&balance, &lifetime)
	return
}

func (h *Handler) loyaltyAccountGet(w http.ResponseWriter, r *http.Request) {
	t, a, e := h.loyaltyAccountTarget(r, "loyalty:read")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	view, e := h.loyaltyView(r.Context(), h.Auth.Service.Pool, t.Organisation, a)
	reply(w, r, 200, view, e)
}

func (h *Handler) loyaltyView(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}, org ids.ID, a loyaltyAccount) (LoyaltyAccountView, error) {
	out := LoyaltyAccountView{ID: a.ID, PropertyID: a.PropertyID, ProgramKey: a.ProgramKey, GuestID: a.GuestID, State: a.State, Tier: a.Tier, Version: a.Version}
	var e error
	if out.BalancePoints, out.LifetimePoints, e = loyaltyBalances(ctx, q, org, a.ID); e != nil {
		return out, e
	}
	if e = q.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.created_at,x.id),'[]'::jsonb) FROM (SELECT * FROM loyalty_transactions WHERE organisation_id=$1 AND account_id=$2 ORDER BY created_at DESC,id DESC LIMIT 200) x`, org, a.ID).Scan(&out.Transactions); e != nil {
		return out, e
	}
	e = q.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.created_at,x.id),'[]'::jsonb) FROM loyalty_entitlements x WHERE organisation_id=$1 AND account_id=$2`, org, a.ID).Scan(&out.Entitlements)
	return out, e
}

// loyaltyStayPoints computes points from the current active rules for an
// actual completed stay. Corporate and non-allowed sources earn nothing.
func loyaltyStayPoints(ctx context.Context, tx pgx.Tx, org ids.ID, p loyaltyProgram, a loyaltyAccount, stay ids.ID, now time.Time) (int64, []ids.ID, map[string]any, error) {
	var fact struct {
		Nights      int     `json:"nights"`
		Source      string  `json:"source"`
		Corporate   bool    `json:"corporate"`
		SpendMinor  int64   `json:"spend_minor"`
		Currency    string  `json:"currency"`
		Reservation ids.ID  `json:"reservation_id"`
		FolioState  *string `json:"folio_state"`
		Balance     *int64  `json:"balance_minor"`
	}
	var raw []byte
	e := tx.QueryRow(ctx, `SELECT jsonb_build_object('nights',(r.date_to-r.date_from),'source',r.source,'corporate',r.corporate_id IS NOT NULL,'reservation_id',r.id,'currency',r.currency,'folio_state',f.state,'spend_minor',coalesce((SELECT sum(i.amount_minor) FROM folio_items i WHERE i.organisation_id=f.organisation_id AND i.folio_id=f.id),0)) FROM stay_records s JOIN reservations r ON r.organisation_id=s.organisation_id AND r.id=s.reservation_id LEFT JOIN folios f ON f.organisation_id=s.organisation_id AND f.stay_id=s.id WHERE s.organisation_id=$1 AND s.id=$2 AND s.guest_id=$3 AND s.state='DEPARTED' AND r.state='COMPLETED' AND r.property_id=$4`, org, stay, a.GuestID, p.PropertyID).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return 0, nil, nil, apperrors.Conflict("LOYALTY_STAY_NOT_QUALIFYING", "Accrual requires an actual departed, canonically completed stay of the enrolled guest in the programme property.")
	}
	if e != nil {
		return 0, nil, nil, e
	}
	if e = json.Unmarshal(raw, &fact); e != nil {
		return 0, nil, nil, e
	}
	evidence := map[string]any{"stay_id": stay, "reservation_id": fact.Reservation, "nights": fact.Nights, "source": fact.Source, "corporate": fact.Corporate, "folio_state": fact.FolioState, "spend_minor": fact.SpendMinor, "currency": fact.Currency}
	if fact.Corporate && p.Definition.CorporateExcluded || !slices.Contains(p.Definition.AllowedSources, fact.Source) {
		return 0, nil, evidence, nil
	}
	if fact.FolioState == nil || *fact.FolioState != "CLOSED" {
		return 0, nil, nil, apperrors.Conflict("LOYALTY_FINANCIAL_EVIDENCE_REQUIRED", "Accrual requires the stay's folio to be closed against settled Payment evidence.")
	}
	rows, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(x) FROM loyalty_rules x WHERE x.organisation_id=$1 AND x.program_key=$2 AND x.version=(SELECT max(version) FROM loyalty_rules y WHERE y.rule_key=x.rule_key) AND x.state='ACTIVE' AND x.effective_at<=$3 ORDER BY x.rule_key`, org, p.ProgramKey, now)
	if e != nil {
		return 0, nil, nil, e
	}
	var points int64
	var used []ids.ID
	for _, raw := range rows {
		var rule struct {
			ID         ids.ID                `json:"id"`
			Kind       string                `json:"kind"`
			Definition LoyaltyRuleDefinition `json:"definition"`
		}
		if e = json.Unmarshal(raw, &rule); e != nil {
			return 0, nil, nil, e
		}
		if fact.Nights < rule.Definition.MinNights {
			continue
		}
		switch rule.Kind {
		case "EARN_PER_NIGHT":
			points += int64(fact.Nights) * rule.Definition.PointsPerNight
		case "EARN_PER_SPEND":
			points += fact.SpendMinor / 100 * rule.Definition.PointsPerMajorUnit
		case "QUALIFYING_STAY_BONUS":
			points += rule.Definition.BonusPoints
		}
		used = append(used, rule.ID)
	}
	return min(points, 100000000), used, evidence, nil
}

func (h *Handler) loyaltyAccrue(w http.ResponseWriter, r *http.Request) {
	var in LoyaltyAccrualCommand
	if !decode(w, r, &in) {
		return
	}
	if in.StayID.ValidateV7() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Actual stay and reason required."))
		return
	}
	_, pre, e := h.loyaltyAccountTarget(r, "loyalty:write")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:loyalty:accrue:"+pre.ID.String()+":"+in.StayID.String(), in, "loyalty:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "loyalty:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		return h.executeLoyaltyAccrual(ctx, tx, t, pre.ID, in.StayID)
	})
}

// executeLoyaltyAccrual awards a stay at most once per account. A duplicate
// command or replayed event returns the original entry without new points.
func (h *Handler) executeLoyaltyAccrual(ctx context.Context, tx pgx.Tx, t tenant, accountID, stay ids.ID) (int, any, error) {
	now := h.Auth.Service.Now().UTC()
	a, e := loadLoyaltyAccount(ctx, tx, t.Organisation, accountID, true)
	if e != nil {
		return 0, nil, e
	}
	var existing ids.ID
	if e = tx.QueryRow(ctx, `SELECT id FROM loyalty_transactions WHERE organisation_id=$1 AND account_id=$2 AND kind='ACCRUAL' AND source_kind='STAY' AND source_id=$3`, t.Organisation, a.ID, stay).Scan(&existing); e == nil {
		return 200, map[string]any{"transaction_id": existing, "duplicate": true, "points": nil}, nil
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return 0, nil, e
	}
	if a.State != "ACTIVE" {
		return 0, nil, apperrors.Conflict("LOYALTY_ACCOUNT_RESTRICTED", "Only an active account accrues; review restrictions first.")
	}
	p, e := currentLoyaltyProgram(ctx, tx, t.Organisation, a.ProgramKey)
	if e != nil {
		return 0, nil, e
	}
	if p.State != "ACTIVE" {
		return 0, nil, apperrors.Conflict("LOYALTY_PROGRAM_INACTIVE", "Accrual requires a current active programme.")
	}
	points, rules, evidence, e := loyaltyStayPoints(ctx, tx, t.Organisation, p, a, stay, now)
	if e != nil {
		return 0, nil, e
	}
	if points <= 0 {
		return 200, map[string]any{"transaction_id": nil, "duplicate": false, "points": 0, "evidence": evidence, "earned": false}, nil
	}
	id := ids.New()
	digest := hashCommercialPayload(marshalCommercial(evidence))
	expires := now.AddDate(0, 0, p.Definition.PointsExpiryDays)
	if _, e = tx.Exec(ctx, `INSERT INTO loyalty_transactions(id,organisation_id,property_id,account_id,kind,points,source_kind,source_id,source_digest,rule_ids,program_version,expires_at,evidence,created_by) VALUES($1,$2,$3,$4,'ACCRUAL',$5,'STAY',$6,$7,$8,$9,$10,$11,$12)`, id, t.Organisation, a.PropertyID, a.ID, points, stay, digest, rules, p.Version, expires, marshalCommercial(evidence), t.Account); e != nil {
		return 0, nil, e
	}
	if e = h.loyaltyRetier(ctx, tx, t, a, p); e != nil {
		return 0, nil, e
	}
	if e = h.commercialCommit(ctx, tx, t, a.PropertyID, id, "loyalty_transaction", "LOYALTY_ACCRUED", "loyalty.transaction.recorded.v1", 1, map[string]any{"account_id": a.ID, "kind": "ACCRUAL", "points": points, "stay_id": stay}); e != nil {
		return 0, nil, e
	}
	return 201, map[string]any{"transaction_id": id, "duplicate": false, "points": points, "evidence": evidence, "earned": true}, nil
}

// loyaltyRetier moves the account to the highest tier its net lifetime
// qualifying points reach; reversals can lower the tier.
func (h *Handler) loyaltyRetier(ctx context.Context, tx pgx.Tx, t tenant, a loyaltyAccount, p loyaltyProgram) error {
	_, lifetime, e := loyaltyBalances(ctx, tx, t.Organisation, a.ID)
	if e != nil {
		return e
	}
	tiers := append([]LoyaltyTier(nil), p.Definition.Tiers...)
	sort.Slice(tiers, func(i, j int) bool { return tiers[i].MinPoints < tiers[j].MinPoints })
	tier := tiers[0].Code
	for _, x := range tiers {
		if lifetime >= x.MinPoints {
			tier = x.Code
		}
	}
	if tier == a.Tier {
		return nil
	}
	_, e = tx.Exec(ctx, `UPDATE loyalty_accounts SET tier=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, a.ID, tier)
	return e
}

func (h *Handler) loyaltyReconcile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Reconciliation reason required."))
		return
	}
	_, pre, e := h.loyaltyAccountTarget(r, "loyalty:write")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:loyalty:reconcile:"+pre.ID.String(), in, "loyalty:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "loyalty:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		return h.executeLoyaltyReconcile(ctx, tx, t, pre.ID)
	})
}

// executeLoyaltyReconcile reverses earnings whose stay is no longer completed
// or whose folio lost settled backing, and retires expired earnings.
func (h *Handler) executeLoyaltyReconcile(ctx context.Context, tx pgx.Tx, t tenant, accountID ids.ID) (int, any, error) {
	now := h.Auth.Service.Now().UTC()
	a, e := loadLoyaltyAccount(ctx, tx, t.Organisation, accountID, true)
	if e != nil {
		return 0, nil, e
	}
	rows, e := commercialJSONRows(ctx, tx, `SELECT jsonb_build_object('id',x.id,'points',x.points,'stay_id',x.source_id,'reservation_state',r.state,'folio_state',f.state,'expired',x.expires_at<=$3,'expiry_recorded',EXISTS(SELECT 1 FROM loyalty_transactions e WHERE e.organisation_id=x.organisation_id AND e.account_id=x.account_id AND e.kind='EXPIRY' AND e.source_id=x.id)) FROM loyalty_transactions x LEFT JOIN stay_records s ON s.organisation_id=x.organisation_id AND s.id=x.source_id LEFT JOIN reservations r ON r.organisation_id=s.organisation_id AND r.id=s.reservation_id LEFT JOIN folios f ON f.organisation_id=s.organisation_id AND f.stay_id=s.id WHERE x.organisation_id=$1 AND x.account_id=$2 AND x.kind='ACCRUAL' AND NOT EXISTS(SELECT 1 FROM loyalty_reversals v WHERE v.organisation_id=x.organisation_id AND v.original_transaction_id=x.id) ORDER BY x.created_at,x.id LIMIT 500`, t.Organisation, a.ID, now)
	if e != nil {
		return 0, nil, e
	}
	var reversed, expired []ids.ID
	for _, raw := range rows {
		var x struct {
			ID               ids.ID  `json:"id"`
			Points           int64   `json:"points"`
			StayID           ids.ID  `json:"stay_id"`
			ReservationState *string `json:"reservation_state"`
			FolioState       *string `json:"folio_state"`
			Expired          bool    `json:"expired"`
			ExpiryRecorded   bool    `json:"expiry_recorded"`
		}
		if e = json.Unmarshal(raw, &x); e != nil {
			return 0, nil, e
		}
		reason := ""
		switch {
		case x.ReservationState == nil || *x.ReservationState != "COMPLETED":
			reason = "STAY_NO_LONGER_QUALIFYING"
		case x.FolioState == nil || *x.FolioState != "CLOSED":
			reason = "FINANCIAL_BACKING_REVERSED"
		}
		if reason != "" && !x.ExpiryRecorded {
			rid := ids.New()
			evidence := map[string]any{"original_transaction_id": x.ID, "stay_id": x.StayID, "reservation_state": x.ReservationState, "folio_state": x.FolioState, "reason_code": reason}
			digest := hashCommercialPayload(marshalCommercial(evidence))
			if _, e = tx.Exec(ctx, `INSERT INTO loyalty_transactions(id,organisation_id,property_id,account_id,kind,points,source_kind,source_id,source_digest,evidence,created_by) VALUES($1,$2,$3,$4,'REVERSAL',$5,'TRANSACTION',$6,$7,$8,$9)`, rid, t.Organisation, a.PropertyID, a.ID, -x.Points, x.ID, digest, marshalCommercial(evidence), t.Account); e != nil {
				return 0, nil, e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO loyalty_reversals(id,organisation_id,property_id,account_id,original_transaction_id,reversal_transaction_id,reason_code,source_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ids.New(), t.Organisation, a.PropertyID, a.ID, x.ID, rid, reason, digest); e != nil {
				return 0, nil, e
			}
			if e = h.commercialCommit(ctx, tx, t, a.PropertyID, rid, "loyalty_transaction", "LOYALTY_REVERSED", "loyalty.transaction.recorded.v1", 1, map[string]any{"account_id": a.ID, "kind": "REVERSAL", "points": -x.Points, "original_transaction_id": x.ID, "reason_code": reason}); e != nil {
				return 0, nil, e
			}
			reversed = append(reversed, x.ID)
			continue
		}
		if x.Expired && !x.ExpiryRecorded {
			balance, _, e := loyaltyBalances(ctx, tx, t.Organisation, a.ID)
			if e != nil {
				return 0, nil, e
			}
			if retire := min(x.Points, balance); retire > 0 {
				evidence := map[string]any{"original_transaction_id": x.ID, "expired_points": retire}
				if _, e = tx.Exec(ctx, `INSERT INTO loyalty_transactions(id,organisation_id,property_id,account_id,kind,points,source_kind,source_id,source_digest,evidence,created_by) VALUES($1,$2,$3,$4,'EXPIRY',$5,'EXPIRY',$6,$7,$8,$9)`, ids.New(), t.Organisation, a.PropertyID, a.ID, -retire, x.ID, hashCommercialPayload(marshalCommercial(evidence)), marshalCommercial(evidence), t.Account); e != nil {
					return 0, nil, e
				}
				expired = append(expired, x.ID)
			}
		}
	}
	balance, _, e := loyaltyBalances(ctx, tx, t.Organisation, a.ID)
	if e != nil {
		return 0, nil, e
	}
	// Reversals after redemption can leave a negative balance; the account is
	// restricted for review rather than silently clawing back entitlements.
	if balance < 0 && a.State == "ACTIVE" {
		if _, e = tx.Exec(ctx, `UPDATE loyalty_accounts SET state='REVIEW_REQUIRED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, a.ID); e != nil {
			return 0, nil, e
		}
	}
	if len(reversed) > 0 {
		p, e := currentLoyaltyProgram(ctx, tx, t.Organisation, a.ProgramKey)
		if e != nil {
			return 0, nil, e
		}
		if a, e = loadLoyaltyAccount(ctx, tx, t.Organisation, a.ID, false); e != nil {
			return 0, nil, e
		}
		if e = h.loyaltyRetier(ctx, tx, t, a, p); e != nil {
			return 0, nil, e
		}
	}
	return 200, map[string]any{"account_id": a.ID, "reversed_transaction_ids": reversed, "expired_transaction_ids": expired, "balance_points": balance}, nil
}

func (h *Handler) loyaltyRedeem(w http.ResponseWriter, r *http.Request) {
	var in LoyaltyRedemptionCommand
	if !decode(w, r, &in) {
		return
	}
	if in.RewardID.ValidateV7() != nil || in.RequestReference.ValidateV7() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Reward, UUIDv7 request reference and reason required."))
		return
	}
	_, pre, e := h.loyaltyAccountTarget(r, "loyalty:write")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:loyalty:redeem:"+pre.ID.String()+":"+in.RequestReference.String(), in, "loyalty:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "loyalty:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		a, e := loadLoyaltyAccount(ctx, tx, t.Organisation, pre.ID, true)
		if e != nil {
			return 0, nil, e
		}
		var prior ids.ID
		if e = tx.QueryRow(ctx, `SELECT id FROM loyalty_redemptions WHERE organisation_id=$1 AND account_id=$2 AND request_reference=$3`, t.Organisation, a.ID, in.RequestReference).Scan(&prior); e == nil {
			return 200, map[string]any{"redemption_id": prior, "duplicate": true}, nil
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return 0, nil, e
		}
		var reward struct {
			ID           ids.ID `json:"id"`
			PointsCost   int64  `json:"points_cost"`
			ValidityDays int    `json:"validity_days"`
		}
		var raw []byte
		if e = tx.QueryRow(ctx, `SELECT to_jsonb(x) FROM loyalty_rewards x WHERE x.organisation_id=$1 AND x.id=$2 AND x.program_key=$3 AND x.state='ACTIVE' AND x.version=(SELECT max(version) FROM loyalty_rewards y WHERE y.reward_key=x.reward_key)`, t.Organisation, in.RewardID, a.ProgramKey).Scan(&raw); errors.Is(e, pgx.ErrNoRows) {
			return 0, nil, apperrors.Conflict("LOYALTY_REWARD_UNAVAILABLE", "Only the current active reward version of this programme can be redeemed.")
		} else if e != nil {
			return 0, nil, e
		}
		if e = json.Unmarshal(raw, &reward); e != nil {
			return 0, nil, e
		}
		tid, rid := ids.New(), ids.New()
		evidence := map[string]any{"reward_id": reward.ID, "request_reference": in.RequestReference}
		if _, e = tx.Exec(ctx, `SAVEPOINT loyalty_redeem`); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO loyalty_transactions(id,organisation_id,property_id,account_id,kind,points,source_kind,source_id,source_digest,evidence,created_by) VALUES($1,$2,$3,$4,'REDEMPTION',$5,'REDEMPTION',$6,$7,$8,$9)`, tid, t.Organisation, a.PropertyID, a.ID, -reward.PointsCost, rid, hashCommercialPayload(marshalCommercial(evidence)), marshalCommercial(evidence), t.Account); e != nil {
			if _, re := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT loyalty_redeem`); re != nil {
				return 0, nil, re
			}
			return 0, nil, apperrors.Conflict("LOYALTY_INSUFFICIENT_POINTS", "Redemption requires an active account with sufficient points.")
		}
		if _, e = tx.Exec(ctx, `INSERT INTO loyalty_redemptions(id,organisation_id,property_id,account_id,reward_id,transaction_id,points,request_reference,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, rid, t.Organisation, a.PropertyID, a.ID, reward.ID, tid, reward.PointsCost, in.RequestReference, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		entitlement := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO loyalty_entitlements(id,organisation_id,property_id,account_id,redemption_id,reward_id,state,valid_until) VALUES($1,$2,$3,$4,$5,$6,'AVAILABLE',$7)`, entitlement, t.Organisation, a.PropertyID, a.ID, rid, reward.ID, now.AddDate(0, 0, reward.ValidityDays)); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, a.PropertyID, tid, "loyalty_transaction", "LOYALTY_REDEEMED", "loyalty.transaction.recorded.v1", 1, map[string]any{"account_id": a.ID, "kind": "REDEMPTION", "points": -reward.PointsCost, "redemption_id": rid}); e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"redemption_id": rid, "transaction_id": tid, "entitlement_id": entitlement, "points": reward.PointsCost, "duplicate": false}, nil
	})
}

// recoveryLoyaltyBenefit posts the exact approved points of a recovery
// compensation to the guest's account in a programme of the case property.
func (h *Handler) recoveryLoyaltyBenefit(ctx context.Context, tx pgx.Tx, t tenant, c CRMRecoveryCase, compensation ids.ID, points int64, digest string) (ids.ID, error) {
	var account ids.ID
	e := tx.QueryRow(ctx, `SELECT id FROM loyalty_accounts WHERE organisation_id=$1 AND property_id=$2 AND guest_id=$3 AND state='ACTIVE' ORDER BY created_at,id LIMIT 1 FOR UPDATE`, t.Organisation, c.PropertyID, c.GuestID).Scan(&account)
	if errors.Is(e, pgx.ErrNoRows) {
		return ids.Nil, apperrors.Conflict("LOYALTY_ACCOUNT_REQUIRED", "A loyalty benefit needs the guest's active consented account in this property.")
	}
	if e != nil {
		return ids.Nil, e
	}
	id := ids.New()
	evidence := map[string]any{"case_id": c.ID, "compensation_id": compensation, "remedy_digest": digest}
	if _, e = tx.Exec(ctx, `INSERT INTO loyalty_transactions(id,organisation_id,property_id,account_id,kind,points,source_kind,source_id,source_digest,evidence,created_by) VALUES($1,$2,$3,$4,'RECOVERY_BENEFIT',$5,'RECOVERY_COMPENSATION',$6,$7,$8,$9)`, id, t.Organisation, c.PropertyID, account, points, compensation, digest, marshalCommercial(evidence), t.Account); e != nil {
		return ids.Nil, e
	}
	return id, h.commercialCommit(ctx, tx, t, c.PropertyID, id, "loyalty_transaction", "LOYALTY_RECOVERY_BENEFIT", "loyalty.transaction.recorded.v1", 1, map[string]any{"account_id": account, "kind": "RECOVERY_BENEFIT", "points": points, "case_id": c.ID})
}
