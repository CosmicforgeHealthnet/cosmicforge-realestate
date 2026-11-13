package integration

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/property-manager-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/property-manager-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit/domaintest"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func deliver(t *testing.T, k *domaintest.Kit, b domainkit.BusinessEvent) {
	t.Helper()
	env, err := events.New(context.Background(), platform.BusinessOperationsService, "manager.assignment.updated.v1", "manager_assignment", b.TargetID, b)
	if err != nil {
		t.Fatal(err)
	}
	if err = database.WithTx(t.Context(), k.RT.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		return usecases.BusinessHandler(k.RT)(ctx, tx, env, b)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMandateAcceptanceLiveAuthorizationDelegatedActionAndRevocation(t *testing.T) {
	k := domaintest.New(t, platform.PropertyManagerService, migrations.FS, usecases.New)
	if raw, _ := fs.ReadFile(migrations.FS, "00001_platform.sql"); string(raw) != domainkit.BaseMigration {
		t.Fatal("00001 platform migration drifted")
	}
	mgr, other := k.Account("mgr@pm.test"), k.Account("other@pm.test")
	org, property, assignment := ids.New(), ids.New(), ids.New()
	ends := time.Now().UTC().Add(30 * 24 * time.Hour)
	ev := domainkit.BusinessEvent{OrganisationID: org, TargetID: assignment, PropertyID: &property, Operation: "CREATED", State: "PROPOSED", Version: 1,
		Snapshot: domainkit.BusinessDetail{Domain: "manager_assignment", Kind: "PROPERTY_MANAGER", Title: "Managed letting", CounterpartyReference: k.RT.PartnerReference(mgr), EndsAt: &ends, Attributes: map[string]any{"capabilities": []any{"maintenance:write"}}}}
	deliver(t, k, ev)
	deliver(t, k, ev) // duplicate projection is harmless
	items := k.Actor(mgr, "GET", "/mandates", "", nil).Items()
	if len(items) != 1 || len(k.Actor(other, "GET", "/mandates", "", nil).Items()) != 0 {
		t.Fatalf("mandates %v", items)
	}
	mid := items[0].(map[string]any)["id"].(string)
	business := "PROPOSED"
	k.Business.Reply = func(c domaintest.Call) (int, any) {
		switch {
		case strings.Contains(c.Path, "/partner-records/manager_assignment/"):
			return 200, map[string]any{"domain": "manager_assignment", "record": map[string]any{"id": assignment, "state": business, "ends_at": ends}, "attributes": map[string]any{"capabilities": []any{"maintenance:write"}}}
		case strings.Contains(c.Path, "/manager-authorizations/"):
			return 200, map[string]any{"allowed": business == "ACTIVE", "state": business, "expires_at": ends}
		case strings.HasSuffix(c.Path, "/maintenance-requests"):
			if business != "ACTIVE" {
				return 403, nil
			}
			return 201, map[string]any{"id": ids.New(), "state": "OPEN", "title": c.Body["title"]}
		}
		return 404, nil
	}
	if r := k.Actor(mgr, "POST", "/mandates/"+mid+"/accept", "acc-1", map[string]any{}); r.Status != 409 {
		t.Fatalf("accept before Business activation %d %s", r.Status, r.Raw)
	}
	business = "ACTIVE"
	if r := k.Actor(mgr, "POST", "/mandates/"+mid+"/accept", "acc-2", map[string]any{}); r.Status != 200 || r.Data()["state"] != "ACTIVE" {
		t.Fatalf("accept %d %s", r.Status, r.Raw)
	}
	if r := k.Actor(mgr, "GET", "/mandates/"+mid+"/authorization?capability=maintenance:write", "", nil); r.Data()["allowed"] != true {
		t.Fatalf("authorization %s", r.Raw)
	}
	req := map[string]any{"kind": "CORRECTIVE", "title": "Leaking pipe", "reason": "Tenant report"}
	first := k.Actor(mgr, "POST", "/mandates/"+mid+"/maintenance-requests", "pm-key-0001", req)
	again := k.Actor(mgr, "POST", "/mandates/"+mid+"/maintenance-requests", "pm-key-0001", req)
	if first.Status != 201 || string(first.Raw) != string(again.Raw) {
		t.Fatalf("delegated action %d %s / %s", first.Status, first.Raw, again.Raw)
	}
	if r := k.Actor(other, "POST", "/mandates/"+mid+"/maintenance-requests", "pm-key-0002", req); r.Status != 404 {
		t.Fatalf("other manager %d", r.Status)
	}
	// Business revokes: the very next action is denied by Business even before
	// the event arrives; the event then revokes the mandate locally.
	business = "REVOKED"
	if r := k.Actor(mgr, "POST", "/mandates/"+mid+"/maintenance-requests", "pm-key-0003", req); r.Status != 403 {
		t.Fatalf("action after revocation %d %s", r.Status, r.Raw)
	}
	ev.Operation, ev.State, ev.Version = "REVOKE", "REVOKED", 3
	deliver(t, k, ev)
	got := k.Actor(mgr, "GET", "/mandates/"+mid, "", nil).Data()
	if got["mandate"].(map[string]any)["state"] != "REVOKED" || len(got["actions"].([]any)) != 1 {
		t.Fatalf("after revocation %v", got)
	}
	if r := k.Actor(mgr, "GET", "/mandates/"+mid+"/authorization?capability=maintenance:write", "", nil); r.Data()["allowed"] != false {
		t.Fatalf("authorization after revocation %s", r.Raw)
	}
	// Expiry sweep.
	past := time.Now().UTC().Add(-time.Hour)
	ev2 := domainkit.BusinessEvent{OrganisationID: org, TargetID: ids.New(), PropertyID: &property, Operation: "CREATED", State: "PROPOSED", Version: 1,
		Snapshot: domainkit.BusinessDetail{Domain: "manager_assignment", Kind: "FACILITY_MANAGER", Title: "Facility", CounterpartyReference: k.RT.PartnerReference(mgr), EndsAt: &past}}
	deliver(t, k, ev2)
	if err := usecases.Sweep(k.RT)(t.Context()); err != nil {
		t.Fatal(err)
	}
	var expired int
	if err := k.RT.Pool.QueryRow(t.Context(), `SELECT count(*) FROM mandates WHERE state='EXPIRED'`).Scan(&expired); err != nil || expired != 1 {
		t.Fatalf("expired %d %v", expired, err)
	}
	if _, err := k.RT.Pool.Exec(t.Context(), `UPDATE mandates SET state='ACTIVE',version=version+1 WHERE state='REVOKED'`); err == nil {
		t.Fatal("revoked mandate reactivated by direct SQL")
	}
	if r := k.Service(platform.BusinessOperationsService, org, "GET", "/mandates/by-assignment/"+assignment.String(), "", nil); r.Status != 200 || r.Data()["state"] != "REVOKED" {
		t.Fatalf("business readback %d %s", r.Status, r.Raw)
	}
}
