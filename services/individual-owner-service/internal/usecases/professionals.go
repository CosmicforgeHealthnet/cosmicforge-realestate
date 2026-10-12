package usecases

import (
	"context"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Professional services and due diligence (§13.23). professional-services
// owns profiles, credentials and the work itself; the owner domain stores
// the appointment purpose, its transaction link, the fee and the use of the
// signed report's findings. The professional (delegated actor of
// professional-services-service) quotes, starts and submits the report
// through the provider contract. Data-room access is a least-privilege,
// expiring grant revoked when the appointment ends; fees move only through
// payment-service (or as recorded off-platform evidence).

const AggAppointment = "professional_appointment"

// TemplateProfessionalUpdate tells the owner about appointment progress.
const TemplateProfessionalUpdate = "owner.professional_update" // purpose, status

// AppointmentInput requests a professional for a property or sale.
type AppointmentInput struct {
	PropertyID        *ids.ID `json:"property_id,omitempty"`
	SaleID            *ids.ID `json:"sale_id,omitempty" doc:"Due diligence on a sale transaction"`
	Purpose           string  `json:"purpose" v:"required,enum=VALUATION|SURVEY|LEGAL_DUE_DILIGENCE|TITLE_SEARCH|INSPECTION|TAX_ADVICE|CONVEYANCING|OTHER"`
	Scope             string  `json:"scope" v:"required,max=4000"`
	ProfessionalRef   ids.ID  `json:"professional_ref" v:"required" doc:"professional-services profile id (from its search)"`
	ProfessionalLabel string  `json:"professional_label" v:"required,max=120"`
	Currency          string  `json:"currency,omitempty" v:"pattern=[A-Z]{3}"`
	DueBy             *string `json:"due_by,omitempty" v:"format=date"`
	CFMeetContextID   *string `json:"cfmeet_context_id,omitempty" v:"max=128"`
}

// FindingView is a report finding and how the owner used it.
type FindingView struct {
	ID         ids.ID    `json:"id"`
	Category   string    `json:"category"`
	Severity   string    `json:"severity"`
	Summary    string    `json:"summary"`
	MediaID    *ids.ID   `json:"media_id"`
	UsedInType *string   `json:"used_in_type"`
	UsedInID   *ids.ID   `json:"used_in_id"`
	CreatedAt  time.Time `json:"created_at"`
}

// AppointmentView is a professional appointment.
type AppointmentView struct {
	ID                ids.ID        `json:"id"`
	PropertyID        *ids.ID       `json:"property_id"`
	SaleID            *ids.ID       `json:"sale_id"`
	Purpose           string        `json:"purpose"`
	Scope             string        `json:"scope"`
	ProfessionalRef   ids.ID        `json:"professional_ref"`
	ProfessionalLabel string        `json:"professional_label"`
	Status            string        `json:"status"`
	QuoteAmountMinor  *int64        `json:"quote_amount_minor"`
	Currency          string        `json:"currency"`
	QuoteValidUntil   *time.Time    `json:"quote_valid_until"`
	LicenceRef        *string       `json:"licence_ref"`
	ConflictDeclared  *bool         `json:"conflict_declared"`
	ConflictNote      *string       `json:"conflict_note"`
	FeeCollection     *string       `json:"fee_collection"`
	PaidMinor         int64         `json:"paid_minor"`
	PaymentStatus     *string       `json:"payment_status"`
	DataRoomGrantID   *ids.ID       `json:"data_room_grant_id"`
	ReportMediaID     *ids.ID       `json:"report_media_id"`
	ReportSigned      bool          `json:"report_signed"`
	ReportSubmittedAt *time.Time    `json:"report_submitted_at"`
	DueBy             *string       `json:"due_by"`
	Rating            *int32        `json:"rating"`
	Feedback          *string       `json:"feedback"`
	CancelReason      *string       `json:"cancel_reason"`
	CFMeetContextID   *string       `json:"cfmeet_context_id"`
	Findings          []FindingView `json:"findings"`
	Version           int64         `json:"version"`
	CreatedAt         time.Time     `json:"created_at"`
}

func appointmentBase(a db.ProfessionalAppointment) AppointmentView {
	return AppointmentView{ID: a.ID, PropertyID: a.PropertyID, SaleID: a.SaleID, Purpose: a.Purpose, Scope: a.Scope, ProfessionalRef: a.ProfessionalRef,
		ProfessionalLabel: a.ProfessionalLabel, Status: a.Status, QuoteAmountMinor: a.QuoteAmountMinor, Currency: a.Currency, QuoteValidUntil: a.QuoteValidUntil,
		LicenceRef: a.LicenceRef, ConflictDeclared: a.ConflictDeclared, ConflictNote: a.ConflictNote, FeeCollection: a.FeeCollection, PaidMinor: a.PaidMinor,
		PaymentStatus: a.LastPaymentStatus, DataRoomGrantID: a.DataRoomGrantID, ReportMediaID: a.ReportMediaID, ReportSigned: a.ReportSigned,
		ReportSubmittedAt: a.ReportSubmittedAt, DueBy: dateStr(a.DueBy), Rating: a.Rating, Feedback: a.Feedback, CancelReason: a.CancelReason,
		CFMeetContextID: a.CfmeetContextID, Version: a.Version, CreatedAt: a.CreatedAt, Findings: []FindingView{}}
}

func (s *Service) appointmentView(ctx context.Context, a db.ProfessionalAppointment) (AppointmentView, error) {
	v := appointmentBase(a)
	fs, err := s.q.ListFindings(ctx, s.dbq(ctx), a.ID)
	for _, f := range fs {
		v.Findings = append(v.Findings, FindingView{ID: f.ID, Category: f.Category, Severity: f.Severity, Summary: f.Summary, MediaID: f.MediaID,
			UsedInType: f.UsedInType, UsedInID: f.UsedInID, CreatedAt: f.CreatedAt})
	}
	return v, err
}

// AppointmentEvent is professional.appointment.updated.v1 (engineering
// addition: professional-services learns of requests and decisions).
type AppointmentEvent struct {
	SchemaVersion      int       `json:"schema_version"`
	AppointmentID      ids.ID    `json:"appointment_id"`
	ProfessionalRef    ids.ID    `json:"professional_ref"`
	PropertyID         *ids.ID   `json:"property_id"`
	SaleID             *ids.ID   `json:"sale_id"`
	Purpose            string    `json:"purpose"`
	Scope              string    `json:"scope"`
	Status             string    `json:"status"`
	DataRoomGrantID    *ids.ID   `json:"data_room_grant_id"`
	DueBy              *string   `json:"due_by"`
	AppointmentVersion int64     `json:"appointment_version"`
	OccurredAt         time.Time `json:"occurred_at"`
}

func (s *Service) appointmentTo(ctx context.Context, id ids.ID, to, cmd, reason string) error {
	if to != "" {
		if _, _, err := s.transition(ctx, domain.ProfessionalAppointment, id, lifecycle.Change{To: to, Command: cmd, Reason: reason}); err != nil {
			return err
		}
	}
	a, err := s.q.GetAppointment(ctx, mustTx(ctx), id)
	if err != nil {
		return err
	}
	return s.publish(ctx, "professional.appointment.updated.v1", AggAppointment, id, AppointmentEvent{SchemaVersion: 1, AppointmentID: a.ID,
		ProfessionalRef: a.ProfessionalRef, PropertyID: a.PropertyID, SaleID: a.SaleID, Purpose: a.Purpose, Scope: a.Scope, Status: a.Status,
		DataRoomGrantID: a.DataRoomGrantID, DueBy: dateStr(a.DueBy), AppointmentVersion: a.Version, OccurredAt: s.now()})
}

// appointmentProperty is the property an appointment is authorized on.
func (s *Service) appointmentProperty(ctx context.Context, propertyID, saleID *ids.ID) (ids.ID, error) {
	if propertyID != nil {
		return *propertyID, nil
	}
	sale, err := s.q.GetSale(ctx, s.dbq(ctx), *saleID)
	if isNoRows(err) {
		return ids.Nil, notFound(AggSale)
	}
	return sale.PropertyID, err
}

func (s *Service) appointmentFor(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.ProfessionalAppointment, authz.Decision, error) {
	a, err := s.q.GetAppointment(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return a, authz.Decision{}, notFound(AggAppointment)
	}
	if err != nil {
		return a, authz.Decision{}, err
	}
	pid, err := s.appointmentProperty(ctx, a.PropertyID, a.SaleID)
	if err != nil {
		return a, authz.Decision{}, err
	}
	d, err := s.authorize(ctx, subj, action, pid)
	if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
		err = notFound(AggAppointment)
	}
	if err == nil && !isReadAction(action) {
		a, err = s.q.GetAppointmentForUpdate(ctx, mustTx(ctx), id)
	}
	return a, d, err
}

