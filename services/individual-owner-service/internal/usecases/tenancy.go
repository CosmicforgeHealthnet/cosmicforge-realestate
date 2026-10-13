package usecases

import (
	"context"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Tenancy (§13.13–§13.15). A tenancy is created PENDING_MOVE_IN by an
// EFFECTIVE lease. Move-in requires the initial obligations to be settled by
// payment-service truth (PLATFORM collection) and no other occupying tenancy
// for the same space; it emits tenancy.started.v1 and marks the listing
// OCCUPIED. Condition evidence is append-only and locked once both parties
// acknowledge — later disagreement is an addendum, never an overwrite.

// TenancyView is a tenancy with its operational summary.
type TenancyView struct {
	ID                       ids.ID             `json:"id"`
	PropertyID               ids.ID             `json:"property_id"`
	UnitID                   *ids.ID            `json:"unit_id"`
	ListingID                *ids.ID            `json:"listing_id"`
	AgreementID              ids.ID             `json:"agreement_id"`
	CustomerRefID            *ids.ID            `json:"customer_ref_id"`
	TenantDisplayName        string             `json:"tenant_display_name"`
	Status                   string             `json:"status"`
	StartDate                string             `json:"start_date"`
	EndDate                  *string            `json:"end_date"`
	RentMinor                int64              `json:"rent_minor"`
	RecurringFeesMinor       int64              `json:"recurring_fees_minor"`
	Currency                 string             `json:"currency"`
	Frequency                string             `json:"frequency"`
	DueDay                   int32              `json:"due_day"`
	RentCollection           string             `json:"rent_collection"`
	DepositHandling          string             `json:"deposit_handling"`
	DisputeFlag              bool               `json:"dispute_flag" doc:"A dispute is open; the lifecycle is unchanged"`
	MoveInAt                 *time.Time         `json:"move_in_at"`
	TerminationEffectiveDate *string            `json:"termination_effective_date"`
	EndedAt                  *time.Time         `json:"ended_at"`
	EndReason                *string            `json:"end_reason"`
	Outstanding              *OutstandingView   `json:"outstanding,omitempty"`
	Deposit                  *DepositView       `json:"deposit,omitempty"`
	Checklists               []ChecklistSummary `json:"checklists"`
	Version                  int64              `json:"version"`
	CreatedAt                time.Time          `json:"created_at"`
}

// OutstandingView summarizes open rent obligations (payment-service truth).
type OutstandingView struct {
	OpenObligations  int32 `json:"open_obligations"`
	OutstandingMinor int64 `json:"outstanding_minor"`
}

// ChecklistSummary is the state of a move-in/move-out checklist.
type ChecklistSummary struct {
	Phase    string     `json:"phase"`
	Status   string     `json:"status"`
	LockedAt *time.Time `json:"locked_at"`
}

func tenancyBase(t db.Tenancy) TenancyView {
	return TenancyView{ID: t.ID, PropertyID: t.PropertyID, UnitID: t.UnitID, ListingID: t.ListingID, AgreementID: t.AgreementID, CustomerRefID: t.CustomerRefID,
		TenantDisplayName: t.TenantDisplayName, Status: t.Status, StartDate: t.StartDate.Format(time.DateOnly), EndDate: dateStr(t.EndDate),
		RentMinor: t.RentMinor, RecurringFeesMinor: t.RecurringFeesMinor, Currency: t.Currency, Frequency: t.Frequency, DueDay: t.DueDay,
		RentCollection: t.RentCollection, DepositHandling: t.DepositHandling, DisputeFlag: t.DisputeFlag, MoveInAt: t.MoveInAt,
		TerminationEffectiveDate: dateStr(t.TerminationEffectiveDate), EndedAt: t.EndedAt, EndReason: t.EndReason, Version: t.Version, CreatedAt: t.CreatedAt,
		Checklists: []ChecklistSummary{}}
}

func (s *Service) tenancyView(ctx context.Context, t db.Tenancy) (TenancyView, error) {
	q := s.dbq(ctx)
	v := tenancyBase(t)
	if t.RentCollection == "PLATFORM" {
		o, err := s.q.OutstandingObligations(ctx, q, t.ID)
		if err != nil {
			return v, err
		}
		v.Outstanding = &OutstandingView{OpenObligations: o.OpenCount, OutstandingMinor: o.OutstandingMinor}
	}
	if d, err := s.q.GetDepositByTenancy(ctx, q, t.ID); err == nil {
		dv := depositView(d)
		v.Deposit = &dv
	} else if !isNoRows(err) {
		return v, err
	}
	for _, ph := range []string{"MOVE_IN", "MOVE_OUT"} {
		c, err := s.q.GetChecklist(ctx, q, db.GetChecklistParams{TenancyID: t.ID, Phase: ph})
		if err == nil {
			v.Checklists = append(v.Checklists, ChecklistSummary{Phase: ph, Status: c.Status, LockedAt: c.LockedAt})
		} else if !isNoRows(err) {
			return v, err
		}
	}
	return v, nil
}

// tenancyFor authorizes and (for mutations) locks a tenancy.
func (s *Service) tenancyFor(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.Tenancy, authz.Decision, error) {
	t, err := s.q.GetTenancy(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return t, authz.Decision{}, notFound(AggTenancy)
	}
	if err != nil {
		return t, authz.Decision{}, err
	}
	d, err := s.authorize(ctx, subj, action, t.PropertyID)
	if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
		err = notFound(AggTenancy)
	}
	if err == nil && !isReadAction(action) {
		t, err = s.q.GetTenancyForUpdate(ctx, mustTx(ctx), id)
	}
	return t, d, err
}

func isReadAction(a authz.Action) bool {
	n := len(a)
	return n > 5 && a[n-5:] == ":read"
}

