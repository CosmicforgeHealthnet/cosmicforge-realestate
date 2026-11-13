package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/messaging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reputation"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func init() {
	registerOpsTable("review_refs", "review_responses", "review_topic_signals", "quality_issues", "review_action_tasks", "review_disputes")
}

type ReviewDimension struct {
	Dimension string `json:"dimension"`
	Value     int    `json:"value"`
	Scale     int    `json:"scale"`
}

// ReviewIntake is one authoritative review observation from its source.
type ReviewIntake struct {
	PropertyID       ids.ID            `json:"property_id"`
	Source           string            `json:"source"`
	ExternalReviewID string            `json:"external_review_id"`
	SourceVersion    int64             `json:"source_version"`
	ReservationID    *ids.ID           `json:"reservation_id,omitempty"`
	StayID           *ids.ID           `json:"stay_id,omitempty"`
	UnitID           *ids.ID           `json:"unit_id,omitempty"`
	RatingValue      int               `json:"rating_value"`
	RatingScale      int               `json:"rating_scale"`
	Dimensions       []ReviewDimension `json:"dimensions,omitempty"`
	Text             string            `json:"text,omitempty"`
	StoreText        bool              `json:"store_text"`
	ContentReference string            `json:"content_reference"`
	Language         string            `json:"language,omitempty"`
	ReviewedAt       time.Time         `json:"reviewed_at"`
	ModerationStatus string            `json:"moderation_status,omitempty"`
	Deleted          bool              `json:"deleted"`
	Reason           string            `json:"reason,omitempty"`
}

type ReviewRef struct {
	ID                 ids.ID    `json:"id"`
	PropertyID         ids.ID    `json:"property_id"`
	ReservationID      *ids.ID   `json:"reservation_id"`
	StayID             *ids.ID   `json:"stay_id"`
	Source             string    `json:"source"`
	ConnectionID       *ids.ID   `json:"connection_id"`
	ExternalReviewID   string    `json:"external_review_id"`
	SourceVersion      int64     `json:"source_version"`
	VerificationStatus string    `json:"verification_status"`
	RatingValue        int       `json:"rating_value"`
	RatingScale        int       `json:"rating_scale"`
	RatingBPS          int       `json:"rating_bps"`
	ContentDigest      string    `json:"content_digest"`
	ContentExcerpt     *string   `json:"content_excerpt"`
	ContentReference   string    `json:"content_reference"`
	ReviewedAt         time.Time `json:"reviewed_at"`
	ModerationStatus   string    `json:"moderation_status"`
	State              string    `json:"state"`
	LegalHold          bool      `json:"legal_hold"`
	ResponseState      string    `json:"response_state"`
	Version            int64     `json:"version"`
}

type ReviewIntakeResult struct {
	Review       ReviewRef `json:"review"`
	Duplicate    bool      `json:"duplicate"`
	Signals      int       `json:"signals"`
	QualityTasks []ids.ID  `json:"quality_tasks"`
}

type InternalReviewCommand struct {
	OrganisationID ids.ID       `json:"organisation_id"`
	ConnectionID   ids.ID       `json:"connection_id"`
	Review         ReviewIntake `json:"review"`
}

type ReviewReplyCommand struct {
	Body   string `json:"body"`
	Reason string `json:"reason"`
}

type ReviewResponse struct {
	ID                 ids.ID     `json:"id"`
	PropertyID         ids.ID     `json:"property_id"`
	ReviewID           ids.ID     `json:"review_id"`
	Body               string     `json:"body"`
	State              string     `json:"state"`
	RequiresApproval   bool       `json:"requires_approval"`
	ApprovalID         *ids.ID    `json:"approval_id"`
	ProviderResponseID *string    `json:"provider_response_id"`
	Attempts           int        `json:"attempts"`
	NextAttemptAt      time.Time  `json:"next_attempt_at"`
	LeaseGeneration    int64      `json:"lease_generation"`
	OutcomeReason      string     `json:"outcome_reason"`
	PublishedAt        *time.Time `json:"published_at"`
	CreatedByMember    ids.ID     `json:"created_by_member"`
	CreatedByRole      ids.ID     `json:"created_by_role"`
	Version            int64      `json:"version"`
}

type ReviewResponseSubmit struct {
	Version    int64  `json:"version"`
	ApprovalID ids.ID `json:"approval_id,omitempty"`
	Reason     string `json:"reason"`
}

type ReviewTopicReview struct {
	Version            int64  `json:"version"`
	Action             string `json:"action"`
	CorrectedSentiment string `json:"corrected_sentiment,omitempty"`
	Reason             string `json:"reason"`
}

type QualityTask struct {
	ID               ids.ID     `json:"id"`
	PropertyID       ids.ID     `json:"property_id"`
	IssueID          *ids.ID    `json:"issue_id"`
	ReviewID         *ids.ID    `json:"review_id"`
	Team             string     `json:"team"`
	Title            string     `json:"title"`
	Severity         string     `json:"severity"`
	State            string     `json:"state"`
	AssignedTo       *ids.ID    `json:"assigned_to"`
	SLADueAt         time.Time  `json:"sla_due_at"`
	LinkedRecordType *string    `json:"linked_record_type"`
	LinkedRecordID   *ids.ID    `json:"linked_record_id"`
	Resolution       string     `json:"resolution"`
	ResolvedAt       *time.Time `json:"resolved_at"`
	Version          int64      `json:"version"`
}

type QualityTaskCreate struct {
	PropertyID ids.ID  `json:"property_id"`
	ReviewID   *ids.ID `json:"review_id,omitempty"`
	Team       string  `json:"team"`
	Title      string  `json:"title"`
	Severity   string  `json:"severity"`
	Reason     string  `json:"reason"`
}

type QualityTaskTransition struct {
	Version           int64   `json:"version"`
	Action            string  `json:"action"`
	AssigneeID        *ids.ID `json:"assignee_id,omitempty"`
	LinkedRecordType  string  `json:"linked_record_type,omitempty"`
	LinkedRecordID    *ids.ID `json:"linked_record_id,omitempty"`
	Resolution        string  `json:"resolution,omitempty"`
	EvidenceReference string  `json:"evidence_reference,omitempty"`
	Reason            string  `json:"reason"`
}

type ReviewDisputeCreate struct {
	ReasonCode   string   `json:"reason_code"`
	Route        string   `json:"route"`
	Statement    string   `json:"statement"`
	EvidenceRefs []ids.ID `json:"evidence_refs"`
	Reason       string   `json:"reason"`
}

type ReviewDispute struct {
	ID                ids.ID          `json:"id"`
	PropertyID        ids.ID          `json:"property_id"`
	ReviewID          ids.ID          `json:"review_id"`
	ReasonCode        string          `json:"reason_code"`
	Route             string          `json:"route"`
	Statement         string          `json:"statement"`
	EvidenceRefs      json.RawMessage `json:"evidence_refs"`
	ReviewVersion     int64           `json:"review_version"`
	State             string          `json:"state"`
	ExternalReference *string         `json:"external_reference"`
	Decision          string          `json:"decision"`
	Version           int64           `json:"version"`
}

type ReviewDisputeTransition struct {
	Version           int64  `json:"version"`
	Action            string `json:"action"`
	ExternalReference string `json:"external_reference,omitempty"`
	Outcome           string `json:"outcome,omitempty"`
	Decision          string `json:"decision,omitempty"`
	Reason            string `json:"reason"`
}

type ReputationSnapshotCommand struct {
	PropertyID ids.ID `json:"property_id"`
	DateFrom   string `json:"date_from"`
	DateTo     string `json:"date_to"`
	Reason     string `json:"reason"`
}

