package config

import (
	"encoding/json"
	"strings"
	"testing"

	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func env(t *testing.T) map[string]string {
	self := testkit.SigningKey(t, "vs-1")
	gw := testkit.SigningKey(t, "gw-1")
	reg, _ := json.Marshal([]map[string]any{{"service": "api-gateway", "keys": []map[string]string{{"kid": "gw-1", "public_key": security.EncodeEd25519PublicKey(gw.PublicKey())}}}})
	return map[string]string{
		"APP_ENV": "staging", "DATABASE_URL": "postgres://u:p@db/verification_db", "MEDIA_SERVICE_URL": "http://media-document-service:8080",
		"NATS_URL": "nats://nats:4222", "SANDBOX_KYC_CALLBACK_SECRET": "s3cr3t",
		"SERVICE_AUTH_KEY_ID": "vs-1", "SERVICE_AUTH_PRIVATE_KEY": security.EncodeEd25519Seed(self.PrivateKey), "SERVICE_AUTH_TRUSTED_KEYS": string(reg),
	}
}

func TestLoad(t *testing.T) {
	c, err := Load(sharedcfg.FromMap(env(t)), ModeWorker)
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultProvider != "sandbox-kyc" || c.Identity == nil || c.Validity["IDENTITY"] <= 0 {
		t.Fatalf("%+v", c)
	}
	m := env(t)
	delete(m, "NATS_URL")
	if _, err := Load(sharedcfg.FromMap(m), ModeWorker); err == nil {
		t.Fatal("worker requires NATS_URL")
	}
	if _, err := Load(sharedcfg.FromMap(m), ModeAPI); err != nil {
		t.Fatalf("api does not need NATS: %v", err)
	}
	m = env(t)
	m["APP_ENV"] = "production"
	_, err = Load(sharedcfg.FromMap(m), ModeAPI)
	if err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("sandbox must be refused in production: %v", err)
	}
	m = env(t)
	delete(m, "DATABASE_URL")
	m["VERIFICATION_VALIDITY_IDENTITY"] = "-1h"
	if _, err := Load(sharedcfg.FromMap(m), ModeAPI); err == nil || strings.Contains(err.Error(), "u:p@") {
		t.Fatalf("expected failure without leaking DSN: %v", err)
	}
}
