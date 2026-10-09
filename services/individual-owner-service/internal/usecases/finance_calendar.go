package usecases

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Owner finance additions (§13.25): the tax/statutory calendar, property
// valuations (the disclosed basis of rental yield) and portfolio insights
// (vacancy, renewal, maintenance). Jurisdiction tax rules are not encoded:
// statutory entries are the owner's own calendar with informational
// estimates, never a tax computation or a payment. Insights are
// deterministic, advisory statistics over owner-domain records with model,
// version, input provenance, confidence and expiry; no external AI or market
// provider is involved and none decides anything for the owner.

// TemplateStatutoryDue reminds the owner of a statutory/tax date.
const TemplateStatutoryDue = "owner.statutory_due" // title, due_on

const (
	portfolioInsightModel   = "cf-portfolio-rules"
	portfolioInsightVersion = "1.0.0"
)

// ---- statutory calendar ----

// StatutoryInput records a statutory or tax obligation date.
type StatutoryInput struct {
	PropertyID           *ids.ID `json:"property_id,omitempty" doc:"Omit for account-level obligations (e.g. income tax filing)"`
	ObligationType       string  `json:"obligation_type" v:"required,enum=INCOME_TAX_FILING|RENTAL_INCOME_TAX|WITHHOLDING_TAX_REMITTANCE|PROPERTY_TAX|LAND_USE_CHARGE|GROUND_RENT|TENEMENT_RATE|SERVICE_LICENCE|STATUTORY_INSURANCE|OTHER"`
	Title                string  `json:"title" v:"required,min=3,max=160"`
	Jurisdiction         string  `json:"jurisdiction" v:"required,format=country"`
	Authority            *string `json:"authority,omitempty" v:"max=160" doc:"Revenue service, state or local authority"`
	Reference            *string `json:"reference,omitempty" v:"max=120" doc:"Assessment or file reference"`
	DueOn                string  `json:"due_on" v:"required,format=date"`
	Recurrence           string  `json:"recurrence" v:"required,enum=NONE|MONTHLY|QUARTERLY|ANNUAL"`
	EstimatedAmountMinor *int64  `json:"estimated_amount_minor,omitempty" v:"min=0" doc:"The owner's informational estimate; never computed by the platform"`
	Currency             *string `json:"currency,omitempty" v:"format=currency"`
	ReminderDays         *int32  `json:"reminder_days,omitempty" v:"min=0,max=90" doc:"Default 14"`
	Notes                *string `json:"notes,omitempty" v:"max=2000"`
}

// StatutoryCloseInput completes or cancels an obligation.
type StatutoryCloseInput struct {
	Action           string   `json:"action" v:"required,enum=COMPLETE|CANCEL"`
	EvidenceMediaIDs []ids.ID `json:"evidence_media_ids,omitempty" v:"max=10" doc:"Receipts or filing acknowledgements (COMPLETE)"`
	Notes            *string  `json:"notes,omitempty" v:"max=2000"`
}

// MediaRefs lists referenced media.
func (in StatutoryCloseInput) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// StatutoryView is a statutory/tax calendar entry.
type StatutoryView struct {
	ID                   ids.ID     `json:"id"`
	PropertyID           *ids.ID    `json:"property_id"`
	ObligationType       string     `json:"obligation_type"`
	Title                string     `json:"title"`
	Jurisdiction         string     `json:"jurisdiction"`
	Authority            *string    `json:"authority"`
	Reference            *string    `json:"reference"`
	DueOn                string     `json:"due_on"`
	Recurrence           string     `json:"recurrence"`
	EstimatedAmountMinor *int64     `json:"estimated_amount_minor"`
	Currency             *string    `json:"currency"`
	ReminderDays         int32      `json:"reminder_days"`
	Status               string     `json:"status" doc:"UPCOMING, COMPLETED or CANCELLED"`
	RemindedAt           *time.Time `json:"reminded_at"`
	CompletedAt          *time.Time `json:"completed_at"`
	EvidenceMediaIDs     []ids.ID   `json:"evidence_media_ids"`
	Notes                *string    `json:"notes"`
	PreviousID           *ids.ID    `json:"previous_id" doc:"The occurrence this recurring entry follows"`
	Version              int64      `json:"version"`
}