func (h *Handler) registerReputation(r chi.Router) {
	r.Get("/reviews", h.reviewList)
	r.Post("/reviews", h.reviewRecord)
	r.Get("/reviews/{id}", h.reviewGet)
	r.Post("/reviews/{id}/reply", h.reviewReply)
	r.Post("/reviews/{id}/disputes", h.reviewDisputeCreate)
	r.Post("/reviews/{id}/topics/{signal_id}/review", h.reviewTopicReview)
	r.Get("/review-responses", h.reviewResponses)
	r.Post("/review-responses/{id}/submit", h.reviewResponseSubmit)
	r.Post("/review-responses/{id}/retract", h.reviewResponseRetract)
	r.Post("/review-responses/{id}/cancel", h.reviewResponseCancel)
	r.Get("/review-disputes", h.reviewDisputes)
	r.Post("/review-disputes/{id}/transitions", h.reviewDisputeTransition)
	r.Get("/reputation", h.reputationGet)
	r.Post("/reputation/snapshots", h.reputationSnapshot)
	r.Get("/quality-issues", h.qualityIssues)
	r.Get("/quality-tasks", h.qualityTasks)
	r.Post("/quality-tasks", h.qualityTaskCreate)
	r.Post("/quality-tasks/{id}/transitions", h.qualityTaskTransition)
}

func (h *Handler) reviewAdapter(source string) messaging.ReviewAdapter {
	if a, ok := h.ReviewAdapters[source]; ok && a != nil {
		return a
	}
	return messaging.DisabledReviews{}
}

var reviewDimensions = []string{"CLEANLINESS", "LOCATION", "VALUE", "COMMUNICATION", "CHECK_IN", "ACCURACY", "STAFF", "COMFORT", "FACILITIES"}

func validReviewIntake(in ReviewIntake, now time.Time) bool {
	if in.PropertyID.ValidateV7() != nil || !opsText(in.ExternalReviewID, 1, 200) || in.SourceVersion < 1 || !opsText(in.ContentReference, 1, 500) || len(in.Text) > 10000 || in.ReviewedAt.IsZero() || in.ReviewedAt.After(now.Add(5*time.Minute)) || len(in.Dimensions) > len(reviewDimensions) {
		return false
	}
	if _, ok := reputation.Sources[in.Source]; !ok {
		return false
	}
	if _, ok := reputation.Normalise(in.RatingValue, in.RatingScale); !ok {
		return false
	}
	if in.ModerationStatus != "" && !slices.Contains([]string{"PUBLISHED", "PENDING_MODERATION", "HIDDEN_BY_PROVIDER", "REMOVED_BY_PROVIDER"}, in.ModerationStatus) || in.Language != "" && len(in.Language) != 2 {
		return false
	}
	seen := map[string]bool{}
	for _, d := range in.Dimensions {
		if _, ok := reputation.Normalise(d.Value, d.Scale); !ok || !slices.Contains(reviewDimensions, d.Dimension) || seen[d.Dimension] {
			return false
		}
		seen[d.Dimension] = true
	}
	return true
}

