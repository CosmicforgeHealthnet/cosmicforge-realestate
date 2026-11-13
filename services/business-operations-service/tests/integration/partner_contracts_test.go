package integration

import (
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// partner signs as an owning partner service with organisation delegation.
type partner struct {
	c      *commercialHarness
	signer *serviceauth.Signer
	org    ids.ID
}

func (a *annexD) partner(svc platform.Service) *partner {
	return &partner{c: a.commercialHarness, signer: a.supplySigner(svc), org: a.owner.registered.OrganisationID}
}

func (p *partner) post(path string, body any, status int) []byte {
	p.c.t.Helper()
	tok, err := p.signer.TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: p.org})
	if err != nil {
		p.c.t.Fatal(err)
	}
	got, raw := p.c.rootCall("/internal/v1/business-supply"+path, body, map[string]string{"Authorization": "Bearer " + tok})
	if got != status {
		p.c.t.Fatalf("POST %s got %d want %d %s", path, got, status, raw)
	}
	return raw
}

func (p *partner) get(path string, status int) []byte {
	p.c.t.Helper()
	tok, _ := p.signer.TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: p.org})
	got, raw := p.c.rootGet("/internal/v1/business-supply"+path, map[string]string{"Authorization": "Bearer " + tok})
	if got != status {
		p.c.t.Fatalf("GET %s got %d want %d %s", path, got, status, raw)
	}
	return raw
}

func partnerRecord(t *testing.T, raw []byte) businesshttp.OpsRecord {
	return commercialData[businesshttp.PartnerRecord](t, raw).Record
}

