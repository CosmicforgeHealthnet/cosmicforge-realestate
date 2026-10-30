// Package approvals owns versioned approval requirements and decision rules.
package approvals

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
)

var (
	ErrInvalid  = errors.New("invalid approval policy or command")
	ErrConflict = errors.New("approval command conflicts with current state")
	ErrDenied   = errors.New("approval decision denied")
)

type Level struct {
	Capability permissions.Permission `json:"capability"`
	Count      int                    `json:"count"`
	RequireMFA bool                   `json:"require_mfa"`
}
type Policy struct {
	Channel              string           `json:"channel,omitempty"`
	Scope                *workforce.Scope `json:"scope,omitempty"`
	ActorRoleKey         *ids.ID          `json:"actor_role_key,omitempty"`
	ID                   ids.ID           `json:"id"`
	OrganisationID       ids.ID           `json:"organisation_id"`
	Version              int64            `json:"version"`
	Action               string           `json:"action"`
	Risk                 string           `json:"risk"`
	Active               bool             `json:"active"`
	EffectiveAt          time.Time        `json:"effective_at"`
	ExpiresAt            *time.Time       `json:"expires_at,omitempty"`
	ThresholdMinor       *int64           `json:"threshold_minor,omitempty"`
	Currency             string           `json:"currency,omitempty"`
	SeparationOfDuties   bool             `json:"separation_of_duties"`
	DistinctAcrossLevels bool             `json:"distinct_across_levels"`
	Levels               []Level          `json:"levels"`
}

func (p Policy) Validate() error {
	if len(p.Channel) > 64 {
		return ErrInvalid
	}
	if p.Scope != nil && (!p.Scope.Valid() || p.Scope.Organisation != p.OrganisationID) || p.ActorRoleKey != nil && p.ActorRoleKey.ValidateV7() != nil {
		return ErrInvalid
	}
	if p.ID.ValidateV7() != nil || p.OrganisationID.ValidateV7() != nil || p.Version < 1 || strings.TrimSpace(p.Action) == "" || p.EffectiveAt.IsZero() || len(p.Levels) == 0 || len(p.Levels) > 10 || p.ExpiresAt != nil && !p.ExpiresAt.After(p.EffectiveAt) {
		return ErrInvalid
	}
	switch p.Risk {
	case "LOW", "MEDIUM", "HIGH", "CRITICAL":
	default:
		return ErrInvalid
	}
	if (p.Risk == "HIGH" || p.Risk == "CRITICAL") && (!p.SeparationOfDuties || !p.DistinctAcrossLevels) {
		return ErrInvalid
	}
	if p.ThresholdMinor != nil && (*p.ThresholdMinor < 0 || !currency(p.Currency)) {
		return ErrInvalid
	}
	for _, l := range p.Levels {
		if !l.Capability.Valid() || l.Count < 1 || l.Count > 20 {
			return ErrInvalid
		}
	}
	return nil
}
func currency(s string) bool {
	_, err := money.ParseCurrency(s)
	return err == nil
}

func (p Policy) Applies(org ids.ID, action, risk string, amount *int64, ccy string, now time.Time) bool {
	if p.Validate() != nil || !p.Active || now.IsZero() || p.OrganisationID != org || p.Action != action || p.Risk != risk || now.Before(p.EffectiveAt) || p.ExpiresAt != nil && !now.Before(*p.ExpiresAt) {
		return false
	}
	return p.ThresholdMinor == nil || amount != nil && *amount >= *p.ThresholdMinor && ccy == p.Currency
}

func clone(p Policy) Policy {
	if p.Scope != nil {
		value := *p.Scope
		p.Scope = &value
	}
	if p.ActorRoleKey != nil {
		value := *p.ActorRoleKey
		p.ActorRoleKey = &value
	}
	p.Levels = append([]Level(nil), p.Levels...)
	if p.ThresholdMinor != nil {
		v := *p.ThresholdMinor
		p.ThresholdMinor = &v
	}
	if p.ExpiresAt != nil {
		v := *p.ExpiresAt
		p.ExpiresAt = &v
	}
	return p
}

