package authz

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Delegable capabilities (§13.21, §16). A co-owner holds them by role; an
// authorized representative or delegated manager only when explicitly
// granted for the exact property, inside its validity window.
const (
	CapPropertyView       Capability = "property.view"
	CapPropertyEdit       Capability = "property.edit"
	CapPropertyMedia      Capability = "property.media"
	CapUnitsManage        Capability = "units.manage"
	CapAssetsManage       Capability = "assets.manage"
	CapListingManage      Capability = "listing.manage"
	CapListingPublish     Capability = "listing.publish"
	CapListingPause       Capability = "listing.pause"
	CapPricingManage      Capability = "pricing.manage"
	CapAvailability       Capability = "availability.manage"
	CapLeadsManage        Capability = "leads.manage"
	CapViewingsManage     Capability = "viewings.manage"
	CapApplicationReview  Capability = "applications.review"
	CapApplicationDecide  Capability = "applications.decide"
	CapOffersManage       Capability = "offers.manage"
	CapAgreementsManage   Capability = "agreements.manage"
	CapTenancyManage      Capability = "tenancy.manage"
	CapNoticesIssue       Capability = "notices.issue"
	CapRentView           Capability = "rent.view"
	CapUtilitiesManage    Capability = "utilities.manage"
	CapServiceCharges     Capability = "service_charges.manage"
	CapShortletManage     Capability = "shortlet.manage"
	CapShortletBookings   Capability = "shortlet.bookings"
	CapMaintenanceReq     Capability = "maintenance.request"
	CapMaintenanceApprove Capability = "maintenance.approve"
	CapProfessionals      Capability = "professionals.appoint"
	CapAccessGrant        Capability = "access.grant"
	CapReportsView        Capability = "reports.view"
	CapDisputesManage     Capability = "disputes.manage"
)

// Owner-only authorities: never delegable to managers or representatives.
// payout.manage is additionally rejected by a database check constraint on
// manager_permissions.
const (
	CapOwnershipManage   Capability = "ownership.manage"
	CapOwnershipTransfer Capability = "ownership.transfer"
	CapSaleManage        Capability = "sale.manage"
	CapManagerAssign     Capability = "manager.assign"
	CapPayoutManage      Capability = "payout.manage"
	CapPropertyArchive   Capability = "property.archive"
)

// DelegableCapabilities lists what may be granted to managers/representatives.
func DelegableCapabilities() []Capability {
	return []Capability{CapPropertyView, CapPropertyEdit, CapPropertyMedia, CapUnitsManage, CapAssetsManage, CapListingManage,
		CapListingPublish, CapListingPause, CapPricingManage, CapAvailability, CapLeadsManage, CapViewingsManage,
		CapApplicationReview, CapApplicationDecide, CapOffersManage, CapAgreementsManage, CapTenancyManage, CapNoticesIssue,
		CapRentView, CapUtilitiesManage, CapServiceCharges, CapShortletManage, CapShortletBookings, CapMaintenanceReq,
		CapMaintenanceApprove, CapProfessionals, CapAccessGrant, CapReportsView, CapDisputesManage}
}

// IsDelegable reports whether c may be granted to a delegate.
func IsDelegable(c Capability) bool {
	for _, d := range DelegableCapabilities() {
		if d == c {
			return true
		}
	}
	return false
}

// Read reports the read action of a capability ("<cap>:read").
func Read(c Capability) Action { return Action(string(c) + ":read") }

// Do reports the mutation action of a capability.
func Do(c Capability) Action { return Action(c) }

// Account-scoped (non-property) owner actions of Sections 6–8.
const (
	ActProfileRead        Action = "owner.profile.read"
	ActProfileWrite       Action = "owner.profile.write"
	ActPayoutRead         Action = "owner.payout.read"
	ActPayoutWrite        Action = "owner.payout.write" // step-up
	ActTaxWrite           Action = "owner.tax.write"    // step-up
	ActPropertyCreate     Action = "owner.property.create"
	ActPortfolioRead      Action = "owner.portfolio.read"
	ActIdentityVerify     Action = "owner.identity.verify"
	ActPrivacyRequest     Action = "owner.privacy.request"
	ActFinancingConsent   Action = "owner.financing.consent"
	ActManagerSearch      Action = "owner.manager.search"
	ActMarketInsightsRead Action = "owner.market_insights.read"
	ActPrivacyVerify      Action = "owner.privacy.verify"  // step-up confirms the requester
	ActAccountDispute     Action = "owner.dispute.account" // disputes about the account itself or its payout accounts
	ActReportsRead        Action = "owner.reports.read"    // portfolio-wide reports, dashboard, calendar, own audit trail
)