// TenancyEvent is tenancy.started.v1 / tenancy.ended.v1.
type TenancyEvent struct {
	SchemaVersion  int       `json:"schema_version"`
	TenancyID      ids.ID    `json:"tenancy_id"`
	PropertyID     ids.ID    `json:"property_id"`
	UnitID         *ids.ID   `json:"unit_id"`
	ListingID      *ids.ID   `json:"listing_id"`
	AgreementID    ids.ID    `json:"agreement_id"`
	CustomerRefID  *ids.ID   `json:"customer_ref_id"`
	Status         string    `json:"status"`
	StartDate      string    `json:"start_date"`
	EndDate        *string   `json:"end_date"`
	RentCollection string    `json:"rent_collection"`
	Currency       string    `json:"currency"`
	Frequency      string    `json:"frequency"`
	EndReason      *string   `json:"end_reason"`
	ReturnToMarket *bool     `json:"return_to_market"`
	TenancyVersion int64     `json:"tenancy_version"`
	OccurredAt     time.Time `json:"occurred_at"`
}

func (s *Service) emitTenancy(ctx context.Context, eventType string, id ids.ID, returnToMarket *bool) error {
	t, err := s.q.GetTenancy(ctx, mustTx(ctx), id)
	if err != nil {
		return err
	}
	return s.publish(ctx, eventType, AggTenancy, id, TenancyEvent{SchemaVersion: 1, TenancyID: t.ID, PropertyID: t.PropertyID, UnitID: t.UnitID,
		ListingID: t.ListingID, AgreementID: t.AgreementID, CustomerRefID: t.CustomerRefID, Status: t.Status, StartDate: t.StartDate.Format(time.DateOnly),
		EndDate: dateStr(t.EndDate), RentCollection: t.RentCollection, Currency: t.Currency, Frequency: t.Frequency, EndReason: t.EndReason,
		ReturnToMarket: returnToMarket, TenancyVersion: t.Version, OccurredAt: s.now()})
}

func (s *Service) tenancyTransition(ctx context.Context, id ids.ID, to, cmd, reason string) error {
	_, _, err := s.transition(ctx, domain.Tenancy, id, lifecycle.Change{To: to, Command: cmd, Reason: reason})
	return err
}

// createTenancy creates the PENDING_MOVE_IN tenancy of an effective lease.
func (s *Service) createTenancy(ctx context.Context, a db.Agreement, t AgreementTerms) (ids.ID, error) {
	tx, now := mustTx(ctx), s.now()
	rt := t.rentTerms()
	end := rt.EndDate()
	tenantName := ""
	ps, err := s.q.ListAgreementParticipants(ctx, tx, a.ID)
	if err != nil {
		return ids.Nil, err
	}
	var customer *ids.ID
	for _, p := range ps {
		if p.Role == "TENANT" {
			tenantName = p.DisplayName
			if p.PartyDomain == string(platform.DomainMarketplace) {
				customer = p.PartyRefID
			}
			break
		}
	}
	var escAfter, escBps *int32
	if t.EscalationMonths > 0 {
		escAfter, escBps = &t.EscalationMonths, &t.EscalationBps
	}
	tn, err := s.q.InsertTenancy(ctx, tx, db.InsertTenancyParams{ID: ids.New(), PropertyID: a.PropertyID, UnitID: a.UnitID, ListingID: a.ListingID,
		AgreementID: a.ID, CustomerRefID: customer, TenantDisplayName: tenantName, StartDate: rt.Start, EndDate: &end, RentMinor: t.RentMinor,
		Currency: t.Currency, Frequency: t.Frequency, DueDay: t.DueDay, RentCollection: t.RentCollection, DepositHandling: t.DepositHandling,
		LateFeeGraceDays: t.LateFeeGraceDays, LateFeeMinor: t.LateFeeMinor, RecurringFeesMinor: t.RecurringFeesMinor, EscalationAfterMonths: escAfter,
		EscalationBps: escBps, RentFreeMonths: t.RentFreeMonths, CreatedAt: now})
	if err != nil {
		return ids.Nil, err
	}
	if err := domain.Tenancy.RecordCreation(ctx, tx, tn.ID, &tn.PropertyID, "PENDING_MOVE_IN", now); err != nil {
		return ids.Nil, err
	}
	if t.RentCollection == "PLATFORM" {
		if err := s.materializeSchedule(ctx, tn); err != nil {
			return ids.Nil, err
		}
	}
	if t.DepositHandling == "PLATFORM" {
		days := t.DepositReleaseDays
		if days == 0 {
			days = s.Policy.Domain.DepositReleaseDays
		}
		d, err := s.q.InsertDeposit(ctx, tx, db.InsertDepositParams{ID: ids.New(), TenancyID: tn.ID, PropertyID: tn.PropertyID, AmountMinor: t.DepositMinor,
			Currency: t.Currency, ReleaseDueDays: days, CreatedAt: now})
		if err != nil {
			return ids.Nil, err
		}
		if err := domain.Deposit.RecordCreation(ctx, tx, d.ID, &d.PropertyID, "REQUIRED", now); err != nil {
			return ids.Nil, err
		}
	}
	if err := s.propertyHistory(ctx, tn.PropertyID, "TENANCY_CREATED", "Tenancy created (pending move-in)", AggTenancy, tn.ID); err != nil {
		return ids.Nil, err
	}
	return tn.ID, s.record(ctx, "tenancy.create", AggTenancy, tn.ID, map[string]any{"agreement_id": a.ID, "rent_collection": t.RentCollection}, nil)
}

