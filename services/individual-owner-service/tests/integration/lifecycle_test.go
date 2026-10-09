package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/migrations"
	sharedauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestAdminRestrictSuspendReinstateWithIdempotencyAndCallerPolicy(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	admin := adminActor()
	path := "/internal/v1/owner/accounts/" + o.id.String()
	restrict := func(as platform.Service, d *serviceauth.Delegation, key string, body map[string]any) result {
		return h.do(call{method: "POST", path: path + "/restrict", as: as, delegate: d, key: key, body: body})
	}
	body := map[string]any{"reason_code": "FRAUD_REVIEW", "note": "case opened"}
	// Negative authorization: valid service tokens without the capability.
	h.expect(h.do(call{method: "POST", path: path + "/restrict", body: body}), 401, "AUTHENTICATION_REQUIRED")
	h.expect(restrict(platform.MarketplaceService, admin, "k1", body), 403, "PERMISSION_DENIED")
	h.expect(restrict(platform.APIGateway, admin, "k1", body), 403, "PERMISSION_DENIED")
	h.expect(restrict(platform.AdminService, nil, "k1", body), 403, "DELEGATED_ACTOR_REQUIRED")
	h.expect(restrict(platform.AdminService, &serviceauth.Delegation{ActorDomain: platform.DomainIndividualOwner, ActorID: o.id}, "k1", body), 403, "PERMISSION_DENIED")
	h.expect(restrict(platform.AdminService, admin, "", body), 400, "IDEMPOTENCY_KEY_REQUIRED")
	// Internal routes are not reachable with the public gateway header.
	h.expect(h.do(call{method: "GET", path: path, token: o.access}), 401, "AUTHENTICATION_REQUIRED")

	r := restrict(platform.AdminService, admin, "k1", body)
	h.expect(r, 200, "")
	var v usecases.AdminAccountView
	r.data(t, &v)
	if v.Status != "RESTRICTED" || len(v.History) != 3 || v.History[2].ActorDomain == nil || *v.History[2].ActorDomain != "admin" {
		t.Fatalf("%+v", v)
	}
	// Replay with the same key returns the stored response; a different body conflicts.
	r2 := restrict(platform.AdminService, admin, "k1", body)
	h.expect(r2, 200, "")
	if r2.header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("replay header missing")
	}
	h.expect(restrict(platform.AdminService, admin, "k1", map[string]any{"reason_code": "OTHER"}), 409, "IDEMPOTENCY_CONFLICT")
	// Contradictory/illegal transition.
	h.expect(restrict(platform.AdminService, admin, "k2", body), 409, "INVALID_STATE_TRANSITION")
	if h.count(`SELECT count(*) FROM outbox_events WHERE event_type='owner.account.restricted.v1' AND aggregate_id=$1`, o.id) != 1 {
		t.Fatal("exactly one restricted event")
	}
	// Restricted owners keep self-service but cannot request closure.
	h.expect(h.do(call{method: "GET", path: "/api/v1/owner/me", token: o.access}), 200, "")
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/closure", token: o.access}), 403, "ACCOUNT_RESTRICTED")

	// Suspension ends sessions immediately and blocks sign-in.
	h.expect(h.do(call{method: "POST", path: path + "/suspend", as: platform.AdminService, delegate: admin, key: "k3", body: map[string]any{"reason_code": "TERMS_VIOLATION"}}), 200, "")
	h.expect(h.do(call{method: "GET", path: "/api/v1/owner/me", token: o.access}), 401, "SESSION_REVOKED")
	r, _ = h.login(o, o.device)
	h.expect(r, 403, "ACCOUNT_UNAVAILABLE")
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/refresh", body: map[string]any{"refresh_token": o.refresh}}), 401, "INVALID_REFRESH_TOKEN")

	// Remediation requires an authorized resolution reference.
	h.expect(h.do(call{method: "POST", path: path + "/reinstate", as: platform.AdminService, delegate: admin, key: "k4",
		body: map[string]any{"reason_code": "REVIEW_CLEARED"}}), 422, "RESOLUTION_REFERENCE_REQUIRED")
	// Stale If-Match is rejected.
	h.expect(h.do(call{method: "POST", path: path + "/reinstate", as: platform.AdminService, delegate: admin, key: "k5",
		headers: map[string]string{"If-Match": `"1"`}, body: map[string]any{"reason_code": "REVIEW_CLEARED", "resolution_reference": "CASE-2026-0042"}}), 409, "VERSION_CONFLICT")
	r = h.do(call{method: "POST", path: path + "/reinstate", as: platform.AdminService, delegate: admin, key: "k6",
		body: map[string]any{"reason_code": "REVIEW_CLEARED", "resolution_reference": "CASE-2026-0042"}})
	h.expect(r, 200, "")
	r.data(t, &v)
	if v.Status != "ACTIVE" {
		t.Fatal(v.Status)
	}
	if h.count(`SELECT count(*) FROM outbox_events WHERE event_type='owner.account.activated.v1' AND aggregate_id=$1`, o.id) != 2 {
		t.Fatal("reinstatement must emit activated")
	}
	r, _ = h.login(o, o.device)
	h.expect(r, 200, "")

	// Admin read is audited as a privileged read; admin forced sign-out works.
	h.expect(h.do(call{method: "GET", path: path, as: platform.AdminService, delegate: admin}), 200, "")
	h.expect(h.do(call{method: "GET", path: path, as: platform.PropertyManagerService, delegate: admin}), 403, "PERMISSION_DENIED")
	h.expect(h.do(call{method: "GET", path: "/internal/v1/owner/accounts/" + ids.New().String(), as: platform.AdminService, delegate: admin}), 404, "ACCOUNT_NOT_FOUND")
	if h.count(`SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='owner.account.privileged_read'`, o.id) != 1 {
		t.Fatal("privileged read not audited")
	}
	r = h.do(call{method: "POST", path: path + "/sessions/revoke", as: platform.AdminService, delegate: admin, key: "k7", body: map[string]any{"reason_code": "COMPROMISE_SUSPECTED"}})
	h.expect(r, 200, "")
	if h.count(`SELECT count(*) FROM auth_sessions WHERE account_id=$1 AND revoked_at IS NULL`, o.id) != 0 {
		t.Fatal("sessions remain")
	}
}

