package usecases

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/sessions"
)

// sessionStore implements shared/go/sessions.Store on individual_owner_db so
// the certified rotation, reuse-detection and revocation logic is reused. It
// is bound to one Querier (usually a transaction); create carries the
// owner-specific session columns for CreateSession.
type sessionStore struct {
	q      *db.Queries
	db     database.Querier
	create *db.CreateSessionParams
}

func (st *sessionStore) CreateSession(ctx context.Context, s sessions.Session, t sessions.RefreshToken) error {
	if st.create == nil {
		return errors.New("usecases: session creation metadata missing")
	}
	p := *st.create
	p.ID, p.AccountID, p.CreatedAt, p.ExpiresAt, p.LastRefreshedAt = s.ID, s.Subject, s.CreatedAt, s.ExpiresAt, s.LastRefreshedAt
	if err := st.q.CreateSession(ctx, st.db, p); err != nil {
		return err
	}
	return st.q.CreateRefreshToken(ctx, st.db, db.CreateRefreshTokenParams{ID: t.ID, SessionID: t.SessionID, TokenHash: t.Hash, IssuedAt: t.IssuedAt, ExpiresAt: t.ExpiresAt})
}

func (st *sessionStore) GetByTokenHash(ctx context.Context, hash string) (sessions.RefreshToken, sessions.Session, error) {
	r, err := st.q.GetRefreshTokenByHash(ctx, st.db, hash)
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
	s := sessions.Session{ID: r.SessionID, Subject: r.AccountID, CreatedAt: r.CreatedAt, ExpiresAt: r.SessionExpiresAt,
		LastRefreshedAt: r.LastRefreshedAt, RevokedAt: r.RevokedAt}
	if r.RevokeReason != nil {
		s.RevokeReason = *r.RevokeReason
	}
	if r.DeviceID != nil {
		s.Device.DeviceID = r.DeviceID.String()
	}
	return t, s, nil
}

// Rotate is a compare-and-set on the old token inside the caller's transaction.
func (st *sessionStore) Rotate(ctx context.Context, oldID ids.ID, next sessions.RefreshToken, at time.Time) error {
	n, err := st.q.ConsumeRefreshToken(ctx, st.db, db.ConsumeRefreshTokenParams{ID: oldID, ConsumedAt: &at, ReplacedBy: &next.ID})
	if err != nil {
		return err
	}
	if n != 1 {
		return sessions.ErrAlreadyConsumed
	}
	if err := st.q.CreateRefreshToken(ctx, st.db, db.CreateRefreshTokenParams{ID: next.ID, SessionID: next.SessionID, TokenHash: next.Hash, IssuedAt: next.IssuedAt, ExpiresAt: next.ExpiresAt}); err != nil {
		return err
	}
	return st.q.TouchSession(ctx, st.db, db.TouchSessionParams{ID: next.SessionID, LastRefreshedAt: at})
}

func (st *sessionStore) RevokeSession(ctx context.Context, sessionID ids.ID, reason string, at time.Time) error {
	_, err := st.q.RevokeSession(ctx, st.db, db.RevokeSessionParams{ID: sessionID, RevokedAt: &at, RevokeReason: &reason})
	return err
}

func (st *sessionStore) RevokeSubject(ctx context.Context, subject ids.ID, reason string, at time.Time) error {
	_, err := st.q.RevokeAccountSessions(ctx, st.db, db.RevokeAccountSessionsParams{Now: at, Reason: reason, AccountID: subject})
	return err
}
