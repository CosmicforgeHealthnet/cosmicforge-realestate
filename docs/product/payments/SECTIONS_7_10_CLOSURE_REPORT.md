# CosmicForge Payment Service — Sections 7–10 closure report

Authority: the user's attached Sections 7–10 implementation prompt. The retained master blueprint v1.0 is reference material; its later chapters do not authorize later work. Blueprint SHA-256: `fd1cfd14fb2f78ed7601fbafc49113f7d03049d3ef8041f6f08f0342d964e7f1`.

This tracked report is finalized after the local commit using `scripts/payments/finalize_report.py --sections-7-10`. The export at `tmp/payments/SECTIONS_7_10_CLOSURE_REPORT.md` records the exact committed SHA and clean-tree check. A commit cannot contain its own SHA.

## A. Repository information

- Starting branch: `codex/payment-sections-4-6`.
- Starting SHA: `8dfb00a36793392a6ac24073da083e0f4d8508f7`; starting tree clean.
- Final branch: `{{FINAL_BRANCH}}`.
- Final SHA: `{{FINAL_SHA}}`.
- Working tree: `{{FINAL_STATUS}}`.
- Commit: `{{FINAL_COMMIT}}`.
- Push: none. No branch publication, merge, deployment or real money operation was performed.
- Baseline migrations 00001–00005 and prior closure/evidence are retained. The original Financial Core is extended; there is no second ledger.

## B. Section 7 closure

**LOCAL PASS**, within the tested local capability boundary.

Five configured deposit types retain canonical payer, intent/quote, business, jurisdiction, currency, liability account, custody/ingress, inspection references and approved deadlines. Deposit/reserve registration excludes ordinary income, tax and processing components. Deposit liabilities cannot become owner availability through ordinary release. Claims and challenges append immutable facts. Exact contested amounts are frozen under row locks; unrelated amounts remain governed by uncontested-release policy.

Case review, decision and execution retain independent authority, actor, reason and evidence references. Current/revoked evidence, separated reviewer/executor and high-value approvers are checked. Return, partial award, reserve extension, rework and legal escalation preserve their consequences. Partial awards create internally funded protected obligations with no provider collection; normal release/payout controls still apply. Expired review requires a policy-bounded privileged override, which cannot change a closed case or journal.

Each refund is separately reserved and attempted. Original-payment serialization and cumulative confirmed/active caps prevent over-refund. Submission is durable before the provider call; readback verifies original charge, attempt marker/ID, merchant, mode, currency and amount. Unknown results retain money and cannot authorize replacement submission, cancellation or wallet substitution. Expired unsubmitted refunds require review; explicit cancellation restores refund-due money. A confirmed no-effect failure is distinct from a generic provider failure.

Original-instrument and approved customer wallet-credit strategies are implemented locally. Provider-reversal, verified-bank and manual regulated-remedy strategies are explicitly gated policy reservations until an approved execution rail exists. They do not pretend to execute a refund. Customer cash-out is disabled.

Authoritative economic chargebacks append compensation, restrict affected pools and preserve journals. Retained protected/available money is consumed first, including funded customer top-up availability. Released/paid/spent money becomes an explicit recoverable. Insufficient real ledger cash becomes PROVIDER_PAYABLE; fake cash and negative controlled balances are prohibited. Economic references prevent duplicate compensation and refund overlap is escalated for reconciliation. Later payout/refund reversals preserve original terminal facts and return money to FROZEN.

Recoverable actions record canonical beneficiary/customer or platform liability assignment, dual authority, current evidence, risk and escalation. Approved funded internal reserves or policy-permitted retained proceeds reduce the existing asset under caps; deposits/tax and arbitrary employee wallets cannot fund recovery. Future beneficiary release/withdrawal is blocked by outstanding assigned debt. Repayment accounting does not claim an external provider cash receipt or settle a payable without separate evidence.

## C. Section 8 closure

**LOCAL PASS** for approved local test-mode payout controls.

Existing verified ownership, role-specific components, fee-only manager mandates, legal-party/KYC, source authority, professional mandates and beneficiary-owned destination bindings remain. Managers cannot redirect owner proceeds. Corporate/business payout approval adds a chain, distinct approvers and the exact accepted business/project reference; configured consent and work-mandate controls are checked again at withdrawal.

Destination versions retain ownership/KYC, predecessor, step-up, risk, notification, expiry and policy cooling. Ordinary cooling retains the prior canonical bank; high-risk cooling blocks withdrawals. A second unversioned bank cannot bypass canonical destination selection. Revoke appends a fact. Current destination, authority, recipient, verification and unused exact approval are rechecked under the money workflow.

