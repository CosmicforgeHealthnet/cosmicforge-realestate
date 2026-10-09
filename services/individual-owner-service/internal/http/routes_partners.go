package http

import (
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

const (
	tagFinancing = "Financing & Insurance"
	tagAccess    = "Smart Access"
)

func partnerRoutes() []Route {
	return []Route{
		List("/financing/options", "getFinancingOptions", tagFinancing, "Partner financing offers for a listing",
			func(c *Call, _ Page) (pagination.Response[usecases.FinanceOfferView], error) {
				lid, err := c.QueryID("listing_id")
				if err != nil {
					return pagination.Response[usecases.FinanceOfferView]{}, err
				}
				return slice(c.S.FinancingOptions(c.Ctx, c.Subj, lid))
			}).Q(Param{Name: "listing_id", Doc: "Required"}).
			Describe("Needs the PARTNER_FINANCING consent (422 CONSENT_REQUIRED) and a financing partner (503 FEATURE_UNAVAILABLE). Offers carry the partner's total-cost disclosure.").
			Errs(422, 503),
		Command("POST", "/financing/consents", "recordFinancingConsent", tagFinancing, "Consent to share listing details with financing partners", http.StatusOK,
			func(c *Call, in *usecases.FinancingConsentInput) (usecases.PrivacyConsentView, error) {
				return c.S.RecordFinancingConsent(c.Ctx, c.Subj, *in)
			}).Describe("Applicant financial details stay with the partner; the owner sees minimum status only."),
		List("/financing/offers", "listFinanceOffers", tagFinancing, "Financing offers received",
			func(c *Call, _ Page) (pagination.Response[usecases.FinanceOfferView], error) {
				return slice(c.S.ListFinanceOffers(c.Ctx, c.Subj))
			}),
		Command("POST", "/financing/offers/{offer_id}/decision", "decideFinanceOffer", tagFinancing, "Accept (with payout preference) or decline an offer", http.StatusOK,
			func(c *Call, in *usecases.FinanceDecisionInput) (usecases.FinanceOfferView, error) {
				return c.S.DecideFinanceOffer(c.Ctx, c.Subj, c.ID("offer_id"), *in)
			}).Errs(409, 503),
		List("/insurance/options", "getInsuranceOptions", tagFinancing, "Partner insurance products for a property",
			func(c *Call, _ Page) (pagination.Response[usecases.InsuranceProductView], error) {
				pid, err := c.QueryID("property_id")
				if err != nil {
					return pagination.Response[usecases.InsuranceProductView]{}, err
				}
				return slice(c.S.InsuranceOptions(c.Ctx, c.Subj, pid))
			}).Q(Param{Name: "property_id", Doc: "Required"}).Describe("Needs the PARTNER_INSURANCE consent and an insurance partner.").Errs(422, 503),
		List("/insurance/policies", "listInsurancePolicies", tagFinancing, "Insurance policies",
			func(c *Call, _ Page) (pagination.Response[usecases.PolicyView], error) {
				pid, err := c.QueryID("property_id")
				if err != nil {
					return pagination.Response[usecases.PolicyView]{}, err
				}
				return slice(c.S.ListPolicies(c.Ctx, c.Subj, pid))
			}).Q(Param{Name: "property_id"}),
		CommandPre("POST", "/insurance/policies", "recordInsurancePolicy", tagFinancing, "Record a policy I hold", http.StatusCreated,
			func(c *Call, in *usecases.PolicyInput) (usecases.Evidence, error) {
				return c.S.CheckMediaEvidence(c.Ctx, c.Subj, usecases.ScopeProperty, in.PropertyID, authz.Do(authz.CapPropertyEdit), in.MediaRefs(), scanClasses...)
			},
			func(c *Call, in *usecases.PolicyInput, ev usecases.Evidence) (usecases.PolicyView, error) {
				return c.S.RecordPolicy(c.Ctx, c.Subj, *in, ev)
			}).Describe("With the policy schedule; renewal reminders 30 days before expiry.").Capture("policy_id", "data.id").Errs(409),
		List("/insurance/claims", "listInsuranceClaims", tagFinancing, "Insurance claims",
			func(c *Call, _ Page) (pagination.Response[usecases.InsuranceClaimView], error) {
				pid, err := c.QueryID("policy_id")
				if err != nil {
					return pagination.Response[usecases.InsuranceClaimView]{}, err
				}
				return slice(c.S.ListInsuranceClaims(c.Ctx, c.Subj, pid))
			}).Q(Param{Name: "policy_id"}),
		CommandPre("POST", "/insurance/claims", "fileInsuranceClaim", tagFinancing, "Record a claim", http.StatusCreated,
			func(c *Call, in *usecases.InsuranceClaimInput) (usecases.Evidence, error) {
				return c.S.CheckOwnEvidence(c.Ctx, c.Subj, in.MediaRefs(), evidenceClasses...)
			},
			func(c *Call, in *usecases.InsuranceClaimInput, ev usecases.Evidence) (usecases.InsuranceClaimView, error) {
				return c.S.FileInsuranceClaim(c.Ctx, c.Subj, *in, ev)
			}).Capture("insurance_claim_id", "data.id").Capture("insurance_claim_version", "data.version").Errs(422, 503),
		Update("PATCH", "/insurance/claims/{claim_id}", "updateInsuranceClaim", tagFinancing, "Record the insurer's progress (If-Match)", false,
			func(c *Call, in *usecases.InsuranceClaimUpdate) (usecases.InsuranceClaimView, error) {
				ev, err := c.S.CheckOwnEvidence(c.Ctx, c.Subj, in.MediaRefs(), evidenceClasses...)
				if err != nil {
					return usecases.InsuranceClaimView{}, err
				}
				return c.S.UpdateInsuranceClaim(c.Ctx, c.Subj, c.ID("claim_id"), c.Version, *in, ev)
			}).Describe("Decisions and payouts need the insurer's letter or payout proof.").Errs(409, 422),
	}
}

func accessRoutes() []Route {
	gid := func(c *Call) ids.ID { return c.ID("grant_id") }
	return []Route{
		List("/access/devices", "listAccessDevices", tagAccess, "Access devices and key sets of a property",
			func(c *Call, _ Page) (pagination.Response[usecases.AccessDeviceView], error) {
				pid, err := c.QueryID("property_id")
				if err != nil {
					return pagination.Response[usecases.AccessDeviceView]{}, err
				}
				return slice(c.S.ListAccessDevices(c.Ctx, c.Subj, pid))
			}).Q(Param{Name: "property_id", Doc: "Required"}),
		Command("POST", "/access/devices", "registerAccessDevice", tagAccess, "Register a device, lockbox, key or card", http.StatusCreated,
			func(c *Call, in *usecases.AccessDeviceInput) (usecases.AccessDeviceView, error) {
				return c.S.RegisterAccessDevice(c.Ctx, c.Subj, *in)
			}).
			Describe("Smart locks and keypads need a smart-access provider (503).").Capture("access_device_id", "data.id").Errs(503),
		List("/access/grants", "listAccessGrants", tagAccess, "Access grants",
			func(c *Call, _ Page) (pagination.Response[usecases.GrantView], error) {
				pid, err := c.QueryID("property_id")
				if err != nil {
					return pagination.Response[usecases.GrantView]{}, err
				}
				return slice(c.S.ListGrants(c.Ctx, c.Subj, pid, c.Query("status")))
			}).Q(Param{Name: "property_id"}, Param{Name: "status", Enum: []string{"REQUESTED", "APPROVED", "ISSUED", "ACTIVE", "EXPIRED", "REVOKED", "COMPROMISED"}}),
		Command("POST", "/access/grants", "createAccessGrant", tagAccess, "Grant time-limited access", http.StatusCreated,
			func(c *Call, in *usecases.GrantInput) (usecases.GrantView, error) {
				return c.S.CreateGrant(c.Ctx, c.Subj, *in)
			}).
			Describe("For a confirmed viewing, live tenancy, confirmed stay, active manager mandate or active maintenance job, within its period; "+
				"ends automatically with it. Smart credentials are provisioned by the provider; keys/cards are issued with a handover record.").
			Capture("access_grant_id", "data.id").Errs(404, 409, 422),
		Command("POST", "/access/grants/{grant_id}/actions", "actOnAccessGrant", tagAccess, "Approve a request, issue a key/card or revoke", http.StatusOK,
			func(c *Call, in *usecases.GrantAction) (usecases.GrantView, error) {
				return c.S.ActOnGrant(c.Ctx, c.Subj, gid(c), *in)
			}).
			Describe("A compromised credential is revoked immediately with a security notification.").Errs(409),
		Command("POST", "/access/grants/{grant_id}/revoke", "revokeAccessGrant", tagAccess, "Revoke access now", http.StatusOK,
			func(c *Call, in *usecases.ReasonCodeInput) (usecases.GrantView, error) {
				return c.S.ActOnGrant(c.Ctx, c.Subj, gid(c), usecases.GrantAction{Action: "REVOKE", ReasonCode: &in.ReasonCode, Note: in.Note})
			}).Errs(409),
		List("/access/history", "getAccessHistory", tagAccess, "Access history",
			func(c *Call, p Page) (pagination.Response[usecases.AccessEventView], error) {
				pid, err := c.QueryID("property_id")
				if err != nil {
					return pagination.Response[usecases.AccessEventView]{}, err
				}
				g, err := c.QueryID("grant_id")
				if err != nil {
					return pagination.Response[usecases.AccessEventView]{}, err
				}
				return c.S.AccessHistory(c.Ctx, c.Subj, pid, g, p.Cursor, p.Limit)
			}).Q(Param{Name: "property_id"}, Param{Name: "grant_id"}),
		Command("POST", "/managed/access/grants", "managerRequestAccessGrant", tagInternal, "Delegated manager requests access (owner approves remotely)", http.StatusCreated,
			func(c *Call, in *usecases.GrantInput) (usecases.GrantView, error) {
				return c.S.CreateGrant(c.Ctx, c.Subj, *in)
			}).Internal().Errs(403, 409, 422),
	}
}
