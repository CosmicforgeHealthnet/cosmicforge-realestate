-- +goose Up
-- Sections 12-24 shared controls and Section 12 unified communication inbox.
-- CF Meet remains the human conversation authority: this schema stores
-- business context references, workflow state and provider-permitted metadata
-- only. Native CF Meet message bodies are never stored (CHECK enforced).

-- Generic governed state machine guard. TG_ARGV[0] is a comma separated list of
-- FROM>TO edges. Identity, tenancy and scope are immutable; every update must
-- advance version by exactly one; terminal rows cannot leave terminal states.
-- +goose StatementBegin
CREATE FUNCTION business_ops_state_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE edges text := ','||TG_ARGV[0]||',';
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION '% records are retained', TG_TABLE_NAME USING ERRCODE='23514'; END IF;
 IF NEW.id<>OLD.id OR NEW.organisation_id<>OLD.organisation_id OR NEW.created_at<>OLD.created_at OR (to_jsonb(NEW)->'property_id') IS DISTINCT FROM (to_jsonb(OLD)->'property_id') THEN
  RAISE EXCEPTION '% identity and scope are immutable', TG_TABLE_NAME USING ERRCODE='23514';
 END IF;
 IF NEW.version<>OLD.version+1 THEN RAISE EXCEPTION '% version must advance by one', TG_TABLE_NAME USING ERRCODE='23514'; END IF;
 IF NEW.state<>OLD.state AND position(','||OLD.state||'>'||NEW.state||',' IN edges)=0 THEN
  RAISE EXCEPTION 'Invalid % transition % -> %', TG_TABLE_NAME, OLD.state, NEW.state USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd

-- One append-only history for every Sections 12-24 governed entity. Company
-- scoped finance entities have no property; actor is a human account or the
-- explicit service identity, never both absent.
CREATE TABLE operations_history (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL REFERENCES organisations(id),property_id uuid,entity_type text NOT NULL CHECK(entity_type~'^[a-z_]{3,60}$'),entity_id uuid NOT NULL,
 entity_version bigint NOT NULL CHECK(entity_version>0),action text NOT NULL CHECK(action~'^[A-Z_]{2,80}$'),from_state text NOT NULL DEFAULT '',to_state text NOT NULL DEFAULT '',
 actor_id uuid REFERENCES business_accounts(id),actor_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),
 correlation_id text NOT NULL DEFAULT '',details jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(details)='object' AND octet_length(details::text)<=65536),created_at timestamptz NOT NULL DEFAULT now(),
 sequence bigint GENERATED ALWAYS AS IDENTITY,
 CHECK(actor_id IS NOT NULL OR actor_service<>''),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(entity_type,entity_id,entity_version,action)
);
CREATE INDEX operations_history_entity ON operations_history(organisation_id,entity_type,entity_id,sequence);
CREATE TRIGGER operations_history_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON operations_history FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

-- Provider registry per connection. Credentials are referenced by secret name
-- only; values live in the approved secret mechanism.
CREATE TABLE messaging_connections (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,
 provider text NOT NULL CHECK(provider IN ('CF_MEET','MARKETPLACE','BOOKING_COM','AIRBNB','DIRECT_WEB')),
 authority text NOT NULL CHECK(authority IN ('CF_MEET','EXTERNAL_CHANNEL')),
 state text NOT NULL DEFAULT 'DISCONNECTED' CHECK(state IN ('DISCONNECTED','AUTHORIZING','CONNECTED','DEGRADED','SUSPENDED')),
 supports_reply boolean NOT NULL DEFAULT false,supports_read_state boolean NOT NULL DEFAULT false,supports_preview boolean NOT NULL DEFAULT false,supports_attachments boolean NOT NULL DEFAULT false,
 retention_days integer NOT NULL DEFAULT 30 CHECK(retention_days BETWEEN 0 AND 3650),credential_reference text NOT NULL DEFAULT '' CHECK(credential_reference~'^[a-z0-9/_.-]{0,200}$'),
 provider_account_reference text NOT NULL CHECK(length(provider_account_reference) BETWEEN 1 AND 200),health_reason text NOT NULL DEFAULT '' CHECK(length(health_reason)<=500),
 consecutive_failures integer NOT NULL DEFAULT 0 CHECK(consecutive_failures>=0),last_success_at timestamptz,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK((provider='CF_MEET')=(authority='CF_MEET')),CHECK(authority<>'CF_MEET' OR NOT supports_preview),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,provider,provider_account_reference)
);
CREATE TRIGGER messaging_connections_guard BEFORE UPDATE OR DELETE ON messaging_connections FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('DISCONNECTED>AUTHORIZING,AUTHORIZING>CONNECTED,AUTHORIZING>DISCONNECTED,CONNECTED>DEGRADED,CONNECTED>SUSPENDED,CONNECTED>DISCONNECTED,DEGRADED>CONNECTED,DEGRADED>SUSPENDED,DEGRADED>DISCONNECTED,SUSPENDED>DISCONNECTED,SUSPENDED>CONNECTED');

