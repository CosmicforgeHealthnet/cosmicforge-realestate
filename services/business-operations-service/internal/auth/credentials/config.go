package credentials

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
)

// LoadSealer loads protected storage without loading account JWT signing keys.
// Worker processes need decryption for queued notifications, not user signing.
func LoadSealer(l *config.Loader) (*Sealer, error) {
	id := l.Required("BUSINESS_MFA_KEY_ID")
	secret := l.Secret("BUSINESS_MFA_KEY", true)
	retired := l.Secret("BUSINESS_MFA_RETIRED_KEYS", false)
	if err := l.Err(); err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(secret.Reveal())
	if err != nil {
		return nil, errors.New("BUSINESS_MFA_KEY must encode 32 bytes")
	}
	old := map[string][]byte{}
	if !retired.IsEmpty() {
		var values map[string]string
		if json.Unmarshal([]byte(retired.Reveal()), &values) != nil {
			return nil, errors.New("BUSINESS_MFA_RETIRED_KEYS is invalid")
		}
		for kid, value := range values {
			raw, err := base64.StdEncoding.DecodeString(value)
			if err != nil || len(raw) != 32 || kid == id || kid == "" {
				return nil, errors.New("invalid retired protected-storage key")
			}
			old[kid] = raw
		}
	}
	return NewSealer(id, key, old)
}
