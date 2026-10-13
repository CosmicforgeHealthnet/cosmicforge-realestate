package usecases

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// RecordView is an ownership/authority record.
type RecordView struct {
	ID                  ids.ID         `json:"id"`
	ClaimType           string         `json:"claim_type"`
	Status              string         `json:"status"`
	IsPrimary           bool           `json:"is_primary"`
	Conflict            bool           `json:"conflict"`
	VerificationID      *ids.ID        `json:"verification_id"`
	VerifiedAt          *time.Time     `json:"verified_at"`
	ExpiresAt           *time.Time     `json:"expires_at"`
	ReasonCode          *string        `json:"reason_code"`
	Documents           []DocumentView `json:"documents"`
	AllowedNextStatuses []string       `json:"allowed_next_statuses"`
	Version             int64          `json:"version"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

// DocumentView is an authority evidence reference (media-service ID only).
type DocumentView struct {
	ID           ids.ID    `json:"id"`
	MediaID      ids.ID    `json:"media_id"`
	DocumentType string    `json:"document_type"`
	MediaStatus  string    `json:"media_status"`
	Submitted    bool      `json:"submitted_to_verification"`
	CreatedAt    time.Time `json:"created_at"`
}

// PartyView is an ownership party (owner, co-owner, representative, beneficial owner).
type PartyView struct {
	ID           ids.ID     `json:"id"`
	PartyRole    string     `json:"party_role"`
	DisplayName  string     `json:"display_name"`
	Status       string     `json:"status"`
	Linked       bool       `json:"linked" doc:"An owner account accepted the invitation"`
	InviteEmail  *string    `json:"invite_email"`
	ShareBps     *int32     `json:"share_bps" doc:"Ownership share in basis points (10000 = 100%)"`
	Capabilities []string   `json:"capabilities"`
	ValidFrom    *time.Time `json:"valid_from"`
	ValidUntil   *time.Time `json:"valid_until"`
	CreatedAt    time.Time  `json:"created_at"`
}

// OwnershipView is §13.4 /properties/{id}/ownership.
type OwnershipView struct {
	PropertyID ids.ID        `json:"property_id"`
	Primary    *RecordView   `json:"primary"`
	Previous   []RecordView  `json:"previous_records"`
	Parties    []PartyView   `json:"parties"`
	Transfer   *TransferView `json:"transfer"`
}

func recordView(r db.OwnershipRecord, docs []db.AuthorityDocument) RecordView {
	v := RecordView{ID: r.ID, ClaimType: r.ClaimType, Status: r.Status, IsPrimary: r.IsPrimary, Conflict: r.Status == "CONFLICT",
		VerificationID: r.VerificationID, VerifiedAt: r.VerifiedAt, ExpiresAt: r.ExpiresAt, ReasonCode: r.ReasonCode,
		Documents: []DocumentView{}, AllowedNextStatuses: domain.Ownership.Targets(r.Status), Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	if v.AllowedNextStatuses == nil {
		v.AllowedNextStatuses = []string{}
	}
	for _, d := range docs {
		v.Documents = append(v.Documents, DocumentView{ID: d.ID, MediaID: d.MediaID, DocumentType: d.DocumentType, MediaStatus: d.MediaStatus,
			Submitted: d.VerificationEvidenceID != nil, CreatedAt: d.CreatedAt})
	}
	return v
}

func partyView(p db.OwnershipParty) PartyView {
	return PartyView{ID: p.ID, PartyRole: p.PartyRole, DisplayName: p.DisplayName, Status: p.Status, Linked: p.AccountID != nil,
		InviteEmail: p.InviteEmail, ShareBps: p.ShareBps, Capabilities: nonNil(p.Capabilities), ValidFrom: p.ValidFrom, ValidUntil: p.ValidUntil, CreatedAt: p.CreatedAt}
}

// GetOwnership returns the property's authority records and parties.
func (s *Service) GetOwnership(ctx context.Context, subj authz.Subject, propertyID ids.ID) (OwnershipView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapOwnershipManage), propertyID); err != nil {
		return OwnershipView{}, err
	}
	return s.ownershipView(ctx, propertyID)
}

func (s *Service) ownershipView(ctx context.Context, propertyID ids.ID) (OwnershipView, error) {
	q := s.dbq(ctx)
	v := OwnershipView{PropertyID: propertyID, Previous: []RecordView{}, Parties: []PartyView{}}
	recs, err := s.q.ListOwnershipRecords(ctx, q, propertyID)
	if err != nil {
		return v, err
	}
	for _, r := range recs {
		docs, err := s.q.ListAuthorityDocuments(ctx, q, r.ID)
		if err != nil {
			return v, err
		}
		rv := recordView(r, docs)
		if r.IsPrimary {
			v.Primary = &rv
		} else {
			v.Previous = append(v.Previous, rv)
		}
	}
	parties, err := s.q.ListOwnershipParties(ctx, q, propertyID)
	if err != nil {
		return v, err
	}
	for _, p := range parties {
		v.Parties = append(v.Parties, partyView(p))
	}
	if c, err := s.q.GetLatestChangeCase(ctx, q, propertyID); err == nil {
		tv := transferView(c)
		v.Transfer = &tv
	} else if !isNoRows(err) {
		return v, err
	}
	return v, nil
}

// DocumentInput references an uploaded authority document.
type DocumentInput struct {
	MediaID      ids.ID `json:"media_id" v:"required"`
	DocumentType string `json:"document_type" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}" ex:"CERTIFICATE_OF_OCCUPANCY"`
}

