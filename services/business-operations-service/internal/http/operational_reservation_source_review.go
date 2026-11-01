package businesshttp

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/distribution"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
)

// An operator cannot acknowledge an arbitrary provider change by selecting a
// booking ID. The exact approved canonical amendment must already exist, and
// every accepted room, price quote and typed guest reference must match it.
func (h *Handler) bindReviewedReservationReceipt(ctx context.Context, tx pgx.Tx, t tenant, c CommercialChannelConnection, event distribution.Receipt, receipt ids.ID, hash string, in ReservationConflictDecision) (reservations.Acceptance, error) {
	var out reservations.Acceptance
	if event.Kind != "RESERVATION_CHANGED" || event.Booking == nil || event.ExpectedReservationVersion < 1 {
		return out, apperrors.Conflict("REVIEWED_SOURCE_CHANGE_REQUIRED", "Binding requires a complete normalized source amendment and its prior canonical version.")
	}
	if e := h.requireChannelCapability(ctx, tx, c, "RESERVATION_CHANGE"); e != nil {
		return out, e
	}
	v, e := reservations.Get(ctx, tx, t.Organisation, *in.ReservationID)
	if e != nil {
		return out, e
	}
	b := event.Booking
	if v.Version != in.ReservationVersion || v.AllocationState != "ACTIVE" || v.PropertyID != c.PropertyID || v.PropertyID != b.PropertyID || v.Channel != c.ProviderCode || v.Source != b.Source || v.SourceReference != event.ExternalObjectID || b.SourceReference != event.ExternalObjectID || v.PaymentResponsibility != b.PaymentResponsibility || b.GroupID != nil || b.CorporateID != nil || b.CorporateContractID != nil {
		return out, apperrors.Conflict("SOURCE_REVIEW_BINDING_MISMATCH", "Source identity, scope, responsibility and reviewed canonical version must match exactly.")
	}
	var proof bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_source_refs WHERE organisation_id=$1 AND connection_id=$2 AND reservation_id=$3 AND external_reservation_id=$4) AND EXISTS(SELECT 1 FROM reservation_modifications m JOIN channel_event_receipts r ON r.organisation_id=m.organisation_id AND r.id=$7 WHERE m.organisation_id=$1 AND m.reservation_id=$3 AND m.prior_version=$5 AND m.new_version=$6 AND m.action='AMENDED' AND m.created_at>=r.received_at) AND NOT EXISTS(SELECT 1 FROM reservation_lineage WHERE organisation_id=$1 AND (source_reservation_id=$3 OR target_reservation_id=$3))`, t.Organisation, c.ID, v.ID, event.ExternalObjectID, event.ExpectedReservationVersion, v.Version, receipt).Scan(&proof); e != nil {
		return out, e
	}
	if !proof {
		return out, apperrors.Conflict("SOURCE_AMENDMENT_PROOF_REQUIRED", "Apply the exact independent canonical amendment after this receipt; split/merged allocations require lineage reconciliation.")
	}
	rooms, e := reservations.Rooms(ctx, tx, t.Organisation, v.ID, true)
	if e != nil {
		return out, e
	}
	if len(rooms) != len(b.Rooms) || len(rooms) == 0 {
		return out, apperrors.Conflict("SOURCE_ROOM_SET_MISMATCH", "Every canonical room line must match the normalized reviewed source contract.")
	}
	seen := map[ids.ID]bool{}
	for _, command := range b.Rooms {
		matched := false
		for _, room := range rooms {
			if !seen[room.ID] && room.ProductID == command.ProductID && room.QuoteID == command.QuoteID && room.DateFrom == command.DateFrom && room.DateTo == command.DateTo && room.Quantity == command.Quantity {
				seen[room.ID] = true
				matched = true
				break
			}
		}
		if !matched {
			return out, apperrors.Conflict("SOURCE_ROOM_SET_MISMATCH", "A source room/date/quantity/accepted quote differs from the reviewed canonical amendment.")
		}
		p, e := inventory.GetProduct(ctx, tx, t.Organisation, command.ProductID)
		if e != nil {
			return out, e
		}
		if e = h.requireConfirmedMappings(ctx, tx, c, p); e != nil {
			return out, e
		}
	}
	guests, e := commercialJSONRows(ctx, tx, `SELECT jsonb_build_object('source_kind',source_kind,'source_reference',source_reference,'is_primary',is_primary) FROM reservation_guests WHERE organisation_id=$1 AND reservation_id=$2 ORDER BY source_kind,source_reference`, t.Organisation, v.ID)
	if e != nil {
		return out, e
	}
	if len(guests) != len(b.Guests) {
		return out, apperrors.Conflict("SOURCE_GUEST_SET_MISMATCH", "Typed source guest references must match the reviewed canonical guest set.")
	}
	wanted := map[string]bool{}
	for _, g := range b.Guests {
		if g.SourceKind != v.Source || g.GuestID != nil {
			return out, apperrors.PermissionDenied("Source guest identities cannot link unrelated canonical people.")
		}
		key := g.SourceKind + ":" + g.SourceReference
		if _, exists := wanted[key]; exists {
			return out, apperrors.InvalidRequest("Duplicate source guest reference.")
		}
		wanted[key] = g.IsPrimary
	}
	for _, raw := range guests {
		var g reservations.GuestCommand
		if e = json.Unmarshal(raw, &g); e != nil {
			return out, e
		}
		primary, ok := wanted[g.SourceKind+":"+g.SourceReference]
		if !ok || primary != g.IsPrimary {
			return out, apperrors.Conflict("SOURCE_GUEST_SET_MISMATCH", "Typed source guest references differ from canonical records.")
		}
	}
	ref := "business-reservation:" + v.ID.String() + ":" + event.ExternalVersion
	if _, e = tx.Exec(ctx, `INSERT INTO reservation_source_refs(id,organisation_id,property_id,connection_id,reservation_id,source_service,provider,external_reservation_id,external_version,payload_hash,receipt_id,acceptance_reference,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, ids.New(), t.Organisation, c.PropertyID, c.ID, v.ID, string(platform.BusinessOperationsService), c.ProviderCode, event.ExternalObjectID, event.ExternalVersion, hash, receipt, ref, t.Account); e != nil {
		return out, e
	}
	return reservations.Acceptance{ReservationID: v.ID, Version: v.Version, State: v.State, DurableReference: ref}, nil
}
