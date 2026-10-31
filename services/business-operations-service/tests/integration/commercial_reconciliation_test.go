package integration

import (
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"testing"
	"time"
)

func TestBusinessCommercialARIOverrideOutageRecoveryHealthAndPeriodicReadback(t *testing.T) {
	c := newCommercialHTTP(t)
	consumer, client := newCommercialSupplyConsumer(t)
	conn := c.connectMarketplace(client)
	_, first := c.publishListing(conn)
	if e := c.domain.ProcessChannelSync(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	if c.syncState(first) != "SUCCEEDED" {
		t.Fatal("First publication failed")
	}
	// Drain the initial history watermark before changing a field.
	if e := c.domain.ProcessCommercialMaintenance(t.Context(), 100); e != nil {
		t.Fatal(e)
	}
	if e := c.domain.ProcessChannelSync(t.Context(), 5); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(time.Hour)
	field := businesshttp.CommercialFieldAuthority{Field: "title", Mode: "MANUAL_OVERRIDE", Value: json.RawMessage(`"A reviewed channel-specific title"`), SourceReference: "operator:content-review-1", ExpiresAt: &deadline, Reason: "Apply an independently reviewed channel title", Version: 0}
	field.ApprovalID = c.approval("COMMERCIAL_CHANNEL_FIELD_OVERRIDE", "channels:approve", distribution.Marketplace, nil, "", map[string]any{"connection_id": conn.ID, "command": field})
	c.must(c.owner, "PUT", "/channels/"+conn.ID.String()+"/field-authority", field, 200)
	consumer.mu.Lock()
	consumer.offline = true
	consumer.mu.Unlock()
	if e := c.domain.ProcessCommercialMaintenance(t.Context(), 100); e != nil {
		t.Fatal(e)
	}
	var job ids.ID
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT id FROM channel_sync_jobs WHERE connection_id=$1 AND kind='ARI' ORDER BY created_at DESC,id DESC LIMIT 1`, conn.ID).Scan(&job); e != nil {
		t.Fatal(e)
	}
	if e := c.domain.ProcessChannelSync(t.Context(), 5); e != nil {
		t.Fatal(e)
	}
	if c.syncState(job) != "RETRY" {
		t.Fatalf("ARI outage not durable: %s", c.syncState(job))
	}
	health := commercialData[map[string]any](t, c.must(c.owner, "GET", "/channels/"+conn.ID.String()+"/sync-health", nil, 200))
	if health["status"] != "DEGRADED" {
		t.Fatalf("Outage health %#v", health)
	}
	consumer.mu.Lock()
	consumer.offline = false
	consumer.mu.Unlock()
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE channel_sync_jobs SET available_at=now() WHERE id=$1`, job); e != nil {
		t.Fatal(e)
	}
	if e := c.domain.ProcessChannelSync(t.Context(), 5); e != nil {
		t.Fatal(e)
	}
	if c.syncState(job) != "SUCCEEDED" {
		var reason string
		_ = c.db.Pool.QueryRow(t.Context(), `SELECT last_error FROM channel_sync_jobs WHERE id=$1`, job).Scan(&reason)
		t.Fatalf("ARI recovery %s %s", c.syncState(job), reason)
	}
	var synced bool
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT last_synced_at IS NOT NULL FROM channel_field_authority WHERE connection_id=$1 AND field='title'`, conn.ID).Scan(&synced); e != nil || !synced {
		t.Fatalf("Override sync evidence %v %v", synced, e)
	}
	consumer.mu.Lock()
	found := false
	for _, raw := range consumer.payloads {
		var value struct {
			Content businesshttp.CommercialContent  `json:"content"`
			ARI     []businesshttp.CommercialARIDay `json:"ari"`
			Pricing json.RawMessage                 `json:"pricing_context"`
		}
		if json.Unmarshal(raw, &value) == nil && value.Content.Title == "A reviewed channel-specific title" && len(value.ARI) > 0 && len(value.Pricing) > 0 {
			found = true
		}
	}
	before := consumer.applied
	consumer.mu.Unlock()
	if !found {
		t.Fatal("ARI omitted reviewed override or exact pricing context")
	}
	// A quiet connection still receives periodic readback without another write.
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE channel_sync_jobs SET last_success_at=now()-interval '6 minutes' WHERE connection_id=$1 AND state='SUCCEEDED'`, conn.ID); e != nil {
		t.Fatal(e)
	}
	if e := c.domain.ProcessCommercialMaintenance(t.Context(), 100); e != nil {
		t.Fatal(e)
	}
	if e := c.domain.ProcessChannelSync(t.Context(), 5); e != nil {
		t.Fatal(e)
	}
	consumer.mu.Lock()
	after := consumer.applied
	consumer.mu.Unlock()
	if before != after {
		t.Fatalf("Readback replayed mutation %d -> %d", before, after)
	}
}
func TestBusinessCommercialPollCursorPoisonQuarantineAndReviewedConflictRejection(t *testing.T) {
	c := newCommercialHTTP(t)
	consumer, client := newCommercialSupplyConsumer(t)
	conn := c.connectMarketplace(client)
	event := distribution.Receipt{ProviderEventID: "poll-event", ExternalObjectID: "remote-content", ExternalVersion: "v1", Kind: "CONTENT_CHANGED", OccurredAt: time.Now().UTC(), Reference: "provider:review"}
	consumer.mu.Lock()
	consumer.pollPages[""] = distribution.PollResult{Cursor: "", NextCursor: "next-1", Events: []distribution.Receipt{event}}
	consumer.mu.Unlock()
	if e := c.domain.PollChannel(t.Context(), c.owner.registered.OrganisationID, conn.ID); e != nil {
		t.Fatal(e)
	}
	// The same object/version arriving with changed immutable content is poison.
	changed := event
	changed.Reference = "provider:changed-facts"
	consumer.mu.Lock()
	consumer.pollPages["next-1"] = distribution.PollResult{Cursor: "next-1", NextCursor: "must-not-advance", Events: []distribution.Receipt{changed}}
	consumer.mu.Unlock()
	if e := c.domain.PollChannel(t.Context(), c.owner.registered.OrganisationID, conn.ID); e == nil {
		t.Fatal("Poison page accepted")
	}
	var cursor string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT poll_cursor FROM channel_sync_cursors WHERE connection_id=$1`, conn.ID).Scan(&cursor); e != nil || cursor != "next-1" {
		t.Fatalf("Poison advanced cursor %s %v", cursor, e)
	}
	var conflict ids.ID
	var version int64
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT id,version FROM channel_conflicts WHERE connection_id=$1 AND classification='PROVIDER_EVENT_IDENTITY_COLLISION'`, conn.ID).Scan(&conflict, &version); e != nil {
		t.Fatal(e)
	}
	decision := businesshttp.CommercialConflictDecision{Action: "REJECT", Version: version, Reason: "Reject the forged conflicting provider version after investigation"}
	decision.ApprovalID = c.approval("COMMERCIAL_CHANNEL_CONFLICT_RESOLUTION", "channels:approve", distribution.Marketplace, nil, "", map[string]any{"conflict_id": conflict, "command": decision})
	c.must(c.owner, "POST", "/channel-conflicts/"+conflict.String()+"/resolve", decision, 202)
	detail := commercialData[struct {
		Conflict businesshttp.CommercialChannelConflict `json:"conflict"`
		History  []json.RawMessage                      `json:"history"`
	}](t, c.must(c.owner, "GET", "/channel-conflicts/"+conflict.String(), nil, 200))
	if detail.Conflict.State != "REJECTED" || len(detail.History) != 1 {
		t.Fatalf("Missing immutable decision %#v", detail)
	}
	consumer.mu.Lock()
	consumer.pollPages["next-1"] = distribution.PollResult{Cursor: "next-1", NextCursor: "next-2", Events: []distribution.Receipt{event}}
	consumer.mu.Unlock()
	if e := c.domain.PollChannel(t.Context(), c.owner.registered.OrganisationID, conn.ID); e != nil {
		t.Fatal(e)
	}
	var receipts int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM channel_event_receipts WHERE connection_id=$1`, conn.ID).Scan(&receipts); e != nil || receipts != 1 {
		t.Fatalf("Repeated page mutated canonical data %d %v", receipts, e)
	}
}

func TestBusinessCommercialAllotmentReleaseExpiryAndCrossTenantChannelIsolation(t *testing.T) {
	c := newCommercialHTTP(t)
	_, adapter := newCommercialSupplyConsumer(t)
	conn := c.connectMarketplace(adapter)
	command := businesshttp.CommercialAllotmentCreate{PoolID: c.pool.ID, PoolVersion: c.pool.Version, Channel: distribution.Marketplace, Quantity: 1, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, ReleaseAt: time.Now().UTC().Add(time.Minute), Reason: "Reserve the existing physical stock for the approved channel"}
	allotment := commercialData[businesshttp.CommercialAllotment](t, c.must(c.owner, "POST", "/allotments", command, 201))
	assertFree := func(channel string, want int) {
		t.Helper()
		days, e := inventory.Calendar(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.pool.ID, c.dates, c.property.Timezone, channel, c.service.Now(), false)
		if e != nil {
			t.Fatal(e)
		}
		for _, day := range days {
			if day.Available != want {
				t.Fatalf("Channel %s free %d want %d", channel, day.Available, want)
			}
		}
	}
	assertFree("", 0)
	assertFree(distribution.Marketplace, 1)
	c.must(c.owner, "POST", "/allotments", command, 409)
	c.must(c.owner, "POST", "/allotments/"+allotment.ID.String()+"/release", businesshttp.CommercialStateChange{Version: allotment.Version, Reason: "Release reserved channel capacity to shared stock"}, 200)
	assertFree("", 1)
	c.must(c.owner, "POST", "/allotments", command, 201)
	originalNow := c.service.Now
	c.service.Now = func() time.Time { return originalNow().Add(2 * time.Minute) }
	defer func() { c.service.Now = originalNow }()
	assertFree("", 1)
	if e := c.domain.ProcessCommercialMaintenance(t.Context(), 100); e != nil {
		t.Fatal(e)
	}
	var expired int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM inventory_allotments WHERE pool_id=$1 AND state='EXPIRED'`, c.pool.ID).Scan(&expired); e != nil || expired != 1 {
		t.Fatalf("Allotment expiry %d %v", expired, e)
	}
	c.service.Now = originalNow
	outsider := c.user()
	c.secure(outsider)
	for _, path := range []string{"/channels/" + conn.ID.String(), "/channels/" + conn.ID.String() + "/mappings", "/rate-plans/" + c.rate.ID.String(), "/inventory-pools/" + c.pool.ID.String()} {
		c.must(outsider, "GET", path, nil, 404)
	}
}

