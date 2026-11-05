package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

func TestBusinessCRMSignedSourceAssertionsReviewRevocationAndForgery(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	_, adapter := newCommercialSupplyConsumer(t)
	connection := c.connectMarketplace(adapter)
	g := crmGuestFixture(c)
	signer := c.supplySigner(platform.MarketplaceService)
	token, e := signer.TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: c.owner.registered.OrganisationID})
	if e != nil {
		t.Fatal(e)
	}
	assertion := businesshttp.CRMSourceAssertion{OrganisationID: c.owner.registered.OrganisationID, PropertyID: c.property.ID, ConnectionID: connection.ID, SourceReference: "opaque-marketplace-customer-reference", SourceVersion: 1, State: "ACTIVE", ExpiresAt: time.Now().UTC().Add(time.Hour), EvidenceReference: "owning-customer-assertion-reference"}
	path := "/internal/v1/business-supply/guest-assertions"
	if status, _ := c.rootCall(path, assertion, nil); status != 401 {
		t.Fatalf("missing service identity %d", status)
	}
	if status, _ := c.rootCall(path, assertion, map[string]string{"Authorization": "Bearer " + c.owner.tokens.AccessToken}); status != 401 {
		t.Fatalf("account token forged source identity %d", status)
	}
	wrong := c.supplySigner(platform.MediaDocumentService)
	wrongToken, e := wrong.TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: assertion.OrganisationID})
	if e != nil {
		t.Fatal(e)
	}
	if status, _ := c.rootCall(path, assertion, map[string]string{"Authorization": "Bearer " + wrongToken, "Idempotency-Key": "wrong-source-assertion"}); status != 403 {
		t.Fatalf("incorrect service identity %d", status)
	}
	header := map[string]string{"Authorization": "Bearer " + token, "Idempotency-Key": "same-source-identity-assertion"}
	status, raw := c.rootCall(path, assertion, header)
	if status != 201 {
		t.Fatalf("signed source assertion %d %s", status, raw)
	}
	projection := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, raw)
	status, raw = c.rootCall(path, assertion, header)
	if status != 201 || commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, raw).ID != projection.ID {
		t.Fatal("source assertion retry duplicated identity evidence")
	}
	link := businesshttp.CRMIdentityLinkCommand{GuestVersion: g.Guest.Version, SourceKind: "MARKETPLACE", SourceReference: assertion.SourceReference, ProjectionID: &projection.ID, EvidenceReference: "reviewed-matching-guest-source-reference", Reason: "Independent review links a signed owning source assertion to the business registration"}
	c.must(c.owner, "POST", "/guests/"+g.Guest.ID.String()+"/identity-links", link, 403)
	reviewer := c.user()
	c.secure(reviewer)
	c.join(reviewer, c.owner, []string{"approvals:decide", "guest_privacy:write"})
	c.approver = reviewer
	op := link
	link.ApprovalID = c.approval("COMMERCIAL_GUEST_IDENTITY_LINK", "guest_privacy:write", "", nil, "", map[string]any{"guest_id": g.Guest.ID, "property_id": c.property.ID, "command": op})
	linked := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", "/guests/"+g.Guest.ID.String()+"/identity-links", link, 201))
	booking := reservationCommandFixture(c, false)
	booking.Guests = []reservations.GuestCommand{{GuestID: &g.Guest.ID, SourceKind: "MARKETPLACE", SourceReference: assertion.SourceReference, IsPrimary: true}}
	v := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", booking, 201))
	// A renewed signed snapshot updates source freshness, not the immutable
	// reviewed mapping or its identity. Expired newest evidence must invalidate
	// an older still-live observation instead of being dropped.
	assertion.SourceVersion = 2
	assertion.ExpiresAt = time.Now().UTC().Add(2 * time.Hour)
	header["Idempotency-Key"] = "source-identity-renewal"
	if status, raw = c.rootCall(path, assertion, header); status != 201 {
		t.Fatalf("source renewal %d %s", status, raw)
	}
	hash := security.SHA256Hex([]byte("business:guest-source:" + assertion.OrganisationID.String() + ":MARKETPLACE:" + assertion.SourceReference))
	if e = crm.RequireIdentityLink(t.Context(), c.db.Pool, assertion.OrganisationID, c.property.ID, g.Guest.ID, "MARKETPLACE", hash, time.Now()); e != nil {
		t.Fatalf("valid renewal lost reviewed mapping %v", e)
	}
	assertion.SourceVersion = 3
	assertion.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	header["Idempotency-Key"] = "source-identity-expiry"
	if status, raw = c.rootCall(path, assertion, header); status != 201 {
		t.Fatalf("source expiry evidence %d %s", status, raw)
	}
	if e = crm.RequireIdentityLink(t.Context(), c.db.Pool, assertion.OrganisationID, c.property.ID, g.Guest.ID, "MARKETPLACE", hash, time.Now()); e == nil {
		t.Fatal("expired newest source observation authorized the previous live snapshot")
	}
	assertion.SourceVersion = 4
	assertion.State = "REVOKED"
	header["Idempotency-Key"] = "source-identity-revocation"
	if status, raw = c.rootCall(path, assertion, header); status != 201 {
		t.Fatalf("authenticated source revocation %d %s", status, raw)
	}
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/cancel", reservations.Command{Version: v.Reservation.Version, Reason: "Release pending inventory before checking current source revocation"}, 200)
	booking = reservationCommandFixture(c, false)
	booking.Guests = []reservations.GuestCommand{{GuestID: &g.Guest.ID, SourceKind: "MARKETPLACE", SourceReference: assertion.SourceReference, IsPrimary: true}}
	c.must(c.owner, "POST", "/reservations", booking, 403)
	c.must(c.owner, "POST", "/guests/"+g.Guest.ID.String()+"/identity-links/"+linked.ID.String()+"/revoke", businesshttp.CRMVersionCommand{Version: 1, Reason: "Retain source link evidence while explicitly withdrawing its authority"}, 200)
	if _, e = c.db.Pool.Exec(t.Context(), `UPDATE customer_accounts_projection SET state='ACTIVE' WHERE id=$1`, projection.ID); e == nil {
		t.Fatal("authenticated source evidence could be overwritten")
	}
}

