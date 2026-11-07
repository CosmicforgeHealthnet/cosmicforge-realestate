package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// ReservationQuantityCommand cancels or detaches the highest-numbered k units
// of one committed line. new_reservation applies only to a split.
type ReservationQuantityCommand struct {
	Version        int64    `json:"version"`
	RoomID         ids.ID   `json:"room_id"`
	RoomVersion    int64    `json:"room_version"`
	Quantity       int      `json:"quantity"`
	GuestIDs       []ids.ID `json:"guest_ids,omitempty"`
	NewReservation bool     `json:"new_reservation,omitempty"`
	ApprovalID     ids.ID   `json:"approval_id,omitempty"`
	Reason         string   `json:"reason"`
}

// ReservationQuantityMergeCommand recombines two lines of one split family
// into the route reservation. The source line may belong to the same
// reservation or to a split-off reservation from the same source contract.
type ReservationQuantityMergeCommand struct {
	Version             int64  `json:"version"`
	RoomID              ids.ID `json:"room_id"`
	RoomVersion         int64  `json:"room_version"`
	SourceReservationID ids.ID `json:"source_reservation_id"`
	SourceVersion       int64  `json:"source_version"`
	SourceRoomID        ids.ID `json:"source_room_id"`
	SourceRoomVersion   int64  `json:"source_room_version"`
	ApprovalID          ids.ID `json:"approval_id,omitempty"`
	Reason              string `json:"reason"`
}
type ReservationQuantityChange struct {
	ID                            ids.ID    `json:"id"`
	OrganisationID                ids.ID    `json:"organisation_id"`
	PropertyID                    ids.ID    `json:"property_id"`
	ReservationID                 ids.ID    `json:"reservation_id"`
	TargetReservationID           ids.ID    `json:"target_reservation_id"`
	Action                        string    `json:"action"`
	SourceRoomIDs                 []ids.ID  `json:"source_room_ids"`
	FamilyRootRoomID              ids.ID    `json:"family_root_room_id"`
	RemainingRoomID               ids.ID    `json:"remaining_room_id"`
	TransferredRoomID             *ids.ID   `json:"transferred_room_id"`
	CancelledRoomID               *ids.ID   `json:"cancelled_room_id"`
	OriginalQuantity              int       `json:"original_quantity"`
	RemainingQuantity             int       `json:"remaining_quantity"`
	TransferredQuantity           int       `json:"transferred_quantity"`
	ReleasedQuantity              int       `json:"released_quantity"`
	OriginalAmountMinor           int64     `json:"original_amount_minor"`
	RemainingAmountMinor          int64     `json:"remaining_amount_minor"`
	TransferredAmountMinor        int64     `json:"transferred_amount_minor"`
	ReleasedAmountMinor           int64     `json:"released_amount_minor"`
	OriginalAccommodationMinor    *int64    `json:"original_accommodation_minor"`
	RemainingAccommodationMinor   *int64    `json:"remaining_accommodation_minor"`
	TransferredAccommodationMinor *int64    `json:"transferred_accommodation_minor"`
	ReleasedAccommodationMinor    *int64    `json:"released_accommodation_minor"`
	Currency                      string    `json:"currency"`
	SourceVersion                 int64     `json:"source_version"`
	TargetVersion                 int64     `json:"target_version"`
	ApprovalID                    *ids.ID   `json:"approval_id"`
	RequestDigest                 string    `json:"request_digest"`
	Reason                        string    `json:"reason"`
	CreatedBy                     ids.ID    `json:"created_by"`
	CreatedAt                     time.Time `json:"created_at"`
}
type ReservationQuantityChangeDay struct {
	ChangeID            ids.ID `json:"change_id"`
	PoolID              ids.ID `json:"pool_id"`
	Day                 string `json:"day"`
	OriginalQuantity    int    `json:"original_quantity"`
	RemainingQuantity   int    `json:"remaining_quantity"`
	TransferredQuantity int    `json:"transferred_quantity"`
	ReleasedQuantity    int    `json:"released_quantity"`
	OriginalAllotted    int    `json:"original_allotted"`
	RemainingAllotted   int    `json:"remaining_allotted"`
	TransferredAllotted int    `json:"transferred_allotted"`
	ReleasedAllotted    int    `json:"released_allotted"`
}
type ReservationQuantityChangeView struct {
	Change ReservationQuantityChange      `json:"change"`
	Days   []ReservationQuantityChangeDay `json:"days"`
}
type ReservationQuantityResult struct {
	Change      ReservationQuantityChangeView `json:"change"`
	Reservation reservations.Detail           `json:"reservation"`
	Target      *reservations.Detail          `json:"target,omitempty"`
}
type ReservationQuantityChangeList struct {
	Items  []ReservationQuantityChangeView `json:"items"`
	Limit  int                             `json:"limit"`
	Offset int                             `json:"offset"`
}

func (h *Handler) registerReservationQuantity(r chi.Router) {
	r.Post("/reservations/{id}/quantity-cancel", h.reservationQuantityCancel)
	r.Post("/reservations/{id}/quantity-split", h.reservationQuantitySplit)
	r.Post("/reservations/{id}/quantity-merge", h.reservationQuantityMerge)
	r.Get("/reservations/{id}/quantity-changes", h.reservationQuantityChanges)
}