// Artisan-reported provider outcomes, quotes and the counterparty binding;
// a different artisan, a different service or a non-allow-listed action is
// refused, and replays are idempotent.
func TestPartnerArtisanQuoteAssignmentEvidenceAndBinding(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	artisan, other := ids.New(), ids.New()
	ref := businesshttp.PartnerReference(platform.ArtisanService, artisan)
	art := a.partner(platform.ArtisanService)
	wo := opsRec(c, c.owner, "POST", "/work-orders", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, SubjectID: &c.unit.ID, Kind: "REPAIR", Title: "Fix boiler", Reason: "Guest report"}, 201)
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "REQUEST_QUOTE", ExternalReference: "artisan-request"}, 200)
	quote := opsRec(c, c.owner, "POST", "/work-orders/"+wo.ID.String()+"/quotes", businesshttp.OpsRecordCreate{Kind: "FIXED_PRICE", Title: "Boiler quote", CounterpartyReference: ref, Attributes: map[string]any{"scope_of_work": "Replace valve"}, Reason: "Request quote"}, 201)
	amount := int64(120000)
	art.post("/partner-records/vendor_quote/"+quote.ID.String()+"/transitions", businesshttp.PartnerTransition{AccountID: other, Action: "RECORD_QUOTE", AmountMinor: &amount, Currency: "NGN", ExternalReference: "artisan-quote-1", EvidenceRefs: evidence(), Reason: "Quote submitted"}, 404)
	art.post("/partner-records/vendor_quote/"+quote.ID.String()+"/transitions", businesshttp.PartnerTransition{AccountID: artisan, Action: "ACCEPT", Reason: "Self acceptance"}, 400)
	q := partnerRecord(t, art.post("/partner-records/vendor_quote/"+quote.ID.String()+"/transitions", businesshttp.PartnerTransition{AccountID: artisan, Action: "RECORD_QUOTE", AmountMinor: &amount, Currency: "NGN", ExternalReference: "artisan-quote-1", EvidenceRefs: evidence(), Reason: "Quote submitted"}, 200))
	if q.State != "RECEIVED" || *q.AmountMinor != amount {
		t.Fatalf("quote %+v", q)
	}
	replay := partnerRecord(t, art.post("/partner-records/vendor_quote/"+quote.ID.String()+"/transitions", businesshttp.PartnerTransition{AccountID: artisan, Action: "RECORD_QUOTE", AmountMinor: &amount, Currency: "NGN", ExternalReference: "artisan-quote-1", EvidenceRefs: evidence(), Reason: "Quote submitted"}, 200))
	if replay.Version != q.Version {
		t.Fatal("replayed partner action changed the record")
	}
	// Business staff accepts and approves the accepted quote amount.
	q = opsMove(c, c.approver, "/work-orders/"+wo.ID.String()+"/quotes", q, businesshttp.OpsRecordTransition{Action: "ACCEPT"}, 200)
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "RECORD_QUOTE", AmountMinor: &amount, Currency: "NGN", ExternalReference: "artisan-quote-1", EvidenceRefs: evidence()}, 200)
	approval := c.approval("COMMERCIAL_WORK_ORDER", "maintenance:approve", "", &amount, "NGN", map[string]any{"domain": "work_order", "record_id": wo.ID, "action": "APPROVE", "version": wo.Version})
	wo = opsRec(c, c.owner, "POST", "/work-orders/"+wo.ID.String()+"/approve", businesshttp.OpsRecordTransition{Version: wo.Version, ApprovalID: approval, Reason: "Approve"}, 200)
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "ASSIGN", ExternalReference: "artisan-job-1"}, 200)
	pa := opsRec(c, c.owner, "POST", "/work-orders/"+wo.ID.String()+"/provider-assignments", businesshttp.OpsRecordCreate{Kind: "ARTISAN", Title: "Boiler repair", CounterpartyReference: ref, Attributes: map[string]any{"scope_of_work": "Replace valve"}, Reason: "Assign"}, 201)
	// Events carry the counterparty so the Artisan service can project work.
	var snapshot string
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT payload->'snapshot'->>'counterparty_reference' FROM outbox_events WHERE event_type='maintenance.assignment.updated.v1' AND aggregate_id=$1 ORDER BY occurred_at LIMIT 1`, pa.ID).Scan(&snapshot); err != nil || snapshot != ref {
		t.Fatalf("event snapshot %q %v", snapshot, err)
	}
	path := "/partner-records/provider_assignment/" + pa.ID.String() + "/transitions"
	art.post(path, businesshttp.PartnerTransition{AccountID: artisan, Action: "RECORD_ACCEPTANCE", ExternalReference: "artisan-wr-1", Reason: "Artisan accepted"}, 200)
	art.post(path, businesshttp.PartnerTransition{AccountID: artisan, Action: "RECORD_COMPLETION", EvidenceRefs: evidence(), Reason: "Too early"}, 409)
	art.post(path, businesshttp.PartnerTransition{AccountID: artisan, Action: "RECORD_ARRIVAL", EvidenceRefs: evidence(), Reason: "On site"}, 200)
	done := partnerRecord(t, art.post(path, businesshttp.PartnerTransition{AccountID: artisan, Action: "RECORD_COMPLETION", EvidenceRefs: evidence(), Attributes: map[string]any{"warranty_days": 90, "warranty_reference": "artisan-warranty-1"}, Reason: "Completed"}, 200))
	if done.State != "COMPLETED" {
		t.Fatal(done.State)
	}
	readback := commercialData[businesshttp.PartnerRecord](t, art.get("/partner-records/provider_assignment/"+pa.ID.String()+"?account_id="+artisan.String(), 200))
	if readback.Attributes["warranty_reference"] != "artisan-warranty-1" {
		t.Fatalf("readback %+v", readback)
	}
	// Business verifies the work itself.
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "START"}, 200)
	wo = opsMove(c, c.owner, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "COMPLETE", EvidenceRefs: evidence()}, 200)
	wo = opsMove(c, c.approver, "/work-orders", wo, businesshttp.OpsRecordTransition{Action: "VERIFY", EvidenceRefs: evidence()}, 200)
	if wo.State != "VERIFIED" {
		t.Fatal(wo.State)
	}
	// Another partner service cannot act on artisan records.
	a.partner(platform.ProfessionalServicesService).post(path, businesshttp.PartnerTransition{AccountID: artisan, Action: "RECORD_DECLINE", Reason: "Forged caller"}, 403)
	var history int
	if err := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM operations_history WHERE entity_id=$1 AND actor_id IS NULL AND actor_service='business-operations-service'`, pa.ID).Scan(&history); err != nil || history < 3 {
		t.Fatalf("partner history %d %v", history, err)
	}
}

