# CosmicForge Payment Service — Sections 4–6 closure report

Authority: user-supplied Sections 4–6 implementation prompt and retained master
blueprint v1.0 PDF, SHA-256
fd1cfd14fb2f78ed7601fbafc49113f7d03049d3ef8041f6f08f0342d964e7f1.
This implements the user's bounded request. Later chapters of the document do
not authorize later workflows or live money activation.

This tracked report is finalized after committing with
scripts/payments/finalize_report.py --sections-4-6 into
tmp/payments/SECTIONS_4_6_CLOSURE_REPORT.md. The export records the exact SHA
and clean-tree result; a commit cannot contain its own hash.

## A. Repository information

- Starting branch: codex/payment-sections-1-3.
- Starting SHA: 789411c220bcccdb5301b90ff7d0e28e61b77353; clean tree, remote fetched.
- Final branch: {{FINAL_BRANCH}}.
- Final SHA: {{FINAL_SHA}}.
- Working tree: {{FINAL_STATUS}}.
- Commit: {{FINAL_COMMIT}}.
- Push: no git push performed; the branch has not been published by this task.
- Original Sections 1–3 certification remains retained in CLOSURE_REPORT.md,
  TEST_EVIDENCE.md and their original saved logs.

## B. Section 4 closure

All paths below are relative to services/payment-service unless prefixed otherwise.
All workflow journals use the existing internal/core and finance service, never
a second ledger. The architecture gate preserves sole payment_db ownership.

| Requirement | Implementation, API and durable fact | Test evidence | Verdict / boundary |
|---|---|---|---|
| Accepted quote/canonical intent | lifecycle/allocation.go; finance/service.go; locked-quotes, payment-intents; immutable FK and unique quote intent in migration 00005 | Lifecycle allocation/context/security tests; custody flag bypass rejected | LOCAL PASS; source facts must be authoritative |
| Exact allocation and ownership | lifecycle/allocation.go; beneficiary-authorities, allocations; finance.allocation_created | Immutable/correction/concurrent allocation, twelve category flows, verified owner/co-owner and manager entitlements | LOCAL PASS; no unexplained residual, fractional minor units rejected |
| Immutable correction | Append superseding snapshot with compensating original journal; accepted rows and line terms sealed | LifecycleAllocationImmutabilityAndCorrection, direct mutation rejected | LOCAL PASS; released/held corrections require later review workflow |
| Protection, deposits and reserves | Per-allocation PROTECTED accounts; DEPOSIT, RESERVE, settlement-control and processing classifications; engine/DB nonnegative controls | DepositSegregationAndPayerReceiptPrivacy; category and hold tests | LOCAL PASS; deposit ordinary release/withdrawal rejected |
| Funding and evidence | lifecycle/release.go; transaction-funding and release-evidence; actual provider settlement membership, gross/fee totals, cash attestation | Funding mismatches, missing evidence, unfunded release and custody gate tests | LOCAL PASS; merchant balance is not lawful custody proof |
| Independent hold and partial release | lifecycle/hold.go and release.go; allocation-lines hold and allocation-releases; finance.allocation_released | Independent manager hold; sixteen contending releases allow ten capped successes; releases across payout phases | LOCAL PASS; custody-required products cannot activate without approval |
| Available-only withdrawal | lifecycle/payout.go; verified bank/recipient binding, exact single-use approval, withdrawals; finance.withdrawal_reserved | Concurrent withdrawal, pending/protected/deposit and fee-coverage rejection | LOCAL PASS; signed beneficiary domain and current KYC required |
| One external attempt | Durable CAS RESERVED to SUBMITTING before provider call, immutable original route/ref; finance.payout_initiated/resolution_required | Unknown outcome repeated commands, readback and callback race: one submission/terminal journal | LOCAL PASS; unknown funds stay reserved |
| Terminal payout and safe compensation | Original authenticated readback validates reference, amount, currency, recipient, merchant, mode, fee/no-effect; finance.payout_confirmed/failed | Paid callback/readback race, no-effect failure, excessive/unknown fee, held and revoked-verification failure | LOCAL PASS; no blind failover or resubmission |
| Financial durability/security | Balanced journals, deferred checks, immutable history, transactional audit/outbox and recovery worker | Real PostgreSQL races, immutable DB guards, signed HTTP, event schema and JetStream tests | LOCAL PASS; source builds/local integration only |

