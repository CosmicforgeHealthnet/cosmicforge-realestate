package jwt

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testClaims struct {
	RegisteredClaims
	Extra string `json:"extra,omitempty"`
}

func newKey(t *testing.T, kid string) SigningKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return SigningKey{KID: kid, PrivateKey: priv}
}

func claimsAt(now time.Time) testClaims {
	return testClaims{RegisteredClaims: RegisteredClaims{
		Issuer: "iss-a", Subject: "sub", Audience: Audience{"aud-x"},
		IssuedAt: NewNumericDate(now), ExpiresAt: NewNumericDate(now.Add(5 * time.Minute)), ID: "jti-1",
	}, Extra: "e"}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	k := newKey(t, "k1")
	res := NewStaticKeys().Add("iss-a", "k1", k.PublicKey())
	tok, err := Sign(k, "at+jwt", claimsAt(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	var out testClaims
	h, err := Verify(context.Background(), tok, "at+jwt", res, &out)
	if err != nil {
		t.Fatal(err)
	}
	if h.Kid != "k1" || out.Extra != "e" || out.Subject != "sub" {
		t.Fatalf("decoded %+v %+v", h, out)
	}
	if err := out.Validate(ValidationOptions{Audience: "aud-x"}); err != nil {
		t.Fatal(err)
	}
}

func TestRejections(t *testing.T) {
	k := newKey(t, "k1")
	other := newKey(t, "k1")
	res := NewStaticKeys().Add("iss-a", "k1", k.PublicKey())
	good, _ := Sign(k, "at+jwt", claimsAt(time.Now()))
	forged, _ := Sign(other, "at+jwt", claimsAt(time.Now()))
	unknownKid, _ := Sign(newKey(t, "k9"), "at+jwt", claimsAt(time.Now()))
	c := claimsAt(time.Now())
	c.Issuer = "iss-b"
	unknownIss, _ := Sign(k, "at+jwt", c)

	parts := strings.Split(good, ".")
	noneHeader := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"k1"}`))
	hsHeader := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","kid":"k1"}`))
	critHeader := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","kid":"k1","crit":["exp"]}`))
	tamperedPayload := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"iss-a","aud":"aud-x","sub":"admin","exp":9999999999,"iat":1,"jti":"x"}`))

	cases := map[string]struct {
		tok string
		typ string
		err error
	}{
		"forged signature": {forged, "at+jwt", ErrInvalidSignature},
		"tampered payload": {parts[0] + "." + tamperedPayload + "." + parts[2], "at+jwt", ErrInvalidSignature},
		"alg none":         {noneHeader + "." + parts[1] + "." + parts[2], "", ErrUnsupportedAlg},
		"alg HS256":        {hsHeader + "." + parts[1] + "." + parts[2], "", ErrUnsupportedAlg},
		"crit":             {critHeader + "." + parts[1] + "." + parts[2], "", ErrMalformed},
		"unknown kid":      {unknownKid, "at+jwt", ErrUnknownKey},
		"unknown issuer":   {unknownIss, "at+jwt", ErrInvalidIssuer},
		"wrong typ":        {good, "service+jwt", ErrUnexpectedType},
		"two parts":        {parts[0] + "." + parts[1], "", ErrMalformed},
		"empty":            {"", "", ErrMalformed},
		"garbage":          {"a.b.c", "", ErrMalformed},
		"oversized":        {strings.Repeat("a", MaxTokenSize+1), "", ErrMalformed},
		"bad sig encoding": {parts[0] + "." + parts[1] + ".!!!", "", ErrMalformed},
		"short signature":  {parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString([]byte("x")), "", ErrMalformed},
	}
	for name, tc := range cases {
		var out testClaims
		_, err := Verify(context.Background(), tc.tok, tc.typ, res, &out)
		if !errors.Is(err, tc.err) {
			t.Errorf("%s: got %v want %v", name, err, tc.err)
		}
	}
}

func TestMalformedClaimsAfterValidSignature(t *testing.T) {
	k := newKey(t, "k1")
	res := NewStaticKeys().Add("iss-a", "k1", k.PublicKey())
	// aud as number is malformed even though signed correctly.
	tok, _ := Sign(k, "", map[string]any{"iss": "iss-a", "aud": 5, "exp": 1, "iat": 1, "jti": "j"})
	var out testClaims
	if _, err := Verify(context.Background(), tok, "", res, &out); !errors.Is(err, ErrMalformed) {
		t.Fatalf("got %v", err)
	}
}

func TestClaimValidationTimePolicy(t *testing.T) {
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	c := claimsAt(base).RegisteredClaims
	at := func(d time.Duration) ValidationOptions {
		return ValidationOptions{Now: func() time.Time { return base.Add(d) }, Audience: "aud-x", MaxLifetime: 15 * time.Minute}
	}
	if err := c.Validate(at(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Within default leeway after expiry: accepted.
	if err := c.Validate(at(5*time.Minute + 20*time.Second)); err != nil {
		t.Fatalf("leeway: %v", err)
	}
	if err := c.Validate(at(5*time.Minute + 31*time.Second)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expected expired, got %v", err)
	}
	if err := c.Validate(at(-time.Minute)); !errors.Is(err, ErrIssuedInFuture) {
		t.Fatalf("expected iat future, got %v", err)
	}
	if err := c.Validate(at(-20 * time.Second)); err != nil {
		t.Fatalf("skew within leeway should pass: %v", err)
	}
	// Leeway is capped at MaxLeeway.
	o := at(5*time.Minute + 3*time.Minute)
	o.Leeway = time.Hour
	if err := c.Validate(o); !errors.Is(err, ErrExpired) {
		t.Fatalf("leeway cap not enforced: %v", err)
	}
	wrongAud := at(0)
	wrongAud.Audience = "other"
	if err := c.Validate(wrongAud); !errors.Is(err, ErrInvalidAudience) {
		t.Fatalf("aud: %v", err)
	}
	long := c
	long.ExpiresAt = NewNumericDate(base.Add(time.Hour))
	if err := long.Validate(at(0)); !errors.Is(err, ErrLifetimeTooLong) {
		t.Fatalf("lifetime: %v", err)
	}
	nbf := c
	nbf.NotBefore = NewNumericDate(base.Add(2 * time.Minute))
	if err := nbf.Validate(at(0)); !errors.Is(err, ErrNotYetValid) {
		t.Fatalf("nbf: %v", err)
	}
	missing := c
	missing.ID = ""
	if err := missing.Validate(at(0)); !errors.Is(err, ErrMissingClaim) {
		t.Fatalf("missing jti: %v", err)
	}
}

func TestAudienceJSON(t *testing.T) {
	b, _ := json.Marshal(Audience{"a"})
	if string(b) != `"a"` {
		t.Fatal(string(b))
	}
	var a Audience
	if err := json.Unmarshal([]byte(`["a","b"]`), &a); err != nil || !a.Contains("b") {
		t.Fatal("array aud")
	}
}

func TestKeyRotation(t *testing.T) {
	old, cur := newKey(t, "2026-09"), newKey(t, "2026-10")
	res := NewStaticKeys().Add("iss-a", old.KID, old.PublicKey()).Add("iss-a", cur.KID, cur.PublicKey())
	for _, k := range []SigningKey{old, cur} {
		tok, _ := Sign(k, "", claimsAt(time.Now()))
		var out testClaims
		if _, err := Verify(context.Background(), tok, "", res, &out); err != nil {
			t.Fatalf("kid %s: %v", k.KID, err)
		}
	}
}

func TestJWKSAndRemoteResolver(t *testing.T) {
	k1, k2 := newKey(t, "k1"), newKey(t, "k2")
	var current atomic.Value
	current.Store(JWKS{Keys: []JWK{PublicJWK("k1", k1.PublicKey())}})
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		_ = json.NewEncoder(w).Encode(current.Load())
	}))
	defer srv.Close()
	now := time.Now()
	r := &RemoteJWKS{Issuer: "iss-a", URL: srv.URL, MinRefreshInterval: time.Minute, Now: func() time.Time { return now }}
	ctx := context.Background()
	if _, err := r.ResolveKey(ctx, "iss-a", "k1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ResolveKey(ctx, "iss-b", "k1"); !errors.Is(err, ErrInvalidIssuer) {
		t.Fatal("issuer must match")
	}
	// Rotation: k2 published; unknown kid triggers refresh only after min interval.
	current.Store(JWKS{Keys: []JWK{PublicJWK("k1", k1.PublicKey()), PublicJWK("k2", k2.PublicKey())}})
	for i := 0; i < 20; i++ {
		_, _ = r.ResolveKey(ctx, "iss-a", "random-"+string(rune('a'+i)))
	}
	if fetches.Load() != 1 {
		t.Fatalf("unknown kids caused %d fetches", fetches.Load())
	}
	now = now.Add(2 * time.Minute)
	if _, err := r.ResolveKey(ctx, "iss-a", "k2"); err != nil {
		t.Fatalf("rotated key not picked up: %v", err)
	}
	// Non-Ed25519 keys are rejected.
	if _, err := ParseJWKS([]byte(`{"keys":[{"kty":"RSA","kid":"r","n":"x","e":"AQAB"}]}`)); err == nil {
		t.Fatal("RSA JWK accepted")
	}
}
