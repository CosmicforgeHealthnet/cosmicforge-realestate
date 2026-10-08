package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/app"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/channels"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/migrations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// fakeFCM emulates the FCM HTTP v1 send endpoint. Tokens containing
// "unregistered" are rejected as UNREGISTERED; tokens containing
// "unavailable" fail with 503 + Retry-After.
type fakeFCM struct {
	mu   sync.Mutex
	sent []map[string]any
}

func (f *fakeFCM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Message map[string]any `json:"message"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	tok, _ := body.Message["token"].(string)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Header.Get("Authorization") != "Bearer fake-access-token":
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":{"code":401,"status":"UNAUTHENTICATED","message":"bad auth"}}`)
	case strings.Contains(tok, "unregistered"):
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"Requested entity was not found.","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`)
	case strings.Contains(tok, "unavailable"):
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(503)
		_, _ = io.WriteString(w, `{"error":{"code":503,"status":"UNAVAILABLE","message":"try later","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNAVAILABLE"}]}}`)
	default:
		f.mu.Lock()
		f.sent = append(f.sent, body.Message)
		n := len(f.sent)
		f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"name":"projects/test/messages/%d"}`, n)
	}
}

func (f *fakeFCM) messages() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.sent...)
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
	fcm     *fakeFCM
	email   *channels.Capture
	sms     *channels.Capture
	signers map[platform.Service]*serviceauth.Signer
	clock   *clock
}

var callers = []platform.Service{
	platform.APIGateway, platform.IndividualOwnerService, platform.MarketplaceService, platform.AdminService,
	platform.VerificationService, platform.PaymentService, platform.MediaDocumentService,
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, signers: map[platform.Service]*serviceauth.Signer{}, clock: &clock{t: time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)}}
	h.db = testkit.NewDatabase(t, migrations.FS)
	reg := serviceauth.NewRegistry()
	for _, s := range callers {
		k := testkit.SigningKey(t, string(s)+"-1")
		_ = reg.Add(s, k.KID, k.PublicKey())
		h.signers[s], _ = serviceauth.NewSigner(serviceauth.SignerConfig{Service: s, Key: k})
	}
	self := testkit.SigningKey(t, "ns-1")
	selfSigner, _ := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.NotificationService, Key: self})
	verifier, _ := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.NotificationService, Registry: reg})

	h.fcm = &fakeFCM{}
	fcmSrv := httptest.NewServer(h.fcm)
	t.Cleanup(fcmSrv.Close)
	push, err := channels.NewFCM(channels.FCMConfig{ProjectID: "test", BaseURL: fcmSrv.URL,
		Tokens: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fake-access-token"}), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	h.email, h.sms = channels.NewCapture(domain.ChannelEmail), channels.NewCapture(domain.ChannelSMS)
	cfg := &config.Config{Environment: "test", DatabaseURL: security.NewSecret(h.db.URL), DBMaxConns: 10,
		Identity: &serviceauth.Identity{Signer: selfSigner, Verifier: verifier}, SubjectPrefix: "realestate",
		DispatchLease: time.Minute, SendTimeout: 5 * time.Second, RetryBase: 10 * time.Second, RetryMax: 80 * time.Second,
		ContentRetention: 24 * time.Hour, DefaultLocale: "en"}
	h.app, err = app.Build(context.Background(), cfg, logging.Discard(), app.Overrides{
		Senders: channels.Registry{domain.ChannelPush: channels.Push(push), domain.ChannelEmail: channels.Email(h.email), domain.ChannelSMS: channels.SMS(h.sms)},
		Now:     h.clock.now, Jitter: func(time.Duration) time.Duration { return 0 }})
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
		tok, err := h.signers[c.as].TokenFor(string(platform.NotificationService), d)
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

// user is an end user in the individual-owner domain.
type user struct {
	id    ids.ID
	actor *serviceauth.Delegation
}

func newUser() user {
	id := ids.New()
	return user{id: id, actor: &serviceauth.Delegation{ActorDomain: platform.DomainIndividualOwner, ActorID: id}}
}

func (u user) recipient() map[string]any {
	return map[string]any{"recipient_domain": "individual-owner", "recipient_id": u.id}
}

type endpoint struct {
	EndpointID  ids.ID `json:"endpoint_id"`
	Status      string `json:"status"`
	TokenSuffix string `json:"token_suffix"`
}

// registerDevice registers a push token through the gateway as the user.
func (h *harness) registerDevice(u user, token string) endpoint {
	h.t.Helper()
	r := h.do(call{method: "POST", path: "/api/v1/notifications/push-endpoints", as: platform.APIGateway, gateway: true, actor: u.actor,
		key: ids.New().String(), body: map[string]any{"token": token, "platform": "ANDROID", "device_id": "pixel-8", "app_version": "1.0.0"}})
	if r.status != 201 {
		h.t.Fatalf("register: %d %s", r.status, r.body)
	}
	var e endpoint
	r.data(h.t, &e)
	return e
}

type delivery struct {
	DeliveryID    ids.ID  `json:"delivery_id"`
	Channel       string  `json:"channel"`
	Status        string  `json:"status"`
	Attempts      int     `json:"attempts"`
	LastErrorCode *string `json:"last_error_code"`
	ReceiptStatus string  `json:"receipt_status"`
}

type notification struct {
	NotificationID  ids.ID     `json:"notification_id"`
	TemplateVersion int        `json:"template_version"`
	Category        string     `json:"category"`
	Critical        bool       `json:"critical"`
	Deliveries      []delivery `json:"deliveries"`
	Suppressed      []struct {
		Channel string `json:"channel"`
		Reason  string `json:"reason"`
	} `json:"suppressed"`
}

func (n notification) suppressed(ch string) string {
	for _, s := range n.Suppressed {
		if s.Channel == ch {
			return s.Reason
		}
	}
	return ""
}

// send requests a notification as the individual-owner domain service.
func (h *harness) send(u user, body map[string]any) (notification, result) {
	h.t.Helper()
	body["recipient"] = u.recipient()
	r := h.do(call{method: "POST", path: "/internal/v1/notifications", as: platform.IndividualOwnerService, key: ids.New().String(), body: body})
	var n notification
	if r.status == 202 {
		r.data(h.t, &n)
	}
	return n, r
}

func (h *harness) mustSend(u user, body map[string]any) notification {
	h.t.Helper()
	n, r := h.send(u, body)
	if r.status != 202 {
		h.t.Fatalf("send: %d %s", r.status, r.body)
	}
	return n
}

func (h *harness) get(id ids.ID) notification {
	h.t.Helper()
	r := h.do(call{method: "GET", path: "/internal/v1/notifications/" + id.String(), as: platform.IndividualOwnerService})
	if r.status != 200 {
		h.t.Fatalf("get: %d %s", r.status, r.body)
	}
	var n notification
	r.data(h.t, &n)
	return n
}

func (h *harness) dispatch() int {
	h.t.Helper()
	n, err := h.app.Service.DispatchDue(context.Background(), "test-worker", 100)
	if err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) eventTypes(id ids.ID) map[string]int {
	rows, _ := h.db.Pool.Query(context.Background(), `SELECT event_type FROM outbox_events WHERE aggregate_id=$1`, id)
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out[s]++
	}
	return out
}

func (h *harness) attempts(deliveryID ids.ID) []string {
	rows, _ := h.db.Pool.Query(context.Background(), `SELECT outcome FROM delivery_attempts WHERE delivery_id=$1 ORDER BY attempt_no`, deliveryID)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func (h *harness) count(sql string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func alert(channels ...string) map[string]any {
	b := map[string]any{"template_key": "account.security_alert", "variables": map[string]string{"event": "New sign-in from Lagos"},
		"destinations": map[string]string{"email": "owner@example.com", "phone": "+2348030000000"}}
	if len(channels) > 0 {
		b["channels"] = channels
	}
	return b
}

func activity(title, message string, channels ...string) map[string]any {
	b := map[string]any{"template_key": "activity.generic_update", "variables": map[string]string{"title": title, "message": message},
		"destinations": map[string]string{"email": "owner@example.com", "phone": "+2348030000000"}}
	if len(channels) > 0 {
		b["channels"] = channels
	}
	return b
}
