package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// folioPaymentFake is an explicit disposable Payment Service contract double
// that keeps one locked quote, intent and receipt per payer obligation. Every
// success, settlement, failure and reversal is controlled by the test.
type folioPaymentFake struct {
	mu              sync.Mutex
	policy          payment.Policy
	quotes          map[ids.ID]payment.Quote
	intents         map[ids.ID]payment.Intent
	byQuote         map[ids.ID]ids.ID
	funded          map[ids.ID]bool
	reversed        map[ids.ID]int64
	failNext        bool
	collectionCalls int
}

func newFolioPaymentFake(policy payment.Policy) *folioPaymentFake {
	return &folioPaymentFake{policy: policy, quotes: map[ids.ID]payment.Quote{}, intents: map[ids.ID]payment.Intent{}, byQuote: map[ids.ID]ids.ID{}, funded: map[ids.ID]bool{}, reversed: map[ids.ID]int64{}}
}
func (f *folioPaymentFake) Policy(context.Context, ids.ID) (payment.Policy, error) {
	return f.policy, nil
}
func (f *folioPaymentFake) Lock(_ context.Context, q payment.Quote, _ string) (payment.Quote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if old, ok := f.quotes[q.ID]; ok && old.AmountMinor != q.AmountMinor {
		return payment.Quote{}, errors.New("locked quote changed")
	}
	f.quotes[q.ID] = q
	return q, nil
}
func (f *folioPaymentFake) Collect(_ context.Context, in payment.IntentInput, _ string) (payment.Intent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.collectionCalls++
	q, ok := f.quotes[*in.LockedQuoteID]
	if !ok || in.AmountMinor != q.AmountMinor {
		return payment.Intent{}, errors.New("locked quote/context mismatch")
	}
	if id, ok := f.byQuote[q.ID]; ok {
		return f.intents[id], nil
	}
	state := "VERIFIED_SUCCESS"
	if f.failNext {
		state, f.failNext = "VERIFIED_FAILURE", false
	}
	intent := payment.Intent{ID: ids.New(), WalletID: in.WalletID, AmountMinor: in.AmountMinor, Currency: in.Currency, State: state, SettlementState: "PENDING", Reference: "disposable-folio-provider-reference"}
	f.intents[intent.ID], f.byQuote[q.ID] = intent, intent.ID
	return intent, nil
}
func (f *folioPaymentFake) Intent(_ context.Context, id ids.ID) (payment.Intent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	intent, ok := f.intents[id]
	if !ok {
		return payment.Intent{}, errors.New("disposable intent unavailable")
	}
	return intent, nil
}
func (f *folioPaymentFake) Receipt(_ context.Context, id ids.ID) (payment.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q, ok := f.quotes[id]
	if !ok {
		return payment.Receipt{}, errors.New("disposable receipt unavailable")
	}
	r := payment.Receipt{FundingConfirmed: f.funded[id], RemedyVersion: 1, Quote: q, CollectionState: "NOT_INITIATED", SettlementState: "PENDING", ReversedMinor: f.reversed[id]}
	r.PayerContext.WalletID = q.PayerWalletID
	r.PayerContext.CanonicalPartyRef = q.LegalPartyRef
	if intentID, ok := f.byQuote[id]; ok {
		intent := f.intents[intentID]
		r.IntentID = &intent.ID
		r.CollectionState = intent.State
		r.SettlementState = intent.SettlementState
	}
	return r, nil
}

// settle marks the payer's Payment-owned obligation as funded and settled.
func (f *folioPaymentFake) settle(groupID ids.ID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for qid, q := range f.quotes {
		if q.BusinessRef == groupID.String() {
			intent := f.intents[f.byQuote[qid]]
			intent.SettlementState = "SETTLED"
			f.intents[intent.ID] = intent
			f.funded[qid] = true
		}
	}
}
func (f *folioPaymentFake) reverse(groupID ids.ID, amount int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for qid, q := range f.quotes {
		if q.BusinessRef == groupID.String() {
			f.reversed[qid] = amount
		}
	}
}
func (f *folioPaymentFake) quoteFor(groupID ids.ID) (payment.Quote, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, q := range f.quotes {
		if q.BusinessRef == groupID.String() {
			return q, true
		}
	}
	return payment.Quote{}, false
}

type pmsFolioRead struct {
	Folio        hospitality.Folio  `json:"folio"`
	Items        []hospitality.Item `json:"items"`
	BalanceMinor int64              `json:"balance_minor"`
}