// ingestReview applies one source observation. Duplicate or older source
// versions have no effect; newer versions append immutable history.
func (h *Handler) ingestReview(ctx context.Context, tx pgx.Tx, t tenant, in ReviewIntake, connection *ids.ID) (ReviewIntakeResult, error) {
	out := ReviewIntakeResult{QualityTasks: []ids.ID{}}
	now := h.Auth.Service.Now().UTC()
	org := t.Organisation
	verification := "UNVERIFIED"
	switch in.Source {
	case "MARKETPLACE_VERIFIED_STAY":
		if in.ReservationID == nil {
			return out, apperrors.InvalidRequest("A verified-stay review requires its canonical reservation.")
		}
		var completed bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND state IN ('CHECKED_OUT','COMPLETED'))`, org, in.PropertyID, *in.ReservationID).Scan(&completed); e != nil {
			return out, e
		}
		if !completed {
			return out, apperrors.PermissionDenied("Verified-stay status requires a completed canonical stay; it is never assumed.")
		}
		verification = "VERIFIED_STAY"
	case "DIRECT_FEEDBACK":
		if in.StayID == nil {
			return out, apperrors.InvalidRequest("Direct guest feedback requires the departed stay.")
		}
		var reservation ids.ID
		if e := tx.QueryRow(ctx, `SELECT reservation_id FROM stay_records WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND state='DEPARTED'`, org, in.PropertyID, *in.StayID).Scan(&reservation); e != nil {
			return out, apperrors.PermissionDenied("Feedback requires an actual departed stay in this property.")
		}
		in.ReservationID, verification = &reservation, "VERIFIED_STAY"
	case "LEGACY_IMPORT":
		verification, in.ReservationID, in.StayID = "LEGACY_REFERENCE", nil, nil
	default:
		if in.ReservationID != nil {
			var completed bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND state IN ('CHECKED_OUT','COMPLETED'))`, org, in.PropertyID, *in.ReservationID).Scan(&completed); e != nil {
				return out, e
			}
			if completed {
				verification = "VERIFIED_STAY"
			} else {
				in.ReservationID = nil
			}
		}
	}
	bps, _ := reputation.Normalise(in.RatingValue, in.RatingScale)
	digest := security.SHA256Hex([]byte(in.Text))
	var excerpt any
	if in.StoreText && in.Text != "" {
		excerpt = truncate(in.Text, 2000)
	}
	moderation := in.ModerationStatus
	if moderation == "" {
		moderation = "PUBLISHED"
	}
	if in.Deleted {
		moderation = "REMOVED_BY_PROVIDER"
	}
	language := in.Language
	if language == "" {
		language = "en"
	}
	snapshot := map[string]any{"rating_value": in.RatingValue, "rating_scale": in.RatingScale, "dimensions": in.Dimensions, "moderation_status": moderation, "deleted": in.Deleted, "content_reference": in.ContentReference, "reviewed_at": in.ReviewedAt.UTC()}
	existing, e := typedJSON[ReviewRef](ctx, tx, `SELECT to_jsonb(x) FROM review_refs x WHERE organisation_id=$1 AND source=$2 AND external_review_id=$3 FOR UPDATE`, org, in.Source, in.ExternalReviewID)
	var creator any
	if !t.Account.IsZero() {
		creator = t.Account
	}
	var id ids.ID
	action := "RECEIVED"
	switch {
	case errors.Is(e, pgx.ErrNoRows):
		id = ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO review_refs(id,organisation_id,property_id,unit_id,reservation_id,stay_id,source,connection_id,external_review_id,source_version,verification_status,rating_value,rating_scale,rating_bps,content_digest,content_excerpt,content_reference,language,reviewed_at,moderation_status,state,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`, id, org, in.PropertyID, in.UnitID, in.ReservationID, in.StayID, in.Source, connection, in.ExternalReviewID, in.SourceVersion, verification, in.RatingValue, in.RatingScale, bps, digest, excerpt, in.ContentReference, language, in.ReviewedAt.UTC(), moderation, map[bool]string{true: "TOMBSTONED", false: "ACTIVE"}[in.Deleted], creator); e != nil {
			return out, e
		}
	case e != nil:
		return out, e
	default:
		if existing.PropertyID != in.PropertyID {
			return out, apperrors.PermissionDenied("The review source identity belongs to another property.")
		}
		if in.SourceVersion <= existing.SourceVersion {
			out.Review, out.Duplicate = existing, true
			return out, nil
		}
		id, action = existing.ID, "UPDATED"
	}
	if _, e = tx.Exec(ctx, `INSERT INTO review_versions(id,organisation_id,review_id,source_version,rating_value,rating_scale,content_digest,content_excerpt,moderation_status,tombstoned,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, ids.New(), org, id, in.SourceVersion, in.RatingValue, in.RatingScale, digest, excerpt, moderation, in.Deleted, marshalCommercial(snapshot)); e != nil {
		return out, e
	}
	for _, d := range in.Dimensions {
		db, _ := reputation.Normalise(d.Value, d.Scale)
		if _, e = tx.Exec(ctx, `INSERT INTO review_dimension_ratings(organisation_id,review_id,source_version,dimension,rating_value,rating_scale,rating_bps) VALUES($1,$2,$3,$4,$5,$6,$7)`, org, id, in.SourceVersion, d.Dimension, d.Value, d.Scale, db); e != nil {
			return out, e
		}
	}
	if action == "UPDATED" {
		// Legal hold keeps the stored excerpt; new versions are still recorded.
		state := existing.State
		if in.Deleted {
			state = "TOMBSTONED"
		}
		if _, e = tx.Exec(ctx, `UPDATE review_refs SET source_version=$3,rating_value=$4,rating_scale=$5,rating_bps=$6,content_digest=$7,content_excerpt=CASE WHEN legal_hold THEN content_excerpt ELSE $8 END,moderation_status=$9,state=$10,reviewed_at=$11,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, org, id, in.SourceVersion, in.RatingValue, in.RatingScale, bps, digest, excerpt, moderation, state, in.ReviewedAt.UTC()); e != nil {
			return out, e
		}
	}
	review, e := opsGet[ReviewRef](ctx, tx, "review_refs", org, id)
	if e != nil {
		return out, e
	}
	out.Review = review
	if e = h.opsCommit(ctx, tx, t, in.PropertyID, id, "review", "REVIEW_"+action, "", review.State, businessEvents.ReviewReceived, review.Version, map[string]any{"source": in.Source, "source_version": in.SourceVersion, "verification_status": verification, "rating_bps": bps, "moderation_status": moderation, "reservation_id": in.ReservationID}); e != nil {
		return out, e
	}
	if in.Deleted || in.Text == "" {
		return out, nil
	}
	for _, s := range reputation.Classify(in.Text, bps) {
		sid := ids.New()
		tag, e := tx.Exec(ctx, `INSERT INTO review_topic_signals(id,organisation_id,property_id,review_id,source_version,topic,sentiment,confidence_bps,engine,model_version,evidence_terms) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, sid, org, in.PropertyID, id, in.SourceVersion, s.Topic, s.Sentiment, s.ConfidenceBPS, reputation.Engine, reputation.ModelVersion, marshalCommercial(s.EvidenceTerms))
		if e != nil {
			return out, e
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		out.Signals++
		if s.Sentiment == "NEGATIVE" {
			task, e := h.qualitySignal(ctx, tx, t, in.PropertyID, id, sid, s.Topic, in.ReviewedAt.UTC(), now)
			if e != nil {
				return out, e
			}
			if task != nil {
				out.QualityTasks = append(out.QualityTasks, *task)
			}
		}
	}
	return out, nil
}

// qualitySignal links a negative topic signal to the live quality issue and
// creates exactly one open task per issue and team once the repeat threshold
// is reached inside the window.
func (h *Handler) qualitySignal(ctx context.Context, tx pgx.Tx, t tenant, property, review, signal ids.ID, topic string, at, now time.Time) (*ids.ID, error) {
	org := t.Organisation
	type issue struct {
		ID          ids.ID    `json:"id"`
		State       string    `json:"state"`
		Version     int64     `json:"version"`
		WindowDays  int       `json:"window_days"`
		SignalCount int       `json:"signal_count"`
		FirstAt     time.Time `json:"first_signal_at"`
	}
	is, e := typedJSON[issue](ctx, tx, `SELECT to_jsonb(x) FROM quality_issues x WHERE organisation_id=$1 AND property_id=$2 AND topic=$3 AND state IN ('OPEN','TASKED','MONITORING') FOR UPDATE`, org, property, topic)
	threshold, window := 2, 30
	var cfg map[string]float64
	var raw []byte
	if e2 := tx.QueryRow(ctx, `SELECT config FROM automation_rules WHERE organisation_id=$1 AND property_id=$2 AND kind='QUALITY_ESCALATION' AND state='ENABLED'`, org, property).Scan(&raw); e2 == nil {
		_ = json.Unmarshal(raw, &cfg)
		if v := int(cfg["repeat_threshold"]); v >= 1 && v <= 50 {
			threshold = v
		}
		if v := int(cfg["window_days"]); v >= 1 && v <= 365 {
			window = v
		}
	}
	if errors.Is(e, pgx.ErrNoRows) {
		is = issue{ID: ids.New(), State: "OPEN", Version: 1, WindowDays: window}
		if _, e = tx.Exec(ctx, `INSERT INTO quality_issues(id,organisation_id,property_id,topic,first_signal_at,last_signal_at,window_days) VALUES($1,$2,$3,$4,$5,$5,$6)`, is.ID, org, property, topic, at, window); e != nil {
			return nil, e
		}
		if e = h.opsCommit(ctx, tx, t, property, is.ID, "quality_issue", "OPENED", "", "OPEN", "", 1, map[string]any{"topic": topic}); e != nil {
			return nil, e
		}
	} else if e != nil {
		return nil, e
	} else if is.State == "MONITORING" {
		// A repeat during monitoring reopens the same issue (no duplicate).
		if _, e = tx.Exec(ctx, `UPDATE quality_issues SET state='OPEN',monitoring_until=NULL,version=version+1,updated_at=now() WHERE id=$1`, is.ID); e != nil {
			return nil, e
		}
		is.Version++
		if e = h.opsCommit(ctx, tx, t, property, is.ID, "quality_issue", "REOPENED", "MONITORING", "OPEN", "", is.Version, map[string]any{"signal_id": signal}); e != nil {
			return nil, e
		}
		is.State = "OPEN"
	}
	tag, e := tx.Exec(ctx, `INSERT INTO quality_issue_signals(organisation_id,issue_id,signal_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, org, is.ID, signal)
	if e != nil || tag.RowsAffected() == 0 {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE quality_issues SET signal_count=signal_count+1,last_signal_at=greatest(last_signal_at,$2),version=version+1,updated_at=now() WHERE id=$1`, is.ID, at); e != nil {
		return nil, e
	}
	is.Version++
	var recent int
	if e = tx.QueryRow(ctx, `SELECT count(DISTINCT s.review_id) FROM quality_issue_signals q JOIN review_topic_signals s ON s.organisation_id=q.organisation_id AND s.id=q.signal_id JOIN review_refs r ON r.organisation_id=s.organisation_id AND r.id=s.review_id WHERE q.organisation_id=$1 AND q.issue_id=$2 AND s.state<>'REJECTED' AND r.state='ACTIVE' AND r.reviewed_at>=$3`, org, is.ID, now.AddDate(0, 0, -window)).Scan(&recent); e != nil {
		return nil, e
	}
	if is.State != "OPEN" || recent < threshold {
		return nil, nil
	}
	team := reputation.Team(topic)
	severity := "MEDIUM"
	if topic == "SAFETY" {
		severity = "CRITICAL"
	} else if recent >= 2*threshold {
		severity = "HIGH"
	}
	task := ids.New()
	if _, e = tx.Exec(ctx, `INSERT INTO review_action_tasks(id,organisation_id,property_id,issue_id,review_id,team,title,severity,sla_due_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, task, org, property, is.ID, review, team, "Repeated "+strings.ToLower(strings.ReplaceAll(topic, "_", " "))+" feedback", severity, now.Add(recoverySLA(severity)), nilIfZero(t.Account)); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE quality_issues SET state='TASKED',severity=$2,version=version+1,updated_at=now() WHERE id=$1`, is.ID, severity); e != nil {
		return nil, e
	}
	if e = h.opsCommit(ctx, tx, t, property, is.ID, "quality_issue", "TASKED", "OPEN", "TASKED", "", is.Version+1, map[string]any{"task_id": task, "recent_reviews": recent, "threshold": threshold}); e != nil {
		return nil, e
	}
	return &task, h.opsCommit(ctx, tx, t, property, task, "quality_task", "CREATED", "", "OPEN", businessEvents.QualityTaskUpdated, 1, map[string]any{"issue_id": is.ID, "team": team, "topic": topic, "severity": severity, "source_review_id": review})
}

func nilIfZero(id ids.ID) any {
	if id.IsZero() {
		return nil
	}
	return id
}

func (h *Handler) reviewList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h.opsList(w, r, "reviews:read", `SELECT to_jsonb(x) FROM review_refs x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR source=$3) AND ($4='' OR response_state=$4) ORDER BY reviewed_at DESC,id LIMIT $5 OFFSET $6`, q.Get("source"), q.Get("response_state"))
}

// reviewRecord records direct guest feedback or authorised legacy references.
// Channel and Marketplace reviews arrive only from their signed source.
func (h *Handler) reviewRecord(w http.ResponseWriter, r *http.Request) {
	var in ReviewIntake
	if !decode(w, r, &in) {
		return
	}
	if in.Source != "DIRECT_FEEDBACK" && in.Source != "LEGACY_IMPORT" || !validReviewIntake(in, h.Auth.Service.Now().UTC()) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Staff may record only DIRECT_FEEDBACK or LEGACY_IMPORT reviews with valid rating, source identity, reference and reason; channel reviews come from their signed source."))
		return
	}
	capability := "reviews:write"
	if in.Source == "LEGACY_IMPORT" {
		capability = "reviews:moderate"
	}
	h.execute(w, r, "reviews:record:"+in.PropertyID.String(), in, capability, "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, capability, "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		res, e := h.ingestReview(ctx, tx, t, in, nil)
		status := 201
		if res.Duplicate {
			status = 200
		}
		return status, res, e
	})
}

