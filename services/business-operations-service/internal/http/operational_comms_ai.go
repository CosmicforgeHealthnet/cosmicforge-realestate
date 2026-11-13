package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/automation"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type AIKnowledgeCreate struct {
	PropertyID      ids.ID    `json:"property_id"`
	Topic           string    `json:"topic"`
	Answer          string    `json:"answer"`
	SourceKind      string    `json:"source_kind"`
	SourceReference string    `json:"source_reference"`
	SourceVersion   int64     `json:"source_version"`
	ExpiresAt       time.Time `json:"expires_at"`
	Reason          string    `json:"reason"`
}

// AIRecommendationRequest submits the guest question as transient input. The
// question text itself is never stored; only its digest and the decision.
type AIRecommendationRequest struct {
	ThreadID ids.ID `json:"thread_id"`
	Question string `json:"question"`
	Reason   string `json:"reason"`
}

type AISuggestion struct {
	ID                ids.ID          `json:"id"`
	PropertyID        ids.ID          `json:"property_id"`
	ThreadID          ids.ID          `json:"thread_id"`
	Kind              string          `json:"kind"`
	State             string          `json:"state"`
	Engine            string          `json:"engine"`
	ModelVersion      string          `json:"model_version"`
	Topic             *string         `json:"topic"`
	Answer            *string         `json:"answer"`
	Citations         json.RawMessage `json:"citations"`
	ConfidenceBPS     int             `json:"confidence_bps"`
	RefusalReason     string          `json:"refusal_reason"`
	InjectionDetected bool            `json:"injection_detected"`
	ExpiresAt         time.Time       `json:"expires_at"`
	Version           int64           `json:"version"`
}

type AISuggestionDecision struct {
	Version int64      `json:"version"`
	Action  string     `json:"action"`
	SendAt  *time.Time `json:"send_at,omitempty"`
	Reason  string     `json:"reason"`
}

type ActionCard struct {
	ID                     ids.ID          `json:"id"`
	PropertyID             ids.ID          `json:"property_id"`
	ThreadID               *ids.ID         `json:"thread_id"`
	SuggestionID           *ids.ID         `json:"suggestion_id"`
	ActionType             string          `json:"action_type"`
	TargetType             string          `json:"target_type"`
	TargetID               ids.ID          `json:"target_id"`
	TargetVersion          *int64          `json:"target_version"`
	Parameters             json.RawMessage `json:"parameters"`
	Explanation            string          `json:"explanation"`
	RiskLevel              string          `json:"risk_level"`
	RequiredCapability     string          `json:"required_capability"`
	RequiredApprovalAction string          `json:"required_approval_action"`
	OwningRoute            string          `json:"owning_route"`
	Source                 string          `json:"source"`
	State                  string          `json:"state"`
	ExpiresAt              time.Time       `json:"expires_at"`
	ExecutionReference     *ids.ID         `json:"execution_reference"`
	Version                int64           `json:"version"`
}

type ActionCardCreate struct {
	PropertyID    ids.ID         `json:"property_id"`
	ThreadID      *ids.ID        `json:"thread_id,omitempty"`
	ActionType    string         `json:"action_type"`
	TargetID      ids.ID         `json:"target_id"`
	TargetVersion *int64         `json:"target_version,omitempty"`
	Parameters    map[string]any `json:"parameters"`
	Explanation   string         `json:"explanation"`
	Reason        string         `json:"reason"`
}

type ActionCardDecision struct {
	Version            int64   `json:"version"`
	Action             string  `json:"action"`
	ExecutionReference *ids.ID `json:"execution_reference,omitempty"`
	Reason             string  `json:"reason"`
}

type AutomationRule struct {
	ID                  ids.ID          `json:"id"`
	PropertyID          ids.ID          `json:"property_id"`
	Kind                string          `json:"kind"`
	State               string          `json:"state"`
	Config              json.RawMessage `json:"config"`
	SponsorAccountID    *ids.ID         `json:"sponsor_account_id"`
	SponsorMembershipID *ids.ID         `json:"sponsor_membership_id"`
	SponsorRoleID       *ids.ID         `json:"sponsor_role_id"`
	ApprovalID          *ids.ID         `json:"approval_id"`
	Reason              string          `json:"reason"`
	Version             int64           `json:"version"`
}

type AutomationRuleCreate struct {
	PropertyID ids.ID         `json:"property_id"`
	Kind       string         `json:"kind"`
	Config     map[string]any `json:"config"`
	Reason     string         `json:"reason"`
}

