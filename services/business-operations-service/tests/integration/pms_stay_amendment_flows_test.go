package integration

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// amendSnapshot counts every record an extension would create or change.
func (x *pmsAmendFixture) amendSnapshot(days []string) []int {
	x.c.t.Helper()
	out := []int{
		x.count(`SELECT count(*) FROM reservation_rooms WHERE reservation_id=$1`, x.v.Reservation.ID),
		x.count(`SELECT count(*) FROM reservation_financial_groups WHERE reservation_id=$1`, x.v.Reservation.ID),
		x.count(`SELECT count(*) FROM folio_items WHERE folio_id=$1`, x.folios[0]),
		x.count(`SELECT count(*) FROM room_assignments WHERE reservation_id=$1`, x.v.Reservation.ID),
		x.count(`SELECT count(*) FROM stay_extensions WHERE stay_id=$1`, x.stays[0]),
		x.count(`SELECT version::integer FROM stay_records WHERE id=$1`, x.stays[0]),
		x.count(`SELECT version::integer FROM reservations WHERE id=$1`, x.v.Reservation.ID),
		x.count(`SELECT count(*) FROM inventory_holds WHERE organisation_id=$1 AND state='ACTIVE'`, x.c.owner.registered.OrganisationID),
	}
	for _, day := range days {
		committed, provenance := x.stockState(day)
		out = append(out, committed, provenance)
	}
	return out
}

