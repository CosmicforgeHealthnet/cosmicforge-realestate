package usecases

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Utilities and service charges (§13.16). These are domain accounting
// projections, not a payment ledger: bills, readings and service-charge items
// are recorded with evidence and attribution; allocation rules are versioned
// and an allocation under dispute keeps its original calculation.

const (
	AggUtility = "utility_account"
	AggBudget  = "service_charge_budget"
)

// UtilityAccountView is a property utility account.
type UtilityAccountView struct {
	ID                    ids.ID    `json:"id"`
	PropertyID            ids.ID    `json:"property_id"`
	UnitID                *ids.ID   `json:"unit_id"`
	UtilityType           string    `json:"utility_type"`
	ProviderName          string    `json:"provider_name"`
	AccountRefLast4       *string   `json:"account_ref_last4" doc:"Only the last 4 characters of the provider account reference are stored"`
	MeterNumber           *string   `json:"meter_number"`
	MeterType             string    `json:"meter_type"`
	Responsibility        string    `json:"responsibility"`
	AllocationRuleVersion int32     `json:"allocation_rule_version"`
	Status                string    `json:"status"`
	Version               int64     `json:"version"`
	CreatedAt             time.Time `json:"created_at"`
}

func utilityView(u db.UtilityAccount) UtilityAccountView {
	return UtilityAccountView{ID: u.ID, PropertyID: u.PropertyID, UnitID: u.UnitID, UtilityType: u.UtilityType, ProviderName: u.ProviderName,
		AccountRefLast4: u.AccountRefLast4, MeterNumber: u.MeterNumber, MeterType: u.MeterType, Responsibility: u.Responsibility,
		AllocationRuleVersion: u.AllocationRuleVersion, Status: u.Status, Version: u.Version, CreatedAt: u.CreatedAt}
}

// UtilityAccountInput creates or updates a utility account.
type UtilityAccountInput struct {
	UnitID          *ids.ID `json:"unit_id,omitempty"`
	UtilityType     string  `json:"utility_type" v:"required,enum=ELECTRICITY|WATER|GAS|INTERNET|WASTE|SECURITY|DIESEL_GENERATOR|OTHER"`
	ProviderName    string  `json:"provider_name" v:"required,max=120"`
	AccountRefLast4 *string `json:"account_ref_last4,omitempty" v:"pattern=[A-Za-z0-9]{1,4}" doc:"Never send the full provider account number"`
	MeterNumber     *string `json:"meter_number,omitempty" v:"max=64"`
	MeterType       string  `json:"meter_type" v:"required,enum=PREPAID|POSTPAID|SHARED|NONE"`
	Responsibility  string  `json:"responsibility" v:"required,enum=OWNER|TENANT|SHARED"`
	Status          string  `json:"status,omitempty" v:"enum=ACTIVE|CLOSED" doc:"Updates only"`
}

// ListUtilityAccounts lists a property's utility accounts.
func (s *Service) ListUtilityAccounts(ctx context.Context, subj authz.Subject, propertyID ids.ID) ([]UtilityAccountView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapUtilitiesManage), propertyID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListUtilityAccounts(ctx, s.dbq(ctx), propertyID)
	out := make([]UtilityAccountView, 0, len(rows))
	for _, r := range rows {
		out = append(out, utilityView(r))
	}
	return out, err
}

// CreateUtilityAccount records a utility account for a property.
func (s *Service) CreateUtilityAccount(ctx context.Context, subj authz.Subject, propertyID ids.ID, in UtilityAccountInput) (UtilityAccountView, error) {
	if err := schema.Validate(in); err != nil {
		return UtilityAccountView{}, err
	}
	var out UtilityAccountView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapUtilitiesManage), propertyID); err != nil {
			return err
		}
		if _, err := s.requireMutable(ctx, propertyID); err != nil {
			return err
		}
		if err := s.checkSpace(ctx, propertyID, in.UnitID, nil); err != nil {
			return err
		}
		u, err := s.q.InsertUtilityAccount(ctx, mustTx(ctx), db.InsertUtilityAccountParams{ID: ids.New(), PropertyID: propertyID, UnitID: in.UnitID,
			UtilityType: in.UtilityType, ProviderName: in.ProviderName, AccountRefLast4: in.AccountRefLast4, MeterNumber: in.MeterNumber, MeterType: in.MeterType,
			Responsibility: in.Responsibility, CreatedAt: s.now()})
		if err != nil {
			return err
		}
		out = utilityView(u)
		return s.record(ctx, "utility.create", AggUtility, u.ID, map[string]any{"utility_type": in.UtilityType, "responsibility": in.Responsibility}, nil)
	})
	return out, err
}

func (s *Service) utilityFor(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.UtilityAccount, error) {
	u, err := s.q.GetUtilityAccount(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return u, notFound(AggUtility)
	}
	if err != nil {
		return u, err
	}
	if _, err := s.authorize(ctx, subj, action, u.PropertyID); err != nil {
		if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
			err = notFound(AggUtility)
		}
		return u, err
	}
	return u, nil
}

