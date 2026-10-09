package usecases

import (
	"context"
	"crypto/subtle"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/credentials"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

// Viewings (§13.11): slots, requests, confirmation, rescheduling, attendance
// and no-shows. Access instructions for occupied properties are gated on
// occupant consent; physical viewings get a one-time access code (sealed at
// rest, valid until shortly after the viewing). The conducting
// representative's authority is checked against their authorization record.
// Live video belongs to CF Meet (only a conversation reference is stored).

// AggViewing is the event aggregate of viewings.
const AggViewing = "viewing"

// SlotView is a viewing slot.
type SlotView struct {
	ID        ids.ID    `json:"id"`
	ListingID ids.ID    `json:"listing_id"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	Mode      string    `json:"mode"`
	Capacity  int32     `json:"capacity"`
	Booked    int32     `json:"booked"`
	HostType  string    `json:"host_type"`
	Status    string    `json:"status"`
	Notes     *string   `json:"notes"`
}

func slotView(s db.ViewingSlot) SlotView {
	return SlotView{ID: s.ID, ListingID: s.ListingID, StartsAt: s.StartsAt, EndsAt: s.EndsAt, Mode: s.Mode, Capacity: s.Capacity, Booked: s.Booked,
		HostType: s.HostType, Status: s.Status, Notes: s.Notes}
}

// SlotInput creates viewing slots.
type SlotInput struct {
	ListingID ids.ID    `json:"listing_id" v:"required"`
	StartsAt  time.Time `json:"starts_at" v:"required"`
	EndsAt    time.Time `json:"ends_at" v:"required"`
	Mode      string    `json:"mode" v:"required,enum=PHYSICAL|VIRTUAL_LIVE|OPEN_HOUSE"`
	Capacity  int32     `json:"capacity,omitempty" v:"min=1,max=200" doc:"Default 1 (OPEN_HOUSE default 20)"`
	Notes     *string   `json:"notes,omitempty" v:"max=1000"`
}

// CreateSlot opens a viewing slot on a published listing.
func (s *Service) CreateSlot(ctx context.Context, subj authz.Subject, in SlotInput) (SlotView, error) {
	if err := schema.Validate(in); err != nil {
		return SlotView{}, err
	}
	if !in.EndsAt.After(in.StartsAt) || in.EndsAt.Sub(in.StartsAt) > 8*time.Hour {
		return SlotView{}, apperrors.InvalidRequest("A slot ends after it starts and lasts at most 8 hours.")
	}
	var out SlotView
	err := s.InTx(ctx, func(ctx context.Context) error {
		l, d, err := s.listingFor(ctx, subj, in.ListingID, authz.Do(authz.CapViewingsManage))
		if err != nil {
			return err
		}
		if l.Status != "PUBLISHED" && l.Status != "PAUSED" {
			return conflict("LISTING_NOT_LIVE", "Viewings are scheduled for published listings.")
		}
		if !in.StartsAt.After(s.now()) {
			return apperrors.InvalidRequest("starts_at must be in the future.")
		}
		tx := mustTx(ctx)
		n, err := s.q.OverlappingSlots(ctx, tx, db.OverlappingSlotsParams{ListingID: l.ID, StartsAt: in.EndsAt, EndsAt: in.StartsAt})
		if err != nil {
			return err
		}
		if n > 0 {
			return conflict("SLOT_OVERLAP", "The slot overlaps another open slot for this listing.")
		}
		capacity := in.Capacity
		if capacity == 0 {
			capacity = 1
			if in.Mode == "OPEN_HOUSE" {
				capacity = 20
			}
		}
		host := "OWNER"
		if d.Via != nil && d.Via.Role == authz.RoleManager {
			host = "MANAGER"
		} else if d.Via != nil && d.Via.Role == authz.RoleRepresentative {
			host = "REPRESENTATIVE"
		}
		sl, err := s.q.InsertViewingSlot(ctx, tx, db.InsertViewingSlotParams{ID: ids.New(), PropertyID: l.PropertyID, ListingID: l.ID, StartsAt: in.StartsAt.UTC(),
			EndsAt: in.EndsAt.UTC(), Mode: in.Mode, Capacity: capacity, HostType: host, Notes: in.Notes, CreatedAt: s.now()})
		if err != nil {
			return err
		}
		out = slotView(sl)
		if err := s.noteDelegateActivity(ctx, d, l.PropertyID, "VIEWING_SLOT_CREATED", "viewing_slot", sl.ID, in.Mode); err != nil {
			return err
		}
		return s.record(ctx, "viewing.slot.create", "viewing_slot", sl.ID, map[string]any{"mode": in.Mode, "capacity": capacity}, nil)
	})
	return out, err
}

// ListSlots lists upcoming slots of a listing.
func (s *Service) ListSlots(ctx context.Context, subj authz.Subject, listingID ids.ID) ([]SlotView, error) {
	l, _, err := s.listingFor(ctx, subj, listingID, authz.Read(authz.CapViewingsManage))
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListViewingSlots(ctx, s.dbq(ctx), db.ListViewingSlotsParams{ListingID: l.ID, EndsAt: s.now()})
	out := make([]SlotView, 0, len(rows))
	for _, r := range rows {
		out = append(out, slotView(r))
	}
	return out, err
}

// CancelSlot cancels a slot and every viewing booked into it.
func (s *Service) CancelSlot(ctx context.Context, subj authz.Subject, slotID ids.ID, in ReasonCodeInput) (SlotView, error) {
	if err := schema.Validate(in); err != nil {
		return SlotView{}, err
	}
	var out SlotView
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx := mustTx(ctx)
		sl, err := s.q.GetViewingSlotForUpdate(ctx, tx, slotID)
		if isNoRows(err) {
			return notFound("viewing_slot")
		}
		if err != nil {
			return err
		}
		if _, err := s.authorize(ctx, subj, authz.Do(authz.CapViewingsManage), sl.PropertyID); err != nil {
			return err
		}
		if n, err := s.q.CancelViewingSlot(ctx, tx, db.CancelViewingSlotParams{ID: slotID, UpdatedAt: s.now()}); err != nil || n == 0 {
			if err == nil {
				err = conflict("SLOT_NOT_OPEN", "The slot is already cancelled.")
			}
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id FROM viewing_requests WHERE slot_id = $1 AND status IN ('REQUESTED','CONFIRMED','RESCHEDULED')`, slotID)
		if err != nil {
			return err
		}
		var affected []ids.ID
		for rows.Next() {
			var id ids.ID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			affected = append(affected, id)
		}
		rows.Close()
		for _, id := range affected {
			if err := s.cancelViewingTx(ctx, id, "OWNER", in.ReasonCode); err != nil {
				return err
			}
		}
		sl.Status = "CANCELLED"
		out = slotView(sl)
		return s.record(ctx, "viewing.slot.cancel", "viewing_slot", slotID, map[string]any{"viewings_cancelled": len(affected)}, nil)
	})
	return out, err
}

