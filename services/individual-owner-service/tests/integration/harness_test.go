package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/app"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/credentials"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/migrations"
	sharedauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/passwords"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// fakeNotify stands in for notification-service. It records requests
// (including one-time codes, which only this fake can observe) and can
// simulate outages and definite rejections.
type fakeNotify struct {
	mu       sync.Mutex
	sent     []notification.SendRequest
	keys     map[string]int
	mode     string // "" ok | "down" | "reject"
	accepted int
}

func (f *fakeNotify) Send(_ context.Context, req notification.SendRequest, key string) (notification.Accepted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch f.mode {
	case "down":
		return notification.Accepted{}, errors.New("dial tcp: connection refused")
	case "reject":
		return notification.Accepted{}, &httpx.RemoteError{Service: "notification-service", Status: 422, Code: "TEMPLATE_VARIABLES_INVALID"}
	}
	if f.keys == nil {
		f.keys = map[string]int{}
	}
	f.keys[key]++
	f.sent = append(f.sent, req)
	f.accepted++
	return notification.Accepted{NotificationID: ids.New()}, nil
}

func (f *fakeNotify) set(mode string) { f.mu.Lock(); f.mode = mode; f.mu.Unlock() }

// lastCode returns the most recent verification code sent to destination.
func (f *fakeNotify) lastCode(t *testing.T, destination string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.sent) - 1; i >= 0; i-- {
		r := f.sent[i]
		if r.TemplateKey == notification.TemplateVerificationCode && (r.Destinations.Email == destination || r.Destinations.Phone == destination) {
			return r.Variables["code"]
		}
	}
	t.Fatalf("no code sent to %s", destination)
	return ""
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type harness struct {
	verif   *fakeVerification
	media   *fakeMedia
	pay     *fakePayment
	t       *testing.T
	db      *testkit.Database
	app     *app.App
	srv     *httptest.Server
	notify  *fakeNotify
	clock   *clock
	signers map[platform.Service]*serviceauth.Signer
	userKey jwt.SigningKey
}

var callers = []platform.Service{platform.APIGateway, platform.AdminService, platform.MarketplaceService,
	platform.PropertyManagerService, platform.NotificationService, platform.ArtisanService, platform.ProfessionalServicesService,
	platform.BusinessOperationsService, platform.PaymentService}

func testConfig(t *testing.T, dbURL string, identity *serviceauth.Identity, userKey jwt.SigningKey) *config.Config {
	p := usecases.DefaultPolicy()
	p.CodeSendLimit = 20
	return &config.Config{
		Environment: "test", DatabaseURL: security.NewSecret(dbURL), DBMaxConns: 20, Identity: identity,
		TokenIssuer: string(platform.IndividualOwnerService), TokenAudience: sharedauth.DefaultAudience, AccessTokenTTL: 15 * time.Minute,
		SigningKey: userKey, RetiredKeys: map[string]string{}, SessionLifetime: 30 * 24 * time.Hour, RefreshIdleTTL: 30 * 24 * time.Hour,
		StepUpWindow: 5 * time.Minute, PasswordWorkers: 8, CodePepper: security.NewSecret(strings.Repeat("p", 32)),
		MFAKeyID: "mfa-1", MFAKey: bytes.Repeat([]byte{9}, 32), TOTPIssuer: "CosmicForge Owner", Policy: p,
		SessionRetention: 90 * 24 * time.Hour, NotificationServiceURL: "http://notification.invalid", NotificationTimeout: time.Second,
		SubjectPrefix: "realestate",
	}
}