// UpdateUtilityAccount updates a utility account (If-Match).
func (s *Service) UpdateUtilityAccount(ctx context.Context, subj authz.Subject, id ids.ID, expected int64, in UtilityAccountInput) (UtilityAccountView, error) {
	if err := schema.Validate(in); err != nil {
		return UtilityAccountView{}, err
	}
	var out UtilityAccountView
	err := s.InTx(ctx, func(ctx context.Context) error {
		u, err := s.utilityFor(ctx, subj, id, authz.Do(authz.CapUtilitiesManage))
		if err != nil {
			return err
		}
		if in.UtilityType != u.UtilityType {
			return precondition("UTILITY_TYPE_IMMUTABLE", "Create a new account for a different utility type.")
		}
		status := u.Status
		if in.Status != "" {
			status = in.Status
		}
		n, err := s.q.UpdateUtilityAccount(ctx, mustTx(ctx), db.UpdateUtilityAccountParams{ID: id, Version: expected, ProviderName: in.ProviderName,
			AccountRefLast4: in.AccountRefLast4, MeterNumber: in.MeterNumber, MeterType: in.MeterType, Responsibility: in.Responsibility, Status: status,
			UpdatedAt: s.now()})
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		out = utilityView(n)
		return s.record(ctx, "utility.update", AggUtility, id, map[string]any{"status": status, "responsibility": in.Responsibility}, nil)
	})
	return out, err
}

// AllocationRuleInput sets a new allocation rule version.
type AllocationRuleInput struct {
	Method string         `json:"method" v:"required,enum=EQUAL|PERCENTAGE|FIXED|SUB_METER"`
	Shares []domain.Share `json:"shares" v:"required,max=50"`
}

// AllocationRuleView is one allocation rule version.
type AllocationRuleView struct {
	VersionNo int32          `json:"version_no"`
	Method    string         `json:"method"`
	Shares    []domain.Share `json:"shares"`
	CreatedAt time.Time      `json:"created_at"`
}

// SetAllocationRule versions the allocation rule of a shared utility.
func (s *Service) SetAllocationRule(ctx context.Context, subj authz.Subject, id ids.ID, in AllocationRuleInput) (AllocationRuleView, error) {
	if err := schema.Validate(in); err != nil {
		return AllocationRuleView{}, err
	}
	if in.Method == "PERCENTAGE" {
		if _, err := domain.Allocate(in.Method, in.Shares, 10000); err != nil {
			return AllocationRuleView{}, err
		}
	}
	var out AllocationRuleView
	err := s.InTx(ctx, func(ctx context.Context) error {
		u, err := s.utilityFor(ctx, subj, id, authz.Do(authz.CapUtilitiesManage))
		if err != nil {
			return err
		}
		var dom *string
		var actor *ids.ID
		if a := actorFromCtx(ctx); a != nil {
			dom, actor = &a.Domain, &a.ID
		}
		r, err := s.q.InsertAllocationRule(ctx, mustTx(ctx), db.InsertAllocationRuleParams{ID: ids.New(), UtilityAccountID: u.ID, Method: in.Method,
			Shares: jsonb(in.Shares), ActorDomain: dom, ActorID: actor, CreatedAt: s.now()})
		if err != nil {
			return err
		}
		if err := s.q.SetUtilityRuleVersion(ctx, mustTx(ctx), db.SetUtilityRuleVersionParams{ID: u.ID, AllocationRuleVersion: r.VersionNo, UpdatedAt: s.now()}); err != nil {
			return err
		}
		out = AllocationRuleView{VersionNo: r.VersionNo, Method: r.Method, Shares: in.Shares, CreatedAt: r.CreatedAt}
		return s.record(ctx, "utility.allocation_rule", AggUtility, id, map[string]any{"version_no": r.VersionNo, "method": in.Method}, nil)
	})
	return out, err
}

// MeterReadingInput records a reading.
type MeterReadingInput struct {
	UtilityAccountID ids.ID     `json:"utility_account_id" v:"required"`
	ReadingType      string     `json:"reading_type" v:"required,enum=MOVE_IN|PERIODIC|MOVE_OUT|CORRECTION"`
	Value            float64    `json:"value" v:"min=0"`
	Unit             string     `json:"unit" v:"required,enum=KWH|M3|LITRE|UNIT|OTHER"`
	ReadAt           *time.Time `json:"read_at,omitempty"`
	MediaID          *ids.ID    `json:"media_id,omitempty" doc:"Photo of the meter (required for move-in and move-out readings)"`
	TenancyID        *ids.ID    `json:"tenancy_id,omitempty"`
	CorrectsID       *ids.ID    `json:"corrects_id,omitempty" doc:"CORRECTION: the reading being corrected (it is kept)"`
}

// MediaRefs lists referenced media.
func (in MeterReadingInput) MediaRefs() []ids.ID { return optIDs(in.MediaID) }

// CustomerMediaRefs lists referenced customer media.
func (in MeterReadingInput) CustomerMediaRefs() []ids.ID { return optIDs(in.MediaID) }

