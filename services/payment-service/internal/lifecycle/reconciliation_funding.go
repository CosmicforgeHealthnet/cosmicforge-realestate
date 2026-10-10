package lifecycle

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

func (s *Service) reconcileFunding(ctx context.Context, tx pgx.Tx, provider ids.ID, adapter providers.SettlementProvider) ([]ReconciliationFinding, error) {
	rows, e := tx.Query(ctx, `SELECT i.intent_id,i.provider_reference,i.amount_minor,i.currency,f.snapshot->>'settlement_ref',COALESCE((SELECT sum(e.amount_minor) FROM journal_entries e JOIN ledger_accounts a USING(account_id) WHERE e.journal_id=f.journal_id AND a.kind='PROCESSING_EXPENSE' AND e.side='DEBIT'),0)::bigint FROM transaction_funding f JOIN payment_intents i USING(intent_id) WHERE i.provider_account_id=$1 AND NOT i.route_snapshot ? 'funding_source' ORDER BY f.created_at LIMIT 1000`, provider)
	if e != nil {
		return nil, e
	}
	type proof struct {
		id                        ids.ID
		ref, currency, settlement string
		amount, fee               int64
	}
	var proofs []proof
	for rows.Next() {
		var p proof
		if e = rows.Scan(&p.id, &p.ref, &p.amount, &p.currency, &p.settlement, &p.fee); e != nil {
			rows.Close()
			return nil, e
		}
		proofs = append(proofs, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []ReconciliationFinding{}
	membership, ok := adapter.(providers.SettlementTransactions)
	for _, p := range proofs {
		issue := func(observed string) {
			out = append(out, ReconciliationFinding{"SETTLEMENT_FEE_OR_MEMBERSHIP_MISMATCH", p.id.String(), p.settlement + ":" + p.ref, observed})
		}
		if !ok {
			issue("membership readback unavailable")
			continue
		}
		settlement, e := adapter.FetchSettlement(ctx, p.settlement)
		if e != nil {
			issue("settlement readback unavailable")
			continue
		}
		charges, e := membership.SettlementTransactions(ctx, p.settlement)
		if e != nil {
			issue("membership readback unavailable")
			continue
		}
		if settlement.Reference != p.settlement || settlement.Currency != p.currency || settlement.SettledAt == nil || settlement.SettledAt.After(time.Now()) || !has([]string{"settled", "success", "processed"}, strings.ToLower(settlement.State)) {
			issue("settlement context mismatch")
			continue
		}
		match := false
		var gross, fees int64
		seen := map[string]bool{}
		valid := true
		for _, c := range charges {
			if c.AmountMinor <= 0 || c.FeeMinor < 0 || c.FeeMinor >= c.AmountMinor || c.AmountMinor > settlement.AmountMinor-gross || c.FeeMinor > settlement.FeeMinor-fees || seen[c.Reference] || c.Currency != p.currency || c.State != "VERIFIED_SUCCESS" {
				valid = false
				break
			}
			seen[c.Reference] = true
			gross += c.AmountMinor
			fees += c.FeeMinor
			if c.Reference == p.ref {
				match = c.AmountMinor == p.amount && c.FeeMinor == p.fee
			}
		}
		if !valid || !match || gross != settlement.AmountMinor || fees != settlement.FeeMinor {
			issue("fee, gross or original membership drift")
		}
	}
	if len(proofs) == 1000 {
		out = append(out, ReconciliationFinding{"RECONCILIATION_BATCH_LIMIT", provider.String(), "complete funding observation", "continue bounded range"})
	}
	return out, nil
}
