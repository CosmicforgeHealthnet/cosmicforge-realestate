package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func init() {
	registerOpsTable("cost_centres", "fiscal_periods", "budgets", "financial_obligations", "expense_requests", "payout_destination_refs", "payout_change_requests", "revenue_split_rules", "channel_settlement_expectations", "financial_exception_cases")
}

type CostCentreCreate struct {
	ParentID         *ids.ID `json:"parent_id,omitempty"`
	PropertyID       *ids.ID `json:"property_id,omitempty"`
	Code             string  `json:"code"`
	Name             string  `json:"name"`
	ProjectReference string  `json:"project_reference,omitempty"`
	Reason           string  `json:"reason"`
}

type FiscalPeriodCreate struct {
	Code     string `json:"code"`
	StartsOn string `json:"starts_on"`
	EndsOn   string `json:"ends_on"`
	Reason   string `json:"reason"`
}

type BudgetLine struct {
	LineNo                 int    `json:"line_no"`
	Category               string `json:"category"`
	AmountMinor            int64  `json:"amount_minor"`
	ApprovalThresholdMinor int64  `json:"approval_threshold_minor"`
}

type BudgetCreate struct {
	CostCentreID   ids.ID       `json:"cost_centre_id"`
	FiscalPeriodID ids.ID       `json:"fiscal_period_id"`
	Currency       string       `json:"currency"`
	Lines          []BudgetLine `json:"lines"`
	Reason         string       `json:"reason"`
}

type Budget struct {
	ID             ids.ID  `json:"id"`
	CostCentreID   ids.ID  `json:"cost_centre_id"`
	FiscalPeriodID ids.ID  `json:"fiscal_period_id"`
	BudgetVersion  int     `json:"budget_version"`
	Currency       string  `json:"currency"`
	TotalMinor     int64   `json:"total_minor"`
	State          string  `json:"state"`
	ApprovalID     *ids.ID `json:"approval_id"`
	Version        int64   `json:"version"`
}

type FinanceApprovalCommand struct {
	Version    int64  `json:"version"`
	ApprovalID ids.ID `json:"approval_id,omitempty"`
	Reason     string `json:"reason"`
}

type ExpenseRequestCreate struct {
	BudgetID              ids.ID  `json:"budget_id"`
	BudgetLineNo          int     `json:"budget_line_no"`
	PropertyID            *ids.ID `json:"property_id,omitempty"`
	Description           string  `json:"description"`
	CounterpartyReference string  `json:"counterparty_reference"`
	AmountMinor           int64   `json:"amount_minor"`
	Reason                string  `json:"reason"`
}

type ExpenseRequest struct {
	ID                    ids.ID     `json:"id"`
	CostCentreID          ids.ID     `json:"cost_centre_id"`
	BudgetID              ids.ID     `json:"budget_id"`
	BudgetLineNo          int        `json:"budget_line_no"`
	PropertyID            *ids.ID    `json:"property_id"`
	Description           string     `json:"description"`
	CounterpartyReference string     `json:"counterparty_reference"`
	AmountMinor           int64      `json:"amount_minor"`
	Currency              string     `json:"currency"`
	RequiresApproval      bool       `json:"requires_approval"`
	State                 string     `json:"state"`
	ApprovalID            *ids.ID    `json:"approval_id"`
	ApprovedAmountMinor   *int64     `json:"approved_amount_minor"`
	ExpiresAt             *time.Time `json:"expires_at"`
	ObligationID          *ids.ID    `json:"obligation_id"`
	Version               int64      `json:"version"`
}

type ObligationCreate struct {
	PropertyID            *ids.ID `json:"property_id,omitempty"`
	CostCentreID          ids.ID  `json:"cost_centre_id"`
	Direction             string  `json:"direction"`
	Category              string  `json:"category"`
	CounterpartyReference string  `json:"counterparty_reference"`
	SourceType            string  `json:"source_type"`
	SourceReference       string  `json:"source_reference"`
	AmountMinor           int64   `json:"amount_minor"`
	Currency              string  `json:"currency"`
	TaxPolicyReference    string  `json:"tax_policy_reference,omitempty"`
	DueOn                 string  `json:"due_on"`
	Reason                string  `json:"reason"`
}

type FinancialObligation struct {
	ID                    ids.ID     `json:"id"`
	PropertyID            *ids.ID    `json:"property_id"`
	CostCentreID          ids.ID     `json:"cost_centre_id"`
	Direction             string     `json:"direction"`
	Category              string     `json:"category"`
	CounterpartyReference string     `json:"counterparty_reference"`
	SourceType            string     `json:"source_type"`
	SourceReference       string     `json:"source_reference"`
	OriginalAmountMinor   int64      `json:"original_amount_minor"`
	AmountMinor           int64      `json:"amount_minor"`
	Currency              string     `json:"currency"`
	DueOn                 string     `json:"due_on"`
	State                 string     `json:"state"`
	ApprovalID            *ids.ID    `json:"approval_id"`
	PaymentReference      *ids.ID    `json:"payment_reference"`
	SettledMinor          int64      `json:"settled_minor"`
	SettlementState       string     `json:"settlement_state"`
	LastReadbackAt        *time.Time `json:"last_readback_at"`
	Version               int64      `json:"version"`
}

type ObligationAdjustment struct {
	Version    int64  `json:"version"`
	DeltaMinor int64  `json:"delta_minor"`
	ApprovalID ids.ID `json:"approval_id"`
	Reason     string `json:"reason"`
}

type ObligationPaymentReference struct {
	Version          int64  `json:"version"`
	PaymentReference ids.ID `json:"payment_reference"`
	Reason           string `json:"reason"`
}

type PayoutDestinationCreate struct {
	PaymentAuthorityID ids.ID `json:"payment_authority_id"`
	Label              string `json:"label"`
	Currency           string `json:"currency"`
	Reason             string `json:"reason"`
}

type PayoutDestination struct {
	ID                  ids.ID     `json:"id"`
	PaymentAuthorityID  ids.ID     `json:"payment_authority_id"`
	Label               string     `json:"label"`
	Currency            string     `json:"currency"`
	State               string     `json:"state"`
	PaymentValidation   string     `json:"payment_validation"`
	ValidatedAt         *time.Time `json:"validated_at"`
	ValidationExpiresAt *time.Time `json:"validation_expires_at"`
	Version             int64      `json:"version"`
}

type PayoutChangeCreate struct {
	ToDestinationID ids.ID `json:"to_destination_id"`
	ApprovalID      ids.ID `json:"approval_id"`
	Reason          string `json:"reason"`
}

type PayoutChange struct {
	ID                ids.ID     `json:"id"`
	FromDestinationID *ids.ID    `json:"from_destination_id"`
	ToDestinationID   ids.ID     `json:"to_destination_id"`
	State             string     `json:"state"`
	ApprovalID        ids.ID     `json:"approval_id"`
	CoolingOffUntil   time.Time  `json:"cooling_off_until"`
	EffectiveAt       *time.Time `json:"effective_at"`
	BlockedReason     string     `json:"blocked_reason"`
	Version           int64      `json:"version"`
}

type RevenueSplitCreate struct {
	PropertyID        ids.ID               `json:"property_id"`
	ContractReference string               `json:"contract_reference"`
	Currency          string               `json:"currency"`
	Shares            []finance.SplitShare `json:"shares"`
	EffectiveFrom     string               `json:"effective_from"`
	EffectiveTo       string               `json:"effective_to,omitempty"`
	Reason            string               `json:"reason"`
}

type RevenueSplitRule struct {
	ID                ids.ID          `json:"id"`
	PropertyID        ids.ID          `json:"property_id"`
	ContractReference string          `json:"contract_reference"`
	Currency          string          `json:"currency"`
	Shares            json.RawMessage `json:"shares"`
	SharesDigest      string          `json:"shares_digest"`
	EffectiveFrom     string          `json:"effective_from"`
	State             string          `json:"state"`
	ApprovalID        *ids.ID         `json:"approval_id"`
	Version           int64           `json:"version"`
}

type RevenueSplitPreview struct {
	AmountMinor int64 `json:"amount_minor"`
}

type ChannelSettlementCreate struct {
	PropertyID                  ids.ID  `json:"property_id"`
	ConnectionID                ids.ID  `json:"connection_id"`
	ReservationID               *ids.ID `json:"reservation_id,omitempty"`
	ProviderRemittanceReference string  `json:"provider_remittance_reference"`
	CollectionModel             string  `json:"collection_model"`
	GrossMinor                  int64   `json:"gross_minor"`
	CommissionMinor             int64   `json:"commission_minor"`
	PromotionMinor              int64   `json:"promotion_minor"`
	FeesMinor                   int64   `json:"fees_minor"`
	TaxMinor                    int64   `json:"tax_minor"`
	Currency                    string  `json:"currency"`
	ExpectedOn                  string  `json:"expected_on"`
	Reason                      string  `json:"reason"`
}