// Professional appointment, finding, verified progress certification and
// Business milestone approval; unverified professionals cannot certify.
func TestPartnerProfessionalAppointmentFindingCertificationAndGrant(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	pro := ids.New()
	ref := businesshttp.PartnerReference(platform.ProfessionalServicesService, pro)
	prof := a.partner(platform.ProfessionalServicesService)
	appt := opsRec(c, c.owner, "POST", "/professional-appointments", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "SURVEYOR", Title: "Survey", CounterpartyReference: ref, Attributes: map[string]any{"purpose": "Pre-sale survey"}, Reason: "Appoint"}, 201)
	path := "/partner-records/professional_appointment/" + appt.ID.String() + "/transitions"
	prof.post(path, businesshttp.PartnerTransition{AccountID: pro, Action: "ACCEPT", ExternalReference: "prof-booking-1", Reason: "Accept"}, 200)
	prof.post(path, businesshttp.PartnerTransition{AccountID: pro, Action: "START", Reason: "Start"}, 200)
	prof.post("/professional-appointments/"+appt.ID.String()+"/findings", businesshttp.ProfessionalFindingCreate{AccountID: pro, FindingKey: "finding-0001", Kind: "DEFECT", Title: "Roof leak", EvidenceRefs: evidence(), Reason: "Finding"}, 400)
	f := commercialData[businesshttp.OpsRecord](t, prof.post("/professional-appointments/"+appt.ID.String()+"/findings", businesshttp.ProfessionalFindingCreate{AccountID: pro, FindingKey: "finding-0001", Kind: "DEFECT", Title: "Roof leak", EvidenceRefs: evidence(), VerificationReference: "verification:prof-1", Reason: "Finding"}, 201))
	again := commercialData[businesshttp.OpsRecord](t, prof.post("/professional-appointments/"+appt.ID.String()+"/findings", businesshttp.ProfessionalFindingCreate{AccountID: pro, FindingKey: "finding-0001", Kind: "DEFECT", Title: "Roof leak", EvidenceRefs: evidence(), VerificationReference: "verification:prof-1", Reason: "Finding"}, 200))
	if again.ID != f.ID {
		t.Fatal("finding duplicated")
	}
	prof.post(path, businesshttp.PartnerTransition{AccountID: pro, Action: "SUBMIT_REPORT", EvidenceRefs: evidence(), Reason: "Unverified"}, 403)
	prof.post(path, businesshttp.PartnerTransition{AccountID: pro, Action: "SUBMIT_REPORT", EvidenceRefs: evidence(), Attributes: map[string]any{"professional_verification_reference": "verification:prof-1"}, Reason: "Report"}, 200)
	// Purpose-limited document authorization for the appointed professional.
	room := opsRec(c, c.owner, "POST", "/data-rooms", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "SALE", Title: "Sale room", Reason: "Room"}, 201)
	ends := time.Now().UTC().Add(48 * time.Hour)
	op := map[string]any{"domain": "data_room_grant", "action": "CREATE", "scope_id": c.property.ID, "parent_id": appt.ID, "subject_id": room.ID, "ends_at": ends, "kind": "VIEW_ONLY"}
	grantApproval := c.approval("PROFESSIONAL_DATA_ROOM_GRANT", "approvals:decide", "", nil, "", op)
	grant := opsRec(c, c.owner, "POST", "/professional-appointments/"+appt.ID.String()+"/data-room-grants", businesshttp.OpsRecordCreate{SubjectID: &room.ID, Kind: "VIEW_ONLY", Title: "Survey access", EndsAt: &ends, ApprovalID: grantApproval, Attributes: map[string]any{"purpose": "Survey document review"}, Reason: "Grant"}, 201)
	auth := commercialData[map[string]any](t, prof.get("/data-room-grants/"+grant.ID.String()+"/authorization?account_id="+pro.String(), 200))
	if auth["allowed"] != true {
		t.Fatalf("grant %v", auth)
	}
	prof.get("/data-room-grants/"+grant.ID.String()+"/authorization?account_id="+ids.New().String(), 404)
	// Development certification evidence -> Business milestone approval.
	branch := c.inventoryBranch(c.owner)
	dev := c.inventoryNode(c.owner, "/projects", map[string]any{"branch_id": branch, "code": "PARTNER-DEV", "name": "Partner estate"})
	base := "/projects/" + dev.ID.String()
	due := time.Now().UTC().Add(30 * 24 * time.Hour)
	ms := opsRec(c, c.owner, "POST", base+"/milestones", businesshttp.OpsRecordCreate{Kind: "STRUCTURE", Title: "Structure", EndsAt: &due, Reason: "Plan"}, 201)
	ms = opsMove(c, c.owner, base+"/milestones", ms, businesshttp.OpsRecordTransition{Action: "START"}, 200)
	ms = opsMove(c, c.owner, base+"/milestones", ms, businesshttp.OpsRecordTransition{Action: "REQUEST_CERTIFICATION", EvidenceRefs: evidence()}, 200)
	cert := opsRec(c, c.owner, "POST", base+"/progress-certifications", businesshttp.OpsRecordCreate{Kind: "INTERIM", Title: "Structure certificate", CounterpartyReference: ref, Attributes: map[string]any{"milestone_id": ms.ID.String(), "percent_complete": 100}, Reason: "Request certification"}, 201)
	cpath := "/partner-records/progress_certification/" + cert.ID.String() + "/transitions"
	prof.post(cpath, businesshttp.PartnerTransition{AccountID: pro, Action: "START_REVIEW", ExternalReference: "prof-review-1", Reason: "Review"}, 200)
	prof.post(cpath, businesshttp.PartnerTransition{AccountID: pro, Action: "CERTIFY", ExternalReference: "qs-cert-1", EvidenceRefs: evidence(), Reason: "No verification"}, 403)
	prof.post(cpath, businesshttp.PartnerTransition{AccountID: pro, Action: "CERTIFY", ExternalReference: "qs-cert-1", EvidenceRefs: evidence(), Attributes: map[string]any{"professional_verification_reference": "verification:prof-1"}, Reason: "Certified"}, 200)
	ms = opsMove(c, c.approver, base+"/milestones", ms, businesshttp.OpsRecordTransition{Action: "CERTIFY", EvidenceRefs: evidence()}, 200)
	if ms.State != "CERTIFIED" {
		t.Fatal(ms.State)
	}
}

