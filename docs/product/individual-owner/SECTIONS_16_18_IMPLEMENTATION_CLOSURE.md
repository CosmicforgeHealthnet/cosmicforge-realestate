# Individual Owner Sections 16–18 implementation closure

Date: 13 October 2026. Authority: the user's Sections 16–18 implementation prompt and original Owner specification pages 26–27. The document supplies requirements; it does not grant provider, banking, custody, legal or deployment approval. Scope stops after Section 18.

**Section 16 LOCAL SECURITY/PRIVACY PASS; Section 17 LOCAL FINANCIAL ARCHITECTURE/CONTRACT PASS; Section 18 LOCAL RELIABILITY/OBSERVABILITY PASS.** These are local implementation and available-contract verdicts. Missing production providers, future producers, commercial legal-party facts, operational approvals and deployed certification remain separate. They are not successful external executions.

## Repository and evidence provenance

- Starting branch: `development`; starting local and remote SHA: `f6bd214caf1ce27ee8628a3673a4adaf3a9f2e9b`; clean working tree.
- Owner PDF SHA-256: `eccb0ece5f0b79d125f88549ae036849796f359f779ec3407c1704af710aa8af`; original PDF pages 26–27 were extracted and checked against the retained text.
- Payment blueprint SHA-256: `fd1cfd14fb2f78ed7601fbafc49113f7d03049d3ef8041f6f08f0342d964e7f1`.
- Historical Sections 12–13 evidence is recorded at `44cce026f9586672c4bae72a977c7f002adb532c`; Sections 14–15 at the starting SHA above. Their reports and the Payment closure reports remain intact. Historical Owner 137, Gateway 22, Shared 135, platform E2E 5+5 and Payment 4/ten-story results are not relabelled as fresh executions.
- Exact final SHA, remote verification, tree status and final CI evidence are exported after commit to `tmp/payments/OWNER_SECTIONS_16_18_FINAL_METADATA.json` and its Markdown companion. A tracked report cannot contain its own commit hash.

In the tables, **U** means `services/individual-owner-service/internal/usecases/`; **T** means `services/individual-owner-service/tests/integration/`; **A** means Owner `internal/authz/`; **S** means `shared/go/`. **F** is Payment integration `TestActualOwnerFinancialCoreConsumer`, which executes the real tagged Owner child `TestFinancialCoreConsumerContract`, including all ten named stories in `T/core_workflow_cross_test.go`. **E** is `tests/e2e`; its current selected tests are listed below. Provider decisions and future-domain actors in F are explicitly TEST fixtures.

## Reuse and demonstrated changes

The existing credentials, sessions, authorization, privacy, immutable history, Financial Core consumer, schemas, lifecycle machines, outbox, leases and worker loops were reused. No second money ledger, authentication engine, broker, scheduler or observability platform was introduced. Payment Service application code and migrations were not changed.

| Demonstrated gap | Final change |
|---|---|
| Password-only recent authentication could authorize sensitive actions | Live session facts now include the verified step-up method and first-factor method. Payout changes/withdrawal, transfer, sensitive sale document grants, high-risk access and co-owner/representative changes require enrolled MFA and a recent permitted second factor. Phone OTP cannot be relabelled as independent SMS MFA. |
| Login hash upgrade could race password reset and overwrite the reset | Hash computation stays outside the lock; the original verified hash is compared under the account lock, then upgraded inside that transaction. Suspended/locked account state is rechecked. |
| Credential configuration lacked bounded configurable Argon2 factors | Existing hasher receives configurable factors with the platform baseline and resource ceilings enforced. All-zero MFA test keys are rejected in staging/production. No development signing-key fallback was added. |
| Structured payloads and formatted errors could bypass key redaction; raw URLs entered telemetry | Nested JSON attributes use recursive key redaction; opaque errors/panics are redacted while safe error codes and request correlation remain. HTTP logs/spans/metrics use route templates; outbound traces omit URLs, headers and payloads. |
| Permanent/malformed/exhausted messages were terminated with logs only | The existing JetStream consumer invokes a failure sink before termination. Owner stores immutable digest/stream/sequence/class evidence. An attributable Admin can record an immutable referenced review decision; this never executes replay or authorizes money. |
| Wallet registration retry was unbounded | Existing original-reference queue stops at 20 attempts and appears in reconciliation/manual-review metrics. No new wallet or money command is invented to repair it. |
| Smart-access revocation could call a provider inside the outer HTTP idempotency transaction | Local revocation commits first; the existing worker executes bounded provider revocation. Audit wording correctly says provider revocation is pending until recorded. Manager requests remain REQUESTED; only an MFA-verified owner approves. |
| Financing decision could call an adapter inside that transaction and repeat an ambiguous command | Existing command now has a pre-call phase. The original operation/fingerprint is committed before the bounded call; confirmation is retained for local adoption. Unknown outcomes become manual review without resubmission. |
| E-sign cancellation pre-call used read permission | It now requires the same agreement write/consent authority before touching an envelope. |
| Worker readiness and consumer shutdown were incomplete | Worker checks persistence/schema, broker, outbox stream, process context and configured required consumers. Future optional producers do not block readiness. Consumer startup is tracked; shutdown cancels and joins consumers before pool closure. |
| Missing operational measurements and trace coverage | Existing OTEL helpers now measure HTTP, DB queries/transactions, NATS, worker cycles, provider calls, authoritative local snapshots and actual broker consumer availability/backlog. API/worker use structured logging; telemetry shutdown is bounded. |

Migration **00023_event_review.sql** adds only Owner review/reference tables and extends the existing external-operation kind constraint for financing decisions. It protects review/decision history against UPDATE, DELETE and TRUNCATE and refuses rollback over populated review or financing operation history. It adds no money balances or Core entities. SQLC regenerated 22 files: only `models.go` changed, adding the two new table models; the other 21 were byte-identical. Existing OTEL/Chi/pgx versions were reused; module changes declare direct usage/checksums rather than upgrading versions.