type ChannelSettlement struct {
	ID                          ids.ID  `json:"id"`
	PropertyID                  ids.ID  `json:"property_id"`
	ConnectionID                ids.ID  `json:"connection_id"`
	ProviderRemittanceReference string  `json:"provider_remittance_reference"`
	ExpectedNetMinor            int64   `json:"expected_net_minor"`
	Currency                    string  `json:"currency"`
	ExpectedOn                  string  `json:"expected_on"`
	State                       string  `json:"state"`
	ActualNetMinor              *int64  `json:"actual_net_minor"`
	ActualSourceReference       *string `json:"actual_source_reference"`
	Version                     int64   `json:"version"`
}

type FinancialExceptionResolve struct {
	Version    int64  `json:"version"`
	Outcome    string `json:"outcome"`
	Resolution string `json:"resolution"`
}

func (h *Handler) registerFinance(r chi.Router) {
	r.Get("/cost-centres", h.costCentres)
	r.Post("/cost-centres", h.costCentreCreate)
	r.Get("/fiscal-periods", h.fiscalPeriods)
	r.Post("/fiscal-periods", h.fiscalPeriodCreate)
	r.Post("/fiscal-periods/{id}/close", h.fiscalPeriodClose)
	r.Get("/budgets", h.budgetList)
	r.Post("/budgets", h.budgetCreate)
	r.Get("/budgets/{id}", h.budgetGet)
	r.Post("/budgets/{id}/activate", h.budgetActivate)
	r.Get("/expense-requests", h.expenseList)
	r.Post("/expense-requests", h.expenseCreate)
	r.Post("/expense-requests/{id}/approve", h.expenseApprove)
	r.Post("/expense-requests/{id}/obligation", h.expenseObligation)
	r.Post("/expense-requests/{id}/cancel", h.expenseCancel)
	r.Get("/financial-obligations", h.obligationList)
	r.Post("/financial-obligations", h.obligationCreate)
	r.Get("/financial-obligations/{id}", h.obligationGet)
	r.Post("/financial-obligations/{id}/approve", h.obligationApprove)
	r.Post("/financial-obligations/{id}/adjustments", h.obligationAdjust)
	r.Post("/financial-obligations/{id}/payment-reference", h.obligationPaymentReference)
	r.Post("/financial-obligations/{id}/readback", h.obligationReadback)
	r.Get("/payout-destinations", h.payoutDestinations)
	r.Post("/payout-destinations", h.payoutDestinationCreate)
	r.Post("/payout-destinations/{id}/verify", h.payoutDestinationVerify)
	r.Get("/payout-change-requests", h.payoutChanges)
	r.Post("/payout-change-requests", h.payoutChangeCreate)
	r.Post("/payout-change-requests/{id}/cancel", h.payoutChangeCancel)
	r.Get("/revenue-splits", h.revenueSplits)
	r.Post("/revenue-splits", h.revenueSplitCreate)
	r.Post("/revenue-splits/{id}/activate", h.revenueSplitActivate)
	r.Post("/revenue-splits/{id}/preview", h.revenueSplitPreview)
	r.Get("/channel-settlements", h.channelSettlements)
	r.Post("/channel-settlements", h.channelSettlementCreate)
	r.Get("/financial-exceptions", h.financialExceptions)
	r.Post("/financial-exceptions/{id}/resolve", h.financialExceptionResolve)
	r.Get("/finance/reports", h.financeReport)
}

func (h *Handler) financeList(w http.ResponseWriter, r *http.Request, capability, query string, extra ...any) {
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, capability, "COMPANY", ids.Nil, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	args := append([]any{t.Organisation}, extra...)
	args = append(args, limit, offset)
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, query, args...)
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}

// financeCommand runs a company-scoped finance mutation with idempotency.
func (h *Handler) financeCommand(w http.ResponseWriter, r *http.Request, operation, capability string, mfa bool, payload any, fn func(context.Context, pgx.Tx, tenant, workforce.Target) (int, any, error)) {
	h.execute(w, r, "finance:"+operation, payload, capability, "COMPANY", ids.Nil, mfa, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, capability, "COMPANY", ids.Nil, mfa)
		if e != nil {
			return 0, nil, e
		}
		return fn(ctx, tx, t, target)
	})
}

func validCurrency(c string) bool {
	_, e := money.ParseCurrency(c)
	return e == nil
}

func (h *Handler) costCentres(w http.ResponseWriter, r *http.Request) {
	h.financeList(w, r, "finance:read", `SELECT to_jsonb(x) FROM cost_centres x WHERE organisation_id=$1 ORDER BY code LIMIT $2 OFFSET $3`)
}

func (h *Handler) costCentreCreate(w http.ResponseWriter, r *http.Request) {
	var in CostCentreCreate
	if !decode(w, r, &in) {
		return
	}
	if len(in.Code) < 2 || len(in.Code) > 40 || !opsText(in.Name, 1, 200) || len(in.ProjectReference) > 200 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Code, name, optional parent/property/project and reason required."))
		return
	}
	h.financeCommand(w, r, "cost-centre:create", "finance:write", false, in, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target) (int, any, error) {
		id := ids.New()
		if _, e := tx.Exec(ctx, `INSERT INTO cost_centres(id,organisation_id,parent_id,property_id,code,name,project_reference,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, in.ParentID, in.PropertyID, in.Code, in.Name, in.ProjectReference, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := oneJSON(ctx, tx, `SELECT to_jsonb(x) FROM cost_centres x WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, ids.Nil, id, "cost_centre", "CREATED", "", "ACTIVE", "", 1, in)
	})
}

func (h *Handler) fiscalPeriods(w http.ResponseWriter, r *http.Request) {
	h.financeList(w, r, "finance:read", `SELECT to_jsonb(x) FROM fiscal_periods x WHERE organisation_id=$1 ORDER BY starts_on LIMIT $2 OFFSET $3`)
}

func (h *Handler) fiscalPeriodCreate(w http.ResponseWriter, r *http.Request) {
	var in FiscalPeriodCreate
	if !decode(w, r, &in) {
		return
	}
	from, e1 := time.Parse(time.DateOnly, in.StartsOn)
	to, e2 := time.Parse(time.DateOnly, in.EndsOn)
	if len(in.Code) < 2 || len(in.Code) > 40 || e1 != nil || e2 != nil || !to.After(from) || to.Sub(from) > 400*24*time.Hour || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Code, non-overlapping period of at most 400 days and reason required."))
		return
	}
	h.financeCommand(w, r, "fiscal-period:create", "finance:write", false, in, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target) (int, any, error) {
		id := ids.New()
		if _, e := tx.Exec(ctx, `INSERT INTO fiscal_periods(id,organisation_id,code,starts_on,ends_on,created_by) VALUES($1,$2,$3,$4,$5,$6)`, id, t.Organisation, in.Code, in.StartsOn, in.EndsOn, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := oneJSON(ctx, tx, `SELECT to_jsonb(x) FROM fiscal_periods x WHERE id=$1`, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, ids.Nil, id, "fiscal_period", "CREATED", "", "OPEN", "", 1, in)
	})
}

// fiscalPeriodClose is blocked while financial exceptions raised during the
// period remain open (financial close exception handling).
func (h *Handler) fiscalPeriodClose(w http.ResponseWriter, r *http.Request) {
	var in OpsVersionCommand
	if !decode(w, r, &in) {
		return
	}
	id, e := pathCommercialID(r)
	if e != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.financeCommand(w, r, "fiscal-period:close:"+id.String(), "finance:approve", true, in, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target) (int, any, error) {
		var starts, ends time.Time
		var state string
		var version int64
		if e := tx.QueryRow(ctx, `SELECT starts_on,ends_on,state,version FROM fiscal_periods WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, id).Scan(&starts, &ends, &state, &version); e != nil {
			return 0, nil, opsNotFound(e)
		}
		if version != in.Version || state == "CLOSED" {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Period version or state has changed.")
		}
		var open int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM financial_exception_cases WHERE organisation_id=$1 AND state IN ('OPEN','INVESTIGATING') AND created_at<($2::date+1)`, t.Organisation, ends).Scan(&open); e != nil {
			return 0, nil, e
		}
		if open > 0 {
			return 0, nil, apperrors.Conflict("OPEN_FINANCIAL_EXCEPTIONS", "Resolve open financial exceptions before closing the period.")
		}
		if state == "OPEN" {
			if _, e := tx.Exec(ctx, `UPDATE fiscal_periods SET state='CLOSING',version=version+1,updated_at=now() WHERE id=$1`, id); e != nil {
				return 0, nil, e
			}
		}
		if _, e := tx.Exec(ctx, `UPDATE fiscal_periods SET state='CLOSED',closed_by=$2,closed_at=now(),version=version+1,updated_at=now() WHERE id=$1`, id, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := oneJSON(ctx, tx, `SELECT to_jsonb(x) FROM fiscal_periods x WHERE id=$1`, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, ids.Nil, id, "fiscal_period", "CLOSED", state, "CLOSED", businessEvents.FinanceBudgetUpdated, version+2, map[string]any{"reason": in.Reason})
	})
}

func (h *Handler) budgetList(w http.ResponseWriter, r *http.Request) {
	h.financeList(w, r, "finance:read", `SELECT to_jsonb(x) FROM budgets x WHERE organisation_id=$1 AND ($2='' OR cost_centre_id::text=$2) ORDER BY created_at DESC LIMIT $3 OFFSET $4`, r.URL.Query().Get("cost_centre_id"))
}