// MeterReadingView is a recorded reading.
type MeterReadingView struct {
	ID               ids.ID    `json:"id"`
	UtilityAccountID ids.ID    `json:"utility_account_id"`
	TenancyID        *ids.ID   `json:"tenancy_id"`
	ReadingType      string    `json:"reading_type"`
	Value            float64   `json:"value"`
	Unit             string    `json:"unit"`
	ReadAt           time.Time `json:"read_at"`
	MediaID          *ids.ID   `json:"media_id"`
	CorrectsID       *ids.ID   `json:"corrects_id"`
	RecordedByParty  string    `json:"recorded_by_party"`
	CreatedAt        time.Time `json:"created_at"`
}

func meterView(m db.MeterReading) MeterReadingView {
	return MeterReadingView{ID: m.ID, UtilityAccountID: m.UtilityAccountID, TenancyID: m.TenancyID, ReadingType: m.ReadingType, Value: m.Value, Unit: m.Unit,
		ReadAt: m.ReadAt, MediaID: m.MediaID, CorrectsID: m.CorrectsID, RecordedByParty: m.RecordedByParty, CreatedAt: m.CreatedAt}
}

func (s *Service) insertReading(ctx context.Context, u db.UtilityAccount, party string, in MeterReadingInput) (db.MeterReading, error) {
	tx, now := mustTx(ctx), s.now()
	if (in.ReadingType == "MOVE_IN" || in.ReadingType == "MOVE_OUT") && in.MediaID == nil {
		return db.MeterReading{}, precondition("METER_PHOTO_REQUIRED", "Move-in and move-out readings need a photo of the meter.")
	}
	if (in.ReadingType == "CORRECTION") != (in.CorrectsID != nil) {
		return db.MeterReading{}, apperrors.InvalidRequest("corrects_id is required for (and only for) CORRECTION readings.")
	}
	if in.CorrectsID != nil {
		prev, err := s.q.GetMeterReading(ctx, tx, *in.CorrectsID)
		if isNoRows(err) || (err == nil && prev.UtilityAccountID != u.ID) {
			return db.MeterReading{}, precondition("READING_NOT_FOUND", "The corrected reading does not belong to this utility account.")
		}
		if err != nil {
			return db.MeterReading{}, err
		}
	}
	if in.TenancyID != nil {
		t, err := s.q.GetTenancy(ctx, tx, *in.TenancyID)
		if isNoRows(err) || (err == nil && t.PropertyID != u.PropertyID) {
			return db.MeterReading{}, precondition("TENANCY_NOT_IN_PROPERTY", "The tenancy does not belong to this property.")
		}
		if err != nil {
			return db.MeterReading{}, err
		}
	}
	at := now
	if in.ReadAt != nil {
		if in.ReadAt.After(now.Add(5 * time.Minute)) {
			return db.MeterReading{}, apperrors.InvalidRequest("read_at cannot be in the future.")
		}
		at = *in.ReadAt
	}
	var dom *string
	var actor *ids.ID
	if a := actorFromCtx(ctx); a != nil {
		dom, actor = &a.Domain, &a.ID
	}
	return s.q.InsertMeterReading(ctx, tx, db.InsertMeterReadingParams{ID: ids.New(), UtilityAccountID: u.ID, PropertyID: u.PropertyID, TenancyID: in.TenancyID,
		ReadingType: in.ReadingType, Value: in.Value, Unit: in.Unit, ReadAt: at, MediaID: in.MediaID, CorrectsID: in.CorrectsID, RecordedByParty: party,
		ActorDomain: dom, ActorID: actor, CreatedAt: now})
}

// RecordMeterReading records an owner-side meter reading (author attributed).
func (s *Service) RecordMeterReading(ctx context.Context, subj authz.Subject, propertyID ids.ID, in MeterReadingInput, ev Evidence) (MeterReadingView, error) {
	if err := schema.Validate(in); err != nil {
		return MeterReadingView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return MeterReadingView{}, err
	}
	var out MeterReadingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		u, err := s.utilityFor(ctx, subj, in.UtilityAccountID, authz.Do(authz.CapUtilitiesManage))
		if err != nil {
			return err
		}
		if u.PropertyID != propertyID {
			return notFound(AggUtility)
		}
		party := "OWNER"
		if subj.Kind == authz.KindService {
			party = "MANAGER"
		}
		m, err := s.insertReading(ctx, u, party, in)
		if err != nil {
			return err
		}
		out = meterView(m)
		return s.record(ctx, "utility.meter_reading", AggUtility, u.ID, map[string]any{"reading_type": in.ReadingType, "value": in.Value}, nil)
	})
	return out, err
}

// ListMeterReadings lists readings of a property (optionally one account).
func (s *Service) ListMeterReadings(ctx context.Context, subj authz.Subject, propertyID, accountID ids.ID) ([]MeterReadingView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapUtilitiesManage), propertyID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListMeterReadings(ctx, s.dbq(ctx), db.ListMeterReadingsParams{PropertyID: propertyID, UtilityAccountID: idp(accountID)})
	out := make([]MeterReadingView, 0, len(rows))
	for _, r := range rows {
		out = append(out, meterView(r))
	}
	return out, err
}

