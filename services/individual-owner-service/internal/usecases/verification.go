package usecases

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// verification-service owns IDENTITY and PROPERTY_AUTHORITY decisions
// (§13.2, §13.4, §13.6). The owner service creates cases, attaches media
// evidence by ID, and keeps verification_links as a projection updated from
// verification.* events and read-back reconciliation.

// VerificationLinkView is the owner-facing verification projection.
type VerificationLinkView struct {
	VerificationID   ids.ID     `json:"verification_id"`
	VerificationType string     `json:"verification_type"`
	Status           string     `json:"status"`
	ReasonCode       *string    `json:"reason_code"`
	DecidedAt        *time.Time `json:"decided_at"`
	ExpiresAt        *time.Time `json:"expires_at"`
	LastSyncedAt     time.Time  `json:"last_synced_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

func linkView(l db.VerificationLink) VerificationLinkView {
	return VerificationLinkView{VerificationID: l.VerificationID, VerificationType: l.VerificationType, Status: l.Status, ReasonCode: l.ReasonCode,
		DecidedAt: l.DecidedAt, ExpiresAt: l.ExpiresAt, LastSyncedAt: l.LastSyncedAt, CreatedAt: l.CreatedAt}
}

// EvidenceInput is one media evidence reference for verification.
type EvidenceInput struct {
	MediaID      ids.ID `json:"media_id" v:"required"`
	EvidenceType string `json:"evidence_type" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}" ex:"ID_DOCUMENT_FRONT"`
}

// IdentityVerificationInput starts an IDENTITY verification.
type IdentityVerificationInput struct {
	Evidence []EvidenceInput `json:"evidence" v:"required,max=10"`
}

// StartedCase is the downstream case created in the pre-transaction phase.
type StartedCase struct {
	Case     verification.Case
	Evidence map[ids.ID]ids.ID // media_id -> evidence_id
}

// StartIdentityVerification creates, fills and submits an IDENTITY case
// (downstream calls only; called before the transaction).
func (s *Service) StartIdentityVerification(ctx context.Context, subj authz.Subject, in IdentityVerificationInput, key func(string) string) (StartedCase, error) {
	if err := schema.Validate(in); err != nil {
		return StartedCase{}, err
	}
	if _, err := s.authorize(ctx, subj, authz.ActIdentityVerify, ids.Nil); err != nil {
		return StartedCase{}, err
	}
	if s.Verification == nil {
		return StartedCase{}, unavailable("Identity verification", "verification-service")
	}
	acc := accountOf(subj)
	cur, err := s.q.LatestVerificationLink(ctx, s.Pool, db.LatestVerificationLinkParams{SubjectType: "USER", SubjectID: acc, VerificationType: "IDENTITY"})
	if err == nil && slices.Contains([]string{"SUBMITTED", "PROCESSING", "MANUAL_REVIEW"}, cur.Status) {
		return StartedCase{}, conflict("VERIFICATION_IN_PROGRESS", "An identity verification is already in progress.")
	}
	for _, e := range in.Evidence {
		if _, err := s.ownedMedia(ctx, e.MediaID, []ids.ID{acc}, "VERIFICATION_DOCUMENT", "IMAGE", "DOCUMENT"); err != nil {
			return StartedCase{}, err
		}
	}
	docs := make([]DocumentInput, 0, len(in.Evidence))
	for _, e := range in.Evidence {
		docs = append(docs, DocumentInput{MediaID: e.MediaID, DocumentType: e.EvidenceType})
	}
	return s.runCase(ctx, verification.TypeIdentity, verification.Subject{Type: verification.SubjectUser,
		Domain: string(platform.DomainIndividualOwner), ID: acc}, "owner-identity:"+acc.String(), docs, key)
}

