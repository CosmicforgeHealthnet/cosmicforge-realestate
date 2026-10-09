package integration

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	sharedauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestRegistrationVerificationActivationAndEvent(t *testing.T) {
	h := newHarness(t)
	o := h.register(false)
	if h.status(o.id) != "PENDING_VERIFICATION" {
		t.Fatal("new account must be PENDING_VERIFICATION")
	}
	// Pending accounts may sign in (to finish verification) but are not active.
	r := h.do(call{method: "GET", path: "/api/v1/owner/me", token: o.access})
	h.expect(r, 200, "")
	if r.header.Get("ETag") != `"1"` {
		t.Fatalf("etag %q", r.header.Get("ETag"))
	}
	// Duplicate registration (case-insensitive email) is rejected.
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/register",
		body: map[string]any{"email": o.email, "password": "another password!", "display_name": "X"}}), 409, "EMAIL_ALREADY_REGISTERED")
	// Wrong code is rejected and counted.
	r = h.do(call{method: "POST", path: "/api/v1/owner/me/email/verification", token: o.access})
	h.expect(r, 202, "")
	var ch usecases.ChallengeView
	r.data(t, &ch)
	if ch.DestinationHint == o.email || ch.DeliveryStatus != "ACCEPTED" {
		t.Fatalf("challenge view %+v", ch)
	}
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/email/verification/confirm", token: o.access,
		body: map[string]any{"challenge_id": ch.ChallengeID, "code": "000000x"}}), 422, "INVALID_VERIFICATION_CODE")
	if h.count(`SELECT attempts FROM auth_challenges WHERE challenge_id=$1`, ch.ChallengeID) != 1 {
		t.Fatal("failed attempt must be committed")
	}
	code := h.notify.lastCode(t, o.email)
	r = h.do(call{method: "POST", path: "/api/v1/owner/me/email/verification/confirm", token: o.access,
		body: map[string]any{"challenge_id": ch.ChallengeID, "code": code}})
	h.expect(r, 200, "")
	var acc usecases.AccountView
	r.data(t, &acc)
	if acc.Status != "ACTIVE" || !acc.EmailVerified {
		t.Fatalf("not activated: %+v", acc)
	}
	// Single use.
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/email/verification/confirm", token: o.access,
		body: map[string]any{"challenge_id": ch.ChallengeID, "code": code}}), 422, "INVALID_VERIFICATION_CODE")
	if n := h.count(`SELECT count(*) FROM outbox_events WHERE event_type='owner.account.activated.v1' AND aggregate_id=$1`, o.id); n != 1 {
		t.Fatalf("activated events: %d", n)
	}
	if n := h.count(`SELECT count(*) FROM account_status_history WHERE account_id=$1`, o.id); n != 2 {
		t.Fatalf("history rows: %d", n)
	}
	if n := h.count(`SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='owner.account.status_changed'`, o.id); n != 1 {
		t.Fatalf("audit rows: %d", n)
	}
}

func TestConcurrentRegistrationSameEmail(t *testing.T) {
	h := newHarness(t)
	var wg sync.WaitGroup
	statuses := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- h.do(call{method: "POST", path: "/api/v1/owner/auth/register",
				body: map[string]any{"email": "race@example.com", "password": "correct horse battery", "display_name": "R"}}).status
		}()
	}
	wg.Wait()
	close(statuses)
	created := 0
	for s := range statuses {
		if s == 201 {
			created++
		} else if s != 409 {
			t.Fatalf("unexpected status %d", s)
		}
	}
	if created != 1 || h.count(`SELECT count(*) FROM owner_accounts WHERE email='race@example.com'`) != 1 {
		t.Fatalf("created %d", created)
	}
}