func newHarness(t *testing.T, opts ...func(*harness)) *harness {
	t.Helper()
	h := &harness{t: t, notify: &fakeNotify{}, clock: &clock{t: time.Now().UTC().Truncate(time.Second)}, signers: map[platform.Service]*serviceauth.Signer{},
		verif: newFakeVerification(), media: newFakeMedia()}
	for _, o := range opts {
		o(h)
	}
	h.db = testkit.NewDatabase(t, migrations.FS)
	reg := serviceauth.NewRegistry()
	for _, s := range callers {
		k := testkit.SigningKey(t, string(s)+"-1")
		_ = reg.Add(s, k.KID, k.PublicKey())
		h.signers[s], _ = serviceauth.NewSigner(serviceauth.SignerConfig{Service: s, Key: k})
	}
	self := testkit.SigningKey(t, "io-svc-1")
	selfSigner, _ := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.IndividualOwnerService, Key: self})
	verifier, _ := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.IndividualOwnerService, Registry: reg})
	h.userKey = testkit.SigningKey(t, "io-user-1")
	cfg := testConfig(t, h.db.URL, &serviceauth.Identity{Signer: selfSigner, Verifier: verifier}, h.userKey)
	pw, _ := passwords.NewHasher(passwords.Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}, 8)
	var err error
	ov := app.Overrides{Notify: h.notify, Now: h.clock.now, Passwords: pw, Verification: h.verif, Media: h.media}
	if h.pay != nil {
		ov.Payment = h.pay
	}
	h.app, err = app.Build(context.Background(), cfg, logging.Discard(), ov)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.app.Close)
	h.srv = httptest.NewServer(h.app.Router)
	t.Cleanup(h.srv.Close)
	return h
}

type call struct {
	method, path string
	body         any
	rawBody      []byte
	token        string // owner access token (Authorization)
	// gateway emulation (public routes): when noGateway is false the
	// harness adds the api-gateway service token with the actor taken from
	// token's subject, exactly as the gateway does after verifying it.
	noGateway   bool
	gatewayAs   platform.Service // default api-gateway
	actorDomain platform.Domain  // override asserted actor (forgery tests)
	actorID     ids.ID
	// internal routes
	as       platform.Service
	delegate *serviceauth.Delegation
	key      string
	headers  map[string]string
}

type result struct {
	status int
	body   []byte
	header http.Header
}

func (r result) data(t *testing.T, v any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(r.body, &env); err != nil || env.Data == nil {
		t.Fatalf("no data envelope (%d): %s", r.status, r.body)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatal(err)
	}
}

func (r result) code() string {
	var env apperrors.Envelope
	_ = json.Unmarshal(r.body, &env)
	return env.Error.Code
}

func tokenSubject(tok string) ids.ID {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return ids.Nil
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ids.Nil
	}
	var c struct {
		Sub string `json:"sub"`
	}
	_ = json.Unmarshal(b, &c)
	id, _ := ids.ParseV7(c.Sub)
	return id
}

