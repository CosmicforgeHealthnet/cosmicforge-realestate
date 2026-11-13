package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/marketplace-service/internal/customer"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/marketplace-service/internal/supply"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/marketplace-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit/domaintest"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// paymentDouble is a recording contract double of the Payment Service
// wallet, policy and intent APIs (service-level test only).
type paymentDouble struct {
	mu     sync.Mutex
	policy ids.ID
	wallet ids.ID
	intent ids.ID
	actors []string
	paths  []string
}

func (p *paymentDouble) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paths = append(p.paths, r.Method+" "+r.URL.Path)
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	w.Header().Set("Content-Type", "application/json")
	reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
	switch {
	case r.URL.Path == "/internal/v1/wallets/ensure":
		p.actors = append(p.actors, body["account_id"].(string))
		reply(map[string]any{"wallet_id": p.wallet, "currency": body["currency"]})
	case strings.HasPrefix(r.URL.Path, "/internal/v1/financial-policies/"):
		reply(map[string]any{"policy_id": p.policy, "policy_key": "HOTEL", "version": "v1", "currency": "NGN", "active": true, "effective_from": time.Now().Add(-time.Hour), "schedules": []string{"ONE_TIME"}})
	case strings.HasPrefix(r.URL.Path, "/internal/v1/payment-intents/"):
		reply(map[string]any{"intent_id": p.intent, "state": "INITIALIZED", "checkout_url": "https://checkout.example.invalid/pay"})
	default:
		w.WriteHeader(404)
	}
}

