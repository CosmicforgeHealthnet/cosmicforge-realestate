package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/integrations/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/migrations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestMigrationsFromEmptyDatabase(t *testing.T) {
	testkit.MigrationRoundTrip(t, migrations.FS)
}

func TestHappyPathSynchronousProvider(t *testing.T) {
	h := newHarness(t)
	subject := ids.New()
	id := h.createReady(subject, "", nil)
	if h.status(id) != "DRAFT" {
		t.Fatal("new case must be DRAFT")
	}
	h.submitAndProcess(id)
	if h.status(id) != "PROCESSING" {
		t.Fatalf("status %s", h.status(id))
	}
	h.dispatch()
	if h.status(id) != "VERIFIED" {
		t.Fatalf("status after dispatch %s", h.status(id))
	}
	got := h.eventTypes(id)
	want := []string{"verification.submitted.v1", "verification.processing.v1", "verification.verified.v1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events %v", got)
	}
	res := h.do(call{method: "GET", path: "/internal/v1/verifications/" + id.String() + "/result", as: platform.IndividualOwnerService})
	var r struct {
		Status   string `json:"status"`
		Decision struct {
			Decision       string     `json:"decision"`
			DecisionSource string     `json:"decision_source"`
			ExpiresAt      *time.Time `json:"expires_at"`
		} `json:"decision"`
		Evidence []struct {
			MediaID ids.ID `json:"media_id"`
		} `json:"evidence"`
	}
	res.data(t, &r)
	if r.Status != "VERIFIED" || r.Decision.Decision != "VERIFIED" || r.Decision.DecisionSource != "PROVIDER" || r.Decision.ExpiresAt == nil || len(r.Evidence) != 1 {
		t.Fatalf("result %+v", r)
	}
}

func TestInvalidTransitions(t *testing.T) {
	h := newHarness(t)
	id := h.createReady(ids.New(), "", nil)
	// process before submit: DRAFT -> PROCESSING is not allowed
	res := h.do(call{method: "POST", path: "/internal/v1/verifications/" + id.String() + "/process", as: platform.IndividualOwnerService, key: ids.New().String()})
	if res.status != 409 || res.code() != apperrors.CodeInvalidStateTransition {
		t.Fatalf("process on DRAFT: %d %s", res.status, res.body)
	}
	h.submitAndProcess(id)
	// submit again with a new key: SUBMITTED/PROCESSING -> SUBMITTED invalid
	res = h.do(call{method: "POST", path: "/internal/v1/verifications/" + id.String() + "/submit", as: platform.IndividualOwnerService, key: ids.New().String()})
	if res.status != 409 || res.code() != apperrors.CodeInvalidStateTransition {
		t.Fatalf("resubmit: %d %s", res.status, res.body)
	}
	// manual decision on a PROCESSING case
	res = h.do(call{method: "POST", path: "/internal/v1/verifications/" + id.String() + "/manual-review/decision", as: platform.AdminService,
		actor: adminActor(), key: ids.New().String(), body: map[string]any{"decision": "VERIFIED"}})
	if res.status != 409 {
		t.Fatalf("decision on PROCESSING: %d %s", res.status, res.body)
	}
	// expire a non-VERIFIED case
	res = h.do(call{method: "POST", path: "/internal/v1/verifications/" + id.String() + "/expire", as: platform.AdminService,
		actor: adminActor(), key: ids.New().String(), body: map[string]any{"reason_code": "DOCUMENT_REVOKED"}})
	if res.status != 409 || res.code() != apperrors.CodeInvalidStateTransition {
		t.Fatalf("expire PROCESSING: %d %s", res.status, res.body)
	}
	// evidence after DRAFT is locked
	res = h.do(call{method: "POST", path: "/internal/v1/verifications/" + id.String() + "/evidence", as: platform.IndividualOwnerService,
		key: ids.New().String(), body: map[string]any{"media_id": h.newMedia(platform.DomainIndividualOwner, "READY"), "evidence_type": "SELFIE"}})
	if res.status != 409 || res.code() != "EVIDENCE_LOCKED" {
		t.Fatalf("evidence after submit: %d %s", res.status, res.body)
	}
}