Additional qualified boundaries: provider fee approval/cap must be independently
funded; an uncertain fee retains the reservation. Large settlement batches exceeding
one 100-record membership page fail exact matching rather than fabricate coverage.
OTP completion, post-terminal reversal remedies and full withdrawal onboarding
remain future integrations. No live custody or transfer activation is enabled.

## C. Section 5 closure

All eighteen families are separate disabled templates in lifecycle/policy.go.
Immutable approved packs support exact jurisdiction/currency/contract/product
and effective windows, later evidenced deactivation, cancellation/refund/dispute
and adjustment references, required human review, commission and withdrawal
configuration. Templates contain UNCONFIGURED legal policy references and no
statutory timeline or production tariff. Activation requires explicit Admin approval.

| Policy key / payer | Components | Beneficiary roles | Evidence conditions | Protection / jurisdiction and policy refs | Tests |
|---|---|---|---|---|---|
| RESIDENTIAL_INITIAL / TENANT | RENT, LEGAL, ONBOARDING, PLATFORM, DEPOSIT, SERVICE, UTILITY, TAX, PROCESSING | OWNER, CO_OWNER, PROFESSIONAL, PROFESSIONAL_FIRM, MANAGER, MANAGEMENT_COMPANY, PLATFORM | SIGNED_AGREEMENT, ACCEPTED_QUOTE, POSSESSION | Protected; segregated deposit/reserve classifications; reviewed jurisdiction pack and C/R/D references required | All18 + rent/deposit/continuity tests |
| RESIDENTIAL_SUBSEQUENT / TENANT | RENT, SERVICE, UTILITY, TAX, PROCESSING | OWNER, CO_OWNER, MANAGER, MANAGEMENT_COMPANY, PLATFORM | VALID_TENANCY, RENT_DUE | Protected / pending platform revenue; reviewed jurisdiction pack and C/R/D references required | All18 + rent/deposit/continuity tests |
| TENANCY_RENEWAL / TENANT | RENT, SERVICE, UTILITY, TAX, PROCESSING | OWNER, CO_OWNER, MANAGER, MANAGEMENT_COMPANY, PLATFORM | VALID_TENANCY, APPROVED_CHANGE | Protected / pending platform revenue; reviewed jurisdiction pack and C/R/D references required | All18 + rent/deposit/continuity tests |
| RESIDENTIAL_DEPOSIT / TENANT | DEPOSIT | CUSTOMER | MOVE_OUT, APPROVED_CLAIM, DISPUTE_DECISION | Protected; segregated deposit/reserve classifications; reviewed jurisdiction pack and C/R/D references required | All18 + rent/deposit/continuity tests |
| COMMERCIAL_LEASE / LESSEE, CORPORATE_LESSEE | RENT, FIT_OUT, SERVICE, DEPOSIT, TAX, PROCESSING | CORPORATE_OWNER, MANAGER, MANAGEMENT_COMPANY, SERVICE_COMPANY, PLATFORM | EXECUTED_LEASE, COMMENCEMENT, CORPORATE_SIGNATORY | Protected; segregated deposit/reserve classifications; reviewed jurisdiction pack and C/R/D references required | All18 + representative flow |
| LAND_LEASE / LESSEE | RESERVATION, OCCUPATION, INSTALLMENT, DEPOSIT, PROFESSIONAL, PROCESSING | OWNER, CO_OWNER, CORPORATE_OWNER, PROFESSIONAL, PLATFORM | LAND_AUTHORITY, POSSESSION, MILESTONE | Protected; segregated deposit/reserve classifications; reviewed jurisdiction pack and C/R/D references required | All18 + representative flow |
| PROPERTY_PURCHASE / BUYER | RESERVATION, PURCHASE_DEPOSIT, INSTALLMENT, SALE, PROFESSIONAL, REGISTRATION, TAX, PROCESSING | OWNER, CO_OWNER, CORPORATE_OWNER, PROFESSIONAL, PROFESSIONAL_FIRM, SERVICE_COMPANY, PLATFORM | TITLE, DUE_DILIGENCE, CLOSING, HANDOVER, REGISTRATION | Protected; segregated deposit/reserve classifications; reviewed jurisdiction pack and C/R/D references required | All18 + representative flow |
| LAND_PURCHASE / BUYER | RESERVATION, SALE, SURVEY, TITLE, CLOSING, PROCESSING | OWNER, CO_OWNER, CORPORATE_OWNER, PROFESSIONAL, PROFESSIONAL_FIRM, PLATFORM | SURVEY, TITLE, ENCUMBRANCE, POSSESSION, CLOSING | Protected / pending platform revenue; reviewed jurisdiction pack and C/R/D references required | All18 + representative flow |
| OFF_PLAN / BUYER | RESERVATION, CONSTRUCTION, HANDOVER, RESERVE, PROFESSIONAL, PROCESSING | DEVELOPER, SERVICE_COMPANY, PROFESSIONAL, PROFESSIONAL_FIRM, PLATFORM | INDEPENDENT_CERTIFICATION, PROJECT_ALLOCATION, SNAGGING | Protected; segregated deposit/reserve classifications; reviewed jurisdiction pack and C/R/D references required | All18 + independent certificate/custody gate |
| SHORT_LET / GUEST | ACCOMMODATION, CLEANING, BOOKING, DEPOSIT, ADD_ON, PROCESSING | OWNER, HOSPITALITY, MANAGER, MANAGEMENT_COMPANY, ARTISAN, SERVICE_COMPANY, PLATFORM | CHECK_IN, STAY_COMPLETE, CHECK_OUT | Protected; segregated deposit/reserve classifications; reviewed jurisdiction pack and C/R/D references required | All18 + representative flow |
| HOTEL / GUEST, CORPORATE_BOOKER | ROOM, BOARD, SERVICE, EXTRA, TAX, DEPOSIT, PENALTY, PROCESSING | HOSPITALITY, SERVICE_COMPANY, PLATFORM | RATE_PLAN, FOLIO, CHECK_IN, CHECK_OUT, EXTRAS, NO_SHOW | Protected; segregated deposit/reserve classifications; reviewed jurisdiction pack and C/R/D references required | All18 + representative flow |
| EVENT_VENUE / EVENT_CUSTOMER | RESERVATION, EVENT, DEPOSIT, CLEANING_RESERVE, PROCESSING | OWNER, HOSPITALITY, SERVICE_COMPANY, PLATFORM | EVENT_COMPLETE, CLEANUP, DAMAGE_ASSESSMENT | Protected; segregated deposit/reserve classifications; reviewed jurisdiction pack and C/R/D references required | All18 + representative flow |
| MAINTENANCE / TENANT, OWNER, BUSINESS | CALL_OUT, MATERIALS, LABOUR, MILESTONE, EXTRA, PROCESSING | ARTISAN, SERVICE_COMPANY, PLATFORM | QUOTE, ACCESS, ATTENDANCE, COMPLETION, ACCEPTANCE, WARRANTY | Protected / pending platform revenue; reviewed jurisdiction pack and C/R/D references required | All18 + representative flow |
| PROFESSIONAL_SERVICE / BUYER, OWNER, BUSINESS | RETAINER, APPOINTMENT, MILESTONE, REPORT, VISIT, EXPENSE, PROCESSING | PROFESSIONAL, PROFESSIONAL_FIRM, PLATFORM | MANDATE, CONFLICT_CHECK, DELIVERABLE, ACCEPTANCE | Protected / pending platform revenue; reviewed jurisdiction pack and C/R/D references required | All18 + representative flow |
| MANAGER_FEE / OWNER, BUSINESS | MANAGER_FEE, INSPECTION, BOOKING, SERVICE, PROCESSING | MANAGER, MANAGEMENT_COMPANY, PLATFORM | MANDATE, FEE_AGREEMENT, SERVICE_PERIOD | Protected / pending platform revenue; reviewed jurisdiction pack and C/R/D references required | All18 + representative flow |
| UTILITIES / TENANT, OWNER, BUSINESS | UTILITY, SERVICE, SHARED_METER, BUDGET, PROCESSING | SERVICE_COMPANY, HOSPITALITY, PLATFORM | VERIFIED_BILL, SERVICE, ADJUSTMENT | Protected / pending platform revenue; reviewed jurisdiction pack and C/R/D references required | All18 + representative flow |
| PROMOTION_SUBSCRIPTION / OWNER, BUSINESS | ADVERT, SUBSCRIPTION, PROCESSING | PLATFORM, SERVICE_COMPANY | PUBLISHED, SERVICE_PERIOD, REVENUE_EARNED | Protected / pending platform revenue; reviewed jurisdiction pack and C/R/D references required | All18 + representative flow |
| REGULATED_FINANCE / CUSTOMER, REGULATED_PARTNER | PREMIUM, LOAN_INSTALLMENT, PARTNER_FEE, PROCESSING | REGULATED_PARTNER | PARTNER_CONTRACT, PARTNER_APPROVAL | Licensed partner required; execution blocked; reviewed jurisdiction pack and C/R/D references required | All18 + unlicensed wallet/partner rejection |

