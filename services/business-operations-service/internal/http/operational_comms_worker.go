package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/automation"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/cfmeet"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/messaging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/jackc/pgx/v5"
)

// workerTenant is the explicit service identity for one organisation.
func workerTenant(org ids.ID) tenant { return tenant{Organisation: org} }

// ProcessCommunications runs every Section 12 durable worker once.
func (h *Handler) ProcessCommunications(ctx context.Context, limit int) error {
	return errors.Join(h.ProcessCommunicationContexts(ctx, limit), h.ProcessInboxSync(ctx, limit), h.ProcessScheduledReplies(ctx, limit), h.ProcessInboxSLA(ctx, limit))
}

// ProcessCommunicationContexts asks CF Meet to establish pending contexts.
// Unavailable contracts keep the context pending with an explicit reason.
func (h *Handler) ProcessCommunicationContexts(ctx context.Context, limit int) error {
	now := h.Auth.Service.Now().UTC()
	type item struct {
		org, id ids.ID
		version int64
	}
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT organisation_id,id,version FROM communication_contexts WHERE state='PENDING_AUTHORITY' AND authority='CF_MEET' AND next_attempt_at<=$1 ORDER BY next_attempt_at,id LIMIT $2`, now, limit)
	if e != nil {
		return e
	}
	items := []item{}
	for rows.Next() {
		var i item
		if e = rows.Scan(&i.org, &i.id, &i.version); e != nil {
			rows.Close()
			return e
		}
		items = append(items, i)
	}
	rows.Close()
	var failures []error
	for _, i := range items {
		c, e := opsGet[CommunicationContext](ctx, h.Auth.Service.Pool, "communication_contexts", i.org, i.id)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		property := c.PropertyID
		participants := []ids.ID{}
		if c.CreatedBy != nil {
			participants = append(participants, *c.CreatedBy)
		}
		// Network call outside any database transaction; context id is the
		// CF Meet idempotency key.
		got, callErr := h.cfMeet().EnsureContext(ctx, cfmeet.CFMeetContextRequest{ContextID: c.ID, OrganisationID: i.org, PropertyID: property, ReservationID: c.ReservationID, Subject: c.Subject, Participants: participants})
		e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if e := opsService(ctx, tx); e != nil {
				return e
			}
			cur, e := opsLock[CommunicationContext](ctx, tx, "communication_contexts", i.org, i.id)
			if e != nil || cur.Version != i.version || cur.State != "PENDING_AUTHORITY" {
				return e
			}
			t := workerTenant(i.org)
			switch {
			case callErr == nil && got.ConversationRef != "":
				if _, e = tx.Exec(ctx, `UPDATE communication_contexts SET state='ACTIVE',external_conversation_ref=$2,deep_link_ref=nullif($3,''),blocked_reason='',attempts=attempts+1,version=version+1,updated_at=now() WHERE id=$1`, i.id, got.ConversationRef, got.DeepLinkRef); e != nil {
					return e
				}
				return h.opsCommit(ctx, tx, t, property, i.id, "communication_context", "AUTHORITY_ESTABLISHED", "PENDING_AUTHORITY", "ACTIVE", businessEvents.CommunicationContextUpdated, cur.Version+1, map[string]any{"authority": "CF_MEET"})
			default:
				reason, delay := "CF_MEET_UNAVAILABLE", time.Duration(min(cur.Attempts+1, 8))*2*time.Minute
				if errors.Is(callErr, cfmeet.ErrContractUnavailable) {
					reason, delay = "CF_MEET_CONTRACT_UNAVAILABLE", 15*time.Minute
				} else if errors.Is(callErr, cfmeet.ErrRejected) {
					reason, delay = "CF_MEET_REJECTED", 6*time.Hour
				}
				_, e = tx.Exec(ctx, `UPDATE communication_contexts SET blocked_reason=$2,attempts=attempts+1,next_attempt_at=$3,version=version+1,updated_at=now() WHERE id=$1`, i.id, reason, h.Auth.Service.Now().UTC().Add(delay))
				return e
			}
		})
		if e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}

// ProcessInboxSync polls connected external channels with leased, fenced
// cursors; receipts are deduplicated by provider message identity.
func (h *Handler) ProcessInboxSync(ctx context.Context, limit int) error {
	now := h.Auth.Service.Now().UTC()
	owner := "inbox-sync-" + ids.New().String()
	rows, e := h.Auth.Service.Pool.Query(ctx, `UPDATE inbox_sync_cursors s SET lease_owner=$1,lease_expires_at=$2,lease_generation=lease_generation+1,updated_at=now() FROM messaging_connections m WHERE m.organisation_id=s.organisation_id AND m.id=s.connection_id AND m.state IN ('CONNECTED','DEGRADED') AND m.authority='EXTERNAL_CHANNEL' AND s.next_poll_at<=$3 AND (s.lease_expires_at IS NULL OR s.lease_expires_at<$3) AND (s.organisation_id,s.connection_id) IN (SELECT organisation_id,connection_id FROM inbox_sync_cursors WHERE next_poll_at<=$3 ORDER BY next_poll_at LIMIT $4 FOR UPDATE SKIP LOCKED) RETURNING s.organisation_id,s.connection_id,s.cursor,s.lease_generation`, owner, now.Add(time.Minute), now, limit)
	if e != nil {
		return e
	}
	type lease struct {
		org, conn  ids.ID
		cursor     string
		generation int64
	}
	leases := []lease{}
	for rows.Next() {
		var l lease
		if e = rows.Scan(&l.org, &l.conn, &l.cursor, &l.generation); e != nil {
			rows.Close()
			return e
		}
		leases = append(leases, l)
	}
	rows.Close()
	var failures []error
	for _, l := range leases {
		c, e := opsGet[MessagingConnection](ctx, h.Auth.Service.Pool, "messaging_connections", l.org, l.conn)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		adapter := h.messagingAdapter(c.Provider)
		page, pollErr := adapter.Poll(ctx, c.ProviderAccountReference, l.cursor)
		if e = h.applyInboxPage(ctx, c, adapter.Capabilities(), l.generation, owner, page, pollErr); e != nil {
			failures = append(failures, fmt.Errorf("inbox sync %s: %w", l.conn, e))
		}
	}
	return errors.Join(failures...)
}

func (h *Handler) applyInboxPage(ctx context.Context, c MessagingConnection, caps messaging.MessagingCapabilities, generation int64, owner string, page messaging.MessagingPage, pollErr error) error {
	return database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if e := opsService(ctx, tx); e != nil {
			return e
		}
		var current int64
		var leaseOwner *string
		if e := tx.QueryRow(ctx, `SELECT lease_generation,lease_owner FROM inbox_sync_cursors WHERE organisation_id=$1 AND connection_id=$2 FOR UPDATE`, c.OrganisationID, c.ID).Scan(&current, &leaseOwner); e != nil {
			return e
		}
		if current != generation || leaseOwner == nil || *leaseOwner != owner {
			return nil // fenced: a newer worker owns this cursor
		}
		org := c.OrganisationID
		t := workerTenant(org)
		now := h.Auth.Service.Now().UTC()
		if pollErr != nil {
			failures := c.ConsecutiveFailures + 1
			next := now.Add(time.Duration(min(failures, 10)) * 30 * time.Second)
			if _, e := tx.Exec(ctx, `UPDATE inbox_sync_cursors SET lease_owner=NULL,lease_expires_at=NULL,next_poll_at=$3,last_error=$4,updated_at=now() WHERE organisation_id=$1 AND connection_id=$2`, org, c.ID, next, truncate(pollErr.Error(), 300)); e != nil {
				return e
			}
			state := c.State
			if failures >= 3 && c.State == "CONNECTED" {
				state = "DEGRADED"
			}
			if _, e := tx.Exec(ctx, `UPDATE messaging_connections SET consecutive_failures=$3,state=$4,health_reason=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, org, c.ID, failures, state, "POLL_FAILED"); e != nil {
				return e
			}
			if state != c.State {
				return h.opsCommit(ctx, tx, t, c.PropertyID, c.ID, "messaging_connection", "DEGRADED", c.State, state, businessEvents.MessagingConnectionUpdated, c.Version+1, map[string]any{"failures": failures})
			}
			return nil
		}
		for _, m := range page.Messages {
			if m.ThreadReference == "" || m.ProviderMessageID == "" || len(m.ProviderMessageID) > 300 || len(m.ThreadReference) > 300 || m.Direction != "INBOUND" && m.Direction != "OUTBOUND" {
				continue // malformed provider data never creates business state
			}
			if e := h.applyInboundMessage(ctx, tx, t, c, caps, m, now); e != nil {
				return e
			}
		}
		if _, e := tx.Exec(ctx, `UPDATE inbox_sync_cursors SET cursor=$3,lease_owner=NULL,lease_expires_at=NULL,last_polled_at=$4,next_poll_at=$5,last_error='',updated_at=now() WHERE organisation_id=$1 AND connection_id=$2`, org, c.ID, truncate(page.NextCursor, 1000), now, now.Add(30*time.Second)); e != nil {
			return e
		}
		state := c.State
		if c.State == "DEGRADED" {
			state = "CONNECTED"
		}
		_, e := tx.Exec(ctx, `UPDATE messaging_connections SET consecutive_failures=0,last_success_at=$3,state=$4,health_reason='',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, org, c.ID, now, state)
		return e
	})
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (h *Handler) applyInboundMessage(ctx context.Context, tx pgx.Tx, t tenant, c MessagingConnection, caps messaging.MessagingCapabilities, m messaging.MessagingInbound, now time.Time) error {
	var contextID ids.ID
	e := tx.QueryRow(ctx, `SELECT id FROM communication_contexts WHERE connection_id=$1 AND external_conversation_ref=$2`, c.ID, m.ThreadReference).Scan(&contextID)
	var th InboxThread
	if errors.Is(e, pgx.ErrNoRows) {
		_, th, e = h.createContextThread(ctx, tx, t, c, CommunicationContextCreate{PropertyID: c.PropertyID, ConnectionID: c.ID, Subject: "Provider conversation " + truncate(m.ThreadReference, 150), ExternalConversationRef: m.ThreadReference})
		if e != nil {
			return e
		}
	} else if e != nil {
		return e
	} else {
		var tid ids.ID
		if e = tx.QueryRow(ctx, `SELECT id FROM inbox_threads WHERE organisation_id=$1 AND context_id=$2`, t.Organisation, contextID).Scan(&tid); e != nil {
			return e
		}
		if th, e = opsLock[InboxThread](ctx, tx, "inbox_threads", t.Organisation, tid); e != nil {
			return e
		}
	}
	var preview, previewExpiry any
	if m.Text != "" && c.SupportsPreview && caps.Preview && c.RetentionDays > 0 {
		preview, previewExpiry = truncate(m.Text, 280), now.Add(time.Duration(c.RetentionDays)*24*time.Hour)
	}
	read := "UNAVAILABLE"
	if c.SupportsReadState && caps.ReadState && (m.ReadState == "READ" || m.ReadState == "UNREAD") {
		read = m.ReadState
	}
	attachments := m.AttachmentRefs
	if !c.SupportsAttachments || attachments == nil {
		attachments = []string{}
	}
	if len(attachments) > 20 {
		attachments = attachments[:20]
	}
	receipt := ids.New()
	tag, e := tx.Exec(ctx, `INSERT INTO inbox_message_receipts(id,organisation_id,property_id,thread_id,connection_id,authority,direction,provider_message_id,source_at,content_digest,preview,preview_expires_at,attachment_refs,read_state) VALUES($1,$2,$3,$4,$5,'EXTERNAL_CHANNEL',$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT (connection_id,provider_message_id) DO NOTHING`, receipt, t.Organisation, c.PropertyID, th.ID, c.ID, m.Direction, m.ProviderMessageID, m.SourceAt.UTC(), security.SHA256Hex([]byte(m.Text)), preview, previewExpiry, marshalCommercial(attachments), read)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 || m.Direction != "INBOUND" {
		return nil // duplicate delivery or echo: no second effect
	}
	first, resolution, e := inboxSLA(ctx, tx, t.Organisation, c.PropertyID, th.Priority)
	if e != nil {
		return e
	}
	src := m.SourceAt.UTC()
	state := th.State
	if state != "OPEN" {
		state = "OPEN"
	}
	threadRead := th.ReadState
	if read != "UNAVAILABLE" {
		threadRead = "UNREAD"
	}
	if _, e = tx.Exec(ctx, `UPDATE inbox_threads SET state=$3,last_inbound_at=greatest(coalesce(last_inbound_at,$4),$4),inbound_count=inbound_count+1,read_state=$5,first_response_due_at=CASE WHEN first_responded_at IS NULL OR first_responded_at<$4 THEN coalesce(CASE WHEN first_responded_at IS NULL OR first_responded_at<last_inbound_at THEN first_response_due_at END,$6) ELSE first_response_due_at END,resolution_due_at=coalesce(CASE WHEN state IN ('OPEN','PENDING_GUEST') THEN resolution_due_at END,$7),resolved_at=NULL,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, th.ID, state, src, threadRead, src.Add(first), src.Add(resolution)); e != nil {
		return e
	}
	if e = h.opsCommit(ctx, tx, t, c.PropertyID, th.ID, "inbox_thread", "INBOUND_RECEIVED", th.State, state, businessEvents.InboxThreadUpdated, th.Version+1, map[string]any{"receipt_id": receipt, "provider_message_id": m.ProviderMessageID}); e != nil {
		return e
	}
	if m.Text != "" {
		return h.autoReply(ctx, tx, t, th.ID, receipt, m.Text)
	}
	return nil
}

