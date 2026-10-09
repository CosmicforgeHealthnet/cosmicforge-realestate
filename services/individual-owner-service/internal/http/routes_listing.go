package http

import (
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

const (
	tagDelegation   = "Delegation"
	tagListings     = "Listings"
	tagPricing      = "Pricing"
	tagAvailability = "Availability"
)

func delegationRoutes() []Route {
	return []Route{
		List("/managers/search", "searchManagers", tagDelegation, "Search managers (property-manager-service)",
			func(c *Call, _ Page) (pagination.Response[usecases.ProposalView], error) {
				return slice(c.S.SearchManagers(c.Ctx, c.Subj))
			}).Describe("Manager discovery is owned by property-manager-service, which has no implemented contract yet: 503 FEATURE_UNAVAILABLE.").Errs(503),
		List("/manager-proposals", "listManagerProposals", tagDelegation, "List a property's management proposals",
			func(c *Call, _ Page) (pagination.Response[usecases.ProposalView], error) {
				pid, err := c.QueryID("property_id")
				if err != nil {
					return pagination.Response[usecases.ProposalView]{}, err
				}
				return slice(c.S.ListProposals(c.Ctx, c.Subj, pid))
			}).Q(Param{Name: "property_id", Required: true}),
		Command("POST", "/manager-proposals", "createManagerProposal", tagDelegation, "Propose a management mandate to a manager", http.StatusCreated,
			func(c *Call, in *usecases.ProposalInput) (usecases.ProposalView, error) {
				return c.S.CreateProposal(c.Ctx, c.Subj, *in)
			}).
			Describe("Capabilities are an explicit subset of the role's capability matrix; payout and ownership authorities are never delegable.").
			Capture("manager_proposal_id", "data.id"),
		Command("POST", "/manager-proposals/{manager_proposal_id}/withdraw", "withdrawManagerProposal", tagDelegation, "Withdraw an open proposal", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.ProposalView, error) {
				return c.S.WithdrawProposal(c.Ctx, c.Subj, c.ID("manager_proposal_id"))
			}).Errs(409),
		List("/manager-assignments", "listManagerAssignments", tagDelegation, "List manager assignments across my properties",
			func(c *Call, p Page) (pagination.Response[usecases.AssignmentView], error) {
				return c.S.ListAssignments(c.Ctx, c.Subj, c.Query("status"), p.Cursor, p.Limit)
			}).Q(Param{Name: "status", Enum: []string{"PROPOSED", "ACCEPTED", "ACTIVE", "SUSPENDED", "EXPIRING", "REVOKED", "EXPIRED", "TERMINATED"}}),
		Command("POST", "/manager-assignments", "createManagerAssignment", tagDelegation, "Create an assignment from a proposal", http.StatusCreated,
			func(c *Call, in *usecases.AssignmentInput) (usecases.AssignmentView, error) {
				return c.S.CreateAssignment(c.Ctx, c.Subj, *in)
			}).Describe("Requires co-owner consent (MANAGER_ASSIGNMENT, target = proposal) when the property has other principals. "+
			"The assignment is PROPOSED until property-manager-service records the manager's acceptance.").
			Capture("manager_assignment_id", "data.id").Errs(409, 422),
		Get("/manager-assignments/{manager_assignment_id}", "getManagerAssignment", tagDelegation, "Get an assignment with its permission history",
			func(c *Call) (usecases.AssignmentView, error) {
				return c.S.GetAssignment(c.Ctx, c.Subj, c.ID("manager_assignment_id"))
			}),
		Update("PATCH", "/manager-assignments/{manager_assignment_id}", "updateManagerAssignment", tagDelegation, "Change capabilities, limits or term (If-Match)", false,
			func(c *Call, in *usecases.AssignmentPatch) (usecases.AssignmentView, error) {
				return c.S.UpdateAssignment(c.Ctx, c.Subj, c.ID("manager_assignment_id"), c.Version, *in)
			}).Describe("Capability/limit changes append a new immutable permission version; widening needs co-owner consent.").Errs(422),
		Command("POST", "/manager-assignments/{manager_assignment_id}/suspend", "suspendManagerAssignment", tagDelegation, "Suspend an assignment", http.StatusOK,
			func(c *Call, in *usecases.ReasonCodeInput) (usecases.AssignmentView, error) {
				return c.S.ChangeAssignmentStatus(c.Ctx, c.Subj, c.ID("manager_assignment_id"), "suspend", *in)
			}),
		Command("POST", "/manager-assignments/{manager_assignment_id}/resume", "resumeManagerAssignment", tagDelegation, "Resume a suspended assignment", http.StatusOK,
			func(c *Call, in *usecases.ReasonCodeInput) (usecases.AssignmentView, error) {
				return c.S.ChangeAssignmentStatus(c.Ctx, c.Subj, c.ID("manager_assignment_id"), "resume", *in)
			}),
		Command("POST", "/manager-assignments/{manager_assignment_id}/revoke", "revokeManagerAssignment", tagDelegation, "Revoke an assignment (immediate)", http.StatusOK,
			func(c *Call, in *usecases.ReasonCodeInput) (usecases.AssignmentView, error) {
				return c.S.ChangeAssignmentStatus(c.Ctx, c.Subj, c.ID("manager_assignment_id"), "revoke", *in)
			}).Describe("Revocation blocks every delegated capability check at once and emits manager.assignment.revoked.v1."),
		List("/manager-assignments/{manager_assignment_id}/activity", "listManagerActivity", tagDelegation, "Manager activity on the assignment",
			func(c *Call, p Page) (pagination.Response[usecases.ActivityView], error) {
				return c.S.ListActivity(c.Ctx, c.Subj, c.ID("manager_assignment_id"), p.Cursor, p.Limit)
			}),
	}
}

