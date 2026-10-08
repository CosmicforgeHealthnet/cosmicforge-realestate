// Package storage is the provider-independent object storage abstraction.
// Raw file bytes live only in object storage; clients upload and download
// directly with short-lived signed URLs and never receive credentials.
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

// SignedURL is a time-limited request a client may perform without credentials.
type SignedURL struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers,omitempty"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// UploadRequest describes an expected upload. The signature binds the content
// type, exact length and SHA-256 so the client cannot upload something else.
type UploadRequest struct {
	Key            string
	ContentType    string
	ContentLength  int64
	ChecksumSHA256 []byte // raw 32-byte digest
	TTL            time.Duration
}

// DownloadRequest describes a download link.
type DownloadRequest struct {
	Key         string
	Filename    string // Content-Disposition attachment name
	ContentType string
	TTL         time.Duration
}

// ObjectInfo is object metadata from the store.
type ObjectInfo struct {
	Size        int64
	ContentType string
	ETag        string
}

// ErrNotFound is returned when an object does not exist.
var ErrNotFound = errors.New("storage: object not found")

// Storage is implemented by object storage adapters.
type Storage interface {
	Bucket() string
	CreateUploadURL(ctx context.Context, req UploadRequest) (SignedURL, error)
	CreateDownloadURL(ctx context.Context, req DownloadRequest) (SignedURL, error)
	HeadObject(ctx context.Context, key string) (ObjectInfo, error)
	GetObject(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	// DeleteObject is idempotent: deleting a missing object succeeds.
	DeleteObject(ctx context.Context, key string) error
	CopyObject(ctx context.Context, srcKey, dstKey string) error
	Ready(ctx context.Context) error
}
