package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type RMSMarketInputCommand struct {
	PropertyID       ids.ID    `json:"property_id"`
	Kind             string    `json:"kind"`
	Channel          string    `json:"channel,omitempty"`
	DateFrom         string    `json:"date_from"`
	DateTo           string    `json:"date_to"`
	ValueMinor       *int64    `json:"value_minor,omitempty"`
	ValueBPS         *int64    `json:"value_bps,omitempty"`
	Currency         string    `json:"currency,omitempty"`
	Label            string    `json:"label"`
	SourceReference  string    `json:"source_reference"`
	LicenceReference string    `json:"licence_reference"`
	EvidenceDigest   string    `json:"evidence_digest"`
	ObservedAt       time.Time `json:"observed_at"`
}
type RMSAnalyticsCommand struct {
	ScopeKind   string `json:"scope_kind"`
	ScopeID     ids.ID `json:"scope_id"`
	DateFrom    string `json:"date_from"`
	DateTo      string `json:"date_to"`
	HistoryDays int    `json:"history_days"`
}
type RMSAnalyticsSnapshot struct {
	ID           ids.ID                  `json:"id"`
	ScopeKind    string                  `json:"scope_kind"`
	ScopeID      ids.ID                  `json:"scope_id"`
	PropertyIDs  []ids.ID                `json:"property_ids"`
	ModelID      ids.ID                  `json:"model_id"`
	DateFrom     string                  `json:"date_from"`
	DateTo       string                  `json:"date_to"`
	AsOf         time.Time               `json:"as_of"`
	InputDigest  string                  `json:"input_digest"`
	ResultDigest string                  `json:"result_digest"`
	Result       revenue.AnalyticsResult `json:"result"`
	Provenance   json.RawMessage         `json:"provenance"`
}
type RMSScenarioCommand struct {
	ForecastID   ids.ID                        `json:"forecast_id"`
	RatePlanID   ids.ID                        `json:"rate_plan_id"`
	AnalyticsID  *ids.ID                       `json:"analytics_id,omitempty"`
	Alternatives []revenue.ScenarioAlternative `json:"alternatives"`
}
type RMSScenarioResult struct {
	ForecastID   ids.ID                     `json:"forecast_id"`
	SourceDigest string                     `json:"source_digest"`
	InputDigest  string                     `json:"input_digest"`
	Simulation   revenue.ScenarioSimulation `json:"simulation"`
	Mutated      bool                       `json:"mutated"`
}

func (h *Handler) registerRMSAnalytics(r chi.Router) {
	r.Get("/revenue/market-inputs", h.rmsMarketInputs)
	r.Post("/revenue/market-inputs", h.rmsMarketInputCreate)
	r.Get("/revenue/analytics", h.rmsAnalyticsList)
	r.Post("/revenue/analytics", h.rmsAnalyticsCreate)
	r.Get("/revenue/analytics/{id}", h.rmsAnalyticsGet)
	r.Post("/revenue/scenario-simulations", h.rmsScenarioSimulate)
	r.Get("/revenue/models", h.rmsModels)
}

func (h *Handler) rmsMarketInputs(w http.ResponseWriter, r *http.Request) {
	h.rmsAutomationScopedList(w, r, "rms_market_inputs", "date_from DESC,id DESC")
}

