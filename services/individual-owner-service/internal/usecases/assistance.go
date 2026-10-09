package usecases

import (
	"context"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Assisted use (§13.27). Concierge onboarding, assisted photography or
// document scanning and remote help are performed by support staff through
// admin-service as attributable admin actors — never a shared or generic
// account and never the owner's own session. The owner first authorizes
// assistance for named scopes (optionally limited to properties) and a
// bounded period (step-up); every assisted action requires a live
// authorization covering it, is recorded append-only with the support actor,
// lands in the audit trail under that actor and notifies the owner. Support
// staff can only create DRAFT property records and attach evidence
// references; publishing, money, ownership and closure stay owner actions.

// TemplateAssistedActivity tells the owner support acted on their account.
const TemplateAssistedActivity = "owner.assisted_activity" // activity

const maxAssistanceHours = 720

// activityScopes maps an assisted activity to the scopes that permit it.
var activityScopes = map[string][]string{
	"ONBOARDING_GUIDANCE":      {"ONBOARDING"},
	"PROPERTY_DRAFT_CREATED":   {"PROPERTY_RECORDS"},
	"PHOTOGRAPHY_CAPTURED":     {"MEDIA_CAPTURE"},
	"DOCUMENT_SCANNED":         {"DOCUMENT_SCANNING"},
	"REMOTE_HELP":              {"ONBOARDING", "PROPERTY_RECORDS", "MEDIA_CAPTURE", "DOCUMENT_SCANNING", "ACCESSIBILITY_SUPPORT"},
	"ACCESSIBILITY_ASSISTANCE": {"ACCESSIBILITY_SUPPORT"},
}

// AssistanceInput authorizes support staff to assist.
type AssistanceInput struct {
	Scopes         []string `json:"scopes" v:"required,min=1,max=5,enum=ONBOARDING|PROPERTY_RECORDS|MEDIA_CAPTURE|DOCUMENT_SCANNING|ACCESSIBILITY_SUPPORT"`
	PropertyIDs    []ids.ID `json:"property_ids,omitempty" v:"max=50" doc:"Limit assistance to these properties (default: no property limit)"`
	Channel        string   `json:"channel" v:"required,enum=IN_APP|PHONE|IN_PERSON|VIDEO"`
	Reason         *string  `json:"reason,omitempty" v:"max=500"`
	ExpiresInHours int32    `json:"expires_in_hours" v:"required,min=1,max=720" doc:"At most 30 days"`
}

// AssistanceRevokeInput revokes an authorization.
type AssistanceRevokeInput struct {
	Reason *string `json:"reason,omitempty" v:"max=500"`
}

// AssistanceView is an assistance authorization.
type AssistanceView struct {
	ID           ids.ID     `json:"id"`
	Scopes       []string   `json:"scopes"`
	PropertyIDs  []ids.ID   `json:"property_ids"`
	Channel      string     `json:"channel"`
	Reason       *string    `json:"reason"`
	Status       string     `json:"status" doc:"ACTIVE, REVOKED or EXPIRED"`
	ExpiresAt    time.Time  `json:"expires_at"`
	RevokedAt    *time.Time `json:"revoked_at"`
	RevokeReason *string    `json:"revoke_reason"`
	Version      int64      `json:"version"`
	CreatedAt    time.Time  `json:"created_at"`
}

func assistanceView(a db.AssistanceAuthorization) AssistanceView {
	return AssistanceView{ID: a.ID, Scopes: nonNil(a.Scopes), PropertyIDs: nonNilIDs(a.PropertyIds), Channel: a.Channel, Reason: a.Reason, Status: a.Status,
		ExpiresAt: a.ExpiresAt, RevokedAt: a.RevokedAt, RevokeReason: a.RevokeReason, Version: a.Version, CreatedAt: a.CreatedAt}
}

// AssistedActivityView is one attributed assisted action.
type AssistedActivityView struct {
	ID                 ids.ID    `json:"id"`
	AuthorizationID    ids.ID    `json:"authorization_id"`
	SupportService     string    `json:"support_service"`
	SupportActorDomain string    `json:"support_actor_domain"`
	SupportActorID     ids.ID    `json:"support_actor_id" doc:"The individual support staff member (admin account)"`
	ActivityType       string    `json:"activity_type"`
	Scope              string    `json:"scope"`
	PropertyID         *ids.ID   `json:"property_id"`
	MediaIDs           []ids.ID  `json:"media_ids"`
	Note               string    `json:"note"`
	CreatedAt          time.Time `json:"created_at"`
}

func assistedActivityView(a db.AssistedActivity) AssistedActivityView {
	return AssistedActivityView{ID: a.ID, AuthorizationID: a.AuthorizationID, SupportService: a.SupportService, SupportActorDomain: a.SupportActorDomain,
		SupportActorID: a.SupportActorID, ActivityType: a.ActivityType, Scope: a.Scope, PropertyID: a.PropertyID, MediaIDs: nonNilIDs(a.MediaIds),
		Note: a.Note, CreatedAt: a.CreatedAt}
}

// GrantAssistance authorizes attributable support staff for a bounded
// purpose and period (step-up).
func (s *Service) GrantAssistance(ctx context.Context, subj authz.Subject, in AssistanceInput) (AssistanceView, error) {
	if err := schema.Validate(in); err != nil {
		return AssistanceView{}, err
	}
	var out AssistanceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActAssistanceGrant, ids.Nil); err != nil {
			return err
		}
		props := uniqueIDs(in.PropertyIDs)
		for _, p := range props {
			if _, err := s.authorize(ctx, subj, authz.Read(authz.CapPropertyView), p); err != nil {
				return err
			}
		}
		now := s.now()
		a, err := s.q.InsertAssistanceAuthorization(ctx, mustTx(ctx), db.InsertAssistanceAuthorizationParams{ID: ids.New(), AccountID: accountOf(subj),
			Scopes: slices.Compact(slices.Sorted(slices.Values(in.Scopes))), PropertyIds: nonNilIDs(props), Channel: in.Channel, Reason: in.Reason,
			ExpiresAt: now.Add(time.Duration(min(in.ExpiresInHours, maxAssistanceHours)) * time.Hour), CreatedAt: now})
		if err != nil {
			return err
		}
		out = assistanceView(a)
		return s.record(ctx, "assistance.grant", "assistance_authorization", a.ID, map[string]any{"scopes": a.Scopes, "properties": len(props),
			"expires_at": a.ExpiresAt}, nil)
	})
	return out, err
}