// AddEvidenceInput attaches authority evidence to the primary claim.
type AddEvidenceInput struct {
	Documents []DocumentInput `json:"documents" v:"required,max=20"`
}

// CheckedMedia is the pre-transaction media check result.
type CheckedMedia map[ids.ID]media.Media

// CheckEvidence validates evidence media outside the transaction.
func (s *Service) CheckEvidence(ctx context.Context, subj authz.Subject, propertyID ids.ID, docs []DocumentInput) (CheckedMedia, error) {
	if _, err := s.authorize(ctx, subj, authz.Do(authz.CapOwnershipManage), propertyID); err != nil {
		return nil, err
	}
	out := CheckedMedia{}
	for _, d := range docs {
		m, err := s.ownedMedia(ctx, d.MediaID, []ids.ID{accountOf(subj), propertyID}, "DOCUMENT", "VERIFICATION_DOCUMENT", "IMAGE")
		if err != nil {
			return nil, err
		}
		out[d.MediaID] = m
	}
	return out, nil
}

// AddOwnershipEvidence attaches authority documents to the primary claim
// (DRAFT → EVIDENCE_PENDING on the first document).
func (s *Service) AddOwnershipEvidence(ctx context.Context, subj authz.Subject, propertyID ids.ID, in AddEvidenceInput, checked CheckedMedia) (OwnershipView, error) {
	if err := schema.Validate(in); err != nil {
		return OwnershipView{}, err
	}
	var out OwnershipView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapOwnershipManage), propertyID); err != nil {
			return err
		}
		if _, err := s.requireMutable(ctx, propertyID); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		rec, err := s.q.GetPrimaryOwnershipRecordForUpdate(ctx, tx, propertyID)
		if err != nil {
			return err
		}
		if !slices.Contains([]string{"DRAFT", "EVIDENCE_PENDING", "REVERIFICATION_REQUIRED", "CONFLICT"}, rec.Status) {
			return conflict("OWNERSHIP_EVIDENCE_LOCKED", "Evidence cannot be added while the claim is "+rec.Status+".")
		}
		for _, d := range in.Documents {
			m, ok := checked[d.MediaID]
			if !ok {
				return precondition("MEDIA_NOT_CHECKED", "Media must be validated before attaching.")
			}
			if _, err := s.q.InsertAuthorityDocument(ctx, tx, db.InsertAuthorityDocumentParams{ID: ids.New(), OwnershipRecordID: rec.ID, PropertyID: propertyID,
				MediaID: d.MediaID, DocumentType: d.DocumentType, MediaStatus: media.Projection(m.Status), UploadedByAccount: s.actingAccount(ctx, subj, propertyID),
				CreatedAt: now}); err != nil && !isNoRows(err) {
				return err
			}
		}
		if rec.Status == "DRAFT" {
			if _, _, err := s.transition(ctx, domain.Ownership, rec.ID, lifecycle.Change{To: "EVIDENCE_PENDING", Command: "add_evidence"}); err != nil {
				return err
			}
		}
		if err := s.propertyHistory(ctx, propertyID, "OWNERSHIP_EVIDENCE_ADDED", "Ownership evidence added", AggOwnership, rec.ID); err != nil {
			return err
		}
		if err := s.record(ctx, "ownership.evidence.add", AggOwnership, rec.ID, map[string]any{"documents": len(in.Documents)}, nil); err != nil {
			return err
		}
		out, err = s.ownershipView(ctx, propertyID)
		return err
	})
	return out, err
}