var budgetCategories = []string{"OPERATIONS", "MAINTENANCE", "MARKETING", "HOUSEKEEPING", "UTILITIES", "CAPEX", "PROFESSIONAL_SERVICES", "OTHER"}

func (h *Handler) budgetCreate(w http.ResponseWriter, r *http.Request) {
	var in BudgetCreate
	if !decode(w, r, &in) {
		return
	}
	ok := in.CostCentreID.ValidateV7() == nil && in.FiscalPeriodID.ValidateV7() == nil && validCurrency(in.Currency) && len(in.Lines) >= 1 && len(in.Lines) <= 200 && validStaffReason(in.Reason)
	var total int64
	seen := map[int]bool{}
	for _, l := range in.Lines {
		ok = ok && l.LineNo >= 1 && l.LineNo <= 200 && !seen[l.LineNo] && slices.Contains(budgetCategories, l.Category) && l.AmountMinor >= 0 && l.AmountMinor <= 1e15 && l.ApprovalThresholdMinor >= 0
		seen[l.LineNo] = true
		total += l.AmountMinor
	}
	if !ok {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Cost centre, fiscal period, currency, unique valid budget lines and reason required."))
		return
	}
	h.financeCommand(w, r, "budget:create", "finance:write", false, in, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target) (int, any, error) {
		var next int
		if e := tx.QueryRow(ctx, `SELECT coalesce(max(budget_version),0)+1 FROM budgets WHERE organisation_id=$1 AND cost_centre_id=$2 AND fiscal_period_id=$3`, t.Organisation, in.CostCentreID, in.FiscalPeriodID).Scan(&next); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e := tx.Exec(ctx, `INSERT INTO budgets(id,organisation_id,cost_centre_id,fiscal_period_id,budget_version,currency,total_minor,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, t.Organisation, in.CostCentreID, in.FiscalPeriodID, next, in.Currency, total, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		for _, l := range in.Lines {
			if _, e := tx.Exec(ctx, `INSERT INTO budget_lines(organisation_id,budget_id,line_no,category,amount_minor,approval_threshold_minor) VALUES($1,$2,$3,$4,$5,$6)`, t.Organisation, id, l.LineNo, l.Category, l.AmountMinor, l.ApprovalThresholdMinor); e != nil {
				return 0, nil, e
			}
		}
		out, e := opsGet[Budget](ctx, tx, "budgets", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, ids.Nil, id, "budget", "CREATED", "", "DRAFT", businessEvents.FinanceBudgetUpdated, 1, map[string]any{"total_minor": total, "currency": in.Currency, "budget_version": next})
	})
}

// budgetUsage returns line amount, threshold and committed (approved or
// obligated expense) minor units.
func budgetUsage(ctx context.Context, tx pgx.Tx, org, budget ids.ID, line int) (int64, int64, int64, error) {
	var amount, threshold, committed int64
	e := tx.QueryRow(ctx, `SELECT l.amount_minor,l.approval_threshold_minor,coalesce((SELECT sum(amount_minor) FROM expense_requests x WHERE x.organisation_id=$1 AND x.budget_id=$2 AND x.budget_line_no=$3 AND x.state IN ('APPROVED','OBLIGATION_CREATED')),0) FROM budget_lines l WHERE l.organisation_id=$1 AND l.budget_id=$2 AND l.line_no=$3`, org, budget, line).Scan(&amount, &threshold, &committed)
	return amount, threshold, committed, opsNotFound(e)
}

func (h *Handler) budgetGet(w http.ResponseWriter, r *http.Request) {
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "finance:read", "COMPANY", ids.Nil, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	b, e := opsGet[Budget](r.Context(), h.Auth.Service.Pool, "budgets", t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	lines, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(l)||jsonb_build_object('committed_minor',coalesce((SELECT sum(amount_minor) FROM expense_requests x WHERE x.budget_id=l.budget_id AND x.budget_line_no=l.line_no AND x.state IN ('APPROVED','OBLIGATION_CREATED')),0),'actual_settled_minor',coalesce((SELECT sum(o.settled_minor) FROM expense_requests x JOIN financial_obligations o ON o.id=x.obligation_id WHERE x.budget_id=l.budget_id AND x.budget_line_no=l.line_no),0)) FROM budget_lines l WHERE l.organisation_id=$1 AND l.budget_id=$2 ORDER BY line_no`, t.Organisation, id)
	reply(w, r, 200, map[string]any{"budget": b, "lines": lines}, e)
}

func (h *Handler) budgetActivate(w http.ResponseWriter, r *http.Request) {
	var in FinanceApprovalCommand
	if !decode(w, r, &in) {
		return
	}
	id, e := pathCommercialID(r)
	if e != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version, approval and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.financeCommand(w, r, "budget:activate:"+id.String(), "finance:write", true, in, func(ctx context.Context, tx pgx.Tx, t tenant, target workforce.Target) (int, any, error) {
		b, e := opsLock[Budget](ctx, tx, "budgets", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if b.Version != in.Version || b.State != "DRAFT" {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Budget version or state has changed.")
		}
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_BUDGET_ACTIVATION", target.Scope, in.ApprovalID, map[string]any{"budget_id": b.ID, "budget_version": b.BudgetVersion}, "", &b.TotalMinor, b.Currency); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE budgets SET state='SUPERSEDED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND cost_centre_id=$2 AND fiscal_period_id=$3 AND state='ACTIVE'`, t.Organisation, b.CostCentreID, b.FiscalPeriodID); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE budgets SET state='ACTIVE',approval_id=$2,version=version+1,updated_at=now() WHERE id=$1`, id, in.ApprovalID); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[Budget](ctx, tx, "budgets", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, ids.Nil, id, "budget", "ACTIVATED", "DRAFT", "ACTIVE", businessEvents.FinanceBudgetUpdated, out.Version, map[string]any{"approval_id": in.ApprovalID, "total_minor": b.TotalMinor})
	})
}

func (h *Handler) expenseList(w http.ResponseWriter, r *http.Request) {
	h.financeList(w, r, "finance:read", `SELECT to_jsonb(x) FROM expense_requests x WHERE organisation_id=$1 AND ($2='' OR state=$2) ORDER BY created_at DESC LIMIT $3 OFFSET $4`, r.URL.Query().Get("state"))
}

func (h *Handler) expenseCreate(w http.ResponseWriter, r *http.Request) {
	var in ExpenseRequestCreate
	if !decode(w, r, &in) {
		return
	}
	if in.BudgetID.ValidateV7() != nil || in.BudgetLineNo < 1 || !opsText(in.Description, 5, 1000) || !opsText(in.CounterpartyReference, 3, 200) || in.AmountMinor <= 0 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Budget line, description, counterparty reference, positive amount and reason required."))
		return
	}
	h.financeCommand(w, r, "expense:create", "finance:write", false, in, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target) (int, any, error) {
		b, e := opsLock[Budget](ctx, tx, "budgets", t.Organisation, in.BudgetID)
		if e != nil {
			return 0, nil, e
		}
		if b.State != "ACTIVE" {
			return 0, nil, apperrors.Conflict("BUDGET_NOT_ACTIVE", "Spend requires an active approved budget.")
		}
		amount, threshold, committed, e := budgetUsage(ctx, tx, t.Organisation, b.ID, in.BudgetLineNo)
		if e != nil {
			return 0, nil, e
		}
		if committed+in.AmountMinor > amount {
			return 0, nil, apperrors.Conflict("BUDGET_EXCEEDED", "The request exceeds the remaining budget line; a new approved budget version is required.")
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO expense_requests(id,organisation_id,cost_centre_id,budget_id,budget_line_no,property_id,description,counterparty_reference,amount_minor,currency,requires_approval,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, t.Organisation, b.CostCentreID, b.ID, in.BudgetLineNo, in.PropertyID, in.Description, in.CounterpartyReference, in.AmountMinor, b.Currency, in.AmountMinor > threshold, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[ExpenseRequest](ctx, tx, "expense_requests", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, ids.Nil, id, "expense_request", "CREATED", "", "DRAFT", businessEvents.FinanceExpenseUpdated, 1, map[string]any{"amount_minor": in.AmountMinor, "currency": b.Currency, "requires_approval": out.RequiresApproval})
	})
}

// expenseApprove records approval. Above the line threshold an exact,
// independent approval bound to amount/currency/line is consumed; a requester
// can never self-approve (approval engine separation of duties).
func (h *Handler) expenseApprove(w http.ResponseWriter, r *http.Request) {
	var in FinanceApprovalCommand
	if !decode(w, r, &in) {
		return
	}
	h.expenseCommand(w, r, in.Version, in.Reason, "APPROVE", true, in, func(ctx context.Context, tx pgx.Tx, t tenant, target workforce.Target, x ExpenseRequest) (string, error) {
		if x.State != "DRAFT" {
			return "", apperrors.Conflict("INVALID_TRANSITION", "Only drafts can be approved.")
		}
		amount, _, committed, e := budgetUsage(ctx, tx, t.Organisation, x.BudgetID, x.BudgetLineNo)
		if e != nil {
			return "", e
		}
		var active bool
		if e = tx.QueryRow(ctx, `SELECT state='ACTIVE' FROM budgets WHERE id=$1`, x.BudgetID).Scan(&active); e != nil {
			return "", e
		}
		if !active || committed+x.AmountMinor > amount {
			return "", apperrors.Conflict("BUDGET_EXCEEDED", "The budget line no longer covers this request.")
		}
		var approval any
		if x.RequiresApproval {
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_EXPENSE_APPROVAL", target.Scope, in.ApprovalID, map[string]any{"expense_id": x.ID, "budget_id": x.BudgetID, "budget_line_no": x.BudgetLineNo, "counterparty_reference": x.CounterpartyReference}, "", &x.AmountMinor, x.Currency); e != nil {
				return "", e
			}
			approval = in.ApprovalID
		}
		_, e = tx.Exec(ctx, `UPDATE expense_requests SET state='APPROVED',approval_id=$2,approved_amount_minor=amount_minor,expires_at=now()+interval '30 days',version=version+1,updated_at=now() WHERE id=$1`, x.ID, approval)
		return "APPROVED", e
	})
}

// expenseObligation creates the payable obligation for an approved, unexpired
// request. Approval never moves money; Payment Service executes payment.
func (h *Handler) expenseObligation(w http.ResponseWriter, r *http.Request) {
	var in OpsVersionCommand
	if !decode(w, r, &in) {
		return
	}
	h.expenseCommand(w, r, in.Version, in.Reason, "OBLIGATE", false, in, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target, x ExpenseRequest) (string, error) {
		if x.State != "APPROVED" {
			return "", apperrors.Conflict("INVALID_TRANSITION", "Only approved requests create obligations.")
		}
		if x.ExpiresAt == nil || !h.Auth.Service.Now().UTC().Before(*x.ExpiresAt) {
			if _, e := tx.Exec(ctx, `UPDATE expense_requests SET state='EXPIRED',version=version+1,updated_at=now() WHERE id=$1`, x.ID); e != nil {
				return "", e
			}
			return "EXPIRED", nil
		}
		obligation := ids.New()
		if _, e := tx.Exec(ctx, `INSERT INTO financial_obligations(id,organisation_id,property_id,cost_centre_id,direction,category,counterparty_reference,source_type,source_reference,original_amount_minor,amount_minor,currency,due_on,state,created_by) VALUES($1,$2,$3,$4,'PAYABLE','EXPENSE',$5,'EXPENSE_REQUEST',$6,$7,$7,$8,current_date+30,'APPROVED',$9)`, obligation, t.Organisation, x.PropertyID, x.CostCentreID, x.CounterpartyReference, "expense:"+x.ID.String(), x.AmountMinor, x.Currency, t.Account); e != nil {
			return "", e
		}
		if _, e := tx.Exec(ctx, `UPDATE expense_requests SET state='OBLIGATION_CREATED',obligation_id=$2,version=version+1,updated_at=now() WHERE id=$1`, x.ID, obligation); e != nil {
			return "", e
		}
		return "OBLIGATION_CREATED", h.opsCommit(ctx, tx, t, ids.Nil, obligation, "financial_obligation", "CREATED_FROM_EXPENSE", "", "APPROVED", businessEvents.FinanceObligationUpdated, 1, map[string]any{"expense_id": x.ID, "amount_minor": x.AmountMinor, "currency": x.Currency, "direction": "PAYABLE"})
	})
}

