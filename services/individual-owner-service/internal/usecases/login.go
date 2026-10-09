package usecases

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/credentials"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/passwords"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/sessions"
)

// Authentication methods.
const (
	AuthPassword = "PASSWORD"
	AuthPhoneOTP = "PHONE_OTP"

	MFATOTP     = "TOTP"
	MFARecovery = "RECOVERY_CODE"
	MFASMS      = "SMS"
	MFAEmail    = "EMAIL"
)

type loginMethod struct {
	auth string
	mfa  string
}

func validPlatform(p string) string {
	switch p {
	case "IOS", "ANDROID", "WEB", "OTHER":
		return p
	}
	return "OTHER"
}

// startSessionTx records the device, detects unrecognized devices, starts a
// refresh-session family and mints an access token — all in tx.
func (s *Service) startSessionTx(ctx context.Context, tx pgx.Tx, acc db.OwnerAccount, m loginMethod, ci ClientInfo) (AuthResult, error) {
	now := s.now()
	ctx = withAccountActor(ctx, acc.AccountID)
	if err := s.q.ResetLoginFailures(ctx, tx, db.ResetLoginFailuresParams{AccountID: acc.AccountID, UpdatedAt: now}); err != nil {
		return AuthResult{}, err
	}
	known, err := s.q.CountKnownDevices(ctx, tx, acc.AccountID)
	if err != nil {
		return AuthResult{}, err
	}
	var device *db.AuthDevice
	recognized := false
	if ci.DeviceKey != "" {
		key := s.Hasher.Hash("DEVICE", acc.AccountID.String(), ci.DeviceKey)
		d, err := s.q.GetDeviceByKey(ctx, tx, db.GetDeviceByKeyParams{AccountID: acc.AccountID, DeviceKeyHash: key})
		switch {
		case err == nil:
			recognized = d.RevokedAt == nil
			d, err = s.q.TouchDevice(ctx, tx, db.TouchDeviceParams{DeviceID: d.DeviceID, LastSeenAt: now, LastIp: ci.IP,
				UserAgent: strp(truncate(ci.UserAgent, 512)), Name: strp(truncate(ci.DeviceName, 120)), Platform: validPlatform(ci.Platform)})
			if err != nil {
				return AuthResult{}, err
			}
		case isNoRows(err):
			d, err = s.q.InsertDevice(ctx, tx, db.InsertDeviceParams{DeviceID: ids.New(), AccountID: acc.AccountID, DeviceKeyHash: key,
				Name: strp(truncate(ci.DeviceName, 120)), Platform: validPlatform(ci.Platform), UserAgent: strp(truncate(ci.UserAgent, 512)),
				LastIp: ci.IP, FirstSeenAt: now})
			if err != nil {
				return AuthResult{}, err
			}
		default:
			return AuthResult{}, err
		}
		device = &d
	}
	// Suspicious-device rule: once an account has known devices, a sign-in
	// from an unknown, revoked or unidentified device is alerted.
	suspicious := known > 0 && !recognized
	create := &db.CreateSessionParams{AuthMethod: m.auth, MfaMethod: strp(m.mfa), AuthenticatedAt: now,
		UserAgent: strp(truncate(ci.UserAgent, 512)), IpAddress: ci.IP, StepUpAt: &now}
	stepMethod := MFASMS
	if m.auth == AuthPassword {
		stepMethod = AuthPassword
	}
	if m.mfa != "" {
		stepMethod = m.mfa
	}
	create.StepUpMethod = &stepMethod
	var devID ids.ID
	if device != nil {
		devID = device.DeviceID
		create.DeviceID, create.DeviceName, create.DevicePlatform = &device.DeviceID, device.Name, &device.Platform
	}
	issued, err := s.sessionManager(tx, create).Start(ctx, acc.AccountID, sessions.Device{})
	if err != nil {
		return AuthResult{}, err
	}
	tok, claims, err := s.Issuer.Issue(auth.IssueRequest{Subject: acc.AccountID, SessionID: issued.Session.ID})
	if err != nil {
		return AuthResult{}, err
	}
	md := map[string]any{"auth_method": m.auth, "recognized_device": recognized}
	if m.mfa != "" {
		md["mfa_method"] = m.mfa
	}
	if err := s.securityEvent(ctx, tx, acc.AccountID, "LOGIN_SUCCEEDED", issued.Session.ID, devID, ci, md); err != nil {
		return AuthResult{}, err
	}
	if err := s.auditTx(ctx, tx, "owner.account.session_started", acc.AccountID, audit.ResultSuccess, nil, md); err != nil {
		return AuthResult{}, err
	}
	if suspicious {
		if err := s.securityEvent(ctx, tx, acc.AccountID, "NEW_DEVICE_LOGIN", issued.Session.ID, devID, ci, nil); err != nil {
			return AuthResult{}, err
		}
		if err := s.enqueueAlert(ctx, tx, acc, "NEW_DEVICE_LOGIN", ""); err != nil {
			return AuthResult{}, err
		}
	}
	view := accountView(acc)
	exp := claims.ExpiresAt.Time()
	return AuthResult{Status: "AUTHENTICATED", Account: &view, Tokens: &TokenPair{TokenType: "Bearer", AccessToken: tok,
		AccessTokenExpiresAt: exp, ExpiresIn: int(exp.Sub(now).Seconds()), RefreshToken: issued.RawToken,
		RefreshTokenExpiresAt: issued.TokenExpiresAt, SessionID: issued.Session.ID}}, nil
}

