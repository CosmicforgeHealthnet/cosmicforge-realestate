package usecases

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Commercial and land (§13.17, §13.18). Property-level commercial and land
// terms are append-only versions (PUT with If-Match on the version number;
// "0" creates the first). Commercial lease negotiation runs through LOIs
// whose rounds are immutable; acceptance converts the LOI into a
// COMMERCIAL_LEASE agreement draft with typed commercial terms.

const AggLOI = "commercial_loi"

// LeaseObligation is a recurring or one-off owner/tenant obligation of a
// commercial property (fit-out, inspections, statutory renewals ...).
type LeaseObligation struct {
	Code            string `json:"code" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}"`
	Description     string `json:"description" v:"required,max=300"`
	Responsible     string `json:"responsible" v:"required,enum=OWNER|TENANT|SHARED"`
	FirstDue        string `json:"first_due" v:"required,format=date"`
	RecurrenceMonth int32  `json:"recurrence_months,omitempty" v:"min=1,max=120" doc:"Repeat every N months (omit for one-off)"`
}

// CommercialTermsInput is a commercial-terms version.
type CommercialTermsInput struct {
	UnitID            *ids.ID           `json:"unit_id,omitempty"`
	PermittedUses     []string          `json:"permitted_uses" v:"required,max=20"`
	ZoningClass       *string           `json:"zoning_class,omitempty" v:"max=64"`
	FloorAreaSqm      int32             `json:"floor_area_sqm,omitempty" v:"min=1,max=10000000"`
	ParkingSpaces     int32             `json:"parking_spaces,omitempty" v:"min=1,max=100000"`
	Facilities        []string          `json:"facilities,omitempty" v:"max=50"`
	FitOutAllowed     bool              `json:"fit_out_allowed"`
	MaxFitOutDays     int32             `json:"max_fit_out_days,omitempty" v:"min=1,max=730"`
	MaxRentFreeMonths int32             `json:"max_rent_free_months,omitempty" v:"min=1,max=60"`
	SignageAllowed    bool              `json:"signage_allowed"`
	SublettingAllowed bool              `json:"subletting_allowed"`
	ServiceChargeBy   string            `json:"service_charge_by" v:"required,enum=OWNER|TENANT|SHARED"`
	InsuranceBy       string            `json:"insurance_by" v:"required,enum=OWNER|TENANT|SHARED"`
	RepairsBy         string            `json:"repairs_by" v:"required,enum=OWNER|TENANT|SHARED"`
	Obligations       []LeaseObligation `json:"obligations,omitempty" v:"max=50"`
}

// CommercialTermsView is the current commercial-terms version.
type CommercialTermsView struct {
	PropertyID ids.ID               `json:"property_id"`
	UnitID     *ids.ID              `json:"unit_id"`
	Version    int64                `json:"version" doc:"Version number (If-Match)"`
	Terms      CommercialTermsInput `json:"terms"`
	CreatedAt  time.Time            `json:"created_at"`
}

func commercialView(t db.CommercialTerm) CommercialTermsView {
	v := CommercialTermsView{PropertyID: t.PropertyID, UnitID: t.UnitID, Version: int64(t.VersionNo), CreatedAt: t.CreatedAt}
	_ = json.Unmarshal(t.Terms, &v.Terms)
	return v
}

func actorParts(ctx context.Context) (*string, *ids.ID) {
	if a := actorFromCtx(ctx); a != nil {
		return &a.Domain, &a.ID
	}
	return nil, nil
}

// GetCommercialTerms returns the current version (404 when none).
func (s *Service) GetCommercialTerms(ctx context.Context, subj authz.Subject, propertyID, unitID ids.ID) (CommercialTermsView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapListingManage), propertyID); err != nil {
		return CommercialTermsView{}, err
	}
	t, err := s.q.LatestCommercialTerms(ctx, s.dbq(ctx), db.LatestCommercialTermsParams{PropertyID: propertyID, UnitID: idp(unitID)})
	if isNoRows(err) {
		return CommercialTermsView{}, apperrors.NotFound("COMMERCIAL_TERMS_NOT_FOUND", "No commercial terms recorded yet.")
	}
	return commercialView(t), err
}

