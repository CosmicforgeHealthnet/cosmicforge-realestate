package integration

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"testing"
	"time"
)

func pmsCollectedStay(c *commercialHarness) (reservations.Detail, businesshttp.PMSStayDetail) {
	t := c.t
	c.approver = c.user()
	c.secure(c.approver)
	c.join(c.approver, c.owner, []string{"approvals:decide", "reservations:approve", "pms:approve", "folios:approve"})
	reservationPolicyFixture(c, false)
	g := crmGuestFixture(c)
	cmd := reservationCommandFixture(c, false)
	cmd.Guests = []reservations.GuestCommand{{GuestID: &g.Guest.ID, SourceKind: g.SourceKind, SourceReference: g.SourceReference, IsPrimary: true}}
	v := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", cmd, 201))
	collection := reservationPaymentCommand(c, v)
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/payment-requests", collection, 202)
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	v = commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+v.Reservation.ID.String(), nil, 200))
	v = commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/confirm", reservations.Command{Version: v.Reservation.Version, Reason: "Confirm exact collection evidence before the physical arrival workflow"}, 200))
	status := pmsReady(c)
	a := pmsAssign(c, v, status)
	check := businesshttp.PMSCheckInCommand{AssignmentID: a.ID, AssignmentVersion: a.Version, ReservationVersion: v.Reservation.Version, RoomStatusVersion: status.Version, GuestID: g.Guest.ID, RegistrationReference: "registered-financial-journey-guest", Reason: "Open the physical stay without conflating collection and final settlement"}
	arrival, e := hospitality.LocalInstant(a.DateFrom, v.Reservation.PolicySnapshot.CheckInLocal, v.Reservation.PolicySnapshot.AcceptanceTimezone)
	if e != nil {
		t.Fatal(e)
	}
	if time.Now().Before(arrival) {
		op := check
		check.ApprovalID = c.approval("COMMERCIAL_PMS_EARLY_CHECK_IN", "pms:approve", "", nil, "", map[string]any{"command": op})
	}
	opened := commercialData[struct {
		StayID  ids.ID `json:"stay_id"`
		FolioID ids.ID `json:"folio_id"`
	}](t, c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/check-in", check, 201))

	return v, commercialData[businesshttp.PMSStayDetail](t, c.must(c.owner, "GET", "/stays/"+opened.StayID.String(), nil, 200))
}
func pmsReadSettled(c *commercialHarness, v reservations.Detail) {
	c.t.Helper()
	f := reservationPaymentProvider(c)
	f.mu.Lock()
	f.intent.SettlementState = "SETTLED"
	f.fundingConfirmed = true
	f.mu.Unlock()
	var version int64
	if e := c.db.Pool.QueryRow(c.t.Context(), `SELECT version FROM reservations WHERE id=$1`, v.Reservation.ID).Scan(&version); e != nil {
		c.t.Fatal(e)
	}
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/payment-readback", map[string]any{"version": version, "financial_group_id": v.FinancialGroups[0].ID, "reason": "Read owning Payment funded settlement without creating a second collection"}, 202)
	if e := c.domain.ProcessReservationFinance(c.t.Context(), 1); e != nil {
		c.t.Fatal(e)
	}
}
func pmsDeferredDeparture(c *commercialHarness, v reservations.Detail, d businesshttp.PMSStayDetail) {
	c.t.Helper()
	var version int64
	if e := c.db.Pool.QueryRow(c.t.Context(), `SELECT version FROM reservations WHERE id=$1`, v.Reservation.ID).Scan(&version); e != nil {
		c.t.Fatal(e)
	}
	in := businesshttp.PMSCheckoutCommand{StayID: d.Stay.ID, Version: d.Stay.Version, ReservationVersion: version, FolioVersion: d.Folio.Version, Reason: "Record actual departure and retain outstanding debt for later owning settlement"}
	op := in
	in.ApprovalID = c.approval("COMMERCIAL_PMS_DEPARTURE_DEBT", "pms:approve", "", &d.BalanceMinor, d.Folio.Currency, map[string]any{"stay_id": d.Stay.ID, "command": op})
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/check-out", in, 200)
}
func pmsRunMaintenance(c *commercialHarness) {
	c.t.Helper()
	if _, e := c.db.Pool.Exec(c.t.Context(), `DELETE FROM hospitality_maintenance_cursors`); e != nil {
		c.t.Fatal(e)
	}
	if e := c.domain.ProcessHospitalityMaintenance(c.t.Context(), 50); e != nil {
		c.t.Fatal(e)
	}
}
func TestBusinessPMSReviewedDeferredCompletionAndBackingRecovery(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	v, d := pmsCollectedStay(c)
	path := "/folios/" + d.Folio.ID.String()
	c.must(c.owner, "POST", path+"/close", businesshttp.PMSVersionCommand{Version: 1, Reason: "An active physical stay cannot be financially closed"}, 409)
	pmsDeferredDeparture(c, v, d)
	review := commercialData[businesshttp.PMSFolioCloseReview](t, c.must(c.owner, "POST", path+"/close-reviews", businesshttp.PMSFolioCloseReviewCommand{Version: 2, AutomaticClose: true, ExpiresAt: time.Now().Add(time.Hour), Reason: "Delegate only final closure of these immutable postings after owning settlement"}, 201))
	pmsRunMaintenance(c)
	var state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM folios WHERE id=$1`, d.Folio.ID).Scan(&state); e != nil || state != "SETTLEMENT_PENDING" {
		t.Fatalf("unsettled closed: %s %v", state, e)
	}
	pmsReadSettled(c, v)
	c.must(c.owner, "POST", path+"/reconcile", businesshttp.PMSVersionCommand{Version: 2, Reason: "Allocate current signed settled receipt to the original accepted postings"}, 200)
	pmsRunMaintenance(c)
	var rootState string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT f.state,r.state FROM folios f JOIN reservations r ON r.id=f.reservation_id WHERE f.id=$1`, d.Folio.ID).Scan(&state, &rootState); e != nil || state != "CLOSED" || rootState != "COMPLETED" {
		t.Fatalf("completion: %s/%s %v", state, rootState, e)
	}
	pmsRunMaintenance(c)
	var count int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM hospitality_history WHERE entity_id=$1 AND action='FOLIO_CLOSED' AND created_by IS NULL AND created_by_service='business-operations-service'`, d.Folio.ID).Scan(&count); e != nil || count != 1 {
		t.Fatalf("service actor/dedup %d %v", count, e)
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE folio_close_reviews SET automatic_close=false WHERE id=$1`, review.ID); e == nil {
		t.Fatal("immutable review changed")
	}
	// A delayed readback cannot extend stale financial authority. Outage and
	// restart retain one durable readback and never create a replacement charge.
	c.domain.Auth.Service.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	f := reservationPaymentProvider(c)
	f.mu.Lock()
	f.unavailable = true
	f.mu.Unlock()
	pmsRunMaintenance(c)
	pmsRunMaintenance(c)
	var queued int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM reservation_financial_requests WHERE reservation_id=$1 AND created_by IS NULL AND kind='READBACK'`, v.Reservation.ID).Scan(&queued); e != nil || queued != 1 {
		t.Fatalf("service readback duplicate %d %v", queued, e)
	}
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	var jobState string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM reservation_financial_requests WHERE reservation_id=$1 AND created_by IS NULL`, v.Reservation.ID).Scan(&jobState); e != nil || jobState != "RETRY" {
		t.Fatalf("outage queue %s %v", jobState, e)
	}
	f.mu.Lock()
	f.unavailable = false
	f.mu.Unlock()
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE reservation_financial_requests SET available_at=now(),version=version+1 WHERE reservation_id=$1 AND created_by IS NULL`, v.Reservation.ID); e != nil {
		t.Fatal(e)
	}
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	pmsRunMaintenance(c)
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM folios WHERE id=$1`, d.Folio.ID).Scan(&state); e != nil || state != "CLOSED" {
		t.Fatalf("readback recovery %s %v", state, e)
	}
	f.mu.Lock()
	calls := f.collectionCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("readback repeated collection %d", calls)
	}
	f = reservationPaymentProvider(c)
	f.mu.Lock()
	f.reversed = 1
	f.mu.Unlock()
	pmsReadSettled(c, v)
	pmsRunMaintenance(c)
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM folios WHERE id=$1`, d.Folio.ID).Scan(&state); e != nil || state != "SETTLEMENT_PENDING" {
		t.Fatalf("reversal backing retained %s %v", state, e)
	}
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM hospitality_operation_exceptions WHERE source_id=$1 AND code='PAYMENT_BACKING_REVIEW_REQUIRED' AND state='OPEN'`, d.Folio.ID).Scan(&count); e != nil || count != 1 {
		t.Fatalf("missing durable exception %d %v", count, e)
	}
}
func TestBusinessPMSAutomaticClosureRechecksAuthorityAndPostings(t *testing.T) {
	for _, scenario := range []string{"review_revoked", "membership_suspended", "scope_revoked", "scope_requires_mfa", "postings_changed", "review_expired"} {
		t.Run(scenario, func(t *testing.T) {
			c := newCommercialHTTPForArrival(t, 0)
			v, d := pmsCollectedStay(c)
			pmsDeferredDeparture(c, v, d)
			path := "/folios/" + d.Folio.ID.String()
			review := commercialData[businesshttp.PMSFolioCloseReview](t, c.must(c.owner, "POST", path+"/close-reviews", businesshttp.PMSFolioCloseReviewCommand{Version: 2, AutomaticClose: true, ExpiresAt: time.Now().Add(time.Hour), Reason: "Bounded reviewed closure must fail safely after later authority or source changes"}, 201))
			pmsReadSettled(c, v)
			c.must(c.owner, "POST", path+"/reconcile", businesshttp.PMSVersionCommand{Version: 2, Reason: "Read current signed settlement before testing subsequent revocation"}, 200)
			switch scenario {
			case "review_revoked":
				c.must(c.owner, "POST", "/folio-close-reviews/"+review.ID.String()+"/revoke", businesshttp.PMSVersionCommand{Version: 1, Reason: "Withdraw previously granted automatic folio closure authority"}, 201)
			case "membership_suspended":
				if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_memberships SET status='SUSPENDED' WHERE id=$1`, review.AuthorizingMembershipID); e != nil {
					t.Fatal(e)
				}
			case "scope_revoked":
				if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET revoked_at=now() WHERE organisation_id=$1 AND membership_id=$2 AND capability='folios:write'`, c.owner.registered.OrganisationID, review.AuthorizingMembershipID); e != nil {
					t.Fatal(e)
				}
			case "scope_requires_mfa":
				if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET require_mfa=true WHERE organisation_id=$1 AND membership_id=$2 AND capability='folios:write'`, c.owner.registered.OrganisationID, review.AuthorizingMembershipID); e != nil {
					t.Fatal(e)
				}
			case "postings_changed":
				c.must(c.owner, "POST", path+"/items", businesshttp.PMSFolioPosting{Version: 3, Kind: "MEALS", SourceReference: "late-actual-restaurant-reference", Quantity: 1, AmountMinor: 10, Currency: d.Folio.Currency, PolicyReference: "accepted-menu-v1", Reason: "A real late source posting invalidates the earlier final review"}, 201)
			case "review_expired":
				c.domain.Auth.Service.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
			}
			pmsRunMaintenance(c)
			var state string
			if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM folios WHERE id=$1`, d.Folio.ID).Scan(&state); e != nil || state == "CLOSED" {
				t.Fatalf("unsafe automatic closure %s %v", state, e)
			}
		})
	}
}

func TestBusinessPMSConcurrentFinalCloseUsesCurrentFolioVersion(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	v, d := pmsCollectedStay(c)
	pmsDeferredDeparture(c, v, d)
	pmsReadSettled(c, v)
	path := "/folios/" + d.Folio.ID.String()
	c.must(c.owner, "POST", path+"/reconcile", businesshttp.PMSVersionCommand{Version: 2, Reason: "Bind actual final settled receipt before concurrent close review"}, 200)
	in := businesshttp.PMSVersionCommand{Version: 3, Reason: "Finalize actual departed settled folio exactly once with current version"}
	codes := make(chan int, 2)
	for range 2 {
		go func() { code, _ := c.call(c.owner, "POST", path+"/close", in, nil); codes <- code }()
	}
	a, b := <-codes, <-codes
	if !(a == 200 && b == 409 || a == 409 && b == 200) {
		t.Fatalf("close race %d/%d", a, b)
	}
	outsider := c.user()
	c.must(outsider, "POST", path+"/close", in, 404)
	c.must(c.owner, "GET", "/folio-close-reviews?property_id="+c.property.ID.String(), nil, 200)
	c.must(c.owner, "GET", "/hospitality-exceptions?property_id="+c.property.ID.String(), nil, 200)
}
