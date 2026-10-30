package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth/credentials"
	sharedauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/passwords"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func LoadRuntime(l *config.Loader, pool *pgxpool.Pool, store *outbox.Store) (*Service, *serviceauth.Identity, error) {
	identity, err := serviceauth.LoadIdentity(l, platform.BusinessOperationsService)
	if err != nil {
		return nil, nil, err
	}
	kid := l.Required("BUSINESS_ACCESS_KEY_ID")
	private := l.Secret("BUSINESS_ACCESS_PRIVATE_KEY", true)
	pepper := l.Secret("BUSINESS_CODE_PEPPER", true)
	mfaKey := l.Secret("BUSINESS_MFA_KEY", true)
	mfaID := l.Required("BUSINESS_MFA_KEY_ID")
	retired := l.Secret("BUSINESS_ACCESS_RETIRED_KEYS", false)
	oldMFA := l.Secret("BUSINESS_MFA_RETIRED_KEYS", false)
	ttl := l.Duration("BUSINESS_ACCESS_TTL", 15*time.Minute)
	if err := l.Err(); err != nil {
		return nil, nil, err
	}
	pk, err := security.ParseEd25519PrivateKey(private.Reveal())
	if err != nil {
		return nil, nil, errors.New("BUSINESS_ACCESS_PRIVATE_KEY is invalid")
	}
	key := jwt.SigningKey{KID: kid, PrivateKey: pk}
	issuer, err := sharedauth.NewIssuer(sharedauth.IssuerConfig{Issuer: string(platform.BusinessOperationsService), Key: key, TTL: ttl})
	if err != nil {
		return nil, nil, err
	}
	keys := jwt.NewStaticKeys().Add(string(platform.BusinessOperationsService), kid, key.PublicKey())
	if !retired.IsEmpty() {
		var registry map[string]string
		if json.Unmarshal([]byte(retired.Reveal()), &registry) != nil {
			return nil, nil, errors.New("BUSINESS_ACCESS_RETIRED_KEYS is invalid")
		}
		for id, value := range registry {
			pub, err := security.ParseEd25519PublicKey(value)
			if err != nil || id == "" || id == kid {
				return nil, nil, errors.New("invalid retired access key")
			}
			keys.Add(string(platform.BusinessOperationsService), id, pub)
		}
	}
	verifier, err := sharedauth.NewVerifier(sharedauth.VerifierConfig{Issuers: []sharedauth.TrustedIssuer{{Issuer: string(platform.BusinessOperationsService), Domain: platform.DomainBusinessOperations, Keys: keys}}})
	if err != nil {
		return nil, nil, err
	}
	rawKey, err := base64.StdEncoding.DecodeString(mfaKey.Reveal())
	if err != nil {
		return nil, nil, errors.New("BUSINESS_MFA_KEY must be base64-encoded 32 bytes")
	}
	old := map[string][]byte{}
	if !oldMFA.IsEmpty() {
		var registry map[string]string
		if json.Unmarshal([]byte(oldMFA.Reveal()), &registry) != nil {
			return nil, nil, errors.New("BUSINESS_MFA_RETIRED_KEYS is invalid")
		}
		for id, value := range registry {
			decoded, err := base64.StdEncoding.DecodeString(value)
			if err != nil || len(decoded) != 32 || id == mfaID || id == "" {
				return nil, nil, errors.New("invalid retired MFA key")
			}
			old[id] = decoded
		}
	}
	sealer, err := credentials.NewSealer(mfaID, rawKey, old)
	if err != nil {
		return nil, nil, err
	}
	hasher, err := passwords.NewHasher(passwords.DefaultParams, 2)
	if err != nil {
		return nil, nil, err
	}
	service, err := New(pool, issuer, verifier, hasher, pepper, store, time.Now)
	if err != nil {
		return nil, nil, err
	}
	service.Sealer = sealer
	return service, identity, nil
}
