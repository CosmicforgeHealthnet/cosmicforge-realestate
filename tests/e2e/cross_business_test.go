package e2e

// Business Operations onboarding and supply through the gateway only, as a
// real organisation would: owner registration with TOTP, staff invitation
// delivered by the Notification Service, KYB, operating capacity and property
// authority decided by the Verification Service (sandbox provider, evidence
// stored by the Media Service), approvals by a distinct staff member, and
// canonical inventory, rates and calendar.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/totp"
)

type bizUser struct {
	email, password string
	account, org    ids.ID
	access          string
	totpSecret      []byte
	lastStep        int64
	mfaAt           time.Time
}

type bizEnv struct {
	p                 *crossEnv
	t                 *testing.T
	owner, approver   *bizUser
	policies          map[string]bool
	property, unit    ids.ID
	propertyVersion   int64
	pool, product     ids.ID
	productVersion    int64
	poolVersion       int64
	rate              ids.ID
	dateFrom, dateTo  string
	paymentPolicy     ids.ID
	connection        ids.ID
	connectionVersion int64
}

// call performs a Business API call through the gateway.
func (b *bizEnv) call(u *bizUser, method, path string, body any) response {
	b.t.Helper()
	token := ""
	if u != nil {
		token = u.access
	}
	return send(b.t, method, b.p.gateway+"/api/v1/business"+path, token, "", body, "e2e-"+ids.New().String())
}

func (b *bizEnv) must(u *bizUser, method, path string, body any, status int) response {
	b.t.Helper()
	if u != nil && method != "GET" {
		b.stepUp(u)
	}
	r := b.call(u, method, path, body)
	if r.status != status {
		b.t.Fatalf("%s %s: got %d want %d %s", method, path, r.status, status, r.body)
	}
	return r
}

func dataOf[T any](t *testing.T, r response) T {
	t.Helper()
	var out T
	r.data(t, &out)
	return out
}

// code returns a TOTP code for a step strictly after the last one used.
func (u *bizUser) code(t *testing.T) string {
	for {
		step := totp.Step(time.Now())
		if step > u.lastStep {
			u.lastStep = step
			return totp.Code(u.totpSecret, step)
		}
		time.Sleep(time.Second)
	}
}

// stepUp keeps the session's MFA recent (privileged Business actions need
// MFA within five minutes).
func (b *bizEnv) stepUp(u *bizUser) {
	b.t.Helper()
	if u.totpSecret == nil || time.Since(u.mfaAt) < 4*time.Minute {
		return
	}
	if r := b.call(u, "POST", "/auth/mfa/verify", map[string]any{"code": u.code(b.t)}); r.status != 200 {
		b.t.Fatalf("step-up %d %s", r.status, r.body)
	}
	u.mfaAt = time.Now()
}

func (b *bizEnv) login(u *bizUser) {
	b.t.Helper()
	r := b.call(nil, "POST", "/auth/login", map[string]any{"email": u.email, "password": u.password, "organisation_id": u.org})
	if r.status != 200 {
		b.t.Fatalf("login %d %s", r.status, r.body)
	}
	var out struct {
		MFARequired bool   `json:"mfa_required"`
		MFAToken    string `json:"mfa_token"`
		Tokens      *struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	r.data(b.t, &out)
	if out.MFARequired {
		v := b.call(nil, "POST", "/auth/mfa/verify", map[string]any{"mfa_token": out.MFAToken, "code": u.code(b.t)})
		if v.status != 200 {
			b.t.Fatalf("mfa login %d %s", v.status, v.body)
		}
		v.data(b.t, &out)
		u.mfaAt = time.Now()
	}
	if out.Tokens == nil {
		b.t.Fatalf("login without tokens %s", r.body)
	}
	u.access = out.Tokens.AccessToken
}

func (b *bizEnv) secure(u *bizUser) {
	b.t.Helper()
	en := b.call(u, "POST", "/auth/mfa/enrol", map[string]any{"password": u.password})
	if en.status != 201 {
		b.t.Fatalf("enrol %d %s", en.status, en.body)
	}
	var e struct {
		Secret string `json:"secret"`
	}
	en.data(b.t, &e)
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(e.Secret)
	if err != nil {
		b.t.Fatal(err)
	}
	u.totpSecret = secret
	if r := b.call(u, "POST", "/auth/mfa/confirm", map[string]any{"code": u.code(b.t)}); r.status != 200 {
		b.t.Fatalf("confirm %d %s", r.status, r.body)
	}
	u.mfaAt = time.Now()
}

// captured waits for a Notification Service delivery to destination whose
// body matches re, and returns the first submatch.
func (p *crossEnv) captured(destination string, re *regexp.Regexp) string {
	p.t.Helper()
	var out string
	eventually(p.t, 30*time.Second, "notification delivered to "+destination, func() bool {
		f, err := os.Open(p.captureFile)
		if err != nil {
			return false
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) != nil || m["destination"] != destination {
				continue
			}
			if c := re.FindStringSubmatch(sc.Text()); c != nil {
				out = c[1]
			}
		}
		return out != ""
	})
	return out
}

