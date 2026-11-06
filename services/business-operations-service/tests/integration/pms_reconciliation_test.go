package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth/credentials"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This restores a stale legacy/import projection, not a physical observation.
// Every version/identity/retention/audit/outbox guard remains enabled. All
// owning booking, stay, departure and financial evidence comes from real HTTP.
func pmsLegacyRoomProjection(c *commercialHarness, state string, at *time.Time) {
	c.t.Helper()
	org := c.owner.registered.OrganisationID
	actor := c.owner.registered.AccountID
	e := database.WithTx(c.t.Context(), c.db.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, org); e != nil {
			return e
		}
		var id ids.ID
		var version int64
		if e := tx.QueryRow(ctx, `UPDATE room_status_current SET state=$3,version=version+1,updated_at=coalesce($4::timestamptz,now()),reason='LEGACY_PROJECTION_FIXTURE_TEST_ONLY',evidence_reference='legacy-projection-source-test-only' WHERE organisation_id=$1 AND room_id=$2 RETURNING id,version`, org, c.unit.ID, state, at).Scan(&id, &version); e != nil {
			return e
		}
		change := map[string]any{"fixture": "LEGACY_PROJECTION_TEST_ONLY", "room_id": c.unit.ID, "state": state}
		raw, e := json.Marshal(change)
		if e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO hospitality_history(id,organisation_id,property_id,entity_id,entity_type,entity_version,action,change_context,created_by) VALUES($1,$2,$3,$4,'room_status',$5,'LEGACY_PROJECTION_RESTORED',$6,$7)`, ids.New(), org, c.property.ID, id, version, raw, actor); e != nil {
			return e
		}
		return businessEvents.CommercialCommit(ctx, tx, c.domain.Auth.Service.Outbox, org, c.property.ID, id, actor, "room_status", "LEGACY_PROJECTION_RESTORED", "room.status.changed.v1", version, change)
	})
	if e != nil {
		c.t.Fatal(e)
	}
}
func pmsRoomProof(c *commercialHarness) struct {
	Proof        hospitality.RoomSafetyProof `json:"proof"`
	SourceDigest string                      `json:"source_digest"`
	Plan         hospitality.RoomSafetyPlan  `json:"plan"`
} {
	c.t.Helper()
	return commercialData[struct {
		Proof        hospitality.RoomSafetyProof `json:"proof"`
		SourceDigest string                      `json:"source_digest"`
		Plan         hospitality.RoomSafetyPlan  `json:"plan"`
	}](c.t, c.must(c.owner, "GET", "/room-status/"+c.unit.ID.String()+"/safety-proof", nil, 200))
}
func pmsRunRooms(c *commercialHarness) {
	c.t.Helper()
	if _, e := c.db.Pool.Exec(c.t.Context(), `UPDATE hospitality_room_cursors SET next_check_at=now()-interval '1 second'`); e != nil {
		c.t.Fatal(e)
	}
	if e := c.domain.ProcessPMSRooms(c.t.Context(), 50); e != nil {
		c.t.Fatal(e)
	}
}
func TestBusinessPMSRoomReconciliationRestoresOwnedArrivalWithoutCreatingAnotherStay(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	v, d := pmsCollectedStay(c)
	pmsLegacyRoomProjection(c, "INSPECTED", nil)
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- c.domain.ProcessPMSRooms(t.Context(), 50) }()
	}
	for range 2 {
		if e := <-results; e != nil {
			t.Fatal(e)
		}
	}
	var state string
	var stays, reports int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state,(SELECT count(*) FROM stay_records WHERE reservation_id=$2),(SELECT count(*) FROM room_reconciliation_reports WHERE room_id=$1 AND state='DERIVED_OCCUPANCY') FROM room_status_current WHERE room_id=$1`, c.unit.ID, v.Reservation.ID).Scan(&state, &stays, &reports); e != nil || state != "OCCUPIED" || stays != 1 || reports != 1 {
		t.Fatalf("derived source duplicated or guessed a stay %s/%d/%d %v", state, stays, reports, e)
	}
	proof := pmsRoomProof(c)
	if proof.Proof.ActiveStayCount != 1 || len(proof.Plan.Issues) != 0 {
		t.Fatalf("actual source not consistent %+v", proof)
	}
	// Neither a fresh inspection nor an unapproved vacancy observation can
	// override a genuine active stay, even when its projection was repaired.
	c.must(c.owner, "PUT", "/room-status/"+c.unit.ID.String(), businesshttp.PMSRoomStatusCommand{PropertyID: c.property.ID, RoomID: c.unit.ID, ExpectedVersion: proof.Proof.RoomVersion, State: "VACANT_DIRTY", EvidenceReference: "actual-active-guest-must-not-be-hidden", Reason: "Attempted physical vacancy cannot replace the owning active stay"}, 409)
	c.must(c.owner, "POST", "/room-status/"+c.unit.ID.String()+"/reconcile", businesshttp.PMSRoomReconcileCommand{ExpectedRoomVersion: proof.Proof.RoomVersion, SourceDigest: proof.SourceDigest, Observation: "OBSERVED_VACANT_DIRTY", EvidenceReference: "cannot-evict-actual-guest-by-observation", Reason: "A qualified observation cannot rewrite genuine physical occupancy"}, 409)
	if d.Folio.Currency != v.Reservation.Currency {
		t.Fatal("owning finance reference changed")
	}
	pmsAutomationContract(t, "PMSRoomReconciliationList", c.must(c.owner, "GET", "/room-reconciliations", nil, 200))
}
func TestBusinessPMSRoomReconciliationOnlyDirtiesAfterRealEvidencedDeparture(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	v, d := pmsCollectedStay(c)
	var old time.Time
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT updated_at FROM room_status_current WHERE room_id=$1`, c.unit.ID).Scan(&old); e != nil {
		t.Fatal(e)
	}
	pmsDeferredDeparture(c, v, d)
	pmsLegacyRoomProjection(c, "OCCUPIED", &old)
	pmsRunRooms(c)
	var state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM room_status_current WHERE room_id=$1`, c.unit.ID).Scan(&state); e != nil || state != "VACANT_DIRTY" {
		t.Fatalf("uninspected room declared ready %s %v", state, e)
	}
	proof := pmsRoomProof(c)
	if proof.Proof.ActiveStayCount != 0 || proof.Proof.LatestDeparture == nil || !proof.Proof.LatestDeparture.HasDepartureEvidence {
		t.Fatalf("departure evidence lost %+v", proof)
	}
}
func TestBusinessPMSUnknownVacancyHoldsStockUntilExactIndependentObservation(t *testing.T) {
	c := newCommercialHTTP(t)
	pmsGroupReviewer(c)
	pmsReady(c)
	pmsLegacyRoomProjection(c, "OCCUPIED", nil)
	pmsRunRooms(c)
	pool, e := inventoryPoolForRoomTest(c)
	if e != nil || !pool {
		t.Fatalf("uncertain vacancy exposed inventory %v %v", pool, e)
	}
	source := pmsRoomProof(c)
	// Protective side-state transitions cannot become a one-person path around
	// the independently reviewed vacancy correction for an unproven occupant.
	c.must(c.owner, "PUT", "/room-status/"+c.unit.ID.String(), businesshttp.PMSRoomStatusCommand{PropertyID: c.property.ID, RoomID: c.unit.ID, ExpectedVersion: source.Proof.RoomVersion, State: "BLOCKED", EvidenceReference: "unproven-occupancy-cannot-be-cleared-through-maintenance", Reason: "An unresolved occupant must retain its distinct source review controls"}, 409)
	in := businesshttp.PMSRoomReconcileCommand{ExpectedRoomVersion: source.Proof.RoomVersion, SourceDigest: source.SourceDigest, Observation: "OBSERVED_VACANT_DIRTY", EvidenceReference: "qualified-independent-empty-room-observation", Reason: "Qualified physical inspection confirms current vacancy without inventing past guest or payment facts"}
	path := "/room-status/" + c.unit.ID.String() + "/reconcile"
	c.must(c.owner, "POST", path, in, 403)
	op := in
	in.ApprovalID = c.approval("COMMERCIAL_PMS_OCCUPANCY_CORRECTION", "pms:approve", "", nil, "", map[string]any{"room_id": c.unit.ID, "command": op})
	key := ids.New().String()
	code, raw := c.call(c.owner, "POST", path, in, map[string]string{"Idempotency-Key": key})
	if code != 200 {
		t.Fatalf("qualified observation %d %s", code, raw)
	}
	pmsAutomationContract(t, "PMSRoomReconcileResult", raw)
	out := commercialData[businesshttp.PMSRoomReconcileResult](t, raw)
	if out.RoomState != "VACANT_DIRTY" || out.SafetyHold || !out.Changed || out.Report.State != "OPERATOR_OBSERVATION" {
		t.Fatalf("unsafe observation result %+v", out)
	}
	pool, e = inventoryPoolForRoomTest(c)
	if e != nil || pool {
		t.Fatalf("source hold was not reconciled %v %v", pool, e)
	}
	c.must(c.owner, "POST", path, in, 409)
	if _, e = c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET revoked_at=now() WHERE organisation_id=$1 AND membership_id=(SELECT id FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2) AND capability='room_status:inspect'`, c.owner.registered.OrganisationID, c.owner.registered.AccountID); e != nil {
		t.Fatal(e)
	}
	if code, _ = c.call(c.owner, "POST", path, in, map[string]string{"Idempotency-Key": key}); code != 403 {
		t.Fatalf("revoked inspection authority replayed high-risk result %d", code)
	}
}
func inventoryPoolForRoomTest(c *commercialHarness) (bool, error) {
	var held bool
	e := c.db.Pool.QueryRow(c.t.Context(), `SELECT operational_hold FROM inventory_pools WHERE id=$1`, c.pool.ID).Scan(&held)
	return held, e
}
func TestBusinessPMSOccupiedHazardCannotBeClearedByInspectionOrCheckout(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	v, d := pmsCollectedStay(c)
	source := pmsRoomProof(c)
	status := businesshttp.PMSRoomStatusCommand{PropertyID: c.property.ID, RoomID: c.unit.ID, ExpectedVersion: source.Proof.RoomVersion, State: "OUT_OF_ORDER", EvidenceReference: "actual-in-house-maintenance-hazard", Reason: "Protect an occupied room while retaining the actual stay and financial obligations"}
	current := commercialData[hospitality.RoomStatus](t, c.must(c.owner, "PUT", "/room-status/"+c.unit.ID.String(), status, 200))
	pmsRunRooms(c)
	status.ExpectedVersion = current.Version
	status.State = "VACANT_DIRTY"
	c.must(c.owner, "PUT", "/room-status/"+c.unit.ID.String(), status, 409)
	pmsDeferredDeparture(c, v, d)
	pmsRunRooms(c)
	var state string
	var version int64
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state,version FROM room_status_current WHERE room_id=$1`, c.unit.ID).Scan(&state, &version); e != nil || state != "OUT_OF_ORDER" {
		t.Fatalf("departure erased actual maintenance %s %v", state, e)
	}
	held, e := inventoryPoolForRoomTest(c)
	if e != nil || !held {
		t.Fatalf("maintenance exposure restored before repair %v %v", held, e)
	}
	status.ExpectedVersion = version
	status.State = "VACANT_DIRTY"
	status.EvidenceReference = "actual-qualified-repair-release-reference"
	status.Reason = "Qualified supervisor clears the separately evidenced hazard after physical departure"
	c.must(c.owner, "PUT", "/room-status/"+c.unit.ID.String(), status, 200)
	pmsRunRooms(c)
	held, e = inventoryPoolForRoomTest(c)
	if e != nil || held {
		t.Fatalf("verified local hazard resolution retained stale source hold %v %v", held, e)
	}
}
func TestBusinessPMSOverduePhysicalOccupancyHoldsStockUntilRealDeparture(t *testing.T) {
	// Only this disposable provider fixture gets a long authorization horizon;
	// the standard fixture's one-hour expiry and all expiry tests stay unchanged.
	c := newCommercialHTTPWithVerificationTTL(t, 0, 14*24*time.Hour)
	v, d := pmsCollectedStay(c)
	due, e := hospitality.LocalInstant(v.Reservation.DateTo, v.Reservation.PolicySnapshot.CheckOutLocal, v.Reservation.PolicySnapshot.AcceptanceTimezone)
	if e != nil {
		t.Fatal(e)
	}
	c.domain.Auth.Service.Now = func() time.Time { return due.Add(time.Minute) }
	pmsRunRooms(c)
	held, e := inventoryPoolForRoomTest(c)
	if e != nil || !held {
		t.Fatalf("overdue physical guest exposed future stock %v %v", held, e)
	}
	// Perform real TOTP step-up at the controlled clock; no MFA timestamp,
	// permission or owning guest record is fabricated to obtain the decision.
	for _, u := range []*businessUser{c.owner, c.approver} {
		if e = c.service.StepUp(t.Context(), u.registered.AccountID, u.tokens.SessionID, (credentials.TOTP{}).Code(u.totpSecret, credentials.Step(c.service.Now())), false); e != nil {
			t.Fatal(e)
		}
	}
	pmsDeferredDeparture(c, v, d)
	pmsRunRooms(c)
	held, e = inventoryPoolForRoomTest(c)
	if e != nil || held {
		t.Fatalf("actual departure did not resolve overdue occupancy %v %v", held, e)
	}
	var state string
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT state FROM room_status_current WHERE room_id=$1`, c.unit.ID).Scan(&state); e != nil || state != "VACANT_DIRTY" {
		t.Fatalf("departure skipped inspection %s %v", state, e)
	}
}

func TestBusinessPMSTerminalUnusedAssignmentCleanupRetainsCanonicalMoneyAndStock(t *testing.T) {
	c := newCommercialHTTP(t)
	v, _ := pmsConfirmed(c)
	ready := pmsReady(c)
	a := pmsAssign(c, v, ready)
	c.must(c.owner, "POST", "/room-assignments/"+a.ID.String()+"/release", businesshttp.PMSVersionCommand{Version: a.Version, Reason: "Release real unoccupied physical allocation before the canonical cancellation"}, 200)
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/cancel", map[string]any{"version": v.Reservation.Version, "reason": "Cancel the actual original accepted obligation without inferring a financial refund"}, 200)
	// Restore only a stale physical assignment projection. Its original released
	// night rows and the genuine canonical cancellation remain immutable.
	e := database.WithTx(t.Context(), c.db.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, c.owner.registered.OrganisationID); e != nil {
			return e
		}
		var version int64
		if e := tx.QueryRow(ctx, `UPDATE room_assignments SET state='ASSIGNED',version=version+1,updated_at=now(),reason='STALE_ASSIGNMENT_PROJECTION_TEST_ONLY' WHERE id=$1 RETURNING version`, a.ID).Scan(&version); e != nil {
			return e
		}
		raw, _ := json.Marshal(map[string]any{"fixture": "STALE_ASSIGNMENT_PROJECTION_TEST_ONLY"})
		if _, e := tx.Exec(ctx, `INSERT INTO hospitality_history(id,organisation_id,property_id,entity_id,entity_type,entity_version,action,change_context,created_by) VALUES($1,$2,$3,$4,'room_assignment',$5,'LEGACY_ASSIGNMENT_PROJECTION_RESTORED',$6,$7)`, ids.New(), c.owner.registered.OrganisationID, c.property.ID, a.ID, version, raw, c.owner.registered.AccountID); e != nil {
			return e
		}
		return businessEvents.CommercialCommit(ctx, tx, c.domain.Auth.Service.Outbox, c.owner.registered.OrganisationID, c.property.ID, a.ID, c.owner.registered.AccountID, "room_assignment", "LEGACY_ASSIGNMENT_PROJECTION_RESTORED", "hospitality.room.assigned.v1", version, map[string]any{"fixture": "STALE_ASSIGNMENT_PROJECTION_TEST_ONLY"})
	})
	if e != nil {
		t.Fatal(e)
	}
	pmsRunRooms(c)
	var state, rootState string
	var committed int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT a.state,r.state,(SELECT coalesce(sum(committed),0) FROM inventory_calendar WHERE pool_id=$2) FROM room_assignments a JOIN reservations r ON r.id=a.reservation_id WHERE a.id=$1`, a.ID, c.pool.ID).Scan(&state, &rootState, &committed); e != nil || state != "RELEASED" || rootState != "CANCELLED" || committed != 0 {
		t.Fatalf("cleanup recreated stock or changed canonical obligations %s/%s/%d %v", state, rootState, committed, e)
	}
}