// ListAssistance lists the owner's assistance authorizations.
func (s *Service) ListAssistance(ctx context.Context, subj authz.Subject) ([]AssistanceView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActAssistanceRead, ids.Nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAssistanceAuthorizations(ctx, s.dbq(ctx), accountOf(subj))
	out := make([]AssistanceView, 0, len(rows))
	for _, r := range rows {
		out = append(out, assistanceView(r))
	}
	return out, err
}

// RevokeAssistance ends an authorization immediately; later assisted
// actions under it are refused.
func (s *Service) RevokeAssistance(ctx context.Context, subj authz.Subject, id ids.ID, in AssistanceRevokeInput) (AssistanceView, error) {
	if err := schema.Validate(in); err != nil {
		return AssistanceView{}, err
	}
	var out AssistanceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActAssistanceRead, ids.Nil); err != nil {
			return err
		}
		tx := mustTx(ctx)
		a, err := s.q.GetAssistanceAuthorizationForUpdate(ctx, tx, id)
		if isNoRows(err) || (err == nil && a.AccountID != accountOf(subj)) {
			return notFound("assistance_authorization")
		}
		if err != nil {
			return err
		}
		if a.Status != "ACTIVE" {
			return apperrors.InvalidStateTransition(a.Status, "REVOKED")
		}
		if a, err = s.q.RevokeAssistanceAuthorization(ctx, tx, db.RevokeAssistanceAuthorizationParams{ID: id, RevokedAt: ptr(s.now()), RevokeReason: in.Reason}); err != nil {
			return err
		}
		out = assistanceView(a)
		return s.record(ctx, "assistance.revoke", "assistance_authorization", id, nil, nil)
	})
	return out, err
}

