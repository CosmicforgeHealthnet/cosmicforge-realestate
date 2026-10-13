package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Actual Gateway -> Owner -> Notification HTTP, API/worker and dependency
// outages. It does not repeat the five previously certified business journeys.
func TestOwnerRealProcessTracingAndReadinessRecovery(t *testing.T) {
	p := startOwnerPlatformConfigured(t, map[string]string{"LOG_LEVEL": "info", "OTEL_TRACES_SAMPLER_RATIO": "1"}, true)
	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	email := "test-operability-" + ids.New().String()[24:] + "@example.com"
	payload, _ := json.Marshal(map[string]any{"email": email, "password": "TEST safe fixture password", "display_name": "TEST Owner"})
	req, _ := http.NewRequest("POST", p.gateway+"/api/v1/owner/auth/register", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("registration failed: %d", resp.StatusCode)
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	data := result["data"].(map[string]any)
	token := data["tokens"].(map[string]any)["access_token"].(string)
	verificationID := data["email_verification"].(map[string]any)["challenge_id"].(string)
	if r := p.asUser(token, "POST", "/api/v1/owner/me/email/verification/confirm", map[string]any{"challenge_id": verificationID, "code": p.capturedCode(email)}, ""); r.status != 200 {
		t.Fatal("activate owner", r.status)
	}
	doc := p.upload(t, token, "VERIFICATION_DOCUMENT", "application/pdf", "NATIONAL_ID", append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("TEST identity fixture "), 200)...))
	verificationBody, _ := json.Marshal(map[string]any{"evidence": []map[string]any{{"media_id": doc, "evidence_type": "ID_DOCUMENT_FRONT"}}})
	verificationReq, _ := http.NewRequest("POST", p.gateway+"/api/v1/owner/verification", bytes.NewReader(verificationBody))
	verificationReq.Header.Set("Content-Type", "application/json")
	verificationReq.Header.Set("Authorization", "Bearer "+token)
	verificationReq.Header.Set("Idempotency-Key", ids.New().String())
	verificationReq.Header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	verificationResp, err := (&http.Client{Timeout: 10 * time.Second}).Do(verificationReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = verificationResp.Body.Close()
	if verificationResp.StatusCode != 201 {
		t.Fatal("verification contract", verificationResp.StatusCode)
	}
	testkit.Eventually(t, 5*time.Second, func() bool {
		ownerSeen, notificationSeen, verificationSeen := false, false, false
		for _, pr := range p.procs {
			pr.mu.Lock()
			logs := pr.out.String()
			pr.mu.Unlock()
			if strings.Contains(logs, token) || strings.Contains(logs, "TEST safe fixture password") {
				t.Fatal("credential leaked in process logs")
			}
			for _, line := range strings.Split(logs, "\n") {
				var record map[string]any
				if json.Unmarshal([]byte(line), &record) != nil {
					continue
				}
				if record["trace_id"] == traceID && record["message"] == "http request" {
					if pr.name == "individual-owner-service api" {
						ownerSeen = true
					}
					if pr.name == "notification-service api" {
						notificationSeen = true
					}
					if pr.name == "verification-service api" {
						verificationSeen = true
					}
				}
			}
		}
		return ownerSeen && notificationSeen && verificationSeen
	}, "real Owner/Verification/Notification correlated HTTP traces")
	ownerAPI := p.internal[platform.IndividualOwnerService]
	worker := ""
	for _, pr := range p.procs {
		if pr.name == "individual-owner-service worker" {
			for _, env := range pr.cmd.Env {
				if strings.HasPrefix(env, "HTTP_ADDR=") {
					worker = "http://" + strings.TrimPrefix(env, "HTTP_ADDR=")
				}
			}
		}
	}
	if worker == "" {
		t.Fatal("worker address missing")
	}
	for _, base := range []string{ownerAPI, worker} {
		for _, path := range []string{"/health", "/ready", "/version"} {
			if r := send(t, "GET", base+path, "", "", nil, ""); r.status != 200 {
				t.Fatal("health contract", path, r.status)
			}
		}
	}
	// Broker loss affects the event worker; the API remains available for local work.
	p.nats.Shutdown()
	testkit.Eventually(t, 5*time.Second, func() bool { return send(t, "GET", worker+"/ready", "", "", nil, "").status == 503 }, "worker broker outage")
	if send(t, "GET", ownerAPI+"/ready", "", "", nil, "").status != 200 {
		t.Fatal("optional API broker dependency")
	}
	p.nats.Restart()
	waitReady(t, worker)
	// Disable only this owned ephemeral DB, then terminate existing connections.
	admin, err := pgxpool.New(context.Background(), testkit.PostgresAdminURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	quoted := pgx.Identifier{p.ownerDB.Name}.Sanitize()
	defer func() { _, _ = admin.Exec(context.Background(), "ALTER DATABASE "+quoted+" ALLOW_CONNECTIONS true") }()
	if _, err := admin.Exec(context.Background(), "ALTER DATABASE "+quoted+" ALLOW_CONNECTIONS false"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1`, p.ownerDB.Name); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{ownerAPI, worker} {
		if send(t, "GET", base+"/ready", "", "", nil, "").status != 503 {
			t.Fatal("ready without database")
		}
		if send(t, "GET", base+"/health", "", "", nil, "").status != 200 {
			t.Fatal("liveness coupled to database")
		}
	}
	if _, err := admin.Exec(context.Background(), "ALTER DATABASE "+quoted+" ALLOW_CONNECTIONS true"); err != nil {
		t.Fatal(err)
	}
	waitReady(t, ownerAPI)
	waitReady(t, worker)
}
