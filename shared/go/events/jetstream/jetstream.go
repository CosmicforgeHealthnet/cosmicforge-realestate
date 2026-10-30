// Package jetstream implements the events transport on NATS JetStream.
//
//   - Publish waits for the JetStream PubAck (durable storage) within a
//     timeout and sets Nats-Msg-Id = event_id so the stream de-duplicates
//     re-publication inside its duplicate window (e.g. outbox crash after
//     publish but before mark-published).
//   - Consumers are durable pull consumers with explicit ack, bounded
//     redelivery (MaxDeliver), back-off, and the delivery attempt exposed to
//     handlers.
//   - Delivery is at-least-once; JetStream de-duplication is an optimisation,
//     not a correctness guarantee — consumers stay idempotent.
package jetstream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
)

// Config configures the connection.
type Config struct {
	URL            string
	Name           string // client name (service)
	PublishTimeout time.Duration
	Logger         *slog.Logger
	// Credentials file / token are deployment specific; optional.
	CredsFile string
	Token     string
}

// Conn wraps a NATS connection and JetStream context.
type Conn struct {
	nc  *nats.Conn
	js  jetstream.JetStream
	cfg Config
	log *slog.Logger
}

// Connect dials NATS with unlimited reconnects (outages are survived by the
// outbox; publishes fail fast while disconnected).
func Connect(cfg Config) (*Conn, error) {
	if cfg.URL == "" {
		return nil, errors.New("jetstream: URL required")
	}
	if cfg.PublishTimeout <= 0 {
		cfg.PublishTimeout = 5 * time.Second
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	opts := []nats.Option{
		nats.Name(cfg.Name),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500 * time.Millisecond),
		nats.RetryOnFailedConnect(true),
		nats.Timeout(3 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Warn("nats disconnected", "error", err.Error())
			}
		}),
		nats.ReconnectHandler(func(*nats.Conn) { log.Info("nats reconnected") }),
	}
	if cfg.CredsFile != "" {
		opts = append(opts, nats.UserCredentials(cfg.CredsFile))
	}
	if cfg.Token != "" {
		opts = append(opts, nats.Token(cfg.Token))
	}
	nc, err := nats.Connect(cfg.URL, opts...)
	if err != nil {
		return nil, err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	return &Conn{nc: nc, js: js, cfg: cfg, log: log}, nil
}

// JetStream exposes the underlying context (stream management, tests).
func (c *Conn) JetStream() jetstream.JetStream { return c.js }

// Ready is a readiness check.
func (c *Conn) Ready(ctx context.Context) error {
	if !c.nc.IsConnected() {
		return errors.New("nats not connected")
	}
	_, err := c.js.AccountInfo(ctx)
	return err
}

// Close drains and closes the connection gracefully.
func (c *Conn) Close() {
	if err := c.nc.Drain(); err != nil {
		c.nc.Close()
	}
}

// StreamConfig describes a stream owned by a producing service.
type StreamConfig struct {
	Name            string   // e.g. REALESTATE_VERIFICATION
	Subjects        []string // e.g. realestate.verification.>
	MaxAge          time.Duration
	DuplicateWindow time.Duration
	Replicas        int
}

// EnsureStream creates or updates a stream idempotently.
func (c *Conn) EnsureStream(ctx context.Context, sc StreamConfig) error {
	if sc.MaxAge <= 0 {
		sc.MaxAge = 7 * 24 * time.Hour
	}
	if sc.DuplicateWindow <= 0 {
		sc.DuplicateWindow = 10 * time.Minute
	}
	if sc.Replicas <= 0 {
		sc.Replicas = 1
	}
	_, err := c.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       sc.Name,
		Subjects:   sc.Subjects,
		Storage:    jetstream.FileStorage,
		Retention:  jetstream.LimitsPolicy,
		MaxAge:     sc.MaxAge,
		Duplicates: sc.DuplicateWindow,
		Replicas:   sc.Replicas,
	})
	return err
}

// Publish implements events.Publisher.
func (c *Conn) Publish(ctx context.Context, subject string, e events.Envelope) (events.PublishAck, error) {
	ctx, span := observability.StartSpan(ctx, "jetstream", "NATS publish")
	defer span.End()
	data, err := e.Marshal()
	if err != nil {
		return events.PublishAck{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.PublishTimeout)
	defer cancel()
	msg := &nats.Msg{Subject: subject, Data: data, Header: nats.Header{}}
	msg.Header.Set("Content-Type", "application/json")
	msg.Header.Set("Event-Type", e.EventType)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(msg.Header))
	if e.TraceID != "" {
		msg.Header.Set("Trace-Id", e.TraceID)
	}
	ack, err := c.js.PublishMsg(ctx, msg, jetstream.WithMsgID(e.EventID.String()))
	if err != nil {
		span.SetStatus(codes.Error, "NATS publish failure")
		observability.NewCounter("jetstream", "event_publish_failures_total", "NATS publish failures").Add(ctx, 1)
		return events.PublishAck{}, err
	}
	return events.PublishAck{Stream: ack.Stream, Sequence: ack.Sequence, Duplicate: ack.Duplicate}, nil
}

// ConsumerConfig configures a durable consumer.
type ConsumerConfig struct {
	Stream         string
	Durable        string // stable consumer name, e.g. "notification-service--verification"
	FilterSubjects []string
	AckWait        time.Duration
	MaxDeliver     int
	Backoff        []time.Duration
	HandlerTimeout time.Duration
	// OnFailure retains terminal delivery evidence before termination. Raw
	// payloads stay in the original stream, never in logs or review metadata.
	OnFailure func(context.Context, Failure) error
}

