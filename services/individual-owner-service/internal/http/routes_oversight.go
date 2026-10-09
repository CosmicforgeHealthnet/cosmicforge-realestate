package http

import (
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

const (
	tagReports  = "Reports & Insights"
	tagDisputes = "Disputes & Privacy"
)

// disputeEvidence verifies owner evidence for a dispute (no property scope:
// account-level disputes have none).
func disputeEvidence[Req mediaRefs](c *Call, in *Req) (usecases.Evidence, error) {
	return c.S.CheckOwnEvidence(c.Ctx, c.Subj, (*in).MediaRefs(), evidenceClasses...)
}

func oversightRoutes() []Route {
	return []Route{
		List("/properties/{property_id}/announcements", "listAnnouncements", "Announcements", "Announcements to tenants and guests with delivery status",
			func(c *Call, _ Page) (pagination.Response[usecases.AnnouncementView], error) {
				return slice(c.S.ListAnnouncements(c.Ctx, c.Subj, c.ID("property_id")))
			}),
		Command("POST", "/properties/{property_id}/announcements", "createAnnouncement", "Announcements", "Notify current tenants or guests", http.StatusCreated,
			func(c *Call, in *usecases.AnnouncementInput) (usecases.AnnouncementView, error) {
				return c.S.Announce(c.Ctx, c.Subj, c.ID("property_id"), *in)
			}).Describe("Operational purposes only (no marketing), at most 5 per property per day. Recipients are tenants of active tenancies and guests " +
			"of confirmed/current stays of this property. Delivery is requested from the recipients' own domain, which applies their consent and " +
			"preferred channels; until that contract exists requests are recorded and reported as BLOCKED.").Errs(409),
		Get("/dashboard", "getDashboard", tagReports, "Owner dashboard",
			func(c *Call) (usecases.DashboardView, error) { return c.S.Dashboard(c.Ctx, c.Subj) }).
			Describe("Portfolio counts, items awaiting the owner, rent highlights and the next 14 days."),
		Command("PUT", "/dashboard/preferences", "putDashboardPreferences", tagReports, "Set dashboard widgets and default property", http.StatusOK,
			func(c *Call, in *usecases.DashboardPrefs) (usecases.DashboardPrefs, error) {
				return c.S.PutDashboardPreferences(c.Ctx, c.Subj, *in)
			}),
		List("/calendar", "getPortfolioCalendar", tagReports, "Viewings, rent dates, tenancy ends, stays, preventive tasks and confirmations",
			func(c *Call, _ Page) (pagination.Response[usecases.CalendarEntry], error) {
				from, to, err := dateRange(c)
				if err != nil {
					return pagination.Response[usecases.CalendarEntry]{}, err
				}
				return slice(c.S.PortfolioCalendar(c.Ctx, c.Subj, from, to))
			}).Q(rangeParams...),
		Get("/reports/portfolio", "getPortfolioReport", tagReports, "Portfolio performance for a period",
			func(c *Call) (usecases.PortfolioReport, error) {
				pid, err := c.QueryID("property_id")
				if err != nil {
					return usecases.PortfolioReport{}, err
				}
				return c.S.PortfolioReportFor(c.Ctx, c.Subj, pid, c.Query("from"), c.Query("to"))
			}).Q(Param{Name: "property_id"}, Param{Name: "from", Doc: "Default: 90 days before to"}, Param{Name: "to", Doc: "Default: today"}).
			Describe("Occupancy, rent due/collected/arrears, deposits held, short-let ADR/occupancy, maintenance and professional spend, net income. " +
				"Money is payment-service projection per currency."),
		Command("POST", "/reports/export", "exportReport", tagReports, "Generate a PDF report", http.StatusAccepted,
			func(c *Call, in *usecases.ReportExportInput) (usecases.OwnerReportView, error) {
				return c.S.ExportReport(c.Ctx, c.Subj, *in)
			}).
			Describe("Generated asynchronously and stored in media-document-service (503 when it is not configured).").Capture("report_id", "data.id").Errs(503),
		List("/reports", "listReports", tagReports, "Generated reports",
			func(c *Call, _ Page) (pagination.Response[usecases.OwnerReportView], error) {
				return slice(c.S.ListReports(c.Ctx, c.Subj))
			}),
		Get("/reports/{report_id}", "getReport", tagReports, "Report status with a short-lived download link",
			func(c *Call) (usecases.OwnerReportView, error) {
				return c.S.GetReport(c.Ctx, c.Subj, c.ID("report_id"))
			}),
		List("/market-insights", "getMarketInsights", tagReports, "Advisory price and trend insights for a listing",
			func(c *Call, _ Page) (pagination.Response[usecases.InsightView], error) {
				lid, err := c.QueryID("listing_id")
				if err != nil {
					return pagination.Response[usecases.InsightView]{}, err
				}
				return slice(c.S.MarketInsights(c.Ctx, c.Subj, lid))
			}).Q(Param{Name: "listing_id", Doc: "Required"}).
			Describe("Comparable-listing statistics with model, version, inputs and confidence; advisory only, refreshed daily. " +
				"Fewer than 3 comparables answers INSUFFICIENT_DATA.").Errs(422),
		List("/audit/activity", "getAuditActivity", tagReports, "My audit trail",
			func(c *Call, p Page) (pagination.Response[usecases.AuditActivityView], error) {
				return c.S.AuditActivity(c.Ctx, c.Subj, p.Cursor, p.Limit)
			}).Describe("My actions, actions on my account (including privileged operator reads) and on my properties."),
		List("/disputes", "listDisputes", tagDisputes, "My disputes",
			func(c *Call, _ Page) (pagination.Response[usecases.DisputeView], error) {
				return slice(c.S.ListDisputes(c.Ctx, c.Subj, c.Query("status")))
			}).Q(Param{Name: "status", Enum: []string{"OPEN", "UNDER_REVIEW", "AWAITING_INFORMATION", "RESOLVED", "REJECTED", "WITHDRAWN"}}),
		CommandPre("POST", "/disputes", "raiseDispute", tagDisputes, "Raise a dispute", http.StatusCreated, disputeEvidence[usecases.DisputeInput],
			func(c *Call, in *usecases.DisputeInput, ev usecases.Evidence) (usecases.DisputeView, error) {
				return c.S.RaiseDispute(c.Ctx, c.Subj, *in, ev)
			}).Describe("About a record I can act on (or my account/payout accounts). Fraud and safety categories are prioritized for operators.").
			Capture("dispute_id", "data.id").Errs(404, 409),
		Get("/disputes/{dispute_id}", "getDispute", tagDisputes, "Dispute with evidence",
			func(c *Call) (usecases.DisputeView, error) { return c.S.GetDispute(c.Ctx, c.Subj, c.ID("dispute_id")) }),
		CommandPre("POST", "/disputes/{dispute_id}/updates", "updateDispute", tagDisputes, "Add information or withdraw", http.StatusOK, disputeEvidence[usecases.DisputeUpdateInput],
			func(c *Call, in *usecases.DisputeUpdateInput, ev usecases.Evidence) (usecases.DisputeView, error) {
				return c.S.UpdateDispute(c.Ctx, c.Subj, c.ID("dispute_id"), *in, ev)
			}).Errs(409),
		List("/privacy/consents", "listPrivacyConsents", tagDisputes, "Current privacy consents",
			func(c *Call, _ Page) (pagination.Response[usecases.PrivacyConsentView], error) {
				return slice(c.S.ListPrivacyConsents(c.Ctx, c.Subj))
			}),
		Command("POST", "/privacy/consents", "recordPrivacyConsent", tagDisputes, "Grant or withdraw a consent", http.StatusOK,
			func(c *Call, in *usecases.PrivacyConsentInput) (usecases.PrivacyConsentView, error) {
				return c.S.RecordPrivacyConsent(c.Ctx, c.Subj, *in)
			}),
		List("/privacy/requests", "listPrivacyRequests", tagDisputes, "My privacy requests",
			func(c *Call, _ Page) (pagination.Response[usecases.PrivacyRequestView], error) {
				return slice(c.S.ListPrivacyRequests(c.Ctx, c.Subj))
			}),
		Command("POST", "/privacy/requests", "submitPrivacyRequest", tagDisputes, "Access, export, correct, delete, object or restrict", http.StatusCreated,
			func(c *Call, in *usecases.PrivacyRequestInput) (usecases.PrivacyRequestView, error) {
				return c.S.SubmitPrivacyRequest(c.Ctx, c.Subj, *in)
			}).
			Describe("A recent step-up verifies the requester; otherwise verify with POST …/verify after stepping up. Access/export requests are fulfilled with a PDF export.").
			Capture("privacy_request_id", "data.id").Errs(409),
		Get("/privacy/requests/{request_id}", "getPrivacyRequest", tagDisputes, "A privacy request",
			func(c *Call) (usecases.PrivacyRequestView, error) {
				return c.S.GetPrivacyRequest(c.Ctx, c.Subj, c.ID("request_id"))
			}),
		Command("POST", "/privacy/requests/{request_id}/verify", "verifyPrivacyRequest", tagDisputes, "Verify the requester (step-up)", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.PrivacyRequestView, error) {
				return c.S.VerifyPrivacyRequest(c.Ctx, c.Subj, c.ID("request_id"))
			}).Errs(403, 409),
	}
}

