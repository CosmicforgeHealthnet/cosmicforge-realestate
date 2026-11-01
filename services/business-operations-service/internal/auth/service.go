// Package auth owns business accounts and composes shared authentication primitives.
package auth

import (
	"context"
	"errors"
	"fmt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth/credentials"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/notification"
	"net/mail"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	sharedauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/passwords"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/sessions"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	Sealer    *credentials.Sealer
	Pool      *pgxpool.Pool
	Issuer    *sharedauth.Issuer
	Verifier  *sharedauth.Verifier
	Passwords *passwords.Hasher
	Pepper    security.Secret
	Outbox    *outbox.Store
	Now       func() time.Time
	dummy     string
	q         *db.Queries
}

func New(pool *pgxpool.Pool, issuer *sharedauth.Issuer, verifier *sharedauth.Verifier, hasher *passwords.Hasher, pepper security.Secret, store *outbox.Store, now func() time.Time) (*Service, error) {
	if pool == nil || issuer == nil || verifier == nil || hasher == nil || len(pepper.Reveal()) < 32 || store == nil {
		return nil, errors.New("business auth dependencies missing")
	}
	if now == nil {
		now = time.Now
	}
	dummy, err := hasher.Hash(context.Background(), security.RandomToken(32))
	if err != nil {
		return nil, err
	}
	return &Service{Pool: pool, Issuer: issuer, Verifier: verifier, Passwords: hasher, Pepper: pepper, Outbox: store, Now: now, dummy: dummy, q: &db.Queries{}}, nil
}
func (s *Service) hash(purpose, value string) string {
	return security.HMACSHA256Hex([]byte(s.Pepper.Reveal()), []byte(purpose+"\x00"+value))
}

// Role names carry no authority: each initial owner receives explicit local
// capabilities and organisation-bound inheritable grants.
var ownerCapabilities = []string{"reservations:read", "reservations:write", "reservations:approve", "reservations:confirm", "reservations:cancel", "reservations:modify", "reservations:overbook", "reservations:resolve", "reservation_risk:read", "reservation_risk:write", "rms:read", "rms:write", "rms:approve", "rms:autopilot", "pms:read", "pms:write", "pms:approve", "room_status:read", "room_status:write", "room_status:inspect", "folios:read", "folios:write", "folios:approve", "cashier:read", "cashier:write", "cashier:approve", "night_audit:read", "night_audit:write", "night_audit:approve", "guests:read", "guests:write", "guest_private:read", "guest_private:write", "guest_privacy:write", "corporate:read", "corporate:write", "loyalty:read", "loyalty:write", "loyalty:approve", "recovery:read", "recovery:write", "recovery:approve", "products:read", "products:write", "pools:read", "pools:write", "calendar:read", "calendar:write", "holds:read", "holds:write", "allotments:read", "allotments:write", "rates:read", "rates:write", "rates:approve", "restrictions:read", "restrictions:write", "promotions:read", "promotions:write", "pricing:read", "pricing:write", "pricing:approve", "content:read", "content:write", "publication:write", "publication:approve", "channels:read", "channels:write", "channels:credentials", "channels:mappings", "channels:override", "channels:resolve", "channels:report", "channels:approve", "tax:read", "tax:write", "portfolio:read", "portfolio:write", "development:read", "development:write", "parcel:read", "parcel:write", "property:read", "property:write", "structure:read", "structure:write", "assets:read", "assets:write", "imports:read", "imports:write", "inventory:restructure", "inventory:mappings", "inventory:verify", "inventory:media", "inventory:approve", "organisation:read", "organisation:write", "organisation:verify", "capacity:write", "hierarchy:read", "hierarchy:write", "staff:read", "staff:invite", "staff:write", "staff:approve", "roles:read", "roles:write", "roles:approve", "permissions:read", "scopes:write", "approvals:read", "approvals:create", "approvals:decide", "policies:read", "policies:write", "delegations:write", "break_glass:request", "break_glass:review", "audit:read"}

type Registration struct {
	Email            string `json:"email"`
	Password         string `json:"password"`
	DisplayName      string `json:"display_name"`
	OrganisationName string `json:"organisation_name"`
}
type Registered struct {
	AccountID         ids.ID `json:"account_id"`
	OrganisationID    ids.ID `json:"organisation_id"`
	OrganisationState string `json:"organisation_state"`
}

