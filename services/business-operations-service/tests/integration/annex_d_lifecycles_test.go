package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Annex D.9-D.42 lifecycles added by migration 00038, exercised through the
// public HTTP API against a real disposable PostgreSQL database. Payment is
// represented by the Section 15 contract double for the Payment Service
// intent readback (contract certification, not live Payment integration).

var annexDApproverCapabilities = []string{"residential:write", "residential:read", "leasing:write", "leasing:read", "land:write", "land:read", "sales:write", "sales:read", "sales:approve",
	"projects:write", "projects:read", "maintenance:write", "maintenance:read", "maintenance:approve", "managers:write", "managers:read", "professional:write", "professional:read",
	"risk:write", "risk:read", "pms:write", "pms:read", "financing:write", "financing:read", "insurance:write", "insurance:read", "integrations:write", "integrations:read",
	"tax:write", "tax:read", "organisation:write", "organisation:read", "organisation:verify", "housekeeping:write", "housekeeping:read", "content:write", "content:read",
	"rms:write", "rms:read", "analytics:read", "products:write", "products:read", "finance:approve"}

type annexD struct {
	*commercialHarness
	pay *financePaymentFake
	cc  ids.ID
}

func newAnnexD(t *testing.T) *annexD {
	c, fake := newS15(t)
	for _, capability := range annexDApproverCapabilities {
		c.grant(c.approver, capability)
	}
	cc := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", "/cost-centres", businesshttp.CostCentreCreate{Code: "ANNEX-D", Name: "Annex D operations", Reason: "Company cost centre"}, 201))
	return &annexD{commercialHarness: c, pay: fake, cc: cc.ID}
}

// settledObligation creates a receivable CONTRACT obligation for source and
// drives it to SETTLED through the Payment intent readback.
func (a *annexD) settledObligation(source string, amount int64, settle bool) ids.ID {
	a.t.Helper()
	o := commercialData[businesshttp.FinancialObligation](a.t, a.must(a.owner, "POST", "/financial-obligations", businesshttp.ObligationCreate{CostCentreID: a.cc, Direction: "RECEIVABLE", Category: "CONTRACT", CounterpartyReference: "counterparty:annex-d", SourceType: "CONTRACT", SourceReference: source, AmountMinor: amount, Currency: "NGN", DueOn: "2027-12-31", Reason: "Contract receivable"}, 201))
	approval := a.companyApproval("COMMERCIAL_FINANCIAL_OBLIGATION", "finance:approve", &amount, "NGN", map[string]any{"obligation_id": o.ID, "direction": "RECEIVABLE", "counterparty_reference": "counterparty:annex-d"})
	o = commercialData[businesshttp.FinancialObligation](a.t, a.must(a.owner, "POST", "/financial-obligations/"+o.ID.String()+"/approve", businesshttp.FinanceApprovalCommand{Version: o.Version, ApprovalID: approval, Reason: "Approve receivable"}, 200))
	intent := ids.New()
	o = commercialData[businesshttp.FinancialObligation](a.t, a.must(a.owner, "POST", "/financial-obligations/"+o.ID.String()+"/payment-reference", businesshttp.ObligationPaymentReference{Version: o.Version, PaymentReference: intent, Reason: "Payment Service collection intent"}, 200))
	if settle {
		a.pay.intents[intent] = payment.Intent{ID: intent, AmountMinor: amount, Currency: "NGN", State: "SUCCEEDED", SettlementState: "SETTLED"}
		o = commercialData[businesshttp.FinancialObligation](a.t, a.must(a.owner, "POST", "/financial-obligations/"+o.ID.String()+"/readback", businesshttp.OpsVersionCommand{Version: o.Version, Reason: "Payment readback"}, 200))
		if o.State != "SETTLED" {
			a.t.Fatalf("obligation not settled %+v", o)
		}
	}
	return o.ID
}

// signAgreement drafts, issues and (by a distinct person) signs an
// agreement version governing subject.
func signAgreement(c *commercialHarness, subject ids.ID, kind, counterparty string) businesshttp.OpsRecord {
	return signAgreementIn(c, c.property.ID, subject, kind, counterparty)
}

