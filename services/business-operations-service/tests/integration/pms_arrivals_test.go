package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func pmsArrivalFixture(t *testing.T, automatic bool) (*commercialHarness, time.Time, businesshttp.ReservationPolicyView) {
	t.Helper()
	loc, e := time.LoadLocation("Africa/Lagos")
	if e != nil {
		t.Fatal(e)
	}
	target := time.Now().Add(2 * time.Minute)
	offset := 0
	if target.In(loc).Format(time.DateOnly) != time.Now().In(loc).Format(time.DateOnly) {
		offset = 1
	}
	c := newCommercialHTTPForArrival(t, offset)
	pmsGroupReviewer(c)
	pmsTwoRoomStock(c)
	policy := reservationPolicyFixture(c, false)
	local, e := time.LoadLocation(c.property.Timezone)
	if e != nil {
		t.Fatal(e)
	}
	target = time.Now().Add(2 * time.Minute)
	policy = pmsArrivalAcceptedPolicy(c, policy, automatic, target.In(local).Format("15:04"))
	deadline, e := hospitality.LocalInstant(c.dates.DateFrom, policy.Definition.CheckInLocal, c.property.Timezone)
	if e != nil {
		t.Fatal(e)
	}
	return c, deadline, policy
}
func pmsArrivalAcceptedPolicy(c *commercialHarness, current businesshttp.ReservationPolicyView, automatic bool, arrival string) businesshttp.ReservationPolicyView {
	c.t.Helper()
	definition := current.Definition
	definition.AutomaticNoShow = automatic
	definition.NoShowGraceHours = 0
	definition.CheckInLocal = arrival
	in := businesshttp.ReservationPolicyCommand{PropertyID: c.property.ID, CurrentPolicyID: &current.ID, ExpectedVersion: current.Version, Definition: definition, EffectiveAt: time.Now().Add(-time.Minute), Reason: "Explicit independent review of the future accepted no-show deadline and opt-in terms"}
	op := in
	in.ApprovalID = c.approval("COMMERCIAL_RESERVATION_POLICY_CHANGE", "reservations:approve", "", nil, "", map[string]any{"command": op})
	return commercialData[businesshttp.ReservationPolicyView](c.t, c.must(c.owner, "POST", "/reservation-policies", in, 201))
}
func pmsArrivalBooking(c *commercialHarness, collected bool) (reservations.Detail, crmRegisteredGuest) {
	c.t.Helper()
	g := crmGuestFixture(c)
	cmd := reservationCommandFixture(c, false)
	cmd.SourceReference = "arrival-booker:" + ids.New().String()
	cmd.Guests = []reservations.GuestCommand{{GuestID: &g.Guest.ID, SourceKind: g.SourceKind, SourceReference: g.SourceReference, IsPrimary: true}}
	if !collected {
		cmd.PaymentResponsibility = "PAY_AT_PROPERTY"
		q, e := loadTestQuote(c, cmd.Rooms[0].QuoteID)
		if e != nil {
			c.t.Fatal(e)
		}
		op := cmd
		cmd.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &q.AmountMinor, q.Currency, map[string]any{"command": op})
	}
	v := commercialData[reservations.Detail](c.t, c.must(c.owner, "POST", "/reservations", cmd, 201))
	if collected {
		c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/payment-requests", reservationPaymentCommand(c, v), 202)
		if e := c.domain.ProcessReservationFinance(c.t.Context(), 1); e != nil {
			c.t.Fatal(e)
		}
		v = commercialData[reservations.Detail](c.t, c.must(c.owner, "GET", "/reservations/"+v.Reservation.ID.String(), nil, 200))
	}
	v = commercialData[reservations.Detail](c.t, c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/confirm", reservations.Command{Version: v.Reservation.Version, Reason: "Confirm actual accepted stock and financial references before the bounded no-show workflow"}, 200))
	return v, g
}

