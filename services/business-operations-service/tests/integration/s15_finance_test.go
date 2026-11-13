package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/finance"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// financePaymentFake is a contract double for the Payment Service readback
// endpoints used by Section 15 (intent and beneficiary authority).
type financePaymentFake struct {
	mu          sync.Mutex
	intents     map[ids.ID]payment.Intent
	authorities map[ids.ID]payment.AuthorityStatus
	unavailable bool
}

func (f *financePaymentFake) Policy(context.Context, ids.ID) (payment.Policy, error) {
	return payment.Policy{}, errors.New("unused")
}
func (f *financePaymentFake) Receipt(context.Context, ids.ID) (payment.Receipt, error) {
	return payment.Receipt{}, errors.New("unused")
}
func (f *financePaymentFake) Lock(context.Context, payment.Quote, string) (payment.Quote, error) {
	return payment.Quote{}, errors.New("unused")
}
func (f *financePaymentFake) Collect(context.Context, payment.IntentInput, string) (payment.Intent, error) {
	return payment.Intent{}, errors.New("unused")
}
func (f *financePaymentFake) Intent(_ context.Context, id ids.ID) (payment.Intent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		return payment.Intent{}, errors.New("payment-service timeout")
	}
	i, ok := f.intents[id]
	if !ok {
		return payment.Intent{}, errors.New("not found")
	}
	return i, nil
}
func (f *financePaymentFake) BeneficiaryAuthority(_ context.Context, id ids.ID) (payment.AuthorityStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.authorities[id]
	if !ok {
		return payment.AuthorityStatus{}, errors.New("not found")
	}
	return a, nil
}

func (f *financePaymentFake) authority(active bool) ids.ID {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := ids.New()
	f.authorities[id] = payment.AuthorityStatus{AuthorityID: id, Domain: "business-operations", Role: "BENEFICIARY", Currency: "NGN", ExpiresAt: time.Now().Add(240 * time.Hour), Active: active}
	return id
}

func newS15(t *testing.T) (*commercialHarness, *financePaymentFake) {
	c := newCommercialHTTP(t)
	fake := &financePaymentFake{intents: map[ids.ID]payment.Intent{}, authorities: map[ids.ID]payment.AuthorityStatus{}}
	c.domain.Payment = fake
	c.grant(c.approver, "finance:approve")
	return c, fake
}

func (c *commercialHarness) companyApproval(action, capability string, amount *int64, currency string, operation any) ids.ID {
	return c.approvalAt(action, capability, "", amount, currency, operation, "COMPANY", c.owner.registered.OrganisationID)
}