// CreateAppointment requests a professional (REQUESTED).
func (s *Service) CreateAppointment(ctx context.Context, subj authz.Subject, in AppointmentInput) (AppointmentView, error) {
	if err := schema.Validate(in); err != nil {
		return AppointmentView{}, err
	}
	if (in.PropertyID == nil) == (in.SaleID == nil) {
		return AppointmentView{}, apperrors.InvalidRequest("Give exactly one of property_id or sale_id.")
	}
	var out AppointmentView
	err := s.InTx(ctx, func(ctx context.Context) error {
		pid, err := s.appointmentProperty(ctx, in.PropertyID, in.SaleID)
		if err != nil {
			return err
		}
		d, err := s.authorize(ctx, subj, authz.Do(authz.CapProfessionals), pid)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		p, err := s.q.GetProperty(ctx, tx, pid)
		if err != nil {
			return err
		}
		cur := in.Currency
		if cur == "" {
			cur = s.Policy.Domain.DefaultCurrency
		}
		a, err := s.q.InsertAppointment(ctx, tx, db.InsertAppointmentParams{ID: ids.New(), OwnerAccountID: p.OwnerAccountID, PropertyID: in.PropertyID,
			SaleID: in.SaleID, Purpose: in.Purpose, Scope: in.Scope, ProfessionalRef: in.ProfessionalRef, ProfessionalLabel: in.ProfessionalLabel,
			Currency: cur, DueBy: schema.ParseDate(in.DueBy), CfmeetContextID: in.CFMeetContextID, CreatedAt: now})
		if err != nil {
			return err
		}
		if err := domain.ProfessionalAppointment.RecordCreation(ctx, tx, a.ID, &pid, "REQUESTED", now); err != nil {
			return err
		}
		if err := s.appointmentTo(ctx, a.ID, "", "", ""); err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, pid, "PROFESSIONAL_REQUESTED", AggAppointment, a.ID, in.Purpose); err != nil {
			return err
		}
		if err := s.record(ctx, "professional.appointment.create", AggAppointment, a.ID, map[string]any{"purpose": in.Purpose, "professional_ref": in.ProfessionalRef}, nil); err != nil {
			return err
		}
		out, err = s.appointmentView(ctx, a)
		return err
	})
	return out, err
}