func (h *Handler) expenseCancel(w http.ResponseWriter, r *http.Request) {
	var in OpsVersionCommand
	if !decode(w, r, &in) {
		return
	}
	h.expenseCommand(w, r, in.Version, in.Reason, "CANCEL", false, in, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target, x ExpenseRequest) (string, error) {
		if x.State != "DRAFT" && x.State != "APPROVED" {
			return "", apperrors.Conflict("INVALID_TRANSITION", "The request is final.")
		}
		_, e := tx.Exec(ctx, `UPDATE expense_requests SET state='CANCELLED',version=version+1,updated_at=now() WHERE id=$1`, x.ID)
		return "CANCELLED", e
	})
}

func (h *Handler) expenseCommand(w http.ResponseWriter, r *http.Request, version int64, reason, action string, mfa bool, payload any, fn func(context.Context, pgx.Tx, tenant, workforce.Target, ExpenseRequest) (string, error)) {
	id, e := pathCommercialID(r)
	if e != nil || version < 1 || !validStaffReason(reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	capability := "finance:write"
	if action == "APPROVE" {
		capability = "finance:approve"
	}
	h.financeCommand(w, r, "expense:"+action+":"+id.String(), capability, mfa, payload, func(ctx context.Context, tx pgx.Tx, t tenant, target workforce.Target) (int, any, error) {
		x, e := opsLock[ExpenseRequest](ctx, tx, "expense_requests", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if x.Version != version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Expense request version has changed.")
		}
		to, e := fn(ctx, tx, t, target, x)
		if e != nil {
			return 0, nil, e
		}
		out, e := opsGet[ExpenseRequest](ctx, tx, "expense_requests", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, ids.Nil, id, "expense_request", action, x.State, to, businessEvents.FinanceExpenseUpdated, out.Version, map[string]any{"reason": reason, "approval_id": out.ApprovalID})
	})
}

func (h *Handler) obligationList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h.financeList(w, r, "finance:read", `SELECT to_jsonb(x) FROM financial_obligations x WHERE organisation_id=$1 AND ($2='' OR state=$2) AND ($3='' OR property_id::text=$3) ORDER BY due_on,id LIMIT $4 OFFSET $5`, q.Get("state"), q.Get("property_id"))
}

func (h *Handler) obligationCreate(w http.ResponseWriter, r *http.Request) {
	var in ObligationCreate
	if !decode(w, r, &in) {
		return
	}
	if _, e := time.Parse(time.DateOnly, in.DueOn); e != nil || in.CostCentreID.ValidateV7() != nil || in.Direction != "RECEIVABLE" && in.Direction != "PAYABLE" || !opsText(in.Category, 2, 60) || !opsText(in.CounterpartyReference, 3, 200) || !slices.Contains([]string{"CONTRACT", "FOLIO", "MANUAL"}, in.SourceType) || !opsText(in.SourceReference, 3, 300) || in.AmountMinor <= 0 || !validCurrency(in.Currency) || len(in.TaxPolicyReference) > 200 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Cost centre, direction, category, counterparty, source (CONTRACT/FOLIO/MANUAL), positive amount, currency, due date and reason required."))
		return
	}
	h.financeCommand(w, r, "obligation:create", "finance:write", false, in, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target) (int, any, error) {
		id := ids.New()
		if _, e := tx.Exec(ctx, `INSERT INTO financial_obligations(id,organisation_id,property_id,cost_centre_id,direction,category,counterparty_reference,source_type,source_reference,original_amount_minor,amount_minor,currency,tax_policy_reference,due_on,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10,$11,$12,$13,$14)`, id, t.Organisation, in.PropertyID, in.CostCentreID, in.Direction, in.Category, in.CounterpartyReference, in.SourceType, in.SourceReference, in.AmountMinor, in.Currency, in.TaxPolicyReference, in.DueOn, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[FinancialObligation](ctx, tx, "financial_obligations", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, ids.Nil, id, "financial_obligation", "CREATED", "", "DRAFT", businessEvents.FinanceObligationUpdated, 1, map[string]any{"direction": in.Direction, "amount_minor": in.AmountMinor, "currency": in.Currency, "source_type": in.SourceType})
	})
}

