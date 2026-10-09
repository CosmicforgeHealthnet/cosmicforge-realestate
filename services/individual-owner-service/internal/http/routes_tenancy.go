package http

import (
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

const (
	tagAgreements = "Agreements"
	tagTenancy    = "Tenancy"
	tagUtilities  = "Utilities & Service Charges"
)

type mediaRefs interface{ MediaRefs() []ids.ID }

type customerMediaRefs interface{ CustomerMediaRefs() []ids.ID }

var (
	docClasses      = []string{"DOCUMENT"}
	scanClasses     = []string{"DOCUMENT", "IMAGE"}
	evidenceClasses = []string{"IMAGE", "VIDEO", "DOCUMENT"}
)

// ownerEvidence is the pre-transaction media check of an owner command.
func ownerEvidence[Req mediaRefs](scope, param string, action authz.Action, classes []string) func(c *Call, in *Req) (usecases.Evidence, error) {
	return func(c *Call, in *Req) (usecases.Evidence, error) {
		return c.S.CheckMediaEvidence(c.Ctx, c.Subj, scope, c.ID(param), action, (*in).MediaRefs(), classes...)
	}
}

// customerEvidence is the pre-transaction media check of a marketplace call.
func customerEvidence[Req customerMediaRefs](classes []string) func(c *Call, in *Req) (usecases.Evidence, error) {
	return func(c *Call, in *Req) (usecases.Evidence, error) {
		return c.S.CheckCustomerEvidence(c.Ctx, c.Subj, (*in).CustomerMediaRefs(), classes...)
	}
}

func agreementRoutes() []Route {
	aid := func(c *Call) ids.ID { return c.ID("agreement_id") }
	manage := authz.Do(authz.CapAgreementsManage)
	return []Route{
		List("/agreement-templates", "listAgreementTemplates", tagAgreements, "Legal agreement templates (controlled configuration)",
			func(c *Call, _ Page) (pagination.Response[usecases.AgreementTemplateView], error) {
				return slice(c.S.ListAgreementTemplates(c.Ctx, c.Subj, c.Query("country"), c.Query("agreement_kind")))
			}).Q(Param{Name: "country"}, Param{Name: "agreement_kind", Enum: []string{"RESIDENTIAL_LEASE", "COMMERCIAL_LEASE", "LAND_LEASE", "RENEWAL_ADDENDUM"}}),
		List("/notice-templates", "listNoticeTemplates", tagAgreements, "Legal notice templates (controlled configuration)",
			func(c *Call, _ Page) (pagination.Response[usecases.NoticeTemplateView], error) {
				return slice(c.S.ListNoticeTemplates(c.Ctx, c.Subj, c.Query("country"), c.Query("notice_type")))
			}).Q(Param{Name: "country"}, Param{Name: "notice_type"}),
		List("/agreements", "listAgreements", tagAgreements, "Agreements across my properties",
			func(c *Call, p Page) (pagination.Response[usecases.AgreementView], error) {
				pid, err := c.QueryID("property_id")
				if err != nil {
					return pagination.Response[usecases.AgreementView]{}, err
				}
				return c.S.ListAgreements(c.Ctx, c.Subj, pid, c.Query("status"), p.Cursor, p.Limit)
			}).Q(Param{Name: "property_id"}, Param{Name: "status", Enum: []string{"DRAFT", "REVIEW", "SIGNING", "FULLY_SIGNED", "EFFECTIVE", "CANCELLED"}}),
		CommandPre("POST", "/agreements", "createAgreement", tagAgreements, "Draft a direct agreement (e.g. an existing tenant)", http.StatusCreated,
			func(c *Call, in *usecases.CreateAgreementInput) (usecases.Evidence, error) {
				return c.S.CheckMediaEvidence(c.Ctx, c.Subj, usecases.ScopeProperty, in.PropertyID, manage, in.MediaRefs(), docClasses...)
			},
			func(c *Call, in *usecases.CreateAgreementInput, ev usecases.Evidence) (usecases.AgreementView, error) {
				return c.S.CreateAgreement(c.Ctx, c.Subj, *in, ev)
			}).
			Describe("Agreements for accepted rental offers are prepared automatically. PLATFORM rent/deposit collection needs payment-service (503 until configured).").
			Capture("agreement_id", "data.id").Capture("agreement_version", "data.version").Errs(422, 503),
		Get("/agreements/{agreement_id}", "getAgreement", tagAgreements, "Agreement with current version, parties and signatures",
			func(c *Call) (usecases.AgreementView, error) { return c.S.GetAgreement(c.Ctx, c.Subj, aid(c)) }),
		Update("PATCH", "/agreements/{agreement_id}", "updateAgreementDraft", tagAgreements, "Record the next draft version (If-Match)", false,
			func(c *Call, in *usecases.AgreementDraftInput) (usecases.AgreementView, error) {
				ev, err := c.S.CheckMediaEvidence(c.Ctx, c.Subj, usecases.ScopeAgreement, aid(c), manage, in.MediaRefs(), docClasses...)
				if err != nil {
					return usecases.AgreementView{}, err
				}
				return c.S.UpdateAgreementDraft(c.Ctx, c.Subj, aid(c), c.Version, *in, ev)
			}).Describe("Versions are immutable: every change is a new version; impossible once under review or signing.").Errs(409, 422),
		List("/agreements/{agreement_id}/versions", "listAgreementVersions", tagAgreements, "Immutable version history",
			func(c *Call, _ Page) (pagination.Response[usecases.AgreementVersionView], error) {
				return slice(c.S.ListAgreementVersions(c.Ctx, c.Subj, aid(c)))
			}),
		Command("POST", "/agreements/{agreement_id}/invite-signers", "inviteAgreementSigners", tagAgreements, "Set the agreement's parties and signers", http.StatusOK,
			func(c *Call, in *usecases.InviteSignersInput) (usecases.AgreementView, error) {
				return c.S.SetAgreementParticipants(c.Ctx, c.Subj, aid(c), *in)
			}).Errs(409, 422),
		Command("POST", "/agreements/{agreement_id}/submit-review", "submitAgreementReview", tagAgreements, "Share the draft with the counterparty", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.AgreementView, error) {
				return c.S.SubmitAgreementForReview(c.Ctx, c.Subj, aid(c))
			}).
			Describe("Needs an ACTIVE template, the filled-in document, valid terms with every required clause, and owner-side + tenant signers.").Errs(422),
		Command("POST", "/agreements/{agreement_id}/reopen", "reopenAgreementDraft", tagAgreements, "Return a reviewed agreement to draft", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.AgreementView, error) {
				return c.S.ReturnAgreementToDraft(c.Ctx, c.Subj, aid(c))
			}),
		Command("POST", "/agreements/{agreement_id}/send-for-signature", "sendAgreementForSignature", tagAgreements, "Invite signatures on the current version", http.StatusOK,
			func(c *Call, in *usecases.SendForSignatureInput) (usecases.AgreementView, error) {
				return c.S.SendForSignature(c.Ctx, c.Subj, aid(c), *in)
			}).
			Describe("Requires verified ownership authority and co-owner consent (LEASE). ESIGN answers 503 until an e-sign provider is configured; envelopes are created and read back by the signature-reconciliation worker.").
			Errs(409, 422, 503),
		CommandPre("POST", "/agreements/{agreement_id}/signatures", "recordOfflineSignature", tagAgreements, "Attach a signature collected offline", http.StatusOK,
			ownerEvidence[usecases.OfflineSignatureInput](usecases.ScopeAgreement, "agreement_id", manage, scanClasses),
			func(c *Call, in *usecases.OfflineSignatureInput, ev usecases.Evidence) (usecases.AgreementView, error) {
				return c.S.RecordOfflineSignature(c.Ctx, c.Subj, aid(c), *in, ev)
			}).Describe("Owner-side and off-platform parties are signed on owner attestation with the scan; marketplace customers must confirm.").Errs(409, 422),
		CommandPre("POST", "/agreements/{agreement_id}/cancel", "cancelAgreement", tagAgreements, "Cancel a pre-effective agreement", http.StatusOK,
			func(c *Call, _ *usecases.ReasonCodeInput) (struct{}, error) {
				return c.S.VoidEnvelopeBeforeCancel(c.Ctx, c.Subj, aid(c))
			},
			func(c *Call, in *usecases.ReasonCodeInput, _ struct{}) (usecases.AgreementView, error) {
				return c.S.CancelAgreement(c.Ctx, c.Subj, aid(c), *in)
			}),
		Command("POST", "/agreements/{agreement_id}/activate", "activateAgreement", tagAgreements, "Make a fully signed agreement effective", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.AgreementView, error) {
				return c.S.ActivateAgreement(c.Ctx, c.Subj, aid(c))
			}).
			Describe("A lease creates the tenancy (PENDING_MOVE_IN) with its rent schedule and deposit obligation; a renewal addendum updates the tenancy.").Errs(409, 503),
	}
}

