# Business Operations Sections 8–11 — closure pass (7 November 2026)

This is a status record for the 7 November 2026 completion pass on `staging`. It is **not** a declaration that Sections 8–11 are fully complete. Every row below is backed by named, executed tests or is explicitly marked PARTIAL/FAIL with the remaining internal work.

Starting SHA: `7e57f13725b185c70230058c632e612fc8e5b946`. CI repair SHA: `d71ac6c50d51c3db98f16a0848fde91fbac56793`. Final SHA and workflow links: see section 7 (filled after the final push).

## 1. Backend CI repair

| Item | Evidence |
|---|---|
| Root cause | `tests/e2e/owner_primary_journeys_test.go` built short-let `check_in` from the UTC civil date. The service (`internal/usecases/shortlet.go`, `businessDate`) correctly compares against the property's `Africa/Lagos` business date. Between 23:00 and 24:00 UTC the UTC date is already in the past in Lagos, so the booking was correctly rejected with HTTP 400. |
| Fix | Test fixture only. New helper `propertyLocalNow` reads the property's timezone through the Owner API, derives date-only fields in that calendar and waits out the last two minutes before local midnight. The production validator and service timezone are unchanged. |
| Regression coverage | `services/individual-owner-service/tests/integration/shortlet_local_midnight_test.go` `TestShortletLagosMidnightBusinessDate` (controlled clock): booking before Lagos midnight, `CHECK_IN` rejected as `TOO_EARLY`, the stale UTC date rejected with the exact past-date error at 23:00 UTC, then check-in, checkout and turnover on the property-local date. |
| Local results | New test PASS; Owner shortlet/clock integration tests PASS; `TestOwnerFunctionalJourneyWithRealServices` (all 4 journeys incl. `14_4_shortlet_hold_stay_access_turnover_completion`) PASS locally without `-race` (no C toolchain on the Windows workstation). |
| CI on `d71ac6c` | Backend (all jobs, including the process-level E2E job) PASS, Architecture structure PASS, Contracts PASS, Security PASS, Images PASS (one api-gateway job failed exporting the GitHub Actions build cache after a successful image push; re-run of that job passed). |

## 2. Section 10 — Hotel PMS

| Requirement | Implementation | Database | API | Tests | Status |
|---|---|---|---|---|---|
| Ancillary / additional-charge collection (meals, minibar, laundry, spa, transfer, events, damage, fees, other) | `internal/http/operational_pms_collection.go`, `internal/hospitality/collection.go` (preserved draft, finished) | `00024_folio_collection_agreements.sql`: `folio_collection_agreements`, `folio_collection_allocations`, `folio_collection_cancellations`, `reservation_financial_requests.folio_agreement_id` | `GET/POST /folios/{id}/collection-agreements`, `POST .../{agreement_id}/release`, `POST .../{agreement_id}/readback` | `TestBusinessPMSFolioSplitPayerCollectionConservesObligations` | PASS |
| Folio collection agreements (guest, corporate, split, allocation limits, currency, approval, consent evidence, replacement, history) | Same; corporate payer requires live profile, current contract and active traveler association plus exact `COMMERCIAL_PMS_FOLIO_COLLECTION` approval and MFA | Agreement/allocation/release triggers conserve amounts, forbid accommodation re-collection, forbid reuse of accommodation financial groups, enforce single replacement | Same | Both PMS collection tests | PASS |
| Partial and mixed payments | Separate Payment intents per payer through the existing durable Payment worker (policy → locked quote → intent → receipt); folio reconcile applies each settled, funded, unreversed receipt once | `folio_payment_references` target guard | `POST /folios/{id}/reconcile` (extended) | Split-payer test: guest settles first (balance keeps corporate + accommodation), then corporate; repeated reconcile does not double-apply | PASS |
| Folio balance conservation | Responsibility view: total = accommodation + guest + corporate + unallocated − over-allocated; flags unverified mandatory payers | — | `GET /folios/{id}/collection-agreements` `responsibility` | Asserted before/after collection, settlement and release | PASS |
| Never settle with unverified mandatory payer | Folio stays `SETTLEMENT_PENDING` while any balance (including pay-at-property accommodation) lacks settled Payment evidence | — | — | Split-payer test final assertion | PASS |
| Unknown / failed / reversed outcomes | Failed collection releases only after authoritative `VERIFIED_FAILURE`; never-dispatched requests release once; reversal removes backing without rewriting history | Release guard | Release/readback routes | `TestBusinessPMSFolioCollectionReleaseReplacementAndVerifiedFailure`; reversal part of split-payer test | PASS |
| Duplicate collection prevention | One collection per agreement; repeated worker passes read back, never re-collect | `UNIQUE(financial_group_id)` + request guard | — | `collectionCalls == 2` after two worker passes | PASS |
| In-house extension (reservation + stay + stock + folio + access) | Not implemented in this pass | — | — | — | **FAIL** (internal work) |
| Early departure (release unused nights, refund/credit obligation, access revocation, RMS signal) | Not implemented in this pass | — | — | — | **FAIL** (internal work) |
| Existing PMS (assignment, check-in/out, moves, folios, cashier, night audit, day close, offline replay, groups, access, reconciliation) | Unchanged from prior certified implementation | — | — | Full Business suite (section 6) | PASS (prior scope) |