type AutomationRuleEnable struct {
	Version    int64  `json:"version"`
	ApprovalID ids.ID `json:"approval_id,omitempty"`
	Reason     string `json:"reason"`
}

func (h *Handler) registerCommsAI(r chi.Router) {
	r.Get("/ai/knowledge", h.aiKnowledge)
	r.Post("/ai/knowledge", h.aiKnowledgeCreate)
	r.Post("/ai/knowledge/{id}/retire", h.aiKnowledgeRetire)
	r.Get("/ai/recommendations", h.aiRecommendations)
	r.Post("/ai/recommendations", h.aiRecommendationCreate)
	r.Post("/ai/recommendations/{id}/decision", h.aiRecommendationDecide)
	r.Get("/action-cards", h.actionCards)
	r.Post("/action-cards", h.actionCardCreate)
	r.Post("/action-cards/{id}/decision", h.actionCardDecide)
	r.Get("/automation-rules", h.automationRules)
	r.Post("/automation-rules", h.automationRuleCreate)
	r.Post("/automation-rules/{id}/enable", h.automationRuleEnable)
	r.Post("/automation-rules/{id}/disable", h.automationRuleDisable)
}

var aiTopics = []string{"CHECK_IN", "CHECK_OUT", "PARKING", "WIFI", "BREAKFAST", "PETS", "SMOKING", "ACCESSIBILITY", "HOUSE_RULES", "CANCELLATION_POLICY", "LOCATION", "FACILITIES", "CONTACT"}

func (h *Handler) aiKnowledge(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "ai:read", `SELECT to_jsonb(x) FROM ai_knowledge_entries x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR state=$3) ORDER BY topic,created_at DESC LIMIT $4 OFFSET $5`, r.URL.Query().Get("state"))
}

func (h *Handler) aiKnowledgeCreate(w http.ResponseWriter, r *http.Request) {
	var in AIKnowledgeCreate
	if !decode(w, r, &in) {
		return
	}
	now := h.Auth.Service.Now().UTC()
	if in.PropertyID.ValidateV7() != nil || !slices.Contains(aiTopics, in.Topic) || !opsText(in.Answer, 5, 1000) || !slices.Contains([]string{"LISTING_VERSION", "PROPERTY_POLICY", "RESERVATION_POLICY", "OPERATOR_APPROVED"}, in.SourceKind) || !opsText(in.SourceReference, 3, 300) || in.SourceVersion < 1 || !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(400*24*time.Hour)) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, approved topic, answer, source provenance, future expiry and reason required."))
		return
	}
	h.execute(w, r, "ai:knowledge:"+in.PropertyID.String()+":"+in.Topic, in, "ai:configure", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "ai:configure", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		// One approved answer per topic: the previous version is retired.
		if _, e = tx.Exec(ctx, `UPDATE ai_knowledge_entries SET state='RETIRED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND property_id=$2 AND topic=$3 AND state='ACTIVE'`, t.Organisation, in.PropertyID, in.Topic); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO ai_knowledge_entries(id,organisation_id,property_id,topic,answer,source_kind,source_reference,source_version,expires_at,approved_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, t.Organisation, in.PropertyID, in.Topic, in.Answer, in.SourceKind, in.SourceReference, in.SourceVersion, in.ExpiresAt.UTC(), t.Account); e != nil {
			return 0, nil, e
		}
		out, e := oneJSON(ctx, tx, `SELECT to_jsonb(x) FROM ai_knowledge_entries x WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, in.PropertyID, id, "ai_knowledge_entry", "APPROVED", "", "ACTIVE", "", 1, map[string]any{"topic": in.Topic, "source_kind": in.SourceKind, "source_reference": in.SourceReference, "source_version": in.SourceVersion})
	})
}

func (h *Handler) aiKnowledgeRetire(w http.ResponseWriter, r *http.Request) {
	var in OpsVersionCommand
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "ai_knowledge_entries", "ai:configure", false)
	if e != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "ai:knowledge:retire:"+id.String(), in, "ai:configure", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "ai:configure", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		tag, e := tx.Exec(ctx, `UPDATE ai_knowledge_entries SET state='RETIRED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND version=$3 AND state='ACTIVE'`, t.Organisation, id, in.Version)
		if e != nil {
			return 0, nil, e
		}
		if tag.RowsAffected() != 1 {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Knowledge entry version or state has changed.")
		}
		return 200, map[string]any{"id": id, "state": "RETIRED", "version": in.Version + 1}, h.opsCommit(ctx, tx, t, property, id, "ai_knowledge_entry", "RETIRED", "ACTIVE", "RETIRED", "", in.Version+1, map[string]any{"reason": in.Reason})
	})
}