// internalReviewReceipt accepts signed Marketplace verified-stay reviews.
func (h *Handler) internalReviewReceipt(w http.ResponseWriter, r *http.Request) {
	var in InternalReviewCommand
	if !decode(w, r, &in) {
		return
	}
	p, ok := serviceauth.PrincipalFrom(r.Context())
	if !ok || p.Service != platform.MarketplaceService || p.Delegation.OrganizationID != in.OrganisationID {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Signed Marketplace organisation delegation required."))
		return
	}
	if in.Review.Source != "MARKETPLACE_VERIFIED_STAY" || !validReviewIntake(in.Review, h.Auth.Service.Now().UTC()) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A valid Marketplace verified-stay review observation is required."))
		return
	}
	connection, e := h.loadChannelConnection(r.Context(), h.Auth.Service.Pool, in.OrganisationID, in.ConnectionID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if connection.PropertyID != in.Review.PropertyID || connection.State != "ACTIVE" && connection.State != "CONNECTED" {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Review connection does not authorise this property."))
		return
	}
	bound, e := idempotency.Fingerprint(r.Method, "reviews:marketplace", marshalCommercial(in))
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	store := idempotency.NewStore(h.Auth.Service.Pool, 30*time.Second, 7*24*time.Hour)
	result, e := store.Execute(r.Context(), idempotency.Key{Scope: "business:" + in.OrganisationID.String() + ":reviews:marketplace", CallerType: idempotency.CallerService, CallerID: string(p.Service), Key: r.Header.Get("Idempotency-Key")}, bound, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, in.OrganisationID); e != nil {
			return idempotency.Result{}, e
		}
		if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','marketplace-service',true)`); e != nil {
			return idempotency.Result{}, e
		}
		res, e := h.ingestReview(ctx, tx, workerTenant(in.OrganisationID), in.Review, nil)
		if e != nil {
			return idempotency.Result{}, e
		}
		return idempotency.Result{Status: 202, Body: map[string]any{"data": res}}, nil
	})
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if result.Replayed {
		w.Header().Set(idempotency.HeaderReplayed, "true")
	}
	httpx.WriteRaw(w, result.Status, result.Body)
}

