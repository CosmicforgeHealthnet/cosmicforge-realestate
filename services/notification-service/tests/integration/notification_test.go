package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/channels"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestMigrations(t *testing.T) { testkit.MigrationRoundTrip(t, migrations.FS) }

func TestQueueAndDispatchLifecycle(t *testing.T) {
	h := newHarness(t)
	u := newUser()
	ep := h.registerDevice(u, "device-token-0001-aaaaaaaa")
	if ep.Status != "ACTIVE" || ep.TokenSuffix != "aaaaaa" {
		t.Fatalf("endpoint %+v", ep)
	}
	n := h.mustSend(u, activity("Offer received", "You have a new offer <b>today</b>"))
	if len(n.Deliveries) != 3 || n.Critical || n.Category != "ACTIVITY" || n.TemplateVersion != 1 {
		t.Fatalf("queued %+v", n)
	}
	for _, d := range n.Deliveries {
		if d.Status != "QUEUED" || d.Attempts != 0 || d.ReceiptStatus != "NONE" {
			t.Fatalf("delivery %+v", d)
		}
	}
	if got := h.dispatch(); got != 3 {
		t.Fatalf("claimed %d", got)
	}
	n = h.get(n.NotificationID)
	for _, d := range n.Deliveries {
		if d.Status != "SENT" || d.Attempts != 1 || d.ReceiptStatus != "NONE" {
			t.Fatalf("after dispatch %+v", d)
		}
		if a := h.attempts(d.DeliveryID); len(a) != 1 || a[0] != "ACCEPTED" {
			t.Fatalf("attempts %v", a)
		}
	}
	// Push went to the registered token with routing data, not content-free.
	msgs := h.fcm.messages()
	if len(msgs) != 1 || msgs[0]["token"] != "device-token-0001-aaaaaaaa" {
		t.Fatalf("fcm %v", msgs)
	}
	data := msgs[0]["data"].(map[string]any)
	if data["notification_id"] != n.NotificationID.String() || msgs[0]["notification"].(map[string]any)["title"] != "Offer received" {
		t.Fatalf("fcm payload %v", msgs[0])
	}
	// Email HTML escapes variable values; plain text does not need to.
	em := h.email.Sent()
	if len(em) != 1 || em[0].Destination != "owner@example.com" || !strings.Contains(em[0].HTML, "&lt;b&gt;today&lt;/b&gt;") || em[0].Subject != "Offer received" {
		t.Fatalf("email %+v", em)
	}
	if sm := h.sms.Sent(); len(sm) != 1 || sm[0].Destination != "+2348030000000" {
		t.Fatalf("sms %+v", sm)
	}
	ev := h.eventTypes(n.NotificationID)
	if ev["notification.queued.v1"] != 3 || ev["notification.sent.v1"] != 3 || ev["notification.failed.v1"] != 0 {
		t.Fatalf("events %v", ev)
	}
	if h.count(`SELECT count(*) FROM audit_events WHERE action IN ('notification.requested','notification.delivery_sent')`) != 4 {
		t.Fatal("audit trail missing")
	}
	// Nothing left to do.
	if h.dispatch() != 0 {
		t.Fatal("sent deliveries must not be claimed again")
	}
}

func TestDuplicateCommandIsIdempotent(t *testing.T) {
	h := newHarness(t)
	u := newUser()
	body := alert("EMAIL")
	body["recipient"] = u.recipient()
	c := call{method: "POST", path: "/internal/v1/notifications", as: platform.IndividualOwnerService, key: "send-1", body: body}
	first := h.do(c)
	second := h.do(c)
	if first.status != 202 || second.status != 202 || string(first.body) != string(second.body) || second.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %d %s", first.status, second.status, second.header)
	}
	body2 := alert("SMS")
	body2["recipient"] = u.recipient()
	if r := h.do(call{method: "POST", path: c.path, as: c.as, key: "send-1", body: body2}); r.status != 409 {
		t.Fatalf("same key, different body: %d %s", r.status, r.body)
	}
	if r := h.do(call{method: "POST", path: c.path, as: c.as, body: body}); r.status != 400 {
		t.Fatalf("missing key: %d", r.status)
	}
	// Concurrent duplicates create exactly one notification.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); h.do(call{method: "POST", path: c.path, as: c.as, key: "send-2", body: body}) }()
	}
	wg.Wait()
	if n := h.count(`SELECT count(*) FROM notification_requests`); n != 2 {
		t.Fatalf("requests %d", n)
	}
	h.dispatch()
	if len(h.email.Sent()) != 2 {
		t.Fatalf("emails %d", len(h.email.Sent()))
	}
}

