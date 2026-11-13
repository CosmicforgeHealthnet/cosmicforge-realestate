package e2e

// Cross-service certification platform: every owning service runs as its own
// process(es) with its own PostgreSQL database, one NATS JetStream broker and
// the api-gateway. The only non-platform party is the external payment
// provider, represented by a loopback Paystack contract double (the Payment
// Service accepts http loopback provider URLs only in test mode). Nothing in
// this file imports service code.

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// paystackDouble is the external payment provider (not a platform service):
// it initialises a hosted checkout, lets the test "pay" a reference, and
// answers verification and customer reads like the provider API.
type paystackDouble struct {
	mu       sync.Mutex
	merchant int64
	charges  map[string]map[string]any
	server   *httptest.Server
}

func newPaystackDouble(t *testing.T) *paystackDouble {
	d := &paystackDouble{merchant: 424242, charges: map[string]map[string]any{}}
	d.server = httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(d.server.Close)
	return d
}

func (d *paystackDouble) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk_test_") {
		w.WriteHeader(401)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"status": true, "data": v}) }
	switch {
	case r.Method == "POST" && r.URL.Path == "/transaction/initialize":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		ref := in["reference"].(string)
		amount := int64(0)
		fmt.Sscan(fmt.Sprint(in["amount"]), &amount)
		md, _ := json.Marshal(in["metadata"])
		d.charges[ref] = map[string]any{"id": len(d.charges) + 1, "reference": ref, "status": "ongoing", "currency": in["currency"], "domain": "test", "amount": amount, "fees": 0, "metadata": string(md),
			"customer": map[string]any{"integration": d.merchant, "customer_code": "CUS_e2e"}}
		reply(map[string]any{"reference": ref, "authorization_url": "https://checkout.paystack.test/" + ref, "access_code": "ac_" + ref})
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/transaction/verify/"):
		c, ok := d.charges[strings.TrimPrefix(r.URL.Path, "/transaction/verify/")]
		if !ok {
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": false})
			return
		}
		reply(c)
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/customer/"):
		reply(map[string]any{"customer_code": "CUS_e2e", "email": "guest@example.com", "identified": true, "integration": d.merchant, "domain": "test"})
	default:
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": false})
	}
}

// pay marks the hosted checkout for an intent reference as paid.
func (d *paystackDouble) pay(ref, status string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.charges[ref]
	if ok {
		c["status"] = status
	}
	return ok
}

// crossEnv is the running cross-service platform.
type crossEnv struct {
	*platformEnv
	keys        map[platform.Service]key
	userKeys    map[platform.Domain]key
	dbs         map[platform.Service]string
	businessDB  string
	paystack    *paystackDouble
	adminMFAKey []byte
	env         map[platform.Service]map[string]string
}

// business_operations_db is a fixed name (Business refuses any other).
func createNamedDatabase(t *testing.T, name string) string {
	t.Helper()
	admin := testkit.PostgresAdminURL(t)
	db, err := sql.Open("pgx", admin)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE DATABASE ` + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := sql.Open("pgx", admin)
		if err == nil {
			_, _ = c.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`)
			c.Close()
		}
	})
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	return u.String()
}

