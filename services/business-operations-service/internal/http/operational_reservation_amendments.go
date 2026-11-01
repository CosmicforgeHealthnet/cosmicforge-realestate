package businesshttp

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"slices"
	"time"
)

type ReservationAmendmentQuote struct {
	Version int64                  `json:"version"`
	Quote   CommercialQuoteCommand `json:"quote"`
}

func (h *Handler) registerReservationAmendments(r chi.Router) {
	r.Post("/reservations/{id}/quotes", h.reservationAmendmentQuote)
	r.Patch("/reservations/{id}", h.reservationAmend)
}
func (h *Handler) reservationAmendmentQuote(w http.ResponseWriter, r *http.Request) {
	var in ReservationAmendmentQuote
	if !decode(w, r, &in) {
		return
	}
	_, pre, e := h.reservationTarget(r, "reservations:modify", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || in.Quote.ProductID.ValidateV7() != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current reservation and canonical quote command required."))
		return
	}
	h.execute(w, r, "reservation:amendment-quote:"+pre.ID.String(), in, "reservations:modify", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reservations:modify", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version || v.AllocationState != "ACTIVE" || !slices.Contains([]string{"INQUIRY", "HOLD", "PENDING_PAYMENT", "PENDING_APPROVAL", "CONFIRMED", "CHECKED_IN"}, v.State) || in.Quote.Channel != v.Channel {
			return 0, nil, apperrors.Conflict("AMENDMENT_QUOTE_STATE", "Current active reservation and its original channel are required.")
		}
		q, e := h.calculateQuote(ctx, tx, t, in.Quote, false)
		if e != nil {
			return 0, nil, e
		}
		if q.PropertyID != v.PropertyID || q.Currency != v.Currency {
			return 0, nil, apperrors.PermissionDenied("Amendment quote cannot change property or currency.")
		}
		var zone string
		if e = tx.QueryRow(ctx, `SELECT timezone FROM inventory_nodes WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.PropertyID).Scan(&zone); e != nil {
			return 0, nil, e
		}
		days, e := inventory.Calendar(ctx, tx, t.Organisation, q.PoolID, inventory.DateRange{DateFrom: q.DateFrom, DateTo: q.DateTo}, zone, v.Channel, h.Auth.Service.Now().UTC(), true)
		if e != nil {
			return 0, nil, e
		}
		for _, day := range days {
			var credit int
			if e = tx.QueryRow(ctx, `SELECT (SELECT coalesce(sum(d.quantity),0) FROM reservation_inventory_days d JOIN reservation_rooms room ON room.id=d.room_id AND room.organisation_id=d.organisation_id WHERE d.organisation_id=$1 AND room.reservation_id=$2 AND room.state='ACTIVE' AND d.pool_id=$3 AND d.day=$4::date AND d.released_at IS NULL)+(SELECT coalesce(sum(d.quantity),0) FROM inventory_hold_days d JOIN inventory_holds h ON h.organisation_id=d.organisation_id AND h.id=d.hold_id JOIN reservation_holds bound ON bound.hold_id=h.id AND bound.organisation_id=h.organisation_id JOIN reservation_rooms room ON room.id=bound.room_id AND room.organisation_id=bound.organisation_id WHERE room.reservation_id=$2 AND room.state='ACTIVE' AND d.organisation_id=$1 AND d.pool_id=$3 AND d.day=$4::date AND h.state='ACTIVE' AND h.expires_at>$5)`, t.Organisation, v.ID, q.PoolID, day.Day, h.Auth.Service.Now().UTC()).Scan(&credit); e != nil {
				return 0, nil, e
			}
			if day.Available+credit < q.Quantity {
				return 0, nil, apperrors.Conflict("INSUFFICIENT_INVENTORY", "Available stock plus this reservation's exact owned nights cannot satisfy the amendment.")
			}
		}
		raw := marshalCommercial(q)
		if _, e = tx.Exec(ctx, `INSERT INTO price_quotes(id,organisation_id,property_id,product_id,rate_version_id,date_from,date_to,amount_minor,currency,expires_at,snapshot,created_by) VALUES($1,$2,$3,$4,$5,$6::date,$7::date,$8,$9,$10,$11,$12)`, q.QuoteID, t.Organisation, q.PropertyID, q.ProductID, q.Nights[0].RateVersionID, q.DateFrom, q.DateTo, q.AmountMinor, q.Currency, q.ExpiresAt, raw, t.Account); e != nil {
			return 0, nil, e
		}
		return 201, q, nil
	})
}
func (h *Handler) reservationAmend(w http.ResponseWriter, r *http.Request) {
	h.reservationCommand(w, r, "AMENDED", "reservations:modify", true, func(ctx context.Context, tx pgx.Tx, t tenant, v reservations.Reservation, in reservations.Command) (string, error) {
		if !slices.Contains([]string{"INQUIRY", "HOLD", "PENDING_PAYMENT", "PENDING_APPROVAL", "CONFIRMED"}, v.State) || len(in.Rooms) < 1 || len(in.Rooms) > 50 || len(in.RoomIDs) > 0 {
			return "", apperrors.Conflict("RESERVATION_AMENDMENT_STATE", "Pre-arrival amendments require a complete replacement set of quoted room lines.")
		}
		if e := h.commercialGate(ctx, tx, t, v.PropertyID); e != nil {
			return "", e
		}
		policy, e := h.currentReservationPolicy(ctx, tx, t.Organisation, v.PropertyID)
		if e != nil {
			return "", e
		}
		if e = tx.QueryRow(ctx, `SELECT timezone FROM inventory_nodes WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.PropertyID).Scan(&policy.Definition.AcceptanceTimezone); e != nil {
			return "", e
		}
		if !slices.Contains(policy.Definition.AllowedSources, v.Source) {
			return "", apperrors.PermissionDenied("Current reservation policy does not permit this source.")
		}
		check := reservations.Create{PropertyID: v.PropertyID, Source: v.Source, Channel: v.Channel, SourceReference: v.SourceReference, State: "HOLD", PaymentResponsibility: v.PaymentResponsibility, Rooms: in.Rooms, Reason: in.Reason}
		if e = check.Validate(); e != nil {
			return "", e
		}
		quotes := []revenue.QuoteSnapshot{}
		amount := int64(0)
		from, to := "", ""
		for _, command := range in.Rooms {
			if command.HoldID != nil {
				return "", apperrors.InvalidRequest("Replacement holds are created atomically from the replacement quotes; independent holds cannot be substituted.")
			}
			q, e := loadCommercialQuote(ctx, tx, t.Organisation, command.QuoteID)
			if e != nil {
				return "", e
			}
			if e = h.validateHoldQuote(ctx, tx, t, inventory.HoldCommand{ProductID: command.ProductID, ProductVersion: command.ProductVersion, PoolVersion: command.PoolVersion, QuoteID: command.QuoteID, DateFrom: command.DateFrom, DateTo: command.DateTo, Quantity: command.Quantity, Channel: v.Channel}); e != nil {
				return "", e
			}
			if q.PropertyID != v.PropertyID || q.Currency != v.Currency {
				return "", apperrors.PermissionDenied("Amendment property/currency mismatch.")
			}
			amount, e = addMinor(amount, q.AmountMinor, v.Currency)
			if e != nil {
				return "", e
			}
			quotes = append(quotes, q)
			if from == "" || q.DateFrom < from {
				from = q.DateFrom
			}
			if q.DateTo > to {
				to = q.DateTo
			}
		}
		target, e := resolve(ctx, tx, t.Organisation, "PROPERTY", v.PropertyID)
		if e != nil {
			return "", e
		}
		operation := in
		operation.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESERVATION_CHANGE", target.Scope, in.ApprovalID, map[string]any{"reservation_id": v.ID, "command": operation}, v.Channel, &amount, v.Currency); e != nil {
			return "", e
		}
		old, e := reservations.Rooms(ctx, tx, t.Organisation, v.ID, true)
		if e != nil {
			return "", e
		}
		groups := map[ids.ID]int64{}
		for _, room := range old {
			groups[room.FinancialGroupID], e = addMinor(groups[room.FinancialGroupID], room.AmountMinor, v.Currency)
			if e != nil {
				return "", e
			}
		}
		// An unresolved collection might already have committed remotely. Reconcile
		// it before changing the charge basis; never collect a second gross amount.
		var uncertain bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_financial_requests j WHERE j.organisation_id=$1 AND j.financial_group_id IN (SELECT financial_group_id FROM reservation_rooms WHERE organisation_id=$1 AND reservation_id=$2 AND state='ACTIVE') AND j.kind='COLLECTION' AND j.state<>'SUCCEEDED')`, t.Organisation, v.ID).Scan(&uncertain); e != nil {
			return "", e
		}
		if uncertain {
			return "", apperrors.Conflict("COLLECTION_RECONCILIATION_REQUIRED", "Resolve the original collection outcome before amending its accepted obligation.")
		}
		guarantee := v.PaymentResponsibility != "PLATFORM_COLLECT"
		financial := "PENDING"
		required := amount
		if guarantee {
			if !slices.Contains(policy.Definition.AllowedGuarantees, v.PaymentResponsibility) || policy.Definition.MaxGuaranteeMinor == 0 || amount > policy.Definition.MaxGuaranteeMinor {
				return "", apperrors.PermissionDenied("Amendment exceeds the current independently approved guarantee limit.")
			}
			financial = "APPROVED_GUARANTEE"
		}
		type credit struct {
			id     ids.ID
			amount int64
		}
		credits := []credit{}
		groupIDs := []ids.ID{}
		for id := range groups {
			groupIDs = append(groupIDs, id)
		}
		slices.SortFunc(groupIDs, func(a, b ids.ID) int {
			if a.String() < b.String() {
				return -1
			}
			if a == b {
				return 0
			}
			return 1
		})
		if !guarantee {
			for _, id := range groupIDs {
				f, e := typedJSON[reservationFinancialGroup](ctx, tx, `SELECT to_jsonb(f) FROM reservation_financial_groups f WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
				if e != nil {
					return "", e
				}
				if f.State == "DISPUTED" || f.State == "UNKNOWN" {
					return "", apperrors.Conflict("COLLECTION_RECONCILIATION_REQUIRED", "Resolve existing disputed financial backing before amending.")
				}
				if f.State != "VERIFIED" {
					continue
				}
				if e = h.reservationFinancialEligibility(ctx, tx, t.Organisation, id); e != nil {
					return "", e
				}
				var used int64
				if e = tx.QueryRow(ctx, `SELECT coalesce(sum(amount_minor),0) FROM reservation_financial_credits WHERE organisation_id=$1 AND source_group_id=$2`, t.Organisation, id).Scan(&used); e != nil {
					return "", e
				}
				value := min(required, groups[id], f.AmountMinor-used)
				if value < 0 {
					return "", apperrors.Conflict("FINANCIAL_CREDIT_RECONCILIATION_REQUIRED", "Prior financial credit allocations exceed accepted backing.")
				}
				if value > 0 {
					credits = append(credits, credit{id, value})
					required -= value
				}
			}
			if required == 0 {
				financial = "VERIFIED"
			}
		}
		financialID := ids.New()
		now := h.Auth.Service.Now().UTC()
		var guaranteeApproval any
		if guarantee {
			guaranteeApproval = in.ApprovalID
		}
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_financial_groups(id,organisation_id,property_id,reservation_id,amount_minor,required_amount_minor,currency,policy_reference,agreement_version,state,responsibility,guarantee_approval_id,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, financialID, t.Organisation, v.PropertyID, v.ID, amount, required, v.Currency, policy.Definition.PolicyReference, v.Version+1, financial, v.PaymentResponsibility, guaranteeApproval, t.Account); e != nil {
			return "", e
		}
		for _, credit := range credits {
			if _, e = tx.Exec(ctx, `INSERT INTO reservation_financial_credits(id,organisation_id,property_id,source_group_id,target_group_id,amount_minor,currency,approval_id,source_reservation_version,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, ids.New(), t.Organisation, v.PropertyID, credit.id, financialID, credit.amount, v.Currency, in.ApprovalID, v.Version, t.Account); e != nil {
				return "", e
			}
		}
		for _, room := range old {
			if e = reservations.Release(ctx, tx, t.Organisation, t.Account, room, room.DateFrom, now); e != nil {
				return "", e
			}
			if _, e = tx.Exec(ctx, `UPDATE reservation_rooms SET state='SUPERSEDED',version=version+1,updated_at=$3 WHERE organisation_id=$1 AND id=$2`, t.Organisation, room.ID, now); e != nil {
				return "", e
			}
		}
		state := v.State
		if state == "INQUIRY" {
			state = "HOLD"
		}
		if state == "CONFIRMED" && financial == "PENDING" {
			state = "PENDING_PAYMENT"
		}
		if policy.Definition.RequireApproval && state != "CONFIRMED" {
			state = "PENDING_APPROVAL"
		}
		deadline := now.Add(time.Duration(min(policy.Definition.HoldSeconds, 900)) * time.Second)
		var expires any = deadline
		for i, command := range in.Rooms {
			room, e := h.acceptReservationRoom(ctx, tx, t, v.ID, financialID, command, quotes[i], v.Channel, state, policy.Definition, now)
			if e != nil {
				return "", e
			}
			if state == "CONFIRMED" {
				if e = h.reservationFinancialEligibility(ctx, tx, t.Organisation, financialID); e != nil {
					return "", e
				}
				if e = reservations.CommitHold(ctx, tx, t.Organisation, t.Account, room, now); e != nil {
					return "", e
				}
			}
		}
		if state == "CONFIRMED" {
			expires = nil
		}
		if _, e = tx.Exec(ctx, `UPDATE reservation_guests SET room_id=NULL,version=version+1,updated_at=$3 WHERE organisation_id=$1 AND reservation_id=$2 AND room_id IS NOT NULL`, t.Organisation, v.ID, now); e != nil {
			return "", e
		}
		_, e = tx.Exec(ctx, `UPDATE reservations SET state=$3,financial_state=$4,amount_minor=$5,date_from=$6::date,date_to=$7::date,policy_id=$8,policy_snapshot=$9,hold_expires_at=$10,reason=$11,version=version+1,updated_at=$12 WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, state, financial, amount, from, to, policy.ID, marshalCommercial(policy.Definition), expires, in.Reason, now)
		return "reservation.modified.v1", e
	})
}
