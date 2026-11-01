package integration

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func (c *commercialHarness) supplySigner(service platform.Service) *serviceauth.Signer {
	c.t.Helper()
	key := testkit.SigningKey(c.t, "disposable-supply-"+string(service))
	if e := c.registry.Add(service, key.KID, key.PublicKey()); e != nil {
		c.t.Fatal(e)
	}
	signer, e := serviceauth.NewSigner(serviceauth.SignerConfig{Service: service, Key: key})
	if e != nil {
		c.t.Fatal(e)
	}
	return signer
}
func (c *commercialHarness) rootCall(path string, body any, headers map[string]string) (int, []byte) {
	c.t.Helper()
	raw, _ := json.Marshal(body)
	request, e := http.NewRequest("POST", c.server.URL+path, bytes.NewReader(raw))
	if e != nil {
		c.t.Fatal(e)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, e := http.DefaultClient.Do(request)
	if e != nil {
		c.t.Fatal(e)
	}
	defer response.Body.Close()
	result, e := io.ReadAll(response.Body)
	if e != nil {
		c.t.Fatal(e)
	}
	return response.StatusCode, result
}
func TestBusinessCommercialSignedReceiptDedupeForgedCallerAndMissingReservationDetails(t *testing.T) {
	c := newCommercialHTTP(t)
	_, adapter := newCommercialSupplyConsumer(t)
	conn := c.connectMarketplace(adapter)
	signer := c.supplySigner(platform.MarketplaceService)
	org := c.owner.registered.OrganisationID
	token, e := signer.TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: org})
	if e != nil {
		t.Fatal(e)
	}
	header := map[string]string{"Authorization": "Bearer " + token}
	event := distribution.Receipt{ProviderEventID: "provider-event-1", ExternalObjectID: "external-reservation-1", ExternalVersion: "v1", Kind: "RESERVATION_CREATED", OccurredAt: time.Now().UTC(), ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Quantity: 1, Reference: "opaque-provider-reservation-reference"}
	command := businesshttp.CommercialReceiptCommand{OrganisationID: org, ConnectionID: conn.ID, Receipt: event}
	path := "/internal/v1/business-supply/channel-receipts"
	if status, _ := c.rootCall(path, command, nil); status != 401 {
		t.Fatalf("Missing service auth %d", status)
	}
	if status, _ := c.rootCall(path, command, map[string]string{"Authorization": "Bearer " + c.owner.tokens.AccessToken}); status != 401 {
		t.Fatalf("User token accepted as service identity %d", status)
	}
	gatewayToken, e := c.signer.TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: org})
	if e != nil {
		t.Fatal(e)
	}
	if status, _ := c.rootCall(path, command, map[string]string{"Authorization": "Bearer " + gatewayToken}); status != 403 {
		t.Fatalf("Wrong signed service caller %d", status)
	}
	command.OrganisationID = ids.New()
	if status, _ := c.rootCall(path, command, header); status != 403 {
		t.Fatalf("Forged organisation delegation %d", status)
	}
	command.OrganisationID = org
	status, raw := c.rootCall(path, command, header)
	if status != 202 {
		t.Fatalf("Durable intake %d %s", status, raw)
	}
	received := commercialData[businesshttp.CommercialReceiptResult](t, raw)
	if received.State != "QUARANTINED" || received.Reason != "CANONICAL_RESERVATION_DETAILS_REQUIRED" || received.CanonicalReservationAccepted {
		t.Fatalf("Fake reservation acceptance %#v", received)
	}
	for _, eventID := range []string{event.ProviderEventID, "provider-event-redelivery-new-id"} {
		command.Receipt.ProviderEventID = eventID
		status, raw = c.rootCall(path, command, header)
		duplicate := commercialData[businesshttp.CommercialReceiptResult](t, raw)
		if status != 202 || !duplicate.Duplicate || duplicate.ReceiptID != received.ReceiptID {
			t.Fatalf("Object/version replay %d %s", status, raw)
		}
	}
	command.Receipt.Quantity = 2
	if status, _ = c.rootCall(path, command, header); status != 409 {
		t.Fatalf("Changed replay body accepted %d", status)
	}
	var receipts, refs, accepted, conflicts, keys int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM channel_event_receipts WHERE connection_id=$1),(SELECT count(*) FROM channel_reservation_refs WHERE connection_id=$1),(SELECT count(*) FROM channel_reservation_refs WHERE connection_id=$1 AND canonical_reservation_id IS NOT NULL),(SELECT count(*) FROM channel_conflicts WHERE connection_id=$1 AND classification='PROVIDER_EVENT_IDENTITY_COLLISION'),(SELECT count(*) FROM channel_event_delivery_keys WHERE connection_id=$1)`, conn.ID).Scan(&receipts, &refs, &accepted, &conflicts, &keys); e != nil || receipts != 1 || refs != 1 || accepted != 0 || conflicts != 1 || keys != 2 {
		t.Fatalf("Receipt integrity %d/%d/%d/%d/%d %v", receipts, refs, accepted, conflicts, keys, e)
	}
	command.Receipt = event
	command.Receipt.ProviderEventID = "unknown-product-event"
	command.Receipt.ExternalObjectID = "unknown-product-reservation"
	command.Receipt.ProductID = ids.New()
	status, raw = c.rootCall(path, command, header)
	unknown := commercialData[businesshttp.CommercialReceiptResult](t, raw)
	if status != 202 || unknown.State != "QUARANTINED" || unknown.Reason != "PROVIDER_RESERVATION_PRODUCT_UNMAPPED" || unknown.CanonicalReservationAccepted {
		t.Fatalf("Unknown signed product event lost %d %s", status, raw)
	}

}
func TestBusinessCommercialProviderCallbackKeyProtectionSignatureAndReplay(t *testing.T) {
	c := newCommercialHTTP(t)
	_, adapter := newCommercialSupplyConsumer(t)
	conn := c.connectMarketplace(adapter)
	key := make([]byte, 32)
	if _, e := rand.Read(key); e != nil {
		t.Fatal(e)
	}
	defer clear(key)
	secret := base64.StdEncoding.EncodeToString(key)
	sum := sha256.Sum256(key)
	reason := "Approve a protected per-connection callback signing key"
	operation := businesshttp.CommercialWebhookKeyOperation{Version: conn.Version, SecretFingerprint: hex.EncodeToString(sum[:]), Reason: reason}
	approval := c.approval("COMMERCIAL_CHANNEL_CHANGE", "channels:approve", distribution.Marketplace, nil, "", map[string]any{"connection_id": conn.ID, "callback_key": operation})
	c.must(c.owner, "PUT", "/channels/"+conn.ID.String()+"/webhook-key", businesshttp.CommercialWebhookKeyCommand{Version: conn.Version, KeyBase64: secret, Reason: reason, ApprovalID: approval}, 200)
	var leaked bool
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM audit_events WHERE change_context::text LIKE '%'||$1||'%') OR EXISTS(SELECT 1 FROM outbox_events WHERE payload::text LIKE '%'||$1||'%')`, secret).Scan(&leaked); e != nil || leaked {
		t.Fatalf("Callback secret leaked to audit/outbox %v %v", leaked, e)
	}
	var stored []byte
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT encrypted_credentials FROM channel_connections WHERE id=$1`, conn.ID).Scan(&stored); e != nil || bytes.Contains(stored, key) {
		t.Fatal("Callback key not protected at rest")
	}
	command := businesshttp.CommercialReceiptCommand{OrganisationID: conn.OrganisationID, ConnectionID: conn.ID, Receipt: distribution.Receipt{ProviderEventID: "signed-callback-1", ExternalObjectID: "provider-content-1", ExternalVersion: "1", Kind: "CONTENT_CHANGED", OccurredAt: time.Now().UTC()}}
	path := "/webhooks/v1/business-channels/COSMICFORGE_MARKETPLACE/" + conn.ID.String()
	gatewayToken, e := c.signer.TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{})
	if e != nil {
		t.Fatal(e)
	}
	headers := map[string]string{serviceauth.HeaderServiceAuthorization: "Bearer " + gatewayToken}
	if status, _ := c.rootCall(path, command, headers); status != 403 {
		t.Fatalf("Unsigned callback accepted %d", status)
	}
	sign := func(at time.Time) {
		raw, _ := json.Marshal(command)
		ts := strconv.FormatInt(at.Unix(), 10)
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(ts + "\n" + path + "\n"))
		mac.Write(raw)
		headers["X-CF-Timestamp"] = ts
		headers["X-CF-Signature"] = "v1=" + hex.EncodeToString(mac.Sum(nil))
	}
	sign(time.Now().Add(-10 * time.Minute))
	if status, _ := c.rootCall(path, command, headers); status != 403 {
		t.Fatalf("Expired callback accepted %d", status)
	}
	sign(time.Now())
	status, raw := c.rootCall(path, command, headers)
	if status != 202 {
		t.Fatalf("Signed callback %d %s", status, raw)
	}
	status, raw = c.rootCall(path, command, headers)
	if status != 202 || !commercialData[businesshttp.CommercialReceiptResult](t, raw).Duplicate {
		t.Fatalf("Signed callback replay %d %s", status, raw)
	}
	command.OrganisationID = ids.New()
	sign(time.Now())
	if status, _ = c.rootCall(path, command, headers); status != 403 {
		t.Fatalf("Cross-tenant signed callback accepted %d", status)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("Callback key echoed")
	}
}
