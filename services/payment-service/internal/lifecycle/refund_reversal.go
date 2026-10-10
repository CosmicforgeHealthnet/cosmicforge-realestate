package lifecycle

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func (s *Service) ReverseRefund(ctx context.Context, id ids.ID) (RefundAttempt, error) {
	if e := core.Require(ctx, platform.PaymentService); e != nil {
		if e = core.Require(ctx, platform.AdminService); e != nil {
			return RefundAttempt{}, e
		}
	}
	out, e := s.refundRow(ctx, s.DB, id)
	if e != nil {
		return out, e
	}
	if out.State == "REVERSED" {
		return out, nil
	}
	if out.State != "CONFIRMED_REFUNDED" || out.Strategy != "ORIGINAL_INSTRUMENT" || out.ProviderRefundID == nil {
		return out, core.Fail(409, "CONFIRMED_PROVIDER_REFUND_REQUIRED")
	}
	adapter, e := s.Adapter(out.ProviderID)
	if e != nil {
		return out, e
	}
	if adapter.Refund == nil {
		return out, core.Fail(503, "REFUND_PROVIDER_UNAVAILABLE")
	}
	obs, e := adapter.Refund.VerifyRefund(ctx, providers.Refund{ID: *out.ProviderRefundID, Reference: out.AttemptReference, ChargeReference: out.OriginalReference, AmountMinor: out.AmountMinor, Currency: out.Currency})
	if e != nil {
		return out, core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
	}
	var cfg finance.ProviderConfig
	var b []byte
	if e = s.DB.QueryRow(ctx, `SELECT route_snapshot FROM payment_intents WHERE intent_id=$1`, out.IntentID).Scan(&b); e != nil {
		return out, e
	}
	if e = decode(b, &cfg); e != nil {
		return out, e
	}
	if obs.State != "REVERSED" || obs.ID != *out.ProviderRefundID || obs.Reference != out.AttemptReference || obs.ChargeReference != out.OriginalReference || obs.AmountMinor != out.AmountMinor || obs.Currency != out.Currency || obs.Mode != cfg.Mode || formatID(obs.MerchantID) != cfg.MerchantRef {
		return out, core.Fail(409, "REFUND_REVERSAL_CONTEXT_MISMATCH")
	}
	a, e := s.lineAllocation(ctx, out.LineID)
	if e != nil {
		return out, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "refund-reversal:"+id.String()); e != nil {
		return out, e
	}
	var exists bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM financial_reversals WHERE refund_id=$1)`, id).Scan(&exists); e != nil {
		return out, e
	}
	if exists {
		out.State = "REVERSED"
		return out, nil
	}
	kind := "SETTLED_CASH"
	var settled bool
	if e = tx.QueryRow(ctx, `SELECT settlement_state='SETTLED' FROM payment_intents WHERE intent_id=$1 FOR UPDATE`, out.IntentID).Scan(&settled); e != nil {
		return out, e
	}
	if !settled {
		kind = "PROVIDER_RECEIVABLE"
	}
	cash, e := s.Account(trusted(ctx), tx, nil, out.PoolID, kind, a.LegalEntity, out.Currency, "")
	if e != nil {
		return out, e
	}
	frozen, e := s.Account(trusted(ctx), tx, &out.WalletID, out.PoolID, "FROZEN", a.LegalEntity, out.Currency, "refund-reversal:"+id.String())
	if e != nil {
		return out, e
	}
	j, e := s.remedyPost(ctx, tx, a, "refund-reversal:"+id.String(), out.AttemptReference, []core.Entry{{AccountID: cash, Side: "DEBIT", AmountMinor: out.AmountMinor}, {AccountID: frozen, Side: "CREDIT", AmountMinor: out.AmountMinor}})
	if e != nil {
		return out, e
	}
	r := ReversalRecord{ID: ids.New(), RefundID: &id, AmountMinor: out.AmountMinor, Currency: out.Currency, EconomicReference: "refund-return:" + obs.ID, JournalID: j}
	_, e = tx.Exec(ctx, `INSERT INTO financial_reversals(reversal_id,refund_id,provider_account_id,external_ref,amount_minor,snapshot,journal_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, r.ID, id, out.ProviderID, r.EconomicReference, r.AmountMinor, raw(r), j)
	if e != nil {
		return out, e
	}
	if e = s.Audit(ctx, tx, "refund.reversed", id.String(), r); e != nil {
		return out, e
	}
	if e = s.Emit(ctx, tx, "finance.financial_reversed.v1", "reversal", r.ID, r); e != nil {
		return out, e
	}
	out.State = "REVERSED"
	return out, tx.Commit(ctx)
}
