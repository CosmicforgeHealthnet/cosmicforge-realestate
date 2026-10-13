package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

func baseEnv(t *testing.T) map[string]string {
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, user, _ := ed25519.GenerateKey(nil)
	trusted, _ := json.Marshal([]map[string]any{{"service": "api-gateway", "keys": []map[string]string{{"kid": "gw-1", "public_key": security.EncodeEd25519PublicKey(pub)}}}})
	return map[string]string{
		"DATABASE_URL": "postgres://x@localhost/individual_owner_db", "NOTIFICATION_SERVICE_URL": "http://notification-service:8080",
		"SERVICE_AUTH_KEY_ID": "io-1", "SERVICE_AUTH_PRIVATE_KEY": security.EncodeEd25519Seed(priv), "SERVICE_AUTH_TRUSTED_KEYS": string(trusted),
		"OWNER_ACCESS_TOKEN_KEY_ID": "io-at-1", "OWNER_ACCESS_TOKEN_PRIVATE_KEY": security.EncodeEd25519Seed(user),
		"OWNER_CODE_PEPPER": strings.Repeat("z", 40), "OWNER_MFA_ENCRYPTION_KEY_ID": "mfa-1",
		"OWNER_MFA_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
}

func TestDeliveryRuntimeConfigurationRejectsUnsafeValues(t *testing.T) {
	for _, pair := range [][2]string{
		{"DB_MAX_CONNS", "0"}, {"DB_MAX_CONNS", "4294967297"}, {"HTTP_ADDR", ":0"}, {"HTTP_ADDR", "localhost:65536"},
		{"DATABASE_URL", "postgres://%invalid"}, {"LOG_LEVEL", "verbose"}, {"OUTBOX_POLL_INTERVAL", "0s"},
		{"SHUTDOWN_TIMEOUT", "40s"}, {"OWNER_CODE_TTL", "-1s"}, {"OWNER_PASSWORD_MIN_LENGTH", "1"},
		{"OTEL_TRACES_SAMPLER_RATIO", "NaN"}, {"OTEL_TRACES_SAMPLER_RATIO", "1.1"}, {"OTEL_EXPORTER_OTLP_ENDPOINT", "https://collector"},
		{"NOTIFICATION_SERVICE_URL", "http://secret:password@example.com"}, {"MEDIA_SERVICE_URL", "file:///tmp/media"},
		{"NATS_SUBJECT_PREFIX", "realestate.*"}, {"SERVICE_AUTH_TOKEN_TTL", "0s"}, {"MAPS_PROVIDER", "maps"},
		{"CFMEET_BASE_URL", "https://meet.example.com"}, {"EXTERNAL_LISTING_PROVIDERS", "ical"}, {"MANAGER_SERVICE_URL", "http://manager"},
		{"SERVICE_AUTH_TRUSTED_KEYS", "[]"},
		{"OWNER_PASSWORD_MIN_LENGTH", "129"},
	} {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) {
			env := baseEnv(t)
			env[pair[0]] = pair[1]
			if _, err := Load(sharedcfg.FromMap(env), ModeAPI); err == nil {
				t.Fatal("invalid setting accepted")
			}
		})
	}
	for _, value := range []string{"http://broker", "nats://", "nats://broker/path", "nats://broker:70000", "nats://broker:0"} {
		env := baseEnv(t)
		env["NATS_URL"] = value
		if _, err := Load(sharedcfg.FromMap(env), ModeWorker); err == nil {
			t.Fatal("bad NATS URL accepted")
		}
	}
	env := baseEnv(t)
	env["NATS_URL"] = "nats://localhost:4222,tls://backup:4222"
	if _, err := Load(sharedcfg.FromMap(env), ModeWorker); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "broker-url")
	if err := os.WriteFile(path, []byte(env["NATS_URL"]), 0600); err != nil {
		t.Fatal(err)
	}
	delete(env, "NATS_URL")
	env["NATS_URL_FILE"] = path
	if _, err := Load(sharedcfg.FromMap(env), ModeWorker); err != nil {
		t.Fatal("broker secret file rejected", err)
	}
}