func TestRecipientIsolationAndCallerPolicy(t *testing.T) {
	h := newHarness(t)
	u := newUser()
	body := alert("EMAIL")
	body["recipient"] = u.recipient()
	cases := []struct {
		as     platform.Service
		status int
		code   string
	}{
		{platform.MarketplaceService, 403, "PERMISSION_DENIED"},   // other domain's user
		{platform.MediaDocumentService, 403, "PERMISSION_DENIED"}, // not an allowed caller
		{platform.VerificationService, 202, ""},                   // shared platform service: any domain
		{platform.PaymentService, 202, ""},
		{platform.AdminService, 202, ""},
		{platform.IndividualOwnerService, 202, ""},
	}
	for _, c := range cases {
		r := h.do(call{method: "POST", path: "/internal/v1/notifications", as: c.as, key: ids.New().String(), body: body})
		if r.status != c.status || (c.code != "" && r.code() != c.code) {
			t.Fatalf("%s: %d %s", c.as, r.status, r.body)
		}
		if c.as == platform.MarketplaceService && !strings.Contains(string(r.body), "RECIPIENT_DOMAIN_MISMATCH") {
			t.Fatalf("missing detail: %s", r.body)
		}
	}
	if r := h.do(call{method: "POST", path: "/internal/v1/notifications", key: "k", body: body}); r.status != 401 {
		t.Fatalf("anonymous: %d", r.status)
	}
	// The gateway's credential is not accepted on internal routes, and users
	// cannot send notifications at all.
	if r := h.do(call{method: "POST", path: "/internal/v1/notifications", as: platform.APIGateway, actor: u.actor, key: "k", body: body}); r.status != 403 {
		t.Fatalf("gateway on internal route: %d %s", r.status, r.body)
	}
	if r := h.do(call{method: "POST", path: "/api/v1/notifications", as: platform.APIGateway, gateway: true, actor: u.actor, key: "k", body: body}); r.status != 404 && r.status != 405 {
		t.Fatalf("no public send route: %d", r.status)
	}
	// A notification is visible to its requester (and admin), not to others.
	var n notification
	h.do(call{method: "POST", path: "/internal/v1/notifications", as: platform.IndividualOwnerService, key: "vis", body: body}).data(t, &n)
	if r := h.do(call{method: "GET", path: "/internal/v1/notifications/" + n.NotificationID.String(), as: platform.VerificationService}); r.status != 404 {
		t.Fatalf("foreign read: %d", r.status)
	}
	if r := h.do(call{method: "GET", path: "/internal/v1/notifications/" + n.NotificationID.String(), as: platform.AdminService}); r.status != 200 {
		t.Fatalf("admin read: %d", r.status)
	}
}