// CustomerRecordMeterReading lets the tenant submit a reading for their tenancy.
func (s *Service) CustomerRecordMeterReading(ctx context.Context, subj authz.Subject, tenancyID ids.ID, in MeterReadingInput, ev Evidence) (MeterReadingView, error) {
	if err := schema.Validate(in); err != nil {
		return MeterReadingView{}, err
	}
	if err := ev.require(in.CustomerMediaRefs()...); err != nil {
		return MeterReadingView{}, err
	}
	var out MeterReadingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		t, _, err := s.customerTenancy(ctx, subj, tenancyID, false)
		if err != nil {
			return err
		}
		if t.Status == "ENDED" {
			return conflict("TENANCY_ENDED", "The tenancy has ended.")
		}
		u, err := s.q.GetUtilityAccount(ctx, mustTx(ctx), in.UtilityAccountID)
		if isNoRows(err) || (err == nil && (u.PropertyID != t.PropertyID || (u.UnitID != nil && t.UnitID != nil && *u.UnitID != *t.UnitID))) {
			return notFound(AggUtility)
		}
		if err != nil {
			return err
		}
		in.TenancyID = &t.ID
		m, err := s.insertReading(ctx, u, "TENANT", in)
		if err != nil {
			return err
		}
		out = meterView(m)
		return s.record(ctx, "utility.meter_reading.tenant", AggUtility, u.ID, map[string]any{"reading_type": in.ReadingType, "value": in.Value}, nil)
	})
	return out, err
}

// ---- bills ----

// UtilityBillInput records a bill.
type UtilityBillInput struct {
	UtilityAccountID ids.ID  `json:"utility_account_id" v:"required"`
	PeriodStart      string  `json:"period_start" v:"required,format=date"`
	PeriodEnd        string  `json:"period_end" v:"required,format=date"`
	AmountMinor      int64   `json:"amount_minor" v:"min=0"`
	Currency         string  `json:"currency" v:"required,format=currency"`
	BillMediaID      *ids.ID `json:"bill_media_id,omitempty" doc:"Bill/receipt document in media-document-service"`
}

// MediaRefs lists referenced media.
func (in UtilityBillInput) MediaRefs() []ids.ID { return optIDs(in.BillMediaID) }

// UtilityBillView is a utility bill and its allocation.
type UtilityBillView struct {
	ID                    ids.ID              `json:"id"`
	UtilityAccountID      ids.ID              `json:"utility_account_id"`
	PeriodStart           string              `json:"period_start"`
	PeriodEnd             string              `json:"period_end"`
	AmountMinor           int64               `json:"amount_minor"`
	Currency              string              `json:"currency"`
	BillMediaID           *ids.ID             `json:"bill_media_id"`
	Status                string              `json:"status"`
	Allocation            []domain.Allocation `json:"allocation"`
	AllocationRuleVersion *int32              `json:"allocation_rule_version"`
	OriginalAllocation    []domain.Allocation `json:"original_allocation" doc:"Preserved when the allocation is disputed"`
	DisputeReason         *string             `json:"dispute_reason"`
	Version               int64               `json:"version"`
}

func billView(b db.UtilityBill) UtilityBillView {
	v := UtilityBillView{ID: b.ID, UtilityAccountID: b.UtilityAccountID, PeriodStart: b.PeriodStart.Format(time.DateOnly), PeriodEnd: b.PeriodEnd.Format(time.DateOnly),
		AmountMinor: b.AmountMinor, Currency: b.Currency, BillMediaID: b.BillMediaID, Status: b.Status, AllocationRuleVersion: b.AllocationRuleVersion,
		DisputeReason: b.DisputeReason, Version: b.Version, Allocation: []domain.Allocation{}, OriginalAllocation: []domain.Allocation{}}
	_ = json.Unmarshal(b.Allocation, &v.Allocation)
	_ = json.Unmarshal(b.OriginalAllocation, &v.OriginalAllocation)
	return v
}

// RecordUtilityBill records a bill against a utility account.
func (s *Service) RecordUtilityBill(ctx context.Context, subj authz.Subject, propertyID ids.ID, in UtilityBillInput, ev Evidence) (UtilityBillView, error) {
	if err := schema.Validate(in); err != nil {
		return UtilityBillView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return UtilityBillView{}, err
	}
	ps, pe := schema.ParseDate(&in.PeriodStart), schema.ParseDate(&in.PeriodEnd)
	if pe.Before(*ps) {
		return UtilityBillView{}, apperrors.InvalidRequest("period_end must not be before period_start.")
	}
	var out UtilityBillView
	err := s.InTx(ctx, func(ctx context.Context) error {
		u, err := s.utilityFor(ctx, subj, in.UtilityAccountID, authz.Do(authz.CapUtilitiesManage))
		if err != nil {
			return err
		}
		if u.PropertyID != propertyID {
			return notFound(AggUtility)
		}
		b, err := s.q.InsertUtilityBill(ctx, mustTx(ctx), db.InsertUtilityBillParams{ID: ids.New(), UtilityAccountID: u.ID, PropertyID: u.PropertyID,
			PeriodStart: *ps, PeriodEnd: *pe, AmountMinor: in.AmountMinor, Currency: in.Currency, BillMediaID: in.BillMediaID, CreatedAt: s.now()})
		if isUnique(err, "utility_bills_uniq") {
			return conflict("BILL_ALREADY_RECORDED", "A bill for this account and period is already recorded.")
		}
		if err != nil {
			return err
		}
		out = billView(b)
		return s.record(ctx, "utility.bill", AggUtility, u.ID, map[string]any{"bill_id": b.ID, "amount_minor": in.AmountMinor}, nil)
	})
	return out, err
}