func (s *Service) Register(ctx context.Context, in Registration, key string) (Registered, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || len(email) > 254 || len(in.DisplayName) < 1 || len(in.DisplayName) > 200 || len(strings.TrimSpace(in.OrganisationName)) < 1 || len(in.OrganisationName) > 200 || len(key) < 8 || len(key) > 128 || passwords.DefaultPolicy.Validate(in.Password) != nil {
		return Registered{}, apperrors.InvalidRequest("Invalid registration fields or Idempotency-Key.")
	}
	hash, err := s.Passwords.Hash(ctx, in.Password)
	if err != nil {
		return Registered{}, err
	}
	keyHash := s.hash("register-key", key)
	fingerprint := s.hash("register-body", email+"\x00"+in.DisplayName+"\x00"+in.OrganisationName+"\x00"+in.Password)
	result := Registered{AccountID: ids.New(), OrganisationID: ids.New(), OrganisationState: "DRAFT"}
	err = database.WithTx(ctx, s.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, keyHash); err != nil {
			return err
		}
		var stored string
		err := tx.QueryRow(ctx, `SELECT request_hash,account_id,organisation_id FROM business_registration_keys WHERE key_hash=$1`, keyHash).Scan(&stored, &result.AccountID, &result.OrganisationID)
		if err == nil {
			if stored != fingerprint {
				return apperrors.Conflict("IDEMPOTENCY_CONFLICT", "Idempotency key conflicts with an earlier request.")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := s.q.CreateAccount(ctx, tx, db.CreateAccountParams{ID: result.AccountID, Email: email, DisplayName: in.DisplayName}); err != nil {
			if database.IsUniqueViolation(err, "") {
				return apperrors.Conflict("REGISTRATION_CONFLICT", "Registration cannot be completed with these details.")
			}
			return err
		}
		if err := s.q.CreateCredential(ctx, tx, db.CreateCredentialParams{AccountID: result.AccountID, PasswordHash: hash}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO organisations(id,representative_account_id,name) VALUES($1,$2,$3)`, result.OrganisationID, result.AccountID, in.OrganisationName); err != nil {
			return err
		}
		role, member := ids.New(), ids.New()
		if _, err := tx.Exec(ctx, `INSERT INTO role_templates(id,organisation_id,role_key,name,version,senior_emergency_eligible) VALUES($1,$2,$1,'Owner',1,true)`, role, result.OrganisationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO staff_memberships(id,organisation_id,account_id,role_id,kind,status,effective_at,scope_id) VALUES($1,$2,$3,$4,'OWNER','ACTIVE',$5,$2)`, member, result.OrganisationID, result.AccountID, role, s.Now().UTC()); err != nil {
			return err
		}
		for _, capability := range ownerCapabilities {
			if _, err := tx.Exec(ctx, `INSERT INTO permissions(code,description) VALUES($1,$1) ON CONFLICT DO NOTHING`, capability); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(role_id,permission) VALUES($1,$2)`, role, capability); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO staff_scope_grants(id,organisation_id,membership_id,capability,scope_kind,scope_id,inherit,effective_at) VALUES($1,$2,$3,$4,'COMPANY',$2,true,$5)`, ids.New(), result.OrganisationID, member, capability, s.Now().UTC()); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO business_registration_keys(key_hash,request_hash,account_id,organisation_id) VALUES($1,$2,$3,$4)`, keyHash, fingerprint, result.AccountID, result.OrganisationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,organisation_id,action,target_type,target_id,correlation_id) VALUES($1,$2,$3,'REGISTRATION','account',$2,$4)`, ids.New(), result.AccountID, result.OrganisationID, requestcontext.From(ctx).RequestID); err != nil {
			return err
		}
		if err := s.record(ctx, tx, &result.AccountID, "REGISTRATION", result.AccountID); err != nil {
			return err
		}
		event, err := events.New(ctx, platform.BusinessOperationsService, "business.account.registered.v1", "business_account", result.AccountID, map[string]any{"account_id": result.AccountID, "organisation_id": result.OrganisationID, "organisation_state": "DRAFT"})
		if err != nil {
			return err
		}
		return s.Outbox.Add(ctx, tx, event)
	})
	return result, err
}

