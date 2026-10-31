# Business Operations staging readiness

This runbook prepares staging only. It is not evidence that a deployment happened. Never apply these commands to production or use a production DSN.

## Actual access boundary

Repository/Actions inspection found no authorised staging deployment transport, no registered self-hosted runner and no usable staging database/provider credentials. The existing Images workflow publishes immutable GHCR digests; the existing staging Compose describes API, worker and one-shot migration processes. No new SSH transport, VPS access, production secret or production workflow is introduced.

An authorised infrastructure operator must provide the staging database, service identities, NATS credentials/ACLs, provider configuration and an approved Actions deployment transport. This coding session does not access the VPS. After that operator-owned path exists, rollout should be triggered through the repository's staging-only GitHub workflow with environment protections and `github.ref == refs/heads/staging` enforced.

## Database provisioning

1. Confirm the endpoint is the authorised staging PostgreSQL cluster. Use a protected operator connection, not a service runtime credential.
2. Execute `scripts/database/business-operations-provision.sql` with `psql -v ON_ERROR_STOP=1`. It creates only `business_operations_db`, `cf_business_owner`, `cf_business_migrator` and `cf_business_runtime`; existing conflicting ownership/privileged roles cause failure. It does not replace another service database or assign passwords.
3. Set migrator/runtime credentials through the approved secret manager/GitHub environment mechanism. The migrator assumes `cf_business_owner`; API and worker use `cf_business_runtime`. Owner is NOLOGIN. Never commit credentials, DSNs, private signing keys or decrypted MFA material.
4. Apply the immutable image's `/app/migrate up` using the migrator connection; repeat `up`, inspect `status` and require `version == 7`. The effective database must be `business_operations_db`. A malformed or cross-service database configuration is rejected before migration and at application startup.
5. Apply `scripts/database/business-operations-runtime-grants.sql`. Confirm runtime cannot create schema objects, mutate immutable history, read Goose metadata, assume owner, or access other services' objects. Other services' database ACLs require their operators' independent verification; this script deliberately does not change those databases.
6. Verify actual staging connectivity, pooling, ownership and migration status with the real runtime/migrator credentials. Disposable CI success cannot substitute for this step.

Migrations are additive/forward-only. Do not run `down-to`, drop tables, recreate a staging database or delete audit history. Rollback uses a compatible immutable image plus a reviewed forward fix where necessary. The company-scope compatibility trigger preserves the prior account API's owner insertion during rollout; do not assume an earlier account-only image provides the new workforce/approval APIs.

## Configuration checklist

Use the existing staging service environment example. Required values include:

- Separate API account signing and service-auth keys, public-key registries and key IDs; register the business account issuer/public keys in the Gateway's trusted issuer configuration.
- At least 32 bytes of `BUSINESS_CODE_PEPPER`; 32 random encryption bytes encoded as base64 for `BUSINESS_MFA_KEY`; retired key maps retained through a planned rotation window.
- Runtime database secret for API/worker and separate migrator credentials. Compose uses separate `business-operations-migrate.env`, `business-operations-service.env` and `business-operations-worker.env`; populate these through the approved secret mechanism. Secret files referenced with `_FILE` require authorised mounts supplied by that mechanism.
- Verification/Notification internal URLs and reciprocal service-auth policy/keys for business-operations-service. No user private signing key is required by the worker.
- HTTPS `BUSINESS_INVITATION_ACCEPT_URL`; invitation tokens are delivered in the URL fragment and encrypted in durable jobs.
- Authenticated NATS/JetStream URL/credentials. Business publish ACLs cover canonical `realestate.business.>` plus retained legacy `realestate.business_operations.>`; verification consumption covers `realestate.verification.>`. API workers must not receive production credentials.
- Configured Notification delivery provider. Acceptance does not prove email/SMS delivery. Capacity-specific eligibility and legal representative assertions remain unavailable provider contracts; do not enable or fabricate those decisions.

## Controlled rollout through the authorised automation

Use the Business Operations digest from the **same final staging SHA** Images artifact. Set `BUSINESS_OPERATIONS_SERVICE_DIGEST=sha256:...` and retain the previous compatible digest. Do not use mutable tags as deployment identity.

The operator-approved workflow should validate staging-only environment/ref, pull the exact digest, run only the Business Operations migration process with the migrator secret, stop on migration failure, apply runtime grants, then replace only the Business Operations API/worker and the necessary Gateway configuration. Preserve other services and containers. Do not use a whole-stack destructive reset.

Post-deployment checks must verify `/health`, `/ready`, `/version`, reported commit, container user, worker health, actual database/migration ownership, signed Gateway routing, registration/login/MFA with disposable authorised test accounts, outbox publication and provider acceptance. Verify unknown/internal routes remain blocked at the Gateway. Record deployment/run URLs, exact image digest, migration status and timestamps.

