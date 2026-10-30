package verification

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestAuthenticatedBusinessContract(t *testing.T) {
	key := testkit.SigningKey(t, "business-1")
	signer, _ := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.BusinessOperationsService, Key: key})
	registry := serviceauth.NewRegistry()
	if err := registry.Add(platform.BusinessOperationsService, key.KID, key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	verifier, _ := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.VerificationService, Registry: registry})
	id, org := ids.New(), ids.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := verifier.Verify(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil || principal.Service != platform.BusinessOperationsService {
			w.WriteHeader(401)
			return
		}
		if r.Method == "POST" {
			var body struct {
				Type    string  `json:"verification_type"`
				Subject Subject `json:"subject"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Type != TypeBusiness || body.Subject.Type != SubjectBusiness || body.Subject.Domain != string(platform.DomainBusinessOperations) || body.Subject.ID != org || r.Header.Get("Idempotency-Key") != "kyb-1" {
				w.WriteHeader(422)
				return
			}
		}
		httpx.WriteData(w, 200, Case{VerificationID: id, Type: TypeBusiness, Subject: Subject{Type: SubjectBusiness, Domain: string(platform.DomainBusinessOperations), ID: org}, Status: "PENDING"})
	}))
	defer srv.Close()
	client, err := New(srv.URL, signer, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.Create(context.Background(), TypeBusiness, Subject{Type: SubjectBusiness, Domain: string(platform.DomainBusinessOperations), ID: org}, org.String(), "kyb-1")
	if err != nil || c.VerificationID != id || c.Status != "PENDING" {
		t.Fatalf("create: %+v %v", c, err)
	}
	c, err = client.Get(context.Background(), id)
	if err != nil || c.VerificationID != id {
		t.Fatalf("get: %+v %v", c, err)
	}
	srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.Get(ctx, id); err == nil {
		t.Fatal("provider outage accepted")
	}
}