func TestAccountClosureCoolingOffAndFinalization(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	h.clock.advance(6 * time.Minute)
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/closure", token: o.access}), 403, "STEP_UP_REQUIRED")
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/step-up", token: o.access, body: map[string]any{"method": "PASSWORD", "password": o.password}}), 200, "")
	r := h.do(call{method: "POST", path: "/api/v1/owner/me/closure", token: o.access})
	h.expect(r, 200, "")
	var acc usecases.AccountView
	r.data(t, &acc)
	if acc.Status != "CLOSURE_PENDING" || acc.ClosureEffectiveAt == nil {
		t.Fatalf("%+v", acc)
	}
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/closure", token: o.access}), 403, "ACCOUNT_NOT_ACTIVE")
	h.expect(h.do(call{method: "DELETE", path: "/api/v1/owner/me/closure", token: o.access}), 200, "")
	if h.status(o.id) != "ACTIVE" {
		t.Fatal("cancel must restore ACTIVE")
	}
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/closure", token: o.access}), 200, "")
	runner := h.app.WorkerRunner(nil, "w")
	if err := runner.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.status(o.id) != "CLOSURE_PENDING" {
		t.Fatal("closed before cooling-off")
	}
	h.clock.advance(31 * 24 * time.Hour)
	if err := runner.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.status(o.id) != "CLOSED" {
		t.Fatal("not closed after cooling-off")
	}
	r, _ = h.login(o, o.device)
	h.expect(r, 401, "INVALID_CREDENTIALS") // closed accounts release their email
	// The email can be registered again as a new, unrelated account.
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/register",
		body: map[string]any{"email": o.email, "password": "correct horse battery", "display_name": "New"}}), 201, "")
}

func TestProfileUpdateRequiresIfMatch(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	patch := func(etag string, body map[string]any) result {
		hdr := map[string]string{}
		if etag != "" {
			hdr["If-Match"] = etag
		}
		return h.do(call{method: "PATCH", path: "/api/v1/owner/me", token: o.access, headers: hdr, body: body})
	}
	h.expect(patch("", map[string]any{"display_name": "New"}), 428, "PRECONDITION_REQUIRED")
	h.expect(patch(`"1"`, map[string]any{"display_name": "New"}), 409, "VERSION_CONFLICT")
	r := h.do(call{method: "GET", path: "/api/v1/owner/me", token: o.access})
	etag := r.header.Get("ETag")
	h.expect(patch(etag, map[string]any{"locale": "english"}), 400, "INVALID_REQUEST")
	h.expect(patch(etag, map[string]any{"owner_id": ids.New()}), 400, "INVALID_JSON") // never trust client owner_id
	r = patch(etag, map[string]any{"display_name": "Ada L.", "locale": "en-NG"})
	h.expect(r, 200, "")
	if r.header.Get("ETag") == etag {
		t.Fatal("version must advance")
	}
	h.expect(patch(etag, map[string]any{"display_name": "Again"}), 409, "VERSION_CONFLICT")
}

