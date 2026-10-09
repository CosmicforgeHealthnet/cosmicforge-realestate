package usecases

import (
	"context"
	"errors"
	"slices"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/media"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Evidence is the result of verifying, before any database transaction is
// opened, that referenced media-document-service files exist, are usable and
// belong to the acting party (owner account / property, or the customer for
// marketplace calls). Use cases only accept media IDs covered by it.
type Evidence struct {
	checked map[ids.ID]media.Media
}

// require fails when a media ID was not verified in the pre phase.
func (e Evidence) require(mediaIDs ...ids.ID) error {
	for _, m := range mediaIDs {
		if _, ok := e.checked[m]; !ok {
			return errors.New("usecases: media " + m.String() + " was not verified before the transaction")
		}
	}
	return nil
}

// Scopes whose property is resolved for an evidence check.
const (
	ScopeProperty       = "property"
	ScopeAgreement      = "agreement"
	ScopeTenancy        = "tenancy"
	ScopeNotice         = "notice"
	ScopeUtilityAccount = "utility_account"
	ScopeBudget         = "service_charge_budget"
	ScopeSale           = "sale_transaction"
	ScopeBooking        = "shortlet_booking"
	ScopeTurnover       = "turnover_job"
	ScopeDamageClaim    = "damage_claim"
	ScopeMaintenance    = "maintenance_request"
)

var scopeTables = map[string]string{ScopeProperty: "properties", ScopeAgreement: "agreements", ScopeTenancy: "tenancies",
	ScopeNotice: "notices", ScopeUtilityAccount: "utility_accounts", ScopeBudget: "service_charge_budgets", ScopeSale: "sale_transactions",
	ScopeBooking: "shortlet_bookings", ScopeTurnover: "turnover_jobs", ScopeDamageClaim: "damage_claims",
	ScopeMaintenance: "maintenance_requests"}

func (s *Service) scopeProperty(ctx context.Context, scope string, id ids.ID) (ids.ID, error) {
	table, ok := scopeTables[scope]
	if !ok {
		return ids.Nil, errors.New("usecases: unknown evidence scope " + scope)
	}
	if scope == ScopeProperty {
		return id, nil
	}
	var pid ids.ID
	err := s.dbq(ctx).QueryRow(ctx, `SELECT property_id FROM `+table+` WHERE id = $1`, id).Scan(&pid)
	if isNoRows(err) {
		return ids.Nil, notFound(scope)
	}
	return pid, err
}

// CheckMediaEvidence verifies owner-side evidence for an action on the scope's
// property. Called outside transactions (CommandPre).
func (s *Service) CheckMediaEvidence(ctx context.Context, subj authz.Subject, scope string, id ids.ID, action authz.Action, mediaIDs []ids.ID, classes ...string) (Evidence, error) {
	ev := Evidence{checked: map[ids.ID]media.Media{}}
	mediaIDs = uniqueIDs(mediaIDs)
	if len(mediaIDs) == 0 {
		return ev, nil
	}
	propertyID, err := s.scopeProperty(ctx, scope, id)
	if err != nil {
		return ev, err
	}
	if _, err := s.authorize(ctx, subj, action, propertyID); err != nil {
		if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
			return ev, notFound(scope)
		}
		return ev, err
	}
	owners := []ids.ID{s.actingAccount(ctx, subj, propertyID), propertyID}
	for _, m := range mediaIDs {
		md, err := s.ownedMedia(ctx, m, owners, classes...)
		if err != nil {
			return ev, err
		}
		ev.checked[m] = md
	}
	return ev, nil
}

// CheckCustomerEvidence verifies media uploaded by the marketplace customer
// acting through marketplace-service (tenant-side evidence).
func (s *Service) CheckCustomerEvidence(ctx context.Context, subj authz.Subject, mediaIDs []ids.ID, classes ...string) (Evidence, error) {
	ev := Evidence{checked: map[ids.ID]media.Media{}}
	mediaIDs = uniqueIDs(mediaIDs)
	if len(mediaIDs) == 0 {
		return ev, nil
	}
	customer, err := s.requireCustomer(ctx, subj)
	if err != nil {
		return ev, err
	}
	return s.checkActorMedia(ctx, ev, platform.DomainMarketplace, customer, mediaIDs, classes)
}

// CheckProviderEvidence verifies files uploaded by the calling provider
// (artisan or professional) in its own domain.
func (s *Service) CheckProviderEvidence(ctx context.Context, subj authz.Subject, mediaIDs []ids.ID, classes ...string) (Evidence, error) {
	ev := Evidence{checked: map[ids.ID]media.Media{}}
	mediaIDs = uniqueIDs(mediaIDs)
	if len(mediaIDs) == 0 {
		return ev, nil
	}
	if _, err := s.Authz.Require(ctx, subj, authz.ActInternalProviderUpdate, authz.Resource{}); err != nil {
		return ev, err
	}
	return s.checkActorMedia(ctx, ev, subj.ActorDomain, subj.ActorID, mediaIDs, classes)
}

func (s *Service) checkActorMedia(ctx context.Context, ev Evidence, domain platform.Domain, actor ids.ID, mediaIDs []ids.ID, classes []string) (Evidence, error) {
	if s.Media == nil {
		return ev, unavailable("Evidence upload", "media-document-service")
	}
	for _, id := range mediaIDs {
		m, err := s.Media.Get(ctx, id)
		if err != nil {
			var re *httpx.RemoteError
			if errors.As(err, &re) && (re.Status == 404 || re.Status == 403) {
				return ev, precondition("MEDIA_NOT_FOUND", "The media file does not exist or is not accessible.")
			}
			return ev, downstream("media-document-service", err)
		}
		if m.OwnerDomain != string(domain) || m.OwnerID != actor {
			return ev, precondition("MEDIA_NOT_OWNED", "The media file was not uploaded by this actor.")
		}
		if len(classes) > 0 && !slices.Contains(classes, m.MediaClass) {
			return ev, precondition("MEDIA_CLASS_NOT_ALLOWED", "The media file's class is not allowed here.")
		}
		if p := media.Projection(m.Status); p == "REJECTED" || p == "DELETED" {
			return ev, precondition("MEDIA_UNUSABLE", "The media file was rejected or deleted.")
		}
		ev.checked[id] = m
	}
	return ev, nil
}

func uniqueIDs(in []ids.ID) []ids.ID {
	var out []ids.ID
	for _, id := range in {
		if !id.IsZero() && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func optIDs(ps ...*ids.ID) []ids.ID {
	var out []ids.ID
	for _, p := range ps {
		if p != nil {
			out = append(out, *p)
		}
	}
	return out
}