// runCase executes create → evidence → submit → process with deterministic
// idempotency keys so a retried command repeats rather than duplicates.
func (s *Service) runCase(ctx context.Context, typ string, subject verification.Subject, ref string, docs []DocumentInput, key func(string) string) (StartedCase, error) {
	c, err := s.Verification.Create(ctx, typ, subject, ref, key("verification.create"))
	if err != nil {
		return StartedCase{}, downstream("verification-service", err)
	}
	out := StartedCase{Evidence: map[ids.ID]ids.ID{}}
	if c.Status == "DRAFT" {
		for _, d := range docs {
			ev, err := s.Verification.AttachEvidence(ctx, c.VerificationID, d.MediaID, d.DocumentType, key("verification.evidence:"+d.MediaID.String()))
			if err != nil {
				return StartedCase{}, downstream("verification-service", err)
			}
			out.Evidence[d.MediaID] = ev.EvidenceID
		}
		if c, err = s.Verification.Submit(ctx, c.VerificationID, key("verification.submit")); err != nil {
			return StartedCase{}, downstream("verification-service", err)
		}
	}
	if c.Status == "SUBMITTED" {
		if c, err = s.Verification.Process(ctx, c.VerificationID, key("verification.process")); err != nil {
			return StartedCase{}, downstream("verification-service", err)
		}
	}
	out.Case = c
	return out, nil
}

// RecordIdentityVerification stores the started case (transaction phase).
func (s *Service) RecordIdentityVerification(ctx context.Context, subj authz.Subject, sc StartedCase) (VerificationLinkView, error) {
	var out VerificationLinkView
	err := s.InTx(ctx, func(ctx context.Context) error {
		acc := accountOf(subj)
		l, err := s.q.InsertVerificationLink(ctx, mustTx(ctx), db.InsertVerificationLinkParams{ID: ids.New(), VerificationID: sc.Case.VerificationID,
			VerificationType: "IDENTITY", SubjectType: "USER", SubjectID: acc, AccountID: acc, Status: linkStatus(sc.Case.Status), LastSyncedAt: s.now()})
		if err != nil {
			return err
		}
		if err := s.record(ctx, "owner.identity_verification.start", "verification_link", l.ID, map[string]any{"verification_id": sc.Case.VerificationID}, nil); err != nil {
			return err
		}
		out = linkView(l)
		return nil
	})
	return out, err
}

func linkStatus(s string) string {
	if s == "" {
		return "SUBMITTED"
	}
	return s
}

// ListIdentityVerifications lists the caller's IDENTITY cases.
func (s *Service) ListIdentityVerifications(ctx context.Context, subj authz.Subject) ([]VerificationLinkView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActProfileRead, ids.Nil); err != nil {
		return nil, err
	}
	return s.links(ctx, "USER", accountOf(subj))
}

func (s *Service) links(ctx context.Context, subjectType string, id ids.ID) ([]VerificationLinkView, error) {
	rows, err := s.q.ListVerificationLinks(ctx, s.dbq(ctx), db.ListVerificationLinksParams{SubjectType: subjectType, SubjectID: id})
	out := make([]VerificationLinkView, 0, len(rows))
	for _, r := range rows {
		out = append(out, linkView(r))
	}
	return out, err
}

// PreparedPropertyVerification is the pre-transaction result of a property
// authority submission.
type PreparedPropertyVerification struct {
	RecordID ids.ID
	Started  StartedCase
}

