package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/jackc/pgx/v5"
)

func pmsFiscalQuoteFixture(c *commercialHarness) (revenue.QuoteSnapshot, revenue.AddOn) {
	c.t.Helper()
	c.approver = c.user()
	c.secure(c.approver)
	c.join(c.approver, c.owner, []string{"approvals:decide", "pricing:approve", "rates:approve", "reservations:approve", "pms:approve", "folios:approve"})
	fiscal := businesshttp.CommercialTaxCreate{PropertyID: c.property.ID, Currency: "NGN", Definition: revenue.TaxPolicy{DisplayMode: "ITEMIZED", FiscalReference: "disposable-qualified-tax-policy", PolicyVersion: 1, Taxes: []revenue.TaxComponent{{Code: "VAT", BasisPoints: 1000, Included: true}, {Code: "LOCAL", BasisPoints: 500}}, Fees: []revenue.FeeComponent{{Code: "RESORT", AmountMinor: 1000, Included: true}, {Code: "SERVICE", AmountMinor: 500}}}, SourceReference: "test-only-approved-source-invoice-policy", EffectiveAt: time.Now().Add(-time.Minute), Reason: "Capture approved mixed fiscal definitions for the actual accepted booking"}
	fiscal.ApprovalID = c.approval("COMMERCIAL_FISCAL_POLICY_CHANGE", "pricing:approve", "", nil, "", map[string]any{"property_id": c.property.ID, "command": fiscal})
	profile := commercialData[businesshttp.CommercialTaxProfile](c.t, c.must(c.owner, "POST", "/tax-profiles", fiscal, 201))
	definition := c.definition
	definition.TaxProfileID = &profile.ID
	created := commercialData[struct {
		Plan revenue.Plan `json:"rate_plan"`
	}](c.t, c.must(c.owner, "POST", "/rate-plans", businesshttp.CommercialRateCreate{PropertyID: c.property.ID, Name: "Fiscal source fixture plan", Kind: "REFUNDABLE", DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Definition: definition, Reason: "Create original immutable quote context with supported fiscal components"}, 201))
	activation := businesshttp.CommercialRatePatch{Version: created.Plan.Version, State: "ACTIVE", Reason: "Independently activate the exact source fiscal rate plan"}
	activation.ApprovalID = c.approval("COMMERCIAL_RATE_ACTIVATION", "rates:approve", "", &definition.CeilingMinor, "NGN", map[string]any{"rate_plan_id": created.Plan.ID, "command": activation})
	c.rate = commercialData[struct {
		Plan revenue.Plan `json:"rate_plan"`
	}](c.t, c.must(c.owner, "PATCH", "/rate-plans/"+created.Plan.ID.String(), activation, 200)).Plan
	update := businesshttp.CommercialProductPatch{Version: c.product.Version, RatePlanID: &c.rate.ID, Reason: "Bind the real product to its independently approved fiscal rate plan"}
	update.ApprovalID = c.approval("COMMERCIAL_PRODUCT_CHANGE", "pricing:approve", "", nil, "", map[string]any{"product_id": c.product.ID, "command": update})
	c.product = commercialData[inventory.Product](c.t, c.must(c.owner, "PATCH", "/sellable-products/"+c.product.ID.String(), update, 200))
	addon := commercialData[revenue.AddOn](c.t, c.must(c.owner, "POST", "/add-ons", businesshttp.CommercialAddOnCreate{PropertyID: c.property.ID, Name: "Actual accepted breakfast obligation", Definition: revenue.AddOnDefinition{Currency: "NGN", AmountMinor: 1000, MaxQuantity: 2, ExecutionReference: "accepted-menu-source-v1"}, Reason: "Create an accepted commercial source without claiming physical service delivery"}, 201))
	c.activateOffer(addon.ID, "add_ons", addon.Version, 2000, "NGN")
	if e := c.db.Pool.QueryRow(c.t.Context(), `SELECT version FROM add_ons WHERE id=$1`, addon.ID).Scan(&addon.Version); e != nil {
		c.t.Fatal(e)
	}
	quote := commercialData[revenue.QuoteSnapshot](c.t, c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1, AddOns: []businesshttp.CommercialPackageComponent{{AddOnID: addon.ID, Quantity: 2}}}, 201))
	return quote, addon
}

