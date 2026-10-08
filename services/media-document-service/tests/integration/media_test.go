package integration

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/scanning"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/migrations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestMigrations(t *testing.T) { testkit.MigrationRoundTrip(t, migrations.FS) }

func TestSignedUploadScanAndDownload(t *testing.T) {
	h := newHarness(t)
	owner := ids.New()
	u, r := h.initiate(owner, "IMAGE", "image/png", "front.png", pngBytes, "", "")
	if r.status != 201 || u.Media.Status != "UPLOAD_PENDING" || u.Upload.Method != "PUT" || u.Upload.Headers["X-Amz-Checksum-Sha256"] == "" {
		t.Fatalf("initiate: %d %s", r.status, r.body)
	}
	if !strings.Contains(u.Upload.URL, "X-Amz-Signature=") || strings.Contains(u.Upload.URL, "SK") {
		t.Fatal("upload URL must be signed without exposing credentials")
	}
	// Nothing is usable before upload + scan.
	if r := h.do(call{method: "POST", path: "/internal/v1/media/" + u.Media.MediaID.String() + "/download-url", as: platform.IndividualOwnerService}); r.status != 409 || r.code() != "MEDIA_NOT_READY" {
		t.Fatalf("download before ready: %d %s", r.status, r.body)
	}
	if code := h.put(u, pngBytes); code != 200 {
		t.Fatalf("upload %d", code)
	}
	r = h.complete(u.Media.MediaID, "c1")
	var mv mediaView
	r.data(t, &mv)
	if mv.Status != "UPLOADED" || mv.ScanStatus != "PENDING" {
		t.Fatalf("after complete: %+v", mv)
	}
	h.scan()
	st, _ := h.status(u.Media.MediaID)
	if st != "READY" {
		t.Fatalf("status %s", st)
	}
	want := "media.upload_completed.v1,media.scanning.v1,media.ready.v1"
	if got := strings.Join(h.eventTypes(u.Media.MediaID), ","); got != want {
		t.Fatalf("events %s", got)
	}
	d := h.do(call{method: "POST", path: "/internal/v1/media/" + u.Media.MediaID.String() + "/download-url", as: platform.IndividualOwnerService, body: map[string]any{"purpose": "LISTING_PREVIEW"}})
	var dl struct {
		URL       string    `json:"url"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	d.data(t, &dl)
	if dl.ExpiresAt.Sub(h.clock.now()) > 6*time.Minute || !strings.Contains(dl.URL, "X-Amz-Expires=300") {
		t.Fatalf("download URL TTL: %+v", dl)
	}
	resp, err := http.Get(dl.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, pngBytes) {
		t.Fatal("downloaded bytes differ")
	}
}

func TestMIMEAndSizePolicy(t *testing.T) {
	h := newHarness(t)
	owner := ids.New()
	cases := []struct {
		class, mime, name string
		size              int
		cls, code         string
	}{
		{"IMAGE", "application/x-msdownload", "setup.exe", 10, "", "MIME_TYPE_NOT_ALLOWED"},
		{"IMAGE", "image/png", "setup.exe", 10, "", "FILE_EXTENSION_MISMATCH"},
		{"IMAGE", "image/png", "big.png", 16 << 20, "", "FILE_TOO_LARGE"},
		{"VERIFICATION_DOCUMENT", "application/pdf", "id.pdf", 10, "PUBLIC", "ACCESS_CLASSIFICATION_NOT_ALLOWED"},
		{"ARCHIVE", "application/zip", "a.zip", 10, "", "INVALID_MEDIA_CLASS"},
	}
	for _, c := range cases {
		body := map[string]any{"owner": map[string]any{"owner_domain": "individual-owner", "owner_type": "USER", "owner_id": owner},
			"media_class": c.class, "filename": c.name, "mime_type": c.mime, "size_bytes": c.size, "checksum_sha256": strings.Repeat("a", 64),
			"document_type": "ID_CARD"}
		if c.cls != "" {
			body["access_classification"] = c.cls
		}
		r := h.do(call{method: "POST", path: "/internal/v1/media/uploads", as: platform.IndividualOwnerService, key: ids.New().String(), body: body})
		if r.code() != c.code {
			t.Errorf("%s %s: %d %s", c.class, c.name, r.status, r.body)
		}
	}
	// Bad checksum format.
	_, r := h.initiate(owner, "IMAGE", "image/png", "a.png", pngBytes, "", "NOTHEX")
	if r.code() != "INVALID_CHECKSUM" {
		t.Fatalf("checksum format: %s", r.body)
	}
}

func TestScanRejections(t *testing.T) {
	h := newHarness(t)
	owner := ids.New()
	type tc struct {
		name, mime, file string
		declared, actual []byte
		reason           string
	}
	exe := append([]byte("MZ\x90\x00"), bytes.Repeat([]byte{1}, 1000)...)
	eicar := append([]byte("%PDF-1.7\n"), []byte(scanning.EICAR)...)
	tampered := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{8}, 1000)...) // same length, different bytes
	cases := []tc{
		{"checksum", "image/png", "a.png", pngBytes, tampered, "CHECKSUM_MISMATCH"},
		{"exe disguised as png", "image/png", "a.png", exe, exe, "EXECUTABLE_CONTENT"},
		{"pdf declared as jpeg", "image/jpeg", "a.jpg", pdfBytes, pdfBytes, "MIME_MISMATCH"},
		{"malware", "application/pdf", "a.pdf", eicar, eicar, "INFECTED"},
	}
	for _, c := range cases {
		class := "IMAGE"
		if c.mime == "application/pdf" {
			class = "DOCUMENT"
		}
		u, r := h.initiate(owner, class, c.mime, c.file, c.declared, "", "")
		if r.status != 201 {
			t.Fatalf("%s: %s", c.name, r.body)
		}
		h.put(u, c.actual)
		if r := h.complete(u.Media.MediaID, ids.New().String()); r.status != 200 {
			t.Fatalf("%s complete: %s", c.name, r.body)
		}
		h.scan()
		st, reason := h.status(u.Media.MediaID)
		if st != "REJECTED" || reason != c.reason {
			t.Errorf("%s: %s %s", c.name, st, reason)
		}
		if got := h.eventTypes(u.Media.MediaID); got[len(got)-1] != "media.rejected.v1" {
			t.Errorf("%s: events %v", c.name, got)
		}
		if r := h.do(call{method: "POST", path: "/internal/v1/media/" + u.Media.MediaID.String() + "/download-url", as: platform.IndividualOwnerService}); r.status != 409 {
			t.Errorf("%s: rejected file must not be downloadable: %d", c.name, r.status)
		}
	}
}

func TestUploadCompletionIdempotencyAndDuplicateFinalization(t *testing.T) {
	h := newHarness(t)
	u, _ := h.initiate(ids.New(), "IMAGE", "image/png", "a.png", pngBytes, "", "")
	id := u.Media.MediaID
	// Complete before upload: retryable conflict, same key can be retried.
	if r := h.complete(id, "k1"); r.status != 409 || r.code() != "UPLOAD_NOT_RECEIVED" {
		t.Fatalf("early complete: %d %s", r.status, r.body)
	}
	h.put(u, pngBytes)
	if r := h.complete(id, "k1"); r.status != 200 || r.header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("retry same key after upload: %d %s", r.status, r.body)
	}
	if r := h.complete(id, "k1"); r.header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("same key must replay")
	}
	// Concurrent duplicate finalization with different keys.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := h.complete(id, ids.New().String()); r.status != 200 {
				t.Errorf("duplicate finalization: %d %s", r.status, r.body)
			}
		}()
	}
	wg.Wait()
	n := 0
	for _, e := range h.eventTypes(id) {
		if e == "media.upload_completed.v1" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("upload_completed emitted %d times", n)
	}
	// Storage outage during completion fails safely.
	u2, _ := h.initiate(ids.New(), "IMAGE", "image/png", "b.png", pngBytes, "", "")
	h.put(u2, pngBytes)
	h.store.setDown(true)
	if r := h.complete(u2.Media.MediaID, "k2"); r.status != 503 {
		t.Fatalf("storage down: %d %s", r.status, r.body)
	}
	h.store.setDown(false)
	if st, _ := h.status(u2.Media.MediaID); st != "UPLOAD_PENDING" {
		t.Fatal("state changed during outage")
	}
	if r := h.complete(u2.Media.MediaID, "k2"); r.status != 200 {
		t.Fatalf("retry after outage: %d", r.status)
	}
	// Session expiry.
	u3, _ := h.initiate(ids.New(), "IMAGE", "image/png", "c.png", pngBytes, "", "")
	h.put(u3, pngBytes)
	h.clock.advance(20 * time.Minute)
	if r := h.complete(u3.Media.MediaID, "k3"); r.status != 409 || r.code() != "UPLOAD_SESSION_EXPIRED" {
		t.Fatalf("expired session: %d %s", r.status, r.body)
	}
}

func TestAccessControl(t *testing.T) {
	h := newHarness(t)
	user := ids.New()
	img := h.ready(user, "IMAGE", "image/png", "a.png", pngBytes, "PRIVATE")
	kyc := h.ready(user, "VERIFICATION_DOCUMENT", "application/pdf", "id.pdf", pdfBytes, "")

	dl := func(id ids.ID, as platform.Service, actor *serviceauth.Delegation, gateway bool) result {
		path := "/internal/v1/media/" + id.String() + "/download-url"
		if gateway {
			path = "/api/v1/media/" + id.String() + "/download-url"
		}
		return h.do(call{method: "POST", path: path, as: as, actor: actor, gateway: gateway})
	}
	me := &serviceauth.Delegation{ActorDomain: platform.DomainIndividualOwner, ActorID: user}
	other := &serviceauth.Delegation{ActorDomain: platform.DomainIndividualOwner, ActorID: ids.New()}
	sameIDOtherDomain := &serviceauth.Delegation{ActorDomain: platform.DomainArtisan, ActorID: user}

	// PRIVATE image.
	if r := dl(img, platform.APIGateway, me, true); r.status != 200 {
		t.Fatalf("owner user: %d %s", r.status, r.body)
	}
	if r := dl(img, platform.APIGateway, other, true); r.status != 404 {
		t.Fatalf("other user: %d", r.status)
	}
	if r := dl(img, platform.APIGateway, sameIDOtherDomain, true); r.status != 404 {
		t.Fatalf("same id other domain: %d", r.status)
	}
	if r := dl(img, platform.MarketplaceService, nil, false); r.status != 404 {
		t.Fatalf("other service: %d", r.status)
	}
	if r := dl(img, platform.IndividualOwnerService, nil, false); r.status != 200 {
		t.Fatalf("owner service: %d", r.status)
	}
	if r := dl(img, platform.NotificationService, nil, false); r.status != 403 {
		t.Fatalf("no capability: %d", r.status)
	}
	// ADMIN_RESTRICTED verification document.
	if r := dl(kyc, platform.APIGateway, me, true); r.status != 403 {
		t.Fatalf("owner user must not download admin-restricted: %d %s", r.status, r.body)
	}
	if r := dl(kyc, platform.IndividualOwnerService, nil, false); r.status != 403 {
		t.Fatalf("owner service must not download admin-restricted: %d", r.status)
	}
	if r := dl(kyc, platform.AdminService, nil, false); r.status != 403 {
		t.Fatalf("admin-service without admin actor: %d", r.status)
	}
	if r := dl(kyc, platform.AdminService, admin(), false); r.status != 200 {
		t.Fatalf("admin: %d %s", r.status, r.body)
	}
	if r := dl(kyc, platform.VerificationService, nil, false); r.status != 200 {
		t.Fatalf("verification-service evidence access: %d", r.status)
	}
	// Metadata: verification-service may read any metadata; other domains may not.
	if r := h.do(call{method: "GET", path: "/internal/v1/media/" + kyc.String(), as: platform.VerificationService}); r.status != 200 {
		t.Fatalf("verification metadata: %d", r.status)
	}
	if r := h.do(call{method: "GET", path: "/internal/v1/media/" + img.String(), as: platform.MarketplaceService}); r.status != 404 {
		t.Fatalf("foreign metadata: %d", r.status)
	}
	// Privileged downloads and denials are audited.
	var privileged, denied int
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE action='media.privileged_download'), count(*) FILTER (WHERE action='media.download_denied') FROM audit_events WHERE target_id=$1`, kyc).Scan(&privileged, &denied)
	if privileged != 2 || denied != 3 {
		t.Fatalf("audit privileged=%d denied=%d", privileged, denied)
	}
	// A foreign service cannot create media for another domain's owner.
	r := h.do(call{method: "POST", path: "/internal/v1/media/uploads", as: platform.MarketplaceService, key: "x",
		body: map[string]any{"owner": map[string]any{"owner_domain": "individual-owner", "owner_type": "USER", "owner_id": user},
			"media_class": "IMAGE", "filename": "a.png", "mime_type": "image/png", "size_bytes": 10, "checksum_sha256": strings.Repeat("a", 64)}})
	if r.status != 403 {
		t.Fatalf("foreign owner create: %d %s", r.status, r.body)
	}
}