// actingAccount returns the owner account attributable for uploads; a
// delegated service actor is attributed to the property owner of record.
func (s *Service) actingAccount(ctx context.Context, subj authz.Subject, propertyID ids.ID) ids.ID {
	if a := accountOf(subj); !a.IsZero() {
		return a
	}
	if p, err := s.q.GetProperty(ctx, s.dbq(ctx), propertyID); err == nil {
		return p.OwnerAccountID
	}
	return ids.Nil
}

// ClaimInput declares or re-declares the ownership claim.
type ClaimInput struct {
	ClaimType string `json:"claim_type" v:"required,enum=SOLE|JOINT|FAMILY|INHERITED|PROBATE|POWER_OF_ATTORNEY|SUBLETTING_AUTHORITY|LEASEHOLD"`
}

// ClaimOwnership sets the claim type of an editable claim, or opens a fresh
// claim after a rejection (the rejected record is kept as history).
func (s *Service) ClaimOwnership(ctx context.Context, subj authz.Subject, propertyID ids.ID, in ClaimInput) (OwnershipView, error) {
	if err := schema.Validate(in); err != nil {
		return OwnershipView{}, err
	}
	var out OwnershipView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapOwnershipManage), propertyID); err != nil {
			return err
		}
		p, err := s.requireMutable(ctx, propertyID)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		rec, err := s.q.GetPrimaryOwnershipRecordForUpdate(ctx, tx, propertyID)
		if err != nil {
			return err
		}
		switch rec.Status {
		case "DRAFT", "EVIDENCE_PENDING":
			if err := s.q.SetOwnershipClaim(ctx, tx, db.SetOwnershipClaimParams{ID: rec.ID, ClaimType: in.ClaimType, UpdatedAt: now}); err != nil {
				return err
			}
		case "REJECTED":
			if err := s.q.DemoteOwnershipRecord(ctx, tx, db.DemoteOwnershipRecordParams{ID: rec.ID, UpdatedAt: now}); err != nil {
				return err
			}
			nrec, err := s.q.InsertOwnershipRecord(ctx, tx, db.InsertOwnershipRecordParams{ID: ids.New(), PropertyID: propertyID,
				ClaimantAccountID: p.OwnerAccountID, ClaimType: in.ClaimType, Status: "DRAFT", IsPrimary: true, CreatedAt: now})
			if err != nil {
				return err
			}
			if err := s.q.MovePartiesToRecord(ctx, tx, db.MovePartiesToRecordParams{OwnershipRecordID: rec.ID, OwnershipRecordID_2: nrec.ID, UpdatedAt: now}); err != nil {
				return err
			}
			if err := domain.Ownership.RecordCreation(ctx, tx, nrec.ID, &propertyID, "DRAFT", now); err != nil {
				return err
			}
			rec = nrec
		default:
			return conflict("OWNERSHIP_CLAIM_LOCKED", "The claim cannot be changed while it is "+rec.Status+".")
		}
		if err := s.propertyHistory(ctx, propertyID, "OWNERSHIP_CLAIMED", "Ownership claim declared: "+in.ClaimType, AggOwnership, rec.ID); err != nil {
			return err
		}
		if err := s.record(ctx, "ownership.claim", AggOwnership, rec.ID, map[string]any{"claim_type": in.ClaimType}, nil); err != nil {
			return err
		}
		out, err = s.ownershipView(ctx, propertyID)
		return err
	})
	return out, err
}

// CoOwnerInput invites a co-owner / representative or records a beneficial owner.
type CoOwnerInput struct {
	PartyRole       string     `json:"party_role" v:"required,enum=CO_OWNER|AUTHORIZED_REPRESENTATIVE|ESTATE_EXECUTOR|BENEFICIAL_OWNER"`
	Email           *string    `json:"email,omitempty" v:"format=email" doc:"Invitee's sign-in email (not needed for BENEFICIAL_OWNER)"`
	DisplayName     string     `json:"display_name" v:"required,max=160"`
	ShareBps        *int32     `json:"share_bps,omitempty" v:"min=1,max=9999" doc:"Co-owner share; the primary owner keeps the remainder"`
	Capabilities    []string   `json:"capabilities,omitempty" v:"max=30" doc:"Explicit delegable capabilities (representatives and executors only)"`
	ValidUntil      *time.Time `json:"valid_until,omitempty" doc:"Required for AUTHORIZED_REPRESENTATIVE"`
	EvidenceMediaID *ids.ID    `json:"evidence_media_id,omitempty" doc:"Mandate / power-of-attorney / letters of administration"`
}