func TestServiceAuthentication(t *testing.T) {
	h := newHarness(t)
	path := "/internal/v1/verifications?subject_type=USER&subject_domain=individual-owner&subject_id=" + ids.New().String()
	if r := h.do(call{method: "GET", path: path}); r.status != 401 || r.code() != apperrors.CodeAuthenticationRequired {
		t.Fatalf("no token: %d", r.status)
	}
	// Token for another audience.
	tok, _ := h.signers[platform.IndividualOwnerService].TokenFor(string(platform.MediaDocumentService), serviceauth.Delegation{})
	if r := h.do(call{method: "GET", path: path, rawToken: tok}); r.status != 401 || r.code() != apperrors.CodeInvalidToken {
		t.Fatalf("wrong audience: %d", r.status)
	}
	// Unknown service (payment-service is not in the trusted registry of this test).
	ps := testkit.SigningKey(t, "ps-1")
	psSigner, _ := serviceauth.NewSigner(serviceauth.SignerConfig{Service: platform.PaymentService, Key: ps})
	tok, _ = psSigner.TokenFor(string(platform.VerificationService), serviceauth.Delegation{})
	if r := h.do(call{method: "GET", path: path, rawToken: tok}); r.status != 401 {
		t.Fatalf("untrusted service: %d", r.status)
	}
	// Valid token from a service that holds no capability.
	if r := h.do(call{method: "GET", path: path, as: platform.NotificationService}); r.status != 403 || r.code() != apperrors.CodePermissionDenied {
		t.Fatalf("valid-but-unauthorized: %d %s", r.status, r.body)
	}
	// Gateway token on an internal route is not authorized either.
	if r := h.do(call{method: "GET", path: path, as: platform.APIGateway}); r.status != 403 {
		t.Fatalf("gateway on internal: %d", r.status)
	}
	if r := h.do(call{method: "GET", path: path, as: platform.IndividualOwnerService}); r.status != 200 {
		t.Fatalf("authorized: %d %s", r.status, r.body)
	}
}

func TestCallerAllowList(t *testing.T) {
	h := newHarness(t)
	create := func(as platform.Service, typ, subjectType, subjectDomain string) result {
		return h.do(call{method: "POST", path: "/internal/v1/verifications", as: as, key: ids.New().String(),
			body: map[string]any{"verification_type": typ, "subject": map[string]any{"subject_type": subjectType, "subject_domain": subjectDomain, "subject_id": ids.New()}}})
	}
	cases := []struct {
		as          platform.Service
		typ, st, sd string
		status      int
	}{
		{platform.IndividualOwnerService, "IDENTITY", "USER", "individual-owner", 201},
		{platform.IndividualOwnerService, "IDENTITY", "USER", "artisan", 403},               // another domain's subject
		{platform.MarketplaceService, "PROPERTY_AUTHORITY", "PROPERTY", "marketplace", 403}, // type not approved for domain
		{platform.PropertyManagerService, "PROPERTY_AUTHORITY", "PROPERTY", "property-manager", 201},
		{platform.ArtisanService, "PROFESSIONAL_CREDENTIAL", "PROFESSIONAL", "artisan", 201},
		{platform.NotificationService, "IDENTITY", "USER", "individual-owner", 403}, // no capability
		{platform.AdminService, "BUSINESS", "BUSINESS", "admin", 403},
		{platform.IndividualOwnerService, "IDENTITY", "PROPERTY", "individual-owner", 422}, // subject type mismatch
		{platform.IndividualOwnerService, "SOMETHING", "USER", "individual-owner", 400},
	}
	for _, c := range cases {
		if r := create(c.as, c.typ, c.st, c.sd); r.status != c.status {
			t.Errorf("%s %s %s/%s: %d %s", c.as, c.typ, c.st, c.sd, r.status, r.body)
		}
	}
	// Read isolation: marketplace cannot see an individual-owner case.
	id := h.createReady(ids.New(), "", nil)
	if r := h.do(call{method: "GET", path: "/internal/v1/verifications/" + id.String(), as: platform.MarketplaceService}); r.status != 404 {
		t.Fatalf("cross-domain read: %d", r.status)
	}
	if r := h.do(call{method: "GET", path: "/internal/v1/verifications/" + id.String(), as: platform.AdminService}); r.status != 200 {
		t.Fatalf("admin read: %d", r.status)
	}
	// Another domain cannot submit/process somebody else's case.
	if r := h.do(call{method: "POST", path: "/internal/v1/verifications/" + id.String() + "/submit", as: platform.ArtisanService, key: "k"}); r.status != 404 {
		t.Fatalf("foreign submit: %d %s", r.status, r.body)
	}
}

