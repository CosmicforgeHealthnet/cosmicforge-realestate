package usecases

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/credentials"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/passwords"
)

// MFAStatus reports enrolled second factors.
func (s *Service) MFAStatus(ctx context.Context, subj authz.Subject) (MFAStatusView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActMFARead, authz.Resource{}); err != nil {
		return MFAStatusView{}, err
	}
	acc, err := s.q.GetAccount(ctx, s.Pool, subj.Account.AccountID)
	if err != nil {
		return MFAStatusView{}, err
	}
	n, err := s.q.CountUsableRecoveryCodes(ctx, s.Pool, acc.AccountID)
	if err != nil {
		return MFAStatusView{}, err
	}
	fb := []string{}
	if acc.MfaEnabled {
		fb, _ = s.mfaMethods(ctx, s.Pool, acc, AuthPassword)
		fb = filter(fb, func(m string) bool { return m == MFASMS || m == MFAEmail })
	}
	return MFAStatusView{TOTPEnabled: acc.MfaEnabled, RecoveryCodesRemaining: n, FallbackMethods: fb}, nil
}

func filter(in []string, keep func(string) bool) []string {
	out := []string{}
	for _, x := range in {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}

// SetupTOTP creates a PENDING authenticator credential (step-up required).
func (s *Service) SetupTOTP(ctx context.Context, subj authz.Subject) (TOTPSetupView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActMFAEnroll, authz.Resource{}); err != nil {
		return TOTPSetupView{}, err
	}
	var v TOTPSetupView
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		acc, err := s.lockAccount(ctx, tx, subj.Account.AccountID)
		if err != nil {
			return err
		}
		if acc.MfaEnabled {
			return apperrors.Conflict("MFA_ALREADY_ENABLED", "An authenticator app is already enrolled.")
		}
		now := s.now()
		if err := s.q.DiscardPendingTOTP(ctx, tx, db.DiscardPendingTOTPParams{AccountID: acc.AccountID, DisabledAt: &now}); err != nil {
			return err
		}
		id, secret := ids.New(), s.TOTP.NewSecret()
		if err := s.q.CreateTOTPCredential(ctx, tx, db.CreateTOTPCredentialParams{CredentialID: id, AccountID: acc.AccountID,
			SecretCiphertext: s.Sealer.Seal(secret, totpAAD(acc.AccountID, id)), KeyID: s.Sealer.KeyID(), CreatedAt: now}); err != nil {
			return err
		}
		v = TOTPSetupView{CredentialID: id, Secret: s.TOTP.EncodeSecret(secret), OTPAuthURI: s.TOTP.URI(secret, acc.Email)}
		return s.auditTx(ctx, tx, "owner.account.mfa_enrolment_started", acc.AccountID, audit.ResultSuccess, nil, nil)
	})
	return v, err
}

// ConfirmTOTP activates the pending credential with a valid code and returns
// one-time recovery codes.
func (s *Service) ConfirmTOTP(ctx context.Context, subj authz.Subject, code string, ci ClientInfo) (RecoveryCodesView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActMFAEnroll, authz.Resource{}); err != nil {
		return RecoveryCodesView{}, err
	}
	var out RecoveryCodesView
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		acc, err := s.lockAccount(ctx, tx, subj.Account.AccountID)
		if err != nil {
			return err
		}
		cred, err := s.q.GetTOTPCredential(ctx, tx, db.GetTOTPCredentialParams{AccountID: acc.AccountID, Status: "PENDING"})
		if isNoRows(err) {
			return apperrors.Conflict("MFA_SETUP_NOT_STARTED", "Start authenticator setup first.")
		}
		if err != nil {
			return err
		}
		secret, err := s.Sealer.Open(cred.KeyID, cred.SecretCiphertext, totpAAD(acc.AccountID, cred.CredentialID))
		if err != nil {
			return err
		}
		now := s.now()
		step, verr := s.TOTP.Verify(secret, code, now)
		if verr != nil {
			return domain.ErrInvalidCode()
		}
		if n, err := s.q.ActivateTOTP(ctx, tx, db.ActivateTOTPParams{CredentialID: cred.CredentialID, ConfirmedAt: &now, LastUsedStep: &step}); err != nil {
			return err
		} else if n != 1 {
			return domain.ErrVersionConflict()
		}
		upd, err := s.q.SetMFAEnabled(ctx, tx, db.SetMFAEnabledParams{AccountID: acc.AccountID, MfaEnabled: true, UpdatedAt: now})
		if err != nil {
			return err
		}
		if out, err = s.newRecoveryCodes(ctx, tx, acc.AccountID); err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, acc.AccountID, "MFA_ENABLED", subj.Account.SessionID, ids.Nil, ci, map[string]any{"method": MFATOTP}); err != nil {
			return err
		}
		if err := s.auditTx(ctx, tx, "owner.account.mfa_enabled", acc.AccountID, audit.ResultSuccess, map[string]any{"mfa_enabled": true}, nil); err != nil {
			return err
		}
		return s.enqueueAlert(ctx, tx, upd, "MFA_ENABLED", "")
	})
	return out, err
}

