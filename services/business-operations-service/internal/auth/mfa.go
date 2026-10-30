package auth

import (
	"context"
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth/credentials"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/sessions"
	"github.com/jackc/pgx/v5"
)

func nullableID(id ids.ID) *ids.ID {
	if id.IsZero() {
		return nil
	}
	return &id
}
func invalidMFA() error {
	return apperrors.New(401, "MFA_VERIFICATION_FAILED", "MFA verification failed.")
}
func (s *Service) mfaReady() error {
	if s.Sealer == nil {
		return apperrors.Unavailable("MFA protected storage is unavailable.")
	}
	return nil
}

type Enrolment struct {
	MethodID        ids.ID    `json:"method_id"`
	Secret          string    `json:"secret"`
	ProvisioningURI string    `json:"provisioning_uri"`
	ExpiresAt       time.Time `json:"expires_at"`
}
type mfaMethod struct {
	id          ids.ID
	account     ids.ID
	status, key string
	cipher      []byte
	last        int64
	expiry      *time.Time
}

func loadMethod(ctx context.Context, tx pgx.Tx, account ids.ID, status string) (mfaMethod, error) {
	var m mfaMethod
	err := tx.QueryRow(ctx, `SELECT id,account_id,status,key_id,encrypted_secret,last_step,expires_at FROM business_mfa_methods WHERE account_id=$1 AND status=$2 FOR UPDATE`, account, status).Scan(&m.id, &m.account, &m.status, &m.key, &m.cipher, &m.last, &m.expiry)
	return m, err
}
func aad(account, method ids.ID) []byte { return []byte(account.String() + ":" + method.String()) }
func (s *Service) checkPassword(ctx context.Context, tx pgx.Tx, account ids.ID, password string) error {
	var hash, status string
	if err := tx.QueryRow(ctx, `SELECT c.password_hash,a.status FROM business_credentials c JOIN business_accounts a ON a.id=c.account_id WHERE a.id=$1`, account).Scan(&hash, &status); err != nil {
		return err
	}
	if _, err := s.Passwords.Verify(ctx, password, hash); err != nil || status != "ACTIVE" {
		return apperrors.New(401, "INVALID_CREDENTIALS", "Invalid credentials.")
	}
	return nil
}
func (s *Service) liveSession(ctx context.Context, tx pgx.Tx, account, session ids.ID) (db.BusinessSession, error) {
	v, err := s.q.GetLiveSession(ctx, tx, db.GetLiveSessionParams{ID: session, AccountID: account})
	if errors.Is(err, pgx.ErrNoRows) {
		return v, apperrors.InvalidToken()
	}
	return v, err
}

func (s *Service) EnrolTOTP(ctx context.Context, account, session ids.ID, password string) (Enrolment, error) {
	if err := s.mfaReady(); err != nil {
		return Enrolment{}, err
	}
	if err := s.limit(ctx, "mfa-enrol:"+account.String(), 5); err != nil {
		return Enrolment{}, err
	}
	result := Enrolment{MethodID: ids.New(), ExpiresAt: s.Now().UTC().Add(10 * time.Minute)}
	err := database.WithTx(ctx, s.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.liveSession(ctx, tx, account, session); err != nil {
			return err
		}
		// Serialize enrolment across sessions without trusting a device identifier.
		if _, err := tx.Exec(ctx, `SELECT id FROM business_accounts WHERE id=$1 FOR UPDATE`, account); err != nil {
			return err
		}
		if err := s.checkPassword(ctx, tx, account, password); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE business_mfa_methods SET status='REMOVED',removed_at=$2,encrypted_secret=''::bytea WHERE account_id=$1 AND status='PENDING' AND expires_at<=$2`, account, s.Now().UTC()); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM business_mfa_methods WHERE account_id=$1 AND status IN ('ACTIVE','PENDING'))`, account).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return apperrors.Conflict("MFA_METHOD_EXISTS", "An active or pending MFA method already exists.")
		}
		totp := credentials.TOTP{Issuer: "CosmicForge Business"}
		secret := totp.NewSecret()
		encrypted := s.Sealer.Seal(secret, aad(account, result.MethodID))
		if _, err := tx.Exec(ctx, `INSERT INTO business_mfa_methods(id,account_id,kind,status,key_id,encrypted_secret,expires_at) VALUES($1,$2,'TOTP','PENDING',$3,$4,$5)`, result.MethodID, account, s.Sealer.KeyID(), encrypted, result.ExpiresAt); err != nil {
			return err
		}
		result.Secret = totp.EncodeSecret(secret)
		result.ProvisioningURI = totp.URI(secret, account.String())
		return s.record(ctx, tx, &account, "MFA_ENROLMENT_STARTED", result.MethodID)
	})
	return result, err
}

