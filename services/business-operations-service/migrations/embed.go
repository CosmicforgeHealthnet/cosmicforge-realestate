package migrations

import "embed"

// FS exposes the service-owned Goose migrations to cmd/migrate.
//
//go:embed *.sql
var FS embed.FS