CREATE TABLE inbox_sync_cursors (
 organisation_id uuid NOT NULL,connection_id uuid NOT NULL,cursor text NOT NULL DEFAULT '' CHECK(length(cursor)<=1000),lease_owner text,lease_expires_at timestamptz,lease_generation bigint NOT NULL DEFAULT 0,
 last_polled_at timestamptz,next_poll_at timestamptz NOT NULL DEFAULT now(),last_error text NOT NULL DEFAULT '',updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organisation_id,connection_id),FOREIGN KEY(organisation_id,connection_id) REFERENCES messaging_connections(organisation_id,id)
);

CREATE TABLE support_cases (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,guest_id uuid,reservation_id uuid,
 category text NOT NULL CHECK(category IN ('BOOKING','PAYMENT','ACCESS','MAINTENANCE','HOUSEKEEPING','COMPLAINT','INFORMATION','OTHER')),
 severity text NOT NULL CHECK(severity IN ('LOW','MEDIUM','HIGH','CRITICAL')),
 state text NOT NULL DEFAULT 'OPEN' CHECK(state IN ('OPEN','ASSIGNED','IN_PROGRESS','PENDING_EXTERNAL','RESOLVED','CLOSED','CANCELLED')),
 summary text NOT NULL CHECK(length(summary) BETWEEN 5 AND 500),resolution text NOT NULL DEFAULT '' CHECK(length(resolution)<=2000),assigned_to uuid REFERENCES business_accounts(id),
 sla_due_at timestamptz NOT NULL,sla_escalated_at timestamptz,resolved_at timestamptz,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid REFERENCES business_accounts(id),
 created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(created_by IS NOT NULL OR created_by_service<>''),CHECK(state NOT IN ('RESOLVED','CLOSED') OR resolved_at IS NOT NULL),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),
 FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE INDEX support_cases_queue ON support_cases(organisation_id,property_id,state,sla_due_at,id);
CREATE TRIGGER support_cases_guard BEFORE UPDATE OR DELETE ON support_cases FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('OPEN>ASSIGNED,OPEN>IN_PROGRESS,OPEN>CANCELLED,ASSIGNED>ASSIGNED,ASSIGNED>IN_PROGRESS,ASSIGNED>PENDING_EXTERNAL,ASSIGNED>RESOLVED,ASSIGNED>CANCELLED,IN_PROGRESS>PENDING_EXTERNAL,IN_PROGRESS>RESOLVED,IN_PROGRESS>ASSIGNED,PENDING_EXTERNAL>IN_PROGRESS,PENDING_EXTERNAL>RESOLVED,RESOLVED>CLOSED,RESOLVED>IN_PROGRESS');

