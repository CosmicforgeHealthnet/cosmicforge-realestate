//go:build financial_cross_service

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	js "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/nats-io/nats.go/jetstream"
)

// Same real-process harness as TestFinancialCoreConsumerContract. No Payment
// fake is installed in Owner. Future business domains are signed boundary
// simulators, and the parent owns explicitly synthetic provider/operator fixtures.
type coreJourney struct {
	h    *harness
	base string
}

func (j coreJourney) remedy(o *owner, q payment.AcceptedQuote) ids.ID {
	var ev struct {
		ID ids.ID `json:"evidence_id"`
	}
	j.fixture("remedy-evidence", map[string]any{"owner_id": o.id, "quote_id": q.ID}, &ev)
	return ev.ID
}
func (j coreJourney) link(o *owner, kind string, id ids.ID, resource string, ref ids.ID) {
	j.h.post(o, "/financial-business/"+id.String()+"/resource-links", usecases.CoreResourceInput{BusinessType: kind, Type: resource, ID: ref}).data(j.h.t, &usecases.CoreResourceView{})
}
func (j coreJourney) refund(o *owner, kind string, id ids.ID, q payment.AcceptedQuote, line payment.CoreLine, amount int64) payment.CoreRefund {
	j.h.t.Helper()
	ev := j.remedy(o, q)
	var c payment.CoreCase
	j.h.post(o, "/financial-business/"+id.String()+"/cases", usecases.CoreCaseInput{BusinessType: kind, CoreCaseRequest: payment.CoreCaseRequest{ID: ids.New(), LineID: line.ID, Kind: "REFUND_REVIEW", AmountMinor: amount, ReasonRef: "recorded-local-cancellation", EvidenceIDs: []ids.ID{ev}}}).data(j.h.t, &c)
	var refund payment.CoreRefund
	j.fixture("approved-refund", map[string]any{"case_id": c.ID, "line_id": line.ID, "evidence_id": ev, "amount_minor": amount, "unknown": true}, &refund)
	j.link(o, kind, id, "REFUND", refund.ID)
	return refund
}

