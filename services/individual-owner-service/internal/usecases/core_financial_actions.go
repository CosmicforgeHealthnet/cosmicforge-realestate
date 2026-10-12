package usecases

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

type CoreRecorded struct {
	Recorded bool `json:"recorded"`
}
type CoreBusinessInput struct {
	BusinessType string `json:"business_type" v:"required"`
}
type CoreEvidenceInput struct {
	BusinessType string `json:"business_type" v:"required"`
	payment.CoreEvidence
}
type CoreReleaseInput struct {
	BusinessType string `json:"business_type" v:"required"`
	payment.CoreRelease
}
type CoreCaseInput struct {
	OwnerClaimID *ids.ID `json:"owner_claim_id,omitempty"`
	BusinessType string  `json:"business_type" v:"required"`
	payment.CoreCaseRequest
}
type CoreCancelInput struct {
	BusinessType string   `json:"business_type" v:"required"`
	EvidenceIDs  []ids.ID `json:"evidence_ids" v:"required,max=30"`
}
type CoreJournal struct {
	JournalID ids.ID `json:"journal_id"`
}
type CoreResourceView struct {
	Type  string          `json:"resource_type"`
	ID    ids.ID          `json:"resource_id"`
	State string          `json:"state"`
	Proof json.RawMessage `json:"proof"`
}

func coreReceiptLine(r payment.CoreReceipt, id ids.ID) (payment.CoreLine, error) {
	for _, group := range [][]payment.CoreAllocation{r.Allocations, r.AwardAllocations} {
		latest := map[ids.ID]int{}
		for _, a := range group {
			if a.Version > latest[a.IntentID] {
				latest[a.IntentID] = a.Version
			}
		}
		for _, a := range group {
			if a.Version == latest[a.IntentID] {
				for _, l := range a.Lines {
					if l.ID == id {
						return l, nil
					}
				}
			}
		}
	}
	return payment.CoreLine{}, precondition("CURRENT_ALLOCATION_LINE_REQUIRED", "Use a current allocation line of this immutable financial binding.")
}