## 3. Section 11 — Guest CRM

| Requirement | Implementation | Database (00025/00026) | API | Tests | Status |
|---|---|---|---|---|---|
| Service recovery lifecycle (OPEN → … → CLOSED, REJECTED/ESCALATED/CANCELLED/REOPEN), SLA, assignment, evidence | `operational_crm_recovery.go` | `service_recovery_cases`, `service_recovery_actions` | `/service-recovery/cases…` (7 routes) | `TestBusinessServiceRecoveryApprovedRemedyAndVerifiedOutcomes` | PASS |
| Compensation with RBAC, MFA, exact digest-bound approval, SoD, verified outcome | Monetary/points remedies need `COMMERCIAL_SERVICE_RECOVERY_REMEDY`; non-monetary need a distinct approver; discount verified against the actual folio adjustment; loyalty benefit posts a ledger entry | `service_recovery_compensation_refs` with frozen terms | `/approval`, `/compensations/{id}/outcome` | Same test (403 without approval, 409 on stale digest, distinct-approver path) | PASS |
| Refund never marked complete from a request | Refund / financial adjustment / booking credit stay `PENDING_EXTERNAL_CONTRACT`; case cannot resolve until a verified outcome or recorded guest decline | Case resolution guard | — | Same test (RESOLVE 409, execute refund 409) | PASS |
| Loyalty programmes, rules, rewards (versioned, approved) | `operational_crm_loyalty.go` | `loyalty_programs`, `loyalty_rules`, `loyalty_rewards` | `/loyalty/programs…` | `TestBusinessLoyaltyAccrualRedemptionReversalAndDuplicateSafety` | PASS |
| Enrollment with consent, accrual from verified completed stays, duplicate safety, tiers | Accrual requires a departed, canonically COMPLETED stay with a closed folio; unique per stay; concurrent duplicates produce exactly one entry | `loyalty_accounts`, `loyalty_transactions` + ledger guard | `/loyalty/accounts…` | Same test (403 without consent, concurrent 201/200, tier upgrade) | PASS |
| Redemption, entitlements, no overdraft | Idempotent per request reference | `loyalty_redemptions`, `loyalty_entitlements` | `/redemptions` | Same test | PASS |
| Reversal after cancellation/refund, expiry | Reconcile reverses earnings whose stay or financial backing no longer qualifies; negative balance restricts the account | `loyalty_reversals` | `/reconcile` | Same test (Payment reversal → reversal once, `REVIEW_REQUIRED`, tier back to BASE) | PASS |
| Privacy requests (export, access, correction, erasure, restriction) | `operational_crm_privacy_lifecycle.go` | `privacy_requests` | `/guests/{id}/privacy-requests`, `/privacy-requests…` | `TestBusinessCRMPrivacyExportLegalHoldAndErasure` | PASS |
| Legal holds | Hold blocks erasure in the application and in the database | `legal_holds`, guest-profile hold trigger | `/guests/{id}/legal-holds`, `/legal-holds/{id}/release` | Same test (direct anonymisation blocked) | PASS |
| Retention / anonymisation worker | `ProcessDataRetention` on the existing worker scheduler; retains reservations, stays, folios, payment evidence, consent history and audit | `data_retention_jobs` | — | Same test (worker completion, replay no-op, irreversible) | PASS |
| VIP classifications | Append-only, evidence-backed, expiring | `vip_classifications` | `/guests/{id}/vip` | `TestBusinessCRMVIPAndSourceBackedSegments` | PASS |
| Segments and membership lifecycle | Closed source-backed criteria only (no protected characteristics); marketing eligibility follows current consent | `guest_segments`, `guest_segment_memberships` | `/guest-segments…` | Same test (withdrawal exits segment) | PASS |
| CRM reporting | `operational_crm_reports.go` with documented numerator/denominator, null on zero denominators, unknown financial fields | — | `GET /crm/reports` | Assertions in loyalty and recovery tests | PASS |
| Guest identity merge workflow | Not implemented (no automatic merge exists; email/phone-only merge remains impossible) | — | — | — | **FAIL** (internal work) |
| CRM-triggered notifications through Notification Service | Not implemented in this pass | — | — | — | **FAIL** (internal work) |

