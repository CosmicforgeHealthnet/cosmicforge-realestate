package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/app"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/integrations/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/migrations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// fakeMedia is a media-document-service stand-in that verifies the caller's
// service JWT exactly as the real service will.
type fakeMedia struct {
	mu       sync.Mutex
	items    map[ids.ID]media.Info
	verifier *serviceauth.Verifier
	calls    int
	down     bool
}

func (f *fakeMedia) put(info media.Info) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[info.MediaID] = info
}

func (f *fakeMedia) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	down := f.down
	f.mu.Unlock()
	if down {
		httpx.WriteError(w, r, apperrors.Unavailable(""))
		return
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	p, err := f.verifier.Verify(r.Context(), tok)
	if err != nil || p.Service != platform.VerificationService {
		httpx.WriteError(w, r, apperrors.InvalidToken())
		return
	}
	id, err := ids.ParseV7(strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/download-url"), "/internal/v1/media/"))
	f.mu.Lock()
	info, ok := f.items[id]
	f.mu.Unlock()
	if err != nil || !ok {
		httpx.WriteError(w, r, apperrors.NotFound("MEDIA_NOT_FOUND", "Media not found."))
		return
	}
	if strings.HasSuffix(r.URL.Path, "/download-url") {
		httpx.WriteData(w, 200, map[string]string{"url": "https://storage.example/signed"})
		return
	}
	httpx.WriteData(w, 200, info)
}

// fakeProvider is a controllable provider for timeout/unknown/read-back tests.
type fakeProvider struct {
	mu       sync.Mutex
	name     string
	submitFn func(req providers.SubmitRequest, call int) (providers.SubmitResult, error)
	statusFn func(ref string) (providers.StatusResult, error)
	submits  map[string]int
	reads    int
}

func (p *fakeProvider) Name() string              { return p.name }
func (p *fakeProvider) Supports(domain.Type) bool { return true }
func (p *fakeProvider) Submit(ctx context.Context, req providers.SubmitRequest) (providers.SubmitResult, error) {
	p.mu.Lock()
	p.submits[req.ClientReference]++
	n := p.submits[req.ClientReference]
	fn := p.submitFn
	p.mu.Unlock()
	return fn(req, n)
}
func (p *fakeProvider) ReadStatus(ctx context.Context, ref string) (providers.StatusResult, error) {
	p.mu.Lock()
	p.reads++
	fn := p.statusFn
	p.mu.Unlock()
	return fn(ref)
}
func (p *fakeProvider) VerifyCallback(context.Context, http.Header, []byte) (providers.CallbackEvent, error) {
	return providers.CallbackEvent{}, providers.ErrInvalidCallback
}
func (p *fakeProvider) FetchResult(ctx context.Context, ref string) (providers.StatusResult, error) {
	return providers.StatusResult{}, errors.New("unsupported")
}

type harness struct {
	t       *testing.T
	db      *testkit.Database
	app     *app.App
	srv     *httptest.Server
	media   *fakeMedia
	sandbox *providers.Sandbox
	fake    *fakeProvider
	signers map[platform.Service]*serviceauth.Signer
	clock   *clock
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

var callers = []platform.Service{
	platform.APIGateway, platform.IndividualOwnerService, platform.ArtisanService, platform.AdminService,
	platform.MarketplaceService, platform.PropertyManagerService, platform.NotificationService, platform.BusinessOperationsService,
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, signers: map[platform.Service]*serviceauth.Signer{}, clock: &clock{t: time.Now().UTC()}}
	h.db = testkit.NewDatabase(t, migrations.FS)
	reg := serviceauth.NewRegistry()
	for _, s := range callers {
		k := testkit.SigningKey(t, string(s)+"-1")
		_ = reg.Add(s, k.KID, k.PublicKey())
		h.signers[s], _ = serviceauth.NewSigner(serviceauth.SignerConfig{Service: s, Key: k})
	}
	selfKey := testkit.SigningKey(t, "vs-1")
	selfSigner, _ := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.VerificationService, Key: selfKey})
	verifier, _ := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.VerificationService, Registry: reg})

	mreg := serviceauth.NewRegistry()
	_ = mreg.Add(platform.VerificationService, "vs-1", selfKey.PublicKey())
	mver, _ := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.MediaDocumentService, Registry: mreg})
	h.media = &fakeMedia{items: map[ids.ID]media.Info{}, verifier: mver}
	mediaSrv := httptest.NewServer(h.media)
	t.Cleanup(mediaSrv.Close)

	h.sandbox = providers.NewSandbox("sandbox-secret")
	h.fake = &fakeProvider{name: "fake-kyc", submits: map[string]int{},
		submitFn: func(providers.SubmitRequest, int) (providers.SubmitResult, error) {
			return providers.SubmitResult{ProviderReference: "fk_1", Outcome: providers.OutcomeVerified, EventID: "fk_evt"}, nil
		},
		statusFn: func(string) (providers.StatusResult, error) { return providers.StatusResult{Found: false}, nil },
	}
	cfg := &config.Config{
		Environment: "test", DatabaseURL: security.NewSecret(h.db.URL), DBMaxConns: 10,
		Identity:        &serviceauth.Identity{Signer: selfSigner, Verifier: verifier},
		MediaServiceURL: mediaSrv.URL, MediaTimeout: 2 * time.Second, SubjectPrefix: "realestate",
		Providers: []string{providers.SandboxName, "fake-kyc"}, DefaultProvider: providers.SandboxName,
		Validity: domain.DefaultValidity, ProviderTimeout: 2 * time.Second, MaxSubmitTries: 4, MaxPollTries: 5, PollInterval: time.Second,
	}
	mc, _ := media.New(mediaSrv.URL, selfSigner, 2*time.Second)
	var err error
	h.app, err = app.Build(context.Background(), cfg, logging.Discard(), app.Overrides{
		Providers: []providers.Provider{h.sandbox, h.fake}, Media: mc, Now: h.clock.now,
	})
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
	as           platform.Service
	actor        *serviceauth.Delegation
	header       string // Authorization or X-Service-Authorization
	body         any
	rawBody      []byte
	key          string
	rawToken     string
	extra        map[string]string
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

