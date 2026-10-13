package usecases

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/customernotify"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Tenant and guest announcements (§13.16, §13.24) through the customer
// notification contract (integrations/customernotify). The owner domain
// enforces sender authority (capability on the property), recipient
// identity (an active tenancy or stay of that property, re-checked at send
// time), a closed set of operational purposes (no marketing) and a per-
// property rate limit. Consent and preferred channels are applied by the
// recipient's own domain, which owns that data and sends through
// notification-service.

const (
	maxAnnouncementsPerDay  = 5
	customerNotifyMaxTries  = 8
	customerNotifyBaseDelay = time.Minute
)

// AnnouncementInput is a message to current tenants or guests.
type AnnouncementInput struct {
	Audience string `json:"audience" v:"required,enum=TENANTS|GUESTS"`
	Purpose  string `json:"purpose" v:"required,enum=SERVICE_ANNOUNCEMENT|UTILITY_INTERRUPTION|SERVICE_CHARGE_UPDATE|MAINTENANCE_ACCESS|SAFETY_NOTICE" doc:"Operational purposes only; marketing is not allowed"`
	Title    string `json:"title" v:"required,min=3,max=120"`
	Message  string `json:"message" v:"required,min=3,max=1000"`
}

// AnnouncementView is an announcement with its delivery summary.
type AnnouncementView struct {
	ID               ids.ID           `json:"id"`
	PropertyID       ids.ID           `json:"property_id"`
	Audience         string           `json:"audience"`
	Purpose          string           `json:"purpose"`
	Title            string           `json:"title"`
	Message          string           `json:"message"`
	SenderRole       string           `json:"sender_role"`
	Recipients       int32            `json:"recipients" doc:"Tenants/guests with a platform account"`
	SkippedNoAccount int32            `json:"skipped_no_account" doc:"Offline tenants without a platform account (not reachable through the platform)"`
	Delivery         map[string]int64 `json:"delivery" doc:"Per-recipient status counts: PENDING, ACCEPTED, SUPPRESSED, BLOCKED, FAILED"`
	CreatedAt        time.Time        `json:"created_at"`
}

func senderRole(d authz.Decision) string {
	if d.Via == nil {
		return "OWNER"
	}
	switch d.Via.Role {
	case authz.RoleCoOwner:
		return "CO_OWNER"
	case authz.RoleRepresentative:
		return "REPRESENTATIVE"
	case authz.RoleManager:
		return "MANAGER"
	}
	return "OWNER"
}

func (s *Service) announcementView(ctx context.Context, a db.OwnerAnnouncement) (AnnouncementView, error) {
	v := AnnouncementView{ID: a.ID, PropertyID: a.PropertyID, Audience: a.Audience, Purpose: a.Purpose, Title: a.Title, Message: a.Message,
		SenderRole: a.SenderRole, Recipients: a.Recipients, SkippedNoAccount: a.SkippedNoAccount, CreatedAt: a.CreatedAt, Delivery: map[string]int64{}}
	rows, err := s.q.AnnouncementDeliverySummary(ctx, s.dbq(ctx), &a.ID)
	for _, r := range rows {
		v.Delivery[r.Status] = r.N
	}
	return v, err
}

// Announce queues an announcement to the property's current tenants or guests.
func (s *Service) Announce(ctx context.Context, subj authz.Subject, propertyID ids.ID, in AnnouncementInput) (AnnouncementView, error) {
	if err := schema.Validate(in); err != nil {
		return AnnouncementView{}, err
	}
	capability := authz.CapTenancyManage
	if in.Audience == "GUESTS" {
		capability = authz.CapShortletBookings
	}
	var out AnnouncementView
	err := s.InTx(ctx, func(ctx context.Context) error {
		d, err := s.authorize(ctx, subj, authz.Do(capability), propertyID)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		n, err := s.q.CountRecentAnnouncements(ctx, tx, db.CountRecentAnnouncementsParams{PropertyID: propertyID, CreatedAt: now.Add(-24 * time.Hour)})
		if err != nil {
			return err
		}
		if n >= maxAnnouncementsPerDay {
			return conflict("ANNOUNCEMENT_LIMIT", "At most 5 announcements per property per day.")
		}
		p, err := s.q.GetProperty(ctx, tx, propertyID)
		if err != nil {
			return err
		}
		a, err := s.q.InsertAnnouncement(ctx, tx, db.InsertAnnouncementParams{ID: ids.New(), PropertyID: propertyID, SenderAccountID: accountOf(subj),
			SenderRole: senderRole(d), Audience: in.Audience, Purpose: in.Purpose, Title: in.Title, Message: in.Message, CreatedAt: now})
		if err != nil {
			return err
		}
		type rcpt struct {
			rel  ids.ID
			cust *ids.ID
		}
		var list []rcpt
		relType := "tenancy"
		if in.Audience == "TENANTS" {
			rows, err := s.q.ActiveTenantRecipients(ctx, tx, propertyID)
			if err != nil {
				return err
			}
			for _, r := range rows {
				list = append(list, rcpt{r.ID, r.CustomerRefID})
			}
		} else {
			relType = "shortlet_booking"
			rows, err := s.q.ActiveGuestRecipients(ctx, tx, db.ActiveGuestRecipientsParams{PropertyID: propertyID, Today: dayOf(now)})
			if err != nil {
				return err
			}
			for _, r := range rows {
				c := r.CustomerRefID
				list = append(list, rcpt{r.ID, &c})
			}
		}
		vars, _ := json.Marshal(map[string]string{"title": in.Title, "message": in.Message, "property_title": p.Title})
		var sent, skipped int32
		for _, r := range list {
			if r.cust == nil {
				skipped++
				continue
			}
			if err := s.q.InsertCustomerNotification(ctx, tx, db.InsertCustomerNotificationParams{ID: ids.New(), AnnouncementID: &a.ID, PropertyID: propertyID,
				RecipientRef: *r.cust, RelationshipType: relType, RelationshipID: r.rel, Purpose: in.Purpose, Variables: vars, NextAttemptAt: now}); err != nil {
				return err
			}
			sent++
		}
		if err := s.q.SetAnnouncementCounts(ctx, tx, db.SetAnnouncementCountsParams{ID: a.ID, Recipients: sent, SkippedNoAccount: skipped}); err != nil {
			return err
		}
		a.Recipients, a.SkippedNoAccount = sent, skipped
		if err := s.noteDelegateActivity(ctx, d, propertyID, "ANNOUNCEMENT_SENT", "owner_announcement", a.ID, in.Title); err != nil {
			return err
		}
		if err := s.record(ctx, "announcement.create", AggProperty, propertyID, map[string]any{"announcement_id": a.ID, "audience": in.Audience,
			"purpose": in.Purpose, "recipients": sent, "skipped_no_account": skipped}, nil); err != nil {
			return err
		}
		out, err = s.announcementView(ctx, a)
		return err
	})
	return out, err
}

