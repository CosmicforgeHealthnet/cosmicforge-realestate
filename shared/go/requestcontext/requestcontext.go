// Package requestcontext carries the canonical per-request fields
// (BACKEND_FOUNDATION_BLUEPRINT.md §10) through context.Context and across
// downstream HTTP calls.
//
// Canonical fields: request_id, trace_id, calling_service, actor_domain,
// actor_id, organization_id, idempotency_key.
//
// Trust rules: request_id, traceparent and Idempotency-Key are read from
// inbound headers. calling_service, actor and organization are NEVER read from
// headers here; they are set only by authentication middleware after a user
// JWT or service JWT has been verified.
package requestcontext

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Header names.
const (
	HeaderRequestID      = "X-Request-ID"
	HeaderIdempotencyKey = "Idempotency-Key"
	HeaderTraceParent    = "traceparent"
)

// Actor is a user-domain actor reference (domain + UUIDv7).
type Actor struct {
	Domain string
	ID     ids.ID
}

// IsZero reports whether no actor is present.
func (a Actor) IsZero() bool { return a.Domain == "" && a.ID.IsZero() }

// Values are the canonical request fields. Only applicable fields are set.
type Values struct {
	RequestID      string
	TraceID        string
	CallingService string
	Actor          Actor
	OrganizationID ids.ID
	IdempotencyKey string
}

type ctxKey struct{}

// From returns the values stored in ctx. TraceID falls back to the active span.
func From(ctx context.Context) Values {
	v, _ := ctx.Value(ctxKey{}).(Values)
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		v.TraceID = sc.TraceID().String()
	}
	return v
}

// With stores v in ctx, replacing any previous values.
func With(ctx context.Context, v Values) context.Context {
	return context.WithValue(ctx, ctxKey{}, v)
}

func update(ctx context.Context, fn func(*Values)) context.Context {
	v, _ := ctx.Value(ctxKey{}).(Values)
	fn(&v)
	return With(ctx, v)
}

// WithActor records an authenticated user actor. Call only after verifying credentials.
func WithActor(ctx context.Context, a Actor) context.Context {
	return update(ctx, func(v *Values) { v.Actor = a })
}

// WithCallingService records an authenticated calling service.
func WithCallingService(ctx context.Context, service string) context.Context {
	return update(ctx, func(v *Values) { v.CallingService = service })
}

// WithOrganization records the authenticated organization context.
func WithOrganization(ctx context.Context, org ids.ID) context.Context {
	return update(ctx, func(v *Values) { v.OrganizationID = org })
}

// WithRequestID sets the request ID (used by workers to correlate jobs).
func WithRequestID(ctx context.Context, id string) context.Context {
	return update(ctx, func(v *Values) { v.RequestID = id })
}

// WithIdempotencyKey sets the idempotency key.
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	return update(ctx, func(v *Values) { v.IdempotencyKey = key })
}

// RequestID returns the request ID or "".
func RequestID(ctx context.Context) string { return From(ctx).RequestID }

// TraceID returns the trace ID or "".
func TraceID(ctx context.Context) string { return From(ctx).TraceID }

// ActorFrom returns the authenticated actor, if any.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a := From(ctx).Actor
	return a, !a.IsZero()
}

var (
	requestIDPattern      = regexp.MustCompile(`^[A-Za-z0-9._:\-]{1,128}$`)
	idempotencyKeyPattern = regexp.MustCompile(`^[\x21-\x7E]{1,255}$`)
)

// ValidRequestID reports whether a client-supplied request ID is acceptable.
func ValidRequestID(s string) bool { return requestIDPattern.MatchString(s) }

// ValidIdempotencyKey reports whether an Idempotency-Key is acceptable
// (1–255 visible ASCII characters).
func ValidIdempotencyKey(s string) bool { return idempotencyKeyPattern.MatchString(s) }

// NewRequestID returns a fresh UUIDv7 request ID.
func NewRequestID() string { return ids.New().String() }

// Middleware populates request_id, trace_id and idempotency_key.
//
// An inbound X-Request-ID is honoured when well-formed, otherwise replaced.
// The trace ID comes from the active OpenTelemetry span (install the
// observability middleware first) or the inbound traceparent; when neither
// exists a random trace ID is generated so every request has one. The request
// ID is echoed on the response.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		reqID := r.Header.Get(HeaderRequestID)
		if !ValidRequestID(reqID) {
			reqID = NewRequestID()
		}
		v := Values{RequestID: reqID}

		sc := trace.SpanContextFromContext(ctx)
		if !sc.HasTraceID() {
			ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(r.Header))
			sc = trace.SpanContextFromContext(ctx)
		}
		if !sc.HasTraceID() {
			// Fall back to standard W3C extraction in case the global
			// propagator was not configured.
			ctx = propagation.TraceContext{}.Extract(ctx, propagation.HeaderCarrier(r.Header))
			sc = trace.SpanContextFromContext(ctx)
		}
		if sc.HasTraceID() {
			v.TraceID = sc.TraceID().String()
		} else {
			v.TraceID = randomTraceID()
		}

		if key := r.Header.Get(HeaderIdempotencyKey); key != "" && ValidIdempotencyKey(key) {
			v.IdempotencyKey = key
		}
		w.Header().Set(HeaderRequestID, reqID)
		next.ServeHTTP(w, r.WithContext(With(ctx, v)))
	})
}

// fallbackTraceKey stores a generated trace ID when no span exists.
func randomTraceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Inject propagates request_id and W3C trace context to an outbound request.
// Identity (calling service/actor) is never propagated as plain headers; it
// travels inside a signed service JWT (see serviceauth).
func Inject(ctx context.Context, h http.Header) {
	v := From(ctx)
	if v.RequestID != "" {
		h.Set(HeaderRequestID, v.RequestID)
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(h))
	if h.Get(HeaderTraceParent) == "" {
		propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(h))
	}
	if h.Get(HeaderTraceParent) == "" && len(v.TraceID) == 32 {
		// No span exists (e.g. tracing disabled); still propagate the trace ID
		// with a fresh parent span ID so downstream logs correlate.
		var span [8]byte
		_, _ = rand.Read(span[:])
		h.Set(HeaderTraceParent, "00-"+v.TraceID+"-"+hex.EncodeToString(span[:])+"-00")
	}
}

// Detach returns a background context that keeps the request values (and the
// trace span context) but not the parent's cancellation. Use it for work that
// must finish after the client disconnects, such as completing a database
// transaction already in flight.
func Detach(ctx context.Context) context.Context {
	out := With(context.Background(), From(ctx))
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		out = trace.ContextWithSpanContext(out, sc)
	}
	return out
}

// Job returns a context for background work (workers, schedulers) with a
// fresh request ID and a trace ID taken from the active span, or generated
// when none exists, so audit records and logs always correlate.
func Job(ctx context.Context, name string) context.Context {
	v := From(ctx)
	v.RequestID = name + "-" + ids.New().String()
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		v.TraceID = sc.TraceID().String()
	} else {
		v.TraceID = randomTraceID()
	}
	return With(ctx, v)
}