func validQuantityCommand(in ReservationQuantityCommand, split bool) bool {
	if in.Version < 1 || in.RoomID.ValidateV7() != nil || in.RoomVersion < 1 || in.Quantity < 1 || in.Quantity > 999 || len(in.GuestIDs) > 200 || !validStaffReason(in.Reason) || !split && in.NewReservation || !in.ApprovalID.IsZero() && in.ApprovalID.ValidateV7() != nil {
		return false
	}
	seen := map[ids.ID]bool{}
	for _, id := range in.GuestIDs {
		if id.ValidateV7() != nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (h *Handler) reservationQuantityCancel(w http.ResponseWriter, r *http.Request) {
	h.reservationQuantityCommand(w, r, false)
}
func (h *Handler) reservationQuantitySplit(w http.ResponseWriter, r *http.Request) {
	h.reservationQuantityCommand(w, r, true)
}
func (h *Handler) reservationQuantityCommand(w http.ResponseWriter, r *http.Request, split bool) {
	var in ReservationQuantityCommand
	if !decode(w, r, &in) {
		return
	}
	if !validQuantityCommand(in, split) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current reservation and line versions, a bounded unit quantity, unique guest references and a reason are required."))
		return
	}
	capability, action := "reservations:cancel", "PARTIAL_CANCEL"
	if split {
		capability, action = "reservations:modify", "SPLIT"
	}
	_, pre, e := h.reservationTarget(r, capability, split)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "reservation:quantity:"+action+":"+pre.ID.String(), in, capability, "PROPERTY", pre.PropertyID, split, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, capability, "PROPERTY", pre.PropertyID, split)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version {
			return 0, nil, apperrors.Conflict("RESERVATION_VERSION_CONFLICT", "Reservation version or allocation ownership changed.")
		}
		line, e := h.quantityLine(ctx, tx, t.Organisation, v, in.RoomID, in.RoomVersion)
		if e != nil {
			return 0, nil, e
		}
		if in.Quantity >= line.Quantity {
			return 0, nil, apperrors.InvalidRequest("Use the whole-line command for every unit; a quantity change must leave at least one unit.")
		}
		if split {
			if e = h.commercialGate(ctx, tx, t, v.PropertyID); e != nil {
				return 0, nil, e
			}
			if e = h.reservationRiskGate(ctx, tx, t.Organisation, v); e != nil {
				return 0, nil, e
			}
		}
		days, e := reservations.LockQuantityDays(ctx, tx, t.Organisation, line)
		if e != nil {
			return 0, nil, e
		}
		tail, remaining, e := reservations.TailPartition(line.AmountMinor, line.Quantity, in.Quantity)
		if e != nil {
			return 0, nil, e
		}
		var tailAcc, remainingAcc *int64
		if line.AccommodationMinor != nil {
			a, b, e := reservations.TailPartition(*line.AccommodationMinor, line.Quantity, in.Quantity)
			if e != nil {
				return 0, nil, e
			}
			tailAcc, remainingAcc = &a, &b
		}
		if split {
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESERVATION_LINEAGE", target.Scope, in.ApprovalID, map[string]any{"reservation_id": v.ID, "action": "QUANTITY_SPLIT", "command": op}, v.Channel, &tail, v.Currency); e != nil {
				return 0, nil, e
			}
		} else if !in.ApprovalID.IsZero() {
			return 0, nil, apperrors.InvalidRequest("Partial unit cancellation follows the frozen cancellation policy and accepts no approval reference.")
		}
		if e = h.quantityGuests(ctx, tx, t.Organisation, v.ID, line.ID, in.GuestIDs); e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		root, e := quantityFamilyRoot(ctx, tx, t.Organisation, line.ID)
		if e != nil {
			return 0, nil, e
		}
		slots, e := lineSlots(ctx, tx, t.Organisation, line)
		if e != nil {
			return 0, nil, e
		}
		remainingLine, e := h.appendQuantityLine(ctx, tx, t, v.ID, line, slots.head(line.Quantity-in.Quantity), remaining, remainingAcc, "ACTIVE", now)
		if e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE reservation_rooms SET state='SUPERSEDED',version=version+1,updated_at=$3 WHERE organisation_id=$1 AND id=$2`, t.Organisation, line.ID, now); e != nil {
			return 0, nil, e
		}
		if e = reservations.ReleaseProvenance(ctx, tx, t.Organisation, line.ID, now); e != nil {
			return 0, nil, e
		}
		destination := v
		var transferred, cancelled *ids.ID
		ledgerDays := []ReservationQuantityChangeDay{}
		if split {
			if in.NewReservation {
				destination, e = h.cloneQuantityReservation(ctx, tx, t, v, line, tail, in.Reason)
				if e != nil {
					return 0, nil, e
				}
			}
			child, e := h.appendQuantityLine(ctx, tx, t, destination.ID, line, slots.tail(in.Quantity), tail, tailAcc, "ACTIVE", now)
			if e != nil {
				return 0, nil, e
			}
			transferred = &child
		} else {
			record, e := h.appendQuantityLine(ctx, tx, t, v.ID, line, slots.tail(in.Quantity), tail, tailAcc, "CANCELLED", now)
			if e != nil {
				return 0, nil, e
			}
			cancelled = &record
		}
		for _, d := range days {
			keep := min(d.Allotted, line.Quantity-in.Quantity)
			moved := d.Allotted - keep
			if e = reservations.CommitProvenance(ctx, tx, t.Organisation, v.PropertyID, remainingLine, d, line.Quantity-in.Quantity, keep); e != nil {
				return 0, nil, e
			}
			row := ReservationQuantityChangeDay{PoolID: d.PoolID, Day: d.Day, OriginalQuantity: d.Quantity, RemainingQuantity: line.Quantity - in.Quantity, OriginalAllotted: d.Allotted, RemainingAllotted: keep}
			if split {
				if e = reservations.CommitProvenance(ctx, tx, t.Organisation, v.PropertyID, *transferred, d, in.Quantity, moved); e != nil {
					return 0, nil, e
				}
				row.TransferredQuantity, row.TransferredAllotted = in.Quantity, moved
			} else {
				if e = reservations.DecommitCalendar(ctx, tx, t.Organisation, t.Account, d, in.Quantity, now); e != nil {
					return 0, nil, e
				}
				row.ReleasedQuantity, row.ReleasedAllotted = in.Quantity, moved
			}
			ledgerDays = append(ledgerDays, row)
		}
		// Guests that travel with the detached units follow them; a cancelled
		// unit's guests are unassigned. Every other unit guest stays with the
		// unchanged remaining slots.
		if len(in.GuestIDs) > 0 {
			if split {
				if _, e = tx.Exec(ctx, `UPDATE reservation_guests SET reservation_id=$3,room_id=$4,version=version+1,updated_at=$5 WHERE organisation_id=$1 AND id=ANY($2::uuid[])`, t.Organisation, in.GuestIDs, destination.ID, *transferred, now); e != nil {
					return 0, nil, e
				}
			} else if _, e = tx.Exec(ctx, `UPDATE reservation_guests SET room_id=NULL,version=version+1,updated_at=$3 WHERE organisation_id=$1 AND id=ANY($2::uuid[]) AND room_id IS NOT NULL`, t.Organisation, in.GuestIDs, now); e != nil {
				return 0, nil, e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE reservation_guests SET room_id=$3,version=version+1,updated_at=$4 WHERE organisation_id=$1 AND room_id=$2`, t.Organisation, line.ID, remainingLine, now); e != nil {
			return 0, nil, e
		}
		if split && in.NewReservation {
			if e = h.copyUnassignedGuests(ctx, tx, t, v, destination.ID); e != nil {
				return 0, nil, e
			}
		}
		amount := v.AmountMinor
		if destination.ID != v.ID || !split {
			amount -= tail
		}
		if _, e = tx.Exec(ctx, `UPDATE reservations SET amount_minor=$3,reason=$4,version=version+1,updated_at=$5 WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, amount, in.Reason, now); e != nil {
			return 0, nil, e
		}
		next, e := reservations.Get(ctx, tx, t.Organisation, v.ID)
		if e != nil {
			return 0, nil, e
		}
		if !split {
			cancelledRoom, e := reservations.Load[reservations.Room](ctx, tx, `SELECT to_jsonb(r) FROM reservation_rooms r WHERE organisation_id=$1 AND id=$2`, t.Organisation, *cancelled)
			if e != nil {
				return 0, nil, e
			}
			if e = h.cancellationObligation(ctx, tx, t, v, []reservations.Room{cancelledRoom}, "PARTIAL_CANCELLED", in.Reason, next.Version); e != nil {
				return 0, nil, e
			}
		}
		if destination.ID != v.ID {
			destination, e = reservations.Get(ctx, tx, t.Organisation, destination.ID)
			if e != nil {
				return 0, nil, e
			}
		} else {
			destination = next
		}
		change := ReservationQuantityChange{ID: ids.New(), ReservationID: v.ID, TargetReservationID: destination.ID, Action: action, SourceRoomIDs: []ids.ID{line.ID}, FamilyRootRoomID: root, RemainingRoomID: remainingLine, TransferredRoomID: transferred, CancelledRoomID: cancelled, OriginalQuantity: line.Quantity, RemainingQuantity: line.Quantity - in.Quantity, OriginalAmountMinor: line.AmountMinor, RemainingAmountMinor: remaining, OriginalAccommodationMinor: line.AccommodationMinor, RemainingAccommodationMinor: remainingAcc, Currency: line.Currency, SourceVersion: next.Version, TargetVersion: destination.Version, Reason: in.Reason}
		zero := int64(0)
		if split {
			change.TransferredQuantity, change.TransferredAmountMinor, change.TransferredAccommodationMinor = in.Quantity, tail, tailAcc
			change.ReleasedAccommodationMinor = nilIfNil(line.AccommodationMinor, &zero)
			change.ApprovalID = &in.ApprovalID
		} else {
			change.ReleasedQuantity, change.ReleasedAmountMinor, change.ReleasedAccommodationMinor = in.Quantity, tail, tailAcc
			change.TransferredAccommodationMinor = nilIfNil(line.AccommodationMinor, &zero)
		}
		if e = h.recordQuantityChange(ctx, tx, t, v.PropertyID, &change, in, ledgerDays); e != nil {
			return 0, nil, e
		}
		if split && destination.ID != v.ID {
			if _, e = tx.Exec(ctx, `INSERT INTO reservation_lineage(id,organisation_id,property_id,source_reservation_id,target_reservation_id,room_ids,action,source_version,target_version,approval_id,created_by) VALUES($1,$2,$3,$4,$5,$6,'SPLIT',$7,$8,$9,$10)`, ids.New(), t.Organisation, v.PropertyID, v.ID, destination.ID, []ids.ID{*transferred}, next.Version, destination.Version, in.ApprovalID, t.Account); e != nil {
				return 0, nil, e
			}
		}
		summary := map[string]any{"quantity_change_id": change.ID, "action": action, "room_id": line.ID, "quantity": in.Quantity, "remaining_room_id": remainingLine, "transferred_room_id": transferred, "cancelled_room_id": cancelled, "target_reservation_id": destination.ID, "approval_id": change.ApprovalID}
		if e = h.reservationCommit(ctx, tx, t, next, "QUANTITY_"+action, "reservation.modified.v1", in.Reason, summary); e != nil {
			return 0, nil, e
		}
		if destination.ID != v.ID {
			if e = h.reservationCommit(ctx, tx, t, destination, "QUANTITY_SPLIT_TARGET", "reservation.created.v1", in.Reason, map[string]any{"quantity_change_id": change.ID, "source_reservation_id": v.ID, "room_id": transferred}); e != nil {
				return 0, nil, e
			}
		}
		if e = businessEvents.PublishCommercial(ctx, tx, h.Auth.Service.Outbox, t.Organisation, v.PropertyID, change.ID, "reservation_quantity_change", "QUANTITY_"+action, "reservation.quantity_changed.v1", 1, change); e != nil {
			return 0, nil, e
		}
		return h.quantityResult(ctx, tx, t.Organisation, change.ID, v.ID, destination.ID)
	})
}

// quantitySlots is the original-quote slot map carried by a replacement line.
type quantitySlots struct {
	root     ids.ID
	quantity int
	slots    []int
}

func lineSlots(ctx context.Context, tx pgx.Tx, org ids.ID, line reservations.Room) (quantitySlots, error) {
	root, quantity, slots, e := reservations.SlotMap(ctx, tx, org, line)
	return quantitySlots{root: root, quantity: quantity, slots: slots}, e
}
func (s quantitySlots) head(n int) quantitySlots {
	return quantitySlots{root: s.root, quantity: s.quantity, slots: append([]int(nil), s.slots[:n]...)}
}
func (s quantitySlots) tail(n int) quantitySlots {
	return quantitySlots{root: s.root, quantity: s.quantity, slots: append([]int(nil), s.slots[len(s.slots)-n:]...)}
}

func nilIfNil(reference, value *int64) *int64 {
	if reference == nil {
		return nil
	}
	return value
}

func (h *Handler) reservationQuantityMerge(w http.ResponseWriter, r *http.Request) {
	var in ReservationQuantityMergeCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || in.RoomID.ValidateV7() != nil || in.RoomVersion < 1 || in.SourceReservationID.ValidateV7() != nil || in.SourceVersion < 1 || in.SourceRoomID.ValidateV7() != nil || in.SourceRoomVersion < 1 || in.SourceRoomID == in.RoomID || !validStaffReason(in.Reason) || !in.ApprovalID.IsZero() && in.ApprovalID.ValidateV7() != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current target and source reservation/line versions, two distinct lines and a reason are required."))
		return
	}
	_, pre, e := h.reservationTarget(r, "reservations:modify", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "reservation:quantity:MERGE:"+pre.ID.String(), in, "reservations:modify", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "reservations:modify", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version {
			return 0, nil, apperrors.Conflict("RESERVATION_VERSION_CONFLICT", "Reservation version or allocation ownership changed.")
		}
		source := v
		if in.SourceReservationID != v.ID {
			source, e = reservations.Get(ctx, tx, t.Organisation, in.SourceReservationID)
			if e != nil {
				return 0, nil, e
			}
			if _, _, e = h.authorize(ctx, tx, "reservations:modify", "PROPERTY", source.PropertyID, true); e != nil {
				return 0, nil, e
			}
		}
		if source.Version != in.SourceVersion {
			return 0, nil, apperrors.Conflict("RESERVATION_VERSION_CONFLICT", "Source reservation version changed.")
		}
		targetLine, e := h.quantityLine(ctx, tx, t.Organisation, v, in.RoomID, in.RoomVersion)
		if e != nil {
			return 0, nil, e
		}
		sourceLine, e := h.quantityLine(ctx, tx, t.Organisation, source, in.SourceRoomID, in.SourceRoomVersion)
		if e != nil {
			return 0, nil, e
		}
		if e = h.commercialGate(ctx, tx, t, v.PropertyID); e != nil {
			return 0, nil, e
		}
		for _, candidate := range []reservations.Reservation{v, source} {
			if e = h.reservationRiskGate(ctx, tx, t.Organisation, candidate); e != nil {
				return 0, nil, e
			}
		}
		rootA, e := quantityFamilyRoot(ctx, tx, t.Organisation, targetLine.ID)
		if e != nil {
			return 0, nil, e
		}
		rootB, e := quantityFamilyRoot(ctx, tx, t.Organisation, sourceLine.ID)
		if e != nil {
			return 0, nil, e
		}
		if rootA != rootB || targetLine.ProductID != sourceLine.ProductID || targetLine.PoolID != sourceLine.PoolID || targetLine.QuoteID != sourceLine.QuoteID || targetLine.DateFrom != sourceLine.DateFrom || targetLine.DateTo != sourceLine.DateTo || targetLine.Adults != sourceLine.Adults || targetLine.Children != sourceLine.Children || targetLine.Currency != sourceLine.Currency || targetLine.FinancialGroupID != sourceLine.FinancialGroupID || (targetLine.AccommodationMinor == nil) != (sourceLine.AccommodationMinor == nil) || targetLine.Quantity+sourceLine.Quantity > 1000 {
			return 0, nil, apperrors.Conflict("RESERVATION_QUANTITY_MERGE_INCOMPATIBLE", "Only lines of one split family with identical product, pool, accepted quote, dates, occupancy, currency and financial responsibility can merge.")
		}
		if source.ID != v.ID {
			if source.State != v.State || source.PropertyID != v.PropertyID || source.Currency != v.Currency || source.Channel != v.Channel || source.Source != v.Source || source.PaymentResponsibility != v.PaymentResponsibility || source.PolicyID != v.PolicyID || hashCommercialPayload(marshalCommercial(source.PolicySnapshot)) != hashCommercialPayload(marshalCommercial(v.PolicySnapshot)) {
				return 0, nil, apperrors.Conflict("RESERVATION_QUANTITY_MERGE_INCOMPATIBLE", "Both reservations must share lifecycle, property, source, channel, currency, payment responsibility and accepted governing terms.")
			}
			if e = sameReservationSourceContract(ctx, tx, t.Organisation, v.ID, source.ID); e != nil {
				return 0, nil, e
			}
		}
		daysA, e := reservations.LockQuantityDays(ctx, tx, t.Organisation, targetLine)
		if e != nil {
			return 0, nil, e
		}
		daysB, e := reservations.LockQuantityDays(ctx, tx, t.Organisation, sourceLine)
		if e != nil {
			return 0, nil, e
		}
		for i := range daysA {
			if daysA[i].Allotted > 0 && daysB[i].Allotted > 0 && *daysA[i].AllotmentID != *daysB[i].AllotmentID {
				return 0, nil, apperrors.Conflict("RESERVATION_QUANTITY_MERGE_INCOMPATIBLE", "Lines drawing different channel allotments cannot merge.")
			}
		}
		moved := sourceLine.AmountMinor
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESERVATION_LINEAGE", target.Scope, in.ApprovalID, map[string]any{"reservation_id": v.ID, "action": "QUANTITY_MERGE", "command": op}, v.Channel, &moved, v.Currency); e != nil {
			return 0, nil, e
		}
		total, e := addMinor(targetLine.AmountMinor, sourceLine.AmountMinor, v.Currency)
		if e != nil {
			return 0, nil, e
		}
		var totalAcc *int64
		if targetLine.AccommodationMinor != nil {
			value, e := addMinor(*targetLine.AccommodationMinor, *sourceLine.AccommodationMinor, v.Currency)
			if e != nil {
				return 0, nil, e
			}
			totalAcc = &value
		}
		now := h.Auth.Service.Now().UTC()
		slotsA, e := lineSlots(ctx, tx, t.Organisation, targetLine)
		if e != nil {
			return 0, nil, e
		}
		slotsB, e := lineSlots(ctx, tx, t.Organisation, sourceLine)
		if e != nil {
			return 0, nil, e
		}
		if slotsA.root != slotsB.root || slotsA.quantity != slotsB.quantity {
			return 0, nil, apperrors.Conflict("RESERVATION_QUANTITY_MERGE_INCOMPATIBLE", "Only lines of one original accepted quote can merge.")
		}
		union := quantitySlots{root: slotsA.root, quantity: slotsA.quantity, slots: append(append([]int(nil), slotsA.slots...), slotsB.slots...)}
		slices.Sort(union.slots)
		merged, e := h.appendQuantityLine(ctx, tx, t, v.ID, targetLine, union, total, totalAcc, "ACTIVE", now)
		if e != nil {
			return 0, nil, e
		}
		ledgerDays := []ReservationQuantityChangeDay{}
		for _, line := range []reservations.Room{targetLine, sourceLine} {
			if _, e = tx.Exec(ctx, `UPDATE reservation_rooms SET state='SUPERSEDED',version=version+1,updated_at=$3 WHERE organisation_id=$1 AND id=$2`, t.Organisation, line.ID, now); e != nil {
				return 0, nil, e
			}
			if e = reservations.ReleaseProvenance(ctx, tx, t.Organisation, line.ID, now); e != nil {
				return 0, nil, e
			}
		}
		for i, d := range daysA {
			combined := d
			if combined.Allotted == 0 {
				combined.AllotmentID = daysB[i].AllotmentID
			}
			allotted := d.Allotted + daysB[i].Allotted
			if e = reservations.CommitProvenance(ctx, tx, t.Organisation, v.PropertyID, merged, combined, d.Quantity+daysB[i].Quantity, allotted); e != nil {
				return 0, nil, e
			}
			quantity := d.Quantity + daysB[i].Quantity
			ledgerDays = append(ledgerDays, ReservationQuantityChangeDay{PoolID: d.PoolID, Day: d.Day, OriginalQuantity: quantity, RemainingQuantity: quantity, OriginalAllotted: allotted, RemainingAllotted: allotted})
		}
		if _, e = tx.Exec(ctx, `UPDATE reservation_guests SET room_id=$3,version=version+1,updated_at=$4 WHERE organisation_id=$1 AND room_id=$2`, t.Organisation, targetLine.ID, merged, now); e != nil {
			return 0, nil, e
		}
		if source.ID != v.ID {
			// Guests move with their unit unless the target already holds the same
			// source identity; that duplicate stays with its original reservation.
			if _, e = tx.Exec(ctx, `UPDATE reservation_guests g SET reservation_id=$3,room_id=$4,version=g.version+1,updated_at=$5 WHERE g.organisation_id=$1 AND g.room_id=$2 AND NOT EXISTS(SELECT 1 FROM reservation_guests d WHERE d.reservation_id=$3 AND d.source_kind=g.source_kind AND d.source_reference=g.source_reference)`, t.Organisation, sourceLine.ID, v.ID, merged, now); e != nil {
				return 0, nil, e
			}
			if _, e = tx.Exec(ctx, `UPDATE reservation_guests SET room_id=NULL,version=version+1,updated_at=$3 WHERE organisation_id=$1 AND room_id=$2`, t.Organisation, sourceLine.ID, now); e != nil {
				return 0, nil, e
			}
			if _, e = tx.Exec(ctx, `UPDATE reservations SET amount_minor=amount_minor-$3,allocation_state=CASE WHEN EXISTS(SELECT 1 FROM reservation_rooms WHERE organisation_id=$1 AND reservation_id=$2 AND state='ACTIVE') THEN allocation_state ELSE 'MERGED_SOURCE' END,reason=$4,version=version+1,updated_at=$5 WHERE organisation_id=$1 AND id=$2`, t.Organisation, source.ID, sourceLine.AmountMinor, in.Reason, now); e != nil {
				return 0, nil, e
			}
			if _, e = tx.Exec(ctx, `UPDATE reservations SET amount_minor=amount_minor+$3,reason=$4,version=version+1,updated_at=$5 WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, sourceLine.AmountMinor, in.Reason, now); e != nil {
				return 0, nil, e
			}
		} else {
			if _, e = tx.Exec(ctx, `UPDATE reservation_guests SET room_id=$3,version=version+1,updated_at=$4 WHERE organisation_id=$1 AND room_id=$2`, t.Organisation, sourceLine.ID, merged, now); e != nil {
				return 0, nil, e
			}
			if _, e = tx.Exec(ctx, `UPDATE reservations SET reason=$3,version=version+1,updated_at=$4 WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, in.Reason, now); e != nil {
				return 0, nil, e
			}
		}
		next, e := reservations.Get(ctx, tx, t.Organisation, v.ID)
		if e != nil {
			return 0, nil, e
		}
		zero := int64(0)
		change := ReservationQuantityChange{ID: ids.New(), ReservationID: v.ID, TargetReservationID: v.ID, Action: "MERGE", SourceRoomIDs: []ids.ID{targetLine.ID, sourceLine.ID}, FamilyRootRoomID: rootA, RemainingRoomID: merged, OriginalQuantity: targetLine.Quantity + sourceLine.Quantity, RemainingQuantity: targetLine.Quantity + sourceLine.Quantity, OriginalAmountMinor: total, RemainingAmountMinor: total, OriginalAccommodationMinor: totalAcc, RemainingAccommodationMinor: totalAcc, TransferredAccommodationMinor: nilIfNil(totalAcc, &zero), ReleasedAccommodationMinor: nilIfNil(totalAcc, &zero), Currency: v.Currency, SourceVersion: next.Version, TargetVersion: next.Version, ApprovalID: &in.ApprovalID, Reason: in.Reason}
		if e = h.recordQuantityChange(ctx, tx, t, v.PropertyID, &change, in, ledgerDays); e != nil {
			return 0, nil, e
		}
		summary := map[string]any{"quantity_change_id": change.ID, "action": "MERGE", "room_ids": change.SourceRoomIDs, "merged_room_id": merged, "source_reservation_id": source.ID, "approval_id": in.ApprovalID}
		if e = h.reservationCommit(ctx, tx, t, next, "QUANTITY_MERGE", "reservation.modified.v1", in.Reason, summary); e != nil {
			return 0, nil, e
		}
		var other ids.ID
		if source.ID != v.ID {
			src, e := reservations.Get(ctx, tx, t.Organisation, source.ID)
			if e != nil {
				return 0, nil, e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO reservation_lineage(id,organisation_id,property_id,source_reservation_id,target_reservation_id,room_ids,action,source_version,target_version,approval_id,created_by) VALUES($1,$2,$3,$4,$5,$6,'MERGED',$7,$8,$9,$10)`, ids.New(), t.Organisation, v.PropertyID, src.ID, v.ID, []ids.ID{sourceLine.ID}, src.Version, next.Version, in.ApprovalID, t.Account); e != nil {
				return 0, nil, e
			}
			if e = h.reservationCommit(ctx, tx, t, src, "QUANTITY_MERGE_SOURCE", "reservation.modified.v1", in.Reason, map[string]any{"quantity_change_id": change.ID, "target_reservation_id": v.ID, "room_id": sourceLine.ID}); e != nil {
				return 0, nil, e
			}
			other = src.ID
		}
		if e = businessEvents.PublishCommercial(ctx, tx, h.Auth.Service.Outbox, t.Organisation, v.PropertyID, change.ID, "reservation_quantity_change", "QUANTITY_MERGE", "reservation.quantity_changed.v1", 1, change); e != nil {
			return 0, nil, e
		}
		if other.IsZero() {
			other = v.ID
		}
		status, out, e := h.quantityResult(ctx, tx, t.Organisation, change.ID, v.ID, other)
		return status, out, e
	})
}

