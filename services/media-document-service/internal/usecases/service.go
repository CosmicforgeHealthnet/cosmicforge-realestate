// Package usecases implements media-document-service behaviour. Object
// storage and scanner calls never run inside a database transaction; every
// lifecycle transition is audited and catalogued transitions emit events via
// the transactional outbox.
package usecases

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/scanning"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/storage"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// Events published by media-document-service.
const (
	EventUploadCompleted = "media.upload_completed.v1"
	EventScanning        = "media.scanning.v1"
	EventReady           = "media.ready.v1"
	EventRejected        = "media.rejected.v1"
	EventDeleted         = "media.deleted.v1"
)

// Options configures behaviour.
type Options struct {
	Policies       map[domain.Class]domain.ClassPolicy
	UploadURLTTL   time.Duration
	DownloadURLTTL time.Duration
	ScanLease      time.Duration // visibility timeout of a scan in progress
	MaxScanTries   int
	Now            func() time.Time
}

func (o *Options) defaults() {
	if o.Policies == nil {
		o.Policies = domain.DefaultPolicies()
	}
	if o.UploadURLTTL <= 0 {
		o.UploadURLTTL = 15 * time.Minute
	}
	if o.DownloadURLTTL <= 0 {
		o.DownloadURLTTL = 5 * time.Minute
	}
	if o.ScanLease <= 0 {
		o.ScanLease = 5 * time.Minute
	}
	if o.MaxScanTries <= 0 {
		o.MaxScanTries = 5
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

// Service is the media application service.
type Service struct {
	db      database.DB
	q       *db.Queries
	outbox  *outbox.Store
	audit   *audit.Recorder
	store   storage.Storage
	scanner scanning.Scanner
	opts    Options
	log     *slog.Logger
}

// New wires the service.
func New(pool database.DB, ob *outbox.Store, st storage.Storage, sc scanning.Scanner, opts Options, log *slog.Logger) *Service {
	opts.defaults()
	if log == nil {
		log = slog.Default()
	}
	return &Service{db: pool, q: db.New(), outbox: ob, audit: audit.NewRecorder(string(platform.MediaDocumentService)),
		store: st, scanner: sc, opts: opts, log: log}
}

func (s *Service) now() time.Time { return s.opts.Now().UTC().Truncate(time.Microsecond) }

// PrincipalFrom builds the access principal from the authenticated context.
func PrincipalFrom(ctx context.Context) domain.Principal {
	p, _ := serviceauth.PrincipalFrom(ctx)
	out := domain.Principal{Service: p.Service}
	if p.Delegation.HasActor() {
		out.ActorDomain, out.ActorID = string(p.Delegation.ActorDomain), p.Delegation.ActorID
	}
	return out
}

// MediaView is the API representation of a media object.
type MediaView struct {
	MediaID              ids.ID     `json:"media_id"`
	OwnerDomain          string     `json:"owner_domain"`
	OwnerType            string     `json:"owner_type"`
	OwnerID              ids.ID     `json:"owner_id"`
	MediaClass           string     `json:"media_class"`
	Filename             string     `json:"filename"`
	MimeType             string     `json:"mime_type"`
	SizeBytes            int64      `json:"size_bytes"`
	ChecksumSHA256       string     `json:"checksum_sha256"`
	AccessClassification string     `json:"access_classification"`
	ScanStatus           string     `json:"scan_status"`
	Status               string     `json:"status"`
	RejectionReason      *string    `json:"rejection_reason"`
	RetentionUntil       *time.Time `json:"retention_until"`
	UploadedAt           *time.Time `json:"uploaded_at"`
	ReadyAt              *time.Time `json:"ready_at"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

func toView(m db.MediaObject) MediaView {
	return MediaView{
		MediaID: m.MediaID, OwnerDomain: m.OwnerDomain, OwnerType: m.OwnerType, OwnerID: m.OwnerID,
		MediaClass: m.MediaClass, Filename: m.Filename, MimeType: m.MimeType, SizeBytes: m.SizeBytes, ChecksumSHA256: m.ChecksumSha256,
		AccessClassification: m.AccessClassification, ScanStatus: m.ScanStatus, Status: m.Status, RejectionReason: m.RejectionReason,
		RetentionUntil: m.RetentionUntil, UploadedAt: m.UploadedAt, ReadyAt: m.ReadyAt, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

func ref(m db.MediaObject) domain.MediaRef {
	return domain.MediaRef{Owner: domain.Owner{Domain: platform.Domain(m.OwnerDomain), Type: m.OwnerType, ID: m.OwnerID},
		Class: domain.Class(m.MediaClass), Classification: domain.Classification(m.AccessClassification)}
}

type eventPayload struct {
	MediaID              ids.ID       `json:"media_id"`
	Owner                domain.Owner `json:"owner"`
	MediaClass           string       `json:"media_class"`
	Status               string       `json:"status"`
	MimeType             string       `json:"mime_type"`
	SizeBytes            int64        `json:"size_bytes"`
	ChecksumSHA256       string       `json:"checksum_sha256"`
	AccessClassification string       `json:"access_classification"`
	ScanStatus           string       `json:"scan_status"`
	RejectionReason      string       `json:"rejection_reason,omitempty"`
	UploadedAt           *time.Time   `json:"uploaded_at,omitempty"`
	ReadyAt              *time.Time   `json:"ready_at,omitempty"`
	DeletedAt            *time.Time   `json:"deleted_at,omitempty"`
}

// change is one audited lifecycle transition.
type change struct {
	to           domain.Status
	scanStatus   string
	detectedMime string
	reason       string
	retention    *time.Time
	uploadedAt   *time.Time
	readyAt      *time.Time
	rejectedAt   *time.Time
	deletedAt    *time.Time
	nextWorkAt   *time.Time
	event        string
	actorType    string
	metadata     map[string]any
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func timePtr(t time.Time) *time.Time { return &t }

func (s *Service) transition(ctx context.Context, tx pgx.Tx, m db.MediaObject, c change) (db.MediaObject, error) {
	from := domain.Status(m.Status)
	if err := domain.Machine.Transition(from, c.to); err != nil {
		return m, err
	}
	updated, err := s.q.UpdateMediaState(ctx, tx, db.UpdateMediaStateParams{
		Status: string(c.to), ScanStatus: strPtr(c.scanStatus), DetectedMimeType: strPtr(c.detectedMime), RejectionReason: strPtr(c.reason),
		RetentionUntil: c.retention, UploadedAt: c.uploadedAt, ReadyAt: c.readyAt, RejectedAt: c.rejectedAt, DeletedAt: c.deletedAt,
		NextWorkAt: c.nextWorkAt, UpdatedAt: s.now(), MediaID: m.MediaID, ExpectedStatus: m.Status,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return m, apperrors.Conflict(apperrors.CodeConflict, "The media changed concurrently; retry.")
	}
	if err != nil {
		return m, err
	}
	md := map[string]any{"owner": map[string]any{"owner_domain": m.OwnerDomain, "owner_type": m.OwnerType, "owner_id": m.OwnerID.String()},
		"media_class": m.MediaClass, "access_classification": m.AccessClassification}
	if c.reason != "" {
		md["reason"] = c.reason
	}
	for k, v := range c.metadata {
		md[k] = v
	}
	if _, err := s.audit.Record(ctx, tx, audit.Event{Action: "media.transition", ActorType: c.actorType, TargetType: "media",
		TargetID: m.MediaID, Changes: map[string]any{"from": string(from), "to": string(c.to)}, Metadata: md}); err != nil {
		return m, err
	}
	if c.event != "" {
		p := eventPayload{MediaID: updated.MediaID, Owner: domain.Owner{Domain: platform.Domain(updated.OwnerDomain), Type: updated.OwnerType, ID: updated.OwnerID},
			MediaClass: updated.MediaClass, Status: updated.Status, MimeType: updated.MimeType, SizeBytes: updated.SizeBytes,
			ChecksumSHA256: updated.ChecksumSha256, AccessClassification: updated.AccessClassification, ScanStatus: updated.ScanStatus,
			UploadedAt: updated.UploadedAt, ReadyAt: updated.ReadyAt, DeletedAt: updated.DeletedAt}
		if updated.RejectionReason != nil {
			p.RejectionReason = *updated.RejectionReason
		}
		e, err := events.New(ctx, platform.MediaDocumentService, c.event, "media", updated.MediaID, p)
		if err != nil {
			return m, err
		}
		if err := s.outbox.Add(ctx, tx, e); err != nil {
			return m, err
		}
	}
	return updated, nil
}

func (s *Service) lock(ctx context.Context, tx pgx.Tx, id ids.ID) (db.MediaObject, error) {
	m, err := s.q.GetMediaForUpdate(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, domain.ErrNotFound()
	}
	return m, err
}

func (s *Service) get(ctx context.Context, id ids.ID) (db.MediaObject, error) {
	m, err := s.q.GetMedia(ctx, s.db, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, domain.ErrNotFound()
	}
	return m, err
}

func (s *Service) grants(ctx context.Context, q database.Querier, id ids.ID) ([]domain.Grant, error) {
	rows, err := s.q.ListActiveGrants(ctx, q, id)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Grant, 0, len(rows))
	for _, r := range rows {
		g := domain.Grant{IsActor: r.GranteeType == "ACTOR"}
		if r.GranteeService != nil {
			g.Service = *r.GranteeService
		}
		if r.GranteeDomain != nil {
			g.Domain = *r.GranteeDomain
		}
		if r.GranteeActorID != nil {
			g.ActorID = *r.GranteeActorID
		}
		out = append(out, g)
	}
	return out, nil
}

func jobCtx(ctx context.Context, name string) context.Context { return requestcontext.Job(ctx, name) }
