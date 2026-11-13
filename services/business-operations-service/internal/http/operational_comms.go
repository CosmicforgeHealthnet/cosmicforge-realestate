package businesshttp

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/cfmeet"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/messaging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func init() {
	registerOpsTable("messaging_connections", "communication_contexts", "inbox_threads", "quick_reply_templates", "scheduled_replies", "support_cases", "ai_knowledge_entries", "ai_suggestions", "action_cards", "automation_rules", "inbox_sla_policies")
}

// MessagingConnection is the provider connection projection. Credential values
// are never returned; only the secret-manager reference name.
type MessagingConnection struct {
	ID                       ids.ID     `json:"id"`
	OrganisationID           ids.ID     `json:"organisation_id"`
	PropertyID               ids.ID     `json:"property_id"`
	Provider                 string     `json:"provider"`
	Authority                string     `json:"authority"`
	State                    string     `json:"state"`
	SupportsReply            bool       `json:"supports_reply"`
	SupportsReadState        bool       `json:"supports_read_state"`
	SupportsPreview          bool       `json:"supports_preview"`
	SupportsAttachments      bool       `json:"supports_attachments"`
	SupportsReviews          bool       `json:"supports_reviews"`
	SupportsReviewReplies    bool       `json:"supports_review_replies"`
	RetentionDays            int        `json:"retention_days"`
	CredentialReference      string     `json:"credential_reference"`
	ProviderAccountReference string     `json:"provider_account_reference"`
	HealthReason             string     `json:"health_reason"`
	ConsecutiveFailures      int        `json:"consecutive_failures"`
	LastSuccessAt            *time.Time `json:"last_success_at"`
	Version                  int64      `json:"version"`
}

type MessagingConnectionCreate struct {
	PropertyID               ids.ID `json:"property_id"`
	Provider                 string `json:"provider"`
	ProviderAccountReference string `json:"provider_account_reference"`
	CredentialReference      string `json:"credential_reference,omitempty"`
	RetentionDays            int    `json:"retention_days"`
	SupportsReply            bool   `json:"supports_reply"`
	SupportsReadState        bool   `json:"supports_read_state"`
	SupportsPreview          bool   `json:"supports_preview"`
	SupportsAttachments      bool   `json:"supports_attachments"`
	SupportsReviews          bool   `json:"supports_reviews"`
	SupportsReviewReplies    bool   `json:"supports_review_replies"`
	Reason                   string `json:"reason"`
}

// OpsStateCommand moves a governed record to a new state at its current version.
type OpsStateCommand struct {
	Version int64  `json:"version"`
	State   string `json:"state"`
	Reason  string `json:"reason"`
}

// OpsVersionCommand carries a current version and reason.
type OpsVersionCommand struct {
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
}

type CommunicationContext struct {
	ID                      ids.ID  `json:"id"`
	PropertyID              ids.ID  `json:"property_id"`
	ConnectionID            ids.ID  `json:"connection_id"`
	Authority               string  `json:"authority"`
	State                   string  `json:"state"`
	ExternalConversationRef *string `json:"external_conversation_ref"`
	DeepLinkRef             *string `json:"deep_link_ref"`
	GuestID                 *ids.ID `json:"guest_id"`
	ReservationID           *ids.ID `json:"reservation_id"`
	SupportCaseID           *ids.ID `json:"support_case_id"`
	Subject                 string  `json:"subject"`
	BlockedReason           string  `json:"blocked_reason"`
	Attempts                int     `json:"attempts"`
	Version                 int64   `json:"version"`
	CreatedBy               *ids.ID `json:"created_by"`
}

type CommunicationContextCreate struct {
	PropertyID              ids.ID  `json:"property_id"`
	ConnectionID            ids.ID  `json:"connection_id"`
	Subject                 string  `json:"subject"`
	ExternalConversationRef string  `json:"external_conversation_ref,omitempty"`
	GuestID                 *ids.ID `json:"guest_id,omitempty"`
	ReservationID           *ids.ID `json:"reservation_id,omitempty"`
	SupportCaseID           *ids.ID `json:"support_case_id,omitempty"`
	Priority                string  `json:"priority,omitempty"`
	Reason                  string  `json:"reason"`
}

type InboxThread struct {
	ID                 ids.ID     `json:"id"`
	PropertyID         ids.ID     `json:"property_id"`
	ContextID          ids.ID     `json:"context_id"`
	Priority           string     `json:"priority"`
	State              string     `json:"state"`
	AssignedTo         *ids.ID    `json:"assigned_to"`
	ReadState          string     `json:"read_state"`
	LastInboundAt      *time.Time `json:"last_inbound_at"`
	LastOutboundAt     *time.Time `json:"last_outbound_at"`
	FirstResponseDueAt *time.Time `json:"first_response_due_at"`
	FirstRespondedAt   *time.Time `json:"first_responded_at"`
	ResolutionDueAt    *time.Time `json:"resolution_due_at"`
	ResolvedAt         *time.Time `json:"resolved_at"`
	SLABreached        bool       `json:"sla_breached"`
	InboundCount       int        `json:"inbound_count"`
	Version            int64      `json:"version"`
}

type InboxThreadCommand struct {
	Version   int64  `json:"version"`
	State     string `json:"state,omitempty"`
	Priority  string `json:"priority,omitempty"`
	ReadState string `json:"read_state,omitempty"`
	Reason    string `json:"reason"`
}

type InboxAssignmentCommand struct {
	ThreadID   ids.ID  `json:"thread_id"`
	Version    int64   `json:"version"`
	AssigneeID *ids.ID `json:"assignee_id,omitempty"`
	Reason     string  `json:"reason"`
}

type InboxSLACommand struct {
	PropertyID           ids.ID `json:"property_id"`
	Priority             string `json:"priority"`
	FirstResponseSeconds int    `json:"first_response_seconds"`
	ResolutionSeconds    int    `json:"resolution_seconds"`
	ExpectedVersion      int64  `json:"expected_version"`
	Reason               string `json:"reason"`
}

type QuickReplyCreate struct {
	PropertyID ids.ID   `json:"property_id"`
	Name       string   `json:"name"`
	Body       string   `json:"body"`
	Variables  []string `json:"variables"`
	Reason     string   `json:"reason"`
}

