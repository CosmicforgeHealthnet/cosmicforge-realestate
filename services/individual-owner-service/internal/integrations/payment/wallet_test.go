package payment

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWalletRegistrationSignedHTTPAdapter(t *testing.T) {
	key := testkit.SigningKey(t, "owner-wallet-client")
	reg := serviceauth.NewRegistry()
	if e := reg.Add(platform.IndividualOwnerService, key.KID, key.PublicKey()); e != nil {
		t.Fatal(e)
	}
	verifier, e := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.PaymentService, Registry: reg})
	if e != nil {
		t.Fatal(e)
	}
	signer, e := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.IndividualOwnerService, Key: key})
	if e != nil {
		t.Fatal(e)
	}
	account, wallet := ids.New(), ids.New()
	handler := serviceauth.Authenticate(verifier, serviceauth.HeaderAuthorization)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := serviceauth.PrincipalFrom(r.Context())
		if !ok || p.Service != platform.IndividualOwnerService {
			t.Error("caller identity")
		}
		if r.Method != "POST" || r.URL.Path != "/internal/v1/wallets/ensure" || r.Header.Get("Idempotency-Key") != "registration-evidence" {
			t.Error("command contract")
		}
		var in map[string]any
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			t.Error(e)
		}
		if in["account_id"] != account.String() || in["registration_ref"] != "registration-evidence" || in["role"] != "OWNER" || in["party_kind"] != "PERSON" || in["legal_entity"] != "cosmicforge-ng" || in["currency"] != "NGN" {
			t.Error("canonical registration mapping")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"wallet_id": wallet}})
	}))
	server := httptest.NewServer(handler)
	defer server.Close()
	client, e := NewWalletProvisioner(server.URL, signer, "cosmicforge-ng", "NGN")
	if e != nil {
		t.Fatal(e)
	}
	if e = client.Ensure(context.Background(), account, "registration-evidence"); e != nil {
		t.Fatal(e)
	}
	server.Close()
	if e = client.Ensure(context.Background(), account, "registration-evidence"); e == nil {
		t.Fatal("outage reported successful delivery")
	}
}