func statutoryView(o db.StatutoryObligation) StatutoryView {
	return StatutoryView{ID: o.ID, PropertyID: o.PropertyID, ObligationType: o.ObligationType, Title: o.Title, Jurisdiction: o.Jurisdiction,
		Authority: o.Authority, Reference: o.Reference, DueOn: o.DueOn.Format(time.DateOnly), Recurrence: o.Recurrence,
		EstimatedAmountMinor: o.EstimatedAmountMinor, Currency: o.Currency, ReminderDays: o.ReminderDays, Status: o.Status, RemindedAt: o.RemindedAt,
		CompletedAt: o.CompletedAt, EvidenceMediaIDs: nonNilIDs(o.EvidenceMediaIds), Notes: o.Notes, PreviousID: o.PreviousID, Version: o.Version}
}

// CreateStatutoryObligation adds an entry to the owner's statutory calendar.
func (s *Service) CreateStatutoryObligation(ctx context.Context, subj authz.Subject, in StatutoryInput) (StatutoryView, error) {
	if err := schema.Validate(in); err != nil {
		return StatutoryView{}, err
	}
	if (in.EstimatedAmountMinor == nil) != (in.Currency == nil) {
		return StatutoryView{}, apperrors.InvalidRequest("estimated_amount_minor and currency go together.")
	}
	var out StatutoryView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActStatutoryWrite, ids.Nil); err != nil {
			return err
		}
		if in.PropertyID != nil {
			if _, err := s.authorize(ctx, subj, authz.Read(authz.CapReportsView), *in.PropertyID); err != nil {
				return err
			}
		}
		now, rd := s.now(), int32(14)
		if in.ReminderDays != nil {
			rd = *in.ReminderDays
		}
		o, err := s.q.InsertStatutoryObligation(ctx, mustTx(ctx), db.InsertStatutoryObligationParams{ID: ids.New(), AccountID: accountOf(subj),
			PropertyID: in.PropertyID, ObligationType: in.ObligationType, Title: in.Title, Jurisdiction: in.Jurisdiction, Authority: in.Authority,
			Reference: in.Reference, DueOn: *schema.ParseDate(&in.DueOn), Recurrence: in.Recurrence, EstimatedAmountMinor: in.EstimatedAmountMinor,
			Currency: in.Currency, ReminderDays: rd, Notes: in.Notes, CreatedAt: now})
		if err != nil {
			return err
		}
		out = statutoryView(o)
		return s.record(ctx, "statutory.create", "statutory_obligation", o.ID, map[string]any{"obligation_type": in.ObligationType, "due_on": in.DueOn,
			"recurrence": in.Recurrence}, nil)
	})
	return out, err
}

// ListStatutoryObligations lists the owner's statutory calendar.
func (s *Service) ListStatutoryObligations(ctx context.Context, subj authz.Subject, propertyID ids.ID, status string) ([]StatutoryView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActReportsRead, ids.Nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListStatutoryObligations(ctx, s.dbq(ctx), db.ListStatutoryObligationsParams{AccountID: accountOf(subj), PropertyID: idp(propertyID),
		Status: strp(status)})
	out := make([]StatutoryView, 0, len(rows))
	for _, r := range rows {
		out = append(out, statutoryView(r))
	}
	return out, err
}

