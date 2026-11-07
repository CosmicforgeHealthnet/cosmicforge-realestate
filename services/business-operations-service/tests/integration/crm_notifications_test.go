package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// guestNotifier stands in for the signed Notification Service client. It
// honours idempotency keys the way the real service does and can simulate an
// outage (retryable) or a rejected request (definite). It never reports delivery.
type guestNotifier struct {
	mu       sync.Mutex
	outage   int
	reject   int
	calls    []notification.SendRequest
	keys     []string
	accepted map[string]ids.ID
}

func newGuestNotifier() *guestNotifier { return &guestNotifier{accepted: map[string]ids.ID{}} }
func (f *guestNotifier) Send(_ context.Context, req notification.SendRequest, key string) (notification.Accepted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	f.keys = append(f.keys, key)
	if f.outage > 0 {
		f.outage--
		return notification.Accepted{}, errors.New("notification-service unavailable")
	}
	if f.reject > 0 {
		f.reject--
		return notification.Accepted{}, &httpx.RemoteError{Service: "notification-service", Status: 422, Code: "TEMPLATE_NOT_FOUND", Message: "template not configured"}
	}
	if id, ok := f.accepted[key]; ok {
		return notification.Accepted{NotificationID: id}, nil
	}
	id := ids.New()
	f.accepted[key] = id
	return notification.Accepted{NotificationID: id}, nil
}
func (f *guestNotifier) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, k := range f.keys {
		if k == key {
			n++
		}
	}
	return n
}
func (f *guestNotifier) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func notificationTemplate(c *commercialHarness, kind, purpose string) businesshttp.CRMNotificationTemplate {
	c.t.Helper()
	in := businesshttp.CRMNotificationTemplateCommand{PropertyID: c.property.ID, Kind: kind, ProviderTemplateKey: "business.guest." + strings.ToLower(kind), Purpose: purpose, Channels: []string{"EMAIL", "SMS"}, Reason: "Approve the operational guest notification template"}
	op := in
	in.ApprovalID = c.approval("COMMERCIAL_GUEST_NOTIFICATION_TEMPLATE", "guest_privacy:write", "", nil, "", map[string]any{"property_id": c.property.ID, "command": op})
	return commercialData[businesshttp.CRMNotificationTemplate](c.t, c.must(c.owner, "POST", "/guest-notification-templates", in, 201))
}

// contactableGuest registers a guest with current operational consent and an
// authorized protected email destination.
func contactableGuest(c *commercialHarness, email string) crmRegisteredGuest {
	c.t.Helper()
	g := crmGuestFixture(c)
	crmConsent(c, g, "OPERATIONAL_CONTACT", "GRANTED", 0)
	c.must(c.owner, "PUT", "/guests/"+g.Guest.ID.String()+"/private", businesshttp.CRMPrivateCommand{Preferences: crm.PrivateProfile{Email: email, ContactPreference: "EMAIL"}, Reason: "Store protected contact after operational consent"}, 200)
	return g
}

func bookAndCancel(c *commercialHarness, g crmRegisteredGuest) reservations.Detail {
	c.t.Helper()
	in := reservationCommandFixture(c, false)
	in.Guests = []reservations.GuestCommand{{GuestID: &g.Guest.ID, SourceKind: g.SourceKind, SourceReference: g.SourceReference, IsPrimary: true}}
	v := commercialData[reservations.Detail](c.t, c.must(c.owner, "POST", "/reservations", in, 201))
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/cancel", reservations.Command{Version: v.Reservation.Version, Reason: "Guest cancelled the booking"}, 200)
	return v
}

