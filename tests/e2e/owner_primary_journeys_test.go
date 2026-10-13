package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// These chains execute real Owner/Gateway/Verification/Media/Notification
// processes and DBs. Source actors, templates and off-platform attestations
// are TEST contracts. Financial Core's separate process stories remain the
// financial certification boundary; no local attestation is bank settlement.
func (p *platformEnv) actor(caller platform.Service, actor serviceauth.Delegation, method, path string, body any) response {
	tok, err := p.signers[caller].TokenFor(string(platform.IndividualOwnerService), actor)
	if err != nil {
		p.t.Fatal(err)
	}
	return send(p.t, method, p.internal[platform.IndividualOwnerService]+"/internal/v1/owner"+path, tok, "", body, ids.New().String())
}

func (p *platformEnv) checked(r response, want int) map[string]any {
	p.t.Helper()
	if r.status != want {
		p.t.Fatalf("HTTP want %d got %d: %s", want, r.status, r.body)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(r.body, &envelope); err != nil {
		p.t.Fatal(err)
	}
	return envelope.Data
}

func (p *platformEnv) ownerCommand(tok, path string, body any) map[string]any {
	return p.checked(p.asUser(tok, "POST", "/api/v1/owner"+path, body, ids.New().String()), 200)
}

func idOf(v map[string]any) string     { return v["id"].(string) }
func versionOf(v map[string]any) int64 { return int64(v["version"].(float64)) }

// restartOwner proves durable authoritative reads after abrupt process loss.
// Windows cleanup uses Kill; it is crash recovery, not graceful shutdown proof.
func (p *platformEnv) restartOwner() {
	for i, pr := range p.procs {
		if pr.name != "individual-owner-service api" {
			continue
		}
		_ = pr.cmd.Process.Kill()
		_ = pr.cmd.Wait()
		cmd := exec.Command(pr.cmd.Path, pr.cmd.Args[1:]...)
		cmd.Env = pr.cmd.Env
		cmd.Stdout, cmd.Stderr = lockedWriter{pr.mu, pr.out}, lockedWriter{pr.mu, pr.out}
		if err := cmd.Start(); err != nil {
			p.t.Fatal(err)
		}
		pr.cmd = cmd
		p.procs[i] = pr
		waitReady(p.t, p.internal[platform.IndividualOwnerService])
		return
	}
	p.t.Fatal("Owner process missing")
}

func (p *platformEnv) certifyPrimaryOwnerJourneys(tok string, ownerID, propertyID, listingID, photo, deed ids.ID) {
	root := p.t
	run := func(name string, fn func()) {
		root.Run(name, func(t *testing.T) { p.t = t; defer func() { p.t = root }(); fn() })
	}
	customer := serviceauth.Delegation{ActorDomain: platform.DomainMarketplace, ActorID: ids.New()}
	admin := serviceauth.Delegation{ActorDomain: platform.DomainAdmin, ActorID: ids.New()}
	var tenancyID string
	run("14_2_rental_application_signing_occupancy_inspection_and_reopening", func() {
		// Discover via the authoritative public contract, never private evidence.
		p.checked(p.asService(platform.MarketplaceService, platform.IndividualOwnerService, "GET", "/internal/v1/owner/listings/"+listingID.String(), nil, ""), 200)
		a := p.checked(p.actor(platform.MarketplaceService, customer, "POST", "/applications", map[string]any{"listing_id": listingID, "term_months": 12, "consent": true, "participants": []map[string]any{{"role": "PRIMARY", "display_name": "Test Tenant"}}}), 201)
		p.ownerCommand(tok, "/applications/"+idOf(a)+"/decision", map[string]any{"decision": "APPROVED"})
		offer := p.checked(p.asUser(tok, "POST", "/api/v1/owner/applications/"+idOf(a)+"/offer", map[string]any{"start_date": time.Now().UTC().Format(time.DateOnly), "term_months": 12}, ids.New().String()), 201)
		p.ownerCommand(tok, "/offers/"+idOf(offer)+"/send", nil)
		p.checked(p.actor(platform.MarketplaceService, customer, "POST", "/offers/"+idOf(offer)+"/response", map[string]any{"decision": "ACCEPT"}), 200)
		var agreements []map[string]any
		r := p.asUser(tok, "GET", "/api/v1/owner/agreements?property_id="+propertyID.String(), nil, "")
		if r.status != 200 {
			p.t.Fatalf("agreements: %s", r.body)
		}
		r.data(p.t, &agreements)
		agreement := agreements[0]
		ap := "/agreements/" + idOf(agreement)
		tpl := p.checked(p.actor(platform.AdminService, admin, "POST", "/admin/agreement-templates", map[string]any{"code": "TEST_JOURNEY_RENT", "country": "NG", "agreement_kind": "RESIDENTIAL_LEASE", "title": "TEST fixture, not legally approved", "document_media_id": deed, "clauses": []any{}}), 201)
		terms := agreement["current_version"].(map[string]any)["terms"].(map[string]any)
		terms["rent_collection"], terms["deposit_handling"], terms["deposit_minor"] = "OFF_PLATFORM", "NONE", 0
		p.checked(p.ifMatch(tok, "PATCH", "/api/v1/owner"+ap, versionOf(agreement), map[string]any{"terms": terms, "template_id": idOf(tpl), "document_media_id": deed}), 200)
		p.ownerCommand(tok, ap+"/submit-review", nil)
		p.ownerCommand(tok, ap+"/send-for-signature", map[string]any{"method": "OFFLINE"})
		agreement = p.checked(p.asUser(tok, "GET", "/api/v1/owner"+ap, nil, ""), 200)
		for _, item := range agreement["participants"].([]any) {
			participant := item.(map[string]any)
			p.ownerCommand(tok, ap+"/signatures", map[string]any{"participant_id": idOf(participant), "evidence_media_id": deed, "signed_at": time.Now().UTC()})
		}
		p.checked(p.actor(platform.MarketplaceService, customer, "POST", ap+"/customer-action", map[string]any{"action": "CONFIRM_SIGNATURE"}), 200)
		agreement = p.ownerCommand(tok, ap+"/activate", nil)
		tenancyID = agreement["tenancy_id"].(string)
		tp := "/tenancies/" + tenancyID
		p.ownerCommand(tok, tp+"/move-in", nil)
		p.ownerCommand(tok, tp+"/move-in-checklist/items", map[string]any{"items": []map[string]any{{"room": "Entry", "item": "Door", "condition": "GOOD", "media_ids": []ids.ID{photo}}}})
		p.ownerCommand(tok, tp+"/move-in-checklist/submit", nil)
		p.checked(p.actor(platform.MarketplaceService, customer, "POST", tp+"/move-in-checklist/customer-response", map[string]any{"decision": "ACKNOWLEDGE"}), 200)
		// Explicit TEST notice policy accelerates the chain. It is neither a
		// Nigerian legal deadline nor a production-approved notice template.
		noticeTemplate := p.checked(p.actor(platform.AdminService, admin, "POST", "/admin/notice-templates", map[string]any{"code": "TEST_ZERO_DAY_NOTICE", "country": "NG", "notice_type": "TERMINATION", "title": "TEST acceleration only", "min_notice_days": 0}), 201)
		notice := p.checked(p.asUser(tok, "POST", "/api/v1/owner"+tp+"/notices", map[string]any{"notice_type": "TERMINATION", "template_id": idOf(noticeTemplate), "summary": "TEST termination", "delivery_channel": "PLATFORM"}, ids.New().String()), 201)
		p.ownerCommand(tok, "/notices/"+idOf(notice)+"/issue", nil)
		p.checked(p.actor(platform.MarketplaceService, customer, "POST", "/notices/"+idOf(notice)+"/customer-action", map[string]any{"action": "ACKNOWLEDGE"}), 200)
		p.ownerCommand(tok, tp+"/move-out", map[string]any{"action": "START", "move_out_date": time.Now().UTC().Format(time.DateOnly), "notice_id": idOf(notice)})
		p.ownerCommand(tok, tp+"/move-out-checklist/items", map[string]any{"items": []map[string]any{{"room": "Entry", "item": "Door", "condition": "GOOD", "media_ids": []ids.ID{photo}}}})
		p.ownerCommand(tok, tp+"/move-out-checklist/submit", nil)
		p.checked(p.actor(platform.MarketplaceService, customer, "POST", tp+"/move-out-checklist/customer-response", map[string]any{"decision": "ACKNOWLEDGE"}), 200)
		p.ownerCommand(tok, tp+"/move-out", map[string]any{"action": "COMPLETE_INSPECTION"})
		p.ownerCommand(tok, tp+"/move-out", map[string]any{"action": "KEYS_RETURNED"})
		p.ownerCommand(tok, tp+"/move-out", map[string]any{"action": "COMPLETE", "return_to_market": true})
		p.restartOwner()
		if p.checked(p.asUser(tok, "GET", "/api/v1/owner"+tp, nil, ""), 200)["status"] != "ENDED" {
			p.t.Fatal("tenancy final state not durable")
		}
	})
	run("14_5_manager_mandate_permission_change_revocation_and_retention", func() {
		mgr := serviceauth.Delegation{ActorDomain: platform.DomainPropertyManager, ActorID: ids.New()}
		proposal := p.checked(p.asUser(tok, "POST", "/api/v1/owner/manager-proposals", map[string]any{"property_id": propertyID, "manager_domain": "property-manager", "manager_ref_id": mgr.ActorID, "manager_display_name": "TEST signed manager", "role_type": "FULL_MANAGEMENT", "fee_type": "NONE", "capabilities": []string{"property.view", "listing.pause"}}, ids.New().String()), 201)
		now := time.Now().UTC()
		assignment := p.checked(p.asUser(tok, "POST", "/api/v1/owner/manager-assignments", map[string]any{"proposal_id": idOf(proposal), "starts_at": now, "ends_at": now.Add(24 * time.Hour), "currency": "NGN", "spend_limit_minor": 0}, ids.New().String()), 201)
		path := "/manager-assignments/" + idOf(assignment)
		p.checked(p.actor(platform.PropertyManagerService, mgr, "POST", path+"/respond", map[string]any{"decision": "ACCEPT"}), 200)
		p.checked(p.actor(platform.PropertyManagerService, mgr, "GET", "/managed/properties/"+propertyID.String(), nil), 200)
		assignment = p.checked(p.asUser(tok, "GET", "/api/v1/owner"+path, nil, ""), 200)
		p.checked(p.ifMatch(tok, "PATCH", "/api/v1/owner"+path, versionOf(assignment), map[string]any{"capabilities": []string{"property.view"}}), 200)
		p.ownerCommand(tok, path+"/revoke", map[string]any{"reason_code": "TEST_HANDOVER"})
		if r := p.actor(platform.PropertyManagerService, mgr, "GET", "/managed/properties/"+propertyID.String(), nil); r.status != 404 {
			p.t.Fatalf("revoked actor retained access: %d", r.status)
		}
		p.restartOwner()
		if p.checked(p.asUser(tok, "GET", "/api/v1/owner"+path, nil, ""), 200)["status"] != "REVOKED" {
			p.t.Fatal("mandate history lost")
		}
	})
	run("14_3_land_sale_diligence_milestones_closing_and_listing_withdrawal", func() {
		pid, lid := p.primaryListing(tok, "LAND", "LAND_PARCEL", "SALE", map[string]any{"title_document_type": "CERTIFICATE_OF_OCCUPANCY"}, map[string]any{"currency": "NGN", "base_amount_minor": 9000000000, "base_period": "TOTAL", "payment_cycle": "ONE_TIME"}, 3, deed)
		_ = pid
		buyer := serviceauth.Delegation{ActorDomain: platform.DomainMarketplace, ActorID: ids.New()}
		offer := p.checked(p.actor(platform.MarketplaceService, buyer, "POST", "/sale-offers", map[string]any{"listing_id": lid, "buyer_display_name": "TEST buyer", "amount_minor": 9000000000, "currency": "NGN", "financing": "CASH", "proof_of_funds_ref": "TEST source proof only"}), 201)
		var sales []map[string]any
		r := p.asUser(tok, "GET", "/api/v1/owner/transactions/sales?property_id="+pid.String(), nil, "")
		if r.status != 200 {
			p.t.Fatalf("sales: %s", r.body)
		}
		r.data(p.t, &sales)
		sp := "/transactions/sales/" + idOf(sales[0])
		p.ownerCommand(tok, sp+"/offers/"+idOf(offer)+"/decision", map[string]any{"decision": "ACCEPT", "settlement": "OFF_PLATFORM"})
		p.ownerCommand(tok, sp+"/advance", map[string]any{"to": "DUE_DILIGENCE"})
		p.ownerCommand(tok, sp+"/data-room", nil)
		p.ownerCommand(tok, sp+"/data-room/documents", map[string]any{"media_id": deed, "category": "TITLE", "title": "TEST title reference"})
		room := p.ownerCommand(tok, sp+"/data-room/grants", map[string]any{"grantee_domain": "marketplace", "grantee_ref_id": buyer.ActorID, "grantee_label": "TEST buyer", "purpose": "sale.due_diligence", "categories": []string{"TITLE"}, "expires_in_days": 1})
		docID := idOf(room["documents"].([]any)[0].(map[string]any))
		downloadPath := "/data-rooms/" + idOf(room) + "/documents/" + docID + "/download-url"
		p.checked(p.actor(platform.MarketplaceService, buyer, "POST", downloadPath, nil), 200)
		if r := p.actor(platform.MarketplaceService, serviceauth.Delegation{ActorDomain: platform.DomainMarketplace, ActorID: ids.New()}, "POST", downloadPath, nil); r.status != 404 {
			p.t.Fatalf("unrelated buyer read private title: %d", r.status)
		}
		grantID := idOf(room["grants"].([]any)[0].(map[string]any))
		p.ownerCommand(tok, sp+"/data-room/grants/"+grantID+"/revoke", nil)
		if r := p.actor(platform.MarketplaceService, buyer, "POST", downloadPath, nil); r.status != 404 {
			p.t.Fatalf("revoked purpose retained access: %d", r.status)
		}
		dd := p.checked(p.asUser(tok, "POST", "/api/v1/owner"+sp+"/due-diligence", map[string]any{"category": "TITLE_SEARCH", "title": "TEST evidence review", "blocking": true}, ids.New().String()), 201)
		p.checked(p.ifMatch(tok, "PATCH", "/api/v1/owner"+sp+"/due-diligence/"+idOf(dd), versionOf(dd), map[string]any{"status": "RESOLVED", "blocking": true, "finding": "TEST fixture report", "resolution_note": "Owner reviewed TEST evidence"}), 200)
		p.ownerCommand(tok, sp+"/advance", map[string]any{"to": "CONTRACTING"})
		p.ownerCommand(tok, sp+"/advance", map[string]any{"to": "CONDITIONS_PENDING"})
		if r := p.asUser(tok, "POST", "/api/v1/owner"+sp+"/advance", map[string]any{"to": "CLOSING"}, ids.New().String()); r.status != 422 {
			p.t.Fatalf("closing without milestones: %d", r.status)
		}
		for _, code := range []string{"SURVEY_CHARTING", "PURCHASE_AGREEMENT", "BALANCE_PAYMENT", "STATE_CONSENT", "POSSESSION_HANDOVER"} {
			p.ownerCommand(tok, sp+"/milestones", map[string]any{"code": code, "action": "EVIDENCE", "evidence_media_ids": []ids.ID{deed}})
			if code == "BALANCE_PAYMENT" {
				p.checked(p.actor(platform.MarketplaceService, buyer, "POST", sp+"/customer-action", map[string]any{"action": "CONFIRM_MILESTONE", "milestone_code": code}), 200)
			}
		}
		p.ownerCommand(tok, sp+"/advance", map[string]any{"to": "CLOSING"})
		p.ownerCommand(tok, sp+"/advance", map[string]any{"to": "COMPLETED"})
		p.restartOwner()
		if p.checked(p.asUser(tok, "GET", "/api/v1/owner"+sp, nil, ""), 200)["status"] != "COMPLETED" {
			p.t.Fatal("closing readback not durable")
		}
		if p.checked(p.asUser(tok, "GET", "/api/v1/owner/listings/"+lid.String(), nil, ""), 200)["status"] != "WITHDRAWN" {
			p.t.Fatal("completed sale still listed")
		}
	})
	run("14_4_shortlet_hold_stay_access_turnover_completion", func() {
		pid, lid := p.primaryListing(tok, "RESIDENTIAL", "APARTMENT", "SHORT_LET", map[string]any{"max_guests": 2, "min_nights": 1, "check_in_time": "00:00", "check_out_time": "11:00"}, map[string]any{"currency": "NGN", "base_amount_minor": 5000000, "base_period": "NIGHT", "payment_cycle": "PER_STAY"}, 5, deed)
		p.checked(p.ifMatch(tok, "PUT", "/api/v1/owner/listings/"+lid.String()+"/shortlet/settings", 0, map[string]any{"max_guests": 2, "min_nights": 1, "max_nights": 30, "check_in_time": "00:00", "check_out_time": "11:00", "instant_book": true, "guest_verification_required": false, "payment_collection": "OFF_PLATFORM", "cancellation_policy": "MODERATE", "turnover_buffer_days": 1}), 200)
		guest := serviceauth.Delegation{ActorDomain: platform.DomainMarketplace, ActorID: ids.New()}
		now := time.Now().UTC()
		body := map[string]any{"listing_id": lid, "check_in": now.Format(time.DateOnly), "check_out": now.AddDate(0, 0, 1).Format(time.DateOnly), "guests": 1, "guest_list": []map[string]any{{"display_name": "TEST guest", "primary": true}}}
		booking := p.checked(p.actor(platform.MarketplaceService, guest, "POST", "/customer-shortlet-bookings", body), 201)
		if r := p.actor(platform.MarketplaceService, serviceauth.Delegation{ActorDomain: platform.DomainMarketplace, ActorID: ids.New()}, "POST", "/customer-shortlet-bookings", body); r.status != 409 {
			p.t.Fatalf("overlapping booking accepted: %d", r.status)
		}
		dev := p.checked(p.asUser(tok, "POST", "/api/v1/owner/access/devices", map[string]any{"property_id": pid, "device_type": "LOCKBOX", "label": "TEST manual access"}, ids.New().String()), 201)
		grant := p.checked(p.asUser(tok, "POST", "/api/v1/owner/access/grants", map[string]any{"device_id": idOf(dev), "relation_type": "shortlet_booking", "relation_id": idOf(booking), "starts_at": now, "ends_at": now.Add(time.Hour)}, ids.New().String()), 201)
		p.ownerCommand(tok, "/access/grants/"+idOf(grant)+"/actions", map[string]any{"action": "ISSUE", "note": "TEST owner recorded handover; no physical device execution"})
		bp := "/shortlet/bookings/" + idOf(booking)
		booking = p.checked(p.ifMatch(tok, "PATCH", "/api/v1/owner"+bp, versionOf(booking), map[string]any{"action": "CHECK_IN"}), 200)
		p.checked(p.ifMatch(tok, "PATCH", "/api/v1/owner"+bp, versionOf(booking), map[string]any{"action": "CHECK_OUT"}), 200)
		var jobs []map[string]any
		r := p.asUser(tok, "GET", "/api/v1/owner/listings/"+lid.String()+"/shortlet/turnover", nil, "")
		if r.status != 200 {
			p.t.Fatalf("turnover: %s", r.body)
		}
		r.data(p.t, &jobs)
		for _, job := range jobs {
			p.checked(p.ifMatch(tok, "PATCH", "/api/v1/owner/shortlet/turnover/"+idOf(job), versionOf(job), map[string]any{"status": "COMPLETED", "evidence_media_ids": []ids.ID{photo}}), 200)
		}
		p.restartOwner()
		booking = p.checked(p.asUser(tok, "GET", "/api/v1/owner"+bp, nil, ""), 200)
		if booking["status"] != "CHECKED_OUT" && booking["status"] != "COMPLETED" {
			p.t.Fatal("stay completion lost")
		}
	})
	var n int
	if err := p.ownerDB.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action='tenancy.move_in' AND actor_id=$1`, ownerID).Scan(&n); err != nil || (tenancyID != "" && n != 1) {
		p.t.Fatalf("durable journey audit count=%d: %v", n, err)
	}
}

func (p *platformEnv) primaryListing(tok, category, propertyType, transaction string, details, pricing map[string]any, photoCount int, deed ids.ID) (ids.ID, ids.ID) {
	prop := p.checked(p.asUser(tok, "POST", "/api/v1/owner/properties", map[string]any{"category": category, "property_type": propertyType, "title": "TEST primary journey", "address": map[string]any{"line1": ids.New().String() + " Test Road", "city": "Lagos", "state_region": "Lagos", "country_code": "NG"}}, ids.New().String()), 201)
	pid := ids.MustParse(idOf(prop))
	pp := "/properties/" + pid.String()
	p.ownerCommand(tok, pp+"/ownership", map[string]any{"documents": []map[string]any{{"media_id": deed, "document_type": "DEED_OF_ASSIGNMENT"}}})
	p.ownerCommand(tok, pp+"/verification", nil)
	deadline := time.Now().Add(45 * time.Second)
	for {
		view := p.checked(p.asUser(tok, "GET", "/api/v1/owner"+pp+"/ownership", nil, ""), 200)
		if view["primary"].(map[string]any)["status"] == "VERIFIED" {
			break
		}
		if time.Now().After(deadline) {
			p.t.Fatal("property authority not verified")
		}
		time.Sleep(100 * time.Millisecond)
	}
	var selection []map[string]any
	for i := 0; i < photoCount; i++ {
		content := append([]byte{0xff, 0xd8, 0xff, 0xe0, byte(i)}, bytes.Repeat([]byte(pid.String()), 100)...)
		m := p.upload(p.t, tok, "IMAGE", "image/jpeg", "", content)
		p.checked(p.asUser(tok, "POST", "/api/v1/owner"+pp+"/media", map[string]any{"media_id": m, "purpose": "PHOTO", "position": i}, ids.New().String()), 201)
		selection = append(selection, map[string]any{"media_id": m, "position": i, "is_cover": i == 0})
	}
	l := p.checked(p.asUser(tok, "POST", "/api/v1/owner/listings", map[string]any{"property_id": pid, "transaction_type": transaction, "title": "TEST complete journey", "description": strings.Repeat("Test supported local business process. ", 8), "details": details, "media": selection}, ids.New().String()), 201)
	lid := ids.MustParse(idOf(l))
	lp := "/listings/" + lid.String()
	p.checked(p.ifMatch(tok, "PUT", "/api/v1/owner"+lp+"/pricing", 0, pricing), 200)
	p.ownerCommand(tok, lp+"/publish", nil)
	return pid, lid
}