// CloseStatutoryObligation completes (with optional evidence) or cancels an
// entry; completing a recurring entry schedules the next occurrence once.
func (s *Service) CloseStatutoryObligation(ctx context.Context, subj authz.Subject, id ids.ID, in StatutoryCloseInput, ev Evidence) (StatutoryView, error) {
	if err := schema.Validate(in); err != nil {
		return StatutoryView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return StatutoryView{}, err
	}
	var out StatutoryView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActStatutoryWrite, ids.Nil); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		o, err := s.q.GetStatutoryObligationForUpdate(ctx, tx, id)
		if isNoRows(err) || (err == nil && o.AccountID != accountOf(subj)) {
			return notFound("statutory_obligation")
		}
		if err != nil {
			return err
		}
		if o.Status != "UPCOMING" {
			return apperrors.InvalidStateTransition(o.Status, in.Action)
		}
		to, done := "CANCELLED", (*time.Time)(nil)
		if in.Action == "COMPLETE" {
			to, done = "COMPLETED", &now
		} else if len(in.EvidenceMediaIDs) > 0 {
			return apperrors.InvalidRequest("Evidence is recorded when completing.")
		}
		if o, err = s.q.CloseStatutoryObligation(ctx, tx, db.CloseStatutoryObligationParams{ID: id, Status: to, CompletedAt: done,
			EvidenceMediaIds: nonNilIDs(in.EvidenceMediaIDs), Notes: in.Notes, UpdatedAt: now}); err != nil {
			return err
		}
		if in.Action == "COMPLETE" && o.Recurrence != "NONE" {
			next := o.DueOn.AddDate(0, map[string]int{"MONTHLY": 1, "QUARTERLY": 3, "ANNUAL": 12}[o.Recurrence], 0)
			if _, err := s.q.InsertStatutoryObligation(ctx, tx, db.InsertStatutoryObligationParams{ID: ids.New(), AccountID: o.AccountID, PropertyID: o.PropertyID,
				ObligationType: o.ObligationType, Title: o.Title, Jurisdiction: o.Jurisdiction, Authority: o.Authority, Reference: o.Reference, DueOn: next,
				Recurrence: o.Recurrence, EstimatedAmountMinor: o.EstimatedAmountMinor, Currency: o.Currency, ReminderDays: o.ReminderDays, PreviousID: &o.ID,
				CreatedAt: now}); err != nil {
				return err
			}
		}
		out = statutoryView(o)
		return s.record(ctx, "statutory."+lower(in.Action), "statutory_obligation", id, map[string]any{"evidence": len(in.EvidenceMediaIDs)}, nil)
	})
	return out, err
}

// RunStatutoryReminders sends one reminder per entry reminder_days before
// it is due.
func (s *Service) RunStatutoryReminders(ctx context.Context, batch int32) error {
	now := s.now()
	due, err := s.q.StatutoryRemindersDue(ctx, s.Pool, db.StatutoryRemindersDueParams{Today: dayOf(now), MaxRows: batch})
	if err != nil {
		return err
	}
	for _, o := range due {
		if err := s.InTx(ctx, func(ctx context.Context) error {
			if err := s.q.MarkStatutoryReminded(ctx, mustTx(ctx), db.MarkStatutoryRemindedParams{ID: o.ID, RemindedAt: &now}); err != nil {
				return err
			}
			return s.notifyOwner(ctx, o.AccountID, TemplateStatutoryDue, map[string]string{"title": o.Title, "due_on": o.DueOn.Format(time.DateOnly)})
		}); err != nil {
			return err
		}
	}
	return nil
}

// ---- valuations ----

// ValuationInput records a property valuation.
type ValuationInput struct {
	ValueMinor                int64    `json:"value_minor" v:"required,min=1"`
	Currency                  string   `json:"currency" v:"required,format=currency"`
	ValuedOn                  string   `json:"valued_on" v:"required,format=date"`
	Basis                     string   `json:"basis" v:"required,enum=OWNER_ESTIMATE|PURCHASE_PRICE|PROFESSIONAL_VALUATION"`
	ProfessionalAppointmentID *ids.ID  `json:"professional_appointment_id,omitempty" doc:"Required for PROFESSIONAL_VALUATION: an appointment on this property with a submitted report"`
	EvidenceMediaIDs          []ids.ID `json:"evidence_media_ids,omitempty" v:"max=10"`
	Note                      *string  `json:"note,omitempty" v:"max=1000"`
}

// MediaRefs lists referenced media.
func (in ValuationInput) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// ValuationView is a recorded valuation (append-only).
type ValuationView struct {
	ID                        ids.ID    `json:"id"`
	PropertyID                ids.ID    `json:"property_id"`
	ValueMinor                int64     `json:"value_minor"`
	Currency                  string    `json:"currency"`
	ValuedOn                  string    `json:"valued_on"`
	Basis                     string    `json:"basis"`
	ProfessionalAppointmentID *ids.ID   `json:"professional_appointment_id"`
	EvidenceMediaIDs          []ids.ID  `json:"evidence_media_ids"`
	Note                      *string   `json:"note"`
	CreatedAt                 time.Time `json:"created_at"`
}

func valuationView(v db.PropertyValuation) ValuationView {
	return ValuationView{ID: v.ID, PropertyID: v.PropertyID, ValueMinor: v.ValueMinor, Currency: v.Currency, ValuedOn: v.ValuedOn.Format(time.DateOnly),
		Basis: v.Basis, ProfessionalAppointmentID: v.ProfessionalAppointmentID, EvidenceMediaIDs: nonNilIDs(v.EvidenceMediaIds), Note: v.Note, CreatedAt: v.CreatedAt}
}

