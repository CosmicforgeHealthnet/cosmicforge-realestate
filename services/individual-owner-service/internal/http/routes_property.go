package http

import (
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

// DomainRoutes is the complete Sections 6–13 route registry.
func DomainRoutes() []Route {
	var rs []Route
	for _, group := range [][]Route{accountRoutes(), bankingRoutes(), propertyRoutes(), ownershipRoutes(), delegationRoutes(), listingRoutes(),
		propertyInternalRoutes(), listingInternalRoutes()} {
		rs = append(rs, group...)
	}
	return rs
}

// Tags (Postman folders, §21 grouping).
const (
	tagAccount    = "Account & Profile"
	tagBanking    = "Banking & Tax"
	tagProperties = "Properties"
	tagOwnership  = "Ownership & Verification"
	tagInternal   = "_Internal"
	tagAdmin      = "_Admin"
)

func slice[T any](v []T, err error) (pagination.Response[T], error) {
	if v == nil {
		v = []T{}
	}
	return pagination.Response[T]{Data: v}, err
}

func accountRoutes() []Route {
	return []Route{
		Get("/profile", "getOwnerProfile", tagAccount, "Get the owner profile",
			func(c *Call) (usecases.ProfileView, error) { return c.S.GetProfile(c.Ctx, c.Subj) }),
		Update("PATCH", "/profile", "updateOwnerProfile", tagAccount, "Replace the editable owner profile (If-Match; \"0\" creates)", true,
			func(c *Call, in *usecases.ProfileInput) (usecases.ProfileView, error) {
				return c.S.SaveOwnerProfile(c.Ctx, c.Subj, c.Version, *in)
			}),
		List("/profile/contacts", "listOwnerContacts", tagAccount, "List additional, emergency and trusted contacts",
			func(c *Call, _ Page) (pagination.Response[usecases.ContactView], error) {
				return slice(c.S.ListContacts(c.Ctx, c.Subj))
			}),
		Command("POST", "/profile/contacts", "addOwnerContact", tagAccount, "Add a contact", http.StatusCreated,
			func(c *Call, in *usecases.ContactInput) (usecases.ContactView, error) {
				return c.S.AddContact(c.Ctx, c.Subj, *in)
			}).
			Capture("owner_contact_id", "data.id"),
		Command("DELETE", "/profile/contacts/{contact_id}", "removeOwnerContact", tagAccount, "Remove a contact", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.Removed, error) {
				return c.S.RemoveContact(c.Ctx, c.Subj, c.ID("contact_id"))
			}),
		Get("/account/status", "getAccountStatus", tagAccount, "Account status, activation rule and sensitive-action gates",
			func(c *Call) (usecases.AccountStatusView, error) { return c.S.AccountStatus(c.Ctx, c.Subj) }),
		List("/verification", "listIdentityVerifications", tagAccount, "List identity verifications (verification-service projection)",
			func(c *Call, _ Page) (pagination.Response[usecases.VerificationLinkView], error) {
				return slice(c.S.ListIdentityVerifications(c.Ctx, c.Subj))
			}),
		CommandPre("POST", "/verification", "startIdentityVerification", tagAccount, "Start an IDENTITY verification with uploaded evidence", http.StatusCreated,
			func(c *Call, in *usecases.IdentityVerificationInput) (usecases.StartedCase, error) {
				return c.S.StartIdentityVerification(c.Ctx, c.Subj, *in, c.Key())
			},
			func(c *Call, _ *usecases.IdentityVerificationInput, sc usecases.StartedCase) (usecases.VerificationLinkView, error) {
				return c.S.RecordIdentityVerification(c.Ctx, c.Subj, sc)
			}).Describe("Creates a verification-service IDENTITY case (subject USER), attaches the media evidence, submits and requests processing. "+
			"The decision arrives asynchronously (verification.* events). 503 FEATURE_UNAVAILABLE when verification-service is not configured.").
			Capture("verification_id", "data.verification_id").Errs(409, 422, 503),
		Command("POST", "/verification/{verification_id}/sync", "syncVerification", tagAccount, "Reconcile a verification by read-back", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.VerificationLinkView, error) {
				return c.S.SyncMyVerification(c.Ctx, c.Subj, c.ID("verification_id"))
			}).Errs(503),
		List("/co-owner-invitations", "listCoOwnerInvitations", tagAccount, "Invitations addressed to my verified email",
			func(c *Call, _ Page) (pagination.Response[usecases.InvitationView], error) {
				return slice(c.S.ListInvitations(c.Ctx, c.Subj))
			}),
		Command("POST", "/co-owner-invitations/{party_id}/accept", "acceptCoOwnerInvitation", tagAccount, "Accept a co-ownership / representation invitation", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.PartyView, error) {
				return c.S.RespondInvitation(c.Ctx, c.Subj, c.ID("party_id"), true)
			}),
		Command("POST", "/co-owner-invitations/{party_id}/decline", "declineCoOwnerInvitation", tagAccount, "Decline an invitation", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.PartyView, error) {
				return c.S.RespondInvitation(c.Ctx, c.Subj, c.ID("party_id"), false)
			}),
		List("/consents", "listMyPendingConsents", tagAccount, "Co-owner consents awaiting my decision",
			func(c *Call, _ Page) (pagination.Response[usecases.PendingConsentView], error) {
				return slice(c.S.ListMyConsents(c.Ctx, c.Subj))
			}),
		Command("POST", "/consents/{consent_id}/decision", "decideConsent", tagAccount, "Approve or reject a consent request (immutable)", http.StatusOK,
			func(c *Call, in *usecases.ConsentDecisionInput) (usecases.ConsentView, error) {
				return c.S.DecideConsent(c.Ctx, c.Subj, c.ID("consent_id"), *in)
			}),
		List("/ownership-transfers/incoming", "listIncomingTransfers", tagAccount, "Ownership transfers addressed to my verified email",
			func(c *Call, _ Page) (pagination.Response[usecases.TransferView], error) {
				return slice(c.S.ListIncomingTransfers(c.Ctx, c.Subj))
			}),
		Command("POST", "/ownership-transfers/{case_id}/accept", "acceptOwnershipTransfer", tagAccount, "Accept an incoming ownership transfer", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.TransferView, error) {
				return c.S.AcceptTransfer(c.Ctx, c.Subj, c.ID("case_id"))
			}),
	}
}