// mfaMethods lists second factors usable by acc for a first factor.
func (s *Service) mfaMethods(ctx context.Context, q db.DBTX, acc db.OwnerAccount, firstFactor string) ([]string, error) {
	var out []string
	if _, err := s.q.GetTOTPCredential(ctx, q, db.GetTOTPCredentialParams{AccountID: acc.AccountID, Status: "ACTIVE"}); err == nil {
		out = append(out, MFATOTP)
	} else if !isNoRows(err) {
		return nil, err
	}
	n, err := s.q.CountUsableRecoveryCodes(ctx, q, acc.AccountID)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		out = append(out, MFARecovery)
	}
	if slices.Contains(s.Policy.MFAFallbackMethods, MFASMS) && acc.PhoneVerifiedAt != nil && firstFactor != AuthPhoneOTP {
		out = append(out, MFASMS)
	}
	if slices.Contains(s.Policy.MFAFallbackMethods, MFAEmail) && acc.EmailVerifiedAt != nil {
		out = append(out, MFAEmail)
	}
	return out, nil
}

// beginMFA creates the LOGIN_MFA challenge after a valid first factor.
func (s *Service) beginMFA(ctx context.Context, tx pgx.Tx, acc db.OwnerAccount, firstFactor string) (AuthResult, error) {
	methods, err := s.mfaMethods(ctx, tx, acc, firstFactor)
	if err != nil {
		return AuthResult{}, err
	}
	if len(methods) == 0 {
		return AuthResult{}, apperrors.Conflict("MFA_UNAVAILABLE", "No second factor is available. Use account recovery.")
	}
	chv, secret, err := s.issueChallenge(ctx, tx, newChallenge{accountID: acc.AccountID, purpose: PurposeLoginMFA, channel: "NONE",
		ttl: s.Policy.MFALoginTTL, maxAttempts: s.Policy.MFALoginMaxAttempts, context: map[string]any{"auth_method": firstFactor, "methods": methods}})
	if err != nil {
		return AuthResult{}, err
	}
	if err := s.securityEvent(ctx, tx, acc.AccountID, "MFA_CHALLENGED", ids.Nil, ids.Nil, ClientInfo{}, map[string]any{"auth_method": firstFactor}); err != nil {
		return AuthResult{}, err
	}
	return AuthResult{Status: "MFA_REQUIRED", MFA: &MFAChallengeView{MFAToken: chv.ChallengeID.String() + "." + secret, Methods: methods, ExpiresAt: chv.ExpiresAt}}, nil
}