// ListUtilityBills lists bills of a property (optionally one account).
func (s *Service) ListUtilityBills(ctx context.Context, subj authz.Subject, propertyID, accountID ids.ID) ([]UtilityBillView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapUtilitiesManage), propertyID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListUtilityBills(ctx, s.dbq(ctx), db.ListUtilityBillsParams{PropertyID: propertyID, UtilityAccountID: idp(accountID)})
	out := make([]UtilityBillView, 0, len(rows))
	for _, r := range rows {
		out = append(out, billView(r))
	}
	return out, err
}

// BillActionInput allocates, disputes or resolves a bill.
type BillActionInput struct {
	Action string  `json:"action" v:"required,enum=ALLOCATE|DISPUTE|RESOLVE" doc:"ALLOCATE/RESOLVE apply the account's current allocation rule version"`
	Reason *string `json:"reason,omitempty" v:"max=1000"`
}

// ActOnUtilityBill allocates a bill with the current rule version, records a
// dispute (keeping the original calculation) or resolves it by
// re-allocating with the then-current rule (If-Match).
func (s *Service) ActOnUtilityBill(ctx context.Context, subj authz.Subject, billID ids.ID, expected int64, in BillActionInput) (UtilityBillView, error) {
	if err := schema.Validate(in); err != nil {
		return UtilityBillView{}, err
	}
	var out UtilityBillView
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx := mustTx(ctx)
		b, err := s.q.GetUtilityBillForUpdate(ctx, tx, billID)
		if isNoRows(err) {
			return notFound("utility_bill")
		}
		if err != nil {
			return err
		}
		u, err := s.utilityFor(ctx, subj, b.UtilityAccountID, authz.Do(authz.CapUtilitiesManage))
		if err != nil {
			return err
		}
		if err := checkVersion(expected, b.Version); err != nil {
			return err
		}
		p := db.UpdateUtilityBillStateParams{ID: b.ID, Version: b.Version, Status: b.Status, Allocation: b.Allocation, AllocationRuleVersion: b.AllocationRuleVersion,
			OriginalAllocation: b.OriginalAllocation, DisputeReason: b.DisputeReason, UpdatedAt: s.now()}
		allocate := func() error {
			if u.AllocationRuleVersion == 0 {
				return precondition("ALLOCATION_RULE_REQUIRED", "Set an allocation rule for this utility first.")
			}
			r, err := s.q.GetAllocationRule(ctx, tx, db.GetAllocationRuleParams{UtilityAccountID: u.ID, VersionNo: u.AllocationRuleVersion})
			if err != nil {
				return err
			}
			var shares []domain.Share
			_ = json.Unmarshal(r.Shares, &shares)
			alloc, err := domain.Allocate(r.Method, shares, b.AmountMinor)
			if err != nil {
				return err
			}
			p.Allocation, p.AllocationRuleVersion = jsonb(alloc), &r.VersionNo
			return nil
		}
		switch in.Action {
		case "ALLOCATE":
			if b.Status != "RECORDED" {
				return conflict("BILL_ALREADY_ALLOCATED", "The bill is "+b.Status+".")
			}
			if err := allocate(); err != nil {
				return err
			}
			p.Status = "ALLOCATED"
		case "DISPUTE":
			if b.Status != "ALLOCATED" {
				return conflict("BILL_NOT_ALLOCATED", "Only an allocated bill can be disputed.")
			}
			if in.Reason == nil {
				return apperrors.InvalidRequest("A dispute needs a reason.")
			}
			p.Status, p.OriginalAllocation, p.DisputeReason = "DISPUTED", b.Allocation, in.Reason
		case "RESOLVE":
			if b.Status != "DISPUTED" {
				return conflict("BILL_NOT_DISPUTED", "The bill is not disputed.")
			}
			if err := allocate(); err != nil {
				return err
			}
			p.Status = "RESOLVED"
		}
		n, err := s.q.UpdateUtilityBillState(ctx, tx, p)
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		out = billView(n)
		return s.record(ctx, "utility.bill."+lower(in.Action), AggUtility, u.ID, map[string]any{"bill_id": b.ID, "rule_version": p.AllocationRuleVersion}, nil)
	})
	return out, err
}

