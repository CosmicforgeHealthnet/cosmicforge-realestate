# Sections 1–3 implementation coverage

> [Paystack TEST certification](PAYSTACK_SANDBOX_CERTIFICATION.md) supersedes historical sandbox statuses below; local implementation evidence is retained.

> Historical Sections 1–3 baseline. The current [Sections 4–6 closure](SECTIONS_4_6_CLOSURE_REPORT.md) and [operations guide](SECTIONS_4_6_OPERATIONS.md) supersede deferred lifecycle descriptions below. Original test evidence and scope are retained.

LOCAL PASS means implemented and tested within the assigned foundation; it does not mean sandbox, custody or production approval. Blueprint subsection numbers are primary. The longer implementation prompt expands them into the requirement rows below. Test names resolve under services/payment-service/tests/integration unless another module is named.

| Blueprint / prompt scope | Implemented code/contract | Evidence and status |
|---|---|---|
| 1.1 / Financial Core ownership | internal/core, finance, migrations; boundaries and ADR-0012 | One ledger and owned DB; route architecture gate, invariant/auth tests — LOCAL PASS |
| 1.2 / Domain facts and permissions | DomainCaller, OwnWallet, signed JWT; ARCHITECTURE_AND_AUTHORIZATION.md | TestWalletRolesConcurrencyAndIsolation, TestHTTPAuthenticationAndOwnership; owner contracts preserved — LOCAL PASS |
| 1.3 / Non-negotiable boundaries | immutable journals/route, scoped pools, live gates, unsupported later interfaces | Ledger/dimension/restriction tests, TestLiveOperationsCannotBeEnabled and NoHardcodedCredentialFallback — LOCAL PASS |
| Prompt 1.3 future services / 1.5–1.6 deliverables/certification | Provider-less app, signed contracts, dependency/coverage/ADR register | TestStartupWithoutProvidersAndRealJetStreamPublication — INTEGRATION PASS locally; future domains contract ready |
| 2 wallet model / prompt 2.0–2.2 | roles, Provision, financial identities/bindings, Owner durable delivery | All 12 roles, concurrent idempotency, same-domain co-role and verified customer/owner linkage; Owner TestAutomaticWalletRegistrationDurableDelivery — LOCAL PASS |
| 2.1 / prompt 2.3 seven views | Balances, account_balances; DEPOSIT grouped once | Collection/coverage/history tests; all withdrawal flags false pending approval — LOCAL PASS |
| 2.2 / prompt 2.4 immutable accounting | Post/PostTx/Reverse; deferred DB checks and sealing | TestLedgerInvariantsImmutableAndReversal, contended 16 reservations with exactly 10 funded successes — real DB/race PASS |
| 2.2 / prompt 2.5 hierarchy | Immutable wallet/pool/dimension_ref accounts and platform categories | Currency/entity/pool and platform/public-account guards; TestHistoryPaginationAndLedgerDimensions — LOCAL PASS |
| Prompt 2.6 financial identity / verification/payout references | Verified LinkAccount, Verification, immutable payout_account_refs | TestRestrictionRetentionAndVerifiedPayoutReferences; no inference from email or existing-history merge — LOCAL PASS with explicit pre-linking dependency |
| Prompt 2.7 liquidity/coverage | Coverage, ReconcileBalance, reserve/restricted pool checks | TestCollectionsExactlyOnceAndSettlementSeparation; unbacked AVAILABLE rejected — LOCAL PASS; complete settlement matching later |
| Prompt 2.8 security / closure | Source/actor ownership; audited identity controls and retained obligations | HTTP/source isolation, manager bank-read denial, restriction/dual-approval/close tests — LOCAL PASS |
| Prompt 2.9 wallet API | 6 public reads, 20 internal operations; no arbitrary balance/post API | TestOpenAPIAndPostmanRouteParity, response DTO/schema validation — CONTRACT PASS |
| Prompt 2.10 migration / 2.11–2.12 certification | Payment 00001–00004; Owner 00019 | Payment migration round trip plus Owner restricted-role/migration tests; final real DB/race suites — LOCAL PASS |
| 3.1 / prompt 3.1–3.2 provider roles/registry | providers interfaces and deployment registration | TestProviderHTTPSCredentialIsolationAndUnknown, unsupported custody, routing fixtures — CONTRACT PASS; executable refunds/payouts/mandates deferred |
| 3.2 / prompt 3.3 Paystack collection | Paystack init/verify/readback, card/bank_transfer, merchant fallback fetch | Official httptest wire contracts, six context-mismatch subtests, exactly-once verification — LOCAL/CONTRACT PASS; SANDBOX NOT RUN |
| 3.2 / prompt 3.4 callback recovery | HMAC raw body, durable inbox, retry/dead/lease/replay, outbox | TestDurableWebhookRecoveryAndReplay, TestPaymentOutboxDurableRetryAndCrashLease, real embedded JetStream — INTEGRATION PASS locally |
| 3.4 / prompt 3.5 DVA provisioning | Consent/KYC/customer validation/Paystack provider discovery and assignment/readback/deactivation | TestDVAConsentVerificationIdempotencyAndSync, TestDVACollectionMatchingExactlyOnceAndRevokedVerification; provider HTTP fixtures — CONTRACT PASS; actual DVA eligibility EXTERNAL BLOCKED |
| 3.2 / prompt 3.6–3.7 Manual Payouts and state | Approval/settlement references, merchant balances, append-only batches, separate collection/settlement states | Settlement-separation/reconciliation tests — LOCAL PASS; Manual Payouts activation and cash attestation external/later |
| 3.3 / prompt routing/recovery/migration/multi-provider | Capability/entity/currency/amount/priority/fee/liquidity/failover filters; immutable intent route | TestRoutingAndUnknownOutcomeNeverRecharges covers alternate routing and original readback; TestMultipleAdapterImplementationsThroughSignedHTTPContract exercises neutral and actual Paystack adapters through the same signed HTTP contract — LOCAL PASS; funded provider migration disabled |
| 3.1–3.4 / custody/security/final certification | DisabledCustody; mode/key/TLS checks; safe observations, logs/metrics | Restriction/reversal/context tests, live gates, final vet/staticcheck/architecture/contracts — LOCAL PASS; external approval NOT APPROVED |

Acceptance scenarios within scope: A04 multiple roles, A10 payment timeout, A11 durable callback recovery, A15 provider swap, A16 accounting proof (local journal/observation foundation), A17 absent custody approval, A18 role-scoped withdrawal prohibition, A19 explicit DVA consent, A20 deferred autopay, A21 source isolation and A22 exception restriction. A01–A03, A05–A09, A12–A14 require later obligations/splits/deposits/release/withdrawal workflows and are not claimed complete.

Gate A Section 1: IMPLEMENTED LOCALLY / CONTRACT PASS. Gate B Section 2: IMPLEMENTED LOCALLY / real PostgreSQL and race INTEGRATION PASS. Gate C Section 3: IMPLEMENTED LOCALLY / CONTRACT PASS / local JetStream INTEGRATION PASS. Gate D contracts/migrations/docs/regression: PASS for executed local checks. Paystack SANDBOX NOT RUN; custody EXTERNAL BLOCKED; staging NOT RUN; production NOT APPROVED. Exact counts, commands and qualifications are in TEST_EVIDENCE.md.
