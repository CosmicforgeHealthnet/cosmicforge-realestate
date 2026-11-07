package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"slices"
	"time"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// PMSStayExtensionCommand extends one in-house unit slot with new canonical
// nights. The quote must start exactly at the current unit chain end.
type PMSStayExtensionCommand struct {
	Version              int64   `json:"version"`
	ReservationVersion   int64   `json:"reservation_version"`
	AssignmentVersion    int64   `json:"assignment_version"`
	FolioVersion         int64   `json:"folio_version"`
	QuoteID              ids.ID  `json:"quote_id"`
	ProductVersion       int64   `json:"product_version"`
	PoolVersion          int64   `json:"pool_version"`
	DateTo               string  `json:"date_to"`
	PayerMode            string  `json:"payer_mode"`
	CorporateCustomerID  *ids.ID `json:"corporate_customer_id,omitempty"`
	CorporateContractID  *ids.ID `json:"corporate_contract_id,omitempty"`
	CorporateAmountMinor int64   `json:"corporate_amount_minor,omitempty"`
	ApprovalID           ids.ID  `json:"approval_id,omitempty"`
	Reason               string  `json:"reason"`
}

// PMSStayExtensionPayer is one accepted payer share of an extension.
type PMSStayExtensionPayer struct {
	PayerKind        string `json:"payer_kind"`
	FinancialGroupID ids.ID `json:"financial_group_id"`
	AmountMinor      int64  `json:"amount_minor"`
	Responsibility   string `json:"responsibility"`
	FinancialState   string `json:"financial_state"`
}

// PMSStayExtensionResult is the committed extension; collection is separate.
type PMSStayExtensionResult struct {
	ExtensionID        ids.ID                  `json:"extension_id"`
	ExtensionRoomID    ids.ID                  `json:"extension_room_id"`
	AssignmentID       ids.ID                  `json:"assignment_id"`
	FolioItemID        ids.ID                  `json:"folio_item_id"`
	DateFrom           string                  `json:"date_from"`
	DateTo             string                  `json:"date_to"`
	AmountMinor        int64                   `json:"amount_minor"`
	Currency           string                  `json:"currency"`
	Payers             []PMSStayExtensionPayer `json:"payers"`
	CollectionRequired bool                    `json:"collection_required"`
	StayVersion        int64                   `json:"stay_version"`
	ReservationVersion int64                   `json:"reservation_version"`
	AccessState        string                  `json:"access_state"`
}

// PMSEarlyDepartureCommand records an approved early departure. Expected
// credit and outstanding balance bind the approval to the evaluated terms.
type PMSEarlyDepartureCommand struct {
	Version                  int64  `json:"version"`
	ReservationVersion       int64  `json:"reservation_version"`
	FolioVersion             int64  `json:"folio_version"`
	ExpectedCreditMinor      int64  `json:"expected_credit_minor"`
	ExpectedOutstandingMinor int64  `json:"expected_outstanding_minor"`
	ApprovalID               ids.ID `json:"approval_id,omitempty"`
	Reason                   string `json:"reason"`
}

// PMSEarlyDepartureSegment is the frozen effect on one accepted line.
type PMSEarlyDepartureSegment struct {
	ReservationRoomID ids.ID                          `json:"reservation_room_id"`
	Slot              int                             `json:"slot"`
	FolioItemID       ids.ID                          `json:"folio_item_id"`
	UnusedNights      int                             `json:"unused_nights"`
	UnusedMinor       int64                           `json:"unused_minor"`
	CreditMinor       int64                           `json:"credit_minor"`
	Payers            []PMSEarlyDepartureSegmentPayer `json:"payers"`
}

// PMSEarlyDepartureSegmentPayer is one financial group's share of a credit.
type PMSEarlyDepartureSegmentPayer struct {
	FinancialGroupID ids.ID `json:"financial_group_id"`
	Responsibility   string `json:"responsibility"`
	CreditMinor      int64  `json:"credit_minor"`
	Collection       string `json:"collection"`
}

// PMSEarlyDepartureTerms is a non-mutating evaluation of the frozen policy.
type PMSEarlyDepartureTerms struct {
	StayID                ids.ID                      `json:"stay_id"`
	DepartureDay          string                      `json:"departure_day"`
	Terms                 hospitality.DepartureTerms  `json:"terms"`
	Policy                hospitality.DeparturePolicy `json:"policy"`
	BalanceMinor          int64                       `json:"balance_minor"`
	OutstandingAfterMinor int64                       `json:"outstanding_after_minor"`
	ApprovalExposureMinor int64                       `json:"approval_exposure_minor"`
	Currency              string                      `json:"currency"`
	Segments              []PMSEarlyDepartureSegment  `json:"segments"`
}

// PMSEarlyDepartureResult is the committed revised obligation and departure.
type PMSEarlyDepartureResult struct {
	EarlyDepartureID ids.ID                   `json:"early_departure_id"`
	Terms            PMSEarlyDepartureTerms   `json:"terms"`
	FinancialState   string                   `json:"financial_state"`
	RefundableMinor  *int64                   `json:"refundable_minor"`
	RefundRequestIDs []ids.ID                 `json:"refund_request_ids"`
	ReleasedNights   []reservations.UnitNight `json:"released_nights"`
	Departure        map[string]any           `json:"departure"`
	RefundExecuted   bool                     `json:"refund_executed"`
}

// PMSStayExtensionCollection queues the guest share of an extension through
// the existing durable Payment worker; 202 is never collected money.
type PMSStayExtensionCollection struct {
	PaymentPolicyID      ids.ID `json:"payment_policy_id"`
	PaymentPolicyVersion string `json:"payment_policy_version"`
	PaymentPolicyFamily  string `json:"payment_policy_family"`
	PayerWalletID        ids.ID `json:"payer_wallet_id"`
	ConsentReference     string `json:"consent_reference"`
	LegalPartyReference  string `json:"legal_party_reference"`
	Email                string `json:"email"`
	Schedule             string `json:"schedule"`
	Reason               string `json:"reason"`
}

func (h *Handler) registerPMSStayAmendments(r chi.Router) {
	r.Get("/stays/{id}/extensions", h.pmsStayExtensions)
	r.Post("/stays/{id}/extensions", h.pmsStayExtend)
	r.Post("/stays/{id}/extensions/{extension_id}/payment-requests", h.pmsStayExtensionCollection)
	r.Get("/stays/{id}/early-departure/preview", h.pmsEarlyDeparturePreview)
	r.Get("/stays/{id}/early-departure", h.pmsEarlyDepartureGet)
	r.Post("/stays/{id}/early-departure", h.pmsEarlyDeparture)
	r.Get("/reservations/{id}/unit-releases", h.reservationUnitReleases)
}

func validStayExtension(in PMSStayExtensionCommand) bool {
	if in.Version < 1 || in.ReservationVersion < 1 || in.AssignmentVersion < 1 || in.FolioVersion < 1 || in.QuoteID.ValidateV7() != nil || in.ProductVersion < 1 || in.PoolVersion < 1 || !validStaffReason(in.Reason) {
		return false
	}
	if _, e := time.Parse(time.DateOnly, in.DateTo); e != nil {
		return false
	}
	corporate := in.CorporateCustomerID != nil && in.CorporateContractID != nil && in.CorporateCustomerID.ValidateV7() == nil && in.CorporateContractID.ValidateV7() == nil
	switch in.PayerMode {
	case "RESERVATION_TERMS":
		return in.CorporateCustomerID == nil && in.CorporateContractID == nil && in.CorporateAmountMinor == 0
	case "CORPORATE":
		return corporate && in.CorporateAmountMinor == 0
	case "SPLIT":
		return corporate && in.CorporateAmountMinor > 0
	}
	return false
}

