package lifecycle

import (
	"context"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/jackc/pgx/v5"
	"time"
)

// Only authenticated gateway delegation for marketplace can invoke these
// customer commands. No body field can impersonate a business source.
func customerContext(ctx context.Context) context.Context {
	p, e := core.Caller(ctx)
	if e == nil && p.Service == platform.APIGateway && p.Delegation.ActorDomain == "marketplace" && p.Delegation.ActorID != ids.Nil {
		p.Service = platform.MarketplaceService
		return serviceauth.WithPrincipal(ctx, p)
	}
	return ctx
}
func beneficiaryContext(ctx context.Context) context.Context {
	p, e := core.Caller(ctx)
	if e == nil && p.Service == platform.APIGateway && p.Delegation.ActorID != ids.Nil {
		service, ok := platform.ServiceForDomain(p.Delegation.ActorDomain)
		if ok && p.Delegation.ActorDomain != "admin" {
			p.Service = service
			return serviceauth.WithPrincipal(ctx, p)
		}
	}
	return ctx
}
func (s *Service) customer(ctx context.Context, id ids.ID) (core.Wallet, error) {
	w, e := s.OwnWallet(ctx, id)
	if e != nil {
		return w, e
	}
	if w.Role != "CUSTOMER" || w.Domain != "marketplace" || w.State != "ACTIVE" {
		return w, core.Fail(403, "CUSTOMER_WALLET_REQUIRED")
	}
	if e = core.DomainCaller(ctx, "marketplace"); e != nil {
		return w, e
	}
	return w, nil
}

type TopupRequest struct {
	ID          ids.ID    `json:"topup_id"`
	WalletID    ids.ID    `json:"wallet_id"`
	PolicyID    ids.ID    `json:"policy_id"`
	AmountMinor int64     `json:"amount_minor"`
	Currency    string    `json:"currency"`
	Country     string    `json:"country"`
	Email       string    `json:"email"`
	ConsentRef  string    `json:"consent_ref"`
	ExpiresAt   time.Time `json:"expires_at"`
	ReceiptID   *ids.ID   `json:"receipt_id,omitempty"`
}
type Topup struct {
	TopupRequest
	IntentID *ids.ID `json:"intent_id,omitempty"`
	PoolID   ids.ID  `json:"pool_id"`
	State    string  `json:"state"`
}