func (h *Handler) aiRecommendations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h.opsList(w, r, "ai:read", `SELECT to_jsonb(x) FROM ai_suggestions x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR thread_id::text=$3) AND ($4='' OR state=$4) ORDER BY created_at DESC,id LIMIT $5 OFFSET $6`, q.Get("thread_id"), q.Get("state"))
}

// groundingInputs loads approved entries for the property and only the facts
// of the reservation bound to this conversation context.
func groundingInputs(ctx context.Context, tx pgx.Tx, org ids.ID, th InboxThread) ([]automation.KnowledgeEntry, *automation.ReservationFacts, CommunicationContext, error) {
	cc, e := opsGet[CommunicationContext](ctx, tx, "communication_contexts", org, th.ContextID)
	if e != nil {
		return nil, nil, cc, e
	}
	rows, e := tx.Query(ctx, `SELECT id::text,topic,answer,source_kind,source_reference,source_version,expires_at FROM ai_knowledge_entries WHERE organisation_id=$1 AND property_id=$2 AND state='ACTIVE'`, org, th.PropertyID)
	if e != nil {
		return nil, nil, cc, e
	}
	entries := []automation.KnowledgeEntry{}
	for rows.Next() {
		var k automation.KnowledgeEntry
		if e = rows.Scan(&k.ID, &k.Topic, &k.Answer, &k.SourceKind, &k.SourceReference, &k.SourceVersion, &k.ExpiresAt); e != nil {
			rows.Close()
			return nil, nil, cc, e
		}
		entries = append(entries, k)
	}
	rows.Close()
	var facts *automation.ReservationFacts
	if cc.ReservationID != nil {
		f := automation.ReservationFacts{}
		if e = tx.QueryRow(ctx, `SELECT id::text,state,date_from::text,date_to::text FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND id=$3`, org, th.PropertyID, *cc.ReservationID).Scan(&f.ReservationID, &f.State, &f.DateFrom, &f.DateTo); e != nil {
			return nil, nil, cc, e
		}
		facts = &f
	}
	return entries, facts, cc, rows.Err()
}

// recordSuggestion persists the AI decision with full provenance and proposes
// action cards for detected intents (never for instruction-like content).
func (h *Handler) recordSuggestion(ctx context.Context, tx pgx.Tx, t tenant, th InboxThread, cc CommunicationContext, a automation.Answer, question, state string, inputs map[string]any) (AISuggestion, []ActionCard, error) {
	id := ids.New()
	now := h.Auth.Service.Now().UTC()
	citations, _ := json.Marshal(a.Citations)
	var topic, answer any
	if a.Topic != "" {
		topic = a.Topic
	}
	if a.Text != "" {
		answer = a.Text
	}
	var creator any
	if !t.Account.IsZero() {
		creator = t.Account
	}
	if _, e := tx.Exec(ctx, `INSERT INTO ai_suggestions(id,organisation_id,property_id,thread_id,kind,question_digest,state,engine,model_version,topic,answer,citations,confidence_bps,refusal_reason,injection_detected,input_references,expires_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`, id, t.Organisation, th.PropertyID, th.ID, a.Kind, security.SHA256Hex([]byte(question)), state, automation.Engine, automation.ModelVersion, topic, answer, citations, a.ConfidenceBPS, a.RefusalReason, a.InjectionDetected, marshalCommercial(inputs), now.Add(24*time.Hour), creator); e != nil {
		return AISuggestion{}, nil, e
	}
	s, e := opsGet[AISuggestion](ctx, tx, "ai_suggestions", t.Organisation, id)
	if e != nil {
		return s, nil, e
	}
	if e = h.opsCommit(ctx, tx, t, th.PropertyID, id, "ai_suggestion", "PROPOSED", "", state, businessEvents.AISuggestionUpdated, 1, map[string]any{"kind": a.Kind, "engine": automation.Engine, "model_version": automation.ModelVersion, "confidence_bps": a.ConfidenceBPS, "citations": a.Citations, "refusal_reason": a.RefusalReason, "injection_detected": a.InjectionDetected}); e != nil {
		return s, nil, e
	}
	cards := []ActionCard{}
	if a.InjectionDetected {
		return s, cards, nil
	}
	for _, intent := range a.Intents {
		p, _ := automation.Policy(intent)
		var target ids.ID
		var version *int64
		switch p.TargetType {
		case "RESERVATION":
			if cc.ReservationID == nil {
				continue
			}
			var v int64
			if e = tx.QueryRow(ctx, `SELECT version FROM reservations WHERE organisation_id=$1 AND id=$2`, t.Organisation, *cc.ReservationID).Scan(&v); e != nil {
				return s, cards, e
			}
			target, version = *cc.ReservationID, &v
		case "PROPERTY":
			target = th.PropertyID
		case "THREAD":
			target = th.ID
		default:
			continue
		}
		card, e := h.insertActionCard(ctx, tx, t, th.PropertyID, &th.ID, &id, intent, target, version, map[string]any{"detected_from": "guest_message_digest"}, "Detected request in the guest conversation; review with the owning workflow.", "AI_SUGGESTION")
		if e != nil {
			return s, cards, e
		}
		cards = append(cards, card)
	}
	return s, cards, nil
}