func notificationJob(t *testing.T, c *commercialHarness, kind string, source ids.ID) businesshttp.CRMNotificationJob {
	t.Helper()
	var id ids.ID
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT id FROM guest_notification_jobs WHERE kind=$1 AND (source_id=$2 OR variables->>'reservation_id'=$2::text) ORDER BY created_at DESC LIMIT 1`, kind, source).Scan(&id); e != nil {
		t.Fatalf("job %s for %s: %v", kind, source, e)
	}
	return commercialData[businesshttp.CRMNotificationJobDetail](t, c.must(c.owner, "GET", "/guest-notifications/"+id.String(), nil, 200)).Job
}

func runNotifications(t *testing.T, c *commercialHarness) {
	t.Helper()
	if e := c.domain.ProcessGuestNotifications(t.Context(), 50); e != nil {
		t.Logf("notification batch: %v", e) // per-job outcomes are asserted from durable state
	}
}

// Durable guest notifications: approved templates, outage retry with one
// acceptance, duplicate safety under concurrent workers, consent withdrawal,
// suppression, supersession, operator cancel/retry, rejected requests,
// missing destinations, no plaintext contact outside protected storage.
func TestBusinessCRMNotificationsDurableConsentSuppressionAndRecovery(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	crmReviewer(c)
	f := newGuestNotifier()
	c.domain.Notification = f

	bad := businesshttp.CRMNotificationTemplateCommand{PropertyID: c.property.ID, Kind: "RESERVATION_CHANGE", ProviderTemplateKey: "business.guest.reservation_change", Purpose: "OPERATIONAL", Channels: []string{"EMAIL"}, Reason: "Template without independent approval"}
	c.must(c.owner, "POST", "/guest-notification-templates", bad, 403)
	bad.Purpose = "MARKETING"
	c.must(c.owner, "POST", "/guest-notification-templates", bad, 400) // operational kinds cannot become marketing
	notificationTemplate(c, "RESERVATION_CHANGE", "OPERATIONAL")

	// 1. Outage: the job retries with backoff and is accepted exactly once.
	email := "guest-" + ids.New().String() + "@example.invalid"
	g := contactableGuest(c, email)
	v := bookAndCancel(c, g)
	f.outage = 1
	runNotifications(t, c)
	j := notificationJob(t, c, "RESERVATION_CHANGE", v.Reservation.ID)
	key := "business-guest-notification:" + j.ID.String()
	if j.State != "RETRY" || j.LastError == nil || *j.LastError != "NOTIFICATION_SERVICE_UNAVAILABLE" || j.ProviderNotificationID != nil || !j.AvailableAt.After(time.Now()) {
		t.Fatalf("outage %+v", j)
	}
	runNotifications(t, c) // backoff not elapsed
	if f.count(key) != 1 {
		t.Fatalf("retried before backoff %d", f.count(key))
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE guest_notification_jobs SET available_at=now(),version=version+1 WHERE id=$1`, j.ID); e != nil {
		t.Fatal(e)
	}
	runNotifications(t, c)
	j = notificationJob(t, c, "RESERVATION_CHANGE", v.Reservation.ID)
	if j.State != "ACCEPTED" || j.DeliveryStatus != "ACCEPTED_BY_NOTIFICATION_SERVICE" || j.ProviderNotificationID == nil || j.Channel == nil || *j.Channel != "EMAIL" || f.count(key) != 2 {
		t.Fatalf("recovery %+v calls %d", j, f.count(key))
	}
	f.mu.Lock()
	last := f.calls[len(f.calls)-1]
	f.mu.Unlock()
	if last.Destinations.Email != email || last.Destinations.Phone != "" || last.Recipient.ID != g.Guest.ID || last.TemplateKey != "business.guest.reservation_change" || last.Variables["reservation_id"] != v.Reservation.ID.String() || last.Variables["action"] != "CANCELLED" {
		t.Fatalf("request %+v", last)
	}
	detail := commercialData[businesshttp.CRMNotificationJobDetail](t, c.must(c.owner, "GET", "/guest-notifications/"+j.ID.String(), nil, 200))
	if detail.DeliveryConfirmation != "UNKNOWN" || len(detail.Attempts) != 2 {
		t.Fatalf("delivery is never inferred %+v", detail)
	}
	for range 2 {
		runNotifications(t, c)
	}
	var jobs int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM guest_notification_jobs WHERE variables->>'reservation_id'=$1`, v.Reservation.ID.String()).Scan(&jobs); e != nil || jobs != 1 || f.count(key) != 2 {
		t.Fatalf("duplicate planning or delivery %d/%d %v", jobs, f.count(key), e)
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE guest_notification_jobs SET state='RETRY',version=version+1 WHERE id=$1`, j.ID); e == nil {
		t.Fatal("accepted notification reopened")
	}

	// 2. Concurrent workers: one acceptance per job.
	g2 := contactableGuest(c, "guest-"+ids.New().String()+"@example.invalid")
	v2 := bookAndCancel(c, g2)
	before := f.total()
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() { defer wg.Done(); runNotifications(t, c) }()
	}
	wg.Wait()
	j2 := notificationJob(t, c, "RESERVATION_CHANGE", v2.Reservation.ID)
	if j2.State != "ACCEPTED" || f.count("business-guest-notification:"+j2.ID.String()) != 1 || f.total() != before+1 {
		t.Fatalf("concurrent dispatch %+v calls %d", j2, f.total()-before)
	}

	// 3. Consent withdrawal before dispatch suppresses the job.
	g3 := contactableGuest(c, "guest-"+ids.New().String()+"@example.invalid")
	v3 := bookAndCancel(c, g3)
	crmConsent(c, g3, "OPERATIONAL_CONTACT", "WITHDRAWN", 1)
	before = f.total()
	runNotifications(t, c)
	j3 := notificationJob(t, c, "RESERVATION_CHANGE", v3.Reservation.ID)
	if j3.State != "SUPPRESSED" || j3.OutcomeReason == nil || *j3.OutcomeReason != "CONSENT_REQUIRED_OPERATIONAL_CONTACT" || f.total() != before {
		t.Fatalf("withdrawn consent %+v", j3)
	}

	// 4. Guest suppression list.
	g4 := contactableGuest(c, "guest-"+ids.New().String()+"@example.invalid")
	suppression := commercialData[businesshttp.CRMNotificationSuppression](t, c.must(c.owner, "POST", "/guests/"+g4.Guest.ID.String()+"/notification-suppressions", businesshttp.CRMNotificationSuppressionCommand{Scope: "ALL", EvidenceReference: "guest-do-not-contact-request", Reason: "Guest asked for no further messages"}, 201))
	c.must(c.owner, "POST", "/guests/"+g4.Guest.ID.String()+"/notification-suppressions", businesshttp.CRMNotificationSuppressionCommand{Scope: "KIND", EvidenceReference: "invalid-kind", Reason: "Kind scope without a kind"}, 400)
	v4 := bookAndCancel(c, g4)
	runNotifications(t, c)
	if j4 := notificationJob(t, c, "RESERVATION_CHANGE", v4.Reservation.ID); j4.State != "SUPPRESSED" || *j4.OutcomeReason != "GUEST_SUPPRESSION_ACTIVE" {
		t.Fatalf("suppression %+v", j4)
	}
	limited := c.user()
	c.secure(limited)
	c.join(limited, c.owner, []string{"guests:read", "guests:write"})
	lift := businesshttp.CRMVersionCommand{Version: suppression.Version, Reason: "Guest re-subscribed in writing"}
	c.must(limited, "POST", "/guests/"+g4.Guest.ID.String()+"/notification-suppressions/"+suppression.ID.String()+"/lift", lift, 403)
	c.must(c.owner, "POST", "/guests/"+g4.Guest.ID.String()+"/notification-suppressions/"+suppression.ID.String()+"/lift", lift, 200)
	c.must(c.owner, "POST", "/guests/"+g4.Guest.ID.String()+"/notification-suppressions/"+suppression.ID.String()+"/lift", lift, 409)

	// 5. A newer transition supersedes an older unsent one.
	g5 := contactableGuest(c, "guest-"+ids.New().String()+"@example.invalid")
	in := reservationCommandFixture(c, false)
	in.Guests = []reservations.GuestCommand{{GuestID: &g5.Guest.ID, SourceKind: g5.SourceKind, SourceReference: g5.SourceReference, IsPrimary: true}}
	in.PaymentResponsibility = "PAY_AT_PROPERTY"
	q := c.quote()
	in.Rooms[0].QuoteID = q.QuoteID
	op := in
	in.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &q.AmountMinor, q.Currency, map[string]any{"command": op})
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", in, 201))
	v5 := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/confirm", reservations.Command{Version: created.Reservation.Version, Reason: "Confirm under the reviewed guarantee"}, 200))
	if _, e := c.domain.PlanGuestNotifications(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", "/reservations/"+v5.Reservation.ID.String()+"/cancel", reservations.Command{Version: v5.Reservation.Version, Reason: "Guest cancelled the confirmed booking"}, 200)
	runNotifications(t, c)
	var superseded, accepted int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE state='CANCELLED' AND outcome_reason='SUPERSEDED_BY_NEWER_TRANSITION'),count(*) FILTER(WHERE state='ACCEPTED') FROM guest_notification_jobs WHERE variables->>'reservation_id'=$1`, v5.Reservation.ID.String()).Scan(&superseded, &accepted); e != nil || superseded != 1 || accepted != 1 {
		t.Fatalf("supersession %d/%d %v", superseded, accepted, e)
	}

	// 6. A rejected request fails for operator review; retry then succeeds once.
	g6 := contactableGuest(c, "guest-"+ids.New().String()+"@example.invalid")
	v6 := bookAndCancel(c, g6)
	f.reject = 1
	runNotifications(t, c)
	j6 := notificationJob(t, c, "RESERVATION_CHANGE", v6.Reservation.ID)
	if j6.State != "FAILED" || *j6.OutcomeReason != "NOTIFICATION_REQUEST_REJECTED" {
		t.Fatalf("rejected %+v", j6)
	}
	c.must(c.owner, "POST", "/guest-notifications/"+j6.ID.String()+"/retry", businesshttp.CRMVersionCommand{Version: j6.Version + 1, Reason: "Template configured; retry"}, 409)
	j6 = commercialData[businesshttp.CRMNotificationJob](t, c.must(c.owner, "POST", "/guest-notifications/"+j6.ID.String()+"/retry", businesshttp.CRMVersionCommand{Version: j6.Version, Reason: "Template configured in Notification Service; retry"}, 200))
	runNotifications(t, c)
	if j6 = notificationJob(t, c, "RESERVATION_CHANGE", v6.Reservation.ID); j6.State != "ACCEPTED" {
		t.Fatalf("operator retry %+v", j6)
	}
	c.must(c.owner, "POST", "/guest-notifications/"+j6.ID.String()+"/cancel", businesshttp.CRMVersionCommand{Version: j6.Version, Reason: "Accepted notifications cannot be cancelled"}, 409)

	// 7. Missing destination blocks; an unavailable client never fakes success.
	g7 := crmGuestFixture(c)
	crmConsent(c, g7, "OPERATIONAL_CONTACT", "GRANTED", 0)
	v7 := bookAndCancel(c, g7)
	runNotifications(t, c)
	j7 := notificationJob(t, c, "RESERVATION_CHANGE", v7.Reservation.ID)
	if j7.State != "BLOCKED" || *j7.OutcomeReason != "NO_AUTHORIZED_DESTINATION" {
		t.Fatalf("destination %+v", j7)
	}
	c.must(c.owner, "POST", "/guest-notifications/"+j7.ID.String()+"/cancel", businesshttp.CRMVersionCommand{Version: j7.Version, Reason: "No contact will be captured"}, 200)
	g8 := contactableGuest(c, "guest-"+ids.New().String()+"@example.invalid")
	v8 := bookAndCancel(c, g8)
	c.domain.Notification = nil
	runNotifications(t, c)
	if j8 := notificationJob(t, c, "RESERVATION_CHANGE", v8.Reservation.ID); j8.State != "RETRY" || *j8.LastError != "NOTIFICATION_CLIENT_UNAVAILABLE" || j8.ProviderNotificationID != nil {
		t.Fatalf("missing client %+v", j8)
	}
	c.domain.Notification = f

	// Tenant isolation and no plaintext contact outside protected storage.
	c.must(c.user(), "GET", "/guest-notifications/"+j.ID.String(), nil, 404)
	list := commercialData[struct {
		Items []map[string]any `json:"items"`
	}](t, c.must(c.owner, "GET", "/guest-notifications?property_id="+c.property.ID.String()+"&guest_id="+g.Guest.ID.String(), nil, 200))
	if len(list.Items) != 1 {
		t.Fatalf("guest filter %d", len(list.Items))
	}
	for _, table := range []string{"guest_notification_jobs", "guest_notification_attempts", "outbox_events", "audit_events"} {
		var stored string
		if e := c.db.Pool.QueryRow(t.Context(), `SELECT coalesce(string_agg(to_jsonb(e)::text,''),'') FROM `+table+` e`).Scan(&stored); e != nil {
			t.Fatal(e)
		}
		if strings.Contains(stored, email) {
			t.Fatalf("contact persisted in plaintext in %s", table)
		}
	}
}

// Waitlist communication: only an explicitly bound registered guest is
// notified; the offer notice is cancelled once the offer is no longer current,
// and the expiry notice follows the authoritative EXPIRED transition.
func TestBusinessCRMWaitlistNotificationsFollowAuthoritativeOffers(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	crmReviewer(c)
	f := newGuestNotifier()
	c.domain.Notification = f
	notificationTemplate(c, "WAITLIST_OFFER", "OPERATIONAL")
	notificationTemplate(c, "WAITLIST_EXPIRED", "OPERATIONAL")
	g := contactableGuest(c, "waitlisted-"+ids.New().String()+"@example.invalid")
	entry := commercialData[businesshttp.ReservationWaitlist](t, c.must(c.owner, "POST", "/waitlist", businesshttp.ReservationWaitlist{PropertyID: c.property.ID, ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1, GuestReference: g.SourceReference, ExpiresAt: time.Now().Add(time.Hour), Reason: "Wait for canonical availability"}, 201))
	unbound := commercialData[businesshttp.ReservationWaitlist](t, c.must(c.owner, "POST", "/waitlist", businesshttp.ReservationWaitlist{PropertyID: c.property.ID, ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1, GuestReference: "unbound-opaque-reference", ExpiresAt: time.Now().Add(time.Hour), Reason: "Wait without a registered guest"}, 201))
	bind := businesshttp.CRMWaitlistGuestBinding{GuestID: g.Guest.ID, EvidenceReference: "waitlist-request-registration", Reason: "Bind the registered guest who asked to wait"}
	c.must(c.owner, "POST", "/waitlist/"+entry.ID.String()+"/guest-binding", businesshttp.CRMWaitlistGuestBinding{GuestID: ids.New(), EvidenceReference: "unknown-guest", Reason: "Bind an unregistered guest"}, 403)
	c.must(c.owner, "POST", "/waitlist/"+entry.ID.String()+"/guest-binding", bind, 201)
	c.must(c.owner, "POST", "/waitlist/"+entry.ID.String()+"/guest-binding", bind, 409)

	q := c.quote()
	offer := commercialData[businesshttp.ReservationWaitlistOffer](t, c.must(c.owner, "POST", "/waitlist/"+entry.ID.String()+"/offers", businesshttp.ReservationWaitlistOfferCommand{Version: entry.Version, QuoteID: q.QuoteID, ProductVersion: c.product.Version, PoolVersion: c.pool.Version, Reason: "Offer the newly available canonical room nights"}, 201))
	runNotifications(t, c)
	j := notificationJob(t, c, "WAITLIST_OFFER", offer.ID)
	if j.State != "ACCEPTED" || j.GuestID == nil || *j.GuestID != g.Guest.ID {
		t.Fatalf("offer notice %+v", j)
	}
	f.mu.Lock()
	last := f.calls[len(f.calls)-1]
	f.mu.Unlock()
	if last.Variables["offer_id"] != offer.ID.String() || last.Variables["offer_expires_at"] == "" {
		t.Fatalf("offer variables %+v", last.Variables)
	}
	var unboundJobs int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM guest_notification_jobs WHERE variables->>'entry_id'=$1`, unbound.ID.String()).Scan(&unboundJobs); e != nil || unboundJobs != 0 {
		t.Fatalf("unbound entry notified %d %v", unboundJobs, e)
	}
	// Owning expiry transition (the reservation worker performs this in production).
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE waitlist_offers SET state='EXPIRED',version=version+1,updated_at=now() WHERE id=$1`, offer.ID); e != nil {
		t.Fatal(e)
	}
	runNotifications(t, c)
	expired := notificationJob(t, c, "WAITLIST_EXPIRED", offer.ID)
	if expired.State != "ACCEPTED" || expired.SourceKind != "WAITLIST_OFFER" {
		t.Fatalf("expiry notice %+v", expired)
	}
	if f.total() != 2 {
		t.Fatalf("waitlist sends %d", f.total())
	}
}
