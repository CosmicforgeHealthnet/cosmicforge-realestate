package usecases

import (
	"context"
	"fmt"
	"net/http"
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
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Maintenance, artisans and preventive care (§13.22). The owner domain owns
// the request, its responsibility, the approval (owner, co-owner or a
// delegated manager within its spend limits) and the property history;
// artisan-service discovers eligible requests from maintenance.request.*
// events and reports quotes and job progress through the provider contract
// (/internal/v1/owner/maintenance…, the provider is the delegated actor).
// Provider evidence is append-only, so a challenged completion never
// overwrites what was submitted. Providers are paid by payment-service only
// for an accepted job and never above the approved amount; off-platform
// payments are evidence plus the provider's acknowledgement.

const AggMaintenance = "maintenance_request"

// Owner notification templates of the maintenance context.
const (
	TemplateMaintenanceUpdate = "owner.maintenance_update" // request_title, status
	TemplatePreventiveDue     = "owner.preventive_due"     // title, due_on
)

// MaintenanceInput reports a maintenance need.
type MaintenanceInput struct {
	PropertyID       ids.ID   `json:"property_id" v:"required"`
	UnitID           *ids.ID  `json:"unit_id,omitempty"`
	AssetID          *ids.ID  `json:"asset_id,omitempty" doc:"Appliance/equipment from the property asset register"`
	TenancyID        *ids.ID  `json:"tenancy_id,omitempty"`
	Title            string   `json:"title" v:"required,max=120"`
	Description      string   `json:"description" v:"required,max=4000"`
	Category         string   `json:"category" v:"required,pattern=[A-Z][A-Z0-9_]{1,39}" doc:"e.g. PLUMBING, ELECTRICAL, ROOFING, HVAC, PEST_CONTROL"`
	Priority         string   `json:"priority" v:"required,enum=LOW|NORMAL|HIGH|EMERGENCY"`
	BudgetMinor      *int64   `json:"budget_minor,omitempty" v:"min=1"`
	Currency         string   `json:"currency,omitempty" v:"pattern=[A-Z]{3}"`
	EvidenceMediaIDs []ids.ID `json:"evidence_media_ids,omitempty" v:"max=20"`
	CFMeetContextID  *string  `json:"cfmeet_context_id,omitempty" v:"max=128" doc:"CF Meet conversation for this request (messages stay in CF Meet)"`
}

// MediaRefs lists referenced media.
func (in MaintenanceInput) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// QuoteView is a provider quote.
type QuoteView struct {
	ID                  ids.ID     `json:"id"`
	Source              string     `json:"source"`
	ProviderRef         *ids.ID    `json:"provider_ref"`
	ProviderDisplayName string     `json:"provider_display_name"`
	Scope               string     `json:"scope"`
	AmountMinor         int64      `json:"amount_minor"`
	Currency            string     `json:"currency"`
	EarliestStartAt     *time.Time `json:"earliest_start_at"`
	ValidUntil          *time.Time `json:"valid_until"`
	WarrantyDays        int32      `json:"warranty_days"`
	EvidenceMediaIDs    []ids.ID   `json:"evidence_media_ids"`
	Status              string     `json:"status"`
	CreatedAt           time.Time  `json:"created_at"`
}

// ApprovalView is an append-only approval decision.
type ApprovalView struct {
	QuoteID     *ids.ID   `json:"quote_id"`
	Decision    string    `json:"decision"`
	AmountMinor *int64    `json:"amount_minor"`
	Currency    string    `json:"currency"`
	ApprovedVia string    `json:"approved_via"`
	ApproverRef ids.ID    `json:"approver_ref"`
	LimitMinor  *int64    `json:"limit_minor" doc:"Delegated manager's limit applied"`
	Emergency   bool      `json:"emergency"`
	Note        *string   `json:"note"`
	CreatedAt   time.Time `json:"created_at"`
}

// JobEventView is one append-only job record.
type JobEventView struct {
	EventType        string    `json:"event_type"`
	Source           string    `json:"source"`
	Note             *string   `json:"note"`
	EvidenceMediaIDs []ids.ID  `json:"evidence_media_ids"`
	AmountMinor      *int64    `json:"amount_minor"`
	OccurredAt       time.Time `json:"occurred_at"`
}

// JobView is the job executing the approved quote.
type JobView struct {
	ID                ids.ID         `json:"id"`
	QuoteID           ids.ID         `json:"quote_id"`
	ProviderRef       *ids.ID        `json:"provider_ref"`
	Status            string         `json:"status"`
	AmountMinor       int64          `json:"amount_minor" doc:"Approved amount (payment ceiling)"`
	InvoicedMinor     *int64         `json:"invoiced_minor"`
	Currency          string         `json:"currency"`
	PaymentCollection string         `json:"payment_collection"`
	PaidMinor         int64          `json:"paid_minor" doc:"payment-service truth (PLATFORM) or recorded evidence (OFF_PLATFORM)"`
	PaymentStatus     *string        `json:"payment_status"`
	ProviderAckPaid   bool           `json:"provider_acknowledged_payment"`
	ReworkCount       int32          `json:"rework_count"`
	AcceptedAt        *time.Time     `json:"accepted_at"`
	Events            []JobEventView `json:"events"`
}

// WarrantyView is the workmanship warranty of a completed job.
type WarrantyView struct {
	StartsOn       string  `json:"starts_on"`
	EndsOn         string  `json:"ends_on"`
	Status         string  `json:"status"`
	ClaimRequestID *ids.ID `json:"claim_request_id"`
}

// MaintenanceView is a maintenance request with its quotes, approvals, job
// and warranty.
type MaintenanceView struct {
	ID                   ids.ID         `json:"id"`
	PropertyID           ids.ID         `json:"property_id"`
	UnitID               *ids.ID        `json:"unit_id"`
	AssetID              *ids.ID        `json:"asset_id"`
	TenancyID            *ids.ID        `json:"tenancy_id"`
	PreventiveScheduleID *ids.ID        `json:"preventive_schedule_id"`
	WarrantyOfRequestID  *ids.ID        `json:"warranty_of_request_id"`
	Title                string         `json:"title"`
	Description          string         `json:"description"`
	Category             string         `json:"category"`
	Priority             string         `json:"priority"`
	Responsibility       string         `json:"responsibility"`
	ResponsibilityNote   *string        `json:"responsibility_note"`
	Sourcing             *string        `json:"sourcing"`
	ReportedByType       string         `json:"reported_by_type"`
	Status               string         `json:"status"`
	EvidenceMediaIDs     []ids.ID       `json:"evidence_media_ids"`
	BudgetMinor          *int64         `json:"budget_minor"`
	Currency             string         `json:"currency"`
	CFMeetContextID      *string        `json:"cfmeet_context_id"`
	CancelReason         *string        `json:"cancel_reason"`
	Quotes               []QuoteView    `json:"quotes"`
	Approvals            []ApprovalView `json:"approvals"`
	Job                  *JobView       `json:"job"`
	Warranty             *WarrantyView  `json:"warranty"`
	Version              int64          `json:"version"`
	CreatedAt            time.Time      `json:"created_at"`
}

func maintenanceBase(m db.MaintenanceRequest) MaintenanceView {
	return MaintenanceView{ID: m.ID, PropertyID: m.PropertyID, UnitID: m.UnitID, AssetID: m.AssetID, TenancyID: m.TenancyID,
		PreventiveScheduleID: m.PreventiveScheduleID, WarrantyOfRequestID: m.WarrantyOfRequestID, Title: m.Title, Description: m.Description,
		Category: m.Category, Priority: m.Priority, Responsibility: m.Responsibility, ResponsibilityNote: m.ResponsibilityNote, Sourcing: m.Sourcing,
		ReportedByType: m.ReportedByType, Status: m.Status, EvidenceMediaIDs: nonNilIDs(m.EvidenceMediaIds), BudgetMinor: m.BudgetMinor,
		Currency: m.Currency, CFMeetContextID: m.CfmeetContextID, CancelReason: m.CancelReason, Version: m.Version, CreatedAt: m.CreatedAt,
		Quotes: []QuoteView{}, Approvals: []ApprovalView{}}
}

func (s *Service) maintenanceView(ctx context.Context, m db.MaintenanceRequest) (MaintenanceView, error) {
	v := maintenanceBase(m)
	q := s.dbq(ctx)
	quotes, err := s.q.ListMaintenanceQuotes(ctx, q, m.ID)
	if err != nil {
		return v, err
	}
	for _, x := range quotes {
		v.Quotes = append(v.Quotes, QuoteView{ID: x.ID, Source: x.Source, ProviderRef: x.ProviderRef, ProviderDisplayName: x.ProviderDisplayName,
			Scope: x.Scope, AmountMinor: x.AmountMinor, Currency: x.Currency, EarliestStartAt: x.EarliestStartAt, ValidUntil: x.ValidUntil,
			WarrantyDays: x.WarrantyDays, EvidenceMediaIDs: nonNilIDs(x.EvidenceMediaIds), Status: x.Status, CreatedAt: x.CreatedAt})
	}
	approvals, err := s.q.ListMaintenanceApprovals(ctx, q, m.ID)
	if err != nil {
		return v, err
	}
	for _, a := range approvals {
		v.Approvals = append(v.Approvals, ApprovalView{QuoteID: a.QuoteID, Decision: a.Decision, AmountMinor: a.AmountMinor, Currency: a.Currency,
			ApprovedVia: a.ApprovedVia, ApproverRef: a.ApproverRef, LimitMinor: a.LimitMinor, Emergency: a.Emergency, Note: a.Note, CreatedAt: a.CreatedAt})
	}
	if j, err := s.q.GetActiveJob(ctx, q, m.ID); err == nil {
		jv := JobView{ID: j.ID, QuoteID: j.QuoteID, ProviderRef: j.ProviderRef, Status: j.Status, AmountMinor: j.AmountMinor, InvoicedMinor: j.InvoicedMinor,
			Currency: j.Currency, PaymentCollection: j.PaymentCollection, PaidMinor: j.PaidMinor, PaymentStatus: j.LastPaymentStatus,
			ProviderAckPaid: j.ProviderPaymentAck, ReworkCount: j.ReworkCount, AcceptedAt: j.AcceptedAt, Events: []JobEventView{}}
		evs, err := s.q.ListJobEvents(ctx, q, j.ID)
		if err != nil {
			return v, err
		}
		for _, e := range evs {
			jv.Events = append(jv.Events, JobEventView{EventType: e.EventType, Source: e.Source, Note: e.Note, EvidenceMediaIDs: nonNilIDs(e.EvidenceMediaIds),
				AmountMinor: e.AmountMinor, OccurredAt: e.OccurredAt})
		}
		v.Job = &jv
	} else if !isNoRows(err) {
		return v, err
	}
	if w, err := s.q.GetWarrantyForRequest(ctx, q, m.ID); err == nil {
		v.Warranty = &WarrantyView{StartsOn: w.StartsOn.Format(time.DateOnly), EndsOn: w.EndsOn.Format(time.DateOnly), Status: w.Status, ClaimRequestID: w.ClaimRequestID}
	} else if !isNoRows(err) {
		return v, err
	}
	return v, nil
}

// MaintenanceEvent is maintenance.request.created.v1 / maintenance.request.updated.v1.
// It carries what a provider needs to discover and execute the work, never
// the owner's budget or contact details.
type MaintenanceEvent struct {
	SchemaVersion       int       `json:"schema_version"`
	RequestID           ids.ID    `json:"request_id"`
	PropertyID          ids.ID    `json:"property_id"`
	UnitID              *ids.ID   `json:"unit_id"`
	Category            string    `json:"category"`
	Priority            string    `json:"priority"`
	Title               string    `json:"title"`
	Description         string    `json:"description"`
	Status              string    `json:"status"`
	Sourcing            *string   `json:"sourcing"`
	City                string    `json:"city"`
	StateRegion         string    `json:"state_region"`
	CountryCode         string    `json:"country_code"`
	JobID               *ids.ID   `json:"job_id"`
	SelectedQuoteID     *ids.ID   `json:"selected_quote_id"`
	ProviderRef         *ids.ID   `json:"provider_ref" doc:"Assigned provider (warranty claims are routed to the original provider)"`
	WarrantyOfRequestID *ids.ID   `json:"warranty_of_request_id"`
	RequestVersion      int64     `json:"request_version"`
	OccurredAt          time.Time `json:"occurred_at"`
}

func (s *Service) emitMaintenance(ctx context.Context, eventType string, id ids.ID) error {
	tx := mustTx(ctx)
	m, err := s.q.GetMaintenanceRequest(ctx, tx, id)
	if err != nil {
		return err
	}
	addr, err := s.q.GetPropertyAddress(ctx, tx, m.PropertyID)
	if err != nil && !isNoRows(err) {
		return err
	}
	e := MaintenanceEvent{SchemaVersion: 1, RequestID: m.ID, PropertyID: m.PropertyID, UnitID: m.UnitID, Category: m.Category, Priority: m.Priority,
		Title: m.Title, Description: m.Description, Status: m.Status, Sourcing: m.Sourcing, City: addr.City, StateRegion: addr.StateRegion,
		CountryCode: addr.CountryCode, WarrantyOfRequestID: m.WarrantyOfRequestID, RequestVersion: m.Version, OccurredAt: s.now()}
	if j, err := s.q.GetActiveJob(ctx, tx, id); err == nil {
		e.JobID, e.SelectedQuoteID, e.ProviderRef = &j.ID, &j.QuoteID, j.ProviderRef
	} else if !isNoRows(err) {
		return err
	}
	if m.WarrantyOfRequestID != nil && e.ProviderRef == nil {
		if j, err := s.q.GetActiveJob(ctx, tx, *m.WarrantyOfRequestID); err == nil {
			e.ProviderRef = j.ProviderRef
		}
	}
	return s.publish(ctx, eventType, AggMaintenance, id, e)
}

// maintenanceTo transitions a request; platform-sourced requests also
// publish the change for artisan-service.
func (s *Service) maintenanceTo(ctx context.Context, id ids.ID, to, cmd, reason string) error {
	if _, _, err := s.transition(ctx, domain.Maintenance, id, lifecycle.Change{To: to, Command: cmd, Reason: reason}); err != nil {
		return err
	}
	m, err := s.q.GetMaintenanceRequest(ctx, mustTx(ctx), id)
	if err != nil {
		return err
	}
	if deref(m.Sourcing) == "PLATFORM" {
		return s.emitMaintenance(ctx, "maintenance.request.updated.v1", id)
	}
	return nil
}

func (s *Service) maintenanceFor(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action) (db.MaintenanceRequest, authz.Decision, error) {
	m, err := s.q.GetMaintenanceRequest(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return m, authz.Decision{}, notFound(AggMaintenance)
	}
	if err != nil {
		return m, authz.Decision{}, err
	}
	d, err := s.authorize(ctx, subj, action, m.PropertyID)
	if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
		err = notFound(AggMaintenance)
	}
	if err == nil && !isReadAction(action) {
		m, err = s.q.GetMaintenanceRequestForUpdate(ctx, mustTx(ctx), id)
	}
	return m, d, err
}

