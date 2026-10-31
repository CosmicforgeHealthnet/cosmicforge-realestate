package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// InternalDistributionRouter never shares the browser's authentication router.
// A signed service identity plus delegated organisation and a connection bound
// to that provider are all required. No cross-service database access occurs.
func (h *Handler) InternalDistributionRouter(verifier *serviceauth.Verifier) http.Handler {
	r := chi.NewRouter()
	r.Use(serviceauth.Authenticate(verifier, serviceauth.HeaderAuthorization))
	r.Group(func(r chi.Router) {
		r.Use(serviceauth.RequireCaller(platform.MarketplaceService))
		r.Post("/channel-receipts", h.internalChannelReceipt)
	})
	r.Group(func(r chi.Router) {
		r.Use(serviceauth.RequireCaller(platform.PaymentService, platform.AdminService))
		r.Post("/channel-financial-inputs", h.internalChannelFinancialInput)
	})
	return r
}

type CommercialReceiptCommand struct {
	OrganisationID ids.ID               `json:"organisation_id"`
	ConnectionID   ids.ID               `json:"connection_id"`
	Receipt        distribution.Receipt `json:"receipt"`
}
type CommercialReceiptResult struct {
	ReceiptID                    ids.ID `json:"receipt_id"`
	State                        string `json:"state"`
	Reason                       string `json:"reason"`
	Duplicate                    bool   `json:"duplicate"`
	CanonicalReservationAccepted bool   `json:"canonical_reservation_accepted"`
}

