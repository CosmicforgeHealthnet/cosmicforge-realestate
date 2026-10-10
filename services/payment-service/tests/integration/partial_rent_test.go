package integration

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"testing"
	"time"
)

func TestLifecyclePartialReleaseAcrossPayoutAndRevokedVerificationFailure(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	a := f.allocate(t)
	line := a.Lines[0].ID
	f.fund(t)
	ev := f.evidence(t, line)
	release := func(n int64, key string) {
		t.Helper()
		if _, e := f.l.Release(f.user, lifecycle.Release{LineID: line, AmountMinor: n, PolicyVersion: f.policy.Version, EvidenceIDs: []ids.ID{ev}}, key); e != nil {
			t.Fatal(e)
		}
	}
	release(4000, "first-milestone")
	destination := f.destination(t)
	payout, e := f.l.Withdraw(f.user, f.withdrawal(t, line, destination, 4000), "first-payout")
	if e != nil {
		t.Fatal(e)
	}
	release(1000, "second-milestone-during-transfer")
	f.p.mu.Lock()
	f.p.transfer.State = "CONFIRMED_PAID"
	f.p.mu.Unlock()
	if out, e := f.l.ResolvePayout(f.user, payout.ID); e != nil || out.State != "CONFIRMED_PAID" {
		t.Fatal(out, e)
	}
	release(5000, "final-milestone-after-transfer")
	payout, e = f.l.Withdraw(f.user, f.withdrawal(t, line, destination, 6000), "remaining-payout")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Verification(verifier(), f.w.IdentityID, "kyc", "REVOKED", time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	f.p.mu.Lock()
	f.p.transfer.State = "CONFIRMED_FAILED"
	f.p.transfer.NoEffectConfirmed = true
	f.p.mu.Unlock()
	if out, e := f.l.ResolvePayout(f.user, payout.ID); e != nil || out.State != "CONFIRMED_FAILED" {
		t.Fatal(out, e)
	}
	var frozen, available string
	e = f.db.Pool.QueryRow(system(), `SELECT COALESCE(sum(balance_minor) FILTER(WHERE kind='FROZEN'),0)::text,COALESCE(sum(balance_minor) FILTER(WHERE kind='AVAILABLE'),0)::text FROM account_balances`).Scan(&frozen, &available)
	if e != nil || frozen != "6000" || available != "0" {
		t.Fatal(frozen, available, e)
	}
	if f.p.submissions.Load() != 2 {
		t.Fatal("provider attempts duplicated")
	}
	validateLifecycleOutbox(t, f)
}

func TestRentManagerFeeRequiresExactOwnerMandateContract(t *testing.T) {
	for _, matched := range []bool{false, true} {
		name := "wrong-contract"
		if matched {
			name = "accepted-contract"
		}
		t.Run(name, func(t *testing.T) {
			f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
			p := makePolicy("RESIDENTIAL_SUBSEQUENT")
			p.Conditions = []string{"VALID_TENANCY"}
			if _, e := f.l.PutPolicy(admin(), p); e != nil {
				t.Fatal(e)
			}
			account := ids.New()
			managerCtx := ctx(platform.PropertyManagerService, platform.DomainPropertyManager, account)
			w, e := f.s.EnsureWallet(managerCtx, core.Provision{Domain: "property-manager", AccountID: account, Role: "MANAGER", PartyKind: "PERSON", LegalEntity: f.w.LegalEntity, Currency: "NGN", RegistrationRef: "manager"})
			if e != nil {
				t.Fatal(e)
			}
			if e = f.s.Verification(verifier(), w.IdentityID, "manager-kyc", "VERIFIED", time.Now().Add(24*time.Hour)); e != nil {
				t.Fatal(e)
			}
			authority := lifecycle.Authority{ID: ids.New(), Domain: "individual-owner", BusinessRef: f.authority.BusinessRef, WalletID: &w.WalletID, LegalPartyRef: "property-manager:" + account.String(), Role: "MANAGER", Currency: "NGN", LegalEntity: f.w.LegalEntity, Components: []string{"MANAGER_FEE"}, EvidenceRef: "owner-accepted-fee", MandateRef: "owner-contract-v1", MaxMinor: 1000, ExpiresAt: time.Now().Add(time.Hour)}
			if _, e = f.l.PutAuthority(f.user, authority); e != nil {
				t.Fatal(e)
			}
			start := time.Now().Add(time.Hour)
			agreement := lifecycle.Agreement{ID: ids.New(), Domain: "individual-owner", Reference: "managed-tenancy", Version: "v1", ApprovalRef: "signed-tenancy", BusinessRef: f.authority.BusinessRef, PayerWalletID: f.w.WalletID, PayerPartyRef: f.authority.LegalPartyRef, PayerConsentRef: "accepted-rent", TenancyType: "CONTINUING", Status: "ACTIVE", Schedule: "ANNUAL", Currency: "NGN", Jurisdiction: "NG", EffectiveFrom: start.Add(-2 * time.Hour), StartsAt: start, InitialPolicyID: f.policy.ID, RecurringPolicyID: p.ID, RentShares: []lifecycle.RentShare{{AuthorityID: f.authority.ID, OwnershipBPS: 10000}}, ManagerAuthorityID: &authority.ID, ManagerFeeMinor: 1000, ManagerContractRef: "another-property-contract", Components: []lifecycle.Component{{Ref: "rent", Kind: "RENT", AmountMinor: 10000, PayerRef: f.authority.LegalPartyRef, Recurrence: "RECURRING"}}}
			if matched {
				agreement.ManagerContractRef = authority.MandateRef
			}
			if _, e = f.l.PutAgreement(f.user, agreement); e != nil {
				t.Fatal(e)
			}
			o, e := f.l.RentObligation(f.user, lifecycle.RentRequest{AgreementID: agreement.ID, PeriodRef: "period-1", PeriodStart: start, PeriodEnd: start.AddDate(1, 0, 0), DueAt: start, Stream: "RENT"})
			if e != nil {
				t.Fatal(e)
			}
			i := collectLockedQuote(t, f, o.Quote, p, "managed-rent-intent")
			allocation, e := f.l.RentAllocation(f.user, o.ID, i.ID)
			if !matched {
				if e == nil {
					t.Fatal("unrelated fee mandate accepted")
				}
				return
			}
			if e != nil || len(allocation.Lines) != 2 || allocation.Lines[0].AmountMinor != 9000 || allocation.Lines[1].AmountMinor != 1000 || *allocation.Lines[1].WalletID != w.WalletID {
				t.Fatal(allocation, e)
			}
		})
	}
}