// Conflicting inventory or a mismatched quote rejects the extension without
// leaving any stock, folio, assignment, financial or version change.
func TestBusinessPMSStayExtensionConflictingInventoryLeavesNoPartialChange(t *testing.T) {
	x := newPMSAmendFixture(t, false, 1)
	c := x.c
	cmd, q := x.extension(0, 2, "RESERVATION_TERMS")
	cmd = x.approveExtension(0, cmd, q.AmountMinor)
	days := amendDays(q.DateFrom, q.DateTo)
	// Another booker holds the last physical unit for the requested nights.
	competing := x.quote(q.DateFrom, q.DateTo, 1)
	c.must(c.owner, "POST", "/holds", inventory.HoldCommand{ProductID: c.product.ID, ProductVersion: x.productVersion(), PoolVersion: x.poolVersion(), QuoteID: competing.QuoteID, DateFrom: competing.DateFrom, DateTo: competing.DateTo, Quantity: 1, DurationSeconds: 600}, 201)
	before := x.amendSnapshot(days)
	c.must(c.owner, "POST", "/stays/"+x.stays[0].String()+"/extensions", cmd, 409)
	if after := x.amendSnapshot(days); !equalInts(before, after) {
		t.Fatalf("rejected extension left partial state %v -> %v", before, after)
	}
	// A quote that does not start at the current departure is rejected too.
	start, _ := time.Parse(time.DateOnly, q.DateFrom)
	gap := x.quote(start.AddDate(0, 0, 2).Format(time.DateOnly), start.AddDate(0, 0, 3).Format(time.DateOnly), 1)
	wrong := cmd
	wrong.ApprovalID = ids.Nil
	wrong.QuoteID, wrong.DateTo = gap.QuoteID, gap.DateTo
	wrong = x.approveExtension(0, wrong, gap.AmountMinor)
	c.must(c.owner, "POST", "/stays/"+x.stays[0].String()+"/extensions", wrong, 409)
	if after := x.amendSnapshot(days); !equalInts(before, after) {
		t.Fatalf("mismatched quote left partial state %v -> %v", before, after)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Guest and corporate shares conserve the exact extension amount in separate
// accepted obligations; a later early departure credits both unit lines and
// releases the extension stock too.
func TestBusinessPMSStayExtensionCorporateSplitAndEarlyDepartureAcrossChain(t *testing.T) {
	x := newPMSAmendFixture(t, false, 1)
	c := x.c
	company, contract := (&pmsCollectionFixture{c: c, guest: x.guests[0]}).corporate(t)
	cmd, q := x.extension(0, 2, "SPLIT")
	cmd.CorporateCustomerID, cmd.CorporateContractID, cmd.CorporateAmountMinor = &company.ID, &contract.ID, q.AmountMinor/2
	stranger := ids.New()
	invalid := cmd
	invalid.CorporateContractID = &stranger
	invalid = x.approveExtension(0, invalid, q.AmountMinor)
	c.must(c.owner, "POST", "/stays/"+x.stays[0].String()+"/extensions", invalid, 403)
	cmd = x.approveExtension(0, cmd, q.AmountMinor)
	out := commercialData[businesshttp.PMSStayExtensionResult](t, c.must(c.owner, "POST", "/stays/"+x.stays[0].String()+"/extensions", cmd, 201))
	if len(out.Payers) != 2 || out.Payers[0].PayerKind != "GUEST" || out.Payers[0].AmountMinor != q.AmountMinor-q.AmountMinor/2 || out.Payers[1].PayerKind != "CORPORATE" || out.Payers[1].Responsibility != "CORPORATE_CREDIT" || out.Payers[1].AmountMinor != q.AmountMinor/2 {
		t.Fatalf("split payers %+v", out.Payers)
	}
	if n := x.count(`SELECT count(*) FROM reservation_financial_groups WHERE id=$1 AND parent_group_id=$2 AND state='APPROVED_GUARANTEE'`, out.Payers[1].FinancialGroupID, out.Payers[0].FinancialGroupID); n != 1 {
		t.Fatal("corporate share is not a child obligation of the extension line")
	}
	if n := x.count(`SELECT (sum(amount_minor)=$2)::integer FROM stay_extension_payers WHERE extension_id=$1`, out.ExtensionID, q.AmountMinor); n != 1 {
		t.Fatal("payer shares do not conserve the extension amount")
	}
	cmdDeparture, terms := x.earlyDeparture(0)
	if len(terms.Segments) != 2 || terms.Terms.UnusedNights != len(amendDays(c.dates.DateFrom, out.DateTo)) || len(terms.Segments[1].Payers) != 2 {
		t.Fatalf("chain terms %+v", terms)
	}
	sum := int64(0)
	for _, s := range terms.Segments {
		sum += s.CreditMinor
		for _, p := range s.Payers {
			if p.Collection != "GUARANTEE" {
				t.Fatalf("guarantee share classified as %s", p.Collection)
			}
		}
	}
	if sum != terms.Terms.CreditMinor {
		t.Fatalf("segment credits %d != %d", sum, terms.Terms.CreditMinor)
	}
	result := commercialData[businesshttp.PMSEarlyDepartureResult](t, c.must(c.owner, "POST", "/stays/"+x.stays[0].String()+"/early-departure", cmdDeparture, 200))
	if result.FinancialState != "FOLIO_CREDIT_ONLY" || len(result.ReleasedNights) != terms.Terms.UnusedNights {
		t.Fatalf("chain departure %+v", result)
	}
	for _, day := range amendDays(c.dates.DateFrom, out.DateTo) {
		if committed, provenance := x.stockState(day); committed != 0 || provenance != 0 {
			t.Fatalf("chain night %s still committed %d/%d", day, committed, provenance)
		}
	}
}

// The platform-collected guest share of an extension is collected through
// the existing durable Payment worker, applied to the folio once, and a later
// reversal surfaces as an unresolved amendment obligation in night audit.
func TestBusinessPMSStayExtensionCollectionSettlementAndReversal(t *testing.T) {
	x := newPMSAmendFixture(t, true, 1)
	c := x.c
	cmd, q := x.extension(0, 1, "RESERVATION_TERMS")
	bad := cmd
	bad.ApprovalID = ids.New()
	c.must(c.owner, "POST", "/stays/"+x.stays[0].String()+"/extensions", bad, 400)
	out := commercialData[businesshttp.PMSStayExtensionResult](t, c.must(c.owner, "POST", "/stays/"+x.stays[0].String()+"/extensions", cmd, 201))
	if !out.CollectionRequired || out.Payers[0].Responsibility != "PLATFORM_COLLECT" || out.Payers[0].FinancialState != "PENDING" {
		t.Fatalf("collectable extension %+v", out)
	}
	var financial string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT financial_state FROM reservations WHERE id=$1`, x.v.Reservation.ID).Scan(&financial); e != nil || financial != "PENDING" {
		t.Fatalf("root financial state hid the pending extension: %s %v", financial, e)
	}
	path := "/stays/" + x.stays[0].String() + "/extensions/" + out.ExtensionID.String() + "/payment-requests"
	collect := businesshttp.PMSStayExtensionCollection{PaymentPolicyID: x.pay.policy.ID, PaymentPolicyVersion: x.pay.policy.Version, PaymentPolicyFamily: "HOTEL", PayerWalletID: ids.New(), ConsentReference: "explicit-extension-payer-consent", LegalPartyReference: "canonical-test-guest-party", Email: "disposable-guest@example.invalid", Schedule: "ONE_TIME", Reason: "Collect the accepted in-house extension share"}
	c.must(c.owner, "POST", path, collect, 202)
	c.must(c.owner, "POST", path, collect, 409) // never a second charge
	x.process()
	x.process()
	if x.pay.collectionCalls != 2 {
		t.Fatalf("payment collections %d", x.pay.collectionCalls)
	}
	group := out.Payers[0].FinancialGroupID
	if quote, ok := x.pay.quoteFor(group); !ok || quote.AmountMinor != q.AmountMinor {
		t.Fatalf("extension locked quote %+v", quote)
	}
	x.reconcile(x.folios[0], 200) // collected but unsettled: extension balance stays
	if d := x.stay(0); d.BalanceMinor != q.AmountMinor {
		t.Fatalf("unsettled extension reduced folio %d", d.BalanceMinor)
	}
	x.pay.settle(group)
	x.readback(group)
	x.reconcile(x.folios[0], 200)
	x.reconcile(x.folios[0], 200)
	d := x.stay(0)
	if d.BalanceMinor != 0 || d.Folio.State != "SETTLED" || x.count(`SELECT count(*) FROM folio_payment_references WHERE folio_id=$1 AND financial_group_id=$2`, x.folios[0], group) != 1 {
		t.Fatalf("settled extension balance %d state %s", d.BalanceMinor, d.Folio.State)
	}
	x.pay.reverse(group, q.AmountMinor)
	x.readback(group)
	if d = x.stay(0); d.BalanceMinor != q.AmountMinor {
		t.Fatalf("reversal hidden %d", d.BalanceMinor)
	}
	audit := commercialData[businesshttp.PMSAuditResult](t, c.must(c.owner, "POST", "/night-audit/validate", businesshttp.PMSAuditCommand{PropertyID: c.property.ID, Reason: "Validate the day after the extension payment reversal"}, 201))
	if audit.Snapshot.Checks["UNRESOLVED_STAY_AMENDMENT_OBLIGATIONS"] != 1 || audit.State != "EXCEPTIONS" {
		t.Fatalf("night audit ignored reversed extension %+v", audit.Snapshot.Checks)
	}
}

// One unit of a two-unit line departs early: only that unit's nights return
// to stock, the line is not split, the other unit stays in house and can still
// be moved, and per-date conservation holds throughout.
func TestBusinessPMSPartialGroupCheckoutConservesStockPerDate(t *testing.T) {
	x := newPMSAmendFixture(t, false, 2)
	c := x.c
	nights := amendDays(c.dates.DateFrom, c.dates.DateTo)
	cmd, terms := x.earlyDeparture(0)
	if terms.Segments[0].Slot != 1 || terms.Terms.UnusedMinor*2 != x.v.Reservation.AmountMinor && terms.Terms.UnusedMinor*2 != x.v.Reservation.AmountMinor+1 {
		t.Fatalf("slot terms %+v", terms)
	}
	c.must(c.owner, "POST", "/stays/"+x.stays[0].String()+"/early-departure", cmd, 200)
	var state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM reservations WHERE id=$1`, x.v.Reservation.ID).Scan(&state); e != nil || state != "CHECKED_IN" {
		t.Fatalf("partial checkout completed the reservation: %s %v", state, e)
	}
	if n := x.count(`SELECT count(*) FROM reservation_rooms WHERE reservation_id=$1 AND state='ACTIVE' AND quantity=2`, x.v.Reservation.ID); n != 1 {
		t.Fatal("partial checkout split or changed the accepted line")
	}
	check := func(remaining int) {
		t.Helper()
		for _, day := range nights {
			if committed, provenance := x.stockState(day); committed != remaining || provenance != remaining {
				t.Fatalf("night %s committed %d provenance %d want %d", day, committed, provenance, remaining)
			}
		}
		days := commercialData[pmsUnitReleaseDays](t, c.must(c.owner, "GET", "/reservations/"+x.v.Reservation.ID.String()+"/unit-releases", nil, 200))
		if len(days.Days) != len(nights) {
			t.Fatalf("conservation days %+v", days)
		}
		for _, d := range days.Days {
			if d.Accepted != 2 || d.Remaining != 1 || d.Released != 1 || !d.Conserved {
				t.Fatalf("per-date conservation %+v", d)
			}
		}
	}
	check(1)
	// The remaining unit keeps its physical backing: it can move into the
	// vacated room, which proves backing equals accepted minus released units.
	status := x.ready(x.rooms[0])
	d := x.stay(1)
	c.must(c.owner, "POST", "/reservations/"+x.v.Reservation.ID.String()+"/room-move", businesshttp.PMSRoomMoveCommand{StayID: x.stays[1], Version: d.Stay.Version, ReservationVersion: x.resVersion(), AssignmentVersion: d.Assignment.Version, RoomID: x.rooms[0].ID, RoomStatusVersion: status.Version, Reason: "Move the remaining unit after the partial checkout"}, 200)
	d = x.stay(1)
	checkout := businesshttp.PMSCheckoutCommand{StayID: x.stays[1], Version: d.Stay.Version, ReservationVersion: x.resVersion(), FolioVersion: d.Folio.Version, Reason: "Record the remaining unit's actual departure"}
	op := checkout
	checkout.ApprovalID = c.approval("COMMERCIAL_PMS_DEPARTURE_DEBT", "pms:approve", "", &d.BalanceMinor, d.Folio.Currency, map[string]any{"stay_id": x.stays[1], "command": op})
	c.must(c.owner, "POST", "/reservations/"+x.v.Reservation.ID.String()+"/check-out", checkout, 200)
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM reservations WHERE id=$1`, x.v.Reservation.ID).Scan(&state); e != nil || state != "CHECKED_OUT" {
		t.Fatalf("all units departed but reservation %s %v", state, e)
	}
	check(1) // an ordinary departure never releases accepted, retained nights
	if n := x.count(`SELECT count(*) FROM reservation_unit_releases WHERE reservation_id=$1 AND kind='PARTIAL_CHECKOUT' AND slot=1`, x.v.Reservation.ID); n != 1 {
		t.Fatal("partial checkout ledger missing")
	}
}

// Concurrent in-house commands on one stay, and two units competing for the
// last extension night, are fenced: exactly one wins and stock stays exact.
func TestBusinessPMSConcurrentInHouseAmendmentsAreFenced(t *testing.T) {
	x := newPMSAmendFixture(t, false, 2, 1)
	c := x.c
	first, q := x.extension(0, 1, "RESERVATION_TERMS")
	first = x.approveExtension(0, first, q.AmountMinor)
	second, q2 := x.extension(1, 1, "RESERVATION_TERMS")
	second = x.approveExtension(1, second, q2.AmountMinor)
	// The second command must use the same reservation version as the first.
	second.ReservationVersion = first.ReservationVersion
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for i, cmd := range []businesshttp.PMSStayExtensionCommand{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := c.call(c.owner, "POST", "/stays/"+x.stays[i].String()+"/extensions", cmd, nil)
			codes <- code
		}()
	}
	wg.Wait()
	close(codes)
	wins := 0
	for code := range codes {
		if code == 201 {
			wins++
		} else if code != 409 {
			t.Fatalf("unexpected race status %d", code)
		}
	}
	if wins != 1 {
		t.Fatalf("last extension night sold %d times", wins)
	}
	committed, provenance := x.stockState(q.DateFrom)
	if committed != 1 || provenance != 1 {
		t.Fatalf("contended night committed %d provenance %d", committed, provenance)
	}
	// Same-stay race: an extension and an early departure prepared against the
	// same versions; the loser sees the winner's version change.
	// The race winner's unit owns the next free extension night.
	w := 0
	if x.count(`SELECT count(*) FROM stay_extensions WHERE stay_id=$1`, x.stays[1]) == 1 {
		w = 1
	}
	ext, eq := x.extension(w, 1, "RESERVATION_TERMS")
	departure, _ := x.earlyDeparture(w)
	ext.ReservationVersion = departure.ReservationVersion
	ext = x.approveExtension(w, ext, eq.AmountMinor)
	results := make(chan int, 2)
	go func() {
		code, _ := c.call(c.owner, "POST", "/stays/"+x.stays[w].String()+"/extensions", ext, nil)
		results <- code
	}()
	go func() {
		code, _ := c.call(c.owner, "POST", "/stays/"+x.stays[w].String()+"/early-departure", departure, nil)
		results <- code
	}()
	a, b := <-results, <-results
	if !(a == 409 && (b == 200 || b == 201) || b == 409 && (a == 200 || a == 201)) {
		t.Fatalf("same-stay race %d/%d", a, b)
	}
	for _, day := range amendDays(c.dates.DateFrom, eq.DateTo) {
		if committed, provenance := x.stockState(day); committed != provenance {
			t.Fatalf("stock and provenance diverged on %s: %d/%d", day, committed, provenance)
		}
	}
}

// An offline front-desk extension replays exactly once; a stale replay of the
// same command becomes a reconciliation task instead of a second effect.
func TestBusinessPMSOfflineStayExtensionReplay(t *testing.T) {
	x := newPMSAmendFixture(t, false, 1)
	c := x.c
	cmd, q := x.extension(0, 1, "RESERVATION_TERMS")
	cmd = x.approveExtension(0, cmd, q.AmountMinor)
	offline := businesshttp.PMSOfflineCommand{PropertyID: c.property.ID, ClientOperationID: ids.New(), OriginalActionAt: time.Now().UTC().Add(-time.Minute), Action: "EXTEND_STAY", TargetID: x.stays[0], Extension: &cmd, Reason: "Replay the front-desk extension captured while offline"}
	first := commercialData[businesshttp.PMSOfflineResult](t, c.must(c.owner, "POST", "/front-desk/offline-operations", offline, 201))
	if first.State != "APPLIED" {
		t.Fatalf("offline extension %+v", first)
	}
	replayed := commercialData[businesshttp.PMSOfflineResult](t, c.must(c.owner, "POST", "/front-desk/offline-operations", offline, 201))
	if replayed.ID != first.ID || x.count(`SELECT count(*) FROM stay_extensions WHERE stay_id=$1`, x.stays[0]) != 1 {
		t.Fatalf("offline replay produced a second effect %+v", replayed)
	}
	stale := offline
	stale.ClientOperationID = ids.New()
	task := commercialData[businesshttp.PMSOfflineResult](t, c.must(c.owner, "POST", "/front-desk/offline-operations", stale, 202))
	if task.State != "RECONCILIATION_REQUIRED" || task.FailureCode != "STAY_EXTENSION_STATE_VERSION" || task.TaskID == nil {
		t.Fatalf("stale offline extension %+v", task)
	}
	if n := x.count(`SELECT count(*) FROM stay_extensions WHERE stay_id=$1`, x.stays[0]); n != 1 {
		t.Fatalf("stale replay extended again %d", n)
	}
}

// A configured access method receives a new bounded window ending at the new
// departure; the previous assignment's credential is revoked.
func TestBusinessPMSStayExtensionAccessEntitlementWindow(t *testing.T) {
	x := newPMSAmendFixture(t, false, 1)
	c := x.c
	pmsAccessConfigure(c, "MANUAL_KEY", "", 0, c.service.Now().Add(7*24*time.Hour-time.Hour))
	before := x.stay(0)
	cmd, q := x.extension(0, 2, "RESERVATION_TERMS")
	cmd = x.approveExtension(0, cmd, q.AmountMinor)
	out := commercialData[businesshttp.PMSStayExtensionResult](t, c.must(c.owner, "POST", "/stays/"+x.stays[0].String()+"/extensions", cmd, 201))
	issued := pmsAccessFor(c, out.AssignmentID, "ISSUE")
	if issued.Command == nil || issued.EffectiveState != "UNKNOWN" || issued.DeliveryConfirmed {
		t.Fatalf("extended access invented delivery %+v", issued)
	}
	var raw []byte
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT request FROM hospitality_access_jobs WHERE id=$1`, issued.Reference.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var request struct {
		EndsAt time.Time `json:"ends_at"`
	}
	if e := json.Unmarshal(raw, &request); e != nil {
		t.Fatal(e)
	}
	want, e := hospitality.LocalInstant(out.DateTo, x.v.Reservation.PolicySnapshot.CheckOutLocal, x.v.Reservation.PolicySnapshot.AcceptanceTimezone)
	if e != nil || !request.EndsAt.Equal(want) {
		t.Fatalf("access window ends %s want %s %v", request.EndsAt, want, e)
	}
	if n := x.count(`SELECT count(*) FROM access_grant_references WHERE assignment_id=$1 AND operation='REVOKE'`, before.Assignment.ID); n != 1 {
		t.Fatal("previous window not revoked")
	}
}