Payout submission uses durable RESERVED/SUBMITTING compare-and-swap and one external call. Provider acceptance is not payment. PROCESSING, OTP_REQUIRED, UNKNOWN_OUTCOME and REVIEW_REQUIRED retain money. CONFIRMED_PAID or confirmed no-effect failure requires authoritative readback, including fee knowledge/cap. A later reversal appends compensation and projects REVERSED without editing CONFIRMED_PAID history. The availability endpoint separates available, withdrawable now, protected, settlement/payout pending, reserved, rail unavailable and restricted. Withdrawal eligibility also checks fee capacity, canonical bank and exact current approval. CUSTOMER withdrawable-now remains false.

## D. Section 9 closure

### 9.1 — LOCAL PASS

Owned verified marketplace CUSTOMER wallets use approved, consented, expiring checkout/DVA top-ups. Original receipts and once-only funding records prevent double credit. Unmatched incoming DVA observations remain preserved without arbitrary wallet assignment. Matching is authoritative and settlement membership/fees plus dual cash attestation precede usable credit. Customer-net and platform-funded fee treatments are explicit approved policy. Expired funding requires review.

An accepted wallet obligation is atomic and idempotent: exact quote/payer/consent/amount/currency/policy validation, locked AVAILABLE debit, internal funding intent, ordinary allocation validator, protected recipient liability, audit and outbox commit together. Repeated spending credits once; different concurrent obligations cannot consume the same balance; insufficient/invalid allocation rolls back. Owner release and platform earning remain separate. Customer top-up chargebacks remove availability or append a canonical recoverable after spending.

### 9.2 — DESIGN RESERVED / EXECUTION DEFERRED

The descriptor reserves consent, accepted obligation and low-balance notice concepts only. Tests verify the DEFERRED descriptor and rejection of mandate capability, live financial activation, recurring execution and customer cash-out flags. No automatic wallet debit, saved-bank debit, saved-card charge, direct debit, mandate execution or automatic recurring provider worker exists. Existing manual rent obligations remain manual. Section 9.2 execution is not labelled PASS.

## E. Section 10 closure

**LOCAL PASS** for local lifecycle/accounting/reliability primitives.

Payment intent states are REQUESTED, SUBMITTING, INITIALIZED, PENDING, PROCESSING, UNKNOWN_OUTCOME, VERIFIED_SUCCESS, VERIFIED_FAILURE, RECONCILIATION_EXCEPTION and unsubmitted CANCELLED. Cancellation requires the original canonical source/Admin, configured policy and current evidence; collected/ambiguous money requires original readback or an approved case/refund. Conflicting terminal observations preserve exception/history rather than generating a new charge.

Allocation states retain PENDING, PROTECTED, HELD, DISPUTED, RELEASE_ELIGIBLE, RELEASED, AVAILABLE, WITHDRAWAL_RESERVED, PAID_OUT, CORRECTED, REVERSED and REVIEW_REQUIRED. Amount-specific cases retain their own frozen balances and state history instead of converting unrelated amounts into a disputed full line. Accepted financial terms remain immutable. SQL state-history/audit triggers retain prior and resulting intent, allocation, payout, refund and case states.

Original collection/payout/refund readback jobs use configurable bounded attempts/backoff and token leases. Exhausted or crashed final leases dead-letter. Sweeps exclude dead/completed, leased and backoff-delayed jobs so older jobs cannot starve newer attempts. Authorized replay retains provider/reference and immutable history, never another external submission. Existing inbox/outbox deduplication, bounded retries, crash leases, dead letters, authenticated callbacks and real JetStream publication remain covered. Unsupported/unconfirmed events remain retryable. Submission states now publish atomically before provider calls; terminal monetary effects, journals, audit and events remain atomic.

Pool/account/identity/line/attempt locks and economic uniqueness protect money. Positive exact integer postings, deferred balance checks, dimensional checks and immutable journals enforce double entry. RECOVERABLE and PROVIDER_PAYABLE have enforced internal normal sides and non-negative controlled balances. Only an authoritative compensating debit may consume already-restricted availability; this cannot credit availability through the restriction.

Reconciliation compares original provider context, missing/stale collection/payout/refund facts, settlement membership/fees, terminal journals, merchant cash, coverage, negative balances, unassigned debt, dead letters and unmatched DVA money. Runs/findings are immutable; bounded batch limits are explicit. Deficits restrict pools. Resolution requires a later clear observation and dual-reviewed evidence, and does not silently alter money. Operational metrics/alert hooks include claims/deposits, refund and payout ambiguity, recoverables, cooling, funding, reconciliation and all dead-letter channels. Monetary metrics remain currency-specific.

