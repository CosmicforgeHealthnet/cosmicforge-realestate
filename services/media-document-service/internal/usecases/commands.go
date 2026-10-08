package usecases

import (
	"context"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/storage"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// InitiateInput starts an upload.
type InitiateInput struct {
	Owner          domain.Owner
	Class          domain.Class
	Filename       string
	MimeType       string
	SizeBytes      int64
	ChecksumSHA256 string // lowercase hex
	Classification domain.Classification
	DocumentType   string
	Title          string
}

// UploadView is returned by initiation.
type UploadView struct {
	Media  MediaView         `json:"media"`
	Upload storage.SignedURL `json:"upload"`
}

var (
	hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	ownerType = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,39}$`)
	docType   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
)

// ValidateInitiate checks the request and that p may create media for the owner.
func (s *Service) ValidateInitiate(p domain.Principal, in *InitiateInput) error {
	if !platform.KnownDomain(string(in.Owner.Domain)) || !ownerType.MatchString(in.Owner.Type) || in.Owner.ID.ValidateV7() != nil {
		return apperrors.InvalidRequest("owner must have a known owner_domain, an uppercase owner_type and a UUIDv7 owner_id.")
	}
	policy, ok := s.opts.Policies[in.Class]
	if !ok {
		return apperrors.Invalid(domain.CodeInvalidClass, "media_class is not supported.")
	}
	if !hexSHA256.MatchString(in.ChecksumSHA256) {
		return apperrors.Invalid(domain.CodeInvalidChecksum, "checksum_sha256 must be the lowercase hex SHA-256 of the file.")
	}
	in.Filename = domain.SanitizeFilename(in.Filename)
	if in.Classification == "" {
		in.Classification = policy.Default
	}
	if err := policy.Validate(in.MimeType, in.Filename, in.SizeBytes, in.Classification); err != nil {
		return err
	}
	if in.Class == domain.ClassDocument || in.Class == domain.ClassVerificationDocument {
		if !docType.MatchString(in.DocumentType) {
			return apperrors.InvalidRequest("document_type is required for documents (uppercase identifier).")
		}
	}
	if len(in.Title) > 200 {
		return apperrors.InvalidRequest("title must be at most 200 characters.")
	}
	// Only the owner's own domain service (or the owning user via the
	// gateway) may create media for an owner.
	if p.Service == platform.APIGateway {
		if !p.HasActor() || p.ActorDomain != string(in.Owner.Domain) || in.Owner.Type != "USER" || in.Owner.ID != p.ActorID {
			return apperrors.PermissionDenied("Users may only upload files they own.")
		}
		return nil
	}
	d, ok := platform.DomainForService(p.Service)
	if !ok || d != in.Owner.Domain {
		return apperrors.PermissionDenied("A service may only create media owned by its own domain.").
			WithDetails(apperrors.Detail{Code: domain.CodeOwnerDomain, Message: "owner_domain must match the calling service's domain"})
	}
	return nil
}

// InitiateTx creates the media record (INITIATED), issues the signed upload
// URL and moves to UPLOAD_PENDING. Presigning is local computation (no I/O).
func (s *Service) InitiateTx(ctx context.Context, tx pgx.Tx, p domain.Principal, in InitiateInput) (UploadView, error) {
	now := s.now()
	id := ids.New()
	params := db.CreateMediaParams{
		MediaID: id, OwnerDomain: string(in.Owner.Domain), OwnerType: in.Owner.Type, OwnerID: in.Owner.ID,
		CreatedByService: string(p.Service), MediaClass: string(in.Class), Filename: in.Filename, MimeType: in.MimeType,
		SizeBytes: in.SizeBytes, ChecksumSha256: in.ChecksumSHA256, StorageKey: domain.StorageKey(string(in.Owner.Domain), in.Class, id),
		AccessClassification: string(in.Classification), CreatedAt: now,
	}
	if p.HasActor() {
		params.CreatedByActorDomain, params.CreatedByActorID = strPtr(p.ActorDomain), &p.ActorID
	}
	m, err := s.q.CreateMedia(ctx, tx, params)
	if err != nil {
		return UploadView{}, err
	}
	if _, err := s.audit.Record(ctx, tx, audit.Event{Action: "media.created", TargetType: "media", TargetID: id,
		Changes: map[string]any{"to": m.Status}, Metadata: map[string]any{"media_class": m.MediaClass, "mime_type": m.MimeType,
			"size_bytes": m.SizeBytes, "access_classification": m.AccessClassification,
			"owner": map[string]any{"owner_domain": m.OwnerDomain, "owner_type": m.OwnerType, "owner_id": m.OwnerID.String()}}}); err != nil {
		return UploadView{}, err
	}
	if in.DocumentType != "" {
		if err := s.q.InsertDocument(ctx, tx, db.InsertDocumentParams{MediaID: id, DocumentType: in.DocumentType, Title: strPtr(in.Title), CreatedAt: now}); err != nil {
			return UploadView{}, err
		}
	}
	sum, _ := hex.DecodeString(in.ChecksumSHA256)
	up, err := s.store.CreateUploadURL(ctx, storage.UploadRequest{Key: m.StorageKey, ContentType: m.MimeType,
		ContentLength: m.SizeBytes, ChecksumSHA256: sum, TTL: s.opts.UploadURLTTL})
	if err != nil {
		return UploadView{}, err
	}
	if _, err := s.q.CreateUploadSession(ctx, tx, db.CreateUploadSessionParams{UploadID: ids.New(), MediaID: id,
		ExpiresAt: now.Add(s.opts.UploadURLTTL), CreatedAt: now}); err != nil {
		return UploadView{}, err
	}
	m, err = s.transition(ctx, tx, m, change{to: domain.StatusUploadPending})
	if err != nil {
		return UploadView{}, err
	}
	return UploadView{Media: toView(m), Upload: up}, nil
}

// PrecheckComplete verifies (outside any transaction, after the idempotency
// key is claimed) that the object exists in storage with the declared size.
func (s *Service) PrecheckComplete(ctx context.Context, p domain.Principal, id ids.ID) error {
	m, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	if !s.canComplete(m, p) {
		return domain.ErrNotFound()
	}
	if m.Status != string(domain.StatusUploadPending) {
		return nil // already finalized (or later): CompleteTx returns current state
	}
	info, err := s.store.HeadObject(ctx, m.StorageKey)
	if errors.Is(err, storage.ErrNotFound) {
		return apperrors.Conflict(domain.CodeUploadIncomplete, "The file has not been uploaded yet.").WithOutcome(apperrors.OutcomeRetryable)
	}
	if err != nil {
		return apperrors.Unavailable("Object storage is unavailable.").WithCause(err)
	}
	if info.Size != m.SizeBytes {
		return apperrors.Unprocessable(domain.CodeUploadMismatch, "The uploaded file does not match the declared size.").WithOutcome(apperrors.OutcomeRetryable)
	}
	return nil
}

func (s *Service) canComplete(m db.MediaObject, p domain.Principal) bool {
	r := ref(m)
	if p.Service == platform.APIGateway {
		return p.HasActor() && p.ActorDomain == m.OwnerDomain && m.OwnerType == "USER" && p.ActorID == m.OwnerID
	}
	return domain.CanManage(r, p)
}

// CompleteTx finalizes an upload: UPLOAD_PENDING -> UPLOADED. Repeated or
// concurrent finalization returns the current state without a second event.
func (s *Service) CompleteTx(ctx context.Context, tx pgx.Tx, p domain.Principal, id ids.ID) (MediaView, error) {
	m, err := s.lock(ctx, tx, id)
	if err != nil {
		return MediaView{}, err
	}
	if !s.canComplete(m, p) {
		return MediaView{}, domain.ErrNotFound()
	}
	switch domain.Status(m.Status) {
	case domain.StatusUploadPending:
	case domain.StatusInitiated:
		return MediaView{}, apperrors.InvalidStateTransition(m.Status, string(domain.StatusUploaded))
	default:
		return toView(m), nil // duplicate finalization
	}
	sess, err := s.q.GetUploadSessionForUpdate(ctx, tx, id)
	if err != nil {
		return MediaView{}, err
	}
	now := s.now()
	if sess.Status == "EXPIRED" || (sess.Status == "OPEN" && sess.ExpiresAt.Before(now)) {
		return MediaView{}, apperrors.Conflict(domain.CodeUploadExpired, "The upload session expired. Start a new upload.")
	}
	if err := s.q.CompleteUploadSession(ctx, tx, db.CompleteUploadSessionParams{UploadID: sess.UploadID, CompletedAt: &now}); err != nil {
		return MediaView{}, err
	}
	if err := s.q.InsertStorageObject(ctx, tx, db.InsertStorageObjectParams{StorageKey: m.StorageKey, MediaID: id, Bucket: s.store.Bucket(),
		SizeBytes: m.SizeBytes, CreatedAt: now}); err != nil {
		return MediaView{}, err
	}
	m, err = s.transition(ctx, tx, m, change{to: domain.StatusUploaded, uploadedAt: &now, scanStatus: "PENDING", nextWorkAt: &now, event: EventUploadCompleted})
	return toView(m), err
}

// GetMetadata returns metadata visible to p.
func (s *Service) GetMetadata(ctx context.Context, p domain.Principal, id ids.ID) (MediaView, error) {
	m, err := s.get(ctx, id)
	if err != nil {
		return MediaView{}, err
	}
	if m.Status == string(domain.StatusDeleted) && !p.IsAdmin() {
		return MediaView{}, domain.ErrNotFound()
	}
	g, err := s.grants(ctx, s.db, id)
	if err != nil {
		return MediaView{}, err
	}
	visible := domain.CanReadMetadata(ref(m), p, g) || (p.Service == platform.APIGateway && s.canComplete(m, p))
	if !visible {
		return MediaView{}, domain.ErrNotFound()
	}
	return toView(m), nil
}

// StatusView is the lightweight lifecycle status.
type StatusView struct {
	MediaID         ids.ID    `json:"media_id"`
	Status          string    `json:"status"`
	ScanStatus      string    `json:"scan_status"`
	RejectionReason *string   `json:"rejection_reason"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// GetStatus returns lifecycle status visible to p.
func (s *Service) GetStatus(ctx context.Context, p domain.Principal, id ids.ID) (StatusView, error) {
	v, err := s.GetMetadata(ctx, p, id)
	if err != nil {
		return StatusView{}, err
	}
	return StatusView{MediaID: v.MediaID, Status: v.Status, ScanStatus: v.ScanStatus, RejectionReason: v.RejectionReason, UpdatedAt: v.UpdatedAt}, nil
}

// DownloadView is an issued download URL.
type DownloadView struct {
	MediaID   ids.ID    `json:"media_id"`
	URL       string    `json:"url"`
	Method    string    `json:"method"`
	ExpiresAt time.Time `json:"expires_at"`
}

// RequestDownload authorizes p and issues a short-lived signed URL.
// Authorization happens here; the signed URL itself carries no authority
// beyond its short lifetime. Privileged downloads are audited.
func (s *Service) RequestDownload(ctx context.Context, p domain.Principal, id ids.ID, purpose string) (DownloadView, error) {
	var res downloadResult
	err := database.WithTx(ctx, s.db, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		res, err = s.download(ctx, tx, p, id, purpose)
		return err
	})
	if err != nil {
		return DownloadView{}, err
	}
	// A denial is committed (with its audit record) and then reported.
	return res.view, res.denied
}

