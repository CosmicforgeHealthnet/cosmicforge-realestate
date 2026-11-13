# Business Operations — full specification requirements matrix

Specification: `docs/specifications/CosmicForge_Business_Property_Operations_Full_Backend_Implementation_Spec_v1.0.md`
(Sections 1–27, Appendix A/B, Annex D.1–D.42, Annexes E–J). Baseline `52592e8`.
Detailed per-feature evidence: `business-operations-annex-d-01-42-closure.md`
(Annex D), `business-operations-deferred-database-resolution.md` (catalogue),
`business-operations-cross-service-integration-report.md` (integrations),
`business-operations-section-16-api-inventory.md` (every route with handler and source).

Owning service is `business-operations-service` unless stated. "Prior" means
implemented before this pass and re-run in this pass's full suite.

## Sections 1–27

| Req | Business requirement | Status | Implementation | Test evidence | External / next action |
|---|---|---|---|---|---|
| S1–S2 | Architecture, bounded contexts, DB isolation, outbox, JetStream, workers, health | PASS_VERIFIED (prior) | `internal/app`, `internal/outbox`, `cmd/worker`; boundary scripts | `outbox_test.go`, `check-business-operations-boundaries.sh` | — |
| S3 | Identity, MFA, organisation, RBAC, approvals, delegation, termination, ownership changes, closure | PASS_VERIFIED | prior + `organisation_change_cases`, `closure_cases` (00038) | auth/staff/approval tests; `TestAnnexDFXFreshnessOrganisationChangeAndClosureContinuity` | KYB partner activation |
| S4 | Enterprise inventory graph | PASS_VERIFIED (prior) | `internal/portfolio` | inventory suites | — |
| S5 | Database catalogue | PASS_VERIFIED | 399 tables; 0 deferred; 23 proven equivalences | `TestBusinessCompleteCatalogueMatchesActualPostgreSQL` | — |
| S6 | Channel Manager, Marketplace first | PASS_VERIFIED internal; Marketplace receiver implemented | `commercial_channel_*`; `services/marketplace-service` | channel suites; marketplace tests | OTA partner approval |
| S7 | Inventory and pricing | PASS_VERIFIED (prior) | `internal/inventory`, `internal/revenue` | commercial suites | — |
| S8–S11 | RMS, reservations, PMS, CRM | PASS_VERIFIED (prior) | see Sections 8–11 closure | journeys and suites | — |
| S12 | Communications and AI | PASS_VERIFIED (prior); CF Meet external | inbox, AI suggestions | s12 tests | CF Meet owning-repo contract |
| S13–S15 | Reputation, marketing, finance | PASS_VERIFIED (prior) | sections 13–15 | s13–s15 tests | Provider activation |
| S16 | Operational domains / API | PASS_VERIFIED | 71 governed domains; 995 routes | domain suites; parity gates | — |
| S17 | Integrations | PARTIAL | Category A connected; Marketplace receiver built; four Category B owning services not built | cross-service report | NEEDS_INTEGRATION: property-manager, artisan, professional-services, admin |
| S18 | Events | PASS_VERIFIED | 193 schemas (was 155); generator drift gate | harness validates every outbox event against its schema | — |
| S19 | Workers | PASS_VERIFIED | `ProcessOperations` + 17 generated date sweeps + rent schedule top-up | Annex D journeys invoke the worker and assert transitions | — |
| S20 | State machines | PASS_VERIFIED | DB guards = generated edges for all 71 domains | `TestAnnexDDatabaseGuardsMatchEveryGeneratedDomain` | — |
| S21 | Security | PASS_VERIFIED | tenant/scope RBAC, MFA, approvals, SoD, secrets refused, webhook SSRF guard, protected attributes refused | security and Annex D tests | — |
| S22 | Resilience | PASS_VERIFIED (prior) + upgrade | readback, outage recovery, restart | `TestJourney09…`, `TestAnnexDUpgradeFromPopulatedSchema37`, marketplace restart E2E | Off-server backup in real environment |
| S23 | OpenAPI / Postman | PASS_VERIFIED | regenerated | `--check --validate` | — |
| S24 | Certification | See final closure | — | — | — |
| S25 | Deployment readiness | LIVE_DEPLOYMENT_DEFERRED | images build in CI | Images workflow | Staging deployment |
| S26 | Implementation workstreams | See BO table | — | — | — |
| S27 | Competitive acceptance | PASS_VERIFIED for repository capability; competitor comparison evidence incomplete | `business-operations-section-27-competitive-acceptance.md` | — | Competitor benchmarking |

## BO-1 to BO-18

See `business-operations-bo-01-18-final-workstream-status.md` (17 complete on the
Business side; BO-18 partial).

## Annex D.1–D.42

All 42 features are PASS_VERIFIED or EQUIVALENT_IMPLEMENTATION_VERIFIED on the
Business side; partner/provider activation items are EXTERNAL_ACTIVATION_DEFERRED.
Per-feature table: `business-operations-annex-d-01-42-closure.md`.

## Annexes E–J

| Annex | Requirement | Status | Evidence |
|---|---|---|---|
| E | Required route catalogue (213 pairs) | PASS_VERIFIED | 213/213 registered, 0 missing (`generate_business_api_inventory.py --check`) |
| F | High-risk permission / approval matrix | PASS_VERIFIED | commercial approval actions incl. new `COMMERCIAL_DEPOSIT_RELEASE`, `COMMERCIAL_LEASE_TERMS`, `COMMERCIAL_SERVICE_CHARGE_BUDGET`, `COMMERCIAL_LAND_ALLOCATION`, `COMMERCIAL_PROJECT_VARIATION`, `COMMERCIAL_ORGANISATION_CHANGE`, `COMMERCIAL_BUSINESS_CLOSURE` (`internal/http/approval_policies.go`); self-approval refused in every test |
| G | Critical sequence contracts | PASS_VERIFIED for Business-internal sequences; cross-service sequences partly contract-only | `business-operations-cross-domain-e2e-certification.md` |
| H | KPI definitions | PASS_VERIFIED (prior) | `/dashboards`, `/revenue/dashboard` |
| I | Postman folder contract | PASS_VERIFIED | 995 requests in `03 Business Operations` |
| J | Final closure criteria | NOT MET in full | `business-operations-final-backend-system-closure.md` |