func (p Policy) MatchesTarget(target workforce.Target, actorRoleKey ids.ID) bool {
	return (p.ActorRoleKey == nil || *p.ActorRoleKey == actorRoleKey) && (p.Scope == nil || workforce.ScopeContains(*p.Scope, target, true))
}

// Restore accepts only repository-owned state read while holding the request's
// database row lock. Historical decisions retain their original authorization;
// they are not re-evaluated against roles that may have changed since then.
func Restore(view View, amount *int64, ccy string) (*Request, error) {
	if view.ID.ValidateV7() != nil || view.RequesterID.ValidateV7() != nil || !view.Scope.Valid() || view.Policy.Validate() != nil || view.Scope.Organisation != view.Policy.OrganisationID || view.Level < 0 || view.Level > len(view.Policy.Levels) || view.Deadline.IsZero() {
		return nil, ErrInvalid
	}
	switch view.State {
	case "DRAFT", "PENDING", "APPROVED", "REJECTED", "CANCELLED", "EXPIRED":
	default:
		return nil, ErrInvalid
	}
	if view.State == "PENDING" && view.Level >= len(view.Policy.Levels) || view.State == "APPROVED" && view.Level != len(view.Policy.Levels) {
		return nil, ErrInvalid
	}
	if amount != nil && (*amount < 0 || !currency(ccy)) {
		return nil, ErrInvalid
	}
	view.Policy = clone(view.Policy)
	view.Decisions = append([]Decision(nil), view.Decisions...)
	for i := range view.Decisions {
		d := &view.Decisions[i]
		if d.ID.ValidateV7() != nil || d.ActorID.ValidateV7() != nil || d.Level < 0 || d.Level >= len(view.Policy.Levels) {
			return nil, ErrInvalid
		}
		d.Evidence = append([]ids.ID(nil), d.Evidence...)
	}
	r := &Request{view: view, currency: ccy}
	if amount != nil {
		value := *amount
		r.amount = &value
	}
	return r, nil
}

type Decision struct {
	ID       ids.ID    `json:"id"`
	ActorID  ids.ID    `json:"actor_id"`
	Level    int       `json:"level"`
	Outcome  string    `json:"outcome"`
	Reason   string    `json:"reason"`
	Evidence []ids.ID  `json:"evidence"`
	At       time.Time `json:"at"`
	Key      string    `json:"-"`
}
type View struct {
	ID          ids.ID          `json:"id"`
	RequesterID ids.ID          `json:"requester_id"`
	Policy      Policy          `json:"policy"`
	Scope       workforce.Scope `json:"scope"`
	State       string          `json:"state"`
	Level       int             `json:"level"`
	Deadline    time.Time       `json:"deadline"`
	Decisions   []Decision      `json:"decisions"`
}

// Request serializes in-process decisions and owns a private policy snapshot.
// A PostgreSQL repository must additionally lock the owning request row before
// loading/applying/saving a command; this type alone is not durable storage.
type Request struct {
	mu       sync.Mutex
	view     View
	amount   *int64
	currency string
}

func NewRequest(policy Policy, requester ids.ID, target workforce.Scope, amount *int64, ccy string, now, deadline time.Time) (*Request, error) {
	if policy.Validate() != nil || !policy.Applies(target.Organisation, policy.Action, policy.Risk, amount, ccy, now) || requester.ValidateV7() != nil || !target.Valid() || !deadline.After(now) || amount != nil && (*amount < 0 || !currency(ccy)) {
		return nil, ErrInvalid
	}
	r := &Request{view: View{ID: ids.New(), RequesterID: requester, Policy: clone(policy), Scope: target, State: "PENDING", Deadline: deadline}, currency: ccy}
	if amount != nil {
		v := *amount
		r.amount = &v
	}
	return r, nil
}

