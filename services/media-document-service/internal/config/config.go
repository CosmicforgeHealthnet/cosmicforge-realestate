// Package config loads media-document-service configuration and fails fast.
package config

import (
	"errors"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/domain"
	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

// Mode selects the process.
type Mode string

const (
	ModeAPI    Mode = "api"
	ModeWorker Mode = "worker"
)

// Scanner kinds.
const (
	ScannerClamAV = "clamav"
	ScannerDev    = "dev"
)

// Config is the validated configuration.
type Config struct {
	Environment     string
	LogLevel        string
	HTTPAddr        string
	ShutdownTimeout time.Duration

	DatabaseURL security.Secret
	DBMaxConns  int32
	Identity    *serviceauth.Identity

	StorageEndpoint       string
	StoragePublicEndpoint string
	StorageRegion         string
	StorageBucket         string
	StorageAccessKeyID    security.Secret
	StorageSecretKey      security.Secret
	StoragePathStyle      bool

	Scanner    string
	ClamAVAddr string

	Policies       map[domain.Class]domain.ClassPolicy
	UploadURLTTL   time.Duration
	DownloadURLTTL time.Duration
	ScanLease      time.Duration
	MaxScanTries   int

	NATSURL       string
	SubjectPrefix string
	EnsureStream  bool

	OutboxPollInterval time.Duration
	ScanPollInterval   time.Duration
	DeletePollInterval time.Duration
	PurgeInterval      time.Duration

	OTLPEndpoint string
	OTLPInsecure bool
	SampleRatio  float64
}

// Load reads configuration for mode.
func Load(l *sharedcfg.Loader, mode Mode) (*Config, error) {
	c := &Config{}
	c.Environment = l.Environment()
	c.LogLevel = l.String("LOG_LEVEL", "info")
	def := ":8080"
	if mode == ModeWorker {
		def = ":8081"
	}
	c.HTTPAddr = l.String("HTTP_ADDR", def)
	c.ShutdownTimeout = l.Duration("SHUTDOWN_TIMEOUT", 20*time.Second)
	c.DatabaseURL = l.Secret("DATABASE_URL", true)
	c.DBMaxConns = int32(l.Int("DB_MAX_CONNS", 10))
	c.StorageEndpoint = l.String("STORAGE_ENDPOINT", "")
	c.StoragePublicEndpoint = l.String("STORAGE_PUBLIC_ENDPOINT", "")
	c.StorageRegion = l.Required("STORAGE_REGION")
	c.StorageBucket = l.Required("STORAGE_BUCKET")
	c.StorageAccessKeyID = l.Secret("STORAGE_ACCESS_KEY_ID", true)
	c.StorageSecretKey = l.Secret("STORAGE_SECRET_ACCESS_KEY", true)
	c.StoragePathStyle = l.Bool("STORAGE_PATH_STYLE", false)
	c.Scanner = l.OneOf("MEDIA_SCANNER", ScannerClamAV, ScannerClamAV, ScannerDev)
	c.ClamAVAddr = l.String("CLAMAV_ADDR", "")
	c.UploadURLTTL = l.Duration("MEDIA_UPLOAD_URL_TTL", 15*time.Minute)
	c.DownloadURLTTL = l.Duration("MEDIA_DOWNLOAD_URL_TTL", 5*time.Minute)
	c.ScanLease = l.Duration("MEDIA_SCAN_LEASE", 5*time.Minute)
	c.MaxScanTries = l.Int("MEDIA_MAX_SCAN_TRIES", 5)
	c.SubjectPrefix = l.String("NATS_SUBJECT_PREFIX", events.DefaultSubjectPrefix)
	c.EnsureStream = l.Bool("NATS_ENSURE_STREAM", true)
	c.OutboxPollInterval = l.Duration("OUTBOX_POLL_INTERVAL", 500*time.Millisecond)
	c.ScanPollInterval = l.Duration("SCAN_POLL_INTERVAL", time.Second)
	c.DeletePollInterval = l.Duration("DELETE_POLL_INTERVAL", 5*time.Second)
	c.PurgeInterval = l.Duration("PURGE_INTERVAL", time.Hour)
	c.OTLPEndpoint = l.String("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	c.OTLPInsecure = l.Bool("OTEL_EXPORTER_OTLP_INSECURE", false)
	c.SampleRatio = l.Float("OTEL_TRACES_SAMPLER_RATIO", 0.1)
	c.Policies = domain.DefaultPolicies()
	for class, p := range c.Policies {
		p.MaxBytes = l.Int64("MEDIA_MAX_BYTES_"+string(class), p.MaxBytes)
		c.Policies[class] = p
	}
	if mode == ModeWorker {
		c.NATSURL = l.Required("NATS_URL")
	}
	if err := l.Err(); err != nil {
		return nil, err
	}
	var problems []string
	if c.Scanner == ScannerDev && c.Environment == sharedcfg.EnvProduction {
		problems = append(problems, "MEDIA_SCANNER=dev is not allowed in production")
	}
	if c.Scanner == ScannerClamAV && c.ClamAVAddr == "" && mode == ModeWorker {
		problems = append(problems, "CLAMAV_ADDR is required when MEDIA_SCANNER=clamav")
	}
	if c.UploadURLTTL <= 0 || c.UploadURLTTL > time.Hour || c.DownloadURLTTL <= 0 || c.DownloadURLTTL > time.Hour {
		problems = append(problems, "signed URL TTLs must be within (0, 1h]")
	}
	for class, p := range c.Policies {
		if p.MaxBytes <= 0 || p.MaxBytes > 5<<30 {
			problems = append(problems, "MEDIA_MAX_BYTES_"+string(class)+" must be within (0, 5GiB]")
		}
	}
	if len(problems) > 0 {
		return nil, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	id, err := serviceauth.LoadIdentity(l, platform.MediaDocumentService)
	if err != nil {
		return nil, err
	}
	c.Identity = id
	return c, nil
}
