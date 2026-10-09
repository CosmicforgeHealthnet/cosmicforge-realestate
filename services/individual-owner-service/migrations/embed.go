// Package migrations embeds the individual_owner_db Goose migrations.
package migrations

import "embed"

// FS holds the SQL migrations in deterministic (version) order.
//
//go:embed *.sql
var FS embed.FS

// RequiredExtensions are the PostgreSQL extensions the migrations create
// (00009: btree_gist for the short-let no-overlap exclusion constraint).
// cmd/migrate verifies them before applying anything; see
// docs/product/individual-owner/DATABASE_PREREQUISITES.md.
var RequiredExtensions = []string{"btree_gist"}
