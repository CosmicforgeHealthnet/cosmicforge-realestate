package integration

import (
	"context"
	"fmt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func customerFixture(t *testing.T, f *lifeFixture) (core.Wallet, context.Context, lifecycle.Policy) {
	return customerFixtureFor(t, f, ids.New())
}
func customerFixtureFor(t *testing.T, f *lifeFixture, account ids.ID) (core.Wallet, context.Context, lifecycle.Policy) {
	t.Helper()
	user := ctx(platform.MarketplaceService, platform.DomainMarketplace, account)
	w, e := f.s.EnsureWallet(user, core.Provision{Domain: "marketplace", AccountID: account, Role: "CUSTOMER", PartyKind: "PERSON", LegalEntity: f.w.LegalEntity, Currency: "NGN", RegistrationRef: "marketplace-customer"})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Verification(verifier(), w.IdentityID, "customer-kyc", "VERIFIED", time.Now().Add(24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	policy := makePolicy("CUSTOMER_FUNDING")
	policy.Conditions = []string{"VERIFIED_FUNDING"}
	if _, e = f.l.PutPolicy(admin(), policy); e != nil {
		t.Fatal(e)
	}
	if e = f.l.PutRemedyPolicy(admin(), remedyPack(policy.ID)); e != nil {
		t.Fatal(e)
	}
	cfg := f.cfg
	cfg.Categories = append(cfg.Categories, "CUSTOMER_FUNDING")
	if e = f.s.Register(system(), cfg, finance.Adapter{Collection: f.fake, DVA: f.fake, Settlement: f.p, Payout: f.p, Refund: f.p, Reversal: f.p}); e != nil {
		t.Fatal(e)
	}
	return w, user, policy
}
func fundedCustomer(t *testing.T, f *lifeFixture, w core.Wallet, user context.Context, policy lifecycle.Policy) lifecycle.Topup {
	return fundedCustomerAmount(t, f, w, user, policy, 10000)
}
func fundedCustomerAmount(t *testing.T, f *lifeFixture, w core.Wallet, user context.Context, policy lifecycle.Policy, amount int64) lifecycle.Topup {
	t.Helper()
	in := lifecycle.TopupRequest{ID: ids.New(), WalletID: w.WalletID, PolicyID: policy.ID, AmountMinor: amount, Currency: "NGN", Country: "NG", Email: "customer@example.invalid", ConsentRef: "customer-funding-consent", ExpiresAt: time.Now().Add(time.Hour)}
	top, e := f.l.RequestTopup(user, in, "fund")
	if e != nil {
		t.Fatal(e)
	}
	i, e := f.s.GetIntent(user, *top.IntentID)
	if e != nil {
		t.Fatal(e)
	}
	f.fake.success(i)
	if _, e = f.s.VerifyIntent(user, i.ID); e != nil {
		t.Fatal(e)
	}
	f.p.charge, _ = f.fake.VerifyCharge(system(), i.Reference)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := f.l.FundTopup(admin(), lifecycle.TopupFunding{TopupID: top.ID, SettlementRef: "42", CashEvidenceRef: "topup-bank-match", Approvers: []string{"a", "b"}}); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	return top
}

func TestFundedTopupChargebackRemovesCustomerAvailability(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	f.fund(t)
	w, user, policy := customerFixture(t, f)
	top := fundedCustomer(t, f, w, user, policy)
	original, e := f.s.GetIntent(user, *top.IntentID)
	if e != nil {
		t.Fatal(e)
	}
	f.p.mu.Lock()
	f.p.reversal = providers.Reversal{Reference: "dispute:customer", EconomicReference: "refund:customer", ChargeReference: original.Reference, AmountMinor: 10000, Currency: "NGN", Mode: "test", MerchantID: 1001, Confirmed: true}
	f.p.mu.Unlock()
	result, e := f.l.ReverseCollection(admin(), lifecycle.ReversalRequest{IntentID: original.ID, ProviderReference: "dispute:customer", AuthorityRef: "confirmed-original-chargeback", Approvers: []string{"a", "b"}})
	if e != nil || result.RecoverableMinor != 0 {
		t.Fatal(result, e)
	}
	available, e := f.l.Availability(user, w.WalletID, top.PoolID)
	if e != nil || available.AvailableMinor != "0" || available.WithdrawableNow {
		t.Fatal(available, e)
	}
	var debt int
	if e = f.db.Pool.QueryRow(system(), `SELECT count(*) FROM financial_recoverables`).Scan(&debt); e != nil || debt != 0 {
		t.Fatal(debt, e)
	}
	validateLifecycleOutbox(t, f)
}

func TestSpentCustomerTopupRecoverableHasCanonicalReviewContext(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	f.fund(t)
	w, user, policy := customerFixture(t, f)
	top := fundedCustomer(t, f, w, user, policy)
	q := f.quote
	q.ID = ids.New()
	q.PayerWalletID = w.WalletID
	var party string
	if e := f.db.Pool.QueryRow(system(), `SELECT legal_party_ref FROM financial_identities WHERE identity_id=$1`, w.IdentityID).Scan(&party); e != nil {
		t.Fatal(e)
	}
	q.LegalPartyRef = party
	q.Components[0].PayerRef = party
	if _, e := f.l.LockQuote(f.user, q, "customer-work"); e != nil {
		t.Fatal(e)
	}
	if e := f.l.PutRemedyPolicy(admin(), remedyPack(f.policy.ID)); e != nil {
		t.Fatal(e)
	}
	if _, e := f.l.SpendWallet(user, lifecycle.WalletSpend{WalletID: w.WalletID, QuoteID: q.ID, PoolID: f.cfg.PoolID, ConsentRef: q.PayerConsentRef, QuoteVersion: q.QuoteVersion, AmountMinor: 10000, Currency: "NGN", Lines: []lifecycle.LineInput{{AuthorityID: f.authority.ID, ComponentRef: "rent", Kind: "BENEFICIARY", AmountMinor: 10000, Currency: "NGN"}}}, "spend"); e != nil {
		t.Fatal(e)
	}
	original, e := f.s.GetIntent(user, *top.IntentID)
	if e != nil {
		t.Fatal(e)
	}
	f.p.mu.Lock()
	f.p.reversal = providers.Reversal{Reference: "dispute:spent-topup", EconomicReference: "refund:spent-topup", ChargeReference: original.Reference, AmountMinor: 10000, Currency: "NGN", Mode: "test", MerchantID: 1001, Confirmed: true}
	f.p.mu.Unlock()
	reversal, e := f.l.ReverseCollection(admin(), lifecycle.ReversalRequest{IntentID: original.ID, ProviderReference: "dispute:spent-topup", AuthorityRef: "confirmed", Approvers: []string{"a", "b"}})
	if e != nil || reversal.RecoverableMinor != 10000 {
		t.Fatal(reversal, e)
	}
	ev := ids.New()
	if e = f.l.PutRemedyEvidence(f.user, lifecycle.RemedyEvidence{ID: ev, BusinessRef: "topup:" + top.ID.String(), Reference: "liable-party-review", Version: "v1", ActorRef: "independent-review", HashRef: "hash", ObservedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	var debt ids.ID
	if e = f.db.Pool.QueryRow(system(), `SELECT recoverable_id FROM financial_recoverables WHERE reversal_id=$1`, reversal.ID).Scan(&debt); e != nil {
		t.Fatal(e)
	}
	action := lifecycle.RecoverableAction{ID: ids.New(), RecoverableID: debt, WalletID: &f.w.WalletID, Action: "ASSIGN_BENEFICIARY", AuthorityRef: "legal-liability-review", RiskRef: "risk-reviewed", Approvers: []string{"a", "b"}, EvidenceIDs: []ids.ID{ev}}
	if e = f.l.RecoverableAction(admin(), action, "wrong-party"); e == nil {
		t.Fatal("customer debt assigned to owner")
	}
	action.ID = ids.New()
	action.WalletID = &w.WalletID
	if e = f.l.RecoverableAction(admin(), action, "canonical-customer"); e != nil {
		t.Fatal(e)
	}
	validateLifecycleOutbox(t, f)
}
func TestCustomerTopupSettlementAndAtomicWalletSpend(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	f.allocate(t)
	f.fund(t)
	f.remedy(t)
	w, user, policy := customerFixture(t, f)
	top := fundedCustomer(t, f, w, user, policy)
	got, e := f.l.Topup(user, top.ID)
	if e != nil || got.State != "AVAILABLE" {
		t.Fatal(got, e)
	}
	var count int
	if e = f.db.Pool.QueryRow(system(), `SELECT count(*) FROM customer_funding WHERE topup_id=$1`, top.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	q := f.quote
	q.ID = ids.New()
	q.PayerWalletID = w.WalletID
	q.LegalPartyRef = "marketplace:" + accountID(user).String()
	q.PayerConsentRef = "accepted-wallet-obligation"
	q.QuoteVersion = "customer-v1"
	q.Components[0].PayerRef = q.LegalPartyRef
	if _, e = f.l.LockQuote(f.user, q, "customer-quote"); e != nil {
		t.Fatal(e)
	}
	in := lifecycle.WalletSpend{WalletID: w.WalletID, QuoteID: q.ID, PoolID: f.cfg.PoolID, ConsentRef: q.PayerConsentRef, QuoteVersion: q.QuoteVersion, AmountMinor: 10000, Currency: "NGN", Lines: []lifecycle.LineInput{{AuthorityID: f.authority.ID, ComponentRef: "rent", Kind: "BENEFICIARY", AmountMinor: 10000, Currency: "NGN"}}}
	bad := in
	bad.AmountMinor = 9999
	if _, e = f.l.SpendWallet(user, bad, "mismatch"); e == nil {
		t.Fatal("client amount accepted")
	}
	if _, e = f.l.SpendWallet(f.user, in, "owner-debit"); e == nil {
		t.Fatal("owner debited customer")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	allocations := map[ids.ID]bool{}
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, e := f.l.SpendWallet(user, in, "same")
			if e != nil {
				t.Error(e)
				return
			}
			mu.Lock()
			allocations[a.ID] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(allocations) != 1 {
		t.Fatal("duplicate obligation funding", allocations)
	}
	if f.fake.initializes.Load() != 2 {
		t.Fatal("wallet spend charged PSP", f.fake.initializes.Load())
	}
	var avail, protected int64
	if e = f.db.Pool.QueryRow(system(), `SELECT COALESCE(sum(balance_minor) FILTER(WHERE wallet_id=$1 AND kind='AVAILABLE'),0)::bigint,COALESCE(sum(balance_minor) FILTER(WHERE wallet_id=$2 AND kind='PROTECTED'),0)::bigint FROM account_balances`, w.WalletID, f.w.WalletID).Scan(&avail, &protected); e != nil || avail != 0 || protected != 20000 {
		t.Fatal(avail, protected, e)
	}
	q.ID = ids.New()
	q.AmountMinor = 100
	q.Components[0].AmountMinor = 100
	if _, e = f.l.LockQuote(f.user, q, "insufficient-quote"); e != nil {
		t.Fatal(e)
	}
	in.QuoteID = q.ID
	in.AmountMinor = 100
	in.Lines[0].AmountMinor = 100
	if _, e = f.l.SpendWallet(user, in, "insufficient"); e == nil {
		t.Fatal("overspend")
	}
	if e = f.db.Pool.QueryRow(system(), `SELECT count(*) FROM wallet_spends`).Scan(&count); e != nil || count != 1 {
		t.Fatal("partial failed spend", count, e)
	}
	metrics, e := f.l.OperationalMetrics(admin())
	if e != nil || metrics["wallet_payment_failures"] == "0" {
		t.Fatal(metrics, e)
	}
}
func accountID(ctx context.Context) ids.ID { p, _ := core.Caller(ctx); return p.Delegation.ActorID }
func TestDVAUnmatchedPendingAndSingleCustomerFunding(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	f.allocate(t)
	f.fund(t)
	w, user, policy := customerFixture(t, f)
	charge := providers.Charge{Reference: "dva-incoming", State: "VERIFIED_SUCCESS", Mode: "test", MerchantID: 1001, CustomerRef: "CUS_fixture", Currency: "NGN", AmountMinor: 10000}
	f.fake.mu.Lock()
	f.fake.charge = charge
	f.fake.mu.Unlock()
	if e := f.s.MatchDVACharge(system(), f.cfg.ID, charge.Reference); e == nil {
		t.Fatal("unknown customer credited")
	}
	var unmatched ids.ID
	if e := f.db.Pool.QueryRow(system(), `SELECT credit_id FROM unmatched_dva_credits WHERE external_ref=$1`, charge.Reference).Scan(&unmatched); e != nil {
		t.Fatal(e)
	}
	f.fake.identified = true
	v, e := f.s.RequestDVA(user, finance.DVAInput{WalletID: w.WalletID, ProviderAccountID: f.cfg.ID, ConsentRef: "dva-consent", ConsentVersion: "v1", ConsentAt: time.Now().Add(-time.Minute), ConsentExpiresAt: time.Now().Add(time.Hour), VerificationRef: "customer-kyc", Customer: providers.CustomerRequest{Email: "dva@example.invalid", FirstName: "Customer", LastName: "Fixture"}, Validation: providers.Validation{Country: "NG", Type: "bank_account", BVN: "fixture-only", BankCode: "001", AccountNumber: "0123456789"}})
	if e != nil || v.State != "ACTIVE" {
		t.Fatal(v, e)
	}
	if e = f.l.ResolveUnmatchedDVA(admin(), unmatched, "verified-mapping", []string{"a", "b"}); e != nil {
		t.Fatal(e)
	}
	var receipt ids.ID
	if e = f.db.Pool.QueryRow(system(), `SELECT receipt_id FROM collection_receipts WHERE external_ref=$1`, charge.Reference).Scan(&receipt); e != nil {
		t.Fatal(e)
	}
	balances, e := f.s.Balances(user, w.WalletID)
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range balances {
		if b.Kind == "AVAILABLE" && b.AmountMinor != "0" {
			t.Fatal("DVA before settlement")
		}
	}
	in := lifecycle.TopupRequest{ID: ids.New(), WalletID: w.WalletID, PolicyID: policy.ID, ReceiptID: &receipt, AmountMinor: 10000, Currency: "NGN", Country: "NG", ConsentRef: "dva-funding", ExpiresAt: time.Now().Add(time.Hour)}
	top, e := f.l.RequestTopup(user, in, "dva")
	if e != nil {
		t.Fatal(e)
	}
	f.p.charge = charge
	funding := lifecycle.TopupFunding{TopupID: top.ID, SettlementRef: "42", CashEvidenceRef: "DVA-bank-match", Approvers: []string{"a", "b"}}
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for n := 0; n < 10; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := f.l.FundTopup(admin(), funding); e != nil {
				t.Error(e)
			} else {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 10 {
		t.Fatal(accepted.Load())
	}
	if e = f.db.Pool.QueryRow(system(), `SELECT balance_minor::bigint FROM account_balances WHERE wallet_id=$1 AND kind='AVAILABLE' AND dimension_ref=''`, w.WalletID).Scan(new(int64)); e != nil {
		t.Fatal(e)
	}
	// Equal amount, distinct provider reference is a distinct economic receipt.
	charge.Reference = "dva-second"
	f.fake.mu.Lock()
	f.fake.charge = charge
	f.fake.mu.Unlock()
	if e = f.s.MatchDVACharge(system(), f.cfg.ID, charge.Reference); e != nil {
		t.Fatal(e)
	}
	var receipts int
	if e = f.db.Pool.QueryRow(system(), `SELECT count(*) FROM collection_receipts WHERE wallet_id=$1`, w.WalletID).Scan(&receipts); e != nil || receipts != 2 {
		t.Fatal(receipts, e)
	}
	if _, e = f.l.RequestTopup(f.user, in, "owner-topup"); e == nil {
		t.Fatal("noncustomer topup")
	}
}
func TestWalletSpendRollbackAndDistinctConcurrentObligations(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	f.allocate(t)
	f.fund(t)
	f.remedy(t)
	w, user, policy := customerFixture(t, f)
	fundedCustomer(t, f, w, user, policy)
	var wg sync.WaitGroup
	var won atomic.Int32
	for n := 0; n < 2; n++ {
		q := f.quote
		q.ID = ids.New()
		q.PayerWalletID = w.WalletID
		q.LegalPartyRef = "marketplace:" + accountID(user).String()
		q.PayerConsentRef = "consent"
		q.Components = append([]lifecycle.Component(nil), q.Components...)
		q.Components[0].PayerRef = q.LegalPartyRef
		if _, e := f.l.LockQuote(f.user, q, fmt.Sprint("quote", n)); e != nil {
			t.Fatal(e)
		}
		wg.Add(1)
		go func(q lifecycle.Quote, n int) {
			defer wg.Done()
			_, e := f.l.SpendWallet(user, lifecycle.WalletSpend{WalletID: w.WalletID, QuoteID: q.ID, PoolID: f.cfg.PoolID, ConsentRef: "consent", QuoteVersion: q.QuoteVersion, AmountMinor: 10000, Currency: "NGN", Lines: []lifecycle.LineInput{{AuthorityID: f.authority.ID, ComponentRef: "rent", Kind: "BENEFICIARY", AmountMinor: 10000, Currency: "NGN"}}}, fmt.Sprint(n))
			if e == nil {
				won.Add(1)
			}
		}(q, n)
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatal("concurrent overspend", won.Load())
	}
}