func TestBusinessPMSRoomSourceFailureHoldsStockQuarantinesAndAllowsOtherRoomsToProgress(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	second := pmsTwoRoomStock(c)
	_, _ = pmsCollectedStay(c)
	first := c.unit
	c.unit = second
	pmsReady(c)
	c.unit = first
	role := "cf_room_source_" + strings.ReplaceAll(ids.New().String(), "-", "_")
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
	for _, sql := range []string{"GRANT USAGE ON SCHEMA public TO " + quoted, "GRANT SELECT,INSERT,UPDATE ON ALL TABLES IN SCHEMA public TO " + quoted, "REVOKE SELECT ON reservation_inventory_days FROM " + quoted} {
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
	if e = c.domain.ProcessPMSRooms(t.Context(), 50); e == nil || !strings.Contains(e.Error(), "reservation_inventory_days") {
		t.Fatalf("real source failure was hidden %v", e)
	}
	held, e := inventoryPoolForRoomTest(c)
	if e != nil || !held {
		t.Fatalf("unknown source continued exposing capacity %v %v", held, e)
	}
	var control int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM room_reconciliation_reports WHERE room_id=$1 AND state='CONSISTENT'`, second.ID).Scan(&control); e != nil || control != 1 {
		t.Fatalf("healthy room starved %d %v", control, e)
	}
	if e = c.domain.ProcessPMSRooms(t.Context(), 50); e != nil {
		t.Fatalf("backoff hot-looped source failure %v", e)
	}
	for range 11 {
		if _, e = c.db.Pool.Exec(t.Context(), `UPDATE hospitality_room_cursors SET next_check_at=now()-interval '1 second' WHERE room_id=$1`, first.ID); e != nil {
			t.Fatal(e)
		}
		if e = c.domain.ProcessPMSRooms(t.Context(), 50); e == nil {
			t.Fatal("unfixed source stopped reporting failure")
		}
	}
	var state string
	var failures int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT state,consecutive_failures FROM hospitality_room_cursors WHERE room_id=$1`, first.ID).Scan(&state, &failures); e != nil || state != "QUARANTINED" || failures != 12 {
		t.Fatalf("source quarantine lost %s/%d %v", state, failures, e)
	}
	if _, e = c.db.Pool.Exec(t.Context(), "GRANT SELECT ON reservation_inventory_days TO "+quoted); e != nil {
		t.Fatal(e)
	}
	if e = c.domain.ProcessPMSRooms(t.Context(), 50); e != nil {
		t.Fatalf("quarantined source silently retried %v", e)
	}
	c.domain.Auth.Service.Pool = original
	proof := pmsRoomProof(c)
	in := businesshttp.PMSRoomReconcileCommand{ExpectedRoomVersion: proof.Proof.RoomVersion, SourceDigest: proof.SourceDigest, Reason: "Reconcile verified current owning source after repairing the actual missing database permission"}
	out := commercialData[businesshttp.PMSRoomReconcileResult](t, c.must(c.owner, "POST", "/room-status/"+first.ID.String()+"/reconcile", in, 200))
	if out.SafetyHold || out.RoomState != "OCCUPIED" {
		t.Fatalf("source repair changed physical truth %+v", out)
	}
	held, e = inventoryPoolForRoomTest(c)
	if e != nil || held {
		t.Fatalf("verified repair retained source quarantine hold %v %v", held, e)
	}
	pmsAutomationContract(t, "PMSRoomReconciliationList", c.must(c.owner, "GET", "/room-reconciliations", nil, 200))
}
