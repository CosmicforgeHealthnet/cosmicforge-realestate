package portfolio

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"testing"
	"time"
)

type safetyGuardFunc func(context.Context, SafetyRequest) (SafetyAssertion, error)

func (f safetyGuardFunc) Check(ctx context.Context, r SafetyRequest) (SafetyAssertion, error) {
	return f(ctx, r)
}
func TestActivatedInventoryDependencyEvidenceFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	org := ids.New()
	nodes := []Node{{ID: ids.New(), OrganisationID: org, State: "ACTIVE", Version: 3, TreeVersion: 9, ActivatedAt: &now}}
	if err := CheckSafety(t.Context(), nil, nodes, "digest", now); err == nil {
		t.Fatal("missing downstream assertions accepted")
	}
	for _, name := range []string{"wrong tenant", "wrong operation", "expired", "incomplete", "unsafe", "missing provenance", "valid"} {
		t.Run(name, func(t *testing.T) {
			guard := safetyGuardFunc(func(_ context.Context, r SafetyRequest) (SafetyAssertion, error) {
				if len(r.NodeIDs) != 1 || r.Versions[0] != 3 || r.TreeVersions[0] != 9 {
					t.Fatalf("unbound safety request %+v", r)
				}
				a := SafetyAssertion{OrganisationID: org, OperationDigest: "digest", Safe: true, Complete: true, ExpiresAt: now.Add(time.Minute), Reference: "provider-evidence"}
				switch name {
				case "wrong tenant":
					a.OrganisationID = ids.New()
				case "wrong operation":
					a.OperationDigest = "other"
				case "expired":
					a.ExpiresAt = now
				case "incomplete":
					a.Complete = false
				case "unsafe":
					a.Safe = false
				case "missing provenance":
					a.Reference = ""
				}
				return a, nil
			})
			err := CheckSafety(t.Context(), guard, nodes, "digest", now)
			if (err == nil) != (name == "valid") {
				t.Fatalf("evidence accepted=%v", err == nil)
			}
		})
	}
	nodes[0].State = "DRAFT"
	if err := CheckSafety(t.Context(), nil, nodes, "digest", now); err == nil {
		t.Fatal("previously activated draft bypass")
	}
	nodes[0].ActivatedAt = nil
	if err := CheckSafety(t.Context(), nil, nodes, "digest", now); err != nil {
		t.Fatal(err)
	}
}

func TestTypedEnterpriseRelationships(t *testing.T) {
	tests := []struct {
		parent, child Kind
		relation      string
		want          bool
	}{{Portfolio, Property, "MEMBERSHIP", true}, {Portfolio, Property, "CONTAINS", false}, {Development, Phase, "CONTAINS", true}, {Phase, Parcel, "CONTAINS", true}, {Property, Unit, "CONTAINS", true}, {Floor, Unit, "CONTAINS", true}, {RoomType, Room, "CONTAINS", false}, {Room, Asset, "CONTAINS", true}, {Asset, Property, "CONTAINS", false}, {Property, Property, "CONTAINS", false}, {Parcel, Property, "ASSOCIATES", true}, {Property, Parcel, "ASSOCIATES", false}}
	for _, test := range tests {
		t.Run(string(test.parent)+"/"+string(test.child)+"/"+test.relation, func(t *testing.T) {
			if got := LegalEdge(test.parent, test.child, test.relation); got != test.want {
				t.Fatalf("relationship=%v want %v", got, test.want)
			}
		})
	}
}
func TestParcelGeometryTopologyAndBounds(t *testing.T) {
	tests := []struct {
		name, typ, coords string
		valid             bool
	}{
		{"polygon", "Polygon", `[[[3,6],[4,6],[4,7],[3,7],[3,6]]]`, true},
		{"hole", "Polygon", `[[[0,0],[4,0],[4,4],[0,4],[0,0]],[[1,1],[1,2],[2,2],[2,1],[1,1]]]`, true},
		{"open", "Polygon", `[[[3,6],[4,6],[4,7],[3,7]]]`, false},
		{"bowtie", "Polygon", `[[[0,0],[2,2],[0,2],[2,0],[0,0]]]`, false},
		{"outside", "Polygon", `[[[180,0],[181,0],[181,1],[180,1],[180,0]]]`, false},
		{"wrong_dimension", "Polygon", `[[[0,0,1],[1,0,1],[1,1,1],[0,0,1]]]`, false},
		{"hole_outside", "Polygon", `[[[0,0],[4,0],[4,4],[0,4],[0,0]],[[5,5],[5,6],[6,6],[6,5],[5,5]]]`, false},
		{"dateline", "Polygon", `[[[-179,0],[179,0],[179,1],[-179,1],[-179,0]]]`, false},
		{"unsupported", "LineString", `[[0,0],[1,1]]`, false},
		{"disjoint", "MultiPolygon", `[[[[0,0],[1,0],[1,1],[0,1],[0,0]]],[[[2,2],[3,2],[3,3],[2,3],[2,2]]]]`, true},
		{"overlap", "MultiPolygon", `[[[[0,0],[3,0],[3,3],[0,3],[0,0]]],[[[2,2],[4,2],[4,4],[2,4],[2,2]]]]`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := Geometry{Type: test.typ, Coordinates: json.RawMessage(test.coords)}
			if got := g.Validate() == nil; got != test.valid {
				t.Fatalf("valid=%v want %v", got, test.valid)
			}
		})
	}
}
