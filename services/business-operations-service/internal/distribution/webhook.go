package distribution

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// VerifySupplyWebhook implements only the versioned CosmicForge supply
// callback contract. OTA signatures must use their actual approved adapters.
func VerifySupplyWebhook(key, body []byte, path, timestamp, signature string, now time.Time) bool {
	seconds, e := strconv.ParseInt(timestamp, 10, 64)
	if e != nil || len(key) != 32 || len(body) > 65536 || len(signature) != 67 || !strings.HasPrefix(signature, "v1=") {
		return false
	}
	at := time.Unix(seconds, 0)
	if now.Sub(at) > 5*time.Minute || at.Sub(now) > time.Minute {
		return false
	}
	got, e := hex.DecodeString(signature[3:])
	if e != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(timestamp + "\n" + path + "\n"))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}
