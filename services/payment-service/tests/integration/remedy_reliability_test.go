package integration

import (
	"fmt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"sync"
	"testing"
	"time"
)

func depositFixture(t *testing.T, typ string) (*lifeFixture, lifecycle.Deposit, ids.ID) {
	t.Helper()
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	f.fund(t)
	ev := f.remedy(t)
	auth := f.authority
	auth.ID = ids.New()
	auth.Components = []string{"DEPOSIT"}
	if _, e := f.l.PutAuthority(f.user, auth); e != nil {
		t.Fatal(e)
	}
	q := f.quote
	q.ID = ids.New()
	q.Components = []lifecycle.Component{{Ref: "security", Kind: "DEPOSIT", AmountMinor: 10000, PayerRef: q.LegalPartyRef, Recurrence: "REFUNDABLE_DEPOSIT"}}
	if _, e := f.l.LockQuote(f.user, q, "deposit"); e != nil {
		t.Fatal(e)
	}
	i, e := f.s.CreateIntent(f.user, finance.IntentInput{LockedQuoteID: &q.ID, WalletID: q.PayerWalletID, AmountMinor: 10000, Currency: "NGN", Country: "NG", Category: f.policy.Key, EvidenceRef: q.QuoteVersion, PolicyVersion: f.policy.Version, Email: "payer@example.invalid"}, "deposit")
	if e != nil {
		t.Fatal(e)
	}
	f.fake.success(i)
	i, e = f.s.VerifyIntent(f.user, i.ID)
	if e != nil {
		t.Fatal(e)
	}
	f.intent = i
	f.quote = q
	f.p.charge, _ = f.fake.VerifyCharge(system(), i.Reference)
	a, e := f.l.Allocate(f.user, lifecycle.AllocationInput{IntentID: i.ID, Version: 1, Lines: []lifecycle.LineInput{{AuthorityID: auth.ID, ComponentRef: "security", Kind: "DEPOSIT", AmountMinor: 10000, Currency: "NGN"}}})
	if e != nil {
		t.Fatal(e)
	}
	f.fund(t)
	d, e := f.l.RegisterDeposit(f.user, lifecycle.Deposit{ID: ids.New(), LineID: a.Lines[0].ID, Type: typ, CustodyRef: "local-liability-only-no-certified-custody", IngressRef: i.Reference, InspectionIDs: []ids.ID{ev}})
	if e != nil {
		t.Fatal(e)
	}
	return f, d, ev
}

