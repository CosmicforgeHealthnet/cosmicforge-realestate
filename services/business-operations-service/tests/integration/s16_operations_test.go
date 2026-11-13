package integration

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

func opsRec(c *commercialHarness, u *businessUser, method, path string, body any, status int) businesshttp.OpsRecord {
	c.t.Helper()
	return commercialData[businesshttp.OpsRecord](c.t, c.must(u, method, path, body, status))
}

func opsMove(c *commercialHarness, u *businessUser, path string, rec businesshttp.OpsRecord, cmd businesshttp.OpsRecordTransition, status int) businesshttp.OpsRecord {
	c.t.Helper()
	cmd.Version = rec.Version
	if cmd.Reason == "" {
		cmd.Reason = "Governed operational transition"
	}
	if status != 200 {
		c.must(u, "POST", path+"/"+rec.ID.String()+"/transitions", cmd, status)
		return rec
	}
	return opsRec(c, u, "POST", path+"/"+rec.ID.String()+"/transitions", cmd, 200)
}

func TestBusinessHousekeepingRoomReadyGateInspectionSeparationAndMaintenanceBlock(t *testing.T) {
	c := newCommercialHTTP(t)
	cleaner := c.user()
	c.join(cleaner, c.owner, []string{"housekeeping:read", "housekeeping:write"})
	c.grant(c.approver, "room_status:inspect")
	c.grant(c.approver, "housekeeping:write")
	c.grant(c.approver, "housekeeping:read")
	task := opsRec(c, c.owner, "POST", "/housekeeping/tasks", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "TURNOVER", Title: "Turnover apartment 101", Reason: "Checkout turnover"}, 201)
	c.must(c.owner, "POST", "/housekeeping/tasks", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "TURNOVER", Title: "Duplicate live task", Reason: "Duplicate"}, 409)
	task = opsMove(c, c.owner, "/housekeeping/tasks", task, businesshttp.OpsRecordTransition{Action: "ASSIGN", AssigneeID: &cleaner.registered.AccountID}, 200)
	task = opsMove(c, cleaner, "/housekeeping/tasks", task, businesshttp.OpsRecordTransition{Action: "START"}, 200)
	opsMove(c, cleaner, "/housekeeping/tasks", task, businesshttp.OpsRecordTransition{Action: "SUBMIT_INSPECTION"}, 400)
	task = opsMove(c, cleaner, "/housekeeping/tasks", task, businesshttp.OpsRecordTransition{Action: "SUBMIT_INSPECTION", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	opsMove(c, cleaner, "/housekeeping/tasks", task, businesshttp.OpsRecordTransition{Action: "PASS"}, 403)
	// A room-blocking work order prevents release.
	wo := opsRec(c, c.owner, "POST", "/work-orders", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "REPAIR", Title: "Leaking tap", Attributes: map[string]any{"blocks_room": true, "warranty_days": 0}, Reason: "Found during cleaning"}, 201)
	opsMove(c, c.approver, "/housekeeping/tasks", task, businesshttp.OpsRecordTransition{Action: "PASS"}, 409)
	opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "CANCEL"}, 200)
	task = opsMove(c, c.approver, "/housekeeping/tasks", task, businesshttp.OpsRecordTransition{Action: "PASS"}, 200)
	if task.State != "DONE" || task.ClosedAt == nil {
		t.Fatalf("task %+v", task)
	}
	if _, err := c.db.Pool.Exec(t.Context(), `UPDATE housekeeping_tasks SET state='PLANNED',version=version+1 WHERE id=$1`, task.ID); err == nil {
		t.Fatal("terminal housekeeping task reopened")
	}
	other := c.user()
	if status, _ := c.call(other, "GET", "/housekeeping/tasks/"+task.ID.String(), nil, nil); status != 403 && status != 404 {
		t.Fatalf("cross-tenant %d", status)
	}
}

