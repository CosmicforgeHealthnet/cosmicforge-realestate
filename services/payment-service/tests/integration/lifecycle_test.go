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

func admin() context.Context    { return ctx(platform.AdminService, "", ids.Nil) }
func verifier() context.Context { return ctx(platform.VerificationService, "", ids.Nil) }

type payoutFixture struct {
	*fakeProvider
	mu            sync.Mutex
	charge        providers.Charge
	transfer      providers.Transfer
	submissions   atomic.Int32
	unknown       bool
	settled       bool
	balanceSet    bool
	balanceMinor  int64
	refunds       map[string]providers.Refund
	refundCalls   atomic.Int32
	refundUnknown bool
	reversal      providers.Reversal
}

func (p *payoutFixture) FetchSettlement(context.Context, string) (providers.Settlement, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	date := time.Now().Add(-time.Minute)
	state := "pending"
	if p.settled {
		state = "settled"
	}
	return providers.Settlement{Reference: "42", AmountMinor: p.charge.AmountMinor, FeeMinor: p.charge.FeeMinor, Currency: p.charge.Currency, State: state, SettledAt: &date}, nil
}
func (p *payoutFixture) SettlementTransactions(context.Context, string) ([]providers.Charge, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return []providers.Charge{p.charge}, nil
}
func (p *payoutFixture) FetchBalance(context.Context) ([]providers.MerchantBalance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	amount := int64(1000000000)
	if p.balanceSet {
		amount = p.balanceMinor
	}
	return []providers.MerchantBalance{{Currency: "NGN", AmountMinor: amount}}, nil
}
func (p *payoutFixture) VerifyBank(_ context.Context, b providers.Bank) (providers.Bank, error) {
	return b, nil
}
func (p *payoutFixture) CreateRecipient(context.Context, providers.Bank) (string, error) {
	return "RCP_fixture", nil
}
func (p *payoutFixture) FetchRecipient(context.Context, string) (providers.Recipient, error) {
	return providers.Recipient{Reference: "RCP_fixture", Currency: "NGN", MerchantID: 1001, Mode: "test", Active: true}, nil
}
func (p *payoutFixture) InitiateTransfer(_ context.Context, in providers.Transfer) (providers.Transfer, error) {
	p.submissions.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	in.Mode = "test"
	in.MerchantID = 1001
	in.State = "PROCESSING"
	in.FeeKnown = true
	p.transfer = in
	if p.unknown {
		return in, providers.ErrUnknown
	}
	return in, nil
}
func (p *payoutFixture) VerifyTransfer(context.Context, string) (providers.Transfer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unknown {
		return providers.Transfer{}, providers.ErrUnknown
	}
	return p.transfer, nil
}
func (p *payoutFixture) FinalizeOTP(context.Context, string, string) error {
	return providers.ErrUnsupported
}

type lifeFixture struct {
	*fixture
	l         *lifecycle.Service
	p         *payoutFixture
	policy    lifecycle.Policy
	authority lifecycle.Authority
	quote     lifecycle.Quote
	intent    finance.Intent
}