// autoReply applies an enabled, approved AI_FACTUAL_AUTOREPLY rule. It only
// queues clearly attributed grounded answers under the rule; everything else
// escalates to a human. The sponsor's authority is rechecked now and again at
// dispatch.
func (h *Handler) autoReply(ctx context.Context, tx pgx.Tx, t tenant, threadID, receipt ids.ID, text string) error {
	th, e := opsGet[InboxThread](ctx, tx, "inbox_threads", t.Organisation, threadID)
	if e != nil {
		return e
	}
	rule, e := typedJSON[AutomationRule](ctx, tx, `SELECT to_jsonb(x) FROM automation_rules x WHERE organisation_id=$1 AND property_id=$2 AND kind='AI_FACTUAL_AUTOREPLY' AND state='ENABLED'`, t.Organisation, th.PropertyID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `INSERT INTO automation_runs(id,organisation_id,rule_id,subject_type,subject_id,outcome,detail) VALUES($1,$2,$3,'RECEIPT',$4,'SKIPPED','pending') ON CONFLICT DO NOTHING`, ids.New(), t.Organisation, rule.ID, receipt)
	if e != nil || tag.RowsAffected() == 0 {
		return e
	}
	ok, e := h.currentRecordedPMSAuthority(ctx, tx, t.Organisation, th.PropertyID, *rule.SponsorMembershipID, *rule.SponsorRoleID, permissions.Permission("inbox:reply"))
	if e != nil || !ok {
		return e
	}
	entries, facts, cc, e := groundingInputs(ctx, tx, t.Organisation, th)
	if e != nil {
		return e
	}
	a := automation.Ground(text, h.Auth.Service.Now().UTC(), entries, facts)
	threshold := automation.MinimumConfidenceBPS
	var cfg map[string]float64
	_ = json.Unmarshal(rule.Config, &cfg)
	if v := int(cfg["minimum_confidence_bps"]); v > threshold {
		threshold = v
	}
	state := "ESCALATED"
	if a.Kind == "FACTUAL_REPLY" && a.ConfidenceBPS >= threshold {
		state = "ACCEPTED"
	}
	s, _, e := h.recordSuggestion(ctx, tx, t, th, cc, a, text, state, map[string]any{"receipt_id": receipt, "rule_id": rule.ID, "approved_entries": len(entries)})
	if e != nil || state != "ACCEPTED" {
		return e
	}
	if _, _, _, e = replyTarget(ctx, tx, t.Organisation, th.ID); e != nil {
		return nil // context/connection cannot reply: suggestion stays for operators
	}
	reply := ids.New()
	if _, e = tx.Exec(ctx, `INSERT INTO scheduled_replies(id,organisation_id,property_id,thread_id,suggestion_id,automation_rule_id,body,ai_attributed,send_at,next_attempt_at) VALUES($1,$2,$3,$4,$5,$6,$7,true,now(),now())`, reply, t.Organisation, th.PropertyID, th.ID, s.ID, rule.ID, aiAttribution+a.Text); e != nil {
		return e
	}
	return h.opsCommit(ctx, tx, t, th.PropertyID, reply, "scheduled_reply", "CREATED", "", "SCHEDULED", businessEvents.InboxReplyUpdated, 1, map[string]any{"automation_rule_id": rule.ID, "suggestion_id": s.ID, "ai_attributed": true})
}

