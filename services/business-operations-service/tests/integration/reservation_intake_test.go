package integration

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"testing"
	"time"
)

func TestBusinessReservationSignedChannelAcceptanceDedupeAndCancellation(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	_, adapter := newCommercialSupplyConsumer(t)
	connection := c.connectMarketplace(adapter)
	signer := c.supplySigner(platform.MarketplaceService)
	org := c.owner.registered.OrganisationID
	token, e := signer.TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: org})
	if e != nil {
		t.Fatal(e)
	}
	headers := map[string]string{"Authorization": "Bearer " + token}
	q := commercialData[revenue.QuoteSnapshot](t, c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1, Channel: distribution.Marketplace}, 201))
	booking := reservations.Create{PropertyID: c.property.ID, Source: "MARKETPLACE", SourceReference: "marketplace-canonical-test-booking", Channel: distribution.Marketplace, State: "HOLD", PaymentResponsibility: "PLATFORM_COLLECT", Rooms: []reservations.RoomCommand{{ProductID: c.product.ID, QuoteID: q.QuoteID, ProductVersion: c.product.Version, PoolVersion: c.pool.Version, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1}}, Guests: []reservations.GuestCommand{{SourceKind: "MARKETPLACE", SourceReference: "typed-marketplace-guest-reference", IsPrimary: true}}, Reason: "Signed source accepts the exact canonical quote"}
	event := distribution.Receipt{ProviderEventID: "canonical-create-1", ExternalObjectID: booking.SourceReference, ExternalVersion: "v1", Kind: "RESERVATION_CREATED", OccurredAt: time.Now().UTC(), ProductID: c.product.ID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1, QuoteID: &q.QuoteID, Booking: &booking}
	command := businesshttp.CommercialReceiptCommand{OrganisationID: org, ConnectionID: connection.ID, Receipt: event}
	path := "/internal/v1/business-supply/channel-receipts"
	status, raw := c.rootCall(path, command, headers)
	accepted := commercialData[businesshttp.CommercialReceiptResult](t, raw)
	if status != 202 || !accepted.CanonicalReservationAccepted || accepted.CanonicalReservationID == nil || accepted.CanonicalReservationState != "HOLD" {
		t.Fatalf("canonical intake %d %s", status, raw)
	}
	id := *accepted.CanonicalReservationID
	canonical := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+id.String(), nil, 200))
	if canonical.Reservation.CreatedBy != nil || canonical.Reservation.CreatedByService != "marketplace-service" {
		t.Fatalf("source command impersonated a business owner: %+v", canonical.Reservation)
	}
	command.Receipt.ProviderEventID = "canonical-redelivery-new-delivery-id"
	status, raw = c.rootCall(path, command, headers)
	replayed := commercialData[businesshttp.CommercialReceiptResult](t, raw)
	if status != 202 || !replayed.Duplicate || !replayed.CanonicalReservationAccepted || replayed.CanonicalReservationID == nil || *replayed.CanonicalReservationID != id {
		t.Fatalf("accepted replay %d %s", status, raw)
	}
	// Reusing the same external booking under another version cannot manufacture
	// a second root. The durable receipt is quarantined without partial effects.
	command.Receipt.ProviderEventID = "canonical-create-collision"
	command.Receipt.ExternalVersion = "v2"
	status, raw = c.rootCall(path, command, headers)
	collision := commercialData[businesshttp.CommercialReceiptResult](t, raw)
	if status != 202 || collision.CanonicalReservationAccepted || collision.Reason != "SOURCE_RESERVATION_ALREADY_EXISTS" {
		t.Fatalf("source identity collision %d %s", status, raw)
	}
	// The source change is durable but unaccepted until the exact canonical
	// amendment is independently approved/applied, then separately reconciled.
	amendQuote := commercialData[revenue.QuoteSnapshot](t, c.must(c.owner, "POST", "/reservations/"+id.String()+"/quotes", businesshttp.ReservationAmendmentQuote{Version: canonical.Reservation.Version, Quote: businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: q.DateFrom, DateTo: q.DateTo, Adults: 2, Quantity: 1, Channel: distribution.Marketplace}}, 201))
	changedBooking := booking
	changedBooking.Rooms = []reservations.RoomCommand{{ProductID: c.product.ID, QuoteID: amendQuote.QuoteID, ProductVersion: amendQuote.ProductVersion, PoolVersion: amendQuote.PoolVersion, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1}}
	command.Receipt = distribution.Receipt{ProviderEventID: "canonical-reviewed-change", ExternalObjectID: booking.SourceReference, ExternalVersion: "v2-reviewed", Kind: "RESERVATION_CHANGED", OccurredAt: time.Now().UTC(), ProductID: c.product.ID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1, Booking: &changedBooking, ExpectedReservationVersion: canonical.Reservation.Version}
	status, raw = c.rootCall(path, command, headers)
	changeReceipt := commercialData[businesshttp.CommercialReceiptResult](t, raw)
	if status != 202 || changeReceipt.CanonicalReservationAccepted || changeReceipt.Reason != "SOURCE_AMENDMENT_REVIEW_REQUIRED" {
		t.Fatalf("unreviewed change accepted %d %s", status, raw)
	}
	var conflictID ids.ID
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT id FROM reservation_conflicts WHERE receipt_id=$1`, changeReceipt.ReceiptID).Scan(&conflictID); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "GET", "/reservation-conflicts/"+conflictID.String(), nil, 200)
	bind := businesshttp.ReservationConflictDecision{Version: 1, Action: "BIND_REVIEWED", ReservationID: &id, ReservationVersion: canonical.Reservation.Version, Reason: "Bind the exact independently applied source amendment", EvidenceReferences: []string{"source-evidence:reviewed-change"}}
	bindOp := bind
	bind.ApprovalID = c.approval("COMMERCIAL_RESERVATION_CONFLICT", "reservations:approve", "", nil, "", map[string]any{"conflict_id": conflictID, "command": bindOp})
	c.must(c.owner, "POST", "/reservation-conflicts/"+conflictID.String()+"/decision", bind, 409)
	amend := reservations.Command{Version: canonical.Reservation.Version, Reason: "Apply the independently approved provider change at its frozen quote", Rooms: changedBooking.Rooms}
	amendOp := amend
	amend.ApprovalID = c.approval("COMMERCIAL_RESERVATION_CHANGE", "reservations:approve", distribution.Marketplace, &amendQuote.AmountMinor, amendQuote.Currency, map[string]any{"reservation_id": id, "command": amendOp})
	canonical = commercialData[reservations.Detail](t, c.must(c.owner, "PATCH", "/reservations/"+id.String(), amend, 200))
	bind.ReservationVersion = canonical.Reservation.Version
	bind.ApprovalID = ids.Nil
	bindOp = bind
	bind.ApprovalID = c.approval("COMMERCIAL_RESERVATION_CONFLICT", "reservations:approve", "", nil, "", map[string]any{"conflict_id": conflictID, "command": bindOp})
	c.must(c.owner, "POST", "/reservation-conflicts/"+conflictID.String()+"/decision", bind, 200)
	c.must(c.owner, "POST", "/reservation-conflicts/"+conflictID.String()+"/decision", bind, 409)
	status, raw = c.rootCall(path, command, headers)
	if accepted := commercialData[businesshttp.CommercialReceiptResult](t, raw); status != 202 || !accepted.Duplicate || !accepted.CanonicalReservationAccepted {
		t.Fatalf("reviewed source redelivery %d %s", status, raw)
	}
	if _, e = c.db.Pool.Exec(t.Context(), `DELETE FROM reservation_conflict_decisions WHERE conflict_id=$1`, conflictID); e == nil {
		t.Fatal("source decision history mutable")
	}
	command.Receipt = distribution.Receipt{ProviderEventID: "canonical-cancel-1", ExternalObjectID: booking.SourceReference, ExternalVersion: "v3", Kind: "RESERVATION_CANCELLED", OccurredAt: time.Now().UTC(), ProductID: c.product.ID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1, ExpectedReservationVersion: canonical.Reservation.Version, Reference: "Guest cancelled through the authenticated provider"}
	status, raw = c.rootCall(path, command, headers)
	cancelled := commercialData[businesshttp.CommercialReceiptResult](t, raw)
	if status != 202 || !cancelled.CanonicalReservationAccepted || cancelled.CanonicalReservationState != "CANCELLED" {
		t.Fatalf("source cancellation %d %s", status, raw)
	}
	var roots, refs int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM reservations),(SELECT count(*) FROM reservation_source_refs)`).Scan(&roots, &refs); e != nil || roots != 1 || refs != 3 {
		t.Fatalf("source lineage %d/%d %v", roots, refs, e)
	}
	// Neither a browser route nor a user token can call internal source status.
	c.must(c.owner, "GET", "/internal/reservations/"+id.String(), nil, 404)
}