// GetAppointment returns an appointment with its findings.
func (s *Service) GetAppointment(ctx context.Context, subj authz.Subject, id ids.ID) (AppointmentView, error) {
	a, _, err := s.appointmentFor(ctx, subj, id, authz.Read(authz.CapProfessionals))
	if err != nil {
		return AppointmentView{}, err
	}
	return s.appointmentView(ctx, a)
}

// ListAppointments lists appointments across the caller's properties.
func (s *Service) ListAppointments(ctx context.Context, subj authz.Subject, status string) ([]AppointmentView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAppointmentsForAccount(ctx, s.dbq(ctx), db.ListAppointmentsForAccountParams{AccountID: accountOf(subj), Now: s.now(), Status: strp(status)})
	out := make([]AppointmentView, 0, len(rows))
	for _, r := range rows {
		out = append(out, appointmentBase(r))
	}
	return out, err
}

// AppointmentCommand is an owner action (PATCH, If-Match).
type AppointmentCommand struct {
	Action              string   `json:"action" v:"required,enum=APPOINT|CANCEL|ACCEPT_REPORT|DISPUTE_REPORT|GRANT_DATA_ROOM|REVOKE_DATA_ROOM|RATE"`
	FeeCollection       string   `json:"fee_collection,omitempty" v:"enum=PLATFORM|OFF_PLATFORM" doc:"APPOINT: PLATFORM needs payment-service"`
	AcknowledgeConflict bool     `json:"acknowledge_conflict,omitempty" doc:"APPOINT despite a declared conflict of interest"`
	ReasonCode          *string  `json:"reason_code,omitempty" v:"pattern=[A-Z][A-Z0-9_]{1,63}"`
	Note                *string  `json:"note,omitempty" v:"max=2000"`
	Categories          []string `json:"categories,omitempty" v:"max=9,enum=TITLE|SURVEY|PLANNING|VALUATION|INSPECTION|TAX|LEGAL|FINANCIAL|OTHER" doc:"GRANT_DATA_ROOM"`
	ExpiresInDays       int32    `json:"expires_in_days,omitempty" v:"min=1,max=30" doc:"GRANT_DATA_ROOM (default 14)"`
	Rating              int32    `json:"rating,omitempty" v:"min=1,max=5"`
}

