package businesshttp

import (
	"math"
	"testing"
	"time"
)

func TestChannelProfitabilityDoesNotTurnMissingStaleOrUnreconciledInputsIntoZero(t *testing.T) {
	now := time.Now().UTC()
	inputs := []CommercialFinancialInput{}
	missing, e := commercialProfitability(inputs, "NGN", now)
	if e != nil || missing["net_contribution_minor"] != (*int64)(nil) {
		t.Fatalf("Missing inputs produced a net: %#v %v", missing, e)
	}
	for i, name := range []string{"GROSS_REVENUE", "COMMISSION", "PAYMENT_FEE", "PROMOTION_COST", "OPERATIONAL_COST"} {
		amount := int64(100)
		if i == 0 {
			amount = 10000
		}
		inputs = append(inputs, CommercialFinancialInput{Component: name, AmountMinor: amount, Currency: "NGN", SourceService: "signed-owning-service", SourceReference: name + ":period-2027-01", ObservedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Reconciled: true})
	}
	result, e := commercialProfitability(inputs, "NGN", now)
	if e != nil {
		t.Fatal(e)
	}
	net, ok := result["net_contribution_minor"].(*int64)
	if !ok || net == nil || *net != 9600 || result["status"] != "RECONCILED_SOURCE_INPUTS" {
		t.Fatalf("Exact net %#v", result)
	}
	inputs[1].ExpiresAt = now.Add(-time.Second)
	result, e = commercialProfitability(inputs, "NGN", now)
	if e != nil || result["net_contribution_minor"] != (*int64)(nil) {
		t.Fatalf("Stale input net %#v %v", result, e)
	}
	inputs[1].ExpiresAt = now.Add(time.Hour)
	inputs[2].Reconciled = false
	result, e = commercialProfitability(inputs, "NGN", now)
	if e != nil || result["net_contribution_minor"] != (*int64)(nil) {
		t.Fatalf("Unreconciled input net %#v %v", result, e)
	}
	inputs[2].Reconciled = true
	inputs[0].Currency = "USD"
	if _, e = commercialProfitability(inputs, "NGN", now); e == nil {
		t.Fatal("Mixed currencies were silently converted")
	}
	inputs[0].Currency = "NGN"
	inputs[0].AmountMinor = math.MaxInt64
	inputs = append(inputs, inputs[0])
	if _, e = commercialProfitability(inputs, "NGN", now); e == nil {
		t.Fatal("Overflowing source totals were accepted")
	}
}
