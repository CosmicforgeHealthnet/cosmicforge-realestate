package http

import (
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

const (
	tagMaintenance   = "Maintenance"
	tagProfessionals = "Professionals"
)

// providerEvidence verifies files the calling provider uploaded.
func providerEvidence[Req mediaRefs](classes []string) func(c *Call, in *Req) (usecases.Evidence, error) {
	return func(c *Call, in *Req) (usecases.Evidence, error) {
		return c.S.CheckProviderEvidence(c.Ctx, c.Subj, (*in).MediaRefs(), classes...)
	}
}

func maintenanceRoutes() []Route {
	mid := func(c *Call) ids.ID { return c.ID("request_id") }
	req := authz.Do(authz.CapMaintenanceReq)
	approve := authz.Do(authz.CapMaintenanceApprove)
	return []Route{
		List("/maintenance", "listMaintenance", tagMaintenance, "Maintenance requests across my properties",
			func(c *Call, p Page) (pagination.Response[usecases.MaintenanceView], error) {
				pid, err := c.QueryID("property_id")
				if err != nil {
					return pagination.Response[usecases.MaintenanceView]{}, err
				}
				return c.S.ListMaintenance(c.Ctx, c.Subj, pid, c.Query("status"), p.Cursor, p.Limit)
			}).Q(Param{Name: "property_id"}, Param{Name: "status", Enum: []string{"OPEN", "TRIAGED", "QUOTING", "APPROVAL_PENDING", "APPROVED", "ASSIGNED",
			"IN_PROGRESS", "COMPLETION_REVIEW", "COMPLETED", "REWORK", "CANCELLED", "DISPUTED"}}),
		CommandPre("POST", "/maintenance", "createMaintenance", tagMaintenance, "Report a maintenance need", http.StatusCreated,
			func(c *Call, in *usecases.MaintenanceInput) (usecases.Evidence, error) {
				return c.S.CheckMediaEvidence(c.Ctx, c.Subj, usecases.ScopeProperty, in.PropertyID, req, in.MediaRefs(), evidenceClasses...)
			},
			func(c *Call, in *usecases.MaintenanceInput, ev usecases.Evidence) (usecases.MaintenanceView, error) {
				return c.S.CreateMaintenance(c.Ctx, c.Subj, *in, ev)
			}).Capture("maintenance_id", "data.id").Capture("maintenance_version", "data.version").Errs(422),
		Get("/maintenance/{request_id}", "getMaintenance", tagMaintenance, "Request with quotes, approvals, job records and warranty",
			func(c *Call) (usecases.MaintenanceView, error) { return c.S.GetMaintenance(c.Ctx, c.Subj, mid(c)) }),
		Update("PATCH", "/maintenance/{request_id}", "triageMaintenance", tagMaintenance, "Triage: priority, responsibility, budget (If-Match)", false,
			func(c *Call, in *usecases.MaintenanceTriage) (usecases.MaintenanceView, error) {
				return c.S.TriageMaintenance(c.Ctx, c.Subj, mid(c), c.Version, *in)
			}).Describe("Deciding responsibility moves an OPEN request to TRIAGED. Locked once work is approved.").Errs(409),
		Command("POST", "/maintenance/{request_id}/request-quotes", "requestMaintenanceQuotes", tagMaintenance, "Open quoting", http.StatusOK,
			func(c *Call, in *usecases.QuoteRequestInput) (usecases.MaintenanceView, error) {
				return c.S.RequestQuotes(c.Ctx, c.Subj, mid(c), *in)
			}).Describe("PLATFORM publishes maintenance.request.created.v1 for artisan discovery; SELF_SOURCED lets the owner record quotes from their own providers.").Errs(409),
		CommandPre("POST", "/maintenance/{request_id}/quotes", "recordMaintenanceQuote", tagMaintenance, "Record a self-sourced quote", http.StatusCreated,
			ownerEvidence[usecases.OwnerQuoteInput](usecases.ScopeMaintenance, "request_id", req, evidenceClasses),
			func(c *Call, in *usecases.OwnerQuoteInput, ev usecases.Evidence) (usecases.MaintenanceView, error) {
				return c.S.RecordOwnerQuote(c.Ctx, c.Subj, mid(c), *in, ev)
			}).Errs(409, 422),
		Command("POST", "/maintenance/{request_id}/approve", "approveMaintenance", tagMaintenance, "Approve a quote or reject all", http.StatusOK,
			func(c *Call, in *usecases.ApprovalInput) (usecases.MaintenanceView, error) {
				return c.S.ApproveMaintenance(c.Ctx, c.Subj, mid(c), *in)
			}).Describe("Approval assigns the job to the quoting provider. Managers approve within their mandate's spend limit and emergency rule "+
			"(403 APPROVAL_LIMIT_EXCEEDED / OWNER_APPROVAL_REQUIRED); budgets are exceeded only by the owner. PLATFORM payment needs payment-service.").Errs(403, 409, 422, 503),
		CommandPre("POST", "/maintenance/{request_id}/job-events", "recordMaintenanceJobEvent", tagMaintenance, "Record self-sourced job progress", http.StatusOK,
			ownerEvidence[usecases.JobEventInput](usecases.ScopeMaintenance, "request_id", req, evidenceClasses),
			func(c *Call, in *usecases.JobEventInput, ev usecases.Evidence) (usecases.MaintenanceView, error) {
				return c.S.RecordOwnerJobEvent(c.Ctx, c.Subj, mid(c), *in, ev)
			}).Describe("Append-only attendance/progress/completion/invoice records; completion needs photos.").Errs(409, 422),
		CommandPre("POST", "/maintenance/{request_id}/completion", "reviewMaintenanceCompletion", tagMaintenance, "Accept, rework or dispute the work", http.StatusOK,
			ownerEvidence[usecases.CompletionReview](usecases.ScopeMaintenance, "request_id", approve, evidenceClasses),
			func(c *Call, in *usecases.CompletionReview, ev usecases.Evidence) (usecases.MaintenanceView, error) {
				return c.S.ReviewCompletion(c.Ctx, c.Subj, mid(c), *in, ev)
			}).Describe("Acceptance starts the quote's warranty, updates the asset's service date and (PLATFORM) requests payment of the approved/invoiced amount.").Errs(409, 422),
		CommandPre("POST", "/maintenance/{request_id}/offline-payment", "recordMaintenancePayment", tagMaintenance, "Record an off-platform provider payment", http.StatusOK,
			ownerEvidence[usecases.OfflinePaymentInput](usecases.ScopeMaintenance, "request_id", approve, evidenceClasses),
			func(c *Call, in *usecases.OfflinePaymentInput, ev usecases.Evidence) (usecases.MaintenanceView, error) {
				return c.S.RecordOfflinePayment(c.Ctx, c.Subj, mid(c), *in, ev)
			}).Describe("Confirmed when the provider acknowledges it; self-sourced payments stay OWNER_ATTESTED.").Errs(409, 422),
		Command("POST", "/maintenance/{request_id}/cancel", "cancelMaintenance", tagMaintenance, "Cancel a request", http.StatusOK,
			func(c *Call, in *usecases.MaintenanceCancel) (usecases.MaintenanceView, error) {
				return c.S.CancelMaintenance(c.Ctx, c.Subj, mid(c), *in)
			}).Errs(409),
		CommandPre("POST", "/maintenance/{request_id}/warranty-claim", "claimMaintenanceWarranty", tagMaintenance, "Claim the workmanship warranty", http.StatusCreated,
			ownerEvidence[usecases.WarrantyClaimInput](usecases.ScopeMaintenance, "request_id", req, evidenceClasses),
			func(c *Call, in *usecases.WarrantyClaimInput, ev usecases.Evidence) (usecases.MaintenanceView, error) {
				return c.S.ClaimWarranty(c.Ctx, c.Subj, mid(c), *in, ev)
			}).Describe("Opens a linked WARRANTY request routed to the original provider.").Errs(409),
		List("/properties/{property_id}/preventive-schedules", "listPreventiveSchedules", tagMaintenance, "Preventive schedules",
			func(c *Call, _ Page) (pagination.Response[usecases.PreventiveView], error) {
				return slice(c.S.ListPreventive(c.Ctx, c.Subj, c.ID("property_id")))
			}),
		Command("POST", "/properties/{property_id}/preventive-schedules", "createPreventiveSchedule", tagMaintenance, "Add a preventive schedule", http.StatusCreated,
			func(c *Call, in *usecases.PreventiveInput) (usecases.PreventiveView, error) {
				return c.S.CreatePreventive(c.Ctx, c.Subj, c.ID("property_id"), *in)
			}).Describe("The preventive-maintenance worker creates a TRIAGED request lead_days before each due date.").Capture("preventive_schedule_id", "data.id"),
		Update("PATCH", "/preventive-schedules/{schedule_id}", "updatePreventiveSchedule", tagMaintenance, "Edit, pause or retire a schedule (If-Match)", false,
			func(c *Call, in *usecases.PreventiveUpdate) (usecases.PreventiveView, error) {
				return c.S.UpdatePreventive(c.Ctx, c.Subj, c.ID("schedule_id"), c.Version, *in)
			}).Errs(409),
	}
}

func professionalRoutes() []Route {
	aid := func(c *Call) ids.ID { return c.ID("appointment_id") }
	return []Route{
		List("/professional-appointments", "listProfessionalAppointments", tagProfessionals, "Professional appointments",
			func(c *Call, _ Page) (pagination.Response[usecases.AppointmentView], error) {
				return slice(c.S.ListAppointments(c.Ctx, c.Subj, c.Query("status")))
			}).Q(Param{Name: "status", Enum: []string{"REQUESTED", "QUOTING", "APPOINTED", "IN_PROGRESS", "REPORT_SUBMITTED", "ACCEPTED", "DISPUTED", "CANCELLED"}}),
		Command("POST", "/professional-appointments", "createProfessionalAppointment", tagProfessionals, "Request a professional", http.StatusCreated,
			func(c *Call, in *usecases.AppointmentInput) (usecases.AppointmentView, error) {
				return c.S.CreateAppointment(c.Ctx, c.Subj, *in)
			}).Describe("For a property or a sale's due diligence; professional-services is told through professional.appointment.updated.v1.").
			Capture("appointment_id", "data.id").Capture("appointment_version", "data.version").Errs(404),
		Get("/professional-appointments/{appointment_id}", "getProfessionalAppointment", tagProfessionals, "Appointment with report findings",
			func(c *Call) (usecases.AppointmentView, error) { return c.S.GetAppointment(c.Ctx, c.Subj, aid(c)) }),
		Update("PATCH", "/professional-appointments/{appointment_id}", "actOnProfessionalAppointment", tagProfessionals, "Appoint, cancel, accept/dispute the report, grant data-room access or rate (If-Match)", false,
			func(c *Call, in *usecases.AppointmentCommand) (usecases.AppointmentView, error) {
				return c.S.ActOnAppointment(c.Ctx, c.Subj, aid(c), c.Version, *in)
			}).Describe("Appointing needs a valid quote and a confirmed licence; a declared conflict must be acknowledged. Data-room grants expire and are revoked "+
			"when the report is accepted or the appointment ends. PLATFORM fees need payment-service.").Errs(409, 422, 503),
		Command("POST", "/professional-appointments/{appointment_id}/findings/{finding_id}/use", "useProfessionalFinding", tagProfessionals, "Link a finding to a due-diligence item or maintenance request", http.StatusOK,
			func(c *Call, in *usecases.UseFindingInput) (usecases.AppointmentView, error) {
				return c.S.UseFinding(c.Ctx, c.Subj, aid(c), c.ID("finding_id"), *in)
			}).Errs(409),
	}
}

func providerInternalRoutes() []Route {
	return []Route{
		CommandPre("POST", "/maintenance/{request_id}/provider-quotes", "artisanSubmitQuote", tagInternal, "Artisan quotes a platform-sourced request (artisan-service)", http.StatusOK,
			providerEvidence[usecases.ProviderQuoteInput](evidenceClasses),
			func(c *Call, in *usecases.ProviderQuoteInput, ev usecases.Evidence) (usecases.ProviderMaintenanceView, error) {
				return c.S.ProviderSubmitQuote(c.Ctx, c.Subj, c.ID("request_id"), *in, ev)
			}).Internal().Describe("Idempotent per external_quote_ref; the delegated artisan actor is the quoting provider.").Errs(409, 422),
		CommandPre("POST", "/maintenance-jobs/{job_id}/provider-events", "artisanJobEvent", tagInternal, "Assigned artisan reports attendance, progress, completion, invoice, payment receipt or withdrawal", http.StatusOK,
			providerEvidence[usecases.JobEventInput](evidenceClasses),
			func(c *Call, in *usecases.JobEventInput, ev usecases.Evidence) (usecases.ProviderMaintenanceView, error) {
				return c.S.ProviderJobEvent(c.Ctx, c.Subj, c.ID("job_id"), *in, ev)
			}).Internal().Describe("Append-only and idempotent per event_id; late records never move state backwards.").Errs(409, 422),
		CommandPre("POST", "/professional-appointments/{appointment_id}/provider-updates", "professionalUpdateAppointment", tagInternal, "Professional quotes, declines, starts or submits the signed report", http.StatusOK,
			providerEvidence[usecases.ProviderAppointmentUpdate](scanClasses),
			func(c *Call, in *usecases.ProviderAppointmentUpdate, ev usecases.Evidence) (usecases.AppointmentView, error) {
				return c.S.ProviderUpdateAppointment(c.Ctx, c.Subj, c.ID("appointment_id"), *in, ev)
			}).Internal().Errs(409, 422),
		Command("POST", "/admin/maintenance/{request_id}/resolution", "adminResolveMaintenance", tagAdmin, "Decide a disputed maintenance completion", http.StatusOK,
			func(c *Call, in *usecases.MaintenanceResolution) (usecases.MaintenanceView, error) {
				return c.S.AdminResolveMaintenance(c.Ctx, c.Subj, c.ID("request_id"), *in)
			}).Internal().Errs(409),
		CommandPre("POST", "/managed/maintenance", "managerCreateMaintenance", tagInternal, "Delegated manager reports maintenance (maintenance.request)", http.StatusCreated,
			func(c *Call, in *usecases.MaintenanceInput) (usecases.Evidence, error) {
				return c.S.CheckMediaEvidence(c.Ctx, c.Subj, usecases.ScopeProperty, in.PropertyID, authz.Do(authz.CapMaintenanceReq), in.MediaRefs(), evidenceClasses...)
			},
			func(c *Call, in *usecases.MaintenanceInput, ev usecases.Evidence) (usecases.MaintenanceView, error) {
				return c.S.CreateMaintenance(c.Ctx, c.Subj, *in, ev)
			}).Internal().Capture("managed_maintenance_id", "data.id"),
		Get("/managed/maintenance/{request_id}", "managerGetMaintenance", tagInternal, "Delegated manager reads a request",
			func(c *Call) (usecases.MaintenanceView, error) {
				return c.S.GetMaintenance(c.Ctx, c.Subj, c.ID("request_id"))
			}).Internal(),
		Command("POST", "/managed/maintenance/{request_id}/approve", "managerApproveMaintenance", tagInternal, "Delegated manager approves within its limits (maintenance.approve)", http.StatusOK,
			func(c *Call, in *usecases.ApprovalInput) (usecases.MaintenanceView, error) {
				return c.S.ApproveMaintenance(c.Ctx, c.Subj, c.ID("request_id"), *in)
			}).Internal().Errs(403, 409, 422, 503),
	}
}
