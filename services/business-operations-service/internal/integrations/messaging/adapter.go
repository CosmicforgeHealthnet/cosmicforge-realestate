// Package messaging defines the typed provider adapter for external channel
// conversations (Marketplace, OTA and direct web inquiry channels). Adapters
// are registered explicitly; no provider is activated by default. Message
// content is returned only when the provider permits it and is never persisted
// beyond a bounded, expiring preview.
package messaging

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrUnsupported means the provider or capability is not enabled.
	ErrUnsupported = errors.New("messaging: provider capability unsupported")
	// ErrRejected is a definite provider refusal; retrying cannot succeed.
	ErrRejected = errors.New("messaging: provider rejected request")
	// ErrNotFound is a definite readback answer that no message exists.
	ErrNotFound = errors.New("messaging: message not found at provider")
)

// MessagingCapabilities are declared per provider adapter and intersected with
// the connection's configured capabilities.
type MessagingCapabilities struct {
	Reply       bool `json:"reply"`
	ReadState   bool `json:"read_state"`
	Preview     bool `json:"preview"`
	Attachments bool `json:"attachments"`
	Poll        bool `json:"poll"`
}

// MessagingSendCommand is an idempotent outbound reply. ClientMessageID is the
// Business Operations scheduled reply identifier and provider idempotency key.
type MessagingSendCommand struct {
	ConnectionReference string `json:"connection_reference"`
	ThreadReference     string `json:"thread_reference"`
	ClientMessageID     string `json:"client_message_id"`
	Body                string `json:"body"`
	AIAttributed        bool   `json:"ai_attributed"`
}

// MessagingSendResult is returned only for a provider acknowledgement.
type MessagingSendResult struct {
	ProviderMessageID string    `json:"provider_message_id"`
	AcceptedAt        time.Time `json:"accepted_at"`
}

// MessagingInbound is one provider-supplied message observation.
type MessagingInbound struct {
	ThreadReference   string    `json:"thread_reference"`
	ProviderMessageID string    `json:"provider_message_id"`
	Direction         string    `json:"direction"`
	SourceAt          time.Time `json:"source_at"`
	Text              string    `json:"text,omitempty"`
	ReadState         string    `json:"read_state,omitempty"`
	AttachmentRefs    []string  `json:"attachment_refs,omitempty"`
}

// MessagingPage is a poll page with the next durable cursor.
type MessagingPage struct {
	Messages   []MessagingInbound `json:"messages"`
	NextCursor string             `json:"next_cursor"`
}

// Adapter is the provider boundary. Any error other than ErrRejected,
// ErrNotFound or ErrUnsupported is ambiguous and requires readback.
type Adapter interface {
	Provider() string
	Capabilities() MessagingCapabilities
	Send(ctx context.Context, in MessagingSendCommand) (MessagingSendResult, error)
	Readback(ctx context.Context, connectionReference, threadReference, clientMessageID string) (MessagingSendResult, error)
	Poll(ctx context.Context, connectionReference, cursor string) (MessagingPage, error)
}

// Disabled is registered for providers whose partner contract is unavailable.
type Disabled struct{ Name string }

func (d Disabled) Provider() string                     { return d.Name }
func (Disabled) Capabilities() MessagingCapabilities { return MessagingCapabilities{} }
func (Disabled) Send(context.Context, MessagingSendCommand) (MessagingSendResult, error) {
	return MessagingSendResult{}, ErrUnsupported
}
func (Disabled) Readback(context.Context, string, string, string) (MessagingSendResult, error) {
	return MessagingSendResult{}, ErrUnsupported
}
func (Disabled) Poll(context.Context, string, string) (MessagingPage, error) {
	return MessagingPage{}, ErrUnsupported
}

// Definite reports whether err is a definite (non-retryable, non-ambiguous) outcome.
func Definite(err error) bool {
	return errors.Is(err, ErrRejected) || errors.Is(err, ErrUnsupported)
}
