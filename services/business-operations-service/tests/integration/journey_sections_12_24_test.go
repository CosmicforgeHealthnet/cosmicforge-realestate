package integration

import (
	"encoding/json"
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/messaging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// J12: signed/provider-permitted external review → dedup → source history →
// tombstone without evidence loss.
func TestJourneyJ12ExternalReviewImportDedupAndTombstone(t *testing.T) {
	c := newCommercialHTTP(t)
	msg := &messagingDouble{}
	reviews := &reviewAdapterDouble{}
	c.domain.Messaging = map[string]messaging.Adapter{"BOOKING_COM": msg}
	c.domain.ReviewAdapters = map[string]messaging.ReviewAdapter{"BOOKING_COM": reviews}
	conn := commercialData[businesshttp.MessagingConnection](t, c.must(c.owner, "POST", "/messaging-connections", businesshttp.MessagingConnectionCreate{PropertyID: c.property.ID, Provider: "BOOKING_COM", ProviderAccountReference: "bdc-hotel-1", CredentialReference: "secrets/bdc/hotel-1", RetentionDays: 30, SupportsReply: true, SupportsReviews: true, SupportsReviewReplies: true, Reason: "Connect approved OTA reviews"}, 201))
	for _, s := range []string{"AUTHORIZING", "CONNECTED"} {
		conn = commercialData[businesshttp.MessagingConnection](t, c.must(c.owner, "POST", "/messaging-connections/"+conn.ID.String()+"/state", businesshttp.OpsStateCommand{Version: conn.Version, State: s, Reason: "Provider review capability verified"}, 200))
	}
	at := time.Now().UTC().Add(-2 * time.Hour)
	v1 := messaging.ReviewObservation{ExternalReviewID: "bdc-r-1", SourceVersion: 1, RatingValue: 6, RatingScale: 10, Text: "Noisy room at night", TextStoragePermitted: true, ContentReference: "bdc://reviews/1", ReviewedAt: at, ModerationStatus: "PUBLISHED"}
	reviews.pages = []messaging.ReviewPage{{Reviews: []messaging.ReviewObservation{v1, v1}, NextCursor: "r1"}}
	if err := c.domain.ProcessReviewImport(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	deleted := v1
	deleted.SourceVersion, deleted.Deleted = 2, true
	reviews.pages = []messaging.ReviewPage{{Reviews: []messaging.ReviewObservation{deleted}, NextCursor: "r2"}}
	if _, err := c.db.Pool.Exec(t.Context(), `UPDATE review_sync_cursors SET next_poll_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := c.domain.ProcessReviewImport(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	var count, versions int
	var state, excerpt string
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM review_refs),(SELECT count(*) FROM review_versions),(SELECT state FROM review_refs),(SELECT coalesce(content_excerpt,'') FROM review_versions WHERE source_version=1)`).Scan(&count, &versions, &state, &excerpt); err != nil || count != 1 || versions != 2 || state != "TOMBSTONED" || excerpt != "Noisy room at night" {
		t.Fatalf("external review import count=%d versions=%d state=%s excerpt=%q %v", count, versions, state, excerpt, err)
	}
	m := commercialData[map[string]any](t, c.must(c.owner, "GET", "/reputation?property_id="+c.property.ID.String(), nil, 200))
	if m["review_count"].(float64) != 0 {
		t.Fatal("tombstoned review counted in metrics")
	}
}

// J24: checkout → room dirty → turnover task scheduled → cleaning →
// supervisor inspection → room ready.
func TestJourneyJ24CheckoutTurnoverInspectionReadyRoom(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	v, d := pmsCollectedStay(c)
	pmsDeferredDeparture(c, v, d)
	var roomState string
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM room_status_current WHERE room_id=$1`, c.unit.ID).Scan(&roomState); err != nil || roomState != "VACANT_DIRTY" {
		t.Fatalf("checkout room state %s %v", roomState, err)
	}
	for range 2 {
		if err := c.domain.ProcessOperations(t.Context(), 50); err != nil {
			t.Fatal(err)
		}
	}
	list := commercialData[businesshttp.OpsRecordList](t, c.must(c.owner, "GET", "/housekeeping/tasks?property_id="+c.property.ID.String(), nil, 200))
	if len(list.Items) != 1 {
		t.Fatalf("turnover tasks %d", len(list.Items))
	}
	var task businesshttp.OpsRecord
	if err := json.Unmarshal(list.Items[0], &task); err != nil {
		t.Fatal(err)
	}
	if task.Kind != "TURNOVER" || task.SubjectID == nil || *task.SubjectID != c.unit.ID {
		t.Fatalf("turnover %+v", task)
	}
	c.grant(c.approver, "room_status:inspect")
	c.grant(c.approver, "housekeeping:write")
	c.grant(c.approver, "housekeeping:read")
	task = opsMove(c, c.owner, "/housekeeping/tasks", task, businesshttp.OpsRecordTransition{Action: "ASSIGN", AssigneeID: &c.owner.registered.AccountID}, 200)
	task = opsMove(c, c.owner, "/housekeeping/tasks", task, businesshttp.OpsRecordTransition{Action: "START"}, 200)
	task = opsMove(c, c.owner, "/housekeeping/tasks", task, businesshttp.OpsRecordTransition{Action: "SUBMIT_INSPECTION", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	task = opsMove(c, c.approver, "/housekeeping/tasks", task, businesshttp.OpsRecordTransition{Action: "PASS"}, 200)
	if task.State != "DONE" {
		t.Fatal(task.State)
	}
}

// J31: business operating lifecycle across canonical identities — stay,
// guest communication, verified review, quality loop, lead attribution,
// conversion funnel, KPI analytics, asynchronous report and finance coding.
func TestJourneyJ31CompleteBusinessOperatingLifecycle(t *testing.T) {
	f := newS13(t)
	c := f.commercialHarness
	adapter := &messagingDouble{}
	c.domain.Messaging = map[string]messaging.Adapter{"MARKETPLACE": adapter}
	conn := commercialData[businesshttp.MessagingConnection](t, c.must(c.owner, "POST", "/messaging-connections", businesshttp.MessagingConnectionCreate{PropertyID: c.property.ID, Provider: "MARKETPLACE", ProviderAccountReference: "mp-guest-channel", RetentionDays: 7, SupportsReply: true, Reason: "Guest communication channel"}, 201))
	for _, s := range []string{"AUTHORIZING", "CONNECTED"} {
		conn = commercialData[businesshttp.MessagingConnection](t, c.must(c.owner, "POST", "/messaging-connections/"+conn.ID.String()+"/state", businesshttp.OpsStateCommand{Version: conn.Version, State: s, Reason: "Verified"}, 200))
	}
	ctx := commercialData[struct {
		Thread businesshttp.InboxThread `json:"thread"`
	}](t, c.must(c.owner, "POST", "/communication-contexts", businesshttp.CommunicationContextCreate{PropertyID: c.property.ID, ConnectionID: conn.ID, Subject: "Stay follow-up", ExternalConversationRef: "mp-thread-stay", ReservationID: &f.reservation, Reason: "Guest stay conversation"}, 201))
	ai := commercialData[struct {
		Suggestion businesshttp.AISuggestion `json:"suggestion"`
	}](t, c.must(c.owner, "POST", "/ai/recommendations", businesshttp.AIRecommendationRequest{ThreadID: ctx.Thread.ID, Question: "When are the dates of my booking?", Reason: "Grounded reservation answer"}, 201))
	if ai.Suggestion.Kind != "FACTUAL_REPLY" {
		t.Fatalf("reservation-bound answer %+v", ai.Suggestion)
	}
	_, review := f.marketplace(businesshttp.ReviewIntake{PropertyID: c.property.ID, Source: "MARKETPLACE_VERIFIED_STAY", ExternalReviewID: "mp-j31", SourceVersion: 1, ReservationID: &f.reservation, RatingValue: 4, RatingScale: 5, Text: "Lovely stay, friendly staff.", StoreText: true, ContentReference: "marketplace://reviews/mp-j31", ReviewedAt: time.Now().UTC().Add(-time.Hour)}, "j31-review")
	if review.Review.VerificationStatus != "VERIFIED_STAY" {
		t.Fatal("verified stay review missing")
	}
	src := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", "/lead-sources", businesshttp.LeadSourceCreate{PropertyID: c.property.ID, Code: "MP-INQUIRY", Name: "Marketplace inquiry", Channel: "MARKETPLACE", Reason: "Lead source"}, 201))
	lead := commercialData[businesshttp.LeadCreateResult](t, c.must(c.owner, "POST", "/leads", businesshttp.LeadCreate{PropertyID: c.property.ID, SourceID: src.ID, ExternalReference: "mp-inquiry-j31", Interest: "STAY", Reason: "Inquiry"}, 201)).Lead
	c.must(c.owner, "POST", "/leads/"+lead.ID.String()+"/convert", businesshttp.LeadConvertCommand{Version: lead.Version, ReservationID: f.reservation, Reason: "Inquiry became the stay"}, 200)
	report := commercialData[map[string]any](t, c.must(c.owner, "GET", "/conversion?property_id="+c.property.ID.String(), nil, 200))
	stages := map[string]any{}
	for _, s := range report["stages"].([]any) {
		m := s.(map[string]any)
		stages[m["stage"].(string)] = m["count"]
	}
	for _, stage := range []string{"INQUIRY", "RESERVATION", "CHECK_IN", "COMPLETED_STAY", "REVIEW"} {
		if stages[stage] == nil || stages[stage].(float64) < 1 {
			t.Fatalf("funnel stage %s missing: %+v", stage, stages)
		}
	}
	kpis := commercialData[map[string]any](t, c.must(c.owner, "GET", "/dashboards?property_id="+c.property.ID.String()+"&date_from="+c.dates.DateFrom+"&date_to="+c.dates.DateTo, nil, 200))
	if kpis["sold_room_nights"] == nil || kpis["definitions_version"] != "annex-h-v1" {
		t.Fatalf("kpis %+v", kpis)
	}
	job := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", "/reports", businesshttp.AnalyticsReportCreate{PropertyID: c.property.ID, Kind: "COMMAND_CENTER", DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Reason: "Monthly command centre"}, 202))
	if err := c.domain.ProcessReports(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	got := commercialData[map[string]any](t, c.must(c.owner, "GET", "/reports/"+job.ID.String(), nil, 200))
	if got["state"] != "SUCCEEDED" || got["snapshot"] == nil {
		t.Fatalf("report %+v", got)
	}
	cc := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", "/cost-centres", businesshttp.CostCentreCreate{PropertyID: &c.property.ID, Code: "ROOMS", Name: "Rooms division", Reason: "Revenue coding"}, 201))
	c.must(c.owner, "POST", "/financial-obligations", businesshttp.ObligationCreate{PropertyID: &c.property.ID, CostCentreID: cc.ID, Direction: "RECEIVABLE", Category: "FOLIO_DEBT", CounterpartyReference: "guest-folio:" + f.reservation.String(), SourceType: "FOLIO", SourceReference: "folio-debt:" + f.reservation.String(), AmountMinor: 1, Currency: "NGN", DueOn: time.Now().UTC().AddDate(0, 0, 14).Format(time.DateOnly), Reason: "Deferred departure debt coding"}, 201)
	fin := commercialData[map[string]any](t, c.must(c.owner, "GET", "/finance/reports?property_id="+c.property.ID.String(), nil, 200))
	if fin["ledger_authority"] != "payment-service" {
		t.Fatal(fin)
	}
}