func TestPushEndpointsLifecycleAndIsolation(t *testing.T) {
	h := newHarness(t)
	u, other := newUser(), newUser()
	ep := h.registerDevice(u, "device-token-0002-bbbbbbbb")
	again := h.registerDevice(u, "device-token-0002-bbbbbbbb")
	if again.EndpointID != ep.EndpointID {
		t.Fatal("re-registering a token must refresh, not duplicate")
	}
	r := h.do(call{method: "GET", path: "/api/v1/notifications/push-endpoints", as: platform.APIGateway, gateway: true, actor: u.actor})
	if r.status != 200 || strings.Contains(string(r.body), "device-token-0002") {
		t.Fatalf("list must not expose tokens: %d %s", r.status, r.body)
	}
	// Users cannot register for someone else, revoke someone else's device,
	// or pass an explicit recipient.
	if r := h.do(call{method: "POST", path: "/api/v1/notifications/push-endpoints", as: platform.APIGateway, gateway: true, actor: other.actor,
		key: "x", body: map[string]any{"recipient": u.recipient(), "token": "device-token-zzzz-zzzzzzzz", "platform": "IOS"}}); r.status != 400 {
		t.Fatalf("explicit recipient: %d", r.status)
	}
	if r := h.do(call{method: "DELETE", path: "/api/v1/notifications/push-endpoints/" + ep.EndpointID.String(), as: platform.APIGateway, gateway: true, actor: other.actor}); r.status != 404 {
		t.Fatalf("foreign revoke: %d", r.status)
	}
	if r := h.do(call{method: "POST", path: "/api/v1/notifications/push-endpoints", as: platform.APIGateway, gateway: true, actor: u.actor,
		key: "bad", body: map[string]any{"token": "short", "platform": "ANDROID"}}); r.status != 400 || r.code() != "INVALID_PUSH_TOKEN" {
		t.Fatalf("bad token: %d %s", r.status, r.body)
	}
	// Domain service registration is limited to its own domain.
	if r := h.do(call{method: "POST", path: "/internal/v1/notifications/push-endpoints", as: platform.MarketplaceService, key: "m",
		body: map[string]any{"recipient": u.recipient(), "token": "device-token-mmmm-mmmmmmmm", "platform": "WEB"}}); r.status != 403 {
		t.Fatalf("cross-domain registration: %d", r.status)
	}
	if r := h.do(call{method: "GET", path: "/internal/v1/notifications/recipients/individual-owner/" + u.id.String() + "/push-endpoints", as: platform.IndividualOwnerService}); r.status != 200 {
		t.Fatalf("internal list: %d", r.status)
	}
	// Token moves to another account on the same device: reassigned + audited.
	moved := h.registerDevice(other, "device-token-0002-bbbbbbbb")
	if moved.EndpointID != ep.EndpointID || h.count(`SELECT count(*) FROM audit_events WHERE action='notification.push_endpoint_reassigned'`) != 1 {
		t.Fatal("reassignment not handled")
	}
	n := h.mustSend(u, activity("t", "m", "PUSH"))
	if n.suppressed("PUSH") != "NO_ACTIVE_PUSH_ENDPOINT" {
		t.Fatalf("old owner must not receive pushes: %+v", n)
	}
	// Revocation is idempotent.
	for range 2 {
		if r := h.do(call{method: "DELETE", path: "/api/v1/notifications/push-endpoints/" + ep.EndpointID.String(), as: platform.APIGateway, gateway: true, actor: other.actor}); r.status != 204 {
			t.Fatalf("revoke: %d %s", r.status, r.body)
		}
	}
	if n := h.mustSend(other, activity("t", "m", "PUSH")); n.suppressed("PUSH") != "NO_ACTIVE_PUSH_ENDPOINT" {
		t.Fatal("revoked endpoint used")
	}
}

