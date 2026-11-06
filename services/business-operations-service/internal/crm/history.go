package crm

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

type StayFact struct {
	StayID       ids.ID     `json:"stay_id"`
	StayVersion  int64      `json:"stay_version"`
	AssignmentID ids.ID     `json:"assignment_id"`
	ProductID    ids.ID     `json:"product_id"`
	RoomID       ids.ID     `json:"room_id"`
	RoomTypeID   *ids.ID    `json:"room_type_id,omitempty"`
	State        string     `json:"state"`
	CheckedInAt  time.Time  `json:"checked_in_at"`
	CheckedOutAt *time.Time `json:"checked_out_at,omitempty"`
	FolioID      ids.ID     `json:"folio_id"`
	FolioVersion int64      `json:"folio_version"`
	FolioState   string     `json:"folio_state"`
}

// StayHistory is an owned factual projection. Settled obligations are not a
// Payment ledger, net revenue or a claim that unknown receipts were funded.
type StayHistory struct {
	ReservationCreatedAt   time.Time  `json:"reservation_created_at"`
	ConfirmedAt            *time.Time `json:"confirmed_at,omitempty"`
	CompletionRecordedAt   *time.Time `json:"completion_recorded_at,omitempty"`
	OrganisationID         ids.ID     `json:"organisation_id"`
	PropertyID             ids.ID     `json:"property_id"`
	GuestID                ids.ID     `json:"guest_id"`
	ReservationID          ids.ID     `json:"reservation_id"`
	ReservationVersion     int64      `json:"reservation_version"`
	ReservationState       string     `json:"reservation_state"`
	Source                 string     `json:"source"`
	Channel                string     `json:"channel"`
	CorporateCustomerID    *ids.ID    `json:"corporate_customer_id,omitempty"`
	Timezone               string     `json:"timezone"`
	Stays                  []StayFact `json:"stays"`
	ActualNights           *int       `json:"actual_nights"`
	Completed              bool       `json:"completed"`
	FinancialState         string     `json:"financial_state"`
	SettledObligationMinor *int64     `json:"settled_obligation_minor"`
	Currency               string     `json:"currency"`
	FinancialValidUntil    *time.Time `json:"financial_valid_until,omitempty"`
	PaymentProjectionIDs   []ids.ID   `json:"payment_projection_ids"`
	SourceDigest           string     `json:"source_digest"`
}

