// Package app wires media-document-service for the api and worker processes.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/config"
	mhttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/scanning"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/storage"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// App holds wired components.
type App struct {
	Cfg     *config.Config
	Log     *slog.Logger
	Pool    *pgxpool.Pool
	Storage storage.Storage
	Scanner scanning.Scanner
	Service *usecases.Service
	Idem    *idempotency.Store
	Outbox  *outbox.Store
	Health  *health.Handler
	Router  http.Handler
}

// Overrides lets tests substitute collaborators.
type Overrides struct {
	Storage storage.Storage
	Scanner scanning.Scanner
	Now     func() time.Time
}

// Build wires the database, storage, scanner, use cases and router.
func Build(ctx context.Context, cfg *config.Config, log *slog.Logger, ov Overrides) (*App, error) {
	pool, err := database.Open(ctx, database.Config{URL: cfg.DatabaseURL, ApplicationName: "media-document-service", MaxConns: cfg.DBMaxConns})
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	a := &App{Cfg: cfg, Log: log, Pool: pool, Storage: ov.Storage, Scanner: ov.Scanner}
	if a.Storage == nil {
		a.Storage, err = storage.NewS3(storage.S3Config{Endpoint: cfg.StorageEndpoint, PublicEndpoint: cfg.StoragePublicEndpoint,
			Region: cfg.StorageRegion, Bucket: cfg.StorageBucket, AccessKeyID: cfg.StorageAccessKeyID, SecretKey: cfg.StorageSecretKey,
			PathStyle: cfg.StoragePathStyle})
		if err != nil {
			pool.Close()
			return nil, err
		}
	}
	if a.Scanner == nil {
		if cfg.Scanner == config.ScannerDev {
			a.Scanner = scanning.DevScanner{}
		} else {
			a.Scanner = scanning.ClamAV{Addr: cfg.ClamAVAddr}
		}
	}
	a.Outbox = outbox.NewStore(pool, cfg.SubjectPrefix)
	a.Idem = idempotency.NewStore(pool, 30*time.Second, 24*time.Hour)
	a.Service = usecases.New(pool, a.Outbox, a.Storage, a.Scanner, usecases.Options{Policies: cfg.Policies,
		UploadURLTTL: cfg.UploadURLTTL, DownloadURLTTL: cfg.DownloadURLTTL, ScanLease: cfg.ScanLease, MaxScanTries: cfg.MaxScanTries, Now: ov.Now}, log)
	a.Health = health.New(string(platform.MediaDocumentService), cfg.Environment, 3*time.Second,
		health.Check{Name: "postgres", Fn: database.Ping(pool)}, health.Check{Name: "object_storage", Fn: a.Storage.Ready})
	a.Router = mhttp.NewRouter(mhttp.Config{Service: a.Service, Idempotency: a.Idem, Verifier: cfg.Identity.Verifier, Health: a.Health, Logger: log})
	return a, nil
}

// Close releases resources.
func (a *App) Close() { a.Pool.Close() }

// Worker runs the background loops until ctx ends: outbox relay, scanning,
// deletion, rejected-file purge, upload-session expiry and housekeeping.
func (a *App) Worker(ctx context.Context) (func(context.Context), *jetstream.Conn, error) {
	conn, err := jetstream.Connect(jetstream.Config{URL: a.Cfg.NATSURL, Name: "media-document-service-worker", Logger: a.Log})
	if err != nil {
		return nil, nil, err
	}
	if a.Cfg.EnsureStream {
		if err := conn.EnsureStream(ctx, jetstream.StreamConfig{Name: jetstream.StreamName(a.Cfg.SubjectPrefix, "media"),
			Subjects: []string{a.Cfg.SubjectPrefix + ".media.>"}}); err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("ensure stream: %w", err)
		}
	}
	host, _ := os.Hostname()
	owner := fmt.Sprintf("%s-%s", host, ids.New().String()[24:])
	relay, err := outbox.NewWorker(a.Outbox, conn, outbox.WorkerConfig{Owner: owner, PollInterval: a.Cfg.OutboxPollInterval, Logger: a.Log})
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	checks := []health.Check{{Name: "postgres", Fn: database.Ping(a.Pool)}, {Name: "nats", Fn: conn.Ready}, {Name: "object_storage", Fn: a.Storage.Ready}}
	if c, ok := a.Scanner.(scanning.ClamAV); ok {
		checks = append(checks, health.Check{Name: "clamav", Fn: c.Ping})
	}
	a.Health = health.New(string(platform.MediaDocumentService), a.Cfg.Environment, 3*time.Second, checks...)
	run := func(ctx context.Context) {
		var wg sync.WaitGroup
		loop := func(name string, every time.Duration, fn func(context.Context) error) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				t := time.NewTicker(every)
				defer t.Stop()
				for {
					if err := fn(ctx); err != nil && ctx.Err() == nil {
						a.Log.Error("worker loop failed", "loop", name, "error", err.Error())
					}
					select {
					case <-ctx.Done():
						return
					case <-t.C:
					}
				}
			}()
		}
		wg.Add(1)
		go func() { defer wg.Done(); _ = relay.Run(ctx) }()
		loop("scan", a.Cfg.ScanPollInterval, func(ctx context.Context) error { _, err := a.Service.ScanDue(ctx, owner, 5); return err })
		loop("delete", a.Cfg.DeletePollInterval, func(ctx context.Context) error { _, err := a.Service.DeleteDue(ctx, owner, 20); return err })
		loop("purge", a.Cfg.PurgeInterval, func(ctx context.Context) error {
			if _, err := a.Service.PurgeRejected(ctx, 100); err != nil {
				return err
			}
			if _, err := a.Service.ExpireUploadSessions(ctx); err != nil {
				return err
			}
			if _, err := a.Idem.Purge(ctx); err != nil {
				return err
			}
			_, err := a.Outbox.PurgePublished(ctx, 7*24*time.Hour)
			return err
		})
		wg.Wait()
	}
	return run, conn, nil
}
