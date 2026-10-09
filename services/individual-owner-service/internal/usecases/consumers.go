package usecases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// §10.1 consumers of other services' events. Each delivery is applied
// exactly once (processed_events in the same transaction) through the same
// use case — and therefore the same authorization, validation and state
// machine — as the producer's synchronous internal API, acting as the
// producing service and its delegated actor. Unknown references are retried
// (out-of-order delivery); business rejections are terminal and logged;
// money truth is only ever projected, never synthesized.
//
// The payload shapes below are this service's consumer contract
// (docs: services/individual-owner-service/README.md "Consumed events"); the
// producers' own schemas do not exist yet, so live integration of these
// families is BLOCKED (docs/product/individual-owner/EVENT_COMPATIBILITY.md).

const (
	ConsumerPayment      = "individual-owner-service--payment"
	ConsumerManager      = "individual-owner-service--property-manager"
	ConsumerArtisan      = "individual-owner-service--artisan"
	ConsumerProfessional = "individual-owner-service--professional-services"
	ConsumerMarketplace  = "individual-owner-service--marketplace"
)

// consumedEvents lists the event types each consumer accepts.
var consumedEvents = map[string][]string{
	ConsumerPayment:      {"payment.intent.updated.v1", "payment.refund.updated.v1"},
	ConsumerManager:      {"manager.mandate.responded.v1", "manager.activity.recorded.v1"},
	ConsumerArtisan:      {"artisan.quote.submitted.v1", "artisan.job.event_recorded.v1"},
	ConsumerProfessional: {"professional.engagement.updated.v1"},
	ConsumerMarketplace:  {"marketplace.listing.reported.v1"},
}

func filters(prefix, consumer string) []string {
	var out []string
	for _, t := range consumedEvents[consumer] {
		out = append(out, events.Subject(prefix, t))
	}
	return out
}

// consumerOutcome classifies a handler error: not-found is retried (the
// referenced record may not be committed yet), other client errors are
// terminal, everything else is retried.
func (s *Service) consumerOutcome(ctx context.Context, consumer string, e events.Envelope, err error) error {
	if err == nil {
		return nil
	}
	var ae *apperrors.Error
	if errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != 404 && ae.Status != 503 {
		s.Logger.WarnContext(ctx, "event rejected", "consumer", consumer, "event_id", e.EventID, "event_type", e.EventType, "code", ae.Code)
		return events.Permanent(err)
	}
	return err
}

// checkUpstream enforces the producer identity and envelope version of an
// upstream delivery. Violations are permanent: they are logged and never
// applied, so they cannot change state.
func checkUpstream(e events.Envelope, producer platform.Service, aggregate string) error {
	if e.Producer != string(producer) {
		return events.Permanent(fmt.Errorf("%s: unexpected producer %q", e.EventType, e.Producer))
	}
	if aggregate != "" && e.AggregateType != aggregate {
		return events.Permanent(fmt.Errorf("%s: unexpected aggregate type %q", e.EventType, e.AggregateType))
	}
	var v struct {
		SchemaVersion *int `json:"schema_version"`
	}
	if err := json.Unmarshal(e.Payload, &v); err != nil {
		return events.Permanent(err)
	}
	if v.SchemaVersion != nil && *v.SchemaVersion != 1 {
		return events.Permanent(fmt.Errorf("%s: unsupported payload schema_version %d", e.EventType, *v.SchemaVersion))
	}
	return nil
}

// consume parses, checks the producer, then applies a delivery once.
func consume[P any](s *Service, consumer string, producer platform.Service, e events.Envelope, pre func(context.Context, P) (Evidence, error),
	apply func(context.Context, P, Evidence) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := checkUpstream(e, producer, ""); err != nil {
			return err
		}
		if !slices.Contains(consumedEvents[consumer], e.EventType) {
			return events.Permanent(fmt.Errorf("unsupported event type or version %q", e.EventType))
		}
		var p P
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return events.Permanent(err)
		}
		var ev Evidence
		if pre != nil {
			var err error
			if ev, err = pre(ctx, p); err != nil {
				return s.consumerOutcome(ctx, consumer, e, err)
			}
		}
		err := s.InTx(ctx, func(ctx context.Context) error {
			first, err := events.MarkProcessed(ctx, mustTx(ctx), consumer, e)
			if err != nil || !first {
				return err
			}
			return apply(ctx, p, ev)
		})
		return s.consumerOutcome(ctx, consumer, e, err)
	}
}

func serviceActor(svc platform.Service, domain platform.Domain, actor ids.ID) authz.Subject {
	return authz.Subject{Kind: authz.KindService, Service: svc, ActorDomain: domain, ActorID: actor}
}

// ---- payment.* ----

