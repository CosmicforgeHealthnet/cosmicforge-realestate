package integration

import (
	"encoding/json"
	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"strings"
	"testing"
	"time"
)

func (h *businessHarness) deliveredCode(method, binding ids.ID, purpose string) (string, events.Envelope, notification.SendRequest) {
	h.t.Helper()
	var codeID ids.ID
	if err := h.db.Pool.QueryRow(h.t.Context(), `SELECT id FROM business_mfa_one_time_codes WHERE method_id=$1 AND binding_id=$2 AND purpose=$3 AND consumed_at IS NULL ORDER BY created_at DESC,id DESC LIMIT 1`, method, binding, purpose).Scan(&codeID); err != nil {
		h.t.Fatal(err)
	}
	rows, err := h.db.Pool.Query(h.t.Context(), `SELECT event_id,event_type,producer,aggregate_type,aggregate_id,payload,occurred_at FROM outbox_events WHERE event_type=$1 ORDER BY occurred_at DESC`, businessEvents.NotificationRequested)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var e events.Envelope
		if err := rows.Scan(&e.EventID, &e.EventType, &e.Producer, &e.AggregateType, &e.AggregateID, &e.Payload, &e.OccurredAt); err != nil {
			h.t.Fatal(err)
		}
		request, err := businessEvents.OpenNotification(h.service.Sealer, e)
		if err != nil {
			h.t.Fatal(err)
		}
		if request.CallerReference == "business-mfa:"+codeID.String() {
			return request.Variables["code"], e, request
		}
	}
	h.t.Fatal("protected Notification challenge was not queued")
	return "", events.Envelope{}, notification.SendRequest{}
}
func TestBusinessEmailSMSMFAEnrolmentLoginStepUpRecoveryAndExpiry(t *testing.T) {
	for _, kind := range []string{"EMAIL", "SMS"} {
		t.Run(kind, func(t *testing.T) {
			h := newBusinessHTTP(t, nil)
			u := h.user()
			body := map[string]any{"password": u.password, "kind": kind}
			phone := "+2348000000000"
			if kind == "SMS" {
				body["phone"] = phone
			}
			status, raw := h.call(u, "POST", "/auth/mfa/enrol", body, nil)
			if status != 201 {
				t.Fatalf("enrol %d", status)
			}
			var enrolled struct {
				Data businessauth.Enrolment `json:"data"`
			}
			if err := json.Unmarshal(raw, &enrolled); err != nil {
				t.Fatal(err)
			}
			method := enrolled.Data.MethodID
			if enrolled.Data.Kind != kind || !enrolled.Data.DeliveryQueued || enrolled.Data.Secret != "" || enrolled.Data.ProvisioningURI != "" {
				t.Fatal("delivery enrolment leaked a secret or claimed wrong method")
			}
			code, event, request := h.deliveredCode(method, method, "ENROL")
			if len(code) != 8 || request.TemplateKey != notification.TemplateVerificationCode || request.Channels[0] != kind {
				t.Fatal("wrong Notification delivery contract")
			}
			if strings.Contains(string(event.Payload), code) || strings.Contains(string(event.Payload), u.email) || strings.Contains(string(event.Payload), phone) {
				t.Fatal("MFA challenge was stored in plaintext")
			}
			provider := &businessNotificationFake{down: true}
			worker := workers.Notifications{Pool: h.db.Pool, Sealer: h.service.Sealer, Sender: provider}
			if err := worker.Handle(t.Context(), events.Delivery{Envelope: event}); err == nil {
				t.Fatal("provider outage was hidden")
			}
			provider.mu.Lock()
			provider.down = false
			provider.mu.Unlock()
			if err := worker.Handle(t.Context(), events.Delivery{Envelope: event}); err != nil {
				t.Fatal(err)
			}
			if status, raw = h.call(u, "POST", "/auth/mfa/confirm", map[string]string{"code": code}, nil); status != 200 {
				t.Fatalf("confirm %d", status)
			}
			var recovery struct {
				Data struct {
					Codes []string `json:"recovery_codes"`
				} `json:"data"`
			}
			if err := json.Unmarshal(raw, &recovery); err != nil || len(recovery.Data.Codes) != 8 {
				t.Fatal("recovery codes missing")
			}
			if status, _ := h.call(u, "POST", "/auth/mfa/confirm", map[string]string{"code": code}, nil); status != 401 {
				t.Fatalf("enrolment replay %d", status)
			}
			login := businessauth.Login{Email: u.email, Password: u.password, OrganisationID: u.registered.OrganisationID}
			first, err := h.service.Login(t.Context(), login, "delivered-login")
			if err != nil || first.MFAKind != kind || !first.DeliveryQueued {
				t.Fatal("login challenge not queued", err)
			}
			var firstBinding ids.ID
			if err := h.db.Pool.QueryRow(t.Context(), `SELECT id FROM business_mfa_challenges WHERE account_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, u.registered.AccountID).Scan(&firstBinding); err != nil {
				t.Fatal(err)
			}
			firstCode, _, _ := h.deliveredCode(method, firstBinding, "LOGIN")
			second, err := h.service.Login(t.Context(), login, "delivered-second-login")
			if err != nil {
				t.Fatal(err)
			}
			var secondBinding ids.ID
			if err := h.db.Pool.QueryRow(t.Context(), `SELECT id FROM business_mfa_challenges WHERE account_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, u.registered.AccountID).Scan(&secondBinding); err != nil {
				t.Fatal(err)
			}
			secondCode, _, _ := h.deliveredCode(method, secondBinding, "LOGIN")
			if firstCode != secondCode {
				if _, err := h.service.CompleteMFALogin(t.Context(), first.MFAToken, secondCode, false); err == nil {
					t.Fatal("code crossed its login challenge")
				}
			}
			tokens, err := h.service.CompleteMFALogin(t.Context(), first.MFAToken, firstCode, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.service.CompleteMFALogin(t.Context(), first.MFAToken, firstCode, false); err == nil {
				t.Fatal("login challenge replay")
			}
			if _, err := h.service.CompleteMFALogin(t.Context(), second.MFAToken, secondCode, false); err != nil {
				t.Fatal(err)
			}
			out, replayed, err := h.service.SendMFAChallenge(t.Context(), u.registered.AccountID, tokens.SessionID, "email-sms-step-up-one")
			if err != nil || replayed || !out.DeliveryQueued {
				t.Fatal("step-up queue", err)
			}
			if _, replayed, err := h.service.SendMFAChallenge(t.Context(), u.registered.AccountID, tokens.SessionID, "email-sms-step-up-one"); err != nil || !replayed {
				t.Fatal("step-up idempotency", err)
			}
			stepCode, stepEvent, _ := h.deliveredCode(method, tokens.SessionID, "STEP_UP")
			for i := 0; i < 5; i++ {
				if err := h.service.StepUp(t.Context(), u.registered.AccountID, tokens.SessionID, "invalid", false); err == nil {
					t.Fatal("wrong code accepted")
				}
			}
			if err := h.service.StepUp(t.Context(), u.registered.AccountID, tokens.SessionID, stepCode, false); err == nil {
				t.Fatal("attempt-exhausted challenge accepted")
			}
			if _, _, err := h.service.SendMFAChallenge(t.Context(), u.registered.AccountID, tokens.SessionID, "email-sms-step-up-two"); err != nil {
				t.Fatal(err)
			}
			stepCode, _, _ = h.deliveredCode(method, tokens.SessionID, "STEP_UP")
			if err := h.service.StepUp(t.Context(), u.registered.AccountID, tokens.SessionID, stepCode, false); err != nil {
				t.Fatal(err)
			}
			if err := h.service.StepUp(t.Context(), u.registered.AccountID, tokens.SessionID, stepCode, false); err == nil {
				t.Fatal("step-up replay")
			}
			if _, _, err := h.service.SendMFAChallenge(t.Context(), u.registered.AccountID, tokens.SessionID, "email-sms-step-up-three"); err != nil {
				t.Fatal(err)
			}
			stepCode, _, _ = h.deliveredCode(method, tokens.SessionID, "STEP_UP")
			h.service.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
			if err := h.service.StepUp(t.Context(), u.registered.AccountID, tokens.SessionID, stepCode, false); err == nil {
				t.Fatal("expired OTP accepted")
			}
			h.service.Now = time.Now
			if err := worker.Handle(t.Context(), events.Delivery{Envelope: stepEvent}); err != nil {
				t.Fatal(err)
			}
			_, accepted := provider.counts()
			if accepted != 1 {
				t.Fatal("superseded MFA challenge was delivered")
			}
			if err := h.service.StepUp(t.Context(), u.registered.AccountID, tokens.SessionID, recovery.Data.Codes[0], true); err != nil {
				t.Fatal(err)
			}
			if err := h.service.StepUp(t.Context(), u.registered.AccountID, tokens.SessionID, recovery.Data.Codes[0], true); err == nil {
				t.Fatal("recovery replay")
			}
			if err := h.service.RemoveMFA(t.Context(), u.registered.AccountID, tokens.SessionID, u.password); err != nil {
				t.Fatal(err)
			}
			if _, _, err := h.service.Authenticate(t.Context(), tokens.AccessToken); err == nil {
				t.Fatal("MFA removal did not revoke sessions")
			}
		})
	}
}