func TestSigningAliasesAndSecretFiles(t *testing.T) {
	env := baseEnv(t)
	key := env["OWNER_ACCESS_TOKEN_PRIVATE_KEY"]
	env["OWNER_AUTH_PRIVATE_KEY"] = key
	if _, err := Load(sharedcfg.FromMap(env), ModeAPI); err != nil {
		t.Fatal("identical alias rejected", err)
	}
	_, other, _ := ed25519.GenerateKey(nil)
	env["OWNER_AUTH_PRIVATE_KEY"] = security.EncodeEd25519Seed(other)
	if _, err := Load(sharedcfg.FromMap(env), ModeAPI); err == nil {
		t.Fatal("conflicting aliases accepted")
	}
	delete(env, "OWNER_ACCESS_TOKEN_PRIVATE_KEY")
	delete(env, "OWNER_AUTH_PRIVATE_KEY")
	path := filepath.Join(t.TempDir(), "owner-key")
	if err := os.WriteFile(path, []byte(key), 0600); err != nil {
		t.Fatal(err)
	}
	env["OWNER_AUTH_PRIVATE_KEY_FILE"] = path
	if _, err := Load(sharedcfg.FromMap(env), ModeAPI); err != nil {
		t.Fatal("file alias rejected", err)
	}
	env["OWNER_AUTH_PRIVATE_KEY"] = key
	if _, err := Load(sharedcfg.FromMap(env), ModeAPI); err == nil {
		t.Fatal("direct/file coexistence accepted")
	}
	delete(env, "OWNER_AUTH_PRIVATE_KEY")
	env["OWNER_AUTH_PRIVATE_KEY_FILE"] = filepath.Join(t.TempDir(), "missing-sensitive-path")
	_, err := Load(sharedcfg.FromMap(env), ModeAPI)
	if err == nil || strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "missing-sensitive-path") {
		t.Fatal("missing file accepted or secret/path leaked")
	}
}

func TestSigningRotationRejectsDuplicateKeyID(t *testing.T) {
	env := baseEnv(t)
	pub, _, _ := ed25519.GenerateKey(nil)
	s := map[string]string{"kid": "retired", "public_key": security.EncodeEd25519PublicKey(pub)}
	b, _ := json.Marshal([]map[string]string{s, s})
	env["OWNER_ACCESS_TOKEN_RETIRED_KEYS"] = string(b)
	if _, err := Load(sharedcfg.FromMap(env), ModeAPI); err == nil {
		t.Fatal("duplicate rotation key accepted")
	}
	env = baseEnv(t)
	peer := map[string]any{"service": "api-gateway", "keys": []map[string]string{s, s}}
	b, _ = json.Marshal([]map[string]any{peer})
	env["SERVICE_AUTH_TRUSTED_KEYS"] = string(b)
	if _, err := Load(sharedcfg.FromMap(env), ModeAPI); err == nil {
		t.Fatal("duplicate peer key accepted")
	}
	peer["service"] = "private-marker-must-not-appear"
	peer["keys"] = []map[string]string{s}
	b, _ = json.Marshal([]map[string]any{peer})
	env["SERVICE_AUTH_TRUSTED_KEYS"] = string(b)
	_, err := Load(sharedcfg.FromMap(env), ModeAPI)
	if err == nil || strings.Contains(err.Error(), "private-marker") {
		t.Fatal("invalid peer accepted or config content leaked")
	}
}

func TestLoadValidConfiguration(t *testing.T) {
	c, err := Load(sharedcfg.FromMap(baseEnv(t)), ModeAPI)
	if err != nil {
		t.Fatal(err)
	}
	if c.TokenIssuer != "individual-owner-service" || c.AccessTokenTTL.Minutes() != 15 || c.Policy.Version == "" || c.HTTPAddr != ":8080" {
		t.Fatalf("%+v", c)
	}
	if strings.Contains(c.DatabaseURL.String(), "postgres") {
		t.Fatal("secret leaked through String()")
	}
}