// upload stores a document through the gateway and the Media Service and
// waits until it is scanned and READY.
func (p *crossEnv) upload(token, class, filename, mime string, content []byte, extra map[string]any) ids.ID {
	p.t.Helper()
	sum := sha256.Sum256(content)
	body := map[string]any{"media_class": class, "filename": filename, "mime_type": mime, "size_bytes": len(content), "checksum_sha256": hex.EncodeToString(sum[:])}
	for k, v := range extra {
		body[k] = v
	}
	r := send(p.t, "POST", p.gateway+"/api/v1/media/uploads", token, "", body, ids.New().String())
	if r.status != 201 {
		p.t.Fatalf("upload %d %s", r.status, r.body)
	}
	var up struct {
		Media struct {
			MediaID ids.ID `json:"media_id"`
		} `json:"media"`
		Upload struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
		} `json:"upload"`
	}
	r.data(p.t, &up)
	req, _ := http.NewRequest(up.Upload.Method, up.Upload.URL, bytes.NewReader(content))
	for k, v := range up.Upload.Headers {
		req.Header.Set(k, v)
	}
	req.ContentLength = int64(len(content))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		p.t.Fatalf("signed upload %v %v", err, resp)
	}
	resp.Body.Close()
	if r := send(p.t, "POST", p.gateway+"/api/v1/media/"+up.Media.MediaID.String()+"/complete", token, "", nil, ids.New().String()); r.status != 200 {
		p.t.Fatalf("complete upload %d %s", r.status, r.body)
	}
	eventually(p.t, 30*time.Second, "media READY", func() bool {
		r := p.signedCall(platform.VerificationService, platform.MediaDocumentService, serviceDelegation(), "GET", "/internal/v1/media/"+up.Media.MediaID.String()+"/status", nil, "")
		return r.status == 200 && strings.Contains(string(r.body), `"status":"READY"`)
	})
	return up.Media.MediaID
}

func pdf(label string) []byte {
	return append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte(label+" "), 200)...)
}