func startCrossPlatform(t *testing.T) *crossEnv {
	t.Helper()
	testkit.PostgresAdminURL(t)
	p := &crossEnv{platformEnv: &platformEnv{t: t, internal: map[platform.Service]string{}, signers: map[platform.Service]*serviceauth.Signer{}},
		keys: map[platform.Service]key{}, userKeys: map[platform.Domain]key{}, dbs: map[platform.Service]string{}, env: map[platform.Service]map[string]string{}}
	services := []platform.Service{platform.APIGateway, platform.VerificationService, platform.MediaDocumentService, platform.NotificationService, platform.PaymentService,
		platform.BusinessOperationsService, platform.MarketplaceService, platform.PropertyManagerService, platform.ArtisanService, platform.ProfessionalServicesService, platform.AdminService}
	var registry []map[string]any
	for _, s := range services {
		k := newKey(string(s) + "-cross")
		p.keys[s] = k
		registry = append(registry, map[string]any{"service": s, "keys": []map[string]string{{"kid": k.kid, "public_key": security.EncodeEd25519PublicKey(k.priv.Public().(ed25519.PublicKey))}}})
		p.signers[s], _ = serviceauth.NewSigner(serviceauth.SignerConfig{Service: s, Key: k.signing()})
	}
	trusted, _ := json.Marshal(registry)
	var issuers []map[string]any
	for _, s := range []platform.Service{platform.BusinessOperationsService, platform.MarketplaceService, platform.PropertyManagerService, platform.ArtisanService, platform.ProfessionalServicesService, platform.AdminService} {
		d, _ := platform.DomainForService(s)
		k := newKey(string(d) + "-users")
		p.userKeys[d] = k
		issuers = append(issuers, map[string]any{"issuer": s, "domain": d, "keys": []map[string]string{{"kid": k.kid, "public_key": security.EncodeEd25519PublicKey(k.priv.Public().(ed25519.PublicKey))}}})
	}
	issuersJSON, _ := json.Marshal(issuers)

	p.nats = testkit.StartNATS(t)
	redisAddr := os.Getenv("CF_TEST_REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = miniredis.RunT(t).Addr()
	}
	s3 := s3mem.New()
	_ = s3.CreateBucket("cf-media")
	s3srv := httptest.NewServer(gofakes3.New(s3).Server())
	t.Cleanup(s3srv.Close)
	p.paystack = newPaystackDouble(t)
	p.captureFile = filepath.Join(t.TempDir(), "notifications.jsonl")

	for _, s := range services[1:] {
		if s == platform.BusinessOperationsService {
			p.businessDB = createNamedDatabase(t, "business_operations_db")
			p.dbs[s] = p.businessDB
			continue
		}
		p.dbs[s] = testkit.NewDatabase(t, nil).URL
	}
	t.Cleanup(p.stop)
	base := func(s platform.Service) map[string]string {
		k := p.keys[s]
		return map[string]string{"APP_ENV": "test", "LOG_LEVEL": "warn", "NATS_URL": p.nats.URL, "NATS_SUBJECT_PREFIX": "realestate",
			"SERVICE_AUTH_KEY_ID": k.kid, "SERVICE_AUTH_PRIVATE_KEY": security.EncodeEd25519Seed(k.priv), "SERVICE_AUTH_TRUSTED_KEYS": string(trusted),
			"OUTBOX_POLL_INTERVAL": "100ms", "DISPATCH_POLL_INTERVAL": "100ms", "DATABASE_URL": p.dbs[s]}
	}
	// Hold every listener until all addresses are chosen so no two
	// processes are handed the same port.
	var held []net.Listener
	reserve := func() string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, l)
		return l.Addr().String()
	}
	addrs := map[platform.Service][2]string{}
	for _, s := range services[1:] {
		addrs[s] = [2]string{reserve(), reserve()}
		p.internal[s] = "http://" + addrs[s][0]
	}
	gwAddr := reserve()
	for _, l := range held {
		_ = l.Close()
	}
	userTokens := func(s platform.Service) []string {
		d, _ := platform.DomainForService(s)
		k := p.userKeys[d]
		return []string{"USER_TOKEN_KEY_ID", k.kid, "USER_TOKEN_PRIVATE_KEY", security.EncodeEd25519Seed(k.priv), "BUSINESS_OPERATIONS_SERVICE_URL", p.internal[platform.BusinessOperationsService]}
	}
	bk := p.userKeys[platform.DomainBusinessOperations]
	p.adminMFAKey = make([]byte, 32)
	copy(p.adminMFAKey, "cross-e2e-admin-mfa-sealing-key!")
	extra := map[platform.Service][]string{
		platform.MediaDocumentService: {"STORAGE_ENDPOINT", s3srv.URL, "STORAGE_REGION", "us-east-1", "STORAGE_BUCKET", "cf-media", "STORAGE_PATH_STYLE", "true",
			"STORAGE_ACCESS_KEY_ID", "e2e-access", "STORAGE_SECRET_ACCESS_KEY", "e2e-secret", "MEDIA_SCANNER", "dev", "SCAN_POLL_INTERVAL", "100ms"},
		platform.VerificationService: {"MEDIA_SERVICE_URL", p.internal[platform.MediaDocumentService], "SANDBOX_KYC_CALLBACK_SECRET", "e2e-sandbox-secret", "PROVIDER_POLL_INTERVAL", "200ms"},
		platform.NotificationService: {"NOTIFY_DEV_CAPTURE_FILE", p.captureFile},
		platform.PaymentService: {"PAYSTACK_BASE_URL", p.paystack.server.URL, "PAYSTACK_E2E_SECRET", "sk_test_cross_e2e",
			"PAYMENT_PROVIDER_CONFIG", fmt.Sprintf(`[{"provider_account_id":"%s","pool_id":"%s","provider":"paystack","legal_entity":"cosmicforge-ng","merchant_ref":"%d","mode":"test","countries":["NG"],"currencies":["NGN"],"categories":["HOTEL","SHORT_LET"],"capabilities":["COLLECTION"],"approved":true,"operational":true,"min_minor":100,"max_minor":100000000,"priority":0,"available_liquidity_minor":0,"policy_version":"e2e-v1","credentials_ref":"PAYSTACK_E2E_SECRET","approval_ref":"e2e-provider-approval"}]`, ids.New(), ids.New(), p.paystack.merchant)},
		platform.BusinessOperationsService: {"BUSINESS_ACCESS_KEY_ID", bk.kid, "BUSINESS_ACCESS_PRIVATE_KEY", security.EncodeEd25519Seed(bk.priv), "BUSINESS_CODE_PEPPER", strings.Repeat("cross-e2e-pepper", 2),
			"BUSINESS_MFA_KEY_ID", "mfa-cross", "BUSINESS_MFA_KEY", base64.StdEncoding.EncodeToString([]byte("cross-e2e-business-mfa-key-32byt")),
			"VERIFICATION_SERVICE_URL", p.internal[platform.VerificationService], "NOTIFICATION_SERVICE_URL", p.internal[platform.NotificationService],
			"PAYMENT_SERVICE_URL", p.internal[platform.PaymentService], "MARKETPLACE_SERVICE_URL", p.internal[platform.MarketplaceService], "MEDIA_SERVICE_URL", p.internal[platform.MediaDocumentService],
			"BUSINESS_INVITATION_ACCEPT_URL", "https://business.cosmicforge.test/invitations/accept"},
		platform.MarketplaceService:          append(userTokens(platform.MarketplaceService), "PAYMENT_SERVICE_URL", p.internal[platform.PaymentService], "PAYMENT_LEGAL_ENTITY", "cosmicforge-ng"),
		platform.PropertyManagerService:      userTokens(platform.PropertyManagerService),
		platform.ArtisanService:              userTokens(platform.ArtisanService),
		platform.ProfessionalServicesService: append(userTokens(platform.ProfessionalServicesService), "VERIFICATION_SERVICE_URL", p.internal[platform.VerificationService]),
		platform.AdminService: append(userTokens(platform.AdminService), "ADMIN_MFA_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(p.adminMFAKey), "ADMIN_MFA_ENCRYPTION_KEY_ID", "mfa-cross",
			"BOOTSTRAP_ROLE_GRANTS", "supervisor@admin.cross=SUPERVISOR,reviewer@admin.cross=REVIEWER",
			"ARTISAN_SERVICE_URL", p.internal[platform.ArtisanService], "PROPERTY_MANAGER_SERVICE_URL", p.internal[platform.PropertyManagerService], "PROFESSIONAL_SERVICES_SERVICE_URL", p.internal[platform.ProfessionalServicesService]),
	}
	with := func(m map[string]string, kv ...string) map[string]string {
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	// Migrate everything first, then start every API and worker, then wait.
	for _, s := range services[1:] {
		p.run(string(s)+"-migrate", map[string]string{"DATABASE_URL": p.dbs[s]}, "up")
	}
	for _, s := range services[1:] {
		apiEnv := with(base(s), append(extra[s], "HTTP_ADDR", addrs[s][0])...)
		workerEnv := with(base(s), append(extra[s], "HTTP_ADDR", addrs[s][1])...)
		p.env[s] = workerEnv
		p.start(string(s)+" api", string(s)+"-api", apiEnv)
		p.start(string(s)+" worker", string(s)+"-worker", workerEnv)
	}
	for _, s := range services[1:] {
		waitReady(t, "http://"+addrs[s][0])
		waitReady(t, "http://"+addrs[s][1])
	}
	p.gateway = "http://" + gwAddr
	gk := p.keys[platform.APIGateway]
	gw := map[string]string{"APP_ENV": "test", "LOG_LEVEL": "warn", "HTTP_ADDR": gwAddr, "REDIS_ADDR": redisAddr, "GATEWAY_TRUSTED_ISSUERS": string(issuersJSON),
		"SERVICE_AUTH_KEY_ID": gk.kid, "SERVICE_AUTH_PRIVATE_KEY": security.EncodeEd25519Seed(gk.priv),
		"GATEWAY_RATE_LIMIT_AUTH_SENSITIVE": "1000/1m", "GATEWAY_RATE_LIMIT_PUBLIC": "5000/1m", "GATEWAY_RATE_LIMIT_AUTHENTICATED": "5000/1m"}
	for _, s := range services[1:] {
		gw["GATEWAY_UPSTREAM_"+strings.ToUpper(strings.ReplaceAll(string(s), "-", "_"))+"_URL"] = p.internal[s]
	}
	p.start("api-gateway", "api-gateway-api", gw)
	waitReady(t, p.gateway)
	return p
}