func TestPasswordWorkFactorsCannotWeakenOrExhaustPolicy(t *testing.T) {
	for _, pair := range [][2]string{{"OWNER_ARGON2_MEMORY_KIB", "8192"}, {"OWNER_ARGON2_MEMORY_KIB", "999999999"}, {"OWNER_ARGON2_ITERATIONS", "1"}, {"OWNER_ARGON2_PARALLELISM", "258"}, {"OWNER_PASSWORD_HASH_CONCURRENCY", "0"}} {
		env := baseEnv(t)
		env[pair[0]] = pair[1]
		if _, err := Load(sharedcfg.FromMap(env), ModeAPI); err == nil {
			t.Fatal("unsafe work factor accepted", pair[0])
		}
	}
	env := baseEnv(t)
	env["OWNER_ARGON2_ITERATIONS"] = "4"
	cfg, err := Load(sharedcfg.FromMap(env), ModeAPI)
	if err != nil || cfg.PasswordParams.Iterations != 4 {
		t.Fatal("stronger parameters unavailable", err)
	}
}

func TestDeployedMFAKeyCannotUseZeroTestFixture(t *testing.T) {
	for _, environment := range []string{"staging", "production"} {
		env := baseEnv(t)
		env["APP_ENV"] = environment
		if _, err := Load(sharedcfg.FromMap(env), ModeAPI); err == nil {
			t.Fatal("all-zero deployed MFA key accepted")
		}
	}
}

func TestLoadFailsFast(t *testing.T) {
	cases := map[string]func(map[string]string){
		"missing pepper":                func(m map[string]string) { delete(m, "OWNER_CODE_PEPPER") },
		"short pepper":                  func(m map[string]string) { m["OWNER_CODE_PEPPER"] = "short" },
		"bad mfa key":                   func(m map[string]string) { m["OWNER_MFA_ENCRYPTION_KEY"] = "abcd" },
		"bad signing key":               func(m map[string]string) { m["OWNER_ACCESS_TOKEN_PRIVATE_KEY"] = "nope" },
		"ttl too long":                  func(m map[string]string) { m["OWNER_ACCESS_TOKEN_TTL"] = "2h" },
		"oauth not available":           func(m map[string]string) { m["OWNER_OAUTH_PROVIDERS"] = "google" },
		"payment not available":         func(m map[string]string) { m["PAYMENT_SERVICE_URL"] = "http://payment-service:8080" },
		"esign not available":           func(m map[string]string) { m["ESIGN_PROVIDER"] = "docusign" },
		"calendar not available":        func(m map[string]string) { m["EXTERNAL_CALENDAR_PROVIDER"] = "ical" },
		"financing not available":       func(m map[string]string) { m["FINANCING_PROVIDER"] = "partner" },
		"insurance not available":       func(m map[string]string) { m["INSURANCE_PROVIDER"] = "partner" },
		"smart access not available":    func(m map[string]string) { m["SMART_ACCESS_PROVIDER"] = "locks" },
		"customer notify not available": func(m map[string]string) { m["CUSTOMER_NOTIFY_SERVICE_URL"] = "http://marketplace-service:8080" },
		"bad fallback":                  func(m map[string]string) { m["OWNER_MFA_FALLBACK_METHODS"] = "WHATSAPP" },
		"bad activation rule":           func(m map[string]string) { m["OWNER_ACTIVATION_REQUIRES"] = "NONE" },
		"missing notification":          func(m map[string]string) { delete(m, "NOTIFICATION_SERVICE_URL") },
		"step-up too long":              func(m map[string]string) { m["OWNER_STEP_UP_WINDOW"] = "2h" },
		"bad retired keys": func(m map[string]string) {
			m["OWNER_ACCESS_TOKEN_RETIRED_KEYS"] = "[{\"kid\":\"x\",\"public_key\":\"bad\"}]"
		},
	}
	for name, mutate := range cases {
		env := baseEnv(t)
		mutate(env)
		if _, err := Load(sharedcfg.FromMap(env), ModeAPI); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	env := baseEnv(t)
	if _, err := Load(sharedcfg.FromMap(env), ModeWorker); err == nil {
		t.Error("worker requires NATS_URL")
	}
}