// PutCommercialTerms records the next commercial-terms version (If-Match).
func (s *Service) PutCommercialTerms(ctx context.Context, subj authz.Subject, propertyID ids.ID, expected int64, in CommercialTermsInput) (CommercialTermsView, error) {
	if err := schema.Validate(in); err != nil {
		return CommercialTermsView{}, err
	}
	var out CommercialTermsView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapListingManage), propertyID); err != nil {
			return err
		}
		p, err := s.requireMutable(ctx, propertyID)
		if err != nil {
			return err
		}
		if p.Category != "COMMERCIAL" && p.Category != "MIXED_USE" {
			return precondition("NOT_A_COMMERCIAL_PROPERTY", "Commercial terms apply to commercial or mixed-use properties.")
		}
		if err := s.checkSpace(ctx, propertyID, in.UnitID, nil); err != nil {
			return err
		}
		tx := mustTx(ctx)
		var cur int64
		if t, err := s.q.LatestCommercialTerms(ctx, tx, db.LatestCommercialTermsParams{PropertyID: propertyID, UnitID: in.UnitID}); err == nil {
			cur = int64(t.VersionNo)
		} else if !isNoRows(err) {
			return err
		}
		if expected != cur {
			return lifecycle.ErrVersionConflict()
		}
		dom, actor := actorParts(ctx)
		t, err := s.q.InsertCommercialTerms(ctx, tx, db.InsertCommercialTermsParams{ID: ids.New(), PropertyID: propertyID, UnitID: in.UnitID,
			VersionNo: int32(cur + 1), PermittedUses: in.PermittedUses, ZoningClass: in.ZoningClass, Terms: jsonb(in), ActorDomain: dom, ActorID: actor,
			CreatedAt: s.now()})
		if isUnique(err, "commercial_terms_version_uniq") {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, propertyID, "COMMERCIAL_TERMS_UPDATED", "Commercial terms version "+itoa(int(t.VersionNo)), "commercial_terms", t.ID); err != nil {
			return err
		}
		out = commercialView(t)
		return s.record(ctx, "commercial_terms.put", AggProperty, propertyID, map[string]any{"version_no": t.VersionNo}, nil)
	})
	return out, err
}

// ObligationDate is one upcoming obligation occurrence.
type ObligationDate struct {
	Code        string `json:"code"`
	Description string `json:"description"`
	Responsible string `json:"responsible"`
	DueDate     string `json:"due_date"`
}

// CommercialCalendar lists obligation occurrences within the next months
// (derived from the current terms version; auditable through its history).
func (s *Service) CommercialCalendar(ctx context.Context, subj authz.Subject, propertyID, unitID ids.ID, months int) ([]ObligationDate, error) {
	v, err := s.GetCommercialTerms(ctx, subj, propertyID, unitID)
	if err != nil {
		return nil, err
	}
	if months <= 0 || months > 60 {
		months = 12
	}
	today := dayOf(s.now())
	until := today.AddDate(0, months, 0)
	out := []ObligationDate{}
	for _, o := range v.Terms.Obligations {
		d := *schema.ParseDate(&o.FirstDue)
		for i := int32(0); !d.After(until) && i < 1000; i++ {
			if !d.Before(today) {
				out = append(out, ObligationDate{Code: o.Code, Description: o.Description, Responsible: o.Responsible, DueDate: d.Format(time.DateOnly)})
			}
			if o.RecurrenceMonth == 0 {
				break
			}
			d = domain.AddMonths(*schema.ParseDate(&o.FirstDue), (i+1)*o.RecurrenceMonth)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DueDate < out[j].DueDate })
	return out, nil
}

// DevelopmentObligation is a land development obligation.
type DevelopmentObligation struct {
	Description    string `json:"description" v:"required,max=300"`
	DeadlineMonths int32  `json:"deadline_months,omitempty" v:"min=1,max=600"`
}

