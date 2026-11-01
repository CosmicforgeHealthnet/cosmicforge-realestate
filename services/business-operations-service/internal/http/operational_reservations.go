package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"slices"
	"strings"
	"time"
)

type ReservationPolicyView struct {
	ID             ids.ID              `json:"id"`
	OrganisationID ids.ID              `json:"organisation_id"`
	PropertyID     ids.ID              `json:"property_id"`
	Definition     reservations.Policy `json:"definition"`
	State          string              `json:"state"`
	Version        int64               `json:"version"`
	ApprovalID     ids.ID              `json:"approval_id"`
	CreatedBy      ids.ID              `json:"created_by"`
	EffectiveAt    time.Time           `json:"effective_at"`
	ExpiresAt      *time.Time          `json:"expires_at,omitempty"`
}
type ReservationPolicyCommand struct {
	PropertyID      ids.ID              `json:"property_id"`
	CurrentPolicyID *ids.ID             `json:"current_policy_id,omitempty"`
	ExpectedVersion int64               `json:"expected_version"`
	Definition      reservations.Policy `json:"definition"`
	EffectiveAt     time.Time           `json:"effective_at"`
	ExpiresAt       *time.Time          `json:"expires_at,omitempty"`
	ApprovalID      ids.ID              `json:"approval_id"`
	Reason          string              `json:"reason"`
}

