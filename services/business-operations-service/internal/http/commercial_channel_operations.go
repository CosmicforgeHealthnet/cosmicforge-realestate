package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/jackc/pgx/v5"
)

func (h *Handler) channelJobs(w http.ResponseWriter, r *http.Request) {
	h.channelCollection(w, r, "jobs")
}
func (h *Handler) channelReceipts(w http.ResponseWriter, r *http.Request) {
	h.channelCollection(w, r, "receipts")
}
func (h *Handler) channelCommissionList(w http.ResponseWriter, r *http.Request) {
	h.channelCollection(w, r, "commission")
}
func (h *Handler) channelCollection(w http.ResponseWriter, r *http.Request, collection string) {
	t, c, e := h.channelTarget(r, "channels:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	queries := map[string]string{
		"jobs":       `SELECT to_jsonb(j)-'payload'-'session_id'-'actor_id'-'lease_owner' FROM channel_sync_jobs j WHERE organisation_id=$1 AND connection_id=$2 ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`,
		"receipts":   `SELECT to_jsonb(j)-'normalized' FROM channel_event_receipts j WHERE organisation_id=$1 AND connection_id=$2 ORDER BY received_at DESC,id DESC LIMIT $3 OFFSET $4`,
		"commission": `SELECT to_jsonb(j)-'created_by' FROM channel_commission_rules j WHERE organisation_id=$1 AND connection_id=$2 ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`}
	query, ok := queries[collection]
	if !ok {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Unknown channel collection."))
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, query, t.Organisation, c.ID, limit, offset)
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) channelHealth(w http.ResponseWriter, r *http.Request) {
	t, c, e := h.channelTarget(r, "channels:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	view, e := h.channelHealthView(r.Context(), h.Auth.Service.Pool, t.Organisation, c)
	reply(w, r, 200, view, e)
}
func (h *Handler) channelHealthView(ctx context.Context, q database.Querier, org ids.ID, c CommercialChannelConnection) (any, error) {
	cursor, e := oneJSON(ctx, q, `SELECT to_jsonb(s) FROM channel_sync_cursors s WHERE organisation_id=$1 AND connection_id=$2`, org, c.ID)
	if e != nil {
		return nil, e
	}
	var queued, running, retry, failed, succeeded, openConflicts, unknown, confirmed int64
	var oldest *time.Time
	var unmapped, staleMappings int64
	if e = q.QueryRow(ctx, `SELECT (SELECT count(*) FROM sellable_products s WHERE s.organisation_id=$1 AND s.property_id=$2 AND s.state='ACTIVE' AND NOT EXISTS(SELECT 1 FROM channel_product_mappings m WHERE m.organisation_id=s.organisation_id AND m.connection_id=$3 AND m.product_id=s.id AND m.state='CONFIRMED')),(SELECT count(*) FROM channel_product_mappings m JOIN sellable_products s ON s.organisation_id=m.organisation_id AND s.id=m.product_id WHERE m.organisation_id=$1 AND m.connection_id=$3 AND (m.state<>'CONFIRMED' OR m.validated_at<s.updated_at))`, org, c.PropertyID, c.ID).Scan(&unmapped, &staleMappings); e != nil {
		return nil, e
	}
	var lastSuccess, lastFailure *time.Time
	e = q.QueryRow(ctx, `SELECT count(*) FILTER(WHERE state='QUEUED'),count(*) FILTER(WHERE state='RUNNING'),count(*) FILTER(WHERE state='RETRY'),count(*) FILTER(WHERE state IN ('QUARANTINED','FAILED_FINAL')),count(*) FILTER(WHERE state='SUCCEEDED'),min(created_at) FILTER(WHERE state IN ('QUEUED','RETRY')),max(last_success_at),max(updated_at) FILTER(WHERE state IN ('RETRY','QUARANTINED','FAILED_FINAL')) FROM channel_sync_jobs WHERE organisation_id=$1 AND connection_id=$2`, org, c.ID).Scan(&queued, &running, &retry, &failed, &succeeded, &oldest, &lastSuccess, &lastFailure)
	if e != nil {
		return nil, e
	}
	if e = q.QueryRow(ctx, `SELECT count(*) FROM channel_conflicts WHERE organisation_id=$1 AND connection_id=$2 AND state IN ('OPEN','RETRY')`, org, c.ID).Scan(&openConflicts); e != nil {
		return nil, e
	}
	if e = q.QueryRow(ctx, `SELECT count(*) FILTER(WHERE delivery_state<>'CONFIRMED'),count(*) FILTER(WHERE delivery_state='CONFIRMED') FROM listing_publication WHERE organisation_id=$1 AND connection_id=$2`, org, c.ID).Scan(&unknown, &confirmed); e != nil {
		return nil, e
	}
	status := "AVAILABLE_UNCONFIGURED"
	if c.State == "CONNECTED" {
		status = "AWAITING_FIRST_CONFIRMED_DELIVERY"
		if lastSuccess != nil {
			status = "HEALTHY"
		}
		if retry+openConflicts+unknown+staleMappings > 0 || c.CredentialExpiresAt != nil && !h.Auth.Service.Now().Before(*c.CredentialExpiresAt) || lastSuccess != nil && h.Auth.Service.Now().Sub(*lastSuccess) > 15*time.Minute {
			status = "DEGRADED"
		}
	} else if c.State == "SUSPENDED" {
		status = "SUSPENDED"
	}
	var lag *int64
	if oldest != nil {
		v := max(int64(0), int64(h.Auth.Service.Now().Sub(*oldest).Seconds()))
		lag = &v
	}
	return map[string]any{"connection": c, "status": status, "cursor": cursor, "unmapped_products": unmapped, "stale_product_mappings": staleMappings, "queued_jobs": queued, "running_jobs": running, "retry_jobs": retry, "failed_or_quarantined_jobs": failed, "succeeded_jobs": succeeded, "open_conflicts": openConflicts, "unconfirmed_publications": unknown, "confirmed_publications": confirmed, "backlog_lag_seconds": lag, "last_success_at": lastSuccess, "last_failure_at": lastFailure, "as_of": h.Auth.Service.Now().UTC(), "provider_sellability": "PER_PUBLICATION_CONFIRMED_READBACK_ONLY"}, nil
}
func (h *Handler) channelResync(w http.ResponseWriter, r *http.Request) {
	var in CommercialStateChange
	if !decode(w, r, &in) {
		return
	}
	_, c, e := h.channelTarget(r, "channels:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Resynchronization reason required."))
		return
	}
	h.execute(w, r, "commercial:channel:resync:"+c.ID.String(), in, "channels:write", "PROPERTY", c.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "channels:write", "PROPERTY", c.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := h.loadChannelConnection(ctx, tx, t.Organisation, c.ID)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Connection version changed.")
		}
		if e = h.requireChannelCapability(ctx, tx, current, "READBACK"); e != nil {
			return 0, nil, e
		}
		jobs, e := h.queueChannelReconciliation(ctx, tx, t, current, in.Reason)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, c.PropertyID, c.ID, "channel_connection", "CHANNEL_RECONCILIATION_QUEUED", "", current.Version, map[string]any{"jobs": jobs, "reason": in.Reason})
		return 202, map[string]any{"job_ids": jobs, "state": "QUEUED"}, e
	})
}
func (h *Handler) queueChannelReconciliation(ctx context.Context, tx pgx.Tx, t tenant, c CommercialChannelConnection, reason string) ([]ids.ID, error) {
	publications, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(p) FROM listing_publication p WHERE organisation_id=$1 AND connection_id=$2 ORDER BY id LIMIT 101`, t.Organisation, c.ID)
	if e != nil {
		return nil, e
	}
	if len(publications) > 100 {
		return nil, apperrors.Conflict("RECONCILIATION_BATCH_LIMIT", "Reconcile no more than 100 current publications per command.")
	}
	jobs := []ids.ID{}
	principal, _ := businessauth.PrincipalFrom(ctx)
	for _, raw := range publications {
		var p CommercialPublication
		if e = json.Unmarshal(raw, &p); e != nil {
			return nil, e
		}
		var payload []byte
		var hash, providerKey string
		e = tx.QueryRow(ctx, `SELECT payload,payload_hash,coalesce(nullif(provider_command_key,''),idempotency_key) FROM channel_sync_jobs WHERE organisation_id=$1 AND publication_id=$2 AND canonical_version=$3 AND kind IN ('CONTENT','ARI','PAUSE','WITHDRAW') ORDER BY created_at DESC,id DESC LIMIT 1`, t.Organisation, p.ID, p.CanonicalVersion).Scan(&payload, &hash, &providerKey)
		if e != nil {
			return nil, e
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO channel_sync_jobs(id,organisation_id,property_id,connection_id,publication_id,kind,canonical_version,payload,payload_hash,idempotency_key,actor_id,session_id,provider_command_key,correlation_context) VALUES($1,$2,$3,$4,$5,'RECONCILE',$6,$7,$8,$9,$10,$11,$12,$13)`, id, t.Organisation, c.PropertyID, c.ID, p.ID, p.CanonicalVersion, payload, hash, "reconcile:"+id.String(), t.Account, nullableCommercialID(principal.SessionID), providerKey, marshalCommercial(requestcontext.From(ctx))); e != nil {
			return nil, e
		}
		jobs = append(jobs, id)
	}
	return jobs, nil
}