All 16 previously listed `REQUIRED_CURRENT_WORK` catalogue tables now exist, are used by routes and have tests. Catalogue: 358 decisions, 237 implemented tables, 120 future-domain deferrals, **0 required current schema gaps**.

## 4. Section 9 — Reservations

| Requirement | Status |
|---|---|
| Existing canonical lifecycle, whole-line split/merge/partial cancellation, waitlist offer/accept, source intake, overbooking, Payment collection | PASS (prior scope, full suite) |
| Folio payer agreements reuse the canonical reservation financial group and Payment worker without changing the reservation's accommodation financial state | PASS (PMS collection tests) |
| Overbooking → CRM recovery case with approved remedy and verified outcome | PASS for the CRM side (`OVERBOOKING` issue type, recovery test); automatic case creation from an overbooking permit is not wired |
| Quantity-level fragmentation (cancel/split/merge part of a line's quantity) | **FAIL** (internal work) |
| Release-triggered automatic waitlist offers and expiry advancement | **FAIL** (internal work) |
| In-house amendments (extension, early departure, partial room checkout) | **FAIL** (internal work) |

## 5. Section 8 — RMS

| Requirement | Status |
|---|---|
| Demand snapshots, pace, deterministic forecasts, manual recommendations, approvals, simulations, application, rollback | PASS (prior scope) |
| `APPROVAL_AUTOMATION` workers and `AUTOPILOT_WITH_LIMITS` bounded execution | **FAIL** (internal work; modes remain policy labels that do not authorize execution, which fails closed) |
| Advanced recommendations, forecast inputs, aggregation, simulations, drift monitoring | **FAIL** (internal work) |

## 6. Verification executed in this pass

Local workstation: Windows, disposable PostgreSQL 18 cluster on a scratch port, embedded disk-backed JetStream, no Docker and no C toolchain (so no local `-race`; Linux CI supplies race and container evidence).

- New Business tests (all PASS): `TestBusinessPMSFolioSplitPayerCollectionConservesObligations`, `TestBusinessPMSFolioCollectionReleaseReplacementAndVerifiedFailure`, `TestBusinessServiceRecoveryApprovedRemedyAndVerifiedOutcomes`, `TestBusinessLoyaltyAccrualRedemptionReversalAndDuplicateSafety`, `TestBusinessCRMPrivacyExportLegalHoldAndErasure`, `TestBusinessCRMVIPAndSourceBackedSegments`.
- New Owner test: `TestShortletLagosMidnightBusinessDate` PASS.
- Business regression subset (PMS, reservation payments, contracts, migrations, catalogue, grants, CRM history, events, overbooking) PASS except the expected schema-version pin, which was updated.
- gofmt (LF-normalised), go vet, staticcheck 2026.2.1, SQLC v1.31.1 diff, OpenAPI/Postman/route drift, event schema drift, database catalogue drift, architecture and Business ownership boundary scripts: PASS.

These are 7 new top-level integration tests, not 33 independent end-to-end journeys. The requested full cross-domain journey list (booking → PMS → CRM → RMS → distribution, RMS automation, waitlist conversion, quantity fragmentation, in-house extension/early departure) is **not** covered by this pass.

## 7. Contract inventory after this pass

| Item | Before | After |
|---|---|---|
| Goose migrations | 23 | 26 |
| Implemented tables | 217 | 237 |
| Registered method/path pairs | 446 | 481 |
| OpenAPI path templates | 322 | 351 |
| Typed OpenAPI schemas | 427 | 473 |
| Business event schemas | 81 | 89 |

New events: `hospitality.folio_collection.updated.v1`, `service_recovery.updated.v1`, `loyalty.program.updated.v1`, `loyalty.account.updated.v1`, `loyalty.transaction.recorded.v1`, `guest.privacy_request.updated.v1`, `guest.legal_hold.updated.v1`, `guest.segment.updated.v1` (published on the existing Business stream under `business.*`).

## 8. External dependencies (unchanged, not used to hide internal gaps)

Payment/Admin refund and adjustment execution (no authorized Business-to-Admin remedy API; requests stay pending), live Payment collection/settlement configuration, Notification templates/delivery, Marketplace/OTA live supply and source contracts, Verification capacity assertions, cashier attestation producer, smart-access devices, market/cost datasets, staging deployment transport, real staging database/roles/secrets and NATS identities.

**LIVE STAGING DEPLOYMENT: NOT CERTIFIED.** No VPS access, no production deployment, no change to `main`.
