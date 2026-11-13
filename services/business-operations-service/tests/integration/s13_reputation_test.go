package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/messaging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

type reviewAdapterDouble struct {
	mu        sync.Mutex
	replies   map[string]string
	calls     int
	ambiguous int
	pages     []messaging.ReviewPage
}

func (d *reviewAdapterDouble) PollReviews(_ context.Context, _, cursor string) (messaging.ReviewPage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.pages) == 0 {
		return messaging.ReviewPage{NextCursor: cursor}, nil
	}
	p := d.pages[0]
	d.pages = d.pages[1:]
	return p, nil
}
func (d *reviewAdapterDouble) Reply(_ context.Context, _, review, client, _ string) (messaging.MessagingSendResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.replies == nil {
		d.replies = map[string]string{}
	}
	d.replies[client] = "provider-reply-" + review
	if d.ambiguous > 0 {
		d.ambiguous--
		return messaging.MessagingSendResult{}, errors.New("connection reset after write")
	}
	return messaging.MessagingSendResult{ProviderMessageID: d.replies[client]}, nil
}
func (d *reviewAdapterDouble) ReadbackReply(_ context.Context, _, _, client string) (messaging.MessagingSendResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if v, ok := d.replies[client]; ok {
		return messaging.MessagingSendResult{ProviderMessageID: v}, nil
	}
	return messaging.MessagingSendResult{}, messaging.ErrNotFound
}
func (d *reviewAdapterDouble) Retract(context.Context, string, string, string) error { return nil }

type s13Fixture struct {
	*commercialHarness
	connection  ids.ID
	stayID      ids.ID
	reservation ids.ID
	token       string
	adapter     *reviewAdapterDouble
}

func newS13(t *testing.T) *s13Fixture {
	c := newCommercialHTTPForArrival(t, 0)
	_, supply := newCommercialSupplyConsumer(t)
	conn := c.connectMarketplace(supply)
	v, d := pmsCollectedStay(c)
	pmsDeferredDeparture(c, v, d)
	token, e := c.supplySigner(platform.MarketplaceService).TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: c.owner.registered.OrganisationID})
	if e != nil {
		t.Fatal(e)
	}
	f := &s13Fixture{commercialHarness: c, connection: conn.ID, stayID: d.Stay.ID, reservation: v.Reservation.ID, token: token, adapter: &reviewAdapterDouble{}}
	c.domain.ReviewAdapters = map[string]messaging.ReviewAdapter{"MARKETPLACE_VERIFIED_STAY": f.adapter}
	return f
}

func (f *s13Fixture) marketplace(review businesshttp.ReviewIntake, key string) (int, businesshttp.ReviewIntakeResult) {
	f.t.Helper()
	status, raw := f.rootCall("/internal/v1/business-supply/reviews", businesshttp.InternalReviewCommand{OrganisationID: f.owner.registered.OrganisationID, ConnectionID: f.connection, Review: review}, map[string]string{"Authorization": "Bearer " + f.token, "Idempotency-Key": key})
	if status >= 300 {
		return status, businesshttp.ReviewIntakeResult{}
	}
	return status, commercialData[businesshttp.ReviewIntakeResult](f.t, raw)
}