// restartWorker stops and restarts a service worker (recovery scenarios).
func (p *crossEnv) restartWorker(s platform.Service) {
	p.t.Helper()
	name := string(s) + " worker"
	for i, pr := range p.procs {
		if pr.name != name || pr.cmd.ProcessState != nil {
			continue
		}
		_ = pr.cmd.Process.Kill()
		_, _ = pr.cmd.Process.Wait()
		p.procs = append(p.procs[:i], p.procs[i+1:]...)
		break
	}
	p.start(name, string(s)+"-worker", p.env[s])
	waitReady(p.t, "http://"+p.env[s]["HTTP_ADDR"])
}

// signedCall calls an internal API as caller with an explicit delegation.
func (p *crossEnv) signedCall(caller, target platform.Service, d serviceauth.Delegation, method, path string, body any, key string) response {
	p.t.Helper()
	tok, err := p.signers[caller].TokenFor(string(target), d)
	if err != nil {
		p.t.Fatal(err)
	}
	return send(p.t, method, p.internal[target]+path, tok, "", body, key)
}

func eventually(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	testkit.Eventually(t, timeout, cond, "%s", msg)
}

func TestCrossPlatformBootsEveryOwningService(t *testing.T) {
	p := startCrossPlatform(t)
	for s, base := range p.internal {
		r := send(t, "GET", base+"/ready", "", "", nil, "")
		if r.status != 200 {
			t.Fatalf("%s not ready: %d %s", s, r.status, r.body)
		}
	}
	_ = context.Background()
}

func serviceDelegation() serviceauth.Delegation { return serviceauth.Delegation{} }