// ViewingView is an owner-facing viewing.
type ViewingView struct {
	ID                  ids.ID           `json:"id"`
	PropertyID          ids.ID           `json:"property_id"`
	ListingID           ids.ID           `json:"listing_id"`
	SlotID              *ids.ID          `json:"slot_id"`
	InquiryID           *ids.ID          `json:"inquiry_id"`
	CustomerRefID       ids.ID           `json:"customer_ref_id"`
	CustomerDisplayName string           `json:"customer_display_name"`
	Mode                string           `json:"mode"`
	Status              string           `json:"status"`
	StartsAt            time.Time        `json:"starts_at"`
	EndsAt              time.Time        `json:"ends_at"`
	ProposedStartsAt    *time.Time       `json:"proposed_starts_at"`
	ProposedEndsAt      *time.Time       `json:"proposed_ends_at"`
	HostType            string           `json:"host_type"`
	HostRefID           *ids.ID          `json:"host_ref_id"`
	Occupied            bool             `json:"occupied"`
	OccupantConsent     bool             `json:"occupant_consent_recorded"`
	AccessCodeIssued    bool             `json:"access_code_issued"`
	AccessCode          *string          `json:"access_code,omitempty" doc:"Returned once, when issued"`
	VideoSession        string           `json:"video_session" doc:"CF Meet session state: NOT_APPLICABLE, LINKED or UNAVAILABLE (CF Meet integration not configured)"`
	OwnerNotes          *string          `json:"owner_notes"`
	CancelReason        *string          `json:"cancel_reason"`
	Attendance          []AttendanceView `json:"attendance"`
	AllowedNextStatuses []string         `json:"allowed_next_statuses"`
	Version             int64            `json:"version"`
	CreatedAt           time.Time        `json:"created_at"`
}

// AttendanceView is one attendance record.
type AttendanceView struct {
	Party      string    `json:"party"`
	Outcome    string    `json:"outcome"`
	Rating     *int32    `json:"rating"`
	Feedback   *string   `json:"feedback"`
	SafetyFlag bool      `json:"safety_flag"`
	OccurredAt time.Time `json:"occurred_at"`
}

func (s *Service) viewingView(ctx context.Context, v db.ViewingRequest) (ViewingView, error) {
	out := ViewingView{ID: v.ID, PropertyID: v.PropertyID, ListingID: v.ListingID, SlotID: v.SlotID, InquiryID: v.InquiryID, CustomerRefID: v.CustomerRefID,
		CustomerDisplayName: v.CustomerDisplayName, Mode: v.Mode, Status: v.Status, StartsAt: v.StartsAt, EndsAt: v.EndsAt, ProposedStartsAt: v.ProposedStartsAt,
		ProposedEndsAt: v.ProposedEndsAt, HostType: v.HostType, HostRefID: v.HostRefID, Occupied: v.Occupied, OccupantConsent: v.OccupantConsentAt != nil,
		AccessCodeIssued: v.AccessCodeSealed != nil, OwnerNotes: v.OwnerNotes, CancelReason: v.CancelReason, Attendance: []AttendanceView{},
		AllowedNextStatuses: nonNil(domain.Viewing.Targets(v.Status)), Version: v.Version, CreatedAt: v.CreatedAt, VideoSession: "NOT_APPLICABLE"}
	if v.Mode == "VIRTUAL_LIVE" {
		out.VideoSession = "UNAVAILABLE"
		if v.ConversationRef != nil {
			out.VideoSession = "LINKED"
		}
	}
	att, err := s.q.ListViewingAttendance(ctx, s.dbq(ctx), v.ID)
	for _, a := range att {
		out.Attendance = append(out.Attendance, AttendanceView{Party: a.Party, Outcome: a.Outcome, Rating: a.Rating, Feedback: a.Feedback, SafetyFlag: a.SafetyFlag, OccurredAt: a.OccurredAt})
	}
	return out, err
}

func (s *Service) viewingFor(ctx context.Context, subj authz.Subject, id ids.ID, action authz.Action, lock bool) (db.ViewingRequest, authz.Decision, error) {
	get := s.q.GetViewing
	if lock {
		get = s.q.GetViewingForUpdate
	}
	v, err := get(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return v, authz.Decision{}, notFound(AggViewing)
	}
	if err != nil {
		return v, authz.Decision{}, err
	}
	d, err := s.authorize(ctx, subj, action, v.PropertyID)
	if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
		err = notFound(AggViewing)
	}
	return v, d, err
}

