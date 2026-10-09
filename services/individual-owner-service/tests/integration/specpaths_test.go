package integration

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// TestSpecificationSection8PathsAreRouted parses Section 8 of the stored
// specification and requires every listed operation to be routed (directly
// or through a specification-path alias).
func TestSpecificationSection8PathsAreRouted(t *testing.T) {
	raw, err := os.ReadFile("../../../../docs/product/individual-owner/CosmicForge_Real_Estate_Individual_Owner_Backend_Implementation_Spec_v1.0.txt")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start, end := strings.Index(text, "8. Public API surface"), strings.Index(text, "8.1 API command rules")
	if start < 0 || end < start {
		t.Fatal("Section 8 not found")
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	line := regexp.MustCompile(`(?m)^\s*•\s+([A-Z/]+)\s+(/api/v1/owner\S*)`)
	h := newHarness(t)
	routed := map[string]bool{}
	for _, r := range implementedRoutes(t, h) {
		routed[r.Method+" "+param.ReplaceAllString(strings.TrimSuffix(r.Path, "/"), "{}")] = true
	}
	n := 0
	for _, m := range line.FindAllStringSubmatch(text[start:end], -1) {
		for _, method := range strings.Split(m[1], "/") {
			n++
			op := method + " " + param.ReplaceAllString(m[2], "{}")
			if !routed[op] {
				t.Errorf("specification operation not routed: %s", op)
			}
		}
	}
	if n < 140 {
		t.Fatalf("parsed only %d operations", n)
	}
}

func TestSpecificationPathAliases(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)

	// Section 5 aliases share the canonical handlers.
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/forgot-password", body: map[string]any{"email": o.email}}), 202, "")
	var sessions []map[string]any
	r := h.do(call{method: "GET", path: "/api/v1/owner/sessions", token: o.access})
	h.expect(r, 200, "")
	r.data(t, &sessions)
	if len(sessions) == 0 {
		t.Fatal("sessions alias")
	}
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/mfa/enrol", token: o.access}), 201, "")
	h.expect(h.do(call{method: "DELETE", path: "/api/v1/owner/sessions/" + ids.New().String(), token: o.access}), 404, "")

	// Property-scoped short-let paths resolve the property's listing.
	o2, l := h.shortletListing("40 Alias Lane")
	pp := "/properties/" + l.PropertyID.String() + "/shortlet"
	h.expect(h.get(o2, pp+"/settings"), 404, "SHORTLET_NOT_CONFIGURED")
	h.expect(h.write(o2, "PUT", pp+"/settings", 0, shortletSettings("OFF_PLATFORM", true, false)), 200, "")
	var st usecases.ShortletSettingsView
	h.get(o2, "/listings/"+l.ID.String()+"/shortlet/settings").data(t, &st)
	if st.ListingID != l.ID || st.Version != 1 {
		t.Fatalf("alias wrote %+v", st)
	}
	h.expect(h.do(call{method: "PUT", path: api + pp + "/calendar", token: o2.access, key: key(),
		body: map[string]any{"ranges": []map[string]any{{"from": dayN(h.clock.now(), 3), "to": dayN(h.clock.now(), 4), "status": "BLOCKED"}}}}), 200, "")
	days := h.calendarDays(o2, l, dayN(h.clock.now(), 3), dayN(h.clock.now(), 3))
	if days[dayN(h.clock.now(), 3)].Status != "BLOCKED" {
		t.Fatalf("calendar alias %+v", days)
	}
	h.expect(h.get(o2, pp+"/rates"), 200, "")
	h.expect(h.get(h.register(true), pp+"/settings"), 404, "")
	h.expect(h.get(o2, "/properties/"+l.PropertyID.String()+"/shortlet/settings?listing_id="+ids.New().String()), 404, "SHORTLET_LISTING_NOT_FOUND")

	// A second short-let listing on the property makes the path ambiguous.
	r = h.post(o2, "/listings", map[string]any{"property_id": l.PropertyID, "transaction_type": "SHORT_LET", "title": "Second short-let",
		"description": "Another unit for short stays with a separate calendar.",
		"details":     map[string]any{"max_guests": 2, "min_nights": 1, "check_in_time": "14:00", "check_out_time": "11:00"}})
	h.expect(r, 201, "")
	h.expect(h.get(o2, pp+"/settings"), 409, "AMBIGUOUS_SHORTLET_LISTING")
	h.expect(h.get(o2, pp+"/settings?listing_id="+l.ID.String()), 200, "")

	// The damage-claim specification path reaches the same command.
	h.expect(h.post(o2, "/shortlet/bookings/"+ids.New().String()+"/damage-claim", map[string]any{"items": []map[string]any{{"description": "x",
		"amount_minor": 1, "media_ids": []ids.ID{h.media.put(o2.id, "IMAGE", "READY")}}}}), 404, "")
}
