package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// WaitlistAutomationPolicyCommand appends a policy version. AUTOMATIC_OFFERS
// needs recent MFA and an exact independent approval; MANUAL pauses
// automation immediately without approval.
type WaitlistAutomationPolicyCommand struct {
	PropertyID        ids.ID    `json:"property_id"`
	Mode              string    `json:"mode"`
	Channel           string    `json:"channel,omitempty"`
	OfferSeconds      int       `json:"offer_seconds"`
	MaxOffersPerEntry int       `json:"max_offers_per_entry"`
	MaxOffersPerRun   int       `json:"max_offers_per_run"`
	EffectiveAt       time.Time `json:"effective_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	ApprovalID        ids.ID    `json:"approval_id,omitempty"`
	Reason            string    `json:"reason"`
}
type WaitlistAutomationPolicy struct {
	ID                      ids.ID    `json:"id"`
	OrganisationID          ids.ID    `json:"organisation_id"`
	PropertyID              ids.ID    `json:"property_id"`
	Version                 int64     `json:"version"`
	Mode                    string    `json:"mode"`
	Channel                 string    `json:"channel"`
	OfferSeconds            int       `json:"offer_seconds"`
	MaxOffersPerEntry       int       `json:"max_offers_per_entry"`
	MaxOffersPerRun         int       `json:"max_offers_per_run"`
	ApprovalID              *ids.ID   `json:"approval_id"`
	AuthorizingMembershipID ids.ID    `json:"authorizing_membership_id"`
	AuthorizingRoleID       ids.ID    `json:"authorizing_role_id"`
	EffectiveAt             time.Time `json:"effective_at"`
	ExpiresAt               time.Time `json:"expires_at"`
	Reason                  string    `json:"reason"`
	CreatedBy               ids.ID    `json:"created_by"`
	CreatedAt               time.Time `json:"created_at"`
}
type WaitlistAutomationRun struct {
	ID               ids.ID          `json:"id"`
	OrganisationID   ids.ID          `json:"organisation_id"`
	PropertyID       ids.ID          `json:"property_id"`
	PolicyID         *ids.ID         `json:"policy_id"`
	PolicyVersion    *int64          `json:"policy_version"`
	LeaseOwner       string          `json:"lease_owner"`
	LeaseGeneration  int64           `json:"lease_generation"`
	Trigger          string          `json:"trigger"`
	State            string          `json:"state"`
	OffersCreated    int             `json:"offers_created"`
	SignalsProcessed int             `json:"signals_processed"`
	Evaluations      json.RawMessage `json:"evaluations"`
	CreatedByService string          `json:"created_by_service"`
	CreatedAt        time.Time       `json:"created_at"`
}
type WaitlistAutomationPolicyList struct {
	Items  []WaitlistAutomationPolicy `json:"items"`
	Limit  int                        `json:"limit"`
	Offset int                        `json:"offset"`
}
type WaitlistAutomationRunList struct {
	Items          []WaitlistAutomationRun `json:"items"`
	PendingSignals int                     `json:"pending_signals"`
	LeaseOwner     *string                 `json:"lease_owner"`
	LeaseExpiresAt *time.Time              `json:"lease_expires_at"`
	Limit          int                     `json:"limit"`
	Offset         int                     `json:"offset"`
}
type WaitlistAutomationTrigger struct {
	PropertyID ids.ID `json:"property_id"`
	Reason     string `json:"reason"`
}
type WaitlistAutomationTriggered struct {
	PropertyID  ids.ID    `json:"property_id"`
	RequestedAt time.Time `json:"requested_at"`
	PolicyMode  string    `json:"policy_mode"`
	Executed    bool      `json:"executed"`
}

// ErrWaitlistLeaseLost fences a worker whose lease expired or was reclaimed.
var ErrWaitlistLeaseLost = errors.New("waitlist automation lease lost")

func (h *Handler) registerReservationWaitlistAutomation(r chi.Router) {
	r.Get("/waitlist-automation-policies", h.reservationWaitlistAutomationPolicies)
	r.Post("/waitlist-automation-policies", h.reservationWaitlistAutomationPolicySave)
	r.Get("/waitlist-automation-runs", h.reservationWaitlistAutomationRuns)
	r.Post("/waitlist-automation-runs", h.reservationWaitlistAutomationTrigger)
}

func (h *Handler) reservationWaitlistAutomationPolicySave(w http.ResponseWriter, r *http.Request) {
	var in WaitlistAutomationPolicyCommand
	if !decode(w, r, &in) {
		return
	}
	now := h.Auth.Service.Now().UTC()
	if in.PropertyID.ValidateV7() != nil || in.Mode != "AUTOMATIC_OFFERS" && in.Mode != "MANUAL" || !inventory.ValidChannel(in.Channel) || in.OfferSeconds < 30 || in.OfferSeconds > 300 || in.MaxOffersPerEntry < 1 || in.MaxOffersPerEntry > 10 || in.MaxOffersPerRun < 1 || in.MaxOffersPerRun > 50 || in.EffectiveAt.IsZero() || !in.ExpiresAt.After(in.EffectiveAt) || in.ExpiresAt.After(in.EffectiveAt.AddDate(0, 0, 366)) || !in.ExpiresAt.After(now) || !validStaffReason(in.Reason) || in.Mode == "MANUAL" && !in.ApprovalID.IsZero() || !in.ApprovalID.IsZero() && in.ApprovalID.ValidateV7() != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, mode, channel, bounded offer window/limits, future validity of at most 366 days and a reason are required."))
		return
	}
	h.execute(w, r, "reservation:waitlist-automation:"+in.PropertyID.String(), in, "reservations:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "reservations:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		var approval any
		if in.Mode == "AUTOMATIC_OFFERS" {
			if e = h.commercialGate(ctx, tx, t, in.PropertyID); e != nil {
				return 0, nil, e
			}
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESERVATION_POLICY_CHANGE", target.Scope, in.ApprovalID, map[string]any{"waitlist_automation": op}, "", nil, ""); e != nil {
				return 0, nil, e
			}
			approval = in.ApprovalID
		}
		var version int64
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0)+1 FROM waitlist_automation_policies WHERE organisation_id=$1 AND property_id=$2`, t.Organisation, in.PropertyID).Scan(&version); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO waitlist_automation_policies(id,organisation_id,property_id,version,mode,channel,offer_seconds,max_offers_per_entry,max_offers_per_run,approval_id,authorizing_membership_id,authorizing_role_id,effective_at,expires_at,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, id, t.Organisation, in.PropertyID, version, in.Mode, in.Channel, in.OfferSeconds, in.MaxOffersPerEntry, in.MaxOffersPerRun, approval, t.Member, t.Role, in.EffectiveAt, in.ExpiresAt, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := reservations.Load[WaitlistAutomationPolicy](ctx, tx, `SELECT to_jsonb(p) FROM waitlist_automation_policies p WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "waitlist_automation_policy", "WAITLIST_AUTOMATION_POLICY_SAVED", "reservation.waitlist_policy.updated.v1", version, out)
		return 201, out, e
	})
}

func (h *Handler) waitlistAutomationProperty(r *http.Request) (tenant, ids.ID, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, ids.Nil, e
	}
	property, e := ids.ParseV7(r.URL.Query().Get("property_id"))
	if e != nil {
		return t, ids.Nil, apperrors.InvalidRequest("A property_id filter is required.")
	}
	t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "reservations:read", "PROPERTY", property, false)
	return t, property, e
}

func (h *Handler) reservationWaitlistAutomationPolicies(w http.ResponseWriter, r *http.Request) {
	t, property, e := h.waitlistAutomationProperty(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(p) FROM waitlist_automation_policies p WHERE organisation_id=$1 AND property_id=$2 ORDER BY version DESC LIMIT $3 OFFSET $4`, t.Organisation, property, limit, offset)
	out := WaitlistAutomationPolicyList{Items: []WaitlistAutomationPolicy{}, Limit: limit, Offset: offset}
	for _, raw := range rows {
		var p WaitlistAutomationPolicy
		if e == nil {
			e = json.Unmarshal(raw, &p)
		}
		out.Items = append(out.Items, p)
	}
	reply(w, r, 200, out, e)
}