// LandTermsInput is a land-terms version.
type LandTermsInput struct {
	ParcelID                  *ids.ID                 `json:"parcel_id,omitempty"`
	PermittedUses             []string                `json:"permitted_uses" v:"required,max=20"`
	LeaseType                 string                  `json:"lease_type" v:"required,enum=GROUND_LEASE|AGRICULTURAL|DEVELOPMENT|STORAGE|OTHER"`
	MaxLeaseYears             int32                   `json:"max_lease_years,omitempty" v:"min=1,max=999"`
	RestorationRequired       bool                    `json:"restoration_required"`
	RestorationTerms          *string                 `json:"restoration_terms,omitempty" v:"max=2000"`
	SubleaseAllowed           bool                    `json:"sublease_allowed"`
	AccessRights              *string                 `json:"access_rights,omitempty" v:"max=1000"`
	EnvironmentalRestrictions *string                 `json:"environmental_restrictions,omitempty" v:"max=2000"`
	DevelopmentObligations    []DevelopmentObligation `json:"development_obligations,omitempty" v:"max=30"`
	EvidenceMediaIDs          []ids.ID                `json:"evidence_media_ids,omitempty" v:"max=20" doc:"Survey, title and charting documents in media-document-service"`
}

// MediaRefs lists referenced media.
func (in LandTermsInput) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// LandTermsView is the current land-terms version.
type LandTermsView struct {
	PropertyID ids.ID         `json:"property_id"`
	ParcelID   *ids.ID        `json:"parcel_id"`
	Version    int64          `json:"version" doc:"Version number (If-Match)"`
	Terms      LandTermsInput `json:"terms"`
	CreatedAt  time.Time      `json:"created_at"`
}

func landView(t db.LandTerm) LandTermsView {
	v := LandTermsView{PropertyID: t.PropertyID, ParcelID: t.ParcelID, Version: int64(t.VersionNo), CreatedAt: t.CreatedAt}
	_ = json.Unmarshal(t.Terms, &v.Terms)
	v.Terms.EvidenceMediaIDs = nonNilIDs(t.EvidenceMediaIds)
	return v
}

// GetLandTerms returns the current land terms (404 when none).
func (s *Service) GetLandTerms(ctx context.Context, subj authz.Subject, propertyID, parcelID ids.ID) (LandTermsView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapListingManage), propertyID); err != nil {
		return LandTermsView{}, err
	}
	t, err := s.q.LatestLandTerms(ctx, s.dbq(ctx), db.LatestLandTermsParams{PropertyID: propertyID, ParcelID: idp(parcelID)})
	if isNoRows(err) {
		return LandTermsView{}, apperrors.NotFound("LAND_TERMS_NOT_FOUND", "No land terms recorded yet.")
	}
	return landView(t), err
}