// Login authenticates email + password.
func (s *Service) Login(ctx context.Context, email, password string, ci ClientInfo) (AuthResult, error) {
	email = NormalizeEmail(email)
	if email == "" || password == "" {
		return AuthResult{}, apperrors.InvalidRequest("email and password are required.")
	}
	acc, err := s.q.GetOpenAccountByEmail(ctx, s.Pool, email)
	if isNoRows(err) {
		_, _ = s.Passwords.Verify(ctx, password, s.dummyPW) // equalize timing
		return AuthResult{}, domain.ErrInvalidCredentials()
	}
	if err != nil {
		return AuthResult{}, err
	}
	if err := s.checkLock(acc); err != nil {
		return AuthResult{}, err
	}
	rehash, err := s.Passwords.Verify(ctx, password, acc.PasswordHash)
	if errors.Is(err, passwords.ErrMismatch) {
		return AuthResult{}, s.recordFailure(ctx, acc, "LOGIN_FAILED", ci)
	}
	if err != nil {
		return AuthResult{}, err
	}
	if !domain.AccountStatus(acc.Status).CanAuthenticate() {
		return AuthResult{}, s.denyUnavailable(ctx, acc, ci)
	}
	if rehash {
		if h, herr := s.Passwords.Hash(ctx, password); herr == nil {
			_ = s.q.RehashPassword(ctx, s.Pool, db.RehashPasswordParams{AccountID: acc.AccountID, PasswordHash: h, UpdatedAt: s.now()})
		}
	}
	var res AuthResult
	err = s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		locked, err := s.lockAccount(ctx, tx, acc.AccountID)
		if err != nil {
			return err
		}
		if locked.MfaEnabled {
			res, err = s.beginMFA(ctx, tx, locked, AuthPassword)
			return err
		}
		res, err = s.startSessionTx(ctx, tx, locked, loginMethod{auth: AuthPassword}, ci)
		return err
	})
	return res, err
}

func (s *Service) checkLock(acc db.OwnerAccount) error {
	if acc.LockedUntil != nil && s.now().Before(*acc.LockedUntil) {
		return domain.ErrAccountLocked(int(acc.LockedUntil.Sub(s.now()).Seconds()) + 1)
	}
	return nil
}

// recordFailure counts a failed credential check and locks the account at
// the policy threshold. It always returns INVALID_CREDENTIALS.
func (s *Service) recordFailure(ctx context.Context, acc db.OwnerAccount, eventType string, ci ClientInfo) error {
	now := s.now()
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		r, err := s.q.RecordLoginFailure(ctx, tx, db.RecordLoginFailureParams{Now: now, MaxFailures: int32(s.Policy.MaxLoginFailures),
			LockUntil: now.Add(s.Policy.LockoutDuration), AccountID: acc.AccountID})
		if err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, acc.AccountID, eventType, ids.Nil, ids.Nil, ci, map[string]any{"failures": r.FailedLoginCount}); err != nil {
			return err
		}
		if err := s.auditTx(ctx, tx, "owner.account.authentication_failed", acc.AccountID, audit.ResultDenied, nil,
			map[string]any{"event": eventType, "failures": r.FailedLoginCount}); err != nil {
			return err
		}
		if r.LockedUntil != nil && r.LockedUntil.After(now) && int(r.FailedLoginCount) == s.Policy.MaxLoginFailures {
			if err := s.securityEvent(ctx, tx, acc.AccountID, "ACCOUNT_LOCKED", ids.Nil, ids.Nil, ci, map[string]any{"until": r.LockedUntil}); err != nil {
				return err
			}
			return s.enqueueAlert(ctx, tx, acc, "ACCOUNT_LOCKED", "")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return domain.ErrInvalidCredentials()
}

func (s *Service) denyUnavailable(ctx context.Context, acc db.OwnerAccount, ci ClientInfo) error {
	_ = s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return s.securityEvent(ctx, tx, acc.AccountID, "LOGIN_BLOCKED_ACCOUNT_STATUS", ids.Nil, ids.Nil, ci, map[string]any{"status": acc.Status})
	})
	return domain.ErrAccountUnavailable()
}