func (h *Handler) registerReservations(r chi.Router) {
	r.Get("/reservation-policies", h.reservationPolicies)
	r.Post("/reservation-policies", h.reservationPolicyCreate)
	r.Get("/reservations", h.reservationList)
	r.Post("/reservations", h.reservationCreate)
	r.Get("/reservations/{id}", h.reservationGet)
	r.Get("/reservations/{id}/history", h.reservationHistory)
	r.Get("/reservations/{id}/cancellation-obligations", h.reservationCancellationObligations)
	r.Get("/reservation-command-center", h.reservationCommandCenter)
	h.registerReservationCommands(r)
	h.registerReservationPayments(r)
	h.registerReservationWaitlist(r)
	h.registerReservationWaitlistRejection(r)
	h.registerReservationAmendments(r)
	h.registerReservationLineage(r)
	h.registerReservationConflicts(r)
}
func (h *Handler) reservationPolicies(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "reservation_policies", "reservations:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		return typedJSON[ReservationPolicyView](ctx, h.Auth.Service.Pool, `SELECT to_jsonb(p) FROM reservation_policies p WHERE organisation_id=$1 AND id=$2`, org, id)
	})
}
func (h *Handler) reservationPolicyCreate(w http.ResponseWriter, r *http.Request) {
	var in ReservationPolicyCommand
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.Definition.AcceptanceTimezone != "" || in.Definition.Validate() != nil || !validStaffReason(in.Reason) || in.EffectiveAt.IsZero() || in.ExpiresAt != nil && !in.ExpiresAt.After(in.EffectiveAt) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid bounded reservation policy, effective dates and reason required."))
		return
	}
	h.execute(w, r, "reservation:policy:create", in, "reservations:approve", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "reservations:approve", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		var current ids.ID
		var version int64
		e = tx.QueryRow(ctx, `SELECT id,version FROM reservation_policies WHERE organisation_id=$1 AND property_id=$2 AND state='ACTIVE' FOR UPDATE`, t.Organisation, in.PropertyID).Scan(&current, &version)
		if errors.Is(e, pgx.ErrNoRows) {
			if in.CurrentPolicyID != nil || in.ExpectedVersion != 0 {
				return 0, nil, apperrors.Conflict("POLICY_VERSION_CHANGED", "No active reservation policy matches this review.")
			}
		} else if e != nil {
			return 0, nil, e
		} else if in.CurrentPolicyID == nil || *in.CurrentPolicyID != current || in.ExpectedVersion != version {
			return 0, nil, apperrors.Conflict("POLICY_VERSION_CHANGED", "Current reservation policy changed.")
		}
		command := in
		command.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESERVATION_POLICY_CHANGE", target.Scope, in.ApprovalID, map[string]any{"command": command}, "", nil, ""); e != nil {
			return 0, nil, e
		}
		if !current.IsZero() {
			if _, e = tx.Exec(ctx, `UPDATE reservation_policies SET state='SUSPENDED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, current); e != nil {
				return 0, nil, e
			}
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_policies(id,organisation_id,property_id,definition,approval_id,state,effective_at,expires_at,created_by) VALUES($1,$2,$3,$4,$5,'ACTIVE',$6,$7,$8)`, id, t.Organisation, in.PropertyID, marshalCommercial(in.Definition), in.ApprovalID, in.EffectiveAt, in.ExpiresAt, t.Account); e != nil {
			return 0, nil, e
		}
		value, e := h.reservationPolicyByID(ctx, tx, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "reservation_policy", "RESERVATION_POLICY_CREATED", "", 1, map[string]any{"policy_id": id, "policy_reference": in.Definition.PolicyReference, "approval_id": in.ApprovalID, "reason": in.Reason})
		return 201, value, e
	})
}
func (h *Handler) reservationPolicyByID(ctx context.Context, q database.Querier, org, id ids.ID) (ReservationPolicyView, error) {
	return typedJSON[ReservationPolicyView](ctx, q, `SELECT to_jsonb(p) FROM reservation_policies p WHERE organisation_id=$1 AND id=$2`, org, id)
}
func (h *Handler) currentReservationPolicy(ctx context.Context, q database.Querier, org, property ids.ID) (ReservationPolicyView, error) {
	v, e := typedJSON[ReservationPolicyView](ctx, q, `SELECT to_jsonb(p) FROM reservation_policies p WHERE organisation_id=$1 AND property_id=$2 AND state='ACTIVE' AND effective_at<=$3 AND (expires_at IS NULL OR expires_at>$3)`, org, property, h.Auth.Service.Now().UTC())
	if errors.Is(e, pgx.ErrNoRows) {
		return v, apperrors.Conflict("RESERVATION_POLICY_REQUIRED", "An independently approved effective reservation policy is required.")
	}
	return v, e
}
func (h *Handler) reservationCreate(w http.ResponseWriter, r *http.Request) {
	var in reservations.Create
	if !decode(w, r, &in) {
		return
	}
	if in.State == "" {
		in.State = "PENDING_PAYMENT"
	}
	if in.PaymentResponsibility == "" {
		in.PaymentResponsibility = "PLATFORM_COLLECT"
	}
	if e := in.Validate(); e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Source == "MARKETPLACE" || in.Source == "OTA" {
		reply(w, r, 0, nil, apperrors.PermissionDenied("External reservation sources require their authenticated internal intake contract."))
		return
	}
	h.execute(w, r, "reservation:create", in, "reservations:write", "PROPERTY", in.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reservations:write", "PROPERTY", in.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		v, e := h.createReservationTx(ctx, tx, t, in, false)
		return 201, v, e
	})
}
func (h *Handler) createReservationTx(ctx context.Context, tx pgx.Tx, t tenant, in reservations.Create, sourceAuthenticated bool) (reservations.Detail, error) {
	var out reservations.Detail
	if e := in.Validate(); e != nil {
		return out, e
	}
	if !sourceAuthenticated && (in.Source == "MARKETPLACE" || in.Source == "OTA") {
		return out, apperrors.PermissionDenied("Signed source authority required.")
	}
	if e := h.commercialGate(ctx, tx, t, in.PropertyID); e != nil {
		return out, e
	}
	policy, e := h.currentReservationPolicy(ctx, tx, t.Organisation, in.PropertyID)
	if e != nil {
		return out, e
	}
	if e = tx.QueryRow(ctx, `SELECT timezone FROM inventory_nodes WHERE organisation_id=$1 AND id=$2`, t.Organisation, in.PropertyID).Scan(&policy.Definition.AcceptanceTimezone); e != nil {
		return out, e
	}
	if !slices.Contains(policy.Definition.AllowedSources, in.Source) {
		return out, apperrors.PermissionDenied("Reservation source is disabled by the current property policy.")
	}
	quotes := []revenue.QuoteSnapshot{}
	seenHolds := map[ids.ID]bool{}
	amount := int64(0)
	currency := ""
	from, to := "", ""
	now := h.Auth.Service.Now().UTC()
	deadline := now.Add(time.Duration(min(policy.Definition.HoldSeconds, 900)) * time.Second)
	for _, command := range in.Rooms {
		q, e := loadCommercialQuote(ctx, tx, t.Organisation, command.QuoteID)
		if e != nil {
			return out, e
		}
		holdCommand := inventory.HoldCommand{ProductID: command.ProductID, ProductVersion: command.ProductVersion, PoolVersion: command.PoolVersion, QuoteID: command.QuoteID, DateFrom: command.DateFrom, DateTo: command.DateTo, Quantity: command.Quantity, Channel: in.Channel, DurationSeconds: min(policy.Definition.HoldSeconds, 900)}
		if e = h.validateHoldQuote(ctx, tx, t, holdCommand); e != nil {
			return out, e
		}
		if q.PropertyID != in.PropertyID {
			return out, apperrors.PermissionDenied("A reservation cannot mix properties.")
		}
		if currency != "" && currency != q.Currency {
			return out, apperrors.Conflict("RESERVATION_CURRENCY_CONFLICT", "All accepted room prices must use one explicit currency.")
		}
		currency = q.Currency
		amount, e = addMinor(amount, q.AmountMinor, currency)
		if e != nil {
			return out, e
		}
		if from == "" || q.DateFrom < from {
			from = q.DateFrom
		}
		if q.DateTo > to {
			to = q.DateTo
		}
		if command.HoldID != nil {
			if seenHolds[*command.HoldID] {
				return out, apperrors.InvalidRequest("A hold cannot satisfy more than one reservation room line.")
			}
			seenHolds[*command.HoldID] = true
			hold, e := inventory.GetHold(ctx, tx, t.Organisation, *command.HoldID)
			if e != nil {
				return out, e
			}
			if hold.Version != command.HoldVersion || hold.State != "ACTIVE" || !now.Before(hold.ExpiresAt) || hold.QuoteID != q.QuoteID || hold.Quantity != q.Quantity || hold.Channel != in.Channel {
				return out, apperrors.Conflict("RESERVATION_HOLD_STALE", "The supplied hold does not match this current accepted quote.")
			}
			if hold.ExpiresAt.Before(deadline) {
				deadline = hold.ExpiresAt
			}
			var bound bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_holds WHERE organisation_id=$1 AND hold_id=$2)`, t.Organisation, hold.ID).Scan(&bound); e != nil {
				return out, e
			}
			if bound {
				return out, apperrors.Conflict("RESERVATION_HOLD_ALREADY_BOUND", "This hold already belongs to a canonical reservation.")
			}
		}
		quotes = append(quotes, q)
	}
	guarantee := in.PaymentResponsibility != "PLATFORM_COLLECT"
	if guarantee && (!slices.Contains(policy.Definition.AllowedGuarantees, in.PaymentResponsibility) || policy.Definition.MaxGuaranteeMinor == 0 || amount > policy.Definition.MaxGuaranteeMinor) {
		return out, apperrors.PermissionDenied("Unpaid guarantee responsibility exceeds the independently approved property policy.")
	}
	operation := in
	operation.ApprovalID = ids.Nil
	operation.GuaranteeApprovalID = ids.Nil
	target, e := resolve(ctx, tx, t.Organisation, "PROPERTY", in.PropertyID)
	if e != nil {
		return out, e
	}
	if guarantee {
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESERVATION_GUARANTEE", target.Scope, in.GuaranteeApprovalID, map[string]any{"command": operation}, in.Channel, &amount, currency); e != nil {
			return out, e
		}
	}
	if policy.Definition.RequireApproval && in.State == "CONFIRMED" {
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESERVATION_CREATE", target.Scope, in.ApprovalID, map[string]any{"command": operation}, in.Channel, &amount, currency); e != nil {
			return out, e
		}
	}
	if in.State == "CONFIRMED" && !guarantee {
		return out, apperrors.Conflict("VERIFIED_PAYMENT_REQUIRED", "Create a pending reservation, request collection, and confirm only after authoritative financial readback.")
	}
	if in.State == "CONFIRMED" && policy.Definition.RequireRiskClear {
		return out, apperrors.Conflict("RESERVATION_RISK_REVIEW_REQUIRED", "Create a pending reservation for an independently authorised risk review before confirmation.")
	}
	if in.Source == "CORPORATE" && (in.CorporateID == nil || in.CorporateContractID == nil) {
		return out, apperrors.InvalidRequest("Corporate reservations require the current authorised corporate customer and contract.")
	}
	// Corporate bindings are supplied only after their owning Section 11 contract is installed.
	if in.CorporateID != nil {
		return out, apperrors.Unavailable("Corporate contract validation is not yet composed.")
	}
	id, financialID := ids.New(), ids.New()
	state := in.State
	if policy.Definition.RequireApproval && state != "INQUIRY" && state != "CONFIRMED" {
		state = "PENDING_APPROVAL"
	}
	financial := "PENDING"
	if guarantee {
		financial = "APPROVED_GUARANTEE"
	}
	group := in.GroupID
	if group == nil && in.GroupName != "" {
		next := ids.New()
		group = &next
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_groups(id,organisation_id,property_id,name,source_reference,created_by) VALUES($1,$2,$3,$4,$5,$6)`, next, t.Organisation, in.PropertyID, in.GroupName, in.SourceReference, t.Account); e != nil {
			return out, e
		}
	}
	if group != nil {
		var valid bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_groups WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND state='ACTIVE')`, t.Organisation, in.PropertyID, *group).Scan(&valid); e != nil {
			return out, e
		}
		if !valid {
			return out, apperrors.PermissionDenied("Active group scope does not match this property.")
		}
	}
	if _, e = tx.Exec(ctx, `INSERT INTO reservations(id,organisation_id,property_id,source,channel,source_reference,state,group_id,policy_id,policy_snapshot,payment_responsibility,financial_state,amount_minor,currency,date_from,date_to,hold_expires_at,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::date,$16::date,$17,$18,$19)`, id, t.Organisation, in.PropertyID, in.Source, in.Channel, in.SourceReference, initialReservationState(state), group, policy.ID, marshalCommercial(policy.Definition), in.PaymentResponsibility, financial, amount, currency, from, to, deadline, in.Reason, t.Account); e != nil {
		return out, e
	}
	var guaranteeApproval any
	if guarantee {
		guaranteeApproval = in.GuaranteeApprovalID
	}
	if _, e = tx.Exec(ctx, `INSERT INTO reservation_financial_groups(id,organisation_id,property_id,reservation_id,amount_minor,required_amount_minor,currency,policy_reference,agreement_version,state,responsibility,guarantee_approval_id,created_by) VALUES($1,$2,$3,$4,$5,$5,$6,$7,1,$8,$9,$10,$11)`, financialID, t.Organisation, in.PropertyID, id, amount, currency, policy.Definition.PolicyReference, financial, in.PaymentResponsibility, guaranteeApproval, t.Account); e != nil {
		return out, e
	}
	for i, command := range in.Rooms {
		if _, e = h.acceptReservationRoom(ctx, tx, t, id, financialID, command, quotes[i], in.Channel, state, policy.Definition, now); e != nil {
			return out, e
		}
	}

	for _, g := range in.Guests {
		if g.DocumentReferences == nil {
			g.DocumentReferences = []ids.ID{}
		}
		if g.RoomID != nil {
			var belongs bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_rooms WHERE organisation_id=$1 AND reservation_id=$2 AND id=$3)`, t.Organisation, id, *g.RoomID).Scan(&belongs); e != nil {
				return out, e
			}
			if !belongs {
				return out, apperrors.InvalidRequest("Guest room must belong to this reservation.")
			}
		}
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_guests(id,organisation_id,property_id,reservation_id,room_id,guest_id,source_kind,source_reference,is_primary,registration_reference,document_references,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, ids.New(), t.Organisation, in.PropertyID, id, g.RoomID, g.GuestID, g.SourceKind, g.SourceReference, g.IsPrimary, g.RegistrationReference, g.DocumentReferences, t.Account); e != nil {
			return out, e
		}
	}
	if state == "CONFIRMED" {
		initial, e := reservations.Get(ctx, tx, t.Organisation, id)
		if e != nil {
			return out, e
		}
		if e = h.reservationCommit(ctx, tx, t, initial, "RESERVATION_CREATED", "reservation.created.v1", in.Reason, nil); e != nil {
			return out, e
		}
		rooms, e := reservations.Rooms(ctx, tx, t.Organisation, id, true)
		if e != nil {
			return out, e
		}
		for _, room := range rooms {
			if e = reservations.CommitHold(ctx, tx, t.Organisation, t.Account, room, now); e != nil {
				return out, e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE reservations SET state='CONFIRMED',confirmed_at=$3,hold_expires_at=NULL,updated_at=$3,version=version+1 WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, now); e != nil {
			return out, e
		}
	}
	value, e := reservations.Get(ctx, tx, t.Organisation, id)
	if e != nil {
		return out, e
	}
	action, event := "RESERVATION_CREATED", "reservation.created.v1"
	if state == "CONFIRMED" {
		action, event = "CONFIRMED", "reservation.modified.v1"
	}
	if e = h.reservationCommit(ctx, tx, t, value, action, event, in.Reason, nil); e != nil {
		return out, e
	}
	return h.reservationDetail(ctx, tx, t.Organisation, id)
}
func (h *Handler) reservationDetail(ctx context.Context, q database.Querier, org, id ids.ID) (reservations.Detail, error) {
	var out reservations.Detail
	var e error
	out.Reservation, e = reservations.Get(ctx, q, org, id)
	if e != nil {
		return out, e
	}
	out.Rooms, e = reservations.Rooms(ctx, q, org, id, false)
	if e != nil {
		return out, e
	}
	raw, e := commercialJSONRows(ctx, q, `SELECT to_jsonb(g)-'document_references' FROM reservation_guests g WHERE organisation_id=$1 AND reservation_id=$2 ORDER BY created_at,id`, org, id)
	if e != nil {
		return out, e
	}
	out.Guests = []reservations.GuestCommand{}
	for _, v := range raw {
		var g reservations.GuestCommand
		if e = json.Unmarshal(v, &g); e != nil {
			return out, e
		}
		out.Guests = append(out.Guests, g)
	}
	financialRows, e := commercialJSONRows(ctx, q, `SELECT to_jsonb(f)||jsonb_build_object('allocated_amount_minor',(SELECT coalesce(sum(r.amount_minor),0) FROM reservation_rooms r WHERE r.organisation_id=$1 AND r.reservation_id=$2 AND r.financial_group_id=f.id AND r.state='ACTIVE'),'allocation_relationship',CASE WHEN EXISTS(SELECT 1 FROM reservation_rooms r WHERE r.organisation_id=$1 AND r.reservation_id=$2 AND r.financial_group_id=f.id AND r.state='ACTIVE') THEN 'ACTIVE_ALLOCATION' ELSE 'HISTORICAL_BACKING' END,'credits_applied_minor',(SELECT coalesce(sum(c.amount_minor),0) FROM reservation_financial_credits c WHERE c.organisation_id=$1 AND c.target_group_id=f.id),'credits_forwarded_minor',(SELECT coalesce(sum(c.amount_minor),0) FROM reservation_financial_credits c WHERE c.organisation_id=$1 AND c.source_group_id=f.id)) FROM reservation_financial_groups f WHERE organisation_id=$1 AND id IN (SELECT financial_group_id FROM reservation_rooms WHERE organisation_id=$1 AND reservation_id=$2) ORDER BY created_at,id`, org, id)
	if e != nil {
		return out, e
	}
	out.FinancialGroups = []reservations.FinancialGroupView{}
	for _, raw := range financialRows {
		var group reservations.FinancialGroupView
		if e = json.Unmarshal(raw, &group); e != nil {
			return out, e
		}
		out.FinancialGroups = append(out.FinancialGroups, group)
	}

	out.History = []json.RawMessage{}
	return out, nil
}
func (h *Handler) reservationTarget(r *http.Request, cap string, mfa bool) (tenant, reservations.Reservation, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, reservations.Reservation{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, reservations.Reservation{}, e
	}
	v, e := reservations.Get(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e == nil {
		t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, cap, "PROPERTY", v.PropertyID, mfa)
	}
	return t, v, e
}
func (h *Handler) reservationGet(w http.ResponseWriter, r *http.Request) {
	t, v, e := h.reservationTarget(r, "reservations:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out, e := h.reservationDetail(r.Context(), h.Auth.Service.Pool, t.Organisation, v.ID)
	reply(w, r, 200, out, e)
}
func (h *Handler) reservationHistory(w http.ResponseWriter, r *http.Request) {
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
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(v)-'snapshot' FROM reservation_versions v WHERE organisation_id=$1 AND reservation_id=$2 ORDER BY reservation_version DESC LIMIT $3 OFFSET $4`, t.Organisation, v.ID, limit, offset)
	reply(w, r, 200, map[string]any{"items": rows, "limit": limit, "offset": offset}, e)
}
func (h *Handler) reservationList(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	property := r.URL.Query().Get("property_id")
	branch := r.URL.Query().Get("branch_id")
	from, to := r.URL.Query().Get("date_from"), r.URL.Query().Get("date_to")
	for _, id := range []string{property, branch} {
		if id != "" {
			if _, e = ids.ParseV7(id); e != nil {
				reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid property/branch filter."))
				return
			}
		}
	}
	for _, date := range []string{from, to} {
		if date != "" {
			if _, e = time.Parse(time.DateOnly, date); e != nil {
				reply(w, r, 0, nil, apperrors.InvalidRequest("Filters use civil dates."))
				return
			}
		}
	}
	state, source, channel, search := r.URL.Query().Get("state"), r.URL.Query().Get("source"), r.URL.Query().Get("channel"), r.URL.Query().Get("search")
	if len(search) > 200 || len(state) > 40 || len(source) > 40 || !inventory.ValidChannel(channel) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Bounded valid reservation filters required."))
		return
	}
	properties, e := h.operationalProperties(r.Context(), h.Auth.Service.Pool, t, "reservations:read", property, branch)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(r) FROM reservations r JOIN inventory_nodes n ON n.organisation_id=r.organisation_id AND n.id=r.property_id WHERE r.organisation_id=$1 AND r.property_id=ANY($12::uuid[]) AND ($2='' OR r.property_id::text=$2) AND ($3='' OR n.branch_id::text=$3) AND ($4='' OR r.state=$4) AND ($5='' OR r.source=$5) AND ($6='' OR r.channel=$6) AND ($7='' OR r.date_to>NULLIF($7,'')::date) AND ($8='' OR r.date_from<NULLIF($8,'')::date) AND ($9='' OR r.id::text=$9 OR r.source_reference ILIKE '%'||$9||'%') ORDER BY r.created_at DESC,r.id DESC LIMIT $10 OFFSET $11`, t.Organisation, property, branch, state, source, channel, from, to, strings.TrimSpace(search), limit, offset, properties)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	out := []reservations.Reservation{}
	for _, raw := range rows {
		var v reservations.Reservation
		if e = json.Unmarshal(raw, &v); e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		out = append(out, v)
	}
	reply(w, r, 200, map[string]any{"items": out, "limit": limit, "offset": offset, "next_offset": offset + len(rows)}, nil)
}
func (h *Handler) reservationCommit(ctx context.Context, tx pgx.Tx, t tenant, v reservations.Reservation, action, event, reason string, change any) error {
	rooms, e := reservations.Rooms(ctx, tx, t.Organisation, v.ID, false)
	if e != nil {
		return e
	}
	snapshot := map[string]any{"reservation": v, "rooms": rooms}
	if _, e = tx.Exec(ctx, `INSERT INTO reservation_versions(id,organisation_id,property_id,reservation_id,reservation_version,action,prior_state,snapshot,reason,correlation_id,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, ids.New(), t.Organisation, v.PropertyID, v.ID, v.Version, action, v.PriorState, marshalCommercial(snapshot), reason, requestcontext.From(ctx).RequestID, t.Account); e != nil {
		return e
	}
	if change != nil {
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_modifications(id,organisation_id,property_id,reservation_id,prior_version,new_version,action,change_context,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, ids.New(), t.Organisation, v.PropertyID, v.ID, v.Version-1, v.Version, action, marshalCommercial(change), reason, t.Account); e != nil {
			return e
		}
	}
	return businessEvents.CommercialCommit(ctx, tx, h.Auth.Service.Outbox, t.Organisation, v.PropertyID, v.ID, t.Account, "reservation", action, event, v.Version, map[string]any{"reservation_id": v.ID, "state": v.State, "source": v.Source, "channel": v.Channel, "version": v.Version, "financial_state": v.FinancialState, "risk_status": v.RiskStatus, "amount_minor": v.AmountMinor, "currency": v.Currency, "allocation_state": v.AllocationState, "date_from": v.DateFrom, "date_to": v.DateTo, "reason": reason})
}

// acceptReservationRoom stores one frozen price line and its real hold. The
// caller validates the quote and financial obligation before invoking it.
func (h *Handler) acceptReservationRoom(ctx context.Context, tx pgx.Tx, t tenant, id, financialID ids.ID, command reservations.RoomCommand, q revenue.QuoteSnapshot, channel, state string, policy reservations.Policy, now time.Time) (reservations.Room, error) {
	var e error

	var hold *inventory.Hold
	if state != "INQUIRY" {
		if command.HoldID == nil {
			value, e := (inventory.Repository{Pool: h.Auth.Service.Pool, Outbox: h.Auth.Service.Outbox, Now: h.Auth.Service.Now}).PlaceHold(ctx, tx, t.Organisation, t.Account, inventory.HoldCommand{ProductID: command.ProductID, ProductVersion: command.ProductVersion, PoolVersion: command.PoolVersion, QuoteID: command.QuoteID, DateFrom: command.DateFrom, DateTo: command.DateTo, Quantity: command.Quantity, Channel: channel, DurationSeconds: min(policy.HoldSeconds, 900)})
			if e != nil {
				return reservations.Room{}, e
			}
			hold = &value
		} else {
			value, e := inventory.GetHold(ctx, tx, t.Organisation, *command.HoldID)
			if e != nil {
				return reservations.Room{}, e
			}
			hold = &value
		}
	}
	roomID := ids.New()
	var holdID any
	if hold != nil {
		holdID = hold.ID
	}
	// Net accommodation attribution is unknown when ancillary amounts share
	// inclusive tax allocation. Keep that missingness explicit for RMS metrics.
	var roomRevenue *int64
	if q.AddOnsMinor == 0 {
		net := q.BaseAmountMinor - q.DiscountMinor
		for _, component := range q.Components {
			if component.Included {
				net -= component.AmountMinor
			}
		}
		if net < 0 {
			return reservations.Room{}, apperrors.Conflict("PRICE_COMPONENT_RECONCILIATION_REQUIRED", "Accepted accommodation components do not reconcile.")
		}
		roomRevenue = &net
	}
	if _, e = tx.Exec(ctx, `INSERT INTO reservation_rooms(id,organisation_id,property_id,reservation_id,financial_group_id,product_id,pool_id,quote_id,hold_id,date_from,date_to,quantity,adults,children,amount_minor,currency,accommodation_minor,snapshot,accepted_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::date,$11::date,$12,$13,$14,$15,$16,$17,$18,$19,$20)`, roomID, t.Organisation, q.PropertyID, id, financialID, q.ProductID, q.PoolID, q.QuoteID, holdID, q.DateFrom, q.DateTo, q.Quantity, q.Adults, q.Children, q.AmountMinor, q.Currency, roomRevenue, marshalCommercial(q), now, t.Account); e != nil {
		return reservations.Room{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO reservation_price_snapshots(id,organisation_id,property_id,reservation_id,room_id,quote_id,snapshot,accepted_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, ids.New(), t.Organisation, q.PropertyID, id, roomID, q.QuoteID, marshalCommercial(q), now, t.Account); e != nil {
		return reservations.Room{}, e
	}
	if hold != nil {
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_holds(id,organisation_id,property_id,room_id,hold_id,hold_version,created_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, ids.New(), t.Organisation, q.PropertyID, roomID, hold.ID, hold.Version, t.Account); e != nil {
			return reservations.Room{}, e
		}
	}
	return reservations.Load[reservations.Room](ctx, tx, `SELECT to_jsonb(r) FROM reservation_rooms r WHERE organisation_id=$1 AND id=$2`, t.Organisation, roomID)
}

func initialReservationState(state string) string {
	if state == "CONFIRMED" {
		return "HOLD"
	}
	return state
}
