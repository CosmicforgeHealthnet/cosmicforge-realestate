package businesshttp

import (
	"encoding/json"
	"net/http"
	"time"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
)

// CRMReport is computed from owned reservations, stays, loyalty and recovery
// records. Ratios are basis points with explicit numerators/denominators and
// stay null when the denominator is zero; unavailable inputs stay unknown.
type CRMReport struct {
	PropertyID  ids.ID            `json:"property_id"`
	DateFrom    string            `json:"date_from"`
	DateTo      string            `json:"date_to"`
	Metrics     json.RawMessage   `json:"metrics"`
	Cohorts     json.RawMessage   `json:"cohorts"`
	Definitions map[string]string `json:"definitions"`
	ObservedAt  time.Time         `json:"observed_at"`
	Unknown     []string          `json:"unknown"`
}

var crmReportDefinitions = map[string]string{
	"repeat_stay_rate_bps":          "guests with two or more departed stays (ever, at checkout of a stay in the window) / guests with a departed stay in the window",
	"average_stay_nights_milli":     "sum of accepted nights of stays departed in the window * 1000 / those stays",
	"corporate_booking_share_bps":   "reservations created in the window with a corporate customer / reservations created in the window, excluding INQUIRY",
	"source_attribution":            "reservations created in the window by canonical source",
	"loyalty_enrolment":             "loyalty accounts created in the window",
	"loyalty_accrued_points":        "accrual and recovery-benefit points recorded in the window, net of reversals of those entries",
	"loyalty_redeemed_points":       "redemption points recorded in the window",
	"recovery_resolution_hours_avg": "mean hours from opening to resolution for cases resolved in the window",
	"recovery_sla_compliance_bps":   "cases resolved in the window at or before their SLA due time / cases resolved in the window",
	"cohorts":                       "guests grouped by month of first departed stay in the window, with the number who departed again in a later month",
}

func (h *Handler) registerCRMReports(r chi.Router) {
	r.Get("/crm/reports", h.crmReport)
}

