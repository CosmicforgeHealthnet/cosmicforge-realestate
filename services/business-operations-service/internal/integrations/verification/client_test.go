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
	id, renewed, org := ids.New(), ids.New(), ids.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := verifier.Verify(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil || principal.Service != platform.BusinessOperationsService {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/internal/v1/verifications/"+id.String()+"/reverify" {
			var body struct {
				Reference string `json:"caller_reference"`
			}
			if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Reference != "renew-business" || r.Header.Get("Idempotency-Key") != "kyb-renew" {
				w.WriteHeader(422)
				return
			}
			httpx.WriteData(w, 200, Case{VerificationID: renewed, SupersedesID: &id, Type: TypeBusiness, Subject: Subject{Type: SubjectBusiness, Domain: string(platform.DomainBusinessOperations), ID: org}, Status: "DRAFT"})
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
	c, err = client.Reverify(context.Background(), id, "renew-business", "kyb-renew")
	if err != nil || c.VerificationID != renewed || c.SupersedesID == nil || *c.SupersedesID != id {
		t.Fatalf("renew: %+v %v", c, err)
	}
	srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.Get(ctx, id); err == nil {
		t.Fatal("provider outage accepted")
	}
}

// Capacity binds only this organisation's OPERATING_CAPACITY case for exactly
// the requested capacity code, and maps Verification status to an assertion.
func TestCapacityAssertionBinding(t *testing.T) {
	key := testkit.SigningKey(t, "business-cap")
	signer, _ := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.BusinessOperationsService, Key: key})
	org, caseID := ids.New(), ids.New()
	status, ref, typ := "PROCESSING", "capacity:HOSPITALITY", "OPERATING_CAPACITY"
	exp := time.Now().Add(time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"verification_id": caseID, "verification_type": typ, "status": status, "caller_reference": ref, "expires_at": exp, "updated_at": time.Now(),
			"subject": map[string]any{"subject_type": "BUSINESS", "subject_domain": "business-operations", "subject_id": org}}})
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, signer, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !c.SupportsCapacity("HOSPITALITY") || c.SupportsCapacity("hospitality") {
		t.Fatal("capacity code support")
	}
	a, err := c.Capacity(context.Background(), org, "HOSPITALITY", caseID)
	if err != nil || a.Status != "PENDING" || a.OrganisationID != org || a.CaseID != caseID {
		t.Fatalf("pending %+v %v", a, err)
	}
	status = "VERIFIED"
	if a, err = c.Capacity(context.Background(), org, "HOSPITALITY", caseID); err != nil || a.Status != "VERIFIED" {
		t.Fatalf("verified %+v %v", a, err)
	}
	if _, err = c.Capacity(context.Background(), org, "MANAGEMENT", caseID); err == nil {
		t.Fatal("case bound to another capacity accepted")
	}
	if _, err = c.Capacity(context.Background(), ids.New(), "HOSPITALITY", caseID); err == nil {
		t.Fatal("another organisation's case accepted")
	}
	typ = "BUSINESS"
	if _, err = c.Capacity(context.Background(), org, "HOSPITALITY", caseID); err == nil {
		t.Fatal("generic KYB accepted as capacity")
	}
}