func reporterOf(d authz.Decision, subj authz.Subject) (string, *ids.ID) {
	if d.Via != nil && d.Via.Role == authz.RoleManager {
		return "MANAGER", &d.Via.ReferenceID
	}
	if subj.Account != nil {
		return "OWNER", &subj.Account.AccountID
	}
	return "SYSTEM", nil
}

// CreateMaintenance records a maintenance request (OPEN).
func (s *Service) CreateMaintenance(ctx context.Context, subj authz.Subject, in MaintenanceInput, ev Evidence) (MaintenanceView, error) {
	if err := schema.Validate(in); err != nil {
		return MaintenanceView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return MaintenanceView{}, err
	}
	var out MaintenanceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		d, err := s.authorize(ctx, subj, authz.Do(authz.CapMaintenanceReq), in.PropertyID)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		if in.UnitID != nil {
			if _, err := s.q.GetUnit(ctx, tx, db.GetUnitParams{ID: *in.UnitID, PropertyID: in.PropertyID}); isNoRows(err) {
				return apperrors.InvalidRequest("unit_id does not belong to the property.")
			} else if err != nil {
				return err
			}
		}
		if in.AssetID != nil {
			if _, err := s.q.GetAsset(ctx, tx, db.GetAssetParams{ID: *in.AssetID, PropertyID: in.PropertyID}); isNoRows(err) {
				return apperrors.InvalidRequest("asset_id does not belong to the property.")
			} else if err != nil {
				return err
			}
		}
		if in.TenancyID != nil {
			t, err := s.q.GetTenancy(ctx, tx, *in.TenancyID)
			if isNoRows(err) || (err == nil && t.PropertyID != in.PropertyID) {
				return apperrors.InvalidRequest("tenancy_id does not belong to the property.")
			} else if err != nil {
				return err
			}
		}
		cur := in.Currency
		if cur == "" {
			cur = s.Policy.Domain.DefaultCurrency
		}
		by, ref := reporterOf(d, subj)
		m, err := s.q.InsertMaintenanceRequest(ctx, tx, db.InsertMaintenanceRequestParams{ID: ids.New(), PropertyID: in.PropertyID, UnitID: in.UnitID,
			AssetID: in.AssetID, TenancyID: in.TenancyID, Title: in.Title, Description: in.Description, Category: in.Category, Priority: in.Priority,
			Responsibility: "PENDING_REVIEW", ReportedByType: by, ReportedByRef: ref, EvidenceMediaIds: nonNilIDs(in.EvidenceMediaIDs),
			BudgetMinor: in.BudgetMinor, Currency: cur, CfmeetContextID: in.CFMeetContextID, CreatedAt: now})
		if err != nil {
			return err
		}
		if err := domain.Maintenance.RecordCreation(ctx, tx, m.ID, &m.PropertyID, "OPEN", now); err != nil {
			return err
		}
		if err := s.propertyHistory(ctx, m.PropertyID, "MAINTENANCE_REPORTED", m.Title, AggMaintenance, m.ID); err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, m.PropertyID, "MAINTENANCE_REPORTED", AggMaintenance, m.ID, m.Title); err != nil {
			return err
		}
		if err := s.record(ctx, "maintenance.request.create", AggMaintenance, m.ID, map[string]any{"priority": in.Priority, "category": in.Category}, nil); err != nil {
			return err
		}
		out, err = s.maintenanceView(ctx, m)
		return err
	})
	return out, err
}

// GetMaintenance returns a request with its quotes, approvals, job and warranty.
func (s *Service) GetMaintenance(ctx context.Context, subj authz.Subject, id ids.ID) (MaintenanceView, error) {
	m, _, err := s.maintenanceFor(ctx, subj, id, authz.Read(authz.CapMaintenanceReq))
	if err != nil {
		return MaintenanceView{}, err
	}
	return s.maintenanceView(ctx, m)
}

