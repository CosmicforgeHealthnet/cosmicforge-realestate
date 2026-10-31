package media

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

func TestSignedMetadataAndPurposeBoundAccessContract(t *testing.T) {
	key := testkit.SigningKey(t, "business-media-contract")
	signer, err := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.BusinessOperationsService, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	registry := serviceauth.NewRegistry()
	if err = registry.Add(platform.BusinessOperationsService, key.KID, key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.MediaDocumentService, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	id, org := ids.New(), ids.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, e := verifier.Verify(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || principal.Service != platform.BusinessOperationsService {
			w.WriteHeader(401)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /internal/v1/media/" + id.String():
			httpx.WriteData(w, 200, Metadata{ID: id, OwnerDomain: string(platform.DomainBusinessOperations), OwnerType: "ORGANIZATION", OwnerID: org, Status: "READY", Classification: "PRIVATE"})
		case "POST /internal/v1/media/" + id.String() + "/download-url":
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["purpose"] != "BUSINESS_INVENTORY_FLOOR_PLAN" {
				w.WriteHeader(422)
				return
			}
			httpx.WriteData(w, 200, Access{URL: "https://media.example.test/temporary", ExpiresAt: time.Now().Add(time.Minute)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(server.URL, signer, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := client.Get(t.Context(), id)
	if err != nil || metadata.OwnerID != org || metadata.ID != id {
		t.Fatalf("metadata %+v %v", metadata, err)
	}
	access, err := client.DownloadURL(t.Context(), id, "BUSINESS_INVENTORY_FLOOR_PLAN")
	if err != nil || access.URL == "" {
		t.Fatalf("access %+v %v", access, err)
	}
	response, err := http.Get(server.URL + "/internal/v1/media/" + id.String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("missing identity accepted")
	}
	if _, err = New(server.URL, nil, time.Second); err == nil {
		t.Fatal("unsigned client accepted")
	}
	server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err = client.Get(ctx, id); err == nil {
		t.Fatal("provider outage accepted")
	}
}
