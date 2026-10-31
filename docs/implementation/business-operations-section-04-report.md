# Section 4 — Enterprise Inventory Graph implementation report

## A. Repository baseline

Repository: CosmicforgeHealthnet/cosmicforge-realestate. Branch: **staging only**.
Started from and repeatedly fetched/confirmed origin/staging at
`0743c32c1a423474899affcb65ec1a72d7ffb2c6` on 2026-10-31. All five baseline
workflows were green. The complete authoritative specification (Sections 1–27,
Annexes D–J) and supplied full Section 4 request were read before implementation.

Implementation commit: `cc29eebb8f2d265d2636b0b68347c4e7c07c16cd`.
Observed Actions evidence is recorded in the certificate below. A report cannot contain its own future
commit hash; the delivery response identifies the final pushed documentation SHA
and its independently observed Actions/digest.

The original two modified Individual Owner frontend files and four untracked
user documents are preserved/excluded from all task commits. No main checkout,
production change, cross-service database access or VPS connection was used.

## B. Audit and implementation matrix

Baseline portfolio/inventory directories were placeholders. Sections 1–3 already
provided working account security, tenant/RBAC, approvals, database ownership,
transactional outbox, signed providers and worker infrastructure; these were
extended rather than replaced. Existing Gateway canonical/compatibility routes,
all 96 prior routes and all 25 prior business event schemas are retained.

| Requirement / request phase | Result | Implementation / executed evidence |
|---|---|---|
| 1 baseline/architecture audit | PASS | baseline above; architecture self-test, ownership scripts; existing service/client/migration audit |
| 2 unified typed graph | PASS | portfolio/model.go, repository.go, migration 00005; typed relationship and real database integrity tests |
| 3 portfolio management | PASS | scoped CRUD, collection edges, lifecycle, hierarchy/summary; canonical structure/import grouping tests |
| 4 development/estate/phases | PASS | typed containment, phase ordering, nested phases; canonical structure test |
| 5 parcels/land | PASS structural scope | bounded topology-validated WGS84 GeoJSON, area/survey/cadastral references; 11 geometry cases; no existing PostGIS infrastructure found |
| 6 canonical property | PASS | same immutable identity across all structural contexts, current verification projection/context; property tests |
| 7 buildings/blocks/floors | PASS | explicit parent matrix, inherited physical context, scoped codes, floor numbers; canonical/integrity tests |
| 8 units | PASS | property-scoped unique codes, dimensions/classification, optional physical levels; residential/commercial/mixed-use test |
| 9 room types/rooms | PASS | category separated from physical parent, mandatory same-property reference; hotel/integrity tests |
| 10 facilities/common areas | PASS | typed metadata, bounded capacity, canonical non_sellable common-area response; structure test |
| 11 asset registry | PASS | canonical parent/component links, manufacturer/model/serial/installation/warranty references; structure/cycle tests |
| 12 controlled changes/lifecycle | PASS fail-closed framework | exact approval, current node/subtree/destination versions, source/destination/descendant authority, immutable node/edge history; reviewed move/stale-child/concurrent tests |
| 13 verification/authority | PASS contracts; live PARTIAL | signed PROPERTY_AUTHORITY read-back, subject/domain/version/expiry/restriction, reconciliation; provider-fake activation/restriction/outage test |
| 14 Media/digital twin | PASS contracts; live PARTIAL | signed metadata/purpose URL adapter, ownership, short HTTPS access, confirmed same-property space refs; adapter and PostgreSQL tests |
| 15 external identifiers | PASS | provider/type/source uniqueness and provenance; explicit reviewed remap; typed namespace/collision tests |
| 16 import engine | PASS | JSON/CSV, preview, topological dependencies, grouping, bounded transaction batches, session/authority recheck, quarantine/explicit partial, resume/dedupe/reconciliation; import tests |
| 17 schema/SQLC/transactions | PASS disposable PG | fresh/repeated Goose 5, populated-v4 upgrade, SQLC drift, tenant FKs/indexes/deferred constraints, owning data/audit/outbox rollback |
| 18 Section 3 authorization/approvals | PASS | actual graph ancestry, current deny/date/grant/delegation rules, exact frozen independent approvals; branch/tenant/revocation tests plus full prior suite |
| 19 public APIs | PASS | 188 method/path pairs; every registered handler matches OpenAPI and official Postman |
| 20 events/outbox | PASS disposable broker | 33 schemas; actual stored envelope validation, canonical graph rollback, two producer namespaces, real broker outage/restart/duplicates; prior quarantine/consumer tests retained |
| 21 workers | PASS component tests | existing scheduler, import checkpoint/claim fencing, reference reconciliation; actual container process certificate PASS at cc29eeb |
| 22 security/concurrency | PASS local suite | tenant/branch/header isolation, direct DB invariants, immediate revocation, stale preview/approval, concurrent edits/workers, provider boundaries |
| 23 E2E journeys | PASS executable test infrastructure | detailed mapping below; live providers/staging excluded |
| 24 OpenAPI/Postman | PASS | 188 routes / 107 typed schemas, populated folder 03 Portfolio & Property Hierarchy, per-entity examples/ID captures, drift/example/schema validation |
| 25 quality/security/CI | PASS local and implementation CI | gofmt/vet/staticcheck/tidy/SQLC/architecture/contract checks; govulncheck; implementation CI certificate below; final delivery SHA checked independently |
| 26 documentation | PASS | this report, service README, graph/security/data/events architecture, staging runbook/config examples |
| 27 staging commit/push | PASS implementation push | only origin/staging; implementation certificate below; final delivery SHA checked in response |

