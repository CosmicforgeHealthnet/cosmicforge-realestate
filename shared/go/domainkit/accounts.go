package domainkit

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/passwords"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/sessions"
)

// Accounts is the domain's own account authority (one per service, tables
// in that service's database). It reuses the shared token issuer, refresh
// session manager and password hasher.
type Accounts struct {
	rt       *Runtime
	issuer   *auth.Issuer
	hasher   *passwords.Hasher
	sessions *sessions.Manager
	policy   passwords.Policy
	dummy    string
	// DefaultRoles are granted to self-registered accounts (empty for
	// domains whose roles must be granted, e.g. admin).
	DefaultRoles []string
	// SelfRegistration disables /auth/register when false.
	SelfRegistration bool
}

// NewAccounts wires the account authority.
func NewAccounts(rt *Runtime) (*Accounts, error) {
	issuer, err := auth.NewIssuer(auth.IssuerConfig{Issuer: string(rt.Cfg.Service), TTL: rt.Cfg.AccessTTL, Key: rt.Cfg.TokenKey, Now: rt.Now})
	if err != nil {
		return nil, err
	}
	params := passwords.DefaultParams
	if rt.Cfg.Environment == "test" {
		params.MemoryKiB, params.Iterations = 8*1024, 1
	}
	hasher, err := passwords.NewHasher(params, 4)
	if err != nil {
		return nil, err
	}
	dummy, err := hasher.Hash(context.Background(), "dummy-password-for-timing-only")
	if err != nil {
		return nil, err
	}
	a := &Accounts{rt: rt, issuer: issuer, hasher: hasher, policy: passwords.DefaultPolicy, dummy: dummy, SelfRegistration: true}
	a.sessions = sessions.NewManager(&sessionStore{rt: rt}, sessions.Config{MaxSessionLifetime: rt.Cfg.SessionTTL, Now: rt.Now})
	return a, nil
}

type registration struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Tokens is the login/refresh response.
type Tokens struct {
	AccountID        ids.ID    `json:"account_id"`
	AccessToken      string    `json:"access_token"`
	AccessExpiresAt  time.Time `json:"access_token_expires_at"`
	RefreshToken     string    `json:"refresh_token"`
	RefreshExpiresAt time.Time `json:"refresh_token_expires_at"`
	SessionID        ids.ID    `json:"session_id"`
	TokenType        string    `json:"token_type"`
}

func normalizeEmail(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	addr, err := mail.ParseAddress(s)
	return s, err == nil && addr.Address == s && len(s) <= 254
}

func (a *Accounts) register(w http.ResponseWriter, r *http.Request) {
	if !a.SelfRegistration {
		httpx.WriteError(w, r, apperrors.PermissionDenied("Accounts in this domain are provisioned, not self-registered."))
		return
	}
	var in registration
	a.rt.Command(w, r, "accounts.register", &in, func(ctx context.Context, tx pgx.Tx) (Change, error) {
		email, ok := normalizeEmail(in.Email)
		name := strings.TrimSpace(in.DisplayName)
		if !ok || len(name) < 2 || len(name) > 120 {
			return Change{}, apperrors.InvalidRequest("A valid email and display name are required.")
		}
		if err := a.policy.Validate(in.Password); err != nil {
			return Change{}, apperrors.InvalidRequest("The password does not meet the platform policy.")
		}
		hash, err := a.hasher.Hash(ctx, in.Password)
		if err != nil {
			return Change{}, err
		}
		roles := append([]string{}, a.DefaultRoles...)
		for _, role := range a.rt.Cfg.BootstrapRoles[email] {
			if !slices.Contains(roles, role) {
				roles = append(roles, role)
			}
		}
		id := ids.New()
		if _, err = tx.Exec(ctx, `INSERT INTO domain_accounts(id,email,password_hash,display_name,roles,status) VALUES($1,$2,$3,$4,$5,'ACTIVE')`, id, email, hash, name, roles); err != nil {
			if database.IsUniqueViolation(err, "") {
				return Change{}, apperrors.Conflict("ACCOUNT_EXISTS", "An account with this email already exists.")
			}
			return Change{}, err
		}
		ev, err := a.rt.Event(ctx, a.rt.Cfg.Area+".account.registered.v1", "account", id, map[string]any{"account_id": id, "roles": roles})
		if err != nil {
			return Change{}, err
		}
		return Change{Status: http.StatusCreated, Body: Account{ID: id, Email: email, DisplayName: name, Roles: roles, Status: "ACTIVE"},
			Audit:  &audit.Event{Action: "account.registered", TargetType: "account", TargetID: id, Changes: map[string]any{"roles": roles}},
			Events: []events.Envelope{ev}}, nil
	})
}