type crmRegisteredGuest struct {
	Guest           crm.Guest `json:"guest"`
	SourceKind      string    `json:"source_kind"`
	SourceReference string    `json:"source_reference"`
}

func crmGuestFixture(c *commercialHarness) crmRegisteredGuest {
	c.t.Helper()
	return commercialData[crmRegisteredGuest](c.t, c.must(c.owner, "POST", "/guests", businesshttp.CRMGuestCreate{PropertyID: c.property.ID, Profile: crm.Profile{DisplayName: "Disposable guest", Language: "en"}, SourceKind: "DIRECT", RegistrationReference: "local-registration:" + ids.New().String(), Reason: "Record an actual operator-captured local registration reference"}, 201))
}
func crmConsent(c *commercialHarness, g crmRegisteredGuest, purpose, state string, version int64) {
	c.t.Helper()
	c.must(c.owner, "POST", "/guests/"+g.Guest.ID.String()+"/consents", businesshttp.CRMConsentCommand{ExpectedVersion: version, Purpose: purpose, State: state, EvidenceReference: "guest-captured-form:" + ids.New().String(), Reason: "Record explicit guest-purpose consent evidence and current decision"}, 201)
}
func TestBusinessCRMProtectedFieldsConsentWithdrawalAndNoContactMerge(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	guest := crmGuestFixture(c)
	other := crmGuestFixture(c)
	if guest.Guest.ID == other.Guest.ID {
		t.Fatal("display names caused an automatic identity merge")
	}
	private := businesshttp.CRMPrivateCommand{Preferences: crm.PrivateProfile{Email: "sensitive-disposable@example.invalid", RoomPreferences: []string{"Upper floor"}, Accessibility: []string{"Step-free route"}}, Reason: "Retain purpose-specific encrypted operational preferences"}
	c.must(c.owner, "PUT", "/guests/"+guest.Guest.ID.String()+"/private", private, 403)
	for _, purpose := range []string{"OPERATIONAL_CONTACT", "PREFERENCES", "ACCESSIBILITY"} {
		crmConsent(c, guest, purpose, "GRANTED", 0)
	}
	c.must(c.owner, "PUT", "/guests/"+guest.Guest.ID.String()+"/private", private, 200)
	crmConsent(c, other, "OPERATIONAL_CONTACT", "GRANTED", 0)
	sameContact := private
	sameContact.Preferences.RoomPreferences = nil
	sameContact.Preferences.Accessibility = nil
	c.must(c.owner, "PUT", "/guests/"+other.Guest.ID.String()+"/private", sameContact, 200)
	public := c.must(c.owner, "GET", "/guests/"+guest.Guest.ID.String(), nil, 200)
	if strings.Contains(string(public), private.Preferences.Email) || strings.Contains(string(public), "Step-free") {
		t.Fatal("ordinary profile exposed protected fields")
	}
	in := reservationCommandFixture(c, false)
	in.Guests = []reservations.GuestCommand{{GuestID: &guest.Guest.ID, SourceKind: guest.SourceKind, SourceReference: guest.SourceReference, IsPrimary: true}}
	v := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", in, 201))
	url := "/guests/" + guest.Guest.ID.String() + "/private?purpose=RESERVATION_SERVICE&reason=Serve%20the%20current%20reservation&reservation_id=" + v.Reservation.ID.String()
	read := commercialData[struct {
		Preferences crm.PrivateProfile `json:"preferences"`
	}](t, c.must(c.owner, "GET", url, nil, 200))
	if read.Preferences.Email != private.Preferences.Email || len(read.Preferences.Accessibility) != 1 {
		t.Fatalf("purpose-bound read %+v", read)
	}
	crmConsent(c, guest, "OPERATIONAL_CONTACT", "WITHDRAWN", 1)
	crmConsent(c, guest, "ACCESSIBILITY", "WITHDRAWN", 1)
	read = commercialData[struct {
		Preferences crm.PrivateProfile `json:"preferences"`
	}](t, c.must(c.owner, "GET", url, nil, 200))
	if read.Preferences.Email != "" || len(read.Preferences.Accessibility) != 0 || len(read.Preferences.RoomPreferences) != 1 {
		t.Fatalf("withdrawal failed immediately %+v", read)
	}
	outsider := c.user()
	c.must(outsider, "GET", "/guests/"+guest.Guest.ID.String(), nil, 404)
	limited := c.user()
	c.join(limited, c.owner, []string{"guests:read", "guest_private:read"})
	c.must(limited, "GET", "/guests/"+guest.Guest.ID.String()+"/private?purpose=RECORD_REVIEW&reason=Review%20this%20protected%20record", nil, 403)
	branch := c.inventoryBranch(c.owner)
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET scope_kind='BRANCH',scope_id=$3 WHERE organisation_id=$1 AND membership_id=(SELECT id FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2) AND capability IN ('guests:read','guest_private:read')`, c.owner.registered.OrganisationID, limited.registered.AccountID, branch); e != nil {
		t.Fatal(e)
	}
	c.must(limited, "GET", "/guests/"+guest.Guest.ID.String(), nil, 403)
	list := commercialData[struct {
		Items []crm.GuestView `json:"items"`
	}](t, c.must(limited, "GET", "/guests?limit=1", nil, 200))
	if len(list.Items) != 0 {
		t.Fatal("guest list filtered scope after pagination")
	}
	for _, table := range []string{"outbox_events", "audit_events", "idempotency_records"} {
		var stored string
		if e := c.db.Pool.QueryRow(t.Context(), `SELECT coalesce(string_agg(to_jsonb(e)::text,''),'') FROM `+table+` e`).Scan(&stored); e != nil {
			t.Fatal(e)
		}
		if strings.Contains(stored, private.Preferences.Email) || strings.Contains(stored, "Step-free route") {
			t.Fatalf("protected preference persisted in plaintext %s", table)
		}
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE consents SET state='GRANTED' WHERE guest_id=$1`, guest.Guest.ID); e == nil {
		t.Fatal("consent evidence was mutable")
	}
}

