package usecases

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// Get returns a case visible to caller.
func (s *Service) Get(ctx context.Context, caller platform.Service, id ids.ID) (CaseView, error) {
	c, err := s.visible(ctx, caller, id)
	return toView(c), err
}

func (s *Service) visible(ctx context.Context, caller platform.Service, id ids.ID) (db.VerificationCase, error) {
	c, err := s.q.GetCase(ctx, s.db, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, domain.ErrNotFound()
	}
	if err != nil {
		return c, err
	}
	if !domain.CanRead(caller, platform.Domain(c.SubjectDomain), c.RequestedByService) {
		return c, domain.ErrNotFound()
	}
	return c, nil
}

// Result returns the decision and evidence for a case.
func (s *Service) Result(ctx context.Context, caller platform.Service, id ids.ID) (ResultView, error) {
	c, err := s.visible(ctx, caller, id)
	if err != nil {
		return ResultView{}, err
	}
	out := ResultView{VerificationID: c.VerificationID, Status: c.Status, Evidence: []EvidenceView{}}
	d, err := s.q.GetDecision(ctx, s.db, id)
	switch {
	case err == nil:
		out.Decision = &DecisionView{Decision: d.Decision, DecisionSource: d.DecisionSource, ReasonCode: d.ReasonCode,
			DecidedAt: d.CreatedAt, ExpiresAt: d.ExpiresAt}
	case !errors.Is(err, pgx.ErrNoRows):
		return ResultView{}, err
	}
	evs, err := s.q.ListEvidence(ctx, s.db, id)
	if err != nil {
		return ResultView{}, err
	}
	for _, e := range evs {
		out.Evidence = append(out.Evidence, EvidenceView{EvidenceID: e.EvidenceID, MediaID: e.MediaID, EvidenceType: e.EvidenceType, CreatedAt: e.CreatedAt})
	}
	return out, nil
}

type caseCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        ids.ID    `json:"i"`
}

func (s *Service) listBySubject(ctx context.Context, sub domain.Subject, typ string, cursor string, limit int) ([]db.VerificationCase, error) {
	var cur caseCursor
	has, err := pagination.Decode(cursor, &cur)
	if err != nil {
		return nil, err
	}
	p := db.ListCasesBySubjectParams{SubjectDomain: string(sub.Domain), SubjectType: string(sub.Type), SubjectID: sub.ID,
		VerificationType: strPtr(typ), RowLimit: int32(limit + 1)}
	if has {
		p.CursorCreatedAt, p.CursorID = &cur.CreatedAt, &cur.ID
	}
	return s.q.ListCasesBySubject(ctx, s.db, p)
}

// ListBySubject lists cases for a subject the caller may read.
func (s *Service) ListBySubject(ctx context.Context, caller platform.Service, sub domain.Subject, typ, cursor string, limit int) (pagination.Response[CaseView], error) {
	if !domain.CanRead(caller, sub.Domain, "") {
		return pagination.Build[CaseView](nil, limit, nil)
	}
	rows, err := s.listBySubject(ctx, sub, typ, cursor, limit)
	if err != nil {
		return pagination.Response[CaseView]{}, err
	}
	views := make([]CaseView, 0, len(rows))
	for _, r := range rows {
		views = append(views, toView(r))
	}
	return pagination.Build(views, limit, func(v CaseView) any { return caseCursor{CreatedAt: v.CreatedAt, ID: v.VerificationID} })
}

func actorSubject(ctx context.Context) (domain.Subject, bool) {
	a, ok := requestcontext.ActorFrom(ctx)
	if !ok {
		return domain.Subject{}, false
	}
	return domain.Subject{Type: domain.SubjectUser, Domain: platform.Domain(a.Domain), ID: a.ID}, true
}

// GetForActor returns a case only if its subject is the authenticated user.
func (s *Service) GetForActor(ctx context.Context, id ids.ID) (PublicCaseView, error) {
	sub, ok := actorSubject(ctx)
	if !ok {
		return PublicCaseView{}, domain.ErrNotFound()
	}
	c, err := s.q.GetCase(ctx, s.db, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (c.SubjectType != string(sub.Type) || c.SubjectDomain != string(sub.Domain) || c.SubjectID != sub.ID)) {
		return PublicCaseView{}, domain.ErrNotFound()
	}
	if err != nil {
		return PublicCaseView{}, err
	}
	return toPublic(c), nil
}

// ListForActor lists the authenticated user's own cases.
func (s *Service) ListForActor(ctx context.Context, typ, cursor string, limit int) (pagination.Response[PublicCaseView], error) {
	sub, ok := actorSubject(ctx)
	if !ok {
		return pagination.Build[PublicCaseView](nil, limit, nil)
	}
	rows, err := s.listBySubject(ctx, sub, typ, cursor, limit)
	if err != nil {
		return pagination.Response[PublicCaseView]{}, err
	}
	views := make([]PublicCaseView, 0, len(rows))
	for _, r := range rows {
		views = append(views, toPublic(r))
	}
	return pagination.Build(views, limit, func(v PublicCaseView) any { return caseCursor{CreatedAt: v.CreatedAt, ID: v.VerificationID} })
}
