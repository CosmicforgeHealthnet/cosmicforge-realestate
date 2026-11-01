// Package inventory owns commercial stock and holds, referencing the existing
// physical graph. A hold is never a confirmed reservation or payment.
package inventory

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

var ErrInvalid = errors.New("invalid commercial inventory")
var channelPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,79}$`)

func ValidChannel(code string) bool { return code == "" || channelPattern.MatchString(code) }

// DateRange is property-local, checkout-exclusive civil dates. AddDate rather
// than 24-hour arithmetic preserves room nights across daylight-saving changes.
type DateRange struct {
	DateFrom string `json:"date_from"`
	DateTo   string `json:"date_to"`
}

func (r DateRange) Days(timezone string) ([]string, error) {
	loc, e := time.LoadLocation(timezone)
	if e != nil {
		return nil, ErrInvalid
	}
	first, e := time.ParseInLocation(time.DateOnly, r.DateFrom, loc)
	if e != nil || first.Year() < 1 || first.Format(time.DateOnly) != r.DateFrom {
		return nil, ErrInvalid
	}
	end, e := time.ParseInLocation(time.DateOnly, r.DateTo, loc)
	if e != nil || end.Format(time.DateOnly) != r.DateTo || !end.After(first) {
		return nil, ErrInvalid
	}
	days := []string{}
	for d := first; d.Before(end); d = d.AddDate(0, 0, 1) {
		if len(days) >= 366 {
			return nil, ErrInvalid
		}
		days = append(days, d.Format(time.DateOnly))
	}
	return days, nil
}

type Pool struct {
	SourceNodeVersion int64     `json:"source_node_version"`
	ID                ids.ID    `json:"id"`
	OrganisationID    ids.ID    `json:"organisation_id"`
	PropertyID        ids.ID    `json:"property_id"`
	NodeID            ids.ID    `json:"node_id"`
	Name              string    `json:"name"`
	Kind              string    `json:"kind"`
	Capacity          int       `json:"capacity"`
	State             string    `json:"state"`
	Version           int64     `json:"version"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}
type Product struct {
	ID             ids.ID    `json:"id"`
	OrganisationID ids.ID    `json:"organisation_id"`
	PropertyID     ids.ID    `json:"property_id"`
	NodeID         ids.ID    `json:"node_id"`
	PoolID         ids.ID    `json:"pool_id"`
	RatePlanID     *ids.ID   `json:"rate_plan_id,omitempty"`
	Name           string    `json:"name"`
	State          string    `json:"state"`
	MinGuests      int       `json:"min_guests"`
	MaxGuests      int       `json:"max_guests"`
	PolicyCode     string    `json:"policy_code"`
	PolicyVersion  int64     `json:"policy_version"`
	Channels       []string  `json:"channels"`
	Version        int64     `json:"version"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type Day struct {
	Day               string    `json:"day"`
	Capacity          int       `json:"capacity"`
	Committed         int       `json:"committed"`
	Overbooked        int       `json:"overbooked"`
	Held              int       `json:"held"`
	Blocked           int       `json:"blocked"`
	Allotted          int       `json:"allotted"`
	ChannelAllotted   int       `json:"channel_allotted"`
	Available         int       `json:"available"`
	PhysicalAvailable int       `json:"physical_available"`
	Version           int64     `json:"version"`
	AllotmentID       *ids.ID   `json:"allotment_id,omitempty"`
	UpdatedAt         time.Time `json:"updated_at"`
}
type Hold struct {
	ID             ids.ID    `json:"id"`
	OrganisationID ids.ID    `json:"organisation_id"`
	PropertyID     ids.ID    `json:"property_id"`
	ProductID      ids.ID    `json:"product_id"`
	PoolID         ids.ID    `json:"pool_id"`
	QuoteID        ids.ID    `json:"quote_id"`
	DateFrom       string    `json:"date_from"`
	DateTo         string    `json:"date_to"`
	Quantity       int       `json:"quantity"`
	Channel        string    `json:"channel"`
	State          string    `json:"state"`
	ExpiresAt      time.Time `json:"expires_at"`
	Version        int64     `json:"version"`
	CreatedAt      time.Time `json:"created_at"`
}

// ReservationAuthority is implemented only by the canonical Section 9 engine.
// Implementations must atomically consume the exact fenced hold and persist the
// accepted immutable price snapshot before returning a durable acknowledgement.
type ReservationCommand struct {
	OrganisationID, HoldID, QuoteID                     ids.ID
	ExpectedHoldVersion                                 int64
	Source, ExternalID, ExternalVersion, IdempotencyKey string
}
type ReservationAcceptance struct {
	OrganisationID, ReservationID, HoldID, QuoteID ids.ID
	DurableReference                               string
	Version                                        int64
}
type ReservationAuthority interface {
	Accept(ctx context.Context, command ReservationCommand) (ReservationAcceptance, error)
}
