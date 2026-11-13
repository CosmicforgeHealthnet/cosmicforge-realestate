package e2e

import (
	"bytes"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// certifyProviderActivation records the Marketplace provider certification
// in Business. Certification evidence is an external activation fact (a
// production certification task), the only Business row this suite writes
// directly; every other Business state is reached through its API.
func (p *crossEnv) certifyProviderActivation() {
	p.t.Helper()
	db, err := sql.Open("pgx", p.businessDB)
	if err != nil {
		p.t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`UPDATE channel_providers SET status='CERTIFIED',certification_reference='e2e-activation-evidence',rate_limit_per_minute=600000 WHERE code='COSMICFORGE_MARKETPLACE'`,
		`UPDATE channel_capabilities SET status='CERTIFIED',evidence_reference='e2e-activation-evidence' WHERE provider_id=(SELECT id FROM channel_providers WHERE code='COSMICFORGE_MARKETPLACE') AND capability IN ('ARI','CONTENT','PROPERTY_MAPPING','PRODUCT_MAPPING','RATE_MAPPING','READBACK','PAUSE','WITHDRAW','WEBHOOK','POLLING','RESERVATION_INGEST','RESERVATION_CHANGE')`,
	} {
		if _, err = db.Exec(q); err != nil {
			p.t.Fatal(err)
		}
	}
}

// dumpBusiness logs diagnostic rows from the Business database on failure.
func (p *crossEnv) dumpBusiness(query string) { p.dump(p.businessDB, query) }

func (p *crossEnv) dump(dsn, query string) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return
	}
	defer db.Close()
	rows, err := db.Query(query)
	if err != nil {
		p.t.Logf("diagnostic query: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		p.t.Logf("business: %s", v)
	}
}

// paymentPolicy creates the HOTEL collection policy in the Payment Service
// with the Admin Service identity (policy authoring is an Admin authority).
func (p *crossEnv) paymentPolicy() ids.ID {
	p.t.Helper()
	id := ids.New()
	body := map[string]any{"policy_id": id, "policy_key": "HOTEL", "version": "e2e-hotel-v1", "jurisdiction": "NG", "currency": "NGN", "contract_type": "HOTEL_STAY", "product": "ROOM",
		"effective_from": time.Now().UTC().Add(-time.Hour), "active": true, "approval_ref": "e2e-policy-approval", "cancellation_policy_ref": "FLEXIBLE-24H:v1", "refund_policy_ref": "REFUND:v1",
		"dispute_policy_ref": "DISPUTE:v1", "adjustment_policy_ref": "ADJUST:v1", "release_conditions": []string{"CHECK_IN"}, "evidence_services": []string{string(platform.BusinessOperationsService)}, "schedules": []string{"ONE_TIME"}}
	if r := p.signedCall(platform.AdminService, platform.PaymentService, serviceauth.Delegation{}, "POST", "/internal/v1/financial-policies", body, "e2e-policy-"+id.String()); r.status != 200 {
		p.t.Fatalf("payment policy %d %s", r.status, r.body)
	}
	return id
}

