package integration

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/admin-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/admin-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit/domaintest"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/totp"
)

// accountOwner is a recording contract double of a domain service's
// internal account-status endpoint (service-level test only).
type accountOwner struct {
	mu    sync.Mutex
	calls []map[string]any
}

func (a *accountOwner) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	body["path"], body["authorized"] = r.URL.Path, strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
	a.mu.Lock()
	a.calls = append(a.calls, body)
	a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"status": body["status"]}})
}

func (a *accountOwner) last() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.calls) == 0 {
		return nil
	}
	return a.calls[len(a.calls)-1]
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

// code advances the clock one TOTP step and returns the current code.
func (c *clock) code(secret []byte) string {
	c.mu.Lock()
	c.now = c.now.Add(31 * time.Second)
	n := c.now
	c.mu.Unlock()
	return totp.Code(secret, totp.Step(n))
}

func TestAdminMakerCheckerDisputeRestrictionAppealAndRemedy(t *testing.T) {
	owner := &accountOwner{}
	os := httptest.NewServer(owner)
	t.Cleanup(os.Close)
	sealer, _ := totp.NewSealer("mfa-test", []byte("0123456789abcdef0123456789abcdef"))
	clk := &clock{now: time.Now().UTC()}
	k := domaintest.New(t, platform.AdminService, migrations.FS, func(rt *domainkit.Runtime) domainkit.App {
		rt.Now = clk.Now
		c, err := httpx.NewClient(httpx.ClientConfig{BaseURL: os.URL, Audience: string(platform.ArtisanService), Tokens: rt.Cfg.Identity.Signer, MaxAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		return usecases.NewWith(rt, usecases.Deps{Sealer: sealer, Subjects: map[platform.Service]*httpx.Client{platform.ArtisanService: c}})
	})
	sup := k.Account("supervisor@admin.test")
	rev := k.Account("reviewer@admin.test")
	if r := k.Actor(rev, "GET", "/cases", "", nil); r.Status != 403 {
		t.Fatalf("self-registered account must hold no admin role: %d", r.Status)
	}
	// Supervisor enrols TOTP; privileged actions require it.
	if r := k.Actor(sup, "POST", "/accounts/"+rev.String()+"/roles", "g0", map[string]any{"roles": []string{"REVIEWER"}, "reason": "Onboard reviewer"}); r.Status != 403 {
		t.Fatalf("grant without MFA %d %s", r.Status, r.Raw)
	}
	en := k.Actor(sup, "POST", "/mfa/enrol", "", nil)
	if en.Status != 201 {
		t.Fatalf("enrol %d %s", en.Status, en.Raw)
	}
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(en.Data()["secret"].(string))
	if r := k.Actor(sup, "POST", "/mfa/confirm", "", map[string]any{"code": clk.code(secret)}); r.Status != 200 {
		t.Fatalf("confirm %d %s", r.Status, r.Raw)
	}
	if r := k.Actor(sup, "POST", "/mfa/enrol", "", nil); r.Status != 409 {
		t.Fatalf("re-enrol over confirmed factor %d", r.Status)
	}
	stepUp := func(account ids.ID, method, path, key string, body any, code string) domaintest.Response {
		return k.ActorWithHeaders(account, method, path, key, body, map[string]string{usecases.HeaderMFA: code})
	}
	used := clk.code(secret)
	if r := stepUp(sup, "POST", "/accounts/"+rev.String()+"/roles", "g1", map[string]any{"roles": []string{"REVIEWER"}, "reason": "Onboard reviewer"}, used); r.Status != 200 {
		t.Fatalf("grant %d %s", r.Status, r.Raw)
	}
	if r := stepUp(sup, "POST", "/accounts/"+rev.String()+"/roles", "g2", map[string]any{"roles": []string{"REVIEWER"}, "reason": "Replay code"}, used); r.Status != 403 {
		t.Fatalf("TOTP replay accepted %d", r.Status)
	}

	// Business submits a dispute for platform review.
	org, dispute := ids.New(), ids.New()
	deliver := func(state string, version int64) {
		b := domainkit.BusinessEvent{OrganisationID: org, TargetID: dispute, Operation: state, State: state, Version: version, Snapshot: domainkit.BusinessDetail{Domain: "dispute", Kind: "DEPOSIT", Title: "Deposit dispute", Attributes: map[string]any{"claim_summary": "Deduction contested", "requested_remedy_minor": 50000.0}}}
		env, _ := events.New(context.Background(), platform.BusinessOperationsService, "dispute.updated.v1", "dispute", dispute, b)
		if err := database.WithTx(t.Context(), k.RT.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			return usecases.DisputeHandler(k.RT)(ctx, tx, env, b)
		}); err != nil {
			t.Fatal(err)
		}
	}
	deliver("SUBMITTED", 3)
	deliver("SUBMITTED", 3) // redelivery is a no-op
	cases := k.Actor(rev, "GET", "/cases?kind=DISPUTE", "", nil).Items()
	if len(cases) != 1 {
		t.Fatalf("cases %v", cases)
	}
	cid := cases[0].(map[string]any)["id"].(string)
	if r := k.Actor(rev, "POST", "/cases/"+cid+"/claim", "c1", map[string]any{}); r.Status != 200 {
		t.Fatalf("claim %d %s", r.Status, r.Raw)
	}
	if r := k.Actor(rev, "POST", "/cases/"+cid+"/propose", "c2", map[string]any{"outcome": "REFUND_ALL", "rationale": "Evidence shows wear only"}); r.Status != 400 {
		t.Fatalf("invalid outcome %d", r.Status)
	}
	if r := k.Actor(rev, "POST", "/cases/"+cid+"/propose", "c3", map[string]any{"outcome": "PARTIALLY_UPHELD", "rationale": "Evidence shows fair wear only"}); r.Status != 200 {
		t.Fatalf("propose %d %s", r.Status, r.Raw)
	}
	if r := k.Actor(rev, "POST", "/cases/"+cid+"/decide", "c4", map[string]any{"rationale": "Reviewer cannot decide"}); r.Status != 403 {
		t.Fatalf("reviewer decide %d", r.Status)
	}
	decision := map[string]any{"rationale": "Partial deduction is justified"}
	if r := k.Actor(sup, "POST", "/cases/"+cid+"/decide", "d0", decision); r.Status != 403 {
		t.Fatalf("decide without MFA %d", r.Status)
	}
	// Business refuses (e.g. stale dispute): nothing is decided locally.
	k.Business.Reply = func(c domaintest.Call) (int, any) { return 409, nil }
	if r := stepUp(sup, "POST", "/cases/"+cid+"/decide", "d1", decision, clk.code(secret)); r.Status != 409 {
		t.Fatalf("business refusal %d %s", r.Status, r.Raw)
	}
	if st := k.Actor(sup, "GET", "/cases/"+cid, "", nil).Data()["case"].(map[string]any)["state"]; st != "PROPOSED" {
		t.Fatalf("case after refusal %v", st)
	}
	k.Business.Reply = nil
	d := stepUp(sup, "POST", "/cases/"+cid+"/decide", "d2", decision, clk.code(secret))
	if d.Status != 200 || d.Data()["state"] != "DECIDED" || d.Data()["outcome"] != "PARTIALLY_UPHELD" {
		t.Fatalf("decide %d %s", d.Status, d.Raw)
	}
	if r := stepUp(sup, "POST", "/cases/"+cid+"/decide", "d2", decision, "000000"); r.Status != 200 || r.Data()["state"] != "DECIDED" {
		t.Fatalf("idempotent replay %d %s", r.Status, r.Raw)
	}
	var rec domaintest.Call
	for _, c := range k.Business.Calls() {
		if c.Body["action"] == "RECORD_DECISION" {
			rec = c
		}
	}
	if rec.Org != org || rec.Caller != platform.AdminService || rec.Path != "/internal/v1/business-supply/partner-records/dispute/"+dispute.String()+"/transitions" || rec.Body["external_reference"] != "admin-service:case:"+cid || rec.Body["version"] != 3.0 {
		t.Fatalf("business decision call %+v", rec)
	}

	// Restriction: reviewer proposes, supervisor approves with MFA; the
	// owning service suspends the account.
	artisan := ids.New()
	rs := k.Actor(rev, "POST", "/restrictions", "r1", map[string]any{"case_id": cid, "subject_service": "artisan-service", "subject_account_id": artisan, "reason": "Repeated deposit abuse"})
	if rs.Status != 201 {
		t.Fatalf("restriction %d %s", rs.Status, rs.Raw)
	}
	rid := rs.Data()["id"].(string)
	if r := k.Actor(rev, "POST", "/restrictions", "r2", map[string]any{"case_id": cid, "subject_service": "artisan-service", "subject_account_id": artisan, "reason": "Duplicate"}); r.Status != 409 {
		t.Fatalf("duplicate live restriction %d", r.Status)
	}
	if r := stepUp(sup, "POST", "/restrictions/"+rid+"/approve", "r3", map[string]any{"reason": "Approved after review"}, clk.code(secret)); r.Status != 200 || r.Data()["state"] != "ACTIVE" {
		t.Fatalf("approve %d %s", r.Status, r.Raw)
	}
	if l := owner.last(); l["status"] != "SUSPENDED" || l["path"] != "/internal/v1/accounts/"+artisan.String()+"/status" || l["authorized"] != true {
		t.Fatalf("suspension call %v", l)
	}

	// Appeal (raised by Business for the affected party) overturns it.
	ap := k.Service(platform.BusinessOperationsService, org, "POST", "/cases/"+cid+"/appeals", "a1", map[string]any{"appellant_reference": "artisan-service:account:" + artisan.String(), "grounds": "The deductions were caused by the tenant"})
	if ap.Status != 201 {
		t.Fatalf("appeal %d %s", ap.Status, ap.Raw)
	}
	if r := k.Service(platform.BusinessOperationsService, org, "POST", "/cases/"+cid+"/appeals", "a2", map[string]any{"appellant_reference": "x-ref", "grounds": "A second appeal attempt"}); r.Status != 409 {
		t.Fatalf("second appeal %d", r.Status)
	}
	aid := ap.Data()["id"].(string)
	k.Actor(rev, "POST", "/cases/"+aid+"/claim", "a3", map[string]any{})
	k.Actor(rev, "POST", "/cases/"+aid+"/propose", "a4", map[string]any{"outcome": "OVERTURNED", "rationale": "New evidence shows tenant damage"})
	if r := stepUp(sup, "POST", "/cases/"+aid+"/decide", "a5", map[string]any{"rationale": "Overturned on new evidence"}, clk.code(secret)); r.Status != 200 {
		t.Fatalf("appeal decide %d %s", r.Status, r.Raw)
	}
	if l := owner.last(); l["status"] != "ACTIVE" {
		t.Fatalf("reinstatement call %v", l)
	}
	if items := k.Actor(sup, "GET", "/restrictions?state=LIFTED", "", nil).Items(); len(items) != 1 {
		t.Fatalf("lifted %v", items)
	}

	// Financial remedy approval from Marketplace: authorisation only.
	in := map[string]any{"kind": "FINANCIAL_REMEDY", "source_reference": "mkt-refund-77", "title": "Goodwill refund", "amount_minor": 25000, "currency": "NGN"}
	fr := k.Service(platform.MarketplaceService, ids.Nil, "POST", "/cases", "f1", in)
	if fr.Status != 201 {
		t.Fatalf("intake %d %s", fr.Status, fr.Raw)
	}
	if r := k.Service(platform.MarketplaceService, ids.Nil, "POST", "/cases", "f1b", in); r.Status != 200 || r.Data()["id"] != fr.Data()["id"] {
		t.Fatalf("intake idempotent by source %d %s", r.Status, r.Raw)
	}
	fid := fr.Data()["id"].(string)
	k.Actor(rev, "POST", "/cases/"+fid+"/claim", "f2", map[string]any{})
	k.Actor(rev, "POST", "/cases/"+fid+"/propose", "f3", map[string]any{"outcome": "APPROVED", "rationale": "Host cancelled within 24 hours"})
	if r := stepUp(sup, "POST", "/cases/"+fid+"/decide", "f4", map[string]any{"rationale": "Policy-compliant goodwill refund"}, clk.code(secret)); r.Status != 200 {
		t.Fatalf("remedy decide %d %s", r.Status, r.Raw)
	}
	var payload string
	if err := k.RT.Pool.QueryRow(t.Context(), `SELECT payload::text FROM outbox_events WHERE event_type='admin.case.decided.v1' AND aggregate_id=$1`, fid).Scan(&payload); err != nil || !strings.Contains(payload, "PENDING_PAYMENT_SERVICE") {
		t.Fatalf("remedy event %s %v", payload, err)
	}
	if r := k.Service(platform.MarketplaceService, ids.Nil, "GET", "/cases/by-source?kind=FINANCIAL_REMEDY&reference=mkt-refund-77", "", nil); r.Data()["state"] != "DECIDED" {
		t.Fatalf("readback %s", r.Raw)
	}
	if r := k.Service(platform.BusinessOperationsService, ids.Nil, "GET", "/cases/by-source?kind=FINANCIAL_REMEDY&reference=mkt-refund-77", "", nil); r.Status != 404 {
		t.Fatalf("other caller readback %d", r.Status)
	}
	// Maker-checker is also a database invariant.
	if _, err := k.RT.Pool.Exec(t.Context(), `UPDATE review_cases SET decided_by=proposed_by,version=version+1 WHERE id=$1`, fid); err == nil {
		t.Fatal("self-decision persisted")
	}
	// Withdrawal by Business closes an undecided dispute case only.
	deliver("WITHDRAWN", 5)
	if st := k.Actor(sup, "GET", "/cases/"+cid, "", nil).Data()["case"].(map[string]any)["state"]; st != "DECIDED" {
		t.Fatalf("decided case withdrawn %v", st)
	}
}
