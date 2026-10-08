package serviceauth

import (
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// Identity bundles a component's signer and verifier.
type Identity struct {
	Signer   *Signer
	Verifier *Verifier
}

// LoadIdentity reads the standard service identity configuration:
//
//	SERVICE_AUTH_KEY_ID               kid of this component's signing key
//	SERVICE_AUTH_PRIVATE_KEY[_FILE]   Ed25519 private key (PEM PKCS#8 or base64 seed)
//	SERVICE_AUTH_TRUSTED_KEYS[_FILE]  JSON registry of trusted peer public keys
//	SERVICE_AUTH_TOKEN_TTL            optional, <= 5m (default 2m)
//
// Keys are injected through secrets management; rotating a key is a config
// change (add the new kid to peers' registries, switch, retire), never a
// rebuild.
func LoadIdentity(l *config.Loader, self platform.Service) (*Identity, error) {
	kid := l.Required("SERVICE_AUTH_KEY_ID")
	priv := l.Secret("SERVICE_AUTH_PRIVATE_KEY", true)
	trusted := l.Secret("SERVICE_AUTH_TRUSTED_KEYS", true)
	ttl := l.Duration("SERVICE_AUTH_TOKEN_TTL", DefaultTTL)
	if err := l.Err(); err != nil {
		return nil, err
	}
	pk, err := security.ParseEd25519PrivateKey(priv.Reveal())
	if err != nil {
		return nil, errors.New("SERVICE_AUTH_PRIVATE_KEY is not a valid Ed25519 key")
	}
	signer, err := NewSigner(SignerConfig{Service: self, Key: jwt.SigningKey{KID: kid, PrivateKey: pk}, TTL: ttl})
	if err != nil {
		return nil, err
	}
	reg, err := ParseRegistry([]byte(trusted.Reveal()))
	if err != nil {
		return nil, err
	}
	verifier, err := NewVerifier(VerifierConfig{Self: self, Registry: reg, Leeway: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	return &Identity{Signer: signer, Verifier: verifier}, nil
}