// ListMaintenance pages requests across the caller's properties.
func (s *Service) ListMaintenance(ctx context.Context, subj authz.Subject, propertyID ids.ID, status, cursor string, limit int) (pagination.Response[MaintenanceView], error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return pagination.Response[MaintenanceView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[MaintenanceView]{}, err
	}
	rows, err := s.q.ListMaintenanceForAccount(ctx, s.dbq(ctx), db.ListMaintenanceForAccountParams{AccountID: accountOf(subj), Now: s.now(),
		PropertyID: idp(propertyID), Status: strp(status), CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[MaintenanceView]{}, err
	}
	out := make([]MaintenanceView, 0, len(rows))
	for _, r := range rows {
		out = append(out, maintenanceBase(r))
	}
	return pagination.Build(out, limit, func(v MaintenanceView) any { return timeCursor{At: v.CreatedAt, ID: v.ID} })
}

// MaintenanceTriage sets priority, responsibility and budget (PATCH, If-Match).
type MaintenanceTriage struct {
	Priority           string  `json:"priority" v:"required,enum=LOW|NORMAL|HIGH|EMERGENCY"`
	Responsibility     string  `json:"responsibility" v:"required,enum=PENDING_REVIEW|OWNER|TENANT|SHARED|WARRANTY"`
	ResponsibilityNote *string `json:"responsibility_note,omitempty" v:"max=1000"`
	BudgetMinor        *int64  `json:"budget_minor,omitempty" v:"min=1"`
	CFMeetContextID    *string `json:"cfmeet_context_id,omitempty" v:"max=128"`
}

// TriageMaintenance updates triage fields; a decided responsibility moves
// an OPEN request to TRIAGED.
func (s *Service) TriageMaintenance(ctx context.Context, subj authz.Subject, id ids.ID, expected int64, in MaintenanceTriage) (MaintenanceView, error) {
	if err := schema.Validate(in); err != nil {
		return MaintenanceView{}, err
	}
	var out MaintenanceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		m, d, err := s.maintenanceFor(ctx, subj, id, authz.Do(authz.CapMaintenanceReq))
		if err != nil {
			return err
		}
		if err := checkVersion(expected, m.Version); err != nil {
			return err
		}
		if !slices.Contains([]string{"OPEN", "TRIAGED", "QUOTING", "APPROVAL_PENDING"}, m.Status) {
			return conflict("MAINTENANCE_LOCKED", "Triage cannot change once work is approved.")
		}
		if m.Status != "OPEN" && in.Responsibility == "PENDING_REVIEW" {
			return apperrors.InvalidRequest("Responsibility was already decided.")
		}
		tx := mustTx(ctx)
		if err := s.q.UpdateMaintenanceTriage(ctx, tx, db.UpdateMaintenanceTriageParams{ID: id, Priority: in.Priority, Responsibility: in.Responsibility,
			ResponsibilityNote: in.ResponsibilityNote, BudgetMinor: in.BudgetMinor, CfmeetContextID: in.CFMeetContextID, Now: s.now()}); err != nil {
			return err
		}
		if m.Status == "OPEN" && in.Responsibility != "PENDING_REVIEW" {
			if err := s.maintenanceTo(ctx, id, "TRIAGED", "triage", ""); err != nil {
				return err
			}
		}
		if err := s.noteDelegateActivity(ctx, d, m.PropertyID, "MAINTENANCE_TRIAGED", AggMaintenance, id, m.Title); err != nil {
			return err
		}
		if err := s.record(ctx, "maintenance.request.triage", AggMaintenance, id, map[string]any{"responsibility": in.Responsibility, "priority": in.Priority,
			"budget_minor": in.BudgetMinor}, nil); err != nil {
			return err
		}
		n, err := s.q.GetMaintenanceRequest(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.maintenanceView(ctx, n)
		return err
	})
	return out, err
}

// QuoteRequestInput opens quoting.
type QuoteRequestInput struct {
	Sourcing string `json:"sourcing" v:"required,enum=PLATFORM|SELF_SOURCED" doc:"PLATFORM: artisans discover the request through artisan-service; SELF_SOURCED: the owner records quotes from their own providers"`
}

// RequestQuotes moves a triaged request to QUOTING. Platform-sourced requests
// are published to artisan-service (maintenance.request.created.v1).
func (s *Service) RequestQuotes(ctx context.Context, subj authz.Subject, id ids.ID, in QuoteRequestInput) (MaintenanceView, error) {
	if err := schema.Validate(in); err != nil {
		return MaintenanceView{}, err
	}
	var out MaintenanceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		m, _, err := s.maintenanceFor(ctx, subj, id, authz.Do(authz.CapMaintenanceReq))
		if err != nil {
			return err
		}
		if m.Status != "TRIAGED" {
			return apperrors.InvalidStateTransition(m.Status, "QUOTING")
		}
		if !slices.Contains([]string{"OWNER", "SHARED", "WARRANTY"}, m.Responsibility) {
			return conflict("NOT_OWNER_RESPONSIBILITY", "Quotes are requested for work the owner is responsible for.")
		}
		tx := mustTx(ctx)
		if err := s.q.SetMaintenanceSourcing(ctx, tx, db.SetMaintenanceSourcingParams{ID: id, Sourcing: &in.Sourcing, UpdatedAt: s.now()}); err != nil {
			return err
		}
		if _, _, err := s.transition(ctx, domain.Maintenance, id, lifecycle.Change{To: "QUOTING", Command: "request_quotes"}); err != nil {
			return err
		}
		if in.Sourcing == "PLATFORM" {
			if err := s.emitMaintenance(ctx, "maintenance.request.created.v1", id); err != nil {
				return err
			}
		}
		if err := s.record(ctx, "maintenance.request.quotes", AggMaintenance, id, map[string]any{"sourcing": in.Sourcing}, nil); err != nil {
			return err
		}
		n, err := s.q.GetMaintenanceRequest(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.maintenanceView(ctx, n)
		return err
	})
	return out, err
}

// OwnerQuoteInput records a quote from the owner's own provider.
type OwnerQuoteInput struct {
	ProviderDisplayName string     `json:"provider_display_name" v:"required,max=120"`
	Scope               string     `json:"scope" v:"required,max=4000"`
	AmountMinor         int64      `json:"amount_minor" v:"required,min=1"`
	ValidUntil          *time.Time `json:"valid_until,omitempty"`
	WarrantyDays        int32      `json:"warranty_days,omitempty" v:"min=0,max=3650"`
	EvidenceMediaIDs    []ids.ID   `json:"evidence_media_ids" v:"required,max=10" doc:"The provider's written quote"`
}

// MediaRefs lists referenced media.
func (in OwnerQuoteInput) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// ProviderQuoteInput is an artisan's quote through artisan-service.
type ProviderQuoteInput struct {
	ExternalQuoteRef    string     `json:"external_quote_ref" v:"required,max=128" doc:"artisan-service quote id (idempotency)"`
	ProviderDisplayName string     `json:"provider_display_name" v:"required,max=120"`
	Scope               string     `json:"scope" v:"required,max=4000"`
	AmountMinor         int64      `json:"amount_minor" v:"required,min=1"`
	Currency            string     `json:"currency" v:"required,pattern=[A-Z]{3}"`
	EarliestStartAt     *time.Time `json:"earliest_start_at,omitempty"`
	ValidUntil          *time.Time `json:"valid_until,omitempty"`
	WarrantyDays        int32      `json:"warranty_days,omitempty" v:"min=0,max=3650"`
	EvidenceMediaIDs    []ids.ID   `json:"evidence_media_ids,omitempty" v:"max=10"`
}

// MediaRefs lists referenced media.
func (in ProviderQuoteInput) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

func (s *Service) addQuote(ctx context.Context, m db.MaintenanceRequest, p db.InsertMaintenanceQuoteParams) error {
	if p.Currency != m.Currency {
		return precondition("CURRENCY_MISMATCH", "Quotes must be in "+m.Currency+".")
	}
	p.ID, p.RequestID, p.CreatedAt = ids.New(), m.ID, s.now()
	if p.EvidenceMediaIds == nil {
		p.EvidenceMediaIds = []ids.ID{}
	}
	if _, err := s.q.InsertMaintenanceQuote(ctx, mustTx(ctx), p); err != nil {
		return err
	}
	if m.Status == "QUOTING" {
		return s.maintenanceTo(ctx, m.ID, "APPROVAL_PENDING", "quote_received", "")
	}
	return nil
}

// RecordOwnerQuote adds a self-sourced quote (with the written quote).
func (s *Service) RecordOwnerQuote(ctx context.Context, subj authz.Subject, id ids.ID, in OwnerQuoteInput, ev Evidence) (MaintenanceView, error) {
	if err := schema.Validate(in); err != nil {
		return MaintenanceView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return MaintenanceView{}, err
	}
	var out MaintenanceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		m, _, err := s.maintenanceFor(ctx, subj, id, authz.Do(authz.CapMaintenanceReq))
		if err != nil {
			return err
		}
		if deref(m.Sourcing) != "SELF_SOURCED" || !slices.Contains([]string{"QUOTING", "APPROVAL_PENDING"}, m.Status) {
			return conflict("QUOTES_NOT_OPEN", "Self-sourced quotes are recorded while the request is quoting.")
		}
		valid := in.ValidUntil
		if valid == nil {
			valid = ptr(s.now().Add(s.Policy.Domain.MaintenanceQuoteTTL))
		}
		if err := s.addQuote(ctx, m, db.InsertMaintenanceQuoteParams{Source: "OWNER_RECORDED", ProviderDisplayName: in.ProviderDisplayName, Scope: in.Scope,
			AmountMinor: in.AmountMinor, Currency: m.Currency, ValidUntil: valid, WarrantyDays: in.WarrantyDays, EvidenceMediaIds: in.EvidenceMediaIDs}); err != nil {
			return err
		}
		if err := s.record(ctx, "maintenance.quote.record", AggMaintenance, id, map[string]any{"amount_minor": in.AmountMinor}, nil); err != nil {
			return err
		}
		n, err := s.q.GetMaintenanceRequest(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.maintenanceView(ctx, n)
		return err
	})
	return out, err
}

// requireProvider authorizes a provider callback and returns the provider id.
func (s *Service) requireProvider(ctx context.Context, subj authz.Subject, d platform.Domain) (ids.ID, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActInternalProviderUpdate, authz.Resource{}); err != nil {
		return ids.Nil, err
	}
	if subj.ActorDomain != d {
		return ids.Nil, apperrors.PermissionDenied("A " + string(d) + " provider actor is required.")
	}
	return subj.ActorID, nil
}