-- Business conversation context references. external_conversation_ref is the
-- CF Meet conversation identifier or the provider thread reference.
CREATE TABLE communication_contexts (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,connection_id uuid NOT NULL,
 authority text NOT NULL CHECK(authority IN ('CF_MEET','EXTERNAL_CHANNEL')),
 state text NOT NULL DEFAULT 'PENDING_AUTHORITY' CHECK(state IN ('PENDING_AUTHORITY','ACTIVE','REVOKED','CLOSED')),
 external_conversation_ref text CHECK(external_conversation_ref IS NULL OR length(external_conversation_ref) BETWEEN 1 AND 300),deep_link_ref text CHECK(deep_link_ref IS NULL OR length(deep_link_ref)<=500),
 guest_id uuid,reservation_id uuid,support_case_id uuid,subject text NOT NULL CHECK(length(subject) BETWEEN 1 AND 200),
 blocked_reason text NOT NULL DEFAULT '' CHECK(length(blocked_reason)<=200),attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),next_attempt_at timestamptz NOT NULL DEFAULT now(),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(created_by IS NOT NULL OR created_by_service<>''),CHECK(state<>'ACTIVE' OR external_conversation_ref IS NOT NULL),
 FOREIGN KEY(organisation_id,connection_id) REFERENCES messaging_connections(organisation_id,id),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),
 FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,property_id,support_case_id) REFERENCES support_cases(organisation_id,property_id,id),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(connection_id,external_conversation_ref)
);
CREATE TRIGGER communication_contexts_guard BEFORE UPDATE OR DELETE ON communication_contexts FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('PENDING_AUTHORITY>ACTIVE,PENDING_AUTHORITY>CLOSED,ACTIVE>REVOKED,ACTIVE>CLOSED,REVOKED>CLOSED');

CREATE TABLE inbox_sla_policies (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,priority text NOT NULL CHECK(priority IN ('LOW','NORMAL','HIGH','URGENT')),
 first_response_seconds integer NOT NULL CHECK(first_response_seconds BETWEEN 60 AND 604800),resolution_seconds integer NOT NULL CHECK(resolution_seconds BETWEEN 300 AND 2592000),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE')),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(resolution_seconds>=first_response_seconds),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,property_id,priority),UNIQUE(organisation_id,id)
);
CREATE TRIGGER inbox_sla_policies_guard BEFORE UPDATE OR DELETE ON inbox_sla_policies FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('');

-- Operational projection only: no message bodies.
CREATE TABLE inbox_threads (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,context_id uuid NOT NULL,
 priority text NOT NULL DEFAULT 'NORMAL' CHECK(priority IN ('LOW','NORMAL','HIGH','URGENT')),
 state text NOT NULL DEFAULT 'OPEN' CHECK(state IN ('OPEN','PENDING_GUEST','RESOLVED','CLOSED')),
 assigned_to uuid REFERENCES business_accounts(id),read_state text NOT NULL DEFAULT 'UNAVAILABLE' CHECK(read_state IN ('UNREAD','READ','UNAVAILABLE')),
 last_inbound_at timestamptz,last_outbound_at timestamptz,first_response_due_at timestamptz,first_responded_at timestamptz,resolution_due_at timestamptz,resolved_at timestamptz,
 sla_breached boolean NOT NULL DEFAULT false,sla_escalated_at timestamptz,inbound_count integer NOT NULL DEFAULT 0 CHECK(inbound_count>=0),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,context_id) REFERENCES communication_contexts(organisation_id,property_id,id),UNIQUE(context_id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE INDEX inbox_threads_queue ON inbox_threads(organisation_id,property_id,state,first_response_due_at,id);
CREATE TRIGGER inbox_threads_guard BEFORE UPDATE OR DELETE ON inbox_threads FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('OPEN>PENDING_GUEST,OPEN>RESOLVED,PENDING_GUEST>OPEN,PENDING_GUEST>RESOLVED,RESOLVED>OPEN,RESOLVED>CLOSED,CLOSED>OPEN');

CREATE TABLE inbox_assignments (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,thread_id uuid NOT NULL,thread_version bigint NOT NULL,
 previous_assignee uuid REFERENCES business_accounts(id),assignee uuid REFERENCES business_accounts(id),reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 1000),
 actor_id uuid REFERENCES business_accounts(id),actor_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(actor_id IS NOT NULL OR actor_service<>''),FOREIGN KEY(organisation_id,property_id,thread_id) REFERENCES inbox_threads(organisation_id,property_id,id),UNIQUE(thread_id,thread_version)
);
CREATE TRIGGER inbox_assignments_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON inbox_assignments FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