// RequestLoginOTP starts phone sign-in. The response never reveals whether
// the phone belongs to an account: a challenge ID is always returned and the
// SMS is dispatched in the background.
func (s *Service) RequestLoginOTP(ctx context.Context, phone string) (ChallengeView, error) {
	if !e164Pattern.MatchString(phone) {
		return ChallengeView{}, apperrors.InvalidRequest("phone must be E.164.", apperrors.Detail{Field: "phone", Code: "INVALID_PHONE", Message: "must be E.164"})
	}
	decoy := ChallengeView{ChallengeID: ids.New(), Channel: notification.ChannelSMS, DestinationHint: maskPhone(phone), ExpiresAt: s.now().Add(s.Policy.CodeTTL)}
	acc, err := s.q.GetOpenAccountByVerifiedPhone(ctx, s.Pool, phone)
	if isNoRows(err) {
		return decoy, nil
	}
	if err != nil {
		return ChallengeView{}, err
	}
	if !domain.AccountStatus(acc.Status).CanAuthenticate() || s.checkLock(acc) != nil {
		return decoy, nil
	}
	var chv ChallengeView
	var code string
	err = s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		chv, code, err = s.issueChallenge(ctx, tx, newChallenge{accountID: acc.AccountID, purpose: PurposeLoginOTP, channel: notification.ChannelSMS,
			destination: phone, ttl: s.Policy.CodeTTL, rateLimited: true})
		return err
	})
	var ae *apperrors.Error
	if errors.As(err, &ae) && ae.Code == "VERIFICATION_CODE_LIMIT" {
		return decoy, nil // do not reveal the limit (and thus the account) to the requester
	}
	if err != nil {
		return ChallengeView{}, err
	}
	sent := chv // the background delivery owns its copy
	s.background(ctx, func(ctx context.Context) { _ = s.deliverCode(ctx, acc.AccountID, &sent, phone, code) })
	chv.DeliveryStatus = ""
	return chv, nil
}

// VerifyLoginOTP completes phone sign-in (then MFA if enabled).
func (s *Service) VerifyLoginOTP(ctx context.Context, challengeID ids.ID, code string, ci ClientInfo) (AuthResult, error) {
	var res AuthResult
	err := s.txKeepingAttempts(ctx, func(ctx context.Context, tx pgx.Tx) error {
		ch, err := s.checkChallenge(ctx, tx, challengeID, code, PurposeLoginOTP)
		if err != nil {
			return err
		}
		acc, err := s.lockAccount(ctx, tx, ch.AccountID)
		if err != nil {
			return err
		}
		if acc.PhoneVerifiedAt == nil || acc.PhoneE164 == nil || ch.Destination == nil || *acc.PhoneE164 != *ch.Destination {
			return domain.ErrInvalidCode()
		}
		if !domain.AccountStatus(acc.Status).CanAuthenticate() {
			return domain.ErrAccountUnavailable()
		}
		if err := s.checkLock(acc); err != nil {
			return err
		}
		if err := s.consumeChallenge(ctx, tx, ch.ChallengeID); err != nil {
			return err
		}
		if acc.MfaEnabled {
			res, err = s.beginMFA(ctx, tx, acc, AuthPhoneOTP)
			return err
		}
		res, err = s.startSessionTx(ctx, tx, acc, loginMethod{auth: AuthPhoneOTP}, ci)
		return err
	})
	return res, err
}

func parseMFAToken(tok string) (ids.ID, string, error) {
	id, secret, ok := strings.Cut(tok, ".")
	cid, err := ids.ParseV7(id)
	if !ok || err != nil || secret == "" {
		return ids.Nil, "", apperrors.Unprocessable("INVALID_MFA_TOKEN", "The MFA token is invalid or expired.")
	}
	return cid, secret, nil
}

// SendMFACode sends an SMS/EMAIL second-factor code for a pending sign-in.
func (s *Service) SendMFACode(ctx context.Context, mfaToken, method string) (ChallengeView, error) {
	cid, secret, err := parseMFAToken(mfaToken)
	if err != nil {
		return ChallengeView{}, err
	}
	if method != MFASMS && method != MFAEmail {
		return ChallengeView{}, apperrors.InvalidRequest("method must be SMS or EMAIL.")
	}
	var chv ChallengeView
	var code, dest string
	var accountID ids.ID
	err = s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		parent, err := s.checkChallenge(ctx, tx, cid, secret, PurposeLoginMFA)
		if errors.Is(err, errAttemptRecorded) {
			return apperrors.Unprocessable("INVALID_MFA_TOKEN", "The MFA token is invalid or expired.")
		}
		if err != nil {
			return apperrors.Unprocessable("INVALID_MFA_TOKEN", "The MFA token is invalid or expired.")
		}
		if !slices.Contains(contextMethods(parent), method) {
			return apperrors.Unprocessable("MFA_METHOD_UNAVAILABLE", "This second factor is not available for this sign-in.")
		}
		acc, err := s.lockAccount(ctx, tx, parent.AccountID)
		if err != nil {
			return err
		}
		accountID = acc.AccountID
		channel := notification.ChannelEmail
		dest = acc.Email
		if method == MFASMS {
			channel, dest = notification.ChannelSMS, *acc.PhoneE164
		}
		chv, code, err = s.issueChallenge(ctx, tx, newChallenge{accountID: acc.AccountID, parentID: parent.ChallengeID, purpose: PurposeMFACode,
			channel: channel, destination: dest, ttl: s.Policy.CodeTTL, rateLimited: true})
		return err
	})
	if err != nil {
		return ChallengeView{}, err
	}
	return chv, s.deliverCode(ctx, accountID, &chv, dest, code)
}