func (h *Handler) obligationGet(w http.ResponseWriter, r *http.Request) {
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "finance:read", "COMPANY", ids.Nil, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	o, e := opsGet[FinancialObligation](r.Context(), h.Auth.Service.Pool, "financial_obligations", t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	adjustments, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x) FROM obligation_adjustments x WHERE organisation_id=$1 AND obligation_id=$2 ORDER BY obligation_version`, t.Organisation, id)
	reply(w, r, 200, map[string]any{"obligation": o, "adjustments": adjustments, "money_movement_authority": "payment-service"}, e)
}

func (h *Handler) obligationCommand(w http.ResponseWriter, r *http.Request, version int64, reason, action, capability string, mfa bool, payload any, fn func(context.Context, pgx.Tx, tenant, workforce.Target, FinancialObligation) (string, error)) {
	id, e := pathCommercialID(r)
	if e != nil || version < 1 || !validStaffReason(reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.financeCommand(w, r, "obligation:"+action+":"+id.String(), capability, mfa, payload, func(ctx context.Context, tx pgx.Tx, t tenant, target workforce.Target) (int, any, error) {
		o, e := opsLock[FinancialObligation](ctx, tx, "financial_obligations", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if o.Version != version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Obligation version has changed.")
		}
		to, e := fn(ctx, tx, t, target, o)
		if e != nil {
			return 0, nil, e
		}
		out, e := opsGet[FinancialObligation](ctx, tx, "financial_obligations", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, ids.Nil, id, "financial_obligation", action, o.State, to, businessEvents.FinanceObligationUpdated, out.Version, map[string]any{"reason": reason, "amount_minor": out.AmountMinor, "settled_minor": out.SettledMinor})
	})
}

func (h *Handler) obligationApprove(w http.ResponseWriter, r *http.Request) {
	var in FinanceApprovalCommand
	if !decode(w, r, &in) {
		return
	}
	h.obligationCommand(w, r, in.Version, in.Reason, "APPROVE", "finance:write", true, in, func(ctx context.Context, tx pgx.Tx, t tenant, target workforce.Target, o FinancialObligation) (string, error) {
		if o.State != "DRAFT" {
			return "", apperrors.Conflict("INVALID_TRANSITION", "Only drafts are approved.")
		}
		if e := h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_FINANCIAL_OBLIGATION", target.Scope, in.ApprovalID, map[string]any{"obligation_id": o.ID, "direction": o.Direction, "counterparty_reference": o.CounterpartyReference}, "", &o.AmountMinor, o.Currency); e != nil {
			return "", e
		}
		_, e := tx.Exec(ctx, `UPDATE financial_obligations SET state='APPROVED',approval_id=$2,version=version+1,updated_at=now() WHERE id=$1`, o.ID, in.ApprovalID)
		return "APPROVED", e
	})
}

// obligationAdjust appends an immutable adjustment under exact approval.
func (h *Handler) obligationAdjust(w http.ResponseWriter, r *http.Request) {
	var in ObligationAdjustment
	if !decode(w, r, &in) {
		return
	}
	h.obligationCommand(w, r, in.Version, in.Reason, "ADJUST", "finance:write", true, in, func(ctx context.Context, tx pgx.Tx, t tenant, target workforce.Target, o FinancialObligation) (string, error) {
		if in.DeltaMinor == 0 || o.State != "APPROVED" && o.State != "DISPUTED" {
			return "", apperrors.Conflict("INVALID_TRANSITION", "Only approved or disputed obligations are adjusted, with a non-zero delta.")
		}
		next := o.AmountMinor + in.DeltaMinor
		if next < o.SettledMinor || next < 0 {
			return "", apperrors.Conflict("ADJUSTMENT_BELOW_SETTLED", "An adjustment cannot reduce the obligation below settled amounts.")
		}
		abs := in.DeltaMinor
		if abs < 0 {
			abs = -abs
		}
		if e := h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_FINANCIAL_OBLIGATION", target.Scope, in.ApprovalID, map[string]any{"obligation_id": o.ID, "delta_minor": in.DeltaMinor, "obligation_version": o.Version}, "", &abs, o.Currency); e != nil {
			return "", e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO obligation_adjustments(id,organisation_id,obligation_id,obligation_version,delta_minor,resulting_amount_minor,approval_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, ids.New(), t.Organisation, o.ID, o.Version+1, in.DeltaMinor, next, in.ApprovalID, in.Reason, t.Account); e != nil {
			return "", e
		}
		_, e := tx.Exec(ctx, `UPDATE financial_obligations SET amount_minor=$2,state=CASE WHEN state='DISPUTED' THEN 'APPROVED' ELSE state END,version=version+1,updated_at=now() WHERE id=$1`, o.ID, next)
		return "APPROVED", e
	})
}

// obligationPaymentReference binds the Payment Service intent/payout record
// created by the owning Payment workflow. It records intent only; no money
// movement happens in Business Operations.
func (h *Handler) obligationPaymentReference(w http.ResponseWriter, r *http.Request) {
	var in ObligationPaymentReference
	if !decode(w, r, &in) {
		return
	}
	h.obligationCommand(w, r, in.Version, in.Reason, "PAYMENT_REQUESTED", "finance:write", false, in, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target, o FinancialObligation) (string, error) {
		if o.State != "APPROVED" || in.PaymentReference.ValidateV7() != nil {
			return "", apperrors.Conflict("INVALID_TRANSITION", "Only approved obligations accept a Payment Service reference.")
		}
		_, e := tx.Exec(ctx, `UPDATE financial_obligations SET state='PAYMENT_REQUESTED',payment_reference=$2,settlement_state='REQUESTED',version=version+1,updated_at=now() WHERE id=$1`, o.ID, in.PaymentReference)
		return "PAYMENT_REQUESTED", e
	})
}

// obligationReadback reads the authoritative Payment Service intent. Only a
// funded/settled readback for the exact amount and currency settles it; an
// unavailable or ambiguous readback stays UNKNOWN_PENDING_READBACK.
func (h *Handler) obligationReadback(w http.ResponseWriter, r *http.Request) {
	var in OpsVersionCommand
	if !decode(w, r, &in) {
		return
	}
	id, e := pathCommercialID(r)
	if e != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "finance:write", "COMPANY", ids.Nil, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	o, e := opsGet[FinancialObligation](r.Context(), h.Auth.Service.Pool, "financial_obligations", t.Organisation, id)
	if e != nil || o.PaymentReference == nil {
		if e == nil {
			e = apperrors.Conflict("NO_PAYMENT_REFERENCE", "No Payment Service reference is bound.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	var intent payment.Intent
	var readErr error = apperrors.Unavailable("Payment Service is not configured.")
	if h.Payment != nil {
		intent, e = h.Payment.Intent(r.Context(), *o.PaymentReference)
		readErr = e
	}
	h.obligationCommand(w, r, in.Version, in.Reason, "READBACK", "finance:write", false, map[string]any{"version": in.Version, "reason": in.Reason, "at": h.Auth.Service.Now().UTC().Truncate(time.Minute)}, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target, cur FinancialObligation) (string, error) {
		if cur.State != "PAYMENT_REQUESTED" && cur.State != "UNKNOWN_PENDING_READBACK" {
			return "", apperrors.Conflict("INVALID_TRANSITION", "Only requested obligations are read back.")
		}
		to, settled, state := "PAYMENT_REQUESTED", cur.SettledMinor, "REQUESTED"
		switch {
		case readErr != nil:
			to, state = "UNKNOWN_PENDING_READBACK", "UNKNOWN"
			if _, e := tx.Exec(ctx, `INSERT INTO financial_exception_cases(id,organisation_id,property_id,kind,source_type,source_id,amount_delta_minor,currency,created_by) VALUES($1,$2,$3,'PAYMENT_UNKNOWN','FINANCIAL_OBLIGATION',$4,$5,$6,$7) ON CONFLICT DO NOTHING`, ids.New(), t.Organisation, cur.PropertyID, cur.ID, cur.AmountMinor, cur.Currency, t.Account); e != nil {
				return "", e
			}
		case intent.ID != *cur.PaymentReference || intent.Currency != cur.Currency || intent.AmountMinor != cur.AmountMinor:
			to, state = "DISPUTED", "READBACK_MISMATCH"
		case intent.SettlementState == "SETTLED":
			to, settled, state = "SETTLED", cur.AmountMinor, "SETTLED"
		case intent.SettlementState == "REVERSED" || intent.State == "REVERSED":
			to, state = "DISPUTED", "REVERSED"
			if _, e := tx.Exec(ctx, `INSERT INTO financial_exception_cases(id,organisation_id,property_id,kind,source_type,source_id,amount_delta_minor,currency,created_by) VALUES($1,$2,$3,'PAYMENT_REVERSAL','FINANCIAL_OBLIGATION',$4,$5,$6,$7) ON CONFLICT DO NOTHING`, ids.New(), t.Organisation, cur.PropertyID, cur.ID, cur.AmountMinor, cur.Currency, t.Account); e != nil {
				return "", e
			}
		default:
			state = "PENDING:" + intent.SettlementState
		}
		_, e := tx.Exec(ctx, `UPDATE financial_obligations SET state=$2,settled_minor=$3,settlement_state=$4,last_readback_at=now(),version=version+1,updated_at=now() WHERE id=$1`, cur.ID, to, settled, truncate(state, 60))
		return to, e
	})
}

func (h *Handler) payoutDestinations(w http.ResponseWriter, r *http.Request) {
	h.financeList(w, r, "finance:read", `SELECT to_jsonb(x) FROM payout_destination_refs x WHERE organisation_id=$1 ORDER BY created_at LIMIT $2 OFFSET $3`)
}

func (h *Handler) payoutDestinationCreate(w http.ResponseWriter, r *http.Request) {
	var in PayoutDestinationCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PaymentAuthorityID.ValidateV7() != nil || !opsText(in.Label, 1, 120) || !validCurrency(in.Currency) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Payment Service beneficiary authority reference, label, currency and reason required."))
		return
	}
	h.financeCommand(w, r, "payout-destination:create", "payouts:write", true, in, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target) (int, any, error) {
		id := ids.New()
		if _, e := tx.Exec(ctx, `INSERT INTO payout_destination_refs(id,organisation_id,payment_authority_id,label,currency,created_by) VALUES($1,$2,$3,$4,$5,$6)`, id, t.Organisation, in.PaymentAuthorityID, in.Label, in.Currency, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[PayoutDestination](ctx, tx, "payout_destination_refs", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, ids.Nil, id, "payout_destination", "REFERENCED", "", "PENDING_VERIFICATION", businessEvents.FinancePayoutChangeUpdated, 1, map[string]any{"payment_authority_id": in.PaymentAuthorityID, "currency": in.Currency})
	})
}

// paymentBeneficiary reads Payment Service beneficiary validation. Only an
// active, unexpired authority of this domain and currency is valid.
func (h *Handler) paymentBeneficiary(ctx context.Context, d PayoutDestination) (string, *time.Time, error) {
	reader, ok := h.Payment.(payment.BeneficiaryReader)
	if h.Payment == nil || !ok {
		return "", nil, apperrors.Unavailable("Payment Service beneficiary validation is unavailable.")
	}
	a, e := reader.BeneficiaryAuthority(ctx, d.PaymentAuthorityID)
	if e != nil {
		return "", nil, apperrors.Unavailable("Payment Service beneficiary validation is unavailable.")
	}
	now := h.Auth.Service.Now().UTC()
	switch {
	case a.AuthorityID != d.PaymentAuthorityID || a.Currency != d.Currency || a.Domain != string(platform.DomainBusinessOperations):
		return "REJECTED", nil, nil
	case !a.ExpiresAt.After(now):
		return "EXPIRED", nil, nil
	case !a.Active:
		return "REJECTED", nil, nil
	}
	return "VERIFIED", &a.ExpiresAt, nil
}

func (h *Handler) payoutDestinationVerify(w http.ResponseWriter, r *http.Request) {
	var in OpsVersionCommand
	if !decode(w, r, &in) {
		return
	}
	id, e := pathCommercialID(r)
	if e != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "payouts:write", "COMPANY", ids.Nil, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	d, e := opsGet[PayoutDestination](r.Context(), h.Auth.Service.Pool, "payout_destination_refs", t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	validation, expires, e := h.paymentBeneficiary(r.Context(), d)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.financeCommand(w, r, "payout-destination:verify:"+id.String(), "payouts:write", false, map[string]any{"version": in.Version, "validation": validation}, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target) (int, any, error) {
		cur, e := opsLock[PayoutDestination](ctx, tx, "payout_destination_refs", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if cur.Version != in.Version || cur.State == "REVOKED" {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Destination version or state has changed.")
		}
		state := map[string]string{"VERIFIED": "ACTIVE", "REJECTED": "REJECTED", "EXPIRED": "PENDING_VERIFICATION"}[validation]
		if cur.State == "REJECTED" && state == "REJECTED" {
			state = "PENDING_VERIFICATION"
		}
		if _, e = tx.Exec(ctx, `UPDATE payout_destination_refs SET state=$2,payment_validation=$3,validated_at=CASE WHEN $3='VERIFIED' THEN now() ELSE validated_at END,validation_expires_at=$4,version=version+1,updated_at=now() WHERE id=$1`, id, state, validation, expires); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[PayoutDestination](ctx, tx, "payout_destination_refs", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, ids.Nil, id, "payout_destination", "PAYMENT_VALIDATION_READBACK", cur.State, state, businessEvents.FinancePayoutChangeUpdated, out.Version, map[string]any{"payment_validation": validation})
	})
}

func (h *Handler) payoutChanges(w http.ResponseWriter, r *http.Request) {
	h.financeList(w, r, "finance:read", `SELECT to_jsonb(x) FROM payout_change_requests x WHERE organisation_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`)
}

// PayoutCoolingOff is the delay before a changed payout destination applies.
const PayoutCoolingOff = 24 * time.Hour

// payoutChangeCreate requires step-up MFA, an exact independent approval
// (Annex F), a Payment-verified destination, then a cooling-off period.
func (h *Handler) payoutChangeCreate(w http.ResponseWriter, r *http.Request) {
	var in PayoutChangeCreate
	if !decode(w, r, &in) {
		return
	}
	if in.ToDestinationID.ValidateV7() != nil || !opsText(in.Reason, 5, 1000) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Target destination, approval and reason required."))
		return
	}
	h.financeCommand(w, r, "payout-change:create", "payouts:write", true, in, func(ctx context.Context, tx pgx.Tx, t tenant, target workforce.Target) (int, any, error) {
		d, e := opsLock[PayoutDestination](ctx, tx, "payout_destination_refs", t.Organisation, in.ToDestinationID)
		if e != nil {
			return 0, nil, e
		}
		if d.State != "ACTIVE" || d.PaymentValidation != "VERIFIED" {
			return 0, nil, apperrors.Conflict("DESTINATION_NOT_PAYMENT_VERIFIED", "Payment Service has not validated this beneficiary; Business metadata is never treated as validation.")
		}
		var from *ids.ID
		if e = tx.QueryRow(ctx, `SELECT current_payout_destination_id FROM financial_profiles WHERE organisation_id=$1 FOR UPDATE`, t.Organisation).Scan(&from); e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return 0, nil, e
		}
		if from != nil && *from == d.ID {
			return 0, nil, apperrors.Conflict("DESTINATION_UNCHANGED", "The destination is already current.")
		}
		if e = h.requirePrivilegeChange(ctx, tx, t, approvals.ActionPayoutDestinationChange, target.Scope, in.ApprovalID, map[string]any{"from_destination_id": from, "to_destination_id": d.ID, "payment_authority_id": d.PaymentAuthorityID}); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		until := h.Auth.Service.Now().UTC().Add(PayoutCoolingOff)
		if _, e = tx.Exec(ctx, `INSERT INTO payout_change_requests(id,organisation_id,from_destination_id,to_destination_id,approval_id,cooling_off_until,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, from, d.ID, in.ApprovalID, until, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[PayoutChange](ctx, tx, "payout_change_requests", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		// The event drives the security notification to organisation owners.
		return 201, out, h.opsCommit(ctx, tx, t, ids.Nil, id, "payout_change", "REQUESTED", "", "COOLING_OFF", businessEvents.FinancePayoutChangeUpdated, 1, map[string]any{"from_destination_id": from, "to_destination_id": d.ID, "cooling_off_until": until, "security_notification": true})
	})
}