// ProviderMaintenanceView is the provider-facing slice of a request.
type ProviderMaintenanceView struct {
	RequestID   ids.ID   `json:"request_id"`
	Status      string   `json:"status"`
	QuoteID     *ids.ID  `json:"quote_id"`
	QuoteStatus *string  `json:"quote_status"`
	JobID       *ids.ID  `json:"job_id"`
	JobStatus   *string  `json:"job_status"`
	AmountMinor *int64   `json:"approved_amount_minor"`
	Currency    string   `json:"currency"`
	ReworkCount int32    `json:"rework_count"`
	Events      []string `json:"recorded_event_types"`
}

func (s *Service) providerView(ctx context.Context, requestID, provider ids.ID) (ProviderMaintenanceView, error) {
	q := s.dbq(ctx)
	m, err := s.q.GetMaintenanceRequest(ctx, q, requestID)
	if err != nil {
		return ProviderMaintenanceView{}, err
	}
	v := ProviderMaintenanceView{RequestID: m.ID, Status: m.Status, Currency: m.Currency, Events: []string{}}
	quotes, err := s.q.ListMaintenanceQuotes(ctx, q, m.ID)
	if err != nil {
		return v, err
	}
	for _, x := range quotes {
		if x.ProviderRef != nil && *x.ProviderRef == provider {
			v.QuoteID, v.QuoteStatus = &x.ID, &x.Status
		}
	}
	if j, err := s.q.GetActiveJob(ctx, q, m.ID); err == nil && j.ProviderRef != nil && *j.ProviderRef == provider {
		v.JobID, v.JobStatus, v.AmountMinor, v.ReworkCount = &j.ID, &j.Status, &j.AmountMinor, j.ReworkCount
		evs, err := s.q.ListJobEvents(ctx, q, j.ID)
		if err != nil {
			return v, err
		}
		for _, e := range evs {
			v.Events = append(v.Events, e.EventType)
		}
	}
	return v, nil
}

// ProviderSubmitQuote records an artisan's quote on a platform-sourced
// request (idempotent per external_quote_ref).
func (s *Service) ProviderSubmitQuote(ctx context.Context, subj authz.Subject, id ids.ID, in ProviderQuoteInput, ev Evidence) (ProviderMaintenanceView, error) {
	if err := schema.Validate(in); err != nil {
		return ProviderMaintenanceView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return ProviderMaintenanceView{}, err
	}
	var provider ids.ID
	err := s.InTx(ctx, func(ctx context.Context) error {
		var err error
		if provider, err = s.requireProvider(ctx, subj, platform.DomainArtisan); err != nil {
			return err
		}
		tx := mustTx(ctx)
		m, err := s.q.GetMaintenanceRequestForUpdate(ctx, tx, id)
		if isNoRows(err) || (err == nil && deref(m.Sourcing) != "PLATFORM") {
			return notFound(AggMaintenance)
		}
		if err != nil {
			return err
		}
		if q, err := s.q.GetMaintenanceQuoteByExternal(ctx, tx, &in.ExternalQuoteRef); err == nil {
			if q.RequestID != id || q.ProviderRef == nil || *q.ProviderRef != provider {
				return conflict("EXTERNAL_REF_IN_USE", "The quote reference belongs to another quote.")
			}
			return nil // duplicate delivery
		} else if !isNoRows(err) {
			return err
		}
		if !slices.Contains([]string{"QUOTING", "APPROVAL_PENDING"}, m.Status) {
			return conflict("QUOTES_NOT_OPEN", "The request is not accepting quotes.")
		}
		if in.ValidUntil != nil && !in.ValidUntil.After(s.now()) {
			return apperrors.InvalidRequest("valid_until must be in the future.")
		}
		if err := s.addQuote(ctx, m, db.InsertMaintenanceQuoteParams{Source: "ARTISAN_SERVICE", ProviderRef: &provider, ProviderDisplayName: in.ProviderDisplayName,
			ExternalQuoteRef: &in.ExternalQuoteRef, Scope: in.Scope, AmountMinor: in.AmountMinor, Currency: in.Currency, EarliestStartAt: in.EarliestStartAt,
			ValidUntil: in.ValidUntil, WarrantyDays: in.WarrantyDays, EvidenceMediaIds: in.EvidenceMediaIDs}); err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, tx, m.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateMaintenanceUpdate, map[string]string{"request_title": m.Title, "status": "quote_received"}); err != nil {
			return err
		}
		return s.record(ctx, "maintenance.quote.provider", AggMaintenance, id, map[string]any{"amount_minor": in.AmountMinor, "provider_ref": provider}, nil)
	})
	if err != nil {
		return ProviderMaintenanceView{}, err
	}
	return s.providerView(ctx, id, provider)
}

// ApprovalInput approves one quote (or rejects all and re-opens quoting).
type ApprovalInput struct {
	Decision          string  `json:"decision" v:"required,enum=APPROVE|REJECT"`
	QuoteID           *ids.ID `json:"quote_id,omitempty"`
	PaymentCollection string  `json:"payment_collection,omitempty" v:"enum=PLATFORM|OFF_PLATFORM" doc:"PLATFORM needs payment-service; self-sourced work is OFF_PLATFORM"`
	ExceedBudget      bool    `json:"exceed_budget,omitempty" doc:"Owner/co-owner only: approve above the request budget"`
	Note              *string `json:"note,omitempty" v:"max=1000"`
}

// ApproveMaintenance records the approval decision. A delegated manager
// approves only within its current spend limit (emergency rule applied for
// EMERGENCY requests); above it the owner must approve.
func (s *Service) ApproveMaintenance(ctx context.Context, subj authz.Subject, id ids.ID, in ApprovalInput) (MaintenanceView, error) {
	if err := schema.Validate(in); err != nil {
		return MaintenanceView{}, err
	}
	if in.PaymentCollection == "PLATFORM" && s.Payment == nil {
		return MaintenanceView{}, unavailable("Platform payment of providers", "payment-service")
	}
	var out MaintenanceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		m, d, err := s.maintenanceFor(ctx, subj, id, authz.Do(authz.CapMaintenanceApprove))
		if err != nil {
			return err
		}
		if m.Status != "APPROVAL_PENDING" {
			return apperrors.InvalidStateTransition(m.Status, "APPROVED")
		}
		tx, now := mustTx(ctx), s.now()
		via, approver := "OWNER", accountOf(subj)
		var assignment *ids.ID
		if d.Via != nil {
			switch d.Via.Role {
			case authz.RoleManager:
				via, approver, assignment = "MANAGER", subj.ActorID, &d.Via.ReferenceID
			case authz.RoleCoOwner:
				via = "CO_OWNER"
			}
		}
		if in.Decision == "REJECT" {
			if err := s.q.RejectOtherQuotes(ctx, tx, db.RejectOtherQuotesParams{RequestID: id, ID: ids.Nil, UpdatedAt: now}); err != nil {
				return err
			}
			if err := s.q.InsertMaintenanceApproval(ctx, tx, db.InsertMaintenanceApprovalParams{ID: ids.New(), RequestID: id, Decision: "REJECTED",
				Currency: m.Currency, ApprovedVia: via, ApproverRef: approver, AssignmentID: assignment, Note: in.Note, CreatedAt: now}); err != nil {
				return err
			}
			if err := s.maintenanceTo(ctx, id, "QUOTING", "reject_quotes", ""); err != nil {
				return err
			}
		} else {
			if in.QuoteID == nil || in.PaymentCollection == "" {
				return apperrors.InvalidRequest("quote_id and payment_collection are required to approve.")
			}
			q, err := s.q.GetMaintenanceQuote(ctx, tx, db.GetMaintenanceQuoteParams{ID: *in.QuoteID, RequestID: id})
			if isNoRows(err) {
				return notFound("maintenance_quote")
			}
			if err != nil {
				return err
			}
			if q.Status != "SUBMITTED" || (q.ValidUntil != nil && !q.ValidUntil.After(now)) {
				return conflict("QUOTE_NOT_AVAILABLE", "The quote is "+lower(q.Status)+" or expired.")
			}
			if q.Source == "OWNER_RECORDED" && in.PaymentCollection == "PLATFORM" {
				return precondition("SELF_SOURCED_OFF_PLATFORM", "Self-sourced providers are paid off-platform.")
			}
			if m.BudgetMinor != nil && q.AmountMinor > *m.BudgetMinor && (via == "MANAGER" || !in.ExceedBudget) {
				return precondition("OVER_BUDGET", fmt.Sprintf("The quote exceeds the budget of %d; the owner can approve with exceed_budget.", *m.BudgetMinor))
			}
			var limit *int64
			emergency := m.Priority == "EMERGENCY"
			if via == "MANAGER" {
				perm, err := s.q.LatestManagerPermission(ctx, tx, *assignment)
				if err != nil {
					return err
				}
				l := perm.SpendLimitMinor
				if emergency {
					switch perm.EmergencyRule {
					case "OWNER_APPROVAL":
						return apperrors.New(http.StatusForbidden, "OWNER_APPROVAL_REQUIRED", "Emergency work needs the owner's approval under this mandate.")
					case "ACT_WITHIN_LIMIT":
						l = max(l, perm.EmergencyLimitMinor)
					}
				}
				if perm.Currency != q.Currency || q.AmountMinor > l {
					return apperrors.New(http.StatusForbidden, "APPROVAL_LIMIT_EXCEEDED", "The quote exceeds the manager's approval limit; the owner must approve.")
				}
				limit = &l
			}
			if err := s.q.InsertMaintenanceApproval(ctx, tx, db.InsertMaintenanceApprovalParams{ID: ids.New(), RequestID: id, QuoteID: &q.ID, Decision: "APPROVED",
				AmountMinor: &q.AmountMinor, Currency: q.Currency, ApprovedVia: via, ApproverRef: approver, AssignmentID: assignment, LimitMinor: limit,
				Emergency: emergency, Note: in.Note, CreatedAt: now}); err != nil {
				return err
			}
			if err := s.q.SetQuoteStatus(ctx, tx, db.SetQuoteStatusParams{ID: q.ID, Status: "SELECTED", UpdatedAt: now}); err != nil {
				return err
			}
			if err := s.q.RejectOtherQuotes(ctx, tx, db.RejectOtherQuotesParams{RequestID: id, ID: q.ID, UpdatedAt: now}); err != nil {
				return err
			}
			if _, _, err := s.transition(ctx, domain.Maintenance, id, lifecycle.Change{To: "APPROVED", Command: "approve"}); err != nil {
				return err
			}
			if _, err := s.q.InsertMaintenanceJob(ctx, tx, db.InsertMaintenanceJobParams{ID: ids.New(), RequestID: id, QuoteID: q.ID, ProviderRef: q.ProviderRef,
				AmountMinor: q.AmountMinor, Currency: q.Currency, PaymentCollection: in.PaymentCollection, CreatedAt: now}); err != nil {
				return err
			}
			if err := s.maintenanceTo(ctx, id, "ASSIGNED", "assign", ""); err != nil {
				return err
			}
			if via == "MANAGER" {
				p, err := s.q.GetProperty(ctx, tx, m.PropertyID)
				if err != nil {
					return err
				}
				if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateMaintenanceUpdate, map[string]string{"request_title": m.Title, "status": "approved_by_manager"}); err != nil {
					return err
				}
			}
		}
		if err := s.noteDelegateActivity(ctx, d, m.PropertyID, "MAINTENANCE_"+in.Decision, AggMaintenance, id, m.Title); err != nil {
			return err
		}
		if err := s.record(ctx, "maintenance.request."+lower(in.Decision), AggMaintenance, id, map[string]any{"quote_id": in.QuoteID, "via": via,
			"payment_collection": in.PaymentCollection}, nil); err != nil {
			return err
		}
		n, err := s.q.GetMaintenanceRequest(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.maintenanceView(ctx, n)
		return err
	})
	return out, err
}

