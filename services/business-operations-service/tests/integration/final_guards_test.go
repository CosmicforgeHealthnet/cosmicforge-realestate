package integration

import (
	"encoding/json"
	"testing"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func TestBusinessResumeRequiresFreshAuthoritativeKYB(t *testing.T) {
	provider := &businessVerificationFake{}
	h := newBusinessHTTP(t, provider)
	u := h.user()
	h.secure(u)
	org := u.registered.OrganisationID
	c, err := provider.Create(t.Context(), verification.TypeBusiness, verification.Subject{Type: verification.SubjectBusiness, Domain: string(platform.DomainBusinessOperations), ID: org}, "resume", "resume")
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(time.Hour)
	provider.set(c.VerificationID, "VERIFIED", &expiry)
	if _, err := h.db.Pool.Exec(t.Context(), `INSERT INTO verification_links(id,organisation_id,case_id,purpose,provider_status,expires_at) VALUES($1,$2,$3,'KYB','VERIFIED',$4)`, ids.New(), org, c.VerificationID, expiry); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Pool.Exec(t.Context(), `UPDATE organisations SET state='RESTRICTED',restriction_source='ADMIN',activated_at=now() WHERE id=$1`, org); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.down = true
	provider.mu.Unlock()
	command := map[string]any{"state": "ACTIVE", "version": 1, "reason": "Resume after independent compliance review"}
	if status, _ := h.call(u, "POST", "/organisation/lifecycle", command, nil); status != 503 {
		t.Fatalf("cached KYB during outage: %d", status)
	}
	provider.mu.Lock()
	provider.down = false
	provider.mu.Unlock()
	provider.set(c.VerificationID, "RESTRICTED", &expiry)
	if status, _ := h.call(u, "POST", "/organisation/lifecycle", command, nil); status == 200 {
		t.Fatal("provider restriction ignored")
	}
	var state, status string
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT o.state,v.provider_status FROM organisations o JOIN verification_links v ON v.organisation_id=o.id WHERE o.id=$1`, org).Scan(&state, &status); err != nil || state != "RESTRICTED" || status != "RESTRICTED" {
		t.Fatalf("projection %s %s %v", state, status, err)
	}
}

func TestBusinessInvalidatedInvitationNotificationIsSuppressed(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	u := h.user()
	h.secure(u)
	var role ids.ID
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT role_id FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2`, u.registered.OrganisationID, u.registered.AccountID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	status, raw := h.call(u, "POST", "/staff", map[string]any{"email": "queued-invite@example.test", "role_id": role, "kind": "EMPLOYEE", "reason": "Staff onboarding", "grants": []map[string]any{{"capability": "staff:read", "scope_kind": "COMPANY"}}}, nil)
	if status != 201 {
		t.Fatalf("invite %d %s", status, raw)
	}
	var result struct {
		Data struct {
			ID ids.ID `json:"invitation_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	rows, err := h.db.Pool.Query(t.Context(), `SELECT event_id,event_type,producer,aggregate_type,aggregate_id,payload,occurred_at FROM outbox_events WHERE event_type=$1`, businessEvents.NotificationRequested)
	if err != nil {
		t.Fatal(err)
	}
	var job events.Envelope
	found := false
	for rows.Next() {
		var e events.Envelope
		if err := rows.Scan(&e.EventID, &e.EventType, &e.Producer, &e.AggregateType, &e.AggregateID, &e.Payload, &e.OccurredAt); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		request, err := businessEvents.OpenNotification(h.service.Sealer, e)
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if request.CallerReference == "business-invitation:"+result.Data.ID.String() {
			job = e
			found = true
		}
	}
	rows.Close()
	if !found {
		t.Fatal("invitation notification missing")
	}
	if _, err := h.db.Pool.Exec(t.Context(), `UPDATE organisations SET authorization_version=authorization_version+1 WHERE id=$1`, u.registered.OrganisationID); err != nil {
		t.Fatal(err)
	}
	provider := &businessNotificationFake{}
	worker := workers.Notifications{Pool: h.db.Pool, Sealer: h.service.Sealer, Sender: provider}
	if err := worker.Handle(t.Context(), events.Delivery{Envelope: job}); err != nil {
		t.Fatal(err)
	}
	if attempts, _ := provider.counts(); attempts != 0 {
		t.Fatal("invalid invitation delivered")
	}
	var count int
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM security_events WHERE event_type='INVITATION_NOTIFICATION_SUPPRESSED' AND target_id=$1`, result.Data.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("suppression audit %d %v", count, err)
	}
}