type CommercialPublication struct {
	ID                  ids.ID `json:"id"`
	ListingID           ids.ID `json:"listing_id"`
	ListingVersionID    ids.ID `json:"listing_version_id"`
	ConnectionID        ids.ID `json:"connection_id"`
	DesiredState        string `json:"desired_state"`
	DeliveryState       string `json:"delivery_state"`
	CanonicalVersion    int64  `json:"canonical_version"`
	AcknowledgedVersion *int64 `json:"acknowledged_version,omitempty"`
	ReadbackVersion     *int64 `json:"readback_version,omitempty"`
	Version             int64  `json:"version"`
}
type CommercialChannelConflict struct {
	SourceObject     string          `json:"source_object"`
	CanonicalVersion *int64          `json:"canonical_version,omitempty"`
	ProviderVersion  *string         `json:"provider_version,omitempty"`
	FirstSeenAt      time.Time       `json:"first_seen_at"`
	LastSeenAt       time.Time       `json:"last_seen_at"`
	ID               ids.ID          `json:"id"`
	OrganisationID   ids.ID          `json:"organisation_id"`
	PropertyID       ids.ID          `json:"property_id"`
	ConnectionID     ids.ID          `json:"connection_id"`
	JobID            *ids.ID         `json:"job_id,omitempty"`
	Classification   string          `json:"classification"`
	Severity         string          `json:"severity"`
	State            string          `json:"state"`
	RequiredAction   string          `json:"required_action"`
	Evidence         json.RawMessage `json:"evidence"`
	Version          int64           `json:"version"`
}

