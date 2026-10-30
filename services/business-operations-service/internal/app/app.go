package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5/pgxpool"
)

type App struct {
	Cfg    *config.Config
	Log    *slog.Logger
	Pool   *pgxpool.Pool
	Outbox *outbox.Store
	Health *health.Handler
	Router http.Handler
}

func Build(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	pool, err := database.Open(ctx, database.Config{URL: cfg.DatabaseURL, ApplicationName: string(platform.BusinessOperationsService), MaxConns: cfg.DBMaxConns})
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	a := &App{Cfg: cfg, Log: log, Pool: pool, Outbox: outbox.NewStore(pool, cfg.SubjectPrefix)}
	a.Health = health.New(string(platform.BusinessOperationsService), cfg.Environment, 2*time.Second, health.Check{Name: "postgres", Fn: database.Ping(pool)})
	a.Router = NewRouter(a.Health)
	return a, nil
}

func (a *App) Close() {
	if a != nil && a.Pool != nil {
		a.Pool.Close()
	}
}
