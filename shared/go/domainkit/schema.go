package domainkit

import _ "embed"

// BaseMigration is the standard platform schema each domain service applies
// as its own 00001 migration (kept byte-identical; services test equality).
//
//go:embed schema/00001_platform.sql
var BaseMigration string
