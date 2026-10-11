package usecases

import (
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"testing"
)

func TestAcceptedRentComponentsPreserveFeesAndRejectInitialCharges(t *testing.T) {
	samples := []struct {
		body     string
		expected int64
		valid    bool
	}{
		{`{"amount_minor":380,"components":[{"kind":"RENT","amount_minor":360,"recurrence":"RECURRING"},{"kind":"SERVICE","amount_minor":20,"recurrence":"RECURRING"}]}`, 380, true},
		{`{"amount_minor":360,"components":[{"kind":"RENT","amount_minor":360,"recurrence":"RECURRING"}]}`, 380, false},
		{`{"amount_minor":380,"components":[{"kind":"RENT","amount_minor":360,"recurrence":"RECURRING"},{"kind":"ONBOARDING","amount_minor":20,"recurrence":"ONE_TIME"}]}`, 380, false},
		{`{"amount_minor":380,"components":[{"kind":"RENT","amount_minor":360,"recurrence":"RECURRING"},{"kind":"DEPOSIT","amount_minor":20,"recurrence":"REFUNDABLE_DEPOSIT"}]}`, 380, false},
		{`{"amount_minor":380,"components":[{"kind":"RENT","amount_minor":360,"recurrence":"RECURRING"},{"kind":"SERVICE","amount_minor":21,"recurrence":"RECURRING"}]}`, 380, false},
		{`{"amount_minor":380,"components":[{"kind":"RENT","amount_minor":360,"recurrence":"RECURRING"},{"kind":"SERVICE","amount_minor":20,"recurrence":"ONE_TIME"}]}`, 380, false},
	}
	for _, sample := range samples {
		var q payment.AcceptedQuote
		if e := json.Unmarshal([]byte(sample.body), &q); e != nil {
			t.Fatal(e)
		}
		if acceptedRentComponents(q, sample.expected) != sample.valid {
			t.Fatal("incorrect accepted fee/rent context", sample.body)
		}
	}
}