// Internal (service principal) actions.
const (
	ActInternalListingRead     Action = "internal.listing.read"
	ActInternalPublicSummary   Action = "internal.property.public_summary"
	ActInternalCustomerIntake  Action = "internal.customer.intake" // marketplace-originated inquiry/viewing/application/booking
	ActInternalManagerRespond  Action = "internal.manager.respond" // manager accepts/declines a mandate
	ActInternalManagerActivity Action = "internal.manager.activity"
	ActInternalProviderUpdate  Action = "internal.provider.update" // artisan job / professional appointment status
	ActInternalAdminModerate   Action = "internal.admin.moderate"
	ActInternalAdminDispute    Action = "internal.admin.dispute"
	ActInternalAdminPrivacy    Action = "internal.admin.privacy"
	ActInternalAdminVerify     Action = "internal.admin.ownership"
	ActSystemProperty          Action = "system.property"
	ActTransferCancel          Action = "ownership.transfer.cancel"
	ActInternalListingReport   Action = "internal.listing.report"
	ActInternalAdminTemplates  Action = "internal.admin.legal_templates" // agreement/notice templates (controlled legal configuration)
	ActLegalTemplatesRead      Action = "owner.legal_templates.read"
	ActInternalDataRoomAccess  Action = "internal.data_room.access" // grantee (buyer or professional) reads a sale data room
)

var (
	activeOnly = []domain.AccountStatus{domain.StatusActive}
	primary    = []Role{RoleOwner, RoleCoOwner}
	delegates  = []Role{RoleOwner, RoleCoOwner, RoleRepresentative, RoleManager}
	ownerSide  = []Role{RoleOwner, RoleCoOwner, RoleRepresentative}
	managerSvc = []ServiceGrant{{Service: platform.PropertyManagerService, ActorDomains: []platform.Domain{platform.DomainPropertyManager}, RequiresActor: true},
		{Service: platform.BusinessOperationsService, ActorDomains: []platform.Domain{platform.DomainBusinessOperations}, RequiresActor: true}}
)