func signAgreementIn(c *commercialHarness, scope, subject ids.ID, kind, counterparty string) businesshttp.OpsRecord {
	c.t.Helper()
	c.grant(c.approver, "residential:write")
	c.grant(c.approver, "residential:read")
	ag := opsRec(c, c.owner, "POST", "/agreements", businesshttp.OpsRecordCreate{ScopeID: &scope, SubjectID: &subject, Kind: kind, Title: "Agreement " + kind, CounterpartyReference: counterparty, Reason: "Draft agreement version"}, 201)
	ag = opsMove(c, c.owner, "/agreements", ag, businesshttp.OpsRecordTransition{Action: "ISSUE", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	opsMove(c, c.owner, "/agreements", ag, businesshttp.OpsRecordTransition{Action: "RECORD_SIGNATURE", EvidenceRefs: []ids.ID{ids.New()}, ExternalReference: "esign-envelope-1"}, 403)
	return opsMove(c, c.approver, "/agreements", ag, businesshttp.OpsRecordTransition{Action: "RECORD_SIGNATURE", EvidenceRefs: []ids.ID{ids.New()}, ExternalReference: "esign-envelope-1"}, 200)
}

func opsState(c *commercialHarness, table string, id ids.ID) string {
	c.t.Helper()
	var state string
	if err := c.db.Pool.QueryRow(c.t.Context(), `SELECT state FROM `+table+` WHERE id=$1`, id).Scan(&state); err != nil {
		c.t.Fatal(err)
	}
	return state
}

func opsExpire(c *commercialHarness, table string, id ids.ID, column string) {
	c.t.Helper()
	if _, err := c.db.Pool.Exec(c.t.Context(), `UPDATE `+table+` SET `+column+`=now()-interval '1 second',version=version+1 WHERE id=$1`, id); err != nil {
		c.t.Fatal(err)
	}
}

func evidence() []ids.ID { return []ids.ID{ids.New()} }

// Journey 7: application -> offer -> agreement -> tenancy -> rent/deposit
// schedule -> arrears -> notice -> renewal -> move-out -> deposit release.
func TestAnnexDResidentialRentalLifecycleFromOfferToDepositRelease(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	app := opsRec(c, c.owner, "POST", "/rental-applications", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "RESIDENTIAL_LONG_TERM", Title: "Application unit 101", CounterpartyReference: "applicant:ngozi", Reason: "Application received"}, 201)
	offerPath := "/rental-applications/" + app.ID.String() + "/offers"
	rent := int64(300000)
	expires := time.Now().UTC().Add(7 * 24 * time.Hour)
	c.must(c.owner, "POST", offerPath, businesshttp.OpsRecordCreate{Kind: "STANDARD", Title: "Offer before approval", AmountMinor: &rent, Currency: "NGN", EndsAt: &expires, Reason: "Too early"}, 409)
	app = opsMove(c, c.owner, "/rental-applications", app, businesshttp.OpsRecordTransition{Action: "SCREEN", ExternalReference: "screening-77"}, 200)
	app = opsMove(c, c.approver, "/rental-applications", app, businesshttp.OpsRecordTransition{Action: "APPROVE"}, 200)
	far := time.Now().UTC().Add(60 * 24 * time.Hour)
	c.must(c.owner, "POST", offerPath, businesshttp.OpsRecordCreate{Kind: "STANDARD", Title: "Long offer", AmountMinor: &rent, Currency: "NGN", EndsAt: &far, Reason: "Too long"}, 400)
	offer := opsRec(c, c.owner, "POST", offerPath, businesshttp.OpsRecordCreate{Kind: "STANDARD", Title: "Offer unit 101", AmountMinor: &rent, Currency: "NGN", EndsAt: &expires, Reason: "Make offer"}, 201)
	c.must(c.owner, "POST", offerPath, businesshttp.OpsRecordCreate{Kind: "STANDARD", Title: "Second live offer", AmountMinor: &rent, Currency: "NGN", EndsAt: &expires, Reason: "Duplicate"}, 409)
	offer = opsMove(c, c.owner, offerPath, offer, businesshttp.OpsRecordTransition{Action: "ISSUE"}, 200)
	offer = opsMove(c, c.owner, offerPath, offer, businesshttp.OpsRecordTransition{Action: "RECORD_ACCEPTANCE", EvidenceRefs: evidence()}, 200)
	start := time.Now().UTC().Add(-20 * 24 * time.Hour)
	end := start.AddDate(1, 0, 0)
	other := rent + 1
	c.must(c.owner, "POST", "/tenancies", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "FIXED_TERM", Title: "Tenancy mismatch", CounterpartyReference: "tenant:ngozi", AmountMinor: &other, Currency: "NGN", StartsAt: &start, EndsAt: &end, Attributes: map[string]any{"application_id": app.ID.String(), "offer_id": offer.ID.String()}, Reason: "Wrong rent"}, 409)
	ten := opsRec(c, c.owner, "POST", "/tenancies", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "FIXED_TERM", Title: "Tenancy unit 101", CounterpartyReference: "tenant:ngozi", AmountMinor: &rent, Currency: "NGN", StartsAt: &start, EndsAt: &end, Attributes: map[string]any{"application_id": app.ID.String(), "offer_id": offer.ID.String(), "rent_frequency": "MONTHLY", "grace_days": 5}, Reason: "Tenancy from accepted offer"}, 201)
	if opsState(c, "rental_offers", offer.ID) != "CONVERTED" || opsState(c, "rental_applications", app.ID) != "CONVERTED" {
		t.Fatal("offer/application not converted")
	}
	ten = opsMove(c, c.owner, "/tenancies", ten, businesshttp.OpsRecordTransition{Action: "SEND_FOR_SIGNATURE"}, 200)
	activation := c.approval("COMMERCIAL_TENANCY", "residential:write", "", &rent, "NGN", map[string]any{"domain": "tenancy", "record_id": ten.ID, "action": "ACTIVATE", "version": ten.Version})
	opsMove(c, c.owner, "/tenancies", ten, businesshttp.OpsRecordTransition{Action: "ACTIVATE", ApprovalID: activation, EvidenceRefs: evidence()}, 409)
	v1 := signAgreement(c, ten.ID, "RESIDENTIAL_TENANCY", "tenant:ngozi")
	if v1.State != "SIGNED" {
		t.Fatal(v1.State)
	}
	// A second signed version requires superseding the first.
	v2 := opsRec(c, c.owner, "POST", "/agreements", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &ten.ID, Kind: "AMENDMENT", Title: "Pet clause amendment", CounterpartyReference: "tenant:ngozi", Reason: "Amendment"}, 201)
	v2 = opsMove(c, c.owner, "/agreements", v2, businesshttp.OpsRecordTransition{Action: "ISSUE", EvidenceRefs: evidence()}, 200)
	opsMove(c, c.approver, "/agreements", v2, businesshttp.OpsRecordTransition{Action: "RECORD_SIGNATURE", EvidenceRefs: evidence(), ExternalReference: "esign-2"}, 409)
	c.must(c.owner, "POST", "/agreements", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &ten.ID, Kind: "COMMERCIAL_LEASE", Title: "Wrong kind", CounterpartyReference: "tenant:ngozi", Reason: "Mismatch"}, 400)
	ten = opsMove(c, c.owner, "/tenancies", ten, businesshttp.OpsRecordTransition{Action: "ACTIVATE", ApprovalID: activation, EvidenceRefs: evidence()}, 200)
	// Rent schedule generated from the activated tenancy.
	var scheduled int
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM rent_obligations WHERE subject_id=$1 AND kind='RENT' AND state='SCHEDULED' AND amount_minor=$2`, ten.ID, rent).Scan(&scheduled); err != nil || scheduled != 12 {
		t.Fatalf("rent schedule %d %v", scheduled, err)
	}
	var first businesshttp.OpsRecord
	list := commercialData[businesshttp.OpsRecordList](t, c.must(c.owner, "GET", "/rent-obligations?property_id="+c.property.ID.String()+"&subject_id="+ten.ID.String()+"&limit=100", nil, 200))
	for _, raw := range list.Items {
		r := decodeOps(t, raw)
		if first.StartsAt == nil || r.StartsAt.Before(*first.StartsAt) {
			first = r
		}
	}
	// Due then overdue by the worker; arrears projection reflects it.
	if err := c.domain.ProcessOperations(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	if s := opsState(c, "rent_obligations", first.ID); s != "OVERDUE" {
		t.Fatalf("first obligation %s", s)
	}
	arrears := commercialData[struct {
		Items []map[string]any `json:"items"`
	}](t, c.must(c.owner, "GET", "/residential/arrears?property_id="+c.property.ID.String(), nil, 200))
	if len(arrears.Items) != 1 || arrears.Items[0]["outstanding_minor"].(float64) != float64(rent) {
		t.Fatalf("arrears %+v", arrears.Items)
	}
	first.Version = currentVersion(c, "rent_obligations", first.ID)
	// Settlement only through the Payment-reconciled financial obligation.
	opsMove(c, c.owner, "/rent-obligations", first, businesshttp.OpsRecordTransition{Action: "RECORD_SETTLEMENT"}, 409)
	wrong := a.settledObligation("rent_obligation:wrong", rent, false)
	opsMove(c, c.owner, "/rent-obligations", first, businesshttp.OpsRecordTransition{Action: "LINK_FINANCIAL_OBLIGATION", ExternalReference: wrong.String()}, 409)
	unsettled := a.settledObligation("rent_obligation:"+first.ID.String(), rent, false)
	first = opsMove(c, c.owner, "/rent-obligations", first, businesshttp.OpsRecordTransition{Action: "LINK_FINANCIAL_OBLIGATION", ExternalReference: unsettled.String()}, 200)
	opsMove(c, c.owner, "/rent-obligations", first, businesshttp.OpsRecordTransition{Action: "RECORD_SETTLEMENT"}, 409)
	var intent ids.ID
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT payment_reference FROM financial_obligations WHERE id=$1`, unsettled).Scan(&intent); err != nil {
		t.Fatal(err)
	}
	a.pay.intents[intent] = payment.Intent{ID: intent, AmountMinor: rent, Currency: "NGN", State: "SUCCEEDED", SettlementState: "SETTLED"}
	c.must(c.owner, "POST", "/financial-obligations/"+unsettled.String()+"/readback", businesshttp.OpsVersionCommand{Version: currentVersion(c, "financial_obligations", unsettled), Reason: "Payment readback"}, 200)
	first = opsMove(c, c.owner, "/rent-obligations", first, businesshttp.OpsRecordTransition{Action: "RECORD_SETTLEMENT"}, 200)
	if first.State != "SETTLED" {
		t.Fatal(first.State)
	}
	// Deposit held only after Payment readback settlement.
	depositAmount := int64(600000)
	dep := opsRec(c, c.owner, "POST", "/deposits", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &ten.ID, Kind: "SECURITY_DEPOSIT", Title: "Security deposit", AmountMinor: &depositAmount, Currency: "NGN", Reason: "Deposit per agreement"}, 201)
	depObligation := a.settledObligation("deposit_obligation:"+dep.ID.String(), depositAmount, true)
	dep = opsMove(c, c.owner, "/deposits", dep, businesshttp.OpsRecordTransition{Action: "LINK_FINANCIAL_OBLIGATION", ExternalReference: depObligation.String()}, 200)
	dep = opsMove(c, c.owner, "/deposits", dep, businesshttp.OpsRecordTransition{Action: "RECORD_HELD"}, 200)
	// Renewal case with a successor contract reference.
	decision := time.Now().UTC().Add(30 * 24 * time.Hour)
	ren := opsRec(c, c.owner, "POST", "/renewals", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &ten.ID, Kind: "RESIDENTIAL", Title: "Renewal 2028", EndsAt: &decision, Reason: "Lease renewal window"}, 201)
	c.must(c.owner, "POST", "/renewals", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &ten.ID, Kind: "RESIDENTIAL", Title: "Duplicate renewal", EndsAt: &decision, Reason: "Duplicate"}, 409)
	newRent := int64(330000)
	ren = opsMove(c, c.owner, "/renewals", ren, businesshttp.OpsRecordTransition{Action: "OFFER", AmountMinor: &newRent, Currency: "NGN"}, 200)
	ren = opsMove(c, c.owner, "/renewals", ren, businesshttp.OpsRecordTransition{Action: "RECORD_DECLINE"}, 200)
	if ren.State != "DECLINED" {
		t.Fatalf("ren state %s", ren.State)
	}
	// Termination notice moves the tenancy to NOTICE_GIVEN.
	effective := time.Now().UTC().Add(30 * 24 * time.Hour)
	notice := opsRec(c, c.owner, "POST", "/notices", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &ten.ID, Kind: "TERMINATION_BY_TENANT", Title: "Tenant notice to quit", StartsAt: &effective, Reason: "Tenant relocating"}, 201)
	notice = opsMove(c, c.owner, "/notices", notice, businesshttp.OpsRecordTransition{Action: "SERVE", EvidenceRefs: evidence()}, 200)
	if notice.State != "SERVED" {
		t.Fatalf("notice state %s", notice.State)
	}
	if opsState(c, "tenancies", ten.ID) != "NOTICE_GIVEN" {
		t.Fatal("tenancy not on notice")
	}
	// Move-out inspection, settlement and deposit deduction.
	moveOut := time.Now().UTC().Add(29 * 24 * time.Hour)
	mo := opsRec(c, c.owner, "POST", "/move-outs", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &ten.ID, Kind: "END_OF_TERM", Title: "Move-out unit 101", StartsAt: &moveOut, Reason: "Notice served"}, 201)
	mo = opsMove(c, c.owner, "/move-outs", mo, businesshttp.OpsRecordTransition{Action: "RECORD_INSPECTION", EvidenceRefs: evidence()}, 200)
	tooMuch := depositAmount + 1
	opsMove(c, c.owner, "/move-outs", mo, businesshttp.OpsRecordTransition{Action: "PROPOSE_SETTLEMENT", AmountMinor: &tooMuch, Currency: "NGN"}, 409)
	deduction := int64(50000)
	mo = opsMove(c, c.owner, "/move-outs", mo, businesshttp.OpsRecordTransition{Action: "PROPOSE_SETTLEMENT", AmountMinor: &deduction, Currency: "NGN"}, 200)
	opsMove(c, c.owner, "/move-outs", mo, businesshttp.OpsRecordTransition{Action: "AGREE_SETTLEMENT", EvidenceRefs: evidence()}, 403)
	mo = opsMove(c, c.approver, "/move-outs", mo, businesshttp.OpsRecordTransition{Action: "AGREE_SETTLEMENT", EvidenceRefs: evidence()}, 200)
	refund := depositAmount - deduction
	wrongRefund := refund + 10
	releaseOp := map[string]any{"domain": "deposit_obligation", "record_id": dep.ID, "action": "REQUEST_RELEASE", "version": dep.Version}
	approval := c.approval("COMMERCIAL_DEPOSIT_RELEASE", "finance:approve", "", &refund, "NGN", releaseOp)
	opsMove(c, c.owner, "/deposits", dep, businesshttp.OpsRecordTransition{Action: "REQUEST_RELEASE", ApprovalID: approval, AmountMinor: &wrongRefund, Currency: "NGN", Attributes: map[string]any{"move_out_case_id": mo.ID.String()}}, 409)
	opsMove(c, c.owner, "/deposits", dep, businesshttp.OpsRecordTransition{Action: "REQUEST_RELEASE", ApprovalID: approval, AmountMinor: &refund, Currency: "NGN"}, 400)
	dep = opsMove(c, c.owner, "/deposits", dep, businesshttp.OpsRecordTransition{Action: "REQUEST_RELEASE", ApprovalID: approval, AmountMinor: &refund, Currency: "NGN", Attributes: map[string]any{"move_out_case_id": mo.ID.String()}}, 200)
	dep = opsMove(c, c.owner, "/deposits", dep, businesshttp.OpsRecordTransition{Action: "RECORD_RELEASE", ExternalReference: "payment-refund-991"}, 200)
	if dep.State != "RELEASED" || *dep.AmountMinor != refund {
		t.Fatalf("deposit %+v", dep)
	}
	mo = opsMove(c, c.owner, "/move-outs", mo, businesshttp.OpsRecordTransition{Action: "CLOSE"}, 200)
	if opsState(c, "tenancies", ten.ID) != "ENDED" {
		t.Fatal("tenancy not ended by move-out closure")
	}
	var cancelled int
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM rent_obligations WHERE subject_id=$1 AND state='CANCELLED'`, ten.ID).Scan(&cancelled); err != nil || cancelled == 0 {
		t.Fatalf("future rent not cancelled %d %v", cancelled, err)
	}
	// Events and immutable history for the journey.
	var events int
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE event_type IN ('tenancy.deposit.updated.v1','tenancy.move_out.updated.v1','tenancy.notice.updated.v1','agreement.updated.v1','rental.offer.updated.v1','tenancy.obligation.updated.v1')`).Scan(&events); err != nil || events < 20 {
		t.Fatalf("events %d %v", events, err)
	}
	if _, err := c.db.Pool.Exec(t.Context(), `UPDATE deposit_obligations SET state='HELD',version=version+1 WHERE id=$1`, dep.ID); err == nil {
		t.Fatal("released deposit reopened")
	}
	other2 := c.user()
	if status, _ := c.call(other2, "GET", "/deposits/"+dep.ID.String(), nil, nil); status != 403 && status != 404 {
		t.Fatalf("cross-tenant deposit %d", status)
	}
}

