package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

func artisan() *serviceauth.Delegation {
	return &serviceauth.Delegation{ActorDomain: platform.DomainArtisan, ActorID: ids.New()}
}

func professional() *serviceauth.Delegation {
	return &serviceauth.Delegation{ActorDomain: platform.DomainProfessionalServices, ActorID: ids.New()}
}

// providerMedia registers a file uploaded by a provider in its own domain.
func (h *harness) providerMedia(d *serviceauth.Delegation, class string) ids.ID {
	id := ids.New()
	h.media.mu.Lock()
	h.media.files[id] = media.Media{MediaID: id, OwnerDomain: string(d.ActorDomain), OwnerType: "USER", OwnerID: d.ActorID, MediaClass: class,
		Status: "READY", MimeType: "image/jpeg", AccessClassification: "PRIVATE"}
	h.media.mu.Unlock()
	return id
}

func (h *harness) asProvider(d *serviceauth.Delegation, path string, body any) result {
	h.t.Helper()
	svc := platform.ArtisanService
	if d.ActorDomain == platform.DomainProfessionalServices {
		svc = platform.ProfessionalServicesService
	}
	return h.do(call{method: "POST", path: internal + path, as: svc, delegate: d, key: key(), body: body})
}

func (h *harness) maintenance(o *owner, id ids.ID) usecases.MaintenanceView {
	h.t.Helper()
	r := h.get(o, "/maintenance/"+id.String())
	h.expect(r, 200, "")
	var v usecases.MaintenanceView
	r.data(h.t, &v)
	return v
}

// activeManager appoints and activates a MAINTENANCE_ONLY manager.
func (h *harness) activeManager(o *owner, pid ids.ID, spend, emergency int64, rule string) *serviceauth.Delegation {
	h.t.Helper()
	mgr := &serviceauth.Delegation{ActorDomain: platform.DomainPropertyManager, ActorID: ids.New()}
	r := h.post(o, "/manager-proposals", map[string]any{"property_id": pid, "manager_domain": "property-manager", "manager_ref_id": mgr.ActorID,
		"manager_display_name": "Fixit Managers", "role_type": "MAINTENANCE_ONLY", "fee_type": "FIXED_MONTHLY", "fee_amount_minor": 100000, "fee_currency": "NGN",
		"capabilities": []string{"property.view", "maintenance.request", "maintenance.approve"}})
	h.expect(r, 201, "")
	var pr usecases.ProposalView
	r.data(h.t, &pr)
	now := h.clock.now()
	r = h.post(o, "/manager-assignments", map[string]any{"proposal_id": pr.ID, "starts_at": now, "ends_at": now.Add(90 * 24 * time.Hour),
		"spend_limit_minor": spend, "emergency_limit_minor": emergency, "emergency_rule": rule, "currency": "NGN"})
	h.expect(r, 201, "")
	var as usecases.AssignmentView
	r.data(h.t, &as)
	h.expect(h.do(call{method: "POST", path: internal + "/manager-assignments/" + as.ID.String() + "/respond", as: platform.PropertyManagerService,
		delegate: mgr, key: key(), body: map[string]any{"decision": "ACCEPT"}}), 200, "")
	return mgr
}