// ViewingEvent is viewing.updated.v1 (customer-visible appointment state).
type ViewingEvent struct {
	SchemaVersion    int        `json:"schema_version"`
	ViewingID        ids.ID     `json:"viewing_id"`
	ListingID        ids.ID     `json:"listing_id"`
	PropertyID       ids.ID     `json:"property_id"`
	CustomerRefID    ids.ID     `json:"customer_ref_id"`
	Status           string     `json:"status"`
	PreviousStatus   string     `json:"previous_status"`
	Mode             string     `json:"mode"`
	StartsAt         time.Time  `json:"starts_at"`
	EndsAt           time.Time  `json:"ends_at"`
	ProposedStartsAt *time.Time `json:"proposed_starts_at"`
	ProposedEndsAt   *time.Time `json:"proposed_ends_at"`
	AccessReady      bool       `json:"access_ready" doc:"An access code is ready for the customer (retrieved via the internal API, never in events)"`
	CancelReason     *string    `json:"cancel_reason"`
	ViewingVersion   int64      `json:"viewing_version"`
	OccurredAt       time.Time  `json:"occurred_at"`
}

func (s *Service) emitViewing(ctx context.Context, id ids.ID, previous string) error {
	v, err := s.q.GetViewing(ctx, mustTx(ctx), id)
	if err != nil {
		return err
	}
	return s.publish(ctx, "viewing.updated.v1", AggViewing, id, ViewingEvent{SchemaVersion: 1, ViewingID: v.ID, ListingID: v.ListingID, PropertyID: v.PropertyID,
		CustomerRefID: v.CustomerRefID, Status: v.Status, PreviousStatus: previous, Mode: v.Mode, StartsAt: v.StartsAt, EndsAt: v.EndsAt,
		ProposedStartsAt: v.ProposedStartsAt, ProposedEndsAt: v.ProposedEndsAt, AccessReady: v.AccessCodeSealed != nil, CancelReason: v.CancelReason,
		ViewingVersion: v.Version, OccurredAt: s.now()})
}

func (s *Service) viewingTransition(ctx context.Context, v db.ViewingRequest, to, cmd, reason string) error {
	if _, _, err := s.transition(ctx, domain.Viewing, v.ID, lifecycle.Change{To: to, Command: cmd, Reason: reason}); err != nil {
		return err
	}
	return s.emitViewing(ctx, v.ID, v.Status)
}

// occupied reports whether the listed property/unit is occupied (occupant
// consent required before access instructions).
func (s *Service) occupied(ctx context.Context, listingID ids.ID) (bool, error) {
	a, err := s.q.GetAvailability(ctx, s.dbq(ctx), listingID)
	if err != nil {
		return false, err
	}
	if a.Status == "OCCUPIED" {
		return true, nil
	}
	for _, c := range occupancyChecks {
		var busy bool
		if err := s.dbq(ctx).QueryRow(ctx, c, listingID).Scan(&busy); err != nil {
			return false, err
		}
		if busy {
			return true, nil
		}
	}
	return false, nil
}

// occupancyChecks are registered by the tenancy context.
var occupancyChecks []string

// issueAccessCode seals a one-time code for a physical viewing once access is permitted.
func (s *Service) issueAccessCode(ctx context.Context, v db.ViewingRequest) (*string, error) {
	if v.Mode == "VIRTUAL_LIVE" || (v.Occupied && v.OccupantConsentAt == nil) {
		return nil, nil
	}
	code := credentials.NumericCode(6)
	sealed := s.Sealer.Seal([]byte(code), []byte("viewing-code:"+v.ID.String()))
	kid := s.Sealer.KeyID()
	exp := v.EndsAt.Add(time.Hour)
	if err := s.q.SetViewingAccessCode(ctx, mustTx(ctx), db.SetViewingAccessCodeParams{ID: v.ID, AccessCodeSealed: sealed, AccessCodeKeyID: &kid,
		AccessCodeExpiresAt: &exp, UpdatedAt: s.now()}); err != nil {
		return nil, err
	}
	return &code, nil
}

// ApproveViewing confirms a requested viewing.
func (s *Service) ApproveViewing(ctx context.Context, subj authz.Subject, id ids.ID) (ViewingView, error) {
	var out ViewingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		v, d, err := s.viewingFor(ctx, subj, id, authz.Do(authz.CapViewingsManage), true)
		if err != nil {
			return err
		}
		if v.Status == "RESCHEDULED" {
			return conflict("AWAITING_CUSTOMER", "The customer must accept the proposed time.")
		}
		if !v.StartsAt.After(s.now()) {
			return conflict("VIEWING_IN_PAST", "The viewing time has passed.")
		}
		if err := s.viewingTransition(ctx, v, "CONFIRMED", "approve", ""); err != nil {
			return err
		}
		nv, err := s.q.GetViewingForUpdate(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		code, err := s.issueAccessCode(ctx, nv)
		if err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, v.PropertyID, "VIEWING_CONFIRMED", AggViewing, id, v.StartsAt.Format(time.RFC3339)); err != nil {
			return err
		}
		if err := s.advanceInquiry(ctx, v.InquiryID, "VIEWING"); err != nil {
			return err
		}
		if err := s.record(ctx, "viewing.approve", AggViewing, id, map[string]any{"access_code_issued": code != nil}, nil); err != nil {
			return err
		}
		nv, err = s.q.GetViewing(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.viewingView(ctx, nv)
		out.AccessCode = code
		return err
	})
	return out, err
}

