package auth

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

func key(t *testing.T, kid string) jwt.SigningKey {
	_, priv, _ := ed25519.GenerateKey(nil)
	return jwt.SigningKey{KID: kid, PrivateKey: priv}
}

type fixture struct {
	ownerKey, artisanKey jwt.SigningKey
	owner, artisan       *Issuer
	verifier             *Verifier
	now                  time.Time
}

func setup(t *testing.T) *fixture {
	f := &fixture{ownerKey: key(t, "io-1"), artisanKey: key(t, "ar-1"), now: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return f.now }
	var err error
	f.owner, err = NewIssuer(IssuerConfig{Issuer: "individual-owner-service", Key: f.ownerKey, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	f.artisan, _ = NewIssuer(IssuerConfig{Issuer: "artisan-service", Key: f.artisanKey, Now: clock})
	f.verifier, err = NewVerifier(VerifierConfig{Now: clock, Issuers: []TrustedIssuer{
		{Issuer: "individual-owner-service", Domain: platform.DomainIndividualOwner, Keys: jwt.NewStaticKeys().Add("individual-owner-service", "io-1", f.ownerKey.PublicKey())},
		{Issuer: "artisan-service", Domain: platform.DomainArtisan, Keys: jwt.NewStaticKeys().Add("artisan-service", "ar-1", f.artisanKey.PublicKey())},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestIssueAndVerify(t *testing.T) {
	f := setup(t)
	sub, sid, org := ids.New(), ids.New(), ids.New()
	tok, claims, err := f.owner.Issue(IssueRequest{Subject: sub, SessionID: sid, OrganizationID: org})
	if err != nil {
		t.Fatal(err)
	}
	if claims.ExpiresAt.Time().Sub(claims.IssuedAt.Time()) != 15*time.Minute {
		t.Fatal("default TTL must be 15 minutes")
	}
	if _, err := ids.ParseV7(claims.ID); err != nil {
		t.Fatal("jti must be UUIDv7")
	}
	h, _ := jwt.UnverifiedHeader(tok)
	if h.Kid != "io-1" || h.Typ != TokenType {
		t.Fatalf("header %+v", h)
	}
	p, err := f.verifier.Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if p.Domain != platform.DomainIndividualOwner || p.ActorID != sub || p.SessionID != sid || p.OrganizationID != org {
		t.Fatalf("principal %+v", p)
	}
}

func TestVerifierRejections(t *testing.T) {
	f := setup(t)
	sub := ids.New()
	good, _, _ := f.owner.Issue(IssueRequest{Subject: sub})

	// Unknown issuer: a validly signed token from an issuer not in the trust list.
	rogueKey := key(t, "r-1")
	rogue, _ := NewIssuer(IssuerConfig{Issuer: "identity-service", Key: rogueKey, Now: func() time.Time { return f.now }})
	rogueTok, _, _ := rogue.Issue(IssueRequest{Subject: sub})

	// Issuer name spoof: claims iss=individual-owner-service but signed with artisan key.
	spoof, _ := NewIssuer(IssuerConfig{Issuer: "individual-owner-service", Key: jwt.SigningKey{KID: "io-1", PrivateKey: f.artisanKey.PrivateKey}, Now: func() time.Time { return f.now }})
	spoofTok, _, _ := spoof.Issue(IssueRequest{Subject: sub})

	// Wrong audience.
	wrongAud, _ := NewIssuer(IssuerConfig{Issuer: "individual-owner-service", Audience: "some-other-api", Key: f.ownerKey, Now: func() time.Time { return f.now }})
	wrongAudTok, _, _ := wrongAud.Issue(IssueRequest{Subject: sub})

	// Over-long lifetime signed by a trusted key directly.
	long := AccessClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "individual-owner-service", Subject: sub.String(), Audience: jwt.Audience{DefaultAudience},
		IssuedAt: jwt.NewNumericDate(f.now), ExpiresAt: jwt.NewNumericDate(f.now.Add(24 * time.Hour)), ID: "j"}}
	longTok, _ := jwt.Sign(f.ownerKey, TokenType, long)

	// Non-UUID subject.
	badSub := long
	badSub.ExpiresAt = jwt.NewNumericDate(f.now.Add(time.Minute))
	badSub.Subject = "user@example.com"
	badSubTok, _ := jwt.Sign(f.ownerKey, TokenType, badSub)

	// Service token presented as a user token.
	svc := long
	svc.ExpiresAt = jwt.NewNumericDate(f.now.Add(time.Minute))
	svcTok, _ := jwt.Sign(f.ownerKey, "service+jwt", svc)

	cases := map[string]struct {
		tok string
		err error
	}{
		"unknown issuer":   {rogueTok, ErrUntrustedIssuer},
		"invalid sig":      {spoofTok, jwt.ErrInvalidSignature},
		"wrong audience":   {wrongAudTok, jwt.ErrInvalidAudience},
		"lifetime":         {longTok, jwt.ErrLifetimeTooLong},
		"malformed sub":    {badSubTok, ErrInvalidSubject},
		"wrong token type": {svcTok, jwt.ErrUnexpectedType},
		"garbage":          {"x.y.z", jwt.ErrMalformed},
	}
	for name, tc := range cases {
		if _, err := f.verifier.Verify(context.Background(), tc.tok); !errors.Is(err, tc.err) {
			t.Errorf("%s: got %v want %v", name, err, tc.err)
		}
	}

	// Expiry honours 30s skew then rejects.
	f.now = f.now.Add(15*time.Minute + 10*time.Second)
	if _, err := f.verifier.Verify(context.Background(), good); err != nil {
		t.Fatalf("within skew should pass: %v", err)
	}
	f.now = f.now.Add(time.Minute)
	if _, err := f.verifier.Verify(context.Background(), good); !errors.Is(err, jwt.ErrExpired) {
		t.Fatalf("expected expiry, got %v", err)
	}
}

func TestCrossDomainTokenNormalizesToIssuerDomain(t *testing.T) {
	f := setup(t)
	sub := ids.New()
	tok, _, _ := f.artisan.Issue(IssueRequest{Subject: sub})
	p, err := f.verifier.Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if p.Domain != platform.DomainArtisan {
		t.Fatalf("artisan token must normalize to artisan domain, got %s", p.Domain)
	}
}

func TestConfigValidation(t *testing.T) {
	k := key(t, "k")
	if _, err := NewIssuer(IssuerConfig{Issuer: "x", Key: k, TTL: 2 * time.Hour}); err == nil {
		t.Fatal("excessive TTL accepted")
	}
	if _, err := NewIssuer(IssuerConfig{Key: k}); err == nil {
		t.Fatal("missing issuer accepted")
	}
	if _, _, err := (&Issuer{cfg: IssuerConfig{Issuer: "x", Key: k, TTL: time.Minute, Now: time.Now}}).Issue(IssueRequest{}); !errors.Is(err, ErrInvalidSubject) {
		t.Fatal("nil subject accepted")
	}
	res := jwt.NewStaticKeys().Add("a", "k", k.PublicKey())
	if _, err := NewVerifier(VerifierConfig{Issuers: []TrustedIssuer{{Issuer: "a", Domain: "identity", Keys: res}}}); err == nil {
		t.Fatal("unknown domain accepted")
	}
	if _, err := NewVerifier(VerifierConfig{Issuers: []TrustedIssuer{
		{Issuer: "a", Domain: platform.DomainArtisan, Keys: res}, {Issuer: "b", Domain: platform.DomainArtisan, Keys: res},
	}}); err == nil {
		t.Fatal("two issuers for one domain accepted")
	}
}

func TestParseTrustedIssuers(t *testing.T) {
	k := key(t, "k")
	pub := securityEncode(k)
	good := `[{"issuer":"artisan-service","domain":"artisan","keys":[{"kid":"k","public_key":"` + pub + `"}]},
	          {"issuer":"admin-service","domain":"admin","jwks_url":"https://admin.internal/jwks"}]`
	list, err := ParseTrustedIssuers([]byte(good), nil)
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %v", list, err)
	}
	if _, err := NewVerifier(VerifierConfig{Issuers: list}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`[]`, `{}`, `[{"issuer":"x","domain":"identity","keys":[]}]`, `[{"issuer":"x","domain":"artisan"}]`,
		`[{"issuer":"x","domain":"artisan","keys":[{"kid":"k","public_key":"bad"}]}]`} {
		if _, err := ParseTrustedIssuers([]byte(bad), nil); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func securityEncode(k jwt.SigningKey) string { return security.EncodeEd25519PublicKey(k.PublicKey()) }