type ScheduledReply struct {
	ID                ids.ID     `json:"id"`
	PropertyID        ids.ID     `json:"property_id"`
	ThreadID          ids.ID     `json:"thread_id"`
	TemplateID        *ids.ID    `json:"template_id"`
	SuggestionID      *ids.ID    `json:"suggestion_id"`
	AutomationRuleID  *ids.ID    `json:"automation_rule_id"`
	Body              string     `json:"body"`
	AIAttributed      bool       `json:"ai_attributed"`
	SendAt            time.Time  `json:"send_at"`
	State             string     `json:"state"`
	Attempts          int        `json:"attempts"`
	MaxAttempts       int        `json:"max_attempts"`
	LeaseGeneration   int64      `json:"lease_generation"`
	ProviderMessageID *string    `json:"provider_message_id"`
	OutcomeReason     string     `json:"outcome_reason"`
	SentAt            *time.Time `json:"sent_at"`
	CreatedBy         *ids.ID    `json:"created_by"`
	CreatedByMember   *ids.ID    `json:"created_by_member"`
	CreatedByRole     *ids.ID    `json:"created_by_role"`
	Version           int64      `json:"version"`
}

type ScheduledReplyCreate struct {
	ThreadID   ids.ID            `json:"thread_id"`
	Body       string            `json:"body,omitempty"`
	TemplateID *ids.ID           `json:"template_id,omitempty"`
	Variables  map[string]string `json:"variables,omitempty"`
	SendAt     *time.Time        `json:"send_at,omitempty"`
	Draft      bool              `json:"draft"`
	Reason     string            `json:"reason"`
}

type ScheduledReplySchedule struct {
	Version int64     `json:"version"`
	SendAt  time.Time `json:"send_at"`
	Reason  string    `json:"reason"`
}

type SupportCase struct {
	ID            ids.ID     `json:"id"`
	PropertyID    ids.ID     `json:"property_id"`
	GuestID       *ids.ID    `json:"guest_id"`
	ReservationID *ids.ID    `json:"reservation_id"`
	Category      string     `json:"category"`
	Severity      string     `json:"severity"`
	State         string     `json:"state"`
	Summary       string     `json:"summary"`
	Resolution    string     `json:"resolution"`
	AssignedTo    *ids.ID    `json:"assigned_to"`
	SLADueAt      time.Time  `json:"sla_due_at"`
	ResolvedAt    *time.Time `json:"resolved_at"`
	Version       int64      `json:"version"`
}

type SupportCaseCreate struct {
	PropertyID    ids.ID  `json:"property_id"`
	GuestID       *ids.ID `json:"guest_id,omitempty"`
	ReservationID *ids.ID `json:"reservation_id,omitempty"`
	ContextID     *ids.ID `json:"context_id,omitempty"`
	Category      string  `json:"category"`
	Severity      string  `json:"severity"`
	Summary       string  `json:"summary"`
	Reason        string  `json:"reason"`
}

type SupportCaseTransition struct {
	Version    int64   `json:"version"`
	Action     string  `json:"action"`
	AssigneeID *ids.ID `json:"assignee_id,omitempty"`
	Resolution string  `json:"resolution,omitempty"`
	Reason     string  `json:"reason"`
}

var commsProviders = []string{"CF_MEET", "MARKETPLACE", "BOOKING_COM", "AIRBNB", "DIRECT_WEB"}
var inboxPriorities = []string{"LOW", "NORMAL", "HIGH", "URGENT"}
var credentialRef = regexp.MustCompile(`^[a-z0-9/_.-]{0,200}$`)

func (h *Handler) registerCommunications(r chi.Router) {
	r.Get("/messaging-connections", h.commsConnections)
	r.Post("/messaging-connections", h.commsConnectionCreate)
	r.Post("/messaging-connections/{id}/state", h.commsConnectionState)
	r.Get("/communication-contexts", h.commsContexts)
	r.Post("/communication-contexts", h.commsContextCreate)
	r.Get("/communication-contexts/{id}", h.commsContextGet)
	r.Post("/communication-contexts/{id}/revoke", h.commsContextRevoke)
	r.Get("/inbox", h.inboxList)
	r.Get("/inbox/threads", h.inboxList)
	r.Get("/inbox/threads/{id}", h.inboxThreadGet)
	r.Post("/inbox/threads/{id}/state", h.inboxThreadState)
	r.Get("/inbox/assignments", h.inboxAssignments)
	r.Post("/inbox/assignments", h.inboxAssign)
	r.Get("/inbox/sla", h.inboxSLAList)
	r.Post("/inbox/sla", h.inboxSLASave)
	r.Get("/inbox/quick-replies", h.inboxQuickReplies)
	r.Post("/inbox/quick-replies", h.inboxQuickReplyCreate)
	r.Post("/inbox/quick-replies/{id}/retire", h.inboxQuickReplyRetire)
	r.Get("/inbox/scheduled-replies", h.inboxScheduledReplies)
	r.Post("/inbox/scheduled-replies", h.inboxScheduledReplyCreate)
	r.Post("/inbox/scheduled-replies/{id}/schedule", h.inboxScheduledReplySchedule)
	r.Post("/inbox/scheduled-replies/{id}/cancel", h.inboxScheduledReplyCancel)
	r.Get("/support-cases", h.supportCases)
	r.Post("/support-cases", h.supportCaseCreate)
	r.Get("/support-cases/{id}", h.supportCaseGet)
	r.Post("/support-cases/{id}/transitions", h.supportCaseTransition)
	h.registerCommsAI(r)
}

// messagingAdapter returns the registered adapter; absent providers are
// disabled and fail closed.
func (h *Handler) messagingAdapter(provider string) messaging.Adapter {
	if a, ok := h.Messaging[provider]; ok && a != nil {
		return a
	}
	return messaging.Disabled{Name: provider}
}

func (h *Handler) cfMeet() cfmeet.API {
	if h.CFMeet == nil {
		return cfmeet.Unavailable{}
	}
	return h.CFMeet
}

func (h *Handler) commsConnections(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "inbox:read", `SELECT to_jsonb(x) FROM messaging_connections x WHERE organisation_id=$1 AND property_id=$2 ORDER BY created_at,id LIMIT $3 OFFSET $4`)
}

