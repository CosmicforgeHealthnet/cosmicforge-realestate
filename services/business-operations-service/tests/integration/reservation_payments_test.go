package integration

import (
	"context"
	"errors"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"strings"
	"sync"
	"testing"
	"time"
)

// Explicit disposable owning-service contract fixture. This is not live Payment
// or provider certification; every success/reversal is controlled by the test.
type reservationPaymentFake struct {
	mu                                sync.Mutex
	policy                            payment.Policy
	quote                             payment.Quote
	intent                            payment.Intent
	reversed                          int64
	remedyReviewRequired              bool
	receiptUnavailableAfterCollection bool
	unavailable                       bool
	loseCollectionResponse            bool
	collectionCalls                   int
	forgedReceipt                     bool
}

func (f *reservationPaymentFake) Policy(context.Context, ids.ID) (payment.Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		return payment.Policy{}, errors.New("disposable payment outage")
	}
	return f.policy, nil
}
func (f *reservationPaymentFake) Lock(_ context.Context, q payment.Quote, _ string) (payment.Quote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		return payment.Quote{}, errors.New("disposable payment outage")
	}
	if !f.quote.ID.IsZero() && f.quote.ID != q.ID {
		return payment.Quote{}, errors.New("another quote was submitted")
	}
	f.quote = q
	return q, nil
}
func (f *reservationPaymentFake) Collect(_ context.Context, in payment.IntentInput, _ string) (payment.Intent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.collectionCalls++
	if f.unavailable {
		return payment.Intent{}, errors.New("disposable payment outage")
	}
	if in.LockedQuoteID == nil || *in.LockedQuoteID != f.quote.ID || in.EvidenceRef != f.quote.QuoteVersion || in.PolicyVersion != f.policy.Version {
		return payment.Intent{}, errors.New("locked quote/context mismatch")
	}
	if f.intent.ID.IsZero() {
		f.intent = payment.Intent{ID: ids.New(), WalletID: in.WalletID, AmountMinor: in.AmountMinor, Currency: in.Currency, State: "VERIFIED_SUCCESS", SettlementState: "PENDING", Reference: "disposable-provider-reference"}
	}
	if f.loseCollectionResponse {
		return payment.Intent{}, errors.New("collection committed but its response was lost")
	}
	return f.intent, nil
}
func (f *reservationPaymentFake) Intent(_ context.Context, id ids.ID) (payment.Intent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable || f.intent.ID != id {
		return payment.Intent{}, errors.New("disposable intent unavailable")
	}
	return f.intent, nil
}
func (f *reservationPaymentFake) Receipt(_ context.Context, id ids.ID) (payment.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable || id != f.quote.ID || f.receiptUnavailableAfterCollection && !f.intent.ID.IsZero() {
		return payment.Receipt{}, errors.New("disposable receipt unavailable")
	}
	r := payment.Receipt{RemedyVersion: 1, RemedyReviewRequired: f.remedyReviewRequired, Quote: f.quote, CollectionState: "NOT_INITIATED", SettlementState: "PENDING", ReversedMinor: f.reversed}
	r.PayerContext.WalletID = f.quote.PayerWalletID
	r.PayerContext.CanonicalPartyRef = f.quote.LegalPartyRef
	if f.forgedReceipt {
		r.Quote.BusinessRef = ids.New().String()
	}
	if !f.intent.ID.IsZero() {
		r.IntentID = &f.intent.ID
		r.CollectionState = f.intent.State
		r.SettlementState = f.intent.SettlementState
	}
	return r, nil
}
func reservationPaymentProvider(c *commercialHarness) *reservationPaymentFake {
	if existing, ok := c.domain.Payment.(*reservationPaymentFake); ok {
		return existing
	}
	f := &reservationPaymentFake{policy: payment.Policy{ID: ids.New(), Key: "HOTEL", Version: "fixture-v1", Currency: "NGN", Jurisdiction: "NG", ContractType: "HOTEL_STAY", Product: "ROOM", Active: true, EffectiveFrom: time.Now().Add(-time.Hour), Schedules: []string{"ONE_TIME"}}}
	c.domain.Payment = f
	return f
}
func reservationPaymentCommand(c *commercialHarness, v reservations.Detail) businesshttp.ReservationCollectionCommand {
	f := reservationPaymentProvider(c)
	c.domain.Payment = f
	group := v.FinancialGroups[0]

	return businesshttp.ReservationCollectionCommand{Version: v.Reservation.Version, FinancialGroupID: group.ID, PaymentPolicyID: f.policy.ID, PaymentPolicyVersion: f.policy.Version, PaymentPolicyFamily: "HOTEL", PayerWalletID: ids.New(), PayerType: "GUEST", ConsentReference: "explicit-test-payer-consent", LegalPartyReference: "canonical-test-guest-party", Email: "disposable-guest@example.invalid", Schedule: "ONE_TIME", Reason: "Collect the frozen accepted hotel obligation"}
}
func TestBusinessReservationPaymentReceiptRecoveryPrivacyAndReversal(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	in := reservationCommandFixture(c, false)
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", in, 201))
	command := reservationPaymentCommand(c, created)
	f := c.domain.Payment.(*reservationPaymentFake)
	f.loseCollectionResponse = true
	wrongPolicy := command
	wrongPolicy.PaymentPolicyID = ids.New()
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/payment-requests", wrongPolicy, 403)
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/payment-requests", command, 202)
	var stored string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT request::text FROM reservation_financial_requests WHERE reservation_id=$1`, created.Reservation.ID).Scan(&stored); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(stored, command.Email) || strings.Contains(stored, command.ConsentReference) {
		t.Fatal("financial request persisted private payer data in plaintext")
	}
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	paid := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+created.Reservation.ID.String(), nil, 200))
	if paid.Reservation.FinancialState != "VERIFIED" || paid.Reservation.State != "HOLD" {
		t.Fatalf("readback fabricated lifecycle or lost collection: %+v", paid.Reservation)
	}
	if f.collectionCalls != 1 {
		t.Fatalf("duplicate collection %d", f.collectionCalls)
	}
	var state, settlement string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT provider_state,settlement_state FROM reservation_payment_projections WHERE financial_group_id=$1`, command.FinancialGroupID).Scan(&state, &settlement); e != nil {
		t.Fatal(e)
	}
	if state != "VERIFIED_SUCCESS" || settlement != "PENDING" {
		t.Fatalf("collection/settlement conflated %s/%s", state, settlement)
	}
	paid = commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/confirm", reservations.Command{Version: paid.Reservation.Version, Reason: "Confirm the exact freshly paid reservation"}, 200))
	if paid.Reservation.State != "CONFIRMED" {
		t.Fatal("bound paid reservation did not confirm")
	}
	// A later authoritative reversal revokes eligibility even while an earlier
	// successful collection projection remains within its freshness window.
	f.mu.Lock()
	f.reversed = 1000
	f.mu.Unlock()
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/payment-readback", map[string]any{"version": paid.Reservation.Version, "financial_group_id": command.FinancialGroupID, "reason": "Refresh original collection and reversal facts"}, 202)
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	reversed := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+created.Reservation.ID.String(), nil, 200))
	if reversed.Reservation.FinancialState != "DISPUTED" {
		t.Fatalf("reversal state %s", reversed.Reservation.FinancialState)
	}
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/confirm", reservations.Command{Version: reversed.Reservation.Version, Reason: "A reversed collection cannot confirm a reservation"}, 409)
}
func TestBusinessReservationPaymentOutageAndForgedContractCannotConfirm(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, false), 201))
	command := reservationPaymentCommand(c, created)
	f := c.domain.Payment.(*reservationPaymentFake)
	f.unavailable = true
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/payment-requests", command, 202)
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	var state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM reservation_financial_requests WHERE reservation_id=$1`, created.Reservation.ID).Scan(&state); e != nil || state != "RETRY" {
		t.Fatalf("outage state %s %v", state, e)
	}
	f.unavailable = false
	f.forgedReceipt = true
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE reservation_financial_requests SET available_at=now(),version=version+1 WHERE reservation_id=$1`, created.Reservation.ID); e != nil {
		t.Fatal(e)
	}
	// Retry readback cannot prove that the first quote was ever created; a
	// provider outage remains unknown instead of causing a fresh collection.
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	if f.collectionCalls != 0 {
		t.Fatal("uncertain provider readback triggered collection")
	}
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/confirm", reservations.Command{Version: created.Reservation.Version, Reason: "Provider outage cannot be an accepted payment"}, 409)
}