func contextMethods(ch db.AuthChallenge) []string {
	var out []string
	if ms, ok := challengeContext(ch)["methods"].([]any); ok {
		for _, m := range ms {
			if str, ok := m.(string); ok {
				out = append(out, str)
			}
		}
	}
	return out
}

// VerifyMFA completes a sign-in with a second factor. Failed attempts are
// counted on the sign-in challenge (bounded) and on the account lockout.
func (s *Service) VerifyMFA(ctx context.Context, mfaToken, method, code string, ci ClientInfo) (AuthResult, error) {
	cid, secret, err := parseMFAToken(mfaToken)
	if err != nil {
		return AuthResult{}, err
	}
	var res AuthResult
	var failedAcc *db.OwnerAccount
	err = s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		failedAcc = nil
		parent, err := s.checkChallenge(ctx, tx, cid, secret, PurposeLoginMFA)
		if err != nil {
			if errors.Is(err, errAttemptRecorded) {
				return nil // commit the attempt; reported below
			}
			return apperrors.Unprocessable("INVALID_MFA_TOKEN", "The MFA token is invalid or expired.")
		}
		if !slices.Contains(contextMethods(parent), method) {
			return apperrors.Unprocessable("MFA_METHOD_UNAVAILABLE", "This second factor is not available for this sign-in.")
		}
		acc, err := s.lockAccount(ctx, tx, parent.AccountID)
		if err != nil {
			return err
		}
		if !domain.AccountStatus(acc.Status).CanAuthenticate() {
			return domain.ErrAccountUnavailable()
		}
		if err := s.checkLock(acc); err != nil {
			return err
		}
		ok, err := s.verifySecondFactor(ctx, tx, acc, parent.ChallengeID, method, code)
		if err != nil {
			return err
		}
		if !ok {
			if err := s.q.RecordChallengeAttempt(ctx, tx, db.RecordChallengeAttemptParams{Now: s.now(), ChallengeID: parent.ChallengeID}); err != nil {
				return err
			}
			failedAcc = &acc
			return nil
		}
		if err := s.consumeChallenge(ctx, tx, parent.ChallengeID); err != nil {
			return err
		}
		first, _ := challengeContext(parent)["auth_method"].(string)
		res, err = s.startSessionTx(ctx, tx, acc, loginMethod{auth: first, mfa: method}, ci)
		return err
	})
	if err != nil {
		return AuthResult{}, err
	}
	if failedAcc != nil {
		return AuthResult{}, s.recordFailure(ctx, *failedAcc, "MFA_FAILED", ci)
	}
	if res.Status == "" {
		return AuthResult{}, apperrors.Unprocessable("INVALID_MFA_TOKEN", "The MFA token is invalid or expired.")
	}
	return res, nil
}