func (h *Handler) reservationWaitlistAutomationRuns(w http.ResponseWriter, r *http.Request) {
	t, property, e := h.waitlistAutomationProperty(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out := WaitlistAutomationRunList{Items: []WaitlistAutomationRun{}, Limit: limit, Offset: offset}
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x) FROM waitlist_automation_runs x WHERE organisation_id=$1 AND property_id=$2 ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, property, limit, offset)
	for _, raw := range rows {
		var v WaitlistAutomationRun
		if e == nil {
			e = json.Unmarshal(raw, &v)
		}
		out.Items = append(out.Items, v)
	}
	if e == nil {
		e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM inventory_release_signals WHERE organisation_id=$1 AND property_id=$2 AND processed_at IS NULL),(SELECT lease_owner FROM waitlist_automation_leases WHERE organisation_id=$1 AND property_id=$2),(SELECT lease_expires_at FROM waitlist_automation_leases WHERE organisation_id=$1 AND property_id=$2)`, t.Organisation, property).Scan(&out.PendingSignals, &out.LeaseOwner, &out.LeaseExpiresAt)
	}
	reply(w, r, 200, out, e)
}

// reservationWaitlistAutomationTrigger durably requests a run. It never offers
// inventory itself: the worker re-checks policy, authority and stock.
func (h *Handler) reservationWaitlistAutomationTrigger(w http.ResponseWriter, r *http.Request) {
	var in WaitlistAutomationTrigger
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property and reason required."))
		return
	}
	h.execute(w, r, "reservation:waitlist-automation:trigger:"+in.PropertyID.String(), in, "reservations:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reservations:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		if _, e = portfolio.Get(ctx, tx, t.Organisation, in.PropertyID); e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		if _, e = tx.Exec(ctx, `INSERT INTO waitlist_automation_leases(organisation_id,property_id,run_requested_at) VALUES($1,$2,$3) ON CONFLICT(organisation_id,property_id) DO UPDATE SET run_requested_at=excluded.run_requested_at,updated_at=$3`, t.Organisation, in.PropertyID, now); e != nil {
			return 0, nil, e
		}
		mode := "MANUAL"
		policy, e := latestWaitlistPolicy(ctx, tx, t.Organisation, in.PropertyID)
		if e != nil {
			return 0, nil, e
		}
		if policy != nil && policy.Mode == "AUTOMATIC_OFFERS" && !now.Before(policy.EffectiveAt) && now.Before(policy.ExpiresAt) {
			mode = policy.Mode
		}
		if e = audit(ctx, tx, t, "WAITLIST_AUTOMATION_RUN_REQUESTED", "property", in.PropertyID, map[string]any{"reason": in.Reason, "policy_mode": mode}); e != nil {
			return 0, nil, e
		}
		return 202, WaitlistAutomationTriggered{PropertyID: in.PropertyID, RequestedAt: now, PolicyMode: mode, Executed: false}, nil
	})
}

func latestWaitlistPolicy(ctx context.Context, q database.Querier, org, property ids.ID) (*WaitlistAutomationPolicy, error) {
	p, e := reservations.Load[WaitlistAutomationPolicy](ctx, q, `SELECT to_jsonb(p) FROM waitlist_automation_policies p WHERE organisation_id=$1 AND property_id=$2 ORDER BY version DESC LIMIT 1`, org, property)
	var app *apperrors.Error
	if errors.As(e, &app) && app.Status == http.StatusNotFound {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return &p, nil
}

// ProcessWaitlistAutomation expires lapsed offers (releasing their holds),
// then offers released stock to the next eligible waiting entries of every
// property whose latest approved policy enables automatic offers. Work is
// driven by durable database release signals, new/returned waiting entries and
// operator run requests, so a restarted worker resumes without losing work.
func (h *Handler) ProcessWaitlistAutomation(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	now := h.Auth.Service.Now().UTC()
	if e := h.expireReservationWaitlist(ctx, limit, now); e != nil {
		return e
	}
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT p.organisation_id,p.property_id FROM waitlist_automation_policies p LEFT JOIN waitlist_automation_leases l ON l.organisation_id=p.organisation_id AND l.property_id=p.property_id WHERE p.mode='AUTOMATIC_OFFERS' AND p.effective_at<=$1 AND p.expires_at>$1 AND p.version=(SELECT max(x.version) FROM waitlist_automation_policies x WHERE x.organisation_id=p.organisation_id AND x.property_id=p.property_id) AND (l.lease_expires_at IS NULL OR l.lease_expires_at<=$1) AND (EXISTS(SELECT 1 FROM inventory_release_signals s WHERE s.organisation_id=p.organisation_id AND s.property_id=p.property_id AND s.processed_at IS NULL) OR l.run_requested_at>coalesce(l.last_completed_at,'-infinity'::timestamptz) OR EXISTS(SELECT 1 FROM waitlist_entries e WHERE e.organisation_id=p.organisation_id AND e.property_id=p.property_id AND e.state='WAITING' AND e.expires_at>$1 AND e.updated_at>coalesce(l.last_completed_at,'-infinity'::timestamptz))) ORDER BY coalesce(l.last_completed_at,'-infinity'::timestamptz),p.property_id LIMIT $2`, now, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, property ids.ID }
	refs := []ref{}
	for rows.Next() {
		var v ref
		if e = rows.Scan(&v.org, &v.property); e != nil {
			break
		}
		refs = append(refs, v)
	}
	rowErr := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if rowErr != nil {
		return rowErr
	}
	owner := "business-waitlist-worker:" + ids.New().String()
	var failures []error
	for _, v := range refs {
		generation, claimed, e := h.ClaimWaitlistAutomationLease(ctx, v.org, v.property, owner, 30*time.Second)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if !claimed {
			continue
		}
		if _, e = h.RunWaitlistAutomation(ctx, v.org, v.property, owner, generation); e != nil && !errors.Is(e, ErrWaitlistLeaseLost) {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}

// ClaimWaitlistAutomationLease takes the property lease only when it is free
// or expired, advancing the fencing generation.
func (h *Handler) ClaimWaitlistAutomationLease(ctx context.Context, org, property ids.ID, owner string, ttl time.Duration) (int64, bool, error) {
	now := h.Auth.Service.Now().UTC()
	var generation int64
	e := database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `INSERT INTO waitlist_automation_leases(organisation_id,property_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, org, property); e != nil {
			return e
		}
		e := tx.QueryRow(ctx, `UPDATE waitlist_automation_leases SET lease_owner=$3,lease_generation=lease_generation+1,lease_expires_at=$4,updated_at=$5 WHERE organisation_id=$1 AND property_id=$2 AND (lease_expires_at IS NULL OR lease_expires_at<=$5) RETURNING lease_generation`, org, property, owner, now.Add(ttl), now).Scan(&generation)
		if errors.Is(e, pgx.ErrNoRows) {
			generation = 0
			return nil
		}
		return e
	})
	return generation, generation > 0, e
}

type waitlistEvaluation struct {
	EntryID ids.ID  `json:"entry_id"`
	Outcome string  `json:"outcome"`
	Code    string  `json:"code,omitempty"`
	OfferID *ids.ID `json:"offer_id,omitempty"`
}

// RunWaitlistAutomation performs one fenced run under the organisation lock.
// A stale owner/generation returns ErrWaitlistLeaseLost and changes nothing.
func (h *Handler) RunWaitlistAutomation(ctx context.Context, org, property ids.ID, owner string, generation int64) (WaitlistAutomationRun, error) {
	var out WaitlistAutomationRun
	e := database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, org); e != nil {
			return e
		}
		now := h.Auth.Service.Now().UTC()
		var currentOwner *string
		var currentGeneration int64
		var expires *time.Time
		var lastCompleted, requested *time.Time
		if e := tx.QueryRow(ctx, `SELECT lease_owner,lease_generation,lease_expires_at,last_completed_at,run_requested_at FROM waitlist_automation_leases WHERE organisation_id=$1 AND property_id=$2 FOR UPDATE`, org, property).Scan(&currentOwner, &currentGeneration, &expires, &lastCompleted, &requested); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return ErrWaitlistLeaseLost
			}
			return e
		}
		if currentOwner == nil || *currentOwner != owner || currentGeneration != generation || expires == nil || !now.Before(*expires) {
			return ErrWaitlistLeaseLost
		}
		if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
			return e
		}
		var signals int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM inventory_release_signals WHERE organisation_id=$1 AND property_id=$2 AND processed_at IS NULL`, org, property).Scan(&signals); e != nil {
			return e
		}
		trigger := "NEW_ENTRY"
		if signals > 0 {
			trigger = "RELEASE_SIGNAL"
		} else if requested != nil && (lastCompleted == nil || requested.After(*lastCompleted)) {
			trigger = "MANUAL"
		}
		run := WaitlistAutomationRun{ID: ids.New(), OrganisationID: org, PropertyID: property, LeaseOwner: owner, LeaseGeneration: generation, Trigger: trigger, CreatedByService: "business-operations-service"}
		evaluations := []waitlistEvaluation{}
		type created struct {
			offer, entry ids.ID
			attempt      int
		}
		offers := []created{}
		state, e := func() (string, error) {
			policy, e := latestWaitlistPolicy(ctx, tx, org, property)
			if e != nil {
				return "", e
			}
			if policy == nil || policy.Mode != "AUTOMATIC_OFFERS" || now.Before(policy.EffectiveAt) || !now.Before(policy.ExpiresAt) {
				return "POLICY_INACTIVE", nil
			}
			run.PolicyID, run.PolicyVersion = &policy.ID, &policy.Version
			allowed, e := h.currentRecordedPMSAuthority(ctx, tx, org, property, policy.AuthorizingMembershipID, policy.AuthorizingRoleID, "reservations:write")
			if e != nil {
				return "", e
			}
			if !allowed {
				return "AUTHORITY_REVOKED", nil
			}
			t := tenant{Organisation: org, State: "ACTIVE"}
			if e = h.commercialGate(ctx, tx, t, property); e != nil {
				var app *apperrors.Error
				if errors.As(e, &app) {
					return "PROPERTY_INELIGIBLE", nil
				}
				return "", e
			}
			var zone string
			if e = tx.QueryRow(ctx, `SELECT timezone FROM inventory_nodes WHERE organisation_id=$1 AND id=$2`, org, property).Scan(&zone); e != nil {
				return "", e
			}
			loc, e := time.LoadLocation(zone)
			if e != nil {
				return "", e
			}
			today := now.In(loc).Format(time.DateOnly)
			// Priority first, then fewer prior automatic offers, then arrival order.
			candidates, e := commercialJSONRows(ctx, tx, `SELECT jsonb_build_object('entry',to_jsonb(e),'attempts',(SELECT count(*) FROM waitlist_automation_offers a WHERE a.organisation_id=e.organisation_id AND a.entry_id=e.id)) FROM waitlist_entries e WHERE e.organisation_id=$1 AND e.property_id=$2 AND e.state='WAITING' AND e.expires_at>$3 AND (SELECT count(*) FROM waitlist_automation_offers a WHERE a.organisation_id=e.organisation_id AND a.entry_id=e.id)<$4 ORDER BY e.priority DESC,(SELECT count(*) FROM waitlist_automation_offers a WHERE a.organisation_id=e.organisation_id AND a.entry_id=e.id),e.created_at,e.id LIMIT 200`, org, property, now, policy.MaxOffersPerEntry)
			if e != nil {
				return "", e
			}
			for _, raw := range candidates {
				if len(offers) >= policy.MaxOffersPerRun {
					break
				}
				var c struct {
					Entry    ReservationWaitlist `json:"entry"`
					Attempts int                 `json:"attempts"`
				}
				if e = json.Unmarshal(raw, &c); e != nil {
					return "", e
				}
				if c.Entry.DateFrom < today {
					evaluations = append(evaluations, waitlistEvaluation{EntryID: c.Entry.ID, Outcome: "INELIGIBLE", Code: "STAY_DATES_PASSED"})
					continue
				}
				if _, e = tx.Exec(ctx, `SAVEPOINT waitlist_offer`); e != nil {
					return "", e
				}
				offer, err := h.automaticWaitlistOffer(ctx, tx, t, *policy, c.Entry, now)
				if err != nil {
					var app *apperrors.Error
					if !errors.As(err, &app) && database.SQLState(err) != database.CodeCheckViolation && database.SQLState(err) != database.CodeUniqueViolation {
						return "", err
					}
					if _, e = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT waitlist_offer`); e != nil {
						return "", e
					}
					code := "CONSTRAINT"
					if app != nil {
						code = app.Code
					}
					evaluations = append(evaluations, waitlistEvaluation{EntryID: c.Entry.ID, Outcome: "SKIPPED", Code: code})
					continue
				}
				if _, e = tx.Exec(ctx, `RELEASE SAVEPOINT waitlist_offer`); e != nil {
					return "", e
				}
				offerID := offer
				evaluations = append(evaluations, waitlistEvaluation{EntryID: c.Entry.ID, Outcome: "OFFERED", OfferID: &offerID})
				offers = append(offers, created{offer: offer, entry: c.Entry.ID, attempt: c.Attempts + 1})
			}
			if len(offers) == 0 {
				return "NO_ELIGIBLE_CANDIDATE", nil
			}
			return "OFFERED", nil
		}()
		if e != nil {
			return e
		}
		run.State, run.OffersCreated, run.SignalsProcessed = state, len(offers), signals
		run.Evaluations = marshalCommercial(evaluations)
		if _, e = tx.Exec(ctx, `INSERT INTO waitlist_automation_runs(id,organisation_id,property_id,policy_id,policy_version,lease_owner,lease_generation,trigger,state,offers_created,signals_processed,evaluations,created_by_service,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'business-operations-service',$13)`, run.ID, org, property, run.PolicyID, run.PolicyVersion, owner, generation, trigger, state, len(offers), signals, run.Evaluations, now); e != nil {
			return e
		}
		for _, o := range offers {
			if _, e = tx.Exec(ctx, `INSERT INTO waitlist_automation_offers(offer_id,organisation_id,property_id,entry_id,run_id,policy_id,policy_version,attempt_number,lease_generation) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, o.offer, org, property, o.entry, run.ID, run.PolicyID, run.PolicyVersion, o.attempt, generation); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE inventory_release_signals SET processed_at=$3,run_id=$4 WHERE organisation_id=$1 AND property_id=$2 AND processed_at IS NULL`, org, property, now, run.ID); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE waitlist_automation_leases SET lease_owner=NULL,lease_expires_at=NULL,last_completed_at=$3,updated_at=$3 WHERE organisation_id=$1 AND property_id=$2`, org, property, now); e != nil {
			return e
		}
		run.CreatedAt = now
		out = run
		return h.commercialCommit(ctx, tx, tenant{Organisation: org}, property, run.ID, "waitlist_automation_run", "WAITLIST_AUTOMATION_RUN_"+state, "", 1, map[string]any{"trigger": trigger, "state": state, "offers_created": len(offers), "signals_processed": signals, "lease_generation": generation})
	})
	return out, e
}