func (h *Handler) crmReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	property, e := ids.ParseV7(q.Get("property_id"))
	from, fe := time.Parse(time.DateOnly, q.Get("date_from"))
	to, te := time.Parse(time.DateOnly, q.Get("date_to"))
	if e != nil || fe != nil || te != nil || !to.After(from) || to.Sub(from) > 366*24*time.Hour {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property and a date window of at most 366 days (date_to exclusive) required."))
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "guests:read", "PROPERTY", property, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out := CRMReport{PropertyID: property, DateFrom: q.Get("date_from"), DateTo: q.Get("date_to"), Definitions: crmReportDefinitions, ObservedAt: h.Auth.Service.Now().UTC(), Unknown: []string{"guest_lifetime_value_minor", "net_revenue_minor"}}
	e = h.Auth.Service.Pool.QueryRow(r.Context(), `WITH window_stays AS (
		SELECT s.id,s.guest_id,s.checked_out_at,(r.date_to-r.date_from) AS nights FROM stay_records s JOIN reservations r ON r.organisation_id=s.organisation_id AND r.id=s.reservation_id
		WHERE s.organisation_id=$1 AND s.property_id=$2 AND s.state='DEPARTED' AND s.checked_out_at>=$3::date AND s.checked_out_at<$4::date),
	guests AS (SELECT DISTINCT guest_id FROM window_stays),
	repeaters AS (SELECT g.guest_id FROM guests g WHERE (SELECT count(*) FROM stay_records x WHERE x.organisation_id=$1 AND x.property_id=$2 AND x.guest_id=g.guest_id AND x.state='DEPARTED' AND x.checked_out_at<$4::date)>=2),
	frequency AS (SELECT CASE WHEN n=1 THEN '1' WHEN n=2 THEN '2' ELSE '3_PLUS' END AS bucket,count(*) AS guests FROM (SELECT guest_id,count(*) n FROM window_stays GROUP BY guest_id) f GROUP BY 1),
	bookings AS (SELECT source,corporate_id FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND state<>'INQUIRY' AND created_at>=$3::date AND created_at<$4::date),
	resolved AS (SELECT created_at,resolved_at,sla_due_at FROM service_recovery_cases WHERE organisation_id=$1 AND property_id=$2 AND resolved_at>=$3::date AND resolved_at<$4::date)
	SELECT jsonb_build_object(
		'departed_stays',(SELECT count(*) FROM window_stays),
		'guests_with_departed_stays',(SELECT count(*) FROM guests),
		'repeat_guests',(SELECT count(*) FROM repeaters),
		'repeat_stay_rate_bps',CASE WHEN (SELECT count(*) FROM guests)>0 THEN ((SELECT count(*) FROM repeaters)*10000/(SELECT count(*) FROM guests)) END,
		'guest_frequency',coalesce((SELECT jsonb_object_agg(bucket,guests) FROM frequency),'{}'::jsonb),
		'average_stay_nights_milli',CASE WHEN (SELECT count(*) FROM window_stays)>0 THEN ((SELECT sum(nights) FROM window_stays)*1000/(SELECT count(*) FROM window_stays)) END,
		'reservations_created',(SELECT count(*) FROM bookings),
		'corporate_reservations',(SELECT count(*) FROM bookings WHERE corporate_id IS NOT NULL),
		'corporate_booking_share_bps',CASE WHEN (SELECT count(*) FROM bookings)>0 THEN ((SELECT count(*) FROM bookings WHERE corporate_id IS NOT NULL)*10000/(SELECT count(*) FROM bookings)) END,
		'source_attribution',coalesce((SELECT jsonb_object_agg(source,n) FROM (SELECT source,count(*) n FROM bookings GROUP BY source) s),'{}'::jsonb),
		'loyalty_enrolment',(SELECT count(*) FROM loyalty_accounts WHERE organisation_id=$1 AND property_id=$2 AND created_at>=$3::date AND created_at<$4::date),
		'loyalty_active_accounts',(SELECT count(*) FROM loyalty_accounts WHERE organisation_id=$1 AND property_id=$2 AND state='ACTIVE'),
		'loyalty_accrued_points',(SELECT coalesce(sum(x.points),0) FROM loyalty_transactions x WHERE x.organisation_id=$1 AND x.property_id=$2 AND x.kind IN ('ACCRUAL','RECOVERY_BENEFIT') AND x.created_at>=$3::date AND x.created_at<$4::date AND NOT EXISTS(SELECT 1 FROM loyalty_reversals v WHERE v.organisation_id=x.organisation_id AND v.original_transaction_id=x.id)),
		'loyalty_redeemed_points',(SELECT coalesce(-sum(points),0) FROM loyalty_transactions WHERE organisation_id=$1 AND property_id=$2 AND kind='REDEMPTION' AND created_at>=$3::date AND created_at<$4::date),
		'recovery_cases_opened',(SELECT count(*) FROM service_recovery_cases WHERE organisation_id=$1 AND property_id=$2 AND created_at>=$3::date AND created_at<$4::date),
		'recovery_cases_resolved',(SELECT count(*) FROM resolved),
		'recovery_resolution_hours_avg',(SELECT round(avg(extract(epoch FROM resolved_at-created_at)/3600)::numeric,2) FROM resolved),
		'recovery_sla_compliance_bps',CASE WHEN (SELECT count(*) FROM resolved)>0 THEN ((SELECT count(*) FROM resolved WHERE resolved_at<=sla_due_at)*10000/(SELECT count(*) FROM resolved)) END,
		'recovery_compensations',coalesce((SELECT jsonb_object_agg(kind||':'||state,n) FROM (SELECT c.kind,c.state,count(*) n FROM service_recovery_compensation_refs c WHERE c.organisation_id=$1 AND c.property_id=$2 AND c.created_at>=$3::date AND c.created_at<$4::date GROUP BY 1,2) k),'{}'::jsonb)),
	coalesce((SELECT jsonb_agg(jsonb_build_object('cohort_month',month,'guests',guests,'returned',returned) ORDER BY month) FROM (
		SELECT to_char(first_month,'YYYY-MM') AS month,count(*) AS guests,count(*) FILTER (WHERE returned) AS returned FROM (
			SELECT f.guest_id,f.first_month,EXISTS(SELECT 1 FROM stay_records later WHERE later.organisation_id=$1 AND later.property_id=$2 AND later.guest_id=f.guest_id AND later.state='DEPARTED' AND later.checked_out_at>=f.first_month+interval '1 month') AS returned
			FROM (SELECT guest_id,date_trunc('month',min(checked_out_at)) AS first_month FROM stay_records WHERE organisation_id=$1 AND property_id=$2 AND state='DEPARTED' GROUP BY guest_id) f WHERE f.first_month>=date_trunc('month',$3::date) AND f.first_month<$4::date) c GROUP BY first_month) m),'[]'::jsonb)`,
		t.Organisation, property, out.DateFrom, out.DateTo).Scan(&out.Metrics, &out.Cohorts)
	reply(w, r, 200, out, e)
}