// CurrentStayHistory reloads authoritative owned rows. Neither event payloads
// nor client supplied success/completion/spend values enter this calculation.
func CurrentStayHistory(ctx context.Context, q database.Querier, org, reservation, guest ids.ID, now time.Time) (StayHistory, error) {
	var out StayHistory
	r, e := reservations.Get(ctx, q, org, reservation)
	if e != nil {
		return out, e
	}
	var linked bool
	e = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_guests WHERE organisation_id=$1 AND reservation_id=$2 AND guest_id=$3) OR EXISTS(SELECT 1 FROM stay_records WHERE organisation_id=$1 AND reservation_id=$2 AND guest_id=$3)`, org, reservation, guest).Scan(&linked)
	if e != nil {
		return out, e
	}
	if !linked {
		return out, errors.New("guest is not associated with this owned reservation")
	}
	out = StayHistory{ReservationCreatedAt: r.CreatedAt, ConfirmedAt: r.ConfirmedAt, CompletionRecordedAt: r.CompletedAt, OrganisationID: org, PropertyID: r.PropertyID, GuestID: guest, ReservationID: r.ID, ReservationVersion: r.Version, ReservationState: r.State, Source: r.Source, Channel: r.Channel, CorporateCustomerID: r.CorporateID, Currency: r.Currency, Stays: []StayFact{}, PaymentProjectionIDs: []ids.ID{}, FinancialState: "UNKNOWN"}
	e = q.QueryRow(ctx, `SELECT timezone FROM inventory_nodes WHERE organisation_id=$1 AND id=$2 AND kind='PROPERTY'`, org, r.PropertyID).Scan(&out.Timezone)
	if e != nil {
		return out, e
	}
	rows, e := q.Query(ctx, `SELECT s.id,s.version,a.id,rr.product_id,a.room_id,CASE WHEN pool.kind='ROOM_TYPE' THEN pool.node_id ELSE NULL END,s.state,s.checked_in_at,s.checked_out_at,f.id,f.version,f.state FROM stay_records s JOIN room_assignments a ON a.organisation_id=s.organisation_id AND a.id=s.assignment_id JOIN reservation_rooms rr ON rr.organisation_id=a.organisation_id AND rr.id=a.reservation_room_id JOIN inventory_pools pool ON pool.organisation_id=rr.organisation_id AND pool.id=rr.pool_id JOIN folios f ON f.organisation_id=s.organisation_id AND f.stay_id=s.id WHERE s.organisation_id=$1 AND s.reservation_id=$2 AND s.guest_id=$3 ORDER BY s.checked_in_at,s.id`, org, reservation, guest)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var v StayFact
		e = rows.Scan(&v.StayID, &v.StayVersion, &v.AssignmentID, &v.ProductID, &v.RoomID, &v.RoomTypeID, &v.State, &v.CheckedInAt, &v.CheckedOutAt, &v.FolioID, &v.FolioVersion, &v.FolioState)
		if e != nil {
			break
		}
		out.Stays = append(out.Stays, v)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if re != nil {
		return out, re
	}
	allDeparted := len(out.Stays) > 0
	for _, stay := range out.Stays {
		if stay.State != "DEPARTED" || stay.CheckedOutAt == nil {
			allDeparted = false
		}
	}
	if allDeparted {
		n, e := ActualGuestNights(out.Stays, out.Timezone)
		if e != nil {
			return out, e
		}
		out.ActualNights = &n
	}
	complete := len(out.Stays) > 0 && r.State == "COMPLETED"
	valid := len(out.Stays) > 0
	var total int64
	for _, stay := range out.Stays {
		if stay.State != "DEPARTED" || stay.CheckedOutAt == nil {
			complete = false
		}
		balance, e := hospitality.Balance(ctx, q, org, stay.FolioID, now)
		if e != nil {
			return out, e
		}
		fiscalReady, e := hospitality.FiscalReady(ctx, q, org, stay.FolioID)
		if e != nil {
			return out, e
		}
		if stay.FolioState != "CLOSED" || balance != 0 || !fiscalReady {
			valid = false
		}
		var amount int64
		if e = q.QueryRow(ctx, `SELECT coalesce(sum(amount_minor),0)::bigint FROM folio_items WHERE organisation_id=$1 AND folio_id=$2`, org, stay.FolioID).Scan(&amount); e != nil {
			return out, e
		}
		if amount < 0 || amount > 1_000_000_000_000_000-total {
			return out, errors.New("folio obligation exceeds projection bounds")
		}
		total += amount
		proofs, e := q.Query(ctx, `SELECT p.id,p.expires_at FROM folio_payment_references a JOIN LATERAL(SELECT id,expires_at FROM reservation_payment_projections p WHERE p.organisation_id=a.organisation_id AND p.financial_group_id=a.financial_group_id AND p.intent_id=a.intent_id ORDER BY observed_at DESC,id DESC LIMIT 1)p ON true WHERE a.organisation_id=$1 AND a.folio_id=$2 ORDER BY a.id`, org, stay.FolioID)
		if e != nil {
			return out, e
		}
		for proofs.Next() {
			var id ids.ID
			var until time.Time
			if e = proofs.Scan(&id, &until); e != nil {
				break
			}
			out.PaymentProjectionIDs = append(out.PaymentProjectionIDs, id)
			if out.FinancialValidUntil == nil || until.Before(*out.FinancialValidUntil) {
				at := until
				out.FinancialValidUntil = &at
			}
		}
		pe := proofs.Err()
		proofs.Close()
		if e != nil {
			return out, e
		}
		if pe != nil {
			return out, pe
		}
	}
	out.Completed = complete
	if valid && complete {
		out.SettledObligationMinor = &total
		out.FinancialState = "CURRENT_SETTLED_OBLIGATION"
	}
	sort.Slice(out.PaymentProjectionIDs, func(i, j int) bool {
		return out.PaymentProjectionIDs[i].String() < out.PaymentProjectionIDs[j].String()
	})
	raw, e := json.Marshal(out)
	if e != nil {
		return out, e
	}
	out.SourceDigest = security.SHA256Hex(raw)
	return out, nil
}

// ActualGuestNights counts unique occupied civil nights, never promised dates
// or duplicate rooms. Same-day stays contribute zero overnight nights; day-use
// remains an actual stay. UTC hour division would be wrong across DST.
func ActualGuestNights(stays []StayFact, zone string) (int, error) {
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return 0, e
	}
	days := map[string]bool{}
	for _, s := range stays {
		if s.CheckedOutAt == nil {
			continue
		}
		if s.CheckedOutAt.Before(s.CheckedInAt) {
			return 0, errors.New("departure precedes actual arrival")
		}
		start, end := s.CheckedInAt.In(loc).Format(time.DateOnly), s.CheckedOutAt.In(loc).Format(time.DateOnly)
		day, e := time.Parse(time.DateOnly, start)
		if e != nil {
			return 0, e
		}
		for day.Format(time.DateOnly) < end {
			days[day.Format(time.DateOnly)] = true
			if len(days) > 3660 {
				return 0, errors.New("actual history exceeds bounded stay horizon")
			}
			day = day.AddDate(0, 0, 1)
		}
	}
	return len(days), nil
}
