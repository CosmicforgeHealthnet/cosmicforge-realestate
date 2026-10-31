package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// This is a disposable signed contract consumer, explicitly not a substitute
// implementation or certification of the currently scaffolded Marketplace.
type commercialSupplyConsumer struct {
	pollPages          map[string]distribution.PollResult
	payloads           map[string]json.RawMessage
	mu                 sync.Mutex
	commands           map[string]distribution.Acknowledgement
	latest             map[ids.ID]int64
	applied            int
	offline, ambiguous bool
	readbackOffline    bool
	server             *httptest.Server
}

func newCommercialSupplyConsumer(t *testing.T) (*commercialSupplyConsumer, *distribution.MarketplaceClient) {
	key := testkit.SigningKey(t, "disposable-business-supply-producer")
	registry := serviceauth.NewRegistry()
	if e := registry.Add(platform.BusinessOperationsService, key.KID, key.PublicKey()); e != nil {
		t.Fatal(e)
	}
	v, e := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.MarketplaceService, Registry: registry})
	if e != nil {
		t.Fatal(e)
	}
	signer, e := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.BusinessOperationsService, Key: key})
	if e != nil {
		t.Fatal(e)
	}
	consumer := &commercialSupplyConsumer{commands: map[string]distribution.Acknowledgement{}, pollPages: map[string]distribution.PollResult{}, payloads: map[string]json.RawMessage{}, latest: map[ids.ID]int64{}}
	protected := serviceauth.Authenticate(v, serviceauth.HeaderAuthorization)(serviceauth.RequireCaller(platform.BusinessOperationsService)(http.HandlerFunc(consumer.handle)))
	consumer.server = httptest.NewServer(protected)
	t.Cleanup(consumer.server.Close)
	client, e := distribution.NewMarketplace(consumer.server.URL, signer)
	if e != nil {
		t.Fatal(e)
	}
	return consumer, client
}
func (s *commercialSupplyConsumer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	principal, _ := serviceauth.PrincipalFrom(r.Context())
	if principal.Delegation.OrganizationID.IsZero() {
		w.WriteHeader(403)
		return
	}
	if s.offline {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":{"code":"PROVIDER_OFFLINE","message":"Disposable test outage"}}`))
		return
	}
	respond := func(value any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": value}) }
	if r.URL.Path == "/internal/v1/business-supply/connections/validate" {
		var c distribution.Connection
		if json.NewDecoder(r.Body).Decode(&c) != nil {
			w.WriteHeader(400)
			return
		}
		pr, _ := serviceauth.PrincipalFrom(r.Context())
		if pr.Delegation.OrganizationID != c.OrganisationID || !strings.HasPrefix(c.CredentialReference, "vault:") {
			w.WriteHeader(403)
			return
		}
		respond(distribution.Authorization{ProviderAccountID: c.ProviderAccountID, PropertyID: c.PropertyID, Reference: "disposable-signed-provider-authorization", ExpiresAt: time.Now().Add(time.Hour), Capabilities: []string{"ARI", "CONTENT", "PROPERTY_MAPPING", "PRODUCT_MAPPING", "RATE_MAPPING", "READBACK", "PAUSE", "WITHDRAW", "WEBHOOK", "POLLING"}})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/mapping-inspection") {
		var ref distribution.MappingReference
		if json.NewDecoder(r.Body).Decode(&ref) != nil {
			w.WriteHeader(400)
			return
		}
		respond(distribution.MappingProof{Kind: ref.Kind, ExternalID: ref.ExternalID, PropertyExternalID: "test-hotel-remote", ProviderAccountID: "disposable-provider-account", Reference: "disposable-provider-object-proof"})
		return
	}
	if r.URL.Path == "/internal/v1/business-supply/commands" && r.Method == "POST" {
		var c distribution.Command
		if json.NewDecoder(r.Body).Decode(&c) != nil {
			w.WriteHeader(400)
			return
		}
		if ack, ok := s.commands[c.IdempotencyKey]; ok {
			ack.ObservedAt = time.Now().UTC()
			respond(ack)
			return
		}
		if s.latest[c.Connection.ID] > c.CanonicalVersion {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":{"code":"STALE_CANONICAL_VERSION","message":"A newer state was already accepted"}}`))
			return
		}
		if principal.Delegation.OrganizationID != c.Connection.OrganisationID {
			w.WriteHeader(403)
			return
		}
		var payload struct {
			State string `json:"desired_state"`
		}
		_ = json.Unmarshal(c.Payload, &payload)
		state := payload.State
		if state == "" {
			state = "SYNCHRONIZED"
		}
		ack := distribution.Acknowledgement{ProviderRequestID: "provider-" + ids.New().String(), ConnectionID: c.Connection.ID, CanonicalVersion: c.CanonicalVersion, PayloadHash: c.PayloadHash, State: state, Durable: true, ObservedAt: time.Now().UTC()}
		s.commands[c.IdempotencyKey] = ack
		s.payloads[c.IdempotencyKey] = append(json.RawMessage{}, c.Payload...)
		s.latest[c.Connection.ID] = c.CanonicalVersion
		s.applied++
		if s.ambiguous {
			s.ambiguous = false
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":{"code":"TEST_RESPONSE_LOST","message":"Commit completed before response failure"}}`))
			return
		}
		respond(ack)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/internal/v1/business-supply/commands/") {
		if s.readbackOffline {
			s.readbackOffline = false
			w.WriteHeader(503)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/internal/v1/business-supply/commands/")
		ack, ok := s.commands[key]
		if !ok {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":"COMMAND_NOT_APPLIED","message":"No command with this durable key"}}`))
			return
		}
		ack.ObservedAt = time.Now().UTC()
		respond(ack)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/events") {
		cursor := r.URL.Query().Get("cursor")
		if page, ok := s.pollPages[cursor]; ok {
			respond(page)
			return
		}
		respond(distribution.PollResult{Cursor: cursor, NextCursor: cursor, Events: []distribution.Receipt{}})
		return
	}
	w.WriteHeader(404)
}
func (c *commercialHarness) connectMarketplace(client distribution.Adapter) businesshttp.CommercialChannelConnection {
	c.t.Helper()
	c.domain.ChannelAdapters = map[string]distribution.Adapter{distribution.Marketplace: client}
	// Provider certification evidence exists only in this disposable test DB.
	if _, e := c.db.Pool.Exec(c.t.Context(), `UPDATE channel_providers SET status='CERTIFIED',certification_reference='disposable-contract-test-evidence',rate_limit_per_minute=600000 WHERE code='COSMICFORGE_MARKETPLACE'`); e != nil {
		c.t.Fatal(e)
	}
	if _, e := c.db.Pool.Exec(c.t.Context(), `UPDATE channel_capabilities SET status='CERTIFIED',evidence_reference='disposable-contract-test-evidence' WHERE provider_id=(SELECT id FROM channel_providers WHERE code='COSMICFORGE_MARKETPLACE') AND capability IN ('ARI','CONTENT','PROPERTY_MAPPING','PRODUCT_MAPPING','RATE_MAPPING','READBACK','PAUSE','WITHDRAW','WEBHOOK','POLLING')`); e != nil {
		c.t.Fatal(e)
	}
	conn := commercialData[businesshttp.CommercialChannelConnection](c.t, c.must(c.owner, "POST", "/channels/connections", businesshttp.CommercialChannelCreate{PropertyID: c.property.ID, ProviderCode: distribution.Marketplace, ProviderAccountID: "disposable-provider-account", Reason: "Configure a disposable signed contract consumer"}, 201))
	authorize := businesshttp.CommercialChannelAuthorize{Version: conn.Version, CredentialReference: "vault:disposable/connection-credentials", Reason: "Independently approve validated provider authority"}
	authorize.ApprovalID = c.approval("COMMERCIAL_CHANNEL_CHANGE", "channels:approve", distribution.Marketplace, nil, "", map[string]any{"connection_id": conn.ID, "command": authorize})
	conn = commercialData[businesshttp.CommercialChannelConnection](c.t, c.must(c.owner, "POST", "/channels/"+conn.ID.String()+"/authorize", authorize, 200))
	entries := []distribution.MappingReference{{Kind: "PROPERTY", ExternalID: "test-hotel-remote", CanonicalID: c.property.ID, CanonicalVersion: c.property.Version}, {Kind: "PRODUCT", ExternalID: "test-apartment-remote", CanonicalID: c.product.ID, CanonicalVersion: c.product.Version}, {Kind: "RATE_PLAN", ExternalID: "test-rate-remote", CanonicalID: c.rate.ID, CanonicalVersion: c.rate.Version}}
	preview := commercialData[struct {
		ID     ids.ID `json:"preview_id"`
		Digest string `json:"digest"`
	}](c.t, c.must(c.owner, "POST", "/channels/"+conn.ID.String()+"/mappings/preview", businesshttp.CommercialMappingPreview{Version: conn.Version, Entries: entries, Reason: "Inspect canonical and provider object bindings"}, 201))
	confirm := businesshttp.CommercialMappingConfirm{PreviewID: preview.ID, Digest: preview.Digest, Version: conn.Version, Reason: "Confirm independently reviewed provider object mappings"}
	confirm.ApprovalID = c.approval("COMMERCIAL_CHANNEL_MAPPING_CHANGE", "channels:approve", distribution.Marketplace, nil, "", map[string]any{"connection_id": conn.ID, "command": confirm})
	c.must(c.owner, "POST", "/channels/"+conn.ID.String()+"/mappings/confirm", confirm, 200)
	return commercialData[businesshttp.CommercialChannelConnection](c.t, c.must(c.owner, "GET", "/channels/"+conn.ID.String(), nil, 200))
}
func (c *commercialHarness) publishListing(conn businesshttp.CommercialChannelConnection) (businesshttp.CommercialListing, ids.ID) {
	c.t.Helper()
	mediaID := ids.New()
	c.domain.Media = &inventoryMediaFake{metadata: media.Metadata{ID: mediaID, OwnerDomain: string(platform.DomainBusinessOperations), OwnerType: "PROPERTY", OwnerID: c.property.ID, Status: "READY", Classification: "PUBLIC", Class: "IMAGE", MimeType: "image/jpeg"}}
	linked := commercialData[struct {
		ID ids.ID `json:"media_link_id"`
	}](c.t, c.must(c.owner, "POST", "/inventory-nodes/"+c.property.ID.String()+"/media", map[string]any{"media_id": mediaID, "purpose": "PHOTO", "version": c.property.Version, "reason": "Reference a public approved property photo"}, 200))
	content := businesshttp.CommercialContent{Title: "Verified apartment nightly stay", Description: "A complete property description for the approved disposable commercial publication journey.", Language: "en-NG", Amenities: []string{"WIFI"}, HouseRules: []string{"No smoking"}, CheckInLocal: "15:00", CheckOutLocal: "11:00", MediaLinkIDs: []ids.ID{linked.ID}}
	draft := commercialData[struct {
		Listing businesshttp.CommercialListing        `json:"listing"`
		Version businesshttp.CommercialListingVersion `json:"current_version"`
	}](c.t, c.must(c.owner, "POST", "/listings", businesshttp.CommercialListingCommand{PropertyID: c.property.ID, ProductID: c.product.ID, PropertyVersion: c.property.Version, Content: content, Reason: "Create versioned canonical property content"}, 201))
	review := commercialData[businesshttp.CommercialListing](c.t, c.must(c.owner, "POST", "/listings/"+draft.Listing.ID.String()+"/submit", businesshttp.CommercialStateChange{Version: draft.Listing.Version, Reason: "Submit complete content for independent publication review"}, 200))
	command := businesshttp.CommercialPublishCommand{Version: review.Version, ListingVersionID: draft.Version.ID, PropertyVersion: c.property.Version, ProductVersion: c.product.Version, PoolVersion: c.pool.Version, RatePlanVersion: c.rate.Version, ConnectionID: conn.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Reason: "Publish verified canonical supply to the certified test consumer"}
	amount := int64(20000)
	command.ApprovalID = c.approval("VERIFIED_PROPERTY_PUBLICATION", "publication:approve", distribution.Marketplace, &amount, "NGN", map[string]any{"listing_id": review.ID, "command": command})
	queued := commercialData[struct {
		JobID ids.ID `json:"job_id"`
	}](c.t, c.must(c.owner, "POST", "/listings/"+review.ID.String()+"/publish", command, 202))
	return review, queued.JobID
}
func (c *commercialHarness) syncState(id ids.ID) string {
	c.t.Helper()
	var state string
	if e := c.db.Pool.QueryRow(c.t.Context(), `SELECT state FROM channel_sync_jobs WHERE id=$1`, id).Scan(&state); e != nil {
		c.t.Fatal(e)
	}
	return state
}
func TestBusinessCommercialSignedPublicationAmbiguousCommitReadbackAndPause(t *testing.T) {
	c := newCommercialHTTP(t)
	consumer, client := newCommercialSupplyConsumer(t)
	conn := c.connectMarketplace(client)
	listing, job := c.publishListing(conn)
	if c.syncState(job) != "QUEUED" {
		t.Fatal("Publication must be durably queued before delivery")
	}
	consumer.mu.Lock()
	consumer.ambiguous = true
	consumer.mu.Unlock()
	if e := c.domain.ProcessChannelSync(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	if state := c.syncState(job); state != "SUCCEEDED" {
		var reason string
		_ = c.db.Pool.QueryRow(t.Context(), `SELECT last_error FROM channel_sync_jobs WHERE id=$1`, job).Scan(&reason)
		t.Fatalf("Ambiguous response/readback %s %s", state, reason)
	}
	consumer.mu.Lock()
	applied := consumer.applied
	consumer.mu.Unlock()
	if applied != 1 {
		t.Fatalf("Duplicate provider side effect %d", applied)
	}
	current := commercialData[struct {
		Listing businesshttp.CommercialListing `json:"listing"`
	}](t, c.must(c.owner, "GET", "/listings/"+listing.ID.String(), nil, 200))
	if current.Listing.State != "PUBLISHED" {
		t.Fatalf("Listing was not confirmed %s", current.Listing.State)
	}
	var confirmed int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM listing_publication WHERE listing_id=$1 AND delivery_state='CONFIRMED' AND acknowledged_version=canonical_version AND readback_version=canonical_version`, listing.ID).Scan(&confirmed); e != nil || confirmed != 1 {
		t.Fatalf("Readback confirmation %d %v", confirmed, e)
	}
	c.must(c.owner, "POST", "/listings/"+listing.ID.String()+"/pause", businesshttp.CommercialStateChange{Version: current.Listing.Version, Reason: "Pause after the completed publication journey"}, 202)
	if e := c.domain.ProcessChannelSync(t.Context(), 2); e != nil {
		t.Fatal(e)
	}
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM listing_publication WHERE listing_id=$1 AND delivery_state='CONFIRMED' AND desired_state='PAUSED' AND acknowledged_version=canonical_version AND readback_version=canonical_version`, listing.ID).Scan(&confirmed); e != nil || confirmed != 1 {
		t.Fatalf("Provider pause was not confirmed %d %v", confirmed, e)
	}
	c.must(c.owner, "PATCH", "/channels/"+conn.ID.String(), businesshttp.CommercialStateChange{Version: conn.Version, State: "DISCONNECTED", Reason: "Disconnect only after authoritative provider pause"}, 200)
}
func TestBusinessCommercialChannelOutageLeaseRecoveryAndCanonicalRestrictionPause(t *testing.T) {
	c := newCommercialHTTP(t)
	consumer, client := newCommercialSupplyConsumer(t)
	conn := c.connectMarketplace(client)
	listing, job := c.publishListing(conn)
	consumer.mu.Lock()
	consumer.offline = true
	consumer.mu.Unlock()
	if e := c.domain.ProcessChannelSync(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	if state := c.syncState(job); state != "RETRY" {
		t.Fatalf("Outage did not persist retry %s", state)
	}
	var state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT delivery_state FROM listing_publication WHERE listing_id=$1`, listing.ID).Scan(&state); e != nil || state != "UNKNOWN" {
		t.Fatalf("Outage reported false confirmation %s %v", state, e)
	}
	// Simulate a crashed worker lease; the next generation reads back the stable
	// command before deciding whether an idempotent first apply is necessary.
	consumer.mu.Lock()
	consumer.offline = false
	consumer.mu.Unlock()
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE channel_sync_jobs SET state='RUNNING',lease_owner='crashed-worker',lease_expires_at=now()-interval '1 second',available_at=now()-interval '1 second' WHERE id=$1`, job); e != nil {
		t.Fatal(e)
	}
	if e := c.domain.ProcessChannelSync(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	if state := c.syncState(job); state != "SUCCEEDED" {
		t.Fatalf("Restart recovery %s", state)
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE verification_links SET provider_status='RESTRICTED' WHERE organisation_id=$1 AND purpose='KYB'`, c.owner.registered.OrganisationID); e != nil {
		t.Fatal(e)
	}
	if e := c.domain.ProcessCommercialMaintenance(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT desired_state||':'||delivery_state FROM listing_publication WHERE listing_id=$1`, listing.ID).Scan(&state); e != nil || state != "PAUSED:UNKNOWN" {
		t.Fatalf("Restriction did not queue truthful safety pause %s %v", state, e)
	}
	if e := c.domain.ProcessChannelSync(t.Context(), 3); e != nil {
		t.Fatal(e)
	}
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT desired_state||':'||delivery_state FROM listing_publication WHERE listing_id=$1`, listing.ID).Scan(&state); e != nil || state != "PAUSED:CONFIRMED" {
		t.Fatalf("Restriction safety pause not confirmed %s %v", state, e)
	}
}
