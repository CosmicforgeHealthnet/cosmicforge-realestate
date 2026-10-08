# API Standard

## Surfaces

- /api/v1/* — application/client APIs
- /internal/v1/* — service-to-service APIs
- /webhooks/v1/* — external provider callbacks

## Transport

Initial synchronous transport: HTTPS REST/JSON.

JSON property names use snake_case.

## Content type

application/json; charset=utf-8

Uploads use media-document-service signed upload flows rather than proxying large files through ordinary domain endpoints.

## Pagination

Default:
- cursor
- limit

Responses:
```json
{
  "data": [],
  "page": {
    "next_cursor": null,
    "has_more": false
  }
}
```

Limit is bounded server-side.

## Commands

Commands that create side effects return the resulting resource/state when known.

If processing is asynchronous, return an accepted operation/resource with a status that the client can query.

Do not return success before an authoritative state transition has actually occurred.

## HTTP semantics

- GET: read-only
- POST: create/command
- PUT: full replacement only where genuinely supported
- PATCH: partial update
- DELETE: deletion/revocation request according to domain rules

## Headers

Supported platform headers:
- Authorization
- X-Request-ID
- traceparent
- Idempotency-Key where required

## Versioning

Breaking contract changes require a new API version.

Adding optional response fields is not considered breaking.

## Validation

Invalid inputs return stable platform error codes and field-level details where safe.

Database errors/provider raw errors are never returned directly.