func TestBusinessFinanceBudgetControlIndependentApprovalAndObligations(t *testing.T) {
	c, _ := newS15(t)
	cc := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", "/cost-centres", businesshttp.CostCentreCreate{PropertyID: &c.property.ID, Code: "HOTEL-OPS", Name: "Hotel operations", Reason: "Property cost centre"}, 201))
	period := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", "/fiscal-periods", businesshttp.FiscalPeriodCreate{Code: "FY2027", StartsOn: "2027-01-01", EndsOn: "2027-12-31", Reason: "Fiscal year"}, 201))
	c.must(c.owner, "POST", "/fiscal-periods", businesshttp.FiscalPeriodCreate{Code: "OVERLAP", StartsOn: "2027-06-01", EndsOn: "2028-01-31", Reason: "Overlapping period"}, 400)
	budget := commercialData[businesshttp.Budget](t, c.must(c.owner, "POST", "/budgets", businesshttp.BudgetCreate{CostCentreID: cc.ID, FiscalPeriodID: period.ID, Currency: "NGN", Lines: []businesshttp.BudgetLine{{LineNo: 1, Category: "MAINTENANCE", AmountMinor: 100000, ApprovalThresholdMinor: 10000}}, Reason: "Annual maintenance budget"}, 201))
	c.must(c.owner, "POST", "/budgets/"+budget.ID.String()+"/activate", businesshttp.FinanceApprovalCommand{Version: budget.Version, Reason: "No approval"}, 403)
	total := int64(100000)
	approval := c.companyApproval("COMMERCIAL_BUDGET_ACTIVATION", "finance:approve", &total, "NGN", map[string]any{"budget_id": budget.ID, "budget_version": 1})
	budget = commercialData[businesshttp.Budget](t, c.must(c.owner, "POST", "/budgets/"+budget.ID.String()+"/activate", businesshttp.FinanceApprovalCommand{Version: budget.Version, ApprovalID: approval, Reason: "Approved budget"}, 200))
	small := commercialData[businesshttp.ExpenseRequest](t, c.must(c.owner, "POST", "/expense-requests", businesshttp.ExpenseRequestCreate{BudgetID: budget.ID, BudgetLineNo: 1, Description: "Replace lobby bulbs", CounterpartyReference: "vendor:lighting-co", AmountMinor: 5000, Reason: "Routine spend"}, 201))
	if small.RequiresApproval {
		t.Fatal("below-threshold spend required approval")
	}
	small = commercialData[businesshttp.ExpenseRequest](t, c.must(c.owner, "POST", "/expense-requests/"+small.ID.String()+"/approve", businesshttp.FinanceApprovalCommand{Version: small.Version, Reason: "Within delegated limit"}, 200))
	small = commercialData[businesshttp.ExpenseRequest](t, c.must(c.owner, "POST", "/expense-requests/"+small.ID.String()+"/obligation", businesshttp.OpsVersionCommand{Version: small.Version, Reason: "Create payable obligation"}, 200))
	if small.State != "OBLIGATION_CREATED" || small.ObligationID == nil {
		t.Fatalf("obligation %+v", small)
	}
	big := commercialData[businesshttp.ExpenseRequest](t, c.must(c.owner, "POST", "/expense-requests", businesshttp.ExpenseRequestCreate{BudgetID: budget.ID, BudgetLineNo: 1, Description: "Boiler replacement", CounterpartyReference: "vendor:boilers", AmountMinor: 50000, Reason: "Capital repair"}, 201))
	c.must(c.owner, "POST", "/expense-requests/"+big.ID.String()+"/approve", businesshttp.FinanceApprovalCommand{Version: big.Version, Reason: "Self approval attempt"}, 403)
	amount := int64(50000)
	approval = c.companyApproval("COMMERCIAL_EXPENSE_APPROVAL", "finance:approve", &amount, "NGN", map[string]any{"expense_id": big.ID, "budget_id": budget.ID, "budget_line_no": 1, "counterparty_reference": "vendor:boilers"})
	big = commercialData[businesshttp.ExpenseRequest](t, c.must(c.owner, "POST", "/expense-requests/"+big.ID.String()+"/approve", businesshttp.FinanceApprovalCommand{Version: big.Version, ApprovalID: approval, Reason: "Independently approved"}, 200))
	c.must(c.owner, "POST", "/expense-requests", businesshttp.ExpenseRequestCreate{BudgetID: budget.ID, BudgetLineNo: 1, Description: "Exceeds the line", CounterpartyReference: "vendor:x", AmountMinor: 60000, Reason: "Too much"}, 409)
	if _, err := c.db.Pool.Exec(t.Context(), `UPDATE expense_requests SET amount_minor=1,version=version+1 WHERE id=$1`, big.ID); err == nil {
		t.Fatal("approved expense amount mutated")
	}
	if _, err := c.db.Pool.Exec(t.Context(), `UPDATE financial_obligations SET amount_minor=amount_minor+1,version=version+1 WHERE id=$1`, *small.ObligationID); err == nil {
		t.Fatal("obligation amount changed without immutable adjustment")
	}
	other := c.user()
	if status, _ := c.call(other, "GET", "/financial-obligations/"+small.ObligationID.String(), nil, nil); status != 403 && status != 404 {
		t.Fatalf("cross-tenant finance read %d", status)
	}
}

