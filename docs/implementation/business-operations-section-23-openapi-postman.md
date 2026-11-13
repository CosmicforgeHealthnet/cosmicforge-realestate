# Business Operations Section 23 — OpenAPI and Postman Handoff

| Artifact | Value |
|---|---|
| Machine contract | `contracts/public/openapi/business-operations.yaml` |
| Human/frontend handoff | `postman/CosmicForge_RealEstate.postman_collection.json` → `03 Business Operations` |
| Route inventory | `services/business-operations-service/contracts/routes.json` |
| Registered method/path pairs | **812** (537 before) |
| OpenAPI path templates | **604** (397 before) |
| Typed OpenAPI schemas | **656** (558 before) |

## Generation and drift gates

* `python scripts/contracts/generate_business_operations.py --check --validate` (Contracts workflow): regenerates OpenAPI, Postman and routes.json and fails on drift; validates every request/response example against its schema.
* New handlers are contracted in `scripts/contracts/business_sections_12_24_routes.py`, derived from the Go handler bodies (request type, capability, success status, step-up) with reviewed overrides for compatibility aliases; generation fails for a registered handler without a derivable contract. Section 16 domain contracts come from the same declarative source as the migration (`business_ops_domains.py --check`).
* `TestBusinessRegisteredRoutesMatchOpenAPIAndOfficialPostman` walks the public, internal and webhook routers and requires handler = OpenAPI = Postman method/path parity.
* `python scripts/contracts/generate_business_events.py --check` and `generate_business_database_catalogue.py --check` gate event schemas and the database catalogue.

## Folder contract (Annex I)

The collection now carries all Annex I folders (00–31, `_Internal`, `_Webhooks`, `_Admin`). New families are placed in their Annex I folders (e.g. Support & Service Recovery, Reviews & Reputation, Housekeeping, Maintenance & Assets, Managers, Professional Services & Data Rooms, Residential, Commercial, Land, Sales, Development, Analytics & AI, Risk, Financing & Insurance, Smart Access, Integrations & Offline Sync). Existing routes keep their folders, except 11 privacy-request and service-recovery routes previously defaulted into `01 Organisation & Staff`, now in `27 Risk, Privacy & Disputes` and `24 Support & Service Recovery`.

Each request carries auth headers, the documented capability and MFA notes, Idempotency-Key for side effects, example bodies, status/envelope tests and ID capture into environment variables. No real credentials are included. Internal service routes are only under `_Internal`.
