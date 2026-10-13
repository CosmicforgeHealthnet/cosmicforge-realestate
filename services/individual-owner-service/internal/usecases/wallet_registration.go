package usecases

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"time"
)

const WalletRegistrationMaxAttempts = 20

type WalletRegistrationClient interface {
	Ensure(context.Context, ids.ID, string) error
}

// SetWalletRegistration is called at startup before workers start.
func (s *Service) SetWalletRegistration(c WalletRegistrationClient) { s.walletRegistration = c }
func (s *Service) DeliverWalletRegistrations(ctx context.Context) error {
	if s.walletRegistration == nil {
		return nil
	}
	token := ids.New()
	rows, e := s.Pool.Query(ctx, `WITH due AS(SELECT d.account_id FROM wallet_registration_delivery d JOIN owner_accounts a USING(account_id) WHERE delivered_at IS NULL AND attempts<20 AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<now()) AND a.status<>'CLOSED' ORDER BY next_attempt_at LIMIT 20 FOR UPDATE OF d SKIP LOCKED) UPDATE wallet_registration_delivery d SET lease_token=$1,lease_until=now()+interval '60 seconds',attempts=attempts+1 FROM due WHERE d.account_id=due.account_id RETURNING d.account_id,d.registration_ref,d.attempts`, token)
	if e != nil {
		return e
	}
	type item struct {
		id       ids.ID
		ref      string
		attempts int
	}
	items := []item{}
	for rows.Next() {
		var x item
		if e = rows.Scan(&x.id, &x.ref, &x.attempts); e != nil {
			rows.Close()
			return e
		}
		items = append(items, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, x := range items {
		cctx, cancel := providerContext(ctx, "wallet registration")
		callErr := s.walletRegistration.Ensure(cctx, x.id, x.ref)
		providerOutcome(cctx, callErr)
		cancel()
		if callErr == nil {
			_, e = s.Pool.Exec(ctx, `UPDATE wallet_registration_delivery SET delivered_at=now(),lease_until=NULL,last_error_code=NULL WHERE account_id=$1 AND lease_token=$2`, x.id, token)
		} else {
			delay := time.Duration(1<<min(x.attempts, 10)) * time.Second
			_, e = s.Pool.Exec(ctx, `UPDATE wallet_registration_delivery SET next_attempt_at=now()+$3::interval,lease_until=NULL,last_error_code=CASE WHEN attempts>=20 THEN 'RETRIES_EXHAUSTED' ELSE 'PAYMENT_WALLET_UNAVAILABLE' END WHERE account_id=$1 AND lease_token=$2`, x.id, token, delay.String())
		}
		if e != nil {
			return e
		}
	}
	return nil
}
