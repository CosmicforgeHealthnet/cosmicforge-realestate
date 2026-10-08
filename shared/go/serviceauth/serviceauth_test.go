package serviceauth

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

type world struct {
	now      time.Time
	gwKey    jwt.SigningKey
	ioKey    jwt.SigningKey
	gw, io   *Signer
	verifier *Verifier
	registry *Registry
}

func key(kid string) jwt.SigningKey {
	_, priv, _ := ed25519.GenerateKey(nil)
	return jwt.SigningKey{KID: kid, PrivateKey: priv}
}

func newWorld(t *testing.T) *world {
	w := &world{now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC), gwKey: key("gw-1"), ioKey: key("io-1")}
	clock := func() time.Time { return w.now }
	var err error
	w.gw, err = NewSigner(SignerConfig{Service: platform.APIGateway, Key: w.gwKey, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	w.io, _ = NewSigner(SignerConfig{Service: platform.IndividualOwnerService, Key: w.ioKey, Now: clock})
	w.registry = NewRegistry()
	_ = w.registry.Add(platform.APIGateway, "gw-1", w.gwKey.PublicKey())
	_ = w.registry.Add(platform.IndividualOwnerService, "io-1", w.ioKey.PublicKey())
	w.verifier, err = NewVerifier(VerifierConfig{Self: platform.VerificationService, Registry: w.registry, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestValidTokenWithDelegation(t *testing.T) {
	w := newWorld(t)
	actor, org := ids.New(), ids.New()
	ctx := requestcontext.With(context.Background(), requestcontext.Values{
		RequestID: "req-1", Actor: requestcontext.Actor{Domain: "individual-owner", ID: actor}, OrganizationID: org,
	})
	tok, err := w.io.Token(ctx, "verification-service")
	if err != nil {
		t.Fatal(err)
	}
	h, _ := jwt.UnverifiedHeader(tok)
	if h.Typ != TokenType || h.Kid != "io-1" {
		t.Fatalf("header %+v", h)
	}
	p, err := w.verifier.Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if p.Service != platform.IndividualOwnerService || p.Delegation.ActorID != actor ||
		p.Delegation.ActorDomain != platform.DomainIndividualOwner || p.Delegation.OrganizationID != org ||
		p.Delegation.OriginalRequestID != "req-1" {
		t.Fatalf("principal %+v", p)
	}
	if p.ExpiresAt.Sub(w.now) > MaxTTL {
		t.Fatal("lifetime exceeds 5 minutes")
	}
}

func TestRejections(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	// Wrong audience: token for media-document-service presented to verification.
	wrongAud, _ := w.io.TokenFor("media-document-service", Delegation{})
	// Wrong service: notification-service is not in the registry.
	unknown, _ := NewSigner(SignerConfig{Service: platform.NotificationService, Key: key("n-1"), Now: func() time.Time { return w.now }})
	unknownTok, _ := unknown.TokenFor("verification-service", Delegation{})
	// Invalid signature: claims to be api-gateway/gw-1 but signed with another key.
	forger, _ := NewSigner(SignerConfig{Service: platform.APIGateway, Key: jwt.SigningKey{KID: "gw-1", PrivateKey: key("x").PrivateKey}, Now: func() time.Time { return w.now }})
	forged, _ := forger.TokenFor("verification-service", Delegation{})
	// Expired.
	expired, _ := w.io.TokenFor("verification-service", Delegation{})
	// User access token presented as service token.
	userTok, _ := jwt.Sign(w.ioKey, "at+jwt", Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "individual-owner-service",
		Audience: jwt.Audience{"verification-service"}, IssuedAt: jwt.NewNumericDate(w.now), ExpiresAt: jwt.NewNumericDate(w.now.Add(time.Minute)), ID: "j"}})
	// Over-long lifetime.
	longTok, _ := jwt.Sign(w.ioKey, TokenType, Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "individual-owner-service",
		Audience: jwt.Audience{"verification-service"}, IssuedAt: jwt.NewNumericDate(w.now), ExpiresAt: jwt.NewNumericDate(w.now.Add(time.Hour)), ID: "j"}})
	// Multiple audiences.
	multiAud, _ := jwt.Sign(w.ioKey, TokenType, Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "individual-owner-service",
		Audience: jwt.Audience{"verification-service", "payment-service"}, IssuedAt: jwt.NewNumericDate(w.now), ExpiresAt: jwt.NewNumericDate(w.now.Add(time.Minute)), ID: "j"}})
	// Bad delegation (actor_domain without actor_id; unknown domain).
	badDel, _ := jwt.Sign(w.ioKey, TokenType, Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "individual-owner-service",
		Audience: jwt.Audience{"verification-service"}, IssuedAt: jwt.NewNumericDate(w.now), ExpiresAt: jwt.NewNumericDate(w.now.Add(time.Minute)), ID: "j"}, ActorDomain: "admin"})
	badDomain, _ := jwt.Sign(w.ioKey, TokenType, Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "individual-owner-service",
		Audience: jwt.Audience{"verification-service"}, IssuedAt: jwt.NewNumericDate(w.now), ExpiresAt: jwt.NewNumericDate(w.now.Add(time.Minute)), ID: "j"}, ActorDomain: "identity", ActorID: ids.New().String()})

	check := func(name, tok string, want error) {
		t.Helper()
		if _, err := w.verifier.Verify(ctx, tok); !errors.Is(err, want) {
			t.Errorf("%s: got %v want %v", name, err, want)
		}
	}
	check("wrong audience", wrongAud, ErrWrongAudience)
	check("wrong service", unknownTok, ErrUnknownService)
	check("invalid signature", forged, jwt.ErrInvalidSignature)
	check("user token", userTok, jwt.ErrUnexpectedType)
	check("lifetime", longTok, jwt.ErrLifetimeTooLong)
	check("multi audience", multiAud, ErrWrongAudience)
	check("half delegation", badDel, ErrBadDelegation)
	check("unknown domain", badDomain, ErrBadDelegation)
	w.now = w.now.Add(10 * time.Minute)
	check("expired", expired, jwt.ErrExpired)
}