// rmsMarketInputCreate records a governed external input. Licence and source
// references are mandatory; nothing is fetched or invented here.
func (h *Handler) rmsMarketInputCreate(w http.ResponseWriter, r *http.Request) {
	var in RMSMarketInputCommand
	if !decode(w, r, &in) {
		return
	}
	kinds := []string{"HOLIDAY", "LOCAL_EVENT", "COMPETITOR_RATE", "MARKET_DEMAND_INDEX", "CHANNEL_COMMISSION", "OPERATING_COST"}
	valid := in.PropertyID.ValidateV7() == nil && slices.Contains(kinds, in.Kind) && inventory.ValidChannel(in.Channel) && validStaffReason(in.SourceReference) && validStaffReason(in.LicenceReference) && len(in.Label) >= 1 && len(in.Label) <= 200 && len(in.EvidenceDigest) == 64 && !in.ObservedAt.IsZero()
	if _, e := (inventory.DateRange{DateFrom: in.DateFrom, DateTo: in.DateTo}).Days("UTC"); e != nil {
		valid = false
	}
	switch in.Kind {
	case "COMPETITOR_RATE", "OPERATING_COST":
		valid = valid && in.ValueMinor != nil && *in.ValueMinor >= 0 && in.ValueBPS == nil
		if _, e := money.ParseCurrency(in.Currency); e != nil {
			valid = false
		}
	case "CHANNEL_COMMISSION", "MARKET_DEMAND_INDEX":
		valid = valid && in.ValueBPS != nil && *in.ValueBPS >= -9000 && *in.ValueBPS <= 10000 && in.ValueMinor == nil && in.Currency == ""
		valid = valid && (in.Kind != "CHANNEL_COMMISSION" || in.Channel != "" && *in.ValueBPS >= 0)
	default:
		valid = valid && in.ValueMinor == nil && in.ValueBPS == nil && in.Currency == ""
	}
	if !valid {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Kind-specific value, civil dates, source and licence references and a SHA-256 evidence digest are required."))
		return
	}
	h.execute(w, r, "revenue:market-input:"+in.PropertyID.String(), in, "rms:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "rms:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		property, e := portfolio.Get(ctx, tx, t.Organisation, in.PropertyID)
		if e != nil {
			return 0, nil, e
		}
		if in.Currency != "" && in.Currency != property.Currency {
			return 0, nil, apperrors.InvalidRequest("Monetary inputs must use the property currency.")
		}
		if in.ObservedAt.After(h.Auth.Service.Now().UTC().Add(time.Minute)) {
			return 0, nil, apperrors.InvalidRequest("An observation cannot be dated in the future.")
		}
		id := ids.New()
		var currency any
		if in.Currency != "" {
			currency = in.Currency
		}
		if _, e = tx.Exec(ctx, `INSERT INTO rms_market_inputs(id,organisation_id,property_id,kind,channel,date_from,date_to,value_minor,value_bps,currency,label,source_reference,licence_reference,evidence_digest,observed_at,created_by) VALUES($1,$2,$3,$4,$5,$6::date,$7::date,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, id, t.Organisation, in.PropertyID, in.Kind, in.Channel, in.DateFrom, in.DateTo, in.ValueMinor, in.ValueBPS, currency, in.Label, in.SourceReference, in.LicenceReference, in.EvidenceDigest, in.ObservedAt.UTC(), t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "rms_market_input", "RMS_MARKET_INPUT_RECORDED", "revenue.market_input.recorded.v1", 1, map[string]any{"kind": in.Kind, "channel": in.Channel, "date_from": in.DateFrom, "date_to": in.DateTo, "source_reference": in.SourceReference, "licence_reference": in.LicenceReference, "rates_changed": false}); e != nil {
			return 0, nil, e
		}
		out, e := oneJSON(ctx, tx, `SELECT to_jsonb(m) FROM rms_market_inputs m WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		return 201, out, e
	})
}

func (h *Handler) rmsAnalyticsScope(ctx context.Context, q database.Querier, t tenant, kind string, id ids.ID, capability string) ([]ids.ID, error) {
	switch kind {
	case "PROPERTY":
		if _, _, e := h.authorize(ctx, q, capability, "PROPERTY", id, false); e != nil {
			return nil, e
		}
		return []ids.ID{id}, nil
	case "BRANCH":
		if _, _, e := h.authorize(ctx, q, capability, "BRANCH", id, false); e != nil {
			return nil, e
		}
		return h.operationalProperties(ctx, q, t, capability, "", id.String())
	case "PORTFOLIO":
		if id != t.Organisation {
			return nil, apperrors.PermissionDenied("Portfolio scope is the current organisation.")
		}
		if _, _, e := h.authorize(ctx, q, capability, "COMPANY", id, false); e != nil {
			return nil, e
		}
		return h.operationalProperties(ctx, q, t, capability, "", "")
	}
	return nil, apperrors.InvalidRequest("Unsupported analytics scope.")
}

