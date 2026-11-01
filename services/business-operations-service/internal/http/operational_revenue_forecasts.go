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
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

type RMSForecastCommand struct {
	SnapshotID    ids.ID                        `json:"snapshot_id"`
	Configuration revenue.ForecastConfiguration `json:"configuration"`
	SupersedesID  *ids.ID                       `json:"supersedes_id,omitempty"`
}
type RMSForecastRun struct {
	ID               ids.ID                        `json:"id"`
	PropertyID       ids.ID                        `json:"property_id"`
	PoolID           ids.ID                        `json:"pool_id"`
	ModelID          ids.ID                        `json:"model_id"`
	SourceSnapshotID ids.ID                        `json:"source_snapshot_id"`
	SourceAsOf       time.Time                     `json:"source_as_of"`
	SourceDigest     string                        `json:"source_digest"`
	DateFrom         string                        `json:"date_from"`
	DateTo           string                        `json:"date_to"`
	Configuration    revenue.ForecastConfiguration `json:"configuration"`
	Result           revenue.DemandForecast        `json:"result"`
	Status           string                        `json:"status"`
	SupersedesID     *ids.ID                       `json:"supersedes_id,omitempty"`
}

func (h *Handler) registerRMSForecasts(r chi.Router) {
	r.Get("/revenue/forecasts", h.rmsForecastList)
	r.Post("/revenue/forecasts", h.rmsForecastCreate)
	r.Get("/revenue/forecasts/{id}", h.rmsForecastGet)
}
func loadRMSForecast(ctx context.Context, q database.Querier, org, id ids.ID) (RMSForecastRun, error) {
	var out RMSForecastRun
	raw, e := db.New().GetRMSForecastRun(ctx, q, db.GetRMSForecastRunParams{OrganisationID: org, ID: id})
	if errors.Is(e, pgx.ErrNoRows) {
		return out, apperrors.NotFound("RMS_FORECAST_NOT_FOUND", "RMS forecast not found.").WithCause(e)
	}
	if e != nil {
		return out, e
	}
	e = json.Unmarshal(raw, &out)
	return out, e
}
func (h *Handler) rmsForecastCreate(w http.ResponseWriter, r *http.Request) {
	var in RMSForecastCommand
	if !decode(w, r, &in) {
		return
	}
	if in.SnapshotID.ValidateV7() != nil || in.Configuration.Validate() != nil || in.SupersedesID != nil && in.SupersedesID.ValidateV7() != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A retained source snapshot and valid explicit model configuration required."))
		return
	}
	_, pre, e := h.rmsDemandTargetCapability(r, in.SnapshotID, "rms:write")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "revenue:forecast:create", in, "rms:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "rms:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		source, e := loadRMSDemandSnapshot(ctx, tx, t.Organisation, in.SnapshotID)
		if e != nil {
			return 0, nil, e
		}
		if in.SupersedesID != nil {
			old, e := loadRMSForecast(ctx, tx, t.Organisation, *in.SupersedesID)
			if e != nil {
				return 0, nil, e
			}
			if old.PoolID != source.PoolID || old.PropertyID != source.PropertyID || old.DateFrom != source.DateFrom || old.DateTo != source.DateTo || old.SourceAsOf.After(source.AsOf) {
				return 0, nil, apperrors.Conflict("RMS_REVISION_SCOPE", "A forecast revision requires the same pool/date scope and a non-older source observation.")
			}
		}
		result, e := revenue.ForecastDemand(source.Dataset, in.Configuration, source.DateFrom, source.DateTo)
		if e != nil {
			return 0, nil, apperrors.Conflict("RMS_FORECAST_SOURCE_INVALID", "The retained observation cannot support this explicit model configuration.")
		}
		var model struct {
			ID ids.ID `json:"id"`
		}
		raw, e := db.New().GetRMSForecastModel(ctx, tx, db.GetRMSForecastModelParams{ModelKey: revenue.BaselineForecastModel, Version: 1})
		if e != nil {
			return 0, nil, e
		}
		if e = json.Unmarshal(raw, &model); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO forecast_runs(id,organisation_id,property_id,pool_id,model_id,source_snapshot_id,source_as_of,source_digest,date_from,date_to,configuration,result,status,supersedes_id,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::date,$10::date,$11,$12,$13,$14,$15)`, id, t.Organisation, source.PropertyID, source.PoolID, model.ID, source.ID, source.AsOf, source.SourceDigest, source.DateFrom, source.DateTo, marshalCommercial(in.Configuration), marshalCommercial(result), result.Status, in.SupersedesID, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, source.PropertyID, id, "forecast_run", "RMS_FORECAST_CREATED", "revenue.forecast.created.v1", 1, map[string]any{"forecast_id": id, "model_id": model.ID, "model_version": 1, "source_snapshot_id": source.ID, "source_digest": source.SourceDigest, "source_as_of": source.AsOf, "status": result.Status, "history_coverage_bps": result.HistoryCoverageBPS, "is_calibrated_probability": false, "rates_changed": false, "capacity_changed": false}); e != nil {
			return 0, nil, e
		}
		out, e := loadRMSForecast(ctx, tx, t.Organisation, id)
		return 201, out, e
	})
}
func (h *Handler) rmsForecastList(w http.ResponseWriter, r *http.Request) {
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
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(f)-'result' FROM forecast_runs f WHERE organisation_id=$1 AND property_id=ANY($2::uuid[]) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, properties, limit, offset)
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) rmsForecastGet(w http.ResponseWriter, r *http.Request) {
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
	v, e := loadRMSForecast(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	_, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "rms:read", "PROPERTY", v.PropertyID, false)
	reply(w, r, 200, v, e)
}
