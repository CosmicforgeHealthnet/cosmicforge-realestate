# Business Operations Section 16 — Complete API Inventory

Machine-readable inventory: `business-operations-section-16-api-inventory.json` (method, path, group, capability, step-up, idempotency, handler, source location, Annex E flag). Generated from `contracts/routes.json` (itself generated from the registered Go routers and checked by `TestBusinessRegisteredRoutesMatchOpenAPIAndOfficialPostman`).

* Registered method/path pairs: **812** (OpenAPI = Postman = handlers; parity test).
* Annex E required pairs: **213**; registered: **213**; missing: **0**.
* Duplicate method/path pairs: **0**.
* Internal routes stay on `/internal/v1/business-supply/*` (signed service identity + caller allowlist); provider callbacks on `/webhooks/v1/business-channels/*`; neither is mounted on the browser router.

| API group | Routes |
|---|---|
| Analytics/AI/automation | 17 |
| Auth/organisation/staff/roles/approvals | 96 |
| CRM/marketing | 106 |
| Channels | 26 |
| Development | 20 |
| Finance/insurance/access/integrations | 70 |
| Housekeeping/maintenance | 18 |
| Internal (service-only) | 9 |
| Inventory/rates | 51 |
| Listings/content | 17 |
| Managers/professionals | 14 |
| Portfolio/property/authority | 98 |
| Reservations/front desk/folio/night audit | 135 |
| Residential/commercial/land/sales | 29 |
| Revenue | 47 |
| Risk/privacy | 14 |
| Support/reputation/communications | 44 |
| Webhooks (provider callbacks) | 1 |

## Annex E compatibility routes

These Annex E paths delegate to the single owning handler (no second implementation):

| Annex E route | Owning behaviour |
|---|---|
| `POST /revenue/forecast`, `POST /revenue/simulations` | `rmsForecastCreate`, `rmsScenarioSimulate` |
| `POST /revenue/recommendations/{id}/approve|override` | `rmsRecommendationDecide` with the action fixed |
| `GET /revenue/dashboard`, `GET /dashboards` | Annex H KPI computation (`commandCenterKPIs`) |
| `POST /reservations/{id}/extend` | active stay → `pmsStayExtend` (Section 10) |
| `POST /folios` | returns the check-in folio of a stay; never fabricates a detached folio |
| `GET/POST /service-recovery` | `crmRecoveryCases`, `crmRecoveryCaseCreate` |
| `POST /privacy-requests` | `crmPrivacyRequestCreate` with `guest_id` |
| `GET/PUT /tax-policy` | tax profile list / new approved version |
| `GET/POST /corporate-rates` | corporate negotiated contracts (Section 11 authority) |
| `POST /offline-sync`, `GET /reconciliation-tasks`, `POST /reconciliation-tasks/{id}/resolve` | Section 10 offline replay handlers |
| `GET/POST /authority`, `POST /authority/{id}/verification` | Verification-service-linked inventory authority |
| `POST /listings/{id}/preview|submit-approval`, `GET /listings/{id}/quality` | listing readiness / submit handlers |
| `GET/POST /projects`, `/projects/{id}/phases` | canonical development / phase inventory graph |

## Governed operational domains

26 Section 16 domains are generated from `scripts/contracts/business_ops_domains.py` (states, database-enforced edges, capabilities, MFA, exact approvals, evidence, separation of duties, events) and executed by `internal/http/operational_domains.go` with domain hooks in `operational_domains_hooks.go`. Each exposes list/create/get/transitions (+PATCH, /revoke, /approve where Annex E requires).

## Explicit limitations

* Sub-ledgers listed in Section 5 for residential/commercial/land/sales (for example `rent_obligations`, `deposit_obligations`, `notices`, `move_out_cases`, `commercial_lois`, `closing_milestones`) are not separate tables; their lifecycle is carried by the governed records and Section 15 obligations. The database catalogue lists them as `DEFERRED_FUTURE_DOMAIN` (68 tables).
* Artisan, property-manager, professional-services and admin services are empty scaffolds in this repository; Business Operations records their references and exposes enforcement endpoints, but cannot certify their execution.