// onboardBusiness registers the owner, invites the approver, and completes
// KYB through the real Verification and Media services.
func onboardBusiness(t *testing.T, p *crossEnv) *bizEnv {
	b := &bizEnv{p: p, t: t, policies: map[string]bool{}}
	b.owner = &bizUser{email: "owner-" + ids.New().String()[24:] + "@biz.cross", password: "correct horse battery"}
	r := b.call(nil, "POST", "/auth/register", map[string]any{"email": b.owner.email, "password": b.owner.password, "display_name": "Ada Owner", "organisation_name": "Lekki Suites Ltd"})
	if r.status != 201 {
		t.Fatalf("register %d %s", r.status, r.body)
	}
	var reg struct {
		AccountID      ids.ID `json:"account_id"`
		OrganisationID ids.ID `json:"organisation_id"`
	}
	r.data(t, &reg)
	b.owner.account, b.owner.org = reg.AccountID, reg.OrganisationID
	b.login(b.owner)
	b.secure(b.owner)

	// The approver joins through a Notification-delivered invitation.
	roles := b.must(b.owner, "GET", "/roles", nil, 200)
	var list struct {
		Items []struct {
			RoleID       ids.ID   `json:"role_id"`
			RoleKey      string   `json:"role_key"`
			Capabilities []string `json:"capabilities"`
		} `json:"items"`
	}
	roles.data(t, &list)
	var role ids.ID
	best := 0
	for _, it := range list.Items {
		if len(it.Capabilities) > best {
			role, best = it.RoleID, len(it.Capabilities)
		}
	}
	b.approver = &bizUser{email: "approver-" + ids.New().String()[24:] + "@biz.cross", password: "correct horse battery", org: b.owner.org}
	grants := []map[string]any{}
	for _, c := range []string{"approvals:decide", "rates:approve", "pricing:approve", "publication:approve", "channels:approve", "reservations:approve",
		"maintenance:read", "maintenance:write", "maintenance:approve", "managers:read", "managers:write", "professional:read", "professional:write",
		"projects:read", "projects:write", "disputes:read", "disputes:write", "pms:approve", "folios:approve", "finance:approve", "guest_privacy:write"} {
		grants = append(grants, map[string]any{"capability": c, "scope_kind": "COMPANY", "scope_id": b.owner.org, "inherit": true})
	}
	b.must(b.owner, "POST", "/staff", map[string]any{"email": b.approver.email, "role_id": role, "grants": grants, "reason": "Independent approver for commercial controls"}, 201)
	token := p.captured(b.approver.email, regexp.MustCompile(`(cfbi_[A-Za-z0-9_-]+)`))
	if r := b.call(nil, "POST", "/staff/invitations/accept", map[string]any{"invitation_token": token, "password": b.approver.password, "display_name": "Ben Approver"}); r.status != 200 {
		t.Fatalf("accept invitation %d %s", r.status, r.body)
	}
	b.login(b.approver)
	b.secure(b.approver)

	// KYB decided by the Verification Service.
	b.must(b.owner, "PATCH", "/organisation", map[string]any{"version": 1, "registered_name": "Lekki Suites Limited", "registration_number": "RC1234567", "jurisdiction": "NG"}, 200)
	b.must(b.owner, "POST", "/organisation/verification", map[string]any{}, 202)
	doc := p.upload(b.owner.access, "VERIFICATION_DOCUMENT", "cac-certificate.pdf", "application/pdf", pdf("certificate of incorporation"), map[string]any{"document_type": "CAC_CERTIFICATE"})
	b.must(b.owner, "POST", "/organisation/verification/evidence", map[string]any{"media_id": doc, "evidence_type": "INCORPORATION_CERTIFICATE"}, 201)
	b.must(b.owner, "POST", "/organisation/verification/submit", map[string]any{}, 202)
	eventually(t, 60*time.Second, "KYB VERIFIED and organisation ACTIVE", func() bool {
		b.call(b.owner, "POST", "/organisation/verification/refresh", map[string]any{})
		r := b.call(b.owner, "GET", "/organisation", nil)
		return r.status == 200 && strings.Contains(string(r.body), `"state":"ACTIVE"`)
	})
	return b
}

// approval obtains an independent approval (distinct approver, MFA) for one
// exact operation, creating the governing policy on first use.
func (b *bizEnv) approval(action, capability, channel string, amount *int64, currency string, operation any, scopeKind string, scopeID ids.ID) ids.ID {
	b.t.Helper()
	key := action + ":" + channel
	if !b.policies[key] {
		b.must(b.owner, "POST", "/approval-policies", map[string]any{"action": action, "risk": "HIGH", "scope_kind": "COMPANY", "channel": channel, "levels": []map[string]any{{"capability": capability, "count": 1, "require_mfa": true}}}, 201)
		b.policies[key] = true
	}
	body := map[string]any{"action": action, "scope_kind": scopeKind, "scope_id": scopeID, "channel": channel, "operation": operation, "reason": "Independent commercial evidence review"}
	if amount != nil {
		body["amount_minor"], body["currency"] = *amount, currency
	}
	var created struct {
		Approval struct {
			ID ids.ID `json:"id"`
		} `json:"approval"`
	}
	b.must(b.owner, "POST", "/approvals", body, 201).data(b.t, &created)
	b.must(b.approver, "POST", "/approvals/"+created.Approval.ID.String()+"/decision", map[string]any{"version": 1, "level": 0, "outcome": "APPROVED", "reason": "Independent source and scope checked"}, 200)
	return created.Approval.ID
}

