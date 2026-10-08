package usecases

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// CreateInput creates a DRAFT case.
type CreateInput struct {
	Type             domain.Type
	Subject          domain.Subject
	Provider         string
	CallerReference  string
	ProviderMetadata map[string]string
}

var metaKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ValidateCreate checks input shape and the caller allow-list.
func (s *Service) ValidateCreate(caller platform.Service, in CreateInput) error {
	if !domain.ValidType(in.Type) {
		return apperrors.Invalid(domain.CodeInvalidType, "verification_type is not supported.")
	}
	if !domain.SupportsSubject(in.Type, in.Subject.Type) {
		return apperrors.Unprocessable(domain.CodeUnsupportedSubject, "subject_type is not supported for this verification type.")
	}
	if len(in.ProviderMetadata) > 16 {
		return apperrors.InvalidRequest("provider_metadata has too many entries.")
	}
	for k, v := range in.ProviderMetadata {
		if !metaKey.MatchString(k) || len(v) > 256 {
			return apperrors.InvalidRequest("provider_metadata keys must be lowercase identifiers and values at most 256 characters.")
		}
	}
	if err := domain.CanInitiate(caller, in.Type, in.Subject); err != nil {
		return err
	}
	if _, err := s.providers.Select(in.Provider, in.Type); err != nil {
		return err
	}
	return nil
}