// PaymentIntentEvent is the consumed payment.intent.updated.v1 payload.
type PaymentIntentEvent struct {
	IntentRef     string    `json:"intent_ref"`
	Status        string    `json:"status"`
	AmountMinor   int64     `json:"amount_minor"`
	PaidMinor     int64     `json:"paid_minor"`
	RefundedMinor int64     `json:"refunded_minor"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// PaymentRefundEvent is the consumed payment.refund.updated.v1 payload.
type PaymentRefundEvent struct {
	RefundRef     string `json:"refund_ref"`
	IntentRef     string `json:"intent_ref"`
	Status        string `json:"status"`
	RefundedMinor int64  `json:"refunded_minor"`
}

// ApplyPaymentEvent projects payment-service outcomes onto rent, deposits,
// sale milestones, bookings, damage claims, maintenance jobs and fees.
func (s *Service) ApplyPaymentEvent(ctx context.Context, e events.Envelope) error {
	switch e.EventType {
	case "payment.intent.updated.v1":
		return consume(s, ConsumerPayment, platform.PaymentService, e, nil, func(ctx context.Context, p PaymentIntentEvent, _ Evidence) error {
			if p.IntentRef == "" || p.Status == "" || p.PaidMinor < 0 || p.UpdatedAt.IsZero() {
				return apperrors.InvalidRequest("intent_ref, status, updated_at and a non-negative paid_minor are required.")
			}
			return s.ProjectPaymentIntent(ctx, e.EventID, payment.Intent{Ref: p.IntentRef, Status: p.Status, AmountMinor: p.AmountMinor, PaidMinor: p.PaidMinor,
				RefundedMinor: p.RefundedMinor, UpdatedAt: p.UpdatedAt})
		})(ctx)
	case "payment.refund.updated.v1":
		return consume(s, ConsumerPayment, platform.PaymentService, e, nil, func(ctx context.Context, p PaymentRefundEvent, _ Evidence) error {
			return s.projectRefund(ctx, p)
		})(ctx)
	}
	return events.Permanent(fmt.Errorf("unsupported event type or version %q", e.EventType))
}

// projectRefund applies a refund outcome to the booking that requested it.
func (s *Service) projectRefund(ctx context.Context, p PaymentRefundEvent) error {
	tx := mustTx(ctx)
	var id ids.ID
	err := tx.QueryRow(ctx, `SELECT id FROM shortlet_bookings WHERE refund_request_ref = $1 FOR UPDATE`, p.RefundRef).Scan(&id)
	if isNoRows(err) {
		return notFound("refund") // not committed yet: retried
	}
	if err != nil {
		return err
	}
	b, err := s.q.GetBooking(ctx, tx, id)
	if err != nil || deref(b.RefundStatus) == payment.RefundCompleted {
		return err // terminal state already projected
	}
	refunded := b.RefundedMinor
	if p.Status == payment.RefundCompleted {
		refunded += p.RefundedMinor
	}
	if err := s.q.SetBookingRefund(ctx, tx, db.SetBookingRefundParams{ID: id, RefundRequestRef: &p.RefundRef, RefundStatus: p.Status, RefundedMinor: refunded,
		Now: s.now()}); err != nil {
		return err
	}
	return s.record(ctx, "shortlet.booking.refund_event", AggBooking, id, map[string]any{"status": p.Status, "refunded_minor": refunded}, nil)
}

// ---- manager.* ----

// ManagerResponseEvent is the consumed manager.mandate.responded.v1 payload.
type ManagerResponseEvent struct {
	AssignmentID ids.ID `json:"assignment_id"`
	ManagerRefID ids.ID `json:"manager_ref_id"`
	Decision     string `json:"decision"`
}

// ManagerActivityEvent is the consumed manager.activity.recorded.v1 payload.
type ManagerActivityEvent struct {
	AssignmentID ids.ID  `json:"assignment_id"`
	ManagerRefID ids.ID  `json:"manager_ref_id"`
	ActivityType string  `json:"activity_type"`
	RefType      *string `json:"ref_type,omitempty"`
	RefID        *ids.ID `json:"ref_id,omitempty"`
	Summary      string  `json:"summary"`
}

// ApplyManagerEvent applies property-manager-service mandate responses and
// activity reports.
func (s *Service) ApplyManagerEvent(ctx context.Context, e events.Envelope) error {
	switch e.EventType {
	case "manager.mandate.responded.v1":
		return consume(s, ConsumerManager, platform.PropertyManagerService, e, nil, func(ctx context.Context, p ManagerResponseEvent, _ Evidence) error {
			_, err := s.ManagerRespond(ctx, serviceActor(platform.PropertyManagerService, platform.DomainPropertyManager, p.ManagerRefID), p.AssignmentID,
				ManagerResponseInput{Decision: p.Decision})
			return err
		})(ctx)
	case "manager.activity.recorded.v1":
		return consume(s, ConsumerManager, platform.PropertyManagerService, e, nil, func(ctx context.Context, p ManagerActivityEvent, _ Evidence) error {
			_, err := s.ManagerRecordActivity(ctx, serviceActor(platform.PropertyManagerService, platform.DomainPropertyManager, p.ManagerRefID), p.AssignmentID,
				ActivityInput{ActivityType: p.ActivityType, RefType: p.RefType, RefID: p.RefID, Summary: p.Summary})
			return err
		})(ctx)
	}
	return events.Permanent(fmt.Errorf("unsupported event type or version %q", e.EventType))
}

// ---- artisan.* ----

// ArtisanQuoteEvent is the consumed artisan.quote.submitted.v1 payload.
type ArtisanQuoteEvent struct {
	RequestID   ids.ID `json:"request_id"`
	ProviderRef ids.ID `json:"provider_ref"`
	ProviderQuoteInput
}

// ArtisanJobEvent is the consumed artisan.job.event_recorded.v1 payload.
type ArtisanJobEvent struct {
	JobID       ids.ID `json:"job_id"`
	ProviderRef ids.ID `json:"provider_ref"`
	JobEventInput
}

// ApplyArtisanEvent applies artisan quotes and job records.
func (s *Service) ApplyArtisanEvent(ctx context.Context, e events.Envelope) error {
	actor := func(p ids.ID) authz.Subject { return serviceActor(platform.ArtisanService, platform.DomainArtisan, p) }
	switch e.EventType {
	case "artisan.quote.submitted.v1":
		return consume(s, ConsumerArtisan, platform.ArtisanService, e,
			func(ctx context.Context, p ArtisanQuoteEvent) (Evidence, error) {
				return s.CheckProviderEvidence(ctx, actor(p.ProviderRef), p.MediaRefs(), "IMAGE", "DOCUMENT")
			},
			func(ctx context.Context, p ArtisanQuoteEvent, ev Evidence) error {
				_, err := s.ProviderSubmitQuote(ctx, actor(p.ProviderRef), p.RequestID, p.ProviderQuoteInput, ev)
				return err
			})(ctx)
	case "artisan.job.event_recorded.v1":
		return consume(s, ConsumerArtisan, platform.ArtisanService, e,
			func(ctx context.Context, p ArtisanJobEvent) (Evidence, error) {
				return s.CheckProviderEvidence(ctx, actor(p.ProviderRef), p.MediaRefs(), "IMAGE", "DOCUMENT")
			},
			func(ctx context.Context, p ArtisanJobEvent, ev Evidence) error {
				_, err := s.ProviderJobEvent(ctx, actor(p.ProviderRef), p.JobID, p.JobEventInput, ev)
				return err
			})(ctx)
	}
	return events.Permanent(fmt.Errorf("unsupported event type or version %q", e.EventType))
}

// ---- professional.* ----

// ProfessionalUpdateEvent is the consumed professional.engagement.updated.v1 payload.
type ProfessionalUpdateEvent struct {
	AppointmentID   ids.ID `json:"appointment_id"`
	ProfessionalRef ids.ID `json:"professional_ref"`
	ProviderAppointmentUpdate
}

// ApplyProfessionalEvent applies professional quotes, starts and reports.
func (s *Service) ApplyProfessionalEvent(ctx context.Context, e events.Envelope) error {
	if e.EventType != "professional.engagement.updated.v1" {
		return events.Permanent(fmt.Errorf("unsupported event type or version %q", e.EventType))
	}
	actor := func(p ids.ID) authz.Subject {
		return serviceActor(platform.ProfessionalServicesService, platform.DomainProfessionalServices, p)
	}
	return consume(s, ConsumerProfessional, platform.ProfessionalServicesService, e,
		func(ctx context.Context, p ProfessionalUpdateEvent) (Evidence, error) {
			return s.CheckProviderEvidence(ctx, actor(p.ProfessionalRef), p.MediaRefs(), "DOCUMENT", "IMAGE")
		},
		func(ctx context.Context, p ProfessionalUpdateEvent, ev Evidence) error {
			_, err := s.ProviderUpdateAppointment(ctx, actor(p.ProfessionalRef), p.AppointmentID, p.ProviderAppointmentUpdate, ev)
			return err
		})(ctx)
}

// ---- marketplace.* ----

// ListingReportedEvent is the consumed marketplace.listing.reported.v1 payload.
type ListingReportedEvent struct {
	ListingID ids.ID `json:"listing_id"`
	ListingReportInput
}

// ApplyMarketplaceEvent routes stale/duplicate/misleading listing reports
// back to the authoritative listing.
func (s *Service) ApplyMarketplaceEvent(ctx context.Context, e events.Envelope) error {
	if e.EventType != "marketplace.listing.reported.v1" {
		return events.Permanent(fmt.Errorf("unsupported event type or version %q", e.EventType))
	}
	return consume(s, ConsumerMarketplace, platform.MarketplaceService, e, nil, func(ctx context.Context, p ListingReportedEvent, _ Evidence) error {
		if err := schema.Validate(p.ListingReportInput); err != nil {
			return err
		}
		_, err := s.InternalReportListing(ctx, authz.Subject{Kind: authz.KindService, Service: platform.MarketplaceService}, p.ListingID, p.ListingReportInput)
		return err
	})(ctx)
}
