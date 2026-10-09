package http

import (
	"net/http"
	"strconv"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

const (
	tagCommercial = "Commercial & Land"
	tagSales      = "Sales"
)

func commercialRoutes() []Route {
	pid := func(c *Call) ids.ID { return c.ID("property_id") }
	return []Route{
		Get("/properties/{property_id}/commercial-terms", "getCommercialTerms", tagCommercial, "Current commercial terms",
			func(c *Call) (usecases.CommercialTermsView, error) {
				uid, err := c.QueryID("unit_id")
				if err != nil {
					return usecases.CommercialTermsView{}, err
				}
				return c.S.GetCommercialTerms(c.Ctx, c.Subj, pid(c), uid)
			}).Q(Param{Name: "unit_id"}),
		Update("PUT", "/properties/{property_id}/commercial-terms", "putCommercialTerms", tagCommercial, "Record a new commercial-terms version (If-Match; \"0\" for the first)", true,
			func(c *Call, in *usecases.CommercialTermsInput) (usecases.CommercialTermsView, error) {
				return c.S.PutCommercialTerms(c.Ctx, c.Subj, pid(c), c.Version, *in)
			}).Describe("Zoning/use/facilities, fit-out, rent-free, responsibilities and an auditable obligation calendar. Versions are append-only.").Errs(409, 422),
		List("/properties/{property_id}/commercial-terms/calendar", "getCommercialCalendar", tagCommercial, "Upcoming commercial obligations",
			func(c *Call, _ Page) (pagination.Response[usecases.ObligationDate], error) {
				uid, err := c.QueryID("unit_id")
				if err != nil {
					return pagination.Response[usecases.ObligationDate]{}, err
				}
				months, _ := strconv.Atoi(c.Query("months"))
				return slice(c.S.CommercialCalendar(c.Ctx, c.Subj, pid(c), uid, months))
			}).Q(Param{Name: "unit_id"}, Param{Name: "months", Doc: "Horizon in months (1-60, default 12)"}),
		Get("/properties/{property_id}/land-terms", "getLandTerms", tagCommercial, "Current land terms",
			func(c *Call) (usecases.LandTermsView, error) {
				parcel, err := c.QueryID("parcel_id")
				if err != nil {
					return usecases.LandTermsView{}, err
				}
				return c.S.GetLandTerms(c.Ctx, c.Subj, pid(c), parcel)
			}).Q(Param{Name: "parcel_id"}),
		Update("PUT", "/properties/{property_id}/land-terms", "putLandTerms", tagCommercial, "Record a new land-terms version (If-Match; \"0\" for the first)", true,
			func(c *Call, in *usecases.LandTermsInput) (usecases.LandTermsView, error) {
				ev, err := c.S.CheckMediaEvidence(c.Ctx, c.Subj, usecases.ScopeProperty, pid(c), authz.Do(authz.CapListingManage), in.MediaRefs(), scanClasses...)
				if err != nil {
					return usecases.LandTermsView{}, err
				}
				return c.S.PutLandTerms(c.Ctx, c.Subj, pid(c), c.Version, *in, ev)
			}).Describe("Survey/title evidence stays in media-document-service; versions are append-only.").Errs(409, 422),
		List("/commercial-lois", "listCommercialLOIs", tagCommercial, "Letters of intent across my properties",
			func(c *Call, _ Page) (pagination.Response[usecases.LOIView], error) {
				p, err := c.QueryID("property_id")
				if err != nil {
					return pagination.Response[usecases.LOIView]{}, err
				}
				return slice(c.S.ListLOIs(c.Ctx, c.Subj, p, c.Query("status")))
			}).Q(Param{Name: "property_id"}, Param{Name: "status", Enum: []string{"SUBMITTED", "COUNTERED", "ACCEPTED", "DECLINED", "WITHDRAWN", "EXPIRED", "CONVERTED"}}),
		Get("/commercial-lois/{loi_id}", "getCommercialLOI", tagCommercial, "Letter of intent with its immutable rounds",
			func(c *Call) (usecases.LOIView, error) { return c.S.GetLOI(c.Ctx, c.Subj, c.ID("loi_id")) }),
		Command("POST", "/commercial-lois/{loi_id}/response", "respondCommercialLOI", tagCommercial, "Accept, counter or decline an LOI", http.StatusOK,
			func(c *Call, in *usecases.LOIResponse) (usecases.LOIView, error) {
				return c.S.RespondLOI(c.Ctx, c.Subj, c.ID("loi_id"), *in)
			}).Describe("Acceptance converts the LOI into a COMMERCIAL_LEASE agreement draft with typed commercial terms and reserves the listing.").Errs(409, 422),
	}
}

func saleRoutes() []Route {
	sid := func(c *Call) ids.ID { return c.ID("sale_id") }
	manage := authz.Do(authz.CapSaleManage)
	return []Route{
		List("/transactions/sales", "listSales", tagSales, "Sale transactions across my properties",
			func(c *Call, p Page) (pagination.Response[usecases.SaleView], error) {
				prop, err := c.QueryID("property_id")
				if err != nil {
					return pagination.Response[usecases.SaleView]{}, err
				}
				return c.S.ListSales(c.Ctx, c.Subj, prop, c.Query("status"), p.Cursor, p.Limit)
			}).Q(Param{Name: "property_id"}, Param{Name: "status"}),
		Command("POST", "/transactions/sales", "createSale", tagSales, "Open a sale transaction", http.StatusCreated,
			func(c *Call, in *usecases.SaleInput) (usecases.SaleView, error) {
				return c.S.CreateSale(c.Ctx, c.Subj, *in)
			}).
			Describe("Owner-only authority with verified ownership; buyers' offers also open one automatically.").Capture("sale_id", "data.id").Errs(409),
		Get("/transactions/sales/{sale_id}", "getSale", tagSales, "Sale with offers, milestones and due diligence",
			func(c *Call) (usecases.SaleView, error) { return c.S.GetSale(c.Ctx, c.Subj, sid(c)) }),
		Command("POST", "/transactions/sales/{sale_id}/offers/{offer_id}/decision", "decideSaleOffer", tagSales, "Accept, counter or decline an offer", http.StatusOK,
			func(c *Call, in *usecases.OfferDecisionInput) (usecases.SaleView, error) {
				return c.S.DecideSaleOffer(c.Ctx, c.Subj, sid(c), c.ID("offer_id"), *in)
			}).
			Describe("Accepting or countering binds the owner: identity policy and co-owner SALE consent apply. Acceptance creates the configured closing checklist; PLATFORM settlement needs payment-service (503).").
			Errs(409, 422, 503),
		Command("POST", "/transactions/sales/{sale_id}/advance", "advanceSale", tagSales, "Move the sale to its next stage", http.StatusOK,
			func(c *Call, in *usecases.AdvanceSaleInput) (usecases.SaleView, error) {
				return c.S.AdvanceSale(c.Ctx, c.Subj, sid(c), *in)
			}).
			Describe("Contracting needs no unresolved blocking diligence; closing needs the documentary/consent milestones; completion needs every required milestone and marks the listing SOLD.").
			Errs(409, 422),
		Command("POST", "/transactions/sales/{sale_id}/end", "endSale", tagSales, "Cancel, fail, dispute or resume a sale", http.StatusOK,
			func(c *Call, in *usecases.EndSaleInput) (usecases.SaleView, error) {
				return c.S.EndSale(c.Ctx, c.Subj, sid(c), *in)
			}).Errs(409),
		CommandPre("POST", "/transactions/sales/{sale_id}/milestones", "recordSaleMilestone", tagSales, "Record milestone evidence, waive or add a milestone", http.StatusOK,
			ownerEvidence[usecases.MilestoneInput](usecases.ScopeSale, "sale_id", manage, scanClasses),
			func(c *Call, in *usecases.MilestoneInput, ev usecases.Evidence) (usecases.SaleView, error) {
				return c.S.RecordMilestone(c.Ctx, c.Subj, sid(c), *in, ev)
			}).Describe("Platform payments are confirmed only by payment-service; off-platform payments need the buyer's confirmation; registration/perfection needs evidence.").
			Errs(409, 422),
		CommandPre("POST", "/transactions/sales/{sale_id}/due-diligence", "addDueDiligenceItem", tagSales, "Raise a due-diligence item", http.StatusCreated,
			ownerEvidence[usecases.DiligenceInput](usecases.ScopeSale, "sale_id", manage, evidenceClasses),
			func(c *Call, in *usecases.DiligenceInput, ev usecases.Evidence) (usecases.DiligenceView, error) {
				return c.S.AddDiligenceItem(c.Ctx, c.Subj, sid(c), *in, ev)
			}).Capture("diligence_item_id", "data.id").Capture("diligence_version", "data.version"),
		Update("PATCH", "/transactions/sales/{sale_id}/due-diligence/{item_id}", "updateDueDiligenceItem", tagSales, "Progress a due-diligence item (If-Match)", false,
			func(c *Call, in *usecases.DiligenceUpdate) (usecases.DiligenceView, error) {
				ev, err := c.S.CheckMediaEvidence(c.Ctx, c.Subj, usecases.ScopeSale, sid(c), manage, in.MediaRefs(), evidenceClasses...)
				if err != nil {
					return usecases.DiligenceView{}, err
				}
				return c.S.UpdateDiligenceItem(c.Ctx, c.Subj, sid(c), c.ID("item_id"), c.Version, *in, ev)
			}),
		Get("/transactions/sales/{sale_id}/data-room", "getDataRoom", tagSales, "Data room with documents and grants",
			func(c *Call) (usecases.DataRoomView, error) { return c.S.GetDataRoom(c.Ctx, c.Subj, sid(c)) }),
		Command("POST", "/transactions/sales/{sale_id}/data-room", "openDataRoom", tagSales, "Open the sale's data room", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.DataRoomView, error) { return c.S.OpenDataRoom(c.Ctx, c.Subj, sid(c)) }).Capture("data_room_id", "data.id"),
		CommandPre("POST", "/transactions/sales/{sale_id}/data-room/documents", "addDataRoomDocument", tagSales, "Add a document", http.StatusOK,
			ownerEvidence[usecases.DataRoomDocumentInput](usecases.ScopeSale, "sale_id", manage, scanClasses),
			func(c *Call, in *usecases.DataRoomDocumentInput, ev usecases.Evidence) (usecases.DataRoomView, error) {
				return c.S.AddDataRoomDocument(c.Ctx, c.Subj, sid(c), *in, ev)
			}).Errs(409),
		Command("DELETE", "/transactions/sales/{sale_id}/data-room/documents/{document_id}", "removeDataRoomDocument", tagSales, "Remove a document", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.DataRoomView, error) {
				return c.S.RemoveDataRoomDocument(c.Ctx, c.Subj, sid(c), c.ID("document_id"))
			}),
		Command("POST", "/transactions/sales/{sale_id}/data-room/grants", "grantDataRoomAccess", tagSales, "Grant time/purpose-limited access", http.StatusOK,
			func(c *Call, in *usecases.DataRoomGrantInput) (usecases.DataRoomView, error) {
				return c.S.GrantDataRoomAccess(c.Ctx, c.Subj, sid(c), *in)
			}).Describe("Least privilege: by category, purpose and expiry (≤ 30 days); every download is logged."),
		Command("POST", "/transactions/sales/{sale_id}/data-room/grants/{grant_id}/revoke", "revokeDataRoomGrant", tagSales, "Revoke a grant", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.DataRoomView, error) {
				return c.S.RevokeDataRoomGrant(c.Ctx, c.Subj, sid(c), c.ID("grant_id"))
			}),
	}
}