var listingFilters = []Param{{Name: "property_id"}, {Name: "status", Enum: []string{"DRAFT", "READY_FOR_REVIEW", "PUBLISHED", "PAUSED", "WITHDRAWN", "ARCHIVED"}},
	{Name: "transaction_type", Enum: []string{"RENT_LONG_TERM", "SALE", "SHORT_LET", "COMMERCIAL_LEASE", "LAND_LEASE"}}}

func listingRoutes() []Route {
	lid := func(c *Call) ids.ID { return c.ID("listing_id") }
	return []Route{
		List("/listings", "listListings", tagListings, "List listings across my properties",
			func(c *Call, p Page) (pagination.Response[usecases.ListingView], error) {
				pid, err := c.QueryID("property_id")
				if err != nil {
					return pagination.Response[usecases.ListingView]{}, err
				}
				return c.S.ListListings(c.Ctx, c.Subj, pid, c.Query("status"), c.Query("transaction_type"), p.Cursor, p.Limit)
			}).Q(listingFilters...),
		Command("POST", "/listings", "createListing", tagListings, "Create a DRAFT listing", http.StatusCreated,
			func(c *Call, in *usecases.CreateListingInput) (usecases.ListingView, error) {
				return c.S.CreateListing(c.Ctx, c.Subj, *in)
			}).
			Capture("listing_id", "data.id").Capture("listing_version", "data.version").Errs(422),
		Get("/listings/{listing_id}", "getListing", tagListings, "Get a listing",
			func(c *Call) (usecases.ListingView, error) { return c.S.GetListing(c.Ctx, c.Subj, lid(c)) }),
		Update("PATCH", "/listings/{listing_id}", "updateListing", tagListings, "Edit a listing (If-Match)", false,
			func(c *Call, in *usecases.UpdateListingInput) (usecases.ListingView, error) {
				return c.S.UpdateListing(c.Ctx, c.Subj, lid(c), c.Version, *in)
			}).Describe("Editing a published listing must keep it publishable; the edit is emitted as a new immutable version (listing.updated.v1).").Errs(422),
		Query("/listings/{listing_id}/preview", "previewListing", tagListings, "Preview the customer view and the publication checklist",
			func(c *Call, _ *Empty) (usecases.PreviewView, error) {
				return c.S.PreviewListing(c.Ctx, c.Subj, lid(c))
			}),
		Command("POST", "/listings/{listing_id}/publish", "publishListing", tagListings, "Publish (or resume) a listing", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.ListingView, error) {
				return c.S.PublishListing(c.Ctx, c.Subj, lid(c))
			}).
			Describe("Validates property status, verified ownership authority, identity verification (policy), co-owner consent (LISTING_PUBLISH), "+
				"listing details, ready photos, full price disclosure and availability. 422 LISTING_NOT_PUBLISHABLE lists every unmet requirement.").Errs(409, 422),
		Command("POST", "/listings/{listing_id}/pause", "pauseListing", tagListings, "Pause a published listing", http.StatusOK,
			func(c *Call, in *usecases.ListingReasonInput) (usecases.ListingView, error) {
				return c.S.PauseListing(c.Ctx, c.Subj, lid(c), *in)
			}).Errs(409),
		Command("POST", "/listings/{listing_id}/withdraw", "withdrawListing", tagListings, "Withdraw a listing from the market", http.StatusOK,
			func(c *Call, in *usecases.ListingReasonInput) (usecases.ListingView, error) {
				return c.S.WithdrawListing(c.Ctx, c.Subj, lid(c), *in)
			}).Errs(409),
		Command("POST", "/listings/{listing_id}/archive", "archiveListing", tagListings, "Archive a listing", http.StatusOK,
			func(c *Call, in *usecases.ListingReasonInput) (usecases.ListingView, error) {
				return c.S.ArchiveListing(c.Ctx, c.Subj, lid(c), *in)
			}).Errs(409),
		Command("POST", "/listings/{listing_id}/copy", "copyListing", tagListings, "Copy a listing into a new draft", http.StatusCreated,
			func(c *Call, in *usecases.CopyListingInput) (usecases.ListingView, error) {
				return c.S.CopyListing(c.Ctx, c.Subj, lid(c), *in)
			}).Capture("copied_listing_id", "data.id"),
		List("/listings/{listing_id}/versions", "listListingVersions", tagListings, "Immutable published versions",
			func(c *Call, _ Page) (pagination.Response[usecases.ListingVersionView], error) {
				return slice(c.S.ListingVersions(c.Ctx, c.Subj, lid(c)))
			}),
		Get("/listings/{listing_id}/pricing", "getListingPricing", tagPricing, "Active price model",
			func(c *Call) (usecases.ScheduleView, error) { return c.S.GetPricing(c.Ctx, c.Subj, lid(c)) }),
		Update("PUT", "/listings/{listing_id}/pricing", "putListingPricing", tagPricing, "Set the price model (new immutable version; If-Match \"0\" first)", true,
			func(c *Call, in *usecases.PricingInput) (usecases.ScheduleView, error) {
				return c.S.PutPricing(c.Ctx, c.Subj, lid(c), c.Version, *in)
			}).Describe("Every mandatory fee must be disclosed with a configured category; platform fees cannot be owner-defined. " +
			"A price change on a published listing needs co-owner consent (PRICE_CHANGE) and republishes.").Errs(422),
		List("/listings/{listing_id}/price-history", "listPriceHistory", tagPricing, "All pricing versions",
			func(c *Call, _ Page) (pagination.Response[usecases.PricingView], error) {
				return slice(c.S.PriceHistory(c.Ctx, c.Subj, lid(c)))
			}),
		Query("/listings/{listing_id}/cost-preview", "previewListingCost", tagPricing, "All-in cost for a scenario (shared calculator)",
			func(c *Call, in *usecases.CostPreviewInput) (usecases.CostPreviewView, error) {
				return c.S.CostPreview(c.Ctx, c.Subj, lid(c), *in)
			}).Errs(422),
		Get("/listings/{listing_id}/availability", "getListingAvailability", tagAvailability, "Listing availability",
			func(c *Call) (usecases.AvailabilityView, error) { return c.S.GetAvailability(c.Ctx, c.Subj, lid(c)) }),
		Update("PUT", "/listings/{listing_id}/availability", "putListingAvailability", tagAvailability, "Set availability (If-Match on the availability version)", false,
			func(c *Call, in *usecases.AvailabilityInput) (usecases.AvailabilityView, error) {
				return c.S.PutAvailability(c.Ctx, c.Subj, lid(c), c.Version, *in)
			}).Describe("Counts as a freshness confirmation. OFF_MARKET, OCCUPIED or SOLD pause a published listing."),
		Command("POST", "/listings/{listing_id}/confirm-availability", "confirmListingAvailability", tagAvailability, "Confirm the listing is still available", http.StatusOK,
			func(c *Call, in *usecases.ConfirmAvailabilityInput) (usecases.ListingView, error) {
				return c.S.ConfirmAvailability(c.Ctx, c.Subj, lid(c), *in)
			}).Describe("Resolves the freshness check, schedules the next one and resumes a listing paused only for missed freshness.").Errs(409),
	}
}

