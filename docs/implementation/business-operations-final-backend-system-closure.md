# Business Operations — final backend system closure (earlier pass, superseded)

> **Superseded.** This document records an earlier pass. Its verdict
> (`BACKEND_ENGINEERING_PARTIAL — INTERNAL_BLOCKERS_REMAIN`) and the
> cross-service blockers listed below were addressed by the cross-service
> mission; see [`cross-service-backend-final-closure.md`](cross-service-backend-final-closure.md)
> for the current state, the remaining items and the final CI evidence.

At the time of this pass the Business Operations service's own scope was
implemented across Sections 1–27 and Annex D.1–D.42, and the verdict stayed
partial because CosmicForge-owned cross-service work was still missing.

## Baseline and scope of this pass

- Start SHA: `52592e801be11a1ef0d1ca1ff071c85ecef73ced` (staging).
- Services changed: `business-operations-service`, `marketplace-service` (new
  implementation), shared CI/workflows, `tests/e2e`.
- Unchanged: `main`, production, all frontend/apps, Individual Owner work.

## Counts recomputed from source

| Item | Baseline record | Now |
|---|---|---|
| Business Goose migrations | 37 | **38** (`00038_annex_d_lifecycles.sql`, additive) |
| Business tables | 354 | **399** |
| Catalogue decisions | 423 (68 deferred) | 423: **400 present, 23 proven equivalences, 0 deferred** |
| Governed domains with DB state guards | 26 | **71** |
| Business API method/path pairs | 812 | **995** (OpenAPI = Postman = handlers) |
| OpenAPI paths / schemas | 604 / 656 | 742 / 656 |
| Annex E required pairs | 213 | 213 registered, 0 missing |
| Business event schemas | 155 | **193** |
| Worker date sweeps | 7 | 7 + **17 generated** + rent schedule top-up |
| marketplace-service | scaffold | migration 00001 (4 tables), 6 internal routes, api + migrate |

## Verification executed locally (PostgreSQL 16, Go race detector)

- `go test -race ./...` for business-operations-service: every package and the
  full integration suite passed except `TestBusinessCompleteCatalogueMatchesActualPostgreSQL`.
  That test did not know the new `EQUIVALENT_IMPLEMENTATION_VERIFIED` decision. It was
  extended (it now also requires each equivalence to name its proving test) and re-run
  green together with every Annex D test, the migration test and the route-parity test.
- marketplace-service: integration test (race) green; real-process E2E
  `TestMarketplaceBusinessSupplyContractRealProcess` green.
- gofmt, go vet, staticcheck 2026.2.1 clean in all changed modules; `go mod tidy`
  stable; architecture and Business boundary scripts pass; OpenAPI validation
  (`openapi-spec-validator`) passes for the Business and the new Marketplace contract;
  all generators pass `--check`.
- govulncheck: only GO-2026-6859 (net/http), caused by the local Go 1.26.10. CI pins
  1.26.11, which contains the fix.
- Not run locally: gitleaks, Trivy, Docker image builds (no Docker daemon here). These
  run in the Security and Images workflows.

GitHub Actions results for the pushed SHA are recorded in the final response of this
pass. They are not claimed here before they have run.

## Internal blockers remaining (owner · location · next action)

1. **Property-manager, artisan, professional-services and admin owning services are
   still scaffolds.** Business-side contracts are complete and fail closed.
   Owner: those services. Locations: `services/{property-manager,artisan,professional-services,admin}-service`.
   Next action: implement each service's actor identity, then the narrow receivers
   listed in `business-operations-cross-service-integration-report.md`.
2. **Marketplace customer journeys.** Nothing produces reservation, review or funnel
   events into `supply_events` yet: this needs Marketplace customer accounts, discovery
   and checkout. Owner: marketplace-service.
3. **Combined multi-process E2E.** Business, Marketplace, Payment and Verification have
   never run together as processes for journeys 1–3 and 18. Each side is tested
   against the shared contract instead (`business-operations-cross-domain-e2e-certification.md`).
   Next action: add business-operations-service to `tests/e2e` components and drive
   publication through its HTTP API.
4. **CF Meet** lives in another repository. Business contexts stay pending and fail
   closed until an owning-repository contract is available.

## External activation / live deployment (not internal blockers)

Live payment provider; production KYB/verification partners; Booking.com and Airbnb
partner approval; SMS/email/WhatsApp providers; physical smart-lock certification;
financing and insurance partners; licensed market data; e-signature, accounting and
POS providers; staging/production deployment, secrets and off-server backup/restore.
No live staging deployment was performed.

## Report index

- `business-operations-full-spec-requirements-matrix.md`
- `business-operations-annex-d-01-42-closure.md`
- `business-operations-deferred-database-resolution.md`
- `business-operations-cross-service-integration-report.md`
- `business-operations-complete-api-audit.md`
- `business-operations-cross-domain-e2e-certification.md`
- `business-operations-section-27-competitive-acceptance.md`
- `business-operations-bo-01-18-final-workstream-status.md`
- Updated: database catalogue (md/json), API inventory (md/json), Sections 18/19/20
  addenda, staging readiness runbook, OpenAPI, Postman, event schemas.