func (s *Service) newRecoveryCodes(ctx context.Context, tx pgx.Tx, accountID ids.ID) (RecoveryCodesView, error) {
	now := s.now()
	if err := s.q.InvalidateRecoveryCodes(ctx, tx, db.InvalidateRecoveryCodesParams{AccountID: accountID, InvalidatedAt: &now}); err != nil {
		return RecoveryCodesView{}, err
	}
	batch := ids.New()
	out := RecoveryCodesView{RecoveryCodes: make([]string, 0, s.Policy.RecoveryCodeCount)}
	for i := 0; i < s.Policy.RecoveryCodeCount; i++ {
		c := credentials.RecoveryCode()
		if err := s.q.InsertRecoveryCode(ctx, tx, db.InsertRecoveryCodeParams{CodeID: ids.New(), AccountID: accountID, BatchID: batch,
			CodeHash: s.Hasher.Hash("RECOVERY", accountID.String(), credentials.NormalizeRecoveryCode(c)), CreatedAt: now}); err != nil {
			return RecoveryCodesView{}, err
		}
		out.RecoveryCodes = append(out.RecoveryCodes, c)
	}
	return out, nil
}

// DisableTOTP removes the authenticator and recovery codes (step-up required).
func (s *Service) DisableTOTP(ctx context.Context, subj authz.Subject, ci ClientInfo) error {
	if _, err := s.Authz.Require(ctx, subj, authz.ActMFADisable, authz.Resource{}); err != nil {
		return err
	}
	return s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		acc, err := s.lockAccount(ctx, tx, subj.Account.AccountID)
		if err != nil {
			return err
		}
		if !acc.MfaEnabled {
			return apperrors.Conflict("MFA_NOT_ENABLED", "Two-step verification is not enabled.")
		}
		now := s.now()
		if _, err := s.q.DisableActiveTOTP(ctx, tx, db.DisableActiveTOTPParams{AccountID: acc.AccountID, DisabledAt: &now}); err != nil {
			return err
		}
		if err := s.q.InvalidateRecoveryCodes(ctx, tx, db.InvalidateRecoveryCodesParams{AccountID: acc.AccountID, InvalidatedAt: &now}); err != nil {
			return err
		}
		upd, err := s.q.SetMFAEnabled(ctx, tx, db.SetMFAEnabledParams{AccountID: acc.AccountID, MfaEnabled: false, UpdatedAt: now})
		if err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, acc.AccountID, "MFA_DISABLED", subj.Account.SessionID, ids.Nil, ci, nil); err != nil {
			return err
		}
		if err := s.auditTx(ctx, tx, "owner.account.mfa_disabled", acc.AccountID, audit.ResultSuccess, map[string]any{"mfa_enabled": false}, nil); err != nil {
			return err
		}
		return s.enqueueAlert(ctx, tx, upd, "MFA_DISABLED", "")
	})
}

// RegenerateRecoveryCodes replaces all recovery codes (step-up required).
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, subj authz.Subject, ci ClientInfo) (RecoveryCodesView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActRecoveryCodesRegenerate, authz.Resource{}); err != nil {
		return RecoveryCodesView{}, err
	}
	var out RecoveryCodesView
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		acc, err := s.lockAccount(ctx, tx, subj.Account.AccountID)
		if err != nil {
			return err
		}
		if !acc.MfaEnabled {
			return domain.ErrMFARequired()
		}
		if out, err = s.newRecoveryCodes(ctx, tx, acc.AccountID); err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, acc.AccountID, "RECOVERY_CODES_REGENERATED", subj.Account.SessionID, ids.Nil, ci, nil); err != nil {
			return err
		}
		if err := s.auditTx(ctx, tx, "owner.account.recovery_codes_regenerated", acc.AccountID, audit.ResultSuccess, nil, nil); err != nil {
			return err
		}
		return s.enqueueAlert(ctx, tx, acc, "RECOVERY_CODES", "")
	})
	return out, err
}