func (h *harness) do(c call) result {
	h.t.Helper()
	var rdr io.Reader
	switch {
	case c.rawBody != nil:
		rdr = bytes.NewReader(c.rawBody)
	case c.body != nil:
		b, _ := json.Marshal(c.body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(c.method, h.srv.URL+c.path, rdr)
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("Idempotency-Key", c.key)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if strings.HasPrefix(c.path, "/internal/") {
		if c.as != "" {
			d := serviceauth.Delegation{}
			if c.delegate != nil {
				d = *c.delegate
			}
			tok, _ := h.signers[c.as].TokenFor(string(platform.IndividualOwnerService), d)
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	} else {
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		if !c.noGateway {
			as := c.gatewayAs
			if as == "" {
				as = platform.APIGateway
			}
			d := serviceauth.Delegation{}
			if c.token != "" {
				d.ActorDomain, d.ActorID = platform.DomainIndividualOwner, tokenSubject(c.token)
			}
			if c.actorDomain != "" {
				d.ActorDomain, d.ActorID = c.actorDomain, c.actorID
			}
			if d.ActorID.IsZero() {
				d.ActorDomain = ""
			}
			tok, _ := h.signers[as].TokenFor(string(platform.IndividualOwnerService), d)
			req.Header.Set("X-Service-Authorization", "Bearer "+tok)
			req.Header.Set("X-Forwarded-For", "203.0.113.7")
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return result{status: resp.StatusCode, body: b, header: resp.Header}
}

func (h *harness) expect(r result, status int, code string) {
	h.t.Helper()
	if r.status != status || (code != "" && r.code() != code) {
		h.t.Fatalf("want %d %s, got %d %s", status, code, r.status, r.body)
	}
}

// owner is a registered owner with an authenticated session.
type owner struct {
	id       ids.ID
	email    string
	password string
	access   string
	refresh  string
	session  ids.ID
	device   string
}

type authResult struct {
	Status            string                     `json:"status"`
	Account           *usecases.AccountView      `json:"account"`
	Tokens            *usecases.TokenPair        `json:"tokens"`
	MFA               *usecases.MFAChallengeView `json:"mfa"`
	EmailVerification *usecases.ChallengeView    `json:"email_verification"`
}

func (o *owner) apply(a authResult) {
	o.access, o.refresh, o.session = a.Tokens.AccessToken, a.Tokens.RefreshToken, a.Tokens.SessionID
}

// register creates and (optionally) activates an owner.
func (h *harness) register(activate bool) *owner {
	h.t.Helper()
	o := &owner{email: "owner-" + ids.New().String()[24:] + "@example.com", password: "correct horse battery", device: "device-" + ids.New().String()}
	r := h.do(call{method: "POST", path: "/api/v1/owner/auth/register", headers: map[string]string{"X-Device-ID": o.device, "X-Device-Platform": "ios"},
		body: map[string]any{"email": strings.ToUpper(o.email), "password": o.password, "display_name": "Ada Owner"}})
	h.expect(r, 201, "")
	var a authResult
	r.data(h.t, &a)
	o.id = a.Account.AccountID
	o.apply(a)
	if activate {
		code := h.notify.lastCode(h.t, o.email)
		r = h.do(call{method: "POST", path: "/api/v1/owner/me/email/verification/confirm", token: o.access,
			body: map[string]any{"challenge_id": a.EmailVerification.ChallengeID, "code": code}})
		h.expect(r, 200, "")
	}
	return o
}

func (h *harness) login(o *owner, device string) (result, authResult) {
	h.t.Helper()
	hdr := map[string]string{}
	if device != "" {
		hdr["X-Device-ID"] = device
	}
	r := h.do(call{method: "POST", path: "/api/v1/owner/auth/login", headers: hdr, body: map[string]any{"email": o.email, "password": o.password}})
	var a authResult
	if r.status == 200 {
		r.data(h.t, &a)
	}
	return r, a
}

func (h *harness) status(id ids.ID) string {
	var s string
	if err := h.db.Pool.QueryRow(context.Background(), `SELECT status FROM owner_accounts WHERE account_id=$1`, id).Scan(&s); err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *harness) count(sql string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) totpSecret(o *owner) []byte {
	h.t.Helper()
	var id ids.ID
	var ct []byte
	var kid string
	if err := h.db.Pool.QueryRow(context.Background(), `SELECT credential_id, secret_ciphertext, key_id FROM mfa_totp_credentials
		WHERE account_id=$1 AND status IN ('PENDING','ACTIVE') ORDER BY created_at DESC LIMIT 1`, o.id).Scan(&id, &ct, &kid); err != nil {
		h.t.Fatal(err)
	}
	s, _ := credentials.NewSealer("mfa-1", bytes.Repeat([]byte{9}, 32), nil)
	secret, err := s.Open(kid, ct, []byte("owner-totp:"+o.id.String()+":"+id.String()))
	if err != nil {
		h.t.Fatal(err)
	}
	return secret
}

func adminActor() *serviceauth.Delegation {
	return &serviceauth.Delegation{ActorDomain: platform.DomainAdmin, ActorID: ids.New()}
}
