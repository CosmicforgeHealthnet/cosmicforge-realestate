package usecases

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// ReviewView is a manual-review queue item.
type ReviewView struct {
	ReviewID         ids.ID         `json:"review_id"`
	VerificationID   ids.ID         `json:"verification_id"`
	VerificationType string         `json:"verification_type"`
	Subject          domain.Subject `json:"subject"`
	Status           string         `json:"status"`
	Reason           string         `json:"reason"`
	AssignedAdminID  *ids.ID        `json:"assigned_admin_id"`
	AssignedAt       *time.Time     `json:"assigned_at"`
	CreatedAt        time.Time      `json:"created_at"`
}

type reviewCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        ids.ID    `json:"i"`
}

// ListReviews returns the manual-review queue (admin only).
func (s *Service) ListReviews(ctx context.Context, statuses []string, cursor string, limit int) (pagination.Response[ReviewView], error) {
	if _, err := RequireAdminActor(ctx); err != nil {
		return pagination.Response[ReviewView]{}, err
	}
	var cur reviewCursor
	has, err := pagination.Decode(cursor, &cur)
	if err != nil {
		return pagination.Response[ReviewView]{}, err
	}
	p := db.ListReviewQueueParams{Statuses: statuses, RowLimit: int32(limit + 1)}
	if has {
		p.CursorCreatedAt, p.CursorID = &cur.CreatedAt, &cur.ID
	}
	rows, err := s.q.ListReviewQueue(ctx, s.db, p)
	if err != nil {
		return pagination.Response[ReviewView]{}, err
	}
	views := make([]ReviewView, 0, len(rows))
	for _, r := range rows {
		views = append(views, ReviewView{ReviewID: r.ReviewID, VerificationID: r.VerificationID, VerificationType: r.VerificationType,
			Subject: domain.Subject{Type: domain.SubjectType(r.SubjectType), Domain: platform.Domain(r.SubjectDomain), ID: r.SubjectID},
			Status:  r.Status, Reason: r.Reason, AssignedAdminID: r.AssignedAdminID, AssignedAt: r.AssignedAt, CreatedAt: r.CreatedAt})
	}
	return pagination.Build(views, limit, func(v ReviewView) any { return reviewCursor{CreatedAt: v.CreatedAt, ID: v.ReviewID} })
}

func (s *Service) activeReview(ctx context.Context, tx pgx.Tx, id ids.ID) (db.ManualReview, error) {
	r, err := s.q.GetActiveReviewForUpdate(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, apperrors.Conflict(domain.CodeReviewNotOpen, "The verification has no open manual review.")
	}
	return r, err
}

// AssignReviewTx assigns the open review to the delegated admin.
func (s *Service) AssignReviewTx(ctx context.Context, tx pgx.Tx, id ids.ID) (ReviewView, error) {
	admin, err := RequireAdminActor(ctx)
	if err != nil {
		return ReviewView{}, err
	}
	c, err := s.lockCase(ctx, tx, id)
	if err != nil {
		return ReviewView{}, err
	}
	if c.Status != string(domain.StatusManualReview) {
		return ReviewView{}, apperrors.Conflict(domain.CodeReviewNotOpen, "The verification is not awaiting manual review.")
	}
	r, err := s.activeReview(ctx, tx, id)
	if err != nil {
		return ReviewView{}, err
	}
	if r.AssignedAdminID != nil && *r.AssignedAdminID != admin.ID {
		return ReviewView{}, apperrors.Conflict(domain.CodeReviewAssignedToOther, "The review is assigned to another administrator.")
	}
	r, err = s.q.AssignReview(ctx, tx, db.AssignReviewParams{ReviewID: r.ReviewID, AssignedAdminID: &admin.ID, AssignedAt: timePtr(s.now())})
	if err != nil {
		return ReviewView{}, err
	}
	if _, err := s.audit.Record(ctx, tx, audit.Event{Action: "verification.manual_review_assigned", TargetType: "verification",
		TargetID: id, Metadata: map[string]any{"review_id": r.ReviewID.String()}}); err != nil {
		return ReviewView{}, err
	}
	return ReviewView{ReviewID: r.ReviewID, VerificationID: r.VerificationID, VerificationType: c.VerificationType,
		Subject: domain.Subject{Type: domain.SubjectType(c.SubjectType), Domain: platform.Domain(c.SubjectDomain), ID: c.SubjectID},
		Status:  r.Status, Reason: r.Reason, AssignedAdminID: r.AssignedAdminID, AssignedAt: r.AssignedAt, CreatedAt: r.CreatedAt}, nil
}