func (h *Handler) payoutChangeCancel(w http.ResponseWriter, r *http.Request) {
	var in OpsVersionCommand
	if !decode(w, r, &in) {
		return
	}
	id, e := pathCommercialID(r)
	if e != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.financeCommand(w, r, "payout-change:cancel:"+id.String(), "payouts:write", false, in, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target) (int, any, error) {
		tag, e := tx.Exec(ctx, `UPDATE payout_change_requests SET state='CANCELLED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND version=$3 AND state='COOLING_OFF'`, t.Organisation, id, in.Version)
		if e != nil {
			return 0, nil, e
		}
		if tag.RowsAffected() != 1 {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Change version or state has changed.")
		}
		out, e := opsGet[PayoutChange](ctx, tx, "payout_change_requests", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, ids.Nil, id, "payout_change", "CANCELLED", "COOLING_OFF", "CANCELLED", businessEvents.FinancePayoutChangeUpdated, out.Version, map[string]any{"reason": in.Reason})
	})
}

func (h *Handler) revenueSplits(w http.ResponseWriter, r *http.Request) {
	h.financeList(w, r, "finance:read", `SELECT to_jsonb(x) FROM revenue_split_rules x WHERE organisation_id=$1 AND ($2='' OR property_id::text=$2) ORDER BY created_at DESC LIMIT $3 OFFSET $4`, r.URL.Query().Get("property_id"))
}

func (h *Handler) revenueSplitCreate(w http.ResponseWriter, r *http.Request) {
	var in RevenueSplitCreate
	if !decode(w, r, &in) {
		return
	}
	from, e1 := time.Parse(time.DateOnly, in.EffectiveFrom)
	var to any
	ok := e1 == nil
	if in.EffectiveTo != "" {
		end, e := time.Parse(time.DateOnly, in.EffectiveTo)
		ok = ok && e == nil && end.After(from)
		to = in.EffectiveTo
	}
	if !ok || in.PropertyID.ValidateV7() != nil || !opsText(in.ContractReference, 3, 200) || !validCurrency(in.Currency) || finance.ValidateShares(in.Shares) != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, contract, currency, conserving shares (percentages total 10000 bps) and effective dates required."))
		return
	}
	for _, s := range in.Shares {
		if _, e := ids.ParseV7(s.DestinationID); e != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Each share must reference a payout destination."))
			return
		}
	}
	h.financeCommand(w, r, "revenue-split:create", "finance:write", false, in, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target) (int, any, error) {
		raw := marshalCommercial(in.Shares)
		id := ids.New()
		if _, e := tx.Exec(ctx, `INSERT INTO revenue_split_rules(id,organisation_id,property_id,contract_reference,currency,shares,shares_digest,effective_from,effective_to,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, t.Organisation, in.PropertyID, in.ContractReference, in.Currency, raw, security.SHA256Hex(raw), in.EffectiveFrom, to, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[RevenueSplitRule](ctx, tx, "revenue_split_rules", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, in.PropertyID, id, "revenue_split", "CREATED", "", "DRAFT", businessEvents.FinanceSplitRuleUpdated, 1, map[string]any{"shares_digest": out.SharesDigest})
	})
}

func (h *Handler) revenueSplitActivate(w http.ResponseWriter, r *http.Request) {
	var in FinanceApprovalCommand
	if !decode(w, r, &in) {
		return
	}
	id, e := pathCommercialID(r)
	if e != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version, approval and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.financeCommand(w, r, "revenue-split:activate:"+id.String(), "finance:write", true, in, func(ctx context.Context, tx pgx.Tx, t tenant, target workforce.Target) (int, any, error) {
		rule, e := opsLock[RevenueSplitRule](ctx, tx, "revenue_split_rules", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if rule.Version != in.Version || rule.State != "DRAFT" {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Rule version or state has changed.")
		}
		var shares []finance.SplitShare
		if e = json.Unmarshal(rule.Shares, &shares); e != nil {
			return 0, nil, e
		}
		for _, s := range shares {
			var active bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payout_destination_refs WHERE organisation_id=$1 AND id::text=$2 AND state='ACTIVE' AND payment_validation='VERIFIED' AND currency=$3)`, t.Organisation, s.DestinationID, rule.Currency).Scan(&active); e != nil {
				return 0, nil, e
			}
			if !active {
				return 0, nil, apperrors.Conflict("DESTINATION_NOT_PAYMENT_VERIFIED", "Every split destination must be Payment-verified in the rule currency.")
			}
		}
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_REVENUE_SPLIT", target.Scope, in.ApprovalID, map[string]any{"rule_id": rule.ID, "shares_digest": rule.SharesDigest}, "", nil, ""); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE revenue_split_rules SET state='SUPERSEDED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND property_id=$2 AND contract_reference=$3 AND state='ACTIVE'`, t.Organisation, rule.PropertyID, rule.ContractReference); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE revenue_split_rules SET state='ACTIVE',approval_id=$2,version=version+1,updated_at=now() WHERE id=$1`, id, in.ApprovalID); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[RevenueSplitRule](ctx, tx, "revenue_split_rules", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, rule.PropertyID, id, "revenue_split", "ACTIVATED", "DRAFT", "ACTIVE", businessEvents.FinanceSplitRuleUpdated, out.Version, map[string]any{"approval_id": in.ApprovalID, "execution_authority": "payment-service"})
	})
}