func TestBusinessReservationPaidAmendmentConservesCreditAndCancellationDoesNotExecuteRefund(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, false), 201))
	collection := reservationPaymentCommand(c, created)
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/payment-requests", collection, 202)
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	paid := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+created.Reservation.ID.String(), nil, 200))
	confirmed := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/confirm", reservations.Command{Version: paid.Reservation.Version, Reason: "Confirm original authoritatively collected stay"}, 200))
	q := commercialData[revenue.QuoteSnapshot](t, c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/quotes", businesshttp.ReservationAmendmentQuote{Version: confirmed.Reservation.Version, Quote: businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1}}, 201))
	amend := reservations.Command{Version: confirmed.Reservation.Version, Reason: "Use the exact already collected value in an approved amendment", Rooms: []reservations.RoomCommand{{ProductID: q.ProductID, QuoteID: q.QuoteID, ProductVersion: q.ProductVersion, PoolVersion: q.PoolVersion, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1}}}
	op := amend
	amend.ApprovalID = c.approval("COMMERCIAL_RESERVATION_CHANGE", "reservations:approve", "", &q.AmountMinor, q.Currency, map[string]any{"reservation_id": created.Reservation.ID, "command": op})
	amended := commercialData[reservations.Detail](t, c.must(c.owner, "PATCH", "/reservations/"+created.Reservation.ID.String(), amend, 200))
	if amended.Reservation.State != "CONFIRMED" {
		t.Fatal("fully backed amendment lost confirmation")
	}
	var group reservations.FinancialGroupView
	for _, candidate := range amended.FinancialGroups {
		if candidate.AllocationRelationship == "ACTIVE_ALLOCATION" {
			group = candidate
		}
	}
	if group.RequiredAmountMinor != 0 || group.AmountMinor != q.AmountMinor || group.ID == collection.FinancialGroupID || group.AllocatedAmountMinor != q.AmountMinor || group.CreditsAppliedMinor != q.AmountMinor {
		t.Fatalf("amendment did not create exact delta obligation %+v", group)
	}

	var credits int64
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT sum(amount_minor) FROM reservation_financial_credits WHERE target_group_id=$1`, group.ID).Scan(&credits); e != nil || credits != q.AmountMinor {
		t.Fatalf("bound financial credits %d %v", credits, e)
	}
	f := c.domain.Payment.(*reservationPaymentFake)
	if f.collectionCalls != 1 {
		t.Fatal("same-value amendment charged the customer again")
	}
	cancelled := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/cancel", reservations.Command{Version: amended.Reservation.Version, Reason: "Cancel before the frozen penalty deadline"}, 200))
	var fee, refundable int64
	var state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT penalty_minor,refundable_minor,financial_state FROM reservation_cancellation_obligations WHERE reservation_id=$1 AND reservation_version=$2`, created.Reservation.ID, cancelled.Reservation.Version).Scan(&fee, &refundable, &state); e != nil || fee != 0 || refundable != q.AmountMinor || state != "KNOWN_PLATFORM_REFERENCE" {
		t.Fatalf("cancellation obligation fee=%d refund=%d state=%s %v", fee, refundable, state, e)
	}
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM reservation_financial_requests WHERE reservation_id=$1 AND kind='REFUND'`, created.Reservation.ID).Scan(&state); e != nil || state != "DEPENDENCY_BLOCKED" {
		t.Fatalf("refund fabricated as executed %s %v", state, e)
	}
	if f.collectionCalls != 1 {
		t.Fatal("cancellation executed another collection")
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE reservation_financial_credits SET amount_minor=0 WHERE target_group_id=$1`, group.ID); e == nil {
		t.Fatal("financial credit history was mutable")
	}
}