For every row, version blueprint-v1.0 identifies the disabled template. Active
packs require approved jurisdiction, cancellation, refund, dispute and adjustment
references; these are opaque reviewed policy facts, not executed refund/dispute
remedies. Category-specific components, payer types, roles and conditions are
validated by the engine. Deposit-sensitive products use explicit component
classification and segregated accounts. A declared refundable deposit cannot
be allocated as ordinary beneficiary income.

TestCategoryPoliciesAllEighteenFamiliesAndApprovalGates has eighteen subtests
proving unique keys, version/payer/component/beneficiary/condition representation,
immutable policy, jurisdiction override and regulated approval gates. The
representative category flow test executes protected collection/funding/release
for twelve nonresidential families; initial/subsequent rent and residential deposit
have separate integration tests. OFF_PLAN tests independent-certifier policy and
custody-routing rejection. REGULATED_FINANCE rejects unapproved partner policies
and attempts to repurpose an ordinary wallet as a mortgage/insurance partner.

Section 5 verdict: LOCAL PASS structurally and for supported local workflows.
Production jurisdiction packs and regulated-partner execution remain unconfigured
until external evidence is supplied.

## D. Section 6 closure

Implemented in lifecycle/rent.go, with immutable agreement/obligation tables,
rent-agreements, rent-obligations and allocation/receipt APIs.

