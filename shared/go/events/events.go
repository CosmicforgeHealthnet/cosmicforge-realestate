// Package events defines the canonical platform event envelope
// (EVENT_CATALOG.md) and the transport-neutral publisher/consumer contracts.
//
//	{
//	  "event_id": "UUIDv7", "event_type": "listing.published.v1",
//	  "producer": "individual-owner-service",
//	  "aggregate_type": "listing", "aggregate_id": "UUIDv7",
//	  "occurred_at": "RFC3339", "trace_id": "...",
//	  "actor": {"domain": "individual-owner", "id": "UUIDv7"},
//	  "metadata": {"request_id": "..."},
//	  "payload": {}
//	}
//
// Delivery is at-least-once. Consumers MUST be idempotent (see
// MarkProcessed) and must ignore unknown optional payload fields. No code may
// assume exactly-once delivery.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// Actor identifies the user on whose behalf the change happened.
type Actor struct {
	Domain string `json:"domain"`
	ID     ids.ID `json:"id"`
}

// Envelope is the canonical event.
type Envelope struct {
	EventID       ids.ID            `json:"event_id"`
	EventType     string            `json:"event_type"`
	Producer      string            `json:"producer"`
	AggregateType string            `json:"aggregate_type"`
	AggregateID   ids.ID            `json:"aggregate_id"`
	OccurredAt    time.Time         `json:"occurred_at"`
	TraceID       string            `json:"trace_id,omitempty"`
	Actor         *Actor            `json:"actor,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Payload       json.RawMessage   `json:"payload"`
}

var (
	eventTypePattern     = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+\.v[1-9][0-9]*$`)
	aggregateTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// ErrInvalid is wrapped by all validation failures.
var ErrInvalid = errors.New("events: invalid envelope")

// ValidEventType reports whether name is a versioned event name such as
// "verification.verified.v1".
func ValidEventType(name string) bool { return eventTypePattern.MatchString(name) }

// Version returns the numeric version suffix of an event type.
func Version(eventType string) int {
	i := strings.LastIndex(eventType, ".v")
	if i < 0 {
		return 0
	}
	var v int
	_, _ = fmt.Sscanf(eventType[i+2:], "%d", &v)
	return v
}

// New builds an envelope stamped with a fresh UUIDv7, the current UTC time
// (microsecond precision to round-trip through PostgreSQL) and the trace,
// request and actor context from ctx.
func New(ctx context.Context, producer platform.Service, eventType, aggregateType string, aggregateID ids.ID, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("%w: payload: %v", ErrInvalid, err)
	}
	rc := requestcontext.From(ctx)
	e := Envelope{
		EventID:       ids.New(),
		EventType:     eventType,
		Producer:      string(producer),
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		OccurredAt:    time.Now().UTC().Truncate(time.Microsecond),
		TraceID:       rc.TraceID,
		Payload:       raw,
	}
	if !rc.Actor.IsZero() {
		e.Actor = &Actor{Domain: rc.Actor.Domain, ID: rc.Actor.ID}
	}
	md := map[string]string{}
	if rc.RequestID != "" {
		md["request_id"] = rc.RequestID
	}
	if rc.CallingService != "" {
		md["calling_service"] = rc.CallingService
	}
	if len(md) > 0 {
		e.Metadata = md
	}
	return e, e.Validate()
}

// Validate checks envelope invariants.
func (e Envelope) Validate() error {
	switch {
	case e.EventID.ValidateV7() != nil:
		return fmt.Errorf("%w: event_id must be UUIDv7", ErrInvalid)
	case !ValidEventType(e.EventType):
		return fmt.Errorf("%w: event_type %q must be versioned (name.action.vN)", ErrInvalid, e.EventType)
	case !platform.KnownService(e.Producer):
		return fmt.Errorf("%w: unknown producer %q", ErrInvalid, e.Producer)
	case !aggregateTypePattern.MatchString(e.AggregateType):
		return fmt.Errorf("%w: aggregate_type", ErrInvalid)
	case e.AggregateID.ValidateV7() != nil:
		return fmt.Errorf("%w: aggregate_id must be UUIDv7", ErrInvalid)
	case e.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurred_at", ErrInvalid)
	}
	if e.Actor != nil && (!platform.KnownDomain(e.Actor.Domain) || e.Actor.ID.ValidateV7() != nil) {
		return fmt.Errorf("%w: actor", ErrInvalid)
	}
	p := bytes.TrimSpace(e.Payload)
	if len(p) == 0 || p[0] != '{' || !json.Valid(p) {
		return fmt.Errorf("%w: payload must be a JSON object", ErrInvalid)
	}
	return nil
}

// Marshal validates and encodes the envelope.
func (e Envelope) Marshal() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(e)
}

// Unmarshal decodes and validates an envelope. Unknown envelope fields are
// ignored for forward compatibility.
func Unmarshal(b []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return Envelope{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return e, e.Validate()
}

// DecodePayload decodes the payload into v (unknown fields are ignored).
func (e Envelope) DecodePayload(v any) error { return json.Unmarshal(e.Payload, v) }

// Subject maps an event type to a transport subject: "<prefix>.<event_type>",
// e.g. "realestate.verification.verified.v1".
func Subject(prefix, eventType string) string {
	if prefix == "" {
		return eventType
	}
	return prefix + "." + eventType
}

// DefaultSubjectPrefix is the platform subject namespace.
const DefaultSubjectPrefix = "realestate"

// PublishAck is the broker acknowledgement.
type PublishAck struct {
	Stream    string
	Sequence  uint64
	Duplicate bool // broker de-duplicated by event_id
}

// Publisher publishes an envelope and returns only after the broker has
// durably acknowledged it.
type Publisher interface {
	Publish(ctx context.Context, subject string, e Envelope) (PublishAck, error)
}

// Delivery is one (possibly repeated) delivery of an event.
type Delivery struct {
	Envelope Envelope
	Subject  string
	// Attempt is the 1-based delivery count reported by the broker.
	Attempt uint64
}

// Handler processes a delivery. Returning nil acknowledges it; returning an
// error requests redelivery (bounded by the consumer's max deliveries);
// returning a Permanent error terminates it without redelivery.
type Handler func(ctx context.Context, d Delivery) error

type permanentError struct{ err error }

func (p permanentError) Error() string { return "permanent: " + p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent marks an error as non-retryable (poison event).
func Permanent(err error) error { return permanentError{err: err} }

// IsPermanent reports whether err was marked Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// ProcessedSchemaSQL is the consumer de-duplication table definition; each
// consuming service includes it in its own migrations.
const ProcessedSchemaSQL = `
CREATE TABLE processed_events (
    consumer      text        NOT NULL,
    event_id      uuid        NOT NULL,
    event_type    text        NOT NULL,
    processed_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, event_id)
);
`

// MarkProcessed records that consumer handled eventID, inside the consumer's
// business transaction. It returns false when the event was already
// processed, in which case the caller must skip its side effects.
func MarkProcessed(ctx context.Context, tx database.Querier, consumer string, e Envelope) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO processed_events (consumer, event_id, event_type) VALUES ($1,$2,$3)
		ON CONFLICT (consumer, event_id) DO NOTHING`, consumer, e.EventID, e.EventType)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		observability.NewCounter("events", "event_duplicates_total", "Duplicate delivery observations").Add(ctx, 1)
	}
	return tag.RowsAffected() == 1, nil
}
