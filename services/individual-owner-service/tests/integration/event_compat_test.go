package integration

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

const upstreamContracts = "../../../../contracts/events/"

// producerEvent builds an envelope exactly as the upstream producer's
// published contract describes it (all required payload fields).
func producerEvent(t *testing.T, producer platform.Service, typ, aggregate string, id ids.ID, payload map[string]any) events.Envelope {
	t.Helper()
	e, err := events.New(context.Background(), producer, typ, aggregate, id, payload)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func envelopeJSON(t *testing.T, e events.Envelope) []byte {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func verificationFixture(t *testing.T, typ string, verificationID, subject ids.ID, domain string) events.Envelope {
	t.Helper()
	now := time.Now().UTC()
	status := map[string]string{"verification.submitted.v1": "SUBMITTED", "verification.processing.v1": "PROCESSING",
		"verification.manual_review_required.v1": "MANUAL_REVIEW", "verification.verified.v1": "VERIFIED", "verification.rejected.v1": "REJECTED",
		"verification.expired.v1": "EXPIRED"}[typ]
	p := map[string]any{"verification_id": verificationID, "verification_type": "PROPERTY_AUTHORITY", "status": status,
		"subject":              map[string]any{"subject_type": "PROPERTY", "subject_domain": domain, "subject_id": subject},
		"requested_by_service": string(platform.IndividualOwnerService), "submitted_at": now, "provider": "registry-adapter",
		"decision_source": "PROVIDER", "decided_at": now, "expires_at": now.AddDate(1, 0, 0), "expired_at": now,
		"review_reason": "PROVIDER_INCONCLUSIVE", "reason_code": "DOCUMENT_MISMATCH"}
	return producerEvent(t, platform.VerificationService, typ, "verification", verificationID, p)
}

func mediaFixture(t *testing.T, typ string, mediaID, owner ids.ID) events.Envelope {
	t.Helper()
	now := time.Now().UTC()
	status := map[string]string{"media.ready.v1": "READY", "media.rejected.v1": "REJECTED", "media.deleted.v1": "DELETED"}[typ]
	p := map[string]any{"media_id": mediaID, "owner": map[string]any{"owner_domain": "individual-owner", "owner_type": "USER", "owner_id": owner},
		"media_class": "IMAGE", "status": status, "mime_type": "image/jpeg", "size_bytes": 2048, "checksum_sha256": strings.Repeat("ab", 32),
		"access_classification": "PRIVATE", "scan_status": "CLEAN", "ready_at": now, "deleted_at": now, "rejection_reason": "INFECTED"}
	return producerEvent(t, platform.MediaDocumentService, typ, "media", mediaID, p)
}

// The consumer accepts exactly what the real producers publish: every
// consumed verification and media type is validated against the producer's
// own schema in contracts/events and then delivered to the consumer.
func TestUpstreamFixturesMatchProducerSchemas(t *testing.T) {
	h := newHarness(t)
	svc, ctx := h.app.Service, context.Background()
	for _, typ := range []string{"verification.submitted.v1", "verification.processing.v1", "verification.manual_review_required.v1",
		"verification.verified.v1", "verification.rejected.v1", "verification.expired.v1"} {
		e := verificationFixture(t, typ, ids.New(), ids.New(), "individual-owner")
		testkit.ValidateJSONSchema(t, upstreamContracts+"verification/"+typ+".schema.json", envelopeJSON(t, e))
		// Unknown case: accepted by the contract, retried until the link exists.
		if err := svc.ApplyVerificationEvent(ctx, e); err == nil || events.IsPermanent(err) {
			t.Fatalf("%s: a conforming event must be accepted and retried for an unknown case: %v", typ, err)
		}
		other := verificationFixture(t, typ, ids.New(), ids.New(), "marketplace")
		if err := svc.ApplyVerificationEvent(ctx, other); err != nil {
			t.Fatalf("%s: another domain's case must be ignored: %v", typ, err)
		}
	}
	for _, typ := range []string{"media.ready.v1", "media.rejected.v1", "media.deleted.v1"} {
		e := mediaFixture(t, typ, ids.New(), ids.New())
		testkit.ValidateJSONSchema(t, upstreamContracts+"media/"+typ+".schema.json", envelopeJSON(t, e))
		if err := svc.ApplyMediaEvent(ctx, e); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
	}
	// No upstream schema exists for the remaining families: their consumer
	// contracts are this service's own and live integration is BLOCKED.
	// Section 1-3 finance.* events describe collection/pending ledger facts.
	// They must never masquerade as this consumer's later payment.* held or
	// refund lifecycle facts. Validate the foundation envelope and reject it.
	wallet := ids.New()
	foundation := producerEvent(t, platform.PaymentService, "finance.wallet.opened.v1", "wallet", wallet, map[string]any{"wallet_id": wallet, "identity_id": ids.New(), "domain": "individual-owner", "role": "OWNER", "legal_entity": "cosmicforge-ng", "currency": "NGN", "state": "ACTIVE"})
	testkit.ValidateJSONSchema(t, upstreamContracts+"payments/finance.wallet.opened.v1.schema.json", envelopeJSON(t, foundation))
	if err := svc.ApplyPaymentEvent(ctx, foundation); !events.IsPermanent(err) {
		t.Fatalf("foundation fact must not assert business payment approval: %v", err)
	}
	entries, err := os.ReadDir(upstreamContracts + "payments")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "payment.") {
			t.Fatalf("payment lifecycle schema now exists: add consumer fixtures and compatibility mapping: %s", entry.Name())
		}
	}
	for _, dir := range []string{"property-managers", "artisans", "professionals", "marketplace"} {
		if n := countSchemas(t, upstreamContracts+dir); n != 0 {
			t.Fatalf("%s now publishes %d schemas: add producer-schema fixtures and update EVENT_COMPATIBILITY.md", dir, n)
		}
	}
}