// milestoneReference returns recorded business facts, never caller prose.
// A condition owned by another authority remains unavailable until that
// authority's actual contract supplies evidence; no fake approval is created.
func (s *Service) coreMilestone(ctx context.Context, b CoreBusinessContext, condition string) (string, error) {
	var reference string
	var valid bool
	var e error
	switch b.Type {
	case AggRentObligation, "deposit_obligation":
		switch condition {
		case "SIGNED_AGREEMENT", "EXECUTED_LEASE":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT a.status='EFFECTIVE' AND a.fully_signed_at IS NOT NULL,a.id::text||':signed-version:'||a.current_version_no::text FROM agreements a JOIN tenancies t ON t.agreement_id=a.id LEFT JOIN rent_obligations o ON o.tenancy_id=t.id LEFT JOIN deposit_obligations d ON d.tenancy_id=t.id WHERE o.id=$1 OR d.id=$1 LIMIT 1`, b.ID).Scan(&valid, &reference)
		case "POSSESSION", "COMMENCEMENT":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT c.locked_at IS NOT NULL AND c.owner_ack_at IS NOT NULL AND c.tenant_ack_at IS NOT NULL,c.id::text FROM move_in_checklists c JOIN tenancies t ON t.id=c.tenancy_id LEFT JOIN rent_obligations o ON o.tenancy_id=t.id WHERE o.id=$1 AND c.phase='MOVE_IN' LIMIT 1`, b.ID).Scan(&valid, &reference)
		case "VALID_TENANCY":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT t.status IN ('ACTIVE','RENEWAL_PENDING','TERMINATION_PENDING') AND NOT t.dispute_flag,t.id::text||':version:'||t.version::text FROM tenancies t JOIN rent_obligations o ON o.tenancy_id=t.id WHERE o.id=$1`, b.ID).Scan(&valid, &reference)
		case "RENT_DUE":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT due_date<=$2 AND status NOT IN ('WAIVED','DISPUTED'),id::text||':'||due_date::text FROM rent_obligations WHERE id=$1`, b.ID, s.now()).Scan(&valid, &reference)
		case "APPROVED_CHANGE":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT a.source='RENEWAL' AND a.status='EFFECTIVE' AND a.fully_signed_at IS NOT NULL AND a.parent_agreement_id IS NOT NULL,a.id::text||':accepted-renewal-version:'||a.current_version_no::text FROM agreements a JOIN tenancies t ON t.agreement_id=a.id JOIN rent_obligations o ON o.tenancy_id=t.id WHERE o.id=$1`, b.ID).Scan(&valid, &reference)
		case "MOVE_OUT":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT m.inspected_at IS NOT NULL AND m.status IN ('INSPECTED','SETTLEMENT_PENDING','COMPLETED'),m.id::text||':inspection:'||COALESCE(m.inspected_at::text,'') FROM move_out_cases m JOIN deposit_obligations d ON d.tenancy_id=m.tenancy_id WHERE d.id=$1`, b.ID).Scan(&valid, &reference)
		case "APPROVED_CLAIM":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT COALESCE(deduction_total_minor>0 AND (tenant_response='ACCEPT' OR resolved_at IS NOT NULL AND resolution_owner_minor>0),false),id::text||':decision-version:'||version::text FROM deposit_obligations WHERE id=$1`, b.ID).Scan(&valid, &reference)
		case "DISPUTE_DECISION":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT resolved_at IS NOT NULL,id::text||':recorded-resolution:'||COALESCE(resolved_at::text,'') FROM deposit_obligations WHERE id=$1`, b.ID).Scan(&valid, &reference)
		case "LAND_AUTHORITY":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT p.category='LAND' AND r.status='VERIFIED' AND (r.expires_at IS NULL OR r.expires_at>$2),r.id::text||':verified:'||r.version::text FROM properties p JOIN ownership_records r ON r.property_id=p.id AND r.is_primary WHERE p.id=$1`, b.PropertyID, s.now()).Scan(&valid, &reference)
		case "MILESTONE":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT a.status='EFFECTIVE' AND a.fully_signed_at IS NOT NULL AND NOT t.dispute_flag,a.id::text||':accepted-version:'||a.current_version_no::text FROM agreements a JOIN tenancies t ON t.agreement_id=a.id JOIN rent_obligations o ON o.tenancy_id=t.id WHERE o.id=$1`, b.ID).Scan(&valid, &reference)
		case "ACCEPTED_QUOTE":
			valid = true
			reference = b.AgreementRef + ":accepted-version:" + b.AgreementVersion
		}
	case AggBooking, aggExtension:
		if b.Type == aggExtension {
			var booking ids.ID
			e = s.dbq(ctx).QueryRow(ctx, `SELECT booking_id FROM shortlet_booking_extensions WHERE id=$1`, b.ID).Scan(&booking)
			if e != nil {
				return "", e
			}
			b.ID = booking
		}
		switch condition {
		case "CHECK_IN":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT checked_in_at IS NOT NULL AND status IN ('CHECKED_IN','CHECKED_OUT','COMPLETED'),id::text||':check-in:'||COALESCE(checked_in_at::text,'') FROM shortlet_bookings WHERE id=$1`, b.ID).Scan(&valid, &reference)
		case "CHECK_OUT", "STAY_COMPLETE":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT checked_out_at IS NOT NULL AND status IN ('CHECKED_OUT','COMPLETED'),id::text||':check-out:'||COALESCE(checked_out_at::text,'') FROM shortlet_bookings WHERE id=$1`, b.ID).Scan(&valid, &reference)
		}
	case "closing_milestone":
		if slices.Contains([]string{"TITLE", "DUE_DILIGENCE", "CLOSING", "HANDOVER", "REGISTRATION", "SURVEY", "ENCUMBRANCE", "POSSESSION"}, condition) {
			// Completing a payment milestone alone cannot authorize title/closing.
			e = s.dbq(ctx).QueryRow(ctx, `SELECT s.status='COMPLETED' AND NOT EXISTS(SELECT 1 FROM closing_milestones x WHERE x.sale_id=s.id AND x.required_for_completion AND x.status NOT IN ('SATISFIED','WAIVED')),s.id::text||':completed:'||COALESCE(s.completed_at::text,'') FROM sale_transactions s JOIN closing_milestones m ON m.sale_id=s.id WHERE m.id=$1`, b.ID).Scan(&valid, &reference)
		}
	case "maintenance_job":
		switch condition {
		case "QUOTE":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT q.status='ACCEPTED',q.id::text FROM maintenance_jobs j JOIN maintenance_quotes q ON q.id=j.quote_id WHERE j.id=$1`, b.ID).Scan(&valid, &reference)
		case "COMPLETION", "ACCEPTANCE":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT status='ACCEPTED' AND accepted_at IS NOT NULL,id::text||':accepted:'||COALESCE(accepted_at::text,'') FROM maintenance_jobs WHERE id=$1`, b.ID).Scan(&valid, &reference)
		case "ATTENDANCE", "ACCESS":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT true,id::text FROM maintenance_job_events WHERE job_id=$1 AND event_type='ATTENDED' AND source='ARTISAN_SERVICE' ORDER BY occurred_at DESC LIMIT 1`, b.ID).Scan(&valid, &reference)
		case "WARRANTY":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT status='ACTIVE',id::text FROM maintenance_warranties WHERE job_id=$1 ORDER BY created_at DESC LIMIT 1`, b.ID).Scan(&valid, &reference)
		}
	case AggAppointment:
		switch condition {
		case "MANDATE":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT status IN ('APPOINTED','IN_PROGRESS','REPORT_SUBMITTED','ACCEPTED') AND fee_collection='PLATFORM',id::text||':mandate:'||scope FROM professional_appointments WHERE id=$1`, b.ID).Scan(&valid, &reference)
		case "CONFLICT_CHECK":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT conflict_declared IS NOT NULL AND licence_ref IS NOT NULL,id::text||':disclosure:'||COALESCE(licence_ref,'') FROM professional_appointments WHERE id=$1`, b.ID).Scan(&valid, &reference)
		case "DELIVERABLE", "ACCEPTANCE":
			e = s.dbq(ctx).QueryRow(ctx, `SELECT status='ACCEPTED' AND report_signed AND report_media_id IS NOT NULL,COALESCE(report_media_id::text,'') FROM professional_appointments WHERE id=$1`, b.ID).Scan(&valid, &reference)
		}
	}
	if e != nil && !isNoRows(e) {
		return "", e
	}
	if !valid || reference == "" {
		return "", precondition("AUTHORITATIVE_MILESTONE_REQUIRED", fmt.Sprintf("%s has no completed authoritative %s evidence; another issuer or business milestone may be required.", b.Type, condition))
	}
	return reference, nil
}
func (s *Service) SubmitCoreBusinessEvidence(ctx context.Context, subj authz.Subject, id ids.ID, in CoreEvidenceInput) error {
	b, e := s.ownedCoreAction(ctx, subj, in.BusinessType, id)
	if e != nil {
		return e
	}
	c, e := s.coreClient()
	if e != nil {
		return e
	}
	r, e := s.readCoreReceipt(ctx, b)
	if e != nil {
		return e
	}
	if _, e = coreReceiptLine(r, in.LineID); e != nil {
		return e
	}
	current, _, e := s.coreBusiness(ctx, in.BusinessType, id)
	if e != nil {
		return e
	}
	p, e := c.Policy(ctx, r.Quote.PolicyID)
	if e != nil {
		return downstream("payment-service", e)
	}
	if !slices.Contains(p.Conditions, in.Condition) || !slices.Contains(p.EvidenceServices, string(platform.IndividualOwnerService)) || p.HumanReview {
		return precondition("AUTHORITATIVE_EVIDENCE_ISSUER_REQUIRED", "This policy requires another evidence authority or recorded human approval.")
	}
	ref, e := s.coreMilestone(ctx, current, in.Condition)
	if e != nil {
		return e
	}
	in.Reference = ref
	in.Version = strconv.FormatInt(current.Version, 10)
	in.PolicyVersion = p.Version
	in.HumanApprovalRef = ""
	if e = c.SubmitEvidence(coreOwnerContext(ctx, b.Business.OwnerID), in.CoreEvidence); e != nil {
		return downstream("payment-service", e)
	}
	return nil
}
func (s *Service) ReleaseCoreBusiness(ctx context.Context, subj authz.Subject, id ids.ID, in CoreReleaseInput, key string) (CoreJournal, error) {
	b, e := s.ownedCoreAction(ctx, subj, in.BusinessType, id)
	if e != nil {
		return CoreJournal{}, e
	}
	c, e := s.coreClient()
	if e != nil {
		return CoreJournal{}, e
	}
	r, e := s.readCoreReceipt(ctx, b)
	if e != nil {
		return CoreJournal{}, e
	}
	line, e := coreReceiptLine(r, in.LineID)
	if e != nil {
		return CoreJournal{}, e
	}
	if line.Kind == "DEPOSIT" || line.Kind == "RESERVE" {
		return CoreJournal{}, precondition("APPROVED_DEPOSIT_REMEDY_REQUIRED", "Deposits are not ordinary Owner earnings; use the amount-specific Core case/decision/refund workflow.")
	}
	current, _, e := s.coreBusiness(ctx, in.BusinessType, id)
	if e != nil {
		return CoreJournal{}, e
	}
	if current.OwnerID != b.Business.OwnerID {
		return CoreJournal{}, precondition("FINANCIAL_OWNER_ASSIGNMENT_REQUIRED", "Property transfer needs a verified assignment; original financial history remains with its issuer.")
	}
	if slices.Contains([]string{"CANCELLED", "DISPUTED", "FAILED", "WAIVED"}, current.Status) {
		return CoreJournal{}, precondition("RELEASE_BUSINESS_STATE_BLOCKED", "Cancelled or disputed business cannot authorize release.")
	}
	p, e := c.Policy(ctx, r.Quote.PolicyID)
	if e != nil {
		return CoreJournal{}, downstream("payment-service", e)
	}
	if in.PolicyVersion != p.Version {
		return CoreJournal{}, precondition("FINANCIAL_POLICY_VERSION_MISMATCH", "Use the immutable accepted policy version.")
	}
	for _, condition := range p.Conditions {
		if _, e = s.coreMilestone(ctx, current, condition); e != nil {
			return CoreJournal{}, e
		}
	}
	v, e := c.Release(coreOwnerContext(ctx, b.Business.OwnerID), in.CoreRelease, key)
	if e != nil {
		return CoreJournal{}, downstream("payment-service", e)
	}
	return CoreJournal{v}, nil
}
func (s *Service) OpenCoreBusinessCase(ctx context.Context, subj authz.Subject, id ids.ID, in CoreCaseInput, key string) (payment.CoreCase, error) {
	b, e := s.ownedCoreAction(ctx, subj, in.BusinessType, id)
	if e != nil {
		return payment.CoreCase{}, e
	}
	c, e := s.coreClient()
	if e != nil {
		return payment.CoreCase{}, e
	}
	r, e := s.readCoreReceipt(ctx, b)
	if e != nil {
		return payment.CoreCase{}, e
	}
	line, e := coreReceiptLine(r, in.LineID)
	if e != nil {
		return payment.CoreCase{}, e
	}
	if in.AmountMinor > line.AmountMinor {
		return payment.CoreCase{}, precondition("FINANCIAL_CASE_AMOUNT_EXCEEDED", "Only the bound line's eligible amount may be contested.")
	}
	claimAuthority := b.Business.Type + ":" + id.String() + ":owner-decision"
	if in.Kind == "DEPOSIT_CLAIM" {
		if in.DepositID == nil {
			return payment.CoreCase{}, precondition("DEPOSIT_DECISION_REQUIRED", "Use a recorded deposit liability and existing business claim.")
		}
		switch b.Business.Type {
		case "deposit_obligation":
			d, e := s.q.GetDepositForUpdate(ctx, s.Pool, id)
			if e != nil {
				return payment.CoreCase{}, e
			}
			if !slices.Contains([]string{"DEDUCTION_PROPOSED", "DISPUTED", "RELEASE_PENDING"}, d.Status) || in.AmountMinor > d.DeductionTotalMinor || d.DeductionTotalMinor <= 0 {
				return payment.CoreCase{}, precondition("DEPOSIT_DECISION_REQUIRED", "An itemized move-out proposal must authorize this amount.")
			}
			claimAuthority = "deposit_obligation:" + id.String() + ":proposal-version:" + strconv.FormatInt(d.Version, 10)
		case AggBooking:
			if in.OwnerClaimID == nil {
				return payment.CoreCase{}, precondition("DAMAGE_CLAIM_REQUIRED", "Use the existing evidenced short-let damage claim.")
			}
			var version int64
			var claim int64
			var status string
			e = s.dbq(ctx).QueryRow(ctx, `SELECT version,claimed_minor,status FROM damage_claims WHERE id=$1 AND booking_id=$2 AND property_id=$3`, *in.OwnerClaimID, id, b.Business.PropertyID).Scan(&version, &claim, &status)
			if e != nil || claim <= 0 || in.AmountMinor > claim || !slices.Contains([]string{"OPEN", "GUEST_ACCEPTED", "DISPUTED", "RESPONSE_OVERDUE", "RESOLVED"}, status) {
				return payment.CoreCase{}, precondition("DAMAGE_CLAIM_REQUIRED", "The actual booking damage claim must authorize the contested amount.")
			}
			claimAuthority = "damage_claim:" + in.OwnerClaimID.String() + ":version:" + strconv.FormatInt(version, 10)
		default:
			return payment.CoreCase{}, precondition("DEPOSIT_DECISION_REQUIRED", "This business has no supported deposit claim.")
		}
		dep, e := c.Deposit(coreOwnerContext(ctx, b.Business.OwnerID), *in.DepositID)
		if e != nil {
			return payment.CoreCase{}, downstream("payment-service", e)
		}
		if dep.IntentID != b.Intent.ID || dep.LineID != in.LineID {
			return payment.CoreCase{}, precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Deposit does not match this claim.")
		}
	}
	owned, e := c.Snapshot(ctx, b.Business.OwnerID)
	if e != nil {
		return payment.CoreCase{}, downstream("payment-service", e)
	}
	in.ClaimantRef = owned.CanonicalPartyRef
	in.RespondentRef = r.Quote.LegalPartyRef
	if in.RespondentRef == in.ClaimantRef {
		in.RespondentRef = b.Business.ReceivingPartyRef
	}
	if in.RespondentRef == "" {
		return payment.CoreCase{}, precondition("AUTHORITATIVE_RESPONDENT_REQUIRED", "An actual payer or beneficiary must be identified.")
	}
	in.AuthorityRef = claimAuthority
	v, e := c.OpenCase(coreOwnerContext(ctx, b.Business.OwnerID), in.CoreCaseRequest, key)
	if e != nil {
		return v, downstream("payment-service", e)
	}
	if v.IntentID != b.Intent.ID || v.QuoteID != b.QuoteID || v.LineID != in.LineID {
		return payment.CoreCase{}, precondition("PAYMENT_READBACK_CONTEXT_MISMATCH", "Case response differs from its binding.")
	}
	return v, nil
}
func (s *Service) CancelCoreBusinessIntent(ctx context.Context, subj authz.Subject, kind string, id ids.ID, evidence []ids.ID, key string) (payment.CollectionIntent, error) {
	b, e := s.ownedCoreAction(ctx, subj, kind, id)
	if e != nil {
		return payment.CollectionIntent{}, e
	}
	current, _, e := s.coreBusiness(ctx, kind, id)
	if e != nil {
		return payment.CollectionIntent{}, e
	}
	if !slices.Contains([]string{"CANCELLED", "FAILED", "WAIVED", "NO_SHOW"}, current.Status) {
		return payment.CollectionIntent{}, precondition("BUSINESS_CANCELLATION_REQUIRED", "Record an authorized business cancellation before requesting a financial remedy.")
	}
	c, e := s.coreClient()
	if e != nil {
		return payment.CollectionIntent{}, e
	}
	v, e := c.Cancel(coreOwnerContext(ctx, b.Business.OwnerID), payment.CoreCancellation{IntentID: b.Intent.ID, AuthorityRef: kind + ":" + id.String() + ":version:" + strconv.FormatInt(current.Version, 10), ReasonRef: current.Status, EvidenceIDs: evidence}, key)
	if e != nil {
		return v, downstream("payment-service", e)
	}
	return v, nil
}