func (s *Service) record(ctx context.Context, tx database.Querier, account *ids.ID, typ string, target ids.ID) error {
	if _, ok := tx.(pgx.Tx); !ok {
		return database.WithTx(ctx, s.Pool, database.TxOptions{}, func(ctx context.Context, owned pgx.Tx) error { return s.record(ctx, owned, account, typ, target) })
	}
	var targetValue any
	if !target.IsZero() {
		targetValue = target
	}
	_, err := tx.Exec(ctx, `INSERT INTO security_events(id,account_id,event_type,target_id,correlation_id,organisation_id) VALUES($1,$2,$3,$4,$5,$6)`, ids.New(), account, typ, targetValue, requestcontext.From(ctx).RequestID, requestcontext.From(ctx).OrganizationID)
	if err != nil {
		return err
	}
	if account != nil {
		var email string
		if err := tx.QueryRow(ctx, `SELECT email FROM business_accounts WHERE id=$1`, *account).Scan(&email); err != nil {
			return err
		}
		event, err := businessEvents.NotificationRequest(ctx, s.Sealer, ids.New(), notification.SendRequest{Recipient: notification.Recipient{Domain: string(platform.DomainBusinessOperations), ID: *account}, TemplateKey: notification.TemplateSecurityAlert, Channels: []string{notification.ChannelEmail}, Variables: map[string]string{"event": typ}, Destinations: notification.Destinations{Email: email}})
		if err != nil {
			return err
		}
		return s.Outbox.Add(ctx, tx, event)
	}
	return nil
}

func (s *Service) limit(ctx context.Context, key string, max int) error {
	var attempts int
	err := s.Pool.QueryRow(ctx, `INSERT INTO business_auth_limits(key_hash,window_start,attempts) VALUES($1,$2,1) ON CONFLICT(key_hash) DO UPDATE SET attempts=CASE WHEN business_auth_limits.window_start<$2-interval '15 minutes' THEN 1 ELSE business_auth_limits.attempts+1 END,window_start=CASE WHEN business_auth_limits.window_start<$2-interval '15 minutes' THEN $2 ELSE business_auth_limits.window_start END RETURNING attempts`, s.hash("auth-limit", key), s.Now().UTC()).Scan(&attempts)
	if err != nil {
		return err
	}
	if attempts > max {
		return apperrors.New(429, "RATE_LIMITED", "Please retry later.").WithRetryAfter(900)
	}
	return nil
}

func (s *Service) RateLimit(ctx context.Context, purpose string, max int) error {
	return s.limit(ctx, purpose, max)
}

func (s *Service) RecordSecurity(ctx context.Context, tx pgx.Tx, account ids.ID, eventType string, target ids.ID) error {
	return s.record(ctx, tx, &account, eventType, target)
}

