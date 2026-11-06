package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/smartaccess"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func pmsAccessConfigure(c *commercialHarness, method, provider string, expected int64, expires time.Time) hospitality.AccessConfiguration {
	c.t.Helper()
	in := businesshttp.PMSAccessConfigurationCommand{PropertyID: c.property.ID, RoomID: c.unit.ID, ExpectedVersion: expected, Method: method, Provider: provider, DeviceReference: "actual-room-key-inventory-reference", ExpiresAt: expires, Reason: "Record independently reviewed actual room access and its bounded accepted window"}
	if method == "DISABLED" {
		in.DeviceReference = ""
	} else {
		op := in
		in.ApprovalID = c.approval("COMMERCIAL_PMS_ACCESS_CONFIGURATION", "pms:approve", "", nil, "", map[string]any{"command": op})
	}
	raw := c.must(c.owner, "POST", "/hospitality-access-configurations", in, 201)
	pmsAutomationContract(c.t, "AccessConfiguration", raw)
	return commercialData[hospitality.AccessConfiguration](c.t, raw)
}
func pmsAccessFor(c *commercialHarness, assignment ids.ID, op string) businesshttp.PMSAccessView {
	c.t.Helper()
	var id ids.ID
	if e := c.db.Pool.QueryRow(c.t.Context(), `SELECT id FROM access_grant_references WHERE organisation_id=$1 AND assignment_id=$2 AND operation=$3`, c.owner.registered.OrganisationID, assignment, op).Scan(&id); e != nil {
		c.t.Fatal(e)
	}
	raw := c.must(c.owner, "GET", "/access-grants/"+id.String(), nil, 200)
	pmsAutomationContract(c.t, "PMSAccessView", raw)
	return commercialData[businesshttp.PMSAccessView](c.t, raw)
}
func pmsAccessAck(c *commercialHarness, v businesshttp.PMSAccessView, credential string) businesshttp.PMSAccessAcknowledgement {
	c.t.Helper()
	if v.Command == nil {
		c.t.Fatal("bounded access command absent")
	}
	return businesshttp.PMSAccessAcknowledgement{Version: v.Reference.Version, RequestDigest: v.Command.RequestDigest, CredentialReference: credential, EvidenceReference: "actual-qualified-physical-handover-or-return-reference", Reason: "Record actual physical credential handover or return without submitting a code or asserting provider truth"}
}
func pmsAccessDue(c *commercialHarness) {
	c.t.Helper()
	if _, e := c.db.Pool.Exec(c.t.Context(), `UPDATE hospitality_access_jobs SET available_at=$1,review_at=$1 WHERE delivery_state<>'RUNNING'`, c.service.Now().Add(-time.Second)); e != nil {
		c.t.Fatal(e)
	}
}
func TestBusinessPMSManualAccessHandoverReturnAndImmutableEvidence(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	v, d := pmsCollectedStay(c, func() { pmsAccessConfigure(c, "MANUAL_KEY", "", 0, c.service.Now().Add(30*time.Minute)) })
	issued := pmsAccessFor(c, d.Assignment.ID, "ISSUE")
	if issued.Command == nil || issued.Command.DeliveryState != "WAITING_MANUAL" || issued.EffectiveState != "UNKNOWN" || issued.DeliveryConfirmed {
		t.Fatalf("pending physical handover invented delivery %+v", issued)
	}
	initialAudit := commercialData[businesshttp.PMSAuditResult](t, c.must(c.owner, "POST", "/night-audit/validate", businesshttp.PMSAuditCommand{PropertyID: c.property.ID, Reason: "Retain the unresolved configured credential outcome in the actual night audit"}, 201))
	if initialAudit.Snapshot.Checks["UNKNOWN_CONFIGURED_ACCESS_OUTCOME"] != 1 {
		t.Fatal("night audit ignored an actual unresolved configured access outcome")
	}
	cmd := pmsAccessAck(c, issued, "key-inventory:actual-issued-item-201")
	path := "/access-grants/" + issued.Reference.ID.String() + "/acknowledge"
	key := ids.New().String()
	code, raw := c.call(c.owner, "POST", path, cmd, map[string]string{"Idempotency-Key": key})
	if code != 200 {
		t.Fatalf("actual handover %d %s", code, raw)
	}
	confirmed := commercialData[businesshttp.PMSAccessView](t, raw)
	pmsAutomationContract(t, "PMSAccessView", raw)
	if !confirmed.DeliveryConfirmed || confirmed.EffectiveState != "RECORDED_ACTIVE" {
		t.Fatalf("physical observation lost %+v", confirmed)
	}
	if code, _ = c.call(c.owner, "POST", path, cmd, map[string]string{"Idempotency-Key": key}); code != 200 {
		t.Fatal("exact handover retry was not idempotent")
	}
	c.must(c.owner, "POST", path, cmd, 409)
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE hospitality_access_jobs SET request=request||'{"ends_at":"2099-01-01T00:00:00Z"}' WHERE id=$1`, issued.Reference.ID); e == nil {
		t.Fatal("accepted access window could be rewritten")
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE access_grant_references SET state='FAILED',version=version+1 WHERE id=$1`, issued.Reference.ID); e == nil {
		t.Fatal("access outcome changed without immutable observation/audit")
	}
	if _, e := c.db.Pool.Exec(t.Context(), `DELETE FROM hospitality_access_observations WHERE access_reference_id=$1`, issued.Reference.ID); e == nil {
		t.Fatal("actual access observations could be erased")
	}
	// The initial version1 history must not satisfy a later version's audit
	// obligation, even if an otherwise matching observation is inserted.
	e := database.WithTx(t.Context(), c.db.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE access_grant_references SET state='FAILED',version=version+1 WHERE id=$1`, issued.Reference.ID); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `INSERT INTO hospitality_access_observations(id,organisation_id,property_id,access_reference_id,reference_version,request_digest,state,origin,credential_reference,evidence_reference,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,'FAILED','MANUAL_OBSERVATION',$7,'negative-test-only-no-new-audit','Prove that initial history cannot authorize a later unaudited outcome',$8)`, ids.New(), c.owner.registered.OrganisationID, c.property.ID, issued.Reference.ID, confirmed.Reference.Version+1, issued.Command.RequestDigest, cmd.CredentialReference, c.owner.registered.AccountID)
		return e
	})
	if e == nil || database.SQLState(e) != database.CodeCheckViolation || !strings.Contains(e.Error(), "per-version") {
		t.Fatalf("old initial evidence satisfied a later unaudited version: %v", e)
	}
	e = database.WithTx(t.Context(), c.db.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		var oldSnapshot []byte
		if e := tx.QueryRow(ctx, `SELECT change_context FROM hospitality_history WHERE organisation_id=$1 AND entity_type='access_reference' AND entity_id=$2 AND entity_version=$3`, c.owner.registered.OrganisationID, issued.Reference.ID, confirmed.Reference.Version).Scan(&oldSnapshot); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE access_grant_references SET version=version+1 WHERE id=$1`, issued.Reference.ID); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO hospitality_access_observations(id,organisation_id,property_id,access_reference_id,reference_version,request_digest,state,origin,credential_reference,evidence_reference,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,'CONFIRMED','MANUAL_OBSERVATION',$7,$8,$9,$10)`, ids.New(), c.owner.registered.OrganisationID, c.property.ID, issued.Reference.ID, confirmed.Reference.Version+1, issued.Command.RequestDigest, cmd.CredentialReference, cmd.EvidenceReference, cmd.Reason, c.owner.registered.AccountID); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO hospitality_history(id,organisation_id,property_id,entity_id,entity_type,entity_version,action,change_context,created_by) VALUES($1,$2,$3,$4,'access_reference',$5,'ACCESS_OUTCOME_RECORDED',$6,$7)`, ids.New(), c.owner.registered.OrganisationID, c.property.ID, issued.Reference.ID, confirmed.Reference.Version+1, oldSnapshot, c.owner.registered.AccountID); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `INSERT INTO commercial_history(id,organisation_id,property_id,entity_type,entity_id,operation,version,actor_id,correlation_id,snapshot) VALUES($1,$2,$3,'access_reference',$4,'ACCESS_OUTCOME_RECORDED',$5,$6,'negative-test-only-reused-prior-audit',$7)`, ids.New(), c.owner.registered.OrganisationID, c.property.ID, issued.Reference.ID, confirmed.Reference.Version+1, c.owner.registered.AccountID, oldSnapshot)
		return e
	})
	if e == nil || database.SQLState(e) != database.CodeCheckViolation || !strings.Contains(e.Error(), "per-version") {
		t.Fatalf("an older security audit could satisfy a later source version: %v", e)
	}
	pmsDeferredDeparture(c, v, d)
	issued = pmsAccessFor(c, d.Assignment.ID, "ISSUE")
	if issued.EffectiveState != "REVOCATION_REQUIRED" {
		t.Fatal("departed guest presented as active access")
	}
	revoke := pmsAccessFor(c, d.Assignment.ID, "REVOKE")
	departureAudit := commercialData[businesshttp.PMSAuditResult](t, c.must(c.owner, "POST", "/night-audit/validate", businesshttp.PMSAuditCommand{PropertyID: c.property.ID, Reason: "Actual physical departure cannot hide pending return of an issued credential"}, 201))
	if departureAudit.Snapshot.Checks["UNRESOLVED_ACCESS_REVOCATION"] != 1 {
		t.Fatal("night audit ignored an actual pending credential return")
	}
	bad := pmsAccessAck(c, revoke, "key-inventory:another-physical-item")
	c.must(c.owner, "POST", "/access-grants/"+revoke.Reference.ID.String()+"/acknowledge", bad, 409)
	ret := pmsAccessAck(c, revoke, cmd.CredentialReference)
	c.must(c.owner, "POST", "/access-grants/"+revoke.Reference.ID.String()+"/acknowledge", ret, 200)
	issued = pmsAccessFor(c, d.Assignment.ID, "ISSUE")
	if issued.EffectiveState != "REVOKED" || !issued.RevocationConfirmed {
		t.Fatalf("actual key return not retained %+v", issued)
	}
	returnedAudit := commercialData[businesshttp.PMSAuditResult](t, c.must(c.owner, "POST", "/night-audit/validate", businesshttp.PMSAuditCommand{PropertyID: c.property.ID, Reason: "Re-read the actual returned credential while retaining unrelated outstanding financial obligations"}, 201))
	if returnedAudit.Snapshot.Checks["UNRESOLVED_ACCESS_REVOCATION"] != 0 || returnedAudit.Snapshot.Checks["UNKNOWN_CONFIGURED_ACCESS_OUTCOME"] != 0 || returnedAudit.Snapshot.SourceDigest == departureAudit.Snapshot.SourceDigest {
		t.Fatal("qualified credential return did not invalidate the old audit source or remove only its own access exception")
	}
	pmsAutomationContract(t, "PMSAccessList", c.must(c.owner, "GET", "/access-grants", nil, 200))
	pmsAutomationContract(t, "PMSAccessConfigurationList", c.must(c.owner, "GET", "/hospitality-access-configurations", nil, 200))
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET revoked_at=now() WHERE organisation_id=$1 AND membership_id=(SELECT id FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2) AND capability='room_status:inspect'`, c.owner.registered.OrganisationID, c.owner.registered.AccountID); e != nil {
		t.Fatal(e)
	}
	if code, _ = c.call(c.owner, "POST", path, cmd, map[string]string{"Idempotency-Key": key}); code != 403 {
		t.Fatalf("cached physical handover bypassed current inspection authority %d", code)
	}
}
func TestBusinessPMSAccessExpiryAndDisabledConfigurationNeverFallBack(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	_, d := pmsCollectedStay(c, func() { pmsAccessConfigure(c, "MANUAL_CARD", "", 0, c.service.Now().Add(2*time.Minute)) })
	issued := pmsAccessFor(c, d.Assignment.ID, "ISSUE")
	cmd := pmsAccessAck(c, issued, "card-inventory:actual-issued-card")
	c.must(c.owner, "POST", "/access-grants/"+issued.Reference.ID.String()+"/acknowledge", cmd, 200)
	now := c.service.Now()
	c.service.Now = func() time.Time { return now.Add(3 * time.Minute) }
	if current := pmsAccessFor(c, d.Assignment.ID, "ISSUE"); current.EffectiveState != "REVOCATION_REQUIRED" {
		t.Fatal("expired access remained locally current before worker")
	}
	pmsAccessDue(c)
	if e := c.domain.ProcessPMSAccess(t.Context(), 20); e != nil {
		t.Fatal(e)
	}
	revoke := pmsAccessFor(c, d.Assignment.ID, "REVOKE")
	if revoke.RevocationConfirmed || revoke.Command.DeliveryState != "WAITING_MANUAL" {
		t.Fatal("expiry fabricated actual card revocation")
	}
	pmsAccessConfigure(c, "DISABLED", "", 1, c.service.Now().Add(time.Minute))
	c.must(c.owner, "POST", "/access-grants/"+issued.Reference.ID.String()+"/acknowledge", cmd, 409)
	if _, e := c.db.Pool.Exec(t.Context(), `DELETE FROM hospitality_access_configurations WHERE room_id=$1`, c.unit.ID); e == nil {
		t.Fatal("configuration history could be removed to reactivate an older policy")
	}
}

// This fake certifies the typed contract and local state machine only. There is
// no real configured provider; it cannot certify live device authentication.
type pmsAccessAdapterFake struct {
	mu                               sync.Mutex
	now                              func() time.Time
	results                          map[ids.ID]smartaccess.Observation
	issueCalls, revokeCalls, lookups int
	commitThenTimeout                bool
	forge                            bool
	beforeIssue                      func()
}

func (f *pmsAccessAdapterFake) Inspect(ctx context.Context, d smartaccess.Device) (smartaccess.DeviceProof, error) {
	return smartaccess.DeviceProof{Device: d, AccountReference: "authenticated-adapter-account-reference", EvidenceReference: "provider-owned-device-inspection-reference", ObservedAt: f.now(), ExpiresAt: f.now().Add(24 * time.Hour)}, ctx.Err()
}
func accessFakeDigest(r smartaccess.Request) string {
	b, _ := json.Marshal(r)
	return security.SHA256Hex(b)
}
func (f *pmsAccessAdapterFake) Execute(ctx context.Context, r smartaccess.Request, key string) (smartaccess.Observation, error) {
	if requestcontext.From(ctx).RequestID != "pms-access:"+r.ID.String() {
		return smartaccess.Observation{}, errors.New("original operation correlation missing")
	}
	if r.Operation == "ISSUE" && f.beforeIssue != nil {
		f.beforeIssue()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if key != "pms-access:"+r.ID.String()+":v1" {
		return smartaccess.Observation{}, errors.New("original idempotency key missing")
	}
	o := smartaccess.Observation{RequestDigest: accessFakeDigest(r), EvidenceReference: "authenticated-provider-original-command-receipt", ObservedAt: f.now()}
	if r.Operation == "ISSUE" {
		f.issueCalls++
		o.State = "ACTIVE"
		o.CredentialReference = "provider-credential-reference:" + r.ID.String()
	} else {
		f.revokeCalls++
		o.State = "REVOKED"
		o.CredentialReference = f.results[r.IssueID].CredentialReference
	}
	f.results[r.ID] = o
	if f.forge {
		o.RequestDigest = security.SHA256Hex([]byte("wrong-provider-request"))
	}
	if f.commitThenTimeout && r.Operation == "ISSUE" {
		f.commitThenTimeout = false
		return smartaccess.Observation{}, context.DeadlineExceeded
	}
	return o, ctx.Err()
}
func (f *pmsAccessAdapterFake) Lookup(ctx context.Context, r smartaccess.Request, key string) (smartaccess.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	o, found := f.results[r.ID]
	if !found {
		o = smartaccess.Observation{RequestDigest: accessFakeDigest(r), State: "ABSENT", EvidenceReference: "definitive-original-key-absence-reference"}
	}
	o.ObservedAt = f.now()
	return o, ctx.Err()
}
func pmsSmartFake(c *commercialHarness) *pmsAccessAdapterFake {
	f := &pmsAccessAdapterFake{now: func() time.Time { return c.service.Now() }, results: map[ids.ID]smartaccess.Observation{}}
	c.domain.AccessAdapters = map[string]smartaccess.API{"DISPOSABLE_TEST_ADAPTER": f}
	return f
}
func TestBusinessPMSSmartAccessLostResponseRecoversOriginalGrantAndRevokesAfterDeparture(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	f := pmsSmartFake(c)
	f.commitThenTimeout = true
	v, d := pmsCollectedStay(c, func() {
		pmsAccessConfigure(c, "SMART_DEVICE", "DISPOSABLE_TEST_ADAPTER", 0, c.service.Now().Add(30*time.Minute))
	})
	issued := pmsAccessFor(c, d.Assignment.ID, "ISSUE")
	c.must(c.owner, "POST", "/access-grants/"+issued.Reference.ID.String()+"/acknowledge", pmsAccessAck(c, issued, "cannot-assert-smart-device-truth"), 403)
	f.beforeIssue = func() {
		var committed bool
		if e := c.db.Pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM stay_records s JOIN reservations r ON r.organisation_id=s.organisation_id AND r.id=s.reservation_id WHERE s.id=$1 AND r.state='CHECKED_IN')`, d.Stay.ID).Scan(&committed); e != nil || !committed {
			t.Fatal("provider called before actual local stay commit")
		}
	}
	if e := c.domain.ProcessPMSAccess(t.Context(), 1); e == nil {
		t.Fatal("ambiguous provider success was hidden")
	}
	unknown := pmsAccessFor(c, d.Assignment.ID, "ISSUE")
	if unknown.DeliveryConfirmed || unknown.Reference.State != "UNKNOWN" || unknown.Command.DeliveryState != "RETRY" {
		t.Fatalf("ambiguous result became success %+v", unknown)
	}
	pmsDeferredDeparture(c, v, d)
	pmsAccessDue(c)
	// Simulate a crashed claimant's expired SQL lease. A newly reconstructed
	// worker must recover the persisted original command, not issue another key.
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE hospitality_access_jobs SET delivery_state='RUNNING',lease_owner='disposable-crashed-claimant',lease_generation=lease_generation+1,lease_expires_at=$2,available_at=$2 WHERE id=$1`, issued.Reference.ID, c.service.Now().Add(-time.Second)); e != nil {
		t.Fatal(e)
	}
	restarted := *c.domain
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- restarted.ProcessPMSAccess(t.Context(), 10) }()
	}
	for range 2 {
		if e := <-results; e != nil && !strings.Contains(e.Error(), "ACCESS_ISSUE_RECONCILIATION_REQUIRED") {
			t.Fatal(e)
		}
	}
	// A revocation can be deferred while the other worker owns original lookup.
	pmsAccessDue(c)
	if e := c.domain.ProcessPMSAccess(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
	current := pmsAccessFor(c, d.Assignment.ID, "ISSUE")
	rev := pmsAccessFor(c, d.Assignment.ID, "REVOKE")
	if current.EffectiveState != "REVOKED" || !rev.RevocationConfirmed {
		t.Fatalf("original credential remained active after recovery %+v %+v", current, rev)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.issueCalls != 1 || f.revokeCalls != 1 || f.lookups < 1 {
		t.Fatalf("ambiguous issue was repeated or not read back %d/%d/%d", f.issueCalls, f.revokeCalls, f.lookups)
	}
}
func TestBusinessPMSSmartAccessCompensatesSourceChangeDuringProviderCall(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	f := pmsSmartFake(c)
	v, d := pmsCollectedStay(c, func() {
		pmsAccessConfigure(c, "SMART_DEVICE", "DISPOSABLE_TEST_ADAPTER", 0, c.service.Now().Add(30*time.Minute))
	})
	f.beforeIssue = func() { pmsDeferredDeparture(c, v, d) }
	if e := c.domain.ProcessPMSAccess(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	current := pmsAccessFor(c, d.Assignment.ID, "ISSUE")
	if !current.DeliveryConfirmed || current.EffectiveState != "REVOCATION_REQUIRED" {
		t.Fatal("late provider success presented as still authorised current access")
	}
	if e := c.domain.ProcessPMSAccess(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
	if current = pmsAccessFor(c, d.Assignment.ID, "ISSUE"); current.EffectiveState != "REVOKED" {
		t.Fatal("late provider success was not durably revoked")
	}
}
func TestBusinessPMSSmartAccessForgedObservationQuarantinesWithoutDelivery(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	f := pmsSmartFake(c)
	f.forge = true
	_, d := pmsCollectedStay(c, func() {
		pmsAccessConfigure(c, "SMART_DEVICE", "DISPOSABLE_TEST_ADAPTER", 0, c.service.Now().Add(30*time.Minute))
	})
	if e := c.domain.ProcessPMSAccess(t.Context(), 1); e == nil {
		t.Fatal("wrong original request proof accepted")
	}
	v := pmsAccessFor(c, d.Assignment.ID, "ISSUE")
	if v.DeliveryConfirmed || v.Command.DeliveryState != "QUARANTINED" {
		t.Fatalf("forged provider result escaped quarantine %+v", v)
	}
	f.forge = false
	c.must(c.owner, "POST", "/access-grants/"+v.Reference.ID.String()+"/retry", businesshttp.PMSVersionCommand{Version: v.Reference.Version, Reason: "Repair the adapter binding and read back the exact original command without another credential"}, 202)
	if e := c.domain.ProcessPMSAccess(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	if v = pmsAccessFor(c, d.Assignment.ID, "ISSUE"); !v.DeliveryConfirmed {
		t.Fatal("qualified original-command repair did not recover actual provider result")
	}
	if f.issueCalls != 1 || f.lookups != 1 {
		t.Fatal("quarantine recovery created another credential")
	}
	// Correct request identity alone cannot replace the original credential
	// reference after its first authenticated confirmation.
	f.mu.Lock()
	changed := f.results[v.Reference.ID]
	changed.CredentialReference = "provider-credential-reference:another-original-command"
	f.results[v.Reference.ID] = changed
	f.mu.Unlock()
	c.must(c.owner, "POST", "/access-grants/"+v.Reference.ID.String()+"/retry", businesshttp.PMSVersionCommand{Version: v.Reference.Version, Reason: "Read back the original credential without accepting a substituted provider reference"}, 202)
	if e := c.domain.ProcessPMSAccess(t.Context(), 1); e == nil {
		t.Fatal("a different credential reference passed original-command readback")
	}
	if v = pmsAccessFor(c, d.Assignment.ID, "ISSUE"); v.DeliveryConfirmed || v.Command.DeliveryState != "QUARANTINED" {
		t.Fatal("credential substitution became confirmed delivery")
	}
}
func TestBusinessPMSUnconfiguredAccessRemainsUnknownUntilExplicitScopedRequest(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	_, d := pmsCollectedStay(c)
	v := pmsAccessFor(c, d.Assignment.ID, "ISSUE")
	other := c.user()
	c.must(other, "GET", "/access-grants/"+v.Reference.ID.String(), nil, 404)
	limited := c.user()
	c.secure(limited)
	c.join(limited, c.owner, []string{"pms:read"})
	branch := c.inventoryBranch(c.owner)
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET scope_kind='BRANCH',scope_id=$3 WHERE organisation_id=$1 AND membership_id=(SELECT id FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2) AND capability='pms:read'`, c.owner.registered.OrganisationID, limited.registered.AccountID, branch); e != nil {
		t.Fatal(e)
	}
	c.must(limited, "GET", "/access-grants/"+v.Reference.ID.String(), nil, 403)
	isolated := commercialData[struct {
		Items []businesshttp.PMSAccessView `json:"items"`
	}](t, c.must(limited, "GET", "/access-grants", nil, 200))
	if len(isolated.Items) != 0 {
		t.Fatal("branch-scoped access listing exposed another physical room")
	}
	if v.Command != nil || v.Reference.State != "UNKNOWN" {
		t.Fatal("unconfigured room invented device issuance")
	}
	pmsAccessConfigure(c, "MANUAL_KEY", "", 0, c.service.Now().Add(30*time.Minute))
	v = pmsAccessFor(c, d.Assignment.ID, "ISSUE")
	if v.Command != nil {
		t.Fatal("configuration silently issued a historical guest credential")
	}
	c.must(c.owner, "POST", "/access-grants/"+v.Reference.ID.String()+"/request", businesshttp.PMSVersionCommand{Version: v.Reference.Version, Reason: "Explicitly bind the existing active stay to the current reviewed physical access configuration"}, 202)
	v = pmsAccessFor(c, d.Assignment.ID, "ISSUE")
	if v.Command == nil || v.Command.DeliveryState != "WAITING_MANUAL" {
		t.Fatal("explicit scoped handover request not retained")
	}
	in := businesshttp.PMSAccessConfigurationCommand{PropertyID: c.property.ID, RoomID: c.unit.ID, ExpectedVersion: 1, Method: "SMART_DEVICE", Provider: "UNCONFIGURED_PROVIDER", DeviceReference: "actual-device-reference", ExpiresAt: c.service.Now().Add(time.Minute), Reason: "Unavailable adapter must remain an explicit provider boundary"}
	c.must(c.owner, "POST", "/hospitality-access-configurations", in, 503)
}