func listingInternalRoutes() []Route {
	return []Route{
		Get("/listings/{listing_id}", "internalGetListing", tagInternal, "Authoritative listing read-back (marketplace)",
			func(c *Call) (usecases.InternalListingView, error) {
				return c.S.InternalGetListing(c.Ctx, c.Subj, c.ID("listing_id"))
			}).Internal().
			Describe("Callers: marketplace-service, admin-service. Used before high-value actions and for projection recovery. Never-published listings answer 404."),
		Query("/listings/{listing_id}/cost-preview", "internalListingCostPreview", tagInternal, "Customer-facing all-in cost of a published listing",
			func(c *Call, in *usecases.CostPreviewInput) (usecases.CostPreviewView, error) {
				return c.S.InternalCostPreview(c.Ctx, c.Subj, c.ID("listing_id"), *in)
			}).Internal(),
		Command("POST", "/listings/{listing_id}/reports", "internalReportListing", tagInternal, "Route a marketplace listing report to the owner domain", http.StatusOK,
			func(c *Call, in *usecases.ListingReportInput) (usecases.ReportView, error) {
				return c.S.InternalReportListing(c.Ctx, c.Subj, c.ID("listing_id"), *in)
			}).Internal(),
		Command("POST", "/listings/{listing_id}/moderation", "moderateListing", tagAdmin, "Admin moderation of a listing", http.StatusOK,
			func(c *Call, in *usecases.ModerationInput) (usecases.ListingView, error) {
				return c.S.ModerateListing(c.Ctx, c.Subj, c.ID("listing_id"), *in)
			}).Internal().Errs(409),
		// property-manager-service / business-operations-service (manager actor).
		Get("/manager-assignments/{manager_assignment_id}", "managerGetAssignment", tagInternal, "Manager reads its own mandate",
			func(c *Call) (usecases.AssignmentView, error) {
				return c.S.ManagerGetAssignment(c.Ctx, c.Subj, c.ID("manager_assignment_id"))
			}).Internal(),
		Command("POST", "/manager-assignments/{manager_assignment_id}/respond", "managerRespondAssignment", tagInternal, "Manager accepts or declines a proposed assignment", http.StatusOK,
			func(c *Call, in *usecases.ManagerResponseInput) (usecases.AssignmentView, error) {
				return c.S.ManagerRespond(c.Ctx, c.Subj, c.ID("manager_assignment_id"), *in)
			}).Internal().Errs(409),
		Command("POST", "/manager-assignments/{manager_assignment_id}/activity", "managerRecordActivity", tagInternal, "Record manager activity", http.StatusCreated,
			func(c *Call, in *usecases.ActivityInput) (usecases.ActivityView, error) {
				return c.S.ManagerRecordActivity(c.Ctx, c.Subj, c.ID("manager_assignment_id"), *in)
			}).Internal().Errs(409),
		Get("/managed/properties/{property_id}", "managerGetProperty", tagInternal, "Delegated manager reads a property (property.view)",
			func(c *Call) (usecases.PropertyView, error) {
				return c.S.GetProperty(c.Ctx, c.Subj, c.ID("property_id"))
			}).Internal(),
		Get("/managed/listings/{listing_id}", "managerGetListing", tagInternal, "Delegated manager reads a listing (listing.manage)",
			func(c *Call) (usecases.ListingView, error) { return c.S.GetListing(c.Ctx, c.Subj, c.ID("listing_id")) }).Internal(),
		Command("POST", "/managed/listings/{listing_id}/confirm-availability", "managerConfirmAvailability", tagInternal, "Delegated manager confirms availability (availability.manage)", http.StatusOK,
			func(c *Call, in *usecases.ConfirmAvailabilityInput) (usecases.ListingView, error) {
				return c.S.ConfirmAvailability(c.Ctx, c.Subj, c.ID("listing_id"), *in)
			}).Internal(),
		Command("POST", "/managed/listings/{listing_id}/pause", "managerPauseListing", tagInternal, "Delegated manager pauses a listing (listing.pause)", http.StatusOK,
			func(c *Call, in *usecases.ListingReasonInput) (usecases.ListingView, error) {
				return c.S.PauseListing(c.Ctx, c.Subj, c.ID("listing_id"), *in)
			}).Internal(),
	}
}