A real preexisting Section 3 timing defect was found by the graph delegation
journey: an omitted effective date was defaulted to now+1 second, causing an
immediate valid delegated read to return 403. The default now uses the same
captured transaction time as delegation validation. Explicit future dates,
expiry/revocation, direct-authority ceilings, MFA and idempotency remain enforced;
original delegation/approval security tests pass.

## C. Inventory model

[Graph/security/event contract](../architecture/BUSINESS_OPERATIONS_SECTION4_GRAPH.md)
describes the complete entity/edge/context/lifecycle model and safety decisions.
There is one canonical UUIDv7 per identity, not a second property system.
CONTAINS is physical, MEMBERSHIP is collection grouping, ASSOCIATES is a parcel
reference. Room type never contains a room physically. Required parent, same
property room category and active-parent/category rules are checked at commit.
The graph cap is 64 levels / 10000 nodes for reviewed subtree operations.

Node `version` protects metadata; `tree_version` binds descendant changes to
reviewed commands. Legal authority, capacity, branch context and immutable kind
are distinct from editable classification. No mutable label makes common areas
sellable. Retirement/archival retains UUIDs, references and audit history.

## D. Database changes

Additive, forward-only Goose migration:
`services/business-operations-service/migrations/00005_enterprise_inventory_graph.sql`.
Existing migrations 1–4 are unchanged. New canonical tables:

- inventory_nodes, inventory_edges, inventory_edge_rules, inventory_graph_versions.
- portfolios, developments, development_phases, parcels, properties, buildings,
  blocks, floors, units, room_types, rooms, facilities, common_areas, assets
  (same-ID typed anchors only).
- inventory_history, inventory_edge_history, inventory_external_mappings,
  inventory_verification_links, inventory_media_links, space_graphs,
  digital_twin_refs.
- inventory_import_jobs, inventory_import_records, inventory_import_reconciliation.

Tenant composite FKs cover hierarchy, room categories, Media/space references and
job membership/branch. External identity uniqueness includes provider and type.
Indexes cover tenant/branch/kind/state/code, ancestors/children, mappings, import
work/records and immutable history. Deferred integrity supports atomic creation
and reparenting; organisation locks prevent cycle/code races. Application IDs
use shared UUIDv7; the migration's temporary UUIDv7 seed helper is removed after
immutable legacy Owner role extension. Custom/limited roles are not expanded.

SQLC: `sql/queries/inventory.sql`, generated `internal/repositories/db/inventory.sql.go`
and models; `sqlc diff -f sql/sqlc.yaml` passes. Typed domain commands retain owning
transactions. Runtime grant script protects server-owned rules, typed anchors,
immutable node/edge/space history and durable provenance. Live staging ownership,
connection/pool and migration status remain unverified.