func (h *Handler) commsConnectionCreate(w http.ResponseWriter, r *http.Request) {
	var in MessagingConnectionCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || !slices.Contains(commsProviders, in.Provider) || !opsText(in.ProviderAccountReference, 1, 200) || !credentialRef.MatchString(in.CredentialReference) || in.RetentionDays < 0 || in.RetentionDays > 3650 || !validStaffReason(in.Reason) || in.Provider == "CF_MEET" && (in.SupportsPreview || in.SupportsReviews) || in.SupportsReviewReplies && !in.SupportsReviews {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, supported provider, account reference, secret reference name, retention and reason required. CF Meet message content is never previewed."))
		return
	}
	h.execute(w, r, "comms:connection:create:"+in.PropertyID.String(), in, "inbox:configure", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "inbox:configure", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		authority := "EXTERNAL_CHANNEL"
		if in.Provider == "CF_MEET" {
			authority = "CF_MEET"
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO messaging_connections(id,organisation_id,property_id,provider,authority,supports_reply,supports_read_state,supports_preview,supports_attachments,retention_days,credential_reference,provider_account_reference,created_by,supports_reviews,supports_review_replies) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, id, t.Organisation, in.PropertyID, in.Provider, authority, in.SupportsReply, in.SupportsReadState, in.SupportsPreview, in.SupportsAttachments, in.RetentionDays, in.CredentialReference, in.ProviderAccountReference, t.Account, in.SupportsReviews, in.SupportsReviewReplies); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO inbox_sync_cursors(organisation_id,connection_id) VALUES($1,$2)`, t.Organisation, id); e != nil {
			return 0, nil, e
		}
		c, e := opsGet[MessagingConnection](ctx, tx, "messaging_connections", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, c, h.opsCommit(ctx, tx, t, in.PropertyID, id, "messaging_connection", "CREATED", "", "DISCONNECTED", businessEvents.MessagingConnectionUpdated, 1, map[string]any{"provider": in.Provider, "reason": in.Reason})
	})
}

func (h *Handler) commsConnectionState(w http.ResponseWriter, r *http.Request) {
	var in OpsStateCommand
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "messaging_connections", "inbox:configure", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, target state and reason required."))
		return
	}
	h.execute(w, r, "comms:connection:state:"+id.String(), in, "inbox:configure", "PROPERTY", property, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "inbox:configure", "PROPERTY", property, true)
		if e != nil {
			return 0, nil, e
		}
		c, e := opsLock[MessagingConnection](ctx, tx, "messaging_connections", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if c.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Messaging connection version has changed.")
		}
		if in.State == "CONNECTED" {
			// Activation requires a registered provider contract whose declared
			// capabilities cover every capability configured on the connection.
			if c.Provider == "CF_MEET" {
				if _, unavailable := h.cfMeet().(cfmeet.Unavailable); unavailable {
					return 0, nil, apperrors.Unavailable("The CF Meet owning-service contract is not configured; the connection stays inactive.")
				}
			} else {
				caps := h.messagingAdapter(c.Provider).Capabilities()
				if c.SupportsReviews {
					if _, disabled := h.reviewAdapter(reviewSourceForProvider(c.Provider)).(messaging.DisabledReviews); disabled {
						return 0, nil, apperrors.Unavailable("No approved review adapter exists for this provider; the connection stays inactive.")
					}
				}
				if !caps.Poll && !caps.Reply || c.SupportsReply && !caps.Reply || c.SupportsReadState && !caps.ReadState || c.SupportsPreview && !caps.Preview || c.SupportsAttachments && !caps.Attachments {
					return 0, nil, apperrors.Unavailable("No approved provider adapter supports the configured capabilities; the connection stays inactive.")
				}
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE messaging_connections SET state=$3,health_reason=$4,consecutive_failures=CASE WHEN $3='CONNECTED' THEN 0 ELSE consecutive_failures END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.State, in.Reason); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[MessagingConnection](ctx, tx, "messaging_connections", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "messaging_connection", "STATE_CHANGED", c.State, in.State, businessEvents.MessagingConnectionUpdated, out.Version, map[string]any{"provider": c.Provider, "reason": in.Reason})
	})
}

func defaultSLA(priority string) (time.Duration, time.Duration) {
	switch priority {
	case "URGENT":
		return 15 * time.Minute, 4 * time.Hour
	case "HIGH":
		return 30 * time.Minute, 8 * time.Hour
	case "LOW":
		return 4 * time.Hour, 72 * time.Hour
	}
	return time.Hour, 24 * time.Hour
}

func inboxSLA(ctx context.Context, q database.Querier, org, property ids.ID, priority string) (time.Duration, time.Duration, error) {
	var first, resolution int
	e := q.QueryRow(ctx, `SELECT first_response_seconds,resolution_seconds FROM inbox_sla_policies WHERE organisation_id=$1 AND property_id=$2 AND priority=$3`, org, property, priority).Scan(&first, &resolution)
	if errors.Is(e, pgx.ErrNoRows) {
		f, r := defaultSLA(priority)
		return f, r, nil
	}
	return time.Duration(first) * time.Second, time.Duration(resolution) * time.Second, e
}

// createContextThread inserts a context and its operational thread.
func (h *Handler) createContextThread(ctx context.Context, tx pgx.Tx, t tenant, c MessagingConnection, in CommunicationContextCreate) (CommunicationContext, InboxThread, error) {
	id, thread := ids.New(), ids.New()
	state := "PENDING_AUTHORITY"
	var ref any
	if c.Authority == "EXTERNAL_CHANNEL" {
		state, ref = "ACTIVE", in.ExternalConversationRef
	}
	var creator any
	if !t.Account.IsZero() {
		creator = t.Account
	}
	if _, e := tx.Exec(ctx, `INSERT INTO communication_contexts(id,organisation_id,property_id,connection_id,authority,state,external_conversation_ref,guest_id,reservation_id,support_case_id,subject,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, t.Organisation, c.PropertyID, c.ID, c.Authority, state, ref, in.GuestID, in.ReservationID, in.SupportCaseID, in.Subject, creator); e != nil {
		return CommunicationContext{}, InboxThread{}, e
	}
	priority := in.Priority
	if priority == "" {
		priority = "NORMAL"
	}
	read := "UNAVAILABLE"
	if c.SupportsReadState {
		read = "READ"
	}
	if _, e := tx.Exec(ctx, `INSERT INTO inbox_threads(id,organisation_id,property_id,context_id,priority,read_state) VALUES($1,$2,$3,$4,$5,$6)`, thread, t.Organisation, c.PropertyID, id, priority, read); e != nil {
		return CommunicationContext{}, InboxThread{}, e
	}
	cc, e := opsGet[CommunicationContext](ctx, tx, "communication_contexts", t.Organisation, id)
	if e != nil {
		return cc, InboxThread{}, e
	}
	th, e := opsGet[InboxThread](ctx, tx, "inbox_threads", t.Organisation, thread)
	if e != nil {
		return cc, th, e
	}
	if e = h.opsCommit(ctx, tx, t, c.PropertyID, id, "communication_context", "CREATED", "", state, businessEvents.CommunicationContextUpdated, 1, map[string]any{"authority": c.Authority, "connection_id": c.ID, "thread_id": thread, "reservation_id": in.ReservationID}); e != nil {
		return cc, th, e
	}
	return cc, th, h.opsCommit(ctx, tx, t, c.PropertyID, thread, "inbox_thread", "CREATED", "", "OPEN", businessEvents.InboxThreadUpdated, 1, map[string]any{"context_id": id, "priority": priority})
}

func (h *Handler) commsContexts(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "inbox:read", `SELECT to_jsonb(x) FROM communication_contexts x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR state=$3) ORDER BY created_at DESC,id LIMIT $4 OFFSET $5`, r.URL.Query().Get("state"))
}