func TestBusinessReservationRefundedCollectionCannotBackConfirmation(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, false), 201))
	command := reservationPaymentCommand(c, created)
	f := c.domain.Payment.(*reservationPaymentFake)
	f.remedyReviewRequired = true
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/payment-requests", command, 202)
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	current := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+created.Reservation.ID.String(), nil, 200))
	if current.Reservation.FinancialState != "DISPUTED" {
		t.Fatalf("refunded funds treated as backing %s", current.Reservation.FinancialState)
	}
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/confirm", reservations.Command{Version: current.Reservation.Version, Reason: "Historical collection success cannot hide a financial remedy"}, 409)
}
func TestBusinessReservationLostCollectionRecoveredAfterSessionRevocation(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, false), 201))
	command := reservationPaymentCommand(c, created)
	f := c.domain.Payment.(*reservationPaymentFake)
	f.loseCollectionResponse = true
	f.receiptUnavailableAfterCollection = true
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/payment-requests", command, 202)
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	var requestID ids.ID
	var version int64
	var state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT id,version,state FROM reservation_financial_requests WHERE reservation_id=$1`, created.Reservation.ID).Scan(&requestID, &version, &state); e != nil || state != "RETRY" {
		t.Fatalf("uncertain collection not retained %s %v", state, e)
	}
	operator := c.user()
	c.secure(operator)
	c.join(operator, c.owner, []string{"reservations:read", "reservations:write"})
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE business_sessions SET revoked_at=now() WHERE id=$1`, c.owner.tokens.SessionID); e != nil {
		t.Fatal(e)
	}
	f.receiptUnavailableAfterCollection = false
	retry := businesshttp.ReservationFinancialRetry{Version: created.Reservation.Version, RequestVersion: version, Reason: "Recover original authoritative receipt after transport and session loss"}
	c.must(operator, "POST", "/reservations/"+created.Reservation.ID.String()+"/payment-requests/"+requestID.String()+"/retry", retry, 202)
	c.must(operator, "POST", "/reservations/"+created.Reservation.ID.String()+"/payment-requests/"+requestID.String()+"/retry", retry, 409)
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	current := commercialData[reservations.Detail](t, c.must(operator, "GET", "/reservations/"+created.Reservation.ID.String(), nil, 200))
	if current.Reservation.FinancialState != "VERIFIED" || current.Reservation.State != "HOLD" || f.collectionCalls != 1 {
		t.Fatalf("recovery changed lifecycle or collected twice: %+v calls=%d", current.Reservation, f.collectionCalls)
	}
	c.must(c.owner, "GET", "/reservations/"+created.Reservation.ID.String(), nil, 401)
}

