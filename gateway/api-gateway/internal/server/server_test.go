package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway/internal/ratelimit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway/internal/routing"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// upstream records what a backend service received.
type upstream struct {
	srv  *httptest.Server
	mu   sync.Mutex
	last *http.Request
	body string
	hits int
	// behaviour
	delay time.Duration
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.last, u.body = r.Clone(context.Background()), string(b)
		u.hits++
		d := u.delay
		u.mu.Unlock()
		if d > 0 {
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Server", "upstream/1.0")
		httpx.WriteData(w, 200, map[string]string{"path": r.URL.Path})
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) url() *url.URL { p, _ := url.Parse(u.srv.URL); return p }

func (u *upstream) snapshot() (*http.Request, string, int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.last, u.body, u.hits
}

type env struct {
	t                          *testing.T
	gw                         *Gateway
	srv                        *httptest.Server
	mr                         *miniredis.Miniredis
	ioKey, arKey, gwKey        jwt.SigningKey
	io, ar                     *auth.Issuer
	verification, media, notif *upstream
	owner, webhook             *upstream
	gwVerifier                 *serviceauth.Verifier
}

func setup(t *testing.T, mut func(*config.Config)) *env {
	e := &env{t: t, ioKey: testkit.SigningKey(t, "io-1"), arKey: testkit.SigningKey(t, "ar-1"), gwKey: testkit.SigningKey(t, "gw-1")}
	e.io, _ = auth.NewIssuer(auth.IssuerConfig{Issuer: "individual-owner-service", Key: e.ioKey})
	e.ar, _ = auth.NewIssuer(auth.IssuerConfig{Issuer: "artisan-service", Key: e.arKey})
	e.verification, e.media, e.notif, e.owner, e.webhook = newUpstream(t), newUpstream(t), newUpstream(t), newUpstream(t), newUpstream(t)

	routes, err := routing.Build(map[platform.Service]*url.URL{
		platform.VerificationService:    e.verification.url(),
		platform.MediaDocumentService:   e.media.url(),
		platform.NotificationService:    e.notif.url(),
		platform.IndividualOwnerService: e.owner.url(),
		platform.PaymentService:         e.webhook.url(),
	}, map[string]platform.Service{"sandbox-kyc": platform.VerificationService, "paystack": platform.PaymentService})
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.APIGateway, Key: e.gwKey})
	limits := map[routing.Category]ratelimit.Policy{}
	for cat, def := range config.DefaultRateLimits {
		p, _ := ratelimit.ParsePolicy(def)
		limits[cat] = p
	}
	p := limits[routing.CategoryAuthSensitive]
	p.FailClosed = true
	limits[routing.CategoryAuthSensitive] = p
	_, trustedNet, _ := net.ParseCIDR("10.0.0.0/8")
	cfg := &config.Config{
		Environment: "test",
		TrustedIssuers: []auth.TrustedIssuer{
			{Issuer: "individual-owner-service", Domain: platform.DomainIndividualOwner, Keys: jwt.NewStaticKeys().Add("individual-owner-service", "io-1", e.ioKey.PublicKey())},
			{Issuer: "artisan-service", Domain: platform.DomainArtisan, Keys: jwt.NewStaticKeys().Add("artisan-service", "ar-1", e.arKey.PublicKey())},
		},
		TokenAudience: auth.DefaultAudience, Signer: signer, Routes: routes,
		UpstreamTimeout: 2 * time.Second, MaxBodyBytes: 1 << 16, RateLimits: limits,
		CORSAllowedOrigins: []string{"https://app.cosmicforge.example"},
		TrustedProxies:     []*net.IPNet{trustedNet}, HSTS: true,
	}
	if mut != nil {
		mut(cfg)
	}
	e.mr = miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: e.mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	e.gw, err = New(cfg, rdb, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	e.srv = httptest.NewServer(e.gw.Handler)
	t.Cleanup(e.srv.Close)
	reg := serviceauth.NewRegistry()
	_ = reg.Add(platform.APIGateway, "gw-1", e.gwKey.PublicKey())
	e.gwVerifier, _ = serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.VerificationService, Registry: reg})
	return e
}

