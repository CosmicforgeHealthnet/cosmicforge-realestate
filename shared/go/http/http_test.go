package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

func decodeEnv(t *testing.T, rec *httptest.ResponseRecorder) apperrors.Envelope {
	t.Helper()
	var env apperrors.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("not an envelope: %s", rec.Body.String())
	}
	return env
}

func TestWriteErrorEnvelopeIncludesRequestIDAndHidesCause(t *testing.T) {
	var logs bytes.Buffer
	l := logging.New(logging.Config{Service: "t", Environment: "test", Output: &logs})
	h := WithLogger(l)(requestcontext.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, errors.New("SELECT * FROM secrets failed"))
	})))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("X-Request-ID", "req-abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Fatalf("status %d", rec.Code)
	}
	env := decodeEnv(t, rec)
	if env.Error.Code != "INTERNAL_ERROR" || env.Error.RequestID != "req-abc" {
		t.Fatalf("envelope %+v", env)
	}
	if strings.Contains(rec.Body.String(), "SELECT") {
		t.Fatal("cause leaked to client")
	}
	if strings.Contains(logs.String(), "SELECT * FROM secrets failed") || !strings.Contains(logs.String(), "req-abc") || !strings.Contains(logs.String(), "INTERNAL_ERROR") {
		t.Fatalf("opaque cause and safe code/request ID expected: %s", logs.String())
	}
	if rec.Header().Get("Content-Type") != ContentTypeJSON {
		t.Fatal("content type")
	}
}

func TestRetryAfterHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest("GET", "/", nil), apperrors.RateLimited(7))
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "7" {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Retry-After"))
	}
}

type cmd struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestDecodeJSON(t *testing.T) {
	cases := []struct {
		body, ct, code string
		status         int
	}{
		{`{"name":"a","count":1}`, "application/json", "", 0},
		{`{"name":"a"}`, "text/plain", apperrors.CodeUnsupportedMediaType, 415},
		{`{"name":"a","extra":1}`, "application/json", apperrors.CodeInvalidJSON, 400},
		{`{"name":"a","count":"x"}`, "application/json", apperrors.CodeInvalidJSON, 400},
		{`{"name":`, "application/json", apperrors.CodeInvalidJSON, 400},
		{``, "application/json", apperrors.CodeInvalidJSON, 400},
		{`{"name":"a"}{"name":"b"}`, "application/json", apperrors.CodeInvalidJSON, 400},
		{`{"name":"` + strings.Repeat("a", 2000) + `"}`, "application/json; charset=utf-8", apperrors.CodePayloadTooLarge, 413},
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", "/", strings.NewReader(c.body))
		req.Header.Set("Content-Type", c.ct)
		var dst cmd
		err := DecodeJSON(httptest.NewRecorder(), req, &dst, 1024)
		if c.code == "" {
			if err != nil {
				t.Errorf("%s: %v", c.body, err)
			}
			continue
		}
		e := apperrors.From(err)
		if e.Code != c.code || e.Status != c.status {
			t.Errorf("%q: got %s %d want %s %d", c.body[:min(len(c.body), 20)], e.Code, e.Status, c.code, c.status)
		}
	}
}

func TestRecovererAndNotFound(t *testing.T) {
	h := Recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("boom secret") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	NotFoundHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/nope", nil))
	if rec.Code != 404 || decodeEnv(t, rec).Error.Code != apperrors.CodeRouteNotFound {
		t.Fatal("not found")
	}
	rec = httptest.NewRecorder()
	MethodNotAllowedHandler().ServeHTTP(rec, httptest.NewRequest("DELETE", "/", nil))
	if rec.Code != 405 {
		t.Fatal("405")
	}
}

func TestMaxBody(t *testing.T) {
	h := MaxBody(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			WriteError(w, r, apperrors.PayloadTooLarge())
		}
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 100))))
	if rec.Code != 413 {
		t.Fatalf("status %d", rec.Code)
	}
}

type staticTokens string

func (s staticTokens) Token(context.Context, string) (string, error) { return string(s), nil }