func (h *Handler) internalChannelReceipt(w http.ResponseWriter, r *http.Request) {
	var in CommercialReceiptCommand
	if !decode(w, r, &in) {
		return
	}
	principal, ok := serviceauth.PrincipalFrom(r.Context())
	if !ok || principal.Service != platform.MarketplaceService || principal.Delegation.OrganizationID != in.OrganisationID {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Signed Marketplace organisation delegation required."))
		return
	}
	c, e := h.loadChannelConnection(r.Context(), h.Auth.Service.Pool, in.OrganisationID, in.ConnectionID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if c.ProviderCode != distribution.Marketplace {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Marketplace cannot forge events for another provider connection."))
		return
	}
	if e = h.requireChannelCapability(r.Context(), h.Auth.Service.Pool, c, "WEBHOOK"); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var result CommercialReceiptResult
	var domainErr error
	e = database.WithTx(r.Context(), h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, in.OrganisationID); e != nil {
			return e
		}
		var failure error
		result, domainErr, failure = h.acceptChannelReceipt(ctx, tx, c, in.Receipt)
		return failure
	})
	if e == nil {
		e = domainErr
	}
	reply(w, r, 202, result, e)
}
func validateChannelReceipt(v distribution.Receipt, now time.Time) error {
	if len(v.ProviderEventID) < 1 || len(v.ProviderEventID) > 200 || len(v.ExternalObjectID) < 1 || len(v.ExternalObjectID) > 200 || len(v.ExternalVersion) < 1 || len(v.ExternalVersion) > 100 || len(v.Reference) > 500 || v.OccurredAt.IsZero() || v.OccurredAt.After(now.Add(time.Minute)) || !slices.Contains([]string{"RESERVATION_CREATED", "RESERVATION_CHANGED", "RESERVATION_CANCELLED", "CONTENT_CHANGED", "ARI_CHANGED", "MAPPING_CHANGED", "CONNECTION_REVOKED"}, v.Kind) {
		return apperrors.InvalidRequest("Valid bounded provider event identity, object/version, kind and timestamp required.")
	}
	if v.Kind == "RESERVATION_CREATED" || v.Kind == "RESERVATION_CHANGED" || v.Kind == "RESERVATION_CANCELLED" {
		if v.ProductID.ValidateV7() != nil || v.Quantity < 1 || v.Quantity > 100000 {
			return apperrors.InvalidRequest("Canonical mapped product and bounded reservation quantity required.")
		}
		if _, e := (inventory.DateRange{DateFrom: v.DateFrom, DateTo: v.DateTo}).Days("UTC"); e != nil {
			return apperrors.InvalidRequest("Valid normalized provider reservation dates required.")
		}
	}
	return nil
}
func (h *Handler) acceptChannelReceipt(ctx context.Context, tx pgx.Tx, c CommercialChannelConnection, event distribution.Receipt) (CommercialReceiptResult, error, error) {
	var result CommercialReceiptResult
	if e := validateChannelReceipt(event, h.Auth.Service.Now().UTC()); e != nil {
		return result, e, nil
	}
	event.OccurredAt = event.OccurredAt.UTC()
	raw, _ := json.Marshal(event)
	semantic := event
	semantic.ProviderEventID = ""
	hash := hashCommercialPayload(marshalCommercial(semantic))
	var id ids.ID
	var storedHash, state, reason string
	e := tx.QueryRow(ctx, `SELECT id,payload_hash,state,reason FROM channel_event_receipts WHERE organisation_id=$1 AND connection_id=$2 AND (id IN (SELECT receipt_id FROM channel_event_delivery_keys WHERE organisation_id=$1 AND connection_id=$2 AND provider_event_id=$3) OR external_object_id=$4 AND external_version=$5) ORDER BY received_at,id LIMIT 1 FOR UPDATE`, c.OrganisationID, c.ID, event.ProviderEventID, event.ExternalObjectID, event.ExternalVersion).Scan(&id, &storedHash, &state, &reason)
	if e == nil {
		if storedHash != hash {
			failure := h.recordChannelConflict(ctx, tx, c.OrganisationID, c.PropertyID, c.ID, ids.Nil, "PROVIDER_EVENT_IDENTITY_COLLISION", "CRITICAL", "RECEIPT:"+id.String(), map[string]any{"receipt_id": id, "incoming_payload_hash": hash}, "INVESTIGATE_PROVIDER_EVENT_IDENTITY_AND_REPLAY")
			return result, apperrors.Conflict("PROVIDER_EVENT_IDENTITY_CONFLICT", "An existing provider event/object version has different immutable content."), failure
		}
		if _, e = tx.Exec(ctx, `INSERT INTO channel_event_delivery_keys(organisation_id,connection_id,provider_event_id,receipt_id,payload_hash) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, c.OrganisationID, c.ID, event.ProviderEventID, id, hash); e != nil {
			return result, nil, e
		}
		_, e = tx.Exec(ctx, `UPDATE channel_event_receipts SET duplicate_count=duplicate_count+1 WHERE organisation_id=$1 AND id=$2`, c.OrganisationID, id)
		return CommercialReceiptResult{ReceiptID: id, State: state, Reason: reason, Duplicate: true}, nil, e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return result, nil, e
	}
	id = ids.New()
	state = "QUARANTINED"
	reason = "PROVIDER_CHANGE_REQUIRES_CANONICAL_RECONCILIATION"
	reservation := slices.Contains([]string{"RESERVATION_CREATED", "RESERVATION_CHANGED", "RESERVATION_CANCELLED"}, event.Kind)
	if reservation {
		reason = "SECTION9_CANONICAL_RESERVATION_AUTHORITY_UNAVAILABLE"
		var exists bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sellable_products WHERE organisation_id=$1 AND id=$2 AND property_id=$3)`, c.OrganisationID, event.ProductID, c.PropertyID).Scan(&exists); e != nil {
			return result, nil, e
		}
		if !exists {
			reason = "PROVIDER_RESERVATION_PRODUCT_UNMAPPED"
		} else {
			p, e := inventory.GetProduct(ctx, tx, c.OrganisationID, event.ProductID)
			if e != nil {
				return result, nil, e
			}
			if e = h.requireConfirmedMappings(ctx, tx, c, p); e != nil {
				var domain *apperrors.Error
				if !errors.As(e, &domain) {
					return result, nil, e
				}
				reason = "PROVIDER_RESERVATION_MAPPING_UNCONFIRMED"
			}
		}
	}
	if _, e = tx.Exec(ctx, `INSERT INTO channel_event_receipts(id,organisation_id,property_id,connection_id,provider_event_id,external_object_id,external_version,payload_hash,normalized,state,reason,processed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())`, id, c.OrganisationID, c.PropertyID, c.ID, event.ProviderEventID, event.ExternalObjectID, event.ExternalVersion, hash, raw, state, reason); e != nil {
		return result, nil, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO channel_event_delivery_keys(organisation_id,connection_id,provider_event_id,receipt_id,payload_hash) VALUES($1,$2,$3,$4,$5)`, c.OrganisationID, c.ID, event.ProviderEventID, id, hash); e != nil {
		return result, nil, e
	}
	if reservation {
		if _, e = tx.Exec(ctx, `INSERT INTO channel_reservation_refs(id,organisation_id,connection_id,receipt_id,external_reservation_id,external_version,state) VALUES($1,$2,$3,$4,$5,$6,'DEPENDENCY_BLOCKED')`, ids.New(), c.OrganisationID, c.ID, id, event.ExternalObjectID, event.ExternalVersion); e != nil {
			return result, nil, e
		}
	}
	if e = h.recordChannelConflict(ctx, tx, c.OrganisationID, c.PropertyID, c.ID, ids.Nil, reason, "CRITICAL", "RECEIPT:"+id.String(), map[string]any{"receipt_id": id, "provider_event_id": event.ProviderEventID, "kind": event.Kind, "external_object_id": event.ExternalObjectID, "external_version": event.ExternalVersion}, "RECONCILE_WITH_OWNING_CANONICAL_SERVICE"); e != nil {
		return result, nil, e
	}
	if reservation || event.Kind == "CONNECTION_REVOKED" {
		pubs, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(p) FROM listing_publication p JOIN listings l ON l.id=p.listing_id AND l.organisation_id=p.organisation_id WHERE p.organisation_id=$1 AND p.connection_id=$2 AND p.desired_state='PUBLISHED' AND ($3::uuid IS NULL OR l.product_id=$3) ORDER BY p.id`, c.OrganisationID, c.ID, nullableCommercialID(func() ids.ID {
			if reason == "PROVIDER_RESERVATION_PRODUCT_UNMAPPED" {
				return ids.Nil
			}
			return event.ProductID
		}()))
		if e != nil {
			return result, nil, e
		}
		for _, raw := range pubs {
			var p CommercialPublication
			if e = json.Unmarshal(raw, &p); e != nil {
				return result, nil, e
			}
			var actor ids.ID
			if e = tx.QueryRow(ctx, `SELECT actor_id FROM channel_sync_jobs WHERE organisation_id=$1 AND publication_id=$2 ORDER BY created_at DESC,id DESC LIMIT 1`, c.OrganisationID, p.ID).Scan(&actor); e != nil {
				return result, nil, e
			}
			if e = h.queueSafetyPause(ctx, tx, CommercialSyncJob{OrganisationID: c.OrganisationID, PropertyID: c.PropertyID, ConnectionID: c.ID, PublicationID: &p.ID, ActorID: actor}, reason); e != nil {
				return result, nil, e
			}
		}
	}
	e = businessEvents.CommercialCommit(ctx, tx, h.Auth.Service.Outbox, c.OrganisationID, c.PropertyID, id, ids.Nil, "channel_receipt", "CHANNEL_RECEIPT_QUARANTINED", "", 1, map[string]any{"connection_id": c.ID, "kind": event.Kind, "reason": reason, "canonical_reservation_accepted": false})
	return CommercialReceiptResult{ReceiptID: id, State: state, Reason: reason}, nil, e
}

