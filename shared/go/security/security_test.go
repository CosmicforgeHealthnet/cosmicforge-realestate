package security

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecretNeverPrints(t *testing.T) {
	s := NewSecret("sk_live_supersecret")
	outputs := []string{
		fmt.Sprintf("%v %s %+v %#v %q", s, s, s, s, s),
		s.String(),
	}
	b, _ := json.Marshal(struct{ K Secret }{s})
	outputs = append(outputs, string(b))
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("cfg", "key", s)
	outputs = append(outputs, buf.String())
	for _, o := range outputs {
		if strings.Contains(o, "supersecret") {
			t.Fatalf("secret leaked: %s", o)
		}
	}
	if s.Reveal() != "sk_live_supersecret" {
		t.Fatal("reveal")
	}
}

func TestRandomToken(t *testing.T) {
	a, b := RandomToken(32), RandomToken(32)
	if a == b || len(a) != 43 {
		t.Fatalf("tokens %q %q", a, b)
	}
}

func TestHMAC(t *testing.T) {
	sig := HMACSHA256Hex([]byte("k"), []byte("payload"))
	if !VerifyHMACSHA256Hex([]byte("k"), []byte("payload"), sig) {
		t.Fatal("valid sig rejected")
	}
	if VerifyHMACSHA256Hex([]byte("k"), []byte("payload2"), sig) || VerifyHMACSHA256Hex([]byte("k2"), []byte("payload"), sig) {
		t.Fatal("invalid sig accepted")
	}
	if VerifyHMACSHA256Hex([]byte("k"), []byte("payload"), "zz") {
		t.Fatal("garbage accepted")
	}
}

func TestEd25519Parsing(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	pemPriv := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	pubDer, _ := x509.MarshalPKIXPublicKey(pub)
	pemPub := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDer}))

	for _, in := range []string{pemPriv, EncodeEd25519Seed(priv), base64.StdEncoding.EncodeToString(priv), base64.StdEncoding.EncodeToString(priv.Seed())} {
		k, err := ParseEd25519PrivateKey(in)
		if err != nil || !k.Equal(priv) {
			t.Fatalf("parse private %q: %v", in[:10], err)
		}
	}
	for _, in := range []string{pemPub, EncodeEd25519PublicKey(pub), base64.StdEncoding.EncodeToString(pub)} {
		k, err := ParseEd25519PublicKey(in)
		if err != nil || !k.Equal(pub) {
			t.Fatalf("parse public: %v", err)
		}
	}
	tampered := append([]byte(nil), priv...)
	tampered[40] ^= 0xFF
	if _, err := ParseEd25519PrivateKey(base64.StdEncoding.EncodeToString(tampered)); err == nil {
		t.Fatal("mismatched public half accepted")
	}
	for _, bad := range []string{"", "not base64!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := ParseEd25519PrivateKey(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
		if _, err := ParseEd25519PublicKey(bad); err == nil {
			t.Fatalf("accepted pub %q", bad)
		}
	}
}

func TestSensitiveKeys(t *testing.T) {
	for _, k := range []string{"password", "refresh_token", "Authorization", "PROVIDER_SECRET_KEY", "private_key", "access_token"} {
		if !IsSensitiveKey(k) {
			t.Errorf("%s should be sensitive", k)
		}
	}
	for _, k := range []string{"request_id", "service", "media_id", "status"} {
		if IsSensitiveKey(k) {
			t.Errorf("%s should not be sensitive", k)
		}
	}
}

func TestHeaders(t *testing.T) {
	h := Headers(HeadersConfig{HSTS: true})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	for _, k := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy", "Strict-Transport-Security", "Referrer-Policy", "Cache-Control"} {
		if rec.Header().Get(k) == "" {
			t.Errorf("missing %s", k)
		}
	}
}