func TestLoginInvalidCredentialsAndLockout(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	r, a := h.login(o, o.device)
	h.expect(r, 200, "")
	if a.Status != "AUTHENTICATED" || a.Tokens.ExpiresIn != 900 {
		t.Fatalf("%+v", a)
	}
	// Unknown email and wrong password are indistinguishable.
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/login", body: map[string]any{"email": "nobody@example.com", "password": "whatever-123"}}), 401, "INVALID_CREDENTIALS")
	for i := 0; i < 5; i++ {
		h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/login", body: map[string]any{"email": o.email, "password": "wrong password"}}), 401, "INVALID_CREDENTIALS")
	}
	// Locked even with the right password.
	r, _ = h.login(o, o.device)
	h.expect(r, 429, "ACCOUNT_TEMPORARILY_LOCKED")
	if r.header.Get("Retry-After") == "" {
		t.Fatal("Retry-After missing")
	}
	if h.count(`SELECT count(*) FROM account_security_events WHERE account_id=$1 AND event_type='ACCOUNT_LOCKED'`, o.id) != 1 {
		t.Fatal("lock event missing")
	}
	h.clock.advance(16 * time.Minute)
	r, _ = h.login(o, o.device)
	h.expect(r, 200, "")
	if h.count(`SELECT failed_login_count FROM owner_accounts WHERE account_id=$1`, o.id) != 0 {
		t.Fatal("failures not reset after success")
	}
}

func TestAccessTokenValidation(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	me := func(c call) result { c.method, c.path = "GET", "/api/v1/owner/me"; return h.do(c) }
	h.expect(me(call{token: o.access}), 200, "")
	h.expect(me(call{}), 401, "AUTHENTICATION_REQUIRED")
	h.expect(me(call{token: "not.a.jwt"}), 401, "INVALID_TOKEN")
	h.expect(me(call{headers: map[string]string{"Authorization": "Basic Zm9vOmJhcg=="}}), 401, "INVALID_TOKEN")

	mint := func(issuer, audience string, key jwt.SigningKey, ttl time.Duration, sid ids.ID) string {
		iss, err := sharedauth.NewIssuer(sharedauth.IssuerConfig{Issuer: issuer, Audience: audience, Key: key, TTL: ttl, Now: h.clock.now})
		if err != nil {
			t.Fatal(err)
		}
		tok, _, _ := iss.Issue(sharedauth.IssueRequest{Subject: o.id, SessionID: sid})
		return tok
	}
	// Wrong issuer (another domain's token, e.g. artisan) — cross-domain misuse.
	h.expect(me(call{token: mint(string(platform.ArtisanService), sharedauth.DefaultAudience, h.userKey, time.Minute, o.session)}), 401, "INVALID_TOKEN")
	// Right issuer name, foreign signing key.
	h.expect(me(call{token: mint(string(platform.IndividualOwnerService), sharedauth.DefaultAudience, testkit.SigningKey(t, h.userKey.KID), time.Minute, o.session)}), 401, "INVALID_TOKEN")
	// Wrong audience.
	h.expect(me(call{token: mint(string(platform.IndividualOwnerService), "other-api", h.userKey, time.Minute, o.session)}), 401, "INVALID_TOKEN")
	// Token without a session ID.
	h.expect(me(call{token: mint(string(platform.IndividualOwnerService), sharedauth.DefaultAudience, h.userKey, time.Minute, ids.Nil)}), 401, "INVALID_TOKEN")
	// alg confusion: unsigned token.
	h.expect(me(call{token: "eyJhbGciOiJub25lIiwia2lkIjoiaW8tdXNlci0xIn0.eyJpc3MiOiJ4In0.AAAA"}), 401, "INVALID_TOKEN")
	// Forged gateway actor context: actor differs from the token subject.
	h.expect(me(call{token: o.access, actorDomain: platform.DomainIndividualOwner, actorID: ids.New()}), 401, "INVALID_TOKEN")
	h.expect(me(call{token: o.access, actorDomain: platform.DomainMarketplace, actorID: o.id}), 401, "INVALID_TOKEN")
	// Not via the gateway / via another service.
	h.expect(me(call{token: o.access, noGateway: true}), 401, "AUTHENTICATION_REQUIRED")
	h.expect(me(call{token: o.access, gatewayAs: platform.MarketplaceService}), 403, "PERMISSION_DENIED")
	// Expired.
	h.clock.advance(16 * time.Minute)
	h.expect(me(call{token: o.access}), 401, "INVALID_TOKEN")
}

