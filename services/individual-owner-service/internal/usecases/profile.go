package usecases

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// ProfileView is the owner profile (§13.2 /profile). Account identity fields
// (email, phone, display name) are managed through /me.
type ProfileView struct {
	AccountID          ids.ID    `json:"account_id"`
	DisplayName        string    `json:"display_name"`
	PreferredName      *string   `json:"preferred_name"`
	Bio                *string   `json:"bio"`
	AvatarMediaID      *ids.ID   `json:"avatar_media_id"`
	PublicProfile      bool      `json:"public_profile"`
	Languages          []string  `json:"languages"`
	Timezone           string    `json:"timezone"`
	AccessibilityNeeds []string  `json:"accessibility_needs"`
	PreferredChannel   string    `json:"preferred_channel"`
	MarketingOptIn     bool      `json:"marketing_opt_in"`
	Version            int64     `json:"version" doc:"0 until the profile is first saved; send it as If-Match"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// ProfileInput replaces the editable profile.
type ProfileInput struct {
	PreferredName      *string  `json:"preferred_name,omitempty" v:"max=120"`
	Bio                *string  `json:"bio,omitempty" v:"max=2000"`
	AvatarMediaID      *ids.ID  `json:"avatar_media_id,omitempty"`
	PublicProfile      bool     `json:"public_profile"`
	Languages          []string `json:"languages,omitempty" v:"max=10,pattern=[a-z]{2}(-[A-Z]{2})?"`
	Timezone           string   `json:"timezone" v:"required,max=64,pattern=[A-Za-z_]+(/[A-Za-z0-9_+-]+){0,2}" ex:"Africa/Lagos"`
	AccessibilityNeeds []string `json:"accessibility_needs,omitempty" v:"max=10,enum=SCREEN_READER|LARGE_TEXT|HIGH_CONTRAST|CAPTIONS|ASSISTED_USE|SIMPLE_LANGUAGE"`
	PreferredChannel   string   `json:"preferred_channel" v:"required,enum=PUSH|EMAIL|SMS|IN_APP"`
	MarketingOptIn     bool     `json:"marketing_opt_in"`
}

func profileView(a db.OwnerAccount, p *db.OwnerProfile) ProfileView {
	v := ProfileView{AccountID: a.AccountID, DisplayName: a.DisplayName, Languages: []string{}, Timezone: "Africa/Lagos",
		AccessibilityNeeds: []string{}, PreferredChannel: "PUSH", UpdatedAt: a.UpdatedAt}
	if p != nil {
		v.PreferredName, v.Bio, v.AvatarMediaID, v.PublicProfile = p.PreferredName, p.Bio, p.AvatarMediaID, p.PublicProfile
		v.Languages, v.Timezone, v.AccessibilityNeeds = p.Languages, p.Timezone, p.AccessibilityNeeds
		v.PreferredChannel, v.MarketingOptIn, v.Version, v.UpdatedAt = p.PreferredChannel, p.MarketingOptIn, p.Version, p.UpdatedAt
	}
	return v
}

// GetProfile returns the caller's profile.
func (s *Service) GetProfile(ctx context.Context, subj authz.Subject) (ProfileView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActProfileRead, ids.Nil); err != nil {
		return ProfileView{}, err
	}
	acc, err := s.q.GetAccount(ctx, s.dbq(ctx), accountOf(subj))
	if err != nil {
		return ProfileView{}, err
	}
	p, err := s.q.GetOwnerProfile(ctx, s.dbq(ctx), acc.AccountID)
	if isNoRows(err) {
		return profileView(acc, nil), nil
	}
	if err != nil {
		return ProfileView{}, err
	}
	return profileView(acc, &p), nil
}

// UpdateProfile replaces the profile with optimistic concurrency
// (If-Match "0" creates it).
func (s *Service) SaveOwnerProfile(ctx context.Context, subj authz.Subject, expected int64, in ProfileInput) (ProfileView, error) {
	if err := schema.Validate(in); err != nil {
		return ProfileView{}, err
	}
	if _, err := s.authorize(ctx, subj, authz.ActProfileWrite, ids.Nil); err != nil {
		return ProfileView{}, err
	}
	if in.AvatarMediaID != nil {
		if err := s.checkOwnedMedia(ctx, *in.AvatarMediaID, accountOf(subj), "IMAGE"); err != nil {
			return ProfileView{}, err
		}
	}
	var out ProfileView
	err := s.InTx(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.saveProfileTx(ctx, accountOf(subj), expected, in)
		return err
	})
	return out, err
}

func (s *Service) saveProfileTx(ctx context.Context, accID ids.ID, expected int64, in ProfileInput) (ProfileView, error) {
	tx, now := mustTx(ctx), s.now()
	acc, err := s.q.GetAccount(ctx, tx, accID)
	if err != nil {
		return ProfileView{}, err
	}
	langs, needs := nonNil(in.Languages), nonNil(in.AccessibilityNeeds)
	cur, err := s.q.GetOwnerProfile(ctx, tx, accID)
	var p db.OwnerProfile
	switch {
	case isNoRows(err):
		if expected != 0 {
			return ProfileView{}, checkVersion(expected, 0)
		}
		p, err = s.q.InsertOwnerProfile(ctx, tx, db.InsertOwnerProfileParams{AccountID: accID, PreferredName: in.PreferredName, Bio: in.Bio,
			AvatarMediaID: in.AvatarMediaID, PublicProfile: in.PublicProfile, Languages: langs, Timezone: in.Timezone,
			AccessibilityNeeds: needs, PreferredChannel: in.PreferredChannel, MarketingOptIn: in.MarketingOptIn, CreatedAt: now})
		if isUnique(err, "") {
			return ProfileView{}, checkVersion(1, 0)
		}
	case err != nil:
		return ProfileView{}, err
	default:
		if expected != cur.Version {
			return ProfileView{}, lifecycle.ErrVersionConflict()
		}
		p, err = s.q.UpdateOwnerProfile(ctx, tx, db.UpdateOwnerProfileParams{AccountID: accID, Version: cur.Version, PreferredName: in.PreferredName,
			Bio: in.Bio, AvatarMediaID: in.AvatarMediaID, PublicProfile: in.PublicProfile, Languages: langs, Timezone: in.Timezone,
			AccessibilityNeeds: needs, PreferredChannel: in.PreferredChannel, MarketingOptIn: in.MarketingOptIn, UpdatedAt: now})
		if isNoRows(err) {
			return ProfileView{}, checkVersion(1, 0)
		}
	}
	if err != nil {
		return ProfileView{}, err
	}
	if err := s.record(ctx, "owner.profile.update", "owner_account", accID, map[string]any{"version": p.Version}, nil); err != nil {
		return ProfileView{}, err
	}
	return profileView(acc, &p), nil
}

func ptr[T any](v T) *T { return &v }

func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// ContactView is an emergency/trusted/additional contact.
type ContactView struct {
	ID           ids.ID     `json:"id"`
	ContactType  string     `json:"contact_type"`
	Label        *string    `json:"label"`
	Name         *string    `json:"name"`
	Relationship *string    `json:"relationship"`
	Email        *string    `json:"email"`
	Phone        *string    `json:"phone"`
	Verified     bool       `json:"verified"`
	CreatedAt    time.Time  `json:"created_at"`
	VerifiedAt   *time.Time `json:"verified_at"`
}

// ContactInput adds a contact.
type ContactInput struct {
	ContactType  string  `json:"contact_type" v:"required,enum=EMAIL|PHONE|EMERGENCY|TRUSTED"`
	Label        *string `json:"label,omitempty" v:"max=60"`
	Name         *string `json:"name,omitempty" v:"max=120"`
	Relationship *string `json:"relationship,omitempty" v:"max=60"`
	Email        *string `json:"email,omitempty" v:"format=email"`
	Phone        *string `json:"phone,omitempty" v:"format=e164"`
}

func contactView(c db.OwnerContact) ContactView {
	return ContactView{ID: c.ID, ContactType: c.ContactType, Label: c.Label, Name: c.Name, Relationship: c.Relationship,
		Email: c.Email, Phone: c.PhoneE164, Verified: c.VerifiedAt != nil, VerifiedAt: c.VerifiedAt, CreatedAt: c.CreatedAt}
}

// ListContacts lists the caller's contacts.
func (s *Service) ListContacts(ctx context.Context, subj authz.Subject) ([]ContactView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActProfileRead, ids.Nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListOwnerContacts(ctx, s.dbq(ctx), accountOf(subj))
	out := make([]ContactView, 0, len(rows))
	for _, r := range rows {
		out = append(out, contactView(r))
	}
	return out, err
}

// AddContact records a contact (contacts are informational; they are not
// sign-in identifiers and are never used for recovery).
func (s *Service) AddContact(ctx context.Context, subj authz.Subject, in ContactInput) (ContactView, error) {
	if err := schema.Validate(in); err != nil {
		return ContactView{}, err
	}
	if in.Email == nil && in.Phone == nil {
		return ContactView{}, precondition("CONTACT_VALUE_REQUIRED", "A contact needs an email or a phone number.")
	}
	if _, err := s.authorize(ctx, subj, authz.ActProfileWrite, ids.Nil); err != nil {
		return ContactView{}, err
	}
	var out ContactView
	err := s.InTx(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.addContactTx(ctx, accountOf(subj), in)
		return err
	})
	return out, err
}

func (s *Service) addContactTx(ctx context.Context, accID ids.ID, in ContactInput) (ContactView, error) {
	tx := mustTx(ctx)
	n, err := s.q.CountOwnerContacts(ctx, tx, accID)
	if err != nil {
		return ContactView{}, err
	}
	if int(n) >= s.Policy.Domain.MaxContacts {
		return ContactView{}, precondition("CONTACT_LIMIT_REACHED", "The maximum number of contacts has been reached.")
	}
	var email *string
	if in.Email != nil {
		e := strings.ToLower(*in.Email)
		email = &e
	}
	c, err := s.q.InsertOwnerContact(ctx, tx, db.InsertOwnerContactParams{ID: ids.New(), AccountID: accID, ContactType: in.ContactType,
		Label: in.Label, Name: in.Name, Relationship: in.Relationship, Email: email, PhoneE164: in.Phone, CreatedAt: s.now()})
	if err != nil {
		return ContactView{}, err
	}
	return contactView(c), s.record(ctx, "owner.contact.add", "owner_contact", c.ID, map[string]any{"contact_type": c.ContactType}, nil)
}

// RemoveContact removes one of the caller's contacts.
func (s *Service) RemoveContact(ctx context.Context, subj authz.Subject, id ids.ID) (Removed, error) {
	if _, err := s.authorize(ctx, subj, authz.ActProfileWrite, ids.Nil); err != nil {
		return Removed{}, err
	}
	err := s.InTx(ctx, func(ctx context.Context) error {
		n, err := s.q.RemoveOwnerContact(ctx, mustTx(ctx), db.RemoveOwnerContactParams{ID: id, AccountID: accountOf(subj), RemovedAt: ptr(s.now())})
		if err != nil {
			return err
		}
		if n == 0 {
			return notFound("contact")
		}
		return s.record(ctx, "owner.contact.remove", "owner_contact", id, nil, nil)
	})
	return Removed{ID: id, Removed: err == nil}, err
}

// Removed acknowledges a removal.
type Removed struct {
	ID      ids.ID `json:"id"`
	Removed bool   `json:"removed"`
}

// AccountStatusView is §13.2 /account/status: lifecycle state, activation
// rule and the policy gates that currently allow or block sensitive actions.
type AccountStatusView struct {
	AccountID          ids.ID     `json:"account_id"`
	Status             string     `json:"status"`
	ActivationRule     string     `json:"activation_rule"`
	EmailVerified      bool       `json:"email_verified"`
	PhoneVerified      bool       `json:"phone_verified"`
	MFAEnabled         bool       `json:"mfa_enabled"`
	IdentityStatus     string     `json:"identity_verification_status" doc:"NONE or the latest IDENTITY verification status"`
	IdentityExpiresAt  *time.Time `json:"identity_verification_expires_at"`
	Gates              []GateView `json:"gates"`
	PolicyVersion      string     `json:"policy_version"`
	ClosureEffectiveAt *time.Time `json:"closure_effective_at"`
}

// GateView reports whether a sensitive action is currently allowed.
type GateView struct {
	Action  string   `json:"action"`
	Allowed bool     `json:"allowed"`
	Missing []string `json:"missing" doc:"Unmet requirements, e.g. ACCOUNT_ACTIVE, IDENTITY_VERIFIED"`
}

// AccountStatus reports the caller's status and gates.
func (s *Service) AccountStatus(ctx context.Context, subj authz.Subject) (AccountStatusView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActProfileRead, ids.Nil); err != nil {
		return AccountStatusView{}, err
	}
	q := s.dbq(ctx)
	acc, err := s.q.GetAccount(ctx, q, accountOf(subj))
	if err != nil {
		return AccountStatusView{}, err
	}
	v := AccountStatusView{AccountID: acc.AccountID, Status: acc.Status, ActivationRule: string(s.Policy.Activation),
		EmailVerified: acc.EmailVerifiedAt != nil, PhoneVerified: acc.PhoneVerifiedAt != nil, MFAEnabled: acc.MfaEnabled,
		IdentityStatus: "NONE", PolicyVersion: s.Policy.Version, ClosureEffectiveAt: acc.ClosureEffectiveAt}
	link, err := s.q.LatestVerificationLink(ctx, q, db.LatestVerificationLinkParams{SubjectType: "USER", SubjectID: acc.AccountID, VerificationType: "IDENTITY"})
	if err != nil && !isNoRows(err) {
		return AccountStatusView{}, err
	}
	if err == nil {
		v.IdentityStatus, v.IdentityExpiresAt = link.Status, link.ExpiresAt
	}
	idOK, err := s.q.IdentityVerified(ctx, q, db.IdentityVerifiedParams{AccountID: acc.AccountID, Now: s.now()})
	if err != nil {
		return AccountStatusView{}, err
	}
	for _, g := range []string{GateListingPublish, GatePayout, GateSale, GateTransfer} {
		gv := GateView{Action: g, Missing: []string{}}
		if acc.Status != "ACTIVE" {
			gv.Missing = append(gv.Missing, "ACCOUNT_ACTIVE")
		}
		if slices.Contains(s.Policy.Domain.IdentityRequiredFor, g) && !idOK {
			gv.Missing = append(gv.Missing, "IDENTITY_VERIFIED")
		}
		gv.Allowed = len(gv.Missing) == 0
		v.Gates = append(v.Gates, gv)
	}
	return v, nil
}

// requireIdentity enforces the identity-verification policy gate.
func (s *Service) requireIdentity(ctx context.Context, accountID ids.ID, gate string) error {
	if accountID.IsZero() || !slices.Contains(s.Policy.Domain.IdentityRequiredFor, gate) {
		return nil
	}
	ok, err := s.q.IdentityVerified(ctx, s.dbq(ctx), db.IdentityVerifiedParams{AccountID: accountID, Now: s.now()})
	if err != nil {
		return err
	}
	if !ok {
		return precondition("IDENTITY_VERIFICATION_REQUIRED", "Verify your identity before performing this action.")
	}
	return nil
}
