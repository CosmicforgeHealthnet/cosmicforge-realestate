# Business Operations foundation remediation

Baseline: staging `065b8e133bd19e6500fe67bcd31bfb61f2cf6b54`; fetched origin/staging matched. Unrelated Individual Owner workspace changes are preserved.

## Backend incident

Run 40984060887, job 123830771080 failed during the Individual Owner test step after approximately 53 minutes. The GitHub check annotation states: "The hosted runner lost communication with the server." Both CLI and connected GitHub job-log retrieval returned missing logs (BlobNotFound). This is a runner communication failure; the underlying cause (host failure, resource starvation, network, or process termination) is not established. No failing assertion or evidence of a Business Operations regression was available. The failed job was rerun on the unchanged SHA for controlled reproduction. The unchanged-SHA rerun passed the Individual Owner race-enabled test job (123963506900), 08:20:43–08:24:47 UTC. This classifies the incident as a transient runner communication failure; its underlying host/network cause remains unknown. The dependent end-to-end job was still running when this checkpoint was written.

Backend tests now emit JSON diagnostics, retain them as per-job artifacts, and have a 20-minute per-package test deadline to obtain goroutine dumps on hangs. Tests and race checks remain enabled. No Individual Owner implementation was changed.

## Gateway routes

Canonical new business contract: `/api/v1/business/`. The existing reserved `/api/v1/business-operations/` prefix remains a compatibility route with identical business-domain token restrictions, forwarded user JWT, gateway service JWT and authentication rate limits. Paths are preserved upstream. Neither prefix exposes `/internal/`. Application handlers still determine whether an operation exists; adding a prefix does not certify Section 3 endpoints.

## Contracts

Business Operations adapters use shared authenticated HTTP clients for Verification and Notification. Verification already supports `BUSINESS` cases for `BUSINESS` subjects in the `business-operations` domain. Creation uses an idempotency key; reads preserve provider status. Only Verification owns verification decisions. Notification acceptance is distinct from delivery. Adapter tests verify signed service identity, audience, request body, idempotency and provider outage handling. These tests use HTTP contract fakes; live provider certification remains outstanding. Application composition, Section 3 callers, durable notification and verification workers are now implemented; see the Section 3 report for the current certification matrix.

## NATS certification

The Business Operations migration integration suite now includes real disk-backed JetStream with disposable PostgreSQL using existing test infrastructure. It covers owning transaction rollback, committed publication, broker outage/restart, relay restart, deduplication, processed-event idempotency, expired lease recovery, stale-owner fencing, bounded poison-event quarantine and cancellation. Local certification now runs against a separate disposable PostgreSQL 18 cluster on loopback port 55439, never the existing installed instance. The original recovery test passed ten repetitions; the production-stream/real-consumer revision passed three repetitions. CI also passed the original revision on c84ffa1. Quarantine uses shared `outbox_events.dead_at`; rows are retained for operator review. It does not imply that arbitrary event consumers have been implemented.

## Staging operational boundary

Repository API inspection found zero self-hosted Actions runners, no visible repository secrets and no accessible staging environment secrets (404). No authorized staging deployment transport or rollout workflow exists. These observations do not prove the remote database is absent; its existence and role grants cannot be inspected without an authorized connection. No remote host was accessed.

The existing staging Compose uses an immutable `BUSINESS_OPERATIONS_SERVICE_DIGEST` and separate migration/API/worker commands. An authorized operator must:

1. Provision or verify `business_operations_db`, its migration owner and a separate runtime role restricted to that database. Remove public database connection/schema-create grants where compatible with the shared PostgreSQL host; verify denial against every other service database. Provide runtime DML grants and migration-owner DDL grants separately. Do not grant runtime superuser, CREATEDB, CREATEROLE or other-service ownership.
2. Supply DATABASE_URL and NATS authentication using the existing external service environment mechanism; configure service identity/key trust for the implemented Verification, Notification and Gateway composition. Never commit key material.
3. Establish a preauthorized GitHub deployment transport and staging environment secret policy. Until then, a deployment workflow cannot honestly perform a rollout. The coding agent must not obtain SSH access.
4. Verify the Images workflow digest artifact for the selected SHA, run migration preflight/up/status as a controlled one-off process, then start only Business Operations API/worker and the configured Gateway update. Do not restart unrelated services unnecessarily.
5. Check API and worker readiness, Gateway routing and broker backlog. On failure, retain database state and outbox rows; restore the previous API/worker image digest. Schema rollback is not automated. New migrations must remain compatible with that previous image or require an explicit recovery plan.

Live staging database, NATS credentials, grants, rollout, smoke checks and rollback remain unverified. Sections 1–2 therefore remain PARTIAL even if disposable-infrastructure CI passes.

## Additional recovered defects

The worker originally subscribed its producer stream to realestate.business_operations.>, which could not accept the specification's realestate.business.* events. A service event-contract package now supplies the actual worker and tests with one stream configuration accepting both namespaces. The stream name is preserved.

Real durable duplicate consumption exposed a shared JetStream bug: AckWait <= 1s produced a non-positive default handler timeout. The fallback now uses a positive fraction of AckWait, with a real-broker regression test. No JWT, password, service-auth or scheduler framework was replaced.

The migration test previously deferred closing its admin handle before cleanup callbacks ran, so database deletion used a closed handle. The cleanup ordering is corrected.

Controlled local Individual Owner execution: go test -json -timeout=20m -count=1 ./... with the disposable PostgreSQL cluster: 180 tests/subtests passed, zero failures or skips. Local race checks remain unavailable because no C compiler is configured; GitHub runs race checks.

## Completed implementation certification

All five workflows passed for 0e4e792: Backend 41115860251, Contracts 41115860249, Architecture 41115860305, Security 41115860499 and Images 41115860326. Business Operations race tests passed 62 tests/subtests with no failures/skips; actual Gateway/API/worker/migration and broker recovery containers passed. The final evidence-only delivery SHA is independently checked in the delivery response. See the Section 3 report for immutable digest and external-provider/staging limits.