// Admin decision on a submitted dispute is recorded as the authoritative
// outcome; Business then resolves it.
func TestPartnerAdminDisputeDecision(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	adm := a.partner(platform.AdminService)
	d := opsRec(c, c.owner, "POST", "/disputes", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "DEPOSIT", Title: "Deposit dispute", CounterpartyReference: "tenant:ngozi", Attributes: map[string]any{"claim_summary": "Deduction contested"}, Reason: "Dispute"}, 201)
	path := "/partner-records/dispute/" + d.ID.String() + "/transitions"
	adm.post(path, businesshttp.PartnerTransition{Action: "RECORD_DECISION", ExternalReference: "admin-case-1", Reason: "Too early"}, 409)
	d = opsMove(c, c.owner, "/disputes", d, businesshttp.OpsRecordTransition{Action: "COLLECT_EVIDENCE"}, 200)
	d = opsMove(c, c.owner, "/disputes", d, businesshttp.OpsRecordTransition{Action: "SUBMIT", ExternalReference: "admin-intake", EvidenceRefs: evidence()}, 200)
	a.partner(platform.ArtisanService).post(path, businesshttp.PartnerTransition{Action: "RECORD_DECISION", ExternalReference: "x-case", Reason: "Not admin"}, 403)
	got := partnerRecord(t, adm.post(path, businesshttp.PartnerTransition{Action: "RECORD_DECISION", ExternalReference: "admin-case-1", Attributes: map[string]any{"outcome": "PARTIALLY_UPHELD", "decided_by_admin": "admin:reviewer"}, Reason: "Admin decision"}, 200))
	if got.State != "DECIDED" || got.ExternalReference != "admin-case-1" {
		t.Fatalf("decision %+v", got)
	}
	d.Version = got.Version
	d = opsMove(c, c.owner, "/disputes", d, businesshttp.OpsRecordTransition{Action: "RESOLVE", EvidenceRefs: evidence()}, 200)
	if d.State != "RESOLVED" {
		t.Fatal(d.State)
	}
}