func TestBusinessCRMConcurrentConsentAndTypedGuestBinding(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	g := crmGuestFixture(c)
	in := businesshttp.CRMConsentCommand{Purpose: "MARKETING", State: "GRANTED", EvidenceReference: "actual-disposable-consent-form", Reason: "Concurrent versioned consent commands must preserve one decision"}
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			status, _ := c.call(c.owner, "POST", "/guests/"+g.Guest.ID.String()+"/consents", in, nil)
			statuses <- status
		}()
	}
	first, second := <-statuses, <-statuses
	if !(first == 201 && second == 409 || first == 409 && second == 201) {
		t.Fatalf("consent race %d/%d", first, second)
	}
	command := reservationCommandFixture(c, false)
	command.Guests = []reservations.GuestCommand{{GuestID: &g.Guest.ID, SourceKind: "DIRECT", SourceReference: "not-the-registered-source", IsPrimary: true}}
	c.must(c.owner, "POST", "/reservations", command, 403)
	command.Guests[0].SourceReference = g.SourceReference
	v := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", command, 201))
	if v.Guests[0].GuestID == nil || *v.Guests[0].GuestID != g.Guest.ID {
		t.Fatal("canonical guest binding lost")
	}
	c.must(c.owner, "PATCH", "/guests/"+g.Guest.ID.String(), businesshttp.CRMGuestPatch{Version: g.Guest.Version, State: "RESTRICTED", Reason: "Restrict future operations after an explicit accountable review"}, 200)
	// Existing reservation evidence remains, while using a new arbitrary source
	// UUID cannot bypass the current guest status on subsequent owning commands.
	if e := crm.RequireGuest(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.property.ID, g.Guest.ID); e == nil {
		t.Fatal("restricted guest authorized")
	}
}

