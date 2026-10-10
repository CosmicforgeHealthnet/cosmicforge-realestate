package integration

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"testing"
	"time"
)

func TestCorporatePayoutChainBusinessScopeAndBankOwnership(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	f.fund(t)
	account := ids.New()
	business := ctx(platform.BusinessOperationsService, platform.DomainBusinessOperations, account)
	wallet, e := f.s.EnsureWallet(business, core.Provision{Domain: "business-operations", AccountID: account, Role: "CORPORATE_OWNER", PartyKind: "ORGANIZATION", LegalEntity: f.w.LegalEntity, Currency: "NGN", RegistrationRef: "corporate-verification"})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Verification(verifier(), wallet.IdentityID, "corp-kyc", "VERIFIED", time.Now().Add(2*time.Hour)); e != nil {
		t.Fatal(e)
	}
	policy := makePolicy("COMMERCIAL_LEASE")
	policy.Conditions = []string{"CORPORATE_SIGNATORY"}
	if _, e = f.l.PutPolicy(admin(), policy); e != nil {
		t.Fatal(e)
	}
	rp := remedyPack(policy.ID)
	rp.ConsentRequired = true
	rp.CorporateApprovalRequired = true
	if e = f.l.PutRemedyPolicy(admin(), rp); e != nil {
		t.Fatal(e)
	}
	auth := f.authority
	auth.ID = ids.New()
	auth.Domain = "business-operations"
	auth.WalletID = &wallet.WalletID
	auth.Role = "CORPORATE_OWNER"
	auth.LegalPartyRef = "business-operations:" + account.String()
	if _, e = f.l.PutAuthority(business, auth); e != nil {
		t.Fatal(e)
	}
	q := f.quote
	q.ID = ids.New()
	q.PolicyID = policy.ID
	q.PayerType = "LESSEE"
	if _, e = f.l.LockQuote(f.user, q, "corp"); e != nil {
		t.Fatal(e)
	}
	i, e := f.s.CreateIntent(f.user, finance.IntentInput{LockedQuoteID: &q.ID, WalletID: q.PayerWalletID, AmountMinor: 10000, Currency: "NGN", Country: "NG", Category: policy.Key, EvidenceRef: q.QuoteVersion, PolicyVersion: policy.Version, Email: "lessee@example.invalid"}, "corp")
	if e != nil {
		t.Fatal(e)
	}
	f.fake.success(i)
	i, e = f.s.VerifyIntent(f.user, i.ID)
	if e != nil {
		t.Fatal(e)
	}
	f.p.charge, _ = f.fake.VerifyCharge(system(), i.Reference)
	a, e := f.l.Allocate(f.user, lifecycle.AllocationInput{IntentID: i.ID, Version: 1, Lines: []lifecycle.LineInput{{AuthorityID: auth.ID, ComponentRef: "rent", Kind: "BENEFICIARY", AmountMinor: 10000, Currency: "NGN"}}})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.l.ConfirmFunding(admin(), lifecycle.FundingInput{IntentID: i.ID, SettlementRef: "42", CashEvidenceRef: "corp-cash", Approvers: []string{"a", "b"}}); e != nil {
		t.Fatal(e)
	}
	ev := ids.New()
	if e = f.l.SubmitEvidence(f.user, lifecycle.Evidence{ID: ev, LineID: a.Lines[0].ID, Condition: "CORPORATE_SIGNATORY", Reference: "approved-signatory", Version: "v1", PolicyVersion: policy.Version, ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.l.Release(f.user, lifecycle.Release{LineID: a.Lines[0].ID, AmountMinor: 10000, PolicyVersion: policy.Version, EvidenceIDs: []ids.ID{ev}}, "corp"); e != nil {
		t.Fatal(e)
	}
	bank, e := f.s.RecordPayoutReference(verifier(), core.PayoutReferenceInput{WalletID: wallet.WalletID, BeneficiaryRef: "corporate-bank", VerificationRef: "corp-kyc", Jurisdiction: "NG", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.l.BindDestination(verifier(), lifecycle.Destination{ID: ids.New(), PayoutAccountID: bank.ID, ProviderID: f.cfg.ID, RecipientRef: "RCP_fixture", EvidenceRef: "bank-corp-owner", ExpiresAt: time.Now().Add(45 * time.Minute)}); e != nil {
		t.Fatal(e)
	}
	ap := lifecycle.WithdrawalApproval{ID: ids.New(), LineID: a.Lines[0].ID, WalletID: wallet.WalletID, PayoutAccountID: bank.ID, AmountMinor: 10000, StepUpRef: "step-up", RiskRef: "risk", Approvers: []string{"finance-a", "finance-b"}, ExpiresAt: time.Now().Add(30 * time.Minute)}
	if e = f.l.ApproveWithdrawal(admin(), ap); e == nil {
		t.Fatal("missing corporate chain")
	}
	ap.ApprovalChainRef = "approved-corporate-chain"
	ap.BranchProjectRef = "another-project"
	ap.ConsentRef = "signatory-consent"
	if e = f.l.ApproveWithdrawal(admin(), ap); e == nil {
		t.Fatal("wrong project")
	}
	ap.BranchProjectRef = q.BusinessRef
	if e = f.l.ApproveWithdrawal(admin(), ap); e != nil {
		t.Fatal(e)
	}
	in := lifecycle.Withdrawal{ApprovalID: ap.ID, LineID: ap.LineID, WalletID: ap.WalletID, PayoutAccountID: ap.PayoutAccountID, AmountMinor: ap.AmountMinor}
	if _, e = f.l.Withdraw(f.user, in, "redirect"); e == nil {
		t.Fatal("owner redirected corporate proceeds")
	}
	if _, e = f.l.Withdraw(business, in, "corp"); e != nil {
		t.Fatal(e)
	}
	validateLifecycleOutbox(t, f)
}

func TestGatewayCaseClaimantAndRecurringExecutionAbsence(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	a := f.allocate(t)
	f.fund(t)
	ev := f.remedy(t)
	gateway := ctx(platform.APIGateway, platform.DomainIndividualOwner, f.account)
	in := lifecycle.CaseRequest{ID: ids.New(), LineID: a.Lines[0].ID, Kind: "DISPUTE", AmountMinor: 1000, ClaimantRef: "another-party", RespondentRef: "payer", AuthorityRef: "signed-party", ReasonRef: "disputed", EvidenceIDs: []ids.ID{ev}}
	if _, e := f.l.OpenCase(gateway, in, "spoofed"); e == nil {
		t.Fatal("gateway spoofed claimant")
	}
	in.ClaimantRef = f.authority.LegalPartyRef
	if _, e := f.l.OpenCase(gateway, in, "owned"); e != nil {
		t.Fatal(e)
	}
	cfg := f.cfg
	cfg.Capabilities = append(cfg.Capabilities, "MANDATE")
	if e := f.s.Register(system(), cfg, finance.Adapter{Collection: f.fake, DVA: f.fake, Settlement: f.p, Payout: f.p, Refund: f.p}); e == nil {
		t.Fatal("mandate execution enabled")
	}
	design := lifecycle.ReservedRecurringDesign()
	if design.Execution != "DEFERRED" {
		t.Fatal(design)
	}
}

func TestFinancialMetricsKeepCurrencyAmountsSeparate(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	cfg := f.cfg
	cfg.ID = ids.New()
	cfg.PoolID = ids.New()
	cfg.MerchantRef = "1002"
	cfg.Currencies = []string{"USD"}
	cfg.Capabilities = []string{"COLLECT"}
	cfg.PayoutApproved = false
	if e := f.s.Register(system(), cfg, finance.Adapter{Collection: f.fake, Settlement: f.p}); e != nil {
		t.Fatal(e)
	}
	wallet, e := f.s.EnsureWallet(f.user, core.Provision{Domain: "individual-owner", AccountID: f.account, Role: "OWNER", PartyKind: "PERSON", LegalEntity: f.w.LegalEntity, Currency: "USD", RegistrationRef: "verified-other-currency"})
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.db.Pool.Begin(system())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(system())
	cash, e := f.s.Account(system(), tx, nil, cfg.PoolID, "SETTLED_CASH", cfg.LegalEntity, "USD", "")
	if e != nil {
		t.Fatal(e)
	}
	deposit, e := f.s.Account(system(), tx, &wallet.WalletID, cfg.PoolID, "DEPOSIT", cfg.LegalEntity, "USD", "segregated-fixture")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.PostTx(system(), tx, core.Posting{OperationRef: "usd-currency-test", PoolID: cfg.PoolID, LegalEntity: cfg.LegalEntity, Currency: "USD", PolicyVersion: "fixture", Authorization: map[string]string{"evidence_ref": "test-fixture", "approved_by": "payment-test"}, Entries: []core.Entry{{AccountID: cash, Side: "DEBIT", AmountMinor: 10000}, {AccountID: deposit, Side: "CREDIT", AmountMinor: 10000}}}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(system()); e != nil {
		t.Fatal(e)
	}
	metrics, e := f.l.OperationalMetrics(admin())
	if e != nil {
		t.Fatal(e)
	}
	if _, exists := metrics["deposit_balance_minor"]; exists {
		t.Fatal("mixed currencies summed")
	}
	if metrics["deposit_balance_minor.USD"] != "10000" || metrics["deposit_balance_minor.NGN"] != "0" || metrics["money_currency"] != "MIXED" {
		t.Fatal(metrics)
	}
}
