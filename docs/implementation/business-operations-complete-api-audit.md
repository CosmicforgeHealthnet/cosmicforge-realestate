# Business Operations — complete API audit (Section 16 / Annex E)

Source of truth: the registered Go routers → `services/business-operations-service/contracts/routes.json`
→ `contracts/public/openapi/business-operations.yaml` → official Postman folder
`03 Business Operations` in `postman/CosmicForge_RealEstate.postman_collection.json`.
Inventory with handler and `file:line` for every route:
`business-operations-section-16-api-inventory.{md,json}`.

## Counts (recomputed from source after this pass)

| Item | Baseline record | Now |
|---|---|---|
| Registered method/path pairs | 812 | **995** |
| OpenAPI paths / operations | 604 / 812 | **742 / 995** |
| OpenAPI schemas | 656 | 656 (new routes reuse the governed `OpsRecord*` schemas) |
| Postman Business requests | 812 | **995** |
| Annex E required pairs registered | 213 | **213 / 213** (0 missing) |
| Duplicate method/path pairs | 0 | 0 |
| Governed generated domains | 26 | **71** |

Parity gates (all executed for this pass):
`generate_business_operations.py --check --validate` (handlers = OpenAPI = Postman,
examples validate against schemas), `generate_business_api_inventory.py --check`,
`business_ops_domains.py --check`, and `TestBusinessRegisteredRoutesMatchOpenAPIAndOfficialPostman`
in the integration suite.

## What the 183 new routes are

45 new governed domains × (list, create, get, transitions) = 180, plus 1 extra
`/revoke` (`/integrations/{id}/credentials/{record_id}/revoke`) and 2 derived reads:
`GET /residential/arrears` and `GET /assets/{id}/service-history`.

Each governed route:
- requires the domain read/write capability on the exact scope (property, parcel,
  development or company), with tenant isolation (cross-tenant → 403/404);
- enforces kind/required fields on create and the transition table on `transitions`;
- requires `Idempotency-Key` on commands and `version` on transitions (409 on drift);
- applies recent MFA, exact independent approvals (bound to record id, action,
  version and authoritative amount) and separation of duties where declared;
- records immutable history, audit and an outbox event in the same transaction;
- is backed by a database state guard (direct SQL cannot bypass transitions).

No route returns fabricated success: provider/partner outcomes are recorded only from
supplied references or Payment/Verification readback; unknown outcomes stay unknown.

## Known API-level limitations (truthful)

- New lifecycles share the generic `OpsRecordCreate` / `OpsRecordTransition` request
  schemas; domain-specific attribute keys are documented in each operation's
  description (generated notes) and enforced server-side, but are not separate typed
  OpenAPI schemas.
- `agreements` uses the `residential:*` capability for all agreement kinds
  (commercial/land agreements included); a finer split was not introduced to avoid
  changing role templates in this pass.