func TestIdempotentCreateAndSubmit(t *testing.T) {
	h := newHarness(t)
	subject := ids.New()
	body := map[string]any{"verification_type": "IDENTITY", "subject": map[string]any{"subject_type": "USER", "subject_domain": "individual-owner", "subject_id": subject}}
	// Concurrent identical requests with the same key: one execution.
	var wg sync.WaitGroup
	results := make([]result, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = h.do(call{method: "POST", path: "/internal/v1/verifications", as: platform.IndividualOwnerService, key: "create-1", body: body})
		}()
	}
	wg.Wait()
	var first ids.ID
	for _, r := range results {
		switch r.status {
		case 201:
			var v caseView
			r.data(t, &v)
			if first.IsZero() {
				first = v.VerificationID
			} else if v.VerificationID != first {
				t.Fatal("replay returned a different case")
			}
		case 409:
			if r.code() != apperrors.CodeIdempotencyInProgress {
				t.Fatalf("unexpected conflict %s", r.body)
			}
		default:
			t.Fatalf("unexpected %d %s", r.status, r.body)
		}
	}
	var n int
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM verification_cases WHERE subject_id=$1`, subject).Scan(&n)
	if n != 1 {
		t.Fatalf("%d cases created", n)
	}
	// Replay after completion.
	r := h.do(call{method: "POST", path: "/internal/v1/verifications", as: platform.IndividualOwnerService, key: "create-1", body: body})
	if r.status != 201 || r.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %v", r.status, r.header)
	}
	// Same key, different payload.
	other := map[string]any{"verification_type": "IDENTITY", "subject": map[string]any{"subject_type": "USER", "subject_domain": "individual-owner", "subject_id": ids.New()}}
	if r := h.do(call{method: "POST", path: "/internal/v1/verifications", as: platform.IndividualOwnerService, key: "create-1", body: other}); r.status != 409 || r.code() != apperrors.CodeIdempotencyConflict {
		t.Fatalf("conflict: %d %s", r.status, r.body)
	}
	// New key, same subject: database uniqueness rejects a second open case.
	if r := h.do(call{method: "POST", path: "/internal/v1/verifications", as: platform.IndividualOwnerService, key: "create-2", body: body}); r.status != 409 || r.code() != "VERIFICATION_ALREADY_OPEN" {
		t.Fatalf("duplicate open: %d %s", r.status, r.body)
	}
	// Missing key.
	if r := h.do(call{method: "POST", path: "/internal/v1/verifications", as: platform.IndividualOwnerService, body: other}); r.status != 400 || r.code() != apperrors.CodeIdempotencyKeyRequired {
		t.Fatalf("missing key: %d", r.status)
	}

	// Idempotent submission.
	id := h.createReady(ids.New(), "", nil)
	sub := func() result {
		return h.do(call{method: "POST", path: "/internal/v1/verifications/" + id.String() + "/submit", as: platform.IndividualOwnerService, key: "submit-1"})
	}
	a, b := sub(), sub()
	if a.status != 200 || b.status != 200 || string(a.body) != string(b.body) || b.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("submit replay %d %d", a.status, b.status)
	}
	if got := h.eventTypes(id); len(got) != 1 {
		t.Fatalf("submitted event emitted %d times", len(got))
	}
}

func TestEvidenceValidationCrossServiceCall(t *testing.T) {
	h := newHarness(t)
	res := h.do(call{method: "POST", path: "/internal/v1/verifications", as: platform.IndividualOwnerService, key: ids.New().String(),
		body: map[string]any{"verification_type": "IDENTITY", "subject": map[string]any{"subject_type": "USER", "subject_domain": "individual-owner", "subject_id": ids.New()}}})
	var v caseView
	res.data(t, &v)
	ev := func(mid ids.ID) result {
		return h.do(call{method: "POST", path: "/internal/v1/verifications/" + v.VerificationID.String() + "/evidence", as: platform.IndividualOwnerService,
			key: ids.New().String(), body: map[string]any{"media_id": mid, "evidence_type": "ID_DOCUMENT_FRONT"}})
	}
	if r := ev(ids.New()); r.status != 422 || r.code() != "EVIDENCE_MEDIA_NOT_FOUND" {
		t.Fatalf("missing media: %d %s", r.status, r.body)
	}
	if r := ev(h.newMedia(platform.DomainArtisan, "READY")); r.status != 422 || r.code() != "EVIDENCE_OWNER_MISMATCH" {
		t.Fatalf("foreign media: %d %s", r.status, r.body)
	}
	// Submission without evidence is refused.
	if r := h.do(call{method: "POST", path: "/internal/v1/verifications/" + v.VerificationID.String() + "/submit", as: platform.IndividualOwnerService, key: ids.New().String()}); r.code() != "EVIDENCE_REQUIRED" {
		t.Fatalf("no evidence: %s", r.body)
	}
	scanning := h.newMedia(platform.DomainIndividualOwner, "SCANNING")
	if r := ev(scanning); r.status != 201 {
		t.Fatalf("attach scanning media: %d %s", r.status, r.body)
	}
	if r := h.do(call{method: "POST", path: "/internal/v1/verifications/" + v.VerificationID.String() + "/submit", as: platform.IndividualOwnerService, key: ids.New().String()}); r.status != 422 || r.code() != "EVIDENCE_NOT_READY" {
		t.Fatalf("not ready: %d %s", r.status, r.body)
	}
	// Media service down: fail safely with 503, no state change.
	h.media.mu.Lock()
	h.media.down = true
	h.media.mu.Unlock()
	if r := h.do(call{method: "POST", path: "/internal/v1/verifications/" + v.VerificationID.String() + "/submit", as: platform.IndividualOwnerService, key: ids.New().String()}); r.status != 503 {
		t.Fatalf("media down: %d %s", r.status, r.body)
	}
	if h.status(v.VerificationID) != "DRAFT" {
		t.Fatal("state changed despite dependency failure")
	}
}

func TestDuplicateCallbackAndAsyncResult(t *testing.T) {
	h := newHarness(t)
	id := h.createReady(ids.New(), "", map[string]string{"sandbox_outcome": "PENDING"})
	h.submitAndProcess(id)
	h.dispatch()
	if h.status(id) != "PROCESSING" {
		t.Fatalf("pending provider must keep PROCESSING: %s", h.status(id))
	}
	var clientRef, sub string
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT client_reference FROM verification_attempts WHERE verification_id=$1`, id).Scan(&clientRef)
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT processing_substatus FROM verification_cases WHERE verification_id=$1`, id).Scan(&sub)
	if sub != "PROVIDER_SUBMITTED" {
		t.Fatalf("substatus %s", sub)
	}
	body, _ := json.Marshal(map[string]string{"event_id": "evt-123", "client_reference": clientRef, "outcome": "REJECTED", "reason_code": "DOCUMENT_EXPIRED"})
	cb := func(sig string) result {
		return h.do(call{method: "POST", path: "/webhooks/v1/sandbox-kyc/callbacks", as: platform.APIGateway, header: serviceauth.HeaderServiceAuthorization,
			rawBody: body, extra: map[string]string{providers.SandboxSignatureHeader: sig, "Content-Type": "application/json"}})
	}
	// Forged signature.
	if r := cb("deadbeef"); r.status != 401 || r.code() != "INVALID_PROVIDER_CALLBACK" {
		t.Fatalf("forged callback: %d %s", r.status, r.body)
	}
	sig := h.sandbox.SignCallback(body)
	var first, second struct {
		Status string `json:"status"`
	}
	cb(sig).data(t, &first)
	cb(sig).data(t, &second)
	if first.Status != "APPLIED" || second.Status != "DUPLICATE" {
		t.Fatalf("callback statuses %s %s", first.Status, second.Status)
	}
	if h.status(id) != "REJECTED" {
		t.Fatalf("status %s", h.status(id))
	}
	if got := h.eventTypes(id); got[len(got)-1] != "verification.rejected.v1" || len(got) != 3 {
		t.Fatalf("events %v", got)
	}
	// Callback not via the gateway is refused.
	if r := h.do(call{method: "POST", path: "/webhooks/v1/sandbox-kyc/callbacks", as: platform.IndividualOwnerService, header: serviceauth.HeaderServiceAuthorization, rawBody: body}); r.status != 403 {
		t.Fatalf("non-gateway callback: %d", r.status)
	}
}

func TestProviderTimeoutUnknownThenReadBack(t *testing.T) {
	h := newHarness(t)
	id := h.createReady(ids.New(), "", map[string]string{"sandbox_outcome": "TIMEOUT"})
	h.submitAndProcess(id)
	h.dispatch() // Submit "times out" after the provider processed it.
	var sub, state string
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT processing_substatus FROM verification_cases WHERE verification_id=$1`, id).Scan(&sub)
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT state FROM verification_attempts WHERE verification_id=$1`, id).Scan(&state)
	if h.status(id) != "PROCESSING" || sub != "UNKNOWN_PROVIDER_RESULT" || state != "UNKNOWN" {
		t.Fatalf("ambiguous outcome must stay PROCESSING/UNKNOWN: %s %s %s", h.status(id), sub, state)
	}
	var ref string
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT client_reference FROM verification_attempts WHERE verification_id=$1`, id).Scan(&ref)
	h.dispatchUntil(func() bool { return h.status(id) == "VERIFIED" })
	if h.sandbox.Calls(ref) != 1 {
		t.Fatalf("read-back must resolve without resubmitting; submit calls=%d", h.sandbox.Calls(ref))
	}
	var ops string
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT string_agg(operation||':'||outcome, ',' ORDER BY created_at) FROM provider_requests WHERE verification_id=$1`, id).Scan(&ops)
	if ops != "SUBMIT:TIMEOUT,READ_STATUS:RESULT" {
		t.Fatalf("provider call log %s", ops)
	}
}

func TestReadBackNotFoundResubmitsSafely(t *testing.T) {
	h := newHarness(t)
	calls := 0
	h.fake.submitFn = func(req providers.SubmitRequest, n int) (providers.SubmitResult, error) {
		calls++
		if n == 1 {
			return providers.SubmitResult{}, &providers.Error{Kind: providers.KindUnknown, Code: "HTTP_502"}
		}
		return providers.SubmitResult{ProviderReference: "fk", Outcome: providers.OutcomeVerified, EventID: "fk-final"}, nil
	}
	h.fake.statusFn = func(string) (providers.StatusResult, error) { return providers.StatusResult{Found: false}, nil }
	id := h.createReady(ids.New(), "fake-kyc", nil)
	h.submitAndProcess(id)
	h.dispatchUntil(func() bool { return h.status(id) == "VERIFIED" })
	if h.fake.reads == 0 || calls != 2 {
		t.Fatalf("expected read-back before resubmission (reads=%d submits=%d)", h.fake.reads, calls)
	}
}

func TestRetryableAndPermanentProviderErrors(t *testing.T) {
	h := newHarness(t)
	retry := h.createReady(ids.New(), "", map[string]string{"sandbox_outcome": "RETRYABLE"})
	h.submitAndProcess(retry)
	h.dispatchUntil(func() bool { return h.status(retry) == "VERIFIED" })

	perm := h.createReady(ids.New(), "", map[string]string{"sandbox_outcome": "PERMANENT"})
	h.submitAndProcess(perm)
	h.dispatch()
	if h.status(perm) != "MANUAL_REVIEW" {
		t.Fatalf("permanent provider failure must go to manual review, not REJECTED: %s", h.status(perm))
	}
	var reason string
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT reason FROM manual_reviews WHERE verification_id=$1`, perm).Scan(&reason)
	if reason != "PROVIDER_FAILURE" {
		t.Fatalf("review reason %s", reason)
	}

	// Persistent ambiguity escalates after bounded tries instead of looping forever.
	h.fake.submitFn = func(providers.SubmitRequest, int) (providers.SubmitResult, error) {
		return providers.SubmitResult{}, &providers.Error{Kind: providers.KindUnknown, Code: "TIMEOUT"}
	}
	h.fake.statusFn = func(string) (providers.StatusResult, error) {
		return providers.StatusResult{}, &providers.Error{Kind: providers.KindUnknown, Code: "TIMEOUT"}
	}
	stuck := h.createReady(ids.New(), "fake-kyc", nil)
	h.submitAndProcess(stuck)
	h.dispatchUntil(func() bool { return h.status(stuck) == "MANUAL_REVIEW" })
}

