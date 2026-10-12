package usecases

import (
	"context"
	"reflect"
	"sort"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
)

// EventSpec documents one produced integration event (§10). The payload type
// drives the generated JSON Schema in contracts/events/owners.
type EventSpec struct {
	Type        string
	Aggregate   string
	Description string
	Consumers   string
	Payload     reflect.Type
}

var producedEvents []EventSpec

func produces(typ, aggregate, desc, consumers string, payload any) {
	producedEvents = append(producedEvents, EventSpec{Type: typ, Aggregate: aggregate, Description: desc, Consumers: consumers, Payload: reflect.TypeOf(payload)})
}

func init() {
	produces("property.created.v1", AggProperty, "A persistent property record was created; it is not necessarily publishable.", "Marketplace, Admin", PropertyEvent{})
	produces("property.updated.v1", AggProperty, "Publicly relevant property fields or status changed.", "Marketplace, Admin", PropertyEvent{})
	produces("property.verification_changed.v1", AggProperty, "The derived public authority/verification summary changed.", "Marketplace", VerificationChangedEvent{})
}

// ProducedEvents lists the §10 events this service emits (sorted by type).
func ProducedEvents() []EventSpec {
	out := append([]EventSpec(nil), producedEvents...)
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// StreamSubjects are the subjects of the service's own JetStream stream.
// Owner-domain families are listed precisely so that other producers'
// families (e.g. property-manager-service manager.* events) never overlap.
func StreamSubjects(prefix string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(prefix + ".owner.>")
	for _, e := range ProducedEvents() {
		add(events.Subject(prefix, familyOf(e.Type)) + ".>")
	}
	return out
}

// familyOf returns the subject family of an event type: all but the last two
// tokens (verb and version), e.g. manager.assignment.created.v1 → manager.assignment.
func familyOf(t string) string {
	dots := []int{}
	for i, c := range t {
		if c == '.' {
			dots = append(dots, i)
		}
	}
	if len(dots) < 2 {
		return t
	}
	return t[:dots[len(dots)-2]]
}

// ConsumerSpec is one durable consumer of another service's events (§10.1).
type ConsumerSpec struct {
	Name    string // durable name
	Stream  string // producer stream area, e.g. "verification"
	Filters []string
	Handle  func(ctx context.Context, e events.Envelope) error
}

// Consumers lists the §10.1 consumers.
func (s *Service) Consumers(prefix string) []ConsumerSpec {
	return []ConsumerSpec{
		{Name: ConsumerVerification, Stream: "verification", Filters: []string{prefix + ".verification.>"}, Handle: s.ApplyVerificationEvent},
		{Name: ConsumerMedia, Stream: "media", Filters: []string{prefix + ".media.ready.v1", prefix + ".media.rejected.v1", prefix + ".media.deleted.v1"},
			Handle: s.ApplyMediaEvent},
		{Name: ConsumerPayment, Stream: "payment", Filters: filters(prefix, ConsumerPayment), Handle: s.ApplyPaymentEvent},
		{Name: ConsumerManager, Stream: "property_manager", Filters: filters(prefix, ConsumerManager), Handle: s.ApplyManagerEvent},
		{Name: ConsumerArtisan, Stream: "artisan", Filters: filters(prefix, ConsumerArtisan), Handle: s.ApplyArtisanEvent},
		{Name: ConsumerProfessional, Stream: "professional_services", Filters: filters(prefix, ConsumerProfessional), Handle: s.ApplyProfessionalEvent},
		{Name: ConsumerMarketplace, Stream: "marketplace", Filters: filters(prefix, ConsumerMarketplace), Handle: s.ApplyMarketplaceEvent},
	}
}

// Job is a Section 11 background job: lease/SKIP LOCKED based, bounded and
// safe to run in several worker instances.
type Job struct {
	Name string
	Run  func(ctx context.Context) error
}

// JobConfig parameterizes jobs.
type JobConfig struct {
	Owner       string
	ImportLease time.Duration
	Batch       int32
}

// DomainJobs lists the Section 11 jobs implemented so far.
func (s *Service) DomainJobs(cfg JobConfig) []Job {
	if cfg.Batch <= 0 {
		cfg.Batch = 50
	}
	if cfg.ImportLease <= 0 {
		cfg.ImportLease = 2 * time.Minute
	}
	return []Job{
		{Name: "wallet-registration", Run: s.DeliverWalletRegistrations},
		{Name: "payout-cooling-off", Run: func(ctx context.Context) error {
			_, err := s.ActivateCooledPayoutAccounts(ctx, cfg.Batch)
			return err
		}},
		{Name: "property-imports", Run: func(ctx context.Context) error {
			for i := 0; i < 5; i++ {
				more, err := s.ProcessImports(ctx, cfg.Owner, cfg.ImportLease, 5)
				if err != nil || !more {
					return err
				}
			}
			return nil
		}},
		{Name: "verification-reconciliation", Run: func(ctx context.Context) error { return s.ReconcileVerifications(ctx, cfg.Batch) }},
		{Name: "ownership-expiry", Run: func(ctx context.Context) error { return s.ExpireOwnershipAuthority(ctx, cfg.Batch) }},
		{Name: "consent-and-mandate-expiry", Run: s.ExpireConsentsAndMandates},
		{Name: "manager-mandates", Run: func(ctx context.Context) error { return s.AdvanceAssignments(ctx, cfg.Batch) }},
		{Name: "lead-follow-ups", Run: func(ctx context.Context) error {
			_, err := s.RunLeadFollowUps(ctx, cfg.Batch)
			return err
		}},
		{Name: "viewing-reminders", Run: func(ctx context.Context) error { return s.RunViewingJobs(ctx, cfg.Batch) }},
		{Name: "application-deadlines", Run: func(ctx context.Context) error { return s.RunApplicationDeadlines(ctx, cfg.Batch) }},
		{Name: "signature-reconciliation", Run: func(ctx context.Context) error { return s.RunSignatureReconciliation(ctx, cfg.Batch) }},
		{Name: "rent-schedule", Run: func(ctx context.Context) error { return s.RunRentSchedule(ctx, cfg.Batch) }},
		{Name: "rent-obligations-arrears", Run: func(ctx context.Context) error { return s.RunRentObligations(ctx, cfg.Batch) }},
		{Name: "financial-core-readbacks", Run: func(ctx context.Context) error { return s.RunCoreFinancialReadbacks(ctx, cfg.Batch) }},
		{Name: "deposits", Run: func(ctx context.Context) error { return s.RunDeposits(ctx, cfg.Batch) }},
		{Name: "renewals-and-notices", Run: func(ctx context.Context) error { return s.RunRenewals(ctx, cfg.Batch) }},
		{Name: "sale-offers-and-milestones", Run: func(ctx context.Context) error { return s.RunSaleJobs(ctx, cfg.Batch) }},
		{Name: "maintenance-preventive-and-payments", Run: func(ctx context.Context) error { return s.RunMaintenanceJobs(ctx, cfg.Batch) }},
		{Name: "professional-fees", Run: func(ctx context.Context) error { return s.RunProfessionalJobs(ctx, cfg.Batch) }},
		{Name: "smart-access-expiry", Run: func(ctx context.Context) error { return s.RunAccessJobs(ctx, cfg.Batch) }},
		{Name: "insurance-renewals", Run: func(ctx context.Context) error { return s.RunInsuranceJobs(ctx, cfg.Batch) }},
		{Name: "statutory-reminders-and-assistance-expiry", Run: func(ctx context.Context) error {
			if err := s.RunStatutoryReminders(ctx, cfg.Batch); err != nil {
				return err
			}
			return s.ExpireAssistance(ctx)
		}},
		{Name: "customer-notifications", Run: func(ctx context.Context) error { return s.RunCustomerNotifications(ctx, cfg.Batch) }},
		{Name: "report-generation", Run: func(ctx context.Context) error { return s.RunReportJobs(ctx, cfg.Batch) }},
		{Name: "privacy-requests-and-retention", Run: func(ctx context.Context) error { return s.RunPrivacyJobs(ctx, cfg.Batch) }},
		{Name: "shortlet-bookings-calendar-and-claims", Run: func(ctx context.Context) error { return s.RunShortletJobs(ctx, cfg.Batch) }},
		{Name: "listing-media-access", Run: func(ctx context.Context) error { return s.RunListingMediaAccess(ctx, cfg.Batch) }},
		{Name: "listing-freshness", Run: func(ctx context.Context) error {
			_, _, err := s.RunFreshness(ctx, cfg.Batch)
			return err
		}},
	}
}
