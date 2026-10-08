package apperrors

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestUnknownErrorsMapToInternal(t *testing.T) {
	raw := errors.New(`pq: relation "users" does not exist; password=hunter2`)
	e := From(raw)
	if e.Code != CodeInternal || e.Status != http.StatusInternalServerError {
		t.Fatalf("got %s %d", e.Code, e.Status)
	}
	if !errors.Is(e, raw) {
		t.Fatal("cause should be retained for logging")
	}
	env, _ := json.Marshal(ToEnvelope(raw, "req-1"))
	s := string(env)
	if strings.Contains(s, "relation") || strings.Contains(s, "hunter2") || strings.Contains(s, "pq:") {
		t.Fatalf("internal detail leaked: %s", s)
	}
	if s != `{"error":{"code":"INTERNAL_ERROR","message":"An internal error occurred.","request_id":"req-1"}}` {
		t.Fatalf("unexpected envelope %s", s)
	}
}

func TestWrappedPlatformErrorPreserved(t *testing.T) {
	base := NotFound("PROPERTY_NOT_FOUND", "Property not found.").WithCause(sql.ErrNoRows)
	wrapped := fmt.Errorf("loading property: %w", base)
	e := From(wrapped)
	if e.Code != "PROPERTY_NOT_FOUND" || e.Status != 404 {
		t.Fatalf("got %+v", e)
	}
	env, _ := json.Marshal(ToEnvelope(wrapped, "r"))
	if strings.Contains(string(env), "no rows") {
		t.Fatal("cause leaked")
	}
	if !errors.Is(wrapped, NotFound("PROPERTY_NOT_FOUND", "other")) {
		t.Fatal("Is should match by code")
	}
	if !errors.Is(wrapped, sql.ErrNoRows) {
		t.Fatal("Is should reach the cause")
	}
}

func TestStatusMapping(t *testing.T) {
	cases := []struct {
		err    *Error
		status int
		code   string
	}{
		{InvalidRequest("bad"), 400, CodeInvalidRequest},
		{Invalid("INVALID_CURRENCY", "bad currency"), 400, "INVALID_CURRENCY"},
		{AuthenticationRequired(), 401, CodeAuthenticationRequired},
		{InvalidToken(), 401, CodeInvalidToken},
		{PermissionDenied(""), 403, CodePermissionDenied},
		{NotFound("MEDIA_NOT_FOUND", "x"), 404, "MEDIA_NOT_FOUND"},
		{IdempotencyConflict(), 409, CodeIdempotencyConflict},
		{IdempotencyInProgress(), 409, CodeIdempotencyInProgress},
		{InvalidStateTransition("DRAFT", "VERIFIED"), 409, CodeInvalidStateTransition},
		{Unprocessable("UNSUPPORTED_SUBJECT", "x"), 422, "UNSUPPORTED_SUBJECT"},
		{RateLimited(3), 429, CodeRateLimited},
		{Internal(nil), 500, CodeInternal},
		{DownstreamProvider(OutcomeUnknown, nil), 502, CodeDownstreamProvider},
		{Unavailable(""), 503, CodeServiceUnavailable},
		{PayloadTooLarge(), 413, CodePayloadTooLarge},
	}
	for _, c := range cases {
		if c.err.Status != c.status || c.err.Code != c.code {
			t.Errorf("%s: got %d %s", c.code, c.err.Status, c.err.Code)
		}
		if !ValidCode(c.err.Code) {
			t.Errorf("code %s not SCREAMING_SNAKE_CASE", c.err.Code)
		}
	}
}

func TestDetailsAndOutcome(t *testing.T) {
	e := InvalidRequest("Validation failed.", Detail{Field: "email", Code: "REQUIRED", Message: "email is required"})
	env, _ := json.Marshal(ToEnvelope(e, "r1"))
	if !strings.Contains(string(env), `"details":[{"field":"email","code":"REQUIRED","message":"email is required"}]`) {
		t.Fatalf("details missing: %s", env)
	}
	p := DownstreamProvider(OutcomeUnknown, errors.New("timeout"))
	if p.Outcome != OutcomeUnknown {
		t.Fatal("outcome lost")
	}
	// copies must not mutate originals
	base := NotFound("X_NOT_FOUND", "x")
	_ = base.WithDetails(Detail{Code: "A", Message: "a"})
	if len(base.Details) != 0 {
		t.Fatal("WithDetails mutated receiver")
	}
}

func TestNewRejectsBadCodes(t *testing.T) {
	for _, bad := range []string{"lower", "Mixed_Case", "TRAILING_", "_LEADING", "DOUBLE__UNDERSCORE", ""} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("expected panic for %q", bad)
				}
			}()
			New(400, bad, "m")
		}()
	}
	defer func() {
		if recover() == nil {
			t.Error("expected panic for status 200")
		}
	}()
	New(200, "OK_CODE", "m")
}

func TestNilHandling(t *testing.T) {
	if From(nil) != nil || CodeOf(nil) != "" {
		t.Fatal("nil should stay nil")
	}
}