// distribute connects the Marketplace channel and publishes the listing.
func (b *bizEnv) distribute() (listingTitle string) {
	t, p := b.t, b.p
	b.paymentPolicy = p.paymentPolicy()
	policy := map[string]any{"property_id": b.property, "expected_version": 0, "definition": map[string]any{"require_approval": false, "require_risk_clear": false, "allowed_payment_policy_ids": []ids.ID{b.paymentPolicy}, "allowed_sources": []string{"DIRECT", "MARKETPLACE", "OTA"},
		"allowed_guarantees": []string{"PAY_AT_PROPERTY"}, "max_guarantee_minor": 1000000, "hold_seconds": 900, "allow_source_cancellation": true, "cancellation_hours": 24, "cancellation_penalty_bps": 5000,
		"no_show_grace_hours": 2, "no_show_retain_nights": 1, "check_in_local": "14:00", "check_out_local": "11:00", "policy_reference": "e2e-hotel-acceptance:v1"},
		"effective_at": time.Now().UTC().Add(-time.Minute), "reason": "Reviewed reservation acceptance policy"}
	policy["approval_id"] = b.approval("COMMERCIAL_RESERVATION_POLICY_CHANGE", "reservations:approve", "", nil, "", map[string]any{"command": withoutApproval(policy)}, "PROPERTY", b.property)
	b.must(b.owner, "POST", "/reservation-policies", policy, 201)

	p.certifyProviderActivation()
	const marketplace = "COSMICFORGE_MARKETPLACE"
	conn := dataOf[struct {
		ID      ids.ID `json:"id"`
		Version int64  `json:"version"`
	}](t, b.must(b.owner, "POST", "/channels/connections", map[string]any{"property_id": b.property, "provider_code": marketplace, "provider_account_id": "cf-first-party:" + b.owner.org.String(), "reason": "Connect the first-party Marketplace"}, 201))
	authorize := map[string]any{"version": conn.Version, "credential_reference": "vault:marketplace/first-party", "reason": "Independently approved provider authority"}
	authorize["approval_id"] = b.approval("COMMERCIAL_CHANNEL_CHANGE", "channels:approve", marketplace, nil, "", map[string]any{"connection_id": conn.ID, "command": withoutApproval(authorize)}, "PROPERTY", b.property)
	conn = dataOf[struct {
		ID      ids.ID `json:"id"`
		Version int64  `json:"version"`
	}](t, b.must(b.owner, "POST", "/channels/"+conn.ID.String()+"/authorize", authorize, 200))
	prop := b.node(b.property)
	var rate struct {
		Plan struct {
			Version int64 `json:"version"`
		}
	}
	b.must(b.owner, "GET", "/rate-plans/"+b.rate.String(), nil, 200).data(t, &rate.Plan)
	entries := []map[string]any{{"kind": "PROPERTY", "external_id": "mkt-property:" + b.property.String(), "canonical_id": b.property, "canonical_version": prop.Version},
		{"kind": "PRODUCT", "external_id": "mkt-product", "canonical_id": b.product, "canonical_version": b.productVersion},
		{"kind": "RATE_PLAN", "external_id": "mkt-rate", "canonical_id": b.rate, "canonical_version": rate.Plan.Version}}
	preview := dataOf[struct {
		ID     ids.ID `json:"preview_id"`
		Digest string `json:"digest"`
	}](t, b.must(b.owner, "POST", "/channels/"+conn.ID.String()+"/mappings/preview", map[string]any{"version": conn.Version, "entries": entries, "reason": "Inspect canonical and provider bindings"}, 201))
	confirm := map[string]any{"preview_id": preview.ID, "digest": preview.Digest, "version": conn.Version, "reason": "Confirm reviewed provider mappings"}
	confirm["approval_id"] = b.approval("COMMERCIAL_CHANNEL_MAPPING_CHANGE", "channels:approve", marketplace, nil, "", map[string]any{"connection_id": conn.ID, "command": withoutApproval(confirm)}, "PROPERTY", b.property)
	b.must(b.owner, "POST", "/channels/"+conn.ID.String()+"/mappings", confirm, 200)
	b.connection = conn.ID

	// Public photo (Media), versioned content, submission and approved publication.
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}, bytes.Repeat([]byte{0x42}, 2048)...)
	photo := p.upload(b.owner.access, "IMAGE", "suite.jpg", "image/jpeg", jpeg, map[string]any{"access_classification": "PUBLIC", "title": "Sea-view suite"})
	prop = b.node(b.property)
	link := dataOf[struct {
		ID ids.ID `json:"media_link_id"`
	}](t, b.must(b.owner, "POST", "/inventory-nodes/"+b.property.String()+"/media", map[string]any{"media_id": photo, "purpose": "PHOTO", "version": prop.Version, "reason": "Approved public property photo"}, 200))
	prop = b.node(b.property)
	title := "Lekki Suites sea-view " + ids.New().String()[30:]
	content := map[string]any{"title": title, "description": "A verified sea-view suite in Lekki with fast wifi and a balcony.", "language": "en-NG", "amenities": []string{"WIFI", "BALCONY"},
		"house_rules": []string{"No smoking"}, "check_in_local": "15:00", "check_out_local": "11:00", "media_link_ids": []ids.ID{link.ID}}
	var draft struct {
		Listing struct {
			ID      ids.ID `json:"id"`
			Version int64  `json:"version"`
		} `json:"listing"`
		Version struct {
			ID ids.ID `json:"id"`
		} `json:"current_version"`
	}
	b.must(b.owner, "POST", "/listings", map[string]any{"property_id": b.property, "product_id": b.product, "property_version": prop.Version, "content": content, "reason": "Create versioned canonical content"}, 201).data(t, &draft)
	review := dataOf[struct {
		ID      ids.ID `json:"id"`
		Version int64  `json:"version"`
	}](t, b.must(b.owner, "POST", "/listings/"+draft.Listing.ID.String()+"/submit", map[string]any{"version": draft.Listing.Version, "reason": "Submit complete content for review"}, 200))
	publish := map[string]any{"version": review.Version, "listing_version_id": draft.Version.ID, "property_version": prop.Version, "product_version": b.productVersion, "pool_version": b.poolVersion,
		"rate_plan_version": rate.Plan.Version, "connection_id": conn.ID, "date_from": b.dateFrom, "date_to": b.dateTo, "reason": "Publish verified supply to the Marketplace"}
	amount := int64(5000000) // server-derived exposure: 2 nights at the approved nightly rate
	publish["approval_id"] = b.approval("VERIFIED_PROPERTY_PUBLICATION", "publication:approve", marketplace, &amount, "NGN", map[string]any{"listing_id": review.ID, "command": withoutApproval(publish)}, "PROPERTY", b.property)
	b.must(b.owner, "POST", "/listings/"+review.ID.String()+"/publish", publish, 202)
	return title
}

