package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Extends the existing cross-process harness, not a parallel financial harness.
// All provider, commercial, KYC and consent references here are synthetic TEST
// fixtures. Future domain actors are signed contract simulators in the Owner child.
func registerOwnerWorkflowFixtures(t *testing.T, mux *http.ServeMux, f *fixture, l *lifecycle.Service, p *payoutFixture, flush func() error) {
	t.Helper()
	fail := func(w http.ResponseWriter, e error) { t.Helper(); t.Error(e); w.WriteHeader(500) }
	mux.HandleFunc("POST /test-fixtures/flush-events", func(w http.ResponseWriter, r *http.Request) {
		if e := flush(); e != nil {
			fail(w, e)
			return
		}
		w.WriteHeader(200)
	})
	mux.HandleFunc("POST /test-fixtures/customer-wallet-spend", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			QuoteID    ids.ID                `json:"quote_id"`
			CustomerID ids.ID                `json:"customer_id"`
			Lines      []lifecycle.LineInput `json:"lines"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			fail(w, e)
			return
		}
		q, e := l.Quote(system(), in.QuoteID)
		if e != nil {
			fail(w, e)
			return
		}
		lf := &lifeFixture{fixture: f, l: l, p: p}
		wallet, user, policy := customerFixtureFor(t, lf, in.CustomerID)
		fundedCustomerAmount(t, lf, wallet, user, policy, q.AmountMinor+1000)
		spend := lifecycle.WalletSpend{WalletID: wallet.WalletID, QuoteID: q.ID, PoolID: f.cfg.PoolID, ConsentRef: q.PayerConsentRef, QuoteVersion: q.QuoteVersion, AmountMinor: q.AmountMinor, Currency: q.Currency, Lines: in.Lines}
		allocation, e := l.SpendWallet(user, spend, "accepted-wallet:"+q.ID.String())
		if e != nil {
			fail(w, e)
			return
		}
		again, e := l.SpendWallet(user, spend, "accepted-wallet:"+q.ID.String())
		if e != nil || allocation.ID != again.ID {
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(allocation)
	})
	mux.HandleFunc("POST /test-fixtures/remedy-evidence", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			QuoteID ids.ID `json:"quote_id"`
			OwnerID ids.ID `json:"owner_id"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			fail(w, e)
			return
		}
		source := ctx(platform.IndividualOwnerService, platform.DomainIndividualOwner, in.OwnerID)
		q, e := l.Quote(source, in.QuoteID)
		if e != nil {
			fail(w, e)
			return
		}
		ev := lifecycle.RemedyEvidence{ID: ids.New(), BusinessRef: q.BusinessRef, Reference: "synthetic-local-inspection", Version: "fixture-v1", ActorRef: "synthetic-inspector", HashRef: "sha256:fixture", ObservedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour)}
		if e = l.PutRemedyEvidence(source, ev); e != nil {
			fail(w, e)
			return
		}
		_ = json.NewEncoder(w).Encode(ev)
	})
	mux.HandleFunc("POST /test-fixtures/register-deposit", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			OwnerID ids.ID            `json:"owner_id"`
			Deposit lifecycle.Deposit `json:"deposit"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			fail(w, e)
			return
		}
		d, e := l.RegisterDeposit(ctx(platform.IndividualOwnerService, platform.DomainIndividualOwner, in.OwnerID), in.Deposit)
		if e != nil {
			fail(w, e)
			return
		}
		_ = json.NewEncoder(w).Encode(d)
	})
	mux.HandleFunc("POST /test-fixtures/approved-refund", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			CaseID     ids.ID `json:"case_id"`
			LineID     ids.ID `json:"line_id"`
			EvidenceID ids.ID `json:"evidence_id"`
			Amount     int64  `json:"amount_minor"`
			Unknown    bool   `json:"unknown"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			fail(w, e)
			return
		}
		fact := lifecycle.CaseFact{ID: ids.New(), CaseID: in.CaseID, Kind: "REVIEW", ActorRef: "synthetic-independent-reviewer", AuthorityRef: "synthetic-review-authority", ReasonRef: "local-evidence-reviewed", EvidenceIDs: []ids.ID{in.EvidenceID}}
		if e := l.AppendCaseFact(admin(), fact, "review:"+in.CaseID.String()); e != nil {
			fail(w, e)
			return
		}
		decision := lifecycle.CaseDecision{CaseFact: lifecycle.CaseFact{ID: ids.New(), CaseID: in.CaseID, Kind: "DECISION", ActorRef: "synthetic-decision-maker", AuthorityRef: "synthetic-decision-authority", ReasonRef: "approved-local-return", EvidenceIDs: []ids.ID{in.EvidenceID}}, Outcome: "RETURN_TO_PAYER", ReviewerRef: "synthetic-independent-reviewer", ExecutorRef: "synthetic-executor", Approvers: []string{"fixture-finance-a", "fixture-finance-b"}}
		if e := l.DecideCase(admin(), decision, "decision:"+in.CaseID.String()); e != nil {
			fail(w, e)
			return
		}
		refund, e := l.RequestRefund(admin(), lifecycle.RefundRequest{CaseID: in.CaseID, LineID: in.LineID, AmountMinor: in.Amount, Strategy: "ORIGINAL_INSTRUMENT", AuthorityRef: "synthetic-approved-return", EvidenceIDs: []ids.ID{in.EvidenceID}, Approvers: []string{"fixture-finance-a", "fixture-finance-b"}}, "refund:"+in.CaseID.String())
		if e != nil {
			fail(w, e)
			return
		}
		p.mu.Lock()
		p.refundUnknown = in.Unknown
		p.mu.Unlock()
		refund, e = l.SubmitRefund(admin(), refund.ID)
		if e != nil {
			fail(w, e)
			return
		}
		_ = json.NewEncoder(w).Encode(refund)
	})
	mux.HandleFunc("POST /test-fixtures/refund-readback", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID      ids.ID `json:"refund_id"`
			State   string `json:"state"`
			Unknown bool   `json:"unknown"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			fail(w, e)
			return
		}
		refund, e := l.Refund(system(), in.ID)
		if e != nil {
			fail(w, e)
			return
		}
		p.mu.Lock()
		p.refundUnknown = in.Unknown
		if in.State != "" {
			v := p.refunds[refund.AttemptReference]
			v.State = in.State
			p.refunds[refund.AttemptReference] = v
		}
		p.mu.Unlock()
		refund, e = l.ResolveRefund(system(), in.ID)
		if e != nil {
			fail(w, e)
			return
		}
		_ = json.NewEncoder(w).Encode(refund)
	})
	mux.HandleFunc("POST /test-fixtures/reverse-collection", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID     ids.ID `json:"intent_id"`
			Amount int64  `json:"amount_minor"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			fail(w, e)
			return
		}
		i, e := f.s.GetIntent(system(), in.ID)
		if e != nil {
			fail(w, e)
			return
		}
		ref := "reversal:" + i.ID.String()
		p.mu.Lock()
		p.reversal = providers.Reversal{Reference: ref, EconomicReference: ref, ChargeReference: i.Reference, AmountMinor: in.Amount, Currency: i.Currency, Mode: "test", MerchantID: 1001, Confirmed: true}
		p.mu.Unlock()
		rev, e := l.ReverseCollection(admin(), lifecycle.ReversalRequest{IntentID: i.ID, ProviderReference: ref, AuthorityRef: "synthetic-confirmed-provider-reversal", Approvers: []string{"fixture-a", "fixture-b"}})
		if e != nil {
			fail(w, e)
			return
		}
		_ = json.NewEncoder(w).Encode(rev)
	})
	mux.HandleFunc("POST /test-fixtures/workflow-quote", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Business struct {
				Type             string   `json:"business_type"`
				ID               ids.ID   `json:"business_id"`
				OwnerID          ids.ID   `json:"owner_account_id"`
				Amount           int64    `json:"amount_minor"`
				Currency         string   `json:"currency"`
				PayerRef         string   `json:"payer_source_ref"`
				PayerTypes       []string `json:"payer_types"`
				Families         []string `json:"policy_families"`
				AgreementRef     string   `json:"agreement_ref"`
				AgreementVersion string   `json:"agreement_version"`
				Country          string   `json:"country"`
			} `json:"business"`
			Family     string                `json:"family"`
			Components []lifecycle.Component `json:"components"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			fail(w, e)
			return
		}
		b := in.Business
		parts := strings.Split(b.PayerRef, ":")
		if len(parts) != 2 {
			w.WriteHeader(400)
			return
		}
		payer, e := ids.ParseV7(parts[1])
		if e != nil {
			fail(w, e)
			return
		}
		svc, ok := platform.ServiceForDomain(platform.Domain(parts[0]))
		if !ok {
			w.WriteHeader(400)
			return
		}
		role := "CUSTOMER"
		if parts[0] == "individual-owner" {
			role = "OWNER"
		}
		wallet, e := f.s.EnsureWallet(ctx(svc, platform.Domain(parts[0]), payer), core.Provision{Domain: parts[0], AccountID: payer, Role: role, PartyKind: "PERSON", LegalEntity: f.w.LegalEntity, Currency: b.Currency, RegistrationRef: "synthetic-owner-journey-payer"})
		if e != nil {
			fail(w, e)
			return
		}
		profile, e := f.s.WalletProfile(system(), wallet.WalletID)
		if e != nil {
			fail(w, e)
			return
		}
		family := in.Family
		if family == "" {
			family = b.Families[0]
		}
		policy := makePolicy(family)
		policy.Conditions = map[string][]string{"RESIDENTIAL_INITIAL": {"SIGNED_AGREEMENT"}, "RESIDENTIAL_SUBSEQUENT": {"RENT_DUE"}, "TENANCY_RENEWAL": {"VALID_TENANCY"}, "RESIDENTIAL_DEPOSIT": {"MOVE_OUT", "APPROVED_CLAIM", "DISPUTE_DECISION"}, "PROPERTY_PURCHASE": {"CLOSING"}, "LAND_PURCHASE": {"CLOSING"}, "LAND_LEASE": {"POSSESSION"}, "COMMERCIAL_LEASE": {"EXECUTED_LEASE"}, "SHORT_LET": {"STAY_COMPLETE"}, "MAINTENANCE": {"ACCEPTANCE"}, "PROFESSIONAL_SERVICE": {"ACCEPTANCE"}}[family]
		var existing ids.ID
		if find := f.db.Pool.QueryRow(system(), `SELECT policy_id FROM financial_policies WHERE policy_key=$1 AND active ORDER BY effective_from DESC LIMIT 1`, family).Scan(&existing); find == nil {
			policy, e = l.Policy(admin(), existing)
			if e != nil {
				fail(w, e)
				return
			}
		} else if _, e = l.PutPolicy(admin(), policy); e != nil {
			fail(w, e)
			return
		}
		var remedyExists bool
		_ = f.db.Pool.QueryRow(system(), `SELECT EXISTS(SELECT 1 FROM remedy_policies WHERE policy_id=$1)`, policy.ID).Scan(&remedyExists)
		if !remedyExists {
			if e = l.PutRemedyPolicy(admin(), remedyPack(policy.ID)); e != nil {
				fail(w, e)
				return
			}
		}
		components := in.Components
		if len(components) == 0 {
			kind := map[string]string{"RESIDENTIAL_INITIAL": "RENT", "RESIDENTIAL_SUBSEQUENT": "RENT", "TENANCY_RENEWAL": "RENT", "RESIDENTIAL_DEPOSIT": "DEPOSIT", "PROPERTY_PURCHASE": "SALE", "LAND_PURCHASE": "SALE", "LAND_LEASE": "OCCUPATION", "COMMERCIAL_LEASE": "RENT", "SHORT_LET": "ACCOMMODATION", "MAINTENANCE": "LABOUR", "PROFESSIONAL_SERVICE": "APPOINTMENT"}[family]
			recurrence := "EVENT_DRIVEN"
			if kind == "RENT" {
				recurrence = "RECURRING"
			}
			if kind == "DEPOSIT" {
				recurrence = "REFUNDABLE_DEPOSIT"
			}
			components = []lifecycle.Component{{Ref: "amount", Kind: kind, AmountMinor: b.Amount, Recurrence: recurrence}}
		}
		var amount int64
		for i := range components {
			components[i].PayerRef = profile.CanonicalPartyRef
			amount += components[i].AmountMinor
		}
		q := lifecycle.Quote{ID: ids.New(), Domain: "individual-owner", BusinessRef: b.Type + ":" + b.ID.String(), PayerWalletID: wallet.WalletID, PayerType: b.PayerTypes[0], PayerConsentRef: "synthetic-accepted-source-contract-consent", LegalPartyRef: profile.CanonicalPartyRef, QuoteVersion: "synthetic-owner-accepted-v1", AgreementRef: b.AgreementRef, AgreementVersion: b.AgreementVersion, PolicyID: policy.ID, Currency: b.Currency, Jurisdiction: b.Country, Schedule: "ANNUAL", AmountMinor: amount, Components: components}
		if _, e = l.LockQuote(ctx(platform.IndividualOwnerService, platform.DomainIndividualOwner, b.OwnerID), q, "workflow:"+q.ID.String()); e != nil {
			fail(w, e)
			return
		}
		_ = json.NewEncoder(w).Encode(q)
	})
	mux.HandleFunc("POST /test-fixtures/workflow-authority", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			QuoteID    ids.ID   `json:"quote_id"`
			OwnerID    ids.ID   `json:"owner_id"`
			PartyID    ids.ID   `json:"party_id"`
			Domain     string   `json:"domain"`
			Role       string   `json:"role"`
			Components []string `json:"components"`
			BPS        int      `json:"ownership_bps"`
			Mandate    string   `json:"mandate_ref"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			fail(w, e)
			return
		}
		source := ctx(platform.IndividualOwnerService, platform.DomainIndividualOwner, in.OwnerID)
		q, e := l.Quote(source, in.QuoteID)
		if e != nil {
			fail(w, e)
			return
		}
		if in.Domain == "" {
			in.Domain = "individual-owner"
		}
		if in.PartyID == ids.Nil {
			in.PartyID = in.OwnerID
		}
		if in.Role == "" {
			in.Role = "OWNER"
		}
		if in.BPS == 0 && in.Role == "OWNER" {
			in.BPS = 10000
		}
		svc, ok := platform.ServiceForDomain(platform.Domain(in.Domain))
		if !ok {
			w.WriteHeader(400)
			return
		}
		wallet, e := f.s.EnsureWallet(ctx(svc, platform.Domain(in.Domain), in.PartyID), core.Provision{Domain: in.Domain, AccountID: in.PartyID, Role: in.Role, PartyKind: "PERSON", LegalEntity: f.w.LegalEntity, Currency: q.Currency, RegistrationRef: "synthetic-verified-receiving-party"})
		if e != nil {
			fail(w, e)
			return
		}
		if e = f.s.Verification(verifier(), wallet.IdentityID, "synthetic-owner-journey-KYC", "VERIFIED", time.Now().Add(24*time.Hour)); e != nil {
			fail(w, e)
			return
		}
		profile, e := f.s.WalletProfile(system(), wallet.WalletID)
		if e != nil {
			fail(w, e)
			return
		}
		a := lifecycle.Authority{ID: ids.New(), Domain: "individual-owner", BusinessRef: q.BusinessRef, WalletID: &wallet.WalletID, LegalPartyRef: profile.CanonicalPartyRef, Role: in.Role, Currency: q.Currency, LegalEntity: f.w.LegalEntity, Components: in.Components, EvidenceRef: "synthetic-authoritative-owner-entitlement", MandateRef: in.Mandate, OwnershipBPS: in.BPS, MaxMinor: q.AmountMinor, ExpiresAt: time.Now().Add(24 * time.Hour)}
		if _, e = l.PutAuthority(source, a); e != nil {
			fail(w, e)
			return
		}
		_ = json.NewEncoder(w).Encode(a)
	})
	mux.HandleFunc("POST /test-fixtures/fund", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			IntentID ids.ID `json:"intent_id"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			fail(w, e)
			return
		}
		i, e := f.s.GetIntent(system(), in.IntentID)
		if e != nil {
			fail(w, e)
			return
		}
		f.fake.success(i)
		if _, e = f.s.VerifyIntent(system(), i.ID); e != nil {
			fail(w, e)
			return
		}
		p.mu.Lock()
		p.charge, _ = f.fake.VerifyCharge(system(), i.Reference)
		p.mu.Unlock()
		if e = l.ConfirmFunding(admin(), lifecycle.FundingInput{IntentID: i.ID, SettlementRef: "42", CashEvidenceRef: "synthetic-local-reconciled-cash", Approvers: []string{"fixture-finance-a", "fixture-finance-b"}}); e != nil {
			fail(w, e)
			return
		}
		w.WriteHeader(200)
	})
	mux.HandleFunc("POST /test-fixtures/revoke-authority", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID ids.ID `json:"authority_id"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			fail(w, e)
			return
		}
		if e := l.RevokeAuthority(admin(), in.ID, "synthetic-authority-revocation"); e != nil {
			fail(w, e)
			return
		}
		w.WriteHeader(200)
	})
	// Operator actions exercise the existing Core with test approval/identity.
	// The Owner client cannot call these paths; they exist only in this httptest mux.
	mux.HandleFunc("POST /test-fixtures/payout-approval", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			OwnerID ids.ID `json:"owner_id"`
			LineID  ids.ID `json:"line_id"`
			Amount  int64  `json:"amount_minor"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			fail(w, e)
			return
		}
		ws, e := f.s.MyWallets(ctx(platform.IndividualOwnerService, platform.DomainIndividualOwner, in.OwnerID))
		if e != nil || len(ws) == 0 {
			w.WriteHeader(500)
			return
		}
		wallet := ws[0]
		ref, e := f.s.RecordPayoutReference(verifier(), core.PayoutReferenceInput{WalletID: wallet.WalletID, BeneficiaryRef: "synthetic-owned-bank", VerificationRef: "synthetic-owner-journey-KYC", Jurisdiction: "NG", ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			fail(w, e)
			return
		}
		if e = l.BindDestination(verifier(), lifecycle.Destination{ID: ids.New(), PayoutAccountID: ref.ID, ProviderID: f.cfg.ID, RecipientRef: "RCP_fixture", EvidenceRef: "synthetic-current-bank-owner", ExpiresAt: time.Now().Add(45 * time.Minute)}); e != nil {
			fail(w, e)
			return
		}
		approval := lifecycle.WithdrawalApproval{ID: ids.New(), LineID: in.LineID, WalletID: wallet.WalletID, PayoutAccountID: ref.ID, AmountMinor: in.Amount, StepUpRef: "synthetic-step-up", RiskRef: "synthetic-risk-review", Approvers: []string{"fixture-finance-a", "fixture-finance-b"}, ExpiresAt: time.Now().Add(30 * time.Minute)}
		if e = l.ApproveWithdrawal(admin(), approval); e != nil {
			fail(w, e)
			return
		}
		_ = json.NewEncoder(w).Encode(approval)
	})
	mux.HandleFunc("POST /test-fixtures/payout-state", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			State   string `json:"state"`
			Unknown bool   `json:"unknown"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			fail(w, e)
			return
		}
		p.mu.Lock()
		p.unknown = in.Unknown
		p.transfer.State = in.State
		p.transfer.NoEffectConfirmed = in.State == "CONFIRMED_FAILED"
		p.mu.Unlock()
		w.WriteHeader(200)
	})
}