func (e *env) do(method, path, token string, hdr map[string]string, body string) (*http.Response, apperrors.Envelope) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var env apperrors.Envelope
	_ = json.Unmarshal(b, &env)
	return resp, env
}

func (e *env) userToken(iss *auth.Issuer) (string, ids.ID) {
	sub := ids.New()
	tok, _, err := iss.Issue(auth.IssueRequest{Subject: sub})
	if err != nil {
		e.t.Fatal(err)
	}
	return tok, sub
}

func TestRouteMatrix(t *testing.T) {
	e := setup(t, nil)
	tok, _ := e.userToken(e.io)
	cases := []struct {
		path string
		up   *upstream
	}{
		{"/api/v1/verification/cases/x", e.verification},
		{"/api/v1/media/uploads", e.media},
		{"/api/v1/notifications/push-endpoints", e.notif},
		{"/api/v1/payments/wallets/me", e.webhook},
		{"/api/v1/owner/properties", e.owner},
	}
	for _, c := range cases {
		resp, _ := e.do("GET", c.path, tok, nil, "")
		if resp.StatusCode != 200 {
			t.Fatalf("%s -> %d", c.path, resp.StatusCode)
		}
		r, _, _ := c.up.snapshot()
		if r == nil || r.URL.Path != c.path {
			t.Fatalf("%s not routed to expected upstream", c.path)
		}
	}
	// Unconfigured domain services are not routed (no fabricated functionality).
	for _, p := range []string{"/api/v1/artisan/jobs", "/api/v1/unknown/x", "/api/v2/verification/x", "/"} {
		resp, env := e.do("GET", p, tok, nil, "")
		if resp.StatusCode != 404 || env.Error.Code != apperrors.CodeRouteNotFound {
			t.Fatalf("%s -> %d %s", p, resp.StatusCode, env.Error.Code)
		}
	}
}

