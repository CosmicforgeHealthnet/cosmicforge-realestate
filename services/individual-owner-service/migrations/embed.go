// Package migrations embeds the individual_owner_db Goose migrations.
package migrations

import "embed"

// FS holds the SQL migrations in deterministic (version) order.
//
//go:embed *.sql
var FS embed.FS