func TestManualReviewAuthorizationAndDecision(t *testing.T) {
	h := newHarness(t)
	id := h.createReady(ids.New(), "", map[string]string{"sandbox_outcome": "INCONCLUSIVE"})
	h.submitAndProcess(id)
	h.dispatch()
	if h.status(id) != "MANUAL_REVIEW" {
		t.Fatalf("status %s", h.status(id))
	}
	if got := h.eventTypes(id); got[len(got)-1] != "verification.manual_review_required.v1" {
		t.Fatalf("events %v", got)
	}
	decide := func(as platform.Service, actor *serviceauth.Delegation, decision string) result {
		return h.do(call{method: "POST", path: "/internal/v1/verifications/" + id.String() + "/manual-review/decision", as: as, actor: actor,
			key: ids.New().String(), body: map[string]any{"decision": decision, "reason_code": "DOCS_CONFIRMED", "notes": "checked by phone"}})
	}
	// Domain service cannot review.
	if r := decide(platform.IndividualOwnerService, nil, "VERIFIED"); r.status != 403 {
		t.Fatalf("domain service review: %d", r.status)
	}
	// admin-service without a delegated admin actor.
	if r := decide(platform.AdminService, nil, "VERIFIED"); r.status != 403 {
		t.Fatalf("admin without actor: %d %s", r.status, r.body)
	}
	// admin-service delegating a non-admin actor.
	if r := decide(platform.AdminService, &serviceauth.Delegation{ActorDomain: platform.DomainArtisan, ActorID: ids.New()}, "VERIFIED"); r.status != 403 {
		t.Fatalf("non-admin actor: %d", r.status)
	}
	// Queue listing.
	q := h.do(call{method: "GET", path: "/internal/v1/manual-reviews", as: platform.AdminService, actor: adminActor()})
	var queue struct {
		Data []struct {
			VerificationID ids.ID `json:"verification_id"`
			Reason         string `json:"reason"`
		} `json:"data"`
	}
	_ = json.Unmarshal(q.body, &queue)
	if q.status != 200 || len(queue.Data) != 1 || queue.Data[0].VerificationID != id || queue.Data[0].Reason != "PROVIDER_INCONCLUSIVE" {
		t.Fatalf("queue %d %s", q.status, q.body)
	}
	// Admin A assigns; admin B cannot decide.
	a, b := adminActor(), adminActor()
	if r := h.do(call{method: "POST", path: "/internal/v1/verifications/" + id.String() + "/manual-review/assign", as: platform.AdminService, actor: a, key: ids.New().String()}); r.status != 200 {
		t.Fatalf("assign: %d %s", r.status, r.body)
	}
	if r := decide(platform.AdminService, b, "VERIFIED"); r.status != 409 || r.code() != "MANUAL_REVIEW_ASSIGNED_TO_OTHER" {
		t.Fatalf("other admin: %d %s", r.status, r.body)
	}
	if r := decide(platform.AdminService, a, "MAYBE"); r.status != 400 {
		t.Fatalf("invalid decision: %d", r.status)
	}
	if r := decide(platform.AdminService, a, "VERIFIED"); r.status != 200 {
		t.Fatalf("decision: %d %s", r.status, r.body)
	}
	if h.status(id) != "VERIFIED" {
		t.Fatal("not verified")
	}
	var src, actorID string
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT decision_source, decided_by_actor_id::text FROM verification_decisions WHERE verification_id=$1`, id).Scan(&src, &actorID)
	if src != "MANUAL_REVIEW" || actorID != a.ActorID.String() {
		t.Fatalf("decision attribution %s %s", src, actorID)
	}
}

func TestExpiryAndReverify(t *testing.T) {
	h := newHarness(t)
	id := h.createReady(ids.New(), "", nil)
	h.submitAndProcess(id)
	h.dispatch()
	if n, _ := h.app.Service.ExpireDue(context.Background(), 10); n != 0 {
		t.Fatal("nothing should expire yet")
	}
	h.clock.advance(366 * 24 * time.Hour)
	if n, err := h.app.Service.ExpireDue(context.Background(), 10); err != nil || n != 1 {
		t.Fatalf("expire due: %d %v", n, err)
	}
	if h.status(id) != "EXPIRED" {
		t.Fatalf("status %s", h.status(id))
	}
	if got := h.eventTypes(id); got[len(got)-1] != "verification.expired.v1" {
		t.Fatalf("events %v", got)
	}
	// Re-verification creates a new linked DRAFT; only the subject's domain may do it.
	if r := h.do(call{method: "POST", path: "/internal/v1/verifications/" + id.String() + "/reverify", as: platform.ArtisanService, key: ids.New().String()}); r.status != 404 {
		t.Fatalf("foreign reverify: %d", r.status)
	}
	r := h.do(call{method: "POST", path: "/internal/v1/verifications/" + id.String() + "/reverify", as: platform.IndividualOwnerService, key: ids.New().String()})
	if r.status != 201 {
		t.Fatalf("reverify: %d %s", r.status, r.body)
	}
	var nv caseView
	r.data(t, &nv)
	if nv.Status != "DRAFT" || nv.SupersedesVerificationID == nil || *nv.SupersedesVerificationID != id {
		t.Fatalf("new case %+v", nv)
	}
	// Admin force-expiry of a VERIFIED case; domain services may not.
	id2 := h.createReady(ids.New(), "", nil)
	h.submitAndProcess(id2)
	h.dispatch()
	if r := h.do(call{method: "POST", path: "/internal/v1/verifications/" + id2.String() + "/expire", as: platform.IndividualOwnerService, key: "x", body: map[string]any{"reason_code": "X"}}); r.status != 403 {
		t.Fatalf("domain expire: %d", r.status)
	}
	if r := h.do(call{method: "POST", path: "/internal/v1/verifications/" + id2.String() + "/expire", as: platform.AdminService, actor: adminActor(), key: "y", body: map[string]any{"reason_code": "DOCUMENT_REVOKED"}}); r.status != 200 {
		t.Fatalf("admin expire: %d %s", r.status, r.body)
	}
	if h.status(id2) != "EXPIRED" {
		t.Fatal("not expired")
	}
}

func TestAuditTrail(t *testing.T) {
	h := newHarness(t)
	id := h.createReady(ids.New(), "", map[string]string{"sandbox_outcome": "INCONCLUSIVE"})
	h.submitAndProcess(id)
	h.dispatch()
	actor := adminActor()
	h.do(call{method: "POST", path: "/internal/v1/verifications/" + id.String() + "/manual-review/decision", as: platform.AdminService, actor: actor,
		key: ids.New().String(), body: map[string]any{"decision": "REJECTED", "reason_code": "FORGED_DOCUMENT"}})
	rows, err := h.db.Pool.Query(context.Background(), `SELECT action, actor_type, coalesce(calling_service,''), coalesce(request_id,''), coalesce(trace_id,''),
		changes->>'from', changes->>'to', metadata->>'decision_source', metadata->'subject'->>'subject_id', coalesce(actor_id::text,'')
		FROM audit_events WHERE target_id=$1 AND action='verification.transition' ORDER BY occurred_at`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct{ action, actorType, svc, req, trace, from, to, src, subject, actorID string }
	var got []row
	for rows.Next() {
		var r row
		var src, from *string
		if err := rows.Scan(&r.action, &r.actorType, &r.svc, &r.req, &r.trace, &from, &r.to, &src, &r.subject, &r.actorID); err != nil {
			t.Fatal(err)
		}
		if src != nil {
			r.src = *src
		}
		if from != nil {
			r.from = *from
		}
		got = append(got, r)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 transitions audited, got %d", len(got))
	}
	want := [][2]string{{"DRAFT", "SUBMITTED"}, {"SUBMITTED", "PROCESSING"}, {"PROCESSING", "MANUAL_REVIEW"}, {"MANUAL_REVIEW", "REJECTED"}}
	for i, w := range want {
		if got[i].from != w[0] || got[i].to != w[1] || got[i].trace == "" || got[i].req == "" || got[i].subject == "" {
			t.Fatalf("audit %d: %+v", i, got[i])
		}
	}
	if got[0].svc != "individual-owner-service" || got[2].actorType != "SYSTEM" || got[2].src != "PROVIDER" {
		t.Fatalf("attribution: %+v %+v", got[0], got[2])
	}
	if got[3].svc != "admin-service" || got[3].actorType != "USER" || got[3].src != "MANUAL_REVIEW" || got[3].actorID != actor.ActorID.String() {
		t.Fatalf("manual decision attribution: %+v", got[3])
	}
	var hist int
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM verification_history WHERE verification_id=$1`, id).Scan(&hist)
	if hist != 5 {
		t.Fatalf("history rows %d", hist)
	}
}

