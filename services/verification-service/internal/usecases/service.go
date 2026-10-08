// Package usecases implements verification-service behaviour. All state
// changes go through transition(), which enforces the approved state machine
// and writes history, an audit event and (where catalogued) an outbox event
// in the same transaction. Provider calls never run inside a transaction.
package usecases

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/integrations/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// Event types published by verification-service (EVENT_CATALOG.md).
const (
	EventSubmitted            = "verification.submitted.v1"
	EventProcessing           = "verification.processing.v1"
	EventManualReviewRequired = "verification.manual_review_required.v1"
	EventVerified             = "verification.verified.v1"
	EventRejected             = "verification.rejected.v1"
	EventExpired              = "verification.expired.v1"
)

// MediaClient is the subset of media-document-service used for evidence.
type MediaClient interface {
	Get(ctx context.Context, mediaID ids.ID) (media.Info, error)
	DownloadURL(ctx context.Context, mediaID ids.ID) (string, error)
}

// Options tune worker behaviour.
type Options struct {
	Validity        domain.Validity
	ProviderTimeout time.Duration // per provider call
	MaxSubmitTries  int           // before escalating to manual review
	MaxPollTries    int           // SUBMITTED polls before escalation
	PollInterval    time.Duration // first poll after an async acceptance
	MaxBackoff      time.Duration
	AttemptLease    time.Duration
	Now             func() time.Time
}