The additive internal route is `POST /internal/v1/owner/admin/reconciliation/events/{review_id}/decision`. Its OpenAPI/Postman contract was regenerated. Review decisions require an attributable Admin actor, reason and evidence reference. A review approval is not a replay result or a financial approval.

## Section 16: all ten controls

| ID/control | Enforcement and classification | Positive/negative evidence | Local verdict and independent qualification |
|---|---|---|---|
| 16.1 Credential protection | REUSED + FIXED: U/login.go, password.go, challenges.go, session store; A; credentials/config; S/passwords, auth, jwt, sessions, security/logging | `TestLoginInvalidCredentialsAndLockout`, `TestAccessTokenValidation`, `TestRefreshRotationReuseAndRevocation`, `TestConcurrentRefreshAllowsExactlyOneRotation`, new `TestPasswordHashUpgradePreservesSuccessfulAuthentication`, `TestConcurrentPasswordResetCannotBeOverwrittenByRehash`, configurable-work-factor/zero-key tests; Shared password/JWT/session suites | PASS. Rotation overlap keys intentionally remain trusted while configured; removal rejects the old key. Production key management and security review remain pending. |
| 16.2 Recent MFA step-up | FIXED: A/property_rules.go, authz.go; U/AuthenticateAccess, banking, transfer, sale, ownership and access | `TestSensitiveActionsRequireCurrentSessionMFA`, `TestSensitiveRulesRequireAnIndependentRecentFactor`, `TestTOTPEnrolmentMFALoginRecoveryAndStepUp`, banking/transfer/access suites and F payout story. Missing enrollment, password-only assurance, repeated phone factor, wrong session, stale factor and revoked session rejected | PASS. Admin service identity does not substitute for Owner MFA. Actual production MFA channels/assurance policy require deployment review. |
| 16.3 Least privilege | REUSED + TESTED: A and PropertyAuthority; source actor/service/domain/property/capability, verified authority, mandate windows, co-owner consent and state gates | `TestPropertyAuthority`, `TestInternalPublicSummaryAdminRestrictionAndCapabilities`, `TestCoOwnersConsentAndGuardedTransfer`, `TestOwnershipTransferAfterSale`, manager/access suites, `TestReconciliationIsClassifiedAndAdminScoped`, F manager-fee/payout ownership negatives | PASS. Valid service JWT alone does not confer a capability. Manager may request access but cannot approve it or own a payout. Future domains require actual producers and scoped broker identities. |
| 16.4 Sensitive documents | REUSED + STRENGTHENED MFA: U/evidence.go, integration.go, sale.go; real Media lifecycle/ownership/classification checks | `TestPropertySaleOffersClosingAndDataRoom`, `TestApplicationsScreeningDecisionsAndOffers`; affected land-sale E2E. Cross-recipient/property, rejected media, revoked grant, purpose-window overflow and revocation during readback cases retained | PASS with bounded-URL qualification below. Owner stores references/decisions, not raw document bytes. |
| 16.5 Purpose limitation | REUSED: current recipient, relationship, purpose, expiry, revocation, consent and category checks across application, sale, tenant/guest, professional and delegation paths | Application/data-room/privacy/manager/guest announcement suites; E sale grant/revocation; current source/consent negatives in F | PASS at implemented contract boundaries. Payment/account registration never implies document or partner consent. Missing customer/manager/professional deployments are external. |
| 16.6 PII minimization | REUSED + FIXED telemetry: normalized verification/reference projections; masked bank references/fingerprint; U views/evidence/privacy; S/logging/HTTP/database telemetry | `TestPayoutAccountsWithPaymentContract`, public-summary and financial-report tests, `TestNestedSecretsAndOpaqueErrors`, `TestNormalizedHTTPSpansExcludePrivateURLsAndPropagate`, `TestOperationalSnapshotAndEventReviewRetention`, NATS payload-attribute test | PASS for inspected Owner flows. Review tables keep digest/reference metadata, not poison payloads. Legal retention and historical financial evidence were preserved. |
| 16.7 Immutable attributable audit | REUSED + NEW review decisions: U/record, auditTx, lifecycle/history; migrations 1/22/23; S/audit | Existing retention/history tests; `TestOperationalSnapshotAndEventReviewRetention`, `TestCoreWorkflowHistoryCannotBeReassignedOrRolledBack`, `TestExternalOperationMigrationRetainsPopulatedHistory`; UPDATE/DELETE/TRUNCATE and populated rollback refusals | PASS against ordinary application DML. Privileged database administrators can change DDL; production needs separate least-privilege runtime/migration roles and protected audit storage. |
| 16.8 Fraud/restrictions/escalation | REUSED + NEW event/provider review: ownership conflicts, restricted state, verification/media binding, beneficiary/mandate refusal, suspicious security events and admin reconciliation | Internal restriction/conflict, verification subject-binding, suspicious-device, protected payout/manager, privacy/dispute and event review suites | PASS for the existing deterministic signal/restriction paths. Denials are classified in correlated logs/metrics; material conflicts retain review records. No external scoring provider, automatic legal finding or approved production threshold was invented. |
| 16.9 Recovery and remote logout | REUSED + reset-race fix: password/MFA recovery, one-time challenges/codes, rotating families, session/device revoke | `TestPasswordChangeForgotResetAndNoEnumeration`, `TestCodeAttemptsAreBounded`, TOTP recovery suite, `TestLogoutRemoteRevocationAndIDOR`, concurrent refresh/reset tests; E real account journey | PASS. Device revocation ends existing sessions; a later credential-authenticated new login is alerted according to the existing policy, not silently trusted. |
| 16.10 Closure/retention | REUSED: U/lifecycle.go, disputes_privacy.go; closure holds, cooling-off and background retention | `TestAccountClosureCoolingOffAndFinalization`, `TestGuestRetentionPreservesHoldsAndAuditsEligibleAnonymization`, `TestDisputesConsentsAndPrivacyRequests`, retained financial/history guards | PASS. Authentication ends while required original beneficiary/business/dispute/history references remain; export and deletion are separate requests. Jurisdiction retention approval remains external. |

