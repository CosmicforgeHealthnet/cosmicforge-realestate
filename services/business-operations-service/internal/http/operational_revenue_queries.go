package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

func decodeRMSRecord[T any](raw []byte, err error) (T, error) {
	var out T
	if errors.Is(err, pgx.ErrNoRows) {
		return out, apperrors.NotFound("RMS_RECORD_NOT_FOUND", "RMS record not found.")
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

func loadRMSDemandSnapshot(ctx context.Context, q database.Querier, org, id ids.ID) (RMSDemandSnapshot, error) {
	var out RMSDemandSnapshot
	raw, e := db.New().GetRMSDemandSnapshot(ctx, q, db.GetRMSDemandSnapshotParams{OrganisationID: org, ID: id})
	if errors.Is(e, pgx.ErrNoRows) {
		return out, apperrors.NotFound("RMS_SNAPSHOT_NOT_FOUND", "RMS snapshot not found.").WithCause(e)
	}
	if e != nil {
		return out, e
	}
	if e = json.Unmarshal(raw, &out); e != nil {
		return out, e
	}
	out.AsOf = out.AsOf.UTC()
	if out.SourceDigest != hashCommercialPayload(marshalCommercial(out.Dataset)) {
		return out, apperrors.Conflict("RMS_SOURCE_INTEGRITY", "Stored observation does not match its retained source digest.")
	}
	return out, nil
}
func (h *Handler) rmsDemandTarget(r *http.Request, id ids.ID) (tenant, RMSDemandSnapshot, error) {
	return h.rmsDemandTargetCapability(r, id, "rms:read")
}
func (h *Handler) rmsDemandTargetCapability(r *http.Request, id ids.ID, capability string) (tenant, RMSDemandSnapshot, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, RMSDemandSnapshot{}, e
	}
	v, e := loadRMSDemandSnapshot(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e == nil {
		t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", v.PropertyID, false)
	}
	return t, v, e
}
func (h *Handler) rmsDemandList(w http.ResponseWriter, r *http.Request) {
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
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(s)-'dataset'-'provenance' FROM demand_snapshots s WHERE organisation_id=$1 AND property_id=ANY($2::uuid[]) ORDER BY as_of DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, properties, limit, offset)
	reply(w, r, 200, map[string]any{"items": rows, "limit": limit, "offset": offset}, e)
}
func (h *Handler) rmsDemandGet(w http.ResponseWriter, r *http.Request) {
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	_, out, e := h.rmsDemandTarget(r, id)
	reply(w, r, 200, out, e)
}

type RMSMetricsResult struct {
	SnapshotID   ids.ID             `json:"snapshot_id"`
	SourceDigest string             `json:"source_digest"`
	DateFrom     string             `json:"date_from"`
	DateTo       string             `json:"date_to"`
	AsOf         time.Time          `json:"as_of"`
	Metrics      revenue.RMSMetrics `json:"metrics"`
}

func (h *Handler) rmsMetricsGet(w http.ResponseWriter, r *http.Request) {
	id, e := ids.ParseV7(r.URL.Query().Get("snapshot_id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A retained snapshot_id is required for reproducible metrics."))
		return
	}
	_, v, e := h.rmsDemandTarget(r, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	data := v.Dataset
	data.Days = nil
	for _, day := range v.Dataset.Days {
		if day.Day >= v.DateFrom && day.Day < v.DateTo {
			data.Days = append(data.Days, day)
		}
	}
	metrics, e := revenue.MetricsFromDemand(data)
	if e != nil {
		reply(w, r, 0, nil, apperrors.Conflict("RMS_METRIC_SOURCE_INVALID", "The retained source cannot support these contractual metrics."))
		return
	}
	reply(w, r, 200, RMSMetricsResult{SnapshotID: id, SourceDigest: v.SourceDigest, DateFrom: v.DateFrom, DateTo: v.DateTo, AsOf: v.AsOf, Metrics: metrics}, nil)
}

type RMSPaceDay struct {
	Day                           string `json:"day"`
	CurrentConfirmedNights        *int64 `json:"current_confirmed_nights"`
	PreviousConfirmedNights       *int64 `json:"previous_confirmed_nights"`
	PickupNights                  *int64 `json:"pickup_nights"`
	ContractualRevenueChangeMinor *int64 `json:"contractual_revenue_change_minor"`
}
type RMSPaceResult struct {
	State              string       `json:"state"`
	CurrentSnapshotID  ids.ID       `json:"current_snapshot_id"`
	PreviousSnapshotID *ids.ID      `json:"previous_snapshot_id"`
	ObservationSeconds *int64       `json:"observation_seconds"`
	Days               []RMSPaceDay `json:"days"`
	Basis              string       `json:"basis"`
}

func (h *Handler) captureRMSPace(ctx context.Context, tx pgx.Tx, t tenant, current ids.ID, in RMSDemandCommand, data revenue.DemandDataset) error {
	var previousID ids.ID
	e := tx.QueryRow(ctx, `SELECT id FROM demand_snapshots WHERE organisation_id=$1 AND property_id=$2 AND pool_id=$3 AND id<>$4 AND date_from=$5::date AND date_to=$6::date AND as_of<$7 AND currency=$8 AND dataset->>'timezone'=$9 ORDER BY as_of DESC,id DESC LIMIT 1`, t.Organisation, in.PropertyID, in.PoolID, current, in.DateFrom, in.DateTo, data.AsOf, data.Currency, data.Timezone).Scan(&previousID)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	out := RMSPaceResult{State: "NO_EARLIER_OBSERVATION", CurrentSnapshotID: current, Days: []RMSPaceDay{}, Basis: "CHANGE_IN_CONFIRMED_COMMITTED_DEMAND_BETWEEN_RETAINED_OBSERVATIONS_NOT_REALIZED_CASH"}
	previous := map[string]revenue.DemandDay{}
	if e == nil {
		p, e := loadRMSDemandSnapshot(ctx, tx, t.Organisation, previousID)
		if e != nil {
			return e
		}
		if p.Dataset.AsOf.Before(data.AsOf) {
			out.State = "COMPARABLE"
			out.PreviousSnapshotID = &previousID
			seconds := int64(data.AsOf.Sub(p.Dataset.AsOf) / time.Second)
			out.ObservationSeconds = &seconds
			for _, day := range p.Dataset.Days {
				previous[day.Day] = day
			}
		}
	}
	currentDays := map[string]revenue.DemandDay{}
	for _, day := range data.Days {
		currentDays[day.Day] = day
	}
	first, _ := time.Parse(time.DateOnly, in.DateFrom)
	last, _ := time.Parse(time.DateOnly, in.DateTo)
	for date := first; date.Before(last); date = date.AddDate(0, 0, 1) {
		key := date.Format(time.DateOnly)
		point := RMSPaceDay{Day: key}
		now, known := currentDays[key]
		old, wasKnown := previous[key]
		if known {
			n := now.ConfirmedNights
			point.CurrentConfirmedNights = &n
		}
		if wasKnown {
			n := old.ConfirmedNights
			point.PreviousConfirmedNights = &n
		}
		if known && wasKnown {
			delta := now.ConfirmedNights - old.ConfirmedNights
			point.PickupNights = &delta
			if now.AccommodationMinor != nil && old.AccommodationMinor != nil {
				change := *now.AccommodationMinor - *old.AccommodationMinor
				point.ContractualRevenueChangeMinor = &change
			}
		}
		out.Days = append(out.Days, point)
	}
	_, e = tx.Exec(ctx, `INSERT INTO booking_pace_snapshots(id,organisation_id,property_id,pool_id,current_snapshot_id,previous_snapshot_id,state,result) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ids.New(), t.Organisation, in.PropertyID, in.PoolID, current, out.PreviousSnapshotID, out.State, marshalCommercial(out))
	return e
}
func (h *Handler) rmsPaceGet(w http.ResponseWriter, r *http.Request) {
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	t, _, e := h.rmsDemandTarget(r, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out, e := oneJSON(r.Context(), h.Auth.Service.Pool, `SELECT result FROM booking_pace_snapshots WHERE organisation_id=$1 AND current_snapshot_id=$2`, t.Organisation, id)
	reply(w, r, 200, out, e)
}
