package integration

import (
	"encoding/json"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type importSnapshot struct {
	ID         ids.ID `json:"import_id"`
	Status     string `json:"status"`
	Version    int64  `json:"version"`
	Checkpoint int    `json:"checkpoint"`
}

func (h *businessHarness) importSnapshot(u *businessUser, id ids.ID) importSnapshot {
	h.t.Helper()
	status, raw := h.call(u, "GET", "/imports/"+id.String(), nil, nil)
	if status != 200 {
		h.t.Fatalf("job %d %s", status, raw)
	}
	var response struct {
		Data importSnapshot `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		h.t.Fatal(err)
	}
	return response.Data
}
func (h *businessHarness) importTick(count int) {
	h.t.Helper()
	if err := h.domain.ProcessInventoryImports(h.t.Context(), count); err != nil {
		h.t.Fatal(err)
	}
}
func TestBusinessImportPreviewResumeDedupeAndSafeRecovery(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	owner := h.user()
	h.secure(owner)
	branch := h.inventoryBranch(owner)
	records := []map[string]any{
		{"source_key": "unit-101", "kind": "UNIT", "parent_key": "floor-1", "create": map[string]any{"code": "APT-101", "name": "Apartment", "attributes": map[string]any{"classification": "APARTMENT"}}},
		{"source_key": "property-1", "kind": "PROPERTY", "create": map[string]any{"code": "PROPERTY-1", "name": "Imported property", "attributes": map[string]any{"classification": "RESIDENTIAL_BUILDING"}}},
		{"source_key": "floor-1", "kind": "FLOOR", "parent_key": "property-1", "create": map[string]any{"code": "FLOOR-1", "name": "First floor", "attributes": map[string]any{"floor_number": 1}}},
		{"source_key": "room-1", "kind": "ROOM", "parent_key": "floor-1", "room_type_key": "type-1", "create": map[string]any{"code": "101", "name": "Room 101", "attributes": map[string]any{"capacity": 2}}},
		{"source_key": "type-1", "kind": "ROOM_TYPE", "parent_key": "property-1", "create": map[string]any{"code": "DELUXE", "name": "Deluxe room", "attributes": map[string]any{"capacity": 2, "beds": 1}}},
	}
	body := map[string]any{"branch_id": branch, "provider": "legacy-test", "format": "JSON", "records": records}
	status, raw := h.call(owner, "POST", "/imports", body, map[string]string{"Idempotency-Key": "import-created-once"})
	if status != 202 {
		t.Fatalf("create %d %s", status, raw)
	}
	var accepted struct {
		Data importSnapshot `json:"data"`
	}
	if err := json.Unmarshal(raw, &accepted); err != nil {
		t.Fatal(err)
	}
	job := accepted.Data.ID
	if status, again := h.call(owner, "POST", "/imports", body, map[string]string{"Idempotency-Key": "import-created-once"}); status != 202 || string(again) != string(raw) {
		t.Fatalf("idempotent job %d", status)
	}
	h.importTick(2)
	snapshot := h.importSnapshot(owner, job)
	if snapshot.Status != "VALIDATING" || snapshot.Checkpoint != 2 {
		t.Fatalf("validation checkpoint %+v", snapshot)
	}
	for i := 0; i < 3; i++ {
		h.importTick(2)
	}
	snapshot = h.importSnapshot(owner, job)
	if snapshot.Status != "PREVIEW_READY" {
		t.Fatalf("preview %+v", snapshot)
	}
	var count int
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM inventory_nodes`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preview mutated canonical inventory: %d %v", count, err)
	}
	if status, raw = h.call(owner, "POST", "/imports/"+job.String()+"/commit", map[string]any{"version": snapshot.Version, "reason": "Preview reviewed"}, nil); status != 200 {
		t.Fatalf("commit %d %s", status, raw)
	}
	h.importTick(2)
	snapshot = h.importSnapshot(owner, job)
	if snapshot.Status != "RUNNING" || snapshot.Checkpoint != 2 {
		t.Fatalf("execution checkpoint %+v", snapshot)
	}
	if status, raw = h.call(owner, "POST", "/imports/"+job.String()+"/pause", map[string]any{"version": snapshot.Version, "reason": "Interrupt migration for restart exercise"}, nil); status != 200 {
		t.Fatalf("pause %d %s", status, raw)
	}
	h.importTick(20)
	snapshot = h.importSnapshot(owner, job)
	if snapshot.Status != "PAUSED" {
		t.Fatal("paused job executed")
	}
	if status, raw = h.call(owner, "POST", "/imports/"+job.String()+"/resume", map[string]any{"version": snapshot.Version, "reason": "Resume from persisted checkpoint"}, nil); status != 200 {
		t.Fatalf("resume %d %s", status, raw)
	}
	h.importTick(20)
	snapshot = h.importSnapshot(owner, job)
	if snapshot.Status != "COMPLETED" {
		t.Fatalf("complete %+v", snapshot)
	}
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM inventory_nodes`).Scan(&count); err != nil || count != 5 {
		t.Fatalf("import nodes %d %v", count, err)
	}
	// Replay a new job against exact external IDs: canonical IDs stay stable.
	status, raw = h.call(owner, "POST", "/imports", body, nil)
	if status != 202 {
		t.Fatalf("repeat create %d %s", status, raw)
	}
	if err := json.Unmarshal(raw, &accepted); err != nil {
		t.Fatal(err)
	}
	repeat := accepted.Data.ID
	h.importTick(20)
	again := h.importSnapshot(owner, repeat)
	if status, raw = h.call(owner, "POST", "/imports/"+repeat.String()+"/commit", map[string]any{"version": again.Version, "reason": "Exact identity deduplication"}, nil); status != 200 {
		t.Fatalf("dedupe commit %d %s", status, raw)
	}
	h.importTick(20)
	if result := h.importSnapshot(owner, repeat); result.Status != "COMPLETED" {
		t.Fatalf("dedupe outcome %+v", result)
	}
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM inventory_nodes`).Scan(&count); err != nil || count != 5 {
		t.Fatalf("duplicate objects %d %v", count, err)
	}
	// A later edit must become a reconciliation task, never be overwritten.
	var unit ids.ID
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT id FROM inventory_nodes WHERE kind='UNIT'`).Scan(&unit); err != nil {
		t.Fatal(err)
	}
	if status, raw = h.call(owner, "PATCH", "/units/"+unit.String(), map[string]any{"version": 1, "name": "Retained later edit", "reason": "Independent structural edit"}, nil); status != 200 {
		t.Fatalf("edit %d %s", status, raw)
	}
	snapshot = h.importSnapshot(owner, job)
	if status, raw = h.call(owner, "POST", "/imports/"+job.String()+"/rollback", map[string]any{"version": snapshot.Version, "reason": "Recover only safe unchanged draft objects"}, nil); status != 200 {
		t.Fatalf("recovery %d %s", status, raw)
	}
	n, err := portfolio.Get(t.Context(), h.db.Pool, owner.registered.OrganisationID, unit)
	if err != nil || n.Name != "Retained later edit" || n.State != "DRAFT" {
		t.Fatalf("unsafe recovery %+v %v", n, err)
	}
	var tasks int
	if err = h.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM inventory_import_reconciliation WHERE job_id=$1`, job).Scan(&tasks); err != nil || tasks == 0 {
		t.Fatalf("reconciliation %d %v", tasks, err)
	}
}
