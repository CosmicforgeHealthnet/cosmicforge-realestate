package integration

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"testing"
	"time"
)

func TestCategoryRepresentativeProtectedCollectionAndRelease(t *testing.T) {
	for _, key := range []string{"PROPERTY_PURCHASE", "LAND_PURCHASE", "COMMERCIAL_LEASE", "LAND_LEASE", "SHORT_LET", "HOTEL", "EVENT_VENUE", "MAINTENANCE", "PROFESSIONAL_SERVICE", "MANAGER_FEE", "UTILITIES", "PROMOTION_SUBSCRIPTION"} {
		t.Run(key, func(t *testing.T) {
			f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
			f.fund(t)
			var family lifecycle.Family
			for _, candidate := range lifecycle.Families() {
				if candidate.Key == key {
					family = candidate
				}
			}
			policy := makePolicy(key)
			policy.Conditions = []string{family.Conditions[0]}
			if _, e := f.l.PutPolicy(admin(), policy); e != nil {
				t.Fatal(e)
			}
			role := family.Roles[0]
			domain := "individual-owner"
			partyKind := "PERSON"
			source := f.user
			switch role {
			case "CORPORATE_OWNER", "HOSPITALITY":
				domain = "business-operations"
				partyKind = "ORGANIZATION"
			case "ARTISAN":
				domain = "artisan"
			case "PROFESSIONAL":
				domain = "professional-services"
			case "MANAGER":
				domain = "property-manager"
			case "SERVICE_COMPANY":
				domain = "artisan"
				partyKind = "ORGANIZATION"
			case "PLATFORM":
				domain = "admin"
				source = admin()
			}
			authority := f.authority
			authority.ID = ids.New()
			authority.Components = []string{family.Components[0]}
			authority.Role = role
			authority.MandateRef = "accepted-work-or-management-mandate"
			authority.OwnershipBPS = 0
			if role == "OWNER" || role == "CORPORATE_OWNER" {
				authority.OwnershipBPS = 10000
			}
			if role == "PLATFORM" {
				authority.WalletID = nil
				authority.LegalPartyRef = "cosmicforge-platform"
				authority.Domain = "admin"
			} else if role != "OWNER" {
				account := ids.New()
				service, _ := platform.ServiceForDomain(platform.Domain(domain))
				serviceCtx := ctx(service, platform.Domain(domain), account)
				w, e := f.s.EnsureWallet(serviceCtx, core.Provision{Domain: domain, AccountID: account, Role: role, PartyKind: partyKind, LegalEntity: f.w.LegalEntity, Currency: "NGN", RegistrationRef: "category-beneficiary"})
				if e != nil {
					t.Fatal(e)
				}
				if e = f.s.Verification(verifier(), w.IdentityID, "category-kyc", "VERIFIED", time.Now().Add(48*time.Hour)); e != nil {
					t.Fatal(e)
				}
				authority.WalletID = &w.WalletID
				authority.LegalPartyRef = domain + ":" + account.String()
			}
			if _, e := f.l.PutAuthority(source, authority); e != nil {
				t.Fatal(e)
			}
			q := f.quote
			q.ID = ids.New()
			q.PolicyID = policy.ID
			q.PayerType = family.Payers[0]
			q.Components = []lifecycle.Component{{Ref: "accepted-component", Kind: family.Components[0], AmountMinor: 10000, PayerRef: q.LegalPartyRef, Recurrence: "ONE_TIME"}}
			invalid := q
			invalid.Components = append([]lifecycle.Component(nil), q.Components...)
			invalid.Components[0].Kind = "UNCONFIGURED_COMPONENT"
			if _, e := f.l.LockQuote(f.user, invalid, "invalid-category"); e == nil {
				t.Fatal("unsupported component accepted")
			}
			i := collectQuote(t, f, q, policy, "category-charge")
			kind := "BENEFICIARY"
			if role == "PLATFORM" {
				kind = "PLATFORM"
			}
			a, e := f.l.Allocate(f.user, lifecycle.AllocationInput{IntentID: i.ID, Version: 1, Lines: []lifecycle.LineInput{{AuthorityID: authority.ID, ComponentRef: "accepted-component", Kind: kind, AmountMinor: 10000, Currency: "NGN"}}})
			if e != nil {
				t.Fatal(e)
			}
			line := a.Lines[0]
			evidence := ids.New()
			if e = f.l.SubmitEvidence(f.user, lifecycle.Evidence{ID: evidence, LineID: line.ID, Condition: family.Conditions[0], Reference: "authoritative-category-evidence", Version: "v1", PolicyVersion: policy.Version, ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
				t.Fatal(e)
			}
			f.p.charge, _ = f.fake.VerifyCharge(system(), i.Reference)
			if e = f.l.ConfirmFunding(admin(), lifecycle.FundingInput{IntentID: i.ID, SettlementRef: "42", CashEvidenceRef: "category-funded-fixture", Approvers: []string{"a", "b"}}); e != nil {
				t.Fatal(e)
			}
			if _, e = f.l.Release(f.user, lifecycle.Release{LineID: line.ID, AmountMinor: 10000, PolicyVersion: policy.Version, EvidenceIDs: []ids.ID{evidence}}, "category-release"); e != nil {
				t.Fatal(e)
			}
			got, e := f.l.Allocation(f.user, a.ID)
			if e != nil || got.Lines[0].ReleasedMinor != 10000 {
				t.Fatal(got, e)
			}
		})
	}
}

func TestRegulatedPartnerCannotBecomeUnlicensedPlatformWallet(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	p := makePolicy("REGULATED_FINANCE")
	p.Conditions = []string{"PARTNER_CONTRACT"}
	if _, e := f.l.PutPolicy(admin(), p); e == nil {
		t.Fatal("partner approval omitted")
	}
	p.PartnerApprovalRef = "licensed-partner-contract-test-fixture"
	if _, e := f.l.PutPolicy(admin(), p); e != nil {
		t.Fatal(e)
	}
	a := f.authority
	a.ID = ids.New()
	a.Role = "REGULATED_PARTNER"
	a.Domain = "admin"
	a.Components = []string{"PREMIUM"}
	if _, e := f.l.PutAuthority(admin(), a); e == nil {
		t.Fatal("ordinary owner wallet became insurance or mortgage partner")
	}
	if _, e := f.s.EnsureWallet(f.user, core.Provision{Domain: "individual-owner", AccountID: ids.New(), Role: "REGULATED_PARTNER", PartyKind: "ORGANIZATION", LegalEntity: f.w.LegalEntity, Currency: "NGN", RegistrationRef: "unlicensed"}); e == nil {
		t.Fatal("unlicensed partner wallet")
	}
}