**Document URL qualification:** new access is denied immediately after grant revocation/expiry, and an issued URL may not outlive its purpose grant. Previously issued storage URLs retain the Media contract's bounded lifetime, normally five minutes. Immediate retroactive URL invalidation requires a reviewed revocable delivery/storage authorization contract; it was not implemented or claimed. Physical-device revocation similarly requires provider execution/readback; local authorization ends first, and missing physical providers remain uncertified.

## Section 17: all five authority divisions

The original specification's statement that Payment Service was pending is preserved as historical source text. Current local Core implementation and real Owner/Core tests change dependency status, not the legal/production gate.

| Division | Owner authority | Core authority | Contract/evidence and verdict |
|---|---|---|---|
| A Obligations / financial truth | Why/what is due, schedule, property/business and accepted agreement context | Intent, provider attempt, collection verification, ledger, funding, settlement, canonical status | U/accepted_collection.go and core_business_context.go; typed FinancialCoreClient; F initial/subsequent rent and wallet-adoption stories: LOCAL PASS |
| B Deposits / disputes | Obligation, inspection/claim evidence, challenge and authorized business decision | Actual reserves/held money, release, refund and finality | U/core_financial_workflows/actions/events; F separate deposit and hospitality damage stories: LOCAL PASS. Owner award is not external execution. |
| C Payout preferences | Owned eligible reference selection, consent/property/category preference | Verified beneficiary, availability, approval, provider execution and reconciliation | U/core_wallet_operations.go; current MFA; F original unknown payout readback and failure compensation; Core reference ownership test: LOCAL PASS |
| D Pricing / receipts | Accepted price snapshot, agreement/version, business beneficiary facts | Collection reference, receipt/journal, allocation/history and financial audit | U/core_rent_context.go, accepted_collection.go, core_business_context.go; F once-only initial fee and statement stories: LOCAL PASS |
| E Refund approval / execution | Authorized justification, amount, source customer/transaction and evidence | Reservation, provider submission/status/final outcome/reconciliation/accounting | U/core_financial_actions/workflows; F pending/partial refunds and protected reserve tests: LOCAL PASS |

`PAYMENT_WALLET_SERVICE_URL` remains the actual typed Core integration. `PAYMENT_SERVICE_URL` remains refused by runtime configuration; no legacy adapter was enabled. Reference/readback projections are reconstructable, not accounting authority. No Owner command can set a ledger balance, infer frontend payment success, manufacture funded state, redirect an old issuer, override custody caps or create compensating money to repair a mismatch.

## Section 17: all fourteen financial families

Each row uses owned source context, accepted pricing/consent/beneficiary facts and original resource IDs where the available contract supports execution. A safe refusal is identified separately from full product execution.

| Family | Existing real contract and verification | Local result | Source/provider qualification |
|---|---|---|---|
| 1 Residential initial rent | F initial/subsequent rent; signed accepted agreement and quote/receipt; separate disclosed initial lines | PASS | TEST pricing/legal policy; real rates/templates not approved |
| 2 Subsequent/recurring rent | Same F story and U/rent schedule/readback; no repeated initial fee | PASS | Manual accepted collection; saved-card/bank automatic debit deferred |
| 3 Refundable deposits | F separate deposit/pending partial refund and dispute; segregated protected funds | PASS | Lawful custody, deductions/deadlines and terminal bank refund remain unapproved |
| 4 Short-let stay/extensions | F shortlet collection/cancellation/extension/protected release; accepted nightly/context facts | PASS | Real guest service/consent and PSP outcomes require certification |
| 5 Short-let damage/cancellation | F hospitality evidenced damage and cancellation/refund boundaries | PASS | TEST claim/policy; no fabricated guest consent or provider settlement |
| 6 Sale deposits/milestones | F reservation/balance/closing/release; separate diligence and possession gates | PASS | Source buyer/legal milestone proof and actual registry/title certification required |
| 7 Land occupation and sale | F land lease possession gate; affected real land-sale E2E plus existing Owner land terms | PASS for available contract | Actual title/perfection/consent and lawful land terms not certified |
| 8 Commercial lease/sale | Existing commercial/LOI and source-category/party guards; personal Owner cannot relabel a corporate source | Contract safety PASS; corporate execution SOURCE BLOCKED | Verified corporate party/signatory authority or separately approved source contract is missing |
| 9 Maintenance/artisan | F actual accepted provider entitlement/source completion; scoped signed artisan simulator | PASS for contract | Actual Artisan backend/verified beneficiary absent |
| 10 Professional services | F accepted mandate/deliverable/beneficiary entitlement; signed professional simulator | PASS for contract | Actual professional producer/licensing facts absent |
| 11 Refunds/chargebacks | Core canonical refund/reversal events/readback, pending/partial refund and refund-adjusted reports | PASS for local contract | Terminal real refund/chargeback certification not repeated or supplied |
| 12 Beneficiary payouts | Current destination eligibility, recent MFA, original unknown-outcome recovery and failed-outcome compensation | PASS locally | Last sandbox transfer eligibility was ACCOUNT BLOCKED; not retried. Production approvals remain OPEN |
| 13 Settlement/reconciliation | Core-owned settlement/audit, stale/duplicate event and lease/readback recovery; conservative mismatch review | PASS locally | Real safeguarded cash, treasury liquidity and settlement SLA approval absent |
| 14 Owned statements | Owned wallet/statement boundaries; original issuer retained; deposit/refund-adjusted Owner projections | PASS | No claim of a bank statement, audited accounts or production balance entitlement |