func TestAccessPolicyChange(t *testing.T) {
	h := newHarness(t)
	img := h.ready(ids.New(), "IMAGE", "image/png", "a.png", pngBytes, "PRIVATE")
	put := func(as platform.Service, actor *serviceauth.Delegation, body map[string]any) result {
		return h.do(call{method: "PUT", path: "/internal/v1/media/" + img.String() + "/access-policy", as: as, actor: actor, key: ids.New().String(), body: body})
	}
	if r := put(platform.MarketplaceService, nil, map[string]any{"access_classification": "PUBLIC"}); r.status != 404 {
		t.Fatalf("non-owner policy change: %d", r.status)
	}
	if r := put(platform.IndividualOwnerService, nil, map[string]any{"access_classification": "ADMIN_RESTRICTED"}); r.status != 422 {
		t.Fatalf("disallowed classification: %d", r.status)
	}
	r := put(platform.IndividualOwnerService, nil, map[string]any{"access_classification": "PRIVATE",
		"grants": []map[string]any{{"grantee_type": "SERVICE", "service": "marketplace-service"}}})
	if r.status != 200 {
		t.Fatalf("grant: %d %s", r.status, r.body)
	}
	if r := h.do(call{method: "POST", path: "/internal/v1/media/" + img.String() + "/download-url", as: platform.MarketplaceService}); r.status != 200 {
		t.Fatalf("granted service download: %d", r.status)
	}
	put(platform.IndividualOwnerService, nil, map[string]any{"access_classification": "PRIVATE", "grants": []any{}})
	if r := h.do(call{method: "POST", path: "/internal/v1/media/" + img.String() + "/download-url", as: platform.MarketplaceService}); r.status != 404 {
		t.Fatalf("revoked grant still works: %d", r.status)
	}
	var n int
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='media.access_policy_changed'`, img).Scan(&n)
	if n != 2 {
		t.Fatalf("policy changes audited %d times", n)
	}
}

func TestDeletionAndRetention(t *testing.T) {
	h := newHarness(t)
	img := h.ready(ids.New(), "IMAGE", "image/png", "a.png", pngBytes, "")
	del := func(id ids.ID, as platform.Service) result {
		return h.do(call{method: "POST", path: "/internal/v1/media/" + id.String() + "/deletion", as: as, key: ids.New().String(), body: map[string]any{"reason": "LISTING_REMOVED"}})
	}
	if r := del(img, platform.MarketplaceService); r.status != 404 {
		t.Fatalf("foreign delete: %d", r.status)
	}
	if r := del(img, platform.IndividualOwnerService); r.status != 202 {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	h.store.setDown(true)
	if n, _ := h.app.Service.DeleteDue(context.Background(), "w", 10); n != 0 {
		t.Fatal("must not mark DELETED while storage is down")
	}
	h.store.setDown(false)
	h.clock.advance(2 * time.Minute)
	if n, err := h.app.Service.DeleteDue(context.Background(), "w", 10); err != nil || n != 1 {
		t.Fatalf("delete due: %d %v", n, err)
	}
	if st, _ := h.status(img); st != "DELETED" {
		t.Fatal(st)
	}
	var key string
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT storage_key FROM media_objects WHERE media_id=$1`, img).Scan(&key)
	if _, err := h.store.HeadObject(context.Background(), key); err == nil {
		t.Fatal("object still in storage")
	}
	if got := h.eventTypes(img); got[len(got)-1] != "media.deleted.v1" {
		t.Fatalf("events %v", got)
	}
	// Verification documents are under 5-year retention.
	kyc := h.ready(ids.New(), "VERIFICATION_DOCUMENT", "application/pdf", "id.pdf", pdfBytes, "")
	if r := del(kyc, platform.IndividualOwnerService); r.status != 409 || r.code() != "RETENTION_PERIOD_ACTIVE" {
		t.Fatalf("retention: %d %s", r.status, r.body)
	}
	// Rejected files are purged after the class window.
	u, _ := h.initiate(ids.New(), "IMAGE", "image/png", "x.png", append([]byte("MZ"), pngBytes[2:]...), "", "")
	h.put(u, append([]byte("MZ"), pngBytes[2:]...))
	h.complete(u.Media.MediaID, "kx")
	h.scan()
	if n, _ := h.app.Service.PurgeRejected(context.Background(), 10); n != 0 {
		t.Fatal("purge too early")
	}
	h.clock.advance(8 * 24 * time.Hour)
	if n, err := h.app.Service.PurgeRejected(context.Background(), 10); err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	if n, _ := h.app.Service.DeleteDue(context.Background(), "w", 10); n != 1 {
		t.Fatal("purged media not deleted")
	}
	if st, _ := h.status(u.Media.MediaID); st != "DELETED" {
		t.Fatal(st)
	}
}