// requireStayChainBacking proves committed stock for one unit slot across its
// accepted line and any contiguous in-house extension lines.
func requireStayChainBacking(ctx context.Context, q database.Querier, org ids.ID, line reservations.Room, slot int, from, to string) error {
	if from < line.DateTo {
		if e := reservations.RequirePhysicalBacking(ctx, q, org, line.ID, inventory.DateRange{DateFrom: from, DateTo: min(to, line.DateTo)}); e != nil {
			return e
		}
	}
	rows, e := commercialJSONRows(ctx, q, `SELECT jsonb_build_object('room_id',extension_room_id,'date_from',date_from,'date_to',date_to) FROM stay_extensions WHERE organisation_id=$1 AND source_room_id=$2 AND slot=$3 AND date_to>$4::date AND date_from<$5::date ORDER BY date_from`, org, line.ID, slot, from, to)
	if e != nil {
		return e
	}
	for _, raw := range rows {
		var x struct {
			RoomID   ids.ID `json:"room_id"`
			DateFrom string `json:"date_from"`
			DateTo   string `json:"date_to"`
		}
		if e = json.Unmarshal(raw, &x); e != nil {
			return e
		}
		if e = reservations.RequirePhysicalBacking(ctx, q, org, x.RoomID, inventory.DateRange{DateFrom: max(from, x.DateFrom), DateTo: min(to, x.DateTo)}); e != nil {
			return e
		}
	}
	return nil
}

func (h *Handler) pmsStayTarget(r *http.Request) (tenant, hospitality.Stay, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, hospitality.Stay{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, hospitality.Stay{}, e
	}
	s, e := hospitality.GetStay(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	return t, s, e
}

func (h *Handler) pmsStayExtend(w http.ResponseWriter, r *http.Request) {
	var in PMSStayExtensionCommand
	if !decode(w, r, &in) {
		return
	}
	if !validStayExtension(in) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current stay, reservation, assignment and folio versions, an exact canonical quote, departure date, payer mode and reason required."))
		return
	}
	_, pre, e := h.pmsStayTarget(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:stay-extension:"+pre.ID.String(), in, "pms:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		out, e := h.executePMSStayExtension(ctx, tx, pre, in)
		return 201, out, e
	})
}

type stayExtensionPayerPlan struct {
	kind           string
	responsibility string
	amount         int64
	corporate      *ids.ID
	contract       *ids.ID
	group          ids.ID
	state          string
}

