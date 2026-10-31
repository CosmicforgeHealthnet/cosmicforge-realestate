# Sections 5–7 implementation and certification

## Baseline and scope

Repository: CosmicforgeHealthnet/cosmicforge-realestate. Branch: **staging only**. Starting/fetched remote commit: `a051e150f7fde5456d3568bd3481ca54b3d02b53`. Baseline five workflows passed: Backend 41170042019, Architecture structure 41170042093, Contracts 41170042059, Security 41170042107, Images 41170042087. Baseline image digest: `sha256:802b5c01cbef6e3fe0eb3bc7919c141be0216d03685d9133f47068e3abc6a155`.

No main checkout, merge, production operation, VPS access or SSH occurred. Existing unrelated Individual Owner web .gitignore/next-env changes, README_FIRST.md and Owner documentation/generator files remain outside the implementation commit. Tests use disposable local databases and disk-backed NATS; provider fixtures are explicit test doubles. No real customer data or provider credentials are used.

Authoritative inputs: full v1.0 specification Sections 1–27 and Annexes D–J, the Sections 5–7 master request and existing ownership/security/deployment docs. Implementation order followed catalogue/schema, inventory/revenue, then distribution. Existing Sections 1–4 APIs, graph, shared auth/money/outbox and service databases are retained.

## Delivery evidence

Initial implementation SHA: `0edf2adcd892ce7d0684720c121fec983e2a0bd9`. Its Repository structure, Contracts, Security and Images workflows passed; Business Operations Linux race tests passed **151/151**, with no skips/failures. Backend including all 20 jobs and process E2E also passed. The correction adds migration 00007, not a rewrite of migration 00006. Its local focused tests passed **24/24**. Final delivery SHA is the committed report revision and is independently checked in the delivery response; all five workflows must be verified for that SHA before final CI PASS.

| Initial implementation workflow | Run | Observed result |
|---|---|---|
| Repository structure | [41208841411](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41208841411) | PASS |
| Contracts | [41208841440](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41208841440) | PASS |
| Security | [41208841516](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41208841516) | PASS |
| Images | [41208841526](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41208841526) | PASS |
| Backend | [41208841404](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41208841404) | PASS (20 jobs, including process E2E) |

Initial implementation Business digest: `sha256:926301dc00a993e07ef918725e68ceef275334e80f6cfee8444d757b6407c824`. Images certified API/worker/migrator non-root startup, real PostgreSQL/NATS/Redis and Gateway processes, migration/runtime grants, health, restart/broker recovery and SIGTERM. Trivy image HIGH/CRITICAL fixed findings: 0. This initial digest is **not** the final correction's image and not evidence of deployment.

Live staging audit on 2026-10-31: GitHub repository environments `[]`, self-hosted runners count `0`, deployments `[]`. No staging deployment workflow or preauthorised runtime transport exists; only Images publication and staging Compose are present. The production promotion workflow was neither changed nor invoked. A published image is not a rollout. Actual staging DB/ACLs/secrets/provider connectivity, smoke and rollback are **PARTIAL / unverified**.

## Requirement matrix

PASS below denotes implemented repository behavior verified with the listed disposable/pure tests. PARTIAL explicitly identifies external/live certification boundaries. Future table catalogue decisions are not missing current workflow scaffolds.