func (h *Handler) reviewGet(w http.ResponseWriter, r *http.Request) {
	t, _, id, e := h.opsRecordProperty(r, "review_refs", "reviews:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	ctx, pool := r.Context(), h.Auth.Service.Pool
	review, e := opsGet[ReviewRef](ctx, pool, "review_refs", t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out := map[string]any{"review": review}
	for key, query := range map[string]string{
		"versions":   `SELECT to_jsonb(x) FROM review_versions x WHERE organisation_id=$1 AND review_id=$2 ORDER BY source_version LIMIT 200`,
		"dimensions": `SELECT to_jsonb(x) FROM review_dimension_ratings x WHERE organisation_id=$1 AND review_id=$2 ORDER BY source_version,dimension LIMIT 200`,
		"signals":    `SELECT to_jsonb(x) FROM review_topic_signals x WHERE organisation_id=$1 AND review_id=$2 ORDER BY source_version,topic LIMIT 200`,
		"responses":  `SELECT to_jsonb(x) FROM review_responses x WHERE organisation_id=$1 AND review_id=$2 ORDER BY created_at LIMIT 50`,
		"disputes":   `SELECT to_jsonb(x) FROM review_disputes x WHERE organisation_id=$1 AND review_id=$2 ORDER BY created_at LIMIT 50`,
	} {
		rows, e := commercialJSONRows(ctx, pool, query, t.Organisation, id)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		out[key] = rows
	}
	reply(w, r, 200, out, nil)
}

func (h *Handler) reviewReply(w http.ResponseWriter, r *http.Request) {
	var in ReviewReplyCommand
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "review_refs", "reviews:write", false)
	if e != nil || !validStaffReason(in.Reason) || len(in.Body) > 4000 {
		if e == nil {
			e = apperrors.InvalidRequest("Response body and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "reviews:reply:"+id.String(), in, "reviews:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reviews:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		review, e := opsLock[ReviewRef](ctx, tx, "review_refs", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if review.State != "ACTIVE" || review.ModerationStatus == "REMOVED_BY_PROVIDER" {
			return 0, nil, apperrors.Conflict("REVIEW_NOT_REPLYABLE", "The review is removed or tombstoned at its source.")
		}
		if code := reputation.ReplyPolicy(review.Source, in.Body); code != "" {
			return 0, nil, apperrors.Conflict(code, "The response violates the provider reply policy: "+code+".")
		}
		rid := ids.New()
		requires := review.RatingBPS <= 4000
		if _, e = tx.Exec(ctx, `INSERT INTO review_responses(id,organisation_id,property_id,review_id,body,requires_approval,created_by,created_by_member,created_by_role) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, rid, t.Organisation, property, id, in.Body, requires, t.Account, t.Member, t.Role); e != nil {
			return 0, nil, e
		}
		if e = h.reviewResponseMirror(ctx, tx, t.Organisation, id, "DRAFT"); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[ReviewResponse](ctx, tx, "review_responses", t.Organisation, rid)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, property, rid, "review_response", "DRAFTED", "", "DRAFT", businessEvents.ReviewResponseUpdated, 1, map[string]any{"review_id": id, "requires_approval": requires})
	})
}

func (h *Handler) reviewResponseMirror(ctx context.Context, tx pgx.Tx, org, review ids.ID, state string) error {
	_, e := tx.Exec(ctx, `UPDATE review_refs SET response_state=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, org, review, state)
	return e
}

func (h *Handler) reviewResponses(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h.opsList(w, r, "reviews:read", `SELECT to_jsonb(x) FROM review_responses x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR review_id::text=$3) AND ($4='' OR state=$4) ORDER BY created_at DESC,id LIMIT $5 OFFSET $6`, q.Get("review_id"), q.Get("state"))
}

func (h *Handler) responseCommand(w http.ResponseWriter, r *http.Request, version int64, reason, action, capability string, mfa bool, payload any, fn func(context.Context, pgx.Tx, tenant, ReviewResponse, ReviewRef) (string, error)) {
	_, property, id, e := h.opsRecordProperty(r, "review_responses", capability, mfa)
	if e != nil || version < 1 || !validStaffReason(reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "reviews:response:"+action+":"+id.String(), payload, capability, "PROPERTY", property, mfa, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, capability, "PROPERTY", property, mfa)
		if e != nil {
			return 0, nil, e
		}
		resp, e := opsLock[ReviewResponse](ctx, tx, "review_responses", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if resp.Version != version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Response version has changed.")
		}
		review, e := opsLock[ReviewRef](ctx, tx, "review_refs", t.Organisation, resp.ReviewID)
		if e != nil {
			return 0, nil, e
		}
		to, e := fn(ctx, tx, t, resp, review)
		if e != nil {
			return 0, nil, e
		}
		if e = h.reviewResponseMirror(ctx, tx, t.Organisation, review.ID, to); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[ReviewResponse](ctx, tx, "review_responses", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "review_response", action, resp.State, to, businessEvents.ReviewResponseUpdated, out.Version, map[string]any{"reason": reason})
	})
}

func (h *Handler) reviewResponseSubmit(w http.ResponseWriter, r *http.Request) {
	var in ReviewResponseSubmit
	if !decode(w, r, &in) {
		return
	}
	h.responseCommand(w, r, in.Version, in.Reason, "SUBMIT", "reviews:write", false, in, func(ctx context.Context, tx pgx.Tx, t tenant, resp ReviewResponse, review ReviewRef) (string, error) {
		if resp.State != "DRAFT" {
			return "", apperrors.Conflict("INVALID_TRANSITION", "Only drafts can be submitted.")
		}
		if code := reputation.ReplyPolicy(review.Source, resp.Body); code != "" {
			return "", apperrors.Conflict(code, "The response violates the provider reply policy.")
		}
		var approval any
		if resp.RequiresApproval {
			target, e := resolve(ctx, tx, t.Organisation, "PROPERTY", resp.PropertyID)
			if e != nil {
				return "", e
			}
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_REVIEW_RESPONSE", target.Scope, in.ApprovalID, map[string]any{"response_id": resp.ID, "body_digest": security.SHA256Hex([]byte(resp.Body))}, "", nil, ""); e != nil {
				return "", e
			}
			approval = in.ApprovalID
		}
		_, e := tx.Exec(ctx, `UPDATE review_responses SET state='APPROVED',approval_id=coalesce($2,approval_id),submitted_by=$3,next_attempt_at=now(),version=version+1,updated_at=now() WHERE id=$1`, resp.ID, approval, t.Account)
		return "APPROVED", e
	})
}

func (h *Handler) reviewResponseRetract(w http.ResponseWriter, r *http.Request) {
	var in OpsVersionCommand
	if !decode(w, r, &in) {
		return
	}
	h.responseCommand(w, r, in.Version, in.Reason, "RETRACT", "reviews:write", false, in, func(ctx context.Context, tx pgx.Tx, t tenant, resp ReviewResponse, review ReviewRef) (string, error) {
		if resp.State != "PUBLISHED" {
			return "", apperrors.Conflict("INVALID_TRANSITION", "Only published responses can be retracted.")
		}
		if !reputation.Sources[review.Source].Retract {
			return "", apperrors.Conflict("PROVIDER_RETRACT_UNSUPPORTED", "The provider does not permit retracting responses.")
		}
		_, e := tx.Exec(ctx, `UPDATE review_responses SET state='RETRACT_REQUESTED',next_attempt_at=now(),version=version+1,updated_at=now() WHERE id=$1`, resp.ID)
		return "RETRACT_REQUESTED", e
	})
}

func (h *Handler) reviewResponseCancel(w http.ResponseWriter, r *http.Request) {
	var in OpsVersionCommand
	if !decode(w, r, &in) {
		return
	}
	h.responseCommand(w, r, in.Version, in.Reason, "CANCEL", "reviews:write", false, in, func(ctx context.Context, tx pgx.Tx, t tenant, resp ReviewResponse, review ReviewRef) (string, error) {
		if resp.State != "DRAFT" && resp.State != "PENDING_APPROVAL" && resp.State != "APPROVED" {
			return "", apperrors.Conflict("INVALID_TRANSITION", "Submitted responses cannot be cancelled.")
		}
		_, e := tx.Exec(ctx, `UPDATE review_responses SET state='CANCELLED',version=version+1,updated_at=now() WHERE id=$1`, resp.ID)
		return "CANCELLED", e
	})
}

func (h *Handler) reviewTopicReview(w http.ResponseWriter, r *http.Request) {
	var in ReviewTopicReview
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "review_refs", "reviews:moderate", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	signal, e := ids.ParseV7(chi.URLParam(r, "signal_id"))
	if e != nil || in.Version < 1 || !slices.Contains([]string{"CONFIRM", "CORRECT", "REJECT"}, in.Action) || in.Action == "CORRECT" && !slices.Contains([]string{"NEGATIVE", "NEUTRAL", "POSITIVE"}, in.CorrectedSentiment) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Signal, current version, CONFIRM/CORRECT/REJECT, corrected sentiment for CORRECT and reason required."))
		return
	}
	h.execute(w, r, "reviews:topic:"+signal.String(), in, "reviews:moderate", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reviews:moderate", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		to := map[string]string{"CONFIRM": "CONFIRMED", "CORRECT": "CORRECTED", "REJECT": "REJECTED"}[in.Action]
		var corrected any
		if in.Action == "CORRECT" {
			corrected = in.CorrectedSentiment
		}
		tag, e := tx.Exec(ctx, `UPDATE review_topic_signals SET state=$4,corrected_sentiment=$5,reviewed_by=$6,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND review_id=$3 AND version=$7 AND state='MACHINE'`, t.Organisation, signal, id, to, corrected, t.Account, in.Version)
		if e != nil {
			return 0, nil, e
		}
		if tag.RowsAffected() != 1 {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Signal version or state has changed.")
		}
		out, e := oneJSON(ctx, tx, `SELECT to_jsonb(x) FROM review_topic_signals x WHERE organisation_id=$1 AND id=$2`, t.Organisation, signal)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, signal, "review_topic_signal", in.Action, "MACHINE", to, "", in.Version+1, map[string]any{"review_id": id, "corrected_sentiment": corrected, "reason": in.Reason})
	})
}

func (h *Handler) reviewDisputes(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "reviews:read", `SELECT to_jsonb(x) FROM review_disputes x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR state=$3) ORDER BY created_at DESC,id LIMIT $4 OFFSET $5`, r.URL.Query().Get("state"))
}