func TestEmailChangeRequiresStepUpAndAlertsOldAddress(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	h.clock.advance(6 * time.Minute)
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/email/change", token: o.access, body: map[string]any{"email": "new@example.com"}}), 403, "STEP_UP_REQUIRED")
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/step-up", token: o.access, body: map[string]any{"method": "PASSWORD", "password": o.password}}), 200, "")
	r := h.do(call{method: "POST", path: "/api/v1/owner/me/email/change", token: o.access, body: map[string]any{"email": "New@Example.com"}})
	h.expect(r, 202, "")
	var ch usecases.ChallengeView
	r.data(t, &ch)
	r = h.do(call{method: "POST", path: "/api/v1/owner/me/email/verification/confirm", token: o.access,
		body: map[string]any{"challenge_id": ch.ChallengeID, "code": h.notify.lastCode(t, "new@example.com")}})
	h.expect(r, 200, "")
	var acc usecases.AccountView
	r.data(t, &acc)
	if acc.Email != "new@example.com" || !acc.EmailVerified {
		t.Fatalf("%+v", acc)
	}
	if h.count(`SELECT count(*) FROM notification_dispatches WHERE account_id=$1 AND destination_email=$2`, o.id, o.email) != 1 {
		t.Fatal("old address must be alerted")
	}
}

func TestOAuthIsExplicitlyUnavailable(t *testing.T) {
	h := newHarness(t)
	r := h.do(call{method: "GET", path: "/api/v1/owner/auth/oauth/providers"})
	h.expect(r, 200, "")
	if !strings.Contains(string(r.body), `"providers":[]`) {
		t.Fatalf("%s", r.body)
	}
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/oauth/google/start"}), 503, "OAUTH_PROVIDER_UNAVAILABLE")
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/oauth/apple/callback"}), 503, "OAUTH_PROVIDER_UNAVAILABLE")
}

// The gateway trusts owner tokens through this JWKS (jwks_url config).
func TestJWKSInteroperatesWithGatewayTrustConfig(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	spec, _ := json.Marshal([]map[string]any{{"issuer": "individual-owner-service", "domain": "individual-owner", "jwks_url": h.srv.URL + "/.well-known/jwks.json"}})
	issuers, err := sharedauth.ParseTrustedIssuers(spec, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	v, err := sharedauth.NewVerifier(sharedauth.VerifierConfig{Issuers: issuers, Now: h.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	p, err := v.Verify(context.Background(), o.access)
	if err != nil || p.ActorID != o.id || p.Domain != platform.DomainIndividualOwner || p.SessionID != o.session {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestHealthReadinessVersion(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/health", "/ready", "/version"} {
		h.expect(h.do(call{method: "GET", path: p, noGateway: true}), 200, "")
	}
	h.expect(h.do(call{method: "GET", path: "/api/v1/owner/nope"}), 404, "ROUTE_NOT_FOUND")
}

func TestMigrationsRoundTrip(t *testing.T) {
	testkit.MigrationRoundTrip(t, migrations.FS)
}

func TestErrorEnvelopeContract(t *testing.T) {
	h := newHarness(t)
	for _, r := range []result{
		h.do(call{method: "GET", path: "/api/v1/owner/me"}),
		h.do(call{method: "POST", path: "/api/v1/owner/auth/login", rawBody: []byte("{bad")}),
		h.do(call{method: "GET", path: "/internal/v1/owner/accounts/x", as: platform.AdminService, delegate: adminActor()}),
	} {
		testkit.ValidateJSONSchema(t, "../../../../contracts/schemas/error-envelope.schema.json", r.body)
	}
	srv := httptest.NewServer(h.app.Router)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/owner/me")
	if err != nil || resp.StatusCode != 401 || resp.Header.Get("X-Request-ID") == "" {
		t.Fatalf("%v %v", resp, err)
	}
}