func TestMaintenancePlatformQuotesManagerLimitsCompletionAndWarranty(t *testing.T) {
	h := newHarness(t, withPayment)
	o := h.register(true)
	p := h.createProperty(o, "8 Repair Lane")
	r := h.post(o, "/properties/"+p.ID.String()+"/assets", map[string]any{"asset_type": "GENERATOR", "name": "Backup generator"})
	h.expect(r, 201, "")
	var asset usecases.AssetView
	r.data(t, &asset)
	mgr := h.activeManager(o, p.ID, 5000000, 1000000, "NOTIFY_OWNER")

	photo := h.media.put(o.id, "IMAGE", "READY")
	r = h.post(o, "/maintenance", map[string]any{"property_id": p.ID, "asset_id": asset.ID, "title": "Generator will not start", "description": "Cranks but no ignition.",
		"category": "ELECTRICAL", "priority": "HIGH", "evidence_media_ids": []ids.ID{photo}})
	h.expect(r, 201, "")
	var m usecases.MaintenanceView
	r.data(t, &m)
	mp := "/maintenance/" + m.ID.String()
	if m.Status != "OPEN" || m.Responsibility != "PENDING_REVIEW" || m.ReportedByType != "OWNER" || m.Currency != "NGN" {
		t.Fatalf("created %+v", m)
	}
	h.expect(h.post(o, mp+"/request-quotes", map[string]any{"sourcing": "PLATFORM"}), 409, "INVALID_STATE_TRANSITION")
	h.expect(h.write(o, "PATCH", mp, m.Version, map[string]any{"priority": "HIGH", "responsibility": "OWNER", "budget_minor": 20000000}), 200, "")
	h.expect(h.write(o, "PATCH", mp, m.Version, map[string]any{"priority": "HIGH", "responsibility": "OWNER"}), 409, "VERSION_CONFLICT")
	h.expect(h.post(o, mp+"/request-quotes", map[string]any{"sourcing": "PLATFORM"}), 200, "")
	if h.outboxTypes(m.ID)["maintenance.request.created.v1"] != 1 {
		t.Fatalf("discovery event %v", h.outboxTypes(m.ID))
	}

	// Providers quote through artisan-service; repeats are idempotent.
	a1, a2 := artisan(), artisan()
	q1 := map[string]any{"external_quote_ref": "aq-1", "provider_display_name": "Volt Bros", "scope": "Replace starter motor", "amount_minor": 8000000,
		"currency": "NGN", "warranty_days": 90}
	h.expect(h.do(call{method: "POST", path: internal + mp + "/provider-quotes", as: platform.MarketplaceService, delegate: customer(), key: key(), body: q1}), 403, "")
	h.expect(h.asProvider(a1, mp+"/provider-quotes", q1), 200, "")
	h.expect(h.asProvider(a1, mp+"/provider-quotes", q1), 200, "")
	h.expect(h.asProvider(a2, mp+"/provider-quotes", q1), 409, "EXTERNAL_REF_IN_USE")
	r = h.asProvider(a2, mp+"/provider-quotes", map[string]any{"external_quote_ref": "aq-2", "provider_display_name": "Spark Co", "scope": "Clean injectors, new plug",
		"amount_minor": 4000000, "currency": "NGN", "warranty_days": 90})
	h.expect(r, 200, "")
	m = h.maintenance(o, m.ID)
	if m.Status != "APPROVAL_PENDING" || len(m.Quotes) != 2 {
		t.Fatalf("quotes %+v", m)
	}
	var expensive, cheap usecases.QuoteView
	for _, q := range m.Quotes {
		if q.AmountMinor == 8000000 {
			expensive = q
		} else {
			cheap = q
		}
	}

	// The manager approves only within its spend limit.
	mm := internal + "/managed" + mp
	h.expect(h.do(call{method: "POST", path: mm + "/approve", as: platform.PropertyManagerService, delegate: mgr, key: key(),
		body: map[string]any{"decision": "APPROVE", "quote_id": expensive.ID, "payment_collection": "PLATFORM"}}), 403, "APPROVAL_LIMIT_EXCEEDED")
	h.expect(h.do(call{method: "POST", path: mm + "/approve", as: platform.PropertyManagerService, delegate: mgr, key: key(),
		body: map[string]any{"decision": "APPROVE", "quote_id": cheap.ID, "payment_collection": "PLATFORM"}}), 200, "")
	m = h.maintenance(o, m.ID)
	if m.Status != "ASSIGNED" || m.Job == nil || *m.Job.ProviderRef != a2.ActorID || len(m.Approvals) != 1 || m.Approvals[0].ApprovedVia != "MANAGER" ||
		*m.Approvals[0].LimitMinor != 5000000 {
		t.Fatalf("approved %+v", m)
	}
	job := "/maintenance-jobs/" + m.Job.ID.String() + "/provider-events"

	// Job records: only the assigned provider; completion needs photos.
	h.expect(h.asProvider(a1, job, map[string]any{"event_id": "e1", "event_type": "ATTENDED"}), 404, "")
	h.expect(h.asProvider(a2, job, map[string]any{"event_id": "e1", "event_type": "COMPLETION_SUBMITTED"}), 422, "EVIDENCE_REQUIRED")
	h.expect(h.asProvider(a2, job, map[string]any{"event_id": "e1", "event_type": "ATTENDED"}), 200, "")
	h.expect(h.asProvider(a2, job, map[string]any{"event_id": "e2", "event_type": "COMPLETION_SUBMITTED", "evidence_media_ids": []ids.ID{photo}}), 422, "MEDIA_NOT_OWNED")
	done := h.providerMedia(a2, "IMAGE")
	h.expect(h.asProvider(a2, job, map[string]any{"event_id": "e2", "event_type": "COMPLETION_SUBMITTED", "evidence_media_ids": []ids.ID{done}}), 200, "")
	h.expect(h.asProvider(a2, job, map[string]any{"event_id": "e2", "event_type": "COMPLETION_SUBMITTED", "evidence_media_ids": []ids.ID{done}}), 200, "")
	m = h.maintenance(o, m.ID)
	if m.Status != "COMPLETION_REVIEW" || len(m.Job.Events) != 2 {
		t.Fatalf("submitted %+v", m.Job)
	}

	// Rework keeps the earlier evidence; the provider resubmits and invoices.
	h.expect(h.post(o, mp+"/completion", map[string]any{"decision": "REWORK"}), 400, "")
	h.expect(h.post(o, mp+"/completion", map[string]any{"decision": "REWORK", "note": "Still stalls under load"}), 200, "")
	h.expect(h.asProvider(a2, job, map[string]any{"event_id": "e3", "event_type": "PROGRESS", "note": "Replaced fuel filter"}), 200, "")
	if got := h.maintenance(o, m.ID); got.Status != "IN_PROGRESS" || got.Job.ReworkCount != 1 {
		t.Fatalf("rework %s %d", got.Status, got.Job.ReworkCount)
	}
	h.expect(h.asProvider(a2, job, map[string]any{"event_id": "e4", "event_type": "COMPLETION_SUBMITTED", "evidence_media_ids": []ids.ID{h.providerMedia(a2, "IMAGE")}}), 200, "")
	h.expect(h.asProvider(a2, job, map[string]any{"event_id": "e5", "event_type": "INVOICE", "amount_minor": 4500000}), 200, "")
	h.expect(h.post(o, mp+"/completion", map[string]any{"decision": "ACCEPT"}), 200, "")
	m = h.maintenance(o, m.ID)
	if m.Status != "COMPLETED" || m.Job.Status != "ACCEPTED" || m.Warranty == nil || m.Warranty.Status != "ACTIVE" || len(m.Job.Events) != 7 {
		t.Fatalf("accepted %+v", m)
	}
	if h.count(`SELECT count(*) FROM property_assets WHERE id=$1 AND last_serviced_on IS NOT NULL`, asset.ID) != 1 {
		t.Fatal("asset service date not recorded")
	}
	if h.count(`SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='maintenance.job.invoice_above_approval'`, m.ID) != 1 {
		t.Fatal("invoice above approval not audited")
	}

	// Payment: capped at the approved amount, payee is the artisan.
	h.runJobs("maintenance-preventive-and-payments")
	var req payment.IntentRequest
	h.pay.mu.Lock()
	for _, rq := range h.pay.requests {
		if rq.ReferenceID == m.Job.ID {
			req = rq
		}
	}
	h.pay.mu.Unlock()
	if req.AmountMinor != 4000000 || req.PayeeDomain != "artisan" || req.PayeeRefID == nil || *req.PayeeRefID != a2.ActorID || req.Purpose != payment.PurposeMaintenanceJob {
		t.Fatalf("intent %+v", req)
	}
	h.pay.settle(m.Job.ID, 4000000, payment.IntentSucceeded)
	h.clock.advance(time.Second)
	h.runJobs("maintenance-preventive-and-payments")
	if got := h.maintenance(o, m.ID); got.Job.PaidMinor != 4000000 || *got.Job.PaymentStatus != payment.IntentSucceeded {
		t.Fatalf("paid %+v", got.Job)
	}

	// Warranty claim: a linked request routed to the original provider.
	r = h.post(o, mp+"/warranty-claim", map[string]any{"description": "Stalls again", "evidence_media_ids": []ids.ID{h.media.put(o.id, "IMAGE", "READY")}})
	h.expect(r, 201, "")
	var claim usecases.MaintenanceView
	r.data(t, &claim)
	if claim.Status != "QUOTING" || claim.Responsibility != "WARRANTY" || claim.WarrantyOfRequestID == nil || *claim.WarrantyOfRequestID != m.ID {
		t.Fatalf("warranty claim %+v", claim)
	}
	env, _ := h.outboxEnvelopeOf(claim.ID, "maintenance.request.created.v1")
	var payload struct {
		Payload usecases.MaintenanceEvent `json:"payload"`
	}
	_ = json.Unmarshal(env, &payload)
	if payload.Payload.ProviderRef == nil || *payload.Payload.ProviderRef != a2.ActorID {
		t.Fatalf("warranty routing %s", env)
	}
	h.expect(h.post(o, mp+"/warranty-claim", map[string]any{"description": "Again", "evidence_media_ids": []ids.ID{h.media.put(o.id, "IMAGE", "READY")}}), 409, "NO_ACTIVE_WARRANTY")
	h.validateEvents(m.ID)
	h.validateEvents(claim.ID)
}

