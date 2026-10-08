package idempotency

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

func TestServeCommand(t *testing.T) {
	s, db := newStore(t, 5*time.Second)
	h := requestcontext.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(requestcontext.WithCallingService(r.Context(), "artisan-service"))
		body, err := httpx.ReadBody(w, r, 1024)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		s.ServeCommand(w, r, "widget.create", body, nil, func(ctx context.Context, tx pgx.Tx) (Result, error) {
			return create("w")(ctx, tx)
		})
	}))
	do := func(key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/internal/v1/widgets", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("", `{}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "IDEMPOTENCY_KEY_REQUIRED") {
		t.Fatalf("missing key: %d %s", rec.Code, rec.Body)
	}
	if rec := do("bad key", `{}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "INVALID_IDEMPOTENCY_KEY") {
		t.Fatalf("bad key: %d %s", rec.Code, rec.Body)
	}
	first := do("k1", `{"a":1}`)
	second := do("k1", `{ "a": 1 }`)
	if first.Code != 201 || second.Code != 201 || second.Header().Get(HeaderReplayed) != "true" || first.Body.String() != second.Body.String() {
		t.Fatalf("replay: %d %d %q", first.Code, second.Code, second.Header().Get(HeaderReplayed))
	}
	if rec := do("k1", `{"a":2}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "IDEMPOTENCY_CONFLICT") {
		t.Fatalf("conflict: %d %s", rec.Code, rec.Body)
	}
	if countWidgets(t, db) != 1 {
		t.Fatal("executed more than once")
	}
}