func (h *Handler) executePMSStayExtension(ctx context.Context, tx pgx.Tx, pre hospitality.Stay, in PMSStayExtensionCommand) (PMSStayExtensionResult, error) {
	var out PMSStayExtensionResult
	t, target, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", pre.PropertyID, true)
	if e != nil {
		return out, e
	}
	for _, capability := range []string{"reservations:modify", "folios:write"} {
		if _, _, e = h.authorize(ctx, tx, capability, "PROPERTY", pre.PropertyID, true); e != nil {
			return out, e
		}
	}
	s, e := hospitality.GetStay(ctx, tx, t.Organisation, pre.ID)
	if e != nil {
		return out, e
	}
	a, e := hospitality.GetAssignment(ctx, tx, t.Organisation, s.AssignmentID)
	if e != nil {
		return out, e
	}
	v, e := reservations.Get(ctx, tx, t.Organisation, s.ReservationID)
	if e != nil {
		return out, e
	}
	f, e := hospitality.Load[hospitality.Folio](ctx, tx, `SELECT to_jsonb(f) FROM folios f WHERE organisation_id=$1 AND stay_id=$2`, t.Organisation, s.ID)
	if e != nil {
		return out, e
	}
	if s.State != "ACTIVE" || s.Version != in.Version || a.State != "OCCUPIED" || a.Version != in.AssignmentVersion || v.State != "CHECKED_IN" || v.AllocationState != "ACTIVE" || v.Version != in.ReservationVersion || f.Version != in.FolioVersion || !slices.Contains([]string{"OPEN", "SETTLEMENT_PENDING", "SETTLED"}, f.State) {
		return out, apperrors.Conflict("STAY_EXTENSION_STATE_VERSION", "Current active stay, occupied assignment, checked-in reservation and reviewable folio required.")
	}
	if e = h.commercialGate(ctx, tx, t, v.PropertyID); e != nil {
		return out, e
	}
	if e = h.reservationRiskGate(ctx, tx, t.Organisation, v); e != nil {
		return out, e
	}
	line, e := hospitality.Load[reservations.Room](ctx, tx, `SELECT to_jsonb(r) FROM reservation_rooms r WHERE organisation_id=$1 AND id=$2 AND reservation_id=$3 AND state='ACTIVE'`, t.Organisation, a.ReservationRoomID, v.ID)
	if e != nil {
		return out, e
	}
	chainEnd, e := reservations.StayChainEnd(ctx, tx, t.Organisation, line, a.Slot)
	if e != nil {
		return out, e
	}
	if a.DateTo != chainEnd {
		return out, apperrors.Conflict("STAY_EXTENSION_ASSIGNMENT_RECONCILIATION", "The physical assignment must cover the exact accepted unit nights before extending.")
	}
	q, e := loadCommercialQuote(ctx, tx, t.Organisation, in.QuoteID)
	if e != nil {
		return out, e
	}
	if q.DateFrom != chainEnd || q.DateTo != in.DateTo || q.Quantity != 1 || q.ProductID != line.ProductID || q.PoolID != line.PoolID || q.PropertyID != v.PropertyID || q.Currency != v.Currency || q.Channel != v.Channel || q.Adults != line.Adults || q.Children != line.Children {
		return out, apperrors.Conflict("STAY_EXTENSION_QUOTE_MISMATCH", "A single-unit quote for the same product, occupancy and channel starting at the current departure is required.")
	}
	if e = h.validateHoldQuote(ctx, tx, t, inventory.HoldCommand{ProductID: q.ProductID, ProductVersion: in.ProductVersion, PoolVersion: in.PoolVersion, QuoteID: q.QuoteID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1, Channel: v.Channel}); e != nil {
		return out, e
	}
	var physicalConflict bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM room_assignment_days d WHERE d.organisation_id=$1 AND d.room_id=$2 AND d.day>=$3::date AND d.day<$4::date AND d.released_at IS NULL) OR NOT EXISTS(SELECT 1 FROM room_status_current s WHERE s.organisation_id=$1 AND s.room_id=$2 AND s.state='OCCUPIED')`, t.Organisation, a.RoomID, q.DateFrom, q.DateTo).Scan(&physicalConflict); e != nil {
		return out, e
	}
	if physicalConflict {
		return out, apperrors.Conflict("PHYSICAL_ROOM_UNAVAILABLE_FOR_EXTENSION", "The occupied physical room is already committed or unavailable for the requested nights; use a room move or another quote.")
	}
	policy, e := h.currentReservationPolicy(ctx, tx, t.Organisation, v.PropertyID)
	if e != nil {
		return out, e
	}
	guestAmount := q.AmountMinor
	payers := []stayExtensionPayerPlan{}
	if in.PayerMode != "RESERVATION_TERMS" {
		corporateAmount := q.AmountMinor
		if in.PayerMode == "SPLIT" {
			if in.CorporateAmountMinor >= q.AmountMinor {
				return out, apperrors.InvalidRequest("A split extension keeps a positive guest share; use CORPORATE for full liability.")
			}
			corporateAmount = in.CorporateAmountMinor
		}
		if _, e = pmsCorporatePayer(ctx, tx, t.Organisation, f, *in.CorporateCustomerID, *in.CorporateContractID, h.Auth.Service.Now()); e != nil {
			return out, e
		}
		guestAmount -= corporateAmount
		payers = append(payers, stayExtensionPayerPlan{kind: "CORPORATE", responsibility: "CORPORATE_CREDIT", amount: corporateAmount, corporate: in.CorporateCustomerID, contract: in.CorporateContractID})
	}
	if guestAmount > 0 {
		payers = append([]stayExtensionPayerPlan{{kind: "GUEST", responsibility: v.PaymentResponsibility, amount: guestAmount}}, payers...)
	}
	if len(payers) == 0 {
		return out, apperrors.Conflict("STAY_EXTENSION_PAYER_REQUIRED", "A zero-value extension still needs an accountable payer.")
	}
	guaranteed := int64(0)
	for i := range payers {
		if payers[i].responsibility == "PLATFORM_COLLECT" {
			payers[i].state = "PENDING"
			continue
		}
		if !slices.Contains(policy.Definition.AllowedGuarantees, payers[i].responsibility) {
			return out, apperrors.PermissionDenied("The current approved reservation policy does not permit this extension guarantee.")
		}
		payers[i].state = "APPROVED_GUARANTEE"
		if guaranteed, e = addMinor(guaranteed, payers[i].amount, v.Currency); e != nil {
			return out, e
		}
	}
	if guaranteed > 0 {
		if policy.Definition.MaxGuaranteeMinor == 0 || guaranteed > policy.Definition.MaxGuaranteeMinor {
			return out, apperrors.PermissionDenied("Extension exceeds the current independently approved guarantee limit.")
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_STAY_EXTENSION", target.Scope, in.ApprovalID, map[string]any{"stay_id": s.ID, "command": op}, "", &guaranteed, v.Currency); e != nil {
			return out, e
		}
	} else if !in.ApprovalID.IsZero() {
		return out, apperrors.InvalidRequest("A collectable guest extension does not consume a guarantee approval.")
	}
	now := h.Auth.Service.Now().UTC()
	var approval any
	if guaranteed > 0 {
		approval = in.ApprovalID
	}
	for i := range payers {
		payers[i].group = ids.New()
		var parent, guarantee any
		if i > 0 {
			parent = payers[0].group
		}
		if payers[i].state == "APPROVED_GUARANTEE" {
			guarantee = in.ApprovalID
		}
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_financial_groups(id,organisation_id,property_id,reservation_id,parent_group_id,amount_minor,required_amount_minor,currency,policy_reference,agreement_version,state,responsibility,guarantee_approval_id,created_by) VALUES($1,$2,$3,$4,$5,$6,$6,$7,$8,$9,$10,$11,$12,$13)`, payers[i].group, t.Organisation, v.PropertyID, v.ID, parent, payers[i].amount, v.Currency, policy.Definition.PolicyReference, v.Version+1, payers[i].state, payers[i].responsibility, guarantee, t.Account); e != nil {
			return out, e
		}
	}
	room, e := h.acceptReservationRoom(ctx, tx, t, v.ID, payers[0].group, reservations.RoomCommand{ProductID: q.ProductID, ProductVersion: in.ProductVersion, PoolVersion: in.PoolVersion, QuoteID: q.QuoteID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1}, q, v.Channel, "CONFIRMED", v.PolicySnapshot, now)
	if e != nil {
		return out, e
	}
	if e = reservations.CommitHold(ctx, tx, t.Organisation, t.Account, room, now); e != nil {
		return out, e
	}
	day, e := h.pmsDay(ctx, tx, t, v.PropertyID, true)
	if e != nil {
		return out, e
	}
	extension, item, next := ids.New(), ids.New(), ids.New()
	snapshot := marshalCommercial(map[string]any{"accepted_quote": q, "reservation_room_id": room.ID, "slot": 1, "quantity": 1, "gross_includes_accepted_components": true, "stay_extension_id": extension, "extends_reservation_room_id": line.ID, "extends_slot": a.Slot})
	if _, e = tx.Exec(ctx, `INSERT INTO folio_items(id,organisation_id,property_id,folio_id,kind,source_reference,quantity,amount_minor,currency,policy_snapshot,approval_id,reason,created_by,business_day_id) VALUES($1,$2,$3,$4,'ACCOMMODATION',$5,1,$6,$7,$8,$9,$10,$11,$12)`, item, t.Organisation, v.PropertyID, f.ID, "stay-extension:"+extension.String(), q.AmountMinor, q.Currency, snapshot, approval, in.Reason, t.Account, day.ID); e != nil {
		return out, e
	}
	grossItem, e := hospitality.Load[hospitality.Item](ctx, tx, `SELECT to_jsonb(i) FROM folio_items i WHERE organisation_id=$1 AND id=$2`, t.Organisation, item)
	if e != nil {
		return out, e
	}
	detail, _, e := h.acceptedFolioFiscal(ctx, tx, f, grossItem)
	if e != nil {
		return out, e
	}
	if _, e = h.captureFolioFiscal(ctx, tx, t, f, grossItem, detail, "ACCEPTED_QUOTE", ids.Nil, "", in.Reason, false); e != nil {
		return out, e
	}
	op := in
	op.ApprovalID = ids.Nil
	if _, e = tx.Exec(ctx, `INSERT INTO stay_extensions(id,organisation_id,property_id,reservation_id,stay_id,previous_assignment_id,assignment_id,source_room_id,slot,extension_room_id,quote_id,date_from,date_to,amount_minor,currency,payer_mode,folio_id,folio_item_id,approval_id,reservation_version,command_digest,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::date,$13::date,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`, extension, t.Organisation, v.PropertyID, v.ID, s.ID, a.ID, next, line.ID, a.Slot, room.ID, q.QuoteID, q.DateFrom, q.DateTo, q.AmountMinor, q.Currency, in.PayerMode, f.ID, item, approval, v.Version+1, hashCommercialPayload(marshalCommercial(op)), in.Reason, t.Account); e != nil {
		return out, e
	}
	for _, p := range payers {
		if _, e = tx.Exec(ctx, `INSERT INTO stay_extension_payers(id,organisation_id,property_id,extension_id,payer_kind,corporate_customer_id,corporate_contract_id,financial_group_id,amount_minor,currency,responsibility) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, ids.New(), t.Organisation, v.PropertyID, extension, p.kind, p.corporate, p.contract, p.group, p.amount, v.Currency, p.responsibility); e != nil {
			return out, e
		}
		out.Payers = append(out.Payers, PMSStayExtensionPayer{PayerKind: p.kind, FinancialGroupID: p.group, AmountMinor: p.amount, Responsibility: p.responsibility, FinancialState: p.state})
		if p.state == "PENDING" {
			out.CollectionRequired = true
		}
	}
	if e = h.pmsCommit(ctx, tx, t, v.PropertyID, extension, "stay_extension", "STAY_EXTENSION_ACCEPTED", "hospitality.stay.extension.accepted.v1", 1, map[string]any{"stay_id": s.ID, "reservation_id": v.ID, "extension_room_id": room.ID, "date_from": q.DateFrom, "date_to": q.DateTo, "payer_mode": in.PayerMode, "approval_id": approval, "reason": in.Reason}); e != nil {
		return out, e
	}
	// The physical continuation is a new immutable assignment of the same room
	// and unit slot from the current civil night to the new departure.
	zone, e := time.LoadLocation(v.PolicySnapshot.AcceptanceTimezone)
	if e != nil {
		return out, e
	}
	from := max(now.In(zone).Format(time.DateOnly), a.DateFrom)
	if _, e = tx.Exec(ctx, `UPDATE room_assignment_days SET released_at=$4 WHERE organisation_id=$1 AND assignment_id=$2 AND day>=$3::date AND released_at IS NULL`, t.Organisation, a.ID, from, now); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `UPDATE room_assignments SET state='RELEASED',version=version+1,reason=$3,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, a.ID, in.Reason); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO room_assignments(id,organisation_id,property_id,reservation_id,reservation_room_id,room_id,slot,date_from,date_to,state,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8::date,$9::date,'OCCUPIED',$10,$11)`, next, t.Organisation, v.PropertyID, v.ID, line.ID, a.RoomID, a.Slot, from, q.DateTo, in.Reason, t.Account); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO room_assignment_days(organisation_id,assignment_id,room_id,day) SELECT $1,$2,$3,d::date FROM generate_series($4::date,$5::date-1,interval '1 day') d`, t.Organisation, next, a.RoomID, from, q.DateTo); e != nil {
		return out, e
	}
	if e = h.pmsCommit(ctx, tx, t, v.PropertyID, a.ID, "room_assignment", "STAY_EXTENSION_SOURCE_RELEASED", "hospitality.room.assigned.v1", a.Version+1, map[string]any{"stay_id": s.ID, "extension_id": extension, "next_assignment_id": next, "reason": in.Reason}); e != nil {
		return out, e
	}
	if e = h.pmsCommit(ctx, tx, t, v.PropertyID, next, "room_assignment", "STAY_EXTENSION_CONTINUATION_OCCUPIED", "hospitality.room.assigned.v1", 1, map[string]any{"stay_id": s.ID, "extension_id": extension, "previous_assignment_id": a.ID, "date_from": from, "date_to": q.DateTo, "reason": in.Reason}); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `UPDATE stay_records SET assignment_id=$3,version=version+1,reason=$4,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, s.ID, next, in.Reason); e != nil {
		return out, e
	}
	if e = h.pmsCommit(ctx, tx, t, v.PropertyID, s.ID, "stay", "STAY_EXTENDED", "hospitality.stay.extended.v1", s.Version+1, map[string]any{"reservation_id": v.ID, "extension_id": extension, "previous_assignment_id": a.ID, "assignment_id": next, "date_to": q.DateTo, "reason": in.Reason}); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `UPDATE folios SET state=CASE WHEN state='SETTLED' THEN 'OPEN' ELSE state END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, f.ID); e != nil {
		return out, e
	}
	if e = h.pmsCommit(ctx, tx, t, v.PropertyID, f.ID, "folio", "FOLIO_EXTENSION_POSTED", "hospitality.folio.updated.v1", f.Version+1, map[string]any{"item_id": item, "extension_id": extension, "amount_minor": q.AmountMinor, "currency": q.Currency, "reason": in.Reason}); e != nil {
		return out, e
	}
	// A device credential is bound to its assignment window; the old grant is
	// revoked and a new window is requested. UNKNOWN never authorizes entry.
	if _, e = h.queuePMSAccess(ctx, tx, t, s.ID, a.ID, "REVOKE", in.Reason); e != nil {
		return out, e
	}
	if _, e = h.queuePMSAccess(ctx, tx, t, s.ID, next, "ISSUE", in.Reason); e != nil {
		return out, e
	}
	financial, e := reservationFinancialSummary(ctx, tx, t.Organisation, v.ID)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `UPDATE reservations SET date_to=greatest(date_to,$3::date),amount_minor=amount_minor+$4,financial_state=$5,version=version+1,updated_at=$6 WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, q.DateTo, q.AmountMinor, financial, now); e != nil {
		return out, e
	}
	v, e = reservations.Get(ctx, tx, t.Organisation, v.ID)
	if e != nil {
		return out, e
	}
	if e = h.reservationCommit(ctx, tx, t, v, "STAY_EXTENDED", "reservation.modified.v1", in.Reason, map[string]any{"stay_id": s.ID, "extension_id": extension, "extension_room_id": room.ID, "quote_id": q.QuoteID, "date_from": q.DateFrom, "date_to": q.DateTo, "amount_minor": q.AmountMinor, "payers": out.Payers, "approval_id": approval}); e != nil {
		return out, e
	}
	out.ExtensionID, out.ExtensionRoomID, out.AssignmentID, out.FolioItemID = extension, room.ID, next, item
	out.DateFrom, out.DateTo, out.AmountMinor, out.Currency = q.DateFrom, q.DateTo, q.AmountMinor, q.Currency
	out.StayVersion, out.ReservationVersion, out.AccessState = s.Version+1, v.Version, "UNKNOWN"
	return out, nil
}

// reservationFinancialSummary derives the root financial state from every
// active accepted line group, matching the Payment readback projection.
func reservationFinancialSummary(ctx context.Context, q database.Querier, org, reservation ids.ID) (string, error) {
	var disputed, unknown, failed, pending, guarantee bool
	if e := q.QueryRow(ctx, `WITH RECURSIVE backing(id) AS (SELECT seed.id FROM (SELECT financial_group_id AS id FROM reservation_rooms WHERE organisation_id=$1 AND reservation_id=$2 AND state='ACTIVE' UNION SELECT p.financial_group_id FROM stay_extension_payers p JOIN stay_extensions x ON x.organisation_id=p.organisation_id AND x.id=p.extension_id WHERE x.organisation_id=$1 AND x.reservation_id=$2) seed UNION SELECT c.source_group_id FROM backing b JOIN reservation_financial_credits c ON c.organisation_id=$1 AND c.target_group_id=b.id) SELECT coalesce(bool_or(f.state='DISPUTED'),false),coalesce(bool_or(f.state='UNKNOWN'),false),coalesce(bool_or(f.state='FAILED'),false),coalesce(bool_or(f.state NOT IN ('DISPUTED','UNKNOWN','FAILED','VERIFIED','APPROVED_GUARANTEE')),false),coalesce(bool_or(f.state='APPROVED_GUARANTEE'),false) FROM backing b JOIN reservation_financial_groups f ON f.organisation_id=$1 AND f.id=b.id`, org, reservation).Scan(&disputed, &unknown, &failed, &pending, &guarantee); e != nil {
		return "", e
	}
	switch {
	case disputed:
		return "DISPUTED", nil
	case unknown:
		return "UNKNOWN", nil
	case failed:
		return "FAILED", nil
	case pending:
		return "PENDING", nil
	case guarantee:
		return "APPROVED_GUARANTEE", nil
	}
	return "VERIFIED", nil
}

func (h *Handler) pmsStayExtensions(w http.ResponseWriter, r *http.Request) {
	t, s, e := h.pmsStayTarget(r)
	if e == nil {
		_, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "pms:read", "PROPERTY", s.PropertyID, false)
	}
	if e == nil {
		_, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "folios:read", "PROPERTY", s.PropertyID, false)
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x)-'command_digest'||jsonb_build_object('payers',(SELECT coalesce(jsonb_agg(jsonb_build_object('payer_kind',p.payer_kind,'financial_group_id',p.financial_group_id,'amount_minor',p.amount_minor,'responsibility',p.responsibility,'financial_state',g.state,'corporate_customer_id',p.corporate_customer_id) ORDER BY p.payer_kind DESC,p.id),'[]') FROM stay_extension_payers p JOIN reservation_financial_groups g ON g.organisation_id=p.organisation_id AND g.id=p.financial_group_id WHERE p.organisation_id=x.organisation_id AND p.extension_id=x.id)) FROM stay_extensions x WHERE x.organisation_id=$1 AND x.stay_id=$2 ORDER BY x.date_from,x.id LIMIT 100`, t.Organisation, s.ID)
	reply(w, r, 200, map[string]any{"items": rows, "stay_id": s.ID}, e)
}

