package domainkit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// Kit is a domainkit runtime behind an httptest server with a gateway signer.
type Kit struct {
	t       *testing.T
	RT      *domainkit.Runtime
	Server  *httptest.Server
	Gateway *serviceauth.Signer
	Signers map[platform.Service]*serviceauth.Signer
}

// newKit builds a runtime for svc with extra migrations and mounts routes.
func newKit(t *testing.T, svc platform.Service, actor func(r chi.Router)) *Kit {
	t.Helper()
	db := testkit.NewDatabase(t, fstest.MapFS{"00001_platform.sql": &fstest.MapFile{Data: []byte(domainkit.BaseMigration)}})
	registry := serviceauth.NewRegistry()
	signers := map[platform.Service]*serviceauth.Signer{}
	for _, s := range []platform.Service{platform.APIGateway, platform.BusinessOperationsService} {
		key := testkit.SigningKey(t, string(s)+"-k")
		if err := registry.Add(s, key.KID, key.PublicKey()); err != nil {
			t.Fatal(err)
		}
		signers[s], _ = serviceauth.NewSigner(serviceauth.SignerConfig{Service: s, Key: key})
	}
	verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: svc, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	domain, _ := platform.DomainForService(svc)
	cfg := &domainkit.Config{Service: svc, Domain: domain, Area: domainkit.Areas[domain], Environment: "test", DatabaseURL: security.NewSecret(db.URL), DBMaxConns: 5,
		Identity: &serviceauth.Identity{Verifier: verifier}, TokenKey: testkit.SigningKey(t, "user-k"), AccessTTL: 15 * time.Minute, SessionTTL: 24 * time.Hour, SubjectPrefix: "realestate"}
	rt, err := domainkit.Open(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	r := rt.Router()
	rt.ActorRoutes(r, actor)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &Kit{t: t, RT: rt, Server: srv, Gateway: signers[platform.APIGateway], Signers: signers}
}

func (k *Kit) call(actor ids.ID, domain platform.Domain, method, path, key string, body any) (int, map[string]any) {
	k.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, k.Server.URL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	d := serviceauth.Delegation{}
	if !actor.IsZero() {
		d.ActorDomain, d.ActorID = domain, actor
	}
	tok, err := k.Gateway.TokenFor(string(k.RT.Cfg.Service), d)
	if err != nil {
		k.t.Fatal(err)
	}
	req.Header.Set(serviceauth.HeaderServiceAuthorization, "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		k.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return res.StatusCode, out
}

func TestDomainKitAccountsActorAuthAndIdempotentCommands(t *testing.T) {
	var k *Kit
	k = newKit(t, platform.ArtisanService, func(r chi.Router) {
		r.With(domainkit.RequireRole("ARTISAN")).Post("/notes", func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Text string `json:"text"`
			}
			k.RT.Command(w, r, "notes.create", &in, func(ctx context.Context, tx pgx.Tx) (domainkit.Change, error) {
				return domainkit.Change{Status: 201, Body: map[string]any{"id": ids.New(), "text": in.Text}}, nil
			})
		})
	})
	k.RT.Accounts.DefaultRoles = []string{"ARTISAN"}
	base := "/api/v1/artisan"
	status, reg := k.call(ids.Nil, "", "POST", base+"/auth/register", "reg-1", map[string]any{"email": "Ada@Example.test", "password": "correct horse battery", "display_name": "Ada Artisan"})
	if status != 201 {
		t.Fatalf("register %d %v", status, reg)
	}
	if status, _ = k.call(ids.Nil, "", "POST", base+"/auth/register", "reg-2", map[string]any{"email": "ada@example.test", "password": "correct horse battery", "display_name": "Ada"}); status != 409 {
		t.Fatalf("duplicate %d", status)
	}
	status, login := k.call(ids.Nil, "", "POST", base+"/auth/login", "", map[string]any{"email": "ada@example.test", "password": "correct horse battery"})
	if status != 200 {
		t.Fatalf("login %d %v", status, login)
	}
	data := login["data"].(map[string]any)
	id, _ := ids.Parse(data["account_id"].(string))
	if status, _ = k.call(ids.Nil, "", "POST", base+"/auth/login", "", map[string]any{"email": "ada@example.test", "password": "wrong password here"}); status != 401 {
		t.Fatalf("bad password %d", status)
	}
	refresh := data["refresh_token"].(string)
	if status, _ = k.call(ids.Nil, "", "POST", base+"/auth/refresh", "", map[string]any{"refresh_token": refresh}); status != 200 {
		t.Fatalf("refresh %d", status)
	}
	if status, _ = k.call(ids.Nil, "", "POST", base+"/auth/refresh", "", map[string]any{"refresh_token": refresh}); status != 401 {
		t.Fatalf("refresh token reuse %d", status)
	}
	if status, me := k.call(id, platform.DomainArtisan, "GET", base+"/me", "", nil); status != 200 || me["data"].(map[string]any)["email"] != "ada@example.test" {
		t.Fatalf("me %d %v", status, me)
	}
	// Another domain's actor is never accepted.
	if status, _ = k.call(id, platform.DomainMarketplace, "GET", base+"/me", "", nil); status != 401 {
		t.Fatalf("cross-domain actor %d", status)
	}
	s1, n1 := k.call(id, platform.DomainArtisan, "POST", base+"/notes", "note-1", map[string]any{"text": "hello"})
	s2, n2 := k.call(id, platform.DomainArtisan, "POST", base+"/notes", "note-1", map[string]any{"text": "hello"})
	if s1 != 201 || s2 != 201 || n1["data"].(map[string]any)["id"] != n2["data"].(map[string]any)["id"] {
		t.Fatalf("idempotent replay %d %d %v %v", s1, s2, n1, n2)
	}
	if status, _ = k.call(id, platform.DomainArtisan, "POST", base+"/notes", "note-1", map[string]any{"text": "different"}); status != 409 {
		t.Fatalf("key reuse with different body %d", status)
	}
	if err := database.WithTx(t.Context(), k.RT.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		return k.RT.Accounts.SetStatus(ctx, tx, id, "SUSPENDED")
	}); err != nil {
		t.Fatal(err)
	}
	if status, _ = k.call(id, platform.DomainArtisan, "GET", base+"/me", "", nil); status != 403 {
		t.Fatalf("suspended account %d", status)
	}
	var audits int
	if err := k.RT.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='account.registered'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit %d %v", audits, err)
	}
	if _, err := k.RT.Pool.Exec(t.Context(), `DELETE FROM audit_events`); err == nil {
		t.Fatal("audit deleted")
	}
}