// quantityLine loads one current committed line of the reservation and checks
// every structural precondition that is independent of the action.
func (h *Handler) quantityLine(ctx context.Context, tx pgx.Tx, org ids.ID, v reservations.Reservation, roomID ids.ID, version int64) (reservations.Room, error) {
	if v.AllocationState != "ACTIVE" || v.State != "CONFIRMED" && v.State != "CHECKED_IN" {
		return reservations.Room{}, apperrors.Conflict("RESERVATION_QUANTITY_STATE", "Quantity changes require a confirmed or in-house reservation with committed stock; use whole-line amendments before confirmation.")
	}
	line, e := reservations.Load[reservations.Room](ctx, tx, `SELECT to_jsonb(r) FROM reservation_rooms r WHERE organisation_id=$1 AND id=$2`, org, roomID)
	if e != nil {
		return line, e
	}
	if line.ReservationID != v.ID {
		return line, apperrors.PermissionDenied("The line must belong to this reservation.")
	}
	if line.Version != version || line.State != "ACTIVE" {
		return line, apperrors.Conflict("RESERVATION_QUANTITY_LINE_STALE", "The line changed or is no longer active.")
	}
	if line.OverbookedQuantity != 0 {
		return line, apperrors.Conflict("RESERVATION_QUANTITY_RECONCILIATION_REQUIRED", "Reconcile outstanding overbooking before changing line quantity.")
	}
	var physical, grouped, uncertain bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM room_assignments a WHERE a.organisation_id=$1 AND a.reservation_room_id=$2 AND (a.state IN ('ASSIGNED','OCCUPIED') OR EXISTS(SELECT 1 FROM stay_records s WHERE s.organisation_id=a.organisation_id AND s.assignment_id=a.id))),EXISTS(SELECT 1 FROM group_blocks WHERE organisation_id=$1 AND reservation_room_id=$2) OR EXISTS(SELECT 1 FROM rooming_lists WHERE organisation_id=$1 AND reservation_room_id=$2),EXISTS(SELECT 1 FROM reservation_financial_requests WHERE organisation_id=$1 AND financial_group_id=$3 AND kind='COLLECTION' AND state NOT IN ('SUCCEEDED','QUARANTINED'))`, org, line.ID, line.FinancialGroupID).Scan(&physical, &grouped, &uncertain); e != nil {
		return line, e
	}
	if physical {
		return line, apperrors.Conflict("RESERVATION_QUANTITY_PHYSICAL_ASSIGNMENT", "A line with physical room assignments or stays changes through front-desk departure workflows, not quantity fragmentation.")
	}
	if grouped {
		return line, apperrors.Conflict("RESERVATION_QUANTITY_GROUP_MANAGED", "Group block and rooming-list lines change through the group commands.")
	}
	if uncertain {
		return line, apperrors.Conflict("COLLECTION_RECONCILIATION_REQUIRED", "Resolve the outstanding collection outcome before changing the accepted obligation.")
	}
	return line, nil
}

func (h *Handler) quantityGuests(ctx context.Context, tx pgx.Tx, org, reservation, line ids.ID, guests []ids.ID) error {
	if len(guests) == 0 {
		return nil
	}
	var found int
	if e := tx.QueryRow(ctx, `SELECT count(*) FROM reservation_guests WHERE organisation_id=$1 AND reservation_id=$2 AND id=ANY($3::uuid[]) AND (room_id IS NULL OR room_id=$4)`, org, reservation, guests, line).Scan(&found); e != nil {
		return e
	}
	if found != len(guests) {
		return apperrors.PermissionDenied("Each guest must belong to this reservation and to this line or no line.")
	}
	return nil
}

func quantityFamilyRoot(ctx context.Context, tx pgx.Tx, org, room ids.ID) (ids.ID, error) {
	var root ids.ID
	e := tx.QueryRow(ctx, `WITH RECURSIVE chain(id,previous,depth) AS (SELECT id,previous_room_id,0 FROM reservation_rooms WHERE organisation_id=$1 AND id=$2 UNION ALL SELECT r.id,r.previous_room_id,c.depth+1 FROM chain c JOIN reservation_rooms r ON r.organisation_id=$1 AND r.id=c.previous WHERE c.depth<10000) SELECT id FROM chain ORDER BY depth DESC LIMIT 1`, org, room).Scan(&root)
	return root, e
}

func sameReservationSourceContract(ctx context.Context, tx pgx.Tx, org, a, b ids.ID) error {
	var same bool
	if e := tx.QueryRow(ctx, `WITH RECURSIVE origins(side,id) AS (VALUES(0,$2::uuid),(1,$3::uuid) UNION SELECT o.side,l.source_reservation_id FROM origins o JOIN reservation_lineage l ON l.organisation_id=$1 AND l.target_reservation_id=o.id), keys AS (SELECT DISTINCT o.side,s.connection_id::text||':'||s.provider||':'||s.external_reservation_id AS key FROM origins o JOIN reservation_source_refs s ON s.organisation_id=$1 AND s.reservation_id=o.id) SELECT coalesce((SELECT array_agg(key ORDER BY key) FROM keys WHERE side=0),ARRAY[]::text[])=coalesce((SELECT array_agg(key ORDER BY key) FROM keys WHERE side=1),ARRAY[]::text[])`, org, a, b).Scan(&same); e != nil {
		return e
	}
	if !same {
		return apperrors.Conflict("RESERVATION_SOURCE_MERGE_DENIED", "Independent external bookings/connections/providers cannot be merged into one source acceptance.")
	}
	return nil
}

// appendQuantityLine appends an immutable replacement line that keeps the
// accepted quote, snapshot, acceptance time and financial responsibility.
func (h *Handler) appendQuantityLine(ctx context.Context, tx pgx.Tx, t tenant, reservation ids.ID, source reservations.Room, slots quantitySlots, amount int64, accommodation *int64, state string, now time.Time) (ids.ID, error) {
	id := ids.New()
	quantity := len(slots.slots)
	if _, e := tx.Exec(ctx, `INSERT INTO reservation_rooms(id,organisation_id,property_id,reservation_id,financial_group_id,product_id,pool_id,quote_id,date_from,date_to,quantity,adults,children,amount_minor,currency,accommodation_minor,snapshot,accepted_at,state,previous_room_id,created_by,updated_at) SELECT $3,organisation_id,property_id,$4,financial_group_id,product_id,pool_id,quote_id,date_from,date_to,$5,adults,children,$6,currency,$7,snapshot,accepted_at,$8,id,$9,$10 FROM reservation_rooms WHERE organisation_id=$1 AND id=$2`, t.Organisation, source.ID, id, reservation, quantity, amount, accommodation, state, t.Account, now); e != nil {
		return id, e
	}
	if _, e := tx.Exec(ctx, `INSERT INTO reservation_room_slot_maps(organisation_id,property_id,room_id,root_room_id,root_quantity,source_slots) SELECT organisation_id,property_id,$2,$3,$4,$5 FROM reservation_rooms WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, slots.root, slots.quantity, slots.slots); e != nil {
		return id, e
	}
	if state == "ACTIVE" {
		if _, e := tx.Exec(ctx, `INSERT INTO reservation_price_snapshots(id,organisation_id,property_id,reservation_id,room_id,quote_id,snapshot,accepted_at,created_by) SELECT $3,organisation_id,property_id,$4,$5,quote_id,snapshot,accepted_at,$6 FROM reservation_price_snapshots WHERE organisation_id=$1 AND room_id=$2`, t.Organisation, source.ID, ids.New(), reservation, id, t.Account); e != nil {
			return id, e
		}
	}
	return id, nil
}

