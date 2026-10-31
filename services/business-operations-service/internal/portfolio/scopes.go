package portfolio

import (
	"context"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func Resolve(ctx context.Context, q database.Querier, org ids.ID, kind string, id ids.ID) (workforce.Target, error) {
	n, err := Get(ctx, q, org, id)
	target := workforce.Target{Scope: workforce.Scope{Organisation: org, Kind: kind, ID: id}}
	if err != nil {
		return target, err
	}
	if string(n.Kind) != kind {
		return target, apperrors.PermissionDenied("Inventory scope type does not match its canonical identity.")
	}
	target.Ancestors = []workforce.Scope{{Organisation: org, Kind: "COMPANY", ID: org}, {Organisation: org, Kind: "BRANCH", ID: n.BranchID}}
	if n.DepartmentID != nil {
		target.Ancestors = append(target.Ancestors, workforce.Scope{Organisation: org, Kind: "DEPARTMENT", ID: *n.DepartmentID})
	}
	if n.TeamID != nil {
		target.Ancestors = append(target.Ancestors, workforce.Scope{Organisation: org, Kind: "TEAM", ID: *n.TeamID})
	}
	var region *ids.ID
	if err = q.QueryRow(ctx, `SELECT region_id FROM branches WHERE organisation_id=$1 AND id=$2`, org, n.BranchID).Scan(&region); err != nil {
		return target, err
	}
	if region != nil {
		target.Ancestors = append(target.Ancestors, workforce.Scope{Organisation: org, Kind: "REGION", ID: *region})
	}
	rows, err := q.Query(ctx, `WITH RECURSIVE a(id) AS(SELECT $2::uuid UNION SELECT e.parent_id FROM inventory_edges e JOIN a ON a.id=e.child_id WHERE e.organisation_id=$1 AND e.relation IN ('CONTAINS','MEMBERSHIP')) SELECT n.id,n.kind FROM inventory_nodes n JOIN a ON a.id=n.id WHERE n.organisation_id=$1 AND n.id<>$2 AND n.state NOT IN ('ARCHIVED','RETIRED') ORDER BY n.id LIMIT 10001`, org, id)
	if err != nil {
		return target, err
	}
	defer rows.Close()
	for rows.Next() {
		var scope workforce.Scope
		scope.Organisation = org
		if err = rows.Scan(&scope.ID, &scope.Kind); err != nil {
			return target, err
		}
		target.Ancestors = append(target.Ancestors, scope)
	}
	if len(target.Ancestors) > 10000 {
		return target, apperrors.PermissionDenied("Inventory ancestry exceeds the authorised processing bound.")
	}
	return target, rows.Err()
}
func Capability(kind Kind, write bool) string {
	p := "structure"
	switch kind {
	case Portfolio:
		p = "portfolio"
	case Development, Phase:
		p = "development"
	case Parcel:
		p = "parcel"
	case Property:
		p = "property"
	case Asset:
		p = "assets"
	}
	if write {
		return p + ":write"
	}
	return p + ":read"
}
