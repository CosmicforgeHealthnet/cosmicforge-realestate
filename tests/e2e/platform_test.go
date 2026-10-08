// Package e2e runs the foundation platform as real processes — api-gateway,
// verification-service, media-document-service and notification-service
// (api + worker each) — against real PostgreSQL and in-process NATS
// JetStream, Redis and S3-compatible storage, and drives it only through
// HTTP and the event stream (black box). It imports no service code.
//
// Requires CF_TEST_POSTGRES_URL (an admin DSN). Skipped otherwise unless
// CF_REQUIRE_INTEGRATION=1, which turns the skip into a failure.
package e2e

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// component is one deployable binary set.
type component struct {
	name string // directory under the repo
	dir  string
	cmds []string
}

var components = []component{
	{"api-gateway", "../../gateway/api-gateway", []string{"api"}},
	{"verification-service", "../../services/verification-service", []string{"api", "worker", "migrate"}},
	{"media-document-service", "../../services/media-document-service", []string{"api", "worker", "migrate"}},
	{"notification-service", "../../services/notification-service", []string{"api", "worker", "migrate"}},
}

var binDir string

func TestMain(m *testing.M) {
	code := func() int {
		if os.Getenv("CF_TEST_POSTGRES_URL") == "" {
			return m.Run() // tests skip (or fail under CF_REQUIRE_INTEGRATION)
		}
		dir, err := os.MkdirTemp("", "cf-e2e-bin-")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(dir)
		binDir = dir
		for _, c := range components {
			for _, cmd := range c.cmds {
				out := filepath.Join(dir, c.name+"-"+cmd)
				b := exec.Command("go", "build", "-o", out, "./cmd/"+cmd)
				b.Dir = c.dir
				b.Env = append(os.Environ(), "CGO_ENABLED=0")
				if msg, err := b.CombinedOutput(); err != nil {
					fmt.Fprintf(os.Stderr, "build %s/%s: %v\n%s", c.name, cmd, err, msg)
					return 1
				}
			}
		}
		return m.Run()
	}()
	os.Exit(code)
}

type key struct {
	kid  string
	priv ed25519.PrivateKey
}

func newKey(kid string) key {
	_, p, _ := ed25519.GenerateKey(rand.Reader)
	return key{kid, p}
}

func (k key) signing() jwt.SigningKey { return jwt.SigningKey{KID: k.kid, PrivateKey: k.priv} }

// platformEnv is a running foundation platform.
type platformEnv struct {
	t        *testing.T
	gateway  string
	internal map[platform.Service]string // service -> base URL
	signers  map[platform.Service]*serviceauth.Signer
	users    *auth.Issuer
	nats     *testkit.NATSServer
	procs    []*proc
}

type proc struct {
	name string
	cmd  *exec.Cmd
	out  *bytes.Buffer
	mu   *sync.Mutex
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func (p *platformEnv) start(name, bin string, env map[string]string) *proc {
	p.t.Helper()
	cmd := exec.Command(filepath.Join(binDir, bin))
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	pr := &proc{name: name, cmd: cmd, out: &bytes.Buffer{}, mu: &sync.Mutex{}}
	cmd.Stdout, cmd.Stderr = lockedWriter{pr.mu, pr.out}, lockedWriter{pr.mu, pr.out}
	if err := cmd.Start(); err != nil {
		p.t.Fatal(err)
	}
	p.procs = append(p.procs, pr)
	return pr
}

func (p *platformEnv) run(bin string, env map[string]string, args ...string) {
	p.t.Helper()
	cmd := exec.Command(filepath.Join(binDir, bin), args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		p.t.Fatalf("%s %v: %v\n%s", bin, args, err, out)
	}
}

func (p *platformEnv) stop() {
	for i := len(p.procs) - 1; i >= 0; i-- {
		pr := p.procs[i]
		_ = pr.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- pr.cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				p.t.Errorf("%s exited uncleanly on SIGTERM: %v", pr.name, err)
			}
		case <-time.After(30 * time.Second):
			_ = pr.cmd.Process.Kill()
			p.t.Errorf("%s did not stop within 30s", pr.name)
		}
		if p.t.Failed() {
			pr.mu.Lock()
			p.t.Logf("---- %s output ----\n%s", pr.name, tail(pr.out.String(), 4000))
			pr.mu.Unlock()
		}
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	testkit.Eventually(t, 30*time.Second, func() bool {
		resp, err := http.Get(url + "/ready")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}, "%s never became ready", url)
}