// domainUser registers and logs in an account of a domainkit service
// through the gateway and returns its access token and id.
func (p *crossEnv) domainUser(domain platform.Domain, email string) (string, ids.ID) {
	p.t.Helper()
	base := p.gateway + "/api/v1/" + string(domain)
	r := send(p.t, "POST", base+"/auth/register", "", "", map[string]any{"email": email, "password": "correct horse battery", "display_name": "E2E " + strings.Split(email, "@")[0]}, "reg-"+ids.New().String())
	if r.status != 201 {
		p.t.Fatalf("register %s %d %s", email, r.status, r.body)
	}
	l := send(p.t, "POST", base+"/auth/login", "", "", map[string]any{"email": email, "password": "correct horse battery"}, "")
	if l.status != 200 {
		p.t.Fatalf("login %s %d %s", email, l.status, l.body)
	}
	var tok struct {
		AccountID   ids.ID `json:"account_id"`
		AccessToken string `json:"access_token"`
	}
	l.data(p.t, &tok)
	return tok.AccessToken, tok.AccountID
}

// as calls a domain API through the gateway with a user token.
func (p *crossEnv) as(token, method, path string, body any) response {
	p.t.Helper()
	return send(p.t, method, p.gateway+path, token, "", body, "e2e-"+ids.New().String())
}

type checkoutView struct {
	ID                    ids.ID  `json:"id"`
	State                 string  `json:"state"`
	AmountMinor           int64   `json:"amount_minor"`
	BusinessReservationID *ids.ID `json:"business_reservation_id"`
	BusinessState         *string `json:"business_state"`
	FinancialState        *string `json:"financial_state"`
	IntentID              *ids.ID `json:"intent_id"`
	CheckoutURL           *string `json:"checkout_url"`
}

