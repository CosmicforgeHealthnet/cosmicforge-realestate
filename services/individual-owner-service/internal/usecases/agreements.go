package usecases

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/esign"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Agreements (§13.13). An agreement is versioned: every term/document change
// is a new immutable agreement_versions row, and no new version can be made
// while signature invitations for a version are outstanding. Signing happens
// through the e-sign adapter (ESIGN) or, while no provider is configured,
// OFFLINE: a signed scan stored in media-document-service plus confirmation
// by each marketplace counterparty (owner attestation only for off-platform
// parties). An EFFECTIVE lease creates the tenancy and its obligations.

// AggAgreement is the agreement aggregate.
const (
	AggAgreement = "agreement"
	AggTenancy   = "tenancy"
)

// AgreementTerms are the structured business terms of an agreement version.
type AgreementTerms struct {
	StartDate          string                 `json:"start_date" v:"required,format=date"`
	TermMonths         int32                  `json:"term_months" v:"required,min=1,max=600"`
	Currency           string                 `json:"currency" v:"required,format=currency"`
	RentMinor          int64                  `json:"rent_minor" v:"required,min=1" doc:"Rent per cycle at the start of the term"`
	RecurringFeesMinor int64                  `json:"recurring_fees_minor,omitempty" v:"min=0" doc:"Mandatory recurring fees per cycle"`
	Frequency          string                 `json:"frequency" v:"required,enum=MONTHLY|QUARTERLY|BIANNUAL|ANNUAL|ONE_TIME"`
	DueDay             int32                  `json:"due_day" v:"required,min=1,max=28"`
	EscalationMonths   int32                  `json:"escalation_after_months,omitempty" v:"min=1,max=600"`
	EscalationBps      int32                  `json:"escalation_bps,omitempty" v:"min=-10000,max=10000"`
	RentFreeMonths     int32                  `json:"rent_free_months,omitempty" v:"min=1,max=120" doc:"Leading rent-free months (whole cycles), e.g. commercial fit-out"`
	RentCollection     string                 `json:"rent_collection" v:"required,enum=PLATFORM|OFF_PLATFORM" doc:"PLATFORM collects through payment-service"`
	DepositMinor       int64                  `json:"deposit_minor,omitempty" v:"min=0"`
	DepositHandling    string                 `json:"deposit_handling" v:"required,enum=PLATFORM|OFF_PLATFORM|NONE" doc:"PLATFORM holds the deposit in payment-service"`
	DepositReleaseDays int32                  `json:"deposit_release_days,omitempty" v:"min=1,max=180"`
	LateFeeMinor       int64                  `json:"late_fee_minor,omitempty" v:"min=0" doc:"Flat late fee per overdue period (capped by policy)"`
	LateFeeGraceDays   int32                  `json:"late_fee_grace_days,omitempty" v:"min=0,max=60"`
	Clauses            []string               `json:"clauses,omitempty" v:"max=100" doc:"Template clause codes included (all required clauses must be present)"`
	HouseRules         []string               `json:"house_rules,omitempty" v:"max=30"`
	SpecialConditions  []string               `json:"special_conditions,omitempty" v:"max=30"`
	InventoryNotes     *string                `json:"inventory_notes,omitempty" v:"max=4000"`
	Commercial         *CommercialLeaseExtras `json:"commercial,omitempty" doc:"Commercial lease extensions (§13.17)"`
}

// CommercialLeaseExtras are the typed commercial terms of a lease.
type CommercialLeaseExtras struct {
	PermittedUse      string `json:"permitted_use" v:"required,max=200"`
	FitOutDays        int32  `json:"fit_out_days,omitempty" v:"min=1,max=730"`
	BreakAfterMonths  int32  `json:"break_after_months,omitempty" v:"min=1,max=600"`
	BreakNoticeDays   int32  `json:"break_notice_days,omitempty" v:"min=1,max=730"`
	ServiceChargeBy   string `json:"service_charge_by" v:"required,enum=OWNER|TENANT|SHARED"`
	InsuranceBy       string `json:"insurance_by" v:"required,enum=OWNER|TENANT|SHARED"`
	RepairsBy         string `json:"repairs_by" v:"required,enum=OWNER|TENANT|SHARED"`
	SublettingAllowed bool   `json:"subletting_allowed"`
	SignageAllowed    bool   `json:"signage_allowed"`
	ParkingSpaces     int32  `json:"parking_spaces,omitempty" v:"min=1,max=10000"`
}

func (t AgreementTerms) rentTerms() domain.RentTerms {
	start, _ := time.Parse(time.DateOnly, t.StartDate)
	return domain.RentTerms{Start: start, TermMonths: t.TermMonths, Frequency: t.Frequency, DueDay: t.DueDay, RentMinor: t.RentMinor,
		FeesMinor: t.RecurringFeesMinor, EscalateAfter: t.EscalationMonths, EscalateBps: t.EscalationBps, RentFreeMonths: t.RentFreeMonths}
}

// validateTerms checks schema, schedule and policy rules of terms.
func (s *Service) validateTerms(t AgreementTerms) error {
	if err := schema.Validate(t); err != nil {
		return err
	}
	if err := domain.ValidateRentTerms(t.rentTerms()); err != nil {
		return err
	}
	if (t.DepositHandling == "NONE") != (t.DepositMinor == 0) {
		return apperrors.InvalidRequest("deposit_minor must be positive unless deposit_handling is NONE (and zero when it is).")
	}
	if max := t.RentMinor * s.Policy.Domain.MaxLateFeeBps / 10000; t.LateFeeMinor > max {
		return precondition("LATE_FEE_ABOVE_POLICY", fmt.Sprintf("The late fee may not exceed %d (policy cap of %d bps of the cycle rent).", max, s.Policy.Domain.MaxLateFeeBps))
	}
	for _, l := range [][]string{t.HouseRules, t.SpecialConditions} {
		for _, r := range l {
			if strings.TrimSpace(r) == "" || len(r) > 500 {
				return apperrors.InvalidRequest("Rules and conditions must be 1–500 characters each.")
			}
		}
	}
	if (t.RentCollection == "PLATFORM" || t.DepositHandling == "PLATFORM") && s.Payment == nil && !s.hasCoreFinancial() {
		return unavailable("Platform rent and deposit collection", "payment-service")
	}
	return nil
}

func parseTerms(raw []byte) AgreementTerms {
	var t AgreementTerms
	_ = json.Unmarshal(raw, &t)
	return t
}

// AgreementVersionView is one immutable version.
type AgreementVersionView struct {
	VersionNo         int32          `json:"version_no"`
	Terms             AgreementTerms `json:"terms"`
	DocumentMediaID   *ids.ID        `json:"document_media_id" doc:"Agreement document in media-document-service"`
	TemplateID        *ids.ID        `json:"template_id"`
	TemplateVersionNo *int32         `json:"template_version_no"`
	ChangeNote        *string        `json:"change_note"`
	CreatedAt         time.Time      `json:"created_at"`
}

func agreementVersionView(v db.AgreementVersion) AgreementVersionView {
	return AgreementVersionView{VersionNo: v.VersionNo, Terms: parseTerms(v.Terms), DocumentMediaID: v.DocumentMediaID, TemplateID: v.TemplateID,
		TemplateVersionNo: v.TemplateVersionNo, ChangeNote: v.ChangeNote, CreatedAt: v.CreatedAt}
}

// AgreementParticipantView is a party to the agreement.
type AgreementParticipantView struct {
	ID           ids.ID  `json:"id"`
	Role         string  `json:"role"`
	PartyDomain  string  `json:"party_domain"`
	PartyRefID   *ids.ID `json:"party_ref_id"`
	DisplayName  string  `json:"display_name"`
	SigningEmail *string `json:"signing_email"`
	SignRequired bool    `json:"sign_required"`
}

// SignatureView is one signer's state for the signing version.
type SignatureView struct {
	ParticipantID      ids.ID     `json:"participant_id"`
	Method             string     `json:"method"`
	Status             string     `json:"status"`
	AttestedBy         *string    `json:"attested_by" doc:"PROVIDER (e-sign), SIGNER (counterparty confirmed) or OWNER (off-platform party, evidence attached)"`
	EvidenceMediaID    *ids.ID    `json:"evidence_media_id"`
	CertificateMediaID *ids.ID    `json:"certificate_media_id"`
	SignedAt           *time.Time `json:"signed_at"`
	DeclinedReason     *string    `json:"declined_reason"`
}