## E. APIs and handoff

[Complete method/path/permission/MFA/idempotency catalogue](../../services/business-operations-service/contracts/routes.json)
contains every implemented endpoint (188 total, 92 additions). All 14 collections
support list/create/detail/versioned metadata update. Required nested development
phases and property units/room-types/rooms/facilities use the same canonical IDs.
Generic inventory-node routes provide hierarchy, authorised summary, history,
reviewed move/context/structural change, lifecycle, grouping, mappings,
verification, Media access and confirmed space graphs. Imports provide creation,
status/preview/row reports, commit/pause/resume/cancel and recovery plan/rollback.

Existing Gateway identity and canonical `/api/v1/business` contract are preserved.
Unknown/internal routes stay isolated. Every command derives the tenant from
current authenticated membership, validates input, uses owning audit/outbox and
idempotency/version/locking where applicable. Lists are bounded/stably ordered
and scope filtered. Exact rows/examples/statuses are generated together in
OpenAPI and the official **03 Business Operations / 03 Portfolio & Property
Hierarchy** Postman folder. Internal/webhook/admin folders remain separate; no
public provider callback or administration bypass was added.

## F. Events and workers

All 25 previous event schemas remain; eight new schemas produce 33 total.
Canonical property.created.v1, property.updated.v1 and property.authority.changed.v1
retain the specification names. Their transport uses `realestate.business.property.*`,
because Individual Owner already reserves `realestate.property.>`. Structure,
mapping, Media, confirmed-space and import events use `realestate.business.*`.
Tests run both streams together and prove no Owner subject leakage. Producer,
UUIDv7 ID, aggregate, actor/trace and actual persisted payload schemas are checked.

The existing shared relay provides leases/retry/deduplication/quarantine. No
publication survives an owning graph rollback. Existing real-broker tests cover
outage/recovery, stale leases, republish, idempotent consumers and poison handling.
The existing worker runs bounded durable imports and provider projection refresh;
current session/role authority is checked each batch. No second scheduler or
worker user-signing key was introduced. Operator controls for partial failures,
checkpoint continuation and non-destructive reconciliation are documented.

## G. Security

Existing Argon2id/Ed25519/session/refresh/MFA/audit primitives remain unchanged.
Resource grants are joined to canonical tenant-owned ancestry, never client
headers. Branch/company/property/portfolio grants, explicit denies, dates,
revocation, delegation and MFA are enforced by the existing evaluator. Reviewed
inventory actions cannot use the owner onboarding approval exception. Same-user
approval fails; source/destination/subtree changes require a new exact review.

Provider truth is not accepted from frontend fields or raw events. Signed
read-back validates subject/case/domain and current expiry/version. Media bytes,
private keys and signed URLs are not persisted or logged. Imports do not fetch
arbitrary URLs or fabricate properties from fuzzy matches. Previously activated
destructive operations fail closed without authentic complete future-domain
safety assertions. Audit/outbox changes commit with the owning transition.

Local govulncheck reports zero reachable/imported-package vulnerabilities and
three non-reachable required-module advisories; no gate was weakened. Local
Gitleaks and final GitHub security results are recorded after execution.

## H. Executed tests and CI

Executed locally on Windows with Go 1.26.10, dedicated PostgreSQL 18 disposable
databases, real disk-backed JetStream and a dedicated Redis 8.8 instance:

| Command / check | Result |
|---|---|
| go test -json -p 2 -count=1 -timeout=15m ./services/business-operations-service/... | 120 test/subtest results PASS, 57 top-level tests, 0 FAIL, 0 SKIP |
| go test ./shared/go/... (combined isolated run) | 140 tests PASS, 0 FAIL/SKIP |
| go test ./gateway/api-gateway/... with CF_TEST_REDIS_ADDR | 24 tests PASS, 0 FAIL/SKIP |
| targeted reviewed move/descendant revision/common-area/atomic portfolio transfer checks | PASS after final added assertions |
| gofmt, go vet ./..., staticcheck ./..., go mod tidy | PASS; no module dependency change |
| sqlc generate and sqlc diff -f sql/sqlc.yaml | PASS |
| architecture self-test, architecture and Business boundaries scripts | PASS |
| generated OpenAPI/Postman drift + examples, event schema drift, OpenAPI validator | PASS: 188 routes, 107 typed schemas, 33 event schemas |
| govulncheck ./... | PASS: zero reachable/imported package vulnerabilities |
| Go compile of standalone container certificate | PASS; execution requires Linux Docker CI |

