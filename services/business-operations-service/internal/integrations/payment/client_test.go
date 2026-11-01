package payment

import (
	"encoding/json"
	"errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// This verifies the signed HTTP contract, not a live payment provider. Exact
// paths/fields are the existing owning Payment API, including coherent receipts.
func TestExistingPaymentHTTPContractRequiresIdentityAndPropagatesContext(t *testing.T) {
	if _, e := New("http://127.0.0.1", nil, time.Second); e == nil {
		t.Fatal("unauthenticated payment client constructed")
	}
	key := testkit.SigningKey(t, "disposable-business-payment-client")
	registry := serviceauth.NewRegistry()
	if e := registry.Add(platform.BusinessOperationsService, key.KID, key.PublicKey()); e != nil {
		t.Fatal(e)
	}
	signer, e := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.BusinessOperationsService, Key: key})
	if e != nil {
		t.Fatal(e)
	}
	verifier, e := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.PaymentService, Registry: registry})
	if e != nil {
		t.Fatal(e)
	}
	org, actor, policyID := ids.New(), ids.New(), ids.New()
	var mu sync.Mutex
	var locked Quote
	var intent Intent
	keys := []string{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		p, ok := serviceauth.PrincipalFrom(r.Context())
		if !ok || p.Service != platform.BusinessOperationsService || p.Delegation.OrganizationID != org || p.Delegation.ActorID != actor || p.Delegation.ActorDomain != platform.DomainBusinessOperations || p.Delegation.OriginalRequestID != "business-payment-contract" {
			t.Errorf("signed context was not preserved: %+v", p)
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		respond := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
		switch {
		case r.Method == "GET" && r.URL.Path == "/internal/v1/financial-policies/"+policyID.String():
			respond(Policy{ID: policyID, Key: "HOTEL", Version: "v1", Currency: "NGN", Jurisdiction: "NG", Active: true, EffectiveFrom: time.Now().Add(-time.Hour), Schedules: []string{"ONE_TIME"}})
		case r.Method == "POST" && r.URL.Path == "/internal/v1/locked-quotes":
			if json.NewDecoder(r.Body).Decode(&locked) != nil || locked.Domain != string(platform.DomainBusinessOperations) || locked.PolicyID != policyID || locked.AmountMinor != 20000 {
				t.Error("locked quote request did not match existing owning API")
				w.WriteHeader(400)
				return
			}
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			respond(locked)
		case r.Method == "POST" && r.URL.Path == "/internal/v1/payment-intents":
			var in IntentInput
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.LockedQuoteID == nil || *in.LockedQuoteID != locked.ID || in.EvidenceRef != locked.QuoteVersion || in.PolicyVersion != "v1" || in.Category != "HOTEL" || in.Email != "disposable@example.invalid" {
				t.Error("collection lost its locked-quote context")
				w.WriteHeader(400)
				return
			}
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			intent = Intent{ID: ids.New(), WalletID: in.WalletID, AmountMinor: in.AmountMinor, Currency: in.Currency, State: "INITIALIZED", SettlementState: "PENDING", Reference: "disposable-original-intent"}
			respond(intent)
		case r.Method == "GET" && r.URL.Path == "/internal/v1/locked-quotes/"+locked.ID.String()+"/receipt":
			receipt := Receipt{Quote: locked, IntentID: &intent.ID, CollectionState: intent.State, SettlementState: intent.SettlementState}
			receipt.PayerContext.WalletID = locked.PayerWalletID
			receipt.PayerContext.CanonicalPartyRef = locked.LegalPartyRef
			respond(receipt)
		case r.Method == "GET" && r.URL.Path == "/internal/v1/payment-intents/"+intent.ID.String():
			respond(intent)
		default:
			t.Errorf("unsupported Payment contract route %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	protected := serviceauth.Authenticate(verifier, serviceauth.HeaderAuthorization)(serviceauth.RequireCaller(platform.BusinessOperationsService)(handler))
	server := httptest.NewServer(protected)
	defer server.Close()
	client, e := New(server.URL, signer, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	ctx := requestcontext.WithOrganization(t.Context(), org)
	ctx = requestcontext.WithActor(ctx, requestcontext.Actor{Domain: string(platform.DomainBusinessOperations), ID: actor})
	ctx = requestcontext.WithRequestID(ctx, "business-payment-contract")
	policy, e := client.Policy(ctx, policyID)
	if e != nil || policy.ID != policyID {
		t.Fatalf("policy contract %+v %v", policy, e)
	}
	quote := Quote{ID: ids.New(), Domain: string(platform.DomainBusinessOperations), BusinessRef: ids.New().String(), PayerWalletID: ids.New(), PayerType: "GUEST", PayerConsentRef: "explicit-disposable-consent", LegalPartyRef: "disposable-canonical-guest-party", QuoteVersion: "accepted-v1", AgreementRef: ids.New().String(), AgreementVersion: "1", PolicyID: policy.ID, Currency: "NGN", Jurisdiction: "NG", Schedule: "ONE_TIME", AmountMinor: 20000, Components: []Component{{Ref: "original-room-obligation", Kind: "ROOM", AmountMinor: 20000, PayerRef: "disposable-canonical-guest-party", Recurrence: "ONE_TIME"}}}
	actual, e := client.Lock(ctx, quote, "original-quote-key")
	if e != nil || actual.ID != quote.ID {
		t.Fatalf("locked quote %+v %v", actual, e)
	}
	collection, e := client.Collect(ctx, IntentInput{LockedQuoteID: &quote.ID, WalletID: quote.PayerWalletID, AmountMinor: quote.AmountMinor, Currency: quote.Currency, Country: quote.Jurisdiction, Category: policy.Key, EvidenceRef: quote.QuoteVersion, PolicyVersion: policy.Version, Email: "disposable@example.invalid"}, "original-intent-key")
	if e != nil || collection.State != "INITIALIZED" {
		t.Fatalf("collection contract %+v %v", collection, e)
	}
	receipt, e := client.Receipt(ctx, quote.ID)
	if e != nil || receipt.IntentID == nil || *receipt.IntentID != collection.ID || receipt.SettlementState != "PENDING" {
		t.Fatalf("coherent receipt %+v %v", receipt, e)
	}
	read, e := client.Intent(ctx, collection.ID)
	if e != nil || read.ID != collection.ID {
		t.Fatalf("intent readback %+v %v", read, e)
	}
	mu.Lock()
	if len(keys) != 2 || keys[0] != "original-quote-key" || keys[1] != "original-intent-key" {
		t.Fatalf("idempotency propagation %v", keys)
	}
	mu.Unlock()
	badKey := testkit.SigningKey(t, "disposable-wrong-payment-caller")
	if e = registry.Add(platform.APIGateway, badKey.KID, badKey.PublicKey()); e != nil {
		t.Fatal(e)
	}
	badSigner, e := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.APIGateway, Key: badKey})
	if e != nil {
		t.Fatal(e)
	}
	badClient, e := New(server.URL, badSigner, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	_, e = badClient.Policy(ctx, policyID)
	var remote *httpx.RemoteError
	if !errors.As(e, &remote) || remote.Status != 403 {
		t.Fatalf("wrong service identity was accepted: %v", e)
	}
}