// verifySecondFactor checks a TOTP, recovery code or delivered code. A
// successful TOTP advances the replay counter; recovery codes are single-use.
func (s *Service) verifySecondFactor(ctx context.Context, tx pgx.Tx, acc db.OwnerAccount, parentID ids.ID, method, code string) (bool, error) {
	now := s.now()
	switch method {
	case MFATOTP:
		cred, err := s.q.GetTOTPCredential(ctx, tx, db.GetTOTPCredentialParams{AccountID: acc.AccountID, Status: "ACTIVE"})
		if isNoRows(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		secret, err := s.Sealer.Open(cred.KeyID, cred.SecretCiphertext, totpAAD(acc.AccountID, cred.CredentialID))
		if err != nil {
			return false, err
		}
		step, verr := s.TOTP.Verify(secret, code, now)
		if verr != nil {
			return false, nil
		}
		n, err := s.q.AdvanceTOTPStep(ctx, tx, db.AdvanceTOTPStepParams{CredentialID: cred.CredentialID, LastUsedStep: &step})
		return n == 1, err // n == 0: replay of an already-used code
	case MFARecovery:
		n, err := s.q.UseRecoveryCode(ctx, tx, db.UseRecoveryCodeParams{AccountID: acc.AccountID,
			CodeHash: s.Hasher.Hash("RECOVERY", acc.AccountID.String(), credentials.NormalizeRecoveryCode(code)), UsedAt: &now})
		if err != nil || n != 1 {
			return false, err
		}
		return true, s.securityEvent(ctx, tx, acc.AccountID, "RECOVERY_CODE_USED", ids.Nil, ids.Nil, ClientInfo{}, nil)
	case MFASMS, MFAEmail:
		child, err := s.q.GetOpenChildChallenge(ctx, tx, db.GetOpenChildChallengeParams{ParentID: &parentID, Purpose: PurposeMFACode})
		if isNoRows(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if (method == MFASMS) != (child.Channel == notification.ChannelSMS) || !now.Before(child.ExpiresAt) {
			return false, nil
		}
		if !credentials.Equal(s.Hasher.Hash(child.Purpose, child.ChallengeID.String(), strings.TrimSpace(code)), child.SecretHash) {
			return false, s.q.RecordChallengeAttempt(ctx, tx, db.RecordChallengeAttemptParams{Now: now, ChallengeID: child.ChallengeID})
		}
		return true, s.consumeChallenge(ctx, tx, child.ChallengeID)
	}
	return false, apperrors.InvalidRequest("method must be TOTP, RECOVERY_CODE, SMS or EMAIL.")
}

func totpAAD(accountID, credentialID ids.ID) []byte {
	return []byte("owner-totp:" + accountID.String() + ":" + credentialID.String())
}

// Refresh rotates a refresh token. Reuse of a rotated token (including a
// lost concurrent race) revokes the whole session family; that revocation is
// committed before TOKEN_REUSE is returned.
func (s *Service) Refresh(ctx context.Context, raw string, ci ClientInfo) (TokenPair, error) {
	var out TokenPair
	var reuse error
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		reuse = nil
		issued, err := s.sessionManager(tx, nil).Refresh(ctx, raw)
		switch {
		case errors.Is(err, sessions.ErrTokenReuse):
			tok, sess, gerr := (&sessionStore{q: s.q, db: tx}).GetByTokenHash(ctx, sessions.HashToken(raw))
			if gerr == nil {
				ctx = withAccountActor(ctx, sess.Subject)
				_ = tok
				if err := s.securityEvent(ctx, tx, sess.Subject, "REFRESH_TOKEN_REUSE", sess.ID, ids.Nil, ci, nil); err != nil {
					return err
				}
				if err := s.auditTx(ctx, tx, "owner.account.refresh_reuse_detected", sess.Subject, audit.ResultDenied, nil, map[string]any{"session_id": sess.ID.String()}); err != nil {
					return err
				}
				if acc, aerr := s.q.GetAccount(ctx, tx, sess.Subject); aerr == nil {
					if err := s.enqueueAlert(ctx, tx, acc, "TOKEN_REUSE", ""); err != nil {
						return err
					}
				}
			}
			reuse = apperrors.New(401, "REFRESH_TOKEN_REUSED", "This refresh token was already used; the session has been ended.")
			return nil
		case errors.Is(err, sessions.ErrInvalidToken), errors.Is(err, sessions.ErrSessionRevoked), errors.Is(err, sessions.ErrSessionExpired):
			return apperrors.New(401, "INVALID_REFRESH_TOKEN", "The refresh token is invalid or expired.")
		case err != nil:
			return err
		}
		acc, err := s.q.GetAccount(ctx, tx, issued.Session.Subject)
		if err != nil {
			return err
		}
		if !domain.AccountStatus(acc.Status).CanAuthenticate() {
			return domain.ErrAccountUnavailable()
		}
		tok, claims, err := s.Issuer.Issue(auth.IssueRequest{Subject: acc.AccountID, SessionID: issued.Session.ID})
		if err != nil {
			return err
		}
		exp := claims.ExpiresAt.Time()
		out = TokenPair{TokenType: "Bearer", AccessToken: tok, AccessTokenExpiresAt: exp, ExpiresIn: int(exp.Sub(s.now()).Seconds()),
			RefreshToken: issued.RawToken, RefreshTokenExpiresAt: issued.TokenExpiresAt, SessionID: issued.Session.ID}
		return nil
	})
	if err != nil {
		return TokenPair{}, err
	}
	if reuse != nil {
		return TokenPair{}, reuse
	}
	return out, nil
}