// PutLandTerms records the next land-terms version (If-Match).
func (s *Service) PutLandTerms(ctx context.Context, subj authz.Subject, propertyID ids.ID, expected int64, in LandTermsInput, ev Evidence) (LandTermsView, error) {
	if err := schema.Validate(in); err != nil {
		return LandTermsView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return LandTermsView{}, err
	}
	var out LandTermsView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapListingManage), propertyID); err != nil {
			return err
		}
		p, err := s.requireMutable(ctx, propertyID)
		if err != nil {
			return err
		}
		if p.Category != "LAND" {
			return precondition("NOT_A_LAND_PROPERTY", "Land terms apply to land properties.")
		}
		tx := mustTx(ctx)
		if in.ParcelID != nil {
			if _, err := s.q.GetParcel(ctx, tx, db.GetParcelParams{ID: *in.ParcelID, PropertyID: propertyID}); isNoRows(err) {
				return precondition("PARCEL_NOT_IN_PROPERTY", "The parcel does not belong to this property.")
			} else if err != nil {
				return err
			}
		}
		var cur int64
		if t, err := s.q.LatestLandTerms(ctx, tx, db.LatestLandTermsParams{PropertyID: propertyID, ParcelID: in.ParcelID}); err == nil {
			cur = int64(t.VersionNo)
		} else if !isNoRows(err) {
			return err
		}
		if expected != cur {
			return lifecycle.ErrVersionConflict()
		}
		dom, actor := actorParts(ctx)
		media := nonNilIDs(in.EvidenceMediaIDs)
		in.EvidenceMediaIDs = nil
		t, err := s.q.InsertLandTerms(ctx, tx, db.InsertLandTermsParams{ID: ids.New(), PropertyID: propertyID, ParcelID: in.ParcelID, VersionNo: int32(cur + 1),
			PermittedUses: in.PermittedUses, Terms: jsonb(in), EvidenceMediaIds: media, ActorDomain: dom, ActorID: actor, CreatedAt: s.now()})
		if isUnique(err, "land_terms_version_uniq") {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, propertyID, "LAND_TERMS_UPDATED", "Land terms version "+itoa(int(t.VersionNo)), "land_terms", t.ID); err != nil {
			return err
		}
		out = landView(t)
		return s.record(ctx, "land_terms.put", AggProperty, propertyID, map[string]any{"version_no": t.VersionNo}, nil)
	})
	return out, err
}

// ---- LOIs ----

// LOITerms are the commercial lease terms negotiated in an LOI round.
type LOITerms struct {
	StartDate          string                `json:"start_date" v:"required,format=date"`
	TermMonths         int32                 `json:"term_months" v:"required,min=1,max=600"`
	Currency           string                `json:"currency" v:"required,format=currency"`
	RentMinor          int64                 `json:"rent_minor" v:"required,min=1"`
	Frequency          string                `json:"frequency" v:"required,enum=MONTHLY|QUARTERLY|BIANNUAL|ANNUAL|ONE_TIME"`
	RentFreeMonths     int32                 `json:"rent_free_months,omitempty" v:"min=1,max=120"`
	EscalationMonths   int32                 `json:"escalation_after_months,omitempty" v:"min=1,max=600"`
	EscalationBps      int32                 `json:"escalation_bps,omitempty" v:"min=-10000,max=10000"`
	ServiceChargeMinor int64                 `json:"service_charge_minor,omitempty" v:"min=0" doc:"Recurring service charge per cycle"`
	DepositMinor       int64                 `json:"deposit_minor,omitempty" v:"min=0"`
	Commercial         CommercialLeaseExtras `json:"commercial"`
}

func (t LOITerms) validate() error {
	if err := schema.Validate(t); err != nil {
		return err
	}
	start, _ := time.Parse(time.DateOnly, t.StartDate)
	return domain.ValidateRentTerms(domain.RentTerms{Start: start, TermMonths: t.TermMonths, Frequency: t.Frequency, DueDay: 1, RentMinor: t.RentMinor,
		EscalateAfter: t.EscalationMonths, EscalateBps: t.EscalationBps, RentFreeMonths: t.RentFreeMonths})
}

// LOIRoundView is one negotiation round.
type LOIRoundView struct {
	RoundNo    int32     `json:"round_no"`
	ProposedBy string    `json:"proposed_by"`
	Terms      LOITerms  `json:"terms"`
	Note       *string   `json:"note"`
	CreatedAt  time.Time `json:"created_at"`
}

// LOIView is a letter of intent.
type LOIView struct {
	ID                      ids.ID         `json:"id"`
	PropertyID              ids.ID         `json:"property_id"`
	ListingID               ids.ID         `json:"listing_id"`
	ApplicantRefID          ids.ID         `json:"applicant_ref_id"`
	BusinessName            string         `json:"business_name"`
	BusinessVerificationRef *string        `json:"business_verification_ref" doc:"Reference to the business verification held by its domain (never copied)"`
	Status                  string         `json:"status"`
	CurrentRound            int32          `json:"current_round"`
	RespondBy               time.Time      `json:"respond_by"`
	AgreementID             *ids.ID        `json:"agreement_id"`
	DeclineReason           *string        `json:"decline_reason"`
	Rounds                  []LOIRoundView `json:"rounds"`
	Version                 int64          `json:"version"`
	CreatedAt               time.Time      `json:"created_at"`
}