func TestScanRetryLeaseRecoveryAndBoundedFailure(t *testing.T) {
	h := newHarness(t)
	u, _ := h.initiate(ids.New(), "IMAGE", "image/png", "a.png", pngBytes, "", "")
	h.put(u, pngBytes)
	h.complete(u.Media.MediaID, "k")
	h.scanner.mu.Lock()
	h.scanner.fails = 1
	h.scanner.mu.Unlock()
	h.scan()
	if st, _ := h.status(u.Media.MediaID); st != "SCANNING" {
		t.Fatalf("scanner outage must keep SCANNING, got %s", st)
	}
	h.clock.advance(2 * time.Minute) // past backoff/lease
	h.scan()
	if st, _ := h.status(u.Media.MediaID); st != "READY" {
		t.Fatalf("recovered scan: %s", st)
	}
	// Persistent scanner failure is bounded and never yields READY.
	u2, _ := h.initiate(ids.New(), "IMAGE", "image/png", "b.png", pngBytes, "", "")
	h.put(u2, pngBytes)
	h.complete(u2.Media.MediaID, "k2")
	h.scanner.mu.Lock()
	h.scanner.fails = 100
	h.scanner.mu.Unlock()
	for i := 0; i < 6; i++ {
		h.scan()
		h.clock.advance(10 * time.Minute)
	}
	if st, reason := h.status(u2.Media.MediaID); st != "REJECTED" || reason != "SCAN_FAILED" {
		t.Fatalf("bounded scan failure: %s %s", st, reason)
	}
	// Crash mid-scan: claim + SCANNING transition happen, process dies; after the
	// lease, another worker reclaims and finishes.
	h.scanner.mu.Lock()
	h.scanner.fails = 0
	h.scanner.mu.Unlock()
	u3, _ := h.initiate(ids.New(), "IMAGE", "image/png", "c.png", pngBytes, "", "")
	h.put(u3, pngBytes)
	h.complete(u3.Media.MediaID, "k3")
	_, _ = h.db.Pool.Exec(context.Background(), `UPDATE media_objects SET status='SCANNING', scan_status='PENDING', next_work_at=$2, lease_owner='dead', lease_expires_at=$2 WHERE media_id=$1`,
		u3.Media.MediaID, h.clock.now().Add(time.Minute))
	h.scan()
	if st, _ := h.status(u3.Media.MediaID); st != "SCANNING" {
		t.Fatal("must not reclaim before the lease ends")
	}
	h.clock.advance(2 * time.Minute)
	h.scan()
	if st, _ := h.status(u3.Media.MediaID); st != "READY" {
		t.Fatalf("lease recovery: %s", st)
	}
}

