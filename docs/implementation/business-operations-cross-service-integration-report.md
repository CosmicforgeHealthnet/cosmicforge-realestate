# Business Operations — cross-service integration report

Repository state inspected at baseline `52592e8`. Service implementation maturity
(Go files at baseline): business-operations 344, individual-owner 203, payment 84,
notification 38, verification 32, media-document 26; **marketplace, property-manager,
artisan, professional-services and admin had no Go code** (scaffold directories only).
CF Meet is not in this repository (`integrations/cf-meet` holds only a placeholder).

Rule kept throughout: no cross-service database access, no shared SQL, no direct
provider money movement from Business; signed service JWTs (EdDSA, audience-bound,
organisation delegation) and versioned events only.

## Integration matrix

| Owning service | Contract | Auth / audience | Idempotency / readback | State after this pass | Contract test | Real-process E2E | External configuration still required |
|---|---|---|---|---|---|---|---|
| **marketplace-service** (Category B — implemented now) | `business-supply.v1`: validate connection, apply command, readback, event poll, mapping inspection, listing readback (`contracts/internal/openapi/marketplace-service.yaml`) | caller must be `business-operations-service`, `aud: marketplace-service`, signed organisation must equal body organisation | `Idempotency-Key` = command key; exactly-once acceptance (append-only `supply_commands`); canonical payload hash verified; monotonic canonical version; authoritative readback, unknown → 404 | **Implemented** narrow owning receiver with its own `marketplace_db` (migration 00001), api + migrate binaries, Dockerfile, CI (lint, test, govulncheck, image) | `services/marketplace-service/tests/integration/supply_test.go` (`TestMarketplaceSupplyExactlyOnceAppendOnlyAndExpiry`, real PostgreSQL, concurrent duplicates) | `tests/e2e/marketplace_supply_test.go` (`TestMarketplaceBusinessSupplyContractRealProcess`: real migrate + api processes, restart recovery). Business side is represented by its signed identity, not a running business-operations process | Deployment: set Business `MARKETPLACE_SERVICE_URL` (already read by `internal/app/identity.go`) and trust keys. **Not implemented**: customer accounts/discovery/checkout producing reservation events, verified-stay reviews, funnel observations — these need Marketplace consumer product functionality; Business intake stays fail-closed |
| payment-service (Category A) | intent/collection readback, beneficiary authority | existing signed client `internal/integrations/payment` | readback-only settlement; UNKNOWN_PENDING_READBACK on lost responses | Reused. New Annex D money paths (rent, deposits, payment closing milestones) settle **only** through `financial_obligations` Payment readback — no second ledger | `s15_finance_test.go`; Annex D residential/land/sale journeys (Payment contract double) | Not run as a process with Business in this pass | Live provider credentials |
| verification-service (A) | KYB/capacity/authority verification links | existing client | signed readback | Reused; land allocation confirmation and organisation changes require a Verification reference | prior Section 3/4 tests; Annex D land and closure tests | Foundation E2E (existing) | Production KYB partner |
| notification-service (A) | encrypted notification requests via outbox | existing | outbox + delivery readback | Unchanged | `crm_notifications_test.go` | Foundation E2E (existing) | Provider activation |
| media-document-service (A) | evidence references; data-room authorization callback | existing; `/internal/v1/.../data-room-authorizations` | — | Unchanged; all new lifecycles accept Media evidence references only (no bytes) | data-room tests | Foundation E2E (existing) | — |
| property-manager-service (B) | manager authorization readback (Business-owned endpoint exists) | signed property-manager caller | — | Business side complete; **owning receiver not implemented** | `TestBusinessManagerDelegationScopedExpiringRevocableAndEnforced` | — | NEEDS_INTEGRATION: owning service scaffold |
| artisan-service (B) | provider request/quote/acceptance/completion references | — | external reference recorded per transition | Business side complete (`vendor_quote_refs`, `provider_assignment_refs`); **owning service not implemented** | Maintenance journey | — | NEEDS_INTEGRATION |
| professional-services-service (B) | appointment/findings/certification references | — | — | Business side complete (`professional_findings`, `progress_certifications`); **owning service not implemented** | Off-plan and professional tests | — | NEEDS_INTEGRATION |
| admin-service (B) | moderation/dispute/refund/high-risk review | — | references recorded on disputes/protection cases | Business side records Admin references; **owning service not implemented** | Dispute/protection tests | — | NEEDS_INTEGRATION |
| CF Meet (external repository) | business context creation/readback | — | pending contexts fail closed | Unchanged (no verified contract available in this repository) | `TestBusinessInboxCFMeetAuthorityPendingThenEstablishedWithoutMessageStorage` | — | Owning-repository contract |

## Why the four other Category B services were not built in this pass

Property-manager, artisan, professional-services and admin each need their own
account/identity domain before a receiver can authenticate their actors; building
those is the "entire unrelated service" scope the brief excludes. Their Business-side
contracts are complete and fail closed (records carry the owner's reference and cannot
be marked complete without evidence/distinct actors). This is reported as an internal
cross-service gap, not an external dependency.
