package usecases

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/sessions"
)

// AccountEventPayload is the payload of owner.account.activated.v1 and
// owner.account.restricted.v1 (contracts/events/owners). No PII.
type AccountEventPayload struct {
	SchemaVersion  int       `json:"schema_version"`
	AccountID      ids.ID    `json:"account_id"`
	Status         string    `json:"status"`
	PreviousStatus string    `json:"previous_status"`
	ReasonCode     string    `json:"reason_code"`
	AccountVersion int64     `json:"account_version"`
	ChangedAt      time.Time `json:"changed_at"`
}

type transition struct {
	to         domain.AccountStatus
	actor      domain.TransitionActor
	reason     string
	resolution string
	note       string
	// expectedVersion, when > 0, must equal the current version (If-Match).
	expectedVersion int64
}

// transitionTx applies a guarded lifecycle transition to a locked account:
// FSM + actor check, compare-and-swap on (status, version), history, audit
// and outbox event — all in tx.
func (s *Service) transitionTx(ctx context.Context, tx pgx.Tx, acc db.OwnerAccount, t transition) (db.OwnerAccount, error) {
	from := domain.AccountStatus(acc.Status)
	if t.expectedVersion > 0 && t.expectedVersion != acc.Version {
		return db.OwnerAccount{}, domain.ErrVersionConflict()
	}
	if _, err := domain.CheckAccountTransition(from, t.to, t.actor, t.resolution); err != nil {
		return db.OwnerAccount{}, err
	}
	if !domain.ValidReasonCode(t.reason) {
		return db.OwnerAccount{}, apperrors.InvalidRequest("reason_code must be SCREAMING_SNAKE_CASE.")
	}
	now := s.now()
	p := db.TransitionAccountStatusParams{Status: string(t.to), StatusReason: strp(t.reason), Now: now, AccountID: acc.AccountID,
		ExpectedStatus: acc.Status, ExpectedVersion: acc.Version}
	switch t.to {
	case domain.StatusClosurePending:
		eff := now.Add(s.Policy.ClosureCoolingOff)
		p.ClosureRequestedAt, p.ClosureEffectiveAt = &now, &eff
	case domain.StatusClosed:
		p.ClosureRequestedAt, p.ClosureEffectiveAt, p.ClosedAt = acc.ClosureRequestedAt, acc.ClosureEffectiveAt, &now
	}
	next, err := s.q.TransitionAccountStatus(ctx, tx, p)
	if isNoRows(err) {
		return db.OwnerAccount{}, domain.ErrVersionConflict()
	}
	if err != nil {
		return db.OwnerAccount{}, err
	}
	rc := requestcontext.From(ctx)
	h := db.InsertStatusHistoryParams{HistoryID: ids.New(), AccountID: acc.AccountID, FromStatus: &acc.Status, ToStatus: string(t.to),
		ReasonCode: t.reason, ActorType: audit.ActorSystem, CallingService: strp(rc.CallingService), ResolutionReference: strp(t.resolution),
		Note: strp(truncate(t.note, 1000)), AccountVersion: next.Version, OccurredAt: now}
	if !rc.Actor.IsZero() {
		h.ActorType, h.ActorDomain, h.ActorID = audit.ActorUser, &rc.Actor.Domain, &rc.Actor.ID
	} else if rc.CallingService != "" {
		h.ActorType = audit.ActorService
	}
	if err := s.q.InsertStatusHistory(ctx, tx, h); err != nil {
		return db.OwnerAccount{}, err
	}
	if err := s.auditTx(ctx, tx, "owner.account.status_changed", acc.AccountID, audit.ResultSuccess,
		map[string]any{"from": acc.Status, "to": string(t.to), "version": next.Version},
		map[string]any{"reason_code": t.reason, "resolution_reference": t.resolution, "transition_actor": string(t.actor)}); err != nil {
		return db.OwnerAccount{}, err
	}
	payload := AccountEventPayload{SchemaVersion: AccountEventSchemaVersion, AccountID: acc.AccountID, Status: string(t.to),
		PreviousStatus: acc.Status, ReasonCode: t.reason, AccountVersion: next.Version, ChangedAt: now}
	switch t.to {
	case domain.StatusActive:
		if from == domain.StatusPendingVerification || from == domain.StatusRestricted || from == domain.StatusSuspended {
			err = s.emit(ctx, tx, EventAccountActivated, acc.AccountID, payload)
		}
	case domain.StatusRestricted, domain.StatusSuspended:
		err = s.emit(ctx, tx, EventAccountRestricted, acc.AccountID, payload)
	}
	if err != nil {
		return db.OwnerAccount{}, err
	}
	if t.to == domain.StatusSuspended || t.to == domain.StatusClosed {
		if _, err := s.q.RevokeAccountSessions(ctx, tx, db.RevokeAccountSessionsParams{Now: now, Reason: "ACCOUNT_" + string(t.to), AccountID: acc.AccountID}); err != nil {
			return db.OwnerAccount{}, err
		}
	}
	return next, nil
}