// The legacy fixture is an already-issued immutable quote using the previous
// serialization. Its source price, fiscal policy and charged total are retained;
// only fields unavailable to the old serializer are absent. All subsequent
// reservation, Payment, assignment, check-in and review operations use HTTP.
func pmsStayFromFiscalQuote(c *commercialHarness, q revenue.QuoteSnapshot, legacy bool) (reservations.Detail, businesshttp.PMSStayDetail) {
	c.t.Helper()
	reservationPolicyFixture(c, false)
	if legacy {
		q.QuoteID = ids.New()
		q.FiscalAllocationVersion = ""
		q.AddOnAmounts = nil
		raw, e := json.Marshal(q)
		if e != nil {
			c.t.Fatal(e)
		}
		_, e = c.db.Pool.Exec(c.t.Context(), `INSERT INTO price_quotes(id,organisation_id,property_id,product_id,rate_version_id,date_from,date_to,amount_minor,currency,expires_at,snapshot,created_by) VALUES($1,$2,$3,$4,$5,$6::date,$7::date,$8,$9,$10,$11,$12)`, q.QuoteID, c.owner.registered.OrganisationID, q.PropertyID, q.ProductID, q.Nights[0].RateVersionID, q.DateFrom, q.DateTo, q.AmountMinor, q.Currency, q.ExpiresAt, raw, c.owner.registered.AccountID)
		if e != nil {
			c.t.Fatal(e)
		}
	}
	guest := crmGuestFixture(c)
	cmd := reservationCommandFixture(c, false)
	cmd.Rooms[0].QuoteID = q.QuoteID
	cmd.Guests = []reservations.GuestCommand{{GuestID: &guest.Guest.ID, SourceKind: guest.SourceKind, SourceReference: guest.SourceReference, IsPrimary: true}}
	v := commercialData[reservations.Detail](c.t, c.must(c.owner, "POST", "/reservations", cmd, 201))
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/payment-requests", reservationPaymentCommand(c, v), 202)
	if e := c.domain.ProcessReservationFinance(c.t.Context(), 1); e != nil {
		c.t.Fatal(e)
	}
	v = commercialData[reservations.Detail](c.t, c.must(c.owner, "GET", "/reservations/"+v.Reservation.ID.String(), nil, 200))
	v = commercialData[reservations.Detail](c.t, c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/confirm", reservations.Command{Version: v.Reservation.Version, Reason: "Confirm accepted actual source and owning collection evidence"}, 200))
	ready := pmsReady(c)
	assignment := pmsAssign(c, v, ready)
	check := businesshttp.PMSCheckInCommand{AssignmentID: assignment.ID, AssignmentVersion: assignment.Version, ReservationVersion: v.Reservation.Version, RoomStatusVersion: ready.Version, GuestID: guest.Guest.ID, RegistrationReference: "actual-fiscal-journey-registration", Reason: "Open the actual physical stay with immutable accepted gross and fiscal source"}
	arrival, e := hospitality.LocalInstant(assignment.DateFrom, v.Reservation.PolicySnapshot.CheckInLocal, v.Reservation.PolicySnapshot.AcceptanceTimezone)
	if e != nil {
		c.t.Fatal(e)
	}
	if time.Now().Before(arrival) {
		op := check
		check.ApprovalID = c.approval("COMMERCIAL_PMS_EARLY_CHECK_IN", "pms:approve", "", nil, "", map[string]any{"command": op})
	}
	opened := commercialData[struct {
		StayID ids.ID `json:"stay_id"`
	}](c.t, c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/check-in", check, 201))
	return v, commercialData[businesshttp.PMSStayDetail](c.t, c.must(c.owner, "GET", "/stays/"+opened.StayID.String(), nil, 200))
}

func TestBusinessPMSFiscalDetailPreservesGrossAndNeverDuplicatesACharge(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	quote, _ := pmsFiscalQuoteFixture(c)
	if quote.FiscalAllocationVersion != "PROPORTIONAL_PRETAX_V1" || len(quote.AddOnAmounts) != 1 || quote.AddOnAmounts[0].Quantity != 2 {
		t.Fatal("new quote failed to retain actual charged source")
	}
	v, d := pmsStayFromFiscalQuote(c, quote, false)
	if len(d.Items) != 1 || d.Items[0].FiscalReview == nil || d.Items[0].FiscalReview.State != "ITEMIZED" || d.Items[0].AmountMinor != quote.AmountMinor || d.BalanceMinor != quote.AmountMinor {
		t.Fatalf("duplicated or missing fiscal obligation %+v", d)
	}
	detail := d.Items[0].FiscalReview.Breakdown
	var sum int64
	tax, fee, addon := false, false, false
	for _, part := range detail.Components {
		sum += part.AmountMinor
		tax = tax || part.Kind == "TAX"
		fee = fee || part.Kind == "FEE"
		addon = addon || part.Kind == "ADD_ON_NET"
	}
	if sum != quote.AmountMinor || !tax || !fee || !addon || detail.FinancialEffect != "DETAIL_ONLY" {
		t.Fatalf("incorrect source detail %+v", detail)
	}
	nochange := businesshttp.PMSFiscalReviewCommand{FolioVersion: 1, ItemID: d.Items[0].ID, ExpectedReviewVersion: 1, Reason: "Rebuild original source without duplicating an unchanged fiscal version"}
	c.must(c.owner, "POST", "/folios/"+d.Folio.ID.String()+"/fiscal-reviews", nochange, 200)
	var items, reviews int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM folio_items WHERE folio_id=$1),(SELECT count(*) FROM folio_fiscal_reviews WHERE folio_id=$1)`, d.Folio.ID).Scan(&items, &reviews); e != nil || items != 1 || reviews != 1 {
		t.Fatalf("duplicate financial or review write %d/%d %v", items, reviews, e)
	}
	c.must(c.owner, "GET", "/folios/"+d.Folio.ID.String()+"/fiscal-reviews", nil, 200)
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE folio_fiscal_reviews SET state='OPERATOR_REVIEWED' WHERE folio_id=$1`, d.Folio.ID); e == nil {
		t.Fatal("fiscal evidence was mutable")
	}
	outsider := c.user()
	c.must(outsider, "POST", "/folios/"+d.Folio.ID.String()+"/fiscal-reviews", nochange, 404)
	pmsReadSettled(c, v)
	c.must(c.owner, "POST", "/folios/"+d.Folio.ID.String()+"/reconcile", businesshttp.PMSVersionCommand{Version: 1, Reason: "Reconcile only the single original obligation against actual signed settlement"}, 200)
	balance, e := hospitality.Balance(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, d.Folio.ID, time.Now())
	if e != nil || balance != 0 {
		t.Fatalf("fiscal detail changed settled gross %d %v", balance, e)
	}
}

