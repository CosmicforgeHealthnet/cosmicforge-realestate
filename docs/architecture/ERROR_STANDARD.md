# Error Standard

All HTTP APIs use one error envelope.

```json
{
  "error": {
    "code": "PROPERTY_NOT_FOUND",
    "message": "Property not found.",
    "request_id": "019...",
    "details": []
  }
}
```

## Rules

- code is stable, machine-readable and SCREAMING_SNAKE_CASE;
- message is safe for the caller;
- request_id is always present;
- details may contain field validation issues;
- stack traces, SQL text and provider secrets are never exposed.

## Status mapping

- 400 INVALID_* / malformed command
- 401 AUTHENTICATION_REQUIRED / INVALID_TOKEN
- 403 PERMISSION_DENIED
- 404 *_NOT_FOUND
- 409 *_CONFLICT / IDEMPOTENCY_CONFLICT / invalid state transition
- 422 domain validation when syntactically valid but unacceptable
- 429 RATE_LIMITED
- 500 INTERNAL_ERROR
- 502 DOWNSTREAM_PROVIDER_ERROR where retry semantics are known
- 503 SERVICE_UNAVAILABLE

## Domain errors

Each service owns its domain error namespace but follows this common shape.

Financial and verification failures must distinguish:
- definite failure;
- retryable failure;
- unknown outcome requiring read-back.