// CreateTx inserts a DRAFT case. Call ValidateCreate first.
func (s *Service) CreateTx(ctx context.Context, tx pgx.Tx, caller platform.Service, in CreateInput, supersedes *ids.ID) (CaseView, error) {
	p, err := s.providers.Select(in.Provider, in.Type)
	if err != nil {
		return CaseView{}, err
	}
	existing, err := s.q.FindOpenCase(ctx, tx, db.FindOpenCaseParams{
		VerificationType: string(in.Type), SubjectType: string(in.Subject.Type), SubjectDomain: string(in.Subject.Domain), SubjectID: in.Subject.ID,
	})
	if err == nil {
		return CaseView{}, domain.ErrAlreadyOpen(existing.VerificationID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CaseView{}, err
	}
	rc := requestcontext.From(ctx)
	params := db.CreateCaseParams{
		VerificationID: ids.New(), VerificationType: string(in.Type), SubjectType: string(in.Subject.Type),
		SubjectDomain: string(in.Subject.Domain), SubjectID: in.Subject.ID, Provider: p.Name(),
		RequestedByService: string(caller), SupersedesVerificationID: supersedes,
		CallerReference: strPtr(in.CallerReference), ProviderMetadata: metadataJSON(in.ProviderMetadata), CreatedAt: s.now(),
	}
	if !rc.Actor.IsZero() {
		params.RequestedByActorDomain, params.RequestedByActorID = strPtr(rc.Actor.Domain), &rc.Actor.ID
	}
	if !rc.OrganizationID.IsZero() {
		params.OrganizationID = &rc.OrganizationID
	}
	c, err := s.q.CreateCase(ctx, tx, params)
	if database.IsUniqueViolation(err, "verification_cases_open_uniq") {
		return CaseView{}, apperrors.Conflict(domain.CodeAlreadyOpen, "An open verification already exists for this subject and type.")
	}
	if err != nil {
		return CaseView{}, err
	}
	if err := s.history(ctx, tx, c, nil, transition{to: domain.StatusDraft}, c.CreatedAt); err != nil {
		return CaseView{}, err
	}
	md := map[string]any{"verification_type": c.VerificationType, "provider": c.Provider,
		"subject": map[string]any{"subject_type": c.SubjectType, "subject_domain": c.SubjectDomain, "subject_id": c.SubjectID.String()}}
	if supersedes != nil {
		md["supersedes_verification_id"] = supersedes.String()
	}
	if _, err := s.audit.Record(ctx, tx, audit.Event{Action: "verification.created", TargetType: "verification",
		TargetID: c.VerificationID, Changes: map[string]any{"to": c.Status}, Metadata: md}); err != nil {
		return CaseView{}, err
	}
	return toView(c), nil
}

// EvidenceInput attaches a media reference.
type EvidenceInput struct {
	MediaID      ids.ID
	EvidenceType string
}

var evidenceTypePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// PrecheckEvidence verifies (outside any transaction) that the media exists
// and belongs to the case subject's domain. It returns the media checksum.
func (s *Service) PrecheckEvidence(ctx context.Context, caller platform.Service, id ids.ID, in EvidenceInput) (string, error) {
	if !evidenceTypePattern.MatchString(in.EvidenceType) {
		return "", apperrors.InvalidRequest("evidence_type must be an uppercase identifier.")
	}
	c, err := s.q.GetCase(ctx, s.db, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !domain.CanRead(caller, platform.Domain(c.SubjectDomain), c.RequestedByService)) {
		return "", domain.ErrNotFound()
	}
	if err != nil {
		return "", err
	}
	info, err := s.mediaInfo(ctx, in.MediaID)
	if err != nil {
		return "", err
	}
	if info.OwnerDomain != c.SubjectDomain {
		return "", apperrors.Unprocessable(domain.CodeEvidenceOwnerMismatch, "The evidence file does not belong to the subject's domain.")
	}
	return info.ChecksumSHA256, nil
}

func (s *Service) mediaInfo(ctx context.Context, mediaID ids.ID) (media.Info, error) {
	info, err := s.media.Get(ctx, mediaID)
	if err != nil {
		var re *httpx.RemoteError
		if errors.As(err, &re) && re.Status == http.StatusNotFound {
			return info, apperrors.Unprocessable(domain.CodeEvidenceNotFound, "The evidence file does not exist.")
		}
		return info, httpx.ToAppError(err)
	}
	return info, nil
}

// AttachEvidenceTx records an evidence reference on a DRAFT case.
func (s *Service) AttachEvidenceTx(ctx context.Context, tx pgx.Tx, caller platform.Service, id ids.ID, in EvidenceInput, checksum string) (EvidenceView, error) {
	c, err := s.lockCase(ctx, tx, id)
	if err != nil {
		return EvidenceView{}, err
	}
	if c.RequestedByService != string(caller) {
		return EvidenceView{}, apperrors.PermissionDenied("Only the requesting service may attach evidence.")
	}
	if c.Status != string(domain.StatusDraft) {
		return EvidenceView{}, apperrors.Conflict(domain.CodeEvidenceLocked, "Evidence can only be attached while the verification is DRAFT.")
	}
	ev, err := s.q.AddEvidence(ctx, tx, db.AddEvidenceParams{
		EvidenceID: ids.New(), VerificationID: id, MediaID: in.MediaID, EvidenceType: in.EvidenceType,
		MediaChecksumSha256: strPtr(checksum), AttachedByService: string(caller), CreatedAt: s.now(),
	})
	if errors.Is(err, pgx.ErrNoRows) { // already attached: idempotent
		ev, err = s.q.GetEvidenceByMedia(ctx, tx, db.GetEvidenceByMediaParams{VerificationID: id, MediaID: in.MediaID})
		if err != nil {
			return EvidenceView{}, err
		}
		return EvidenceView{EvidenceID: ev.EvidenceID, MediaID: ev.MediaID, EvidenceType: ev.EvidenceType, CreatedAt: ev.CreatedAt}, nil
	}
	if err != nil {
		return EvidenceView{}, err
	}
	if _, err := s.audit.Record(ctx, tx, audit.Event{Action: "verification.evidence_attached", TargetType: "verification",
		TargetID: id, Metadata: map[string]any{"media_id": in.MediaID.String(), "evidence_type": in.EvidenceType, "media_checksum_sha256": checksum}}); err != nil {
		return EvidenceView{}, err
	}
	return EvidenceView{EvidenceID: ev.EvidenceID, MediaID: ev.MediaID, EvidenceType: ev.EvidenceType, CreatedAt: ev.CreatedAt}, nil
}

// PrecheckSubmit verifies (outside any transaction) that every evidence file
// is READY in media-document-service. It returns the number checked.
func (s *Service) PrecheckSubmit(ctx context.Context, caller platform.Service, id ids.ID) (int, error) {
	c, err := s.q.GetCase(ctx, s.db, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !domain.CanRead(caller, platform.Domain(c.SubjectDomain), c.RequestedByService)) {
		return 0, domain.ErrNotFound()
	}
	if err != nil {
		return 0, err
	}
	if c.Status != string(domain.StatusDraft) {
		return 0, domain.Machine.Transition(domain.Status(c.Status), domain.StatusSubmitted)
	}
	evs, err := s.q.ListEvidence(ctx, s.db, id)
	if err != nil {
		return 0, err
	}
	if len(evs) == 0 {
		return 0, apperrors.Unprocessable(domain.CodeEvidenceRequired, "At least one evidence file is required before submission.")
	}
	for _, ev := range evs {
		info, err := s.mediaInfo(ctx, ev.MediaID)
		if err != nil {
			return 0, err
		}
		if info.Status != media.StatusReady {
			return 0, apperrors.Unprocessable(domain.CodeEvidenceNotReady, "Evidence files must finish scanning before submission.").
				WithDetails(apperrors.Detail{Field: "media_id", Code: info.Status, Message: ev.MediaID.String()})
		}
		if info.OwnerDomain != c.SubjectDomain {
			return 0, apperrors.Unprocessable(domain.CodeEvidenceOwnerMismatch, "The evidence file does not belong to the subject's domain.")
		}
	}
	return len(evs), nil
}

// SubmitTx moves DRAFT -> SUBMITTED. checked is the evidence count verified
// by PrecheckSubmit; a mismatch means evidence changed concurrently.
func (s *Service) SubmitTx(ctx context.Context, tx pgx.Tx, caller platform.Service, id ids.ID, checked int) (CaseView, error) {
	c, err := s.lockCase(ctx, tx, id)
	if err != nil {
		return CaseView{}, err
	}
	if c.RequestedByService != string(caller) {
		return CaseView{}, apperrors.PermissionDenied("Only the requesting service may submit this verification.")
	}
	evs, err := s.q.ListEvidence(ctx, tx, id)
	if err != nil {
		return CaseView{}, err
	}
	if len(evs) != checked {
		return CaseView{}, apperrors.Conflict(apperrors.CodeConflict, "Evidence changed during submission; retry.")
	}
	now := s.now()
	updated, err := s.apply(ctx, tx, c, transition{to: domain.StatusSubmitted, submittedAt: &now, event: EventSubmitted})
	return toView(updated), err
}

// RequestProcessingTx moves SUBMITTED -> PROCESSING and queues a provider
// attempt. The provider is called later by the dispatcher, never here.
func (s *Service) RequestProcessingTx(ctx context.Context, tx pgx.Tx, caller platform.Service, id ids.ID) (CaseView, error) {
	c, err := s.lockCase(ctx, tx, id)
	if err != nil {
		return CaseView{}, err
	}
	if c.RequestedByService != string(caller) && caller != platform.AdminService {
		return CaseView{}, apperrors.PermissionDenied("Only the requesting service may request processing.")
	}
	if _, ok := s.providers.Get(c.Provider); !ok {
		return CaseView{}, domain.ErrProviderUnavailable()
	}
	updated, err := s.apply(ctx, tx, c, transition{to: domain.StatusProcessing, substatus: substatus(domain.SubstatusAwaitingProvider),
		provider: c.Provider, event: EventProcessing})
	if err != nil {
		return CaseView{}, err
	}
	no, err := s.q.NextAttemptNo(ctx, tx, id)
	if err != nil {
		return CaseView{}, err
	}
	attemptID := ids.New()
	if _, err := s.q.CreateAttempt(ctx, tx, db.CreateAttemptParams{
		AttemptID: attemptID, VerificationID: id, AttemptNo: no, Provider: c.Provider,
		ClientReference: "cfv_" + attemptID.String(), NextAttemptAt: s.now(),
	}); err != nil {
		return CaseView{}, err
	}
	return toView(updated), nil
}

// RequireAdminActor checks the admin-service delegation context.
func RequireAdminActor(ctx context.Context) (requestcontext.Actor, error) {
	rc := requestcontext.From(ctx)
	if rc.CallingService != string(platform.AdminService) || rc.Actor.Domain != string(platform.DomainAdmin) || rc.Actor.ID.IsZero() {
		return requestcontext.Actor{}, apperrors.PermissionDenied("A delegated admin actor is required.").
			WithDetails(apperrors.Detail{Code: domain.CodeAdminActorRequired, Message: "admin-service with an authenticated admin actor is required"})
	}
	return rc.Actor, nil
}

// ExpireTx force-expires a VERIFIED case (admin-authorized).
func (s *Service) ExpireTx(ctx context.Context, tx pgx.Tx, id ids.ID, reasonCode string) (CaseView, error) {
	if _, err := RequireAdminActor(ctx); err != nil {
		return CaseView{}, err
	}
	c, err := s.lockCase(ctx, tx, id)
	if err != nil {
		return CaseView{}, err
	}
	now := s.now()
	updated, err := s.apply(ctx, tx, c, transition{to: domain.StatusExpired, expiredAt: &now, reasonCode: reasonCode,
		decisionSource: domain.SourceAdmin, event: EventExpired})
	return toView(updated), err
}

// ExpireDue expires VERIFIED cases past expires_at (worker). Explicit, audited
// transitions — never a silent mutation.
func (s *Service) ExpireDue(ctx context.Context, batch int) (int, error) {
	ctx = requestcontext.Job(ctx, "verification-expiry")
	n := 0
	err := database.WithTx(ctx, s.db, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		n = 0
		cases, err := s.q.ClaimExpiredCases(ctx, tx, db.ClaimExpiredCasesParams{Now: s.now(), RowLimit: int32(batch)})
		if err != nil {
			return err
		}
		for _, c := range cases {
			now := s.now()
			if _, err := s.apply(ctx, tx, c, transition{to: domain.StatusExpired, expiredAt: &now, reasonCode: "VALIDITY_ELAPSED",
				decisionSource: domain.SourceSystemExpiry, event: EventExpired, actorType: audit.ActorSystem}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// ReverifyTx creates a new DRAFT case superseding a decided/expired one.
func (s *Service) ReverifyTx(ctx context.Context, tx pgx.Tx, caller platform.Service, id ids.ID, in CreateInput) (CaseView, error) {
	old, err := s.lockCase(ctx, tx, id)
	if err != nil {
		return CaseView{}, err
	}
	switch domain.Status(old.Status) {
	case domain.StatusVerified, domain.StatusExpired, domain.StatusRejected:
	default:
		return CaseView{}, apperrors.Conflict(domain.CodeReverifyNotAllowed, "Re-verification requires a decided or expired verification.")
	}
	in.Type = domain.Type(old.VerificationType)
	in.Subject = domain.Subject{Type: domain.SubjectType(old.SubjectType), Domain: platform.Domain(old.SubjectDomain), ID: old.SubjectID}
	if in.Provider == "" {
		in.Provider = old.Provider
	}
	if in.ProviderMetadata == nil {
		in.ProviderMetadata = metadataMap(old.ProviderMetadata)
	}
	if err := s.ValidateCreate(caller, in); err != nil {
		return CaseView{}, err
	}
	return s.CreateTx(ctx, tx, caller, in, &old.VerificationID)
}