// AgreementView is an agreement with its current version, parties and signatures.
type AgreementView struct {
	ID                ids.ID                     `json:"id"`
	PropertyID        ids.ID                     `json:"property_id"`
	UnitID            *ids.ID                    `json:"unit_id"`
	ListingID         *ids.ID                    `json:"listing_id"`
	OfferID           *ids.ID                    `json:"offer_id"`
	LOIID             *ids.ID                    `json:"loi_id"`
	ParentAgreementID *ids.ID                    `json:"parent_agreement_id"`
	RenewalCaseID     *ids.ID                    `json:"renewal_case_id"`
	TenancyID         *ids.ID                    `json:"tenancy_id"`
	AgreementKind     string                     `json:"agreement_kind"`
	Source            string                     `json:"source"`
	Status            string                     `json:"status"`
	CustomerRefID     *ids.ID                    `json:"customer_ref_id"`
	TemplateID        *ids.ID                    `json:"template_id"`
	PricingVersionID  *ids.ID                    `json:"pricing_version_id" doc:"Frozen price snapshot accepted with the offer"`
	CurrentVersion    *AgreementVersionView      `json:"current_version"`
	Participants      []AgreementParticipantView `json:"participants"`
	SigningMethod     *string                    `json:"signing_method"`
	SigningVersionNo  *int32                     `json:"signing_version_no"`
	Signatures        []SignatureView            `json:"signatures"`
	SigningStartedAt  *time.Time                 `json:"signing_started_at"`
	FullySignedAt     *time.Time                 `json:"fully_signed_at"`
	EffectiveAt       *time.Time                 `json:"effective_at"`
	CancelledAt       *time.Time                 `json:"cancelled_at"`
	CancelReason      *string                    `json:"cancel_reason"`
	Version           int64                      `json:"version"`
	CreatedAt         time.Time                  `json:"created_at"`
	UpdatedAt         time.Time                  `json:"updated_at"`
}

func (s *Service) agreementView(ctx context.Context, a db.Agreement) (AgreementView, error) {
	q := s.dbq(ctx)
	v := AgreementView{ID: a.ID, PropertyID: a.PropertyID, UnitID: a.UnitID, ListingID: a.ListingID, OfferID: a.OfferID, LOIID: a.LoiID, ParentAgreementID: a.ParentAgreementID,
		RenewalCaseID: a.RenewalCaseID, TenancyID: a.TenancyID, AgreementKind: a.AgreementKind, Source: a.Source, Status: a.Status, CustomerRefID: a.CustomerRefID,
		TemplateID: a.TemplateID, PricingVersionID: a.PricingVersionID, SigningMethod: a.SigningMethod, SigningVersionNo: a.SigningVersionNo,
		SigningStartedAt: a.SigningStartedAt, FullySignedAt: a.FullySignedAt, EffectiveAt: a.EffectiveAt, CancelledAt: a.CancelledAt,
		CancelReason: a.CancelReason, Version: a.Version, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
		Participants: []AgreementParticipantView{}, Signatures: []SignatureView{}}
	if a.CurrentVersionNo > 0 {
		cv, err := s.q.GetAgreementVersion(ctx, q, db.GetAgreementVersionParams{AgreementID: a.ID, VersionNo: a.CurrentVersionNo})
		if err != nil {
			return v, err
		}
		vv := agreementVersionView(cv)
		v.CurrentVersion = &vv
	}
	ps, err := s.q.ListAgreementParticipants(ctx, q, a.ID)
	if err != nil {
		return v, err
	}
	for _, p := range ps {
		v.Participants = append(v.Participants, AgreementParticipantView{ID: p.ID, Role: p.Role, PartyDomain: p.PartyDomain, PartyRefID: p.PartyRefID,
			DisplayName: p.DisplayName, SigningEmail: p.SigningEmail, SignRequired: p.SignRequired})
	}
	if a.SigningVersionNo != nil {
		srs, err := s.q.ListSignatureRequests(ctx, q, db.ListSignatureRequestsParams{AgreementID: a.ID, AgreementVersionNo: *a.SigningVersionNo})
		if err != nil {
			return v, err
		}
		for _, r := range srs {
			v.Signatures = append(v.Signatures, SignatureView{ParticipantID: r.ParticipantID, Method: r.Method, Status: r.Status, AttestedBy: r.AttestedBy,
				EvidenceMediaID: r.EvidenceMediaID, CertificateMediaID: r.CertificateMediaID, SignedAt: r.SignedAt, DeclinedReason: r.DeclinedReason})
		}
	}
	return v, nil
}

// agreementFor authorizes and (for mutations) locks an agreement.
func (s *Service) agreementFor(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.Agreement, authz.Decision, error) {
	a, err := s.q.GetAgreement(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return a, authz.Decision{}, notFound(AggAgreement)
	}
	if err != nil {
		return a, authz.Decision{}, err
	}
	d, err := s.authorize(ctx, subj, action, a.PropertyID)
	if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
		err = notFound(AggAgreement)
	}
	if err == nil && action != authz.Read(authz.CapAgreementsManage) {
		a, err = s.q.GetAgreementForUpdate(ctx, mustTx(ctx), id)
	}
	return a, d, err
}

func (s *Service) agreementTransition(ctx context.Context, a db.Agreement, to, cmd, reason string) error {
	_, _, err := s.transition(ctx, domain.Agreement, a.ID, lifecycle.Change{To: to, Command: cmd, Reason: reason})
	return err
}

var leaseKinds = map[string]string{"RENT_LONG_TERM": "RESIDENTIAL_LEASE", "COMMERCIAL_LEASE": "COMMERCIAL_LEASE", "LAND_LEASE": "LAND_LEASE"}