type CommercialFinancialInputCommand struct {
	OrganisationID ids.ID                   `json:"organisation_id"`
	ConnectionID   ids.ID                   `json:"connection_id"`
	ProductID      *ids.ID                  `json:"product_id,omitempty"`
	RatePlanID     *ids.ID                  `json:"rate_plan_id,omitempty"`
	PromotionID    *ids.ID                  `json:"promotion_id,omitempty"`
	PeriodFrom     string                   `json:"period_from"`
	PeriodTo       string                   `json:"period_to"`
	Input          CommercialFinancialInput `json:"input"`
}

func (h *Handler) internalChannelFinancialInput(w http.ResponseWriter, r *http.Request) {
	var in CommercialFinancialInputCommand
	if !decode(w, r, &in) {
		return
	}
	pr, ok := serviceauth.PrincipalFrom(r.Context())
	if !ok || pr.Delegation.OrganizationID != in.OrganisationID {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Authoritative service organisation delegation required."))
		return
	}
	allowed := pr.Service == platform.PaymentService && slices.Contains([]string{"GROSS_REVENUE", "PAYMENT_FEE", "COMMISSION"}, in.Input.Component) || pr.Service == platform.AdminService && slices.Contains([]string{"PROMOTION_COST", "OPERATIONAL_COST"}, in.Input.Component)
	if !allowed || in.Input.SourceService != string(pr.Service) {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Financial component must come from its signed owning source service."))
		return
	}
	_, e := commercialMoney(in.Input.AmountMinor, in.Input.Currency)
	from, e2 := time.Parse(time.DateOnly, in.PeriodFrom)
	to, e3 := time.Parse(time.DateOnly, in.PeriodTo)
	now := h.Auth.Service.Now().UTC()
	if e != nil || e2 != nil || e3 != nil || !to.After(from) || to.After(from.AddDate(1, 0, 0)) || in.Input.AmountMinor < 0 || len(in.Input.SourceReference) < 1 || len(in.Input.SourceReference) > 500 || in.Input.ObservedAt.IsZero() || in.Input.ObservedAt.After(now) || !now.Before(in.Input.ExpiresAt) || in.Input.ExpiresAt.After(now.Add(90*24*time.Hour)) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Bounded source-backed exact financial input, currency, reporting period and expiry required."))
		return
	}
	c, e := h.loadChannelConnection(r.Context(), h.Auth.Service.Pool, in.OrganisationID, in.ConnectionID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id := ids.New()
	in.Input.ObservedAt = in.Input.ObservedAt.UTC()
	in.Input.ExpiresAt = in.Input.ExpiresAt.UTC()
	fingerprint := hashCommercialPayload(marshalCommercial(in))
	e = database.WithTx(r.Context(), h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, in.OrganisationID); e != nil {
			return e
		}
		if in.PromotionID != nil {
			var same bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM promotions WHERE organisation_id=$1 AND property_id=$2 AND id=$3)`, in.OrganisationID, c.PropertyID, *in.PromotionID).Scan(&same); e != nil {
				return e
			}
			if !same {
				return apperrors.PermissionDenied("Promotion input scope differs from its connection property.")
			}
		}
		var existing ids.ID
		var storedFingerprint string
		e := tx.QueryRow(ctx, `SELECT id,source_payload_hash FROM channel_financial_inputs WHERE connection_id=$1 AND component=$2 AND source_service=$3 AND source_reference=$4`, c.ID, in.Input.Component, string(pr.Service), in.Input.SourceReference).Scan(&existing, &storedFingerprint)
		if e == nil {
			if storedFingerprint != fingerprint {
				return apperrors.Conflict("FINANCIAL_SOURCE_IDENTITY_CONFLICT", "A source reference cannot be replayed with different financial facts.")
			}
			id = existing
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO channel_financial_inputs(id,organisation_id,property_id,connection_id,product_id,rate_plan_id,promotion_id,component,amount_minor,currency,period_from,period_to,source_service,source_reference,observed_at,expires_at,reconciled,source_payload_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::date,$12::date,$13,$14,$15,$16,$17,$18)`, id, in.OrganisationID, c.PropertyID, c.ID, in.ProductID, in.RatePlanID, in.PromotionID, in.Input.Component, in.Input.AmountMinor, in.Input.Currency, in.PeriodFrom, in.PeriodTo, string(pr.Service), in.Input.SourceReference, in.Input.ObservedAt, in.Input.ExpiresAt, in.Input.Reconciled, fingerprint); e != nil {
			return e
		}
		return businessEvents.CommercialCommit(ctx, tx, h.Auth.Service.Outbox, in.OrganisationID, c.PropertyID, id, ids.Nil, "channel_financial_input", "CHANNEL_FINANCIAL_INPUT_RECORDED", "", 1, map[string]any{"connection_id": c.ID, "component": in.Input.Component, "source_service": string(pr.Service), "source_reference": in.Input.SourceReference, "amount_minor": in.Input.AmountMinor, "currency": in.Input.Currency, "reconciled": in.Input.Reconciled})
	})
	reply(w, r, 201, map[string]any{"input_id": id}, e)
}

