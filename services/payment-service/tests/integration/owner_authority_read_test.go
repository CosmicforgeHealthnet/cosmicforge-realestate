package integration

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	phttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// Owner must inspect an existing Core authority before submitting a split;
// it cannot invent the beneficiary behind a caller-provided reference.
func TestOwnerSourceAuthorityReadContract(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	registry := serviceauth.NewRegistry()
	signers := map[platform.Service]*serviceauth.Signer{}
	for _, svc := range []platform.Service{platform.IndividualOwnerService, platform.PropertyManagerService} {
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
	verifier, e := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.PaymentService, Registry: registry})
	if e != nil {
		t.Fatal(e)
	}
	router := phttp.NewRouter(phttp.Config{Service: f.s, Verifier: verifier, Health: health.New("payment-service", "test", time.Second), Logger: f.s.Log})
	call := func(svc platform.Service) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/internal/v1/beneficiary-authorities/"+f.authority.ID.String(), nil)
		if svc != "" {
			token, e := signers[svc].TokenFor("payment-service", serviceauth.Delegation{ActorDomain: platform.DomainIndividualOwner, ActorID: f.account})
			if e != nil {
				t.Fatal(e)
			}
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := call(platform.IndividualOwnerService); w.Code != http.StatusOK {
		t.Fatalf("original issuer cannot read approved authority: HTTP %d", w.Code)
	}
	if w := call(platform.PropertyManagerService); w.Code != http.StatusForbidden {
		t.Fatalf("unrelated source can inspect authority: HTTP %d", w.Code)
	}
	if w := call(""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned read: HTTP %d", w.Code)
	}
}

func TestVerifiedPayoutReferenceReadEligibility(t *testing.T) {
	f := newLife(t, "RESIDENTIAL_INITIAL", 10000)
	id := f.destination(t)
	refs, e := f.s.PayoutReferences(f.user, f.w.WalletID)
	if e != nil {
		t.Fatal(e)
	}
	if len(refs) != 1 || refs[0].ID != id || !refs[0].Eligible {
		t.Fatal("current Verification-attested payout reference is not readable as eligible", refs)
	}
	if _, e = f.s.PayoutReferences(ctx(platform.IndividualOwnerService, platform.DomainIndividualOwner, ids.New()), f.w.WalletID); e == nil {
		t.Fatal("foreign payout reference visible")
	}
}