func bankingRoutes() []Route {
	return []Route{
		List("/payout-accounts", "listPayoutAccounts", tagBanking, "List payout accounts (masked)",
			func(c *Call, _ Page) (pagination.Response[usecases.PayoutAccountView], error) {
				return slice(c.S.ListPayoutAccounts(c.Ctx, c.Subj))
			}),
		CommandPre("POST", "/payout-accounts", "addPayoutAccount", tagBanking, "Register a payout account with payment-service", http.StatusCreated,
			func(c *Call, in *usecases.AddPayoutAccountInput) (payment.Beneficiary, error) {
				return c.S.RegisterPayoutBeneficiary(c.Ctx, c.Subj, *in, c.DownstreamKey("payment.beneficiary"))
			},
			func(c *Call, in *usecases.AddPayoutAccountInput, b payment.Beneficiary) (usecases.PayoutAccountView, error) {
				return c.S.AddPayoutAccount(c.Ctx, c.Subj, *in, b)
			}).SteppedUp().Describe("Step-up required. Beneficiary verification and account-name matching are performed by payment-service; "+
			"only the last 4 digits and a keyed fingerprint are stored. Verified accounts pass a cooling-off period before they become ACTIVE. "+
			"503 FEATURE_UNAVAILABLE while payment-service is not configured (it is not implemented yet).").Capture("payout_account_id", "data.id").Errs(409, 503),
		CommandPre("POST", "/payout-accounts/{payout_account_id}/verify", "verifyPayoutAccount", tagBanking, "Refresh payment-service's verification verdict", http.StatusOK,
			func(c *Call, _ *Empty) (payment.Beneficiary, error) {
				return c.S.RefreshPayoutBeneficiary(c.Ctx, c.Subj, c.ID("payout_account_id"))
			},
			func(c *Call, _ *Empty, b payment.Beneficiary) (usecases.PayoutAccountView, error) {
				return c.S.VerifyPayoutAccount(c.Ctx, c.Subj, c.ID("payout_account_id"), b)
			}).SteppedUp().Errs(503),
		Command("POST", "/payout-accounts/{payout_account_id}/make-default", "makeDefaultPayoutAccount", tagBanking, "Make an ACTIVE account the default", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.PayoutAccountView, error) {
				return c.S.MakeDefaultPayout(c.Ctx, c.Subj, c.ID("payout_account_id"))
			}).SteppedUp().Errs(409),
		Command("DELETE", "/payout-accounts/{payout_account_id}", "removePayoutAccount", tagBanking, "Remove a payout account", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.Removed, error) {
				return c.S.RemovePayoutAccount(c.Ctx, c.Subj, c.ID("payout_account_id"))
			}).SteppedUp().Errs(409),
		Get("/payout-preferences", "getPayoutPreferences", tagBanking, "Payout account per business line",
			func(c *Call) (usecases.PayoutPreferencesView, error) { return c.S.GetPayoutPreferences(c.Ctx, c.Subj) }),
		Update("PUT", "/payout-preferences", "putPayoutPreferences", tagBanking, "Replace payout preferences (If-Match; \"0\" creates)", true,
			func(c *Call, in *usecases.PayoutPreferencesInput) (usecases.PayoutPreferencesView, error) {
				return c.S.PutPayoutPreferences(c.Ctx, c.Subj, c.Version, *in)
			}).SteppedUp(),
		Get("/tax-profile", "getTaxProfile", tagBanking, "Masked tax / reporting profile",
			func(c *Call) (usecases.TaxProfileView, error) { return c.S.GetTaxProfile(c.Ctx, c.Subj) }),
		Update("PUT", "/tax-profile", "putTaxProfile", tagBanking, "Replace the tax profile (If-Match; \"0\" creates)", true,
			func(c *Call, in *usecases.TaxProfileInput) (usecases.TaxProfileView, error) {
				return c.S.PutTaxProfile(c.Ctx, c.Subj, c.Version, *in)
			}).SteppedUp(),
		List("/statements", "listStatements", tagBanking, "Owner statements (payment-service)",
			func(c *Call, p Page) (pagination.Response[payment.Statement], error) {
				st, next, err := c.S.ListStatements(c.Ctx, c.Subj, p.Cursor, p.Limit)
				resp, _ := slice(st, nil)
				if next != "" {
					resp.Page = pagination.Page{NextCursor: &next, HasMore: true}
				}
				return resp, err
			}).Describe("payment-service is the financial authority. 503 FEATURE_UNAVAILABLE while it is not configured.").Errs(503),
	}
}