func TestValidTokenForwardsSignedDelegation(t *testing.T) {
	e := setup(t, nil)
	tok, sub := e.userToken(e.io)
	resp, _ := e.do("POST", "/api/v1/verification/cases", tok, map[string]string{
		"X-Request-ID": "client-req-1",
		"traceparent":  "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		// spoofing attempts
		"X-Actor-Id": ids.New().String(), "X-Actor-Domain": "admin", "X-Calling-Service": "payment-service",
		"X-Service-Authorization": "Bearer forged",
	}, `{"a":1}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Request-ID") != "client-req-1" || resp.Header.Get("Server") != "" {
		t.Fatal("response headers")
	}
	r, body, _ := e.verification.snapshot()
	if body != `{"a":1}` {
		t.Fatalf("body %q", body)
	}
	for _, h := range []string{"X-Actor-Id", "X-Actor-Domain", "X-Calling-Service"} {
		if r.Header.Get(h) != "" {
			t.Fatalf("client header %s leaked upstream", h)
		}
	}
	if r.Header.Get("Authorization") != "" {
		t.Fatal("user token must not be forwarded to shared platform services")
	}
	if r.Header.Get("X-Request-ID") != "client-req-1" || !strings.Contains(r.Header.Get("traceparent"), "4bf92f3577b34da6a3ce929d0e0e4736") {
		t.Fatalf("request context not propagated: %v", r.Header)
	}
	assertion := strings.TrimPrefix(r.Header.Get(serviceauth.HeaderServiceAuthorization), "Bearer ")
	p, err := e.gwVerifier.Verify(context.Background(), assertion)
	if err != nil {
		t.Fatalf("upstream cannot verify gateway assertion: %v", err)
	}
	if p.Service != platform.APIGateway || p.Delegation.ActorID != sub || p.Delegation.ActorDomain != platform.DomainIndividualOwner ||
		p.Delegation.OriginalRequestID != "client-req-1" {
		t.Fatalf("delegation %+v", p)
	}
	// Domain routes receive the user's own token for re-verification.
	e.do("GET", "/api/v1/owner/me", tok, nil, "")
	r, _, _ = e.owner.snapshot()
	if r.Header.Get("Authorization") != "Bearer "+tok {
		t.Fatal("domain route should receive the user token")
	}
}

func TestTokenRejections(t *testing.T) {
	e := setup(t, nil)
	sub := ids.New()
	now := time.Now()
	sign := func(k jwt.SigningKey, iss, aud string, exp time.Time) string {
		tok, _ := jwt.Sign(k, auth.TokenType, auth.AccessClaims{RegisteredClaims: jwt.RegisteredClaims{
			Issuer: iss, Subject: sub.String(), Audience: jwt.Audience{aud}, IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(exp), ID: ids.New().String()}})
		return tok
	}
	rogue := testkit.SigningKey(t, "io-1")
	cases := map[string]string{
		"invalid signature": sign(rogue, "individual-owner-service", auth.DefaultAudience, now.Add(10*time.Minute)),
		"wrong audience":    sign(e.ioKey, "individual-owner-service", "other-api", now.Add(10*time.Minute)),
		"unknown issuer":    sign(e.ioKey, "identity-service", auth.DefaultAudience, now.Add(10*time.Minute)),
		"expired":           sign(e.ioKey, "individual-owner-service", auth.DefaultAudience, now.Add(-2*time.Minute)),
		"garbage":           "not.a.jwt",
	}
	for name, tok := range cases {
		resp, env := e.do("GET", "/api/v1/verification/cases/x", tok, nil, "")
		if resp.StatusCode != 401 || env.Error.Code != apperrors.CodeInvalidToken || env.Error.RequestID == "" {
			t.Errorf("%s: %d %+v", name, resp.StatusCode, env)
		}
	}
	resp, env := e.do("GET", "/api/v1/verification/cases/x", "", nil, "")
	if resp.StatusCode != 401 || env.Error.Code != apperrors.CodeAuthenticationRequired {
		t.Fatalf("missing token: %d %s", resp.StatusCode, env.Error.Code)
	}
	_, _, hits := e.verification.snapshot()
	if hits != 0 {
		t.Fatal("rejected requests reached upstream")
	}
}

func TestCrossDomainTokenMisuse(t *testing.T) {
	e := setup(t, nil)
	artisanTok, _ := e.userToken(e.ar)
	resp, env := e.do("GET", "/api/v1/owner/properties", artisanTok, nil, "")
	if resp.StatusCode != 403 || env.Error.Code != apperrors.CodePermissionDenied {
		t.Fatalf("artisan token on individual-owner route: %d %s", resp.StatusCode, env.Error.Code)
	}
	// Shared platform services accept any trusted domain; they authorize by subject.
	if resp, _ := e.do("GET", "/api/v1/media/x", artisanTok, nil, ""); resp.StatusCode != 200 {
		t.Fatalf("artisan token on shared service: %d", resp.StatusCode)
	}
	// Anonymous access to a domain's optional-auth route is allowed (domain decides).
	if resp, _ := e.do("GET", "/api/v1/owner/public-listings", "", nil, ""); resp.StatusCode != 200 {
		t.Fatalf("anonymous optional route: %d", resp.StatusCode)
	}
}

func TestInternalRoutesNeverExposed(t *testing.T) {
	e := setup(t, nil)
	tok, _ := e.userToken(e.io)
	paths := []string{
		"/internal/v1/verifications",
		"/internal",
		"/api/v1/media/../../internal/v1/media",
		"/api/v1/media/%2e%2e/%2e%2e/internal/v1/media",
		"/api/v1/media/..%2f..%2finternal/v1/media",
		"/api/v1/media//internal",
		"/api/v1/verification/./x",
	}
	for _, p := range paths {
		req, _ := http.NewRequest("GET", e.srv.URL+"/", nil)
		req.URL.Opaque = p // send the raw path without client-side cleaning
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("%s -> %d", p, resp.StatusCode)
		}
	}
	for _, u := range []*upstream{e.verification, e.media, e.notif} {
		if _, _, hits := u.snapshot(); hits != 0 {
			t.Fatal("internal/non-canonical path reached an upstream")
		}
	}
}

func TestWebhookRouting(t *testing.T) {
	e := setup(t, nil)
	resp, _ := e.do("POST", "/webhooks/v1/sandbox-kyc/callbacks", "", map[string]string{"X-Signature": "abc"}, `{"event":"x"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("webhook %d", resp.StatusCode)
	}
	r, body, _ := e.verification.snapshot()
	if body != `{"event":"x"}` || r.Header.Get("X-Signature") != "abc" || r.Header.Get(serviceauth.HeaderServiceAuthorization) == "" {
		t.Fatal("webhook not forwarded intact with gateway assertion")
	}
	if resp, _ := e.do("POST", "/webhooks/v1/paystack/events", "", nil, `{}`); resp.StatusCode != 200 {
		t.Fatal("paystack webhooks must route to payment-service only")
	}
	if r, _, _ := e.webhook.snapshot(); r == nil {
		t.Fatal("payment webhook not routed")
	}
	if resp, _ := e.do("POST", "/webhooks/v1/unknown/x", "", nil, `{}`); resp.StatusCode != 404 {
		t.Fatal("unknown provider must 404")
	}
}

func TestRateLimiting(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.RateLimits[routing.CategoryAuthenticated] = ratelimit.Policy{Rate: 3, Period: time.Minute, Burst: 3}
		c.RateLimits[routing.CategoryPublic] = ratelimit.Policy{Rate: 2, Period: time.Minute, Burst: 2}
	})
	tok, _ := e.userToken(e.io)
	for i := 0; i < 3; i++ {
		if resp, _ := e.do("GET", "/api/v1/media/x", tok, nil, ""); resp.StatusCode != 200 {
			t.Fatalf("request %d: %d", i, resp.StatusCode)
		}
	}
	resp, env := e.do("GET", "/api/v1/media/x", tok, nil, "")
	if resp.StatusCode != 429 || env.Error.Code != apperrors.CodeRateLimited || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("expected 429, got %d %s", resp.StatusCode, env.Error.Code)
	}
	// Another actor has an independent budget.
	other, _ := e.userToken(e.io)
	if resp, _ := e.do("GET", "/api/v1/media/x", other, nil, ""); resp.StatusCode != 200 {
		t.Fatal("limits must be per actor")
	}
	// Anonymous public route keyed by client IP; spoofed XFF from an untrusted peer is ignored.
	for i := 0; i < 2; i++ {
		e.do("GET", "/api/v1/owner/listings", "", map[string]string{"X-Forwarded-For": "1.2.3." + string(rune('1'+i))}, "")
	}
	if resp, _ := e.do("GET", "/api/v1/owner/listings", "", map[string]string{"X-Forwarded-For": "9.9.9.9"}, ""); resp.StatusCode != 429 {
		t.Fatalf("XFF spoofing bypassed IP limit: %d", resp.StatusCode)
	}
}