// ConfirmTOTP returns recovery codes once, after proof of possession. No later
// method/status response returns either the seed or these raw codes.
func (s *Service) ConfirmTOTP(ctx context.Context, account, session ids.ID, code string) ([]string, error) {
	if err := s.mfaReady(); err != nil {
		return nil, err
	}
	if err := s.limit(ctx, "mfa-confirm:"+account.String(), 5); err != nil {
		return nil, err
	}
	var recovery []string
	err := database.WithTx(ctx, s.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		recovery = nil
		if _, err := s.liveSession(ctx, tx, account, session); err != nil {
			return err
		}
		m, err := loadMethod(ctx, tx, account, "PENDING")
		if errors.Is(err, pgx.ErrNoRows) {
			return invalidMFA()
		}
		if err != nil {
			return err
		}
		now := s.Now().UTC()
		if m.expiry == nil || !now.Before(*m.expiry) {
			return invalidMFA()
		}
		secret, err := s.Sealer.Open(m.key, m.cipher, aad(account, m.id))
		if err != nil {
			return err
		}
		step, err := (credentials.TOTP{}).Verify(secret, code, now)
		if err != nil || step <= m.last {
			return invalidMFA()
		}
		if _, err := tx.Exec(ctx, `UPDATE business_mfa_methods SET status='ACTIVE',confirmed_at=$2,last_step=$3,expires_at=NULL WHERE id=$1`, m.id, now, step); err != nil {
			return err
		}
		for i := 0; i < 8; i++ {
			raw := credentials.RecoveryCode()
			if _, err := tx.Exec(ctx, `INSERT INTO business_mfa_recovery_codes(id,method_id,code_hash) VALUES($1,$2,$3)`, ids.New(), m.id, s.hash("recovery:"+m.id.String(), credentials.NormalizeRecoveryCode(raw))); err != nil {
				return err
			}
			recovery = append(recovery, raw)
		}
		if _, err := tx.Exec(ctx, `UPDATE business_sessions SET mfa_at=$2 WHERE id=$1`, session, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE business_sessions SET revoked_at=$3,revoke_reason='MFA_CHANGED' WHERE account_id=$1 AND id<>$2 AND revoked_at IS NULL`, account, session, now); err != nil {
			return err
		}
		return s.record(ctx, tx, &account, "MFA_CONFIRMED", m.id)
	})
	if err != nil {
		return nil, err
	}
	return recovery, nil
}

func (s *Service) verifyCode(ctx context.Context, tx pgx.Tx, account ids.ID, code string, backup bool) error {
	m, err := loadMethod(ctx, tx, account, "ACTIVE")
	if errors.Is(err, pgx.ErrNoRows) {
		return invalidMFA()
	}
	if err != nil {
		return err
	}
	now := s.Now().UTC()
	if backup {
		tag, err := tx.Exec(ctx, `UPDATE business_mfa_recovery_codes SET consumed_at=$3 WHERE method_id=$1 AND code_hash=$2 AND consumed_at IS NULL`, m.id, s.hash("recovery:"+m.id.String(), credentials.NormalizeRecoveryCode(code)), now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return invalidMFA()
		}
		return nil
	}
	secret, err := s.Sealer.Open(m.key, m.cipher, aad(account, m.id))
	if err != nil {
		return err
	}
	step, err := (credentials.TOTP{}).Verify(secret, code, now)
	if err != nil || step <= m.last {
		return invalidMFA()
	}
	_, err = tx.Exec(ctx, `UPDATE business_mfa_methods SET last_step=$2 WHERE id=$1`, m.id, step)
	return err
}

func (s *Service) StepUp(ctx context.Context, account, session ids.ID, code string, backup bool) error {
	if err := s.mfaReady(); err != nil {
		return err
	}
	if err := s.limit(ctx, "mfa-step-up:"+account.String(), 10); err != nil {
		return err
	}
	return database.WithTx(ctx, s.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.liveSession(ctx, tx, account, session); err != nil {
			return err
		}
		if err := s.verifyCode(ctx, tx, account, code, backup); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE business_sessions SET mfa_at=$2 WHERE id=$1`, session, s.Now().UTC()); err != nil {
			return err
		}
		return s.record(ctx, tx, &account, "MFA_STEP_UP", session)
	})
}