var propertyFilters = []Param{{Name: "status", Enum: []string{"DRAFT", "ACTIVE", "RESTRICTED", "TRANSFER_PENDING", "ARCHIVED"}},
	{Name: "category", Enum: []string{"RESIDENTIAL", "COMMERCIAL", "LAND", "MIXED_USE"}}}

func propertyRoutes() []Route {
	return []Route{
		List("/properties", "listProperties", tagProperties, "List my portfolio (owned, co-owned and represented)",
			func(c *Call, p Page) (pagination.Response[usecases.PropertySummary], error) {
				return c.S.ListProperties(c.Ctx, c.Subj, c.Query("status"), c.Query("category"), p.Cursor, p.Limit)
			}).Q(propertyFilters...),
		Command("POST", "/properties", "createProperty", tagProperties, "Create a persistent property record", http.StatusCreated,
			func(c *Call, in *usecases.CreatePropertyInput) (usecases.PropertyView, error) {
				return c.S.CreateProperty(c.Ctx, c.Subj, *in)
			}).Describe("Creates the property (DRAFT), address, features and the primary ownership claim. A claim on an address another owner holds "+
			"is recorded with ownership status CONFLICT for platform review; the other owner is never disclosed. 409 PROPERTY_DUPLICATE for your own duplicate.").
			Capture("property_id", "data.id").Capture("property_version", "data.version").Errs(409),
		Command("POST", "/properties/imports", "importProperties", tagProperties, "Bulk import properties (asynchronous)", http.StatusAccepted,
			func(c *Call, in *usecases.ImportInput) (usecases.ImportView, error) {
				return c.S.CreateImport(c.Ctx, c.Subj, *in)
			}).
			Describe("Up to 500 rows; each row needs external_ref, the idempotent matching key (existing refs are matched, never duplicated). "+
				"Poll GET /properties/imports/{import_id} for the per-row error report.").Capture("import_id", "data.id"),
		Get("/properties/imports/{import_id}", "getPropertyImport", tagProperties, "Get an import job and its error report",
			func(c *Call) (usecases.ImportView, error) { return c.S.GetImport(c.Ctx, c.Subj, c.ID("import_id")) }),
		Get("/properties/{property_id}", "getProperty", tagProperties, "Get a property",
			func(c *Call) (usecases.PropertyView, error) {
				return c.S.GetProperty(c.Ctx, c.Subj, c.ID("property_id"))
			}),
		Update("PATCH", "/properties/{property_id}", "updateProperty", tagProperties, "Update property details (If-Match)", false,
			func(c *Call, in *usecases.UpdatePropertyInput) (usecases.PropertyView, error) {
				return c.S.UpdateProperty(c.Ctx, c.Subj, c.ID("property_id"), c.Version, *in)
			}).Describe("Changing the address of a VERIFIED property moves the ownership claim to REVERIFICATION_REQUIRED.").Errs(409),
		Command("POST", "/properties/{property_id}/archive", "archiveProperty", tagProperties, "Archive a property without active business", http.StatusOK,
			func(c *Call, in *ReasonInput) (usecases.PropertyView, error) {
				return c.S.ArchiveProperty(c.Ctx, c.Subj, c.ID("property_id"), in.Reason)
			}).Errs(409),
		List("/properties/{property_id}/history", "getPropertyHistory", tagProperties, "Immutable property history",
			func(c *Call, p Page) (pagination.Response[usecases.HistoryView], error) {
				return c.S.PropertyHistory(c.Ctx, c.Subj, c.ID("property_id"), p.Cursor, p.Limit)
			}),
		List("/properties/{property_id}/units", "listUnits", tagProperties, "List units",
			func(c *Call, _ Page) (pagination.Response[usecases.UnitView], error) {
				return slice(c.S.ListUnits(c.Ctx, c.Subj, c.ID("property_id"), c.Query("include_archived") == "true"))
			}).Q(Param{Name: "include_archived", Enum: []string{"true", "false"}}),
		Command("POST", "/properties/{property_id}/units", "createUnit", tagProperties, "Add a unit / room / floor", http.StatusCreated,
			func(c *Call, in *usecases.UnitInput) (usecases.UnitView, error) {
				return c.S.CreateUnit(c.Ctx, c.Subj, c.ID("property_id"), *in)
			}).Capture("unit_id", "data.id").Capture("unit_version", "data.version").Errs(409),
		Update("PATCH", "/properties/{property_id}/units/{unit_id}", "updateUnit", tagProperties, "Update or archive a unit (If-Match)", false,
			func(c *Call, in *usecases.UnitPatch) (usecases.UnitView, error) {
				return c.S.UpdateUnit(c.Ctx, c.Subj, c.ID("property_id"), c.ID("unit_id"), c.Version, *in)
			}).Errs(409),
		List("/properties/{property_id}/parcels", "listParcels", tagProperties, "List land parcels",
			func(c *Call, _ Page) (pagination.Response[usecases.ParcelView], error) {
				return slice(c.S.ListParcels(c.Ctx, c.Subj, c.ID("property_id")))
			}),
		Command("POST", "/properties/{property_id}/parcels", "createParcel", tagProperties, "Add a land parcel", http.StatusCreated,
			func(c *Call, in *usecases.ParcelInput) (usecases.ParcelView, error) {
				return c.S.CreateParcel(c.Ctx, c.Subj, c.ID("property_id"), *in)
			}).Capture("parcel_id", "data.id").Errs(409, 422),
		Update("PUT", "/properties/{property_id}/parcels/{parcel_id}", "replaceParcel", tagProperties, "Replace a parcel (If-Match)", false,
			func(c *Call, in *usecases.ParcelInput) (usecases.ParcelView, error) {
				return c.S.UpdateParcel(c.Ctx, c.Subj, c.ID("property_id"), c.ID("parcel_id"), c.Version, *in)
			}),
		List("/properties/{property_id}/assets", "listAssets", tagProperties, "Asset register",
			func(c *Call, _ Page) (pagination.Response[usecases.AssetView], error) {
				return slice(c.S.ListAssets(c.Ctx, c.Subj, c.ID("property_id"), c.Query("include_retired") == "true"))
			}).Q(Param{Name: "include_retired", Enum: []string{"true", "false"}}),
		Command("POST", "/properties/{property_id}/assets", "createAsset", tagProperties, "Register an asset", http.StatusCreated,
			func(c *Call, in *usecases.AssetInput) (usecases.AssetView, error) {
				return c.S.CreateAsset(c.Ctx, c.Subj, c.ID("property_id"), *in)
			}).Capture("asset_id", "data.id"),
		Update("PUT", "/properties/{property_id}/assets/{asset_id}", "replaceAsset", tagProperties, "Replace an asset (If-Match)", false,
			func(c *Call, in *usecases.AssetInput) (usecases.AssetView, error) {
				return c.S.UpdateAsset(c.Ctx, c.Subj, c.ID("property_id"), c.ID("asset_id"), c.Version, *in)
			}),
		List("/properties/{property_id}/media", "listPropertyMedia", tagProperties, "List linked media",
			func(c *Call, _ Page) (pagination.Response[usecases.MediaLinkView], error) {
				return slice(c.S.ListPropertyMedia(c.Ctx, c.Subj, c.ID("property_id")))
			}),
		CommandPre("POST", "/properties/{property_id}/media", "linkPropertyMedia", tagProperties, "Link an uploaded media file", http.StatusCreated,
			func(c *Call, in *usecases.MediaLinkInput) (media.Media, error) {
				return c.S.CheckPropertyMedia(c.Ctx, c.Subj, c.ID("property_id"), *in)
			},
			func(c *Call, in *usecases.MediaLinkInput, m media.Media) (usecases.MediaLinkView, error) {
				return c.S.AddPropertyMedia(c.Ctx, c.Subj, c.ID("property_id"), *in, m)
			}).Describe("The file must have been uploaded to media-document-service by you (or for the property). Bytes are never stored here.").
			Capture("media_link_id", "data.id").Errs(409, 422, 503),
		Command("DELETE", "/properties/{property_id}/media/{media_link_id}", "unlinkPropertyMedia", tagProperties, "Unlink media", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.Removed, error) {
				return c.S.RemovePropertyMedia(c.Ctx, c.Subj, c.ID("property_id"), c.ID("media_link_id"))
			}),
	}
}

