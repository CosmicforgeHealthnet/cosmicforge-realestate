package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// TrustedIssuerSpec is the JSON configuration form of a trusted issuer:
//
//	[{"issuer":"artisan-service","domain":"artisan",
//	  "keys":[{"kid":"ar-2026-10","public_key":"<base64url Ed25519>"}],
//	  "jwks_url":"https://artisan.internal/.well-known/jwks.json"}]
//
// Static keys and a JWKS URL are alternatives; static keys take precedence.
type TrustedIssuerSpec struct {
	Issuer  string          `json:"issuer"`
	Domain  platform.Domain `json:"domain"`
	Keys    []PublicKeySpec `json:"keys,omitempty"`
	JWKSURL string          `json:"jwks_url,omitempty"`
}

// PublicKeySpec is a kid + public key (PEM or base64).
type PublicKeySpec struct {
	KID       string `json:"kid"`
	PublicKey string `json:"public_key"`
}

// ParseTrustedIssuers decodes and validates issuer configuration.
func ParseTrustedIssuers(data []byte, client *http.Client) ([]TrustedIssuer, error) {
	var specs []TrustedIssuerSpec
	if err := json.Unmarshal(data, &specs); err != nil {
		return nil, fmt.Errorf("auth: trusted issuers JSON invalid: %w", err)
	}
	if len(specs) == 0 {
		return nil, errors.New("auth: no trusted issuers configured")
	}
	out := make([]TrustedIssuer, 0, len(specs))
	for _, s := range specs {
		if s.Issuer == "" || !platform.KnownDomain(string(s.Domain)) {
			return nil, fmt.Errorf("auth: invalid trusted issuer entry %q", s.Issuer)
		}
		var resolver jwt.KeyResolver
		switch {
		case len(s.Keys) > 0:
			st := jwt.NewStaticKeys()
			for _, k := range s.Keys {
				pub, err := security.ParseEd25519PublicKey(k.PublicKey)
				if err != nil || k.KID == "" {
					return nil, fmt.Errorf("auth: invalid key for issuer %q", s.Issuer)
				}
				st.Add(s.Issuer, k.KID, pub)
			}
			resolver = st
		case s.JWKSURL != "":
			resolver = &jwt.RemoteJWKS{Issuer: s.Issuer, URL: s.JWKSURL, Client: client, RefreshInterval: 10 * time.Minute}
		default:
			return nil, fmt.Errorf("auth: issuer %q has neither keys nor jwks_url", s.Issuer)
		}
		out = append(out, TrustedIssuer{Issuer: s.Issuer, Domain: s.Domain, Keys: resolver})
	}
	return out, nil
}