func (o *Options) defaults() {
	if o.Validity == nil {
		o.Validity = domain.DefaultValidity
	}
	if o.ProviderTimeout <= 0 {
		o.ProviderTimeout = 20 * time.Second
	}
	if o.MaxSubmitTries <= 0 {
		o.MaxSubmitTries = 8
	}
	if o.MaxPollTries <= 0 {
		o.MaxPollTries = 60
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 30 * time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = time.Hour
	}
	if o.AttemptLease <= 0 {
		o.AttemptLease = 2 * time.Minute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

// Service is the verification application service.
type Service struct {
	db        database.DB
	q         *db.Queries
	outbox    *outbox.Store
	audit     *audit.Recorder
	providers *providers.Registry
	media     MediaClient
	opts      Options
	log       *slog.Logger
}

// New wires the service.
func New(pool database.DB, ob *outbox.Store, reg *providers.Registry, mc MediaClient, opts Options, log *slog.Logger) *Service {
	opts.defaults()
	if log == nil {
		log = slog.Default()
	}
	return &Service{db: pool, q: db.New(), outbox: ob, audit: audit.NewRecorder(string(platform.VerificationService)),
		providers: reg, media: mc, opts: opts, log: log}
}

func (s *Service) now() time.Time { return s.opts.Now().UTC().Truncate(time.Microsecond) }

// CaseView is the internal API representation of a case.
type CaseView struct {
	VerificationID           ids.ID         `json:"verification_id"`
	VerificationType         string         `json:"verification_type"`
	Subject                  domain.Subject `json:"subject"`
	Status                   string         `json:"status"`
	Provider                 string         `json:"provider"`
	RequestedByService       string         `json:"requested_by_service"`
	OrganizationID           *ids.ID        `json:"organization_id,omitempty"`
	CallerReference          *string        `json:"caller_reference,omitempty"`
	SupersedesVerificationID *ids.ID        `json:"supersedes_verification_id,omitempty"`
	ReasonCode               *string        `json:"reason_code,omitempty"`
	SubmittedAt              *time.Time     `json:"submitted_at"`
	DecidedAt                *time.Time     `json:"decided_at"`
	ExpiresAt                *time.Time     `json:"expires_at"`
	ExpiredAt                *time.Time     `json:"expired_at"`
	CreatedAt                time.Time      `json:"created_at"`
	UpdatedAt                time.Time      `json:"updated_at"`
}

// PublicCaseView is what an end user may see about their own case.
type PublicCaseView struct {
	VerificationID   ids.ID     `json:"verification_id"`
	VerificationType string     `json:"verification_type"`
	Status           string     `json:"status"`
	SubmittedAt      *time.Time `json:"submitted_at"`
	DecidedAt        *time.Time `json:"decided_at"`
	ExpiresAt        *time.Time `json:"expires_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// EvidenceView is an attached evidence reference.
type EvidenceView struct {
	EvidenceID   ids.ID    `json:"evidence_id"`
	MediaID      ids.ID    `json:"media_id"`
	EvidenceType string    `json:"evidence_type"`
	CreatedAt    time.Time `json:"created_at"`
}

// DecisionView is a final decision.
type DecisionView struct {
	Decision       string     `json:"decision"`
	DecisionSource string     `json:"decision_source"`
	ReasonCode     *string    `json:"reason_code"`
	DecidedAt      time.Time  `json:"decided_at"`
	ExpiresAt      *time.Time `json:"expires_at"`
}

// ResultView is the verification result.
type ResultView struct {
	VerificationID ids.ID         `json:"verification_id"`
	Status         string         `json:"status"`
	Decision       *DecisionView  `json:"decision"`
	Evidence       []EvidenceView `json:"evidence"`
}

func toView(c db.VerificationCase) CaseView {
	return CaseView{
		VerificationID: c.VerificationID, VerificationType: c.VerificationType,
		Subject: domain.Subject{Type: domain.SubjectType(c.SubjectType), Domain: platform.Domain(c.SubjectDomain), ID: c.SubjectID},
		Status:  c.Status, Provider: c.Provider, RequestedByService: c.RequestedByService,
		OrganizationID: c.OrganizationID, CallerReference: c.CallerReference, SupersedesVerificationID: c.SupersedesVerificationID,
		ReasonCode: c.ReasonCode, SubmittedAt: c.SubmittedAt, DecidedAt: c.DecidedAt, ExpiresAt: c.ExpiresAt, ExpiredAt: c.ExpiredAt,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

func toPublic(c db.VerificationCase) PublicCaseView {
	return PublicCaseView{VerificationID: c.VerificationID, VerificationType: c.VerificationType, Status: c.Status,
		SubmittedAt: c.SubmittedAt, DecidedAt: c.DecidedAt, ExpiresAt: c.ExpiresAt, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

// eventPayload is the payload of every verification.* event.
type eventPayload struct {
	VerificationID     ids.ID         `json:"verification_id"`
	VerificationType   string         `json:"verification_type"`
	Subject            domain.Subject `json:"subject"`
	Status             string         `json:"status"`
	RequestedByService string         `json:"requested_by_service"`
	Provider           string         `json:"provider,omitempty"`
	DecisionSource     string         `json:"decision_source,omitempty"`
	ReasonCode         string         `json:"reason_code,omitempty"`
	ReviewReason       string         `json:"review_reason,omitempty"`
	SubmittedAt        *time.Time     `json:"submitted_at,omitempty"`
	DecidedAt          *time.Time     `json:"decided_at,omitempty"`
	ExpiresAt          *time.Time     `json:"expires_at,omitempty"`
	ExpiredAt          *time.Time     `json:"expired_at,omitempty"`
	SupersedesID       *ids.ID        `json:"supersedes_verification_id,omitempty"`
}

// transition describes one audited state change.
type transition struct {
	to             domain.Status
	substatus      *domain.Substatus
	submittedAt    *time.Time
	decidedAt      *time.Time
	expiresAt      *time.Time
	expiredAt      *time.Time
	reasonCode     string
	decisionSource domain.DecisionSource
	reviewReason   domain.ReviewReason
	provider       string
	providerRef    string
	event          string
	actorType      string // audit actor type override (PROVIDER for callbacks)
}

// apply validates and persists a transition with history, audit and event.
func (s *Service) apply(ctx context.Context, tx pgx.Tx, c db.VerificationCase, t transition) (db.VerificationCase, error) {
	from := domain.Status(c.Status)
	if err := domain.Machine.Transition(from, t.to); err != nil {
		return c, err
	}
	now := s.now()
	var sub *string
	if t.substatus != nil {
		v := string(*t.substatus)
		sub = &v
	}
	var reason *string
	if t.reasonCode != "" {
		reason = &t.reasonCode
	}
	updated, err := s.q.UpdateCaseState(ctx, tx, db.UpdateCaseStateParams{
		Status: string(t.to), ProcessingSubstatus: sub, SubmittedAt: t.submittedAt, DecidedAt: t.decidedAt,
		ExpiresAt: t.expiresAt, ExpiredAt: t.expiredAt, ReasonCode: reason, UpdatedAt: now,
		VerificationID: c.VerificationID, ExpectedStatus: c.Status,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return c, apperrors.Conflict(apperrors.CodeConflict, "The verification changed concurrently; retry.")
	}
	if err != nil {
		return c, err
	}
	if err := s.history(ctx, tx, updated, &from, t, now); err != nil {
		return c, err
	}
	if err := s.auditTransition(ctx, tx, updated, string(from), t); err != nil {
		return c, err
	}
	if t.event != "" {
		if err := s.emit(ctx, tx, updated, t); err != nil {
			return c, err
		}
	}
	return updated, nil
}

func (s *Service) history(ctx context.Context, tx pgx.Tx, c db.VerificationCase, from *domain.Status, t transition, at time.Time) error {
	rc := requestcontext.From(ctx)
	p := db.InsertHistoryParams{HistoryID: ids.New(), VerificationID: c.VerificationID, ToStatus: c.Status, OccurredAt: at}
	if from != nil {
		f := string(*from)
		p.FromStatus = &f
	}
	p.Substatus = c.ProcessingSubstatus
	p.DecisionSource = strPtr(string(t.decisionSource))
	p.ReasonCode = strPtr(t.reasonCode)
	p.CallingService = strPtr(rc.CallingService)
	if !rc.Actor.IsZero() {
		p.ActorDomain, p.ActorID = strPtr(rc.Actor.Domain), &rc.Actor.ID
	}
	p.Provider = strPtr(t.provider)
	p.ProviderReference = strPtr(t.providerRef)
	p.RequestID = strPtr(rc.RequestID)
	p.TraceID = strPtr(rc.TraceID)
	return s.q.InsertHistory(ctx, tx, p)
}

func (s *Service) auditTransition(ctx context.Context, tx pgx.Tx, c db.VerificationCase, from string, t transition) error {
	md := map[string]any{
		"verification_type": c.VerificationType,
		"subject":           map[string]any{"subject_type": c.SubjectType, "subject_domain": c.SubjectDomain, "subject_id": c.SubjectID.String()},
		"requested_by":      c.RequestedByService,
	}
	if t.decisionSource != "" {
		md["decision_source"] = string(t.decisionSource)
	}
	if t.provider != "" {
		md["provider"] = t.provider
	}
	if t.providerRef != "" {
		md["provider_reference"] = t.providerRef
	}
	if t.reasonCode != "" {
		md["reason_code"] = t.reasonCode
	}
	if t.reviewReason != "" {
		md["review_reason"] = string(t.reviewReason)
	}
	_, err := s.audit.Record(ctx, tx, audit.Event{
		Action: "verification.transition", ActorType: t.actorType, TargetType: "verification", TargetID: c.VerificationID,
		Changes: map[string]any{"from": from, "to": c.Status}, Metadata: md,
	})
	return err
}

func (s *Service) emit(ctx context.Context, tx pgx.Tx, c db.VerificationCase, t transition) error {
	p := eventPayload{
		VerificationID: c.VerificationID, VerificationType: c.VerificationType, Status: c.Status,
		Subject:            domain.Subject{Type: domain.SubjectType(c.SubjectType), Domain: platform.Domain(c.SubjectDomain), ID: c.SubjectID},
		RequestedByService: c.RequestedByService, Provider: c.Provider, DecisionSource: string(t.decisionSource),
		ReasonCode: t.reasonCode, ReviewReason: string(t.reviewReason), SubmittedAt: c.SubmittedAt,
		DecidedAt: c.DecidedAt, ExpiresAt: c.ExpiresAt, ExpiredAt: c.ExpiredAt, SupersedesID: c.SupersedesVerificationID,
	}
	e, err := events.New(ctx, platform.VerificationService, t.event, "verification", c.VerificationID, p)
	if err != nil {
		return err
	}
	return s.outbox.Add(ctx, tx, e)
}

func (s *Service) lockCase(ctx context.Context, tx pgx.Tx, id ids.ID) (db.VerificationCase, error) {
	c, err := s.q.GetCaseForUpdate(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, domain.ErrNotFound()
	}
	return c, err
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func substatus(s domain.Substatus) *domain.Substatus { return &s }

func timePtr(t time.Time) *time.Time { return &t }

func metadataJSON(m map[string]string) []byte {
	if len(m) == 0 {
		return []byte("{}")
	}
	b, _ := json.Marshal(m)
	return b
}

func metadataMap(b []byte) map[string]string {
	m := map[string]string{}
	_ = json.Unmarshal(b, &m)
	return m
}