// StartPropertyVerification submits the primary ownership claim and its READY
// evidence to verification-service as a PROPERTY_AUTHORITY case (downstream
// phase; the transaction phase is RecordPropertyVerification).
func (s *Service) StartPropertyVerification(ctx context.Context, subj authz.Subject, propertyID ids.ID, key func(string) string) (PreparedPropertyVerification, error) {
	if _, err := s.authorize(ctx, subj, authz.Do(authz.CapOwnershipManage), propertyID); err != nil {
		return PreparedPropertyVerification{}, err
	}
	if _, err := s.requireMutable(ctx, propertyID); err != nil {
		return PreparedPropertyVerification{}, err
	}
	if s.Verification == nil {
		return PreparedPropertyVerification{}, unavailable("Property verification", "verification-service")
	}
	rec, err := s.q.GetPrimaryOwnershipRecord(ctx, s.Pool, propertyID)
	if err != nil {
		return PreparedPropertyVerification{}, err
	}
	if rec.Status != "EVIDENCE_PENDING" && rec.Status != "REVERIFICATION_REQUIRED" {
		return PreparedPropertyVerification{}, conflict("OWNERSHIP_NOT_SUBMITTABLE", "The claim must have evidence and be awaiting submission (status "+rec.Status+").")
	}
	docs, err := s.q.ListAuthorityDocuments(ctx, s.Pool, rec.ID)
	if err != nil {
		return PreparedPropertyVerification{}, err
	}
	var ready []DocumentInput
	for _, d := range docs {
		st := d.MediaStatus
		if st != "READY" && s.Media != nil {
			if m, err := s.Media.Get(ctx, d.MediaID); err == nil {
				st = media.Projection(m.Status)
			}
		}
		if st == "READY" {
			ready = append(ready, DocumentInput{MediaID: d.MediaID, DocumentType: d.DocumentType})
		}
	}
	if len(ready) == 0 {
		return PreparedPropertyVerification{}, precondition("EVIDENCE_NOT_READY", "At least one authority document must finish scanning (READY) before submission.")
	}
	sc, err := s.runCase(ctx, verification.TypePropertyAuthority, verification.Subject{Type: verification.SubjectProperty,
		Domain: string(platform.DomainIndividualOwner), ID: propertyID}, "owner-property:"+rec.ID.String(), ready, key)
	return PreparedPropertyVerification{RecordID: rec.ID, Started: sc}, err
}

// RecordPropertyVerification moves the claim to UNDER_VERIFICATION and links the case.
func (s *Service) RecordPropertyVerification(ctx context.Context, subj authz.Subject, propertyID ids.ID, pv PreparedPropertyVerification) (OwnershipView, error) {
	var out OwnershipView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapOwnershipManage), propertyID); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		rec, err := s.q.GetPrimaryOwnershipRecordForUpdate(ctx, tx, propertyID)
		if err != nil {
			return err
		}
		if rec.ID != pv.RecordID {
			return conflict("OWNERSHIP_CHANGED", "The ownership claim changed during submission; retry.")
		}
		if _, _, err := s.transition(ctx, domain.Ownership, rec.ID, lifecycle.Change{To: "UNDER_VERIFICATION", Command: "submit_verification",
			Metadata: map[string]any{"verification_id": pv.Started.Case.VerificationID}}); err != nil {
			return err
		}
		vid := pv.Started.Case.VerificationID
		if err := s.q.SetOwnershipVerification(ctx, tx, db.SetOwnershipVerificationParams{ID: rec.ID, VerificationID: &vid, UpdatedAt: now}); err != nil {
			return err
		}
		docs, err := s.q.ListAuthorityDocuments(ctx, tx, rec.ID)
		if err != nil {
			return err
		}
		for _, d := range docs {
			if ev, ok := pv.Started.Evidence[d.MediaID]; ok {
				if err := s.q.SetAuthorityEvidence(ctx, tx, db.SetAuthorityEvidenceParams{ID: d.ID, VerificationEvidenceID: &ev, UpdatedAt: now}); err != nil {
					return err
				}
			}
		}
		if _, err := s.q.InsertVerificationLink(ctx, tx, db.InsertVerificationLinkParams{ID: ids.New(), VerificationID: vid, VerificationType: "PROPERTY_AUTHORITY",
			SubjectType: "PROPERTY", SubjectID: propertyID, AccountID: s.actingAccount(ctx, subj, propertyID), PropertyID: &propertyID,
			OwnershipRecordID: &rec.ID, Status: linkStatus(pv.Started.Case.Status), LastSyncedAt: now}); err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, propertyID, "VERIFICATION_SUBMITTED", "Ownership authority submitted for verification", "verification", vid); err != nil {
			return err
		}
		if err := s.record(ctx, "ownership.verification.submit", AggOwnership, rec.ID, map[string]any{"verification_id": vid}, nil); err != nil {
			return err
		}
		if err := s.emitVerificationChanged(ctx, propertyID); err != nil {
			return err
		}
		out, err = s.ownershipView(ctx, propertyID)
		return err
	})
	return out, err
}

