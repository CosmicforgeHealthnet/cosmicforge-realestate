package notification

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// The client must authenticate as individual-owner-service with audience
// notification-service, send the Idempotency-Key and decode the contract.
func TestClientContractAndFailureClassification(t *testing.T) {
	key := testkit.SigningKey(t, "io-1")
	signer, _ := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.IndividualOwnerService, Key: key})
	reg := serviceauth.NewRegistry()
	_ = reg.Add(platform.IndividualOwnerService, "io-1", key.PublicKey())
	ver, _ := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.NotificationService, Registry: reg})
	nid := ids.New()
	mode := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := ver.Verify(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil || p.Service != platform.IndividualOwnerService || r.URL.Path != "/internal/v1/notifications" || r.Header.Get("Idempotency-Key") != "k-1" {
			httpx.WriteError(w, r, apperrors.InvalidToken())
			return
		}
		var req SendRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch mode {
		case "422":
			httpx.WriteError(w, r, apperrors.Unprocessable("TEMPLATE_VARIABLES_INVALID", "bad"))
		case "503":
			httpx.WriteError(w, r, apperrors.Unavailable(""))
		default:
			if req.TemplateKey != TemplateVerificationCode || req.Variables["code"] != "123456" || req.Recipient.Domain != "individual-owner" {
				httpx.WriteError(w, r, apperrors.InvalidRequest("bad request"))
				return
			}
			httpx.WriteData(w, http.StatusAccepted, map[string]any{"notification_id": nid, "suppressed": []any{}})
		}
	}))
	defer srv.Close()
	c, err := New(srv.URL, signer, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	req := SendRequest{Recipient: Recipient{Domain: "individual-owner", ID: ids.New()}, TemplateKey: TemplateVerificationCode,
		Channels: []string{ChannelEmail}, Variables: map[string]string{"code": "123456"}, Destinations: Destinations{Email: "a@b.co"}}
	acc, err := c.Send(context.Background(), req, "k-1")
	if err != nil || acc.NotificationID != nid {
		t.Fatalf("%+v %v", acc, err)
	}
	mode = "422"
	if _, err := c.Send(context.Background(), req, "k-1"); Classify(err) != OutcomeDefinite {
		t.Fatalf("422 must be definite: %v", err)
	}
	mode = "503"
	if _, err := c.Send(context.Background(), req, "k-1"); Classify(err) != OutcomeRetryable {
		t.Fatalf("503 must be retryable: %v", err)
	}
	srv.Close()
	if _, err := c.Send(context.Background(), req, "k-1"); err == nil || Classify(err) != OutcomeRetryable {
		t.Fatalf("transport failure must be retryable: %v", err)
	}
}
