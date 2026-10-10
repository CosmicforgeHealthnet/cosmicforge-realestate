package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPaystackPayoutReadbackAndSettlementWireContracts(t *testing.T) {
	state := "pending"
	merchant := int64(1001)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk_test_payout_fixture" {
			t.Error("secret isolation")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/transfer":
			var in map[string]any
			if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
				t.Error(e)
			}
			if in["recipient"] != "RCP_fixture" || in["amount"] != float64(10000) || in["source"] != "balance" {
				t.Error(in)
			}
			fmt.Fprint(w, `{"status":true,"data":{"reference":"cfp-fixture-reference"}}`)
		case "/transfer/verify/cfp-fixture-reference":
			json.NewEncoder(w).Encode(map[string]any{"status": true, "data": map[string]any{"reference": "cfp-fixture-reference", "status": state, "amount": 10000, "fee_charged": 0, "currency": "NGN", "integration": merchant, "domain": "test", "recipient": map[string]any{"recipient_code": "RCP_fixture", "integration": merchant, "domain": "test"}}})
		case "/transferrecipient/RCP_fixture":
			fmt.Fprint(w, `{"status":true,"data":{"recipient_code":"RCP_fixture","integration":1001,"domain":"test","currency":"NGN","active":true}}`)
		case "/settlement/42/transactions":
			fmt.Fprint(w, `{"status":true,"data":[{"reference":"cf-charge","status":"success","amount":10000,"currency":"NGN","domain":"test","fees":0,"metadata":{"intent_id":"intent"},"customer":{"customer_code":"CUS_fixture","integration":1001}}]}`)
		default:
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	p, e := NewPaystack(server.URL, security.NewSecret("sk_test_payout_fixture"), 1001, "test", nil)
	if e != nil {
		t.Fatal(e)
	}
	in := Transfer{Reference: "cfp-fixture-reference", RecipientRef: "RCP_fixture", AmountMinor: 10000, Currency: "NGN"}
	ack, e := p.InitiateTransfer(context.Background(), in)
	if e != nil || ack.State != "PROCESSING" {
		t.Fatal(ack, e)
	}
	for _, s := range []string{"pending", "success", "failed", "reversed", "unexpected"} {
		state = s
		t.Run(s, func(t *testing.T) {
			result, e := p.VerifyTransfer(context.Background(), in.Reference)
			if e != nil || !result.FeeKnown || result.FeeMinor != 0 {
				t.Fatal(result, e)
			}
			if s == "failed" && !result.NoEffectConfirmed {
				t.Fatal(result)
			}
			if s == "unexpected" && result.State != "UNKNOWN_OUTCOME" {
				t.Fatal(result)
			}
			if s == "success" && result.State != "CONFIRMED_PAID" {
				t.Fatal(result)
			}
		})
	}
	merchant = 2002
	if _, e = p.VerifyTransfer(context.Background(), in.Reference); e != ErrMismatch {
		t.Fatal("wrong merchant", e)
	}
	if _, e = p.FetchRecipient(context.Background(), in.RecipientRef); e != nil {
		t.Fatal(e)
	}
	charges, e := p.SettlementTransactions(context.Background(), "42")
	if e != nil || len(charges) != 1 || charges[0].State != "VERIFIED_SUCCESS" {
		t.Fatal(charges, e)
	}
}
