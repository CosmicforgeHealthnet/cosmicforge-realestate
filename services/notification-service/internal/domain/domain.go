// Package domain holds the notification model: delivery state machine,
// channels, categories and quiet-hours policy.
package domain

import (
	"time"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/fsm"
)

// Status is a delivery state.
type Status string

const (
	StatusQueued         Status = "QUEUED"
	StatusDispatching    Status = "DISPATCHING"
	StatusSent           Status = "SENT"
	StatusRetryScheduled Status = "RETRY_SCHEDULED"
	StatusFailedFinal    Status = "FAILED_FINAL"
)

// Machine is the approved delivery state machine:
//
//	QUEUED -> DISPATCHING -> SENT | RETRY_SCHEDULED | FAILED_FINAL
//	RETRY_SCHEDULED -> DISPATCHING
//
// SENT means the provider accepted the message; user delivery/read receipts
// are tracked separately (receipt_status) and never conflated with SENT.
var Machine = fsm.New(map[Status][]Status{
	StatusQueued:         {StatusDispatching},
	StatusDispatching:    {StatusSent, StatusRetryScheduled, StatusFailedFinal},
	StatusRetryScheduled: {StatusDispatching},
})

// Channel is a delivery channel.
type Channel string

const (
	ChannelPush  Channel = "PUSH"
	ChannelEmail Channel = "EMAIL"
	ChannelSMS   Channel = "SMS"
)

// ValidChannel reports a supported channel.
func ValidChannel(c Channel) bool { return c == ChannelPush || c == ChannelEmail || c == ChannelSMS }

// Category classifies notifications for preferences.
type Category string

const (
	// CategorySecurity is critical: it ignores opt-outs and quiet hours.
	CategorySecurity      Category = "SECURITY"
	CategoryAccount       Category = "ACCOUNT"
	CategoryTransactional Category = "TRANSACTIONAL"
	CategoryActivity      Category = "ACTIVITY"
)

// Critical reports whether a category overrides preferences.
func (c Category) Critical() bool { return c == CategorySecurity }

// Configurable reports whether recipients may change preferences for c.
func (c Category) Configurable() bool {
	return c == CategoryAccount || c == CategoryTransactional || c == CategoryActivity
}

// MaxAttempts per channel (bounded retry).
var MaxAttempts = map[Channel]int{ChannelPush: 5, ChannelEmail: 6, ChannelSMS: 4}

// QuietHours defers non-critical PUSH and SMS deliveries.
type QuietHours struct {
	Start    time.Duration // offset from local midnight
	End      time.Duration
	Location *time.Location
}

// DeferUntil returns when a non-critical PUSH/SMS created at t may be sent.
// Windows may cross midnight (e.g. 22:00-07:00).
func (q QuietHours) DeferUntil(t time.Time) time.Time {
	if q.Location == nil || q.Start == q.End {
		return t
	}
	local := t.In(q.Location)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, q.Location)
	off := local.Sub(midnight)
	if q.Start < q.End {
		if off >= q.Start && off < q.End {
			return midnight.Add(q.End).UTC()
		}
		return t
	}
	// crosses midnight
	if off >= q.Start {
		return midnight.Add(24*time.Hour + q.End).UTC()
	}
	if off < q.End {
		return midnight.Add(q.End).UTC()
	}
	return t
}

// Error codes owned by notification-service.
const (
	CodeNotFound          = "NOTIFICATION_NOT_FOUND"
	CodeTemplateNotFound  = "TEMPLATE_NOT_FOUND"
	CodeTemplateInvalid   = "INVALID_TEMPLATE"
	CodeVariables         = "INVALID_TEMPLATE_VARIABLES"
	CodeChannelNotAllowed = "CHANNEL_NOT_ALLOWED"
	CodeRecipientDomain   = "RECIPIENT_DOMAIN_MISMATCH"
	CodeEndpointNotFound  = "PUSH_ENDPOINT_NOT_FOUND"
	CodeInvalidToken      = "INVALID_PUSH_TOKEN"
)

// ErrNotFound hides missing/invisible notifications.
func ErrNotFound() *apperrors.Error {
	return apperrors.NotFound(CodeNotFound, "Notification not found.")
}