// JobEventInput is a job record from the provider (artisan-service) or, for
// self-sourced work, the owner.
type JobEventInput struct {
	EventID          string     `json:"event_id" v:"required,max=128" doc:"Idempotency id of this record"`
	EventType        string     `json:"event_type" v:"required,enum=ATTENDED|PROGRESS|COMPLETION_SUBMITTED|INVOICE|PAYMENT_ACKNOWLEDGED|WITHDRAWN"`
	Note             *string    `json:"note,omitempty" v:"max=2000"`
	EvidenceMediaIDs []ids.ID   `json:"evidence_media_ids,omitempty" v:"max=20"`
	AmountMinor      *int64     `json:"amount_minor,omitempty" v:"min=1" doc:"INVOICE amount"`
	OccurredAt       *time.Time `json:"occurred_at,omitempty"`
	ExternalJobRef   *string    `json:"external_job_ref,omitempty" v:"max=128"`
}

// MediaRefs lists referenced media.
func (in JobEventInput) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// applyJobEvent appends a job record and advances the job/request. Records
// arriving out of order are kept but never move state backwards.
func (s *Service) applyJobEvent(ctx context.Context, m db.MaintenanceRequest, j db.MaintenanceJob, source string, in JobEventInput) error {
	tx, now := mustTx(ctx), s.now()
	at := now
	if in.OccurredAt != nil && in.OccurredAt.Before(now) {
		at = *in.OccurredAt
	}
	if in.EventType == "COMPLETION_SUBMITTED" && len(in.EvidenceMediaIDs) == 0 {
		return precondition("EVIDENCE_REQUIRED", "Completion needs photo evidence.")
	}
	if in.EventType == "INVOICE" && in.AmountMinor == nil {
		return apperrors.InvalidRequest("amount_minor is required for an invoice.")
	}
	if in.EventType == "WITHDRAWN" && slices.Contains([]string{"COMPLETION_SUBMITTED", "ACCEPTED"}, j.Status) {
		return conflict("JOB_COMPLETED", "Completed work cannot be withdrawn.")
	}
	stored := in.EventType
	if stored == "WITHDRAWN" {
		stored = "CANCELLED"
	}
	ev, err := s.q.InsertJobEvent(ctx, tx, db.InsertJobEventParams{ID: ids.New(), JobID: j.ID, EventType: stored, Source: source, ExternalEventID: &in.EventID,
		Note: in.Note, EvidenceMediaIds: nonNilIDs(in.EvidenceMediaIDs), AmountMinor: in.AmountMinor, OccurredAt: at, RecordedAt: now})
	if isNoRows(err) {
		return nil // duplicate delivery
	}
	if err != nil {
		return err
	}
	upd := db.UpdateMaintenanceJobParams{ID: j.ID, Status: j.Status, ExternalJobRef: in.ExternalJobRef, Now: now}
	var to string
	switch in.EventType {
	case "ATTENDED", "PROGRESS":
		if j.Status == "ASSIGNED" || j.Status == "REWORK_REQUESTED" {
			upd.Status, to = "IN_PROGRESS", "IN_PROGRESS"
		}
	case "COMPLETION_SUBMITTED":
		if slices.Contains([]string{"ASSIGNED", "IN_PROGRESS", "REWORK_REQUESTED"}, j.Status) {
			if m.Status == "ASSIGNED" || m.Status == "REWORK" {
				if err := s.maintenanceTo(ctx, m.ID, "IN_PROGRESS", "provider_progress", ""); err != nil {
					return err
				}
			}
			upd.Status, to = "COMPLETION_SUBMITTED", "COMPLETION_REVIEW"
		}
	case "INVOICE":
		upd.InvoicedMinor = in.AmountMinor
		if *in.AmountMinor > j.AmountMinor {
			if err := s.record(ctx, "maintenance.job.invoice_above_approval", AggMaintenance, m.ID, map[string]any{"invoiced_minor": *in.AmountMinor,
				"approved_minor": j.AmountMinor}, map[string]any{"policy": "payment is capped at the approved amount"}); err != nil {
				return err
			}
		}
	case "PAYMENT_ACKNOWLEDGED":
		upd.PaymentAck = true
	case "WITHDRAWN":
		upd.Status, to = "CANCELLED", "CANCELLED"
		if err := s.q.SetMaintenanceCancelReason(ctx, tx, db.SetMaintenanceCancelReasonParams{ID: m.ID, CancelReason: ptr("PROVIDER_WITHDREW"), UpdatedAt: now}); err != nil {
			return err
		}
	}
	if err := s.q.UpdateMaintenanceJob(ctx, tx, upd); err != nil {
		return err
	}
	if upd.PaymentAck && len(j.OfflinePaymentEvidence) > 0 {
		if err := s.q.SetJobOfflinePayment(ctx, tx, db.SetJobOfflinePaymentParams{ID: j.ID, OfflinePaymentEvidence: j.OfflinePaymentEvidence, PaidMinor: j.PaidMinor,
			LastPaymentStatus: ptr("OFF_PLATFORM_CONFIRMED"), UpdatedAt: now}); err != nil {
			return err
		}
	}
	if to != "" && to != m.Status {
		if err := s.maintenanceTo(ctx, m.ID, to, "job_"+lower(in.EventType), ""); err != nil {
			return err
		}
	}
	if in.EventType == "COMPLETION_SUBMITTED" {
		p, err := s.q.GetProperty(ctx, tx, m.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateMaintenanceUpdate, map[string]string{"request_title": m.Title, "status": "completion_review"}); err != nil {
			return err
		}
	}
	return s.record(ctx, "maintenance.job."+lower(stored), AggMaintenance, m.ID, map[string]any{"job_event_id": ev.ID, "source": source}, nil)
}

// ProviderJobEvent records attendance/progress/completion/invoice/payment
// acknowledgement/withdrawal from the assigned artisan.
func (s *Service) ProviderJobEvent(ctx context.Context, subj authz.Subject, jobID ids.ID, in JobEventInput, ev Evidence) (ProviderMaintenanceView, error) {
	if err := schema.Validate(in); err != nil {
		return ProviderMaintenanceView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return ProviderMaintenanceView{}, err
	}
	var provider, requestID ids.ID
	err := s.InTx(ctx, func(ctx context.Context) error {
		var err error
		if provider, err = s.requireProvider(ctx, subj, platform.DomainArtisan); err != nil {
			return err
		}
		tx := mustTx(ctx)
		j, err := s.q.GetMaintenanceJobForUpdate(ctx, tx, jobID)
		if isNoRows(err) || (err == nil && (j.ProviderRef == nil || *j.ProviderRef != provider)) {
			return notFound("maintenance_job")
		}
		if err != nil {
			return err
		}
		requestID = j.RequestID
		if j.Status == "CANCELLED" {
			return conflict("JOB_CANCELLED", "The job was cancelled.")
		}
		m, err := s.q.GetMaintenanceRequestForUpdate(ctx, tx, j.RequestID)
		if err != nil {
			return err
		}
		return s.applyJobEvent(ctx, m, j, "ARTISAN_SERVICE", in)
	})
	if err != nil {
		return ProviderMaintenanceView{}, err
	}
	return s.providerView(ctx, requestID, provider)
}

