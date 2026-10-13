package http

import (
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

const tagAssistance = "Assisted Use"

// Section 13 closure routes: statutory/tax calendar, valuations and
// portfolio insights (§13.25) and assisted use (§13.27).
func closureRoutes() []Route {
	return []Route{
		List("/statutory-obligations", "listStatutoryObligations", tagReports, "My tax and statutory calendar",
			func(c *Call, _ Page) (pagination.Response[usecases.StatutoryView], error) {
				pid, err := c.QueryID("property_id")
				if err != nil {
					return pagination.Response[usecases.StatutoryView]{}, err
				}
				return slice(c.S.ListStatutoryObligations(c.Ctx, c.Subj, pid, c.Query("status")))
			}).Q(Param{Name: "property_id"}, Param{Name: "status", Enum: []string{"UPCOMING", "COMPLETED", "CANCELLED"}}),
		Command("POST", "/statutory-obligations", "createStatutoryObligation", tagReports, "Add a tax or statutory date", http.StatusCreated,
			func(c *Call, in *usecases.StatutoryInput) (usecases.StatutoryView, error) {
				return c.S.CreateStatutoryObligation(c.Ctx, c.Subj, *in)
			}).Describe("The owner's own calendar of filings, rates, ground rent and licences with a reminder before the due date. Amounts are the "+
			"owner's informational estimates: the platform computes no tax and collects nothing here.").Capture("statutory_id", "data.id"),
		CommandPre("POST", "/statutory-obligations/{statutory_id}/close", "closeStatutoryObligation", tagReports, "Complete (with receipts) or cancel", http.StatusOK,
			func(c *Call, in *usecases.StatutoryCloseInput) (usecases.Evidence, error) {
				return c.S.CheckOwnEvidence(c.Ctx, c.Subj, in.MediaRefs(), scanClasses...)
			},
			func(c *Call, in *usecases.StatutoryCloseInput, ev usecases.Evidence) (usecases.StatutoryView, error) {
				return c.S.CloseStatutoryObligation(c.Ctx, c.Subj, c.ID("statutory_id"), *in, ev)
			}).Describe("Completing a recurring entry schedules its next occurrence.").Errs(409),
		List("/properties/{property_id}/valuations", "listPropertyValuations", tagReports, "Valuations (latest first)",
			func(c *Call, _ Page) (pagination.Response[usecases.ValuationView], error) {
				return slice(c.S.ListValuations(c.Ctx, c.Subj, c.ID("property_id")))
			}),
		CommandPre("POST", "/properties/{property_id}/valuations", "recordPropertyValuation", tagReports, "Record a valuation", http.StatusCreated,
			ownerEvidence[usecases.ValuationInput](usecases.ScopeProperty, "property_id", authz.Do(authz.CapPropertyEdit), scanClasses),
			func(c *Call, in *usecases.ValuationInput, ev usecases.Evidence) (usecases.ValuationView, error) {
				return c.S.RecordValuation(c.Ctx, c.Subj, c.ID("property_id"), *in, ev)
			}).Describe("Append-only. The latest valuation is the disclosed basis of gross yield in portfolio reports; a professional valuation " +
			"references an appointment on the property with a submitted report.").Errs(422),
		List("/properties/{property_id}/insights", "getPropertyInsights", tagReports, "Advisory vacancy, renewal and maintenance insights",
			func(c *Call, _ Page) (pagination.Response[usecases.InsightView], error) {
				return slice(c.S.PropertyInsights(c.Ctx, c.Subj, c.ID("property_id")))
			}).Describe("Deterministic rules over this property's records with model, version, inputs, confidence and expiry (refreshed daily). " +
			"Advisory only: no external AI or market provider is used and nothing is decided for the owner."),
		List("/assistance/authorizations", "listAssistanceAuthorizations", tagAssistance, "Support assistance I authorized",
			func(c *Call, _ Page) (pagination.Response[usecases.AssistanceView], error) {
				return slice(c.S.ListAssistance(c.Ctx, c.Subj))
			}),
		Command("POST", "/assistance/authorizations", "grantAssistance", tagAssistance, "Authorize support staff to assist (step-up)", http.StatusCreated,
			func(c *Call, in *usecases.AssistanceInput) (usecases.AssistanceView, error) {
				return c.S.GrantAssistance(c.Ctx, c.Subj, *in)
			}).Describe("Concierge onboarding, assisted photography/scanning, accessibility support and remote help for named scopes, optional "+
			"properties and at most 30 days. Support acts as named staff through admin-service, never with my session; every action is recorded "+
			"and notified.").Capture("assistance_id", "data.id").Errs(403),
		Command("POST", "/assistance/authorizations/{assistance_id}/revoke", "revokeAssistance", tagAssistance, "Revoke an authorization now", http.StatusOK,
			func(c *Call, in *usecases.AssistanceRevokeInput) (usecases.AssistanceView, error) {
				return c.S.RevokeAssistance(c.Ctx, c.Subj, c.ID("assistance_id"), *in)
			}).Errs(409),
		List("/assistance/activity", "listAssistedActivity", tagAssistance, "What support did on my account",
			func(c *Call, _ Page) (pagination.Response[usecases.AssistedActivityView], error) {
				return slice(c.S.ListAssistedActivity(c.Ctx, c.Subj))
			}),
	}
}

func closureAdminRoutes() []Route {
	return []Route{
		Get("/admin/reconciliation", "adminGetReconciliation", tagAdmin, "Classified reference, delivery and original-operation recovery issues",
			func(c *Call) ([]usecases.ReconciliationIssue, error) { return c.S.ReconciliationIssues(c.Ctx, c.Subj) }).Internal().
			Describe("Requires an attributable Admin actor. Read-only, bounded to 500 oldest issues. Existing workers recover safe cases; material conflicts require manual review. This view never infers provider success or rewrites money."),
		Get("/admin/accounts/{account_id}/assistance", "adminGetAssistance", tagAdmin, "Live assistance authorizations and accessibility needs (audited)",
			func(c *Call) (usecases.AdminAssistanceView, error) {
				return c.S.AdminGetAssistance(c.Ctx, c.Subj, c.ID("account_id"))
			}).Internal(),
		CommandPre("POST", "/admin/accounts/{account_id}/assisted-activities", "adminRecordAssistedActivity", tagAdmin, "Record an assisted action", http.StatusCreated,
			func(c *Call, in *usecases.AssistedActivityInput) (usecases.Evidence, error) {
				return c.S.CheckAssistedMedia(c.Ctx, c.Subj, c.ID("account_id"), *in)
			},
			func(c *Call, in *usecases.AssistedActivityInput, ev usecases.Evidence) (usecases.AssistedActivityView, error) {
				return c.S.AdminRecordAssistedActivity(c.Ctx, c.Subj, c.ID("account_id"), *in, ev)
			}).Internal().Describe("Needs an attributable admin actor and a live owner authorization covering the activity and property; the owner is notified.").Errs(403, 404),
		Command("POST", "/admin/accounts/{account_id}/assisted-properties", "adminCreateAssistedProperty", tagAdmin, "Create a DRAFT property for the owner", http.StatusCreated,
			func(c *Call, in *usecases.AssistedPropertyInput) (usecases.AssistedPropertyView, error) {
				return c.S.AdminCreateAssistedProperty(c.Ctx, c.Subj, c.ID("account_id"), *in)
			}).Internal().Describe("Concierge onboarding under a PROPERTY_RECORDS authorization. The record stays DRAFT for the owner to review; "+
			"verification and publication remain owner actions.").Errs(403, 409, 422),
	}
}