func (s *Service) Topup(ctx context.Context, id ids.ID) (Topup, error) {
	var r Topup
	e := load(ctx, s.DB, `SELECT snapshot||jsonb_build_object('state',CASE WHEN EXISTS(SELECT 1 FROM customer_funding f WHERE f.topup_id=customer_topups.topup_id) THEN 'AVAILABLE' ELSE 'SETTLEMENT_PENDING' END) FROM customer_topups WHERE topup_id=$1`, id, &r)
	if e != nil {
		return r, e
	}
	if _, e = s.OwnWallet(ctx, r.WalletID); e != nil {
		return Topup{}, e
	}
	return r, nil
}
func (s *Service) RequestTopup(ctx context.Context, in TopupRequest, key string) (Topup, error) {
	ctx = customerContext(ctx)
	w, e := s.customer(ctx, in.WalletID)
	if e != nil {
		return Topup{}, e
	}
	if s.Environment == "production" {
		return Topup{}, core.Fail(403, "PRODUCTION_CAPABILITY_DISABLED")
	}
	policy, e := s.Policy(ctx, in.PolicyID)
	if e != nil {
		return Topup{}, e
	}
	if policy.Key != "CUSTOMER_FUNDING" || policy.Currency != w.Currency || policy.Jurisdiction != in.Country || policyAt(policy, time.Now()) != nil {
		return Topup{}, core.Fail(403, "CUSTOMER_FUNDING_POLICY_REQUIRED")
	}
	rp, e := s.remedyPolicy(ctx, s.DB, in.PolicyID)
	if e != nil {
		return Topup{}, e
	}
	if !rp.WalletTopup || !has([]string{"CUSTOMER_NET", "PLATFORM_FUNDED"}, rp.TopupFeeTreatment) {
		return Topup{}, core.Fail(403, "CUSTOMER_FUNDING_POLICY_REQUIRED")
	}
	if key == "" || len(key) > 128 || in.ID.ValidateV7() != nil || in.AmountMinor <= 0 || in.Currency != w.Currency || in.ConsentRef == "" || !in.ExpiresAt.After(time.Now()) {
		return Topup{}, fail("INVALID_TOPUP")
	}
	var disabled bool
	if e = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM policy_deactivations WHERE policy_id=$1 AND effective_at<=now())`, in.PolicyID).Scan(&disabled); e != nil {
		return Topup{}, e
	}
	if disabled {
		return Topup{}, core.Fail(403, "POLICY_DISABLED_OR_NOT_EFFECTIVE")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Topup{}, e
	}
	defer tx.Rollback(ctx)
	op := "customer-topup:" + w.WalletID.String() + ":" + key
	if e = lock(ctx, tx, op); e != nil {
		return Topup{}, e
	}
	var prior Topup
	var fp string
	var priorRaw []byte
	e = tx.QueryRow(ctx, `SELECT snapshot,fingerprint FROM customer_topups WHERE operation_key=$1`, op).Scan(&priorRaw, &fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return Topup{}, core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		if e = decode(priorRaw, &prior); e != nil {
			return Topup{}, e
		}
		return prior, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return Topup{}, e
	}
	out := Topup{TopupRequest: in, State: "SETTLEMENT_PENDING"}
	if in.ReceiptID == nil {

		var party string
		if e = tx.QueryRow(ctx, `SELECT legal_party_ref FROM financial_identities WHERE identity_id=$1`, w.IdentityID).Scan(&party); e != nil {
			return out, e
		}
		if e = s.currentWallet(ctx, tx, w.WalletID, party); e != nil {
			return out, e
		}

		i, e := s.CreateIntent(ctx, finance.IntentInput{WalletID: w.WalletID, AmountMinor: in.AmountMinor, Currency: in.Currency, Country: in.Country, Category: policy.Key, EvidenceRef: in.ConsentRef, PolicyVersion: policy.Version, Email: in.Email}, "topup:"+w.WalletID.String()+":"+key)
		if e != nil {
			return out, e
		}
		out.IntentID = &i.ID
		out.PoolID = i.PoolID
	} else {
		var owner ids.ID
		var amount int64
		var currency string
		e = s.DB.QueryRow(ctx, `SELECT r.wallet_id,r.amount_minor,r.currency,c.pool_id FROM collection_receipts r JOIN provider_accounts p USING(provider_account_id) JOIN custody_pools c USING(provider_account_id) WHERE r.receipt_id=$1 AND NOT EXISTS(SELECT 1 FROM payment_intents i WHERE i.provider_account_id=r.provider_account_id AND i.provider_reference=r.external_ref)`, *in.ReceiptID).Scan(&owner, &amount, &currency, &out.PoolID)
		if e != nil {
			return out, e
		}
		if owner != w.WalletID || amount != in.AmountMinor || currency != in.Currency {
			return out, core.Fail(403, "DVA_TOPUP_CONTEXT_MISMATCH")
		}
	}

	_, e = tx.Exec(ctx, `INSERT INTO customer_topups(topup_id,wallet_id,intent_id,receipt_id,pool_id,amount_minor,currency,policy_id,expires_at,fingerprint,snapshot,operation_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, in.ID, w.WalletID, out.IntentID, in.ReceiptID, out.PoolID, in.AmountMinor, in.Currency, in.PolicyID, in.ExpiresAt, core.Fingerprint(in), raw(out), op)
	if e != nil {
		return out, e
	}
	if e = s.Audit(ctx, tx, "customer.topup_requested", in.ID.String(), map[string]any{"topup_id": in.ID, "wallet_id": w.WalletID, "intent_id": out.IntentID, "receipt_id": in.ReceiptID}); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

type TopupFunding struct {
	TopupID         ids.ID   `json:"topup_id"`
	SettlementRef   string   `json:"settlement_ref"`
	CashEvidenceRef string   `json:"cash_evidence_ref"`
	Approvers       []string `json:"approvers"`
}

