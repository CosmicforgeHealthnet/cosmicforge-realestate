# API Documentation and Postman Policy

**Status:** Accepted

## Decision

Postman is the official human-facing API documentation, manual testing, QA and frontend handoff surface.

OpenAPI 3.1 remains the internal machine-readable API contract and CI validation source.

Swagger UI is not required.

## Repository artifacts

```text
postman/
├── CosmicForge_RealEstate.postman_collection.json
├── environments/
│   ├── local.postman_environment.json
│   ├── development.postman_environment.json
│   └── staging.postman_environment.json
└── README.md
```

## Completion rule

A frontend-facing endpoint is not complete until:
- backend handler exists;
- OpenAPI contract exists;
- Postman request exists;
- success example exists;
- relevant error examples exist;
- authentication and variables are documented.

Postman must not document nonexistent endpoints.

## Folder boundary

Frontend requests remain separate from:
- internal service routes;
- admin-only privileged operations;
- provider webhooks.

## Secrets

Committed Postman files contain no real credentials or tokens.

## Drift

CI should validate JSON syntax and, as API implementation matures, compare implemented public routes, OpenAPI routes and Postman routes for obvious drift.
