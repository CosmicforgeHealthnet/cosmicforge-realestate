# ADR-0010: Postman as Frontend API Handoff

**Status:** Accepted

Postman is the official human-facing API documentation, QA and frontend integration surface for CosmicForge Real Estate.

OpenAPI 3.1 remains the machine-readable contract used for validation and automation.

Swagger UI is not required.

Every frontend-facing endpoint must be added to both OpenAPI and the Postman collection as part of the same implementation change.