// RecordOwnerJobEvent records job progress of self-sourced work.
func (s *Service) RecordOwnerJobEvent(ctx context.Context, subj authz.Subject, id ids.ID, in JobEventInput, ev Evidence) (MaintenanceView, error) {
	if err := schema.Validate(in); err != nil {
		return MaintenanceView{}, err
	}
	if in.EventType == "PAYMENT_ACKNOWLEDGED" {
		return MaintenanceView{}, apperrors.InvalidRequest("Only the provider acknowledges payment.")
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return MaintenanceView{}, err
	}
	var out MaintenanceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		m, d, err := s.maintenanceFor(ctx, subj, id, authz.Do(authz.CapMaintenanceReq))
		if err != nil {
			return err
		}
		tx := mustTx(ctx)
		j, err := s.q.GetActiveJob(ctx, tx, id)
		if isNoRows(err) {
			return conflict("NO_ACTIVE_JOB", "The request has no active job.")
		}
		if err != nil {
			return err
		}
		if deref(m.Sourcing) != "SELF_SOURCED" {
			return conflict("PROVIDER_REPORTS_PROGRESS", "Platform providers report job progress themselves.")
		}
		source, _ := reporterOf(d, subj)
		if err := s.applyJobEvent(ctx, m, j, source, in); err != nil {
			return err
		}
		n, err := s.q.GetMaintenanceRequest(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.maintenanceView(ctx, n)
		return err
	})
	return out, err
}

// CompletionReview is the owner's decision on submitted work.
type CompletionReview struct {
	Decision         string   `json:"decision" v:"required,enum=ACCEPT|REWORK|DISPUTE"`
	Note             *string  `json:"note,omitempty" v:"max=2000"`
	EvidenceMediaIDs []ids.ID `json:"evidence_media_ids,omitempty" v:"max=20"`
}

// MediaRefs lists referenced media.
func (in CompletionReview) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// ReviewCompletion accepts the work (warranty starts, platform payment is
// requested), asks for rework, or disputes it. Evidence is appended.
func (s *Service) ReviewCompletion(ctx context.Context, subj authz.Subject, id ids.ID, in CompletionReview, ev Evidence) (MaintenanceView, error) {
	if err := schema.Validate(in); err != nil {
		return MaintenanceView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return MaintenanceView{}, err
	}
	var out MaintenanceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		m, d, err := s.maintenanceFor(ctx, subj, id, authz.Do(authz.CapMaintenanceApprove))
		if err != nil {
			return err
		}
		if m.Status != "COMPLETION_REVIEW" && !(in.Decision == "DISPUTE" && m.Status == "COMPLETED") {
			return apperrors.InvalidStateTransition(m.Status, in.Decision)
		}
		if in.Decision != "ACCEPT" && (in.Note == nil || *in.Note == "") {
			return apperrors.InvalidRequest("Explain what is wrong in note.")
		}
		tx, now := mustTx(ctx), s.now()
		j, err := s.q.GetActiveJob(ctx, tx, id)
		if err != nil {
			return err
		}
		source, _ := reporterOf(d, subj)
		appendEvent := func(t string) error {
			_, err := s.q.InsertJobEvent(ctx, tx, db.InsertJobEventParams{ID: ids.New(), JobID: j.ID, EventType: t, Source: source, Note: in.Note,
				EvidenceMediaIds: nonNilIDs(in.EvidenceMediaIDs), OccurredAt: now, RecordedAt: now})
			return err
		}
		switch in.Decision {
		case "ACCEPT":
			if err := appendEvent("ACCEPTED"); err != nil {
				return err
			}
			if err := s.q.UpdateMaintenanceJob(ctx, tx, db.UpdateMaintenanceJobParams{ID: j.ID, Status: "ACCEPTED", AcceptedAt: &now, Now: now}); err != nil {
				return err
			}
			if err := s.maintenanceTo(ctx, id, "COMPLETED", "accept_completion", ""); err != nil {
				return err
			}
			q, err := s.q.GetMaintenanceQuote(ctx, tx, db.GetMaintenanceQuoteParams{ID: j.QuoteID, RequestID: id})
			if err != nil {
				return err
			}
			if q.WarrantyDays > 0 {
				start := dayOf(now)
				if err := s.q.InsertWarranty(ctx, tx, db.InsertWarrantyParams{ID: ids.New(), RequestID: id, JobID: j.ID, ProviderRef: j.ProviderRef,
					StartsOn: start, EndsOn: start.AddDate(0, 0, int(q.WarrantyDays)), CreatedAt: now}); err != nil {
					return err
				}
			}
			if m.AssetID != nil {
				if err := s.q.MarkAssetServiced(ctx, tx, db.MarkAssetServicedParams{ID: *m.AssetID, LastServicedOn: ptr(dayOf(now)), UpdatedAt: now}); err != nil {
					return err
				}
			}
			if err := s.propertyHistory(ctx, m.PropertyID, "MAINTENANCE_COMPLETED", m.Title, AggMaintenance, id); err != nil {
				return err
			}
		case "REWORK":
			if j.ReworkCount >= s.Policy.Domain.MaxReworkRounds {
				return precondition("REWORK_LIMIT_REACHED", "Further disagreement is handled as a dispute.")
			}
			if err := appendEvent("REWORK_REQUESTED"); err != nil {
				return err
			}
			if err := s.q.UpdateMaintenanceJob(ctx, tx, db.UpdateMaintenanceJobParams{ID: j.ID, Status: "REWORK_REQUESTED", ReworkInc: 1, Now: now}); err != nil {
				return err
			}
			if err := s.maintenanceTo(ctx, id, "REWORK", "request_rework", ""); err != nil {
				return err
			}
		case "DISPUTE":
			if err := s.maintenanceTo(ctx, id, "DISPUTED", "dispute_completion", ""); err != nil {
				return err
			}
		}
		if err := s.noteDelegateActivity(ctx, d, m.PropertyID, "MAINTENANCE_"+in.Decision, AggMaintenance, id, m.Title); err != nil {
			return err
		}
		if err := s.record(ctx, "maintenance.completion."+lower(in.Decision), AggMaintenance, id, map[string]any{"job_id": j.ID},
			map[string]any{"evidence_media_ids": in.EvidenceMediaIDs}); err != nil {
			return err
		}
		n, err := s.q.GetMaintenanceRequest(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.maintenanceView(ctx, n)
		return err
	})
	return out, err
}

// OfflinePaymentInput records an off-platform provider payment.
type OfflinePaymentInput struct {
	AmountMinor      int64    `json:"amount_minor" v:"required,min=1"`
	EvidenceMediaIDs []ids.ID `json:"evidence_media_ids" v:"required,max=10" doc:"Proof of payment"`
}

// MediaRefs lists referenced media.
func (in OfflinePaymentInput) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// RecordOfflinePayment records the owner's evidence of an off-platform
// payment. It is confirmed when the platform provider acknowledges it;
// self-sourced work has no platform counterparty and stays OWNER_ATTESTED.
func (s *Service) RecordOfflinePayment(ctx context.Context, subj authz.Subject, id ids.ID, in OfflinePaymentInput, ev Evidence) (MaintenanceView, error) {
	if err := schema.Validate(in); err != nil {
		return MaintenanceView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return MaintenanceView{}, err
	}
	var out MaintenanceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		m, _, err := s.maintenanceFor(ctx, subj, id, authz.Do(authz.CapMaintenanceApprove))
		if err != nil {
			return err
		}
		tx := mustTx(ctx)
		j, err := s.q.GetActiveJob(ctx, tx, id)
		if isNoRows(err) || (err == nil && (j.Status != "ACCEPTED" || j.PaymentCollection != "OFF_PLATFORM")) {
			return conflict("PAYMENT_NOT_RECORDABLE", "Off-platform payments are recorded for accepted, off-platform jobs.")
		}
		if err != nil {
			return err
		}
		if in.AmountMinor > j.AmountMinor {
			return precondition("ABOVE_APPROVED_AMOUNT", "Payment cannot exceed the approved amount.")
		}
		status := "AWAITING_PROVIDER_CONFIRMATION"
		switch {
		case deref(m.Sourcing) == "SELF_SOURCED":
			status = "OWNER_ATTESTED"
		case j.ProviderPaymentAck:
			status = "OFF_PLATFORM_CONFIRMED"
		}
		if err := s.q.SetJobOfflinePayment(ctx, tx, db.SetJobOfflinePaymentParams{ID: j.ID, OfflinePaymentEvidence: append(nonNilIDs(j.OfflinePaymentEvidence),
			in.EvidenceMediaIDs...), PaidMinor: in.AmountMinor, LastPaymentStatus: &status, UpdatedAt: s.now()}); err != nil {
			return err
		}
		if err := s.record(ctx, "maintenance.job.offline_payment", AggMaintenance, id, map[string]any{"amount_minor": in.AmountMinor, "status": status},
			map[string]any{"evidence_media_ids": in.EvidenceMediaIDs, "attested_by": "OWNER"}); err != nil {
			return err
		}
		n, err := s.q.GetMaintenanceRequest(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.maintenanceView(ctx, n)
		return err
	})
	return out, err
}

// MaintenanceCancel cancels a request before completion review.
type MaintenanceCancel struct {
	ReasonCode string `json:"reason_code" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}"`
}

// CancelMaintenance cancels the request and any active job.
func (s *Service) CancelMaintenance(ctx context.Context, subj authz.Subject, id ids.ID, in MaintenanceCancel) (MaintenanceView, error) {
	if err := schema.Validate(in); err != nil {
		return MaintenanceView{}, err
	}
	var out MaintenanceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		m, d, err := s.maintenanceFor(ctx, subj, id, authz.Do(authz.CapMaintenanceReq))
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		if j, err := s.q.GetActiveJob(ctx, tx, id); err == nil {
			if j.Status == "COMPLETION_SUBMITTED" || j.Status == "ACCEPTED" {
				return conflict("JOB_COMPLETED", "Submitted work is accepted, reworked or disputed, not cancelled.")
			}
			if err := s.q.UpdateMaintenanceJob(ctx, tx, db.UpdateMaintenanceJobParams{ID: j.ID, Status: "CANCELLED", Now: now}); err != nil {
				return err
			}
		} else if !isNoRows(err) {
			return err
		}
		if err := s.q.SetMaintenanceCancelReason(ctx, tx, db.SetMaintenanceCancelReasonParams{ID: id, CancelReason: &in.ReasonCode, UpdatedAt: now}); err != nil {
			return err
		}
		if err := s.maintenanceTo(ctx, id, "CANCELLED", "cancel", in.ReasonCode); err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, m.PropertyID, "MAINTENANCE_CANCELLED", AggMaintenance, id, m.Title); err != nil {
			return err
		}
		if err := s.record(ctx, "maintenance.request.cancel", AggMaintenance, id, map[string]any{"reason_code": in.ReasonCode}, nil); err != nil {
			return err
		}
		n, err := s.q.GetMaintenanceRequest(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.maintenanceView(ctx, n)
		return err
	})
	return out, err
}

