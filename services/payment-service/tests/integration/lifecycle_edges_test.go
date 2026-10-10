package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func validateLifecycleOutbox(t *testing.T, f *lifeFixture) {
	t.Helper()
	rows, e := f.s.Outbox.Claim(system(), "lifecycle-contract-evidence", 200, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range rows {
		body, e := json.Marshal(row.Envelope)
		if e != nil {
			t.Fatal(e)
		}
		testkit.ValidateJSONSchema(t, "../../../../contracts/events/payments/"+row.Envelope.EventType+".schema.json", body)
	}
}

func collectQuote(t *testing.T, f *lifeFixture, q lifecycle.Quote, policy lifecycle.Policy, key string) finance.Intent {
	t.Helper()
	if _, e := f.l.LockQuote(f.user, q, key); e != nil {
		t.Fatal(e)
	}
	return collectLockedQuote(t, f, q, policy, key)
}
func collectLockedQuote(t *testing.T, f *lifeFixture, q lifecycle.Quote, policy lifecycle.Policy, key string) finance.Intent {
	t.Helper()
	i, e := f.s.CreateIntent(f.user, finance.IntentInput{LockedQuoteID: &q.ID, WalletID: q.PayerWalletID, AmountMinor: q.AmountMinor, Currency: q.Currency, Country: q.Jurisdiction, Category: policy.Key, PolicyVersion: policy.Version, EvidenceRef: q.QuoteVersion, Email: "payer@example.invalid", RequiresCustody: policy.RequiresCustody}, key)
	if e != nil {
		t.Fatal(e)
	}
	f.fake.success(i)
	if i, e = f.s.VerifyIntent(f.user, i.ID); e != nil {
		t.Fatal(e)
	}
	return i
}

func TestLifecycleDepositSegregationAndPayerReceiptPrivacy(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	payerAccount := ids.New()
	payerCtx := ctx(platform.MarketplaceService, platform.DomainMarketplace, payerAccount)
	payer, e := f.s.EnsureWallet(payerCtx, core.Provision{Domain: "marketplace", AccountID: payerAccount, Role: "CUSTOMER", PartyKind: "PERSON", LegalEntity: f.w.LegalEntity, Currency: "NGN", RegistrationRef: "payer-registration"})
	if e != nil {
		t.Fatal(e)
	}
	q := f.quote
	q.ID = ids.New()
	q.PayerWalletID = payer.WalletID
	q.LegalPartyRef = "marketplace:" + payerAccount.String()
	q.Components = append([]lifecycle.Component(nil), f.quote.Components...)
	q.Components[0].PayerRef = q.LegalPartyRef
	i := collectQuote(t, f, q, f.policy, "payer-rent")
	a, e := f.l.Allocate(f.user, lifecycle.AllocationInput{IntentID: i.ID, Version: 1, Lines: []lifecycle.LineInput{{AuthorityID: f.authority.ID, ComponentRef: "rent", Kind: "BENEFICIARY", AmountMinor: 10000, Currency: "NGN"}}})
	if e != nil {
		t.Fatal(e)
	}
	receipt, e := f.l.Receipt(payerCtx, q.ID)
	if e != nil || receipt.CollectionState != "VERIFIED_SUCCESS" || receipt.Quote.AmountMinor != 10000 || len(receipt.Allocations) != 0 {
		t.Fatal(receipt, e)
	}
	if _, e = f.l.Allocation(payerCtx, a.ID); e == nil {
		t.Fatal("payer accessed owner allocation")
	}
	p := makePolicy("RESIDENTIAL_DEPOSIT")
	p.Conditions = []string{"MOVE_OUT"}
	if _, e = f.l.PutPolicy(admin(), p); e != nil {
		t.Fatal(e)
	}
	auth := f.authority
	auth.ID = ids.New()
	auth.Components = []string{"DEPOSIT"}
	if _, e = f.l.PutAuthority(f.user, auth); e != nil {
		t.Fatal(e)
	}
	q.ID = ids.New()
	q.PolicyID = p.ID
	q.Components = []lifecycle.Component{{Ref: "deposit", Kind: "DEPOSIT", AmountMinor: 10000, PayerRef: q.LegalPartyRef, Recurrence: "REFUNDABLE_DEPOSIT"}}
	i = collectQuote(t, f, q, p, "payer-deposit")
	in := lifecycle.AllocationInput{IntentID: i.ID, Version: 1, Lines: []lifecycle.LineInput{{AuthorityID: auth.ID, ComponentRef: "deposit", Kind: "BENEFICIARY", AmountMinor: 10000, Currency: "NGN"}}}
	if _, e = f.l.Allocate(f.user, in); e == nil {
		t.Fatal("deposit converted to proceeds")
	}
	in.Lines[0].Kind = "DEPOSIT"
	a, e = f.l.Allocate(f.user, in)
	if e != nil {
		t.Fatal(e)
	}
	var deposit, available string
	e = f.db.Pool.QueryRow(system(), `SELECT COALESCE(sum(balance_minor) FILTER(WHERE kind='DEPOSIT'),0)::text,COALESCE(sum(balance_minor) FILTER(WHERE kind='AVAILABLE'),0)::text FROM account_balances WHERE wallet_id=$1`, payer.WalletID).Scan(&deposit, &available)
	if e != nil || deposit != "10000" || available != "0" {
		t.Fatal(deposit, available, e)
	}
	if _, e = f.l.Release(f.user, lifecycle.Release{LineID: a.Lines[0].ID, AmountMinor: 10000, PolicyVersion: p.Version}, "deposit-release"); e == nil {
		t.Fatal("deposit released as income")
	}
	if _, e = f.l.Withdraw(payerCtx, lifecycle.Withdrawal{LineID: a.Lines[0].ID, WalletID: payer.WalletID, AmountMinor: 1}, "deposit-withdraw"); e == nil {
		t.Fatal("deposit withdrawn")
	}
	coverage, e := f.s.Coverage(admin(), f.cfg.PoolID)
	if e != nil || coverage.LiabilityMinor != "30000" {
		t.Fatal(coverage, e)
	}
}

func TestLifecyclePayoutFeeCoverageUnknownFeeAndHeldFailure(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	a := f.allocate(t)
	line := a.Lines[0].ID
	f.fund(t)
	ev := f.evidence(t, line)
	if _, e := f.l.Release(f.user, lifecycle.Release{LineID: line, AmountMinor: 10000, PolicyVersion: f.policy.Version, EvidenceIDs: []ids.ID{ev}}, "release"); e != nil {
		t.Fatal(e)
	}
	f.cfg.PayoutFeeMinor = 100
	if e := f.s.Register(system(), f.cfg, finance.Adapter{Collection: f.fake, DVA: f.fake, Settlement: f.p, Payout: f.p}); e != nil {
		t.Fatal(e)
	}
	destination := f.destination(t)
	in := f.withdrawal(t, line, destination, 10000)
	if _, e := f.l.Withdraw(f.user, in, "fee-reservation"); e == nil || f.p.submissions.Load() != 0 {
		t.Fatal("spent beneficiary money on provider fee", e)
	}
	// An explicitly reconciled test treasury contribution funds provider costs.
	tx, e := f.db.Pool.Begin(system())
	if e != nil {
		t.Fatal(e)
	}
	cashAccount, e := f.s.Account(system(), tx, nil, f.cfg.PoolID, "SETTLED_CASH", f.w.LegalEntity, "NGN", "")
	if e != nil {
		t.Fatal(e)
	}
	treasury, e := f.s.Account(system(), tx, nil, f.cfg.PoolID, "TREASURY", f.w.LegalEntity, "NGN", "")
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(system()); e != nil {
		t.Fatal(e)
	}
	if _, e := f.post("test-treasury-fee", []core.Entry{{AccountID: cashAccount, Side: "DEBIT", AmountMinor: 100}, {AccountID: treasury, Side: "CREDIT", AmountMinor: 100}}); e != nil {
		t.Fatal(e)
	}
	payout, e := f.l.Withdraw(f.user, in, "fee-reservation")
	if e != nil {
		t.Fatal(e)
	}
	f.p.mu.Lock()
	f.p.transfer.State = "CONFIRMED_PAID"
	f.p.transfer.FeeKnown = false
	f.p.mu.Unlock()
	result, e := f.l.ResolvePayout(f.user, payout.ID)
	if e != nil || result.State != "UNKNOWN_OUTCOME" {
		t.Fatal(result, e)
	}
	f.p.mu.Lock()
	f.p.transfer.FeeKnown = true
	f.p.transfer.FeeMinor = 101
	f.p.mu.Unlock()
	result, e = f.l.ResolvePayout(f.user, payout.ID)
	if e != nil || result.State != "UNKNOWN_OUTCOME" {
		t.Fatal(result, e)
	}
	if e = f.l.Hold(f.user, line, lifecycle.Hold{State: "HELD", EvidenceRef: "review-after-submission"}, "hold"); e != nil {
		t.Fatal(e)
	}
	f.p.mu.Lock()
	f.p.transfer.State = "CONFIRMED_FAILED"
	f.p.transfer.NoEffectConfirmed = true
	f.p.transfer.FeeMinor = 100
	f.p.mu.Unlock()
	result, e = f.l.ResolvePayout(f.user, payout.ID)
	if e != nil || result.State != "CONFIRMED_FAILED" {
		t.Fatal(result, e)
	}
	var frozen, available, expense, cash string
	e = f.db.Pool.QueryRow(system(), `SELECT COALESCE(sum(balance_minor) FILTER(WHERE kind='FROZEN'),0)::text,COALESCE(sum(balance_minor) FILTER(WHERE kind='AVAILABLE'),0)::text,COALESCE(sum(balance_minor) FILTER(WHERE kind='PROCESSING_EXPENSE'),0)::text,COALESCE(sum(balance_minor) FILTER(WHERE kind='SETTLED_CASH'),0)::text FROM account_balances`).Scan(&frozen, &available, &expense, &cash)
	if e != nil || frozen != "10000" || available != "0" || expense != "100" || cash != "10000" {
		t.Fatal(frozen, available, expense, cash, e)
	}
	for n := 0; n < 3; n++ {
		if _, e = f.l.ResolvePayout(f.user, payout.ID); e != nil {
			t.Fatal(e)
		}
	}
	if f.p.submissions.Load() != 1 {
		t.Fatal("resubmitted terminal attempt")
	}
	validateLifecycleOutbox(t, f)
}

func TestLifecycleCustodyPolicyCannotBypassProviderGate(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	p := makePolicy("OFF_PLAN")
	p.Conditions = []string{"INDEPENDENT_CERTIFICATION"}
	p.EvidenceServices = []string{string(platform.VerificationService)}
	p.RequiresCustody = true
	if _, e := f.l.PutPolicy(admin(), p); e != nil {
		t.Fatal(e)
	}
	q := f.quote
	q.ID = ids.New()
	q.PolicyID = p.ID
	q.PayerType = "BUYER"
	q.Components = []lifecycle.Component{{Ref: "milestone", Kind: "CONSTRUCTION", AmountMinor: 10000, PayerRef: q.LegalPartyRef, Recurrence: "ONE_TIME"}}
	if _, e := f.l.LockQuote(f.user, q, "custody-quote"); e != nil {
		t.Fatal(e)
	}
	in := finance.IntentInput{LockedQuoteID: &q.ID, WalletID: q.PayerWalletID, AmountMinor: 10000, Currency: "NGN", Country: "NG", Category: p.Key, PolicyVersion: p.Version, EvidenceRef: q.QuoteVersion, Email: "buyer@example.invalid"}
	if _, e := f.s.CreateIntent(f.user, in, "bypass-custody"); e == nil {
		t.Fatal("custody flag omitted")
	}
	in.RequiresCustody = true
	if _, e := f.s.CreateIntent(f.user, in, "approved-custody"); e == nil {
		t.Fatal("unapproved custody routed")
	}
	if f.fake.initializes.Load() != 1 {
		t.Fatal("custody charge initialized")
	}
}
