package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/app"
	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestBusinessAuthenticationHTTPGatewayBoundary(t *testing.T) {
	service, _ := newBusinessAuth(t)
	key := testkit.SigningKey(t, "gateway-business-1")
	signer, err := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.APIGateway, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	registry := serviceauth.NewRegistry()
	if err := registry.Add(platform.APIGateway, key.KID, key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.BusinessOperationsService, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := businessauth.NewHandler(service, verifier)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.NewRouter(health.New("business-operations-service", "test", 0), handler.Router))
	defer server.Close()
	call := func(method, path string, body any, token string, actor ids.ID, gateway bool) (int, []byte) {
		t.Helper()
		var encoded []byte
		if body != nil {
			encoded, _ = json.Marshal(body)
		}
		req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "http-registration-one")
		req.Header.Set("X-Request-ID", "business-http-test")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if gateway {
			delegation := serviceauth.Delegation{}
			if !actor.IsZero() {
				delegation.ActorID = actor
				delegation.ActorDomain = platform.DomainBusinessOperations
			}
			signed, err := signer.TokenFor(string(platform.BusinessOperationsService), delegation)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set(serviceauth.HeaderServiceAuthorization, "Bearer "+signed)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Header.Get("X-Request-ID") != "business-http-test" {
			t.Fatal("request correlation missing")
		}
		return resp.StatusCode, data
	}
	registration := businessauth.Registration{Email: "http@example.test", Password: "correct horse battery", DisplayName: "HTTP Owner", OrganisationName: "HTTP Business"}
	if status, _ := call("POST", "/api/v1/business/auth/register", registration, "", ids.Nil, false); status != 401 {
		t.Fatalf("unauthenticated gateway: %d", status)
	}
	status, body := call("POST", "/api/v1/business/auth/register", registration, "", ids.Nil, true)
	if status != 201 {
		t.Fatalf("register %d %s", status, body)
	}
	var registered struct {
		Data businessauth.Registered `json:"data"`
	}
	if err := json.Unmarshal(body, &registered); err != nil {
		t.Fatal(err)
	}
	status, body = call("POST", "/api/v1/business/auth/login", businessauth.Login{Email: registration.Email, Password: registration.Password, OrganisationID: registered.Data.OrganisationID}, "", ids.Nil, true)
	if status != 200 {
		t.Fatalf("login %d %s", status, body)
	}
	var login struct {
		Data businessauth.LoginResult `json:"data"`
	}
	if err := json.Unmarshal(body, &login); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"/api/v1/business", "/api/v1/business-operations"} {
		if status, body := call("GET", prefix+"/sessions", nil, login.Data.Tokens.AccessToken, registered.Data.AccountID, true); status != 200 {
			t.Fatalf("sessions %d %s", status, body)
		}
	}
	if status, _ := call("GET", "/api/v1/business/sessions", nil, login.Data.Tokens.AccessToken, ids.New(), true); status != 403 {
		t.Fatalf("forged delegated actor: %d", status)
	}
	if status, _ := call("GET", "/api/v1/business/sessions?limit=1000", nil, login.Data.Tokens.AccessToken, registered.Data.AccountID, true); status != 400 {
		t.Fatalf("pagination: %d", status)
	}
	if status, _ := call("GET", "/internal/v1/business/accounts", nil, login.Data.Tokens.AccessToken, registered.Data.AccountID, true); status != 404 {
		t.Fatalf("internal route exposed: %d", status)
	}
	if status, _ := call("POST", "/api/v1/business/auth/logout", nil, login.Data.Tokens.AccessToken, registered.Data.AccountID, true); status != 200 {
		t.Fatalf("logout: %d", status)
	}
	if _, _, err := service.Authenticate(context.Background(), login.Data.Tokens.AccessToken); err == nil {
		t.Fatal("HTTP logout did not revoke session")
	}
	if status, _ := call("GET", "/api/v1/business/sessions", nil, login.Data.Tokens.AccessToken, registered.Data.AccountID, true); status != 401 {
		t.Fatalf("revoked session HTTP access: %d", status)
	}
}
