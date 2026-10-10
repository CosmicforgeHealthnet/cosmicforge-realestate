package integration

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"sync"
	"testing"
	"time"
)

func releasedLine(t *testing.T, f *lifeFixture) ids.ID {
	t.Helper()
	a := f.allocate(t)
	f.fund(t)
	ev := f.evidence(t, a.Lines[0].ID)
	if _, e := f.l.Release(f.user, lifecycle.Release{LineID: a.Lines[0].ID, AmountMinor: f.intent.AmountMinor, PolicyVersion: f.policy.Version, EvidenceIDs: []ids.ID{ev}}, "release"); e != nil {
		t.Fatal(e)
	}
	return a.Lines[0].ID
}
func TestDestinationCoolingCanonicalBankAndAvailabilityTruth(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	line := releasedLine(t, f)
	f.remedy(t)
	first := f.destination(t)
	second, e := f.s.RecordPayoutReference(verifier(), core.PayoutReferenceInput{WalletID: f.w.WalletID, BeneficiaryRef: "second-owned-bank", VerificationRef: "kyc", Jurisdiction: "NG", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.l.BindDestination(verifier(), lifecycle.Destination{ID: ids.New(), PayoutAccountID: second.ID, ProviderID: f.cfg.ID, RecipientRef: "RCP_fixture", EvidenceRef: "second-bank-ownership", ExpiresAt: time.Now().Add(50 * time.Minute)}); e != nil {
		t.Fatal(e)
	}
	in := f.withdrawal(t, line, second.ID, 10000)
	if _, e = f.l.Withdraw(f.user, in, "unversioned-bank"); e == nil {
		t.Fatal("changed bank bypassed cooling")
	}
	change := lifecycle.DestinationChange{ID: ids.New(), WalletID: f.w.WalletID, PayoutAccountID: second.ID, PolicyID: f.policy.ID, StepUpRef: "step-up", VerificationRef: "kyc", OwnershipRef: "bank-owner", RiskRef: "risk-review", NotificationRef: "notification-command", ExpiresAt: time.Now().Add(45 * time.Minute)}
	version, e := f.l.ChangeDestination(verifier(), change)
	if e != nil {
		t.Fatal(e)
	}
	if version.CoolingEndsAt.Before(time.Now()) {
		t.Fatal("no cooling")
	}
	if _, e = f.l.Withdraw(f.user, in, "cooling-bank"); e == nil {
		t.Fatal("bank immediately eligible")
	}
	original := f.withdrawal(t, line, first, 10000)
	available, e := f.l.Availability(f.user, f.w.WalletID, f.cfg.PoolID)
	if e != nil || !available.WithdrawableNow || available.AvailableMinor != "10000" {
		t.Fatal(available, e)
	}
	cfg := f.cfg
	cfg.Operational = false
	if e = f.s.Register(system(), cfg, finance.Adapter{Collection: f.fake, DVA: f.fake, Settlement: f.p, Payout: f.p, Refund: f.p}); e != nil {
		t.Fatal(e)
	}
	available, e = f.l.Availability(f.user, f.w.WalletID, f.cfg.PoolID)
	if e != nil || available.WithdrawableNow || available.AvailableMinor != "10000" {
		t.Fatal("rail changed earnings truth", available, e)
	}
	cfg.Operational = true
	if e = f.s.Register(system(), cfg, finance.Adapter{Collection: f.fake, DVA: f.fake, Settlement: f.p, Payout: f.p, Refund: f.p}); e != nil {
		t.Fatal(e)
	}
	time.Sleep(time.Until(version.EffectiveAt) + 20*time.Millisecond)
	if _, e = f.l.Withdraw(f.user, original, "old-superseded-bank"); e == nil {
		t.Fatal("old bank not superseded")
	}
	payout, e := f.l.Withdraw(f.user, in, "current-bank")
	if e != nil || payout.State != "PROCESSING" {
		t.Fatal(payout, e)
	}
	if e = f.l.RevokeDestination(verifier(), version.ID, "fraud-review"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.Pool.Exec(system(), `UPDATE destination_versions SET effective_at=now() WHERE version_id=$1`, version.ID); e == nil {
		t.Fatal("destination history mutable")
	}
}
func TestPaidPayoutLaterReversalPreservesHistory(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	line := releasedLine(t, f)
	dest := f.destination(t)
	p, e := f.l.Withdraw(f.user, f.withdrawal(t, line, dest, 10000), "payout")
	if e != nil {
		t.Fatal(e)
	}
	f.p.mu.Lock()
	f.p.transfer.State = "CONFIRMED_PAID"
	f.p.mu.Unlock()
	if _, e = f.l.ResolvePayout(f.user, p.ID); e != nil {
		t.Fatal(e)
	}
	f.p.mu.Lock()
	f.p.transfer.State = "REVERSED"
	f.p.mu.Unlock()
	var wg sync.WaitGroup
	for n := 0; n < 10; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.l.ResolvePayout(f.user, p.ID)
			if e != nil || r.State != "REVERSED" {
				t.Error(r, e)
			}
		}()
	}
	wg.Wait()
	var state string
	var paid, cash, frozen int64
	if e = f.db.Pool.QueryRow(system(), `SELECT state FROM payout_attempts WHERE payout_id=$1`, p.ID).Scan(&state); e != nil || state != "CONFIRMED_PAID" {
		t.Fatal("terminal history rewritten", state, e)
	}
	if e = f.db.Pool.QueryRow(system(), `SELECT paid_minor FROM allocation_lines WHERE line_id=$1`, line).Scan(&paid); e != nil || paid != 10000 {
		t.Fatal(paid, e)
	}
	if e = f.db.Pool.QueryRow(system(), `SELECT COALESCE(sum(balance_minor) FILTER(WHERE kind='SETTLED_CASH'),0)::bigint,COALESCE(sum(balance_minor) FILTER(WHERE kind='FROZEN'),0)::bigint FROM account_balances`).Scan(&cash, &frozen); e != nil || cash != 10000 || frozen != 10000 {
		t.Fatal(cash, frozen, e)
	}
	var n int
	if e = f.db.Pool.QueryRow(system(), `SELECT count(*) FROM financial_reversals WHERE payout_id=$1`, p.ID).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
func TestChargebackAfterPaidPayoutCreatesRecoverableAndPayable(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	line := releasedLine(t, f)
	dest := f.destination(t)
	p, e := f.l.Withdraw(f.user, f.withdrawal(t, line, dest, 10000), "payout")
	if e != nil {
		t.Fatal(e)
	}
	f.p.mu.Lock()
	f.p.transfer.State = "CONFIRMED_PAID"
	f.p.reversal = providers.Reversal{Reference: "dispute:1", EconomicReference: "refund:1", ChargeReference: f.intent.Reference, Currency: "NGN", Mode: "test", MerchantID: 1001, AmountMinor: 10000, Confirmed: true}
	f.p.mu.Unlock()
	if _, e = f.l.ResolvePayout(f.user, p.ID); e != nil {
		t.Fatal(e)
	}
	req := lifecycle.ReversalRequest{IntentID: f.intent.ID, ProviderReference: "dispute:1", AuthorityRef: "provider-confirmed-chargeback", Approvers: []string{"a", "b"}}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.l.ReverseCollection(admin(), req)
			if e != nil || r.RecoverableMinor != 10000 {
				t.Error(r, e)
			}
		}()
	}
	wg.Wait()
	var cash, asset, payable int64
	if e = f.db.Pool.QueryRow(system(), `SELECT COALESCE(sum(balance_minor) FILTER(WHERE kind='SETTLED_CASH'),0)::bigint,COALESCE(sum(balance_minor) FILTER(WHERE kind='RECOVERABLE'),0)::bigint,COALESCE(sum(balance_minor) FILTER(WHERE kind='PROVIDER_PAYABLE'),0)::bigint FROM account_balances`).Scan(&cash, &asset, &payable); e != nil || cash != 0 || asset != 10000 || payable != 10000 {
		t.Fatal(cash, asset, payable, e)
	}
	if _, e = f.l.Release(f.user, lifecycle.Release{LineID: line, AmountMinor: 1, PolicyVersion: f.policy.Version}, "after-chargeback"); e == nil {
		t.Fatal("proceeds released after debt")
	}
	var count int
	if e = f.db.Pool.QueryRow(system(), `SELECT count(*) FROM financial_recoverables`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}
func TestRefundConfirmedThenReversedAndCollectionOverlap(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	a := f.allocate(t)
	f.fund(t)
	ev := f.remedy(t)
	c := f.openCase(t, a.Lines[0].ID, 10000, ev, "case")
	f.decide(t, c, ev)
	r, e := f.l.RequestRefund(admin(), lifecycle.RefundRequest{LineID: a.Lines[0].ID, CaseID: c.ID, AmountMinor: 10000, Strategy: "ORIGINAL_INSTRUMENT", AuthorityRef: "approved", EvidenceIDs: []ids.ID{ev}, Approvers: []string{"a", "b"}}, "refund")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.l.SubmitRefund(admin(), r.ID); e != nil {
		t.Fatal(e)
	}
	f.p.mu.Lock()
	v := f.p.refunds[r.AttemptReference]
	v.State = "CONFIRMED_REFUNDED"
	f.p.refunds[r.AttemptReference] = v
	f.p.mu.Unlock()
	if _, e = f.l.ResolveRefund(system(), r.ID); e != nil {
		t.Fatal(e)
	}
	f.p.mu.Lock()
	v.State = "REVERSED"
	f.p.refunds[r.AttemptReference] = v
	f.p.mu.Unlock()
	for n := 0; n < 3; n++ {
		out, e := f.l.ResolveRefund(system(), r.ID)
		if e != nil || out.State != "REVERSED" {
			t.Fatal(out, e)
		}
	}
	var original string
	if e = f.db.Pool.QueryRow(system(), `SELECT state FROM refund_attempts WHERE refund_id=$1`, r.ID).Scan(&original); e != nil || original != "CONFIRMED_REFUNDED" {
		t.Fatal(original, e)
	}
	f.p.reversal = providers.Reversal{Reference: "dispute:2", EconomicReference: "refund:2", ChargeReference: f.intent.Reference, Currency: "NGN", Mode: "test", MerchantID: 1001, AmountMinor: 10000, Confirmed: true}
	if _, e = f.l.ReverseCollection(admin(), lifecycle.ReversalRequest{IntentID: f.intent.ID, ProviderReference: "dispute:2", AuthorityRef: "approved", Approvers: []string{"a", "b"}}); e == nil {
		t.Fatal("overlapping compensation")
	}
}