func TestBusinessPMSAccessCurrentRoleRevocationStopsIssuanceButStillRevokesDevice(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	f := pmsSmartFake(c)
	_, d := pmsCollectedStay(c, func() {
		pmsAccessConfigure(c, "SMART_DEVICE", "DISPOSABLE_TEST_ADAPTER", 0, c.service.Now().Add(30*time.Minute))
	})
	if e := c.domain.ProcessPMSAccess(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET revoked_at=now() WHERE organisation_id=$1 AND membership_id=(SELECT id FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2) AND capability='pms:write'`, c.owner.registered.OrganisationID, c.owner.registered.AccountID); e != nil {
		t.Fatal(e)
	}
	current := pmsAccessFor(c, d.Assignment.ID, "ISSUE")
	if current.EffectiveState != "REVOCATION_REQUIRED" {
		t.Fatal("removed maker authority still authorized current access")
	}
	pmsAccessDue(c)
	if e := c.domain.ProcessPMSAccess(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
	if current = pmsAccessFor(c, d.Assignment.ID, "ISSUE"); current.EffectiveState != "REVOKED" {
		t.Fatal("security revocation improperly required the old maker's live permission")
	}
	if f.issueCalls != 1 || f.revokeCalls != 1 {
		t.Fatal("revoked maker caused a fresh grant or lost the actual device revocation")
	}
}
func TestBusinessPMSAccessActualAuthorityReadFailureQueuesConservativeManualReturn(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	_, d := pmsCollectedStay(c, func() { pmsAccessConfigure(c, "MANUAL_KEY", "", 0, c.service.Now().Add(30*time.Minute)) })
	issued := pmsAccessFor(c, d.Assignment.ID, "ISSUE")
	cmd := pmsAccessAck(c, issued, "key-inventory:actual-source-failure-key")
	c.must(c.owner, "POST", "/access-grants/"+issued.Reference.ID.String()+"/acknowledge", cmd, 200)
	role := "bo_access_source_" + strings.ReplaceAll(ids.New().String(), "-", "")
	quoted := pgx.Identifier{role}.Sanitize()
	if _, e := c.db.Pool.Exec(t.Context(), "CREATE ROLE "+quoted+" NOLOGIN"); e != nil {
		t.Fatal(e)
	}
	original := c.domain.Auth.Service.Pool
	var restricted *pgxpool.Pool
	t.Cleanup(func() {
		c.domain.Auth.Service.Pool = original
		if restricted != nil {
			restricted.Close()
		}
		_, _ = c.db.Pool.Exec(context.Background(), "DROP OWNED BY "+quoted)
		_, _ = c.db.Pool.Exec(context.Background(), "DROP ROLE "+quoted)
	})
	for _, sql := range []string{"GRANT USAGE ON SCHEMA public TO " + quoted, "GRANT SELECT,INSERT,UPDATE ON ALL TABLES IN SCHEMA public TO " + quoted, "REVOKE SELECT ON staff_scope_grants FROM " + quoted} {
		if _, e := c.db.Pool.Exec(t.Context(), sql); e != nil {
			t.Fatal(e)
		}
	}
	config := original.Config().Copy()
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error { _, e := conn.Exec(ctx, "SET ROLE "+quoted); return e }
	var e error
	restricted, e = pgxpool.NewWithConfig(t.Context(), config)
	if e != nil {
		t.Fatal(e)
	}
	c.domain.Auth.Service.Pool = restricted
	if e = c.domain.ProcessPMSAccess(t.Context(), 10); e == nil || !strings.Contains(e.Error(), "ACCESS_AUTHORITY_SOURCE_READ_FAILED") {
		t.Fatalf("actual source read failure was hidden %v", e)
	}
	c.domain.Auth.Service.Pool = original
	revoke := pmsAccessFor(c, d.Assignment.ID, "REVOKE")
	if revoke.RevocationConfirmed || revoke.Command == nil || revoke.Command.DeliveryState != "WAITING_MANUAL" {
		t.Fatal("source failure invented physical key return")
	}
	var notices int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM hospitality_history WHERE entity_type='access_reference' AND entity_id=$1 AND change_context->>'reason' LIKE 'Recorded access authority source could not be read%'`, revoke.Reference.ID).Scan(&notices); e != nil || notices != 1 {
		t.Fatalf("source failure did not retain security evidence %d %v", notices, e)
	}
	c.must(c.owner, "POST", "/access-grants/"+revoke.Reference.ID.String()+"/acknowledge", pmsAccessAck(c, revoke, cmd.CredentialReference), 200)
}