// PropertyVerifications lists the property's verification links.
func (s *Service) PropertyVerifications(ctx context.Context, subj authz.Subject, propertyID ids.ID) ([]VerificationLinkView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapPropertyView), propertyID); err != nil {
		return nil, err
	}
	return s.links(ctx, "PROPERTY", propertyID)
}

// VerificationChangedEvent is property.verification_changed.v1.
type VerificationChangedEvent struct {
	SchemaVersion     int        `json:"schema_version"`
	PropertyID        ids.ID     `json:"property_id"`
	AuthorityVerified bool       `json:"authority_verified"`
	OwnershipStatus   string     `json:"ownership_status"`
	VerificationLevel string     `json:"verification_level" v:"enum=NONE|OWNER_AUTHORITY_VERIFIED"`
	VerifiedAt        *time.Time `json:"verified_at"`
	ExpiresAt         *time.Time `json:"expires_at"`
	OccurredAt        time.Time  `json:"occurred_at"`
}

func (s *Service) emitVerificationChanged(ctx context.Context, propertyID ids.ID) error {
	rec, err := s.q.GetPrimaryOwnershipRecord(ctx, mustTx(ctx), propertyID)
	if err != nil {
		return err
	}
	sum := ownershipSummary(rec, s.now())
	level := "NONE"
	if sum.Verified {
		level = "OWNER_AUTHORITY_VERIFIED"
	}
	return s.publish(ctx, "property.verification_changed.v1", AggProperty, propertyID, VerificationChangedEvent{SchemaVersion: 1, PropertyID: propertyID,
		AuthorityVerified: sum.Verified, OwnershipStatus: rec.Status, VerificationLevel: level, VerifiedAt: rec.VerifiedAt, ExpiresAt: rec.ExpiresAt, OccurredAt: s.now()})
}

// ConsumerVerification is the durable consumer name for verification.* events.
const ConsumerVerification = "individual-owner-service--verification"

type verificationPayload struct {
	VerificationID   ids.ID               `json:"verification_id"`
	VerificationType string               `json:"verification_type"`
	Subject          verification.Subject `json:"subject"`
	Status           string               `json:"status"`
	ReasonCode       string               `json:"reason_code"`
	DecidedAt        *time.Time           `json:"decided_at"`
	ExpiresAt        *time.Time           `json:"expires_at"`
}

var linkRank = map[string]int{"DRAFT": 0, "SUBMITTED": 1, "PROCESSING": 2, "MANUAL_REVIEW": 3, "VERIFIED": 4, "REJECTED": 4, "EXPIRED": 5}

// ErrUnknownVerification signals an event for a case whose link is not
// committed yet (out-of-order delivery); the consumer retries.
var ErrUnknownVerification = apperrorsUnknownVerification{}

type apperrorsUnknownVerification struct{}

func (apperrorsUnknownVerification) Error() string { return "verification link not found yet" }

// ApplyVerificationEvent applies one verification.* delivery exactly once.
func (s *Service) ApplyVerificationEvent(ctx context.Context, e events.Envelope) error {
	var p verificationPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return events.Permanent(err)
	}
	if p.Subject.Domain != string(platform.DomainIndividualOwner) {
		return nil // another domain's case
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		first, err := events.MarkProcessed(ctx, mustTx(ctx), ConsumerVerification, e)
		if err != nil || !first {
			return err
		}
		return s.applyVerification(ctx, p, e.OccurredAt)
	})
}

// SyncVerification reconciles one case by read-back (lost events, crash).
func (s *Service) SyncVerification(ctx context.Context, verificationID ids.ID) error {
	if s.Verification == nil {
		return unavailable("Verification reconciliation", "verification-service")
	}
	c, err := s.Verification.Get(ctx, verificationID)
	if err != nil {
		return downstream("verification-service", err)
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		err := s.applyVerification(ctx, verificationPayload{VerificationID: c.VerificationID, VerificationType: c.Type, Subject: c.Subject,
			Status: c.Status, ReasonCode: c.ReasonCode, DecidedAt: c.DecidedAt, ExpiresAt: c.ExpiresAt}, c.UpdatedAt)
		if err == ErrUnknownVerification {
			return notFound("verification")
		}
		return err
	})
}

