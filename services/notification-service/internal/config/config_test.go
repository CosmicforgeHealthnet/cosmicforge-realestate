package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// base is a minimal environment: no email or SMS provider settings at all.
func base(t *testing.T, appEnv string) map[string]string {
	self := testkit.SigningKey(t, "ns-1")
	gw := testkit.SigningKey(t, "gw-1")
	reg, _ := json.Marshal([]map[string]any{{"service": "api-gateway", "keys": []map[string]string{{"kid": "gw-1", "public_key": security.EncodeEd25519PublicKey(gw.PublicKey())}}}})
	return map[string]string{
		"APP_ENV": appEnv, "DATABASE_URL": "postgres://u:p@db/notification_db", "NATS_URL": "nats://nats:4222",
		"SERVICE_AUTH_KEY_ID": "ns-1", "SERVICE_AUTH_PRIVATE_KEY": security.EncodeEd25519Seed(self.PrivateKey), "SERVICE_AUTH_TRUSTED_KEYS": string(reg),
	}
}

func load(m map[string]string, mode Mode) (*Config, error) { return Load(sharedcfg.FromMap(m), mode) }

func TestDevelopmentAndTestUseCaptureWithoutCredentials(t *testing.T) {
	for _, env := range []string{"development", "test"} {
		c, err := load(base(t, env), ModeWorker)
		if err != nil {
			t.Fatal(err)
		}
		if c.PushProvider != ProviderDev || c.EmailProvider != ProviderDev || c.SMSProvider != ProviderDev || c.FCM != nil || c.SMTP != nil || c.Twilio != nil {
			t.Fatalf("%s: %+v", env, c)
		}
	}
}

func TestStagingDefaultsFCMPushAndNoEmailOrSMSProvider(t *testing.T) {
	m := base(t, "staging")
	if _, err := load(m, ModeAPI); err == nil || !strings.Contains(err.Error(), "FCM_PROJECT_ID") {
		t.Fatalf("FCM is the selected push provider and needs its settings: %v", err)
	}
	m["FCM_PROJECT_ID"], m["FCM_CREDENTIALS_JSON"] = "proj", `{"type":"service_account"}`
	// Optional adapter settings present but not selected are ignored.
	m["TWILIO_ACCOUNT_SID"], m["SMTP_HOST"] = "AC1", "smtp.example.com"
	c, err := load(m, ModeAPI)
	if err != nil {
		t.Fatal(err)
	}
	if c.PushProvider != ProviderFCM || c.FCM == nil || c.EmailProvider != ProviderDisabled || c.SMSProvider != ProviderDisabled || c.Twilio != nil || c.SMTP != nil {
		t.Fatalf("%+v", c)
	}
	m["NOTIFY_PUSH_PROVIDER"] = "disabled"
	delete(m, "FCM_PROJECT_ID")
	if _, err := load(m, ModeAPI); err != nil {
		t.Fatalf("push may be disabled explicitly: %v", err)
	}
}

func TestOptionalAdaptersAreOptIn(t *testing.T) {
	m := base(t, "staging")
	m["NOTIFY_PUSH_PROVIDER"], m["NOTIFY_SMS_PROVIDER"] = "disabled", "twilio"
	if _, err := load(m, ModeAPI); err == nil {
		t.Fatal("twilio selected without settings must fail")
	}
	m["TWILIO_ACCOUNT_SID"], m["TWILIO_AUTH_TOKEN"] = "AC1", "twilio-token"
	if _, err := load(m, ModeAPI); err == nil || !strings.Contains(err.Error(), "TWILIO_FROM") || strings.Contains(err.Error(), "twilio-token") {
		t.Fatalf("sender required, token not leaked: %v", err)
	}
	m["TWILIO_FROM"] = "+15550000000"
	if c, err := load(m, ModeAPI); err != nil || c.Twilio == nil || c.Twilio.BaseURL != "https://api.twilio.com" {
		t.Fatalf("%v", err)
	}
	m = base(t, "production")
	m["NOTIFY_PUSH_PROVIDER"], m["NOTIFY_EMAIL_PROVIDER"], m["SMTP_HOST"], m["SMTP_FROM"], m["SMTP_REQUIRE_TLS"] = "disabled", "smtp", "smtp.example.com", "no-reply@example.com", "false"
	if _, err := load(m, ModeAPI); err == nil {
		t.Fatal("plaintext SMTP must be refused in production")
	}
	m = base(t, "staging")
	m["NOTIFY_SMS_PROVIDER"] = "acme"
	if _, err := load(m, ModeAPI); err == nil {
		t.Fatal("unknown provider names are refused")
	}
}

func TestProductionRefusesCaptureAndWorkerNeedsNATS(t *testing.T) {
	m := base(t, "production")
	m["NOTIFY_PUSH_PROVIDER"], m["NOTIFY_SMS_PROVIDER"] = "disabled", "dev"
	if _, err := load(m, ModeAPI); err == nil || !strings.Contains(err.Error(), "NOTIFY_SMS_PROVIDER=dev") {
		t.Fatalf("dev provider must be refused in production: %v", err)
	}
	m = base(t, "development")
	delete(m, "NATS_URL")
	if _, err := load(m, ModeWorker); err == nil {
		t.Fatal("worker requires NATS_URL")
	}
	if _, err := load(m, ModeAPI); err != nil {
		t.Fatalf("api does not need NATS: %v", err)
	}
	m["NOTIFY_SEND_TIMEOUT"] = "5m"
	if _, err := load(m, ModeAPI); err == nil || strings.Contains(err.Error(), "u:p@") {
		t.Fatalf("expected failure without leaking DSN: %v", err)
	}
}

func TestCaptureFileOnlyForLocalDevAndTest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	for _, env := range []string{"development", "test"} {
		m := base(t, env)
		m["NOTIFY_DEV_CAPTURE_FILE"] = path
		if _, err := load(m, ModeWorker); err != nil {
			t.Fatalf("%s: %v", env, err)
		}
	}
	for _, env := range []string{"staging", "production"} {
		m := base(t, env)
		m["NOTIFY_PUSH_PROVIDER"], m["NOTIFY_DEV_CAPTURE_FILE"] = "disabled", path
		if _, err := load(m, ModeWorker); err == nil || !strings.Contains(err.Error(), "NOTIFY_DEV_CAPTURE_FILE is only allowed") {
			t.Fatalf("%s must refuse the capture file: %v", env, err)
		}
	}
	m := base(t, "development")
	m["NOTIFY_DEV_CAPTURE_FILE"] = "relative.jsonl"
	if _, err := load(m, ModeWorker); err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("relative path accepted: %v", err)
	}
	m = base(t, "development")
	m["NOTIFY_DEV_CAPTURE_FILE"] = path
	m["NOTIFY_PUSH_PROVIDER"], m["NOTIFY_EMAIL_PROVIDER"], m["NOTIFY_SMS_PROVIDER"] = "disabled", "disabled", "disabled"
	if _, err := load(m, ModeWorker); err == nil || !strings.Contains(err.Error(), "requires a dev provider") {
		t.Fatalf("capture file without a dev provider accepted: %v", err)
	}
}
