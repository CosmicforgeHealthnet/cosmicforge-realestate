package integration

import (
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type recoveryDetail struct {
	Case          businesshttp.CRMRecoveryCase `json:"case"`
	Compensations []struct {
		ID         ids.ID  `json:"id"`
		Kind       string  `json:"kind"`
		State      string  `json:"state"`
		Version    int64   `json:"version"`
		Dependency *string `json:"dependency"`
	} `json:"compensations"`
}

func recoveryApprover(c *commercialHarness) {
	c.approver = c.user()
	c.secure(c.approver)
	c.join(c.approver, c.owner, []string{"approvals:decide", "recovery:approve", "recovery:write", "recovery:read", "loyalty:approve"})
}

func recoveryMove(t *testing.T, c *commercialHarness, id ids.ID, version int64, action string, extra func(*businesshttp.CRMRecoveryTransition), status int) businesshttp.CRMRecoveryCase {
	in := businesshttp.CRMRecoveryTransition{Version: version, Action: action, Reason: "Record the accountable recovery lifecycle decision"}
	if extra != nil {
		extra(&in)
	}
	raw := c.must(c.owner, "POST", "/service-recovery/cases/"+id.String()+"/transitions", in, status)
	if status != 200 {
		return businesshttp.CRMRecoveryCase{}
	}
	return commercialData[businesshttp.CRMRecoveryCase](t, raw)
}

// Overbooking recovery: exact approved remedy, a refund that stays pending on
// the Admin-owned Payment contract, verified outcomes, resolution and reopen.
func TestBusinessServiceRecoveryApprovedRemedyAndVerifiedOutcomes(t *testing.T) {
	c := newCommercialHTTP(t)
	recoveryApprover(c)
	g := crmGuestFixture(c)
	created := commercialData[businesshttp.CRMRecoveryCase](t, c.must(c.owner, "POST", "/service-recovery/cases", businesshttp.CRMRecoveryCaseCreate{PropertyID: c.property.ID, GuestID: g.Guest.ID, IssueType: "OVERBOOKING", Severity: "HIGH", Summary: "Guest walked to a partner hotel after a physical room shortage", EvidenceReference: "front-desk-incident-log-reference", Reason: "Open recovery for the actual affected guest"}, 201))
	if sla := created.SLADueAt.Sub(created.CreatedAt); created.State != "OPEN" || sla < 24*time.Hour-time.Minute || sla > 24*time.Hour+time.Minute {
		t.Fatalf("opened %+v", created)
	}
	c.must(c.user(), "GET", "/service-recovery/cases/"+created.ID.String(), nil, 404) // tenant isolation
	stranger := ids.New()
	c.must(c.owner, "POST", "/service-recovery/cases", businesshttp.CRMRecoveryCaseCreate{PropertyID: c.property.ID, GuestID: stranger, IssueType: "OTHER", Severity: "LOW", Summary: "Unknown guest reference", EvidenceReference: "unverified-reference", Reason: "Attempt recovery for a guest outside scope"}, 403)

	approverID := c.approver.registered.AccountID
	cs := recoveryMove(t, c, created.ID, 1, "ASSIGN", func(in *businesshttp.CRMRecoveryTransition) { in.AssigneeID = &approverID }, 200)
	cs = recoveryMove(t, c, cs.ID, cs.Version, "INVESTIGATE", nil, 200)
	recoveryMove(t, c, cs.ID, cs.Version, "RESOLVE", func(in *businesshttp.CRMRecoveryTransition) {
		in.Outcome, in.CommunicationReference = "Premature resolution attempt", "guest-email-reference"
	}, 409)

	remedy := businesshttp.CRMRecoveryRemedyCommand{Version: cs.Version, Remedies: []businesshttp.CRMRecoveryRemedyItem{{Kind: "ALTERNATIVE_ROOM", Description: "Comparable room at the partner property"}, {Kind: "REFUND_REQUEST", AmountMinor: 5000, Currency: "NGN", Description: "Refund the first night"}}, Reason: "Propose the exact remedy for independent approval"}
	cs = commercialData[businesshttp.CRMRecoveryCase](t, c.must(c.owner, "POST", "/service-recovery/cases/"+cs.ID.String()+"/remedy", remedy, 200))
	if cs.State != "REMEDY_PROPOSED" || cs.RemedyAmountMinor != 5000 || cs.RemedyDigest == nil {
		t.Fatalf("remedy %+v", cs)
	}
	cs = recoveryMove(t, c, cs.ID, cs.Version, "SUBMIT", nil, 200)
	path := "/service-recovery/cases/" + cs.ID.String() + "/approval"
	approve := businesshttp.CRMRecoveryApproval{Version: cs.Version, RemedyDigest: *cs.RemedyDigest, Reason: "Approve the exact remedy digest"}
	c.must(c.owner, "POST", path, approve, 403) // monetary remedy needs an independent approval
	stale := approve
	stale.RemedyDigest = "a" + (*cs.RemedyDigest)[1:]
	if stale.RemedyDigest == *cs.RemedyDigest {
		stale.RemedyDigest = "b" + (*cs.RemedyDigest)[1:]
	}
	c.must(c.owner, "POST", path, stale, 409)
	var items []businesshttp.CRMRecoveryRemedyItem
	items = append(items, remedy.Remedies...)
	amount := int64(5000)
	approve.ApprovalID = c.approval("COMMERCIAL_SERVICE_RECOVERY_REMEDY", "recovery:approve", "", &amount, "NGN", map[string]any{"case_id": cs.ID, "remedy_digest": *cs.RemedyDigest, "remedies": items})
	cs = commercialData[businesshttp.CRMRecoveryCase](t, c.must(c.owner, "POST", path, approve, 200))
	if cs.State != "EXECUTION_PENDING" {
		t.Fatalf("approved %+v", cs)
	}
	d := commercialData[recoveryDetail](t, c.must(c.owner, "GET", "/service-recovery/cases/"+cs.ID.String(), nil, 200))
	if len(d.Compensations) != 2 || d.Compensations[1].State != "PENDING_EXTERNAL_CONTRACT" || d.Compensations[1].Dependency == nil {
		t.Fatalf("compensations %+v", d.Compensations)
	}
	outcome := func(i int, in businesshttp.CRMRecoveryExecution, status int) {
		in.Version, in.Reason = d.Compensations[i].Version, "Record the verified delivered remedy outcome"
		c.must(c.owner, "POST", "/service-recovery/cases/"+cs.ID.String()+"/compensations/"+d.Compensations[i].ID.String()+"/outcome", in, status)
	}
	outcome(0, businesshttp.CRMRecoveryExecution{EvidenceReference: "partner-hotel-relocation-voucher"}, 200)
	cs = commercialData[recoveryDetail](t, c.must(c.owner, "GET", "/service-recovery/cases/"+cs.ID.String(), nil, 200)).Case
	resolve := func(in *businesshttp.CRMRecoveryTransition) {
		in.Outcome, in.CommunicationReference = "Guest relocated and refund handled", "guest-email-reference-1"
	}
	recoveryMove(t, c, cs.ID, cs.Version, "RESOLVE", resolve, 409) // the refund is only requested
	outcome(1, businesshttp.CRMRecoveryExecution{EvidenceReference: "refund-request-accepted-reference"}, 409)
	outcome(1, businesshttp.CRMRecoveryExecution{Decline: true, EvidenceReference: "guest-declined-refund-in-writing"}, 200)
	cs = commercialData[recoveryDetail](t, c.must(c.owner, "GET", "/service-recovery/cases/"+cs.ID.String(), nil, 200)).Case
	cs = recoveryMove(t, c, cs.ID, cs.Version, "RESOLVE", resolve, 200)
	cs = recoveryMove(t, c, cs.ID, cs.Version, "CLOSE", nil, 200)
	m := crmReportFor(t, c)
	if m["recovery_cases_opened"].(float64) != 1 || m["recovery_cases_resolved"].(float64) != 1 || m["recovery_sla_compliance_bps"].(float64) != 10000 || m["repeat_stay_rate_bps"] != nil {
		t.Fatalf("recovery report %+v", m)
	}
	cs = recoveryMove(t, c, cs.ID, cs.Version, "REOPEN", nil, 200)
	if cs.State != "INVESTIGATING" || cs.ReopenedCount != 1 || cs.RemedyDigest != nil {
		t.Fatalf("reopened %+v", cs)
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE service_recovery_compensation_refs SET amount_minor=1 WHERE case_id=$1`, cs.ID); e == nil {
		t.Fatal("approved remedy terms changed")
	}
	if _, e := c.db.Pool.Exec(t.Context(), `DELETE FROM service_recovery_actions WHERE case_id=$1`, cs.ID); e == nil {
		t.Fatal("recovery history erased")
	}

	// A non-monetary remedy still needs someone other than its proposer.
	second := commercialData[businesshttp.CRMRecoveryCase](t, c.must(c.owner, "POST", "/service-recovery/cases", businesshttp.CRMRecoveryCaseCreate{PropertyID: c.property.ID, GuestID: g.Guest.ID, IssueType: "CLEANLINESS", Severity: "LOW", Summary: "Room was not cleaned on arrival", EvidenceReference: "housekeeping-inspection-reference", Reason: "Open recovery for an actual service failure"}, 201))
	second = recoveryMove(t, c, second.ID, 1, "ASSIGN", func(in *businesshttp.CRMRecoveryTransition) { in.AssigneeID = &approverID }, 200)
	second = recoveryMove(t, c, second.ID, second.Version, "INVESTIGATE", nil, 200)
	second = commercialData[businesshttp.CRMRecoveryCase](t, c.must(c.owner, "POST", "/service-recovery/cases/"+second.ID.String()+"/remedy", businesshttp.CRMRecoveryRemedyCommand{Version: second.Version, Remedies: []businesshttp.CRMRecoveryRemedyItem{{Kind: "COMPLIMENTARY_SERVICE", Description: "Complimentary breakfast"}}, Reason: "Propose a non-cash remedy"}, 200))
	second = recoveryMove(t, c, second.ID, second.Version, "SUBMIT", nil, 200)
	nonCash := businesshttp.CRMRecoveryApproval{Version: second.Version, RemedyDigest: *second.RemedyDigest, Reason: "Approve the exact non-cash remedy"}
	c.must(c.owner, "POST", "/service-recovery/cases/"+second.ID.String()+"/approval", nonCash, 403)
	c.must(c.approver, "POST", "/service-recovery/cases/"+second.ID.String()+"/approval", nonCash, 200)
}

func crmReportFor(t *testing.T, c *commercialHarness) map[string]any {
	now := time.Now().UTC()
	q := "/crm/reports?property_id=" + c.property.ID.String() + "&date_from=" + now.AddDate(0, 0, -1).Format(time.DateOnly) + "&date_to=" + now.AddDate(0, 0, 2).Format(time.DateOnly)
	return commercialData[struct {
		Metrics map[string]any `json:"metrics"`
	}](t, c.must(c.owner, "GET", q, nil, 200)).Metrics
}

func loyaltyCompletedStay(t *testing.T, c *commercialHarness) (ids.ID, ids.ID, int64) {
	v, d := pmsCollectedStay(c)
	path := "/folios/" + d.Folio.ID.String()
	pmsDeferredDeparture(c, v, d)
	c.must(c.owner, "POST", path+"/close-reviews", businesshttp.PMSFolioCloseReviewCommand{Version: 2, AutomaticClose: true, ExpiresAt: time.Now().Add(time.Hour), Reason: "Delegate only final closure of these immutable postings after owning settlement"}, 201)
	pmsReadSettled(c, v)
	c.must(c.owner, "POST", path+"/reconcile", businesshttp.PMSVersionCommand{Version: 2, Reason: "Allocate current signed settled receipt to the original accepted postings"}, 200)
	pmsRunMaintenance(c)
	var state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM reservations WHERE id=$1`, v.Reservation.ID).Scan(&state); e != nil || state != "COMPLETED" {
		t.Fatalf("completion %s %v", state, e)
	}
	from, _ := time.Parse(time.DateOnly, v.Rooms[0].DateFrom)
	to, _ := time.Parse(time.DateOnly, v.Rooms[0].DateTo)
	return d.Stay.ID, d.Stay.GuestID, int64(to.Sub(from).Hours() / 24)
}

func TestBusinessLoyaltyAccrualRedemptionReversalAndDuplicateSafety(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	stay, guest, nights := loyaltyCompletedStay(t, c)
	recoveryApprover(c)

	program := businesshttp.LoyaltyProgramCommand{PropertyID: c.property.ID, Name: "Guest rewards", State: "ACTIVE", Definition: businesshttp.LoyaltyProgramDefinition{Tiers: []businesshttp.LoyaltyTier{{Code: "BASE"}, {Code: "GOLD", MinPoints: 100}}, PointsExpiryDays: 365, CorporateExcluded: true, AllowedSources: []string{"DIRECT"}}, EffectiveAt: time.Now().Add(-time.Minute), Reason: "Independently approved optional loyalty policy"}
	c.must(c.owner, "POST", "/loyalty/programs", program, 403)
	op := program
	program.ApprovalID = c.approval("COMMERCIAL_LOYALTY_PROGRAM", "loyalty:approve", "", nil, "", map[string]any{"property_id": c.property.ID, "command": op})
	created := commercialData[struct {
		ProgramKey ids.ID `json:"program_key"`
	}](t, c.must(c.owner, "POST", "/loyalty/programs", program, 201))
	base := "/loyalty/programs/" + created.ProgramKey.String()
	for _, rule := range []businesshttp.LoyaltyRuleCommand{
		{Kind: "EARN_PER_NIGHT", State: "ACTIVE", Definition: businesshttp.LoyaltyRuleDefinition{PointsPerNight: 100, MinNights: 1}},
		{Kind: "QUALIFYING_STAY_BONUS", State: "ACTIVE", Definition: businesshttp.LoyaltyRuleDefinition{BonusPoints: 50}},
	} {
		rule.EffectiveAt, rule.Reason = time.Now().Add(-time.Minute), "Independently approved earning rule"
		ruleOp := rule
		rule.ApprovalID = c.approval("COMMERCIAL_LOYALTY_PROGRAM", "loyalty:approve", "", nil, "", map[string]any{"program_key": created.ProgramKey, "command": ruleOp})
		code, raw := c.call(c.owner, "POST", base+"/rules", rule, map[string]string{"Idempotency-Key": ids.New().String()})
		if code != 201 {
			t.Fatalf("rule %d %s", code, raw)
		}
	}
	reward := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", base+"/rewards", businesshttp.LoyaltyRewardCommand{Name: "Late checkout", Kind: "LATE_CHECKOUT", PointsCost: 100, ValidityDays: 30, State: "ACTIVE", Reason: "Define a non-cash reward"}, 201))
	expensive := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", base+"/rewards", businesshttp.LoyaltyRewardCommand{Name: "Upgrade", Kind: "UPGRADE_ELIGIBILITY", PointsCost: 1000000, ValidityDays: 30, State: "ACTIVE", Reason: "Define an expensive non-cash reward"}, 201))

	enrol := businesshttp.LoyaltyEnrolCommand{ProgramKey: created.ProgramKey, GuestID: guest, Reason: "Enrol the consenting guest"}
	c.must(c.owner, "POST", "/loyalty/accounts", enrol, 403) // no LOYALTY consent yet
	crmConsent(c, crmRegisteredGuest{Guest: crm.Guest{ID: guest}}, "LOYALTY", "GRANTED", 0)
	account := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", "/loyalty/accounts", enrol, 201))
	c.must(c.owner, "POST", "/loyalty/accounts", enrol, 409)
	ap := "/loyalty/accounts/" + account.ID.String()

	accrual := businesshttp.LoyaltyAccrualCommand{StayID: stay, Reason: "Award the completed qualifying stay once"}
	codes := make(chan int, 2)
	for range 2 {
		go func() {
			code, _ := c.call(c.owner, "POST", ap+"/accruals", accrual, map[string]string{"Idempotency-Key": ids.New().String()})
			codes <- code
		}()
	}
	if a, b := <-codes, <-codes; !(a == 201 && b == 200 || a == 200 && b == 201) {
		t.Fatalf("concurrent duplicate accrual %d/%d", a, b)
	}
	c.must(c.owner, "POST", ap+"/accruals", accrual, 200)
	var entries int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM loyalty_transactions WHERE account_id=$1 AND kind='ACCRUAL'`, account.ID).Scan(&entries); e != nil || entries != 1 {
		t.Fatalf("stay awarded %d times %v", entries, e)
	}
	earned := nights*100 + 50
	view := commercialData[businesshttp.LoyaltyAccountView](t, c.must(c.owner, "GET", ap, nil, 200))
	if view.BalancePoints != earned || view.Tier != "GOLD" || view.PaymentWallet {
		t.Fatalf("accrued %+v", view)
	}

	reference := ids.New()
	redeem := businesshttp.LoyaltyRedemptionCommand{RewardID: reward.ID, RequestReference: reference, Reason: "Redeem the reward at the guest request"}
	c.must(c.owner, "POST", ap+"/redemptions", redeem, 201)
	c.must(c.owner, "POST", ap+"/redemptions", redeem, 200)
	c.must(c.owner, "POST", ap+"/redemptions", businesshttp.LoyaltyRedemptionCommand{RewardID: expensive.ID, RequestReference: ids.New(), Reason: "Attempt to overdraw the account"}, 409)
	if view = commercialData[businesshttp.LoyaltyAccountView](t, c.must(c.owner, "GET", ap, nil, 200)); view.BalancePoints != earned-100 {
		t.Fatalf("redeemed balance %d", view.BalancePoints)
	}

	// A later Payment reversal removes the financial backing of the stay.
	f := reservationPaymentProvider(c)
	f.mu.Lock()
	f.reversed = 1
	f.mu.Unlock()
	var reservation ids.ID
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT reservation_id FROM stay_records WHERE id=$1`, stay).Scan(&reservation); e != nil {
		t.Fatal(e)
	}
	var version int64
	var group ids.ID
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT r.version,rr.financial_group_id FROM reservations r JOIN reservation_rooms rr ON rr.reservation_id=r.id WHERE r.id=$1 LIMIT 1`, reservation).Scan(&version, &group); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", "/reservations/"+reservation.String()+"/payment-readback", map[string]any{"version": version, "financial_group_id": group, "reason": "Read owning Payment reversal after completion"}, 202)
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	pmsRunMaintenance(c)
	c.must(c.owner, "POST", ap+"/reconcile", map[string]any{"reason": "Reconcile loyalty against current stay and financial backing"}, 200)
	c.must(c.owner, "POST", ap+"/reconcile", map[string]any{"reason": "Repeat reconciliation without double reversal"}, 200)
	view = commercialData[businesshttp.LoyaltyAccountView](t, c.must(c.owner, "GET", ap, nil, 200))
	if view.BalancePoints != -100 || view.State != "REVIEW_REQUIRED" || view.Tier != "BASE" {
		t.Fatalf("reversed %+v", view)
	}
	var reversals int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM loyalty_reversals WHERE account_id=$1`, account.ID).Scan(&reversals); e != nil || reversals != 1 {
		t.Fatalf("reversals %d %v", reversals, e)
	}
	c.must(c.owner, "POST", ap+"/redemptions", businesshttp.LoyaltyRedemptionCommand{RewardID: reward.ID, RequestReference: ids.New(), Reason: "Restricted account cannot redeem"}, 409)
	m := crmReportFor(t, c)
	if m["departed_stays"].(float64) != 1 || m["repeat_stay_rate_bps"].(float64) != 0 || m["loyalty_enrolment"].(float64) != 1 || m["loyalty_accrued_points"].(float64) != 0 || m["loyalty_redeemed_points"].(float64) != 100 {
		t.Fatalf("crm report %+v", m)
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE loyalty_transactions SET points=1 WHERE account_id=$1`, account.ID); e == nil {
		t.Fatal("loyalty ledger mutable")
	}
	if _, e := c.db.Pool.Exec(t.Context(), `INSERT INTO loyalty_transactions(id,organisation_id,property_id,account_id,kind,points,source_kind,source_id,source_digest,evidence,created_by) SELECT $1,organisation_id,property_id,id,'ACCRUAL',10,'STAY',$2,repeat('a',64),'{}',created_by FROM loyalty_accounts WHERE id=$3`, ids.New(), ids.New(), account.ID); e == nil {
		t.Fatal("accrual without an actual completed stay accepted")
	}
}
