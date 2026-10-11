//go:build paystack_sandbox

package integration

// Opt-in real-provider certification. Ordinary/race suites never enable this
// tag: an explicit run fails rather than skips when credentials are missing.
// Only hosted test checkout receives card fixtures; the service receives none.
import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

type sandboxObservation struct {
	Method             string `json:"method"`
	Operation          string `json:"operation"`
	HTTPStatus         int    `json:"http_status"`
	ProviderStatus     bool   `json:"provider_status"`
	Classification     string `json:"classification,omitempty"`
	Mode               string `json:"mode,omitempty"`
	State              string `json:"state,omitempty"`
	AccessCodeReturned bool   `json:"access_code_returned,omitempty"`
	FailureReason      string `json:"failure_reason,omitempty"`
}
type sandboxTransport struct {
	mu           sync.Mutex
	observations []sandboxObservation
}

func (s *sandboxTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "api.paystack.co" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk_test_") {
		return nil, providers.ErrMismatch
	}
	res, e := http.DefaultTransport.RoundTrip(r)
	if e != nil {
		return nil, providers.ErrUnknown
	}
	body, e := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	if e != nil {
		return nil, providers.ErrUnknown
	}
	res.Body = io.NopCloser(bytes.NewReader(body))
	var envelope struct {
		Status  bool            `json:"status"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(body, &envelope)
	var data struct {
		Domain string `json:"domain"`
		Status string `json:"status"`
		Access string `json:"access_code"`
	}
	_ = json.Unmarshal(envelope.Data, &data)
	op := r.URL.Path
	for _, prefix := range []string{"/transaction/verify/", "/customer/", "/refund/", "/transfer/verify/", "/transferrecipient/", "/dedicated_account/"} {
		if strings.HasPrefix(op, prefix) && op != "/dedicated_account/available_providers" {
			op = prefix + ":original-id"
			break
		}
	}
	classification := ""
	reason := ""
	switch {
	case strings.Contains(envelope.Message, "Dedicated NUBAN is not available"):
		classification = "DVA_ACCOUNT_INELIGIBLE"
	case strings.Contains(strings.ToLower(envelope.Message), "insufficient"):
		classification = "INSUFFICIENT_TEST_BALANCE"
	case strings.Contains(strings.ToLower(envelope.Message), "minimum"):
		classification = "PROVIDER_MINIMUM_AMOUNT"
	case strings.Contains(strings.ToLower(envelope.Message), "third party") || strings.Contains(strings.ToLower(envelope.Message), "starter business"):
		classification = "PAYOUT_ACCOUNT_INELIGIBLE"
	case strings.Contains(envelope.Message, "Invalid key"):
		classification = "INVALID_CREDENTIAL"
	case res.StatusCode >= 400:
		classification = "PROVIDER_DECLINED_REQUEST"
	}
	// Only generic validation/account-control text is retained. Response data,
	// credentials, headers, card/bank details and customer data are excluded.
	if res.StatusCode >= 400 {
		lower := strings.ToLower(envelope.Message)
		if strings.Contains(lower, "amount") || strings.Contains(lower, "balance") || strings.Contains(lower, "business") || strings.Contains(lower, "transfer") {
			reason = envelope.Message
			for _, value := range []string{os.Getenv("PAYSTACK_SECRET_KEY"), os.Getenv("PAYSTACK_PUBLIC_KEY")} {
				if value != "" {
					reason = strings.ReplaceAll(reason, value, "[REDACTED]")
				}
			}
		}
	}
	s.mu.Lock()
	s.observations = append(s.observations, sandboxObservation{r.Method, op, res.StatusCode, envelope.Status, classification, data.Domain, data.Status, data.Access != "", reason})
	s.mu.Unlock()
	return res, nil
}
func (s *sandboxTransport) count(method, op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, v := range s.observations {
		if v.Method == method && v.Operation == op {
			n++
		}
	}
	return n
}

type sandboxCollection struct {
	*providers.Paystack
	fault bool
	last  providers.Initialized
}

func (s *sandboxCollection) InitializeCharge(ctx context.Context, in providers.ChargeRequest) (providers.Initialized, error) {
	v, e := s.Paystack.InitializeCharge(ctx, in)
	s.last = v
	if s.fault && e == nil {
		s.fault = false
		return providers.Initialized{}, providers.ErrUnknown
	}
	return v, e
}

func TestRealPaystackSandbox(t *testing.T) {
	if os.Getenv("CF_RUN_PAYSTACK_SANDBOX") != "1" || os.Getenv("CF_REQUIRE_INTEGRATION") != "1" {
		t.Fatal("explicit real sandbox and required infrastructure flags must be set")
	}
	root := os.Getenv("CF_SANDBOX_ROOT")
	if root == "" {
		t.Fatal("sandbox repository root required")
	}
	secret := security.NewSecret(os.Getenv("PAYSTACK_SECRET_KEY"))
	if !strings.HasPrefix(secret.Reveal(), "sk_test_") {
		t.Fatal("test secret required; value suppressed")
	}
	var bootstrap struct {
		Merchant string `json:"merchant_ref"`
		Customer string `json:"customer_code"`
		Domain   string `json:"customer_domain"`
	}
	b, e := os.ReadFile(filepath.Join(root, "tmp/payments/paystack-bootstrap.json"))
	if e != nil || json.Unmarshal(b, &bootstrap) != nil || bootstrap.Domain != "test" {
		t.Fatal("authoritative test customer bootstrap missing")
	}
	merchant, e := strconv.ParseInt(bootstrap.Merchant, 10, 64)
	if e != nil || merchant <= 0 {
		t.Fatal("authoritative merchant identifier missing")
	}
	transport := &sandboxTransport{}
	p, e := providers.NewPaystack("https://api.paystack.co", secret, merchant, "test", &http.Client{Transport: transport, Timeout: 25 * time.Second})
	if e != nil {
		t.Fatal("adapter setup failed")
	}
	status := map[string]any{"mode": "test", "public_webhook_delivery": "NOT RUN", "dva_assignment": "NOT RUN", "dva_funding": "NOT RUN", "custody": "NOT APPROVED", "autopay": "DESIGN RESERVED / EXECUTION DEFERRED", "refund_reversal": "NOT RUN", "payout_reversal": "NOT RUN"}
	write := func(name string, v any) {
		t.Helper()
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal("evidence encoding failed")
		}
		if bytes.Contains(data, []byte(secret.Reveal())) {
			t.Fatal("secret detected; evidence suppressed")
		}
		if err = os.WriteFile(filepath.Join(root, "tmp/payments/"+name), append(data, '\n'), 0600); err != nil {
			t.Fatal("evidence write failed")
		}
	}
	t.Cleanup(func() {
		transport.mu.Lock()
		defer transport.mu.Unlock()
		status["observations"] = transport.observations
		write("paystack-real-evidence.json", status)
	})
	balances, e := p.FetchBalance(system())
	if e != nil || len(balances) == 0 {
		t.Fatal("real test authentication/balance failed; details suppressed")
	}
	status["authentication"] = "CERTIFIED"
	status["balance"] = "CERTIFIED"
	invalid, e := providers.NewPaystack("https://api.paystack.co", security.NewSecret("sk_test_intentionally_invalid_sandbox_credential"), merchant, "test", &http.Client{Transport: transport, Timeout: 25 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = invalid.FetchBalance(system()); e == nil {
		t.Fatal("invalid credential accepted")
	}
	status["invalid_credential_rejected"] = true
	cust, e := p.FetchCustomer(system(), bootstrap.Customer)
	if e != nil || cust.MerchantID != merchant || cust.Mode != "test" {
		t.Fatal("original merchant/customer context mismatch")
	}
	status["merchant_binding"] = "CERTIFIED"
	if banks, err := p.AvailableDVAProviders(system()); err != nil {
		status["dva_discovery"] = "EXTERNAL/ACCOUNT BLOCKED"
		status["dva_reason"] = "DVA_ACCOUNT_INELIGIBLE"
	} else {
		status["dva_discovery"] = "CERTIFIED"
		status["dva_providers"] = banks
		status["dva_assignment"] = "NOT RUN: current customer identification and approved consent required"
	}
	f := setup(t)
	l := lifecycle.New(f.s)
	wrapper := &sandboxCollection{Paystack: p}
	c := f.cfg
	c.ID = ids.New()
	c.PoolID = ids.New()
	c.Name = "paystack"
	c.MerchantRef = bootstrap.Merchant
	c.CredentialsRef = "PAYSTACK_SECRET_KEY"
	c.Capabilities = []string{"COLLECTION", "SETTLEMENT"}
	c.DVAApproved = false
	c.CustodyApproved = false
	c.ManualPayoutsApproved = false
	c.PayoutApproved = false
	c.PayoutApprovalRef = ""
	c.RefundApproved = true
	c.RefundApprovalRef = "user-authorized-test-certification-only"
	c.ApprovalRef = c.RefundApprovalRef
	c.Categories = []string{"RESIDENTIAL_INITIAL"}
	c.Version = "sandbox-certification-only-v1"
	if e = f.s.Register(system(), c, finance.Adapter{Collection: wrapper, Settlement: p, Payout: p, Refund: p, Reversal: p}); e != nil {
		t.Fatal(e)
	}
	write("paystack-sandbox-provider.json", c)
	if e = f.s.Verification(verifier(), f.w.IdentityID, "synthetic-sandbox-party-only", "VERIFIED", time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	policy := makePolicy("RESIDENTIAL_INITIAL")
	policy.Version = c.Version
	policy.ApprovalRef = c.ApprovalRef
	if _, e = l.PutPolicy(admin(), policy); e != nil {
		t.Fatal(e)
	}
	rp := remedyPack(policy.ID)
	rp.RefundSeconds = 3600
	rp.ClaimSeconds = 3600
	rp.ReviewSeconds = 3600
	rp.ChallengeSeconds = 3600
	rp.ApprovalRef = c.ApprovalRef
	if e = l.PutRemedyPolicy(admin(), rp); e != nil {
		t.Fatal(e)
	}
	auth := lifecycle.Authority{ID: ids.New(), Domain: "individual-owner", BusinessRef: "sandbox:synthetic-obligation", WalletID: &f.w.WalletID, LegalPartyRef: "individual-owner:" + f.account.String(), Role: "OWNER", Currency: "NGN", LegalEntity: f.w.LegalEntity, Components: []string{"RENT"}, EvidenceRef: "synthetic-sandbox-party-only", OwnershipBPS: 10000, MaxMinor: 1000000, ExpiresAt: time.Now().Add(time.Hour)}
	if _, e = l.PutAuthority(f.user, auth); e != nil {
		t.Fatal(e)
	}
	type checkout struct {
		Scenario string         `json:"scenario"`
		URL      string         `json:"url"`
		Intent   finance.Intent `json:"intent"`
	}
	var checkouts []checkout
	for n, scenario := range []string{"partial_refund", "full_refund", "failed_refund", "needs_attention_refund"} {
		q := lifecycle.Quote{ID: ids.New(), Domain: "individual-owner", BusinessRef: auth.BusinessRef, PayerWalletID: f.w.WalletID, PayerType: "TENANT", PayerConsentRef: "explicit-test-certification-only", LegalPartyRef: auth.LegalPartyRef, QuoteVersion: "sandbox-accepted-v1", AgreementRef: "synthetic-test-obligation", AgreementVersion: "v1", PolicyID: policy.ID, Currency: "NGN", Jurisdiction: "NG", Schedule: "ANNUAL", AmountMinor: 100000, Components: []lifecycle.Component{{Ref: "rent", Kind: "RENT", AmountMinor: 100000, PayerRef: auth.LegalPartyRef, Recurrence: "ONE_TIME"}}}
		if _, e = l.LockQuote(f.user, q, scenario); e != nil {
			t.Fatal(e)
		}
		in := finance.IntentInput{LockedQuoteID: &q.ID, WalletID: f.w.WalletID, AmountMinor: q.AmountMinor, Currency: "NGN", Country: "NG", Category: policy.Key, EvidenceRef: q.QuoteVersion, PolicyVersion: policy.Version, Email: "cosmicforge-certification-20261011@example.com", ProviderConstraint: &c.ID}
		wrapper.fault = n == 0
		i, err := f.s.CreateIntent(f.user, in, scenario)
		if err != nil {
			t.Fatal(err)
		}
		expected := "INITIALIZED"
		if n == 0 {
			expected = "UNKNOWN_OUTCOME"
		}
		if i.State != expected || wrapper.last.CheckoutURL == "" {
			t.Fatal("real initialization did not retain expected state/checkout")
		}
		if again, err := f.s.CreateIntent(f.user, in, scenario); err != nil || again.ID != i.ID {
			t.Fatal("intent retry lost canonical identity")
		}
		checkouts = append(checkouts, checkout{scenario, wrapper.last.CheckoutURL, i})
	}
	if transport.count("POST", "/transaction/initialize") != 4 {
		t.Fatal("duplicate external initialization")
	}
	var posted int
	if e = f.db.Pool.QueryRow(system(), `SELECT count(*) FROM journals`).Scan(&posted); e != nil || posted != 0 {
		t.Fatal("initialization credited money")
	}
	status["initialization"] = "CERTIFIED"
	status["initialization_no_credit"] = true
	status["initialization_idempotency"] = true
	write("paystack-sandbox-checkouts.json", checkouts)
	t.Log("Hosted TEST checkouts ready in ignored tmp/payments/paystack-sandbox-checkouts.json; no credentials or access codes emitted")
	// The host controls the checkout UI. Readback, never the redirect/control
	// file, establishes success. Eight minutes is a finite certification window.
	deadline := time.Now().Add(8 * time.Minute)
	var results []map[string]any
	for _, item := range checkouts {
		var charge providers.Charge
		for time.Now().Before(deadline) {
			charge, e = p.VerifyCharge(system(), item.Intent.Reference)
			if e == nil && charge.State == "VERIFIED_SUCCESS" {
				break
			}
			time.Sleep(3 * time.Second)
		}
		if e != nil || charge.State != "VERIFIED_SUCCESS" {
			status["checkout"] = "NOT RUN: hosted test checkout did not complete in certification window"
			t.Fatal("hosted checkout not complete; original attempts retained, no resubmission")
		}
		if charge.Reference != item.Intent.Reference || charge.IntentID != item.Intent.ID.String() || charge.AmountMinor != 100000 || charge.Currency != "NGN" || charge.Mode != "test" || charge.MerchantID != merchant {
			t.Fatal("original real charge context mismatch")
		}
		for n := 0; n < 2; n++ {
			v, err := f.s.VerifyIntent(f.user, item.Intent.ID)
			if err != nil || v.State != "VERIFIED_SUCCESS" {
				t.Fatal("canonical charge completion failed")
			}
		}
		result := map[string]any{"scenario": item.Scenario, "checkout": "CERTIFIED", "readback": "CERTIFIED", "intent_id": item.Intent.ID, "reference": item.Intent.Reference, "amount_minor": 100000, "currency": "NGN", "fee_minor": charge.FeeMinor}
		a, err := l.Allocate(f.user, lifecycle.AllocationInput{IntentID: item.Intent.ID, Version: 1, Lines: []lifecycle.LineInput{{AuthorityID: auth.ID, ComponentRef: "rent", Kind: "BENEFICIARY", AmountMinor: 100000, Currency: "NGN"}}})
		if err != nil {
			t.Fatal(err)
		}
		lf := &lifeFixture{fixture: f, l: l, policy: policy, authority: auth, quote: lifecycle.Quote{BusinessRef: auth.BusinessRef}, intent: item.Intent}
		ev := ids.New()
		if err = l.PutRemedyEvidence(f.user, lifecycle.RemedyEvidence{ID: ev, BusinessRef: auth.BusinessRef, Reference: "synthetic-test-return", Version: "v1", ActorRef: "sandbox-review", HashRef: "sha256:test-policy-only", ObservedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		claim := lf.openCase(t, a.Lines[0].ID, 100000, ev, item.Scenario)
		lf.decide(t, claim, ev)
		amount := int64(100000)
		if item.Scenario == "partial_refund" {
			amount = 40000
		}
		rq := lifecycle.RefundRequest{LineID: a.Lines[0].ID, CaseID: claim.ID, AmountMinor: amount, Strategy: "ORIGINAL_INSTRUMENT", AuthorityRef: "explicit-sandbox-refund-only", EvidenceIDs: []ids.ID{ev}, Approvers: []string{"sandbox-a", "sandbox-b"}}
		refund, err := l.RequestRefund(admin(), rq, item.Scenario)
		if err != nil {
			t.Fatal(err)
		}
		if again, err := l.RequestRefund(admin(), rq, item.Scenario); err != nil || again.ID != refund.ID {
			t.Fatal("duplicate refund reservation")
		}
		over := rq
		over.AmountMinor = 100001
		if _, err = l.RequestRefund(admin(), over, item.Scenario+"-over"); err == nil {
			t.Fatal("over-refund accepted")
		}
		before := transport.count("POST", "/refund")
		refund, err = l.SubmitRefund(admin(), refund.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = l.SubmitRefund(admin(), refund.ID); err != nil {
			t.Fatal(err)
		}
		if transport.count("POST", "/refund") != before+1 {
			t.Fatal("duplicate external refund")
		}
		for n := 0; n < 8; n++ {
			refund, err = l.ResolveRefund(system(), refund.ID)
			if err != nil {
				t.Fatal(err)
			}
			if refund.State == "CONFIRMED_REFUNDED" || refund.State == "REVIEW_REQUIRED" {
				break
			}
			time.Sleep(2 * time.Second)
		}
		result["refund_amount_minor"] = amount
		result["refund_state"] = refund.State
		result["refund_id"] = refund.ID
		result["duplicate_prevention"] = true
		result["over_refund_rejected"] = true
		if refund.State == "CONFIRMED_REFUNDED" {
			result["refund"] = "CERTIFIED"
		} else {
			result["refund"] = "NOT RUN: terminal effect not authoritatively confirmed"
			var held int64
			if err = f.db.Pool.QueryRow(system(), `SELECT b.balance_minor FROM refund_attempts r JOIN account_balances b ON b.account_id=r.pending_account_id WHERE refund_id=$1`, refund.ID).Scan(&held); err != nil || held != amount {
				t.Fatal("ambiguous refund released reservation")
			}
			result["reservation_retained"] = true
			wallet := rq
			wallet.Strategy = "WALLET_CREDIT"
			if _, err = l.RequestRefund(admin(), wallet, item.Scenario+"-wallet"); err == nil {
				t.Fatal("wallet replacement while external refund unresolved")
			}
		}
		results = append(results, result)
		status["transactions"] = results
		write("paystack-real-evidence.json", status)
	}
	status["checkout"] = "CERTIFIED"
	status["verify_readback"] = "CERTIFIED"
	status["controlled_lost_initialization_response"] = "CERTIFIED: injected locally after real successful POST, original reference read back; Paystack timeout not claimed"
	// Locally signed minimal callback using the real test key. This proves the
	// crypto/inbox contract, not public delivery from Paystack.
	body, _ := json.Marshal(map[string]any{"event": "charge.success", "data": map[string]string{"reference": checkouts[0].Intent.Reference}})
	mac := hmac.New(sha512.New, []byte(secret.Reveal()))
	mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))
	gateway := ctx(platform.APIGateway, "", ids.Nil)
	if e = f.s.IngestWebhook(gateway, c.ID, body, "invalid"); e == nil {
		t.Fatal("invalid signature accepted")
	}
	for n := 0; n < 2; n++ {
		if e = f.s.IngestWebhook(gateway, c.ID, body, signature); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = f.s.ProcessInbox(system()); e != nil {
		t.Fatal(e)
	}
	var callbacks int
	if e = f.db.Pool.QueryRow(system(), `SELECT count(*) FROM provider_inbox`).Scan(&callbacks); e != nil || callbacks != 1 {
		t.Fatal("duplicate callback retained twice")
	}
	status["webhook_crypto"] = "LOCAL CONTRACT PASS"
	bank, e := p.VerifyBank(system(), providers.Bank{BankCode: "057", AccountNumber: "0000000000"})
	if e != nil {
		status["payout"] = "EXTERNAL/ACCOUNT BLOCKED: test bank resolution unavailable"
	} else {
		recipient, err := p.CreateRecipient(system(), bank)
		if err != nil {
			status["payout"] = "FAILED: recipient contract mismatch or provider rejection"
		} else {
			status["recipient"] = "CERTIFIED"
			transferRef := "cf-" + ids.New().String()
			transfer, err := p.InitiateTransfer(system(), providers.Transfer{Reference: transferRef, RecipientRef: recipient, AmountMinor: 100000, Currency: "NGN"})
			if err != nil {
				status["payout"] = "EXTERNAL/ACCOUNT BLOCKED: provider rejected test transfer; see observations"
			} else {
				status["transfer_submit"] = "CERTIFIED"
				for n := 0; n < 6; n++ {
					transfer, err = p.VerifyTransfer(system(), transferRef)
					if err == nil && transfer.State == "CONFIRMED_PAID" {
						break
					}
					time.Sleep(2 * time.Second)
				}
				status["transfer_state"] = transfer.State
				if err == nil && transfer.State == "CONFIRMED_PAID" {
					status["payout"] = "CERTIFIED: adapter test transfer; lawful custody/ledger withdrawal not certified"
				} else {
					status["payout"] = "NOT RUN: terminal payout not confirmed; original reference retained"
				}
			}
		}
	}
	if e = f.s.ReconcileBalance(admin(), c.ID); e != nil {
		t.Fatal("real balance reconciliation failed")
	}
	run, e := l.Reconcile(admin(), lifecycle.ReconciliationRequest{ID: ids.New(), ProviderID: c.ID, AuthorityRef: "real-sandbox-observations-only", StaleAfterSeconds: 1})
	if e != nil {
		t.Fatal("real original-reference reconciliation failed")
	}
	status["reconciliation"] = "CERTIFIED: original collection/refund readbacks and test balance, not settled custody"
	status["reconciliation_findings"] = run.Findings
	var total int
	if e = f.db.Pool.QueryRow(system(), `SELECT count(*) FROM journals WHERE operation_ref LIKE 'charge:%'`).Scan(&total); e != nil || total != 4 {
		t.Fatal("one charge produced multiple collection journals")
	}
	status["once_only_collection_journals"] = true
	validateLifecycleOutbox(t, &lifeFixture{fixture: f, l: l})
}

// This is intentionally read-only: it can follow pending provider outcomes
// without creating another charge, refund, recipient or transfer.
func TestRealPaystackSandboxReadbackFollowup(t *testing.T) {
	if os.Getenv("CF_RUN_PAYSTACK_SANDBOX") != "1" {
		t.Fatal("explicit real sandbox flag required")
	}
	root := os.Getenv("CF_SANDBOX_ROOT")
	secret := security.NewSecret(os.Getenv("PAYSTACK_SECRET_KEY"))
	if root == "" || !strings.HasPrefix(secret.Reveal(), "sk_test_") {
		t.Fatal("test sandbox configuration required; values suppressed")
	}
	var bootstrap struct {
		Merchant string `json:"merchant_ref"`
		Domain   string `json:"customer_domain"`
	}
	b, e := os.ReadFile(filepath.Join(root, "tmp/payments/paystack-bootstrap.json"))
	if e != nil || json.Unmarshal(b, &bootstrap) != nil || bootstrap.Domain != "test" {
		t.Fatal("authoritative bootstrap required")
	}
	merchant, e := strconv.ParseInt(bootstrap.Merchant, 10, 64)
	if e != nil {
		t.Fatal("merchant context required")
	}
	audit := &sandboxTransport{}
	p, e := providers.NewPaystack("https://api.paystack.co", secret, merchant, "test", &http.Client{Transport: audit, Timeout: 25 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	var prior struct {
		Transactions []struct {
			Scenario     string `json:"scenario"`
			Reference    string `json:"reference"`
			IntentID     string `json:"intent_id"`
			RefundID     string `json:"refund_id"`
			Amount       int64  `json:"amount_minor"`
			RefundAmount int64  `json:"refund_amount_minor"`
			Currency     string `json:"currency"`
		} `json:"transactions"`
	}
	b, e = os.ReadFile(filepath.Join(root, "tmp/payments/paystack-real-evidence.json"))
	if e != nil || json.Unmarshal(b, &prior) != nil || len(prior.Transactions) != 4 {
		t.Fatal("original completed certification evidence required")
	}
	out := map[string]any{"mutation_calls": 0, "public_webhook_delivery": "NOT RUN", "settled_cash_attestation": "NOT RUN"}
	var rows []map[string]any
	for _, v := range prior.Transactions {
		charge, e := p.VerifyCharge(system(), v.Reference)
		if e != nil || charge.Reference != v.Reference || charge.IntentID != v.IntentID || charge.AmountMinor != v.Amount || charge.Currency != v.Currency || charge.MerchantID != merchant || charge.Mode != "test" {
			t.Fatal("original real collection context changed")
		}
		request := providers.Refund{Reference: "cfr-" + v.RefundID, ChargeReference: v.Reference, AmountMinor: v.RefundAmount, Currency: v.Currency}
		refund, e := p.VerifyRefund(system(), request)
		if e != nil || refund.Reference != request.Reference || refund.ChargeReference != request.ChargeReference || refund.AmountMinor != request.AmountMinor || refund.Currency != request.Currency || refund.MerchantID != merchant || refund.Mode != "test" || refund.ID == "" {
			t.Fatal("original real refund context not confirmed")
		}
		request.ID = refund.ID
		again, e := p.VerifyRefund(system(), request)
		if e != nil || again.ID != refund.ID {
			t.Fatal("refund ID original readback mismatch")
		}
		rows = append(rows, map[string]any{"scenario": v.Scenario, "original_reference": v.Reference, "provider_refund_id": refund.ID, "amount_minor": refund.AmountMinor, "currency": refund.Currency, "refund_state": again.State, "readback": "CERTIFIED", "terminal_confirmed": again.State == "CONFIRMED_REFUNDED"})
	}
	balances, e := p.FetchBalance(system())
	if e != nil {
		t.Fatal("real balance follow-up failed")
	}
	out["merchant_balances_not_custody"] = balances
	transactions, e := p.ListTransactions(system(), "1")
	if e != nil {
		t.Fatal("real transaction listing contract failed")
	}
	out["transaction_list_count"] = len(transactions)
	out["refunds"] = rows
	for _, v := range audit.observations {
		if v.Method != "GET" {
			t.Fatal("readback follow-up mutated provider")
		}
	}
	out["observations"] = audit.observations
	b, e = json.MarshalIndent(out, "", "  ")
	if e != nil || bytes.Contains(b, []byte(secret.Reveal())) {
		t.Fatal("unsafe evidence suppressed")
	}
	if e = os.WriteFile(filepath.Join(root, "tmp/payments/paystack-readback-followup.json"), append(b, '\n'), 0600); e != nil {
		t.Fatal("evidence write failed")
	}
}

func TestRealPaystackCheckoutReceiptReconciliation(t *testing.T) {
	if os.Getenv("CF_RUN_PAYSTACK_SANDBOX") != "1" || os.Getenv("CF_REQUIRE_INTEGRATION") != "1" {
		t.Fatal("explicit sandbox/infrastructure flags required")
	}
	root := os.Getenv("CF_SANDBOX_ROOT")
	secret := security.NewSecret(os.Getenv("PAYSTACK_SECRET_KEY"))
	if root == "" || !strings.HasPrefix(secret.Reveal(), "sk_test_") {
		t.Fatal("test-only sandbox configuration required")
	}
	var bootstrap struct {
		Merchant string `json:"merchant_ref"`
		Domain   string `json:"customer_domain"`
	}
	b, e := os.ReadFile(filepath.Join(root, "tmp/payments/paystack-bootstrap.json"))
	if e != nil || json.Unmarshal(b, &bootstrap) != nil || bootstrap.Domain != "test" {
		t.Fatal("authoritative bootstrap required")
	}
	merchant, e := strconv.ParseInt(bootstrap.Merchant, 10, 64)
	if e != nil {
		t.Fatal("merchant identifier required")
	}
	audit := &sandboxTransport{}
	p, e := providers.NewPaystack("https://api.paystack.co", secret, merchant, "test", &http.Client{Transport: audit, Timeout: 25 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	f := setup(t)
	l := lifecycle.New(f.s)
	c := f.cfg
	c.ID = ids.New()
	c.PoolID = ids.New()
	c.Name = "paystack"
	c.MerchantRef = bootstrap.Merchant
	c.CredentialsRef = "PAYSTACK_SECRET_KEY"
	c.ApprovalRef = "user-authorized-test-certification-only"
	c.DVAApproved = false
	c.CustodyApproved = false
	c.Capabilities = []string{"COLLECTION", "SETTLEMENT"}
	if e = f.s.Register(system(), c, finance.Adapter{Collection: p, Settlement: p}); e != nil {
		t.Fatal(e)
	}
	in := finance.IntentInput{WalletID: f.w.WalletID, AmountMinor: 100000, Currency: "NGN", Country: "NG", Category: "RENT", EvidenceRef: "synthetic-test-obligation-accepted", PolicyVersion: c.Version, Email: "cosmicforge-certification-20261011@example.com", ProviderConstraint: &c.ID}
	i, e := f.s.CreateIntent(f.user, in, "real-receipt-regression")
	if e != nil || i.State != "INITIALIZED" || i.CheckoutURL == nil {
		t.Fatal("original real initialization failed")
	}
	b, _ = json.Marshal(i)
	if e = os.WriteFile(filepath.Join(root, "tmp/payments/paystack-receipt-checkout.json"), b, 0600); e != nil {
		t.Fatal("checkout file write failed")
	}
	t.Log("TEST receipt regression checkout ready in ignored tmp/payments/paystack-receipt-checkout.json")
	deadline := time.Now().Add(8 * time.Minute)
	var observed providers.Charge
	for time.Now().Before(deadline) {
		observed, e = p.VerifyCharge(system(), i.Reference)
		if e == nil && observed.State == "VERIFIED_SUCCESS" {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if e != nil || observed.State != "VERIFIED_SUCCESS" {
		t.Fatal("TEST receipt checkout did not complete; no replacement submission")
	}
	for n := 0; n < 3; n++ {
		completed, e := f.s.VerifyIntent(f.user, i.ID)
		if e != nil || completed.State != "VERIFIED_SUCCESS" {
			t.Fatal("original canonical confirmation failed")
		}
	}
	var receipts int
	var exact bool
	if e = f.db.Pool.QueryRow(system(), `SELECT count(*),bool_and(r.journal_id=i.credited_journal_id AND r.wallet_id=i.wallet_id AND r.currency=i.currency AND r.amount_minor=i.amount_minor) FROM collection_receipts r JOIN payment_intents i ON i.provider_account_id=r.provider_account_id AND i.provider_reference=r.external_ref WHERE i.intent_id=$1`, i.ID).Scan(&receipts, &exact); e != nil || receipts != 1 || !exact {
		t.Fatal("real receipt/journal canonical invariant failed")
	}
	run, e := l.Reconcile(admin(), lifecycle.ReconciliationRequest{ID: ids.New(), ProviderID: c.ID, AuthorityRef: "real-original-receipt-regression", StaleAfterSeconds: 30})
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range run.Findings {
		if v.Kind == "MISSING_COLLECTION" || v.Kind == "COLLECTION_CONTEXT_MISMATCH" || v.Kind == "MISSING_PROVIDER_COLLECTION" || v.Kind == "PROVIDER_REFERENCE_MISMATCH" {
			t.Fatal("real reconciliation lost original collection context")
		}
	}
	var available int64
	if e = f.db.Pool.QueryRow(system(), `SELECT COALESCE(sum(balance_minor),0)::bigint FROM account_balances WHERE pool_id=$1 AND kind='AVAILABLE'`, c.PoolID).Scan(&available); e != nil || available != 0 {
		t.Fatal("test balance/checkout fabricated customer availability")
	}
	out := map[string]any{"receipt_atomic_confirmation": "CERTIFIED", "reconciliation_collection_context": "CERTIFIED", "provider": "paystack", "mode": "test", "intent_id": i.ID, "reference": i.Reference, "amount_minor": i.AmountMinor, "currency": i.Currency, "duplicate_receipt_count": receipts, "available_minor": available, "custody_approved": false, "reconciliation_findings": run.Findings, "observations": audit.observations}
	b, _ = json.MarshalIndent(out, "", "  ")
	if bytes.Contains(b, []byte(secret.Reveal())) {
		t.Fatal("unsafe evidence suppressed")
	}
	if e = os.WriteFile(filepath.Join(root, "tmp/payments/paystack-receipt-regression.json"), append(b, '\n'), 0600); e != nil {
		t.Fatal("evidence write failed")
	}
	validateLifecycleOutbox(t, &lifeFixture{fixture: f, l: l})
}
