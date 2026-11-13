package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/artisan-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/artisan-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit/domaintest"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func businessEvent(t *testing.T, k *domaintest.Kit, eventType string, b domainkit.BusinessEvent) {
	t.Helper()
	env, err := events.New(context.Background(), platform.BusinessOperationsService, eventType, b.Snapshot.Domain, b.TargetID, b)
	if err != nil {
		t.Fatal(err)
	}
	h := usecases.BusinessHandler(k.RT)
	for range 2 { // redelivery is harmless
		if err = database.WithTx(t.Context(), k.RT.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error { return h(ctx, tx, env, b) }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestArtisanWorkLifecycleReportsToBusinessBeforeLocalCommit(t *testing.T) {
	k := domaintest.New(t, platform.ArtisanService, migrations.FS, usecases.New)
	if string(migrationsBase(t)) != domainkit.BaseMigration {
		t.Fatal("00001 platform migration drifted from domainkit.BaseMigration")
	}
	artisan, other := k.Account("ade@artisan.test"), k.Account("bola@artisan.test")
	org, property, workOrder, assignment, quote := ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	businessEvent(t, k, "maintenance.quote.updated.v1", domainkit.BusinessEvent{OrganisationID: org, TargetID: quote, PropertyID: &property, Operation: "CREATED", State: "REQUESTED", Version: 1,
		Snapshot: domainkit.BusinessDetail{Domain: "vendor_quote", ParentID: &workOrder, Title: "Boiler quote", CounterpartyReference: k.RT.PartnerReference(artisan), Attributes: map[string]any{"scope_of_work": "Replace valve"}}})
	businessEvent(t, k, "maintenance.assignment.updated.v1", domainkit.BusinessEvent{OrganisationID: org, TargetID: assignment, PropertyID: &property, Operation: "CREATED", State: "REQUESTED", Version: 1,
		Snapshot: domainkit.BusinessDetail{Domain: "provider_assignment", ParentID: &workOrder, Title: "Boiler repair", CounterpartyReference: k.RT.PartnerReference(artisan)}})
	// Work addressed to an unknown account or another service is ignored.
	businessEvent(t, k, "maintenance.assignment.updated.v1", domainkit.BusinessEvent{OrganisationID: org, TargetID: ids.New(), PropertyID: &property, Operation: "CREATED", State: "REQUESTED", Version: 1,
		Snapshot: domainkit.BusinessDetail{Domain: "provider_assignment", ParentID: &workOrder, Title: "Not ours", CounterpartyReference: "professional-services-service:account:" + artisan.String()}})
	quotes := k.Actor(artisan, "GET", "/quote-requests", "", nil).Items()
	work := k.Actor(artisan, "GET", "/work-requests", "", nil).Items()
	if len(quotes) != 1 || len(work) != 1 {
		t.Fatalf("projection quotes=%d work=%d", len(quotes), len(work))
	}
	if n := len(k.Actor(other, "GET", "/work-requests", "", nil).Items()); n != 0 {
		t.Fatalf("other artisan sees %d", n)
	}
	qid := quotes[0].(map[string]any)["id"].(string)
	wid := work[0].(map[string]any)["id"].(string)
	if r := k.Actor(artisan, "POST", "/quote-requests/"+qid+"/submit", "q1", map[string]any{"amount_minor": 120000, "currency": "NGN"}); r.Status != 400 {
		t.Fatalf("quote without evidence %d", r.Status)
	}
	if r := k.Actor(artisan, "POST", "/quote-requests/"+qid+"/submit", "q2", map[string]any{"amount_minor": 120000, "currency": "NGN", "evidence_refs": []ids.ID{ids.New()}}); r.Status != 200 || r.Data()["state"] != "SUBMITTED" {
		t.Fatalf("quote %d %s", r.Status, r.Raw)
	}
	// Business rejection leaves local state untouched.
	k.Business.Reply = func(c domaintest.Call) (int, any) { return 409, nil }
	if r := k.Actor(artisan, "POST", "/work-requests/"+wid+"/accept", "a1", map[string]any{}); r.Status != 409 {
		t.Fatalf("business rejection %d %s", r.Status, r.Raw)
	}
	if st := k.Actor(artisan, "GET", "/work-requests/"+wid, "", nil).Data()["work_request"].(map[string]any)["state"]; st != "OFFERED" {
		t.Fatalf("state changed despite Business rejection: %v", st)
	}
	k.Business.Reply = nil
	if r := k.Actor(other, "POST", "/work-requests/"+wid+"/accept", "a2", map[string]any{}); r.Status != 404 {
		t.Fatalf("other artisan accept %d", r.Status)
	}
	if r := k.Actor(artisan, "POST", "/work-requests/"+wid+"/accept", "a3", map[string]any{}); r.Status != 200 || r.Data()["state"] != "ACCEPTED" {
		t.Fatalf("accept %d %s", r.Status, r.Raw)
	}
	if r := k.Actor(artisan, "POST", "/work-requests/"+wid+"/arrive", "a4", map[string]any{}); r.Status != 400 {
		t.Fatalf("arrive without before evidence %d", r.Status)
	}
	before, after := ids.New(), ids.New()
	k.Actor(artisan, "POST", "/work-requests/"+wid+"/evidence", "e1", map[string]any{"stage": "BEFORE", "media_reference": before})
	if r := k.Actor(artisan, "POST", "/work-requests/"+wid+"/arrive", "a5", map[string]any{}); r.Status != 200 {
		t.Fatalf("arrive %d %s", r.Status, r.Raw)
	}
	k.Actor(artisan, "POST", "/work-requests/"+wid+"/evidence", "e2", map[string]any{"stage": "AFTER", "media_reference": after})
	done := k.Actor(artisan, "POST", "/work-requests/"+wid+"/complete", "a6", map[string]any{"warranty_days": 90})
	replay := k.Actor(artisan, "POST", "/work-requests/"+wid+"/complete", "a6", map[string]any{"warranty_days": 90})
	if done.Status != 200 || done.Data()["state"] != "COMPLETED" || string(replay.Raw) != string(done.Raw) {
		t.Fatalf("complete %d %s / replay %s", done.Status, done.Raw, replay.Raw)
	}
	// Every Business call was signed, organisation-scoped and evidence-backed.
	var completion domaintest.Call
	for _, c := range k.Business.Calls() {
		if c.Org != org || c.Caller != platform.ArtisanService || !strings.HasPrefix(c.Path, "/internal/v1/business-supply/partner-records/") {
			t.Fatalf("unexpected business call %+v", c)
		}
		if c.Body["action"] == "RECORD_COMPLETION" {
			completion = c
		}
	}
	if completion.Body == nil || len(completion.Body["evidence_refs"].([]any)) != 1 || completion.Body["attributes"].(map[string]any)["warranty_reference"] != "artisan-warranty:"+wid {
		t.Fatalf("completion call %+v", completion)
	}
	if n := len(k.Business.Calls()); n != 5 { // quote, rejected accept, accept, arrive, complete (replay not re-sent)
		t.Fatalf("business calls %d", n)
	}
	warranties := k.Actor(artisan, "GET", "/warranties", "", nil).Items()
	if len(warranties) != 1 || warranties[0].(map[string]any)["state"] != "ACTIVE" {
		t.Fatalf("warranties %v", warranties)
	}
	// Business cancellation of completed work is ignored; rework links.
	businessEvent(t, k, "maintenance.assignment.updated.v1", domainkit.BusinessEvent{OrganisationID: org, TargetID: ids.New(), PropertyID: &property, Operation: "CREATED", State: "REQUESTED", Version: 1,
		Snapshot: domainkit.BusinessDetail{Domain: "provider_assignment", ParentID: &workOrder, Title: "Rework valve", CounterpartyReference: k.RT.PartnerReference(artisan), Attributes: map[string]any{"rework_of": assignment.String()}}})
	var rework int
	if err := k.RT.Pool.QueryRow(t.Context(), `SELECT count(*) FROM work_requests WHERE kind='REWORK' AND rework_of=$1`, wid).Scan(&rework); err != nil || rework != 1 {
		t.Fatalf("rework %d %v", rework, err)
	}
	// Database guards: no illegal transition, no evidence rewrite.
	if _, err := k.RT.Pool.Exec(t.Context(), `UPDATE work_requests SET state='OFFERED',version=version+1 WHERE id=$1`, wid); err == nil {
		t.Fatal("completed work reopened")
	}
	if _, err := k.RT.Pool.Exec(t.Context(), `DELETE FROM work_evidence`); err == nil {
		t.Fatal("evidence deleted")
	}
	// Admin can suspend the artisan; the account is then refused.
	if r := k.Service(platform.AdminService, ids.Nil, "POST", "/accounts/"+artisan.String()+"/status", "s1", map[string]any{"status": "SUSPENDED", "reason": "Fraud review"}); r.Status != 200 {
		t.Fatalf("suspend %d %s", r.Status, r.Raw)
	}
	if r := k.Actor(artisan, "GET", "/work-requests", "", nil); r.Status != 403 {
		t.Fatalf("suspended artisan %d", r.Status)
	}
	if r := k.Service(platform.MarketplaceService, ids.Nil, "GET", "/work-requests/by-assignment/"+assignment.String(), "", nil); r.Status != 403 {
		t.Fatalf("marketplace internal readback %d", r.Status)
	}
	if r := k.Service(platform.BusinessOperationsService, org, "GET", "/work-requests/by-assignment/"+assignment.String(), "", nil); r.Status != 200 || r.Data()["state"] != "COMPLETED" {
		t.Fatalf("business readback %d %s", r.Status, r.Raw)
	}
	var registered int
	if err := k.RT.Pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE event_type='artisan.work_request.updated.v1'`).Scan(&registered); err != nil || registered < 4 {
		t.Fatalf("outbox events %d %v", registered, err)
	}
}