// ---- service-charge budgets ----

// BudgetCategory is one budget line.
type BudgetCategory struct {
	Category      string `json:"category" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}"`
	ExpectedMinor int64  `json:"expected_minor" v:"min=0"`
}

// BudgetInput creates or edits a draft budget.
type BudgetInput struct {
	PeriodStart string           `json:"period_start" v:"required,format=date"`
	PeriodEnd   string           `json:"period_end" v:"required,format=date"`
	Currency    string           `json:"currency" v:"required,format=currency"`
	Categories  []BudgetCategory `json:"categories" v:"required,max=50"`
	Status      string           `json:"status,omitempty" v:"enum=DRAFT|APPROVED|CLOSED" doc:"Updates: DRAFT → APPROVED → CLOSED"`
}

// BudgetLine compares expected and recorded amounts of a category.
type BudgetLine struct {
	Category      string `json:"category"`
	ExpectedMinor int64  `json:"expected_minor"`
	ActualMinor   int64  `json:"actual_minor"`
	BilledMinor   int64  `json:"billed_minor"`
	PaidMinor     int64  `json:"paid_minor" doc:"Evidence-backed paid amounts (not the payment ledger)"`
	AdjustedMinor int64  `json:"adjusted_minor"`
}

// BudgetView is a service-charge budget with actuals.
type BudgetView struct {
	ID                 ids.ID       `json:"id"`
	PropertyID         ids.ID       `json:"property_id"`
	PeriodStart        string       `json:"period_start"`
	PeriodEnd          string       `json:"period_end"`
	Currency           string       `json:"currency"`
	Status             string       `json:"status"`
	TotalExpectedMinor int64        `json:"total_expected_minor"`
	Lines              []BudgetLine `json:"lines"`
	ApprovedAt         *time.Time   `json:"approved_at"`
	ClosedAt           *time.Time   `json:"closed_at"`
	Version            int64        `json:"version"`
}

func (s *Service) budgetView(ctx context.Context, b db.ServiceChargeBudget) (BudgetView, error) {
	v := BudgetView{ID: b.ID, PropertyID: b.PropertyID, PeriodStart: b.PeriodStart.Format(time.DateOnly), PeriodEnd: b.PeriodEnd.Format(time.DateOnly),
		Currency: b.Currency, Status: b.Status, TotalExpectedMinor: b.TotalExpectedMinor, ApprovedAt: b.ApprovedAt, ClosedAt: b.ClosedAt, Version: b.Version,
		Lines: []BudgetLine{}}
	var cats []BudgetCategory
	_ = json.Unmarshal(b.Categories, &cats)
	idx := map[string]int{}
	for _, c := range cats {
		idx[c.Category] = len(v.Lines)
		v.Lines = append(v.Lines, BudgetLine{Category: c.Category, ExpectedMinor: c.ExpectedMinor})
	}
	totals, err := s.q.BudgetTotals(ctx, s.dbq(ctx), b.ID)
	for _, t := range totals {
		i, ok := idx[t.Category]
		if !ok {
			continue
		}
		switch t.ItemType {
		case "ACTUAL":
			v.Lines[i].ActualMinor = t.TotalMinor
		case "BILLED":
			v.Lines[i].BilledMinor = t.TotalMinor
		case "PAID":
			v.Lines[i].PaidMinor = t.TotalMinor
		case "ADJUSTMENT":
			v.Lines[i].AdjustedMinor = t.TotalMinor
		}
	}
	return v, err
}

func budgetTotals(in BudgetInput) (int64, error) {
	var total int64
	seen := map[string]bool{}
	for _, c := range in.Categories {
		if seen[c.Category] {
			return 0, apperrors.InvalidRequest("Duplicate budget category " + c.Category + ".")
		}
		seen[c.Category] = true
		total += c.ExpectedMinor
	}
	ps, pe := schema.ParseDate(&in.PeriodStart), schema.ParseDate(&in.PeriodEnd)
	if !pe.After(*ps) {
		return 0, apperrors.InvalidRequest("period_end must be after period_start.")
	}
	return total, nil
}

// ListBudgets lists a property's service-charge budgets.
func (s *Service) ListBudgets(ctx context.Context, subj authz.Subject, propertyID ids.ID) ([]BudgetView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapServiceCharges), propertyID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListBudgets(ctx, s.dbq(ctx), propertyID)
	if err != nil {
		return nil, err
	}
	out := make([]BudgetView, 0, len(rows))
	for _, r := range rows {
		v, err := s.budgetView(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// CreateBudget drafts a service-charge budget.
func (s *Service) CreateBudget(ctx context.Context, subj authz.Subject, propertyID ids.ID, in BudgetInput) (BudgetView, error) {
	if err := schema.Validate(in); err != nil {
		return BudgetView{}, err
	}
	total, err := budgetTotals(in)
	if err != nil {
		return BudgetView{}, err
	}
	var out BudgetView
	err = s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapServiceCharges), propertyID); err != nil {
			return err
		}
		if _, err := s.requireMutable(ctx, propertyID); err != nil {
			return err
		}
		b, err := s.q.InsertBudget(ctx, mustTx(ctx), db.InsertBudgetParams{ID: ids.New(), PropertyID: propertyID, PeriodStart: *schema.ParseDate(&in.PeriodStart),
			PeriodEnd: *schema.ParseDate(&in.PeriodEnd), Currency: in.Currency, Categories: jsonb(in.Categories), TotalExpectedMinor: total, CreatedAt: s.now()})
		if isUnique(err, "service_charge_budgets_uniq") {
			return conflict("BUDGET_EXISTS", "A budget for this period already exists.")
		}
		if err != nil {
			return err
		}
		if err := s.record(ctx, "service_charge.budget.create", AggBudget, b.ID, map[string]any{"total_expected_minor": total}, nil); err != nil {
			return err
		}
		out, err = s.budgetView(ctx, b)
		return err
	})
	return out, err
}