func TestPublicUploadViaGateway(t *testing.T) {
	h := newHarness(t)
	user := ids.New()
	me := &serviceauth.Delegation{ActorDomain: platform.DomainArtisan, ActorID: user}
	body := map[string]any{"media_class": "IMAGE", "filename": "portfolio.jpg", "mime_type": "image/jpeg",
		"size_bytes": len(pngBytes), "checksum_sha256": sum(pngBytes)}
	r := h.do(call{method: "POST", path: "/api/v1/media/uploads", as: platform.APIGateway, actor: me, gateway: true, key: "u1", body: body})
	if r.status != 201 {
		t.Fatalf("public initiate: %d %s", r.status, r.body)
	}
	var u upload
	r.data(t, &u)
	m := h.do(call{method: "GET", path: "/api/v1/media/" + u.Media.MediaID.String(), as: platform.APIGateway, actor: me, gateway: true})
	var mv mediaView
	m.data(t, &mv)
	if mv.OwnerDomain != "artisan" || mv.AccessClassification != "PRIVATE" {
		t.Fatalf("owner derived from actor: %+v", mv)
	}
	body["owner"] = map[string]any{"owner_domain": "artisan", "owner_type": "USER", "owner_id": ids.New()}
	if r := h.do(call{method: "POST", path: "/api/v1/media/uploads", as: platform.APIGateway, actor: me, gateway: true, key: "u2", body: body}); r.status != 400 {
		t.Fatalf("client-supplied owner: %d", r.status)
	}
	if r := h.do(call{method: "POST", path: "/api/v1/media/uploads", as: platform.ArtisanService, actor: me, gateway: true, key: "u3", body: body}); r.status != 403 {
		t.Fatalf("public route without gateway: %d", r.status)
	}
	other := &serviceauth.Delegation{ActorDomain: platform.DomainArtisan, ActorID: ids.New()}
	if r := h.do(call{method: "POST", path: "/api/v1/media/" + u.Media.MediaID.String() + "/complete", as: platform.APIGateway, actor: other, gateway: true, key: "c"}); r.status != 404 {
		t.Fatalf("other user complete: %d", r.status)
	}
}

func TestHealthAndErrors(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/health", "/ready", "/version"} {
		if r := h.do(call{method: "GET", path: p}); r.status != 200 {
			t.Fatalf("%s %d %s", p, r.status, r.body)
		}
	}
	r := h.do(call{method: "GET", path: "/internal/v1/media/" + ids.New().String(), as: platform.IndividualOwnerService})
	if r.status != 404 || r.code() != "MEDIA_NOT_FOUND" {
		t.Fatal(r.status)
	}
	testkit.ValidateJSONSchema(t, "../../../../contracts/schemas/error-envelope.schema.json", r.body)
	if r := h.do(call{method: "GET", path: "/internal/v1/media/" + ids.New().String()}); r.status != 401 || r.code() != apperrors.CodeAuthenticationRequired {
		t.Fatal("unauthenticated")
	}
}