- Initial accepted rent represents fully disclosed component classification.
  The fixture NGN 1,000,000 rent plus NGN 200,000 ONE_TIME onboarding totals
  NGN 1,200,000; the constants are test data, never runtime tariff defaults.
- Under unchanged tenancy and no new legal work, the next period includes only
  recurring rent: NGN 1,000,000. The initial fee cannot recur, including across
  agreement revisions. ACTUAL_SERVICE_ONLY legal components require work evidence
  and are excluded from automatic recurrence selection.
- Owner-side commission uses immutable approved policy BPS and contract reference.
  The 200 BPS fixture allocates NGN 20,000 to platform and NGN 980,000 to owner
  while tenant payable stays NGN 1,000,000. Revenue is zero before funded release;
  the test confirms earned revenue only after required evidence/funding.
- Manager fees require an explicit amount and precise owner-issued mandate matching
  the accepted agreement contract. A separate fixture allocates 1,000 minor units
  to manager and 9,000 to owner; an unrelated mandate fails. Managers cannot take
  remaining rent or self-authorize owner proceeds.
- Fixed and continuing agreement facts are distinct from manually created payment
  obligations. Fixed terms cannot automatically continue beyond their end.
  Continuing tenancy uses accepted authoritative terms; approved later revisions
  retain historical obligations and require material-change evidence.