// RecordValuation appends a valuation; the latest one is the yield basis.
func (s *Service) RecordValuation(ctx context.Context, subj authz.Subject, propertyID ids.ID, in ValuationInput, ev Evidence) (ValuationView, error) {
	if err := schema.Validate(in); err != nil {
		return ValuationView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return ValuationView{}, err
	}
	if (in.Basis == "PROFESSIONAL_VALUATION") != (in.ProfessionalAppointmentID != nil) {
		return ValuationView{}, apperrors.InvalidRequest("professional_appointment_id is given exactly for PROFESSIONAL_VALUATION.")
	}
	valued := *schema.ParseDate(&in.ValuedOn)
	var out ValuationView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapPropertyEdit), propertyID); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		if valued.After(dayOf(now)) {
			return apperrors.InvalidRequest("valued_on cannot be in the future.")
		}
		if in.ProfessionalAppointmentID != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM professional_appointments WHERE id = $1 AND property_id = $2
				AND status IN ('REPORT_SUBMITTED','ACCEPTED'))`, *in.ProfessionalAppointmentID, propertyID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return precondition("VALUATION_REPORT_REQUIRED", "A professional valuation references an appointment on this property with a submitted report.")
			}
		}
		v, err := s.q.InsertPropertyValuation(ctx, tx, db.InsertPropertyValuationParams{ID: ids.New(), PropertyID: propertyID, ValueMinor: in.ValueMinor,
			Currency: in.Currency, ValuedOn: valued, Basis: in.Basis, ProfessionalAppointmentID: in.ProfessionalAppointmentID,
			EvidenceMediaIds: nonNilIDs(in.EvidenceMediaIDs), Note: in.Note, RecordedByAccount: accountOf(subj), CreatedAt: now})
		if err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, propertyID, "VALUATION_RECORDED", in.Basis+" valuation recorded", "property_valuation", v.ID); err != nil {
			return err
		}
		out = valuationView(v)
		return s.record(ctx, "property.valuation.record", AggProperty, propertyID, map[string]any{"valuation_id": v.ID, "basis": in.Basis}, nil)
	})
	return out, err
}

// ListValuations lists a property's valuations, latest first.
func (s *Service) ListValuations(ctx context.Context, subj authz.Subject, propertyID ids.ID) ([]ValuationView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapReportsView), propertyID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListPropertyValuations(ctx, s.dbq(ctx), propertyID)
	out := make([]ValuationView, 0, len(rows))
	for _, r := range rows {
		out = append(out, valuationView(r))
	}
	return out, err
}

// YieldFigures is gross rental yield over valued properties.
type YieldFigures struct {
	ValuedProperties int64            `json:"valued_properties" doc:"Properties with a recorded valuation (others are excluded)"`
	Valuation        Money            `json:"valuation" doc:"Latest valuation per property, summed per currency"`
	AnnualizedIncome Money            `json:"annualized_income" doc:"Rent collected + short-let revenue of the valued properties in the period, annualized"`
	GrossYieldBps    map[string]int64 `json:"gross_yield_bps"`
	Bases            []string         `json:"bases" doc:"Valuation bases used (OWNER_ESTIMATE, PURCHASE_PRICE, PROFESSIONAL_VALUATION)"`
}

// yieldFigures computes gross yield from collected money only (payment
// projections), never from outstanding obligations.
func (s *Service) yieldFigures(ctx context.Context, account ids.ID, propertyID *ids.ID, from, to time.Time) (*YieldFigures, error) {
	rows, err := s.dbq(ctx).Query(ctx, propsCTE+`,
		val AS (SELECT DISTINCT ON (v.property_id) v.property_id, v.value_minor, v.currency, v.basis FROM property_valuations v
		         WHERE v.property_id IN (SELECT property_id FROM props) ORDER BY v.property_id, v.valued_on DESC, v.created_at DESC)
		SELECT v.currency, count(*), sum(v.value_minor)::bigint, array_agg(DISTINCT v.basis),
		  coalesce((SELECT sum(o.paid_minor) FROM rent_obligations o JOIN tenancies t ON t.id = o.tenancy_id
		             WHERE t.property_id IN (SELECT property_id FROM val x WHERE x.currency = v.currency) AND o.currency = v.currency
		               AND o.due_date BETWEEN $4 AND $5), 0)::bigint
		+ coalesce((SELECT sum(b.paid_minor - b.refunded_minor) FROM shortlet_bookings b
		             WHERE b.property_id IN (SELECT property_id FROM val x WHERE x.currency = v.currency) AND b.currency = v.currency
		               AND b.status IN ('CONFIRMED','CHECKED_IN','CHECKED_OUT','COMPLETED','DISPUTED') AND b.check_in BETWEEN $4 AND $5), 0)::bigint
		FROM val v GROUP BY v.currency`, account, s.now(), propertyID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	y := &YieldFigures{Valuation: Money{}, AnnualizedIncome: Money{}, GrossYieldBps: map[string]int64{}, Bases: []string{}}
	days := int64(to.Sub(from).Hours()/24) + 1
	seen := map[string]bool{}
	for rows.Next() {
		var cur string
		var n, value, income int64
		var bases []string
		if err := rows.Scan(&cur, &n, &value, &bases, &income); err != nil {
			return nil, err
		}
		y.ValuedProperties += n
		y.Valuation[cur] = value
		annual := income * 365 / max(days, 1)
		y.AnnualizedIncome[cur] = annual
		y.GrossYieldBps[cur] = annual * 10000 / value
		for _, b := range bases {
			if !seen[b] {
				seen[b] = true
				y.Bases = append(y.Bases, b)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if y.ValuedProperties == 0 {
		return nil, nil
	}
	return y, nil
}

// ---- portfolio insights ----

// PropertyInsights returns (or computes) advisory vacancy, renewal and
// maintenance insights for a property.
func (s *Service) PropertyInsights(ctx context.Context, subj authz.Subject, propertyID ids.ID) ([]InsightView, error) {
	var out []InsightView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActMarketInsightsRead, ids.Nil); err != nil {
			return err
		}
		if _, err := s.authorize(ctx, subj, authz.Read(authz.CapReportsView), propertyID); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		types := []string{"VACANCY_RISK", "RENEWAL_OUTLOOK", "MAINTENANCE_PATTERN"}
		for _, t := range types {
			m, err := s.q.LatestInsight(ctx, tx, db.LatestInsightParams{PropertyID: propertyID, InsightType: t, Now: now})
			if isNoRows(err) {
				break
			}
			if err != nil {
				return err
			}
			out = append(out, insightView(m))
		}
		if len(out) == len(types) {
			return nil
		}
		out = nil
		for _, f := range []func(context.Context, ids.ID, time.Time) (string, map[string]any, map[string]any, float64, error){
			s.vacancyInsight, s.renewalInsight, s.maintenanceInsight} {
			t, inputs, res, conf, err := f(ctx, propertyID, now)
			if err != nil {
				return err
			}
			inputs["method"] = "deterministic rules over this property's owner-domain records; no external AI or market provider"
			rawIn, _ := json.Marshal(inputs)
			rawRes, _ := json.Marshal(res)
			m, err := s.q.InsertInsight(ctx, tx, db.InsertInsightParams{ID: ids.New(), AccountID: accountOf(subj), PropertyID: propertyID, InsightType: t,
				ModelName: portfolioInsightModel, ModelVersion: portfolioInsightVersion, Inputs: rawIn, Result: rawRes, Confidence: math.Round(conf*1000) / 1000,
				GeneratedAt: now, ExpiresAt: now.Add(insightTTL)})
			if err != nil {
				return err
			}
			out = append(out, insightView(m))
		}
		return nil
	})
	return out, err
}

func (s *Service) vacancyInsight(ctx context.Context, pid ids.ID, now time.Time) (string, map[string]any, map[string]any, float64, error) {
	tx := mustTx(ctx)
	leaf := `('UNIT','ROOM','SHOP','OFFICE','WAREHOUSE')`
	active := `('PENDING_MOVE_IN','ACTIVE','RENEWAL_PENDING','TERMINATION_PENDING','MOVE_OUT_PENDING')`
	var units, occupied, live, inquiries30, staleChecks, longest int64
	err := tx.QueryRow(ctx, `
		WITH spaces AS (
		  SELECT u.id AS space_id FROM property_units u WHERE u.property_id = $1 AND u.status = 'ACTIVE' AND u.unit_type IN `+leaf+`
		  UNION ALL SELECT p.id FROM properties p WHERE p.id = $1
		    AND NOT EXISTS (SELECT 1 FROM property_units u WHERE u.property_id = $1 AND u.status = 'ACTIVE' AND u.unit_type IN `+leaf+`)),
		occ AS (SELECT DISTINCT coalesce(t.unit_id, t.property_id) AS space_id FROM tenancies t WHERE t.property_id = $1 AND t.status IN `+active+`),
		vacant AS (SELECT s.space_id, coalesce((SELECT max(t.ended_at) FROM tenancies t WHERE t.property_id = $1 AND coalesce(t.unit_id, t.property_id) = s.space_id),
		                  (SELECT created_at FROM properties WHERE id = $1)) AS since FROM spaces s WHERE s.space_id NOT IN (SELECT space_id FROM occ))
		SELECT (SELECT count(*) FROM spaces), (SELECT count(*) FROM spaces WHERE space_id IN (SELECT space_id FROM occ)),
		       (SELECT count(*) FROM listings WHERE property_id = $1 AND status = 'PUBLISHED' AND transaction_type <> 'SHORT_LET'),
		       (SELECT count(*) FROM inquiries WHERE property_id = $1 AND created_at >= $2::timestamptz - interval '30 days'),
		       (SELECT count(*) FROM freshness_checks f JOIN listings l ON l.id = f.listing_id WHERE l.property_id = $1 AND f.status IN ('PENDING','REMINDED') AND f.due_at < $2),
		       coalesce((SELECT max(extract(day FROM $2::timestamptz - since))::bigint FROM vacant), 0)`, pid, now).
		Scan(&units, &occupied, &live, &inquiries30, &staleChecks, &longest)
	if err != nil {
		return "", nil, nil, 0, err
	}
	vacant := units - occupied
	level, reasons := "LOW", []string{}
	switch {
	case vacant == 0:
		reasons = append(reasons, "NO_VACANT_SPACE")
	case live == 0:
		level, reasons = "HIGH", append(reasons, "VACANT_WITHOUT_LIVE_LISTING")
	case longest > 60 && inquiries30 < 3:
		level, reasons = "HIGH", append(reasons, "LONG_VACANCY_FEW_INQUIRIES")
	case longest > 30 || inquiries30 < 3:
		level, reasons = "MEDIUM", append(reasons, "VACANCY_OVER_30_DAYS_OR_FEW_INQUIRIES")
	}
	if staleChecks > 0 {
		reasons = append(reasons, "AVAILABILITY_CONFIRMATION_OVERDUE")
	}
	inputs := map[string]any{"rentable_spaces": units, "occupied_spaces": occupied, "live_long_term_listings": live, "inquiries_last_30_days": inquiries30,
		"overdue_availability_confirmations": staleChecks, "longest_vacancy_days": longest}
	res := map[string]any{"risk_level": level, "vacant_spaces": vacant, "reasons": reasons,
		"suggestion": "Review price position (market insights), listing quality and availability confirmation; the decision stays with the owner."}
	return "VACANCY_RISK", inputs, res, math.Min(1, 0.4+float64(inquiries30+units)/20), nil
}

func (s *Service) renewalInsight(ctx context.Context, pid ids.ID, now time.Time) (string, map[string]any, map[string]any, float64, error) {
	rows, err := mustTx(ctx).Query(ctx, `
		SELECT t.id, t.end_date, (t.end_date - $2::date)::bigint,
		       coalesce((SELECT r.status FROM renewal_cases r WHERE r.tenancy_id = t.id ORDER BY r.created_at DESC LIMIT 1), 'NONE'),
		       EXISTS (SELECT 1 FROM notices n WHERE n.tenancy_id = t.id AND n.status IN ('ISSUED','DELIVERED','ACKNOWLEDGED')),
		       (SELECT count(*) FROM rent_obligations o WHERE o.tenancy_id = t.id AND o.status = 'OVERDUE'),
		       (SELECT count(*) FROM rent_obligations o WHERE o.tenancy_id = t.id AND o.due_date < $2::date AND o.status <> 'WAIVED'),
		       (SELECT count(*) FROM rent_obligations o WHERE o.tenancy_id = t.id AND o.due_date < $2::date AND o.status = 'PAID')
		  FROM tenancies t WHERE t.property_id = $1 AND t.status IN ('ACTIVE','RENEWAL_PENDING') AND t.end_date IS NOT NULL
		   AND t.end_date <= $2::date + 120 ORDER BY t.end_date LIMIT 50`, pid, dayOf(now))
	if err != nil {
		return "", nil, nil, 0, err
	}
	defer rows.Close()
	items := []map[string]any{}
	var history int64
	for rows.Next() {
		var id ids.ID
		var end time.Time
		var days, overdue, past, paid int64
		var renewal string
		var notice bool
		if err := rows.Scan(&id, &end, &days, &renewal, &notice, &overdue, &past, &paid); err != nil {
			return "", nil, nil, 0, err
		}
		history += past
		signal := "ACTION_NEEDED"
		switch {
		case notice:
			signal = "ENDING"
		case renewal == "ACCEPTED" || renewal == "COMPLETED":
			signal = "RENEWED"
		case renewal == "PROPOSED" || renewal == "COUNTERED":
			signal = "IN_NEGOTIATION"
		case overdue > 0:
			signal = "AT_RISK"
		case days > 60:
			signal = "PLAN_RENEWAL"
		}
		onTime := int64(-1)
		if past > 0 {
			onTime = paid * 10000 / past
		}
		items = append(items, map[string]any{"tenancy_id": id, "end_date": end.Format(time.DateOnly), "days_to_end": days, "renewal_status": renewal,
			"notice_given": notice, "overdue_obligations": overdue, "paid_on_record_bps": onTime, "signal": signal})
	}
	if err := rows.Err(); err != nil {
		return "", nil, nil, 0, err
	}
	inputs := map[string]any{"window_days": 120, "tenancies_considered": len(items), "rent_obligations_considered": history}
	res := map[string]any{"tenancies": items, "note": "Payment history uses payment-service projections only; renewal terms remain the owner's decision."}
	return "RENEWAL_OUTLOOK", inputs, res, math.Min(1, 0.5+float64(history)/40), nil
}

func (s *Service) maintenanceInsight(ctx context.Context, pid ids.ID, now time.Time) (string, map[string]any, map[string]any, float64, error) {
	tx := mustTx(ctx)
	rows, err := tx.Query(ctx, `SELECT category, count(*), count(*) FILTER (WHERE status NOT IN ('COMPLETED','CANCELLED')),
		count(*) FILTER (WHERE warranty_of_request_id IS NOT NULL)
		FROM maintenance_requests WHERE property_id = $1 AND created_at >= $2::timestamptz - interval '365 days' GROUP BY category ORDER BY count(*) DESC`, pid, now)
	if err != nil {
		return "", nil, nil, 0, err
	}
	byCat, recurring := map[string]int64{}, []string{}
	var total, open, rework int64
	for rows.Next() {
		var c string
		var n, o, w int64
		if err := rows.Scan(&c, &n, &o, &w); err != nil {
			rows.Close()
			return "", nil, nil, 0, err
		}
		byCat[c], total, open, rework = n, total+n, open+o, rework+w
		if n >= 3 {
			recurring = append(recurring, c)
		}
	}
	rows.Close()
	spend := Money{}
	srows, err := tx.Query(ctx, `SELECT j.currency, coalesce(sum(j.paid_minor),0)::bigint FROM maintenance_jobs j JOIN maintenance_requests m ON m.id = j.request_id
		WHERE m.property_id = $1 AND j.created_at >= $2::timestamptz - interval '365 days' GROUP BY j.currency`, pid, now)
	if err != nil {
		return "", nil, nil, 0, err
	}
	for srows.Next() {
		var c string
		var v int64
		if err := srows.Scan(&c, &v); err != nil {
			srows.Close()
			return "", nil, nil, 0, err
		}
		spend.add(c, v)
	}
	srows.Close()
	var overduePreventive int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM preventive_schedules WHERE property_id = $1 AND status = 'ACTIVE' AND next_due_on < $2::date`,
		pid, dayOf(now)).Scan(&overduePreventive); err != nil {
		return "", nil, nil, 0, err
	}
	suggestions := []string{}
	if len(recurring) > 0 {
		suggestions = append(suggestions, "Recurring categories may justify a preventive schedule or asset replacement review.")
	}
	if overduePreventive > 0 {
		suggestions = append(suggestions, "Preventive tasks are overdue.")
	}
	inputs := map[string]any{"window_days": 365, "requests_considered": total}
	res := map[string]any{"requests_by_category": byCat, "recurring_categories": recurring, "open_requests": open, "warranty_claims": rework,
		"paid_spend": spend, "overdue_preventive_tasks": overduePreventive, "suggestions": suggestions}
	return "MAINTENANCE_PATTERN", inputs, res, math.Min(1, 0.3+float64(total)/20), nil
}
