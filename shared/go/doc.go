// Package sharedgo is the root of the CosmicForge Real Estate shared Go
// foundation module.
//
// The module contains reusable platform primitives only (identifiers, money,
// errors, request context, authentication/security primitives, idempotency,
// events, outbox, observability and test helpers). It never owns persistence
// for users, accounts or any business entity: every table referenced by the
// reusable PostgreSQL helpers lives inside the database of the service that
// uses them.
package sharedgo