func (s *Service) loiView(ctx context.Context, l db.CommercialLoi) (LOIView, error) {
	v := LOIView{ID: l.ID, PropertyID: l.PropertyID, ListingID: l.ListingID, ApplicantRefID: l.ApplicantRefID, BusinessName: l.BusinessName,
		BusinessVerificationRef: l.BusinessVerificationRef, Status: l.Status, CurrentRound: l.CurrentRound, RespondBy: l.RespondBy, AgreementID: l.AgreementID,
		DeclineReason: l.DeclineReason, Version: l.Version, CreatedAt: l.CreatedAt, Rounds: []LOIRoundView{}}
	rs, err := s.q.ListLOIRounds(ctx, s.dbq(ctx), l.ID)
	for _, r := range rs {
		rv := LOIRoundView{RoundNo: r.RoundNo, ProposedBy: r.ProposedBy, Note: r.Note, CreatedAt: r.CreatedAt}
		_ = json.Unmarshal(r.Terms, &rv.Terms)
		v.Rounds = append(v.Rounds, rv)
	}
	return v, err
}

// CustomerLOIInput is a business applicant's LOI (marketplace).
type CustomerLOIInput struct {
	ListingID               ids.ID   `json:"listing_id" v:"required"`
	BusinessName            string   `json:"business_name" v:"required,max=200"`
	BusinessVerificationRef *string  `json:"business_verification_ref,omitempty" v:"max=128"`
	Terms                   LOITerms `json:"terms"`
	Note                    *string  `json:"note,omitempty" v:"max=2000"`
}

// CustomerSubmitLOI records an LOI for a published commercial-lease listing.
func (s *Service) CustomerSubmitLOI(ctx context.Context, subj authz.Subject, in CustomerLOIInput) (LOIView, error) {
	if err := schema.Validate(in); err != nil {
		return LOIView{}, err
	}
	if err := in.Terms.validate(); err != nil {
		return LOIView{}, err
	}
	var out LOIView
	err := s.InTx(ctx, func(ctx context.Context) error {
		applicant, err := s.requireCustomer(ctx, subj)
		if err != nil {
			return err
		}
		l, err := s.bookableListing(ctx, in.ListingID, "COMMERCIAL_LEASE")
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		loi, err := s.q.InsertLOI(ctx, tx, db.InsertLOIParams{ID: ids.New(), PropertyID: l.PropertyID, UnitID: l.UnitID, ListingID: l.ID, ApplicantRefID: applicant,
			BusinessName: in.BusinessName, BusinessVerificationRef: in.BusinessVerificationRef, RespondBy: now.Add(s.Policy.Domain.OfferTTL), CreatedAt: now})
		if isUnique(err, "commercial_lois_open_uniq") {
			return conflict("LOI_ALREADY_OPEN", "You already have an open letter of intent for this listing.")
		}
		if err != nil {
			return err
		}
		if err := domain.CommercialLOI.RecordCreation(ctx, tx, loi.ID, &loi.PropertyID, "SUBMITTED", now); err != nil {
			return err
		}
		if _, err := s.q.InsertLOIRound(ctx, tx, db.InsertLOIRoundParams{ID: ids.New(), LoiID: loi.ID, RoundNo: 1, ProposedBy: "APPLICANT", Terms: jsonb(in.Terms),
			Note: in.Note, CreatedAt: now}); err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, tx, l.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateCommercialLOI, map[string]string{"listing_title": l.Title, "business_name": in.BusinessName}); err != nil {
			return err
		}
		if err := s.record(ctx, "loi.submit", AggLOI, loi.ID, map[string]any{"listing_id": l.ID}, nil); err != nil {
			return err
		}
		out, err = s.loiView(ctx, loi)
		return err
	})
	return out, err
}

