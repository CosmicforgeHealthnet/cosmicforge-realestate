package integration

import (
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// Marketplace checkout: canonical quote on a published product, canonical
// HOLD booking, channel collection from the customer's own wallet through
// the Payment contract fixture, then Business-owned auto-confirmation.
func TestBusinessMarketplaceCheckoutQuoteHoldCollectionAndAutoConfirm(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	consumer, adapter := newCommercialSupplyConsumer(t)
	_ = consumer
	connection := c.connectMarketplace(adapter)
	org := c.owner.registered.OrganisationID
	keyed := func(svc platform.Service, delegated ids.ID, key string) map[string]string {
		tok, e := c.supplySigner(svc).TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: delegated})
		if e != nil {
			t.Fatal(e)
		}
		return map[string]string{"Authorization": "Bearer " + tok, "Idempotency-Key": key}
	}
	token := func(svc platform.Service, delegated ids.ID) map[string]string {
		return keyed(svc, delegated, "checkout-"+ids.New().String())
	}
	mk := func() map[string]string { return token(platform.MarketplaceService, org) }
	quoteIn := businesshttp.MarketplaceQuoteCommand{OrganisationID: org, ConnectionID: connection.ID, ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1}
	// Unpublished products are not priced for the channel.
	if status, raw := c.rootCall("/internal/v1/business-supply/channel-quotes", quoteIn, mk()); status != 409 {
		t.Fatalf("unpublished quote %d %s", status, raw)
	}
	c.publishListing(connection)
	if status, raw := c.rootCall("/internal/v1/business-supply/channel-quotes", quoteIn, token(platform.ArtisanService, org)); status != 403 {
		t.Fatalf("non-marketplace caller %d %s", status, raw)
	}
	if status, raw := c.rootCall("/internal/v1/business-supply/channel-quotes", quoteIn, token(platform.MarketplaceService, ids.New())); status != 403 {
		t.Fatalf("wrong delegated organisation %d %s", status, raw)
	}
	status, raw := c.rootCall("/internal/v1/business-supply/channel-quotes", quoteIn, mk())
	if status != 201 {
		t.Fatalf("quote %d %s", status, raw)
	}
	q := commercialData[revenue.QuoteSnapshot](t, raw)
	var creator *ids.ID
	var creatorService string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT created_by,created_by_service FROM price_quotes WHERE id=$1`, q.QuoteID).Scan(&creator, &creatorService); e != nil || creator != nil || creatorService != "marketplace-service" {
		t.Fatalf("quote provenance %v %s %v", creator, creatorService, e)
	}
	booking := reservations.Create{PropertyID: c.property.ID, Source: "MARKETPLACE", SourceReference: "mkt-checkout-1", Channel: distribution.Marketplace, State: "HOLD", PaymentResponsibility: "PLATFORM_COLLECT", Rooms: []reservations.RoomCommand{{ProductID: c.product.ID, QuoteID: q.QuoteID, ProductVersion: q.ProductVersion, PoolVersion: q.PoolVersion, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1}}, Guests: []reservations.GuestCommand{{SourceKind: "MARKETPLACE", SourceReference: "marketplace-customer:fixture", IsPrimary: true}}, Reason: "Marketplace customer checkout hold"}
	receipt := businesshttp.CommercialReceiptCommand{OrganisationID: org, ConnectionID: connection.ID, Receipt: distribution.Receipt{ProviderEventID: "mkt-checkout-1:create", ExternalObjectID: booking.SourceReference, ExternalVersion: "1", Kind: "RESERVATION_CREATED", OccurredAt: time.Now().UTC(), ProductID: c.product.ID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1, QuoteID: &q.QuoteID, Booking: &booking}}
	status, raw = c.rootCall("/internal/v1/business-supply/channel-receipts", receipt, mk())
	accepted := commercialData[businesshttp.CommercialReceiptResult](t, raw)
	if status != 202 || !accepted.CanonicalReservationAccepted || accepted.CanonicalReservationState != "HOLD" {
		t.Fatalf("hold %d %s", status, raw)
	}
	rid := *accepted.CanonicalReservationID
	readback := func() map[string]any {
		status, raw := c.rootGet("/internal/v1/business-supply/reservations/"+rid.String()+"?connection_id="+connection.ID.String(), mk())
		if status != 200 {
			t.Fatalf("readback %d %s", status, raw)
		}
		return commercialData[map[string]any](t, raw)
	}
	rb := readback()
	pay := rb["payment"].(map[string]any)
	if rb["state"] != "HOLD" || pay["collection_request_id"] != nil {
		t.Fatalf("readback %v", rb)
	}
	f := reservationPaymentProvider(c)
	collect := businesshttp.MarketplaceCollectionCommand{OrganisationID: org, ConnectionID: connection.ID, Version: int64(rb["version"].(float64)), PaymentPolicyID: f.policy.ID, PaymentPolicyVersion: f.policy.Version, PaymentPolicyFamily: "HOTEL", PayerWalletID: ids.New(), ConsentReference: "marketplace-checkout-consent:1", LegalPartyReference: "marketplace:" + ids.New().String(), Email: "guest@example.invalid", Schedule: "ONE_TIME"}
	bad := collect
	bad.LegalPartyReference = "business-operations:someone"
	if status, raw := c.rootCall("/internal/v1/business-supply/reservations/"+rid.String()+"/collections", bad, token(platform.MarketplaceService, org)); status != 400 {
		t.Fatalf("non-customer payer %d %s", status, raw)
	}
	status, raw = c.rootCall("/internal/v1/business-supply/reservations/"+rid.String()+"/collections", collect, keyed(platform.MarketplaceService, org, "collect-1"))
	if status != 202 {
		t.Fatalf("collection %d %s", status, raw)
	}
	if status, _ := c.rootCall("/internal/v1/business-supply/reservations/"+rid.String()+"/collections", collect, keyed(platform.MarketplaceService, org, "collect-1")); status != 202 {
		t.Fatalf("idempotent replay %d", status)
	}
	if status, _ := c.rootCall("/internal/v1/business-supply/reservations/"+rid.String()+"/collections", collect, token(platform.MarketplaceService, org)); status != 409 {
		t.Fatalf("second collection must be refused %d", status)
	}
	if e := c.domain.ProcessReservationFinance(t.Context(), 5); e != nil {
		t.Fatal(e)
	}
	if f.collectionCalls != 1 || f.quote.LegalPartyRef != collect.LegalPartyReference || f.quote.PayerWalletID != collect.PayerWalletID {
		t.Fatalf("collection %d %+v", f.collectionCalls, f.quote)
	}
	rb = readback()
	pay = rb["payment"].(map[string]any)
	if rb["state"] != "CONFIRMED" || rb["confirmed"] != true || rb["financial_state"] != "VERIFIED" || pay["intent_id"] == nil {
		t.Fatalf("auto-confirm %v", rb)
	}
	var service string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT created_by_service FROM reservation_financial_requests WHERE reservation_id=$1`, rid).Scan(&service); e != nil || service != "marketplace-service" {
		t.Fatalf("collection provenance %s %v", service, e)
	}
}
