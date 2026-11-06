package hospitality

import (
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// These are owning operational source references, not guest profiles, payment
// truth or a replacement occupancy graph. Only bounded current sources and the
// latest evidenced departure are retained in reconciliation reports.
type RoomStaySource struct {
	ID                 ids.ID     `json:"id"`
	Version            int64      `json:"version"`
	AssignmentID       ids.ID     `json:"assignment_id"`
	AssignmentState    string     `json:"assignment_state"`
	AssignmentVersion  int64      `json:"assignment_version"`
	ReservationID      ids.ID     `json:"reservation_id"`
	ReservationRoomID  ids.ID     `json:"reservation_room_id"`
	ReservationVersion int64      `json:"reservation_version"`
	ReservationState   string     `json:"reservation_state"`
	AllocationState    string     `json:"allocation_state"`
	DateFrom           string     `json:"date_from"`
	DateTo             string     `json:"date_to"`
	Occupants          int        `json:"occupants"`
	CheckoutLocal      string     `json:"checkout_local"`
	AcceptanceTimezone string     `json:"acceptance_timezone"`
	HasArrivalEvidence bool       `json:"has_arrival_evidence"`
	StockBacked        bool       `json:"stock_backed"`
	DepartureDueAt     *time.Time `json:"departure_due_at,omitempty"`
	Overdue            bool       `json:"overdue"`
}
type RoomAssignmentSource struct {
	ID                  ids.ID `json:"id"`
	Version             int64  `json:"version"`
	State               string `json:"state"`
	ReservationID       ids.ID `json:"reservation_id"`
	ReservationVersion  int64  `json:"reservation_version"`
	ReservationState    string `json:"reservation_state"`
	HasAnyStay          bool   `json:"has_any_stay"`
	HasTerminalEvidence bool   `json:"has_terminal_evidence"`
}
type RoomDepartureSource struct {
	StayID               ids.ID    `json:"stay_id"`
	StayVersion          int64     `json:"stay_version"`
	AssignmentID         ids.ID    `json:"assignment_id"`
	AssignmentState      string    `json:"assignment_state"`
	CheckedOutAt         time.Time `json:"checked_out_at"`
	RecordedAt           time.Time `json:"recorded_at"`
	HasDepartureEvidence bool      `json:"has_departure_evidence"`
}
type RoomSafetyProof struct {
	OrganisationID           ids.ID                 `json:"organisation_id"`
	PropertyID               ids.ID                 `json:"property_id"`
	RoomID                   ids.ID                 `json:"room_id"`
	StatusID                 ids.ID                 `json:"status_id"`
	RoomState                string                 `json:"room_state"`
	RoomVersion              int64                  `json:"room_version"`
	StatusUpdatedAt          time.Time              `json:"status_updated_at"`
	NodeKind                 string                 `json:"node_kind"`
	NodeState                string                 `json:"node_state"`
	NodeVersion              int64                  `json:"node_version"`
	NodePropertyID           ids.ID                 `json:"node_property_id"`
	NodeCapacity             int                    `json:"node_capacity"`
	ActiveStayCount          int                    `json:"active_stay_count"`
	OccupiedAssignmentCount  int                    `json:"occupied_assignment_count"`
	EffectiveAssignmentCount int                    `json:"effective_assignment_count"`
	ActiveStays              []RoomStaySource       `json:"active_stays"`
	Assignments              []RoomAssignmentSource `json:"assignments"`
	LatestDeparture          *RoomDepartureSource   `json:"latest_departure,omitempty"`
}
type RoomSafetyPlan struct {
	DesiredState         string   `json:"desired_state"`
	Issues               []string `json:"issues"`
	ReleaseAssignmentIDs []ids.ID `json:"release_assignment_ids"`
}

func PlanRoomReconciliation(p RoomSafetyProof) RoomSafetyPlan {
	out := RoomSafetyPlan{DesiredState: p.RoomState, Issues: []string{}, ReleaseAssignmentIDs: []ids.ID{}}
	add := func(code string) {
		if !slices.Contains(out.Issues, code) {
			out.Issues = append(out.Issues, code)
		}
	}
	if p.PropertyID != p.NodePropertyID || p.NodeKind != "ROOM" && p.NodeKind != "UNIT" {
		add("CANONICAL_ROOM_SCOPE_REVIEW_REQUIRED")
		return out
	}
	if p.EffectiveAssignmentCount > 500 || p.ActiveStayCount > 10 {
		add("BOUNDED_SOURCE_REVIEW_REQUIRED")
		return out
	}
	if p.ActiveStayCount != len(p.ActiveStays) || p.EffectiveAssignmentCount != len(p.Assignments) {
		add("SOURCE_COUNT_REVIEW_REQUIRED")
		return out
	}
	if p.ActiveStayCount > 1 {
		add("MULTIPLE_ACTIVE_STAYS")
	}
	if p.OccupiedAssignmentCount != p.ActiveStayCount {
		add("OCCUPANCY_ASSIGNMENT_MISMATCH")
	}
	for _, stay := range p.ActiveStays {
		if stay.AssignmentState != "OCCUPIED" {
			add("ACTIVE_STAY_ASSIGNMENT_MISMATCH")
		}
		if stay.ReservationState != "CHECKED_IN" || stay.AllocationState != "ACTIVE" {
			add("ACTIVE_STAY_CANONICAL_STATE_REVIEW_REQUIRED")
		}
		if !stay.HasArrivalEvidence {
			add("ARRIVAL_EVIDENCE_REVIEW_REQUIRED")
		}
		if !stay.StockBacked {
			add("COMMITTED_STOCK_REVIEW_REQUIRED")
		}
		if p.NodeState != "ACTIVE" || p.NodeCapacity < stay.Occupants {
			add("ACTIVE_ROOM_CAPACITY_REVIEW_REQUIRED")
		}
		if stay.DepartureDueAt == nil {
			add("DEPARTURE_POLICY_REVIEW_REQUIRED")
		} else if stay.Overdue {
			add("OVERDUE_PHYSICAL_OCCUPANCY")
		}
	}
	if p.ActiveStayCount == 1 && len(p.ActiveStays) == 1 {
		stay := p.ActiveStays[0]
		if p.RoomState == "BLOCKED" || p.RoomState == "OUT_OF_ORDER" {
			add("HAZARDOUS_ROOM_OCCUPIED")
		} else if p.RoomState != "OCCUPIED" {
			if stay.AssignmentState == "OCCUPIED" && stay.ReservationState == "CHECKED_IN" && stay.HasArrivalEvidence {
				out.DesiredState = "OCCUPIED"
			} else {
				add("OCCUPANCY_SOURCE_REVIEW_REQUIRED")
			}
		}
	}
	if p.ActiveStayCount == 0 && p.OccupiedAssignmentCount == 0 {
		if p.RoomState == "OCCUPIED" || Ready(p.RoomState) {
			departure := p.LatestDeparture
			proved := departure != nil && departure.AssignmentState == "RELEASED" && departure.HasDepartureEvidence && !departure.RecordedAt.IsZero()
			if proved && (p.RoomState == "OCCUPIED" && !departure.RecordedAt.Before(p.StatusUpdatedAt) || Ready(p.RoomState) && departure.RecordedAt.After(p.StatusUpdatedAt)) {
				out.DesiredState = "VACANT_DIRTY"
			} else if p.RoomState == "OCCUPIED" {
				add("UNPROVEN_PHYSICAL_VACANCY")
			}
		}
		if out.DesiredState != "OCCUPIED" {
			for _, a := range p.Assignments {
				terminal := slices.Contains([]string{"CANCELLED", "REJECTED", "EXPIRED", "NO_SHOW"}, a.ReservationState)
				if a.State == "ASSIGNED" && terminal {
					if !a.HasAnyStay && a.HasTerminalEvidence {
						out.ReleaseAssignmentIDs = append(out.ReleaseAssignmentIDs, a.ID)
					} else {
						add("TERMINAL_ASSIGNMENT_REVIEW_REQUIRED")
					}
				}
			}
		}
	}
	slices.Sort(out.Issues)
	return out
}