func oversightAdminRoutes() []Route {
	return []Route{
		List("/admin/disputes", "adminListDisputes", tagAdmin, "Owner disputes (urgent first)",
			func(c *Call, p Page) (pagination.Response[usecases.DisputeView], error) {
				return c.S.AdminListDisputes(c.Ctx, c.Subj, c.Query("status"), p.Cursor, p.Limit)
			}).Internal().Q(Param{Name: "status"}),
		Get("/admin/disputes/{dispute_id}", "adminGetDispute", tagAdmin, "Dispute with evidence (audited privileged read)",
			func(c *Call) (usecases.DisputeView, error) {
				return c.S.AdminGetDispute(c.Ctx, c.Subj, c.ID("dispute_id"))
			}).Internal(),
		Command("POST", "/admin/disputes/{dispute_id}/status", "adminUpdateDispute", tagAdmin, "Review, request information, resolve or reject", http.StatusOK,
			func(c *Call, in *usecases.AdminDisputeAction) (usecases.DisputeView, error) {
				return c.S.AdminUpdateDispute(c.Ctx, c.Subj, c.ID("dispute_id"), *in)
			}).Internal().Errs(409),
		List("/admin/privacy-requests", "adminListPrivacyRequests", tagAdmin, "Open privacy requests by due date",
			func(c *Call, _ Page) (pagination.Response[usecases.PrivacyRequestView], error) {
				return slice(c.S.AdminListPrivacyRequests(c.Ctx, c.Subj))
			}).Internal(),
		Command("POST", "/admin/privacy-requests/{request_id}/decision", "adminDecidePrivacyRequest", tagAdmin, "Decide a correction/deletion/objection/restriction", http.StatusOK,
			func(c *Call, in *usecases.PrivacyDecision) (usecases.PrivacyRequestView, error) {
				return c.S.AdminDecidePrivacyRequest(c.Ctx, c.Subj, c.ID("request_id"), *in)
			}).Internal().Describe("Records retained categories under legal/business holds.").Errs(409),
	}
}
