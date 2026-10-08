package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// TestFoundationPlatformEndToEnd drives a user's verification journey across
// all foundation components: upload a document through the gateway (media),
// verify it (verification -> media over authenticated internal calls),
// notify the user (notification), and observe every service's events.
func TestFoundationPlatformEndToEnd(t *testing.T) {
	p := startPlatform(t)
	userID := ids.New()
	userToken, _, err := p.users.Issue(auth.IssueRequest{Subject: userID, SessionID: ids.New()})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("gateway rejects unauthenticated, forged and internal requests", func(t *testing.T) {
		if r := p.asUser("", "POST", "/api/v1/media/uploads", map[string]any{}, "k"); r.status != 401 {
			t.Fatalf("anonymous: %d %s", r.status, r.body)
		}
		forged := userToken[:len(userToken)-4] + "AAAA"
		if r := p.asUser(forged, "GET", "/api/v1/notifications/preferences", nil, ""); r.status != 401 {
			t.Fatalf("forged token: %d", r.status)
		}
		if r := p.asUser(userToken, "GET", "/internal/v1/media", nil, ""); r.status != 404 {
			t.Fatalf("internal routes must not be exposed: %d", r.status)
		}
		// A user token is not a service credential on internal APIs.
		if r := send(t, "GET", p.internal[platform.NotificationService]+"/internal/v1/notifications/"+ids.New().String(), userToken, "", nil, ""); r.status != 401 {
			t.Fatalf("user token on internal API: %d", r.status)
		}
	})

	// 1. Upload a verification document through the gateway.
	doc := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("identity document "), 200)...)
	sum := sha256.Sum256(doc)
	var up struct {
		Media struct {
			MediaID ids.ID `json:"media_id"`
		} `json:"media"`
		Upload struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
		} `json:"upload"`
	}
	r := p.asUser(userToken, "POST", "/api/v1/media/uploads", map[string]any{"media_class": "VERIFICATION_DOCUMENT", "filename": "national-id.pdf",
		"mime_type": "application/pdf", "size_bytes": len(doc), "checksum_sha256": hex.EncodeToString(sum[:]), "document_type": "NATIONAL_ID"}, ids.New().String())
	if r.status != 201 {
		t.Fatalf("initiate upload via gateway: %d %s", r.status, r.body)
	}
	if r.header.Get("X-Request-ID") == "" {
		t.Fatal("gateway must return X-Request-ID")
	}
	r.data(t, &up)
	req, _ := http.NewRequest(up.Upload.Method, up.Upload.URL, bytes.NewReader(doc))
	for k, v := range up.Upload.Headers {
		req.Header.Set(k, v)
	}
	req.ContentLength = int64(len(doc))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("signed upload: %v %v", err, resp)
	}
	resp.Body.Close()
	mediaID := up.Media.MediaID
	if r := p.asUser(userToken, "POST", "/api/v1/media/"+mediaID.String()+"/complete", nil, ids.New().String()); r.status != 200 {
		t.Fatalf("complete via gateway: %d %s", r.status, r.body)
	}
	testkit.Eventually(t, 30*time.Second, func() bool {
		r := p.asService(platform.VerificationService, platform.MediaDocumentService, "GET", "/internal/v1/media/"+mediaID.String()+"/status", nil, "")
		return r.status == 200 && strings.Contains(string(r.body), `"status":"READY"`)
	}, "media never became READY (worker scan)")

	// 2. The user's domain service runs an identity verification with that
	// document; verification-service checks the evidence with media.
	var vc struct {
		VerificationID ids.ID `json:"verification_id"`
		Status         string `json:"status"`
	}
	io := platform.IndividualOwnerService
	r = p.asService(io, platform.VerificationService, "POST", "/internal/v1/verifications", map[string]any{"verification_type": "IDENTITY",
		"provider_metadata": map[string]string{"sandbox_outcome": "VERIFIED"},
		"subject":           map[string]any{"subject_type": "USER", "subject_domain": "individual-owner", "subject_id": userID}}, ids.New().String())
	if r.status != 201 {
		t.Fatalf("create verification: %d %s", r.status, r.body)
	}
	r.data(t, &vc)
	vid := vc.VerificationID.String()
	if r := p.asService(io, platform.VerificationService, "POST", "/internal/v1/verifications/"+vid+"/evidence",
		map[string]any{"media_id": mediaID, "evidence_type": "ID_DOCUMENT_FRONT"}, ids.New().String()); r.status != 201 {
		t.Fatalf("attach evidence (verification -> media): %d %s", r.status, r.body)
	}
	for _, step := range []string{"submit", "process"} {
		if r := p.asService(io, platform.VerificationService, "POST", "/internal/v1/verifications/"+vid+"/"+step, nil, ids.New().String()); r.status >= 300 {
			t.Fatalf("%s: %d %s", step, r.status, r.body)
		}
	}
	testkit.Eventually(t, 30*time.Second, func() bool {
		r := p.asService(io, platform.VerificationService, "GET", "/internal/v1/verifications/"+vid, nil, "")
		return r.status == 200 && strings.Contains(string(r.body), `"status":"VERIFIED"`)
	}, "verification never reached VERIFIED (worker + sandbox provider)")
	if r := p.asUser(userToken, "GET", "/api/v1/verification/cases", nil, ""); r.status != 200 || !strings.Contains(string(r.body), vid) || !strings.Contains(string(r.body), "VERIFIED") {
		t.Fatalf("user sees own case via gateway: %d %s", r.status, r.body)
	}
	other, _, _ := p.users.Issue(auth.IssueRequest{Subject: ids.New(), SessionID: ids.New()})
	if r := p.asUser(other, "GET", "/api/v1/verification/cases/"+vid, nil, ""); r.status != 404 {
		t.Fatalf("another user must not see the case: %d", r.status)
	}

	// 3. Notify the user: device registered through the gateway, notification
	// requested by the domain service, dispatched by the worker.
	if r := p.asUser(userToken, "POST", "/api/v1/notifications/push-endpoints",
		map[string]any{"token": "e2e-device-token-0123456789", "platform": "ANDROID"}, ids.New().String()); r.status != 201 {
		t.Fatalf("register device via gateway: %d %s", r.status, r.body)
	}
	if r := p.asUser(userToken, "PUT", "/api/v1/notifications/preferences",
		map[string]any{"preferences": []map[string]any{{"category": "ACTIVITY", "channel": "SMS", "enabled": false}}}, ""); r.status != 200 {
		t.Fatalf("preferences via gateway: %d %s", r.status, r.body)
	}
	var n struct {
		NotificationID ids.ID `json:"notification_id"`
		Deliveries     []struct {
			Channel string `json:"channel"`
		} `json:"deliveries"`
	}
	r = p.asService(io, platform.NotificationService, "POST", "/internal/v1/notifications", map[string]any{
		"recipient": map[string]any{"recipient_domain": "individual-owner", "recipient_id": userID}, "template_key": "verification.status_changed",
		"variables": map[string]string{"status": "VERIFIED"}, "destinations": map[string]string{"email": "owner@example.com"},
		"caller_reference": "verification:" + vid}, ids.New().String())
	if r.status != 202 {
		t.Fatalf("send notification: %d %s", r.status, r.body)
	}
	r.data(t, &n)
	if len(n.Deliveries) != 2 {
		t.Fatalf("expected PUSH + EMAIL deliveries: %s", r.body)
	}
	testkit.Eventually(t, 30*time.Second, func() bool {
		r := p.asService(io, platform.NotificationService, "GET", "/internal/v1/notifications/"+n.NotificationID.String(), nil, "")
		return r.status == 200 && strings.Count(string(r.body), `"status":"SENT"`) == 2
	}, "notification deliveries never reached SENT")

	t.Logf("media %s READY; verification %s VERIFIED; notification %s SENT on %d channels", mediaID, vid, n.NotificationID, len(n.Deliveries))

	// 4. Every service published its events through its outbox to JetStream,
	// and every event matches its contract.
	nc, err := nats.Connect(p.nats.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	want := map[string][]string{
		"REALESTATE_MEDIA":        {"media.upload_completed.v1", "media.scanning.v1", "media.ready.v1"},
		"REALESTATE_VERIFICATION": {"verification.submitted.v1", "verification.verified.v1"},
		"REALESTATE_NOTIFICATION": {"notification.queued.v1", "notification.sent.v1"},
	}
	for stream, types := range want {
		seen := map[string]bool{}
		testkit.Eventually(t, 20*time.Second, func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			st, err := js.Stream(ctx, stream)
			if err != nil {
				return false
			}
			cons, err := st.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{})
			if err != nil {
				return false
			}
			batch, err := cons.Fetch(200, jetstream.FetchMaxWait(time.Second))
			if err != nil {
				return false
			}
			for m := range batch.Messages() {
				env, err := events.Unmarshal(m.Data())
				if err != nil {
					t.Fatalf("%s: %v", stream, err)
				}
				dir := strings.SplitN(env.EventType, ".", 2)[0]
				testkit.ValidateJSONSchema(t, "../../contracts/events/"+dir+"/"+env.EventType+".schema.json", m.Data())
				if env.TraceID == "" {
					t.Fatalf("%s has no trace id", env.EventType)
				}
				seen[env.EventType] = true
			}
			for _, ty := range types {
				if !seen[ty] {
					return false
				}
			}
			return true
		}, "%s missing events %v (seen %v)", stream, types, seen)
		t.Logf("%s: %v", stream, seen)
	}
}