func TestRefreshRotationReuseAndRevocation(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	refresh := func(tok string) result {
		return h.do(call{method: "POST", path: "/api/v1/owner/auth/refresh", body: map[string]any{"refresh_token": tok}})
	}
	r := refresh(o.refresh)
	h.expect(r, 200, "")
	var pair usecases.TokenPair
	r.data(t, &pair)
	if pair.RefreshToken == o.refresh || pair.SessionID != o.session {
		t.Fatal("token must rotate within the same session")
	}
	h.expect(h.do(call{method: "GET", path: "/api/v1/owner/me", token: pair.AccessToken}), 200, "")
	// Reusing the rotated token is theft: the family is revoked.
	h.expect(refresh(o.refresh), 401, "REFRESH_TOKEN_REUSED")
	h.expect(refresh(pair.RefreshToken), 401, "INVALID_REFRESH_TOKEN")
	h.expect(h.do(call{method: "GET", path: "/api/v1/owner/me", token: pair.AccessToken}), 401, "SESSION_REVOKED")
	if h.count(`SELECT count(*) FROM account_security_events WHERE account_id=$1 AND event_type='REFRESH_TOKEN_REUSE'`, o.id) != 1 {
		t.Fatal("reuse not recorded")
	}
	h.expect(refresh("cfrt_garbage"), 401, "INVALID_REFRESH_TOKEN")
	h.expect(refresh(""), 401, "INVALID_REFRESH_TOKEN")
}

func TestConcurrentRefreshAllowsExactlyOneRotation(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	const n = 10
	var wg sync.WaitGroup
	codes := make(chan int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes <- h.do(call{method: "POST", path: "/api/v1/owner/auth/refresh", body: map[string]any{"refresh_token": o.refresh}}).status
		}()
	}
	close(start)
	wg.Wait()
	close(codes)
	ok := 0
	for c := range codes {
		if c == http.StatusOK {
			ok++
		} else if c != http.StatusUnauthorized {
			t.Fatalf("unexpected %d", c)
		}
	}
	if ok > 1 {
		t.Fatalf("%d concurrent rotations succeeded", ok)
	}
	// Any losing presentation is treated as reuse: the family is revoked.
	if ok < n && h.count(`SELECT count(*) FROM auth_sessions WHERE id=$1 AND revoke_reason='TOKEN_REUSE'`, o.session) != 1 {
		t.Fatal("family not revoked after concurrent reuse")
	}
	if h.count(`SELECT count(*) FROM auth_refresh_tokens WHERE session_id=$1 AND consumed_at IS NOT NULL`, o.session) != 1 {
		t.Fatal("exactly one token may be consumed")
	}
}

func TestLogoutRemoteRevocationAndIDOR(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	_, b := h.login(o, "second-device-0001")
	other := h.register(true)
	r := h.do(call{method: "GET", path: "/api/v1/owner/me/sessions", token: o.access})
	h.expect(r, 200, "")
	var sess []usecases.SessionView
	r.data(t, &sess)
	if len(sess) != 2 {
		t.Fatalf("sessions %d", len(sess))
	}
	// Another owner cannot see or revoke this owner's session (IDOR).
	h.expect(h.do(call{method: "DELETE", path: "/api/v1/owner/me/sessions/" + b.Tokens.SessionID.String(), token: other.access}), 404, "SESSION_NOT_FOUND")
	// Remote revocation takes effect immediately for the revoked session.
	h.expect(h.do(call{method: "DELETE", path: "/api/v1/owner/me/sessions/" + b.Tokens.SessionID.String(), token: o.access}), 204, "")
	h.expect(h.do(call{method: "GET", path: "/api/v1/owner/me", token: b.Tokens.AccessToken}), 401, "SESSION_REVOKED")
	h.expect(h.do(call{method: "GET", path: "/api/v1/owner/me", token: o.access}), 200, "")
	// Logout ends the current session; logout-all ends every session.
	_, c := h.login(o, o.device)
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/logout", token: c.Tokens.AccessToken}), 204, "")
	h.expect(h.do(call{method: "GET", path: "/api/v1/owner/me", token: c.Tokens.AccessToken}), 401, "SESSION_REVOKED")
	r = h.do(call{method: "POST", path: "/api/v1/owner/auth/logout-all", token: o.access})
	h.expect(r, 200, "")
	h.expect(h.do(call{method: "GET", path: "/api/v1/owner/me", token: o.access}), 401, "SESSION_REVOKED")
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/refresh", body: map[string]any{"refresh_token": o.refresh}}), 401, "INVALID_REFRESH_TOKEN")
	h.expect(h.do(call{method: "GET", path: "/api/v1/owner/me", token: other.access}), 200, "")
}