// ReasonInput is an optional free-text reason.
type ReasonInput struct {
	Reason string `json:"reason,omitempty" v:"max=500"`
}

func ownershipRoutes() []Route {
	return []Route{
		Get("/properties/{property_id}/ownership", "getOwnership", tagOwnership, "Ownership records, parties, evidence and transfer",
			func(c *Call) (usecases.OwnershipView, error) {
				return c.S.GetOwnership(c.Ctx, c.Subj, c.ID("property_id"))
			}),
		CommandPre("POST", "/properties/{property_id}/ownership", "addOwnershipEvidence", tagOwnership, "Attach authority documents to the claim", http.StatusOK,
			func(c *Call, in *usecases.AddEvidenceInput) (usecases.CheckedMedia, error) {
				return c.S.CheckEvidence(c.Ctx, c.Subj, c.ID("property_id"), in.Documents)
			},
			func(c *Call, in *usecases.AddEvidenceInput, m usecases.CheckedMedia) (usecases.OwnershipView, error) {
				return c.S.AddOwnershipEvidence(c.Ctx, c.Subj, c.ID("property_id"), *in, m)
			}).Errs(409, 422, 503),
		Command("POST", "/properties/{property_id}/claim", "claimOwnership", tagOwnership, "Declare the claim type (or re-claim after rejection)", http.StatusOK,
			func(c *Call, in *usecases.ClaimInput) (usecases.OwnershipView, error) {
				return c.S.ClaimOwnership(c.Ctx, c.Subj, c.ID("property_id"), *in)
			}).Errs(409),
		CommandPre("POST", "/properties/{property_id}/verification", "submitPropertyVerification", tagOwnership, "Submit the claim to verification-service (PROPERTY_AUTHORITY)", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.PreparedPropertyVerification, error) {
				return c.S.StartPropertyVerification(c.Ctx, c.Subj, c.ID("property_id"), c.Key())
			},
			func(c *Call, _ *Empty, pv usecases.PreparedPropertyVerification) (usecases.OwnershipView, error) {
				return c.S.RecordPropertyVerification(c.Ctx, c.Subj, c.ID("property_id"), pv)
			}).Describe("Requires the claim in EVIDENCE_PENDING or REVERIFICATION_REQUIRED with at least one READY document. "+
			"verification-service decides; the result arrives via verification.* events (or POST /verification/{id}/sync).").Errs(409, 422, 503),
		List("/properties/{property_id}/verification", "listPropertyVerifications", tagOwnership, "Property verification history",
			func(c *Call, _ Page) (pagination.Response[usecases.VerificationLinkView], error) {
				return slice(c.S.PropertyVerifications(c.Ctx, c.Subj, c.ID("property_id")))
			}),
		Get("/properties/{property_id}/verification-summary", "getVerificationSummary", tagOwnership, "Public verification summary (projection with limitations)",
			func(c *Call) (usecases.PublicSummary, error) {
				return c.S.VerificationSummary(c.Ctx, c.Subj, c.ID("property_id"))
			}),
		Command("POST", "/properties/{property_id}/co-owners", "addCoOwner", tagOwnership, "Invite a co-owner / representative or record a beneficial owner", http.StatusCreated,
			func(c *Call, in *usecases.CoOwnerInput) (usecases.PartyView, error) {
				return c.S.AddCoOwner(c.Ctx, c.Subj, c.ID("property_id"), *in)
			}).Describe("Invitees accept with the invited email once verified on their own owner account (account existence is never disclosed). "+
			"Adding a co-owner to a VERIFIED claim requires re-verification.").Capture("party_id", "data.id").Errs(409, 422),
		Command("DELETE", "/properties/{property_id}/co-owners/{party_id}", "removeCoOwner", tagOwnership, "Remove a party", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.PartyView, error) {
				return c.S.RemoveParty(c.Ctx, c.Subj, c.ID("property_id"), c.ID("party_id"))
			}).Errs(409, 422),
		List("/properties/{property_id}/consents", "listPropertyConsents", tagOwnership, "Consent history of the property",
			func(c *Call, _ Page) (pagination.Response[usecases.ConsentView], error) {
				return slice(c.S.ListPropertyConsents(c.Ctx, c.Subj, c.ID("property_id")))
			}),
		Command("POST", "/properties/{property_id}/consents", "requestConsent", tagOwnership, "Request co-owner consent for an action", http.StatusCreated,
			func(c *Call, in *usecases.RequestConsentInput) (usecases.ConsentStatus, error) {
				return c.S.RequestConsent(c.Ctx, c.Subj, c.ID("property_id"), *in)
			}).Capture("consent_id", "data.consents.0.id").Errs(422),
		Get("/properties/{property_id}/ownership-transfer", "getOwnershipTransfer", tagOwnership, "Latest ownership transfer case",
			func(c *Call) (usecases.TransferView, error) {
				return c.S.GetTransfer(c.Ctx, c.Subj, c.ID("property_id"))
			}),
		Command("POST", "/properties/{property_id}/ownership-transfer", "openOwnershipTransfer", tagOwnership, "Open a guarded ownership transfer", http.StatusCreated,
			func(c *Call, in *usecases.TransferInput) (usecases.TransferView, error) {
				return c.S.OpenTransfer(c.Ctx, c.Subj, c.ID("property_id"), *in)
			}).SteppedUp().Describe("Requires step-up, verified authority, identity verification (policy) and co-owner consent (OWNERSHIP_TRANSFER, target = property). "+
			"The recipient accepts, the transferor submits evidence, platform review decides; the owner never changes by a direct update.").
			Capture("transfer_case_id", "data.id").Errs(409, 422),
		CommandPre("POST", "/properties/{property_id}/ownership-transfer/submit", "submitOwnershipTransfer", tagOwnership, "Submit transfer evidence for review", http.StatusOK,
			func(c *Call, in *usecases.AddEvidenceInput) (usecases.CheckedMedia, error) {
				return c.S.CheckEvidence(c.Ctx, c.Subj, c.ID("property_id"), in.Documents)
			},
			func(c *Call, in *usecases.AddEvidenceInput, m usecases.CheckedMedia) (usecases.TransferView, error) {
				return c.S.SubmitTransfer(c.Ctx, c.Subj, c.ID("property_id"), *in, m)
			}).SteppedUp().Errs(409, 422),
		Command("POST", "/properties/{property_id}/ownership-transfer/cancel", "cancelOwnershipTransfer", tagOwnership, "Cancel an open transfer", http.StatusOK,
			func(c *Call, _ *Empty) (usecases.TransferView, error) {
				return c.S.CancelTransfer(c.Ctx, c.Subj, c.ID("property_id"))
			}).Errs(409),
	}
}