func (h *Handler) pmsStayExtensionCollection(w http.ResponseWriter, r *http.Request) {
	var in PMSStayExtensionCollection
	if !decode(w, r, &in) {
		return
	}
	address, mailErr := mail.ParseAddress(in.Email)
	extensionID, idErr := ids.ParseV7(chi.URLParam(r, "extension_id"))
	if idErr != nil || in.PaymentPolicyID.ValidateV7() != nil || in.PayerWalletID.ValidateV7() != nil || !slices.Contains([]string{"HOTEL", "SHORT_LET"}, in.PaymentPolicyFamily) || len(in.PaymentPolicyVersion) < 1 || len(in.PaymentPolicyVersion) > 100 || len(in.Schedule) < 1 || len(in.Schedule) > 80 || len(in.ConsentReference) < 1 || len(in.ConsentReference) > 300 || len(in.LegalPartyReference) < 1 || len(in.LegalPartyReference) > 300 || mailErr != nil || address.Address != in.Email || len(in.Email) > 254 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Extension, Payment policy, payer wallet and explicit consent references required."))
		return
	}
	_, pre, e := h.pmsStayTarget(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:stay-extension-collection:"+extensionID.String(), in, "folios:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "folios:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		if _, _, e = h.authorize(ctx, tx, "reservations:write", "PROPERTY", pre.PropertyID, true); e != nil {
			return 0, nil, e
		}
		var group ids.ID
		var folioState string
		e = tx.QueryRow(ctx, `SELECT p.financial_group_id,f.state FROM stay_extension_payers p JOIN stay_extensions x ON x.organisation_id=p.organisation_id AND x.id=p.extension_id JOIN folios f ON f.organisation_id=x.organisation_id AND f.id=x.folio_id WHERE x.organisation_id=$1 AND x.id=$2 AND x.stay_id=$3 AND p.payer_kind='GUEST' AND p.responsibility='PLATFORM_COLLECT'`, t.Organisation, extensionID, pre.ID).Scan(&group, &folioState)
		if errors.Is(e, pgx.ErrNoRows) {
			return 0, nil, apperrors.Conflict("STAY_EXTENSION_COLLECTION_NOT_REQUIRED", "Only a platform-collected guest extension share is collected here; guarantees remain folio obligations.")
		}
		if e != nil {
			return 0, nil, e
		}
		if folioState == "CLOSED" || folioState == "DISPUTED" {
			return 0, nil, apperrors.Conflict("STAY_EXTENSION_COLLECTION_STATE", "A closed or disputed folio cannot start a new collection.")
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, pre.ReservationID)
		if e != nil {
			return 0, nil, e
		}
		if !slices.Contains(v.PolicySnapshot.AllowedPaymentPolicyIDs, in.PaymentPolicyID) {
			return 0, nil, apperrors.PermissionDenied("This Payment policy is not authorised by the accepted property contract.")
		}
		g, e := typedJSON[reservationFinancialGroup](ctx, tx, `SELECT to_jsonb(f) FROM reservation_financial_groups f WHERE organisation_id=$1 AND id=$2`, t.Organisation, group)
		if e != nil {
			return 0, nil, e
		}
		var existing bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_financial_requests WHERE organisation_id=$1 AND financial_group_id=$2 AND kind='COLLECTION')`, t.Organisation, group).Scan(&existing); e != nil {
			return 0, nil, e
		}
		if existing || g.State == "VERIFIED" || g.RequiredAmountMinor <= 0 {
			return 0, nil, apperrors.Conflict("RESERVATION_COLLECTION_EXISTS", "An existing collection must be read back; never create another charge to resolve uncertainty.")
		}
		if h.Auth.Service.Sealer == nil {
			return 0, nil, apperrors.Unavailable("Protected financial request storage unavailable.")
		}
		command := ReservationCollectionCommand{Version: v.Version, FinancialGroupID: group, PaymentPolicyID: in.PaymentPolicyID, PaymentPolicyVersion: in.PaymentPolicyVersion, PaymentPolicyFamily: in.PaymentPolicyFamily, PayerWalletID: in.PayerWalletID, PayerType: "GUEST", ConsentReference: in.ConsentReference, LegalPartyReference: in.LegalPartyReference, Email: in.Email, Schedule: in.Schedule, Reason: in.Reason}
		id := ids.New()
		plain := marshalCommercial(reservationCollectionPayload{Command: command, QuoteID: ids.New()})
		aad := []byte("business:reservation-finance:" + t.Organisation.String() + ":" + id.String())
		secret := protectedFinancialRequest{KeyID: h.Auth.Service.Sealer.KeyID(), Ciphertext: h.Auth.Service.Sealer.Seal(plain, aad)}
		p, _ := businessauth.PrincipalFrom(ctx)
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_financial_requests(id,organisation_id,property_id,reservation_id,financial_group_id,session_id,kind,request,request_hash,idempotency_key,created_by) VALUES($1,$2,$3,$4,$5,$6,'COLLECTION',$7,$8,$9,$10)`, id, t.Organisation, v.PropertyID, v.ID, group, p.SessionID, marshalCommercial(secret), hashCommercialPayload(plain), "stay-extension-collection:"+id.String(), t.Account); e != nil {
			return 0, nil, e
		}
		if e = audit(ctx, tx, t, "STAY_EXTENSION_COLLECTION_REQUESTED", "stay_extension", extensionID, map[string]any{"financial_group_id": group, "request_id": id, "payment_policy_id": in.PaymentPolicyID, "amount_minor": g.RequiredAmountMinor, "currency": g.Currency}); e != nil {
			return 0, nil, e
		}
		return 202, map[string]any{"id": id, "state": "QUEUED", "financial_group_id": group, "payment_authority": "payment-service", "collection_confirmed": false}, nil
	})
}

