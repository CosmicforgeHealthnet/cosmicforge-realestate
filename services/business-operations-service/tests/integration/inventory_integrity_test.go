package integration

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

func TestBusinessInventoryDatabaseIntegrityAndConcurrentEdits(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	owner, other := h.user(), h.user()
	h.secure(owner)
	h.secure(other)
	branch := h.inventoryBranch(owner)
	otherBranch := h.inventoryBranch(other)
	p := h.inventoryNode(owner, "/properties", map[string]any{"branch_id": branch, "code": "P", "name": "Property", "attributes": map[string]any{}})
	q := h.inventoryNode(other, "/properties", map[string]any{"branch_id": otherBranch, "code": "Q", "name": "Other tenant", "attributes": map[string]any{}})
	a := h.inventoryNode(owner, "/assets", map[string]any{"parent_id": p.ID, "code": "A", "name": "Plant", "attributes": map[string]any{}})
	b := h.inventoryNode(owner, "/assets", map[string]any{"parent_id": a.ID, "code": "B", "name": "Component", "attributes": map[string]any{}})
	org := owner.registered.OrganisationID
	rolledBack := portfolio.Node{ID: ids.New(), OrganisationID: org, BranchID: branch, Kind: portfolio.Property, Code: "ROLLBACK", Name: "Rolled back identity", Timezone: p.Timezone, Currency: p.Currency, PolicyCode: "STANDARD", PolicyVersion: 1}
	tx, e := h.db.Pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	if e = portfolio.Create(t.Context(), tx, rolledBack, nil, ids.Nil, owner.registered.AccountID); e != nil {
		t.Fatal(e)
	}
	if e = (portfolio.Repository{Outbox: h.service.Outbox}).Commit(t.Context(), tx, org, rolledBack.ID, owner.registered.AccountID, "CREATED", nil); e != nil {
		t.Fatal(e)
	}
	if e = tx.Rollback(t.Context()); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
		t.Fatal(e)
	}
	var leaked int
	if e = h.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM inventory_nodes WHERE id=$1)+(SELECT count(*) FROM outbox_events WHERE aggregate_id=$1)`, rolledBack.ID).Scan(&leaked); e != nil || leaked != 0 {
		t.Fatalf("rollback leaked state/event %d %v", leaked, e)
	}
	tests := []struct {
		name, sql string
		args      []any
	}{
		{"self parent", `INSERT INTO inventory_edges(id,organisation_id,parent_id,child_id,relation,created_by) VALUES($1,$2,$3,$3,'CONTAINS',$4)`, []any{ids.New(), org, a.ID, owner.registered.AccountID}},
		{"asset cycle", `UPDATE inventory_edges SET parent_id=$3 WHERE organisation_id=$1 AND child_id=$2`, []any{org, a.ID, b.ID}},
		{"cross tenant", `INSERT INTO inventory_edges(id,organisation_id,parent_id,child_id,relation,created_by) VALUES($1,$2,$3,$4,'CONTAINS',$5)`, []any{ids.New(), org, q.ID, a.ID, owner.registered.AccountID}},
		{"two physical parents", `INSERT INTO inventory_edges(id,organisation_id,parent_id,child_id,relation,created_by) VALUES($1,$2,$3,$4,'CONTAINS',$5)`, []any{ids.New(), org, p.ID, b.ID, owner.registered.AccountID}},
		{"orphan", `DELETE FROM inventory_edges WHERE organisation_id=$1 AND child_id=$2`, []any{org, b.ID}},
		{"identity immutable", `UPDATE inventory_nodes SET kind='UNIT',version=version+1 WHERE id=$1`, []any{a.ID}},
		{"context mismatch", `UPDATE inventory_nodes SET timezone='UTC',version=version+1 WHERE id=$1`, []any{a.ID}},
		{"parent retirement", `UPDATE inventory_nodes SET state='ARCHIVED',version=version+1 WHERE id=$1`, []any{p.ID}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := h.db.Pool.Exec(t.Context(), test.sql, test.args...); err == nil {
				t.Fatal("invalid graph mutation committed")
			}
		})
	}
	// Two real HTTP transactions with the same optimistic version: exactly one wins.
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"Concurrent one", "Concurrent two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			status, _ := h.call(owner, "PATCH", "/assets/"+a.ID.String(), map[string]any{"version": 1, "name": name, "reason": "Concurrent metadata correction"}, nil)
			statuses <- status
		}(name)
	}
	wg.Wait()
	close(statuses)
	got := map[int]int{}
	for status := range statuses {
		got[status]++
	}
	if got[200] != 1 || got[409] != 1 {
		t.Fatalf("concurrent edits %v", got)
	}
	current, err := portfolio.Get(context.Background(), h.db.Pool, org, a.ID)
	if err != nil || current.Version != 2 {
		t.Fatalf("version %+v %v", current, err)
	}
	// A same-property room category is a database invariant, not just API validation.
	r := h.inventoryNode(owner, "/properties", map[string]any{"branch_id": branch, "code": "R", "name": "Second property", "attributes": map[string]any{}})
	typ := h.inventoryNode(owner, "/room-types", map[string]any{"parent_id": p.ID, "code": "STANDARD", "name": "Standard", "attributes": map[string]any{}})
	if status, _ := h.call(owner, "POST", "/rooms", map[string]any{"parent_id": r.ID, "code": "101", "name": "Room", "room_type_id": typ.ID, "attributes": map[string]any{}}, nil); status != 400 {
		t.Fatalf("cross-property category %d", status)
	}
	status, raw := h.call(owner, "GET", "/inventory-nodes/"+p.ID.String()+"/summary", nil, nil)
	if status != 200 {
		t.Fatalf("summary %d %s", status, raw)
	}
}