F also freshly passed manager percentage/covered-period fee checks, wallet-funded payment adoption without another charge, and stale/duplicate/expired worker claim recovery. Fixed monthly manager invoices still require an independent period/source invoice. Core accepts the whole locked quote; true partial capture/installments are unsupported. Unattributed mixed-fee reversals remain conservative/manual review.

## Section 18: all eight operational controls

| ID/control | Enforcement/measurement | Tests and safe failure/recovery | Local result |
|---|---|---|---|
| 18.1 API/worker health, ready, version | S/health; Owner app/worker_health.go; process binaries | `TestOwnerAPIHealthOptionalOutageAndShutdown`, `TestWorkerRequiredDependenciesAndRecovery`, existing health suite; E real-process broker/DB outage, liveness/readiness/recovery and version | PASS. Version exposes safe build/service/environment metadata only. Windows process cleanup still uses kill; Unix OS-signal shutdown is not newly certified here. |
| 18.2 Structured safe logging | S/logging, security, HTTP; worker health logger context | Nested-secret/error fixture, privacy span test, actual E process logs | PASS. Safe code/request/trace fields remain; raw error strings are deliberately opaque. Caller code must not place arbitrary secrets in log messages. |
| 18.3 OTEL | Existing Setup/exporters; normalized HTTP/server/client, DB query/transaction, NATS publish/consume, worker and bounded adapter spans | `TestNormalizedHTTPSpansExcludePrivateURLsAndPropagate`, `TestUnavailableExporterDoesNotBlockHTTP`, `TestPublishConsumeContinuesTraceWithoutPayloadAttributes`; E actual Gateway→Owner→Verification/Notification correlation | PASS. NATS consume is a child of its actual publish span. Background publication has its own trace; envelope/request correlation remains available. No external vendor/deployed collector is certified. |
| 18.4 Timeouts / lock boundaries | Existing signed HTTP clients; provider_context.go; durable e-sign/smart/financing intent; pre-call phases | New financing timeout-after-acceptance/no-transaction/deadline test; existing ambiguous e-sign/smart and financial readback stories | PASS. No distributed transaction. Original references retained; unresolved financing requires manual partner readback, not a fresh command. |
| 18.5 Leases / bounded recovery | Existing SKIP LOCKED outbox/dispatch/import/financial leases and lifecycle CAS; 20-attempt registration ceiling; existing loops | Shared outbox races/reclaim/dead-letter; Owner dispatch/import/access tests; `TestWalletRegistrationRetriesEndInReview`; F expired fenced claim recovery | PASS for available workers. Periodic reconciliations keep discovering current facts; poison commands/events enter bounded review. Missing providers cannot be physically certified. |
| 18.6 Hard / optional dependencies | Role-specific readiness, configured required consumers, future optional producer availability | Worker/API dependency and E outage/recovery tests; unsupported-provider fail-fast configuration tests | PASS. Optional integrations degrade only their features; absent future consumers are not called synchronized. |
| 18.7 Poison/dead-letter/manual review | Existing retry/Term semantics plus migration 23 reference sink and audited Admin decision | `TestTerminalJetStreamFailuresPersistBeforeTermination`: malformed, permanent and retry-exhausted records; review authorization/mutation/rollback negatives | PASS. If the sink is unavailable, no Term is issued; original stream reference and failure metric remain. At broker MaxDeliver, operator replay may be required after DB recovery. Review approval never executes money or replay. |
| 18.8 Reconciliation / lag | Existing admin reconciliation and authoritative snapshot; real consumer Info/availability/pending where running | Reconciliation classification tests; `TestOperationalSnapshotAndEventReviewRetention`, `TestOperationalMetricsUnknownIsNotZero`; F missing/stale/duplicate financial observation recovery | PASS within available sources. Unavailable external projections remain UNKNOWN/UNAVAILABLE; no guessed Marketplace/Manager delivery lag or automatic money correction. |

### Explicit dependency/readiness matrix

| Dependency | API | Worker | Failure meaning |
|---|---|---|---|
| Owner PostgreSQL/current schema (migration >=23) | HARD | HARD | `/ready` 503; liveness remains independent; recover connection/schema before work |
| NATS/account and owned outbox stream | Asynchronous/recoverable | HARD | Durable local outbox retained; worker `/ready` 503 on broker/stream loss |
| Configured Verification/Media/Payment consumers | Feature/request dependency | HARD when corresponding URL is configured | Startup/running consumer and broker resource must exist; absent configured consumer fails worker readiness |
| Payment HTTP | Wallet/collection feature dependency | Recoverable asynchronous/readback | No fake provision/payment; original IDs remain; retry ceiling/manual review where applicable |
| Notification HTTP | Security-code request dependency | Recoverable queued dispatch | Code request reports failure; original alert request retries/dead-letters |
| Redis | Gateway rate/ephemeral dependency | Not an Owner hard dependency | Gateway owns its health/limits; Owner does not introduce a Redis persistence dependency |
| Future Marketplace/Manager/Artisan/Professional | Deferred producers/contracts | Optional consumers until an actual required producer is configured | `owner_consumer_available=0`; no pending-count sample advertised as zero lag |
| E-sign/smart/calendar/partners/recipient service | Optional feature | Recoverable or unavailable feature | Runtime refuses unsupported provider configuration; no physical/external success invented |
| CF Meet/maps | Deferred/optional | Not hard | Existing context/manual coordinates only; feature cannot claim absent provider execution |
| OTLP collector | Optional | Optional | Request/work correctness survives exporter failure; shutdown is bounded |

### All six metric families

Metrics use existing OTEL export conventions. Route, method, status, fixed action/code, fixed consumer/loop, and schema-bounded family/kind/state labels contain no raw user/property/payment IDs, emails, bank values or document URLs. `owner_operational_observation_available=0` emits no substitute all-zero business snapshot. These observations are counts/durations, not financial balances.