func propertyInternalRoutes() []Route {
	return []Route{
		Get("/properties/{property_id}/public-summary", "internalPropertyPublicSummary", tagInternal, "Fresh public property/authority summary (marketplace read-back)",
			func(c *Call) (usecases.PublicSummary, error) {
				return c.S.InternalPublicSummary(c.Ctx, c.Subj, c.ID("property_id"))
			}).Internal().
			Describe("Callers: marketplace-service, admin-service. No owner personal data; exact coordinates only with EXACT visibility."),
		List("/ownership-conflicts", "listOwnershipConflicts", tagAdmin, "Ownership claims in CONFLICT awaiting review",
			func(c *Call, p Page) (pagination.Response[usecases.ConflictView], error) {
				return c.S.ListConflicts(c.Ctx, c.Subj, p.Cursor, p.Limit)
			}).Internal(),
		Command("POST", "/ownership-records/{record_id}/conflict-decision", "resolveOwnershipConflict", tagAdmin, "Release or reject a conflicting claim", http.StatusOK,
			func(c *Call, in *usecases.ResolveConflictInput) (usecases.RecordView, error) {
				return c.S.ResolveConflict(c.Ctx, c.Subj, c.ID("record_id"), *in)
			}).Internal().Errs(409),
		Command("POST", "/ownership-transfers/{case_id}/decision", "decideOwnershipTransfer", tagAdmin, "Approve or reject an ownership transfer under review", http.StatusOK,
			func(c *Call, in *usecases.AdminDecisionInput) (usecases.TransferView, error) {
				return c.S.DecideTransfer(c.Ctx, c.Subj, c.ID("case_id"), *in)
			}).Internal().Errs(409),
		Command("POST", "/properties/{property_id}/restrict", "restrictProperty", tagAdmin, "Restrict a property (platform review)", http.StatusOK,
			func(c *Call, in *usecases.RestrictInput) (usecases.PublicSummary, error) {
				return c.S.AdminRestrictProperty(c.Ctx, c.Subj, c.ID("property_id"), true, *in)
			}).Internal().Errs(409),
		Command("POST", "/properties/{property_id}/lift-restriction", "liftPropertyRestriction", tagAdmin, "Lift a property restriction", http.StatusOK,
			func(c *Call, in *usecases.RestrictInput) (usecases.PublicSummary, error) {
				return c.S.AdminRestrictProperty(c.Ctx, c.Subj, c.ID("property_id"), false, *in)
			}).Internal().Errs(409),
	}
}
