package config

import (
	"encoding/json"
	"strings"
	"testing"

	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func base(t *testing.T) map[string]string {
	k := testkit.SigningKey(t, "io-1")
	issuers, _ := json.Marshal([]map[string]any{{"issuer": "individual-owner-service", "domain": "individual-owner",
		"keys": []map[string]string{{"kid": "io-1", "public_key": security.EncodeEd25519PublicKey(k.PublicKey())}}}})
	gw := testkit.SigningKey(t, "gw-1")
	return map[string]string{
		"APP_ENV": "development", "REDIS_ADDR": "127.0.0.1:6379",
		"GATEWAY_TRUSTED_ISSUERS": string(issuers),
		"SERVICE_AUTH_KEY_ID":     "gw-1", "SERVICE_AUTH_PRIVATE_KEY": security.EncodeEd25519Seed(gw.PrivateKey),
		"GATEWAY_UPSTREAM_VERIFICATION_SERVICE_URL": "http://verification-service:8080",
		"GATEWAY_WEBHOOK_ROUTES":                    "sandbox-kyc=verification-service",
		"GATEWAY_TRUSTED_PROXIES":                   "10.0.0.0/8",
	}
}

func TestLoadValid(t *testing.T) {
	c, err := Load(sharedcfg.FromMap(base(t)))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Routes) != 2 || len(c.TrustedIssuers) != 1 || c.Signer == nil || !c.RateLimits["auth-sensitive"].FailClosed {
		t.Fatalf("%+v", c)
	}
}

func TestLoadFailsFast(t *testing.T) {
	m := base(t)
	delete(m, "REDIS_ADDR")
	delete(m, "GATEWAY_TRUSTED_ISSUERS")
	if _, err := Load(sharedcfg.FromMap(m)); err == nil || !strings.Contains(err.Error(), "REDIS_ADDR") || !strings.Contains(err.Error(), "GATEWAY_TRUSTED_ISSUERS") {
		t.Fatalf("expected missing config error, got %v", err)
	}
	m = base(t)
	m["APP_ENV"] = "production"
	m["GATEWAY_CORS_ALLOWED_ORIGINS"] = "*"
	if _, err := Load(sharedcfg.FromMap(m)); err == nil {
		t.Fatal("wildcard CORS accepted in production")
	}
	m = base(t)
	m["SERVICE_AUTH_PRIVATE_KEY"] = "secret-but-invalid"
	_, err := Load(sharedcfg.FromMap(m))
	if err == nil || strings.Contains(err.Error(), "secret-but-invalid") {
		t.Fatalf("invalid key: %v", err)
	}
	m = base(t)
	m["GATEWAY_RATE_LIMIT_PUBLIC"] = "lots"
	if _, err := Load(sharedcfg.FromMap(m)); err == nil {
		t.Fatal("invalid rate limit accepted")
	}
	m = base(t)
	m["GATEWAY_WEBHOOK_ROUTES"] = "x=identity-service"
	if _, err := Load(sharedcfg.FromMap(m)); err == nil {
		t.Fatal("invalid webhook route accepted")
	}
}