// tenancyRentTerms rebuilds the schedule terms of the tenancy's current
// segment (from the original start, or from the latest renewal's start).
func tenancyRentTerms(t db.Tenancy) domain.RentTerms {
	start, rentFree := t.StartDate, t.RentFreeMonths
	if t.ScheduleAnchor != nil {
		start, rentFree = *t.ScheduleAnchor, 0
	}
	months := int32(12)
	if t.EndDate != nil {
		// whole months between the segment start and end+1 day
		e := t.EndDate.AddDate(0, 0, 1)
		months = int32((e.Year()-start.Year())*12 + int(e.Month()) - int(start.Month()))
	}
	return domain.RentTerms{Start: start, TermMonths: months, Frequency: t.Frequency, DueDay: t.DueDay, RentMinor: t.RentMinor,
		FeesMinor: t.RecurringFeesMinor, EscalateAfter: deref(t.EscalationAfterMonths), EscalateBps: deref(t.EscalationBps), RentFreeMonths: rentFree}
}

func (s *Service) insertObligation(ctx context.Context, t db.Tenancy, p domain.RentPeriod) error {
	tx, now := mustTx(ctx), s.now()
	o, err := s.q.InsertRentObligation(ctx, tx, db.InsertRentObligationParams{ID: ids.New(), TenancyID: t.ID, PropertyID: t.PropertyID, Kind: "RENT",
		PeriodStart: p.PeriodStart, PeriodEnd: p.PeriodEnd, DueDate: p.DueDate, AmountMinor: p.AmountMinor, Currency: t.Currency, CreatedAt: now})
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return domain.RentObligation.RecordCreation(ctx, tx, o.ID, &o.PropertyID, "SCHEDULED", now)
}

// materializeSchedule creates the obligations due within the horizon
// (idempotent: one row per tenancy/kind/period).
func (s *Service) materializeSchedule(ctx context.Context, t db.Tenancy) error {
	tx, now := mustTx(ctx), s.now()
	horizon := now.Add(s.Policy.Domain.RentScheduleHorizon)
	if t.StartDate.After(horizon) {
		horizon = t.StartDate
	}
	for _, p := range domain.RentSchedule(tenancyRentTerms(t), horizon) {
		if err := s.insertObligation(ctx, t, p); err != nil {
			return err
		}
	}
	to := horizon
	if t.EndDate != nil && t.EndDate.Before(to) {
		to = *t.EndDate
	}
	return s.q.SetTenancyScheduleTo(ctx, tx, db.SetTenancyScheduleToParams{ID: t.ID, ScheduleMaterializedTo: &to})
}

// GetTenancy returns a tenancy.
func (s *Service) GetTenancy(ctx context.Context, subj authz.Subject, id ids.ID) (TenancyView, error) {
	t, _, err := s.tenancyFor(ctx, subj, id, authz.Read(authz.CapTenancyManage))
	if err != nil {
		return TenancyView{}, err
	}
	return s.tenancyView(ctx, t)
}

// ListTenancies pages tenancies across the caller's properties.
func (s *Service) ListTenancies(ctx context.Context, subj authz.Subject, propertyID ids.ID, status, cursor string, limit int) (pagination.Response[TenancyView], error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return pagination.Response[TenancyView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[TenancyView]{}, err
	}
	rows, err := s.q.ListTenanciesForAccount(ctx, s.dbq(ctx), db.ListTenanciesForAccountParams{AccountID: accountOf(subj), Now: s.now(),
		PropertyID: idp(propertyID), Status: strp(status), CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[TenancyView]{}, err
	}
	out := make([]TenancyView, 0, len(rows))
	for _, r := range rows {
		out = append(out, tenancyBase(r))
	}
	return pagination.Build(out, limit, func(v TenancyView) any { return timeCursor{At: v.CreatedAt, ID: v.ID} })
}

func (s *Service) tenancyCommand(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action, audit string,
	fn func(ctx context.Context, t db.Tenancy, d authz.Decision) error) (TenancyView, error) {
	var out TenancyView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, d, err := s.tenancyFor(ctx, subj, id, action)
		if err != nil {
			return err
		}
		if err := fn(ctx, t, d); err != nil {
			return err
		}
		if err := s.record(ctx, audit, AggTenancy, id, nil, nil); err != nil {
			return err
		}
		n, err := s.q.GetTenancy(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.tenancyView(ctx, n)
		return err
	})
	return out, err
}

// MoveInInput confirms possession.
type MoveInInput struct {
	MoveInAt *time.Time `json:"move_in_at,omitempty" doc:"Defaults to now"`
}