// WarrantyClaimInput reports a defect under the job's warranty.
type WarrantyClaimInput struct {
	Description      string   `json:"description" v:"required,max=4000"`
	EvidenceMediaIDs []ids.ID `json:"evidence_media_ids" v:"required,max=20"`
}

// MediaRefs lists referenced media.
func (in WarrantyClaimInput) MediaRefs() []ids.ID { return in.EvidenceMediaIDs }

// ClaimWarranty opens a linked WARRANTY request routed to the original
// provider (platform-sourced) or tracked for the owner's own provider.
func (s *Service) ClaimWarranty(ctx context.Context, subj authz.Subject, id ids.ID, in WarrantyClaimInput, ev Evidence) (MaintenanceView, error) {
	if err := schema.Validate(in); err != nil {
		return MaintenanceView{}, err
	}
	if err := ev.require(in.MediaRefs()...); err != nil {
		return MaintenanceView{}, err
	}
	var out MaintenanceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		m, d, err := s.maintenanceFor(ctx, subj, id, authz.Do(authz.CapMaintenanceReq))
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		w, err := s.q.GetWarrantyForRequest(ctx, tx, id)
		if isNoRows(err) || (err == nil && (w.Status != "ACTIVE" || dayOf(now).After(w.EndsOn))) {
			return conflict("NO_ACTIVE_WARRANTY", "The work has no active warranty.")
		}
		if err != nil {
			return err
		}
		by, ref := reporterOf(d, subj)
		c, err := s.q.InsertMaintenanceRequest(ctx, tx, db.InsertMaintenanceRequestParams{ID: ids.New(), PropertyID: m.PropertyID, UnitID: m.UnitID,
			AssetID: m.AssetID, TenancyID: m.TenancyID, WarrantyOfRequestID: &m.ID, Title: truncate("Warranty: "+m.Title, 120), Description: in.Description,
			Category: m.Category, Priority: m.Priority, Responsibility: "WARRANTY", ReportedByType: by, ReportedByRef: ref,
			EvidenceMediaIds: in.EvidenceMediaIDs, Currency: m.Currency, CfmeetContextID: m.CfmeetContextID, CreatedAt: now})
		if err != nil {
			return err
		}
		if err := domain.Maintenance.RecordCreation(ctx, tx, c.ID, &c.PropertyID, "OPEN", now); err != nil {
			return err
		}
		if n, err := s.q.ClaimWarranty(ctx, tx, db.ClaimWarrantyParams{ID: w.ID, ClaimRequestID: &c.ID, UpdatedAt: now}); err != nil || n == 0 {
			if err == nil {
				err = conflict("NO_ACTIVE_WARRANTY", "The warranty was already claimed.")
			}
			return err
		}
		if err := s.q.SetMaintenanceSourcing(ctx, tx, db.SetMaintenanceSourcingParams{ID: c.ID, Sourcing: m.Sourcing, UpdatedAt: now}); err != nil {
			return err
		}
		if _, _, err := s.transition(ctx, domain.Maintenance, c.ID, lifecycle.Change{To: "TRIAGED", Command: "warranty_claim"}); err != nil {
			return err
		}
		if _, _, err := s.transition(ctx, domain.Maintenance, c.ID, lifecycle.Change{To: "QUOTING", Command: "warranty_claim"}); err != nil {
			return err
		}
		if deref(m.Sourcing) == "PLATFORM" {
			if err := s.emitMaintenance(ctx, "maintenance.request.created.v1", c.ID); err != nil {
				return err
			}
		}
		if err := s.record(ctx, "maintenance.warranty.claim", AggMaintenance, id, map[string]any{"claim_request_id": c.ID}, nil); err != nil {
			return err
		}
		n, err := s.q.GetMaintenanceRequest(ctx, tx, c.ID)
		if err != nil {
			return err
		}
		out, err = s.maintenanceView(ctx, n)
		return err
	})
	return out, err
}

// MaintenanceResolution is an operator's decision on a disputed request.
type MaintenanceResolution struct {
	Outcome string  `json:"outcome" v:"required,enum=REWORK|COMPLETED|CANCELLED"`
	Note    *string `json:"note,omitempty" v:"max=1000"`
}

// AdminResolveMaintenance decides a disputed completion (admin dispute contract).
func (s *Service) AdminResolveMaintenance(ctx context.Context, subj authz.Subject, id ids.ID, in MaintenanceResolution) (MaintenanceView, error) {
	if err := schema.Validate(in); err != nil {
		return MaintenanceView{}, err
	}
	var out MaintenanceView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminDispute, authz.Resource{}); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		m, err := s.q.GetMaintenanceRequestForUpdate(ctx, tx, id)
		if isNoRows(err) {
			return notFound(AggMaintenance)
		}
		if err != nil {
			return err
		}
		if m.Status != "DISPUTED" {
			return conflict("NOT_DISPUTED", "The request is not disputed.")
		}
		j, err := s.q.GetActiveJob(ctx, tx, id)
		if err != nil {
			return err
		}
		upd := db.UpdateMaintenanceJobParams{ID: j.ID, Status: j.Status, Now: now}
		switch in.Outcome {
		case "REWORK":
			upd.Status, upd.ReworkInc = "REWORK_REQUESTED", 1
		case "COMPLETED":
			upd.Status = "ACCEPTED"
			if j.AcceptedAt == nil {
				upd.AcceptedAt = &now
			}
		case "CANCELLED":
			if j.Status == "ACCEPTED" && j.PaidMinor > 0 {
				return conflict("JOB_PAID", "A paid job is not cancelled; refunds are handled by payment-service.")
			}
			upd.Status = "CANCELLED"
		}
		if err := s.q.UpdateMaintenanceJob(ctx, tx, upd); err != nil {
			return err
		}
		if err := s.maintenanceTo(ctx, id, in.Outcome, "admin_resolve", ""); err != nil {
			return err
		}
		if err := s.record(ctx, "maintenance.dispute.resolve", AggMaintenance, id, map[string]any{"outcome": in.Outcome, "note": in.Note}, nil); err != nil {
			return err
		}
		n, err := s.q.GetMaintenanceRequest(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.maintenanceView(ctx, n)
		return err
	})
	return out, err
}

// ---- preventive schedules ----

// PreventiveInput defines a recurring asset/property task.
type PreventiveInput struct {
	AssetID      *ids.ID `json:"asset_id,omitempty"`
	Title        string  `json:"title" v:"required,max=120"`
	Category     string  `json:"category" v:"required,pattern=[A-Z][A-Z0-9_]{1,39}"`
	IntervalDays int32   `json:"interval_days" v:"required,min=1,max=3650"`
	LeadDays     int32   `json:"lead_days,omitempty" v:"min=0,max=90" doc:"Create the task this many days before it is due (default 7)"`
	FirstDueOn   string  `json:"first_due_on" v:"required,format=date"`
	Priority     string  `json:"priority,omitempty" v:"enum=LOW|NORMAL|HIGH"`
}

// PreventiveView is a preventive schedule.
type PreventiveView struct {
	ID              ids.ID  `json:"id"`
	PropertyID      ids.ID  `json:"property_id"`
	AssetID         *ids.ID `json:"asset_id"`
	Title           string  `json:"title"`
	Category        string  `json:"category"`
	IntervalDays    int32   `json:"interval_days"`
	LeadDays        int32   `json:"lead_days"`
	NextDueOn       string  `json:"next_due_on"`
	Priority        string  `json:"priority"`
	Status          string  `json:"status"`
	LastGeneratedOn *string `json:"last_generated_on"`
	Version         int64   `json:"version"`
}

func preventiveView(p db.PreventiveSchedule) PreventiveView {
	return PreventiveView{ID: p.ID, PropertyID: p.PropertyID, AssetID: p.AssetID, Title: p.Title, Category: p.Category, IntervalDays: p.IntervalDays,
		LeadDays: p.LeadDays, NextDueOn: p.NextDueOn.Format(time.DateOnly), Priority: p.Priority, Status: p.Status, LastGeneratedOn: dateStr(p.LastGeneratedOn),
		Version: p.Version}
}

// CreatePreventive adds a preventive schedule to a property.
func (s *Service) CreatePreventive(ctx context.Context, subj authz.Subject, propertyID ids.ID, in PreventiveInput) (PreventiveView, error) {
	if err := schema.Validate(in); err != nil {
		return PreventiveView{}, err
	}
	if in.Priority == "" {
		in.Priority = "NORMAL"
	}
	if in.LeadDays == 0 {
		in.LeadDays = 7
	}
	var out PreventiveView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapAssetsManage), propertyID); err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		if in.AssetID != nil {
			if _, err := s.q.GetAsset(ctx, tx, db.GetAssetParams{ID: *in.AssetID, PropertyID: propertyID}); isNoRows(err) {
				return apperrors.InvalidRequest("asset_id does not belong to the property.")
			} else if err != nil {
				return err
			}
		}
		p, err := s.q.InsertPreventiveSchedule(ctx, tx, db.InsertPreventiveScheduleParams{ID: ids.New(), PropertyID: propertyID, AssetID: in.AssetID,
			Title: in.Title, Category: in.Category, IntervalDays: in.IntervalDays, LeadDays: in.LeadDays, NextDueOn: *schema.ParseDate(&in.FirstDueOn),
			Priority: in.Priority, CreatedAt: now})
		if err != nil {
			return err
		}
		out = preventiveView(p)
		return s.record(ctx, "maintenance.preventive.create", AggProperty, propertyID, map[string]any{"schedule_id": p.ID, "interval_days": in.IntervalDays}, nil)
	})
	return out, err
}