func TestSignerConfig(t *testing.T) {
	if _, err := NewSigner(SignerConfig{Service: "identity-service", Key: key("k")}); err == nil {
		t.Fatal("unknown service accepted")
	}
	if _, err := NewSigner(SignerConfig{Service: platform.APIGateway, Key: key("k"), TTL: 10 * time.Minute}); err == nil {
		t.Fatal("TTL > 5m accepted")
	}
	s, _ := NewSigner(SignerConfig{Service: platform.APIGateway, Key: key("k")})
	if _, err := s.TokenFor("evil-service", Delegation{}); err == nil {
		t.Fatal("unknown audience accepted")
	}
}

func TestRegistryJSON(t *testing.T) {
	k := key("gw-1")
	data, _ := json.Marshal([]map[string]any{{"service": "api-gateway", "keys": []map[string]string{{"kid": "gw-1", "public_key": security.EncodeEd25519PublicKey(k.PublicKey())}}}})
	r, err := ParseRegistry(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ResolveKey(context.Background(), "api-gateway", "gw-1"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`[{"service":"identity-service","keys":[{"kid":"a","public_key":"` + security.EncodeEd25519PublicKey(k.PublicKey()) + `"}]}]`, `[{"service":"api-gateway","keys":[]}]`, `nope`} {
		if _, err := ParseRegistry([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestMiddlewareAuthAndCapabilityPolicy(t *testing.T) {
	w := newWorld(t)
	policy := NewPolicy().Allow("verification.create", platform.IndividualOwnerService)
	var seen requestcontext.Values
	h := requestcontext.Middleware(Authenticate(w.verifier, HeaderAuthorization)(Require(policy, "verification.create")(
		http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			seen = requestcontext.From(r.Context())
			rw.WriteHeader(204)
		}))))

	do := func(tok string) (int, string) {
		req := httptest.NewRequest("POST", "/internal/v1/verifications", nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var env apperrors.Envelope
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		return rec.Code, env.Error.Code
	}

	actor := ids.New()
	allowed, _ := w.io.TokenFor("verification-service", Delegation{ActorDomain: platform.DomainIndividualOwner, ActorID: actor})
	if code, _ := do(allowed); code != 204 {
		t.Fatalf("allowed caller got %d", code)
	}
	if seen.CallingService != "individual-owner-service" || seen.Actor.ID != actor || seen.Actor.Domain != "individual-owner" {
		t.Fatalf("request context not populated: %+v", seen)
	}
	// Valid token, authenticated service, but NOT authorized for the capability.
	gwTok, _ := w.gw.TokenFor("verification-service", Delegation{})
	if code, ec := do(gwTok); code != 403 || ec != apperrors.CodePermissionDenied {
		t.Fatalf("valid-but-unauthorized caller got %d %s", code, ec)
	}
	if code, ec := do(""); code != 401 || ec != apperrors.CodeAuthenticationRequired {
		t.Fatalf("missing token got %d %s", code, ec)
	}
	if code, ec := do("garbage"); code != 401 || ec != apperrors.CodeInvalidToken {
		t.Fatalf("garbage token got %d %s", code, ec)
	}
	wrongAud, _ := w.io.TokenFor("payment-service", Delegation{})
	if code, _ := do(wrongAud); code != 401 {
		t.Fatalf("wrong audience got %d", code)
	}
}

func TestLoadIdentity(t *testing.T) {
	gw := key("gw-1")
	reg, _ := json.Marshal([]map[string]any{{"service": "api-gateway", "keys": []map[string]string{{"kid": "gw-1", "public_key": security.EncodeEd25519PublicKey(gw.PublicKey())}}}})
	self := key("vs-1")
	l := config.FromMap(map[string]string{
		"SERVICE_AUTH_KEY_ID": "vs-1", "SERVICE_AUTH_PRIVATE_KEY": security.EncodeEd25519Seed(self.PrivateKey),
		"SERVICE_AUTH_TRUSTED_KEYS": string(reg),
	})
	id, err := LoadIdentity(l, platform.VerificationService)
	if err != nil {
		t.Fatal(err)
	}
	gs, _ := NewSigner(SignerConfig{Service: platform.APIGateway, Key: gw})
	tok, _ := gs.TokenFor("verification-service", Delegation{})
	if _, err := id.Verifier.Verify(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIdentity(config.FromMap(map[string]string{}), platform.VerificationService); err == nil {
		t.Fatal("missing config accepted")
	}
	bad := config.FromMap(map[string]string{"SERVICE_AUTH_KEY_ID": "k", "SERVICE_AUTH_PRIVATE_KEY": "nope", "SERVICE_AUTH_TRUSTED_KEYS": "[]"})
	if _, err := LoadIdentity(bad, platform.VerificationService); err == nil {
		t.Fatal("bad key accepted")
	}
}
