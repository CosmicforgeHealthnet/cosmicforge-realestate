package integration

import (
	"bytes"
	"encoding/base32"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/app"
	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth/credentials"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

type businessHarness struct {
	t       *testing.T
	service *businessauth.Service
	db      *testkit.Database
	server  *httptest.Server
	signer  *serviceauth.Signer
	domain  *businesshttp.Handler
}
type businessUser struct {
	registered      businessauth.Registered
	tokens          businessauth.Tokens
	email, password string
}

func newBusinessHTTP(t *testing.T, provider verification.API) *businessHarness {
	service, db := newBusinessAuth(t)
	sealer, err := credentials.NewSealer("test-mfa-key", []byte("01234567890123456789012345678901"), nil)
	if err != nil {
		t.Fatal(err)
	}
	service.Sealer = sealer
	key := testkit.SigningKey(t, "test-gateway-key")
	registry := serviceauth.NewRegistry()
	if err := registry.Add(platform.APIGateway, key.KID, key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.BusinessOperationsService, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	signer, err := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.APIGateway, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := businessauth.NewHandler(service, verifier)
	if err != nil {
		t.Fatal(err)
	}
	domain := businesshttp.Register(handler, provider, nil)
	domain.InvitationAcceptURL = "https://business.example.test/invitations/accept"
	server := httptest.NewServer(app.NewRouter(health.New("business-operations-service", "test", 0), handler.Router))
	t.Cleanup(server.Close)
	t.Cleanup(func() { validateStoredBusinessEvents(t, db.Pool) })
	return &businessHarness{t: t, service: service, db: db, server: server, signer: signer, domain: domain}
}
func (h *businessHarness) user() *businessUser {
	h.t.Helper()
	u := &businessUser{email: "business-" + ids.New().String() + "@example.test", password: "correct horse battery"}
	registered, err := h.service.Register(h.t.Context(), businessauth.Registration{Email: u.email, Password: u.password, DisplayName: "Test Owner", OrganisationName: "Test Business"}, ids.New().String())
	if err != nil {
		h.t.Fatal(err)
	}
	u.registered = registered
	logged, err := h.service.Login(h.t.Context(), businessauth.Login{Email: u.email, Password: u.password, OrganisationID: registered.OrganisationID}, "client-"+ids.New().String())
	if err != nil {
		h.t.Fatal(err)
	}
	u.tokens = *logged.Tokens
	return u
}
func (h *businessHarness) secure(u *businessUser) {
	h.t.Helper()
	enrolled, err := h.service.EnrolTOTP(h.t.Context(), u.registered.AccountID, u.tokens.SessionID, u.password)
	if err != nil {
		h.t.Fatal(err)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrolled.Secret)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.service.ConfirmTOTP(h.t.Context(), u.registered.AccountID, u.tokens.SessionID, (credentials.TOTP{}).Code(secret, credentials.Step(h.service.Now()))); err != nil {
		h.t.Fatal(err)
	}
}
func (h *businessHarness) call(u *businessUser, method, path string, body any, headers map[string]string) (int, []byte) {
	h.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req, err := http.NewRequest(method, h.server.URL+"/api/v1/business"+path, bytes.NewReader(raw))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "command-"+ids.New().String())
	req.Header.Set("X-Request-ID", "business-domain-request")
	delegation := serviceauth.Delegation{}
	if u != nil {
		req.Header.Set("Authorization", "Bearer "+u.tokens.AccessToken)
		delegation.ActorDomain = platform.DomainBusinessOperations
		delegation.ActorID = u.registered.AccountID
		delegation.OrganizationID = u.registered.OrganisationID
	}
	signed, err := h.signer.TokenFor(string(platform.BusinessOperationsService), delegation)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set(serviceauth.HeaderServiceAuthorization, "Bearer "+signed)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer response.Body.Close()
	out, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return response.StatusCode, out
}