`SECTIONS_7_10_OPERATIONS.md` documents incidents, unknown-result readback, replay, provider/signing-key rotation, secret references, audit retention, encrypted backup/PITR expectations and isolated restore procedure. Local tests verify crash/replay and migration retention. A production restore drill, approved RPO/RTO, monitoring deployment and infrastructure remain external blockers.

## F. Database changes

Only new forward migration `00006_remedies_customer_reliability.sql` is added. SQLC models are regenerated. Prior migrations are unchanged. Complete table/constraint/index/function/trigger inventory, including generated trigger expansions, is in `SECTIONS_7_10_SCHEMA_CATALOG.md`.

New tables: remedy_policies, remedy_evidence, refundable_deposits, financial_cases, financial_case_facts, refund_attempts, financial_reversals, financial_recoverables, recoverable_actions, destination_versions, destination_revocations, customer_topups, customer_funding, wallet_spends, unmatched_dva_credits, unmatched_dva_resolutions, reconciliation_runs, reconciliation_findings, reconciliation_resolutions, financial_state_history, original_readback_jobs and original_readback_history.

New explicit indexes: refund_recovery and case_line_open, plus table primary/unique indexes. Ledger-kind checks add RECOVERABLE/PROVIDER_PAYABLE; intent-state checks add CANCELLED. Foreign keys, positive/capped amounts, case split equality, original economic uniqueness, once-only funding/spend, destination predecessor uniqueness and valid states are enforced. Trigger functions enforce immutable terms/history, state transition restrictions, original-readback terms, correct internal asset dimensions, append state audit and deferred non-negative remedy balances. Financial tables reject DELETE/TRUNCATE or retain immutable terms as applicable. Empty-schema up/down/up passes; rollback with retained Section 7–10 facts is refused.

## G. API/event changes

100 implemented operations and 31 payment event schemas are generated and matched against actual routes/DTOs/outbox envelopes. The complete new internal/public operation list and webhook contract are retained in `SECTIONS_7_10_API_HANDOFF.md` and the generated contracts. Postman includes pool UUID and required idempotency headers.

New internal commands/reads cover remedy policy/evidence, deposit, case/facts/decision/override, refund attempts, collection reversal, recoverable action, destination versions, customer top-up/funding/spend, availability, unmatched DVA resolution, readback replay, reconciliation/resolution, metrics, cancellation and deferred recurring design. Public additions provide owned top-ups/spend/availability, permitted case/challenge/history/refund/deposit reads and beneficiary withdrawal. Admin/Verification execution controls remain internal.

Webhook handling validates current/prior-key raw-body signatures and normalizes refund/dispute identifiers. It performs original readback; a callback alone cannot move money. New events are deposit_registered, case_opened, case_decided, refund_reserved, refund_state_changed, financial_reversed, recoverable_action, destination_changed, customer_funded, wallet_spent, reconciliation_completed and provider_state_changed, each under `finance.*.v1`. Existing envelopes/events remain retained.

## H. Testing

| Suite | Top-level tests | Subtests | Failures | Skips |
| --- | ---: | ---: | ---: | ---: |
| payment-service | 69 | 68 | 0 | 0 |
| individual-owner-service | 122 | 0 | 0 | 0 |
| api-gateway | 22 | 0 | 0 | 0 |
| **Unique total** | **213** | **68** | **0** | **0** |

The total is 213 unique top-level tests plus 68 subtests, all passing under race detection. Subtests are reported separately and are not counted as additional top-level tests.

Evidence: `sections-7-10-final-test-results.jsonl` contains the final owner/gateway race results; `sections-7-10-payment-final-test-results.jsonl` contains the latest complete payment race results. These are disjoint suites. Superseded work runs are excluded. `sections-7-10-test-summary.json` is produced from these exact files.

Real local PostgreSQL 18, Redis 8.8 and embedded real NATS JetStream are used with `CF_REQUIRE_INTEGRATION=1`; no integration test is skipped. Provider calls use deterministic adapters and explicit HTTP simulations. No real Paystack sandbox, live payout, live refund, Titan eligibility, regulated custody, staging or production test is claimed.

Final gates: race suite, all-three-module vet/staticcheck/build, architecture boundary/self-test, payment/owner SQLC diff, route/Postman parity, actual event/response schema validation, migration up/down/up and retained-history rejection, formatting and whitespace checks. Toolchain: Go 1.26.9, SQLC 1.31.1, staticcheck 2026.2.1, CGO/GCC race support. Saved failure-free final runs supersede exploratory runs.

