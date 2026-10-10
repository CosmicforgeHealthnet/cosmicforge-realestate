package integration

import (
	"context"
	"fmt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func (p *payoutFixture) InitiateRefund(_ context.Context, r providers.Refund) (providers.Refund, error) {
	p.refundCalls.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refunds == nil {
		p.refunds = map[string]providers.Refund{}
	}
	r.ID = fmt.Sprint(len(p.refunds) + 1)
	r.Mode = "test"
	r.MerchantID = 1001
	r.State = "PROCESSING"
	p.refunds[r.Reference] = r
	if p.refundUnknown {
		return providers.Refund{}, providers.ErrUnknown
	}
	return r, nil
}
func (p *payoutFixture) VerifyRefund(_ context.Context, r providers.Refund) (providers.Refund, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refundUnknown {
		return providers.Refund{}, providers.ErrUnknown
	}
	v, ok := p.refunds[r.Reference]
	if !ok {
		return providers.Refund{}, providers.ErrUnknown
	}
	return v, nil
}
func (p *payoutFixture) VerifyReversal(context.Context, string) (providers.Reversal, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reversal, nil
}
func remedyPack(id ids.ID) lifecycle.RemedyPolicy {
	return lifecycle.RemedyPolicy{PolicyID: id, RecoverFromProceeds: true, ReserveRecovery: true, ApprovalRef: "approved-jurisdiction-fixture", DepositTypes: []string{"RESIDENTIAL_SECURITY", "HOSPITALITY_DAMAGE", "EVENT_CLEANING_DAMAGE", "LAND_RESTORATION", "POLICY_REFUNDABLE_RESERVE"}, ClaimSeconds: 100, ChallengeSeconds: 100, ReviewSeconds: 100, RefundSeconds: 100, EscalationSeconds: 100, ReserveExtensionSeconds: 100, NoticeSeconds: 100, CoolingSeconds: 2, DualAboveMinor: 10000, RefundStrategies: []string{"ORIGINAL_INSTRUMENT", "WALLET_CREDIT"}, EvidenceServices: []string{string(platform.IndividualOwnerService)}, AllowUncontested: true, WalletSpend: true, WalletTopup: true, TopupFeeTreatment: "CUSTOMER_NET"}
}
func (f *lifeFixture) remedy(t *testing.T) ids.ID {
	t.Helper()
	if e := f.l.PutRemedyPolicy(admin(), remedyPack(f.policy.ID)); e != nil {
		t.Fatal(e)
	}
	id := ids.New()
	if e := f.l.PutRemedyEvidence(f.user, lifecycle.RemedyEvidence{ID: id, BusinessRef: f.quote.BusinessRef, Reference: "inspection", Version: "v1", ActorRef: "inspector", HashRef: "sha256:fixture", ObservedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	return id
}
func (f *lifeFixture) openCase(t *testing.T, line ids.ID, amount int64, ev ids.ID, key string) lifecycle.FinancialCase {
	t.Helper()
	c, e := f.l.OpenCase(f.user, lifecycle.CaseRequest{ID: ids.New(), LineID: line, Kind: "DISPUTE", AmountMinor: amount, ClaimantRef: "tenant", RespondentRef: "owner", AuthorityRef: "signed-claim", ReasonRef: "contested-work", EvidenceIDs: []ids.ID{ev}}, key)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func (f *lifeFixture) decide(t *testing.T, c lifecycle.FinancialCase, ev ids.ID) lifecycle.CaseDecision {
	t.Helper()
	review := lifecycle.CaseFact{ID: ids.New(), CaseID: c.ID, Kind: "REVIEW", ActorRef: "reviewer", AuthorityRef: "review-authority", ReasonRef: "evidence-reviewed", EvidenceIDs: []ids.ID{ev}}
	if e := f.l.AppendCaseFact(admin(), review, "review"); e != nil {
		t.Fatal(e)
	}
	in := lifecycle.CaseDecision{CaseFact: lifecycle.CaseFact{ID: ids.New(), CaseID: c.ID, Kind: "DECISION", ActorRef: "decision-maker", AuthorityRef: "decision-authority", ReasonRef: "return-payer", EvidenceIDs: []ids.ID{ev}}, Outcome: "RETURN_TO_PAYER", ReviewerRef: "reviewer", ExecutorRef: "executor", Approvers: []string{"approver-a", "approver-b"}}
	if e := f.l.DecideCase(admin(), in, "decide"); e != nil {
		t.Fatal(e)
	}
	return in
}
func TestContestedAmountOnlyAndConcurrentClaimCaps(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 500000)
	a := f.allocate(t)
	f.fund(t)
	ev := f.remedy(t)
	line := a.Lines[0].ID
	c := f.openCase(t, line, 75000, ev, "claim")
	releaseEv := f.evidence(t, line)
	if _, e := f.l.Release(f.user, lifecycle.Release{LineID: line, AmountMinor: 425000, PolicyVersion: f.policy.Version, EvidenceIDs: []ids.ID{releaseEv}}, "uncontested"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.l.Release(f.user, lifecycle.Release{LineID: line, AmountMinor: 1, PolicyVersion: f.policy.Version, EvidenceIDs: []ids.ID{releaseEv}}, "contested"); e == nil {
		t.Fatal("contested money released")
	}
	var frozen int64
	if e := f.db.Pool.QueryRow(system(), `SELECT sum(balance_minor)::bigint FROM account_balances WHERE kind='FROZEN'`).Scan(&frozen); e != nil || frozen != 75000 {
		t.Fatal(frozen, e)
	}
	// Challenges append facts without replacing the original proposed claim.
	fact := lifecycle.CaseFact{ID: ids.New(), CaseID: c.ID, Kind: "CHALLENGE", ActorRef: "tenant", AuthorityRef: "payer-consent", ReasonRef: "claim-denied", EvidenceIDs: []ids.ID{ev}}
	if e := f.l.AppendCaseFact(f.user, fact, "challenge"); e != nil {
		t.Fatal(e)
	}
	f.decide(t, c, ev)
	if e := f.l.AppendCaseFact(f.user, fact, "challenge"); e != nil {
		t.Fatal("same immutable challenge must replay", e)
	}
	var count int
	if e := f.db.Pool.QueryRow(system(), `SELECT count(*) FROM financial_case_facts WHERE case_id=$1 AND kind='CLAIM'`, c.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	if _, e := f.db.Pool.Exec(system(), `UPDATE financial_case_facts SET snapshot='{}' WHERE case_id=$1`, c.ID); e == nil {
		t.Fatal("claim mutable")
	}
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, e := f.l.OpenCase(f.user, lifecycle.CaseRequest{ID: ids.New(), LineID: line, Kind: "DISPUTE", AmountMinor: 100000, ClaimantRef: "tenant", RespondentRef: "owner", AuthorityRef: "signed", ReasonRef: "race", EvidenceIDs: []ids.ID{ev}}, fmt.Sprint(n))
			if e == nil {
				accepted.Add(1)
			}
		}(n)
	}
	wg.Wait()
	if accepted.Load() != 4 {
		t.Fatal("claim cap", accepted.Load())
	}
}
func TestRefundUnknownPartialCapsAndSingleSubmission(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	a := f.allocate(t)
	f.fund(t)
	ev := f.remedy(t)
	c := f.openCase(t, a.Lines[0].ID, 10000, ev, "claim")
	f.decide(t, c, ev)
	req := lifecycle.RefundRequest{LineID: a.Lines[0].ID, CaseID: c.ID, AmountMinor: 4000, Strategy: "ORIGINAL_INSTRUMENT", AuthorityRef: "refund-approved", EvidenceIDs: []ids.ID{ev}, Approvers: []string{"finance-a", "finance-b"}}
	r, e := f.l.RequestRefund(admin(), req, "first")
	if e != nil {
		t.Fatal(e)
	}
	f.p.refundUnknown = true
	var wg sync.WaitGroup
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := f.l.SubmitRefund(admin(), r.ID); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if f.p.refundCalls.Load() != 1 {
		t.Fatal("duplicate submission")
	}
	if _, e = f.l.CancelRefund(admin(), r.ID, "cancel"); e == nil {
		t.Fatal("unknown refund cancelled")
	}
	if _, e = f.l.ResolveRefund(system(), r.ID); e != nil {
		t.Fatal(e)
	}
	req.AmountMinor = 7000
	if _, e = f.l.RequestRefund(admin(), req, "over"); e == nil {
		t.Fatal("active reservations ignored")
	}
	req.AmountMinor = 4000
	req.Strategy = "WALLET_CREDIT"
	if _, e = f.l.RequestRefund(admin(), req, "wallet-substitution"); e == nil {
		t.Fatal("provider timeout replaced by wallet credit")
	}
	f.p.mu.Lock()
	f.p.refundUnknown = false
	v := f.p.refunds[r.AttemptReference]
	v.State = "CONFIRMED_REFUNDED"
	f.p.refunds[r.AttemptReference] = v
	f.p.mu.Unlock()
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := f.l.ResolveRefund(system(), r.ID)
			if e != nil || v.State != "CONFIRMED_REFUNDED" {
				t.Error(v, e)
			}
		}()
	}
	wg.Wait()
	req.AmountMinor = 6000
	req.Strategy = "ORIGINAL_INSTRUMENT"
	r2, e := f.l.RequestRefund(admin(), req, "second")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.l.CancelRefund(admin(), r2.ID, "cancel-before-submit"); e != nil {
		t.Fatal(e)
	}
	var terminals int
	if e = f.db.Pool.QueryRow(system(), `SELECT count(*) FROM journals WHERE operation_ref=$1`, "refund-terminal:"+r.ID.String()).Scan(&terminals); e != nil || terminals != 1 {
		t.Fatal(terminals, e)
	}
	if _, e = f.db.Pool.Exec(system(), `DELETE FROM refund_attempts WHERE refund_id=$1`, r.ID); e == nil {
		t.Fatal("refund history deleted")
	}
}
func TestRemedyPolicyEvidenceAndDecisionAuthority(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	a := f.allocate(t)
	f.fund(t)
	req := lifecycle.CaseRequest{ID: ids.New(), LineID: a.Lines[0].ID, Kind: "DISPUTE", AmountMinor: 10000, ClaimantRef: "tenant", RespondentRef: "owner", AuthorityRef: "authority", ReasonRef: "damage"}
	if _, e := f.l.OpenCase(f.user, req, "unconfigured"); e == nil {
		t.Fatal("unconfigured jurisdiction accepted")
	}
	ev := f.remedy(t)
	req.EvidenceIDs = []ids.ID{ev}
	c, e := f.l.OpenCase(f.user, req, "configured")
	if e != nil {
		t.Fatal(e)
	}
	decision := lifecycle.CaseDecision{CaseFact: lifecycle.CaseFact{ID: ids.New(), CaseID: c.ID, ActorRef: "owner", AuthorityRef: "a", ReasonRef: "b", EvidenceIDs: []ids.ID{ev}}, Outcome: "RETURN_TO_PAYER", ReviewerRef: "tenant", ExecutorRef: "tenant", Approvers: []string{"a", "b"}}
	if e = f.l.DecideCase(f.user, decision, "unauthorized"); e == nil {
		t.Fatal("source arbitrary award")
	}
	if e = f.l.DecideCase(admin(), decision, "self-review"); e == nil {
		t.Fatal("no independent review")
	}
	if e = f.l.RevokeRemedyEvidence(f.user, ev, "inspection-revoked"); e != nil {
		t.Fatal(e)
	}
	review := lifecycle.CaseFact{ID: ids.New(), CaseID: c.ID, Kind: "REVIEW", ActorRef: "reviewer", AuthorityRef: "a", ReasonRef: "b", EvidenceIDs: []ids.ID{ev}}
	if e = f.l.AppendCaseFact(admin(), review, "stale"); e == nil {
		t.Fatal("revoked inspection accepted")
	}
}
