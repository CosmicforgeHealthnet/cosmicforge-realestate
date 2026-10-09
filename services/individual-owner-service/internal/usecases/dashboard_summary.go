package usecases

import (
	"context"
	"fmt"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Dashboard portfolio summary and quick actions (§13.1). Every figure is
// read from the authoritative owner-domain tables for the properties the
// account holds authority over (property_access); quick actions only point
// at the normal, authorized domain commands and queries.

func (s *Service) portfolioSummary(ctx context.Context, account ids.ID, now time.Time) (PortfolioSummary, error) {
	q := s.dbq(ctx)
	sum := PortfolioSummary{PropertiesByStatus: map[string]int64{}, PropertyVerification: map[string]int64{}, Pipeline: map[string]int64{},
		Operations: map[string]int64{}, IdentityVerification: "NOT_STARTED"}
	group := func(sql string, into map[string]int64) error {
		rows, err := q.Query(ctx, propsCTE+sql, account, now, nil)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			var n int64
			if err := rows.Scan(&k, &n); err != nil {
				return err
			}
			into[k] = n
		}
		return rows.Err()
	}
	if err := group(` SELECT status, count(*) FROM properties WHERE id IN (SELECT property_id FROM props) GROUP BY status`, sum.PropertiesByStatus); err != nil {
		return sum, err
	}
	if err := group(` SELECT coalesce(vs, 'NOT_STARTED'), count(*) FROM (
		SELECT (SELECT v.status FROM verification_links v WHERE v.property_id = p.property_id AND v.verification_type = 'PROPERTY_AUTHORITY'
		         ORDER BY v.created_at DESC LIMIT 1) AS vs FROM props p) x GROUP BY 1`, sum.PropertyVerification); err != nil {
		return sum, err
	}
	if err := q.QueryRow(ctx, `SELECT coalesce((SELECT status FROM verification_links WHERE account_id = $1 AND verification_type = 'IDENTITY'
		ORDER BY created_at DESC LIMIT 1), 'NOT_STARTED')`, account).Scan(&sum.IdentityVerification); err != nil {
		return sum, err
	}
	counts := []struct {
		into map[string]int64
		key  string
		sql  string
	}{
		{sum.Pipeline, "open_inquiries", `SELECT count(*) FROM inquiries WHERE property_id IN (SELECT property_id FROM props) AND status NOT IN ('CLOSED_WON','CLOSED_LOST')`},
		{sum.Pipeline, "upcoming_viewings", `SELECT count(*) FROM viewing_requests WHERE property_id IN (SELECT property_id FROM props) AND status IN ('REQUESTED','CONFIRMED','RESCHEDULED') AND starts_at >= $2`},
		{sum.Pipeline, "open_applications", `SELECT count(*) FROM rental_applications WHERE property_id IN (SELECT property_id FROM props) AND status IN ('RECEIVED','SUBMITTED','UNDER_REVIEW','INFO_REQUESTED','HUMAN_REVIEW','APPEAL','WAITLISTED','CONDITIONAL')`},
		{sum.Pipeline, "rental_offers_sent", `SELECT count(*) FROM rental_offers WHERE property_id IN (SELECT property_id FROM props) AND status = 'SENT'`},
		{sum.Pipeline, "active_sales", `SELECT count(*) FROM sale_transactions WHERE property_id IN (SELECT property_id FROM props) AND status NOT IN ('COMPLETED','CANCELLED','FAILED')`},
		{sum.Pipeline, "open_sale_offers", `SELECT count(*) FROM sale_offers o JOIN sale_transactions t ON t.id = o.sale_id WHERE t.property_id IN (SELECT property_id FROM props) AND o.status IN ('SUBMITTED','COUNTERED')`},
		{sum.Pipeline, "upcoming_bookings", `SELECT count(*) FROM shortlet_bookings WHERE property_id IN (SELECT property_id FROM props) AND status IN ('PENDING_APPROVAL','PENDING_PAYMENT','CONFIRMED') AND check_in >= $2::date`},
		{sum.Pipeline, "stays_in_progress", `SELECT count(*) FROM shortlet_bookings WHERE property_id IN (SELECT property_id FROM props) AND status = 'CHECKED_IN'`},
		{sum.Operations, "active_manager_assignments", `SELECT count(*) FROM manager_assignments WHERE property_id IN (SELECT property_id FROM props) AND status IN ('ACTIVE','EXPIRING')`},
		{sum.Operations, "open_maintenance", `SELECT count(*) FROM maintenance_requests WHERE property_id IN (SELECT property_id FROM props) AND status NOT IN ('COMPLETED','CANCELLED')`},
		{sum.Operations, "open_professional_appointments", `SELECT count(*) FROM professional_appointments WHERE owner_account_id = $1 AND status NOT IN ('ACCEPTED','CANCELLED')`},
		{sum.Operations, "open_disputes", `SELECT count(*) FROM domain_disputes WHERE account_id = $1 AND status IN ('OPEN','UNDER_REVIEW','AWAITING_INFORMATION')`},
		{sum.Operations, "reports_ready_30d", `SELECT count(*) FROM report_snapshots WHERE account_id = $1 AND status = 'READY' AND created_at >= $2 - interval '30 days'`},
		{sum.Operations, "statutory_due_30d", `SELECT count(*) FROM statutory_obligations WHERE account_id = $1 AND status = 'UPCOMING' AND due_on <= $2::date + 30`},
		{sum.Operations, "open_stay_extensions", `SELECT count(*) FROM shortlet_booking_extensions WHERE property_id IN (SELECT property_id FROM props) AND status IN ('REQUESTED','PAYMENT_PENDING')`},
	}
	for _, c := range counts {
		var n int64
		if err := q.QueryRow(ctx, propsCTE+" "+c.sql, account, now, nil).Scan(&n); err != nil {
			return sum, fmt.Errorf("dashboard %s: %w", c.key, err)
		}
		c.into[c.key] = n
	}
	return sum, nil
}