// AddCoOwner records an invited party. Invitations are matched to an owner
// account only when that account signs in with the verified invite email —
// account existence is never looked up or disclosed.
func (s *Service) AddCoOwner(ctx context.Context, subj authz.Subject, propertyID ids.ID, in CoOwnerInput) (PartyView, error) {
	if err := schema.Validate(in); err != nil {
		return PartyView{}, err
	}
	if _, err := s.authorize(ctx, subj, "owner.sensitive_grant", ids.Nil); err != nil {
		return PartyView{}, err
	}
	rep := in.PartyRole == "AUTHORIZED_REPRESENTATIVE" || in.PartyRole == "ESTATE_EXECUTOR"
	for _, c := range in.Capabilities {
		if !authz.IsDelegable(authz.Capability(c)) {
			return PartyView{}, apperrors.InvalidRequest("Capability "+c+" cannot be delegated.", apperrors.Detail{Field: "capabilities", Code: "NOT_DELEGABLE", Message: c})
		}
	}
	switch {
	case len(in.Capabilities) > 0 && !rep:
		return PartyView{}, apperrors.InvalidRequest("Only representatives and executors hold explicit capabilities.")
	case in.PartyRole == "AUTHORIZED_REPRESENTATIVE" && in.ValidUntil == nil:
		return PartyView{}, apperrors.InvalidRequest("valid_until is required for authorized representatives.", apperrors.Detail{Field: "valid_until", Code: "REQUIRED", Message: "required"})
	case in.ValidUntil != nil && !in.ValidUntil.After(s.now()):
		return PartyView{}, apperrors.InvalidRequest("valid_until must be in the future.")
	case in.PartyRole != "BENEFICIAL_OWNER" && in.Email == nil:
		return PartyView{}, apperrors.InvalidRequest("email is required.", apperrors.Detail{Field: "email", Code: "REQUIRED", Message: "required"})
	case in.ShareBps != nil && in.PartyRole != "CO_OWNER" && in.PartyRole != "BENEFICIAL_OWNER":
		return PartyView{}, apperrors.InvalidRequest("Only co-owners and beneficial owners hold shares.")
	}
	if in.EvidenceMediaID != nil {
		if _, err := s.ownedMedia(ctx, *in.EvidenceMediaID, []ids.ID{accountOf(subj), propertyID}, "DOCUMENT", "VERIFICATION_DOCUMENT"); err != nil {
			return PartyView{}, err
		}
	}
	var out PartyView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapOwnershipManage), propertyID); err != nil {
			return err
		}
		if _, err := s.requireMutable(ctx, propertyID); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		rec, err := s.q.GetPrimaryOwnershipRecordForUpdate(ctx, tx, propertyID)
		if err != nil {
			return err
		}
		if slices.Contains([]string{"UNDER_VERIFICATION", "TRANSFER_PENDING", "TRANSFERRED", "REJECTED"}, rec.Status) {
			return conflict("OWNERSHIP_PARTIES_LOCKED", "Parties cannot change while the claim is "+rec.Status+".")
		}
		var email *string
		if in.Email != nil {
			e := strings.ToLower(strings.TrimSpace(*in.Email))
			email = &e
			if acc, err := s.q.GetAccount(ctx, tx, accountOf(subj)); err == nil && acc.Email == e {
				return precondition("CANNOT_INVITE_SELF", "You cannot invite yourself.")
			}
		}
		status := "INVITED"
		if in.PartyRole == "BENEFICIAL_OWNER" && email == nil {
			status = "ACTIVE"
		}
		var from *time.Time
		if status == "ACTIVE" {
			from = &now
		}
		party, err := s.q.InsertOwnershipParty(ctx, tx, db.InsertOwnershipPartyParams{ID: ids.New(), OwnershipRecordID: rec.ID, PropertyID: propertyID,
			PartyRole: in.PartyRole, InviteEmail: email, DisplayName: strings.TrimSpace(in.DisplayName), ShareBps: in.ShareBps,
			Capabilities: nonNil(in.Capabilities), EvidenceMediaID: in.EvidenceMediaID, Status: status, ValidFrom: from, ValidUntil: in.ValidUntil, CreatedAt: now})
		if err != nil {
			return err
		}
		if in.ShareBps != nil {
			if err := s.rebalanceShares(ctx, rec.ID); err != nil {
				return err
			}
		}
		if in.PartyRole == "CO_OWNER" || in.PartyRole == "BENEFICIAL_OWNER" {
			if err := s.ownershipStructureChanged(ctx, propertyID, rec); err != nil {
				return err
			}
		}
		if err := s.propertyHistory(ctx, propertyID, "OWNERSHIP_PARTY_ADDED", in.PartyRole+" added: "+party.DisplayName, "ownership_party", party.ID); err != nil {
			return err
		}
		if err := s.record(ctx, "ownership.party.add", "ownership_party", party.ID, map[string]any{"party_role": in.PartyRole,
			"capabilities": party.Capabilities, "share_bps": in.ShareBps}, nil); err != nil {
			return err
		}
		out = partyView(party)
		return nil
	})
	return out, err
}

