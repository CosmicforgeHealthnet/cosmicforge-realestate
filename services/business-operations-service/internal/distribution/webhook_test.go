package distribution

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

func TestSupplyCallbackSignatureBindsRawBodyConnectionPathAndBoundedTimestamp(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"provider_event_id":"immutable-event-1"}`)
	path := "/webhooks/v1/business-channels/COSMICFORGE_MARKETPLACE/connection-1"
	sign := func(at time.Time) (string, string) {
		timestamp := strconv.FormatInt(at.Unix(), 10)
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(timestamp + "\n" + path + "\n"))
		mac.Write(body)
		return timestamp, "v1=" + hex.EncodeToString(mac.Sum(nil))
	}
	timestamp, signature := sign(now)
	if !VerifySupplyWebhook(key, body, path, timestamp, signature, now) {
		t.Fatal("Valid signed supply callback rejected")
	}
	if VerifySupplyWebhook(key, []byte(`{"provider_event_id":"forged"}`), path, timestamp, signature, now) || VerifySupplyWebhook(key, body, path+"other-tenant", timestamp, signature, now) || VerifySupplyWebhook([]byte("11234567890123456789012345678901"), body, path, timestamp, signature, now) {
		t.Fatal("Signature did not bind body, connection path and key")
	}
	for _, at := range []time.Time{now.Add(-5*time.Minute - time.Second), now.Add(time.Minute + time.Second)} {
		ts, sig := sign(at)
		if VerifySupplyWebhook(key, body, path, ts, sig, now) {
			t.Fatal("Expired/future callback replay accepted")
		}
	}
	if VerifySupplyWebhook(key, body, path, "not-a-timestamp", signature, now) || VerifySupplyWebhook(key, body, path, timestamp, "v1=invalid", now) {
		t.Fatal("Malformed signature accepted")
	}
}