func (h *Handler) commsContextCreate(w http.ResponseWriter, r *http.Request) {
	var in CommunicationContextCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.ConnectionID.ValidateV7() != nil || !opsText(in.Subject, 1, 200) || len(in.ExternalConversationRef) > 300 || in.Priority != "" && !slices.Contains(inboxPriorities, in.Priority) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, connection, subject, optional priority and reason required."))
		return
	}
	h.execute(w, r, "comms:context:create:"+in.PropertyID.String(), in, "inbox:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "inbox:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		c, e := opsGet[MessagingConnection](ctx, tx, "messaging_connections", t.Organisation, in.ConnectionID)
		if e != nil {
			return 0, nil, e
		}
		if c.PropertyID != in.PropertyID || c.State != "CONNECTED" && c.State != "DEGRADED" {
			return 0, nil, apperrors.Conflict("CONNECTION_NOT_ACTIVE", "The messaging connection is not active for this property.")
		}
		if (c.Authority == "CF_MEET") != (in.ExternalConversationRef == "") {
			return 0, nil, apperrors.InvalidRequest("CF Meet assigns its conversation reference; external channels require the provider thread reference.")
		}
		if in.GuestID != nil || in.ReservationID != nil {
			var ok bool
			if e = tx.QueryRow(ctx, `SELECT ($3::uuid IS NULL OR EXISTS(SELECT 1 FROM guest_property_scopes WHERE organisation_id=$1 AND property_id=$2 AND guest_id=$3 AND state='ACTIVE')) AND ($4::uuid IS NULL OR EXISTS(SELECT 1 FROM reservations r WHERE r.organisation_id=$1 AND r.property_id=$2 AND r.id=$4 AND ($3::uuid IS NULL OR EXISTS(SELECT 1 FROM reservation_guests g WHERE g.organisation_id=r.organisation_id AND g.reservation_id=r.id AND g.guest_id=$3))))`, t.Organisation, in.PropertyID, in.GuestID, in.ReservationID).Scan(&ok); e != nil {
				return 0, nil, e
			}
			if !ok {
				return 0, nil, apperrors.PermissionDenied("Guest and reservation must be actual records of the same guest in this property.")
			}
		}
		cc, th, e := h.createContextThread(ctx, tx, t, c, in)
		if e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"context": cc, "thread": th}, nil
	})
}

func (h *Handler) commsContextGet(w http.ResponseWriter, r *http.Request) {
	t, _, id, e := h.opsRecordProperty(r, "communication_contexts", "inbox:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	c, e := opsGet[CommunicationContext](r.Context(), h.Auth.Service.Pool, "communication_contexts", t.Organisation, id)
	reply(w, r, 200, c, e)
}

func (h *Handler) commsContextRevoke(w http.ResponseWriter, r *http.Request) {
	var in OpsVersionCommand
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "communication_contexts", "inbox:write", false)
	if e != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "comms:context:revoke:"+id.String(), in, "inbox:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "inbox:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		c, e := opsLock[CommunicationContext](ctx, tx, "communication_contexts", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if c.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Context version has changed.")
		}
		to := "REVOKED"
		if c.State == "PENDING_AUTHORITY" {
			to = "CLOSED"
		}
		if _, e = tx.Exec(ctx, `UPDATE communication_contexts SET state=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, to); e != nil {
			return 0, nil, e
		}
		// Pending business replies on a revoked context are blocked, never sent.
		if _, e = tx.Exec(ctx, `UPDATE scheduled_replies s SET state='CANCELLED',outcome_reason='CONTEXT_REVOKED',version=version+1,updated_at=now() FROM inbox_threads t WHERE t.organisation_id=s.organisation_id AND t.id=s.thread_id AND t.context_id=$2 AND s.organisation_id=$1 AND s.state IN ('DRAFT','SCHEDULED')`, t.Organisation, id); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[CommunicationContext](ctx, tx, "communication_contexts", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "communication_context", "REVOKED", c.State, to, businessEvents.CommunicationContextUpdated, out.Version, map[string]any{"reason": in.Reason})
	})
}

// inboxList is the unified, multi-property operational inbox. Without a
// property filter it returns only threads from properties where the caller
// currently holds inbox:read.
func (h *Handler) inboxList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	properties := []ids.ID{}
	if raw := q.Get("property_id"); raw != "" {
		p, e := ids.ParseV7(raw)
		if e != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid property_id."))
			return
		}
		properties = append(properties, p)
	} else {
		rows, e := h.Auth.Service.Pool.Query(r.Context(), `SELECT DISTINCT property_id FROM inbox_threads WHERE organisation_id=$1 LIMIT 500`, t.Organisation)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		for rows.Next() {
			var p ids.ID
			if e = rows.Scan(&p); e != nil {
				rows.Close()
				reply(w, r, 0, nil, e)
				return
			}
			properties = append(properties, p)
		}
		rows.Close()
	}
	allowed := []ids.ID{}
	for _, p := range properties {
		if _, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "inbox:read", "PROPERTY", p, false); e == nil {
			allowed = append(allowed, p)
		} else if q.Get("property_id") != "" {
			reply(w, r, 0, nil, e)
			return
		}
	}
	assigned := q.Get("assignment")
	if assigned != "" && assigned != "mine" && assigned != "unassigned" {
		reply(w, r, 0, nil, apperrors.InvalidRequest("assignment must be mine or unassigned."))
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x)||jsonb_build_object('subject',c.subject,'authority',c.authority,'context_state',c.state,'reservation_id',c.reservation_id,'guest_id',c.guest_id,'sla_overdue',x.state IN ('OPEN','PENDING_GUEST') AND x.first_response_due_at IS NOT NULL AND x.first_response_due_at<$8 AND (x.first_responded_at IS NULL OR x.first_responded_at<x.last_inbound_at)) FROM inbox_threads x JOIN communication_contexts c ON c.organisation_id=x.organisation_id AND c.id=x.context_id WHERE x.organisation_id=$1 AND x.property_id=ANY($2) AND ($3='' OR x.state=$3) AND ($4='' OR x.priority=$4) AND ($5='' OR $5='mine' AND x.assigned_to=$9 OR $5='unassigned' AND x.assigned_to IS NULL) AND ($10='' OR c.subject ILIKE '%'||$10||'%') ORDER BY CASE x.priority WHEN 'URGENT' THEN 0 WHEN 'HIGH' THEN 1 WHEN 'NORMAL' THEN 2 ELSE 3 END,x.first_response_due_at NULLS LAST,x.id LIMIT $6 OFFSET $7`, t.Organisation, allowed, q.Get("state"), q.Get("priority"), assigned, limit, offset, h.Auth.Service.Now().UTC(), t.Account, strings.ReplaceAll(strings.ReplaceAll(q.Get("search"), "%", ""), "_", ""))
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset, "properties": allowed}, e)
}