// SyncMyVerification lets the owner force read-back of one of their cases.
func (s *Service) SyncMyVerification(ctx context.Context, subj authz.Subject, verificationID ids.ID) (VerificationLinkView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActIdentityVerify, ids.Nil); err != nil {
		return VerificationLinkView{}, err
	}
	l, err := s.q.GetVerificationLink(ctx, s.Pool, verificationID)
	if isNoRows(err) || (err == nil && l.AccountID != accountOf(subj) && l.SubjectID != accountOf(subj)) {
		return VerificationLinkView{}, notFound("verification")
	}
	if err != nil {
		return VerificationLinkView{}, err
	}
	if l.PropertyID != nil {
		if _, err := s.authorize(ctx, subj, authz.Read(authz.CapOwnershipManage), *l.PropertyID); err != nil {
			return VerificationLinkView{}, err
		}
	}
	if err := s.SyncVerification(ctx, verificationID); err != nil {
		return VerificationLinkView{}, err
	}
	l, err = s.q.GetVerificationLink(ctx, s.Pool, verificationID)
	return linkView(l), err
}

func (s *Service) applyVerification(ctx context.Context, p verificationPayload, at time.Time) error {
	tx, now := mustTx(ctx), s.now()
	l, err := s.q.GetVerificationLinkForUpdate(ctx, tx, p.VerificationID)
	if isNoRows(err) {
		return ErrUnknownVerification
	}
	if err != nil {
		return err
	}
	// Out-of-order protection: never move backwards; EXPIRED only after VERIFIED.
	if linkRank[p.Status] < linkRank[l.Status] || (p.Status == l.Status) || (p.Status == "EXPIRED" && l.Status != "VERIFIED") {
		_, err := s.q.UpdateVerificationLink(ctx, tx, db.UpdateVerificationLinkParams{VerificationID: l.VerificationID, Status: l.Status, ReasonCode: l.ReasonCode,
			DecidedAt: l.DecidedAt, ExpiresAt: l.ExpiresAt, LastEventAt: l.LastEventAt, LastSyncedAt: now})
		return err
	}
	if _, err := s.q.UpdateVerificationLink(ctx, tx, db.UpdateVerificationLinkParams{VerificationID: l.VerificationID, Status: p.Status, ReasonCode: strp(p.ReasonCode),
		DecidedAt: p.DecidedAt, ExpiresAt: p.ExpiresAt, LastEventAt: &at, LastSyncedAt: now}); err != nil {
		return err
	}
	if l.VerificationType != "PROPERTY_AUTHORITY" || l.OwnershipRecordID == nil || l.PropertyID == nil {
		return s.record(ctx, "owner.identity_verification."+lower(p.Status), "verification_link", l.ID, map[string]any{"status": p.Status}, nil)
	}
	propertyID := *l.PropertyID
	rec, err := s.q.GetOwnershipRecord(ctx, tx, *l.OwnershipRecordID)
	if err != nil {
		return err
	}
	var to, summary string
	switch {
	case p.Status == "VERIFIED" && rec.Status == "UNDER_VERIFICATION":
		to, summary = "VERIFIED", "Ownership authority verified"
	case p.Status == "REJECTED" && rec.Status == "UNDER_VERIFICATION":
		to, summary = "REJECTED", "Ownership authority rejected"
	case p.Status == "EXPIRED" && rec.Status == "VERIFIED":
		to, summary = "REVERIFICATION_REQUIRED", "Ownership verification expired; re-verification required"
	default:
		return s.propertyHistory(ctx, propertyID, "VERIFICATION_"+p.Status, "Verification status: "+p.Status, "verification", l.VerificationID)
	}
	if _, _, err := s.transition(ctx, domain.Ownership, rec.ID, lifecycle.Change{To: to, Command: "verification_" + lower(p.Status), Reason: p.ReasonCode,
		Metadata: map[string]any{"verification_id": l.VerificationID}}); err != nil {
		return err
	}
	if to == "VERIFIED" || to == "REJECTED" {
		var verifiedAt *time.Time
		if to == "VERIFIED" {
			verifiedAt = p.DecidedAt
			if verifiedAt == nil {
				verifiedAt = &now
			}
		}
		if err := s.q.SetOwnershipDecision(ctx, tx, db.SetOwnershipDecisionParams{ID: rec.ID, VerifiedAt: verifiedAt, ExpiresAt: p.ExpiresAt,
			ReasonCode: strp(p.ReasonCode), UpdatedAt: now}); err != nil {
			return err
		}
	}
	if to == "VERIFIED" {
		prop, err := s.q.GetProperty(ctx, tx, propertyID)
		if err != nil {
			return err
		}
		if prop.Status == "DRAFT" {
			if _, _, err := s.transition(ctx, domain.Property, propertyID, lifecycle.Change{To: "ACTIVE", Command: "authority_verified"}); err != nil {
				return err
			}
			if prop, err = s.q.GetProperty(ctx, tx, propertyID); err != nil {
				return err
			}
			if err := s.emitProperty(ctx, "property.updated.v1", prop, []string{"status"}); err != nil {
				return err
			}
		}
	}
	if to != "VERIFIED" {
		if err := s.authorityLost(ctx, propertyID, to); err != nil {
			return err
		}
	}
	if err := s.propertyHistory(ctx, propertyID, "OWNERSHIP_"+to, summary, AggOwnership, rec.ID); err != nil {
		return err
	}
	if err := s.record(ctx, "ownership.verification."+lower(to), AggOwnership, rec.ID, map[string]any{"status": to, "verification_id": l.VerificationID}, nil); err != nil {
		return err
	}
	return s.emitVerificationChanged(ctx, propertyID)
}