func TestBusinessCRMRestrictedOrganisationCanWithdrawButCannotGrantConsent(t *testing.T) {
	c := newCommercialHTTP(t)
	g := crmGuestFixture(c)
	crmConsent(c, g, "MARKETING", "GRANTED", 0)
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE organisations SET state='SUSPENDED' WHERE id=$1`, c.owner.registered.OrganisationID); e != nil {
		t.Fatal(e)
	}
	crmConsent(c, g, "MARKETING", "WITHDRAWN", 1)
	c.must(c.owner, "POST", "/guests/"+g.Guest.ID.String()+"/consents", businesshttp.CRMConsentCommand{ExpectedVersion: 2, Purpose: "MARKETING", State: "GRANTED", EvidenceReference: "an-untrusted-request-cannot-enable-consent", Reason: "Restricted organisations cannot reuse the protective withdrawal context to grant authority"}, 403)
	var status string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM consents WHERE guest_id=$1 AND purpose='MARKETING' ORDER BY version DESC LIMIT 1`, g.Guest.ID).Scan(&status); e != nil || status != "WITHDRAWN" {
		t.Fatalf("current consent %s %v", status, e)
	}
}

func TestBusinessCRMCorporateContractAndTravelerBindActualReservation(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	g := crmGuestFixture(c)
	company := commercialData[crm.Corporate](t, c.must(c.owner, "POST", "/corporate-customers", businesshttp.CRMCorporateCreate{PropertyID: c.property.ID, LegalName: "Disposable customer company", RegistrationReference: "corp-register:" + ids.New().String(), Contacts: []crm.CorporateContact{{Name: "Travel officer", Email: "travel@example.invalid", RepresentativeReference: "company-designation-proof"}}, Reason: "Register a canonical corporate customer with protected contacts"}, 201))
	now := time.Now().UTC()
	terms := crm.CorporateTerms{RatePlanIDs: []ids.ID{c.rate.ID}, AllowedSources: []string{"CORPORATE"}, BillingResponsibility: "PAY_AT_PROPERTY", BillingReference: "independently-approved-corporate-guarantee", MaxBookingMinor: 100000, Currency: "NGN", RequireNamedTravelers: true, MaxNights: 30}
	contractCommand := businesshttp.CRMCorporateContractCommand{State: "ACTIVE", Definition: terms, EffectiveAt: now.Add(-time.Minute), ExpiresAt: now.AddDate(0, 1, 0), Reason: "Independent negotiated canonical rate and corporate billing limits"}
	op := contractCommand
	contractCommand.ApprovalID = c.approval("COMMERCIAL_CORPORATE_CONTRACT", "reservations:approve", "", &terms.MaxBookingMinor, terms.Currency, map[string]any{"corporate_id": company.ID, "property_id": c.property.ID, "command": op})
	contract := commercialData[crm.CorporateContract](t, c.must(c.owner, "POST", "/corporate-customers/"+company.ID.String()+"/contracts", contractCommand, 201))
	traveler := businesshttp.CRMTravelerCommand{GuestID: g.Guest.ID, ContractID: contract.ID, State: "ACTIVE", Policy: terms, EffectiveAt: contract.EffectiveAt, ExpiresAt: contract.ExpiresAt, Reason: "Independently approve the actual registered traveler within company limits"}
	travelOp := traveler
	traveler.ApprovalID = c.approval("COMMERCIAL_CORPORATE_TRAVELER", "reservations:approve", "", &terms.MaxBookingMinor, terms.Currency, map[string]any{"corporate_id": company.ID, "property_id": c.property.ID, "command": travelOp})
	c.must(c.owner, "POST", "/corporate-customers/"+company.ID.String()+"/travelers", traveler, 200)
	in := reservationCommandFixture(c, false)
	in.Source = "CORPORATE"
	in.PaymentResponsibility = "PAY_AT_PROPERTY"
	in.CorporateID = &company.ID
	in.CorporateContractID = &contract.ID
	in.Guests = []reservations.GuestCommand{{GuestID: &g.Guest.ID, SourceKind: g.SourceKind, SourceReference: g.SourceReference, IsPrimary: true}}
	quoteAmount := int64(20000)
	operation := in
	in.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &quoteAmount, "NGN", map[string]any{"command": operation})
	v := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", in, 201))
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/confirm", reservations.Command{Version: v.Reservation.Version, Reason: "Confirm a real named traveler with the exact current negotiated contract"}, 200)
	paused := contractCommand
	paused.ExpectedVersion = contract.Version
	paused.ContractKey = &contract.ContractKey
	paused.State = "SUSPENDED"
	paused.ApprovalID = ids.Nil
	c.must(c.owner, "POST", "/corporate-customers/"+company.ID.String()+"/contracts", paused, 201)
	if e := crm.RequireCurrentContract(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.property.ID, company.ID, contract.ID, time.Now()); e == nil {
		t.Fatal("superseded corporate contract remained authoritative")
	}
	if _, e := c.db.Pool.Exec(t.Context(), `DELETE FROM corporate_customer_contracts WHERE id=$1`, contract.ID); e == nil {
		t.Fatal("contract evidence deleted")
	}
}