// Logout ends the current session.
func (s *Service) Logout(ctx context.Context, subj authz.Subject, ci ClientInfo) error {
	if _, err := s.Authz.Require(ctx, subj, authz.ActLogout, authz.Resource{}); err != nil {
		return err
	}
	return s.revokeOwnSession(ctx, subj, subj.Account.SessionID, sessions.ReasonLogout, ci)
}

// LogoutAll ends every session of the account, including the current one.
func (s *Service) LogoutAll(ctx context.Context, subj authz.Subject, ci ClientInfo) (int64, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActLogout, authz.Resource{}); err != nil {
		return 0, err
	}
	var n int64
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		acc, err := s.lockAccount(ctx, tx, subj.Account.AccountID)
		if err != nil {
			return err
		}
		if n, err = s.q.RevokeAccountSessions(ctx, tx, db.RevokeAccountSessionsParams{Now: s.now(), Reason: sessions.ReasonLogoutAll, AccountID: acc.AccountID}); err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, acc.AccountID, "LOGOUT_ALL", subj.Account.SessionID, ids.Nil, ci, map[string]any{"revoked": n}); err != nil {
			return err
		}
		if err := s.auditTx(ctx, tx, "owner.account.logout_all", acc.AccountID, audit.ResultSuccess, map[string]any{"revoked": n}, nil); err != nil {
			return err
		}
		return s.enqueueAlert(ctx, tx, acc, "SESSIONS_REVOKED", "")
	})
	return n, err
}

func (s *Service) revokeOwnSession(ctx context.Context, subj authz.Subject, sessionID ids.ID, reason string, ci ClientInfo) error {
	return s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		sess, err := s.q.GetSession(ctx, tx, db.GetSessionParams{ID: sessionID, AccountID: subj.Account.AccountID})
		if isNoRows(err) {
			return apperrors.NotFound("SESSION_NOT_FOUND", "Session not found.")
		}
		if err != nil {
			return err
		}
		if sess.RevokedAt != nil {
			return nil // idempotent
		}
		now := s.now()
		if _, err := s.q.RevokeSession(ctx, tx, db.RevokeSessionParams{ID: sess.ID, RevokedAt: &now, RevokeReason: &reason}); err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, subj.Account.AccountID, "SESSION_REVOKED", sess.ID, ids.Nil, ci, map[string]any{"reason": reason}); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, "owner.account.session_revoked", subj.Account.AccountID, audit.ResultSuccess, nil,
			map[string]any{"session_id": sess.ID.String(), "reason": reason})
	})
}

// AuthenticateAccess verifies an owner access token issued by this service
// and checks the live session and account state (immediate remote
// revocation). gatewayActor is the actor the gateway asserted; it must equal
// the token subject.
func (s *Service) AuthenticateAccess(ctx context.Context, rawToken string, gatewayActor ids.ID) (authz.Subject, error) {
	p, err := s.Verifier.Verify(ctx, rawToken)
	if err != nil {
		return authz.Subject{}, apperrors.InvalidToken().WithCause(err)
	}
	if p.Domain != platform.DomainIndividualOwner || p.SessionID.IsZero() {
		return authz.Subject{}, apperrors.InvalidToken()
	}
	if gatewayActor != p.ActorID {
		return authz.Subject{}, apperrors.InvalidToken().WithCause(errors.New("gateway actor does not match token subject"))
	}
	row, err := s.q.GetSessionAuth(ctx, s.Pool, p.SessionID)
	if isNoRows(err) {
		return authz.Subject{}, domain.ErrSessionInvalid()
	}
	if err != nil {
		return authz.Subject{}, err
	}
	if row.AccountID != p.ActorID {
		return authz.Subject{}, apperrors.InvalidToken()
	}
	if row.RevokedAt != nil || !s.now().Before(row.ExpiresAt) {
		return authz.Subject{}, domain.ErrSessionInvalid()
	}
	st := domain.AccountStatus(row.AccountStatus)
	if !st.CanAuthenticate() {
		return authz.Subject{}, domain.ErrAccountUnavailable()
	}
	return authz.Subject{Kind: authz.KindOwnerSession, ActorDomain: platform.DomainIndividualOwner, ActorID: p.ActorID,
		Account: &authz.AccountContext{AccountID: row.AccountID, SessionID: row.SessionID, Status: st, StepUpAt: row.StepUpAt, MFAEnabled: row.MfaEnabled}}, nil
}
