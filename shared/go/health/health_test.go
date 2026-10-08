package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/buildinfo"
)

type mux map[string]http.HandlerFunc

func (m mux) Get(p string, fn http.HandlerFunc) { m[p] = fn }

func call(m mux, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	m[path](rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestHealthReadyVersion(t *testing.T) {
	dbErr := error(nil)
	h := New("verification-service", "staging", 200*time.Millisecond,
		Check{Name: "postgres", Fn: func(context.Context) error { return dbErr }},
		Check{Name: "nats", Fn: func(context.Context) error { return nil }},
	)
	m := mux{}
	h.Register(m)
	if rec := call(m, "/health"); rec.Code != 200 {
		t.Fatal("health")
	}
	if rec := call(m, "/ready"); rec.Code != 200 {
		t.Fatalf("ready %d %s", rec.Code, rec.Body)
	}
	dbErr = errors.New("dial tcp 10.0.0.5:5432: password authentication failed for user x")
	rec := call(m, "/ready")
	if rec.Code != 503 || strings.Contains(rec.Body.String(), "password") || !strings.Contains(rec.Body.String(), `"postgres","status":"fail"`) {
		t.Fatalf("not-ready %d %s", rec.Code, rec.Body)
	}
	buildinfo.Version, buildinfo.Commit = "1.0.0", "abc123"
	rec = call(m, "/version")
	var v VersionInfo
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.Service != "verification-service" || v.Version != "1.0.0" || v.Commit != "abc123" || v.Environment != "staging" {
		t.Fatalf("version %+v", v)
	}
}

func TestReadyTimeoutAndShutdown(t *testing.T) {
	h := New("s", "test", 50*time.Millisecond, Check{Name: "slow", Fn: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}})
	m := mux{}
	h.Register(m)
	start := time.Now()
	if rec := call(m, "/ready"); rec.Code != 503 || time.Since(start) > time.Second {
		t.Fatal("slow check must time out to 503")
	}
	h2 := New("s", "test", 0)
	m2 := mux{}
	h2.Register(m2)
	h2.SetShuttingDown()
	if rec := call(m2, "/ready"); rec.Code != 503 {
		t.Fatal("shutting down must be not ready")
	}
	if rec := call(m2, "/health"); rec.Code != 200 {
		t.Fatal("liveness unaffected by shutdown")
	}
}
