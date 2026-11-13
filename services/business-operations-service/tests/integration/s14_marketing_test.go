package integration

import (
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

func s14Segment(c *commercialHarness) businesshttp.Campaign {
	c.t.Helper()
	key := commercialData[struct {
		SegmentKey ids.ID `json:"segment_key"`
	}](c.t, c.must(c.owner, "POST", "/guest-segments", businesshttp.CRMSegmentCommand{PropertyID: c.property.ID, Name: "Marketing eligible", Kind: "MARKETING_ELIGIBLE", Definition: businesshttp.CRMSegmentDefinition{}, State: "ACTIVE", Reason: "Define a source-backed guest segment"}, 201)).SegmentKey
	c.must(c.owner, "POST", "/guest-segments/"+key.String()+"/evaluate", map[string]any{"reason": "Evaluate against current authoritative records"}, 200)
	seg := commercialData[struct {
		ID ids.ID `json:"id"`
	}](c.t, c.must(c.owner, "POST", "/segments", businesshttp.MarketingSegmentCreate{PropertyID: c.property.ID, GuestSegmentKey: key, Name: "Spring audience", Reason: "Audience from Section 11 segment"}, 201))
	c.grant(c.approver, "guest_privacy:write")
	tpl := notificationTemplate(c, "CAMPAIGN_MESSAGE", "MARKETING")
	return commercialData[businesshttp.Campaign](c.t, c.must(c.owner, "POST", "/campaigns", businesshttp.CampaignCreate{PropertyID: c.property.ID, Name: "Spring offer", SegmentID: seg.ID, TemplateID: tpl.ID, ChannelCategory: "EMAIL", Reason: "Seasonal campaign"}, 201))
}

func TestBusinessCampaignConsentAwareDispatchWithdrawalAndApprovalLimits(t *testing.T) {
	c := newCommercialHTTP(t)
	notifier := newGuestNotifier()
	c.domain.Notification = notifier
	a := contactableGuest(c, "guest-a@example.invalid")
	b := contactableGuest(c, "guest-b@example.invalid")
	crmConsent(c, a, "MARKETING", "GRANTED", 0)
	crmConsent(c, b, "MARKETING", "GRANTED", 0)
	campaign := s14Segment(c)
	// b withdraws after segment evaluation: never treated as eligible.
	crmConsent(c, b, "MARKETING", "WITHDRAWN", 1)
	campaign = commercialData[businesshttp.Campaign](t, c.must(c.owner, "POST", "/campaigns/"+campaign.ID.String()+"/launch", businesshttp.CampaignLaunch{Version: campaign.Version, Reason: "Launch approved seasonal campaign"}, 200))
	if campaign.State != "RUNNING" || campaign.AudienceSize == nil || *campaign.AudienceSize != 2 {
		t.Fatalf("launch %+v", campaign)
	}
	for range 2 {
		if err := c.domain.ProcessCampaigns(t.Context(), 10); err != nil {
			t.Fatal(err)
		}
	}
	var eligible, suppressed, jobs int
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE eligibility='ELIGIBLE'),count(*) FILTER (WHERE eligibility='SUPPRESSED_NO_CONSENT'),count(notification_job_id) FROM campaign_members WHERE campaign_id=$1`, campaign.ID).Scan(&eligible, &suppressed, &jobs); err != nil || eligible != 1 || suppressed != 1 || jobs != 1 {
		t.Fatalf("members eligible=%d suppressed=%d jobs=%d %v", eligible, suppressed, jobs, err)
	}
	// a withdraws before dispatch: the pending send is suppressed, nothing sent.
	crmConsent(c, a, "MARKETING", "WITHDRAWN", 1)
	if err := c.domain.ProcessGuestNotifications(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT j.state,coalesce(j.outcome_reason,'') FROM guest_notification_jobs j JOIN campaign_members m ON m.notification_job_id=j.id WHERE m.campaign_id=$1`, campaign.ID).Scan(&state, &reason); err != nil || state != "SUPPRESSED" || notifier.total() != 0 {
		t.Fatalf("withdrawal did not suppress: %s %s sent=%d %v", state, reason, notifier.total(), err)
	}
	if err := c.domain.ProcessCampaigns(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	report := commercialData[map[string]any](t, c.must(c.owner, "GET", "/campaigns/"+campaign.ID.String()+"/report", nil, 200))
	if report["state"] != "COMPLETED" || report["delivery_confirmed"] != nil || report["accepted_by_notification_service"].(float64) != 0 {
		t.Fatalf("report %+v", report)
	}
	// Budgeted campaigns require an exact independent approval.
	tpl := notificationTemplate(c, "CAMPAIGN_MESSAGE", "MARKETING")
	var segment ids.ID
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT segment_id FROM campaigns WHERE id=$1`, campaign.ID).Scan(&segment); err != nil {
		t.Fatal(err)
	}
	budgeted := commercialData[businesshttp.Campaign](t, c.must(c.owner, "POST", "/campaigns", businesshttp.CampaignCreate{PropertyID: c.property.ID, Name: "Paid push", SegmentID: segment, TemplateID: tpl.ID, ChannelCategory: "EMAIL", BudgetMinor: 50000, Currency: "NGN", Reason: "Budgeted campaign"}, 201))
	c.must(c.owner, "POST", "/campaigns/"+budgeted.ID.String()+"/launch", businesshttp.CampaignLaunch{Version: budgeted.Version, Reason: "Launch without approval"}, 403)
	c.grant(c.approver, "campaigns:approve")
	budget := int64(50000)
	approval := c.approval("COMMERCIAL_CAMPAIGN_LAUNCH", "campaigns:approve", "", &budget, "NGN", map[string]any{"campaign_id": budgeted.ID, "campaign_version": 1, "audience_size": 2})
	c.must(c.owner, "POST", "/campaigns/"+budgeted.ID.String()+"/launch", businesshttp.CampaignLaunch{Version: budgeted.Version, ApprovalID: approval, Reason: "Independently approved budget"}, 200)
}

func TestBusinessLeadsDedupAssignmentConversionAttributionAndFunnel(t *testing.T) {
	c := newCommercialHTTP(t)
	src := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", "/lead-sources", businesshttp.LeadSourceCreate{PropertyID: c.property.ID, Code: "WEBSITE-FORM", Name: "Website enquiry form", Channel: "WEBSITE", Reason: "Approved lead source"}, 201))
	in := businesshttp.LeadCreate{PropertyID: c.property.ID, SourceID: src.ID, ExternalReference: "form-submission-881", Interest: "STAY", Reason: "Captured enquiry"}
	first := commercialData[businesshttp.LeadCreateResult](t, c.must(c.owner, "POST", "/leads", in, 201))
	in.ExternalReference = " FORM-SUBMISSION-881 "
	dup := commercialData[businesshttp.LeadCreateResult](t, c.must(c.owner, "POST", "/leads", in, 200))
	if !dup.Duplicate || dup.Lead.ID != first.Lead.ID {
		t.Fatal("duplicate lead not detected")
	}
	viewer := c.user()
	c.join(viewer, c.owner, []string{"leads:read"})
	c.must(c.owner, "POST", "/lead-assignments", businesshttp.LeadAssignmentCommand{LeadID: first.Lead.ID, Version: first.Lead.Version, AssigneeID: viewer.registered.AccountID, Reason: "Assign to viewer"}, 403)
	lead := commercialData[businesshttp.Lead](t, c.must(c.owner, "POST", "/lead-assignments", businesshttp.LeadAssignmentCommand{LeadID: first.Lead.ID, Version: first.Lead.Version, AssigneeID: c.owner.registered.AccountID, Reason: "Owner follows up"}, 200))
	activity := commercialData[struct {
		Lead businesshttp.Lead `json:"lead"`
	}](t, c.must(c.owner, "POST", "/leads/"+lead.ID.String()+"/activities", businesshttp.LeadActivityCreate{Kind: "CALL", Summary: "Called the guest about dates", OccurredAt: time.Now().UTC()}, 201))
	if activity.Lead.State != "CONTACTED" || activity.Lead.FirstRespondedAt == nil {
		t.Fatalf("response SLA %+v", activity.Lead)
	}
	follow := time.Now().UTC().Add(time.Hour)
	lead = commercialData[businesshttp.Lead](t, c.must(c.owner, "PATCH", "/leads/"+lead.ID.String(), businesshttp.LeadPatch{Version: activity.Lead.Version, FollowUpAt: &follow, Reason: "Schedule follow-up"}, 200))
	if _, err := c.db.Pool.Exec(t.Context(), `UPDATE leads SET follow_up_at=now()-interval '1 minute',version=version+1 WHERE id=$1`, lead.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.domain.ProcessLeadFollowUps(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	var due int
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM lead_activities WHERE lead_id=$1 AND kind='FOLLOW_UP_DUE'`, lead.ID).Scan(&due); err != nil || due != 1 {
		t.Fatalf("follow-up %d %v", due, err)
	}
	if err := c.domain.ProcessLeadFollowUps(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	// Conversion links an actual reservation and creates attribution.
	v, _ := pmsConfirmed(c)
	lead, _ = typedLead(c, lead.ID)
	c.must(c.owner, "POST", "/leads/"+lead.ID.String()+"/convert", businesshttp.LeadConvertCommand{Version: lead.Version, ReservationID: ids.New(), Reason: "Unknown reservation"}, 409)
	lead = commercialData[businesshttp.Lead](t, c.must(c.owner, "POST", "/leads/"+lead.ID.String()+"/convert", businesshttp.LeadConvertCommand{Version: lead.Version, ReservationID: v.Reservation.ID, Reason: "Guest booked"}, 200))
	if lead.State != "CONVERTED" {
		t.Fatal(lead.State)
	}
	report := commercialData[map[string]any](t, c.must(c.owner, "GET", "/conversion?property_id="+c.property.ID.String(), nil, 200))
	stages := map[string]any{}
	for _, s := range report["stages"].([]any) {
		m := s.(map[string]any)
		stages[m["stage"].(string)] = m["count"]
	}
	if stages["IMPRESSION"] != nil || stages["INQUIRY"].(float64) != 1 || stages["RESERVATION"].(float64) < 1 {
		t.Fatalf("funnel %+v", stages)
	}
	if report["rates_bps"].(map[string]any)["listing_view_rate"] != nil {
		t.Fatal("unsourced denominator produced a rate")
	}
	// Owning-source exposure facts arrive only signed from Marketplace.
	token, err := c.supplySigner(platform.MarketplaceService).TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: c.owner.registered.OrganisationID})
	if err != nil {
		t.Fatal(err)
	}
	obs := businesshttp.FunnelObservationCommand{OrganisationID: c.owner.registered.OrganisationID, PropertyID: c.property.ID, Stage: "IMPRESSION", SourceReference: "mp-impressions-day-1", Quantity: 40, ObservedAt: time.Now().UTC()}
	for range 2 {
		if status, _ := c.rootCall("/internal/v1/business-supply/funnel-observations", obs, map[string]string{"Authorization": "Bearer " + token}); status != 202 {
			t.Fatalf("funnel intake %d", status)
		}
	}
	obs.Stage, obs.SourceReference, obs.Quantity = "LISTING_VIEW", "mp-views-day-1", 10
	c.rootCall("/internal/v1/business-supply/funnel-observations", obs, map[string]string{"Authorization": "Bearer " + token})
	obs.Stage = "RESERVATION"
	if status, _ := c.rootCall("/internal/v1/business-supply/funnel-observations", obs, map[string]string{"Authorization": "Bearer " + token}); status != 400 {
		t.Fatalf("Marketplace asserted a canonical stage %d", status)
	}
	report = commercialData[map[string]any](t, c.must(c.owner, "GET", "/conversion?property_id="+c.property.ID.String(), nil, 200))
	if rate := report["rates_bps"].(map[string]any)["listing_view_rate"]; rate == nil || rate.(float64) != 2500 {
		t.Fatalf("sourced impression rate %v", rate)
	}
	// Storefront projection never publishes unpublished supply.
	c.must(c.owner, "POST", "/storefront", businesshttp.StorefrontCreate{PropertyID: c.property.ID, Name: "Direct offers", ListingIDs: []ids.ID{ids.New()}, Reason: "Collection"}, 409)
	other := c.user()
	if status, _ := c.call(other, "GET", "/leads/"+lead.ID.String(), nil, nil); status != 403 && status != 404 {
		t.Fatalf("cross-tenant lead %d", status)
	}
}