// startPlatform launches every component with fresh databases and keys.
func startPlatform(t *testing.T) *platformEnv {
	t.Helper()
	testkit.PostgresAdminURL(t) // skips (or fails under CF_REQUIRE_INTEGRATION) without PostgreSQL
	p := &platformEnv{t: t, internal: map[platform.Service]string{}, signers: map[platform.Service]*serviceauth.Signer{}}

	// Identities: platform components plus individual-owner-service as the
	// (not yet implemented) domain caller and user-token issuer.
	keys := map[platform.Service]key{}
	var registry []map[string]any
	for _, s := range []platform.Service{platform.APIGateway, platform.VerificationService, platform.MediaDocumentService,
		platform.NotificationService, platform.IndividualOwnerService} {
		k := newKey(string(s) + "-e2e")
		keys[s] = k
		registry = append(registry, map[string]any{"service": s, "keys": []map[string]string{{"kid": k.kid, "public_key": security.EncodeEd25519PublicKey(k.priv.Public().(ed25519.PublicKey))}}})
		p.signers[s], _ = serviceauth.NewSigner(serviceauth.SignerConfig{Service: s, Key: k.signing()})
	}
	trusted, _ := json.Marshal(registry)
	userKey := newKey("io-users-e2e")
	p.users, _ = auth.NewIssuer(auth.IssuerConfig{Issuer: string(platform.IndividualOwnerService), TTL: 15 * time.Minute, Key: userKey.signing()})
	issuers, _ := json.Marshal([]map[string]any{{"issuer": platform.IndividualOwnerService, "domain": platform.DomainIndividualOwner,
		"keys": []map[string]string{{"kid": userKey.kid, "public_key": security.EncodeEd25519PublicKey(userKey.priv.Public().(ed25519.PublicKey))}}}})

	p.nats = testkit.StartNATS(t)
	redisAddr := os.Getenv("CF_TEST_REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = miniredis.RunT(t).Addr()
	}
	s3 := s3mem.New()
	_ = s3.CreateBucket("cf-media")
	s3srv := httptest.NewServer(gofakes3.New(s3).Server())
	t.Cleanup(s3srv.Close)

	base := func(s platform.Service) map[string]string {
		k := keys[s]
		return map[string]string{"APP_ENV": "test", "LOG_LEVEL": "warn", "NATS_URL": p.nats.URL,
			"SERVICE_AUTH_KEY_ID": k.kid, "SERVICE_AUTH_PRIVATE_KEY": security.EncodeEd25519Seed(k.priv), "SERVICE_AUTH_TRUSTED_KEYS": string(trusted),
			"OUTBOX_POLL_INTERVAL": "100ms", "DISPATCH_POLL_INTERVAL": "100ms"}
	}
	with := func(m map[string]string, kv ...string) map[string]string {
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	// Databases first: cleanups run LIFO, so processes stop before the
	// databases are dropped.
	dbs := map[platform.Service]string{}
	for _, s := range []platform.Service{platform.MediaDocumentService, platform.VerificationService, platform.NotificationService} {
		dbs[s] = testkit.NewDatabase(t, nil).URL
	}
	t.Cleanup(p.stop)
	launch := func(s platform.Service, extra ...string) {
		dbURL := dbs[s]
		p.run(string(s)+"-migrate", map[string]string{"DATABASE_URL": dbURL}, "up")
		apiAddr, workerAddr := freeAddr(t), freeAddr(t)
		p.internal[s] = "http://" + apiAddr
		p.start(string(s)+" api", string(s)+"-api", with(base(s), append(extra, "DATABASE_URL", dbURL, "HTTP_ADDR", apiAddr)...))
		p.start(string(s)+" worker", string(s)+"-worker", with(base(s), append(extra, "DATABASE_URL", dbURL, "HTTP_ADDR", workerAddr)...))
		waitReady(t, "http://"+apiAddr)
		waitReady(t, "http://"+workerAddr)
	}
	launch(platform.MediaDocumentService, "STORAGE_ENDPOINT", s3srv.URL, "STORAGE_REGION", "us-east-1", "STORAGE_BUCKET", "cf-media",
		"STORAGE_PATH_STYLE", "true", "STORAGE_ACCESS_KEY_ID", "e2e-access", "STORAGE_SECRET_ACCESS_KEY", "e2e-secret",
		"MEDIA_SCANNER", "dev", "SCAN_POLL_INTERVAL", "100ms")
	launch(platform.VerificationService, "MEDIA_SERVICE_URL", p.internal[platform.MediaDocumentService],
		"SANDBOX_KYC_CALLBACK_SECRET", "e2e-sandbox-secret", "PROVIDER_POLL_INTERVAL", "200ms")
	launch(platform.NotificationService) // APP_ENV=test: capture providers, no credentials

	gwAddr := freeAddr(t)
	p.gateway = "http://" + gwAddr
	gk := keys[platform.APIGateway]
	p.start("api-gateway", "api-gateway-api", map[string]string{"APP_ENV": "test", "LOG_LEVEL": "warn", "HTTP_ADDR": gwAddr,
		"REDIS_ADDR": redisAddr, "GATEWAY_TRUSTED_ISSUERS": string(issuers),
		"SERVICE_AUTH_KEY_ID": gk.kid, "SERVICE_AUTH_PRIVATE_KEY": security.EncodeEd25519Seed(gk.priv),
		"GATEWAY_UPSTREAM_VERIFICATION_SERVICE_URL":   p.internal[platform.VerificationService],
		"GATEWAY_UPSTREAM_MEDIA_DOCUMENT_SERVICE_URL": p.internal[platform.MediaDocumentService],
		"GATEWAY_UPSTREAM_NOTIFICATION_SERVICE_URL":   p.internal[platform.NotificationService]})
	waitReady(t, p.gateway)
	return p
}

// ---- HTTP helpers ----

type response struct {
	status int
	body   []byte
	header http.Header
}

func (r response) data(t *testing.T, v any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(r.body, &env); err != nil || env.Data == nil {
		t.Fatalf("no data (%d): %s", r.status, r.body)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatal(err)
	}
}

func send(t *testing.T, method, url, bearer, header string, body any, idemKey string) response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		if header == "" {
			header = "Authorization"
		}
		req.Header.Set(header, "Bearer "+bearer)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return response{resp.StatusCode, b, resp.Header}
}

// asService calls a service's internal API with a service JWT.
func (p *platformEnv) asService(caller, target platform.Service, method, path string, body any, idemKey string) response {
	p.t.Helper()
	tok, err := p.signers[caller].TokenFor(string(target), serviceauth.Delegation{})
	if err != nil {
		p.t.Fatal(err)
	}
	return send(p.t, method, p.internal[target]+path, tok, "", body, idemKey)
}

// asUser calls the public API through the gateway with a user access token.
func (p *platformEnv) asUser(token, method, path string, body any, idemKey string) response {
	p.t.Helper()
	return send(p.t, method, p.gateway+path, token, "", body, idemKey)
}
