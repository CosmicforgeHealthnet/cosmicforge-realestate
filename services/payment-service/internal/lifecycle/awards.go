package lifecycle

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

// Approved awards are derived obligations funded by the original protected
// money. They have canonical quote/intent/allocation records, no PSP collection,
// and use the existing evidence, release and payout machinery.
type derivedAward struct {
	Allocation  Allocation
	Quote       Quote
	ProtectedID ids.ID
}

func (s *Service) prepareAward(ctx context.Context, tx pgx.Tx, c FinancialCase, a Allocation, auth Authority, amount int64) (derivedAward, error) {
	var original Quote
	if e := load(ctx, tx, `SELECT snapshot FROM locked_quotes WHERE quote_id=$1`, c.QuoteID, &original); e != nil {
		return derivedAward{}, e
	}
	q := original
	q.ID = ids.New()
	q.AmountMinor = amount
	q.QuoteVersion = "decision:" + c.ID.String()
	q.Components = []Component{{Ref: "award", Kind: "DEPOSIT_CLAIM", AmountMinor: amount, PayerRef: q.LegalPartyRef, Recurrence: "ONE_TIME"}}
	_, e := tx.Exec(ctx, `INSERT INTO locked_quotes(quote_id,source_service,source_domain,business_ref,operation_key,fingerprint,payer_wallet_id,policy_id,amount_minor,currency,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, q.ID, c.SourceService, q.Domain, q.BusinessRef, "award:"+c.ID.String(), core.Fingerprint(q), q.PayerWalletID, q.PolicyID, amount, q.Currency, raw(q))
	if e != nil {
		return derivedAward{}, e
	}
	intent := ids.New()
	var route []byte
	var provider ids.ID
	if e = tx.QueryRow(ctx, `SELECT route_snapshot,provider_account_id FROM payment_intents WHERE intent_id=$1`, c.IntentID).Scan(&route, &provider); e != nil {
		return derivedAward{}, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO payment_intents(intent_id,source_service,wallet_id,operation_key,fingerprint,amount_minor,currency,legal_entity,category,country,evidence_ref,policy_version,provider_account_id,pool_id,route_snapshot,provider_reference,state,settlement_state,locked_quote_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'APPROVED_AWARD',$9,$10,$11,$12,$13,$14::jsonb||jsonb_build_object('funding_source','CASE_AWARD','original_intent_id',$15::text,'case_id',$16::text),$17,'VERIFIED_SUCCESS','SETTLED',$18)`, intent, c.SourceService, q.PayerWalletID, "award:"+c.ID.String(), core.Fingerprint(q), amount, a.Currency, a.LegalEntity, c.Jurisdiction, c.AuthorityRef, c.PolicyID.String(), provider, a.PoolID, string(route), c.IntentID.String(), c.ID.String(), "cfa-"+intent.String(), q.ID)
	if e != nil {
		return derivedAward{}, e
	}
	allocation := Allocation{ID: ids.New(), IntentID: intent, Version: 1, QuoteID: q.ID, PolicyID: q.PolicyID, PoolID: a.PoolID, Currency: a.Currency, LegalEntity: a.LegalEntity}
	line := AllocationLine{ID: ids.New(), LineInput: LineInput{AuthorityID: auth.ID, ComponentRef: "award", Kind: "BENEFICIARY", AmountMinor: amount, Currency: a.Currency}, WalletID: auth.WalletID, State: "PROTECTED"}
	dimension := "allocation:" + line.ID.String()
	protected, e := s.Account(trusted(ctx), tx, auth.WalletID, a.PoolID, "PROTECTED", a.LegalEntity, a.Currency, dimension)
	if e != nil {
		return derivedAward{}, e
	}
	available, e := s.Account(trusted(ctx), tx, auth.WalletID, a.PoolID, "AVAILABLE", a.LegalEntity, a.Currency, dimension)
	if e != nil {
		return derivedAward{}, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO allocation_lines(line_id,allocation_id,authority_id,wallet_id,amount_minor,snapshot,protected_account_id,available_account_id,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'PROTECTED')`, line.ID, allocation.ID, auth.ID, auth.WalletID, amount, raw(line), protected, available)
	if e != nil {
		return derivedAward{}, e
	}
	allocation.Lines = []AllocationLine{line}
	return derivedAward{allocation, q, protected}, nil
}
func (s *Service) sealAward(ctx context.Context, tx pgx.Tx, award derivedAward, c FinancialCase, journal ids.ID) error {
	a := award.Allocation
	_, e := tx.Exec(ctx, `INSERT INTO allocation_snapshots(allocation_id,intent_id,version,fingerprint,snapshot,journal_id) VALUES($1,$2,1,$3,$4,$5)`, a.ID, a.IntentID, core.Fingerprint(a), raw(a), journal)
	if e != nil {
		return e
	}
	var custody string
	if e = tx.QueryRow(ctx, `SELECT COALESCE(snapshot->>'custody_evidence_ref','') FROM transaction_funding WHERE intent_id=$1`, c.IntentID).Scan(&custody); e != nil {
		return e
	}
	proof := map[string]any{"funding_source": "CASE_AWARD", "original_intent_id": c.IntentID, "case_id": c.ID, "custody_evidence_ref": custody}
	_, e = tx.Exec(ctx, `INSERT INTO transaction_funding(funding_id,intent_id,journal_id,snapshot,fingerprint) VALUES($1,$2,$3,$4,$5)`, ids.New(), a.IntentID, journal, raw(proof), core.Fingerprint(proof))
	if e != nil {
		return e
	}
	if e = s.Emit(ctx, tx, "finance.allocation_created.v1", "allocation", a.ID, a); e != nil {
		return e
	}
	return s.Audit(ctx, tx, "case.award_protected", c.ID.String(), map[string]any{"allocation_id": a.ID, "intent_id": a.IntentID, "journal_id": journal, "amount_minor": a.Lines[0].AmountMinor})
}