// downloadResult separates a committed business denial from a failure.
type downloadResult struct {
	view   DownloadView
	denied error
}

func (s *Service) download(ctx context.Context, tx pgx.Tx, p domain.Principal, id ids.ID, purpose string) (downloadResult, error) {
	m, err := s.lock(ctx, tx, id)
	if err != nil {
		return downloadResult{denied: err}, nil
	}
	g, err := s.grants(ctx, tx, id)
	if err != nil {
		return downloadResult{}, err
	}
	r := ref(m)
	allowed := domain.CanDownload(r, p, g)
	if !allowed {
		if _, err := s.audit.Record(ctx, tx, audit.Event{Action: "media.download_denied", TargetType: "media", TargetID: id, Result: audit.ResultDenied,
			Metadata: map[string]any{"access_classification": m.AccessClassification, "purpose": purpose}}); err != nil {
			return downloadResult{}, err
		}
		if domain.CanReadMetadata(r, p, g) || s.canComplete(m, p) {
			return downloadResult{denied: apperrors.PermissionDenied("You are not allowed to download this file.")}, nil
		}
		return downloadResult{denied: domain.ErrNotFound()}, nil
	}
	if m.Status != string(domain.StatusReady) {
		return downloadResult{denied: apperrors.Conflict(domain.CodeNotReady, "The file is not available for download.")}, nil
	}
	url, err := s.store.CreateDownloadURL(ctx, storage.DownloadRequest{Key: m.StorageKey, Filename: m.Filename, ContentType: m.MimeType, TTL: s.opts.DownloadURLTTL})
	if err != nil {
		return downloadResult{}, err
	}
	now := s.now()
	gp := db.InsertDownloadGrantParams{GrantID: ids.New(), MediaID: id, GranteeService: string(p.Service), Purpose: strPtr(purpose),
		ExpiresAt: now.Add(s.opts.DownloadURLTTL), CreatedAt: now}
	if p.HasActor() {
		gp.GranteeDomain, gp.GranteeActorID = strPtr(p.ActorDomain), &p.ActorID
	}
	if err := s.q.InsertDownloadGrant(ctx, tx, gp); err != nil {
		return downloadResult{}, err
	}
	if domain.Privileged(domain.Classification(m.AccessClassification)) {
		if _, err := s.audit.Record(ctx, tx, audit.Event{Action: "media.privileged_download", TargetType: "media", TargetID: id,
			Metadata: map[string]any{"access_classification": m.AccessClassification, "media_class": m.MediaClass, "purpose": purpose,
				"grant_id": gp.GrantID.String(), "expires_at": gp.ExpiresAt}}); err != nil {
			return downloadResult{}, err
		}
	}
	return downloadResult{view: DownloadView{MediaID: id, URL: url.URL, Method: url.Method, ExpiresAt: url.ExpiresAt}}, nil
}