// authorityLostHooks run when a property's ownership authority stops being
// verified (business contexts pause what depends on it, e.g. published listings).
var authorityLostHooks []func(s *Service, ctx context.Context, propertyID ids.ID, status string) error

func (s *Service) authorityLost(ctx context.Context, propertyID ids.ID, status string) error {
	for _, h := range authorityLostHooks {
		if err := h(s, ctx, propertyID, status); err != nil {
			return err
		}
	}
	return nil
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// PublicSummary is the fresh public property/authority summary (§9
// marketplace read-back, §13.6 verification summary). It contains no owner
// personal data and no exact address unless the owner chose EXACT visibility.
type PublicSummary struct {
	PropertyID        ids.ID     `json:"property_id"`
	Status            string     `json:"status"`
	Category          string     `json:"category"`
	PropertyType      string     `json:"property_type"`
	Title             string     `json:"title"`
	City              string     `json:"city"`
	StateRegion       string     `json:"state_region"`
	CountryCode       string     `json:"country_code"`
	AreaLabel         *string    `json:"area_label"`
	Latitude          *float64   `json:"latitude" doc:"Only when the owner chose EXACT visibility"`
	Longitude         *float64   `json:"longitude"`
	AuthorityVerified bool       `json:"authority_verified"`
	VerificationLevel string     `json:"verification_level"`
	VerifiedAt        *time.Time `json:"verified_at"`
	ExpiresAt         *time.Time `json:"expires_at"`
	Restricted        bool       `json:"restricted"`
	ReadyPhotoCount   int64      `json:"ready_photo_count"`
	Limitations       []string   `json:"limitations"`
	PropertyVersion   int64      `json:"property_version"`
	GeneratedAt       time.Time  `json:"generated_at"`
}

// PublicSummaryOf builds the public summary (no authorization: callers check).
func (s *Service) PublicSummaryOf(ctx context.Context, propertyID ids.ID) (PublicSummary, error) {
	q, now := s.dbq(ctx), s.now()
	p, err := s.q.GetProperty(ctx, q, propertyID)
	if isNoRows(err) {
		return PublicSummary{}, notFound(AggProperty)
	}
	if err != nil {
		return PublicSummary{}, err
	}
	a, err := s.q.GetPropertyAddress(ctx, q, propertyID)
	if err != nil {
		return PublicSummary{}, err
	}
	v := PublicSummary{PropertyID: p.ID, Status: p.Status, Category: p.Category, PropertyType: p.PropertyType, Title: p.Title, City: a.City,
		StateRegion: a.StateRegion, CountryCode: a.CountryCode, AreaLabel: a.AreaLabel, Restricted: p.Status == "RESTRICTED",
		VerificationLevel: "NONE", PropertyVersion: p.Version, GeneratedAt: now,
		Limitations: []string{"Verification confirms the owner's authority to offer the property; it is not a title guarantee.",
			"Coordinates are owner-provided unless a maps provider confirmed them."}}
	if a.Visibility == "EXACT" {
		v.Latitude, v.Longitude = a.Latitude, a.Longitude
	}
	if rec, err := s.q.GetPrimaryOwnershipRecord(ctx, q, propertyID); err == nil {
		sum := ownershipSummary(rec, now)
		v.AuthorityVerified, v.VerifiedAt, v.ExpiresAt = sum.Verified, rec.VerifiedAt, rec.ExpiresAt
		if sum.Verified {
			v.VerificationLevel = "OWNER_AUTHORITY_VERIFIED"
		}
	} else if !isNoRows(err) {
		return PublicSummary{}, err
	}
	if v.ReadyPhotoCount, err = s.q.CountReadyPropertyPhotos(ctx, q, propertyID); err != nil {
		return PublicSummary{}, err
	}
	return v, nil
}

// VerificationSummary is the owner's view of the public summary.
func (s *Service) VerificationSummary(ctx context.Context, subj authz.Subject, propertyID ids.ID) (PublicSummary, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapPropertyView), propertyID); err != nil {
		return PublicSummary{}, err
	}
	return s.PublicSummaryOf(ctx, propertyID)
}