// advanceInquiry moves a linked inquiry forward in the pipeline.
func (s *Service) advanceInquiry(ctx context.Context, id *ids.ID, to string) error {
	if id == nil {
		return nil
	}
	i, err := s.q.GetInquiry(ctx, mustTx(ctx), *id)
	if err != nil {
		return err
	}
	if i.Status == "NEW" {
		if err := s.moveInquiry(ctx, i, "ACKNOWLEDGED", "auto"); err != nil {
			return err
		}
		i.Status = "ACKNOWLEDGED"
	}
	if i.Status == "ACKNOWLEDGED" {
		if err := s.moveInquiry(ctx, i, "QUALIFIED", "auto"); err != nil {
			return err
		}
		i.Status = "QUALIFIED"
	}
	return s.moveInquiry(ctx, i, to, "auto")
}

// RescheduleInput proposes a new time (or slot).
type RescheduleInput struct {
	SlotID   *ids.ID    `json:"slot_id,omitempty"`
	StartsAt *time.Time `json:"starts_at,omitempty"`
	EndsAt   *time.Time `json:"ends_at,omitempty"`
}

// RescheduleViewing proposes a new time; the customer accepts through marketplace.
func (s *Service) RescheduleViewing(ctx context.Context, subj authz.Subject, id ids.ID, in RescheduleInput) (ViewingView, error) {
	var out ViewingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		v, _, err := s.viewingFor(ctx, subj, id, authz.Do(authz.CapViewingsManage), true)
		if err != nil {
			return err
		}
		start, end, slot, err := s.resolveTime(ctx, v.ListingID, in.SlotID, in.StartsAt, in.EndsAt)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		if v.SlotID != nil {
			if _, err := s.q.AdjustSlotBooking(ctx, tx, db.AdjustSlotBookingParams{ID: *v.SlotID, Delta: -1, Now: now}); err != nil {
				return err
			}
		}
		if slot != nil {
			if n, err := s.q.AdjustSlotBooking(ctx, tx, db.AdjustSlotBookingParams{ID: *slot, Delta: 1, Now: now}); err != nil || n == 0 {
				if err == nil {
					err = conflict("SLOT_FULL", "The slot is full or no longer open.")
				}
				return err
			}
		}
		if err := s.q.SetViewingProposal(ctx, tx, db.SetViewingProposalParams{ID: id, ProposedStartsAt: &start, ProposedEndsAt: &end, SlotID: slot, UpdatedAt: now}); err != nil {
			return err
		}
		if err := s.q.SetViewingAccessCode(ctx, tx, db.SetViewingAccessCodeParams{ID: id, UpdatedAt: now}); err != nil {
			return err
		}
		if err := s.viewingTransition(ctx, v, "RESCHEDULED", "reschedule", ""); err != nil {
			return err
		}
		if err := s.record(ctx, "viewing.reschedule", AggViewing, id, map[string]any{"proposed_starts_at": start}, nil); err != nil {
			return err
		}
		nv, err := s.q.GetViewing(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.viewingView(ctx, nv)
		return err
	})
	return out, err
}

// resolveTime validates a slot or explicit window for a listing.
func (s *Service) resolveTime(ctx context.Context, listingID ids.ID, slotID *ids.ID, start, end *time.Time) (time.Time, time.Time, *ids.ID, error) {
	if slotID != nil {
		sl, err := s.q.GetViewingSlotForUpdate(ctx, mustTx(ctx), *slotID)
		if isNoRows(err) || (err == nil && (sl.ListingID != listingID || sl.Status != "OPEN")) {
			return time.Time{}, time.Time{}, nil, precondition("SLOT_INVALID", "The slot does not exist for this listing or is cancelled.")
		}
		if err != nil {
			return time.Time{}, time.Time{}, nil, err
		}
		if !sl.StartsAt.After(s.now()) {
			return time.Time{}, time.Time{}, nil, precondition("SLOT_IN_PAST", "The slot has already started.")
		}
		return sl.StartsAt, sl.EndsAt, slotID, nil
	}
	if start == nil || end == nil || !end.After(*start) || end.Sub(*start) > 4*time.Hour {
		return time.Time{}, time.Time{}, nil, apperrors.InvalidRequest("Give slot_id, or starts_at and ends_at (at most 4 hours).")
	}
	if !start.After(s.now()) {
		return time.Time{}, time.Time{}, nil, apperrors.InvalidRequest("starts_at must be in the future.")
	}
	return start.UTC(), end.UTC(), nil, nil
}

// CancelViewingInput cancels a viewing.
type CancelViewingInput struct {
	ReasonCode string `json:"reason_code" v:"required,enum=OWNER_UNAVAILABLE|PROPERTY_UNAVAILABLE|SAFETY_CONCERN|CUSTOMER_REQUEST|DUPLICATE|OTHER"`
}

// CancelViewing cancels a requested/confirmed/rescheduled viewing.
func (s *Service) CancelViewing(ctx context.Context, subj authz.Subject, id ids.ID, in CancelViewingInput) (ViewingView, error) {
	if err := schema.Validate(in); err != nil {
		return ViewingView{}, err
	}
	var out ViewingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, _, err := s.viewingFor(ctx, subj, id, authz.Do(authz.CapViewingsManage), true); err != nil {
			return err
		}
		if err := s.cancelViewingTx(ctx, id, "OWNER", in.ReasonCode); err != nil {
			return err
		}
		nv, err := s.q.GetViewing(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.viewingView(ctx, nv)
		return err
	})
	return out, err
}

