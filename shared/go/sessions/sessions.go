// Package sessions provides reusable refresh-session primitives
// (AUTHENTICATION_MODEL.md):
//
//   - opaque, cryptographically random refresh tokens (256-bit);
//   - only a SHA-256 hash of each token is ever stored;
//   - a session is the token family; every successful refresh rotates the token;
//   - presenting an already-rotated (consumed) or revoked token is treated as
//     reuse/theft and revokes the whole session;
//   - absolute session lifetime defaults to 30 days and is configurable;
//   - explicit revocation of one session or all sessions of a subject.
//
// Persistence is behind Store. Each user-domain service implements Store with
// its own tables in its own database (see schema_reference.sql); this package
// never creates shared session storage. MemoryStore is a reference/test
// implementation.
package sessions

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// TokenPrefix marks refresh tokens so they are recognisable in secret scanners.
const TokenPrefix = "cfrt_"

// DefaultMaxSessionLifetime is the default absolute session lifetime.
const DefaultMaxSessionLifetime = 30 * 24 * time.Hour

var (
	ErrNotFound        = errors.New("sessions: not found")
	ErrInvalidToken    = errors.New("sessions: invalid refresh token")
	ErrSessionRevoked  = errors.New("sessions: session revoked")
	ErrSessionExpired  = errors.New("sessions: session expired")
	ErrTokenReuse      = errors.New("sessions: refresh token reuse detected; session revoked")
	ErrAlreadyConsumed = errors.New("sessions: token already consumed")
	ErrInvalidSubject  = errors.New("sessions: subject must be a UUIDv7")
)

// Revocation reasons.
const (
	ReasonLogout         = "LOGOUT"
	ReasonTokenReuse     = "TOKEN_REUSE"
	ReasonAdmin          = "ADMIN_REVOKED"
	ReasonPasswordChange = "PASSWORD_CHANGED"
	ReasonLogoutAll      = "LOGOUT_ALL"
)

// Device describes the client a session was created from. Values are
// informational; never use them for authorization.
type Device struct {
	DeviceID  string
	Name      string
	Platform  string // ios|android|web|...
	UserAgent string
	IPAddress string
}

// Session is a refresh-token family.
type Session struct {
	ID              ids.ID
	Subject         ids.ID
	CreatedAt       time.Time
	ExpiresAt       time.Time
	LastRefreshedAt time.Time
	RevokedAt       *time.Time
	RevokeReason    string
	Device          Device
}

// Active reports whether the session can still be refreshed at t.
func (s Session) Active(t time.Time) bool { return s.RevokedAt == nil && t.Before(s.ExpiresAt) }

// RefreshToken is a stored token record. Hash is the SHA-256 of the raw token.
type RefreshToken struct {
	ID         ids.ID
	SessionID  ids.ID
	Hash       string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	ReplacedBy ids.ID
}

// Store persists sessions and token hashes. Implementations must make Rotate
// atomic: it succeeds only if oldID is still unconsumed (compare-and-set, e.g.
// UPDATE ... WHERE id=$1 AND consumed_at IS NULL), otherwise ErrAlreadyConsumed.
type Store interface {
	CreateSession(ctx context.Context, s Session, t RefreshToken) error
	GetByTokenHash(ctx context.Context, hash string) (RefreshToken, Session, error)
	Rotate(ctx context.Context, oldID ids.ID, next RefreshToken, at time.Time) error
	RevokeSession(ctx context.Context, sessionID ids.ID, reason string, at time.Time) error
	RevokeSubject(ctx context.Context, subject ids.ID, reason string, at time.Time) error
}

// Config configures the manager.
type Config struct {
	MaxSessionLifetime time.Duration // absolute; default 30 days
	// TokenIdleTTL bounds how long an individual refresh token is usable
	// (defaults to the session lifetime). Expired-but-unconsumed tokens are
	// rejected without revoking the session.
	TokenIdleTTL time.Duration
	Now          func() time.Time
}

// Manager implements issuance, rotation, reuse detection and revocation.
type Manager struct {
	store Store
	cfg   Config
}

