// Package audit records append-only audit events (AUDIT_STANDARD.md) into the
// owning service's own audit_events table, inside the same transaction as the
// audited change.
//
// Never place secrets or raw regulated documents in audit payloads: change
// summaries are filtered for sensitive keys, and evidence is referenced by
// stable IDs/hashes only.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// SchemaSQL is the canonical, append-only audit table. UPDATE and DELETE are
// rejected by trigger.
const SchemaSQL = `
CREATE TABLE audit_events (
    audit_id         uuid PRIMARY KEY,
    occurred_at      timestamptz NOT NULL,
    service          text        NOT NULL,
    action           text        NOT NULL,
    actor_type       text        NOT NULL CHECK (actor_type IN ('USER','SERVICE','SYSTEM','PROVIDER')),
    actor_domain     text,
    actor_id         uuid,
    calling_service  text,
    target_type      text        NOT NULL,
    target_id        uuid        NOT NULL,
    request_id       text,
    trace_id         text,
    result           text        NOT NULL CHECK (result IN ('SUCCESS','DENIED','FAILURE')),
    changes          jsonb,
    metadata         jsonb
);
CREATE INDEX audit_events_target_idx ON audit_events (target_type, target_id, occurred_at);
CREATE INDEX audit_events_actor_idx ON audit_events (actor_domain, actor_id, occurred_at) WHERE actor_id IS NOT NULL;

CREATE FUNCTION audit_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only';
END;
$fn$;
CREATE TRIGGER audit_events_no_update BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_append_only();
`

// DropSQL reverses SchemaSQL (for down migrations).
const DropSQL = `
DROP TABLE IF EXISTS audit_events;
DROP FUNCTION IF EXISTS audit_events_append_only();
`

// Actor types.
const (
	ActorUser     = "USER"
	ActorService  = "SERVICE"
	ActorSystem   = "SYSTEM"
	ActorProvider = "PROVIDER"
)

// Results.
const (
	ResultSuccess = "SUCCESS"
	ResultDenied  = "DENIED"
	ResultFailure = "FAILURE"
)

// Event is an audit record.
type Event struct {
	Action     string // e.g. "verification.transition"
	ActorType  string // optional: derived from context when empty
	TargetType string
	TargetID   ids.ID
	Result     string
	// Changes is a safe before/after summary (sensitive keys are dropped).
	Changes map[string]any
	// Metadata holds safe source metadata (decision source, provider ref).
	Metadata map[string]any
}

var actionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// Recorder writes audit events for one service.
type Recorder struct {
	service string
	now     func() time.Time
}

// NewRecorder returns a recorder for service.
func NewRecorder(service string) *Recorder { return &Recorder{service: service, now: time.Now} }

// Record inserts e within tx, stamping actor, calling service, request and
// trace IDs from ctx.
func (r *Recorder) Record(ctx context.Context, tx database.Querier, e Event) (ids.ID, error) {
	if !actionPattern.MatchString(e.Action) || e.TargetType == "" || e.TargetID.IsZero() {
		return ids.Nil, errors.New("audit: action, target_type and target_id are required")
	}
	if e.Result == "" {
		e.Result = ResultSuccess
	}
	rc := requestcontext.From(ctx)
	actorType := e.ActorType
	var actorDomain *string
	var actorID any
	if !rc.Actor.IsZero() {
		actorDomain, actorID = &rc.Actor.Domain, rc.Actor.ID
		if actorType == "" {
			actorType = ActorUser
		}
	}
	if actorType == "" {
		if rc.CallingService != "" {
			actorType = ActorService
		} else {
			actorType = ActorSystem
		}
	}
	id := ids.New()
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_events (audit_id, occurred_at, service, action, actor_type, actor_domain, actor_id,
		    calling_service, target_type, target_id, request_id, trace_id, result, changes, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,nullif($8,''),$9,$10,nullif($11,''),nullif($12,''),$13,$14,$15)`,
		id, r.now().UTC(), r.service, e.Action, actorType, actorDomain, actorID, rc.CallingService,
		e.TargetType, e.TargetID, rc.RequestID, rc.TraceID, e.Result, jsonb(Sanitize(e.Changes)), jsonb(Sanitize(e.Metadata)))
	return id, err
}

// Sanitize removes sensitive keys recursively.
func Sanitize(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if security.IsSensitiveKey(k) {
			continue
		}
		if nested, ok := v.(map[string]any); ok {
			v = Sanitize(nested)
		}
		out[k] = v
	}
	return out
}

func jsonb(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return string(b)
}