// automaticWaitlistOffer prices the exact requested stay under the policy
// channel, places an atomic canonical hold and records an ordinary offer.
// Notification is separate; only the guest's acceptance creates a booking.
func (h *Handler) automaticWaitlistOffer(ctx context.Context, tx pgx.Tx, t tenant, policy WaitlistAutomationPolicy, candidate ReservationWaitlist, now time.Time) (ids.ID, error) {
	entry, e := reservations.Load[ReservationWaitlist](ctx, tx, `SELECT to_jsonb(e) FROM waitlist_entries e WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, candidate.ID)
	if e != nil {
		return ids.Nil, e
	}
	if entry.Version != candidate.Version || entry.State != "WAITING" || !now.Before(entry.ExpiresAt) {
		return ids.Nil, apperrors.Conflict("WAITLIST_STALE", "The entry changed before automatic offer.")
	}
	q, e := h.calculateQuote(ctx, tx, t, CommercialQuoteCommand{ProductID: entry.ProductID, DateFrom: entry.DateFrom, DateTo: entry.DateTo, Adults: entry.Adults, Children: entry.Children, Quantity: entry.Quantity, Channel: policy.Channel}, true)
	if e != nil {
		return ids.Nil, e
	}
	if q.PropertyID != entry.PropertyID || len(q.Nights) == 0 {
		return ids.Nil, apperrors.Conflict("WAITLIST_QUOTE_MISMATCH", "The automatic quote must cover the requested property stay.")
	}
	if _, e = tx.Exec(ctx, `INSERT INTO price_quotes(id,organisation_id,property_id,product_id,rate_version_id,date_from,date_to,amount_minor,currency,expires_at,snapshot) VALUES($1,$2,$3,$4,$5,$6::date,$7::date,$8,$9,$10,$11)`, q.QuoteID, t.Organisation, q.PropertyID, q.ProductID, q.Nights[0].RateVersionID, q.DateFrom, q.DateTo, q.AmountMinor, q.Currency, q.ExpiresAt, marshalCommercial(q)); e != nil {
		return ids.Nil, e
	}
	seconds := min(policy.OfferSeconds, int(entry.ExpiresAt.Sub(now).Seconds()), int(q.ExpiresAt.Sub(now).Seconds()))
	if seconds < 30 {
		return ids.Nil, apperrors.Conflict("WAITLIST_OFFER_TOO_SHORT", "The entry and quote must remain valid for at least 30 seconds.")
	}
	command := inventory.HoldCommand{ProductID: entry.ProductID, ProductVersion: q.ProductVersion, PoolVersion: q.PoolVersion, QuoteID: q.QuoteID, DateFrom: entry.DateFrom, DateTo: entry.DateTo, Quantity: entry.Quantity, Channel: policy.Channel, DurationSeconds: seconds}
	if e = h.validateHoldQuote(ctx, tx, t, command); e != nil {
		return ids.Nil, e
	}
	hold, e := (inventory.Repository{Pool: h.Auth.Service.Pool, Outbox: h.Auth.Service.Outbox, Now: h.Auth.Service.Now}).PlaceHold(ctx, tx, t.Organisation, ids.Nil, command)
	if e != nil {
		return ids.Nil, e
	}
	id := ids.New()
	if _, e = tx.Exec(ctx, `INSERT INTO waitlist_offers(id,organisation_id,property_id,entry_id,hold_id,quote_id,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, t.Organisation, entry.PropertyID, entry.ID, hold.ID, q.QuoteID, hold.ExpiresAt); e != nil {
		return ids.Nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE waitlist_entries SET state='OFFERED',version=version+1,updated_at=$3 WHERE organisation_id=$1 AND id=$2`, t.Organisation, entry.ID, now); e != nil {
		return ids.Nil, e
	}
	return id, h.commercialCommit(ctx, tx, t, entry.PropertyID, entry.ID, "waitlist", "WAITLIST_OFFERED_AUTOMATICALLY", "reservation.waitlist.changed.v1", entry.Version+1, map[string]any{"offer_id": id, "hold_id": hold.ID, "quote_id": q.QuoteID, "state": "OFFERED", "expires_at": hold.ExpiresAt, "policy_id": policy.ID, "policy_version": policy.Version, "automatic": true, "guest_acceptance_required": true})
}
