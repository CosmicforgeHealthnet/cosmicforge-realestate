package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

func TestStructuredFieldsAndContext(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{Service: "verification-service", Environment: "test", Output: &buf})
	actor := ids.New()
	ctx := requestcontext.With(context.Background(), requestcontext.Values{
		RequestID: "req-1", TraceID: "abc123", CallingService: "api-gateway",
		Actor: requestcontext.Actor{Domain: "artisan", ID: actor},
	})
	l.InfoContext(ctx, "hello", "media_id", "m1")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"level": "info", "service": "verification-service", "environment": "test",
		"message": "hello", "request_id": "req-1", "trace_id": "abc123",
		"calling_service": "api-gateway", "actor_domain": "artisan", "actor_id": actor.String(), "media_id": "m1",
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("%s = %v want %v", k, rec[k], v)
		}
	}
	if _, ok := rec["timestamp"]; !ok {
		t.Error("timestamp missing")
	}
}

func TestSecretsRedacted(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{Service: "s", Environment: "test", Output: &buf})
	l.Info("login",
		"password", "hunter2",
		"refresh_token", "rt_abcdef",
		"authorization", "Bearer eyJhbGciOiJFZERTQSJ9.x.y",
		"signing_key", "seedseedseed",
		"cfg", security.NewSecret("sk_live_xyz"),
	)
	l.WithGroup("provider").Info("call", "api_key", "pk_123")
	out := buf.String()
	for _, leak := range []string{"hunter2", "rt_abcdef", "eyJhbGci", "seedseedseed", "sk_live_xyz", "pk_123"} {
		if strings.Contains(out, leak) {
			t.Fatalf("leaked %q in %s", leak, out)
		}
	}
	if !strings.Contains(out, security.Redacted) {
		t.Fatal("expected redaction marker")
	}
}

func TestNestedSecretsAndOpaqueErrors(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{Service: "owner", Environment: "test", Output: &buf})
	l.Error("provider call failed", "error", "request https://private.example/download?signature=TEST-private-url otp TEST-otp",
		"error_class", "DEPENDENCY_TIMEOUT", "payload", map[string]any{"safe": "diagnostic", "nested": []any{map[string]any{"mfa_code": "TEST-mfa", "account_number": "TEST-bank", "document_url": "TEST-document", "refresh_token": "TEST-refresh"}}})
	for _, value := range []string{"TEST-private-url", "TEST-otp", "TEST-mfa", "TEST-bank", "TEST-document", "TEST-refresh"} {
		if strings.Contains(buf.String(), value) {
			t.Fatalf("sensitive fixture leaked: %s", value)
		}
	}
	if !strings.Contains(buf.String(), "DEPENDENCY_TIMEOUT") || !strings.Contains(buf.String(), "diagnostic") {
		t.Fatal("safe diagnostics lost")
	}
}

func TestLevels(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{Service: "s", Environment: "test", Output: &buf, Level: "warn"})
	l.Info("hidden")
	l.Warn("shown")
	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), "shown") {
		t.Fatal(buf.String())
	}
}

func TestContextLogger(t *testing.T) {
	l := Discard()
	if FromContext(WithLogger(context.Background(), l)) != l {
		t.Fatal("logger not stored")
	}
	if FromContext(context.Background()) == nil {
		t.Fatal("default expected")
	}
}
