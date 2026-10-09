package usecases

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/passwords"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/sessions"
)

// ChangePassword verifies the current password, sets a new one and ends
// every other session.
func (s *Service) ChangePassword(ctx context.Context, subj authz.Subject, current, next string, ci ClientInfo) error {
	if _, err := s.Authz.Require(ctx, subj, authz.ActPasswordChange, authz.Resource{}); err != nil {
		return err
	}
	if err := validatePassword(s.Policy.PasswordPolicy, "new_password", next); err != nil {
		return err
	}
	if current == next {
		return apperrors.InvalidRequest("The new password must differ from the current one.")
	}
	acc, err := s.q.GetAccount(ctx, s.Pool, subj.Account.AccountID)
	if err != nil {
		return err
	}
	if err := s.checkLock(acc); err != nil {
		return err
	}
	if _, err := s.Passwords.Verify(ctx, current, acc.PasswordHash); errors.Is(err, passwords.ErrMismatch) {
		return s.recordFailure(ctx, acc, "PASSWORD_CHANGE_FAILED", ci)
	} else if err != nil {
		return err
	}
	hash, err := s.Passwords.Hash(ctx, next)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		now := s.now()
		upd, err := s.q.UpdatePassword(ctx, tx, db.UpdatePasswordParams{AccountID: acc.AccountID, PasswordHash: hash, PasswordChangedAt: now})
		if err != nil {
			return err
		}
		keep := subj.Account.SessionID
		n, err := s.q.RevokeAccountSessions(ctx, tx, db.RevokeAccountSessionsParams{Now: now, Reason: sessions.ReasonPasswordChange, AccountID: acc.AccountID, KeepSessionID: &keep})
		if err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, acc.AccountID, "PASSWORD_CHANGED", keep, ids.Nil, ci, map[string]any{"other_sessions_revoked": n}); err != nil {
			return err
		}
		if err := s.auditTx(ctx, tx, "owner.account.password_changed", acc.AccountID, audit.ResultSuccess, map[string]any{"sessions_revoked": n}, nil); err != nil {
			return err
		}
		return s.enqueueAlert(ctx, tx, upd, "PASSWORD_CHANGED", "")
	})
}

// ForgotPassword starts recovery. The response never reveals whether an
// account exists; the code is dispatched in the background.
func (s *Service) ForgotPassword(ctx context.Context, email, phone string) (ChallengeView, error) {
	var acc db.OwnerAccount
	var err error
	channel, dest := notification.ChannelEmail, NormalizeEmail(email)
	switch {
	case email != "" && phone == "":
		acc, err = s.q.GetOpenAccountByEmail(ctx, s.Pool, dest)
	case phone != "" && email == "":
		if !e164Pattern.MatchString(phone) {
			return ChallengeView{}, apperrors.InvalidRequest("phone must be E.164.")
		}
		channel, dest = notification.ChannelSMS, phone
		acc, err = s.q.GetOpenAccountByVerifiedPhone(ctx, s.Pool, phone)
	default:
		return ChallengeView{}, apperrors.InvalidRequest("Provide exactly one of email or phone.")
	}
	decoy := ChallengeView{ChallengeID: ids.New(), Channel: channel, ExpiresAt: s.now().Add(s.Policy.CodeTTL)}
	if channel == notification.ChannelEmail {
		decoy.DestinationHint = maskEmail(dest)
	} else {
		decoy.DestinationHint = maskPhone(dest)
	}
	if isNoRows(err) {
		return decoy, nil
	}
	if err != nil {
		return ChallengeView{}, err
	}
	if !domain.AccountStatus(acc.Status).CanAuthenticate() {
		return decoy, nil
	}
	var chv ChallengeView
	var code string
	err = s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		chv, code, err = s.issueChallenge(ctx, tx, newChallenge{accountID: acc.AccountID, purpose: PurposePasswordReset, channel: channel,
			destination: dest, ttl: s.Policy.CodeTTL, rateLimited: true})
		if err != nil {
			return err
		}
		return s.securityEvent(ctx, tx, acc.AccountID, "PASSWORD_RESET_REQUESTED", ids.Nil, ids.Nil, ClientInfo{}, map[string]any{"channel": channel})
	})
	var ae *apperrors.Error
	if errors.As(err, &ae) && ae.Code == "VERIFICATION_CODE_LIMIT" {
		return decoy, nil
	}
	if err != nil {
		return ChallengeView{}, err
	}
	sent := chv // the background delivery owns its copy
	s.background(ctx, func(ctx context.Context) { _ = s.deliverCode(ctx, acc.AccountID, &sent, dest, code) })
	chv.DeliveryStatus = ""
	return chv, nil
}

// ResetPassword completes recovery: new password, every session revoked,
// lockout cleared. MFA still applies at the next sign-in.
func (s *Service) ResetPassword(ctx context.Context, challengeID ids.ID, code, next string, ci ClientInfo) error {
	if err := validatePassword(s.Policy.PasswordPolicy, "new_password", next); err != nil {
		return err
	}
	hash, err := s.Passwords.Hash(ctx, next)
	if err != nil {
		return err
	}
	return s.txKeepingAttempts(ctx, func(ctx context.Context, tx pgx.Tx) error {
		ch, err := s.checkChallenge(ctx, tx, challengeID, code, PurposePasswordReset)
		if err != nil {
			return err
		}
		acc, err := s.lockAccount(ctx, tx, ch.AccountID)
		if err != nil {
			return err
		}
		if !domain.AccountStatus(acc.Status).CanAuthenticate() {
			return domain.ErrAccountUnavailable()
		}
		ctx = withAccountActor(ctx, acc.AccountID)
		if err := s.consumeChallenge(ctx, tx, ch.ChallengeID); err != nil {
			return err
		}
		now := s.now()
		upd, err := s.q.UpdatePassword(ctx, tx, db.UpdatePasswordParams{AccountID: acc.AccountID, PasswordHash: hash, PasswordChangedAt: now})
		if err != nil {
			return err
		}
		n, err := s.q.RevokeAccountSessions(ctx, tx, db.RevokeAccountSessionsParams{Now: now, Reason: sessions.ReasonPasswordChange, AccountID: acc.AccountID})
		if err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, acc.AccountID, "PASSWORD_RESET", ids.Nil, ids.Nil, ci, map[string]any{"sessions_revoked": n}); err != nil {
			return err
		}
		if err := s.auditTx(ctx, tx, "owner.account.password_reset", acc.AccountID, audit.ResultSuccess, map[string]any{"sessions_revoked": n}, nil); err != nil {
			return err
		}
		return s.enqueueAlert(ctx, tx, upd, "PASSWORD_RESET", "")
	})
}