type Login struct {
	Email          string `json:"email"`
	Password       string `json:"password"`
	OrganisationID ids.ID `json:"organisation_id,omitempty"`
	DeviceName     string `json:"device_name,omitempty"`
	DevicePlatform string `json:"device_platform,omitempty"`
}
type Tokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	SessionID    ids.ID    `json:"session_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}
type LoginResult struct {
	MFAKind        string  `json:"mfa_kind,omitempty"`
	DeliveryQueued bool    `json:"delivery_queued,omitempty"`
	Tokens         *Tokens `json:"tokens,omitempty"`
	MFAToken       string  `json:"mfa_token,omitempty"`
	MFARequired    bool    `json:"mfa_required"`
}

func (s *Service) Login(ctx context.Context, in Login, clientKey string) (LoginResult, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if len(email) > 254 || len(in.Password) > 4096 || len(in.DeviceName) > 100 || len(in.DevicePlatform) > 30 {
		return LoginResult{}, apperrors.InvalidRequest("Invalid login fields.")
	}
	if err := s.limit(ctx, "login-email:"+email, 10); err != nil {
		return LoginResult{}, err
	}
	if err := s.limit(ctx, "login-client:"+clientKey, 50); err != nil {
		return LoginResult{}, err
	}
	a, err := s.q.GetAccountCredential(ctx, s.Pool, email)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return LoginResult{}, err
	}
	stored := a.PasswordHash
	if stored == "" {
		stored = s.dummy
	}
	_, passwordErr := s.Passwords.Verify(ctx, in.Password, stored)
	if err != nil || passwordErr != nil || a.Status != "ACTIVE" {
		var account *ids.ID
		if !a.ID.IsZero() {
			account = &a.ID
		}
		if err := s.record(ctx, s.Pool, account, "LOGIN_FAILED", ids.Nil); err != nil {
			return LoginResult{}, err
		}
		return LoginResult{}, apperrors.New(401, "INVALID_CREDENTIALS", "Invalid credentials.")
	}
	var result LoginResult
	err = database.WithTx(ctx, s.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		var status, currentHash string
		if err := tx.QueryRow(ctx, `SELECT a.status,c.password_hash FROM business_accounts a JOIN business_credentials c ON c.account_id=a.id WHERE a.id=$1 FOR UPDATE OF a,c`, a.ID).Scan(&status, &currentHash); err != nil {
			return err
		}
		if status != "ACTIVE" || currentHash != stored {
			return apperrors.New(401, "INVALID_CREDENTIALS", "Invalid credentials.")
		}
		var mfa bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM business_mfa_methods WHERE account_id=$1 AND status='ACTIVE')`, a.ID).Scan(&mfa); err != nil {
			return err
		}
		if mfa {
			raw := security.RandomToken(32)
			challengeID := ids.New()
			_, err := tx.Exec(ctx, `INSERT INTO business_mfa_challenges(id,account_id,token_hash,purpose,expires_at,organisation_id,device_name,device_platform) VALUES($1,$2,$3,'LOGIN',$4,$5,$6,$7)`, challengeID, a.ID, s.hash("mfa-login", raw), s.Now().UTC().Add(5*time.Minute), nullableID(in.OrganisationID), in.DeviceName, in.DevicePlatform)
			if err != nil {
				return err
			}
			method, err := loadMethod(ctx, tx, a.ID, "ACTIVE")
			if err != nil {
				return err
			}
			queued := false
			if method.kind != "TOTP" {
				if _, err = s.queueMFAOTP(ctx, tx, method, challengeID, "LOGIN"); err != nil {
					return err
				}
				queued = true
			}
			result = LoginResult{MFAToken: raw, MFARequired: true, MFAKind: method.kind, DeliveryQueued: queued}
			return s.record(ctx, tx, &a.ID, "MFA_LOGIN_CHALLENGE", challengeID)
		}
		tokens, err := s.start(ctx, tx, a.ID, in.OrganisationID, in.DeviceName, in.DevicePlatform, nil)
		if err != nil {
			return err
		}
		result.Tokens = &tokens
		return s.record(ctx, tx, &a.ID, "LOGIN_SUCCEEDED", tokens.SessionID)
	})
	return result, err
}