func TestAuthSensitiveRateLimitAndRedisOutage(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.RateLimits[routing.CategoryAuthSensitive] = ratelimit.Policy{Rate: 2, Period: time.Minute, Burst: 2, FailClosed: true}
		c.RateLimits[routing.CategoryAuthenticated] = ratelimit.Policy{Rate: 100, Period: time.Minute, Burst: 100}
	})
	for i := 0; i < 2; i++ {
		e.do("POST", "/api/v1/owner/auth/login", "", nil, `{}`)
	}
	if resp, _ := e.do("POST", "/api/v1/owner/auth/login", "", nil, `{}`); resp.StatusCode != 429 {
		t.Fatalf("login brute force not limited: %d", resp.StatusCode)
	}
	// Redis unavailable.
	e.mr.Close()
	tok, _ := e.userToken(e.io)
	if resp, _ := e.do("GET", "/api/v1/media/x", tok, nil, ""); resp.StatusCode != 200 {
		t.Fatalf("authenticated route should degrade to local limiter, got %d", resp.StatusCode)
	}
	resp, env := e.do("POST", "/api/v1/owner/auth/register", "", nil, `{}`)
	if resp.StatusCode != 503 || env.Error.Code != apperrors.CodeServiceUnavailable {
		t.Fatalf("auth-sensitive must fail closed without Redis: %d %s", resp.StatusCode, env.Error.Code)
	}
}