// ListAssistedActivity lists what support did on the owner's account.
func (s *Service) ListAssistedActivity(ctx context.Context, subj authz.Subject) ([]AssistedActivityView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActAssistanceRead, ids.Nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAssistedActivities(ctx, s.dbq(ctx), accountOf(subj))
	out := make([]AssistedActivityView, 0, len(rows))
	for _, r := range rows {
		out = append(out, assistedActivityView(r))
	}
	return out, err
}

// ExpireAssistance expires lapsed authorizations.
func (s *Service) ExpireAssistance(ctx context.Context) error {
	_, err := s.q.ExpireAssistanceAuthorizations(ctx, s.Pool, s.now())
	return err
}

// ---- admin-service (support staff) ----

// AdminAssistanceView is what support sees before assisting.
type AdminAssistanceView struct {
	AccountID      ids.ID           `json:"account_id"`
	AccountStatus  string           `json:"account_status"`
	Accessibility  []string         `json:"accessibility_needs" doc:"The owner's accessibility preferences"`
	Authorizations []AssistanceView `json:"authorizations" doc:"Live authorizations only"`
}

// AdminGetAssistance returns the live authorizations of an account
// (audited privileged read).
func (s *Service) AdminGetAssistance(ctx context.Context, subj authz.Subject, accountID ids.ID) (AdminAssistanceView, error) {
	var out AdminAssistanceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminAssist, authz.Resource{}); err != nil {
			return err
		}
		tx := mustTx(ctx)
		acc, err := s.q.GetAccount(ctx, tx, accountID)
		if isNoRows(err) {
			return notFound("account")
		}
		if err != nil {
			return err
		}
		out = AdminAssistanceView{AccountID: accountID, AccountStatus: acc.Status, Accessibility: []string{}, Authorizations: []AssistanceView{}}
		if p, err := s.q.GetOwnerProfile(ctx, tx, accountID); err == nil {
			out.Accessibility = nonNil(p.AccessibilityNeeds)
		} else if !isNoRows(err) {
			return err
		}
		rows, err := s.q.ActiveAssistanceAuthorizations(ctx, tx, db.ActiveAssistanceAuthorizationsParams{AccountID: accountID, ExpiresAt: s.now()})
		if err != nil {
			return err
		}
		for _, r := range rows {
			out.Authorizations = append(out.Authorizations, assistanceView(r))
		}
		return s.record(ctx, "assistance.privileged_read", "account", accountID, nil, nil)
	})
	return out, err
}

// AssistedActivityInput records an assisted action.
type AssistedActivityInput struct {
	AuthorizationID ids.ID   `json:"authorization_id" v:"required"`
	ActivityType    string   `json:"activity_type" v:"required,enum=ONBOARDING_GUIDANCE|PHOTOGRAPHY_CAPTURED|DOCUMENT_SCANNED|REMOTE_HELP|ACCESSIBILITY_ASSISTANCE"`
	PropertyID      *ids.ID  `json:"property_id,omitempty"`
	MediaIDs        []ids.ID `json:"media_ids,omitempty" v:"max=20" doc:"Captured photos/scans, uploaded to media-document-service for the owner or the property"`
	Note            string   `json:"note" v:"required,min=3,max=2000"`
}

