// Package notification is individual-owner-service's client for the
// notification-service internal API (contracts/internal/openapi/notification-service.yaml).
// Delivery is owned by notification-service; this service never talks to an
// email/SMS/push provider directly and never claims delivery or read
// receipts — only that notification-service accepted the request.
package notification

import (
	"context"
	"errors"
	"net/http"
	"time"

	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Templates seeded by notification-service (00002_seed_templates.sql).
const (
	TemplateVerificationCode = "account.verification_code" // EMAIL, SMS; variable: code
	TemplateSecurityAlert    = "account.security_alert"    // PUSH, EMAIL, SMS; variable: event
)

// Channels.
const (
	ChannelPush  = "PUSH"
	ChannelEmail = "EMAIL"
	ChannelSMS   = "SMS"
)

// Recipient is an owner-domain recipient reference.
type Recipient struct {
	Domain string `json:"recipient_domain"`
	ID     ids.ID `json:"recipient_id"`
}

// Destinations are contact details owned by this domain.
type Destinations struct {
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"`
}

// SendRequest is the subset of the notification-service SendRequest used here.
type SendRequest struct {
	Recipient       Recipient         `json:"recipient"`
	TemplateKey     string            `json:"template_key"`
	Channels        []string          `json:"channels,omitempty"`
	Variables       map[string]string `json:"variables"`
	Destinations    Destinations      `json:"destinations"`
	CallerReference string            `json:"caller_reference,omitempty"`
}

// Accepted is notification-service's acceptance (not delivery).
type Accepted struct {
	NotificationID ids.ID `json:"notification_id"`
	Suppressed     []struct {
		Channel string `json:"channel"`
		Reason  string `json:"reason"`
	} `json:"suppressed"`
}

// Sender sends notifications. idempotencyKey makes retries safe.
type Sender interface {
	Send(ctx context.Context, req SendRequest, idempotencyKey string) (Accepted, error)
}

// Client implements Sender over HTTP.
type Client struct{ c *httpx.Client }

// New builds a client authenticated with service tokens.
func New(baseURL string, tokens httpx.TokenSource, timeout time.Duration) (*Client, error) {
	c, err := httpx.NewClient(httpx.ClientConfig{BaseURL: baseURL, Audience: string(platform.NotificationService), Tokens: tokens, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	return &Client{c: c}, nil
}

// Send implements Sender.
func (c *Client) Send(ctx context.Context, req SendRequest, idempotencyKey string) (Accepted, error) {
	var out struct {
		Data Accepted `json:"data"`
	}
	err := c.c.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/internal/v1/notifications", Body: req, IdempotencyKey: idempotencyKey}, &out)
	return out.Data, err
}

// Outcome classifies a send failure for retry decisions.
type Outcome int

const (
	// OutcomeRetryable: transport failure or 5xx/429 — safe to retry with the same key.
	OutcomeRetryable Outcome = iota
	// OutcomeDefinite: 4xx — retrying the same request cannot succeed.
	OutcomeDefinite
)

// Classify returns the retry class of err.
func Classify(err error) Outcome {
	var re *httpx.RemoteError
	if errors.As(err, &re) && re.Status >= 400 && re.Status < 500 && re.Status != 408 && re.Status != 429 && re.Code != "IDEMPOTENCY_IN_PROGRESS" {
		return OutcomeDefinite
	}
	return OutcomeRetryable
}