func TestBusinessPMSAccessNeverDispatchesAfterMakerAuthorityEnds(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	f := pmsSmartFake(c)
	_, d := pmsCollectedStay(c, func() {
		pmsAccessConfigure(c, "SMART_DEVICE", "DISPOSABLE_TEST_ADAPTER", 0, c.service.Now().Add(30*time.Minute))
	})
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET revoked_at=now() WHERE organisation_id=$1 AND membership_id=(SELECT id FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2) AND capability='pms:write'`, c.owner.registered.OrganisationID, c.owner.registered.AccountID); e != nil {
		t.Fatal(e)
	}
	if e := c.domain.ProcessPMSAccess(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
	issued := pmsAccessFor(c, d.Assignment.ID, "ISSUE")
	revoke := pmsAccessFor(c, d.Assignment.ID, "REVOKE")
	if issued.Reference.State != "FAILED" || issued.Command.DeliveryState != "CANCELLED" || !revoke.RevocationConfirmed || issued.EffectiveState != "REVOKED" {
		t.Fatalf("unissued original command did not retain definitive local absence %+v %+v", issued, revoke)
	}
	if f.issueCalls != 0 || f.revokeCalls != 0 || f.lookups != 0 {
		t.Fatal("authority loss dispatched a provider operation instead of cancelling the unsent original command")
	}
	var origin string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT origin FROM hospitality_access_observations WHERE access_reference_id=$1 ORDER BY reference_version DESC LIMIT 1`, revoke.Reference.ID).Scan(&origin); e != nil || origin != "SERVICE_SOURCE" {
		t.Fatal("local never-dispatched evidence was misrepresented as an actual provider acknowledgement")
	}
}