func (h *Handler) insertActionCard(ctx context.Context, tx pgx.Tx, t tenant, property ids.ID, thread, suggestion *ids.ID, action string, target ids.ID, version *int64, params map[string]any, explanation, source string) (ActionCard, error) {
	p, ok := automation.Policy(action)
	if !ok {
		return ActionCard{}, apperrors.InvalidRequest("Unsupported action type.")
	}
	var creator any
	if !t.Account.IsZero() {
		creator = t.Account
	}
	id := ids.New()
	if _, e := tx.Exec(ctx, `INSERT INTO action_cards(id,organisation_id,property_id,thread_id,suggestion_id,action_type,target_type,target_id,target_version,parameters,explanation,risk_level,required_capability,required_approval_action,owning_route,source,expires_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`, id, t.Organisation, property, thread, suggestion, action, p.TargetType, target, version, marshalCommercial(params), explanation, p.Risk, p.Capability, p.ApprovalAction, p.OwningRoute, source, h.Auth.Service.Now().UTC().Add(72*time.Hour), creator); e != nil {
		return ActionCard{}, e
	}
	card, e := opsGet[ActionCard](ctx, tx, "action_cards", t.Organisation, id)
	if e != nil {
		return card, e
	}
	return card, h.opsCommit(ctx, tx, t, property, id, "action_card", "PROPOSED", "", "PROPOSED", businessEvents.AutomationActionProposed, 1, map[string]any{"action_type": action, "target_type": p.TargetType, "target_id": target, "required_capability": p.Capability, "required_approval_action": p.ApprovalAction, "risk_level": p.Risk, "source": source, "executes": false})
}

func (h *Handler) aiRecommendationCreate(w http.ResponseWriter, r *http.Request) {
	var in AIRecommendationRequest
	if !decode(w, r, &in) {
		return
	}
	if in.ThreadID.ValidateV7() != nil || !opsText(in.Question, 1, 2000) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Thread, bounded question text and reason required."))
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
	h.execute(w, r, "ai:recommendation:"+in.ThreadID.String(), in, "inbox:reply", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "inbox:reply", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		th, e := opsGet[InboxThread](ctx, tx, "inbox_threads", t.Organisation, in.ThreadID)
		if e != nil {
			return 0, nil, e
		}
		entries, facts, cc, e := groundingInputs(ctx, tx, t.Organisation, th)
		if e != nil {
			return 0, nil, e
		}
		a := automation.Ground(in.Question, h.Auth.Service.Now().UTC(), entries, facts)
		state := "PROPOSED"
		if a.Kind == "ESCALATION" {
			state = "ESCALATED"
		}
		s, cards, e := h.recordSuggestion(ctx, tx, t, th, cc, a, in.Question, state, map[string]any{"thread_id": th.ID, "context_id": cc.ID, "reservation_id": cc.ReservationID, "approved_entries": len(entries)})
		if e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"suggestion": s, "action_cards": cards, "executed": false}, nil
	})
}