// cloneQuantityReservation creates the split-off canonical reservation with the
// same governing terms, source, financial responsibility and risk evidence.
func (h *Handler) cloneQuantityReservation(ctx context.Context, tx pgx.Tx, t tenant, v reservations.Reservation, line reservations.Room, amount int64, reason string) (reservations.Reservation, error) {
	id := ids.New()
	if _, e := tx.Exec(ctx, `INSERT INTO reservations(id,organisation_id,property_id,source,channel,source_reference,state,group_id,parent_reservation_id,policy_id,policy_snapshot,payment_responsibility,financial_state,amount_minor,currency,date_from,date_to,hold_expires_at,confirmed_at,risk_status,corporate_id,corporate_contract_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,'CONFIRMED',$7,$8,$9,$10,$11,$12,$13,$14,$15::date,$16::date,NULL,$17,$18,$19,$20,$21,$22)`, id, t.Organisation, v.PropertyID, v.Source, v.Channel, v.SourceReference, v.GroupID, v.ID, v.PolicyID, marshalCommercial(v.PolicySnapshot), v.PaymentResponsibility, v.FinancialState, amount, v.Currency, line.DateFrom, line.DateTo, v.ConfirmedAt, v.RiskStatus, v.CorporateID, v.CorporateContractID, reason, t.Account); e != nil {
		return reservations.Reservation{}, e
	}
	if _, e := tx.Exec(ctx, `INSERT INTO reservation_risk_projection(id,organisation_id,property_id,reservation_id,source_service,source_reference,status,reason_code,review_reference,appeal_reference,expires_at,approval_id,created_by) SELECT business_uuidv7(),organisation_id,property_id,$3,source_service,source_reference,status,reason_code,review_reference,appeal_reference,expires_at,approval_id,$4 FROM reservation_risk_projection WHERE organisation_id=$1 AND reservation_id=$2`, t.Organisation, v.ID, id, t.Account); e != nil {
		return reservations.Reservation{}, e
	}
	return reservations.Get(ctx, tx, t.Organisation, id)
}

