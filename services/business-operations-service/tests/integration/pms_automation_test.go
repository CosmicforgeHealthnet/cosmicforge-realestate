package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func pmsAutomationContract(t *testing.T, schema string, body []byte) {
	t.Helper()
	root, e := os.ReadFile(filepath.Join("../../../..", "contracts/public/openapi/business-operations.yaml"))
	if e != nil {
		t.Fatal(e)
	}
	var source map[string]json.RawMessage
	if e = json.Unmarshal(root, &source); e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(map[string]any{"$ref": "#/components/schemas/" + schema, "components": source["components"]})
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "response.schema.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if e = json.Unmarshal(body, &envelope); e != nil {
		t.Fatal(e)
	}
	testkit.ValidateJSONSchema(t, path, envelope.Data)
}

func pmsAutomationFixture(t *testing.T) (*commercialHarness, time.Time, businesshttp.PMSAuditResult) {
	t.Helper()
	c := newCommercialHTTP(t)
	reviewer := c.user()
	c.secure(reviewer)
	c.join(reviewer, c.owner, []string{"approvals:decide", "night_audit:approve"})
	c.approver = reviewer
	now := time.Now().UTC()
	loc, e := time.LoadLocation(c.property.Timezone)
	if e != nil {
		t.Fatal(e)
	}
	policy := businesshttp.PMSHospitalityPolicyCommand{PropertyID: c.property.ID, CutoffLocal: now.Add(2 * time.Minute).In(loc).Format("15:04"), Reason: "Configure the actual independently approved local cutoff for the bounded day worker"}
	op := policy
	policy.ApprovalID = c.approval("COMMERCIAL_PMS_BUSINESS_DAY_POLICY", "night_audit:approve", "", nil, "", map[string]any{"command": op})
	c.must(c.owner, "POST", "/hospitality-policies", policy, 201)
	audit := commercialData[businesshttp.PMSAuditResult](t, c.must(c.owner, "POST", "/night-audit/validate", businesshttp.PMSAuditCommand{PropertyID: c.property.ID, Reason: "Open the actual property business day before testing scheduled closing"}, 201))
	return c, now, audit
}
func pmsAutomationGrant(c *commercialHarness, now time.Time, mode string, expected int64) businesshttp.PMSDayAutomationPolicy {
	c.t.Helper()
	var cutoffVersion int64
	if e := c.db.Pool.QueryRow(c.t.Context(), `SELECT coalesce(max(version),0) FROM hospitality_policies WHERE organisation_id=$1 AND property_id=$2`, c.owner.registered.OrganisationID, c.property.ID).Scan(&cutoffVersion); e != nil {
		c.t.Fatal(e)
	}
	command := businesshttp.PMSDayAutomationCommand{PropertyID: c.property.ID, ExpectedVersion: expected, Mode: mode, HospitalityPolicyVersion: cutoffVersion, EffectiveAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), MaxDaysPerRun: 1, Reason: "Authorize bounded service-owned night auditing without waiving any operational exception"}
	if mode != "MANUAL" {
		op := command
		command.ApprovalID = c.approval("COMMERCIAL_PMS_DAY_AUTOMATION", "night_audit:approve", "", nil, "", map[string]any{"command": op})
	}
	return commercialData[businesshttp.PMSDayAutomationPolicy](c.t, c.must(c.owner, "POST", "/hospitality-day-automation", command, 201))
}
func TestBusinessPMSDayAutomationClosesOnceAfterCutoffWithoutHumanImpersonation(t *testing.T) {
	c, now, audit := pmsAutomationFixture(t)
	p := pmsAutomationGrant(c, now, "CLOSE_IF_CLEAR", 0)
	if e := c.domain.ProcessPMSDayAutomation(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
	var n int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM business_day_closes WHERE business_day_id=$1`, audit.Snapshot.Day.ID).Scan(&n); e != nil || n != 0 {
		t.Fatalf("closed before deadline %d %v", n, e)
	}
	c.domain.Auth.Service.Now = func() time.Time { return now.Add(3 * time.Minute) }
	errors := make(chan error, 2)
	for range 2 {
		go func() { errors <- c.domain.ProcessPMSDayAutomation(t.Context(), 10) }()
	}
	for range 2 {
		if e := <-errors; e != nil {
			t.Fatal(e)
		}
	}
	var actor *ids.ID
	var service string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT created_by,created_by_service FROM business_day_closes WHERE business_day_id=$1`, audit.Snapshot.Day.ID).Scan(&actor, &service); e != nil || actor != nil || service != "business-operations-service" {
		t.Fatalf("worker fabricated human authority %v %s %v", actor, service, e)
	}
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM hospitality_day_automation_runs WHERE policy_id=$1 AND state='CLOSED'`, p.ID).Scan(&n); e != nil || n != 1 {
		t.Fatalf("repeated close %d %v", n, e)
	}
	if e := c.domain.ProcessPMSDayAutomation(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE hospitality_day_automation_policies SET mode='MANUAL' WHERE id=$1`, p.ID); e == nil {
		t.Fatal("policy evidence was mutable")
	}
	pmsAutomationContract(t, "PMSDayAutomationPolicyList", c.must(c.owner, "GET", "/hospitality-day-automation?property_id="+c.property.ID.String(), nil, 200))
	pmsAutomationContract(t, "PMSDayAutomationRunList", c.must(c.owner, "GET", "/hospitality-day-automation-runs?property_id="+c.property.ID.String(), nil, 200))
	outsider := c.user()
	isolated := commercialData[struct {
		Items []any `json:"items"`
	}](t, c.must(outsider, "GET", "/hospitality-day-automation-runs?property_id="+c.property.ID.String(), nil, 200))
	if len(isolated.Items) != 0 {
		t.Fatal("cross-tenant list exposed automation evidence")
	}
}
func TestBusinessPMSDayAutomationAuditsButNeverWaivesCashOrArrivalExceptions(t *testing.T) {
	c, now, audit := pmsAutomationFixture(t)
	pmsAutomationGrant(c, now, "CLOSE_IF_CLEAR", 0)
	drawer := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", "/cash-drawers", businesshttp.PMSDrawerCommand{PropertyID: c.property.ID, Code: "AUTOMATION-CASH-EXCEPTION", Currency: c.property.Currency, Reason: "An actual open cashier shift must block automatic business-day close"}, 201))
	c.must(c.owner, "POST", "/cashier-shifts/open", businesshttp.PMSCashierOpenCommand{DrawerID: drawer.ID, OpeningMinor: 1000, Reason: "Keep actual custody and financial reconciliation pending for the night auditor"}, 201)
	c.domain.Auth.Service.Now = func() time.Time { return now.Add(3 * time.Minute) }
	if e := c.domain.ProcessPMSDayAutomation(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
	c.domain.Auth.Service.Now = func() time.Time { return now.Add(5 * time.Minute) }
	if e := c.domain.ProcessPMSDayAutomation(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
	var closes, runs, audits int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM business_day_closes WHERE business_day_id=$1),(SELECT count(*) FROM hospitality_day_automation_runs WHERE business_day_id=$1 AND state='BLOCKED'),(SELECT count(*) FROM night_audits WHERE business_day_id=$1 AND state='EXCEPTIONS')`, audit.Snapshot.Day.ID).Scan(&closes, &runs, &audits); e != nil || closes != 0 || runs != 1 || audits != 1 {
		t.Fatalf("exception waived or duplicate evidence %d/%d/%d %v", closes, runs, audits, e)
	}
}
func TestBusinessPMSDayAutomationAuditOnlyAndManualModeDoNotClose(t *testing.T) {
	c, now, audit := pmsAutomationFixture(t)
	pmsAutomationGrant(c, now, "AUDIT_ONLY", 0)
	c.domain.Auth.Service.Now = func() time.Time { return now.Add(3 * time.Minute) }
	if e := c.domain.ProcessPMSDayAutomation(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
	pmsAutomationGrant(c, now.Add(3*time.Minute), "MANUAL", 1)
	pmsAutomationContract(t, "PMSDayAutomationPolicyList", c.must(c.owner, "GET", "/hospitality-day-automation", nil, 200))
	c.domain.Auth.Service.Now = func() time.Time { return now.Add(5 * time.Minute) }
	if e := c.domain.ProcessPMSDayAutomation(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
	var closes, audits int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM business_day_closes WHERE business_day_id=$1),(SELECT count(*) FROM hospitality_day_automation_runs WHERE business_day_id=$1 AND state='AUDITED')`, audit.Snapshot.Day.ID).Scan(&closes, &audits); e != nil || closes != 0 || audits != 1 {
		t.Fatalf("mode expanded authority %d/%d %v", closes, audits, e)
	}
}
func TestBusinessPMSDayAutomationStopsOnRevocationExpiryAuthorityOrCutoffChange(t *testing.T) {
	for _, scenario := range []string{"revoked", "expired", "membership_suspended", "scope_revoked", "scope_requires_mfa", "cutoff_changed", "open_day_cutoff_mismatch", "latest_revoked_no_fallback"} {
		t.Run(scenario, func(t *testing.T) {
			c, now, audit := pmsAutomationFixture(t)
			p := pmsAutomationGrant(c, now, "CLOSE_IF_CLEAR", 0)
			switch scenario {
			case "revoked", "latest_revoked_no_fallback":
				if scenario == "latest_revoked_no_fallback" {
					p = pmsAutomationGrant(c, now, "CLOSE_IF_CLEAR", 1)
				}
				c.must(c.owner, "POST", "/hospitality-day-automation/"+p.ID.String()+"/revoke", businesshttp.PMSVersionCommand{Version: p.Version, Reason: "Immediately withdraw bounded unattended close authority without reviving an older policy"}, 201)
			case "membership_suspended":
				if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_memberships SET status='SUSPENDED' WHERE id=$1`, p.AuthorizingMembershipID); e != nil {
					t.Fatal(e)
				}
			case "scope_revoked":
				if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET revoked_at=now() WHERE organisation_id=$1 AND membership_id=$2 AND capability='night_audit:write'`, p.OrganisationID, p.AuthorizingMembershipID); e != nil {
					t.Fatal(e)
				}
			case "scope_requires_mfa":
				if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET require_mfa=true WHERE organisation_id=$1 AND membership_id=$2 AND capability='night_audit:write'`, p.OrganisationID, p.AuthorizingMembershipID); e != nil {
					t.Fatal(e)
				}
			case "cutoff_changed", "open_day_cutoff_mismatch":
				policy := businesshttp.PMSHospitalityPolicyCommand{PropertyID: c.property.ID, ExpectedVersion: 1, CutoffLocal: "04:00", Reason: "Any newly approved cutoff must invalidate the old unattended execution policy"}
				op := policy
				policy.ApprovalID = c.approval("COMMERCIAL_PMS_BUSINESS_DAY_POLICY", "night_audit:approve", "", nil, "", map[string]any{"command": op})
				c.must(c.owner, "POST", "/hospitality-policies", policy, 201)
				if scenario == "open_day_cutoff_mismatch" {
					pmsAutomationGrant(c, now, "CLOSE_IF_CLEAR", 1)
				}
			}
			after := now.Add(3 * time.Minute)
			if scenario == "expired" {
				after = now.Add(2 * time.Hour)
			}
			c.domain.Auth.Service.Now = func() time.Time { return after }
			if e := c.domain.ProcessPMSDayAutomation(t.Context(), 10); e != nil {
				t.Fatal(e)
			}
			var closes int
			if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM business_day_closes WHERE business_day_id=$1`, audit.Snapshot.Day.ID).Scan(&closes); e != nil || closes != 0 {
				t.Fatalf("unsafe close after %s %d %v", scenario, closes, e)
			}
		})
	}
}

