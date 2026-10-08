# CosmicForge Real Estate — Postman

Postman is the official human-facing API documentation, manual testing, QA and frontend handoff surface for CosmicForge Real Estate.

OpenAPI 3.1 remains the internal machine-readable contract used for validation and drift detection. Swagger UI is not required.

## Import

Import:

1. `CosmicForge_RealEstate.postman_collection.json`
2. one environment from `postman/environments/`

Available environments:
- Local
- Development
- Staging

The committed Development and Staging `base_url` values are safe placeholders until real deployment URLs are available. Replace them in your local Postman environment or workspace; do not commit private credentials.

## Collection organization

```text
00 Platform
01 Marketplace
02 Individual Owner
03 Business Operations
04 Property Manager
05 Artisan
06 Professional Services
07 Admin
08 Verification
09 Payment
10 Notification
11 Media & Documents
```

Internal service routes, admin-only operations and provider webhooks must be placed in clearly named `_Internal`, `_Admin` and `_Webhooks` folders. Frontend engineers should normally work only with frontend-facing folders.

## Variables

Common variables include:
- base_url
- access_token
- refresh_token
- service_token
- domain-specific access tokens
- request_id
- idempotency_key
- verification_id
- media_id
- notification_id
- payment_id
- property_id
- listing_id
- organization_id

Foundation-block variables:
- `verification_service_url`, `media_service_url`, `notification_service_url` — direct internal
  service URLs for `_Internal` requests (local defaults `:8091`, `:8092`, `:8093`; empty for
  development/staging, where internal APIs are reachable only inside the platform network)
- `upload_url`, `checksum_sha256`, `owner_id` — signed media upload flow
- `subject_id`, `client_reference`, `sandbox_signature` — verification (sandbox provider)
- `endpoint_id`, `fcm_token`, `recipient_id` — push devices and notifications

`_Internal` requests use `service_token` (a short-lived service JWT minted by a backend
engineer); frontend requests use `access_token` through `base_url` (the API Gateway).

The collection pre-request script generates a fresh `request_id` and `idempotency_key` for each request unless a request deliberately overrides them.

## Authentication

Frontend authenticated requests use:

```text
Authorization: Bearer {{access_token}}
```

Domain-specific folders may use a domain token variable where necessary.

Internal service requests use:

```text
Authorization: Bearer {{service_token}}
```

Internal service requests must never be confused with frontend APIs.

## Endpoint completion rule

A frontend-facing endpoint is not complete until the Postman collection contains:
- request name;
- method and path;
- description;
- authentication requirements;
- headers;
- path/query parameters;
- request body where applicable;
- success example;
- relevant error examples;
- required environment variables;
- Postman tests where useful.

Do not add a Postman request before the corresponding backend endpoint exists.

## Saving returned IDs

Create operations should save useful resource IDs for later requests where appropriate.

Example:

```javascript
const body = pm.response.json();
if (body.data && body.data.verification_id) {
  pm.environment.set("verification_id", body.data.verification_id);
}
```

Use the actual response shape of the implemented endpoint.

## Media upload flow

The Media & Documents collection must represent the real signed-upload flow:

1. initiate upload through the Real Estate API;
2. receive a signed object-storage upload URL;
3. upload bytes to the signed URL;
4. finalize/complete the upload through the Real Estate API;
5. read/poll media state if necessary;
6. receive READY or REJECTED.

Do not replace this with a direct backend multipart flow unless the architecture is explicitly changed.

## Secrets

Never commit:
- passwords;
- access/refresh tokens;
- service tokens;
- JWT/service signing private keys;
- Paystack secret keys;
- Firebase service-account secrets;
- email/SMS provider secrets;
- database passwords;
- object-storage credentials.

Committed environments contain placeholders only.

## Development vs staging

Use the Development environment while backend work is being integrated on the development branch/environment.

Use Staging only after a release candidate is promoted and deployed to staging.

## Contract consistency

For every implemented frontend-facing endpoint:
1. handler exists;
2. OpenAPI contract exists;
3. Postman request exists;
4. method and path match;
5. request/response shapes match;
6. examples reflect the actual contract.

Postman is the frontend/QA handoff. OpenAPI is the machine contract.
