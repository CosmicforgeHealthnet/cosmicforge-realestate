package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth/credentials"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/migrations"
	sharedauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/passwords"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func newBusinessAuth(t *testing.T) (*businessauth.Service, *testkit.Database) {
	db := testkit.NewDatabase(t, migrations.FS)
	key := testkit.SigningKey(t, "business-auth-1")
	issuer, err := sharedauth.NewIssuer(sharedauth.IssuerConfig{Issuer: string(platform.BusinessOperationsService), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := credentials.NewSealer("test-protection", []byte("01234567890123456789012345678901"), nil)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := sharedauth.NewVerifier(sharedauth.VerifierConfig{Issuers: []sharedauth.TrustedIssuer{{Issuer: string(platform.BusinessOperationsService), Domain: platform.DomainBusinessOperations, Keys: jwt.NewStaticKeys().Add(string(platform.BusinessOperationsService), key.KID, key.PublicKey())}}})
	if err != nil {
		t.Fatal(err)
	}
	hasher, err := passwords.NewHasher(passwords.Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}, 2)
	if err != nil {
		t.Fatal(err)
	}
	service, err := businessauth.New(db.Pool, issuer, verifier, hasher, security.NewSecret("test-pepper-only-not-a-production-secret-123"), outbox.NewStore(db.Pool, "realestate"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	service.Sealer = sealer
	return service, db
}

func TestBusinessAccountsSessionRotationAndReuse(t *testing.T) {
	service, db := newBusinessAuth(t)
	ctx := context.Background()
	request := businessauth.Registration{Email: "Owner@Example.test", Password: "correct horse battery", DisplayName: "Owner", OrganisationName: "Business Test"}
	registered, err := service.Register(ctx, request, "register-one")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := service.Register(ctx, request, "register-one")
	if err != nil || repeated != registered {
		t.Fatalf("registration retry: %+v %v", repeated, err)
	}
	changed := request
	changed.OrganisationName = "Different"
	if _, err := service.Register(ctx, changed, "register-one"); err == nil {
		t.Fatal("conflicting retry accepted")
	}
	if _, err := service.Register(ctx, request, "register-two"); err == nil {
		t.Fatal("duplicate credential accepted")
	}
	var state string
	if err := db.Pool.QueryRow(ctx, `SELECT state FROM organisations WHERE id=$1`, registered.OrganisationID).Scan(&state); err != nil || state != "DRAFT" {
		t.Fatalf("registration falsely verified: %s %v", state, err)
	}
	if _, err := service.Login(ctx, businessauth.Login{Email: request.Email, Password: "wrong password"}, "test-client"); err == nil {
		t.Fatal("wrong password accepted")
	}
	logged, err := service.Login(ctx, businessauth.Login{Email: request.Email, Password: request.Password, OrganisationID: registered.OrganisationID}, "test-client")
	if err != nil || logged.Tokens == nil {
		t.Fatalf("login: %+v %v", logged, err)
	}
	if _, _, err := service.Authenticate(ctx, logged.Tokens.AccessToken); err != nil {
		t.Fatal(err)
	}
	rotated, err := service.Refresh(ctx, logged.Tokens.RefreshToken)
	if err != nil || rotated.RefreshToken == logged.Tokens.RefreshToken {
		t.Fatalf("rotation: %v", err)
	}
	if _, err := service.Refresh(ctx, logged.Tokens.RefreshToken); err == nil {
		t.Fatal("refresh reuse accepted")
	}
	if _, _, err := service.Authenticate(ctx, rotated.AccessToken); err == nil {
		t.Fatal("reuse did not invalidate family access")
	}
	var misuse int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM security_events WHERE event_type='REFRESH_TOKEN_REUSE'`).Scan(&misuse); err != nil || misuse != 1 {
		t.Fatalf("reuse audit: %d %v", misuse, err)
	}
	logged, err = service.Login(ctx, businessauth.Login{Email: request.Email, Password: request.Password}, "test-client")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var results [2]businessauth.Tokens
	var failures [2]error
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], failures[i] = service.Refresh(ctx, logged.Tokens.RefreshToken)
		}(i)
	}
	wg.Wait()
	success := 0
	for i := range results {
		if failures[i] == nil {
			success++
			if _, _, err := service.Authenticate(ctx, results[i].AccessToken); err == nil {
				t.Fatal("concurrent reuse did not revoke winning token")
			}
		}
	}
	if success != 1 {
		t.Fatalf("concurrent refresh winners=%d errors=%v", success, failures)
	}
	logged, err = service.Login(ctx, businessauth.Login{Email: request.Email, Password: request.Password}, "test-client")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Revoke(ctx, registered.AccountID, logged.Tokens.SessionID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Authenticate(ctx, logged.Tokens.AccessToken); err == nil {
		t.Fatal("revoked session access accepted")
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE business_accounts SET status='DISABLED' WHERE id=$1`, registered.AccountID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Login(ctx, businessauth.Login{Email: request.Email, Password: request.Password}, "test-client"); err == nil {
		t.Fatal("disabled account login accepted")
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE security_events SET event_type='TAMPERED'`); err == nil {
		t.Fatal("security history mutable")
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM audit_events`); err == nil {
		t.Fatal("audit history mutable")
	}
}
