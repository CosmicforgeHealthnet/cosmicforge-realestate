package integration

import (
	"context"
	"encoding/base32"
	"testing"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth/credentials"
)

func TestBusinessMFAProtectedEnrolmentRecoveryAndReplay(t *testing.T) {
	service, db := newBusinessAuth(t)
	sealer, err := credentials.NewSealer("test-key", []byte("01234567890123456789012345678901"), nil)
	if err != nil {
		t.Fatal(err)
	}
	service.Sealer = sealer
	ctx := context.Background()
	password := "correct horse battery"
	reg, err := service.Register(ctx, businessauth.Registration{Email: "mfa@example.test", Password: password, DisplayName: "MFA Owner", OrganisationName: "MFA Test"}, "register-mfa")
	if err != nil {
		t.Fatal(err)
	}
	login := businessauth.Login{Email: "mfa@example.test", Password: password, OrganisationID: reg.OrganisationID}
	logged, err := service.Login(ctx, login, "mfa-client")
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := service.EnrolTOTP(ctx, reg.AccountID, logged.Tokens.SessionID, password)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrolled.Secret)
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	if err := db.Pool.QueryRow(ctx, `SELECT encrypted_secret FROM business_mfa_methods WHERE id=$1`, enrolled.MethodID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if string(ciphertext) == string(secret) {
		t.Fatal("MFA seed stored as plaintext")
	}
	code := (credentials.TOTP{}).Code(secret, credentials.Step(service.Now()))
	recovery, err := service.ConfirmTOTP(ctx, reg.AccountID, logged.Tokens.SessionID, code)
	if err != nil || len(recovery) != 8 {
		t.Fatalf("confirmation: %d %v", len(recovery), err)
	}
	if _, err := service.ConfirmTOTP(ctx, reg.AccountID, logged.Tokens.SessionID, code); err == nil {
		t.Fatal("completed enrolment returned secrets again")
	}
	if err := service.StepUp(ctx, reg.AccountID, logged.Tokens.SessionID, code, false); err == nil {
		t.Fatal("TOTP replay accepted")
	}
	challenge, err := service.Login(ctx, login, "mfa-client")
	if err != nil || !challenge.MFARequired || challenge.Tokens != nil {
		t.Fatalf("MFA bypass: %+v %v", challenge, err)
	}
	verified, err := service.CompleteMFALogin(ctx, challenge.MFAToken, recovery[0], true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Authenticate(ctx, verified.AccessToken); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteMFALogin(ctx, challenge.MFAToken, recovery[1], true); err == nil {
		t.Fatal("challenge reuse accepted")
	}
	if err := service.StepUp(ctx, reg.AccountID, verified.SessionID, recovery[0], true); err == nil {
		t.Fatal("recovery code reused")
	}
	challenge, err = service.Login(ctx, login, "mfa-client")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := service.CompleteMFALogin(ctx, challenge.MFAToken, "invalid-backup", true); err == nil {
			t.Fatal("invalid code accepted")
		}
	}
	if _, err := service.CompleteMFALogin(ctx, challenge.MFAToken, recovery[1], true); err == nil {
		t.Fatal("exhausted challenge accepted")
	}
	if err := service.RemoveMFA(ctx, reg.AccountID, logged.Tokens.SessionID, password); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Authenticate(ctx, verified.AccessToken); err == nil {
		t.Fatal("MFA removal did not revoke sessions")
	}
	after, err := service.Login(ctx, login, "mfa-client")
	if err != nil || after.MFARequired || after.Tokens == nil {
		t.Fatalf("removed method still required: %v", err)
	}
}