// LOIResponse answers the latest round.
type LOIResponse struct {
	Decision   string    `json:"decision" v:"required,enum=ACCEPT|COUNTER|DECLINE|WITHDRAW"`
	Counter    *LOITerms `json:"counter,omitempty"`
	Note       *string   `json:"note,omitempty" v:"max=2000"`
	ReasonCode *string   `json:"reason_code,omitempty" v:"pattern=[A-Z][A-Z0-9_]{1,63}"`
}

func (s *Service) respondLOI(ctx context.Context, l db.CommercialLoi, party string, in LOIResponse) error {
	awaiting := map[string]string{"SUBMITTED": "OWNER", "COUNTERED": "APPLICANT"}[l.Status]
	tx, now := mustTx(ctx), s.now()
	move := func(to, reason string) error {
		_, _, err := s.transition(ctx, domain.CommercialLOI, l.ID, lifecycle.Change{To: to, Command: lower(in.Decision), Reason: reason})
		return err
	}
	if in.Decision == "WITHDRAW" {
		if party != "APPLICANT" {
			return apperrors.InvalidRequest("Owners decline instead of withdrawing.")
		}
		return move("WITHDRAWN", "APPLICANT_WITHDREW")
	}
	if awaiting != party {
		return conflict("LOI_NOT_AWAITING_YOU", "The letter of intent is not awaiting your response.")
	}
	if !now.Before(l.RespondBy) {
		return conflict("LOI_EXPIRED", "The response deadline has passed.")
	}
	switch in.Decision {
	case "DECLINE":
		if err := move("DECLINED", deref(in.ReasonCode)); err != nil {
			return err
		}
		return s.q.SetLOIOutcome(ctx, tx, db.SetLOIOutcomeParams{ID: l.ID, DeclineReason: in.ReasonCode, UpdatedAt: now})
	case "COUNTER":
		if in.Counter == nil {
			return apperrors.InvalidRequest("counter terms are required.")
		}
		if err := in.Counter.validate(); err != nil {
			return err
		}
		next := map[string]string{"OWNER": "COUNTERED", "APPLICANT": "SUBMITTED"}[party]
		if _, err := s.q.InsertLOIRound(ctx, tx, db.InsertLOIRoundParams{ID: ids.New(), LoiID: l.ID, RoundNo: l.CurrentRound + 1, ProposedBy: party,
			Terms: jsonb(*in.Counter), Note: in.Note, CreatedAt: now}); err != nil {
			return err
		}
		if err := move(next, ""); err != nil {
			return err
		}
		return s.q.SetLOIRound(ctx, tx, db.SetLOIRoundParams{ID: l.ID, CurrentRound: l.CurrentRound + 1, RespondBy: now.Add(s.Policy.Domain.OfferTTL), UpdatedAt: now})
	default:
		if err := move("ACCEPTED", ""); err != nil {
			return err
		}
		return s.convertLOI(ctx, l)
	}
}