func (s *Service) FundTopup(ctx context.Context, in TopupFunding) error {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	if s.Environment == "production" {
		return core.Fail(403, "PRODUCTION_CAPABILITY_DISABLED")
	}
	if in.SettlementRef == "" || in.CashEvidenceRef == "" || !dual(in.Approvers) {
		return fail("DUAL_SETTLEMENT_ATTESTATION_REQUIRED")
	}
	var top Topup
	if e := load(ctx, s.DB, `SELECT snapshot FROM customer_topups WHERE topup_id=$1`, in.TopupID, &top); e != nil {
		return e
	}
	if top.IntentID != nil {
		if e := s.ConfirmFunding(ctx, FundingInput{IntentID: *top.IntentID, SettlementRef: in.SettlementRef, CashEvidenceRef: in.CashEvidenceRef, Approvers: in.Approvers}); e != nil {
			return e
		}
	}
	var provider ids.ID
	var ref, entity string
	e := s.DB.QueryRow(ctx, `SELECT p.provider_account_id,p.legal_entity,COALESCE(i.provider_reference,r.external_ref) FROM customer_topups t JOIN custody_pools p USING(pool_id) LEFT JOIN payment_intents i USING(intent_id) LEFT JOIN collection_receipts r USING(receipt_id) WHERE t.topup_id=$1`, in.TopupID).Scan(&provider, &entity, &ref)
	if e != nil {
		return e
	}
	adapter, e := s.Adapter(provider)
	if e != nil {
		return e
	}
	membership, ok := adapter.Settlement.(providers.SettlementTransactions)
	if !ok {
		return core.Fail(503, "MATCHED_SETTLEMENT_UNAVAILABLE")
	}
	settlement, e := adapter.Settlement.FetchSettlement(ctx, in.SettlementRef)
	if e != nil {
		return core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
	}
	xs, e := membership.SettlementTransactions(ctx, in.SettlementRef)
	if e != nil {
		return e
	}
	var match providers.Charge
	var gross, fees int64
	seen := map[string]bool{}
	for _, v := range xs {
		if v.AmountMinor <= 0 || v.FeeMinor < 0 || v.FeeMinor >= v.AmountMinor || seen[v.Reference] || v.Currency != top.Currency || v.State != "VERIFIED_SUCCESS" || v.AmountMinor > settlement.AmountMinor-gross || v.FeeMinor > settlement.FeeMinor-fees {
			return core.Fail(409, "SETTLEMENT_CONTEXT_MISMATCH")
		}
		seen[v.Reference] = true
		gross += v.AmountMinor
		fees += v.FeeMinor
		if v.Reference == ref {
			match = v
		}
	}
	var cfg finance.ProviderConfig
	var b []byte
	if e = s.DB.QueryRow(ctx, `SELECT config FROM provider_accounts WHERE provider_account_id=$1`, provider).Scan(&b); e != nil {
		return e
	}
	if e = decode(b, &cfg); e != nil {
		return e
	}
	if settlement.Reference != in.SettlementRef || settlement.Currency != top.Currency || !has([]string{"settled", "success", "processed"}, settlement.State) || settlement.SettledAt == nil || settlement.SettledAt.After(time.Now()) || gross != settlement.AmountMinor || fees != settlement.FeeMinor || match.AmountMinor != top.AmountMinor || match.Mode != cfg.Mode || formatID(match.MerchantID) != cfg.MerchantRef {
		return core.Fail(409, "SETTLEMENT_CONTEXT_MISMATCH")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "customer-funding:"+in.TopupID.String()); e != nil {
		return e
	}
	var fp string
	e = tx.QueryRow(ctx, `SELECT fingerprint FROM customer_funding WHERE topup_id=$1`, in.TopupID).Scan(&fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if !top.ExpiresAt.After(time.Now()) {
		return core.Fail(403, "TOPUP_EXPIRED_REVIEW_REQUIRED")
	}
	var party string
	if e = tx.QueryRow(ctx, `SELECT i.legal_party_ref FROM wallets w JOIN financial_identities i USING(identity_id) WHERE wallet_id=$1 AND role='CUSTOMER'`, top.WalletID).Scan(&party); e != nil {
		return e
	}
	if e = s.currentWallet(ctx, tx, top.WalletID, party); e != nil {
		return e
	}
	rp, e := s.remedyPolicy(ctx, tx, top.PolicyID)
	if e != nil {
		return e
	}
	pending, e := s.Account(trusted(ctx), tx, &top.WalletID, top.PoolID, "PAYMENT_PENDING", entity, top.Currency, "")
	if e != nil {
		return e
	}
	avail, e := s.Account(trusted(ctx), tx, &top.WalletID, top.PoolID, "AVAILABLE", entity, top.Currency, "")
	if e != nil {
		return e
	}
	credit := top.AmountMinor
	entries := []core.Entry{{AccountID: pending, Side: "DEBIT", AmountMinor: top.AmountMinor}}
	if rp.TopupFeeTreatment == "CUSTOMER_NET" {
		credit -= match.FeeMinor
		if match.FeeMinor > 0 {
			expense, e := s.Account(trusted(ctx), tx, nil, top.PoolID, "PROCESSING_EXPENSE", entity, top.Currency, "")
			if e != nil {
				return e
			}
			entries = append(entries, core.Entry{AccountID: expense, Side: "CREDIT", AmountMinor: match.FeeMinor})
		}
	}
	entries = append(entries, core.Entry{AccountID: avail, Side: "CREDIT", AmountMinor: credit})
	if top.ReceiptID != nil {
		var customer string
		if e = tx.QueryRow(ctx, `SELECT v.customer_ref FROM virtual_accounts v JOIN collection_receipts r ON r.wallet_id=v.wallet_id AND r.provider_account_id=v.provider_account_id WHERE receipt_id=$1 AND v.state='ACTIVE' AND consent_expires_at>now() FOR SHARE OF v`, *top.ReceiptID).Scan(&customer); e != nil {
			return e
		}
		if match.CustomerRef != customer || match.IntentID != "" {
			return core.Fail(403, "DVA_TOPUP_CONTEXT_MISMATCH")
		}
		// Funding must cover already reconciled cash plus this receipt. A merchant
		// balance alone never establishes receipt membership or lawful custody.
		balances, e := adapter.Settlement.FetchBalance(ctx)
		if e != nil {
			return e
		}
		var observed int64 = -1
		for _, v := range balances {
			if v.Currency == top.Currency {
				if observed >= 0 {
					return fail("INVALID_LIQUIDITY_OBSERVATION")
				}
				observed = v.AmountMinor
			}
		}
		if _, e = tx.Exec(ctx, `SELECT pool_id FROM custody_pools WHERE pool_id=$1 FOR UPDATE`, top.PoolID); e != nil {
			return e
		}
		var cashNow int64
		var exception bool
		if e = tx.QueryRow(ctx, `SELECT COALESCE((SELECT sum(balance_minor) FROM account_balances WHERE pool_id=$1 AND kind='SETTLED_CASH'),0)::bigint,EXISTS(SELECT 1 FROM reconciliation_exceptions WHERE pool_id=$1 AND resolved_at IS NULL)`, top.PoolID).Scan(&cashNow, &exception); e != nil {
			return e
		}
		if exception || observed < cashNow || top.AmountMinor-match.FeeMinor > observed-cashNow {
			return core.Fail(409, "UNBACKED_SETTLEMENT")
		}
		cash, e := s.Account(trusted(ctx), tx, nil, top.PoolID, "SETTLED_CASH", entity, top.Currency, "")
		if e != nil {
			return e
		}
		receivable, e := s.Account(trusted(ctx), tx, nil, top.PoolID, "PROVIDER_RECEIVABLE", entity, top.Currency, "")
		if e != nil {
			return e
		}
		entries = append(entries, core.Entry{AccountID: cash, Side: "DEBIT", AmountMinor: top.AmountMinor - match.FeeMinor}, core.Entry{AccountID: receivable, Side: "CREDIT", AmountMinor: top.AmountMinor})
		if match.FeeMinor > 0 {
			expense, e := s.Account(trusted(ctx), tx, nil, top.PoolID, "PROCESSING_EXPENSE", entity, top.Currency, "")
			if e != nil {
				return e
			}
			entries = append(entries, core.Entry{AccountID: expense, Side: "DEBIT", AmountMinor: match.FeeMinor})
		}
		if _, e = tx.Exec(ctx, `UPDATE custody_pools SET restricted=false WHERE pool_id=$1`, top.PoolID); e != nil {
			return e
		}
	}
	j, e := s.PostTx(trusted(ctx), tx, core.Posting{OperationRef: "topup-funding:" + in.TopupID.String(), PoolID: top.PoolID, LegalEntity: entity, Currency: top.Currency, PolicyVersion: top.PolicyID.String(), Authorization: map[string]string{"evidence_ref": in.CashEvidenceRef, "approved_by": raw(in.Approvers)}, Entries: entries})
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO customer_funding(topup_id,journal_id,snapshot,fingerprint) VALUES($1,$2,$3,$4)`, in.TopupID, j, raw(in), core.Fingerprint(in))
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "customer.topup_funded", in.TopupID.String(), in); e != nil {
		return e
	}
	if e = s.Emit(ctx, tx, "finance.customer_funded.v1", "topup", in.TopupID, map[string]any{"topup_id": in.TopupID, "wallet_id": top.WalletID, "amount_minor": credit, "currency": top.Currency, "journal_id": j}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type WalletSpend struct {
	WalletID     ids.ID      `json:"wallet_id"`
	QuoteID      ids.ID      `json:"quote_id"`
	PoolID       ids.ID      `json:"pool_id"`
	ConsentRef   string      `json:"consent_ref"`
	QuoteVersion string      `json:"quote_version"`
	AmountMinor  int64       `json:"amount_minor"`
	Currency     string      `json:"currency"`
	Lines        []LineInput `json:"lines"`
}

func (s *Service) SpendWallet(ctx context.Context, in WalletSpend, key string) (result Allocation, err error) {
	ctx = customerContext(ctx)
	w, e := s.customer(ctx, in.WalletID)
	if e != nil {
		return Allocation{}, e
	}
	defer func() {
		if err != nil {
			tx, e := s.DB.Begin(ctx)
			if e != nil {
				return
			}
			defer tx.Rollback(ctx)
			if e = s.Audit(ctx, tx, "customer.wallet_spend_rejected", in.QuoteID.String(), map[string]any{"wallet_id": in.WalletID, "quote_id": in.QuoteID, "code": "WALLET_PAYMENT_REJECTED"}); e == nil {
				_ = tx.Commit(ctx)
			}
		}
	}()
	if s.Environment == "production" {
		return Allocation{}, core.Fail(403, "PRODUCTION_CAPABILITY_DISABLED")
	}
	if key == "" || len(key) > 128 || in.ConsentRef == "" || in.AmountMinor <= 0 || len(in.Lines) == 0 || len(in.Lines) > 100 {
		return Allocation{}, fail("ACCEPTED_WALLET_OBLIGATION_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Allocation{}, e
	}
	defer tx.Rollback(ctx)
	op := "wallet-spend:" + w.WalletID.String() + ":" + key
	if e = lock(ctx, tx, op); e != nil {
		return Allocation{}, e
	}
	var previous ids.ID
	var fp string
	e = tx.QueryRow(ctx, `SELECT intent_id,fingerprint FROM wallet_spends WHERE operation_key=$1`, op).Scan(&previous, &fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return Allocation{}, core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		var id ids.ID
		if e = tx.QueryRow(ctx, `SELECT allocation_id FROM allocation_snapshots WHERE intent_id=$1 AND version=1`, previous).Scan(&id); e != nil {
			return Allocation{}, e
		}
		return s.allocation(ctx, tx, id)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return Allocation{}, e
	}
	var q Quote
	if e = load(ctx, tx, `SELECT snapshot FROM locked_quotes WHERE quote_id=$1 FOR SHARE`, in.QuoteID, &q); e != nil {
		return Allocation{}, e
	}
	if q.PayerWalletID != w.WalletID || q.PayerConsentRef != in.ConsentRef || q.QuoteVersion != in.QuoteVersion || q.AmountMinor != in.AmountMinor || q.Currency != in.Currency || q.Currency != w.Currency {
		return Allocation{}, core.Fail(403, "WALLET_OBLIGATION_CONTEXT_MISMATCH")
	}
	var policy Policy
	if e = load(ctx, tx, `SELECT snapshot FROM financial_policies WHERE policy_id=$1`, q.PolicyID, &policy); e != nil {
		return Allocation{}, e
	}
	rp, e := s.remedyPolicy(ctx, tx, q.PolicyID)
	if e != nil {
		return Allocation{}, e
	}
	if !rp.WalletSpend {
		return Allocation{}, core.Fail(403, "WALLET_SPEND_POLICY_DISABLED")
	}
	if policy.RequiresCustody {
		return Allocation{}, core.Fail(403, "APPROVED_CUSTODY_REQUIRED")
	}
	if e = s.currentWallet(ctx, tx, w.WalletID, q.LegalPartyRef); e != nil {
		return Allocation{}, e
	}
	var provider ids.ID
	var source string
	var cfgRaw []byte
	var entity, currency string
	if e = tx.QueryRow(ctx, `SELECT provider_account_id,legal_entity,currency FROM custody_pools WHERE pool_id=$1 AND NOT restricted FOR UPDATE`, in.PoolID).Scan(&provider, &entity, &currency); e != nil {
		return Allocation{}, core.Fail(409, "FUNDED_CUSTOMER_POOL_REQUIRED")
	}
	if entity != w.LegalEntity || currency != w.Currency {
		return Allocation{}, fail("LEDGER_DIMENSION_MISMATCH")
	}
	if e = tx.QueryRow(ctx, `SELECT source_service FROM locked_quotes WHERE quote_id=$1`, q.ID).Scan(&source); e != nil {
		return Allocation{}, e
	}
	if e = tx.QueryRow(ctx, `SELECT config FROM provider_accounts WHERE provider_account_id=$1`, provider).Scan(&cfgRaw); e != nil {
		return Allocation{}, e
	}
	available, e := s.Account(trusted(ctx), tx, &w.WalletID, in.PoolID, "AVAILABLE", entity, currency, "")
	if e != nil {
		return Allocation{}, e
	}
	pending, e := s.Account(trusted(ctx), tx, &w.WalletID, in.PoolID, "PAYMENT_PENDING", entity, currency, "")
	if e != nil {
		return Allocation{}, e
	}
	intent := finance.Intent{ID: ids.New(), WalletID: w.WalletID, AmountMinor: q.AmountMinor, Currency: currency, LegalEntity: entity, ProviderAccountID: provider, PoolID: in.PoolID, State: "VERIFIED_SUCCESS", SettlementState: "SETTLED"}
	intent.Reference = "cfw-" + intent.ID.String()
	// Internal wallet funding is explicit in the immutable route snapshot; it
	// never calls a PSP and never pretends to be a new external collection.
	_, e = tx.Exec(ctx, `INSERT INTO payment_intents(intent_id,source_service,wallet_id,operation_key,fingerprint,amount_minor,currency,legal_entity,category,country,evidence_ref,policy_version,provider_account_id,pool_id,route_snapshot,provider_reference,state,settlement_state,locked_quote_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb||'{"funding_source":"CUSTOMER_WALLET"}'::jsonb,$16,'VERIFIED_SUCCESS','SETTLED',$17)`, intent.ID, source, w.WalletID, op, core.Fingerprint(in), q.AmountMinor, currency, entity, policy.Key, q.Jurisdiction, in.ConsentRef, policy.Version, provider, in.PoolID, string(cfgRaw), intent.Reference, q.ID)
	if e != nil {
		return Allocation{}, e
	}
	j, e := s.PostTx(trusted(ctx), tx, core.Posting{OperationRef: op, PoolID: in.PoolID, LegalEntity: entity, Currency: currency, PolicyVersion: policy.Version, Authorization: map[string]string{"evidence_ref": in.ConsentRef, "approved_by": "authenticated-customer"}, Entries: []core.Entry{{AccountID: available, Side: "DEBIT", AmountMinor: q.AmountMinor}, {AccountID: pending, Side: "CREDIT", AmountMinor: q.AmountMinor}}})
	if e != nil {
		return Allocation{}, e
	}
	allocation, e := s.allocateTx(trusted(ctx), tx, intent, q, policy, AllocationInput{IntentID: intent.ID, Version: 1, Lines: in.Lines}, source)
	if e != nil {
		return allocation, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO transaction_funding(funding_id,intent_id,journal_id,snapshot,fingerprint) VALUES($1,$2,$3,$4,$5)`, ids.New(), intent.ID, j, raw(map[string]any{"funding_source": "CUSTOMER_WALLET", "spend_key": op, "custody_evidence_ref": ""}), core.Fingerprint(in))
	if e != nil {
		return allocation, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO wallet_spends(spend_id,quote_id,intent_id,wallet_id,operation_key,fingerprint,snapshot,journal_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ids.New(), q.ID, intent.ID, w.WalletID, op, core.Fingerprint(in), raw(in), j)
	if e != nil {
		return allocation, e
	}
	if e = s.Audit(ctx, tx, "customer.wallet_spent", intent.ID.String(), map[string]any{"quote_id": q.ID, "wallet_id": w.WalletID, "journal_id": j}); e != nil {
		return allocation, e
	}
	if e = s.Emit(ctx, tx, "finance.wallet_spent.v1", "allocation", allocation.ID, allocation); e != nil {
		return allocation, e
	}
	return allocation, tx.Commit(ctx)
}

// RecurringDesign is descriptive, non-executable and carries no payment secret,
// provider mandate ID, automatic debit method or worker registration.
type RecurringDesign struct {
	Execution            string `json:"execution"`
	ConsentRequired      bool   `json:"consent_required"`
	ObligationRequired   bool   `json:"obligation_required"`
	LowBalanceNoticeOnly bool   `json:"low_balance_notice_only"`
}

func ReservedRecurringDesign() RecurringDesign { return RecurringDesign{"DEFERRED", true, true, true} }