// GrantInput is an explicit read grant.
type GrantInput struct {
	GranteeType string          `json:"grantee_type"` // SERVICE | ACTOR
	Service     string          `json:"service,omitempty"`
	Domain      platform.Domain `json:"domain,omitempty"`
	ActorID     ids.ID          `json:"actor_id,omitempty"`
}

// PolicyView is the effective access policy.
type PolicyView struct {
	MediaID              ids.ID       `json:"media_id"`
	AccessClassification string       `json:"access_classification"`
	Grants               []GrantInput `json:"grants"`
}

// SetPolicyTx replaces the access classification and grants (owner domain
// service or admin). Always audited.
func (s *Service) SetPolicyTx(ctx context.Context, tx pgx.Tx, p domain.Principal, id ids.ID, cls domain.Classification, grants []GrantInput) (PolicyView, error) {
	m, err := s.lock(ctx, tx, id)
	if err != nil {
		return PolicyView{}, err
	}
	if !domain.CanManage(ref(m), p) {
		return PolicyView{}, domain.ErrNotFound()
	}
	if m.Status == string(domain.StatusDeleted) || m.Status == string(domain.StatusDeletePending) {
		return PolicyView{}, apperrors.Conflict(domain.CodeNotReady, "The media is being deleted.")
	}
	allowed := false
	for _, c := range s.opts.Policies[domain.Class(m.MediaClass)].Classifications {
		if c == cls {
			allowed = true
		}
	}
	if !allowed {
		return PolicyView{}, apperrors.Unprocessable(domain.CodeClassificationNotAllowed, "This access classification is not allowed for this media class.")
	}
	if len(grants) > 50 {
		return PolicyView{}, apperrors.InvalidRequest("Too many grants.")
	}
	before, err := s.grants(ctx, tx, id)
	if err != nil {
		return PolicyView{}, err
	}
	now := s.now()
	if _, err := s.q.RevokeAllGrants(ctx, tx, db.RevokeAllGrantsParams{MediaID: id, RevokedAt: &now}); err != nil {
		return PolicyView{}, err
	}
	out := PolicyView{MediaID: id, AccessClassification: string(cls), Grants: []GrantInput{}}
	for _, g := range grants {
		params := db.InsertGrantParams{PolicyID: ids.New(), MediaID: id, GranteeType: g.GranteeType, GrantedByService: string(p.Service), CreatedAt: now}
		switch g.GranteeType {
		case "SERVICE":
			if !platform.KnownService(g.Service) {
				return PolicyView{}, apperrors.InvalidRequest("grant service must be a known platform service.")
			}
			params.GranteeService = &g.Service
		case "ACTOR":
			if !platform.KnownDomain(string(g.Domain)) || g.ActorID.ValidateV7() != nil {
				return PolicyView{}, apperrors.InvalidRequest("actor grants need a known domain and a UUIDv7 actor_id.")
			}
			d := string(g.Domain)
			params.GranteeDomain, params.GranteeActorID = &d, &g.ActorID
		default:
			return PolicyView{}, apperrors.InvalidRequest("grantee_type must be SERVICE or ACTOR.")
		}
		if p.HasActor() {
			params.GrantedByActorDomain, params.GrantedByActorID = strPtr(p.ActorDomain), &p.ActorID
		}
		if _, err := s.q.InsertGrant(ctx, tx, params); err != nil {
			return PolicyView{}, err
		}
		out.Grants = append(out.Grants, g)
	}
	if _, err := s.q.SetAccessClassification(ctx, tx, db.SetAccessClassificationParams{MediaID: id, AccessClassification: string(cls), UpdatedAt: now}); err != nil {
		return PolicyView{}, err
	}
	if _, err := s.audit.Record(ctx, tx, audit.Event{Action: "media.access_policy_changed", TargetType: "media", TargetID: id,
		Changes:  map[string]any{"from": m.AccessClassification, "to": string(cls), "grants_before": len(before), "grants_after": len(out.Grants)},
		Metadata: map[string]any{"grants": out.Grants}}); err != nil {
		return PolicyView{}, err
	}
	return out, nil
}