func newLife(t *testing.T, key string, amount int64) *lifeFixture {
	f := setup(t)
	l := lifecycle.New(f.s)
	p := &payoutFixture{fakeProvider: f.fake, settled: true}
	c := f.cfg
	c.Categories = []string{"RENT"}
	for _, family := range lifecycle.Families() {
		c.Categories = append(c.Categories, family.Key)
	}
	c.PayoutApproved = true
	c.PayoutApprovalRef = "test-only-payout-approval"
	c.PayoutFeeApproved = true
	c.RefundApproved = true
	c.RefundApprovalRef = "test-only-refund-approval"
	c.Capabilities = append(c.Capabilities, "TRANSFER")
	if e := f.s.Register(system(), c, finance.Adapter{Collection: f.fake, DVA: f.fake, Settlement: p, Payout: p, Refund: p, Reversal: p}); e != nil {
		t.Fatal(e)
	}
	f.cfg = c
	expires := time.Now().Add(24 * time.Hour)
	if e := f.s.Verification(verifier(), f.w.IdentityID, "kyc", "VERIFIED", expires); e != nil {
		t.Fatal(e)
	}
	policy := makePolicy(key)
	if _, e := l.PutPolicy(admin(), policy); e != nil {
		t.Fatal(e)
	}
	a := lifecycle.Authority{ID: ids.New(), Domain: "individual-owner", BusinessRef: "property:accepted", WalletID: &f.w.WalletID, LegalPartyRef: "individual-owner:" + f.account.String(), Role: "OWNER", Currency: "NGN", LegalEntity: f.w.LegalEntity, Components: []string{"RENT"}, EvidenceRef: "verified-ownership-v1", OwnershipBPS: 10000, MaxMinor: 1000000000, ExpiresAt: expires}
	if _, e := l.PutAuthority(f.user, a); e != nil {
		t.Fatal(e)
	}
	q := lifecycle.Quote{ID: ids.New(), Domain: "individual-owner", BusinessRef: a.BusinessRef, PayerWalletID: f.w.WalletID, PayerType: "TENANT", PayerConsentRef: "signed-payer-consent", LegalPartyRef: a.LegalPartyRef, QuoteVersion: "accepted-v1", AgreementRef: "lease", AgreementVersion: "v1", PolicyID: policy.ID, Currency: "NGN", Jurisdiction: "NG", Schedule: "ANNUAL", AmountMinor: amount, Components: []lifecycle.Component{{Ref: "rent", Kind: "RENT", AmountMinor: amount, PayerRef: a.LegalPartyRef, Recurrence: "RECURRING"}}}
	if _, e := l.LockQuote(f.user, q, "quote"); e != nil {
		t.Fatal(e)
	}
	in := finance.IntentInput{LockedQuoteID: &q.ID, WalletID: f.w.WalletID, AmountMinor: amount, Currency: "NGN", Country: "NG", Category: key, EvidenceRef: q.QuoteVersion, PolicyVersion: policy.Version, Email: "tenant@example.invalid"}
	i, e := f.s.CreateIntent(f.user, in, "economic-intent")
	if e != nil {
		t.Fatal(e)
	}
	f.fake.success(i)
	i, e = f.s.VerifyIntent(f.user, i.ID)
	if e != nil {
		t.Fatal(e)
	}
	p.charge, _ = f.fake.VerifyCharge(system(), i.Reference)
	return &lifeFixture{f, l, p, policy, a, q, i}
}
func makePolicy(key string) lifecycle.Policy {
	return lifecycle.Policy{ID: ids.New(), Key: key, Version: "approved-fixture-v1", Jurisdiction: "NG", Currency: "NGN", ContractType: "fixture-contract", Product: "fixture-product", EffectiveFrom: time.Now().Add(-365 * 24 * time.Hour), Active: true, ApprovalRef: "test-only-commercial-policy", CancellationRef: "approved-cancellation-ref", RefundRef: "approved-refund-ref", DisputeRef: "approved-dispute-ref", AdjustmentRef: "approved-adjustment-ref", Conditions: []string{"POSSESSION"}, EvidenceServices: []string{string(platform.IndividualOwnerService)}, Schedules: []string{"ANNUAL", "SEMIANNUAL", "QUARTERLY", "MONTHLY", "CUSTOM"}, WithdrawalLimitMinor: 1000000000, DualApprovalAboveMinor: 10000}
}
func (f *lifeFixture) allocate(t *testing.T) lifecycle.Allocation {
	t.Helper()
	a, e := f.l.Allocate(f.user, lifecycle.AllocationInput{IntentID: f.intent.ID, Version: 1, Lines: []lifecycle.LineInput{{AuthorityID: f.authority.ID, ComponentRef: "rent", Kind: "BENEFICIARY", AmountMinor: f.intent.AmountMinor, Currency: "NGN"}}})
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func (f *lifeFixture) fund(t *testing.T) {
	t.Helper()
	e := f.l.ConfirmFunding(admin(), lifecycle.FundingInput{IntentID: f.intent.ID, SettlementRef: "42", CashEvidenceRef: "bank-reconciled-fixture", Approvers: []string{"finance-a", "finance-b"}})
	if e != nil {
		t.Fatal(e)
	}
}
func (f *lifeFixture) evidence(t *testing.T, line ids.ID) ids.ID {
	t.Helper()
	id := ids.New()
	e := f.l.SubmitEvidence(f.user, lifecycle.Evidence{ID: id, LineID: line, Condition: "POSSESSION", Reference: "owner-possession-evidence", Version: "v1", PolicyVersion: f.policy.Version, ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	return id
}
func TestLifecycleAllocationImmutabilityAndCorrection(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	in := lifecycle.AllocationInput{IntentID: f.intent.ID, Version: 1, Lines: []lifecycle.LineInput{{AuthorityID: f.authority.ID, ComponentRef: "rent", Kind: "BENEFICIARY", AmountMinor: 10000, Currency: "NGN"}}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	idsSeen := map[ids.ID]bool{}
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, e := f.l.Allocate(f.user, in)
			if e != nil {
				t.Error(e)
				return
			}
			mu.Lock()
			idsSeen[a.ID] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(idsSeen) != 1 {
		t.Fatal(idsSeen)
	}
	var a lifecycle.Allocation
	for id := range idsSeen {
		a, _ = f.l.Allocation(f.user, id)
	}
	if _, e := f.db.Pool.Exec(system(), `UPDATE allocation_snapshots SET snapshot='{}' WHERE allocation_id=$1`, a.ID); e == nil {
		t.Fatal("mutable snapshot")
	}
	in.Lines[0].AmountMinor = 9999
	if _, e := f.l.Allocate(f.user, in); e == nil {
		t.Fatal("conflicting allocation")
	}
	in.Version = 2
	in.SupersedesID = &a.ID
	in.CorrectionRef = "approved-correction"
	if _, e := f.l.Allocate(f.user, in); e == nil {
		t.Fatal("residual accepted")
	}
	in.Lines[0].AmountMinor = 10000
	if _, e := f.l.Allocate(f.user, in); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := f.db.Pool.QueryRow(system(), `SELECT count(*) FROM allocation_snapshots`).Scan(&count); e != nil || count != 2 {
		t.Fatal(count, e)
	}
}
func TestLifecycleReleaseFundingEvidenceAndConcurrency(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	a := f.allocate(t)
	line := a.Lines[0].ID
	ev := f.evidence(t, line)
	in := lifecycle.Release{LineID: line, AmountMinor: 1000, PolicyVersion: f.policy.Version, EvidenceIDs: []ids.ID{ev}}
	if _, e := f.l.Release(f.user, in, "unfunded"); e == nil {
		t.Fatal("unfunded release")
	}
	f.fund(t)
	missing := in
	missing.EvidenceIDs = nil
	if _, e := f.l.Release(f.user, missing, "missing"); e == nil {
		t.Fatal("no milestone")
	}
	var wg sync.WaitGroup
	var success atomic.Int32
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if _, e := f.l.Release(f.user, in, fmt.Sprint(n)); e == nil {
				success.Add(1)
			}
		}(n)
	}
	wg.Wait()
	if success.Load() != 10 {
		t.Fatal(success.Load())
	}
	out, e := f.l.Allocation(f.user, a.ID)
	if e != nil || out.Lines[0].ReleasedMinor != 10000 {
		t.Fatal(out, e)
	}
	for _, kind := range []string{"PROTECTED", "AVAILABLE"} {
		var balance string
		e = f.db.Pool.QueryRow(system(), `SELECT sum(balance_minor)::text FROM account_balances WHERE dimension_ref=$1 AND kind=$2`, "allocation:"+line.String(), kind).Scan(&balance)
		if e != nil {
			t.Fatal(e)
		}
		want := "0"
		if kind == "AVAILABLE" {
			want = "10000"
		}
		if balance != want {
			t.Fatal(balance, want)
		}
	}
}
func (f *lifeFixture) destination(t *testing.T) ids.ID {
	t.Helper()
	expiry := time.Now().Add(time.Hour)
	p, e := f.s.RecordPayoutReference(verifier(), core.PayoutReferenceInput{WalletID: f.w.WalletID, BeneficiaryRef: "verified-bank-fixture", VerificationRef: "kyc", Jurisdiction: "NG", ExpiresAt: expiry})
	if e != nil {
		t.Fatal(e)
	}
	e = f.l.BindDestination(verifier(), lifecycle.Destination{ID: ids.New(), PayoutAccountID: p.ID, ProviderID: f.cfg.ID, RecipientRef: "RCP_fixture", EvidenceRef: "verifier-attested-bank-owner", ExpiresAt: expiry.Add(-time.Minute)})
	if e != nil {
		t.Fatal(e)
	}
	return p.ID
}
func (f *lifeFixture) withdrawal(t *testing.T, line, destination ids.ID, amount int64) lifecycle.Withdrawal {
	t.Helper()
	ap := lifecycle.WithdrawalApproval{ID: ids.New(), LineID: line, WalletID: f.w.WalletID, PayoutAccountID: destination, AmountMinor: amount, StepUpRef: "identity-step-up-session", RiskRef: "approved-risk-decision", Approvers: []string{"a", "b"}, ExpiresAt: time.Now().Add(45 * time.Minute)}
	if e := f.l.ApproveWithdrawal(admin(), ap); e != nil {
		t.Fatal(e)
	}
	return lifecycle.Withdrawal{ApprovalID: ap.ID, LineID: line, WalletID: f.w.WalletID, PayoutAccountID: destination, AmountMinor: amount}
}
func TestLifecyclePayoutUnknownReservationReadbackAndRace(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	a := f.allocate(t)
	line := a.Lines[0].ID
	f.fund(t)
	ev := f.evidence(t, line)
	if _, e := f.l.Release(f.user, lifecycle.Release{LineID: line, AmountMinor: 10000, PolicyVersion: f.policy.Version, EvidenceIDs: []ids.ID{ev}}, "all"); e != nil {
		t.Fatal(e)
	}
	destination := f.destination(t)
	in := f.withdrawal(t, line, destination, 10000)
	f.p.unknown = true
	p, e := f.l.Withdraw(f.user, in, "one")
	if e != nil || p.State != "UNKNOWN_OUTCOME" {
		t.Fatal(p, e)
	}
	for n := 0; n < 5; n++ {
		if _, e = f.l.Withdraw(f.user, in, "one"); e != nil {
			t.Fatal(e)
		}
		if _, e = f.l.ResolvePayout(f.user, p.ID); e != nil {
			t.Fatal(e)
		}
	}
	if f.p.submissions.Load() != 1 {
		t.Fatal("duplicate provider effect")
	}
	other := f.withdrawal(t, line, destination, 1)
	if _, e = f.l.Withdraw(f.user, other, "other"); e == nil {
		t.Fatal("overspent reservation")
	}
	f.p.mu.Lock()
	f.p.unknown = false
	f.p.transfer.State = "CONFIRMED_PAID"
	f.p.mu.Unlock()
	body := []byte(fmt.Sprintf(`{"event":"transfer.success","data":{"reference":%q}}`, p.Reference))
	if e = f.s.IngestWebhook(ctx(platform.APIGateway, "", ids.Nil), f.cfg.ID, body, "valid-test-fixture"); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if n%2 == 0 {
				_, e := f.s.ProcessInbox(system())
				if e != nil {
					t.Error(e)
				}
			} else {
				_, e := f.l.ResolvePayout(f.user, p.ID)
				if e != nil {
					t.Error(e)
				}
			}
		}(n)
	}
	wg.Wait()
	out, e := f.l.Payout(f.user, p.ID)
	if e != nil || out.State != "CONFIRMED_PAID" || f.p.submissions.Load() != 1 {
		t.Fatal(out, e)
	}
	var journals int
	e = f.db.Pool.QueryRow(system(), `SELECT count(*) FROM journals WHERE operation_ref=$1`, "payout-terminal:"+p.ID.String()).Scan(&journals)
	if e != nil || journals != 1 {
		t.Fatal(journals, e)
	}
}
func TestLifecycleConcurrentWithdrawalsAndNoEffectFailure(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	a := f.allocate(t)
	line := a.Lines[0].ID
	f.fund(t)
	ev := f.evidence(t, line)
	if _, e := f.l.Release(f.user, lifecycle.Release{LineID: line, AmountMinor: 10000, PolicyVersion: f.policy.Version, EvidenceIDs: []ids.ID{ev}}, "all"); e != nil {
		t.Fatal(e)
	}
	destination := f.destination(t)
	ins := []lifecycle.Withdrawal{f.withdrawal(t, line, destination, 10000), f.withdrawal(t, line, destination, 10000)}
	var wg sync.WaitGroup
	outs := make(chan lifecycle.Payout, 2)
	for n, in := range ins {
		wg.Add(1)
		go func(n int, in lifecycle.Withdrawal) {
			defer wg.Done()
			p, e := f.l.Withdraw(f.user, in, fmt.Sprint(n))
			if e == nil {
				outs <- p
			}
		}(n, in)
	}
	wg.Wait()
	close(outs)
	if len(outs) != 1 || f.p.submissions.Load() != 1 {
		t.Fatal(len(outs), f.p.submissions.Load())
	}
	p := <-outs
	f.p.mu.Lock()
	f.p.transfer.State = "CONFIRMED_FAILED"
	f.p.transfer.NoEffectConfirmed = false
	f.p.mu.Unlock()
	out, e := f.l.ResolvePayout(f.user, p.ID)
	if e != nil || out.State != "UNKNOWN_OUTCOME" {
		t.Fatal(out, e)
	}
	f.p.mu.Lock()
	f.p.transfer.NoEffectConfirmed = true
	f.p.mu.Unlock()
	out, e = f.l.ResolvePayout(f.user, p.ID)
	if e != nil || out.State != "CONFIRMED_FAILED" {
		t.Fatal(out, e)
	}
	retry := f.withdrawal(t, line, destination, 10000)
	if _, e = f.l.Withdraw(f.user, retry, "authorized-retry"); e != nil {
		t.Fatal(e)
	}
	if f.p.submissions.Load() != 2 {
		t.Fatal(f.p.submissions.Load())
	}
}