func (h *Handler) channelConflictGet(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	v, e := typedJSON[CommercialChannelConflict](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(c) FROM channel_conflicts c WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if _, e = h.commercialScope(r, "channels:read", v.PropertyID, false); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	c, e := h.loadChannelConnection(r.Context(), h.Auth.Service.Pool, t.Organisation, v.ConnectionID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	history, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(d) FROM channel_conflict_decisions d WHERE organisation_id=$1 AND conflict_id=$2 ORDER BY created_at,id LIMIT $3 OFFSET $4`, t.Organisation, id, limit, offset)
	reply(w, r, 200, map[string]any{"conflict": v, "provider_code": c.ProviderCode, "history": history, "limit": limit, "offset": offset}, e)
}

func (h *Handler) recordChannelConflict(ctx context.Context, tx pgx.Tx, org, property, connection, job ids.ID, classification, severity, source string, evidence any, required string) error {
	raw, e := json.Marshal(evidence)
	if e != nil {
		return e
	}
	var existing ids.ID
	var version int64
	e = tx.QueryRow(ctx, `SELECT id,version FROM channel_conflicts WHERE organisation_id=$1 AND connection_id=$2 AND classification=$3 AND source_object=$4 AND state IN ('OPEN','RETRY') ORDER BY first_seen_at LIMIT 1 FOR UPDATE`, org, connection, classification, source).Scan(&existing, &version)
	if errors.Is(e, pgx.ErrNoRows) {
		existing = ids.New()
		version = 1
		_, e = tx.Exec(ctx, `INSERT INTO channel_conflicts(id,organisation_id,property_id,connection_id,job_id,classification,severity,source_object,evidence,required_action) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, existing, org, property, connection, nullableCommercialID(job), classification, severity, source, raw, required)
	} else if e == nil {
		version++
		_, e = tx.Exec(ctx, `UPDATE channel_conflicts SET last_seen_at=now(),evidence=$3,version=version+1 WHERE organisation_id=$1 AND id=$2`, org, existing, raw)
	}
	if e != nil {
		return e
	}
	return businessEvents.CommercialCommit(ctx, tx, h.Auth.Service.Outbox, org, property, existing, ids.Nil, "channel_conflict", "CHANNEL_CONFLICT_RECORDED", businessEvents.ChannelSyncFailed, version, map[string]any{"connection_id": connection, "job_id": nullableCommercialID(job), "classification": classification, "severity": severity, "required_action": required})
}
func (h *Handler) channelConflicts(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "channel_conflicts", "channels:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		return typedJSON[CommercialChannelConflict](ctx, h.Auth.Service.Pool, `SELECT to_jsonb(c) FROM channel_conflicts c WHERE organisation_id=$1 AND id=$2`, org, id)
	})
}