func decodeOps(t *testing.T, raw []byte) businesshttp.OpsRecord {
	t.Helper()
	var rec businesshttp.OpsRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func currentVersion(c *commercialHarness, table string, id ids.ID) int64 {
	c.t.Helper()
	var v int64
	if err := c.db.Pool.QueryRow(c.t.Context(), `SELECT version FROM `+table+` WHERE id=$1`, id).Scan(&v); err != nil {
		c.t.Fatal(err)
	}
	return v
}

// Journey 8: corporate applicant -> LOI -> approved terms -> signed lease ->
// service-charge budget -> reconciliation -> lease termination.
func TestAnnexDCommercialLOITermsLeaseServiceChargeLifecycle(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	deal := opsRec(c, c.owner, "POST", "/commercial/deals", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "LEASE", Title: "Office lease 101", CounterpartyReference: "tenant:acme-ltd", Reason: "Corporate enquiry"}, 201)
	loiPath := "/commercial/deals/" + deal.ID.String() + "/lois"
	expiry := time.Now().UTC().Add(30 * 24 * time.Hour)
	loi := opsRec(c, c.owner, "POST", loiPath, businesshttp.OpsRecordCreate{Kind: "NON_BINDING", Title: "LOI for office 101", EndsAt: &expiry, Reason: "Heads of terms"}, 201)
	loi = opsMove(c, c.owner, loiPath, loi, businesshttp.OpsRecordTransition{Action: "ISSUE", EvidenceRefs: evidence()}, 200)
	loi = opsMove(c, c.owner, loiPath, loi, businesshttp.OpsRecordTransition{Action: "RECORD_COUNTERSIGNATURE", EvidenceRefs: evidence()}, 200)
	if loi.State != "COUNTERSIGNED" {
		t.Fatalf("loi state %s", loi.State)
	}
	deal = opsMove(c, c.owner, "/commercial/deals", deal, businesshttp.OpsRecordTransition{Action: "ISSUE_LOI", EvidenceRefs: evidence()}, 200)
	termsPath := "/commercial/deals/" + deal.ID.String() + "/terms"
	rent := int64(1800000000)
	commence := time.Now().UTC().Add(45 * 24 * time.Hour)
	c.must(c.owner, "POST", termsPath, businesshttp.OpsRecordCreate{Kind: "HEADS_OF_TERMS", Title: "Bad terms", AmountMinor: &rent, Currency: "NGN", StartsAt: &commence, Attributes: map[string]any{"rent_free_months": 99}, Reason: "Out of range"}, 400)
	terms := opsRec(c, c.owner, "POST", termsPath, businesshttp.OpsRecordCreate{Kind: "HEADS_OF_TERMS", Title: "Agreed heads of terms", AmountMinor: &rent, Currency: "NGN", StartsAt: &commence, Attributes: map[string]any{"rent_free_months": 3, "escalation_bps": 500, "term_months": 60, "fit_out_contribution_minor": 50000000}, Reason: "Negotiated terms"}, 201)
	opsMove(c, c.owner, termsPath, terms, businesshttp.OpsRecordTransition{Action: "APPROVE"}, 403)
	approval := c.approval("COMMERCIAL_LEASE_TERMS", "leasing:write", "", &rent, "NGN", map[string]any{"domain": "commercial_terms", "record_id": terms.ID, "action": "APPROVE", "version": terms.Version})
	terms = opsMove(c, c.owner, termsPath, terms, businesshttp.OpsRecordTransition{Action: "APPROVE", ApprovalID: approval}, 200)
	revised := opsRec(c, c.owner, "POST", termsPath, businesshttp.OpsRecordCreate{Kind: "AMENDMENT", Title: "Revised escalation", AmountMinor: &rent, Currency: "NGN", StartsAt: &commence, Attributes: map[string]any{"escalation_bps": 400}, Reason: "Tenant counter"}, 201)
	approval2 := c.approval("COMMERCIAL_LEASE_TERMS", "leasing:write", "", &rent, "NGN", map[string]any{"domain": "commercial_terms", "record_id": revised.ID, "action": "APPROVE", "version": revised.Version})
	opsMove(c, c.owner, termsPath, revised, businesshttp.OpsRecordTransition{Action: "APPROVE", ApprovalID: approval2}, 200)
	if opsState(c, "commercial_terms", terms.ID) != "SUPERSEDED" {
		t.Fatal("prior approved terms not superseded")
	}
	deal = opsMove(c, c.owner, "/commercial/deals", deal, businesshttp.OpsRecordTransition{Action: "NEGOTIATE", AmountMinor: &rent, Currency: "NGN"}, 200)
	dealApproval := c.approval("COMMERCIAL_DEAL", "leasing:write", "", &rent, "NGN", map[string]any{"domain": "commercial_deal", "record_id": deal.ID, "action": "APPROVE", "version": deal.Version})
	deal = opsMove(c, c.owner, "/commercial/deals", deal, businesshttp.OpsRecordTransition{Action: "APPROVE", ApprovalID: dealApproval}, 200)
	deal = opsMove(c, c.owner, "/commercial/deals", deal, businesshttp.OpsRecordTransition{Action: "SIGN", EvidenceRefs: evidence()}, 200)
	opsMove(c, c.owner, "/commercial/deals", deal, businesshttp.OpsRecordTransition{Action: "ACTIVATE"}, 409)
	signAgreement(c, deal.ID, "COMMERCIAL_LEASE", "tenant:acme-ltd")
	deal = opsMove(c, c.owner, "/commercial/deals", deal, businesshttp.OpsRecordTransition{Action: "ACTIVATE"}, 200)
	// Service charge budget and obligations.
	budget := int64(24000000)
	yearStart := time.Now().UTC().Add(24 * time.Hour)
	yearEnd := yearStart.AddDate(1, 0, -1)
	scb := opsRec(c, c.owner, "POST", "/commercial/service-charge-budgets", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "ANNUAL", Title: "Service charge 2027", AmountMinor: &budget, Currency: "NGN", StartsAt: &yearStart, EndsAt: &yearEnd, Reason: "Annual budget"}, 201)
	scbApproval := c.approval("COMMERCIAL_SERVICE_CHARGE_BUDGET", "finance:approve", "", &budget, "NGN", map[string]any{"domain": "service_charge_budget", "record_id": scb.ID, "action": "APPROVE", "version": scb.Version})
	scb = opsMove(c, c.owner, "/commercial/service-charge-budgets", scb, businesshttp.OpsRecordTransition{Action: "APPROVE", ApprovalID: scbApproval}, 200)
	actual := int64(25000000)
	opsMove(c, c.owner, "/commercial/service-charge-budgets", scb, businesshttp.OpsRecordTransition{Action: "RECORD_RECONCILIATION", AmountMinor: &actual, Currency: "NGN", EvidenceRefs: evidence()}, 403)
	scb = opsMove(c, c.approver, "/commercial/service-charge-budgets", scb, businesshttp.OpsRecordTransition{Action: "RECORD_RECONCILIATION", AmountMinor: &actual, Currency: "NGN", EvidenceRefs: evidence()}, 200)
	if m := attrMap(t, scb); m["variance_minor"].(float64) != 1000000 || m["budget_minor"].(float64) != float64(budget) {
		t.Fatalf("service charge variance %v", m)
	}
	charge := int64(2000000)
	due := time.Now().UTC().Add(10 * 24 * time.Hour)
	grace := due.Add(7 * 24 * time.Hour)
	opsRec(c, c.owner, "POST", "/rent-obligations", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &deal.ID, Kind: "SERVICE_CHARGE", Title: "Service charge Q1", AmountMinor: &charge, Currency: "NGN", StartsAt: &due, EndsAt: &grace, Reason: "Quarterly service charge"}, 201)
	c.must(c.owner, "POST", "/rent-obligations", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &deal.ID, Kind: "SERVICE_CHARGE", Title: "Duplicate charge", AmountMinor: &charge, Currency: "NGN", StartsAt: &due, EndsAt: &grace, Reason: "Duplicate"}, 409)
	deal = opsMove(c, c.owner, "/commercial/deals", deal, businesshttp.OpsRecordTransition{Action: "CLOSE", EvidenceRefs: evidence()}, 200)
	if deal.State != "CLOSED" {
		t.Fatal(deal.State)
	}
	c.must(c.owner, "POST", loiPath, businesshttp.OpsRecordCreate{Kind: "NON_BINDING", Title: "LOI after close", EndsAt: &expiry, Reason: "Too late"}, 409)
}

func attrMap(t *testing.T, rec businesshttp.OpsRecord) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal(rec.Attributes, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Journey 9: land transaction -> terms -> due diligence -> title-verified
// allocation -> settled payment milestone -> perfection -> completion.
func TestAnnexDLandTitleAllocationDueDiligenceAndClosingMilestones(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	branch := c.inventoryBranch(c.owner)
	parcel := c.inventoryNode(c.owner, "/parcels", map[string]any{"branch_id": branch, "code": "PLOT-9", "name": "Plot 9", "attributes": map[string]any{"classification": "RESIDENTIAL_PLOT", "area_square_metres": "600.00", "survey_reference": "SURVEY-009", "geometry": map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{3, 6}, {4, 6}, {4, 7}, {3, 7}, {3, 6}}}}}})
	land := opsRec(c, c.owner, "POST", "/land/transactions", businesshttp.OpsRecordCreate{ScopeID: &parcel.ID, Kind: "SALE", Title: "Sale of plot 9", CounterpartyReference: "buyer:kemi", Reason: "Land sale"}, 201)
	termsPath := "/land/transactions/" + land.ID.String() + "/terms"
	price := int64(3500000000)
	terms := opsRec(c, c.owner, "POST", termsPath, businesshttp.OpsRecordCreate{Kind: "SALE_TERMS", Title: "Sale terms plot 9", AmountMinor: &price, Currency: "NGN", Reason: "Agreed price"}, 201)
	if terms.ScopeKind != "PARCEL" || terms.ScopeID != parcel.ID {
		t.Fatalf("terms scope %+v", terms)
	}
	opsMove(c, c.owner, termsPath, terms, businesshttp.OpsRecordTransition{Action: "APPROVE"}, 403)
	opsMove(c, c.approver, termsPath, terms, businesshttp.OpsRecordTransition{Action: "APPROVE"}, 200)
	land = opsMove(c, c.owner, "/land/transactions", land, businesshttp.OpsRecordTransition{Action: "START_DUE_DILIGENCE", EvidenceRefs: evidence()}, 200)
	dd := opsRec(c, c.owner, "POST", "/due-diligence", businesshttp.OpsRecordCreate{ScopeID: &parcel.ID, SubjectID: &land.ID, Kind: "TITLE", Title: "Title search plot 9", Reason: "Condition precedent"}, 201)
	if dd.ScopeKind != "PARCEL" || dd.PropertyID != nil {
		t.Fatalf("dd scope %+v", dd)
	}
	c.must(c.owner, "POST", "/due-diligence", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &land.ID, Kind: "TITLE", Title: "Wrong scope", Reason: "Mismatch"}, 404)
	landApproval := c.approvalAt("COMMERCIAL_LAND_TRANSACTION", "land:write", "", &price, "NGN", map[string]any{"domain": "land_transaction", "record_id": land.ID, "action": "APPROVE", "version": land.Version}, "PARCEL", parcel.ID)
	opsMove(c, c.owner, "/land/transactions", land, businesshttp.OpsRecordTransition{Action: "APPROVE", ApprovalID: landApproval, AmountMinor: &price, Currency: "NGN"}, 409)
	dd = opsMove(c, c.owner, "/due-diligence", dd, businesshttp.OpsRecordTransition{Action: "START_REVIEW", ExternalReference: "lawyer-appointment-1"}, 200)
	opsMove(c, c.owner, "/due-diligence", dd, businesshttp.OpsRecordTransition{Action: "SATISFY", EvidenceRefs: evidence()}, 403)
	dd = opsMove(c, c.approver, "/due-diligence", dd, businesshttp.OpsRecordTransition{Action: "SATISFY", EvidenceRefs: evidence()}, 200)
	land = opsMove(c, c.owner, "/land/transactions", land, businesshttp.OpsRecordTransition{Action: "APPROVE", ApprovalID: landApproval, AmountMinor: &price, Currency: "NGN"}, 200)
	// Allocation: one live allocation per parcel; title verification required.
	hold := time.Now().UTC().Add(10 * 24 * time.Hour)
	alloc := opsRec(c, c.owner, "POST", "/land/allocations", businesshttp.OpsRecordCreate{ScopeID: &parcel.ID, Kind: "SALE_ALLOCATION", Title: "Allocation plot 9", CounterpartyReference: "buyer:kemi", AmountMinor: &price, Currency: "NGN", EndsAt: &hold, Reason: "Provisional allocation"}, 201)
	c.must(c.owner, "POST", "/land/allocations", businesshttp.OpsRecordCreate{ScopeID: &parcel.ID, Kind: "SALE_ALLOCATION", Title: "Double allocation", CounterpartyReference: "buyer:other", AmountMinor: &price, Currency: "NGN", EndsAt: &hold, Reason: "Second buyer"}, 409)
	allocApproval := c.approvalAt("COMMERCIAL_LAND_ALLOCATION", "land:write", "", &price, "NGN", map[string]any{"domain": "land_allocation", "record_id": alloc.ID, "action": "CONFIRM", "version": alloc.Version}, "PARCEL", parcel.ID)
	opsMove(c, c.owner, "/land/allocations", alloc, businesshttp.OpsRecordTransition{Action: "CONFIRM", ApprovalID: allocApproval, ExternalReference: "payment-ref-plot9"}, 400)
	alloc = opsMove(c, c.owner, "/land/allocations", alloc, businesshttp.OpsRecordTransition{Action: "CONFIRM", ApprovalID: allocApproval, ExternalReference: "payment-ref-plot9", Attributes: map[string]any{"title_verification_reference": "verification:title:plot9"}}, 200)
	// Closing milestones: payment milestone needs Payment-readback settlement.
	closeBy := time.Now().UTC().Add(20 * 24 * time.Hour)
	pay := opsRec(c, c.owner, "POST", "/closing-milestones", businesshttp.OpsRecordCreate{ScopeID: &parcel.ID, SubjectID: &land.ID, Kind: "BALANCE_PAID", Title: "Balance paid", EndsAt: &closeBy, Reason: "Closing plan"}, 201)
	reg := opsRec(c, c.owner, "POST", "/closing-milestones", businesshttp.OpsRecordCreate{ScopeID: &parcel.ID, SubjectID: &land.ID, Kind: "REGISTRATION_PERFECTED", Title: "Registration perfected", EndsAt: &closeBy, Reason: "Closing plan"}, 201)
	unpaid := a.settledObligation("closing_milestone:"+pay.ID.String(), price, false)
	opsMove(c, c.owner, "/closing-milestones", pay, businesshttp.OpsRecordTransition{Action: "SUBMIT_EVIDENCE", EvidenceRefs: evidence(), ExternalReference: unpaid.String()}, 409)
	paid := opsRec(c, c.owner, "POST", "/closing-milestones", businesshttp.OpsRecordCreate{ScopeID: &parcel.ID, SubjectID: &land.ID, Kind: "DEPOSIT_PAID", Title: "Deposit paid", EndsAt: &closeBy, Reason: "Closing plan"}, 201)
	settled := a.settledObligation("closing_milestone:"+paid.ID.String(), 350000000, true)
	paid = opsMove(c, c.owner, "/closing-milestones", paid, businesshttp.OpsRecordTransition{Action: "SUBMIT_EVIDENCE", EvidenceRefs: evidence(), ExternalReference: settled.String()}, 200)
	opsMove(c, c.owner, "/closing-milestones", paid, businesshttp.OpsRecordTransition{Action: "CONFIRM"}, 403)
	opsMove(c, c.approver, "/closing-milestones", paid, businesshttp.OpsRecordTransition{Action: "CONFIRM"}, 200)
	opsMove(c, c.owner, "/land/transactions", land, businesshttp.OpsRecordTransition{Action: "COMPLETE", EvidenceRefs: evidence()}, 409)
	pay.Version = currentVersion(c, "closing_milestones", pay.ID)
	opsMove(c, c.approver, "/closing-milestones", pay, businesshttp.OpsRecordTransition{Action: "WAIVE"}, 200)
	reg = opsMove(c, c.owner, "/closing-milestones", reg, businesshttp.OpsRecordTransition{Action: "SUBMIT_EVIDENCE", EvidenceRefs: evidence(), ExternalReference: "land-registry-filing-55"}, 200)
	opsMove(c, c.approver, "/closing-milestones", reg, businesshttp.OpsRecordTransition{Action: "CONFIRM"}, 200)
	alloc = opsMove(c, c.approver, "/land/allocations", alloc, businesshttp.OpsRecordTransition{Action: "RECORD_PERFECTION", EvidenceRefs: evidence(), ExternalReference: "registry-certificate-55"}, 200)
	land = opsMove(c, c.owner, "/land/transactions", land, businesshttp.OpsRecordTransition{Action: "COMPLETE", EvidenceRefs: evidence()}, 200)
	if land.State != "COMPLETED" || alloc.State != "PERFECTED" {
		t.Fatalf("land %s allocation %s", land.State, alloc.State)
	}
	ag := signAgreementIn(c, parcel.ID, land.ID, "SALE_CONTRACT", "buyer:kemi")
	if ag.ScopeKind != "PARCEL" {
		t.Fatal(ag.ScopeKind)
	}
}