func (h *Handler) inboxThreadGet(w http.ResponseWriter, r *http.Request) {
	t, _, id, e := h.opsRecordProperty(r, "inbox_threads", "inbox:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	ctx, pool, now := r.Context(), h.Auth.Service.Pool, h.Auth.Service.Now().UTC()
	th, e := opsGet[InboxThread](ctx, pool, "inbox_threads", t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	cc, e := opsGet[CommunicationContext](ctx, pool, "communication_contexts", t.Organisation, th.ContextID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	// Expired previews are never returned even before retention cleanup runs.
	receipts, e := commercialJSONRows(ctx, pool, `SELECT jsonb_build_object('id',id,'direction',direction,'provider_message_id',provider_message_id,'source_at',source_at,'content_digest',content_digest,'read_state',read_state,'attachment_refs',attachment_refs,'preview',CASE WHEN preview_expires_at>$3 THEN preview END) FROM inbox_message_receipts WHERE organisation_id=$1 AND thread_id=$2 ORDER BY source_at,id LIMIT 200`, t.Organisation, id, now)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	assignments, e := commercialJSONRows(ctx, pool, `SELECT to_jsonb(a) FROM inbox_assignments a WHERE organisation_id=$1 AND thread_id=$2 ORDER BY thread_version LIMIT 200`, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	replies, e := commercialJSONRows(ctx, pool, `SELECT to_jsonb(s) FROM scheduled_replies s WHERE organisation_id=$1 AND thread_id=$2 ORDER BY created_at,id LIMIT 200`, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	suggestions, e := commercialJSONRows(ctx, pool, `SELECT to_jsonb(s) FROM ai_suggestions s WHERE organisation_id=$1 AND thread_id=$2 ORDER BY created_at,id LIMIT 200`, t.Organisation, id)
	reply(w, r, 200, map[string]any{"thread": th, "context": cc, "receipts": receipts, "assignments": assignments, "scheduled_replies": replies, "ai_suggestions": suggestions}, e)
}

func (h *Handler) inboxThreadState(w http.ResponseWriter, r *http.Request) {
	var in InboxThreadCommand
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "inbox_threads", "inbox:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) || in.State == "" && in.Priority == "" && in.ReadState == "" || in.State != "" && !slices.Contains([]string{"OPEN", "PENDING_GUEST", "RESOLVED", "CLOSED"}, in.State) || in.Priority != "" && !slices.Contains(inboxPriorities, in.Priority) || in.ReadState != "" && in.ReadState != "READ" && in.ReadState != "UNREAD" {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, a supported state/priority/read-state change and reason required."))
		return
	}
	h.execute(w, r, "inbox:thread:state:"+id.String(), in, "inbox:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "inbox:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		th, e := opsLock[InboxThread](ctx, tx, "inbox_threads", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if th.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Thread version has changed.")
		}
		if in.ReadState != "" {
			var supported bool
			if e = tx.QueryRow(ctx, `SELECT m.supports_read_state FROM communication_contexts c JOIN messaging_connections m ON m.organisation_id=c.organisation_id AND m.id=c.connection_id WHERE c.organisation_id=$1 AND c.id=$2`, t.Organisation, th.ContextID).Scan(&supported); e != nil {
				return 0, nil, e
			}
			if !supported {
				return 0, nil, apperrors.Conflict("READ_STATE_UNAVAILABLE", "The owning provider does not expose read state; it is not reported.")
			}
		}
		state, priority, read := th.State, th.Priority, th.ReadState
		if in.State != "" {
			state = in.State
		}
		if in.Priority != "" {
			priority = in.Priority
		}
		if in.ReadState != "" {
			read = in.ReadState
		}
		var firstDue, resolutionDue = th.FirstResponseDueAt, th.ResolutionDueAt
		if priority != th.Priority && th.LastInboundAt != nil {
			f, res, e := inboxSLA(ctx, tx, t.Organisation, property, priority)
			if e != nil {
				return 0, nil, e
			}
			fd, rd := th.LastInboundAt.Add(f), th.LastInboundAt.Add(res)
			firstDue, resolutionDue = &fd, &rd
		}
		if _, e = tx.Exec(ctx, `UPDATE inbox_threads SET state=$3,priority=$4,read_state=$5,first_response_due_at=$6,resolution_due_at=$7,resolved_at=CASE WHEN $3 IN ('RESOLVED','CLOSED') THEN coalesce(resolved_at,now()) WHEN $3 IN ('OPEN','PENDING_GUEST') THEN NULL ELSE resolved_at END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, state, priority, read, firstDue, resolutionDue); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[InboxThread](ctx, tx, "inbox_threads", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "inbox_thread", "UPDATED", th.State, state, businessEvents.InboxThreadUpdated, out.Version, map[string]any{"priority": priority, "read_state": read, "reason": in.Reason})
	})
}

// staffAuthority finds a current membership for account and checks a
// capability on the property using recorded grants.
func (h *Handler) staffAuthority(ctx context.Context, tx pgx.Tx, org, property, account ids.ID, capability string) (bool, error) {
	var member, role ids.ID
	e := tx.QueryRow(ctx, `SELECT id,role_id FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2 AND status='ACTIVE' ORDER BY effective_at DESC LIMIT 1`, org, account).Scan(&member, &role)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	return h.currentRecordedPMSAuthority(ctx, tx, org, property, member, role, permissions.Permission(capability))
}

func (h *Handler) inboxAssignments(w http.ResponseWriter, r *http.Request) {
	thread := r.URL.Query().Get("thread_id")
	h.opsList(w, r, "inbox:read", `SELECT to_jsonb(a) FROM inbox_assignments a WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR thread_id::text=$3) ORDER BY created_at DESC,id LIMIT $4 OFFSET $5`, thread)
}

func (h *Handler) inboxAssign(w http.ResponseWriter, r *http.Request) {
	var in InboxAssignmentCommand
	if !decode(w, r, &in) {
		return
	}
	if in.ThreadID.ValidateV7() != nil || in.Version < 1 || in.AssigneeID != nil && in.AssigneeID.ValidateV7() != nil || !opsText(in.Reason, 5, 1000) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Thread, current version, optional assignee and reason required."))
		return
	}
	t0, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var property ids.ID
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT property_id FROM inbox_threads WHERE organisation_id=$1 AND id=$2`, t0.Organisation, in.ThreadID).Scan(&property); e != nil {
		reply(w, r, 0, nil, opsNotFound(e))
		return
	}
	h.execute(w, r, "inbox:assign:"+in.ThreadID.String(), in, "inbox:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "inbox:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		th, e := opsLock[InboxThread](ctx, tx, "inbox_threads", t.Organisation, in.ThreadID)
		if e != nil {
			return 0, nil, e
		}
		if th.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Thread version has changed.")
		}
		if in.AssigneeID != nil {
			ok, e := h.staffAuthority(ctx, tx, t.Organisation, property, *in.AssigneeID, "inbox:read")
			if e != nil {
				return 0, nil, e
			}
			if !ok {
				return 0, nil, apperrors.PermissionDenied("The assignee must currently hold inbox access for this property.")
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE inbox_threads SET assigned_to=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, th.ID, in.AssigneeID); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO inbox_assignments(id,organisation_id,property_id,thread_id,thread_version,previous_assignee,assignee,reason,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, ids.New(), t.Organisation, property, th.ID, th.Version+1, th.AssignedTo, in.AssigneeID, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[InboxThread](ctx, tx, "inbox_threads", t.Organisation, th.ID)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, th.ID, "inbox_thread", "ASSIGNED", th.State, th.State, businessEvents.InboxThreadUpdated, out.Version, map[string]any{"assignee_id": in.AssigneeID, "previous_assignee": th.AssignedTo})
	})
}

func (h *Handler) inboxSLAList(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "inbox:read", `SELECT to_jsonb(x) FROM inbox_sla_policies x WHERE organisation_id=$1 AND property_id=$2 ORDER BY priority LIMIT $3 OFFSET $4`)
}