func TestBusinessCommercialMappingCollisionCannotRebindExistingProviderStock(t *testing.T) {
	c := newCommercialHTTP(t)
	_, adapter := newCommercialSupplyConsumer(t)
	conn := c.connectMarketplace(adapter)
	alias := commercialData[inventory.Product](t, c.must(c.owner, "POST", "/sellable-products", businesshttp.CommercialProductCreate{PropertyID: c.property.ID, NodeID: c.unit.ID, PoolID: c.pool.ID, RatePlanID: &c.rate.ID, Name: "Alternative packaging of the same physical stock", MinGuests: 1, MaxGuests: 2, PolicyCode: "ALTERNATIVE", PolicyVersion: 1}, 201))
	entry := distribution.MappingReference{Kind: "PRODUCT", ExternalID: "test-apartment-remote", CanonicalID: alias.ID, CanonicalVersion: alias.Version}
	c.must(c.owner, "POST", "/channels/"+conn.ID.String()+"/mappings/preview", businesshttp.CommercialMappingPreview{Version: conn.Version, Entries: []distribution.MappingReference{entry}, Reason: "Cannot bind one provider product to a second canonical product"}, 409)
	entry.CanonicalID = c.product.ID
	entry.CanonicalVersion = c.product.Version
	entry.ExpectedMappingVersion = 1
	c.must(c.owner, "POST", "/channels/"+conn.ID.String()+"/mappings/preview", businesshttp.CommercialMappingPreview{Version: conn.Version, Entries: []distribution.MappingReference{entry, entry}, Reason: "Duplicate provider IDs cannot be accepted within a preview"}, 400)
	var existing ids.ID
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT product_id FROM channel_product_mappings WHERE connection_id=$1 AND external_id='test-apartment-remote'`, conn.ID).Scan(&existing); e != nil || existing != c.product.ID {
		t.Fatalf("Canonical mapping was overwritten %s %v", existing, e)
	}
}

func TestBusinessCommercialExplicitReadbackResolvesOnlyMatchingDeliveryConflict(t *testing.T) {
	c := newCommercialHTTP(t)
	consumer, adapter := newCommercialSupplyConsumer(t)
	conn := c.connectMarketplace(adapter)
	_, first := c.publishListing(conn)
	consumer.mu.Lock()
	consumer.ambiguous = true
	consumer.readbackOffline = true
	consumer.mu.Unlock()
	if e := c.domain.ProcessChannelSync(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	if c.syncState(first) != "RETRY" {
		t.Fatalf("Expected unknown delivery retry %s", c.syncState(first))
	}
	var conflict ids.ID
	var version int64
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT id,version FROM channel_conflicts WHERE job_id=$1 AND state='OPEN'`, first).Scan(&conflict, &version); e != nil {
		t.Fatal(e)
	}
	decision := businesshttp.CommercialConflictDecision{Action: "RECONCILE", Version: version, Reason: "Independently review lost response and read back the original immutable command"}
	decision.ApprovalID = c.approval("COMMERCIAL_CHANNEL_CONFLICT_RESOLUTION", "channels:approve", distribution.Marketplace, nil, "", map[string]any{"conflict_id": conflict, "command": decision})
	// Fence the original retry so this journey exercises the operator readback job.
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE channel_sync_jobs SET available_at=now()+interval '1 hour' WHERE id=$1`, first); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", "/channel-conflicts/"+conflict.String()+"/resolve", decision, 202)
	if e := c.domain.ProcessChannelSync(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	var state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM channel_conflicts WHERE id=$1`, conflict).Scan(&state); e != nil || state != "RESOLVED" {
		t.Fatalf("Matching authoritative readback did not resolve conflict %s %v", state, e)
	}
	consumer.mu.Lock()
	applied := consumer.applied
	consumer.mu.Unlock()
	if applied != 1 {
		t.Fatalf("Reconciliation duplicated provider mutation %d", applied)
	}
}