// commercialSaleInternalRoutes are marketplace and professional contracts.
func commercialSaleInternalRoutes() []Route {
	return []Route{
		Command("POST", "/commercial-lois", "customerSubmitLOI", tagInternal, "Business applicant submits an LOI", http.StatusCreated,
			func(c *Call, in *usecases.CustomerLOIInput) (usecases.LOIView, error) {
				return c.S.CustomerSubmitLOI(c.Ctx, c.Subj, *in)
			}).
			Internal().Describe("Corporate verification stays with its domain: only business_verification_ref is stored.").Errs(409),
		Get("/commercial-lois/{loi_id}/customer-view", "customerGetLOI", tagInternal, "LOI as seen by the applicant",
			func(c *Call) (usecases.LOIView, error) { return c.S.CustomerGetLOI(c.Ctx, c.Subj, c.ID("loi_id")) }).Internal(),
		Command("POST", "/commercial-lois/{loi_id}/customer-response", "customerRespondLOI", tagInternal, "Applicant accepts, counters, declines or withdraws", http.StatusOK,
			func(c *Call, in *usecases.LOIResponse) (usecases.LOIView, error) {
				return c.S.CustomerRespondLOI(c.Ctx, c.Subj, c.ID("loi_id"), *in)
			}).Internal().Errs(409),
		Command("POST", "/sale-offers", "customerMakeSaleOffer", tagInternal, "Buyer makes an offer on a SALE listing", http.StatusCreated,
			func(c *Call, in *usecases.CustomerSaleOfferInput) (usecases.SaleOfferView, error) {
				return c.S.CustomerMakeSaleOffer(c.Ctx, c.Subj, *in)
			}).Internal().Describe("Proof of funds is referenced, never copied.").Capture("sale_offer_id", "data.id").Errs(409),
		Command("POST", "/sale-offers/{offer_id}/customer-response", "customerRespondSaleOffer", tagInternal, "Buyer answers a counter-offer or withdraws", http.StatusOK,
			func(c *Call, in *usecases.CustomerSaleOfferResponse) (usecases.SaleOfferView, error) {
				return c.S.CustomerRespondSaleOffer(c.Ctx, c.Subj, c.ID("offer_id"), *in)
			}).Internal().Errs(409),
		Get("/transactions/sales/{sale_id}/customer-view", "customerGetSale", tagInternal, "Sale as seen by the accepted buyer",
			func(c *Call) (usecases.SaleView, error) { return c.S.CustomerGetSale(c.Ctx, c.Subj, c.ID("sale_id")) }).Internal(),
		Command("POST", "/transactions/sales/{sale_id}/customer-action", "customerSaleAction", tagInternal, "Buyer confirms a payment, withdraws or disputes", http.StatusOK,
			func(c *Call, in *usecases.CustomerSaleAction) (usecases.SaleView, error) {
				return c.S.CustomerActOnSale(c.Ctx, c.Subj, c.ID("sale_id"), *in)
			}).Internal().Errs(409),
		CommandPre("POST", "/transactions/sales/{sale_id}/customer-due-diligence", "customerRaiseDueDiligence", tagInternal, "Buyer raises a due-diligence item", http.StatusCreated,
			customerEvidence[usecases.DiligenceInput](evidenceClasses),
			func(c *Call, in *usecases.DiligenceInput, ev usecases.Evidence) (usecases.DiligenceView, error) {
				return c.S.CustomerRaiseDiligence(c.Ctx, c.Subj, c.ID("sale_id"), *in, ev)
			}).Internal(),
		Get("/data-rooms/{data_room_id}", "granteeListDataRoom", tagInternal, "Documents within the grantee's scope",
			func(c *Call) (usecases.DataRoomView, error) {
				return c.S.GranteeListDataRoom(c.Ctx, c.Subj, c.ID("data_room_id"))
			}).
			Internal().Describe("Callers: marketplace-service (buyer actor) or professional-services-service (professional actor) holding an active grant."),
		Query("/data-rooms/{data_room_id}/documents/{document_id}/download-url", "granteeDataRoomDownloadUrl", tagInternal, "Short-lived, audited download URL",
			func(c *Call, _ *Empty) (usecases.DataRoomDownload, error) {
				return c.S.GranteeDownloadURL(c.Ctx, c.Subj, c.ID("data_room_id"), c.ID("document_id"))
			}).Internal().Errs(503),
	}
}