| Request requirement | Result | Implementation / evidence |
|---|---|---|
| 2, 2.1–2.8 complete catalogue, aliases, owners, schemas/future domains | PASS | 279 decisions, 122 actual tables, 157 deferred; catalogue generator and real pg_catalog verification |
| 3.1–3.3 canonical products, physical pools, calendar | PASS | commercial.go, commercial_stock.go, inventory repository; unit/room-type and day-version tests |
| 3.4 atomic holds, expiry, cancellation, safe handoff | PASS | owning transaction locks/constraints; last-capacity race and expiry tests; handoff explicitly unavailable to Section 9 |
| 3.5 availability and 3.6 allotments | PASS | live eligibility and stock calculation; allotment release/expiry and tenant tests |
| 4.1–4.6 plan kinds/versions, derived, occupancy, LOS, windows | PASS | immutable rate versions, revenue exact arithmetic; derived/occupancy/LOS/window tests |
| 5.1–5.4 restrictions, conflicts, precedence, stop-sell | PASS | live inherited hard restrictions and checkout CTD; stale quote/stop-sell/intersection tests |
| 6 promotions/packages/add-ons | PASS | approved lifecycle and bounded stacking; package/promotion precedence journey |
| 7.1–7.4 money/fiscal/quotes/channel rules | PASS | integer ISO money, overflow, exact rounding, immutable fiscal provenance; pure and HTTP quote tests |
| 8 bulk rate workflow and safe rollback | PASS | immutable preview/digest, SoD amount binding, branch/product selection, atomic worker/version fencing; bulk tests |
| 9–10 typed connectors and provider registry | PASS | distribution types + 17 gated capabilities; unavailable/uncertified operations disabled |
| 11 secure connection lifecycle | PASS | opaque secret references, actual signed validation, independent approval, confirmed stop before disconnect |
| 12 producer and publication readiness | PASS | signed disposable Marketplace wire consumer, fresh Media assertions, owning transaction intent and matched ack/readback |
| 12 live Marketplace consumer/readback | PARTIAL | actual Marketplace service lacks the supply operations; operator/provider implementation needed |
| 13 actual Booking.com/Airbnb activation | PARTIAL | partner access/credentials/API certification unavailable; explicit disabled adapters; no fake SDK behavior |
| 14 inspected/versioned mappings and collision prevention | PASS | immutable preview+provider proofs, same-tenant/property checks, collision test, normalized tables |
| 15 field authority and reviewed overrides | PASS | content + stricter controls, expiry/version fences, canonical price rules; ARI override/last-sync test |
| 16–17 outbound queue, ARI, fencing, recovery | PASS | durable claims/leases, provider quota/serialization, current projections, unknown/readback/retry; outage/restart tests |
| 18 authenticated webhook/poll contracts and dedupe | PASS | Marketplace HMAC/service JWT, encrypted key, delivery key/object version dedupe; replay/poison cursor tests |
| 18.1 real canonical reservation acceptance | PARTIAL | Section 9 absent; durable QUARANTINED/DEPENDENCY_BLOCKED refs, accepted=false; no stock/reservation fabrication |
| 19 conflict review/reconciliation | PASS | immutable scoped MFA/SoD decisions, actual readback before resolution; reviewed rejection test |
| 20 health and operational measurements | PASS | persisted current state/cursors/jobs/mappings/conflicts/lag/latency; ARI outage health test |
| 21 contribution and explicit missingness | PASS | signed owning-source facts, period/hash/currency/freshness/reconciliation; exact contribution + missing input tests |
| 22 optional provider capabilities | PASS | registry/contracts explicit unsupported/certification states; no fabricated messaging/reviews/payment execution |
| 23–24 pipeline and Section 3 scope/approval integration | PASS | canonical graph → reviewed content/price → durable distribution; self approval/revocation/amount and multitenant tests |
| 25–26 public/private APIs and contract drift | PASS | 282 routes total, 94 added, 213 typed schemas; actual Chi walk vs OpenAPI/Postman; service-only intake isolated |
| 27 events/workers/recovery | PASS | seven new versioned event types, shared outbox, increasing source checkpoints, existing scheduler; actual JetStream recovery/inbox dedupe |
| 28–31 DB/domain/channel/security journeys | PASS | local service suite 150 tests passed (0 skipped); focused later reconciliation changes passed; initial Linux CI passed 151/151; corrected final source is verified separately |
| 32 local quality/architecture/vulnerability checks | PASS | gofmt, tidy, vet, staticcheck, architecture checks, Gateway routing; govulncheck: 0 reachable findings |
| 32 secret scan / 34 Docker, race, Trivy and all five Actions | PARTIAL | candidate secret scan and initial Images/Trivy passed; initial Business race 151/151 passed; exact corrected-SHA CI pending |
| 33 OpenAPI/Postman | PASS | generated schema-valid examples and handler drift gate; preserved old folders, new frontend/private folders |
| 35 reports | PASS | three section reports, this certificate, full machine/human database catalogue, architecture/contract docs |
| 36–39 live verification/capacity and staging | PARTIAL | authoritative CapacityAuthority operation is missing; runtime commercial activation remains 503/fail-closed; staging transport/secrets unverified |
| Section 8 RMS/autopilot, later PMS/ledger/messaging execution | NOT APPLICABLE | deliberately outside Sections 5–7; table ownership/catalogue preserved for future implementation |