type pmsCollectionFixture struct {
	c       *commercialHarness
	pay     *folioPaymentFake
	guest   crmRegisteredGuest
	v       reservations.Detail
	folioID ids.ID
	stayID  ids.ID
}

func newPMSCollectionFixture(t *testing.T) *pmsCollectionFixture {
	c := newCommercialHTTPForArrival(t, 0)
	c.approver = c.user()
	c.secure(c.approver)
	c.join(c.approver, c.owner, []string{"approvals:decide", "reservations:approve", "pms:approve", "folios:approve", "pricing:approve"})
	v, g := pmsConfirmed(c)
	pay := newFolioPaymentFake(reservationPaymentProvider(c).policy)
	c.domain.Payment = pay
	status := pmsReady(c)
	a := pmsAssign(c, v, status)
	in := businesshttp.PMSCheckInCommand{AssignmentID: a.ID, AssignmentVersion: a.Version, ReservationVersion: v.Reservation.Version, RoomStatusVersion: status.Version, GuestID: g.Guest.ID, RegistrationReference: "actual-collection-front-desk-registration", Reason: "Check in the registered guest before posting actual ancillary services"}
	arrival, e := hospitality.LocalInstant(a.DateFrom, v.Reservation.PolicySnapshot.CheckInLocal, v.Reservation.PolicySnapshot.AcceptanceTimezone)
	if e != nil {
		t.Fatal(e)
	}
	if time.Now().Before(arrival) {
		op := in
		in.ApprovalID = c.approval("COMMERCIAL_PMS_EARLY_CHECK_IN", "pms:approve", "", nil, "", map[string]any{"command": op})
	}
	opened := commercialData[struct {
		StayID  ids.ID `json:"stay_id"`
		FolioID ids.ID `json:"folio_id"`
	}](t, c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/check-in", in, 201))
	return &pmsCollectionFixture{c: c, pay: pay, guest: g, v: v, folioID: opened.FolioID, stayID: opened.StayID}
}

func (x *pmsCollectionFixture) folio() pmsFolioRead {
	return commercialData[pmsFolioRead](x.c.t, x.c.must(x.c.owner, "GET", "/folios/"+x.folioID.String(), nil, 200))
}
func (x *pmsCollectionFixture) post(kind, source string, amount int64) ids.ID {
	f := x.folio()
	in := businesshttp.PMSFolioPosting{Version: f.Folio.Version, Kind: kind, SourceReference: source, Quantity: 1, AmountMinor: amount, Currency: f.Folio.Currency, PolicyReference: "accepted-ancillary-service-policy-v1", Reason: "Post the actual consumed ancillary service with its immutable source"}
	return commercialData[struct {
		ItemID ids.ID `json:"item_id"`
	}](x.c.t, x.c.must(x.c.owner, "POST", "/folios/"+x.folioID.String()+"/items", in, 201)).ItemID
}
func (x *pmsCollectionFixture) agreement(payer string, claims ...hospitality.FolioCollectionClaim) businesshttp.PMSCollectionAgreementCommand {
	return businesshttp.PMSCollectionAgreementCommand{FolioVersion: x.folio().Folio.Version, PayerType: payer, Allocations: claims, PaymentPolicyID: x.pay.policy.ID, PaymentPolicyVersion: x.pay.policy.Version, PaymentPolicyFamily: "HOTEL", PayerWalletID: ids.New(), ConsentReference: "explicit-payer-consent-reference", LegalPartyReference: "canonical-payer-party-" + payer, Email: "disposable-payer@example.invalid", Schedule: "ONE_TIME", Reason: "Assign the exact accepted ancillary obligation to its consenting payer"}
}

type pmsAgreementQueued struct {
	ID               ids.ID `json:"id"`
	FinancialGroupID ids.ID `json:"financial_group_id"`
	RequestID        ids.ID `json:"request_id"`
	AmountMinor      int64  `json:"amount_minor"`
}
type pmsAgreementList struct {
	Items          []businesshttp.PMSCollectionAgreement `json:"items"`
	Responsibility businesshttp.PMSFolioResponsibility   `json:"responsibility"`
}

func (x *pmsCollectionFixture) agreements() pmsAgreementList {
	return commercialData[pmsAgreementList](x.c.t, x.c.must(x.c.owner, "GET", "/folios/"+x.folioID.String()+"/collection-agreements", nil, 200))
}
func (x *pmsCollectionFixture) process(t *testing.T) {
	if e := x.c.domain.ProcessReservationFinance(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
}
func (x *pmsCollectionFixture) readback(t *testing.T, agreement ids.ID) {
	x.c.must(x.c.owner, "POST", "/folios/"+x.folioID.String()+"/collection-agreements/"+agreement.String()+"/readback", businesshttp.PMSCollectionReadbackCommand{FolioVersion: x.folio().Folio.Version, Reason: "Read the original owning Payment receipt after provider settlement"}, 202)
	x.process(t)
}
func (x *pmsCollectionFixture) reconcile(t *testing.T, status int) {
	x.c.must(x.c.owner, "POST", "/folios/"+x.folioID.String()+"/reconcile", businesshttp.PMSVersionCommand{Version: x.folio().Folio.Version, Reason: "Reconcile the folio against current authoritative funded settlements only"}, status)
}
func (x *pmsCollectionFixture) corporate(t *testing.T) (crm.Corporate, crm.CorporateContract) {
	c := x.c
	company := commercialData[crm.Corporate](t, c.must(c.owner, "POST", "/corporate-customers", businesshttp.CRMCorporateCreate{PropertyID: c.property.ID, LegalName: "Disposable incidentals company", RegistrationReference: "corp-register:" + ids.New().String(), Contacts: []crm.CorporateContact{{Name: "Travel officer", Email: "travel@example.invalid", RepresentativeReference: "company-designation-proof"}}, Reason: "Register a canonical corporate customer with protected contacts"}, 201))
	now := time.Now().UTC()
	terms := crm.CorporateTerms{RatePlanIDs: []ids.ID{c.rate.ID}, AllowedSources: []string{"CORPORATE"}, BillingResponsibility: "PAY_AT_PROPERTY", BillingReference: "approved-corporate-incidentals-billing", MaxBookingMinor: 100000, Currency: "NGN", RequireNamedTravelers: true, MaxNights: 30}
	cmd := businesshttp.CRMCorporateContractCommand{State: "ACTIVE", Definition: terms, EffectiveAt: now.Add(-time.Minute), ExpiresAt: now.AddDate(0, 1, 0), Reason: "Independent negotiated corporate incidentals billing limits"}
	op := cmd
	cmd.ApprovalID = c.approval("COMMERCIAL_CORPORATE_CONTRACT", "reservations:approve", "", &terms.MaxBookingMinor, terms.Currency, map[string]any{"corporate_id": company.ID, "property_id": c.property.ID, "command": op})
	contract := commercialData[crm.CorporateContract](t, c.must(c.owner, "POST", "/corporate-customers/"+company.ID.String()+"/contracts", cmd, 201))
	traveler := businesshttp.CRMTravelerCommand{GuestID: x.guest.Guest.ID, ContractID: contract.ID, State: "ACTIVE", Policy: terms, EffectiveAt: contract.EffectiveAt, ExpiresAt: contract.ExpiresAt, Reason: "Independently approve the actual registered in-house traveler"}
	travelOp := traveler
	traveler.ApprovalID = c.approval("COMMERCIAL_CORPORATE_TRAVELER", "reservations:approve", "", &terms.MaxBookingMinor, terms.Currency, map[string]any{"corporate_id": company.ID, "property_id": c.property.ID, "command": travelOp})
	c.must(c.owner, "POST", "/corporate-customers/"+company.ID.String()+"/travelers", traveler, 200)
	return company, contract
}

// Guest pays meals plus part of the minibar; the company pays the rest of the
// minibar and laundry. Accommodation stays with its reservation obligation.
func TestBusinessPMSFolioSplitPayerCollectionConservesObligations(t *testing.T) {
	x := newPMSCollectionFixture(t)
	c := x.c
	meals := x.post("MEALS", "restaurant-order-collection-1", 5000)
	minibar := x.post("MINIBAR", "minibar-count-collection-1", 3000)
	laundry := x.post("LAUNDRY", "laundry-ticket-collection-1", 2000)
	base := x.folio()
	var accommodation ids.ID
	for _, item := range base.Items {
		if item.Kind == "ACCOMMODATION" {
			accommodation = item.ID
		}
	}
	path := "/folios/" + x.folioID.String() + "/collection-agreements"

	// Accommodation is never collected again through a folio agreement.
	c.must(c.owner, "POST", path, x.agreement("GUEST", hospitality.FolioCollectionClaim{ItemID: accommodation, AmountMinor: 100}), 403)
	guest := commercialData[pmsAgreementQueued](t, c.must(c.owner, "POST", path, x.agreement("GUEST", hospitality.FolioCollectionClaim{ItemID: meals, AmountMinor: 5000}, hospitality.FolioCollectionClaim{ItemID: minibar, AmountMinor: 1000}), 202))
	if guest.AmountMinor != 6000 {
		t.Fatalf("guest agreement %+v", guest)
	}

	company, contract := x.corporate(t)
	corp := x.agreement("CORPORATE_BOOKER", hospitality.FolioCollectionClaim{ItemID: minibar, AmountMinor: 2000}, hospitality.FolioCollectionClaim{ItemID: laundry, AmountMinor: 2000})
	corp.CorporateCustomerID, corp.CorporateContractID = &company.ID, &contract.ID
	c.must(c.owner, "POST", path, corp, 403) // corporate liability needs an exact independent approval
	op := corp
	amount := int64(4000)
	corp.ApprovalID = c.approval("COMMERCIAL_PMS_FOLIO_COLLECTION", "folios:approve", "", &amount, "NGN", map[string]any{"folio_id": x.folioID, "command": op})
	corporate := commercialData[pmsAgreementQueued](t, c.must(c.owner, "POST", path, corp, 202))

	// The original obligation is exhausted: nothing more can be allocated.
	c.must(c.owner, "POST", path, x.agreement("GUEST", hospitality.FolioCollectionClaim{ItemID: minibar, AmountMinor: 1}), 409)
	list := x.agreements()
	r := list.Responsibility
	if len(list.Items) != 2 || r.AllocatedGuestMinor != 6000 || r.AllocatedCorporateMinor != 4000 || r.UnallocatedMinor != 0 || !r.Conserved || !r.MandatoryPayerUnverified || r.FolioTotalMinor != r.AccommodationMinor+10000 {
		t.Fatalf("responsibility %+v", r)
	}

	x.process(t)
	x.process(t) // a repeated worker pass reads back; it never collects twice
	if x.pay.collectionCalls != 2 {
		t.Fatalf("collection calls %d", x.pay.collectionCalls)
	}
	gq, ok := x.pay.quoteFor(guest.FinancialGroupID)
	if !ok || gq.AmountMinor != 6000 || len(gq.Components) != 2 {
		t.Fatalf("guest quote %+v", gq)
	}
	for _, component := range gq.Components {
		if component.Kind == "ROOM" || component.Kind == "ACCOMMODATION" {
			t.Fatal("accommodation charged again through the folio")
		}
	}
	if q, _ := x.pay.quoteFor(corporate.FinancialGroupID); q.LegalPartyRef != "canonical-payer-party-CORPORATE_BOOKER" || q.AmountMinor != 4000 {
		t.Fatalf("corporate quote %+v", q)
	}
	// Collected but unsettled money never reduces the folio.
	x.reconcile(t, 409)

	x.pay.settle(guest.FinancialGroupID)
	x.readback(t, guest.ID)
	x.reconcile(t, 200)
	x.reconcile(t, 200)
	var refs int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM folio_payment_references WHERE folio_id=$1 AND financial_group_id=$2`, x.folioID, guest.FinancialGroupID).Scan(&refs); e != nil || refs != 1 {
		t.Fatalf("guest receipt applied %d times %v", refs, e)
	}
	accommodationMinor := r.AccommodationMinor
	f := x.folio()
	if f.BalanceMinor != accommodationMinor+4000 || f.Folio.State != "SETTLEMENT_PENDING" {
		t.Fatalf("partial settlement balance %d state %s", f.BalanceMinor, f.Folio.State)
	}
	list = x.agreements()
	if list.Items[0].CollectionState != "APPLIED" || list.Items[1].CollectionState != "COLLECTED_SETTLEMENT_PENDING" || !list.Responsibility.MandatoryPayerUnverified {
		t.Fatalf("payer states %+v", list.Items)
	}

	x.pay.settle(corporate.FinancialGroupID)
	x.readback(t, corporate.ID)
	x.reconcile(t, 200)
	f = x.folio()
	// Accommodation remains a pay-at-property guarantee, not collected money.
	if f.BalanceMinor != accommodationMinor || f.Folio.State != "SETTLEMENT_PENDING" {
		t.Fatalf("unverified accommodation hidden: balance %d state %s", f.BalanceMinor, f.Folio.State)
	}
	list = x.agreements()
	if list.Responsibility.MandatoryPayerUnverified || list.Responsibility.AppliedReceiptsMinor != 10000 || !list.Responsibility.Conserved {
		t.Fatalf("settled responsibility %+v", list.Responsibility)
	}

	// A dispatched collection can never be released as "never dispatched".
	c.must(c.owner, "POST", path+"/"+guest.ID.String()+"/release", businesshttp.PMSCollectionReleaseCommand{FolioVersion: x.folio().Folio.Version, Disposition: "NEVER_DISPATCHED", Reason: "Attempt to release an allocation whose money already moved"}, 409)

	// A later Payment reversal removes backing immediately without rewriting history.
	x.pay.reverse(guest.FinancialGroupID, 6000)
	x.readback(t, guest.ID)
	if f = x.folio(); f.BalanceMinor != accommodationMinor+6000 {
		t.Fatalf("reversal concealed guest debt %d", f.BalanceMinor)
	}
	if list = x.agreements(); list.Items[0].CollectionState != "REVERSAL_REVIEW_REQUIRED" {
		t.Fatalf("reversed agreement %+v", list.Items[0])
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE folio_collection_allocations SET amount_minor=1 WHERE agreement_id=$1`, guest.ID); e == nil {
		t.Fatal("accepted allocations were mutable")
	}
	if _, e := c.db.Pool.Exec(t.Context(), `DELETE FROM folio_collection_agreements WHERE id=$1`, corporate.ID); e == nil {
		t.Fatal("agreement evidence deleted")
	}
}

// A never-dispatched agreement can be released and replaced once; an
// authoritatively failed collection returns its allocation for another payer.
func TestBusinessPMSFolioCollectionReleaseReplacementAndVerifiedFailure(t *testing.T) {
	x := newPMSCollectionFixture(t)
	c := x.c
	transfer := x.post("TRANSFER", "airport-transfer-collection-1", 1500)
	path := "/folios/" + x.folioID.String() + "/collection-agreements"
	first := commercialData[pmsAgreementQueued](t, c.must(c.owner, "POST", path, x.agreement("GUEST", hospitality.FolioCollectionClaim{ItemID: transfer, AmountMinor: 1500}), 202))
	release := businesshttp.PMSCollectionReleaseCommand{FolioVersion: x.folio().Folio.Version, Disposition: "VERIFIED_FAILURE", Reason: "Attempt a failure release without authoritative Payment failure"}
	c.must(c.owner, "POST", path+"/"+first.ID.String()+"/release", release, 409)
	release.Disposition = "NEVER_DISPATCHED"
	release.Reason = "Release the never-dispatched payer allocation before collection"
	c.must(c.owner, "POST", path+"/"+first.ID.String()+"/release", release, 200)
	x.process(t)
	if x.pay.collectionCalls != 0 {
		t.Fatal("released agreement reached Payment")
	}
	var state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM reservation_financial_requests WHERE id=$1`, first.RequestID).Scan(&state); e != nil || state != "DEPENDENCY_BLOCKED" {
		t.Fatalf("released request %s %v", state, e)
	}

	replacement := x.agreement("GUEST", hospitality.FolioCollectionClaim{ItemID: transfer, AmountMinor: 1500})
	replacement.ReplacesAgreementID = &first.ID
	second := commercialData[pmsAgreementQueued](t, c.must(c.owner, "POST", path, replacement, 202))
	again := x.agreement("GUEST", hospitality.FolioCollectionClaim{ItemID: transfer, AmountMinor: 1})
	again.ReplacesAgreementID = &first.ID
	c.must(c.owner, "POST", path, again, 409)

	x.pay.failNext = true
	x.process(t)
	if list := x.agreements(); list.Items[1].CollectionState != "FAILED" || list.Responsibility.UnallocatedMinor != 0 {
		t.Fatalf("failed collection %+v", list)
	}
	x.reconcile(t, 409)
	release = businesshttp.PMSCollectionReleaseCommand{FolioVersion: x.folio().Folio.Version, Disposition: "VERIFIED_FAILURE", Reason: "Release the allocation after authoritative Payment failure"}
	c.must(c.owner, "POST", path+"/"+second.ID.String()+"/release", release, 200)
	list := x.agreements()
	if list.Responsibility.UnallocatedMinor != 1500 || !list.Responsibility.Conserved || list.Items[1].CollectionState != "RELEASED" {
		t.Fatalf("released failure %+v", list.Responsibility)
	}
	// The released obligation can be assigned again; the failed history remains.
	c.must(c.owner, "POST", path, x.agreement("GUEST", hospitality.FolioCollectionClaim{ItemID: transfer, AmountMinor: 1500}), 202)
	var n int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM folio_collection_agreements WHERE folio_id=$1`, x.folioID).Scan(&n); e != nil || n != 3 {
		t.Fatalf("agreement history %d %v", n, e)
	}
}
