package e2e

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// canonicalHash mirrors the Business distribution.PayloadHash contract.
func canonicalHash(t *testing.T, v any) (json.RawMessage, string) {
	t.Helper()
	raw, _ := json.Marshal(v)
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		t.Fatal(err)
	}
	canonical, _ := json.Marshal(value)
	sum := sha256.Sum256(canonical)
	return raw, hex.EncodeToString(sum[:])
}

// TestMarketplaceBusinessSupplyContractRealProcess runs the real
// marketplace-service binaries (migrate + api) against their own PostgreSQL
// database and drives the business-supply.v1 contract exactly as the
// Business Operations MarketplaceClient does: signed service JWTs with the
// organisation delegation, Idempotency-Key, canonical payload hashes and
// authoritative readback. The Business side is represented by its signed
// identity (a contract client), not a running business-operations process.
func TestMarketplaceBusinessSupplyContractRealProcess(t *testing.T) {
	testkit.PostgresAdminURL(t)
	p := &platformEnv{t: t, internal: map[platform.Service]string{}, signers: map[platform.Service]*serviceauth.Signer{}}
	var registry []map[string]any
	keys := map[platform.Service]key{}
	for _, s := range []platform.Service{platform.MarketplaceService, platform.BusinessOperationsService, platform.VerificationService} {
		k := newKey(string(s) + "-supply-e2e")
		keys[s] = k
		registry = append(registry, map[string]any{"service": s, "keys": []map[string]string{{"kid": k.kid, "public_key": security.EncodeEd25519PublicKey(k.priv.Public().(ed25519.PublicKey))}}})
		p.signers[s], _ = serviceauth.NewSigner(serviceauth.SignerConfig{Service: s, Key: k.signing()})
	}
	trusted, _ := json.Marshal(registry)
	db := testkit.NewDatabase(t, nil).URL
	t.Cleanup(p.stop)
	p.run("marketplace-service-migrate", map[string]string{"DATABASE_URL": db}, "up")
	p.run("marketplace-service-migrate", map[string]string{"DATABASE_URL": db}, "up")
	mk := keys[platform.MarketplaceService]
	addr := freeAddr(t)
	userKey := newKey("marketplace-user-tokens")
	env := map[string]string{"APP_ENV": "test", "LOG_LEVEL": "warn", "DATABASE_URL": db, "HTTP_ADDR": addr,
		"SERVICE_AUTH_KEY_ID": mk.kid, "SERVICE_AUTH_PRIVATE_KEY": security.EncodeEd25519Seed(mk.priv), "SERVICE_AUTH_TRUSTED_KEYS": string(trusted),
		"USER_TOKEN_KEY_ID": userKey.kid, "USER_TOKEN_PRIVATE_KEY": security.EncodeEd25519Seed(userKey.priv)}
	p.start("marketplace api", "marketplace-service-api", env)
	base := "http://" + addr
	waitReady(t, base)

	org, otherOrg := ids.New(), ids.New()
	call := func(caller platform.Service, delegatedOrg ids.ID, method, path string, body any, idem string) response {
		t.Helper()
		tok, err := p.signers[caller].TokenFor(string(platform.MarketplaceService), serviceauth.Delegation{OrganizationID: delegatedOrg})
		if err != nil {
			t.Fatal(err)
		}
		return send(t, method, base+path, tok, "", body, idem)
	}
	conn := map[string]any{"connection_id": ids.New(), "organisation_id": org, "property_id": ids.New(), "provider_account_id": "cf-first-party:" + org.String(), "credential_reference": "vault:marketplace/first-party"}
	connID := conn["connection_id"].(ids.ID)

	// Only Business Operations, only for its signed organisation.
	if r := call(platform.VerificationService, org, "POST", "/internal/v1/business-supply/connections/validate", conn, ""); r.status != 403 {
		t.Fatalf("non-business caller %d %s", r.status, r.body)
	}
	if r := call(platform.BusinessOperationsService, otherOrg, "POST", "/internal/v1/business-supply/connections/validate", conn, ""); r.status != 400 {
		t.Fatalf("organisation mismatch %d %s", r.status, r.body)
	}
	r := call(platform.BusinessOperationsService, org, "POST", "/internal/v1/business-supply/connections/validate", conn, "validate:1")
	var auth struct {
		Reference    string   `json:"validation_reference"`
		Capabilities []string `json:"capabilities"`
	}
	r.data(t, &auth)
	if r.status != 200 || auth.Reference == "" || len(auth.Capabilities) == 0 {
		t.Fatalf("validate %d %s", r.status, r.body)
	}

	command := func(kind string, version int64, payload any, key string) (map[string]any, string) {
		raw, hash := canonicalHash(t, payload)
		return map[string]any{"contract_version": "business-supply.v1", "job_id": ids.New(), "connection": conn, "kind": kind, "canonical_version": version, "idempotency_key": key, "payload_hash": hash, "payload": raw}, hash
	}
	type ack struct {
		ProviderRequestID string `json:"provider_request_id"`
		State             string `json:"state"`
		Durable           bool   `json:"durable"`
		CanonicalVersion  int64  `json:"canonical_version"`
		PayloadHash       string `json:"payload_hash"`
	}
	content := map[string]any{"title": "Lagoon View Apartments", "units": 12, "amenities": []string{"pool", "wifi"}}
	c1, h1 := command("CONTENT", 1, content, "content:v1:0001")
	r = call(platform.BusinessOperationsService, org, "POST", "/internal/v1/business-supply/commands", c1, "content:v1:0001")
	var a1 ack
	r.data(t, &a1)
	if r.status != 200 || a1.State != "PUBLISHED" || !a1.Durable || a1.PayloadHash != h1 {
		t.Fatalf("apply %d %s", r.status, r.body)
	}
	// Exactly-once replay returns the original acknowledgement.
	r = call(platform.BusinessOperationsService, org, "POST", "/internal/v1/business-supply/commands", c1, "content:v1:0001")
	var replay ack
	r.data(t, &replay)
	if replay.ProviderRequestID != a1.ProviderRequestID {
		t.Fatalf("replay produced a second acceptance %s %s", replay.ProviderRequestID, a1.ProviderRequestID)
	}
	reused, _ := command("CONTENT", 1, map[string]any{"title": "Different"}, "content:v1:0001")
	if r = call(platform.BusinessOperationsService, org, "POST", "/internal/v1/business-supply/commands", reused, "content:v1:0001"); r.status != 409 {
		t.Fatalf("idempotency key reuse %d %s", r.status, r.body)
	}
	tampered, _ := command("CONTENT", 2, content, "content:v2:tamper")
	tampered["payload_hash"] = h1[:63] + "0"
	if r = call(platform.BusinessOperationsService, org, "POST", "/internal/v1/business-supply/commands", tampered, "content:v2:tamper"); r.status != 400 {
		t.Fatalf("hash mismatch %d %s", r.status, r.body)
	}
	stale, _ := command("CONTENT", 1, content, "content:v1:again")
	if r = call(platform.BusinessOperationsService, org, "POST", "/internal/v1/business-supply/commands", stale, "content:v1:again"); r.status != 409 {
		t.Fatalf("stale version %d %s", r.status, r.body)
	}
	content["title"] = "Lagoon View Residences"
	c2, h2 := command("CONTENT", 2, content, "content:v2:0002")
	if r = call(platform.BusinessOperationsService, org, "POST", "/internal/v1/business-supply/commands", c2, "content:v2:0002"); r.status != 200 {
		t.Fatalf("v2 %d %s", r.status, r.body)
	}
	ari, _ := command("ARI", 1, map[string]any{"product_id": ids.New(), "days": []map[string]any{{"date": "2027-01-10", "available": 3, "rate_minor": 8500000, "currency": "NGN"}}}, "ari:v1:0001")
	r = call(platform.BusinessOperationsService, org, "POST", "/internal/v1/business-supply/commands", ari, "ari:v1:0001")
	var aAri ack
	r.data(t, &aAri)
	if aAri.State != "SYNCHRONIZED" {
		t.Fatalf("ari %s", r.body)
	}
	// Storefront readback shows the latest canonical content.
	listing := func() map[string]any {
		r := call(platform.BusinessOperationsService, org, "GET", "/internal/v1/business-supply/connections/"+connID.String()+"/listing", nil, "")
		var out struct {
			Projections []struct {
				Kind             string         `json:"kind"`
				CanonicalVersion int64          `json:"canonical_version"`
				PayloadHash      string         `json:"payload_hash"`
				PublicationState string         `json:"publication_state"`
				Payload          map[string]any `json:"payload"`
			} `json:"projections"`
		}
		r.data(t, &out)
		m := map[string]any{}
		for _, pr := range out.Projections {
			m[pr.Kind] = pr
			if pr.Kind == "CONTENT" && (pr.CanonicalVersion != 2 || pr.PayloadHash != h2 || pr.Payload["title"] != "Lagoon View Residences") {
				t.Fatalf("content projection %+v", pr)
			}
			m[pr.Kind+":state"] = pr.PublicationState
		}
		return m
	}
	if m := listing(); m["CONTENT:state"] != "PUBLISHED" || m["ARI:state"] != "PUBLISHED" {
		t.Fatalf("listing %v", m)
	}
	// Authoritative readback; unknown commands are not fabricated.
	r = call(platform.BusinessOperationsService, org, "GET", "/internal/v1/business-supply/commands/content:v1:0001?connection_id="+connID.String(), nil, "")
	var rb ack
	r.data(t, &rb)
	if rb.ProviderRequestID != a1.ProviderRequestID || !rb.Durable {
		t.Fatalf("readback %s", r.body)
	}
	if r = call(platform.BusinessOperationsService, org, "GET", "/internal/v1/business-supply/commands/never-sent-key?connection_id="+connID.String(), nil, ""); r.status != 404 {
		t.Fatalf("unknown readback %d", r.status)
	}
	if r = call(platform.BusinessOperationsService, otherOrg, "GET", "/internal/v1/business-supply/connections/"+connID.String()+"/listing", nil, ""); r.status != 404 {
		t.Fatalf("cross-organisation listing %d", r.status)
	}
	// Mapping proof and empty event feed with a stable cursor.
	r = call(platform.BusinessOperationsService, org, "POST", "/internal/v1/business-supply/connections/"+connID.String()+"/mapping-inspection", map[string]any{"kind": "PROPERTY", "external_id": "mkt-listing-1", "canonical_id": conn["property_id"], "canonical_version": 1}, "")
	var proof struct {
		PropertyExternalID string `json:"property_external_id"`
		ProviderAccountID  string `json:"provider_account_id"`
	}
	r.data(t, &proof)
	if proof.ProviderAccountID != conn["provider_account_id"] || proof.PropertyExternalID == "" {
		t.Fatalf("mapping proof %s", r.body)
	}
	r = call(platform.BusinessOperationsService, org, "GET", "/internal/v1/business-supply/connections/"+connID.String()+"/events?cursor=0", nil, "")
	var poll struct {
		NextCursor string            `json:"next_cursor"`
		Events     []json.RawMessage `json:"events"`
	}
	r.data(t, &poll)
	if poll.NextCursor != "0" || len(poll.Events) != 0 {
		t.Fatalf("poll %s", r.body)
	}
	// Pause and withdraw propagate to the storefront projection.
	pause, _ := command("PAUSE", 3, map[string]any{"reason": "renovation"}, "pause:v3:0001")
	if r = call(platform.BusinessOperationsService, org, "POST", "/internal/v1/business-supply/commands", pause, "pause:v3:0001"); r.status != 200 {
		t.Fatalf("pause %d %s", r.status, r.body)
	}
	if m := listing(); m["CONTENT:state"] != "PAUSED" {
		t.Fatalf("paused listing %v", m)
	}
	// Restart recovery: acceptances survive a process restart.
	p.stop()
	p.procs = nil
	addr = freeAddr(t)
	env["HTTP_ADDR"] = addr
	base = "http://" + addr
	p.start("marketplace api restarted", "marketplace-service-api", env)
	waitReady(t, base)
	r = call(platform.BusinessOperationsService, org, "GET", "/internal/v1/business-supply/commands/pause:v3:0001?connection_id="+connID.String(), nil, "")
	var after ack
	r.data(t, &after)
	if after.State != "PAUSED" || !after.Durable {
		t.Fatalf("readback after restart %s", r.body)
	}
	withdraw, _ := command("WITHDRAW", 4, map[string]any{"reason": "sold"}, "withdraw:v4:0001")
	if r = call(platform.BusinessOperationsService, org, "POST", "/internal/v1/business-supply/commands", withdraw, "withdraw:v4:0001"); r.status != 200 {
		t.Fatalf("withdraw %d %s", r.status, r.body)
	}
	if m := listing(); m["CONTENT:state"] != "WITHDRAWN" {
		t.Fatalf("withdrawn listing %v", m)
	}
}