// Journey 10: buyer offer -> unit hold -> conditions -> payment milestones
// -> closing, with cross-domain unit conservation.
func TestAnnexDSaleOfferUnitHoldConditionsPaymentMilestonesClosing(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	c.grant(c.owner, "sales:approve")
	sale := opsRec(c, c.owner, "POST", "/sales", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "UNIT_SALE", Title: "Sale apartment 101", Reason: "List for sale"}, 201)
	holdPath := "/sales/" + sale.ID.String() + "/unit-holds"
	hold := time.Now().UTC().Add(5 * 24 * time.Hour)
	unitHold := opsRec(c, c.owner, "POST", holdPath, businesshttp.OpsRecordCreate{SubjectID: &c.unit.ID, Kind: "BUYER_HOLD", Title: "Hold for buyer", CounterpartyReference: "buyer:tunde", EndsAt: &hold, Reason: "Buyer reservation"}, 201)
	c.must(c.owner, "POST", holdPath, businesshttp.OpsRecordCreate{SubjectID: &c.unit.ID, Kind: "BUYER_HOLD", Title: "Second hold", CounterpartyReference: "buyer:other", EndsAt: &hold, Reason: "Double hold"}, 409)
	amount := int64(90000000)
	offer := opsRec(c, c.owner, "POST", "/sales/"+sale.ID.String()+"/offers", businesshttp.OpsRecordCreate{Kind: "CASH", Title: "Buyer offer", CounterpartyReference: "buyer:tunde", AmountMinor: &amount, Currency: "NGN", Reason: "Offer received"}, 201)
	c.must(c.owner, "POST", "/sales/"+sale.ID.String()+"/offers/"+offer.ID.String()+"/decision", businesshttp.SaleOfferDecision{Version: offer.Version, Decision: "ACCEPT", Reason: "Accept"}, 200)
	sale.Version = currentVersion(c, "sale_transactions", sale.ID)
	approval := c.approval("HIGH_VALUE_BULK_DISPOSAL_OR_SALE", "approvals:decide", "", nil, "", map[string]any{"domain": "sale_transaction", "record_id": sale.ID, "action": "APPROVE", "version": sale.Version})
	sale = opsMove(c, c.owner, "/sales", sale, businesshttp.OpsRecordTransition{Action: "APPROVE", ApprovalID: approval}, 200)
	survey := opsRec(c, c.owner, "POST", "/due-diligence", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &sale.ID, Kind: "SURVEY", Title: "Building survey", Reason: "Buyer condition"}, 201)
	opsMove(c, c.owner, "/sales", sale, businesshttp.OpsRecordTransition{Action: "CONTRACT", EvidenceRefs: evidence()}, 409)
	opsMove(c, c.approver, "/due-diligence", survey, businesshttp.OpsRecordTransition{Action: "WAIVE"}, 200)
	sale = opsMove(c, c.owner, "/sales", sale, businesshttp.OpsRecordTransition{Action: "CONTRACT", EvidenceRefs: evidence()}, 200)
	unitHold = opsMove(c, c.owner, holdPath, unitHold, businesshttp.OpsRecordTransition{Action: "CONVERT", EvidenceRefs: evidence()}, 200)
	closeBy := time.Now().UTC().Add(15 * 24 * time.Hour)
	balance := opsRec(c, c.owner, "POST", "/closing-milestones", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &sale.ID, Kind: "BALANCE_PAID", Title: "Balance", EndsAt: &closeBy, Reason: "Payment milestone"}, 201)
	keys := opsRec(c, c.owner, "POST", "/closing-milestones", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &sale.ID, Kind: "KEYS_HANDED_OVER", Title: "Keys", EndsAt: &closeBy, Reason: "Handover milestone"}, 201)
	opsMove(c, c.owner, "/sales", sale, businesshttp.OpsRecordTransition{Action: "COMPLETE", EvidenceRefs: evidence()}, 409)
	ob := a.settledObligation("closing_milestone:"+balance.ID.String(), amount, true)
	balance = opsMove(c, c.owner, "/closing-milestones", balance, businesshttp.OpsRecordTransition{Action: "SUBMIT_EVIDENCE", EvidenceRefs: evidence(), ExternalReference: ob.String()}, 200)
	opsMove(c, c.approver, "/closing-milestones", balance, businesshttp.OpsRecordTransition{Action: "CONFIRM"}, 200)
	keys = opsMove(c, c.owner, "/closing-milestones", keys, businesshttp.OpsRecordTransition{Action: "SUBMIT_EVIDENCE", EvidenceRefs: evidence(), ExternalReference: "handover-protocol-1"}, 200)
	opsMove(c, c.approver, "/closing-milestones", keys, businesshttp.OpsRecordTransition{Action: "CONFIRM"}, 200)
	sale = opsMove(c, c.owner, "/sales", sale, businesshttp.OpsRecordTransition{Action: "COMPLETE", EvidenceRefs: evidence()}, 200)
	if sale.State != "COMPLETED" || unitHold.State != "CONVERTED" {
		t.Fatalf("sale %s hold %s", sale.State, unitHold.State)
	}
	// Expiry of an unconverted hold and release on a failed sale.
	sale2 := opsRec(c, c.owner, "POST", "/sales", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "BULK_DISPOSAL", Title: "Second sale", Reason: "Another listing"}, 201)
	unit2 := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", "/units", map[string]any{"parent_id": c.property.ID, "code": "U-202", "name": "Unit 202", "attributes": map[string]any{"classification": "APARTMENT"}}, 201))
	h2 := opsRec(c, c.owner, "POST", "/sales/"+sale2.ID.String()+"/unit-holds", businesshttp.OpsRecordCreate{SubjectID: &unit2.ID, Kind: "BUYER_HOLD", Title: "Hold 202", CounterpartyReference: "buyer:ada", EndsAt: &hold, Reason: "Hold"}, 201)
	opsExpire(c, "sale_allocations", h2.ID, "ends_at")
	if err := c.domain.ProcessOperations(t.Context(), 50); err != nil {
		t.Fatal(err)
	}
	if opsState(c, "sale_allocations", h2.ID) != "EXPIRED" {
		t.Fatal("hold not expired")
	}
}