// ActOnAppointment applies an owner action.
func (s *Service) ActOnAppointment(ctx context.Context, subj authz.Subject, id ids.ID, expected int64, in AppointmentCommand) (AppointmentView, error) {
	if err := schema.Validate(in); err != nil {
		return AppointmentView{}, err
	}
	if in.Action == "APPOINT" && in.FeeCollection == "PLATFORM" && s.Payment == nil && !s.hasCoreFinancial() {
		return AppointmentView{}, unavailable("Platform payment of professional fees", "payment-service")
	}
	var out AppointmentView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, d, err := s.appointmentFor(ctx, subj, id, authz.Do(authz.CapProfessionals))
		if err != nil {
			return err
		}
		if err := checkVersion(expected, a.Version); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		switch in.Action {
		case "APPOINT":
			switch {
			case a.Status != "QUOTING":
				return apperrors.InvalidStateTransition(a.Status, "APPOINTED")
			case in.FeeCollection == "":
				return apperrors.InvalidRequest("fee_collection is required.")
			case a.QuoteValidUntil != nil && !a.QuoteValidUntil.After(now):
				return conflict("QUOTE_EXPIRED", "The professional's quote expired.")
			case a.LicenceRef == nil:
				return precondition("LICENCE_NOT_CONFIRMED", "professional-services has not confirmed the professional's licence.")
			case deref(a.ConflictDeclared) && !in.AcknowledgeConflict:
				return precondition("CONFLICT_DECLARED", "The professional declared a conflict of interest; acknowledge it to appoint.")
			}
			if err := s.q.SetAppointmentFee(ctx, tx, db.SetAppointmentFeeParams{ID: id, FeeCollection: &in.FeeCollection, UpdatedAt: now}); err != nil {
				return err
			}
			err = s.appointmentTo(ctx, id, "APPOINTED", "appoint", "")
		case "CANCEL":
			if !slices.Contains([]string{"REQUESTED", "QUOTING", "APPOINTED", "IN_PROGRESS"}, a.Status) {
				return apperrors.InvalidStateTransition(a.Status, "CANCELLED")
			}
			if in.ReasonCode == nil {
				return apperrors.InvalidRequest("reason_code is required to cancel.")
			}
			if err := s.endAppointmentAccess(ctx, a); err != nil {
				return err
			}
			if err := s.q.SetAppointmentCancel(ctx, tx, db.SetAppointmentCancelParams{ID: id, CancelReason: in.ReasonCode, UpdatedAt: now}); err != nil {
				return err
			}
			err = s.appointmentTo(ctx, id, "CANCELLED", "cancel", *in.ReasonCode)
		case "ACCEPT_REPORT", "DISPUTE_REPORT":
			to := "ACCEPTED"
			if in.Action == "DISPUTE_REPORT" {
				to = "DISPUTED"
				if in.Note == nil {
					return apperrors.InvalidRequest("Explain the dispute in note.")
				}
			}
			if a.Status != "REPORT_SUBMITTED" && !(to == "ACCEPTED" && a.Status == "DISPUTED") {
				return apperrors.InvalidStateTransition(a.Status, to)
			}
			if to == "ACCEPTED" {
				if err := s.endAppointmentAccess(ctx, a); err != nil {
					return err
				}
			}
			err = s.appointmentTo(ctx, id, to, lower(in.Action), "")
		case "GRANT_DATA_ROOM":
			err = s.grantAppointmentRoom(ctx, a, in)
		case "REVOKE_DATA_ROOM":
			if a.DataRoomGrantID == nil {
				return conflict("NO_DATA_ROOM_GRANT", "The appointment has no data-room grant.")
			}
			err = s.endAppointmentAccess(ctx, a)
			if err == nil {
				err = s.appointmentTo(ctx, id, "", "", "")
			}
		case "RATE":
			if a.Status != "ACCEPTED" || in.Rating == 0 {
				return precondition("RATING_NOT_ALLOWED", "Rate (1–5) an appointment whose report was accepted.")
			}
			err = s.q.SetAppointmentFeedback(ctx, tx, db.SetAppointmentFeedbackParams{ID: id, Rating: &in.Rating, Feedback: in.Note, UpdatedAt: now})
		}
		if err != nil {
			return err
		}
		pid, err := s.appointmentProperty(ctx, a.PropertyID, a.SaleID)
		if err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, pid, "PROFESSIONAL_"+in.Action, AggAppointment, id, a.Purpose); err != nil {
			return err
		}
		if err := s.record(ctx, "professional.appointment."+lower(in.Action), AggAppointment, id, map[string]any{"fee_collection": in.FeeCollection,
			"reason_code": in.ReasonCode, "rating": in.Rating}, nil); err != nil {
			return err
		}
		n, err := s.q.GetAppointment(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.appointmentView(ctx, n)
		return err
	})
	return out, err
}