func TestMaintenanceSelfSourcedDisputePreventiveAndCancel(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	p := h.createProperty(o, "9 Repair Lane")
	r := h.post(o, "/maintenance", map[string]any{"property_id": p.ID, "title": "Leaking roof", "description": "Water in the bedroom.", "category": "ROOFING",
		"priority": "NORMAL"})
	h.expect(r, 201, "")
	var m usecases.MaintenanceView
	r.data(t, &m)
	mp := "/maintenance/" + m.ID.String()
	h.expect(h.write(o, "PATCH", mp, m.Version, map[string]any{"priority": "NORMAL", "responsibility": "OWNER", "budget_minor": 1000000}), 200, "")
	h.expect(h.post(o, mp+"/request-quotes", map[string]any{"sourcing": "SELF_SOURCED"}), 200, "")
	h.expect(h.post(o, mp+"/quotes", map[string]any{"provider_display_name": "My roofer", "scope": "Patch and reseal", "amount_minor": 1500000}), 400, "")
	h.expect(h.post(o, mp+"/quotes", map[string]any{"provider_display_name": "My roofer", "scope": "Patch and reseal", "amount_minor": 1500000,
		"warranty_days": 30, "evidence_media_ids": []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")}}), 201, "")
	m = h.maintenance(o, m.ID)
	if m.Status != "APPROVAL_PENDING" || len(m.Quotes) != 1 || m.Quotes[0].Source != "OWNER_RECORDED" || m.Quotes[0].ValidUntil == nil {
		t.Fatalf("self quote %+v", m)
	}
	q := m.Quotes[0].ID
	h.expect(h.post(o, mp+"/approve", map[string]any{"decision": "APPROVE", "quote_id": q, "payment_collection": "PLATFORM"}), 503, "FEATURE_UNAVAILABLE")
	h.expect(h.post(o, mp+"/approve", map[string]any{"decision": "APPROVE", "quote_id": q, "payment_collection": "OFF_PLATFORM"}), 422, "OVER_BUDGET")
	h.expect(h.post(o, mp+"/approve", map[string]any{"decision": "APPROVE", "quote_id": q, "payment_collection": "OFF_PLATFORM", "exceed_budget": true}), 200, "")

	// Self-sourced progress is recorded by the owner; a disputed completion
	// is decided by an operator.
	h.expect(h.post(o, mp+"/job-events", map[string]any{"event_id": "s1", "event_type": "PAYMENT_ACKNOWLEDGED"}), 400, "")
	h.expect(h.post(o, mp+"/job-events", map[string]any{"event_id": "s1", "event_type": "COMPLETION_SUBMITTED",
		"evidence_media_ids": []ids.ID{h.media.put(o.id, "IMAGE", "READY")}}), 200, "")
	h.expect(h.post(o, mp+"/completion", map[string]any{"decision": "DISPUTE", "note": "Leak persists"}), 200, "")
	h.expect(h.admin("POST", "/admin/maintenance/"+m.ID.String()+"/resolution", map[string]any{"outcome": "REWORK"}), 200, "")
	h.expect(h.post(o, mp+"/job-events", map[string]any{"event_id": "s2", "event_type": "PROGRESS"}), 200, "")
	h.expect(h.post(o, mp+"/job-events", map[string]any{"event_id": "s3", "event_type": "COMPLETION_SUBMITTED",
		"evidence_media_ids": []ids.ID{h.media.put(o.id, "IMAGE", "READY")}}), 200, "")
	h.expect(h.post(o, mp+"/offline-payment", map[string]any{"amount_minor": 1500000, "evidence_media_ids": []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")}}), 409, "PAYMENT_NOT_RECORDABLE")
	h.expect(h.post(o, mp+"/completion", map[string]any{"decision": "ACCEPT"}), 200, "")
	h.expect(h.post(o, mp+"/offline-payment", map[string]any{"amount_minor": 2000000, "evidence_media_ids": []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")}}), 422, "ABOVE_APPROVED_AMOUNT")
	h.expect(h.post(o, mp+"/offline-payment", map[string]any{"amount_minor": 1500000, "evidence_media_ids": []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")}}), 200, "")
	if got := h.maintenance(o, m.ID); got.Status != "COMPLETED" || *got.Job.PaymentStatus != "OWNER_ATTESTED" || got.Job.ReworkCount != 1 {
		t.Fatalf("self-sourced done %+v", got.Job)
	}

	// Preventive schedules create one triaged task per due date.
	first := h.clock.now().AddDate(0, 0, 5).Format(time.DateOnly)
	r = h.post(o, "/properties/"+p.ID.String()+"/preventive-schedules", map[string]any{"title": "Service water pump", "category": "PLUMBING",
		"interval_days": 90, "first_due_on": first})
	h.expect(r, 201, "")
	var sched usecases.PreventiveView
	r.data(t, &sched)
	h.runJobs("maintenance-preventive-and-payments")
	h.runJobs("maintenance-preventive-and-payments")
	if n := h.count(`SELECT count(*) FROM maintenance_requests WHERE preventive_schedule_id=$1 AND status='TRIAGED' AND reported_by_type='SYSTEM'`, sched.ID); n != 1 {
		t.Fatalf("preventive tasks %d", n)
	}
	var list []usecases.PreventiveView
	h.get(o, "/properties/"+p.ID.String()+"/preventive-schedules").data(t, &list)
	if len(list) != 1 || list[0].NextDueOn != h.clock.now().AddDate(0, 0, 95).Format(time.DateOnly) || list[0].LastGeneratedOn == nil {
		t.Fatalf("schedule advanced %+v", list)
	}
	h.expect(h.write(o, "PATCH", "/preventive-schedules/"+sched.ID.String(), list[0].Version, map[string]any{"title": "Service water pump",
		"interval_days": 90, "lead_days": 7, "next_due_on": list[0].NextDueOn, "priority": "NORMAL", "status": "PAUSED"}), 200, "")

	// Cancellation with a reason; tenants' responsibility is never quoted.
	r = h.post(o, "/maintenance", map[string]any{"property_id": p.ID, "title": "Broken window", "description": "Tenant damage.", "category": "GLAZING", "priority": "LOW"})
	h.expect(r, 201, "")
	var w usecases.MaintenanceView
	r.data(t, &w)
	wp := "/maintenance/" + w.ID.String()
	h.expect(h.write(o, "PATCH", wp, w.Version, map[string]any{"priority": "LOW", "responsibility": "TENANT"}), 200, "")
	h.expect(h.post(o, wp+"/request-quotes", map[string]any{"sourcing": "SELF_SOURCED"}), 409, "NOT_OWNER_RESPONSIBILITY")
	h.expect(h.post(o, wp+"/cancel", map[string]any{"reason_code": "TENANT_RESPONSIBILITY"}), 200, "")
	h.expect(h.get(h.register(true), wp), 404, "")
	var page struct {
		Data []usecases.MaintenanceView `json:"data"`
	}
	_ = json.Unmarshal(h.get(o, "/maintenance?status=CANCELLED").body, &page)
	if len(page.Data) != 1 || page.Data[0].ID != w.ID {
		t.Fatalf("list %+v", page.Data)
	}
}

func TestProfessionalAppointmentDueDiligence(t *testing.T) {
	h := newHarness(t, withPayment)
	o, l := h.saleListing("21 Survey Road")
	c := customer()
	h.offer(c, l, 8000000000)
	var sales struct {
		Data []usecases.SaleView `json:"data"`
	}
	_ = json.Unmarshal(h.get(o, "/transactions/sales?property_id="+l.PropertyID.String()).body, &sales)
	sid := sales.Data[0].ID
	h.expect(h.post(o, "/transactions/sales/"+sid.String()+"/data-room", nil), 200, "")

	pro, other := professional(), professional()
	r := h.post(o, "/professional-appointments", map[string]any{"sale_id": sid, "purpose": "SURVEY", "scope": "Boundary survey and encroachment check",
		"professional_ref": pro.ActorID, "professional_label": "Land Surveyors Ltd"})
	h.expect(r, 201, "")
	var a usecases.AppointmentView
	r.data(t, &a)
	ap := "/professional-appointments/" + a.ID.String()
	h.expect(h.asProvider(other, ap+"/provider-updates", map[string]any{"type": "QUOTE", "quote_amount_minor": 50000000}), 404, "")
	h.expect(h.asProvider(pro, ap+"/provider-updates", map[string]any{"type": "QUOTE", "quote_amount_minor": 50000000, "conflict_declared": true,
		"conflict_note": "Surveyed the neighbouring plot", "external_ref": "ps-77"}), 200, "")
	a = h.appointment(o, a.ID)
	h.expect(h.write(o, "PATCH", ap, a.Version, map[string]any{"action": "APPOINT", "fee_collection": "PLATFORM"}), 422, "LICENCE_NOT_CONFIRMED")
	h.expect(h.asProvider(pro, ap+"/provider-updates", map[string]any{"type": "QUOTE", "quote_amount_minor": 50000000, "conflict_declared": true,
		"licence_ref": "SURCON-1234", "external_ref": "ps-77"}), 200, "")
	a = h.appointment(o, a.ID)
	h.expect(h.write(o, "PATCH", ap, a.Version, map[string]any{"action": "APPOINT", "fee_collection": "PLATFORM"}), 422, "CONFLICT_DECLARED")
	h.expect(h.write(o, "PATCH", ap, a.Version, map[string]any{"action": "APPOINT", "fee_collection": "PLATFORM", "acknowledge_conflict": true}), 200, "")
	a = h.appointment(o, a.ID)
	h.expect(h.write(o, "PATCH", ap, a.Version, map[string]any{"action": "GRANT_DATA_ROOM", "categories": []string{"SURVEY"}, "expires_in_days": 5}), 200, "")
	a = h.appointment(o, a.ID)
	if a.Status != "APPOINTED" || a.DataRoomGrantID == nil {
		t.Fatalf("appointed %+v", a)
	}
	var room usecases.DataRoomView
	h.get(o, "/transactions/sales/"+sid.String()+"/data-room").data(t, &room)
	rpath := internal + "/data-rooms/" + room.ID.String()
	h.expect(h.do(call{method: "GET", path: rpath, as: platform.ProfessionalServicesService, delegate: pro}), 200, "")

	// Fee via payment-service; signed report with findings.
	h.runJobs("professional-fees")
	h.pay.settle(a.ID, 50000000, payment.IntentHeld)
	h.clock.advance(time.Second)
	h.runJobs("professional-fees")
	h.expect(h.asProvider(pro, ap+"/provider-updates", map[string]any{"type": "START"}), 200, "")
	report := h.providerMedia(pro, "DOCUMENT")
	h.expect(h.asProvider(pro, ap+"/provider-updates", map[string]any{"type": "REPORT", "report_media_id": report}), 422, "SIGNED_REPORT_REQUIRED")
	h.expect(h.asProvider(pro, ap+"/provider-updates", map[string]any{"type": "REPORT", "report_media_id": report, "report_signed": true,
		"findings": []map[string]any{{"ref": "f1", "category": "ENCROACHMENT", "severity": "HIGH", "summary": "Fence 0.4m over the eastern boundary"}}}), 200, "")
	a = h.appointment(o, a.ID)
	if a.Status != "REPORT_SUBMITTED" || len(a.Findings) != 1 || a.PaidMinor != 50000000 {
		t.Fatalf("report %+v", a)
	}
	mr := h.post(o, "/maintenance", map[string]any{"property_id": l.PropertyID, "title": "Move fence", "description": "Per survey.", "category": "FENCING", "priority": "NORMAL"})
	h.expect(mr, 201, "")
	var fix usecases.MaintenanceView
	mr.data(t, &fix)
	h.expect(h.post(o, ap+"/findings/"+a.Findings[0].ID.String()+"/use", map[string]any{"target_type": "maintenance_request", "target_id": fix.ID}), 200, "")
	h.expect(h.write(o, "PATCH", ap, a.Version, map[string]any{"action": "RATE", "rating": 5}), 422, "RATING_NOT_ALLOWED")
	h.expect(h.write(o, "PATCH", ap, a.Version, map[string]any{"action": "ACCEPT_REPORT"}), 200, "")
	a = h.appointment(o, a.ID)
	if a.Status != "ACCEPTED" || a.DataRoomGrantID != nil || a.Findings[0].UsedInID == nil || *a.Findings[0].UsedInID != fix.ID {
		t.Fatalf("accepted %+v", a)
	}
	h.expect(h.do(call{method: "GET", path: rpath, as: platform.ProfessionalServicesService, delegate: pro}), 404, "")
	h.expect(h.write(o, "PATCH", ap, a.Version, map[string]any{"action": "RATE", "rating": 5, "note": "Thorough"}), 200, "")
	if h.outboxTypes(a.ID)["professional.appointment.updated.v1"] < 5 {
		t.Fatalf("appointment events %v", h.outboxTypes(a.ID))
	}
	h.validateEvents(a.ID)
}

// outboxEnvelopeOf returns the latest envelope of a type for an aggregate.
func (h *harness) outboxEnvelopeOf(aggregate ids.ID, eventType string) ([]byte, string) {
	h.t.Helper()
	var id ids.ID
	if err := h.db.Pool.QueryRow(context.Background(), `SELECT event_id FROM outbox_events WHERE aggregate_id=$1 AND event_type=$2 ORDER BY occurred_at DESC LIMIT 1`,
		aggregate, eventType).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	return h.outboxEnvelope(id)
}

func (h *harness) appointment(o *owner, id ids.ID) usecases.AppointmentView {
	h.t.Helper()
	r := h.get(o, "/professional-appointments/"+id.String())
	h.expect(r, 200, "")
	var v usecases.AppointmentView
	r.data(h.t, &v)
	return v
}