func (h *harness) do(c call) result {
	h.t.Helper()
	var rdr io.Reader
	if c.body != nil {
		b, _ := json.Marshal(c.body)
		rdr = bytes.NewReader(b)
	}
	if c.rawBody != nil {
		rdr = bytes.NewReader(c.rawBody)
	}
	req, _ := http.NewRequest(c.method, h.srv.URL+c.path, rdr)
	if c.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("Idempotency-Key", c.key)
	}
	hdr := c.header
	if hdr == "" {
		hdr = "Authorization"
	}
	tok := c.rawToken
	if tok == "" && c.as != "" {
		d := serviceauth.Delegation{OriginalRequestID: "orig-req"}
		if c.actor != nil {
			d = *c.actor
		}
		var err error
		tok, err = h.signers[c.as].TokenFor(string(platform.VerificationService), d)
		if err != nil {
			h.t.Fatal(err)
		}
	}
	if tok != "" {
		req.Header.Set(hdr, "Bearer "+tok)
	}
	for k, v := range c.extra {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return result{status: resp.StatusCode, body: b, header: resp.Header}
}

// newMedia registers a READY media owned by domain d.
func (h *harness) newMedia(d platform.Domain, status string) ids.ID {
	id := ids.New()
	h.media.put(media.Info{MediaID: id, OwnerDomain: string(d), OwnerType: "USER", OwnerID: ids.New(), Status: status,
		MimeType: "image/jpeg", SizeBytes: 1000, ChecksumSHA256: strings.Repeat("a", 64), AccessClassification: "ADMIN_RESTRICTED"})
	return id
}

type caseView struct {
	VerificationID ids.ID `json:"verification_id"`
	Status         string `json:"status"`
	Provider       string `json:"provider"`
	ExpiresAt      *time.Time
	Subject        struct {
		Type   string `json:"subject_type"`
		Domain string `json:"subject_domain"`
		ID     ids.ID `json:"subject_id"`
	} `json:"subject"`
	SupersedesVerificationID *ids.ID `json:"supersedes_verification_id"`
}

// createReady creates a case for an individual-owner user with READY evidence
// and returns its ID. meta sets provider metadata.
func (h *harness) createReady(subject ids.ID, provider string, meta map[string]string) ids.ID {
	h.t.Helper()
	res := h.do(call{method: "POST", path: "/internal/v1/verifications", as: platform.IndividualOwnerService, key: ids.New().String(),
		body: map[string]any{"verification_type": "IDENTITY", "provider": provider, "provider_metadata": meta,
			"subject": map[string]any{"subject_type": "USER", "subject_domain": "individual-owner", "subject_id": subject}}})
	if res.status != 201 {
		h.t.Fatalf("create: %d %s", res.status, res.body)
	}
	var v caseView
	res.data(h.t, &v)
	mid := h.newMedia(platform.DomainIndividualOwner, "READY")
	res = h.do(call{method: "POST", path: "/internal/v1/verifications/" + v.VerificationID.String() + "/evidence", as: platform.IndividualOwnerService,
		key: ids.New().String(), body: map[string]any{"media_id": mid, "evidence_type": "ID_DOCUMENT_FRONT"}})
	if res.status != 201 {
		h.t.Fatalf("evidence: %d %s", res.status, res.body)
	}
	return v.VerificationID
}

func (h *harness) submitAndProcess(id ids.ID) {
	h.t.Helper()
	for _, step := range []string{"submit", "process"} {
		res := h.do(call{method: "POST", path: "/internal/v1/verifications/" + id.String() + "/" + step, as: platform.IndividualOwnerService, key: ids.New().String()})
		if res.status >= 300 {
			h.t.Fatalf("%s: %d %s", step, res.status, res.body)
		}
	}
}

func (h *harness) status(id ids.ID) string {
	var s string
	if err := h.db.Pool.QueryRow(context.Background(), `SELECT status FROM verification_cases WHERE verification_id=$1`, id).Scan(&s); err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *harness) dispatch() {
	h.t.Helper()
	if _, err := h.app.Service.DispatchDue(context.Background(), "test-worker", 50); err != nil {
		h.t.Fatal(err)
	}
}

// dispatchUntil advances the clock and dispatches until cond holds.
func (h *harness) dispatchUntil(cond func() bool) {
	h.t.Helper()
	for i := 0; i < 30; i++ {
		h.dispatch()
		if cond() {
			return
		}
		h.clock.advance(2 * time.Hour)
	}
	h.t.Fatal("condition not reached by dispatching")
}

func (h *harness) eventTypes(id ids.ID) []string {
	rows, err := h.db.Pool.Query(context.Background(), `SELECT event_type FROM outbox_events WHERE aggregate_id=$1 ORDER BY occurred_at, event_type`, id)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func adminActor() *serviceauth.Delegation {
	return &serviceauth.Delegation{ActorDomain: platform.DomainAdmin, ActorID: ids.New(), OriginalRequestID: "admin-req"}
}
