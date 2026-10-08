package sessions

import (
	"context"
	"sync"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// MemoryStore is a concurrency-safe in-memory Store for tests and as a
// behavioural reference for database implementations. Never use it in a
// deployed service: sessions must survive restarts and scale-out.
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[ids.ID]Session
	tokens   map[ids.ID]RefreshToken
	byHash   map[string]ids.ID
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: map[ids.ID]Session{}, tokens: map[ids.ID]RefreshToken{}, byHash: map[string]ids.ID{}}
}

func (s *MemoryStore) CreateSession(_ context.Context, sess Session, t RefreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
	s.tokens[t.ID] = t
	s.byHash[t.Hash] = t.ID
	return nil
}

func (s *MemoryStore) GetByTokenHash(_ context.Context, hash string) (RefreshToken, Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byHash[hash]
	if !ok {
		return RefreshToken{}, Session{}, ErrNotFound
	}
	t := s.tokens[id]
	return t, s.sessions[t.SessionID], nil
}

func (s *MemoryStore) Rotate(_ context.Context, oldID ids.ID, next RefreshToken, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.tokens[oldID]
	if !ok {
		return ErrNotFound
	}
	if old.ConsumedAt != nil {
		return ErrAlreadyConsumed
	}
	old.ConsumedAt = &at
	old.ReplacedBy = next.ID
	s.tokens[oldID] = old
	s.tokens[next.ID] = next
	s.byHash[next.Hash] = next.ID
	sess := s.sessions[old.SessionID]
	sess.LastRefreshedAt = at
	s.sessions[old.SessionID] = sess
	return nil
}

func (s *MemoryStore) RevokeSession(_ context.Context, id ids.ID, reason string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return ErrNotFound
	}
	if sess.RevokedAt == nil {
		sess.RevokedAt, sess.RevokeReason = &at, reason
		s.sessions[id] = sess
	}
	return nil
}

func (s *MemoryStore) RevokeSubject(_ context.Context, subject ids.ID, reason string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.sessions {
		if sess.Subject == subject && sess.RevokedAt == nil {
			sess.RevokedAt, sess.RevokeReason = &at, reason
			s.sessions[id] = sess
		}
	}
	return nil
}

// Session returns a copy of a stored session (test helper).
func (s *MemoryStore) Session(id ids.ID) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	return sess, ok
}