| Family | Actual instruments/sources | Verification/qualification |
|---|---|---|
| HTTP | `http_requests_total`, `http_request_duration_seconds`: completed requests/normalized route/method/status; 401/403 and 429; `external_http_failures_total` transport/timeout/cancel classification | Shared HTTP/OTEL/privacy tests and real E requests; no raw route identifiers |
| DB | `db_query_duration_seconds`, `db_operation_failures_total`, `db_transaction_retries_total`; `owner_db_connections` acquired/idle/maximum; visible current-DB lock waiters | Real PG, serialization/deadlock/bounded-lock shared tests; Snapshot/ManualReader tests. PG activity visibility is that of the configured role. No SQL/parameters exported. |
| Events | `outbox_events_published_total`, `outbox_publish_failures_total`, `event_publish_failures_total`, `event_duplicates_total`, terminal/review persistence failures; durable backlog/oldest age; actual `owner_consumer_available` / `owner_consumer_pending` | Shared actual JetStream/outbox tests, poison retention, F duplicate/readback recovery. Broker pending/ack-pending is not proof of a missing downstream business projection. |
| Workers | `owner_worker_cycles_total`, failures and duration; outbox claim duration/expired lease recoveries; persisted attempts, expired lease count/overdue age, registration exhaustion, external unknown/review count/age | Real DB/NATS concurrent claim, lease recovery, registration ceiling and ambiguous provider tests; historical state gauges are not fabricated monotonic execution counters |
| Business | Published/stale-paused listings, tenancy state/occupancy, rent obligation state/overdue, verification/ownership, application decision mean, calendar conflict audit totals, booking-conflict counter, stale Core readback and confirmed booking/payment contradiction counts | Owner 150 and selected E/F scenarios; no invented earnings or safeguarded-fund availability |
| Security | `owner_authz_denials_total` action/code; observed MFA/login/new-device/lockout/step-up security records; denied audit count; authority/event/manual review | MFA/authz/session/recovery/ownership/review negatives. Operational thresholds and incident-routing approval remain pending. |

### Alert definitions and safe runbooks

The following are implementable expressions over actual readiness/instruments/records, not deployed or approved production alert rules. Numerical thresholds/window durations and routing are **OPERATIONAL APPROVAL PENDING**. Operators must configure them in their existing collector/monitor; no vendor was deployed.

| Condition / signal | Suggested severity / owner | Triage and permitted recovery | Escalation |
|---|---|---|---|
| API `/health` unavailable or sustained `/ready` 503 | Critical / platform + Owner | Check process, schema and DB; restore required dependency; verify readiness before traffic | Failed migration/credentials or persistent startup failure to platform; do not bypass checks |
| Worker `/ready` 503 | Critical / Owner/platform | Check broker/account/stream and configured consumer initialization, DB and shutdown context | Required producer missing to its owner; optional future producer remains unavailable |
| Pool acquired/max saturation or lock waiters above approved threshold | High / DB operator | Identify service-visible blockers, statement/lock timeout and retry metrics; restore capacity | Persistent deadlocks/query regression to engineering; do not disable concurrency safety |
| Growing pending outbox / oldest unpublished age | High / Owner/events | Check publisher failures/broker, original rows, expired lease and bounded attempts | Dead rows to referenced manual review; reuse event ID, never a new financial command |
| Retry exhaustion / terminal event / review sink persistence failure | High / Owner/source producer | Preserve stream/sequence/digest; inspect original producer record and approved schema/version; restore DB first | At MaxDeliver replay explicitly from original retained stream/source after attributable review |
| Booking/payment contradiction or other Core reconciliation mismatch | Critical / finance + Owner/Core | Read original owned intent/resource from Core; compare accepted source facts and event watermark | Material conflict to authorized finance review; no invented ledger compensation |
| Unknown external operation exceeds approved age / manual review backlog | High / provider integration | Read original key/fingerprint/reference; use supported readback; inspect fenced lease | Unresolved e-sign/device/financing result to provider/operator; do not resubmit a fresh operation |
| Verification readback overdue/failing or authority expiry mismatch | High / Owner/Verification | Check original linked case/subject/type; retry bounded read; ensure new sensitive action is blocked when authority lost | Material mismatch to verification admin; never manufacture VERIFIED |
| Elevated authorization/MFA/login/payout/document/manager denials | High / security + Owner | Inspect safe action/code, request/trace, live session/mandate and immutable security records | Security review under approved policy; no automatic unapproved final fraud verdict |
| Required significant audit/review decision missing | Critical / security/DB | Check transaction rollback, append-only triggers, audit writer and authorized actor/source context | Security incident; do not repair by silently editing protected history |
| Persistent critical worker failure/expired lease backlog | High / Owner/platform | Check job name, duration/failure, original lease, safe retry class and resource state | Engineering/manual review for poison records; no parallel unbounded retry loop |
| Consumer unavailable / pending or calendar conflict above approved window | High / source integration | Distinguish absent producer from measurable pending; check current consent/availability before import/replay | Actual Marketplace/Manager/calendar owner required; never label unavailable as synchronized |

The original broker stream retains payloads under its configured retention (default seven days in the shared stream helper). Review evidence in Owner is durable metadata. Operators must recover/archive the original source through approved producer/retention procedures before broker retention expires; this report does not claim an indefinitely retained raw-payload archive or approve a legal retention period.

## All twenty cross-cutting certification scenarios

