package usecases

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type WalletSnapshotClient interface {
	Snapshot(context.Context, ids.ID) (payment.WalletSnapshot, error)
}

func (s *Service) OwnFinancialWallet(ctx context.Context, subj authz.Subject) (payment.WalletSnapshot, error) {
	if _, e := s.Authz.Require(ctx, subj, authz.ActAccountRead, authz.Resource{}); e != nil {
		return payment.WalletSnapshot{}, e
	}
	client, ok := s.walletRegistration.(WalletSnapshotClient)
	if !ok {
		return payment.WalletSnapshot{}, unavailable("Financial wallet", "payment-service")
	}
	account, e := s.q.GetAccount(ctx, s.Pool, subj.Account.AccountID)
	if e != nil {
		return payment.WalletSnapshot{}, e
	}
	if account.Status != "ACTIVE" {
		return payment.WalletSnapshot{}, precondition("WALLET_ACTIVATION_PENDING", "Activate your Owner account before requesting the financial wallet.")
	}
	out, e := client.Snapshot(ctx, account.AccountID)
	if e != nil {
		return out, downstream("payment-service", e)
	}
	return out, nil
}