func TestEndpointRevokedBeforeDispatch(t *testing.T) {
	h := newHarness(t)
	u := newUser()
	ep := h.registerDevice(u, "device-token-0003-cccccccc")
	n := h.mustSend(u, activity("t", "m", "PUSH"))
	h.do(call{method: "DELETE", path: "/api/v1/notifications/push-endpoints/" + ep.EndpointID.String(), as: platform.APIGateway, gateway: true, actor: u.actor})
	h.dispatch()
	d := h.get(n.NotificationID).Deliveries[0]
	if d.Status != "FAILED_FINAL" || *d.LastErrorCode != "PUSH_ENDPOINT_INACTIVE" || len(h.fcm.messages()) != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestPreferencesCriticalOverrideAndQuietHours(t *testing.T) {
	h := newHarness(t)
	u := newUser()
	h.registerDevice(u, "device-token-0004-dddddddd")
	put := func(body map[string]any) result {
		return h.do(call{method: "PUT", path: "/api/v1/notifications/preferences", as: platform.APIGateway, gateway: true, actor: u.actor, body: body})
	}
	r := put(map[string]any{"preferences": []map[string]any{{"category": "ACTIVITY", "channel": "EMAIL", "enabled": false}}})
	if r.status != 200 {
		t.Fatalf("put: %d %s", r.status, r.body)
	}
	if r := put(map[string]any{"preferences": []map[string]any{{"category": "SECURITY", "channel": "EMAIL", "enabled": false}}}); r.status != 400 {
		t.Fatalf("SECURITY must not be configurable: %d", r.status)
	}
	n := h.mustSend(u, activity("t", "m", "EMAIL", "PUSH"))
	if n.suppressed("EMAIL") != "PREFERENCE_DISABLED" || len(n.Deliveries) != 1 {
		t.Fatalf("preference ignored: %+v", n)
	}
	// Quiet hours 11:00-13:00 UTC cover the harness clock (12:00).
	if r := put(map[string]any{"quiet_hours": map[string]any{"start": "11:00", "end": "13:00", "timezone": "UTC"}}); r.status != 200 {
		t.Fatalf("quiet hours: %d %s", r.status, r.body)
	}
	g := h.do(call{method: "GET", path: "/api/v1/notifications/preferences", as: platform.APIGateway, gateway: true, actor: u.actor})
	if !strings.Contains(string(g.body), `"start":"11:00"`) || !strings.Contains(string(g.body), `"category":"ACTIVITY","channel":"EMAIL","enabled":false`) {
		t.Fatalf("get: %s", g.body)
	}
	act := h.mustSend(u, activity("t", "m", "PUSH", "SMS"))
	sec := h.mustSend(u, alert())
	if len(sec.Deliveries) != 3 || !sec.Critical {
		t.Fatalf("security alerts ignore preferences: %+v", sec)
	}
	h.dispatch()
	for _, d := range h.get(act.NotificationID).Deliveries {
		if d.Status != "QUEUED" {
			t.Fatalf("non-critical push/sms must wait for quiet hours to end: %+v", d)
		}
	}
	for _, d := range h.get(sec.NotificationID).Deliveries {
		if d.Status != "SENT" {
			t.Fatalf("security alert deferred: %+v", d)
		}
	}
	h.clock.advance(61 * time.Minute)
	h.dispatch()
	for _, d := range h.get(act.NotificationID).Deliveries {
		if d.Status != "SENT" {
			t.Fatalf("after quiet hours: %+v", d)
		}
	}
	// Clearing quiet hours and validation.
	if r := put(map[string]any{"quiet_hours": nil}); r.status != 200 || !strings.Contains(string(r.body), `"quiet_hours":null`) {
		t.Fatalf("clear: %s", r.body)
	}
	if r := put(map[string]any{"quiet_hours": map[string]any{"start": "25:00", "end": "07:00", "timezone": "UTC"}}); r.status != 400 {
		t.Fatalf("invalid quiet hours: %d", r.status)
	}
	if r := put(map[string]any{"quiet_hours": map[string]any{"start": "22:00", "end": "07:00", "timezone": "Mars/Base"}}); r.status != 400 {
		t.Fatalf("invalid timezone: %d", r.status)
	}
	// Domain services manage their own domain's users only.
	if r := h.do(call{method: "GET", path: "/internal/v1/notifications/recipients/individual-owner/" + u.id.String() + "/preferences", as: platform.MarketplaceService}); r.status != 403 {
		t.Fatalf("cross-domain preferences: %d", r.status)
	}
	if r := h.do(call{method: "PUT", path: "/internal/v1/notifications/recipients/individual-owner/" + u.id.String() + "/preferences", as: platform.IndividualOwnerService,
		body: map[string]any{"preferences": []map[string]any{{"category": "ACCOUNT", "channel": "SMS", "enabled": false}}}}); r.status != 200 {
		t.Fatalf("own-domain preferences: %d %s", r.status, r.body)
	}
	if h.count(`SELECT count(*) FROM audit_events WHERE action='notification.preferences_updated'`) != 4 {
		t.Fatal("preference changes must be audited")
	}
}

func TestValidationAndSuppression(t *testing.T) {
	h := newHarness(t)
	u := newUser()
	check := func(body map[string]any, status int, code string) {
		t.Helper()
		_, r := h.send(u, body)
		if r.status != status || (code != "" && r.code() != code) {
			t.Fatalf("%v: %d %s", body, r.status, r.body)
		}
	}
	check(map[string]any{"template_key": "account.security_alert", "variables": map[string]string{}}, 422, "INVALID_TEMPLATE_VARIABLES")
	check(map[string]any{"template_key": "account.security_alert", "variables": map[string]string{"event": "x", "extra": "y"}}, 422, "INVALID_TEMPLATE_VARIABLES")
	check(map[string]any{"template_key": "account.verification_code", "channels": []string{"PUSH"}, "variables": map[string]string{"code": "123456"}}, 422, "CHANNEL_NOT_ALLOWED")
	check(map[string]any{"template_key": "no.such_template", "variables": map[string]string{}}, 404, "TEMPLATE_NOT_FOUND")
	check(map[string]any{"template_key": "account.security_alert", "locale": "fr", "variables": map[string]string{"event": "x"}}, 404, "TEMPLATE_NOT_FOUND")
	check(map[string]any{"template_key": "account.security_alert", "variables": map[string]string{"event": "x"}, "destinations": map[string]string{"phone": "08030000000"}}, 400, "INVALID_REQUEST")
	check(map[string]any{"template_key": "account.security_alert", "variables": map[string]string{"event": "x"}, "destinations": map[string]string{"email": "Bob <bob@example.com>"}}, 400, "INVALID_REQUEST")
	check(map[string]any{"template_key": "account.security_alert", "channels": []string{"FAX"}, "variables": map[string]string{"event": "x"}}, 400, "INVALID_REQUEST")
	n := h.mustSend(u, map[string]any{"template_key": "account.security_alert", "variables": map[string]string{"event": "x"}})
	if len(n.Deliveries) != 0 || n.suppressed("EMAIL") != "NO_DESTINATION" || n.suppressed("SMS") != "NO_DESTINATION" || n.suppressed("PUSH") != "NO_ACTIVE_PUSH_ENDPOINT" {
		t.Fatalf("%+v", n)
	}
	// Variables cannot inject template syntax.
	h.mustSend(u, activity("{{message}}", "plain", "EMAIL"))
	h.dispatch()
	if s := h.email.Sent(); len(s) != 1 || s[0].Subject != "{{message}}" {
		t.Fatalf("variable values must not be re-rendered: %+v", s)
	}
}

func TestTransientRetryBackoffAndExhaustion(t *testing.T) {
	h := newHarness(t)
	u := newUser()
	transient := &channels.Error{Kind: channels.Transient, Code: "SMTP_421", HTTPStatus: 421, Err: errors.New("busy")}
	// One transient failure, then success.
	h.email.FailNext(transient)
	n := h.mustSend(u, alert("EMAIL"))
	h.dispatch()
	d := h.get(n.NotificationID).Deliveries[0]
	if d.Status != "RETRY_SCHEDULED" || d.Attempts != 1 || *d.LastErrorCode != "SMTP_421" {
		t.Fatalf("%+v", d)
	}
	if h.dispatch() != 0 {
		t.Fatal("retry must wait for backoff")
	}
	h.clock.advance(10 * time.Second)
	h.dispatch()
	if d := h.get(n.NotificationID).Deliveries[0]; d.Status != "SENT" || d.Attempts != 2 {
		t.Fatalf("%+v", d)
	}
	if a := h.attempts(d.DeliveryID); strings.Join(a, ",") != "TRANSIENT_ERROR,ACCEPTED" {
		t.Fatalf("attempts %v", a)
	}

	// SMS: always transient -> bounded (4 attempts) then FAILED_FINAL.
	h.sms.FailNext(transient, transient, transient, transient, transient)
	n = h.mustSend(u, alert("SMS"))
	for range 6 {
		h.dispatch()
		h.clock.advance(2 * time.Minute)
	}
	d = h.get(n.NotificationID).Deliveries[0]
	if d.Status != "FAILED_FINAL" || d.Attempts != 4 {
		t.Fatalf("exhaustion %+v", d)
	}
	if a := h.attempts(d.DeliveryID); len(a) != 4 {
		t.Fatalf("attempts %v", a)
	}
	if ev := h.eventTypes(n.NotificationID); ev["notification.failed.v1"] != 1 {
		t.Fatalf("events %v", ev)
	}
	// FCM UNAVAILABLE with Retry-After is retried.
	h.registerDevice(u, "device-token-unavailable-0005")
	n = h.mustSend(u, alert("PUSH"))
	h.dispatch()
	if d := h.get(n.NotificationID).Deliveries[0]; d.Status != "RETRY_SCHEDULED" || *d.LastErrorCode != "FCM_UNAVAILABLE" {
		t.Fatalf("%+v", d)
	}
	var retryAfter int
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT retry_after_seconds FROM provider_responses ORDER BY created_at DESC LIMIT 1`).Scan(&retryAfter)
	if retryAfter != 1 {
		t.Fatalf("retry-after %d", retryAfter)
	}
}

func TestPermanentFailureAndInvalidToken(t *testing.T) {
	h := newHarness(t)
	u := newUser()
	ep := h.registerDevice(u, "device-token-unregistered-0006")
	n := h.mustSend(u, alert("PUSH"))
	h.dispatch()
	d := h.get(n.NotificationID).Deliveries[0]
	if d.Status != "FAILED_FINAL" || d.Attempts != 1 || *d.LastErrorCode != "FCM_UNREGISTERED" {
		t.Fatalf("%+v", d)
	}
	var status string
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT status FROM push_endpoints WHERE endpoint_id=$1`, ep.EndpointID).Scan(&status)
	if status != "INVALID" {
		t.Fatalf("endpoint must be invalidated, got %s", status)
	}
	if n := h.mustSend(u, alert("PUSH")); n.suppressed("PUSH") != "NO_ACTIVE_PUSH_ENDPOINT" {
		t.Fatal("invalid endpoint reused")
	}
	// Permanent email error: no retry.
	h.email.FailNext(&channels.Error{Kind: channels.Permanent, Code: "SMTP_554", HTTPStatus: 554, Err: errors.New("rejected")})
	n = h.mustSend(u, alert("EMAIL"))
	h.dispatch()
	h.clock.advance(time.Hour)
	h.dispatch()
	if d := h.get(n.NotificationID).Deliveries[0]; d.Status != "FAILED_FINAL" || d.Attempts != 1 {
		t.Fatalf("%+v", d)
	}
	// Security content is cleared once handled; metadata is kept.
	var body string
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT body FROM notification_deliveries WHERE delivery_id=$1`, d.DeliveryID).Scan(&body)
	if body != "" {
		t.Fatal("security content must be cleared after a final outcome")
	}
}

func TestWorkerLeaseRecoveryAndConcurrentDispatch(t *testing.T) {
	h := newHarness(t)
	u := newUser()
	n := h.mustSend(u, alert("EMAIL"))
	id := n.Deliveries[0].DeliveryID
	// Simulate a worker that crashed mid-send: DISPATCHING, attempt 1, lease expired.
	_, err := h.db.Pool.Exec(context.Background(), `UPDATE notification_deliveries SET status='DISPATCHING', attempts=1, lease_owner='dead-worker',
		lease_expires_at=$2 WHERE delivery_id=$1`, id, h.clock.now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	h.dispatch()
	d := h.get(n.NotificationID).Deliveries[0]
	if d.Status != "RETRY_SCHEDULED" || *d.LastErrorCode != "OUTCOME_UNKNOWN_LEASE_EXPIRED" {
		t.Fatalf("%+v", d)
	}
	if a := h.attempts(id); len(a) != 1 || a[0] != "UNKNOWN" {
		t.Fatalf("unknown outcome must be recorded, got %v", a)
	}
	h.clock.advance(time.Minute)
	h.dispatch()
	if d := h.get(n.NotificationID).Deliveries[0]; d.Status != "SENT" || d.Attempts != 2 {
		t.Fatalf("%+v", d)
	}

	// Many workers, many deliveries: each delivery sent exactly once here.
	before := len(h.email.Sent())
	for i := range 30 {
		h.mustSend(u, activity("t", "m"+string(rune('a'+i%26)), "EMAIL"))
	}
	var wg sync.WaitGroup
	for w := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				_, _ = h.app.Service.DispatchDue(context.Background(), "worker-"+string(rune('0'+w)), 4)
			}
		}()
	}
	wg.Wait()
	h.dispatch()
	if got := len(h.email.Sent()) - before; got != 30 {
		t.Fatalf("sent %d, want 30", got)
	}
	if h.count(`SELECT count(*) FROM delivery_attempts WHERE outcome='ACCEPTED'`) != 31 {
		t.Fatal("duplicate attempts recorded")
	}
}

func TestExpiryAndContentPurge(t *testing.T) {
	h := newHarness(t)
	u := newUser()
	h.sms.FailNext(&channels.Error{Kind: channels.Transient, Code: "SMS_HTTP_503", Err: errors.New("x")})
	// verification_code has a 10 minute TTL.
	body := map[string]any{"template_key": "account.verification_code", "channels": []string{"SMS"}, "variables": map[string]string{"code": "482913"},
		"destinations": map[string]string{"phone": "+2348030000001"}}
	n := h.mustSend(u, body)
	h.dispatch()
	h.clock.advance(11 * time.Minute)
	h.dispatch()
	d := h.get(n.NotificationID).Deliveries[0]
	if d.Status != "FAILED_FINAL" || *d.LastErrorCode != "EXPIRED" || len(h.sms.Sent()) != 0 {
		t.Fatalf("expired code must never be sent: %+v", d)
	}
	// Non-critical content is purged after the retention window.
	n = h.mustSend(u, activity("t", "secret-ish", "EMAIL"))
	h.dispatch()
	if c, _ := h.app.Service.PurgeContent(context.Background()); c != 0 {
		t.Fatalf("purged too early: %d", c)
	}
	h.clock.advance(25 * time.Hour)
	if c, _ := h.app.Service.PurgeContent(context.Background()); c != 1 {
		t.Fatalf("purged %d", c)
	}
	if h.count(`SELECT count(*) FROM notification_deliveries WHERE body LIKE '%482913%' OR body LIKE '%secret-ish%'`) != 0 {
		t.Fatal("content retained")
	}
}

func TestTemplateVersioning(t *testing.T) {
	h := newHarness(t)
	u := newUser()
	tmpl := map[string]any{"template_key": "activity.generic_update", "locale": "en", "category": "ACTIVITY", "channels": []string{"EMAIL", "PUSH"},
		"required_variables": []string{"title", "message"}, "push_title": "{{title}}", "push_body": "v2: {{message}}",
		"email_subject": "[v2] {{title}}", "email_text": "{{message}}"}
	if r := h.do(call{method: "POST", path: "/internal/v1/notifications/templates", as: platform.MarketplaceService, key: "t0", body: tmpl}); r.status != 403 {
		t.Fatalf("non-admin template write: %d", r.status)
	}
	bad := map[string]any{}
	for k, v := range tmpl {
		bad[k] = v
	}
	bad["email_text"] = "{{undeclared}}"
	if r := h.do(call{method: "POST", path: "/internal/v1/notifications/templates", as: platform.AdminService, key: "t1", body: bad}); r.status != 422 || r.code() != "INVALID_TEMPLATE" {
		t.Fatalf("undeclared placeholder: %d %s", r.status, r.body)
	}
	bad["email_text"] = "{{message | upper}} {{title}}"
	if r := h.do(call{method: "POST", path: "/internal/v1/notifications/templates", as: platform.AdminService, key: "t2", body: bad}); r.status != 422 {
		t.Fatalf("template syntax must be restricted: %d", r.status)
	}
	r := h.do(call{method: "POST", path: "/internal/v1/notifications/templates", as: platform.AdminService, actor: &serviceauth.Delegation{ActorDomain: platform.DomainAdmin, ActorID: ids.New()}, key: "t3", body: tmpl})
	if r.status != 201 || !strings.Contains(string(r.body), `"version":2`) {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	n := h.mustSend(u, activity("Hi", "there", "EMAIL"))
	if n.TemplateVersion != 2 {
		t.Fatalf("latest version not used: %+v", n)
	}
	b := activity("Hi", "there", "SMS")
	b["template_version"] = 1
	if n := h.mustSend(u, b); n.TemplateVersion != 1 || len(n.Deliveries) != 1 {
		t.Fatalf("pinned version: %+v", n)
	}
	h.dispatch()
	if s := h.email.Sent(); len(s) != 1 || s[0].Subject != "[v2] Hi" {
		t.Fatalf("%+v", s)
	}
	l := h.do(call{method: "GET", path: "/internal/v1/notifications/templates/activity.generic_update", as: platform.IndividualOwnerService})
	if l.status != 200 || strings.Count(string(l.body), `"template_id"`) != 2 {
		t.Fatalf("list: %d %s", l.status, l.body)
	}
	if h.count(`SELECT count(*) FROM audit_events WHERE action='notification.template_version_created'`) != 1 {
		t.Fatal("template change not audited")
	}
}

func TestHealthEndpoints(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/health", "/ready", "/version"} {
		if r := h.do(call{method: "GET", path: p}); r.status != 200 {
			t.Fatalf("%s: %d %s", p, r.status, r.body)
		}
	}
}