func (h *Handler) reviewDisputeCreate(w http.ResponseWriter, r *http.Request) {
	var in ReviewDisputeCreate
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "review_refs", "reviews:moderate", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	ok := slices.Contains([]string{"FAKE_REVIEW", "ABUSIVE", "WRONG_BOOKING", "PROVIDER_MODERATION", "POLICY_VIOLATION"}, in.ReasonCode) && (in.Route == "PROVIDER" || in.Route == "ADMIN") && opsText(in.Statement, 10, 2000) && len(in.EvidenceRefs) >= 1 && len(in.EvidenceRefs) <= 20 && validStaffReason(in.Reason)
	for _, ref := range in.EvidenceRefs {
		ok = ok && ref.ValidateV7() == nil
	}
	if !ok {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Reason code, route, statement, 1-20 Media evidence references and reason required."))
		return
	}
	h.execute(w, r, "reviews:dispute:"+id.String(), in, "reviews:moderate", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reviews:moderate", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		review, e := opsLock[ReviewRef](ctx, tx, "review_refs", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		did := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO review_disputes(id,organisation_id,property_id,review_id,reason_code,route,statement,evidence_refs,review_version,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, did, t.Organisation, property, id, in.ReasonCode, in.Route, in.Statement, marshalCommercial(in.EvidenceRefs), review.SourceVersion, t.Account); e != nil {
			return 0, nil, e
		}
		// Original evidence is preserved under hold while the dispute is live.
		if _, e = tx.Exec(ctx, `UPDATE review_refs SET legal_hold=true,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[ReviewDispute](ctx, tx, "review_disputes", t.Organisation, did)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, property, did, "review_dispute", "OPENED", "", "DRAFT", businessEvents.ReviewDisputeUpdated, 1, map[string]any{"review_id": id, "reason_code": in.ReasonCode, "route": in.Route, "review_version": review.SourceVersion})
	})
}

func (h *Handler) reviewDisputeTransition(w http.ResponseWriter, r *http.Request) {
	var in ReviewDisputeTransition
	if !decode(w, r, &in) {
		return
	}
	mfa := in.Action == "RECORD_DECISION"
	_, property, id, e := h.opsRecordProperty(r, "review_disputes", "reviews:moderate", mfa)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || !slices.Contains([]string{"SUBMIT", "RECORD_ACKNOWLEDGEMENT", "RECORD_DECISION", "APPEAL", "WITHDRAW"}, in.Action) || in.Action == "RECORD_ACKNOWLEDGEMENT" && !opsText(in.ExternalReference, 3, 300) || in.Action == "RECORD_DECISION" && (in.Outcome != "UPHELD" && in.Outcome != "REJECTED" || !opsText(in.Decision, 5, 2000)) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, supported action, provider/Admin reference for acknowledgement, outcome and decision text for decisions, and reason required."))
		return
	}
	h.execute(w, r, "reviews:dispute:transition:"+id.String(), in, "reviews:moderate", "PROPERTY", property, mfa, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reviews:moderate", "PROPERTY", property, mfa)
		if e != nil {
			return 0, nil, e
		}
		d, e := opsLock[ReviewDispute](ctx, tx, "review_disputes", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if d.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Dispute version has changed.")
		}
		allowed := map[string][]string{"SUBMIT": {"DRAFT"}, "RECORD_ACKNOWLEDGEMENT": {"SUBMITTED"}, "RECORD_DECISION": {"UNDER_REVIEW", "APPEALED"}, "APPEAL": {"REJECTED"}, "WITHDRAW": {"DRAFT", "SUBMITTED", "UNDER_REVIEW", "APPEALED"}}
		if !slices.Contains(allowed[in.Action], d.State) {
			return 0, nil, apperrors.Conflict("INVALID_TRANSITION", "The dispute action is not allowed from "+d.State+".")
		}
		var to string
		switch in.Action {
		case "SUBMIT":
			to = "SUBMITTED"
		case "RECORD_ACKNOWLEDGEMENT":
			to = "UNDER_REVIEW"
		case "RECORD_DECISION":
			to = in.Outcome
		case "APPEAL":
			to = "APPEALED"
		case "WITHDRAW":
			to = "WITHDRAWN"
		}
		var ref any
		if in.ExternalReference != "" {
			ref = in.ExternalReference
		}
		if _, e = tx.Exec(ctx, `UPDATE review_disputes SET state=$3,external_reference=coalesce($4,external_reference),decision=CASE WHEN $5<>'' THEN $5 ELSE decision END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, to, ref, in.Decision); e != nil {
			return 0, nil, e
		}
		// The hold is released only when no dispute on the review remains live;
		// the review row and its versions are never rewritten by a decision.
		if _, e = tx.Exec(ctx, `UPDATE review_refs r SET legal_hold=EXISTS(SELECT 1 FROM review_disputes d WHERE d.review_id=r.id AND d.state IN ('DRAFT','SUBMITTED','UNDER_REVIEW','APPEALED')),version=version+1,updated_at=now() WHERE r.organisation_id=$1 AND r.id=$2`, t.Organisation, d.ReviewID); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[ReviewDispute](ctx, tx, "review_disputes", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "review_dispute", in.Action, d.State, to, businessEvents.ReviewDisputeUpdated, out.Version, map[string]any{"external_reference": ref, "decision": in.Decision, "reason": in.Reason})
	})
}

func (h *Handler) qualityIssues(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "quality:read", `SELECT to_jsonb(x) FROM quality_issues x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR state=$3) ORDER BY last_signal_at DESC,id LIMIT $4 OFFSET $5`, r.URL.Query().Get("state"))
}

func (h *Handler) qualityTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h.opsList(w, r, "quality:read", `SELECT to_jsonb(x)||jsonb_build_object('sla_breached',x.state IN ('OPEN','ASSIGNED','IN_PROGRESS') AND x.sla_due_at<now()) FROM review_action_tasks x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR state=$3) AND ($4='' OR team=$4) ORDER BY sla_due_at,id LIMIT $5 OFFSET $6`, q.Get("state"), q.Get("team"))
}

var qualityTeams = []string{"HOUSEKEEPING", "MAINTENANCE", "FRONT_DESK", "OPERATIONS", "GUEST_RECOVERY"}

func (h *Handler) qualityTaskCreate(w http.ResponseWriter, r *http.Request) {
	var in QualityTaskCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || !slices.Contains(qualityTeams, in.Team) || !opsText(in.Title, 5, 200) || !slices.Contains([]string{"LOW", "MEDIUM", "HIGH", "CRITICAL"}, in.Severity) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, team, title, severity and reason required."))
		return
	}
	h.execute(w, r, "quality:create:"+in.PropertyID.String(), in, "quality:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "quality:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO review_action_tasks(id,organisation_id,property_id,review_id,team,title,severity,sla_due_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, t.Organisation, in.PropertyID, in.ReviewID, in.Team, in.Title, in.Severity, h.Auth.Service.Now().UTC().Add(recoverySLA(in.Severity)), t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[QualityTask](ctx, tx, "review_action_tasks", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, in.PropertyID, id, "quality_task", "CREATED", "", "OPEN", businessEvents.QualityTaskUpdated, 1, map[string]any{"team": in.Team, "severity": in.Severity, "reason": in.Reason})
	})
}

var qualityTaskRules = map[string]struct {
	from []string
	to   string
}{
	"ASSIGN":  {[]string{"OPEN", "ASSIGNED", "IN_PROGRESS"}, "ASSIGNED"},
	"START":   {[]string{"OPEN", "ASSIGNED", "RESOLVED"}, "IN_PROGRESS"},
	"LINK":    {[]string{"OPEN", "ASSIGNED", "IN_PROGRESS"}, ""},
	"RESOLVE": {[]string{"IN_PROGRESS"}, "RESOLVED"},
	"VERIFY":  {[]string{"RESOLVED"}, "VERIFIED"},
	"CANCEL":  {[]string{"OPEN", "ASSIGNED"}, "CANCELLED"},
}

