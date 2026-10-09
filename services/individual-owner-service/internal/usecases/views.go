package usecases

import (
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Event types and aggregate (Section 10 catalogue).
const (
	AggregateOwnerAccount     = "owner_account"
	EventAccountActivated     = "owner.account.activated.v1"
	EventAccountRestricted    = "owner.account.restricted.v1"
	AccountEventSchemaVersion = 1
)

// AccountView is the owner's own account (API DTO; never the DB row).
type AccountView struct {
	AccountID          ids.ID     `json:"account_id"`
	Status             string     `json:"status"`
	Email              string     `json:"email"`
	EmailVerified      bool       `json:"email_verified"`
	Phone              *string    `json:"phone"`
	PhoneVerified      bool       `json:"phone_verified"`
	DisplayName        string     `json:"display_name"`
	Locale             string     `json:"locale"`
	MFAEnabled         bool       `json:"mfa_enabled"`
	ClosureEffectiveAt *time.Time `json:"closure_effective_at"`
	Version            int64      `json:"version"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func accountView(a db.OwnerAccount) AccountView {
	return AccountView{AccountID: a.AccountID, Status: a.Status, Email: a.Email, EmailVerified: a.EmailVerifiedAt != nil,
		Phone: a.PhoneE164, PhoneVerified: a.PhoneVerifiedAt != nil, DisplayName: a.DisplayName, Locale: a.Locale,
		MFAEnabled: a.MfaEnabled, ClosureEffectiveAt: a.ClosureEffectiveAt, Version: a.Version, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
}

// AdminAccountView is the operator projection (admin-service only).
type AdminAccountView struct {
	AccountView
	StatusReason     *string             `json:"status_reason"`
	StatusChangedAt  time.Time           `json:"status_changed_at"`
	FailedLoginCount int32               `json:"failed_login_count"`
	LockedUntil      *time.Time          `json:"locked_until"`
	History          []StatusHistoryView `json:"status_history"`
}

// StatusHistoryView is one lifecycle transition.
type StatusHistoryView struct {
	FromStatus          *string   `json:"from_status"`
	ToStatus            string    `json:"to_status"`
	ReasonCode          string    `json:"reason_code"`
	ActorType           string    `json:"actor_type"`
	ActorDomain         *string   `json:"actor_domain"`
	ActorID             *ids.ID   `json:"actor_id"`
	ResolutionReference *string   `json:"resolution_reference"`
	AccountVersion      int64     `json:"account_version"`
	OccurredAt          time.Time `json:"occurred_at"`
}

// TokenPair is returned once per session issuance/refresh.
type TokenPair struct {
	TokenType             string    `json:"token_type"`
	AccessToken           string    `json:"access_token"`
	AccessTokenExpiresAt  time.Time `json:"access_token_expires_at"`
	ExpiresIn             int       `json:"expires_in"`
	RefreshToken          string    `json:"refresh_token"`
	RefreshTokenExpiresAt time.Time `json:"refresh_token_expires_at"`
	SessionID             ids.ID    `json:"session_id"`
}

// MFAChallengeView asks the client for a second factor.
type MFAChallengeView struct {
	MFAToken  string    `json:"mfa_token"`
	Methods   []string  `json:"methods"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AuthResult is the outcome of a sign-in step.
type AuthResult struct {
	Status  string            `json:"status"` // AUTHENTICATED | MFA_REQUIRED
	Account *AccountView      `json:"account,omitempty"`
	Tokens  *TokenPair        `json:"tokens,omitempty"`
	MFA     *MFAChallengeView `json:"mfa,omitempty"`
}

// ChallengeView describes an issued one-time code (never the code itself).
type ChallengeView struct {
	ChallengeID     ids.ID    `json:"challenge_id"`
	Channel         string    `json:"channel"`
	DestinationHint string    `json:"destination_hint,omitempty"`
	ExpiresAt       time.Time `json:"expires_at"`
	DeliveryStatus  string    `json:"delivery_status,omitempty"`
}

// SessionView is one active session.
type SessionView struct {
	SessionID       ids.ID     `json:"session_id"`
	Current         bool       `json:"current"`
	DeviceID        *ids.ID    `json:"device_id"`
	DeviceName      *string    `json:"device_name"`
	DevicePlatform  *string    `json:"device_platform"`
	UserAgent       *string    `json:"user_agent"`
	IPAddress       *string    `json:"ip_address"`
	AuthMethod      string     `json:"auth_method"`
	MFAMethod       *string    `json:"mfa_method"`
	CreatedAt       time.Time  `json:"created_at"`
	LastRefreshedAt time.Time  `json:"last_refreshed_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	StepUpAt        *time.Time `json:"step_up_at"`
}

func sessionView(s db.AuthSession, current ids.ID) SessionView {
	v := SessionView{SessionID: s.ID, Current: s.ID == current, DeviceID: s.DeviceID, DeviceName: s.DeviceName, DevicePlatform: s.DevicePlatform,
		UserAgent: s.UserAgent, AuthMethod: s.AuthMethod, MFAMethod: s.MfaMethod, CreatedAt: s.CreatedAt, LastRefreshedAt: s.LastRefreshedAt,
		ExpiresAt: s.ExpiresAt, StepUpAt: s.StepUpAt}
	if s.IpAddress != nil {
		ip := s.IpAddress.String()
		v.IPAddress = &ip
	}
	return v
}

// DeviceView is a known device.
type DeviceView struct {
	DeviceID    ids.ID     `json:"device_id"`
	Name        *string    `json:"name"`
	Platform    string     `json:"platform"`
	Trusted     bool       `json:"trusted"`
	TrustedAt   *time.Time `json:"trusted_at"`
	FirstSeenAt time.Time  `json:"first_seen_at"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	LastIP      *string    `json:"last_ip"`
}

func deviceView(d db.AuthDevice) DeviceView {
	v := DeviceView{DeviceID: d.DeviceID, Name: d.Name, Platform: d.Platform, Trusted: d.TrustedAt != nil, TrustedAt: d.TrustedAt,
		FirstSeenAt: d.FirstSeenAt, LastSeenAt: d.LastSeenAt}
	if d.LastIp != nil {
		ip := d.LastIp.String()
		v.LastIP = &ip
	}
	return v
}

// SecurityEventView is one owner-visible security event.
type SecurityEventView struct {
	EventID    ids.ID         `json:"event_id"`
	EventType  string         `json:"event_type"`
	SessionID  *ids.ID        `json:"session_id"`
	DeviceID   *ids.ID        `json:"device_id"`
	IPAddress  *string        `json:"ip_address"`
	UserAgent  *string        `json:"user_agent"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	OccurredAt time.Time      `json:"occurred_at"`
}

// MFAStatusView summarises second factors.
type MFAStatusView struct {
	TOTPEnabled            bool     `json:"totp_enabled"`
	RecoveryCodesRemaining int64    `json:"recovery_codes_remaining"`
	FallbackMethods        []string `json:"fallback_methods"`
}

// TOTPSetupView is shown once during enrolment.
type TOTPSetupView struct {
	CredentialID ids.ID `json:"credential_id"`
	Secret       string `json:"secret"`
	OTPAuthURI   string `json:"otpauth_uri"`
}

// RecoveryCodesView is shown exactly once.
type RecoveryCodesView struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

// StepUpView reports step-up freshness.
type StepUpView struct {
	StepUpAt  time.Time `json:"step_up_at"`
	Method    string    `json:"method"`
	ExpiresAt time.Time `json:"expires_at"`
}

func maskEmail(e string) string {
	at := strings.IndexByte(e, '@')
	if at <= 1 {
		return "***" + e[max(at, 0):]
	}
	return e[:1] + "***" + e[at:]
}

func maskPhone(p string) string {
	if len(p) <= 4 {
		return "***"
	}
	return strings.Repeat("*", len(p)-4) + p[len(p)-4:]
}
