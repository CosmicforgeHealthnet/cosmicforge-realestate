package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// TokenSource supplies a service JWT for a destination audience. The token
// may carry delegated actor context derived from ctx (see serviceauth.Signer).
type TokenSource interface {
	Token(ctx context.Context, audience string) (string, error)
}

// ClientConfig configures an internal service client.
type ClientConfig struct {
	// BaseURL of the destination service, e.g. http://verification-service:8080.
	BaseURL string
	// Audience is the destination service name used as the JWT audience.
	Audience string
	Tokens   TokenSource
	// Timeout bounds each attempt (connect + headers + body). Default 5s.
	Timeout time.Duration
	// MaxAttempts bounds retries of safe/idempotent requests. Default 3.
	MaxAttempts int
	// BaseBackoff is the first retry delay. Default 100ms (exponential, jittered, capped at 2s).
	BaseBackoff time.Duration
	// MaxResponseBytes bounds response bodies. Default 4MiB.
	MaxResponseBytes int64
	Transport        http.RoundTripper
}

// Client is the standard internal HTTP client:
//   - strict per-attempt timeouts and context cancellation;
//   - request ID and W3C trace propagation;
//   - service JWT authentication;
//   - platform error-envelope decoding into *RemoteError;
//   - bounded, jittered retry ONLY for safe methods or requests carrying an
//     Idempotency-Key; unsafe commands without a key are never retried.
type Client struct {
	cfg  ClientConfig
	base *url.URL
	http *http.Client
}

// NewClient validates configuration.
func NewClient(cfg ClientConfig) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("httpx: invalid base URL")
	}
	if cfg.Audience == "" {
		return nil, errors.New("httpx: audience required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.MaxAttempts > 5 {
		cfg.MaxAttempts = 5
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = 100 * time.Millisecond
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = 4 << 20
	}
	tr := cfg.Transport
	if tr == nil {
		tr = &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   3 * time.Second,
			ResponseHeaderTimeout: cfg.Timeout,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
		}
	}
	return &Client{cfg: cfg, base: u, http: &http.Client{
		Transport: observability.HTTPTransport(tr),
		// Internal APIs never redirect; following one could leak the token.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// Request describes one internal call.
type Request struct {
	Method         string
	Path           string // e.g. /internal/v1/media/{id}
	Query          url.Values
	Body           any
	IdempotencyKey string
}

// RemoteError is a non-2xx response decoded from the platform envelope.
type RemoteError struct {
	Service   string
	Status    int
	Code      string
	Message   string
	RequestID string
	Details   []apperrors.Detail
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("%s responded %d %s: %s", e.Service, e.Status, e.Code, e.Message)
}

// AppError maps a downstream failure to a local platform error. 4xx responses
// keep their code (downstream messages are client-safe by standard); 5xx and
// transport failures become SERVICE_UNAVAILABLE.
func (e *RemoteError) AppError() *apperrors.Error {
	if e.Status >= 400 && e.Status < 500 && apperrors.ValidCode(e.Code) {
		return apperrors.New(e.Status, e.Code, e.Message).WithDetails(e.Details...).WithCause(e)
	}
	return apperrors.Unavailable("A dependent service is unavailable.").WithCause(e)
}

// ErrUnknownOutcome wraps failures where an unsafe request may or may not
// have been applied downstream (timeout after sending). Callers must read
// back state before repeating the side effect.
var ErrUnknownOutcome = errors.New("httpx: downstream outcome unknown")

// ToAppError converts any client error into a platform error.
func ToAppError(err error) *apperrors.Error {
	var re *RemoteError
	if errors.As(err, &re) {
		return re.AppError()
	}
	if errors.Is(err, ErrUnknownOutcome) {
		return apperrors.Unavailable("A dependent service did not respond.").WithOutcome(apperrors.OutcomeUnknown).WithCause(err)
	}
	return apperrors.Unavailable("A dependent service is unavailable.").WithCause(err)
}

func retryable(method, idemKey string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return idemKey != ""
}

// Do executes req and decodes a JSON success body into out (if non-nil).
// out receives the raw response body unmarshalled as-is (callers usually pass
// a struct with a Data field to unwrap {"data": ...}).
func (c *Client) Do(ctx context.Context, req Request, out any) error {
	var payload []byte
	if req.Body != nil {
		b, err := json.Marshal(req.Body)
		if err != nil {
			return err
		}
		payload = b
	}
	canRetry := retryable(req.Method, req.IdempotencyKey)
	attempts := 1
	if canRetry {
		attempts = c.cfg.MaxAttempts
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		status, body, retryAfter, err := c.once(ctx, req, payload)
		if err == nil && status < 300 {
			if out != nil && len(body) > 0 {
				if err := json.Unmarshal(body, out); err != nil {
					return fmt.Errorf("httpx: decoding %s response: %w", c.cfg.Audience, err)
				}
			}
			return nil
		}
		if err == nil {
			lastErr = c.remoteError(status, body)
			if !canRetry || !(status == 429 || status == 502 || status == 503 || status == 504) {
				return lastErr
			}
		} else {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !canRetry {
				// An unsafe request may have reached the server.
				return fmt.Errorf("%w: %s %s: %v", ErrUnknownOutcome, req.Method, req.Path, err)
			}
			lastErr = err
		}
		if attempt == attempts {
			break
		}
		delay := c.backoff(attempt, retryAfter)
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	return lastErr
}

func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	d := c.cfg.BaseBackoff << (attempt - 1)
	if d > 2*time.Second {
		d = 2 * time.Second
	}
	d = d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
	if retryAfter > d && retryAfter <= 5*time.Second {
		d = retryAfter
	}
	return d
}

func (c *Client) once(ctx context.Context, req Request, payload []byte) (int, []byte, time.Duration, error) {
	actx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + req.Path
	if len(req.Query) > 0 {
		u.RawQuery = req.Query.Encode()
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	hr, err := http.NewRequestWithContext(actx, req.Method, u.String(), body)
	if err != nil {
		return 0, nil, 0, err
	}
	hr.Header.Set("Accept", "application/json")
	if payload != nil {
		hr.Header.Set("Content-Type", ContentTypeJSON)
	}
	if req.IdempotencyKey != "" {
		hr.Header.Set(requestcontext.HeaderIdempotencyKey, req.IdempotencyKey)
	}
	requestcontext.Inject(ctx, hr.Header)
	if c.cfg.Tokens != nil {
		tok, err := c.cfg.Tokens.Token(ctx, c.cfg.Audience)
		if err != nil {
			return 0, nil, 0, fmt.Errorf("httpx: service token: %w", err)
		}
		hr.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.http.Do(hr)
	if err != nil {
		return 0, nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxResponseBytes+1))
	if err != nil {
		return 0, nil, 0, err
	}
	if int64(len(b)) > c.cfg.MaxResponseBytes {
		return 0, nil, 0, errors.New("httpx: response too large")
	}
	var ra time.Duration
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			ra = time.Duration(n) * time.Second
		}
	}
	return resp.StatusCode, b, ra, nil
}

func (c *Client) remoteError(status int, body []byte) *RemoteError {
	re := &RemoteError{Service: c.cfg.Audience, Status: status}
	var env apperrors.Envelope
	if err := json.Unmarshal(body, &env); err == nil && env.Error.Code != "" {
		re.Code, re.Message, re.RequestID, re.Details = env.Error.Code, env.Error.Message, env.Error.RequestID, env.Error.Details
	} else {
		re.Code, re.Message = "UPSTREAM_ERROR", http.StatusText(status)
	}
	return re
}