func (s *Service) lockAccount(ctx context.Context, tx pgx.Tx, id ids.ID) (db.OwnerAccount, error) {
	acc, err := s.q.GetAccountForUpdate(ctx, tx, id)
	if isNoRows(err) {
		return db.OwnerAccount{}, domain.ErrAccountNotFound()
	}
	return acc, err
}

// activationSatisfied applies the versioned activation policy.
func (s *Service) activationSatisfied(acc db.OwnerAccount) bool {
	e, p := acc.EmailVerifiedAt != nil, acc.PhoneVerifiedAt != nil
	switch s.Policy.Activation {
	case ActivateOnPhone:
		return p
	case ActivateOnEmailOrPhone:
		return e || p
	case ActivateOnEmailAndPhn:
		return e && p
	}
	return e
}

// maybeActivate moves a pending account to ACTIVE when policy is satisfied.
func (s *Service) maybeActivate(ctx context.Context, tx pgx.Tx, acc db.OwnerAccount) (db.OwnerAccount, error) {
	if acc.Status != string(domain.StatusPendingVerification) || !s.activationSatisfied(acc) {
		return acc, nil
	}
	return s.transitionTx(ctx, tx, acc, transition{to: domain.StatusActive, actor: domain.ActorSystem, reason: "CONTACT_VERIFIED"})
}

// AdminCommand is an admin-service lifecycle command.
type AdminCommand struct {
	ReasonCode          string
	Note                string
	ResolutionReference string
	TargetStatus        string // reinstate: ACTIVE (default) | RESTRICTED
	ExpectedVersion     int64
}

// AdminTransitionTx restricts, suspends or reinstates an account on behalf of
// an attributable admin actor. Runs inside the idempotency transaction.
func (s *Service) AdminTransitionTx(ctx context.Context, tx pgx.Tx, subj authz.Subject, action authz.Action, accountID ids.ID, cmd AdminCommand) (AdminAccountView, error) {
	if _, err := s.Authz.Require(ctx, subj, action, authz.Resource{AccountID: accountID}); err != nil {
		return AdminAccountView{}, err
	}
	acc, err := s.lockAccount(ctx, tx, accountID)
	if err != nil {
		return AdminAccountView{}, err
	}
	t := transition{actor: domain.ActorAdmin, reason: cmd.ReasonCode, note: cmd.Note, resolution: cmd.ResolutionReference, expectedVersion: cmd.ExpectedVersion}
	alert := "ACCOUNT_RESTRICTION"
	switch action {
	case authz.ActAdminRestrict:
		t.to = domain.StatusRestricted
	case authz.ActAdminSuspend:
		t.to = domain.StatusSuspended
	case authz.ActAdminReinstate:
		t.to = domain.StatusActive
		if cmd.TargetStatus == string(domain.StatusRestricted) {
			t.to = domain.StatusRestricted
		} else if cmd.TargetStatus != "" && cmd.TargetStatus != string(domain.StatusActive) {
			return AdminAccountView{}, apperrors.InvalidRequest("target_status must be ACTIVE or RESTRICTED.")
		}
		if acc.Status != string(domain.StatusRestricted) && acc.Status != string(domain.StatusSuspended) {
			return AdminAccountView{}, apperrors.InvalidStateTransition(acc.Status, string(t.to))
		}
	default:
		return AdminAccountView{}, errors.New("usecases: unsupported admin action")
	}
	if acc.Status == string(t.to) {
		return AdminAccountView{}, apperrors.InvalidStateTransition(acc.Status, string(t.to))
	}
	next, err := s.transitionTx(ctx, tx, acc, t)
	if err != nil {
		return AdminAccountView{}, err
	}
	if err := s.securityEvent(ctx, tx, accountID, "ACCOUNT_STATUS_CHANGED", ids.Nil, ids.Nil, ClientInfo{},
		map[string]any{"status": next.Status, "reason_code": cmd.ReasonCode}); err != nil {
		return AdminAccountView{}, err
	}
	if err := s.enqueueAlert(ctx, tx, next, alert, ""); err != nil {
		return AdminAccountView{}, err
	}
	return s.adminView(ctx, tx, next)
}