func TestClientPropagatesAndDecodes(t *testing.T) {
	var gotAuth, gotReq, gotTrace, gotIdem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotReq, gotTrace, gotIdem = r.Header.Get("Authorization"), r.Header.Get("X-Request-ID"), r.Header.Get("traceparent"), r.Header.Get("Idempotency-Key")
		if r.URL.Path == "/internal/v1/missing" {
			WriteError(w, r, apperrors.NotFound("MEDIA_NOT_FOUND", "Media not found."))
			return
		}
		WriteData(w, 200, map[string]string{"status": "READY"})
	}))
	defer srv.Close()
	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Audience: "media-document-service", Tokens: staticTokens("svc-token")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := requestcontext.With(context.Background(), requestcontext.Values{RequestID: "req-77", TraceID: "4bf92f3577b34da6a3ce929d0e0e4736"})
	var out struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := c.Do(ctx, Request{Method: "POST", Path: "/internal/v1/x", Body: map[string]int{"a": 1}, IdempotencyKey: "k1"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Status != "READY" || gotAuth != "Bearer svc-token" || gotReq != "req-77" || !strings.Contains(gotTrace, "4bf92f3577b34da6a3ce929d0e0e4736") || gotIdem != "k1" {
		t.Fatalf("propagation failed: %q %q %q %q %+v", gotAuth, gotReq, gotTrace, gotIdem, out)
	}
	err = c.Do(ctx, Request{Method: "GET", Path: "/internal/v1/missing"}, nil)
	var re *RemoteError
	if !errors.As(err, &re) || re.Status != 404 || re.Code != "MEDIA_NOT_FOUND" {
		t.Fatalf("remote error %v", err)
	}
	if ae := ToAppError(err); ae.Code != "MEDIA_NOT_FOUND" || ae.Status != 404 {
		t.Fatalf("mapping %v", ae)
	}
}

func TestClientRetriesOnlySafeOrIdempotent(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		WriteError(w, r, apperrors.Unavailable(""))
	}))
	defer srv.Close()
	c, _ := NewClient(ClientConfig{BaseURL: srv.URL, Audience: "x", BaseBackoff: time.Millisecond})
	ctx := context.Background()

	_ = c.Do(ctx, Request{Method: "GET", Path: "/"}, nil)
	if calls.Load() != 3 {
		t.Fatalf("GET should retry to 3 attempts, got %d", calls.Load())
	}
	calls.Store(0)
	err := c.Do(ctx, Request{Method: "POST", Path: "/"}, nil)
	if calls.Load() != 1 {
		t.Fatalf("unsafe POST without key must not retry, got %d", calls.Load())
	}
	if ToAppError(err).Code != apperrors.CodeServiceUnavailable {
		t.Fatal("5xx must map to SERVICE_UNAVAILABLE")
	}
	calls.Store(0)
	_ = c.Do(ctx, Request{Method: "POST", Path: "/", IdempotencyKey: "k"}, nil)
	if calls.Load() != 3 {
		t.Fatalf("POST with idempotency key should retry, got %d", calls.Load())
	}
}

func TestClientTimeoutUnsafeIsUnknownOutcome(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	c, _ := NewClient(ClientConfig{BaseURL: srv.URL, Audience: "x", Timeout: 50 * time.Millisecond})
	start := time.Now()
	err := c.Do(context.Background(), Request{Method: "POST", Path: "/"}, nil)
	if !errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("expected unknown outcome, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout not enforced")
	}
	if ToAppError(err).Outcome != apperrors.OutcomeUnknown {
		t.Fatal("outcome should be UNKNOWN")
	}
}

func TestClientContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, apperrors.Unavailable(""))
	}))
	defer srv.Close()
	c, _ := NewClient(ClientConfig{BaseURL: srv.URL, Audience: "x", BaseBackoff: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.Do(ctx, Request{Method: "GET", Path: "/"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("cancellation not honoured: %v", err)
	}
}

func TestClientDownstreamUnreachable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	c, _ := NewClient(ClientConfig{BaseURL: "http://" + addr, Audience: "x", BaseBackoff: time.Millisecond})
	err := c.Do(context.Background(), Request{Method: "GET", Path: "/"}, nil)
	if err == nil || ToAppError(err).Status != 503 {
		t.Fatalf("expected 503 mapping, got %v", err)
	}
}

func TestGracefulShutdownWaitsForInflight(t *testing.T) {
	started := make(chan struct{})
	var notReady atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(204)
	})
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	cfg := ServerConfig{Handler: handler, ShutdownTimeout: 5 * time.Second, BeforeShutdown: func() { notReady.Store(true) }, Logger: logging.Discard()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, NewServer(cfg), ln) }()

	respCh := make(chan int, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		if err != nil {
			respCh <- -1
			return
		}
		resp.Body.Close()
		respCh <- resp.StatusCode
	}()
	<-started
	cancel()
	if code := <-respCh; code != 204 {
		t.Fatalf("in-flight request was dropped: %d", code)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !notReady.Load() {
		t.Fatal("BeforeShutdown not called")
	}
	if _, err := http.Get("http://" + ln.Addr().String()); err == nil {
		t.Fatal("server still accepting after shutdown")
	}
}