// linkedRecordExists verifies the owning operational record in the property.
func linkedRecordExists(ctx context.Context, tx pgx.Tx, org, property ids.ID, kind string, id ids.ID) (bool, error) {
	table := map[string]string{"HOUSEKEEPING_TASK": "housekeeping_tasks", "WORK_ORDER": "work_orders", "SERVICE_RECOVERY_CASE": "service_recovery_cases", "SUPPORT_CASE": "support_cases"}[kind]
	if table == "" {
		return false, nil
	}
	var ok bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE organisation_id=$1 AND property_id=$2 AND id=$3)`, org, property, id).Scan(&ok)
	return ok, e
}

func (h *Handler) qualityTaskTransition(w http.ResponseWriter, r *http.Request) {
	var in QualityTaskTransition
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "review_action_tasks", "quality:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rule, ok := qualityTaskRules[in.Action]
	if !ok || in.Version < 1 || !validStaffReason(in.Reason) || in.Action == "ASSIGN" && in.AssigneeID == nil || in.Action == "LINK" && in.LinkedRecordID == nil || in.Action == "RESOLVE" && (!opsText(in.Resolution, 5, 2000) || !opsText(in.EvidenceReference, 5, 300)) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Supported action, current version, reason and action-specific assignee, linked record or resolution evidence required."))
		return
	}
	h.execute(w, r, "quality:transition:"+id.String(), in, "quality:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "quality:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		task, e := opsLock[QualityTask](ctx, tx, "review_action_tasks", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if task.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Task version has changed.")
		}
		if !slices.Contains(rule.from, task.State) {
			return 0, nil, apperrors.Conflict("INVALID_TRANSITION", "The action is not allowed from "+task.State+".")
		}
		to := rule.to
		if to == "" {
			to = task.State
		}
		assignee := task.AssignedTo
		if in.AssigneeID != nil {
			ok, e := h.staffAuthority(ctx, tx, t.Organisation, property, *in.AssigneeID, "quality:write")
			if e != nil {
				return 0, nil, e
			}
			if !ok {
				return 0, nil, apperrors.PermissionDenied("The assignee must hold quality access for this property.")
			}
			assignee = in.AssigneeID
		}
		var linkType, linkID any = task.LinkedRecordType, task.LinkedRecordID
		if in.Action == "LINK" {
			ok, e := linkedRecordExists(ctx, tx, t.Organisation, property, in.LinkedRecordType, *in.LinkedRecordID)
			if e != nil {
				return 0, nil, e
			}
			if !ok {
				return 0, nil, apperrors.Conflict("LINKED_RECORD_NOT_FOUND", "The linked operational record does not exist in this property.")
			}
			linkType, linkID = in.LinkedRecordType, *in.LinkedRecordID
		}
		if _, e = tx.Exec(ctx, `UPDATE review_action_tasks SET state=$3,assigned_to=$4,linked_record_type=$5,linked_record_id=$6,resolution=CASE WHEN $7<>'' THEN $7 ELSE resolution END,evidence_reference=CASE WHEN $8<>'' THEN $8 ELSE evidence_reference END,resolved_at=CASE WHEN $3='RESOLVED' THEN now() WHEN $3='IN_PROGRESS' THEN NULL ELSE resolved_at END,follow_up_at=CASE WHEN $3='RESOLVED' THEN now()+interval '7 days' ELSE follow_up_at END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, to, assignee, linkType, linkID, in.Resolution, in.EvidenceReference); e != nil {
			return 0, nil, e
		}
		if (to == "VERIFIED" || to == "CANCELLED") && task.IssueID != nil {
			// The issue enters monitoring only when no other task is open.
			if _, e = tx.Exec(ctx, `UPDATE quality_issues q SET state='MONITORING',monitoring_until=now()+make_interval(days=>q.window_days),version=version+1,updated_at=now() WHERE q.organisation_id=$1 AND q.id=$2 AND q.state='TASKED' AND NOT EXISTS(SELECT 1 FROM review_action_tasks x WHERE x.issue_id=q.id AND x.state IN ('OPEN','ASSIGNED','IN_PROGRESS','RESOLVED'))`, t.Organisation, *task.IssueID); e != nil {
				return 0, nil, e
			}
		}
		out, e := opsGet[QualityTask](ctx, tx, "review_action_tasks", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "quality_task", in.Action, task.State, to, businessEvents.QualityTaskUpdated, out.Version, map[string]any{"assignee_id": assignee, "linked_record_type": linkType, "linked_record_id": linkID, "reason": in.Reason})
	})
}

