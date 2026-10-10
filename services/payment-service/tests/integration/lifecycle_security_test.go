package integration

import (
	"bytes"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	phttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLifecycleSignedHTTPAuthorityAndSchemaParity(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	registry := serviceauth.NewRegistry()
	signers := map[platform.Service]*serviceauth.Signer{}
	for _, svc := range []platform.Service{platform.IndividualOwnerService, platform.AdminService, platform.APIGateway} {
		key := testkit.SigningKey(t, string(svc))
		if e := registry.Add(svc, key.KID, key.PublicKey()); e != nil {
			t.Fatal(e)
		}
		signer, e := serviceauth.NewSigner(serviceauth.SignerConfig{Service: svc, Key: key})
		if e != nil {
			t.Fatal(e)
		}
		signers[svc] = signer
	}
	ver, e := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.PaymentService, Registry: registry})
	if e != nil {
		t.Fatal(e)
	}
	router := phttp.NewRouter(phttp.Config{Service: f.s, Verifier: ver, Health: health.New("payment-service", "test", time.Second), Logger: f.s.Log})
	call := func(path string, svc platform.Service, body any) int {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if svc != "" {
			token, e := signers[svc].TokenFor("payment-service", serviceauth.Delegation{ActorDomain: platform.DomainIndividualOwner, ActorID: f.account})
			if e != nil {
				t.Fatal(e)
			}
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res.Code
	}
	for _, path := range []string{"/financial-policies", "/locked-quotes", "/allocations", "/release-evidence", "/transaction-funding", "/withdrawal-approvals", "/withdrawals", "/rent-agreements", "/rent-obligations", "/remedy-policies", "/remedy-evidence", "/refundable-deposits", "/financial-cases", "/financial-case-facts", "/financial-case-decisions", "/financial-case-overrides", "/refunds", "/recoverable-actions", "/customer-topups", "/wallet-spends", "/customer-topup-funding", "/reconciliation-runs", "/reconciliation-resolutions", "/intent-cancellations", "/payout-destination-versions"} {
		if call("/internal/v1"+path, "", map[string]any{}) != 401 {
			t.Fatal(path, "unauthenticated command")
		}
	}
	p := f.policy
	p.ID = ids.New()
	p.Jurisdiction = "NG-region"
	if call("/internal/v1/financial-policies", platform.IndividualOwnerService, p) != 403 {
		t.Fatal("policy authority")
	}
	if call("/internal/v1/financial-policies", platform.AdminService, p) != 200 {
		t.Fatal("signed approved policy")
	}
	a := f.allocate(t)
	b, e := os.ReadFile("../../../../contracts/internal/openapi/payment-service.yaml")
	if e != nil {
		t.Fatal(e)
	}
	var doc map[string]any
	if e = json.Unmarshal(b, &doc); e != nil {
		t.Fatal(e)
	}
	instances := map[string]any{"Policy": f.policy, "Authority": f.authority, "Quote": f.quote, "Allocation": a, "Family": lifecycle.Families()[0]}
	for name, value := range instances {
		t.Run(name, func(t *testing.T) {
			schema := map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "$ref": "#/components/schemas/" + name, "components": doc["components"]}
			raw, _ := json.Marshal(schema)
			file := filepath.Join(t.TempDir(), "schema.json")
			if e := os.WriteFile(file, raw, 0600); e != nil {
				t.Fatal(e)
			}
			body, _ := json.Marshal(value)
			testkit.ValidateJSONSchema(t, file, body)
		})
	}
	claimed, e := f.s.Outbox.Claim(system(), "lifecycle-schema", 100, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range claimed {
		b, _ := json.Marshal(row.Envelope)
		testkit.ValidateJSONSchema(t, "../../../../contracts/events/payments/"+row.Envelope.EventType+".schema.json", b)
	}
}
func TestLifecycleFundingMismatchesAndDisabledCustody(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	a := f.allocate(t)
	ev := f.evidence(t, a.Lines[0].ID)
	proof := lifecycle.FundingInput{IntentID: f.intent.ID, SettlementRef: "42", CashEvidenceRef: "cash-proof", Approvers: []string{"a", "b"}}
	f.p.settled = false
	if e := f.l.ConfirmFunding(admin(), proof); e == nil {
		t.Fatal("clearing became cash")
	}
	f.p.settled = true
	f.p.mu.Lock()
	f.p.charge.IntentID = "wrong"
	f.p.mu.Unlock()
	if e := f.l.ConfirmFunding(admin(), proof); e == nil {
		t.Fatal("wrong settlement membership")
	}
	f.p.mu.Lock()
	f.p.charge.IntentID = f.intent.ID.String()
	f.p.mu.Unlock()
	f.fund(t)
	if e := f.l.Hold(f.user, a.Lines[0].ID, lifecycle.Hold{State: "HELD", EvidenceRef: "hold"}, "hold"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.l.Release(f.user, lifecycle.Release{LineID: a.Lines[0].ID, AmountMinor: 1, PolicyVersion: f.policy.Version, EvidenceIDs: []ids.ID{ev}}, "held"); e == nil {
		t.Fatal("held spent")
	}
	var frozen string
	if e := f.db.Pool.QueryRow(system(), `SELECT sum(balance_minor)::text FROM account_balances WHERE kind='FROZEN'`).Scan(&frozen); e != nil || frozen != "10000" {
		t.Fatal(frozen, e)
	}
	if _, e := f.l.Allocation(ctx(platform.PropertyManagerService, platform.DomainPropertyManager, ids.New()), a.ID); e == nil {
		t.Fatal("unrelated share read")
	}
	live := f.cfg
	live.Mode = "live"
	if e := f.s.Register(system(), live, finance.Adapter{Payout: f.p}); e == nil {
		t.Fatal("live payout activated")
	}
}
func TestPolicyEffectiveDeactivationAndRentFixedSchedules(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	at := time.Now().Add(time.Hour)
	selector := lifecycle.PolicySelector{Key: f.policy.Key, Jurisdiction: f.policy.Jurisdiction, Currency: f.policy.Currency, ContractType: f.policy.ContractType, Product: f.policy.Product, At: at}
	if p, e := f.l.EffectivePolicy(f.user, selector); e != nil || p.ID != f.policy.ID {
		t.Fatal(p, e)
	}
	deactivate := lifecycle.PolicyDeactivation{ID: ids.New(), PolicyID: f.policy.ID, EffectiveAt: at, EvidenceRef: "approved-stop-new-quotes"}
	if e := f.l.DeactivatePolicy(admin(), deactivate); e != nil {
		t.Fatal(e)
	}
	if _, e := f.l.EffectivePolicy(f.user, selector); e == nil {
		t.Fatal("deactivated policy selected")
	}
	next := f.policy
	next.ID = ids.New()
	next.Version = "v2"
	next.EffectiveFrom = at
	if _, e := f.l.PutPolicy(admin(), next); e != nil {
		t.Fatal(e)
	}
	if p, e := f.l.EffectivePolicy(f.user, selector); e != nil || p.ID != next.ID {
		t.Fatal(p, e)
	}
	recurring := makePolicy("RESIDENTIAL_SUBSEQUENT")
	recurring.Conditions = []string{"VALID_TENANCY"}
	if _, e := f.l.PutPolicy(admin(), recurring); e != nil {
		t.Fatal(e)
	}
	for _, frequency := range []string{"ANNUAL", "SEMIANNUAL", "QUARTERLY", "MONTHLY", "CUSTOM"} {
		t.Run(frequency, func(t *testing.T) {
			start := time.Now().Add(time.Minute)
			end := start.AddDate(2, 0, 0)
			agreement := lifecycle.Agreement{ID: ids.New(), Domain: "individual-owner", Reference: "fixed-" + frequency, Version: "v1", ApprovalRef: "signed-fixed-term", BusinessRef: f.authority.BusinessRef, PayerWalletID: f.w.WalletID, PayerPartyRef: f.authority.LegalPartyRef, PayerConsentRef: "consent", TenancyType: "FIXED_TERM", Status: "ACTIVE", Schedule: frequency, Currency: "NGN", Jurisdiction: "NG", EffectiveFrom: start.Add(-time.Hour), StartsAt: start, EndsAt: &end, InitialPolicyID: f.policy.ID, RecurringPolicyID: recurring.ID, RentShares: []lifecycle.RentShare{{AuthorityID: f.authority.ID, OwnershipBPS: 10000}}, Components: []lifecycle.Component{{Ref: "rent", Kind: "RENT", Recurrence: "RECURRING", AmountMinor: 10000, PayerRef: f.authority.LegalPartyRef}}}
			if _, e := f.l.PutAgreement(f.user, agreement); e != nil {
				t.Fatal(e)
			}
			req := lifecycle.RentRequest{AgreementID: agreement.ID, PeriodRef: "installment", PeriodStart: start, PeriodEnd: start.AddDate(0, 1, 0), DueAt: start, Stream: "RENT"}
			o, e := f.l.RentObligation(f.user, req)
			if e != nil || o.Schedule != frequency {
				t.Fatal(o, e)
			}
			req.PeriodRef = "outside-term"
			req.PeriodStart = end
			req.PeriodEnd = end.AddDate(0, 1, 0)
			if _, e = f.l.RentObligation(f.user, req); e == nil {
				t.Fatal("fixed term automatically renewed")
			}
		})
	}
}