func TestBusinessPMSLegacyFiscalAttributionNeedsDistinctApprovalAndNewCloseReview(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	quote, addon := pmsFiscalQuoteFixture(c)
	v, d := pmsStayFromFiscalQuote(c, quote, true)
	if len(d.Items) != 1 || d.Items[0].FiscalReview == nil || d.Items[0].FiscalReview.State != "LEGACY_AGGREGATE_REVIEW_REQUIRED" {
		t.Fatalf("invented legacy source detail %+v", d)
	}
	pmsReadSettled(c, v)
	path := "/folios/" + d.Folio.ID.String()
	c.must(c.owner, "POST", path+"/reconcile", businesshttp.PMSVersionCommand{Version: 1, Reason: "Record actual settlement independently of missing legacy net attribution"}, 200)
	var rootVersion int64
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT version FROM reservations WHERE id=$1`, v.Reservation.ID).Scan(&rootVersion); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/check-out", businesshttp.PMSCheckoutCommand{StayID: d.Stay.ID, Version: 1, ReservationVersion: rootVersion, FolioVersion: 2, Reason: "Record actual departure while retaining explicit pending fiscal review"}, 200)
	c.must(c.owner, "POST", path+"/close", businesshttp.PMSVersionCommand{Version: 3, Reason: "Do not close an unreconciled original legacy fiscal source"}, 409)
	c.must(c.owner, "POST", path+"/close-reviews", businesshttp.PMSFolioCloseReviewCommand{Version: 3, AutomaticClose: true, ExpiresAt: time.Now().Add(time.Hour), Reason: "Any later fiscal source review must invalidate this captured final-posting digest"}, 201)
	var net int64
	for _, part := range d.Items[0].FiscalReview.Breakdown.Components {
		if part.Kind == "ACCEPTED_SERVICES_NET" {
			net = part.AmountMinor
		}
	}
	m, e := money.New(net, "NGN")
	if e != nil {
		t.Fatal(e)
	}
	attributed, e := m.Allocate(quote.BaseAmountMinor-quote.DiscountMinor, quote.AddOnsMinor)
	if e != nil {
		t.Fatal(e)
	}
	review := businesshttp.PMSFiscalReviewCommand{FolioVersion: 3, ItemID: d.Items[0].ID, ExpectedReviewVersion: 1, EvidenceReference: "test-only-qualified-invoice-review", NetAllocations: []businesshttp.PMSFiscalNetAllocation{{Kind: "ACCOMMODATION_NET", AmountMinor: attributed[0].AmountMinor}, {Kind: "ADD_ON_NET", AmountMinor: attributed[1].AmountMinor, AddOnID: &addon.ID, SourceVersion: addon.Version, Quantity: 2}}, Reason: "Attest original legacy net source allocation without modifying frozen taxes fees or gross"}
	c.must(c.owner, "POST", path+"/fiscal-reviews", review, 403)
	for _, invalid := range []businesshttp.PMSFiscalNetAllocation{
		{Kind: "TAX", AmountMinor: net},
		{Kind: "ADD_ON_NET", AmountMinor: net, AddOnID: &addon.ID, SourceVersion: addon.Version + 1, Quantity: 2},
	} {
		bad := review
		bad.NetAllocations = []businesshttp.PMSFiscalNetAllocation{invalid}
		c.must(c.owner, "POST", path+"/fiscal-reviews", bad, 400)
	}
	op := review
	review.ApprovalID = c.approval("COMMERCIAL_PMS_FISCAL_REVIEW", "folios:approve", "", &d.Items[0].AmountMinor, "NGN", map[string]any{"folio_id": d.Folio.ID, "command": op})
	type outcome struct {
		code int
		body []byte
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			code, body := c.call(c.owner, "POST", path+"/fiscal-reviews", review, nil)
			results <- outcome{code, body}
		}()
	}
	a, b := <-results, <-results
	if a.code == 409 {
		a, b = b, a
	}
	if a.code != 201 || b.code != 409 {
		t.Fatalf("concurrent review must commit exactly once: %d/%d %s %s", a.code, b.code, a.body, b.body)
	}
	result := commercialData[struct {
		Review       hospitality.FiscalReview `json:"review"`
		FolioVersion int64                    `json:"folio_version"`
		Changed      bool                     `json:"changed"`
	}](t, a.body)
	if !result.Changed || result.Review.State != "OPERATOR_REVIEWED" || result.FolioVersion != 4 || result.Review.Breakdown.GrossMinor != quote.AmountMinor {
		t.Fatalf("invalid reviewed source %+v", result)
	}
	pmsRunMaintenance(c)
	var state string
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT state FROM folios WHERE id=$1`, d.Folio.ID).Scan(&state); e != nil || state == "CLOSED" {
		t.Fatalf("old close review survived fiscal change %s %v", state, e)
	}
	c.must(c.owner, "POST", path+"/close", businesshttp.PMSVersionCommand{Version: 4, Reason: "Close actual departed settled folio after the independently reviewed source correction"}, 200)
	var count int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM folio_items WHERE folio_id=$1`, d.Folio.ID).Scan(&count); e != nil || count != 1 {
		t.Fatalf("source review created a second charge %d %v", count, e)
	}
	provider := reservationPaymentProvider(c)
	provider.mu.Lock()
	calls := provider.collectionCalls
	provider.mu.Unlock()
	if calls != 1 {
		t.Fatalf("source review created another collection %d", calls)
	}
}

func TestBusinessPMSFiscalBackfillQuarantinesDuplicateSourceUntilApprovedCorrection(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	_, d := pmsCollectedStay(c)
	original := d.Items[0]
	duplicate := ids.New()
	org := c.owner.registered.OrganisationID
	actor := c.owner.registered.AccountID
	// Deliberately malformed legacy/import fixture. All immutable/version/audit
	// guards remain enabled; the public posting API cannot create accommodation
	// duplicates. Reconciliation must quarantine this source, never certify it.
	e := database.WithTx(t.Context(), c.db.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, org); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO folio_items(id,organisation_id,property_id,folio_id,kind,source_reference,quantity,amount_minor,currency,policy_snapshot,reason,created_by,business_day_id) SELECT $1,organisation_id,property_id,folio_id,kind,$2,quantity,amount_minor,currency,policy_snapshot,'MALFORMED_LEGACY_IMPORT_TEST_ONLY',$3,business_day_id FROM folio_items WHERE id=$4`, duplicate, "malformed-legacy-source:"+duplicate.String(), actor, original.ID); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE folios SET version=version+1,updated_at=now() WHERE id=$1`, d.Folio.ID); e != nil {
			return e
		}
		evidence, _ := json.Marshal(map[string]any{"fixture": "MALFORMED_LEGACY_IMPORT_TEST_ONLY", "item_id": duplicate})
		if _, e := tx.Exec(ctx, `INSERT INTO hospitality_history(id,organisation_id,property_id,entity_id,entity_type,entity_version,action,change_context,created_by) VALUES($1,$2,$3,$4,'folio',2,'LEGACY_IMPORT_FIXTURE',$5,$6)`, ids.New(), org, c.property.ID, d.Folio.ID, evidence, actor); e != nil {
			return e
		}
		return businessEvents.CommercialCommit(ctx, tx, c.domain.Auth.Service.Outbox, org, c.property.ID, d.Folio.ID, actor, "folio", "LEGACY_IMPORT_FIXTURE", "hospitality.folio.updated.v1", 2, map[string]any{"fixture": "MALFORMED_LEGACY_IMPORT_TEST_ONLY"})
	})
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e = c.domain.ProcessFolioFiscalMaintenance(t.Context(), 50); e != nil {
			t.Fatal(e)
		}
	}
	var badReviews, exceptions int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM folio_fiscal_reviews WHERE folio_item_id=$1),(SELECT count(*) FROM hospitality_operation_exceptions WHERE source_id=$2 AND code='FISCAL_SOURCE_REVIEW_REQUIRED' AND state='OPEN')`, duplicate, d.Folio.ID).Scan(&badReviews, &exceptions); e != nil || badReviews != 0 || exceptions != 1 {
		t.Fatalf("invalid source escaped quarantine %d/%d %v", badReviews, exceptions, e)
	}
	ready, e := hospitality.FiscalReady(t.Context(), c.db.Pool, org, d.Folio.ID)
	if e != nil || ready {
		t.Fatal("duplicate source incorrectly qualified for closure")
	}
	adjustment := businesshttp.PMSFolioPosting{Version: 2, Kind: "ADJUSTMENT", SourceReference: "reviewed-import-correction:" + duplicate.String(), Quantity: 1, AmountMinor: -original.AmountMinor, Currency: original.Currency, PolicyReference: "qualified-import-correction-policy-v1", AdjustsItemID: &duplicate, Reason: "Retain the malformed import and offset only its independently reviewed duplicate obligation"}
	op := adjustment
	amount := original.AmountMinor
	adjustment.ApprovalID = c.approval("COMMERCIAL_PMS_FOLIO_ADJUSTMENT", "folios:approve", "", &amount, original.Currency, map[string]any{"folio_id": d.Folio.ID, "command": op})
	c.must(c.owner, "POST", "/folios/"+d.Folio.ID.String()+"/items", adjustment, 201)
	ready, e = hospitality.FiscalReady(t.Context(), c.db.Pool, org, d.Folio.ID)
	if e != nil || !ready {
		t.Fatalf("approved full correction did not reconcile source %v", e)
	}
	balance, e := hospitality.Balance(t.Context(), c.db.Pool, org, d.Folio.ID, time.Now())
	if e != nil || balance != original.AmountMinor {
		t.Fatalf("correction changed original accepted obligation %d %v", balance, e)
	}
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM hospitality_operation_exceptions WHERE source_id=$1 AND code='FISCAL_SOURCE_REVIEW_REQUIRED' AND state='OPEN'`, d.Folio.ID).Scan(&exceptions); e != nil || exceptions != 0 {
		t.Fatalf("verified correction left stale exception %d %v", exceptions, e)
	}
}