// AdminRevokeSessionsTx force-signs-out every session of an account.
func (s *Service) AdminRevokeSessionsTx(ctx context.Context, tx pgx.Tx, subj authz.Subject, accountID ids.ID, reason string) (int64, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActAdminRevokeSessions, authz.Resource{AccountID: accountID}); err != nil {
		return 0, err
	}
	if !domain.ValidReasonCode(reason) {
		return 0, apperrors.InvalidRequest("reason_code must be SCREAMING_SNAKE_CASE.")
	}
	acc, err := s.lockAccount(ctx, tx, accountID)
	if err != nil {
		return 0, err
	}
	n, err := s.q.RevokeAccountSessions(ctx, tx, db.RevokeAccountSessionsParams{Now: s.now(), Reason: sessions.ReasonAdmin, AccountID: accountID})
	if err != nil {
		return 0, err
	}
	if err := s.auditTx(ctx, tx, "owner.account.sessions_revoked", accountID, audit.ResultSuccess, map[string]any{"revoked": n}, map[string]any{"reason_code": reason}); err != nil {
		return 0, err
	}
	if err := s.securityEvent(ctx, tx, accountID, "SESSIONS_REVOKED_BY_ADMIN", ids.Nil, ids.Nil, ClientInfo{}, map[string]any{"revoked": n}); err != nil {
		return 0, err
	}
	return n, s.enqueueAlert(ctx, tx, acc, "SESSIONS_REVOKED", "")
}

// AdminGetAccount returns the operator projection.
func (s *Service) AdminGetAccount(ctx context.Context, subj authz.Subject, accountID ids.ID) (AdminAccountView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActAdminAccountRead, authz.Resource{AccountID: accountID}); err != nil {
		return AdminAccountView{}, err
	}
	acc, err := s.q.GetAccount(ctx, s.Pool, accountID)
	if isNoRows(err) {
		return AdminAccountView{}, domain.ErrAccountNotFound()
	}
	if err != nil {
		return AdminAccountView{}, err
	}
	var v AdminAccountView
	err = s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.audit.Record(ctx, tx, audit.Event{Action: "owner.account.privileged_read", TargetType: "owner_account", TargetID: accountID}); err != nil {
			return err
		}
		v, err = s.adminView(ctx, tx, acc)
		return err
	})
	return v, err
}

func (s *Service) adminView(ctx context.Context, q db.DBTX, acc db.OwnerAccount) (AdminAccountView, error) {
	hist, err := s.q.ListStatusHistory(ctx, q, acc.AccountID)
	if err != nil {
		return AdminAccountView{}, err
	}
	v := AdminAccountView{AccountView: accountView(acc), StatusReason: acc.StatusReason, StatusChangedAt: acc.StatusChangedAt,
		FailedLoginCount: acc.FailedLoginCount, LockedUntil: acc.LockedUntil, History: []StatusHistoryView{}}
	for _, h := range hist {
		v.History = append(v.History, StatusHistoryView{FromStatus: h.FromStatus, ToStatus: h.ToStatus, ReasonCode: h.ReasonCode,
			ActorType: h.ActorType, ActorDomain: h.ActorDomain, ActorID: h.ActorID, ResolutionReference: h.ResolutionReference,
			AccountVersion: h.AccountVersion, OccurredAt: h.OccurredAt})
	}
	return v, nil
}