// revenueSplitPreview computes the exact conserved allocation; execution of
// any split remains with Payment Service.
func (h *Handler) revenueSplitPreview(w http.ResponseWriter, r *http.Request) {
	var in RevenueSplitPreview
	if !decode(w, r, &in) {
		return
	}
	id, e := pathCommercialID(r)
	if e != nil || in.AmountMinor < 0 || in.AmountMinor > 1e15 {
		if e == nil {
			e = apperrors.InvalidRequest("A non-negative amount is required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "finance:read", "COMPANY", ids.Nil, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rule, e := opsGet[RevenueSplitRule](r.Context(), h.Auth.Service.Pool, "revenue_split_rules", t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var shares []finance.SplitShare
	if e = json.Unmarshal(rule.Shares, &shares); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	allocations, e := finance.Allocate(in.AmountMinor, shares)
	if e != nil {
		reply(w, r, 0, nil, apperrors.Conflict("ALLOCATION_IMPOSSIBLE", e.Error()))
		return
	}
	var sum int64
	for _, a := range allocations {
		sum += a.AmountMinor
	}
	reply(w, r, 200, map[string]any{"rule_id": id, "amount_minor": in.AmountMinor, "currency": rule.Currency, "allocations": allocations, "allocated_minor": sum, "conserved": sum == in.AmountMinor, "execution_authority": "payment-service"}, nil)
}

func (h *Handler) channelSettlements(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h.financeList(w, r, "finance:read", `SELECT to_jsonb(x) FROM channel_settlement_expectations x WHERE organisation_id=$1 AND ($2='' OR state=$2) AND ($3='' OR property_id::text=$3) ORDER BY expected_on,id LIMIT $4 OFFSET $5`, q.Get("state"), q.Get("property_id"))
}

func (h *Handler) channelSettlementCreate(w http.ResponseWriter, r *http.Request) {
	var in ChannelSettlementCreate
	if !decode(w, r, &in) {
		return
	}
	net, ne := finance.NetExpected(in.GrossMinor, in.CommissionMinor, in.PromotionMinor, in.FeesMinor, in.TaxMinor)
	if _, e := time.Parse(time.DateOnly, in.ExpectedOn); e != nil || ne != nil || in.PropertyID.ValidateV7() != nil || in.ConnectionID.ValidateV7() != nil || !opsText(in.ProviderRemittanceReference, 3, 200) || !slices.Contains([]string{"CHANNEL_COLLECT", "VIRTUAL_CARD", "PLATFORM_COLLECT"}, in.CollectionModel) || !validCurrency(in.Currency) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, channel connection, remittance reference, collection model, conserving amounts, currency, expected date and reason required."))
		return
	}
	h.financeCommand(w, r, "channel-settlement:create", "finance:write", false, in, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target) (int, any, error) {
		id := ids.New()
		if _, e := tx.Exec(ctx, `INSERT INTO channel_settlement_expectations(id,organisation_id,property_id,connection_id,reservation_id,provider_remittance_reference,collection_model,gross_minor,commission_minor,promotion_minor,fees_minor,tax_minor,expected_net_minor,currency,expected_on,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, id, t.Organisation, in.PropertyID, in.ConnectionID, in.ReservationID, in.ProviderRemittanceReference, in.CollectionModel, in.GrossMinor, in.CommissionMinor, in.PromotionMinor, in.FeesMinor, in.TaxMinor, net, in.Currency, in.ExpectedOn, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[ChannelSettlement](ctx, tx, "channel_settlement_expectations", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, in.PropertyID, id, "channel_settlement", "EXPECTED", "", "EXPECTED", businessEvents.FinanceSettlementUpdated, 1, map[string]any{"expected_net_minor": net, "currency": in.Currency, "settled": false})
	})
}

func (h *Handler) financialExceptions(w http.ResponseWriter, r *http.Request) {
	h.financeList(w, r, "finance:read", `SELECT to_jsonb(x) FROM financial_exception_cases x WHERE organisation_id=$1 AND ($2='' OR state=$2) ORDER BY created_at DESC LIMIT $3 OFFSET $4`, r.URL.Query().Get("state"))
}

func (h *Handler) financialExceptionResolve(w http.ResponseWriter, r *http.Request) {
	var in FinancialExceptionResolve
	if !decode(w, r, &in) {
		return
	}
	id, e := pathCommercialID(r)
	if e != nil || in.Version < 1 || !slices.Contains([]string{"INVESTIGATING", "RESOLVED", "WRITTEN_OFF"}, in.Outcome) || in.Outcome != "INVESTIGATING" && !opsText(in.Resolution, 5, 2000) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version, outcome and resolution required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.financeCommand(w, r, "exception:resolve:"+id.String(), "finance:approve", in.Outcome == "WRITTEN_OFF", in, func(ctx context.Context, tx pgx.Tx, t tenant, _ workforce.Target) (int, any, error) {
		var state, sourceType string
		var version int64
		var source ids.ID
		if e := tx.QueryRow(ctx, `SELECT state,version,source_type,source_id FROM financial_exception_cases WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, id).Scan(&state, &version, &sourceType, &source); e != nil {
			return 0, nil, opsNotFound(e)
		}
		if version != in.Version || state == "RESOLVED" || state == "WRITTEN_OFF" {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Exception version or state has changed.")
		}
		if _, e := tx.Exec(ctx, `UPDATE financial_exception_cases SET state=$2,resolution=$3,version=version+1,updated_at=now() WHERE id=$1`, id, in.Outcome, in.Resolution); e != nil {
			return 0, nil, e
		}
		if sourceType == "CHANNEL_SETTLEMENT" && in.Outcome != "INVESTIGATING" {
			if _, e := tx.Exec(ctx, `UPDATE channel_settlement_expectations SET state='RESOLVED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND state IN ('MISMATCH','LATE')`, t.Organisation, source); e != nil {
				return 0, nil, e
			}
		}
		out, e := oneJSON(ctx, tx, `SELECT to_jsonb(x) FROM financial_exception_cases x WHERE id=$1`, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, ids.Nil, id, "financial_exception", in.Outcome, state, in.Outcome, businessEvents.FinanceExceptionUpdated, version+1, map[string]any{"resolution": in.Resolution})
	})
}

// financeReport separates expected, verified and unknown values. Verified
// revenue/spend comes only from Payment readback or authoritative financial
// inputs; unknown values are null, never estimated.
func (h *Handler) financeReport(w http.ResponseWriter, r *http.Request) {
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "finance:read", "COMPANY", ids.Nil, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	property := r.URL.Query().Get("property_id")
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT jsonb_build_object('currency',currency,'expected_receivable_minor',sum(amount_minor) FILTER (WHERE direction='RECEIVABLE' AND state NOT IN ('CANCELLED','DRAFT')),'verified_received_minor',sum(settled_minor) FILTER (WHERE direction='RECEIVABLE'),'payable_obligations_minor',sum(amount_minor) FILTER (WHERE direction='PAYABLE' AND state NOT IN ('CANCELLED','DRAFT')),'verified_paid_minor',sum(settled_minor) FILTER (WHERE direction='PAYABLE'),'outstanding_minor',sum(amount_minor-settled_minor) FILTER (WHERE state IN ('APPROVED','PAYMENT_REQUESTED','UNKNOWN_PENDING_READBACK','DISPUTED')),'unknown_outcome_minor',sum(amount_minor) FILTER (WHERE state='UNKNOWN_PENDING_READBACK')) FROM financial_obligations WHERE organisation_id=$1 AND ($2='' OR property_id::text=$2) GROUP BY currency ORDER BY currency`, t.Organisation, property)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	channels, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT jsonb_build_object('connection_id',connection_id,'currency',currency,'expected_gross_minor',sum(gross_minor),'expected_commission_minor',sum(commission_minor),'expected_net_minor',sum(expected_net_minor),'verified_net_minor',sum(actual_net_minor) FILTER (WHERE state IN ('MATCHED','MISMATCH','RESOLVED')),'late',count(*) FILTER (WHERE state='LATE'),'mismatched',count(*) FILTER (WHERE state='MISMATCH')) FROM channel_settlement_expectations WHERE organisation_id=$1 AND ($2='' OR property_id::text=$2) GROUP BY connection_id,currency ORDER BY connection_id`, t.Organisation, property)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	budgets, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT jsonb_build_object('budget_id',b.id,'cost_centre_id',b.cost_centre_id,'currency',b.currency,'total_minor',b.total_minor,'committed_minor',coalesce((SELECT sum(amount_minor) FROM expense_requests x WHERE x.budget_id=b.id AND x.state IN ('APPROVED','OBLIGATION_CREATED')),0),'actual_settled_minor',coalesce((SELECT sum(o.settled_minor) FROM expense_requests x JOIN financial_obligations o ON o.id=x.obligation_id WHERE x.budget_id=b.id),0)) FROM budgets b WHERE b.organisation_id=$1 AND b.state='ACTIVE' ORDER BY b.created_at`, t.Organisation)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var exceptions int
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT count(*) FROM financial_exception_cases WHERE organisation_id=$1 AND state IN ('OPEN','INVESTIGATING')`, t.Organisation).Scan(&exceptions); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	reply(w, r, 200, map[string]any{"obligations": rows, "channel_profitability": channels, "budgets": budgets, "open_exceptions": exceptions, "basis": "VERIFIED_FROM_PAYMENT_READBACK_OR_AUTHORITATIVE_INPUTS_ONLY", "ledger_authority": "payment-service"}, nil)
}

