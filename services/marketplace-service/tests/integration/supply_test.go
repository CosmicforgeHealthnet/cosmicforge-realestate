package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/marketplace-service/internal/supply"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/marketplace-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func hash(t *testing.T, raw []byte) string {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	c, _ := json.Marshal(v)
	s := sha256.Sum256(c)
	return hex.EncodeToString(s[:])
}

// Concurrent deliveries of one command produce exactly one durable
// acceptance; accepted commands are append-only; validation expiry and
// connection identity are enforced against real PostgreSQL.
func TestMarketplaceSupplyExactlyOnceAppendOnlyAndExpiry(t *testing.T) {
	db := testkit.NewDatabase(t, migrations.FS)
	now := time.Now().UTC()
	svc := &supply.Service{Pool: db.Pool, TTL: time.Hour, Now: func() time.Time { return now }}
	org := ids.New()
	conn := supply.Connection{ID: ids.New(), OrganisationID: org, PropertyID: ids.New(), ProviderAccountID: "cf-first-party", CredentialReference: "vault:marketplace"}
	if _, err := svc.Validate(t.Context(), org, conn); err != nil {
		t.Fatal(err)
	}
	moved := conn
	moved.PropertyID = ids.New()
	if _, err := svc.Validate(t.Context(), org, moved); err == nil {
		t.Fatal("connection rebound to another property")
	}
	if _, err := svc.Validate(t.Context(), ids.New(), conn); err == nil {
		t.Fatal("connection validated for a different organisation")
	}
	payload := json.RawMessage(`{"title":"Harbour Suites","b":1,"a":2}`)
	cmd := supply.Command{ContractVersion: supply.ContractVersion, JobID: ids.New(), Connection: conn, Kind: "CONTENT", CanonicalVersion: 1, IdempotencyKey: "content:v1:concurrent", PayloadHash: hash(t, payload), Payload: payload}
	var wg sync.WaitGroup
	acks := make([]supply.Acknowledgement, 8)
	errs := make([]error, 8)
	for i := range acks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			acks[i], errs[i] = svc.Apply(t.Context(), org, cmd)
		}(i)
	}
	wg.Wait()
	var accepted string
	for i, err := range errs {
		if err != nil {
			continue
		}
		if accepted == "" {
			accepted = acks[i].ProviderRequestID
		}
		if acks[i].ProviderRequestID != accepted {
			t.Fatalf("two acceptances for one command: %s %s", accepted, acks[i].ProviderRequestID)
		}
	}
	var n int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM supply_commands WHERE idempotency_key=$1`, cmd.IdempotencyKey).Scan(&n); err != nil || n != 1 || accepted == "" {
		t.Fatalf("accepted %d %q %v", n, accepted, err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE supply_commands SET state='WITHDRAWN'`); err == nil {
		t.Fatal("accepted command rewritten")
	}
	if _, err := db.Pool.Exec(t.Context(), `DELETE FROM supply_commands`); err == nil {
		t.Fatal("accepted command deleted")
	}
	if _, err := svc.Apply(t.Context(), ids.New(), cmd); err == nil {
		t.Fatal("command applied for another organisation")
	}
	now = now.Add(2 * time.Hour)
	next := cmd
	next.CanonicalVersion, next.IdempotencyKey = 2, "content:v2:expired"
	if _, err := svc.Apply(t.Context(), org, next); err == nil {
		t.Fatal("command accepted on an expired connection validation")
	}
	if _, err := svc.Validate(t.Context(), org, conn); err != nil {
		t.Fatal(err)
	}
	if ack, err := svc.Apply(t.Context(), org, next); err != nil || ack.State != "PUBLISHED" {
		t.Fatalf("after revalidation %+v %v", ack, err)
	}
}