func (b *bizEnv) propertyApproval(action, capability, channel string, amount *int64, currency string, operation any) ids.ID {
	return b.approval(action, capability, channel, amount, currency, operation, "PROPERTY", b.property)
}

type bizNode struct {
	ID          ids.ID `json:"id"`
	Version     int64  `json:"version"`
	TreeVersion int64  `json:"tree_version"`
	State       string `json:"state"`
	Timezone    string `json:"timezone"`
}

func (b *bizEnv) node(id ids.ID) bizNode {
	b.t.Helper()
	collection := "properties"
	if id == b.unit {
		collection = "units"
	}
	return dataOf[bizNode](b.t, b.must(b.owner, "GET", "/"+collection+"/"+id.String(), nil, 200))
}

// supplyBusiness establishes verified operating capacity, a verified and
// active property with physical stock, approved tax, rate and product, and
// an open calendar.
func (b *bizEnv) supplyBusiness() {
	t, p := b.t, b.p
	// Operating capacity decided by the Verification Service.
	capacity := dataOf[struct {
		ID ids.ID `json:"capacity_id"`
	}](t, b.must(b.owner, "POST", "/organisation/capacities", map[string]any{"code": "HOSPITALITY"}, 201))
	licence := p.upload(b.owner.access, "VERIFICATION_DOCUMENT", "hotel-licence.pdf", "application/pdf", pdf("hospitality operating licence"), map[string]any{"document_type": "OPERATING_LICENCE"})
	vc := dataOf[struct {
		ID ids.ID `json:"verification_id"`
	}](t, b.must(b.owner, "POST", "/organisation/capacities/"+capacity.ID.String()+"/verification-case", map[string]any{"media_id": licence, "evidence_type": "OPERATING_LICENCE"}, 202))
	version := int64(1)
	eventually(t, 60*time.Second, "capacity ACTIVE from Verification", func() bool {
		b.stepUp(b.owner)
		if r := b.call(b.owner, "POST", "/organisation/capacities/"+capacity.ID.String()+"/verification", map[string]any{"verification_id": vc.ID, "version": version}); r.status == 200 {
			version++
		}
		r := b.call(b.owner, "GET", "/organisation/capacities", nil)
		return strings.Contains(string(r.body), `"status":"ACTIVE"`)
	})
	// Property and unit; property authority decided by Verification.
	branch := dataOf[struct {
		ID ids.ID `json:"id"`
	}](t, b.must(b.owner, "POST", "/branches", map[string]any{"name": "Lagos branch", "timezone": "Africa/Lagos", "currency": "NGN"}, 201))
	prop := dataOf[bizNode](t, b.must(b.owner, "POST", "/properties", map[string]any{"branch_id": branch.ID, "code": "LEKKI-SUITES", "name": "Lekki Suites", "capacity_code": "HOSPITALITY", "attributes": map[string]any{"classification": "HOTEL"}}, 201))
	unit := dataOf[bizNode](t, b.must(b.owner, "POST", "/units", map[string]any{"parent_id": prop.ID, "code": "SUITE-101", "name": "Sea-view suite", "attributes": map[string]any{"classification": "APARTMENT", "capacity": 2}}, 201))
	b.property, b.unit = prop.ID, unit.ID
	deed := p.upload(b.owner.access, "VERIFICATION_DOCUMENT", "deed.pdf", "application/pdf", pdf("certificate of occupancy"), map[string]any{"document_type": "TITLE_DEED"})
	pv := dataOf[struct {
		CaseID ids.ID `json:"case_id"`
	}](t, b.must(b.owner, "POST", "/inventory-nodes/"+prop.ID.String()+"/verification", map[string]any{"version": prop.Version, "create_case": true, "evidence_media_id": deed, "evidence_type": "TITLE_DEED"}, 200))
	_ = pv
	eventually(t, 60*time.Second, "property authority VERIFIED", func() bool {
		r := b.call(b.owner, "GET", "/inventory-nodes/"+prop.ID.String()+"/verification", nil)
		var v struct {
			CaseID ids.ID `json:"case_id"`
			Status string `json:"status"`
		}
		_ = json.Unmarshal(dataRaw(r), &v)
		if v.Status == "VERIFIED" {
			return true
		}
		if !v.CaseID.IsZero() {
			b.stepUp(b.owner)
			b.call(b.owner, "POST", "/inventory-nodes/"+prop.ID.String()+"/verification", map[string]any{"version": b.node(prop.ID).Version, "verification_id": v.CaseID})
		}
		return false
	})
	for _, id := range []ids.ID{prop.ID, unit.ID} {
		n := b.node(id)
		b.must(b.owner, "POST", "/inventory-nodes/"+id.String()+"/lifecycle", map[string]any{"version": n.Version, "tree_version": n.TreeVersion, "state": "ACTIVE", "reason": "Activate verified physical inventory"}, 200)
	}
	prop = b.node(prop.ID)
	b.propertyVersion = prop.Version
	// Physical stock, fiscal policy, rate plan, product, calendar.
	pool := dataOf[struct {
		ID      ids.ID `json:"id"`
		Version int64  `json:"version"`
	}](t, b.must(b.owner, "POST", "/inventory-pools", map[string]any{"property_id": prop.ID, "node_id": unit.ID, "name": "Suite physical stock"}, 201))
	pool = dataOf[struct {
		ID      ids.ID `json:"id"`
		Version int64  `json:"version"`
	}](t, b.must(b.owner, "PATCH", "/inventory-pools/"+pool.ID.String(), map[string]any{"version": pool.Version, "state": "ACTIVE", "reason": "Canonical physical stock verified"}, 200))
	b.pool, b.poolVersion = pool.ID, pool.Version
	fiscal := map[string]any{"property_id": prop.ID, "currency": "NGN", "definition": map[string]any{"display_mode": "INCLUSIVE", "fiscal_reference": "e2e-explicit-zero-tax", "policy_version": 1, "taxes": []any{}, "fees": []any{}},
		"source_reference": "e2e fiscal evidence", "effective_at": time.Now().UTC().Add(-time.Minute), "reason": "Approved explicit zero tax"}
	fiscal["approval_id"] = b.propertyApproval("COMMERCIAL_FISCAL_POLICY_CHANGE", "pricing:approve", "", nil, "", map[string]any{"property_id": prop.ID, "command": withoutApproval(fiscal)})
	tax := dataOf[struct {
		ID ids.ID `json:"id"`
	}](t, b.must(b.owner, "POST", "/tax-profiles", fiscal, 201))
	loc, _ := time.LoadLocation(prop.Timezone)
	arrival := time.Now().In(loc) // arrival today: the front desk checks the guest in during flow 3
	b.dateFrom, b.dateTo = arrival.Format(time.DateOnly), arrival.AddDate(0, 0, 2).Format(time.DateOnly)
	definition := map[string]any{"currency": "NGN", "amount_minor": 2500000, "included_adults": 2, "min_guests": 1, "max_guests": 2, "min_nights": 1, "max_nights": 30, "cancellation_reference": "FLEXIBLE-24H:v1",
		"prepayment_reference": "PAYMENT-SERVICE-REFERENCE:v1", "policy_code": "E2E-BASE", "policy_version": 1, "floor_minor": 100000, "ceiling_minor": 5000000, "max_change_bps": 1000, "tax_profile_id": tax.ID}
	var rate struct {
		Plan struct {
			ID      ids.ID `json:"id"`
			Version int64  `json:"version"`
		} `json:"rate_plan"`
	}
	b.must(b.owner, "POST", "/rate-plans", map[string]any{"property_id": prop.ID, "name": "Flexible stay", "kind": "REFUNDABLE", "date_from": b.dateFrom, "date_to": arrival.AddDate(0, 0, 30).Format(time.DateOnly), "definition": definition, "reason": "Canonical approved pricing policy"}, 201).data(t, &rate)
	ceiling := int64(5000000)
	activate := map[string]any{"version": rate.Plan.Version, "state": "ACTIVE", "reason": "Independent rate activation"}
	activate["approval_id"] = b.propertyApproval("COMMERCIAL_RATE_ACTIVATION", "rates:approve", "", &ceiling, "NGN", map[string]any{"rate_plan_id": rate.Plan.ID, "command": withoutApproval(activate)})
	b.must(b.owner, "PATCH", "/rate-plans/"+rate.Plan.ID.String(), activate, 200)
	b.rate = rate.Plan.ID
	product := dataOf[struct {
		ID      ids.ID `json:"id"`
		Version int64  `json:"version"`
	}](t, b.must(b.owner, "POST", "/sellable-products", map[string]any{"property_id": prop.ID, "node_id": unit.ID, "pool_id": pool.ID, "rate_plan_id": rate.Plan.ID, "name": "Sea-view suite nightly stay", "min_guests": 1, "max_guests": 2, "policy_code": "ACCOMMODATION", "policy_version": 1, "channels": []string{}}, 201))
	pa := map[string]any{"version": product.Version, "state": "ACTIVE", "reason": "Activate the approved stock product"}
	pa["approval_id"] = b.propertyApproval("COMMERCIAL_PRODUCT_CHANGE", "pricing:approve", "", nil, "", map[string]any{"product_id": product.ID, "command": withoutApproval(pa)})
	product = dataOf[struct {
		ID      ids.ID `json:"id"`
		Version int64  `json:"version"`
	}](t, b.must(b.owner, "PATCH", "/sellable-products/"+product.ID.String(), pa, 200))
	b.product, b.productVersion = product.ID, product.Version
	versions := map[string]int64{}
	for d := arrival; d.Before(arrival.AddDate(0, 0, 2)); d = d.AddDate(0, 0, 1) {
		versions[d.Format(time.DateOnly)] = 0
	}
	b.must(b.owner, "PUT", "/inventory", map[string]any{"pool_id": pool.ID, "date_from": b.dateFrom, "date_to": b.dateTo, "capacity": 1, "expected_versions": versions, "reason": "Open existing physical stock"}, 200)
}