// Poll pages are received outside a transaction; receipt dedupe and cursor
// advancement then commit together. Crashes/restarts replay the same page.
func (h *Handler) PollChannel(ctx context.Context, org, id ids.ID) error {
	c, e := h.loadChannelConnection(ctx, h.Auth.Service.Pool, org, id)
	if e != nil {
		return e
	}
	if e = h.requireChannelCapability(ctx, h.Auth.Service.Pool, c, "POLLING"); e != nil {
		return e
	}
	var cursor string
	if e = h.Auth.Service.Pool.QueryRow(ctx, `SELECT poll_cursor FROM channel_sync_cursors WHERE organisation_id=$1 AND connection_id=$2`, org, id).Scan(&cursor); e != nil {
		return e
	}
	page, e := h.ChannelAdapters[c.ProviderCode].Poll(ctx, c.contract(), cursor)
	if e != nil {
		return e
	}
	if page.Cursor != cursor || len(page.NextCursor) > 2000 || len(page.Events) > 100 {
		return apperrors.Conflict("PROVIDER_POLL_PAGE_INVALID", "Provider cursor or bounded event page is invalid.")
	}
	var domainFailure error
	e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, org); e != nil {
			return e
		}
		var actual string
		if e = tx.QueryRow(ctx, `SELECT poll_cursor FROM channel_sync_cursors WHERE organisation_id=$1 AND connection_id=$2 FOR UPDATE`, org, id).Scan(&actual); e != nil {
			return e
		}
		if actual != cursor {
			return apperrors.Conflict("POLL_CURSOR_CHANGED", "Another poll committed this cursor.")
		}
		current, e := h.loadChannelConnection(ctx, tx, org, id)
		if e != nil {
			return e
		}
		if current.Version != c.Version {
			return apperrors.Conflict("VERSION_CONFLICT", "Provider connection changed during polling.")
		}
		for _, event := range page.Events {
			_, domainErr, failure := h.acceptChannelReceipt(ctx, tx, current, event)
			if failure != nil {
				return failure
			}
			if domainErr != nil {
				domainFailure = domainErr
				if failure = h.recordChannelConflict(ctx, tx, org, c.PropertyID, c.ID, ids.Nil, "PROVIDER_POLL_POISON_EVENT", "HIGH", "POLL_CURSOR:"+cursor, map[string]any{"payload_hash": hashCommercialPayload(marshalCommercial(event)), "classification": commercialErrorCode(domainErr)}, "REVIEW_REJECTED_PROVIDER_PAGE_WITHOUT_ADVANCING_CURSOR"); failure != nil {
					return failure
				}
				return nil
			}
		}
		_, e = tx.Exec(ctx, `UPDATE channel_sync_cursors SET poll_cursor=$3,last_poll_at=now(),version=version+1 WHERE organisation_id=$1 AND connection_id=$2`, org, id, page.NextCursor)
		return e
	})
	if e != nil {
		return e
	}
	return domainFailure
}