func TestBusinessReviewsVerifiedIntakeDedupHistoryQualityLoopAndMetrics(t *testing.T) {
	f := newS13(t)
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	unrelated := ids.New()
	bad := businesshttp.ReviewIntake{PropertyID: f.property.ID, Source: "MARKETPLACE_VERIFIED_STAY", ExternalReviewID: "mp-r-0", SourceVersion: 1, ReservationID: &unrelated, RatingValue: 5, RatingScale: 5, ContentReference: "marketplace://reviews/mp-r-0", ReviewedAt: now}
	if status, _ := f.marketplace(bad, "bad-review"); status != 403 {
		t.Fatalf("fabricated verified stay accepted %d", status)
	}
	if status, _ := f.rootCall("/internal/v1/business-supply/reviews", businesshttp.InternalReviewCommand{OrganisationID: f.owner.registered.OrganisationID, ConnectionID: f.connection, Review: bad}, map[string]string{"Authorization": "Bearer " + f.owner.tokens.AccessToken}); status != 401 {
		t.Fatalf("account token reached signed intake %d", status)
	}
	review := businesshttp.ReviewIntake{PropertyID: f.property.ID, Source: "MARKETPLACE_VERIFIED_STAY", ExternalReviewID: "mp-r-1", SourceVersion: 1, ReservationID: &f.reservation, RatingValue: 2, RatingScale: 5, Dimensions: []businesshttp.ReviewDimension{{Dimension: "CLEANLINESS", Value: 1, Scale: 5}}, Text: "The room was dirty. The bathroom was dirty and had a smell.", StoreText: true, ContentReference: "marketplace://reviews/mp-r-1", ReviewedAt: now}
	status, first := f.marketplace(review, "review-v1")
	if status != 202 || first.Review.VerificationStatus != "VERIFIED_STAY" || first.Signals == 0 || len(first.QualityTasks) != 0 {
		t.Fatalf("verified intake %d %+v", status, first)
	}
	if _, again := f.marketplace(review, "review-v1-other-key"); !again.Duplicate {
		t.Fatal("duplicate source version not detected")
	}
	review.SourceVersion, review.RatingValue = 2, 3
	if _, v2 := f.marketplace(review, "review-v2"); v2.Review.SourceVersion != 2 || v2.Review.RatingBPS != 6000 {
		t.Fatalf("source update %+v", v2.Review)
	}
	review.SourceVersion = 1
	if _, stale := f.marketplace(review, "review-v1-late"); !stale.Duplicate || stale.Review.SourceVersion != 2 {
		t.Fatal("late older version regressed the review")
	}
	var versions int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM review_versions WHERE review_id=$1`, first.Review.ID).Scan(&versions); err != nil || versions != 2 {
		t.Fatalf("immutable history %d %v", versions, err)
	}
	// Direct guest feedback from the departed stay repeats cleanliness: one
	// quality task for housekeeping, never duplicated by redelivery.
	direct := businesshttp.ReviewIntake{PropertyID: f.property.ID, Source: "DIRECT_FEEDBACK", ExternalReviewID: "front-desk-feedback-1", SourceVersion: 1, StayID: &f.stayID, RatingValue: 2, RatingScale: 5, Text: "Dirty towels and a dirty floor on arrival.", StoreText: true, ContentReference: "feedback-form:1", ReviewedAt: now, Reason: "Guest feedback captured at checkout"}
	recorded := commercialData[businesshttp.ReviewIntakeResult](t, f.must(f.owner, "POST", "/reviews", direct, 201))
	if len(recorded.QualityTasks) != 1 {
		t.Fatalf("repeated issue did not create exactly one task %+v", recorded)
	}
	f.must(f.owner, "POST", "/reviews", direct, 200)
	direct.ExternalReviewID, direct.Text = "front-desk-feedback-2", "Room was dirty again."
	f.must(f.owner, "POST", "/reviews", direct, 201)
	var tasks int
	var team string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*),max(team) FROM review_action_tasks WHERE property_id=$1`, f.property.ID).Scan(&tasks, &team); err != nil || tasks != 1 || team != "HOUSEKEEPING" {
		t.Fatalf("quality task dedup %d %s %v", tasks, team, err)
	}
	if status, _ := f.call(f.owner, "POST", "/reviews", businesshttp.ReviewIntake{PropertyID: f.property.ID, Source: "TRIPADVISOR", ExternalReviewID: "x", SourceVersion: 1, RatingValue: 5, RatingScale: 5, ContentReference: "x", ReviewedAt: now, Reason: "Unsupported source"}, nil); status != 400 {
		t.Fatalf("unsupported source accepted %d", status)
	}
	// Topic correction never rewrites the original evidence.
	var signal ids.ID
	var digest string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT s.id,r.content_digest FROM review_topic_signals s JOIN review_refs r ON r.id=s.review_id WHERE s.review_id=$1 AND s.topic='CLEANLINESS' LIMIT 1`, first.Review.ID).Scan(&signal, &digest); err != nil {
		t.Fatal(err)
	}
	f.must(f.owner, "POST", "/reviews/"+first.Review.ID.String()+"/topics/"+signal.String()+"/review", businesshttp.ReviewTopicReview{Version: 1, Action: "CORRECT", CorrectedSentiment: "NEUTRAL", Reason: "Operator correction of machine signal"}, 200)
	var after string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT content_digest FROM review_refs WHERE id=$1`, first.Review.ID).Scan(&after); err != nil || after != digest {
		t.Fatal("original review rewritten")
	}
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE review_refs SET content_digest=repeat('b',64),version=version+1 WHERE id=$1`, first.Review.ID); err == nil {
		t.Fatal("same-version evidence rewrite allowed")
	}
	// Metrics count each review once from its latest source version.
	m := commercialData[map[string]any](t, f.must(f.owner, "GET", "/reputation?property_id="+f.property.ID.String(), nil, 200))
	if m["review_count"].(float64) != 3 || int(m["average_rating_bps"].(float64)) != 4666 {
		t.Fatalf("metrics %+v", m)
	}
	if corr := m["correlations"].(map[string]any); corr["conversion"] != nil || corr["conversion_unavailable_reason"] == "" {
		t.Fatalf("conversion fabricated %+v", corr)
	}
	f.must(f.owner, "POST", "/reputation/snapshots", businesshttp.ReputationSnapshotCommand{PropertyID: f.property.ID, DateFrom: now.AddDate(0, 0, -30).Format(time.DateOnly), DateTo: now.Format(time.DateOnly), Reason: "Monthly reputation snapshot"}, 201)
	// Cross-organisation isolation.
	other := f.user()
	if status, _ := f.call(other, "GET", "/reviews/"+first.Review.ID.String(), nil, nil); status != 403 && status != 404 {
		t.Fatalf("cross-tenant review %d", status)
	}
}

func TestBusinessReviewResponsesPolicyApprovalAmbiguousReadbackAndDisputes(t *testing.T) {
	f := newS13(t)
	now := time.Now().UTC().Add(-time.Hour)
	_, low := f.marketplace(businesshttp.ReviewIntake{PropertyID: f.property.ID, Source: "MARKETPLACE_VERIFIED_STAY", ExternalReviewID: "mp-low", SourceVersion: 1, ReservationID: &f.reservation, RatingValue: 1, RatingScale: 5, Text: "Staff were rude.", ContentReference: "marketplace://reviews/mp-low", ReviewedAt: now}, "low")
	f.must(f.owner, "POST", "/reviews/"+low.Review.ID.String()+"/reply", businesshttp.ReviewReplyCommand{Body: "Please email us at manager@example.com", Reason: "Reply"}, 409)
	resp := commercialData[businesshttp.ReviewResponse](t, f.must(f.owner, "POST", "/reviews/"+low.Review.ID.String()+"/reply", businesshttp.ReviewReplyCommand{Body: "We are sorry about your experience and have retrained the team.", Reason: "Respond to a negative review"}, 201))
	if !resp.RequiresApproval {
		t.Fatal("negative review response must require approval")
	}
	f.must(f.owner, "POST", "/review-responses/"+resp.ID.String()+"/submit", businesshttp.ReviewResponseSubmit{Version: resp.Version, Reason: "Submit without approval"}, 403)
	f.grant(f.approver, "reviews:moderate")
	approval := f.approval("COMMERCIAL_REVIEW_RESPONSE", "reviews:moderate", "", nil, "", map[string]any{"response_id": resp.ID, "body_digest": security.SHA256Hex([]byte(resp.Body))})
	resp = commercialData[businesshttp.ReviewResponse](t, f.must(f.owner, "POST", "/review-responses/"+resp.ID.String()+"/submit", businesshttp.ReviewResponseSubmit{Version: resp.Version, ApprovalID: approval, Reason: "Independently approved response"}, 200))
	f.adapter.ambiguous = 1
	if err := f.domain.ProcessReviewResponses(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT state FROM review_responses WHERE id=$1`, resp.ID).Scan(&state); err != nil || state != "UNKNOWN_PENDING_READBACK" {
		t.Fatalf("ambiguous provider outcome reported as %s", state)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE review_responses SET next_attempt_at=now()-interval '1 second',version=version+1 WHERE id=$1`, resp.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.domain.ProcessReviewResponses(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	var reviewState string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT r.state,v.response_state FROM review_responses r JOIN review_refs v ON v.id=r.review_id WHERE r.id=$1`, resp.ID).Scan(&state, &reviewState); err != nil || state != "PUBLISHED" || reviewState != "PUBLISHED" || f.adapter.calls != 1 {
		t.Fatalf("readback %s/%s calls=%d %v", state, reviewState, f.adapter.calls, err)
	}
	// Direct feedback has no provider reply channel.
	_, d := f.marketplace(businesshttp.ReviewIntake{PropertyID: f.property.ID, Source: "MARKETPLACE_VERIFIED_STAY", ExternalReviewID: "mp-abuse", SourceVersion: 1, ReservationID: &f.reservation, RatingValue: 1, RatingScale: 5, Text: "Abusive content", StoreText: true, ContentReference: "marketplace://reviews/mp-abuse", ReviewedAt: now}, "abuse")
	direct := commercialData[businesshttp.ReviewIntakeResult](t, f.must(f.owner, "POST", "/reviews", businesshttp.ReviewIntake{PropertyID: f.property.ID, Source: "DIRECT_FEEDBACK", ExternalReviewID: "fd-9", SourceVersion: 1, StayID: &f.stayID, RatingValue: 5, RatingScale: 5, ContentReference: "feedback:9", ReviewedAt: now, Reason: "Captured feedback"}, 201))
	f.must(f.owner, "POST", "/reviews/"+direct.Review.ID.String()+"/reply", businesshttp.ReviewReplyCommand{Body: "Thank you very much for staying with us.", Reason: "Reply"}, 409)
	// Dispute keeps original evidence under legal hold.
	media := ids.New()
	dispute := commercialData[businesshttp.ReviewDispute](t, f.must(f.owner, "POST", "/reviews/"+d.Review.ID.String()+"/disputes", businesshttp.ReviewDisputeCreate{ReasonCode: "ABUSIVE", Route: "PROVIDER", Statement: "The review contains abusive language against staff.", EvidenceRefs: []ids.ID{media}, Reason: "Moderation request"}, 201))
	var hold bool
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT legal_hold FROM review_refs WHERE id=$1`, d.Review.ID).Scan(&hold); err != nil || !hold {
		t.Fatal("dispute did not place evidence on hold")
	}
	dispute = commercialData[businesshttp.ReviewDispute](t, f.must(f.owner, "POST", "/review-disputes/"+dispute.ID.String()+"/transitions", businesshttp.ReviewDisputeTransition{Version: dispute.Version, Action: "SUBMIT", Reason: "Submit to provider"}, 200))
	f.must(f.owner, "POST", "/review-disputes/"+dispute.ID.String()+"/transitions", businesshttp.ReviewDisputeTransition{Version: dispute.Version, Action: "RECORD_DECISION", Outcome: "UPHELD", Decision: "Cannot decide before acknowledgement", Reason: "Out of order"}, 409)
	dispute = commercialData[businesshttp.ReviewDispute](t, f.must(f.owner, "POST", "/review-disputes/"+dispute.ID.String()+"/transitions", businesshttp.ReviewDisputeTransition{Version: dispute.Version, Action: "RECORD_ACKNOWLEDGEMENT", ExternalReference: "provider-case-123", Reason: "Provider acknowledged"}, 200))
	dispute = commercialData[businesshttp.ReviewDispute](t, f.must(f.owner, "POST", "/review-disputes/"+dispute.ID.String()+"/transitions", businesshttp.ReviewDisputeTransition{Version: dispute.Version, Action: "RECORD_DECISION", Outcome: "UPHELD", Decision: "Provider removed the review for abuse", Reason: "Provider decision"}, 200))
	var excerpt *string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT legal_hold,content_excerpt FROM review_refs WHERE id=$1`, d.Review.ID).Scan(&hold, &excerpt); err != nil || hold || excerpt == nil || *excerpt != "Abusive content" {
		t.Fatalf("evidence after decision hold=%v excerpt=%v %v", hold, excerpt, err)
	}
	if !strings.HasPrefix(dispute.State, "UPHELD") {
		t.Fatal(dispute.State)
	}
}

// grant adds a company-scoped capability to an existing member.
func (h *businessHarness) grant(u *businessUser, capability string) {
	h.t.Helper()
	if _, err := h.db.Pool.Exec(h.t.Context(), `INSERT INTO staff_scope_grants(id,organisation_id,membership_id,capability,scope_kind,scope_id,inherit,effective_at) SELECT $1,m.organisation_id,m.id,$3,'COMPANY',m.organisation_id,true,now() FROM staff_memberships m WHERE m.account_id=$2 AND m.organisation_id<>(SELECT organisation_id FROM staff_memberships WHERE account_id=$2 AND kind='OWNER' LIMIT 1)`, ids.New(), u.registered.AccountID, capability); err != nil {
		h.t.Fatal(err)
	}
}
