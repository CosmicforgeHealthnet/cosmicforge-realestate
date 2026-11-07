package integration

import (
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func acceptedKinds(t *testing.T, c *commercialHarness) map[string]int {
	t.Helper()
	rows, e := c.db.Pool.Query(t.Context(), `SELECT kind,count(*) FROM guest_notification_jobs WHERE state='ACCEPTED' GROUP BY kind`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var kind string
		var n int
		if e = rows.Scan(&kind, &n); e != nil {
			t.Fatal(e)
		}
		out[kind] = n
	}
	return out
}

// Journey 5: corporate customer → eligible traveler → negotiated rate →
// booking → room assignment → split folio → payment status → corporate
// reporting, with the corporate contact notified through approved templates.
func TestBusinessJourneyCorporateBookingSplitFolioAndReporting(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	reservationPolicyFixture(c, false)
	crmReviewer(c)
	f := newGuestNotifier()
	c.domain.Notification = f
	notificationTemplate(c, "CORPORATE_BOOKING_UPDATE", "OPERATIONAL")
	g := crmGuestFixture(c)

	company := commercialData[crm.Corporate](t, c.must(c.owner, "POST", "/corporate-customers", businesshttp.CRMCorporateCreate{PropertyID: c.property.ID, LegalName: "Journey corporate customer", RegistrationReference: "corp-register:" + ids.New().String(), Contacts: []crm.CorporateContact{{Name: "Travel officer", Email: "corporate-travel@example.invalid", RepresentativeReference: "company-designation-proof"}}, Reason: "Register a canonical corporate customer with protected contacts"}, 201))
	now := time.Now().UTC()
	terms := crm.CorporateTerms{RatePlanIDs: []ids.ID{c.rate.ID}, AllowedSources: []string{"CORPORATE"}, BillingResponsibility: "PAY_AT_PROPERTY", BillingReference: "approved-corporate-billing", MaxBookingMinor: 100000, Currency: "NGN", RequireNamedTravelers: true, MaxNights: 30}
	contractCmd := businesshttp.CRMCorporateContractCommand{State: "ACTIVE", Definition: terms, EffectiveAt: now.Add(-time.Minute), ExpiresAt: now.AddDate(0, 1, 0), Reason: "Independent negotiated canonical rate and billing limits"}
	op := contractCmd
	contractCmd.ApprovalID = c.approval("COMMERCIAL_CORPORATE_CONTRACT", "reservations:approve", "", &terms.MaxBookingMinor, terms.Currency, map[string]any{"corporate_id": company.ID, "property_id": c.property.ID, "command": op})
	contract := commercialData[crm.CorporateContract](t, c.must(c.owner, "POST", "/corporate-customers/"+company.ID.String()+"/contracts", contractCmd, 201))
	traveler := businesshttp.CRMTravelerCommand{GuestID: g.Guest.ID, ContractID: contract.ID, State: "ACTIVE", Policy: terms, EffectiveAt: contract.EffectiveAt, ExpiresAt: contract.ExpiresAt, Reason: "Independently approve the named traveler"}
	travelOp := traveler
	traveler.ApprovalID = c.approval("COMMERCIAL_CORPORATE_TRAVELER", "reservations:approve", "", &terms.MaxBookingMinor, terms.Currency, map[string]any{"corporate_id": company.ID, "property_id": c.property.ID, "command": travelOp})
	c.must(c.owner, "POST", "/corporate-customers/"+company.ID.String()+"/travelers", traveler, 200)

	// Negotiated plan quote → corporate booking under the contract.
	q := c.quote()
	if q.RatePlanID != c.rate.ID {
		t.Fatalf("quote not priced from the negotiated plan %+v", q)
	}
	in := reservationCommandFixture(c, false)
	in.Rooms[0].QuoteID = q.QuoteID
	in.Source, in.PaymentResponsibility = "CORPORATE", "PAY_AT_PROPERTY"
	in.CorporateID, in.CorporateContractID = &company.ID, &contract.ID
	in.Guests = []reservations.GuestCommand{{GuestID: &g.Guest.ID, SourceKind: g.SourceKind, SourceReference: g.SourceReference, IsPrimary: true}}
	bookOp := in
	in.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &q.AmountMinor, q.Currency, map[string]any{"command": bookOp})
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", in, 201))
	v := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/confirm", reservations.Command{Version: created.Reservation.Version, Reason: "Confirm the named traveler under the current contract"}, 200))

	// Room assignment and check-in.
	pay := newFolioPaymentFake(reservationPaymentProvider(c).policy)
	c.domain.Payment = pay
	status := pmsReady(c)
	a := pmsAssign(c, v, status)
	checkIn := businesshttp.PMSCheckInCommand{AssignmentID: a.ID, AssignmentVersion: a.Version, ReservationVersion: v.Reservation.Version, RoomStatusVersion: status.Version, GuestID: g.Guest.ID, RegistrationReference: "corporate-traveler-front-desk-registration", Reason: "Check in the corporate traveler"}
	arrival, e := hospitality.LocalInstant(a.DateFrom, v.Reservation.PolicySnapshot.CheckInLocal, v.Reservation.PolicySnapshot.AcceptanceTimezone)
	if e != nil {
		t.Fatal(e)
	}
	if time.Now().Before(arrival) {
		early := checkIn
		checkIn.ApprovalID = c.approval("COMMERCIAL_PMS_EARLY_CHECK_IN", "pms:approve", "", nil, "", map[string]any{"command": early})
	}
	opened := commercialData[struct {
		StayID  ids.ID `json:"stay_id"`
		FolioID ids.ID `json:"folio_id"`
	}](t, c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/check-in", checkIn, 201))
	x := &pmsCollectionFixture{c: c, pay: pay, guest: g, v: v, folioID: opened.FolioID, stayID: opened.StayID}

	// Split folio: the traveler pays meals, the company pays laundry.
	meals := x.post("MEALS", "corporate-journey-restaurant", 4000)
	laundry := x.post("LAUNDRY", "corporate-journey-laundry", 3000)
	path := "/folios/" + x.folioID.String() + "/collection-agreements"
	guestShare := commercialData[pmsAgreementQueued](t, c.must(c.owner, "POST", path, x.agreement("GUEST", hospitality.FolioCollectionClaim{ItemID: meals, AmountMinor: 4000}), 202))
	corp := x.agreement("CORPORATE_BOOKER", hospitality.FolioCollectionClaim{ItemID: laundry, AmountMinor: 3000})
	corp.CorporateCustomerID, corp.CorporateContractID = &company.ID, &contract.ID
	corpOp := corp
	amount := int64(3000)
	corp.ApprovalID = c.approval("COMMERCIAL_PMS_FOLIO_COLLECTION", "folios:approve", "", &amount, "NGN", map[string]any{"folio_id": x.folioID, "command": corpOp})
	corporateShare := commercialData[pmsAgreementQueued](t, c.must(c.owner, "POST", path, corp, 202))
	x.process(t)
	x.reconcile(t, 409) // collected but unsettled money never reduces the folio
	x.pay.settle(guestShare.FinancialGroupID)
	x.pay.settle(corporateShare.FinancialGroupID)
	x.readback(t, guestShare.ID)
	x.readback(t, corporateShare.ID)
	x.reconcile(t, 200)
	list := x.agreements()
	if r := list.Responsibility; r.AllocatedGuestMinor != 4000 || r.AllocatedCorporateMinor != 3000 || r.AppliedReceiptsMinor != 7000 || !r.Conserved || r.MandatoryPayerUnverified {
		t.Fatalf("corporate split responsibility %+v", r)
	}
	if fo := x.folio(); fo.BalanceMinor != list.Responsibility.AccommodationMinor || fo.Folio.State != "SETTLEMENT_PENDING" {
		t.Fatalf("payment status %d %s", fo.BalanceMinor, fo.Folio.State)
	}

	// Corporate reporting and corporate-contact notifications.
	m := crmReportFor(t, c)
	if m["corporate_booking_share_bps"] == nil || m["corporate_booking_share_bps"].(float64) != 10000 {
		t.Fatalf("corporate report %+v", m)
	}
	runNotifications(t, c)
	if kinds := acceptedKinds(t, c); kinds["CORPORATE_BOOKING_UPDATE"] < 1 {
		t.Fatalf("corporate notifications %+v", kinds)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call.Recipient.ID != company.ID || call.Destinations.Email != "corporate-travel@example.invalid" {
			t.Fatalf("corporate notification recipient %+v", call)
		}
	}
}

// Journey 6: overbooking → automatic CRM case → authorized remedy →
// Notification → verified outcome → closure.
func TestBusinessJourneyOverbookingRecoveryNotificationAndClosure(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	crmReviewer(c)
	f := newGuestNotifier()
	c.domain.Notification = f
	notificationTemplate(c, "SERVICE_RECOVERY_UPDATE", "OPERATIONAL")
	notificationTemplate(c, "COMPENSATION_STATUS", "OPERATIONAL")
	base := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, true), 201))
	c.must(c.owner, "POST", "/reservations/"+base.Reservation.ID.String()+"/confirm", reservations.Command{Version: base.Reservation.Version, Reason: "Confirm the sole physical allocation"}, 200)
	g := contactableGuest(c, "walked-guest-"+ids.New().String()+"@example.invalid")
	policy := overbookingPolicyFixture(c)
	q := commercialData[revenue.QuoteSnapshot](t, c.must(c.owner, "POST", "/reservation-overbooking-quotes", businesshttp.ReservationOverbookingQuote{PolicyID: policy.ID, PolicyVersion: policy.Version, Quote: businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1}}, 201))
	inquiry := reservations.Create{PropertyID: c.property.ID, Source: "DIRECT", State: "INQUIRY", PaymentResponsibility: "PAY_AT_PROPERTY", Rooms: []reservations.RoomCommand{{ProductID: q.ProductID, ProductVersion: q.ProductVersion, PoolVersion: q.PoolVersion, QuoteID: q.QuoteID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1}}, Guests: []reservations.GuestCommand{{GuestID: &g.Guest.ID, SourceKind: g.SourceKind, SourceReference: g.SourceReference, IsPrimary: true}}, Reason: "Retain a priced inquiry while physical stock is fully occupied"}
	inqOp := inquiry
	inquiry.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &q.AmountMinor, q.Currency, map[string]any{"command": inqOp})
	pending := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", inquiry, 201))
	c.must(c.owner, "POST", "/reservations/"+pending.Reservation.ID.String()+"/overbook", overbookingCommand(c, policy, pending.Reservation, "ACCEPT"), 200)

	var caseID ids.ID
	var issue, severity, state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT id,issue_type,severity,state FROM service_recovery_cases WHERE reservation_id=$1 AND guest_id=$2`, pending.Reservation.ID, g.Guest.ID).Scan(&caseID, &issue, &severity, &state); e != nil || issue != "OVERBOOKING" || severity != "HIGH" || state != "OPEN" {
		t.Fatalf("automatic recovery case %s/%s/%s %v", issue, severity, state, e)
	}
	approverID := c.approver.registered.AccountID
	cs := recoveryMove(t, c, caseID, 1, "ASSIGN", func(in *businesshttp.CRMRecoveryTransition) { in.AssigneeID = &approverID }, 200)
	cs = recoveryMove(t, c, cs.ID, cs.Version, "INVESTIGATE", nil, 200)
	cs = commercialData[businesshttp.CRMRecoveryCase](t, c.must(c.owner, "POST", "/service-recovery/cases/"+cs.ID.String()+"/remedy", businesshttp.CRMRecoveryRemedyCommand{Version: cs.Version, Remedies: []businesshttp.CRMRecoveryRemedyItem{{Kind: "ALTERNATIVE_ROOM", Description: "Comparable room at the partner property"}}, Reason: "Propose the exact relocation remedy"}, 200))
	cs = recoveryMove(t, c, cs.ID, cs.Version, "SUBMIT", nil, 200)
	approve := businesshttp.CRMRecoveryApproval{Version: cs.Version, RemedyDigest: *cs.RemedyDigest, Reason: "Approve the exact relocation remedy"}
	c.must(c.owner, "POST", "/service-recovery/cases/"+cs.ID.String()+"/approval", approve, 403) // proposer cannot approve
	cs = commercialData[businesshttp.CRMRecoveryCase](t, c.must(c.approver, "POST", "/service-recovery/cases/"+cs.ID.String()+"/approval", approve, 200))
	runNotifications(t, c) // updates so far reach the guest
	d := commercialData[recoveryDetail](t, c.must(c.owner, "GET", "/service-recovery/cases/"+cs.ID.String(), nil, 200))
	c.must(c.owner, "POST", "/service-recovery/cases/"+cs.ID.String()+"/compensations/"+d.Compensations[0].ID.String()+"/outcome", businesshttp.CRMRecoveryExecution{Version: d.Compensations[0].Version, EvidenceReference: "partner-hotel-relocation-voucher", Reason: "Record the verified delivered relocation"}, 200)
	cs = commercialData[recoveryDetail](t, c.must(c.owner, "GET", "/service-recovery/cases/"+cs.ID.String(), nil, 200)).Case
	cs = recoveryMove(t, c, cs.ID, cs.Version, "RESOLVE", func(in *businesshttp.CRMRecoveryTransition) {
		in.Outcome, in.CommunicationReference = "Guest relocated to a comparable partner room", "guest-notification-reference"
	}, 200)
	cs = recoveryMove(t, c, cs.ID, cs.Version, "CLOSE", nil, 200)
	if cs.State != "CLOSED" {
		t.Fatalf("closure %+v", cs)
	}
	runNotifications(t, c)
	kinds := acceptedKinds(t, c)
	if kinds["SERVICE_RECOVERY_UPDATE"] < 4 || kinds["COMPENSATION_STATUS"] != 1 {
		t.Fatalf("recovery notifications %+v", kinds)
	}
	var unsent int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM guest_notification_jobs WHERE guest_id=$1 AND state<>'ACCEPTED'`, g.Guest.ID).Scan(&unsent); e != nil || unsent != 0 {
		t.Fatalf("recovery notices left unsent %d %v", unsent, e)
	}
	if m := crmReportFor(t, c); m["recovery_cases_resolved"].(float64) != 1 {
		t.Fatalf("report %+v", m)
	}
}

