// contracts is a build-time DTO reflection tool, never an HTTP surface.
package main

import (
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"os"
	"reflect"
	"strings"
	"time"
)

func schema(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Pointer {
		return schema(t.Elem())
	}
	if t == reflect.TypeFor[ids.ID]() {
		return map[string]any{"type": "string", "format": "uuid"}
	}
	if t == reflect.TypeFor[time.Time]() {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer", "format": "int64"}
	case reflect.Slice:
		return map[string]any{"type": []string{"array", "null"}, "items": schema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": []string{"object", "null"}, "additionalProperties": schema(t.Elem())}
	case reflect.Struct:
		props := map[string]any{}
		required := []string{}
		for n := 0; n < t.NumField(); n++ {
			f := t.Field(n)
			if f.Anonymous {
				embedded := schema(f.Type)
				for k, v := range embedded["properties"].(map[string]any) {
					props[k] = v
				}
				required = append(required, embedded["required"].([]string)...)
				continue
			}
			tag := strings.Split(f.Tag.Get("json"), ",")
			if tag[0] == "" || tag[0] == "-" {
				continue
			}
			props[tag[0]] = schema(f.Type)
			if len(tag) == 1 {
				required = append(required, tag[0])
			}
		}
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	panic("unsupported DTO type: " + t.String())
}
func main() {
	types := []any{finance.StateChange{}, lifecycle.RecoverableAction{}, lifecycle.CaseOverride{}, lifecycle.ReconciliationResolution{}, lifecycle.Cancellation{}, lifecycle.Family{}, lifecycle.Policy{}, lifecycle.Authority{}, lifecycle.Component{}, lifecycle.Quote{}, lifecycle.LineInput{}, lifecycle.AllocationInput{}, lifecycle.AllocationLine{}, lifecycle.Allocation{}, lifecycle.FundingInput{}, lifecycle.Evidence{}, lifecycle.Release{}, lifecycle.Hold{}, lifecycle.Destination{}, lifecycle.WithdrawalApproval{}, lifecycle.Withdrawal{}, lifecycle.Payout{}, lifecycle.Agreement{}, lifecycle.RentRequest{}, lifecycle.Obligation{}, lifecycle.Receipt{}, lifecycle.PolicySelector{}, lifecycle.PolicyDeactivation{}, lifecycle.RemedyPolicy{}, lifecycle.RemedyEvidence{}, lifecycle.Deposit{}, lifecycle.CaseRequest{}, lifecycle.FinancialCase{}, lifecycle.CaseFact{}, lifecycle.CaseDecision{}, lifecycle.CaseHistoryFact{}, lifecycle.RefundRequest{}, lifecycle.RefundAttempt{}, lifecycle.ReversalRequest{}, lifecycle.ReversalRecord{}, lifecycle.DestinationChange{}, lifecycle.Availability{}, lifecycle.TopupRequest{}, lifecycle.Topup{}, lifecycle.TopupFunding{}, lifecycle.WalletSpend{}, lifecycle.RecurringDesign{}, lifecycle.ReconciliationRequest{}, lifecycle.ReconciliationFinding{}, lifecycle.ReconciliationRun{}}
	out := map[string]any{}
	for _, v := range types {
		t := reflect.TypeOf(v)
		out[t.Name()] = schema(t)
	}
	if e := json.NewEncoder(os.Stdout).Encode(out); e != nil {
		panic(e)
	}
}