func (in AssistedActivityInput) validateMedia() error {
	if len(in.MediaIDs) > 0 && in.ActivityType != "PHOTOGRAPHY_CAPTURED" && in.ActivityType != "DOCUMENT_SCANNED" {
		return apperrors.InvalidRequest("media_ids are recorded for photography and scanning only.")
	}
	return nil
}

// AssistedPropertyInput creates a DRAFT property for the owner.
type AssistedPropertyInput struct {
	AuthorizationID ids.ID              `json:"authorization_id" v:"required"`
	Property        CreatePropertyInput `json:"property" v:"required"`
	Note            string              `json:"note" v:"required,min=3,max=2000"`
}

// AssistedPropertyView is the created draft and its activity record.
type AssistedPropertyView struct {
	Property PropertyView         `json:"property"`
	Activity AssistedActivityView `json:"activity"`
}

// CheckAssistedMedia verifies, outside any transaction, that captured files
// exist, are usable and belong to the owner account or the property.
func (s *Service) CheckAssistedMedia(ctx context.Context, subj authz.Subject, accountID ids.ID, in AssistedActivityInput) (Evidence, error) {
	ev := Evidence{checked: map[ids.ID]media.Media{}}
	list := uniqueIDs(in.MediaIDs)
	if len(list) == 0 {
		return ev, nil
	}
	if err := in.validateMedia(); err != nil {
		return ev, err
	}
	if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminAssist, authz.Resource{}); err != nil {
		return ev, err
	}
	owners := []ids.ID{accountID}
	if in.PropertyID != nil {
		owners = append(owners, *in.PropertyID)
	}
	classes := []string{"IMAGE", "DOCUMENT", "VIDEO"}
	for _, m := range list {
		md, err := s.ownedMedia(ctx, m, owners, classes...)
		if err != nil {
			return ev, err
		}
		ev.checked[m] = md
	}
	return ev, nil
}