// quickActions derives shortcuts from what awaits the owner.
func (s *Service) quickActions(ctx context.Context, subj authz.Subject, v DashboardView) ([]QuickAction, error) {
	const base = "/api/v1/owner"
	out := []QuickAction{}
	if _, err := s.authorize(ctx, subj, authz.ActPropertyCreate, ids.Nil); err == nil {
		out = append(out, QuickAction{Key: "CREATE_PROPERTY", Label: "Add a property", Method: "POST", Path: base + "/properties"})
	}
	add := func(key, label, method, path string, n int64) {
		if n > 0 {
			out = append(out, QuickAction{Key: key, Label: label, Method: method, Path: base + path, Count: n})
		}
	}
	a := v.ActionItems
	add("RESPOND_TO_INQUIRIES", "Respond to new inquiries", "GET", "/inquiries?status=NEW", a["new_inquiries"])
	add("REVIEW_VIEWING_REQUESTS", "Review viewing requests", "GET", "/viewings?status=REQUESTED", a["viewing_requests"])
	add("REVIEW_APPLICATIONS", "Review applications", "GET", "/applications?status=SUBMITTED", a["applications_to_review"])
	add("APPROVE_BOOKINGS", "Approve booking requests", "GET", "/shortlet/bookings?status=PENDING_APPROVAL", a["bookings_awaiting_approval"])
	add("MAINTENANCE_DECISIONS", "Decide on maintenance", "GET", "/maintenance?status=APPROVAL_PENDING", a["maintenance_awaiting_decision"])
	add("CO_OWNER_CONSENTS", "Answer co-owner consents", "GET", "/consents", a["co_owner_consents"])
	add("OVERDUE_RENT", "Follow up overdue rent", "GET", "/tenancies", a["overdue_rent"])
	add("STATUTORY_DUE", "Upcoming tax and statutory dates", "GET", "/statutory-obligations?status=UPCOMING", v.Portfolio.Operations["statutory_due_30d"])
	var listingID ids.ID
	err := s.dbq(ctx).QueryRow(ctx, propsCTE+` SELECT f.listing_id FROM freshness_checks f JOIN listings l ON l.id = f.listing_id
		WHERE l.property_id IN (SELECT property_id FROM props) AND f.status IN ('PENDING','REMINDED') ORDER BY f.due_at LIMIT 1`, accountOf(subj), s.now(), nil).Scan(&listingID)
	if err == nil {
		add("CONFIRM_AVAILABILITY", "Confirm a listing is still available", "POST", "/listings/"+listingID.String()+"/confirm-availability", a["availability_confirmations_due"])
	} else if !isNoRows(err) {
		return nil, err
	}
	err = s.dbq(ctx).QueryRow(ctx, propsCTE+` SELECT id FROM listings WHERE property_id IN (SELECT property_id FROM props)
		AND status IN ('DRAFT','READY_FOR_REVIEW') ORDER BY updated_at DESC LIMIT 1`, accountOf(subj), s.now(), nil).Scan(&listingID)
	if err == nil {
		add("PUBLISH_LISTING", "Preview and publish a draft listing", "GET", "/listings/"+listingID.String()+"/preview",
			v.ListingsByStatus["DRAFT"]+v.ListingsByStatus["READY_FOR_REVIEW"])
	} else if !isNoRows(err) {
		return nil, err
	}
	return out, nil
}
