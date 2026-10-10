package integration

import (
	"fmt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"sync"
	"testing"
	"time"
)

func TestCategoryPoliciesAllEighteenFamiliesAndApprovalGates(t *testing.T) {
	f := setup(t)
	l := lifecycle.New(f.s)
	families := lifecycle.Families()
	if len(families) != 18 {
		t.Fatal(len(families))
	}
	keys := map[string]bool{}
	for _, family := range families {
		t.Run(family.Key, func(t *testing.T) {
			if keys[family.Key] || family.Enabled || len(family.Components) == 0 || len(family.Roles) == 0 || len(family.Payers) == 0 || len(family.Conditions) == 0 {
				t.Fatal(family)
			}
			keys[family.Key] = true
			p := makePolicy(family.Key)
			p.Conditions = []string{family.Conditions[0]}
			if family.Regulated {
				if _, e := l.PutPolicy(admin(), p); e == nil {
					t.Fatal("unlicensed partner")
				}
				p.PartnerApprovalRef = "licensed-partner-test-only"
			}
			if _, e := l.PutPolicy(admin(), p); e != nil {
				t.Fatal(e)
			}
			got, e := l.Policy(f.user, p.ID)
			if e != nil || got.RefundRef == "" || got.DisputeRef == "" || got.CancellationRef == "" {
				t.Fatal(got, e)
			}
			p.CommissionBPS = 200
			if _, e = l.PutPolicy(admin(), p); e == nil {
				t.Fatal("mutated historical policy")
			}
			p.ID = ids.New()
			p.Version = "regional-version"
			p.Jurisdiction = "NG-LA"
			p.CommissionBPS = 0
			if _, e = l.PutPolicy(admin(), p); e != nil {
				t.Fatal(e)
			}
		})
	}
	p := makePolicy("RESIDENTIAL_INITIAL")
	p.Version = "disabled-v2"
	p.ID = ids.New()
	p.Active = false
	p.ApprovalRef = ""
	p.CancellationRef = ""
	if _, e := l.PutPolicy(f.user, p); e == nil {
		t.Fatal("non-admin policy activation")
	}
	if _, e := l.PutPolicy(admin(), p); e != nil {
		t.Fatal(e)
	}
}
func TestRentInitialRecurringCommissionDepositContinuityAndRevision(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	recurring := makePolicy("RESIDENTIAL_SUBSEQUENT")
	recurring.Conditions = []string{"VALID_TENANCY"}
	recurring.CommissionBPS = 200
	recurring.CommissionContractRef = "owner-approved-commission-v1"
	if _, e := f.l.PutPolicy(admin(), recurring); e != nil {
		t.Fatal(e)
	}
	deposit := makePolicy("RESIDENTIAL_DEPOSIT")
	deposit.Conditions = []string{"MOVE_OUT"}
	if _, e := f.l.PutPolicy(admin(), deposit); e != nil {
		t.Fatal(e)
	}
	platformAuthority := lifecycle.Authority{ID: ids.New(), Domain: "admin", BusinessRef: f.authority.BusinessRef, LegalPartyRef: "cosmicforge-platform", Role: "PLATFORM", Currency: "NGN", LegalEntity: f.w.LegalEntity, Components: []string{"RENT", "ONBOARDING"}, EvidenceRef: "approved-owner-fee", MaxMinor: 1000000000, ExpiresAt: time.Now().Add(48 * time.Hour)}
	if _, e := f.l.PutAuthority(admin(), platformAuthority); e != nil {
		t.Fatal(e)
	}
	agreement := lifecycle.Agreement{ID: ids.New(), Domain: "individual-owner", Reference: "continuing-agreement", Version: "v1", ApprovalRef: "signed-current-tenancy", BusinessRef: f.authority.BusinessRef, PayerWalletID: f.w.WalletID, PayerPartyRef: f.authority.LegalPartyRef, PayerConsentRef: "accepted-full-quote", TenancyType: "CONTINUING", Status: "ACTIVE", Schedule: "ANNUAL", Currency: "NGN", Jurisdiction: "NG", EffectiveFrom: time.Now().Add(-24 * time.Hour), StartsAt: time.Now().Add(-24 * time.Hour), InitialPolicyID: f.policy.ID, RecurringPolicyID: recurring.ID, DepositPolicyID: &deposit.ID, PlatformAuthorityID: &platformAuthority.ID, RentShares: []lifecycle.RentShare{{AuthorityID: f.authority.ID, OwnershipBPS: 10000}}, ComponentAuthorities: map[string]ids.ID{"onboarding": platformAuthority.ID, "deposit": f.authority.ID}, Components: []lifecycle.Component{{Ref: "rent", Kind: "RENT", AmountMinor: 100000000, PayerRef: f.authority.LegalPartyRef, Recurrence: "RECURRING"}, {Ref: "onboarding", Kind: "ONBOARDING", AmountMinor: 20000000, PayerRef: f.authority.LegalPartyRef, Recurrence: "ONE_TIME"}, {Ref: "deposit", Kind: "DEPOSIT", AmountMinor: 10000000, PayerRef: f.authority.LegalPartyRef, Recurrence: "REFUNDABLE_DEPOSIT"}}}
	if _, e := f.l.PutAgreement(f.user, agreement); e != nil {
		t.Fatal(e)
	}
	start := time.Now().Add(time.Hour).Truncate(time.Second)
	initialReq := lifecycle.RentRequest{AgreementID: agreement.ID, PeriodRef: "year-1", PeriodStart: start, PeriodEnd: start.AddDate(1, 0, 0), DueAt: start, Initial: true, Stream: "RENT"}
	initial, e := f.l.RentObligation(f.user, initialReq)
	if e != nil || initial.Quote.AmountMinor != 120000000 || len(initial.Quote.Components) != 2 {
		t.Fatal(initial, e)
	}
	nextReq := initialReq
	nextReq.Initial = false
	nextReq.PeriodRef = "year-2"
	nextReq.PeriodStart = initialReq.PeriodEnd
	nextReq.PeriodEnd = nextReq.PeriodStart.AddDate(1, 0, 0)
	nextReq.DueAt = nextReq.PeriodStart
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[ids.ID]bool{}
	for n := 0; n < 10; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, e := f.l.RentObligation(f.user, nextReq)
			if e != nil {
				t.Error(e)
				return
			}
			if o.Quote.AmountMinor != 100000000 || len(o.Quote.Components) != 1 {
				t.Error(o)
			}
			mu.Lock()
			seen[o.ID] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != 1 {
		t.Fatal(seen)
	}
	next, e := f.l.RentObligation(f.user, nextReq)
	if e != nil {
		t.Fatal(e)
	}
	repeatedInitial := nextReq
	repeatedInitial.PeriodRef = "year-3"
	repeatedInitial.Initial = true
	repeatedInitial.PeriodStart = nextReq.PeriodEnd
	repeatedInitial.PeriodEnd = repeatedInitial.PeriodStart.AddDate(1, 0, 0)
	if _, e = f.l.RentObligation(f.user, repeatedInitial); e == nil {
		t.Fatal("repeated onboarding")
	}
	depositReq := initialReq
	depositReq.Stream = "DEPOSIT"
	depositReq.Initial = false
	d, e := f.l.RentObligation(f.user, depositReq)
	if e != nil || d.Quote.AmountMinor != 10000000 || d.Quote.Components[0].Kind != "DEPOSIT" {
		t.Fatal(d, e)
	}
	f.fund(t)
	in := finance.IntentInput{LockedQuoteID: &next.Quote.ID, WalletID: f.w.WalletID, AmountMinor: next.Quote.AmountMinor, Currency: "NGN", Country: "NG", Category: recurring.Key, PolicyVersion: recurring.Version, EvidenceRef: next.Quote.QuoteVersion, Email: "rent@example.invalid"}
	i, e := f.s.CreateIntent(f.user, in, "next-rent")
	if e != nil {
		t.Fatal(e)
	}
	f.fake.success(i)
	if _, e = f.s.VerifyIntent(f.user, i.ID); e != nil {
		t.Fatal(e)
	}
	a, e := f.l.RentAllocation(f.user, next.ID, i.ID)
	if e != nil {
		t.Fatal(e)
	}
	var owner, platform int64
	for _, line := range a.Lines {
		if line.Kind == "PLATFORM" {
			platform += line.AmountMinor
		} else {
			owner += line.AmountMinor
		}
	}
	if owner != 98000000 || platform != 2000000 {
		t.Fatal(owner, platform)
	}
	var earned string
	if e = f.db.Pool.QueryRow(system(), `SELECT COALESCE(sum(balance_minor),0)::text FROM account_balances WHERE kind='REVENUE'`).Scan(&earned); e != nil || earned != "0" {
		t.Fatal("unearned revenue", earned, e)
	}
	f.p.charge, _ = f.fake.VerifyCharge(system(), i.Reference)
	if e = f.l.ConfirmFunding(admin(), lifecycle.FundingInput{IntentID: i.ID, SettlementRef: "42", CashEvidenceRef: "recurring-rent-cash", Approvers: []string{"a", "b"}}); e != nil {
		t.Fatal(e)
	}
	for _, line := range a.Lines {
		ev := ids.New()
		if e = f.l.SubmitEvidence(f.user, lifecycle.Evidence{ID: ev, LineID: line.ID, Condition: "VALID_TENANCY", Reference: "tenancy-active", Version: "v1", PolicyVersion: recurring.Version, ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
			t.Fatal(e)
		}
		if _, e = f.l.Release(f.user, lifecycle.Release{LineID: line.ID, AmountMinor: line.AmountMinor, PolicyVersion: recurring.Version, EvidenceIDs: []ids.ID{ev}}, line.ID.String()); e != nil {
			t.Fatal(e)
		}
	}
	if e = f.db.Pool.QueryRow(system(), `SELECT COALESCE(sum(balance_minor),0)::text FROM account_balances WHERE kind='REVENUE'`).Scan(&earned); e != nil || earned != "2000000" {
		t.Fatal("commission not earned after funded release", earned, e)
	}
	receipt, e := f.l.Receipt(f.user, next.Quote.ID)
	if e != nil || receipt.SettlementState != "SETTLED" || len(receipt.Allocations) != 1 || len(receipt.Quote.Components) != 1 {
		t.Fatal(receipt, e)
	}
	revised := agreement
	revised.ID = ids.New()
	revised.PreviousID = &agreement.ID
	revised.Version = "v2"
	revised.EffectiveFrom = nextReq.PeriodEnd
	revised.Components = append([]lifecycle.Component(nil), agreement.Components...)
	revised.Components[0].AmountMinor = 110000000
	if _, e = f.l.PutAgreement(f.user, revised); e == nil {
		t.Fatal("unapproved material change")
	}
	revised.ChangeEvidenceRef = "authoritative-reviewed-notice-and-consent"
	if _, e = f.l.PutAgreement(f.user, revised); e != nil {
		t.Fatal(e)
	}
	future := nextReq
	future.PeriodRef = "year-3"
	future.PeriodStart = nextReq.PeriodEnd
	future.PeriodEnd = future.PeriodStart.AddDate(1, 0, 0)
	future.AgreementID = revised.ID
	o, e := f.l.RentObligation(f.user, future)
	if e != nil || o.Quote.AmountMinor != 110000000 {
		t.Fatal(o, e)
	}
	historic, e := f.l.Obligation(f.user, next.ID)
	if e != nil || historic.Quote.AmountMinor != 100000000 || historic.AgreementVersion != "v1" {
		t.Fatal(historic, e)
	}
	legalAgreement := agreement
	legalAgreement.ID = ids.New()
	legalAgreement.Reference = "separately-accepted-legal-work"
	legalAgreement.Components = []lifecycle.Component{agreement.Components[0], {Ref: "legal-work", Kind: "LEGAL", AmountMinor: 1000000, PayerRef: agreement.PayerPartyRef, Recurrence: "ACTUAL_SERVICE_ONLY"}}
	if _, e = f.l.PutAgreement(f.user, legalAgreement); e == nil {
		t.Fatal("legal fee without actual work evidence")
	}
	legalAgreement.Components[1].ServiceEvidenceRef = "accepted-legal-work-order"
	legalAgreement.Components[1].Recurrence = "RECURRING"
	if _, e = f.l.PutAgreement(f.user, legalAgreement); e == nil {
		t.Fatal("automatic recurring legal fee")
	}
	legalAgreement.Components[1].Recurrence = "ACTUAL_SERVICE_ONLY"
	if _, e = f.l.PutAgreement(f.user, legalAgreement); e != nil {
		t.Fatal(e)
	}
	legalInitial := initialReq
	legalInitial.AgreementID = legalAgreement.ID
	legalFirst, e := f.l.RentObligation(f.user, legalInitial)
	if e != nil || legalFirst.Quote.AmountMinor != 101000000 {
		t.Fatal(legalFirst, e)
	}
	legalNext := nextReq
	legalNext.AgreementID = legalAgreement.ID
	legalRecurring, e := f.l.RentObligation(f.user, legalNext)
	if e != nil || legalRecurring.Quote.AmountMinor != 100000000 || len(legalRecurring.Quote.Components) != 1 {
		t.Fatal("old legal work recurred", legalRecurring, e)
	}
	var initializes int32 = f.fake.initializes.Load()
	if initializes != 2 {
		t.Fatal("obligations charged automatically", initializes)
	}
	validateLifecycleOutbox(t, f)
}
func TestLifecycleIndependentHoldManagerEntitlementAndDepositProtection(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	managerAccount := ids.New()
	managerCtx := ctx(platform.PropertyManagerService, platform.DomainPropertyManager, managerAccount)
	w, e := f.s.EnsureWallet(managerCtx, core.Provision{Domain: "property-manager", AccountID: managerAccount, Role: "MANAGER", PartyKind: "PERSON", LegalEntity: f.w.LegalEntity, Currency: "NGN", RegistrationRef: "manager-registration"})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Verification(verifier(), w.IdentityID, "manager-kyc", "VERIFIED", time.Now().Add(48*time.Hour)); e != nil {
		t.Fatal(e)
	}
	manager := lifecycle.Authority{ID: ids.New(), Domain: "individual-owner", BusinessRef: f.authority.BusinessRef, WalletID: &w.WalletID, LegalPartyRef: "property-manager:" + managerAccount.String(), Role: "MANAGER", Currency: "NGN", LegalEntity: f.w.LegalEntity, Components: []string{"RENT"}, EvidenceRef: "owner-signed-fee", MandateRef: "active-property-mandate", MaxMinor: 1000, ExpiresAt: time.Now().Add(time.Hour)}
	if _, e = f.l.PutAuthority(f.user, manager); e == nil {
		t.Fatal("manager theft")
	}
	manager.Components = []string{"MANAGER_FEE"}
	if _, e = f.l.PutAuthority(managerCtx, manager); e == nil {
		t.Fatal("manager self-mandate")
	}
	if _, e = f.l.PutAuthority(f.user, manager); e != nil {
		t.Fatal(e)
	}
	in := lifecycle.AllocationInput{IntentID: f.intent.ID, Version: 1, Lines: []lifecycle.LineInput{{AuthorityID: f.authority.ID, ComponentRef: "rent", Kind: "BENEFICIARY", AmountMinor: 9000, Currency: "NGN"}, {AuthorityID: manager.ID, ComponentRef: "rent", Kind: "BENEFICIARY", AmountMinor: 1000, Currency: "NGN"}}}
	a, e := f.l.Allocate(f.user, in)
	if e != nil {
		t.Fatal(e)
	}
	f.fund(t)
	if e = f.l.Hold(f.user, a.Lines[1].ID, lifecycle.Hold{State: "DISPUTED", EvidenceRef: "manager-dispute"}, "hold"); e != nil {
		t.Fatal(e)
	}
	for n, line := range a.Lines {
		ev := f.evidence(t, line.ID)
		_, e = f.l.Release(f.user, lifecycle.Release{LineID: line.ID, AmountMinor: line.AmountMinor, PolicyVersion: f.policy.Version, EvidenceIDs: []ids.ID{ev}}, fmt.Sprint(n))
		if (n == 0 && e != nil) || (n == 1 && e == nil) {
			t.Fatal(n, e)
		}
	}
	malicious := f.authority
	malicious.ID = ids.New()
	malicious.Role = "CO_OWNER"
	malicious.OwnershipBPS = 0
	if _, e = f.l.PutAuthority(f.user, malicious); e == nil {
		t.Fatal("unverified co-owner share")
	}
	if e = f.l.RevokeAuthority(f.user, manager.ID, "mandate-ended"); e != nil {
		t.Fatal(e)
	}
	if e = f.l.Hold(f.user, a.Lines[1].ID, lifecycle.Hold{State: "PROTECTED", EvidenceRef: "resolved", Approvers: []string{"a", "b"}}, "clear"); e != nil {
		t.Fatal(e)
	}
	ev := f.evidence(t, a.Lines[1].ID)
	if _, e = f.l.Release(f.user, lifecycle.Release{LineID: a.Lines[1].ID, AmountMinor: 1000, PolicyVersion: f.policy.Version, EvidenceIDs: []ids.ID{ev}}, "revoked"); e == nil {
		t.Fatal("revoked mandate release")
	}
}