func tenancyRoutes() []Route {
	tid := func(c *Call) ids.ID { return c.ID("tenancy_id") }
	manage := authz.Do(authz.CapTenancyManage)
	rs := []Route{
		List("/tenancies", "listTenancies", tagTenancy, "Tenancies across my properties",
			func(c *Call, p Page) (pagination.Response[usecases.TenancyView], error) {
				pid, err := c.QueryID("property_id")
				if err != nil {
					return pagination.Response[usecases.TenancyView]{}, err
				}
				return c.S.ListTenancies(c.Ctx, c.Subj, pid, c.Query("status"), p.Cursor, p.Limit)
			}).Q(Param{Name: "property_id"}, Param{Name: "status", Enum: []string{"PENDING_MOVE_IN", "ACTIVE", "RENEWAL_PENDING", "TERMINATION_PENDING", "MOVE_OUT_PENDING", "ENDED"}}),
		Get("/tenancies/{tenancy_id}", "getTenancy", tagTenancy, "Tenancy with outstanding balance, deposit and checklists",
			func(c *Call) (usecases.TenancyView, error) { return c.S.GetTenancy(c.Ctx, c.Subj, tid(c)) }),
		Command("POST", "/tenancies/{tenancy_id}/move-in", "moveIn", tagTenancy, "Confirm move-in (tenancy becomes ACTIVE)", http.StatusOK,
			func(c *Call, in *usecases.MoveInInput) (usecases.TenancyView, error) {
				return c.S.MoveIn(c.Ctx, c.Subj, tid(c), *in)
			}).
			Describe("PLATFORM collection: the first rent and the deposit must be confirmed by payment-service. Emits tenancy.started.v1; the listing becomes OCCUPIED.").Errs(409, 422),
		Command("POST", "/tenancies/{tenancy_id}/cancel", "cancelTenancy", tagTenancy, "Cancel a tenancy before move-in (no money received)", http.StatusOK,
			func(c *Call, in *usecases.ReasonCodeInput) (usecases.TenancyView, error) {
				return c.S.CancelTenancyBeforeMoveIn(c.Ctx, c.Subj, tid(c), *in)
			}).Errs(409),
		List("/tenancies/{tenancy_id}/rent-obligations", "listRentObligations", tagTenancy, "Rent obligations (payment-service truth)",
			func(c *Call, _ Page) (pagination.Response[usecases.RentObligationView], error) {
				return slice(c.S.ListRentObligations(c.Ctx, c.Subj, tid(c), c.Query("status")))
			}).Q(Param{Name: "status"}),
		Get("/rent-obligations/{obligation_id}", "getRentObligation", tagTenancy, "Obligation with its projected status history",
			func(c *Call) (usecases.RentObligationView, error) {
				return c.S.GetRentObligation(c.Ctx, c.Subj, c.ID("obligation_id"))
			}),
		Command("POST", "/rent-obligations/{obligation_id}/decision", "decideRentObligation", tagTenancy, "Waive, dispute or reinstate an obligation", http.StatusOK,
			func(c *Call, in *usecases.ObligationDecisionInput) (usecases.RentObligationView, error) {
				return c.S.DecideObligation(c.Ctx, c.Subj, c.ID("obligation_id"), *in)
			}).Describe("Owners never mark money as received: payment states come only from payment-service.").Errs(409),
		Get("/tenancies/{tenancy_id}/deposit", "getDeposit", tagTenancy, "Deposit obligation",
			func(c *Call) (usecases.DepositView, error) { return c.S.GetDeposit(c.Ctx, c.Subj, tid(c)) }),
		CommandPre("POST", "/tenancies/{tenancy_id}/deposit/settlement", "proposeDepositSettlement", tagTenancy, "Propose the deposit settlement", http.StatusOK,
			ownerEvidence[usecases.DepositSettlementInput](usecases.ScopeTenancy, "tenancy_id", manage, evidenceClasses),
			func(c *Call, in *usecases.DepositSettlementInput, ev usecases.Evidence) (usecases.DepositView, error) {
				return c.S.ProposeDepositSettlement(c.Ctx, c.Subj, tid(c), *in, ev)
			}).Describe("After the move-out inspection: full release, or itemized deductions with evidence for the tenant to accept or dispute.").Errs(409, 422),
		Command("POST", "/tenancies/{tenancy_id}/deposit/concede", "concedeDepositDispute", tagTenancy, "Give up the disputed deductions", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.DepositView, error) {
				return c.S.ConcedeDepositDispute(c.Ctx, c.Subj, tid(c))
			}).Errs(409),
		List("/tenancies/{tenancy_id}/keys", "listKeyItems", tagTenancy, "Key and access inventory",
			func(c *Call, _ Page) (pagination.Response[usecases.KeyItemView], error) {
				return slice(c.S.ListKeyItems(c.Ctx, c.Subj, tid(c)))
			}),
		Command("POST", "/tenancies/{tenancy_id}/keys", "addKeyItem", tagTenancy, "Record keys/cards/codes handed over", http.StatusCreated,
			func(c *Call, in *usecases.KeyItemInput) (usecases.KeyItemView, error) {
				return c.S.AddKeyItem(c.Ctx, c.Subj, tid(c), *in)
			}).
			Describe("Codes and smart-access credentials are never stored, only references.").Capture("key_item_id", "data.id"),
		Command("POST", "/tenancies/{tenancy_id}/keys/{key_item_id}/return", "returnKeyItem", tagTenancy, "Record a key/access item return", http.StatusOK,
			func(c *Call, in *usecases.KeyReturnInput) (usecases.KeyItemView, error) {
				return c.S.ReturnKeyItem(c.Ctx, c.Subj, tid(c), c.ID("key_item_id"), *in)
			}).Errs(409),
		List("/tenancies/{tenancy_id}/renewals", "listRenewals", tagTenancy, "Renewal cases",
			func(c *Call, _ Page) (pagination.Response[usecases.RenewalView], error) {
				return slice(c.S.ListRenewals(c.Ctx, c.Subj, tid(c)))
			}),
		Command("POST", "/tenancies/{tenancy_id}/renewal", "proposeRenewal", tagTenancy, "Propose a renewal", http.StatusCreated,
			func(c *Call, in *usecases.RenewalTermsInput) (usecases.RenewalView, error) {
				return c.S.ProposeRenewal(c.Ctx, c.Subj, tid(c), *in)
			}).
			Describe("Rent changes need an explanation; optional advisory market context is shown to the tenant.").Capture("renewal_id", "data.id").Errs(409, 422),
		Command("POST", "/renewals/{renewal_id}/response", "respondRenewal", tagTenancy, "Accept, counter, decline or withdraw", http.StatusOK,
			func(c *Call, in *usecases.RenewalResponse) (usecases.RenewalView, error) {
				return c.S.RespondRenewal(c.Ctx, c.Subj, c.ID("renewal_id"), *in)
			}).Describe("Accepted terms generate a RENEWAL_ADDENDUM agreement to sign.").Errs(409),
		List("/tenancies/{tenancy_id}/notices", "listNotices", tagTenancy, "Notices",
			func(c *Call, _ Page) (pagination.Response[usecases.NoticeView], error) {
				return slice(c.S.ListNotices(c.Ctx, c.Subj, tid(c)))
			}),
		CommandPre("POST", "/tenancies/{tenancy_id}/notices", "createNotice", tagTenancy, "Draft a notice", http.StatusCreated,
			ownerEvidence[usecases.NoticeInput](usecases.ScopeTenancy, "tenancy_id", authz.Do(authz.CapNoticesIssue), docClasses),
			func(c *Call, in *usecases.NoticeInput, ev usecases.Evidence) (usecases.NoticeView, error) {
				return c.S.CreateNotice(c.Ctx, c.Subj, tid(c), *in, ev)
			}).Capture("notice_id", "data.id").Errs(422),
		Command("POST", "/notices/{notice_id}/issue", "issueNotice", tagTenancy, "Serve a notice (fixes the legal effective date)", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.NoticeView, error) {
				return c.S.IssueNotice(c.Ctx, c.Subj, c.ID("notice_id"))
			}).
			Describe("Effective date = later of the requested date and issue date + the template's minimum notice; acknowledgement never moves it. A TERMINATION notice moves the tenancy to TERMINATION_PENDING.").Errs(409, 422),
		CommandPre("POST", "/notices/{notice_id}/delivery", "recordNoticeDelivery", tagTenancy, "Record service of an off-platform notice", http.StatusOK,
			ownerEvidence[usecases.NoticeDeliveryInput](usecases.ScopeNotice, "notice_id", authz.Do(authz.CapNoticesIssue), scanClasses),
			func(c *Call, in *usecases.NoticeDeliveryInput, ev usecases.Evidence) (usecases.NoticeView, error) {
				return c.S.RecordNoticeDelivery(c.Ctx, c.Subj, c.ID("notice_id"), *in, ev)
			}).Errs(409, 422),
		Command("POST", "/notices/{notice_id}/acknowledge", "acknowledgeTenantNotice", tagTenancy, "Acknowledge a notice given by the tenant", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.NoticeView, error) {
				return c.S.AcknowledgeTenantNotice(c.Ctx, c.Subj, c.ID("notice_id"))
			}).Errs(409),
		Get("/tenancies/{tenancy_id}/move-out", "getMoveOut", tagTenancy, "Move-out case",
			func(c *Call) (usecases.MoveOutView, error) { return c.S.GetMoveOut(c.Ctx, c.Subj, tid(c)) }),
		Command("POST", "/tenancies/{tenancy_id}/move-out", "moveOut", tagTenancy, "Start or advance the move-out", http.StatusOK,
			func(c *Call, in *usecases.MoveOutInput) (usecases.MoveOutView, error) {
				return c.S.MoveOut(c.Ctx, c.Subj, tid(c), *in)
			}).
			Describe("COMPLETE needs the inspection, the deposit settled by payment-service (a dispute only freezes the disputed amount) and the owner's return-to-market confirmation. Emits tenancy.ended.v1.").
			Errs(409, 422),
	}
	for _, ph := range []struct{ slug, phase, op string }{{"move-in-checklist", "MOVE_IN", "MoveIn"}, {"move-out-checklist", "MOVE_OUT", "MoveOut"}} {
		ph := ph
		rs = append(rs,
			Get("/tenancies/{tenancy_id}/"+ph.slug, "get"+ph.op+"Checklist", tagTenancy, "Condition checklist ("+ph.phase+")",
				func(c *Call) (usecases.ChecklistView, error) {
					return c.S.GetChecklist(c.Ctx, c.Subj, tid(c), ph.phase)
				}),
			CommandPre("POST", "/tenancies/{tenancy_id}/"+ph.slug+"/items", "add"+ph.op+"ConditionItems", tagTenancy, "Record condition entries ("+ph.phase+")", http.StatusOK,
				ownerEvidence[usecases.ConditionItemsInput](usecases.ScopeTenancy, "tenancy_id", manage, evidenceClasses),
				func(c *Call, in *usecases.ConditionItemsInput, ev usecases.Evidence) (usecases.ChecklistView, error) {
					return c.S.AddConditionItems(c.Ctx, c.Subj, tid(c), ph.phase, *in, ev)
				}).Describe("Entries are append-only; after both acknowledgements new entries are addenda.").Errs(409),
			CommandPre("POST", "/tenancies/{tenancy_id}/"+ph.slug+"/submit", "submit"+ph.op+"Checklist", tagTenancy, "Owner acknowledgement ("+ph.phase+")", http.StatusOK,
				ownerEvidence[usecases.SubmitChecklistInput](usecases.ScopeTenancy, "tenancy_id", manage, scanClasses),
				func(c *Call, in *usecases.SubmitChecklistInput, ev usecases.Evidence) (usecases.ChecklistView, error) {
					return c.S.SubmitChecklist(c.Ctx, c.Subj, tid(c), ph.phase, *in, ev)
				}).Errs(409, 422),
		)
	}
	return rs
}