- RENT and DEPOSIT have separate immutable streams and non-overlapping rent period
  constraints. Deposits remain DEPOSIT liabilities, excluded from owner commission
  and ordinary release/withdrawal. Refund/deduction decisions are deferred.
- Annual, semiannual, quarterly, monthly and custom schedules are representable
  under explicit policy. Sources supply dates; no legal notice duration is inferred.
- There is no obligation scheduler, automatic provider initialization, saved bank
  authorization reuse, debit mandate or autopay execution. Creating obligations
  does not charge a provider; tests assert exact initialization counts.
- Receipts project quote components and confirmed collection/settlement/allocation
  counters. Payer access does not expose another party's allocation or wallet.

Evidence: RentInitialRecurringCommissionDepositContinuityAndRevision,
RentManagerFeeRequiresExactOwnerMandateContract, PolicyEffectiveDeactivationAndRentFixedSchedules,
LifecyclePartialReleaseAcrossPayoutAndRevokedVerificationFailure and deposit/privacy tests.
Section 6 verdict: LOCAL PASS within explicitly commanded local financial workflows.

## E. Database changes

One new forward migration: migrations/00005_lifecycle_policy_rent.sql. Baseline
00001–00004 remain unchanged. SQLC-generated models reflect the new owned schema.

| Table / change | Invariant |
|---|---|
| financial_policies | Immutable scoped version, effective interval, approved snapshot/fingerprint; serialized overlap rejection |
| policy_deactivations | Append-only evidenced effective decisions; historical policy retained |
| beneficiary_authorities | Immutable terms/source/wallet/evidence and amount cap; revocation irreversible |
| locked_quotes | Immutable complete accepted payable/components, payer/source/policy; command fingerprint uniqueness |
| payment_intents.locked_quote_id | Immutable canonical FK, unique intent per nonnull quote |
| transaction_funding | One immutable matched evidence/journal per intent |
| allocation_snapshots | Immutable version sequence, unique intent/version and supersession; journal retained |
| allocation_lines | Immutable accepted terms/accounts; positive amounts and bounded release/reserved/paid counters; deferred header FK |
| lifecycle_decisions | Append-only evidence, release, hold and withdrawal approval; unique operation key/fingerprint |
| payout_attempts | Immutable wallet/destination/provider/pool/ref/route/approval/amount; durable states and terminal history/journals |
| payout_destination_bindings | Immutable verified payout-reference to original provider recipient association/expiry |
| rent_agreements | Immutable signed source/reference/version/effective snapshot |
| rent_obligations | Immutable agreement/period/stream/quote; unique period and one initial RENT obligation across versions |
| DB guards | No accepted history update/delete/truncate; no late line append; deferred allocation sum/count reconciliation; line/authority/payout term guards |
| validate_journal | Extends controlled nonnegative balances to SETTLEMENT_CONTROL and RESERVE; preserves existing dimension/balance checks |

Engine guards additionally enforce component-level reconciliation, unique current
authority, aggregate beneficiary cap, exact owner proportions, current verification,
non-overlapping periods, serialized coverage and approved fee capacity. Empty
Goose up/down round trips pass. Destructive lifecycle rollback with retained policy,
quote, authority or agreement history is rejected. Down restores the original
journal check and explicitly drops the canonical-intent trigger. Use forward
migrations and compensating history after adoption.