type CommercialConflictDecision struct {
	Version    int64  `json:"version"`
	Action     string `json:"action"`
	Reason     string `json:"reason"`
	ApprovalID ids.ID `json:"approval_id"`
}

func (h *Handler) channelConflictResolve(w http.ResponseWriter, r *http.Request) {
	var in CommercialConflictDecision
	if !decode(w, r, &in) {
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	conflict, e := typedJSON[CommercialChannelConflict](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(c) FROM channel_conflicts c WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Action != "RECONCILE" && in.Action != "REJECT" && in.Action != "PAUSE" || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Reconcile or reject with a reason. Provider sellability cannot be manually declared resolved."))
		return
	}
	h.execute(w, r, "commercial:channel:conflict:"+id.String(), in, "channels:resolve", "PROPERTY", conflict.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "channels:resolve", "PROPERTY", conflict.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := typedJSON[CommercialChannelConflict](ctx, tx, `SELECT to_jsonb(c) FROM channel_conflicts c WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.Version || current.State == "RESOLVED" || current.State == "REJECTED" {
			return 0, nil, apperrors.Conflict("CONFLICT_VERSION_CHANGED", "Conflict was already decided or changed.")
		}
		c, e := h.loadChannelConnection(ctx, tx, t.Organisation, current.ConnectionID)
		if e != nil {
			return 0, nil, e
		}
		command := in
		command.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_CHANNEL_CONFLICT_RESOLUTION", target.Scope, in.ApprovalID, map[string]any{"conflict_id": id, "command": command}, c.ProviderCode, nil, ""); e != nil {
			return 0, nil, e
		}
		state := "REJECTED"
		jobs := []ids.ID{}
		if in.Action == "RECONCILE" {
			if e = h.requireChannelCapability(ctx, tx, c, "READBACK"); e != nil {
				return 0, nil, e
			}
			jobs, e = h.queueChannelReconciliation(ctx, tx, t, c, in.Reason)
			if e != nil {
				return 0, nil, e
			}
			state = "RETRY"
		}
		if in.Action == "PAUSE" {
			pubs, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(p) FROM listing_publication p WHERE organisation_id=$1 AND connection_id=$2 AND desired_state='PUBLISHED' ORDER BY id`, t.Organisation, c.ID)
			if e != nil {
				return 0, nil, e
			}
			for _, raw := range pubs {
				var p CommercialPublication
				if e = json.Unmarshal(raw, &p); e != nil {
					return 0, nil, e
				}
				if e = h.queueSafetyPause(ctx, tx, CommercialSyncJob{OrganisationID: t.Organisation, PropertyID: c.PropertyID, ConnectionID: c.ID, PublicationID: &p.ID, ActorID: t.Account}, in.Reason); e != nil {
					return 0, nil, e
				}
			}
			state = "RETRY"
		}
		if _, e = tx.Exec(ctx, `INSERT INTO channel_conflict_decisions(id,organisation_id,conflict_id,action,reason,actor_id) VALUES($1,$2,$3,$4,$5,$6)`, ids.New(), t.Organisation, id, in.Action, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE channel_conflicts SET state=$3,version=version+1,last_seen_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, state); e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, c.PropertyID, id, "channel_conflict", "CHANNEL_CONFLICT_DECIDED", "", current.Version+1, map[string]any{"action": in.Action, "state": state, "reason": in.Reason, "job_ids": jobs})
		return 202, map[string]any{"conflict_id": id, "state": state, "version": current.Version + 1, "job_ids": jobs}, e
	})
}

