package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	phttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// The same signed HTTP contract runs through a controlled neutral implementation
// and the real Paystack adapter over an official-wire HTTP simulator.
func TestMultipleAdapterImplementationsThroughSignedHTTPContract(t *testing.T) {
	for _, kind := range []string{"neutral-contract", "paystack-http-contract"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			if kind == "paystack-http-contract" {
				var mu sync.Mutex
				var initialized map[string]any
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer sk_test_core_contract" {
						t.Error("merchant credentials not scoped")
					}
					w.Header().Set("Content-Type", "application/json")
					mu.Lock()
					defer mu.Unlock()
					switch {
					case r.URL.Path == "/transaction/initialize":
						if e := json.NewDecoder(r.Body).Decode(&initialized); e != nil {
							t.Error(e)
						}
						json.NewEncoder(w).Encode(map[string]any{"status": true, "data": map[string]any{"reference": initialized["reference"], "authorization_url": "https://checkout.paystack.com/contract"}})
					case r.URL.Path == "/customer/CUS_core":
						fmt.Fprint(w, `{"status":true,"data":{"customer_code":"CUS_core","integration":1002,"domain":"test"}}`)
					case initialized != nil && r.URL.Path == "/transaction/verify/"+initialized["reference"].(string):
						amount, e := strconv.ParseInt(initialized["amount"].(string), 10, 64)
						if e != nil {
							t.Error(e)
						}
						json.NewEncoder(w).Encode(map[string]any{"status": true, "data": map[string]any{"reference": initialized["reference"], "status": "success", "amount": amount, "currency": "NGN", "domain": "test", "metadata": initialized["metadata"], "customer": map[string]any{"customer_code": "CUS_core"}}})
					default:
						w.WriteHeader(404)
					}
				}))
				defer server.Close()
				p, e := providers.NewPaystack(server.URL, security.NewSecret("sk_test_core_contract"), 1002, "test", nil)
				if e != nil {
					t.Fatal(e)
				}
				f.cfg.Operational = false
				if e = f.s.Register(system(), f.cfg, finance.Adapter{Collection: f.fake}); e != nil {
					t.Fatal(e)
				}
				c := f.cfg
				c.ID = ids.New()
				c.PoolID = ids.New()
				c.Name = "paystack"
				c.MerchantRef = "1002"
				c.Operational = true
				c.DVAApproved = false
				c.Capabilities = []string{"COLLECTION"}
				if e = f.s.Register(system(), c, finance.Adapter{Collection: p}); e != nil {
					t.Fatal(e)
				}
			}
			key := testkit.SigningKey(t, "signed-owner")
			registry := serviceauth.NewRegistry()
			if e := registry.Add(platform.IndividualOwnerService, key.KID, key.PublicKey()); e != nil {
				t.Fatal(e)
			}
			verifier, e := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.PaymentService, Registry: registry})
			if e != nil {
				t.Fatal(e)
			}
			signer, e := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.IndividualOwnerService, Key: key})
			if e != nil {
				t.Fatal(e)
			}
			token, e := signer.TokenFor("payment-service", serviceauth.Delegation{ActorDomain: platform.DomainIndividualOwner, ActorID: f.account})
			if e != nil {
				t.Fatal(e)
			}
			router := phttp.NewRouter(phttp.Config{Service: f.s, Verifier: verifier, Health: health.New("payment-service", "test", time.Second), Logger: f.s.Log})
			call := func(method, path string, body any) finance.Intent {
				t.Helper()
				raw, _ := json.Marshal(body)
				request := httptest.NewRequest(method, path, bytes.NewReader(raw))
				request.Header.Set("Authorization", "Bearer "+token)
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Idempotency-Key", "same-contract")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				if response.Code != 200 {
					t.Fatal(response.Code, response.Body.String())
				}
				var envelope struct {
					Data finance.Intent `json:"data"`
				}
				if e := json.Unmarshal(response.Body.Bytes(), &envelope); e != nil {
					t.Fatal(e)
				}
				return envelope.Data
			}
			in := finance.IntentInput{WalletID: f.w.WalletID, AmountMinor: 10000, Currency: "NGN", Country: "NG", Category: "RENT", EvidenceRef: "signed-source-accepted-quote", PolicyVersion: "v1", Email: "fixture@example.invalid"}
			i := call("POST", "/internal/v1/payment-intents", in)
			if kind == "neutral-contract" {
				f.fake.success(i)
			}
			verified := call("POST", "/internal/v1/payment-intents/"+i.ID.String()+"/verify", nil)
			if verified.State != "VERIFIED_SUCCESS" || verified.SettlementState != "PENDING" {
				t.Fatal(verified)
			}
			if duplicate := call("POST", "/internal/v1/payment-intents", in); duplicate.ID != i.ID {
				t.Fatal("duplicate changed intent")
			}
			bs, e := f.s.Balances(f.user, f.w.WalletID)
			if e != nil {
				t.Fatal(e)
			}
			for _, b := range bs {
				if b.Kind == "PAYMENT_PENDING" && b.AmountMinor != "10000" {
					t.Fatal(b)
				}
				if b.Withdrawable {
					t.Fatal(b)
				}
			}
		})
	}
}
