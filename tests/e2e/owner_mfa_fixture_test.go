package e2e

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"time"
)

// Black-box TEST authenticator enrolment; no production bypass or DB fixture.
func (p *platformEnv) secureOwnerFixture(token string) {
	setup := p.checked(p.asUser(token, "POST", "/api/v1/owner/me/mfa/totp/setup", nil, ids.New().String()), 201)
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup["secret"].(string))
	if err != nil {
		p.t.Fatal(err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(time.Now().Unix()/30))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(counter[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 15
	code := fmt.Sprintf("%06d", (binary.BigEndian.Uint32(digest[offset:offset+4])&0x7fffffff)%1000000)
	codes := p.checked(p.asUser(token, "POST", "/api/v1/owner/me/mfa/totp/confirm", map[string]any{"code": code}, ids.New().String()), 200)
	recovery := codes["recovery_codes"].([]any)[0].(string)
	p.checked(p.asUser(token, "POST", "/api/v1/owner/auth/step-up", map[string]any{"method": "RECOVERY_CODE", "code": recovery}, ""), 200)
}
