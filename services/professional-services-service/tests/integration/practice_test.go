package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/professional-services-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/professional-services-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit/domaintest"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// verificationDouble is a recording contract double of the Verification
// Service case API (service-level test only).
type verificationDouble struct {
	mu      sync.Mutex
	status  string
	subject ids.ID
	id      ids.ID
	paths   []string
}

func (v *verificationDouble) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.paths = append(v.paths, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost && r.URL.Path == "/internal/v1/verifications" {
		var body struct {
			Subject struct {
				ID ids.ID `json:"subject_id"`
			} `json:"subject"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		v.id, v.subject, v.status = ids.New(), body.Subject.ID, "DRAFT"
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"verification_id": v.id}})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/submit") {
		v.status = "PROCESSING"
	}
	exp := time.Now().UTC().Add(365 * 24 * time.Hour)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"verification_id": v.id, "status": v.status, "expires_at": exp, "subject": map[string]any{"subject_domain": "professional-services", "subject_id": v.subject}}})
}

func deliver(t *testing.T, k *domaintest.Kit, eventType string, b domainkit.BusinessEvent) {
	t.Helper()
	env, _ := events.New(context.Background(), platform.BusinessOperationsService, eventType, b.Snapshot.Domain, b.TargetID, b)
	if err := database.WithTx(t.Context(), k.RT.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		return usecases.BusinessHandler(k.RT)(ctx, tx, env, b)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProfessionalVerifiedCredentialAppointmentFindingsCertificationAndDocuments(t *testing.T) {
	vd := &verificationDouble{}
	vs := httptest.NewServer(vd)
	t.Cleanup(vs.Close)
	k := domaintest.New(t, platform.ProfessionalServicesService, migrations.FS, func(rt *domainkit.Runtime) domainkit.App {
		c, err := httpx.NewClient(httpx.ClientConfig{BaseURL: vs.URL, Audience: string(platform.VerificationService), Tokens: rt.Cfg.Identity.Signer, MaxAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		return usecases.NewWithVerification(rt, c)
	})
	pro := k.Account("qs@pro.test")
	org, property, appt, cert, dev := ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	if r := k.Actor(pro, "PUT", "/profile", "p1", map[string]any{"profession": "QUANTITY_SURVEYOR", "license_number": "QS-1234"}); r.Status != 200 || r.Data()["state"] != "UNVERIFIED" {
		t.Fatalf("profile %d %s", r.Status, r.Raw)
	}
	deliver(t, k, "professional.appointment.updated.v1", domainkit.BusinessEvent{OrganisationID: org, TargetID: appt, PropertyID: &property, Operation: "CREATED", State: "REQUESTED", Version: 1,
		Snapshot: domainkit.BusinessDetail{Domain: "professional_appointment", Kind: "SURVEYOR", Title: "Structural survey", CounterpartyReference: k.RT.PartnerReference(pro), Attributes: map[string]any{"purpose": "Pre-sale survey"}}})
	deliver(t, k, "development.progress.updated.v1", domainkit.BusinessEvent{OrganisationID: org, TargetID: cert, Operation: "CREATED", State: "SUBMITTED", Version: 1,
		Snapshot: domainkit.BusinessDetail{Domain: "progress_certification", ScopeID: dev, Title: "Structure certificate", CounterpartyReference: k.RT.PartnerReference(pro), Attributes: map[string]any{"milestone_id": ids.New().String()}}})
	aid := k.Actor(pro, "GET", "/appointments", "", nil).Items()[0].(map[string]any)["id"].(string)
	cid := k.Actor(pro, "GET", "/certifications", "", nil).Items()[0].(map[string]any)["id"].(string)
	for _, step := range []string{"accept", "start"} {
		if r := k.Actor(pro, "POST", "/appointments/"+aid+"/"+step, step, map[string]any{}); r.Status != 200 {
			t.Fatalf("%s %d %s", step, r.Status, r.Raw)
		}
	}
	// Unverified professionals cannot record findings, report or certify.
	if r := k.Actor(pro, "POST", "/appointments/"+aid+"/findings", "f1", map[string]any{"kind": "DEFECT", "title": "Roof leak", "evidence_refs": []ids.ID{ids.New()}}); r.Status != 403 {
		t.Fatalf("unverified finding %d %s", r.Status, r.Raw)
	}
	if r := k.Actor(pro, "POST", "/certifications/"+cid+"/review", "c0", map[string]any{}); r.Status != 403 {
		t.Fatalf("unverified review %d", r.Status)
	}
	// Verification Service case: evidence attached and submitted; only its
	// VERIFIED outcome verifies the professional.
	if r := k.Actor(pro, "POST", "/profile/verification", "v1", map[string]any{"media_id": ids.New()}); r.Status != 202 || r.Data()["state"] != "PENDING" {
		t.Fatalf("verification %d %s", r.Status, r.Raw)
	}
	if r := k.Actor(pro, "POST", "/profile/verification/refresh", "v2", nil); r.Status != 200 || r.Data()["profile"].(map[string]any)["state"] != "PENDING" {
		t.Fatalf("refresh pending %d %s", r.Status, r.Raw)
	}
	vd.mu.Lock()
	vd.status = "VERIFIED"
	vd.mu.Unlock()
	if r := k.Actor(pro, "POST", "/profile/verification/refresh", "v3", nil); r.Status != 200 || r.Data()["profile"].(map[string]any)["state"] != "VERIFIED" {
		t.Fatalf("refresh verified %d %s", r.Status, r.Raw)
	}
	if got := strings.Join(vd.paths, ","); !strings.Contains(got, "/evidence") || !strings.Contains(got, "/submit") || !strings.Contains(got, "/process") {
		t.Fatalf("verification calls %s", got)
	}
	bizFinding := ids.New()
	k.Business.Reply = func(c domaintest.Call) (int, any) {
		if strings.HasSuffix(c.Path, "/findings") {
			return 201, map[string]any{"id": bizFinding, "state": "OPEN", "version": 1}
		}
		return 200, map[string]any{"record": map[string]any{"state": "OK"}}
	}
	findingBody := map[string]any{"kind": "DEFECT", "title": "Roof leak", "evidence_refs": []ids.ID{ids.New()}}
	f := k.Actor(pro, "POST", "/appointments/"+aid+"/findings", "f2", findingBody)
	if f.Status != 201 || f.Data()["business_finding_id"] != bizFinding.String() {
		t.Fatalf("finding %d %s", f.Status, f.Raw)
	}
	if r := k.Actor(pro, "POST", "/appointments/"+aid+"/findings", "f2", findingBody); r.Status != 201 || r.Data()["finding_id"] != f.Data()["finding_id"] {
		t.Fatalf("finding replay %d %s", r.Status, r.Raw)
	}
	if r := k.Actor(pro, "POST", "/appointments/"+aid+"/report", "r1", map[string]any{"report_reference": ids.New()}); r.Status != 200 || r.Data()["state"] != "REPORT_SUBMITTED" {
		t.Fatalf("report %d %s", r.Status, r.Raw)
	}
	k.Actor(pro, "POST", "/certifications/"+cid+"/review", "c1", map[string]any{})
	if r := k.Actor(pro, "POST", "/certifications/"+cid+"/certify", "c2", map[string]any{"certificate_reference": "QS-CERT-77"}); r.Status != 400 {
		t.Fatalf("certify without evidence %d", r.Status)
	}
	if r := k.Actor(pro, "POST", "/certifications/"+cid+"/certify", "c3", map[string]any{"certificate_reference": "QS-CERT-77", "evidence_refs": []ids.ID{ids.New()}}); r.Status != 200 || r.Data()["state"] != "CERTIFIED" {
		t.Fatalf("certify %d %s", r.Status, r.Raw)
	}
	// Every Business report carried the Verification Service reference.
	for _, c := range k.Business.Calls() {
		if c.Body["action"] == "CERTIFY" || c.Body["action"] == "SUBMIT_REPORT" {
			attrs := c.Body["attributes"].(map[string]any)
			if !strings.HasPrefix(attrs["professional_verification_reference"].(string), "verification-service:") {
				t.Fatalf("missing verification reference %+v", c)
			}
		}
	}
	// Purpose-limited document access is decided by Business, live.
	allowed := true
	k.Business.Reply = func(c domaintest.Call) (int, any) {
		if strings.Contains(c.Path, "/data-room-grants/") {
			return 200, map[string]any{"allowed": allowed, "purpose": "Survey review", "download_policy": "VIEW_ONLY"}
		}
		return 200, map[string]any{"record": map[string]any{"state": "OK"}}
	}
	grant := ids.New().String()
	if r := k.Actor(pro, "GET", "/appointments/"+aid+"/documents/"+grant+"/authorization", "", nil); r.Data()["allowed"] != true {
		t.Fatalf("document %s", r.Raw)
	}
	allowed = false
	if r := k.Actor(pro, "GET", "/appointments/"+aid+"/documents/"+grant+"/authorization", "", nil); r.Data()["allowed"] != false {
		t.Fatalf("revoked document %s", r.Raw)
	}
	if r := k.Service(platform.BusinessOperationsService, org, "GET", "/professionals/"+pro.String()+"/credential", "", nil); r.Data()["verified"] != true {
		t.Fatalf("credential readback %s", r.Raw)
	}
	if _, err := k.RT.Pool.Exec(t.Context(), `UPDATE findings SET title='x'`); err == nil {
		t.Fatal("finding rewritten")
	}
}