// InternalPublicSummary is the marketplace/admin read-back.
func (s *Service) InternalPublicSummary(ctx context.Context, subj authz.Subject, propertyID ids.ID) (PublicSummary, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActInternalPublicSummary, authz.Resource{PropertyID: propertyID}); err != nil {
		return PublicSummary{}, err
	}
	return s.PublicSummaryOf(ctx, propertyID)
}

// ConsumerMedia is the durable consumer name for media.* events.
const ConsumerMedia = "individual-owner-service--media"

// ApplyMediaEvent updates media usability projections exactly once.
func (s *Service) ApplyMediaEvent(ctx context.Context, e events.Envelope) error {
	var p struct {
		MediaID ids.ID `json:"media_id"`
		Owner   struct {
			Domain string `json:"owner_domain"`
		} `json:"owner"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return events.Permanent(err)
	}
	if p.Owner.Domain != string(platform.DomainIndividualOwner) {
		return nil
	}
	status := media.Projection(p.Status)
	if status == "PENDING" {
		return nil
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		first, err := events.MarkProcessed(ctx, mustTx(ctx), ConsumerMedia, e)
		if err != nil || !first {
			return err
		}
		return s.applyMediaStatus(ctx, p.MediaID, status)
	})
}

// mediaStatusHooks let business contexts react to media becoming unusable
// (e.g. pausing a published listing whose required photos were deleted).
var mediaStatusHooks []func(s *Service, ctx context.Context, mediaID ids.ID, status string) error

func (s *Service) applyMediaStatus(ctx context.Context, mediaID ids.ID, status string) error {
	tx, now := mustTx(ctx), s.now()
	a, err := s.q.SetPropertyMediaStatus(ctx, tx, db.SetPropertyMediaStatusParams{MediaID: mediaID, MediaStatus: status, UpdatedAt: now})
	if err != nil {
		return err
	}
	b, err := s.q.SetAuthorityDocumentStatus(ctx, tx, db.SetAuthorityDocumentStatusParams{MediaID: mediaID, MediaStatus: status, UpdatedAt: now})
	if err != nil {
		return err
	}
	for _, pid := range append(a, b...) {
		if err := s.propertyHistory(ctx, pid, "MEDIA_"+status, "Media "+mediaID.String()+" is now "+status, "media", mediaID); err != nil {
			return err
		}
	}
	for _, h := range mediaStatusHooks {
		if err := h(s, ctx, mediaID, status); err != nil {
			return err
		}
	}
	return nil
}
