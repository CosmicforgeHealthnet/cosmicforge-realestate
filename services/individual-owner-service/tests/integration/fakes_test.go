package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/esign"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// fakeVerification emulates verification-service's contract: idempotent by
// key, DRAFT → SUBMITTED → PROCESSING, decisions injected by the test.
type fakeVerification struct {
	mu    sync.Mutex
	cases map[ids.ID]*verification.Case
	byKey map[string]ids.ID
	calls map[string]int
	down  bool
}

func newFakeVerification() *fakeVerification {
	return &fakeVerification{cases: map[ids.ID]*verification.Case{}, byKey: map[string]ids.ID{}, calls: map[string]int{}}
}

func (f *fakeVerification) Create(_ context.Context, typ string, subject verification.Subject, _ string, key string) (verification.Case, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["create"]++
	if f.down {
		return verification.Case{}, fmt.Errorf("dial tcp: connection refused")
	}
	if id, ok := f.byKey[key]; ok {
		return *f.cases[id], nil
	}
	c := &verification.Case{VerificationID: ids.New(), Type: typ, Subject: subject, Status: "DRAFT", UpdatedAt: time.Now().UTC()}
	f.cases[c.VerificationID], f.byKey[key] = c, c.VerificationID
	return *c, nil
}

func (f *fakeVerification) AttachEvidence(_ context.Context, id, mediaID ids.ID, typ, _ string) (verification.Evidence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["evidence"]++
	return verification.Evidence{EvidenceID: ids.New(), MediaID: mediaID, EvidenceType: typ}, nil
}

func (f *fakeVerification) step(id ids.ID, from, to string) (verification.Case, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.cases[id]
	if !ok {
		return verification.Case{}, &httpx.RemoteError{Service: "verification-service", Status: 404, Code: "VERIFICATION_NOT_FOUND"}
	}
	if c.Status == from {
		c.Status = to
	}
	return *c, nil
}

func (f *fakeVerification) Submit(_ context.Context, id ids.ID, _ string) (verification.Case, error) {
	return f.step(id, "DRAFT", "SUBMITTED")
}

func (f *fakeVerification) Process(_ context.Context, id ids.ID, _ string) (verification.Case, error) {
	return f.step(id, "SUBMITTED", "PROCESSING")
}

func (f *fakeVerification) Get(_ context.Context, id ids.ID) (verification.Case, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.cases[id]
	if !ok {
		return verification.Case{}, &httpx.RemoteError{Service: "verification-service", Status: 404, Code: "VERIFICATION_NOT_FOUND"}
	}
	return *c, nil
}

// decide sets a decision and returns the verification.* event envelope.
func (f *fakeVerification) decide(t *testing.T, id ids.ID, status, reason string, expires *time.Time) events.Envelope {
	t.Helper()
	f.mu.Lock()
	c := f.cases[id]
	c.Status, c.ReasonCode = status, reason
	now := time.Now().UTC()
	c.DecidedAt, c.ExpiresAt, c.UpdatedAt = &now, expires, now
	cc := *c
	f.mu.Unlock()
	return verificationEvent(t, cc)
}