func typedLead(c *commercialHarness, id ids.ID) (businesshttp.Lead, error) {
	out := commercialData[struct {
		Lead businesshttp.Lead `json:"lead"`
	}](c.t, c.must(c.owner, "GET", "/leads/"+id.String(), nil, 200))
	return out.Lead, nil
}

func TestBusinessUpsellUsesCanonicalAddOnPriceAndFolioAuthority(t *testing.T) {
	c := newCommercialHTTP(t)
	addon := commercialData[revenue.AddOn](t, c.must(c.owner, "POST", "/add-ons", businesshttp.CommercialAddOnCreate{PropertyID: c.property.ID, Name: "Breakfast", Definition: revenue.AddOnDefinition{Currency: "NGN", AmountMinor: 2500, MaxQuantity: 4, ExecutionReference: "breakfast-menu-v1"}, Reason: "Canonical breakfast add-on"}, 201))
	c.activateOffer(addon.ID, "add_ons", addon.Version, 10000, "NGN")
	cap := 2
	offer := commercialData[businesshttp.UpsellOffer](t, c.must(c.owner, "POST", "/upsell-offers", businesshttp.UpsellOfferCreate{PropertyID: c.property.ID, AddOnID: addon.ID, Category: "BREAKFAST", MinNights: 1, DailyCapacity: &cap, ValidFrom: c.dates.DateFrom, ValidTo: c.dates.DateTo, Reason: "Breakfast upsell"}, 201))
	offer = commercialData[businesshttp.UpsellOffer](t, c.must(c.owner, "POST", "/upsell-offers/"+offer.ID.String()+"/state", businesshttp.OpsStateCommand{Version: offer.Version, State: "ACTIVE", Reason: "Activate offer"}, 200))
	v, _ := pmsConfirmed(c)
	accepted := commercialData[businesshttp.UpsellAcceptance](t, c.must(c.owner, "POST", "/upsell-offers/"+offer.ID.String()+"/acceptances", businesshttp.UpsellAcceptanceCreate{ReservationID: v.Reservation.ID, ServiceDate: c.dates.DateFrom, Quantity: 2, Reason: "Guest accepted breakfast"}, 201))
	if accepted.UnitAmountMinor != 2500 || accepted.AmountMinor != 5000 || accepted.State != "ACCEPTED_PENDING_FOLIO" {
		t.Fatalf("acceptance %+v", accepted)
	}
	c.must(c.owner, "POST", "/upsell-offers/"+offer.ID.String()+"/acceptances", businesshttp.UpsellAcceptanceCreate{ReservationID: v.Reservation.ID, ServiceDate: c.dates.DateFrom, Quantity: 1, Reason: "Exceeds daily capacity"}, 409)
	c.must(c.owner, "POST", "/upsell-acceptances/"+accepted.ID.String()+"/folio-link", businesshttp.UpsellFolioLink{Version: accepted.Version, FolioItemID: ids.New(), Reason: "No such posting"}, 409)
}
