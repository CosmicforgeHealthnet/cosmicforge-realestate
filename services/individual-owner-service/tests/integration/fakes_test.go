package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

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
	e, err := events.New(context.Background(), platform.VerificationService, "verification."+lowerStatus(c.Status)+".v1", "verification", c.VerificationID, payload)
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
	mu    sync.Mutex
	files map[ids.ID]media.Media
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
	mu     sync.Mutex
	status string
	match  string
	refs   map[string]payment.Beneficiary
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