Coverage includes full deposit awards and retained override authority, recovery sweep starvation, deposit types/partial awards through payout, challenged facts, current/revoked authority, contested-only release/concurrent claim caps, partial refund caps, duplicate/unknown refund submission, expired reservations, refund/payout reversals, paid/spent chargebacks, recoverable liability/reserve caps, canonical bank/cooling, corporate project/approval chains, gateway claimant authority, DVA unmatched/matched funding, once-only top-ups, accepted wallet spend/concurrency/rollback, spent top-up canonical debt, reconciliation/follow-up resolution, dead-letter/crash/replay, webhook rotation, mixed-currency metrics and recurring execution absence.

## I. Security and financial safety

| Requirement | Local verdict and boundary |
| --- | --- |
| Deposit segregation | PASS; original payer liability, no ordinary income release |
| Over-refund | PASS; original payment/decision/active-plus-confirmed caps |
| Duplicate refund | PASS; one durable external submission and economic completion |
| Chargeback integrity | PASS; original evidence, append compensation, explicit asset/payable |
| Payout bank change | PASS; current owned bank, immutable version, cooling/revocation |
| False available/withdrawable balance | PASS; eligibility projection and posting/rail/fee controls |
| Double top-up | PASS; unique receipt/intent/top-up/funding facts |
| Double wallet debit | PASS; quote/key fingerprint, locks and atomic allocation |
| Unknown external outcome | PASS; retained reservation, original readback, bounded dead-letter |
| Autopay absence | PASS for absence only; execution DEFERRED |

Current legal-party/KYC, source/domain authority, signed gateway delegation, Admin/Verification roles, issuer evidence, strict request decoding and bounded bodies remain enforced. No raw card/CVV/PIN, bank-login secret or primary inspection document is introduced. Credential references and bounded webhook-key overlap are validated; runtime keys remain secret-wrapped. These local checks do not establish regulatory authorization or provider certification.

## J. External blockers

| Dependency | Status and required evidence |
| --- | --- |
| Paystack real sandbox | NOT CERTIFIED; actual merchant test credentials and end-to-end evidence |
| Refund provider | NOT CERTIFIED; processed/ambiguous/reversed results and original instrument evidence |
| Payout provider | NOT CERTIFIED; actual beneficiary, fee, failure and reversal evidence |
| DVA/Titan eligibility | NOT CERTIFIED; merchant/country/account product approval and real matching |
| Custody/safeguarding | NOT CERTIFIED; lawful safeguarding, segregated bank custody and reconciliation |
| Production policy packs | NOT APPROVED; commercial/risk/approval controls and deployment activation |
| Jurisdiction deposit deadlines | NOT APPROVED; approved legal deadlines, notices and remedies by jurisdiction |
| Staging | NOT CERTIFIED; environment, secrets, durable workers and end-to-end operations |
| Production infrastructure | NOT READY; approved RPO/RTO/PITR drills, observability, incident and security gates |

Live collections, custody, transfers, refunds, recurring execution and customer cash-out activation are rejected. Other reserved refund rails remain disabled pending appropriate approved adapters. No lack of external credentials is reported as a local test PASS for the real provider.

## K. Overall verdict

- Section 7: **LOCAL PASS** within implemented approved local remedy strategies.
- Section 8: **LOCAL PASS** for approved local test-mode controls.
- Section 9.1: **LOCAL PASS**.
- Section 9.2: **DESIGN RESERVED / EXECUTION DEFERRED**; autopay remains deferred.
- Section 10: **LOCAL PASS**.
- Paystack sandbox, payout/refund certification, DVA eligibility, custody and staging: **NOT CERTIFIED**.
- Production readiness: **BLOCKED BY EXTERNAL APPROVAL/CERTIFICATION/INFRASTRUCTURE**.

## L. Next-section handoff

Later authorized work may rely on the original immutable ledger, signed service/domain authority, canonical accepted quote/intent/receipt/funding/allocation records, amount-specific claims, independently approved decisions/awards, capped durable refund attempts, original-reference reversal/recovery facts, role-scoped payout approvals and destination versions, explicit availability, owned customer funding/atomic wallet spend, immutable state audit, inbox/outbox, bounded readback/replay, reconciliation findings/resolution and metrics.

Later work must preserve original references/history and pass separate production/provider/legal gates. The recurring descriptor supplies no execution authority. No automatic recurring rent/wallet/provider payment, saved-bank/card debit, direct debit, mandate execution or customer cash-out was added. **Section 11 was not started.** Stop after this tested local commit; no push or deployment is authorized.