// convertLOI creates the COMMERCIAL_LEASE agreement draft and reserves the listing.
func (s *Service) convertLOI(ctx context.Context, l db.CommercialLoi) error {
	tx, now := mustTx(ctx), s.now()
	r, err := s.q.LatestLOIRound(ctx, tx, l.ID)
	if err != nil {
		return err
	}
	var lt LOITerms
	_ = json.Unmarshal(r.Terms, &lt)
	start, _ := time.Parse(time.DateOnly, lt.StartDate)
	extras := lt.Commercial
	terms := AgreementTerms{StartDate: lt.StartDate, TermMonths: lt.TermMonths, Currency: lt.Currency, RentMinor: lt.RentMinor,
		RecurringFeesMinor: lt.ServiceChargeMinor, Frequency: lt.Frequency, DueDay: min(int32(start.Day()), 28), EscalationMonths: lt.EscalationMonths,
		EscalationBps: lt.EscalationBps, RentFreeMonths: lt.RentFreeMonths, RentCollection: "OFF_PLATFORM", DepositMinor: lt.DepositMinor,
		DepositHandling: "NONE", DepositReleaseDays: s.Policy.Domain.DepositReleaseDays, Commercial: &extras}
	if s.Payment != nil {
		terms.RentCollection = "PLATFORM"
	}
	if lt.DepositMinor > 0 {
		terms.DepositHandling = "OFF_PLATFORM"
		if s.Payment != nil {
			terms.DepositHandling = "PLATFORM"
		}
	}
	a, err := s.q.InsertAgreement(ctx, tx, db.InsertAgreementParams{ID: ids.New(), PropertyID: l.PropertyID, UnitID: l.UnitID, ListingID: &l.ListingID,
		AgreementKind: "COMMERCIAL_LEASE", Source: "COMMERCIAL_LOI", CustomerRefID: &l.ApplicantRefID, LoiID: &l.ID, CreatedAt: now})
	if err != nil {
		return err
	}
	if err := domain.Agreement.RecordCreation(ctx, tx, a.ID, &a.PropertyID, "DRAFT", now); err != nil {
		return err
	}
	if _, err := s.newAgreementVersion(ctx, a, terms, nil, nil, "Prepared from the accepted letter of intent"); err != nil {
		return err
	}
	p, err := s.q.GetProperty(ctx, tx, l.PropertyID)
	if err != nil {
		return err
	}
	acc, err := s.q.GetAccount(ctx, tx, p.OwnerAccountID)
	if err != nil {
		return err
	}
	for _, pt := range []db.InsertAgreementParticipantParams{
		{Role: "OWNER", PartyDomain: string(platform.DomainIndividualOwner), PartyRefID: &acc.AccountID, DisplayName: acc.DisplayName, SigningEmail: &acc.Email},
		{Role: "TENANT", PartyDomain: string(platform.DomainMarketplace), PartyRefID: &l.ApplicantRefID, DisplayName: l.BusinessName},
	} {
		pt.ID, pt.AgreementID, pt.SignRequired, pt.CreatedAt = ids.New(), a.ID, true, now
		if _, err := s.q.InsertAgreementParticipant(ctx, tx, pt); err != nil {
			return err
		}
	}
	if _, _, err := s.transition(ctx, domain.CommercialLOI, l.ID, lifecycle.Change{To: "CONVERTED", Command: "convert"}); err != nil {
		return err
	}
	if err := s.q.SetLOIOutcome(ctx, tx, db.SetLOIOutcomeParams{ID: l.ID, AgreementID: &a.ID, UpdatedAt: now}); err != nil {
		return err
	}
	if av, err := s.q.GetAvailability(ctx, tx, l.ListingID); err == nil && domain.Availability.Can(av.Status, "OFFER_ACCEPTED") {
		if _, _, err := s.transition(ctx, domain.Availability, l.ListingID, lifecycle.Change{To: "OFFER_ACCEPTED", Command: "loi_accepted",
			ExpectedVersion: av.Version, Metadata: map[string]any{"loi_id": l.ID}}); err != nil {
			return err
		}
		if ls, err := s.q.GetListingForUpdate(ctx, tx, l.ListingID); err != nil {
			return err
		} else if ls.Status == "PUBLISHED" {
			if err := s.pauseTx(ctx, ls, "SYSTEM", "OFFER_ACCEPTED"); err != nil {
				return err
			}
		}
	} else if err == nil {
		return conflict("LISTING_NO_LONGER_AVAILABLE", "The listing is no longer available ("+av.Status+").")
	} else if !isNoRows(err) {
		return err
	}
	return s.propertyHistory(ctx, l.PropertyID, "AGREEMENT_PREPARED", "Commercial lease prepared from accepted LOI", AggAgreement, a.ID)
}

func (s *Service) loiFor(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.CommercialLoi, error) {
	l, err := s.q.GetLOI(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return l, notFound(AggLOI)
	}
	if err != nil {
		return l, err
	}
	if _, err := s.authorize(ctx, subj, action, l.PropertyID); err != nil {
		if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
			err = notFound(AggLOI)
		}
		return l, err
	}
	if !isReadAction(action) {
		return s.q.GetLOIForUpdate(ctx, mustTx(ctx), id)
	}
	return l, nil
}