// StartStepUpCode sends an SMS/EMAIL code bound to the current session.
func (s *Service) StartStepUpCode(ctx context.Context, subj authz.Subject, method string) (ChallengeView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActStepUp, authz.Resource{}); err != nil {
		return ChallengeView{}, err
	}
	acc, err := s.q.GetAccount(ctx, s.Pool, subj.Account.AccountID)
	if err != nil {
		return ChallengeView{}, err
	}
	channel, dest := "", ""
	switch {
	case method == MFAEmail && acc.EmailVerifiedAt != nil:
		channel, dest = notification.ChannelEmail, acc.Email
	case method == MFASMS && acc.PhoneVerifiedAt != nil:
		channel, dest = notification.ChannelSMS, *acc.PhoneE164
	default:
		return ChallengeView{}, apperrors.Unprocessable("STEP_UP_METHOD_UNAVAILABLE", "This step-up method requires a verified contact.")
	}
	if acc.MfaEnabled && !containsStr(s.Policy.MFAFallbackMethods, method) {
		return ChallengeView{}, apperrors.Unprocessable("STEP_UP_METHOD_UNAVAILABLE", "This step-up method is not permitted by policy.")
	}
	var chv ChallengeView
	var code string
	err = s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		chv, code, err = s.issueChallenge(ctx, tx, newChallenge{accountID: acc.AccountID, sessionID: subj.Account.SessionID, purpose: PurposeStepUpCode,
			channel: channel, destination: dest, ttl: s.Policy.CodeTTL, rateLimited: true})
		return err
	})
	if err != nil {
		return ChallengeView{}, err
	}
	return chv, s.deliverCode(ctx, acc.AccountID, &chv, dest, code)
}

func containsStr(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// StepUpInput re-authenticates the current session.
type StepUpInput struct {
	Method      string // PASSWORD | TOTP | RECOVERY_CODE | SMS | EMAIL
	Password    string
	Code        string
	ChallengeID ids.ID // SMS/EMAIL
}

// StepUp refreshes the session's step-up time. With MFA enabled a second
// factor is required (a password alone is not a step-up).
func (s *Service) StepUp(ctx context.Context, subj authz.Subject, in StepUpInput, ci ClientInfo) (StepUpView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActStepUp, authz.Resource{}); err != nil {
		return StepUpView{}, err
	}
	acc, err := s.q.GetAccount(ctx, s.Pool, subj.Account.AccountID)
	if err != nil {
		return StepUpView{}, err
	}
	if err := s.checkLock(acc); err != nil {
		return StepUpView{}, err
	}
	if acc.MfaEnabled && in.Method == AuthPassword {
		return StepUpView{}, apperrors.Unprocessable("STEP_UP_METHOD_UNAVAILABLE", "Accounts with two-step verification must step up with a second factor.")
	}
	sid := subj.Account.SessionID
	if in.Method == AuthPassword {
		if _, err := s.Passwords.Verify(ctx, in.Password, acc.PasswordHash); errors.Is(err, passwords.ErrMismatch) {
			return StepUpView{}, s.recordFailure(ctx, acc, "STEP_UP_FAILED", ci)
		} else if err != nil {
			return StepUpView{}, err
		}
	}
	var out StepUpView
	var failed bool
	err = s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		failed = false
		ok := in.Method == AuthPassword
		switch in.Method {
		case AuthPassword:
		case MFATOTP, MFARecovery:
			if !acc.MfaEnabled {
				return domain.ErrMFARequired()
			}
			var err error
			if ok, err = s.verifySecondFactor(ctx, tx, acc, ids.Nil, in.Method, in.Code); err != nil {
				return err
			}
		case MFASMS, MFAEmail:
			ch, err := s.checkChallenge(ctx, tx, in.ChallengeID, in.Code, PurposeStepUpCode)
			if errors.Is(err, errAttemptRecorded) {
				failed = true
				return nil
			}
			if err != nil {
				return err
			}
			if ch.SessionID == nil || *ch.SessionID != sid || (in.Method == MFASMS) != (ch.Channel == notification.ChannelSMS) {
				return domain.ErrInvalidCode()
			}
			if err := s.consumeChallenge(ctx, tx, ch.ChallengeID); err != nil {
				return err
			}
			ok = true
		default:
			return apperrors.InvalidRequest("method must be PASSWORD, TOTP, RECOVERY_CODE, SMS or EMAIL.")
		}
		if !ok {
			failed = true
			return nil
		}
		now := s.now()
		m := in.Method
		if err := s.q.SetSessionStepUp(ctx, tx, db.SetSessionStepUpParams{ID: sid, StepUpAt: &now, StepUpMethod: &m}); err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, acc.AccountID, "STEP_UP_SUCCEEDED", sid, ids.Nil, ci, map[string]any{"method": m}); err != nil {
			return err
		}
		out = StepUpView{StepUpAt: now, Method: m, ExpiresAt: now.Add(s.Authz.StepUpWindow())}
		return nil
	})
	if err != nil {
		return StepUpView{}, err
	}
	if failed {
		// A failed second factor counts toward the lockout like a password.
		err := s.recordFailure(ctx, acc, "STEP_UP_FAILED", ci)
		var ae *apperrors.Error
		if errors.As(err, &ae) && ae.Code == "INVALID_CREDENTIALS" {
			return StepUpView{}, domain.ErrInvalidCode()
		}
		return StepUpView{}, err
	}
	return out, nil
}