func (h *Handler) inboxSLASave(w http.ResponseWriter, r *http.Request) {
	var in InboxSLACommand
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || !slices.Contains(inboxPriorities, in.Priority) || in.FirstResponseSeconds < 60 || in.FirstResponseSeconds > 604800 || in.ResolutionSeconds < in.FirstResponseSeconds || in.ResolutionSeconds < 300 || in.ResolutionSeconds > 2592000 || in.ExpectedVersion < 0 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, priority, bounded first-response and resolution targets, expected version and reason required."))
		return
	}
	h.execute(w, r, "inbox:sla:"+in.PropertyID.String()+":"+in.Priority, in, "inbox:configure", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "inbox:configure", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		var id ids.ID
		var version int64
		e = tx.QueryRow(ctx, `SELECT id,version FROM inbox_sla_policies WHERE organisation_id=$1 AND property_id=$2 AND priority=$3 FOR UPDATE`, t.Organisation, in.PropertyID, in.Priority).Scan(&id, &version)
		switch {
		case errors.Is(e, pgx.ErrNoRows):
			if in.ExpectedVersion != 0 {
				return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "SLA policy does not exist.")
			}
			id, version = ids.New(), 1
			_, e = tx.Exec(ctx, `INSERT INTO inbox_sla_policies(id,organisation_id,property_id,priority,first_response_seconds,resolution_seconds,created_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, t.Organisation, in.PropertyID, in.Priority, in.FirstResponseSeconds, in.ResolutionSeconds, t.Account)
		case e == nil:
			if version != in.ExpectedVersion {
				return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "SLA policy version has changed.")
			}
			version++
			_, e = tx.Exec(ctx, `UPDATE inbox_sla_policies SET first_response_seconds=$3,resolution_seconds=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.FirstResponseSeconds, in.ResolutionSeconds)
		}
		if e != nil {
			return 0, nil, e
		}
		out, e := oneJSON(ctx, tx, `SELECT to_jsonb(x) FROM inbox_sla_policies x WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, in.PropertyID, id, "inbox_sla_policy", "SAVED", "", "ACTIVE", "", version, in)
	})
}

func (h *Handler) inboxQuickReplies(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "inbox:read", `SELECT to_jsonb(x) FROM quick_reply_templates x WHERE organisation_id=$1 AND property_id=$2 AND state='ACTIVE' ORDER BY name,id LIMIT $3 OFFSET $4`)
}

var quickReplyVariables = []string{"guest_name", "property_name", "check_in_time", "check_out_time", "reservation_reference"}

func (h *Handler) inboxQuickReplyCreate(w http.ResponseWriter, r *http.Request) {
	var in QuickReplyCreate
	if !decode(w, r, &in) {
		return
	}
	ok := in.PropertyID.ValidateV7() == nil && opsText(in.Name, 1, 120) && opsText(in.Body, 1, 2000) && len(in.Variables) <= 10 && validStaffReason(in.Reason)
	for _, v := range in.Variables {
		ok = ok && slices.Contains(quickReplyVariables, v)
	}
	if !ok {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, name, bounded body, supported variables and reason required."))
		return
	}
	if in.Variables == nil {
		in.Variables = []string{}
	}
	h.execute(w, r, "inbox:quick-reply:create:"+in.PropertyID.String(), in, "inbox:configure", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "inbox:configure", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO quick_reply_templates(id,organisation_id,property_id,name,body,variables,created_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, t.Organisation, in.PropertyID, in.Name, in.Body, in.Variables, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := oneJSON(ctx, tx, `SELECT to_jsonb(x) FROM quick_reply_templates x WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, in.PropertyID, id, "quick_reply_template", "CREATED", "", "ACTIVE", "", 1, map[string]any{"name": in.Name, "reason": in.Reason})
	})
}