## F. API/event changes

Updated internal/public OpenAPI, Paystack webhook specification and Payment
Postman collection: **45 internal + 10 public + 1 webhook = 56 operations**.
Internal signed commands added:

- `GET /internal/v1/financial-policy-families`
- `POST /internal/v1/financial-policies`
- `POST /internal/v1/financial-policies/resolve`
- `POST /internal/v1/financial-policy-deactivations`
- `GET /internal/v1/financial-policies/{id}`
- `POST /internal/v1/beneficiary-authorities`
- `POST /internal/v1/beneficiary-authorities/{id}/revoke`
- `POST /internal/v1/locked-quotes`
- `GET /internal/v1/locked-quotes/{id}`
- `GET /internal/v1/locked-quotes/{id}/receipt`
- `POST /internal/v1/allocations`
- `GET /internal/v1/allocations/{id}`
- `POST /internal/v1/allocation-lines/{id}/hold`
- `POST /internal/v1/release-evidence`
- `POST /internal/v1/allocation-releases`
- `POST /internal/v1/transaction-funding`
- `POST /internal/v1/payout-destination-bindings`
- `POST /internal/v1/withdrawal-approvals`
- `POST /internal/v1/withdrawals`
- `GET /internal/v1/withdrawals/{id}`
- `POST /internal/v1/withdrawals/{id}/resolve`
- `POST /internal/v1/rent-agreements`
- `POST /internal/v1/rent-obligations`
- `GET /internal/v1/rent-obligations/{id}`
- `POST /internal/v1/rent-obligations/{id}/allocate`

Four safe public reads added under /api/v1/payments: allocations/{id},
withdrawals/{id}, rent-obligations/{id}, locked-quotes/{id}/receipt. Public commands
remain absent. Internal /payment-intents additionally accepts the optional canonical
locked_quote_id; legacy foundation intents remain compatible.

Nineteen canonical event schemas are present: eight retained foundation facts
and eleven added lifecycle facts:

- `finance.allocation_created.v1`
- `finance.allocation_released.v1`
- `finance.payout_confirmed.v1`
- `finance.payout_failed.v1`
- `finance.payout_initiated.v1`
- `finance.payout_resolution_required.v1`
- `finance.platform_commission_allocated.v1`
- `finance.rent_obligation_created.v1`
- `finance.rent_payment_verified.v1`
- `finance.rent_split_pending.v1`
- `finance.withdrawal_reserved.v1`

The Paystack callback endpoint remains the same signed raw-body HMAC intake;
transfer.success/failed/reversed route through original-reference readback. Gateway
delegation/routing is reused and Owner contracts/business events remain unchanged.
No collected event is translated into a false Owner HELD/RELEASED or confirmed
booking fact. Dedicated cross-service business lifecycle adapters remain a source-team
integration dependency.

Contracts are generated by scripts/payments/generate_contracts.py and
lifecycle_contracts.py. cmd/contracts reflects actual JSON DTO tags, UUID/date
formats, optional fields and nullable arrays/maps. Existing simple Evidence
commands retain their schema; release evidence has its own ReleaseEvidence schema.
Event schemas include only reachable payload definitions. Route/Postman parity,
OpenAPI loading and actual serialized DTO/outbox JSON validation pass.

## G. Testing

Final unique certified set: **186 top-level tests + 59 subtests**, zero failed
or skipped tests. Payment's final run supersedes its earlier full-regression slice;
repeated runs are not added together. Packages containing no test files are not
counted as skipped tests.

