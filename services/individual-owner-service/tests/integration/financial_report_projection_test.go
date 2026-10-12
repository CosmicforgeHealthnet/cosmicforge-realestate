package integration

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"testing"
	"time"
)

// Only report math is seeded here. This is not collection/settlement evidence;
// actual Core truth is certified by the existing separate-process harness.
func TestFinancialReportsExcludeRefundedRent(t *testing.T) {
	h := newHarness(t, withPayment)
	o, l := h.liveListing("Refund Report Street")
	tenant := customer()
	h.acceptedOffer(o, l, tenant, day(h.clock.now(), 0))
	g := h.agreementOf(o, l.PropertyID, "RENTAL_OFFER")
	g = h.prepareDraft(o, g, h.agreementTemplate("RESIDENTIAL_LEASE"), nil)
	g = h.signOffline(o, tenant, g)
	h.post(o, "/agreements/"+g.ID.String()+"/activate", nil).data(t, &g)
	var obligations []usecases.RentObligationView
	h.get(o, "/tenancies/"+g.TenancyID.String()+"/rent-obligations").data(t, &obligations)
	r := obligations[0]
	if _, e := h.db.Pool.Exec(context.Background(), `UPDATE rent_obligations SET paid_minor=amount_minor,refunded_minor=10000000,status='REFUND_PENDING' WHERE id=$1`, r.ID); e != nil {
		t.Fatal(e)
	}
	var rep usecases.PortfolioReport
	h.get(o, "/reports/portfolio?from="+r.DueDate+"&to="+r.DueDate).data(t, &rep)
	if rep.RentCollected["NGN"] != r.AmountMinor-10000000 || rep.NetIncome["NGN"] != r.AmountMinor-10000000 {
		t.Fatal("refunded rent remains reported income", rep)
	}
}

func TestFinancialReportsExcludeProtectedStayDeposits(t *testing.T) {
	h := newHarness(t, withPayment)
	o, l := h.shortletListing("Protected Deposit Report Street")
	pricing := map[string]any{"currency": "NGN", "base_amount_minor": 5000000, "base_period": "NIGHT", "payment_cycle": "PER_STAY", "deposit_amount_minor": 1000000}
	h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/pricing", int64(l.Pricing.VersionNo), pricing), 200, "")
	h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/shortlet/settings", 0, shortletSettings("PLATFORM", true, false)), 200, "")
	now := h.clock.now()
	b := h.requestBooking(customer(), bookingBody(l, dayN(now, 1), dayN(now, 3), "Guest"))
	h.runJobs("shortlet-bookings-calendar-and-claims")
	h.pay.settle(b.ID, b.TotalMinor, payment.IntentHeld)
	h.clock.advance(time.Second)
	h.runJobs("shortlet-bookings-calendar-and-claims")
	var rep usecases.PortfolioReport
	h.get(o, "/reports/portfolio?from="+dayN(now, 0)+"&to="+dayN(now, 4)).data(t, &rep)
	if rep.Shortlet.Revenue["NGN"] != 10000000 || rep.NetIncome["NGN"] != 10000000 {
		t.Fatal("protected security deposit counted as stay income", rep)
	}
}
