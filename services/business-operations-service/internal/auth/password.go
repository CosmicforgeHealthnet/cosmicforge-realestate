package auth

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/passwords"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/sessions"
	"github.com/jackc/pgx/v5"
	"time"
)

func (s *Service) ChangePassword(ctx context.Context, account, session ids.ID, current, next string) error {
	if err := passwords.DefaultPolicy.Validate(next); err != nil {
		return apperrors.InvalidRequest("The new password does not meet the password policy.")
	}
	if err := s.limit(ctx, "password-change:"+account.String(), 5); err != nil {
		return err
	}
	hash, err := s.Passwords.Hash(ctx, next)
	if err != nil {
		return err
	}
	return database.WithTx(ctx, s.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		live, err := s.liveSession(ctx, tx, account, session)
		if err != nil {
			return err
		}
		var mfa bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM business_mfa_methods WHERE account_id=$1 AND status='ACTIVE')`, account).Scan(&mfa); err != nil {
			return err
		}
		now := s.Now().UTC()
		if mfa && (live.MfaAt == nil || live.MfaAt.After(now) || now.Sub(*live.MfaAt) >= 5*time.Minute) {
			return apperrors.PermissionDenied("Recent MFA is required.")
		}
		var existing string
		if err := tx.QueryRow(ctx, `SELECT password_hash FROM business_credentials WHERE account_id=$1 FOR UPDATE`, account).Scan(&existing); err != nil {
			return err
		}
		if _, err := s.Passwords.Verify(ctx, current, existing); err != nil {
			return apperrors.New(401, "INVALID_CREDENTIALS", "Invalid credentials.")
		}
		if _, err := tx.Exec(ctx, `UPDATE business_credentials SET password_hash=$2,changed_at=$3 WHERE account_id=$1`, account, hash, now); err != nil {
			return err
		}
		if err := sessions.NewManager(&sessionStore{q: s.q, tx: tx}, sessions.Config{Now: s.Now}).RevokeAll(ctx, account, sessions.ReasonPasswordChange); err != nil {
			return err
		}
		return s.record(ctx, tx, &account, "PASSWORD_CHANGED", account)
	})
}