// stayExtensionCollectionAuthority is the in-house authority for a queued
// guest extension collection: the stay's folio must still be reviewable.
func (h *Handler) stayExtensionCollectionAuthority(ctx context.Context, tx pgx.Tx, t tenant, job reservationFinancialJob) (bool, error) {
	var folioState, reservationState string
	e := tx.QueryRow(ctx, `SELECT f.state,r.state FROM stay_extension_payers p JOIN stay_extensions x ON x.organisation_id=p.organisation_id AND x.id=p.extension_id JOIN folios f ON f.organisation_id=x.organisation_id AND f.id=x.folio_id JOIN reservations r ON r.organisation_id=x.organisation_id AND r.id=x.reservation_id WHERE p.organisation_id=$1 AND p.financial_group_id=$2 AND p.payer_kind='GUEST' AND p.responsibility='PLATFORM_COLLECT'`, job.OrganisationID, job.FinancialGroupID).Scan(&folioState, &reservationState)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return true, e
	}
	if _, _, e = h.authorize(ctx, tx, "folios:write", "PROPERTY", job.PropertyID, false); e != nil {
		return true, e
	}
	if folioState == "CLOSED" || folioState == "DISPUTED" || reservationState != "CHECKED_IN" && reservationState != "CHECKED_OUT" {
		return true, apperrors.Conflict("COLLECTION_AUTHORITY_STALE", "Extension folio or stay changed before collection; readback remains available.")
	}
	return true, nil
}

type earlyDepartureSegment struct {
	room   reservations.Room
	slot   int
	item   ids.ID
	nights []hospitality.DepartureNight
	unused int64
	count  int
	credit int64
	payers []PMSEarlyDepartureSegmentPayer
	weight []int64
}