// grantAppointmentRoom grants the professional least-privilege, expiring
// access to the sale's data room.
func (s *Service) grantAppointmentRoom(ctx context.Context, a db.ProfessionalAppointment, in AppointmentCommand) error {
	if a.SaleID == nil {
		return precondition("NO_SALE", "Data-room access applies to sale due diligence.")
	}
	if !slices.Contains([]string{"APPOINTED", "IN_PROGRESS"}, a.Status) {
		return conflict("NOT_APPOINTED", "Grant access after appointing the professional.")
	}
	tx, now := mustTx(ctx), s.now()
	dr, err := s.q.GetDataRoomBySale(ctx, tx, *a.SaleID)
	if isNoRows(err) {
		return conflict("DATA_ROOM_NOT_OPEN", "Open the sale's data room first.")
	}
	if err != nil {
		return err
	}
	if a.DataRoomGrantID != nil {
		if err := s.endAppointmentAccess(ctx, a); err != nil {
			return err
		}
	}
	days := in.ExpiresInDays
	if days == 0 {
		days = 14
	}
	g, err := s.q.InsertDataRoomGrant(ctx, tx, db.InsertDataRoomGrantParams{ID: ids.New(), DataRoomID: dr.ID, GranteeDomain: string(platform.DomainProfessionalServices),
		GranteeRefID: a.ProfessionalRef, GranteeLabel: a.ProfessionalLabel, Purpose: "professional_appointment:" + a.ID.String(), Categories: nonNil(in.Categories),
		ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour), CreatedAt: now})
	if err != nil {
		return err
	}
	if err := s.q.SetAppointmentGrant(ctx, tx, db.SetAppointmentGrantParams{ID: a.ID, DataRoomGrantID: &g.ID, UpdatedAt: now}); err != nil {
		return err
	}
	return s.appointmentTo(ctx, a.ID, "", "", "")
}

