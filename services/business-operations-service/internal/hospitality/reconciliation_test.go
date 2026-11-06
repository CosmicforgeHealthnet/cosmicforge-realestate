package hospitality

import (
	"slices"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func TestRoomReconciliationNeverInventsVacancyCleanlinessOrDeparture(t *testing.T) {
	now := time.Now().UTC()
	property, room, assignment := ids.New(), ids.New(), ids.New()
	base := func() RoomSafetyProof {
		return RoomSafetyProof{PropertyID: property, NodePropertyID: property, RoomID: room, NodeKind: "ROOM", NodeState: "ACTIVE", NodeCapacity: 2, RoomState: "INSPECTED", StatusUpdatedAt: now.Add(-time.Hour), ActiveStays: []RoomStaySource{}, Assignments: []RoomAssignmentSource{}}
	}
	active := func() RoomSafetyProof {
		p := base()
		due := now.Add(time.Hour)
		p.ActiveStayCount = 1
		p.OccupiedAssignmentCount = 1
		p.EffectiveAssignmentCount = 1
		p.ActiveStays = []RoomStaySource{{AssignmentID: assignment, AssignmentState: "OCCUPIED", ReservationState: "CHECKED_IN", AllocationState: "ACTIVE", Occupants: 2, HasArrivalEvidence: true, StockBacked: true, DepartureDueAt: &due}}
		p.Assignments = []RoomAssignmentSource{{ID: assignment, State: "OCCUPIED"}}
		return p
	}
	cases := []struct {
		name, state, issue string
		source             func() RoomSafetyProof
		releases           int
	}{
		{"canonical arrival", "OCCUPIED", "", active, 0},
		{"overdue guest stays occupied", "OCCUPIED", "OVERDUE_PHYSICAL_OCCUPANCY", func() RoomSafetyProof { p := active(); p.ActiveStays[0].Overdue = true; return p }, 0},
		{"hazard remains blocked", "OUT_OF_ORDER", "HAZARDOUS_ROOM_OCCUPIED", func() RoomSafetyProof { p := active(); p.RoomState = "OUT_OF_ORDER"; return p }, 0},
		{"real departed source only dirties room", "VACANT_DIRTY", "", func() RoomSafetyProof {
			p := base()
			p.RoomState = "OCCUPIED"
			p.LatestDeparture = &RoomDepartureSource{AssignmentState: "RELEASED", HasDepartureEvidence: true, RecordedAt: now}
			return p
		}, 0},
		{"old inspection predates departure", "VACANT_DIRTY", "", func() RoomSafetyProof {
			p := base()
			p.LatestDeparture = &RoomDepartureSource{AssignmentState: "RELEASED", HasDepartureEvidence: true, RecordedAt: now}
			return p
		}, 0},
		{"fresh actual inspection retained", "INSPECTED", "", func() RoomSafetyProof {
			p := base()
			p.StatusUpdatedAt = now.Add(time.Hour)
			p.LatestDeparture = &RoomDepartureSource{AssignmentState: "RELEASED", HasDepartureEvidence: true, RecordedAt: now}
			return p
		}, 0},
		{"unproven vacancy held", "OCCUPIED", "UNPROVEN_PHYSICAL_VACANCY", func() RoomSafetyProof { p := base(); p.RoomState = "OCCUPIED"; return p }, 0},
		{"lost stock cannot certify availability", "OCCUPIED", "COMMITTED_STOCK_REVIEW_REQUIRED", func() RoomSafetyProof { p := active(); p.ActiveStays[0].StockBacked = false; return p }, 0},
		{"terminal never occupied assignment released", "INSPECTED", "", func() RoomSafetyProof {
			p := base()
			p.EffectiveAssignmentCount = 1
			p.Assignments = []RoomAssignmentSource{{ID: assignment, State: "ASSIGNED", ReservationState: "NO_SHOW", HasTerminalEvidence: true}}
			return p
		}, 1},
		{"confirmed future assignment retained", "INSPECTED", "", func() RoomSafetyProof {
			p := base()
			p.EffectiveAssignmentCount = 1
			p.Assignments = []RoomAssignmentSource{{ID: assignment, State: "ASSIGNED", ReservationState: "CONFIRMED"}}
			return p
		}, 0},
		{"historical occupancy cannot be erased", "INSPECTED", "TERMINAL_ASSIGNMENT_REVIEW_REQUIRED", func() RoomSafetyProof {
			p := base()
			p.EffectiveAssignmentCount = 1
			p.Assignments = []RoomAssignmentSource{{ID: assignment, State: "ASSIGNED", ReservationState: "NO_SHOW", HasTerminalEvidence: true, HasAnyStay: true}}
			return p
		}, 0},
		{"scope mismatch fails closed", "INSPECTED", "CANONICAL_ROOM_SCOPE_REVIEW_REQUIRED", func() RoomSafetyProof { p := base(); p.NodePropertyID = ids.New(); return p }, 0},
		{"partial source count fails closed", "INSPECTED", "SOURCE_COUNT_REVIEW_REQUIRED", func() RoomSafetyProof { p := base(); p.ActiveStayCount = 1; return p }, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := PlanRoomReconciliation(c.source())
			if plan.DesiredState != c.state || len(plan.ReleaseAssignmentIDs) != c.releases || c.issue != "" && !slices.Contains(plan.Issues, c.issue) || c.issue == "" && len(plan.Issues) > 0 {
				t.Fatalf("unsafe plan %+v", plan)
			}
		})
	}
}