| Scenario | Evidence / outcome |
|---|---|
| 1 Payout change / current MFA | New sensitive-session test + banking tests + F payout: PASS |
| 2 Revoked Owner session / financial resource | New sensitive-session test, logout/refresh families, E account journey: PASS |
| 3 Revoked manager / beneficiary diversion | Authorization/delegation and F exact manager fee/owned payout checks: PASS |
| 4 Private documents / events and logs | Data-room/evidence/minimization, nested redaction and HTTP/NATS span tests: PASS |
| 5 Financial timeout / no second collection | F original intent/wallet adoption/unknown readback: PASS with TEST Core providers |
| 6 Projection mismatch / no new ledger entry | Existing classified reconciliation plus snapshot and F readback: PASS |
| 7 Verification expiry / transaction blocking | Subject binding/lost expiry and authority/readiness gates: PASS |
| 8 Outbox publish crash / recovery | Shared reclaim/de-duplication and Owner real JetStream relay recovery: PASS |
| 9 Duplicate NATS / once-only mutation | `TestEventConsumersApplyOnceThroughUseCases`, F stale/duplicate/fenced recovery: PASS |
| 10 E-sign/smart original unknown operation | Existing timeout-after-acceptance tests and retained fencing: PASS for neutral contracts |
| 11 Expired worker lease / no duplicate | Shared races, Owner access/import/dispatch and F lease recovery: PASS |
| 12 Optional outage / service readiness | Worker/API optional dependency test and E API broker independence: PASS |
| 13 Required DB outage / readiness failure | E actual DB connection disable/terminate/restore; worker pool exhaustion/recovery: PASS |
| 14 Poison reaches bounded review | Actual JetStream malformed/permanent/exhausted + reference sink/decision tests: PASS |
| 15 Unauthorized support/admin evidence access | Existing assistance, dispute/evidence/source-domain and admin-scope tests: PASS |
| 16 Closure / retained legal-financial refs | Closure/guest legal hold and immutable financial/history tests: PASS |
| 17 JWT/MFA/refresh absent from logs/traces | Nested fixture, HTTP/NATS attribute tests, actual E token/password log checks: PASS |
| 18 Failed/pending/unavailable metrics | Snapshot/reference tests, manual reader unknown!=zero, actual consumer availability source: PASS |
| 19 Statements exclude protected/refunded income | `TestFinancialReportsExcludeRefundedRent`, `TestFinancialReportsExcludeProtectedStayDeposits`, F owned statements: PASS |
| 20 Provider/regulatory flag bypass | Core `TestCapabilityEvidenceCannotActivateProduction`, locked collection policy revocation and unsupported provider config: PASS |

## Executed certification and preserved corrections

All commands use the existing ignored environment helper `tmp/payments/owner-14-15-env.ps1`: local PostgreSQL 18 at 53613, Redis 8.8 at 63702, `CF_REQUIRE_INTEGRATION=1`, CGO and the existing race-capable toolchain. No actual tests were skipped. PostgreSQL databases were ephemeral and distinct per service/test; JetStream was a real embedded server. Process tests executed actual race-built service binaries, real gateway/Redis/Verification/Media/Notification APIs/workers and a local SMTP recipient sink. S3 storage, KYC and financial provider/approval/source fixtures remain explicitly simulated.

| Command / evidence file under tmp/payments | Actual result |
|---|---|
| `go test -race -count=1 -p 2 -json ./services/individual-owner-service/... ./gateway/api-gateway/... ./shared/go/...` / owner-16-18-consolidated.jsonl | **Original run EXIT 1:** Owner 149 PASS + 1 FAIL; Gateway 22 PASS; Shared 138 PASS. It is not relabelled PASS. |
| `go test -race -count=1 -json ./services/individual-owner-service/tests/integration -run '^TestPhoneVerificationAndOTPLogin$\|^TestSensitiveActionsRequireCurrentSessionMFA$'` / owner-16-18-phone-correction.jsonl | 2/2 PASS after correction |
| `go test -race -count=1 -p 2 -json ./services/individual-owner-service/...` / owner-16-18-final-owner-certification.jsonl | **Final complete Owner rerun: 150/150 PASS**, exit 0 |
| `go test -race -count=1 -json ./services/payment-service/tests/integration -run '^TestActualOwnerFinancialCoreConsumer$\|^TestCapabilityEvidenceCannotActivateProduction$\|^TestLockedCollectionPolicyRevocationDoesNotResubmit$\|^TestVerifiedPayoutReferenceReadEligibility$'` / owner-16-18-financial-regression.jsonl | 4/4 PASS; actual tagged Owner child and all ten F stories PASS |
| `go test -race -count=1 -json ./tests/e2e -run '^(TestOwnerRealProcessTracingAndReadinessRecovery\|TestOwnerAccountJourneyThroughGateway\|TestOwnerFunctionalJourneyWithRealServices)$/(14_3_\|14_4_)'` / owner-16-18-e2e-certification.jsonl | 3 top-level + 2 affected journey subtests PASS; not described as a fresh rerun of all five original journeys |
| `go test -race -count=1 -json ./shared/go/events/jetstream -run '^TestPublishConsumeContinuesTraceWithoutPayloadAttributes$'` / owner-16-18-nats-tracing.jsonl | Additional 1/1 PASS after adding verification only; Shared effective current coverage 139 tests |

The one consolidated defect was `TestPhoneVerificationAndOTPLogin`: storing PHONE_OTP in the constrained second-factor method column returned 500. The corrected session leaves its second-factor method unset until a permitted factor is verified; first-factor identity is retained separately. The full final Owner rerun passed. Gateway/Shared/F/E software paths exercised in their earlier current-task runs did not change as a result of this phone-only representation correction. The additional NATS test changed verification, not production code.

Development failures are preserved in the ignored logs: old password-only fixtures after MFA enforcement; test expectation corrections for accepted code-delivery status; delayed post-commit physical-revocation assertion; legacy-hash fixture initially matching the cheap harness hasher; expected partner unavailable classification; and uppercase TEST email versus normalized SMTP recipient. These are not hidden PASS runs. Contract generation initially used the wrong default root and failed before writing; rerun with `-root .` succeeded.