func TestDepositTypesSegregationClaimsAndPartialAward(t *testing.T) {
	for _, typ := range []string{"RESIDENTIAL_SECURITY", "HOSPITALITY_DAMAGE", "EVENT_CLEANING_DAMAGE", "LAND_RESTORATION", "POLICY_REFUNDABLE_RESERVE"} {
		t.Run(typ, func(t *testing.T) {
			f, d, ev := depositFixture(t, typ)
			if _, e := f.l.Release(f.user, lifecycle.Release{LineID: d.LineID, AmountMinor: 1, PolicyVersion: f.policy.Version, EvidenceIDs: []ids.ID{f.evidence(t, d.LineID)}}, "deposit-income"); e == nil {
				t.Fatal("deposit released as income")
			}
			c, e := f.l.OpenCase(f.user, lifecycle.CaseRequest{ID: ids.New(), LineID: d.LineID, DepositID: &d.ID, Kind: "DEPOSIT_CLAIM", AmountMinor: 4000, ClaimantRef: "owner", RespondentRef: "payer", AuthorityRef: "inspection-claim", ReasonRef: "damage", EvidenceIDs: []ids.ID{ev}}, "damage")
			if e != nil {
				t.Fatal(e)
			}
			if e = f.l.AppendCaseFact(f.user, lifecycle.CaseFact{ID: ids.New(), CaseID: c.ID, Kind: "CHALLENGE", ActorRef: "payer", AuthorityRef: "challenge", ReasonRef: "counter-inspection", EvidenceIDs: []ids.ID{ev}}, "challenge"); e != nil {
				t.Fatal(e)
			}
			if e = f.l.AppendCaseFact(admin(), lifecycle.CaseFact{ID: ids.New(), CaseID: c.ID, Kind: "REVIEW", ActorRef: "reviewer", AuthorityRef: "review", ReasonRef: "evidence", EvidenceIDs: []ids.ID{ev}}, "review"); e != nil {
				t.Fatal(e)
			}
			auth := f.authority
			auth.ID = ids.New()
			auth.Components = []string{"DEPOSIT_CLAIM"}
			if _, e = f.l.PutAuthority(f.user, auth); e != nil {
				t.Fatal(e)
			}
			decision := lifecycle.CaseDecision{CaseFact: lifecycle.CaseFact{ID: ids.New(), CaseID: c.ID, ActorRef: "decision", AuthorityRef: "independent-award", ReasonRef: "partial", EvidenceIDs: []ids.ID{ev}}, Outcome: "PARTIAL_AWARD", AwardMinor: 1500, AwardAuthorityID: &auth.ID, ReviewerRef: "reviewer", ExecutorRef: "executor", Approvers: []string{"a", "b"}}
			if e = f.l.DecideCase(admin(), decision, "award"); e != nil {
				t.Fatal(e)
			}
			var awardedLine ids.ID
			if e = f.db.Pool.QueryRow(system(), `SELECT l.line_id FROM allocation_lines l JOIN allocation_snapshots a USING(allocation_id) JOIN payment_intents i USING(intent_id) WHERE i.category='APPROVED_AWARD'`).Scan(&awardedLine); e != nil {
				t.Fatal(e)
			}
			if _, e = f.l.Release(f.user, lifecycle.Release{LineID: awardedLine, AmountMinor: 1500, PolicyVersion: f.policy.Version, EvidenceIDs: []ids.ID{f.evidence(t, awardedLine)}}, "award-release"); e != nil {
				t.Fatal(e)
			}
			var deposit, frozen, available, refund int64
			if e = f.db.Pool.QueryRow(system(), `SELECT COALESCE(sum(balance_minor) FILTER(WHERE kind='DEPOSIT'),0)::bigint,COALESCE(sum(balance_minor) FILTER(WHERE kind='FROZEN'),0)::bigint,COALESCE(sum(balance_minor) FILTER(WHERE kind='AVAILABLE'),0)::bigint,COALESCE(sum(balance_minor) FILTER(WHERE kind='REFUND_PENDING'),0)::bigint FROM account_balances`).Scan(&deposit, &frozen, &available, &refund); e != nil || deposit != 6000 || frozen != 0 || available != 1500 || refund != 2500 {
				t.Fatal(deposit, frozen, available, refund, e)
			}
			r, e := f.l.RequestRefund(admin(), lifecycle.RefundRequest{LineID: d.LineID, CaseID: c.ID, AmountMinor: 2500, Strategy: "ORIGINAL_INSTRUMENT", AuthorityRef: "approved-return", EvidenceIDs: []ids.ID{ev}, Approvers: []string{"a", "b"}}, "refund")
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
			projected, e := f.l.Deposit(f.user, d.ID)
			if e != nil || projected.State != "PARTIAL_AWARD" {
				t.Fatal(projected, e)
			}
			p, e := f.l.Withdraw(f.user, f.withdrawal(t, awardedLine, f.destination(t), 1500), "award-payout")
			if e != nil {
				t.Fatal(e)
			}
			f.p.mu.Lock()
			f.p.transfer.State = "CONFIRMED_PAID"
			f.p.mu.Unlock()
			if _, e = f.l.ResolvePayout(system(), p.ID); e != nil {
				t.Fatal(e)
			}
			validateLifecycleOutbox(t, f)
		})
	}
}