-- Technical receipts deduplicated by provider message identity. Preview is
-- permitted only for external channels whose connection declares it, and it
-- expires with the provider retention window.
CREATE TABLE inbox_message_receipts (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,thread_id uuid NOT NULL,connection_id uuid NOT NULL,
 authority text NOT NULL CHECK(authority IN ('CF_MEET','EXTERNAL_CHANNEL')),direction text NOT NULL CHECK(direction IN ('INBOUND','OUTBOUND')),
 provider_message_id text NOT NULL CHECK(length(provider_message_id) BETWEEN 1 AND 300),source_at timestamptz NOT NULL,content_digest text NOT NULL CHECK(content_digest~'^[a-f0-9]{64}$'),
 preview text CHECK(preview IS NULL OR length(preview)<=280),preview_expires_at timestamptz,attachment_refs jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(attachment_refs)='array' AND jsonb_array_length(attachment_refs)<=20),
 read_state text NOT NULL DEFAULT 'UNAVAILABLE' CHECK(read_state IN ('UNREAD','READ','UNAVAILABLE')),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(authority<>'CF_MEET' OR preview IS NULL),CHECK((preview IS NULL)=(preview_expires_at IS NULL)),
 FOREIGN KEY(organisation_id,property_id,thread_id) REFERENCES inbox_threads(organisation_id,property_id,id),FOREIGN KEY(organisation_id,connection_id) REFERENCES messaging_connections(organisation_id,id),
 UNIQUE(connection_id,provider_message_id)
);
CREATE INDEX inbox_receipts_thread ON inbox_message_receipts(organisation_id,thread_id,source_at,id);

