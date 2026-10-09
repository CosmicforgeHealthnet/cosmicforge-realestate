package http

import (
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

const (
	tagLeads        = "Leads"
	tagViewings     = "Viewings"
	tagApplications = "Applications"
)

func listingParam(c *Call) (ids.ID, error) { return c.QueryID("listing_id") }

func leadRoutes() []Route {
	iid := func(c *Call) ids.ID { return c.ID("inquiry_id") }
	return []Route{
		List("/inquiries", "listInquiries", tagLeads, "Lead pipeline across my properties",
			func(c *Call, p Page) (pagination.Response[usecases.InquiryView], error) {
				lid, err := listingParam(c)
				if err != nil {
					return pagination.Response[usecases.InquiryView]{}, err
				}
				return c.S.ListInquiries(c.Ctx, c.Subj, lid, c.Query("status"), p.Cursor, p.Limit)
			}).Q(Param{Name: "listing_id"}, Param{Name: "status", Enum: []string{"NEW", "ACKNOWLEDGED", "QUALIFIED", "VIEWING", "APPLICATION", "OFFER", "BOOKING", "CLOSED_WON", "CLOSED_LOST"}}),
		Get("/inquiries/{inquiry_id}", "getInquiry", tagLeads, "Inquiry with its activity history",
			func(c *Call) (usecases.InquiryView, error) { return c.S.GetInquiry(c.Ctx, c.Subj, iid(c)) }),
		Update("PATCH", "/inquiries/{inquiry_id}", "updateInquiry", tagLeads, "Priority, follow-up reminder or owner note (If-Match)", false,
			func(c *Call, in *usecases.InquiryPatch) (usecases.InquiryView, error) {
				return c.S.UpdateInquiry(c.Ctx, c.Subj, iid(c), c.Version, *in)
			}),
		Command("POST", "/inquiries/{inquiry_id}/acknowledge", "acknowledgeInquiry", tagLeads, "Acknowledge an inquiry", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.InquiryView, error) {
				return c.S.AcknowledgeInquiry(c.Ctx, c.Subj, iid(c))
			}),
		Command("POST", "/inquiries/{inquiry_id}/qualify", "qualifyInquiry", tagLeads, "Qualify (or close) a lead with lawful fields", http.StatusOK,
			func(c *Call, in *usecases.QualifyInput) (usecases.InquiryView, error) {
				return c.S.QualifyInquiry(c.Ctx, c.Subj, iid(c), *in)
			}).
			Describe("Only a fixed set of lawful qualification fields and closing reasons are accepted (no owner-defined criteria)."),
		Command("POST", "/inquiries/{inquiry_id}/close", "closeInquiry", tagLeads, "Close a lead as won or lost", http.StatusOK,
			func(c *Call, in *usecases.CloseInquiryInput) (usecases.InquiryView, error) {
				return c.S.CloseInquiry(c.Ctx, c.Subj, iid(c), *in)
			}),
		List("/campaigns", "listCampaigns", tagLeads, "Listing campaigns and promotion",
			func(c *Call, _ Page) (pagination.Response[usecases.InquiryView], error) {
				return slice(c.S.Campaigns(c.Ctx, c.Subj))
			}).
			Describe("Promotion references marketplace commercial capability (and payment-service for paid promotion), which have no implemented contract: 503 FEATURE_UNAVAILABLE.").Errs(503),
	}
}