func TestBusinessReservationLateCollectionAfterExpiryCreatesOneFrozenRefundReview(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, false), 201))
	command := reservationPaymentCommand(c, created)
	f := c.domain.Payment.(*reservationPaymentFake)
	f.loseCollectionResponse = true
	f.receiptUnavailableAfterCollection = true
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/payment-requests", command, 202)
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	clock := created.Reservation.HoldExpiresAt.Add(time.Second)
	c.service.Now = func() time.Time { return clock }
	if e := c.domain.ProcessReservationMaintenance(t.Context(), 20); e != nil {
		t.Fatal(e)
	}
	expired := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+created.Reservation.ID.String(), nil, 200))
	if expired.Reservation.State != "EXPIRED" {
		t.Fatal("reservation did not expire")
	}
	f.receiptUnavailableAfterCollection = false
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE reservation_financial_requests SET available_at=now(),version=version+1 WHERE reservation_id=$1 AND kind='COLLECTION'`, created.Reservation.ID); e != nil {
		t.Fatal(e)
	}
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	reconciled := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+created.Reservation.ID.String(), nil, 200))
	if reconciled.Reservation.State != "EXPIRED" || f.collectionCalls != 1 {
		t.Fatal("late collection revived a booking or charged twice")
	}
	var count int
	var amount int64
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*),coalesce(sum((request->>'amount_minor')::bigint),0) FROM reservation_financial_requests WHERE reservation_id=$1 AND kind='REFUND' AND state='DEPENDENCY_BLOCKED'`, created.Reservation.ID).Scan(&count, &amount); e != nil || count != 1 || amount != created.Reservation.AmountMinor {
		t.Fatalf("lost late-payment refund review count=%d amount=%d %v", count, amount, e)
	}
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/payment-readback", map[string]any{"version": reconciled.Reservation.Version, "financial_group_id": command.FinancialGroupID, "reason": "Repeat readback without duplicating the frozen remedy"}, 202)
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM reservation_financial_requests WHERE reservation_id=$1 AND kind='REFUND'`, created.Reservation.ID).Scan(&count); e != nil || count != 1 {
		t.Fatalf("duplicate refund obligation %d %v", count, e)
	}
}

func TestBusinessReservationFinancialRemedyInvalidatesEverySplitRoot(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	start, _ := time.Parse(time.DateOnly, c.dates.DateFrom)
	middle := start.AddDate(0, 0, 1).Format(time.DateOnly)
	commands := []reservations.RoomCommand{}
	for _, dates := range []inventory.DateRange{{DateFrom: c.dates.DateFrom, DateTo: middle}, {DateFrom: middle, DateTo: c.dates.DateTo}} {
		q := commercialData[revenue.QuoteSnapshot](t, c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: dates.DateFrom, DateTo: dates.DateTo, Adults: 2, Quantity: 1}, 201))
		commands = append(commands, reservations.RoomCommand{ProductID: q.ProductID, QuoteID: q.QuoteID, ProductVersion: q.ProductVersion, PoolVersion: q.PoolVersion, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1})
	}
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservations.Create{PropertyID: c.property.ID, Source: "GROUP", State: "HOLD", PaymentResponsibility: "PLATFORM_COLLECT", GroupName: "Shared original financial collection", Rooms: commands, Reason: "Preserve one original collected obligation across split roots"}, 201))
	collection := reservationPaymentCommand(c, created)
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/payment-requests", collection, 202)
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	paid := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+created.Reservation.ID.String(), nil, 200))
	moved := paid.Rooms[0]
	split := reservations.Command{Version: paid.Reservation.Version, RoomIDs: []ids.ID{moved.ID}, Reason: "Split a priced line retaining the same original payment backing"}
	op := split
	split.ApprovalID = c.approval("COMMERCIAL_RESERVATION_LINEAGE", "reservations:approve", "", &moved.AmountMinor, moved.Currency, map[string]any{"reservation_id": paid.Reservation.ID, "action": "SPLIT", "command": op})
	type result struct {
		Source reservations.Reservation `json:"source"`
		Target reservations.Detail      `json:"target"`
	}
	divided := commercialData[result](t, c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/split", split, 200))
	f := c.domain.Payment.(*reservationPaymentFake)
	f.remedyReviewRequired = true
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/payment-readback", map[string]any{"version": divided.Source.Version, "financial_group_id": collection.FinancialGroupID, "reason": "Observe an authoritative remedy against the shared original payment"}, 202)
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	for _, id := range []ids.ID{created.Reservation.ID, divided.Target.Reservation.ID} {
		current := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+id.String(), nil, 200))
		if current.Reservation.FinancialState != "DISPUTED" {
			t.Fatalf("split root retained stale financial success: %+v", current.Reservation)
		}
	}
}