func utilityRoutes() []Route {
	pid := func(c *Call) ids.ID { return c.ID("property_id") }
	manage := authz.Do(authz.CapUtilitiesManage)
	return []Route{
		List("/properties/{property_id}/utilities", "listUtilityAccounts", tagUtilities, "Utility accounts",
			func(c *Call, _ Page) (pagination.Response[usecases.UtilityAccountView], error) {
				return slice(c.S.ListUtilityAccounts(c.Ctx, c.Subj, pid(c)))
			}),
		Command("POST", "/properties/{property_id}/utilities", "createUtilityAccount", tagUtilities, "Add a utility account", http.StatusCreated,
			func(c *Call, in *usecases.UtilityAccountInput) (usecases.UtilityAccountView, error) {
				return c.S.CreateUtilityAccount(c.Ctx, c.Subj, pid(c), *in)
			}).Capture("utility_account_id", "data.id").Capture("utility_account_version", "data.version"),
		Update("PATCH", "/utilities/{utility_account_id}", "updateUtilityAccount", tagUtilities, "Update a utility account (If-Match)", false,
			func(c *Call, in *usecases.UtilityAccountInput) (usecases.UtilityAccountView, error) {
				return c.S.UpdateUtilityAccount(c.Ctx, c.Subj, c.ID("utility_account_id"), c.Version, *in)
			}),
		Command("POST", "/utilities/{utility_account_id}/allocation-rule", "setAllocationRule", tagUtilities, "Version a shared-utility allocation rule", http.StatusOK,
			func(c *Call, in *usecases.AllocationRuleInput) (usecases.AllocationRuleView, error) {
				return c.S.SetAllocationRule(c.Ctx, c.Subj, c.ID("utility_account_id"), *in)
			}),
		List("/properties/{property_id}/meter-readings", "listMeterReadings", tagUtilities, "Meter readings",
			func(c *Call, _ Page) (pagination.Response[usecases.MeterReadingView], error) {
				acc, err := c.QueryID("utility_account_id")
				if err != nil {
					return pagination.Response[usecases.MeterReadingView]{}, err
				}
				return slice(c.S.ListMeterReadings(c.Ctx, c.Subj, pid(c), acc))
			}).Q(Param{Name: "utility_account_id"}),
		CommandPre("POST", "/properties/{property_id}/meter-readings", "recordMeterReading", tagUtilities, "Record a meter reading", http.StatusCreated,
			ownerEvidence[usecases.MeterReadingInput](usecases.ScopeProperty, "property_id", manage, []string{"IMAGE"}),
			func(c *Call, in *usecases.MeterReadingInput, ev usecases.Evidence) (usecases.MeterReadingView, error) {
				return c.S.RecordMeterReading(c.Ctx, c.Subj, pid(c), *in, ev)
			}).Describe("Readings are append-only and attributed; corrections reference the corrected reading.").Errs(422),
		List("/properties/{property_id}/utility-bills", "listUtilityBills", tagUtilities, "Utility bills",
			func(c *Call, _ Page) (pagination.Response[usecases.UtilityBillView], error) {
				acc, err := c.QueryID("utility_account_id")
				if err != nil {
					return pagination.Response[usecases.UtilityBillView]{}, err
				}
				return slice(c.S.ListUtilityBills(c.Ctx, c.Subj, pid(c), acc))
			}).Q(Param{Name: "utility_account_id"}),
		CommandPre("POST", "/properties/{property_id}/utility-bills", "recordUtilityBill", tagUtilities, "Record a utility bill", http.StatusCreated,
			ownerEvidence[usecases.UtilityBillInput](usecases.ScopeProperty, "property_id", manage, scanClasses),
			func(c *Call, in *usecases.UtilityBillInput, ev usecases.Evidence) (usecases.UtilityBillView, error) {
				return c.S.RecordUtilityBill(c.Ctx, c.Subj, pid(c), *in, ev)
			}).Capture("utility_bill_id", "data.id").Errs(409),
		Update("POST", "/utility-bills/{utility_bill_id}/action", "actOnUtilityBill", tagUtilities, "Allocate, dispute or resolve a bill (If-Match)", false,
			func(c *Call, in *usecases.BillActionInput) (usecases.UtilityBillView, error) {
				return c.S.ActOnUtilityBill(c.Ctx, c.Subj, c.ID("utility_bill_id"), c.Version, *in)
			}).Describe("A disputed allocation keeps its original calculation; resolution re-allocates with the current rule version.").Errs(409, 422),
		List("/properties/{property_id}/service-charge-budgets", "listServiceChargeBudgets", tagUtilities, "Service-charge budgets",
			func(c *Call, _ Page) (pagination.Response[usecases.BudgetView], error) {
				return slice(c.S.ListBudgets(c.Ctx, c.Subj, pid(c)))
			}),
		Command("POST", "/properties/{property_id}/service-charge-budgets", "createServiceChargeBudget", tagUtilities, "Draft a service-charge budget", http.StatusCreated,
			func(c *Call, in *usecases.BudgetInput) (usecases.BudgetView, error) {
				return c.S.CreateBudget(c.Ctx, c.Subj, pid(c), *in)
			}).
			Capture("budget_id", "data.id").Capture("budget_version", "data.version").Errs(409),
		Get("/service-charge-budgets/{budget_id}", "getServiceChargeBudget", tagUtilities, "Budget with expected vs recorded amounts",
			func(c *Call) (usecases.BudgetView, error) { return c.S.GetBudget(c.Ctx, c.Subj, c.ID("budget_id")) }),
		Update("PATCH", "/service-charge-budgets/{budget_id}", "updateServiceChargeBudget", tagUtilities, "Edit a draft or approve/close a budget (If-Match)", false,
			func(c *Call, in *usecases.BudgetInput) (usecases.BudgetView, error) {
				return c.S.UpdateBudget(c.Ctx, c.Subj, c.ID("budget_id"), c.Version, *in)
			}).Errs(409),
		List("/service-charge-budgets/{budget_id}/items", "listServiceChargeItems", tagUtilities, "Service-charge items",
			func(c *Call, _ Page) (pagination.Response[usecases.ServiceChargeItemView], error) {
				return slice(c.S.ListServiceChargeItems(c.Ctx, c.Subj, c.ID("budget_id")))
			}),
		CommandPre("POST", "/service-charge-budgets/{budget_id}/items", "addServiceChargeItem", tagUtilities, "Record an actual, billed, paid or adjustment item", http.StatusCreated,
			ownerEvidence[usecases.ServiceChargeItemInput](usecases.ScopeBudget, "budget_id", authz.Do(authz.CapServiceCharges), scanClasses),
			func(c *Call, in *usecases.ServiceChargeItemInput, ev usecases.Evidence) (usecases.ServiceChargeItemView, error) {
				return c.S.AddServiceChargeItem(c.Ctx, c.Subj, c.ID("budget_id"), *in, ev)
			}).Describe("Items are append-only; corrections are ADJUSTMENT items referencing the corrected item. Not a payment ledger.").Errs(409, 422),
	}
}

