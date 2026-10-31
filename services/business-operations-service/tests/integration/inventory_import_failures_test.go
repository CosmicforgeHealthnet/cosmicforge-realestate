package integration

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func (h *businessHarness) queueImport(u *businessUser, body any) ids.ID {
	h.t.Helper()
	status, raw := h.call(u, "POST", "/imports", body, nil)
	if status != 202 {
		h.t.Fatalf("queue %d %s", status, raw)
	}
	var r struct {
		Data importSnapshot `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		h.t.Fatal(err)
	}
	return r.Data.ID
}
func TestBusinessImportCSVGroupingValidationAndExplicitPartialExecution(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	u := h.user()
	h.secure(u)
	branch := h.inventoryBranch(u)
	body := map[string]any{"branch_id": branch, "provider": "csv-system", "format": "CSV", "csv": "source_key,kind,code,name,parent_key,portfolio_keys\np,PROPERTY,P,Property,,fund\nfund,PORTFOLIO,FUND,Collection,,\ninvalid,UNIT,BAD,Unknown parent,missing,\n"}
	job := h.queueImport(u, body)
	h.importTick(20)
	s := h.importSnapshot(u, job)
	if s.Status != "PREVIEW_READY" {
		t.Fatalf("preview %+v", s)
	}
	if status, _ := h.call(u, "POST", "/imports/"+job.String()+"/commit", map[string]any{"version": s.Version, "reason": "Reviewed incomplete source"}, nil); status != 409 {
		t.Fatalf("implicit partial %d", status)
	}
	if status, raw := h.call(u, "POST", "/imports/"+job.String()+"/commit", map[string]any{"version": s.Version, "allow_partial": true, "reason": "Accept two valid rows and retain invalid report"}, nil); status != 200 {
		t.Fatalf("explicit partial %d %s", status, raw)
	}
	h.importTick(20)
	s = h.importSnapshot(u, job)
	if s.Status != "COMPLETED_WITH_ERRORS" {
		t.Fatalf("outcome %+v", s)
	}
	var memberships int
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM inventory_edges WHERE relation='MEMBERSHIP'`).Scan(&memberships); err != nil || memberships != 1 {
		t.Fatalf("physical/group separation %d %v", memberships, err)
	}
	// Exact replay resolves the existing collection and property, without duplicate identities.
	replay := h.queueImport(u, body)
	h.importTick(20)
	s = h.importSnapshot(u, replay)
	if status, raw := h.call(u, "POST", "/imports/"+replay.String()+"/commit", map[string]any{"version": s.Version, "allow_partial": true, "reason": "Exact provider identity replay"}, nil); status != 200 {
		t.Fatalf("replay %d %s", status, raw)
	}
	h.importTick(20)
	var count int
	_ = h.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM inventory_nodes`).Scan(&count)
	if count != 2 {
		t.Fatalf("replay duplicated nodes: %d", count)
	}
	for name, rows := range map[string][]map[string]any{
		"cycle":              {{"source_key": "a", "kind": "ASSET", "parent_key": "b", "create": map[string]any{"code": "A", "name": "A", "attributes": map[string]any{}}}, {"source_key": "b", "kind": "ASSET", "parent_key": "a", "create": map[string]any{"code": "B", "name": "B", "attributes": map[string]any{}}}},
		"duplicate external": {{"source_key": "x", "external_id": "same", "kind": "PROPERTY", "create": map[string]any{"code": "X", "name": "X", "attributes": map[string]any{}}}, {"source_key": "y", "external_id": "same", "kind": "PROPERTY", "create": map[string]any{"code": "Y", "name": "Y", "attributes": map[string]any{}}}},
	} {
		t.Run(name, func(t *testing.T) {
			j := h.queueImport(u, map[string]any{"branch_id": branch, "provider": "invalid-source-" + ids.New().String(), "format": "JSON", "records": rows})
			h.importTick(20)
			var invalid int
			_ = h.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM inventory_import_records WHERE job_id=$1 AND status='INVALID'`, j).Scan(&invalid)
			if invalid == 0 {
				t.Fatal("invalid source accepted")
			}
		})
	}
}

func TestBusinessImportStalePreviewPausesAndWorkersFenceConcurrentClaims(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	u := h.user()
	h.secure(u)
	branch := h.inventoryBranch(u)
	p := h.inventoryNode(u, "/properties", map[string]any{"branch_id": branch, "code": "P", "name": "Existing", "attributes": map[string]any{}})
	job := h.queueImport(u, map[string]any{"branch_id": branch, "provider": "concurrent-source", "format": "JSON", "records": []map[string]any{
		{"source_key": "existing", "kind": "PROPERTY", "canonical_id": p.ID, "create": map[string]any{"code": "P", "name": "Existing", "attributes": map[string]any{}}},
		{"source_key": "later", "kind": "PROPERTY", "create": map[string]any{"code": "LATER", "name": "Later", "attributes": map[string]any{}}},
	}})
	h.importTick(20)
	s := h.importSnapshot(u, job)
	if status, _ := h.call(u, "PATCH", "/properties/"+p.ID.String(), map[string]any{"version": 1, "name": "Changed after preview", "reason": "Independent operation"}, nil); status != 200 {
		t.Fatal(status)
	}
	if status, _ := h.call(u, "POST", "/imports/"+job.String()+"/commit", map[string]any{"version": s.Version, "reason": "Execute reviewed preview"}, nil); status != 200 {
		t.Fatal(status)
	}
	h.importTick(20)
	s = h.importSnapshot(u, job)
	if s.Status != "PAUSED" || s.Checkpoint != 1 {
		t.Fatalf("stale preview did not pause %+v", s)
	}
	if status, _ := h.call(u, "POST", "/imports/"+job.String()+"/resume", map[string]any{"version": s.Version, "reason": "Must acknowledge quarantine"}, nil); status != 409 {
		t.Fatalf("unreviewed resume %d", status)
	}
	if status, _ := h.call(u, "POST", "/imports/"+job.String()+"/resume", map[string]any{"version": s.Version, "allow_partial": true, "reason": "Accept partial execution after conflict review"}, nil); status != 200 {
		t.Fatal(status)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- h.domain.ProcessInventoryImports(t.Context(), 20) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s = h.importSnapshot(u, job)
	if s.Status != "COMPLETED_WITH_ERRORS" {
		t.Fatalf("concurrent completion %+v", s)
	}
	var count int
	_ = h.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM inventory_nodes WHERE code='LATER'`).Scan(&count)
	if count != 1 {
		t.Fatalf("concurrent duplicate effect %d", count)
	}
}

func TestBusinessImportSessionRevocation(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	u := h.user()
	h.secure(u)
	branch := h.inventoryBranch(u)
	job := h.queueImport(u, map[string]any{"branch_id": branch, "provider": "expired-session", "format": "JSON", "records": []map[string]any{{"source_key": "p", "kind": "PROPERTY", "create": map[string]any{"code": "P", "name": "Property", "attributes": map[string]any{}}}}})
	if _, err := h.db.Pool.Exec(t.Context(), `UPDATE business_sessions SET revoked_at=now() WHERE id=$1`, u.tokens.SessionID); err != nil {
		t.Fatal(err)
	}
	h.importTick(20)
	var status string
	_ = h.db.Pool.QueryRow(t.Context(), `SELECT status FROM inventory_import_jobs WHERE id=$1`, job).Scan(&status)
	if status != "PAUSED" {
		t.Fatalf("revoked initiator %s", status)
	}
	if code, _ := h.call(u, "GET", "/imports/"+job.String(), nil, nil); code != 401 {
		t.Fatalf("revoked API session %d", code)
	}
}