// createAgreementFromOffer prepares the DRAFT agreement when a rental offer
// is accepted (§14.2 step 6): terms come from the frozen price snapshot.
func (s *Service) createAgreementFromOffer(ctx context.Context, o db.RentalOffer) error {
	tx, now := mustTx(ctx), s.now()
	l, err := s.q.GetListing(ctx, tx, o.ListingID)
	if err != nil {
		return err
	}
	kind, ok := leaseKinds[l.TransactionType]
	if !ok {
		return nil
	}
	var cost domain.Cost
	_ = json.Unmarshal(o.CostSnapshot, &cost)
	_, model, err := s.loadPricing(ctx, o.PricingVersionID, nil)
	if err != nil {
		return err
	}
	var rent int64
	for _, line := range cost.Lines {
		if line.Code == "rent" {
			rent = line.AmountMinor
		}
	}
	freq := cost.Cycle
	if freq == "" {
		freq = "MONTHLY"
	}
	terms := AgreementTerms{StartDate: o.StartDate.Format(time.DateOnly), TermMonths: o.TermMonths, Currency: cost.Currency, RentMinor: rent,
		RecurringFeesMinor: max(cost.RecurringPerCycle-rent, 0), Frequency: freq, DueDay: min(int32(o.StartDate.Day()), 28),
		RentCollection: "OFF_PLATFORM", DepositMinor: model.Deposit, DepositHandling: "NONE", DepositReleaseDays: s.Policy.Domain.DepositReleaseDays,
		SpecialConditions: nonNil(o.Conditions)}
	if s.Payment != nil {
		terms.RentCollection = "PLATFORM"
	}
	if model.Deposit > 0 {
		terms.DepositHandling = "OFF_PLATFORM"
		if s.Payment != nil {
			terms.DepositHandling = "PLATFORM"
		}
	}
	for _, r := range model.Rules {
		if r.Type == "ESCALATION" {
			terms.EscalationMonths, terms.EscalationBps = r.Params.AfterMonths, r.Params.RateBps
		}
	}
	a, err := s.q.InsertAgreement(ctx, tx, db.InsertAgreementParams{ID: ids.New(), PropertyID: o.PropertyID, UnitID: o.UnitID, ListingID: &o.ListingID,
		OfferID: &o.ID, AgreementKind: kind, Source: "RENTAL_OFFER", CustomerRefID: &o.CustomerRefID, PricingVersionID: &o.PricingVersionID, CreatedAt: now})
	if isUnique(err, "agreements_offer_uniq") {
		return nil
	}
	if err != nil {
		return err
	}
	if err := domain.Agreement.RecordCreation(ctx, tx, a.ID, &a.PropertyID, "DRAFT", now); err != nil {
		return err
	}
	if _, err := s.newAgreementVersion(ctx, a, terms, nil, nil, "Prepared from the accepted rental offer"); err != nil {
		return err
	}
	// Parties: the owner of record and the applicants of the application.
	p, err := s.q.GetProperty(ctx, tx, o.PropertyID)
	if err != nil {
		return err
	}
	acc, err := s.q.GetAccount(ctx, tx, p.OwnerAccountID)
	if err != nil {
		return err
	}
	if _, err := s.q.InsertAgreementParticipant(ctx, tx, db.InsertAgreementParticipantParams{ID: ids.New(), AgreementID: a.ID, Role: "OWNER",
		PartyDomain: string(platform.DomainIndividualOwner), PartyRefID: &acc.AccountID, DisplayName: acc.DisplayName, SigningEmail: &acc.Email,
		SignRequired: true, CreatedAt: now}); err != nil {
		return err
	}
	parts, err := s.q.ListParticipants(ctx, tx, o.ApplicationID)
	if err != nil {
		return err
	}
	roles := map[string]string{"PRIMARY": "TENANT", "CO_APPLICANT": "CO_TENANT", "GUARANTOR": "GUARANTOR"}
	for _, ap := range parts {
		role, ok := roles[ap.Role]
		if !ok {
			continue
		}
		ref, dom := ap.CustomerRefID, string(platform.DomainMarketplace)
		if ap.Role == "PRIMARY" {
			ref = &o.CustomerRefID
		}
		if ref == nil {
			dom = "external"
		}
		if _, err := s.q.InsertAgreementParticipant(ctx, tx, db.InsertAgreementParticipantParams{ID: ids.New(), AgreementID: a.ID, Role: role,
			PartyDomain: dom, PartyRefID: ref, DisplayName: ap.DisplayName, SignRequired: true, CreatedAt: now}); err != nil {
			return err
		}
	}
	if err := s.propertyHistory(ctx, a.PropertyID, "AGREEMENT_PREPARED", "Agreement prepared from accepted offer", AggAgreement, a.ID); err != nil {
		return err
	}
	return s.record(ctx, "agreement.create", AggAgreement, a.ID, map[string]any{"source": "RENTAL_OFFER", "offer_id": o.ID}, nil)
}

func (s *Service) newAgreementVersion(ctx context.Context, a db.Agreement, t AgreementTerms, doc *ids.ID, tpl *db.AgreementTemplate, note string) (db.AgreementVersion, error) {
	tx := mustTx(ctx)
	var tplID *ids.ID
	var tplNo *int32
	if tpl != nil {
		tplID, tplNo = &tpl.ID, &tpl.VersionNo
	}
	var dom *string
	var actor *ids.ID
	if a := actorFromCtx(ctx); a != nil {
		dom, actor = &a.Domain, &a.ID
	}
	if t.Clauses == nil {
		t.Clauses = []string{}
	}
	v, err := s.q.InsertAgreementVersion(ctx, tx, db.InsertAgreementVersionParams{ID: ids.New(), AgreementID: a.ID, Terms: jsonb(t), DocumentMediaID: doc,
		TemplateID: tplID, TemplateVersionNo: tplNo, ChangeNote: strp(note), ActorDomain: dom, ActorID: actor, CreatedAt: s.now()})
	if err != nil {
		return v, err
	}
	return v, s.q.SetAgreementVersionNo(ctx, tx, db.SetAgreementVersionNoParams{ID: a.ID, CurrentVersionNo: v.VersionNo, TemplateID: tplID, UpdatedAt: s.now()})
}

// TenantPartyInput names the tenant of a direct agreement.
type TenantPartyInput struct {
	DisplayName   string  `json:"display_name" v:"required,max=120"`
	CustomerRefID *ids.ID `json:"customer_ref_id,omitempty" doc:"Marketplace customer reference when the tenant is on the platform"`
	SigningEmail  *string `json:"signing_email,omitempty" v:"format=email"`
}

// CreateAgreementInput creates a DIRECT agreement (e.g. an existing tenant).
type CreateAgreementInput struct {
	PropertyID      ids.ID           `json:"property_id" v:"required"`
	UnitID          *ids.ID          `json:"unit_id,omitempty"`
	ListingID       *ids.ID          `json:"listing_id,omitempty"`
	AgreementKind   string           `json:"agreement_kind" v:"required,enum=RESIDENTIAL_LEASE|COMMERCIAL_LEASE|LAND_LEASE"`
	TemplateID      *ids.ID          `json:"template_id,omitempty"`
	Terms           AgreementTerms   `json:"terms"`
	DocumentMediaID *ids.ID          `json:"document_media_id,omitempty"`
	Tenant          TenantPartyInput `json:"tenant"`
}

// MediaRefs lists the media referenced by agreement inputs.
func (in CreateAgreementInput) MediaRefs() []ids.ID { return optIDs(in.DocumentMediaID) }

// CreateAgreement creates a DIRECT draft agreement.
func (s *Service) CreateAgreement(ctx context.Context, subj authz.Subject, in CreateAgreementInput, ev Evidence) (AgreementView, error) {
	if err := schema.Validate(in); err != nil {
		return AgreementView{}, err
	}
	if err := s.validateTerms(in.Terms); err != nil {
		return AgreementView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return AgreementView{}, err
	}
	var out AgreementView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapAgreementsManage), in.PropertyID); err != nil {
			return err
		}
		if _, err := s.requireMutable(ctx, in.PropertyID); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		if err := s.checkSpace(ctx, in.PropertyID, in.UnitID, in.ListingID); err != nil {
			return err
		}
		var tpl *db.AgreementTemplate
		if in.TemplateID != nil {
			t, err := s.templateForProperty(ctx, *in.TemplateID, in.PropertyID, in.AgreementKind)
			if err != nil {
				return err
			}
			tpl = &t
		}
		a, err := s.q.InsertAgreement(ctx, tx, db.InsertAgreementParams{ID: ids.New(), PropertyID: in.PropertyID, UnitID: in.UnitID, ListingID: in.ListingID,
			AgreementKind: in.AgreementKind, Source: "DIRECT", CustomerRefID: in.Tenant.CustomerRefID, CreatedAt: now})
		if err != nil {
			return err
		}
		if err := domain.Agreement.RecordCreation(ctx, tx, a.ID, &a.PropertyID, "DRAFT", now); err != nil {
			return err
		}
		if _, err := s.newAgreementVersion(ctx, a, in.Terms, in.DocumentMediaID, tpl, "Initial draft"); err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, tx, in.PropertyID)
		if err != nil {
			return err
		}
		acc, err := s.q.GetAccount(ctx, tx, p.OwnerAccountID)
		if err != nil {
			return err
		}
		if _, err := s.q.InsertAgreementParticipant(ctx, tx, db.InsertAgreementParticipantParams{ID: ids.New(), AgreementID: a.ID, Role: "OWNER",
			PartyDomain: string(platform.DomainIndividualOwner), PartyRefID: &acc.AccountID, DisplayName: acc.DisplayName, SigningEmail: &acc.Email,
			SignRequired: true, CreatedAt: now}); err != nil {
			return err
		}
		dom := "external"
		if in.Tenant.CustomerRefID != nil {
			dom = string(platform.DomainMarketplace)
		}
		if _, err := s.q.InsertAgreementParticipant(ctx, tx, db.InsertAgreementParticipantParams{ID: ids.New(), AgreementID: a.ID, Role: "TENANT",
			PartyDomain: dom, PartyRefID: in.Tenant.CustomerRefID, DisplayName: in.Tenant.DisplayName, SigningEmail: in.Tenant.SigningEmail,
			SignRequired: true, CreatedAt: now}); err != nil {
			return err
		}
		if err := s.record(ctx, "agreement.create", AggAgreement, a.ID, map[string]any{"source": "DIRECT", "kind": in.AgreementKind}, nil); err != nil {
			return err
		}
		n, err := s.q.GetAgreement(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		out, err = s.agreementView(ctx, n)
		return err
	})
	return out, err
}

