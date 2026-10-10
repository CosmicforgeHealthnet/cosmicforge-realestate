package providers

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"net/http"
	"net/http/httptest"
	"testing"
)

// These are HTTP simulations against the adapter, not Paystack sandbox evidence.
func TestPaystackRefundOriginalContextAndAmbiguousFailure(t *testing.T) {
	status := "processed"
	merchant := int64(1001)
	transaction := int64(21)
	marker := "cfr-original"
	currency := "NGN"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/refund", "/refund/11":
			wire := map[string]any{"id": 11, "integration": merchant, "domain": "test", "currency": currency, "amount": 4000, "status": status, "merchant_note": marker, "transaction": transaction}
			if r.Method == "POST" {
				var in map[string]any
				if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
					t.Error(e)
				}
				if in["merchant_note"] != "cfr-original" || in["transaction"] != "cf-charge" {
					t.Error(in)
				}
			}
			var data any = wire
			if r.URL.Path == "/refund" && r.Method == "GET" {
				data = []any{wire}
				if r.URL.Query().Get("transaction") != "21" {
					t.Error("missing original transaction filter")
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"status": true, "data": data})
		case "/transaction/verify/cf-charge":
			w.Write([]byte(`{"status":true,"data":{"id":21,"reference":"cf-charge","status":"success","amount":10000,"currency":"NGN","domain":"test","metadata":{"intent_id":"original"},"customer":{"customer_code":"CUS_original","integration":1001}}}`))
		default:
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	p, e := NewPaystack(server.URL, security.NewSecret("sk_test_fixture"), 1001, "test", nil)
	if e != nil {
		t.Fatal(e)
	}
	request := Refund{Reference: "cfr-original", ChargeReference: "cf-charge", Currency: "NGN", AmountMinor: 4000}
	out, e := p.InitiateRefund(context.Background(), request)
	if e != nil || out.ID != "11" || out.State != "CONFIRMED_REFUNDED" {
		t.Fatal(out, e)
	}
	discovered, e := p.VerifyRefund(context.Background(), request)
	if e != nil || discovered.ID != "11" {
		t.Fatal(discovered, e)
	}
	request.ID = "11"
	for _, scenario := range []string{"merchant", "transaction", "marker", "currency"} {
		t.Run(scenario, func(t *testing.T) {
			switch scenario {
			case "merchant":
				merchant = 1002
			case "transaction":
				transaction = 22
			case "marker":
				marker = "another-attempt"
			case "currency":
				currency = "USD"
			}
			if _, e := p.VerifyRefund(context.Background(), request); e == nil {
				t.Fatal("mismatch accepted")
			}
			merchant = 1001
			transaction = 21
			marker = "cfr-original"
			currency = "NGN"
		})
	}
	status = "failed"
	out, e = p.VerifyRefund(context.Background(), request)
	if e != nil || out.NoEffectConfirmed || out.State != "UNKNOWN_OUTCOME" {
		t.Fatal("generic failure restored money", out, e)
	}
}

func TestPaystackDisputeNeedsAssociatedEconomicRefund(t *testing.T) {
	processed := false
	associated := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dispute/7":
			w.Write([]byte(`{"status":true,"data":{"id":7,"domain":"test","currency":"NGN","status":"resolved","refund_amount":4000,"transaction":{"id":21,"reference":"cf-original","domain":"test","customer":{"integration":1001}}}}`))
		case "/transaction/verify/cf-original":
			w.Write([]byte(`{"status":true,"data":{"id":21,"reference":"cf-original","status":"success","amount":10000,"currency":"NGN","domain":"test","customer":{"customer_code":"CUS_original","integration":1001}}}`))
		case "/refund":
			status := "pending"
			if processed {
				status = "processed"
			}
			transaction := 21
			if !associated {
				transaction = 22
			}
			json.NewEncoder(w).Encode(map[string]any{"status": true, "data": []any{map[string]any{"id": 9, "dispute": 7, "integration": 1001, "domain": "test", "currency": "NGN", "amount": 4000, "status": status, "transaction": transaction}}})
		default:
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	p, e := NewPaystack(server.URL, security.NewSecret("sk_test_fixture"), 1001, "test", nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.VerifyReversal(context.Background(), "dispute:7"); e == nil {
		t.Fatal("resolved dispute alone authorized money")
	}
	processed = true
	r, e := p.VerifyReversal(context.Background(), "dispute:7")
	if e != nil || !r.Confirmed || r.EconomicReference != "refund:9" || r.ChargeReference != "cf-original" {
		t.Fatal(r, e)
	}
	associated = false
	if _, e = p.VerifyReversal(context.Background(), "dispute:7"); e == nil {
		t.Fatal("unrelated refund authorized chargeback")
	}
}

func TestWebhookRotationOverlapIsBoundedAndRevocable(t *testing.T) {
	p, e := NewPaystack("https://api.paystack.co", security.NewSecret("sk_test_current"), 1001, "test", nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = p.SetWebhookOverlap([]security.Secret{security.NewSecret("sk_live_old")}); e == nil {
		t.Fatal("live overlap")
	}
	if e = p.SetWebhookOverlap([]security.Secret{security.NewSecret("sk_test_old"), security.NewSecret("sk_test_older"), security.NewSecret("sk_test_oldest")}); e == nil {
		t.Fatal("unbounded overlap")
	}
	if e = p.SetWebhookOverlap([]security.Secret{security.NewSecret("sk_test_old")}); e != nil {
		t.Fatal(e)
	}
	body := []byte(`{"event":"refund.processed","data":{"id":11,"merchant_note":"cfr-original"}}`)
	sign := func(secret string) string {
		m := hmac.New(sha512.New, []byte(secret))
		m.Write(body)
		return hex.EncodeToString(m.Sum(nil))
	}
	for _, secret := range []string{"sk_test_current", "sk_test_old"} {
		event, e := p.ReceiveWebhook(body, sign(secret))
		if e != nil || event.Reference != "cfr-original" {
			t.Fatal(event, e)
		}
	}
	if e = p.SetWebhookOverlap(nil); e != nil {
		t.Fatal(e)
	}
	if _, e = p.ReceiveWebhook(body, sign("sk_test_old")); e == nil {
		t.Fatal("retired secret accepted")
	}
	if _, e = p.ReceiveWebhook(body, sign("sk_test_current")); e != nil {
		t.Fatal(e)
	}
}