// MoveIn starts occupancy (PENDING_MOVE_IN → ACTIVE).
func (s *Service) MoveIn(ctx context.Context, subj authz.Subject, id ids.ID, in MoveInInput) (TenancyView, error) {
	return s.tenancyCommand(ctx, subj, id, authz.Do(authz.CapTenancyManage), "tenancy.move_in", func(ctx context.Context, t db.Tenancy, d authz.Decision) error {
		if t.Status != "PENDING_MOVE_IN" {
			return apperrors.InvalidStateTransition(t.Status, "ACTIVE")
		}
		tx, now := mustTx(ctx), s.now()
		at := now
		if in.MoveInAt != nil {
			if in.MoveInAt.After(now) {
				return apperrors.InvalidRequest("move_in_at cannot be in the future.")
			}
			at = *in.MoveInAt
		}
		businessDay, err := s.businessDate(ctx, t.PropertyID, at)
		if err != nil {
			return err
		}
		if businessDay.Before(t.StartDate) {
			return precondition("AGREEMENT_NOT_STARTED", "Move-in requires the accepted agreement's property-local start date.")
		}
		if t.RentCollection == "PLATFORM" {
			today, err := s.businessDate(ctx, t.PropertyID, now)
			if err != nil {
				return err
			}
			ok, err := s.q.InitialObligationsSettled(ctx, tx, db.InitialObligationsSettledParams{TenancyID: t.ID, Today: today})
			if err != nil {
				return err
			}
			if ok == nil || !*ok {
				return precondition("INITIAL_RENT_UNPAID", "The first rent payment has not been confirmed by payment-service.")
			}
		}
		if dep, err := s.q.GetDepositByTenancy(ctx, tx, t.ID); err == nil {
			if dep.Status != "FUNDED" && dep.Status != "HELD" {
				return precondition("DEPOSIT_NOT_FUNDED", "The deposit has not been confirmed as funded by payment-service.")
			}
		} else if !isNoRows(err) {
			return err
		}
		n, err := s.q.CountOccupyingTenancies(ctx, tx, db.CountOccupyingTenanciesParams{PropertyID: t.PropertyID, UnitID: t.UnitID, ExcludeID: t.ID})
		if err != nil {
			return err
		}
		if n > 0 {
			return conflict("SPACE_OCCUPIED", "Another tenancy still occupies this property/unit.")
		}
		if err := s.tenancyTransition(ctx, t.ID, "ACTIVE", "move_in", ""); err != nil {
			if isUnique(err, "tenancies_occupying_uniq") {
				return conflict("SPACE_OCCUPIED", "Another tenancy still occupies this property/unit.")
			}
			return err
		}
		if err := s.q.SetTenancyMovedIn(ctx, tx, db.SetTenancyMovedInParams{ID: t.ID, MoveInAt: &at}); err != nil {
			return err
		}
		if t.ListingID != nil {
			if err := s.setOccupied(ctx, *t.ListingID, t.ID); err != nil {
				return err
			}
		}
		if _, err := s.q.InsertChecklist(ctx, tx, db.InsertChecklistParams{ID: ids.New(), TenancyID: t.ID, PropertyID: t.PropertyID, Phase: "MOVE_IN",
			CreatedAt: now}); err != nil && !isNoRows(err) {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, t.PropertyID, "TENANCY_MOVE_IN", AggTenancy, t.ID, "Tenant moved in"); err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, t.PropertyID, "TENANCY_STARTED", "Tenant moved in", AggTenancy, t.ID); err != nil {
			return err
		}
		return s.emitTenancy(ctx, "tenancy.started.v1", t.ID, nil)
	})
}

// setOccupied moves the listing's availability to OCCUPIED and keeps the
// listing out of discovery.
func (s *Service) setOccupied(ctx context.Context, listingID, tenancyID ids.ID) error {
	tx := mustTx(ctx)
	av, err := s.q.GetAvailability(ctx, tx, listingID)
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if av.Status != "OCCUPIED" && domain.Availability.Can(av.Status, "OCCUPIED") {
		if _, _, err := s.transition(ctx, domain.Availability, listingID, lifecycle.Change{To: "OCCUPIED", Command: "tenancy_started",
			ExpectedVersion: av.Version, Metadata: map[string]any{"tenancy_id": tenancyID}}); err != nil {
			return err
		}
	}
	l, err := s.q.GetListingForUpdate(ctx, tx, listingID)
	if err != nil {
		return err
	}
	if l.Status == "PUBLISHED" {
		return s.pauseTx(ctx, l, "SYSTEM", "OCCUPIED")
	}
	return nil
}