// tenancyInternalRoutes are marketplace (customer actor) and admin contracts.
func tenancyInternalRoutes() []Route {
	return []Route{
		Get("/agreements/{agreement_id}/customer-view", "customerGetAgreement", tagInternal, "Agreement as seen by a party (marketplace)",
			func(c *Call) (usecases.AgreementView, error) {
				return c.S.CustomerGetAgreement(c.Ctx, c.Subj, c.ID("agreement_id"))
			}).Internal(),
		Command("POST", "/agreements/{agreement_id}/customer-action", "customerAgreementAction", tagInternal, "Counterparty review or signature response", http.StatusOK,
			func(c *Call, in *usecases.CustomerAgreementAction) (usecases.AgreementView, error) {
				return c.S.CustomerActOnAgreement(c.Ctx, c.Subj, c.ID("agreement_id"), *in)
			}).Internal().Errs(409),
		List("/customer-tenancies", "customerListTenancies", tagInternal, "The customer's tenancies",
			func(c *Call, _ Page) (pagination.Response[usecases.TenancyView], error) {
				return slice(c.S.CustomerListTenancies(c.Ctx, c.Subj))
			}).Internal(),
		Get("/tenancies/{tenancy_id}/customer-view", "customerGetTenancy", tagInternal, "Tenancy as seen by the tenant",
			func(c *Call) (usecases.TenancyView, error) {
				return c.S.CustomerGetTenancy(c.Ctx, c.Subj, c.ID("tenancy_id"))
			}).Internal(),
		CommandPre("POST", "/tenancies/{tenancy_id}/move-in-checklist/customer-response", "customerRespondMoveInChecklist", tagInternal, "Tenant acknowledges or disputes the move-in checklist", http.StatusOK,
			customerEvidence[usecases.CustomerChecklistInput](evidenceClasses),
			func(c *Call, in *usecases.CustomerChecklistInput, ev usecases.Evidence) (usecases.ChecklistView, error) {
				return c.S.CustomerRespondChecklist(c.Ctx, c.Subj, c.ID("tenancy_id"), "MOVE_IN", *in, ev)
			}).Internal().Errs(409),
		CommandPre("POST", "/tenancies/{tenancy_id}/move-out-checklist/customer-response", "customerRespondMoveOutChecklist", tagInternal, "Tenant acknowledges or disputes the move-out checklist", http.StatusOK,
			customerEvidence[usecases.CustomerChecklistInput](evidenceClasses),
			func(c *Call, in *usecases.CustomerChecklistInput, ev usecases.Evidence) (usecases.ChecklistView, error) {
				return c.S.CustomerRespondChecklist(c.Ctx, c.Subj, c.ID("tenancy_id"), "MOVE_OUT", *in, ev)
			}).Internal().Errs(409),
		Command("POST", "/renewals/{renewal_id}/customer-response", "customerRespondRenewal", tagInternal, "Tenant accepts, counters or declines a renewal", http.StatusOK,
			func(c *Call, in *usecases.RenewalResponse) (usecases.RenewalView, error) {
				return c.S.CustomerRespondRenewal(c.Ctx, c.Subj, c.ID("renewal_id"), *in)
			}).Internal().Errs(409),
		Command("POST", "/tenancies/{tenancy_id}/customer-notices", "customerGiveNotice", tagInternal, "Tenant gives notice", http.StatusCreated,
			func(c *Call, in *usecases.CustomerNoticeInput) (usecases.NoticeView, error) {
				return c.S.CustomerGiveNotice(c.Ctx, c.Subj, c.ID("tenancy_id"), *in)
			}).Internal().Errs(409),
		Command("POST", "/notices/{notice_id}/customer-action", "customerNoticeAction", tagInternal, "Notice shown to, acknowledged or disputed by the tenant", http.StatusOK,
			func(c *Call, in *usecases.CustomerNoticeAction) (usecases.NoticeView, error) {
				return c.S.CustomerActOnNotice(c.Ctx, c.Subj, c.ID("notice_id"), *in)
			}).Internal().Errs(409),
		Command("POST", "/tenancies/{tenancy_id}/deposit/customer-response", "customerRespondDeposit", tagInternal, "Tenant accepts or disputes deposit deductions", http.StatusOK,
			func(c *Call, in *usecases.CustomerDepositResponse) (usecases.DepositView, error) {
				return c.S.CustomerRespondDeposit(c.Ctx, c.Subj, c.ID("tenancy_id"), *in)
			}).Internal().Errs(409),
		CommandPre("POST", "/tenancies/{tenancy_id}/customer-meter-readings", "customerRecordMeterReading", tagInternal, "Tenant submits a meter reading", http.StatusCreated,
			customerEvidence[usecases.MeterReadingInput]([]string{"IMAGE"}),
			func(c *Call, in *usecases.MeterReadingInput, ev usecases.Evidence) (usecases.MeterReadingView, error) {
				return c.S.CustomerRecordMeterReading(c.Ctx, c.Subj, c.ID("tenancy_id"), *in, ev)
			}).Internal().Errs(422),
		// Admin contracts (admin-service with an admin actor).
		List("/admin/agreement-templates", "adminListAgreementTemplates", tagAdmin, "All agreement template versions",
			func(c *Call, _ Page) (pagination.Response[usecases.AgreementTemplateView], error) {
				return slice(c.S.ListAgreementTemplates(c.Ctx, c.Subj, c.Query("country"), c.Query("agreement_kind")))
			}).Internal().Q(Param{Name: "country"}, Param{Name: "agreement_kind"}),
		Command("POST", "/admin/agreement-templates", "adminPublishAgreementTemplate", tagAdmin, "Publish the next agreement template version", http.StatusCreated,
			func(c *Call, in *usecases.AgreementTemplateInput) (usecases.AgreementTemplateView, error) {
				return c.S.AdminPublishAgreementTemplate(c.Ctx, c.Subj, *in)
			}).Internal().Capture("agreement_template_id", "data.id"),
		Command("POST", "/admin/agreement-templates/{template_id}/retire", "adminRetireAgreementTemplate", tagAdmin, "Retire an agreement template version", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.AgreementTemplateView, error) {
				return c.S.AdminRetireAgreementTemplate(c.Ctx, c.Subj, c.ID("template_id"))
			}).Internal(),
		List("/admin/notice-templates", "adminListNoticeTemplates", tagAdmin, "All notice template versions",
			func(c *Call, _ Page) (pagination.Response[usecases.NoticeTemplateView], error) {
				return slice(c.S.ListNoticeTemplates(c.Ctx, c.Subj, c.Query("country"), c.Query("notice_type")))
			}).Internal().Q(Param{Name: "country"}, Param{Name: "notice_type"}),
		Command("POST", "/admin/notice-templates", "adminPublishNoticeTemplate", tagAdmin, "Publish the next notice template version", http.StatusCreated,
			func(c *Call, in *usecases.NoticeTemplateInput) (usecases.NoticeTemplateView, error) {
				return c.S.AdminPublishNoticeTemplate(c.Ctx, c.Subj, *in)
			}).Internal().Capture("notice_template_id", "data.id"),
		Command("POST", "/admin/notice-templates/{template_id}/retire", "adminRetireNoticeTemplate", tagAdmin, "Retire a notice template version", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.NoticeTemplateView, error) {
				return c.S.AdminRetireNoticeTemplate(c.Ctx, c.Subj, c.ID("template_id"))
			}).Internal(),
		Command("POST", "/admin/tenancies/{tenancy_id}/deposit/resolution", "adminResolveDepositDispute", tagAdmin, "Decide a deposit dispute", http.StatusOK,
			func(c *Call, in *usecases.DepositResolutionInput) (usecases.DepositView, error) {
				return c.S.AdminResolveDepositDispute(c.Ctx, c.Subj, c.ID("tenancy_id"), *in)
			}).Internal().Describe("Splits the frozen disputed amount; payment-service executes the release.").Errs(409, 422),
	}
}
