package e2e

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// startOwnerPlatform runs individual-owner-service (api + worker) with the
// real notification-service and the real gateway. The gateway trusts owner
// access tokens only through the owner service's published JWKS. With
// producers, the real verification-service and media-document-service run
// too and the owner consumes their events from JetStream.
func startOwnerPlatform(t *testing.T, producers ...bool) *platformEnv {
	return startOwnerPlatformConfigured(t, nil, producers...)
}

func startOwnerPlatformConfigured(t *testing.T, overrides map[string]string, producers ...bool) *platformEnv {
	t.Helper()
	withProducers := len(producers) > 0 && producers[0]
	testkit.PostgresAdminURL(t)
	p := &platformEnv{t: t, internal: map[platform.Service]string{}, signers: map[platform.Service]*serviceauth.Signer{}}
	keys := map[platform.Service]key{}
	var registry []map[string]any
	services := []platform.Service{platform.APIGateway, platform.NotificationService, platform.IndividualOwnerService, platform.AdminService}
	if withProducers {
		// marketplace-service only signs test requests (it is not implemented).
		services = append(services, platform.VerificationService, platform.MediaDocumentService, platform.MarketplaceService, platform.PropertyManagerService)
	}
	for _, s := range services {
		k := newKey(string(s) + "-e2e")
		keys[s] = k
		registry = append(registry, map[string]any{"service": s, "keys": []map[string]string{{"kid": k.kid, "public_key": security.EncodeEd25519PublicKey(k.priv.Public().(ed25519.PublicKey))}}})
		p.signers[s], _ = serviceauth.NewSigner(serviceauth.SignerConfig{Service: s, Key: k.signing()})
	}
	trusted, _ := json.Marshal(registry)
	p.nats = testkit.StartNATS(t)
	redisAddr := os.Getenv("CF_TEST_REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = miniredis.RunT(t).Addr()
	}
	base := func(s platform.Service) map[string]string {
		k := keys[s]
		m := map[string]string{"APP_ENV": "test", "LOG_LEVEL": "warn", "NATS_URL": p.nats.URL,
			"SERVICE_AUTH_KEY_ID": k.kid, "SERVICE_AUTH_PRIVATE_KEY": security.EncodeEd25519Seed(k.priv), "SERVICE_AUTH_TRUSTED_KEYS": string(trusted),
			"OUTBOX_POLL_INTERVAL": "100ms", "DISPATCH_POLL_INTERVAL": "100ms"}
		for k, v := range overrides {
			m[k] = v
		}
		return m
	}
	dbs := map[platform.Service]string{}
	for _, s := range services[1:] {
		if s == platform.AdminService || s == platform.MarketplaceService || s == platform.PropertyManagerService {
			continue
		}
		db := testkit.NewDatabase(t, nil)
		dbs[s] = db.URL
		if s == platform.IndividualOwnerService {
			p.ownerDB = db
		}
	}
	t.Cleanup(p.stop)
	launch := func(s platform.Service, extra ...string) {
		p.run(string(s)+"-migrate", map[string]string{"DATABASE_URL": dbs[s]}, "up")
		apiAddr, workerAddr := freeAddr(t), freeAddr(t)
		p.internal[s] = "http://" + apiAddr
		env := func(addr string) map[string]string {
			m := base(s)
			m["DATABASE_URL"], m["HTTP_ADDR"] = dbs[s], addr
			for i := 0; i < len(extra); i += 2 {
				m[extra[i]] = extra[i+1]
			}
			return m
		}
		p.start(string(s)+" api", string(s)+"-api", env(apiAddr))
		p.start(string(s)+" worker", string(s)+"-worker", env(workerAddr))
		waitReady(t, "http://"+apiAddr)
		waitReady(t, "http://"+workerAddr)
	}
	p.captureFile = filepath.Join(t.TempDir(), "notifications.jsonl")
	if runtime.GOOS == "windows" {
		p.smtp = startSMTPCapture(t)
		host, port, _ := net.SplitHostPort(p.smtp.addr)
		launch(platform.NotificationService, "NOTIFY_EMAIL_PROVIDER", "smtp", "SMTP_HOST", host, "SMTP_PORT", port, "SMTP_FROM", "test@cosmicforge.example", "SMTP_REQUIRE_TLS", "false")
	} else {
		launch(platform.NotificationService, "NOTIFY_DEV_CAPTURE_FILE", p.captureFile)
	}
	var producerEnv []string
	gatewayUpstreams := map[string]string{}
	if withProducers {
		s3 := s3mem.New()
		_ = s3.CreateBucket("cf-media")
		s3srv := httptest.NewServer(gofakes3.New(s3).Server())
		t.Cleanup(s3srv.Close)
		launch(platform.MediaDocumentService, "STORAGE_ENDPOINT", s3srv.URL, "STORAGE_REGION", "us-east-1", "STORAGE_BUCKET", "cf-media",
			"STORAGE_PATH_STYLE", "true", "STORAGE_ACCESS_KEY_ID", "e2e-access", "STORAGE_SECRET_ACCESS_KEY", "e2e-secret",
			"MEDIA_SCANNER", "dev", "SCAN_POLL_INTERVAL", "100ms")
		launch(platform.VerificationService, "MEDIA_SERVICE_URL", p.internal[platform.MediaDocumentService],
			"SANDBOX_KYC_CALLBACK_SECRET", "e2e-sandbox-secret", "PROVIDER_POLL_INTERVAL", "200ms")
		producerEnv = []string{"VERIFICATION_SERVICE_URL", p.internal[platform.VerificationService], "MEDIA_SERVICE_URL", p.internal[platform.MediaDocumentService],
			"DOMAIN_WORKER_INTERVAL", "1s"}
		gatewayUpstreams["GATEWAY_UPSTREAM_MEDIA_DOCUMENT_SERVICE_URL"] = p.internal[platform.MediaDocumentService]
	}
	userKey := newKey("io-at-e2e")
	launch(platform.IndividualOwnerService, append([]string{
		"NOTIFICATION_SERVICE_URL", p.internal[platform.NotificationService],
		"OWNER_ACCESS_TOKEN_KEY_ID", userKey.kid, "OWNER_ACCESS_TOKEN_PRIVATE_KEY", security.EncodeEd25519Seed(userKey.priv),
		"OWNER_CODE_PEPPER", strings.Repeat("e2e-pepper-", 4), "OWNER_MFA_ENCRYPTION_KEY_ID", "mfa-e2e",
		"OWNER_MFA_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)), "CLOSURE_POLL_INTERVAL", "1s"}, producerEnv...)...)

	issuers, _ := json.Marshal([]map[string]any{{"issuer": platform.IndividualOwnerService, "domain": platform.DomainIndividualOwner,
		"jwks_url": p.internal[platform.IndividualOwnerService] + "/.well-known/jwks.json"}})
	gwAddr := freeAddr(t)
	p.gateway = "http://" + gwAddr
	gk := keys[platform.APIGateway]
	gwEnv := map[string]string{"APP_ENV": "test", "LOG_LEVEL": "warn", "HTTP_ADDR": gwAddr,
		"REDIS_ADDR": redisAddr, "GATEWAY_TRUSTED_ISSUERS": string(issuers),
		"SERVICE_AUTH_KEY_ID": gk.kid, "SERVICE_AUTH_PRIVATE_KEY": security.EncodeEd25519Seed(gk.priv),
		"GATEWAY_UPSTREAM_INDIVIDUAL_OWNER_SERVICE_URL": p.internal[platform.IndividualOwnerService],
		"GATEWAY_UPSTREAM_NOTIFICATION_SERVICE_URL":     p.internal[platform.NotificationService]}
	for k, v := range overrides {
		gwEnv[k] = v
	}
	for k, v := range gatewayUpstreams {
		gwEnv[k] = v
	}
	p.start("api-gateway", "api-gateway-api", gwEnv)
	waitReady(t, p.gateway)
	return p
}

// TestOwnerAccountJourneyThroughGateway is critical E2E scenario A/C (account
// part): register via the gateway, use the session, rotate tokens, detect
// reuse, admin restriction propagated as an event, logout — across real
// processes and the real notification-service contract.
func TestOwnerAccountJourneyThroughGateway(t *testing.T) {
	p := startOwnerPlatform(t)
	email := "e2e-" + ids.New().String()[24:] + "@example.com"
	r := send(t, "POST", p.gateway+"/api/v1/owner/auth/register", "", "", map[string]any{
		"email": email, "password": "correct horse battery", "display_name": "E2E Owner"}, "")
	if r.status != 201 {
		t.Fatalf("register: %d %s", r.status, r.body)
	}
	var reg struct {
		Account struct {
			AccountID ids.ID `json:"account_id"`
			Status    string `json:"status"`
		} `json:"account"`
		Tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
		EmailVerification struct {
			DeliveryStatus string `json:"delivery_status"`
		} `json:"email_verification"`
	}
	r.data(t, &reg)
	// The real notification-service accepted the code (acceptance, not delivery).
	if reg.Account.Status != "PENDING_VERIFICATION" || reg.EmailVerification.DeliveryStatus != "ACCEPTED" {
		t.Fatalf("%s", r.body)
	}
	// The gateway verifies the owner token via JWKS; the owner service re-verifies.
	if r := p.asUser(reg.Tokens.AccessToken, "GET", "/api/v1/owner/me", nil, ""); r.status != 200 {
		t.Fatalf("me: %d %s", r.status, r.body)
	}
	// A token from an untrusted issuer never reaches the owner service.
	foreign, _ := auth.NewIssuer(auth.IssuerConfig{Issuer: string(platform.ArtisanService), Key: newKey("ar").signing()})
	ft, _, _ := foreign.Issue(auth.IssueRequest{Subject: reg.Account.AccountID, SessionID: ids.New()})
	if r := p.asUser(ft, "GET", "/api/v1/owner/me", nil, ""); r.status != 401 {
		t.Fatalf("foreign token: %d", r.status)
	}
	// Internal routes are not reachable through the edge.
	if r := p.asUser(reg.Tokens.AccessToken, "GET", "/internal/v1/owner/accounts/"+reg.Account.AccountID.String(), nil, ""); r.status != 404 {
		t.Fatalf("internal via edge: %d", r.status)
	}
	// Refresh rotation and reuse detection through the gateway.
	r = send(t, "POST", p.gateway+"/api/v1/owner/auth/refresh", "", "", map[string]any{"refresh_token": reg.Tokens.RefreshToken}, "")
	if r.status != 200 {
		t.Fatalf("refresh: %d %s", r.status, r.body)
	}
	if r := send(t, "POST", p.gateway+"/api/v1/owner/auth/refresh", "", "", map[string]any{"refresh_token": reg.Tokens.RefreshToken}, ""); r.status != 401 {
		t.Fatalf("reuse: %d", r.status)
	}
	if r := p.asUser(reg.Tokens.AccessToken, "GET", "/api/v1/owner/me", nil, ""); r.status != 401 {
		t.Fatalf("session must be revoked after reuse: %d", r.status)
	}
	// Sign in again.
	r = send(t, "POST", p.gateway+"/api/v1/owner/auth/login", "", "", map[string]any{"email": email, "password": "correct horse battery"}, "")
	if r.status != 200 {
		t.Fatalf("login: %d %s", r.status, r.body)
	}
	var login struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	r.data(t, &login)

	// Logout ends the session immediately.
	if r := p.asUser(login.Tokens.AccessToken, "POST", "/api/v1/owner/auth/logout", nil, ""); r.status != 204 {
		t.Fatalf("logout: %d %s", r.status, r.body)
	}
	if r := p.asUser(login.Tokens.AccessToken, "GET", "/api/v1/owner/me", nil, ""); r.status != 401 {
		t.Fatalf("after logout: %d", r.status)
	}
	r = send(t, "POST", p.gateway+"/api/v1/owner/auth/login", "", "", map[string]any{"email": email, "password": "correct horse battery"}, "")
	r.data(t, &login)

	// Admin suspension (admin-service with an attributable admin actor) is
	// guarded by the account state machine (a PENDING_VERIFICATION account
	// cannot be RESTRICTED), revokes sessions and is published to JetStream
	// by the owner worker.
	nc, err := nats.Connect(p.nats.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	tok, _ := p.signers[platform.AdminService].TokenFor(string(platform.IndividualOwnerService),
		serviceauth.Delegation{ActorDomain: platform.DomainAdmin, ActorID: ids.New()})
	accountPath := p.internal[platform.IndividualOwnerService] + "/internal/v1/owner/accounts/" + reg.Account.AccountID.String()
	if r := send(t, "POST", accountPath+"/restrict", tok, "", map[string]any{"reason_code": "FRAUD_REVIEW"}, "e2e-restrict-1"); r.status != 409 {
		t.Fatalf("restrict pending account: %d %s", r.status, r.body)
	}
	r = send(t, "POST", accountPath+"/suspend", tok, "", map[string]any{"reason_code": "FRAUD_REVIEW"}, "e2e-suspend-1")
	if r.status != 200 {
		t.Fatalf("suspend: %d %s", r.status, r.body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var got string
	testkit.Eventually(t, 20*time.Second, func() bool {
		st, err := js.Stream(ctx, "REALESTATE_OWNER")
		if err != nil {
			return false
		}
		cons, err := st.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{FilterSubjects: []string{"realestate.owner.account.restricted.v1"}})
		if err != nil {
			return false
		}
		m, err := cons.Next(jetstream.FetchMaxWait(time.Second))
		if err != nil {
			return false
		}
		got = string(m.Data())
		return true
	}, "owner.account.restricted.v1 published")
	if !strings.Contains(got, reg.Account.AccountID.String()) || !strings.Contains(got, `"SUSPENDED"`) || strings.Contains(got, email) {
		t.Fatalf("event: %s", got)
	}
	if r := p.asUser(login.Tokens.AccessToken, "GET", "/api/v1/owner/me", nil, ""); r.status != 401 {
		t.Fatalf("suspended session still usable: %d %s", r.status, r.body)
	}
	if r := send(t, "POST", p.gateway+"/api/v1/owner/auth/login", "", "", map[string]any{"email": email, "password": "correct horse battery"}, ""); r.status != 403 {
		t.Fatalf("suspended login: %d %s", r.status, r.body)
	}
}