func (s *Service) cancelViewingTx(ctx context.Context, id ids.ID, by, reason string) error {
	tx, now := mustTx(ctx), s.now()
	v, err := s.q.GetViewingForUpdate(ctx, tx, id)
	if err != nil {
		return err
	}
	if v.SlotID != nil && slices.Contains([]string{"REQUESTED", "CONFIRMED", "RESCHEDULED"}, v.Status) {
		if _, err := s.q.AdjustSlotBooking(ctx, tx, db.AdjustSlotBookingParams{ID: *v.SlotID, Delta: -1, Now: now}); err != nil {
			return err
		}
	}
	if err := s.q.SetViewingCancellation(ctx, tx, db.SetViewingCancellationParams{ID: id, CancelReason: &reason, CancelledBy: &by, UpdatedAt: now}); err != nil {
		return err
	}
	if err := s.viewingTransition(ctx, v, "CANCELLED", "cancel", reason); err != nil {
		return err
	}
	return s.record(ctx, "viewing.cancel", AggViewing, id, map[string]any{"by": by, "reason_code": reason}, nil)
}

// RepresentativeInput assigns who conducts the viewing.
type RepresentativeInput struct {
	HostType  string  `json:"host_type" v:"required,enum=OWNER|REPRESENTATIVE|MANAGER"`
	HostRefID *ids.ID `json:"host_ref_id,omitempty" doc:"Ownership party ID (REPRESENTATIVE) or manager assignment ID (MANAGER)"`
}

// AssignViewingHost sets the conducting host after checking their authority
// (viewings.manage on the exact property, inside the validity window).
func (s *Service) AssignViewingHost(ctx context.Context, subj authz.Subject, id ids.ID, in RepresentativeInput) (ViewingView, error) {
	if err := schema.Validate(in); err != nil {
		return ViewingView{}, err
	}
	var out ViewingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		v, _, err := s.viewingFor(ctx, subj, id, authz.Do(authz.CapViewingsManage), true)
		if err != nil {
			return err
		}
		if !slices.Contains([]string{"REQUESTED", "CONFIRMED", "RESCHEDULED"}, v.Status) {
			return conflict("VIEWING_CLOSED", "The viewing is "+v.Status+".")
		}
		tx, now := mustTx(ctx), s.now()
		switch in.HostType {
		case "OWNER":
			in.HostRefID = nil
		case "REPRESENTATIVE":
			if in.HostRefID == nil {
				return apperrors.InvalidRequest("host_ref_id is required.")
			}
			p, err := s.q.GetOwnershipParty(ctx, tx, *in.HostRefID)
			ok := err == nil && p.PropertyID == v.PropertyID && p.Status == "ACTIVE" && p.AccountID != nil &&
				slices.Contains(p.Capabilities, string(authz.CapViewingsManage)) && (p.ValidUntil == nil || p.ValidUntil.After(v.EndsAt))
			if !ok {
				return precondition("REPRESENTATIVE_NOT_AUTHORIZED", "The representative has no viewings.manage authority covering this viewing.")
			}
		case "MANAGER":
			if in.HostRefID == nil {
				return apperrors.InvalidRequest("host_ref_id is required.")
			}
			a, err := s.q.GetManagerAssignment(ctx, tx, *in.HostRefID)
			ok := err == nil && a.PropertyID == v.PropertyID && (a.Status == "ACTIVE" || a.Status == "EXPIRING") && a.EndsAt.After(v.EndsAt)
			if ok {
				perm, err := s.q.GetManagerPermission(ctx, tx, db.GetManagerPermissionParams{AssignmentID: a.ID, VersionNo: a.CurrentPermissionVersion})
				ok = err == nil && slices.Contains(perm.Capabilities, string(authz.CapViewingsManage))
			}
			if !ok {
				return precondition("MANAGER_NOT_AUTHORIZED", "The manager assignment does not grant viewings.manage for this viewing.")
			}
		}
		if err := s.q.SetViewingHost(ctx, tx, db.SetViewingHostParams{ID: id, HostType: in.HostType, HostRefID: in.HostRefID, UpdatedAt: now}); err != nil {
			return err
		}
		if err := s.record(ctx, "viewing.host", AggViewing, id, map[string]any{"host_type": in.HostType, "host_ref_id": in.HostRefID}, nil); err != nil {
			return err
		}
		nv, err := s.q.GetViewing(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.viewingView(ctx, nv)
		return err
	})
	return out, err
}

// OccupantConsentInput records the occupant's consent to the viewing.
type OccupantConsentInput struct {
	ConsentRef string `json:"consent_ref" v:"required,max=128" doc:"Reference of the occupant's consent (e.g. CF Meet message or signed notice)"`
}

// RecordOccupantConsent satisfies the occupied-property gate and, for a
// confirmed physical viewing, issues the access code.
func (s *Service) RecordOccupantConsent(ctx context.Context, subj authz.Subject, id ids.ID, in OccupantConsentInput) (ViewingView, error) {
	if err := schema.Validate(in); err != nil {
		return ViewingView{}, err
	}
	var out ViewingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		v, _, err := s.viewingFor(ctx, subj, id, authz.Do(authz.CapViewingsManage), true)
		if err != nil {
			return err
		}
		if !v.Occupied {
			return precondition("NOT_OCCUPIED", "Occupant consent applies to occupied properties only.")
		}
		tx, now := mustTx(ctx), s.now()
		if err := s.q.SetViewingOccupantConsent(ctx, tx, db.SetViewingOccupantConsentParams{ID: id, OccupantConsentAt: &now, OccupantConsentRef: &in.ConsentRef}); err != nil {
			return err
		}
		var code *string
		if v.Status == "CONFIRMED" {
			nv, err := s.q.GetViewingForUpdate(ctx, tx, id)
			if err != nil {
				return err
			}
			if code, err = s.issueAccessCode(ctx, nv); err != nil {
				return err
			}
			if err := s.emitViewing(ctx, id, v.Status); err != nil {
				return err
			}
		}
		if err := s.record(ctx, "viewing.occupant_consent", AggViewing, id, map[string]any{"consent_ref": in.ConsentRef}, nil); err != nil {
			return err
		}
		nv, err := s.q.GetViewing(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = s.viewingView(ctx, nv)
		out.AccessCode = code
		return err
	})
	return out, err
}