// NewManager validates configuration.
func NewManager(store Store, cfg Config) *Manager {
	if cfg.MaxSessionLifetime <= 0 {
		cfg.MaxSessionLifetime = DefaultMaxSessionLifetime
	}
	if cfg.TokenIdleTTL <= 0 || cfg.TokenIdleTTL > cfg.MaxSessionLifetime {
		cfg.TokenIdleTTL = cfg.MaxSessionLifetime
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Manager{store: store, cfg: cfg}
}

// Issued is returned to the domain; RawToken is shown to the client exactly once.
type Issued struct {
	Session        Session
	TokenID        ids.ID
	RawToken       string
	TokenExpiresAt time.Time
}

// NewRawToken generates a refresh token and its storage hash.
func NewRawToken() (raw, hash string) {
	raw = TokenPrefix + security.RandomToken(32)
	return raw, HashToken(raw)
}

// HashToken returns the storage hash of a raw token.
func HashToken(raw string) string { return security.SHA256Hex([]byte(raw)) }

func (m *Manager) newToken(sessionID ids.ID, now, sessionExpiry time.Time) (RefreshToken, string) {
	raw, hash := NewRawToken()
	exp := now.Add(m.cfg.TokenIdleTTL)
	if exp.After(sessionExpiry) {
		exp = sessionExpiry
	}
	return RefreshToken{ID: ids.New(), SessionID: sessionID, Hash: hash, IssuedAt: now, ExpiresAt: exp}, raw
}

// Start creates a new session (token family) for subject.
func (m *Manager) Start(ctx context.Context, subject ids.ID, device Device) (Issued, error) {
	if subject.ValidateV7() != nil {
		return Issued{}, ErrInvalidSubject
	}
	now := m.cfg.Now().UTC()
	s := Session{ID: ids.New(), Subject: subject, CreatedAt: now, LastRefreshedAt: now,
		ExpiresAt: now.Add(m.cfg.MaxSessionLifetime), Device: device}
	t, raw := m.newToken(s.ID, now, s.ExpiresAt)
	if err := m.store.CreateSession(ctx, s, t); err != nil {
		return Issued{}, err
	}
	return Issued{Session: s, TokenID: t.ID, RawToken: raw, TokenExpiresAt: t.ExpiresAt}, nil
}

// Refresh validates raw, rotates it and returns the replacement. Reuse of a
// consumed token revokes the session and returns ErrTokenReuse.
func (m *Manager) Refresh(ctx context.Context, raw string) (Issued, error) {
	if !strings.HasPrefix(raw, TokenPrefix) || len(raw) > 128 {
		return Issued{}, ErrInvalidToken
	}
	now := m.cfg.Now().UTC()
	tok, sess, err := m.store.GetByTokenHash(ctx, HashToken(raw))
	if errors.Is(err, ErrNotFound) {
		return Issued{}, ErrInvalidToken
	}
	if err != nil {
		return Issued{}, err
	}
	if sess.RevokedAt != nil {
		return Issued{}, ErrSessionRevoked
	}
	if tok.ConsumedAt != nil {
		if rerr := m.store.RevokeSession(ctx, sess.ID, ReasonTokenReuse, now); rerr != nil {
			return Issued{}, rerr
		}
		return Issued{}, ErrTokenReuse
	}
	if !now.Before(sess.ExpiresAt) {
		return Issued{}, ErrSessionExpired
	}
	if !now.Before(tok.ExpiresAt) {
		return Issued{}, ErrInvalidToken
	}
	next, nextRaw := m.newToken(sess.ID, now, sess.ExpiresAt)
	if err := m.store.Rotate(ctx, tok.ID, next, now); err != nil {
		if errors.Is(err, ErrAlreadyConsumed) {
			// Lost a race with another presentation of the same token: reuse.
			if rerr := m.store.RevokeSession(ctx, sess.ID, ReasonTokenReuse, now); rerr != nil {
				return Issued{}, rerr
			}
			return Issued{}, ErrTokenReuse
		}
		return Issued{}, err
	}
	sess.LastRefreshedAt = now
	return Issued{Session: sess, TokenID: next.ID, RawToken: nextRaw, TokenExpiresAt: next.ExpiresAt}, nil
}

// Revoke ends a session (logout).
func (m *Manager) Revoke(ctx context.Context, sessionID ids.ID, reason string) error {
	return m.store.RevokeSession(ctx, sessionID, reason, m.cfg.Now().UTC())
}

// RevokeAll ends every session of subject (password change, compromise).
func (m *Manager) RevokeAll(ctx context.Context, subject ids.ID, reason string) error {
	return m.store.RevokeSubject(ctx, subject, reason, m.cfg.Now().UTC())
}
