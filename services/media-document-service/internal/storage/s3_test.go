package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// NewFakeS3 starts an in-process S3-compatible server with a bucket.
func newFake(t *testing.T) *S3 {
	backend := s3mem.New()
	_ = backend.CreateBucket("media")
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(srv.Close)
	s, err := NewS3(S3Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "media", PathStyle: true,
		AccessKeyID: security.NewSecret("AKIDTEST"), SecretKey: security.NewSecret("SECRETTEST")})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSignedUploadDownloadLifecycle(t *testing.T) {
	s := newFake(t)
	ctx := context.Background()
	body := []byte("%PDF-1.7 test document")
	sum := sha256.Sum256(body)
	up, err := s.CreateUploadURL(ctx, UploadRequest{Key: "k/1", ContentType: "application/pdf", ContentLength: int64(len(body)), ChecksumSHA256: sum[:], TTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(up.URL)
	q := u.Query()
	signed := q.Get("X-Amz-SignedHeaders")
	for _, h := range []string{"content-length", "content-type", "x-amz-checksum-sha256"} {
		if !strings.Contains(signed, h) {
			t.Fatalf("%s not bound to signature: %s", h, signed)
		}
	}
	if q.Get("X-Amz-Expires") != "900" || strings.Contains(up.URL, "SECRETTEST") || up.Method != "PUT" {
		t.Fatalf("presign: %s", up.URL)
	}
	req, _ := http.NewRequest(up.Method, up.URL, bytes.NewReader(body))
	for k, v := range up.Headers {
		req.Header.Set(k, v)
	}
	req.ContentLength = int64(len(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("upload via signed URL: %v %v", err, resp.Status)
	}
	resp.Body.Close()

	info, err := s.HeadObject(ctx, "k/1")
	if err != nil || info.Size != int64(len(body)) {
		t.Fatalf("head %+v %v", info, err)
	}
	rc, _, err := s.GetObject(ctx, "k/1")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatal("content mismatch")
	}
	dl, err := s.CreateDownloadURL(ctx, DownloadRequest{Key: "k/1", Filename: "deed.pdf", ContentType: "application/pdf", TTL: 5 * time.Minute})
	if err != nil || !strings.Contains(dl.URL, "response-content-disposition") || !strings.Contains(dl.URL, "X-Amz-Expires=300") {
		t.Fatalf("download url %s %v", dl.URL, err)
	}
	if err := s.CopyObject(ctx, "k/1", "k/2"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObject(ctx, "k/1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObject(ctx, "k/1"); err != nil {
		t.Fatal("delete must be idempotent")
	}
	if _, err := s.HeadObject(ctx, "k/1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if err := s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPublicEndpointUsedForSignedURLs(t *testing.T) {
	s, err := NewS3(S3Config{Endpoint: "http://minio.internal:9000", PublicEndpoint: "https://files.example.com", Region: "auto", Bucket: "b", PathStyle: true,
		AccessKeyID: security.NewSecret("a"), SecretKey: security.NewSecret("b")})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("x"))
	up, err := s.CreateUploadURL(context.Background(), UploadRequest{Key: "k", ContentType: "image/png", ContentLength: 1, ChecksumSHA256: sum[:], TTL: time.Minute})
	if err != nil || !strings.HasPrefix(up.URL, "https://files.example.com/b/k") {
		t.Fatalf("%s %v", up.URL, err)
	}
	if _, err := s.CreateUploadURL(context.Background(), UploadRequest{Key: "k", TTL: time.Minute}); err == nil {
		t.Fatal("checksum must be required")
	}
}

// TestRealS3ChecksumEnforcement runs against a real S3-compatible server
// (MinIO in CI) when CF_TEST_S3_ENDPOINT is set: a signed upload with the
// wrong bytes must be refused by storage itself.
func TestRealS3ChecksumEnforcement(t *testing.T) {
	endpoint := os.Getenv("CF_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("CF_TEST_S3_ENDPOINT not set")
	}
	ctx := context.Background()
	s, err := NewS3(S3Config{Endpoint: endpoint, Region: "us-east-1", Bucket: envOr("CF_TEST_S3_BUCKET", "cf-test"), PathStyle: true,
		AccessKeyID: security.NewSecret(envOr("CF_TEST_S3_ACCESS_KEY", "minioadmin")), SecretKey: security.NewSecret(envOr("CF_TEST_S3_SECRET_KEY", "minioadmin"))})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(ctx); err != nil {
		// Test infrastructure convenience: create the bucket (production
		// buckets are provisioned by Terraform, never by the service).
		if _, cerr := s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(s.cfg.Bucket)}); cerr != nil {
			t.Fatalf("bucket not reachable: %v / %v", err, cerr)
		}
	}
	body := []byte("%PDF-1.7 genuine")
	sum := sha256.Sum256(body)
	key := "it/" + time.Now().Format("150405.000000")
	up, err := s.CreateUploadURL(ctx, UploadRequest{Key: key, ContentType: "application/pdf", ContentLength: int64(len(body)), ChecksumSHA256: sum[:], TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	send := func(b []byte, ct string) int {
		req, _ := http.NewRequest(up.Method, up.URL, bytes.NewReader(b))
		for k, v := range up.Headers {
			req.Header.Set(k, v)
		}
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		req.ContentLength = int64(len(b))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := send([]byte("%PDF-1.7 tampered"), ""); code < 400 {
		t.Fatalf("tampered bytes with the same length were accepted: %d", code)
	}
	if code := send(body, "image/png"); code < 400 {
		t.Fatalf("different content type was accepted: %d", code)
	}
	if code := send(body, ""); code != 200 {
		t.Fatalf("genuine upload refused: %d", code)
	}
	if info, err := s.HeadObject(ctx, key); err != nil || info.Size != int64(len(body)) {
		t.Fatalf("head: %+v %v", info, err)
	}
	_ = s.DeleteObject(ctx, key)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