func TestSuspiciousDeviceDetectionAndAlertDispatch(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	r, _ := h.login(o, o.device)
	h.expect(r, 200, "")
	if h.count(`SELECT count(*) FROM account_security_events WHERE account_id=$1 AND event_type='NEW_DEVICE_LOGIN'`, o.id) != 0 {
		t.Fatal("known device flagged")
	}
	r, _ = h.login(o, "unknown-device-77")
	h.expect(r, 200, "")
	r, _ = h.login(o, "") // unidentified device
	h.expect(r, 200, "")
	if n := h.count(`SELECT count(*) FROM account_security_events WHERE account_id=$1 AND event_type='NEW_DEVICE_LOGIN'`, o.id); n != 2 {
		t.Fatalf("new device events %d", n)
	}
	if n := h.count(`SELECT count(*) FROM notification_dispatches WHERE account_id=$1 AND status='PENDING'`, o.id); n != 2 {
		t.Fatalf("pending alerts %d", n)
	}
	// The worker delivers queued alerts; outages are retried with backoff.
	runner := h.app.WorkerRunner(nil, "w1")
	h.notify.set("down")
	if err := runner.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.count(`SELECT count(*) FROM notification_dispatches WHERE account_id=$1 AND status='PENDING' AND attempts=1 AND last_error IS NOT NULL`, o.id) != 2 {
		t.Fatal("failed dispatch not rescheduled")
	}
	h.notify.set("")
	h.clock.advance(time.Minute)
	if err := runner.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT count(*) FROM notification_dispatches WHERE account_id=$1 AND status='ACCEPTED' AND destination_email IS NULL`, o.id); n != 2 {
		t.Fatalf("accepted %d", n)
	}
	// Device management: list, trust (step-up via fresh login), revoke ends its sessions.
	_, fresh := h.login(o, o.device)
	r = h.do(call{method: "GET", path: "/api/v1/owner/me/devices", token: fresh.Tokens.AccessToken})
	var devs []usecases.DeviceView
	r.data(t, &devs)
	if len(devs) != 2 {
		t.Fatalf("devices %d", len(devs))
	}
	var unknown ids.ID
	for _, d := range devs {
		if d.Platform == "OTHER" {
			unknown = d.DeviceID
		}
	}
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/devices/" + unknown.String() + "/trust", token: fresh.Tokens.AccessToken}), 200, "")
	h.expect(h.do(call{method: "DELETE", path: "/api/v1/owner/me/devices/" + unknown.String(), token: fresh.Tokens.AccessToken}), 204, "")
	if h.count(`SELECT count(*) FROM auth_sessions WHERE device_id=$1 AND revoked_at IS NULL`, unknown) != 0 {
		t.Fatal("device sessions not revoked")
	}
	// Security history is paginated with a cursor.
	r = h.do(call{method: "GET", path: "/api/v1/owner/me/security-events?limit=3", token: fresh.Tokens.AccessToken})
	h.expect(r, 200, "")
	var page struct {
		Data []usecases.SecurityEventView `json:"data"`
		Page struct {
			NextCursor *string `json:"next_cursor"`
			HasMore    bool    `json:"has_more"`
		} `json:"page"`
	}
	_ = jsonUnmarshal(r.body, &page)
	if len(page.Data) != 3 || !page.Page.HasMore || page.Page.NextCursor == nil {
		t.Fatalf("page %+v", page.Page)
	}
	r = h.do(call{method: "GET", path: "/api/v1/owner/me/security-events?limit=3&cursor=" + *page.Page.NextCursor, token: fresh.Tokens.AccessToken})
	h.expect(r, 200, "")
	h.expect(h.do(call{method: "GET", path: "/api/v1/owner/me/security-events?cursor=bm90LWpzb24", token: fresh.Tokens.AccessToken}), 400, "INVALID_REQUEST")
}