func TestBusinessPMSDayAutomationRequiresIndependentApprovalAndDirectUnattendedScope(t *testing.T) {
	c, now, _ := pmsAutomationFixture(t)
	in := businesshttp.PMSDayAutomationCommand{PropertyID: c.property.ID, Mode: "CLOSE_IF_CLEAR", HospitalityPolicyVersion: 1, EffectiveAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), MaxDaysPerRun: 1, Reason: "Unattended execution requires independently approved direct scoped authority"}
	c.must(c.owner, "POST", "/hospitality-day-automation", in, 403)
	op := in
	in.ApprovalID = c.approval("COMMERCIAL_PMS_DAY_AUTOMATION", "night_audit:approve", "", nil, "", map[string]any{"command": op})
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET require_mfa=true WHERE organisation_id=$1 AND membership_id=(SELECT id FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2) AND capability='night_audit:write'`, c.owner.registered.OrganisationID, c.owner.registered.AccountID); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", "/hospitality-day-automation", in, 403)
	var policies int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM hospitality_day_automation_policies WHERE organisation_id=$1`, c.owner.registered.OrganisationID).Scan(&policies); e != nil || policies != 0 {
		t.Fatalf("invalid unattended authority was retained %d %v", policies, e)
	}
}

func TestBusinessPMSDayAutomationSourceFailureRollsBackBacksOffAndRequiresReviewedRepair(t *testing.T) {
	c, now, audit := pmsAutomationFixture(t)
	p := pmsAutomationGrant(c, now, "CLOSE_IF_CLEAR", 0)
	c.domain.Auth.Service.Now = func() time.Time { return now.Add(3 * time.Minute) }
	role := "cf_pms_source_" + strings.ReplaceAll(ids.New().String(), "-", "_")
	quoted := pgx.Identifier{role}.Sanitize()
	if _, e := c.db.Pool.Exec(t.Context(), "CREATE ROLE "+quoted+" NOLOGIN"); e != nil {
		t.Fatal(e)
	}
	var restricted *pgxpool.Pool
	original := c.domain.Auth.Service.Pool
	t.Cleanup(func() {
		c.domain.Auth.Service.Pool = original
		if restricted != nil {
			restricted.Close()
		}
		if _, e := c.db.Pool.Exec(context.Background(), "DROP OWNED BY "+quoted); e != nil {
			t.Error(e)
		}
		if _, e := c.db.Pool.Exec(context.Background(), "DROP ROLE "+quoted); e != nil {
			t.Error(e)
		}
	})
	for _, sql := range []string{"GRANT USAGE ON SCHEMA public TO " + quoted, "GRANT SELECT ON ALL TABLES IN SCHEMA public TO " + quoted, "REVOKE SELECT ON folio_items FROM " + quoted, "GRANT UPDATE ON organisations,staff_memberships,role_templates,business_accounts,hospitality_business_days TO " + quoted, "GRANT INSERT,UPDATE ON hospitality_day_automation_cursors,outbox_events TO " + quoted, "GRANT INSERT ON hospitality_day_automation_runs,hospitality_history,commercial_history,audit_events TO " + quoted} {
		if _, e := c.db.Pool.Exec(t.Context(), sql); e != nil {
			t.Fatal(e)
		}
	}
	config := c.db.Pool.Config()
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error { _, e := conn.Exec(ctx, "SET ROLE "+quoted); return e }
	var e error
	restricted, e = pgxpool.NewWithConfig(t.Context(), config)
	if e != nil {
		t.Fatal(e)
	}
	c.domain.Auth.Service.Pool = restricted
	if e = c.domain.ProcessPMSDayAutomation(t.Context(), 10); e == nil || !strings.Contains(e.Error(), "folio_items") {
		t.Fatalf("source failure was hidden or did not reach the actual source: %v", e)
	}
	if e = c.domain.ProcessPMSDayAutomation(t.Context(), 10); e != nil {
		t.Fatalf("backoff hot-looped: %v", e)
	}
	for range 11 {
		if _, e = c.db.Pool.Exec(t.Context(), `UPDATE hospitality_day_automation_cursors SET next_check_at=now()-interval '1 second' WHERE organisation_id=$1 AND property_id=$2`, p.OrganisationID, p.PropertyID); e != nil {
			t.Fatal(e)
		}
		if e = c.domain.ProcessPMSDayAutomation(t.Context(), 10); e == nil {
			t.Fatal("unfixed source stopped reporting failure")
		}
	}
	var count, closes, runs int
	var state string
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT consecutive_failures,state,(SELECT count(*) FROM business_day_closes WHERE business_day_id=$3),(SELECT count(*) FROM hospitality_day_automation_runs WHERE policy_id=$4 AND state='SOURCE_REVIEW_REQUIRED') FROM hospitality_day_automation_cursors WHERE organisation_id=$1 AND property_id=$2`, p.OrganisationID, p.PropertyID, audit.Snapshot.Day.ID, p.ID).Scan(&count, &state, &closes, &runs); e != nil || count != 12 || state != "QUARANTINED" || closes != 0 || runs != 1 {
		t.Fatalf("failure evidence or rollback lost %d/%s/%d/%d %v", count, state, closes, runs, e)
	}
	if _, e = c.db.Pool.Exec(t.Context(), "GRANT SELECT ON folio_items TO "+quoted); e != nil {
		t.Fatal(e)
	}
	if e = c.domain.ProcessPMSDayAutomation(t.Context(), 10); e != nil {
		t.Fatalf("quarantine silently retried repaired source: %v", e)
	}
	c.domain.Auth.Service.Pool = original
	pmsAutomationContract(t, "PMSDayAutomationPolicyList", c.must(c.owner, "GET", "/hospitality-day-automation", nil, 200))
	pmsAutomationContract(t, "PMSDayAutomationRunList", c.must(c.owner, "GET", "/hospitality-day-automation-runs", nil, 200))
	// A new scoped, independently approved policy is the explicit repair/reset.
	pmsAutomationGrant(c, now.Add(3*time.Minute), "CLOSE_IF_CLEAR", 1)
	if e = c.domain.ProcessPMSDayAutomation(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM business_day_closes WHERE business_day_id=$1`, audit.Snapshot.Day.ID).Scan(&closes); e != nil || closes != 1 {
		t.Fatalf("reviewed source recovery failed %d %v", closes, e)
	}
}