// ListPreventive lists a property's preventive schedules.
func (s *Service) ListPreventive(ctx context.Context, subj authz.Subject, propertyID ids.ID) ([]PreventiveView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapAssetsManage), propertyID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListPreventiveSchedules(ctx, s.dbq(ctx), propertyID)
	out := make([]PreventiveView, 0, len(rows))
	for _, r := range rows {
		out = append(out, preventiveView(r))
	}
	return out, err
}

// PreventiveUpdate edits, pauses, resumes or retires a schedule (If-Match).
type PreventiveUpdate struct {
	Title        string `json:"title" v:"required,max=120"`
	IntervalDays int32  `json:"interval_days" v:"required,min=1,max=3650"`
	LeadDays     int32  `json:"lead_days" v:"min=0,max=90"`
	NextDueOn    string `json:"next_due_on" v:"required,format=date"`
	Priority     string `json:"priority" v:"required,enum=LOW|NORMAL|HIGH"`
	Status       string `json:"status" v:"required,enum=ACTIVE|PAUSED|RETIRED"`
}

// UpdatePreventive updates a schedule.
func (s *Service) UpdatePreventive(ctx context.Context, subj authz.Subject, id ids.ID, expected int64, in PreventiveUpdate) (PreventiveView, error) {
	if err := schema.Validate(in); err != nil {
		return PreventiveView{}, err
	}
	var out PreventiveView
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx := mustTx(ctx)
		p, err := s.q.GetPreventiveSchedule(ctx, tx, id)
		if isNoRows(err) {
			return notFound("preventive_schedule")
		}
		if err != nil {
			return err
		}
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapAssetsManage), p.PropertyID); err != nil {
			return err
		}
		if p.Status == "RETIRED" {
			return conflict("SCHEDULE_RETIRED", "The schedule is retired.")
		}
		n, err := s.q.UpdatePreventiveSchedule(ctx, tx, db.UpdatePreventiveScheduleParams{ID: id, Version: expected, Title: in.Title, IntervalDays: in.IntervalDays,
			LeadDays: in.LeadDays, NextDueOn: *schema.ParseDate(&in.NextDueOn), Priority: in.Priority, Status: in.Status, UpdatedAt: s.now()})
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		out = preventiveView(n)
		return s.record(ctx, "maintenance.preventive.update", AggProperty, p.PropertyID, map[string]any{"schedule_id": id, "status": in.Status}, nil)
	})
	return out, err
}

// ---- jobs (§11 preventive maintenance; provider payments) ----

// RunMaintenanceJobs expires quotes and warranties, creates due preventive
// tasks and requests/reconciles provider payments.
func (s *Service) RunMaintenanceJobs(ctx context.Context, batch int32) error {
	now := s.now()
	if _, err := s.q.ExpireQuotes(ctx, s.Pool, now); err != nil {
		return err
	}
	if _, err := s.q.ExpireWarranties(ctx, s.Pool, db.ExpireWarrantiesParams{Now: now, Today: dayOf(now)}); err != nil {
		return err
	}
	if err := s.InTx(ctx, func(ctx context.Context) error { return s.generatePreventive(ctx, batch) }); err != nil {
		return err
	}
	if s.Payment == nil {
		return nil
	}
	need, err := s.q.JobsNeedingIntent(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, id := range need {
		if err := s.requestJobIntent(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "maintenance payment intent deferred", "job_id", id, "error", err)
		}
	}
	recon, err := s.q.JobsToReconcile(ctx, s.Pool, db.JobsToReconcileParams{CheckedBefore: now.Add(-s.Policy.Domain.PaymentReconcileAfter), MaxRows: batch})
	if err != nil {
		return err
	}
	for _, id := range recon {
		j, err := s.q.GetMaintenanceJob(ctx, s.Pool, id)
		if err != nil {
			return err
		}
		in, err := s.Payment.GetIntent(ctx, *j.PaymentIntentRef)
		if err != nil {
			s.Logger.WarnContext(ctx, "maintenance payment read-back deferred", "job_id", id, "error", err)
			continue
		}
		if err := s.InTx(ctx, func(ctx context.Context) error { return s.projectJobIntent(ctx, id, in) }); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) generatePreventive(ctx context.Context, batch int32) error {
	tx, now := mustTx(ctx), s.now()
	due, err := s.q.DuePreventiveSchedules(ctx, tx, db.DuePreventiveSchedulesParams{Today: dayOf(now), MaxRows: batch})
	if err != nil {
		return err
	}
	for _, p := range due {
		dueOn := p.NextDueOn
		m, err := s.q.InsertMaintenanceRequest(ctx, tx, db.InsertMaintenanceRequestParams{ID: ids.New(), PropertyID: p.PropertyID, AssetID: p.AssetID,
			PreventiveScheduleID: &p.ID, DueOn: &dueOn, Title: p.Title, Description: "Preventive task due " + dueOn.Format(time.DateOnly) + ".",
			Category: p.Category, Priority: p.Priority, Responsibility: "OWNER", ReportedByType: "SYSTEM", EvidenceMediaIds: []ids.ID{},
			Currency: s.Policy.Domain.DefaultCurrency, CreatedAt: now})
		if err != nil && !isNoRows(err) {
			return err
		}
		if err == nil {
			if err := domain.Maintenance.RecordCreation(ctx, tx, m.ID, &m.PropertyID, "OPEN", now); err != nil {
				return err
			}
			if _, _, err := s.transition(ctx, domain.Maintenance, m.ID, lifecycle.Change{To: "TRIAGED", Command: "preventive_schedule"}); err != nil {
				return err
			}
			prop, err := s.q.GetProperty(ctx, tx, p.PropertyID)
			if err != nil {
				return err
			}
			if err := s.notifyOwner(ctx, prop.OwnerAccountID, TemplatePreventiveDue, map[string]string{"title": p.Title, "due_on": dueOn.Format(time.DateOnly)}); err != nil {
				return err
			}
		}
		if err := s.q.AdvancePreventiveSchedule(ctx, tx, db.AdvancePreventiveScheduleParams{ID: p.ID, NextDueOn: dueOn.AddDate(0, 0, int(p.IntervalDays)),
			LastGeneratedOn: &dueOn, UpdatedAt: now}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) requestJobIntent(ctx context.Context, id ids.ID) error {
	j, err := s.q.GetMaintenanceJob(ctx, s.Pool, id)
	if err != nil || j.PaymentIntentRef != nil {
		return err
	}
	m, err := s.q.GetMaintenanceRequest(ctx, s.Pool, j.RequestID)
	if err != nil {
		return err
	}
	p, err := s.q.GetProperty(ctx, s.Pool, m.PropertyID)
	if err != nil {
		return err
	}
	amount := j.AmountMinor
	if j.InvoicedMinor != nil && *j.InvoicedMinor < amount {
		amount = *j.InvoicedMinor
	}
	in, err := s.Payment.CreateIntent(ctx, payment.IntentRequest{Purpose: payment.PurposeMaintenanceJob, ReferenceType: "maintenance_job", ReferenceID: j.ID,
		PayeeDomain: string(platform.DomainArtisan), PayeeRefID: j.ProviderRef, PayerDomain: string(platform.DomainIndividualOwner), PayerRefID: &p.OwnerAccountID,
		AmountMinor: amount, Currency: j.Currency, DueDate: s.now().Add(7 * 24 * time.Hour)}, "maintenance_job:"+j.ID.String())
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		if err := s.q.SetJobIntent(ctx, mustTx(ctx), db.SetJobIntentParams{ID: id, PaymentIntentRef: &in.Ref, LastPaymentStatus: &in.Status, LastCheckedAt: ptr(s.now())}); err != nil {
			return err
		}
		return s.projectJobIntent(ctx, id, in)
	})
}

func (s *Service) projectJobIntent(ctx context.Context, id ids.ID, in payment.Intent) error {
	if fresh, err := s.intentIsFresh(ctx, in); !fresh || err != nil {
		return err
	}
	tx := mustTx(ctx)
	if err := s.q.SetJobPayment(ctx, tx, db.SetJobPaymentParams{ID: id, PaidMinor: in.PaidMinor, LastPaymentStatus: &in.Status, LastCheckedAt: ptr(s.now())}); err != nil {
		return err
	}
	if in.Status == payment.IntentSucceeded {
		j, err := s.q.GetMaintenanceJob(ctx, tx, id)
		if err != nil {
			return err
		}
		return s.record(ctx, "maintenance.job.paid", AggMaintenance, j.RequestID, map[string]any{"paid_minor": in.PaidMinor}, nil)
	}
	return nil
}

func init() {
	produces("maintenance.request.created.v1", AggMaintenance, "A platform-sourced maintenance request is open for artisan quotes (also warranty claims, routed to the original provider).", "Artisan", MaintenanceEvent{})
	produces("maintenance.request.updated.v1", AggMaintenance, "Platform-sourced request state for the provider (quote decisions, assignment, rework, completion, cancellation).", "Artisan", MaintenanceEvent{})
	archiveBlockerChecks = append(archiveBlockerChecks, struct{ name, sql string }{"maintenance job",
		`SELECT EXISTS (SELECT 1 FROM maintenance_requests WHERE property_id = $1 AND status IN ('APPROVED','ASSIGNED','IN_PROGRESS','COMPLETION_REVIEW','REWORK','DISPUTED'))`})
}
