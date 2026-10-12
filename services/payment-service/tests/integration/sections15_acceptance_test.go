package integration

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"testing"
	"time"
)

// Local synthetic policy/cash fixtures: no provider or safeguarding certification.
func acceptanceBeneficiary(t *testing.T, f *lifeFixture, domain, role, kind string) (core.Wallet, context.Context) {
	t.Helper()
	account := ids.New()
	service, _ := platform.ServiceForDomain(platform.Domain(domain))
	actor := ctx(service, platform.Domain(domain), account)
	w, e := f.s.EnsureWallet(actor, core.Provision{Domain: domain, AccountID: account, Role: role, PartyKind: kind, LegalEntity: f.w.LegalEntity, Currency: "NGN", RegistrationRef: "synthetic-acceptance-principal"})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Verification(verifier(), w.IdentityID, "synthetic-acceptance-verification", "VERIFIED", time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	return w, actor
}
func acceptanceEvidence(t *testing.T, f *lifeFixture, issuer context.Context, line ids.ID, condition string) ids.ID {
	t.Helper()
	id := ids.New()
	if e := f.l.SubmitEvidence(issuer, lifecycle.Evidence{ID: id, LineID: line, Condition: condition, Reference: "synthetic-acceptance-milestone", Version: "v1", PolicyVersion: f.policy.Version, ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	return id
}
func acceptanceBalance(t *testing.T, f *lifeFixture, w core.Wallet, kind string) string {
	t.Helper()
	var amount string
	if e := f.db.Pool.QueryRow(system(), `SELECT COALESCE(sum(balance_minor),0)::text FROM account_balances WHERE wallet_id=$1 AND kind=$2`, w.WalletID, kind).Scan(&amount); e != nil {
		t.Fatal(e)
	}
	return amount
}

func TestA05ShortletOwnerManagerPlatformSeparateMilestones(t *testing.T) {
	f := newAcceptanceLife(t, "SHORT_LET", 10000, func(p *lifecycle.Policy) { p.Conditions = []string{"CHECK_IN", "STAY_COMPLETE"} }, func(q *lifecycle.Quote) {
		q.PayerType = "GUEST"
		q.Components = []lifecycle.Component{{Ref: "stay", Kind: "ACCOMMODATION", AmountMinor: 8000, Recurrence: "EVENT_DRIVEN", PayerRef: q.LegalPartyRef}, {Ref: "booking", Kind: "BOOKING", AmountMinor: 2000, Recurrence: "EVENT_DRIVEN", PayerRef: q.LegalPartyRef}}
	})
	policy := f.policy
	owner := f.authority
	owner.ID = ids.New()
	owner.Components = []string{"ACCOMMODATION"}
	if _, e := f.l.PutAuthority(f.user, owner); e != nil {
		t.Fatal(e)
	}
	wallet, _ := acceptanceBeneficiary(t, f, "property-manager", "MANAGER", "PERSON")
	manager := lifecycle.Authority{ID: ids.New(), Domain: "individual-owner", BusinessRef: f.quote.BusinessRef, WalletID: &wallet.WalletID, LegalPartyRef: "", Role: "MANAGER", Currency: "NGN", LegalEntity: wallet.LegalEntity, Components: []string{"BOOKING"}, EvidenceRef: "synthetic-owner-fee-mandate", MandateRef: "synthetic-active-property-mandate", MaxMinor: 2000, ExpiresAt: time.Now().Add(time.Hour)}
	profile, e := f.s.WalletProfile(system(), wallet.WalletID)
	if e != nil {
		t.Fatal(e)
	}
	manager.LegalPartyRef = profile.CanonicalPartyRef
	if _, e = f.l.PutAuthority(f.user, manager); e != nil {
		t.Fatal(e)
	}
	platformAuthority := lifecycle.Authority{ID: ids.New(), Domain: "admin", BusinessRef: f.quote.BusinessRef, LegalPartyRef: "synthetic-platform", Role: "PLATFORM", Currency: "NGN", LegalEntity: f.w.LegalEntity, Components: []string{"BOOKING"}, EvidenceRef: "synthetic-disclosed-booking-fee", MaxMinor: 2000, ExpiresAt: time.Now().Add(time.Hour)}
	if _, e = f.l.PutAuthority(admin(), platformAuthority); e != nil {
		t.Fatal(e)
	}
	a, e := f.l.Allocate(f.user, lifecycle.AllocationInput{IntentID: f.intent.ID, Version: 1, Lines: []lifecycle.LineInput{{AuthorityID: owner.ID, ComponentRef: "stay", Kind: "BENEFICIARY", AmountMinor: 8000, Currency: "NGN"}, {AuthorityID: manager.ID, ComponentRef: "booking", Kind: "BENEFICIARY", AmountMinor: 1000, Currency: "NGN"}, {AuthorityID: platformAuthority.ID, ComponentRef: "booking", Kind: "PLATFORM", AmountMinor: 1000, Currency: "NGN"}}})
	if e != nil {
		t.Fatal(e)
	}
	f.fund(t)
	for _, line := range a.Lines {
		if _, e = f.l.Release(f.user, lifecycle.Release{LineID: line.ID, AmountMinor: line.AmountMinor, PolicyVersion: policy.Version}, "uncertified"); e == nil {
			t.Fatal("milestone-free release")
		}
	}
	if acceptanceBalance(t, f, f.w, "AVAILABLE") != "0" || acceptanceBalance(t, f, wallet, "AVAILABLE") != "0" {
		t.Fatal("unearned beneficiary availability")
	}
	for n, line := range a.Lines {
		checkin := acceptanceEvidence(t, f, f.user, line.ID, "CHECK_IN")
		if _, e = f.l.Release(f.user, lifecycle.Release{LineID: line.ID, AmountMinor: line.AmountMinor, PolicyVersion: policy.Version, EvidenceIDs: []ids.ID{checkin}}, "early"); e == nil {
			t.Fatal("incomplete stay milestones")
		}
		complete := acceptanceEvidence(t, f, f.user, line.ID, "STAY_COMPLETE")
		if _, e = f.l.Release(f.user, lifecycle.Release{LineID: line.ID, AmountMinor: line.AmountMinor, PolicyVersion: policy.Version, EvidenceIDs: []ids.ID{checkin, complete}}, "completed"); e != nil {
			t.Fatal(e)
		}
		if n == 0 && acceptanceBalance(t, f, wallet, "AVAILABLE") != "0" {
			t.Fatal("owner release released manager share")
		}
	}
	var revenue string
	if e = f.db.Pool.QueryRow(system(), `SELECT COALESCE(sum(balance_minor),0)::text FROM account_balances WHERE kind='REVENUE'`).Scan(&revenue); e != nil {
		t.Fatal(e)
	}
	if acceptanceBalance(t, f, f.w, "AVAILABLE") != "8000" || acceptanceBalance(t, f, wallet, "AVAILABLE") != "1000" || revenue != "1000" {
		t.Fatal("shortlet split mismatch", revenue)
	}
	validateLifecycleOutbox(t, f)
}
func TestA07VerifiedCoOwnerPercentagesAndRevokedMandate(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	wallet, _ := acceptanceBeneficiary(t, f, "individual-owner", "CO_OWNER", "PERSON")
	profile, e := f.s.WalletProfile(system(), wallet.WalletID)
	if e != nil {
		t.Fatal(e)
	}
	owner := f.authority
	owner.ID = ids.New()
	owner.OwnershipBPS = 6000
	owner.EvidenceRef = "synthetic-verified-co-ownership-60"
	coowner := owner
	coowner.ID = ids.New()
	coowner.WalletID = &wallet.WalletID
	coowner.LegalPartyRef = profile.CanonicalPartyRef
	coowner.Role = "CO_OWNER"
	coowner.OwnershipBPS = 4000
	coowner.EvidenceRef = "synthetic-verified-co-ownership-40"
	for _, authority := range []lifecycle.Authority{owner, coowner} {
		if _, e = f.l.PutAuthority(f.user, authority); e != nil {
			t.Fatal(e)
		}
	}
	input := lifecycle.AllocationInput{IntentID: f.intent.ID, Version: 1, Lines: []lifecycle.LineInput{{AuthorityID: owner.ID, ComponentRef: "rent", Kind: "BENEFICIARY", AmountMinor: 6001, Currency: "NGN"}, {AuthorityID: coowner.ID, ComponentRef: "rent", Kind: "BENEFICIARY", AmountMinor: 3999, Currency: "NGN"}}}
	if _, e = f.l.Allocate(f.user, input); e == nil {
		t.Fatal("unmandated percentages allocated")
	}
	input.Lines[0].AmountMinor = 6000
	input.Lines[1].AmountMinor = 4000
	a, e := f.l.Allocate(f.user, input)
	if e != nil {
		t.Fatal(e)
	}
	f.fund(t)
	if _, e = f.l.Release(ctx(platform.PropertyManagerService, platform.DomainPropertyManager, ids.New()), lifecycle.Release{LineID: a.Lines[0].ID, AmountMinor: 6000, PolicyVersion: f.policy.Version}, "manager-theft"); e == nil {
		t.Fatal("manager redirected owner share")
	}
	proof := f.evidence(t, a.Lines[1].ID)
	if e = f.l.RevokeAuthority(f.user, coowner.ID, "synthetic-co-owner-mandate-revoked"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.l.Release(f.user, lifecycle.Release{LineID: a.Lines[1].ID, AmountMinor: 4000, PolicyVersion: f.policy.Version, EvidenceIDs: []ids.ID{proof}}, "revoked-owner"); e == nil {
		t.Fatal("revoked co-owner released")
	}
	proof = f.evidence(t, a.Lines[0].ID)
	if _, e = f.l.Release(f.user, lifecycle.Release{LineID: a.Lines[0].ID, AmountMinor: 6000, PolicyVersion: f.policy.Version, EvidenceIDs: []ids.ID{proof}}, "authorized-owner"); e != nil {
		t.Fatal(e)
	}
	if acceptanceBalance(t, f, f.w, "AVAILABLE") != "6000" || acceptanceBalance(t, f, wallet, "PROTECTED") != "4000" || acceptanceBalance(t, f, wallet, "AVAILABLE") != "0" {
		t.Fatal("co-owner entitlement boundaries")
	}
}
func TestA09OffPlanIndependentConstructionCertification(t *testing.T) {
	f := newAcceptanceLife(t, "OFF_PLAN", 10000, func(p *lifecycle.Policy) {
		p.Conditions = []string{"INDEPENDENT_CERTIFICATION", "PROJECT_ALLOCATION"}
		p.EvidenceServices = []string{string(platform.VerificationService)}
	}, func(q *lifecycle.Quote) {
		q.PayerType = "BUYER"
		q.Components = []lifecycle.Component{{Ref: "construction", Kind: "CONSTRUCTION", AmountMinor: 10000, Recurrence: "EVENT_DRIVEN", PayerRef: q.LegalPartyRef}}
	})
	policy := f.policy
	wallet, _ := acceptanceBeneficiary(t, f, "business-operations", "DEVELOPER", "ORGANIZATION")
	profile, e := f.s.WalletProfile(system(), wallet.WalletID)
	if e != nil {
		t.Fatal(e)
	}
	authority := lifecycle.Authority{ID: ids.New(), Domain: "business-operations", BusinessRef: f.quote.BusinessRef, WalletID: &wallet.WalletID, LegalPartyRef: profile.CanonicalPartyRef, Role: "DEVELOPER", Currency: "NGN", LegalEntity: wallet.LegalEntity, Components: []string{"CONSTRUCTION"}, EvidenceRef: "synthetic-project-contract", MaxMinor: 10000, ExpiresAt: time.Now().Add(time.Hour)}
	business := ctx(platform.BusinessOperationsService, "", ids.Nil)
	// The project beneficiary's authoritative service issues its own entitlement;
	// the accepted quote's source remains Owner in this isolated adapter scenario.
	if _, e = f.l.PutAuthority(business, authority); e != nil {
		t.Fatal(e)
	}
	a, e := f.l.Allocate(f.user, lifecycle.AllocationInput{IntentID: f.intent.ID, Version: 1, Lines: []lifecycle.LineInput{{AuthorityID: authority.ID, ComponentRef: "construction", Kind: "BENEFICIARY", AmountMinor: 10000, Currency: "NGN"}}})
	if e != nil {
		t.Fatal(e)
	}
	f.fund(t)
	line := a.Lines[0]
	if _, e = f.l.Release(f.user, lifecycle.Release{LineID: line.ID, AmountMinor: 10000, PolicyVersion: policy.Version}, "uncertified-project"); e == nil {
		t.Fatal("uncertified construction released")
	}
	if e = f.l.SubmitEvidence(business, lifecycle.Evidence{ID: ids.New(), LineID: line.ID, Condition: "INDEPENDENT_CERTIFICATION", Reference: "self-certification", Version: "v1", PolicyVersion: policy.Version, ExpiresAt: time.Now().Add(time.Hour)}); e == nil {
		t.Fatal("developer self-certified")
	}
	project := acceptanceEvidence(t, f, verifier(), line.ID, "PROJECT_ALLOCATION")
	if _, e = f.l.Release(f.user, lifecycle.Release{LineID: line.ID, AmountMinor: 10000, PolicyVersion: policy.Version, EvidenceIDs: []ids.ID{project}}, "missing-independent"); e == nil {
		t.Fatal("project allocation substituted certification")
	}
	certificate := acceptanceEvidence(t, f, verifier(), line.ID, "INDEPENDENT_CERTIFICATION")
	if _, e = f.l.Release(f.user, lifecycle.Release{LineID: line.ID, AmountMinor: 10000, PolicyVersion: policy.Version, EvidenceIDs: []ids.ID{project, certificate}}, "certified-project"); e != nil {
		t.Fatal(e)
	}
	if acceptanceBalance(t, f, wallet, "AVAILABLE") != "10000" {
		t.Fatal("certified milestone missing")
	}
	if _, e = f.l.Release(f.user, lifecycle.Release{LineID: line.ID, AmountMinor: 1, PolicyVersion: policy.Version, EvidenceIDs: []ids.ID{project, certificate}}, "over-milestone"); e == nil {
		t.Fatal("project contract amount exceeded")
	}
}
