package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
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

func TestLoadFailsFast(t *testing.T) {
	cases := map[string]func(map[string]string){
		"missing pepper":       func(m map[string]string) { delete(m, "OWNER_CODE_PEPPER") },
		"short pepper":         func(m map[string]string) { m["OWNER_CODE_PEPPER"] = "short" },
		"bad mfa key":          func(m map[string]string) { m["OWNER_MFA_ENCRYPTION_KEY"] = "abcd" },
		"bad signing key":      func(m map[string]string) { m["OWNER_ACCESS_TOKEN_PRIVATE_KEY"] = "nope" },
		"ttl too long":         func(m map[string]string) { m["OWNER_ACCESS_TOKEN_TTL"] = "2h" },
		"oauth not available":  func(m map[string]string) { m["OWNER_OAUTH_PROVIDERS"] = "google" },
		"bad fallback":         func(m map[string]string) { m["OWNER_MFA_FALLBACK_METHODS"] = "WHATSAPP" },
		"bad activation rule":  func(m map[string]string) { m["OWNER_ACTIVATION_REQUIRES"] = "NONE" },
		"missing notification": func(m map[string]string) { delete(m, "NOTIFICATION_SERVICE_URL") },
		"step-up too long":     func(m map[string]string) { m["OWNER_STEP_UP_WINDOW"] = "2h" },
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