func viewingRoutes() []Route {
	vid := func(c *Call) ids.ID { return c.ID("viewing_id") }
	return []Route{
		List("/viewing-slots", "listViewingSlots", tagViewings, "Upcoming viewing slots of a listing",
			func(c *Call, _ Page) (pagination.Response[usecases.SlotView], error) {
				lid, err := listingParam(c)
				if err != nil {
					return pagination.Response[usecases.SlotView]{}, err
				}
				return slice(c.S.ListSlots(c.Ctx, c.Subj, lid))
			}).Q(Param{Name: "listing_id", Required: true}),
		Command("POST", "/viewing-slots", "createViewingSlot", tagViewings, "Open a viewing slot", http.StatusCreated,
			func(c *Call, in *usecases.SlotInput) (usecases.SlotView, error) {
				return c.S.CreateSlot(c.Ctx, c.Subj, *in)
			}).
			Capture("viewing_slot_id", "data.id").Errs(409),
		Command("POST", "/viewing-slots/{viewing_slot_id}/cancel", "cancelViewingSlot", tagViewings, "Cancel a slot and its bookings", http.StatusOK,
			func(c *Call, in *usecases.ReasonCodeInput) (usecases.SlotView, error) {
				return c.S.CancelSlot(c.Ctx, c.Subj, c.ID("viewing_slot_id"), *in)
			}),
		List("/viewings", "listViewings", tagViewings, "Viewings across my properties",
			func(c *Call, p Page) (pagination.Response[usecases.ViewingView], error) {
				lid, err := listingParam(c)
				if err != nil {
					return pagination.Response[usecases.ViewingView]{}, err
				}
				return c.S.ListViewings(c.Ctx, c.Subj, lid, c.Query("status"), p.Cursor, p.Limit)
			}).Q(Param{Name: "listing_id"}, Param{Name: "status", Enum: []string{"REQUESTED", "CONFIRMED", "CHECKED_IN", "COMPLETED", "RESCHEDULED", "CANCELLED", "NO_SHOW"}}),
		Get("/viewings/{viewing_id}", "getViewing", tagViewings, "Get a viewing",
			func(c *Call) (usecases.ViewingView, error) { return c.S.GetViewing(c.Ctx, c.Subj, vid(c)) }),
		Update("PATCH", "/viewings/{viewing_id}", "updateViewing", tagViewings, "Owner notes (If-Match)", false,
			func(c *Call, in *usecases.ViewingNotesInput) (usecases.ViewingView, error) {
				return c.S.UpdateViewingNotes(c.Ctx, c.Subj, vid(c), c.Version, *in)
			}),
		Command("POST", "/viewings/{viewing_id}/approve", "approveViewing", tagViewings, "Confirm a requested viewing", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.ViewingView, error) {
				return c.S.ApproveViewing(c.Ctx, c.Subj, vid(c))
			}).
			Describe("Physical viewings receive a one-time access code (returned once). Occupied properties need occupant consent first.").Errs(409),
		Command("POST", "/viewings/{viewing_id}/reschedule", "rescheduleViewing", tagViewings, "Propose a new time or slot", http.StatusOK,
			func(c *Call, in *usecases.RescheduleInput) (usecases.ViewingView, error) {
				return c.S.RescheduleViewing(c.Ctx, c.Subj, vid(c), *in)
			}).Errs(409),
		Command("POST", "/viewings/{viewing_id}/cancel", "cancelViewing", tagViewings, "Cancel a viewing", http.StatusOK,
			func(c *Call, in *usecases.CancelViewingInput) (usecases.ViewingView, error) {
				return c.S.CancelViewing(c.Ctx, c.Subj, vid(c), *in)
			}),
		Command("POST", "/viewings/{viewing_id}/representative", "assignViewingRepresentative", tagViewings, "Assign who conducts the viewing", http.StatusOK,
			func(c *Call, in *usecases.RepresentativeInput) (usecases.ViewingView, error) {
				return c.S.AssignViewingHost(c.Ctx, c.Subj, vid(c), *in)
			}).Describe("A representative or manager must hold viewings.manage for this property for the whole viewing.").Errs(422),
		Command("POST", "/viewings/{viewing_id}/occupant-consent", "recordOccupantConsent", tagViewings, "Record occupant consent (occupied property)", http.StatusOK,
			func(c *Call, in *usecases.OccupantConsentInput) (usecases.ViewingView, error) {
				return c.S.RecordOccupantConsent(c.Ctx, c.Subj, vid(c), *in)
			}),
		Command("POST", "/viewings/{viewing_id}/check-in", "checkInViewing", tagViewings, "Record arrival (verifies the access code)", http.StatusOK,
			func(c *Call, in *usecases.CheckInInput) (usecases.ViewingView, error) {
				return c.S.CheckInViewing(c.Ctx, c.Subj, vid(c), *in)
			}).Errs(422),
		Command("POST", "/viewings/{viewing_id}/outcome", "recordViewingOutcome", tagViewings, "Complete a viewing or record a no-show", http.StatusOK,
			func(c *Call, in *usecases.ViewingOutcomeInput) (usecases.ViewingView, error) {
				return c.S.RecordViewingOutcome(c.Ctx, c.Subj, vid(c), *in)
			}),
	}
}