func (s *Service) budgetFor(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.ServiceChargeBudget, error) {
	b, err := s.q.GetBudget(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return b, notFound(AggBudget)
	}
	if err != nil {
		return b, err
	}
	if _, err := s.authorize(ctx, subj, action, b.PropertyID); err != nil {
		if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
			err = notFound(AggBudget)
		}
		return b, err
	}
	return b, nil
}

// GetBudget returns a budget with expected vs recorded amounts.
func (s *Service) GetBudget(ctx context.Context, subj authz.Subject, id ids.ID) (BudgetView, error) {
	b, err := s.budgetFor(ctx, subj, id, authz.Read(authz.CapServiceCharges))
	if err != nil {
		return BudgetView{}, err
	}
	return s.budgetView(ctx, b)
}

// UpdateBudget edits a DRAFT budget or moves it DRAFT → APPROVED → CLOSED (If-Match).
func (s *Service) UpdateBudget(ctx context.Context, subj authz.Subject, id ids.ID, expected int64, in BudgetInput) (BudgetView, error) {
	if err := schema.Validate(in); err != nil {
		return BudgetView{}, err
	}
	total, err := budgetTotals(in)
	if err != nil {
		return BudgetView{}, err
	}
	var out BudgetView
	err = s.InTx(ctx, func(ctx context.Context) error {
		b, err := s.budgetFor(ctx, subj, id, authz.Do(authz.CapServiceCharges))
		if err != nil {
			return err
		}
		status := b.Status
		if in.Status != "" {
			status = in.Status
		}
		allowed := map[string][]string{"DRAFT": {"DRAFT", "APPROVED"}, "APPROVED": {"APPROVED", "CLOSED"}, "CLOSED": {}}
		if !slices.Contains(allowed[b.Status], status) {
			return apperrors.InvalidStateTransition(b.Status, status)
		}
		if b.Status != "DRAFT" && (string(jsonb(in.Categories)) != string(b.Categories) || total != b.TotalExpectedMinor ||
			!schema.ParseDate(&in.PeriodEnd).Equal(b.PeriodEnd)) {
			return conflict("BUDGET_NOT_EDITABLE", "Approved budgets are changed through ADJUSTMENT items, not edits.")
		}
		if !schema.ParseDate(&in.PeriodStart).Equal(b.PeriodStart) || in.Currency != b.Currency {
			return precondition("BUDGET_PERIOD_IMMUTABLE", "The period start and currency of a budget cannot change.")
		}
		now := s.now()
		approved, closed := b.ApprovedAt, b.ClosedAt
		if status == "APPROVED" && approved == nil {
			approved = &now
		}
		if status == "CLOSED" && closed == nil {
			closed = &now
		}
		n, err := s.q.UpdateBudget(ctx, mustTx(ctx), db.UpdateBudgetParams{ID: id, Version: expected, PeriodEnd: *schema.ParseDate(&in.PeriodEnd),
			Categories: jsonb(in.Categories), TotalExpectedMinor: total, Status: status, ApprovedAt: approved, ClosedAt: closed, UpdatedAt: now})
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		if err := s.record(ctx, "service_charge.budget.update", AggBudget, id, map[string]any{"status": status, "total_expected_minor": total}, nil); err != nil {
			return err
		}
		out, err = s.budgetView(ctx, n)
		return err
	})
	return out, err
}

// ServiceChargeItemInput records an actual/billed/paid amount or adjustment.
type ServiceChargeItemInput struct {
	Category         string   `json:"category" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}"`
	ItemType         string   `json:"item_type" v:"required,enum=ACTUAL|BILLED|PAID|ADJUSTMENT"`
	AmountMinor      int64    `json:"amount_minor" doc:"Positive; ADJUSTMENT may be negative"`
	Description      string   `json:"description" v:"required,max=500"`
	EvidenceMediaIDs []ids.ID `json:"evidence_media_ids,omitempty" v:"max=10"`
	AdjustsID        *ids.ID  `json:"adjusts_id,omitempty" doc:"ADJUSTMENT: the item being corrected (it is kept)"`
	IncurredOn       string   `json:"incurred_on" v:"required,format=date"`
}

