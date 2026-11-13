// Package domaintest runs a domainkit service against a real disposable
// PostgreSQL database for service-level integration tests. FakeBusiness is
// a recording contract double of the Business partner API: it is used only
// in service-level tests; cross-service certification uses real processes.
package domaintest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// Kit is a running service under test.
type Kit struct {
	T        *testing.T
	RT       *domainkit.Runtime
	App      domainkit.App
	Server   *httptest.Server
	Business *FakeBusiness
	signers  map[platform.Service]*serviceauth.Signer
}

// New migrates a fresh database with migrations and starts the service.
func New(t *testing.T, svc platform.Service, migrations fs.FS, build func(*domainkit.Runtime) domainkit.App) *Kit {
	t.Helper()
	db := testkit.NewDatabase(t, migrations)
	registry := serviceauth.NewRegistry()
	signers := map[platform.Service]*serviceauth.Signer{}
	for _, s := range []platform.Service{platform.APIGateway, platform.BusinessOperationsService, platform.AdminService, platform.MarketplaceService, platform.ArtisanService, svc} {
		if signers[s] != nil {
			continue
		}
		key := testkit.SigningKey(t, string(s)+"-test")
		if err := registry.Add(s, key.KID, key.PublicKey()); err != nil {
			t.Fatal(err)
		}
		signers[s], _ = serviceauth.NewSigner(serviceauth.SignerConfig{Service: s, Key: key})
	}
	verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: svc, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	fb := &FakeBusiness{t: t, verifier: mustVerifier(t, registry)}
	fb.server = httptest.NewServer(fb)
	t.Cleanup(fb.server.Close)
	domain, _ := platform.DomainForService(svc)
	cfg := &domainkit.Config{Service: svc, Domain: domain, Area: domainkit.Areas[domain], Environment: "test", DatabaseURL: security.NewSecret(db.URL), DBMaxConns: 8,
		Identity: &serviceauth.Identity{Verifier: verifier, Signer: signers[svc]}, TokenKey: testkit.SigningKey(t, "user-token"), AccessTTL: 15 * time.Minute, SessionTTL: 24 * time.Hour,
		SubjectPrefix: "realestate", BusinessURL: fb.server.URL, BootstrapRoles: map[string][]string{"supervisor@admin.test": {"SUPERVISOR"}}}
	rt, err := domainkit.Open(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	// Registered after Close so it runs first (cleanups are LIFO): every
	// event the service produced must match its published contract schema.
	t.Cleanup(func() { validateStoredEvents(t, rt) })
	app := build(rt)
	srv := httptest.NewServer(rt.Handler(app))
	t.Cleanup(srv.Close)
	return &Kit{T: t, RT: rt, App: app, Server: srv, Business: fb, signers: signers}
}

func mustVerifier(t *testing.T, registry *serviceauth.Registry) *serviceauth.Verifier {
	v, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.BusinessOperationsService, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Response is a decoded HTTP response.
type Response struct {
	Status int
	Body   map[string]any
	Raw    []byte
}

// Data returns the "data" object.
func (r Response) Data() map[string]any {
	d, _ := r.Body["data"].(map[string]any)
	return d
}

// Items returns data.items.
func (r Response) Items() []any {
	items, _ := r.Data()["items"].([]any)
	return items
}

func (k *Kit) do(method, path, header, token, key string, body any, extra map[string]string) Response {
	k.T.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, k.Server.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	req.Header.Set(header, "Bearer "+token)
	for h, v := range extra {
		req.Header.Set(h, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		k.T.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return Response{Status: res.StatusCode, Body: out, Raw: raw}
}

// Actor calls an actor route as the gateway delegating account (zero = no actor).
func (k *Kit) Actor(account ids.ID, method, path, key string, body any) Response {
	k.T.Helper()
	return k.ActorWithHeaders(account, method, path, key, body, nil)
}

// ActorWithHeaders is Actor with extra request headers (e.g. step-up codes).
func (k *Kit) ActorWithHeaders(account ids.ID, method, path, key string, body any, headers map[string]string) Response {
	k.T.Helper()
	d := serviceauth.Delegation{}
	if !account.IsZero() {
		d.ActorDomain, d.ActorID = k.RT.Cfg.Domain, account
	}
	tok, err := k.signers[platform.APIGateway].TokenFor(string(k.RT.Cfg.Service), d)
	if err != nil {
		k.T.Fatal(err)
	}
	return k.do(method, "/api/v1/"+string(k.RT.Cfg.Domain)+path, serviceauth.HeaderServiceAuthorization, tok, key, body, headers)
}

// Service calls an internal route as caller with an organisation delegation.
func (k *Kit) Service(caller platform.Service, org ids.ID, method, path, key string, body any) Response {
	k.T.Helper()
	signer := k.signers[caller]
	if signer == nil {
		k.T.Fatalf("no signer for %s", caller)
	}
	tok, err := signer.TokenFor(string(k.RT.Cfg.Service), serviceauth.Delegation{OrganizationID: org})
	if err != nil {
		k.T.Fatal(err)
	}
	return k.do(method, "/internal/v1"+path, serviceauth.HeaderAuthorization, tok, key, body, nil)
}

// Account registers and returns an account id.
func (k *Kit) Account(email string) ids.ID {
	k.T.Helper()
	res := k.Actor(ids.Nil, "POST", "/auth/register", "reg-"+email, map[string]any{"email": email, "password": "correct horse battery", "display_name": "Test " + strings.Split(email, "@")[0]})
	if res.Status != 201 {
		k.T.Fatalf("register %s %d %s", email, res.Status, res.Raw)
	}
	id, err := ids.Parse(res.Data()["id"].(string))
	if err != nil {
		k.T.Fatal(err)
	}
	return id
}

// Call is one recorded Business call.
type Call struct {
	Method, Path, Key string
	Org               ids.ID
	Caller            platform.Service
	Body              map[string]any
}

// FakeBusiness records signed partner calls and answers with Reply.
type FakeBusiness struct {
	t        *testing.T
	verifier *serviceauth.Verifier
	server   *httptest.Server
	mu       sync.Mutex
	calls    []Call
	// Reply returns status and data for a call (default 200, echo).
	Reply func(c Call) (int, any)
}

// Calls returns recorded calls.
func (f *FakeBusiness) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

func (f *FakeBusiness) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	p, err := f.verifier.Verify(r.Context(), tok)
	if err != nil {
		w.WriteHeader(401)
		return
	}
	body := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	c := Call{Method: r.Method, Path: r.URL.Path, Key: r.Header.Get("Idempotency-Key"), Org: p.Delegation.OrganizationID, Caller: p.Service, Body: body}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	reply := f.Reply
	f.mu.Unlock()
	status, data := 200, any(map[string]any{"domain": "", "record": map[string]any{"state": "OK"}})
	if reply != nil {
		status, data = reply(c)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status >= 400 {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "BUSINESS_REJECTED", "message": "rejected"}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// eventDirs maps a domain area to its contracts/events directory.
var eventDirs = map[string]string{"marketplace": "marketplace", "property_manager": "property-managers", "artisan": "artisans", "professional": "professionals", "admin": "admin"}

func validateStoredEvents(t *testing.T, rt *domainkit.Runtime) {
	t.Helper()
	dir, ok := eventDirs[rt.Cfg.Area]
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := rt.Pool.Query(ctx, `SELECT event_type,jsonb_strip_nulls(jsonb_build_object('event_id',event_id,'event_type',event_type,'producer',producer,'aggregate_type',aggregate_type,'aggregate_id',aggregate_id,'occurred_at',occurred_at,'trace_id',trace_id,'actor',CASE WHEN actor_id IS NOT NULL THEN jsonb_build_object('domain',actor_domain,'id',actor_id) END,'metadata',metadata,'payload',payload)) FROM outbox_events ORDER BY occurred_at,event_id`)
	if err != nil {
		t.Errorf("stored events: %v", err)
		return
	}
	type stored struct {
		typ string
		raw []byte
	}
	var list []stored
	for rows.Next() {
		var e stored
		if err := rows.Scan(&e.typ, &e.raw); err != nil {
			rows.Close()
			t.Errorf("stored events: %v", err)
			return
		}
		list = append(list, e)
	}
	rows.Close()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	for _, e := range list {
		testkit.ValidateJSONSchema(t, filepath.Join(root, "contracts/events", dir, e.typ+".schema.json"), e.raw)
	}
}