// Invalid producer identity, unknown versions, mismatched payloads,
// duplicates and out-of-order deliveries never corrupt state.
func TestUpstreamEventsCannotCorruptState(t *testing.T) {
	h := newHarness(t, withPayment)
	svc, ctx := h.app.Service, context.Background()
	o := h.register(true)
	p := h.createProperty(o, "90 Contract Close")
	h.verifyProperty(o, p.ID)
	var vid ids.ID
	if err := h.db.Pool.QueryRow(ctx, `SELECT verification_id FROM verification_links WHERE property_id=$1 AND status='VERIFIED'`, p.ID).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	linkStatus := func() string {
		var s string
		if err := h.db.Pool.QueryRow(ctx, `SELECT status FROM verification_links WHERE verification_id=$1`, vid).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	processed := func(consumer string) int {
		return h.count(`SELECT count(*) FROM processed_events WHERE consumer=$1`, consumer)
	}
	before := processed(usecases.ConsumerVerification)

	// Verification: identity, version, payload/type agreement.
	rejected := verificationFixture(t, "verification.rejected.v1", vid, p.ID, "individual-owner")
	forged := rejected
	forged.Producer = string(platform.MarketplaceService)
	v2 := rejected
	v2.EventType = "verification.rejected.v2"
	wrongAgg := rejected
	wrongAgg.AggregateType = "property"
	mismatch := verificationFixture(t, "verification.rejected.v1", vid, p.ID, "individual-owner")
	mismatch.EventType = "verification.verified.v1"
	versioned := verificationFixture(t, "verification.rejected.v1", vid, p.ID, "individual-owner")
	versioned.Payload = setField(t, versioned.Payload, "schema_version", 2)
	for name, e := range map[string]events.Envelope{"forged producer": forged, "unknown version": v2, "wrong aggregate": wrongAgg,
		"type/status mismatch": mismatch, "payload schema_version": versioned} {
		if err := svc.ApplyVerificationEvent(ctx, e); !events.IsPermanent(err) {
			t.Fatalf("verification %s must be rejected permanently: %v", name, err)
		}
	}
	// Out of order: a late PROCESSING never moves a VERIFIED case backwards.
	late := verificationFixture(t, "verification.processing.v1", vid, p.ID, "individual-owner")
	for range 2 {
		if err := svc.ApplyVerificationEvent(ctx, late); err != nil {
			t.Fatal(err)
		}
	}
	if s := linkStatus(); s != "VERIFIED" {
		t.Fatalf("verification moved backwards to %s", s)
	}
	if n := processed(usecases.ConsumerVerification) - before; n != 1 {
		t.Fatalf("only the valid delivery is recorded, once: %d", n)
	}

	// Media: deleted is terminal whatever arrives later.
	photo := h.photos(o, p.ID, 1)[0]
	for _, typ := range []string{"media.deleted.v1", "media.ready.v1"} {
		if err := svc.ApplyMediaEvent(ctx, mediaFixture(t, typ, photo, o.id)); err != nil {
			t.Fatal(err)
		}
	}
	var ms string
	if err := h.db.Pool.QueryRow(ctx, `SELECT media_status FROM property_media_links WHERE media_id=$1`, photo).Scan(&ms); err != nil {
		t.Fatal(err)
	}
	if ms != "DELETED" {
		t.Fatalf("deleted media resurrected as %s", ms)
	}
	forgedMedia := mediaFixture(t, "media.ready.v1", ids.New(), o.id)
	forgedMedia.Producer = string(platform.IndividualOwnerService)
	if err := svc.ApplyMediaEvent(ctx, forgedMedia); !events.IsPermanent(err) {
		t.Fatalf("forged media producer: %v", err)
	}
	scanning := mediaFixture(t, "media.ready.v1", ids.New(), o.id)
	scanning.EventType = "media.scanning.v1"
	if err := svc.ApplyMediaEvent(ctx, scanning); !events.IsPermanent(err) {
		t.Fatalf("unconsumed media type: %v", err)
	}

	// Payment: a newer outcome is never overwritten by an older one.
	so, l := h.shortletListing("91 Contract Close")
	h.expect(h.write(so, "PUT", "/listings/"+l.ID.String()+"/shortlet/settings", 0, shortletSettings("PLATFORM", true, false)), 200, "")
	c := customer()
	b := h.requestBooking(c, bookingBody(l, dayN(h.clock.now(), 5), dayN(h.clock.now(), 6), "Ade"))
	h.runJobs("shortlet-bookings-calendar-and-claims")
	ref := h.pay.refOf(b.ID)
	t1, t2 := time.Now().UTC(), time.Now().UTC().Add(time.Minute)
	held := envelope(t, platform.PaymentService, "payment.intent.updated.v1", map[string]any{"intent_ref": ref, "status": payment.IntentHeld,
		"amount_minor": b.TotalMinor, "paid_minor": b.TotalMinor, "updated_at": t2})
	stale := envelope(t, platform.PaymentService, "payment.intent.updated.v1", map[string]any{"intent_ref": ref, "status": payment.IntentRequiresPayment,
		"amount_minor": b.TotalMinor, "paid_minor": 0, "updated_at": t1})
	replay := envelope(t, platform.PaymentService, "payment.intent.updated.v1", map[string]any{"intent_ref": ref, "status": payment.IntentRequiresPayment,
		"amount_minor": b.TotalMinor, "paid_minor": 0, "updated_at": t2})
	for _, e := range []events.Envelope{held, held, stale, replay} {
		if err := svc.ApplyPaymentEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	var last string
	if err := h.db.Pool.QueryRow(ctx, `SELECT last_payment_status FROM shortlet_bookings WHERE id=$1`, b.ID).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if got := h.booking(so, b.ID); got.Status != "CONFIRMED" || got.PaidMinor != b.TotalMinor || last != payment.IntentHeld {
		t.Fatalf("stale payment outcome applied: %s paid %d last %s", got.Status, got.PaidMinor, last)
	}
	noTime := envelope(t, platform.PaymentService, "payment.intent.updated.v1", map[string]any{"intent_ref": ref, "status": payment.IntentHeld, "paid_minor": 1})
	if err := svc.ApplyPaymentEvent(ctx, noTime); !events.IsPermanent(err) {
		t.Fatalf("payment event without updated_at: %v", err)
	}

	// Families without a live producer: identity, version and schema_version
	// are enforced by the same consumer path.
	type fam struct {
		apply    func(context.Context, events.Envelope) error
		producer platform.Service
		typ      string
		consumer string
	}
	for _, f := range []fam{
		{svc.ApplyPaymentEvent, platform.PaymentService, "payment.refund.updated.v1", usecases.ConsumerPayment},
		{svc.ApplyManagerEvent, platform.PropertyManagerService, "manager.activity.recorded.v1", usecases.ConsumerManager},
		{svc.ApplyArtisanEvent, platform.ArtisanService, "artisan.quote.submitted.v1", usecases.ConsumerArtisan},
		{svc.ApplyProfessionalEvent, platform.ProfessionalServicesService, "professional.engagement.updated.v1", usecases.ConsumerProfessional},
		{svc.ApplyMarketplaceEvent, platform.MarketplaceService, "marketplace.listing.reported.v1", usecases.ConsumerMarketplace},
	} {
		n0 := processed(f.consumer)
		payload := map[string]any{"assignment_id": ids.New()}
		cases := map[string]events.Envelope{
			"forged producer": envelope(t, platform.AdminService, f.typ, payload),
			"unknown version": envelope(t, f.producer, strings.TrimSuffix(f.typ, ".v1")+".v2", payload),
			"schema_version":  envelope(t, f.producer, f.typ, map[string]any{"schema_version": 3}),
		}
		for name, e := range cases {
			if err := f.apply(ctx, e); !events.IsPermanent(err) {
				t.Fatalf("%s %s must be rejected permanently: %v", f.typ, name, err)
			}
		}
		if n := processed(f.consumer); n != n0 {
			t.Fatalf("%s: rejected deliveries were recorded (%d)", f.typ, n-n0)
		}
	}
}

func countSchemas(t *testing.T, dir string) int {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range es {
		if strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n
}

func setField(t *testing.T, raw json.RawMessage, k string, v any) json.RawMessage {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m[k] = v
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