The first combined Gateway run failed because CF_TEST_REDIS_ADDR was absent.
A native disposable Redis process was then configured and all Gateway tests
passed; no test was disabled. Native Windows Redis required its real executable
and relative configuration path; unsuccessful setup attempts are not counted as
passing evidence. Local Docker/CGO race execution is unavailable; Linux Actions executed the
required race, full Backend matrix, image/container and security checks at cc29eeb.
The final atomic-transfer correction adds one top-level test (120 local results);
its final delivery SHA is independently rechecked in Actions.

Executable journey mapping:

| Journey | Evidence |
|---|---|
| 1 portfolio / 2 estate / 3 residential / 4 hotel / 5 commercial | TestBusinessEnterpriseCanonicalStructures: all 14 kinds plus three standalone property classes, nested APIs and context/typed-anchor checks |
| 6 tenant/staff/branch isolation | TestBusinessInventoryTrustedScopeRevocationAndBranchIsolation; TestBusinessInventoryDatabaseIntegrityAndConcurrentEdits |
| 7 hierarchy move | TestBusinessInventoryReviewedMoveVersionsAndImmutableHistory: SoD, exact review, retry, stale versions/descendants, immutable edge history; AtomicReviewedPortfolioTransfer proves successful transfer and rollback of an invalid destination without losing the source membership |
| 8 bulk import | PreviewResumeDedupeAndSafeRecovery; CSVGroupingValidationAndExplicitPartialExecution; StalePreviewPausesAndWorkersFenceConcurrentClaims; SessionRevocation |
| 9 external IDs | TestBusinessInventoryExternalMappingNamespacesAndCollision; exact import replay retains canonical identities |
| 10 verification/Media | AuthoritativeVerificationActivationAndExpiry; MediaOwnershipAccessAndConfirmedSpaces; signed Media adapter contract |
| 11 event resilience | InventoryEventsKeepOwnerNamespaceAndRecoverAfterBrokerRestart; BusinessOutboxRecovery; graph-owning rollback test |
| 12 future-module safety | ActivatedInventoryDependencyEvidenceFailsClosed plus activated property mutation 503; no future business execution added |

Provider fakes certify executable contracts/control flow, not actual live provider
approval, eligibility, title, delivery or staged deployment. The extended Images
certificate runs the actual Gateway, API/worker/migrator, PostgreSQL/NATS/Redis,
TOTP, property/unit graph and CSV preview/commit across worker restart.

## I. External dependencies and blockers

Re-audit of GitHub on 2026-10-31: zero self-hosted runners, no configured GitHub
environment and zero staging deployment records. Existing validation/Images
workflows are the only available automated path. No authorised deployment
transport or usable staging database/provider secrets are available in this task.
No SSH/VPS access or invented transport was introduced. Operator actions are in
[business-operations-staging-readiness.md](business-operations-staging-readiness.md).

1. Operator must provision/verify business_operations_db and least-privilege roles,
   protected secrets, service identities, NATS ACLs and approved staging workflow
   transport. Run migration 5, reapply grants, deploy the exact final image digest,
   and execute live readiness/Gateway/worker/provider smoke and rollback checks.
2. Owning Verification Service still lacks capacity-specific/legal-representative
   assertion operations recorded by Section 3. Generic KYB is not substituted.
   Live activation remains blocked until supported eligible capacity is configured.
3. Real reservation/lease/sale dependency assertion providers belong to later
   sections. Activated destructive graph changes return unavailable/unproven;
   only never-active draft changes can proceed without those providers.
4. Signed property-authority/Media contracts exist and are integrated/tested; live
   configured provider connectivity and real decisions/access remain uncertified.