// Journey 11: off-plan milestone -> professional progress certification ->
// costed and approved variation -> snagging -> handover, with conservation
// against sale holds.
func TestAnnexDOffPlanProgressCertificationVariationAndConservation(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	branch := c.inventoryBranch(c.owner)
	dev := c.inventoryNode(c.owner, "/projects", map[string]any{"branch_id": branch, "code": "ESTATE-9", "name": "Ikoyi estate"})
	prop := c.inventoryNode(c.owner, "/properties", map[string]any{"parent_id": dev.ID, "code": "ESTATE9-A", "name": "Block A", "capacity_code": "DEVELOPER"})
	unit := c.inventoryNode(c.owner, "/units", map[string]any{"parent_id": prop.ID, "code": "A-1", "name": "Unit A-1", "attributes": map[string]any{"classification": "APARTMENT"}})
	base := "/projects/" + dev.ID.String()
	due := time.Now().UTC().Add(60 * 24 * time.Hour)
	ms := opsRec(c, c.owner, "POST", base+"/milestones", businesshttp.OpsRecordCreate{Kind: "FOUNDATION", Title: "Foundation", EndsAt: &due, Reason: "Plan"}, 201)
	ms = opsMove(c, c.owner, base+"/milestones", ms, businesshttp.OpsRecordTransition{Action: "START"}, 200)
	ms = opsMove(c, c.owner, base+"/milestones", ms, businesshttp.OpsRecordTransition{Action: "REQUEST_CERTIFICATION", EvidenceRefs: evidence()}, 200)
	c.must(c.owner, "POST", base+"/progress-certifications", businesshttp.OpsRecordCreate{Kind: "INTERIM", Title: "No milestone", Attributes: map[string]any{"percent_complete": 50}, Reason: "Missing"}, 400)
	cert := opsRec(c, c.owner, "POST", base+"/progress-certifications", businesshttp.OpsRecordCreate{Kind: "INTERIM", Title: "Foundation certificate", Attributes: map[string]any{"milestone_id": ms.ID.String(), "percent_complete": 100}, Reason: "Contractor claim"}, 201)
	opsMove(c, c.approver, base+"/milestones", ms, businesshttp.OpsRecordTransition{Action: "CERTIFY", EvidenceRefs: evidence()}, 409)
	cert = opsMove(c, c.owner, base+"/progress-certifications", cert, businesshttp.OpsRecordTransition{Action: "START_REVIEW", ExternalReference: "qs-appointment-9"}, 200)
	opsMove(c, c.owner, base+"/progress-certifications", cert, businesshttp.OpsRecordTransition{Action: "CERTIFY", EvidenceRefs: evidence(), ExternalReference: "qs-cert-9"}, 403)
	opsMove(c, c.approver, base+"/progress-certifications", cert, businesshttp.OpsRecordTransition{Action: "CERTIFY", EvidenceRefs: evidence(), ExternalReference: "qs-cert-9"}, 200)
	ms = opsMove(c, c.approver, base+"/milestones", ms, businesshttp.OpsRecordTransition{Action: "CERTIFY", EvidenceRefs: evidence()}, 200)
	// Variation costed and independently approved at the costed amount.
	c.must(c.owner, "POST", base+"/variations", businesshttp.OpsRecordCreate{Kind: "SPECIFICATION", Title: "No justification", Reason: "Missing"}, 400)
	v := opsRec(c, c.owner, "POST", base+"/variations", businesshttp.OpsRecordCreate{Kind: "SPECIFICATION", Title: "Upgrade windows", Attributes: map[string]any{"justification": "Energy efficiency requirement"}, Reason: "Design change"}, 201)
	cost := int64(12000000)
	v = opsMove(c, c.owner, base+"/variations", v, businesshttp.OpsRecordTransition{Action: "COST", AmountMinor: &cost, Currency: "NGN", EvidenceRefs: evidence()}, 200)
	vApproval := c.approvalAt("COMMERCIAL_PROJECT_VARIATION", "projects:write", "", &cost, "NGN", map[string]any{"domain": "project_variation", "record_id": v.ID, "action": "APPROVE", "version": v.Version}, "DEVELOPMENT", dev.ID)
	v = opsMove(c, c.owner, base+"/variations", v, businesshttp.OpsRecordTransition{Action: "APPROVE", ApprovalID: vApproval}, 200)
	v = opsMove(c, c.owner, base+"/variations", v, businesshttp.OpsRecordTransition{Action: "IMPLEMENT", EvidenceRefs: evidence()}, 200)
	// Conservation: a unit held in a sale cannot also be allocated off-plan.
	sale := opsRec(c, c.owner, "POST", "/sales", businesshttp.OpsRecordCreate{ScopeID: &prop.ID, SubjectID: &unit.ID, Kind: "OFF_PLAN_SALE", Title: "Off-plan sale A-1", Reason: "List"}, 201)
	hold := time.Now().UTC().Add(5 * 24 * time.Hour)
	sh := opsRec(c, c.owner, "POST", "/sales/"+sale.ID.String()+"/unit-holds", businesshttp.OpsRecordCreate{SubjectID: &unit.ID, Kind: "BUYER_HOLD", Title: "Hold A-1", CounterpartyReference: "buyer:femi", EndsAt: &hold, Reason: "Hold"}, 201)
	price := int64(55000000)
	c.must(c.owner, "POST", base+"/allocations", businesshttp.OpsRecordCreate{SubjectID: &unit.ID, Kind: "OFF_PLAN", Title: "Allocation A-1", CounterpartyReference: "buyer:other", AmountMinor: &price, Currency: "NGN", EndsAt: &hold, Reason: "Conflicting allocation"}, 409)
	sale = opsMove(c, c.owner, "/sales", sale, businesshttp.OpsRecordTransition{Action: "WITHDRAW"}, 200)
	if opsState(c, "sale_allocations", sh.ID) != "RELEASED" {
		t.Fatal("withdrawn sale hold not released")
	}
	opsRec(c, c.owner, "POST", base+"/allocations", businesshttp.OpsRecordCreate{SubjectID: &unit.ID, Kind: "OFF_PLAN", Title: "Allocation A-1", CounterpartyReference: "buyer:femi", AmountMinor: &price, Currency: "NGN", EndsAt: &hold, Reason: "Allocation after release"}, 201)
	if ms.State != "CERTIFIED" || v.State != "IMPLEMENTED" {
		t.Fatalf("milestone %s variation %s", ms.State, v.State)
	}
}

// Journey 12: work order -> vendor quotes -> independent quote acceptance ->
// approval bound to the accepted quote -> provider assignment -> evidence ->
// verification -> warranty -> asset service history.
func TestAnnexDMaintenanceQuotesProviderWarrantyAndAssetHistory(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	wo := opsRec(c, c.owner, "POST", "/work-orders", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "REPAIR", Title: "Replace inverter", Attributes: map[string]any{"warranty_days": 90}, Reason: "Failure report"}, 201)
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "REQUEST_QUOTE", ExternalReference: "artisan-request-5"}, 200)
	qPath := "/work-orders/" + wo.ID.String() + "/quotes"
	q1 := opsRec(c, c.owner, "POST", qPath, businesshttp.OpsRecordCreate{Kind: "FIXED_PRICE", Title: "Quote vendor A", CounterpartyReference: "artisan:vendor-a", Reason: "Request quote"}, 201)
	q2 := opsRec(c, c.owner, "POST", qPath, businesshttp.OpsRecordCreate{Kind: "FIXED_PRICE", Title: "Quote vendor B", CounterpartyReference: "artisan:vendor-b", Reason: "Request quote"}, 201)
	a1, a2 := int64(400000), int64(350000)
	q1 = opsMove(c, c.owner, qPath, q1, businesshttp.OpsRecordTransition{Action: "RECORD_QUOTE", AmountMinor: &a1, Currency: "NGN", ExternalReference: "quote-a", EvidenceRefs: evidence()}, 200)
	q2 = opsMove(c, c.owner, qPath, q2, businesshttp.OpsRecordTransition{Action: "RECORD_QUOTE", AmountMinor: &a2, Currency: "NGN", ExternalReference: "quote-b", EvidenceRefs: evidence()}, 200)
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "RECORD_QUOTE", AmountMinor: &a2, Currency: "NGN", ExternalReference: "quote-b", EvidenceRefs: evidence()}, 200)
	early := c.approval("COMMERCIAL_WORK_ORDER", "maintenance:approve", "", &a2, "NGN", map[string]any{"domain": "work_order", "record_id": wo.ID, "action": "APPROVE", "version": wo.Version})
	c.must(c.owner, "POST", "/work-orders/"+wo.ID.String()+"/approve", businesshttp.OpsRecordTransition{Version: wo.Version, ApprovalID: early, Reason: "No accepted quote"}, 409)
	opsMove(c, c.owner, qPath, q2, businesshttp.OpsRecordTransition{Action: "ACCEPT"}, 403)
	q2 = opsMove(c, c.approver, qPath, q2, businesshttp.OpsRecordTransition{Action: "ACCEPT"}, 200)
	if q2.State != "ACCEPTED" {
		t.Fatalf("q2 state %s", q2.State)
	}
	opsMove(c, c.approver, qPath, q1, businesshttp.OpsRecordTransition{Action: "ACCEPT"}, 409)
	// The approval is bound to the accepted quote amount, not a typed one.
	typed := int64(999999)
	c.must(c.owner, "POST", "/work-orders/"+wo.ID.String()+"/approve", businesshttp.OpsRecordTransition{Version: wo.Version, ApprovalID: c.approval("COMMERCIAL_WORK_ORDER", "maintenance:approve", "", &typed, "NGN", map[string]any{"domain": "work_order", "record_id": wo.ID, "action": "APPROVE", "version": wo.Version}), AmountMinor: &typed, Currency: "NGN", Reason: "Inflated amount"}, 403)
	wo = opsRec(c, c.owner, "POST", "/work-orders/"+wo.ID.String()+"/approve", businesshttp.OpsRecordTransition{Version: wo.Version, ApprovalID: early, Reason: "Approve accepted quote"}, 200)
	if *wo.AmountMinor != a2 {
		t.Fatalf("approved amount %d", *wo.AmountMinor)
	}
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "ASSIGN", ExternalReference: "artisan-job-5"}, 200)
	pPath := "/work-orders/" + wo.ID.String() + "/provider-assignments"
	pa := opsRec(c, c.owner, "POST", pPath, businesshttp.OpsRecordCreate{Kind: "ARTISAN", Title: "Vendor B assignment", CounterpartyReference: "artisan:vendor-b", Reason: "Assign provider"}, 201)
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "START"}, 200)
	opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "COMPLETE", EvidenceRefs: evidence()}, 409)
	pa = opsMove(c, c.owner, pPath, pa, businesshttp.OpsRecordTransition{Action: "RECORD_ACCEPTANCE", ExternalReference: "artisan-accept-5"}, 200)
	pa = opsMove(c, c.owner, pPath, pa, businesshttp.OpsRecordTransition{Action: "RECORD_ARRIVAL", EvidenceRefs: evidence()}, 200)
	pa = opsMove(c, c.owner, pPath, pa, businesshttp.OpsRecordTransition{Action: "RECORD_COMPLETION", EvidenceRefs: evidence()}, 200)
	if pa.State != "COMPLETED" {
		t.Fatalf("pa state %s", pa.State)
	}
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "COMPLETE", EvidenceRefs: evidence()}, 200)
	wo = opsMove(c, c.approver, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "VERIFY", EvidenceRefs: evidence()}, 200)
	start := time.Now().UTC().Add(-24 * time.Hour)
	end := start.AddDate(1, 0, 0)
	w := opsRec(c, c.owner, "POST", "/warranties", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "WORKMANSHIP", Title: "Inverter workmanship", CounterpartyReference: "artisan:vendor-b", StartsAt: &start, EndsAt: &end, Attributes: map[string]any{"work_order_id": wo.ID.String()}, Reason: "Workmanship warranty"}, 201)
	w = opsMove(c, c.owner, "/warranties", w, businesshttp.OpsRecordTransition{Action: "CLAIM", EvidenceRefs: evidence(), ExternalReference: "warranty-claim-1"}, 200)
	w = opsMove(c, c.owner, "/warranties", w, businesshttp.OpsRecordTransition{Action: "RECORD_CLAIM_OUTCOME", EvidenceRefs: evidence()}, 200)
	opsExpire(c, "warranties", w.ID, "ends_at")
	sc := opsRec(c, c.owner, "POST", "/service-contracts", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "GENERATOR", Title: "Generator service contract", CounterpartyReference: "vendor:powergen", StartsAt: &start, EndsAt: ptrTime(start.Add(20 * 24 * time.Hour)), Reason: "Annual service"}, 201)
	if err := c.domain.ProcessOperations(t.Context(), 50); err != nil {
		t.Fatal(err)
	}
	if opsState(c, "warranties", w.ID) != "EXPIRED" || opsState(c, "service_contract_refs", sc.ID) != "EXPIRING" {
		t.Fatalf("warranty %s contract %s", opsState(c, "warranties", w.ID), opsState(c, "service_contract_refs", sc.ID))
	}
	history := commercialData[struct {
		Items []map[string]any `json:"items"`
	}](t, c.must(c.owner, "GET", "/assets/"+c.unit.ID.String()+"/service-history?property_id="+c.property.ID.String(), nil, 200))
	kinds := map[string]bool{}
	for _, item := range history.Items {
		kinds[item["entity_type"].(string)] = true
	}
	if !kinds["work_order"] || !kinds["warranty"] {
		t.Fatalf("asset history %+v", history.Items)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// Journeys 13/14 supplements: manager performance and professional
// findings linked to due-diligence conditions.
func TestAnnexDManagerPerformanceAndProfessionalFindingConditionLink(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	appt := opsRec(c, c.owner, "POST", "/professional-appointments", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "SURVEYOR", Title: "Structural survey", CounterpartyReference: "professional:surveyor-1", Reason: "Pre-sale survey"}, 201)
	fPath := "/professional-appointments/" + appt.ID.String() + "/findings"
	c.must(c.owner, "POST", fPath, businesshttp.OpsRecordCreate{Kind: "DEFECT", Title: "Too early", EvidenceRefs: evidence(), Reason: "Not started"}, 409)
	appt = opsMove(c, c.owner, "/professional-appointments", appt, businesshttp.OpsRecordTransition{Action: "ACCEPT", ExternalReference: "prof-booking-1"}, 200)
	appt = opsMove(c, c.owner, "/professional-appointments", appt, businesshttp.OpsRecordTransition{Action: "START"}, 200)
	c.must(c.owner, "POST", fPath, businesshttp.OpsRecordCreate{Kind: "DEFECT", Title: "No evidence", Reason: "Missing"}, 400)
	f := opsRec(c, c.owner, "POST", fPath, businesshttp.OpsRecordCreate{Kind: "DEFECT", Title: "Roof membrane failure", EvidenceRefs: evidence(), Reason: "Surveyor finding"}, 201)
	opsMove(c, c.owner, fPath, f, businesshttp.OpsRecordTransition{Action: "ACCEPT"}, 403)
	f = opsMove(c, c.approver, fPath, f, businesshttp.OpsRecordTransition{Action: "ACCEPT"}, 200)
	sale := opsRec(c, c.owner, "POST", "/sales", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "UNIT_SALE", Title: "Sale with survey", Reason: "List"}, 201)
	dd := opsRec(c, c.owner, "POST", "/due-diligence", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &sale.ID, Kind: "TECHNICAL", Title: "Roof repair condition", Reason: "Finding condition"}, 201)
	opsMove(c, c.owner, fPath, f, businesshttp.OpsRecordTransition{Action: "LINK_CONDITION", ExternalReference: ids.New().String()}, 409)
	f = opsMove(c, c.owner, fPath, f, businesshttp.OpsRecordTransition{Action: "LINK_CONDITION", ExternalReference: dd.ID.String()}, 200)
	if f.State != "LINKED_TO_CONDITION" {
		t.Fatal(f.State)
	}
	ends := time.Now().UTC().Add(60 * 24 * time.Hour)
	ma := opsRec(c, c.owner, "POST", "/manager-assignments", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "PROPERTY_MANAGER", Title: "Managed letting", CounterpartyReference: "manager-co:9", EndsAt: &ends, Attributes: map[string]any{"capabilities": []string{"maintenance:write"}, "mandate_reference": "mandate-9"}, Reason: "Appoint manager"}, 201)
	pPath := "/manager-assignments/" + ma.ID.String() + "/performance"
	from := time.Now().UTC().Add(-30 * 24 * time.Hour)
	to := time.Now().UTC()
	c.must(c.owner, "POST", pPath, businesshttp.OpsRecordCreate{Kind: "MONTHLY", Title: "Not active", StartsAt: &from, EndsAt: ptrTime(to.Add(time.Hour)), Reason: "Proposed only"}, 409)
}