func (h *Handler) inboxQuickReplyRetire(w http.ResponseWriter, r *http.Request) {
	var in OpsVersionCommand
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "quick_reply_templates", "inbox:configure", false)
	if e != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "inbox:quick-reply:retire:"+id.String(), in, "inbox:configure", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "inbox:configure", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		tag, e := tx.Exec(ctx, `UPDATE quick_reply_templates SET state='RETIRED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND version=$3 AND state='ACTIVE'`, t.Organisation, id, in.Version)
		if e != nil {
			return 0, nil, e
		}
		if tag.RowsAffected() != 1 {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Template version or state has changed.")
		}
		out, e := oneJSON(ctx, tx, `SELECT to_jsonb(x) FROM quick_reply_templates x WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "quick_reply_template", "RETIRED", "ACTIVE", "RETIRED", "", in.Version+1, map[string]any{"reason": in.Reason})
	})
}

func renderQuickReply(body string, allowed []string, values map[string]string) (string, error) {
	for k, v := range values {
		if !slices.Contains(allowed, k) || len(v) > 200 || strings.ContainsAny(v, "{}\x00") {
			return "", apperrors.InvalidRequest("Template variables must be declared and bounded.")
		}
		body = strings.ReplaceAll(body, "{{"+k+"}}", v)
	}
	if strings.Contains(body, "{{") {
		return "", apperrors.InvalidRequest("Every template variable must be supplied.")
	}
	return body, nil
}

func (h *Handler) inboxScheduledReplies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h.opsList(w, r, "inbox:read", `SELECT to_jsonb(x) FROM scheduled_replies x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR thread_id::text=$3) AND ($4='' OR state=$4) ORDER BY send_at,id LIMIT $5 OFFSET $6`, q.Get("thread_id"), q.Get("state"))
}

// replyTarget validates that a thread can carry a business reply now.
func replyTarget(ctx context.Context, tx pgx.Tx, org, thread ids.ID) (InboxThread, CommunicationContext, MessagingConnection, error) {
	th, e := opsLock[InboxThread](ctx, tx, "inbox_threads", org, thread)
	if e != nil {
		return th, CommunicationContext{}, MessagingConnection{}, e
	}
	cc, e := opsGet[CommunicationContext](ctx, tx, "communication_contexts", org, th.ContextID)
	if e != nil {
		return th, cc, MessagingConnection{}, e
	}
	c, e := opsGet[MessagingConnection](ctx, tx, "messaging_connections", org, cc.ConnectionID)
	if e != nil {
		return th, cc, c, e
	}
	if cc.State != "ACTIVE" {
		return th, cc, c, apperrors.Conflict("CONTEXT_NOT_ACTIVE", "The conversation context is not active with its owning authority.")
	}
	if !c.SupportsReply || c.State == "DISCONNECTED" || c.State == "SUSPENDED" {
		return th, cc, c, apperrors.Conflict("REPLY_UNSUPPORTED", "The owning provider connection does not currently permit business replies.")
	}
	return th, cc, c, nil
}

func (h *Handler) inboxScheduledReplyCreate(w http.ResponseWriter, r *http.Request) {
	var in ScheduledReplyCreate
	if !decode(w, r, &in) {
		return
	}
	if in.ThreadID.ValidateV7() != nil || (in.Body == "") == (in.TemplateID == nil) || in.Body != "" && !opsText(in.Body, 1, 4000) || len(in.Variables) > 10 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Thread, exactly one of body or template, bounded variables and reason required."))
		return
	}
	now := h.Auth.Service.Now().UTC()
	sendAt := now
	if in.SendAt != nil {
		sendAt = in.SendAt.UTC()
		if sendAt.Before(now.Add(-time.Minute)) || sendAt.After(now.Add(90*24*time.Hour)) {
			reply(w, r, 0, nil, apperrors.InvalidRequest("send_at must be within the next 90 days."))
			return
		}
	}
	t0, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var property ids.ID
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT property_id FROM inbox_threads WHERE organisation_id=$1 AND id=$2`, t0.Organisation, in.ThreadID).Scan(&property); e != nil {
		reply(w, r, 0, nil, opsNotFound(e))
		return
	}
	h.execute(w, r, "inbox:reply:create:"+in.ThreadID.String(), in, "inbox:reply", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "inbox:reply", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		if _, _, _, e = replyTarget(ctx, tx, t.Organisation, in.ThreadID); e != nil {
			return 0, nil, e
		}
		body := in.Body
		if in.TemplateID != nil {
			var tb string
			var vars []string
			if e = tx.QueryRow(ctx, `SELECT body,variables FROM quick_reply_templates WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND state='ACTIVE'`, t.Organisation, property, *in.TemplateID).Scan(&tb, &vars); e != nil {
				return 0, nil, opsNotFound(e)
			}
			if body, e = renderQuickReply(tb, vars, in.Variables); e != nil {
				return 0, nil, e
			}
		}
		state := "SCHEDULED"
		if in.Draft {
			state = "DRAFT"
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO scheduled_replies(id,organisation_id,property_id,thread_id,template_id,body,send_at,next_attempt_at,state,created_by,created_by_member,created_by_role) VALUES($1,$2,$3,$4,$5,$6,$7,$7,$8,$9,$10,$11)`, id, t.Organisation, property, in.ThreadID, in.TemplateID, body, sendAt, state, t.Account, t.Member, t.Role); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[ScheduledReply](ctx, tx, "scheduled_replies", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, property, id, "scheduled_reply", "CREATED", "", state, businessEvents.InboxReplyUpdated, 1, map[string]any{"thread_id": in.ThreadID, "send_at": sendAt, "reason": in.Reason})
	})
}

func (h *Handler) inboxScheduledReplySchedule(w http.ResponseWriter, r *http.Request) {
	var in ScheduledReplySchedule
	if !decode(w, r, &in) {
		return
	}
	h.replyTransition(w, r, in.Version, in.Reason, "SCHEDULE", func(ctx context.Context, tx pgx.Tx, t tenant, s ScheduledReply) (string, error) {
		if s.State != "DRAFT" {
			return "", apperrors.Conflict("INVALID_STATE", "Only drafts can be scheduled.")
		}
		now := h.Auth.Service.Now().UTC()
		if in.SendAt.Before(now.Add(-time.Minute)) || in.SendAt.After(now.Add(90*24*time.Hour)) {
			return "", apperrors.InvalidRequest("send_at must be within the next 90 days.")
		}
		if _, _, _, e := replyTarget(ctx, tx, t.Organisation, s.ThreadID); e != nil {
			return "", e
		}
		_, e := tx.Exec(ctx, `UPDATE scheduled_replies SET state='SCHEDULED',send_at=$2,next_attempt_at=$2,version=version+1,updated_at=now() WHERE id=$1`, s.ID, in.SendAt.UTC())
		return "SCHEDULED", e
	})
}

func (h *Handler) inboxScheduledReplyCancel(w http.ResponseWriter, r *http.Request) {
	var in OpsVersionCommand
	if !decode(w, r, &in) {
		return
	}
	h.replyTransition(w, r, in.Version, in.Reason, "CANCEL", func(ctx context.Context, tx pgx.Tx, _ tenant, s ScheduledReply) (string, error) {
		if s.State != "DRAFT" && s.State != "SCHEDULED" {
			return "", apperrors.Conflict("REPLY_ALREADY_DISPATCHED", "Only drafts and scheduled replies not yet dispatched can be cancelled.")
		}
		_, e := tx.Exec(ctx, `UPDATE scheduled_replies SET state='CANCELLED',outcome_reason='CANCELLED_BY_OPERATOR',version=version+1,updated_at=now() WHERE id=$1`, s.ID)
		return "CANCELLED", e
	})
}

func (h *Handler) replyTransition(w http.ResponseWriter, r *http.Request, version int64, reason, action string, fn func(context.Context, pgx.Tx, tenant, ScheduledReply) (string, error)) {
	_, property, id, e := h.opsRecordProperty(r, "scheduled_replies", "inbox:reply", false)
	if e != nil || version < 1 || !validStaffReason(reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "inbox:reply:"+action+":"+id.String(), map[string]any{"version": version, "reason": reason}, "inbox:reply", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "inbox:reply", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		s, e := opsLock[ScheduledReply](ctx, tx, "scheduled_replies", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if s.Version != version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Scheduled reply version has changed.")
		}
		to, e := fn(ctx, tx, t, s)
		if e != nil {
			return 0, nil, e
		}
		out, e := opsGet[ScheduledReply](ctx, tx, "scheduled_replies", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "scheduled_reply", action, s.State, to, businessEvents.InboxReplyUpdated, out.Version, map[string]any{"reason": reason})
	})
}

func (h *Handler) supportCases(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "support:read", `SELECT to_jsonb(x)||jsonb_build_object('sla_breached',x.state NOT IN ('RESOLVED','CLOSED','CANCELLED') AND x.sla_due_at<now()) FROM support_cases x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR state=$3) ORDER BY sla_due_at,id LIMIT $4 OFFSET $5`, r.URL.Query().Get("state"))
}

func (h *Handler) supportCaseCreate(w http.ResponseWriter, r *http.Request) {
	var in SupportCaseCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || !slices.Contains([]string{"BOOKING", "PAYMENT", "ACCESS", "MAINTENANCE", "HOUSEKEEPING", "COMPLAINT", "INFORMATION", "OTHER"}, in.Category) || !slices.Contains([]string{"LOW", "MEDIUM", "HIGH", "CRITICAL"}, in.Severity) || !opsText(in.Summary, 5, 500) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, category, severity, summary and reason required."))
		return
	}
	h.execute(w, r, "support:create:"+in.PropertyID.String(), in, "support:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "support:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO support_cases(id,organisation_id,property_id,guest_id,reservation_id,category,severity,summary,sla_due_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, t.Organisation, in.PropertyID, in.GuestID, in.ReservationID, in.Category, in.Severity, in.Summary, h.Auth.Service.Now().UTC().Add(recoverySLA(in.Severity)), t.Account); e != nil {
			return 0, nil, e
		}
		if in.ContextID != nil {
			tag, e := tx.Exec(ctx, `UPDATE communication_contexts SET support_case_id=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND property_id=$4 AND support_case_id IS NULL`, t.Organisation, *in.ContextID, id, in.PropertyID)
			if e != nil {
				return 0, nil, e
			}
			if tag.RowsAffected() != 1 {
				return 0, nil, apperrors.Conflict("CONTEXT_LINK_CONFLICT", "The context is not in this property or is already linked to a case.")
			}
		}
		out, e := opsGet[SupportCase](ctx, tx, "support_cases", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, in.PropertyID, id, "support_case", "OPENED", "", "OPEN", businessEvents.SupportCaseUpdated, 1, map[string]any{"category": in.Category, "severity": in.Severity, "context_id": in.ContextID})
	})
}

func (h *Handler) supportCaseGet(w http.ResponseWriter, r *http.Request) {
	t, _, id, e := h.opsRecordProperty(r, "support_cases", "support:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	c, e := opsGet[SupportCase](r.Context(), h.Auth.Service.Pool, "support_cases", t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	history, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x) FROM operations_history x WHERE organisation_id=$1 AND entity_type='support_case' AND entity_id=$2 ORDER BY sequence LIMIT 500`, t.Organisation, id)
	reply(w, r, 200, map[string]any{"case": c, "history": history}, e)
}