## Fifteen required journeys

| # | Executed test evidence | Result / limit |
|---|---|---|
| 1 | Commercial harness + QuoteAtomicHoldRaceReleaseAndTenantIsolation | PASS on explicit disposable Verification/capacity fixtures; live capacity unavailable |
| 2 | RoomTypePoolUsesDistinctPhysicalRoomsAndNeverManufacturesStock | PASS; real canonical rooms; Section 4 physical inactivation remains fail-closed to unavailable downstream assertions |
| 3 | DerivedOccupancyLOSInheritedRestrictionsAndCycleRejection | PASS |
| 4 | QuoteAtomicHoldRaceReleaseAndTenantIsolation | PASS: one winner, one conflict, no partial days/history |
| 5 | QuoteRechecksRateStopSellAndCapacityAuthority | PASS: new use denied, existing hold retained |
| 6 | Bulk preview/apply/rollback, stale/session revocation and BranchBulkScopeChecksEveryPropertyAndSelectedProduct | PASS |
| 7 | SignedPublicationAmbiguousCommitReadbackAndPause | PASS signed shared HTTP consumer; no live Marketplace claim |
| 8 | MappingCollisionCannotRebindExistingProviderStock + connection mapping helper | PASS canonical IDs/proofs/collision fences |
| 9 | ARIOverrideOutageRecoveryHealthAndPeriodicReadback | PASS real HTTP outage/retry/readback, correct persisted health |
| 10 | SignedReceiptDedupeForgedCallerAndUnavailableReservationAuthority; ProviderCallbackKeyProtectionSignatureAndReplay | PASS; acceptance correctly blocked |
| 11 | PollCursorPoisonQuarantineAndReviewedConflictRejection | PASS immutable evidence/decision; no canonical overwrite |
| 12 | AllotmentReleaseExpiryAndCrossTenantChannelIsolation; branch/bulk/hold tests | PASS channels/rates/pools/mappings and limited branch scope |
| 13 | ApprovedPromotionPackageAddOnPolicyPrecedence | PASS |
| 14 | InventoryOutboxBrokerRecoveryAndConsumerEffectiveDedupe + existing shared recovery tests | PASS disk-backed JetStream outage, replay and one effective inbox action; Owner namespace empty |
| 15 | ProfitabilitySignedSourceInputsAndMissingness | PASS exact five source-backed components; missing net is null |

## Executed commands and evidence boundaries

- Go 1.26.10; PostgreSQL 18 at isolated loopback test cluster. Integration CI uses PostgreSQL 16 container. Tests create/drop unique databases. Disk-backed embedded real nats-server exercises actual JetStream.
- `go test ./... -count=1 -json`: 150 passing test cases, no failures/skips; integration package 137.099 s. Later focused four-test run passed after the new collision test exposed and fixed nil channels; a further three-test run passed with explicit reviewed readback conflict resolution and signed publication/polling. Final source is certified again by Linux Backend CI with `go test -race -json -timeout=20m -count=1`.
- `go mod tidy`, `go vet ./...`, `staticcheck ./...`; gateway `go test ./internal/routing -count=1`; architecture self-test/main and Business ownership script: passed locally.
- SQLC generation `sqlc generate -f sql/sqlc.yaml`; generated drift is verified by Backend CI. Fresh/repeated Goose, populated graph/Owner upgrade and actual table/index checks execute in integration tests.
- `python scripts/contracts/generate_business_operations.py --check --validate`, events/catalogue `--check`: 282 routes / 213 schemas, 40 event schemas, 279 table decisions. Actual handler walk checks private routes independently too.
- `govulncheck ./...`: zero reachable vulnerabilities. Scanner also reports three non-reachable findings in required modules; do not equate these with executed vulnerable symbols.
- Full local working-directory Gitleaks scan found 95 findings only in pre-existing ignored .env/Owner build output/tmp evidence, outside the commit candidate. Values were redacted, none read into reports or committed. Exported index scan: PASS, zero findings across 30.87 MB of actual candidate source. Full-history Actions scan remains to be verified below.
- Local Docker/image/Trivy and CGO race execution are unavailable; exact Linux CI results must be observed before marking them PASS. No test was disabled or bypassed.