// endAppointmentAccess revokes the appointment's data-room grant.
func (s *Service) endAppointmentAccess(ctx context.Context, a db.ProfessionalAppointment) error {
	if a.DataRoomGrantID == nil {
		return nil
	}
	tx, now := mustTx(ctx), s.now()
	if _, err := tx.Exec(ctx, `UPDATE data_room_grants SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, *a.DataRoomGrantID, now); err != nil {
		return err
	}
	return s.q.SetAppointmentGrant(ctx, tx, db.SetAppointmentGrantParams{ID: a.ID, DataRoomGrantID: nil, UpdatedAt: now})
}

// ProviderFinding is one finding of a signed report.
type ProviderFinding struct {
	Ref      string  `json:"ref" v:"required,max=128"`
	Category string  `json:"category" v:"required,pattern=[A-Z][A-Z0-9_]{1,39}"`
	Severity string  `json:"severity" v:"required,enum=INFO|LOW|MEDIUM|HIGH|BLOCKING"`
	Summary  string  `json:"summary" v:"required,max=2000"`
	MediaID  *ids.ID `json:"media_id,omitempty"`
}

// ProviderAppointmentUpdate is the professional's update through
// professional-services-service.
type ProviderAppointmentUpdate struct {
	Type             string            `json:"type" v:"required,enum=QUOTE|DECLINE|START|REPORT"`
	ExternalRef      *string           `json:"external_ref,omitempty" v:"max=128"`
	QuoteAmountMinor int64             `json:"quote_amount_minor,omitempty" v:"min=1"`
	QuoteValidUntil  *time.Time        `json:"quote_valid_until,omitempty"`
	LicenceRef       *string           `json:"licence_ref,omitempty" v:"max=128" doc:"Licence confirmed by professional-services"`
	ConflictDeclared bool              `json:"conflict_declared,omitempty"`
	ConflictNote     *string           `json:"conflict_note,omitempty" v:"max=1000"`
	ReportMediaID    *ids.ID           `json:"report_media_id,omitempty"`
	ReportSigned     bool              `json:"report_signed,omitempty"`
	Findings         []ProviderFinding `json:"findings,omitempty" v:"max=100"`
}

// MediaRefs lists referenced media.
func (in ProviderAppointmentUpdate) MediaRefs() []ids.ID {
	var out []ids.ID
	if in.ReportMediaID != nil {
		out = append(out, *in.ReportMediaID)
	}
	for _, f := range in.Findings {
		if f.MediaID != nil {
			out = append(out, *f.MediaID)
		}
	}
	return out
}

// ProviderUpdateAppointment applies the professional's quote, decline,
// start or signed report. Repeats of an applied update are no-ops.
func (s *Service) ProviderUpdateAppointment(ctx context.Context, subj authz.Subject, id ids.ID, in ProviderAppointmentUpdate, ev Evidence) (AppointmentView, error) {
	if err := schema.Validate(in); err != nil {
		return AppointmentView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return AppointmentView{}, err
	}
	var out AppointmentView
	err := s.InTx(ctx, func(ctx context.Context) error {
		pro, err := s.requireProvider(ctx, subj, platform.DomainProfessionalServices)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		a, err := s.q.GetAppointmentForUpdate(ctx, tx, id)
		if isNoRows(err) || (err == nil && a.ProfessionalRef != pro) {
			return notFound(AggAppointment)
		}
		if err != nil {
			return err
		}
		switch in.Type {
		case "QUOTE":
			if a.Status != "REQUESTED" && a.Status != "QUOTING" {
				return apperrors.InvalidStateTransition(a.Status, "QUOTING")
			}
			if in.QuoteAmountMinor == 0 {
				return apperrors.InvalidRequest("quote_amount_minor is required.")
			}
			if err := s.q.SetAppointmentQuote(ctx, tx, db.SetAppointmentQuoteParams{ID: id, ExternalRef: in.ExternalRef, QuoteAmountMinor: in.QuoteAmountMinor,
				QuoteValidUntil: in.QuoteValidUntil, LicenceRef: in.LicenceRef, ConflictDeclared: in.ConflictDeclared, ConflictNote: in.ConflictNote, Now: now}); err != nil {
				return err
			}
			to := ""
			if a.Status == "REQUESTED" {
				to = "QUOTING"
			}
			err = s.appointmentTo(ctx, id, to, "provider_quote", "")
		case "DECLINE":
			if !slices.Contains([]string{"REQUESTED", "QUOTING", "APPOINTED"}, a.Status) {
				return apperrors.InvalidStateTransition(a.Status, "CANCELLED")
			}
			if err := s.endAppointmentAccess(ctx, a); err != nil {
				return err
			}
			if err := s.q.SetAppointmentCancel(ctx, tx, db.SetAppointmentCancelParams{ID: id, CancelReason: ptr("PROFESSIONAL_DECLINED"), UpdatedAt: now}); err != nil {
				return err
			}
			err = s.appointmentTo(ctx, id, "CANCELLED", "provider_decline", "PROFESSIONAL_DECLINED")
		case "START":
			if a.Status == "IN_PROGRESS" {
				break
			}
			if a.Status != "APPOINTED" {
				return apperrors.InvalidStateTransition(a.Status, "IN_PROGRESS")
			}
			err = s.appointmentTo(ctx, id, "IN_PROGRESS", "provider_start", "")
		case "REPORT":
			if a.Status == "REPORT_SUBMITTED" && a.ReportMediaID != nil && in.ReportMediaID != nil && *a.ReportMediaID == *in.ReportMediaID {
				break // duplicate delivery
			}
			if a.Status != "IN_PROGRESS" && a.Status != "DISPUTED" {
				return apperrors.InvalidStateTransition(a.Status, "REPORT_SUBMITTED")
			}
			if in.ReportMediaID == nil || !in.ReportSigned {
				return precondition("SIGNED_REPORT_REQUIRED", "Submit the signed report document.")
			}
			if a.Status == "DISPUTED" {
				return conflict("REPORT_DISPUTED", "A disputed report is resolved by the owner or an operator.")
			}
			if err := s.q.SetAppointmentReport(ctx, tx, db.SetAppointmentReportParams{ID: id, ReportMediaID: in.ReportMediaID, ReportSigned: true,
				ReportSubmittedAt: &now}); err != nil {
				return err
			}
			for _, f := range in.Findings {
				if err := s.q.InsertFinding(ctx, tx, db.InsertFindingParams{ID: ids.New(), AppointmentID: id, Category: f.Category, Severity: f.Severity,
					Summary: f.Summary, MediaID: f.MediaID, ExternalFindingRef: &f.Ref, CreatedAt: now}); err != nil {
					return err
				}
			}
			if err = s.appointmentTo(ctx, id, "REPORT_SUBMITTED", "provider_report", ""); err == nil {
				err = s.notifyOwner(ctx, a.OwnerAccountID, TemplateProfessionalUpdate, map[string]string{"purpose": lower(a.Purpose), "status": "report_submitted"})
			}
		}
		if err != nil {
			return err
		}
		if err := s.record(ctx, "professional.appointment.provider_"+lower(in.Type), AggAppointment, id, map[string]any{"findings": len(in.Findings)}, nil); err != nil {
			return err
		}
		n, err := s.q.GetAppointment(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.appointmentView(ctx, n)
		return err
	})
	return out, err
}

// UseFindingInput links a finding to the owner workflow that uses it.
type UseFindingInput struct {
	TargetType string `json:"target_type" v:"required,enum=due_diligence_item|maintenance_request"`
	TargetID   ids.ID `json:"target_id" v:"required"`
}

// UseFinding records that a finding informed a due-diligence item or a
// maintenance request of the same property.
func (s *Service) UseFinding(ctx context.Context, subj authz.Subject, id, findingID ids.ID, in UseFindingInput) (AppointmentView, error) {
	if err := schema.Validate(in); err != nil {
		return AppointmentView{}, err
	}
	var out AppointmentView
	err := s.InTx(ctx, func(ctx context.Context) error {
		a, _, err := s.appointmentFor(ctx, subj, id, authz.Do(authz.CapProfessionals))
		if err != nil {
			return err
		}
		if a.Status != "REPORT_SUBMITTED" && a.Status != "ACCEPTED" {
			return conflict("NO_REPORT", "Findings are used once the report is submitted.")
		}
		tx := mustTx(ctx)
		pid, err := s.appointmentProperty(ctx, a.PropertyID, a.SaleID)
		if err != nil {
			return err
		}
		var owner ids.ID
		switch in.TargetType {
		case "due_diligence_item":
			err = tx.QueryRow(ctx, `SELECT st.property_id FROM due_diligence_items d JOIN sale_transactions st ON st.id = d.sale_id WHERE d.id = $1`, in.TargetID).Scan(&owner)
		case "maintenance_request":
			err = tx.QueryRow(ctx, `SELECT property_id FROM maintenance_requests WHERE id = $1`, in.TargetID).Scan(&owner)
		}
		if isNoRows(err) || (err == nil && owner != pid) {
			return apperrors.InvalidRequest("The target does not belong to the appointment's property.")
		}
		if err != nil {
			return err
		}
		n, err := s.q.UseFinding(ctx, tx, db.UseFindingParams{ID: findingID, AppointmentID: id, UsedInType: &in.TargetType, UsedInID: &in.TargetID})
		if err != nil {
			return err
		}
		if n == 0 {
			return notFound("professional_finding")
		}
		if err := s.record(ctx, "professional.finding.use", AggAppointment, id, map[string]any{"finding_id": findingID, "target_type": in.TargetType,
			"target_id": in.TargetID}, nil); err != nil {
			return err
		}
		out, err = s.appointmentView(ctx, a)
		return err
	})
	return out, err
}

// RunProfessionalJobs requests and reconciles platform fee payments.
func (s *Service) RunProfessionalJobs(ctx context.Context, batch int32) error {
	if s.Payment == nil {
		return nil
	}
	need, err := s.q.AppointmentsNeedingIntent(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range need {
		if err := s.requestFeeIntent(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "professional fee intent deferred", "appointment_id", id, "error", err)
		}
	}
	recon, err := s.q.AppointmentsToReconcile(ctx, s.Pool, db.AppointmentsToReconcileParams{CheckedBefore: s.now().Add(-s.Policy.Domain.PaymentReconcileAfter), MaxRows: batch})
	if err != nil {
		return err
	}
	for _, id := range recon {
		a, err := s.q.GetAppointment(ctx, s.Pool, id)
		if err != nil {
			return err
		}
		in, err := s.Payment.GetIntent(ctx, *a.PaymentIntentRef)
		if err != nil {
			s.Logger.WarnContext(ctx, "professional fee read-back deferred", "appointment_id", id, "error", err)
			continue
		}
		if err := s.InTx(ctx, func(ctx context.Context) error { return s.projectFeeIntent(ctx, id, in) }); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) requestFeeIntent(ctx context.Context, id ids.ID) error {
	a, err := s.q.GetAppointment(ctx, s.Pool, id)
	if err != nil || a.PaymentIntentRef != nil || a.QuoteAmountMinor == nil {
		return err
	}
	in, err := s.Payment.CreateIntent(ctx, payment.IntentRequest{Purpose: payment.PurposeProfessionalFee, ReferenceType: AggAppointment, ReferenceID: a.ID,
		PayeeDomain: string(platform.DomainProfessionalServices), PayeeRefID: &a.ProfessionalRef, PayerDomain: string(platform.DomainIndividualOwner),
		PayerRefID: &a.OwnerAccountID, AmountMinor: *a.QuoteAmountMinor, Currency: a.Currency, DueDate: s.now().Add(7 * 24 * time.Hour), Escrow: true},
		"professional_fee:"+a.ID.String())
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		if err := s.q.SetAppointmentIntent(ctx, mustTx(ctx), db.SetAppointmentIntentParams{ID: id, PaymentIntentRef: &in.Ref, LastPaymentStatus: &in.Status,
			LastCheckedAt: ptr(s.now())}); err != nil {
			return err
		}
		return s.projectFeeIntent(ctx, id, in)
	})
}

func (s *Service) projectFeeIntent(ctx context.Context, id ids.ID, in payment.Intent) error {
	if fresh, err := s.intentIsFresh(ctx, in); !fresh || err != nil {
		return err
	}
	return s.q.SetAppointmentPayment(ctx, mustTx(ctx), db.SetAppointmentPaymentParams{ID: id, PaidMinor: in.PaidMinor, LastPaymentStatus: &in.Status,
		LastCheckedAt: ptr(s.now())})
}

func init() {
	produces("professional.appointment.updated.v1", AggAppointment, "Professional appointment requested/decided/ended, with any data-room grant (engineering addition).", "Professional services", AppointmentEvent{})
}
