package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// capturedCode waits for a verification code sent to destination through
// the real notification-service (dev provider capture file).
func (p *platformEnv) capturedCode(destination string) string {
	p.t.Helper()
	re := regexp.MustCompile(`\b(\d{6})\b`)
	var code string
	testkit.Eventually(p.t, 20*time.Second, func() bool {
		if p.smtp != nil {
			if c := re.FindStringSubmatch(p.smtp.message(destination)); c != nil {
				code = c[1]
			}
			return code != ""
		}
		f, err := os.Open(p.captureFile)
		if err != nil {
			return false
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var m map[string]string
			if json.Unmarshal(sc.Bytes(), &m) == nil && m["destination"] == destination {
				if c := re.FindStringSubmatch(m["body"]); c != nil {
					code = c[1]
				}
			}
		}
		return code != ""
	}, "verification code delivered")
	return code
}

// ifMatch sends a request with If-Match through the gateway.
func (p *platformEnv) ifMatch(token, method, path string, version int64, body any) response {
	p.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, p.gateway+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("If-Match", fmt.Sprintf(`"%d"`, version))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, body: raw}
}

// TestOwnerDomainJourneyThroughGateway exercises the Sections 6–13 domain
// API across real processes: activation through the real
// notification-service, property and maintenance commands through the
// gateway, and the domain event relayed by the owner worker to JetStream.
func TestOwnerDomainJourneyThroughGateway(t *testing.T) {
	p := startOwnerPlatform(t)
	email := "domain-" + ids.New().String()[24:] + "@example.com"
	r := send(t, "POST", p.gateway+"/api/v1/owner/auth/register", "", "", map[string]any{"email": email, "password": "correct horse battery",
		"display_name": "Domain Owner"}, "")
	if r.status != 201 {
		t.Fatalf("register: %d %s", r.status, r.body)
	}
	var reg struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
		EmailVerification struct {
			ChallengeID ids.ID `json:"challenge_id"`
		} `json:"email_verification"`
	}
	r.data(t, &reg)
	tok := reg.Tokens.AccessToken
	if r := p.asUser(tok, "POST", "/api/v1/owner/properties", map[string]any{"category": "RESIDENTIAL", "property_type": "APARTMENT", "title": "Pending home",
		"address": map[string]any{"line1": "1 E2E Road", "city": "Lagos", "state_region": "Lagos", "country_code": "NG"}}, ids.New().String()); r.status != 403 {
		t.Fatalf("pending account created a property: %d %s", r.status, r.body)
	}
	code := p.capturedCode(email)
	if r := p.asUser(tok, "POST", "/api/v1/owner/me/email/verification/confirm", map[string]any{"challenge_id": reg.EmailVerification.ChallengeID, "code": code}, ""); r.status != 200 {
		t.Fatalf("activate: %d %s", r.status, r.body)
	}

	r = p.asUser(tok, "POST", "/api/v1/owner/properties", map[string]any{"category": "RESIDENTIAL", "property_type": "APARTMENT", "title": "E2E home",
		"address": map[string]any{"line1": "1 E2E Road", "city": "Lagos", "state_region": "Lagos", "country_code": "NG", "latitude": 6.45, "longitude": 3.39}},
		ids.New().String())
	if r.status != 201 {
		t.Fatalf("property: %d %s", r.status, r.body)
	}
	var prop struct {
		ID ids.ID `json:"id"`
	}
	r.data(t, &prop)
	r = p.asUser(tok, "POST", "/api/v1/owner/maintenance", map[string]any{"property_id": prop.ID, "title": "Roof leak", "description": "Water in hallway.",
		"category": "ROOFING", "priority": "HIGH", "budget_minor": 25000000}, ids.New().String())
	if r.status != 201 {
		t.Fatalf("maintenance: %d %s", r.status, r.body)
	}
	var m struct {
		ID      ids.ID `json:"id"`
		Version int64  `json:"version"`
	}
	r.data(t, &m)
	if r := p.ifMatch(tok, "PATCH", "/api/v1/owner/maintenance/"+m.ID.String(), m.Version, map[string]any{"priority": "HIGH", "responsibility": "OWNER",
		"budget_minor": 25000000}); r.status != 200 {
		t.Fatalf("triage: %d %s", r.status, r.body)
	}
	if r := p.asUser(tok, "POST", "/api/v1/owner/maintenance/"+m.ID.String()+"/request-quotes", map[string]any{"sourcing": "PLATFORM"}, ids.New().String()); r.status != 200 {
		t.Fatalf("request quotes: %d %s", r.status, r.body)
	}
	if r := p.asUser(tok, "GET", "/api/v1/owner/dashboard", nil, ""); r.status != 200 || !strings.Contains(string(r.body), `"maintenance_awaiting_decision"`) {
		t.Fatalf("dashboard: %d %s", r.status, r.body)
	}

	// The worker relays maintenance.request.created.v1 for artisan discovery;
	// the owner's budget and contact details never leave the domain.
	nc, err := nats.Connect(p.nats.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var got string
	testkit.Eventually(t, 20*time.Second, func() bool {
		st, err := js.Stream(ctx, "REALESTATE_OWNER")
		if err != nil {
			return false
		}
		cons, err := st.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{FilterSubjects: []string{"realestate.maintenance.request.created.v1"}})
		if err != nil {
			return false
		}
		msg, err := cons.Next(jetstream.FetchMaxWait(time.Second))
		if err != nil {
			return false
		}
		got = string(msg.Data())
		return true
	}, "maintenance.request.created.v1 published")
	if !strings.Contains(got, m.ID.String()) || !strings.Contains(got, `"city":"Lagos"`) || strings.Contains(got, "25000000") || strings.Contains(got, email) {
		t.Fatalf("event: %s", got)
	}
}