func applicationRoutes() []Route {
	aid := func(c *Call) ids.ID { return c.ID("application_id") }
	oid := func(c *Call) ids.ID { return c.ID("offer_id") }
	return []Route{
		List("/properties/{property_id}/screening-policies", "listScreeningPolicies", tagApplications, "Screening policy versions",
			func(c *Call, _ Page) (pagination.Response[usecases.ScreeningPolicyView], error) {
				return slice(c.S.ListScreeningPolicies(c.Ctx, c.Subj, c.ID("property_id")))
			}),
		Command("POST", "/properties/{property_id}/screening-policies", "createScreeningPolicy", tagApplications, "New screening policy version (configured criteria only)", http.StatusCreated,
			func(c *Call, in *usecases.ScreeningPolicyInput) (usecases.ScreeningPolicyView, error) {
				return c.S.CreateScreeningPolicy(c.Ctx, c.Subj, c.ID("property_id"), *in)
			}),
		List("/applications", "listApplications", tagApplications, "Applications across my properties",
			func(c *Call, p Page) (pagination.Response[usecases.ApplicationView], error) {
				lid, err := listingParam(c)
				if err != nil {
					return pagination.Response[usecases.ApplicationView]{}, err
				}
				return c.S.ListApplications(c.Ctx, c.Subj, lid, c.Query("status"), p.Cursor, p.Limit)
			}).Q(Param{Name: "listing_id"}, Param{Name: "status"}),
		Get("/applications/{application_id}", "getApplication", tagApplications, "Full application for review",
			func(c *Call) (usecases.ApplicationView, error) { return c.S.GetApplication(c.Ctx, c.Subj, aid(c)) }),
		Command("POST", "/applications/{application_id}/review", "startApplicationReview", tagApplications, "Start (or resume) review; appeals go to human review", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.ApplicationView, error) {
				return c.S.StartReview(c.Ctx, c.Subj, aid(c))
			}),
		Command("POST", "/applications/{application_id}/request-info", "requestApplicationInfo", tagApplications, "Request evidence (alternative evidence accepted)", http.StatusOK,
			func(c *Call, in *usecases.RequestInfoInput) (usecases.ApplicationView, error) {
				return c.S.RequestInfo(c.Ctx, c.Subj, aid(c), *in)
			}),
		Command("POST", "/applications/{application_id}/screening", "recordScreening", tagApplications, "Record screening assessments", http.StatusOK,
			func(c *Call, in *usecases.ScreeningInput) (usecases.ApplicationView, error) {
				return c.S.RecordScreening(c.Ctx, c.Subj, aid(c), *in)
			}),
		Command("POST", "/applications/{application_id}/decision", "decideApplication", tagApplications, "Record the human decision", http.StatusOK,
			func(c *Call, in *usecases.DecisionInput) (usecases.ApplicationView, error) {
				return c.S.DecideApplication(c.Ctx, c.Subj, aid(c), *in)
			}).
			Describe("Automated screening flags never decide. Declines need configured lawful reason codes; the screening policy version in force is recorded.").Errs(422),
		Query("/applications/{application_id}/evidence/{grant_id}/download-url", "getEvidenceDownloadUrl", tagApplications, "Short-lived evidence URL (purpose-limited, audited)",
			func(c *Call, _ *Empty) (usecases.EvidenceDownload, error) {
				return c.S.EvidenceDownloadURL(c.Ctx, c.Subj, aid(c), c.ID("grant_id"))
			}).Errs(409, 503),
		Command("POST", "/applications/{application_id}/offer", "createRentalOffer", tagApplications, "Draft a rental offer", http.StatusCreated,
			func(c *Call, in *usecases.OfferInput) (usecases.OfferView, error) {
				return c.S.CreateOffer(c.Ctx, c.Subj, aid(c), *in)
			}).
			Capture("offer_id", "data.id").Capture("offer_version", "data.version").Errs(409, 422),
		Get("/offers/{offer_id}", "getRentalOffer", tagApplications, "Get a rental offer",
			func(c *Call) (usecases.OfferView, error) { return c.S.GetOffer(c.Ctx, c.Subj, oid(c)) }),
		Update("PATCH", "/offers/{offer_id}", "updateRentalOffer", tagApplications, "Edit a draft offer (re-priced; If-Match)", false,
			func(c *Call, in *usecases.OfferInput) (usecases.OfferView, error) {
				return c.S.UpdateOffer(c.Ctx, c.Subj, oid(c), c.Version, *in)
			}),
		Command("POST", "/offers/{offer_id}/send", "sendRentalOffer", tagApplications, "Send the offer to the applicant", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.OfferView, error) { return c.S.SendOffer(c.Ctx, c.Subj, oid(c)) }).Errs(409),
		Command("POST", "/offers/{offer_id}/withdraw", "withdrawRentalOffer", tagApplications, "Withdraw an offer", http.StatusOK,
			func(c *Call, in *usecases.ReasonCodeInput) (usecases.OfferView, error) {
				return c.S.WithdrawOffer(c.Ctx, c.Subj, oid(c), *in)
			}),
	}
}