CREATE TABLE quick_reply_templates (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,name text NOT NULL CHECK(length(name) BETWEEN 1 AND 120),
 body text NOT NULL CHECK(length(body) BETWEEN 1 AND 2000),variables text[] NOT NULL DEFAULT '{}' CHECK(cardinality(variables)<=10 AND variables<@ARRAY['guest_name','property_name','check_in_time','check_out_time','reservation_reference']::text[]),
 state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','RETIRED')),version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TRIGGER quick_reply_templates_guard BEFORE UPDATE OR DELETE ON quick_reply_templates FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('ACTIVE>RETIRED');

-- Business-owned reply commands. The body is the business's outbound command,
-- not a copy of native conversation history; the provider owns delivery.
CREATE TABLE scheduled_replies (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,thread_id uuid NOT NULL,template_id uuid,suggestion_id uuid,
 body text NOT NULL CHECK(length(body) BETWEEN 1 AND 4000),ai_attributed boolean NOT NULL DEFAULT false,send_at timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'SCHEDULED' CHECK(state IN ('DRAFT','SCHEDULED','DISPATCHING','SENT','UNKNOWN_PENDING_READBACK','FAILED','CANCELLED','BLOCKED')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 20),max_attempts integer NOT NULL DEFAULT 6 CHECK(max_attempts BETWEEN 1 AND 20),next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_owner text,lease_expires_at timestamptz,lease_generation bigint NOT NULL DEFAULT 0,provider_message_id text CHECK(provider_message_id IS NULL OR length(provider_message_id) BETWEEN 1 AND 300),
 outcome_reason text NOT NULL DEFAULT '' CHECK(length(outcome_reason)<=200),sent_at timestamptz,
 created_by uuid REFERENCES business_accounts(id),created_by_member uuid,created_by_role uuid,created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(created_by IS NOT NULL AND created_by_member IS NOT NULL AND created_by_role IS NOT NULL OR created_by IS NULL AND created_by_service<>''),
 CHECK(state<>'SENT' OR provider_message_id IS NOT NULL AND sent_at IS NOT NULL),
 FOREIGN KEY(organisation_id,property_id,thread_id) REFERENCES inbox_threads(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,template_id) REFERENCES quick_reply_templates(organisation_id,property_id,id),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE INDEX scheduled_replies_due ON scheduled_replies(state,next_attempt_at,id) WHERE state IN ('SCHEDULED','DISPATCHING','UNKNOWN_PENDING_READBACK');
CREATE TRIGGER scheduled_replies_guard BEFORE UPDATE OR DELETE ON scheduled_replies FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('DRAFT>SCHEDULED,DRAFT>CANCELLED,SCHEDULED>DISPATCHING,SCHEDULED>CANCELLED,SCHEDULED>BLOCKED,DISPATCHING>SENT,DISPATCHING>UNKNOWN_PENDING_READBACK,DISPATCHING>FAILED,DISPATCHING>SCHEDULED,DISPATCHING>BLOCKED,UNKNOWN_PENDING_READBACK>DISPATCHING,UNKNOWN_PENDING_READBACK>SENT,UNKNOWN_PENDING_READBACK>SCHEDULED,UNKNOWN_PENDING_READBACK>FAILED');
CREATE TABLE scheduled_reply_attempts (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,reply_id uuid NOT NULL,attempt integer NOT NULL CHECK(attempt>0),lease_generation bigint NOT NULL,
 kind text NOT NULL CHECK(kind IN ('SEND','READBACK')),outcome text NOT NULL CHECK(outcome IN ('ACCEPTED','AMBIGUOUS','REJECTED','NOT_FOUND','BLOCKED')),provider_message_id text,detail text NOT NULL DEFAULT '' CHECK(length(detail)<=500),
 created_at timestamptz NOT NULL DEFAULT now(),FOREIGN KEY(organisation_id,reply_id) REFERENCES scheduled_replies(organisation_id,id),UNIQUE(reply_id,attempt,kind)
);
CREATE TRIGGER scheduled_reply_attempts_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON scheduled_reply_attempts FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

-- Approved grounding content for factual AI answers. Only operator approved or
-- canonical-source entries may ground an answer; each carries provenance.
CREATE TABLE ai_knowledge_entries (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,
 topic text NOT NULL CHECK(topic IN ('CHECK_IN','CHECK_OUT','PARKING','WIFI','BREAKFAST','PETS','SMOKING','ACCESSIBILITY','HOUSE_RULES','CANCELLATION_POLICY','LOCATION','FACILITIES','CONTACT')),
 answer text NOT NULL CHECK(length(answer) BETWEEN 5 AND 1000),source_kind text NOT NULL CHECK(source_kind IN ('LISTING_VERSION','PROPERTY_POLICY','RESERVATION_POLICY','OPERATOR_APPROVED')),
 source_reference text NOT NULL CHECK(length(source_reference) BETWEEN 3 AND 300),source_version bigint NOT NULL CHECK(source_version>0),
 state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','RETIRED')),expires_at timestamptz NOT NULL,approved_by uuid NOT NULL REFERENCES business_accounts(id),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE UNIQUE INDEX ai_knowledge_one_active ON ai_knowledge_entries(organisation_id,property_id,topic) WHERE state='ACTIVE';
CREATE TRIGGER ai_knowledge_entries_guard BEFORE UPDATE OR DELETE ON ai_knowledge_entries FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('ACTIVE>RETIRED');

CREATE TABLE ai_suggestions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,thread_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('FACTUAL_REPLY','ESCALATION')),question_digest text NOT NULL CHECK(question_digest~'^[a-f0-9]{64}$'),
 state text NOT NULL CHECK(state IN ('PROPOSED','ACCEPTED','REJECTED','ESCALATED','EXPIRED')),engine text NOT NULL CHECK(length(engine) BETWEEN 3 AND 100),model_version text NOT NULL CHECK(length(model_version) BETWEEN 1 AND 100),
 topic text,answer text CHECK(answer IS NULL OR length(answer)<=2000),citations jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(citations)='array'),confidence_bps integer NOT NULL CHECK(confidence_bps BETWEEN 0 AND 10000),
 refusal_reason text NOT NULL DEFAULT '' CHECK(length(refusal_reason)<=200),injection_detected boolean NOT NULL DEFAULT false,input_references jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(input_references)='object'),
 expires_at timestamptz NOT NULL,decided_by uuid REFERENCES business_accounts(id),decided_at timestamptz,version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(created_by IS NOT NULL OR created_by_service<>''),CHECK(kind<>'FACTUAL_REPLY' OR answer IS NOT NULL AND jsonb_array_length(citations)>0),
 FOREIGN KEY(organisation_id,property_id,thread_id) REFERENCES inbox_threads(organisation_id,property_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TRIGGER ai_suggestions_guard BEFORE UPDATE OR DELETE ON ai_suggestions FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('PROPOSED>ACCEPTED,PROPOSED>REJECTED,PROPOSED>ESCALATED,PROPOSED>EXPIRED');
ALTER TABLE scheduled_replies ADD FOREIGN KEY(organisation_id,property_id,suggestion_id) REFERENCES ai_suggestions(organisation_id,property_id,id);

-- AI or rule suggested actions. A card never executes: acceptance hands the
-- operator to the owning use case, which enforces its own capability,
-- MFA, approval and current-version controls.
CREATE TABLE action_cards (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,thread_id uuid,suggestion_id uuid,
 action_type text NOT NULL CHECK(action_type IN ('RESERVATION_AMENDMENT','CANCELLATION_REVIEW','MAINTENANCE_REQUEST','REFUND_REVIEW','ACCESS_ISSUE','RATE_REVIEW','HOUSEKEEPING_ESCALATION','SUPPORT_CASE','STAFF_FOLLOW_UP')),
 target_type text NOT NULL CHECK(target_type IN ('RESERVATION','ROOM','PROPERTY','THREAD','RATE_PLAN','SUPPORT_CASE')),target_id uuid NOT NULL,target_version bigint,
 parameters jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(parameters)='object' AND octet_length(parameters::text)<=8192),explanation text NOT NULL CHECK(length(explanation) BETWEEN 5 AND 1000),
 risk_level text NOT NULL CHECK(risk_level IN ('LOW','MEDIUM','HIGH')),required_capability text NOT NULL,required_approval_action text NOT NULL DEFAULT '',owning_route text NOT NULL,
 source text NOT NULL CHECK(source IN ('AI_SUGGESTION','AUTOMATION_RULE','OPERATOR')),
 state text NOT NULL DEFAULT 'PROPOSED' CHECK(state IN ('PROPOSED','ACCEPTED','DISMISSED','EXPIRED','EXECUTED')),expires_at timestamptz NOT NULL,
 execution_reference uuid,decided_by uuid REFERENCES business_accounts(id),decided_at timestamptz,version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(created_by IS NOT NULL OR created_by_service<>''),CHECK(state<>'EXECUTED' OR execution_reference IS NOT NULL),
 FOREIGN KEY(organisation_id,property_id,thread_id) REFERENCES inbox_threads(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,suggestion_id) REFERENCES ai_suggestions(organisation_id,property_id,id),
 UNIQUE(organisation_id,id)
);
CREATE TRIGGER action_cards_guard BEFORE UPDATE OR DELETE ON action_cards FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('PROPOSED>ACCEPTED,PROPOSED>DISMISSED,PROPOSED>EXPIRED,ACCEPTED>EXECUTED,ACCEPTED>EXPIRED,ACCEPTED>DISMISSED');