// MediaRefs lists referenced media.
func (in ServiceChargeItemInput) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// ServiceChargeItemView is a recorded item.
type ServiceChargeItemView struct {
	ID               ids.ID    `json:"id"`
	Category         string    `json:"category"`
	ItemType         string    `json:"item_type"`
	AmountMinor      int64     `json:"amount_minor"`
	Description      string    `json:"description"`
	EvidenceMediaIDs []ids.ID  `json:"evidence_media_ids"`
	AdjustsID        *ids.ID   `json:"adjusts_id"`
	IncurredOn       string    `json:"incurred_on"`
	CreatedAt        time.Time `json:"created_at"`
}

func itemView(i db.ServiceChargeItem) ServiceChargeItemView {
	return ServiceChargeItemView{ID: i.ID, Category: i.Category, ItemType: i.ItemType, AmountMinor: i.AmountMinor, Description: i.Description,
		EvidenceMediaIDs: nonNilIDs(i.EvidenceMediaIds), AdjustsID: i.AdjustsID, IncurredOn: i.IncurredOn.Format(time.DateOnly), CreatedAt: i.CreatedAt}
}

// AddServiceChargeItem records an item on an APPROVED budget (append-only).
func (s *Service) AddServiceChargeItem(ctx context.Context, subj authz.Subject, budgetID ids.ID, in ServiceChargeItemInput, ev Evidence) (ServiceChargeItemView, error) {
	if err := schema.Validate(in); err != nil {
		return ServiceChargeItemView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return ServiceChargeItemView{}, err
	}
	if in.ItemType != "ADJUSTMENT" && in.AmountMinor < 0 {
		return ServiceChargeItemView{}, apperrors.InvalidRequest("Only adjustments can be negative.")
	}
	if (in.ItemType == "ADJUSTMENT") != (in.AdjustsID != nil) {
		return ServiceChargeItemView{}, apperrors.InvalidRequest("adjusts_id is required for (and only for) ADJUSTMENT items.")
	}
	if (in.ItemType == "PAID" || in.ItemType == "ACTUAL") && len(in.EvidenceMediaIDs) == 0 {
		return ServiceChargeItemView{}, precondition("EVIDENCE_REQUIRED", "Actual and paid items need evidence (invoice, receipt).")
	}
	var out ServiceChargeItemView
	err := s.InTx(ctx, func(ctx context.Context) error {
		b, err := s.budgetFor(ctx, subj, budgetID, authz.Do(authz.CapServiceCharges))
		if err != nil {
			return err
		}
		if b.Status != "APPROVED" {
			return conflict("BUDGET_NOT_APPROVED", "Items are recorded against an approved, open budget.")
		}
		var cats []BudgetCategory
		_ = json.Unmarshal(b.Categories, &cats)
		if !slices.ContainsFunc(cats, func(c BudgetCategory) bool { return c.Category == in.Category }) {
			return precondition("UNKNOWN_BUDGET_CATEGORY", "The category is not part of this budget.")
		}
		tx := mustTx(ctx)
		if in.AdjustsID != nil {
			prev, err := s.q.GetServiceChargeItem(ctx, tx, *in.AdjustsID)
			if isNoRows(err) || (err == nil && prev.BudgetID != b.ID) {
				return precondition("ITEM_NOT_FOUND", "The adjusted item does not belong to this budget.")
			}
			if err != nil {
				return err
			}
		}
		var dom *string
		var actor *ids.ID
		if a := actorFromCtx(ctx); a != nil {
			dom, actor = &a.Domain, &a.ID
		}
		i, err := s.q.InsertServiceChargeItem(ctx, tx, db.InsertServiceChargeItemParams{ID: ids.New(), BudgetID: b.ID, PropertyID: b.PropertyID, Category: in.Category,
			ItemType: in.ItemType, AmountMinor: in.AmountMinor, Description: in.Description, EvidenceMediaIds: nonNilIDs(in.EvidenceMediaIDs),
			AdjustsID: in.AdjustsID, IncurredOn: *schema.ParseDate(&in.IncurredOn), ActorDomain: dom, ActorID: actor, CreatedAt: s.now()})
		if err != nil {
			return err
		}
		out = itemView(i)
		return s.record(ctx, "service_charge.item", AggBudget, b.ID, map[string]any{"item_type": in.ItemType, "category": in.Category, "amount_minor": in.AmountMinor}, nil)
	})
	return out, err
}

// ListServiceChargeItems lists a budget's items.
func (s *Service) ListServiceChargeItems(ctx context.Context, subj authz.Subject, budgetID ids.ID) ([]ServiceChargeItemView, error) {
	if _, err := s.budgetFor(ctx, subj, budgetID, authz.Read(authz.CapServiceCharges)); err != nil {
		return nil, err
	}
	rows, err := s.q.ListServiceChargeItems(ctx, s.dbq(ctx), budgetID)
	out := make([]ServiceChargeItemView, 0, len(rows))
	for _, r := range rows {
		out = append(out, itemView(r))
	}
	return out, err
}