// ProcessScheduledReplies dispatches due replies at most once: a leased claim
// commits before the provider call; ambiguous outcomes are read back with the
// same client message id before any retry.
func (h *Handler) ProcessScheduledReplies(ctx context.Context, limit int) error {
	now := h.Auth.Service.Now().UTC()
	owner := "reply-dispatch-" + ids.New().String()
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT organisation_id,id FROM scheduled_replies WHERE (state IN ('SCHEDULED','UNKNOWN_PENDING_READBACK') AND next_attempt_at<=$1 AND send_at<=$1) OR (state='DISPATCHING' AND lease_expires_at<$1) ORDER BY next_attempt_at,id LIMIT $2`, now, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, id ids.ID }
	refs := []ref{}
	for rows.Next() {
		var r ref
		if e = rows.Scan(&r.org, &r.id); e != nil {
			rows.Close()
			return e
		}
		refs = append(refs, r)
	}
	rows.Close()
	var failures []error
	for _, r := range refs {
		if e := h.dispatchReply(ctx, r.org, r.id, owner); e != nil {
			failures = append(failures, fmt.Errorf("reply %s: %w", r.id, e))
		}
	}
	return errors.Join(failures...)
}

type replyClaim struct {
	reply    ScheduledReply
	conn     MessagingConnection
	threadRf string
	readback bool
}

func (h *Handler) dispatchReply(ctx context.Context, org, id ids.ID, owner string) error {
	var claim *replyClaim
	t := workerTenant(org)
	e := database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if e := opsService(ctx, tx); e != nil {
			return e
		}
		s, e := opsLock[ScheduledReply](ctx, tx, "scheduled_replies", org, id)
		if e != nil {
			return e
		}
		now := h.Auth.Service.Now().UTC()
		// Backoff is honoured under the row lock, so concurrent dispatchers
		// never retry or read back early.
		claimable := (s.State == "SCHEDULED" || s.State == "UNKNOWN_PENDING_READBACK") && !s.SendAt.After(now) && !s.NextAttemptAt.After(now)
		var expired bool
		if s.State == "DISPATCHING" {
			if e = tx.QueryRow(ctx, `SELECT lease_expires_at<$2 FROM scheduled_replies WHERE id=$1`, id, now).Scan(&expired); e != nil {
				return e
			}
		}
		if !claimable && !expired {
			return nil
		}
		readback := s.State != "SCHEDULED"
		// Current eligibility and authority are rechecked at dispatch time.
		block := ""
		th, cc, c, e := replyTarget(ctx, tx, org, s.ThreadID)
		if e != nil {
			block = "TARGET_NOT_ELIGIBLE"
		} else if s.CreatedBy != nil {
			ok, e := h.currentRecordedPMSAuthority(ctx, tx, org, th.PropertyID, *s.CreatedByMember, *s.CreatedByRole, permissions.Permission("inbox:reply"))
			if e != nil {
				return e
			}
			if !ok {
				block = "AUTHOR_AUTHORITY_REVOKED"
			}
		} else if s.AutomationRuleID != nil {
			rule, e := opsGet[AutomationRule](ctx, tx, "automation_rules", org, *s.AutomationRuleID)
			if e != nil {
				return e
			}
			ok := rule.State == "ENABLED" && rule.SponsorMembershipID != nil
			if ok {
				if ok, e = h.currentRecordedPMSAuthority(ctx, tx, org, th.PropertyID, *rule.SponsorMembershipID, *rule.SponsorRoleID, permissions.Permission("inbox:reply")); e != nil {
					return e
				}
			}
			if !ok {
				block = "AUTOMATION_AUTHORITY_REVOKED"
			}
		}
		if block != "" && !readback {
			if _, e = tx.Exec(ctx, `UPDATE scheduled_replies SET state='BLOCKED',outcome_reason=$2,version=version+1,updated_at=now() WHERE id=$1`, id, block); e != nil {
				return e
			}
			return h.opsCommit(ctx, tx, t, s.PropertyID, id, "scheduled_reply", "BLOCKED", s.State, "BLOCKED", businessEvents.InboxReplyUpdated, s.Version+1, map[string]any{"reason": block})
		}
		if s.State != "DISPATCHING" {
			if _, e = tx.Exec(ctx, `UPDATE scheduled_replies SET state='DISPATCHING',lease_owner=$2,lease_expires_at=$3,lease_generation=lease_generation+1,attempts=attempts+1,version=version+1,updated_at=now() WHERE id=$1`, id, owner, now.Add(2*time.Minute)); e != nil {
				return e
			}
		} else {
			if _, e = tx.Exec(ctx, `UPDATE scheduled_replies SET lease_owner=$2,lease_expires_at=$3,lease_generation=lease_generation+1,version=version+1,updated_at=now() WHERE id=$1`, id, owner, now.Add(2*time.Minute)); e != nil {
				return e
			}
			readback = true // an expired in-flight claim may already have reached the provider
		}
		s, e = opsGet[ScheduledReply](ctx, tx, "scheduled_replies", org, id)
		if e != nil {
			return e
		}
		claim = &replyClaim{reply: s, conn: c, threadRf: deref(cc.ExternalConversationRef), readback: readback}
		return nil
	})
	if e != nil || claim == nil {
		return e
	}
	adapter := h.messagingAdapter(claim.conn.Provider)
	if claim.conn.Authority == "CF_MEET" {
		adapter = messaging.Disabled{Name: "CF_MEET"} // CF Meet send contract is not published
	}
	kind := "SEND"
	var result messaging.MessagingSendResult
	var callErr error
	if claim.readback {
		kind = "READBACK"
		result, callErr = adapter.Readback(ctx, claim.conn.ProviderAccountReference, claim.threadRf, claim.reply.ID.String())
	} else {
		result, callErr = adapter.Send(ctx, messaging.MessagingSendCommand{ConnectionReference: claim.conn.ProviderAccountReference, ThreadReference: claim.threadRf, ClientMessageID: claim.reply.ID.String(), Body: claim.reply.Body, AIAttributed: claim.reply.AIAttributed})
	}
	return database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if e := opsService(ctx, tx); e != nil {
			return e
		}
		s, e := opsLock[ScheduledReply](ctx, tx, "scheduled_replies", org, id)
		if e != nil {
			return e
		}
		if s.State != "DISPATCHING" || s.LeaseGeneration != claim.reply.LeaseGeneration {
			return nil // fenced by a newer claim
		}
		now := h.Auth.Service.Now().UTC()
		outcome, to, reason := "", "", ""
		switch {
		case callErr == nil && result.ProviderMessageID != "":
			outcome, to = "ACCEPTED", "SENT"
		case errors.Is(callErr, messaging.ErrNotFound) && kind == "READBACK":
			outcome, to, reason = "NOT_FOUND", "SCHEDULED", "READBACK_NOT_FOUND_RETRY"
		case callErr != nil && messaging.Definite(callErr):
			outcome, to, reason = "REJECTED", "FAILED", "PROVIDER_REJECTED"
		default:
			outcome, to, reason = "AMBIGUOUS", "UNKNOWN_PENDING_READBACK", "PROVIDER_OUTCOME_UNKNOWN"
		}
		if to == "SCHEDULED" && s.Attempts >= s.MaxAttempts {
			to, reason = "FAILED", "ATTEMPTS_EXHAUSTED"
		}
		detail := ""
		if callErr != nil {
			detail = truncate(callErr.Error(), 400)
		}
		if _, e = tx.Exec(ctx, `INSERT INTO scheduled_reply_attempts(id,organisation_id,reply_id,attempt,lease_generation,kind,outcome,provider_message_id,detail) VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''),$9) ON CONFLICT DO NOTHING`, ids.New(), org, id, max(s.Attempts, 1), s.LeaseGeneration, kind, outcome, result.ProviderMessageID, detail); e != nil {
			return e
		}
		backoff := now.Add(time.Duration(min(s.Attempts, 8)) * time.Minute)
		if _, e = tx.Exec(ctx, `UPDATE scheduled_replies SET state=$2,provider_message_id=coalesce(nullif($3,''),provider_message_id),sent_at=CASE WHEN $2='SENT' THEN $4::timestamptz END,outcome_reason=$5,next_attempt_at=$6,lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=now() WHERE id=$1`, id, to, result.ProviderMessageID, now, reason, backoff); e != nil {
			return e
		}
		if to == "SENT" {
			th, e := opsLock[InboxThread](ctx, tx, "inbox_threads", org, s.ThreadID)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO inbox_message_receipts(id,organisation_id,property_id,thread_id,connection_id,authority,direction,provider_message_id,source_at,content_digest) VALUES($1,$2,$3,$4,$5,$6,'OUTBOUND',$7,$8,$9) ON CONFLICT DO NOTHING`, ids.New(), org, s.PropertyID, s.ThreadID, claim.conn.ID, claim.conn.Authority, result.ProviderMessageID, now, security.SHA256Hex([]byte(s.Body))); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE inbox_threads SET last_outbound_at=$3,first_responded_at=CASE WHEN last_inbound_at IS NOT NULL AND (first_responded_at IS NULL OR first_responded_at<last_inbound_at) THEN $3 ELSE first_responded_at END,state=CASE WHEN state='OPEN' THEN 'PENDING_GUEST' ELSE state END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, org, th.ID, now); e != nil {
				return e
			}
		}
		return h.opsCommit(ctx, tx, t, s.PropertyID, id, "scheduled_reply", "DISPATCH_"+outcome, "DISPATCHING", to, businessEvents.InboxReplyUpdated, s.Version+1, map[string]any{"attempt": s.Attempts, "kind": kind, "provider_acknowledged": to == "SENT", "reason": reason})
	})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ProcessInboxSLA marks first-response breaches, escalates under an enabled