func (s *Service) CompleteMFALogin(ctx context.Context, raw, code string, backup bool) (Tokens, error) {
	if err := s.mfaReady(); err != nil {
		return Tokens{}, err
	}
	if len(raw) > 128 || len(code) > 100 {
		return Tokens{}, invalidMFA()
	}
	if err := s.limit(ctx, "mfa-login:"+s.hash("challenge", raw), 10); err != nil {
		return Tokens{}, err
	}
	var result Tokens
	var clientErr error
	err := database.WithTx(ctx, s.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		var id, account ids.ID
		var org *ids.ID
		var expires time.Time
		var consumed *time.Time
		var attempts int
		var name, platformName string
		err := tx.QueryRow(ctx, `SELECT id,account_id,organisation_id,expires_at,consumed_at,attempts,device_name,device_platform FROM business_mfa_challenges WHERE token_hash=$1 AND purpose='LOGIN' FOR UPDATE`, s.hash("mfa-login", raw)).Scan(&id, &account, &org, &expires, &consumed, &attempts, &name, &platformName)
		if errors.Is(err, pgx.ErrNoRows) {
			clientErr = invalidMFA()
			return nil
		}
		if err != nil {
			return err
		}
		now := s.Now().UTC()
		if consumed != nil || attempts >= 5 || !now.Before(expires) {
			clientErr = invalidMFA()
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE business_mfa_challenges SET attempts=attempts+1 WHERE id=$1`, id); err != nil {
			return err
		}
		// A savepoint preserves failed-attempt accounting while rolling back any
		// partial one-time-code state if verification itself fails.
		nested, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		if err := s.verifyCode(ctx, nested, account, code, backup); err != nil {
			_ = nested.Rollback(ctx)
			if errors.As(err, new(*apperrors.Error)) {
				clientErr = invalidMFA()
				return s.record(ctx, tx, &account, "MFA_LOGIN_FAILED", id)
			}
			return err
		}
		if err := nested.Commit(ctx); err != nil {
			return err
		}
		a, err := s.q.GetAccount(ctx, tx, account)
		if err != nil {
			return err
		}
		if a.Status != "ACTIVE" {
			clientErr = invalidMFA()
			return nil
		}
		orgID := ids.Nil
		if org != nil {
			orgID = *org
		}
		result, err = s.start(ctx, tx, account, orgID, name, platformName, &now)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE business_mfa_challenges SET consumed_at=$2 WHERE id=$1`, id, now); err != nil {
			return err
		}
		return s.record(ctx, tx, &account, "MFA_LOGIN_SUCCEEDED", result.SessionID)
	})
	if err != nil {
		return Tokens{}, err
	}
	return result, clientErr
}

func (s *Service) RemoveMFA(ctx context.Context, account, session ids.ID, password string) error {
	if err := s.limit(ctx, "mfa-remove:"+account.String(), 5); err != nil {
		return err
	}
	return database.WithTx(ctx, s.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.liveSession(ctx, tx, account, session)
		if err != nil {
			return err
		}
		now := s.Now().UTC()
		if current.MfaAt == nil || current.MfaAt.After(now) || now.Sub(*current.MfaAt) >= 5*time.Minute {
			return apperrors.PermissionDenied("Recent MFA is required.")
		}
		if err := s.checkPassword(ctx, tx, account, password); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE business_mfa_methods SET status='REMOVED',removed_at=$2,encrypted_secret=''::bytea WHERE account_id=$1 AND status IN ('ACTIVE','PENDING')`, account, now); err != nil {
			return err
		}
		if err := sessions.NewManager(&sessionStore{q: s.q, tx: tx}, sessions.Config{Now: s.Now}).RevokeAll(ctx, account, "MFA_REMOVED"); err != nil {
			return err
		}
		return s.record(ctx, tx, &account, "MFA_REMOVED", account)
	})
}
