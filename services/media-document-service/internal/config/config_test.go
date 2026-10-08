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
	self := testkit.SigningKey(t, "md-1")
	gw := testkit.SigningKey(t, "gw-1")
	reg, _ := json.Marshal([]map[string]any{{"service": "api-gateway", "keys": []map[string]string{{"kid": "gw-1", "public_key": security.EncodeEd25519PublicKey(gw.PublicKey())}}}})
	return map[string]string{
		"APP_ENV": "staging", "DATABASE_URL": "postgres://u:p@db/media_document_db", "NATS_URL": "nats://nats:4222",
		"STORAGE_REGION": "auto", "STORAGE_BUCKET": "cf-media", "STORAGE_ACCESS_KEY_ID": "ak", "STORAGE_SECRET_ACCESS_KEY": "sk",
		"MEDIA_SCANNER": "clamav", "CLAMAV_ADDR": "clamav:3310",
		"SERVICE_AUTH_KEY_ID": "md-1", "SERVICE_AUTH_PRIVATE_KEY": security.EncodeEd25519Seed(self.PrivateKey), "SERVICE_AUTH_TRUSTED_KEYS": string(reg),
	}
}

func TestLoad(t *testing.T) {
	c, err := Load(sharedcfg.FromMap(env(t)), ModeWorker)
	if err != nil {
		t.Fatal(err)
	}
	if c.StorageSecretKey.Reveal() != "sk" || c.Policies["IMAGE"].MaxBytes != 15<<20 {
		t.Fatalf("%+v", c)
	}
	m := env(t)
	m["APP_ENV"] = "production"
	m["MEDIA_SCANNER"] = "dev"
	if _, err := Load(sharedcfg.FromMap(m), ModeAPI); err == nil || !strings.Contains(err.Error(), "dev") {
		t.Fatal("dev scanner must be refused in production")
	}
	m = env(t)
	delete(m, "CLAMAV_ADDR")
	if _, err := Load(sharedcfg.FromMap(m), ModeWorker); err == nil {
		t.Fatal("clamav address required for worker")
	}
	m = env(t)
	m["MEDIA_MAX_BYTES_IMAGE"] = "1024"
	c, _ = Load(sharedcfg.FromMap(m), ModeAPI)
	if c.Policies["IMAGE"].MaxBytes != 1024 {
		t.Fatal("size override ignored")
	}
	m = env(t)
	delete(m, "STORAGE_SECRET_ACCESS_KEY")
	if _, err := Load(sharedcfg.FromMap(m), ModeAPI); err == nil {
		t.Fatal("missing storage credentials accepted")
	}
}