func verificationEvent(t *testing.T, c verification.Case) events.Envelope {
	t.Helper()
	payload := map[string]any{"verification_id": c.VerificationID, "verification_type": c.Type, "subject": c.Subject, "status": c.Status,
		"requested_by_service": string(platform.IndividualOwnerService), "reason_code": c.ReasonCode}
	if c.DecidedAt != nil {
		payload["decided_at"] = c.DecidedAt
	}
	if c.ExpiresAt != nil {
		payload["expires_at"] = c.ExpiresAt
	}
	typ := "verification." + lowerStatus(c.Status) + ".v1"
	if c.Status == "MANUAL_REVIEW" {
		typ = "verification.manual_review_required.v1"
	}
	e, err := events.New(context.Background(), platform.VerificationService, typ, "verification", c.VerificationID, payload)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func lowerStatus(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// fakeMedia emulates media-document-service metadata reads.
type fakeMedia struct {
	mu      sync.Mutex
	files   map[ids.ID]media.Media
	uploads map[ids.ID][]byte
	byKey   map[string]ids.ID
}

func newFakeMedia() *fakeMedia { return &fakeMedia{files: map[ids.ID]media.Media{}} }

func (f *fakeMedia) put(owner ids.ID, class, status string) ids.ID {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := ids.New()
	f.files[id] = media.Media{MediaID: id, OwnerDomain: string(platform.DomainIndividualOwner), OwnerType: "USER", OwnerID: owner, MediaClass: class,
		Status: status, MimeType: "image/jpeg", AccessClassification: "PRIVATE"}
	return id
}

func (f *fakeMedia) Get(_ context.Context, id ids.ID) (media.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.files[id]
	if !ok {
		return media.Media{}, &httpx.RemoteError{Service: "media-document-service", Status: 404, Code: "MEDIA_NOT_FOUND"}
	}
	return m, nil
}

func (f *fakeMedia) DownloadURL(_ context.Context, id ids.ID, _ string) (media.Download, error) {
	return media.Download{URL: "https://media.invalid/" + id.String(), ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (f *fakeMedia) ReplaceAccessPolicy(context.Context, ids.ID, string, []media.Grant, string) error {
	return nil
}

// Upload stores a generated document (idempotent per key) and keeps its bytes
// so tests can inspect it.
func (f *fakeMedia) Upload(_ context.Context, req media.UploadRequest, key string) (media.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uploads == nil {
		f.uploads, f.byKey = map[ids.ID][]byte{}, map[string]ids.ID{}
	}
	if id, ok := f.byKey[key]; ok {
		return f.files[id], nil
	}
	id := ids.New()
	m := media.Media{MediaID: id, OwnerDomain: string(platform.DomainIndividualOwner), OwnerType: "USER", OwnerID: req.OwnerAccountID, MediaClass: "DOCUMENT",
		Status: "READY", MimeType: req.MimeType, AccessClassification: "PRIVATE"}
	f.files[id], f.uploads[id], f.byKey[key] = m, req.Content, id
	return m, nil
}

func mediaEvent(t *testing.T, m media.Media, status string) events.Envelope {
	t.Helper()
	e, err := events.New(context.Background(), platform.MediaDocumentService, "media."+lowerStatus(status)+".v1", "media", m.MediaID,
		map[string]any{"media_id": m.MediaID, "owner": map[string]any{"owner_domain": m.OwnerDomain, "owner_type": m.OwnerType, "owner_id": m.OwnerID},
			"status": status, "media_class": m.MediaClass})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// fakePayment stands in for the (not yet implemented) payment-service
// contract so the owner-side logic can be exercised. It is never used
// outside tests and never certifies payment behaviour.
type fakePayment struct {
	mu       sync.Mutex
	status   string
	match    string
	refs     map[string]payment.Beneficiary
	intents  map[string]*payment.Intent // by ref
	byKey    map[string]string          // idempotency key → ref
	requests map[string]payment.IntentRequest
	releases map[string]*payment.Release
	relReqs  map[string]payment.DepositReleaseRequest
	refunds  map[string]*payment.Refund
	refReqs  map[string]payment.RefundRequest
	creates  int
}

func (f *fakePayment) init() {
	if f.intents == nil {
		f.intents, f.byKey, f.requests = map[string]*payment.Intent{}, map[string]string{}, map[string]payment.IntentRequest{}
		f.releases, f.relReqs = map[string]*payment.Release{}, map[string]payment.DepositReleaseRequest{}
		f.refunds, f.refReqs = map[string]*payment.Refund{}, map[string]payment.RefundRequest{}
	}
}

func (f *fakePayment) CreateIntent(_ context.Context, req payment.IntentRequest, key string) (payment.Intent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if ref, ok := f.byKey[key]; ok {
		return *f.intents[ref], nil
	}
	f.creates++
	in := &payment.Intent{Ref: "pi_" + ids.New().String()[24:], Status: payment.IntentRequiresPayment, AmountMinor: req.AmountMinor, UpdatedAt: time.Now()}
	f.intents[in.Ref], f.byKey[key], f.requests[in.Ref] = in, in.Ref, req
	return *in, nil
}

func (f *fakePayment) GetIntent(_ context.Context, ref string) (payment.Intent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	in, ok := f.intents[ref]
	if !ok {
		return payment.Intent{}, &httpx.RemoteError{Service: "payment-service", Status: 404, Code: "NOT_FOUND"}
	}
	return *in, nil
}

func (f *fakePayment) CancelIntent(_ context.Context, ref, _ string) (payment.Intent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	in := f.intents[ref]
	if in.PaidMinor == 0 {
		in.Status, in.UpdatedAt = payment.IntentCancelled, time.Now()
	}
	return *in, nil
}

// settle simulates payment-service collecting money for the intent of reference.
func (f *fakePayment) settle(reference ids.ID, paid int64, status string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	for ref, r := range f.requests {
		if r.ReferenceID == reference {
			f.intents[ref].PaidMinor, f.intents[ref].Status, f.intents[ref].UpdatedAt = paid, status, time.Now()
			return ref
		}
	}
	return ""
}

func (f *fakePayment) RequestDepositRelease(_ context.Context, req payment.DepositReleaseRequest, key string) (payment.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if r, ok := f.releases[key]; ok {
		return *r, nil
	}
	r := &payment.Release{Ref: "rel_" + ids.New().String()[24:], Status: payment.ReleasePending}
	f.releases[key], f.releases[r.Ref], f.relReqs[r.Ref] = r, r, req
	return *r, nil
}

func (f *fakePayment) GetRelease(_ context.Context, ref string) (payment.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	return *f.releases[ref], nil
}

func (f *fakePayment) RequestRefund(_ context.Context, req payment.RefundRequest, key string) (payment.Refund, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if r, ok := f.refunds[key]; ok {
		return *r, nil
	}
	r := &payment.Refund{Ref: "rf_" + ids.New().String()[24:], Status: payment.RefundPending}
	f.refunds[key], f.refunds[r.Ref], f.refReqs[r.Ref] = r, r, req
	return *r, nil
}

func (f *fakePayment) GetRefund(_ context.Context, ref string) (payment.Refund, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	return *f.refunds[ref], nil
}

// completeRefunds marks every pending refund as paid out.
func (f *fakePayment) completeRefunds() []payment.RefundRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var done []payment.RefundRequest
	for ref, req := range f.refReqs {
		if r := f.refunds[ref]; r.Status == payment.RefundPending {
			r.Status, r.RefundedMinor = payment.RefundCompleted, req.AmountMinor
			done = append(done, req)
		}
	}
	return done
}

// completeReleases marks every pending release as executed.
func (f *fakePayment) completeReleases() []payment.DepositReleaseRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var done []payment.DepositReleaseRequest
	for ref, req := range f.relReqs {
		r := f.releases[ref]
		if r.Status != payment.ReleasePending {
			continue
		}
		r.Status, r.ReleasedMinor = payment.ReleaseCompleted, req.RefundMinor+req.DeductionMinor
		if req.WithheldMinor > 0 {
			r.Status = payment.ReleasePartial
		}
		done = append(done, req)
	}
	return done
}

// fakeESign stands in for an e-sign provider adapter (none is selected).
type fakeESign struct {
	mu        sync.Mutex
	envelopes map[string]*esign.Envelope
	byKey     map[string]string
	creates   int
}

func newFakeESign() *fakeESign {
	return &fakeESign{envelopes: map[string]*esign.Envelope{}, byKey: map[string]string{}}
}

func (f *fakeESign) CreateEnvelope(_ context.Context, req esign.EnvelopeRequest, key string) (esign.Envelope, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ref, ok := f.byKey[key]; ok {
		return *f.envelopes[ref], nil
	}
	f.creates++
	env := &esign.Envelope{Ref: "env_" + ids.New().String()[24:], Status: "SENT"}
	for _, s := range req.Signers {
		env.Signers = append(env.Signers, esign.SignerStatus{ParticipantID: s.ParticipantID, SignerRef: "sgn_" + ids.New().String()[28:], Status: esign.SignerSent})
	}
	f.envelopes[env.Ref], f.byKey[key] = env, env.Ref
	return *env, nil
}

func (f *fakeESign) GetEnvelope(_ context.Context, ref string) (esign.Envelope, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.envelopes[ref], nil
}

func (f *fakeESign) VoidEnvelope(_ context.Context, ref, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.envelopes[ref].Status = "VOIDED"
	return nil
}

// signAll completes every envelope with a certificate.
func (f *fakeESign) signAll(cert ids.ID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	for _, e := range f.envelopes {
		for i := range e.Signers {
			e.Signers[i].Status, e.Signers[i].SignedAt = esign.SignerSigned, &now
		}
		e.Status, e.CertificateMediaID = "COMPLETED", &cert
	}
}

func (f *fakePayment) RegisterBeneficiary(_ context.Context, req payment.BeneficiaryRequest, key string) (payment.Beneficiary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refs == nil {
		f.refs = map[string]payment.Beneficiary{}
	}
	if b, ok := f.refs[key]; ok {
		return b, nil
	}
	b := payment.Beneficiary{Ref: "ben_" + ids.New().String()[24:], Status: f.status, NameMatch: f.match}
	f.refs[key] = b
	return b, nil
}

func (f *fakePayment) GetBeneficiary(_ context.Context, ref string) (payment.Beneficiary, error) {
	return payment.Beneficiary{Ref: ref, Status: "VERIFIED", NameMatch: "MATCH"}, nil
}

func (f *fakePayment) ListStatements(context.Context, ids.ID, string, int) ([]payment.Statement, string, error) {
	return []payment.Statement{}, "", nil
}

var _ = json.Marshal