// DecisionInput is a manual-review decision.
type DecisionInput struct {
	Decision   domain.Status // VERIFIED | REJECTED
	ReasonCode string
	Notes      string
}

// DecideReviewTx records an admin decision: MANUAL_REVIEW -> VERIFIED|REJECTED.
func (s *Service) DecideReviewTx(ctx context.Context, tx pgx.Tx, id ids.ID, in DecisionInput) (CaseView, error) {
	admin, err := RequireAdminActor(ctx)
	if err != nil {
		return CaseView{}, err
	}
	if in.Decision != domain.StatusVerified && in.Decision != domain.StatusRejected {
		return CaseView{}, apperrors.InvalidRequest("decision must be VERIFIED or REJECTED.")
	}
	if in.Decision == domain.StatusRejected && in.ReasonCode == "" {
		return CaseView{}, apperrors.InvalidRequest("reason_code is required for a rejection.")
	}
	if len(in.Notes) > 1000 {
		return CaseView{}, apperrors.InvalidRequest("notes must be at most 1000 characters.")
	}
	c, err := s.lockCase(ctx, tx, id)
	if err != nil {
		return CaseView{}, err
	}
	if err := domain.Machine.Transition(domain.Status(c.Status), in.Decision); err != nil || c.Status != string(domain.StatusManualReview) {
		if err == nil {
			err = apperrors.InvalidStateTransition(c.Status, string(in.Decision))
		}
		return CaseView{}, err
	}
	r, err := s.activeReview(ctx, tx, id)
	if err != nil {
		return CaseView{}, err
	}
	if r.AssignedAdminID != nil && *r.AssignedAdminID != admin.ID {
		return CaseView{}, apperrors.Conflict(domain.CodeReviewAssignedToOther, "The review is assigned to another administrator.")
	}
	now := s.now()
	var expires *time.Time
	ev := EventRejected
	if in.Decision == domain.StatusVerified {
		expires, ev = timePtr(now.Add(s.opts.Validity[domain.Type(c.VerificationType)])), EventVerified
	}
	updated, err := s.apply(ctx, tx, c, transition{to: in.Decision, decidedAt: &now, expiresAt: expires, reasonCode: in.ReasonCode,
		decisionSource: domain.SourceManualReview, event: ev})
	if err != nil {
		return CaseView{}, err
	}
	if err := s.q.DecideReview(ctx, tx, db.DecideReviewParams{ReviewID: r.ReviewID, DecidedAt: &now, AssignedAdminID: &admin.ID}); err != nil {
		return CaseView{}, err
	}
	if _, err := s.q.InsertDecision(ctx, tx, db.InsertDecisionParams{DecisionID: ids.New(), VerificationID: id, Decision: string(in.Decision),
		DecisionSource: string(domain.SourceManualReview), ReasonCode: strPtr(in.ReasonCode), Notes: strPtr(in.Notes),
		DecidedByService: string(platform.AdminService), DecidedByActorDomain: strPtr(admin.Domain), DecidedByActorID: &admin.ID,
		ExpiresAt: expires, CreatedAt: now}); err != nil {
		return CaseView{}, err
	}
	return toView(updated), nil
}