func (s *Service) start(ctx context.Context, tx pgx.Tx, account, org ids.ID, name, platformName string, mfaAt *time.Time) (Tokens, error) {
	var orgRef *ids.ID
	if !org.IsZero() {
		var valid bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM staff_memberships m JOIN organisations o ON o.id=m.organisation_id WHERE m.account_id=$1 AND m.organisation_id=$2 AND m.status='ACTIVE' AND m.effective_at<=$3 AND (m.expires_at IS NULL OR m.expires_at>$3) AND o.state<>'CLOSED')`, account, org, s.Now().UTC()).Scan(&valid)
		if err != nil {
			return Tokens{}, err
		}
		if !valid {
			return Tokens{}, apperrors.PermissionDenied("Organisation access denied.")
		}
		orgRef = &org
	}
	device := ids.New()
	if _, err := tx.Exec(ctx, `INSERT INTO business_devices(id,account_id,label,platform,risk_flags) VALUES($1,$2,$3,$4,jsonb_build_array('UNTRUSTED_NEW_DEVICE'))`, device, account, name, platformName); err != nil {
		return Tokens{}, err
	}
	store := &sessionStore{q: s.q, tx: tx, create: db.CreateSessionParams{DeviceID: &device, OrganisationID: orgRef, MfaAt: mfaAt}}
	issued, err := sessions.NewManager(store, sessions.Config{Now: s.Now}).Start(ctx, account, sessions.Device{})
	if err != nil {
		return Tokens{}, err
	}
	return s.tokens(issued, org)
}
func (s *Service) tokens(issued sessions.Issued, org ids.ID) (Tokens, error) {
	access, claims, err := s.Issuer.Issue(sharedauth.IssueRequest{Subject: issued.Session.Subject, SessionID: issued.Session.ID, OrganizationID: org})
	if err != nil {
		return Tokens{}, err
	}
	return Tokens{AccessToken: access, RefreshToken: issued.RawToken, SessionID: issued.Session.ID, ExpiresAt: claims.ExpiresAt.Time()}, nil
}

func (s *Service) Refresh(ctx context.Context, raw string) (Tokens, error) {
	if len(raw) > 128 {
		return Tokens{}, apperrors.InvalidToken()
	}
	var result Tokens
	var clientErr error
	err := database.WithTx(ctx, s.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		store := &sessionStore{q: s.q, tx: tx}
		manager := sessions.NewManager(store, sessions.Config{Now: s.Now})
		issued, err := manager.Refresh(ctx, raw)
		if err != nil {
			if errors.Is(err, sessions.ErrTokenReuse) {
				var account, family ids.ID
				if err := tx.QueryRow(ctx, `SELECT s.account_id,s.id FROM business_refresh_tokens t JOIN business_sessions s ON s.id=t.session_id WHERE t.token_hash=$1`, sessions.HashToken(raw)).Scan(&account, &family); err != nil {
					return err
				}
				if err := s.record(ctx, tx, &account, "REFRESH_TOKEN_REUSE", family); err != nil {
					return err
				}
				clientErr = apperrors.InvalidToken()
				return nil
			}
			if errors.Is(err, sessions.ErrInvalidToken) || errors.Is(err, sessions.ErrSessionRevoked) || errors.Is(err, sessions.ErrSessionExpired) {
				clientErr = apperrors.InvalidToken()
				return nil
			}
			return err
		}
		account, err := s.q.GetAccount(ctx, tx, issued.Session.Subject)
		if err != nil {
			return err
		}
		if account.Status != "ACTIVE" {
			if err := manager.RevokeAll(ctx, account.ID, sessions.ReasonAdmin); err != nil {
				return err
			}
			clientErr = apperrors.InvalidToken()
			return nil
		}
		var org *ids.ID
		if err := tx.QueryRow(ctx, `SELECT organisation_id FROM business_sessions WHERE id=$1`, issued.Session.ID).Scan(&org); err != nil {
			return err
		}
		organisation := ids.Nil
		if org != nil {
			organisation = *org
		}
		result, err = s.tokens(issued, organisation)
		return err
	})
	if err != nil {
		return Tokens{}, err
	}
	return result, clientErr
}

func (s *Service) Authenticate(ctx context.Context, raw string) (sharedauth.Principal, db.BusinessSession, error) {
	p, err := s.Verifier.Verify(ctx, raw)
	if err != nil || p.Domain != platform.DomainBusinessOperations || p.SessionID.IsZero() {
		return sharedauth.Principal{}, db.BusinessSession{}, apperrors.InvalidToken()
	}
	session, err := s.q.GetLiveSession(ctx, s.Pool, db.GetLiveSessionParams{ID: p.SessionID, AccountID: p.ActorID})
	if errors.Is(err, pgx.ErrNoRows) {
		return p, session, apperrors.InvalidToken()
	}
	if err != nil {
		return p, session, err
	}
	if session.OrganisationID == nil && !p.OrganizationID.IsZero() || session.OrganisationID != nil && *session.OrganisationID != p.OrganizationID {
		return p, session, apperrors.InvalidToken()
	}
	return p, session, nil
}

func (s *Service) Revoke(ctx context.Context, account, session ids.ID, all bool) error {
	return database.WithTx(ctx, s.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		store := &sessionStore{q: s.q, tx: tx}
		at := s.Now().UTC()
		if all {
			if err := store.RevokeSubject(ctx, account, sessions.ReasonLogoutAll, at); err != nil {
				return err
			}
		} else {
			var owner ids.ID
			if err := tx.QueryRow(ctx, `SELECT account_id FROM business_sessions WHERE id=$1 FOR UPDATE`, session).Scan(&owner); errors.Is(err, pgx.ErrNoRows) {
				return apperrors.NotFound("SESSION_NOT_FOUND", "Session not found.")
			} else if err != nil {
				return err
			}
			if owner != account {
				return apperrors.NotFound("SESSION_NOT_FOUND", "Session not found.")
			}
			if err := store.RevokeSession(ctx, session, sessions.ReasonLogout, at); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, &account, "SESSION_REVOKED", session)
	})
}

func (s *Service) String() string {
	return fmt.Sprintf("BusinessAuth(%s)", platform.BusinessOperationsService)
}
