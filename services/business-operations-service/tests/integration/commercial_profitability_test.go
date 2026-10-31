package integration

import (
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

func TestBusinessCommercialProfitabilitySignedSourceInputsAndMissingness(t *testing.T) {
	c := newCommercialHTTP(t)
	_, adapter := newCommercialSupplyConsumer(t)
	conn := c.connectMarketplace(adapter)
	now := time.Now().UTC()
	from, to := now.AddDate(0, 0, -2).Format(time.DateOnly), now.Format(time.DateOnly)
	path := "/channels/" + conn.ID.String() + "/profitability?date_from=" + from + "&date_to=" + to + "&currency=NGN"
	missing := commercialData[struct {
		Status  string   `json:"status"`
		Net     *int64   `json:"net_contribution_minor"`
		Missing []string `json:"missing_components"`
	}](t, c.must(c.owner, "GET", path, nil, 200))
	if missing.Net != nil || len(missing.Missing) != 5 || missing.Status != "INCOMPLETE_INPUTS" {
		t.Fatalf("Missing source facts were invented %#v", missing)
	}
	payment, admin := c.supplySigner(platform.PaymentService), c.supplySigner(platform.AdminService)
	org := c.owner.registered.OrganisationID
	paymentToken, e := payment.TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: org})
	if e != nil {
		t.Fatal(e)
	}
	adminToken, e := admin.TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: org})
	if e != nil {
		t.Fatal(e)
	}
	for i, component := range []string{"GROSS_REVENUE", "COMMISSION", "PAYMENT_FEE", "PROMOTION_COST", "OPERATIONAL_COST"} {
		source, token := platform.PaymentService, paymentToken
		if i > 2 {
			source, token = platform.AdminService, adminToken
		}
		amount := int64(100)
		if i == 0 {
			amount = 10000
		}
		in := businesshttp.CommercialFinancialInputCommand{OrganisationID: org, ConnectionID: conn.ID, PeriodFrom: from, PeriodTo: to, Input: businesshttp.CommercialFinancialInput{Component: component, AmountMinor: amount, Currency: "NGN", SourceService: string(source), SourceReference: "disposable-source:" + component, ObservedAt: now, ExpiresAt: now.Add(time.Hour), Reconciled: true}}
		headers := map[string]string{"Authorization": "Bearer " + token}
		status, raw := c.rootCall("/internal/v1/business-supply/channel-financial-inputs", in, headers)
		if status != 201 {
			t.Fatalf("Source input %s %d %s", component, status, raw)
		}
		again, replay := c.rootCall("/internal/v1/business-supply/channel-financial-inputs", in, headers)
		if again != 201 || string(replay) != string(raw) {
			t.Fatalf("Source input replay changed identity %d %s", again, replay)
		}
		in.Input.AmountMinor++
		if status, _ = c.rootCall("/internal/v1/business-supply/channel-financial-inputs", in, headers); status != 409 {
			t.Fatalf("Changed source fact accepted %d", status)
		}
	}
	report := commercialData[struct {
		Status     string                                  `json:"status"`
		Net        *int64                                  `json:"net_contribution_minor"`
		Provenance []businesshttp.CommercialFinancialInput `json:"provenance"`
	}](t, c.must(c.owner, "GET", path, nil, 200))
	if report.Status != "RECONCILED_SOURCE_INPUTS" || report.Net == nil || *report.Net != 9600 || len(report.Provenance) != 5 {
		t.Fatalf("Source-backed contribution %#v", report)
	}
	forged := businesshttp.CommercialFinancialInputCommand{OrganisationID: org, ConnectionID: conn.ID, PeriodFrom: from, PeriodTo: to, Input: businesshttp.CommercialFinancialInput{Component: "GROSS_REVENUE", AmountMinor: 999999, Currency: "NGN", SourceService: string(platform.AdminService), SourceReference: "must-not-be-accepted", ObservedAt: now, ExpiresAt: now.Add(time.Hour), Reconciled: true}}
	if status, _ := c.rootCall("/internal/v1/business-supply/channel-financial-inputs", forged, map[string]string{"Authorization": "Bearer " + adminToken}); status != 403 {
		t.Fatalf("Non-owning financial authority accepted %d", status)
	}
}
