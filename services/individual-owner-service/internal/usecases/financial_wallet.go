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
	// Structural source identity only, used to correlate canonical payout events.
	// No balances or verification verdicts are copied into an Owner ledger.
	if _, e = s.Pool.Exec(ctx, `INSERT INTO financial_core_wallet_links(wallet_id,owner_account_id,legal_entity,currency) VALUES($1,$2,$3,$4) ON CONFLICT(wallet_id) DO NOTHING`, out.Wallet.WalletID, account.AccountID, out.Wallet.LegalEntity, out.Wallet.Currency); e != nil {
		return out, e
	}
	var matches bool
	if e = s.Pool.QueryRow(ctx, `SELECT owner_account_id=$2 AND legal_entity=$3 AND currency=$4 FROM financial_core_wallet_links WHERE wallet_id=$1`, out.Wallet.WalletID, account.AccountID, out.Wallet.LegalEntity, out.Wallet.Currency).Scan(&matches); e != nil {
		return out, e
	}
	if !matches {
		return out, precondition("WALLET_SOURCE_CONTEXT_MISMATCH", "Structural financial wallet identity does not match its Owner.")
	}
	return out, nil
}