// ListAnnouncements lists a property's announcements with delivery status.
func (s *Service) ListAnnouncements(ctx context.Context, subj authz.Subject, propertyID ids.ID) ([]AnnouncementView, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapPropertyView), propertyID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAnnouncements(ctx, s.dbq(ctx), propertyID)
	if err != nil {
		return nil, err
	}
	out := make([]AnnouncementView, 0, len(rows))
	for _, r := range rows {
		v, err := s.announcementView(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// RunCustomerNotifications hands due requests to the recipient domain.
// Rows are leased in a short transaction, the recipient domain is called
// outside any transaction (idempotency key = notification id) and the
// outcome is recorded in a second one. Without the recipient-domain
// contract requests are marked BLOCKED (kept and retried hourly, so they go
// out once the contract exists); a relationship that ended meanwhile
// suppresses the request.
func (s *Service) RunCustomerNotifications(ctx context.Context, batch int32) error {
	var due []db.CustomerNotification
	if err := s.InTx(ctx, func(ctx context.Context) error {
		tx, now := mustTx(ctx), s.now()
		var err error
		if due, err = s.q.DueCustomerNotifications(ctx, tx, db.DueCustomerNotificationsParams{NextAttemptAt: now, Limit: batch}); err != nil {
			return err
		}
		for _, n := range due {
			if err := s.q.LeaseCustomerNotification(ctx, tx, db.LeaseCustomerNotificationParams{ID: n.ID, NextAttemptAt: now.Add(5 * time.Minute)}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for _, n := range due {
		now := s.now()
		set := db.SetCustomerNotificationStatusParams{ID: n.ID, Status: n.Status, Channels: []string{}, UpdatedAt: now, NextAttemptAt: now}
		active, err := s.q.RelationshipStillActive(ctx, s.Pool, db.RelationshipStillActiveParams{RelationshipType: n.RelationshipType,
			RelationshipID: n.RelationshipID, RecipientRef: n.RecipientRef})
		if err != nil {
			return err
		}
		switch {
		case !active:
			set.Status, set.StatusReason = "SUPPRESSED", strp("RELATIONSHIP_ENDED")
		case s.CustomerNotify == nil:
			set.Status, set.StatusReason, set.NextAttemptAt = "BLOCKED", strp("RECIPIENT_DOMAIN_CONTRACT_UNAVAILABLE"), now.Add(time.Hour)
		default:
			vars := map[string]string{}
			_ = json.Unmarshal(n.Variables, &vars)
			cctx, cancel := providerContext(ctx, "recipient notification")
			res, err := s.CustomerNotify.Notify(cctx, customernotify.Request{NotificationID: n.ID, RecipientDomain: n.RecipientDomain, RecipientRef: n.RecipientRef,
				RelationshipType: n.RelationshipType, RelationshipID: n.RelationshipID, PropertyID: n.PropertyID, Purpose: n.Purpose, Variables: vars},
				"customer_notification:"+n.ID.String())
			providerOutcome(cctx, err)
			cancel()
			set.Attempts = 1
			var re *httpx.RemoteError
			switch {
			case err == nil && res.Status == customernotify.StatusAccepted:
				set.Status, set.Channels, set.ExternalRef = "ACCEPTED", nonNil(res.Channels), strp(res.Ref)
			case err == nil:
				set.Status, set.StatusReason = "SUPPRESSED", strp(firstOr([]string{res.Reason}, "SUPPRESSED_BY_RECIPIENT_DOMAIN"))
			case errors.As(err, &re) && re.Status >= 400 && re.Status < 500 && re.Status != 429:
				set.Status, set.StatusReason = "FAILED", strp("REJECTED_BY_RECIPIENT_DOMAIN")
			case n.Attempts+1 >= customerNotifyMaxTries:
				set.Status, set.StatusReason = "FAILED", strp("RETRIES_EXHAUSTED")
			default:
				set.Status, set.StatusReason = "PENDING", strp("RETRY_SCHEDULED")
				set.NextAttemptAt = now.Add(customerNotifyBaseDelay << min(n.Attempts, 6))
			}
		}
		if err := s.InTx(ctx, func(ctx context.Context) error {
			return s.q.SetCustomerNotificationStatus(ctx, mustTx(ctx), set)
		}); err != nil {
			return err
		}
	}
	return nil
}