// Journeys 21-23: financing consent scope/expiry, insurance compliance and
// smart-access provider outcomes including unknown results.
func TestAnnexDFinancingConsentInsuranceComplianceAndAccessOutcomes(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	principal := int64(50000000)
	fo := opsRec(c, c.owner, "POST", "/financing", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "MORTGAGE", Title: "Buyer mortgage", CounterpartyReference: "lender:bank-1", AmountMinor: &principal, Currency: "NGN", Attributes: map[string]any{"total_cost_minor": 80000000, "apr_bps": 1800, "term_months": 240}, Reason: "Mortgage request"}, 201)
	fo = opsMove(c, c.owner, "/financing", fo, businesshttp.OpsRecordTransition{Action: "RECORD_CONSENT", EvidenceRefs: evidence()}, 200)
	opsMove(c, c.owner, "/financing", fo, businesshttp.OpsRecordTransition{Action: "SUBMIT", ExternalReference: "lender-app-1"}, 409)
	cPath := "/financing/" + fo.ID.String() + "/consents"
	expiry := time.Now().UTC().Add(90 * 24 * time.Hour)
	c.must(c.owner, "POST", cPath, businesshttp.OpsRecordCreate{Kind: "DATA_SHARING", Title: "Overbroad consent", CounterpartyReference: "lender:bank-1", EndsAt: &expiry, EvidenceRefs: evidence(), Attributes: map[string]any{"data_scope": []string{"identity", "medical_history"}}, Reason: "Bad scope"}, 400)
	consent := opsRec(c, c.owner, "POST", cPath, businesshttp.OpsRecordCreate{Kind: "DATA_SHARING", Title: "Lender data sharing", CounterpartyReference: "lender:bank-1", EndsAt: &expiry, EvidenceRefs: evidence(), Attributes: map[string]any{"data_scope": []string{"identity", "income", "transaction_price"}}, Reason: "Customer consent"}, 201)
	fo = opsMove(c, c.owner, "/financing", fo, businesshttp.OpsRecordTransition{Action: "SUBMIT", ExternalReference: "lender-app-1"}, 200)
	opsMove(c, c.owner, cPath, consent, businesshttp.OpsRecordTransition{Action: "REVOKE", EvidenceRefs: evidence()}, 200)
	// Insurance compliance evaluated from policy references.
	start := time.Now().UTC().Add(-time.Hour)
	end := time.Now().UTC().Add(200 * 24 * time.Hour)
	cover := int64(900000000)
	opsRec(c, c.owner, "POST", "/insurance/policies", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "PROPERTY", Title: "Building cover", CounterpartyReference: "insurer:alpha", AmountMinor: &cover, Currency: "NGN", StartsAt: &start, EndsAt: &end, Reason: "Policy reference"}, 201)
	through := time.Now().UTC().Add(100 * 24 * time.Hour)
	ok := opsRec(c, c.owner, "POST", "/insurance/compliance-checks", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "PROPERTY", Title: "Lender cover requirement", EndsAt: &through, Attributes: map[string]any{"required_cover_minor": 500000000}, Reason: "Lender covenant"}, 201)
	gap := opsRec(c, c.owner, "POST", "/insurance/compliance-checks", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "LIABILITY", Title: "Liability requirement", EndsAt: &through, Reason: "Lease covenant"}, 201)
	if ok.State != "COMPLIANT" || gap.State != "NON_COMPLIANT" {
		t.Fatalf("compliance %s %s", ok.State, gap.State)
	}
	opsMove(c, c.owner, "/insurance/compliance-checks", gap, businesshttp.OpsRecordTransition{Action: "RECORD_REMEDIATION", EvidenceRefs: evidence(), ExternalReference: "insurer:beta:policy-2"}, 200)
	// Smart access: only provider-confirmed outcomes change the grant.
	dev := opsRec(c, c.owner, "POST", "/access/devices", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "SMART_LOCK", Title: "Front door lock", CounterpartyReference: "lock-provider:acme", Reason: "Register"}, 201)
	dev = opsMove(c, c.owner, "/access/devices", dev, businesshttp.OpsRecordTransition{Action: "ACTIVATE", EvidenceRefs: evidence(), ExternalReference: "pairing-1"}, 200)
	gs := time.Now().UTC().Add(time.Hour)
	ge := gs.Add(48 * time.Hour)
	grant := opsRec(c, c.owner, "POST", "/access/grants", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, ParentID: &dev.ID, Kind: "GUEST", Title: "Guest code", CounterpartyReference: "guest:stay-1", StartsAt: &gs, EndsAt: &ge, Reason: "Stay access"}, 201)
	issue := opsRec(c, c.owner, "POST", "/access/events", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, ParentID: &grant.ID, Kind: "ISSUE_COMMAND", Title: "Issue code", Reason: "Provider command"}, 201)
	issue = opsMove(c, c.owner, "/access/events", issue, businesshttp.OpsRecordTransition{Action: "RECORD_UNKNOWN"}, 200)
	if opsState(c, "access_grants", grant.ID) != "REQUESTED" {
		t.Fatal("unknown provider result activated grant")
	}
	opsMove(c, c.owner, "/access/events", issue, businesshttp.OpsRecordTransition{Action: "RESOLVE_MANUALLY", EvidenceRefs: evidence()}, 403)
	issue = opsMove(c, c.owner, "/access/events", issue, businesshttp.OpsRecordTransition{Action: "RECORD_CONFIRMED", ExternalReference: "provider-op-77", EvidenceRefs: evidence()}, 200)
	if issue.State != "CONFIRMED" {
		t.Fatalf("issue state %s", issue.State)
	}
	if opsState(c, "access_grants", grant.ID) != "ACTIVE" {
		t.Fatal("provider confirmation did not activate grant")
	}
	revoke := opsRec(c, c.owner, "POST", "/access/events", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, ParentID: &grant.ID, Kind: "REVOKE_COMMAND", Title: "Revoke code", Reason: "Early checkout"}, 201)
	opsMove(c, c.owner, "/access/events", revoke, businesshttp.OpsRecordTransition{Action: "RECORD_CONFIRMED", ExternalReference: "provider-op-78", EvidenceRefs: evidence()}, 200)
	if opsState(c, "access_grants", grant.ID) != "REVOKED" {
		t.Fatal("grant not revoked")
	}
	hPath := "/access/devices/" + dev.ID.String() + "/health"
	opsRec(c, c.owner, "POST", hPath, businesshttp.OpsRecordCreate{Kind: "OFFLINE", Title: "Device offline", Reason: "Heartbeat missed"}, 201)
	if opsState(c, "access_devices", dev.ID) != "OFFLINE" {
		t.Fatal("offline snapshot did not mark device offline")
	}
}

// Journey 24: partner credential -> webhook subscription -> rotation ->
// revocation cascade; secrets are never accepted.
func TestAnnexDPartnerCredentialsWebhooksRotationAndRevocation(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	ic := opsRec(c, c.owner, "POST", "/integrations", businesshttp.OpsRecordCreate{Kind: "ACCOUNTING", Title: "Ledger sync", CounterpartyReference: "partner:ledgerco", Attributes: map[string]any{"scopes": []string{"finance:read"}, "credential_reference": smRef("ledgerco"), "rate_limit_per_minute": 120}, Reason: "Accounting connection"}, 201)
	ic = opsMove(c, c.owner, "/integrations", ic, businesshttp.OpsRecordTransition{Action: "ACTIVATE", EvidenceRefs: evidence()}, 200)
	credPath := "/integrations/" + ic.ID.String() + "/credentials"
	exp := time.Now().UTC().Add(180 * 24 * time.Hour)
	fp := fixtureFingerprint("a")
	c.must(c.owner, "POST", credPath, businesshttp.OpsRecordCreate{Kind: "API_KEY_REF", Title: "Raw secret", EndsAt: &exp, Attributes: map[string]any{"credential_reference": smRef("ledgerco/key1"), "fingerprint_sha256": fp, "api_key": "raw-value"}, Reason: "Secret leak"}, 400)
	cred := opsRec(c, c.owner, "POST", credPath, businesshttp.OpsRecordCreate{Kind: "API_KEY_REF", Title: "Ledger API key", EndsAt: &exp, Attributes: map[string]any{"credential_reference": smRef("ledgerco/key1"), "fingerprint_sha256": fp}, Reason: "Issue credential"}, 201)
	next := fixtureFingerprint("b")
	cred = opsMove(c, c.owner, credPath, cred, businesshttp.OpsRecordTransition{Action: "START_ROTATION", ExternalReference: smRef("ledgerco/key2"), Attributes: map[string]any{"next_fingerprint_sha256": next}}, 200)
	cred = opsMove(c, c.owner, credPath, cred, businesshttp.OpsRecordTransition{Action: "COMPLETE_ROTATION"}, 200)
	if m := attrMap(t, cred); m["credential_reference"] != smRef("ledgerco/key2") || m["fingerprint_sha256"] != next {
		t.Fatalf("rotation %v", m)
	}
	whPath := "/integrations/" + ic.ID.String() + "/webhooks"
	for _, bad := range []string{"http://partner.example.com/hook", "https://127.0.0.1/hook", "https://10.0.0.5/hook", "https://localhost/hook", "https://user:pw@partner.example.com/hook"} {
		c.must(c.owner, "POST", whPath, businesshttp.OpsRecordCreate{Kind: "RESERVATION_EVENTS", Title: "Bad webhook", Attributes: map[string]any{"url": bad, "signing_secret_reference": smRef("ledgerco/hmac"), "event_types": []string{"reservation.created.v1"}}, Reason: "Unsafe URL"}, 400)
	}
	wh := opsRec(c, c.owner, "POST", whPath, businesshttp.OpsRecordCreate{Kind: "RESERVATION_EVENTS", Title: "Reservation webhook", Attributes: map[string]any{"url": "https://hooks.partner.example.com/cf", "signing_secret_reference": smRef("ledgerco/hmac"), "event_types": []string{"reservation.created.v1"}}, Reason: "Subscribe"}, 201)
	wh = opsMove(c, c.owner, whPath, wh, businesshttp.OpsRecordTransition{Action: "VERIFY", ExternalReference: "challenge-ok-1"}, 200)
	ic = opsMove(c, c.owner, "/integrations", ic, businesshttp.OpsRecordTransition{Action: "SUSPEND"}, 200)
	if opsState(c, "webhook_subscriptions", wh.ID) != "PAUSED" {
		t.Fatal("suspension did not pause webhook")
	}
	ic = opsMove(c, c.owner, "/integrations", ic, businesshttp.OpsRecordTransition{Action: "REVOKE"}, 200)
	if opsState(c, "api_credentials", cred.ID) != "REVOKED" || opsState(c, "webhook_subscriptions", wh.ID) != "REVOKED" {
		t.Fatal("revocation did not cascade")
	}
	c.must(c.owner, "POST", credPath, businesshttp.OpsRecordCreate{Kind: "API_KEY_REF", Title: "After revoke", EndsAt: &exp, Attributes: map[string]any{"credential_reference": smRef("ledgerco/key3"), "fingerprint_sha256": fp}, Reason: "Too late"}, 409)
}