Local logs are temporary under the task's TEMP directory and are not committed (may contain request metadata). CI uploads are the durable source for full output/counts. Failed exploratory fixtures and local PostgreSQL restart/connection-startup failures were corrected/re-run; they are not passing evidence. Captured regressions fixed include JSONB-stable payload hashes, exact mapping capability names, nil channel arrays, current checkout restriction fencing, metadata-only last-sync version guards and poison cursor retention.

## External actions and rollback

1. Verification owner must implement/configure authenticated current operating-capacity assertions; integrate the existing CapacityAuthority boundary, then prove live KYB/property/capacity restriction/expiry journeys. Never auto-approve.
2. Marketplace owner must implement/certify the versioned signed supply validation, mapping inspection, durable command, readback and optional poll operations described in the architecture/contract document. Configure reciprocal service keys and `MARKETPLACE_SERVICE_URL` for API and worker.
3. Section 9 must implement canonical reservation acceptance/commitment and source-of-truth reconciliation. Until then all external reservation changes remain quarantined/dependency-blocked and safety pauses are explicit intent.
4. Approved OTA operator must obtain provider access/property scopes, implement the actual provider-specific adapter/signature/OAuth contracts and supply certification evidence before changing capability states. Optional unsupported capabilities stay disabled.
5. Authorised staging infrastructure operator must establish an approved staging-only workflow/environment/runner/transport and secrets before rollout, provision business_operations_db and least-privilege roles, run migrations 00006–00007 then runtime grants, deploy the exact API/worker image digest and perform actual readiness/signed smoke/broker/provider/rollback checks. Do not access VPS from the coding agent or invent a transport.
6. Roll back only to a compatible immutable image through the approved staging workflow; pause/withdraw remote supply with confirmed provider outcomes first. Preserve additive schema/immutable evidence. Prefer reviewed forward corrections to dropping tables or reverting audit history.

## API additions

All public browser paths use `/api/v1/business`; the existing Gateway compatibility alias remains identical. `step_up=true` also requires live membership/grants and recent MFA; the matrix identifies the capability and exact request/response DTO. Private paths additionally use signed caller/delegation or the documented HMAC contract, never frontend bearer authentication alone. List operations have stable bounded pagination/property filtering. Side-effecting retries use transactional idempotency except externally keyed receipt ingestion.

