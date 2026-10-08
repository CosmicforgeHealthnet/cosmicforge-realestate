// Package migrations embeds the media_document_db Goose migrations.
package migrations

import "embed"

// FS holds the SQL migrations in deterministic (version) order.
//
//go:embed *.sql
var FS embed.FS