// rmsAnalyticsInput gathers canonical calendar, booking and governed market
// facts for every property in scope. Missing facts remain missing.
func (h *Handler) rmsAnalyticsInput(ctx context.Context, tx pgx.Tx, t tenant, in RMSAnalyticsCommand, properties []ids.ID) (revenue.AnalyticsInput, error) {
	out := revenue.AnalyticsInput{ScopeKind: in.ScopeKind, ScopeID: in.ScopeID, AsOf: h.Auth.Service.Now().UTC().Truncate(time.Microsecond), DateFrom: in.DateFrom, DateTo: in.DateTo, Properties: []revenue.PropertyDemand{}, Bookings: []revenue.BookingFact{}, MarketInputs: []revenue.MarketInput{}}
	slices.SortFunc(properties, func(a, b ids.ID) int {
		if a.String() < b.String() {
			return -1
		}
		if a == b {
			return 0
		}
		return 1
	})
	for i, id := range properties {
		property, e := portfolio.Get(ctx, tx, t.Organisation, id)
		if e != nil {
			return out, e
		}
		loc, e := time.LoadLocation(property.Timezone)
		if e != nil {
			return out, e
		}
		today, _ := time.Parse(time.DateOnly, out.AsOf.In(loc).Format(time.DateOnly))
		if i == 0 {
			out.Today = today.Format(time.DateOnly)
			out.HistoryFrom = today.AddDate(0, 0, -in.HistoryDays).Format(time.DateOnly)
		}
		pools, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(p) FROM inventory_pools p WHERE organisation_id=$1 AND property_id=$2 AND state='ACTIVE' ORDER BY id`, t.Organisation, id)
		if e != nil {
			return out, e
		}
		byDay := map[string]revenue.DemandDay{}
		for _, raw := range pools {
			var pool inventory.Pool
			if e = json.Unmarshal(raw, &pool); e != nil {
				return out, e
			}
			data, _, e := h.captureRMSDemand(ctx, tx, t, pool, property, RMSDemandCommand{PropertyID: id, PoolID: pool.ID, DateFrom: in.DateFrom, DateTo: in.DateTo, HistoryDays: in.HistoryDays})
			if e != nil {
				return out, e
			}
			for _, d := range data.Days {
				a, existed := byDay[d.Day]
				a.Day = d.Day
				a.Capacity += d.Capacity
				a.Blocked += d.Blocked
				a.ConfirmedNights += d.ConfirmedNights
				a.HeldNights += d.HeldNights
				a.OverbookedNights += d.OverbookedNights
				a.SellableNights += d.SellableNights
				a.CalendarVersion = max(a.CalendarVersion, d.CalendarVersion)
				switch {
				case !existed:
					a.AccommodationMinor = d.AccommodationMinor
				case a.AccommodationMinor != nil && d.AccommodationMinor != nil:
					v := *a.AccommodationMinor + *d.AccommodationMinor
					a.AccommodationMinor = &v
				default:
					a.AccommodationMinor = nil
				}
				byDay[d.Day] = a
			}
		}
		days := []revenue.DemandDay{}
		for _, d := range byDay {
			days = append(days, d)
		}
		slices.SortFunc(days, func(a, b revenue.DemandDay) int {
			if a.Day < b.Day {
				return -1
			}
			if a.Day == b.Day {
				return 0
			}
			return 1
		})
		out.Properties = append(out.Properties, revenue.PropertyDemand{PropertyID: id, BranchID: property.BranchID, Currency: property.Currency, Timezone: property.Timezone, Days: days})
	}
	rows, e := commercialJSONRows(ctx, tx, `SELECT jsonb_build_object('room_id',r.id,'property_id',r.property_id,'product_id',r.product_id,'channel',p.channel,'source',p.source,'state',p.state,'booked_at',r.accepted_at,'confirmed_at',p.confirmed_at,'cancelled_at',p.cancelled_at,'date_from',r.date_from,'date_to',r.date_to,'quantity',r.quantity,'accommodation_minor',r.accommodation_minor,'currency',r.currency) FROM reservation_rooms r JOIN reservations p ON p.organisation_id=r.organisation_id AND p.id=r.reservation_id WHERE r.organisation_id=$1 AND r.property_id=ANY($2::uuid[]) AND r.state IN ('ACTIVE','CANCELLED','COMPLETED') AND r.date_to>$3::date AND r.date_from<$4::date ORDER BY r.id LIMIT 200001`, t.Organisation, properties, out.HistoryFrom, in.DateTo)
	if e != nil {
		return out, e
	}
	if len(rows) > 200000 {
		return out, apperrors.Conflict("RMS_ANALYTICS_SCOPE_TOO_LARGE", "Narrow the analytics scope or history window.")
	}
	for _, raw := range rows {
		var b revenue.BookingFact
		if e = json.Unmarshal(raw, &b); e != nil {
			return out, e
		}
		b.BookedAt = b.BookedAt.UTC()
		out.Bookings = append(out.Bookings, b)
	}
	inputs, e := commercialJSONRows(ctx, tx, `SELECT jsonb_build_object('kind',kind,'channel',channel,'date_from',date_from,'date_to',date_to,'value_minor',value_minor,'value_bps',value_bps,'currency',currency,'label',label,'source_reference',source_reference) FROM rms_market_inputs WHERE organisation_id=$1 AND property_id=ANY($2::uuid[]) AND date_to>$3::date AND date_from<$4::date AND observed_at<=$5 ORDER BY kind,date_from,id`, t.Organisation, properties, out.HistoryFrom, in.DateTo, out.AsOf)
	if e != nil {
		return out, e
	}
	for _, raw := range inputs {
		var m revenue.MarketInput
		if e = json.Unmarshal(raw, &m); e != nil {
			return out, e
		}
		out.MarketInputs = append(out.MarketInputs, m)
	}
	return out, nil
}

func (h *Handler) rmsAnalyticsCreate(w http.ResponseWriter, r *http.Request) {
	var in RMSAnalyticsCommand
	if !decode(w, r, &in) {
		return
	}
	if in.HistoryDays == 0 {
		in.HistoryDays = 365
	}
	if in.ScopeID.ValidateV7() != nil || !slices.Contains([]string{"PROPERTY", "BRANCH", "PORTFOLIO"}, in.ScopeKind) || in.HistoryDays < 21 || in.HistoryDays > 366 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, branch or portfolio scope, target civil dates and 21-366 history days required."))
		return
	}
	if _, e := (inventory.DateRange{DateFrom: in.DateFrom, DateTo: in.DateTo}).Days("UTC"); e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid target civil dates required."))
		return
	}
	kind := in.ScopeKind
	if kind == "PORTFOLIO" {
		kind = "COMPANY"
	}
	h.execute(w, r, "revenue:analytics:"+in.ScopeID.String(), in, "rms:write", kind, in.ScopeID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, e := h.load(ctx, tx)
		if e != nil {
			return 0, nil, e
		}
		properties, e := h.rmsAnalyticsScope(ctx, tx, t, in.ScopeKind, in.ScopeID, "rms:write")
		if e != nil {
			return 0, nil, e
		}
		if len(properties) == 0 {
			return 0, nil, apperrors.PermissionDenied("No property in scope is authorized for RMS analytics.")
		}
		input, e := h.rmsAnalyticsInput(ctx, tx, t, in, properties)
		if e != nil {
			return 0, nil, e
		}
		result, e := revenue.AnalyzeDemand(input)
		if e != nil {
			return 0, nil, apperrors.Conflict("RMS_ANALYTICS_SOURCE_INVALID", "Retained facts cannot support this analytics scope and horizon.")
		}
		var model ids.ID
		if e = tx.QueryRow(ctx, `SELECT id FROM forecast_models WHERE model_key=$1 AND version=1`, revenue.AnalyticsModel).Scan(&model); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		inputDigest, resultDigest := hashCommercialPayload(marshalCommercial(input)), hashCommercialPayload(marshalCommercial(result))
		provenance := map[string]any{"source_service": "business-operations-service", "source_tables": []string{"inventory_calendar", "inventory_holds", "reservation_inventory_days", "reservation_rooms", "reservations", "rms_market_inputs"}, "model": revenue.AnalyticsModel, "model_version": 1, "history_from": input.HistoryFrom, "property_count": len(properties), "booking_lines": len(input.Bookings), "market_inputs": len(input.MarketInputs), "reproducibility": "result_digest is a deterministic function of input_digest and model version", "realized_financial_truth": false}
		if _, e = tx.Exec(ctx, `INSERT INTO rms_analytics_snapshots(id,organisation_id,scope_kind,scope_id,property_ids,model_id,date_from,date_to,as_of,input_digest,result_digest,result,provenance,created_by) VALUES($1,$2,$3,$4,$5,$6,$7::date,$8::date,$9,$10,$11,$12,$13,$14)`, id, t.Organisation, in.ScopeKind, in.ScopeID, properties, model, in.DateFrom, in.DateTo, input.AsOf, inputDigest, resultDigest, marshalCommercial(result), marshalCommercial(provenance), t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, properties[0], id, "rms_analytics_snapshot", "RMS_ANALYTICS_CREATED", "revenue.analytics.snapshot.created.v1", 1, map[string]any{"scope_kind": in.ScopeKind, "scope_id": in.ScopeID, "input_digest": inputDigest, "result_digest": resultDigest, "status": result.Status, "rates_changed": false}); e != nil {
			return 0, nil, e
		}
		out, e := h.loadRMSAnalytics(ctx, tx, t.Organisation, id)
		return 201, out, e
	})
}

func (h *Handler) loadRMSAnalytics(ctx context.Context, q database.Querier, org, id ids.ID) (RMSAnalyticsSnapshot, error) {
	var raw []byte
	e := q.QueryRow(ctx, `SELECT to_jsonb(a) FROM rms_analytics_snapshots a WHERE organisation_id=$1 AND id=$2`, org, id).Scan(&raw)
	return decodeRMSRecord[RMSAnalyticsSnapshot](raw, e)
}

func (h *Handler) rmsAnalyticsAuthorized(ctx context.Context, q database.Querier, v RMSAnalyticsSnapshot) error {
	for _, id := range v.PropertyIDs {
		if _, _, e := h.authorize(ctx, q, "rms:read", "PROPERTY", id, false); e != nil {
			return e
		}
	}
	return nil
}
func (h *Handler) rmsAnalyticsGet(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	v, e := h.loadRMSAnalytics(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e == nil {
		e = h.rmsAnalyticsAuthorized(r.Context(), h.Auth.Service.Pool, v)
	}
	reply(w, r, 200, v, e)
}
func (h *Handler) rmsAnalyticsList(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	properties, e := h.operationalProperties(r.Context(), h.Auth.Service.Pool, t, "rms:read", "", "")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(a)-'result' FROM rms_analytics_snapshots a WHERE organisation_id=$1 AND property_ids<@$2::uuid[] ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, properties, limit, offset)
	if items == nil {
		items = []json.RawMessage{}
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}

// rmsScenarioSimulate is a read-only projection; it writes nothing.
func (h *Handler) rmsScenarioSimulate(w http.ResponseWriter, r *http.Request) {
	var in RMSScenarioCommand
	if !decode(w, r, &in) {
		return
	}
	if in.ForecastID.ValidateV7() != nil || in.RatePlanID.ValidateV7() != nil || in.AnalyticsID != nil && in.AnalyticsID.ValidateV7() != nil || len(in.Alternatives) < 1 || len(in.Alternatives) > 20 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Retained forecast, canonical rate plan and 1-20 scenario alternatives required."))
		return
	}
	ctx := r.Context()
	t, e := h.load(ctx, h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out, e := func() (RMSScenarioResult, error) {
		f, e := loadRMSForecast(ctx, h.Auth.Service.Pool, t.Organisation, in.ForecastID)
		if e != nil {
			return RMSScenarioResult{}, e
		}
		if _, _, e = h.authorize(ctx, h.Auth.Service.Pool, "rms:read", "PROPERTY", f.PropertyID, false); e != nil {
			return RMSScenarioResult{}, e
		}
		plan, e := revenue.GetPlan(ctx, h.Auth.Service.Pool, t.Organisation, in.RatePlanID)
		if e != nil {
			return RMSScenarioResult{}, e
		}
		if plan.PropertyID != f.PropertyID {
			return RMSScenarioResult{}, apperrors.PermissionDenied("Rate plan and forecast must belong to one property.")
		}
		if f.Status != "BASELINE_READY" {
			return RMSScenarioResult{}, apperrors.Conflict("RMS_SIMULATION_UNSUPPORTED", "A baseline-ready forecast is required.")
		}
		property, e := portfolio.Get(ctx, h.Auth.Service.Pool, t.Organisation, f.PropertyID)
		if e != nil {
			return RMSScenarioResult{}, e
		}
		input := revenue.ScenarioInput{Currency: property.Currency, Days: []revenue.ScenarioDay{}, BaselineChannelMix: []revenue.ChannelShare{}, CommissionBPS: map[string]int64{}, Alternatives: in.Alternatives}
		for _, d := range f.Result.Days {
			version, e := revenue.VersionForDay(ctx, h.Auth.Service.Pool, t.Organisation, plan.ID, d.Day)
			if e != nil {
				return RMSScenarioResult{}, e
			}
			if version.Currency != property.Currency || d.ProjectedSoldMilliNights == nil {
				return RMSScenarioResult{}, apperrors.Conflict("RMS_SIMULATION_UNSUPPORTED", "Single-currency current rates and projected nights are required.")
			}
			input.Days = append(input.Days, revenue.ScenarioDay{Day: d.Day, PhysicalCapacity: d.PhysicalCapacity, ProjectedMilliNights: *d.ProjectedSoldMilliNights, CurrentAmountMinor: version.Definition.AmountMinor})
		}
		if in.AnalyticsID != nil {
			a, e := h.loadRMSAnalytics(ctx, h.Auth.Service.Pool, t.Organisation, *in.AnalyticsID)
			if e != nil {
				return RMSScenarioResult{}, e
			}
			if e = h.rmsAnalyticsAuthorized(ctx, h.Auth.Service.Pool, a); e != nil {
				return RMSScenarioResult{}, e
			}
			if !slices.Contains(a.PropertyIDs, f.PropertyID) {
				return RMSScenarioResult{}, apperrors.PermissionDenied("Analytics snapshot must cover the forecast property.")
			}
			var total int64
			for _, c := range a.Result.Channels {
				if c.ShareBPS != nil {
					input.BaselineChannelMix = append(input.BaselineChannelMix, revenue.ChannelShare{Channel: c.Key, ShareBPS: *c.ShareBPS})
					total += *c.ShareBPS
				}
			}
			// Rounded shares are normalised onto the largest channel so the mix sums exactly.
			if len(input.BaselineChannelMix) > 0 && total != 10000 {
				input.BaselineChannelMix[0].ShareBPS += 10000 - total
			}
			input.CurrentAverageLOSMilli = a.Result.AverageLOSMilliNights
		}
		inputs, e := commercialJSONRows(ctx, h.Auth.Service.Pool, `SELECT jsonb_build_object('kind',kind,'channel',channel,'value_minor',value_minor,'value_bps',value_bps) FROM rms_market_inputs WHERE organisation_id=$1 AND property_id=$2 AND kind IN ('CHANNEL_COMMISSION','OPERATING_COST') AND date_from<=$3::date AND date_to>=$4::date ORDER BY observed_at DESC,id DESC`, t.Organisation, f.PropertyID, f.DateFrom, f.DateTo)
		if e != nil {
			return RMSScenarioResult{}, e
		}
		for _, raw := range inputs {
			var m struct {
				Kind       string `json:"kind"`
				Channel    string `json:"channel"`
				ValueMinor *int64 `json:"value_minor"`
				ValueBPS   *int64 `json:"value_bps"`
			}
			if e = json.Unmarshal(raw, &m); e != nil {
				return RMSScenarioResult{}, e
			}
			if _, seen := input.CommissionBPS[m.Channel]; m.Kind == "CHANNEL_COMMISSION" && m.ValueBPS != nil && !seen {
				input.CommissionBPS[m.Channel] = *m.ValueBPS
			}
			if m.Kind == "OPERATING_COST" && m.ValueMinor != nil && input.OperatingCostPerNightMinor == nil {
				input.OperatingCostPerNightMinor = m.ValueMinor
			}
		}
		simulation, e := revenue.SimulateScenarios(input)
		if e != nil {
			return RMSScenarioResult{}, apperrors.InvalidRequest("Scenario alternatives are invalid for this retained forecast.")
		}
		return RMSScenarioResult{ForecastID: f.ID, SourceDigest: f.SourceDigest, InputDigest: hashCommercialPayload(marshalCommercial(input)), Simulation: simulation, Mutated: false}, nil
	}()
	reply(w, r, 200, out, e)
}

func (h *Handler) rmsModels(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	properties, e := h.operationalProperties(r.Context(), h.Auth.Service.Pool, t, "rms:read", "", "")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if len(properties) == 0 {
		reply(w, r, 0, nil, apperrors.PermissionDenied("RMS read authority required."))
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(m) FROM forecast_models m ORDER BY model_key,version`)
	if items == nil && !errors.Is(e, pgx.ErrNoRows) {
		items = []json.RawMessage{}
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": len(items), "offset": 0}, e)
}
