package auth

import (
	"context"
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/sessions"
	"github.com/jackc/pgx/v5"
)

// All methods are bound to the command's PostgreSQL transaction. In particular,
// token misuse revocation must be committed even when the command returns a
// client authentication error; callers keep that error outside the transaction.
type sessionStore struct {
	q      *db.Queries
	tx     database.Querier
	create db.CreateSessionParams
}

func (s *sessionStore) CreateSession(ctx context.Context, v sessions.Session, t sessions.RefreshToken) error {
	p := s.create
	p.ID = v.ID
	p.AccountID = v.Subject
	p.CreatedAt = v.CreatedAt
	p.ExpiresAt = v.ExpiresAt
	p.LastRefreshedAt = v.LastRefreshedAt
	if err := s.q.CreateSession(ctx, s.tx, p); err != nil {
		return err
	}
	return s.q.CreateRefreshToken(ctx, s.tx, db.CreateRefreshTokenParams{ID: t.ID, SessionID: t.SessionID, TokenHash: t.Hash, IssuedAt: t.IssuedAt, ExpiresAt: t.ExpiresAt})
}
func (s *sessionStore) GetByTokenHash(ctx context.Context, hash string) (sessions.RefreshToken, sessions.Session, error) {
	r, err := s.q.GetRefreshToken(ctx, s.tx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return sessions.RefreshToken{}, sessions.Session{}, sessions.ErrNotFound
	}
	if err != nil {
		return sessions.RefreshToken{}, sessions.Session{}, err
	}
	t := sessions.RefreshToken{ID: r.ID, SessionID: r.SessionID, Hash: r.TokenHash, IssuedAt: r.IssuedAt, ExpiresAt: r.TokenExpiresAt, ConsumedAt: r.ConsumedAt}
	if r.ReplacedBy != nil {
		t.ReplacedBy = *r.ReplacedBy
	}
	return t, sessions.Session{ID: r.SessionID, Subject: r.AccountID, CreatedAt: r.CreatedAt, ExpiresAt: r.SessionExpiresAt, LastRefreshedAt: r.LastRefreshedAt, RevokedAt: r.RevokedAt, RevokeReason: r.RevokeReason}, nil
}
func (s *sessionStore) Rotate(ctx context.Context, old ids.ID, next sessions.RefreshToken, at time.Time) error {
	n, err := s.q.ConsumeRefreshToken(ctx, s.tx, db.ConsumeRefreshTokenParams{ID: old, ConsumedAt: &at, ReplacedBy: &next.ID})
	if err != nil {
		return err
	}
	if n != 1 {
		return sessions.ErrAlreadyConsumed
	}
	if err := s.q.CreateRefreshToken(ctx, s.tx, db.CreateRefreshTokenParams{ID: next.ID, SessionID: next.SessionID, TokenHash: next.Hash, IssuedAt: next.IssuedAt, ExpiresAt: next.ExpiresAt}); err != nil {
		return err
	}
	return s.q.TouchSession(ctx, s.tx, db.TouchSessionParams{ID: next.SessionID, LastRefreshedAt: at})
}
func (s *sessionStore) RevokeSession(ctx context.Context, id ids.ID, reason string, at time.Time) error {
	_, err := s.q.RevokeSession(ctx, s.tx, db.RevokeSessionParams{ID: id, RevokedAt: &at, RevokeReason: reason})
	return err
}
func (s *sessionStore) RevokeSubject(ctx context.Context, id ids.ID, reason string, at time.Time) error {
	return s.q.RevokeAccountSessions(ctx, s.tx, db.RevokeAccountSessionsParams{AccountID: id, RevokedAt: &at, RevokeReason: reason})
}
