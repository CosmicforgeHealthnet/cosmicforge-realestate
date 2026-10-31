package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

func (h *Handler) channelMetrics(w http.ResponseWriter, r *http.Request) {
	t, c, e := h.channelTarget(r, "channels:report", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	current, e := h.channelMeasurements(r.Context(), h.Auth.Service.Pool, t.Organisation, c.ID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	history, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(s) FROM channel_metrics_snapshots s WHERE organisation_id=$1 AND connection_id=$2 ORDER BY observed_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, c.ID, limit, offset)
	reply(w, r, 200, map[string]any{"current": current, "history": history, "limit": limit, "offset": offset}, e)
}
func (h *Handler) channelMeasurements(ctx context.Context, q database.Querier, org, connection ids.ID) (any, error) {
	var jobs, success, failed, attempts, quarantined, duplicates, blocked int64
	var meanLatencyMS, p95LatencyMS *float64
	e := q.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE state='SUCCEEDED'),count(*) FILTER(WHERE state IN ('FAILED_FINAL','QUARANTINED')),coalesce(sum(attempts),0),count(*) FILTER(WHERE state='QUARANTINED'),avg(extract(epoch FROM last_success_at-created_at)*1000) FILTER(WHERE state='SUCCEEDED'),percentile_cont(0.95) WITHIN GROUP(ORDER BY extract(epoch FROM last_success_at-created_at)*1000) FILTER(WHERE state='SUCCEEDED') FROM channel_sync_jobs WHERE organisation_id=$1 AND connection_id=$2 AND created_at>=$3`, org, connection, h.Auth.Service.Now().UTC().Add(-24*time.Hour)).Scan(&jobs, &success, &failed, &attempts, &quarantined, &meanLatencyMS, &p95LatencyMS)
	if e != nil {
		return nil, e
	}
	if e = q.QueryRow(ctx, `SELECT coalesce(sum(duplicate_count),0),count(*) FILTER(WHERE state='QUARANTINED') FROM channel_event_receipts WHERE organisation_id=$1 AND connection_id=$2 AND received_at>=$3`, org, connection, h.Auth.Service.Now().UTC().Add(-24*time.Hour)).Scan(&duplicates, &blocked); e != nil {
		return nil, e
	}
	return map[string]any{"window_hours": 24, "jobs": jobs, "successful_jobs": success, "failed_jobs": failed, "attempts": attempts, "quarantined_jobs": quarantined, "duplicate_receipts": duplicates, "quarantined_receipts": blocked, "mean_delivery_latency_ms": meanLatencyMS, "p95_delivery_latency_ms": p95LatencyMS, "as_of": h.Auth.Service.Now().UTC(), "conversion_rate": nil, "occupancy": nil, "reservation_measurements": "UNAVAILABLE_UNTIL_CANONICAL_SECTION9_SOURCE"}, nil
}

type CommercialFinancialInput struct {
	Component       string    `json:"component"`
	AmountMinor     int64     `json:"amount_minor"`
	Currency        string    `json:"currency"`
	SourceService   string    `json:"source_service"`
	SourceReference string    `json:"source_reference"`
	ObservedAt      time.Time `json:"observed_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	Reconciled      bool      `json:"reconciled"`
}