func TestReadbackDeadLetterReplayLeaseAndOriginalReference(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	line := releasedLine(t, f)
	f.p.unknown = true
	p, e := f.l.Withdraw(f.user, f.withdrawal(t, line, f.destination(t), 10000), "ambiguous")
	if e != nil {
		t.Fatal(e)
	}
	policy := finance.RecoveryPolicy{MaxAttempts: 2, BaseSeconds: 1, MaxBackoffSeconds: 2, LeaseSeconds: 1}
	if e = f.s.ConfigureRecovery(system(), policy); e != nil {
		t.Fatal(e)
	}
	for n := 0; n < 2; n++ {
		token, claimed, e := f.s.ClaimReadback(system(), "PAYOUT", p.ID)
		if e != nil || !claimed {
			t.Fatal(token, claimed, e)
		}
		if _, other, e := f.s.ClaimReadback(system(), "PAYOUT", p.ID); e != nil || other {
			t.Fatal("concurrent lease", e)
		}
		if e = f.s.FinishReadback(system(), "PAYOUT", p.ID, token, false); e != nil {
			t.Fatal(e)
		}
		if n == 0 {
			time.Sleep(1100 * time.Millisecond)
		}
	}
	var state, ref string
	if e = f.db.Pool.QueryRow(system(), `SELECT state,original_reference FROM original_readback_jobs WHERE aggregate_id=$1`, p.ID).Scan(&state, &ref); e != nil || state != "DEAD" || ref != p.Reference {
		t.Fatal(state, ref, e)
	}
	if _, claimed, e := f.s.ClaimReadback(system(), "PAYOUT", p.ID); e != nil || claimed {
		t.Fatal(claimed, e)
	}
	if e = f.s.ReplayReadback(f.user, "PAYOUT", p.ID, "bad"); e == nil {
		t.Fatal("customer replay")
	}
	if e = f.s.ReplayReadback(admin(), "PAYOUT", p.ID, "operator-original-reference-review"); e != nil {
		t.Fatal(e)
	}
	token, claimed, e := f.s.ClaimReadback(system(), "PAYOUT", p.ID)
	if e != nil || !claimed {
		t.Fatal(claimed, e)
	}
	f.p.mu.Lock()
	f.p.unknown = false
	f.p.transfer.State = "CONFIRMED_PAID"
	f.p.mu.Unlock()
	if _, e = f.l.ResolvePayout(system(), p.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.s.FinishReadback(system(), "PAYOUT", p.ID, token, true); e != nil {
		t.Fatal(e)
	}
	if f.p.submissions.Load() != 1 {
		t.Fatal("recovery resubmitted")
	}
	if _, e = f.db.Pool.Exec(system(), `UPDATE original_readback_jobs SET original_reference='changed' WHERE aggregate_id=$1`, p.ID); e == nil {
		t.Fatal("mutable original")
	}
	if _, e = f.db.Pool.Exec(system(), `DELETE FROM original_readback_history`); e == nil {
		t.Fatal("history deleted")
	}
	validateLifecycleOutbox(t, f)
}

func TestRecoverableAssignmentReserveCapsAndHistory(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	line := releasedLine(t, f)
	ev := f.remedy(t)
	p, e := f.l.Withdraw(f.user, f.withdrawal(t, line, f.destination(t), 10000), "paid")
	if e != nil {
		t.Fatal(e)
	}
	f.p.mu.Lock()
	f.p.transfer.State = "CONFIRMED_PAID"
	f.p.reversal = providers.Reversal{Reference: "dispute:2", EconomicReference: "refund:2", ChargeReference: f.intent.Reference, Currency: "NGN", Mode: "test", MerchantID: 1001, AmountMinor: 10000, Confirmed: true}
	f.p.mu.Unlock()
	if _, e = f.l.ResolvePayout(system(), p.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.l.ReverseCollection(admin(), lifecycle.ReversalRequest{IntentID: f.intent.ID, ProviderReference: "dispute:2", AuthorityRef: "verified", Approvers: []string{"a", "b"}}); e != nil {
		t.Fatal(e)
	}
	var debt ids.ID
	if e = f.db.Pool.QueryRow(system(), `SELECT recoverable_id FROM financial_recoverables`).Scan(&debt); e != nil {
		t.Fatal(e)
	}
	assign := lifecycle.RecoverableAction{ID: ids.New(), RecoverableID: debt, WalletID: &f.w.WalletID, Action: "ASSIGN_BENEFICIARY", AuthorityRef: "liability-review", RiskRef: "risk", Approvers: []string{"a", "b"}, EvidenceIDs: []ids.ID{ev}}
	if e = f.l.RecoverableAction(admin(), assign, "assign"); e != nil {
		t.Fatal(e)
	}
	if e = f.l.RecoverableAction(admin(), assign, "assign"); e != nil {
		t.Fatal(e)
	}
	tx, e := f.db.Pool.Begin(system())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(system())
	reserve, e := f.s.Account(system(), tx, nil, f.cfg.PoolID, "TREASURY", f.w.LegalEntity, "NGN", "approved-recovery-reserve:fixture")
	if e != nil {
		t.Fatal(e)
	}
	cash, e := f.s.Account(system(), tx, nil, f.cfg.PoolID, "SETTLED_CASH", f.w.LegalEntity, "NGN", "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.PostTx(system(), tx, core.Posting{OperationRef: "attested-test-reserve", PoolID: f.cfg.PoolID, LegalEntity: f.w.LegalEntity, Currency: "NGN", PolicyVersion: "approved-fixture", Authorization: map[string]string{"evidence_ref": "test-only-cash-attestation", "approved_by": "a,b"}, Entries: []core.Entry{{AccountID: cash, Side: "DEBIT", AmountMinor: 10000}, {AccountID: reserve, Side: "CREDIT", AmountMinor: 10000}}}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(system()); e != nil {
		t.Fatal(e)
	}
	recovery := assign
	recovery.ID = ids.New()
	recovery.WalletID = nil
	recovery.Action = "USE_RESERVE"
	recovery.SourceAccountID = &reserve
	recovery.AmountMinor = 4000
	if e = f.l.RecoverableAction(admin(), recovery, "reserve-1"); e != nil {
		t.Fatal(e)
	}
	if e = f.l.RecoverableAction(admin(), recovery, "reserve-1"); e != nil {
		t.Fatal(e)
	}
	recovery.ID = ids.New()
	recovery.AmountMinor = 6001
	if e = f.l.RecoverableAction(admin(), recovery, "over"); e == nil {
		t.Fatal("over recovery")
	}
	recovery.ID = ids.New()
	recovery.AmountMinor = 6000
	if e = f.l.RecoverableAction(admin(), recovery, "reserve-2"); e != nil {
		t.Fatal(e)
	}
	var balance int64
	if e = f.db.Pool.QueryRow(system(), `SELECT sum(balance_minor)::bigint FROM account_balances WHERE kind='RECOVERABLE'`).Scan(&balance); e != nil || balance != 0 {
		t.Fatal(balance, e)
	}
	if _, e = f.db.Pool.Exec(system(), `UPDATE recoverable_actions SET amount_minor=0`); e == nil {
		t.Fatal("mutable recoverable history")
	}
	validateLifecycleOutbox(t, f)
}

func TestReconciliationPreservesExceptionsAndResolutionRequiresFollowup(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	f.fund(t)
	in := lifecycle.ReconciliationRequest{ID: ids.New(), ProviderID: f.cfg.ID, AuthorityRef: "reconcile", StaleAfterSeconds: 30}
	r, e := f.l.Reconcile(admin(), in)
	if e != nil || len(r.Findings) == 0 {
		t.Fatal(r, e)
	}
	if _, e = f.l.Reconcile(f.user, in); e == nil {
		t.Fatal("customer reconciler")
	}
	again, e := f.l.Reconcile(admin(), in)
	if e != nil || fmt.Sprint(again) != fmt.Sprint(r) {
		t.Fatal(again, e)
	}
	var finding ids.ID
	if e = f.db.Pool.QueryRow(system(), `SELECT finding_id FROM reconciliation_findings LIMIT 1`).Scan(&finding); e != nil {
		t.Fatal(e)
	}
	if e = f.l.ResolveReconciliation(admin(), lifecycle.ReconciliationResolution{ID: ids.New(), FindingID: finding, FollowupRunID: in.ID, AuthorityRef: "review", EvidenceRef: "review", Approvers: []string{"a", "b"}}); e == nil {
		t.Fatal("same run cleared own discrepancy")
	}
	if _, e = f.db.Pool.Exec(system(), `DELETE FROM reconciliation_findings`); e == nil {
		t.Fatal("deleted discrepancy")
	}
	m, e := f.l.OperationalMetrics(admin())
	if e != nil || m["reconciliation_exceptions"] == "0" {
		t.Fatal(m, e)
	}
	var cashFinding ids.ID
	if e = f.db.Pool.QueryRow(system(), `SELECT finding_id FROM reconciliation_findings WHERE kind='BALANCE_MISMATCH' LIMIT 1`).Scan(&cashFinding); e != nil {
		t.Fatal(e)
	}
	f.p.mu.Lock()
	f.p.balanceSet = true
	f.p.balanceMinor = 10000
	f.p.mu.Unlock()
	follow := in
	follow.ID = ids.New()
	next, e := f.l.Reconcile(admin(), follow)
	if e != nil {
		t.Fatal(next, e)
	}
	resolution := lifecycle.ReconciliationResolution{ID: ids.New(), FindingID: cashFinding, FollowupRunID: follow.ID, AuthorityRef: "review", EvidenceRef: "provider-balance-reviewed", Approvers: []string{"a", "b"}}
	if e = f.l.ResolveReconciliation(admin(), resolution); e != nil {
		t.Fatal(e)
	}
	validateLifecycleOutbox(t, f)
}

func TestReviewExpiryPrivilegedOverrideAndRetainedReserve(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	a := f.allocate(t)
	f.fund(t)
	rp := remedyPack(f.policy.ID)
	rp.ReviewSeconds = 1
	if e := f.l.PutRemedyPolicy(admin(), rp); e != nil {
		t.Fatal(e)
	}
	ev := ids.New()
	if e := f.l.PutRemedyEvidence(f.user, lifecycle.RemedyEvidence{ID: ev, BusinessRef: f.quote.BusinessRef, Reference: "inspection", Version: "v1", ActorRef: "inspector", HashRef: "hash", ObservedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	c := f.openCase(t, a.Lines[0].ID, 10000, ev, "case")
	if e := f.l.AppendCaseFact(admin(), lifecycle.CaseFact{ID: ids.New(), CaseID: c.ID, Kind: "REVIEW", ActorRef: "reviewer", AuthorityRef: "review", ReasonRef: "review", EvidenceIDs: []ids.ID{ev}}, "review"); e != nil {
		t.Fatal(e)
	}
	time.Sleep(1100 * time.Millisecond)
	decision := lifecycle.CaseDecision{CaseFact: lifecycle.CaseFact{ID: ids.New(), CaseID: c.ID, ActorRef: "decision", AuthorityRef: "decision", ReasonRef: "return", EvidenceIDs: []ids.ID{ev}}, Outcome: "RETURN_TO_PAYER", ReviewerRef: "reviewer", ExecutorRef: "executor", Approvers: []string{"a", "b"}}
	if e := f.l.DecideCase(admin(), decision, "late"); e == nil {
		t.Fatal("expired review executed")
	}
	override := lifecycle.CaseOverride{ID: ids.New(), CaseID: c.ID, ActorRef: "privileged", AuthorityRef: "authorized-exception", ReasonRef: "late evidence", EvidenceIDs: []ids.ID{ev}, Approvers: []string{"a", "b"}, ExpiresAt: time.Now().Add(time.Minute)}
	if e := f.l.OverrideCase(f.user, override); e == nil {
		t.Fatal("owner override")
	}
	if e := f.l.OverrideCase(admin(), override); e != nil {
		t.Fatal(e)
	}
	if e := f.l.DecideCase(admin(), decision, "late"); e != nil {
		t.Fatal(e)
	}
	override.ID = ids.New()
	if e := f.l.OverrideCase(admin(), override); e == nil {
		t.Fatal("closed case override")
	}
}

func TestOriginalRecoveryConcurrentLeasesAndCancelledIntent(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	ev := f.remedy(t)
	var wg sync.WaitGroup
	tokens := make(chan ids.ID, 12)
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, claimed, e := f.s.ClaimReadback(system(), "COLLECTION", f.intent.ID)
			if e != nil {
				t.Error(e)
			}
			if claimed {
				tokens <- token
			}
		}()
	}
	wg.Wait()
	close(tokens)
	if len(tokens) != 1 {
		t.Fatal("duplicate readback leases", len(tokens))
	}
	// An unsubmitted intent is seeded at the real crash boundary; no adapter call
	// or economic receipt exists for this new canonical accepted quote.
	q := f.quote
	q.ID = ids.New()
	if _, e := f.l.LockQuote(f.user, q, "unsubmitted"); e != nil {
		t.Fatal(e)
	}
	id := ids.New()
	_, e := f.db.Pool.Exec(system(), `INSERT INTO payment_intents(intent_id,source_service,wallet_id,operation_key,fingerprint,amount_minor,currency,legal_entity,category,country,evidence_ref,policy_version,provider_account_id,pool_id,route_snapshot,provider_reference,state,locked_quote_id) SELECT $1,source_service,wallet_id,$2,$2,amount_minor,currency,legal_entity,category,country,evidence_ref,policy_version,provider_account_id,pool_id,route_snapshot,$2,'REQUESTED',$3 FROM payment_intents WHERE intent_id=$4`, id, "cancel:"+id.String(), q.ID, f.intent.ID)
	if e != nil {
		t.Fatal(e)
	}
	in := lifecycle.Cancellation{IntentID: id, AuthorityRef: "accepted-cancellation", ReasonRef: "before-checkout", EvidenceIDs: []ids.ID{ev}}
	cancelled, e := f.l.CancelIntent(f.user, in, "cancel")
	if e != nil || cancelled.State != "CANCELLED" {
		t.Fatal(cancelled, e)
	}
	tag, e := f.s.ChangeProviderState(system(), "COLLECTION", id, "REQUESTED", "SUBMITTING", "")
	if e != nil || tag.RowsAffected() != 0 {
		t.Fatal("cancelled submission", tag, e)
	}
	in.IntentID = f.intent.ID
	if _, e = f.l.CancelIntent(f.user, in, "already-collected"); e == nil {
		t.Fatal("collected cancellation bypassed refund")
	}
	validateLifecycleOutbox(t, f)
}

func TestExpiredRefundReservationRetainsMoneyUntilExplicitCancellation(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	a := f.allocate(t)
	f.fund(t)
	rp := remedyPack(f.policy.ID)
	rp.RefundSeconds = 1
	if e := f.l.PutRemedyPolicy(admin(), rp); e != nil {
		t.Fatal(e)
	}
	ev := ids.New()
	if e := f.l.PutRemedyEvidence(f.user, lifecycle.RemedyEvidence{ID: ev, BusinessRef: f.quote.BusinessRef, Reference: "evidence", Version: "v1", ActorRef: "inspector", HashRef: "hash", ObservedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	c := f.openCase(t, a.Lines[0].ID, 10000, ev, "case")
	f.decide(t, c, ev)
	r, e := f.l.RequestRefund(admin(), lifecycle.RefundRequest{LineID: a.Lines[0].ID, CaseID: c.ID, AmountMinor: 10000, Strategy: "ORIGINAL_INSTRUMENT", AuthorityRef: "approved", EvidenceIDs: []ids.ID{ev}, Approvers: []string{"a", "b"}}, "refund")
	if e != nil {
		t.Fatal(e)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, e = f.l.SubmitRefund(admin(), r.ID); e == nil {
		t.Fatal("expired refund submitted")
	}
	if f.p.refundCalls.Load() != 0 {
		t.Fatal("external submission after deadline")
	}
	cancelled, e := f.l.CancelRefund(admin(), r.ID, "review-expired-reservation")
	if e != nil || cancelled.State != "CANCELLED" {
		t.Fatal(cancelled, e)
	}
	var due, available int64
	if e = f.db.Pool.QueryRow(system(), `SELECT COALESCE(sum(balance_minor) FILTER(WHERE kind='REFUND_PENDING'),0)::bigint,COALESCE(sum(balance_minor) FILTER(WHERE kind='AVAILABLE'),0)::bigint FROM account_balances`).Scan(&due, &available); e != nil || due != 10000 || available != 0 {
		t.Fatal(due, available, e)
	}
	if e = f.s.ConfigureRecovery(system(), finance.RecoveryPolicy{MaxAttempts: 1, BaseSeconds: 1, MaxBackoffSeconds: 1, LeaseSeconds: 1}); e != nil {
		t.Fatal(e)
	}
	_, claimed, e := f.s.ClaimReadback(system(), "COLLECTION", f.intent.ID)
	if e != nil || !claimed {
		t.Fatal(claimed, e)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, claimed, e = f.s.ClaimReadback(system(), "COLLECTION", f.intent.ID); e != nil || claimed {
		t.Fatal("exhausted crash lease reclaimed", claimed, e)
	}
	var state string
	if e = f.db.Pool.QueryRow(system(), `SELECT state FROM original_readback_jobs WHERE aggregate_id=$1`, f.intent.ID).Scan(&state); e != nil || state != "DEAD" {
		t.Fatal(state, e)
	}
	validateLifecycleOutbox(t, f)
}

func TestFullDepositAwardAndPrivilegedHistoryProjection(t *testing.T) {
	f, d, ev := depositFixture(t, "RESIDENTIAL_SECURITY")
	c, e := f.l.OpenCase(f.user, lifecycle.CaseRequest{ID: ids.New(), LineID: d.LineID, DepositID: &d.ID, Kind: "DEPOSIT_CLAIM", AmountMinor: 10000, ClaimantRef: "owner", RespondentRef: "payer", AuthorityRef: "claim", ReasonRef: "verified-full-damage", EvidenceIDs: []ids.ID{ev}}, "full")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.l.AppendCaseFact(admin(), lifecycle.CaseFact{ID: ids.New(), CaseID: c.ID, Kind: "REVIEW", ActorRef: "reviewer", AuthorityRef: "independent", ReasonRef: "evidence-reviewed", EvidenceIDs: []ids.ID{ev}}, "review"); e != nil {
		t.Fatal(e)
	}
	override := lifecycle.CaseOverride{ID: ids.New(), CaseID: c.ID, ActorRef: "privileged-reviewer", AuthorityRef: "authorized-review-exception", ReasonRef: "documented-exception", EvidenceIDs: []ids.ID{ev}, Approvers: []string{"a", "b"}, ExpiresAt: time.Now().Add(time.Minute)}
	if e = f.l.OverrideCase(admin(), override); e != nil {
		t.Fatal(e)
	}
	facts, e := f.l.CaseFacts(f.user, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	var retained bool
	for _, fact := range facts {
		if fact.Kind == "OVERRIDE" {
			retained = fact.Override != nil && fact.Override.ID == override.ID && len(fact.Override.Approvers) == 2 && fact.Override.ExpiresAt.Equal(override.ExpiresAt)
		}
	}
	if !retained {
		t.Fatal("override authority/expiry lost from case history")
	}
	auth := f.authority
	auth.ID = ids.New()
	auth.Components = []string{"DEPOSIT_CLAIM"}
	if _, e = f.l.PutAuthority(f.user, auth); e != nil {
		t.Fatal(e)
	}
	if e = f.l.DecideCase(admin(), lifecycle.CaseDecision{CaseFact: lifecycle.CaseFact{ID: ids.New(), CaseID: c.ID, ActorRef: "decision", AuthorityRef: "approved-full-award", ReasonRef: "verified-full-damage", EvidenceIDs: []ids.ID{ev}}, Outcome: "RELEASE_TO_BENEFICIARY", AwardMinor: 10000, AwardAuthorityID: &auth.ID, ReviewerRef: "reviewer", ExecutorRef: "executor", Approvers: []string{"a", "b"}}, "full-award"); e != nil {
		t.Fatal(e)
	}
	projected, e := f.l.Deposit(f.user, d.ID)
	if e != nil || projected.State != "AWARDED" {
		t.Fatal(projected, e)
	}
	validateLifecycleOutbox(t, f)
}

func TestRecoverySweepDeadLettersDoNotStarveNewerAttempts(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	seed := func(n int) finance.Intent {
		t.Helper()
		id := ids.New()
		ref := "cf-sweep-" + id.String()
		_, e := f.db.Pool.Exec(system(), `INSERT INTO payment_intents(intent_id,source_service,wallet_id,operation_key,fingerprint,amount_minor,currency,legal_entity,category,country,evidence_ref,policy_version,provider_account_id,pool_id,route_snapshot,provider_reference,state,updated_at) SELECT $1,source_service,wallet_id,$2,$2,amount_minor,currency,legal_entity,category,country,evidence_ref,policy_version,provider_account_id,pool_id,route_snapshot,$2,'UNKNOWN_OUTCOME',now()-$4::interval FROM payment_intents WHERE intent_id=$3`, id, ref, f.intent.ID, (time.Duration(100-n) * time.Second).String())
		if e != nil {
			t.Fatal(e)
		}
		i := f.intent
		i.ID = id
		i.Reference = ref
		i.State = "UNKNOWN_OUTCOME"
		return i
	}
	for n := 0; n < 55; n++ {
		i := seed(n)
		_, e := f.db.Pool.Exec(system(), `INSERT INTO original_readback_jobs(aggregate_id,kind,provider_account_id,original_reference,state,attempts) VALUES($1,'COLLECTION',$2,$3,'DEAD',12)`, i.ID, i.ProviderAccountID, i.Reference)
		if e != nil {
			t.Fatal(e)
		}
	}
	live := seed(60)
	f.fake.success(live)
	if e := f.s.RecoverUnknown(system()); e != nil {
		t.Fatal(e)
	}
	result, e := f.s.GetIntent(f.user, live.ID)
	if e != nil || result.State != "VERIFIED_SUCCESS" {
		t.Fatal("dead jobs starved newer recovery", result, e)
	}
	var state string
	if e = f.db.Pool.QueryRow(system(), `SELECT state FROM original_readback_jobs WHERE aggregate_id=$1`, live.ID).Scan(&state); e != nil || state != "COMPLETED" {
		t.Fatal(state, e)
	}
	if f.fake.initializes.Load() != 1 {
		t.Fatal("sweep initialized another charge")
	}
}