// RequestDeletionTx moves READY/REJECTED media to DELETE_PENDING, honouring
// retention rules. The worker removes the object and completes deletion.
func (s *Service) RequestDeletionTx(ctx context.Context, tx pgx.Tx, p domain.Principal, id ids.ID, reason string) (MediaView, error) {
	m, err := s.lock(ctx, tx, id)
	if err != nil {
		return MediaView{}, err
	}
	if !domain.CanManage(ref(m), p) {
		return MediaView{}, domain.ErrNotFound()
	}
	if m.Status == string(domain.StatusDeletePending) || m.Status == string(domain.StatusDeleted) {
		return toView(m), nil
	}
	now := s.now()
	if m.Status == string(domain.StatusReady) && m.RetentionUntil != nil && m.RetentionUntil.After(now) {
		return MediaView{}, apperrors.Conflict(domain.CodeRetentionActive, "The file is within its mandatory retention period.").
			WithDetails(apperrors.Detail{Field: "retention_until", Code: "RETENTION", Message: m.RetentionUntil.Format(time.RFC3339)})
	}
	return s.markDeletePending(ctx, tx, m, string(p.Service), p, reason, "")
}

func (s *Service) markDeletePending(ctx context.Context, tx pgx.Tx, m db.MediaObject, by string, p domain.Principal, reason, actorType string) (MediaView, error) {
	now := s.now()
	dp := db.CreateDeletionRequestParams{RequestID: ids.New(), MediaID: m.MediaID, Reason: reason, RequestedByService: by, CreatedAt: now}
	if p.HasActor() {
		dp.RequestedByActorDomain, dp.RequestedByActorID = strPtr(p.ActorDomain), &p.ActorID
	}
	if _, err := s.q.CreateDeletionRequest(ctx, tx, dp); err != nil {
		return MediaView{}, err
	}
	m, err := s.transition(ctx, tx, m, change{to: domain.StatusDeletePending, nextWorkAt: &now, actorType: actorType,
		metadata: map[string]any{"deletion_reason": reason}})
	return toView(m), err
}

type mediaCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        ids.ID    `json:"i"`
}

// ListByOwner lists an owner's media (owner domain service or admin).
func (s *Service) ListByOwner(ctx context.Context, p domain.Principal, owner domain.Owner, cursor string, limit int) (pagination.Response[MediaView], error) {
	if !domain.CanManage(domain.MediaRef{Owner: owner}, p) {
		return pagination.Build[MediaView](nil, limit, nil)
	}
	var cur mediaCursor
	has, err := pagination.Decode(cursor, &cur)
	if err != nil {
		return pagination.Response[MediaView]{}, err
	}
	params := db.ListMediaByOwnerParams{OwnerDomain: string(owner.Domain), OwnerType: owner.Type, OwnerID: owner.ID, RowLimit: int32(limit + 1)}
	if has {
		params.CursorCreatedAt, params.CursorID = &cur.CreatedAt, &cur.ID
	}
	rows, err := s.q.ListMediaByOwner(ctx, s.db, params)
	if err != nil {
		return pagination.Response[MediaView]{}, err
	}
	views := make([]MediaView, 0, len(rows))
	for _, r := range rows {
		views = append(views, toView(r))
	}
	return pagination.Build(views, limit, func(v MediaView) any { return mediaCursor{CreatedAt: v.CreatedAt, ID: v.MediaID} })
}
