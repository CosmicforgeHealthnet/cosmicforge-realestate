# ADR-0009: Separate Go Modules Per Deployable Component

**Status:** Accepted

Each microservice and the API Gateway is an independent Go module. shared/go is also its own module.

A root go.work supports monorepo development.

This prevents accidental source-level coupling between service internals and keeps service boundaries enforceable in CI.
