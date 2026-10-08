package usecases

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// PushEndpointInput registers a device token.
type PushEndpointInput struct {
	Recipient  Recipient
	Token      string
	Platform   string
	DeviceID   string
	AppVersion string
}

// PushEndpointView never returns the full token.
type PushEndpointView struct {
	EndpointID  ids.ID    `json:"endpoint_id"`
	Recipient   Recipient `json:"recipient"`
	Provider    string    `json:"provider"`
	Platform    string    `json:"platform"`
	TokenSuffix string    `json:"token_suffix"`
	DeviceID    *string   `json:"device_id"`
	AppVersion  *string   `json:"app_version"`
	Status      string    `json:"status"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	CreatedAt   time.Time `json:"created_at"`
}

func endpointView(e db.PushEndpoint) PushEndpointView {
	suffix := e.Token
	if len(suffix) > 6 {
		suffix = suffix[len(suffix)-6:]
	}
	return PushEndpointView{EndpointID: e.EndpointID, Recipient: Recipient{Domain: platform.Domain(e.RecipientDomain), ID: e.RecipientID},
		Provider: e.Provider, Platform: e.Platform, TokenSuffix: suffix, DeviceID: e.DeviceID, AppVersion: e.AppVersion, Status: e.Status,
		LastSeenAt: e.LastSeenAt, CreatedAt: e.CreatedAt}
}

var (
	fcmToken  = regexp.MustCompile(`^[A-Za-z0-9_:\-.]+$`)
	shortText = regexp.MustCompile(`^[A-Za-z0-9 ._:\-+()/]{1,128}$`)
)

// ValidatePushEndpoint checks the registration and recipient authorization.
func ValidatePushEndpoint(p Principal, in PushEndpointInput) error {
	if err := AuthorizeRecipient(p, in.Recipient); err != nil {
		return err
	}
	if len(in.Token) < 16 || len(in.Token) > 4096 || !fcmToken.MatchString(in.Token) {
		return apperrors.Invalid(domain.CodeInvalidToken, "token must be an FCM registration token.")
	}
	switch in.Platform {
	case "ANDROID", "IOS", "WEB":
	default:
		return apperrors.InvalidRequest("platform must be ANDROID, IOS or WEB.")
	}
	if (in.DeviceID != "" && !shortText.MatchString(in.DeviceID)) || (in.AppVersion != "" && !shortText.MatchString(in.AppVersion)) {
		return apperrors.InvalidRequest("device_id and app_version must be at most 128 plain characters.")
	}
	return nil
}

// RegisterPushEndpointTx registers (or refreshes) a device token. A token
// already registered to another recipient is reassigned (the device signed
// in as a different user) and the reassignment is audited.
func (s *Service) RegisterPushEndpointTx(ctx context.Context, tx pgx.Tx, in PushEndpointInput) (PushEndpointView, error) {
	prev, err := s.q.GetPushEndpointByToken(ctx, tx, in.Token)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return PushEndpointView{}, err
	}
	existed := err == nil
	e, err := s.q.UpsertPushEndpoint(ctx, tx, db.UpsertPushEndpointParams{EndpointID: ids.New(), RecipientDomain: string(in.Recipient.Domain),
		RecipientID: in.Recipient.ID, Platform: in.Platform, Token: in.Token, DeviceID: strPtr(in.DeviceID), AppVersion: strPtr(in.AppVersion),
		LastSeenAt: s.now()})
	if err != nil {
		return PushEndpointView{}, err
	}
	md := map[string]any{"platform": e.Platform, "recipient": map[string]any{"recipient_domain": e.RecipientDomain, "recipient_id": e.RecipientID.String()}}
	action := "notification.push_endpoint_registered"
	if existed && (prev.RecipientDomain != e.RecipientDomain || prev.RecipientID != e.RecipientID) {
		action = "notification.push_endpoint_reassigned"
		md["previous_recipient"] = map[string]any{"recipient_domain": prev.RecipientDomain, "recipient_id": prev.RecipientID.String()}
	} else if existed && prev.Status == "ACTIVE" {
		return endpointView(e), nil // refresh only
	}
	if _, err := s.audit.Record(ctx, tx, audit.Event{Action: action, TargetType: "push_endpoint", TargetID: e.EndpointID,
		Changes: map[string]any{"to": e.Status}, Metadata: md}); err != nil {
		return PushEndpointView{}, err
	}
	return endpointView(e), nil
}

// ListPushEndpoints returns a recipient's active endpoints.
func (s *Service) ListPushEndpoints(ctx context.Context, p Principal, r Recipient) ([]PushEndpointView, error) {
	if err := AuthorizeRecipient(p, r); err != nil {
		return nil, err
	}
	rows, err := s.q.ListActivePushEndpoints(ctx, s.db, db.ListActivePushEndpointsParams{RecipientDomain: string(r.Domain), RecipientID: r.ID})
	if err != nil {
		return nil, err
	}
	out := make([]PushEndpointView, 0, len(rows))
	for _, e := range rows {
		out = append(out, endpointView(e))
	}
	return out, nil
}

// RevokePushEndpoint revokes an endpoint the caller may manage. Revoking an
// already revoked endpoint succeeds (idempotent).
func (s *Service) RevokePushEndpoint(ctx context.Context, p Principal, id ids.ID) error {
	return s.withTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		e, err := s.q.GetPushEndpoint(ctx, tx, id)
		if err != nil {
			return notFound(err, apperrors.NotFound(domain.CodeEndpointNotFound, "Push endpoint not found."))
		}
		if AuthorizeRecipient(p, Recipient{Domain: platform.Domain(e.RecipientDomain), ID: e.RecipientID}) != nil {
			return apperrors.NotFound(domain.CodeEndpointNotFound, "Push endpoint not found.")
		}
		n, err := s.q.RevokePushEndpoint(ctx, tx, db.RevokePushEndpointParams{EndpointID: id, RecipientDomain: e.RecipientDomain,
			RecipientID: e.RecipientID, UpdatedAt: s.now()})
		if err != nil || n == 0 {
			return err
		}
		_, err = s.audit.Record(ctx, tx, audit.Event{Action: "notification.push_endpoint_revoked", TargetType: "push_endpoint", TargetID: id,
			Changes: map[string]any{"from": e.Status, "to": "REVOKED"}})
		return err
	})
}

func (s *Service) invalidateEndpoint(ctx context.Context, tx pgx.Tx, id ids.ID, reason string) error {
	if err := s.q.InvalidatePushEndpoint(ctx, tx, db.InvalidatePushEndpointParams{EndpointID: id, InvalidReason: strPtr(reason), UpdatedAt: s.now()}); err != nil {
		return err
	}
	_, err := s.audit.Record(ctx, tx, audit.Event{Action: "notification.push_endpoint_invalidated", ActorType: audit.ActorProvider,
		TargetType: "push_endpoint", TargetID: id, Changes: map[string]any{"to": "INVALID"}, Metadata: map[string]any{"reason": reason}})
	return err
}

// Preference is one category/channel switch.
type Preference struct {
	Category domain.Category `json:"category"`
	Channel  domain.Channel  `json:"channel"`
	Enabled  bool            `json:"enabled"`
}

// QuietHoursView is a daily window ("22:00"-"07:00") in an IANA time zone.
type QuietHoursView struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Timezone string `json:"timezone"`
}

// PreferencesView is the full preference matrix (defaults included).
type PreferencesView struct {
	Recipient   Recipient       `json:"recipient"`
	Preferences []Preference    `json:"preferences"`
	QuietHours  *QuietHoursView `json:"quiet_hours"`
	// SECURITY notifications are always delivered and not configurable.
	AlwaysOn []domain.Category `json:"always_on_categories"`
}

var configurable = []domain.Category{domain.CategoryAccount, domain.CategoryTransactional, domain.CategoryActivity}
var allChannels = []domain.Channel{domain.ChannelPush, domain.ChannelEmail, domain.ChannelSMS}

// GetPreferences returns the recipient's preference matrix.
func (s *Service) GetPreferences(ctx context.Context, p Principal, r Recipient) (PreferencesView, error) {
	if err := AuthorizeRecipient(p, r); err != nil {
		return PreferencesView{}, err
	}
	return s.preferences(ctx, s.db, r)
}

func (s *Service) preferences(ctx context.Context, q db.DBTX, r Recipient) (PreferencesView, error) {
	disabled, err := s.disabled(ctx, q, r)
	if err != nil {
		return PreferencesView{}, err
	}
	v := PreferencesView{Recipient: r, AlwaysOn: []domain.Category{domain.CategorySecurity}}
	for _, c := range configurable {
		for _, ch := range allChannels {
			v.Preferences = append(v.Preferences, Preference{Category: c, Channel: ch, Enabled: !disabled[string(c)+"|"+string(ch)]})
		}
	}
	st, err := s.q.GetSettings(ctx, q, db.GetSettingsParams{RecipientDomain: string(r.Domain), RecipientID: r.ID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return PreferencesView{}, err
	}
	if err == nil && st.QuietStart.Valid && st.Timezone != nil {
		v.QuietHours = &QuietHoursView{Start: clock(st.QuietStart), End: clock(st.QuietEnd), Timezone: *st.Timezone}
	}
	return v, nil
}

func clock(t pgtype.Time) string {
	d := time.Duration(t.Microseconds) * time.Microsecond
	return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Add(d).Format("15:04")
}

func parseClock(s string) (pgtype.Time, bool) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return pgtype.Time{}, false
	}
	return pgtype.Time{Microseconds: int64(t.Hour()*3600+t.Minute()*60) * 1e6, Valid: true}, true
}

// PreferencesUpdate changes switches and (optionally) quiet hours.
type PreferencesUpdate struct {
	Preferences   []Preference
	SetQuietHours bool
	QuietHours    *QuietHoursView // nil with SetQuietHours clears them
}

// UpdatePreferences applies an update (PUT semantics per listed switch;
// naturally idempotent) and audits the change.
func (s *Service) UpdatePreferences(ctx context.Context, p Principal, r Recipient, u PreferencesUpdate) (PreferencesView, error) {
	if err := AuthorizeRecipient(p, r); err != nil {
		return PreferencesView{}, err
	}
	if len(u.Preferences) > 9 {
		return PreferencesView{}, apperrors.InvalidRequest("At most 9 preferences may be set at once.")
	}
	seen := map[string]bool{}
	for _, pr := range u.Preferences {
		k := string(pr.Category) + "|" + string(pr.Channel)
		if !pr.Category.Configurable() || !domain.ValidChannel(pr.Channel) || seen[k] {
			return PreferencesView{}, apperrors.InvalidRequest("Each preference needs a distinct configurable category (ACCOUNT, TRANSACTIONAL, ACTIVITY) and channel (PUSH, EMAIL, SMS); SECURITY is always on.")
		}
		seen[k] = true
	}
	settings := db.UpsertSettingsParams{RecipientDomain: string(r.Domain), RecipientID: r.ID}
	if u.SetQuietHours && u.QuietHours != nil {
		st, ok1 := parseClock(u.QuietHours.Start)
		en, ok2 := parseClock(u.QuietHours.End)
		_, tzErr := time.LoadLocation(u.QuietHours.Timezone)
		if !ok1 || !ok2 || st == en || tzErr != nil || u.QuietHours.Timezone == "" || u.QuietHours.Timezone == "Local" {
			return PreferencesView{}, apperrors.InvalidRequest("quiet_hours needs distinct HH:MM start and end and an IANA timezone.")
		}
		settings.QuietStart, settings.QuietEnd, settings.Timezone = st, en, &u.QuietHours.Timezone
	}
	var v PreferencesView
	err := s.withTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		now := s.now()
		for _, pr := range u.Preferences {
			if err := s.q.UpsertPreference(ctx, tx, db.UpsertPreferenceParams{RecipientDomain: string(r.Domain), RecipientID: r.ID,
				Category: string(pr.Category), Channel: string(pr.Channel), Enabled: pr.Enabled, UpdatedAt: now}); err != nil {
				return err
			}
		}
		if u.SetQuietHours {
			settings.UpdatedAt = now
			if err := s.q.UpsertSettings(ctx, tx, settings); err != nil {
				return err
			}
		}
		var err error
		if v, err = s.preferences(ctx, tx, r); err != nil {
			return err
		}
		_, err = s.audit.Record(ctx, tx, audit.Event{Action: "notification.preferences_updated", TargetType: "recipient", TargetID: r.ID,
			Changes:  map[string]any{"preferences": u.Preferences, "quiet_hours_set": u.SetQuietHours, "quiet_hours": v.QuietHours},
			Metadata: map[string]any{"recipient_domain": string(r.Domain)}})
		return err
	})
	return v, err
}
