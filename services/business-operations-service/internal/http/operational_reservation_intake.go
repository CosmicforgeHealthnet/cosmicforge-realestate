package businesshttp

import (
	"context"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/jackc/pgx/v5"
	"net/http"
)

// acceptReservationReceipt is invoked only after authenticated callback/poll
// validation and under the existing tenant fence. Its caller uses a savepoint:
// a failed booking must retain the durable receipt/conflict without any partial
// stock, price, reservation, approval or outbox effects.
func (h *Handler) acceptReservationReceipt(ctx context.Context, tx pgx.Tx, c CommercialChannelConnection, event distribution.Receipt, receipt ids.ID, hash string) (reservations.Acceptance, error) {
	var out reservations.Acceptance
	cap := "RESERVATION_INGEST"
	if event.Kind != "RESERVATION_CREATED" {
		cap = "RESERVATION_CHANGE"
	}
	if e := h.requireChannelCapability(ctx, tx, c, cap); e != nil {
		return out, e
	}
	sourceService := string(platform.BusinessOperationsService)
	if p, ok := serviceauth.PrincipalFrom(ctx); ok && p.Service == platform.MarketplaceService {
		sourceService = string(p.Service)
	}
	if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service',$1,true)`, sourceService); e != nil {
		return out, e
	}
	t := tenant{Organisation: c.OrganisationID}
	if e := tx.QueryRow(ctx, `SELECT state FROM organisations WHERE id=$1`, c.OrganisationID).Scan(&t.State); e != nil {
		return out, e
	}
	var existing ids.ID
	e := tx.QueryRow(ctx, `SELECT s.reservation_id FROM reservation_source_refs s JOIN channel_event_receipts receipt ON receipt.id=s.receipt_id AND receipt.organisation_id=s.organisation_id WHERE s.organisation_id=$1 AND s.provider=$2 AND s.external_reservation_id=$3 AND receipt.connection_id=$4 ORDER BY s.created_at,s.id LIMIT 1`, c.OrganisationID, c.ProviderCode, event.ExternalObjectID, c.ID).Scan(&existing)
	found := e == nil
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	var value reservations.Reservation
	switch event.Kind {
	case "RESERVATION_CREATED":
		if found {
			return out, apperrors.Conflict("SOURCE_RESERVATION_ALREADY_EXISTS", "This provider booking already has a canonical reservation; submit a versioned change instead.")
		}
		if event.Booking == nil {
			return out, apperrors.InvalidRequest("Complete normalized canonical reservation acceptance details are required.")
		}
		in := *event.Booking
		expectedSource := "OTA"
		if c.ProviderCode == distribution.Marketplace {
			expectedSource = "MARKETPLACE"
		}
		if in.GroupID != nil || in.CorporateID != nil || in.CorporateContractID != nil {
			return out, apperrors.PermissionDenied("Externally supplied canonical group/corporate relationships require a separate verified source mapping.")
		}
		if in.Source != expectedSource || in.Channel != c.ProviderCode || in.PropertyID != c.PropertyID || in.SourceReference != event.ExternalObjectID || in.PaymentResponsibility != "PLATFORM_COLLECT" || in.State != "HOLD" && in.State != "PENDING_PAYMENT" && in.State != "PENDING_APPROVAL" || len(in.Rooms) == 0 || in.Rooms[0].ProductID != event.ProductID {
			return out, apperrors.PermissionDenied("Provider identity, property, channel, source booking and pending financial responsibility must match the authenticated connection.")
		}
		for _, guest := range in.Guests {
			if guest.SourceKind != expectedSource || guest.GuestID != nil {
				return out, apperrors.PermissionDenied("Source intake accepts only its own typed guest references; canonical guest linking requires its separate verified identity contract.")
			}
		}
		from, to := "", ""
		quantity := 0
		for _, room := range in.Rooms {
			p, e := inventory.GetProduct(ctx, tx, t.Organisation, room.ProductID)
			if e != nil {
				return out, e
			}
			if e = h.requireConfirmedMappings(ctx, tx, c, p); e != nil {
				return out, e
			}
			if from == "" || room.DateFrom < from {
				from = room.DateFrom
			}
			if room.DateTo > to {
				to = room.DateTo
			}
			quantity += room.Quantity
		}
		if from != event.DateFrom || to != event.DateTo || quantity != event.Quantity {
			return out, apperrors.InvalidRequest("Normalized reservation dates/quantity must match its accepted canonical room lines.")
		}
		created, e := h.createReservationTx(ctx, tx, t, in, true)
		if e != nil {
			return out, e
		}
		value = created.Reservation
	case "RESERVATION_CANCELLED":
		if !found {
			return out, apperrors.NotFound("SOURCE_RESERVATION_NOT_FOUND", "No canonical reservation belongs to this authenticated provider booking.")
		}
		value, e = reservations.Get(ctx, tx, t.Organisation, existing)
		if e != nil {
			return out, e
		}
		if value.Version != event.ExpectedReservationVersion || value.AllocationState != "ACTIVE" || !value.PolicySnapshot.AllowSourceCancellation || !reservations.Transition(value.State, "CANCELLED") {
			return out, apperrors.Conflict("SOURCE_CANCELLATION_REVIEW_REQUIRED", "Current canonical version, eligible lifecycle and explicit source-cancellation policy are required.")
		}
		var lineage bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_lineage WHERE organisation_id=$1 AND (source_reservation_id=$2 OR target_reservation_id=$2))`, t.Organisation, value.ID).Scan(&lineage); e != nil {
			return out, e
		}
		if lineage {
			return out, apperrors.Conflict("SOURCE_CANCELLATION_REVIEW_REQUIRED", "Split or merged source allocations require an exact lineage-aware operator review before provider cancellation can be acknowledged.")
		}
		rooms, e := reservations.Rooms(ctx, tx, t.Organisation, value.ID, true)
		if e != nil {
			return out, e
		}
		now := h.Auth.Service.Now().UTC()
		for _, room := range rooms {
			if e = reservations.Release(ctx, tx, t.Organisation, ids.Nil, room, room.DateFrom, now); e != nil {
				return out, e
			}
			if _, e = tx.Exec(ctx, `UPDATE reservation_rooms SET state='CANCELLED',version=version+1,updated_at=$3 WHERE organisation_id=$1 AND id=$2`, t.Organisation, room.ID, now); e != nil {
				return out, e
			}
		}
		reason := "Authenticated provider cancellation: " + event.Reference
		if !validStaffReason(reason) {
			reason = "Authenticated provider cancellation at the exact canonical version"
		}
		if _, e = tx.Exec(ctx, `UPDATE reservations SET state='CANCELLED',hold_expires_at=NULL,cancelled_at=$3,reason=$4,version=version+1,updated_at=$3 WHERE organisation_id=$1 AND id=$2`, t.Organisation, value.ID, now, reason); e != nil {
			return out, e
		}
		if e = h.cancellationObligation(ctx, tx, t, value, rooms, "CANCELLED", reason, value.Version+1); e != nil {
			return out, e
		}
		value, e = reservations.Get(ctx, tx, t.Organisation, value.ID)
		if e != nil {
			return out, e
		}
		if e = h.reservationCommit(ctx, tx, t, value, "SOURCE_CANCELLED", "reservation.cancelled.v1", reason, map[string]any{"receipt_id": receipt, "provider": c.ProviderCode, "external_version": event.ExternalVersion}); e != nil {
			return out, e
		}
	default:
		return out, apperrors.Conflict("SOURCE_AMENDMENT_REVIEW_REQUIRED", "A changed provider contract requires a canonical version-bound amendment review; retain the receipt and reconcile its price, financial and approval effects.")
	}
	reference := "business-reservation:" + value.ID.String() + ":" + event.ExternalVersion
	if _, e = tx.Exec(ctx, `INSERT INTO reservation_source_refs(id,organisation_id,property_id,connection_id,reservation_id,source_service,provider,external_reservation_id,external_version,payload_hash,receipt_id,acceptance_reference,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULL)`, ids.New(), t.Organisation, c.PropertyID, c.ID, value.ID, sourceService, c.ProviderCode, event.ExternalObjectID, event.ExternalVersion, hash, receipt, reference); e != nil {
		return out, e
	}
	return reservations.Acceptance{ReservationID: value.ID, Version: value.Version, State: value.State, DurableReference: reference}, nil
}
func (h *Handler) internalReservationStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := serviceauth.PrincipalFrom(r.Context())
	if !ok || p.Service != platform.MarketplaceService || p.Delegation.OrganizationID.IsZero() {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Signed Marketplace organisation delegation required."))
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	connection, e := queryCommercialID(r, "connection_id")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	c, e := h.loadChannelConnection(r.Context(), h.Auth.Service.Pool, p.Delegation.OrganizationID, connection)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if c.ProviderCode != distribution.Marketplace {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Connection provider does not match Marketplace identity."))
		return
	}
	if e = h.requireChannelCapability(r.Context(), h.Auth.Service.Pool, c, "READBACK"); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var allowed bool
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM reservation_source_refs s JOIN channel_event_receipts receipt ON receipt.id=s.receipt_id AND receipt.organisation_id=s.organisation_id WHERE s.organisation_id=$1 AND s.reservation_id=$2 AND receipt.connection_id=$3)`, c.OrganisationID, id, c.ID).Scan(&allowed); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if !allowed {
		reply(w, r, 0, nil, apperrors.NotFound("SOURCE_RESERVATION_NOT_FOUND", "Reservation does not belong to this provider connection."))
		return
	}
	v, e := reservations.Get(r.Context(), h.Auth.Service.Pool, c.OrganisationID, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var lineage bool
	if e = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM reservation_lineage WHERE organisation_id=$1 AND (source_reservation_id=$2 OR target_reservation_id=$2))`, c.OrganisationID, id).Scan(&lineage); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	reply(w, r, 200, map[string]any{"reservation_id": v.ID, "version": v.Version, "state": v.State, "allocation_state": v.AllocationState, "financial_state": v.FinancialState, "amount_minor": v.AmountMinor, "currency": v.Currency, "canonical_reservation_accepted": true, "lineage_reconciliation_required": lineage, "confirmed": !lineage && v.AllocationState == "ACTIVE" && (v.State == "CONFIRMED" || v.State == "CHECKED_IN" || v.State == "CHECKED_OUT" || v.State == "COMPLETED")}, nil)
}
