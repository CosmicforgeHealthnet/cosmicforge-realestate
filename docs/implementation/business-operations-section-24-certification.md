# Business Operations Section 24 — CI and Certification Gates

Baseline SHA: `8088a9eb837816c5f43b7ed298b2601f86cd3190`. Branch: `staging` only. Exact-SHA CI evidence is recorded in `business-operations-sections-12-24-final-closure.md` (the commit that adds the evidence is itself verified on all five workflows).

## Gate mapping

| Section 24 gate | Workflow / job | Command | Local result before push |
|---|---|---|---|
| Format | Backend → lint | `gofmt -l` | clean |
| Vet | Backend → lint | `go vet ./...` | clean |
| Static analysis | Backend → lint | staticcheck | clean |
| Architecture & boundaries | Backend → lint; Architecture structure | `check-architecture.sh`, `check-business-operations-boundaries.sh` | OK |
| SQLC drift | Backend → sqlc | `sqlc diff -f sql/sqlc.yaml` | no diff |
| Migrations from empty DB, re-run no-op, version 37 | Backend → test | `TestOutboxMigrationFromEmptyDatabase` and upgrade tests | PASS |
| Unit + integration + race | Backend → test (business module) | `go test -race -json -timeout=45m ./...` | full non-race local suite PASS after fixes; race suite CI success on `d28c74c` (run 43395612679) |
| End-to-end processes | Backend → e2e | `tests/e2e` | CI |
| OpenAPI/Postman/routes drift and example validation | Contracts | `generate_business_operations.py --check --validate` | OK (812 routes) |
| Event schema drift | Contracts | `generate_business_events.py --check` | OK (155 schemas) |
| Database catalogue drift | Contracts | `generate_business_database_catalogue.py --check` | OK |
| Section 16 generator drift | local/Backend tests | `business_ops_domains.py --check` | OK |
| OpenAPI validity | Contracts | `openapi-spec-validator` (all documents) | OK — fixed duplicate operationIds for `/access/grants` vs `/access-grants` |
| JSON Schema metaschema | Contracts | `check-jsonschema --check-metaschema` | OK |
| Secrets | Security | gitleaks (git + dir) | CI success (run 43395612724) |
| Vulnerabilities | Security | govulncheck | GO-2026-6859 found on go1.26.10 → toolchain moved to go1.26.11; CI success (run 43395612724) |
| Images | Images | build, smoke, business container smoke, Trivy, digest | CI success (run 43395612835); digest `sha256:cb804a4a095af49d2b35060488a3b08285569abbdf90e3195ad1c8e72894a7a1` |

## Local execution evidence

* Full `go test ./... -count=1` with `CF_REQUIRE_INTEGRATION=1` against real PostgreSQL: all unit packages passed; integration package (219 top-level tests at the time; 220 with the J26 test added afterwards) ran 810 s with two failures, both version pins advanced legitimately by this delivery:
  * `TestOutboxMigrationFromEmptyDatabase` expected Goose version 30 → now 37.
  * `TestBusinessReservationMigrationUpgradesOnlyUnrestrictedInitialOwner` expected Owner role version 4 → now 5 because migration 00032 upgrades the unrestricted Owner once more. The test now also asserts the immutable `SECTIONS1224_OWNER_CAPABILITY_MIGRATION` audit row; restricted/blocked owners and limited staff still gain nothing.
  Both rerun and passed. No test was skipped, disabled or given a longer timeout.
* Journeys J12, J24, J31 (new file) and the J26 commercial-deal/land test passed; J24 initially failed on a test decoding bug (list items are bare records) — fixed in the test, behaviour unchanged.

## Not certified here

* Live staging deployment, backups/off-server restore, provider sandboxes (messaging, OTA reviews, CF Meet, Marketplace storefront readback, Payment payables execution, smart-lock devices). **LIVE STAGING: NOT CERTIFIED.**