| Evidence | Exact result |
|---|---|
| Final Payment race suite, sections-4-6-payment-final-test-results.jsonl | 42 top-level tests + 59 subtests: 36 PostgreSQL integration tests, 2 config tests, 4 provider HTTP-simulator tests |
| Full three-module race run, sections-4-6-final-test-results.jsonl | Payment 40 + 57 before final two edge tests; gateway 22; Owner 122; all PASS |
| Final aggregate | Payment 42 + gateway 22 + Owner 122 = 186 top-level; final Payment 59 subtests; zero test skips/failures |
| PostgreSQL | Real PostgreSQL 18, dedicated loopback port 56439, disposable databases, actual migrations; CF_REQUIRE_INTEGRATION=1 |
| Redis | Real Redis 8.8 on dedicated loopback port 56440; gateway rate-limit integration PASS |
| JetStream | Embedded real NATS server/network JetStream publication, acknowledgements/recovery and shutdown PASS; no hosted ACL certification |
| Provider simulation | Actual Paystack adapter and neutral test adapters against explicit HTTP fixtures; recipient/transfer/settlement contexts and financial race behavior PASS |
| Race/toolchain | Go 1.26.9; CGO_ENABLED=1; verified GCC 16.2 UCRT; -race -count=1 on all affected modules and final Payment rerun PASS |
| vet/staticcheck/build | GOWORK=off independently in Payment, Owner and gateway; staticcheck 2026.2.1; source builds PASS, final Payment changes rechecked |
| SQLC | v1.31.1 generate/diff, Payment and Owner PASS |
| Architecture | Full boundary gate and self-test PASS; Paystack confined to Payment, no competing ledger or cross-service DB access |
| Contracts | 56 route/Postman operations, 19 event schemas, actual DTO and outbox payload validation PASS; deterministic generation |
| Format/whitespace | Changed Payment Go source gofmt; git diff --check PASS; pre-existing unrelated CRLF source retained |
| Real Paystack sandbox | NOT RUN — actual merchant credentials/commercial approvals unavailable |
| Docker image / hosted CI / staging | NOT RUN in this local task |

sections-4-6-test-summary.json records exact package totals. The complete Owner
and gateway suites retain their Sections 1–3 regression certifications. HTTP
simulators, embedded JetStream and local builds are explicitly distinguished
from real provider certification and deployed staging.

Superseded diagnostic failures were corrected before final certification:
transaction queries incorrectly using the pool caused pool starvation; migration
rollback needed an explicit trigger drop; a test treasury account incorrectly
used a public wallet; test policy condition/component keys and an already-locked
quote setup were corrected. The review also fixed payer receipt access, authority/
verification serialization, payout fee reserves, partial release across payout
phases and revoked-verification compensation. Final evidence contains no failure.

## H. Security and financial safety

| Safety property | Verdict and enforcement |
|---|---|
| Arbitrary money/balance/journal mutation | PASS: no public mutation, internal journal primitive Payment-only |
| Allocation without authority / manager theft / unverifiable co-owner | PASS: signed source, current immutable beneficiary authority, ownership BPS, fee-only mandate, aggregate cap |
| Pending/protected/deposit spend | PASS: release funding/evidence and line account classification; withdrawal AVAILABLE-only |
| Unfunded or unsupported custody release | PASS: actual matched funding plus pool coverage; custody-required products blocked by unapproved capability |
| Over-release / concurrent withdrawal | PASS: locked counters, exact caps, positive arithmetic and DB/engine invariants |
| Duplicate payout after timeout | PASS: original attempt/ref retained, durable CAS, repeated commands do not initialize again |
| Forged callback / payout context | PASS: signed service/HMAC intake and independent amount/currency/recipient/merchant/mode/reference readback |
| Fee consumed from beneficiary funds | PASS: explicit approved cap, separately available cash surplus reserved; unknown or excessive fee retains payout reservation |
| Failed payout while held/restricted/KYC revoked | PASS: safe frozen compensation and preserved terminal history |
| Tenant repeated initial/legal fees | PASS: immutable recurrence classes, unique initial obligation, new actual work required |
| Hardcoded production 2% / NGN 200,000 / legal deadline | PASS: test fixtures only; approved packs or unconfigured/disabled state |
| Unauthorized rent increase/change | PASS: accepted current version, later effective time, consent and material-change evidence |
| Payer/beneficiary privacy | PASS: receipt and owned-share projections; unrelated wallets/allocations rejected or omitted |
| Autopay or saved-account execution | PASS: no scheduler, mandate/custody/live capability activation or automatic debit path |

