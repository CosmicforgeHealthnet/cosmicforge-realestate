package businesshttp

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"sort"
	"time"
)

type RMSDemandCommand struct {
	PropertyID  ids.ID `json:"property_id"`
	PoolID      ids.ID `json:"pool_id"`
	DateFrom    string `json:"date_from"`
	DateTo      string `json:"date_to"`
	HistoryDays int    `json:"history_days"`
}
type RMSDemandSnapshot struct {
	ID           ids.ID                `json:"id"`
	PropertyID   ids.ID                `json:"property_id"`
	PoolID       ids.ID                `json:"pool_id"`
	DateFrom     string                `json:"date_from"`
	DateTo       string                `json:"date_to"`
	AsOf         time.Time             `json:"as_of"`
	SourceDigest string                `json:"source_digest"`
	Dataset      revenue.DemandDataset `json:"dataset"`
	Provenance   json.RawMessage       `json:"provenance"`
}

func (h *Handler) registerRMSObservations(r chi.Router) {
	r.Get("/revenue/demand-snapshots", h.rmsDemandList)
	r.Post("/revenue/demand-snapshots", h.rmsDemandCreate)
	r.Get("/revenue/demand-snapshots/{id}", h.rmsDemandGet)
	r.Get("/revenue/demand-snapshots/{id}/pace", h.rmsPaceGet)
	r.Get("/revenue/metrics", h.rmsMetricsGet)
	h.registerRMSForecasts(r)
	h.registerRMSPolicies(r)
}
func (h *Handler) rmsDemandCreate(w http.ResponseWriter, r *http.Request) {
	var in RMSDemandCommand
	if !decode(w, r, &in) {
		return
	}
	if in.HistoryDays == 0 {
		in.HistoryDays = 56
	}
	if in.PropertyID.ValidateV7() != nil || in.PoolID.ValidateV7() != nil || in.HistoryDays < 21 || in.HistoryDays > 366 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, physical pool, target civil dates and 21–366 observed history days required."))
		return
	}
	h.execute(w, r, "revenue:demand-snapshot:"+in.PoolID.String(), in, "rms:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "rms:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		if e = h.commercialGate(ctx, tx, t, in.PropertyID); e != nil {
			return 0, nil, e
		}
		pool, e := inventory.GetPool(ctx, tx, t.Organisation, in.PoolID)
		if e != nil {
			return 0, nil, e
		}
		if pool.PropertyID != in.PropertyID {
			return 0, nil, apperrors.PermissionDenied("Physical pool belongs to another property.")
		}
		if e = inventory.CheckStockAuthority(ctx, tx, pool); e != nil {
			return 0, nil, e
		}
		property, e := portfolio.Get(ctx, tx, t.Organisation, in.PropertyID)
		if e != nil {
			return 0, nil, e
		}
		data, historyFrom, e := h.captureRMSDemand(ctx, tx, t, pool, property, in)
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		digest := hashCommercialPayload(marshalCommercial(data))
		provenance := map[string]any{"source_service": "business-operations-service", "source_tables": []string{"inventory_calendar", "inventory_holds", "reservation_inventory_days", "reservation_rooms", "reservations"}, "scope": "PHYSICAL_POOL", "pool_version": pool.Version, "property_version": property.Version, "history_from": historyFrom, "historical_calendar_filled": false, "room_revenue_attribution": "EXACT_ACCEPTED_ACCOMMODATION_ALLOCATED_BY_FROZEN_NIGHT_WEIGHTS", "realized_financial_truth": false, "dataset_schema_version": 1}
		if _, e = tx.Exec(ctx, `INSERT INTO demand_snapshots(id,organisation_id,property_id,pool_id,date_from,date_to,as_of,currency,source_digest,dataset,provenance,created_by) VALUES($1,$2,$3,$4,$5::date,$6::date,$7,$8,$9,$10,$11,$12)`, id, t.Organisation, in.PropertyID, pool.ID, in.DateFrom, in.DateTo, data.AsOf, data.Currency, digest, marshalCommercial(data), marshalCommercial(provenance), t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.captureRMSPace(ctx, tx, t, id, in, data); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "demand_snapshot", "RMS_DEMAND_CAPTURED", "revenue.demand_snapshot.created.v1", 1, map[string]any{"snapshot_id": id, "pool_id": pool.ID, "source_digest": digest, "as_of": data.AsOf, "date_from": in.DateFrom, "date_to": in.DateTo, "observed_calendar_days": len(data.Days), "realized_financial_truth": false}); e != nil {
			return 0, nil, e
		}
		out, e := loadRMSDemandSnapshot(ctx, tx, t.Organisation, id)
		return 201, out, e
	})
}
func (h *Handler) captureRMSDemand(ctx context.Context, tx pgx.Tx, t tenant, pool inventory.Pool, property portfolio.Node, in RMSDemandCommand) (revenue.DemandDataset, string, error) {
	data := revenue.DemandDataset{Currency: property.Currency, Timezone: property.Timezone, Basis: "CONFIRMED_COMMITTED_DEMAND", AsOf: h.Auth.Service.Now().UTC().Truncate(time.Microsecond), Days: []revenue.DemandDay{}, MissingInputs: []string{"realized_stays", "payment_refund_reconciled_revenue", "marketplace_conversion_attribution", "calibrated_price_elasticity", "verified_market_holiday_event_inputs"}}
	loc, e := time.LoadLocation(property.Timezone)
	if e != nil {
		return data, "", e
	}
	today, _ := time.Parse(time.DateOnly, data.AsOf.In(loc).Format(time.DateOnly))
	from, e := time.Parse(time.DateOnly, in.DateFrom)
	if e != nil {
		return data, "", apperrors.InvalidRequest("Target dates use YYYY-MM-DD.")
	}
	to, e := time.Parse(time.DateOnly, in.DateTo)
	if e != nil || from.Before(today) || !from.Before(to) || to.After(today.AddDate(0, 0, 90)) {
		return data, "", apperrors.InvalidRequest("Target horizon must be within the next 90 property-local civil days.")
	}
	historyStart := today.AddDate(0, 0, -in.HistoryDays)
	historyFrom := historyStart.Format(time.DateOnly)
	stock := []inventory.Day{}
	for start := historyStart; start.Before(to); {
		end := start.AddDate(0, 0, 366)
		if end.After(to) {
			end = to
		}
		observed, e := inventory.CalendarObserved(ctx, tx, t.Organisation, pool.ID, inventory.DateRange{DateFrom: start.Format(time.DateOnly), DateTo: end.Format(time.DateOnly)}, property.Timezone, "", data.AsOf)
		if e != nil {
			return data, historyFrom, e
		}
		stock = append(stock, observed...)
		start = end
	}
	type commitment struct {
		Snapshot           revenue.QuoteSnapshot `json:"snapshot"`
		Currency           string                `json:"currency"`
		AccommodationMinor *int64                `json:"accommodation_minor"`
		ActiveDays         []string              `json:"active_days"`
		Quantity           int64                 `json:"quantity"`
	}
	rows, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(r)||jsonb_build_object('active_days',ARRAY(SELECT d.day::text FROM reservation_inventory_days d WHERE d.organisation_id=r.organisation_id AND d.room_id=r.id AND d.released_at IS NULL AND d.day>=$3::date AND d.day<$4::date ORDER BY d.day)) FROM reservation_rooms r JOIN reservations parent ON parent.organisation_id=r.organisation_id AND parent.id=r.reservation_id WHERE r.organisation_id=$1 AND r.pool_id=$2 AND parent.state IN ('CONFIRMED','CHECKED_IN','CHECKED_OUT','COMPLETED') AND r.date_to>$3::date AND r.date_from<$4::date ORDER BY r.id`, t.Organisation, pool.ID, historyFrom, in.DateTo)
	if e != nil {
		return data, historyFrom, e
	}
	confirmed := map[string]int64{}
	amounts := map[string]int64{}
	unknown := map[string]bool{}
	for _, raw := range rows {
		var room commitment
		if e = json.Unmarshal(raw, &room); e != nil {
			return data, historyFrom, e
		}
		for _, day := range room.ActiveDays {
			confirmed[day] += room.Quantity
		}
		if room.AccommodationMinor == nil || room.Currency != data.Currency {
			for _, day := range room.ActiveDays {
				unknown[day] = true
			}
			continue
		}
		base, e := money.New(*room.AccommodationMinor, data.Currency)
		if e != nil {
			return data, historyFrom, e
		}
		weights := []int64{}
		positive := false
		for _, night := range room.Snapshot.Nights {
			weights = append(weights, night.AmountMinor)
			positive = positive || night.AmountMinor > 0
		}
		if !positive {
			for i := range weights {
				weights[i] = 1
			}
		}
		if len(weights) == 0 {
			return data, historyFrom, apperrors.Conflict("RMS_ACCEPTED_PRICE_MISSING", "Accepted room-night attribution is missing.")
		}
		parts, e := base.Allocate(weights...)
		if e != nil {
			return data, historyFrom, e
		}
		active := map[string]bool{}
		for _, day := range room.ActiveDays {
			active[day] = true
		}
		for i, night := range room.Snapshot.Nights {
			if active[night.Day] {
				n, e := money.New(amounts[night.Day], data.Currency)
				if e != nil {
					return data, historyFrom, e
				}
				n, e = n.Add(parts[i])
				if e != nil {
					return data, historyFrom, e
				}
				amounts[night.Day] = n.AmountMinor
			}
		}
	}
	for _, day := range stock {
		var amount *int64
		if !unknown[day.Day] {
			n := amounts[day.Day]
			amount = &n
		}
		data.Days = append(data.Days, revenue.DemandDay{Day: day.Day, Capacity: int64(day.Capacity), Blocked: int64(day.Blocked), ConfirmedNights: confirmed[day.Day], HeldNights: int64(day.Held), OverbookedNights: int64(day.Overbooked), SellableNights: int64(day.Available), CalendarVersion: day.Version, AccommodationMinor: amount})
	}
	sort.Slice(data.Days, func(i, j int) bool { return data.Days[i].Day < data.Days[j].Day })
	return data, historyFrom, nil
}