| Method | Path | Capability | Request → response | Status | Step-up |
|---|---|---|---|---|---|
| GET | `/api/v1/business/inventory` | `products:read` | `—` → `List:Product` | 200 | False |
| PUT | `/api/v1/business/inventory` | `calendar:write` | `CommercialCalendarCommand` → `CommercialCalendarWrite` | 200 | True |
| GET | `/api/v1/business/sellable-products` | `products:read` | `—` → `List:Product` | 200 | False |
| POST | `/api/v1/business/sellable-products` | `products:write` | `CommercialProductCreate` → `Product` | 201 | True |
| GET | `/api/v1/business/sellable-products/{id}` | `products:read` | `—` → `Product` | 200 | False |
| PATCH | `/api/v1/business/sellable-products/{id}` | `products:write` | `CommercialProductPatch` → `Product` | 200 | True |
| GET | `/api/v1/business/inventory-pools` | `pools:read` | `—` → `List:Pool` | 200 | False |
| POST | `/api/v1/business/inventory-pools` | `pools:write` | `CommercialPoolCreate` → `Pool` | 201 | True |
| GET | `/api/v1/business/inventory-pools/{id}` | `pools:read` | `—` → `Pool` | 200 | False |
| PATCH | `/api/v1/business/inventory-pools/{id}` | `pools:write` | `CommercialStateChange` → `Pool` | 200 | True |
| GET | `/api/v1/business/calendar` | `calendar:read` | `—` → `CommercialCalendarResult` | 200 | False |
| GET | `/api/v1/business/availability` | `calendar:read` | `—` → `CommercialAvailabilityResult` | 200 | False |
| POST | `/api/v1/business/holds` | `holds:write` | `HoldCommand` → `Hold` | 201 | False |
| GET | `/api/v1/business/holds` | `holds:read` | `—` → `List:Hold` | 200 | False |
| GET | `/api/v1/business/holds/{id}` | `holds:read` | `—` → `Hold` | 200 | False |
| DELETE | `/api/v1/business/holds/{id}` | `holds:write` | `CommercialHoldChange` → `Hold` | 200 | False |
| POST | `/api/v1/business/holds/{id}/extend` | `holds:write` | `CommercialHoldChange` → `Hold` | 200 | True |
| POST | `/api/v1/business/holds/{id}/handoff` | `holds:write` | `—` → `Error` | 503 | True |
| GET | `/api/v1/business/allotments` | `allotments:read` | `—` → `List:CommercialAllotment` | 200 | False |
| POST | `/api/v1/business/allotments` | `allotments:write` | `CommercialAllotmentCreate` → `CommercialAllotment` | 201 | True |
| POST | `/api/v1/business/allotments/{id}/release` | `allotments:write` | `CommercialStateChange` → `CommercialAllotment` | 200 | True |
| POST | `/internal/v1/business-supply/channel-receipts` | `signed:marketplace-service` | `CommercialReceiptCommand` → `CommercialReceiptResult` | 202 | False |
| POST | `/internal/v1/business-supply/channel-financial-inputs` | `signed:financial-owner` | `CommercialFinancialInputCommand` → `CommercialInputResult` | 201 | False |
| POST | `/webhooks/v1/business-channels/{provider}/{connection_id}` | `signed:provider-callback` | `CommercialReceiptCommand` → `CommercialReceiptResult` | 202 | False |
| PUT | `/api/v1/business/channels/{id}/webhook-key` | `channels:credentials` | `CommercialWebhookKeyCommand` → `CommercialKeyResult` | 200 | True |
| GET | `/api/v1/business/channels/providers` | `channels:read` | `—` → `CommercialProviderList` | 200 | False |
| GET | `/api/v1/business/channels/connections` | `channels:read` | `—` → `List:CommercialChannelConnection` | 200 | False |
| POST | `/api/v1/business/channels/connections` | `channels:write` | `CommercialChannelCreate` → `CommercialChannelConnection` | 201 | True |
| GET | `/api/v1/business/channels/{id}` | `channels:read` | `—` → `CommercialChannelConnection` | 200 | False |
| PATCH | `/api/v1/business/channels/{id}` | `channels:write` | `CommercialStateChange` → `CommercialChannelConnection` | 200 | True |
| POST | `/api/v1/business/channels/{id}/authorize` | `channels:credentials` | `CommercialChannelAuthorize` → `CommercialChannelConnection` | 200 | True |
| GET | `/api/v1/business/channels/{id}/mappings` | `channels:read` | `—` → `CommercialMappingList` | 200 | False |
| POST | `/api/v1/business/channels/{id}/mappings/preview` | `channels:mappings` | `CommercialMappingPreview` → `CommercialMappingPreviewResult` | 201 | True |
| POST | `/api/v1/business/channels/{id}/mappings/confirm` | `channels:mappings` | `CommercialMappingConfirm` → `CommercialMappingResult` | 200 | True |
| GET | `/api/v1/business/channels/{id}/field-authority` | `channels:read` | `—` → `CommercialRawList` | 200 | False |
| GET | `/api/v1/business/channels/{id}/content-overrides` | `channels:read` | `—` → `CommercialRawList` | 200 | False |
| PUT | `/api/v1/business/channels/{id}/field-authority` | `channels:override` | `CommercialFieldAuthority` → `CommercialFieldAuthorityResult` | 200 | True |
| PUT | `/api/v1/business/channels/{id}/content-overrides/{listing_id}` | `channels:override` | `CommercialContentOverride` → `CommercialOverrideResult` | 200 | True |
| GET | `/api/v1/business/channels/{id}/sync-health` | `channels:read` | `—` → `CommercialChannelHealth` | 200 | False |
| POST | `/api/v1/business/channels/{id}/resync` | `channels:write` | `CommercialStateChange` → `CommercialResyncResult` | 202 | True |
| GET | `/api/v1/business/channels/{id}/sync-jobs` | `channels:read` | `—` → `CommercialRawList` | 200 | False |
| GET | `/api/v1/business/channels/{id}/receipts` | `channels:read` | `—` → `CommercialRawList` | 200 | False |
| GET | `/api/v1/business/channels/{id}/metrics` | `channels:report` | `—` → `CommercialChannelMetrics` | 200 | False |
| GET | `/api/v1/business/channels/{id}/profitability` | `channels:report` | `—` → `CommercialChannelProfitability` | 200 | False |
| GET | `/api/v1/business/channels/{id}/commission-rules` | `channels:read` | `—` → `CommercialRawList` | 200 | False |
| POST | `/api/v1/business/channels/{id}/commission-rules` | `channels:write` | `CommercialCommissionCommand` → `CommercialCommissionResult` | 201 | True |
| GET | `/api/v1/business/channel-conflicts` | `channels:read` | `—` → `List:CommercialChannelConflict` | 200 | False |
| GET | `/api/v1/business/channel-conflicts/{id}` | `channels:read` | `—` → `CommercialConflictDetail` | 200 | False |
| POST | `/api/v1/business/channel-conflicts/{id}/resolve` | `channels:resolve` | `CommercialConflictDecision` → `CommercialConflictResult` | 202 | True |
| GET | `/api/v1/business/listing-templates` | `content:read` | `—` → `CommercialRawList` | 200 | False |
| POST | `/api/v1/business/listing-templates` | `content:write` | `CommercialTemplateCommand` → `CommercialTemplateView` | 201 | True |
| PATCH | `/api/v1/business/listing-templates/{id}` | `content:write` | `CommercialTemplateCommand` → `CommercialTemplateView` | 200 | True |
| GET | `/api/v1/business/listings` | `content:read` | `—` → `List:CommercialListingDetail` | 200 | False |
| POST | `/api/v1/business/listings` | `content:write` | `CommercialListingCommand` → `CommercialListingDetail` | 201 | True |
| GET | `/api/v1/business/listings/{id}` | `content:read` | `—` → `CommercialListingDetail` | 200 | False |
| PATCH | `/api/v1/business/listings/{id}` | `content:write` | `CommercialListingCommand` → `CommercialListingDetail` | 200 | True |
| GET | `/api/v1/business/listings/{id}/versions` | `content:read` | `—` → `List:CommercialListingVersion` | 200 | False |
| GET | `/api/v1/business/listings/{id}/readiness` | `content:read` | `—` → `CommercialContentReadiness` | 200 | False |
| POST | `/api/v1/business/listings/{id}/submit` | `content:write` | `CommercialStateChange` → `CommercialListing` | 200 | True |
| POST | `/api/v1/business/listings/{id}/publish` | `publication:write` | `CommercialPublishCommand` → `CommercialPublicationQueued` | 202 | True |
| POST | `/api/v1/business/listings/{id}/pause` | `publication:write` | `CommercialStateChange` → `CommercialListingStopped` | 202 | True |
| POST | `/api/v1/business/listings/{id}/withdraw` | `publication:write` | `CommercialStateChange` → `CommercialListingStopped` | 202 | True |
| GET | `/api/v1/business/listings/{id}/publication` | `channels:read` | `—` → `List:CommercialPublication` | 200 | False |
| GET | `/api/v1/business/rate-plans` | `rates:read` | `—` → `List:Plan` | 200 | False |
| POST | `/api/v1/business/rate-plans` | `rates:write` | `CommercialRateCreate` → `CommercialRateResult` | 201 | True |
| GET | `/api/v1/business/rate-plans/{id}` | `rates:read` | `—` → `Plan` | 200 | False |
| PATCH | `/api/v1/business/rate-plans/{id}` | `rates:write` | `CommercialRatePatch` → `CommercialRateResult` | 200 | True |
| GET | `/api/v1/business/rate-plans/{id}/versions` | `rates:read` | `—` → `List:RateVersion` | 200 | False |
| GET | `/api/v1/business/rate-plan-versions/{id}` | `rates:read` | `—` → `RateVersion` | 200 | False |
| GET | `/api/v1/business/restrictions` | `restrictions:read` | `—` → `List:RestrictionRecord` | 200 | False |
| POST | `/api/v1/business/restrictions` | `restrictions:write` | `CommercialRestrictionCreate` → `RestrictionRecord` | 201 | True |
| PATCH | `/api/v1/business/restrictions/{id}` | `restrictions:write` | `CommercialStateChange` → `RestrictionRecord` | 200 | True |
| GET | `/api/v1/business/promotions` | `promotions:read` | `—` → `List:Promotion` | 200 | False |
| POST | `/api/v1/business/promotions` | `promotions:write` | `CommercialPromotionCreate` → `Promotion` | 201 | True |
| PATCH | `/api/v1/business/promotions/{id}` | `promotions:write` | `CommercialStateChange` → `Promotion` | 200 | True |
| GET | `/api/v1/business/add-ons` | `promotions:read` | `—` → `List:AddOn` | 200 | False |
| POST | `/api/v1/business/add-ons` | `promotions:write` | `CommercialAddOnCreate` → `AddOn` | 201 | True |
| PATCH | `/api/v1/business/add-ons/{id}` | `promotions:write` | `CommercialStateChange` → `AddOn` | 200 | True |
| GET | `/api/v1/business/packages` | `promotions:read` | `—` → `List:CommercialPackageDetail` | 200 | False |
| POST | `/api/v1/business/packages` | `promotions:write` | `CommercialPackageCreate` → `CommercialPackageDetail` | 201 | True |
| PATCH | `/api/v1/business/packages/{id}` | `promotions:write` | `CommercialStateChange` → `Package` | 200 | True |
| POST | `/api/v1/business/rate-quotes` | `pricing:read` | `CommercialQuoteCommand` → `QuoteSnapshot` | 201 | False |
| GET | `/api/v1/business/rate-quotes/{id}` | `pricing:read` | `—` → `CommercialQuoteDetail` | 200 | False |
| POST | `/api/v1/business/rates/bulk-preview` | `rates:write` | `CommercialBulkRateCommand` → `CommercialBulkView` | 201 | True |
| POST | `/api/v1/business/rates/bulk-apply` | `rates:write` | `CommercialBulkApply` → `CommercialBulkView` | 202 | True |
| POST | `/api/v1/business/rates/bulk-rollback` | `rates:write` | `CommercialBulkRollback` → `CommercialBulkView` | 201 | True |
| GET | `/api/v1/business/rates/bulk-commands/{id}` | `rates:read` | `—` → `CommercialBulkView` | 200 | False |
| GET | `/api/v1/business/tax-profiles` | `tax:read` | `—` → `List:CommercialTaxProfile` | 200 | False |
| POST | `/api/v1/business/tax-profiles` | `tax:write` | `CommercialTaxCreate` → `CommercialTaxProfile` | 201 | True |
| GET | `/api/v1/business/currency-policy` | `tax:read` | `—` → `CommercialCurrencyResult` | 200 | False |
| PUT | `/api/v1/business/currency-policy` | `tax:write` | `CommercialCurrencyCommand` → `CommercialCurrencyPolicy` | 200 | True |
| GET | `/api/v1/business/compliance-holds` | `restrictions:read` | `—` → `List:CommercialComplianceHold` | 200 | False |
| POST | `/api/v1/business/compliance-holds` | `restrictions:write` | `CommercialComplianceCreate` → `CommercialComplianceHold` | 201 | True |
| POST | `/api/v1/business/compliance-holds/{id}/revoke` | `restrictions:write` | `CommercialStateChange` → `CommercialComplianceHold` | 200 | True |

Connection authority hardening: forward-only migration `00007_channel_connection_authority.sql` persists the operation list from the signed provider credential proof. Every capability requires both provider certification and a grant to that particular connection. Existing connections default to no grants until real revalidation. `TestBusinessCommercialProviderCertificationCannotExpandCredentialAuthority` proves registry certification cannot authorize polling/webhook intake with a narrower credential. Current Goose schema version is **7**; migration 00006 remains unchanged.