// RequestClosure moves the owner's account to CLOSURE_PENDING (step-up).
func (s *Service) RequestClosure(ctx context.Context, subj authz.Subject, ci ClientInfo) (AccountView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActClosureRequest, authz.Resource{}); err != nil {
		return AccountView{}, err
	}
	id := subj.Account.AccountID
	var out db.OwnerAccount
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		acc, err := s.lockAccount(ctx, tx, id)
		if err != nil {
			return err
		}
		if out, err = s.transitionTx(ctx, tx, acc, transition{to: domain.StatusClosurePending, actor: domain.ActorOwner, reason: "OWNER_REQUESTED"}); err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, id, "CLOSURE_REQUESTED", subj.Account.SessionID, ids.Nil, ci, map[string]any{"effective_at": out.ClosureEffectiveAt}); err != nil {
			return err
		}
		return s.enqueueAlert(ctx, tx, out, "CLOSURE_REQUESTED", "")
	})
	return accountView(out), err
}

// CancelClosure returns a CLOSURE_PENDING account to its prior standing.
func (s *Service) CancelClosure(ctx context.Context, subj authz.Subject, ci ClientInfo) (AccountView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActClosureCancel, authz.Resource{}); err != nil {
		return AccountView{}, err
	}
	id := subj.Account.AccountID
	var out db.OwnerAccount
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		acc, err := s.lockAccount(ctx, tx, id)
		if err != nil {
			return err
		}
		to := domain.StatusPendingVerification
		if s.activationSatisfied(acc) {
			to = domain.StatusActive
		}
		if out, err = s.transitionTx(ctx, tx, acc, transition{to: to, actor: domain.ActorOwner, reason: "OWNER_CANCELLED_CLOSURE"}); err != nil {
			return err
		}
		return s.securityEvent(ctx, tx, id, "CLOSURE_CANCELLED", subj.Account.SessionID, ids.Nil, ci, nil)
	})
	return accountView(out), err
}

// FinalizeDueClosures closes accounts whose cooling-off elapsed and that have
// no holds. Safe for parallel workers (FOR UPDATE SKIP LOCKED).
func (s *Service) FinalizeDueClosures(ctx context.Context, limit int) (closed, held int, err error) {
	sys := authz.Subject{Kind: authz.KindSystem}
	if _, err := s.Authz.Require(ctx, sys, authz.ActSystemFinalizeClosure, authz.Resource{}); err != nil {
		return 0, 0, err
	}
	err = s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		closed, held = 0, 0
		due, err := s.q.ClaimDueClosures(ctx, tx, db.ClaimDueClosuresParams{Now: s.now(), RowLimit: int32(limit)})
		if err != nil {
			return err
		}
		for _, acc := range due {
			holds, err := s.ClosureHolds.Holds(ctx, tx, acc.AccountID)
			if err != nil {
				return err
			}
			if len(holds) > 0 {
				held++
				if err := s.auditTx(ctx, tx, "owner.account.closure_held", acc.AccountID, audit.ResultDenied, nil, map[string]any{"holds": holds}); err != nil {
					return err
				}
				continue
			}
			if _, err := s.transitionTx(ctx, tx, acc, transition{to: domain.StatusClosed, actor: domain.ActorSystem, reason: "CLOSURE_COOLING_OFF_ELAPSED"}); err != nil {
				return err
			}
			if err := s.q.InvalidateRecoveryCodes(ctx, tx, db.InvalidateRecoveryCodesParams{AccountID: acc.AccountID, InvalidatedAt: ptrTime(s.now())}); err != nil {
				return err
			}
			if _, err := s.q.DisableActiveTOTP(ctx, tx, db.DisableActiveTOTPParams{AccountID: acc.AccountID, DisabledAt: ptrTime(s.now())}); err != nil {
				return err
			}
			closed++
		}
		return nil
	})
	return closed, held, err
}