// TestCrossServiceCertificationFlows runs the certification flows across the
// real processes, in order, on one platform.
func TestCrossServiceCertificationFlows(t *testing.T) {
	p := startCrossPlatform(t)
	b := onboardBusiness(t, p)
	b.supplyBusiness()
	var title string
	t.Run("1 listing publication reaches Marketplace discovery", func(t *testing.T) {
		b.t = t
		title = b.distribute()
		q := strings.Fields(title)[2]
		eventually(t, 60*time.Second, "published listing discoverable on Marketplace", func() bool {
			r := send(t, "GET", p.gateway+"/api/v1/marketplace/listings?q="+q, "", "", nil, "")
			return r.status == 200 && strings.Contains(string(r.body), title)
		})
	})
	var customer string
	var checkout checkoutView
	t.Run("2 customer availability hold reservation payment and confirmation", func(t *testing.T) {
		p.t, b.t = t, t
		customer, _ = p.domainUser(platform.DomainMarketplace, "ada@customer.cross")
		detail := p.as("", "GET", "/api/v1/marketplace/listings/"+b.connection.String(), nil)
		if detail.status != 200 || !strings.Contains(string(detail.body), b.dateFrom) {
			t.Fatalf("listing availability %d %s", detail.status, detail.body)
		}
		if r := p.as(customer, "GET", "/api/v1/marketplace/listings?date_from="+b.dateFrom+"&date_to="+b.dateTo, nil); !strings.Contains(string(r.body), b.connection.String()) {
			t.Fatalf("available stay not listed %s", r.body)
		}
		q := p.as(customer, "POST", "/api/v1/marketplace/quotes", map[string]any{"listing_id": b.connection, "date_from": b.dateFrom, "date_to": b.dateTo, "adults": 2})
		if q.status != 201 {
			t.Fatalf("canonical quote %d %s", q.status, q.body)
		}
		q.data(t, &checkout)
		if checkout.State != "QUOTED" || checkout.AmountMinor != 5000000 {
			t.Fatalf("quote %s", q.body)
		}
		h := p.as(customer, "POST", "/api/v1/marketplace/checkouts/"+checkout.ID.String()+"/hold", map[string]any{})
		if h.status != 200 {
			t.Fatalf("hold %d %s", h.status, h.body)
		}
		h.data(t, &checkout)
		if checkout.State != "HELD" || checkout.BusinessReservationID == nil || *checkout.BusinessState != "HOLD" {
			t.Fatalf("hold %s", h.body)
		}
		pay := p.as(customer, "POST", "/api/v1/marketplace/checkouts/"+checkout.ID.String()+"/pay", map[string]any{"consent": true})
		if pay.status != 202 {
			t.Fatalf("pay %d %s", pay.status, pay.body)
		}
		// Business collects through the Payment Service; the customer opens
		// the intent's hosted checkout from the Payment API (own wallet).
		eventually(t, 120*time.Second, "Business-created Payment intent", func() bool {
			r := p.as(customer, "GET", "/api/v1/marketplace/checkouts/"+checkout.ID.String(), nil)
			r.data(t, &checkout)
			return checkout.IntentID != nil
		})
		if checkout.State != "PAYMENT_PENDING" {
			t.Fatalf("confirmed before payment %+v", checkout)
		}
		var intent struct {
			State       string  `json:"state"`
			CheckoutURL *string `json:"checkout_url"`
			AmountMinor int64   `json:"amount_minor"`
		}
		if r := p.as(customer, "GET", "/api/v1/payments/payment-intents/"+checkout.IntentID.String(), nil); r.status != 200 {
			t.Fatalf("customer reads own intent %d %s", r.status, r.body)
		} else {
			r.data(t, &intent)
		}
		if intent.CheckoutURL == nil || intent.AmountMinor != 5000000 || intent.State != "INITIALIZED" {
			t.Fatalf("hosted checkout %+v", intent)
		}
		// The customer pays at the provider; its signed webhook reaches the
		// Payment Service through the gateway.
		if !p.paystack.pay("cf-"+checkout.IntentID.String(), "success") {
			t.Fatalf("provider has no charge for intent %s", checkout.IntentID)
		}
		if r := p.paystack.webhook(t, p.gateway, p.providerAccount, "charge.success", "cf-"+checkout.IntentID.String()); r.status != 200 {
			t.Fatalf("provider webhook %d %s", r.status, r.body)
		}
		// Confirmation is Business's decision after its own Payment readback.
		eventually(t, 150*time.Second, "Business confirms after verified collection", func() bool {
			r := p.as(customer, "GET", "/api/v1/marketplace/checkouts/"+checkout.ID.String(), nil)
			r.data(t, &checkout)
			return checkout.State == "CONFIRMED"
		})
		if *checkout.BusinessState != "CONFIRMED" || *checkout.FinancialState != "VERIFIED" {
			t.Fatalf("confirmation %+v", checkout)
		}
	})
	t.Run("4 maintenance artisan work verified and resolved by Business", func(t *testing.T) {
		p.t, b.t = t, t
		artisan, artisanID := p.domainUser(platform.DomainArtisan, "tunde@artisan.cross")
		evidence := []ids.ID{p.upload(b.owner.access, "DOCUMENT", "report.pdf", "application/pdf", pdf("maintenance report"), map[string]any{"document_type": "MAINTENANCE_REPORT"})}
		wo := b.opsCreate(b.owner, "/work-orders", map[string]any{"scope_id": b.property, "subject_id": b.unit, "kind": "REPAIR", "title": "Fix bathroom leak", "reason": "Guest report"})
		approval := b.approval("COMMERCIAL_WORK_ORDER", "maintenance:approve", "", nil, "", map[string]any{"domain": "work_order", "record_id": wo.ID, "action": "APPROVE", "version": wo.Version}, "PROPERTY", b.property)
		wo = dataOf[opsRecord](t, b.must(b.owner, "POST", "/work-orders/"+wo.ID.String()+"/approve", map[string]any{"version": wo.Version, "approval_id": approval, "reason": "Approve repair"}, 200))
		wo = b.opsMove(b.owner, "/work-orders", wo, map[string]any{"action": "ASSIGN", "external_reference": "artisan-job-1"})
		pa := b.opsCreate(b.owner, "/work-orders/"+wo.ID.String()+"/provider-assignments", map[string]any{"kind": "ARTISAN", "title": "Bathroom leak repair",
			"counterparty_reference": "artisan-service:account:" + artisanID.String(), "attributes": map[string]any{"scope_of_work": "Replace the shower valve"}, "reason": "Assign artisan"})
		// The Artisan Service projects the Business assignment from JetStream.
		var work struct {
			ID ids.ID `json:"id"`
		}
		eventually(t, 60*time.Second, "artisan work request projected from Business", func() bool {
			r := p.as(artisan, "GET", "/api/v1/artisan/work-requests", nil)
			var list struct {
				Items []struct {
					ID ids.ID `json:"id"`
				} `json:"items"`
			}
			r.data(t, &list)
			if len(list.Items) == 1 {
				work.ID = list.Items[0].ID
			}
			return !work.ID.IsZero()
		})
		path := "/api/v1/artisan/work-requests/" + work.ID.String()
		mustStatus(t, p.as(artisan, "POST", path+"/accept", map[string]any{}), 200)
		mustStatus(t, p.as(artisan, "POST", path+"/arrive", map[string]any{}), 400) // BEFORE evidence first
		jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}, bytes.Repeat([]byte{0x24}, 1024)...)
		before := p.upload(artisan, "IMAGE", "before.jpg", "image/jpeg", jpeg, nil)
		mustStatus(t, p.as(artisan, "POST", path+"/evidence", map[string]any{"stage": "BEFORE", "media_reference": before}), 201)
		mustStatus(t, p.as(artisan, "POST", path+"/arrive", map[string]any{}), 200)
		after := p.upload(artisan, "IMAGE", "after.jpg", "image/jpeg", append(jpeg, 1), nil)
		mustStatus(t, p.as(artisan, "POST", path+"/evidence", map[string]any{"stage": "AFTER", "media_reference": after}), 201)
		mustStatus(t, p.as(artisan, "POST", path+"/complete", map[string]any{"warranty_days": 90}), 200)
		// Business holds the authoritative assignment outcome and verifies the work.
		if got := b.opsGet("/work-orders/" + wo.ID.String() + "/provider-assignments/" + pa.ID.String()); got.State != "COMPLETED" {
			t.Fatalf("business assignment %+v", got)
		}
		wo = b.opsMove(b.owner, "/work-orders", wo, map[string]any{"action": "START"})
		wo = b.opsMove(b.owner, "/work-orders", wo, map[string]any{"action": "COMPLETE", "evidence_refs": evidence})
		wo = b.opsMove(b.approver, "/work-orders", wo, map[string]any{"action": "VERIFY", "evidence_refs": evidence})
		if wo.State != "VERIFIED" {
			t.Fatalf("work order %+v", wo)
		}
		if r := p.as(artisan, "GET", "/api/v1/artisan/warranties", nil); !strings.Contains(string(r.body), `"ACTIVE"`) {
			t.Fatalf("warranty %s", r.body)
		}
	})

	t.Run("5 manager mandate permission delegated action and revocation", func(t *testing.T) {
		p.t, b.t = t, t
		manager, managerID := p.domainUser(platform.DomainPropertyManager, "kemi@manager.cross")
		evidence := []ids.ID{p.upload(b.owner.access, "DOCUMENT", "mandate.pdf", "application/pdf", pdf("management agreement"), map[string]any{"document_type": "MANAGEMENT_AGREEMENT"})}
		ends := time.Now().UTC().Add(30 * 24 * time.Hour)
		as := b.opsCreate(b.owner, "/manager-assignments", map[string]any{"scope_id": b.property, "kind": "PROPERTY_MANAGER", "title": "Lekki Suites management",
			"counterparty_reference": "property-manager-service:account:" + managerID.String(), "ends_at": ends, "attributes": map[string]any{"capabilities": []string{"maintenance:write"}, "mandate_reference": "mandate-lekki-1"}})
		approval := b.approval("EXTERNAL_MANAGER_ACCESS", "approvals:decide", "", nil, "", map[string]any{"domain": "manager_assignment", "record_id": as.ID, "action": "ACTIVATE", "version": as.Version}, "PROPERTY", b.property)
		as = b.opsMove(b.owner, "/manager-assignments", as, map[string]any{"action": "ACTIVATE", "approval_id": approval, "evidence_refs": evidence})
		var mandate struct {
			ID ids.ID `json:"id"`
		}
		eventually(t, 60*time.Second, "mandate projected by the Property Manager Service", func() bool {
			var list struct {
				Items []struct {
					ID ids.ID `json:"id"`
				} `json:"items"`
			}
			p.as(manager, "GET", "/api/v1/property-manager/mandates", nil).data(t, &list)
			if len(list.Items) == 1 {
				mandate.ID = list.Items[0].ID
			}
			return !mandate.ID.IsZero()
		})
		path := "/api/v1/property-manager/mandates/" + mandate.ID.String()
		mustStatus(t, p.as(manager, "POST", path+"/accept", map[string]any{}), 200)
		var auth struct {
			Allowed bool `json:"allowed"`
		}
		mustStatus(t, p.as(manager, "GET", path+"/authorization?capability=maintenance:write", nil), 200).data(t, &auth)
		if !auth.Allowed {
			t.Fatal("live Business authorization refused an active mandate")
		}
		mustStatus(t, p.as(manager, "GET", path+"/authorization?capability=finance:write", nil), 200).data(t, &auth)
		if auth.Allowed {
			t.Fatal("capability outside the mandate allowed")
		}
		req := mustStatus(t, p.as(manager, "POST", path+"/maintenance-requests", map[string]any{"kind": "CORRECTIVE", "title": "Broken balcony door", "reason": "Reported by the on-site manager"}), 201)
		if !strings.Contains(string(req.body), "business_request_id") && !strings.Contains(string(req.body), "OPEN") {
			t.Fatalf("delegated action %s", req.body)
		}
		// Business revokes; the next delegated action is refused by Business
		// and the projection follows the Business event.
		cur := b.opsGet("/manager-assignments/" + as.ID.String())
		b.must(b.owner, "POST", "/manager-assignments/"+as.ID.String()+"/revoke", map[string]any{"version": cur.Version, "reason": "Mandate ended by the owner"}, 200)
		if r := p.as(manager, "POST", path+"/maintenance-requests", map[string]any{"kind": "CORRECTIVE", "title": "Another request", "reason": "After revocation"}); r.status != 403 && r.status != 409 {
			t.Fatalf("revoked mandate acted %d %s", r.status, r.body)
		}
		eventually(t, 60*time.Second, "revocation projected", func() bool {
			r := p.as(manager, "GET", path, nil)
			return strings.Contains(string(r.body), `"REVOKED"`)
		})
		mustStatus(t, p.as(manager, "GET", path+"/authorization?capability=maintenance:write", nil), 200).data(t, &auth)
		if auth.Allowed {
			t.Fatal("revoked mandate still authorized")
		}
	})

	t.Run("6 professional appointment certification evidence and Business milestone approval", func(t *testing.T) {
		p.t, b.t = t, t
		pro, proID := p.domainUser(platform.DomainProfessionalServices, "ngozi@qs.cross")
		const base = "/api/v1/professional-services"
		mustStatus(t, p.as(pro, "PUT", base+"/profile", map[string]any{"profession": "QUANTITY_SURVEYOR", "license_number": "QS-NG-4471"}), 200)
		// Credential decided by the Verification Service from Media evidence.
		licence := p.upload(pro, "VERIFICATION_DOCUMENT", "qs-licence.pdf", "application/pdf", pdf("quantity surveyor licence"), map[string]any{"document_type": "PROFESSIONAL_LICENSE"})
		mustStatus(t, p.as(pro, "POST", base+"/profile/verification", map[string]any{"media_id": licence}), 202)
		eventually(t, 60*time.Second, "professional credential VERIFIED by Verification", func() bool {
			r := p.as(pro, "POST", base+"/profile/verification/refresh", nil)
			return strings.Contains(string(r.body), `"VERIFIED"`)
		})
		ref := "professional-services-service:account:" + proID.String()
		appt := b.opsCreate(b.owner, "/professional-appointments", map[string]any{"scope_id": b.property, "kind": "SURVEYOR", "title": "Structural survey", "counterparty_reference": ref, "attributes": map[string]any{"purpose": "Pre-expansion survey"}})
		branch := dataOf[struct {
			ID ids.ID `json:"id"`
		}](t, b.must(b.owner, "POST", "/branches", map[string]any{"name": "Development branch", "timezone": "Africa/Lagos", "currency": "NGN"}, 201))
		dev := dataOf[bizNode](t, b.must(b.owner, "POST", "/projects", map[string]any{"branch_id": branch.ID, "code": "LEKKI-ANNEX", "name": "Lekki Suites annex"}, 201))
		projectBase := "/projects/" + dev.ID.String()
		evidence := []ids.ID{p.upload(b.owner.access, "DOCUMENT", "progress.pdf", "application/pdf", pdf("site progress"), map[string]any{"document_type": "PROGRESS_REPORT"})}
		due := time.Now().UTC().Add(30 * 24 * time.Hour)
		ms := b.opsCreate(b.owner, projectBase+"/milestones", map[string]any{"kind": "STRUCTURE", "title": "Structure complete", "ends_at": due})
		ms = b.opsMove(b.owner, projectBase+"/milestones", ms, map[string]any{"action": "START"})
		ms = b.opsMove(b.owner, projectBase+"/milestones", ms, map[string]any{"action": "REQUEST_CERTIFICATION", "evidence_refs": evidence})
		b.opsCreate(b.owner, projectBase+"/progress-certifications", map[string]any{"kind": "INTERIM", "title": "Structure certificate", "counterparty_reference": ref, "attributes": map[string]any{"milestone_id": ms.ID.String(), "percent_complete": 100}})
		// The Professional Services Service projects both Business records.
		var apptID, certID string
		eventually(t, 60*time.Second, "appointment and certification projected", func() bool {
			var a, c struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
			}
			p.as(pro, "GET", base+"/appointments", nil).data(t, &a)
			p.as(pro, "GET", base+"/certifications", nil).data(t, &c)
			if len(a.Items) == 1 && len(c.Items) == 1 {
				apptID, certID = a.Items[0].ID, c.Items[0].ID
			}
			return apptID != "" && certID != ""
		})
		proEvidence := []ids.ID{p.upload(pro, "DOCUMENT", "survey.pdf", "application/pdf", pdf("survey findings"), map[string]any{"document_type": "SURVEY_REPORT"})}
		for _, step := range []string{"accept", "start"} {
			mustStatus(t, p.as(pro, "POST", base+"/appointments/"+apptID+"/"+step, map[string]any{}), 200)
		}
		mustStatus(t, p.as(pro, "POST", base+"/appointments/"+apptID+"/findings", map[string]any{"kind": "DEFECT", "title": "Hairline crack on east wall", "evidence_refs": proEvidence}), 201)
		mustStatus(t, p.as(pro, "POST", base+"/appointments/"+apptID+"/report", map[string]any{"report_reference": proEvidence[0]}), 200)
		if got := b.opsGet("/professional-appointments/" + appt.ID.String()); got.State != "REPORT_SUBMITTED" {
			t.Fatalf("business appointment %+v", got)
		}
		mustStatus(t, p.as(pro, "POST", base+"/certifications/"+certID+"/review", map[string]any{}), 200)
		mustStatus(t, p.as(pro, "POST", base+"/certifications/"+certID+"/certify", map[string]any{"certificate_reference": "QS-CERT-2026-118", "evidence_refs": proEvidence}), 200)
		// Business approves the milestone on the certified evidence.
		ms = b.opsMove(b.approver, projectBase+"/milestones", ms, map[string]any{"action": "CERTIFY", "evidence_refs": evidence})
		if ms.State != "CERTIFIED" {
			t.Fatalf("milestone %+v", ms)
		}
	})
}

func mustStatus(t *testing.T, r response, status int) response {
	t.Helper()
	if r.status != status {
		t.Fatalf("got %d want %d: %s", r.status, status, r.body)
	}
	return r
}