// PropertyRules is the Sections 6–8 policy table.
func PropertyRules() []Rule {
	var rules []Rule
	for _, c := range DelegableCapabilities() {
		// Reads: any live relationship holding the capability, also on a
		// restricted property, in any signed-in account state.
		rules = append(rules, Rule{Action: Read(c), PropertyScoped: true, Roles: delegates, Capability: c,
			AllowRestricted: true, Services: managerSvc})
		r := Rule{Action: Do(c), PropertyScoped: true, Roles: delegates, Capability: c, AccountStatuses: activeOnly, Services: managerSvc}
		switch c {
		case CapListingPublish:
			r.CoOwnerConsent, r.RequireVerifiedAuthority = true, true
		case CapPricingManage, CapAgreementsManage:
			// Price changes on published listings and leases need co-owner
			// consent (§13.4); the use case decides when it applies.
			r.CoOwnerConsent = true
		case CapAccessGrant:
			r.StepUp = true
		}
		rules = append(rules, r)
	}
	ownerOnly := func(c Capability, roles []Role, stepUp, consent, verified bool) Rule {
		return Rule{Action: Do(c), PropertyScoped: true, Roles: roles, Capability: c, AccountStatuses: activeOnly,
			StepUp: stepUp, CoOwnerConsent: consent, RequireVerifiedAuthority: verified}
	}
	rules = append(rules,
		// Ownership evidence/parties: owners and co-owners; representatives
		// only when explicitly granted (probate executors, attorneys).
		ownerOnly(CapOwnershipManage, ownerSide, false, false, false),
		Rule{Action: Read(CapOwnershipManage), PropertyScoped: true, Roles: ownerSide, Capability: CapPropertyView, AllowRestricted: true},
		ownerOnly(CapOwnershipTransfer, primary, true, true, true),
		ownerOnly(CapSaleManage, primary, false, true, true),
		Rule{Action: Read(CapSaleManage), PropertyScoped: true, Roles: primary, AllowRestricted: true},
		ownerOnly(CapManagerAssign, primary, false, true, false),
		Rule{Action: Read(CapManagerAssign), PropertyScoped: true, Roles: ownerSide, Capability: CapPropertyView, AllowRestricted: true},
		ownerOnly(CapPropertyArchive, primary, false, false, false),
		Rule{Action: ActTransferCancel, PropertyScoped: true, Roles: primary, AccountStatuses: activeOnly},
		// Account-scoped owner actions.
		Rule{Action: ActProfileRead, SelfService: true, AccountStatuses: signedIn},
		Rule{Action: ActProfileWrite, SelfService: true, AccountStatuses: signedIn},
		Rule{Action: ActPayoutRead, SelfService: true, AccountStatuses: signedIn},
		Rule{Action: ActPayoutWrite, SelfService: true, AccountStatuses: activeOnly, StepUp: true},
		Rule{Action: ActTaxWrite, SelfService: true, AccountStatuses: activeOnly, StepUp: true},
		Rule{Action: ActPropertyCreate, SelfService: true, AccountStatuses: activeOnly},
		Rule{Action: ActPortfolioRead, SelfService: true, AccountStatuses: signedIn},
		Rule{Action: ActIdentityVerify, SelfService: true, AccountStatuses: []domain.AccountStatus{domain.StatusPendingVerification, domain.StatusActive, domain.StatusRestricted}},
		Rule{Action: ActPrivacyRequest, SelfService: true, AccountStatuses: signedIn},
		Rule{Action: ActFinancingConsent, SelfService: true, AccountStatuses: activeOnly},
		Rule{Action: ActManagerSearch, SelfService: true, AccountStatuses: signedIn},
		Rule{Action: ActMarketInsightsRead, SelfService: true, AccountStatuses: signedIn},
		Rule{Action: ActPrivacyVerify, SelfService: true, AccountStatuses: signedIn, StepUp: true},
		Rule{Action: ActAccountDispute, SelfService: true, AccountStatuses: signedIn},
		Rule{Action: ActReportsRead, SelfService: true, AccountStatuses: signedIn},
		// Internal contracts (§9): capability-scoped per calling service.
		Rule{Action: ActInternalListingRead, Services: []ServiceGrant{{Service: platform.MarketplaceService}, {Service: platform.AdminService}}},
		Rule{Action: ActInternalListingReport, Services: []ServiceGrant{{Service: platform.MarketplaceService}}},
		Rule{Action: ActInternalPublicSummary, Services: []ServiceGrant{{Service: platform.MarketplaceService}, {Service: platform.AdminService}}},
		Rule{Action: ActInternalCustomerIntake, Services: []ServiceGrant{{Service: platform.MarketplaceService, ActorDomains: []platform.Domain{platform.DomainMarketplace}, RequiresActor: true}}},
		Rule{Action: ActInternalManagerRespond, Services: managerSvc},
		Rule{Action: ActInternalManagerActivity, Services: managerSvc},
		// Providers act for themselves: the delegated actor must be the
		// artisan/professional the quote, job or appointment belongs to.
		Rule{Action: ActInternalProviderUpdate, Services: []ServiceGrant{
			{Service: platform.ArtisanService, ActorDomains: []platform.Domain{platform.DomainArtisan}, RequiresActor: true},
			{Service: platform.ProfessionalServicesService, ActorDomains: []platform.Domain{platform.DomainProfessionalServices}, RequiresActor: true}}},
		Rule{Action: ActInternalAdminModerate, Services: adminActor},
		Rule{Action: ActInternalAdminDispute, Services: adminActor},
		Rule{Action: ActInternalAdminPrivacy, Services: adminActor},
		Rule{Action: ActInternalAdminVerify, Services: adminActor},
		Rule{Action: ActInternalAdminTemplates, Services: adminActor},
		Rule{Action: ActLegalTemplatesRead, SelfService: true, AccountStatuses: signedIn},
		Rule{Action: ActInternalDataRoomAccess, Services: []ServiceGrant{
			{Service: platform.MarketplaceService, ActorDomains: []platform.Domain{platform.DomainMarketplace}, RequiresActor: true},
			{Service: platform.ProfessionalServicesService, ActorDomains: []platform.Domain{platform.DomainProfessionalServices}, RequiresActor: true}}},
		Rule{Action: ActSystemProperty, System: true},
	)
	return rules
}

// AllRules is the complete policy table of the service.
func AllRules() []Rule { return append(AccountRules(), PropertyRules()...) }