// rebalanceShares gives the primary owner the remainder of declared shares.
func (s *Service) rebalanceShares(ctx context.Context, recordID ids.ID) error {
	tx := mustTx(ctx)
	owner, err := s.q.GetPrimaryOwnerParty(ctx, tx, recordID)
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	total, err := s.q.SumOwnershipShares(ctx, tx, recordID)
	if err != nil {
		return err
	}
	others := total - int64(deref(owner.ShareBps))
	if others >= 10000 {
		return precondition("OWNERSHIP_SHARES_EXCEED_TOTAL", "Declared co-owner shares must leave a share for the primary owner.")
	}
	return s.q.SetPartyShare(ctx, tx, db.SetPartyShareParams{ID: owner.ID, ShareBps: ptr(int32(10000 - others)), UpdatedAt: s.now()})
}

// ownershipStructureChanged requires re-verification when the principals of
// a verified claim change.
func (s *Service) ownershipStructureChanged(ctx context.Context, propertyID ids.ID, rec db.OwnershipRecord) error {
	if rec.Status != "VERIFIED" {
		return nil
	}
	if _, _, err := s.transition(ctx, domain.Ownership, rec.ID, lifecycle.Change{To: "REVERIFICATION_REQUIRED", Command: "parties_changed",
		Reason: "OWNERSHIP_PARTIES_CHANGED"}); err != nil {
		return err
	}
	if err := s.propertyHistory(ctx, propertyID, "REVERIFICATION_REQUIRED", "Ownership parties changed; authority must be re-verified", AggOwnership, rec.ID); err != nil {
		return err
	}
	return s.emitVerificationChanged(ctx, propertyID)
}

// RemoveParty revokes a party (never the primary owner).
func (s *Service) RemoveParty(ctx context.Context, subj authz.Subject, propertyID, partyID ids.ID) (PartyView, error) {
	if _, err := s.authorize(ctx, subj, "owner.sensitive_grant", ids.Nil); err != nil {
		return PartyView{}, err
	}
	var out PartyView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapOwnershipManage), propertyID); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		party, err := s.q.GetOwnershipPartyForUpdate(ctx, tx, partyID)
		if isNoRows(err) || (err == nil && party.PropertyID != propertyID) {
			return notFound("ownership_party")
		}
		if err != nil {
			return err
		}
		if party.PartyRole == "OWNER" {
			return precondition("CANNOT_REMOVE_PRIMARY_OWNER", "The primary owner changes only through an ownership transfer.")
		}
		if acc := accountOf(subj); party.AccountID != nil && *party.AccountID == acc {
			return precondition("CANNOT_REMOVE_SELF", "Ask another principal to remove you.")
		}
		ended, err := s.q.EndOwnershipParty(ctx, tx, db.EndOwnershipPartyParams{ID: partyID, Status: "REVOKED", UpdatedAt: now})
		if isNoRows(err) {
			return conflict("PARTY_ALREADY_ENDED", "The party is no longer active.")
		}
		if err != nil {
			return err
		}
		if err := s.q.WithdrawPendingConsentsForParty(ctx, tx, db.WithdrawPendingConsentsForPartyParams{PartyID: partyID, DecidedAt: &now}); err != nil {
			return err
		}
		rec, err := s.q.GetPrimaryOwnershipRecordForUpdate(ctx, tx, propertyID)
		if err != nil {
			return err
		}
		if party.ShareBps != nil {
			if err := s.rebalanceShares(ctx, rec.ID); err != nil {
				return err
			}
		}
		if party.PartyRole == "CO_OWNER" || party.PartyRole == "BENEFICIAL_OWNER" {
			if err := s.ownershipStructureChanged(ctx, propertyID, rec); err != nil {
				return err
			}
		}
		if err := s.propertyHistory(ctx, propertyID, "OWNERSHIP_PARTY_REMOVED", party.PartyRole+" removed: "+party.DisplayName, "ownership_party", partyID); err != nil {
			return err
		}
		if err := s.record(ctx, "ownership.party.remove", "ownership_party", partyID, map[string]any{"party_role": party.PartyRole}, nil); err != nil {
			return err
		}
		out = partyView(ended)
		return nil
	})
	return out, err
}