Fresh gates: Owner/Gateway/Shared/E vet, staticcheck and build PASS; tagged Owner financial-cross-service vet/staticcheck PASS; corrected final Owner vet/staticcheck/build PASS; architecture self-test/boundaries PASS; generated contract `-root . -check` PASS; event/OpenAPI/Postman schema/route parity PASS; SQLC PASS with the one expected model change; clean migration roundtrip and populated history compatibility/rollback refusals PASS; final diff and staged secret review PASS before commit. Raw hashes and exact counts are recorded below and in `owner-16-18-certification-summary.json`.

## External decisions and certification still outstanding

| Payment decision | Status / responsible approval |
|---|---|
| O-001 Commission by product/payer/jurisdiction | OPEN — pricing/finance |
| O-002 PSP fees, tax/withholding and failed-transfer charges | OPEN — finance/legal |
| O-003 Rent release/handover milestone | OPEN — legal/product |
| O-004 State agreement/deposit/notice/advance-rent rules | OPEN — counsel |
| O-005 Category refund/no-show/cancellation/release/escalation deadlines | OPEN — product/legal |
| O-006 Lawful custody/escrow and written provider approval | OPEN — banking/legal/compliance/provider |
| O-007 Payout thresholds/dual approval/reserves/co-owner/provider limits | OPEN — finance/risk |
| O-008 DVA provisioning, withdrawal permission/capacity/consent | OPEN — payments/compliance |
| O-009 Cross-border/diaspora PSP permission | OPEN — compliance/providers |
| O-010 Real Estate/future CosmicForge service boundary | OPEN — architecture |
| O-011 Accepted fee/legal invoice trigger for new versus recurring agreements | OPEN — legal/product |
| O-012 Settlement SLA and liquidity reservations | OPEN — treasury/providers |

No new Paystack calls were made. Last retained sandbox evidence covers hosted collection/verify/once-only receipt, refund submission/pending readback and recipient registration within its recorded scope. Terminal refund, public webhook delivery, DVA funding/assignment and real transfer execution are not newly certified; prior merchant transfer/DVA eligibility blockers were not retried. Balance/settlement visibility is not safeguarding approval. Saved bank/card automatic debit remains deferred; live custody/cash-out/escrow is not enabled.

Actual Marketplace/Manager/Artisan/Professional/Admin producers/console, verified corporate/legal-party/bank facts, tenant/guest recipient delivery, CF Meet/maps, licensed financing/insurance, certified e-sign/device/calendar providers and lawful templates/retention policies remain as specified in the prior closure reports. Local contract fixtures cannot supply these facts. Operational thresholds, protected telemetry/audit infrastructure, distinct runtime/migration roles, producer-scoped NATS ACLs, external penetration/security review and deployed recovery drills require production review. Existing issued-URL lifetime and post-commit physical revocation qualifications remain explicit.

At the starting SHA, Actions runs `38343937250` (Backend), `38343937179` (Architecture), `38343937235` (Images), `38343937181` (Security) and `38343937227` (Contracts) failed without executing jobs; Backend had 18 jobs and zero steps. Owner check `115120912294` stated account payments/spending-limit restrictions. This is historical infrastructure evidence, not final-commit CI. Final push/run IDs and job-step evidence are exported after push; no repeated manual workflow dispatch or billing modification is authorized by this task.

Final independent verdicts: **Owner Sections 5–18 LOCAL IMPLEMENTATION CLOSED within the available backend/contract scope; Payment Core regression PASS; CI last observed BLOCKED - INFRASTRUCTURE; exact final-commit state is exported after push; staging NOT CERTIFIED; production BLOCKED.** External/source-blocked commercial execution and future providers are not included in that local closure as successful products. No Section 19+ work, deployment or live financial activation was performed.

## Raw certification hashes

| Evidence | SHA-256 |
|---|---|
| `owner-16-18-consolidated.jsonl` | `408e495827a2c56ca800ee9483088ed15620bbf0cc781d4cdc8246e37995b1ae` |
| `owner-16-18-final-owner-certification.jsonl` | `f0ddf37b7b33750567319fa423c11c29011eccf614d22e039304160986b4bcb6` |
| `owner-16-18-phone-correction.jsonl` | `9a2165899a105ca70f9e978d7bd827edda1b26fe95552248b0f81119ab1f972a` |
| `owner-16-18-financial-regression.jsonl` | `060655de7d4a68f0ecf118a911314b1f245feb28f606f8b4efc6a9de0d1395dc` |
| `owner-16-18-e2e-certification.jsonl` | `c8de365a5319f713edd1eba6fab4bf6dd83b56ab926039acdfa024a3414721d5` |
| `owner-16-18-nats-tracing.jsonl` | `8246ca0372b955cd8af5fb940faee5dd020ffaad77ae8a8021e72a21217a9430` |

## Exact certification commands

Run from `C:/Projects/cosmicforge-realestate` after dot-sourcing the retained environment helper. Escaped table separators above are presentation only; these are the exact regex arguments.

```powershell
. ./tmp/payments/owner-14-15-env.ps1
go test -race -count=1 -p 2 -json ./services/individual-owner-service/... ./gateway/api-gateway/... ./shared/go/...
go test -race -count=1 -json ./services/individual-owner-service/tests/integration -run '^TestPhoneVerificationAndOTPLogin$|^TestSensitiveActionsRequireCurrentSessionMFA$'
go test -race -count=1 -p 2 -json ./services/individual-owner-service/...
go test -race -count=1 -json ./services/payment-service/tests/integration -run '^TestActualOwnerFinancialCoreConsumer$|^TestCapabilityEvidenceCannotActivateProduction$|^TestLockedCollectionPolicyRevocationDoesNotResubmit$|^TestVerifiedPayoutReferenceReadEligibility$'
go test -race -count=1 -json ./tests/e2e -run '^(TestOwnerRealProcessTracingAndReadinessRecovery|TestOwnerAccountJourneyThroughGateway|TestOwnerFunctionalJourneyWithRealServices)$/(14_3_|14_4_)'
go test -race -count=1 -json ./shared/go/events/jetstream -run '^TestPublishConsumeContinuesTraceWithoutPayloadAttributes$'
go vet ./services/individual-owner-service/... ./gateway/api-gateway/... ./shared/go/... ./tests/e2e/...
staticcheck ./services/individual-owner-service/... ./gateway/api-gateway/... ./shared/go/... ./tests/e2e/...
go build ./services/individual-owner-service/... ./gateway/api-gateway/... ./shared/go/... ./tests/e2e/...
go vet -tags financial_cross_service ./services/individual-owner-service/tests/integration
staticcheck -tags financial_cross_service ./services/individual-owner-service/tests/integration
go run ./services/individual-owner-service/cmd/apidocgen -root . -check
sqlc generate -f services/individual-owner-service/sql/sqlc.yaml
bash scripts/ci/check-architecture.sh --self-test
bash scripts/ci/check-architecture.sh
python scripts/payments/check_secrets.py
git diff --check
```

