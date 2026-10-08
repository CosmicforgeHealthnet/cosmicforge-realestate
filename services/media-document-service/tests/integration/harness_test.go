package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/app"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/scanning"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/storage"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/migrations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// flakyScanner wraps the dev scanner and can fail on demand.
type flakyScanner struct {
	mu    sync.Mutex
	fails int
	calls int
}

func (f *flakyScanner) Name() string { return "test-scanner" }
func (f *flakyScanner) Scan(ctx context.Context, r io.Reader) (scanning.Verdict, error) {
	f.mu.Lock()
	f.calls++
	fail := f.fails > 0
	if fail {
		f.fails--
	}
	f.mu.Unlock()
	if fail {
		return scanning.Verdict{}, errors.New("scanner unavailable")
	}
	return scanning.DevScanner{}.Scan(ctx, r)
}

// toggleStorage can simulate an object-storage outage.
type toggleStorage struct {
	storage.Storage
	mu   sync.Mutex
	down bool
}

func (t *toggleStorage) setDown(v bool) { t.mu.Lock(); t.down = v; t.mu.Unlock() }
func (t *toggleStorage) isDown() bool   { t.mu.Lock(); defer t.mu.Unlock(); return t.down }
func (t *toggleStorage) HeadObject(ctx context.Context, k string) (storage.ObjectInfo, error) {
	if t.isDown() {
		return storage.ObjectInfo{}, errors.New("connection refused")
	}
	return t.Storage.HeadObject(ctx, k)
}
func (t *toggleStorage) DeleteObject(ctx context.Context, k string) error {
	if t.isDown() {
		return errors.New("connection refused")
	}
	return t.Storage.DeleteObject(ctx, k)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }

type harness struct {
	t       *testing.T
	db      *testkit.Database
	app     *app.App
	srv     *httptest.Server
	store   *toggleStorage
	scanner *flakyScanner
	signers map[platform.Service]*serviceauth.Signer
	clock   *clock
}

var callers = []platform.Service{
	platform.APIGateway, platform.IndividualOwnerService, platform.ArtisanService, platform.AdminService,
	platform.MarketplaceService, platform.VerificationService, platform.NotificationService,
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
	self := testkit.SigningKey(t, "md-1")
	selfSigner, _ := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.MediaDocumentService, Key: self})
	verifier, _ := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.MediaDocumentService, Registry: reg})

	backend := s3mem.New()
	_ = backend.CreateBucket("media")
	s3srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(s3srv.Close)
	s3, err := storage.NewS3(storage.S3Config{Endpoint: s3srv.URL, Region: "us-east-1", Bucket: "media", PathStyle: true,
		AccessKeyID: security.NewSecret("AK"), SecretKey: security.NewSecret("SK")})
	if err != nil {
		t.Fatal(err)
	}
	h.store = &toggleStorage{Storage: s3}
	h.scanner = &flakyScanner{}
	cfg := &config.Config{Environment: "test", DatabaseURL: security.NewSecret(h.db.URL), DBMaxConns: 10,
		Identity: &serviceauth.Identity{Signer: selfSigner, Verifier: verifier}, SubjectPrefix: "realestate",
		Policies: domain.DefaultPolicies(), UploadURLTTL: 15 * time.Minute, DownloadURLTTL: 5 * time.Minute, ScanLease: time.Minute, MaxScanTries: 3}
	h.app, err = app.Build(context.Background(), cfg, logging.Discard(), app.Overrides{Storage: h.store, Scanner: h.scanner, Now: h.clock.now})
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
	gateway      bool // send as X-Service-Authorization
	body         any
	key          string
}

type result struct {
	status int
	body   []byte
	header http.Header
}

func (r result) code() string {
	var env apperrors.Envelope
	_ = json.Unmarshal(r.body, &env)
	return env.Error.Code
}