// Delegated manager action is re-validated live: allowed while the mandate
// is active with maintenance:write, idempotent per request key, and refused
// after revocation.
func TestPartnerPropertyManagerDelegatedActionAndRevocation(t *testing.T) {
	a := newAnnexD(t)
	c := a.commercialHarness
	mgr := ids.New()
	pm := a.partner(platform.PropertyManagerService)
	ends := time.Now().UTC().Add(30 * 24 * time.Hour)
	as := opsRec(c, c.owner, "POST", "/manager-assignments", businesshttp.OpsRecordCreate{ScopeID: &c.property.ID, Kind: "PROPERTY_MANAGER", Title: "Managed property", CounterpartyReference: businesshttp.PartnerReference(platform.PropertyManagerService, mgr), EndsAt: &ends, Attributes: map[string]any{"capabilities": []string{"maintenance:write"}, "mandate_reference": "mandate-1"}, Reason: "Appoint"}, 201)
	req := businesshttp.ManagerActionRequest{AccountID: mgr, RequestKey: "pm-req-00001", Kind: "CORRECTIVE", Title: "Leaking pipe", Reason: "Reported by tenant"}
	pm.post("/manager-assignments/"+as.ID.String()+"/maintenance-requests", req, 403)
	approval := c.approval("EXTERNAL_MANAGER_ACCESS", "approvals:decide", "", nil, "", map[string]any{"domain": "manager_assignment", "record_id": as.ID, "action": "ACTIVATE", "version": as.Version})
	as = opsMove(c, c.owner, "/manager-assignments", as, businesshttp.OpsRecordTransition{Action: "ACTIVATE", ApprovalID: approval, EvidenceRefs: evidence()}, 200)
	detail := commercialData[businesshttp.PartnerRecord](t, pm.get("/partner-records/manager_assignment/"+as.ID.String()+"?account_id="+mgr.String(), 200))
	if detail.Record.State != "ACTIVE" || detail.Attributes["capabilities"] == nil {
		t.Fatalf("detail %+v", detail)
	}
	mr := commercialData[businesshttp.OpsRecord](t, pm.post("/manager-assignments/"+as.ID.String()+"/maintenance-requests", req, 201))
	mr2 := commercialData[businesshttp.OpsRecord](t, pm.post("/manager-assignments/"+as.ID.String()+"/maintenance-requests", req, 200))
	if mr.ID != mr2.ID || mr.State != "OPEN" {
		t.Fatalf("delegated request %+v %+v", mr, mr2)
	}
	req.AccountID = ids.New()
	pm.post("/manager-assignments/"+as.ID.String()+"/maintenance-requests", req, 404)
	req.AccountID = mgr
	c.must(c.owner, "POST", "/manager-assignments/"+as.ID.String()+"/revoke", businesshttp.OpsRecordTransition{Version: as.Version, Reason: "Mandate ended"}, 200)
	req.RequestKey = "pm-req-00002"
	pm.post("/manager-assignments/"+as.ID.String()+"/maintenance-requests", req, 403)
}