For rollback, restore a compatible previously recorded digest through the same approved automation, verify schema/API compatibility and repeat readiness/smoke checks. A failed migration must not be followed by application rollout. No automatic destructive schema rollback is safe for immutable audit/session/approval state.

## Evidence currently available

Local isolated PostgreSQL provisioning was run twice successfully; fresh/repeated Goose migrations and runtime privilege checks passed. Runtime schema CREATE and audit UPDATE permissions are false. Local component tests use real PostgreSQL and disk-backed JetStream. On implementation commit `0e4e792`, Images successfully executed the additional container certification with real PostgreSQL/NATS containers, runtime permissions, registration/outbox publication, signed Notification contract fake, outage/recovery, restart and graceful shutdown. The final delivery SHA must still be checked separately in Actions; image publishing does not prove staging rollout.

Actual staging database provisioning, credentials, external-provider delivery, rollout, live smoke and rollback proof remain unverified until the authorised operator path is available. Production and main are outside this task.

Implementation certificate: all five workflows passed at `0e4e792`; Images run 41115860326 published and certified Business Operations digest `sha256:966bc07a44985e27bd5268481e49148fa1ecd69e0b84c0e4b3f30a2d2523f302`. This is image/test evidence, not a rollout. Use the independently verified final delivery SHA/digest when an authorised staging deployment path is available.

## Section 4 rollout addition

Apply migration 00005 using the existing owner/migrator process, then reapply
runtime grants before starting the new API/worker. Do not roll back by dropping
graph identities or history. Migration creates immutable new Owner role versions
only for eligible initial representatives with current unrestricted company
role-management authority; custom/limited staff roles retain their versions.
Configure independent `inventory:approve` reviewers and INVENTORY_RESTRUCTURE /
INVENTORY_MAPPING_CHANGE policies through Section 3 before reviewed changes.

Set `MEDIA_SERVICE_URL` for API and worker when the authenticated Media endpoint
is available. New property envelopes keep the specification's property.* types,
with Business transport subjects `realestate.business.property.*` to avoid the
Individual Owner stream's existing `realestate.property.>` reservation. Grant
only the established Business publish ACL; do not alter the Owner stream.

Post-rollout smoke additionally creates disposable draft property/unit identities,
checks scoped hierarchy/summary, previews a bounded import and verifies worker
restart recovery. Exercise signed property/Media references with an authorised
provider test case. Draft creation is not proof of KYB, operating-capacity,
property-authority or downstream reservation/lease/sale dependency certification.
The repository still has no preauthorised staging transport/environment; rollout
and real connectivity remain operator-owned and unverified.

Section 4 implementation certificate: all five workflows passed at `cc29eeb`
(Backend 41169209592; Images 41169209593), including all 20 Backend jobs and the
extended graph/import/container certificate. Implementation image digest:
`sha256:ce45c0e440ecb2a91321f19bc0084a9c00ecf89522c562f91282c9fc28f84ba5`.
The final delivery correction/evidence commit builds its own digest, verified
separately in the delivery response. Neither image publication nor these tests
establish an actual staging deployment.

## Sections 5–7 rollout addition

Apply additive migrations 00006–00007 then updated runtime grants using the existing authorised migrator before replacing API/worker. Retain previous compatible immutable digest; do not drop stock/rate/quote/provider histories. Provider registry/capability certification is operator-owned and unavailable by frontend API. Configure independent pricing/rate/publication/channel reviewers and exact high-risk policies.

Set `MARKETPLACE_SERVICE_URL` identically for API and worker only after the real Marketplace consumer validates business-supply.v1, reciprocal service keys, delegated organisation, idempotent projection commit and readback. Preserve `MEDIA_SERVICE_URL` and public Media authority. Preserve Business-only NATS subjects including `realestate.business.inventory.*`, `realestate.business.rate_plan.*`, `realestate.business.listing.*`, `realestate.business.channel.*`; do not change Owner root stream subjects.

Real CapacityAuthority is still unavailable; current commercial use must remain fail-closed until its owning Verification operation is provided/certified. OTA adapters remain disabled pending actual partner access and contract evidence. Section 9 reservation receipt quarantine is intentional dependency gating, not a live reservation engine.

Audit on 2026-10-31 again found no GitHub environments, no self-hosted runners and no deployments. No deployment transport, secret or VPS access was introduced. Final SHA/image/CI evidence is in the Sections 5–7 combined certificate and delivery response. Operator must prove actual staging DB/ACL/migration status, signed commercial smoke, provider pause/readback, worker recovery and compatible rollback via operator-approved staging automation once supplied. Image publication alone never establishes deployment.

Connection authority hardening: forward-only migration `00007_channel_connection_authority.sql` persists the operation list from the signed provider credential proof. Every capability requires both provider certification and a grant to that particular connection. Existing connections default to no grants until real revalidation. `TestBusinessCommercialProviderCertificationCannotExpandCredentialAuthority` proves registry certification cannot authorize polling/webhook intake with a narrower credential. Current Goose schema version is **7**; migration 00006 remains unchanged.