// copyUnassignedGuests gives the split-off booking the original unassigned
// booker/guest references without merging global guest identities.
func (h *Handler) copyUnassignedGuests(ctx context.Context, tx pgx.Tx, t tenant, v reservations.Reservation, destination ids.ID) error {
	rows, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(g) FROM reservation_guests g WHERE organisation_id=$1 AND reservation_id=$2 AND room_id IS NULL ORDER BY id`, t.Organisation, v.ID)
	if e != nil {
		return e
	}
	for _, raw := range rows {
		var g reservations.GuestCommand
		if e = json.Unmarshal(raw, &g); e != nil {
			return e
		}
		if g.DocumentReferences == nil {
			g.DocumentReferences = []ids.ID{}
		}
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_guests(id,organisation_id,property_id,reservation_id,guest_id,source_kind,source_reference,is_primary,registration_reference,document_references,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(reservation_id,source_kind,source_reference) DO NOTHING`, ids.New(), t.Organisation, v.PropertyID, destination, g.GuestID, g.SourceKind, g.SourceReference, g.IsPrimary, g.RegistrationReference, g.DocumentReferences, t.Account); e != nil {
			return e
		}
	}
	return nil
}

func (h *Handler) recordQuantityChange(ctx context.Context, tx pgx.Tx, t tenant, property ids.ID, c *ReservationQuantityChange, command any, days []ReservationQuantityChangeDay) error {
	c.RequestDigest = hashCommercialPayload(marshalCommercial(command))
	var approval any
	if c.ApprovalID != nil && !c.ApprovalID.IsZero() {
		approval = *c.ApprovalID
	} else {
		c.ApprovalID = nil
	}
	if _, e := tx.Exec(ctx, `INSERT INTO reservation_quantity_changes(id,organisation_id,property_id,reservation_id,target_reservation_id,action,source_room_ids,family_root_room_id,remaining_room_id,transferred_room_id,cancelled_room_id,original_quantity,remaining_quantity,transferred_quantity,released_quantity,original_amount_minor,remaining_amount_minor,transferred_amount_minor,released_amount_minor,original_accommodation_minor,remaining_accommodation_minor,transferred_accommodation_minor,released_accommodation_minor,currency,source_version,target_version,approval_id,request_digest,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30)`, c.ID, t.Organisation, property, c.ReservationID, c.TargetReservationID, c.Action, c.SourceRoomIDs, c.FamilyRootRoomID, c.RemainingRoomID, c.TransferredRoomID, c.CancelledRoomID, c.OriginalQuantity, c.RemainingQuantity, c.TransferredQuantity, c.ReleasedQuantity, c.OriginalAmountMinor, c.RemainingAmountMinor, c.TransferredAmountMinor, c.ReleasedAmountMinor, c.OriginalAccommodationMinor, c.RemainingAccommodationMinor, c.TransferredAccommodationMinor, c.ReleasedAccommodationMinor, c.Currency, c.SourceVersion, c.TargetVersion, approval, c.RequestDigest, c.Reason, t.Account); e != nil {
		return e
	}
	for _, d := range days {
		if _, e := tx.Exec(ctx, `INSERT INTO reservation_quantity_change_days(organisation_id,property_id,change_id,pool_id,day,original_quantity,remaining_quantity,transferred_quantity,released_quantity,original_allotted,remaining_allotted,transferred_allotted,released_allotted) VALUES($1,$2,$3,$4,$5::date,$6,$7,$8,$9,$10,$11,$12,$13)`, t.Organisation, property, c.ID, d.PoolID, d.Day, d.OriginalQuantity, d.RemainingQuantity, d.TransferredQuantity, d.ReleasedQuantity, d.OriginalAllotted, d.RemainingAllotted, d.TransferredAllotted, d.ReleasedAllotted); e != nil {
			return e
		}
	}
	return nil
}