// CheckInInput records arrival; physical viewings verify the access code.
type CheckInInput struct {
	AccessCode *string `json:"access_code,omitempty" v:"pattern=[0-9]{6}"`
}

// CheckInViewing marks the customer as arrived (CONFIRMED → CHECKED_IN).
func (s *Service) CheckInViewing(ctx context.Context, subj authz.Subject, id ids.ID, in CheckInInput) (ViewingView, error) {
	if err := schema.Validate(in); err != nil {
		return ViewingView{}, err
	}
	var out ViewingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		v, _, err := s.viewingFor(ctx, subj, id, authz.Do(authz.CapViewingsManage), true)
		if err != nil {
			return err
		}
		if v.AccessCodeSealed != nil {
			if in.AccessCode == nil {
				return apperrors.InvalidRequest("access_code is required for this viewing.")
			}
			plain, err := s.Sealer.Open(deref(v.AccessCodeKeyID), v.AccessCodeSealed, []byte("viewing-code:"+id.String()))
			if err != nil || subtle.ConstantTimeCompare(plain, []byte(*in.AccessCode)) != 1 || (v.AccessCodeExpiresAt != nil && !v.AccessCodeExpiresAt.After(s.now())) {
				return precondition("ACCESS_CODE_INVALID", "The access code is invalid or expired.")
			}
		}
		if err := s.viewingTransition(ctx, v, "CHECKED_IN", "check_in", ""); err != nil {
			return err
		}
		if err := s.attendance(ctx, id, "CUSTOMER", "CHECKED_IN", nil, nil, false); err != nil {
			return err
		}
		nv, err := s.q.GetViewing(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.viewingView(ctx, nv)
		return err
	})
	return out, err
}

func (s *Service) attendance(ctx context.Context, id ids.ID, party, outcome string, rating *int32, feedback *string, safety bool) error {
	dom, actor := "", ids.Nil
	if a := actorFromCtx(ctx); a != nil {
		dom, actor = a.Domain, a.ID
	}
	_, err := s.q.InsertViewingAttendance(ctx, mustTx(ctx), db.InsertViewingAttendanceParams{ID: ids.New(), ViewingID: id, Party: party, Outcome: outcome,
		Rating: rating, Feedback: feedback, SafetyFlag: safety, ActorDomain: strp(dom), ActorID: idp(actor), OccurredAt: s.now()})
	return err
}

// ViewingOutcomeInput completes a viewing or records a no-show.
type ViewingOutcomeInput struct {
	Outcome    string  `json:"outcome" v:"required,enum=COMPLETED|NO_SHOW"`
	Rating     *int32  `json:"rating,omitempty" v:"min=1,max=5"`
	Feedback   *string `json:"feedback,omitempty" v:"max=2000"`
	SafetyFlag bool    `json:"safety_flag,omitempty" doc:"Flag a safety concern for platform review"`
}

// RecordViewingOutcome closes a viewing (CHECKED_IN → COMPLETED, CONFIRMED/REQUESTED → NO_SHOW).
func (s *Service) RecordViewingOutcome(ctx context.Context, subj authz.Subject, id ids.ID, in ViewingOutcomeInput) (ViewingView, error) {
	if err := schema.Validate(in); err != nil {
		return ViewingView{}, err
	}
	var out ViewingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		v, d, err := s.viewingFor(ctx, subj, id, authz.Do(authz.CapViewingsManage), true)
		if err != nil {
			return err
		}
		if in.Outcome == "NO_SHOW" && v.StartsAt.After(s.now()) {
			return conflict("VIEWING_NOT_STARTED", "A no-show can only be recorded after the start time.")
		}
		if err := s.viewingTransition(ctx, v, in.Outcome, lower(in.Outcome), ""); err != nil {
			return err
		}
		if err := s.attendance(ctx, id, "HOST", in.Outcome, in.Rating, in.Feedback, in.SafetyFlag); err != nil {
			return err
		}
		if err := s.q.SetViewingAccessCode(ctx, mustTx(ctx), db.SetViewingAccessCodeParams{ID: id, UpdatedAt: s.now()}); err != nil {
			return err
		}
		if err := s.noteDelegateActivity(ctx, d, v.PropertyID, "VIEWING_"+in.Outcome, AggViewing, id, v.CustomerDisplayName); err != nil {
			return err
		}
		if in.SafetyFlag {
			if err := s.propertyHistory(ctx, v.PropertyID, "VIEWING_SAFETY_FLAG", "Safety concern flagged on a viewing", AggViewing, id); err != nil {
				return err
			}
		}
		if err := s.record(ctx, "viewing.outcome", AggViewing, id, map[string]any{"outcome": in.Outcome, "safety_flag": in.SafetyFlag}, nil); err != nil {
			return err
		}
		nv, err := s.q.GetViewing(ctx, mustTx(ctx), id)
		if err != nil {
			return err
		}
		out, err = s.viewingView(ctx, nv)
		return err
	})
	return out, err
}

// ViewingNotesInput updates owner notes.
type ViewingNotesInput struct {
	OwnerNotes *string `json:"owner_notes,omitempty" v:"max=2000"`
}

// UpdateViewingNotes patches owner notes (If-Match).
func (s *Service) UpdateViewingNotes(ctx context.Context, subj authz.Subject, id ids.ID, expected int64, in ViewingNotesInput) (ViewingView, error) {
	if err := schema.Validate(in); err != nil {
		return ViewingView{}, err
	}
	var out ViewingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, _, err := s.viewingFor(ctx, subj, id, authz.Do(authz.CapViewingsManage), true); err != nil {
			return err
		}
		nv, err := s.q.SetViewingNotes(ctx, mustTx(ctx), db.SetViewingNotesParams{ID: id, Version: expected, OwnerNotes: in.OwnerNotes, UpdatedAt: s.now()})
		if isNoRows(err) {
			return lifecycle.ErrVersionConflict()
		}
		if err != nil {
			return err
		}
		out, err = s.viewingView(ctx, nv)
		return err
	})
	return out, err
}

