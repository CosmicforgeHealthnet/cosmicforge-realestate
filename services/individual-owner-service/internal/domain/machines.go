package domain

import "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/lifecycle"

// Section 7 canonical lifecycles (spec v1.0 §7). Edges marked "eng" are
// engineering additions required for a complete, safe machine where §7
// gives only the forward path (§15: they do not change product behaviour).
// The account lifecycle is in account.go.
var (
	Ownership = lifecycle.New("ownership_record", "ownership_records",
		[]string{"DRAFT", "EVIDENCE_PENDING", "UNDER_VERIFICATION", "VERIFIED", "REJECTED", "CONFLICT", "REVERIFICATION_REQUIRED", "TRANSFER_PENDING", "TRANSFERRED"},
		map[string][]string{
			"DRAFT":                   {"EVIDENCE_PENDING", "CONFLICT"},
			"EVIDENCE_PENDING":        {"UNDER_VERIFICATION"},
			"UNDER_VERIFICATION":      {"VERIFIED", "REJECTED", "CONFLICT"},
			"VERIFIED":                {"REVERIFICATION_REQUIRED", "TRANSFER_PENDING"},
			"TRANSFER_PENDING":        {"VERIFIED", "TRANSFERRED"},
			"REVERIFICATION_REQUIRED": {"UNDER_VERIFICATION"},           // eng: re-verification resubmission
			"CONFLICT":                {"EVIDENCE_PENDING", "REJECTED"}, // eng: admin conflict resolution (release to verification or reject)
		})

	Property = lifecycle.New("property", "properties",
		[]string{"DRAFT", "ACTIVE", "RESTRICTED", "TRANSFER_PENDING", "ARCHIVED"},
		map[string][]string{
			"DRAFT":            {"ACTIVE", "ARCHIVED"},
			"ACTIVE":           {"RESTRICTED", "TRANSFER_PENDING", "ARCHIVED"},
			"RESTRICTED":       {"ACTIVE"}, // eng: authorized remediation
			"TRANSFER_PENDING": {"ACTIVE"}, // eng: transfer completed or cancelled
		})

	Listing = lifecycle.New("listing", "listings",
		[]string{"DRAFT", "READY_FOR_REVIEW", "PUBLISHED", "PAUSED", "WITHDRAWN", "ARCHIVED"},
		map[string][]string{
			"DRAFT":            {"READY_FOR_REVIEW", "ARCHIVED"},
			"READY_FOR_REVIEW": {"PUBLISHED", "DRAFT"},
			"PUBLISHED":        {"PAUSED", "WITHDRAWN", "ARCHIVED"},
			"PAUSED":           {"PUBLISHED", "WITHDRAWN", "ARCHIVED"},
			"WITHDRAWN":        {"ARCHIVED"},
		})

	// §7 defines the availability state set; transitions are eng.
	Availability = lifecycle.New("availability", "availability_state",
		[]string{"AVAILABLE", "RESERVED", "APPLICATION_UNDER_REVIEW", "OFFER_ACCEPTED", "OCCUPIED", "TEMP_UNAVAILABLE", "OFF_MARKET", "SOLD", "WITHDRAWN"},
		map[string][]string{
			"AVAILABLE":                {"RESERVED", "APPLICATION_UNDER_REVIEW", "OFFER_ACCEPTED", "OCCUPIED", "TEMP_UNAVAILABLE", "OFF_MARKET", "SOLD", "WITHDRAWN"},
			"RESERVED":                 {"AVAILABLE", "APPLICATION_UNDER_REVIEW", "OFFER_ACCEPTED", "OCCUPIED", "SOLD", "WITHDRAWN"},
			"APPLICATION_UNDER_REVIEW": {"AVAILABLE", "OFFER_ACCEPTED", "WITHDRAWN"},
			"OFFER_ACCEPTED":           {"AVAILABLE", "OCCUPIED", "SOLD", "WITHDRAWN"},
			"OCCUPIED":                 {"AVAILABLE", "OFF_MARKET", "WITHDRAWN"},
			"TEMP_UNAVAILABLE":         {"AVAILABLE", "OFF_MARKET", "WITHDRAWN"},
			"OFF_MARKET":               {"AVAILABLE", "WITHDRAWN"},
		})

	Inquiry = lifecycle.New("inquiry", "inquiries",
		[]string{"NEW", "ACKNOWLEDGED", "QUALIFIED", "VIEWING", "APPLICATION", "OFFER", "BOOKING", "CLOSED_WON", "CLOSED_LOST"},
		map[string][]string{
			"NEW":          {"ACKNOWLEDGED", "CLOSED_LOST"},
			"ACKNOWLEDGED": {"QUALIFIED", "CLOSED_LOST"},
			"QUALIFIED":    {"VIEWING", "APPLICATION", "OFFER", "BOOKING", "CLOSED_WON", "CLOSED_LOST"},
			"VIEWING":      {"APPLICATION", "OFFER", "BOOKING", "CLOSED_WON", "CLOSED_LOST"},
			"APPLICATION":  {"OFFER", "CLOSED_WON", "CLOSED_LOST"},
			"OFFER":        {"CLOSED_WON", "CLOSED_LOST"},
			"BOOKING":      {"CLOSED_WON", "CLOSED_LOST"},
		})

	Viewing = lifecycle.New("viewing", "viewing_requests",
		[]string{"REQUESTED", "CONFIRMED", "CHECKED_IN", "COMPLETED", "RESCHEDULED", "CANCELLED", "NO_SHOW"},
		map[string][]string{
			"REQUESTED":   {"CONFIRMED", "RESCHEDULED", "CANCELLED", "NO_SHOW"},
			"CONFIRMED":   {"CHECKED_IN", "RESCHEDULED", "CANCELLED", "NO_SHOW"},
			"CHECKED_IN":  {"COMPLETED"},
			"RESCHEDULED": {"CONFIRMED", "CANCELLED"}, // eng
		})

	Application = lifecycle.New("rental_application", "rental_applications",
		[]string{"DRAFT", "RECEIVED", "SUBMITTED", "UNDER_REVIEW", "INFO_REQUESTED", "APPROVED", "CONDITIONAL", "DECLINED", "WAITLISTED", "WITHDRAWN", "EXPIRED", "APPEAL", "HUMAN_REVIEW"},
		map[string][]string{
			"DRAFT":          {"SUBMITTED", "WITHDRAWN"},
			"RECEIVED":       {"UNDER_REVIEW", "WITHDRAWN", "EXPIRED"},
			"SUBMITTED":      {"UNDER_REVIEW", "WITHDRAWN", "EXPIRED"},
			"UNDER_REVIEW":   {"INFO_REQUESTED", "APPROVED", "CONDITIONAL", "DECLINED", "WAITLISTED", "WITHDRAWN", "EXPIRED"},
			"INFO_REQUESTED": {"UNDER_REVIEW", "WITHDRAWN", "EXPIRED"},
			"WAITLISTED":     {"UNDER_REVIEW", "WITHDRAWN", "EXPIRED"}, // eng
			"DECLINED":       {"APPEAL"},
			"APPEAL":         {"HUMAN_REVIEW"},
			"HUMAN_REVIEW":   {"APPROVED", "CONDITIONAL", "DECLINED"},
		})

	RentalOffer = lifecycle.New("rental_offer", "rental_offers",
		[]string{"DRAFT", "SENT", "ACCEPTED", "DECLINED", "EXPIRED", "WITHDRAWN"},
		map[string][]string{
			"DRAFT": {"SENT", "WITHDRAWN"},
			"SENT":  {"ACCEPTED", "DECLINED", "EXPIRED", "WITHDRAWN"},
		})

	Agreement = lifecycle.New("agreement", "agreements",
		[]string{"DRAFT", "REVIEW", "SIGNING", "FULLY_SIGNED", "EFFECTIVE", "CANCELLED"},
		map[string][]string{
			"DRAFT":        {"REVIEW", "CANCELLED"},
			"REVIEW":       {"SIGNING", "DRAFT", "CANCELLED"},
			"SIGNING":      {"FULLY_SIGNED", "CANCELLED"},
			"FULLY_SIGNED": {"EFFECTIVE", "CANCELLED"},
		})

	Tenancy = lifecycle.New("tenancy", "tenancies",
		[]string{"PENDING_MOVE_IN", "ACTIVE", "RENEWAL_PENDING", "TERMINATION_PENDING", "MOVE_OUT_PENDING", "ENDED"},
		map[string][]string{
			"PENDING_MOVE_IN":     {"ACTIVE", "ENDED"}, // eng: cancelled before move-in
			"ACTIVE":              {"RENEWAL_PENDING", "TERMINATION_PENDING"},
			"RENEWAL_PENDING":     {"ACTIVE", "TERMINATION_PENDING"},
			"TERMINATION_PENDING": {"MOVE_OUT_PENDING", "ACTIVE"}, // eng: notice withdrawn
			"MOVE_OUT_PENDING":    {"ENDED"},
		})

	RentObligation = lifecycle.New("rent_obligation", "rent_obligations",
		[]string{"SCHEDULED", "DUE", "PARTIAL", "PAID", "OVERDUE", "DISPUTED", "WAIVED", "REFUND_PENDING", "REFUNDED"},
		map[string][]string{
			"SCHEDULED":      {"DUE", "WAIVED"},
			"DUE":            {"PARTIAL", "PAID", "OVERDUE", "DISPUTED", "WAIVED"},
			"PARTIAL":        {"PAID", "OVERDUE", "DISPUTED"},
			"OVERDUE":        {"PARTIAL", "PAID", "DISPUTED", "WAIVED"},
			"DISPUTED":       {"DUE", "OVERDUE", "PAID", "WAIVED", "REFUND_PENDING"},
			"PAID":           {"REFUND_PENDING", "DISPUTED"},
			"REFUND_PENDING": {"REFUNDED", "PAID"},
		})

	Deposit = lifecycle.New("deposit", "deposit_obligations",
		[]string{"REQUIRED", "FUNDED", "HELD", "RELEASE_PENDING", "DEDUCTION_PROPOSED", "PARTIAL_RELEASED", "RELEASED", "DISPUTED"},
		map[string][]string{
			"REQUIRED":           {"FUNDED"},
			"FUNDED":             {"HELD"},
			"HELD":               {"RELEASE_PENDING", "DEDUCTION_PROPOSED"},
			"RELEASE_PENDING":    {"RELEASED", "DISPUTED"},
			"DEDUCTION_PROPOSED": {"PARTIAL_RELEASED", "RELEASED", "DISPUTED"},
			"DISPUTED":           {"PARTIAL_RELEASED", "RELEASED"},
		})

	Notice = lifecycle.New("notice", "notices",
		[]string{"DRAFT", "ISSUED", "DELIVERED", "ACKNOWLEDGED", "DELIVERY_FAILED", "DISPUTED"},
		map[string][]string{
			"DRAFT":           {"ISSUED"},
			"ISSUED":          {"DELIVERED", "DELIVERY_FAILED"},
			"DELIVERED":       {"ACKNOWLEDGED", "DISPUTED"},
			"DELIVERY_FAILED": {"ISSUED"},   // eng: re-issue
			"ACKNOWLEDGED":    {"DISPUTED"}, // eng
		})

	Sale = lifecycle.New("sale_transaction", "sale_transactions",
		[]string{"INQUIRY", "OFFERING", "OFFER_ACCEPTED", "DUE_DILIGENCE", "CONTRACTING", "CONDITIONS_PENDING", "CLOSING", "COMPLETED", "CANCELLED", "FAILED", "DISPUTED"},
		func() map[string][]string {
			fwd := []string{"INQUIRY", "OFFERING", "OFFER_ACCEPTED", "DUE_DILIGENCE", "CONTRACTING", "CONDITIONS_PENDING", "CLOSING", "COMPLETED"}
			m := map[string][]string{}
			for i := 0; i < len(fwd)-1; i++ {
				m[fwd[i]] = []string{fwd[i+1], "CANCELLED", "FAILED", "DISPUTED"}
			}
			m["DISPUTED"] = []string{"DUE_DILIGENCE", "CONTRACTING", "CONDITIONS_PENDING", "CANCELLED", "FAILED"} // eng: dispute resolution
			return m
		}())

	ShortletBooking = lifecycle.New("shortlet_booking", "shortlet_bookings",
		[]string{"INQUIRY", "HOLD", "PENDING_APPROVAL", "PENDING_PAYMENT", "CONFIRMED", "CHECKED_IN", "CHECKED_OUT", "COMPLETED", "CANCELLED", "NO_SHOW", "DISPUTED"},
		map[string][]string{
			"INQUIRY":          {"HOLD", "PENDING_APPROVAL", "CANCELLED"},
			"HOLD":             {"PENDING_APPROVAL", "PENDING_PAYMENT", "CANCELLED"},
			"PENDING_APPROVAL": {"PENDING_PAYMENT", "CONFIRMED", "CANCELLED"},
			"PENDING_PAYMENT":  {"CONFIRMED", "CANCELLED"},
			"CONFIRMED":        {"CHECKED_IN", "CANCELLED", "NO_SHOW"},
			"CHECKED_IN":       {"CHECKED_OUT", "DISPUTED"},
			"CHECKED_OUT":      {"COMPLETED", "DISPUTED"},
			"DISPUTED":         {"COMPLETED"},
		})

	ManagerAssignment = lifecycle.New("manager_assignment", "manager_assignments",
		[]string{"PROPOSED", "ACCEPTED", "ACTIVE", "SUSPENDED", "EXPIRING", "REVOKED", "EXPIRED", "TERMINATED"},
		map[string][]string{
			"PROPOSED":  {"ACCEPTED", "TERMINATED"},
			"ACCEPTED":  {"ACTIVE", "REVOKED", "TERMINATED"},
			"ACTIVE":    {"SUSPENDED", "EXPIRING", "REVOKED", "EXPIRED", "TERMINATED"},
			"SUSPENDED": {"ACTIVE", "REVOKED", "EXPIRED", "TERMINATED"},
			"EXPIRING":  {"ACTIVE", "REVOKED", "EXPIRED", "TERMINATED"}, // ACTIVE = eng: term extended
		})

	Maintenance = lifecycle.New("maintenance_request", "maintenance_requests",
		[]string{"OPEN", "TRIAGED", "QUOTING", "APPROVAL_PENDING", "APPROVED", "ASSIGNED", "IN_PROGRESS", "COMPLETION_REVIEW", "COMPLETED", "REWORK", "CANCELLED", "DISPUTED"},
		map[string][]string{
			"OPEN":              {"TRIAGED", "CANCELLED"},
			"TRIAGED":           {"QUOTING", "APPROVAL_PENDING", "CANCELLED"},
			"QUOTING":           {"APPROVAL_PENDING", "CANCELLED"},
			"APPROVAL_PENDING":  {"APPROVED", "QUOTING", "CANCELLED"},
			"APPROVED":          {"ASSIGNED", "CANCELLED"},
			"ASSIGNED":          {"IN_PROGRESS", "CANCELLED"},
			"IN_PROGRESS":       {"COMPLETION_REVIEW", "CANCELLED"},
			"COMPLETION_REVIEW": {"COMPLETED", "REWORK", "DISPUTED"},
			"REWORK":            {"IN_PROGRESS", "DISPUTED", "CANCELLED"},
			"COMPLETED":         {"DISPUTED"},
			"DISPUTED":          {"REWORK", "COMPLETED", "CANCELLED"}, // eng: dispute resolution
		})

	ProfessionalAppointment = lifecycle.New("professional_appointment", "professional_appointments",
		[]string{"REQUESTED", "QUOTING", "APPOINTED", "IN_PROGRESS", "REPORT_SUBMITTED", "ACCEPTED", "DISPUTED", "CANCELLED"},
		map[string][]string{
			"REQUESTED":        {"QUOTING", "APPOINTED", "CANCELLED"},
			"QUOTING":          {"APPOINTED", "CANCELLED"},
			"APPOINTED":        {"IN_PROGRESS", "CANCELLED"},
			"IN_PROGRESS":      {"REPORT_SUBMITTED", "CANCELLED"},
			"REPORT_SUBMITTED": {"ACCEPTED", "DISPUTED"},
			"DISPUTED":         {"ACCEPTED", "CANCELLED"}, // eng
		})

	DataSubjectRequest = lifecycle.New("data_subject_request", "data_subject_requests",
		[]string{"SUBMITTED", "VERIFIED_REQUESTER", "PROCESSING", "PARTIALLY_FULFILLED", "FULFILLED", "DENIED_WITH_REASON", "CLOSED"},
		map[string][]string{
			"SUBMITTED":           {"VERIFIED_REQUESTER", "DENIED_WITH_REASON"},
			"VERIFIED_REQUESTER":  {"PROCESSING", "DENIED_WITH_REASON"},
			"PROCESSING":          {"PARTIALLY_FULFILLED", "FULFILLED", "DENIED_WITH_REASON"},
			"PARTIALLY_FULFILLED": {"FULFILLED", "CLOSED"},
			"FULFILLED":           {"CLOSED"},
			"DENIED_WITH_REASON":  {"CLOSED"},
		})

	AccessGrant = lifecycle.New("access_grant", "access_grants",
		[]string{"REQUESTED", "APPROVED", "ISSUED", "ACTIVE", "EXPIRED", "REVOKED", "COMPROMISED"},
		map[string][]string{
			"REQUESTED": {"APPROVED", "REVOKED"},
			"APPROVED":  {"ISSUED", "REVOKED", "EXPIRED"},
			"ISSUED":    {"ACTIVE", "REVOKED", "EXPIRED", "COMPROMISED"},
			"ACTIVE":    {"EXPIRED", "REVOKED", "COMPROMISED"},
		})

	// Engineering-addition workflows for guarded cases referenced by §13.
	OwnershipChange = lifecycle.New("ownership_change_case", "ownership_change_cases",
		[]string{"OPEN", "EVIDENCE_PENDING", "UNDER_REVIEW", "APPROVED", "COMPLETED", "REJECTED", "CANCELLED"},
		map[string][]string{
			"OPEN":             {"EVIDENCE_PENDING", "CANCELLED"},
			"EVIDENCE_PENDING": {"UNDER_REVIEW", "CANCELLED"},
			"UNDER_REVIEW":     {"APPROVED", "REJECTED"},
			"APPROVED":         {"COMPLETED", "CANCELLED"},
		})
)

// Machines lists every Section 7 machine (documentation and tests).
func Machines() []*lifecycle.Machine {
	return []*lifecycle.Machine{Ownership, Property, Listing, Availability, Inquiry, Viewing, Application, RentalOffer, Agreement,
		Tenancy, RentObligation, Deposit, Notice, Sale, ShortletBooking, ManagerAssignment, Maintenance, ProfessionalAppointment,
		DataSubjectRequest, AccessGrant, OwnershipChange}
}