5. Availability, publication, reservation, rental/sale execution, PMS, channels,
   marketplace, payments and 3D generation are future work, outside Section 4.

## J. Final certification

Repository functionality, disposable integration evidence and live operational
certification are deliberately separate.

| Area | Current certificate |
|---|---|
| Sections 1–3 regression | PASS local and complete cc29eeb Linux Backend certificate |
| Graph / hierarchy / property identity / scoped CRUD | PASS executable tests |
| Lifecycle / approval / restructuring / audit | PASS fail-closed framework |
| Import preview / execution / resume / dedupe / safe recovery | PASS executable tests |
| Database / SQLC / tenant integrity | PASS disposable PostgreSQL |
| Events / outbox / recovery | PASS real disposable JetStream |
| Verification / capacity / Media | PASS contract/control tests; live PARTIAL |
| OpenAPI / Postman / route drift | PASS |
| Security / concurrency | PASS local and cc29eeb Linux race/container/security |
| Implementation CI / immutable image digest | PASS cc29eeb certificate below; final delivery SHA/digest independently checked in response |
| Actual staging rollout / real database/provider connection | PARTIAL / NOT EXECUTED: operator dependencies above |

Overall live operational status remains **PARTIAL** until the external operator
and provider checks succeed. No scaffold is counted as executable completion.

## Executed implementation CI certificate

Observed **2026-10-31 04:46 UTC** for
`cc29eebb8f2d265d2636b0b68347c4e7c07c16cd`:

| Required workflow | Result / evidence |
|---|---|
| Backend | [41169209592](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41169209592) PASS: all 20 jobs successful, zero skipped jobs, including Individual Owner, Payment and final process-level foundation E2E |
| Architecture structure | [41169209607](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41169209607) PASS |
| Contracts | [41169209645](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41169209645) PASS, generated drift/examples, OpenAPI, schema metaschemas and Postman |
| Security | [41169209590](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41169209590) PASS, complete-history/working-tree Gitleaks, configured govulncheck and IaC scans |
| Images | [41169209593](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41169209593) PASS, all components and immutable digest artifact |

Business Linux race job [124443302007](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41169209592/job/124443302007),
artifact `backend-tests-7`: **119 test/subtest results PASS, 56 top-level tests,
0 FAIL, 0 SKIP**, using PostgreSQL 16 and real disk-backed JetStream. This count
precedes the final atomic portfolio-transfer test; the complete local delivery
rerun has **120 PASS / 57 top-level / 0 FAIL / 0 SKIP**. Conditional infrastructure
steps that do not apply to a component do not substitute for required tests.

Business image job [124443302024](https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/actions/runs/41169209593/job/124443302024)
executed the actual Gateway/Redis and non-root API/worker/migration containers,
fresh/repeated migrations and runtime grants, registration/login/TOTP,
property/unit creation, CSV preview and committed import across worker restart,
signed Notification acceptance, broker outage/recovery, health/readiness and
SIGTERM. Trivy: **0 fixable HIGH / 0 CRITICAL** under the existing scan policy.
The provider in this certificate is a signed contract fake, not live delivery.

Certified implementation image:

`ghcr.io/cosmicforgehealthnet/cosmicforge-realestate/business-operations-service@sha256:ce45c0e440ecb2a91321f19bc0084a9c00ecf89522c562f91282c9fc28f84ba5`

Closing review adds an atomic reviewed portfolio-membership replacement to the
existing relationship command (both old/new scopes and subtree versions, source
edge restored on insertion failure), and corrects Postman domain codes that had
been overwritten by the MFA OTP variable. No additional route or future-domain
execution is introduced. The final delivery commit includes those corrections
and this evidence. Its full five-workflow status and its own image digest are
observed separately and recorded in the delivery response; a new build embeds
its own commit and must not reuse this older digest as evidence for another SHA.

Final deployment re-audit still found **0 staging deployments, 0 self-hosted
runners and 0 configured GitHub environments**. No live database/provider or
staging rollout certification is claimed. After the final evidence push, the
working tree is expected to contain only the original unrelated user files.
