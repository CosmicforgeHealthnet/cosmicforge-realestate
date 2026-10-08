# Service Authentication Standard

## Purpose

Internal service authentication is independent from end-user authentication.

## Service identity

Every deployable backend service receives:
- stable service name;
- Ed25519 private signing key;
- public key with kid;
- trusted peer key/issuer configuration.

Keys are injected through secrets management and rotated.

## Internal service JWT

Required claims:
- typ = service+jwt
- iss = calling service name
- aud = intended receiving service
- iat
- exp <= iat + 5 minutes
- jti

Optional delegation context:
- actor_domain
- actor_id
- organization_id
- original_request_id

## Authorization

Valid authentication is not sufficient authorization.

Every sensitive internal operation defines allowed callers/capabilities.

Example:
- only payment-service may execute provider money calls;
- only authorized domain services may create verification cases for supported subject types;
- notification-service accepts send requests only from registered service callers.

## Replay and audit

Receivers reject expired/invalid tokens and log service identity, request_id and trace_id.

High-risk commands use idempotency keys in addition to service authentication.
