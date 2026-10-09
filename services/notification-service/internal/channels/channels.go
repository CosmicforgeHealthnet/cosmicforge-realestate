// Package channels defines provider-neutral interfaces for PUSH, EMAIL and
// SMS providers and their adapters. Use cases see only Sender, Message,
// Result and classified errors — never a concrete provider type or provider
// response structure. Adding a provider means writing an adapter that
// implements PushProvider, EmailProvider or SMSProvider and registering it
// in the application's provider registry; notification logic is unchanged.
//
// Provider status: FCM is the selected push provider. No production email or
// SMS provider has been selected; SMTP and the Twilio-compatible adapter are
// optional adapters only. Capture adapters serve development and tests.
package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Message is a rendered message for one destination.
type Message struct {
	DeliveryID  ids.ID // idempotency reference where the provider supports one
	Destination string // FCM token, email address or E.164 number
	Subject     string // email subject / push title
	Body        string // plain text body
	HTML        string // email HTML body (optional)
	Data        map[string]string
}

// Result is a provider acceptance. Acceptance is not user delivery.
type Result struct {
	ProviderMessageID string
	HTTPStatus        int
	ProviderCode      string
}

// Kind classifies failures for retry decisions.
type Kind int

const (
	// Transient failures are retried with backoff (bounded).
	Transient Kind = iota + 1
	// Permanent failures are final (bad request, unsupported, auth config).
	Permanent
	// InvalidDestination is final and invalidates the destination (e.g. an
	// unregistered FCM token).
	InvalidDestination
)

// Error is a classified provider failure (safe to log: no content/secrets).
type Error struct {
	Kind       Kind
	Code       string
	HTTPStatus int
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string { return fmt.Sprintf("%s (%d): %v", e.Code, e.HTTPStatus, e.Err) }
func (e *Error) Unwrap() error { return e.Err }

// Classify returns the failure details; unclassified errors are transient.
func Classify(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Kind: Transient, Code: "PROVIDER_ERROR", Err: err}
}

// Sender delivers messages on one channel. It is the only provider-facing
// type the use cases depend on.
type Sender interface {
	Name() string
	Channel() domain.Channel
	Send(ctx context.Context, m Message) (Result, error)
}

// PushProvider sends one push message to one device token.
type PushProvider interface {
	Name() string
	SendPush(ctx context.Context, m Message) (Result, error)
}

// EmailProvider sends one email (Subject, Body as text, optional HTML).
type EmailProvider interface {
	Name() string
	SendEmail(ctx context.Context, m Message) (Result, error)
}

// SMSProvider sends one text message to one E.164 number.
type SMSProvider interface {
	Name() string
	SendSMS(ctx context.Context, m Message) (Result, error)
}

type pushSender struct{ p PushProvider }
type emailSender struct{ p EmailProvider }
type smsSender struct{ p SMSProvider }

// Push adapts a PushProvider to the PUSH channel.
func Push(p PushProvider) Sender { return pushSender{p} }

// Email adapts an EmailProvider to the EMAIL channel.
func Email(p EmailProvider) Sender { return emailSender{p} }

// SMS adapts an SMSProvider to the SMS channel.
func SMS(p SMSProvider) Sender { return smsSender{p} }

func (s pushSender) Name() string            { return s.p.Name() }
func (s pushSender) Channel() domain.Channel { return domain.ChannelPush }
func (s pushSender) Send(ctx context.Context, m Message) (Result, error) {
	return s.p.SendPush(ctx, m)
}
func (s emailSender) Name() string            { return s.p.Name() }
func (s emailSender) Channel() domain.Channel { return domain.ChannelEmail }
func (s emailSender) Send(ctx context.Context, m Message) (Result, error) {
	return s.p.SendEmail(ctx, m)
}
func (s smsSender) Name() string            { return s.p.Name() }
func (s smsSender) Channel() domain.Channel { return domain.ChannelSMS }
func (s smsSender) Send(ctx context.Context, m Message) (Result, error) {
	return s.p.SendSMS(ctx, m)
}

// Registry maps channels to configured senders.
type Registry map[domain.Channel]Sender

// Capture is the development/test provider for any channel: it records
// messages in memory (needing no credentials or network) and can be told to
// fail. Configuration refuses it in production.
type Capture struct {
	ChannelName domain.Channel
	// File, when set, also appends each message as a JSON line (local and
	// end-to-end test tooling only; the dev provider is refused in production).
	File     string
	mu       sync.Mutex
	sent     []Message
	failures []error
}

// NewCapture returns a capture sender for ch.
func NewCapture(ch domain.Channel) *Capture { return &Capture{ChannelName: ch} }

func (c *Capture) Name() string            { return "capture-" + strings.ToLower(string(c.ChannelName)) }
func (c *Capture) Channel() domain.Channel { return c.ChannelName }

// SendPush implements PushProvider.
func (c *Capture) SendPush(ctx context.Context, m Message) (Result, error) { return c.Send(ctx, m) }

// SendEmail implements EmailProvider.
func (c *Capture) SendEmail(ctx context.Context, m Message) (Result, error) { return c.Send(ctx, m) }

// SendSMS implements SMSProvider.
func (c *Capture) SendSMS(ctx context.Context, m Message) (Result, error) { return c.Send(ctx, m) }

// FailNext queues errors returned by the next Send calls.
func (c *Capture) FailNext(errs ...error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures = append(c.failures, errs...)
}

func (c *Capture) Send(ctx context.Context, m Message) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.failures) > 0 {
		err := c.failures[0]
		c.failures = c.failures[1:]
		return Result{}, err
	}
	c.sent = append(c.sent, m)
	if c.File != "" {
		line, _ := json.Marshal(map[string]string{"channel": string(c.ChannelName), "destination": m.Destination, "subject": m.Subject, "body": m.Body})
		f, err := os.OpenFile(c.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return Result{}, err
		}
		_, err = f.Write(append(line, '\n'))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return Result{}, err
		}
	}
	return Result{ProviderMessageID: "cap-" + m.DeliveryID.String(), HTTPStatus: 200}, nil
}

// Sent returns captured messages.
func (c *Capture) Sent() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Message(nil), c.sent...)
}

// Compile-time interface checks.
var (
	_ PushProvider  = (*FCM)(nil)
	_ EmailProvider = (*SMTP)(nil)
	_ SMSProvider   = (*Twilio)(nil)
	_ PushProvider  = (*Capture)(nil)
	_ EmailProvider = (*Capture)(nil)
	_ SMSProvider   = (*Capture)(nil)
)