// InvitationView is a pending co-ownership/representation invitation.
type InvitationView struct {
	PartyID       ids.ID     `json:"party_id"`
	PropertyID    ids.ID     `json:"property_id"`
	PropertyTitle string     `json:"property_title"`
	PartyRole     string     `json:"party_role"`
	ShareBps      *int32     `json:"share_bps"`
	Capabilities  []string   `json:"capabilities"`
	ValidUntil    *time.Time `json:"valid_until"`
	CreatedAt     time.Time  `json:"created_at"`
}

func (s *Service) verifiedEmail(ctx context.Context, accountID ids.ID) (string, error) {
	acc, err := s.q.GetAccount(ctx, s.dbq(ctx), accountID)
	if err != nil {
		return "", err
	}
	if acc.EmailVerifiedAt == nil {
		return "", precondition("EMAIL_NOT_VERIFIED", "Verify your email address to see invitations sent to it.")
	}
	return acc.Email, nil
}

// ListInvitations lists invitations addressed to the caller's verified email.
func (s *Service) ListInvitations(ctx context.Context, subj authz.Subject) ([]InvitationView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActProfileRead, ids.Nil); err != nil {
		return nil, err
	}
	email, err := s.verifiedEmail(ctx, accountOf(subj))
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListInvitationsForEmail(ctx, s.dbq(ctx), &email)
	out := make([]InvitationView, 0, len(rows))
	for _, r := range rows {
		out = append(out, InvitationView{PartyID: r.ID, PropertyID: r.PropertyID, PropertyTitle: r.PropertyTitle, PartyRole: r.PartyRole,
			ShareBps: r.ShareBps, Capabilities: nonNil(r.Capabilities), ValidUntil: r.ValidUntil, CreatedAt: r.CreatedAt})
	}
	return out, err
}

// RespondInvitation accepts or declines an invitation addressed to the
// caller's verified email.
func (s *Service) RespondInvitation(ctx context.Context, subj authz.Subject, partyID ids.ID, accept bool) (PartyView, error) {
	var out PartyView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActProfileWrite, ids.Nil); err != nil {
			return err
		}
		email, err := s.verifiedEmail(ctx, accountOf(subj))
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		party, err := s.q.GetOwnershipPartyForUpdate(ctx, tx, partyID)
		if isNoRows(err) || (err == nil && (party.InviteEmail == nil || *party.InviteEmail != email)) {
			return notFound("invitation")
		}
		if err != nil {
			return err
		}
		if party.Status != "INVITED" {
			return conflict("INVITATION_NOT_PENDING", "The invitation is no longer pending.")
		}
		var res db.OwnershipParty
		if accept {
			acc, err := s.q.GetAccount(ctx, tx, accountOf(subj))
			if err != nil {
				return err
			}
			if acc.Status != "ACTIVE" {
				return precondition("ACCOUNT_NOT_ACTIVE", "Activate your account before accepting.")
			}
			id := accountOf(subj)
			res, err = s.q.AcceptOwnershipParty(ctx, tx, db.AcceptOwnershipPartyParams{ID: partyID, AccountID: &id, UpdatedAt: now})
			if isUnique(err, "ownership_parties_account_uniq") {
				return conflict("ALREADY_A_PARTY", "You already hold this role on the property.")
			}
			if err != nil {
				return err
			}
		} else {
			res, err = s.q.EndOwnershipParty(ctx, tx, db.EndOwnershipPartyParams{ID: partyID, Status: "REVOKED", UpdatedAt: now})
			if err != nil {
				return err
			}
		}
		verb := map[bool]string{true: "accepted", false: "declined"}[accept]
		if err := s.propertyHistory(ctx, party.PropertyID, "OWNERSHIP_INVITATION_"+strings.ToUpper(verb), party.PartyRole+" invitation "+verb, "ownership_party", partyID); err != nil {
			return err
		}
		if err := s.record(ctx, "ownership.invitation."+verb, "ownership_party", partyID, nil, nil); err != nil {
			return err
		}
		out = partyView(res)
		return nil
	})
	return out, err
}
