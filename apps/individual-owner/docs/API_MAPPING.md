# API handoff · integration NOT_STARTED

Inspected against `ee3b8bdfd15f9805a615f10948eac89481bee0b3` (origin/development at implementation start). No API requests were executed. All current repositories are local mocks. Presentation models and fictional `demo-*` IDs are not wire DTOs or persistent UUIDv7 IDs.

## Authoritative files (repository-relative)

| Reference | Path | Confirmed |
|---|---|---|
| Official collection | `postman/CosmicForge_RealEstate.postman_collection.json` | Exists; use public requests in `02 Individual Owner` |
| Postman usage | `postman/README.md` | Exists |
| Owner accounts/auth | `contracts/public/openapi/individual-owner.yaml` | Exists |
| Owner business domain | `contracts/public/openapi/individual-owner-domain.yaml` | Exists |
| Gateway | `contracts/public/openapi/api-gateway.yaml` | Exists |
| Payment authority | `contracts/public/openapi/payments.yaml` | Exists |
| Verification authority | `contracts/public/openapi/verification.yaml` | Exists |
| Media/document authority | `contracts/public/openapi/media.yaml` | Exists |
| Notification authority | `contracts/public/openapi/notifications.yaml` | Exists |

## Future mapping candidates; documentation inspection only

| Current surface | Public contract candidate | Operation | Integration status |
|---|---|---|---|
| Foundation home | `GET /api/v1/owner/dashboard` | `getDashboard`, owner-domain `DashboardView` | NOT_STARTED |
| Planned dashboard preferences | `PUT /api/v1/owner/dashboard/preferences` | `putDashboardPreferences`; Idempotency-Key required | NOT_STARTED |
| Planned property directory | `GET /api/v1/owner/properties` | `listProperties` | NOT_STARTED |

The session-only workspace label and task checklist are demonstration behavior. No server operation is assumed to support their exact shape. A later adapter must inspect schemas and allowed commands; do not blindly map these presentation methods to guessed URLs. No HTTP adapter is implemented or enabled; Dio is installed for later use and is not instantiated.

Future integration must pin the contracts again, generate DTOs, map request/response envelopes, permissions, step-up, idempotency and If-Match/version guards, and verify denied/pending/restricted/unavailable states. Exclude `_Admin`, `_Internal`, service tokens and provider webhooks from frontend operations. No direct database access.

The owner service owns domain records. Payment, Verification, Media & Document, Notification and CF Meet retain their separate authorities. No frontend can issue a real verification, legal signature, settlement, device action or message during this phase. Integration remains gated on completion, testing, review and explicit approval of the full 30-module mock.