// checkSpace validates unit/listing references against the property.
func (s *Service) checkSpace(ctx context.Context, propertyID ids.ID, unitID, listingID *ids.ID) error {
	q := s.dbq(ctx)
	if unitID != nil {
		_, err := s.q.GetUnit(ctx, q, db.GetUnitParams{ID: *unitID, PropertyID: propertyID})
		if isNoRows(err) {
			return precondition("UNIT_NOT_IN_PROPERTY", "The unit does not belong to this property.")
		}
		if err != nil {
			return err
		}
	}
	if listingID != nil {
		l, err := s.q.GetListing(ctx, q, *listingID)
		if isNoRows(err) || (err == nil && l.PropertyID != propertyID) {
			return precondition("LISTING_NOT_IN_PROPERTY", "The listing does not belong to this property.")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) templateForProperty(ctx context.Context, templateID, propertyID ids.ID, kind string) (db.AgreementTemplate, error) {
	addr, err := s.q.GetPropertyAddress(ctx, s.dbq(ctx), propertyID)
	if isNoRows(err) {
		return db.AgreementTemplate{}, precondition("PROPERTY_ADDRESS_REQUIRED", "The property needs an address (jurisdiction) before choosing a legal template.")
	}
	if err != nil {
		return db.AgreementTemplate{}, err
	}
	return s.requireAgreementTemplate(ctx, templateID, addr.CountryCode, kind)
}

// GetAgreement returns an agreement.
func (s *Service) GetAgreement(ctx context.Context, subj authz.Subject, id ids.ID) (AgreementView, error) {
	a, _, err := s.agreementFor(ctx, subj, id, authz.Read(authz.CapAgreementsManage))
	if err != nil {
		return AgreementView{}, err
	}
	return s.agreementView(ctx, a)
}

// ListAgreementVersions returns the version history.
func (s *Service) ListAgreementVersions(ctx context.Context, subj authz.Subject, id ids.ID) ([]AgreementVersionView, error) {
	if _, _, err := s.agreementFor(ctx, subj, id, authz.Read(authz.CapAgreementsManage)); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAgreementVersions(ctx, s.dbq(ctx), id)
	out := make([]AgreementVersionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, agreementVersionView(r))
	}
	return out, err
}

// ListAgreements pages agreements across the caller's properties.
func (s *Service) ListAgreements(ctx context.Context, subj authz.Subject, propertyID ids.ID, status, cursor string, limit int) (pagination.Response[AgreementView], error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return pagination.Response[AgreementView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[AgreementView]{}, err
	}
	rows, err := s.q.ListAgreementsForAccount(ctx, s.dbq(ctx), db.ListAgreementsForAccountParams{AccountID: accountOf(subj), Now: s.now(),
		PropertyID: idp(propertyID), Status: strp(status), CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[AgreementView]{}, err
	}
	out := make([]AgreementView, 0, len(rows))
	for _, r := range rows {
		v, err := s.agreementView(ctx, r)
		if err != nil {
			return pagination.Response[AgreementView]{}, err
		}
		out = append(out, v)
	}
	return pagination.Build(out, limit, func(v AgreementView) any { return timeCursor{At: v.CreatedAt, ID: v.ID} })
}

// AgreementDraftInput creates the next draft version (PATCH, If-Match).
type AgreementDraftInput struct {
	TemplateID      *ids.ID        `json:"template_id,omitempty" doc:"ACTIVE template for the property's jurisdiction and agreement kind"`
	Terms           AgreementTerms `json:"terms"`
	DocumentMediaID *ids.ID        `json:"document_media_id,omitempty" doc:"Filled-in agreement document uploaded to media-document-service"`
	ChangeNote      *string        `json:"change_note,omitempty" v:"max=1000"`
}

// MediaRefs lists referenced media.
func (in AgreementDraftInput) MediaRefs() []ids.ID { return optIDs(in.DocumentMediaID) }

// UpdateAgreementDraft records a new version of a DRAFT agreement.
func (s *Service) UpdateAgreementDraft(ctx context.Context, subj authz.Subject, id ids.ID, expected int64, in AgreementDraftInput, ev Evidence) (AgreementView, error) {
	if err := schema.Validate(in); err != nil {
		return AgreementView{}, err
	}
	if err := s.validateTerms(in.Terms); err != nil {
		return AgreementView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return AgreementView{}, err
	}
	var out AgreementView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, _, err := s.agreementFor(ctx, subj, id, authz.Do(authz.CapAgreementsManage))
		if err != nil {
			return err
		}
		if err := checkVersion(expected, a.Version); err != nil {
			return err
		}
		if a.Status != "DRAFT" {
			return conflict("AGREEMENT_NOT_EDITABLE", "Only draft agreements can be edited (versions are immutable once under review or signing).")
		}
		var tpl *db.AgreementTemplate
		tplID := in.TemplateID
		if tplID == nil {
			tplID = a.TemplateID
		}
		if tplID != nil {
			t, err := s.templateForProperty(ctx, *tplID, a.PropertyID, a.AgreementKind)
			if err != nil {
				return err
			}
			tpl = &t
		}
		v, err := s.newAgreementVersion(ctx, a, in.Terms, in.DocumentMediaID, tpl, deref(in.ChangeNote))
		if err != nil {
			return err
		}
		if _, err := mustTx(ctx).Exec(ctx, `UPDATE agreements SET version = version + 1 WHERE id = $1`, a.ID); err != nil {
			return err
		}
		if err := s.record(ctx, "agreement.version", AggAgreement, id, map[string]any{"version_no": v.VersionNo}, nil); err != nil {
			return err
		}
		n, err := s.q.GetAgreement(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.agreementView(ctx, n)
		return err
	})
	return out, err
}

// SignerInput is one participant of the agreement.
type SignerInput struct {
	Role          string  `json:"role" v:"required,enum=OWNER|CO_OWNER|REPRESENTATIVE|TENANT|CO_TENANT|GUARANTOR|WITNESS"`
	DisplayName   string  `json:"display_name" v:"required,max=120"`
	PartyDomain   string  `json:"party_domain" v:"required,enum=individual-owner|marketplace|external"`
	PartyRefID    *ids.ID `json:"party_ref_id,omitempty" doc:"Owner account or marketplace customer reference (not for external parties)"`
	SigningEmail  *string `json:"signing_email,omitempty" v:"format=email"`
	SignRequired  *bool   `json:"sign_required,omitempty" doc:"Default true"`
	ParticipantID *ids.ID `json:"participant_id,omitempty" doc:"Existing participant to keep (omit to add)"`
}

// InviteSignersInput replaces the participant set of a draft or reviewed agreement.
type InviteSignersInput struct {
	Participants []SignerInput `json:"participants" v:"required,max=20"`
}

var ownerSideRoles = []string{"OWNER", "CO_OWNER", "REPRESENTATIVE"}
var tenantSideRoles = []string{"TENANT", "CO_TENANT", "GUARANTOR"}

// SetAgreementParticipants sets who signs (spec "invite-signers").
func (s *Service) SetAgreementParticipants(ctx context.Context, subj authz.Subject, id ids.ID, in InviteSignersInput) (AgreementView, error) {
	if err := schema.Validate(in); err != nil {
		return AgreementView{}, err
	}
	var owners, tenants int
	for _, p := range in.Participants {
		if (p.PartyDomain == "external") != (p.PartyRefID == nil) {
			return AgreementView{}, apperrors.InvalidRequest("party_ref_id is required for platform parties and forbidden for external ones.")
		}
		if p.PartyDomain == string(platform.DomainIndividualOwner) && !slices.Contains(ownerSideRoles, p.Role) {
			return AgreementView{}, apperrors.InvalidRequest("Owner accounts can only take owner-side roles.")
		}
		if slices.Contains(ownerSideRoles, p.Role) && p.PartyDomain == string(platform.DomainMarketplace) {
			return AgreementView{}, apperrors.InvalidRequest("Owner-side roles cannot be marketplace customers.")
		}
		if p.SignRequired == nil || *p.SignRequired {
			if slices.Contains(ownerSideRoles, p.Role) {
				owners++
			}
			if p.Role == "TENANT" {
				tenants++
			}
		}
	}
	if owners == 0 || tenants == 0 {
		return AgreementView{}, precondition("SIGNERS_INCOMPLETE", "At least one owner-side signer and the tenant must sign.")
	}
	var out AgreementView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, _, err := s.agreementFor(ctx, subj, id, authz.Do(authz.CapAgreementsManage))
		if err != nil {
			return err
		}
		if a.Status != "DRAFT" {
			return conflict("AGREEMENT_NOT_EDITABLE", "Participants can only change while the agreement is a draft.")
		}
		tx, now := mustTx(ctx), s.now()
		existing, err := s.q.ListAgreementParticipants(ctx, tx, id)
		if err != nil {
			return err
		}
		keep := map[ids.ID]bool{}
		for _, p := range in.Participants {
			if p.ParticipantID != nil {
				keep[*p.ParticipantID] = true
			}
		}
		for _, e := range existing {
			if !keep[e.ID] {
				if _, err := s.q.RemoveAgreementParticipant(ctx, tx, db.RemoveAgreementParticipantParams{ID: e.ID, AgreementID: id, RemovedAt: &now}); err != nil {
					return err
				}
			}
		}
		for _, p := range in.Participants {
			if p.ParticipantID != nil {
				if !slices.ContainsFunc(existing, func(e db.AgreementParticipant) bool { return e.ID == *p.ParticipantID }) {
					return precondition("PARTICIPANT_NOT_FOUND", "participant_id does not belong to this agreement.")
				}
				continue
			}
			if p.PartyDomain == string(platform.DomainIndividualOwner) {
				if err := s.requirePropertyPrincipal(ctx, a.PropertyID, *p.PartyRefID); err != nil {
					return err
				}
			}
			req := p.SignRequired == nil || *p.SignRequired
			if _, err := s.q.InsertAgreementParticipant(ctx, tx, db.InsertAgreementParticipantParams{ID: ids.New(), AgreementID: id, Role: p.Role,
				PartyDomain: p.PartyDomain, PartyRefID: p.PartyRefID, DisplayName: p.DisplayName, SigningEmail: p.SigningEmail, SignRequired: req,
				CreatedAt: now}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE agreements SET version = version + 1, updated_at = $2 WHERE id = $1`, id, now); err != nil {
			return err
		}
		if err := s.record(ctx, "agreement.participants", AggAgreement, id, map[string]any{"participants": len(in.Participants)}, nil); err != nil {
			return err
		}
		n, err := s.q.GetAgreement(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.agreementView(ctx, n)
		return err
	})
	return out, err
}

// requirePropertyPrincipal checks an owner account holds an owner-side role.
func (s *Service) requirePropertyPrincipal(ctx context.Context, propertyID, accountID ids.ID) error {
	var n int
	err := s.dbq(ctx).QueryRow(ctx, `SELECT count(*) FROM property_access WHERE property_id = $1 AND account_id = $2
		AND role IN ('OWNER','CO_OWNER','REPRESENTATIVE') AND (valid_until IS NULL OR valid_until > $3)`, propertyID, accountID, s.now()).Scan(&n)
	if err == nil && n == 0 {
		return precondition("PARTICIPANT_NOT_PRINCIPAL", "The owner account is not an owner, co-owner or representative of this property.")
	}
	return err
}

// checkJurisdictionTerms applies the approved jurisdiction policy to terms
// before they go out for signature: late fee cap and deposit settlement
// deadline.
func (s *Service) checkJurisdictionTerms(ctx context.Context, propertyID ids.ID, t AgreementTerms) error {
	lp, err := s.legalPolicy(ctx, propertyID)
	if err != nil || !lp.Approved {
		return err
	}
	if max := t.RentMinor * lp.Rules.MaxLateFeeBps / 10000; t.LateFeeMinor > max {
		return precondition("LATE_FEE_ABOVE_POLICY", fmt.Sprintf("The late fee may not exceed %d (jurisdiction cap of %d bps of the cycle rent).", max, lp.Rules.MaxLateFeeBps))
	}
	if t.DepositReleaseDays > lp.Rules.DepositReleaseDaysMax {
		return precondition("DEPOSIT_RELEASE_ABOVE_POLICY", fmt.Sprintf("The deposit must be settled within %d days of move-out in this jurisdiction.", lp.Rules.DepositReleaseDaysMax))
	}
	return nil
}

// readyForReview checks the current version is complete.
func (s *Service) readyForReview(ctx context.Context, a db.Agreement) error {
	if a.CurrentVersionNo == 0 || a.TemplateID == nil {
		return precondition("AGREEMENT_TEMPLATE_REQUIRED", "Choose the legal template for this agreement first.")
	}
	q := s.dbq(ctx)
	v, err := s.q.GetAgreementVersion(ctx, q, db.GetAgreementVersionParams{AgreementID: a.ID, VersionNo: a.CurrentVersionNo})
	if err != nil {
		return err
	}
	if v.DocumentMediaID == nil {
		return precondition("AGREEMENT_DOCUMENT_REQUIRED", "Attach the completed agreement document first.")
	}
	t := parseTerms(v.Terms)
	if err := s.validateTerms(t); err != nil {
		return err
	}
	if err := s.checkJurisdictionTerms(ctx, a.PropertyID, t); err != nil {
		return err
	}
	tpl, err := s.q.GetAgreementTemplate(ctx, q, *a.TemplateID)
	if err != nil {
		return err
	}
	if tpl.Status != "ACTIVE" {
		return precondition("AGREEMENT_TEMPLATE_RETIRED", "The template version was retired; re-draft with the current version.")
	}
	for _, c := range requiredClauses(tpl) {
		if !slices.Contains(t.Clauses, c) {
			return precondition("REQUIRED_CLAUSE_MISSING", "Required clause "+c+" of the template is missing from the terms.")
		}
	}
	ps, err := s.q.ListAgreementParticipants(ctx, q, a.ID)
	if err != nil {
		return err
	}
	var owner, tenant bool
	for _, p := range ps {
		owner = owner || (p.SignRequired && slices.Contains(ownerSideRoles, p.Role))
		tenant = tenant || (p.SignRequired && p.Role == "TENANT")
	}
	if !owner || !tenant {
		return precondition("SIGNERS_INCOMPLETE", "At least one owner-side signer and the tenant must sign.")
	}
	return nil
}

// SubmitAgreementForReview shares the draft with the counterparty (DRAFT → REVIEW).
func (s *Service) SubmitAgreementForReview(ctx context.Context, subj authz.Subject, id ids.ID) (AgreementView, error) {
	return s.agreementCommand(ctx, subj, id, func(ctx context.Context, a db.Agreement, _ authz.Decision) error {
		if err := s.readyForReview(ctx, a); err != nil {
			return err
		}
		return s.agreementTransition(ctx, a, "REVIEW", "submit_review", "")
	}, "agreement.review")
}

// ReturnAgreementToDraft reopens a reviewed agreement for editing.
func (s *Service) ReturnAgreementToDraft(ctx context.Context, subj authz.Subject, id ids.ID) (AgreementView, error) {
	return s.agreementCommand(ctx, subj, id, func(ctx context.Context, a db.Agreement, _ authz.Decision) error {
		if a.Status != "REVIEW" {
			return apperrors.InvalidStateTransition(a.Status, "DRAFT")
		}
		return s.agreementTransition(ctx, a, "DRAFT", "reopen_draft", "")
	}, "agreement.reopen")
}

func (s *Service) agreementCommand(ctx context.Context, subj authz.Subject, id ids.ID, fn func(ctx context.Context, a db.Agreement, d authz.Decision) error, action string) (AgreementView, error) {
	var out AgreementView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, d, err := s.agreementFor(ctx, subj, id, authz.Do(authz.CapAgreementsManage))
		if err != nil {
			return err
		}
		if err := fn(ctx, a, d); err != nil {
			return err
		}
		if err := s.record(ctx, action, AggAgreement, id, nil, nil); err != nil {
			return err
		}
		n, err := s.q.GetAgreement(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.agreementView(ctx, n)
		return err
	})
	return out, err
}

// SendForSignatureInput starts signing.
type SendForSignatureInput struct {
	Method string `json:"method" v:"required,enum=ESIGN|OFFLINE" doc:"ESIGN needs a configured e-sign provider; OFFLINE uses signed scans + counterparty confirmation"`
}

// SendForSignature freezes the current version and invites signers
// (REVIEW → SIGNING). Leases need co-owner consent and verified authority.
func (s *Service) SendForSignature(ctx context.Context, subj authz.Subject, id ids.ID, in SendForSignatureInput) (AgreementView, error) {
	if err := schema.Validate(in); err != nil {
		return AgreementView{}, err
	}
	if in.Method == "ESIGN" && s.ESign == nil {
		return AgreementView{}, unavailable("Electronic signature", "an e-sign provider")
	}
	return s.agreementCommand(ctx, subj, id, func(ctx context.Context, a db.Agreement, d authz.Decision) error {
		if a.Status != "REVIEW" {
			return apperrors.InvalidStateTransition(a.Status, "SIGNING")
		}
		if err := s.readyForReview(ctx, a); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		st, err := s.q.PropertyAuthorityState(ctx, tx, db.PropertyAuthorityStateParams{Now: now, PropertyID: a.PropertyID})
		if err != nil {
			return err
		}
		if st.Status != "ACTIVE" || !st.AuthorityVerified {
			return precondition("PROPERTY_AUTHORITY_UNVERIFIED", "Verified ownership authority on an active property is required before signing a lease.")
		}
		if err := s.requireConsent(ctx, d, a.PropertyID, ConsentLease, a.ID, s.actingAccount(ctx, subj, a.PropertyID)); err != nil {
			return err
		}
		if err := s.q.SetAgreementSigning(ctx, tx, db.SetAgreementSigningParams{ID: a.ID, SigningMethod: &in.Method, SigningVersionNo: &a.CurrentVersionNo,
			SigningStartedAt: &now}); err != nil {
			return err
		}
		if err := s.agreementTransition(ctx, a, "SIGNING", "send_for_signature", ""); err != nil {
			return err
		}
		ps, err := s.q.ListAgreementParticipants(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		for _, p := range ps {
			if !p.SignRequired {
				continue
			}
			if in.Method == "ESIGN" && p.SigningEmail == nil {
				return precondition("SIGNING_EMAIL_REQUIRED", "Every signer needs a signing email for electronic signature ("+p.DisplayName+").")
			}
			if _, err := s.q.InsertSignatureRequest(ctx, tx, db.InsertSignatureRequestParams{ID: ids.New(), AgreementID: a.ID, AgreementVersionNo: a.CurrentVersionNo,
				ParticipantID: p.ID, Method: in.Method, Status: "PENDING", CreatedAt: now}); err != nil {
				return err
			}
		}
		return s.propertyHistory(ctx, a.PropertyID, "AGREEMENT_SIGNING", "Agreement sent for signature ("+lower(in.Method)+")", AggAgreement, a.ID)
	}, "agreement.send_for_signature")
}

// OfflineSignatureInput records a signature collected outside the platform.
type OfflineSignatureInput struct {
	ParticipantID   ids.ID    `json:"participant_id" v:"required"`
	EvidenceMediaID ids.ID    `json:"evidence_media_id" v:"required" doc:"Signed page/scan uploaded to media-document-service"`
	SignedAt        time.Time `json:"signed_at" v:"required"`
}

// MediaRefs lists referenced media.
func (in OfflineSignatureInput) MediaRefs() []ids.ID { return []ids.ID{in.EvidenceMediaID} }

// RecordOfflineSignature attaches signed evidence for a participant. Owner-
// side and off-platform parties are SIGNED on owner attestation; marketplace
// customers must confirm through marketplace-service.
func (s *Service) RecordOfflineSignature(ctx context.Context, subj authz.Subject, id ids.ID, in OfflineSignatureInput, ev Evidence) (AgreementView, error) {
	if err := schema.Validate(in); err != nil {
		return AgreementView{}, err
	}
	if err := ev.require(in.EvidenceMediaID); err != nil {
		return AgreementView{}, err
	}
	if in.SignedAt.After(s.now().Add(5 * time.Minute)) {
		return AgreementView{}, apperrors.InvalidRequest("signed_at cannot be in the future.")
	}
	return s.agreementCommand(ctx, subj, id, func(ctx context.Context, a db.Agreement, _ authz.Decision) error {
		if a.Status != "SIGNING" || deref(a.SigningMethod) != "OFFLINE" {
			return conflict("AGREEMENT_NOT_OFFLINE_SIGNING", "The agreement is not awaiting offline signatures.")
		}
		tx := mustTx(ctx)
		sr, err := s.q.GetSignatureRequestForUpdate(ctx, tx, db.GetSignatureRequestForUpdateParams{AgreementID: a.ID, AgreementVersionNo: *a.SigningVersionNo,
			ParticipantID: in.ParticipantID})
		if isNoRows(err) {
			return precondition("PARTICIPANT_NOT_SIGNER", "The participant is not a signer of this agreement version.")
		}
		if err != nil {
			return err
		}
		if sr.Status != "PENDING" && sr.Status != "AWAITING_CONFIRMATION" {
			return conflict("SIGNATURE_ALREADY_FINAL", "This signature is already "+sr.Status+".")
		}
		var p db.AgreementParticipant
		if err := tx.QueryRow(ctx, `SELECT party_domain FROM agreement_participants WHERE id = $1`, in.ParticipantID).Scan(&p.PartyDomain); err != nil {
			return err
		}
		status, attested, signedAt := "SIGNED", ptr("OWNER"), &in.SignedAt
		if p.PartyDomain == string(platform.DomainMarketplace) {
			status, attested, signedAt = "AWAITING_CONFIRMATION", nil, nil
		}
		if _, err := s.q.UpdateSignatureRequest(ctx, tx, db.UpdateSignatureRequestParams{ID: sr.ID, Status: status, EvidenceMediaID: &in.EvidenceMediaID,
			AttestedBy: attested, SignedAt: signedAt, UpdatedAt: s.now()}); err != nil {
			return err
		}
		return s.completeIfSigned(ctx, a)
	}, "agreement.signature.record")
}

// completeIfSigned moves SIGNING → FULLY_SIGNED once every signer signed.
func (s *Service) completeIfSigned(ctx context.Context, a db.Agreement) error {
	tx := mustTx(ctx)
	srs, err := s.q.ListSignatureRequests(ctx, tx, db.ListSignatureRequestsParams{AgreementID: a.ID, AgreementVersionNo: *a.SigningVersionNo})
	if err != nil {
		return err
	}
	for _, r := range srs {
		if r.Status != "SIGNED" {
			return nil
		}
	}
	cur, err := s.q.GetAgreement(ctx, tx, a.ID)
	if err != nil {
		return err
	}
	if err := s.agreementTransition(ctx, cur, "FULLY_SIGNED", "all_signed", ""); err != nil {
		return err
	}
	if err := s.q.SetAgreementFullySigned(ctx, tx, db.SetAgreementFullySignedParams{ID: a.ID, FullySignedAt: ptr(s.now())}); err != nil {
		return err
	}
	if err := s.propertyHistory(ctx, a.PropertyID, "AGREEMENT_SIGNED", "Agreement fully signed", AggAgreement, a.ID); err != nil {
		return err
	}
	p, err := s.q.GetProperty(ctx, tx, a.PropertyID)
	if err != nil {
		return err
	}
	return s.notifyOwner(ctx, p.OwnerAccountID, TemplateAgreementSigned, map[string]string{"property_title": p.Title})
}

// declineSigning voids the signing round: the agreement returns to DRAFT so a
// new version can be prepared (the signed-for version stays immutable).
func (s *Service) declineSigning(ctx context.Context, a db.Agreement, reason string) error {
	tx := mustTx(ctx)
	if err := s.q.VoidSignatureRequests(ctx, tx, db.VoidSignatureRequestsParams{AgreementID: a.ID, AgreementVersionNo: *a.SigningVersionNo, UpdatedAt: s.now()}); err != nil {
		return err
	}
	return s.agreementTransition(ctx, a, "DRAFT", "signing_declined", reason)
}

// CancelAgreement cancels a pre-effective agreement. An accepted-offer
// listing returns to AVAILABLE (still paused until the owner republishes).
func (s *Service) CancelAgreement(ctx context.Context, subj authz.Subject, id ids.ID, in ReasonCodeInput) (AgreementView, error) {
	if err := schema.Validate(in); err != nil {
		return AgreementView{}, err
	}
	return s.agreementCommand(ctx, subj, id, func(ctx context.Context, a db.Agreement, _ authz.Decision) error {
		if err := s.agreementTransition(ctx, a, "CANCELLED", "cancel", in.ReasonCode); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		if a.SigningVersionNo != nil {
			if err := s.q.VoidSignatureRequests(ctx, tx, db.VoidSignatureRequestsParams{AgreementID: a.ID, AgreementVersionNo: *a.SigningVersionNo, UpdatedAt: now}); err != nil {
				return err
			}
		}
		if err := s.q.SetAgreementCancelled(ctx, tx, db.SetAgreementCancelledParams{ID: a.ID, CancelledAt: &now, CancelReason: &in.ReasonCode}); err != nil {
			return err
		}
		if a.RenewalCaseID != nil {
			if rc, err := s.q.GetRenewalCaseForUpdate(ctx, tx, *a.RenewalCaseID); err == nil && rc.Status == "ACCEPTED" {
				if err := s.closeRenewal(ctx, rc, "WITHDRAWN", "ADDENDUM_CANCELLED"); err != nil {
					return err
				}
			}
		}
		if a.Source == "RENTAL_OFFER" && a.ListingID != nil {
			av, err := s.q.GetAvailability(ctx, tx, *a.ListingID)
			if err == nil && av.Status == "OFFER_ACCEPTED" {
				if _, _, err := s.transition(ctx, domain.Availability, *a.ListingID, lifecycle.Change{To: "AVAILABLE", Command: "agreement_cancelled",
					ExpectedVersion: av.Version, Reason: in.ReasonCode}); err != nil {
					return err
				}
			} else if err != nil && !isNoRows(err) {
				return err
			}
		}
		return s.propertyHistory(ctx, a.PropertyID, "AGREEMENT_CANCELLED", "Agreement cancelled ("+in.ReasonCode+")", AggAgreement, a.ID)
	}, "agreement.cancel")
}

// VoidEnvelopeBeforeCancel voids an outstanding e-sign envelope (pre phase,
// outside the transaction). A failure leaves the agreement unchanged.
func (s *Service) VoidEnvelopeBeforeCancel(ctx context.Context, subj authz.Subject, id ids.ID) (struct{}, error) {
	a, _, err := s.agreementFor(ctx, subj, id, authz.Read(authz.CapAgreementsManage))
	if err != nil || a.Status != "SIGNING" || a.EsignEnvelopeRef == nil || s.ESign == nil {
		return struct{}{}, err
	}
	if err := s.ESign.VoidEnvelope(ctx, *a.EsignEnvelopeRef, "agreement cancelled by owner"); err != nil {
		return struct{}{}, downstream("e-sign provider", err)
	}
	return struct{}{}, nil
}

// ActivateAgreement makes a fully signed agreement EFFECTIVE: a lease
// creates the tenancy (PENDING_MOVE_IN) with its rent schedule and deposit;
// a renewal addendum updates the running tenancy.
func (s *Service) ActivateAgreement(ctx context.Context, subj authz.Subject, id ids.ID) (AgreementView, error) {
	return s.agreementCommand(ctx, subj, id, func(ctx context.Context, a db.Agreement, _ authz.Decision) error {
		if a.Status != "FULLY_SIGNED" {
			return apperrors.InvalidStateTransition(a.Status, "EFFECTIVE")
		}
		tx, now := mustTx(ctx), s.now()
		v, err := s.q.GetAgreementVersion(ctx, tx, db.GetAgreementVersionParams{AgreementID: a.ID, VersionNo: *a.SigningVersionNo})
		if err != nil {
			return err
		}
		t := parseTerms(v.Terms)
		if (t.RentCollection == "PLATFORM" || t.DepositHandling == "PLATFORM") && s.Payment == nil && !s.hasCoreFinancial() {
			return unavailable("Platform rent and deposit collection", "payment-service")
		}
		if err := s.agreementTransition(ctx, a, "EFFECTIVE", "activate", ""); err != nil {
			return err
		}
		var tenancyID *ids.ID
		if a.AgreementKind == "RENEWAL_ADDENDUM" {
			if err := s.applyRenewalAddendum(ctx, a, t); err != nil {
				return err
			}
		} else {
			tn, err := s.createTenancy(ctx, a, t)
			if err != nil {
				return err
			}
			tenancyID = &tn
		}
		if err := s.q.SetAgreementEffective(ctx, tx, db.SetAgreementEffectiveParams{ID: a.ID, EffectiveAt: &now, TenancyID: tenancyID}); err != nil {
			return err
		}
		return s.propertyHistory(ctx, a.PropertyID, "AGREEMENT_EFFECTIVE", "Agreement effective", AggAgreement, a.ID)
	}, "agreement.activate")
}

// ---- e-sign reconciliation (§11 agreement/signature reconciliation) ----

// RunSignatureReconciliation creates missing envelopes (idempotently) and
// reads back envelope state. Ambiguous outcomes are only ever resolved by
// read-back; a create retry reuses the same idempotency key.
func (s *Service) RunSignatureReconciliation(ctx context.Context, batch int32) error {
	if s.ESign == nil {
		return nil
	}
	pending, err := s.q.AgreementsAwaitingEnvelope(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range pending {
		if err := s.createEnvelope(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "e-sign envelope creation deferred", "agreement_id", id, "error", err)
		}
	}
	open, err := s.q.AgreementsAwaitingEsign(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range open {
		if err := s.readBackEnvelope(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "e-sign read-back deferred", "agreement_id", id, "error", err)
		}
	}
	return nil
}

func (s *Service) createEnvelope(ctx context.Context, id ids.ID) error {
	a, err := s.q.GetAgreement(ctx, s.Pool, id)
	if err != nil || a.Status != "SIGNING" || a.EsignEnvelopeRef != nil {
		return err
	}
	v, err := s.q.GetAgreementVersion(ctx, s.Pool, db.GetAgreementVersionParams{AgreementID: id, VersionNo: *a.SigningVersionNo})
	if err != nil {
		return err
	}
	ps, err := s.q.ListAgreementParticipants(ctx, s.Pool, id)
	if err != nil {
		return err
	}
	req := esign.EnvelopeRequest{AgreementID: id, VersionNo: v.VersionNo, DocumentMediaID: *v.DocumentMediaID}
	for i, p := range ps {
		if p.SignRequired {
			req.Signers = append(req.Signers, esign.Signer{ParticipantID: p.ID, Name: p.DisplayName, Email: deref(p.SigningEmail), Order: i + 1})
		}
	}
	env, err := s.ESign.CreateEnvelope(ctx, req, fmt.Sprintf("agreement:%s:v%d", id, v.VersionNo))
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.q.GetAgreementForUpdate(ctx, mustTx(ctx), id)
		if err != nil || cur.Status != "SIGNING" || cur.EsignEnvelopeRef != nil {
			return err
		}
		if err := s.q.SetAgreementEnvelope(ctx, mustTx(ctx), db.SetAgreementEnvelopeParams{ID: id, EsignEnvelopeRef: &env.Ref, UpdatedAt: s.now()}); err != nil {
			return err
		}
		return s.applyEnvelope(ctx, cur, env)
	})
}

func (s *Service) readBackEnvelope(ctx context.Context, id ids.ID) error {
	a, err := s.q.GetAgreement(ctx, s.Pool, id)
	if err != nil || a.EsignEnvelopeRef == nil {
		return err
	}
	env, err := s.ESign.GetEnvelope(ctx, *a.EsignEnvelopeRef)
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.q.GetAgreementForUpdate(ctx, mustTx(ctx), id)
		if err != nil || cur.Status != "SIGNING" {
			return err
		}
		if err := s.applyEnvelope(ctx, cur, env); err != nil {
			return err
		}
		_, err = mustTx(ctx).Exec(ctx, `UPDATE agreements SET updated_at = $2 WHERE id = $1 AND status = 'SIGNING'`, id, s.now())
		return err
	})
}

// applyEnvelope projects provider signer states onto signature requests.
func (s *Service) applyEnvelope(ctx context.Context, a db.Agreement, env esign.Envelope) error {
	tx := mustTx(ctx)
	statuses := map[string]string{esign.SignerSent: "SENT", esign.SignerViewed: "VIEWED", esign.SignerSigned: "SIGNED", esign.SignerDeclined: "DECLINED",
		esign.SignerExpired: "EXPIRED"}
	declined := ""
	for _, ss := range env.Signers {
		sr, err := s.q.GetSignatureRequestForUpdate(ctx, tx, db.GetSignatureRequestForUpdateParams{AgreementID: a.ID, AgreementVersionNo: *a.SigningVersionNo,
			ParticipantID: ss.ParticipantID})
		if isNoRows(err) {
			continue
		}
		if err != nil {
			return err
		}
		st := statuses[ss.Status]
		if st == "" || st == sr.Status || sr.Status == "SIGNED" {
			continue
		}
		p := db.UpdateSignatureRequestParams{ID: sr.ID, Status: st, ProviderSignerRef: strp(ss.SignerRef), UpdatedAt: s.now()}
		if st == "SIGNED" {
			p.AttestedBy, p.SignedAt, p.CertificateMediaID = ptr("PROVIDER"), ss.SignedAt, env.CertificateMediaID
			if p.SignedAt == nil {
				p.SignedAt = ptr(s.now())
			}
		}
		if st == "DECLINED" || st == "EXPIRED" {
			p.DeclinedReason = strp(truncate(ss.Reason, 500))
			declined = st
		}
		if _, err := s.q.UpdateSignatureRequest(ctx, tx, p); err != nil {
			return err
		}
	}
	if declined != "" || env.Status == "VOIDED" || env.Status == "EXPIRED" || env.Status == "DECLINED" {
		return s.declineSigning(ctx, a, "SIGNATURE_"+firstOr([]string{declined}, "VOIDED"))
	}
	return s.completeIfSigned(ctx, a)
}

// ---- marketplace (customer actor) ----

// customerAgreement loads an agreement the customer is a party to.
func (s *Service) customerAgreement(ctx context.Context, subj authz.Subject, id ids.ID, lock bool) (db.Agreement, ids.ID, []string, error) {
	customer, err := s.requireCustomer(ctx, subj)
	if err != nil {
		return db.Agreement{}, ids.Nil, nil, err
	}
	q := s.dbq(ctx)
	a, err := s.q.GetAgreement(ctx, q, id)
	if isNoRows(err) {
		return a, customer, nil, notFound(AggAgreement)
	}
	if err != nil {
		return a, customer, nil, err
	}
	roles, err := s.q.CustomerAgreementRoles(ctx, q, db.CustomerAgreementRolesParams{AgreementID: id, PartyRefID: &customer})
	if err != nil {
		return a, customer, nil, err
	}
	if len(roles) == 0 || a.Status == "DRAFT" && a.CurrentVersionNo == 0 {
		return a, customer, nil, notFound(AggAgreement)
	}
	if lock {
		a, err = s.q.GetAgreementForUpdate(ctx, mustTx(ctx), id)
	}
	return a, customer, roles, err
}

// CustomerGetAgreement returns the agreement to a party (marketplace).
func (s *Service) CustomerGetAgreement(ctx context.Context, subj authz.Subject, id ids.ID) (AgreementView, error) {
	a, _, _, err := s.customerAgreement(ctx, subj, id, false)
	if err != nil {
		return AgreementView{}, err
	}
	return s.agreementView(ctx, a)
}

// CustomerAgreementAction is a counterparty's review or signature response.
type CustomerAgreementAction struct {
	Action string  `json:"action" v:"required,enum=APPROVE_REVIEW|REQUEST_CHANGES|CONFIRM_SIGNATURE|DECLINE_SIGNATURE"`
	Reason *string `json:"reason,omitempty" v:"max=500"`
}

// CustomerActOnAgreement records the counterparty's review or confirms the
// offline signature attributed to them.
func (s *Service) CustomerActOnAgreement(ctx context.Context, subj authz.Subject, id ids.ID, in CustomerAgreementAction) (AgreementView, error) {
	if err := schema.Validate(in); err != nil {
		return AgreementView{}, err
	}
	var out AgreementView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, customer, _, err := s.customerAgreement(ctx, subj, id, true)
		if err != nil {
			return err
		}
		tx := mustTx(ctx)
		switch in.Action {
		case "APPROVE_REVIEW":
			if a.Status != "REVIEW" {
				return conflict("AGREEMENT_NOT_IN_REVIEW", "The agreement is not awaiting review.")
			}
		case "REQUEST_CHANGES":
			if a.Status != "REVIEW" {
				return conflict("AGREEMENT_NOT_IN_REVIEW", "The agreement is not awaiting review.")
			}
			if err := s.agreementTransition(ctx, a, "DRAFT", "changes_requested", truncate(deref(in.Reason), 200)); err != nil {
				return err
			}
		default:
			if a.Status != "SIGNING" || a.SigningVersionNo == nil {
				return conflict("AGREEMENT_NOT_SIGNING", "The agreement is not awaiting signatures.")
			}
			var pid ids.ID
			if err := tx.QueryRow(ctx, `SELECT p.id FROM agreement_participants p JOIN signature_requests r ON r.participant_id = p.id
				WHERE p.agreement_id = $1 AND p.party_domain = 'marketplace' AND p.party_ref_id = $2 AND p.removed_at IS NULL
				  AND r.agreement_version_no = $3 AND r.status IN ('PENDING','SENT','VIEWED','AWAITING_CONFIRMATION') LIMIT 1`,
				a.ID, customer, *a.SigningVersionNo).Scan(&pid); isNoRows(err) {
				return conflict("NO_PENDING_SIGNATURE", "You have no pending signature on this agreement.")
			} else if err != nil {
				return err
			}
			sr, err := s.q.GetSignatureRequestForUpdate(ctx, tx, db.GetSignatureRequestForUpdateParams{AgreementID: a.ID, AgreementVersionNo: *a.SigningVersionNo, ParticipantID: pid})
			if err != nil {
				return err
			}
			if in.Action == "DECLINE_SIGNATURE" {
				if _, err := s.q.UpdateSignatureRequest(ctx, tx, db.UpdateSignatureRequestParams{ID: sr.ID, Status: "DECLINED",
					DeclinedReason: strp(truncate(deref(in.Reason), 500)), UpdatedAt: s.now()}); err != nil {
					return err
				}
				if err := s.declineSigning(ctx, a, "SIGNATURE_DECLINED"); err != nil {
					return err
				}
				break
			}
			if sr.Method != "OFFLINE" || sr.Status != "AWAITING_CONFIRMATION" {
				return conflict("SIGNATURE_NOT_AWAITING_CONFIRMATION", "Electronic signatures are completed with the e-sign provider; offline ones after the owner uploads the signed copy.")
			}
			if _, err := s.q.UpdateSignatureRequest(ctx, tx, db.UpdateSignatureRequestParams{ID: sr.ID, Status: "SIGNED", AttestedBy: ptr("SIGNER"),
				SignedAt: ptr(s.now()), UpdatedAt: s.now()}); err != nil {
				return err
			}
			if err := s.completeIfSigned(ctx, a); err != nil {
				return err
			}
		}
		if err := s.record(ctx, "agreement.customer_action", AggAgreement, id, map[string]any{"action": in.Action}, nil); err != nil {
			return err
		}
		n, err := s.q.GetAgreement(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.agreementView(ctx, n)
		return err
	})
	return out, err
}

// Owner notification templates of the tenancy contexts.
const (
	TemplateAgreementSigned = "owner.agreement_signed"    // property_title
	TemplateRentOverdue     = "owner.rent_overdue"        // property_title, due_date
	TemplateRenewalDue      = "owner.tenancy_renewal_due" // property_title, end_date
	TemplateTenantNotice    = "owner.tenant_notice"       // property_title, notice_type
	TemplateTenantResponse  = "owner.tenant_response"     // property_title, subject
)

func init() {
	offerAcceptedHooks = append(offerAcceptedHooks, func(s *Service, ctx context.Context, o db.RentalOffer) error {
		return s.createAgreementFromOffer(ctx, o)
	})
}