func TestUpstreamFailures(t *testing.T) {
	e := setup(t, func(c *config.Config) { c.UpstreamTimeout = 200 * time.Millisecond })
	tok, _ := e.userToken(e.io)
	e.verification.mu.Lock()
	e.verification.delay = time.Second
	e.verification.mu.Unlock()
	start := time.Now()
	resp, env := e.do("GET", "/api/v1/verification/x", tok, nil, "")
	if resp.StatusCode != 503 || env.Error.Code != apperrors.CodeServiceUnavailable || time.Since(start) > 900*time.Millisecond {
		t.Fatalf("timeout: %d %s %s", resp.StatusCode, env.Error.Code, time.Since(start))
	}
	e.media.srv.Close()
	resp, env = e.do("GET", "/api/v1/media/x", tok, nil, "")
	if resp.StatusCode != 503 || strings.Contains(env.Error.Message, "127.0.0.1") {
		t.Fatalf("down upstream: %d %+v", resp.StatusCode, env)
	}
}

func TestBodyLimitSecurityHeadersAndCORS(t *testing.T) {
	e := setup(t, nil)
	tok, _ := e.userToken(e.io)
	resp, env := e.do("POST", "/api/v1/media/uploads", tok, nil, `{"x":"`+strings.Repeat("a", 1<<17)+`"}`)
	if resp.StatusCode != 413 || env.Error.Code != apperrors.CodePayloadTooLarge {
		t.Fatalf("body limit: %d", resp.StatusCode)
	}
	resp, _ = e.do("GET", "/health", "", nil, "")
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "Strict-Transport-Security", "Content-Security-Policy"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
	resp, _ = e.do("OPTIONS", "/api/v1/media/uploads", "", map[string]string{"Origin": "https://app.cosmicforge.example", "Access-Control-Request-Method": "POST"}, "")
	if resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Origin") != "https://app.cosmicforge.example" ||
		!strings.Contains(resp.Header.Get("Access-Control-Allow-Headers"), "Idempotency-Key") {
		t.Fatalf("preflight: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = e.do("OPTIONS", "/api/v1/media/uploads", "", map[string]string{"Origin": "https://evil.example", "Access-Control-Request-Method": "POST"}, "")
	if resp.StatusCode != 403 || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("disallowed origin preflight: %d", resp.StatusCode)
	}
}

func TestHealthReadyVersion(t *testing.T) {
	e := setup(t, nil)
	for _, p := range []string{"/health", "/ready", "/version"} {
		resp, _ := e.do("GET", p, "", nil, "")
		if resp.StatusCode != 200 {
			t.Fatalf("%s -> %d", p, resp.StatusCode)
		}
	}
	e.gw.Health().SetShuttingDown()
	if resp, _ := e.do("GET", "/ready", "", nil, ""); resp.StatusCode != 503 {
		t.Fatal("ready must fail during shutdown")
	}
}

func TestGracefulShutdownCompletesInflight(t *testing.T) {
	e := setup(t, nil)
	tok, _ := e.userToken(e.io)
	e.verification.mu.Lock()
	e.verification.delay = 300 * time.Millisecond
	e.verification.mu.Unlock()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	cfg := httpx.ServerConfig{Handler: e.gw.Handler, ShutdownTimeout: 5 * time.Second, BeforeShutdown: e.gw.Health().SetShuttingDown, Logger: logging.Discard()}
	done := make(chan error, 1)
	go func() { done <- httpx.Serve(ctx, cfg, httpx.NewServer(cfg), ln) }()
	result := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest("GET", "http://"+ln.Addr().String()+"/api/v1/verification/slow", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			result <- -1
			return
		}
		resp.Body.Close()
		result <- resp.StatusCode
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	if code := <-result; code != 200 {
		t.Fatalf("in-flight request dropped during shutdown: %d", code)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOpenAPIContractMatchesGatewayHandlers(t *testing.T) {
	e := setup(t, nil)
	var routes []testkit.Route
	err := chi.Walk(e.gw.Handler.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, testkit.Route{Method: method, Path: route})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRoutesMatchOpenAPI(t, "../../../../contracts/public/openapi/api-gateway.yaml", routes, "/health", "/ready", "/version")
}
