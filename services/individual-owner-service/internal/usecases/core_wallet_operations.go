package usecases

import (
	"context"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type CoreStatement struct {
	Wallet    payment.Wallet             `json:"wallet"`
	Entries   []payment.CoreHistoryEntry `json:"entries"`
	NextAfter *ids.ID                    `json:"next_after,omitempty"`
	Source    string                     `json:"source"`
}

func (s *Service) CoreFinancialStatement(ctx context.Context, subj authz.Subject, after ids.ID, limit int) (CoreStatement, error) {
	snap, e := s.OwnFinancialWallet(ctx, subj)
	if e != nil {
		return CoreStatement{}, e
	}
	c, e := s.coreClient()
	if e != nil {
		return CoreStatement{}, e
	}
	if limit < 1 || limit > 100 {
		return CoreStatement{}, precondition("INVALID_PAGE_LIMIT", "Limit must be between 1 and 100.")
	}
	entries, e := c.History(ctx, accountOf(subj), snap.Wallet.WalletID, after, limit)
	if e != nil {
		return CoreStatement{}, downstream("payment-service", e)
	}
	out := CoreStatement{Wallet: snap.Wallet, Entries: entries, Source: "payment-service ledger readback; entries are not a new Owner ledger or a generated PDF statement"}
	if len(entries) == limit {
		out.NextAfter = &entries[len(entries)-1].EntryID
	}
	return out, nil
}
func (s *Service) CorePayoutReferences(ctx context.Context, subj authz.Subject) ([]payment.CorePayoutReference, error) {
	if _, e := s.authorize(ctx, subj, authz.ActPayoutRead, ids.Nil); e != nil {
		return nil, e
	}
	snap, e := s.OwnFinancialWallet(ctx, subj)
	if e != nil {
		return nil, e
	}
	c, e := s.coreClient()
	if e != nil {
		return nil, e
	}
	v, e := c.PayoutReferences(ctx, accountOf(subj), snap.Wallet.WalletID)
	if e != nil {
		return nil, downstream("payment-service", e)
	}
	return v, nil
}
func (s *Service) CoreAvailableBalance(ctx context.Context, subj authz.Subject, pool ids.ID) (payment.CoreAvailability, error) {
	snap, e := s.OwnFinancialWallet(ctx, subj)
	if e != nil {
		return payment.CoreAvailability{}, e
	}
	c, e := s.coreClient()
	if e != nil {
		return payment.CoreAvailability{}, e
	}
	v, e := c.Availability(ctx, accountOf(subj), snap.Wallet.WalletID, pool)
	if e != nil {
		return v, downstream("payment-service", e)
	}
	return v, nil
}

type CorePayoutPreference struct {
	PayoutAccountID ids.ID `json:"payout_account_id" v:"required"`
	Version         int64  `json:"version"`
}

func (s *Service) PrepareCorePayoutPreference(ctx context.Context, subj authz.Subject, in CorePayoutPreference) (CorePayoutPreference, error) {
	if _, e := s.authorize(ctx, subj, authz.ActPayoutWrite, ids.Nil); e != nil {
		return in, e
	}
	if e := s.requireIdentity(ctx, accountOf(subj), GatePayout); e != nil {
		return in, e
	}
	refs, e := s.CorePayoutReferences(ctx, subj)
	if e != nil {
		return in, e
	}
	for _, r := range refs {
		if r.ID == in.PayoutAccountID && r.Eligible {
			return in, nil
		}
	}
	return in, precondition("VERIFIED_CORE_PAYOUT_REFERENCE_REQUIRED", "Select a current Core-verified owned payout reference. Owner cannot verify a new bank itself.")
}
func (s *Service) PutCorePayoutPreference(ctx context.Context, subj authz.Subject, version int64, in CorePayoutPreference) (CorePayoutPreference, error) {
	if _, e := s.authorize(ctx, subj, authz.ActPayoutWrite, ids.Nil); e != nil {
		return in, e
	}
	var current int64
	e := mustTx(ctx).QueryRow(ctx, `SELECT COALESCE((SELECT version FROM financial_core_payout_preferences WHERE owner_account_id=$1),0)`, accountOf(subj)).Scan(&current)
	if e != nil {
		return in, e
	}
	if current != version {
		return in, conflict("VERSION_CONFLICT", "Reload the payout preference.")
	}
	tag, e := mustTx(ctx).Exec(ctx, `INSERT INTO financial_core_payout_preferences(owner_account_id,payout_account_id,updated_at) VALUES($1,$2,$3) ON CONFLICT(owner_account_id) DO UPDATE SET payout_account_id=EXCLUDED.payout_account_id,updated_at=EXCLUDED.updated_at,version=financial_core_payout_preferences.version+1 WHERE financial_core_payout_preferences.version=$4`, accountOf(subj), in.PayoutAccountID, s.now(), version)
	if e != nil {
		return in, e
	}
	if tag.RowsAffected() == 0 {
		return in, conflict("VERSION_CONFLICT", "The payout preference changed; reload it before changing the destination.")
	}
	if e = mustTx(ctx).QueryRow(ctx, `SELECT version FROM financial_core_payout_preferences WHERE owner_account_id=$1`, accountOf(subj)).Scan(&in.Version); e != nil {
		return in, e
	}
	a, e := s.q.GetAccount(ctx, mustTx(ctx), accountOf(subj))
	if e != nil {
		return in, e
	}
	if e = s.enqueueAlert(ctx, mustTx(ctx), a, "PAYOUT_CHANGED", ""); e != nil {
		return in, e
	}
	e = s.record(ctx, "financial.core_payout_preference_changed", "account", accountOf(subj), map[string]any{"payout_account_id": in.PayoutAccountID}, nil)
	return in, e
}
func (s *Service) GetCorePayoutPreference(ctx context.Context, subj authz.Subject) (CorePayoutPreference, error) {
	if _, e := s.authorize(ctx, subj, authz.ActPayoutRead, ids.Nil); e != nil {
		return CorePayoutPreference{}, e
	}
	var out CorePayoutPreference
	e := s.Pool.QueryRow(ctx, `SELECT payout_account_id,version FROM financial_core_payout_preferences WHERE owner_account_id=$1`, accountOf(subj)).Scan(&out.PayoutAccountID, &out.Version)
	if isNoRows(e) {
		return out, nil
	}
	return out, e
}
func (s *Service) WithdrawCoreEarnings(ctx context.Context, subj authz.Subject, in payment.CoreWithdrawal, key string) (payment.CorePayout, error) {
	if _, e := s.authorize(ctx, subj, authz.ActPayoutWrite, ids.Nil); e != nil {
		return payment.CorePayout{}, e
	}
	if e := s.requireIdentity(ctx, accountOf(subj), GatePayout); e != nil {
		return payment.CorePayout{}, e
	}
	snap, e := s.OwnFinancialWallet(ctx, subj)
	if e != nil {
		return payment.CorePayout{}, e
	}
	if in.WalletID != ids.Nil && in.WalletID != snap.Wallet.WalletID {
		return payment.CorePayout{}, precondition("PAYOUT_WALLET_MISMATCH", "Withdraw only this Owner's released earnings.")
	}
	in.WalletID = snap.Wallet.WalletID
	pref, e := s.GetCorePayoutPreference(ctx, subj)
	if e != nil {
		return payment.CorePayout{}, e
	}
	if pref.PayoutAccountID != ids.Nil && pref.PayoutAccountID != in.PayoutAccountID {
		return payment.CorePayout{}, precondition("PAYOUT_PREFERENCE_MISMATCH", "The approved destination must match the selected owned preference.")
	}
	c, e := s.coreClient()
	if e != nil {
		return payment.CorePayout{}, e
	}
	v, e := c.Withdraw(ctx, accountOf(subj), in, key)
	if e != nil {
		return v, downstream("payment-service", e)
	}
	return v, nil
}
func (s *Service) ReadCorePayout(ctx context.Context, subj authz.Subject, id ids.ID, resolve bool) (payment.CorePayout, error) {
	if _, e := s.authorize(ctx, subj, authz.ActPayoutRead, ids.Nil); e != nil {
		return payment.CorePayout{}, e
	}
	snap, e := s.OwnFinancialWallet(ctx, subj)
	if e != nil {
		return payment.CorePayout{}, e
	}
	c, e := s.coreClient()
	if e != nil {
		return payment.CorePayout{}, e
	}
	v, e := c.Payout(ctx, accountOf(subj), id, resolve)
	if e != nil {
		return v, downstream("payment-service", e)
	}
	if v.WalletID != snap.Wallet.WalletID || v.Currency != snap.Wallet.Currency {
		return payment.CorePayout{}, precondition("PAYOUT_WALLET_MISMATCH", "Payout belongs to another wallet.")
	}
	return v, nil
}