func TestBusinessWorkOrderQuoteApprovalEvidenceVerificationAndWarrantyRework(t *testing.T) {
	c := newCommercialHTTP(t)
	c.grant(c.approver, "maintenance:approve")
	c.grant(c.approver, "maintenance:write")
	c.grant(c.approver, "maintenance:read")
	wo := opsRec(c, c.owner, "POST", "/work-orders", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "REPAIR", Title: "Replace water heater", Attributes: map[string]any{"warranty_days": 30}, Reason: "Guest report"}, 201)
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "REQUEST_QUOTE", ExternalReference: "artisan-request-77"}, 200)
	amount := int64(85000)
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "RECORD_QUOTE", AmountMinor: &amount, Currency: "NGN", ExternalReference: "artisan-quote-77", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	c.must(c.owner, "POST", "/work-orders/"+wo.ID.String()+"/approve", businesshttp.OpsRecordTransition{Version: wo.Version, Reason: "Approve without independent approval"}, 403)
	approval := c.approval("COMMERCIAL_WORK_ORDER", "maintenance:approve", "", &amount, "NGN", map[string]any{"domain": "work_order", "record_id": wo.ID, "action": "APPROVE", "version": wo.Version})
	wo = opsRec(c, c.owner, "POST", "/work-orders/"+wo.ID.String()+"/approve", businesshttp.OpsRecordTransition{Version: wo.Version, ApprovalID: approval, Reason: "Independently approved quote"}, 200)
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "ASSIGN", ExternalReference: "artisan-job-77"}, 200)
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "START"}, 200)
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "COMPLETE", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "VERIFY", EvidenceRefs: []ids.ID{ids.New()}}, 403)
	wo = opsMove(c, c.approver, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "VERIFY", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "REWORK", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	if wo.State != "REWORK" {
		t.Fatal(wo.State)
	}
	var events int
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE event_type='maintenance.work_order.updated.v1' AND aggregate_id=$1`, wo.ID).Scan(&events); err != nil || events < 8 {
		t.Fatalf("work order events %d %v", events, err)
	}
}

func TestBusinessManagerDelegationScopedExpiringRevocableAndEnforced(t *testing.T) {
	c := newCommercialHTTP(t)
	ends := time.Now().UTC().Add(48 * time.Hour)
	c.must(c.owner, "POST", "/manager-assignments", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "PROPERTY_MANAGER", Title: "Managed letting", CounterpartyReference: "manager-co:42", EndsAt: &ends, Attributes: map[string]any{"capabilities": []string{"payouts:write"}}, Reason: "Overreaching delegation"}, 403)
	a := opsRec(c, c.owner, "POST", "/manager-assignments", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "PROPERTY_MANAGER", Title: "Managed letting", CounterpartyReference: "manager-co:42", EndsAt: &ends, Attributes: map[string]any{"capabilities": []string{"reservations:read", "maintenance:write"}, "mandate_reference": "verified-mandate-42"}, Reason: "Appoint verified manager"}, 201)
	opsMove(c, c.owner, "/manager-assignments", a, businesshttp.OpsRecordTransition{Action: "ACTIVATE", EvidenceRefs: []ids.ID{ids.New()}}, 403)
	approval := c.approval(approvals.ActionExternalManagerAccess, "approvals:decide", "", nil, "", map[string]any{"domain": "manager_assignment", "record_id": a.ID, "action": "ACTIVATE", "version": a.Version})
	a = opsMove(c, c.owner, "/manager-assignments", a, businesshttp.OpsRecordTransition{Action: "ACTIVATE", ApprovalID: approval, EvidenceRefs: []ids.ID{ids.New()}}, 200)
	token, err := c.supplySigner(platform.PropertyManagerService).TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: c.owner.registered.OrganisationID})
	if err != nil {
		t.Fatal(err)
	}
	check := func(capability string) bool {
		status, raw := c.rootGet("/internal/v1/business-supply/manager-authorizations/"+a.ID.String()+"?property_id="+c.property.ID.String()+"&capability="+capability, map[string]string{"Authorization": "Bearer " + token})
		if status != 200 {
			t.Fatalf("authorization check %d %s", status, raw)
		}
		return commercialData[map[string]any](t, raw)["allowed"] == true
	}
	if !check("maintenance:write") || check("finance:approve") {
		t.Fatal("delegated scope not enforced")
	}
	c.must(c.owner, "POST", "/manager-assignments/"+a.ID.String()+"/revoke", businesshttp.OpsRecordTransition{Version: a.Version, Reason: "Mandate ended"}, 200)
	if check("maintenance:write") {
		t.Fatal("revoked delegation still authorised")
	}
	// Expiry is applied by the worker without human sessions.
	b := opsRec(c, c.owner, "POST", "/manager-assignments", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "FACILITY_MANAGER", Title: "Facility", CounterpartyReference: "fm-co:7", EndsAt: &ends, Attributes: map[string]any{"capabilities": []string{"maintenance:write"}}, Reason: "Second"}, 201)
	approval = c.approval(approvals.ActionExternalManagerAccess, "approvals:decide", "", nil, "", map[string]any{"domain": "manager_assignment", "record_id": b.ID, "action": "ACTIVATE", "version": b.Version})
	b = opsMove(c, c.owner, "/manager-assignments", b, businesshttp.OpsRecordTransition{Action: "ACTIVATE", ApprovalID: approval, EvidenceRefs: []ids.ID{ids.New()}}, 200)
	if _, err = c.db.Pool.Exec(t.Context(), `UPDATE manager_assignment_refs SET ends_at=now()-interval '1 second',version=version+1 WHERE id=$1`, b.ID); err == nil {
		t.Log("expiry fixture applied")
	}
	if err = c.domain.ProcessOperations(t.Context(), 50); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = c.db.Pool.QueryRow(t.Context(), `SELECT state FROM manager_assignment_refs WHERE id=$1`, b.ID).Scan(&state); err != nil || state != "EXPIRED" {
		t.Fatalf("expiry %s %v", state, err)
	}
}

func TestBusinessProfessionalDataRoomGrantPurposeExpiryApprovalAndRevocation(t *testing.T) {
	c := newCommercialHTTP(t)
	room := opsRec(c, c.owner, "POST", "/data-rooms", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "DUE_DILIGENCE", Title: "Sale due diligence", Attributes: map[string]any{"document_refs": []string{ids.New().String()}}, Reason: "Open data room"}, 201)
	appt := opsRec(c, c.owner, "POST", "/professional-appointments", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "VALUER", Title: "Valuation", CounterpartyReference: "professional:valuer-9", Reason: "Appoint valuer"}, 201)
	ends := time.Now().UTC().Add(72 * time.Hour)
	grantPath := "/professional-appointments/" + appt.ID.String() + "/data-room-grants"
	body := businesshttp.OpsRecordCreate{SubjectID: &room.ID, Kind: "WATERMARKED_DOWNLOAD", Title: "Valuer access", EndsAt: &ends, Attributes: map[string]any{"purpose": "Valuation report preparation"}, Reason: "Grant evidence access"}
	c.must(c.owner, "POST", grantPath, body, 409) // appointment not yet accepted
	appt = opsMove(c, c.owner, "/professional-appointments", appt, businesshttp.OpsRecordTransition{Action: "ACCEPT", ExternalReference: "professional-acceptance-9"}, 200)
	c.must(c.owner, "POST", grantPath, body, 403) // approval required
	body.ApprovalID = c.approval(approvals.ActionProfessionalDataRoomGrant, "approvals:decide", "", nil, "", map[string]any{"domain": "data_room_grant", "action": "CREATE", "scope_id": c.property.ID, "parent_id": appt.ID, "subject_id": room.ID, "ends_at": ends, "kind": "WATERMARKED_DOWNLOAD"})
	grant := opsRec(c, c.owner, "POST", grantPath, body, 201)
	token, err := c.supplySigner(platform.MediaDocumentService).TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: c.owner.registered.OrganisationID})
	if err != nil {
		t.Fatal(err)
	}
	allowed := func() map[string]any {
		status, raw := c.rootGet("/internal/v1/business-supply/data-room-grants/"+grant.ID.String()+"/authorization", map[string]string{"Authorization": "Bearer " + token})
		if status != 200 {
			t.Fatalf("grant check %d %s", status, raw)
		}
		return commercialData[map[string]any](t, raw)
	}
	if v := allowed(); v["allowed"] != true || v["watermark"] != true {
		t.Fatalf("grant %+v", v)
	}
	c.must(c.owner, "POST", grantPath+"/"+grant.ID.String()+"/transitions", businesshttp.OpsRecordTransition{Version: grant.Version, Action: "REVOKE", Reason: "Report delivered"}, 200)
	if allowed()["allowed"] == true {
		t.Fatal("revoked grant still authorised")
	}
}

func TestBusinessDevelopmentAllocationMilestonesSnagsAndHandover(t *testing.T) {
	c := newCommercialHTTP(t)
	c.grant(c.approver, "sales:approve")
	c.grant(c.approver, "projects:write")
	c.grant(c.approver, "projects:read")
	branch := c.inventoryBranch(c.owner)
	dev := c.inventoryNode(c.owner, "/projects", map[string]any{"branch_id": branch, "code": "ESTATE-1", "name": "Lekki estate"})
	prop := c.inventoryNode(c.owner, "/properties", map[string]any{"parent_id": dev.ID, "code": "ESTATE-BLOCK-A", "name": "Block A", "capacity_code": "DEVELOPER"})
	unit := c.inventoryNode(c.owner, "/units", map[string]any{"parent_id": prop.ID, "code": "A-101", "name": "Unit A-101", "attributes": map[string]any{"classification": "APARTMENT"}})
	base := "/projects/" + dev.ID.String()
	due := time.Now().UTC().Add(90 * 24 * time.Hour)
	ms := opsRec(c, c.owner, "POST", base+"/milestones", businesshttp.OpsRecordCreate{Kind: "STRUCTURE", Title: "Superstructure complete", EndsAt: &due, Reason: "Plan milestone"}, 201)
	ms = opsMove(c, c.owner, base+"/milestones", ms, businesshttp.OpsRecordTransition{Action: "START"}, 200)
	ms = opsMove(c, c.owner, base+"/milestones", ms, businesshttp.OpsRecordTransition{Action: "REQUEST_CERTIFICATION", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	opsMove(c, c.owner, base+"/milestones", ms, businesshttp.OpsRecordTransition{Action: "CERTIFY", EvidenceRefs: []ids.ID{ids.New()}}, 403)
	ms = opsMove(c, c.approver, base+"/milestones", ms, businesshttp.OpsRecordTransition{Action: "CERTIFY", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	hold := time.Now().UTC().Add(72 * time.Hour)
	price := int64(45000000)
	c.must(c.owner, "POST", base+"/allocations", businesshttp.OpsRecordCreate{SubjectID: &c.unit.ID, Kind: "OFF_PLAN", Title: "Outside unit", CounterpartyReference: "buyer:outside", AmountMinor: &price, Currency: "NGN", EndsAt: &hold, Reason: "Wrong development"}, 403)
	alloc := opsRec(c, c.owner, "POST", base+"/allocations", businesshttp.OpsRecordCreate{SubjectID: &unit.ID, Kind: "OFF_PLAN", Title: "Allocation A-101", CounterpartyReference: "buyer:ade", AmountMinor: &price, Currency: "NGN", EndsAt: &hold, Reason: "Buyer hold"}, 201)
	c.must(c.owner, "POST", base+"/allocations", businesshttp.OpsRecordCreate{SubjectID: &unit.ID, Kind: "OFF_PLAN", Title: "Double hold", CounterpartyReference: "buyer:other", AmountMinor: &price, Currency: "NGN", EndsAt: &hold, Reason: "Second buyer"}, 409)
	alloc = opsMove(c, c.owner, base+"/allocations", alloc, businesshttp.OpsRecordTransition{Action: "RESERVE", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	c.grant(c.owner, "sales:approve")
	approval := c.approvalAt("COMMERCIAL_DEVELOPMENT_ALLOCATION", "sales:approve", "", &price, "NGN", map[string]any{"domain": "development_allocation", "record_id": alloc.ID, "action": "ALLOCATE", "version": alloc.Version}, "DEVELOPMENT", dev.ID)
	alloc = opsMove(c, c.owner, base+"/allocations", alloc, businesshttp.OpsRecordTransition{Action: "ALLOCATE", ApprovalID: approval}, 200)
	if string(alloc.Attributes) == "{}" {
		t.Fatal("milestone schedule not frozen")
	}
	alloc = opsMove(c, c.owner, base+"/allocations", alloc, businesshttp.OpsRecordTransition{Action: "CONTRACT", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	snag := opsRec(c, c.owner, "POST", base+"/snagging", businesshttp.OpsRecordCreate{SubjectID: &unit.ID, Kind: "FINISH", Title: "Cracked tile", Reason: "Pre-handover inspection"}, 201)
	opsMove(c, c.approver, base+"/allocations", alloc, businesshttp.OpsRecordTransition{Action: "HAND_OVER", EvidenceRefs: []ids.ID{ids.New()}}, 409)
	snag = opsMove(c, c.owner, base+"/snagging", snag, businesshttp.OpsRecordTransition{Action: "MARK_FIXED", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	opsMove(c, c.approver, base+"/snagging", snag, businesshttp.OpsRecordTransition{Action: "VERIFY", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	alloc = opsMove(c, c.approver, base+"/allocations", alloc, businesshttp.OpsRecordTransition{Action: "HAND_OVER", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	if alloc.State != "HANDED_OVER" {
		t.Fatal(alloc.State)
	}
	opsRec(c, c.owner, "POST", base+"/warranties", businesshttp.OpsRecordCreate{SubjectID: &unit.ID, Kind: "MEP", Title: "Water heater fault", CounterpartyReference: "buyer:ade", Reason: "Post-handover warranty"}, 201)
}

func TestBusinessRiskAppealSeparationSalesOffersAndTenancyConversion(t *testing.T) {
	c := newCommercialHTTP(t)
	c.grant(c.approver, "risk:write")
	c.grant(c.approver, "risk:read")
	c.must(c.owner, "POST", "/risk-cases", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "PAYMENT_FRAUD", Title: "Suspicious card pattern", Attributes: map[string]any{"nationality": "X"}, Reason: "Protected attribute"}, 400)
	rc := opsRec(c, c.owner, "POST", "/risk-cases", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "PAYMENT_FRAUD", Title: "Suspicious card pattern", Attributes: map[string]any{"signal": "velocity"}, Reason: "Signal review"}, 201)
	rc = opsMove(c, c.owner, "/risk-cases", rc, businesshttp.OpsRecordTransition{Action: "REVIEW", AssigneeID: &c.owner.registered.AccountID}, 200)
	rc = opsMove(c, c.owner, "/risk-cases", rc, businesshttp.OpsRecordTransition{Action: "PROPOSE_RESTRICTION", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	opsMove(c, c.owner, "/risk-cases", rc, businesshttp.OpsRecordTransition{Action: "APPROVE_RESTRICTION"}, 403)
	rc = opsMove(c, c.approver, "/risk-cases", rc, businesshttp.OpsRecordTransition{Action: "APPROVE_RESTRICTION"}, 200)
	rc = opsMove(c, c.owner, "/risk-cases", rc, businesshttp.OpsRecordTransition{Action: "APPEAL", EvidenceRefs: []ids.ID{ids.New()}}, 200)
	rc = opsMove(c, c.approver, "/risk-cases", rc, businesshttp.OpsRecordTransition{Action: "CLEAR"}, 200)
	if rc.State != "CLEARED" {
		t.Fatal(rc.State)
	}
	// Sales: offers, acceptance and privileged approval.
	c.grant(c.owner, "sales:approve")
	sale := opsRec(c, c.owner, "POST", "/sales", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "UNIT_SALE", Title: "Sale of apartment 101", Reason: "List for sale"}, 201)
	amount := int64(90000000)
	offer := opsRec(c, c.owner, "POST", "/sales/"+sale.ID.String()+"/offers", businesshttp.OpsRecordCreate{Kind: "CASH", Title: "Buyer offer", CounterpartyReference: "buyer:tola", AmountMinor: &amount, Currency: "NGN", Reason: "Offer received"}, 201)
	c.must(c.owner, "POST", "/sales/"+sale.ID.String()+"/offers/"+offer.ID.String()+"/decision", businesshttp.SaleOfferDecision{Version: offer.Version, Decision: "ACCEPT", Reason: "Accept best offer"}, 200)
	var state string
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM sale_transactions WHERE id=$1`, sale.ID).Scan(&state); err != nil || state != "UNDER_OFFER" {
		t.Fatalf("sale %s %v", state, err)
	}
	sale.Version += 1
	opsMove(c, c.owner, "/sales", sale, businesshttp.OpsRecordTransition{Action: "APPROVE"}, 403)
	// Residential: application approval then tenancy conversion.
	app := opsRec(c, c.owner, "POST", "/rental-applications", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "RESIDENTIAL_LONG_TERM", Title: "Application unit 101", CounterpartyReference: "applicant:bisi", Reason: "Application received"}, 201)
	app = opsMove(c, c.owner, "/rental-applications", app, businesshttp.OpsRecordTransition{Action: "SCREEN", ExternalReference: "screening-ref-1"}, 200)
	app = opsMove(c, c.approver, "/rental-applications", app, businesshttp.OpsRecordTransition{Action: "APPROVE"}, 403)
	c.grant(c.approver, "residential:write")
	app = opsMove(c, c.approver, "/rental-applications", app, businesshttp.OpsRecordTransition{Action: "APPROVE"}, 200)
	rent := int64(250000)
	start := time.Now().UTC().Add(24 * time.Hour)
	ten := opsRec(c, c.owner, "POST", "/tenancies", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "FIXED_TERM", Title: "Tenancy unit 101", CounterpartyReference: "tenant:bisi", AmountMinor: &rent, Currency: "NGN", StartsAt: &start, Attributes: map[string]any{"application_id": app.ID.String()}, Reason: "Tenancy from approved application"}, 201)
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM rental_applications WHERE id=$1`, app.ID).Scan(&state); err != nil || state != "CONVERTED" {
		t.Fatalf("application %s %v", state, err)
	}
	ten = opsMove(c, c.owner, "/tenancies", ten, businesshttp.OpsRecordTransition{Action: "SEND_FOR_SIGNATURE"}, 200)
	approval := c.approval("COMMERCIAL_TENANCY", "residential:write", "", &rent, "NGN", map[string]any{"domain": "tenancy", "record_id": ten.ID, "action": "ACTIVATE", "version": ten.Version})
	ten = opsMove(c, c.owner, "/tenancies", ten, businesshttp.OpsRecordTransition{Action: "ACTIVATE", ApprovalID: approval, EvidenceRefs: []ids.ID{ids.New()}}, 200)
	if ten.State != "ACTIVE" {
		t.Fatal(ten.State)
	}
}

func TestBusinessDomainStateGuardsMatchGeneratedTransitions(t *testing.T) {
	c := newCommercialHTTP(t)
	rows, err := c.db.Pool.Query(t.Context(), `SELECT c.relname,encode(t.tgargs,'escape') FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid WHERE t.tgname=c.relname||'_guard' AND t.tgfoid='business_ops_state_guard'::regproc`)
	if err != nil {
		t.Fatal(err)
	}
	guards := map[string]string{}
	for rows.Next() {
		var table, args string
		if err = rows.Scan(&table, &args); err != nil {
			t.Fatal(err)
		}
		guards[table] = args
	}
	rows.Close()
	if len(guards) < 60 {
		t.Fatalf("expected governed state guards, found %d", len(guards))
	}
	for _, table := range []string{"housekeeping_tasks", "work_orders", "manager_assignment_refs", "development_allocations", "risk_cases", "data_room_grants"} {
		if guards[table] == "" {
			t.Fatalf("missing database state guard for %s", table)
		}
	}
}

func (c *commercialHarness) rootGet(path string, headers map[string]string) (int, []byte) {
	c.t.Helper()
	request, e := http.NewRequest("GET", c.server.URL+path, nil)
	if e != nil {
		c.t.Fatal(e)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, e := http.DefaultClient.Do(request)
	if e != nil {
		c.t.Fatal(e)
	}
	defer response.Body.Close()
	result, e := io.ReadAll(response.Body)
	if e != nil {
		c.t.Fatal(e)
	}
	return response.StatusCode, result
}