This is local software safety evidence, not an external legal/provider approval.
Opaque business attestations require real authenticated source integrations; local
fixtures do not certify the correctness of real property or bank ownership evidence.

## I. External blockers

| Dependency | Status / required evidence |
|---|---|
| Original eight v5 product feature documents | Unavailable; controlled product/source documents needed before reconciling additional legal rules |
| Paystack merchant/category/channel and payout approval | Uncertified externally; actual secret-managed merchant test configuration and written commercial approval |
| Paystack-Titan DVA eligibility/provisioning | Externally blocked; approved validation/consent/merchant activation and real sandbox/live evidence |
| Licensed custody/safeguarding/escrow | NOT APPROVED; legal/banking/provider arrangement, approval and proof; merchant balances are insufficient |
| Jurisdiction policy packs / tax / approved tariff | Unconfigured production; reviewed cancellation/refund/dispute/deposit timelines and contract templates |
| Owner commission and manager fee | Explicit accepted production contracts/rates required; no fixture rate promoted to a default |
| Bank ownership, current KYC/KYB and step-up/risk evidence | Real Verification/Admin integrations, expiry/revocation and approval evidence required |
| Regulated mortgage/insurance partner | Licensed partner contract, eligibility, approved integration and provider/custody legal structure required; ordinary wallet execution rejected |
| Other domain lifecycle adapters | Authenticated source facts and consumer mapping required; existing Owner integration is retained, broader live workflows not certified |
| Settlement membership >100 records | Complete pagination and provider certification needed; current incomplete totals fail closed |
| Staging and production infrastructure | Least-privilege DB, scoped broker ACL/TLS, secrets, deployed E2E, alerting/recovery, image build/digest and explicit approval |

## J. Overall verdict

| Certification | Independent status |
|---|---|
| Section 4 LOCAL STATUS | PASS — bounded allocation/release/reservation/readback implementation with real DB races |
| Section 5 LOCAL STATUS | PASS — all eighteen structural policy families, immutable packs and supported representative workflows |
| Section 6 LOCAL STATUS | PASS — manually commanded rent/continuity/commission/fee/deposit rules; no autopay |
| Paystack sandbox | NOT RUN / NOT CERTIFIED |
| DVA | Local foundation adapter regression PASS; real eligibility/provisioning NOT CERTIFIED |
| Payout provider certification | HTTP/local financial simulator PASS; real provider approval/sandbox NOT CERTIFIED |
| Custody | NOT APPROVED; activation disabled |
| Staging | NOT RUN |
| Production readiness | NOT APPROVED; live execution remains disabled |

## K. Next-section handoff

The next explicitly authorized blueprint section may rely on immutable canonical
quotes, verified collection, versioned exact allocations and source authority,
separate protected/deposit/reserve accounting, matched test funding, policy-bound
partial release, independent holds, available-only approved reservation, durable
one-attempt payout readback/recovery, append-only decisions, eighteen disabled
policy templates and immutable manually commanded rent obligations/receipts.
Retained stable baseline wallet, ledger, intent, routing, inbox/outbox and Owner
registration contracts remain available.

It must preserve original merchant/entity/currency pools, terminal and accepted
history, transaction outbox, payer privacy and existing business-event separation.
Refund/deposit remedies, chargeback and post-terminal reversal workflows need
separate implementation and approvals. Full OTP/recipient-change onboarding,
regulated partner execution, large-batch pagination, staged domain adapters and
live provider/custody gates remain explicitly qualified dependencies.

**STOP: Section 7 was not started. No automatic debit, autopay execution or live
custody/payout activation was implemented. No push was performed.**