func (a *Accounts) login(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := httpx.DecodeJSON(w, r, &in, 16<<10); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	email, _ := normalizeEmail(in.Email)
	var id ids.ID
	var hash, status string
	err := a.rt.Pool.QueryRow(r.Context(), `SELECT id,password_hash,status FROM domain_accounts WHERE email=$1`, email).Scan(&id, &hash, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		_, _ = a.hasher.Verify(r.Context(), in.Password, a.dummy)
		httpx.WriteError(w, r, apperrors.New(http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid email or password."))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// Verify returns (needsRehash, nil) on match and ErrMismatch otherwise.
	_, err = a.hasher.Verify(r.Context(), in.Password, hash)
	if err != nil && !errors.Is(err, passwords.ErrMismatch) {
		httpx.WriteError(w, r, err)
		return
	}
	if err != nil || status != "ACTIVE" {
		httpx.WriteError(w, r, apperrors.New(http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid email or password."))
		return
	}
	issued, err := a.sessions.Start(r.Context(), id, sessions.Device{UserAgent: truncate(r.UserAgent(), 300)})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	a.respondTokens(w, r, id, issued)
}

func (a *Accounts) refresh(w http.ResponseWriter, r *http.Request) {
	var in refreshRequest
	if err := httpx.DecodeJSON(w, r, &in, 16<<10); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	issued, err := a.sessions.Refresh(r.Context(), in.RefreshToken)
	if err != nil {
		httpx.WriteError(w, r, apperrors.New(http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "The refresh token is invalid, expired or already used."))
		return
	}
	var status string
	if err = a.rt.Pool.QueryRow(r.Context(), `SELECT status FROM domain_accounts WHERE id=$1`, issued.Session.Subject).Scan(&status); err != nil || status != "ACTIVE" {
		httpx.WriteError(w, r, apperrors.New(http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "The account is not active."))
		return
	}
	a.respondTokens(w, r, issued.Session.Subject, issued)
}

func (a *Accounts) respondTokens(w http.ResponseWriter, r *http.Request, id ids.ID, issued sessions.Issued) {
	token, claims, err := a.issuer.Issue(auth.IssueRequest{Subject: id, SessionID: issued.Session.ID})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, Tokens{AccountID: id, AccessToken: token, AccessExpiresAt: claims.ExpiresAt.Time(), RefreshToken: issued.RawToken, RefreshExpiresAt: issued.TokenExpiresAt, SessionID: issued.Session.ID, TokenType: "Bearer"})
}

func (a *Accounts) me(w http.ResponseWriter, r *http.Request) {
	acc, _ := AccountFrom(r.Context())
	httpx.WriteData(w, http.StatusOK, acc)
}

func (a *Accounts) logout(w http.ResponseWriter, r *http.Request) {
	acc, _ := AccountFrom(r.Context())
	if err := a.sessions.RevokeAll(r.Context(), acc.ID, "LOGOUT"); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetStatus suspends or reactivates an account and revokes its sessions when
// suspended (used by domain administrators).
func (a *Accounts) SetStatus(ctx context.Context, tx pgx.Tx, id ids.ID, status string) error {
	if status != "ACTIVE" && status != "SUSPENDED" {
		return apperrors.InvalidRequest("Unsupported account status.")
	}
	tag, err := tx.Exec(ctx, `UPDATE domain_accounts SET status=$2,version=version+1,updated_at=now() WHERE id=$1`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.NotFound("ACCOUNT_NOT_FOUND", "Account not found.")
	}
	if status == "SUSPENDED" {
		_, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=now(),revoke_reason='ACCOUNT_SUSPENDED' WHERE account_id=$1 AND revoked_at IS NULL`, id)
	}
	return err
}

// GrantRoles replaces an account's roles (caller authorizes).
func (a *Accounts) GrantRoles(ctx context.Context, tx pgx.Tx, id ids.ID, roles []string) error {
	tag, err := tx.Exec(ctx, `UPDATE domain_accounts SET roles=$2,version=version+1,updated_at=now() WHERE id=$1`, id, roles)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.NotFound("ACCOUNT_NOT_FOUND", "Account not found.")
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// sessionStore implements sessions.Store over the service's own tables.
type sessionStore struct{ rt *Runtime }

func (s *sessionStore) CreateSession(ctx context.Context, sess sessions.Session, t sessions.RefreshToken) error {
	return database.WithTx(ctx, s.rt.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO auth_sessions(id,account_id,created_at,expires_at,last_refreshed_at,user_agent) VALUES($1,$2,$3,$4,$5,$6)`, sess.ID, sess.Subject, sess.CreatedAt, sess.ExpiresAt, sess.LastRefreshedAt, sess.Device.UserAgent); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO auth_refresh_tokens(id,session_id,token_hash,issued_at,expires_at) VALUES($1,$2,$3,$4,$5)`, t.ID, t.SessionID, t.Hash, t.IssuedAt, t.ExpiresAt)
		return err
	})
}

func (s *sessionStore) GetByTokenHash(ctx context.Context, hash string) (sessions.RefreshToken, sessions.Session, error) {
	var t sessions.RefreshToken
	var sess sessions.Session
	var replaced *ids.ID
	var reason *string
	err := s.rt.Pool.QueryRow(ctx, `SELECT t.id,t.session_id,t.token_hash,t.issued_at,t.expires_at,t.consumed_at,t.replaced_by,s.id,s.account_id,s.created_at,s.expires_at,s.last_refreshed_at,s.revoked_at,s.revoke_reason FROM auth_refresh_tokens t JOIN auth_sessions s ON s.id=t.session_id WHERE t.token_hash=$1`, hash).
		Scan(&t.ID, &t.SessionID, &t.Hash, &t.IssuedAt, &t.ExpiresAt, &t.ConsumedAt, &replaced, &sess.ID, &sess.Subject, &sess.CreatedAt, &sess.ExpiresAt, &sess.LastRefreshedAt, &sess.RevokedAt, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, sess, sessions.ErrNotFound
	}
	if replaced != nil {
		t.ReplacedBy = *replaced
	}
	if reason != nil {
		sess.RevokeReason = *reason
	}
	return t, sess, err
}

func (s *sessionStore) Rotate(ctx context.Context, oldID ids.ID, next sessions.RefreshToken, at time.Time) error {
	return database.WithTx(ctx, s.rt.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE auth_refresh_tokens SET consumed_at=$2,replaced_by=$3 WHERE id=$1 AND consumed_at IS NULL`, oldID, at, next.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return sessions.ErrAlreadyConsumed
		}
		if _, err = tx.Exec(ctx, `INSERT INTO auth_refresh_tokens(id,session_id,token_hash,issued_at,expires_at) VALUES($1,$2,$3,$4,$5)`, next.ID, next.SessionID, next.Hash, next.IssuedAt, next.ExpiresAt); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE auth_sessions SET last_refreshed_at=$2 WHERE id=$1`, next.SessionID, at)
		return err
	})
}

func (s *sessionStore) RevokeSession(ctx context.Context, id ids.ID, reason string, at time.Time) error {
	_, err := s.rt.Pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at=$2,revoke_reason=$3 WHERE id=$1 AND revoked_at IS NULL`, id, at, reason)
	return err
}

func (s *sessionStore) RevokeSubject(ctx context.Context, subject ids.ID, reason string, at time.Time) error {
	_, err := s.rt.Pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at=$2,revoke_reason=$3 WHERE account_id=$1 AND revoked_at IS NULL`, subject, at, reason)
	return err
}

// ActorID returns the delegated actor id (for audit and ownership checks).
func ActorID(ctx context.Context) ids.ID {
	a, _ := requestcontext.ActorFrom(ctx)
	return a.ID
}
