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
		{Name: "listing-freshness", Run: func(ctx context.Context) error {
			_, _, err := s.RunFreshness(ctx, cfg.Batch)
			return err
		}},
	}
}