// earlyDepartureTerms evaluates, without mutation, the frozen policy over the
// unit chain (accepted line slot plus in-house extensions) of an active stay.
func (h *Handler) earlyDepartureTerms(ctx context.Context, q database.Querier, t tenant, s hospitality.Stay, a hospitality.Assignment, v reservations.Reservation, f hospitality.Folio) (PMSEarlyDepartureTerms, []earlyDepartureSegment, error) {
	var out PMSEarlyDepartureTerms
	zone := v.PolicySnapshot.AcceptanceTimezone
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return out, nil, e
	}
	now := h.Auth.Service.Now().UTC()
	out.StayID, out.Currency = s.ID, f.Currency
	out.DepartureDay = now.In(loc).Format(time.DateOnly)
	out.Policy = hospitality.DeparturePolicy{CheckInLocal: v.PolicySnapshot.CheckInLocal, Timezone: zone, CancellationHours: v.PolicySnapshot.CancellationHours, CancellationPenaltyBPS: v.PolicySnapshot.CancellationPenaltyBPS, PolicyReference: v.PolicySnapshot.PolicyReference}
	line, e := hospitality.Load[reservations.Room](ctx, q, `SELECT to_jsonb(r) FROM reservation_rooms r WHERE organisation_id=$1 AND id=$2 AND state='ACTIVE'`, t.Organisation, a.ReservationRoomID)
	if e != nil {
		return out, nil, e
	}
	segments := []earlyDepartureSegment{{room: line, slot: a.Slot}}
	var item ids.ID
	if e = q.QueryRow(ctx, `SELECT id FROM folio_items WHERE organisation_id=$1 AND folio_id=$2 AND kind='ACCOMMODATION' AND (policy_snapshot->>'reservation_room_id')::uuid=$3 AND (policy_snapshot->>'slot')::integer=$4`, t.Organisation, f.ID, line.ID, a.Slot).Scan(&item); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return out, nil, apperrors.Conflict("EARLY_DEPARTURE_SOURCE_REVIEW_REQUIRED", "The original accepted accommodation posting for this unit is required.")
		}
		return out, nil, e
	}
	segments[0].item = item
	extensions, e := commercialJSONRows(ctx, q, `SELECT jsonb_build_object('room',to_jsonb(r),'item',x.folio_item_id) FROM stay_extensions x JOIN reservation_rooms r ON r.organisation_id=x.organisation_id AND r.id=x.extension_room_id WHERE x.organisation_id=$1 AND x.source_room_id=$2 AND x.slot=$3 ORDER BY x.date_from`, t.Organisation, line.ID, a.Slot)
	if e != nil {
		return out, nil, e
	}
	for _, raw := range extensions {
		var x struct {
			Room reservations.Room `json:"room"`
			Item ids.ID            `json:"item"`
		}
		if e = json.Unmarshal(raw, &x); e != nil {
			return out, nil, e
		}
		segments = append(segments, earlyDepartureSegment{room: x.Room, slot: 1, item: x.Item})
	}
	all := []hospitality.DepartureNight{}
	weights := make([]int64, len(segments))
	for i := range segments {
		seg := &segments[i]
		amounts, e := hospitality.SlotNightAmounts(seg.room.AmountMinor, seg.room.Currency, seg.room.Quantity, seg.slot, seg.room.Snapshot.Nights)
		if e != nil {
			return out, nil, apperrors.Conflict("EARLY_DEPARTURE_SOURCE_REVIEW_REQUIRED", "Accepted nightly amounts cannot be partitioned without repricing.")
		}
		for j, night := range seg.room.Snapshot.Nights {
			n := hospitality.DepartureNight{Day: night.Day, AmountMinor: amounts[j]}
			seg.nights = append(seg.nights, n)
			all = append(all, n)
			if n.Day >= out.DepartureDay {
				seg.unused += n.AmountMinor
				seg.count++
			}
		}
		weights[i] = seg.unused
	}
	out.Terms, e = hospitality.EvaluateEarlyDeparture(all, out.DepartureDay, now, out.Policy)
	if errors.Is(e, hospitality.ErrNotEarlyDeparture) {
		return out, nil, apperrors.Conflict("EARLY_DEPARTURE_NOT_APPLICABLE", "No unused accepted night remains; use the ordinary checkout.")
	}
	if e != nil {
		return out, nil, e
	}
	credits, e := hospitality.SplitByWeights(out.Terms.CreditMinor, f.Currency, weights)
	if e != nil {
		return out, nil, e
	}
	for i := range segments {
		seg := &segments[i]
		seg.credit = credits[i]
		var remaining int64
		if e = q.QueryRow(ctx, `SELECT (i.amount_minor+coalesce((SELECT sum(c.amount_minor) FROM folio_items c WHERE c.organisation_id=i.organisation_id AND c.adjusts_item_id=i.id),0))::bigint FROM folio_items i WHERE i.organisation_id=$1 AND i.id=$2`, t.Organisation, seg.item).Scan(&remaining); e != nil {
			return out, nil, e
		}
		if seg.credit > remaining {
			return out, nil, apperrors.Conflict("FOLIO_ADJUSTMENT_EXCEEDS_SOURCE", "Prior corrections leave less than the contractual credit; review the folio first.")
		}
		payers := []PMSEarlyDepartureSegmentPayer{}
		if i == 0 {
			g, e := typedJSON[reservationFinancialGroup](ctx, q, `SELECT to_jsonb(f) FROM reservation_financial_groups f WHERE organisation_id=$1 AND id=$2`, t.Organisation, seg.room.FinancialGroupID)
			if e != nil {
				return out, nil, e
			}
			payers = append(payers, PMSEarlyDepartureSegmentPayer{FinancialGroupID: g.ID, Responsibility: g.Responsibility})
			seg.weight = []int64{1}
		} else {
			rows, e := commercialJSONRows(ctx, q, `SELECT jsonb_build_object('group',p.financial_group_id,'responsibility',p.responsibility,'amount',p.amount_minor) FROM stay_extension_payers p JOIN stay_extensions x ON x.organisation_id=p.organisation_id AND x.id=p.extension_id WHERE x.organisation_id=$1 AND x.extension_room_id=$2 ORDER BY p.payer_kind DESC,p.id`, t.Organisation, seg.room.ID)
			if e != nil {
				return out, nil, e
			}
			for _, raw := range rows {
				var p struct {
					Group          ids.ID `json:"group"`
					Responsibility string `json:"responsibility"`
					Amount         int64  `json:"amount"`
				}
				if e = json.Unmarshal(raw, &p); e != nil {
					return out, nil, e
				}
				payers = append(payers, PMSEarlyDepartureSegmentPayer{FinancialGroupID: p.Group, Responsibility: p.Responsibility})
				seg.weight = append(seg.weight, p.Amount)
			}
		}
		shares, e := hospitality.SplitByWeights(seg.credit, f.Currency, seg.weight)
		if e != nil {
			return out, nil, e
		}
		for j := range payers {
			payers[j].CreditMinor = shares[j]
			payers[j].Collection, e = h.departureCollectionState(ctx, q, t.Organisation, payers[j].FinancialGroupID, payers[j].Responsibility)
			if e != nil {
				return out, nil, e
			}
		}
		seg.payers = payers
		out.Segments = append(out.Segments, PMSEarlyDepartureSegment{ReservationRoomID: seg.room.ID, Slot: seg.slot, FolioItemID: seg.item, UnusedNights: seg.count, UnusedMinor: seg.unused, CreditMinor: seg.credit, Payers: payers})
	}
	out.BalanceMinor, e = hospitality.Balance(ctx, q, t.Organisation, f.ID, now)
	if e != nil {
		return out, nil, e
	}
	out.OutstandingAfterMinor = out.BalanceMinor - out.Terms.CreditMinor
	exposure := out.OutstandingAfterMinor
	if exposure < 0 {
		exposure = -exposure
	}
	out.ApprovalExposureMinor = out.Terms.CreditMinor + exposure
	return out, segments, nil
}

// departureCollectionState classifies a payer group: COLLECTED means fresh
// authoritative Payment backing, NOT_COLLECTED means nothing was ever
// requested, GUARANTEE is a non-cash obligation and UNKNOWN needs readback.
func (h *Handler) departureCollectionState(ctx context.Context, q database.Querier, org, group ids.ID, responsibility string) (string, error) {
	if responsibility != "PLATFORM_COLLECT" {
		if responsibility == "CHANNEL_COLLECT" {
			return "EXTERNAL_UNKNOWN", nil
		}
		return "GUARANTEE", nil
	}
	g, e := typedJSON[reservationFinancialGroup](ctx, q, `SELECT to_jsonb(f) FROM reservation_financial_groups f WHERE organisation_id=$1 AND id=$2`, org, group)
	if e != nil {
		return "", e
	}
	if g.State == "VERIFIED" {
		if readErr := h.reservationFinancialEligibility(ctx, q, org, group); readErr == nil {
			return "COLLECTED", nil
		} else if !isReservationFinancialPending(readErr) {
			return "", readErr
		}
		return "UNKNOWN", nil
	}
	var started bool
	if e = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_financial_requests WHERE organisation_id=$1 AND financial_group_id=$2 AND kind='COLLECTION') OR EXISTS(SELECT 1 FROM reservation_financial_credits WHERE organisation_id=$1 AND target_group_id=$2)`, org, group).Scan(&started); e != nil {
		return "", e
	}
	if started {
		return "UNKNOWN", nil
	}
	return "NOT_COLLECTED", nil
}

func (h *Handler) pmsEarlyDepartureLoad(ctx context.Context, q database.Querier, t tenant, id ids.ID) (hospitality.Stay, hospitality.Assignment, reservations.Reservation, hospitality.Folio, error) {
	s, e := hospitality.GetStay(ctx, q, t.Organisation, id)
	if e != nil {
		return s, hospitality.Assignment{}, reservations.Reservation{}, hospitality.Folio{}, e
	}
	a, e := hospitality.GetAssignment(ctx, q, t.Organisation, s.AssignmentID)
	if e != nil {
		return s, a, reservations.Reservation{}, hospitality.Folio{}, e
	}
	v, e := reservations.Get(ctx, q, t.Organisation, s.ReservationID)
	if e != nil {
		return s, a, v, hospitality.Folio{}, e
	}
	f, e := hospitality.Load[hospitality.Folio](ctx, q, `SELECT to_jsonb(f) FROM folios f WHERE organisation_id=$1 AND stay_id=$2`, t.Organisation, s.ID)
	return s, a, v, f, e
}

func (h *Handler) pmsEarlyDeparturePreview(w http.ResponseWriter, r *http.Request) {
	t, pre, e := h.pmsStayTarget(r)
	if e == nil {
		_, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "pms:read", "PROPERTY", pre.PropertyID, false)
	}
	if e == nil {
		_, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "folios:read", "PROPERTY", pre.PropertyID, false)
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	s, a, v, f, e := h.pmsEarlyDepartureLoad(r.Context(), h.Auth.Service.Pool, t, pre.ID)
	if e == nil && (s.State != "ACTIVE" || a.State != "OCCUPIED" || v.State != "CHECKED_IN") {
		e = apperrors.Conflict("EARLY_DEPARTURE_STATE", "An active in-house stay is required.")
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	terms, _, e := h.earlyDepartureTerms(r.Context(), h.Auth.Service.Pool, t, s, a, v, f)
	w.Header().Set("Cache-Control", "no-store")
	reply(w, r, 200, terms, e)
}

func (h *Handler) pmsEarlyDepartureGet(w http.ResponseWriter, r *http.Request) {
	t, s, e := h.pmsStayTarget(r)
	if e == nil {
		_, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "pms:read", "PROPERTY", s.PropertyID, false)
	}
	if e == nil {
		_, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "folios:read", "PROPERTY", s.PropertyID, false)
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out, e := oneJSON(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(d)||jsonb_build_object('refund_requests',(SELECT coalesce(jsonb_agg(jsonb_build_object('id',j.id,'financial_group_id',j.financial_group_id,'state',j.state,'provider_reference',j.provider_reference) ORDER BY j.id),'[]') FROM reservation_financial_requests j WHERE j.organisation_id=d.organisation_id AND j.idempotency_key LIKE 'early-departure-refund:'||d.id::text||':%')) FROM stay_early_departures d WHERE d.organisation_id=$1 AND d.stay_id=$2`, t.Organisation, s.ID)
	reply(w, r, 200, out, e)
}