func TestPublicEndpointsOnlyViaGatewayForOwnSubject(t *testing.T) {
	h := newHarness(t)
	user := ids.New()
	id := h.createReady(user, "", nil)
	other := h.createReady(ids.New(), "", nil)
	me := &serviceauth.Delegation{ActorDomain: platform.DomainIndividualOwner, ActorID: user, OriginalRequestID: "r"}
	gw := func(path string, actor *serviceauth.Delegation) result {
		return h.do(call{method: "GET", path: path, as: platform.APIGateway, actor: actor, header: serviceauth.HeaderServiceAuthorization})
	}
	r := gw("/api/v1/verification/cases/"+id.String(), me)
	var pub map[string]any
	r.data(t, &pub)
	if r.status != 200 || pub["status"] != "DRAFT" || pub["provider"] != nil || pub["requested_by_service"] != nil {
		t.Fatalf("public view: %d %v", r.status, pub)
	}
	if r := gw("/api/v1/verification/cases/"+other.String(), me); r.status != 404 {
		t.Fatalf("other user's case visible: %d", r.status)
	}
	// Same UUID but different domain is a different identity.
	artisan := &serviceauth.Delegation{ActorDomain: platform.DomainArtisan, ActorID: user}
	if r := gw("/api/v1/verification/cases/"+id.String(), artisan); r.status != 404 {
		t.Fatalf("cross-domain identity leak: %d", r.status)
	}
	list := gw("/api/v1/verification/cases", me)
	var page struct {
		Data []map[string]any `json:"data"`
		Page struct {
			HasMore bool `json:"has_more"`
		} `json:"page"`
	}
	_ = json.Unmarshal(list.body, &page)
	if list.status != 200 || len(page.Data) != 1 {
		t.Fatalf("list: %d %s", list.status, list.body)
	}
	if r := gw("/api/v1/verification/cases", nil); r.status != 401 {
		t.Fatalf("no actor: %d", r.status)
	}
	if r := h.do(call{method: "GET", path: "/api/v1/verification/cases", as: platform.IndividualOwnerService, actor: me, header: serviceauth.HeaderServiceAuthorization}); r.status != 403 {
		t.Fatalf("public route bypassing gateway: %d", r.status)
	}
}

func TestHealthReadyVersion(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/health", "/ready", "/version"} {
		if r := h.do(call{method: "GET", path: p}); r.status != http.StatusOK {
			t.Fatalf("%s: %d", p, r.status)
		}
	}
	h.app.Pool.Close()
	if r := h.do(call{method: "GET", path: "/ready"}); r.status != http.StatusServiceUnavailable {
		t.Fatalf("ready with closed pool: %d", r.status)
	}
}