CREATE TABLE automation_rules (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('AI_FACTUAL_AUTOREPLY','INBOX_SLA_ESCALATION','ACTION_CARD_SUGGESTION','QUALITY_ESCALATION','LEAD_FOLLOW_UP')),
 state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','ENABLED','DISABLED')),config jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(config)='object' AND octet_length(config::text)<=8192),
 sponsor_account_id uuid REFERENCES business_accounts(id),sponsor_membership_id uuid,sponsor_role_id uuid,approval_id uuid,reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 1000),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(state<>'ENABLED' OR sponsor_membership_id IS NOT NULL),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE UNIQUE INDEX automation_rules_one_live ON automation_rules(organisation_id,property_id,kind) WHERE state IN ('DRAFT','ENABLED');
CREATE TRIGGER automation_rules_guard BEFORE UPDATE OR DELETE ON automation_rules FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('DRAFT>ENABLED,DRAFT>DISABLED,ENABLED>DISABLED,DISABLED>ENABLED');
CREATE TABLE automation_runs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,rule_id uuid NOT NULL,subject_type text NOT NULL,subject_id uuid NOT NULL,
 outcome text NOT NULL CHECK(outcome IN ('APPLIED','SKIPPED','BLOCKED')),detail text NOT NULL DEFAULT '' CHECK(length(detail)<=500),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,rule_id) REFERENCES automation_rules(organisation_id,id),UNIQUE(rule_id,subject_type,subject_id)
);
ALTER TABLE scheduled_replies ADD COLUMN automation_rule_id uuid;
ALTER TABLE scheduled_replies ADD FOREIGN KEY(organisation_id,automation_rule_id) REFERENCES automation_rules(organisation_id,id);
ALTER TABLE scheduled_replies ADD CONSTRAINT scheduled_reply_service_rule CHECK(created_by IS NOT NULL OR automation_rule_id IS NOT NULL);
CREATE TRIGGER automation_runs_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON automation_runs FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

-- +goose Down
-- Forward only; communication audit and provider receipts are retained.
SELECT 1;
