package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"reflect"
	"strconv"
	"time"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/jackc/pgx/v5"
)

type CommercialSyncJob struct {
	ID                 ids.ID                `json:"id"`
	OrganisationID     ids.ID                `json:"organisation_id"`
	PropertyID         ids.ID                `json:"property_id"`
	ConnectionID       ids.ID                `json:"connection_id"`
	PublicationID      *ids.ID               `json:"publication_id,omitempty"`
	Kind               string                `json:"kind"`
	CanonicalVersion   int64                 `json:"canonical_version"`
	Payload            json.RawMessage       `json:"payload"`
	PayloadHash        string                `json:"payload_hash"`
	IdempotencyKey     string                `json:"idempotency_key"`
	ProviderCommandKey string                `json:"provider_command_key"`
	ActorID            ids.ID                `json:"actor_id"`
	SessionID          *ids.ID               `json:"session_id,omitempty"`
	State              string                `json:"state"`
	Attempts           int                   `json:"attempts"`
	LeaseOwner         string                `json:"lease_owner"`
	LeaseGeneration    int64                 `json:"lease_generation"`
	LeaseExpiresAt     time.Time             `json:"lease_expires_at"`
	CorrelationContext requestcontext.Values `json:"correlation_context"`
}

func (h *Handler) ProcessChannelSync(ctx context.Context, limit int) error {
	if limit < 1 || limit > 20 {
		limit = 20
	}
	owner := ids.New().String()
	for n := 0; n < limit; n++ {
		job, e := typedJSON[CommercialSyncJob](ctx, h.Auth.Service.Pool, `UPDATE channel_sync_jobs j SET state='RUNNING',lease_owner=$1,lease_expires_at=now()+interval '30 seconds',lease_generation=lease_generation+1,attempts=attempts+1,updated_at=now() WHERE id=(SELECT id FROM channel_sync_jobs WHERE (state IN ('QUEUED','RETRY') OR state='RUNNING' AND lease_expires_at<now()) AND available_at<=now() ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING to_jsonb(j)`, owner)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		callCtx := requestcontext.With(ctx, job.CorrelationContext)
		callCtx = requestcontext.WithOrganization(callCtx, job.OrganisationID)
		callCtx = requestcontext.WithRequestID(callCtx, "sync:"+job.ID.String())
		if job.SessionID != nil {
			callCtx = businessauth.StoredJobContext(callCtx, job.ActorID, job.OrganisationID, *job.SessionID, job.ID)
		}
		connection, e := h.prepareChannelJob(callCtx, job)
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e = h.finishChannelFailure(callCtx, job, e, false); e != nil {
				return e
			}
			continue
		}
		adapter := h.ChannelAdapters[connection.ProviderCode]
		if commercialExpectedProviderState(job) == "PUBLISHED" {
			if mediaErr := h.validateDeliveryMedia(callCtx, job); mediaErr != nil {
				if e = h.finishChannelFailure(callCtx, job, mediaErr, false); e != nil {
					return e
				}
				continue
			}
		}
		if adapter == nil {
			if e = h.finishChannelFailure(callCtx, job, distribution.ErrProviderUnavailable, false); e != nil {
				return e
			}
			continue
		}
		commandKey := job.IdempotencyKey
		if job.ProviderCommandKey != "" {
			commandKey = job.ProviderCommandKey
		}
		command := distribution.Command{ContractVersion: "business-supply.v1", JobID: job.ID, Connection: connection.contract(), Kind: job.Kind, CanonicalVersion: job.CanonicalVersion, IdempotencyKey: commandKey, PayloadHash: job.PayloadHash, Payload: job.Payload}
		call, cancel := context.WithTimeout(callCtx, 12*time.Second)
		if job.Attempts > 1 || job.Kind == "RECONCILE" {
			previous, readErr := adapter.Readback(call, command)
			if readErr == nil && distribution.ValidAcknowledgement(previous, command, h.Auth.Service.Now().UTC()) && previous.State == commercialExpectedProviderState(job) {
				cancel()
				if e = h.finishChannelSuccess(callCtx, job, previous); e != nil {
					return e
				}
				continue
			}
			var remote *httpx.RemoteError
			notApplied := errors.As(readErr, &remote) && remote.Status == http.StatusNotFound
			if job.Kind == "RECONCILE" || !notApplied {
				cancel()
				if readErr == nil {
					readErr = distribution.ErrAmbiguous
				}
				if e = h.finishChannelFailure(callCtx, job, readErr, true); e != nil {
					return e
				}
				continue
			}
		}
		var ack distribution.Acknowledgement
		var sendErr error
		if job.Kind != "RECONCILE" {
			ack, sendErr = adapter.Apply(call, command)
		}
		// Even a timeout may have committed remotely. Readback of the same stable
		// provider command precedes any replay; an unverifiable outcome stays unknown.
		readback, readErr := adapter.Readback(call, command)
		cancel()
		now := h.Auth.Service.Now().UTC()
		expected := commercialExpectedProviderState(job)
		validRead := readErr == nil && distribution.ValidAcknowledgement(readback, command, now) && readback.State == expected
		validAck := job.Kind == "RECONCILE" || sendErr == nil && distribution.ValidAcknowledgement(ack, command, now) && ack.State == expected
		if validRead && (validAck || sendErr != nil) {
			if e = h.finishChannelSuccess(callCtx, job, readback); e != nil {
				return e
			}
			continue
		}
		failure := sendErr
		if failure == nil {
			failure = readErr
		}
		if failure == nil {
			failure = distribution.ErrAmbiguous
		}
		if e = h.finishChannelFailure(callCtx, job, failure, true); e != nil {
			return e
		}
	}
	return nil
}
func commercialExpectedProviderState(job CommercialSyncJob) string {
	var value struct {
		Desired string `json:"desired_state"`
	}
	if json.Unmarshal(job.Payload, &value) == nil && value.Desired != "" {
		return value.Desired
	}
	switch job.Kind {
	case "PAUSE":
		return "PAUSED"
	case "WITHDRAW":
		return "WITHDRAWN"
	}
	return "SYNCHRONIZED"
}
func (h *Handler) prepareChannelJob(ctx context.Context, job CommercialSyncJob) (CommercialChannelConnection, error) {
	var connection CommercialChannelConnection
	e := database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, job.OrganisationID); e != nil {
			return e
		}
		if e := h.checkJobFence(ctx, tx, job); e != nil {
			return e
		}
		var e error
		connection, e = h.loadChannelConnection(ctx, tx, job.OrganisationID, job.ConnectionID)
		if e != nil {
			return e
		}
		if hashCommercialPayload(job.Payload) != job.PayloadHash {
			return apperrors.Conflict("SYNC_PAYLOAD_CORRUPT", "Persisted payload hash changed.")
		}
		capability := "ARI"
		switch job.Kind {
		case "CONTENT":
			capability = "CONTENT"
		case "PAUSE":
			capability = "PAUSE"
		case "WITHDRAW":
			capability = "WITHDRAW"
		case "RECONCILE":
			capability = "READBACK"
		}
		stop := job.Kind == "PAUSE" || job.Kind == "WITHDRAW" || job.Kind == "RECONCILE" && commercialExpectedProviderState(job) != "PUBLISHED"
		checked := connection
		if stop && checked.State == "SUSPENDED" {
			checked.State = "CONNECTED"
		}
		if e = h.requireChannelCapability(ctx, tx, checked, capability); e != nil {
			return e
		}
		if e = h.requireChannelCapability(ctx, tx, checked, "READBACK"); e != nil {
			return e
		}
		if job.PublicationID != nil {
			p, e := typedJSON[CommercialPublication](ctx, tx, `SELECT to_jsonb(p) FROM listing_publication p WHERE organisation_id=$1 AND id=$2`, job.OrganisationID, *job.PublicationID)
			if e != nil {
				return e
			}
			if p.ConnectionID != connection.ID || p.CanonicalVersion != job.CanonicalVersion || p.DesiredState != commercialExpectedProviderState(job) {
				return apperrors.Conflict("SYNC_SUPERSEDED", "A newer desired publication supersedes this job.")
			}
		}
		if !stop {
			if e = h.validateQueuedChannelAuthority(ctx, tx, connection, job); e != nil {
				return e
			}
			if job.SessionID == nil {
				return apperrors.PermissionDenied("Original publishing session is no longer available.")
			}
			cap := "publication:write"
			if job.Kind == "ARI" {
				cap = "channels:write"
			}
			t, _, e := h.authorize(ctx, tx, cap, "PROPERTY", job.PropertyID, false)
			if e != nil {
				return e
			}
			if e = h.commercialGate(ctx, tx, t, job.PropertyID); e != nil {
				return e
			}
			var body struct {
				ARI              []CommercialARIDay          `json:"ari"`
				Product          inventory.Product           `json:"product"`
				Pool             inventory.Pool              `json:"pool"`
				Price            revenue.QuoteSnapshot       `json:"price_example"`
				Restrictions     []revenue.RestrictionRecord `json:"restrictions"`
				ListingID        ids.ID                      `json:"listing_id"`
				ListingVersionID ids.ID                      `json:"listing_version_id"`
			}
			if e = json.Unmarshal(job.Payload, &body); e != nil {
				return e
			}
			if !body.ListingID.IsZero() {
				listing, e := latestListingVersion(ctx, tx, job.OrganisationID, body.ListingID)
				if e != nil {
					return e
				}
				property, e := portfolio.Get(ctx, tx, job.OrganisationID, job.PropertyID)
				if e != nil {
					return e
				}
				if listing.ID != body.ListingVersionID || listing.PropertyVersion != property.Version {
					return apperrors.Conflict("SYNC_CANONICAL_VERSION_CHANGED", "Reviewed listing or property changed before delivery.")
				}
			}
			if !body.Product.ID.IsZero() {
				p, e := inventory.GetProduct(ctx, tx, job.OrganisationID, body.Product.ID)
				if e != nil {
					return e
				}
				if p.Version != body.Product.Version || p.PropertyID != job.PropertyID {
					return apperrors.Conflict("SYNC_CANONICAL_VERSION_CHANGED", "Published product changed.")
				}
				if e = h.commercialProductEligibility(ctx, tx, t, p, p.MinGuests, 0, connection.ProviderCode); e != nil {
					return e
				}
				if e = h.requireConfirmedMappings(ctx, tx, connection, p); e != nil {
					return e
				}
				pool, e := inventory.GetPool(ctx, tx, job.OrganisationID, p.PoolID)
				if e != nil {
					return e
				}
				if pool.Version != body.Pool.Version {
					return apperrors.Conflict("SYNC_CANONICAL_VERSION_CHANGED", "Published pool changed.")
				}
				if e = inventory.CheckStockAuthority(ctx, tx, pool); e != nil {
					return e
				}
				if len(body.ARI) > 0 {
					property, e := portfolio.Get(ctx, tx, job.OrganisationID, job.PropertyID)
					if e != nil {
						return e
					}
					at, e := time.Parse(time.DateOnly, body.ARI[len(body.ARI)-1].Day)
					if e != nil {
						return e
					}
					fresh, e := h.currentChannelARI(ctx, tx, t, connection, p, pool, inventory.DateRange{DateFrom: body.ARI[0].Day, DateTo: at.AddDate(0, 0, 1).Format(time.DateOnly)}, property.Timezone)
					if e != nil {
						return e
					}
					if !reflect.DeepEqual(fresh, body.ARI) {
						return apperrors.Conflict("SYNC_CANONICAL_VERSION_CHANGED", "Canonical availability, rate, fiscal or restriction controls changed after queuing.")
					}
					records, e := revenue.Restrictions(ctx, tx, p, *p.RatePlanID, inventory.DateRange{DateFrom: body.ARI[0].Day, DateTo: at.AddDate(0, 0, 1).Format(time.DateOnly)}, connection.ProviderCode)
					if e != nil {
						return e
					}
					if !reflect.DeepEqual(records, body.Restrictions) {
						return apperrors.Conflict("SYNC_CANONICAL_VERSION_CHANGED", "Canonical checkout or stay restrictions changed before delivery.")
					}
				}
				for _, day := range body.ARI {
					var calendarVersion int64
					if e = tx.QueryRow(ctx, `SELECT version FROM inventory_calendar WHERE organisation_id=$1 AND pool_id=$2 AND day=$3::date`, job.OrganisationID, pool.ID, day.Day).Scan(&calendarVersion); e != nil {
						return e
					}
					if calendarVersion != day.CalendarVersion || p.RatePlanID == nil {
						return apperrors.Conflict("SYNC_CANONICAL_VERSION_CHANGED", "Availability changed after this canonical ARI snapshot was queued.")
					}
					rate, e := revenue.VersionForDay(ctx, tx, job.OrganisationID, *p.RatePlanID, day.Day)
					if e != nil {
						return e
					}
					if rate.ID != day.RateVersionID {
						return apperrors.Conflict("SYNC_CANONICAL_VERSION_CHANGED", "A queued ARI rate version is no longer effective.")
					}
				}
				if !body.Price.RatePlanID.IsZero() {
					plan, e := revenue.GetPlan(ctx, tx, job.OrganisationID, body.Price.RatePlanID)
					if e != nil {
						return e
					}
					if plan.State != "ACTIVE" || plan.Version != body.Price.RatePlanVersion {
						return apperrors.Conflict("SYNC_CANONICAL_VERSION_CHANGED", "Published price plan changed.")
					}
				}
			}
		}
		var rate int
		if e = tx.QueryRow(ctx, `SELECT rate_limit_per_minute FROM channel_providers WHERE id=$1`, connection.ProviderID).Scan(&rate); e != nil {
			return e
		}
		var next time.Time
		var providerOwner *string
		var providerExpiry *time.Time
		e = tx.QueryRow(ctx, `SELECT next_at,lease_owner,lease_expires_at FROM channel_provider_dispatch WHERE provider_id=$1 FOR UPDATE`, connection.ProviderID).Scan(&next, &providerOwner, &providerExpiry)
		now := h.Auth.Service.Now().UTC()
		if errors.Is(e, pgx.ErrNoRows) {
			next = now
		} else if e != nil {
			return e
		}
		if next.After(now) || providerOwner != nil && providerExpiry != nil && now.Before(*providerExpiry) && *providerOwner != providerLeaseKey(job) {
			return apperrors.Conflict("CHANNEL_RATE_LIMIT", "Provider dispatch quota is temporarily occupied.")
		}
		interval := time.Minute / time.Duration(rate)
		next = now.Add(3 * interval)
		_, e = tx.Exec(ctx, `INSERT INTO channel_provider_dispatch(provider_id,next_at,lease_owner,lease_expires_at) VALUES($1,$2,$3,$4) ON CONFLICT(provider_id) DO UPDATE SET next_at=excluded.next_at,lease_owner=excluded.lease_owner,lease_expires_at=excluded.lease_expires_at`, connection.ProviderID, next, providerLeaseKey(job), now.Add(20*time.Second))
		return e
	})
	return connection, e
}
func (h *Handler) checkJobFence(ctx context.Context, tx pgx.Tx, job CommercialSyncJob) error {
	var valid bool
	e := tx.QueryRow(ctx, `SELECT state='RUNNING' AND lease_owner=$3 AND lease_generation=$4 AND lease_expires_at>now() FROM channel_sync_jobs WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, job.OrganisationID, job.ID, job.LeaseOwner, job.LeaseGeneration).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return apperrors.Conflict("SYNC_LEASE_LOST", "Another worker owns this delivery generation.")
	}
	return nil
}
func (h *Handler) finishChannelSuccess(ctx context.Context, job CommercialSyncJob, ack distribution.Acknowledgement) error {
	return database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, job.OrganisationID); e != nil {
			return e
		}
		if e := h.checkJobFence(ctx, tx, job); e != nil {
			return e
		}
		now := h.Auth.Service.Now().UTC()
		if e := h.releaseProviderLease(ctx, tx, job); e != nil {
			return e
		}
		if e := h.recordSyncObservations(ctx, tx, job, now); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE channel_sync_jobs SET state='SUCCEEDED',provider_request_id=$3,acknowledged_version=$4,readback_version=$4,last_success_at=$5,lease_owner=NULL,lease_expires_at=NULL,updated_at=now() WHERE organisation_id=$1 AND id=$2`, job.OrganisationID, job.ID, ack.ProviderRequestID, ack.CanonicalVersion, now); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE channel_sync_cursors SET last_published_version=greatest(last_published_version,$3),last_acknowledged_version=greatest(last_acknowledged_version,$3),readback_version=greatest(readback_version,$3),last_success_at=$4,version=version+1 WHERE organisation_id=$1 AND connection_id=$2`, job.OrganisationID, job.ConnectionID, ack.CanonicalVersion, now); e != nil {
			return e
		}
		if job.PublicationID != nil {
			var listing ids.ID
			tag, e := tx.Exec(ctx, `UPDATE listing_publication SET delivery_state='CONFIRMED',acknowledged_version=$3,readback_version=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND canonical_version=$3 AND desired_state=$4`, job.OrganisationID, *job.PublicationID, job.CanonicalVersion, ack.State)
			if e != nil {
				return e
			}
			if tag.RowsAffected() == 1 {
				if e = tx.QueryRow(ctx, `SELECT listing_id FROM listing_publication WHERE organisation_id=$1 AND id=$2`, job.OrganisationID, *job.PublicationID).Scan(&listing); e != nil {
					return e
				}
				event := ""
				operation := "CHANNEL_PUBLICATION_CONFIRMED"
				if ack.State == "PUBLISHED" {
					event = businessEvents.ListingPublished
					operation = "LISTING_PUBLISHED"
					if _, e = tx.Exec(ctx, `UPDATE listings SET state='PUBLISHED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND state='APPROVED'`, job.OrganisationID, listing); e != nil {
						return e
					}
				}
				if ack.State == "WITHDRAWN" {
					event = businessEvents.ListingWithdrawn
					operation = "LISTING_WITHDRAWN"
				}
				if e = businessEvents.CommercialCommit(ctx, tx, h.Auth.Service.Outbox, job.OrganisationID, job.PropertyID, listing, job.ActorID, "listing", operation, event, job.CanonicalVersion, map[string]any{"connection_id": job.ConnectionID, "publication_id": job.PublicationID, "provider_request_id": ack.ProviderRequestID, "canonical_version": ack.CanonicalVersion, "provider_state": ack.State, "delivery_state": "CONFIRMED"}); e != nil {
					return e
				}
			}
		}
		// A verified readback can close only conflicts tied to this exact job or
		// superseded publication, never unrelated reservation or mapping conflicts.
		_, e := tx.Exec(ctx, `UPDATE channel_conflicts SET state='RESOLVED',resolved_at=$3,last_seen_at=$3,version=version+1 WHERE organisation_id=$1 AND connection_id=$2 AND (job_id=$4 OR ($5='RECONCILE' AND job_id IN (SELECT id FROM channel_sync_jobs WHERE organisation_id=$1 AND connection_id=$2 AND publication_id=$6 AND canonical_version=$7 AND payload_hash=$8))) AND state IN ('OPEN','RETRY')`, job.OrganisationID, job.ConnectionID, now, job.ID, job.Kind, job.PublicationID, job.CanonicalVersion, job.PayloadHash)
		return e
	})
}
func commercialRetryDelay(attempt int) time.Duration {
	base := time.Second * time.Duration(1<<min(max(attempt, 1), 10))
	return base + time.Duration(rand.Int64N(int64(base/4+1)))
}
func (h *Handler) finishChannelFailure(ctx context.Context, job CommercialSyncJob, cause error, unknown bool) error {
	code := commercialErrorCode(cause)
	state := "RETRY"
	var remote *httpx.RemoteError
	if errors.As(cause, &remote) {
		if remote.Status == http.StatusUnauthorized || remote.Status == http.StatusForbidden {
			code = "CHANNEL_AUTHORIZATION_REVOKED"
			state = "QUARANTINED"
		} else if remote.Status >= 400 && remote.Status < 500 && remote.Status != http.StatusTooManyRequests && remote.Status != http.StatusRequestTimeout {
			code = "PROVIDER_CONTRACT_REJECTED"
			state = "QUARANTINED"
		} else if remote.Status == http.StatusTooManyRequests {
			code = "PROVIDER_RATE_LIMITED"
		}
	}
	if code == "SYNC_LEASE_LOST" {
		return nil
	}
	if code == "SYNC_SUPERSEDED" {
		state = "QUARANTINED"
	}
	if code == "PERMISSION_DENIED" || code == "SYNC_PAYLOAD_CORRUPT" || code == "SYNC_CANONICAL_VERSION_CHANGED" || code == "PHYSICAL_OCCUPANCY_CHANGED" {
		state = "QUARANTINED"
	}
	if job.Attempts >= 12 {
		state = "QUARANTINED"
	}
	if errors.Is(cause, distribution.ErrAmbiguous) {
		code = "PROVIDER_OUTCOME_UNKNOWN"
	}
	if errors.Is(cause, distribution.ErrProviderUnavailable) {
		code = "PROVIDER_UNCONFIGURED"
	}
	delay := commercialRetryDelay(job.Attempts)
	if code == "CHANNEL_RATE_LIMIT" {
		state = "RETRY"
		delay = time.Second
	}
	return database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, job.OrganisationID); e != nil {
			return e
		}
		if e := h.checkJobFence(ctx, tx, job); e != nil {
			if commercialErrorCode(e) == "SYNC_LEASE_LOST" {
				return nil
			}
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE channel_sync_jobs SET state=$3,last_error=$4,available_at=$5,lease_owner=NULL,lease_expires_at=NULL,updated_at=now() WHERE organisation_id=$1 AND id=$2`, job.OrganisationID, job.ID, state, code, h.Auth.Service.Now().UTC().Add(delay)); e != nil {
			return e
		}
		if e := h.releaseProviderLease(ctx, tx, job); e != nil {
			return e
		}
		if code == "CHANNEL_RATE_LIMIT" {
			_, e := tx.Exec(ctx, `UPDATE channel_sync_jobs SET attempts=greatest(attempts-1,0) WHERE organisation_id=$1 AND id=$2`, job.OrganisationID, job.ID)
			return e
		}
		evidence := map[string]any{"classification": code, "outcome_unknown": unknown, "attempt": job.Attempts, "retry_state": state}
		raw, _ := json.Marshal(evidence)
		if _, e := tx.Exec(ctx, `INSERT INTO channel_sync_failures(id,organisation_id,connection_id,job_id,classification,evidence,attempt) VALUES($1,$2,$3,$4,$5,$6,$7)`, ids.New(), job.OrganisationID, job.ConnectionID, job.ID, code, raw, job.Attempts); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE channel_sync_cursors SET last_failure_at=now(),version=version+1 WHERE organisation_id=$1 AND connection_id=$2`, job.OrganisationID, job.ConnectionID); e != nil {
			return e
		}
		if job.PublicationID != nil && code != "SYNC_SUPERSEDED" {
			delivery := "PENDING"
			if unknown {
				delivery = "UNKNOWN"
			}
			if state == "QUARANTINED" {
				delivery = "QUARANTINED"
			}
			if _, e := tx.Exec(ctx, `UPDATE listing_publication SET delivery_state=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND canonical_version=$4`, job.OrganisationID, *job.PublicationID, delivery, job.CanonicalVersion); e != nil {
				return e
			}
		}
		if code == "SYNC_SUPERSEDED" {
			return nil
		}
		severity := "HIGH"
		if unknown || code == "PERMISSION_DENIED" {
			severity = "CRITICAL"
		}
		if e := h.recordChannelConflict(ctx, tx, job.OrganisationID, job.PropertyID, job.ConnectionID, job.ID, code, severity, "JOB:"+job.ID.String(), evidence, "RECONCILE_AUTHORITATIVE_PROVIDER_STATE"); e != nil {
			return e
		}
		if state == "QUARANTINED" && job.PublicationID != nil && commercialExpectedProviderState(job) == "PUBLISHED" {
			return h.queueSafetyPause(ctx, tx, job, code)
		}
		return nil
	})
}
func providerLeaseKey(job CommercialSyncJob) string {
	return job.ID.String() + ":" + strconv.FormatInt(job.LeaseGeneration, 10)
}
func (h *Handler) releaseProviderLease(ctx context.Context, tx pgx.Tx, job CommercialSyncJob) error {
	_, e := tx.Exec(ctx, `UPDATE channel_provider_dispatch SET lease_owner=NULL,lease_expires_at=NULL WHERE provider_id=(SELECT provider_id FROM channel_connections WHERE organisation_id=$1 AND id=$2) AND lease_owner=$3`, job.OrganisationID, job.ConnectionID, providerLeaseKey(job))
	return e
}
func (h *Handler) queueSafetyPause(ctx context.Context, tx pgx.Tx, job CommercialSyncJob, reason string) error {
	p, e := typedJSON[CommercialPublication](ctx, tx, `SELECT to_jsonb(p) FROM listing_publication p WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, job.OrganisationID, *job.PublicationID)
	if e != nil {
		return e
	}
	if p.DesiredState != "PUBLISHED" {
		return nil
	}
	version := p.CanonicalVersion + 1
	payload := marshalCommercial(map[string]any{"organisation_id": job.OrganisationID, "property_id": job.PropertyID, "listing_id": p.ListingID, "listing_version_id": p.ListingVersionID, "canonical_version": version, "desired_state": "PAUSED", "reason": reason})
	id := ids.New()
	if _, e = tx.Exec(ctx, `UPDATE listing_publication SET desired_state='PAUSED',delivery_state='UNKNOWN',canonical_version=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, job.OrganisationID, p.ID, version); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE listings SET state='PAUSED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND state<>'ARCHIVED'`, job.OrganisationID, p.ListingID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO channel_sync_jobs(id,organisation_id,property_id,connection_id,publication_id,kind,canonical_version,payload,payload_hash,idempotency_key,actor_id,correlation_context) VALUES($1,$2,$3,$4,$5,'PAUSE',$6,$7,$8,$9,$10,$11)`, id, job.OrganisationID, job.PropertyID, job.ConnectionID, p.ID, version, payload, hashCommercialPayload(payload), "safety-pause:"+id.String(), job.ActorID, marshalCommercial(requestcontext.From(ctx))); e != nil {
		return e
	}
	return businessEvents.CommercialCommit(ctx, tx, h.Auth.Service.Outbox, job.OrganisationID, job.PropertyID, p.ListingID, ids.Nil, "listing", "SAFETY_PAUSE_REQUESTED", "", version, map[string]any{"connection_id": job.ConnectionID, "reason": reason, "provider_sellability": "UNKNOWN_UNTIL_CONFIRMED_PAUSE"})
}