// Journeys 25 and 30: source-backed FX freshness, verified organisation
// change, and closure blocked by live obligations then retained.
func TestAnnexDFXFreshnessOrganisationChangeAndClosureContinuity(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	org := c.owner.registered.OrganisationID
	asOf := time.Now().UTC().Add(-time.Hour)
	fresh := asOf.Add(24 * time.Hour)
	c.must(c.owner, "POST", "/fx-rates", businesshttp.OpsRecordCreate{Kind: "SETTLEMENT", Title: "Unsourced rate", StartsAt: &asOf, EndsAt: &fresh, Attributes: map[string]any{"base_currency": "USD", "quote_currency": "NGN", "rate": "1550.25"}, Reason: "No source"}, 400)
	c.must(c.owner, "POST", "/fx-rates", businesshttp.OpsRecordCreate{Kind: "SETTLEMENT", Title: "Float rate", StartsAt: &asOf, EndsAt: &fresh, Attributes: map[string]any{"base_currency": "USD", "quote_currency": "NGN", "rate": 1550.25, "source_reference": "cbn:2026-11-13"}, Reason: "Float"}, 400)
	r1 := opsRec(c, c.owner, "POST", "/fx-rates", businesshttp.OpsRecordCreate{Kind: "SETTLEMENT", Title: "USD/NGN settlement", StartsAt: &asOf, EndsAt: &fresh, Attributes: map[string]any{"base_currency": "USD", "quote_currency": "NGN", "rate": "1550.25", "source_reference": "cbn:2026-11-13"}, Reason: "Daily rate"}, 201)
	opsMove(c, c.owner, "/fx-rates", r1, businesshttp.OpsRecordTransition{Action: "ACTIVATE"}, 403)
	r1 = opsMove(c, c.approver, "/fx-rates", r1, businesshttp.OpsRecordTransition{Action: "ACTIVATE"}, 200)
	r2 := opsRec(c, c.owner, "POST", "/fx-rates", businesshttp.OpsRecordCreate{Kind: "SETTLEMENT", Title: "USD/NGN settlement 2", StartsAt: &asOf, EndsAt: &fresh, Attributes: map[string]any{"base_currency": "USD", "quote_currency": "NGN", "rate": "1551.00", "source_reference": "cbn:2026-11-13T12"}, Reason: "Intraday"}, 201)
	r2 = opsMove(c, c.approver, "/fx-rates", r2, businesshttp.OpsRecordTransition{Action: "ACTIVATE"}, 200)
	if opsState(c, "fx_rate_refs", r1.ID) != "SUPERSEDED" {
		t.Fatal("older rate not superseded")
	}
	opsExpire(c, "fx_rate_refs", r2.ID, "ends_at")
	if err := c.domain.ProcessOperations(t.Context(), 50); err != nil {
		t.Fatal(err)
	}
	if opsState(c, "fx_rate_refs", r2.ID) != "STALE" {
		t.Fatal("rate not marked stale")
	}
	// Name change: verification + independent approval before applying.
	c.must(c.owner, "POST", "/business-changes", businesshttp.OpsRecordCreate{Kind: "NAME_CHANGE", Title: "Rename", Reason: "Missing name"}, 400)
	ch := opsRec(c, c.owner, "POST", "/business-changes", businesshttp.OpsRecordCreate{Kind: "NAME_CHANGE", Title: "Rename to Cosmic Homes Ltd", Attributes: map[string]any{"new_legal_name": "Cosmic Homes Ltd"}, Reason: "Registry rename"}, 201)
	ch = opsMove(c, c.owner, "/business-changes", ch, businesshttp.OpsRecordTransition{Action: "SUBMIT", EvidenceRefs: evidence()}, 200)
	ch = opsMove(c, c.owner, "/business-changes", ch, businesshttp.OpsRecordTransition{Action: "RECORD_VERIFICATION", ExternalReference: "verification:kyb-change-1"}, 200)
	opsMove(c, c.owner, "/business-changes", ch, businesshttp.OpsRecordTransition{Action: "APPROVE"}, 403)
	chApproval := c.approvalAt("COMMERCIAL_ORGANISATION_CHANGE", "organisation:verify", "", nil, "", map[string]any{"domain": "organisation_change_case", "record_id": ch.ID, "action": "APPROVE", "version": ch.Version}, "COMPANY", org)
	ch = opsMove(c, c.owner, "/business-changes", ch, businesshttp.OpsRecordTransition{Action: "APPROVE", ApprovalID: chApproval}, 200)
	ch = opsMove(c, c.owner, "/business-changes", ch, businesshttp.OpsRecordTransition{Action: "APPLY"}, 200)
	var name string
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT registered_name FROM organisations WHERE id=$1`, org).Scan(&name); err != nil || name != "Cosmic Homes Ltd" {
		t.Fatalf("name %q %v", name, err)
	}
	// Closure blocked by a live tenancy; continuity before approval.
	rent := int64(100000)
	start := time.Now().UTC()
	ten := opsRec(c, c.owner, "POST", "/tenancies", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "PERIODIC", Title: "Periodic tenancy", CounterpartyReference: "tenant:ola", AmountMinor: &rent, Currency: "NGN", StartsAt: &start, Reason: "Periodic letting"}, 201)
	ten = opsMove(c, c.owner, "/tenancies", ten, businesshttp.OpsRecordTransition{Action: "SEND_FOR_SIGNATURE"}, 200)
	signAgreement(c, ten.ID, "RESIDENTIAL_TENANCY", "tenant:ola")
	act := c.approval("COMMERCIAL_TENANCY", "residential:write", "", &rent, "NGN", map[string]any{"domain": "tenancy", "record_id": ten.ID, "action": "ACTIVATE", "version": ten.Version})
	ten = opsMove(c, c.owner, "/tenancies", ten, businesshttp.OpsRecordTransition{Action: "ACTIVATE", ApprovalID: act, EvidenceRefs: evidence()}, 200)
	var periodic int
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM rent_obligations WHERE subject_id=$1`, ten.ID).Scan(&periodic); err != nil || periodic != 12 {
		t.Fatalf("periodic schedule %d %v", periodic, err)
	}
	cl := opsRec(c, c.owner, "POST", "/closure-cases", businesshttp.OpsRecordCreate{Kind: "VOLUNTARY_CLOSURE", Title: "Wind down", Reason: "Owners retiring"}, 201)
	c.must(c.owner, "POST", "/closure-cases", businesshttp.OpsRecordCreate{Kind: "VOLUNTARY_CLOSURE", Title: "Second closure", Reason: "Duplicate"}, 409)
	cl = opsMove(c, c.owner, "/closure-cases", cl, businesshttp.OpsRecordTransition{Action: "START_REVIEW"}, 200)
	clApproval := c.approvalAt("COMMERCIAL_BUSINESS_CLOSURE", "organisation:verify", "", nil, "", map[string]any{"domain": "closure_case", "record_id": cl.ID, "action": "APPROVE", "version": cl.Version}, "COMPANY", org)
	opsMove(c, c.owner, "/closure-cases", cl, businesshttp.OpsRecordTransition{Action: "APPROVE", ApprovalID: clApproval}, 409)
	ten = opsMove(c, c.owner, "/tenancies", ten, businesshttp.OpsRecordTransition{Action: "TERMINATE", EvidenceRefs: evidence()}, 200)
	cl = opsMove(c, c.owner, "/closure-cases", cl, businesshttp.OpsRecordTransition{Action: "APPROVE", ApprovalID: clApproval}, 200)
	opsMove(c, c.owner, "/closure-cases", cl, businesshttp.OpsRecordTransition{Action: "ENTER_RETENTION", Attributes: map[string]any{"retain_until": time.Now().UTC().AddDate(0, 6, 0).Format(time.RFC3339)}}, 400)
	cl = opsMove(c, c.owner, "/closure-cases", cl, businesshttp.OpsRecordTransition{Action: "ENTER_RETENTION", Attributes: map[string]any{"retain_until": time.Now().UTC().AddDate(7, 0, 0).Format(time.RFC3339)}}, 200)
	var state string
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM organisations WHERE id=$1`, org).Scan(&state); err != nil || state != "CLOSURE_PENDING" {
		t.Fatalf("organisation %s %v", state, err)
	}
	opsMove(c, c.owner, "/closure-cases", cl, businesshttp.OpsRecordTransition{Action: "CLOSE"}, 403)
}

// Journeys 26-28 supplements: source-backed risk signals that never
// restrict on their own, protection cases, incidents, branch incidents and
// operator-confirmed content suggestions.
func TestAnnexDRiskSignalsProtectionIncidentsAndContentSuggestions(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	c.must(c.owner, "POST", "/risk/signals", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "VELOCITY", Title: "Unsourced", Reason: "No source"}, 400)
	c.must(c.owner, "POST", "/risk/signals", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "VELOCITY", Title: "Protected", Attributes: map[string]any{"source_service": "payment-service", "source_reference": "pay-sig-1", "religion": "x"}, Reason: "Protected"}, 400)
	sig := opsRec(c, c.owner, "POST", "/risk/signals", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "PAYMENT_ANOMALY", Title: "Card velocity", Attributes: map[string]any{"source_service": "payment-service", "source_reference": "pay-sig-1"}, Reason: "Payment signal"}, 201)
	var restricted int
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM risk_cases WHERE organisation_id=$1 AND state='RESTRICTED'`, c.owner.registered.OrganisationID).Scan(&restricted); err != nil || restricted != 0 {
		t.Fatal("signal produced a restriction")
	}
	rc := opsRec(c, c.owner, "POST", "/risk-cases", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "RESERVATION_RISK", Title: "Review reservation risk", Reason: "Manual review"}, 201)
	sig = opsMove(c, c.owner, "/risk/signals", sig, businesshttp.OpsRecordTransition{Action: "LINK_CASE", ExternalReference: rc.ID.String()}, 200)
	if sig.State != "LINKED" {
		t.Fatalf("sig state %s", sig.State)
	}
	pc := opsRec(c, c.owner, "POST", "/protection-cases", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, ParentID: &rc.ID, Kind: "DAMAGE_PROTECTION", Title: "Damage protection", CounterpartyReference: "insurer:alpha", Reason: "Protection request"}, 201)
	pc = opsMove(c, c.owner, "/protection-cases", pc, businesshttp.OpsRecordTransition{Action: "RECORD_OPENED", ExternalReference: "insurer-case-1"}, 200)
	inc := opsRec(c, c.owner, "POST", "/incidents", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "PROPERTY_DAMAGE", Title: "Broken window", Reason: "Housekeeping report"}, 201)
	inc = opsMove(c, c.owner, "/incidents", inc, businesshttp.OpsRecordTransition{Action: "INVESTIGATE", AssigneeID: &c.owner.registered.AccountID}, 200)
	inc = opsMove(c, c.owner, "/incidents", inc, businesshttp.OpsRecordTransition{Action: "RESOLVE", EvidenceRefs: evidence()}, 200)
	opsMove(c, c.owner, "/incidents", inc, businesshttp.OpsRecordTransition{Action: "CLOSE"}, 403)
	opsMove(c, c.approver, "/incidents", inc, businesshttp.OpsRecordTransition{Action: "CLOSE"}, 200)
	started := time.Now().UTC().Add(-2 * time.Hour)
	bi := opsRec(c, c.owner, "POST", "/branch-incidents", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "CONNECTIVITY_LOSS", Title: "Front desk offline", StartsAt: &started, Reason: "ISP outage"}, 201)
	bi = opsMove(c, c.owner, "/branch-incidents", bi, businesshttp.OpsRecordTransition{Action: "RESOLVE", EvidenceRefs: evidence()}, 200)
	opsMove(c, c.approver, "/branch-incidents", bi, businesshttp.OpsRecordTransition{Action: "CLOSE"}, 200)
	c.must(c.owner, "POST", "/content-suggestions", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "DESCRIPTION", Title: "Unsourced", Attributes: map[string]any{"source_kind": "MEDIA_EXTRACTION", "suggested_text": "Sea view"}, Reason: "No evidence"}, 400)
	cs := opsRec(c, c.owner, "POST", "/content-suggestions", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "AMENITY", Title: "Balcony detected", EvidenceRefs: evidence(), Attributes: map[string]any{"source_kind": "MEDIA_EXTRACTION", "suggested_text": "Private balcony"}, Reason: "Photo extraction"}, 201)
	opsMove(c, c.owner, "/content-suggestions", cs, businesshttp.OpsRecordTransition{Action: "ACCEPT"}, 403)
	opsMove(c, c.approver, "/content-suggestions", cs, businesshttp.OpsRecordTransition{Action: "ACCEPT_EDITED"}, 400)
	cs = opsMove(c, c.approver, "/content-suggestions", cs, businesshttp.OpsRecordTransition{Action: "ACCEPT_EDITED", Attributes: map[string]any{"edited_text": "Private balcony with garden view"}}, 200)
	cs = opsMove(c, c.owner, "/content-suggestions", cs, businesshttp.OpsRecordTransition{Action: "PUBLISH", ExternalReference: ids.New().String()}, 200)
	if cs.State != "PUBLISHED" || pc.State != "OPEN" {
		t.Fatalf("content %s protection %s", cs.State, pc.State)
	}
}

