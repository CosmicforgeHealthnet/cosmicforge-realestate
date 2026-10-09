package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// activeOwner registers an owner through the gateway and activates the
// account with the code captured from the real notification-service.
func (p *platformEnv) activeOwner(t *testing.T) (token string, accountID ids.ID) {
	t.Helper()
	email := "events-" + ids.New().String()[24:] + "@example.com"
	r := send(t, "POST", p.gateway+"/api/v1/owner/auth/register", "", "", map[string]any{"email": email, "password": "correct horse battery",
		"display_name": "Event Owner"}, "")
	if r.status != 201 {
		t.Fatalf("register: %d %s", r.status, r.body)
	}
	var reg struct {
		Account struct {
			AccountID ids.ID `json:"account_id"`
		} `json:"account"`
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
		EmailVerification struct {
			ChallengeID ids.ID `json:"challenge_id"`
		} `json:"email_verification"`
	}
	r.data(t, &reg)
	code := p.capturedCode(email)
	if r := p.asUser(reg.Tokens.AccessToken, "POST", "/api/v1/owner/me/email/verification/confirm",
		map[string]any{"challenge_id": reg.EmailVerification.ChallengeID, "code": code}, ""); r.status != 200 {
		t.Fatalf("activate: %d %s", r.status, r.body)
	}
	return reg.Tokens.AccessToken, reg.Account.AccountID
}

// upload stores a file through the gateway's media API and waits until the
// real media worker has scanned it READY.
func (p *platformEnv) upload(t *testing.T, token, class, mime, docType string, content []byte) ids.ID {
	t.Helper()
	sum := sha256.Sum256(content)
	ext := map[string]string{"application/pdf": ".pdf", "image/jpeg": ".jpg"}[mime]
	body := map[string]any{"media_class": class, "filename": "file" + ext, "mime_type": mime, "size_bytes": len(content), "checksum_sha256": hex.EncodeToString(sum[:])}
	if docType != "" {
		// Identity evidence must be readable by the owner domain's service
		// (it checks the file is the caller's); the class default is
		// ADMIN_RESTRICTED.
		body["document_type"], body["access_classification"] = docType, "DOMAIN_RESTRICTED"
	}
	r := p.asUser(token, "POST", "/api/v1/media/uploads", body, ids.New().String())
	if r.status != 201 {
		t.Fatalf("initiate upload: %d %s", r.status, r.body)
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
	r.data(t, &up)
	req, _ := http.NewRequest(up.Upload.Method, up.Upload.URL, bytes.NewReader(content))
	for k, v := range up.Upload.Headers {
		req.Header.Set(k, v)
	}
	req.ContentLength = int64(len(content))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("signed upload: %v %v", err, resp)
	}
	resp.Body.Close()
	id := up.Media.MediaID
	if r := p.asUser(token, "POST", "/api/v1/media/"+id.String()+"/complete", nil, ids.New().String()); r.status != 200 {
		t.Fatalf("complete: %d %s", r.status, r.body)
	}
	testkit.Eventually(t, 30*time.Second, func() bool {
		r := p.asService(platform.VerificationService, platform.MediaDocumentService, "GET", "/internal/v1/media/"+id.String()+"/status", nil, "")
		return r.status == 200 && strings.Contains(string(r.body), `"status":"READY"`)
	}, "media never became READY")
	return id
}

func (p *platformEnv) processed(t *testing.T, consumer, eventType string) int {
	t.Helper()
	var n int
	if err := p.ownerDB.Pool.QueryRow(context.Background(), `SELECT count(*) FROM processed_events WHERE consumer=$1 AND event_type=$2`,
		consumer, eventType).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestOwnerConsumesRealProducerEvents is the real-process contract test for
// the two upstream producers that exist: verification-service and
// media-document-service publish to JetStream and the owner worker applies
// their events. No fake adapter is involved.
func TestOwnerConsumesRealProducerEvents(t *testing.T) {
	p := startOwnerPlatform(t, true)
	tok, _ := p.activeOwner(t)

	// verification.* — an IDENTITY case started by the owner service on the
	// real verification-service is decided by its worker (sandbox provider)
	// and the decision reaches the owner through verification.verified.v1.
	doc := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("identity document "), 200)...)
	idDoc := p.upload(t, tok, "VERIFICATION_DOCUMENT", "application/pdf", "NATIONAL_ID", doc)
	r := p.asUser(tok, "POST", "/api/v1/owner/verification", map[string]any{"evidence": []map[string]any{{"media_id": idDoc, "evidence_type": "ID_DOCUMENT_FRONT"}}},
		ids.New().String())
	if r.status != 201 {
		t.Fatalf("start identity verification: %d %s", r.status, r.body)
	}
	testkit.Eventually(t, 45*time.Second, func() bool {
		return p.processed(t, "individual-owner-service--verification", "verification.verified.v1") == 1
	}, "verification.verified.v1 from verification-service was never consumed")
	if r := p.asUser(tok, "GET", "/api/v1/owner/verification", nil, ""); r.status != 200 || !strings.Contains(string(r.body), `"status":"VERIFIED"`) {
		t.Fatalf("identity verification after the event: %d %s", r.status, r.body)
	}

	// media.* — a property photo deleted in media-document-service becomes
	// unusable in the owner domain through media.deleted.v1.
	r = p.asUser(tok, "POST", "/api/v1/owner/properties", map[string]any{"category": "RESIDENTIAL", "property_type": "APARTMENT", "title": "Event home",
		"address": map[string]any{"line1": "2 E2E Road", "city": "Lagos", "state_region": "Lagos", "country_code": "NG"}}, ids.New().String())
	if r.status != 201 {
		t.Fatalf("property: %d %s", r.status, r.body)
	}
	var prop struct {
		ID ids.ID `json:"id"`
	}
	r.data(t, &prop)
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte("photo"), 400)...)
	photo := p.upload(t, tok, "IMAGE", "image/jpeg", "", jpeg)
	mp := "/api/v1/owner/properties/" + prop.ID.String() + "/media"
	if r := p.asUser(tok, "POST", mp, map[string]any{"media_id": photo, "purpose": "PHOTO", "position": 0}, ids.New().String()); r.status != 201 {
		t.Fatalf("link photo: %d %s", r.status, r.body)
	}
	if r := p.asService(platform.IndividualOwnerService, platform.MediaDocumentService, "POST", "/internal/v1/media/"+photo.String()+"/deletion",
		map[string]any{"reason": "OWNER_REMOVED"}, ids.New().String()); r.status != 202 {
		t.Fatalf("request deletion: %d %s", r.status, r.body)
	}
	testkit.Eventually(t, 45*time.Second, func() bool {
		r := p.asUser(tok, "GET", mp, nil, "")
		return r.status == 200 && strings.Contains(string(r.body), `"DELETED"`)
	}, "media.deleted.v1 from media-document-service was never applied")
	if n := p.processed(t, "individual-owner-service--media", "media.deleted.v1"); n != 1 {
		t.Fatalf("media.deleted.v1 processed %d times", n)
	}
}
