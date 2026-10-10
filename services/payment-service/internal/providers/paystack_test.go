package providers

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

func TestPaystackOfficialCollectionContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk_test_contract_fixture" {
			t.Error("credential")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/transaction/initialize":
			var in map[string]any
			if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
				t.Error(e)
			}
			if in["amount"] != "120000" || in["currency"] != "NGN" || in["reference"] != "cf-fixture" {
				t.Error(in)
			}
			if _, ok := in["split_code"]; ok {
				t.Error("premature splits")
			}
			w.Write([]byte(`{"status":true,"data":{"reference":"cf-fixture","authorization_url":"https://checkout.paystack.com/fixture"}}`))
		case "/transaction/verify/cf-fixture":
			w.Write([]byte(`{"status":true,"data":{"reference":"cf-fixture","status":"success","amount":120000,"currency":"NGN","domain":"test","metadata":{"intent_id":"fixture-intent"},"customer":{"customer_code":"CUS_fixture"}}}`))
		case "/customer/CUS_fixture":
			w.Write([]byte(`{"status":true,"data":{"customer_code":"CUS_fixture","integration":1001,"domain":"test"}}`))
		default:
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	p, e := NewPaystack(server.URL, security.NewSecret("sk_test_contract_fixture"), 1001, "test", nil)
	if e != nil {
		t.Fatal(e)
	}
	i, e := p.InitializeCharge(context.Background(), ChargeRequest{Reference: "cf-fixture", Email: "fixture@example.invalid", Currency: "NGN", IntentID: "fixture-intent", AmountMinor: 120000, Channels: []string{"card", "bank_transfer"}})
	if e != nil || i.Reference != "cf-fixture" {
		t.Fatal(i, e)
	}
	c, e := p.VerifyCharge(context.Background(), i.Reference)
	if e != nil || c.State != "VERIFIED_SUCCESS" || c.IntentID != "fixture-intent" {
		t.Fatal(c, e)
	}
	body := []byte(`{"event":"charge.success","data":{"reference":"cf-fixture"}}`)
	m := hmac.New(sha512.New, []byte("sk_test_contract_fixture"))
	m.Write(body)
	sig := hex.EncodeToString(m.Sum(nil))
	if _, e = p.ReceiveWebhook(body, sig); e != nil {
		t.Fatal(e)
	}
	if _, e = p.ReceiveWebhook(append(body, ' '), sig); e == nil {
		t.Fatal("raw signature")
	}
	if _, e = p.ReceiveWebhook(body, "invalid"); e == nil {
		t.Fatal("invalid signature")
	}
	if e = p.CancelCharge(context.Background(), i.Reference); e != ErrUnsupported {
		t.Fatal(e)
	}
}
func TestPaystackDVAContractAndValidationAcceptance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/customer":
			w.Write([]byte(`{"status":true,"data":{"customer_code":"CUS_fixture","identified":false,"integration":1001,"domain":"test"}}`))
		case "/customer/CUS_fixture":
			w.Write([]byte(`{"status":true,"data":{"customer_code":"CUS_fixture","identified":true,"integration":1001,"domain":"test"}}`))
		case "/customer/CUS_fixture/identification":
			w.WriteHeader(202)
			w.Write([]byte(`{"status":true,"message":"Customer Identification in progress"}`))
		case "/dedicated_account":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["preferred_bank"] != "titan-paystack" || body["customer"] != "CUS_fixture" {
				t.Error(body)
			}
			w.Write([]byte(`{"status":true,"data":{"id":123,"active":true,"currency":"NGN","account_number":"0123456789","bank":{"slug":"titan-paystack"},"assignment":{"integration":1001},"customer":{"customer_code":"CUS_fixture"}}}`))
		case "/dedicated_account/123":
			w.Write([]byte(`{"status":true,"data":{"id":123,"active":true,"currency":"NGN","account_number":"0123456789","bank":{"slug":"titan-paystack"},"assignment":{"integration":1001},"customer":{"customer_code":"CUS_fixture"}}}`))
		case "/dedicated_account/requery":
			if r.URL.Query().Get("provider_slug") != "titan-paystack" {
				t.Error(r.URL)
			}
			w.Write([]byte(`{"status":true}`))
		default:
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	p, e := NewPaystack(server.URL, security.NewSecret("sk_test_contract_fixture"), 1001, "test", nil)
	if e != nil {
		t.Fatal(e)
	}
	c, e := p.CreateCustomer(context.Background(), CustomerRequest{Email: "fixture@example.invalid"})
	if e != nil || c.Identified {
		t.Fatal(c, e)
	}
	if e = p.ValidateCustomer(context.Background(), c.Code, Validation{Country: "NG", Type: "bank_account"}); e != nil {
		t.Fatal(e)
	}
	c, e = p.FetchCustomer(context.Background(), c.Code)
	if e != nil || !c.Identified {
		t.Fatal(c, e)
	}
	d, e := p.AssignDVA(context.Background(), c.Code)
	if e != nil || d.ID != "123" {
		t.Fatal(d, e)
	}
	if _, e = p.FetchDVA(context.Background(), d.ID); e != nil {
		t.Fatal(e)
	}
	if e = p.RequeryDVA(context.Background(), d.AccountNumber, d.BankCode); e != nil {
		t.Fatal(e)
	}
}
func TestProviderHTTPSCredentialIsolationAndUnknown(t *testing.T) {
	for _, test := range []struct{ base, key, mode string }{{"http://example.com", "sk_test_fixture", "test"}, {"https://example.com", "sk_test_fixture", "live"}, {"https://example.com", "hardcoded", "test"}} {
		if _, e := NewPaystack(test.base, security.NewSecret(test.key), 1001, test.mode, nil); e == nil {
			t.Fatal("unsafe configuration")
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte("private financial details"))
	}))
	defer server.Close()
	p, e := NewPaystack(server.URL, security.NewSecret("sk_test_fixture"), 1001, "test", nil)
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.VerifyCharge(context.Background(), "cf-fixture")
	if e != ErrUnknown || strings.Contains(e.Error(), "private") {
		t.Fatal(e)
	}
	if _, e = (DisabledCustody{}).Proof(context.Background(), "merchant-balance"); e != ErrUnsupported {
		t.Fatal("fabricated custody")
	}
}