// D.14/D.15/D.17/D.23 hospitality and analytics supplements.
func TestAnnexDHospitalityChecklistsLostFoundLinenSettingsServicesExperiments(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	c.must(c.owner, "POST", "/housekeeping/checklists", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "TURNOVER", Title: "Empty", Attributes: map[string]any{"items": []string{}}, Reason: "Empty"}, 400)
	cl := opsRec(c, c.owner, "POST", "/housekeeping/checklists", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "TURNOVER", Title: "Turnover standard", Attributes: map[string]any{"items": []string{"Linen changed", "Bathroom sanitised", "Minibar restocked"}}, Reason: "SOP"}, 201)
	opsRec(c, c.owner, "POST", "/housekeeping/tasks", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "TURNOVER", Title: "Inactive checklist", Attributes: map[string]any{"checklist_id": cl.ID.String()}, Reason: "Draft checklist"}, 409)
	cl = opsMove(c, c.owner, "/housekeeping/checklists", cl, businesshttp.OpsRecordTransition{Action: "ACTIVATE"}, 200)
	task := opsRec(c, c.owner, "POST", "/housekeeping/tasks", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "TURNOVER", Title: "Turnover 101", Attributes: map[string]any{"checklist_id": cl.ID.String()}, Reason: "Departure"}, 201)
	task = opsMove(c, c.owner, "/housekeeping/tasks", task, businesshttp.OpsRecordTransition{Action: "ASSIGN", AssigneeID: &c.owner.registered.AccountID}, 200)
	task = opsMove(c, c.owner, "/housekeeping/tasks", task, businesshttp.OpsRecordTransition{Action: "START"}, 200)
	opsMove(c, c.owner, "/housekeeping/tasks", task, businesshttp.OpsRecordTransition{Action: "SUBMIT_INSPECTION", EvidenceRefs: evidence(), Attributes: map[string]any{"checklist_results": map[string]any{"Linen changed": true}}}, 400)
	opsMove(c, c.owner, "/housekeeping/tasks", task, businesshttp.OpsRecordTransition{Action: "SUBMIT_INSPECTION", EvidenceRefs: evidence(), Attributes: map[string]any{"checklist_results": map[string]any{"Linen changed": true, "Bathroom sanitised": true, "Minibar restocked": false}}}, 200)
	item := opsRec(c, c.owner, "POST", "/lost-found", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "ELECTRONICS", Title: "Phone charger", Attributes: map[string]any{"found_location": "Room 101"}, Reason: "Found at turnover"}, 201)
	item = opsMove(c, c.owner, "/lost-found", item, businesshttp.OpsRecordTransition{Action: "STORE", EvidenceRefs: evidence()}, 200)
	opsMove(c, c.approver, "/lost-found", item, businesshttp.OpsRecordTransition{Action: "DISPOSE", EvidenceRefs: evidence()}, 409)
	item = opsMove(c, c.owner, "/lost-found", item, businesshttp.OpsRecordTransition{Action: "RECORD_CLAIM", ExternalReference: "guest:stay-1"}, 200)
	opsMove(c, c.approver, "/lost-found", item, businesshttp.OpsRecordTransition{Action: "RECORD_RETURN", EvidenceRefs: evidence()}, 200)
	ln := opsRec(c, c.owner, "POST", "/linen-stock", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "TOWELS", Title: "Towels 101", Attributes: map[string]any{"par_level": 8, "on_hand": 10}, Reason: "Par setup"}, 201)
	ln = opsMove(c, c.owner, "/linen-stock", ln, businesshttp.OpsRecordTransition{Action: "RECORD_COUNT", EvidenceRefs: evidence(), Attributes: map[string]any{"on_hand": 5}}, 200)
	if attrMap(t, ln)["below_par"] != true {
		t.Fatal("below par not computed")
	}
	s1 := opsRec(c, c.owner, "POST", "/shortlet/settings", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "HOUSE_RULES", Title: "House rules v1", Attributes: map[string]any{"check_in_time": "14:00", "check_out_time": "11:00", "max_guests": 4, "min_nights": 2}, Reason: "Setup"}, 201)
	s1 = opsMove(c, c.owner, "/shortlet/settings", s1, businesshttp.OpsRecordTransition{Action: "ACTIVATE"}, 200)
	s2 := opsRec(c, c.owner, "POST", "/shortlet/settings", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "HOUSE_RULES", Title: "House rules v2", Attributes: map[string]any{"check_in_time": "15:00", "check_out_time": "11:00", "max_guests": 4, "min_nights": 1}, Reason: "Update"}, 201)
	opsMove(c, c.owner, "/shortlet/settings", s2, businesshttp.OpsRecordTransition{Action: "ACTIVATE"}, 200)
	if opsState(c, "shortlet_settings", s1.ID) != "SUPERSEDED" {
		t.Fatal("settings not superseded")
	}
	price := int64(1500000)
	c.must(c.owner, "POST", "/hotel/services", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "SPA", Title: "Massage", AmountMinor: &price, Currency: "NGN", Reason: "No unit"}, 400)
	svc := opsRec(c, c.owner, "POST", "/hotel/services", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "SPA", Title: "60 minute massage", AmountMinor: &price, Currency: "NGN", Attributes: map[string]any{"pricing_unit": "PER_PERSON"}, Reason: "Service catalogue"}, 201)
	opsMove(c, c.owner, "/hotel/services", svc, businesshttp.OpsRecordTransition{Action: "ACTIVATE"}, 200)
	ends := time.Now().UTC().Add(30 * 24 * time.Hour)
	ex := opsRec(c, c.owner, "POST", "/revenue/experiments", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "PACKAGE", Title: "Breakfast bundle test", EndsAt: &ends, Attributes: map[string]any{"hypothesis": "Bundled breakfast lifts conversion", "primary_metric": "conversion_rate", "variants": []string{"control", "bundle"}}, Reason: "Commercial test"}, 201)
	opsMove(c, c.owner, "/revenue/experiments", ex, businesshttp.OpsRecordTransition{Action: "START"}, 403)
	opsMove(c, c.approver, "/revenue/experiments", ex, businesshttp.OpsRecordTransition{Action: "START"}, 200)
	from := time.Now().UTC().Add(-time.Hour)
	until := from.Add(30 * 24 * time.Hour)
	c.must(c.owner, "POST", "/market-benchmarks", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "ADR", Title: "Unlicensed", StartsAt: &from, EndsAt: &until, Attributes: map[string]any{"source_reference": "str:lagos", "value": 9500000}, Reason: "No licence"}, 400)
	mb := opsRec(c, c.owner, "POST", "/market-benchmarks", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "ADR", Title: "Lagos ADR comp set", StartsAt: &from, EndsAt: &until, Attributes: map[string]any{"source_reference": "str:lagos:2026-10", "licence_reference": "licence:str-2026", "value": 9500000}, Reason: "Licensed benchmark"}, 201)
	opsMove(c, c.approver, "/market-benchmarks", mb, businesshttp.OpsRecordTransition{Action: "ACTIVATE"}, 200)
}

// Equivalence proofs for catalogue entries served by existing models:
// dispute_evidence_refs (disputes.evidence_refs + immutable history),
// integration_health (derived health projection without database access)
// and accounting_connection_refs (integration_clients kind ACCOUNTING).
func TestAnnexDEquivalenceDisputeEvidenceIntegrationHealthAccounting(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	d := opsRec(c, c.owner, "POST", "/disputes", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "DEPOSIT", Title: "Deposit deduction dispute", CounterpartyReference: "tenant:ngozi", Reason: "Tenant disputes deduction"}, 201)
	d = opsMove(c, c.owner, "/disputes", d, businesshttp.OpsRecordTransition{Action: "COLLECT_EVIDENCE"}, 200)
	first, second := ids.New(), ids.New()
	opsMove(c, c.owner, "/disputes", d, businesshttp.OpsRecordTransition{Action: "SUBMIT", ExternalReference: "admin-dispute-1"}, 400)
	d = opsMove(c, c.owner, "/disputes", d, businesshttp.OpsRecordTransition{Action: "SUBMIT", ExternalReference: "admin-dispute-1", EvidenceRefs: []ids.ID{first, second}}, 200)
	var refs []ids.ID
	if err := json.Unmarshal(d.EvidenceRefs, &refs); err != nil || len(refs) != 2 || refs[0] != first {
		t.Fatalf("dispute evidence refs %v %v", refs, err)
	}
	if _, err := c.db.Pool.Exec(t.Context(), `UPDATE disputes SET evidence_refs='[]',version=version+1,state='OPEN' WHERE id=$1`, d.ID); err == nil {
		t.Fatal("dispute evidence rewritten")
	}
	ic := opsRec(c, c.owner, "POST", "/integrations", businesshttp.OpsRecordCreate{Kind: "ACCOUNTING", Title: "Accounting connection", CounterpartyReference: "partner:books", Attributes: map[string]any{"scopes": []string{"finance:read"}, "credential_reference": smRef("books"), "rate_limit_per_minute": 60}, Reason: "Accounting connection reference"}, 201)
	health := commercialData[map[string]any](t, c.must(c.owner, "GET", "/integrations/"+ic.ID.String()+"/health", nil, 200))
	if health["status"] != "NOT_ACTIVATED" || health["database_access"] != false {
		t.Fatalf("health %v", health)
	}
	opsMove(c, c.owner, "/integrations", ic, businesshttp.OpsRecordTransition{Action: "ACTIVATE", EvidenceRefs: evidence()}, 200)
	health = commercialData[map[string]any](t, c.must(c.owner, "GET", "/integrations/"+ic.ID.String()+"/health", nil, 200))
	if health["status"] != "HEALTHY" {
		t.Fatalf("health %v", health)
	}
}

// smRef names a secret-manager entry (a reference, never a secret value).
func smRef(path string) string { return "secrets/partners/" + path }

// fixtureFingerprint is a deterministic 64-hex SHA-256-shaped fixture.
func fixtureFingerprint(seed string) string {
	sum := sha256.Sum256([]byte("annex-d-fixture-" + seed))
	return hex.EncodeToString(sum[:])
}
