// Package migrations embeds the admin-service Goose migrations.
package migrations

import "embed"

// FS holds the SQL migrations in deterministic (version) order.
//
//go:embed *.sql
var FS embed.FS