func (h *Handler) aiRecommendationDecide(w http.ResponseWriter, r *http.Request) {
	var in AISuggestionDecision
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "ai_suggestions", "inbox:reply", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || !slices.Contains([]string{"ACCEPT", "REJECT", "ESCALATE"}, in.Action) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, ACCEPT/REJECT/ESCALATE and reason required."))
		return
	}
	h.execute(w, r, "ai:recommendation:decide:"+id.String(), in, "inbox:reply", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "inbox:reply", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		s, e := opsLock[AISuggestion](ctx, tx, "ai_suggestions", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		if s.Version != in.Version || s.State != "PROPOSED" {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Suggestion version or state has changed.")
		}
		if !now.Before(s.ExpiresAt) {
			return 0, nil, apperrors.Conflict("SUGGESTION_EXPIRED", "The suggestion expired; request a fresh grounded answer.")
		}
		to := map[string]string{"ACCEPT": "ACCEPTED", "REJECT": "REJECTED", "ESCALATE": "ESCALATED"}[in.Action]
		result := map[string]any{}
		if in.Action == "ACCEPT" {
			if s.Kind != "FACTUAL_REPLY" || s.Answer == nil {
				return 0, nil, apperrors.Conflict("NOT_A_FACTUAL_ANSWER", "Only grounded factual answers can be accepted as replies.")
			}
			// Every cited approved source must still be the same active version.
			var cites []automation.Citation
			if e = json.Unmarshal(s.Citations, &cites); e != nil {
				return 0, nil, e
			}
			for _, c := range cites {
				if c.SourceKind == "RESERVATION" {
					continue
				}
				var current bool
				if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_knowledge_entries WHERE organisation_id=$1 AND id::text=$2 AND state='ACTIVE' AND source_version=$3 AND expires_at>$4)`, t.Organisation, c.SourceID, c.SourceVersion, now).Scan(&current); e != nil {
					return 0, nil, e
				}
				if !current {
					return 0, nil, apperrors.Conflict("GROUNDING_SOURCE_CHANGED", "A cited source was retired or changed; regenerate the answer.")
				}
			}
			if _, _, _, e = replyTarget(ctx, tx, t.Organisation, s.ThreadID); e != nil {
				return 0, nil, e
			}
			sendAt := now
			if in.SendAt != nil && in.SendAt.After(now) && in.SendAt.Before(now.Add(90*24*time.Hour)) {
				sendAt = in.SendAt.UTC()
			}
			reply := ids.New()
			if _, e = tx.Exec(ctx, `INSERT INTO scheduled_replies(id,organisation_id,property_id,thread_id,suggestion_id,body,ai_attributed,send_at,next_attempt_at,created_by,created_by_member,created_by_role) VALUES($1,$2,$3,$4,$5,$6,true,$7,$7,$8,$9,$10)`, reply, t.Organisation, property, s.ThreadID, s.ID, aiAttribution+*s.Answer, sendAt, t.Account, t.Member, t.Role); e != nil {
				return 0, nil, e
			}
			if e = h.opsCommit(ctx, tx, t, property, reply, "scheduled_reply", "CREATED", "", "SCHEDULED", businessEvents.InboxReplyUpdated, 1, map[string]any{"thread_id": s.ThreadID, "suggestion_id": s.ID, "ai_attributed": true}); e != nil {
				return 0, nil, e
			}
			result["scheduled_reply_id"] = reply
		}
		if _, e = tx.Exec(ctx, `UPDATE ai_suggestions SET state=$3,decided_by=$4,decided_at=now(),version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, to, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[AISuggestion](ctx, tx, "ai_suggestions", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		result["suggestion"] = out
		return 200, result, h.opsCommit(ctx, tx, t, property, id, "ai_suggestion", in.Action, "PROPOSED", to, businessEvents.AISuggestionUpdated, out.Version, map[string]any{"reason": in.Reason, "operator_decision": in.Action})
	})
}

// aiAttribution clearly marks automated answers to guests.
const aiAttribution = "[Automated answer from approved property information] "

func (h *Handler) actionCards(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "ai:read", `SELECT to_jsonb(x) FROM action_cards x WHERE organisation_id=$1 AND property_id=$2 AND ($3='' OR state=$3) ORDER BY created_at DESC,id LIMIT $4 OFFSET $5`, r.URL.Query().Get("state"))
}

// actionTargetCurrent checks the target belongs to the property and returns
// its current version when versioned.
func actionTargetCurrent(ctx context.Context, tx pgx.Tx, org, property ids.ID, targetType string, target ids.ID) (*int64, error) {
	var v int64
	var e error
	switch targetType {
	case "RESERVATION":
		e = tx.QueryRow(ctx, `SELECT version FROM reservations WHERE organisation_id=$1 AND property_id=$2 AND id=$3`, org, property, target).Scan(&v)
	case "PROPERTY":
		if target != property {
			return nil, apperrors.PermissionDenied("Target must be the card property.")
		}
		return nil, nil
	case "THREAD":
		e = tx.QueryRow(ctx, `SELECT version FROM inbox_threads WHERE organisation_id=$1 AND property_id=$2 AND id=$3`, org, property, target).Scan(&v)
	case "RATE_PLAN":
		e = tx.QueryRow(ctx, `SELECT version FROM rate_plans WHERE organisation_id=$1 AND property_id=$2 AND id=$3`, org, property, target).Scan(&v)
	default:
		return nil, apperrors.InvalidRequest("Unsupported target.")
	}
	if e != nil {
		return nil, opsNotFound(e)
	}
	return &v, nil
}