func (h *Handler) channelProfitability(w http.ResponseWriter, r *http.Request) {
	t, c, e := h.channelTarget(r, "channels:report", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	from, to := r.URL.Query().Get("date_from"), r.URL.Query().Get("date_to")
	a, e := time.Parse(time.DateOnly, from)
	b, e2 := time.Parse(time.DateOnly, to)
	currency, e3 := money.ParseCurrency(r.URL.Query().Get("currency"))
	if e != nil || e2 != nil || e3 != nil || !b.After(a) || b.After(a.AddDate(1, 0, 0)) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("An exact reporting period up to one year and explicit currency are required."))
		return
	}
	filters := []string{r.URL.Query().Get("product_id"), r.URL.Query().Get("rate_plan_id"), r.URL.Query().Get("promotion_id")}
	for _, raw := range filters {
		if raw != "" {
			if _, e = ids.ParseV7(raw); e != nil {
				reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid canonical financial-report filter."))
				return
			}
		}
	}
	rows, e := h.Auth.Service.Pool.Query(r.Context(), `SELECT component,amount_minor,currency,source_service,source_reference,observed_at,expires_at,reconciled FROM channel_financial_inputs WHERE organisation_id=$1 AND connection_id=$2 AND period_from=$3::date AND period_to=$4::date AND currency=$5 AND ($6='' OR product_id::text=$6) AND ($7='' OR rate_plan_id::text=$7) AND ($8='' OR promotion_id::text=$8) ORDER BY component,source_service,source_reference`, t.Organisation, c.ID, from, to, currency.String(), filters[0], filters[1], filters[2])
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	inputs := []CommercialFinancialInput{}
	for rows.Next() {
		var v CommercialFinancialInput
		if e = rows.Scan(&v.Component, &v.AmountMinor, &v.Currency, &v.SourceService, &v.SourceReference, &v.ObservedAt, &v.ExpiresAt, &v.Reconciled); e != nil {
			break
		}
		inputs = append(inputs, v)
	}
	rowErr := rows.Err()
	rows.Close()
	if e == nil {
		e = rowErr
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	report, e := commercialProfitability(inputs, currency.String(), h.Auth.Service.Now().UTC())
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	report["connection_id"] = c.ID
	report["property_id"] = c.PropertyID
	report["period_from"] = from
	report["period_to"] = to
	report["filters"] = map[string]string{"product_id": filters[0], "rate_plan_id": filters[1], "promotion_id": filters[2]}
	reply(w, r, 200, report, nil)
}
func commercialProfitability(inputs []CommercialFinancialInput, currency string, now time.Time) (map[string]any, error) {
	components := []string{"GROSS_REVENUE", "COMMISSION", "PAYMENT_FEE", "PROMOTION_COST", "OPERATIONAL_COST"}
	sums := map[string]int64{}
	counts := map[string]int{}
	stale := []string{}
	unreconciled := []string{}
	provenance := []CommercialFinancialInput{}
	for _, v := range inputs {
		if !slices.Contains(components, v.Component) || v.Currency != currency || v.AmountMinor < 0 || v.SourceReference == "" || v.SourceService == "" {
			return nil, apperrors.Conflict("FINANCIAL_INPUT_INVALID", "A source-backed reconciled component has inconsistent currency, amount or provenance.")
		}
		if !now.Before(v.ExpiresAt) || v.ObservedAt.After(now) {
			stale = append(stale, v.Component+":"+v.SourceReference)
			continue
		}
		if !v.Reconciled {
			unreconciled = append(unreconciled, v.Component+":"+v.SourceReference)
			continue
		}
		amount, e := addMinor(sums[v.Component], v.AmountMinor, currency)
		if e != nil {
			return nil, e
		}
		sums[v.Component] = amount
		counts[v.Component]++
		provenance = append(provenance, v)
	}
	values := map[string]*int64{}
	missing := []string{}
	for _, name := range components {
		if counts[name] == 0 {
			values[name] = nil
			missing = append(missing, name)
		} else {
			amount := sums[name]
			values[name] = &amount
		}
	}
	var net *int64
	status := "INCOMPLETE_INPUTS"
	if len(missing) == 0 && len(stale) == 0 && len(unreconciled) == 0 {
		value, e := money.New(sums["GROSS_REVENUE"], currency)
		if e != nil {
			return nil, e
		}
		for _, name := range components[1:] {
			cost, e := money.New(sums[name], currency)
			if e != nil {
				return nil, e
			}
			value, e = value.Sub(cost)
			if e != nil {
				return nil, e
			}
		}
		amount := value.AmountMinor
		net = &amount
		status = "RECONCILED_SOURCE_INPUTS"
	}
	return map[string]any{"status": status, "currency": currency, "components_minor": values, "net_contribution_minor": net, "missing_components": missing, "stale_inputs": stale, "unreconciled_inputs": unreconciled, "provenance": provenance, "as_of": now, "financial_execution": false, "formula": "GROSS_REVENUE - COMMISSION - PAYMENT_FEE - PROMOTION_COST - OPERATIONAL_COST"}, nil
}
func marshalCommercial(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
