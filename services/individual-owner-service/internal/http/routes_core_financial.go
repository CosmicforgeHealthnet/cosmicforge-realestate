package http

import (
	"encoding/json"
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
)

func coreFinancialRoutes() []Route {
	const tag = "Financial Core Workflows"
	business := Param{Name: "business_type", Required: true, Enum: []string{"rent_obligation", "deposit_obligation", "closing_milestone", "shortlet_booking", "shortlet_booking_extension", "maintenance_job", "professional_appointment"}, Doc: "Existing accepted Owner aggregate type; source, payer, amount and permissions are validated against its stored records."}
	pref := CommandPre("PUT", "/financial-wallet/payout-preference", "putCorePayoutPreference", tag, "Select an existing verified Core payout destination", http.StatusOK,
		func(c *Call, in *usecases.CorePayoutPreference) (usecases.CorePayoutPreference, error) {
			return c.S.PrepareCorePayoutPreference(c.Ctx, c.Subj, *in)
		},
		func(c *Call, _ *usecases.CorePayoutPreference, proof usecases.CorePayoutPreference) (usecases.CorePayoutPreference, error) {
			return c.S.PutCorePayoutPreference(c.Ctx, c.Subj, c.Version, proof)
		}).SteppedUp().Describe("If-Match selects the current preference version; 0 creates. This selects only an owned Verification-attested Core reference; it does not verify a bank or bypass Core cooling/withdrawal approvals.")
	pref.IfMatch = true
	pref.Upsert = true
	return []Route{
		Get("/financial-business/{business_id}/context", "getCoreBusinessContext", tag, "Read authoritative context for an existing business obligation", func(c *Call) (usecases.CoreBusinessContext, error) {
			return c.S.OwnedCoreBusiness(c.Ctx, c.Subj, c.Query("business_type"), c.ID("business_id"))
		}).Q(business),
		CommandPre("POST", "/financial-business/{business_id}/collections", "createCoreBusinessCollection", tag, "Collect an accepted Core quote for an existing business obligation", http.StatusOK,
			func(c *Call, in *usecases.CoreCollectionInput) (usecases.CoreCollectionEvidence, error) {
				return c.S.PrepareCoreCollection(c.Ctx, c.Subj, c.ID("business_id"), *in)
			},
			func(c *Call, _ *usecases.CoreCollectionInput, proof usecases.CoreCollectionEvidence) (payment.CollectionIntent, error) {
				return c.S.RecordCoreCollection(c.Ctx, c.Subj, c.ID("business_id"), proof)
			}).Describe("An existing signed Core quote must match the actual business payer, contract version, disclosed amount/currency and financial policy. No fabricated consent, default beneficiary or legacy translation. Unknown outcomes retain the original business key."),
		Get("/financial-business/{business_id}/receipt", "getCoreBusinessReceipt", tag, "Read current Core collection, settlement and allocation facts", func(c *Call) (payment.CoreReceipt, error) {
			return c.S.FinancialBusinessReceipt(c.Ctx, c.Subj, c.Query("business_type"), c.ID("business_id"))
		}).Q(business),
		CommandPre("POST", "/financial-business/{business_id}/reconcile", "reconcileCoreBusiness", tag, "Reconcile a binding through original Core readback", http.StatusOK,
			func(c *Call, in *usecases.CoreBusinessInput) (payment.CoreReceipt, error) {
				return c.S.ReconcileCoreBusiness(c.Ctx, c.Subj, in.BusinessType, c.ID("business_id"))
			},
			func(_ *Call, _ *usecases.CoreBusinessInput, proof payment.CoreReceipt) (payment.CoreReceipt, error) {
				return proof, nil
			}).Describe("Readback is serialized by an Owner observation lease, outside DB transactions. Collection alone never confirms paid/settled rent or a booking. Matched funding, exact allocation and reversals are checked before applying existing business projections."),
		CommandPre("POST", "/financial-business/{business_id}/allocations", "allocateCoreBusiness", tag, "Allocate to existing verified business beneficiaries", http.StatusOK,
			func(c *Call, in *usecases.CoreAllocationRequest) (payment.CoreAllocation, error) {
				return c.S.AllocateCoreBusiness(c.Ctx, c.Subj, c.ID("business_id"), *in, c.DownstreamKey("allocation"))
			},
			func(_ *Call, _ *usecases.CoreAllocationRequest, proof payment.CoreAllocation) (payment.CoreAllocation, error) {
				return proof, nil
			}).Describe("Core retains sole split authority. Owner checks original beneficiary records, verified ownership percentages, accepted provider/mandate and disclosed fee context before posting a protected allocation."),
		CommandPre("POST", "/financial-business/{business_id}/release-evidence", "submitCoreBusinessEvidence", tag, "Publish a recorded Owner milestone as Core evidence", http.StatusOK,
			func(c *Call, in *usecases.CoreEvidenceInput) (usecases.CoreRecorded, error) {
				e := c.S.SubmitCoreBusinessEvidence(c.Ctx, c.Subj, c.ID("business_id"), *in)
				return usecases.CoreRecorded{Recorded: e == nil}, e
			},
			func(_ *Call, _ *usecases.CoreEvidenceInput, proof usecases.CoreRecorded) (usecases.CoreRecorded, error) {
				return proof, nil
			}).Describe("Evidence reference/version is derived from completed authoritative Owner facts. Another issuer's or human approval cannot be supplied as Owner prose."),
		CommandPre("POST", "/financial-business/{business_id}/releases", "releaseCoreBusiness", tag, "Request an evidence-bound eligible beneficiary release", http.StatusOK,
			func(c *Call, in *usecases.CoreReleaseInput) (usecases.CoreJournal, error) {
				return c.S.ReleaseCoreBusiness(c.Ctx, c.Subj, c.ID("business_id"), *in, c.DownstreamKey("release"))
			},
			func(_ *Call, _ *usecases.CoreReleaseInput, proof usecases.CoreJournal) (usecases.CoreJournal, error) {
				return proof, nil
			}).Describe("No release of deposits, disputed/cancelled business or uncertified milestones. Core rechecks actual funding, current authority, holds, evidence and remaining amounts; repeat the same idempotency key."),
		CommandPre("POST", "/financial-business/{business_id}/cases", "openCoreBusinessCase", tag, "Submit an amount-specific dispute, cancellation or refund review", http.StatusOK,
			func(c *Call, in *usecases.CoreCaseInput) (payment.CoreCase, error) {
				return c.S.OpenCoreBusinessCase(c.Ctx, c.Subj, c.ID("business_id"), *in, c.DownstreamKey("case"))
			},
			func(c *Call, in *usecases.CoreCaseInput, proof payment.CoreCase) (payment.CoreCase, error) {
				raw, _ := json.Marshal(proof)
				binding, e := c.S.CoreBindingForOwner(c.Ctx, c.Subj, in.BusinessType, c.ID("business_id"))
				if e != nil {
					return proof, e
				}
				_, e = c.S.RecordCoreResource(c.Ctx, c.Subj, c.ID("business_id"), usecases.CoreResourceEvidence{Binding: binding, Type: "CASE", ID: proof.ID, State: proof.State, Snapshot: raw})
				return proof, e
			}).Describe("Owner claimant and authority come from the stored business. Itemized deposit deductions must already be recorded. Admin retains Core decisions, awards and refund execution; this is not a bank refund approval."),
		CommandPre("POST", "/financial-business/{business_id}/resource-links", "linkCoreBusinessResource", tag, "Link a matching authoritative deposit, case or approved refund", http.StatusOK,
			func(c *Call, in *usecases.CoreResourceInput) (usecases.CoreResourceEvidence, error) {
				return c.S.PrepareCoreResource(c.Ctx, c.Subj, c.ID("business_id"), *in)
			},
			func(c *Call, _ *usecases.CoreResourceInput, proof usecases.CoreResourceEvidence) (usecases.CoreResourceView, error) {
				proof, e := c.S.RecordCoreResource(c.Ctx, c.Subj, c.ID("business_id"), proof)
				return usecases.CoreResourceView{Type: proof.Type, ID: proof.ID, State: proof.State, Proof: proof.Snapshot}, e
			}).Describe("Signed readback validates original business, intent, payer, line, provider/pool and currency. A pending or unknown refund remains pending; amounts are not incremented on duplicate events."),
		CommandPre("POST", "/financial-business/{business_id}/cancellations", "cancelCoreBusinessIntent", tag, "Request a contract-supported cancellation of an ended obligation", http.StatusOK,
			func(c *Call, in *usecases.CoreCancelInput) (payment.CollectionIntent, error) {
				return c.S.CancelCoreBusinessIntent(c.Ctx, c.Subj, in.BusinessType, c.ID("business_id"), in.EvidenceIDs, c.DownstreamKey("cancel"))
			},
			func(_ *Call, _ *usecases.CoreCancelInput, proof payment.CollectionIntent) (payment.CollectionIntent, error) {
				return proof, nil
			}).Describe("An initialized, collected or unknown external charge is never treated as cancelled: Core requires original readback or an approved case/refund instead."),
		Get("/financial-wallet/statement", "getCoreFinancialStatement", tag, "Read my authoritative financial ledger entries", func(c *Call) (usecases.CoreStatement, error) {
			after, e := c.QueryID("after")
			if e != nil {
				return usecases.CoreStatement{}, e
			}
			return c.S.CoreFinancialStatement(c.Ctx, c.Subj, after, 50)
		}).Q(Param{Name: "after", Doc: "Last Core entry UUID; forward pagination."}).Describe("Signed owned-wallet history. No synthetic received revenue, fabricated PDF/download reference or direct payment_db query."),
		Get("/financial-wallet/payout-references", "getCorePayoutReferences", tag, "Read my current verified Core payout references", func(c *Call) ([]payment.CorePayoutReference, error) { return c.S.CorePayoutReferences(c.Ctx, c.Subj) }),
		Get("/financial-wallet/availability/{pool_id}", "getCoreFinancialAvailability", tag, "Read released, protected, reserved and cooling/rail availability", func(c *Call) (payment.CoreAvailability, error) {
			return c.S.CoreAvailableBalance(c.Ctx, c.Subj, c.ID("pool_id"))
		}),
		Get("/financial-wallet/payout-preference", "getCorePayoutPreference", tag, "Read my selected Core payout reference", func(c *Call) (usecases.CorePayoutPreference, error) {
			return c.S.GetCorePayoutPreference(c.Ctx, c.Subj)
		}),
		pref,
		CommandPre("POST", "/financial-wallet/withdrawals", "withdrawCoreEarnings", tag, "Request an already approved withdrawal of released earnings", http.StatusOK,
			func(c *Call, in *payment.CoreWithdrawal) (payment.CorePayout, error) {
				return c.S.WithdrawCoreEarnings(c.Ctx, c.Subj, *in, c.DownstreamKey("withdrawal"))
			},
			func(_ *Call, _ *payment.CoreWithdrawal, proof payment.CorePayout) (payment.CorePayout, error) {
				return proof, nil
			}).SteppedUp().Describe("Owned current wallet, approved line/amount/destination, active verification, cooling and provider policy are required. No customer cash-out, protected-money withdrawal or self-approved payout."),
		Get("/financial-wallet/withdrawals/{payout_id}", "getCorePayout", tag, "Read my original payout outcome", func(c *Call) (payment.CorePayout, error) {
			return c.S.ReadCorePayout(c.Ctx, c.Subj, c.ID("payout_id"), false)
		}),
		CommandPre("POST", "/financial-wallet/withdrawals/{payout_id}/readback", "readbackCorePayout", tag, "Read back the original provider payout without resubmitting", http.StatusOK,
			func(c *Call, _ *Empty) (payment.CorePayout, error) {
				return c.S.ReadCorePayout(c.Ctx, c.Subj, c.ID("payout_id"), true)
			},
			func(_ *Call, _ *Empty, proof payment.CorePayout) (payment.CorePayout, error) { return proof, nil }),
	}
}