type CommercialCommissionCommand struct {
	Currency        string     `json:"currency"`
	CommissionBPS   int        `json:"commission_bps"`
	FixedMinor      int64      `json:"fixed_minor"`
	EffectiveAt     time.Time  `json:"effective_at"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	SourceReference string     `json:"source_reference"`
	Reason          string     `json:"reason"`
	ApprovalID      ids.ID     `json:"approval_id"`
}

func (h *Handler) channelCommissionCreate(w http.ResponseWriter, r *http.Request) {
	var in CommercialCommissionCommand
	if !decode(w, r, &in) {
		return
	}
	_, c, e := h.channelTarget(r, "channels:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if _, e = commercialMoney(in.FixedMinor, in.Currency); e != nil || in.FixedMinor < 0 || in.CommissionBPS < 0 || in.CommissionBPS > 10000 || in.EffectiveAt.IsZero() || in.ExpiresAt != nil && !in.ExpiresAt.After(in.EffectiveAt) || len(in.SourceReference) < 1 || len(in.SourceReference) > 500 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Explicit commission amount/basis points, currency, effective window, contractual source and reason required."))
		return
	}
	h.execute(w, r, "commercial:channel:commission:"+c.ID.String(), in, "channels:write", "PROPERTY", c.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "channels:write", "PROPERTY", c.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		command := in
		command.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_CHANNEL_CHANGE", target.Scope, in.ApprovalID, map[string]any{"connection_id": c.ID, "commission": command}, c.ProviderCode, &in.FixedMinor, in.Currency); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		var version int64
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0)+1 FROM channel_commission_rules WHERE organisation_id=$1 AND connection_id=$2 AND currency=$3`, t.Organisation, c.ID, in.Currency).Scan(&version); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO channel_commission_rules(id,organisation_id,connection_id,currency,commission_bps,fixed_minor,effective_at,expires_at,source_reference,version,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, t.Organisation, c.ID, in.Currency, in.CommissionBPS, in.FixedMinor, in.EffectiveAt, in.ExpiresAt, in.SourceReference, version, t.Account); e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, c.PropertyID, id, "channel_commission_rule", "CHANNEL_COMMISSION_VERSION_CREATED", businessEvents.ChannelConnectionChanged, version, map[string]any{"connection_id": c.ID, "commission": command, "version": version})
		return 201, map[string]any{"id": id, "version": version, "basis": "CONTRACTUAL_ESTIMATE_RECONCILED_ACTUALS_REQUIRED"}, e
	})
}
func hashCommercialPayload(payload []byte) string {
	hash, _ := distribution.PayloadHash(payload)
	return hash
}

// Periodic reconciliation reads back the original durable provider command.
// It never sends a second mutation and cannot manufacture a provider success.
func (h *Handler) queuePeriodicChannelReadback(ctx context.Context, tx pgx.Tx, t tenant, c CommercialChannelConnection, p CommercialPublication) error {
	var last *time.Time
	var active bool
	if e := tx.QueryRow(ctx, `SELECT max(last_success_at),coalesce(bool_or(state IN ('QUEUED','RUNNING','RETRY')),false) FROM channel_sync_jobs WHERE organisation_id=$1 AND publication_id=$2 AND canonical_version=$3`, t.Organisation, p.ID, p.CanonicalVersion).Scan(&last, &active); e != nil {
		return e
	}
	if active || last != nil && h.Auth.Service.Now().Sub(*last) < 5*time.Minute {
		return nil
	}
	var source CommercialSyncJob
	source, e := typedJSON[CommercialSyncJob](ctx, tx, `SELECT to_jsonb(j) FROM channel_sync_jobs j WHERE organisation_id=$1 AND publication_id=$2 AND canonical_version=$3 AND kind IN ('CONTENT','ARI') ORDER BY created_at DESC,id DESC LIMIT 1`, t.Organisation, p.ID, p.CanonicalVersion)
	if e != nil {
		return e
	}
	key := source.IdempotencyKey
	if source.ProviderCommandKey != "" {
		key = source.ProviderCommandKey
	}
	id := ids.New()
	if _, e = tx.Exec(ctx, `INSERT INTO channel_sync_jobs(id,organisation_id,property_id,connection_id,publication_id,kind,canonical_version,payload,payload_hash,idempotency_key,actor_id,session_id,provider_command_key,correlation_context) VALUES($1,$2,$3,$4,$5,'RECONCILE',$6,$7,$8,$9,$10,$11,$12,$13)`, id, t.Organisation, c.PropertyID, c.ID, p.ID, p.CanonicalVersion, source.Payload, source.PayloadHash, "periodic-readback:"+id.String(), source.ActorID, source.SessionID, key, marshalCommercial(source.CorrelationContext)); e != nil {
		return e
	}
	return businessEvents.CommercialCommit(ctx, tx, h.Auth.Service.Outbox, t.Organisation, c.PropertyID, id, t.Account, "channel_sync_job", "CHANNEL_PERIODIC_READBACK_QUEUED", "", 1, map[string]any{"publication_id": p.ID, "connection_id": c.ID})
}
