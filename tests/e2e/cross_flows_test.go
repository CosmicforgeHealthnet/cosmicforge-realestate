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
}