// liveAuthorization loads an authorization that covers the activity.
func (s *Service) liveAuthorization(ctx context.Context, accountID, authID ids.ID, activity string, propertyID *ids.ID) (db.AssistanceAuthorization, string, error) {
	tx, now := mustTx(ctx), s.now()
	a, err := s.q.GetAssistanceAuthorizationForUpdate(ctx, tx, authID)
	if isNoRows(err) || (err == nil && a.AccountID != accountID) {
		return a, "", notFound("assistance_authorization")
	}
	if err != nil {
		return a, "", err
	}
	if a.Status != "ACTIVE" || !a.ExpiresAt.After(now) {
		return a, "", apperrors.PermissionDenied("The owner's assistance authorization is not active.")
	}
	scope := ""
	for _, sc := range activityScopes[activity] {
		if slices.Contains(a.Scopes, sc) {
			scope = sc
			break
		}
	}
	if scope == "" {
		return a, "", apperrors.PermissionDenied("The owner did not authorize this kind of assistance.")
	}
	if propertyID != nil {
		if len(a.PropertyIds) > 0 && !slices.Contains(a.PropertyIds, *propertyID) {
			return a, "", apperrors.PermissionDenied("The owner did not authorize assistance on this property.")
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM property_access WHERE account_id = $1 AND property_id = $2
			AND role IN ('OWNER','CO_OWNER') AND (valid_until IS NULL OR valid_until > $3))`, accountID, *propertyID, now).Scan(&ok); err != nil {
			return a, "", err
		}
		if !ok {
			return a, "", notFound(AggProperty)
		}
	}
	acc, err := s.q.GetAccount(ctx, tx, accountID)
	if err != nil {
		return a, "", err
	}
	if !slices.Contains([]string{"PENDING_VERIFICATION", "ACTIVE"}, acc.Status) {
		return a, "", precondition("ACCOUNT_NOT_ASSISTABLE", "Assistance is available on pending or active accounts only.")
	}
	return a, scope, nil
}

func (s *Service) insertAssisted(ctx context.Context, subj authz.Subject, accountID ids.ID, a db.AssistanceAuthorization, activity, scope string,
	propertyID *ids.ID, media []ids.ID, note string) (db.AssistedActivity, error) {
	r, err := s.q.InsertAssistedActivity(ctx, mustTx(ctx), db.InsertAssistedActivityParams{ID: ids.New(), AccountID: accountID, AuthorizationID: a.ID,
		SupportService: string(subj.Service), SupportActorDomain: string(subj.ActorDomain), SupportActorID: subj.ActorID, ActivityType: activity, Scope: scope,
		PropertyID: propertyID, MediaIds: nonNilIDs(media), Note: note, CreatedAt: s.now()})
	if err != nil {
		return r, err
	}
	if propertyID != nil {
		if err := s.propertyHistory(ctx, *propertyID, "ASSISTED_"+activity, "Support assisted: "+lower(activity), "assisted_activity", r.ID); err != nil {
			return r, err
		}
	}
	if err := s.record(ctx, "assistance.activity", "account", accountID, map[string]any{"activity_id": r.ID, "activity_type": activity, "scope": scope,
		"authorization_id": a.ID, "property_id": propertyID, "media": len(media)}, nil); err != nil {
		return r, err
	}
	return r, s.notifyOwner(ctx, accountID, TemplateAssistedActivity, map[string]string{"activity": lower(activity)})
}

// AdminRecordAssistedActivity records an attributed assisted action.
func (s *Service) AdminRecordAssistedActivity(ctx context.Context, subj authz.Subject, accountID ids.ID, in AssistedActivityInput, ev Evidence) (AssistedActivityView, error) {
	if err := schema.Validate(in); err != nil {
		return AssistedActivityView{}, err
	}
	if err := ev.require(in.MediaIDs...); err != nil {
		return AssistedActivityView{}, err
	}
	if err := in.validateMedia(); err != nil {
		return AssistedActivityView{}, err
	}
	var out AssistedActivityView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminAssist, authz.Resource{}); err != nil {
			return err
		}
		a, scope, err := s.liveAuthorization(ctx, accountID, in.AuthorizationID, in.ActivityType, in.PropertyID)
		if err != nil {
			return err
		}
		r, err := s.insertAssisted(ctx, subj, accountID, a, in.ActivityType, scope, in.PropertyID, uniqueIDs(in.MediaIDs), in.Note)
		out = assistedActivityView(r)
		return err
	})
	return out, err
}

// AdminCreateAssistedProperty creates a DRAFT property record for the owner
// (concierge onboarding). The owner reviews it; nothing is published.
func (s *Service) AdminCreateAssistedProperty(ctx context.Context, subj authz.Subject, accountID ids.ID, in AssistedPropertyInput) (AssistedPropertyView, error) {
	if err := schema.Validate(in); err != nil {
		return AssistedPropertyView{}, err
	}
	var out AssistedPropertyView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminAssist, authz.Resource{}); err != nil {
			return err
		}
		a, scope, err := s.liveAuthorization(ctx, accountID, in.AuthorizationID, "PROPERTY_DRAFT_CREATED", nil)
		if err != nil {
			return err
		}
		acc, err := s.q.GetAccount(ctx, mustTx(ctx), accountID)
		if err != nil {
			return err
		}
		if acc.Status != "ACTIVE" {
			return precondition("ACCOUNT_NOT_ACTIVE", "Property records are created for active accounts.")
		}
		p, err := s.createPropertyTx(ctx, accountID, in.Property, nil)
		if err != nil {
			return err
		}
		r, err := s.insertAssisted(ctx, subj, accountID, a, "PROPERTY_DRAFT_CREATED", scope, &p.ID, nil, in.Note)
		if err != nil {
			return err
		}
		if out.Property, err = s.propertyView(ctx, p.ID, string(authz.RoleOwner)); err != nil {
			return err
		}
		out.Activity = assistedActivityView(r)
		return nil
	})
	return out, err
}