// Read the actual immutable quote used by the command, without repricing it.
func loadTestQuote(c *commercialHarness, id ids.ID) (struct {
	AmountMinor int64
	Currency    string
}, error) {
	var q struct {
		AmountMinor int64
		Currency    string
	}
	e := c.db.Pool.QueryRow(c.t.Context(), `SELECT amount_minor,currency FROM price_quotes WHERE organisation_id=$1 AND id=$2`, c.owner.registered.OrganisationID, id).Scan(&q.AmountMinor, &q.Currency)
	return q, e
}
func pmsArrivalGrant(c *commercialHarness, expected int64, per, total int64) businesshttp.PMSArrivalPolicy {
	c.t.Helper()
	now := c.domain.Auth.Service.Now().UTC()
	in := businesshttp.PMSArrivalPolicyCommand{PropertyID: c.property.ID, ExpectedVersion: expected, Mode: "AUTOMATIC_NO_SHOW", AllowedSources: []string{"DIRECT", "WALK_IN", "GROUP", "CORPORATE"}, MaxReservationsPerRun: 5, MaxReservationMinor: per, MaxTotalMinor: total, Currency: c.property.Currency, EffectiveAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Reason: "Authorize bounded current no-show processing without creating or executing financial remedies"}
	op := in
	in.ApprovalID = c.approval("COMMERCIAL_PMS_NO_SHOW_AUTOMATION", "reservations:approve", "", &total, in.Currency, map[string]any{"command": op})
	return commercialData[businesshttp.PMSArrivalPolicy](c.t, c.must(c.owner, "POST", "/hospitality-arrival-automation", in, 201))
}
func TestBusinessPMSAutomaticNoShowConservesAcceptedStockFeesAndUnoccupiedAssignments(t *testing.T) {
	c, deadline, _ := pmsArrivalFixture(t, true)
	v, _ := pmsArrivalBooking(c, true)
	ready := pmsReady(c)
	assignment := pmsAssign(c, v, ready)
	p := pmsArrivalGrant(c, 0, v.Reservation.AmountMinor, v.Reservation.AmountMinor)
	if e := c.domain.ProcessPMSArrivals(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	var state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM reservations WHERE id=$1`, v.Reservation.ID).Scan(&state); e != nil || state != "CONFIRMED" {
		t.Fatalf("early no-show %s %v", state, e)
	}
	c.domain.Auth.Service.Now = func() time.Time { return deadline.Add(time.Second) }
	outcomes := make(chan error, 2)
	for range 2 {
		go func() { outcomes <- c.domain.ProcessPMSArrivals(t.Context(), 50) }()
	}
	for range 2 {
		if e := <-outcomes; e != nil {
			t.Fatal(e)
		}
	}
	current := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+v.Reservation.ID.String(), nil, 200))
	if current.Reservation.State != "NO_SHOW" || current.Reservation.AmountMinor != v.Reservation.AmountMinor {
		t.Fatalf("false state or repricing %+v", current.Reservation)
	}
	var runs, stays, refunds int
	var accepted, penalty int64
	var financial string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM hospitality_arrival_runs WHERE policy_id=$1 AND state='MARKED_NO_SHOW'),(SELECT count(*) FROM stay_records WHERE reservation_id=$2),(SELECT count(*) FROM reservation_financial_requests WHERE reservation_id=$2 AND kind='REFUND' AND state='DEPENDENCY_BLOCKED'),accepted_amount_minor,penalty_minor,financial_state FROM reservation_cancellation_obligations WHERE reservation_id=$2`, p.ID, v.Reservation.ID).Scan(&runs, &stays, &refunds, &accepted, &penalty, &financial); e != nil || runs != 1 || stays != 0 || refunds != 1 || accepted != v.Reservation.AmountMinor || penalty <= 0 || penalty >= accepted || financial != "KNOWN_PLATFORM_REFERENCE" {
		t.Fatalf("no-show invented a stay or financial execution %d/%d/%d %d/%d %s %v", runs, stays, refunds, accepted, penalty, financial, e)
	}
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM room_assignments WHERE id=$1`, assignment.ID).Scan(&state); e != nil || state != "RELEASED" {
		t.Fatalf("unoccupied assignment retained %s %v", state, e)
	}
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM room_status_current WHERE room_id=$1`, c.unit.ID).Scan(&state); e != nil || state != "INSPECTED" {
		t.Fatalf("no-show changed physical cleanliness %s %v", state, e)
	}
	var retained, released int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT committed FROM inventory_calendar WHERE pool_id=$1 AND day=$2::date),(SELECT committed FROM inventory_calendar WHERE pool_id=$1 AND day=$3::date-1)`, c.pool.ID, v.Reservation.DateFrom, v.Reservation.DateTo).Scan(&retained, &released); e != nil || retained != 1 || released != 0 {
		t.Fatalf("accepted retained-night stock changed %d/%d %v", retained, released, e)
	}
	var actor *ids.ID
	var service string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT created_by,created_by_service FROM reservation_versions WHERE reservation_id=$1 AND action='NO_SHOW'`, v.Reservation.ID).Scan(&actor, &service); e != nil || actor != nil || service != "business-operations-service" {
		t.Fatalf("fabricated human no-show %v/%s %v", actor, service, e)
	}
	provider := reservationPaymentProvider(c)
	provider.mu.Lock()
	calls := provider.collectionCalls
	provider.mu.Unlock()
	if calls != 1 {
		t.Fatalf("no-show executed another collection %d", calls)
	}
	pmsAutomationContract(t, "PMSArrivalPolicyList", c.must(c.owner, "GET", "/hospitality-arrival-automation", nil, 200))
	pmsAutomationContract(t, "PMSArrivalRunList", c.must(c.owner, "GET", "/hospitality-arrival-runs", nil, 200))
	audit := commercialData[businesshttp.PMSAuditResult](t, c.must(c.owner, "POST", "/night-audit/validate", businesshttp.PMSAuditCommand{PropertyID: c.property.ID, Reason: "An accepted no-show does not settle its pending owning-service financial remedy"}, 201))
	blocked := false
	for _, ex := range audit.Snapshot.Exceptions {
		if ex.Code == "PENDING_RESERVATION_FINANCIAL_REMEDIES" && ex.HardBlock {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("no-show silently cleared unresolved financial close requirements")
	}
}
func TestBusinessPMSAutomaticNoShowRespectsCumulativeApprovedExposure(t *testing.T) {
	c, deadline, _ := pmsArrivalFixture(t, true)
	a, _ := pmsArrivalBooking(c, false)
	pmsArrivalBooking(c, false)
	pmsArrivalGrant(c, 0, a.Reservation.AmountMinor, a.Reservation.AmountMinor)
	c.domain.Auth.Service.Now = func() time.Time { return deadline.Add(time.Second) }
	if e := c.domain.ProcessPMSArrivals(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	var marked, limited int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE state='MARKED_NO_SHOW'),count(*) FILTER(WHERE state='BUDGET_EXHAUSTED') FROM hospitality_arrival_runs`).Scan(&marked, &limited); e != nil || marked != 1 || limited != 1 {
		t.Fatalf("cumulative policy exposure exceeded %d/%d %v", marked, limited, e)
	}
}
func TestBusinessPMSAutomaticNoShowNeverRewritesOldAcceptedTerms(t *testing.T) {
	c, deadline, policy := pmsArrivalFixture(t, false)
	v, _ := pmsArrivalBooking(c, false)
	pmsArrivalAcceptedPolicy(c, policy, true, policy.Definition.CheckInLocal)
	pmsArrivalGrant(c, 0, v.Reservation.AmountMinor, v.Reservation.AmountMinor)
	c.domain.Auth.Service.Now = func() time.Time { return deadline.Add(time.Second) }
	if e := c.domain.ProcessPMSArrivals(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	var state string
	var automatic bool
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state,coalesce((policy_snapshot->>'automatic_no_show')::boolean,false) FROM reservations WHERE id=$1`, v.Reservation.ID).Scan(&state, &automatic); e != nil || state != "CONFIRMED" || automatic {
		t.Fatalf("accepted terms changed %s/%v %v", state, automatic, e)
	}
}
func TestBusinessPMSAutomaticNoShowStopsOnRevocationOrChangedAuthority(t *testing.T) {
	for _, scenario := range []string{"revoked", "latest_revoked_no_fallback", "expired", "membership_suspended", "scope_requires_mfa", "physical_stay"} {
		t.Run(scenario, func(t *testing.T) {
			c, deadline, _ := pmsArrivalFixture(t, true)
			v, g := pmsArrivalBooking(c, false)
			p := pmsArrivalGrant(c, 0, v.Reservation.AmountMinor, v.Reservation.AmountMinor)
			switch scenario {
			case "revoked", "latest_revoked_no_fallback":
				if scenario == "latest_revoked_no_fallback" {
					p = pmsArrivalGrant(c, 1, v.Reservation.AmountMinor, v.Reservation.AmountMinor)
				}
				c.must(c.owner, "POST", "/hospitality-arrival-automation/"+p.ID.String()+"/revoke", businesshttp.PMSVersionCommand{Version: p.Version, Reason: "Withdraw the current scoped arrival automation grant immediately"}, 201)
			case "membership_suspended":
				if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_memberships SET status='SUSPENDED' WHERE id=$1`, p.AuthorizingMembershipID); e != nil {
					t.Fatal(e)
				}
			case "scope_requires_mfa":
				if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET require_mfa=true WHERE membership_id=$1 AND capability='reservations:cancel'`, p.AuthorizingMembershipID); e != nil {
					t.Fatal(e)
				}
			case "physical_stay":
				ready := pmsReady(c)
				a := pmsAssign(c, v, ready)
				in := businesshttp.PMSCheckInCommand{AssignmentID: a.ID, AssignmentVersion: a.Version, ReservationVersion: v.Reservation.Version, RoomStatusVersion: ready.Version, GuestID: g.Guest.ID, RegistrationReference: "actual-arrival-prevents-no-show", Reason: "Record real physical arrival before the accepted no-show deadline"}
				op := in
				in.ApprovalID = c.approval("COMMERCIAL_PMS_EARLY_CHECK_IN", "pms:approve", "", nil, "", map[string]any{"command": op})
				c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/check-in", in, 201)
			}
			after := deadline.Add(time.Second)
			if scenario == "expired" {
				after = deadline.Add(2 * time.Hour)
			}
			c.domain.Auth.Service.Now = func() time.Time { return after }
			if e := c.domain.ProcessPMSArrivals(t.Context(), 50); e != nil {
				t.Fatal(e)
			}
			var state string
			if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM reservations WHERE id=$1`, v.Reservation.ID).Scan(&state); e != nil || state == "NO_SHOW" {
				t.Fatalf("unsafe no-show after %s: %s %v", scenario, state, e)
			}
		})
	}
}
func TestBusinessPMSAutomaticNoShowRollsBackFailedAssignmentAndDoesNotStarveAnotherBooking(t *testing.T) {
	c, deadline, _ := pmsArrivalFixture(t, true)
	first, _ := pmsArrivalBooking(c, false)
	second, _ := pmsArrivalBooking(c, false)
	ready := pmsReady(c)
	a := pmsAssign(c, first, ready)
	pmsArrivalGrant(c, 0, first.Reservation.AmountMinor, 2*first.Reservation.AmountMinor)
	c.domain.Auth.Service.Now = func() time.Time { return deadline.Add(time.Second) }
	role := "cf_arrival_source_" + strings.ReplaceAll(ids.New().String(), "-", "_")
	quoted := pgx.Identifier{role}.Sanitize()
	if _, e := c.db.Pool.Exec(t.Context(), "CREATE ROLE "+quoted+" NOLOGIN"); e != nil {
		t.Fatal(e)
	}
	var restricted *pgxpool.Pool
	original := c.domain.Auth.Service.Pool
	t.Cleanup(func() {
		c.domain.Auth.Service.Pool = original
		if restricted != nil {
			restricted.Close()
		}
		if _, e := c.db.Pool.Exec(context.Background(), "DROP OWNED BY "+quoted); e != nil {
			t.Error(e)
		}
		if _, e := c.db.Pool.Exec(context.Background(), "DROP ROLE "+quoted); e != nil {
			t.Error(e)
		}
	})
	for _, sql := range []string{"GRANT USAGE ON SCHEMA public TO " + quoted, "GRANT SELECT,INSERT,UPDATE ON ALL TABLES IN SCHEMA public TO " + quoted, "REVOKE UPDATE ON room_assignment_days FROM " + quoted} {
		if _, e := c.db.Pool.Exec(t.Context(), sql); e != nil {
			t.Fatal(e)
		}
	}
	config := c.db.Pool.Config()
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error { _, e := conn.Exec(ctx, "SET ROLE "+quoted); return e }
	var e error
	restricted, e = pgxpool.NewWithConfig(t.Context(), config)
	if e != nil {
		t.Fatal(e)
	}
	c.domain.Auth.Service.Pool = restricted
	if e = c.domain.ProcessPMSArrivals(t.Context(), 50); e == nil || !strings.Contains(e.Error(), "room_assignment_days") {
		t.Fatalf("failed source was hidden: %v", e)
	}
	var firstState, secondState, assignmentState string
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT state FROM reservations WHERE id=$1),(SELECT state FROM reservations WHERE id=$2),(SELECT state FROM room_assignments WHERE id=$3)`, first.Reservation.ID, second.Reservation.ID, a.ID).Scan(&firstState, &secondState, &assignmentState); e != nil || firstState != "CONFIRMED" || secondState != "NO_SHOW" || assignmentState != "ASSIGNED" {
		t.Fatalf("partial writes or starvation %s/%s/%s %v", firstState, secondState, assignmentState, e)
	}
	c.domain.Auth.Service.Pool = original
	if _, e = c.db.Pool.Exec(t.Context(), "GRANT UPDATE ON room_assignment_days TO "+quoted); e != nil {
		t.Fatal(e)
	}
	pmsArrivalGrant(c, 1, first.Reservation.AmountMinor, 2*first.Reservation.AmountMinor)
	if e = c.domain.ProcessPMSArrivals(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT state FROM reservations WHERE id=$1`, first.Reservation.ID).Scan(&firstState); e != nil || firstState != "NO_SHOW" {
		t.Fatalf("reviewed recovery failed %s %v", firstState, e)
	}
}

func TestBusinessPMSAutomaticNoShowRequiresReviewOfConfirmationAfterAcceptedDeadline(t *testing.T) {
	c, deadline, _ := pmsArrivalFixture(t, true)
	// Confirmation after the original arrival deadline is an actual late booking,
	// not evidence of a guest failing to arrive under an earlier confirmation.
	c.domain.Auth.Service.Now = func() time.Time { return deadline.Add(time.Second) }
	v, _ := pmsArrivalBooking(c, false)
	pmsArrivalGrant(c, 0, v.Reservation.AmountMinor, v.Reservation.AmountMinor)
	if e := c.domain.ProcessPMSArrivals(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	var state, code string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT r.state,run.evidence->>'code' FROM reservations r JOIN hospitality_arrival_runs run ON run.organisation_id=r.organisation_id AND run.reservation_id=r.id WHERE r.id=$1`, v.Reservation.ID).Scan(&state, &code); e != nil || state != "CONFIRMED" || code != "LATE_BOOKING_REQUIRES_MANUAL_ARRIVAL_REVIEW" {
		t.Fatalf("late confirmation was treated as a no-show %s/%s %v", state, code, e)
	}
}

func TestBusinessPMSArrivalPolicyRequiresExactApprovalAndRejectsUnsupportedRemoteAutomation(t *testing.T) {
	c, _, _ := pmsArrivalFixture(t, true)
	now := c.domain.Auth.Service.Now().UTC()
	in := businesshttp.PMSArrivalPolicyCommand{PropertyID: c.property.ID, Mode: "AUTOMATIC_NO_SHOW", AllowedSources: []string{"DIRECT"}, MaxReservationsPerRun: 5, MaxReservationMinor: 100000, MaxTotalMinor: 200000, Currency: c.property.Currency, EffectiveAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Reason: "A bounded no-show policy cannot self-authorize or acquire remote booking authority"}
	c.must(c.owner, "POST", "/hospitality-arrival-automation", in, 403)
	bad := in
	bad.AllowedSources = []string{"OTA"}
	c.must(c.owner, "POST", "/hospitality-arrival-automation", bad, 400)
	op := in
	in.ApprovalID = c.approval("COMMERCIAL_PMS_NO_SHOW_AUTOMATION", "reservations:approve", "", &in.MaxTotalMinor, in.Currency, map[string]any{"command": op})
	in.MaxTotalMinor++
	c.must(c.owner, "POST", "/hospitality-arrival-automation", in, 403)
	var consumed *time.Time
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT consumed_at FROM approval_requests WHERE id=$1`, in.ApprovalID).Scan(&consumed); e != nil || consumed != nil {
		t.Fatalf("mismatched command consumed approval %v %v", consumed, e)
	}
}