// GetViewing returns a viewing.
func (s *Service) GetViewing(ctx context.Context, subj authz.Subject, id ids.ID) (ViewingView, error) {
	v, _, err := s.viewingFor(ctx, subj, id, authz.Read(authz.CapViewingsManage), false)
	if err != nil {
		return ViewingView{}, err
	}
	return s.viewingView(ctx, v)
}

// ListViewings pages viewings across the caller's properties.
func (s *Service) ListViewings(ctx context.Context, subj authz.Subject, listingID ids.ID, status, cursor string, limit int) (pagination.Response[ViewingView], error) {
	if _, err := s.authorize(ctx, subj, authz.ActPortfolioRead, ids.Nil); err != nil {
		return pagination.Response[ViewingView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[ViewingView]{}, err
	}
	rows, err := s.q.ListViewingsForAccount(ctx, s.dbq(ctx), db.ListViewingsForAccountParams{AccountID: accountOf(subj), Now: s.now(), ListingID: idp(listingID),
		Status: strp(status), CursorAt: at, CursorID: cid, MaxRows: int32(limit + 1)})
	if err != nil {
		return pagination.Response[ViewingView]{}, err
	}
	out := make([]ViewingView, 0, len(rows))
	for _, r := range rows {
		v, err := s.viewingView(ctx, r)
		if err != nil {
			return pagination.Response[ViewingView]{}, err
		}
		out = append(out, v)
	}
	return pagination.Build(out, limit, func(v ViewingView) any { return timeCursor{At: v.StartsAt, ID: v.ID} })
}

// ---- marketplace (customer actor) ----

// CustomerViewingInput requests a viewing.
type CustomerViewingInput struct {
	ListingID           ids.ID     `json:"listing_id" v:"required"`
	InquiryID           *ids.ID    `json:"inquiry_id,omitempty"`
	CustomerDisplayName string     `json:"customer_display_name" v:"required,max=120"`
	Mode                string     `json:"mode" v:"required,enum=PHYSICAL|VIRTUAL_LIVE|OPEN_HOUSE"`
	SlotID              *ids.ID    `json:"slot_id,omitempty"`
	StartsAt            *time.Time `json:"starts_at,omitempty"`
	EndsAt              *time.Time `json:"ends_at,omitempty"`
	ConversationRef     *string    `json:"conversation_ref,omitempty" v:"max=128" doc:"CF Meet context for live video"`
}

// CustomerRequestViewing records a viewing request from marketplace.
func (s *Service) CustomerRequestViewing(ctx context.Context, subj authz.Subject, in CustomerViewingInput) (ViewingView, error) {
	if err := schema.Validate(in); err != nil {
		return ViewingView{}, err
	}
	var out ViewingView
	err := s.InTx(ctx, func(ctx context.Context) error {
		customer, err := s.requireCustomer(ctx, subj)
		if err != nil {
			return err
		}
		l, err := s.bookableListing(ctx, in.ListingID)
		if err != nil {
			return err
		}
		if in.InquiryID != nil {
			i, err := s.q.GetInquiry(ctx, mustTx(ctx), *in.InquiryID)
			if err != nil || i.ListingID != l.ID || i.CustomerRefID != customer {
				return precondition("INQUIRY_INVALID", "The inquiry does not belong to this customer and listing.")
			}
		}
		start, end, slot, err := s.resolveTime(ctx, l.ID, in.SlotID, in.StartsAt, in.EndsAt)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		if slot != nil {
			if n, err := s.q.AdjustSlotBooking(ctx, tx, db.AdjustSlotBookingParams{ID: *slot, Delta: 1, Now: now}); err != nil || n == 0 {
				if err == nil {
					err = conflict("SLOT_FULL", "The slot is full or no longer open.")
				}
				return err
			}
		}
		occ, err := s.occupied(ctx, l.ID)
		if err != nil {
			return err
		}
		v, err := s.q.InsertViewing(ctx, tx, db.InsertViewingParams{ID: ids.New(), PropertyID: l.PropertyID, ListingID: l.ID, SlotID: slot, InquiryID: in.InquiryID,
			CustomerRefID: customer, CustomerDisplayName: in.CustomerDisplayName, Mode: in.Mode, StartsAt: start, EndsAt: end, HostType: "OWNER",
			Occupied: occ, ConversationRef: in.ConversationRef, CreatedAt: now})
		if err != nil {
			return err
		}
		if err := domain.Viewing.RecordCreation(ctx, tx, v.ID, &v.PropertyID, "REQUESTED", now); err != nil {
			return err
		}
		if err := s.emitViewing(ctx, v.ID, ""); err != nil {
			return err
		}
		p, err := s.q.GetProperty(ctx, tx, l.PropertyID)
		if err != nil {
			return err
		}
		if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateViewingRequested, map[string]string{"listing_title": l.Title, "starts_at": start.Format(time.RFC3339)}); err != nil {
			return err
		}
		if err := s.record(ctx, "viewing.request", AggViewing, v.ID, map[string]any{"mode": in.Mode, "listing_id": l.ID}, nil); err != nil {
			return err
		}
		out, err = s.viewingView(ctx, v)
		return err
	})
	return out, err
}

// CustomerViewingResponse is the customer's answer to a reschedule, or a cancellation.
type CustomerViewingResponse struct {
	Action string `json:"action" v:"required,enum=ACCEPT_RESCHEDULE|CANCEL"`
}