// customerRoutes are marketplace-service calls on behalf of a customer actor.
func customerRoutes() []Route {
	return []Route{
		Command("POST", "/inquiries", "customerCreateInquiry", tagInternal, "Marketplace inquiry for a published listing", http.StatusCreated,
			func(c *Call, in *usecases.CustomerInquiryInput) (usecases.InquiryView, error) {
				return c.S.CustomerCreateInquiry(c.Ctx, c.Subj, *in)
			}).Internal().
			Describe("Caller: marketplace-service with a delegated marketplace customer actor. Message content stays in CF Meet."),
		Command("POST", "/viewings", "customerRequestViewing", tagInternal, "Marketplace viewing request", http.StatusCreated,
			func(c *Call, in *usecases.CustomerViewingInput) (usecases.ViewingView, error) {
				return c.S.CustomerRequestViewing(c.Ctx, c.Subj, *in)
			}).Internal(),
		Command("POST", "/viewings/{viewing_id}/customer-response", "customerRespondViewing", tagInternal, "Customer accepts a reschedule or cancels", http.StatusOK,
			func(c *Call, in *usecases.CustomerViewingResponse) (usecases.CustomerViewingAccess, error) {
				return c.S.CustomerRespondViewing(c.Ctx, c.Subj, c.ID("viewing_id"), *in)
			}).Internal(),
		Get("/viewings/{viewing_id}/access", "customerViewingAccess", tagInternal, "Customer's appointment and access code",
			func(c *Call) (usecases.CustomerViewingAccess, error) {
				return c.S.CustomerGetViewingAccess(c.Ctx, c.Subj, c.ID("viewing_id"))
			}).Internal(),
		Command("POST", "/applications", "customerSubmitApplication", tagInternal, "Marketplace rental application", http.StatusCreated,
			func(c *Call, in *usecases.CustomerApplicationInput) (usecases.ApplicationView, error) {
				return c.S.CustomerSubmitApplication(c.Ctx, c.Subj, *in)
			}).Internal(),
		Command("POST", "/applications/{application_id}/evidence", "customerProvideEvidence", tagInternal, "Customer grants time-limited evidence", http.StatusCreated,
			func(c *Call, in *usecases.CustomerEvidenceInput) (usecases.EvidenceGrantView, error) {
				return c.S.CustomerProvideEvidence(c.Ctx, c.Subj, c.ID("application_id"), *in)
			}).Internal(),
		Command("POST", "/applications/{application_id}/customer-action", "customerApplicationAction", tagInternal, "Customer withdraws or appeals", http.StatusOK,
			func(c *Call, in *usecases.CustomerApplicationAction) (usecases.ApplicationEvent, error) {
				return c.S.CustomerActOnApplication(c.Ctx, c.Subj, c.ID("application_id"), *in)
			}).Internal(),
		Command("POST", "/offers/{offer_id}/response", "customerRespondOffer", tagInternal, "Customer accepts or declines a rental offer", http.StatusOK,
			func(c *Call, in *usecases.CustomerOfferResponse) (usecases.OfferView, error) {
				return c.S.CustomerRespondOffer(c.Ctx, c.Subj, c.ID("offer_id"), *in)
			}).Internal().Describe("Acceptance freezes the pricing version and moves the listing to OFFER_ACCEPTED; competing acceptances are serialized.").Errs(409),
	}
}