// ListLOIs lists LOIs across the caller's properties.
func (s *Service) ListLOIs(ctx context.Context, subj authz.Subject, propertyID ids.ID, status string) ([]LOIView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListLOIsForAccount(ctx, s.dbq(ctx), db.ListLOIsForAccountParams{AccountID: accountOf(subj), Now: s.now(), PropertyID: idp(propertyID),
		Status: strp(status)})
	if err != nil {
		return nil, err
	}
	out := make([]LOIView, 0, len(rows))
	for _, r := range rows {
		v, err := s.loiView(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// GetLOI returns an LOI with its rounds.
func (s *Service) GetLOI(ctx context.Context, subj authz.Subject, id ids.ID) (LOIView, error) {
	l, err := s.loiFor(ctx, subj, id, authz.Read(authz.CapApplicationReview))
	if err != nil {
		return LOIView{}, err
	}
	return s.loiView(ctx, l)
}

// RespondLOI is the owner's response.
func (s *Service) RespondLOI(ctx context.Context, subj authz.Subject, id ids.ID, in LOIResponse) (LOIView, error) {
	if err := schema.Validate(in); err != nil {
		return LOIView{}, err
	}
	var out LOIView
	err := s.InTx(ctx, func(ctx context.Context) error {
		l, err := s.loiFor(ctx, subj, id, authz.Do(authz.CapApplicationDecide))
		if err != nil {
			return err
		}
		if err := s.respondLOI(ctx, l, "OWNER", in); err != nil {
			return err
		}
		if err := s.record(ctx, "loi.owner_response", AggLOI, id, map[string]any{"decision": in.Decision}, nil); err != nil {
			return err
		}
		n, err := s.q.GetLOI(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.loiView(ctx, n)
		return err
	})
	return out, err
}

// CustomerRespondLOI is the applicant's response (marketplace).
func (s *Service) CustomerRespondLOI(ctx context.Context, subj authz.Subject, id ids.ID, in LOIResponse) (LOIView, error) {
	if err := schema.Validate(in); err != nil {
		return LOIView{}, err
	}
	var out LOIView
	err := s.InTx(ctx, func(ctx context.Context) error {
		applicant, err := s.requireCustomer(ctx, subj)
		if err != nil {
			return err
		}
		l, err := s.q.GetLOIForUpdate(ctx, mustTx(ctx), id)
		if isNoRows(err) || (err == nil && l.ApplicantRefID != applicant) {
			return notFound(AggLOI)
		}
		if err != nil {
			return err
		}
		if err := s.respondLOI(ctx, l, "APPLICANT", in); err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, mustTx(ctx), l.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateTenantResponse, map[string]string{"property_title": p.Title, "subject": "letter of intent " + lower(in.Decision)}); err != nil {
			return err
		}
		if err := s.record(ctx, "loi.applicant_response", AggLOI, id, map[string]any{"decision": in.Decision}, nil); err != nil {
			return err
		}
		n, err := s.q.GetLOI(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.loiView(ctx, n)
		return err
	})
	return out, err
}

// CustomerGetLOI returns the applicant's LOI.
func (s *Service) CustomerGetLOI(ctx context.Context, subj authz.Subject, id ids.ID) (LOIView, error) {
	applicant, err := s.requireCustomer(ctx, subj)
	if err != nil {
		return LOIView{}, err
	}
	l, err := s.q.GetLOI(ctx, s.dbq(ctx), id)
	if isNoRows(err) || (err == nil && l.ApplicantRefID != applicant) {
		return LOIView{}, notFound(AggLOI)
	}
	if err != nil {
		return LOIView{}, err
	}
	return s.loiView(ctx, l)
}

// TemplateCommercialLOI notifies the owner of a new LOI.
const TemplateCommercialLOI = "owner.commercial_loi" // listing_title, business_name
