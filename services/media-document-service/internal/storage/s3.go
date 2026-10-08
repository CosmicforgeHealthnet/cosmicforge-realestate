package storage

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// S3Config configures an S3-compatible store (AWS S3, Cloudflare R2,
// DigitalOcean Spaces, MinIO, ...).
type S3Config struct {
	Endpoint string // internal endpoint used by the service (empty = AWS default)
	// PublicEndpoint is the host clients use for signed URLs (when the
	// service reaches storage through a private address).
	PublicEndpoint string
	Region         string
	Bucket         string
	AccessKeyID    security.Secret
	SecretKey      security.Secret
	PathStyle      bool
	HTTPClient     *http.Client
}

// S3 implements Storage.
type S3 struct {
	cfg     S3Config
	client  *s3.Client
	presign *s3.PresignClient
}

// NewS3 builds the adapter. Credentials are held only by the service.
func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Bucket == "" || cfg.Region == "" {
		return nil, errors.New("storage: bucket and region are required")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	base := aws.Config{
		Region:      cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKeyID.Reveal(), cfg.SecretKey.Reveal(), ""),
		HTTPClient:  hc,
	}
	opts := func(endpoint string) func(o *s3.Options) {
		return func(o *s3.Options) {
			o.UsePathStyle = cfg.PathStyle
			if endpoint != "" {
				o.BaseEndpoint = aws.String(endpoint)
			}
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		}
	}
	client := s3.NewFromConfig(base, opts(cfg.Endpoint))
	pubEndpoint := cfg.PublicEndpoint
	if pubEndpoint == "" {
		pubEndpoint = cfg.Endpoint
	}
	presignClient := s3.NewPresignClient(s3.NewFromConfig(base, opts(pubEndpoint)))
	return &S3{cfg: cfg, client: client, presign: presignClient}, nil
}

func (s *S3) Bucket() string { return s.cfg.Bucket }

// CreateUploadURL presigns a PUT that is only valid with the declared
// Content-Type, Content-Length and x-amz-checksum-sha256.
func (s *S3) CreateUploadURL(ctx context.Context, req UploadRequest) (SignedURL, error) {
	if len(req.ChecksumSHA256) != 32 {
		return SignedURL{}, errors.New("storage: sha256 checksum required")
	}
	sum := base64.StdEncoding.EncodeToString(req.ChecksumSHA256)
	out, err := s.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:         aws.String(s.cfg.Bucket),
		Key:            aws.String(req.Key),
		ContentType:    aws.String(req.ContentType),
		ContentLength:  aws.Int64(req.ContentLength),
		ChecksumSHA256: aws.String(sum),
	}, s3.WithPresignExpires(req.TTL))
	if err != nil {
		return SignedURL{}, err
	}
	headers := map[string]string{}
	for k, v := range out.SignedHeader {
		if strings.EqualFold(k, "host") || len(v) == 0 {
			continue
		}
		headers[http.CanonicalHeaderKey(k)] = v[0]
	}
	headers["Content-Type"] = req.ContentType
	headers["Content-Length"] = fmt.Sprint(req.ContentLength)
	headers["X-Amz-Checksum-Sha256"] = sum
	return SignedURL{URL: out.URL, Method: http.MethodPut, Headers: headers, ExpiresAt: time.Now().Add(req.TTL).UTC()}, nil
}

// CreateDownloadURL presigns a GET with an attachment disposition.
func (s *S3) CreateDownloadURL(ctx context.Context, req DownloadRequest) (SignedURL, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(req.Key)}
	if req.Filename != "" {
		in.ResponseContentDisposition = aws.String(mime.FormatMediaType("attachment", map[string]string{"filename": req.Filename}))
	}
	if req.ContentType != "" {
		in.ResponseContentType = aws.String(req.ContentType)
	}
	out, err := s.presign.PresignGetObject(ctx, in, s3.WithPresignExpires(req.TTL))
	if err != nil {
		return SignedURL{}, err
	}
	return SignedURL{URL: out.URL, Method: http.MethodGet, ExpiresAt: time.Now().Add(req.TTL).UTC()}, nil
}

func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return true
	}
	var re *smithyhttp.ResponseError
	return errors.As(err, &re) && re.HTTPStatusCode() == http.StatusNotFound
}

func (s *S3) HeadObject(ctx context.Context, key string) (ObjectInfo, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, err
	}
	return ObjectInfo{Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType), ETag: aws.ToString(out.ETag)}, nil
}

func (s *S3) GetObject(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, err
	}
	return out.Body, ObjectInfo{Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType), ETag: aws.ToString(out.ETag)}, nil
}

func (s *S3) DeleteObject(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key)})
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}

func (s *S3) CopyObject(ctx context.Context, srcKey, dstKey string) error {
	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String(s.cfg.Bucket), Key: aws.String(dstKey), CopySource: aws.String(s.cfg.Bucket + "/" + srcKey),
	})
	if err != nil && isNotFound(err) {
		return ErrNotFound
	}
	return err
}

func (s *S3) Ready(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.cfg.Bucket)})
	return err
}