func TestMarketplaceCustomerDiscoveryCheckoutPaymentReviewAndFunnel(t *testing.T) {
	pd := &paymentDouble{policy: ids.New(), wallet: ids.New(), intent: ids.New()}
	ps := httptest.NewServer(pd)
	t.Cleanup(ps.Close)
	var sup *supply.Service
	k := domaintest.New(t, platform.MarketplaceService, migrations.FS, func(rt *domainkit.Runtime) domainkit.App {
		c, err := httpx.NewClient(httpx.ClientConfig{BaseURL: ps.URL, Audience: string(platform.PaymentService), Tokens: rt.Cfg.Identity.Signer, MaxAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		sup = &supply.Service{Pool: rt.Pool, TTL: time.Hour}
		return customer.NewWith(rt, customer.Deps{Supply: sup, Payment: c, LegalEntity: "cosmicforge-ng"})
	})
	// Business publishes supply through the business-supply.v1 contract.
	org, property, product, listing := ids.New(), ids.New(), ids.New(), ids.New()
	conn := supply.Connection{ID: ids.New(), OrganisationID: org, PropertyID: property, ProviderAccountID: "cf-first-party:" + org.String(), CredentialReference: "vault:marketplace"}
	if _, err := sup.Validate(t.Context(), org, conn); err != nil {
		t.Fatal(err)
	}
	from := time.Now().UTC().AddDate(0, 0, 10)
	days := []map[string]any{}
	for i := range 5 {
		days = append(days, map[string]any{"day": from.AddDate(0, 0, i).Format(time.DateOnly), "available": 2, "amount_minor": 2500000, "currency": "NGN"})
	}
	for v, kind := range []string{"CONTENT", "ARI"} {
		payload, _ := json.Marshal(map[string]any{"content": map[string]any{"title": "Harbour Suites Lekki", "description": "Sea-view serviced apartment", "amenities": []string{"WIFI"}}, "listing_id": listing, "product": map[string]any{"id": product}, "ari": days})
		hash, _ := supply.PayloadHash(payload)
		if _, err := sup.Apply(t.Context(), org, supply.Command{ContractVersion: supply.ContractVersion, JobID: ids.New(), Connection: conn, Kind: kind, CanonicalVersion: int64(v + 1), IdempotencyKey: "seed-" + kind, PayloadHash: hash, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	// Anonymous discovery and availability filter.
	df, dt := from.Format(time.DateOnly), from.AddDate(0, 0, 2).Format(time.DateOnly)
	res := k.Actor(ids.Nil, "GET", "/listings?q=harbour&date_from="+df+"&date_to="+dt, "", nil)
	if res.Status != 200 || len(res.Items()) != 1 || res.Items()[0].(map[string]any)["indicative_total_minor"] != 5000000.0 {
		t.Fatalf("search %d %s", res.Status, res.Raw)
	}
	if res := k.Actor(ids.Nil, "GET", "/listings?date_from="+df+"&date_to="+from.AddDate(0, 0, 9).Format(time.DateOnly), "", nil); len(res.Items()) != 0 {
		t.Fatalf("unavailable stay listed %s", res.Raw)
	}
	if r := k.Actor(ids.Nil, "POST", "/listings/"+conn.ID.String()+"/views", "", map[string]any{"view_id": ids.New()}); r.Status != 202 {
		t.Fatalf("view %d %s", r.Status, r.Raw)
	}
	if r := k.Actor(ids.Nil, "POST", "/quotes", "q0", map[string]any{"listing_id": conn.ID}); r.Status != 401 {
		t.Fatalf("anonymous quote %d", r.Status)
	}
	cust := k.Account("ada@customer.test")
	reservation, quoteID := ids.New(), ids.Nil
	state, completed := "HOLD", false
	var mu sync.Mutex
	k.Business.Reply = func(c domaintest.Call) (int, any) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(c.Path, "/channel-quotes"):
			quoteID = ids.New()
			return 201, map[string]any{"quote_id": quoteID, "product_id": product, "product_version": 3, "pool_version": 2, "date_from": df, "date_to": dt, "amount_minor": 5100000, "currency": "NGN", "expires_at": time.Now().Add(15 * time.Minute)}
		case strings.HasSuffix(c.Path, "/channel-receipts"):
			st := "HOLD"
			if c.Body["receipt"].(map[string]any)["kind"] == "RESERVATION_CANCELLED" {
				state, st = "CANCELLED", "CANCELLED"
			}
			return 202, map[string]any{"canonical_reservation_accepted": true, "canonical_reservation_id": reservation, "canonical_reservation_state": st}
		case strings.Contains(c.Path, "/reservations/") && c.Method == "GET":
			pay := map[string]any{"financial_group_id": ids.New(), "required_amount_minor": 5100000, "currency": "NGN", "financial_state": "PENDING", "allowed_payment_policy_ids": []ids.ID{pd.policy}}
			if state != "HOLD" {
				pay["intent_id"] = pd.intent
			}
			return 200, map[string]any{"reservation_id": reservation, "version": 4, "state": state, "financial_state": "PENDING", "confirmed": state == "CONFIRMED" || completed, "stay_completed": completed, "payment": pay}
		case strings.HasSuffix(c.Path, "/collections"):
			return 202, map[string]any{"id": ids.New(), "state": "QUEUED"}
		case strings.HasSuffix(c.Path, "/reviews"):
			return 202, map[string]any{"review_id": ids.New(), "state": "ACCEPTED"}
		case strings.HasSuffix(c.Path, "/funnel-observations"):
			return 202, map[string]any{"accepted": true}
		}
		return 404, nil
	}
	q := k.Actor(cust, "POST", "/quotes", "q1", map[string]any{"listing_id": conn.ID, "date_from": df, "date_to": dt, "adults": 2})
	if q.Status != 201 || q.Data()["state"] != "QUOTED" || q.Data()["amount_minor"] != 5100000.0 {
		t.Fatalf("quote %d %s", q.Status, q.Raw)
	}
	cid := q.Data()["id"].(string)
	if r := k.Actor(cust, "POST", "/checkouts/"+cid+"/review", "rv0", map[string]any{"rating": 5}); r.Status != 409 {
		t.Fatalf("review before stay %d", r.Status)
	}
	h := k.Actor(cust, "POST", "/checkouts/"+cid+"/hold", "h1", map[string]any{})
	if h.Status != 200 || h.Data()["state"] != "HELD" || h.Data()["business_reservation_id"] != reservation.String() {
		t.Fatalf("hold %d %s", h.Status, h.Raw)
	}
	other := k.Account("eve@customer.test")
	if r := k.Actor(other, "GET", "/checkouts/"+cid, "", nil); r.Status != 404 {
		t.Fatalf("other customer read %d", r.Status)
	}
	if r := k.Actor(cust, "POST", "/checkouts/"+cid+"/pay", "p0", map[string]any{"consent": false}); r.Status != 400 {
		t.Fatalf("pay without consent %d", r.Status)
	}
	p := k.Actor(cust, "POST", "/checkouts/"+cid+"/pay", "p1", map[string]any{"consent": true})
	if p.Status != 202 || p.Data()["state"] != "PAYMENT_PENDING" || p.Data()["wallet_id"] != pd.wallet.String() {
		t.Fatalf("pay %d %s %v", p.Status, p.Raw, pd.paths)
	}
	var collect domaintest.Call
	for _, c := range k.Business.Calls() {
		if strings.HasSuffix(c.Path, "/collections") {
			collect = c
		}
		if c.Caller != platform.MarketplaceService || c.Org != org {
			t.Fatalf("unsigned or undelegated business call %+v", c)
		}
	}
	if collect.Body["legal_party_reference"] != "marketplace:"+cust.String() || collect.Body["payer_wallet_id"] != pd.wallet.String() || collect.Key != "mkt-collect:"+cid {
		t.Fatalf("collection %+v", collect)
	}
	// Business confirms after verified payment; Marketplace only reads back.
	mu.Lock()
	state = "CONFIRMED"
	mu.Unlock()
	g := k.Actor(cust, "GET", "/checkouts/"+cid, "", nil)
	if g.Data()["state"] != "CONFIRMED" || g.Data()["checkout_url"] != "https://checkout.example.invalid/pay" {
		t.Fatalf("reconcile %s", g.Raw)
	}
	mu.Lock()
	completed, state = true, "CHECKED_OUT"
	mu.Unlock()
	rv := k.Actor(cust, "POST", "/checkouts/"+cid+"/review", "rv1", map[string]any{"rating": 5, "body": "Spotless and quiet"})
	if rv.Status != 201 {
		t.Fatalf("review %d %s", rv.Status, rv.Raw)
	}
	if r := k.Actor(cust, "POST", "/checkouts/"+cid+"/review", "rv2", map[string]any{"rating": 4}); r.Status != 409 {
		t.Fatalf("second review %d %s", r.Status, r.Raw)
	}
	if g := k.Actor(cust, "GET", "/checkouts/"+cid, "", nil); g.Data()["state"] != "COMPLETED" {
		t.Fatalf("completed %s", g.Raw)
	}
	// A second quote is cancelled locally (no Business reservation exists).
	q2 := k.Actor(cust, "POST", "/quotes", "q2", map[string]any{"listing_id": conn.ID, "date_from": df, "date_to": dt, "adults": 1})
	if q2.Status != 201 {
		t.Fatalf("quote2 %d %s", q2.Status, q2.Raw)
	}
	if r := k.Actor(cust, "POST", "/checkouts/"+q2.Data()["id"].(string)+"/cancel", "c2", map[string]any{}); r.Status != 200 || r.Data()["state"] != "CANCELLED" {
		t.Fatalf("cancel quote %d %s", r.Status, r.Raw)
	}
	// Funnel observations reach Business through the worker sweep.
	if err := customer.Sweep(k.App)(context.Background()); err != nil {
		t.Fatal(err)
	}
	var pending, delivered int
	if err := k.RT.Pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE delivered_at IS NULL),count(*) FILTER (WHERE delivered_at IS NOT NULL) FROM funnel_events`).Scan(&pending, &delivered); err != nil || pending != 0 || delivered < 4 {
		t.Fatalf("funnel pending=%d delivered=%d %v", pending, delivered, err)
	}
	// Supply routes keep their own Business-only authentication.
	checkSupplyAuth(t, k)
	if _, err := k.RT.Pool.Exec(t.Context(), `UPDATE stay_reviews SET rating=1`); err == nil {
		t.Fatal("review rewritten")
	}
}

func checkSupplyAuth(t *testing.T, k *domaintest.Kit) {
	r := k.Service(platform.BusinessOperationsService, ids.New(), "GET", "/business-supply/connections/"+ids.New().String()+"/listing", "", nil)
	if r.Status != 404 && r.Status != 400 {
		t.Fatalf("supply route %d %s", r.Status, r.Raw)
	}
	r = k.Service(platform.ArtisanService, ids.New(), "GET", "/business-supply/connections/"+ids.New().String()+"/listing", "", nil)
	if r.Status != 403 {
		t.Fatalf("non-business supply caller %d", r.Status)
	}
}
