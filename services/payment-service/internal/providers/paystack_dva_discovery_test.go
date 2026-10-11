package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

func TestPaystackDVADiscoveryEligibilityAndBankNeutrality(t *testing.T) {
	for _, tc := range []struct {
		name, catalog, returnedBank string
		blocked                     bool
	}{
		{"test-bank", `[{"provider_slug":"test-bank","bank_name":"Test Bank"}]`, "test-bank", false},
		{"wema-discovery", `[{"provider_slug":"wema-bank","bank_name":"Wema Bank"}]`, "wema-bank", false},
		{"another-paystack-bank", `[{"provider_slug":"another-bank","bank_name":"Another Bank"}]`, "another-bank", false},
		{"no-providers", `[]`, "", true},
		{"duplicate-provider", `[{"provider_slug":"wema-bank"},{"provider_slug":"wema-bank"}]`, "", true},
		{"merchant-ineligible", `null`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var assignments atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/dedicated_account/available_providers":
					if tc.name == "merchant-ineligible" {
						w.WriteHeader(403)
						w.Write([]byte(`{"status":false,"message":"Dedicated NUBAN is not available for your business"}`))
						return
					}
					w.Write([]byte(`{"status":true,"data":` + tc.catalog + `}`))
				case "/dedicated_account":
					assignments.Add(1)
					var in map[string]string
					_ = json.NewDecoder(r.Body).Decode(&in)
					if in["preferred_bank"] != "test-bank" || in["customer"] != "CUS_fixture" {
						t.Error("test assignment selection mismatch")
					}
					w.Write([]byte(`{"status":true,"data":{"id":7,"active":true,"currency":"NGN","account_number":"0123456789","bank":{"slug":"` + tc.returnedBank + `"},"assignment":{"integration":1001},"customer":{"customer_code":"CUS_fixture"}}}`))
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			p, e := NewPaystack(server.URL, security.NewSecret("sk_test_contract_fixture"), 1001, "test", nil)
			if e != nil {
				t.Fatal(e)
			}
			v, e := p.AssignDVA(context.Background(), "CUS_fixture")
			if tc.blocked {
				if e == nil || assignments.Load() != 0 {
					t.Fatal("discovery/eligibility bypassed")
				}
			} else if e != nil || v.BankCode != tc.returnedBank || assignments.Load() != 1 {
				t.Fatal("provider-neutral mapping failed")
			}
		})
	}
}