// rule, escalates support cases past SLA and expires stale suggestions/cards.
func (h *Handler) ProcessInboxSLA(ctx context.Context, limit int) error {
	now := h.Auth.Service.Now().UTC()
	return database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if e := opsService(ctx, tx); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT organisation_id,id FROM inbox_threads WHERE state IN ('OPEN','PENDING_GUEST') AND NOT sla_breached AND first_response_due_at<$1 AND (first_responded_at IS NULL OR first_responded_at<last_inbound_at) ORDER BY first_response_due_at LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
		if e != nil {
			return e
		}
		type ref struct{ org, id ids.ID }
		refs := []ref{}
		for rows.Next() {
			var r ref
			if e = rows.Scan(&r.org, &r.id); e != nil {
				rows.Close()
				return e
			}
			refs = append(refs, r)
		}
		rows.Close()
		for _, r := range refs {
			th, e := opsGet[InboxThread](ctx, tx, "inbox_threads", r.org, r.id)
			if e != nil {
				return e
			}
			var escalate bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM automation_rules WHERE organisation_id=$1 AND property_id=$2 AND kind='INBOX_SLA_ESCALATION' AND state='ENABLED')`, r.org, th.PropertyID).Scan(&escalate); e != nil {
				return e
			}
			priority := th.Priority
			if escalate && priority != "URGENT" {
				priority = map[string]string{"LOW": "NORMAL", "NORMAL": "HIGH", "HIGH": "URGENT"}[priority]
			}
			if _, e = tx.Exec(ctx, `UPDATE inbox_threads SET sla_breached=true,sla_escalated_at=CASE WHEN $3<>priority THEN $4::timestamptz ELSE sla_escalated_at END,priority=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, r.org, r.id, priority, now); e != nil {
				return e
			}
			if e = h.opsCommit(ctx, tx, workerTenant(r.org), th.PropertyID, r.id, "inbox_thread", "SLA_BREACHED", th.State, th.State, businessEvents.InboxThreadUpdated, th.Version+1, map[string]any{"first_response_due_at": th.FirstResponseDueAt, "priority": priority, "escalated": priority != th.Priority}); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE support_cases SET sla_escalated_at=$1,version=version+1,updated_at=now() WHERE state NOT IN ('RESOLVED','CLOSED','CANCELLED') AND sla_due_at<$1 AND sla_escalated_at IS NULL`, now); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE ai_suggestions SET state='EXPIRED',version=version+1,updated_at=now() WHERE state='PROPOSED' AND expires_at<=$1`, now); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE action_cards SET state='EXPIRED',version=version+1,updated_at=now() WHERE state IN ('PROPOSED','ACCEPTED') AND expires_at<=$1`, now)
		return e
	})
}