func (j coreJourney) fixture(path string, body, out any) {
	j.h.t.Helper()
	raw, _ := json.Marshal(body)
	r, e := http.Post(j.base+"/test-fixtures/"+path, "application/json", bytes.NewReader(raw))
	if e != nil {
		j.h.t.Fatal(e)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		j.h.t.Fatalf("synthetic fixture %s: HTTP %d", path, r.StatusCode)
	}
	if out != nil {
		if e = json.NewDecoder(r.Body).Decode(out); e != nil {
			j.h.t.Fatal(e)
		}
	}
}
func (j coreJourney) context(o *owner, kind string, id ids.ID) usecases.CoreBusinessContext {
	j.h.t.Helper()
	var b usecases.CoreBusinessContext
	j.h.get(o, "/financial-business/"+id.String()+"/context?business_type="+kind).data(j.h.t, &b)
	return b
}
func (j coreJourney) quote(o *owner, kind string, id ids.ID, family string, components []payment.CoreComponent) payment.AcceptedQuote {
	j.h.t.Helper()
	b := j.context(o, kind, id)
	var q payment.AcceptedQuote
	j.fixture("workflow-quote", map[string]any{"business": b, "family": family, "components": components}, &q)
	return q
}
func (j coreJourney) collect(o *owner, kind string, id ids.ID, q payment.AcceptedQuote) payment.CollectionIntent {
	j.h.t.Helper()
	path := "/financial-business/" + id.String() + "/collections"
	body := map[string]any{"business_type": kind, "quote_id": q.ID, "payer_email": "accepted-fixture@example.invalid"}
	var i, retry payment.CollectionIntent
	j.h.post(o, path, body).data(j.h.t, &i)
	j.h.post(o, path, body).data(j.h.t, &retry)
	if i.ID != retry.ID {
		j.h.t.Fatal("collection retry created a second intent")
	}
	j.fixture("verify", map[string]any{"intent_id": i.ID}, nil)
	return i
}
func (j coreJourney) authority(o *owner, q payment.AcceptedQuote, role, domain string, party ids.ID, components ...string) payment.CoreAuthority {
	j.h.t.Helper()
	var a payment.CoreAuthority
	j.fixture("workflow-authority", map[string]any{"quote_id": q.ID, "owner_id": o.id, "role": role, "domain": domain, "party_id": party, "components": components, "mandate_ref": "synthetic-accepted-provider-mandate"}, &a)
	return a
}
func (j coreJourney) allocate(o *owner, kind string, id ids.ID, q payment.AcceptedQuote, a payment.CoreAuthority) payment.CoreAllocation {
	j.h.t.Helper()
	var lines []payment.CoreLineInput
	for _, c := range q.Components {
		lineKind := "BENEFICIARY"
		if c.Kind == "DEPOSIT" {
			lineKind = "DEPOSIT"
		}
		lines = append(lines, payment.CoreLineInput{AuthorityID: a.ID, ComponentRef: c.Ref, Kind: lineKind, AmountMinor: c.AmountMinor, Currency: q.Currency})
	}
	var allocation payment.CoreAllocation
	j.h.post(o, "/financial-business/"+id.String()+"/allocations", usecases.CoreAllocationRequest{BusinessType: kind, Lines: lines}).data(j.h.t, &allocation)
	return allocation
}
func (j coreJourney) reconcile(o *owner, kind string, id ids.ID) payment.CoreReceipt {
	j.h.t.Helper()
	var r payment.CoreReceipt
	j.h.post(o, "/financial-business/"+id.String()+"/reconcile", map[string]any{"business_type": kind}).data(j.h.t, &r)
	return r
}
func (j coreJourney) release(o *owner, kind string, id ids.ID, line payment.CoreLine, condition, policy string) {
	j.h.t.Helper()
	eid := ids.New()
	j.h.post(o, "/financial-business/"+id.String()+"/release-evidence", map[string]any{"business_type": kind, "evidence_id": eid, "line_id": line.ID, "condition": condition, "expires_at": time.Now().Add(time.Hour)}).data(j.h.t, &map[string]any{})
	body := map[string]any{"business_type": kind, "line_id": line.ID, "amount_minor": line.AmountMinor, "policy_version": policy, "evidence_ids": []ids.ID{eid}}
	path := api + "/financial-business/" + id.String() + "/releases"
	k := key()
	r := j.h.do(call{method: "POST", path: path, token: o.access, key: k, body: body})
	j.h.expect(r, 200, "")
	again := j.h.do(call{method: "POST", path: path, token: o.access, key: k, body: body})
	j.h.expect(again, 200, "")
	if !bytes.Equal(r.body, again.body) {
		j.h.t.Fatal("release replay changed journal")
	}
}
func certifyAdditionalCoreJourneys(t *testing.T, client *payment.WalletProvisioner, base string) {
	t.Run("land_lease_occupation_collection_retains_the_possession_gate", func(t *testing.T) {
		h := newHarness(t)
		h.app.Service.SetWalletRegistration(client)
		j := coreJourney{h, base}
		o := h.register(true)
		h.verifyIdentity(o)
		p := h.propertyOf(o, "Land Core Lease Street", "LAND", "FARMLAND")
		h.verifyProperty(o, p.ID)
		tenant := customer()
		var g usecases.AgreementView
		h.post(o, "/agreements", map[string]any{"property_id": p.ID, "agreement_kind": "LAND_LEASE", "template_id": h.agreementTemplate("LAND_LEASE"), "terms": map[string]any{"start_date": day(h.clock.now(), 1), "term_months": 12, "currency": "NGN", "rent_minor": 100000000, "frequency": "MONTHLY", "due_day": 1, "rent_collection": "PLATFORM", "deposit_handling": "NONE"}, "tenant": map[string]any{"display_name": "Accepted Lessee", "customer_ref_id": tenant.ActorID, "signing_email": "lessee@example.invalid"}}).data(t, &g)
		g = h.prepareDraft(o, g, h.agreementTemplate("LAND_LEASE"), nil)
		g = h.signOffline(o, tenant, g)
		h.post(o, "/agreements/"+g.ID.String()+"/activate", nil).data(t, &g)
		var obligations []usecases.RentObligationView
		h.get(o, "/tenancies/"+g.TenancyID.String()+"/rent-obligations").data(t, &obligations)
		if len(obligations) == 0 {
			t.Fatal("accepted land lease has no source obligations")
		}
		id := obligations[0].ID
		q := j.quote(o, "rent_obligation", id, "LAND_LEASE", nil)
		i := j.collect(o, "rent_obligation", id, q)
		a := j.authority(o, q, "OWNER", "individual-owner", o.id, "OCCUPATION")
		allocation := j.allocate(o, "rent_obligation", id, q, a)
		j.fixture("fund", map[string]any{"intent_id": i.ID}, nil)
		j.reconcile(o, "rent_obligation", id)
		if h.count(`SELECT count(*) FROM rent_obligations WHERE id=$1 AND paid_minor=100000000 AND status='PAID'`, id) != 1 {
			t.Fatal("accepted occupation principal not projected")
		}
		h.expect(h.post(o, "/financial-business/"+id.String()+"/release-evidence", map[string]any{"business_type": "rent_obligation", "evidence_id": ids.New(), "line_id": allocation.Lines[0].ID, "condition": "POSSESSION", "expires_at": time.Now().Add(time.Hour)}), 422, "AUTHORITATIVE_MILESTONE_REQUIRED")
	})
	t.Run("hospitality_deposit_claim_uses_the_actual_evidenced_damage_record", func(t *testing.T) {
		h := newHarness(t)
		h.app.Service.SetWalletRegistration(client)
		j := coreJourney{h, base}
		o, l := h.shortletListing("Damage Liability Core Street")
		h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/pricing", int64(l.Pricing.VersionNo), map[string]any{"currency": "NGN", "base_amount_minor": 5000000, "base_period": "NIGHT", "payment_cycle": "PER_STAY", "deposit_amount_minor": 1000000}), 200, "")
		h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/shortlet/settings", 0, shortletSettings("PLATFORM", true, false)), 200, "")
		now := h.clock.now()
		guest := customer()
		b := h.requestBooking(guest, bookingBody(l, dayN(now, 1), dayN(now, 3), "Damage Guest"))
		q := j.quote(o, "shortlet_booking", b.ID, "SHORT_LET", []payment.CoreComponent{{Ref: "accommodation", Kind: "ACCOMMODATION", AmountMinor: 10000000, Recurrence: "EVENT_DRIVEN"}, {Ref: "deposit", Kind: "DEPOSIT", AmountMinor: 1000000, Recurrence: "REFUNDABLE_DEPOSIT"}})
		i := j.collect(o, "shortlet_booking", b.ID, q)
		a := j.authority(o, q, "OWNER", "individual-owner", o.id, "ACCOMMODATION", "DEPOSIT")
		alloc := j.allocate(o, "shortlet_booking", b.ID, q, a)
		j.fixture("fund", map[string]any{"intent_id": i.ID}, nil)
		j.reconcile(o, "shortlet_booking", b.ID)
		var line payment.CoreLine
		for _, x := range alloc.Lines {
			if x.Kind == "DEPOSIT" {
				line = x
			}
		}
		ev := j.remedy(o, q)
		dep := payment.CoreDeposit{ID: ids.New(), LineID: line.ID, Type: "HOSPITALITY_DAMAGE", CustodyRef: "synthetic-local-liability-not-custody-certification", IngressRef: i.Reference, InspectionIDs: []ids.ID{ev}}
		j.fixture("register-deposit", map[string]any{"owner_id": o.id, "deposit": dep}, &dep)
		j.link(o, "shortlet_booking", b.ID, "DEPOSIT", dep.ID)
		var report usecases.PortfolioReport
		h.get(o, "/reports/portfolio?from="+dayN(now, 0)+"&to="+dayN(now, 4)).data(t, &report)
		if report.Shortlet.Revenue["NGN"] != 10000000 {
			t.Fatal("Core protected deposit counted as stay revenue", report)
		}
		h.clock.advance(24 * time.Hour)
		h.relogin(o)
		h.checkIn(o, b.ID)
		h.clock.advance(2 * 24 * time.Hour)
		h.relogin(o)
		h.expect(h.write(o, "PATCH", "/shortlet/bookings/"+b.ID.String(), h.booking(o, b.ID).Version, map[string]any{"action": "CHECK_OUT"}), 200, "")
		var damage usecases.DamageClaimView
		h.post(o, "/shortlet/bookings/"+b.ID.String()+"/damage-claims", map[string]any{"items": []map[string]any{{"description": "Evidenced broken lamp", "amount_minor": 500000, "media_ids": []ids.ID{h.media.put(o.id, "IMAGE", "READY")}}}}).data(t, &damage)
		in := usecases.CoreCaseInput{BusinessType: "shortlet_booking", OwnerClaimID: &damage.ID, CoreCaseRequest: payment.CoreCaseRequest{ID: ids.New(), LineID: line.ID, DepositID: &dep.ID, Kind: "DEPOSIT_CLAIM", AmountMinor: 500000, ReasonRef: "damage-claim:" + damage.ID.String(), EvidenceIDs: []ids.ID{ev}}}
		bad := in
		foreign := ids.New()
		bad.OwnerClaimID = &foreign
		h.expect(h.post(o, "/financial-business/"+b.ID.String()+"/cases", bad), 422, "DAMAGE_CLAIM_REQUIRED")
		h.post(o, "/financial-business/"+b.ID.String()+"/cases", in).data(t, &payment.CoreCase{})
		h.expect(h.asCustomer(guest, "POST", "/damage-claims/"+damage.ID.String()+"/customer-response", map[string]any{"decision": "DISPUTE", "note": "Disputed inspection", "evidence_media_ids": []ids.ID{h.customerMedia(guest, "IMAGE")}}), 200, "")
		j.reconcile(o, "shortlet_booking", b.ID)
		if h.booking(o, b.ID).Status != "DISPUTED" || h.booking(o, b.ID).RefundedMinor != 0 {
			t.Fatal("dispute fabricated settlement or cleared withheld money")
		}
	})
	t.Run("jetstream_stale_duplicate_and_expired_worker_claim_recovery", func(t *testing.T) {
		h := newHarness(t)
		h.app.Service.SetWalletRegistration(client)
		j := coreJourney{h, base}
		j.fixture("flush-events", nil, nil)
		conn, e := js.Connect(js.Config{URL: os.Getenv("CF_PAYMENT_E2E_NATS"), Name: "owner-financial-recovery", Logger: logging.Discard()})
		if e != nil {
			t.Fatal(e)
		}
		defer conn.Close()
		stream, e := conn.JetStream().Stream(context.Background(), "CROSS_FINANCE")
		if e != nil {
			t.Fatal(e)
		}
		consumer, e := stream.CreateOrUpdateConsumer(context.Background(), jetstream.ConsumerConfig{Name: "recovery-" + ids.New().String(), DeliverPolicy: jetstream.DeliverNewPolicy, AckPolicy: jetstream.AckExplicitPolicy, FilterSubjects: []string{"cross.finance.payment.collected.v1", "cross.finance.allocation_created.v1"}})
		if e != nil {
			t.Fatal(e)
		}
		o, l := h.shortletListing("Durable Core Readback Street")
		h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/shortlet/settings", 0, shortletSettings("PLATFORM", true, false)), 200, "")
		b := h.requestBooking(customer(), bookingBody(l, dayN(h.clock.now(), 1), dayN(h.clock.now(), 3), "Recovery Guest"))
		q := j.quote(o, "shortlet_booking", b.ID, "SHORT_LET", nil)
		i := j.collect(o, "shortlet_booking", b.ID, q)
		a := j.authority(o, q, "OWNER", "individual-owner", o.id, "ACCOMMODATION")
		j.allocate(o, "shortlet_booking", b.ID, q, a)
		j.fixture("flush-events", nil, nil)
		batch, e := consumer.Fetch(2, jetstream.FetchMaxWait(5*time.Second))
		if e != nil {
			t.Fatal(e)
		}
		var collected events.Envelope
		count := 0
		for message := range batch.Messages() {
			event, e := events.Unmarshal(message.Data())
			if e != nil {
				t.Fatal(e)
			}
			for range 2 {
				if e = h.app.Service.ApplyPaymentEvent(context.Background(), event); e != nil {
					t.Fatal(e)
				}
			}
			if event.EventType == "finance.payment.collected.v1" {
				collected = event
			}
			if e = message.Ack(); e != nil {
				t.Fatal(e)
			}
			count++
		}
		if count != 2 {
			t.Fatal("did not consume actual collection and allocation from JetStream", count)
		}
		if h.booking(o, b.ID).Status != "PENDING_PAYMENT" {
			t.Fatal("collection event fabricated funding")
		}
		j.fixture("fund", map[string]any{"intent_id": i.ID}, nil)
		j.reconcile(o, "shortlet_booking", b.ID)
		stale := collected
		stale.EventID = ids.New()
		stale.OccurredAt = stale.OccurredAt.Add(-time.Hour)
		if e = h.app.Service.ApplyPaymentEvent(context.Background(), stale); e != nil {
			t.Fatal(e)
		}
		if h.booking(o, b.ID).Status != "CONFIRMED" {
			t.Fatal("stale collection reverted current signed truth")
		}
		// A crashed worker is represented by its persisted unexpired claim. No
		// fabricated financial result or fresh provider submission clears it.
		_, e = h.db.Pool.Exec(context.Background(), `INSERT INTO financial_core_readback_leases(intent_id,claim_id,expires_at,last_attempt_at) VALUES($1,$2,$3,$4) ON CONFLICT(intent_id) DO UPDATE SET claim_id=EXCLUDED.claim_id,expires_at=EXCLUDED.expires_at`, i.ID, ids.New(), h.clock.now().Add(time.Minute), h.clock.now())
		if e != nil {
			t.Fatal(e)
		}
		h.expect(h.post(o, "/financial-business/"+b.ID.String()+"/reconcile", map[string]any{"business_type": "shortlet_booking"}), 422, "CORE_READBACK_IN_PROGRESS")
		_, e = h.db.Pool.Exec(context.Background(), `UPDATE financial_core_readback_leases SET expires_at=$2 WHERE intent_id=$1`, i.ID, h.clock.now().Add(-time.Second))
		if e != nil {
			t.Fatal(e)
		}
		for range 2 {
			h.runJobs("financial-core-readbacks")
		}
		if h.count(`SELECT count(*) FROM financial_core_business_bindings WHERE business_id=$1 AND intent_id=$2`, b.ID, i.ID) != 1 || h.booking(o, b.ID).Status != "CONFIRMED" {
			t.Fatal("expired-claim recovery replaced or duplicated the financial binding")
		}
		forged := collected
		forged.EventID = ids.New()
		forged.AggregateID = ids.New()
		if e = h.app.Service.ApplyPaymentEvent(context.Background(), forged); e == nil || !events.IsPermanent(e) {
			t.Fatal("mismatched canonical aggregate accepted", e)
		}
	})
	t.Run("rent_manager_split_requires_the_accepted_fee_and_covered_period", func(t *testing.T) {
		h := newHarness(t)
		h.app.Service.SetWalletRegistration(client)
		j := coreJourney{h, base}
		o := h.register(true)
		h.verifyIdentity(o)
		p := h.createProperty(o, "Manager Financial Mandate Street")
		l := h.liveOf(o, p, "RENT_LONG_TERM", map[string]any{"min_term_months": 12, "furnishing": "UNFURNISHED"}, map[string]any{"currency": "NGN", "base_amount_minor": 100000000, "base_period": "MONTH", "payment_cycle": "MONTHLY"})
		tenant := customer()
		h.acceptedOffer(o, l, tenant, day(h.clock.now(), 1))
		g := h.agreementOf(o, p.ID, "RENTAL_OFFER")
		g.CurrentVersion.Terms.RentCollection = "PLATFORM"
		g = h.prepareDraft(o, g, h.agreementTemplate("RESIDENTIAL_LEASE"), nil)
		g = h.signOffline(o, tenant, g)
		h.post(o, "/agreements/"+g.ID.String()+"/activate", nil).data(t, &g)
		var obligations []usecases.RentObligationView
		h.get(o, "/tenancies/"+g.TenancyID.String()+"/rent-obligations").data(t, &obligations)
		obligation := obligations[0]
		q := j.quote(o, "rent_obligation", obligation.ID, "RESIDENTIAL_INITIAL", nil)
		i := j.collect(o, "rent_obligation", obligation.ID, q)
		manager := ids.New()
		var proposal usecases.ProposalView
		h.post(o, "/manager-proposals", map[string]any{"property_id": p.ID, "manager_domain": "property-manager", "manager_ref_id": manager, "manager_display_name": "Contract manager", "role_type": "FULL_MANAGEMENT", "fee_type": "PERCENT_OF_RENT", "fee_bps": 1000}).data(t, &proposal)
		var mandate usecases.AssignmentView
		h.post(o, "/manager-assignments", map[string]any{"proposal_id": proposal.ID, "starts_at": h.clock.now(), "ends_at": h.clock.now().Add(90 * 24 * time.Hour), "currency": "NGN"}).data(t, &mandate)
		h.expect(h.do(call{method: "POST", path: internal + "/manager-assignments/" + mandate.ID.String() + "/respond", as: platform.PropertyManagerService, delegate: &serviceauth.Delegation{ActorDomain: platform.DomainPropertyManager, ActorID: manager}, key: key(), body: map[string]any{"decision": "ACCEPT"}}), 200, "")
		var ma payment.CoreAuthority
		j.fixture("workflow-authority", map[string]any{"quote_id": q.ID, "owner_id": o.id, "role": "MANAGER", "domain": "property-manager", "party_id": manager, "components": []string{"MANAGER_FEE"}, "mandate_ref": mandate.ID.String()}, &ma)
		oa := j.authority(o, q, "OWNER", "individual-owner", o.id, "RENT")
		lines := []payment.CoreLineInput{{AuthorityID: oa.ID, ComponentRef: q.Components[0].Ref, Kind: "BENEFICIARY", AmountMinor: 90000000, Currency: "NGN"}, {AuthorityID: ma.ID, ComponentRef: q.Components[0].Ref, Kind: "BENEFICIARY", AmountMinor: 10000000, Currency: "NGN"}}
		wrong := append([]payment.CoreLineInput{}, lines...)
		wrong[0].AmountMinor = 80000000
		wrong[1].AmountMinor = 20000000
		h.expect(h.post(o, "/financial-business/"+obligation.ID.String()+"/allocations", usecases.CoreAllocationRequest{BusinessType: "rent_obligation", Lines: wrong}), 422, "MANAGER_FEE_MANDATE_REQUIRED")
		h.post(o, "/financial-business/"+obligation.ID.String()+"/allocations", usecases.CoreAllocationRequest{BusinessType: "rent_obligation", Lines: lines}).data(t, &payment.CoreAllocation{})
		j.fixture("fund", map[string]any{"intent_id": i.ID}, nil)
		j.reconcile(o, "rent_obligation", obligation.ID)
		var wallet payment.WalletSnapshot
		h.get(o, "/financial-wallet").data(t, &wallet)
		found := false
		for _, balance := range wallet.Balances {
			if balance.Kind == "PROTECTED" && balance.AmountMinor == "90000000" {
				found = true
			}
		}
		if !found {
			t.Fatal("manager fee redirected or duplicated owner earnings", wallet)
		}
	})
	t.Run("sale_reservation_balance_closing_and_release_boundaries", func(t *testing.T) {
		h := newHarness(t)
		h.app.Service.SetWalletRegistration(client)
		j := coreJourney{h, base}
		o, l := h.saleListing("Core Closing Street")
		buyer := customer()
		offer := h.offer(buyer, l, 5000000000)
		sale := h.sale(o, h.offerSale(offer.ID))
		path := "/transactions/sales/" + sale.ID.String()
		h.expect(h.post(o, path+"/offers/"+offer.ID.String()+"/decision", map[string]any{"decision": "ACCEPT", "settlement": "PLATFORM", "reservation_minor": 500000000}), 200, "")
		sale = h.sale(o, sale.ID)
		var paidLines []struct {
			id   ids.ID
			line payment.CoreLine
		}
		for _, m := range sale.Milestones {
			if m.Kind != "PAYMENT" {
				continue
			}
			components := []payment.CoreComponent{{Ref: "amount", Kind: "SALE", AmountMinor: *m.AmountMinor, Recurrence: "EVENT_DRIVEN"}}
			if m.Code == "RESERVATION_DEPOSIT" {
				components[0].Kind = "RESERVATION"
			}
			q := j.quote(o, "closing_milestone", m.ID, "PROPERTY_PURCHASE", components)
			i := j.collect(o, "closing_milestone", m.ID, q)
			a := j.authority(o, q, "OWNER", "individual-owner", o.id, components[0].Kind)
			alloc := j.allocate(o, "closing_milestone", m.ID, q, a)
			j.fixture("fund", map[string]any{"intent_id": i.ID}, nil)
			j.reconcile(o, "closing_milestone", m.ID)
			paidLines = append(paidLines, struct {
				id   ids.ID
				line payment.CoreLine
			}{m.ID, alloc.Lines[0]})
			h.expect(h.post(o, "/financial-business/"+m.ID.String()+"/release-evidence", map[string]any{"business_type": "closing_milestone", "evidence_id": ids.New(), "line_id": alloc.Lines[0].ID, "condition": "CLOSING", "expires_at": time.Now().Add(time.Hour)}), 422, "AUTHORITATIVE_MILESTONE_REQUIRED")
		}
		sale = h.sale(o, sale.ID)
		if sale.Status == "COMPLETED" || h.listing(o, l.ID).Availability.Status == "SOLD" {
			t.Fatal("financial evidence alone completed title transfer")
		}
		for _, stage := range []string{"DUE_DILIGENCE", "CONTRACTING", "CONDITIONS_PENDING"} {
			h.expect(h.post(o, path+"/advance", map[string]any{"to": stage}), 200, "")
		}
		h.expect(h.post(o, path+"/milestones", map[string]any{"code": "PURCHASE_AGREEMENT", "action": "EVIDENCE", "evidence_media_ids": []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")}}), 200, "")
		h.expect(h.post(o, path+"/advance", map[string]any{"to": "CLOSING"}), 200, "")
		h.expect(h.post(o, path+"/advance", map[string]any{"to": "COMPLETED"}), 422, "MILESTONES_INCOMPLETE")
		h.expect(h.post(o, path+"/milestones", map[string]any{"code": "POSSESSION_HANDOVER", "action": "EVIDENCE", "evidence_media_ids": []ids.ID{h.media.put(o.id, "DOCUMENT", "READY")}}), 200, "")
		h.expect(h.post(o, path+"/advance", map[string]any{"to": "COMPLETED"}), 200, "")
		for _, paid := range paidLines {
			j.release(o, "closing_milestone", paid.id, paid.line, "CLOSING", "approved-fixture-v1")
		}
	})
	t.Run("customer_wallet_payment_is_adopted_without_another_charge", func(t *testing.T) {
		h := newHarness(t)
		h.app.Service.SetWalletRegistration(client)
		j := coreJourney{h, base}
		o, l := h.shortletListing("Customer Wallet Core Street")
		h.expect(h.write(o, "PUT", "/listings/"+l.ID.String()+"/shortlet/settings", 0, shortletSettings("PLATFORM", true, false)), 200, "")
		guest := customer()
		b := h.requestBooking(guest, bookingBody(l, dayN(h.clock.now(), 1), dayN(h.clock.now(), 3), "Wallet Guest"))
		q := j.quote(o, "shortlet_booking", b.ID, "SHORT_LET", nil)
		a := j.authority(o, q, "OWNER", "individual-owner", o.id, "ACCOMMODATION")
		var allocation payment.CoreAllocation
		j.fixture("customer-wallet-spend", map[string]any{"customer_id": guest.ActorID, "quote_id": q.ID, "lines": []payment.CoreLineInput{{AuthorityID: a.ID, ComponentRef: q.Components[0].Ref, Kind: "BENEFICIARY", AmountMinor: q.AmountMinor, Currency: q.Currency}}}, &allocation)
		i := j.collect(o, "shortlet_booking", b.ID, q)
		if i.ID != allocation.IntentID || i.State != "VERIFIED_SUCCESS" {
			t.Fatal("wallet-funded quote created another external intent", i, allocation)
		}
		r := j.reconcile(o, "shortlet_booking", b.ID)
		if !r.FundingConfirmed || h.booking(o, b.ID).Status != "CONFIRMED" {
			t.Fatal("wallet funding not read back as existing confirmed truth")
		}
	})
	t.Run("separate_security_deposit_pending_partial_refund_and_dispute", func(t *testing.T) {
		h := newHarness(t)
		h.app.Service.SetWalletRegistration(client)
		j := coreJourney{h, base}
		o := h.register(true)
		h.verifyIdentity(o)
		p := h.createProperty(o, "Core Deposit Street")
		l := h.liveOf(o, p, "RENT_LONG_TERM", map[string]any{"min_term_months": 12, "furnishing": "UNFURNISHED"}, map[string]any{"currency": "NGN", "base_amount_minor": 100000000, "base_period": "MONTH", "payment_cycle": "MONTHLY", "deposit_amount_minor": 10000000})
		tenant := customer()
		h.acceptedOffer(o, l, tenant, day(h.clock.now(), 1))
		g := h.agreementOf(o, p.ID, "RENTAL_OFFER")
		g.CurrentVersion.Terms.RentCollection = "PLATFORM"
		g.CurrentVersion.Terms.DepositHandling = "PLATFORM"
		g = h.prepareDraft(o, g, h.agreementTemplate("RESIDENTIAL_LEASE"), nil)
		g = h.signOffline(o, tenant, g)
		h.post(o, "/agreements/"+g.ID.String()+"/activate", nil).data(t, &g)
		tp := "/tenancies/" + g.TenancyID.String()
		var d usecases.DepositView
		h.get(o, tp+"/deposit").data(t, &d)
		q := j.quote(o, "deposit_obligation", d.ID, "RESIDENTIAL_DEPOSIT", nil)
		i := j.collect(o, "deposit_obligation", d.ID, q)
		a := j.authority(o, q, "CUSTOMER", "marketplace", tenant.ActorID, "DEPOSIT")
		alloc := j.allocate(o, "deposit_obligation", d.ID, q, a)
		j.reconcile(o, "deposit_obligation", d.ID)
		h.get(o, tp+"/deposit").data(t, &d)
		if d.FundedMinor != 0 {
			t.Fatal("unfunded deposit marked funded")
		}
		j.fixture("fund", map[string]any{"intent_id": i.ID}, nil)
		j.reconcile(o, "deposit_obligation", d.ID)
		h.get(o, tp+"/deposit").data(t, &d)
		if d.Status != "HELD" || d.FundedMinor != 10000000 {
			t.Fatal("separate protected deposit not held", d)
		}
		ev := j.remedy(o, q)
		dep := payment.CoreDeposit{ID: ids.New(), LineID: alloc.Lines[0].ID, Type: "RESIDENTIAL_SECURITY", CustodyRef: "synthetic-test-liability-not-production-custody", IngressRef: i.Reference, InspectionIDs: []ids.ID{ev}}
		j.fixture("register-deposit", map[string]any{"owner_id": o.id, "deposit": dep}, &dep)
		j.link(o, "deposit_obligation", d.ID, "DEPOSIT", dep.ID)
		h.expect(h.post(o, "/financial-business/"+d.ID.String()+"/releases", usecases.CoreReleaseInput{BusinessType: "deposit_obligation", CoreRelease: payment.CoreRelease{LineID: alloc.Lines[0].ID, AmountMinor: 1, PolicyVersion: "approved-fixture-v1", EvidenceIDs: []ids.ID{ev}}}), 422, "APPROVED_DEPOSIT_REMEDY_REQUIRED")
		refund := j.refund(o, "deposit_obligation", d.ID, q, alloc.Lines[0], 4000000)
		j.reconcile(o, "deposit_obligation", d.ID)
		h.get(o, tp+"/deposit").data(t, &d)
		if d.ReleasedMinor != 0 {
			t.Fatal("unknown refund released deposit")
		}
		j.fixture("refund-readback", map[string]any{"refund_id": refund.ID, "unknown": false, "state": "CONFIRMED_REFUNDED"}, &refund)
		for range 2 {
			j.reconcile(o, "deposit_obligation", d.ID)
		}
		h.get(o, tp+"/deposit").data(t, &d)
		if d.ReleasedMinor != 4000000 {
			t.Fatal("terminal partial deposit refund not projected exactly once", d)
		}
		var rep usecases.PortfolioReport
		h.get(o, "/reports/portfolio").data(t, &rep)
		if rep.NetIncome["NGN"] != 0 || rep.DepositsHeld["NGN"] != 6000000 {
			t.Fatal("deposit became owner income or held liability overstated", rep)
		}
	})
	t.Run("maintenance_and_professional_accepted_provider_entitlements", func(t *testing.T) {
		h := newHarness(t)
		h.app.Service.SetWalletRegistration(client)
		j := coreJourney{h, base}
		o := h.register(true)
		h.verifyIdentity(o)
		property := h.createProperty(o, "Core Provider Street")
		var m usecases.MaintenanceView
		h.post(o, "/maintenance", map[string]any{"property_id": property.ID, "title": "Roof repair", "description": "Accepted repair scope", "category": "ROOFING", "priority": "NORMAL"}).data(t, &m)
		mp := "/maintenance/" + m.ID.String()
		h.expect(h.write(o, "PATCH", mp, m.Version, map[string]any{"responsibility": "OWNER", "priority": "NORMAL", "budget_minor": 4000000}), 200, "")
		h.expect(h.post(o, mp+"/request-quotes", map[string]any{"sourcing": "PLATFORM"}), 200, "")
		provider := artisan()
		h.expect(h.asProvider(provider, mp+"/provider-quotes", map[string]any{"external_quote_ref": "core-roof-quote", "provider_display_name": "Local contract artisan", "scope": "Repair roof", "amount_minor": 4000000, "currency": "NGN", "warranty_days": 30}), 200, "")
		m = h.maintenance(o, m.ID)
		h.expect(h.post(o, mp+"/approve", map[string]any{"decision": "APPROVE", "quote_id": m.Quotes[0].ID, "payment_collection": "PLATFORM"}), 200, "")
		m = h.maintenance(o, m.ID)
		q := j.quote(o, "maintenance_job", m.Job.ID, "MAINTENANCE", nil)
		i := j.collect(o, "maintenance_job", m.Job.ID, q)
		wrong := j.authority(o, q, "ARTISAN", "artisan", ids.New(), "LABOUR")
		h.expect(h.post(o, "/financial-business/"+m.Job.ID.String()+"/allocations", usecases.CoreAllocationRequest{BusinessType: "maintenance_job", Lines: []payment.CoreLineInput{{AuthorityID: wrong.ID, ComponentRef: q.Components[0].Ref, Kind: "BENEFICIARY", AmountMinor: q.AmountMinor, Currency: q.Currency}}}), 422, "PROVIDER_ENTITLEMENT_REQUIRED")
		a := j.authority(o, q, "ARTISAN", "artisan", provider.ActorID, "LABOUR")
		alloc := j.allocate(o, "maintenance_job", m.Job.ID, q, a)
		j.fixture("fund", map[string]any{"intent_id": i.ID}, nil)
		j.reconcile(o, "maintenance_job", m.Job.ID)
		h.expect(h.post(o, "/financial-business/"+m.Job.ID.String()+"/release-evidence", map[string]any{"business_type": "maintenance_job", "evidence_id": ids.New(), "line_id": alloc.Lines[0].ID, "condition": "ACCEPTANCE", "expires_at": time.Now().Add(time.Hour)}), 422, "AUTHORITATIVE_MILESTONE_REQUIRED")
		job := "/maintenance-jobs/" + m.Job.ID.String() + "/provider-events"
		h.expect(h.asProvider(provider, job, map[string]any{"event_id": "done", "event_type": "COMPLETION_SUBMITTED", "evidence_media_ids": []ids.ID{h.providerMedia(provider, "IMAGE")}}), 200, "")
		h.expect(h.post(o, mp+"/completion", map[string]any{"decision": "ACCEPT"}), 200, "")
		j.release(o, "maintenance_job", m.Job.ID, alloc.Lines[0], "ACCEPTANCE", "approved-fixture-v1")
		pro := professional()
		var appointment usecases.AppointmentView
		h.post(o, "/professional-appointments", map[string]any{"property_id": property.ID, "purpose": "SURVEY", "scope": "Boundary survey", "professional_ref": pro.ActorID, "professional_label": "Local contract surveyor"}).data(t, &appointment)
		ap := "/professional-appointments/" + appointment.ID.String()
		h.expect(h.asProvider(pro, ap+"/provider-updates", map[string]any{"type": "QUOTE", "quote_amount_minor": 5000000, "conflict_declared": false, "licence_ref": "synthetic-survey-licence", "external_ref": "accepted-survey"}), 200, "")
		appointment = h.appointment(o, appointment.ID)
		h.expect(h.write(o, "PATCH", ap, appointment.Version, map[string]any{"action": "APPOINT", "fee_collection": "PLATFORM"}), 200, "")
		pq := j.quote(o, "professional_appointment", appointment.ID, "PROFESSIONAL_SERVICE", nil)
		pi := j.collect(o, "professional_appointment", appointment.ID, pq)
		pa := j.authority(o, pq, "PROFESSIONAL", "professional-services", pro.ActorID, "APPOINTMENT")
		pal := j.allocate(o, "professional_appointment", appointment.ID, pq, pa)
		j.fixture("fund", map[string]any{"intent_id": pi.ID}, nil)
		j.reconcile(o, "professional_appointment", appointment.ID)
		h.expect(h.asProvider(pro, ap+"/provider-updates", map[string]any{"type": "START"}), 200, "")
		h.expect(h.asProvider(pro, ap+"/provider-updates", map[string]any{"type": "REPORT", "report_media_id": h.providerMedia(pro, "DOCUMENT"), "report_signed": true}), 200, "")
		appointment = h.appointment(o, appointment.ID)
		h.expect(h.write(o, "PATCH", ap, appointment.Version, map[string]any{"action": "ACCEPT_REPORT"}), 200, "")
		j.release(o, "professional_appointment", appointment.ID, pal.Lines[0], "ACCEPTANCE", "approved-fixture-v1")
	})
	t.Run("initial_subsequent_rent_statements_and_payout_recovery", func(t *testing.T) {
		h := newHarness(t)
		h.app.Service.SetWalletRegistration(client)
		j := coreJourney{h, base}
		o := h.register(true)
		h.verifyIdentity(o)
		p := h.createProperty(o, "Accepted Financial Rent Street")
		l := h.liveOf(o, p, "RENT_LONG_TERM", map[string]any{"min_term_months": 12, "furnishing": "UNFURNISHED"}, map[string]any{"currency": "NGN", "base_amount_minor": 100000000, "base_period": "MONTH", "payment_cycle": "MONTHLY", "deposit_amount_minor": 10000000, "fees": []map[string]any{{"code": "agency_fee", "category": "AGENCY", "label": "One-time accepted charge", "calculation": "FIXED", "amount_minor": 20000000, "frequency": "ONE_TIME", "mandatory": true, "payable_to": "OWNER"}}})
		tenant := customer()
		h.acceptedOffer(o, l, tenant, day(h.clock.now(), 1))
		g := h.agreementOf(o, l.PropertyID, "RENTAL_OFFER")
		g.CurrentVersion.Terms.RentCollection = "PLATFORM"
		g.CurrentVersion.Terms.DepositHandling = "PLATFORM"
		g = h.prepareDraft(o, g, h.agreementTemplate("RESIDENTIAL_LEASE"), nil)
		g = h.signOffline(o, tenant, g)
		h.post(o, "/agreements/"+g.ID.String()+"/activate", nil).data(t, &g)
		var obligations []usecases.RentObligationView
		h.get(o, "/tenancies/"+g.TenancyID.String()+"/rent-obligations").data(t, &obligations)
		if len(obligations) < 2 {
			t.Fatal("missing existing monthly rent schedule")
		}
		first, next := obligations[0], obligations[1]
		q := j.quote(o, "rent_obligation", first.ID, "RESIDENTIAL_INITIAL", []payment.CoreComponent{{Ref: "rent", Kind: "RENT", AmountMinor: 100000000, Recurrence: "RECURRING"}, {Ref: "agency_fee", Kind: "ONBOARDING", AmountMinor: 20000000, Recurrence: "ONE_TIME"}})
		initial := j.collect(o, "rent_obligation", first.ID, q)
		if initial.AmountMinor != 120000000 {
			t.Fatal("initial amount does not equal 1m rent plus 200k once-only")
		}
		other := h.register(true)
		h.expect(h.get(other, "/financial-business/"+first.ID.String()+"/receipt?business_type=rent_obligation"), 404, "")
		a := j.authority(o, q, "OWNER", "individual-owner", o.id, "RENT", "ONBOARDING")
		allocation := j.allocate(o, "rent_obligation", first.ID, q, a)
		r := j.reconcile(o, "rent_obligation", first.ID)
		if r.FundingConfirmed {
			t.Fatal("unfunded collection reported funded")
		}
		if h.count(`SELECT count(*) FROM rent_obligations WHERE id=$1 AND paid_minor=0`, first.ID) != 1 {
			t.Fatal("allocation inferred paid rent")
		}
		j.fixture("fund", map[string]any{"intent_id": initial.ID}, nil)
		j.reconcile(o, "rent_obligation", first.ID)
		if h.count(`SELECT count(*) FROM rent_obligations WHERE id=$1 AND paid_minor=100000000 AND status='PAID'`, first.ID) != 1 {
			t.Fatal("funded rent included the one-time fee or failed to project paid state")
		}
		nextQuote := j.quote(o, "rent_obligation", next.ID, "RESIDENTIAL_SUBSEQUENT", nil)
		subsequent := j.collect(o, "rent_obligation", next.ID, nextQuote)
		if subsequent.AmountMinor != 100000000 || len(nextQuote.Components) != 1 {
			t.Fatal("ordinary subsequent rent repeated one-time charges")
		}
		nextAuth := j.authority(o, nextQuote, "OWNER", "individual-owner", o.id, "RENT")
		j.allocate(o, "rent_obligation", next.ID, nextQuote, nextAuth)
		j.fixture("fund", map[string]any{"intent_id": subsequent.ID}, nil)
		j.reconcile(o, "rent_obligation", next.ID)
		var stmt usecases.CoreStatement
		h.get(o, "/financial-wallet/statement").data(t, &stmt)
		if len(stmt.Entries) == 0 {
			t.Fatal("no authoritative financial statement entries")
		}
		h.expect(h.get(other, "/financial-wallet/statement"), 200, "")
		// Initial test policy requires the actual signed agreement, not a fake possession declaration.
		j.release(o, "rent_obligation", first.ID, allocation.Lines[0], "SIGNED_AGREEMENT", "approved-fixture-v1")
		var approval payment.CoreWithdrawal
		j.fixture("payout-approval", map[string]any{"owner_id": o.id, "line_id": allocation.Lines[0].ID, "amount_minor": 50000000}, &approval)
		h.expect(h.do(call{method: "POST", path: api + "/auth/step-up", token: o.access, body: map[string]any{"method": "PASSWORD", "password": o.password}}), 200, "")
		var pref usecases.CorePayoutPreference
		h.do(call{method: "PUT", path: api + "/financial-wallet/payout-preference", token: o.access, key: key(), headers: map[string]string{"If-Match": `"0"`}, body: map[string]any{"payout_account_id": approval.PayoutAccountID}}).data(t, &pref)
		j.fixture("payout-state", map[string]any{"unknown": true}, nil)
		var payout payment.CorePayout
		withdrawalKey := key()
		path := api + "/financial-wallet/withdrawals"
		res := h.do(call{method: "POST", path: path, token: o.access, key: withdrawalKey, body: approval})
		res.data(t, &payout)
		if payout.State != "UNKNOWN_OUTCOME" {
			t.Fatal("unknown payout was finalized")
		}
		h.expect(h.do(call{method: "POST", path: path, token: o.access, key: withdrawalKey, body: approval}), 200, "")
		h.post(o, "/financial-wallet/withdrawals/"+payout.ID.String()+"/readback", nil).data(t, &payout)
		if payout.State != "UNKNOWN_OUTCOME" {
			t.Fatal("readback freed unknown reservation")
		}
		j.fixture("payout-state", map[string]any{"unknown": false, "state": "CONFIRMED_FAILED"}, nil)
		h.post(o, "/financial-wallet/withdrawals/"+payout.ID.String()+"/readback", nil).data(t, &payout)
		if payout.State != "CONFIRMED_FAILED" {
			t.Fatal("definitive failed payout not reconciled")
		}
		for range 2 {
			h.post(o, "/financial-wallet/withdrawals/"+payout.ID.String()+"/readback", nil).data(t, &payout)
		}
		h.expect(h.get(other, "/financial-wallet/withdrawals/"+payout.ID.String()), 502, "DOWNSTREAM_REJECTED")
		j.fixture("reverse-collection", map[string]any{"intent_id": subsequent.ID, "amount_minor": 20000000}, nil)
		for range 2 {
			j.reconcile(o, "rent_obligation", next.ID)
		}
		if h.count(`SELECT count(*) FROM rent_obligations WHERE id=$1 AND paid_minor=80000000 AND status='PARTIAL'`, next.ID) != 1 {
			t.Fatal("confirmed partial reversal did not restore partial rent exactly once")
		}
		// Legacy event cannot turn a Core binding into fabricated paid/refunded money.
		legacy := producerEvent(t, platform.PaymentService, "payment.intent.updated.v1", "payment_intent", ids.New(), map[string]any{"intent_ref": subsequent.ID.String(), "status": "SUCCEEDED", "amount_minor": 100000000, "paid_minor": 999999999, "updated_at": time.Now()})
		if e := h.app.Service.ApplyPaymentEvent(context.Background(), legacy); e == nil || !events.IsPermanent(e) {
			t.Fatal("legacy state updated a canonical intent", e)
		}
	})
	t.Run("shortlet_collection_cancellation_extension_and_protected_release", func(t *testing.T) {
		h := newHarness(t)
		h.app.Service.SetWalletRegistration(client)
		j := coreJourney{h, base}
		o, l := h.shortletListing("Core Shortlet Street")
		h.write(o, "PUT", "/listings/"+l.ID.String()+"/shortlet/settings", 0, shortletSettings("PLATFORM", true, false)).data(t, &map[string]any{})
		guest := customer()
		var b usecases.BookingView
		h.asCustomer(guest, "POST", "/customer-shortlet-bookings", bookingBody(l, dayN(h.clock.now(), 1), dayN(h.clock.now(), 3), "Guest")).data(t, &b)
		q := j.quote(o, "shortlet_booking", b.ID, "SHORT_LET", nil)
		i := j.collect(o, "shortlet_booking", b.ID, q)
		a := j.authority(o, q, "OWNER", "individual-owner", o.id, "ACCOMMODATION")
		alloc := j.allocate(o, "shortlet_booking", b.ID, q, a)
		j.reconcile(o, "shortlet_booking", b.ID)
		if h.booking(o, b.ID).Status != "PENDING_PAYMENT" {
			t.Fatal("unfunded booking confirmed")
		}
		j.fixture("fund", map[string]any{"intent_id": i.ID}, nil)
		j.reconcile(o, "shortlet_booking", b.ID)
		if h.booking(o, b.ID).Status != "CONFIRMED" {
			t.Fatal("matched funded booking not confirmed")
		}
		premature := h.post(o, "/financial-business/"+b.ID.String()+"/release-evidence", map[string]any{"business_type": "shortlet_booking", "evidence_id": ids.New(), "line_id": alloc.Lines[0].ID, "condition": "STAY_COMPLETE", "expires_at": time.Now().Add(time.Hour)})
		h.expect(premature, 422, "AUTHORITATIVE_MILESTONE_REQUIRED")
		// Business cancellation remains independently authorized and does not claim a bank refund.
		h.write(o, "PATCH", "/shortlet/bookings/"+b.ID.String(), h.booking(o, b.ID).Version, map[string]any{"action": "CANCEL", "reason_code": "OWNER_UNAVAILABLE"}).data(t, &map[string]any{})
		j.reconcile(o, "shortlet_booking", b.ID)
		if h.booking(o, b.ID).Status != "CANCELLED" || h.booking(o, b.ID).RefundedMinor != 0 {
			t.Fatal("cancellation fabricated a terminal refund")
		}
		h.expect(h.post(o, "/financial-business/"+b.ID.String()+"/releases", map[string]any{"business_type": "shortlet_booking", "line_id": alloc.Lines[0].ID, "amount_minor": alloc.Lines[0].AmountMinor, "policy_version": "approved-fixture-v1", "evidence_ids": []ids.ID{ids.New()}}), 422, "RELEASE_BUSINESS_STATE_BLOCKED")
		refund := j.refund(o, "shortlet_booking", b.ID, q, alloc.Lines[0], b.TotalMinor)
		j.reconcile(o, "shortlet_booking", b.ID)
		if h.booking(o, b.ID).RefundedMinor != 0 {
			t.Fatal("unknown refund became final")
		}
		j.fixture("refund-readback", map[string]any{"refund_id": refund.ID, "unknown": false, "state": "CONFIRMED_REFUNDED"}, &refund)
		for range 2 {
			j.reconcile(o, "shortlet_booking", b.ID)
		}
		if h.booking(o, b.ID).RefundedMinor != b.TotalMinor {
			t.Fatal("confirmed cancellation refund was not tracked exactly once")
		}
		now := h.clock.now()
		stay := h.requestBooking(guest, bookingBody(l, dayN(now, 4), dayN(now, 6), "Returning Guest"))
		sq := j.quote(o, "shortlet_booking", stay.ID, "SHORT_LET", nil)
		si := j.collect(o, "shortlet_booking", stay.ID, sq)
		sa := j.authority(o, sq, "OWNER", "individual-owner", o.id, "ACCOMMODATION")
		sal := j.allocate(o, "shortlet_booking", stay.ID, sq, sa)
		j.fixture("fund", map[string]any{"intent_id": si.ID}, nil)
		j.reconcile(o, "shortlet_booking", stay.ID)
		h.clock.advance(4 * 24 * time.Hour)
		h.relogin(o)
		h.checkIn(o, stay.ID)
		er := h.asCustomer(guest, "POST", "/customer-shortlet-bookings/"+stay.ID.String()+"/extensions", map[string]any{"new_check_out": dayN(now, 7)})
		h.expect(er, 201, "")
		ext := h.extensionReq(er)
		h.expect(h.post(o, "/shortlet/extensions/"+ext.ID.String()+"/decision", map[string]any{"decision": "ACCEPT"}), 200, "")
		eq := j.quote(o, "shortlet_booking_extension", ext.ID, "SHORT_LET", nil)
		ei := j.collect(o, "shortlet_booking_extension", ext.ID, eq)
		ea := j.authority(o, eq, "OWNER", "individual-owner", o.id, "ACCOMMODATION")
		eal := j.allocate(o, "shortlet_booking_extension", ext.ID, eq, ea)
		j.reconcile(o, "shortlet_booking_extension", ext.ID)
		if h.booking(o, stay.ID).CheckOut != dayN(now, 6) {
			t.Fatal("unfunded extension applied")
		}
		j.fixture("fund", map[string]any{"intent_id": ei.ID}, nil)
		for range 2 {
			j.reconcile(o, "shortlet_booking_extension", ext.ID)
		}
		if h.booking(o, stay.ID).CheckOut != dayN(now, 7) {
			t.Fatal("funded extension not applied")
		}
		h.clock.advance(3 * 24 * time.Hour)
		h.relogin(o)
		h.expect(h.write(o, "PATCH", "/shortlet/bookings/"+stay.ID.String(), h.booking(o, stay.ID).Version, map[string]any{"action": "CHECK_OUT"}), 200, "")
		j.release(o, "shortlet_booking", stay.ID, sal.Lines[0], "STAY_COMPLETE", "approved-fixture-v1")
		j.release(o, "shortlet_booking_extension", ext.ID, eal.Lines[0], "STAY_COMPLETE", "approved-fixture-v1")
	})
}