// withoutApproval copies a command without its approval_id (the exact
// operation an approval is bound to).
func withoutApproval(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if k != "approval_id" {
			out[k] = v
		}
	}
	return out
}

func dataRaw(r response) []byte {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(r.body, &env)
	return env.Data
}

type opsRecord struct {
	ID      ids.ID `json:"id"`
	Version int64  `json:"version"`
	State   string `json:"state"`
}

func (b *bizEnv) opsCreate(u *bizUser, path string, body map[string]any) opsRecord {
	b.t.Helper()
	if body["reason"] == nil {
		body["reason"] = "Governed operational record"
	}
	return dataOf[opsRecord](b.t, b.must(u, "POST", path, body, 201))
}

// opsMove applies one governed transition at the record's current version.
func (b *bizEnv) opsMove(u *bizUser, path string, rec opsRecord, cmd map[string]any) opsRecord {
	b.t.Helper()
	cmd["version"] = rec.Version
	if cmd["reason"] == nil {
		cmd["reason"] = "Governed operational transition"
	}
	return dataOf[opsRecord](b.t, b.must(u, "POST", path+"/"+rec.ID.String()+"/transitions", cmd, 200))
}

func (b *bizEnv) opsGet(path string) opsRecord {
	b.t.Helper()
	return dataOf[struct {
		Record opsRecord `json:"record"`
	}](b.t, b.must(b.owner, "GET", path, nil, 200)).Record
}