func (h *Handler) actionCardCreate(w http.ResponseWriter, r *http.Request) {
	var in ActionCardCreate
	if !decode(w, r, &in) {
		return
	}
	p, known := automation.Policy(in.ActionType)
	if in.PropertyID.ValidateV7() != nil || !known || in.TargetID.ValidateV7() != nil || !opsText(in.Explanation, 5, 1000) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, supported action, target, explanation and reason required."))
		return
	}
	if in.Parameters == nil {
		in.Parameters = map[string]any{}
	}
	h.execute(w, r, "action-card:create:"+in.PropertyID.String(), in, "inbox:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "inbox:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		current, e := actionTargetCurrent(ctx, tx, t.Organisation, in.PropertyID, p.TargetType, in.TargetID)
		if e != nil {
			return 0, nil, e
		}
		if in.TargetVersion != nil && (current == nil || *current != *in.TargetVersion) {
			return 0, nil, apperrors.Conflict("STALE_TARGET", "The target version has changed.")
		}
		card, e := h.insertActionCard(ctx, tx, t, in.PropertyID, in.ThreadID, nil, in.ActionType, in.TargetID, current, in.Parameters, in.Explanation, "OPERATOR")
		return 201, card, e
	})
}

// executionEvidence verifies that an owning workflow record exists for the
// target. A card is marked EXECUTED only from this evidence.
func executionEvidence(ctx context.Context, tx pgx.Tx, org ids.ID, c ActionCard, ref ids.ID) (bool, error) {
	var ok bool
	var e error
	switch c.ActionType {
	case "RESERVATION_AMENDMENT", "CANCELLATION_REVIEW":
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_versions WHERE organisation_id=$1 AND id=$2 AND reservation_id=$3 AND reservation_version>coalesce($4,0))`, org, ref, c.TargetID, c.TargetVersion).Scan(&ok)
	case "REFUND_REVIEW":
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_financial_requests WHERE organisation_id=$1 AND id=$2 AND reservation_id=$3)`, org, ref, c.TargetID).Scan(&ok)
	case "SUPPORT_CASE":
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM support_cases WHERE organisation_id=$1 AND id=$2 AND property_id=$3)`, org, ref, c.PropertyID).Scan(&ok)
	case "MAINTENANCE_REQUEST":
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM work_orders WHERE organisation_id=$1 AND id=$2 AND property_id=$3)`, org, ref, c.PropertyID).Scan(&ok)
	case "HOUSEKEEPING_ESCALATION":
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM housekeeping_tasks WHERE organisation_id=$1 AND id=$2 AND property_id=$3)`, org, ref, c.PropertyID).Scan(&ok)
	case "STAFF_FOLLOW_UP":
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_assignments WHERE organisation_id=$1 AND id=$2 AND thread_id=$3)`, org, ref, c.TargetID).Scan(&ok)
	case "RATE_REVIEW":
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pricing_recommendations WHERE organisation_id=$1 AND id=$2 AND property_id=$3)`, org, ref, c.PropertyID).Scan(&ok)
	case "ACCESS_ISSUE":
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hospitality_access_jobs WHERE organisation_id=$1 AND id=$2 AND property_id=$3)`, org, ref, c.PropertyID).Scan(&ok)
	}
	return ok, e
}