// ProcessFinance runs Section 15 workers: payout change cooling-off with
// Payment re-verification, channel remittance reconciliation and expiry.
func (h *Handler) ProcessFinance(ctx context.Context, limit int) error {
	return errors.Join(h.processPayoutChanges(ctx, limit), h.processSettlements(ctx, limit), h.processExpenseExpiry(ctx, limit))
}

func (h *Handler) processPayoutChanges(ctx context.Context, limit int) error {
	now := h.Auth.Service.Now().UTC()
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT organisation_id,id FROM payout_change_requests WHERE state='COOLING_OFF' AND cooling_off_until<=$1 ORDER BY cooling_off_until LIMIT $2`, now, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, id ids.ID }
	refs := []ref{}
	for rows.Next() {
		var r ref
		if e = rows.Scan(&r.org, &r.id); e != nil {
			rows.Close()
			return e
		}
		refs = append(refs, r)
	}
	rows.Close()
	var failures []error
	for _, r := range refs {
		c, e := opsGet[PayoutChange](ctx, h.Auth.Service.Pool, "payout_change_requests", r.org, r.id)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		d, e := opsGet[PayoutDestination](ctx, h.Auth.Service.Pool, "payout_destination_refs", r.org, c.ToDestinationID)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		validation, _, verr := h.paymentBeneficiary(ctx, d)
		if verr != nil {
			continue // Payment unavailable: the change waits; nothing is assumed
		}
		e = withServiceTx(ctx, h, func(ctx context.Context, tx pgx.Tx) error {
			cur, e := opsLock[PayoutChange](ctx, tx, "payout_change_requests", r.org, r.id)
			if e != nil || cur.State != "COOLING_OFF" {
				return e
			}
			t := workerTenant(r.org)
			if validation != "VERIFIED" {
				if _, e = tx.Exec(ctx, `UPDATE payout_change_requests SET state='BLOCKED',blocked_reason=$2,version=version+1,updated_at=now() WHERE id=$1`, r.id, "PAYMENT_VALIDATION_"+validation); e != nil {
					return e
				}
				return h.opsCommit(ctx, tx, t, ids.Nil, r.id, "payout_change", "BLOCKED", "COOLING_OFF", "BLOCKED", businessEvents.FinancePayoutChangeUpdated, cur.Version+1, map[string]any{"payment_validation": validation})
			}
			if _, e = tx.Exec(ctx, `INSERT INTO financial_profiles(organisation_id,id,default_currency,current_payout_destination_id) VALUES($1,$2,$3,$4) ON CONFLICT (organisation_id) DO UPDATE SET current_payout_destination_id=EXCLUDED.current_payout_destination_id,version=financial_profiles.version+1,updated_at=now()`, r.org, ids.New(), d.Currency, d.ID); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE payout_change_requests SET state='EFFECTIVE',effective_at=now(),version=version+1,updated_at=now() WHERE id=$1`, r.id); e != nil {
				return e
			}
			return h.opsCommit(ctx, tx, t, ids.Nil, r.id, "payout_change", "EFFECTIVE", "COOLING_OFF", "EFFECTIVE", businessEvents.FinancePayoutChangeUpdated, cur.Version+1, map[string]any{"to_destination_id": d.ID, "security_notification": true})
		})
		if e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}

// processSettlements compares expected remittances with authoritative
// financial inputs (Payment/Admin signed). Expected amounts never become
// settled revenue without that evidence.
func (h *Handler) processSettlements(ctx context.Context, limit int) error {
	return withServiceTx(ctx, h, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT e.organisation_id,e.property_id,e.id,e.state,e.version,e.expected_net_minor,e.currency,e.expected_on,
		 (SELECT sum(CASE WHEN f.component='GROSS_REVENUE' THEN f.amount_minor ELSE -f.amount_minor END) FROM channel_financial_inputs f WHERE f.organisation_id=e.organisation_id AND f.connection_id=e.connection_id AND f.source_reference=e.provider_remittance_reference AND f.currency=e.currency AND f.component IN ('GROSS_REVENUE','COMMISSION','PAYMENT_FEE','PROMOTION_COST')),
		 (SELECT count(DISTINCT f.source_service) FROM channel_financial_inputs f WHERE f.organisation_id=e.organisation_id AND f.connection_id=e.connection_id AND f.source_reference=e.provider_remittance_reference AND f.component='GROSS_REVENUE')
		 FROM channel_settlement_expectations e WHERE e.state IN ('EXPECTED','LATE') ORDER BY e.expected_on,e.id LIMIT $1 FOR UPDATE OF e SKIP LOCKED`, limit)
		if e != nil {
			return e
		}
		type item struct {
			org, property, id ids.ID
			state, currency   string
			version, expected int64
			expectedOn        time.Time
			actual            *int64
			sources           int
		}
		items := []item{}
		for rows.Next() {
			var i item
			if e = rows.Scan(&i.org, &i.property, &i.id, &i.state, &i.version, &i.expected, &i.currency, &i.expectedOn, &i.actual, &i.sources); e != nil {
				rows.Close()
				return e
			}
			items = append(items, i)
		}
		rows.Close()
		today := h.Auth.Service.Now().UTC().Truncate(24 * time.Hour)
		for _, i := range items {
			t := workerTenant(i.org)
			var to, kind string
			var delta *int64
			switch {
			case i.actual != nil && i.sources > 1:
				to, kind = "MISMATCH", "DUPLICATE_SETTLEMENT"
			case i.actual != nil && *i.actual == i.expected:
				to = "MATCHED"
			case i.actual != nil:
				to, kind = "MISMATCH", "SETTLEMENT_MISMATCH"
				d := *i.actual - i.expected
				delta = &d
			case i.state == "EXPECTED" && i.expectedOn.Before(today.AddDate(0, 0, -3)):
				to, kind = "LATE", "SETTLEMENT_LATE"
			default:
				continue
			}
			if _, e = tx.Exec(ctx, `UPDATE channel_settlement_expectations SET state=$2,actual_net_minor=$3,reconciled_at=CASE WHEN $3::bigint IS NOT NULL THEN now() END,version=version+1,updated_at=now() WHERE id=$1`, i.id, to, i.actual); e != nil {
				return e
			}
			if kind != "" {
				if _, e = tx.Exec(ctx, `INSERT INTO financial_exception_cases(id,organisation_id,property_id,kind,source_type,source_id,amount_delta_minor,currency) VALUES($1,$2,$3,$4,'CHANNEL_SETTLEMENT',$5,$6,$7) ON CONFLICT DO NOTHING`, ids.New(), i.org, i.property, kind, i.id, delta, i.currency); e != nil {
					return e
				}
			}
			if e = h.opsCommit(ctx, tx, t, i.property, i.id, "channel_settlement", "RECONCILED_"+to, i.state, to, businessEvents.FinanceSettlementUpdated, i.version+1, map[string]any{"expected_net_minor": i.expected, "actual_net_minor": i.actual, "exception": kind}); e != nil {
				return e
			}
		}
		return nil
	})
}

func (h *Handler) processExpenseExpiry(ctx context.Context, limit int) error {
	_, e := h.Auth.Service.Pool.Exec(ctx, `UPDATE expense_requests SET state='EXPIRED',version=version+1,updated_at=now() WHERE id IN (SELECT id FROM expense_requests WHERE state='APPROVED' AND expires_at<now() LIMIT $1)`, limit)
	return e
}