func loadQuantityChange(ctx context.Context, q pgx.Tx, org, id ids.ID) (ReservationQuantityChangeView, error) {
	var out ReservationQuantityChangeView
	var e error
	out.Change, e = reservations.Load[ReservationQuantityChange](ctx, q, `SELECT to_jsonb(c) FROM reservation_quantity_changes c WHERE organisation_id=$1 AND id=$2`, org, id)
	if e != nil {
		return out, e
	}
	rows, e := commercialJSONRows(ctx, q, `SELECT to_jsonb(d)||jsonb_build_object('day',d.day::text) FROM reservation_quantity_change_days d WHERE organisation_id=$1 AND change_id=$2 ORDER BY day`, org, id)
	if e != nil {
		return out, e
	}
	out.Days = []ReservationQuantityChangeDay{}
	for _, raw := range rows {
		var d ReservationQuantityChangeDay
		if e = json.Unmarshal(raw, &d); e != nil {
			return out, e
		}
		out.Days = append(out.Days, d)
	}
	return out, nil
}

func (h *Handler) quantityResult(ctx context.Context, tx pgx.Tx, org, change, reservation, other ids.ID) (int, any, error) {
	view, e := loadQuantityChange(ctx, tx, org, change)
	if e != nil {
		return 0, nil, e
	}
	detail, e := h.reservationDetail(ctx, tx, org, reservation)
	if e != nil {
		return 0, nil, e
	}
	out := ReservationQuantityResult{Change: view, Reservation: detail}
	if other != reservation {
		target, e := h.reservationDetail(ctx, tx, org, other)
		if e != nil {
			return 0, nil, e
		}
		out.Target = &target
	}
	return 200, out, nil
}

func (h *Handler) reservationQuantityChanges(w http.ResponseWriter, r *http.Request) {
	t, v, e := h.reservationTarget(r, "reservations:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out := ReservationQuantityChangeList{Items: []ReservationQuantityChangeView{}, Limit: limit, Offset: offset}
	e = pgx.BeginTxFunc(r.Context(), h.Auth.Service.Pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		rows, e := tx.Query(r.Context(), `SELECT id FROM reservation_quantity_changes WHERE organisation_id=$1 AND (reservation_id=$2 OR target_reservation_id=$2 OR $2=ANY(ARRAY(SELECT reservation_id FROM reservation_rooms WHERE organisation_id=$1 AND id=ANY(source_room_ids)))) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, v.ID, limit, offset)
		if e != nil {
			return e
		}
		found := []ids.ID{}
		for rows.Next() {
			var id ids.ID
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			found = append(found, id)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
		for _, id := range found {
			view, e := loadQuantityChange(r.Context(), tx, t.Organisation, id)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, view)
		}
		return nil
	})
	reply(w, r, 200, out, e)
}