type Failure struct {
	Consumer string
	Stream   string
	Sequence uint64
	Attempt  uint64
	EventID  string
	Digest   string
	Class    string
}

// Consumer is a running consumer.
type Consumer struct {
	cc   jetstream.ConsumeContext
	wg   sync.WaitGroup
	once sync.Once
}

// Stop stops fetching and waits for in-flight handlers.
func (c *Consumer) Stop() {
	c.once.Do(func() {
		c.cc.Stop()
		c.wg.Wait()
	})
}

// Consume starts a durable consumer. Messages that fail to decode are
// terminated (poison); handler errors are Nak'd with delay until MaxDeliver,
// after which JetStream stops redelivering (and the final failure is logged).
func (c *Conn) Consume(ctx context.Context, cfg ConsumerConfig, h events.Handler) (*Consumer, error) {
	if cfg.AckWait <= 0 {
		cfg.AckWait = 30 * time.Second
	}
	if cfg.MaxDeliver <= 0 {
		cfg.MaxDeliver = 10
	}
	if cfg.HandlerTimeout <= 0 || cfg.HandlerTimeout > cfg.AckWait {
		cfg.HandlerTimeout = cfg.AckWait - time.Second
		if cfg.HandlerTimeout <= 0 {
			cfg.HandlerTimeout = cfg.AckWait - cfg.AckWait/2
		}
	}
	jc := jetstream.ConsumerConfig{
		Durable:        cfg.Durable,
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        cfg.AckWait,
		MaxDeliver:     cfg.MaxDeliver,
		BackOff:        cfg.Backoff,
		DeliverPolicy:  jetstream.DeliverAllPolicy,
		FilterSubjects: cfg.FilterSubjects,
	}
	cons, err := c.js.CreateOrUpdateConsumer(ctx, cfg.Stream, jc)
	if err != nil {
		return nil, err
	}
	out := &Consumer{}
	cc, err := cons.Consume(func(m jetstream.Msg) {
		out.wg.Add(1)
		defer out.wg.Done()
		c.handle(m, cfg, h)
	})
	if err != nil {
		return nil, err
	}
	out.cc = cc
	return out, nil
}

func (c *Conn) handle(m jetstream.Msg, cfg ConsumerConfig, h events.Handler) {
	meta, err := m.Metadata()
	attempt := uint64(1)
	if err == nil {
		attempt = meta.NumDelivered
	}
	env, err := events.Unmarshal(m.Data())
	ctx, cancel := context.WithTimeout(context.Background(), cfg.HandlerTimeout)
	defer cancel()
	ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(m.Headers()))
	ctx, span := observability.StartSpan(ctx, "jetstream", "NATS consume")
	defer span.End()
	terminal := func(class string) {
		f := Failure{Consumer: cfg.Durable, Attempt: attempt, Digest: security.SHA256Hex(m.Data()), Class: class}
		if meta != nil {
			f.Stream, f.Sequence = meta.Stream, meta.Sequence.Stream
		}
		if !env.EventID.IsZero() {
			f.EventID = env.EventID.String()
		}
		span.SetStatus(codes.Error, "Event requires review")
		if cfg.OnFailure != nil {
			fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer fcancel()
			if ferr := cfg.OnFailure(fctx, f); ferr != nil {
				c.log.Error("event review persistence failed; original stream replay required", "consumer", cfg.Durable, "stream", f.Stream, "sequence", f.Sequence)
				observability.NewCounter("jetstream", "event_review_persistence_failures_total", "Review persistence failures").Add(ctx, 1)
				_ = m.NakWithDelay(30 * time.Second)
				return
			}
		}
		observability.NewCounter("jetstream", "event_terminal_failures_total", "Terminal event failures").Add(ctx, 1)
		_ = m.Term()
	}
	if err != nil {
		c.log.Error("poison event requires review", "consumer", cfg.Durable, "error", err.Error())
		terminal("MALFORMED")
		return
	}
	ctx = requestcontext.With(ctx, requestcontext.Values{RequestID: env.Metadata["request_id"], TraceID: env.TraceID})
	err = h(ctx, events.Delivery{Envelope: env, Subject: m.Subject(), Attempt: attempt})
	switch {
	case err == nil:
		if aerr := m.DoubleAck(ctx); aerr != nil {
			c.log.Warn("ack failed; event will be redelivered", "event_id", env.EventID.String(), "error", aerr.Error())
		}
	case events.IsPermanent(err):
		c.log.Error("event permanently rejected", "consumer", cfg.Durable, "event_id", env.EventID.String(),
			"event_type", env.EventType, "error", err.Error())
		terminal("PERMANENT")
	default:
		if attempt >= uint64(cfg.MaxDeliver) {
			c.log.Error("event exhausted deliveries", "consumer", cfg.Durable, "event_id", env.EventID.String(),
				"attempt", attempt, "error", err.Error())
			terminal("EXHAUSTED")
			return
		}
		delay := time.Duration(attempt) * time.Second
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		_ = m.NakWithDelay(delay)
	}
}

// StreamName builds a stream name for a producer domain, e.g. "verification"
// -> "REALESTATE_VERIFICATION".
func StreamName(prefix, area string) string {
	return fmt.Sprintf("%s_%s", upper(prefix), upper(area))
}

func upper(s string) string {
	b := []byte(s)
	for i, ch := range b {
		switch {
		case ch >= 'a' && ch <= 'z':
			b[i] = ch - 32
		case ch == '-' || ch == '.':
			b[i] = '_'
		}
	}
	return string(b)
}
