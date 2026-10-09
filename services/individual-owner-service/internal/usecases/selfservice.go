package usecases

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

// MaxListedSessions bounds the active-session list (active sessions per
// account are naturally bounded by sign-ins within the session lifetime).
const MaxListedSessions = 100

// ListSessions returns the account's active sessions.
func (s *Service) ListSessions(ctx context.Context, subj authz.Subject) ([]SessionView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActSessionsRead, authz.Resource{}); err != nil {
		return nil, err
	}
	rows, err := s.q.ListActiveSessions(ctx, s.Pool, db.ListActiveSessionsParams{AccountID: subj.Account.AccountID, ExpiresAt: s.now(), Limit: MaxListedSessions})
	if err != nil {
		return nil, err
	}
	out := make([]SessionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, sessionView(r, subj.Account.SessionID))
	}
	return out, nil
}

// RevokeSession ends one of the account's sessions (remote sign-out).
func (s *Service) RevokeSession(ctx context.Context, subj authz.Subject, sessionID ids.ID, ci ClientInfo) error {
	if _, err := s.Authz.Require(ctx, subj, authz.ActSessionsRevoke, authz.Resource{}); err != nil {
		return err
	}
	return s.revokeOwnSession(ctx, subj, sessionID, "REMOTE_REVOKED", ci)
}

// ListDevices returns known, unrevoked devices.
func (s *Service) ListDevices(ctx context.Context, subj authz.Subject) ([]DeviceView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActDevicesRead, authz.Resource{}); err != nil {
		return nil, err
	}
	rows, err := s.q.ListDevices(ctx, s.Pool, db.ListDevicesParams{AccountID: subj.Account.AccountID, Limit: 100})
	if err != nil {
		return nil, err
	}
	out := make([]DeviceView, 0, len(rows))
	for _, r := range rows {
		out = append(out, deviceView(r))
	}
	return out, nil
}

// TrustDevice marks a device trusted (step-up required). Trust is recorded
// for the owner's visibility and suspicious-sign-in evaluation only; it never
// bypasses MFA.
func (s *Service) TrustDevice(ctx context.Context, subj authz.Subject, deviceID ids.ID, ci ClientInfo) (DeviceView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActDevicesTrust, authz.Resource{}); err != nil {
		return DeviceView{}, err
	}
	var out db.AuthDevice
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.q.TrustDevice(ctx, tx, db.TrustDeviceParams{DeviceID: deviceID, AccountID: subj.Account.AccountID, TrustedAt: ptrTime(s.now())})
		if isNoRows(err) {
			return apperrors.NotFound("DEVICE_NOT_FOUND", "Device not found.")
		}
		if err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, subj.Account.AccountID, "DEVICE_TRUSTED", subj.Account.SessionID, deviceID, ci, nil); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, "owner.account.device_trusted", subj.Account.AccountID, audit.ResultSuccess, nil, map[string]any{"device_id": deviceID.String()})
	})
	return deviceView(out), err
}

// RevokeDevice forgets a device and ends its sessions.
func (s *Service) RevokeDevice(ctx context.Context, subj authz.Subject, deviceID ids.ID, ci ClientInfo) error {
	if _, err := s.Authz.Require(ctx, subj, authz.ActDevicesRevoke, authz.Resource{}); err != nil {
		return err
	}
	return s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		now := s.now()
		_, err := s.q.RevokeDevice(ctx, tx, db.RevokeDeviceParams{DeviceID: deviceID, AccountID: subj.Account.AccountID, RevokedAt: &now})
		if isNoRows(err) {
			return apperrors.NotFound("DEVICE_NOT_FOUND", "Device not found.")
		}
		if err != nil {
			return err
		}
		reason := "DEVICE_REVOKED"
		n, err := s.q.RevokeDeviceSessions(ctx, tx, db.RevokeDeviceSessionsParams{AccountID: subj.Account.AccountID, DeviceID: &deviceID, RevokedAt: &now, RevokeReason: &reason})
		if err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, subj.Account.AccountID, "DEVICE_REVOKED", subj.Account.SessionID, deviceID, ci, map[string]any{"sessions_revoked": n}); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, "owner.account.device_revoked", subj.Account.AccountID, audit.ResultSuccess, map[string]any{"sessions_revoked": n},
			map[string]any{"device_id": deviceID.String()})
	})
}

type eventCursor struct {
	At time.Time `json:"t"`
	ID ids.ID    `json:"i"`
}

// ListSecurityEvents pages through the owner-visible security history.
func (s *Service) ListSecurityEvents(ctx context.Context, subj authz.Subject, cursor string, limit int) (pagination.Response[SecurityEventView], error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActSecurityEventsRead, authz.Resource{}); err != nil {
		return pagination.Response[SecurityEventView]{}, err
	}
	p := db.ListSecurityEventsParams{AccountID: subj.Account.AccountID, RowLimit: int32(limit + 1)}
	var c eventCursor
	ok, err := pagination.Decode(cursor, &c)
	if err != nil {
		return pagination.Response[SecurityEventView]{}, err
	}
	if ok {
		p.CursorOccurredAt, p.CursorID = &c.At, &c.ID
	}
	rows, err := s.q.ListSecurityEvents(ctx, s.Pool, p)
	if err != nil {
		return pagination.Response[SecurityEventView]{}, err
	}
	views := make([]SecurityEventView, 0, len(rows))
	for _, r := range rows {
		v := SecurityEventView{EventID: r.EventID, EventType: r.EventType, SessionID: r.SessionID, DeviceID: r.DeviceID, UserAgent: r.UserAgent, OccurredAt: r.OccurredAt}
		if r.IpAddress != nil {
			ip := r.IpAddress.String()
			v.IPAddress = &ip
		}
		if len(r.Metadata) > 0 {
			_ = json.Unmarshal(r.Metadata, &v.Metadata)
		}
		views = append(views, v)
	}
	return pagination.Build(views, limit, func(v SecurityEventView) any { return eventCursor{At: v.OccurredAt, ID: v.EventID} })
}

// OAuthProviders lists configured sign-in providers (none until an adapter
// is certified).
func (s *Service) OAuthProviders() []string { return s.OAuth.Names() }