func TestBusinessFinanceObligationReadbackUnknownSettledAndReversal(t *testing.T) {
	c, fake := newS15(t)
	cc := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", "/cost-centres", businesshttp.CostCentreCreate{Code: "CORP", Name: "Corporate", Reason: "Company cost centre"}, 201))
	o := commercialData[businesshttp.FinancialObligation](t, c.must(c.owner, "POST", "/financial-obligations", businesshttp.ObligationCreate{CostCentreID: cc.ID, Direction: "RECEIVABLE", Category: "SERVICE_CHARGE", CounterpartyReference: "tenant:unit-4", SourceType: "CONTRACT", SourceReference: "lease-4:service-charge:2027-01", AmountMinor: 20000, Currency: "NGN", DueOn: "2027-01-31", Reason: "Service charge"}, 201))
	amount := int64(20000)
	approval := c.companyApproval("COMMERCIAL_FINANCIAL_OBLIGATION", "finance:approve", &amount, "NGN", map[string]any{"obligation_id": o.ID, "direction": "RECEIVABLE", "counterparty_reference": "tenant:unit-4"})
	o = commercialData[businesshttp.FinancialObligation](t, c.must(c.owner, "POST", "/financial-obligations/"+o.ID.String()+"/approve", businesshttp.FinanceApprovalCommand{Version: o.Version, ApprovalID: approval, Reason: "Approve obligation"}, 200))
	delta := int64(500)
	adj := c.companyApproval("COMMERCIAL_FINANCIAL_OBLIGATION", "finance:approve", &delta, "NGN", map[string]any{"obligation_id": o.ID, "delta_minor": 500, "obligation_version": o.Version})
	o = commercialData[businesshttp.FinancialObligation](t, c.must(c.owner, "POST", "/financial-obligations/"+o.ID.String()+"/adjustments", businesshttp.ObligationAdjustment{Version: o.Version, DeltaMinor: 500, ApprovalID: adj, Reason: "Late fee per contract"}, 200))
	if o.AmountMinor != 20500 || o.OriginalAmountMinor != 20000 {
		t.Fatalf("adjustment %+v", o)
	}
	intent := ids.New()
	o = commercialData[businesshttp.FinancialObligation](t, c.must(c.owner, "POST", "/financial-obligations/"+o.ID.String()+"/payment-reference", businesshttp.ObligationPaymentReference{Version: o.Version, PaymentReference: intent, Reason: "Payment Service collection intent"}, 200))
	fake.unavailable = true
	o = commercialData[businesshttp.FinancialObligation](t, c.must(c.owner, "POST", "/financial-obligations/"+o.ID.String()+"/readback", businesshttp.OpsVersionCommand{Version: o.Version, Reason: "Read back"}, 200))
	if o.State != "UNKNOWN_PENDING_READBACK" || o.SettledMinor != 0 {
		t.Fatalf("lost provider response %+v", o)
	}
	fake.unavailable = false
	fake.intents[intent] = payment.Intent{ID: intent, AmountMinor: 20500, Currency: "NGN", State: "SUCCEEDED", SettlementState: "PENDING"}
	o = commercialData[businesshttp.FinancialObligation](t, c.must(c.owner, "POST", "/financial-obligations/"+o.ID.String()+"/readback", businesshttp.OpsVersionCommand{Version: o.Version, Reason: "Read back again"}, 200))
	if o.State != "PAYMENT_REQUESTED" || o.SettledMinor != 0 {
		t.Fatalf("pending collection settled %+v", o)
	}
	fake.intents[intent] = payment.Intent{ID: intent, AmountMinor: 20500, Currency: "NGN", State: "SUCCEEDED", SettlementState: "SETTLED"}
	o = commercialData[businesshttp.FinancialObligation](t, c.must(c.owner, "POST", "/financial-obligations/"+o.ID.String()+"/readback", businesshttp.OpsVersionCommand{Version: o.Version, Reason: "Final readback"}, 200))
	if o.State != "SETTLED" || o.SettledMinor != 20500 {
		t.Fatalf("settlement %+v", o)
	}
	var unknown int
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM financial_exception_cases WHERE source_id=$1 AND kind='PAYMENT_UNKNOWN'`, o.ID).Scan(&unknown); err != nil || unknown != 1 {
		t.Fatalf("unknown exception %d %v", unknown, err)
	}
}

func TestBusinessFinancePayoutDestinationMFAApprovalCoolingOffAndSplitConservation(t *testing.T) {
	c, fake := newS15(t)
	verified := fake.authority(true)
	inactive := fake.authority(false)
	d := commercialData[businesshttp.PayoutDestination](t, c.must(c.owner, "POST", "/payout-destinations", businesshttp.PayoutDestinationCreate{PaymentAuthorityID: verified, Label: "Operating account", Currency: "NGN", Reason: "Reference Payment beneficiary"}, 201))
	if d.State != "PENDING_VERIFICATION" {
		t.Fatal("business metadata treated as validated")
	}
	bad := commercialData[businesshttp.PayoutDestination](t, c.must(c.owner, "POST", "/payout-destinations", businesshttp.PayoutDestinationCreate{PaymentAuthorityID: inactive, Label: "Inactive", Currency: "NGN", Reason: "Reference"}, 201))
	c.must(c.owner, "POST", "/payout-change-requests", businesshttp.PayoutChangeCreate{ToDestinationID: d.ID, Reason: "Unverified destination"}, 409)
	d = commercialData[businesshttp.PayoutDestination](t, c.must(c.owner, "POST", "/payout-destinations/"+d.ID.String()+"/verify", businesshttp.OpsVersionCommand{Version: d.Version, Reason: "Payment readback"}, 200))
	bad = commercialData[businesshttp.PayoutDestination](t, c.must(c.owner, "POST", "/payout-destinations/"+bad.ID.String()+"/verify", businesshttp.OpsVersionCommand{Version: bad.Version, Reason: "Payment readback"}, 200))
	if d.State != "ACTIVE" || bad.State != "REJECTED" {
		t.Fatalf("validation %s %s", d.State, bad.State)
	}
	c.must(c.owner, "POST", "/payout-change-requests", businesshttp.PayoutChangeCreate{ToDestinationID: d.ID, Reason: "Missing approval"}, 403)
	approval := c.companyApproval(approvals.ActionPayoutDestinationChange, "approvals:decide", nil, "", map[string]any{"from_destination_id": nil, "to_destination_id": d.ID, "payment_authority_id": verified})
	change := commercialData[businesshttp.PayoutChange](t, c.must(c.owner, "POST", "/payout-change-requests", businesshttp.PayoutChangeCreate{ToDestinationID: d.ID, ApprovalID: approval, Reason: "Independently approved change"}, 201))
	if err := c.domain.ProcessFinance(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM payout_change_requests WHERE id=$1`, change.ID).Scan(&state); err != nil || state != "COOLING_OFF" {
		t.Fatalf("cooling-off bypassed %s", state)
	}
	if _, err := c.db.Pool.Exec(t.Context(), `UPDATE payout_change_requests SET cooling_off_until=created_at+interval '1 millisecond',version=version+1 WHERE id=$1`, change.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.domain.ProcessFinance(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	var current ids.ID
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT p.current_payout_destination_id FROM financial_profiles p WHERE organisation_id=$1`, c.owner.registered.OrganisationID).Scan(&current); err != nil || current != d.ID {
		t.Fatalf("effective destination %v %v", current, err)
	}
	second := fake.authority(true)
	d2 := commercialData[businesshttp.PayoutDestination](t, c.must(c.owner, "POST", "/payout-destinations", businesshttp.PayoutDestinationCreate{PaymentAuthorityID: second, Label: "Owner share", Currency: "NGN", Reason: "Second beneficiary"}, 201))
	d2 = commercialData[businesshttp.PayoutDestination](t, c.must(c.owner, "POST", "/payout-destinations/"+d2.ID.String()+"/verify", businesshttp.OpsVersionCommand{Version: d2.Version, Reason: "Payment readback"}, 200))
	c.must(c.owner, "POST", "/revenue-splits", businesshttp.RevenueSplitCreate{PropertyID: c.property.ID, ContractReference: "mgmt-agreement-1", Currency: "NGN", Shares: []finance.SplitShare{{DestinationID: d.ID.String(), Basis: "PERCENT_BPS", Value: 6000}, {DestinationID: d2.ID.String(), Basis: "PERCENT_BPS", Value: 3000}}, EffectiveFrom: "2027-01-01", Reason: "Non conserving"}, 400)
	rule := commercialData[businesshttp.RevenueSplitRule](t, c.must(c.owner, "POST", "/revenue-splits", businesshttp.RevenueSplitCreate{PropertyID: c.property.ID, ContractReference: "mgmt-agreement-1", Currency: "NGN", Shares: []finance.SplitShare{{DestinationID: d.ID.String(), Basis: "PERCENT_BPS", Value: 7000}, {DestinationID: d2.ID.String(), Basis: "PERCENT_BPS", Value: 3000}}, EffectiveFrom: "2027-01-01", Reason: "Management agreement split"}, 201))
	splitApproval := c.companyApproval("COMMERCIAL_REVENUE_SPLIT", "finance:approve", nil, "", map[string]any{"rule_id": rule.ID, "shares_digest": rule.SharesDigest})
	c.must(c.owner, "POST", "/revenue-splits/"+rule.ID.String()+"/activate", businesshttp.FinanceApprovalCommand{Version: rule.Version, ApprovalID: splitApproval, Reason: "Approved split"}, 200)
	preview := commercialData[map[string]any](t, c.must(c.owner, "POST", "/revenue-splits/"+rule.ID.String()+"/preview", businesshttp.RevenueSplitPreview{AmountMinor: 10001}, 200))
	if preview["conserved"] != true || preview["allocated_minor"].(float64) != 10001 {
		t.Fatalf("split preview %+v", preview)
	}
}

func TestBusinessFinanceChannelRemittanceReconciliationAndCloseBlocking(t *testing.T) {
	c, _ := newS15(t)
	_, supply := newCommercialSupplyConsumer(t)
	conn := c.connectMarketplace(supply)
	period := commercialData[struct {
		ID      ids.ID `json:"id"`
		Version int64  `json:"version"`
	}](t, c.must(c.owner, "POST", "/fiscal-periods", businesshttp.FiscalPeriodCreate{Code: "CURRENT", StartsOn: time.Now().UTC().AddDate(0, 0, -20).Format(time.DateOnly), EndsOn: time.Now().UTC().AddDate(0, 0, 20).Format(time.DateOnly), Reason: "Current period"}, 201))
	expect := func(ref string, gross, commission, promotion, fees int64, on string) businesshttp.ChannelSettlement {
		return commercialData[businesshttp.ChannelSettlement](t, c.must(c.owner, "POST", "/channel-settlements", businesshttp.ChannelSettlementCreate{PropertyID: c.property.ID, ConnectionID: conn.ID, ProviderRemittanceReference: ref, CollectionModel: "CHANNEL_COLLECT", GrossMinor: gross, CommissionMinor: commission, PromotionMinor: promotion, FeesMinor: fees, Currency: "NGN", ExpectedOn: on, Reason: "Expected remittance"}, 201))
	}
	today := time.Now().UTC().Format(time.DateOnly)
	matched := expect("remit-1", 10000, 1500, 500, 200, today)
	mismatch := expect("remit-2", 10000, 1500, 0, 0, today)
	late := expect("remit-3", 10000, 1000, 0, 0, time.Now().UTC().AddDate(0, 0, -10).Format(time.DateOnly))
	if matched.ExpectedNetMinor != 7800 {
		t.Fatal(matched.ExpectedNetMinor)
	}
	input := func(ref, component string, amount int64) {
		if _, err := c.db.Pool.Exec(t.Context(), `INSERT INTO channel_financial_inputs(id,organisation_id,property_id,connection_id,component,amount_minor,currency,period_from,period_to,source_service,source_reference,observed_at,expires_at,source_payload_hash) VALUES($1,$2,$3,$4,$5,$6,'NGN',current_date-1,current_date,'payment-service',$7,now(),now()+interval '30 days',repeat('c',64))`, ids.New(), c.owner.registered.OrganisationID, c.property.ID, conn.ID, component, amount, ref); err != nil {
			t.Fatal(err)
		}
	}
	input("remit-1", "GROSS_REVENUE", 10000)
	input("remit-1", "COMMISSION", 1500)
	input("remit-1", "PROMOTION_COST", 500)
	input("remit-1", "PAYMENT_FEE", 200)
	input("remit-2", "GROSS_REVENUE", 10000)
	input("remit-2", "COMMISSION", 2000)
	for range 2 {
		if err := c.domain.ProcessFinance(t.Context(), 20); err != nil {
			t.Fatal(err)
		}
	}
	states := map[ids.ID]string{}
	rows, err := c.db.Pool.Query(t.Context(), `SELECT id,state FROM channel_settlement_expectations`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id ids.ID
		var s string
		if err = rows.Scan(&id, &s); err != nil {
			t.Fatal(err)
		}
		states[id] = s
	}
	rows.Close()
	if states[matched.ID] != "MATCHED" || states[mismatch.ID] != "MISMATCH" || states[late.ID] != "LATE" {
		t.Fatalf("reconciliation %+v", states)
	}
	var exceptions int
	if err = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM financial_exception_cases WHERE state='OPEN'`).Scan(&exceptions); err != nil || exceptions != 2 {
		t.Fatalf("exceptions %d %v", exceptions, err)
	}
	c.must(c.owner, "POST", "/fiscal-periods/"+period.ID.String()+"/close", businesshttp.OpsVersionCommand{Version: period.Version, Reason: "Close with open exceptions"}, 409)
	rows, err = c.db.Pool.Query(t.Context(), `SELECT id,version FROM financial_exception_cases WHERE state='OPEN'`)
	if err != nil {
		t.Fatal(err)
	}
	type ex struct {
		id ids.ID
		v  int64
	}
	var list []ex
	for rows.Next() {
		var e ex
		if err = rows.Scan(&e.id, &e.v); err != nil {
			t.Fatal(err)
		}
		list = append(list, e)
	}
	rows.Close()
	for _, e := range list {
		c.must(c.owner, "POST", "/financial-exceptions/"+e.id.String()+"/resolve", businesshttp.FinancialExceptionResolve{Version: e.v, Outcome: "RESOLVED", Resolution: "Channel confirmed corrected remittance"}, 200)
	}
	c.must(c.owner, "POST", "/fiscal-periods/"+period.ID.String()+"/close", businesshttp.OpsVersionCommand{Version: period.Version, Reason: "Close after resolution"}, 200)
	report := commercialData[map[string]any](t, c.must(c.owner, "GET", "/finance/reports", nil, 200))
	if report["ledger_authority"] != "payment-service" {
		t.Fatal(report)
	}
}
