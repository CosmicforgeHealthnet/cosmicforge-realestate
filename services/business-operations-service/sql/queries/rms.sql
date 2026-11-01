-- name: GetRMSDemandSnapshot :one
SELECT to_jsonb(s) FROM demand_snapshots s WHERE organisation_id=$1 AND id=$2;

-- name: GetRMSForecastRun :one
SELECT to_jsonb(f) FROM forecast_runs f WHERE organisation_id=$1 AND id=$2;

-- name: GetRMSForecastModel :one
SELECT to_jsonb(m) FROM forecast_models m WHERE model_key=$1 AND version=$2;

-- name: GetRMSPolicy :one
SELECT to_jsonb(p) FROM rms_policies p WHERE organisation_id=$1 AND id=$2;

-- name: GetRMSRecommendation :one
SELECT to_jsonb(p) FROM pricing_recommendations p WHERE organisation_id=$1 AND id=$2;
