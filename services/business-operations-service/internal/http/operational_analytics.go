package businesshttp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/jackc/pgx/v5"
)

func init() { registerOpsTable("report_jobs", "analytics_snapshots") }

// KPIDefinitionsVersion identifies the Annex H definitions used.
const KPIDefinitionsVersion = "annex-h-v1"

// commandCenterKPIs computes Annex H KPIs from canonical records. A KPI whose
// denominator or source is unavailable is null; nothing is estimated.
func commandCenterKPIs(ctx context.Context, q database.Querier, org, property ids.ID, from, to time.Time) (map[string]any, error) {
	out := map[string]any{"property_id": property, "date_from": from.Format(time.DateOnly), "date_to": to.AddDate(0, 0, -1).Format(time.DateOnly), "definitions_version": KPIDefinitionsVersion}
	var available, sold *int64
	if e := q.QueryRow(ctx, `SELECT sum(c.capacity-c.blocked),sum(c.committed) FROM inventory_calendar c JOIN inventory_pools p ON p.organisation_id=c.organisation_id AND p.id=c.pool_id WHERE c.organisation_id=$1 AND p.property_id=$2 AND c.day>=$3 AND c.day<$4`, org, property, from, to).Scan(&available, &sold); e != nil {
		return nil, e
	}
	var revenue *int64
	var currency *string
	if e := q.QueryRow(ctx, `SELECT sum(r.amount_minor*greatest(0,(least(r.date_to,$4::date)-greatest(r.date_from,$3::date)))/greatest(1,(r.date_to-r.date_from)))::bigint,min(r.currency) FROM reservations r WHERE r.organisation_id=$1 AND r.property_id=$2 AND r.state IN ('CONFIRMED','CHECKED_IN','CHECKED_OUT','COMPLETED') AND r.date_from<$4 AND r.date_to>$3`, org, property, from, to).Scan(&revenue, &currency); e != nil {
		return nil, e
	}
	ratio := func(num, den *int64, scale int64) any {
		if num == nil || den == nil || *den == 0 {
			return nil
		}
		return *num * scale / *den
	}
	out["available_room_nights"], out["sold_room_nights"], out["contractual_accommodation_revenue_minor"], out["currency"] = available, sold, revenue, currency
	out["occupancy_bps"], out["adr_minor"], out["revpar_minor"] = ratio(sold, available, 10000), ratio(revenue, sold, 1), ratio(revenue, available, 1)
	var gross, cancelled, noShow, due int64
	var lead *float64
	if e := q.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE state='CANCELLED'),count(*) FILTER (WHERE state='NO_SHOW'),count(*) FILTER (WHERE state IN ('CHECKED_IN','CHECKED_OUT','COMPLETED','NO_SHOW') AND date_from<current_date),avg(date_from-created_at::date) FILTER (WHERE state NOT IN ('CANCELLED','REJECTED','EXPIRED'))::float8 FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND created_at>=$3 AND created_at<$4`, org, property, from, to).Scan(&gross, &cancelled, &noShow, &due, &lead); e != nil {
		return nil, e
	}
	out["cancellation_rate_bps"], out["no_show_rate_bps"], out["booking_lead_time_days"] = ratio(&cancelled, &gross, 10000), ratio(&noShow, &due, 10000), lead
	var responseSeconds, turnaround, maintenance *float64
	var recovered, recoveryTotal int64
	var freshness *float64
	var quality *float64
	if e := q.QueryRow(ctx, `SELECT
	 (SELECT avg(extract(epoch FROM first_responded_at-last_inbound_at)) FROM inbox_threads WHERE organisation_id=$1 AND property_id=$2 AND first_responded_at>=last_inbound_at AND last_inbound_at>=$3 AND last_inbound_at<$4)::float8,
	 (SELECT avg(extract(epoch FROM closed_at-created_at)) FROM housekeeping_tasks WHERE organisation_id=$1 AND property_id=$2 AND state='DONE' AND created_at>=$3 AND created_at<$4)::float8,
	 (SELECT avg(extract(epoch FROM closed_at-created_at)) FROM work_orders WHERE organisation_id=$1 AND property_id=$2 AND state='VERIFIED' AND created_at>=$3 AND created_at<$4)::float8,
	 (SELECT count(*) FROM service_recovery_cases WHERE organisation_id=$1 AND property_id=$2 AND state IN ('RESOLVED','CLOSED') AND resolved_at<=sla_due_at AND created_at>=$3 AND created_at<$4),
	 (SELECT count(*) FROM service_recovery_cases WHERE organisation_id=$1 AND property_id=$2 AND created_at>=$3 AND created_at<$4),
	 (SELECT extract(epoch FROM now()-max(updated_at)) FROM channel_sync_jobs WHERE organisation_id=$1 AND property_id=$2 AND state='SUCCEEDED')::float8,
	 (SELECT avg(q.score) FROM content_quality_scores q JOIN listing_versions v ON v.organisation_id=q.organisation_id AND v.id=q.listing_version_id JOIN listings l ON l.organisation_id=v.organisation_id AND l.id=v.listing_id WHERE l.organisation_id=$1 AND l.property_id=$2)::float8`, org, property, from, to).Scan(&responseSeconds, &turnaround, &maintenance, &recovered, &recoveryTotal, &freshness, &quality); e != nil {
		return nil, e
	}
	out["response_sla_seconds"], out["housekeeping_turnaround_seconds"], out["maintenance_cycle_seconds"] = responseSeconds, turnaround, maintenance
	out["guest_recovery_rate_bps"], out["inventory_sync_freshness_seconds"], out["content_quality_score"] = ratio(&recovered, &recoveryTotal, 10000), freshness, quality
	var net *int64
	if e := q.QueryRow(ctx, `SELECT sum(CASE WHEN component='GROSS_REVENUE' THEN amount_minor ELSE -amount_minor END) FROM channel_financial_inputs WHERE organisation_id=$1 AND property_id=$2 AND period_from>=$3 AND period_to<=$4 AND component IN ('GROSS_REVENUE','COMMISSION','PAYMENT_FEE','PROMOTION_COST','OPERATIONAL_COST')`, org, property, from, to).Scan(&net); e != nil {
		return nil, e
	}
	out["net_revpar_minor"] = ratio(net, available, 1)
	out["net_revpar_basis"] = "AUTHORITATIVE_CHANNEL_FINANCIAL_INPUTS"
	return out, nil
}

func (h *Handler) kpiRequest(w http.ResponseWriter, r *http.Request, capability string) {
	property, e := ids.ParseV7(r.URL.Query().Get("property_id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A property_id scope is required."))
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", property, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	from, to, e := reputationWindow(r, h.Auth.Service.Now().UTC())
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out, e := commandCenterKPIs(r.Context(), h.Auth.Service.Pool, t.Organisation, property, from.Truncate(24*time.Hour), to.Truncate(24*time.Hour))
	reply(w, r, 200, out, e)
}

func (h *Handler) analyticsDashboard(w http.ResponseWriter, r *http.Request) {
	h.kpiRequest(w, r, "analytics:read")
}
func (h *Handler) revenueDashboard(w http.ResponseWriter, r *http.Request) {
	h.kpiRequest(w, r, "rms:read")
}

func (h *Handler) analyticsReportCreate(w http.ResponseWriter, r *http.Request) {
	var in AnalyticsReportCreate
	if !decode(w, r, &in) {
		return
	}
	from, e1 := time.Parse(time.DateOnly, in.DateFrom)
	to, e2 := time.Parse(time.DateOnly, in.DateTo)
	if in.PropertyID.ValidateV7() != nil || !slices.Contains([]string{"COMMAND_CENTER", "REPUTATION", "CONVERSION", "FINANCE", "OPERATIONS"}, in.Kind) || e1 != nil || e2 != nil || to.Before(from) || to.Sub(from) > 731*24*time.Hour || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, report kind, window of at most two years and reason required."))
		return
	}
	h.execute(w, r, "reports:create:"+in.PropertyID.String(), in, "analytics:read", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "analytics:read", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO report_jobs(id,organisation_id,property_id,kind,date_from,date_to,created_by,created_by_member,created_by_role) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, t.Organisation, in.PropertyID, in.Kind, in.DateFrom, in.DateTo, t.Account, t.Member, t.Role); e != nil {
			return 0, nil, e
		}
		out, e := oneJSON(ctx, tx, `SELECT to_jsonb(x) FROM report_jobs x WHERE id=$1`, id)
		if e != nil {
			return 0, nil, e
		}
		return 202, out, h.opsCommit(ctx, tx, t, in.PropertyID, id, "report_job", "QUEUED", "", "QUEUED", "", 1, in)
	})
}

func (h *Handler) analyticsReportGet(w http.ResponseWriter, r *http.Request) {
	t, _, id, e := h.opsRecordProperty(r, "report_jobs", "analytics:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	job, e := oneJSON(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x)||jsonb_build_object('snapshot',(SELECT to_jsonb(s) FROM analytics_snapshots s WHERE s.organisation_id=x.organisation_id AND s.id=x.snapshot_id)) FROM report_jobs x WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	reply(w, r, 200, job, e)
}

// analyticsBenchmarks returns only governed, licensed market inputs with
// their source and licence references; absent data is reported as absent.
func (h *Handler) analyticsBenchmarks(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "analytics:read", `SELECT jsonb_build_object('id',id,'kind',kind,'channel',channel,'date_from',date_from,'date_to',date_to,'value_minor',value_minor,'value_bps',value_bps,'currency',currency,'label',label,'provenance',to_jsonb(x)-'id'-'organisation_id') FROM rms_market_inputs x WHERE organisation_id=$1 AND property_id=$2 AND kind IN ('COMPETITOR_RATE','MARKET_DEMAND_INDEX') ORDER BY date_from DESC,id LIMIT $3 OFFSET $4`)
}

// ProcessReports materialises queued report jobs with leased fencing. The
// requester's current authority is rechecked; snapshots are immutable.
func (h *Handler) ProcessReports(ctx context.Context, limit int) error {
	owner := "report-" + ids.New().String()
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT organisation_id,id FROM report_jobs WHERE state='QUEUED' OR state='RUNNING' AND lease_expires_at<now() ORDER BY created_at LIMIT $1`, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, id ids.ID }
	refs := []ref{}
	for rows.Next() {
		var x ref
		if e = rows.Scan(&x.org, &x.id); e != nil {
			rows.Close()
			return e
		}
		refs = append(refs, x)
	}
	rows.Close()
	var failures []error
	for _, x := range refs {
		e := withServiceTx(ctx, h, func(ctx context.Context, tx pgx.Tx) error {
			var property, member, role ids.ID
			var kind, state string
			var from, to time.Time
			var version int64
			var attempts int
			if e := tx.QueryRow(ctx, `SELECT property_id,created_by_member,created_by_role,kind,state,date_from,date_to,version,attempts FROM report_jobs WHERE id=$1 FOR UPDATE SKIP LOCKED`, x.id).Scan(&property, &member, &role, &kind, &state, &from, &to, &version, &attempts); e != nil {
				if errors.Is(e, pgx.ErrNoRows) {
					return nil
				}
				return e
			}
			if _, e := tx.Exec(ctx, `UPDATE report_jobs SET state='RUNNING',lease_owner=$2,lease_expires_at=now()+interval '2 minutes',lease_generation=lease_generation+1,attempts=attempts+1,version=version+1,updated_at=now() WHERE id=$1`, x.id, owner); e != nil {
				return e
			}
			t := workerTenant(x.org)
			ok, e := h.recordedAuthority(ctx, tx, x.org, "PROPERTY", property, member, role, "analytics:read")
			if e != nil {
				return e
			}
			if !ok {
				if _, e = tx.Exec(ctx, `UPDATE report_jobs SET state='FAILED',failure_reason='REQUESTER_AUTHORITY_REVOKED',version=version+1,updated_at=now() WHERE id=$1`, x.id); e != nil {
					return e
				}
				return h.opsCommit(ctx, tx, t, property, x.id, "report_job", "FAILED", "RUNNING", "FAILED", "", version+2, map[string]any{"reason": "REQUESTER_AUTHORITY_REVOKED"})
			}
			var metrics map[string]any
			end := to.AddDate(0, 0, 1)
			switch kind {
			case "REPUTATION":
				metrics, e = reputationMetrics(ctx, tx, x.org, property, from, end)
			default:
				metrics, e = commandCenterKPIs(ctx, tx, x.org, property, from, end)
			}
			if e != nil {
				return e
			}
			raw := marshalCommercial(metrics)
			snapshot := ids.New()
			if _, e = tx.Exec(ctx, `INSERT INTO analytics_snapshots(id,organisation_id,property_id,kind,date_from,date_to,metrics,input_digest,definitions_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, snapshot, x.org, property, kind, from, to, raw, security.SHA256Hex(raw), KPIDefinitionsVersion); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE report_jobs SET state='SUCCEEDED',snapshot_id=$2,lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=now() WHERE id=$1`, x.id, snapshot); e != nil {
				return e
			}
			return h.opsCommit(ctx, tx, t, property, snapshot, "analytics_snapshot", "MATERIALIZED", "", "READY", businessEvents.AnalyticsSnapshotReady, 1, map[string]any{"report_job_id": x.id, "kind": kind, "input_digest": security.SHA256Hex(raw), "definitions_version": KPIDefinitionsVersion})
		})
		if e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