func (h *Handler) actionCardDecide(w http.ResponseWriter, r *http.Request) {
	var in ActionCardDecision
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "action_cards", "ai:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || !slices.Contains([]string{"ACCEPT", "DISMISS", "RECORD_EXECUTION"}, in.Action) || in.Action == "RECORD_EXECUTION" && (in.ExecutionReference == nil || in.ExecutionReference.ValidateV7() != nil) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, ACCEPT/DISMISS/RECORD_EXECUTION, execution reference for RECORD_EXECUTION and reason required."))
		return
	}
	h.execute(w, r, "action-card:decide:"+id.String(), in, "ai:read", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "ai:read", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		c, e := opsLock[ActionCard](ctx, tx, "action_cards", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if c.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Action card version has changed.")
		}
		// The deciding operator must personally hold the owning capability; the
		// card itself confers nothing.
		if in.Action != "DISMISS" {
			if _, _, e = h.authorize(ctx, tx, c.RequiredCapability, "PROPERTY", property, false); e != nil {
				return 0, nil, e
			}
		}
		now := h.Auth.Service.Now().UTC()
		var to string
		var execRef any
		switch in.Action {
		case "ACCEPT":
			if c.State != "PROPOSED" {
				return 0, nil, apperrors.Conflict("INVALID_TRANSITION", "Only proposed cards can be accepted.")
			}
			if !now.Before(c.ExpiresAt) {
				return 0, nil, apperrors.Conflict("ACTION_CARD_EXPIRED", "The card expired.")
			}
			current, e := actionTargetCurrent(ctx, tx, t.Organisation, property, c.TargetType, c.TargetID)
			if e != nil {
				return 0, nil, e
			}
			if c.TargetVersion != nil && (current == nil || *current != *c.TargetVersion) {
				return 0, nil, apperrors.Conflict("STALE_TARGET", "The target changed since the card was proposed; review current state.")
			}
			to = "ACCEPTED"
		case "DISMISS":
			if c.State != "PROPOSED" && c.State != "ACCEPTED" {
				return 0, nil, apperrors.Conflict("INVALID_TRANSITION", "The card is final.")
			}
			to = "DISMISSED"
		case "RECORD_EXECUTION":
			if c.State != "ACCEPTED" {
				return 0, nil, apperrors.Conflict("INVALID_TRANSITION", "Only accepted cards record an owning execution.")
			}
			ok, e := executionEvidence(ctx, tx, t.Organisation, c, *in.ExecutionReference)
			if e != nil {
				return 0, nil, e
			}
			if !ok {
				return 0, nil, apperrors.Conflict("EXECUTION_EVIDENCE_MISSING", "No owning workflow record proves this execution.")
			}
			to, execRef = "EXECUTED", *in.ExecutionReference
		}
		if _, e = tx.Exec(ctx, `UPDATE action_cards SET state=$3,execution_reference=coalesce($4,execution_reference),decided_by=$5,decided_at=now(),version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, to, execRef, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[ActionCard](ctx, tx, "action_cards", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, map[string]any{"card": out, "executed_by_card": false, "owning_route": c.OwningRoute, "required_approval_action": c.RequiredApprovalAction}, h.opsCommit(ctx, tx, t, property, id, "action_card", in.Action, c.State, to, businessEvents.AutomationActionProposed, out.Version, map[string]any{"reason": in.Reason, "execution_reference": execRef})
	})
}

var automationKinds = []string{"AI_FACTUAL_AUTOREPLY", "INBOX_SLA_ESCALATION", "ACTION_CARD_SUGGESTION", "QUALITY_ESCALATION", "LEAD_FOLLOW_UP"}

func (h *Handler) automationRules(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "automation:read", `SELECT to_jsonb(x) FROM automation_rules x WHERE organisation_id=$1 AND property_id=$2 ORDER BY created_at DESC,id LIMIT $3 OFFSET $4`)
}

func validAutomationConfig(kind string, c map[string]any) bool {
	raw, _ := json.Marshal(c)
	if len(raw) > 8192 {
		return false
	}
	if v, ok := c["minimum_confidence_bps"]; ok {
		f, ok := v.(float64)
		if !ok || f < automation.MinimumConfidenceBPS || f > 10000 {
			return false
		}
	}
	if v, ok := c["follow_up_hours"]; ok {
		f, ok := v.(float64)
		if !ok || f < 1 || f > 720 {
			return false
		}
	}
	for k := range c {
		if !slices.Contains([]string{"minimum_confidence_bps", "follow_up_hours", "escalate_to_priority", "repeat_threshold", "window_days"}, k) {
			return false
		}
	}
	return slices.Contains(automationKinds, kind)
}

func (h *Handler) automationRuleCreate(w http.ResponseWriter, r *http.Request) {
	var in AutomationRuleCreate
	if !decode(w, r, &in) {
		return
	}
	if in.Config == nil {
		in.Config = map[string]any{}
	}
	if in.PropertyID.ValidateV7() != nil || !validAutomationConfig(in.Kind, in.Config) || !opsText(in.Reason, 5, 1000) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, supported kind, bounded configuration and reason required."))
		return
	}
	h.execute(w, r, "automation:create:"+in.PropertyID.String(), in, "automation:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "automation:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO automation_rules(id,organisation_id,property_id,kind,config,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, t.Organisation, in.PropertyID, in.Kind, marshalCommercial(in.Config), in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[AutomationRule](ctx, tx, "automation_rules", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 201, out, h.opsCommit(ctx, tx, t, in.PropertyID, id, "automation_rule", "CREATED", "", "DRAFT", businessEvents.AutomationRuleUpdated, 1, map[string]any{"kind": in.Kind, "config": in.Config})
	})
}

// automationCapability is what the sponsor must hold for a rule to act.
func automationCapability(kind string) string {
	switch kind {
	case "AI_FACTUAL_AUTOREPLY":
		return "inbox:reply"
	case "INBOX_SLA_ESCALATION", "ACTION_CARD_SUGGESTION":
		return "inbox:write"
	case "QUALITY_ESCALATION":
		return "quality:write"
	case "LEAD_FOLLOW_UP":
		return "leads:write"
	}
	return ""
}

func (h *Handler) automationRuleEnable(w http.ResponseWriter, r *http.Request) {
	var in AutomationRuleEnable
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "automation_rules", "automation:write", true)
	if e != nil || in.Version < 1 || !opsText(in.Reason, 5, 1000) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "automation:enable:"+id.String(), in, "automation:write", "PROPERTY", property, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "automation:write", "PROPERTY", property, true)
		if e != nil {
			return 0, nil, e
		}
		rule, e := opsLock[AutomationRule](ctx, tx, "automation_rules", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if rule.Version != in.Version || rule.State == "ENABLED" {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Rule version or state has changed.")
		}
		// The sponsor must personally hold the capability the rule exercises.
		if _, _, e = h.authorize(ctx, tx, automationCapability(rule.Kind), "PROPERTY", property, true); e != nil {
			return 0, nil, e
		}
		var approval any
		if rule.Kind == "AI_FACTUAL_AUTOREPLY" {
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_AI_AUTOREPLY_ACTIVATION", target.Scope, in.ApprovalID, map[string]any{"rule_id": rule.ID, "config": rule.Config}, "", nil, ""); e != nil {
				return 0, nil, e
			}
			approval = in.ApprovalID
		}
		if _, e = tx.Exec(ctx, `UPDATE automation_rules SET state='ENABLED',sponsor_account_id=$3,sponsor_membership_id=$4,sponsor_role_id=$5,approval_id=coalesce($6,approval_id),version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, t.Account, t.Member, t.Role, approval); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[AutomationRule](ctx, tx, "automation_rules", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "automation_rule", "ENABLED", rule.State, "ENABLED", businessEvents.AutomationRuleUpdated, out.Version, map[string]any{"approval_id": approval, "reason": in.Reason})
	})
}