Migration verification includes `TestMigrationsRoundTrip`, restricted-role extension preflight and the populated review/operation/history tests in the final Owner suite. SQLC output was hash-compared before/after. Corrected Owner vet/staticcheck/build and contract check were repeated after the phone fix; all exited zero. The final NATS verification-only test received a separate vet/staticcheck check. No new external sandbox/certification command was executed.

## Reviewed file inventory

66 scoped files before index normalization, including this report and coverage. No Payment application/ledger/migration file or unrelated service was modified.

- `contracts/internal/openapi/individual-owner-service-domain.yaml`
- `docs/product/individual-owner/IMPLEMENTATION_COVERAGE.md`
- `docs/product/individual-owner/SECTIONS_16_18_IMPLEMENTATION_CLOSURE.md`
- `postman/CosmicForge_RealEstate.postman_collection.json`
- `services/individual-owner-service/cmd/api/main.go`
- `services/individual-owner-service/cmd/worker/main.go`
- `services/individual-owner-service/go.mod`
- `services/individual-owner-service/internal/app/app.go`
- `services/individual-owner-service/internal/app/worker_health.go`
- `services/individual-owner-service/internal/authz/authz.go`
- `services/individual-owner-service/internal/authz/authz_test.go`
- `services/individual-owner-service/internal/authz/property_rules.go`
- `services/individual-owner-service/internal/config/config.go`
- `services/individual-owner-service/internal/config/config_test.go`
- `services/individual-owner-service/internal/http/api.go`
- `services/individual-owner-service/internal/http/routes_closure.go`
- `services/individual-owner-service/internal/http/routes_partners.go`
- `services/individual-owner-service/internal/repositories/db/models.go`
- `services/individual-owner-service/internal/telemetry/metrics.go`
- `services/individual-owner-service/internal/usecases/access.go`
- `services/individual-owner-service/internal/usecases/access_provider.go`
- `services/individual-owner-service/internal/usecases/agreements.go`
- `services/individual-owner-service/internal/usecases/announcements.go`
- `services/individual-owner-service/internal/usecases/esign_recovery.go`
- `services/individual-owner-service/internal/usecases/event_review.go`
- `services/individual-owner-service/internal/usecases/login.go`
- `services/individual-owner-service/internal/usecases/ownership.go`
- `services/individual-owner-service/internal/usecases/partners.go`
- `services/individual-owner-service/internal/usecases/provider_context.go`
- `services/individual-owner-service/internal/usecases/reconciliation.go`
- `services/individual-owner-service/internal/usecases/sale.go`
- `services/individual-owner-service/internal/usecases/shortlet.go`
- `services/individual-owner-service/internal/usecases/wallet_registration.go`
- `services/individual-owner-service/internal/workers/workers.go`
- `services/individual-owner-service/migrations/00023_event_review.sql`
- `services/individual-owner-service/tests/integration/core_workflow_cross_test.go`
- `services/individual-owner-service/tests/integration/harness_test.go`
- `services/individual-owner-service/tests/integration/partners_test.go`
- `services/individual-owner-service/tests/integration/property2_test.go`
- `services/individual-owner-service/tests/integration/property_test.go`
- `services/individual-owner-service/tests/integration/sale_test.go`
- `services/individual-owner-service/tests/integration/sale_transfer_test.go`
- `services/individual-owner-service/tests/integration/security_operability_test.go`
- `services/individual-owner-service/tests/integration/shortlet_test.go`
- `shared/go/database/database.go`
- `shared/go/database/telemetry.go`
- `shared/go/events/events.go`
- `shared/go/events/jetstream/jetstream.go`
- `shared/go/events/jetstream/trace_test.go`
- `shared/go/go.mod`
- `shared/go/go.sum`
- `shared/go/http/http_test.go`
- `shared/go/http/middleware.go`
- `shared/go/http/response.go`
- `shared/go/logging/logging.go`
- `shared/go/logging/logging_test.go`
- `shared/go/observability/observability.go`
- `shared/go/observability/privacy_test.go`
- `shared/go/outbox/outbox.go`
- `shared/go/security/security.go`
- `tests/e2e/go.mod`
- `tests/e2e/go.sum`
- `tests/e2e/owner_mfa_fixture_test.go`
- `tests/e2e/owner_operability_test.go`
- `tests/e2e/owner_primary_journeys_test.go`
- `tests/e2e/owner_test.go`

## Final staged confidentiality review

`check_secrets.py` checked 1,308 tracked/untracked/evidence files and the staged diff: supplied-key matches NONE, secret Authorization literals NONE, provider credential literals NONE, index-key matches NONE. The ignored real `.env` remains untracked and preserved. The 66-file staged inventory contains no env/credential/key/database/dump/backup file, private-key block, GitHub token literal or credential-bearing URL. `git diff --cached --check` passed. These scans do not claim an independent penetration test or deployed security certification.