// CustomerViewingAccess is the customer's view including the access code.
type CustomerViewingAccess struct {
	ViewingID  ids.ID     `json:"viewing_id"`
	Status     string     `json:"status"`
	StartsAt   time.Time  `json:"starts_at"`
	EndsAt     time.Time  `json:"ends_at"`
	AccessCode *string    `json:"access_code" doc:"Present once access is permitted (confirmed, consent recorded)"`
	ExpiresAt  *time.Time `json:"access_code_expires_at"`
}

func (s *Service) customerViewing(ctx context.Context, subj authz.Subject, id ids.ID) (db.ViewingRequest, error) {
	customer, err := s.requireCustomer(ctx, subj)
	if err != nil {
		return db.ViewingRequest{}, err
	}
	v, err := s.q.GetViewingForUpdate(ctx, mustTx(ctx), id)
	if isNoRows(err) || (err == nil && v.CustomerRefID != customer) {
		return v, notFound(AggViewing)
	}
	return v, err
}

// CustomerRespondViewing applies the customer's response.
func (s *Service) CustomerRespondViewing(ctx context.Context, subj authz.Subject, id ids.ID, in CustomerViewingResponse) (CustomerViewingAccess, error) {
	if err := schema.Validate(in); err != nil {
		return CustomerViewingAccess{}, err
	}
	var out CustomerViewingAccess
	err := s.InTx(ctx, func(ctx context.Context) error {
		v, err := s.customerViewing(ctx, subj, id)
		if err != nil {
			return err
		}
		switch in.Action {
		case "CANCEL":
			if err := s.cancelViewingTx(ctx, id, "CUSTOMER", "CUSTOMER_REQUEST"); err != nil {
				return err
			}
		case "ACCEPT_RESCHEDULE":
			if v.Status != "RESCHEDULED" || v.ProposedStartsAt == nil {
				return conflict("NO_PROPOSAL", "There is no proposed time to accept.")
			}
			if err := s.q.SetViewingSchedule(ctx, mustTx(ctx), db.SetViewingScheduleParams{ID: id, StartsAt: *v.ProposedStartsAt, EndsAt: *v.ProposedEndsAt,
				SlotID: v.SlotID, UpdatedAt: s.now()}); err != nil {
				return err
			}
			if err := s.viewingTransition(ctx, v, "CONFIRMED", "customer_accept_reschedule", ""); err != nil {
				return err
			}
			nv, err := s.q.GetViewingForUpdate(ctx, mustTx(ctx), id)
			if err != nil {
				return err
			}
			if _, err := s.issueAccessCode(ctx, nv); err != nil {
				return err
			}
		}
		out, err = s.customerAccess(ctx, id)
		return err
	})
	return out, err
}

// CustomerGetViewingAccess returns the customer's appointment and access code.
func (s *Service) CustomerGetViewingAccess(ctx context.Context, subj authz.Subject, id ids.ID) (CustomerViewingAccess, error) {
	var out CustomerViewingAccess
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.customerViewing(ctx, subj, id); err != nil {
			return err
		}
		var err error
		out, err = s.customerAccess(ctx, id)
		return err
	})
	return out, err
}

func (s *Service) customerAccess(ctx context.Context, id ids.ID) (CustomerViewingAccess, error) {
	v, err := s.q.GetViewing(ctx, mustTx(ctx), id)
	if err != nil {
		return CustomerViewingAccess{}, err
	}
	out := CustomerViewingAccess{ViewingID: v.ID, Status: v.Status, StartsAt: v.StartsAt, EndsAt: v.EndsAt, ExpiresAt: v.AccessCodeExpiresAt}
	if v.AccessCodeSealed != nil && v.Status == "CONFIRMED" && (v.AccessCodeExpiresAt == nil || v.AccessCodeExpiresAt.After(s.now())) {
		plain, err := s.Sealer.Open(deref(v.AccessCodeKeyID), v.AccessCodeSealed, []byte("viewing-code:"+id.String()))
		if err == nil {
			code := string(plain)
			out.AccessCode = &code
		}
	}
	return out, nil
}

// RunViewingJobs sends owner reminders for confirmed viewings starting within
// 24 hours and cancels stale unanswered requests (§11 viewing reminders).
func (s *Service) RunViewingJobs(ctx context.Context, batch int32) error {
	return s.InTx(ctx, func(ctx context.Context) error {
		tx, now := mustTx(ctx), s.now()
		due, err := s.q.DueViewingReminders(ctx, tx, db.DueViewingRemindersParams{Now: now, RemindBefore: now.Add(24 * time.Hour), MaxRows: batch})
		if err != nil {
			return err
		}
		for _, v := range due {
			l, err := s.q.GetListing(ctx, tx, v.ListingID)
			if err != nil {
				return err
			}
			p, err := s.q.GetProperty(ctx, tx, v.PropertyID)
			if err != nil {
				return err
			}
			if err := s.notifyOwner(ctx, p.OwnerAccountID, TemplateViewingReminder, map[string]string{"listing_title": l.Title, "starts_at": v.StartsAt.Format(time.RFC3339)}); err != nil {
				return err
			}
		}
		stale, err := s.q.StaleViewings(ctx, tx, db.StaleViewingsParams{EndsAt: now, Limit: batch})
		if err != nil {
			return err
		}
		for _, id := range stale {
			if err := s.cancelViewingTx(ctx, id, "SYSTEM", "NOT_CONFIRMED_IN_TIME"); err != nil {
				return err
			}
		}
		return nil
	})
}

func init() {
	produces("viewing.updated.v1", AggViewing, "Customer-visible viewing appointment state changed.", "Marketplace, Notification", ViewingEvent{})
}