// automationRuleDisable is the kill switch: no MFA so it can always be used.
func (h *Handler) automationRuleDisable(w http.ResponseWriter, r *http.Request) {
	var in OpsVersionCommand
	if !decode(w, r, &in) {
		return
	}
	_, property, id, e := h.opsRecordProperty(r, "automation_rules", "automation:write", false)
	if e != nil || in.Version < 1 || !validStaffReason(in.Reason) {
		if e == nil {
			e = apperrors.InvalidRequest("Current version and reason required.")
		}
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "automation:disable:"+id.String(), in, "automation:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "automation:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		rule, e := opsLock[AutomationRule](ctx, tx, "automation_rules", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if rule.Version != in.Version || rule.State == "DISABLED" {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Rule version or state has changed.")
		}
		if _, e = tx.Exec(ctx, `UPDATE automation_rules SET state='DISABLED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id); e != nil {
			return 0, nil, e
		}
		// Pending automated replies from this rule are cancelled immediately.
		if _, e = tx.Exec(ctx, `UPDATE scheduled_replies SET state='CANCELLED',outcome_reason='AUTOMATION_DISABLED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND automation_rule_id=$2 AND state IN ('DRAFT','SCHEDULED')`, t.Organisation, id); e != nil {
			return 0, nil, e
		}
		out, e := opsGet[AutomationRule](ctx, tx, "automation_rules", t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		return 200, out, h.opsCommit(ctx, tx, t, property, id, "automation_rule", "DISABLED", rule.State, "DISABLED", businessEvents.AutomationRuleUpdated, out.Version, map[string]any{"reason": in.Reason})
	})
}