var supportTransitions = map[string]struct {
	from []string
	to   string
}{
	"ASSIGN":        {[]string{"OPEN", "ASSIGNED", "IN_PROGRESS"}, "ASSIGNED"},
	"START":         {[]string{"OPEN", "ASSIGNED", "PENDING_EXTERNAL", "RESOLVED"}, "IN_PROGRESS"},
	"WAIT_EXTERNAL": {[]string{"ASSIGNED", "IN_PROGRESS"}, "PENDING_EXTERNAL"},
	"RESOLVE":       {[]string{"ASSIGNED", "IN_PROGRESS", "PENDING_EXTERNAL"}, "RESOLVED"},
	"CLOSE":         {[]string{"RESOLVED"}, "CLOSED"},
	"CANCEL":        {[]string{"OPEN", "ASSIGNED"}, "CANCELLED"},
}

func (h *Handler) supportCaseTransition(w http.ResponseWriter, r *http.Request) {
	var in SupportCaseTransition
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "support_cases", "support:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rule, known := supportTransitions[in.Action]
	if !known || in.Version < 1 || !validStaffReason(in.Reason) || in.Action == "ASSIGN" && in.AssigneeID == nil || in.Action == "RESOLVE" && !opsText(in.Resolution, 5, 2000) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Supported action, current version, reason, assignee for ASSIGN and resolution for RESOLVE required."))
		return
	}
	h.execute(w, r, "support:transition:"+id.String(), in, "support:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "support:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		c, e := opsLock[SupportCase](ctx, tx, "support_cases", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if c.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Support case version has changed.")
		}
		if !slices.Contains(rule.from, c.State) {
			return 0, nil, apperrors.Conflict("INVALID_TRANSITION", "The action is not allowed from "+c.State+".")
		}
		assignee := c.AssignedTo
		if in.AssigneeID != nil {
			ok, e := h.staffAuthority(ctx, tx, t.Organisation, property, *in.AssigneeID, "support:write")
			if e != nil {
				return 0, nil, e
			}
			if !ok {
				return 0, nil, apperrors.PermissionDenied("The assignee must currently hold support access for this property.")
			}
			assignee = in.AssigneeID
		}
		resolution := c.Resolution
		if in.Resolution != "" {
			resolution = in.Resolution
		}
		if _, e = tx.Exec(ctx, `UPDATE support_cases SET state=$3,assigned_to=$4,resolution=$5,resolved_at=CASE WHEN $3 IN ('RESOLVED','CLOSED') THEN coalesce(resolved_at,now()) WHEN $3='IN_PROGRESS' THEN NULL ELSE resolved_at END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, rule.to, assignee, resolution); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[SupportCase](ctx, tx, "support_cases", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "support_case", in.Action, c.State, rule.to, businessEvents.SupportCaseUpdated, out.Version, map[string]any{"assignee_id": assignee, "reason": in.Reason})
	})
}