// reputationMetrics computes source-backed metrics. Every review counts once
// (its latest source version); removed/tombstoned reviews are excluded.
// Correlations are descriptive; unavailable inputs are null with a reason.
func reputationMetrics(ctx context.Context, q database.Querier, org, property ids.ID, from, to time.Time) (map[string]any, error) {
	out := map[string]any{"property_id": property, "date_from": from.Format(time.DateOnly), "date_to": to.Format(time.DateOnly), "basis": "ACTIVE_PUBLISHED_REVIEWS_LATEST_SOURCE_VERSION", "correlation_is_not_causation": true}
	var count, responded int
	var avg, avgResponseSeconds *float64
	if e := q.QueryRow(ctx, `SELECT count(*),avg(rating_bps)::float8,count(*) FILTER (WHERE response_state='PUBLISHED'),(SELECT avg(extract(epoch FROM rr.published_at-r2.reviewed_at))::float8 FROM review_responses rr JOIN review_refs r2 ON r2.id=rr.review_id WHERE r2.organisation_id=$1 AND r2.property_id=$2 AND rr.state='PUBLISHED' AND r2.reviewed_at>=$3 AND r2.reviewed_at<$4) FROM review_refs WHERE organisation_id=$1 AND property_id=$2 AND state='ACTIVE' AND moderation_status='PUBLISHED' AND reviewed_at>=$3 AND reviewed_at<$4`, org, property, from, to).Scan(&count, &avg, &responded, &avgResponseSeconds); e != nil {
		return nil, e
	}
	out["review_count"], out["average_rating_bps"], out["average_response_seconds"] = count, avg, avgResponseSeconds
	if count > 0 {
		out["response_rate_bps"] = responded * 10000 / count
	} else {
		out["response_rate_bps"] = nil
	}
	for key, query := range map[string]string{
		"by_source":       `SELECT jsonb_build_object('source',source,'count',count(*),'average_rating_bps',avg(rating_bps)::float8) FROM review_refs WHERE organisation_id=$1 AND property_id=$2 AND state='ACTIVE' AND moderation_status='PUBLISHED' AND reviewed_at>=$3 AND reviewed_at<$4 GROUP BY source ORDER BY source`,
		"dimensions":      `SELECT jsonb_build_object('dimension',d.dimension,'count',count(*),'average_rating_bps',avg(d.rating_bps)::float8) FROM review_dimension_ratings d JOIN review_refs r ON r.organisation_id=d.organisation_id AND r.id=d.review_id AND r.source_version=d.source_version WHERE r.organisation_id=$1 AND r.property_id=$2 AND r.state='ACTIVE' AND r.moderation_status='PUBLISHED' AND r.reviewed_at>=$3 AND r.reviewed_at<$4 GROUP BY d.dimension ORDER BY d.dimension`,
		"topic_frequency": `SELECT jsonb_build_object('topic',s.topic,'sentiment',coalesce(s.corrected_sentiment,s.sentiment),'reviews',count(DISTINCT s.review_id)) FROM review_topic_signals s JOIN review_refs r ON r.organisation_id=s.organisation_id AND r.id=s.review_id AND r.source_version=s.source_version WHERE r.organisation_id=$1 AND r.property_id=$2 AND r.state='ACTIVE' AND s.state<>'REJECTED' AND r.reviewed_at>=$3 AND r.reviewed_at<$4 GROUP BY s.topic,coalesce(s.corrected_sentiment,s.sentiment) ORDER BY 1`,
	} {
		rows, e := commercialJSONRows(ctx, q, query, org, property, from, to)
		if e != nil {
			return nil, e
		}
		out[key] = rows
	}
	var current, previous *float64
	mid := from.Add(to.Sub(from) / 2)
	if e := q.QueryRow(ctx, `SELECT avg(rating_bps) FILTER (WHERE reviewed_at>=$4)::float8,avg(rating_bps) FILTER (WHERE reviewed_at<$4)::float8 FROM review_refs WHERE organisation_id=$1 AND property_id=$2 AND state='ACTIVE' AND moderation_status='PUBLISHED' AND reviewed_at>=$3 AND reviewed_at<$5`, org, property, from, mid, to).Scan(&current, &previous); e != nil {
		return nil, e
	}
	trend := map[string]any{"first_half_average_bps": previous, "second_half_average_bps": current, "negative_trend": nil}
	if current != nil && previous != nil {
		trend["negative_trend"] = *current < *previous-500
	}
	out["trend"] = trend
	var openIssues, repeated int
	if e := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state IN ('OPEN','TASKED','MONITORING')),count(*) FILTER (WHERE signal_count>=2) FROM quality_issues WHERE organisation_id=$1 AND property_id=$2`, org, property).Scan(&openIssues, &repeated); e != nil {
		return nil, e
	}
	out["open_quality_issues"], out["repeated_quality_issues"] = openIssues, repeated
	var reviewed, recovered, cancelled, repeat, guests int
	if e := q.QueryRow(ctx, `WITH rv AS (SELECT DISTINCT r.reservation_id FROM review_refs r WHERE r.organisation_id=$1 AND r.property_id=$2 AND r.reservation_id IS NOT NULL AND r.state='ACTIVE' AND r.reviewed_at>=$3 AND r.reviewed_at<$4), g AS (SELECT DISTINCT rg.guest_id FROM rv JOIN reservation_guests rg ON rg.organisation_id=$1 AND rg.reservation_id=rv.reservation_id WHERE rg.guest_id IS NOT NULL)
	 SELECT (SELECT count(*) FROM rv),(SELECT count(*) FROM rv WHERE EXISTS(SELECT 1 FROM service_recovery_cases c WHERE c.organisation_id=$1 AND c.reservation_id=rv.reservation_id)),
	 (SELECT count(*) FROM reservations x WHERE x.organisation_id=$1 AND x.property_id=$2 AND x.state='CANCELLED' AND x.created_at>=$3 AND x.created_at<$4),
	 (SELECT count(*) FROM g WHERE (SELECT count(DISTINCT rg2.reservation_id) FROM reservation_guests rg2 JOIN reservations r2 ON r2.organisation_id=rg2.organisation_id AND r2.id=rg2.reservation_id WHERE rg2.organisation_id=$1 AND rg2.guest_id=g.guest_id AND r2.property_id=$2)>1),(SELECT count(*) FROM g)`, org, property, from, to).Scan(&reviewed, &recovered, &cancelled, &repeat, &guests); e != nil {
		return nil, e
	}
	corr := map[string]any{"reviewed_reservations": reviewed, "reviewed_with_service_recovery": recovered, "cancelled_reservations_in_period": cancelled}
	if guests > 0 {
		corr["repeat_stay_rate_bps"] = repeat * 10000 / guests
	} else {
		corr["repeat_stay_rate_bps"] = nil
	}
	var funnel int
	if e := q.QueryRow(ctx, `SELECT count(*) FROM funnel_observations WHERE organisation_id=$1 AND property_id=$2 AND stage='LISTING_VIEW' AND observed_at>=$3 AND observed_at<$4`, org, property, from, to).Scan(&funnel); e != nil {
		corr["conversion"], corr["conversion_unavailable_reason"] = nil, "FUNNEL_SOURCE_UNAVAILABLE"
	} else if funnel == 0 {
		corr["conversion"], corr["conversion_unavailable_reason"] = nil, "NO_SOURCED_LISTING_VIEWS"
	} else {
		var booked int
		if e := q.QueryRow(ctx, `SELECT count(*) FROM funnel_observations WHERE organisation_id=$1 AND property_id=$2 AND stage='RESERVATION' AND observed_at>=$3 AND observed_at<$4`, org, property, from, to).Scan(&booked); e != nil {
			return nil, e
		}
		corr["conversion"] = map[string]any{"listing_views": funnel, "reservations": booked, "view_to_reservation_bps": booked * 10000 / funnel}
	}
	out["correlations"] = corr
	return out, nil
}

func reputationWindow(r *http.Request, now time.Time) (time.Time, time.Time, error) {
	from, to := now.AddDate(0, 0, -90), now.Add(time.Second)
	var e error
	if raw := r.URL.Query().Get("date_from"); raw != "" {
		if from, e = time.Parse(time.DateOnly, raw); e != nil {
			return from, to, apperrors.InvalidRequest("Invalid date_from.")
		}
	}
	if raw := r.URL.Query().Get("date_to"); raw != "" {
		if to, e = time.Parse(time.DateOnly, raw); e != nil {
			return from, to, apperrors.InvalidRequest("Invalid date_to.")
		}
		to = to.AddDate(0, 0, 1)
	}
	if !to.After(from) || to.Sub(from) > 731*24*time.Hour {
		return from, to, apperrors.InvalidRequest("The window must be positive and at most two years.")
	}
	return from, to, nil
}

// reputationGet returns property metrics, or a branch roll-up of the
// properties the caller may read.
func (h *Handler) reputationGet(w http.ResponseWriter, r *http.Request) {
	from, to, e := reputationWindow(r, h.Auth.Service.Now().UTC())
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	q := r.URL.Query()
	if raw := q.Get("property_id"); raw != "" {
		property, e := ids.ParseV7(raw)
		if e != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid property_id."))
			return
		}
		t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "reputation:read", "PROPERTY", property, false)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		out, e := reputationMetrics(r.Context(), h.Auth.Service.Pool, t.Organisation, property, from, to)
		reply(w, r, 200, out, e)
		return
	}
	branch, e := ids.ParseV7(q.Get("branch_id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("property_id or branch_id is required."))
		return
	}
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "reputation:read", "BRANCH", branch, false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rows, e := h.Auth.Service.Pool.Query(r.Context(), `SELECT id FROM inventory_nodes WHERE organisation_id=$1 AND kind='PROPERTY' AND branch_id=$2 ORDER BY id LIMIT 200`, t.Organisation, branch)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	props := []ids.ID{}
	for rows.Next() {
		var p ids.ID
		if e = rows.Scan(&p); e != nil {
			rows.Close()
			reply(w, r, 0, nil, e)
			return
		}
		props = append(props, p)
	}
	rows.Close()
	items := []map[string]any{}
	for _, p := range props {
		if _, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "reputation:read", "PROPERTY", p, false); e != nil {
			continue
		}
		m, e := reputationMetrics(r.Context(), h.Auth.Service.Pool, t.Organisation, p, from, to)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		items = append(items, m)
	}
	reply(w, r, 200, map[string]any{"branch_id": branch, "properties": items}, nil)
}

func (h *Handler) reputationSnapshot(w http.ResponseWriter, r *http.Request) {
	var in ReputationSnapshotCommand
	if !decode(w, r, &in) {
		return
	}
	from, e1 := time.Parse(time.DateOnly, in.DateFrom)
	to, e2 := time.Parse(time.DateOnly, in.DateTo)
	if in.PropertyID.ValidateV7() != nil || e1 != nil || e2 != nil || to.Before(from) || to.Sub(from) > 731*24*time.Hour || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, date window and reason required."))
		return
	}
	h.execute(w, r, "reputation:snapshot:"+in.PropertyID.String(), in, "reputation:read", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reputation:read", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		m, e := reputationMetrics(ctx, tx, t.Organisation, in.PropertyID, from, to.AddDate(0, 0, 1))
		if e != nil {
			return 0, nil, e
		}
		raw := marshalCommercial(m)
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO reputation_metrics(id,organisation_id,property_id,period_from,period_to,metrics,input_digest,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, in.PropertyID, in.DateFrom, in.DateTo, raw, security.SHA256Hex(raw), t.Account); e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"id": id, "metrics": m, "input_digest": security.SHA256Hex(raw)}, h.opsCommit(ctx, tx, t, in.PropertyID, id, "reputation_snapshot", "MATERIALIZED", "", "READY", businessEvents.ReputationSnapshotReady, 1, map[string]any{"date_from": in.DateFrom, "date_to": in.DateTo, "input_digest": security.SHA256Hex(raw)})
	})
}
