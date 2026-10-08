package usecases

import (
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/templates"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Suppression reasons (a channel that will not be attempted).
const (
	SuppressPreference    = "PREFERENCE_DISABLED"
	SuppressNoDestination = "NO_DESTINATION"
	SuppressNoEndpoint    = "NO_ACTIVE_PUSH_ENDPOINT"
	SuppressNotConfigured = "CHANNEL_NOT_CONFIGURED"
)

// SendInput is a request to notify one recipient.
type SendInput struct {
	Recipient       Recipient
	TemplateKey     string
	TemplateVersion int32 // 0 = latest active
	Locale          string
	Channels        []domain.Channel // empty = all template channels
	Variables       map[string]string
	Email           string
	Phone           string
	Data            map[string]string // opaque PUSH data (deep-link ids), not rendered
	CallerReference string
}

// Suppression explains a channel that was not queued.
type Suppression struct {
	Channel domain.Channel `json:"channel"`
	Reason  string         `json:"reason"`
}

// DeliveryView is the API view of one delivery (no message content).
type DeliveryView struct {
	DeliveryID     ids.ID     `json:"delivery_id"`
	Channel        string     `json:"channel"`
	Provider       string     `json:"provider"`
	PushEndpointID *ids.ID    `json:"push_endpoint_id"`
	Status         string     `json:"status"`
	Attempts       int32      `json:"attempts"`
	MaxAttempts    int32      `json:"max_attempts"`
	NextAttemptAt  *time.Time `json:"next_attempt_at"`
	LastErrorCode  *string    `json:"last_error_code"`
	ReceiptStatus  string     `json:"receipt_status"`
	SentAt         *time.Time `json:"sent_at"`
	FailedAt       *time.Time `json:"failed_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
}

// NotificationView is the API view of a notification request.
type NotificationView struct {
	NotificationID  ids.ID         `json:"notification_id"`
	Recipient       Recipient      `json:"recipient"`
	TemplateKey     string         `json:"template_key"`
	TemplateVersion int32          `json:"template_version"`
	Locale          string         `json:"locale"`
	Category        string         `json:"category"`
	Critical        bool           `json:"critical"`
	RequestedBy     string         `json:"requested_by_service"`
	CallerReference *string        `json:"caller_reference"`
	Deliveries      []DeliveryView `json:"deliveries"`
	Suppressed      []Suppression  `json:"suppressed"`
	ExpiresAt       time.Time      `json:"expires_at"`
	CreatedAt       time.Time      `json:"created_at"`
}

func deliveryView(d db.NotificationDelivery) DeliveryView {
	v := DeliveryView{DeliveryID: d.DeliveryID, Channel: d.Channel, Provider: d.Provider, PushEndpointID: d.PushEndpointID, Status: d.Status,
		Attempts: d.Attempts, MaxAttempts: d.MaxAttempts, LastErrorCode: d.LastErrorCode, ReceiptStatus: d.ReceiptStatus,
		SentAt: d.SentAt, FailedAt: d.FailedAt, ExpiresAt: d.ExpiresAt}
	if d.Status == string(domain.StatusQueued) || d.Status == string(domain.StatusRetryScheduled) {
		t := d.NextAttemptAt
		v.NextAttemptAt = &t
	}
	return v
}

func notificationView(r db.NotificationRequest, ds []db.NotificationDelivery) NotificationView {
	v := NotificationView{NotificationID: r.NotificationID, Recipient: Recipient{Domain: platform.Domain(r.RecipientDomain), ID: r.RecipientID},
		TemplateKey: r.TemplateKey, TemplateVersion: r.TemplateVersion, Locale: r.Locale, Category: r.Category, Critical: r.Critical,
		RequestedBy: r.RequestedByService, CallerReference: r.CallerReference, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt,
		Deliveries: make([]DeliveryView, 0, len(ds)), Suppressed: []Suppression{}}
	for _, d := range ds {
		v.Deliveries = append(v.Deliveries, deliveryView(d))
	}
	_ = json.Unmarshal(r.Suppressed, &v.Suppressed)
	return v
}

var (
	e164       = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)
	templateRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	localeRe   = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)
	callerRef  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	dataKey    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
)

// ValidateSend checks the request shape and recipient authorization.
func (s *Service) ValidateSend(p Principal, in *SendInput) error {
	if err := AuthorizeRecipient(p, in.Recipient); err != nil {
		return err
	}
	if !templateRe.MatchString(in.TemplateKey) {
		return apperrors.InvalidRequest("template_key is required (e.g. account.security_alert).")
	}
	if in.Locale == "" {
		in.Locale = s.opts.DefaultLocale
	}
	if !localeRe.MatchString(in.Locale) || in.TemplateVersion < 0 {
		return apperrors.InvalidRequest("locale or template_version is invalid.")
	}
	seen := map[domain.Channel]bool{}
	for _, c := range in.Channels {
		if !domain.ValidChannel(c) || seen[c] {
			return apperrors.InvalidRequest("channels must be distinct values of PUSH, EMAIL, SMS.")
		}
		seen[c] = true
	}
	if in.Email != "" {
		a, err := mail.ParseAddress(in.Email)
		if err != nil || a.Address != in.Email || len(in.Email) > 254 {
			return apperrors.InvalidRequest("destinations.email must be a plain email address.")
		}
	}
	if in.Phone != "" && !e164.MatchString(in.Phone) {
		return apperrors.InvalidRequest("destinations.phone must be an E.164 number.")
	}
	if in.CallerReference != "" && !callerRef.MatchString(in.CallerReference) {
		return apperrors.InvalidRequest("caller_reference must be 1-128 characters of [A-Za-z0-9._:-].")
	}
	if len(in.Data) > 10 {
		return apperrors.InvalidRequest("data may contain at most 10 entries.")
	}
	for k, v := range in.Data {
		if !dataKey.MatchString(k) || len(v) > 256 {
			return apperrors.InvalidRequest("data keys must be lowercase identifiers and values at most 256 characters.")
		}
	}
	if len(in.Variables) > 30 {
		return apperrors.InvalidRequest("variables may contain at most 30 entries.")
	}
	return nil
}

func (s *Service) template(ctx context.Context, q db.DBTX, key, locale string, version int32) (db.NotificationTemplate, error) {
	var t db.NotificationTemplate
	var err error
	if version > 0 {
		t, err = s.q.GetTemplateVersion(ctx, q, db.GetTemplateVersionParams{TemplateKey: key, Locale: locale, Version: version})
	} else {
		t, err = s.q.GetActiveTemplate(ctx, q, db.GetActiveTemplateParams{TemplateKey: key, Locale: locale})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return t, apperrors.NotFound(domain.CodeTemplateNotFound, "No template exists for this key, locale and version.")
	}
	return t, err
}

// preferences returns enabled[category|channel] (missing = enabled).
func (s *Service) disabled(ctx context.Context, q db.DBTX, r Recipient) (map[string]bool, error) {
	rows, err := s.q.ListPreferences(ctx, q, db.ListPreferencesParams{RecipientDomain: string(r.Domain), RecipientID: r.ID})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, p := range rows {
		if !p.Enabled {
			out[p.Category+"|"+p.Channel] = true
		}
	}
	return out, nil
}

func (s *Service) quietHours(ctx context.Context, q db.DBTX, r Recipient) (domain.QuietHours, error) {
	st, err := s.q.GetSettings(ctx, q, db.GetSettingsParams{RecipientDomain: string(r.Domain), RecipientID: r.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.QuietHours{}, nil
	}
	if err != nil || !st.QuietStart.Valid || st.Timezone == nil {
		return domain.QuietHours{}, err
	}
	loc, err := time.LoadLocation(*st.Timezone)
	if err != nil {
		return domain.QuietHours{}, nil // validated on write; ignore if tzdata changed
	}
	return domain.QuietHours{Start: time.Duration(st.QuietStart.Microseconds) * time.Microsecond,
		End: time.Duration(st.QuietEnd.Microseconds) * time.Microsecond, Location: loc}, nil
}

type rendered struct {
	subject, body, html string
}

func render(t db.NotificationTemplate, ch domain.Channel, vars map[string]string) rendered {
	switch ch {
	case domain.ChannelPush:
		return rendered{subject: templates.Render(deref(t.PushTitle), vars, false), body: templates.Render(deref(t.PushBody), vars, false)}
	case domain.ChannelEmail:
		r := rendered{subject: templates.Render(deref(t.EmailSubject), vars, false), body: templates.Render(deref(t.EmailText), vars, false)}
		if t.EmailHtml != nil {
			r.html = templates.Render(*t.EmailHtml, vars, true)
		}
		return r
	default:
		return rendered{body: templates.Render(deref(t.SmsText), vars, false)}
	}
}

// SendTx renders the template and queues one delivery per channel
// destination (per active push endpoint for PUSH). Preferences and quiet
// hours apply to non-critical categories; SECURITY is always sent.
func (s *Service) SendTx(ctx context.Context, tx pgx.Tx, p Principal, in SendInput) (NotificationView, error) {
	t, err := s.template(ctx, tx, in.TemplateKey, in.Locale, in.TemplateVersion)
	if err != nil {
		return NotificationView{}, err
	}
	if err := templates.CheckVariables(t.RequiredVariables, in.Variables); err != nil {
		return NotificationView{}, apperrors.Unprocessable(domain.CodeVariables, "Template variables are invalid: "+err.Error()+".")
	}
	chans := in.Channels
	if len(chans) == 0 {
		for _, c := range t.Channels {
			chans = append(chans, domain.Channel(c))
		}
	}
	for _, c := range chans {
		if !slices.Contains(t.Channels, string(c)) {
			return NotificationView{}, apperrors.Unprocessable(domain.CodeChannelNotAllowed, "The template does not support channel "+string(c)+".")
		}
	}
	cat := domain.Category(t.Category)
	critical := cat.Critical()
	disabled, err := s.disabled(ctx, tx, in.Recipient)
	if err != nil {
		return NotificationView{}, err
	}
	quiet, err := s.quietHours(ctx, tx, in.Recipient)
	if err != nil {
		return NotificationView{}, err
	}
	now := s.now()
	expires := now.Add(time.Duration(t.TtlSeconds) * time.Second)
	nid := ids.New()

	type plan struct {
		ch       domain.Channel
		endpoint *ids.ID
		dest     string
	}
	var plans []plan
	suppressed := []Suppression{}
	for _, ch := range chans {
		if !critical && disabled[string(cat)+"|"+string(ch)] {
			suppressed = append(suppressed, Suppression{ch, SuppressPreference})
			continue
		}
		if _, ok := s.senders[ch]; !ok {
			suppressed = append(suppressed, Suppression{ch, SuppressNotConfigured})
			continue
		}
		switch ch {
		case domain.ChannelEmail:
			if in.Email == "" {
				suppressed = append(suppressed, Suppression{ch, SuppressNoDestination})
				continue
			}
			plans = append(plans, plan{ch: ch, dest: in.Email})
		case domain.ChannelSMS:
			if in.Phone == "" {
				suppressed = append(suppressed, Suppression{ch, SuppressNoDestination})
				continue
			}
			plans = append(plans, plan{ch: ch, dest: in.Phone})
		case domain.ChannelPush:
			eps, err := s.q.ListActivePushEndpoints(ctx, tx, db.ListActivePushEndpointsParams{RecipientDomain: string(in.Recipient.Domain), RecipientID: in.Recipient.ID})
			if err != nil {
				return NotificationView{}, err
			}
			if len(eps) == 0 {
				suppressed = append(suppressed, Suppression{ch, SuppressNoEndpoint})
				continue
			}
			for _, e := range eps {
				id := e.EndpointID
				// The token is resolved at dispatch time so a revoked or
				// invalidated endpoint is never sent to.
				plans = append(plans, plan{ch: ch, endpoint: &id, dest: "push_endpoint:" + id.String()})
			}
		}
	}
	sup, _ := json.Marshal(suppressed)
	req, err := s.q.InsertRequest(ctx, tx, db.InsertRequestParams{NotificationID: nid, RecipientDomain: string(in.Recipient.Domain),
		RecipientID: in.Recipient.ID, TemplateID: t.TemplateID, TemplateKey: t.TemplateKey, TemplateVersion: t.Version, Locale: t.Locale,
		Category: t.Category, Critical: critical, RequestedByService: string(p.Service), RequestedByActorDomain: strPtr(p.ActorDomain),
		RequestedByActorID: actorPtr(p), CallerReference: strPtr(in.CallerReference), Suppressed: sup, ExpiresAt: expires, CreatedAt: now})
	if err != nil {
		return NotificationView{}, err
	}
	var data []byte
	if len(in.Data) > 0 {
		data, _ = json.Marshal(in.Data)
	}
	deliveries := make([]db.NotificationDelivery, 0, len(plans))
	for _, pl := range plans {
		r := render(t, pl.ch, in.Variables)
		due := now
		if !critical && (pl.ch == domain.ChannelPush || pl.ch == domain.ChannelSMS) {
			due = quiet.DeferUntil(now)
		}
		params := db.InsertDeliveryParams{DeliveryID: ids.New(), NotificationID: nid, Channel: string(pl.ch), Provider: s.senders[pl.ch].Name(),
			PushEndpointID: pl.endpoint, Destination: pl.dest, MaxAttempts: int32(domain.MaxAttempts[pl.ch]), NextAttemptAt: due,
			Subject: strPtr(r.subject), Body: r.body, HtmlBody: strPtr(r.html), ExpiresAt: expires, CreatedAt: now}
		if pl.ch == domain.ChannelPush {
			params.Data = data
		}
		d, err := s.q.InsertDelivery(ctx, tx, params)
		if err != nil {
			return NotificationView{}, err
		}
		if err := s.emit(ctx, tx, req, d, EventQueued); err != nil {
			return NotificationView{}, err
		}
		deliveries = append(deliveries, d)
	}
	if _, err := s.audit.Record(ctx, tx, audit.Event{Action: "notification.requested", TargetType: "notification", TargetID: nid,
		Metadata: map[string]any{"template_key": t.TemplateKey, "template_version": t.Version, "category": t.Category,
			"recipient":  map[string]any{"recipient_domain": string(in.Recipient.Domain), "recipient_id": in.Recipient.ID.String()},
			"deliveries": len(deliveries), "suppressed": suppressed}}); err != nil {
		return NotificationView{}, err
	}
	return notificationView(req, deliveries), nil
}

func actorPtr(p Principal) *ids.ID {
	if !p.HasActor() {
		return nil
	}
	id := p.ActorID
	return &id
}

type eventPayload struct {
	NotificationID ids.ID     `json:"notification_id"`
	DeliveryID     ids.ID     `json:"delivery_id"`
	Recipient      Recipient  `json:"recipient"`
	TemplateKey    string     `json:"template_key"`
	Category       string     `json:"category"`
	Channel        string     `json:"channel"`
	Status         string     `json:"status"`
	Attempts       int32      `json:"attempts"`
	RequestedBy    string     `json:"requested_by_service"`
	CallerRef      *string    `json:"caller_reference"`
	ErrorCode      *string    `json:"error_code,omitempty"`
	SentAt         *time.Time `json:"sent_at,omitempty"`
	FailedAt       *time.Time `json:"failed_at,omitempty"`
}

// emit queues a delivery event (no content or destination: PII stays here).
func (s *Service) emit(ctx context.Context, tx pgx.Tx, r db.NotificationRequest, d db.NotificationDelivery, eventType string) error {
	p := eventPayload{NotificationID: r.NotificationID, DeliveryID: d.DeliveryID,
		Recipient: Recipient{Domain: platform.Domain(r.RecipientDomain), ID: r.RecipientID}, TemplateKey: r.TemplateKey, Category: r.Category,
		Channel: d.Channel, Status: d.Status, Attempts: d.Attempts, RequestedBy: r.RequestedByService, CallerRef: r.CallerReference,
		ErrorCode: d.LastErrorCode, SentAt: d.SentAt, FailedAt: d.FailedAt}
	e, err := events.New(ctx, platform.NotificationService, eventType, "notification", r.NotificationID, p)
	if err != nil {
		return err
	}
	return s.outbox.Add(ctx, tx, e)
}

// Get returns a notification to the service that requested it (or admin).
func (s *Service) Get(ctx context.Context, p Principal, id ids.ID) (NotificationView, error) {
	r, err := s.q.GetRequest(ctx, s.db, id)
	if err != nil {
		return NotificationView{}, notFound(err, domain.ErrNotFound())
	}
	if r.RequestedByService != string(p.Service) && p.Service != platform.AdminService {
		return NotificationView{}, domain.ErrNotFound()
	}
	ds, err := s.q.ListDeliveries(ctx, s.db, id)
	if err != nil {
		return NotificationView{}, err
	}
	return notificationView(r, ds), nil
}
