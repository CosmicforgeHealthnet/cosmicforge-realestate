package finance

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// MatchDVACharge only records externally confirmed, mapped funding as pending.
// Wallet top-up release/spend policy and business billing remain deferred.
func (s *Service) MatchDVACharge(ctx context.Context, provider ids.ID, ref string) error {
	if e := core.Require(ctx, platform.PaymentService); e != nil {
		return e
	}
	a, e := s.Adapter(provider)
	if e != nil {
		return e
	}
	if a.Collection == nil {
		return core.Fail(503, "PROVIDER_UNAVAILABLE")
	}
	c, e := a.Collection.VerifyCharge(ctx, ref)
	if e != nil {
		return core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
	}
	config, e := s.dvaConfig(ctx, provider)
	if e != nil {
		return e
	}
	if c.Reference != ref || c.State != "VERIFIED_SUCCESS" || c.Mode != config.Mode || formatMerchant(c.MerchantID) != config.MerchantRef || c.AmountMinor <= 0 || c.IntentID != "" {
		return core.Fail(409, "CHARGE_CONTEXT_MISMATCH")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "dva-charge:"+provider.String()+":"+ref)
	if e != nil {
		return e
	}
	var exists bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM collection_receipts WHERE provider_account_id=$1 AND external_ref=$2)`, provider, ref).Scan(&exists)
	if e != nil {
		return e
	}
	if exists {
		return nil
	}
	var wallet ids.ID
	var entity string
	e = tx.QueryRow(ctx, `SELECT v.wallet_id,w.legal_entity FROM virtual_accounts v JOIN wallets w USING(wallet_id) JOIN financial_identities i USING(identity_id) JOIN verification_refs k USING(identity_id) WHERE v.provider_account_id=$1 AND v.customer_ref=$2 AND v.state='ACTIVE' AND w.currency=$3 AND w.legal_entity=$4 AND i.state='ACTIVE' AND v.consent_expires_at>now() AND k.state='VERIFIED' AND k.expires_at>now() AND k.verification_ref=v.verification_ref FOR UPDATE OF v`, provider, c.CustomerRef, c.Currency, config.LegalEntity).Scan(&wallet, &entity)
	if e != nil {
		return core.Fail(409, "UNMATCHED_DVA_COLLECTION")
	}
	pending, e := s.Account(ctx, tx, &wallet, config.PoolID, "PAYMENT_PENDING", entity, c.Currency, "")
	if e != nil {
		return e
	}
	receivable, e := s.Account(ctx, tx, nil, config.PoolID, "PROVIDER_RECEIVABLE", entity, c.Currency, "")
	if e != nil {
		return e
	}
	for _, kind := range core.Views {
		if _, e = s.Account(ctx, tx, &wallet, config.PoolID, kind, entity, c.Currency, ""); e != nil {
			return e
		}
	}
	journal, e := s.PostTx(ctx, tx, core.Posting{OperationRef: "charge:" + provider.String() + ":" + ref, LegalEntity: entity, Currency: c.Currency, PoolID: config.PoolID, PolicyVersion: "dva-collection-confirmation-v1", Authorization: map[string]string{"evidence_ref": ref, "approved_by": "verified-provider-readback"}, Entries: []core.Entry{{AccountID: receivable, Side: "DEBIT", AmountMinor: c.AmountMinor}, {AccountID: pending, Side: "CREDIT", AmountMinor: c.AmountMinor}}})
	if e != nil {
		return e
	}
	id := ids.New()
	evidence, _ := json.Marshal(map[string]string{"provider_account_id": provider.String(), "reference": ref, "settlement_state": "PENDING"})
	_, e = tx.Exec(ctx, `INSERT INTO collection_receipts(receipt_id,provider_account_id,external_ref,wallet_id,journal_id,amount_minor,currency,evidence) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, provider, ref, wallet, journal, c.AmountMinor, c.Currency, string(evidence))
	if e != nil {
		return e
	}
	if e = s.Emit(ctx, tx, "finance.wallet.funding_pending.v1", "wallet", wallet, map[string]any{"receipt_id": id, "wallet_id": wallet, "amount_minor": c.AmountMinor, "currency": c.Currency, "settlement_state": "PENDING"}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