// Journey 7: completed stay → guest history → qualifying accrual →
// redemption → authorized reversal/reconciliation, with loyalty notifications,
// and history/loyalty references that survive a later merge of the guest.
func TestBusinessJourneyLoyaltyHistoryNotificationsAndMerge(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	stay, guest, nights := loyaltyCompletedStay(t, c)
	crmReviewer(c)
	f := newGuestNotifier()
	c.domain.Notification = f
	notificationTemplate(c, "LOYALTY_ENROLLED", "OPERATIONAL")
	notificationTemplate(c, "LOYALTY_REWARD", "OPERATIONAL")
	member := crmRegisteredGuest{Guest: crm.Guest{ID: guest}}
	crmConsent(c, member, "OPERATIONAL_CONTACT", "GRANTED", 0)
	crmConsent(c, member, "LOYALTY", "GRANTED", 0)
	c.must(c.owner, "PUT", "/guests/"+guest.String()+"/private", businesshttp.CRMPrivateCommand{Preferences: crm.PrivateProfile{Email: "loyal-" + ids.New().String() + "@example.invalid", ContactPreference: "EMAIL"}, Reason: "Store protected contact after operational consent"}, 200)

	gv := commercialData[crm.GuestView](t, c.must(c.owner, "GET", "/guests/"+guest.String(), nil, 200))
	c.must(c.owner, "POST", "/guests/"+guest.String()+"/stay-history/rebuild", businesshttp.CRMStayHistoryRebuild{GuestVersion: gv.Guest.Version, Reason: "Project the completed stay from owned canonical facts"}, 200)
	history := commercialData[businesshttp.CRMStayHistoryList](t, c.must(c.owner, "GET", "/guests/"+guest.String()+"/stay-history", nil, 200))
	if len(history.Items) != 1 || !history.Items[0].History.Completed {
		t.Fatalf("completed stay history %+v", history)
	}
	program := businesshttp.LoyaltyProgramCommand{PropertyID: c.property.ID, Name: "Guest rewards", State: "ACTIVE", Definition: businesshttp.LoyaltyProgramDefinition{Tiers: []businesshttp.LoyaltyTier{{Code: "BASE"}, {Code: "GOLD", MinPoints: 100}}, PointsExpiryDays: 365, CorporateExcluded: true, AllowedSources: []string{"DIRECT"}}, EffectiveAt: time.Now().Add(-time.Minute), Reason: "Independently approved optional loyalty policy"}
	op := program
	program.ApprovalID = c.approval("COMMERCIAL_LOYALTY_PROGRAM", "loyalty:approve", "", nil, "", map[string]any{"property_id": c.property.ID, "command": op})
	created := commercialData[struct {
		ProgramKey ids.ID `json:"program_key"`
	}](t, c.must(c.owner, "POST", "/loyalty/programs", program, 201))
	base := "/loyalty/programs/" + created.ProgramKey.String()
	rule := businesshttp.LoyaltyRuleCommand{Kind: "EARN_PER_NIGHT", State: "ACTIVE", Definition: businesshttp.LoyaltyRuleDefinition{PointsPerNight: 100, MinNights: 1}, EffectiveAt: time.Now().Add(-time.Minute), Reason: "Independently approved earning rule"}
	ruleOp := rule
	rule.ApprovalID = c.approval("COMMERCIAL_LOYALTY_PROGRAM", "loyalty:approve", "", nil, "", map[string]any{"program_key": created.ProgramKey, "command": ruleOp})
	c.must(c.owner, "POST", base+"/rules", rule, 201)
	reward := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", base+"/rewards", businesshttp.LoyaltyRewardCommand{Name: "Late checkout", Kind: "LATE_CHECKOUT", PointsCost: 100, ValidityDays: 30, State: "ACTIVE", Reason: "Define a non-cash reward"}, 201))
	account := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", "/loyalty/accounts", businesshttp.LoyaltyEnrolCommand{ProgramKey: created.ProgramKey, GuestID: guest, Reason: "Enrol the consenting guest"}, 201))
	ap := "/loyalty/accounts/" + account.ID.String()
	c.must(c.owner, "POST", ap+"/accruals", businesshttp.LoyaltyAccrualCommand{StayID: stay, Reason: "Award the completed qualifying stay once"}, 201)
	c.must(c.owner, "POST", ap+"/redemptions", businesshttp.LoyaltyRedemptionCommand{RewardID: reward.ID, RequestReference: ids.New(), Reason: "Redeem the reward at the guest request"}, 201)
	runNotifications(t, c)
	if kinds := acceptedKinds(t, c); kinds["LOYALTY_ENROLLED"] != 1 || kinds["LOYALTY_REWARD"] != 2 {
		t.Fatalf("loyalty notifications %+v", kinds)
	}
	if view := commercialData[businesshttp.LoyaltyAccountView](t, c.must(c.owner, "GET", ap, nil, 200)); view.BalancePoints != nights*100-100 {
		t.Fatalf("balance %+v", view)
	}

	// Merge the member into a duplicate record; history and loyalty survive.
	target := crmGuestFixture(c)
	m := mergePreview(c, guest, target.Guest.ID, "VERIFIED_OWNERSHIP", 201)
	if !m.Preview.Executable || !hasConflict(m.Preview, "LOYALTY_ACCOUNT_ASSOCIATION", false) || m.Preview.Counts.Stays != 1 {
		t.Fatalf("loyalty member preview %+v", m.Preview)
	}
	exec := businesshttp.CRMGuestMergeExecute{Version: m.Version, PreviewDigest: m.PreviewDigest, Reason: "Merge the verified duplicate loyalty member"}
	exec.ApprovalID = mergeApproval(c, m, exec)
	c.must(c.owner, "POST", "/guest-merges/"+m.ID.String()+"/execute", exec, 200)
	var associated, stays int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE entity_type='loyalty_account' AND operation='LOYALTY_ASSOCIATED' AND entity_id=$2),count(*) FILTER(WHERE entity_type='stay_record' AND entity_id=$3) FROM guest_merge_references WHERE merge_id=$1`, m.ID, account.ID, stay).Scan(&associated, &stays); e != nil || associated != 1 || stays != 1 {
		t.Fatalf("merge references %d/%d %v", associated, stays, e)
	}
	merged := commercialData[businesshttp.CRMStayHistoryList](t, c.must(c.owner, "GET", "/guests/"+target.Guest.ID.String()+"/stay-history", nil, 200))
	if len(merged.Items) != 1 || merged.Items[0].History.GuestID != guest || !merged.Items[0].History.Completed {
		t.Fatalf("history after merge %+v", merged)
	}

	// Authorized reversal after a Payment reversal; the notice resolves to the
	// canonical guest, whose consent was never widened by the merge.
	pf := reservationPaymentProvider(c)
	pf.mu.Lock()
	pf.reversed = 1
	pf.mu.Unlock()
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
	view := commercialData[businesshttp.LoyaltyAccountView](t, c.must(c.owner, "GET", ap, nil, 200))
	if view.BalancePoints != -100 || view.State != "REVIEW_REQUIRED" {
		t.Fatalf("reversal %+v", view)
	}
	runNotifications(t, c)
	var resolved ids.ID
	var jobState, reason string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT j.resolved_guest_id,j.state,j.outcome_reason FROM guest_notification_jobs j JOIN loyalty_transactions x ON x.id=j.source_id WHERE x.account_id=$1 AND x.kind='REVERSAL'`, account.ID).Scan(&resolved, &jobState, &reason); e != nil || resolved != target.Guest.ID || jobState != "SUPPRESSED" || reason != "CONSENT_REQUIRED_OPERATIONAL_CONTACT" {
		t.Fatalf("post-merge reversal notice %v %s %s %v", resolved, jobState, reason, e)
	}
}
