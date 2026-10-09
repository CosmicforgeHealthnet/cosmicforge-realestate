// Package customernotify is the contract for notifying people who belong to
// another user domain (marketplace customers acting as tenants or guests).
//
// notification-service lets a user-domain service address only recipients
// of its own domain, and the recipient's contacts, consent and preferred
// channels are owned by that domain. The owner domain therefore asks the
// recipient's domain service to notify its customer, supplying: the sender's
// authority context, the relationship that entitles the sender to contact
// the recipient, a fixed purpose and the content variables. The recipient
// domain decides — by the customer's consent and channel preferences —
// whether and how notification-service delivers it, and answers ACCEPTED
// (with the channels used) or SUPPRESSED (with a reason).
//
// marketplace-service does not implement this contract yet: the service is
// built with a nil API, requests stay recorded and are marked BLOCKED, and
// `CUSTOMER_NOTIFY_SERVICE_URL` is refused by configuration.
package customernotify

import (
	"context"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Request asks the recipient's domain to notify its customer.
type Request struct {
	NotificationID   ids.ID // idempotency reference
	RecipientDomain  string // "marketplace"
	RecipientRef     ids.ID
	RelationshipType string // tenancy | shortlet_booking
	RelationshipID   ids.ID
	PropertyID       ids.ID
	SenderAccountID  ids.ID
	Purpose          string // SERVICE_ANNOUNCEMENT | UTILITY_INTERRUPTION | SERVICE_CHARGE_UPDATE | MAINTENANCE_ACCESS | SAFETY_NOTICE
	Variables        map[string]string
}

// Result statuses.
const (
	StatusAccepted   = "ACCEPTED"
	StatusSuppressed = "SUPPRESSED"
)

// Result is the recipient domain's answer.
type Result struct {
	Status   string   // ACCEPTED | SUPPRESSED
	Reason   string   // e.g. NO_CONSENT, CHANNEL_OPTED_OUT, UNKNOWN_RECIPIENT (SUPPRESSED)
	Channels []string // channels used (ACCEPTED)
	Ref      string   // recipient-domain reference
}

// API is the contract.
type API interface {
	Notify(ctx context.Context, req Request, idemKey string) (Result, error)
}