func (r *Request) Snapshot() View {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.view
	v.Policy = clone(v.Policy)
	v.Decisions = append([]Decision(nil), v.Decisions...)
	for i := range v.Decisions {
		v.Decisions[i].Evidence = append([]ids.ID(nil), v.Decisions[i].Evidence...)
	}
	return v
}

// Decide uses the caller's freshly loaded membership and trusted hierarchy.
// Reusing an idempotency key with another command is a conflict. A completed
// successful command can be repeated after the request became terminal.
func (r *Request) Decide(m workforce.Membership, target workforce.Target, outcome, reason, key string, evidence []ids.ID, now time.Time, mfaAt *time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if key == "" || len(key) > 128 || len(strings.TrimSpace(reason)) < 3 || len(reason) > 2000 || len(evidence) > 20 || now.IsZero() || (outcome != "APPROVED" && outcome != "REJECTED") {
		return ErrInvalid
	}
	for _, id := range evidence {
		if id.ValidateV7() != nil {
			return ErrInvalid
		}
	}
	for _, d := range r.view.Decisions {
		if d.Key == key {
			if d.ActorID == m.AccountID && d.Outcome == outcome && d.Reason == reason && equalIDs(d.Evidence, evidence) {
				return nil
			}
			return ErrConflict
		}
	}
	if r.view.State != "PENDING" {
		return ErrConflict
	}
	if !now.Before(r.view.Deadline) {
		r.view.State = "EXPIRED"
		return ErrConflict
	}
	p := r.view.Policy
	if target.Scope != r.view.Scope || p.SeparationOfDuties && m.AccountID == r.view.RequesterID {
		return ErrDenied
	}
	for _, d := range r.view.Decisions {
		if d.ActorID == m.AccountID && (p.DistinctAcrossLevels || d.Level == r.view.Level) {
			return ErrDenied
		}
	}
	level := p.Levels[r.view.Level]
	if level.RequireMFA && !workforce.RecentMFA(mfaAt, now, 5*time.Minute) {
		return ErrDenied
	}
	if workforce.Authorize(m, workforce.DecisionInput{AccountID: m.AccountID, Target: target, Capability: level.Capability, Now: now, AmountMinor: r.amount, Currency: r.currency, MFAAt: mfaAt, StepUpWindow: 5 * time.Minute}) != nil {
		return ErrDenied
	}
	d := Decision{ID: ids.New(), ActorID: m.AccountID, Level: r.view.Level, Outcome: outcome, Reason: reason, Evidence: append([]ids.ID(nil), evidence...), At: now, Key: key}
	r.view.Decisions = append(r.view.Decisions, d)
	if outcome == "REJECTED" {
		r.view.State = "REJECTED"
		return nil
	}
	count := 0
	for _, d := range r.view.Decisions {
		if d.Level == r.view.Level && d.Outcome == "APPROVED" {
			count++
		}
	}
	if count >= level.Count {
		r.view.Level++
		if r.view.Level == len(p.Levels) {
			r.view.State = "APPROVED"
		}
	}
	return nil
}

func equalIDs(a, b []ids.ID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (r *Request) Cancel(actor ids.ID, reason string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if actor != r.view.RequesterID || strings.TrimSpace(reason) == "" || len(reason) > 2000 || now.IsZero() {
		return ErrDenied
	}
	if r.view.State != "PENDING" && r.view.State != "DRAFT" {
		return ErrConflict
	}
	if !now.Before(r.view.Deadline) {
		r.view.State = "EXPIRED"
		return ErrConflict
	}
	r.view.State = "CANCELLED"
	r.view.Decisions = append(r.view.Decisions, Decision{ID: ids.New(), ActorID: actor, Level: r.view.Level, Outcome: "CANCELLED", Reason: reason, At: now})
	return nil
}

func (r *Request) Expire(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !now.IsZero() && r.view.State == "PENDING" && !now.Before(r.view.Deadline) {
		r.view.State = "EXPIRED"
		return true
	}
	return false
}