// CancelTenancyBeforeMoveIn ends a tenancy that never started. It is only
// possible while no money was received (otherwise settle through move-out).
func (s *Service) CancelTenancyBeforeMoveIn(ctx context.Context, subj authz.Subject, id ids.ID, in ReasonCodeInput) (TenancyView, error) {
	if err := schema.Validate(in); err != nil {
		return TenancyView{}, err
	}
	return s.tenancyCommand(ctx, subj, id, authz.Do(authz.CapTenancyManage), "tenancy.cancel", func(ctx context.Context, t db.Tenancy, _ authz.Decision) error {
		if t.Status != "PENDING_MOVE_IN" {
			return apperrors.InvalidStateTransition(t.Status, "ENDED")
		}
		tx, now := mustTx(ctx), s.now()
		var moneyIn bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rent_obligations WHERE tenancy_id = $1 AND (paid_minor > 0 OR status IN ('PARTIAL','PAID')))
			OR EXISTS (SELECT 1 FROM deposit_obligations WHERE tenancy_id = $1 AND (funded_minor > 0 OR status <> 'REQUIRED'))`, t.ID).Scan(&moneyIn); err != nil {
			return err
		}
		if moneyIn {
			return conflict("PAYMENTS_RECEIVED", "Payments were already received; end the tenancy through move-out so they can be settled.")
		}
		rows, err := tx.Query(ctx, `SELECT id FROM rent_obligations WHERE tenancy_id = $1 AND status IN ('SCHEDULED','DUE','OVERDUE')`, t.ID)
		if err != nil {
			return err
		}
		obl, err := collectIDs(rows)
		if err != nil {
			return err
		}
		for _, o := range obl {
			if err := s.obligationTo(ctx, o, "WAIVED", "SYSTEM", "TENANCY_CANCELLED", nil, nil); err != nil {
				return err
			}
		}
		if err := s.tenancyTransition(ctx, t.ID, "ENDED", "cancel_before_move_in", in.ReasonCode); err != nil {
			return err
		}
		if err := s.q.SetTenancyEnded(ctx, tx, db.SetTenancyEndedParams{ID: t.ID, EndedAt: &now, EndReason: &in.ReasonCode}); err != nil {
			return err
		}
		return s.propertyHistory(ctx, t.PropertyID, "TENANCY_CANCELLED", "Tenancy cancelled before move-in", AggTenancy, t.ID)
	})
}

// ---- condition checklists ----

// ConditionItemInput records the condition of one item.
type ConditionItemInput struct {
	Room         string   `json:"room" v:"required,max=80"`
	Item         string   `json:"item" v:"required,max=120"`
	Condition    string   `json:"condition" v:"required,enum=EXCELLENT|GOOD|FAIR|POOR|DAMAGED|MISSING|NOT_PRESENT"`
	Notes        *string  `json:"notes,omitempty" v:"max=1000"`
	MediaIDs     []ids.ID `json:"media_ids,omitempty" v:"max=10" doc:"Photos/videos uploaded to media-document-service"`
	SupersedesID *ids.ID  `json:"supersedes_id,omitempty" doc:"Earlier item this entry corrects (the earlier entry is kept)"`
}

// ConditionItemsInput adds items to a checklist.
type ConditionItemsInput struct {
	Items []ConditionItemInput `json:"items" v:"required,max=100"`
}

// MediaRefs lists referenced media.
func (in ConditionItemsInput) MediaRefs() []ids.ID {
	var out []ids.ID
	for _, i := range in.Items {
		out = append(out, i.MediaIDs...)
	}
	return out
}

// ConditionItemView is one recorded condition entry.
type ConditionItemView struct {
	ID              ids.ID    `json:"id"`
	Room            string    `json:"room"`
	Item            string    `json:"item"`
	Condition       string    `json:"condition"`
	Notes           *string   `json:"notes"`
	MediaIDs        []ids.ID  `json:"media_ids"`
	SupersedesID    *ids.ID   `json:"supersedes_id"`
	Addendum        bool      `json:"addendum" doc:"Recorded after both parties acknowledged (dispute addendum)"`
	RecordedByParty string    `json:"recorded_by_party"`
	RecordedAt      time.Time `json:"recorded_at"`
}

// ChecklistView is a checklist with its entries.
type ChecklistView struct {
	ID          ids.ID              `json:"id"`
	TenancyID   ids.ID              `json:"tenancy_id"`
	Phase       string              `json:"phase"`
	Status      string              `json:"status"`
	SubmittedAt *time.Time          `json:"submitted_at"`
	OwnerAckAt  *time.Time          `json:"owner_ack_at"`
	TenantAckAt *time.Time          `json:"tenant_ack_at"`
	LockedAt    *time.Time          `json:"locked_at"`
	Items       []ConditionItemView `json:"items"`
	Version     int64               `json:"version"`
}

func (s *Service) checklistView(ctx context.Context, c db.MoveInChecklist) (ChecklistView, error) {
	v := ChecklistView{ID: c.ID, TenancyID: c.TenancyID, Phase: c.Phase, Status: c.Status, SubmittedAt: c.SubmittedAt, OwnerAckAt: c.OwnerAckAt,
		TenantAckAt: c.TenantAckAt, LockedAt: c.LockedAt, Version: c.Version, Items: []ConditionItemView{}}
	items, err := s.q.ListConditionItems(ctx, s.dbq(ctx), c.ID)
	for _, i := range items {
		v.Items = append(v.Items, ConditionItemView{ID: i.ID, Room: i.Room, Item: i.Item, Condition: i.Condition, Notes: i.Notes, MediaIDs: nonNilIDs(i.MediaIds),
			SupersedesID: i.SupersedesID, Addendum: i.Addendum, RecordedByParty: i.RecordedByParty, RecordedAt: i.RecordedAt})
	}
	return v, err
}

func nonNilIDs(in []ids.ID) []ids.ID {
	if in == nil {
		return []ids.ID{}
	}
	return in
}

var checklistPhaseFor = map[string][]string{"MOVE_IN": {"PENDING_MOVE_IN", "ACTIVE", "RENEWAL_PENDING", "TERMINATION_PENDING"}, "MOVE_OUT": {"MOVE_OUT_PENDING"}}

// GetChecklist returns a checklist (created on first use).
func (s *Service) GetChecklist(ctx context.Context, subj authz.Subject, tenancyID ids.ID, phase string) (ChecklistView, error) {
	if _, _, err := s.tenancyFor(ctx, subj, tenancyID, authz.Read(authz.CapTenancyManage)); err != nil {
		return ChecklistView{}, err
	}
	c, err := s.q.GetChecklist(ctx, s.dbq(ctx), db.GetChecklistParams{TenancyID: tenancyID, Phase: phase})
	if isNoRows(err) {
		return ChecklistView{}, notFound("checklist")
	}
	if err != nil {
		return ChecklistView{}, err
	}
	return s.checklistView(ctx, c)
}

// lockedChecklist loads (creating when allowed) and locks a checklist.
func (s *Service) lockedChecklist(ctx context.Context, t db.Tenancy, phase string) (db.MoveInChecklist, error) {
	tx := mustTx(ctx)
	if !slices.Contains(checklistPhaseFor[phase], t.Status) {
		return db.MoveInChecklist{}, conflict("CHECKLIST_NOT_AVAILABLE", "The "+phase+" checklist is not available while the tenancy is "+t.Status+".")
	}
	if _, err := s.q.InsertChecklist(ctx, tx, db.InsertChecklistParams{ID: ids.New(), TenancyID: t.ID, PropertyID: t.PropertyID, Phase: phase,
		CreatedAt: s.now()}); err != nil && !isNoRows(err) {
		return db.MoveInChecklist{}, err
	}
	return s.q.GetChecklistForUpdate(ctx, tx, db.GetChecklistForUpdateParams{TenancyID: t.ID, Phase: phase})
}

func (s *Service) addConditionItems(ctx context.Context, c db.MoveInChecklist, party string, items []ConditionItemInput) error {
	tx, now := mustTx(ctx), s.now()
	addendum := c.LockedAt != nil || c.Status == "DISPUTED"
	var dom *string
	var actor *ids.ID
	if a := actorFromCtx(ctx); a != nil {
		dom, actor = &a.Domain, &a.ID
	}
	for _, it := range items {
		if it.SupersedesID != nil {
			prev, err := s.q.GetConditionItem(ctx, tx, *it.SupersedesID)
			if isNoRows(err) || (err == nil && prev.ChecklistID != c.ID) {
				return precondition("CONDITION_ITEM_NOT_FOUND", "supersedes_id is not an item of this checklist.")
			}
			if err != nil {
				return err
			}
		}
		if _, err := s.q.InsertConditionItem(ctx, tx, db.InsertConditionItemParams{ID: ids.New(), ChecklistID: c.ID, PropertyID: c.PropertyID, Room: it.Room,
			Item: it.Item, Condition: it.Condition, Notes: it.Notes, MediaIds: nonNilIDs(it.MediaIDs), SupersedesID: it.SupersedesID, Addendum: addendum,
			RecordedByParty: party, ActorDomain: dom, ActorID: actor, RecordedAt: now}); err != nil {
			return err
		}
	}
	return nil
}

// AddConditionItems records owner-side condition entries. Before the
// checklist is submitted they form the inventory; after both parties
// acknowledged they are addenda (the acknowledged record never changes).
func (s *Service) AddConditionItems(ctx context.Context, subj authz.Subject, tenancyID ids.ID, phase string, in ConditionItemsInput, ev Evidence) (ChecklistView, error) {
	if err := schema.Validate(in); err != nil {
		return ChecklistView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return ChecklistView{}, err
	}
	var out ChecklistView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.tenancyFor(ctx, subj, tenancyID, authz.Do(authz.CapTenancyManage))
		if err != nil {
			return err
		}
		c, err := s.lockedChecklist(ctx, t, phase)
		if err != nil {
			return err
		}
		if c.Status == "SUBMITTED" {
			return conflict("CHECKLIST_AWAITING_TENANT", "The checklist was submitted; wait for the tenant's acknowledgement or dispute.")
		}
		if err := s.addConditionItems(ctx, c, "OWNER", in.Items); err != nil {
			return err
		}
		if err := s.record(ctx, "tenancy.checklist.items", AggTenancy, tenancyID, map[string]any{"phase": phase, "items": len(in.Items)}, nil); err != nil {
			return err
		}
		out, err = s.checklistView(ctx, c)
		return err
	})
	return out, err
}

// SubmitChecklistInput submits a checklist.
type SubmitChecklistInput struct {
	TenantAckEvidenceMediaID *ids.ID `json:"tenant_ack_evidence_media_id,omitempty" doc:"Off-platform tenants only: the checklist signed by the tenant (media-document-service)"`
}

// MediaRefs lists referenced media.
func (in SubmitChecklistInput) MediaRefs() []ids.ID { return optIDs(in.TenantAckEvidenceMediaID) }

// SubmitChecklist acknowledges the checklist on the owner side. A platform
// tenant then acknowledges or disputes it through marketplace-service; for an
// off-platform tenant the owner attaches the tenant-signed checklist, which
// locks it. A DISPUTED checklist is locked when the owner accepts the
// tenant's addenda.
func (s *Service) SubmitChecklist(ctx context.Context, subj authz.Subject, tenancyID ids.ID, phase string, in SubmitChecklistInput, ev Evidence) (ChecklistView, error) {
	if err := ev.require(in.MediaRefs()...); err != nil {
		return ChecklistView{}, err
	}
	var out ChecklistView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.tenancyFor(ctx, subj, tenancyID, authz.Do(authz.CapTenancyManage))
		if err != nil {
			return err
		}
		c, err := s.lockedChecklist(ctx, t, phase)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		p := db.UpdateChecklistStateParams{ID: c.ID, Version: c.Version, SubmittedAt: c.SubmittedAt, OwnerAckAt: &now, TenantAckAt: c.TenantAckAt, UpdatedAt: now}
		switch c.Status {
		case "OPEN":
			items, err := s.q.ListConditionItems(ctx, tx, c.ID)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				return precondition("CHECKLIST_EMPTY", "Record at least one condition entry before submitting.")
			}
			p.Status, p.SubmittedAt = "SUBMITTED", &now
			if t.CustomerRefID == nil {
				if in.TenantAckEvidenceMediaID == nil {
					return precondition("TENANT_ACK_EVIDENCE_REQUIRED", "Attach the checklist signed by the off-platform tenant.")
				}
				p.Status, p.TenantAckAt, p.LockedAt, p.TenantAckEvidenceMediaID = "ACKNOWLEDGED", &now, &now, in.TenantAckEvidenceMediaID
			} else if in.TenantAckEvidenceMediaID != nil {
				return apperrors.InvalidRequest("Platform tenants acknowledge the checklist themselves.")
			}
		case "DISPUTED":
			p.Status, p.LockedAt = "ACKNOWLEDGED", &now
		default:
			return conflict("CHECKLIST_NOT_SUBMITTABLE", "The checklist is "+c.Status+".")
		}
		n, err := s.q.UpdateChecklistState(ctx, tx, p)
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		if err := s.record(ctx, "tenancy.checklist.submit", AggTenancy, tenancyID, map[string]any{"phase": phase, "status": n.Status}, nil); err != nil {
			return err
		}
		out, err = s.checklistView(ctx, n)
		return err
	})
	return out, err
}

// ---- keys and access inventory ----

// KeyItemInput records keys/cards/codes handed over (never the code itself).
type KeyItemInput struct {
	ItemType       string     `json:"item_type" v:"required,enum=KEY|CARD|FOB|REMOTE|LOCKBOX|SMART_ACCESS|OTHER"`
	Label          string     `json:"label" v:"required,max=120"`
	Quantity       int32      `json:"quantity" v:"required,min=1,max=100"`
	AccessGrantRef *ids.ID    `json:"access_grant_ref,omitempty" doc:"Smart-access grant reference (credentials are never stored)"`
	IssuedAt       *time.Time `json:"issued_at,omitempty"`
	Notes          *string    `json:"notes,omitempty" v:"max=500"`
}

// KeyItemView is one handed-over item.
type KeyItemView struct {
	ID               ids.ID     `json:"id"`
	ItemType         string     `json:"item_type"`
	Label            string     `json:"label"`
	Quantity         int32      `json:"quantity"`
	AccessGrantRef   *ids.ID    `json:"access_grant_ref"`
	IssuedAt         time.Time  `json:"issued_at"`
	ReturnedAt       *time.Time `json:"returned_at"`
	ReturnedQuantity *int32     `json:"returned_quantity"`
	Notes            *string    `json:"notes"`
}

func keyView(k db.KeyAccessInventory) KeyItemView {
	return KeyItemView{ID: k.ID, ItemType: k.ItemType, Label: k.Label, Quantity: k.Quantity, AccessGrantRef: k.AccessGrantRef, IssuedAt: k.IssuedAt,
		ReturnedAt: k.ReturnedAt, ReturnedQuantity: k.ReturnedQuantity, Notes: k.Notes}
}

// ListKeyItems lists the key/access inventory of a tenancy.
func (s *Service) ListKeyItems(ctx context.Context, subj authz.Subject, tenancyID ids.ID) ([]KeyItemView, error) {
	if _, _, err := s.tenancyFor(ctx, subj, tenancyID, authz.Read(authz.CapTenancyManage)); err != nil {
		return nil, err
	}
	rows, err := s.q.ListKeyItems(ctx, s.dbq(ctx), tenancyID)
	out := make([]KeyItemView, 0, len(rows))
	for _, r := range rows {
		out = append(out, keyView(r))
	}
	return out, err
}

// AddKeyItem records a handover.
func (s *Service) AddKeyItem(ctx context.Context, subj authz.Subject, tenancyID ids.ID, in KeyItemInput) (KeyItemView, error) {
	if err := schema.Validate(in); err != nil {
		return KeyItemView{}, err
	}
	var out KeyItemView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.tenancyFor(ctx, subj, tenancyID, authz.Do(authz.CapTenancyManage))
		if err != nil {
			return err
		}
		if t.Status == "ENDED" {
			return conflict("TENANCY_ENDED", "The tenancy has ended.")
		}
		now := s.now()
		issued := now
		if in.IssuedAt != nil {
			issued = *in.IssuedAt
		}
		k, err := s.q.InsertKeyItem(ctx, mustTx(ctx), db.InsertKeyItemParams{ID: ids.New(), TenancyID: t.ID, PropertyID: t.PropertyID, ItemType: in.ItemType,
			Label: in.Label, Quantity: in.Quantity, AccessGrantRef: in.AccessGrantRef, IssuedAt: issued, Notes: in.Notes, CreatedAt: now})
		if err != nil {
			return err
		}
		out = keyView(k)
		return s.record(ctx, "tenancy.key.issue", AggTenancy, tenancyID, map[string]any{"item_type": in.ItemType, "quantity": in.Quantity}, nil)
	})
	return out, err
}

// KeyReturnInput records the return of an item.
type KeyReturnInput struct {
	ReturnedQuantity int32   `json:"returned_quantity" v:"min=0,max=100"`
	Notes            *string `json:"notes,omitempty" v:"max=500"`
}

// ReturnKeyItem records a return (partial returns keep a note).
func (s *Service) ReturnKeyItem(ctx context.Context, subj authz.Subject, tenancyID, itemID ids.ID, in KeyReturnInput) (KeyItemView, error) {
	if err := schema.Validate(in); err != nil {
		return KeyItemView{}, err
	}
	var out KeyItemView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, _, err := s.tenancyFor(ctx, subj, tenancyID, authz.Do(authz.CapTenancyManage)); err != nil {
			return err
		}
		now := s.now()
		k, err := s.q.ReturnKeyItem(ctx, mustTx(ctx), db.ReturnKeyItemParams{ID: itemID, TenancyID: tenancyID, ReturnedAt: &now, ReturnedQuantity: &in.ReturnedQuantity,
			Notes: in.Notes})
		if isNoRows(err) {
			return conflict("KEY_ITEM_NOT_OUTSTANDING", "The item does not exist or was already returned.")
		}
		if err != nil {
			return err
		}
		out = keyView(k)
		return s.record(ctx, "tenancy.key.return", AggTenancy, tenancyID, map[string]any{"item_id": itemID, "returned_quantity": in.ReturnedQuantity}, nil)
	})
	return out, err
}

// ---- marketplace (customer actor) ----

// customerTenancy loads a tenancy the customer is a party to.
func (s *Service) customerTenancy(ctx context.Context, subj authz.Subject, id ids.ID, lock bool) (db.Tenancy, ids.ID, error) {
	customer, err := s.requireCustomer(ctx, subj)
	if err != nil {
		return db.Tenancy{}, ids.Nil, err
	}
	q := s.dbq(ctx)
	t, err := s.q.GetTenancy(ctx, q, id)
	if isNoRows(err) {
		return t, customer, notFound(AggTenancy)
	}
	if err != nil {
		return t, customer, err
	}
	if t.CustomerRefID == nil || *t.CustomerRefID != customer {
		roles, err := s.q.CustomerAgreementRoles(ctx, q, db.CustomerAgreementRolesParams{AgreementID: t.AgreementID, PartyRefID: &customer})
		if err != nil {
			return t, customer, err
		}
		if !slices.ContainsFunc(roles, func(r string) bool { return slices.Contains(tenantSideRoles, r) }) {
			return t, customer, notFound(AggTenancy)
		}
	}
	if lock {
		t, err = s.q.GetTenancyForUpdate(ctx, mustTx(ctx), id)
	}
	return t, customer, err
}

// CustomerListTenancies lists the customer's tenancies.
func (s *Service) CustomerListTenancies(ctx context.Context, subj authz.Subject) ([]TenancyView, error) {
	customer, err := s.requireCustomer(ctx, subj)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.CustomerTenancies(ctx, s.dbq(ctx), &customer)
	out := make([]TenancyView, 0, len(rows))
	for _, r := range rows {
		out = append(out, tenancyBase(r))
	}
	return out, err
}

// CustomerGetTenancy returns a tenancy to its tenant.
func (s *Service) CustomerGetTenancy(ctx context.Context, subj authz.Subject, id ids.ID) (TenancyView, error) {
	t, _, err := s.customerTenancy(ctx, subj, id, false)
	if err != nil {
		return TenancyView{}, err
	}
	return s.tenancyView(ctx, t)
}

// CustomerChecklistInput is the tenant's acknowledgement or dispute.
type CustomerChecklistInput struct {
	Decision string               `json:"decision" v:"required,enum=ACKNOWLEDGE|DISPUTE"`
	Items    []ConditionItemInput `json:"items,omitempty" v:"max=100" doc:"Tenant's own entries (required to dispute); recorded as addenda"`
}

// CustomerMediaRefs lists referenced customer media.
func (in CustomerChecklistInput) CustomerMediaRefs() []ids.ID {
	return ConditionItemsInput{Items: in.Items}.MediaRefs()
}

// CustomerRespondChecklist acknowledges (locks) or disputes a submitted checklist.
func (s *Service) CustomerRespondChecklist(ctx context.Context, subj authz.Subject, tenancyID ids.ID, phase string, in CustomerChecklistInput, ev Evidence) (ChecklistView, error) {
	if err := schema.Validate(in); err != nil {
		return ChecklistView{}, err
	}
	if err := ev.require(in.CustomerMediaRefs()...); err != nil {
		return ChecklistView{}, err
	}
	if in.Decision == "DISPUTE" && len(in.Items) == 0 {
		return ChecklistView{}, apperrors.InvalidRequest("A dispute needs the tenant's own condition entries.")
	}
	var out ChecklistView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.customerTenancy(ctx, subj, tenancyID, true)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		c, err := s.q.GetChecklistForUpdate(ctx, tx, db.GetChecklistForUpdateParams{TenancyID: t.ID, Phase: phase})
		if isNoRows(err) {
			return notFound("checklist")
		}
		if err != nil {
			return err
		}
		if c.Status != "SUBMITTED" {
			return conflict("CHECKLIST_NOT_SUBMITTED", "The checklist is not awaiting your response.")
		}
		var n db.MoveInChecklist
		if in.Decision == "ACKNOWLEDGE" {
			if len(in.Items) > 0 {
				return apperrors.InvalidRequest("Acknowledging accepts the record as is; dispute to add entries.")
			}
			n, err = s.q.UpdateChecklistState(ctx, tx, db.UpdateChecklistStateParams{ID: c.ID, Version: c.Version, Status: "ACKNOWLEDGED", SubmittedAt: c.SubmittedAt,
				OwnerAckAt: c.OwnerAckAt, TenantAckAt: &now, LockedAt: &now, UpdatedAt: now})
		} else {
			c.Status = "DISPUTED"
			if err := s.addConditionItems(ctx, c, "TENANT", in.Items); err != nil {
				return err
			}
			n, err = s.q.UpdateChecklistState(ctx, tx, db.UpdateChecklistStateParams{ID: c.ID, Version: c.Version, Status: "DISPUTED", SubmittedAt: c.SubmittedAt,
				OwnerAckAt: c.OwnerAckAt, TenantAckAt: &now, LockedAt: nil, UpdatedAt: now})
		}
		if err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, tx, t.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateTenantResponse, map[string]string{"property_title": p.Title,
			"subject": lower(phase) + " checklist " + lower(in.Decision)}); err != nil {
			return err
		}
		if err := s.record(ctx, "tenancy.checklist.customer", AggTenancy, tenancyID, map[string]any{"phase": phase, "decision": in.Decision}, nil); err != nil {
			return err
		}
		out, err = s.checklistView(ctx, n)
		return err
	})
	return out, err
}

func collectIDs(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}) ([]ids.ID, error) {
	defer rows.Close()
	var out []ids.ID
	for rows.Next() {
		var id ids.ID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func init() {
	occupancyChecks = append(occupancyChecks, `SELECT EXISTS (SELECT 1 FROM tenancies t JOIN listings l ON l.id = $1
		WHERE t.property_id = l.property_id AND (l.unit_id IS NULL OR t.unit_id IS NULL OR t.unit_id = l.unit_id)
		  AND t.status IN ('ACTIVE','RENEWAL_PENDING','TERMINATION_PENDING','MOVE_OUT_PENDING'))`)
	archiveBlockerChecks = append(archiveBlockerChecks, struct{ name, sql string }{"open tenancy",
		`SELECT EXISTS (SELECT 1 FROM tenancies WHERE property_id = $1 AND status <> 'ENDED')`})
	unitArchiveChecks = append(unitArchiveChecks, struct{ name, sql string }{"tenancy",
		`SELECT EXISTS (SELECT 1 FROM tenancies WHERE unit_id = $1 AND status <> 'ENDED')`})
	closureHoldChecks = append(closureHoldChecks,
		struct{ code, sql string }{"OPEN_TENANCY", `SELECT EXISTS (SELECT 1 FROM tenancies t JOIN properties p ON p.id = t.property_id
			WHERE p.owner_account_id = $1 AND t.status <> 'ENDED')`},
		struct{ code, sql string }{"UNSETTLED_DEPOSIT", `SELECT EXISTS (SELECT 1 FROM deposit_obligations d JOIN properties p ON p.id = d.property_id
			WHERE p.owner_account_id = $1 AND d.funded_minor > 0 AND d.status NOT IN ('RELEASED','PARTIAL_RELEASED'))`})
	produces("tenancy.started.v1", AggTenancy, "Occupancy began (tenant moved in under an effective agreement).", "Marketplace, Payment, Notification", TenancyEvent{})
	produces("tenancy.ended.v1", AggTenancy, "Occupancy ended after move-out completion and owner confirmation.", "Marketplace, Payment, Notification", TenancyEvent{})
}