func (h *Handler) pmsEarlyDeparture(w http.ResponseWriter, r *http.Request) {
	var in PMSEarlyDepartureCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || in.ReservationVersion < 1 || in.FolioVersion < 1 || in.ExpectedCreditMinor < 0 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current stay, reservation and folio versions, expected evaluated terms and reason required."))
		return
	}
	_, pre, e := h.pmsStayTarget(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:early-departure:"+pre.ID.String(), in, "pms:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		out, e := h.executePMSEarlyDeparture(ctx, tx, pre, in)
		return 200, out, e
	})
}

func (h *Handler) executePMSEarlyDeparture(ctx context.Context, tx pgx.Tx, pre hospitality.Stay, in PMSEarlyDepartureCommand) (PMSEarlyDepartureResult, error) {
	var out PMSEarlyDepartureResult
	t, target, e := h.authorize(ctx, tx, "pms:write", "PROPERTY", pre.PropertyID, true)
	if e != nil {
		return out, e
	}
	for _, capability := range []string{"folios:write", "reservations:modify"} {
		if _, _, e = h.authorize(ctx, tx, capability, "PROPERTY", pre.PropertyID, true); e != nil {
			return out, e
		}
	}
	s, a, v, f, e := h.pmsEarlyDepartureLoad(ctx, tx, t, pre.ID)
	if e != nil {
		return out, e
	}
	if s.State != "ACTIVE" || s.Version != in.Version || a.State != "OCCUPIED" || v.State != "CHECKED_IN" || v.Version != in.ReservationVersion || f.Version != in.FolioVersion || !slices.Contains([]string{"OPEN", "SETTLEMENT_PENDING", "SETTLED"}, f.State) {
		return out, apperrors.Conflict("EARLY_DEPARTURE_STATE_VERSION", "Current active stay, occupied assignment, checked-in reservation and reviewable folio required.")
	}
	terms, segments, e := h.earlyDepartureTerms(ctx, tx, t, s, a, v, f)
	if e != nil {
		return out, e
	}
	if terms.Terms.CreditMinor != in.ExpectedCreditMinor || terms.OutstandingAfterMinor != in.ExpectedOutstandingMinor {
		return out, apperrors.Conflict("EARLY_DEPARTURE_TERMS_CHANGED", "Frozen policy evaluation or folio balance changed; review the current terms again.")
	}
	op := in
	op.ApprovalID = ids.Nil
	exposure := terms.ApprovalExposureMinor
	if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_EARLY_DEPARTURE", target.Scope, in.ApprovalID, map[string]any{"stay_id": s.ID, "command": op}, "", &exposure, f.Currency); e != nil {
		return out, e
	}
	now := h.Auth.Service.Now().UTC()
	day, e := h.pmsDay(ctx, tx, t, v.PropertyID, true)
	if e != nil {
		return out, e
	}
	departure := ids.New()
	// Unused unit nights return to canonical stock through the release ledger.
	out.ReleasedNights = []reservations.UnitNight{}
	for _, seg := range segments {
		if seg.count == 0 {
			continue
		}
		kind := "EARLY_DEPARTURE"
		if seg.room.Quantity > 1 {
			kind = "PARTIAL_CHECKOUT"
		}
		from := max(terms.DepartureDay, seg.room.DateFrom)
		release := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_unit_releases(id,organisation_id,property_id,reservation_id,room_id,slot,stay_id,kind,date_from,date_to,nights,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::date,$10::date,$11,$12,$13)`, release, t.Organisation, v.PropertyID, v.ID, seg.room.ID, seg.slot, s.ID, kind, from, seg.room.DateTo, seg.count, in.Reason, t.Account); e != nil {
			return out, e
		}
		nights, e := reservations.ReleaseUnitNights(ctx, tx, t.Organisation, t.Account, release, seg.room, seg.slot, from, now)
		if e != nil {
			return out, e
		}
		if len(nights) != seg.count {
			return out, apperrors.Conflict("RESERVATION_STOCK_RECONCILIATION_REQUIRED", "Unused unit nights differ from their committed canonical provenance.")
		}
		out.ReleasedNights = append(out.ReleasedNights, nights...)
		if e = h.pmsCommit(ctx, tx, t, v.PropertyID, release, "reservation_unit_release", "RESERVATION_UNIT_NIGHTS_RELEASED", "reservation.unit_nights.released.v1", 1, map[string]any{"reservation_id": v.ID, "reservation_room_id": seg.room.ID, "slot": seg.slot, "stay_id": s.ID, "kind": kind, "date_from": from, "date_to": seg.room.DateTo, "nights": nights, "early_departure_id": departure, "reason": in.Reason}); e != nil {
			return out, e
		}
	}
	// Revised accommodation obligation: compensating credits per accepted line.
	refunds := map[ids.ID]int64{}
	unknown, external := false, false
	collected := int64(0)
	effects := []map[string]any{}
	for _, seg := range segments {
		if seg.credit > 0 {
			if _, e = tx.Exec(ctx, `INSERT INTO folio_items(id,organisation_id,property_id,folio_id,kind,source_reference,quantity,amount_minor,currency,policy_snapshot,adjusts_item_id,approval_id,reason,created_by,business_day_id) VALUES($1,$2,$3,$4,'ADJUSTMENT',$5,1,$6,$7,$8,$9,$10,$11,$12,$13)`, ids.New(), t.Organisation, v.PropertyID, f.ID, "early-departure:"+departure.String()+":"+seg.room.ID.String(), -seg.credit, f.Currency, marshalCommercial(map[string]any{"early_departure_id": departure, "policy": terms.Policy, "terms": terms.Terms, "reservation_room_id": seg.room.ID, "slot": seg.slot, "unused_minor": seg.unused}), seg.item, in.ApprovalID, in.Reason, t.Account, day.ID); e != nil {
				return out, e
			}
		}
		for _, p := range seg.payers {
			switch p.Collection {
			case "COLLECTED":
				refunds[p.FinancialGroupID] += p.CreditMinor
				collected += p.CreditMinor
			case "UNKNOWN":
				unknown = unknown || p.CreditMinor > 0
			case "EXTERNAL_UNKNOWN":
				external = external || p.CreditMinor > 0
			}
		}
		effects = append(effects, map[string]any{"reservation_room_id": seg.room.ID, "slot": seg.slot, "folio_item_id": seg.item, "unused_nights": seg.count, "unused_minor": seg.unused, "credit_minor": seg.credit, "payers": seg.payers, "actual_refund_executed": false})
	}
	state := "NO_CREDIT"
	var collectedValue, refundable any
	switch {
	case unknown:
		state = "READBACK_REQUIRED"
	case external:
		state = "EXTERNAL_COLLECTION_UNKNOWN"
	case collected > 0:
		state = "REFUND_PENDING_EXTERNAL"
	case terms.Terms.CreditMinor > 0:
		state = "FOLIO_CREDIT_ONLY"
	}
	if state != "READBACK_REQUIRED" && state != "EXTERNAL_COLLECTION_UNKNOWN" {
		collectedValue, refundable = collected, collected
		value := collected
		out.RefundableMinor = &value
	}
	if _, e = tx.Exec(ctx, `UPDATE reservations SET version=version+1,reason=$3,updated_at=$4 WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, in.Reason, now); e != nil {
		return out, e
	}
	v, e = reservations.Get(ctx, tx, t.Organisation, v.ID)
	if e != nil {
		return out, e
	}
	if e = h.reservationCommit(ctx, tx, t, v, "EARLY_DEPARTURE", "reservation.modified.v1", in.Reason, map[string]any{"stay_id": s.ID, "early_departure_id": departure, "departure_day": terms.DepartureDay, "terms": terms.Terms, "released_nights": out.ReleasedNights, "financial_state": state, "approval_id": in.ApprovalID}); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO stay_early_departures(id,organisation_id,property_id,reservation_id,stay_id,folio_id,departure_day,departed_at,unused_nights,currency,unused_minor,penalty_minor,credit_minor,platform_collected_minor,refundable_minor,financial_state,policy_snapshot,line_effects,approval_id,reservation_version,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7::date,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`, departure, t.Organisation, v.PropertyID, v.ID, s.ID, f.ID, terms.DepartureDay, now, terms.Terms.UnusedNights, f.Currency, terms.Terms.UnusedMinor, terms.Terms.PenaltyMinor, terms.Terms.CreditMinor, collectedValue, refundable, state, marshalCommercial(map[string]any{"policy": terms.Policy, "reservation_policy": v.PolicySnapshot, "terms": terms.Terms}), marshalCommercial(effects), in.ApprovalID, v.Version, in.Reason, t.Account); e != nil {
		return out, e
	}
	groups := make([]ids.ID, 0, len(refunds))
	for g := range refunds {
		groups = append(groups, g)
	}
	slices.SortFunc(groups, func(a, b ids.ID) int {
		switch {
		case a.String() < b.String():
			return -1
		case a == b:
			return 0
		}
		return 1
	})
	out.RefundRequestIDs = []ids.ID{}
	for _, g := range groups {
		if refunds[g] <= 0 {
			continue
		}
		id := ids.New()
		request := marshalCommercial(map[string]any{"early_departure_id": departure, "financial_group_id": g, "amount_minor": refunds[g], "currency": f.Currency, "approval_state": "REQUIRES_INDEPENDENT_FINANCIAL_APPROVAL", "submission_contract": "BUSINESS_AUTHORISED_REFUND_OPERATION_UNAVAILABLE", "actual_refund_executed": false})
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_financial_requests(id,organisation_id,property_id,reservation_id,financial_group_id,kind,state,request,request_hash,idempotency_key,last_error,created_by) VALUES($1,$2,$3,$4,$5,'REFUND','DEPENDENCY_BLOCKED',$6,$7,$8,'PAYMENT_REFUND_REQUIRES_AUTHORISED_ADMIN_HANDOFF',$9)`, id, t.Organisation, v.PropertyID, v.ID, g, request, hashCommercialPayload(request), "early-departure-refund:"+departure.String()+":"+g.String(), t.Account); e != nil {
			return out, e
		}
		out.RefundRequestIDs = append(out.RefundRequestIDs, id)
	}
	if e = h.pmsCommit(ctx, tx, t, v.PropertyID, departure, "stay_early_departure", "STAY_EARLY_DEPARTURE_RECORDED", "hospitality.stay.early_departure.recorded.v1", 1, map[string]any{"stay_id": s.ID, "reservation_id": v.ID, "departure_day": terms.DepartureDay, "unused_nights": terms.Terms.UnusedNights, "penalty_applied": terms.Terms.PenaltyApplied, "financial_state": state, "refund_executed": false, "approval_id": in.ApprovalID, "reason": in.Reason}); e != nil {
		return out, e
	}
	// The folio version increases once for the credit postings; departure then
	// reads the current balance and fiscal readiness.
	if terms.Terms.CreditMinor > 0 {
		if _, e = tx.Exec(ctx, `UPDATE folios SET state=CASE WHEN state='SETTLED' THEN 'OPEN' ELSE state END,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, f.ID); e != nil {
			return out, e
		}
		if e = h.pmsCommit(ctx, tx, t, v.PropertyID, f.ID, "folio", "FOLIO_EARLY_DEPARTURE_CREDITED", "hospitality.folio.updated.v1", f.Version+1, map[string]any{"early_departure_id": departure, "credit_minor": terms.Terms.CreditMinor, "approval_id": in.ApprovalID, "reason": in.Reason}); e != nil {
			return out, e
		}
		if f, e = hospitality.GetFolio(ctx, tx, t.Organisation, f.ID); e != nil {
			return out, e
		}
	}
	balance, e := hospitality.Balance(ctx, tx, t.Organisation, f.ID, now)
	if e != nil {
		return out, e
	}
	if balance != terms.OutstandingAfterMinor {
		return out, apperrors.Conflict("EARLY_DEPARTURE_TERMS_CHANGED", "The folio balance changed during the departure.")
	}
	fiscalReady, e := hospitality.FiscalReady(ctx, tx, t.Organisation, f.ID)
	if e != nil {
		return out, e
	}
	folioState := "CLOSED"
	if balance != 0 || !fiscalReady {
		folioState = "SETTLEMENT_PENDING"
	}
	out.Departure, e = h.completePMSDeparture(ctx, tx, t, s, a, v, f, balance, folioState, in.ApprovalID, in.Reason)
	if e != nil {
		return out, e
	}
	out.EarlyDepartureID, out.Terms, out.FinancialState, out.RefundExecuted = departure, terms, state, false
	return out, nil
}

// reservationUnitReleases reports per-date conservation for every line with
// released unit nights: accepted = remaining committed + released.
func (h *Handler) reservationUnitReleases(w http.ResponseWriter, r *http.Request) {
	t, v, e := h.reservationTarget(r, "reservations:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	releases, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(u) FROM reservation_unit_releases u WHERE organisation_id=$1 AND reservation_id=$2 ORDER BY created_at,id LIMIT 500`, t.Organisation, v.ID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	days, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT jsonb_build_object('reservation_room_id',d.room_id,'day',d.day,'accepted_quantity',rr.quantity,'remaining_quantity',CASE WHEN d.released_at IS NULL THEN d.quantity ELSE 0 END,'released_unit_nights',(SELECT count(*) FROM reservation_unit_release_days u WHERE u.organisation_id=d.organisation_id AND u.room_id=d.room_id AND u.day=d.day),'conserved',rr.quantity=(CASE WHEN d.released_at IS NULL THEN d.quantity ELSE 0 END)+(SELECT count(*) FROM reservation_unit_release_days u WHERE u.organisation_id=d.organisation_id AND u.room_id=d.room_id AND u.day=d.day)) FROM reservation_inventory_days d JOIN reservation_rooms rr ON rr.organisation_id=d.organisation_id AND rr.id=d.room_id WHERE d.organisation_id=$1 AND rr.reservation_id=$2 AND EXISTS(SELECT 1 FROM reservation_unit_releases u WHERE u.organisation_id=d.organisation_id AND u.room_id=d.room_id) ORDER BY d.room_id,d.day LIMIT 2000`, t.Organisation, v.ID)
	reply(w, r, 200, map[string]any{"reservation_id": v.ID, "releases": releases, "days": days}, e)
}