func (r result) data(t *testing.T, v any) {
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

func (h *harness) do(c call) result {
	h.t.Helper()
	var rdr io.Reader
	if c.body != nil {
		b, _ := json.Marshal(c.body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(c.method, h.srv.URL+c.path, rdr)
	if c.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("Idempotency-Key", c.key)
	}
	if c.as != "" {
		d := serviceauth.Delegation{}
		if c.actor != nil {
			d = *c.actor
		}
		tok, err := h.signers[c.as].TokenFor(string(platform.MediaDocumentService), d)
		if err != nil {
			h.t.Fatal(err)
		}
		hdr := "Authorization"
		if c.gateway {
			hdr = serviceauth.HeaderServiceAuthorization
		}
		req.Header.Set(hdr, "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return result{status: resp.StatusCode, body: b, header: resp.Header}
}

type upload struct {
	Media struct {
		MediaID ids.ID `json:"media_id"`
		Status  string `json:"status"`
	} `json:"media"`
	Upload storage.SignedURL `json:"upload"`
}

type mediaView struct {
	MediaID              ids.ID  `json:"media_id"`
	Status               string  `json:"status"`
	ScanStatus           string  `json:"scan_status"`
	AccessClassification string  `json:"access_classification"`
	OwnerDomain          string  `json:"owner_domain"`
	RejectionReason      *string `json:"rejection_reason"`
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

var (
	pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 1000)...)
	pdfBytes = append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 1000)...)
)

// initiate starts an upload owned by individual-owner via the domain service.
func (h *harness) initiate(owner ids.ID, class, mime, filename string, content []byte, classification string, declaredSum string) (upload, result) {
	h.t.Helper()
	if declaredSum == "" {
		declaredSum = sum(content)
	}
	body := map[string]any{"owner": map[string]any{"owner_domain": "individual-owner", "owner_type": "USER", "owner_id": owner},
		"media_class": class, "filename": filename, "mime_type": mime, "size_bytes": len(content), "checksum_sha256": declaredSum}
	if classification != "" {
		body["access_classification"] = classification
	}
	if class == "DOCUMENT" || class == "VERIFICATION_DOCUMENT" {
		body["document_type"] = "ID_CARD"
	}
	r := h.do(call{method: "POST", path: "/internal/v1/media/uploads", as: platform.IndividualOwnerService, key: ids.New().String(), body: body})
	var u upload
	if r.status == 201 {
		r.data(h.t, &u)
	}
	return u, r
}

// put uploads bytes with the signed URL exactly as a client would.
func (h *harness) put(u upload, content []byte) int {
	h.t.Helper()
	req, _ := http.NewRequest(u.Upload.Method, u.Upload.URL, bytes.NewReader(content))
	for k, v := range u.Upload.Headers {
		req.Header.Set(k, v)
	}
	req.ContentLength = int64(len(content))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (h *harness) complete(id ids.ID, key string) result {
	return h.do(call{method: "POST", path: "/internal/v1/media/" + id.String() + "/complete", as: platform.IndividualOwnerService, key: key})
}

// ready performs the full upload → complete → scan flow and returns the id.
func (h *harness) ready(owner ids.ID, class, mime, filename string, content []byte, classification string) ids.ID {
	h.t.Helper()
	u, r := h.initiate(owner, class, mime, filename, content, classification, "")
	if r.status != 201 {
		h.t.Fatalf("initiate: %d %s", r.status, r.body)
	}
	if code := h.put(u, content); code != 200 {
		h.t.Fatalf("put: %d", code)
	}
	if r := h.complete(u.Media.MediaID, ids.New().String()); r.status != 200 {
		h.t.Fatalf("complete: %d %s", r.status, r.body)
	}
	h.scan()
	return u.Media.MediaID
}

func (h *harness) scan() {
	h.t.Helper()
	if _, err := h.app.Service.ScanDue(context.Background(), "test", 50); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) status(id ids.ID) (string, string) {
	var st, reason string
	var r *string
	if err := h.db.Pool.QueryRow(context.Background(), `SELECT status, rejection_reason FROM media_objects WHERE media_id=$1`, id).Scan(&st, &r); err != nil {
		h.t.Fatal(err)
	}
	if r != nil {
		reason = *r
	}
	return st, reason
}

func (h *harness) eventTypes(id ids.ID) []string {
	rows, _ := h.db.Pool.Query(context.Background(), `SELECT event_type FROM outbox_events WHERE aggregate_id=$1 ORDER BY occurred_at`, id)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func admin() *serviceauth.Delegation {
	return &serviceauth.Delegation{ActorDomain: platform.DomainAdmin, ActorID: ids.New()}
}
